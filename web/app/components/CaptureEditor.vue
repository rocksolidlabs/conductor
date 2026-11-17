<script setup lang="ts">
import type { Component } from 'vue'
import type { CaptureAnswer, CaptureFrom, CaptureRect, CaptureSource, Observed, StoredElement } from '~/utils/capture'
import { RESTLESS_WORDS } from '~/utils/capture'
import type { Draft, PreviewSession } from '~/composables/usePreviewBridge'
import { captureBody, editOf } from '~/composables/useCaptures'
import { canvasEncoder, encodeShot } from '~/utils/screenshot'
import { CommandRefused, mintCommandId } from '~/utils/tasks'

/**
 * The capture editor (docs/plans/browser-evidence.md, "The editor"; "What a person sees", "Selecting" and "Saving"): beside the frame on
 * a wide screen, the whole page on a phone. Its drafts (each one moment: its screenshot with each annotation's box drawn over it and
 * numbered, never burnt in) and the captures saved from them, one shown at a time. A draft takes Select another element, Select by
 * selector, Draw a region, the notes, the order, removal and Save; a saved capture takes the notes, the order, removal and Draw a
 * region, saved as a new revision (Save changes), and offers its sends (the Send slot, CaptureSend.vue once it is there) and Capture
 * again; a selection after a save starts a new capture, never joining a saved one.
 */
const props = defineProps<{
  session: PreviewSession
  /** What the captures are of: the attempt (and its checkout) or the link. */
  from: CaptureFrom | null
  /** The run's own ticket, which the sends offer first. */
  runTicket?: string
  /** The attempt's session, where a found source opens. */
  attemptSession?: string
  /** Whether the person may select in the page now (the bridge answers). */
  canSelect: boolean
  /** Capture again is offered on a saved capture of an attempt (a candidate's checkout to retake it in). */
  again?: boolean
}>()
const emit = defineEmits<{ close: []; again: [capture: CaptureAnswer] }>()

const captures = useCaptures()
const desktop = typeof window !== 'undefined' && 'conductorDesktop' in window

/** A saved capture as the editor holds it: the server's answer, its screenshot's object URL, the annotations as edited, unsaved. */
interface EditAnnotation {
  key: number
  k: number
  kind: 'element' | 'region'
  note: string
  rect: CaptureRect
  element?: StoredElement
  sources?: CaptureSource[]
  observed?: Observed
}
interface Saved {
  capture: CaptureAnswer
  image: string
  edits: EditAnnotation[]
  dirty: boolean
  error: string
  saving: boolean
}
const saved = ref<Saved[]>([])
const selected = ref('')
const saving = ref(false)
const error = ref('')
let keys = 0

const drafts = computed(() => props.session.state.drafts)
const draft = computed<Draft | null>(() => drafts.value.find((d) => `d:${d.id}` === selected.value) ?? null)
const kept = computed<Saved | null>(() => saved.value.find((s) => `s:${s.capture.id}` === selected.value) ?? null)

// The newest draft is shown as it arrives.
watch(
  () => drafts.value.map((d) => d.id).join(','),
  (now, was) => {
    const ids = drafts.value.map((d) => d.id)
    const before = new Set((was ?? '').split(',').filter(Boolean).map(Number))
    const fresh = ids.filter((id) => !before.has(id))
    if (fresh.length) selected.value = `d:${fresh[fresh.length - 1]}`
    else if (!draft.value && !kept.value) selected.value = ids.length ? `d:${ids[ids.length - 1]}` : saved.value.length ? `s:${saved.value[saved.value.length - 1]!.capture.id}` : ''
  },
  { immediate: true },
)

// ---- The screenshot and its boxes ----

const canvas = ref<HTMLCanvasElement | null>(null)
watch(
  [canvas, draft],
  ([c, d]) => {
    if (!c || !d?.shot) return
    c.width = d.shot.width
    c.height = d.shot.height
    c.getContext('2d')?.putImageData(new ImageData(new Uint8ClampedArray(d.shot.data), d.shot.width, d.shot.height), 0, 0)
  },
  { flush: 'post' },
)
const size = computed(() => {
  if (draft.value?.shot) return { width: draft.value.shot.width, height: draft.value.shot.height }
  const img = kept.value?.capture.image
  return img?.width && img.height ? { width: img.width, height: img.height } : null
})
const boxes = computed<CaptureRect[]>(() => (draft.value ? draft.value.annotations.map((a) => a.rect) : (kept.value?.edits.map((a) => a.rect) ?? [])))
function boxStyle(r: CaptureRect) {
  const s = size.value
  if (!s) return {}
  return { left: `${(r.x / s.width) * 100}%`, top: `${(r.y / s.height) * 100}%`, width: `${(r.width / s.width) * 100}%`, height: `${(r.height / s.height) * 100}%` }
}

