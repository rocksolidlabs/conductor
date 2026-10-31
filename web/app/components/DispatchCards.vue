<script setup lang="ts">
import type { DispatchLabel, TicketSummary } from '~/composables/useDispatch'
import type { AgentInfo } from '~/composables/useSessions'
import { assigneeOf, labelDot, priorityWord, ticketPath } from '~/utils/dispatch'
import { avatarTone } from '~/utils/avatar'
import { initials } from '~/utils/sessions'

/** One column of cards: a board's status, a roadmap's horizon. */
export interface CardColumn {
  /** Its id in the page (a status, a horizon's name). */
  id: string
  label: string
  icon: string
  /** The icon's colour class. */
  tone?: string
  tickets: readonly TicketSummary[]
  /** Drawn folded, its count alone, until opened (the roadmap's Not planned). */
  folded?: boolean
}

/**
 * The columns of a board or a roadmap (docs/plans/dispatch.md, "web/app"; DispatchBoard.vue, DispatchRoadmap.vue): a card per ticket,
 * in the list's order within its column, with its key, title, priority, labels and assignees, a link to the ticket's page. A card is
 * moved by the browser's drag and drop onto another column, or by ← → while it has the focus, one column at a time (`step`, which never
 * wraps); the column says `move`, and its host makes the edit, the card shown in the column it was moved to until the store answers
 * (snapping back when refused). The focus stays on a card a key moved. A folded column takes a drop as any other and opens to show its
 * cards. The columns scroll sideways when they do not fit (a phone), the page never does.
 */
const props = defineProps<{
  columns: readonly CardColumn[]
  labels: ReadonlyMap<string, DispatchLabel>
  agents: readonly AgentInfo[]
  /** Nothing can be moved (an archived project). */
  locked?: boolean
  /** The id of the column `delta` columns from the card's, or null when there is none that way. */
  step: (t: TicketSummary, delta: 1 | -1) => string | null
}>()
const emit = defineEmits<{ move: [key: string, column: string] }>()

const root = useTemplateRef<HTMLElement>('root')
/** The card being dragged, and the column it is over. */
const dragging = ref<string | null>(null)
const over = ref<string | null>(null)
/** The folded columns opened. */
const opened = ref(new Set<string>())
/** A card a key moved: it keeps the focus when it is drawn again in its new column, or back in its old one when the move is refused. */
const keep = ref<string | null>(null)

const DRAG_TYPE = 'text/x-dispatch-ticket'

function folded(c: CardColumn): boolean {
  return !!c.folded && !opened.value.has(c.id)
}
function unfold(id: string) {
  opened.value = new Set([...opened.value, id])
}
function fold(id: string) {
  const next = new Set(opened.value)
  next.delete(id)
  opened.value = next
}

function cardOf(key: string): HTMLElement | null {
  return root.value?.querySelector<HTMLElement>(`[data-card="${CSS.escape(key)}"]`) ?? null
}

/** The column a card is in. */
function columnOf(key: string): CardColumn | undefined {
  return props.columns.find((c) => c.tickets.some((t) => t.key === key))
}

function onDragStart(e: DragEvent, t: TicketSummary) {
  if (props.locked) {
    e.preventDefault()
    return
  }
  dragging.value = t.key
  e.dataTransfer?.setData(DRAG_TYPE, t.key)
  e.dataTransfer?.setData('text/plain', t.key)
  if (e.dataTransfer) e.dataTransfer.effectAllowed = 'move'
}
function onDragOver(e: DragEvent, c: CardColumn) {
  if (!dragging.value && !e.dataTransfer?.types.includes(DRAG_TYPE)) return
  e.preventDefault()
  if (e.dataTransfer) e.dataTransfer.dropEffect = 'move'
  over.value = c.id
}
function onDragLeave(e: DragEvent, c: CardColumn) {
  const to = e.relatedTarget as Node | null
  if (over.value === c.id && !(to && (e.currentTarget as HTMLElement).contains(to))) over.value = null
}
function onDrop(e: DragEvent, c: CardColumn) {
  e.preventDefault()
  const key = dragging.value ?? e.dataTransfer?.getData(DRAG_TYPE) ?? ''
  dragging.value = null
  over.value = null
  if (!key || props.locked) return
  const from = columnOf(key)
  if (!from || from.id === c.id) return
  if (c.folded) unfold(c.id)
  emit('move', key, c.id)
}
function onDragEnd() {
  dragging.value = null
  over.value = null
}

