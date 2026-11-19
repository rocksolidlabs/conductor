<script setup lang="ts">
import {
  activeFilters,
  FILTER_WORDS,
  classChips,
  clearedQuery,
  cutWords,
  filterOf,
  historyNoneWords,
  historyQuestions,
  indexingWords,
  machineOf,
  outcomeChips,
  sinceChips,
  withFilter,
  withoutFilter,
  type ActiveFilter,
  type HistoryFilter,
} from '~/utils/history'
import { pageState } from '~/utils/pageState'
import { agentChips, byDay } from '~/utils/historyRows'
import type { FilterGroup } from '~/components/HistoryFilterGroups.vue'
import { LATEST_SESSIONS } from '~/composables/usePaletteHistory'
import type { HistoryOptions } from '~/composables/useHistorySearch'
import type { ProjectsPage } from '~/composables/useDispatch'
import type { Integration } from '~/composables/useSessions'

/**
 * The History page (Program item HS1, docs/plans/history.md, "web/app", PR-7): the palette's `~` search with room. The search box
 * takes the palette's grammar; the chips write the query's filters (the kind of evidence, the outcome, the project from Dispatch's
 * projects, the host from the records, the agent from the catalog, since), so the query is the one truth and the URL keeps it
 * (`?q=`): a search is a link. Every result, paged with Show more (the reply's `next`), under the heading of its local day (#1024),
 * each row its name first (HistoryRow). Nothing typed: the four questions, and the latest sessions. On a phone, one column: the
 * results right after the search, the chip groups behind Filters.
 *
 * Its second view, Live (`?view=feed`, the UX review's P12: the retired Events page's feed), is the live feed of what agents report
 * with its last hour beside it, kept in this tab's memory (useEvents): it says so, from the moment this page opened.
 */
const route = useRoute()
const router = useRouter()
const admin = useWorkbenchToken()
const { request } = useApi()

/** The page's view, in the address: Search (the default) or Feed. */
type HistoryView = 'search' | 'feed'
const view = computed<HistoryView>({
  get: () => (route.query.view === 'feed' ? 'feed' : 'search'),
  set: (v) => void router.replace({ query: { ...route.query, view: v === 'feed' ? 'feed' : undefined } }),
})
const views = [
  { label: 'Search', value: 'search', icon: 'i-lucide-search' },
  { label: 'Live', value: 'feed', icon: 'i-lucide-radio-tower' },
]
useHead({ title: () => (view.value === 'feed' ? 'History · Live' : 'History') })

// The feed: filled from the live stream on every page (useEvents), its aside's Reporting list from the integrations, read while the
// Feed is shown.
const sessionsApi = useSessions()
const attention = useAttention()
const events = useEvents()
const integrations = ref<Integration[]>([])
const feedLoading = ref(false)
const feedError = ref('')
async function refreshFeed() {
  if (!admin.hasToken.value) {
    admin.needsToken.value = true
    return
  }
  feedLoading.value = true
  try {
    integrations.value = (await sessionsApi.integrations()).integrations
    feedError.value = ''
  } catch (e) {
    feedError.value = (e as Error).message
  } finally {
    feedLoading.value = false
  }
}
onMounted(() => attention.start())
watch(
  () => [view.value, admin.token.value] as const,
  ([v]) => {
    if (v === 'feed') void refreshFeed()
  },
  { immediate: true },
)

// This page's group in the command palette: its two views, and Refresh while the Feed is shown.
usePalette().registerPage(() => ({
  label: 'History',
  rows: [
    ...views.map((v) => ({ key: `page:view:${v.value}`, label: v.label, icon: v.icon, on: view.value === v.value, run: () => (view.value = v.value as HistoryView) })),
    ...(view.value === 'feed' ? [{ key: 'page:refresh', label: 'Refresh', icon: 'i-lucide-refresh-cw', run: refreshFeed }] : []),
  ],
}))

