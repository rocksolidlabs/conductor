<script setup lang="ts">
import { ApiError } from '~/composables/useApi'
import type { PersonWork } from '~/composables/usePeople'
import { SerialReads, decisionsLive } from '~/composables/useDecisions'
import { avatarTone } from '~/utils/avatar'
import { responsibilityWords } from '~/utils/crews'
import { statusWord, ticketPath } from '~/utils/dispatch'
import { boundedRead, freshTicketLists, keepsTicketLists, ownedHow, PERSON_READ_MS, personFromSegment, personLists, readTicketsPage, type PersonTicketList } from '~/utils/people'
import { initials } from '~/utils/sessions'

/**
 * A person's work (docs/plans/decisions.md, HM1, PR-11): a person is a local label, never a session, and their page lists what they
 * answer for. The crews where they hold a responsibility, the tasks they own in the runs the server holds and the managed runs' records
 * (as each run's crew was launched, so a person removed from the saved crew since keeps that run's tasks here), and Dispatch's tickets
 * they own or are assigned (the counts, then each list a page at a time, "Show more" reading the next by its cursor). The work is shown
 * as soon as it is read, and each ticket list is read on its own after it. The decisions for them (PR-12), open and answered, each with
 * its card and Answer opening the app's decision modal. Every list is read and holding rows, read and empty, or not
 * read, wholly or in part (`personLists`): one not read whole says so in its place, its rows read kept, and the rest of the page stays.
 * Read as the page opens and when the workbench token changes; nothing polls. One column on a phone.
 */

const route = useRoute()
const admin = useWorkbenchToken()
const people = usePeople()
const { api } = useDispatch()

// The route's segment, unescaped, is the name by `personSegment`'s rule; one that is no name's is no one's page, and is not read.
const segment = computed(() => String(route.params.name ?? ''))
const named = computed(() => personFromSegment(segment.value))
const name = computed(() => named.value ?? segment.value)
useHead({ title: computed(() => `${name.value} · People`) })

const work = ref<PersonWork | null>(null)
const missing = ref(false)
const error = ref('')
const loading = ref(false)
/** Dispatch's two lists for the person (owner= and assignee=person:), each read a page at a time. */
const lists = ref<PersonTicketList[]>([])
const reading = ref<Record<string, boolean>>({})

/** The reads of the work: a reply to one older than the last started is dropped. */
let seq = 0
/** The ticket lists' generation, and whom and which tickets they were started from: a read that keeps them leaves their pages alone. */
let listSeq = 0
let listsFor: string | null = null
let listsFrom: PersonWork['tickets'] | undefined
/** The read of the work under way, aborted when a newer one starts or the page goes. */
let inflight: AbortController | null = null

/** Reads the next page of one list (again, after a failed read), while the page still shows the lists it was read for. */
async function more(list: PersonTicketList): Promise<void> {
  const n = listSeq
  reading.value = { ...reading.value, [list.id]: true }
  await readTicketsPage(list, (q) => api.tickets(q), () => n === listSeq)
  if (n === listSeq) reading.value = { ...reading.value, [list.id]: false }
}

/**
 * Reads the person's work, bounded (`PERSON_READ_MS`, `boundedRead`): a read that gets no reply is aborted and says so, so the reads a
 * decision's change queues after it (`SerialReads`) are made. One made for a decision's change (`decisionsOnly`) keeps the ticket lists
 * and the pages read of them (`keepsTicketLists`); any other starts them again.
 */
async function load(decisionsOnly = false): Promise<void> {
  if (!admin.hasToken.value) {
    admin.needsToken.value = true
    return
  }
  const n = ++seq
  inflight?.abort()
  inflight = null
  const who = named.value
  if (who === null) {
    missing.value = true
    work.value = null
    error.value = ''
    loading.value = false
    return
  }
  loading.value = true
  const ctl = new AbortController()
  inflight = ctl
  try {
    const w = await boundedRead((signal) => people.person(who, signal), PERSON_READ_MS, ctl)
    if (n !== seq) return
    work.value = w
    missing.value = false
    error.value = ''
    if (keepsTicketLists(decisionsOnly, listsFor, who, listsFrom, w.tickets)) return
    listSeq++
    listsFor = who
    listsFrom = w.tickets
    lists.value = freshTicketLists(w.tickets)
    reading.value = {}
    // Each list on its own, after the work is shown: one that fails says so in its place.
    for (const list of lists.value) void more(list)
  } catch (e) {
    if (n !== seq) return
    if (e instanceof ApiError && e.status === 404) {
      missing.value = true
      work.value = null
      error.value = ''
    } else {
      error.value = (e as Error).message
    }
  } finally {
    if (n === seq) {
      loading.value = false
      if (inflight === ctl) inflight = null
    }
  }
}