/** ← → on a focused card: it moves a column that way, and keeps the focus. */
function onCardKey(e: KeyboardEvent, t: TicketSummary) {
  if (e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return
  if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return
  e.preventDefault()
  if (props.locked) return
  const to = props.step(t, e.key === 'ArrowRight' ? 1 : -1)
  if (!to) return
  const c = props.columns.find((x) => x.id === to)
  if (c?.folded) unfold(c.id)
  keep.value = t.key
  emit('move', t.key, to)
}

function onFocusIn(e: FocusEvent) {
  const key = (e.target as HTMLElement).closest('[data-card]')?.getAttribute('data-card') ?? null
  if (key !== keep.value) keep.value = null
}
function onFocusOut(e: FocusEvent) {
  // The focus left for somewhere else on the page (not for nowhere, as when the card it was on is drawn again elsewhere).
  const to = e.relatedTarget as Node | null
  if (to && !root.value?.contains(to)) keep.value = null
}
// A card drawn again in another column is a new element: the focus a key's move left on the old one follows it.
watch(
  () => props.columns,
  () => {
    const key = keep.value
    if (!key) return
    const active = document.activeElement
    if (active && active !== document.body && root.value?.contains(active)) return
    cardOf(key)?.focus()
  },
  { flush: 'post' },
)

/** An actor's name: the person's, or the agent's from the catalog. */
function actorName(actor: string): string {
  const a = assigneeOf(actor)
  return a.kind === 'agent' ? (props.agents.find((x) => x.id === a.name)?.name ?? a.name) : a.name
}

/**
 * Gives a card the focus (a ticket's page left by Escape: its card focused again), its column unfolded first when it is folded (the
 * roadmap's Not planned), once the card is drawn. False when no column shows it.
 */
async function focusCard(key: string): Promise<boolean> {
  const c = columnOf(key)
  if (!c) return false
  if (folded(c)) {
    unfold(c.id)
    await nextTick()
  }
  const el = cardOf(key)
  el?.focus()
  return !!el
}

defineExpose({ unfold, fold, focusCard })
</script>

<template>
  <div ref="root" class="flex min-h-0 min-w-0 flex-1 gap-3 overflow-x-auto overscroll-x-contain p-3 sm:px-6" data-cards @focusin="onFocusIn" @focusout="onFocusOut">
    <section
      v-for="c in columns"
      :key="c.id"
      class="flex min-h-0 flex-none flex-col rounded-lg bg-elevated/40 ring ring-default transition-colors"
      :class="[folded(c) ? 'w-14' : 'w-[17rem]', over === c.id ? 'bg-primary/10 ring-primary/60' : '']"
      :data-column="c.id"
      :data-count="c.tickets.length"
      :data-folded="folded(c) ? '' : undefined"
      :aria-label="`${c.label}: ${c.tickets.length} ${c.tickets.length === 1 ? 'ticket' : 'tickets'}`"
      @dragover="onDragOver($event, c)"
      @dragleave="onDragLeave($event, c)"
      @drop="onDrop($event, c)"
    >
      <!-- A folded column: its name on its side and its count; a click opens it. -->
      <button v-if="folded(c)" type="button" class="flex flex-1 flex-col items-center gap-2 py-3 text-muted hover:text-highlighted" :aria-label="`Show ${c.label}, ${c.tickets.length} ${c.tickets.length === 1 ? 'ticket' : 'tickets'}`" data-column-unfold @click="unfold(c.id)">
        <UIcon :name="c.icon" class="size-4 flex-none" :class="c.tone" />
        <span class="text-xs tabular-nums">{{ c.tickets.length }}</span>
        <span class="text-xs [writing-mode:vertical-rl]">{{ c.label }}</span>
      </button>
      <template v-else>
        <header class="flex flex-none items-center gap-2 px-3 pt-2.5 pb-2">
          <UIcon :name="c.icon" class="size-4 flex-none" :class="c.tone" />
          <h3 class="min-w-0 truncate text-sm font-medium text-highlighted">{{ c.label }}</h3>
          <span class="text-xs text-muted tabular-nums" data-column-count>{{ c.tickets.length }}</span>
          <UButton v-if="c.folded" icon="i-lucide-chevrons-left" size="xs" color="neutral" variant="ghost" class="ml-auto" :aria-label="`Fold ${c.label}`" data-column-fold @click="fold(c.id)" />
        </header>
        <div class="flex min-h-24 flex-1 flex-col gap-2 overflow-y-auto px-2 pb-2">
          <NuxtLink
            v-for="t in c.tickets"
            :key="t.id"
            :to="ticketPath(t.key)"
            :draggable="locked ? 'false' : 'true'"
            class="flex flex-col gap-1.5 rounded-md bg-default p-2.5 shadow-xs ring ring-default outline-none hover:ring-accented focus-visible:ring-2 focus-visible:ring-primary"
            :class="dragging === t.key ? 'opacity-50' : ''"
            :data-card="t.key"
            :data-ticket-key="t.key"
            :data-card-column="c.id"
            :aria-keyshortcuts="locked ? undefined : 'ArrowLeft ArrowRight'"
            @dragstart="onDragStart($event, t)"
            @dragend="onDragEnd"
            @keydown="onCardKey($event, t)"
          >
            <span class="flex items-center gap-2">
              <span class="font-mono text-xs text-muted" data-card-key>{{ t.key }}</span>
              <UBadge v-if="t.blocked && t.status !== 'done' && t.status !== 'cancelled'" label="blocked" color="warning" variant="subtle" size="sm" />
              <UIcon :name="priorityWord(t.priority).icon" class="ml-auto size-4 flex-none" :class="t.priority === 4 ? 'text-error' : t.priority === 0 ? 'text-dimmed' : 'text-muted'" :title="priorityWord(t.priority).label" />
            </span>
            <span class="line-clamp-3 break-words text-sm text-highlighted" data-card-title>{{ t.title }}</span>
            <span v-if="t.labels.length || t.assignees.length" class="flex flex-wrap items-center gap-1.5 text-xs text-muted">
              <span v-for="id in t.labels.slice(0, 3)" :key="id" class="flex max-w-28 items-center gap-1 rounded-full px-1.5 py-0.5 ring ring-default">
                <span class="size-1.5 flex-none rounded-full" :class="labelDot(labels.get(id)?.color ?? 'gray')" />
                <span class="truncate">{{ labels.get(id)?.name ?? 'label' }}</span>
              </span>
              <span v-if="t.labels.length > 3" class="text-dimmed">+{{ t.labels.length - 3 }}</span>
              <span v-if="t.assignees.length" class="ml-auto flex flex-none items-center gap-0.5" :title="`Assigned to ${t.assignees.map(actorName).join(', ')}`">
                <template v-for="a in t.assignees.slice(0, 3)" :key="a">
                  <SessionAvatar v-if="assigneeOf(a).kind === 'agent'" :agent-id="assigneeOf(a).name" />
                  <span v-else class="grid size-6 place-items-center rounded-full text-[10px] font-semibold" :class="avatarTone(assigneeOf(a).name)">{{ initials(assigneeOf(a).name) }}</span>
                </template>
                <span v-if="t.assignees.length > 3" class="grid size-6 place-items-center rounded-full bg-elevated text-[10px]">+{{ t.assignees.length - 3 }}</span>
              </span>
            </span>
          </NuxtLink>
          <p v-if="!c.tickets.length" class="px-1 py-3 text-center text-xs text-dimmed">{{ locked ? 'None' : 'None. Drop a card here.' }}</p>
        </div>
      </template>
    </section>
    <slot />
  </div>
</template>