// The chips' values: the projects Dispatch keeps, the hosts the records name, the agents of the catalog. Read under the history's read
// rule (useHistorySearch: bounded, cancelled by the next, cleared and asked again when the token changes); a source that fails leaves
// its row out.
async function readOptions(signal: AbortSignal): Promise<HistoryOptions> {
  const [p, r, a] = await Promise.allSettled([
    request<ProjectsPage>('/api/dispatch/projects', { signal }),
    request<{ records: Array<{ host?: { name?: string }; agent?: { id?: string } }> }>('/api/history/sessions', { query: { limit: '100' }, signal }),
    request<{ agents?: Array<{ id: string; name: string }> }>('/api/catalog', { signal }),
  ])
  return {
    projects: p.status === 'fulfilled' ? (p.value.projects ?? []).filter((x) => !x.archivedAt).map((x) => x.key) : [],
    hosts: r.status === 'fulfilled' ? [...new Set(r.value.records.map((x) => x.host?.name ?? '').filter(Boolean))].sort() : [],
    agents: a.status === 'fulfilled' ? (a.value.agents ?? []).map((x) => ({ id: x.id, name: x.name })) : [],
    // Each agent's sessions among the latest records: the Agent chips put the agents that have history first (#1024).
    used: r.status === 'fulfilled' ? r.value.records.reduce<Record<string, number>>((n, x) => (x.agent?.id ? { ...n, [x.agent.id]: (n[x.agent.id] ?? 0) + 1 } : n), {}) : {},
  }
}
const search = useHistorySearch(undefined, readOptions)
void search.readOptions()

const text = ref(typeof route.query.q === 'string' ? route.query.q : '')
// The URL follows the box, and the box the URL (Back, Forward, a link opened): one query either way.
watch(
  () => route.query.q,
  (q) => {
    const v = typeof q === 'string' ? q : ''
    if (v !== text.value) text.value = v
  },
)
watch(text, (q) => {
  const cur = typeof route.query.q === 'string' ? route.query.q : ''
  if (q.trim() === cur.trim()) return
  void router.replace({ query: { ...route.query, q: q.trim() || undefined } })
})

const query = computed(() => text.value.trim())
const asked = computed(() => query.value || LATEST_SESSIONS)
watch(
  () => [asked.value, admin.hasToken.value] as const,
  ([q, has]) => {
    if (has) search.search(q)
    else if (view.value === 'search') admin.needsToken.value = true
  },
  { immediate: true },
)

const now = ref(Date.now())
let tick: ReturnType<typeof setInterval> | undefined
onMounted(() => (tick = setInterval(() => (now.value = Date.now()), 15_000)))
onBeforeUnmount(() => clearInterval(tick))

const shown = computed(() => (search.result.value?.q === asked.value ? search.result.value.reply : null))
const error = computed(() => (search.error.value?.q === asked.value ? search.error.value : null))
// The rows under day headings, by the person's local day (#1024).
const days = computed(() => byDay(shown.value?.entries ?? [], now.value))
const indexing = computed(() => indexingWords(shown.value?.indexing))
// What the page shows of its answer (P23): reading, a first read that failed, nothing kept or nothing matched, or the entries. With
// entries the search's caption folds to About this page.
const state = computed(() => pageState({ read: !!shown.value, error: error.value?.message, count: shown.value?.entries.length ?? 0 }))
function readAgain() {
  search.search(asked.value)
}

// ---- The chips' values, as read above: none until read, and none again once the token is forgotten.
const projects = computed(() => search.options.value?.projects ?? [])
const hosts = computed(() => search.options.value?.hosts ?? [])
const agents = computed(() => search.options.value?.agents ?? [])

