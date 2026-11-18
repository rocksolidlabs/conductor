<script setup lang="ts">
import type { CaptureAnswer, CaptureFrom, ViewportMode } from '~/utils/capture'
import { DEVICE_SIZES, NO_BRIDGE_WORDS, SELF_WORDS, isWorkbench, labelWords, originOf, retakeAddress } from '~/utils/capture'
import { previewSeam, usePreviewBridge } from '~/composables/usePreviewBridge'
import { type LabelTarget, usePreviewLabel } from '~/composables/usePreviewLabel'
import { screenshotSource } from '~/utils/screenshot'
import { desktopPreviewBridge } from '~/utils/screenshotDesktop'

/**
 * A preview in Conductor (docs/plans/browser-evidence.md, "A preview in Conductor" and "The pane"): the frame of an attempt's
 * preview (its label's admin address, the tree's secret put in, which only the frame and New tab are given) or of a link, the
 * revision label above it (read every 5 s and at every run event, neutral ink), the device mode (Fit the pane, or Desktop 1280 × 800,
 * Tablet 820 × 1180, Phone 390 × 844: the frame that many CSS pixels, scaled down to fit), Refresh, New tab and Capture, which puts
 * the page into selection mode through the bridge (usePreviewBridge) and opens the capture editor over the frame's edge. The frame keeps
 * the page's own origin and its scripts (`allow-scripts allow-same-origin allow-forms allow-popups`, as the session's link tab
 * always had), sends no referrer, and is never the workbench itself, under any spelling of its address.
 */
const props = defineProps<{
  /** An attempt's preview: its run, task, attempt and, for a candidate's checkout, `seq`. */
  target?: LabelTarget | null
  /** A link's preview (a session's link tab): no label, no revision. */
  link?: string
  /** The link tab's own header holds Refresh and New tab: the pane draws the label, the mode and Capture alone. */
  compact?: boolean
  /** The run's revision: a move reads the label again (its run events bring it). */
  revision?: unknown
  runTicket?: string
  attemptSession?: string
  /** A capture to offer Capture again of on this preview (the preview page's `&retake=`). */
  retake?: string
  /** A candidate's checkout's state as the run reads it, for the words while its label is not offered yet. */
  checkout?: 'resolved' | 'ready' | 'ending' | ''
  /** Counts the link tab's Refresh: each one loads the frame again, as the pane's own Refresh does, the drafts kept. */
  reloads?: number
}>()
const emit = defineEmits<{ label: [words: string] }>()

const labels = usePreviewLabel(
  () => props.target ?? null,
  () => props.revision,
)
const phone = useIsPhone()

/** The preview's address: the label's admin URL once read (kept until the label names another), or the link. */
const src = ref('')
/** Where the frame was sent on this preview's origin (Capture again), '' for the preview's own address. */
const moved = ref('')
watch(
  () => (props.target ? (labels.label.value?.url ?? '') : (props.link ?? '')),
  (u) => {
    if (u && u !== src.value) src.value = u
    if (!props.target) src.value = u
    moved.value = ''
  },
  { immediate: true },
)
/** The frame's address: Capture again's on this preview's origin, else the preview's. */
const address = computed(() => moved.value || src.value)
const workbench = typeof location === 'undefined' ? '' : location.origin
const self = computed(() => !!src.value && isWorkbench(src.value, workbench))
const framed = computed(() => !!src.value && !self.value && !!originOf(src.value))
const words = computed(() => labelWords(labels.label.value, props.target ? { task: props.target.task, attempt: props.target.attempt } : null))
watch(words, (w) => emit('label', w), { immediate: true })

// ---- The frame's size and scale ----

