<script setup lang="ts">
import type { CaptureAnswer, CaptureFrom, ViewportMode } from '~/utils/capture'
import { DEVICE_SIZES, NO_BRIDGE_WORDS, SELF_WORDS, isWorkbench, labelWords, originOf } from '~/utils/capture'
import { previewSeam, usePreviewBridge } from '~/composables/usePreviewBridge'
import { type LabelTarget, usePreviewLabel } from '~/composables/usePreviewLabel'
import { screenshotSource } from '~/utils/screenshot'

/**
 * A preview in Conductor (docs/plans/browser-evidence.md, "A preview in Conductor" and "The pane"): the frame of an attempt's
 * preview (its label's admin address, the tree's secret put in, which only the frame and New tab are given) or of a link, the
 * revision label above it (read every 5 s and at every run event, neutral ink), the device mode (Fit the pane, or Desktop 1280 × 800,
 * Tablet 820 × 1180, Phone 390 × 844: the frame that many CSS pixels, scaled down to fit), Refresh, New tab and Capture, which puts
 * the page into selection mode through the bridge (usePreviewBridge) and opens the capture editor beside the frame. The frame keeps
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
  /** Capture again offered on a saved capture (a candidate's checkout to retake it in). */
  again?: boolean
}>()
const emit = defineEmits<{ again: [capture: CaptureAnswer]; label: [words: string] }>()

const labels = usePreviewLabel(
  () => props.target ?? null,
  () => props.revision,
)
const phone = useIsPhone()

/** The frame's address: the label's admin URL once read (kept until the label names another), or the link. */
const src = ref('')
watch(
  () => (props.target ? (labels.label.value?.url ?? '') : (props.link ?? '')),
  (u) => {
    if (u && u !== src.value) src.value = u
    if (!props.target) src.value = u
  },
  { immediate: true },
)
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
  rect: () => {
    const r = frame.value?.getBoundingClientRect()
    return r ? { x: r.x, y: r.y, width: r.width, height: r.height } : { x: 0, y: 0, width: 0, height: 0 }
  },
  view: () => ({ mode: mode.value, scale: scale.value }),
  fallback: () => {
    const d = device.value
    return { url: src.value, width: d ? d.width : room.value.width, height: d ? d.height : room.value.height }
  },
})
const frameKey = ref(0)
function refresh() {
  frameKey.value++
}
/** The editor: beside the frame on a wide screen; on a phone over it, as the page, and out of the way while the person selects. */
const editorOpen = ref(false)

const from = computed<CaptureFrom | null>(() => {
  if (props.target) return { run: props.target.run, task: props.target.task, attempt: props.target.attempt, ...(props.target.seq ? { seq: props.target.seq } : {}) }
  return props.link ? { url: props.link } : null
})

async function capture() {
  editorOpen.value = true
  // The editor opens first, so the frame's size is settled before the moment is read.
  await nextTick()
  if (session.state.bridge === 'ready') await session.select()
  else await session.snap()
}

function onKey(e: KeyboardEvent) {
  if (e.key !== 'Escape') return
  if (session.state.busy === 'select') session.cancel()
  else void session.release()
}
onMounted(() => window.addEventListener('keydown', onKey))
onBeforeUnmount(() => window.removeEventListener('keydown', onKey))

const selecting = computed(() => session.state.busy === 'select')
const canSelect = computed(() => session.state.bridge === 'ready' && framed.value)
defineExpose({ capture, session })
</script>

<template>
  <div class="flex h-full min-h-0 min-w-0 flex-col" data-preview-pane :data-preview-bridge="session.state.bridge">
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
      <span v-else-if="labels.error.value" class="min-w-0 flex-1 truncate text-muted" data-preview-label-error>The label could not be read: {{ labels.error.value }}</span>
    </div>

    <div class="relative flex min-h-0 min-w-0 flex-1">
      <div ref="stage" class="relative min-h-0 min-w-0 flex-1 overflow-hidden bg-elevated/40" data-preview-stage>
        <div v-if="self" class="flex h-full flex-col items-center justify-center gap-2 p-6 text-center text-sm" data-preview-self>
          <span class="text-default">{{ SELF_WORDS }}.</span>
          <UButton label="New tab" icon="i-lucide-external-link" size="sm" color="neutral" variant="outline" :to="src" target="_blank" rel="noopener noreferrer" external />
        </div>
        <p v-else-if="target && labels.missing.value" class="p-6 text-sm text-muted" data-preview-missing>No preview of this attempt is open now: its tree has ended or its services are not ready.</p>
        <p v-else-if="!src" class="p-6 text-sm text-muted">Reading the preview…</p>
        <p v-else-if="!framed" class="p-6 text-sm text-muted">Only an http or https address is framed.</p>
        <div v-else class="overflow-hidden" :style="holder">
          <iframe
            ref="frame"
            :key="frameKey"
            :src="src"
            :style="frameStyle"
            class="block border-0 bg-white"
            sandbox="allow-scripts allow-same-origin allow-forms allow-popups"
            referrerpolicy="no-referrer"
            title="Preview"
            data-preview-frame
            @load="session.hello()"
          />
        </div>
      </div>
      <CaptureEditor
        v-if="editorOpen"
        v-show="!phone || !selecting"
        :session="session"
        :from="from"
        :run-ticket="runTicket"
        :attempt-session="attemptSession"
        :can-select="canSelect"
        :again="again"
        :class="phone ? 'absolute inset-0 z-10 bg-default' : 'w-[26rem] max-w-[50%] flex-none border-l border-default'"
        @close="editorOpen = false"
        @again="(c: CaptureAnswer) => emit('again', c)"
      />
    </div>
  </div>
</template>