// This machine's name, from the latest sessions' records (machineOf: a session on this server, never a hosted one's host); until it is
// known, "Sessions on this machine" asks for every session (`kind:session`) rather than name a host the records hold, which may be
// another machine's (a hosted session's) or this one's before a rename.
const machine = computed(() => (search.result.value?.q === LATEST_SESSIONS ? machineOf(search.result.value.reply.entries) : ''))
const project = computed(() => (projects.value.length === 1 ? projects.value[0] : undefined))
// The groups in the order a person reaches for them (#1024): Since first; the agents that have history, the most used first, the rest
// behind More agents; Project and Host only when there is a choice; then Kind of evidence and Outcome.
const chipRows = computed<FilterGroup[]>(() => {
  const agentRow = agentChips(agents.value, search.options.value?.used ?? {}, filterOf(text.value, 'agent').split(',').filter(Boolean))
  return [
    { name: 'since' as const, label: FILTER_WORDS.since, chips: sinceChips() },
    { name: 'agent' as const, label: FILTER_WORDS.agent, chips: agentRow.shown, more: agentRow.more, moreLabel: 'More agents' },
    { name: 'project' as const, label: FILTER_WORDS.project, chips: projects.value.length > 1 ? projects.value.map((k) => ({ value: k, label: k })) : [] },
    { name: 'host' as const, label: FILTER_WORDS.host, chips: hosts.value.length > 1 ? hosts.value.map((h) => ({ value: h, label: h })) : [] },
    { name: 'class' as const, label: FILTER_WORDS.class, chips: classChips() },
    // Rejected writes the rejecting outcomes the "Rejected designs" question asks for (utils/history.ts, REJECTING_OUTCOMES).
    { name: 'outcome' as const, label: FILTER_WORDS.outcome, chips: outcomeChips() },
  ].filter((r) => r.chips.length || r.more?.length)
})
// An agent's chip is pressed by its id as the server reads it: lower case, each value once (`agent:Codex`, `agent:codex,codex`).
const agentValue = (v: string) => [...new Set(v.toLowerCase().split(',').filter(Boolean))].join(',')
const chipOn = (name: HistoryFilter, value: string) => (name === 'agent' ? agentValue(filterOf(text.value, name)) === value : filterOf(text.value, name) === value)
// On a narrow window the groups wait behind Filters, its count the filters on (the words are not one).
const filtersOpen = ref(false)
const filterCount = computed(() => active.value.filter((f) => f.name !== 'words').length)
/** A chip pressed: its filter written into the query (the one value it holds), or taken out when it already held that value. */
function press(name: HistoryFilter, value: string) {
  text.value = withFilter(text.value.trim(), name, chipOn(name, value) ? '' : value)
}

// ---- What filters the search, listed under the box, each with its own ×, and Clear all beside them (#1023). Clear all empties the
// search: every filter and the words (clearedQuery). A removal and Clear all are each a step of the browser's history (push), so Back
// puts back what they took off; the route's watch above writes the box.
const agentName = (id: string) => agents.value.find((x) => x.id === id)?.name
const active = computed(() => activeFilters(text.value, agentName))
const field = useTemplateRef<{ inputRef?: HTMLInputElement }>('field')
const activeBar = useTemplateRef<HTMLElement>('activeBar')
async function writeQuery(q: string) {
  await router.push({ query: { ...route.query, q: q || undefined } })
  // The route's watch writes the box when the route moves; written here too, so that a push the router refused leaves the box right.
  text.value = q
  await nextTick()
}
/** One taken off: the focus goes to the next one's ×, else the one before it, else the box. */
async function removeFilter(f: ActiveFilter, i: number) {
  await writeQuery(withoutFilter(text.value.trim(), f))
  const xs = activeBar.value?.querySelectorAll<HTMLElement>('[data-history-active-remove]') ?? []
  const to = xs[Math.min(i, xs.length - 1)]
  if (to) to.focus()
  else field.value?.inputRef?.focus()
}
/** Clear all: the search emptied, the focus in the box. */
async function clearAll() {
  await writeQuery(clearedQuery())
  field.value?.inputRef?.focus()
}
const removeWords = (f: ActiveFilter) => (f.name === 'words' ? `Remove the words ${f.value}` : `Remove the filter ${f.label}: ${f.value}`)

const questions = computed(() => historyQuestions({ project: project.value, host: machine.value || undefined }))
/** The typed filters, in the chips' words: what About this page says of the search box (#1024). */
const TYPED = 'Filters can be typed too, each as name:value: since: (Since: 24h, 7d, 30d), agent: (Agent), project: (Project), host: (Host), class: (Kind of evidence: said, decided, checked, record), outcome: (Outcome: passed, failed), and kind:, repo:, crew:, run:, ticket:, session: and until:.'
/** The search's caption: in place while the history has nothing to show, under About this page once it has. */
const CAPTION = 'What was said (Conversation), decided (Decision) and checked (a receipt) are kept apart: a sentence repeated is still Conversation.'
</script>