const mode = ref<ViewportMode>('fit')
const MODES: Array<{ mode: ViewportMode; icon: string; label: string }> = [
  { mode: 'fit', icon: 'i-lucide-maximize', label: 'Fit the pane' },
  { mode: 'desktop', icon: 'i-lucide-monitor', label: 'Desktop, 1280 × 800' },
  { mode: 'tablet', icon: 'i-lucide-tablet', label: 'Tablet, 820 × 1180' },
  { mode: 'phone', icon: 'i-lucide-smartphone', label: 'Phone, 390 × 844' },
]
const stage = ref<HTMLElement | null>(null)
const frame = ref<HTMLIFrameElement | null>(null)
const room = ref({ width: 0, height: 0 })
let watcher: ResizeObserver | null = null
onMounted(() => {
  watcher = new ResizeObserver(() => {
    const el = stage.value
    if (el) room.value = { width: el.clientWidth, height: el.clientHeight }
  })
  if (stage.value) watcher.observe(stage.value)
})
onBeforeUnmount(() => watcher?.disconnect())
watch(stage, (el, was) => {
  if (was) watcher?.unobserve(was)
  if (el) watcher?.observe(el)
})
/** The stage's size read now, as the observer would on its next turn: a size the editor's opening changed is laid out at once. */
function settleRoom() {
  const el = stage.value
  if (el && (el.clientWidth !== room.value.width || el.clientHeight !== room.value.height)) room.value = { width: el.clientWidth, height: el.clientHeight }
}
const device = computed(() => (mode.value === 'fit' ? null : DEVICE_SIZES[mode.value]))
const scale = computed(() => {
  const d = device.value
  if (!d || !room.value.width || !room.value.height) return 1
  return Math.min(1, room.value.width / d.width, room.value.height / d.height)
})
const holder = computed(() => (device.value ? { width: `${device.value.width * scale.value}px`, height: `${device.value.height * scale.value}px` } : { width: '100%', height: '100%' }))
const frameStyle = computed(() =>
  device.value ? { width: `${device.value.width}px`, height: `${device.value.height}px`, transform: `scale(${scale.value})`, transformOrigin: '0 0' } : { width: '100%', height: '100%' },
)

// ---- The bridge and the captures ----

const session = usePreviewBridge({
  listen(heard) {
    if (typeof window === 'undefined') return () => {}
    const h = (e: MessageEvent) => heard(e)
    window.addEventListener('message', h)
    return () => window.removeEventListener('message', h)
  },
  end: () => ({ window: framed.value ? (frame.value?.contentWindow ?? null) : null, origin: framed.value ? originOf(src.value) : '' }),
  label: () => (props.target ? labels.moment() : Promise.resolve(null)),
  source: () => screenshotSource(frame.value!, { seam: previewSeam.source }),
  // The frame's box, cut to the stage's: what is outside the stage (the editor beside it) is never in a screenshot.
  rect: () => {
    const r = frame.value?.getBoundingClientRect()
    const st = stage.value?.getBoundingClientRect()
    if (!r) return { x: 0, y: 0, width: 0, height: 0 }
    if (!st) return { x: r.x, y: r.y, width: r.width, height: r.height }
    const x = Math.max(r.left, st.left)
    const y = Math.max(r.top, st.top)
    return { x, y, width: Math.max(0, Math.min(r.right, st.right) - x), height: Math.max(0, Math.min(r.bottom, st.bottom) - y) }
  },
  beforeFrame: async () => {
    // Nothing of the workbench over the frame while its pixels are taken: on a phone the editor goes while the session is busy (the
    // template's v-show), a size the editor's opening changed is laid out, and both are painted before the frame is read.
    settleRoom()
    await nextTick()
    await painted()
  },
  frameElement: () => frame.value,
  view: () => ({ mode: mode.value, scale: scale.value }),
  fallback: () => {
    const d = device.value
    return { url: src.value, width: d ? d.width : room.value.width, height: d ? d.height : room.value.height }
  },
})
/** Two animation frames: what the last change drew is on the screen. */
function painted(): Promise<void> {
  if (typeof requestAnimationFrame === 'undefined') return Promise.resolve()
  return new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
}
/** The frame loaded: in the desktop app the shell puts the bridge into it first (any page has it there), then the hello. */
async function loaded() {
  const at = src.value
  if (framed.value) await desktopPreviewBridge(at)
  if (at === src.value) {
    hellos.value++
    session.hello()
  }
}
const hellos = ref(0)
// A frame element replaced or removed, or its address changed, ends the load it was on: its hellos stop and nothing of it is taken
// again; the next load says hello afresh.
watch(
  [frame, address],
  ([el, at], [was, wasAt]) => {
    if ((was && el !== was) || (wasAt && at !== wasAt)) session.gone()
  },
  { flush: 'sync' },
)
const frameKey = ref(0)
function refresh() {
  frameKey.value++
}
watch(
  () => props.reloads,
  (n, was) => n !== was && was !== undefined && refresh(),
)
/**
 * The editor: over the frame, never beside it, so its opening never narrows the frame and lays the page out again under the pointer
 * (Capture's selection is made on the frame's box as it was at the press): on a wide screen over the stage's right edge, on a phone
 * over the whole stage, as the page; out of the way while the session selects, describes or takes pixels. Made at its first opening
 * and kept: closed, it is hidden, its drafts' notes and saved captures kept. `opens` counts its openings: each lists the attempt's
 * saved captures again.
 */
