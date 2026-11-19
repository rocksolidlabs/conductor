<script setup lang="ts">
import { KIND_SUBJECTS, refRoute } from '~/utils/brakeman'
import { pageState } from '~/utils/pageState'
import { relativeTime } from '~/utils/sessions'
import {
  INBOX_KINDS,
  INBOX_ROW_CLASS,
  atZero,
  choicesWords,
  decisionIdOf,
  firstToOpen,
  inboxEmpty,
  inboxSections,
  kindLook,
  lastClosedWords,
  pickShown,
  recentBadge,
  recentFor,
  rowLine,
  showingWords,
  trackKey,
  unknownWords,
  type InboxAction,
  type InboxItem,
  type RecentEntry,
} from '~/utils/inbox'

// Needs you (docs/plans/decision-inbox.md, Program item MB0, PR-2): every item a person must decide, oldest first, then
// what this browser acted on in the last day with the four steps of each action. Built for a phone first: one column below sm, the
// list and the open item side by side at lg, every target at least 44 px tall. The item is read whole before anything is done to it
// (the list carries no action's input or scope); each action goes through its own operation's route at the revision that operation
// checks, and the operation, not the page, judges whether that still holds. `?item=<ref>` opens one; a Recent row adds its own
// action (`receipt`, `attempt`), whose steps the item then shows.
//
// A DR1 record (docs/plans/decision-inbox.md, PR-4) is read from DR1's own route and drawn by DR1's card and controls (DecisionCard,
// DecisionRespond) inside the item, its brief, what waits and its evidence around it; one answered and not yet applied stays under
// "Answered, not yet applied" from the server's list, and the records closed in the last day are the server's Recent, the same for
// every viewer. This browser's Recent keeps only its actions on the items DR1 does not hold.
useHead({ title: 'Needs you' })

const inbox = useInbox()
const live = useAttention()
const route = useRoute()
const router = useRouter()
const api = useSessions()
const bk = useBrakeman()
/** Every send of the page, its outcome and each item's feedback (useInboxActs). */
const acts = useInboxActs()
/** The run page's question for Accept anyway and Cancel. */
const commands = useTaskCommands(() => '')

/** The workbench token: the page asks for it as the workbench's other pages do (a pass held without one never reaches it: decide.global.ts). */
const admin = useWorkbenchToken()

let stopTracking: (() => void) | undefined
onMounted(() => {
  if (!admin.hasToken.value) admin.needsToken.value = true
  live.start()
  inbox.start()
  stopTracking = inbox.live.track()
})
onBeforeUnmount(() => stopTracking?.())

const state = computed(() => inbox.state.value)
const showing = computed(() => showingWords(state.value.items.length, state.value.total))
/** What needs a person now, and the DR1 records answered and not yet applied, each in the list's order. */
const sections = computed(() => inboxSections(state.value.items))
/** The open item: the query's. */
const openRef = computed(() => (typeof route.query.item === 'string' ? route.query.item : ''))
/** The action a Recent row opened, when one did: its receipt and its attempt. */
const openAction = computed(() => ({
  receipt: typeof route.query.receipt === 'string' ? route.query.receipt : '',
  attempt: typeof route.query.attempt === 'string' ? route.query.attempt : '',
}))
const empty = computed(() => inboxEmpty(state.value))
/** Before the first answer the page says it reads; a first read that failed says so with Read again (PageEmpty, UX1 PR-23). */
const shape = computed(() => (state.value.unavailable ? 'content' : pageState({ read: state.value.loaded, error: state.value.error, count: 0 })))
/** Nothing needs the person and nothing is open: the page is one column that says so (UX1 PR-6). */
const zero = computed(() => atZero(state.value, openRef.value))
/** "Pick an item" only while there is an item to pick. */
const pick = computed(() => pickShown(sections.value, openRef.value))
/**
 * At lg the oldest open item opens by itself (UX1 PR-21), through the query, which the page's acting rests on; never below lg and
 * never over an item already in the query. A row picked while that item is read wins: the reader drops a read for an item no longer
 * open.
 */
const wide = useMedia('(min-width: 1024px)')
watch(
  () => firstToOpen(wide.value, openRef.value, sections.value),
  (r) => {
    if (r) void router.replace({ query: { ...route.query, item: r, receipt: undefined, attempt: undefined } })
  },
  { immediate: true },
)

/**
 * The open item's DR1 record, when it names one: read whole from DR1's route (useDecisionRecord: one read at a time, ordered by the
 * record's rev, again on its own event and after each of its card's writes), drawn by DR1's card. Its rev moving (an answer recorded,
 * applied) reads the item and the list again, so what waits and where it stands follow.
 */