<template>
  <UDashboardPanel id="history">
    <template #header>
      <UDashboardNavbar title="History">
        <template #leading>
          <UIcon name="i-lucide-history" class="size-5 text-muted" />
        </template>
        <template #right>
          <UButton v-if="view === 'feed'" label="Refresh" icon="i-lucide-refresh-cw" color="neutral" variant="outline" :loading="feedLoading" data-history-refresh @click="refreshFeed" />
          <PageAbout v-if="view === 'search'" :shown="state === 'content'">
            <p>{{ CAPTION }} The palette searches the same with <UKbd value="~" size="sm" />.</p>
            <p class="mt-2" data-history-about-typed>{{ TYPED }}</p>
          </PageAbout>
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <div class="mx-auto w-full max-w-6xl min-w-0">
        <!-- Manual activation: a view changes on a click or Enter, never on the focus coming back to a tab (the palette closing). -->
        <UTabs v-model="view" :items="views" :content="false" activation-mode="manual" color="neutral" variant="link" size="sm" aria-label="History's views" data-history-views />
      </div>
      <div v-if="view === 'feed'" class="mx-auto flex w-full max-w-6xl min-w-0 flex-1 flex-col" data-history-feed>
        <p class="text-xs text-muted" data-history-feed-caption>What agents report, from the moment this page opened.</p>
        <UAlert v-if="feedError" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="feedError" class="mt-3" />
        <div class="flex min-h-0 flex-1 flex-col xl:flex-row">
          <div class="min-w-0 flex-1 pb-6 xl:pr-6">
            <EventFeed :entries="events.entries.value" :now="now" />
          </div>
          <EventsAside :entries="events.entries.value" :now="now" :routes="events.routes.value" :integrations="integrations" class="flex-none border-t border-default pt-5 xl:w-[340px] xl:border-t-0 xl:border-l xl:pt-0 xl:pl-5" />
        </div>
      </div>
      <div v-else class="mx-auto grid w-full max-w-6xl min-w-0 grid-cols-1 gap-5 lg:grid-cols-[15rem_minmax(0,1fr)] lg:grid-rows-[auto_1fr]" data-history-page>
        <!-- The search box first on a phone, beside the chips on a wide screen. -->
        <div class="min-w-0 lg:col-start-2 lg:row-start-1" data-history-search>
          <div class="flex min-w-0 items-center gap-2">
            <UInput ref="field" v-model="text" icon="i-lucide-search" placeholder="Search the history" size="lg" class="min-w-0 flex-1" aria-label="Search the history" data-history-input />
            <!-- A narrow window: the filter groups wait behind this button, its count the filters on. -->
            <UButton
              label="Filters"
              icon="i-lucide-sliders-horizontal"
              size="lg"
              color="neutral"
              variant="outline"
              class="flex-none max-sm:min-h-11 lg:hidden"
              :aria-expanded="filtersOpen"
              data-history-filters-button
              @click="filtersOpen = true"
            >
              <template v-if="filterCount" #trailing><UBadge :label="String(filterCount)" color="primary" variant="solid" size="sm" data-history-filters-count /></template>
            </UButton>
          </div>
          <!-- What narrows the search, each with its own ×, and Clear all: shown only while something does. A wide window wraps the chips;
               a narrow one keeps them on one line that scrolls sideways, Clear all beside it. -->
          <div v-if="active.length" ref="activeBar" role="group" aria-label="Active filters" class="mt-2 flex min-w-0 items-center gap-1.5" data-history-active-bar>
            <div class="flex min-w-0 flex-1 items-center gap-1.5 max-lg:overflow-x-auto lg:flex-wrap" data-history-active-strip>
              <UBadge v-for="(f, i) in active" :key="f.key" color="primary" variant="subtle" size="md" class="max-w-full min-w-0 flex-none gap-1 py-0 pr-0 lg:flex-initial" :data-history-active="f.key">
                <span class="min-w-0 truncate whitespace-nowrap" data-history-active-words>{{ f.label }}: <span class="font-semibold">{{ f.value }}</span></span>
                <IconButton icon="i-lucide-x" size="xs" color="primary" variant="link" :label="removeWords(f)" class="flex-none justify-center max-sm:size-11" data-history-active-remove @click="removeFilter(f, i)" />
              </UBadge>
            </div>
            <UButton label="Clear all" icon="i-lucide-filter-x" size="xs" color="neutral" variant="outline" class="flex-none max-sm:min-h-11 lg:self-start" data-history-clear-all @click="clearAll" />
          </div>
          <p v-if="state !== 'content'" class="mt-1 text-xs text-muted" data-history-caption>{{ CAPTION }} The palette searches the same with <UKbd value="~" size="sm" />.</p>
        </div>

        <!-- The chips: each writes one filter of the query. -->
        <aside class="hidden min-w-0 lg:col-start-1 lg:row-span-2 lg:row-start-1 lg:block" aria-label="Filters" data-history-chips>
          <HistoryFilterGroups :groups="chipRows" :on="chipOn" @press="press" />
        </aside>
        <!-- A narrow window's filters: the same groups in a sheet; the results behind it follow each press. -->
        <UDrawer v-model:open="filtersOpen" direction="bottom" title="Filters" :description="filterCount ? `${filterCount} on` : 'None on'" close>
          <template #body>
            <div class="flex flex-col gap-4 pb-2" data-history-filters-sheet>
              <HistoryFilterGroups :groups="chipRows" :on="chipOn" big @press="press" />
              <div class="flex gap-2">
                <UButton v-if="active.length" label="Clear all" icon="i-lucide-filter-x" color="neutral" variant="outline" class="min-h-11" data-history-sheet-clear-all @click="clearAll" />
                <UButton label="Show the results" color="primary" class="ml-auto min-h-11" data-history-sheet-done @click="filtersOpen = false" />
              </div>
            </div>
          </template>
        </UDrawer>

        <section class="flex min-w-0 flex-col gap-3 lg:col-start-2" data-history-results>
          <UAlert v-if="!admin.hasToken.value" color="neutral" variant="subtle" icon="i-lucide-key-round" title="History needs the workbench token" />
          <template v-else>
            <div v-if="!query" class="flex flex-col gap-1" data-history-questions>
              <h3 class="text-[11px] font-semibold uppercase tracking-wide text-muted">Ask the history</h3>
              <div class="flex gap-1.5 max-lg:overflow-x-auto lg:flex-wrap">
                <UButton v-for="qn in questions" :key="qn.id" :label="qn.label" icon="i-lucide-circle-help" size="sm" color="neutral" variant="soft" class="flex-none max-sm:min-h-11" :title="qn.query" :data-history-question="qn.id" @click="text = qn.query" />
              </div>
            </div>
            <UAlert v-if="error && state === 'content'" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="error.message" :description="error.code" data-history-error />
            <p v-if="indexing" class="text-xs text-muted" data-history-indexing>{{ indexing }}</p>
            <PageEmpty v-if="state === 'reading'" :page="query ? undefined : 'history'" state="reading" thing="history" data-history-loading />
            <PageEmpty v-else-if="state === 'failed'" :page="query ? undefined : 'history'" state="failed" :error="error?.code ? `${error.message} (${error.code})` : error?.message" data-history-error @again="readAgain" />
            <!-- Nothing kept with nothing typed (the page's sentence above it); nothing matched with a query (a filter's answer, no first line). -->
            <PageEmpty
              v-else-if="state === 'empty'"
              :page="query ? undefined : 'history'"
              state="empty"
              :words="historyNoneWords(query)"
              :actions="query ? [{ label: 'Clear all', icon: 'i-lucide-filter-x', onClick: clearAll }] : []"
              icon="i-lucide-search"
              data-history-none
            />
            <template v-if="shown && state === 'content'">
              <p class="text-xs text-muted" data-history-total>
                <template v-if="query">{{ shown.total }}{{ shown.totalCapped ? '+' : '' }} {{ shown.total === 1 && !shown.totalCapped ? 'match' : 'matches' }}<template v-if="shown.cut"> · <span data-history-cut>{{ cutWords(shown.cut) }}</span></template></template>
                <template v-else>The latest sessions</template>
              </p>
              <!-- The rows under day headings that stay at the top while their day scrolls; a thin rule between rows, no cards. -->
              <section v-for="d in days" :key="d.key" class="min-w-0" :aria-labelledby="`history-day-${d.key}`" :data-history-day="d.day || 'undated'">
                <h3 :id="`history-day-${d.key}`" class="sticky top-0 z-20 -mx-1 border-b border-default bg-default/95 px-3 py-1.5 text-xs font-semibold text-toned backdrop-blur" data-history-day-heading>{{ d.label }}</h3>
                <ul class="flex flex-col divide-y divide-default">
                  <HistoryRow v-for="e in d.entries" :key="e.key" :entry="e" :now="now" :searched="!!query" />
                </ul>
              </section>
              <div v-if="shown.next" class="flex justify-center">
                <UButton label="Show more" icon="i-lucide-chevron-down" size="sm" color="neutral" variant="soft" :loading="search.pending.value" data-history-more @click="search.more()" />
              </div>
            </template>
          </template>
        </section>
      </div>
    </template>
  </UDashboardPanel>
</template>