const editorOpen = ref(false)
const editorMade = ref(false)
const opens = ref(0)
function openEditor() {
  editorMade.value = true
  if (!editorOpen.value) opens.value++
  editorOpen.value = true
}
const editorShown = computed(() => editorOpen.value && !session.state.busy)

const from = computed<CaptureFrom | null>(() => {
  if (props.target) return { run: props.target.run, task: props.target.task, attempt: props.target.attempt, ...(props.target.seq ? { seq: props.target.seq } : {}) }
  return props.link ? { url: props.link } : null
})

async function capture() {
  openEditor()
  // The editor opens first (over the frame: the frame keeps its box), and the stage's size is read before the moment is.
  await nextTick()
  settleRoom()
  await nextTick()
  // While the hello is under way Capture waits for it: selection mode where the page answers, the screenshot alone where it does not.
  await session.start()
}

/** Waits for the hello after the frame's next load to be answered or given up (a bound on a frame that never loads). */
function nextHello(after: number): Promise<void> {
  return new Promise((resolve) => {
    const done = () => {
      stop()
      clearTimeout(bound)
      resolve()
    }
    const stop = watch([hellos, () => session.state.bridge], ([n, b]) => n > after && (b === 'ready' || b === 'absent') && done(), { immediate: true })
    const bound = setTimeout(done, 30_000)
  })
}

/**
 * Capture again (PV1 PR-8): the earlier capture's device mode and address (its path on this preview's own origin, never another), each
 * of its elements described again by its selector into a new capture that names it as the one it retakes.
 */
const captures = useCaptures()
const retakeError = ref('')
async function again(c: CaptureAnswer) {
  retakeError.value = ''
  openEditor()
  if (c.viewport.mode !== mode.value) mode.value = c.viewport.mode
  await nextTick()
  settleRoom()
  const want = retakeAddress(c.url, src.value)
  if (!want) {
    retakeError.value = 'the capture names no page this preview can open'
    return
  }
  // The frame is always loaded again at the capture's page: the page it shows may be another than the address it was given (a link
  // followed inside it, a route the page pushed), which nothing tells the pane.
  const before = hellos.value
  moved.value = want === src.value ? '' : want
  frameKey.value++
  await nextHello(before)
  if (session.state.bridge !== 'ready') {
    retakeError.value = "the page did not answer: Capture again needs the bridge to describe the capture's elements"
    return
  }
  const elements = (c.annotations ?? []).filter((a) => a.kind === 'element' && a.element).map((a) => ({ selector: a.element!.selector, note: a.note }))
  await session.retake(c.id, elements)
}
/** The capture `retake` names, read to offer Capture again of it. */
const retaking = ref<CaptureAnswer | null>(null)
watch(
  () => props.retake,
  async (id) => {
    retaking.value = null
    if (!id) return
    try {
      retaking.value = await captures.read(id)
    } catch (e) {
      retakeError.value = (e as Error).message
    }
  },
  { immediate: true },
)

