<script setup lang="ts">
import type { SessionInfo } from '~/composables/useSessions'
import { dropIndex, keyMove, moveInOrder, type StripView } from '~/utils/carousel'
import { sessionStatus } from '~/utils/status'

/**
 * The strip at the Roundhouse's foot: one chip per session in the kept
 * order, the current one marked, the rotation's progress under it. The
 * chips are rearranged by drag or by keyboard (#310): Space picks the
 * focused chip up, the arrows move it, Space or Enter drops it, Escape
 * puts it back. The chip picked up, by key or by drag, is known by its
 * session id and found again at each move, so a session that ends
 * meanwhile moves nothing else (Codex's round on #310). As thumbnails
 * the chips carry a small live view of the current session and its
 * neighbours (the ones the Roundhouse connects anyway) and the idle mark
 * for the rest: a thumbnail is the slide's own screen as text, taken
 * every second, never a connection of its own (the second whole-batch
 * round: a view of its own doubled the connections and took a viewer
 * slot); it is inert, nothing in it takes the focus or a key.
 */
const props = defineProps<{
  sessions: SessionInfo[]
  selected: number
  /** 0..1 of the current rotation interval that has elapsed. */
  progress: number
  /** Session id follow mode is holding on, if any. */
  holdingId?: string
  rotating: boolean
  /** A slide's screen as text (its own view's, so a thumbnail opens no connection), null for a slide not connected. */
  snapshot?: (id: string) => string[] | null
}>()
const emit = defineEmits<{ select: [index: number]; reorder: [ids: string[]] }>()
const view = defineModel<StripView>('view', { default: 'chips' })

const ids = computed(() => props.sessions.map((s) => s.id))

// The thumbnails: each near slide's screen, read again every second while they show.
const screens = ref<Record<string, string[] | null>>({})
let shot: number | undefined
function takeScreens() {
  if (!props.snapshot) return
  const next: Record<string, string[] | null> = {}
  for (const s of props.sessions) next[s.id] = props.snapshot(s.id)
  screens.value = next
}
watch(
  () => view.value === 'thumbs',
  (on) => {
    window.clearInterval(shot)
    shot = undefined
    if (!on) return
    takeScreens()
    shot = window.setInterval(takeScreens, 1000)
  },
  { immediate: true },
)
onBeforeUnmount(() => window.clearInterval(shot))

const now = useNow()

function bar(s: SessionInfo, i: number): { width: string; cls: string } {
  if (s.id === props.holdingId) return { width: '100%', cls: 'bg-warning' }
  if (i === props.selected && props.rotating) return { width: `${Math.round(props.progress * 100)}%`, cls: 'bg-primary' }
  return { width: '0%', cls: 'bg-primary' }
}

// Drag: the chip picked up (by id), the place it would land (the index in the list once it is taken out), and the boundary the
// marker shows (the index in the list as shown, 0..n), as HTML5 drag and drop reports them.
const dragFromId = ref<string | null>(null)
const dropAt = ref<number | null>(null)
const markerAt = ref<number | null>(null)
function onDragStart(s: SessionInfo, e: DragEvent) {
  dragFromId.value = s.id
  if (e.dataTransfer) {
    e.dataTransfer.effectAllowed = 'move'
    e.dataTransfer.setData('text/plain', s.id)
  }
}
function onDragOver(i: number, e: DragEvent) {
  const from = dragFromId.value ? ids.value.indexOf(dragFromId.value) : -1
  if (from < 0) return
  e.preventDefault()
  if (e.dataTransfer) e.dataTransfer.dropEffect = 'move'
  const r = (e.currentTarget as HTMLElement).getBoundingClientRect()
  const ratio = r.width > 0 ? (e.clientX - r.left) / r.width : 0
  markerAt.value = ratio > 0.5 ? i + 1 : i
  dropAt.value = dropIndex(i, ratio, from)
}
function onDrop(e: DragEvent) {
  e.preventDefault()
  const from = dragFromId.value ? ids.value.indexOf(dragFromId.value) : -1
  if (from >= 0 && dropAt.value !== null && dropAt.value !== from) emit('reorder', moveInOrder(ids.value, from, dropAt.value))
  onDragEnd()
}
function onDragEnd() {
  dragFromId.value = null
  dropAt.value = null
  markerAt.value = null
}