// Draw a region: a drag on the screenshot, in its pixels.
const drawing = ref(false)
const dragFrom = ref<{ x: number; y: number } | null>(null)
const dragAt = ref<{ x: number; y: number } | null>(null)
function pointOf(e: PointerEvent): { x: number; y: number } | null {
  const s = size.value
  const el = e.currentTarget as HTMLElement | null
  if (!s || !el) return null
  const r = el.getBoundingClientRect()
  if (!r.width || !r.height) return null
  return { x: Math.min(s.width, Math.max(0, ((e.clientX - r.left) / r.width) * s.width)), y: Math.min(s.height, Math.max(0, ((e.clientY - r.top) / r.height) * s.height)) }
}
const dragRect = computed<CaptureRect | null>(() => {
  const a = dragFrom.value
  const b = dragAt.value
  if (!a || !b) return null
  return { x: Math.min(a.x, b.x), y: Math.min(a.y, b.y), width: Math.abs(a.x - b.x), height: Math.abs(a.y - b.y) }
})
function down(e: PointerEvent) {
  if (!drawing.value) return
  ;(e.currentTarget as HTMLElement).setPointerCapture?.(e.pointerId)
  dragFrom.value = dragAt.value = pointOf(e)
}
function move(e: PointerEvent) {
  if (dragFrom.value) dragAt.value = pointOf(e)
}
function up() {
  const r = dragRect.value
  dragFrom.value = dragAt.value = null
  if (!r || r.width < 2 || r.height < 2) return
  drawing.value = false
  if (draft.value) props.session.addRegion(draft.value, r)
  else if (kept.value) {
    kept.value.edits.push({ key: ++keys, k: 0, kind: 'region', note: '', rect: r })
    kept.value.dirty = true
  }
}

// ---- A draft's annotations ----

function swap<T>(list: T[], i: number, j: number) {
  if (j < 0 || j >= list.length) return
  const a = list[i]!
  list[i] = list[j]!
  list[j] = a
}
function moveIn(list: Array<unknown>, i: number, by: number, s?: Saved) {
  swap(list, i, i + by)
  if (s) s.dirty = true
}
function removeIn(list: Array<unknown>, i: number, s?: Saved) {
  list.splice(i, 1)
  if (s) s.dirty = true
}

const selector = ref('')
function describe() {
  const s = selector.value.trim()
  if (s) void props.session.describe(s)
}

// ---- Save ----

/** A refusal's or a failure's words, the server's own. */
function failure(e: unknown): string {
  if (e instanceof CommandRefused) return e.message
  return (e as Error).message || 'the capture could not be saved'
}

async function load(id: string): Promise<Saved> {
  const capture = await captures.read(id)
  let image = ''
  if (capture.image?.sha256) {
    try {
      image = URL.createObjectURL(await captures.image(id))
    } catch {
      image = ''
    }
  }
  return {
    capture,
    image,
    edits: (capture.annotations ?? []).map((a) => ({ key: ++keys, k: a.k, kind: a.kind, note: a.note, rect: a.rect, element: a.element, sources: a.sources, observed: a.observed })),
    dirty: false,
    error: '',
    saving: false,
  }
}

async function save(d: Draft) {
  if (!props.from || saving.value) return
  saving.value = true
  error.value = ''
  try {
    const shot = d.shot ? await encodeShot(d.shot, canvasEncoder()) : { why: d.why || 'no screenshot could be taken' }
    const body = captureBody(d, { opId: mintCommandId(), from: props.from, via: desktop ? 'desktop' : 'web', shot })
    const reply = await captures.create(body, 'blob' in shot ? shot.blob : null)
    const s = await load(reply.capture.id)
    saved.value.push(s)
    props.session.drop(d)
    selected.value = `s:${s.capture.id}`
    // The screenshot source ends at Save (the tab's track stopped); the next capture asks again.
    void props.session.release()
  } catch (e) {
    error.value = failure(e)
  } finally {
    saving.value = false
  }
}

async function saveChanges(s: Saved) {
  if (s.saving) return
  s.saving = true
  s.error = ''
  try {
    await captures.edit(s.capture.id, s.capture.rev, editOf(s.edits))
    const fresh = await load(s.capture.id)
    if (s.image) URL.revokeObjectURL(s.image)
    Object.assign(s, fresh)
  } catch (e) {
    s.error = failure(e)
  } finally {
    s.saving = false
  }
}

