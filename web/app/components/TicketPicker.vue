<script setup lang="ts">
import type { TicketSummary } from '~/composables/useDispatch'
import { DispatchOrder, through } from '~/utils/dispatchOrder'
import { pickerSearch, rankTickets, statusWord, ticketSummaryOf } from '~/utils/dispatch'

/**
 * A ticket of a project, picked by its key or its title (docs/plans/dispatch.md, "web/app"): for a parent, and for a relation's other
 * end. What is typed is searched on the server (the list's `q`, the project's tickets, live ones alone) a moment after the typing
 * stops, its pages read on while they hold fewer than a page's worth that rank, and the ticket whose key is typed whole read by itself
 * (pickerSearch), then ranked here (rankTickets: the key typed whole first, then the keys it begins, then the titles), the tickets
 * `exclude` names left out (the page's own, those related already). Each answer goes through a gate of its own, so a slow one never
 * replaces the answer to what was typed since.
 */
const props = defineProps<{
  project: string
  /** The ids of the tickets not to offer. */
  exclude?: readonly string[]
  placeholder?: string
  disabled?: boolean
}>()
const emit = defineEmits<{ pick: [ticket: TicketSummary] }>()

const api = useDispatchApi()
const gate = new DispatchOrder()
const term = ref('')
const found = ref<TicketSummary[]>([])
const loading = ref(false)
const open = ref(false)

/** How long the typing rests before it is searched. */
const SEARCH_REST_MS = 200
let timer: ReturnType<typeof setTimeout> | undefined

async function search() {
  const typed = term.value
  const project = props.project
  const exclude = new Set(props.exclude ?? [])
  const reads = { list: (query: string) => api.tickets(query), ticket: async (key: string) => ticketSummaryOf(await api.ticket(key)) }
  loading.value = true
  try {
    await through(gate, 'search', () => pickerSearch(project, typed, reads, exclude), (tickets) => (found.value = tickets))
  } catch {
    // What was found before stays offered.
  } finally {
    loading.value = false
  }
}

watch(term, () => {
  clearTimeout(timer)
  timer = setTimeout(() => void search(), SEARCH_REST_MS)
})
watch(open, (o) => {
  if (o) void search()
  else term.value = ''
})
onBeforeUnmount(() => clearTimeout(timer))

const items = computed(() =>
  rankTickets(term.value, found.value, new Set(props.exclude ?? [])).map((t) => ({
    label: `${t.key} ${t.title}`,
    value: t.key,
    icon: statusWord(t.status).icon,
    ticket: t,
  })),
)

/**
 * Counts the picks: the menu is made again after each, so it holds no value of its own. Holding the last one, it would show it as if
 * chosen, and a pick of the same ticket again (another relation with it, or one tried again after a refusal) would change nothing and
 * never be emitted.
 */
const picks = ref(0)

function pick(v: unknown) {
  const t = found.value.find((x) => x.key === v)
  open.value = false
  picks.value++
  if (t) emit('pick', t)
}
</script>

<template>
  <USelectMenu
    :key="picks"
    v-model:open="open"
    v-model:search-term="term"
    :model-value="undefined"
    :items="items"
    value-key="value"
    ignore-filter
    :loading="loading"
    :disabled="disabled"
    :placeholder="placeholder ?? 'A ticket…'"
    :search-input="{ placeholder: 'A key or words of its title…' }"
    size="sm"
    class="w-full"
    data-ticket-picker
    @update:model-value="pick"
  >
    <template #item-label="{ item }">
      <span class="flex min-w-0 items-baseline gap-2" :data-ticket-pick="item.value">
        <span class="flex-none font-mono text-xs text-muted">{{ item.ticket.key }}</span>
        <span class="truncate">{{ item.ticket.title }}</span>
      </span>
    </template>
    <template #empty>{{ loading ? 'Searching…' : 'No ticket matches.' }}</template>
  </USelectMenu>
</template>
