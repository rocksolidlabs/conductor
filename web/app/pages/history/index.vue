<script setup lang="ts">
import {
  activeFilters,
  byAnchor,
  FILTER_WORDS,
  classChips,
  clearedQuery,
  cutWords,
  filterOf,
  historyNoneWords,
  historyQuestions,
  historyTarget,
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
import { LATEST_SESSIONS } from '~/composables/usePaletteHistory'
import type { HistoryOptions } from '~/composables/useHistorySearch'
import type { ProjectsPage } from '~/composables/useDispatch'
import type { Integration } from '~/composables/useSessions'

/**
 * The History page (Program item HS1, docs/plans/history.md, "web/app", PR-7): the palette's `~` search with room. The search box
 * takes the palette's grammar; the chips write the query's filters (the kind of evidence, the outcome, the project from Dispatch's
 * projects, the host from the records, the agent from the catalog, since), so the query is the one truth and the URL keeps it
 * (`?q=`): a search is a link. Every result, paged with Show more (the reply's `next`), grouped under the session or the task it
 * belongs to. Nothing typed: the four questions, and the latest sessions. On a phone, one column.
 *
 * Its second view, Feed (`?view=feed`, the UX review's P12: the retired Events page's feed), is the live feed of what agents report
 * with its last hour beside it, kept in this tab's memory (useEvents): it says so, "Since this tab opened".
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
  { label: 'Feed', value: 'feed', icon: 'i-lucide-radio-tower' },
]
useHead({ title: () => (view.value === 'feed' ? 'History · Feed' : 'History') })

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
    request<{ records: Array<{ host?: { name?: string } }> }>('/api/history/sessions', { query: { limit: '100' }, signal }),
    request<{ agents?: Array<{ id: string; name: string }> }>('/api/catalog', { signal }),
  ])
  return {
    projects: p.status === 'fulfilled' ? (p.value.projects ?? []).filter((x) => !x.archivedAt).map((x) => x.key) : [],
    hosts: r.status === 'fulfilled' ? [...new Set(r.value.records.map((x) => x.host?.name ?? '').filter(Boolean))].sort() : [],
    agents: a.status === 'fulfilled' ? (a.value.agents ?? []).map((x) => ({ id: x.id, name: x.name })) : [],
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
const groups = computed(() => byAnchor(shown.value?.entries ?? []))
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

interface Chip {
  value: string
  label: string
}
// This machine's name, from the latest sessions' records (machineOf: a session on this server, never a hosted one's host); until it is
// known, "Sessions on this machine" asks for every session (`kind:session`) rather than name a host the records hold, which may be
// another machine's (a hosted session's) or this one's before a rename.
const machine = computed(() => (search.result.value?.q === LATEST_SESSIONS ? machineOf(search.result.value.reply.entries) : ''))
const project = computed(() => (projects.value.length === 1 ? projects.value[0] : undefined))
const chipRows = computed<Array<{ name: HistoryFilter; label: string; chips: Chip[] }>>(() =>
  [
    { name: 'class' as const, label: FILTER_WORDS.class, chips: classChips() },
    {
      name: 'outcome' as const,
      label: FILTER_WORDS.outcome,
      // Rejected writes the rejecting outcomes the "Rejected designs" question asks for (utils/history.ts, REJECTING_OUTCOMES).
      chips: outcomeChips(),
    },
    { name: 'project' as const, label: FILTER_WORDS.project, chips: projects.value.map((k) => ({ value: k, label: k })) },
    { name: 'host' as const, label: FILTER_WORDS.host, chips: hosts.value.map((h) => ({ value: h, label: h })) },
    { name: 'agent' as const, label: FILTER_WORDS.agent, chips: agents.value.map((x) => ({ value: x.id, label: x.name || x.id })) },
    { name: 'since' as const, label: FILTER_WORDS.since, chips: sinceChips() },
  ].filter((r) => r.chips.length),
)
/** A chip pressed: its filter written into the query (the one value it holds), or taken out when it already held that value. */
function press(name: HistoryFilter, value: string) {
  text.value = withFilter(text.value.trim(), name, filterOf(text.value, name) === value ? '' : value)
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
        <p class="text-xs text-muted" data-history-feed-caption>Since this tab opened</p>
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
          <UInput ref="field" v-model="text" icon="i-lucide-search" placeholder="Words, or filters: class: outcome: project: host: agent: since:" size="lg" class="w-full" aria-label="Search the history" data-history-input />
          <!-- What narrows the search, each with its own ×, and Clear all: shown only while something does. -->
          <div v-if="active.length" ref="activeBar" role="group" aria-label="Active filters" class="mt-2 flex min-w-0 flex-wrap items-center gap-1.5" data-history-active-bar>
            <UBadge v-for="(f, i) in active" :key="f.key" color="primary" variant="subtle" size="md" class="max-w-full min-w-0 gap-1 py-0 pr-0" :data-history-active="f.key">
              <span class="min-w-0 truncate" data-history-active-words>{{ f.label }}: <span class="font-semibold">{{ f.value }}</span></span>
              <IconButton icon="i-lucide-x" size="xs" color="primary" variant="link" :label="removeWords(f)" class="flex-none justify-center max-sm:size-11" data-history-active-remove @click="removeFilter(f, i)" />
            </UBadge>
            <UButton label="Clear all" icon="i-lucide-filter-x" size="xs" color="neutral" variant="outline" class="max-sm:min-h-11" data-history-clear-all @click="clearAll" />
          </div>
          <p v-if="state !== 'content'" class="mt-1 text-xs text-muted" data-history-caption>{{ CAPTION }} The palette searches the same with <UKbd value="~" size="sm" />.</p>
        </div>

        <!-- The chips: each writes one filter of the query. -->
        <aside class="flex min-w-0 flex-col gap-3 lg:col-start-1 lg:row-span-2 lg:row-start-1" aria-label="Filters" data-history-chips>
          <div v-for="r in chipRows" :key="r.name" class="min-w-0" :data-history-chips-of="r.name">
            <h3 class="mb-1 text-[11px] font-semibold uppercase tracking-wide text-muted">{{ r.label }}</h3>
            <div class="flex flex-wrap gap-1">
              <UButton
                v-for="c in r.chips"
                :key="c.value"
                :label="c.label"
                size="xs"
                :color="filterOf(text, r.name) === c.value ? 'primary' : 'neutral'"
                :variant="filterOf(text, r.name) === c.value ? 'soft' : 'outline'"
                :aria-pressed="filterOf(text, r.name) === c.value"
                :data-history-chip="`${r.name}:${c.value}`"
                :data-history-chip-on="filterOf(text, r.name) === c.value ? '' : undefined"
                @click="press(r.name, c.value)"
              />
            </div>
          </div>
        </aside>

        <section class="flex min-w-0 flex-col gap-3 lg:col-start-2" data-history-results>
          <UAlert v-if="!admin.hasToken.value" color="neutral" variant="subtle" icon="i-lucide-key-round" title="History needs the workbench token" />
          <template v-else>
            <div v-if="!query" class="flex flex-col gap-1" data-history-questions>
              <h3 class="text-[11px] font-semibold uppercase tracking-wide text-muted">Ask the history</h3>
              <div class="flex flex-wrap gap-1.5">
                <UButton v-for="qn in questions" :key="qn.id" :label="qn.label" icon="i-lucide-circle-help" size="sm" color="neutral" variant="soft" :title="qn.query" :data-history-question="qn.id" @click="text = qn.query" />
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
              <div v-for="g in groups" :key="g.anchor.ref" class="min-w-0 rounded-lg border border-default" :data-history-group="g.anchor.ref">
                <h3 class="flex min-w-0 items-center gap-2 border-b border-default px-3 py-1.5 text-xs font-medium text-toned">
                  <NuxtLink v-if="historyTarget({ url: g.anchor.url ?? '', anchor: g.anchor })" :to="historyTarget({ url: g.anchor.url ?? '', anchor: g.anchor })!" class="min-w-0 truncate hover:underline">{{ g.anchor.label }}</NuxtLink>
                  <span v-else class="min-w-0 truncate">{{ g.anchor.label }}</span>
                </h3>
                <ul class="flex flex-col p-1">
                  <HistoryRow v-for="e in g.entries" :key="e.key" :entry="e" :now="now" />
                </ul>
              </div>
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
