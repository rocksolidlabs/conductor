<script setup lang="ts">
import type { BrakemanRequest, BrakemanRow } from '~/utils/brakeman'
import { closed, kindWords, stateWords } from '~/utils/brakeman'
import type { BrakemanListQuery } from '~/composables/useBrakeman'
import { EARLIER_FILTERS, askAgain, kindLine, localMatch, rowAge, rowMeta, rowTitle, searchProblem, subjectWords, type EarlierFilter } from '~/utils/brakemanRows'

/**
 * Earlier, on the Brakeman page (the Brakeman design review): every request done, newest first, the one the latest card shows among
 * them (`data-request-latest`). A row says what its brief found without being opened: its title, then its kind, its subjects by name
 * and its counts, and how long ago. A row's link shows its brief in the latest card (and folds the card when it shows that one); a row
 * with no brief says why, and offers Ask again where the same ask is not waiting already. All, With a decision, Briefings and Drafts
 * filter the list on the server, and a word searches it; an older server, which takes no filter, hides them and searches the pages read.
 * A filtered list reads its first page again when a brief is kept, and the rows shown keep their place; nothing takes the focus.
 */
const props = defineProps<{
  /** The latest card's request, and the one it shows now (folded or not). */
  latest: string
  shown: string
  folded: boolean
  /** Whether the server takes the list's filters: false on an older one (the latest card's read said so), undefined until known. */
  served?: boolean
  /** The requests waiting or under way: Ask again is not offered for an ask among them. */
  waiting: BrakemanRequest[]
}>()
const emit = defineEmits<{ toggle: [id: string] }>()

const bk = useBrakeman()
const live = useAttention()
const asker = useAskBrakeman()
const now = useNow()

const names = {
  session: (id: string) => live.sessions.value.find((s) => s.id === id)?.name,
  run: (id: string) => live.runNames.value[id],
}
const sessionName = (id?: string) => (id ? names.session(id) : undefined)

const filter = ref<EarlierFilter>('all')
const search = ref('')
/** The query as it was last sent (the search debounced). */
const query = ref('')
const problem = computed(() => searchProblem(search.value))
let debounce: ReturnType<typeof setTimeout> | undefined
watch(search, (v) => {
  if (debounce !== undefined) clearTimeout(debounce)
  debounce = setTimeout(() => {
    debounce = undefined
    if (!searchProblem(v)) query.value = v.trim()
  }, 300)
})
onBeforeUnmount(() => {
  if (debounce !== undefined) clearTimeout(debounce)
})

/** The server takes no filter: the filters are hidden and the search filters the pages read. */
const local = ref(false)
watch(
  () => props.served,
  (s) => {
    if (s === false) local.value = true
  },
  { immediate: true },
)

/** The list route's query for the filter and the search, or null for the live store's list (All, nothing searched). */
const asked = computed<BrakemanListQuery | null>(() => {
  if (local.value) return null
  const f = EARLIER_FILTERS.find((x) => x.key === filter.value)!
  if (!f.query && !query.value) return null
  return { ...f.query, ...(query.value ? { q: query.value } : {}) }
})

/** The filtered list as the server answered it: its ids in order, and the id to read the next page after. */
const ids = ref<string[]>([])
const next = ref('')
const reading = ref(false)
const failed = ref('')
let sent = 0
let busy = false
let again = false
/** Reads the first page of the filtered list (one read at a time: one asked meanwhile is one after); the rows shown keep their place. */
async function readFirst(fresh: boolean) {
  const q = asked.value
  if (!q) return
  if (busy) {
    again = true
    return
  }
  busy = true
  reading.value = fresh
  const mine = ++sent
  try {
    const out = await bk.list(q)
    if (mine !== sent || asked.value !== q) return
    if (!out.filtered) {
      local.value = true
      return
    }
    failed.value = ''
    const got = out.requests.map((r) => r.id)
    // A page read again: what is new goes first, what was shown keeps its place.
    ids.value = fresh ? got : [...got.filter((id) => !ids.value.includes(id)), ...ids.value]
    if (fresh) next.value = out.next
  } catch (e) {
    if (mine === sent) failed.value = (e as Error).message
  } finally {
    busy = false
    reading.value = false
    if (again) {
      again = false
      void readFirst(false)
    }
  }
}
watch(asked, (q) => {
  ids.value = []
  next.value = ''
  failed.value = ''
  sent++
  if (q) void readFirst(true)
})
// A brief kept while a filter is on (or the stream back): its first page again.
watch(
  () => bk.events.value.seq,
  () => {
    const e = bk.events.value
    if (asked.value && (e.id === '' || e.state === 'briefed' || closed(e.state))) void readFirst(false)
  },
)
const moreBusy = ref(false)
async function readMore() {
  const q = asked.value
  if (!q) return bk.more()
  if (!next.value) return
  moreBusy.value = true
  try {
    const out = await bk.list({ ...q, after: next.value })
    if (asked.value !== q) return
    ids.value = [...ids.value, ...out.requests.map((r) => r.id).filter((id) => !ids.value.includes(id))]
    next.value = out.next
  } catch (e) {
    failed.value = (e as Error).message
  } finally {
    moreBusy.value = false
  }
}

/** Every request done the live store holds, newest first. */
const done = computed(() => bk.requests.value.filter((r) => closed(r.state)))
/** The rows shown: the filtered list's (each as the live store has it now), the live store's, or the pages read searched here. */
const rows = computed<BrakemanRow[]>(() => {
  if (asked.value) {
    const byId = new Map(bk.requests.value.map((r) => [r.id, r]))
    return ids.value.map((id) => byId.get(id)).filter((r): r is BrakemanRow => !!r)
  }
  if (local.value && search.value.trim()) return done.value.filter((r) => localMatch(r, search.value, names))
  return done.value
})
const hasMore = computed(() => (asked.value ? next.value !== '' : bk.hasMore.value))
const searched = computed(() => !!asked.value || (local.value && !!search.value.trim()))