onMounted(() => load())
watch([() => admin.token.value, name], () => load())
// A decision changed (answered, applied, routed, opened): their page is read again, one read at a time, so a decision answered for them
// leaves it and one opened for them comes (the app's one events stream, useDecisions); the ticket lists, which no decision changes,
// keep the pages read of them.
const decisionReads = new SerialReads(() => load(true))
const stopDecisions = decisionsLive().subscribe(() => void decisionReads.run())
onBeforeUnmount(() => {
  stopDecisions()
  // Stopped before the read under way is aborted: its settling would otherwise start the read a decision's change queued behind it,
  // after the page is gone (Codex's round 2 on #741).
  decisionReads.stop()
  seq++
  inflight?.abort()
})

function stateColor(state: string): 'success' | 'error' | 'neutral' | 'info' | 'primary' {
  switch (state) {
    case 'running':
      return 'success'
    case 'verifying':
      return 'info'
    case 'failed':
      return 'error'
    case 'succeeded':
      return 'primary'
  }
  return 'neutral'
}
const read = computed(() => (work.value ? personLists(work.value) : null))
const decisionModal = useDecisionModal()
/** The cards' clock: their times are relative ("5 minutes ago"). */
const now = ref(Date.now())
let tick: number | undefined
onMounted(() => (tick = window.setInterval(() => (now.value = Date.now()), 5000)))
onBeforeUnmount(() => window.clearInterval(tick))
const taskPath = (run: string, task: string) => `/runs/${encodeURIComponent(run)}?view=tasks&task=${encodeURIComponent(task)}`
</script>