/** Escape in the pane (its toolbar, its stage) leaves selection mode, or ends the tab's capture; an Escape elsewhere (a menu, a note) does not. */
const root = ref<HTMLElement | null>(null)
function onKey(e: KeyboardEvent) {
  if (e.key !== 'Escape') return
  if (session.state.busy === 'select') session.cancel()
  else if (e.target instanceof Node && root.value?.contains(e.target) && !(e.target as Element).closest?.('[data-capture-editor]')) void session.release()
}
onMounted(() => window.addEventListener('keydown', onKey))
onBeforeUnmount(() => window.removeEventListener('keydown', onKey))

const selecting = computed(() => session.state.busy === 'select')
const canSelect = computed(() => session.state.bridge === 'ready' && framed.value)
defineExpose({ capture, session, again, refresh })
</script>

<template>
  <div ref="root" class="flex h-full min-h-0 min-w-0 flex-col" data-preview-pane :data-preview-bridge="session.state.bridge">
    <!-- The label, then what the pane offers: one line that never wraps, so nothing it shows resizes the frame (a page's resize is a
         new moment). -->
    <div class="flex h-9 min-w-0 flex-none flex-nowrap items-center gap-2 border-b border-default px-3 text-xs">
      <UIcon :name="target ? 'i-lucide-git-commit-horizontal' : 'i-lucide-globe'" class="size-3.5 flex-none text-muted" />
      <span class="min-w-0 flex-1 truncate text-default" :title="words" data-preview-label :data-preview-tree="labels.label.value?.tree ?? (target ? '' : 'link')">{{ words }}</span>
      <div class="flex flex-none items-center gap-0.5" role="group" aria-label="Device mode">
        <IconButton
          v-for="m in MODES"
          :key="m.mode"
          :icon="m.icon"
          size="xs"
          color="neutral"
          :variant="mode === m.mode ? 'soft' : 'ghost'"
          :label="m.label"
          :aria-pressed="mode === m.mode"
          :data-preview-mode="m.mode"
          :data-preview-mode-on="mode === m.mode ? '' : undefined"
          @click="mode = m.mode"
        />
      </div>
      <template v-if="!compact">
        <IconButton icon="i-lucide-refresh-cw" size="xs" color="neutral" variant="ghost" label="Refresh" :disabled="!framed" data-preview-refresh @click="refresh" />
        <IconButton icon="i-lucide-external-link" size="xs" color="neutral" variant="ghost" label="New tab" :to="src || undefined" :disabled="!src" target="_blank" rel="noopener noreferrer" external data-preview-newtab />
      </template>
      <IconButton icon="i-lucide-images" size="xs" color="neutral" :variant="editorOpen ? 'soft' : 'ghost'" :label="editorOpen ? 'Hide the captures' : 'Show the captures'" :aria-pressed="editorOpen" data-preview-captures @click="editorOpen ? (editorOpen = false) : openEditor()" />
      <UButton label="Capture" icon="i-lucide-scan" size="xs" class="flex-none" :loading="session.state.busy === 'shot'" :disabled="!framed || selecting" data-preview-capture @click="capture" />
    </div>
    <!-- What the pane says now, in one line of a fixed height: selecting, no bridge, a label that could not be read. -->
    <div class="flex h-7 min-w-0 flex-none items-center gap-2 border-b border-default px-3 text-xs" data-preview-status>
      <template v-if="selecting">
        <span class="min-w-0 flex-1 truncate text-default" title="Point at an element and click it. Up and Down step through what is under the pointer, Left and Right walk to its parent and child, Shift with Up and Down to its siblings; Escape leaves." data-preview-selecting>
          Selecting: click an element; Up and Down step through what is under the pointer; Escape leaves.
        </span>
        <UButton label="Stop selecting" size="xs" color="neutral" variant="ghost" class="flex-none" data-preview-cancel @click="session.cancel()" />
      </template>
      <span v-else-if="session.state.bridge === 'absent'" class="min-w-0 flex-1 truncate text-muted" :title="`${NO_BRIDGE_WORDS}.`" data-preview-nobridge>{{ NO_BRIDGE_WORDS }}.</span>
      <span v-else-if="retakeError" class="min-w-0 flex-1 truncate text-default" data-preview-retake-error>{{ retakeError }}</span>
      <template v-else-if="retaking && !retaking.deletedAt">
        <span class="min-w-0 flex-1 truncate text-default" :title="retaking.url" data-preview-retake-of>Capture again of <span class="font-mono" data-mono="id">{{ retaking.id }}</span>: its page and device mode here, each element described again.</span>
        <UButton label="Capture again" icon="i-lucide-repeat" size="xs" color="neutral" variant="outline" class="flex-none" :disabled="!framed || !!session.state.busy" data-preview-retake @click="again(retaking)" />
      </template>
      <span v-else-if="labels.error.value" class="min-w-0 flex-1 truncate text-muted" data-preview-label-error>The label could not be read: {{ labels.error.value }}</span>
    </div>

    <div class="relative flex min-h-0 min-w-0 flex-1">
      <div ref="stage" class="relative min-h-0 min-w-0 flex-1 overflow-hidden bg-elevated/40" data-preview-stage>
        <div v-if="self" class="flex h-full flex-col items-center justify-center gap-2 p-6 text-center text-sm" data-preview-self>
          <span class="text-default">{{ SELF_WORDS }}.</span>
          <UButton label="New tab" icon="i-lucide-external-link" size="sm" color="neutral" variant="outline" :to="src" target="_blank" rel="noopener noreferrer" external />
        </div>
        <p v-else-if="target?.seq && labels.missing.value && checkout === 'resolved'" class="p-6 text-sm text-muted" data-preview-opening>The candidate's checkout is being made: its setup runs and its services start, then its preview shows here.</p>
        <p v-else-if="target?.seq && labels.missing.value" class="p-6 text-sm text-muted" data-preview-missing>No preview of this candidate's checkout is open now: it was closed, it ended, or it has not started.</p>
        <p v-else-if="target && labels.missing.value" class="p-6 text-sm text-muted" data-preview-missing>No preview of this attempt is open now: its tree has ended or its services are not ready.</p>
        <p v-else-if="!src" class="p-6 text-sm text-muted">Reading the preview…</p>
        <p v-else-if="!framed" class="p-6 text-sm text-muted">Only an http or https address is framed.</p>
        <div v-else class="overflow-hidden" :style="holder">
          <iframe
            ref="frame"
            :key="frameKey"
            :src="address"
            :style="frameStyle"
            class="block border-0 bg-white"
            sandbox="allow-scripts allow-same-origin allow-forms allow-popups"
            referrerpolicy="no-referrer"
            title="Preview"
            data-preview-frame
            @load="loaded"
          />
        </div>
      </div>
      <CaptureEditor
        v-if="editorMade"
        v-show="editorShown"
        :session="session"
        :opens="opens"
        :from="from"
        :run-ticket="runTicket"
        :attempt-session="attemptSession"
        :can-select="canSelect"
        again
        :class="phone ? 'absolute inset-0 z-10 bg-default' : 'absolute inset-y-0 right-0 z-10 w-[26rem] max-w-[50%] border-l border-default bg-default'"
        @close="editorOpen = false"
        @again="again"
      />
    </div>
  </div>
</template>