const record = useDecisionRecord(() => decisionIdOf(openRef.value))
/** Whether the record's DecisionRespond draws its buttons: its card then leaves out its option rows (UX1 PR-20). */
const controls = ref(false)
watch(
  () => [record.decision.value?.id, record.decision.value?.rev, record.decision.value?.state],
  (now, was) => {
    if (!was?.[0] || now[0] !== was[0]) return
    void readItem()
    void inbox.changed()
  },
)
/** The clock DR1's card reads its times by. */
const now = ref(Date.now())
let tick: number | undefined
onMounted(() => (tick = window.setInterval(() => (now.value = Date.now()), 5000)))
onBeforeUnmount(() => window.clearInterval(tick))

/** The open item as the list sums it up, while the list holds it: its title stands in the pane until the item is read whole. */
const openSummary = computed(() => state.value.items.find((s) => s.ref === openRef.value))
/** The open item as last read whole; null before it is read, or when it names nothing the server holds (`missing`). */
const item = ref<InboxItem | null>(null)
const missing = ref('')
/** The feedback of the item shown, and only of it: an action under way, or its refusal, on another item is that item's. */
const shownFeedback = computed(() => (item.value ? acts.feedback.value[item.value.ref] : undefined))
const busy = computed(() => shownFeedback.value?.busy ?? '')
const refusal = computed(() => shownFeedback.value?.words ?? '')
/**
 * The open item's reads: one in flight, at most one queued behind it however often the list is read again, so a read that answers is
 * drawn even while the list keeps changing faster than an item is read. Only another item (`openRef` changed, `opened`) drops what
 * an earlier item's read brings back, and its own read starts at once. Resolves once a read started after the call has answered.
 */
let opened = 0
let reader: { opened: number; again: boolean; done: Promise<void> } | null = null
function readItem(): Promise<void> {
  if (reader && reader.opened === opened) {
    reader.again = true
    return reader.done
  }
  const l = { opened, again: false, done: Promise.resolve() }
  l.done = (async () => {
    try {
      do {
        l.again = false
        await readOnce(l.opened)
      } while (l.again && l.opened === opened)
    } finally {
      if (reader === l) reader = null
    }
  })()
  reader = l
  return l.done
}
async function readOnce(n: number) {
  const r = openRef.value
  if (!r) {
    item.value = null
    missing.value = ''
    return
  }
  try {
    const it = await api.inboxItem(r)
    if (n !== opened) return
    item.value = it
    missing.value = ''
  } catch (e) {
    if (n !== opened) return
    item.value = null
    missing.value = (e as { status?: number }).status === 404 ? 'Nothing the server holds goes by this name now.' : (e as Error).message
  }
}
watch(openRef, (r) => {
  // Another item: the one shown goes until the new one is read, so nothing drawn for one is pressed for the other.
  opened++
  if (item.value && item.value.ref !== r) item.value = null
  missing.value = ''
  void readItem()
}, { immediate: true })
// The list read again (an event, the stream's return): the open item is read again too (coalesced), so a choice already made
// elsewhere is gone.
watch(() => [state.value.items, state.value.total], () => {
  if (openRef.value && !acts.pending(openRef.value)) void readItem()
})

function open(ref: string) {
  void router.push({ query: { ...route.query, item: ref, receipt: undefined, attempt: undefined } })
}
/** A Recent row: its item, with this action's own steps. */
function openRecent(e: RecentEntry) {
  void router.push({ query: { ...route.query, item: e.ref, receipt: e.receipt, attempt: e.attempt } })
}
function back() {
  void router.push({ query: { ...route.query, item: undefined, receipt: undefined, attempt: undefined } })
}

/** The page's own routes for a ref: the workbench's (a decision link's page gives its own, MB0's PR-7). */
const routeOf = (r: string): string | null => refRoute(r)

/** This browser's action on the open item (the one a Recent row named, else the latest), and its steps as last read. */
const latest = computed<RecentEntry | undefined>(() => recentFor(state.value.recent, openRef.value, openAction.value))
const delivery = computed(() => (latest.value ? state.value.tracks[trackKey(latest.value)] : undefined))
/**
 * An action this browser sent whose outcome nobody confirmed (no receipt came: a transport failure, a reply lost, an owner that
 * never confirms, a page reloaded while it was out), and the server's track has not recorded it either: the card says so and offers
 * to read again, never to send again by itself. A comment's (kept in no recent entry) is said by its feedback.
 */