<template>
  <UDashboardPanel id="person">
    <template #header>
      <UDashboardNavbar :toggle="false" :title="name">
        <template #leading>
          <span class="grid size-6 flex-none place-items-center rounded-full text-[10px] font-semibold" :class="avatarTone(name)" aria-hidden="true">{{ initials(name) }}</span>
        </template>
      </UDashboardNavbar>
    </template>
    <template #body>
      <div class="mx-auto flex w-full max-w-4xl flex-col gap-6" data-person-page :data-person-page-name="name">
        <UAlert v-if="error" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="error" />
        <UAlert v-if="missing" color="neutral" variant="subtle" icon="i-lucide-user-x" :title="`Conductor knows no one named ${name}`" description="No saved crew, run or Dispatch ticket names them, and Dispatch's people do not list them." data-person-missing />
        <p v-else-if="!work && loading" class="text-sm text-muted">Reading their work…</p>

        <template v-if="work && read">
          <p class="text-sm text-muted">
            A person is never started: no session, worktree or agent. They own decisions and outcomes.
            <span v-if="work.dispatch">They are among Dispatch's people.</span>
          </p>

          <section class="flex flex-col gap-2" data-person-crews>
            <h2 class="text-xs font-semibold uppercase tracking-wide text-muted">Crews</h2>
            <ul v-if="read.crews.state === 'rows'" class="flex flex-col gap-1">
              <li v-for="c in work.crews" :key="c.id" class="flex min-w-0 flex-wrap items-center gap-x-2 text-sm" :data-person-crew="c.id">
                <NuxtLink :to="`/crews/${encodeURIComponent(c.id)}`" class="min-w-0 truncate font-medium text-highlighted hover:underline">{{ c.name }}</NuxtLink>
                <span class="text-muted" data-person-crew-words>{{ responsibilityWords(c.responsibilities) || 'no responsibility' }}</span>
              </li>
              <li v-if="work.crewsMore" class="text-xs text-dimmed">More crews list them than are shown here.</li>
            </ul>
            <p v-else-if="read.crews.state === 'empty'" class="text-sm text-dimmed">No saved crew lists them.</p>
            <p v-if="read.crews.unread" class="text-sm text-warning" data-person-list-unread="crews">{{ read.crews.unread }}</p>
          </section>

          <section class="flex flex-col gap-2" data-person-tasks>
            <h2 class="text-xs font-semibold uppercase tracking-wide text-muted">Tasks they own</h2>
            <ul v-if="read.tasks.state === 'rows'" class="flex flex-col divide-y divide-default rounded-md border border-default">
              <li v-for="t in work.tasks" :key="`${t.run}/${t.task}`" class="flex min-w-0 flex-col gap-1 px-3 py-2 text-sm sm:flex-row sm:items-center sm:gap-3" :data-person-task="`${t.run}/${t.task}`" :data-person-task-from="t.ownerFrom">
                <NuxtLink :to="taskPath(t.run, t.task)" class="font-mono font-semibold text-highlighted hover:underline">{{ t.task }}</NuxtLink>
                <UBadge :label="t.state" :color="stateColor(t.state)" variant="subtle" size="sm" class="w-fit flex-none" />
                <span class="min-w-0 truncate text-muted">in {{ t.runLabel }} · done by {{ t.member }}<span v-if="ownedHow(t)"> · {{ ownedHow(t) }}</span></span>
              </li>
            </ul>
            <p v-else-if="read.tasks.state === 'empty'" class="text-sm text-dimmed">They own no task of a run the server holds.</p>
            <p v-if="read.tasks.unread" class="text-sm text-warning" data-person-list-unread="tasks">{{ read.tasks.unread }}</p>
            <p v-if="work.more" class="text-xs text-dimmed">The newest 200 are shown.</p>
          </section>

          <section class="flex flex-col gap-2" data-person-decisions>
            <h2 class="text-xs font-semibold uppercase tracking-wide text-muted">Decisions for them</h2>
            <p class="text-xs text-dimmed">The operator answers each for them; the answer's receipt keeps who answered and for whom.</p>
            <ul v-if="work.decisions.length" class="flex flex-col gap-3">
              <li v-for="d in work.decisions" :key="d.id" class="flex min-w-0 flex-col gap-2 rounded-md border border-default p-3" :data-person-decision="d.id">
                <DecisionCard :decision="d" :now="now" />
                <UButton :label="d.state === 'open' ? 'Answer' : 'Show'" size="xs" :color="d.state === 'open' ? 'warning' : 'neutral'" variant="soft" class="w-fit" :data-person-decision-open="d.id" @click="decisionModal.open(d.id)" />
              </li>
              <li v-if="work.decisionsMore" class="text-xs text-dimmed">The newest 100 are shown.</li>
            </ul>
            <p v-else-if="!work.decisionsUnavailable" class="text-sm text-dimmed" data-person-decisions-none>No decision waits for them.</p>
            <p v-if="work.decisionsUnavailable" class="text-sm text-warning" data-person-list-unread="decisions">The decisions could not be read: one for them may not be shown.</p>
          </section>

          <section class="flex flex-col gap-2" data-person-tickets>
            <h2 class="text-xs font-semibold uppercase tracking-wide text-muted">Dispatch tickets</h2>
            <p v-if="!work.tickets" class="text-sm text-warning" data-person-tickets-unavailable data-person-list-unread="tickets">{{ read.tickets.unread }}</p>
            <template v-else>
              <p class="text-sm text-muted" data-person-ticket-counts>They own {{ work.tickets.owner }} and are assigned {{ work.tickets.assignee }}.</p>
              <div v-for="list in lists" :key="list.id" class="flex flex-col gap-1" :data-person-ticket-list="list.id" :data-person-ticket-shown="list.tickets.length">
                <span class="text-xs font-medium text-muted">{{ list.label }}<span v-if="list.read && list.total > list.tickets.length" data-person-ticket-part> · {{ list.tickets.length }} of {{ list.total }} shown</span></span>
                <ul v-if="list.tickets.length" class="flex flex-col gap-1">
                  <li v-for="tk in list.tickets" :key="tk.id" class="flex min-w-0 items-center gap-2 text-sm" :data-person-ticket="tk.key">
                    <UIcon :name="statusWord(tk.status).icon" class="size-4 flex-none text-muted" />
                    <NuxtLink :to="ticketPath(tk.key)" class="flex-none font-mono text-xs text-muted hover:underline">{{ tk.key }}</NuxtLink>
                    <span class="min-w-0 truncate">{{ tk.title }}</span>
                  </li>
                </ul>
                <p v-else-if="!list.read && !list.error" class="text-sm text-dimmed">Reading the tickets…</p>
                <p v-if="list.error" class="text-sm text-warning" data-person-ticket-error>These tickets could not be read: {{ list.error }}</p>
                <UButton
                  v-if="list.error || (list.read && list.next)"
                  :label="list.error ? 'Try again' : 'Show more'"
                  :loading="reading[list.id]"
                  size="xs"
                  variant="soft"
                  color="neutral"
                  class="w-fit"
                  data-person-ticket-more
                  @click="more(list)"
                />
              </div>
            </template>
          </section>
        </template>
      </div>
    </template>
  </UDashboardPanel>
</template>
