<script setup lang="ts">
import type { DispatchLabel, DispatchMilestone, TicketSummary } from '~/composables/useDispatch'
import type { AgentInfo } from '~/composables/useSessions'
import { TICKET_ROW_TRACKS, assigneeOf, labelDot, listFocusStep, listRefocus, priorityWord, statusWord, ticketPath, ticketWords } from '~/utils/dispatch'
import { avatarTone } from '~/utils/avatar'
import { initials, relativeTime } from '~/utils/sessions'

/**
 * A project's tickets as a list (docs/plans/dispatch.md, "web/app"): a row each, in the order the server sorted them, with its key, its
 * status, its title, its labels, its milestone, its priority, its owner, its assignees as avatar chips (a person's initials, an agent as
 * the sidebar shows it) and when it was last updated. From a 48rem container every row is one grid (TICKET_ROW_TRACKS), each cell drawn
 * on every row, empty (data-empty) when the ticket has none, so that a column reads straight down. Below it a row is two lines: its key
 * and title, then a grid of the rest under an indent. A row is a link to the ticket's page. The keyboard moves a roving focus over the
 * rows (utils/dispatch.ts: J and K, or the arrows, wrapping at the ends; a row gone hands the focus on; Enter opens the row, as a link
 * does). The tickets selected (X, the page's) are marked, and while any is, every row shows a box that selects it by a click, its track
 * widened for every row at once.
 */
const props = defineProps<{
  tickets: readonly TicketSummary[]
  labels: ReadonlyMap<string, DispatchLabel>
  milestones: ReadonlyMap<string, DispatchMilestone>
  agents: readonly AgentInfo[]
  now: number
  /** The keys of the tickets selected. */
  selected?: ReadonlySet<string>
}>()
const emit = defineEmits<{ toggle: [key: string] }>()

/** The key of the row the roving focus is on, or none. */
const focus = ref<string | null>(null)
const root = useTemplateRef<HTMLElement>('root')

/** The row of a key, as drawn. */
function rowOf(key: string): HTMLElement | null {
  return root.value?.querySelector<HTMLElement>(`[data-ticket-row="${CSS.escape(key)}"]`) ?? null
}

const keys = computed(() => props.tickets.map((t) => t.key))

/** Moves the focus a row down (1) or up (-1), wrapping at the ends, and gives that row the keyboard. */
function step(delta: 1 | -1) {
  focus.value = listFocusStep(keys.value, focus.value, delta)
  void nextTick(() => {
    if (focus.value) rowOf(focus.value)?.focus()
  })
}

/** Gives a row the focus by its key (back from its ticket's page), when the list shows it. */
function focusKey(key: string) {
  if (!keys.value.includes(key)) return
  focus.value = key
  void nextTick(() => rowOf(key)?.focus())
}

/** Leaves the list: no row has the focus. */
function leave() {
  const active = document.activeElement
  if (active instanceof HTMLElement && root.value?.contains(active)) active.blur()
  focus.value = null
}

// The rows change (a re-read, a filter): the focus stays on its row, or a row gone hands it on, and the keyboard follows it when it was
// in the list.
watch(keys, (after, before) => {
  const had = root.value?.contains(document.activeElement) ?? false
  focus.value = listRefocus(before ?? [], after, focus.value)
  if (had && focus.value) {
    void nextTick(() => {
      if (focus.value) rowOf(focus.value)?.focus()
    })
  }
})

function onKeydown(e: KeyboardEvent) {
  if (e.altKey || e.ctrlKey || e.metaKey) return
  if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
    e.preventDefault()
    step(e.key === 'ArrowDown' ? 1 : -1)
  }
}

/** An actor's name: the person's, or the agent's from the catalog. */
function actorName(actor: string): string {
  const a = assigneeOf(actor)
  return a.kind === 'agent' ? (props.agents.find((x) => x.id === a.name)?.name ?? a.name) : a.name
}

const iconColor: Record<string, string> = { neutral: 'text-muted', primary: 'text-primary', success: 'text-success', info: 'text-info', warning: 'text-warning', error: 'text-error' }

const selecting = computed(() => !!props.selected?.size)