const unknown = computed(() => shownFeedback.value?.unknown || unknownWords(latest.value, delivery.value, !!latest.value && out(latest.value)))
/** Whether this action's request is still out from this page (its item's feedback names it, busy). */
function out(e: RecentEntry): boolean {
  const f = acts.feedback.value[e.ref]
  return !!f?.busy && f.receipt === e.receipt && (f.attempt ?? '') === (e.attempt ?? '')
}
async function reread() {
  const e = latest.value
  if (e) await inbox.live.trackNow(e)
  await readItem()
}

/**
 * Ask Brakeman to explain the item's subject, at once, where the server has Brakeman and explains that kind (issue 993, F3): the
 * request's id is said in a toast with a link to it on Brakeman's page; a refusal in the server's words. The page stays where it is.
 */
const askBrakeman = computed(() => {
  const it = item.value
  if (bk.available.value !== true || !it) return undefined
  const kind = it.subject.slice(0, it.subject.indexOf(':'))
  if (!(KIND_SUBJECTS.explain?.kinds ?? []).includes(kind)) return undefined
  return it.subject
})
const askingBrakeman = ref(false)
const asker = useAskBrakeman()
async function askAbout(subject: string) {
  if (askingBrakeman.value) return
  askingBrakeman.value = true
  try {
    await asker.tell({ kind: 'explain', subjects: [subject] })
  } finally {
    askingBrakeman.value = false
  }
}

/** The run page's question for Accept anyway and Cancel (useTaskCommands.question); Withdraw's own. */
function confirmQuestion(a: InboxAction): { title: string; detail: string } {
  const m = /\/api\/runs\/([^/]+)\/tasks\/([^/]+)\//.exec(a.route)
  if (m && (a.name === 'accept' || a.name === 'cancel')) {
    const t = live.runOf(m[1]!)?.tasks?.find((x) => x.id === m[2]) ?? { id: m[2]!, state: 'failed' as const, attempts: 0 }
    return commands.question(t, a.name)
  }
  return { title: `${a.label}?`, detail: a.scope }
}

/**
 * Does one action through its own operation's route (useInboxActs: its receipt kept in this browser's recent list before the reply
 * comes back, its one outcome decided there). The action names the item it was drawn for: one pressed for an item no longer shown
 * does nothing. Whatever item is shown when it comes back is read again.
 */
async function act(ref: string, a: InboxAction, text?: string) {
  const it = item.value
  if (!it || it.ref !== ref || acts.pending(ref)) return
  // Opened from Recent on an earlier action: the card shows this one's steps from now on.
  if (openAction.value.receipt) void router.replace({ query: { ...route.query, receipt: undefined, attempt: undefined } })
  await acts.act(it, a, text)
  await readItem()
  void inbox.changed()
}

/**
 * Comment on the ticket a run of the item was launched from: Dispatch's comment operation; for the item shown, or not at all. `taken`
 * says at once whether it goes: an action under way on the item sends nothing, and the card keeps the draft.
 */
async function comment(ref: string, ticket: string, body: string, taken?: (sent: boolean) => void) {
  const it = item.value
  if (it?.ref !== ref || acts.pending(ref)) {
    taken?.(false)
    return
  }
  taken?.(true)
  await acts.comment(it, ticket, body)
}

/** Open on your phone: the dialog the layout mounts once (OpenOnPhone), opened here as from your menu. */
const phone = useOpenOnPhone()

const recent = computed(() => state.value.recent)
/** A Recent row's word (recentBadge: Not confirmed before any word of success). */
const recentWord = (e: RecentEntry) => recentBadge(e, state.value.tracks[trackKey(e)], out(e))
/** When the last thing closed, by the server's records and this browser's own acts. */
const lastClosed = computed(() => lastClosedWords(state.value.closed, recent.value, now.value))
</script>