// Keyboard: the chip picked up with Space, by id, and the order it was picked from, for Escape.
const grabbedId = ref<string | null>(null)
let pickedFrom: string[] = []
const announce = ref('')
const chips = new Map<string, HTMLElement>()
function chipRef(id: string) {
  return (el: unknown) => {
    if (el) chips.set(id, el as HTMLElement)
    else chips.delete(id)
  }
}
async function focusChip(id: string | null) {
  if (!id) return
  await nextTick()
  chips.get(id)?.focus()
}
function onKeyDown(s: SessionInfo, i: number, e: KeyboardEvent) {
  if (e.altKey || e.ctrlKey || e.metaKey) return
  const grabbed = grabbedId.value ? ids.value.indexOf(grabbedId.value) : -1
  if (e.key === 'Enter' && grabbed < 0) {
    e.preventDefault()
    emit('select', i)
    return
  }
  if (!(e.key === ' ' || e.key === 'Enter' || e.key === 'Escape' || e.key.startsWith('Arrow'))) return
  if (grabbed < 0 && e.key !== ' ') return
  // While a chip is picked up, the arrows move it and the page's own arrow keys (stepping) stay out of it.
  e.preventDefault()
  e.stopPropagation()
  const before = ids.value
  const r = keyMove(before, i, grabbed >= 0 ? grabbed : null, e.key)
  const picked = grabbedId.value ?? s.id
  const name = props.sessions.find((x) => x.id === picked)?.name ?? ''
  if (r.done === 'cancelled') {
    if (pickedFrom.join('\n') !== before.join('\n')) emit('reorder', pickedFrom)
    announce.value = `${name} put back`
    grabbedId.value = null
    pickedFrom = []
    void focusChip(picked)
    return
  }
  if (r.done === 'dropped') {
    announce.value = `${name} dropped at place ${r.focused + 1} of ${before.length}`
    grabbedId.value = null
    pickedFrom = []
    void focusChip(picked)
    return
  }
  if (grabbed < 0 && r.grabbed !== null) {
    pickedFrom = [...before]
    grabbedId.value = s.id
    announce.value = `${name} picked up. The arrows move it, Space drops it, Escape puts it back`
    return
  }
  if (r.ids.join('\n') !== before.join('\n')) {
    emit('reorder', r.ids)
    announce.value = `${name} moved to place ${r.focused + 1} of ${before.length}`
    void focusChip(picked)
  }
}
function onKeyUp(e: KeyboardEvent) {
  if (e.key === ' ') e.preventDefault()
}
/** A picked chip whose session ends is let go: nothing else is moved in its name. */
watch(ids, (now) => {
  if (grabbedId.value && !now.includes(grabbedId.value)) {
    grabbedId.value = null
    pickedFrom = []
  }
  if (dragFromId.value && !now.includes(dragFromId.value)) onDragEnd()
})
</script>

<template>
  <div class="flex items-stretch gap-2.5" data-carousel-strip>
    <div class="grid min-w-0 flex-1 gap-2.5" :style="{ gridTemplateColumns: `repeat(${Math.max(sessions.length, 1)}, minmax(0, 1fr))` }">
      <div
        v-for="(s, i) in sessions"
        :key="s.id"
        :ref="chipRef(s.id)"
        role="button"
        tabindex="0"
        draggable="true"
        class="relative flex cursor-grab flex-col overflow-hidden rounded-md border bg-default text-left transition-colors outline-none focus-visible:ring-2 focus-visible:ring-primary"
        :class="[
          i === selected ? 'border-primary ring-1 ring-primary' : 'border-default hover:border-accented',
          grabbedId === s.id ? 'cursor-grabbing opacity-80 ring-2 ring-primary' : '',
          dragFromId === s.id ? 'opacity-50' : '',
        ]"
        :aria-current="i === selected ? 'true' : undefined"
        :aria-grabbed="grabbedId === s.id ? 'true' : 'false'"
        :aria-label="`${s.name}, ${sessionStatus(s, now).word}, place ${i + 1} of ${sessions.length}`"
        :data-strip-chip="s.id"
        :data-strip-grabbed="grabbedId === s.id ? '' : undefined"
        @click="emit('select', i)"
        @keydown="onKeyDown(s, i, $event)"
        @keyup="onKeyUp"
        @dragstart="onDragStart(s, $event)"
        @dragover="onDragOver(i, $event)"
        @drop="onDrop"
        @dragend="onDragEnd"
      >
        <!-- Where a dragged chip would land: the boundary before this chip, or after the last. -->
        <span v-if="dragFromId !== null && markerAt === i" class="pointer-events-none absolute inset-y-0 left-0 w-0.5 bg-primary" aria-hidden="true" data-strip-marker />
        <span v-else-if="dragFromId !== null && markerAt === sessions.length && i === sessions.length - 1" class="pointer-events-none absolute inset-y-0 right-0 w-0.5 bg-primary" aria-hidden="true" data-strip-marker />
        <div class="flex items-center gap-1.5 px-2 py-1.5">
          <SessionAvatar :agent-id="s.agentId" :solid="i === selected" />
          <span class="truncate text-xs font-semibold">{{ s.name }}</span>
          <StatusMark :status="sessionStatus(s, now)" class="ml-auto" />
        </div>
        <!-- A thumbnail: a live view for a slide the Roundhouse connects, the idle mark for the rest; never a connection of its own,
             and inert: no focus, no key, no pointer reaches it. -->
        <div v-if="view === 'thumbs'" class="pointer-events-none relative h-20 overflow-hidden border-t border-default bg-default" inert data-strip-thumb>
          <pre v-if="screens[s.id]" class="m-0 h-full overflow-hidden whitespace-pre px-1.5 py-1 font-mono text-[6px] leading-[7px] text-muted" aria-hidden="true" data-strip-screen>{{ screens[s.id]!.join('\n') }}</pre>
          <div v-else class="flex h-full items-center justify-center gap-1.5 text-[11px] text-muted" data-strip-idle>
            <SessionAvatar :agent-id="s.agentId" />
            <StatusMark :status="sessionStatus(s, now)" word />
          </div>
        </div>
        <div class="h-[3px] w-full bg-elevated">
          <div class="h-[3px] transition-[width] duration-200 ease-linear" :class="bar(s, i).cls" :style="{ width: bar(s, i).width }" />
        </div>
      </div>
    </div>
    <UTooltip :text="view === 'thumbs' ? 'Show the strip as chips' : 'Show the strip as thumbnails'" :content="{ side: 'left' }">
      <UButton :icon="view === 'thumbs' ? 'i-lucide-rows-2' : 'i-lucide-images'" size="xs" color="neutral" variant="ghost" class="self-start" :aria-label="view === 'thumbs' ? 'Show the strip as chips' : 'Show the strip as thumbnails'" :data-strip-view="view" @click="view = view === 'thumbs' ? 'chips' : 'thumbs'" />
    </UTooltip>
    <span class="sr-only" aria-live="polite" data-strip-announce>{{ announce }}</span>
  </div>
</template>
