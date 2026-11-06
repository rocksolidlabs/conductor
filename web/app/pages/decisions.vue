<script setup lang="ts">
import { refRoute } from '~/utils/brakeman'
import { isStale } from '~/utils/brakemanCards'
import { DispatchOrder } from '~/utils/dispatchOrder'
import { replyFailure } from '~/utils/reply'
import { relativeTime } from '~/utils/sessions'
import { CommandRefused, mintCommandId } from '~/utils/tasks'
import { qrSvg } from '~/utils/qr'
import {
  answerFailure,
  inboxEmpty,
  mintRequestId,
  movedOnWords,
  phoneOffer,
  planIdOf,
  recentFor,
  refParts,
  showingWords,
  stateWords,
  trackKey,
  UNCONFIRMED,
  type InboxAction,
  type InboxItem,
  type RecentEntry,
} from '~/utils/inbox'

// Needs your decision (docs/plans/decision-inbox.md, Program item MB0, PR-2): every item a person must decide, oldest first, then
// what this browser acted on in the last day with the four steps of each action. Built for a phone first: one column below sm, the
// list and the open item side by side at lg, every target at least 44 px tall. The item is read whole before anything is done to it
// (the list carries no action's input or scope); each action goes through its own operation's route at the revision that operation
// checks, and the operation, not the page, judges whether that still holds. `?item=<ref>` opens one; a Recent row adds its own
// action (`receipt`, `attempt`), whose steps the item then shows.
useHead({ title: 'Needs your decision' })

const inbox = useInbox()
const live = useAttention()
const route = useRoute()
const router = useRouter()
const api = useSessions()
const dispatch = useDispatchApi()
const quick = useQuickReply()
const bk = useBrakeman()
const ops = useDispatchOps(new DispatchOrder())
const cmdRun = ref('')
const commands = useTaskCommands(() => cmdRun.value)

let stopTracking: (() => void) | undefined
onMounted(() => {
  live.start()
  inbox.start()
  stopTracking = inbox.live.track()
})
onBeforeUnmount(() => stopTracking?.())

const state = computed(() => inbox.state.value)
const showing = computed(() => showingWords(state.value.items.length, state.value.total))
/** The open item: the query's. */
const openRef = computed(() => (typeof route.query.item === 'string' ? route.query.item : ''))
/** The action a Recent row opened, when one did: its receipt and its attempt. */
const openAction = computed(() => ({
  receipt: typeof route.query.receipt === 'string' ? route.query.receipt : '',
  attempt: typeof route.query.attempt === 'string' ? route.query.attempt : '',
}))
const empty = computed(() => inboxEmpty(state.value))