async function reload(s: Saved) {
  try {
    const fresh = await load(s.capture.id)
    if (s.image) URL.revokeObjectURL(s.image)
    Object.assign(s, { ...fresh, edits: s.dirty ? s.edits : fresh.edits, dirty: s.dirty })
  } catch (e) {
    s.error = failure(e)
  }
}

onBeforeUnmount(() => {
  for (const s of saved.value) if (s.image) URL.revokeObjectURL(s.image)
})

// The sends' slot: CaptureSend.vue (the sends' component) once it is there.
const sendModules = import.meta.glob<{ default: Component }>('./CaptureSend.vue')
const CaptureSend = sendModules['./CaptureSend.vue'] ? defineAsyncComponent(sendModules['./CaptureSend.vue']) : null

const label = (d: Draft) => `Capture ${drafts.value.indexOf(d) + 1 + saved.value.length}`
const refusal = computed(() => props.session.state.refusal)
</script>

<template>
  <section class="flex min-h-0 min-w-0 flex-col gap-3 overflow-y-auto p-3" data-capture-editor>
    <div class="flex min-w-0 items-center gap-2">
      <h2 class="min-w-0 flex-1 truncate text-sm font-semibold text-highlighted">Captures</h2>
      <IconButton icon="i-lucide-x" size="xs" color="neutral" variant="ghost" label="Close the editor" data-capture-close @click="emit('close')" />
    </div>
    <!-- The drafts and the captures saved, one shown at a time. -->
    <div v-if="drafts.length + saved.length > 1" class="flex flex-wrap gap-1" data-capture-items>
      <UButton
        v-for="d in drafts"
        :key="`d:${d.id}`"
        :label="label(d)"
        size="xs"
        color="neutral"
        :variant="selected === `d:${d.id}` ? 'soft' : 'ghost'"
        :data-capture-draft-tab="d.id"
        @click="selected = `d:${d.id}`"
      />
      <UButton
        v-for="(s, i) in saved"
        :key="`s:${s.capture.id}`"
        :label="`Saved ${i + 1}`"
        size="xs"
        color="neutral"
        :variant="selected === `s:${s.capture.id}` ? 'soft' : 'ghost'"
        :data-capture-saved-tab="s.capture.id"
        @click="selected = `s:${s.capture.id}`"
      />
    </div>
    <UAlert v-if="refusal" color="neutral" variant="subtle" icon="i-lucide-circle-alert" :title="refusal" :data-capture-refusal="refusal === RESTLESS_WORDS ? 'restless' : ''" />
    <p v-if="!draft && !kept" class="text-sm text-muted">Select an element in the page, or Capture again, to start a capture.</p>

    <!-- The screenshot, its boxes drawn over it and numbered. -->
    <template v-if="draft || kept">
      <div class="flex min-w-0 flex-col gap-1" :data-capture-draft="draft ? draft.id : undefined" :data-capture-saved="kept ? kept.capture.id : undefined" :data-capture-url="draft ? draft.moment.url : kept!.capture.url">
        <p v-for="w in draft?.words ?? []" :key="w" class="text-xs text-default" data-capture-changed>{{ w }}</p>
        <code class="min-w-0 break-all text-xs text-muted">{{ draft ? draft.moment.url : kept!.capture.url }}</code>
        <div v-if="size" class="relative w-full select-none overflow-hidden rounded-md border border-default" :class="drawing && 'cursor-crosshair'" data-capture-shot @pointerdown="down" @pointermove="move" @pointerup="up">
          <canvas v-if="draft" ref="canvas" class="block h-auto w-full" />
          <img v-else-if="kept?.image" :src="kept.image" alt="The capture's screenshot" class="block h-auto w-full" />
          <div v-for="(r, i) in boxes" :key="i" class="pointer-events-none absolute border-2 border-primary bg-primary/10" :style="boxStyle(r)" :data-capture-box="i + 1">
            <span class="absolute -left-0.5 -top-5 rounded bg-primary px-1 text-[10px] font-semibold text-inverted">{{ i + 1 }}</span>
          </div>
          <div v-if="dragRect" class="pointer-events-none absolute border-2 border-dashed border-primary" :style="boxStyle(dragRect)" />
        </div>
        <p v-else class="text-xs text-muted" data-capture-noshot>{{ draft ? draft.why : kept!.capture.image.why || 'No screenshot.' }}</p>
        <p v-if="kept?.capture.origin.drift" class="text-xs text-default" data-capture-drift>The worktree changed between the selection and the save.</p>
        <p v-if="kept && kept.capture.origin.serving.state !== 'declared'" class="text-xs text-muted" :data-capture-serving="kept.capture.origin.serving.state">
          {{ kept.capture.origin.serving.state === 'stale' ? `The page declares ${kept.capture.origin.serving.page}, another revision than the tree's.` : 'What the page shows is not confirmed against the tree.' }}
        </p>
      </div>

      <div class="flex flex-wrap items-center gap-1.5">
        <template v-if="draft">
          <UButton label="Select another element" icon="i-lucide-mouse-pointer-click" size="xs" color="neutral" variant="outline" :disabled="!canSelect || !!session.state.busy" data-capture-select @click="session.select()" />
        </template>
        <UButton v-else-if="again" label="Capture again" icon="i-lucide-repeat" size="xs" color="neutral" variant="outline" data-capture-again @click="emit('again', kept!.capture)" />
        <UButton :label="drawing ? 'Drawing a region…' : 'Draw a region'" icon="i-lucide-square-dashed" size="xs" color="neutral" :variant="drawing ? 'soft' : 'outline'" :disabled="!size" data-capture-region @click="drawing = !drawing" />
      </div>
      <div v-if="draft" class="flex min-w-0 items-center gap-1.5">
        <UInput v-model="selector" size="xs" placeholder="Select by selector: button.save" class="min-w-0 flex-1" :disabled="!canSelect" data-capture-selector @keydown.enter.prevent="describe" />
        <UButton label="Select" size="xs" color="neutral" variant="outline" :disabled="!canSelect || !selector.trim() || !!session.state.busy" data-capture-describe @click="describe" />
      </div>

      <ol v-if="draft" class="flex flex-col gap-2" data-capture-annotations>
        <CaptureAnnotation
          v-for="(a, i) in draft.annotations"
          :key="a.key"
          :n="i + 1"
          :kind="a.kind"
          :note="a.note"
          :element="a.element"
          :observed="a.observed"
          :words="a.words"
          :session="attemptSession"
          :first="i === 0"
          :last="i === draft.annotations.length - 1"
          @update:note="(v: string) => (a.note = v)"
          @up="moveIn(draft.annotations, i, -1)"
          @down="moveIn(draft.annotations, i, 1)"
          @remove="removeIn(draft.annotations, i)"
        />
      </ol>
      <ol v-else-if="kept" class="flex flex-col gap-2" data-capture-annotations>
        <CaptureAnnotation
          v-for="(a, i) in kept.edits"
          :key="a.key"
          :n="i + 1"
          :kind="a.kind"
          :note="a.note"
          :element="a.element"
          :sources="a.element ? (a.sources ?? []) : undefined"
          :observed="a.observed"
          :session="attemptSession"
          :first="i === 0"
          :last="i === kept.edits.length - 1"
          @update:note="(v: string) => ((a.note = v), (kept!.dirty = true))"
          @up="moveIn(kept.edits, i, -1, kept)"
          @down="moveIn(kept.edits, i, 1, kept)"
          @remove="removeIn(kept.edits, i, kept)"
        />
      </ol>

      <UAlert v-if="draft && error" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="error" data-capture-error />
      <UAlert v-if="kept?.error" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="kept.error" data-capture-error />
      <div class="flex flex-wrap items-center justify-end gap-2">
        <UButton v-if="draft" label="Discard" size="sm" color="neutral" variant="ghost" data-capture-discard @click="session.drop(draft)" />
        <UButton v-if="draft" label="Save" icon="i-lucide-save" size="sm" :loading="saving" :disabled="!from" data-capture-save @click="save(draft)" />
        <UButton v-else-if="kept" label="Save changes" icon="i-lucide-save" size="sm" :loading="kept.saving" :disabled="!kept.dirty" data-capture-save-changes @click="saveChanges(kept)" />
      </div>
      <p v-if="kept" class="text-xs text-muted" data-capture-rev>
        Saved as <span class="font-mono" data-mono="id">{{ kept.capture.id }}</span> r{{ kept.capture.rev }}
      </p>
      <component :is="CaptureSend" v-if="kept && CaptureSend" :key="kept.capture.id" :capture="kept.capture" :run-ticket="runTicket" :dirty="kept.dirty" @sent="reload(kept)" />
    </template>
  </section>
</template>
