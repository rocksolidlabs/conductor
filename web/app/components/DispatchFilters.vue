<script setup lang="ts">
import type { DispatchLabel, DispatchMilestone, DispatchSort, Horizon, TicketStatus } from '~/composables/useDispatch'
import type { AgentInfo } from '~/composables/useSessions'
import { HORIZONS, LIST_SORTS, PRIORITIES, TICKET_STATUSES, labelDot, sortDesc, statusWord } from '~/utils/dispatch'
import { emptyFilter, filterChips, filterCount, filterNarrows, MAX_SEARCH, milestoneChoices, type DispatchFilter, type FilterChip } from '~/utils/dispatchQuery'
import { DispatchOrder, through } from '~/utils/dispatchOrder'

/**
 * A project's toolbar (docs/plans/dispatch.md, "web/app"), one row from `sm` up: the page's view tabs (`#leading`), the search, Filter,
 * a chip per value of the filter in use, each taking itself out, and Clear; the page's count and Views (`#trailing`); then the sort and
 * its direction. Filter opens a popover of the eight filters, status, priority, labels, owner, assignees, milestone, horizon and blocked,
 * in two columns, and says how many values apply besides the search (filterCount: a parent and deleted, which only the link sets, are
 * chips too). Chips that do not fit wrap onto a second line of the toolbar. Below `sm` it is two lines: the tabs and the search, then
 * Filter, Views, the sort and the count, the chips under them. The filter is the page's link (utils/dispatchQuery.ts): each change is
 * a new filter, which the page writes to the URL and reads the list by. The search waits for a pause in the typing. People are read from
 * the directory when a menu that offers them opens.
 */
const props = defineProps<{
  modelValue: DispatchFilter
  labels: readonly DispatchLabel[]
  milestones: readonly DispatchMilestone[]
  agents: readonly AgentInfo[]
}>()
defineSlots<{ leading?: () => unknown; trailing?: () => unknown }>()
const emit = defineEmits<{ 'update:modelValue': [filter: DispatchFilter] }>()

function set<K extends keyof DispatchFilter>(key: K, value: DispatchFilter[K]) {
  emit('update:modelValue', { ...props.modelValue, [key]: value })
}

// The search: what is typed shows at once, and the list is asked once the typing pauses.
const SEARCH_PAUSE_MS = 300
const search = ref(props.modelValue.q)
let searchTimer: number | undefined
watch(
  () => props.modelValue.q,
  (q) => {
    if (q !== search.value.slice(0, MAX_SEARCH)) search.value = q
  },
)
function onSearch(v: string | number) {
  search.value = String(v)
  window.clearTimeout(searchTimer)
  searchTimer = window.setTimeout(() => set('q', search.value.slice(0, MAX_SEARCH)), SEARCH_PAUSE_MS)
}
onBeforeUnmount(() => window.clearTimeout(searchTimer))

const api = useDispatchApi()
const people = ref<string[]>([])
// The directory read whole, through a gate of its own (utils/dispatchOrder.ts): a slow reply never replaces a newer one.
const gate = new DispatchOrder()
async function loadPeople(open: boolean) {
  if (!open) return
  try {
    await through(gate, 'people', () => api.people(), (list) => (people.value = list.map((p) => p.name)))
  } catch {
    // The names read before stay offered.
  }
}

const statusItems = TICKET_STATUSES.map((s) => ({ label: statusWord(s).label, value: s, icon: statusWord(s).icon }))
const priorityItems = [...PRIORITIES].reverse().map((p) => ({ label: p.label, value: p.value, icon: p.icon }))
// A menu's value is never empty: not planned goes as its name.
const horizonItems = HORIZONS.map((h) => ({ label: h.label, value: h.name, icon: h.icon }))
const labelItems = computed(() => [...new Set(props.labels.map((l) => l.name))].sort((a, b) => a.localeCompare(b)).map((name) => ({ label: name, value: name, color: props.labels.find((l) => l.name === name)?.color ?? 'gray' })))
// Each milestone once, one whose name another has too by its id (milestoneChoices), so that every one can be chosen.
const milestoneItems = computed(() => milestoneChoices(props.milestones))
const ownerItems = computed(() => [...new Set([...people.value, ...(props.modelValue.owner ? [props.modelValue.owner] : [])])].sort((a, b) => a.localeCompare(b)))
const assigneeItems = computed(() => [
  ...[...new Set([...people.value.map((n) => `person:${n}`), ...props.modelValue.assignees.filter((a) => a.startsWith('person:'))])].map((value) => ({ label: value.slice(7), value, icon: 'i-lucide-user' })),
  ...props.agents.map((a) => ({ label: a.name, value: `agent:${a.id}`, icon: 'i-lucide-bot' })),
])
const sortItems = LIST_SORTS.map((s) => ({ label: s.label, value: s.value }))