function clear() {
  filter.value = 'all'
  search.value = ''
  query.value = ''
}

/** A row's first line: its brief's title, else (an older server's row) its id and kind. */
const title = (r: BrakemanRow) => rowTitle(r) || `${r.id} · ${kindWords(r.kind)}`
/** A row with no brief: "<kind> · <subjects>" then why. */
const what = (r: BrakemanRow) => {
  const subjects = subjectWords(r.subjects, names)
  return subjects ? `${kindLine(r)} · ${subjects}` : kindLine(r)
}

const asking = ref('')
async function ask(r: BrakemanRow) {
  const body = askAgain(r, props.waiting)
  if (!body) return
  asking.value = r.id
  try {
    await asker.tell(body)
  } finally {
    asking.value = ''
  }
}
function openRow(e: MouseEvent, id: string) {
  // A plain click shows the brief in the card; a click with a modifier opens the link where the browser puts it.
  if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return
  e.preventDefault()
  emit('toggle', id)
}
</script>

<template>
  <section class="flex min-w-0 flex-col gap-3" data-brakeman-briefs :data-briefs-filtered="local ? 'local' : 'server'">
    <div class="flex min-w-0 flex-wrap items-center gap-2">
      <h2 class="mr-2 text-sm font-semibold text-highlighted">Earlier</h2>
      <div v-if="!local" class="flex flex-wrap items-center gap-1" role="group" aria-label="Show">
        <UButton
          v-for="f in EARLIER_FILTERS"
          :key="f.key"
          :label="f.label"
          size="sm"
          :color="filter === f.key ? 'primary' : 'neutral'"
          :variant="filter === f.key ? 'soft' : 'ghost'"
          class="max-sm:min-h-11"
          :aria-pressed="filter === f.key"
          :data-briefs-filter="f.key"
          @click="filter = f.key"
        />
      </div>
      <UInput
        v-model="search"
        icon="i-lucide-search"
        size="sm"
        placeholder="a run, a ticket, a word"
        aria-label="Search briefs"
        class="min-w-0 flex-1 sm:ml-auto sm:max-w-64 max-sm:basis-full"
        :ui="{ base: 'max-sm:min-h-11' }"
        data-briefs-search
      />
    </div>
    <p v-if="problem" class="text-xs text-error" data-briefs-search-problem>{{ problem }}</p>
    <p v-if="local && search.trim()" class="text-xs text-muted" data-briefs-local>Only the pages read are searched.</p>
    <UAlert v-if="failed" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="failed" />

    <p v-if="reading && !rows.length" class="text-sm text-muted">Reading…</p>
    <div v-else-if="searched && !rows.length" class="flex flex-wrap items-center gap-2 text-sm text-muted" data-briefs-none>
      <span>No brief matches.</span>
      <UButton label="Clear" size="sm" color="neutral" variant="ghost" class="max-sm:min-h-11" @click="clear" />
    </div>

    <ul v-if="rows.length" class="flex min-w-0 flex-col divide-y divide-default rounded-md ring ring-default">
      <li
        v-for="r in rows"
        :key="r.id"
        class="grid min-w-0 grid-cols-[minmax(0,1fr)_auto] items-start gap-x-3 gap-y-0.5 px-3 py-2.5"
        :data-request="r.id"
        :data-request-state="r.state"
        :data-request-latest="r.id === latest ? '' : undefined"
      >
        <div class="flex min-w-0 items-center gap-2">
          <NuxtLink
            v-if="r.hasBrief"
            :to="`/brakeman?request=${encodeURIComponent(r.id)}`"
            class="min-w-0 truncate text-sm font-medium text-highlighted hover:underline max-sm:py-2.5"
            :title="title(r)"
            :aria-current="shown === r.id && !folded ? 'true' : undefined"
            data-request-open
            data-request-title
            @click="openRow($event, r.id)"
            >{{ title(r) }}</NuxtLink
          >
          <span v-else class="min-w-0 truncate text-sm" :title="what(r)" data-request-title>{{ what(r) }}</span>
          <UBadge v-if="r.hasBrief && bk.unopened.value.includes(r.id)" label="new" color="primary" variant="subtle" size="sm" class="flex-none" data-request-new />
        </div>
        <span class="row-span-2 text-right text-xs text-muted tabular-nums" data-request-age>{{ rowAge(r, now) }}</span>
        <p class="flex min-w-0 flex-wrap items-center gap-x-1.5 text-xs text-muted" data-request-meta>
          <template v-if="r.hasBrief">
            <template v-for="(part, i) in rowMeta(r, names, sessionName(r.sessionId))" :key="i">
              <span v-if="i" aria-hidden="true">·</span>
              <span class="min-w-0 break-words">{{ part }}</span>
            </template>
          </template>
          <template v-else>
            <span>no brief: <span data-request-words>{{ stateWords(r, sessionName(r.sessionId)) }}</span></span>
            <UButton
              v-if="askAgain(r, waiting)"
              label="Ask again"
              size="xs"
              color="neutral"
              variant="outline"
              class="ml-1 max-sm:min-h-11"
              :loading="asking === r.id"
              data-request-again
              @click="ask(r)"
            />
          </template>
        </p>
      </li>
    </ul>
    <UButton v-if="hasMore" label="Older requests" color="neutral" variant="outline" class="self-start max-sm:min-h-11" :loading="moreBusy" data-briefs-more @click="readMore()" />
  </section>
</template>
