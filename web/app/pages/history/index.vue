<script setup lang="ts">
import { byAnchor, cutWords, filterOf, historyQuestions, historyTarget, indexingWords, machineOf, outcomeChips, withFilter, type HistoryFilter } from '~/utils/history'
import { LATEST_SESSIONS } from '~/composables/usePaletteHistory'

/**
 * The History page (Program item HS1, docs/plans/history.md, "web/app", PR-7): the palette's `~` search with room. The search box
 * takes the palette's grammar; the chips write the query's filters (the kind of evidence, the outcome, the project from Dispatch's
 * projects, the host from the records, the agent from the catalog, since), so the query is the one truth and the URL keeps it
 * (`?q=`): a search is a link. Every result, paged with Show more (the reply's `next`), grouped under the session or the task it
 * belongs to. Nothing typed: the four questions, and the latest sessions. On a phone, one column.
 */
useHead({ title: 'History' })

const route = useRoute()
const router = useRouter()
const admin = useWorkbenchToken()
const search = useHistorySearch()
const { request } = useApi()
const dispatch = useDispatchApi()
const sessions = useSessions()

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
    else admin.needsToken.value = true
  },
  { immediate: true },
)

const now = ref(Date.now())
let tick: ReturnType<typeof setInterval> | undefined
onMounted(() => (tick = setInterval(() => (now.value = Date.now()), 30_000)))
onBeforeUnmount(() => clearInterval(tick))

const shown = computed(() => (search.result.value?.q === asked.value ? search.result.value.reply : null))
const error = computed(() => (search.error.value?.q === asked.value ? search.error.value : null))
const groups = computed(() => byAnchor(shown.value?.entries ?? []))
const indexing = computed(() => indexingWords(shown.value?.indexing))

// ---- The chips' values: the projects Dispatch keeps, the hosts the records name, the agents of the catalog; read once with the token.
const projects = ref<string[]>([])
const hosts = ref<string[]>([])
const agents = ref<Array<{ id: string; name: string }>>([])
async function loadChips() {
  if (!admin.hasToken.value) return
  const [p, r, a] = await Promise.allSettled([
    dispatch.projects(),
    request<{ records: Array<{ host?: { name?: string; hosted?: boolean; user?: string } }> }>('/api/history/sessions', { query: { limit: '100' } }),
    sessions.catalog(),
  ])
  if (p.status === 'fulfilled') projects.value = (p.value.projects ?? []).filter((x) => !x.archivedAt).map((x) => x.key)
  if (r.status === 'fulfilled') hosts.value = [...new Set(r.value.records.map((x) => x.host?.name ?? '').filter(Boolean))].sort()
  if (a.status === 'fulfilled') agents.value = a.value.map((x) => ({ id: x.id, name: x.name }))
}
watch(
  () => admin.token.value,
  () => void loadChips(),
  { immediate: true },
)

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
    {
      name: 'class' as const,
      label: 'Kind of evidence',
      chips: [
        { value: 'said', label: 'Conversation' },
        { value: 'decided', label: 'Decision' },
        { value: 'checked', label: 'Checked' },
        { value: 'record', label: 'Records' },
      ],
    },
    {
      name: 'outcome' as const,
      label: 'Outcome',
      // Rejected writes the rejecting outcomes the "Rejected designs" question asks for (utils/history.ts, REJECTING_OUTCOMES).
      chips: outcomeChips(),
    },
    { name: 'project' as const, label: 'Project', chips: projects.value.map((k) => ({ value: k, label: k })) },
    { name: 'host' as const, label: 'Host', chips: hosts.value.map((h) => ({ value: h, label: h })) },
    { name: 'agent' as const, label: 'Agent', chips: agents.value.map((x) => ({ value: x.id, label: x.name || x.id })) },
    {
      name: 'since' as const,
      label: 'Since',
      chips: [
        { value: '24h', label: 'A day' },
        { value: '7d', label: 'A week' },
        { value: '30d', label: 'A month' },
      ],
    },
  ].filter((r) => r.chips.length),
)
/** A chip pressed: its filter written into the query (the one value it holds), or taken out when it already held that value. */
function press(name: HistoryFilter, value: string) {
  text.value = withFilter(text.value.trim(), name, filterOf(text.value, name) === value ? '' : value)
}
const questions = computed(() => historyQuestions({ project: project.value, host: machine.value || undefined }))
</script>

<template>
  <UDashboardPanel id="history">
    <template #header>
      <UDashboardNavbar title="History">
        <template #leading>
          <UIcon name="i-lucide-history" class="size-5 text-muted" />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <div class="mx-auto grid w-full max-w-6xl min-w-0 grid-cols-1 gap-5 lg:grid-cols-[15rem_minmax(0,1fr)]" data-history-page>
        <!-- The search box first on a phone, beside the chips on a wide screen. -->
        <div class="min-w-0 lg:col-start-2 lg:row-start-1" data-history-search>
          <UInput v-model="text" icon="i-lucide-search" placeholder="Words, or filters: class: outcome: project: host: agent: since:" size="lg" class="w-full" aria-label="Search the history" data-history-input />
          <p class="mt-1 text-xs text-muted">What was said (Conversation), decided (Decision) and checked (a receipt) are kept apart: a sentence repeated is still Conversation. The palette searches the same with <UKbd value="~" size="sm" />.</p>
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
            <UAlert v-if="error" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="error.message" :description="error.code" data-history-error />
            <p v-if="indexing" class="text-xs text-muted" data-history-indexing>{{ indexing }}</p>
            <p v-if="!shown && !error" class="text-sm text-muted" data-history-loading>Loading…</p>
            <template v-if="shown">
              <p class="text-xs text-muted" data-history-total>
                <template v-if="query">{{ shown.total }}{{ shown.totalCapped ? '+' : '' }} {{ shown.total === 1 && !shown.totalCapped ? 'match' : 'matches' }}<template v-if="shown.cut"> · <span data-history-cut>{{ cutWords(shown.cut) }}</span></template></template>
                <template v-else>The latest sessions</template>
              </p>
              <p v-if="!shown.entries.length" class="py-6 text-center text-sm text-muted" data-history-none>Nothing in the history matches.</p>
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