const horizonNames = computed(() => props.modelValue.horizon.map((h) => HORIZONS.find((x) => x.value === h)?.name ?? 'none'))
function setHorizons(v: unknown) {
  const names = Array.isArray(v) ? v : []
  set(
    'horizon',
    HORIZONS.filter((h) => names.includes(h.name)).map((h) => h.value as Horizon),
  )
}
function setSort(v: unknown) {
  if (typeof v !== 'string' || v === props.modelValue.sort) return
  // A sort is read in its own direction until turned.
  emit('update:modelValue', { ...props.modelValue, sort: v as DispatchSort, desc: sortDesc(v) })
}

const narrows = computed(() => filterNarrows(props.modelValue))
/** The values in use, as chips; Filter's number is theirs without the search's. */
const chips = computed(() => filterChips(props.modelValue, { labels: props.labels, milestones: props.milestones }, props.agents))
const count = computed(() => filterCount(chips.value))
/** The popover of the eight filters. */
const filterOpen = ref(false)
function clear() {
  window.clearTimeout(searchTimer)
  search.value = ''
  emit('update:modelValue', { ...emptyFilter(), sort: props.modelValue.sort, desc: props.modelValue.desc, group: props.modelValue.group })
}
/** A chip's x: the filter without its value; the search's empties the field at once, a typing's pause not waited for. */
function takeOut(c: FilterChip) {
  if (c.field === 'q') {
    window.clearTimeout(searchTimer)
    search.value = ''
  }
  emit('update:modelValue', c.without)
}

/** Opens the popover of the filters (the palette's "Filter the tickets…"). */
function openFilter() {
  filterOpen.value = true
}
defineExpose({ openFilter })

const strings = (v: unknown) => (Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [])
</script>