/** The open item as last read whole; null before it is read, or when it names nothing the server holds (`missing`). */
const item = ref<InboxItem | null>(null)
const missing = ref('')
const refusal = ref('')
const busy = ref('')
let reading = 0
async function readItem() {
  const r = openRef.value
  const n = ++reading
  if (!r) {
    item.value = null
    missing.value = ''
    return
  }
  try {
    const it = await api.inboxItem(r)
    if (n !== reading) return
    item.value = it
    missing.value = ''
  } catch (e) {
    if (n !== reading) return
    item.value = null
    missing.value = (e as { status?: number }).status === 404 ? 'Nothing the server holds goes by this name now.' : (e as Error).message
  }
}
watch(openRef, (r) => {
  refusal.value = ''
  // Another item: the one shown goes until the new one is read, so nothing drawn for one is pressed for the other.
  if (item.value && item.value.ref !== r) item.value = null
  missing.value = ''
  void readItem()
}, { immediate: true })
// The list read again (an event, the stream's return): the open item is read again too, so a choice already made elsewhere is gone.
watch(() => [state.value.items, state.value.total], () => {
  if (openRef.value && !busy.value) void readItem()
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
 * An answer this browser sent whose outcome nobody confirmed (an owner that never confirms, a connection lost before its word, a
 * page reloaded while it was out), and the server's track has not recorded it either: the card says so and offers to read again.
 */
const unknown = computed(() => {
  const e = latest.value
  if (!e?.attempt || busy.value || (e.outcome && e.outcome !== UNCONFIRMED)) return ''
  const recorded = delivery.value?.find((s) => s.step === 'recorded')?.state
  if (recorded === 'done' || recorded === 'refused') return ''
  return 'Your answer went, and nothing has said whether the session took it. Read again to see what the server knows now; answer again only if the agent still asks.'
})
async function reread() {
  const e = latest.value
  if (e) await inbox.live.trackNow(e)
  await readItem()
}

/** Ask Brakeman about the item's subject, on the Brakeman page's composer, where the server has Brakeman and explains that kind. */
const askBrakeman = computed(() => {
  const it = item.value
  if (bk.available.value !== true || !it) return undefined
  const kind = it.subject.slice(0, it.subject.indexOf(':'))
  if (!['question', 'task', 'ticket', 'run', 'session', 'attempt'].includes(kind)) return undefined
  return `/brakeman?kind=explain&subject=${encodeURIComponent(it.subject)}`
})

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
 * Does one action through its own operation's route, its receipt kept in this browser's recent list before the reply comes back.
 * The action names the item it was drawn for: one pressed for an item no longer shown does nothing.
 */
async function act(ref: string, a: InboxAction, text?: string) {
  const it = item.value
  if (!it || it.ref !== ref || busy.value) return
  refusal.value = ''
  busy.value = a.label
  // Opened from Recent on an earlier action: the card shows this one's steps from now on.
  if (openAction.value.receipt) void router.replace({ query: { ...route.query, receipt: undefined, attempt: undefined } })
  try {
    if (a.name === 'answer') await answer(it, a, text)
    else if (a.name === 'retry' || a.name === 'accept' || a.name === 'cancel') await command(it, a)
    else if (a.name === 'approve' || a.name === 'withdraw') await planOp(it, a)
  } finally {
    busy.value = ''
    await readItem()
    void inbox.changed()
  }
}

async function answer(it: InboxItem, a: InboxAction, text?: string) {
  const sessionId = refParts(it.ref).a
  const s = live.sessions.value.find((x) => x.id === sessionId) ?? { id: sessionId, kind: 'server' as const, name: '' }
  const reqId = mintRequestId()
  const entry: RecentEntry = { ref: it.ref, receipt: a.expect, attempt: reqId, at: Date.now(), title: it.title, label: text === undefined ? a.label : 'A line' }
  inbox.live.remember(entry)
  try {
    const out = await quick.answer(s, text === undefined ? { input: a.input ?? '' } : { text }, { question: a.expect, reqId })
    if (out === 'not_sent') {
      // Nothing left this tab: it is no action of this browser's.
      inbox.live.forget(entry)
      refusal.value = 'Not sent: a reply from this tab is already on its way to this session, or this tab answered this question already.'
      return
    }
    // The owner's word, or none to come (an older owner): only the word is kept as taken.
    inbox.live.outcome(entry.receipt, reqId, out === 'taken' ? 'accepted' : UNCONFIRMED)
  } catch (e) {
    const how = answerFailure(e)
    if (how === 'not_sent') {
      inbox.live.forget(entry)
      const f = replyFailure(e, s.name || undefined)
      refusal.value = [f.title, f.description].filter(Boolean).join(': ')
      return
    }
    if (how === 'unconfirmed') inbox.live.outcome(entry.receipt, reqId, UNCONFIRMED)
    else {
      const f = replyFailure(e, s.name || undefined)
      refusal.value = [f.title, f.description].filter(Boolean).join(': ')
      inbox.live.outcome(entry.receipt, reqId, `refused: ${refusal.value}`)
    }
  }
  await inbox.live.trackNow(entry)
}

async function command(it: InboxItem, a: InboxAction) {
  const m = /\/api\/runs\/([^/]+)\/tasks\/([^/]+)\/(retry|accept|cancel)$/.exec(a.route)
  if (!m) return
  const [, run, task, name] = m as unknown as [string, string, string, 'retry' | 'accept' | 'cancel']
  cmdRun.value = run
  const commandId = mintCommandId()
  const entry: RecentEntry = { ref: it.ref, receipt: commandId, at: Date.now(), title: it.title, label: a.label, ...(a.track && a.track !== it.ref ? { track: a.track } : {}) }
  inbox.live.remember(entry)
  let failed: unknown = null
  const ok = await commands.send({ id: task }, name, { expectedRevision: Number(a.expect) || undefined, commandId, refused: (e) => (failed = e) })
  if (ok) inbox.live.outcome(commandId, undefined, 'accepted')
  else if (failed) {
    const r = await live.refreshRun(run).catch(() => null)
    const t = r?.tasks?.find((x) => x.id === task)
    refusal.value = isStale(failed)
      ? `The run moved on${r?.managed ? ` (revision ${r.managed.revision} now, the choice was read at ${a.expect})` : ''}${t ? `: ${t.id} is ${t.state} now` : ''}. Nothing was done.`
      : (failed as Error).message
    inbox.live.outcome(commandId, undefined, `refused: ${refusal.value}`)
  }
  await inbox.live.trackNow(entry)
}

async function planOp(it: InboxItem, a: InboxAction) {
  const id = planIdOf(a.route)
  if (!id) return
  const opId = mintCommandId()
  const entry: RecentEntry = { ref: it.ref, receipt: opId, at: Date.now(), title: it.title, label: a.label }
  inbox.live.remember(entry)
  try {
    await ops.run((body) => (a.name === 'approve' ? dispatch.approvePlan(id, { ...body, ticketRev: a.ticketRev }) : dispatch.withdrawPlan(id, body)), {
      thing: `plan:${id}`,
      what: it.title,
      expectedRev: Number(a.expect) || undefined,
      opId,
      raise: true,
      done: a.name === 'approve' ? 'Approved: launching is a separate step on the ticket page' : 'Withdrawn',
    })
    inbox.live.outcome(opId, undefined, 'accepted')
  } catch (e) {
    const now = await api.inboxItem(it.ref).catch(() => null)
    refusal.value = isStale(e) || (e instanceof CommandRefused && (e.receipt as { refusal?: string }).refusal === 'stale_revision') ? movedOnWords('plan', now) : (e as Error).message
    inbox.live.outcome(opId, undefined, `refused: ${refusal.value}`)
  }
  await inbox.live.trackNow(entry)
}

/** Comment on the ticket a run of the item was launched from: Dispatch's comment operation; for the item shown, or not at all. */
async function comment(ref: string, ticket: string, body: string) {
  if (item.value?.ref !== ref) return
  await ops.run((b) => dispatch.addComment(ticket, { ...b, body }), { thing: `ticket:${ticket}`, what: ticket, done: `Commented on ${ticket}` })
}

/** Open on your phone (at lg and up): the page's address as a QR code, the address alone; on a loopback origin, the words why not. */
const phoneOpen = ref(false)
const offer = computed(() => (import.meta.client ? phoneOffer(location.origin) : null))
const qr = computed(() => (offer.value?.kind === 'qr' ? qrSvg(offer.value.url) : null))
/** The dialog's line: the code's, only where there is a code. */
const phoneWords = computed(() =>
  offer.value?.kind === 'qr'
    ? "Scan the code with a phone on the same network. It holds the page's address alone: the phone asks for the workbench token once, as any browser does."
    : "This page's address is this computer's own.",
)

const recent = computed(() => state.value.recent)
function recentWord(e: RecentEntry): { text: string; color: 'success' | 'warning' | 'error' | 'neutral' } {
  const steps = state.value.tracks[trackKey(e)]
  const of = (n: string) => steps?.find((s) => s.step === n)?.state
  if (of('validated') === 'refused' || of('recorded') === 'refused') return { text: 'Refused', color: 'error' }
  const r = of('resumed')
  if (r === 'done') return { text: 'Work resumed', color: 'success' }
  if (r === 'not_applicable') return { text: 'Taken', color: 'neutral' }
  if (r) return { text: `Resumed: ${stateWords(r).toLowerCase()}`, color: 'warning' }
  return { text: 'Sent', color: 'neutral' }
}
</script>

<template>
  <UDashboardPanel id="decisions">
    <template #header>
      <UDashboardNavbar title="Needs your decision" :toggle="false">
        <template #right>
          <UButton label="Open on your phone" icon="i-lucide-smartphone" color="neutral" variant="outline" size="sm" class="hidden lg:inline-flex" data-open-on-phone @click="phoneOpen = true" />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <div class="grid min-h-0 gap-6 lg:grid-cols-[minmax(0,24rem)_minmax(0,1fr)]" data-decisions>
        <!-- The list: the open items oldest first, then this browser's recent actions. Below lg it gives way to the open item. -->
        <section class="flex min-w-0 flex-col gap-4" :class="openRef && 'max-lg:hidden'" data-decisions-list>
          <UAlert v-if="state.unavailable" color="neutral" variant="subtle" icon="i-lucide-info" :title="state.unavailable" data-decisions-unavailable />
          <UAlert
            v-else-if="state.error"
            color="warning"
            variant="subtle"
            icon="i-lucide-triangle-alert"
            :title="state.error"
            :description="state.loaded ? 'What needs your decision is not known now: the list below is as it was last read.' : undefined"
            data-decisions-error
          />
          <div v-if="empty" class="flex flex-col items-center gap-2 py-8 text-center text-muted" data-decisions-empty>
            <UIcon name="i-lucide-circle-check" class="size-8" />
            <p class="text-sm">Nothing needs your decision now.</p>
          </div>
          <ul v-else class="flex flex-col gap-2">
            <li v-for="s in state.items" :key="s.ref">
              <button
                type="button"
                class="flex min-h-11 w-full flex-col gap-1 rounded-md border px-3 py-2 text-left transition-colors hover:bg-elevated/50"
                :class="openRef === s.ref ? 'border-primary bg-elevated/50' : 'border-default'"
                :aria-current="openRef === s.ref ? 'true' : undefined"
                :data-inbox-row="s.ref"
                @click="open(s.ref)"
              >
                <span class="flex items-baseline justify-between gap-2">
                  <span class="truncate text-sm font-medium text-highlighted" data-inbox-row-title>{{ s.title }}</span>
                  <span class="shrink-0 text-xs text-muted">{{ relativeTime(s.since) }}</span>
                </span>
                <span class="line-clamp-2 text-xs text-muted">{{ s.question || s.brief }}</span>
                <span class="flex flex-wrap gap-1">
                  <UBadge v-if="s.card" label="Brakeman's brief" color="primary" variant="subtle" size="sm" />
                  <UBadge v-if="s.blocks" :label="`${s.blocks} waiting on it`" color="neutral" variant="subtle" size="sm" />
                  <UBadge v-if="!s.actions.length" label="Open the terminal" color="warning" variant="subtle" size="sm" />
                  <UBadge v-else :label="`${s.actions.length} ${s.actions.length === 1 ? 'choice' : 'choices'}`" color="neutral" variant="outline" size="sm" />
                </span>
              </button>
            </li>
          </ul>
          <div v-if="showing" class="flex items-center justify-between gap-2 text-xs text-muted">
            <span data-decisions-showing>{{ showing }}</span>
            <UButton v-if="state.next" label="More" color="neutral" variant="outline" size="sm" class="min-h-11" data-decisions-more @click="inbox.more()" />
          </div>

          <section v-if="recent.length" class="flex flex-col gap-2" data-decisions-recent>
            <h2 class="text-xs font-semibold uppercase tracking-wide text-muted">Recent <span class="font-normal normal-case tracking-normal">· acted on from this browser, the last day</span></h2>
            <ul class="flex flex-col gap-1">
              <li v-for="e in recent" :key="trackKey(e)">
                <button type="button" class="flex min-h-11 w-full items-center justify-between gap-2 rounded-md px-3 py-2 text-left hover:bg-elevated/50" :data-recent-row="e.ref" :data-recent-receipt="e.receipt" @click="openRecent(e)">
                  <span class="min-w-0">
                    <span class="block truncate text-sm">{{ e.title || e.ref }}</span>
                    <span class="block truncate text-xs text-muted">{{ e.label }} · {{ relativeTime(new Date(e.at).toISOString()) }} ago</span>
                  </span>
                  <UBadge :label="recentWord(e).text" :color="recentWord(e).color" variant="subtle" size="sm" data-recent-word />
                </button>
              </li>
            </ul>
          </section>
        </section>

        <!-- The open item, whole. Below lg it takes the page, with the way back to the list. -->
        <section class="min-w-0" :class="!openRef && 'max-lg:hidden'" data-decisions-item>
          <UButton v-if="openRef" label="Back to the list" icon="i-lucide-arrow-left" color="neutral" variant="ghost" size="lg" class="mb-3 min-h-11 lg:hidden" data-inbox-back @click="back" />
          <InboxItem
            v-if="item"
            :key="item.ref"
            :item="item"
            :route-of="routeOf"
            :delivery="delivery"
            :busy="busy"
            :refusal="refusal"
            :unknown="unknown"
            :ask-brakeman="askBrakeman"
            :confirm-question="confirmQuestion"
            @act="act"
            @comment="comment"
            @reread="reread"
          />
          <div v-else-if="missing" class="flex flex-col gap-4">
            <UAlert color="neutral" variant="subtle" icon="i-lucide-info" :title="missing" data-inbox-missing />
            <!-- The item is gone, and what this browser did to it is what is left: its steps, as the server's track keeps them. -->
            <section v-if="delivery?.length" class="flex flex-col gap-2" data-inbox-missing-delivery>
              <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">What became of your action<template v-if="latest?.label"> · {{ latest.label }}</template></h3>
              <InboxDelivery :steps="delivery" :route-of="routeOf" />
            </section>
          </div>
          <p v-else-if="!openRef" class="hidden py-8 text-center text-sm text-muted lg:block">Pick an item to see its brief, its choices and what each authorizes.</p>
        </section>
      </div>

      <UModal
        v-model:open="phoneOpen"
        title="Open on your phone"
        :description="phoneWords"
      >
        <template #body>
          <div v-if="offer?.kind === 'qr' && qr" class="flex flex-col items-center gap-3" data-phone-offer="qr" :data-phone-url="offer.url">
            <svg :viewBox="qr.viewBox" class="size-48 rounded bg-white text-black" shape-rendering="crispEdges" role="img" :aria-label="offer.url" data-phone-qr>
              <path :d="qr.path" fill="currentColor" />
            </svg>
            <code class="break-all text-sm">{{ offer.url }}</code>
          </div>
          <p v-else-if="offer?.kind === 'loopback'" class="text-sm text-pretty" data-phone-offer="loopback">{{ offer.words }}</p>
        </template>
      </UModal>
    </template>
  </UDashboardPanel>
</template>