/** A ticket's milestone's name, '' when it has none (or its row has gone). */
function milestoneOf(t: TicketSummary): string {
  return (t.milestoneId && props.milestones.get(t.milestoneId)?.name) || ''
}
/** A ticket's labels' names, in its order. */
function labelNames(t: TicketSummary): string[] {
  return t.labels.map((id) => props.labels.get(id)?.name ?? 'label')
}

defineExpose({ step, leave, focus, focusKey })
</script>

<template>
  <div ref="root" class="@container flex flex-col divide-y divide-default" :style="{ '--ticket-select': selecting ? '1.25rem' : '0px', '--ticket-tracks': TICKET_ROW_TRACKS }" data-ticket-list @keydown="onKeydown">
    <NuxtLink
      v-for="t in tickets"
      :key="t.id"
      :to="ticketPath(t.key)"
      class="flex flex-col gap-1 px-4 py-2.5 outline-none hover:bg-elevated/50 focus-visible:bg-elevated focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-primary sm:px-6 @min-[48rem]:grid @min-[48rem]:grid-cols-(--ticket-tracks) @min-[48rem]:items-center @min-[48rem]:gap-x-3"
      :class="[focus === t.key && 'bg-elevated/60', selected?.has(t.key) && 'bg-primary/10 hover:bg-primary/15']"
      :data-ticket-row="t.key"
      :data-ticket-row-status="t.status"
      :data-selected="selected?.has(t.key) ? '' : undefined"
      :aria-current="selected?.has(t.key) ? 'true' : undefined"
      @focus="focus = t.key"
    >
      <!-- Line one below 48rem: the select box, the status, the key and the title, which wraps. From 48rem its cells are the row's own. -->
      <span class="flex min-w-0 items-center gap-2 @min-[48rem]:contents">
        <span class="min-w-0" :class="selecting ? 'flex flex-none' : 'hidden @min-[48rem]:block'">
          <span v-if="selecting" class="grid size-5 place-items-center text-muted hover:text-highlighted" :title="selected?.has(t.key) ? `Leave ${t.key} out of the selection` : `Select ${t.key}`" data-row-select @click.prevent.stop="emit('toggle', t.key)">
            <UIcon :name="selected?.has(t.key) ? 'i-lucide-square-check' : 'i-lucide-square'" class="size-4" :class="selected?.has(t.key) && 'text-primary'" />
            <span class="sr-only">{{ selected?.has(t.key) ? 'selected' : 'not selected' }}</span>
          </span>
        </span>
        <UIcon :name="statusWord(t.status).icon" class="size-4 flex-none" :class="iconColor[statusWord(t.status).color]" :title="ticketWords(t).text" data-row-status-icon />
        <span class="sr-only">{{ ticketWords(t).text }}</span>
        <span class="w-[4.5rem] flex-none truncate font-mono text-xs text-muted" data-row-key>{{ t.key }}</span>
        <span class="flex min-w-0 flex-1 items-center gap-2" data-row-title-cell>
          <span class="min-w-0 break-words text-sm text-highlighted @min-[48rem]:truncate" :title="t.title" data-row-title>{{ t.title }}</span>
          <UBadge v-if="t.blocked && t.status !== 'done' && t.status !== 'cancelled'" label="blocked" color="warning" variant="subtle" size="sm" class="flex-none" data-row-blocked />
          <span v-if="t.labels.length" class="hidden flex-none items-center gap-1.5 text-xs text-muted @min-[48rem]:flex" :title="labelNames(t).join(', ')" data-row-labels>
            <span v-for="id in t.labels.slice(0, 2)" :key="id" class="flex max-w-28 items-center gap-1 rounded-full px-1.5 py-0.5 ring ring-default" data-row-label>
              <span class="size-1.5 flex-none rounded-full" :class="labelDot(labels.get(id)?.color ?? 'gray')" />
              <span class="truncate">{{ labels.get(id)?.name ?? 'label' }}</span>
            </span>
            <span v-if="t.labels.length > 2" class="tabular-nums" data-row-labels-more>+{{ t.labels.length - 2 }}</span>
          </span>
        </span>
      </span>

      <!--
        Line two below 48rem, under a 24 px indent: a grid of the priority, the owner's initials, the assignees, the milestone and the
        labels (cut), and the age, so that the priority and the age line up from row to row there too. From 48rem its cells are the
        row's own, in the tracks' order: milestone, priority, owner, assignees, age. A cell the ticket has nothing for is drawn empty.
      -->
      <span class="grid min-w-0 grid-cols-[1.25rem_1.5rem_4.5rem_minmax(0,1fr)_3rem] items-center gap-x-2 pl-6 text-xs text-muted @min-[48rem]:contents">
        <span class="order-4 flex h-6 min-w-0 items-center gap-1 @min-[48rem]:order-none" :title="milestoneOf(t) ? `Milestone: ${milestoneOf(t)}` : undefined" data-row-milestone :data-empty="milestoneOf(t) ? undefined : ''">
          <template v-if="milestoneOf(t)">
            <UIcon name="i-lucide-milestone" class="size-3.5 flex-none" />
            <span class="truncate">{{ milestoneOf(t) }}</span>
          </template>
          <span v-if="milestoneOf(t) && t.labels.length" class="flex-none @min-[48rem]:hidden" aria-hidden="true">·</span>
          <span v-if="t.labels.length" class="min-w-0 truncate @min-[48rem]:hidden">{{ labelNames(t).join(', ') }}</span>
        </span>
        <span class="order-1 flex h-6 items-center @min-[48rem]:order-none" :title="priorityWord(t.priority).label" data-row-priority :data-empty="t.priority === 0 ? '' : undefined">
          <UIcon :name="priorityWord(t.priority).icon" class="size-4 flex-none" :class="t.priority === 4 ? 'text-error' : t.priority === 0 ? 'text-dimmed' : 'text-muted'" />
          <span class="sr-only">{{ priorityWord(t.priority).label }}</span>
        </span>
        <span class="order-2 flex h-6 min-w-0 items-center gap-1 @min-[48rem]:order-none" :title="t.owner ? `Owner: ${t.owner}` : undefined" data-row-owner :data-empty="t.owner ? undefined : ''">
          <template v-if="t.owner">
            <span class="grid size-5 flex-none place-items-center rounded-full text-[9px] font-semibold @min-[48rem]:hidden" :class="avatarTone(t.owner)" aria-hidden="true">{{ initials(t.owner) }}</span>
            <UIcon name="i-lucide-user" class="hidden size-3.5 flex-none @min-[48rem]:block" />
            <span class="sr-only @min-[48rem]:hidden">{{ t.owner }}</span>
            <span class="hidden min-w-0 truncate @min-[48rem]:block">{{ t.owner }}</span>
          </template>
        </span>
        <span class="order-3 flex h-6 min-w-0 items-center" :title="t.assignees.length ? `Assigned to ${t.assignees.map(actorName).join(', ')}` : undefined" data-row-assignees :data-empty="t.assignees.length ? undefined : ''">
          <span v-for="(a, i) in t.assignees.slice(0, 3)" :key="a" class="flex-none rounded-full ring-2 ring-(--ui-bg)" :class="i > 0 && '-ml-2'">
            <SessionAvatar v-if="assigneeOf(a).kind === 'agent'" :agent-id="assigneeOf(a).name" />
            <span v-else class="grid size-6 place-items-center rounded-full text-[10px] font-semibold" :class="avatarTone(assigneeOf(a).name)">{{ initials(assigneeOf(a).name) }}</span>
          </span>
          <span v-if="t.assignees.length > 3" class="-ml-2 grid size-6 flex-none place-items-center rounded-full bg-elevated text-[10px] ring-2 ring-(--ui-bg)">+{{ t.assignees.length - 3 }}</span>
        </span>
        <time class="order-5 block text-right tabular-nums @min-[48rem]:order-none" :datetime="t.updatedAt" :title="`Updated ${new Date(t.updatedAt).toLocaleString()}`" data-row-updated>{{ relativeTime(t.updatedAt, now) }}</time>
      </span>
    </NuxtLink>
  </div>
</template>