<template>
  <div class="flex flex-wrap items-center gap-1.5 sm:gap-2" data-dispatch-toolbar data-dispatch-filters>
    <div class="order-1 flex flex-none items-center sm:order-none">
      <slot name="leading" />
    </div>
    <UInput :model-value="search" icon="i-lucide-search" placeholder="Search the tickets…" size="sm" class="order-2 min-w-0 flex-1 sm:order-none sm:w-56 sm:flex-none" :maxlength="MAX_SEARCH" aria-label="Search the tickets" data-filter-search @update:model-value="onSearch" />
    <!-- Below sm: the line breaks here, under the tabs and the search. -->
    <span class="order-3 h-0 basis-full sm:hidden" aria-hidden="true" />
    <UPopover v-model:open="filterOpen" :content="{ align: 'start', sideOffset: 6 }">
      <UButton :label="count ? `Filter · ${count}` : 'Filter'" icon="i-lucide-list-filter" size="sm" color="neutral" :variant="filterOpen ? 'soft' : 'outline'" class="order-4 flex-none sm:order-none" :aria-expanded="filterOpen" :data-count="count" data-filter-open />
      <template #content>
        <div class="grid w-[min(22rem,calc(100vw-2rem))] grid-cols-2 gap-2 p-3" data-filter-popover data-filter-fields>
          <USelectMenu
            :model-value="modelValue.status"
            :items="statusItems"
            value-key="value"
            multiple
            :search-input="false"
            placeholder="Status"
            size="sm"
            class="w-full"
            data-filter-status
            @update:model-value="(v: unknown) => set('status', strings(v) as TicketStatus[])"
          />
          <USelectMenu
            :model-value="modelValue.priority"
            :items="priorityItems"
            value-key="value"
            multiple
            :search-input="false"
            placeholder="Priority"
            size="sm"
            class="w-full"
            data-filter-priority
            @update:model-value="(v: unknown) => set('priority', Array.isArray(v) ? v.filter((x): x is number => typeof x === 'number') : [])"
          />
          <USelectMenu :model-value="modelValue.labels" :items="labelItems" value-key="value" multiple placeholder="Labels" size="sm" class="w-full" data-filter-labels @update:model-value="(v: unknown) => set('labels', strings(v))">
            <template #item-leading="{ item }">
              <span class="size-2 rounded-full" :class="labelDot(item.color)" />
            </template>
            <template #empty>No labels in this project yet.</template>
          </USelectMenu>
          <USelectMenu
            :model-value="modelValue.owner || undefined"
            :items="ownerItems"
            clear
            placeholder="Owner"
            size="sm"
            class="w-full"
            data-filter-owner
            @update:open="loadPeople"
            @update:model-value="(v: unknown) => set('owner', typeof v === 'string' ? v : '')"
          />
          <USelectMenu :model-value="modelValue.assignees" :items="assigneeItems" value-key="value" multiple placeholder="Assignees" size="sm" class="w-full" data-filter-assignees @update:open="loadPeople" @update:model-value="(v: unknown) => set('assignees', strings(v))" />
          <USelectMenu :model-value="modelValue.milestone || undefined" :items="milestoneItems" value-key="value" clear placeholder="Milestone" size="sm" class="w-full" data-filter-milestone @update:model-value="(v: unknown) => set('milestone', typeof v === 'string' ? v : '')">
            <template #empty>No milestones in this project yet.</template>
          </USelectMenu>
          <USelectMenu :model-value="horizonNames" :items="horizonItems" value-key="value" multiple :search-input="false" placeholder="Horizon" size="sm" class="w-full" data-filter-horizon @update:model-value="setHorizons" />
          <UButton
            label="Blocked"
            icon="i-lucide-octagon-pause"
            size="sm"
            color="neutral"
            :variant="modelValue.blocked ? 'soft' : 'outline'"
            :aria-pressed="modelValue.blocked"
            class="w-full"
            data-filter-blocked
            @click="set('blocked', !modelValue.blocked)"
          />
        </div>
      </template>
    </UPopover>
    <!--
      The values in use: below sm a line of their own under the rest; from sm each chip is the toolbar's own, after Filter, and those that
      do not fit go onto a second line of the toolbar, with what follows them.
    -->
    <div v-if="chips.length || narrows" class="order-8 flex min-w-0 basis-full flex-wrap items-center gap-1.5 sm:contents" data-filter-chips>
      <span v-for="c in chips" :key="c.id" class="flex h-6 max-w-56 flex-none items-center gap-1 rounded-md bg-elevated py-0.5 pr-0.5 pl-2 text-xs text-default ring ring-default" :title="c.words" :data-filter-chip="c.id">
        <span class="min-w-0 truncate">{{ c.words }}</span>
        <button type="button" class="grid size-5 flex-none place-items-center rounded text-muted hover:bg-accented hover:text-highlighted focus-visible:outline-2 focus-visible:outline-primary" :aria-label="`Take out ${c.words}`" data-filter-chip-remove @click="takeOut(c)">
          <UIcon name="i-lucide-x" class="size-3.5" />
        </button>
      </span>
      <UButton v-if="narrows" label="Clear" icon="i-lucide-x" size="xs" color="neutral" variant="ghost" class="flex-none" data-filter-clear @click="clear" />
    </div>
    <span class="hidden sm:block sm:flex-1" aria-hidden="true" />
    <!-- The page's count and Views: each child places itself below sm (order). -->
    <slot name="trailing" />
    <div class="order-6 flex flex-none items-center gap-1 sm:order-none">
      <USelect :model-value="modelValue.sort" :items="sortItems" size="sm" class="w-28 sm:w-32" aria-label="Sort by" data-filter-sort @update:model-value="setSort" />
      <IconButton
        :icon="modelValue.desc ? 'i-lucide-arrow-down-wide-narrow' : 'i-lucide-arrow-up-narrow-wide'"
        size="sm"
        color="neutral"
        variant="outline"
        :label="modelValue.desc ? 'Descending: turn it' : 'Ascending: turn it'"
        data-filter-direction
        @click="set('desc', !modelValue.desc)"
      />
    </div>
  </div>
</template>