<template>
  <UDashboardPanel id="decisions">
    <template #header>
      <UDashboardNavbar title="Needs you" :toggle="false">
        <template #right>
          <UButton label="Open on your phone" icon="i-lucide-smartphone" color="neutral" variant="outline" size="sm" class="hidden lg:inline-flex" data-open-on-phone @click="phone.show()" />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <div class="grid min-h-0 gap-6" :class="zero ? 'mx-auto w-full max-w-2xl' : 'lg:grid-cols-[minmax(0,24rem)_minmax(0,1fr)]'" :data-decisions-zero="zero ? '' : undefined" data-decisions>
        <!-- The list: the open items oldest first, then this browser's recent actions. Below lg it gives way to the open item. -->
        <section class="flex min-w-0 flex-col gap-4" :class="openRef && 'max-lg:hidden'" data-decisions-list>
          <UAlert v-if="state.unavailable" color="neutral" variant="subtle" icon="i-lucide-info" :title="state.unavailable" data-decisions-unavailable />
          <UAlert
            v-else-if="state.error && state.loaded"
            color="warning"
            variant="subtle"
            icon="i-lucide-triangle-alert"
            :title="state.error"
            description="What needs you is not known now: the list below is as it was last read."
            data-decisions-error
          />
          <!-- Before the first answer, and when it failed: the one shape of a page that has nothing to show yet (PageEmpty). -->
          <PageEmpty v-if="shape === 'reading'" page="decisions" state="reading" thing="list of what needs you" />
          <PageEmpty v-else-if="shape === 'failed'" page="decisions" state="failed" :error="state.error" data-decisions-error @again="inbox.changed()" />
          <!-- Nothing needs you: the empty shape, with what shows up here and what was decided in the last day. -->
          <PageEmpty v-else-if="zero" page="decisions" state="empty" words="Nothing needs you now." data-decisions-empty>
            <div class="flex w-full flex-col gap-6">
              <p v-if="lastClosed" class="text-center text-xs text-muted" data-decisions-last>{{ lastClosed }}</p>
              <section class="flex flex-col gap-2 text-left" data-decisions-kinds>
                <h2 class="text-xs font-semibold uppercase tracking-wide text-muted">What shows up here</h2>
                <ul class="flex flex-col gap-2">
                  <li v-for="k in INBOX_KINDS" :key="k" class="flex items-center gap-3 rounded-md border border-default px-3 py-2" :data-decisions-kind="k">
                    <UIcon :name="kindLook(k).icon" class="size-5 shrink-0 text-muted" />
                    <span class="min-w-0">
                      <span class="block text-sm font-medium text-highlighted">{{ kindLook(k).word }}</span>
                      <span class="block text-xs text-muted">{{ kindLook(k).line }}</span>
                    </span>
                  </li>
                </ul>
              </section>
              <DecisionsRecent :closed="state.closed" :recent="recent" :word="recentWord" zero @open="open" @open-recent="openRecent" />
            </div>
          </PageEmpty>
          <!-- Only records answered and not yet applied: nothing needs a person, and they are listed below. -->
          <p v-else-if="empty" class="py-2 text-sm text-muted" data-decisions-empty>Nothing needs you now.</p>
          <ul v-else-if="sections.open.length" class="flex flex-col gap-2">
            <li v-for="s in sections.open" :key="s.ref">
              <button
                type="button"
                :class="[INBOX_ROW_CLASS, openRef === s.ref ? 'border-primary bg-elevated/50' : 'border-default']"
                :aria-current="openRef === s.ref ? 'true' : undefined"
                :data-inbox-row="s.ref"
                @click="open(s.ref)"
              >
                <span class="flex items-baseline justify-between gap-2">
                  <span class="truncate text-sm font-medium text-highlighted" data-inbox-row-title>{{ s.title }}</span>
                  <span class="shrink-0 text-xs text-muted" data-inbox-row-since>{{ relativeTime(s.since) }}</span>
                </span>
                <span class="line-clamp-2 text-xs text-muted" data-inbox-row-line>{{ rowLine(s) }}</span>
                <span class="flex flex-wrap gap-1">
                  <UBadge :label="kindLook(s.kind).word" :icon="kindLook(s.kind).icon" color="neutral" variant="outline" size="sm" :data-inbox-row-kind="s.kind" />
                  <UBadge v-if="s.card" label="Brakeman's brief" color="primary" variant="subtle" size="sm" />
                  <UBadge v-if="s.blocks" :label="`${s.blocks} waiting on it`" color="neutral" variant="subtle" size="sm" />
                  <UBadge v-if="!s.actions.length" label="Open the terminal" color="warning" variant="subtle" size="sm" />
                  <UBadge v-else-if="choicesWords(s)" :label="choicesWords(s)" color="neutral" variant="outline" size="sm" data-inbox-row-choices />
                </span>
              </button>
            </li>
          </ul>
          <!-- DR1's records answered and not yet applied: from the server's list, until DR1 gives each a final state. -->
          <section v-if="sections.tracking.length" class="flex flex-col gap-2" data-decisions-tracking>
            <h2 class="text-xs font-semibold uppercase tracking-wide text-muted">Answered, not yet applied</h2>
            <ul class="flex flex-col gap-2">
              <li v-for="s in sections.tracking" :key="s.ref">
                <button
                  type="button"
                  :class="[INBOX_ROW_CLASS, openRef === s.ref ? 'border-primary bg-elevated/50' : 'border-default']"
                  :aria-current="openRef === s.ref ? 'true' : undefined"
                  :data-inbox-row="s.ref"
                  data-inbox-tracking
                  @click="open(s.ref)"
                >
                  <span class="flex items-baseline justify-between gap-2">
                    <span class="truncate text-sm font-medium text-highlighted" data-inbox-row-title>{{ s.title }}</span>
                    <span class="shrink-0 text-xs text-muted" data-inbox-row-since>{{ relativeTime(s.since) }}</span>
                  </span>
                  <span class="line-clamp-2 text-xs text-muted" data-inbox-row-line>{{ rowLine(s) }}</span>
                  <span class="flex flex-wrap gap-1">
                    <UBadge :label="kindLook(s.kind).word" :icon="kindLook(s.kind).icon" color="neutral" variant="outline" size="sm" :data-inbox-row-kind="s.kind" />
                    <UBadge label="Recorded" color="neutral" variant="subtle" size="sm" />
                  </span>
                </button>
              </li>
            </ul>
          </section>
          <div v-if="showing" class="flex items-center justify-between gap-2 text-xs text-muted">
            <span data-decisions-showing>{{ showing }}</span>
            <UButton v-if="state.next" label="More" color="neutral" variant="outline" size="sm" class="min-h-11" data-decisions-more @click="inbox.more()" />
          </div>

          <DecisionsRecent v-if="!zero" :closed="state.closed" :recent="recent" :word="recentWord" @open="open" @open-recent="openRecent" />
        </section>

        <!-- The open item, whole. Below lg it takes the page, with the way back to the list. -->
        <section v-if="!zero" class="min-w-0" :class="!openRef && 'max-lg:hidden'" data-decisions-item>
          <UButton v-if="openRef" label="Back to the list" icon="i-lucide-arrow-left" color="neutral" variant="ghost" size="lg" class="mb-3 min-h-11 lg:hidden" data-inbox-back @click="back" />
          <InboxItem
            v-if="item"
            :key="item.ref"
            :item="item"
            :now="now"
            :route-of="routeOf"
            :delivery="delivery"
            :busy="busy"
            :refusal="refusal"
            :unknown="unknown"
            :ask-brakeman="askBrakeman"
            :asking-brakeman="askingBrakeman"
            :confirm-question="confirmQuestion"
            :drawn="record.decision.value?.request.evidence"
            :record-shown="!!record.decision.value && record.decision.value.id === decisionIdOf(item.ref)"
            @act="act"
            @comment="comment"
            @reread="reread"
            @ask="askAbout"
          >
            <template #record>
              <UAlert v-if="record.error.value" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="record.error.value" data-inbox-record-error>
                <template #actions><UButton label="Read again" size="xs" color="neutral" variant="outline" class="min-h-11" @click="record.read()" /></template>
              </UAlert>
              <p v-if="!record.decision.value && !record.error.value" class="text-sm text-muted">Reading {{ decisionIdOf(item.ref) }}…</p>
              <template v-if="record.decision.value && record.decision.value.id === decisionIdOf(item.ref)">
                <DecisionCard :decision="record.decision.value" :refs="record.refs.value" :now="now" :controls="controls" captures :policy-now="record.policyNow.value" />
                <DecisionRespond :decision="record.decision.value" :begin="record.begin" :policy-now="record.policyNow.value" @read="record.read()" @controls="controls = $event" />
              </template>
            </template>
          </InboxItem>
          <div v-else-if="missing" class="flex flex-col gap-4">
            <UAlert color="neutral" variant="subtle" icon="i-lucide-info" :title="missing" data-inbox-missing />
            <!-- The item is gone, and what this browser did to it is what is left: its steps, as the server's track keeps them. -->
            <section v-if="delivery?.length" class="flex flex-col gap-2" data-inbox-missing-delivery>
              <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">What became of your action<template v-if="latest?.label"> · {{ latest.label }}</template></h3>
              <InboxDelivery :steps="delivery" :route-of="routeOf" />
            </section>
          </div>
          <!-- The open item while it is read whole: the list's own title for it, when the list holds it, and that it is being read. -->
          <div v-else-if="openRef" class="flex flex-col gap-2" data-inbox-reading>
            <h2 v-if="openSummary" class="text-base font-semibold text-highlighted text-pretty break-words" data-inbox-reading-title>{{ openSummary.title }}</h2>
            <p class="flex items-center gap-2 text-sm text-muted"><UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" /> Reading the item…</p>
          </div>
          <p v-else-if="pick" class="hidden py-8 text-center text-sm text-muted lg:block">Pick an item to see its brief, its choices and what each authorizes.</p>
        </section>
      </div>

    </template>
  </UDashboardPanel>
</template>
