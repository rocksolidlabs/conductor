<script setup lang="ts">
import { ApiError } from '~/composables/useApi'
import type { AgentInfo, RunInfo } from '~/composables/useSessions'
import { draftMember, memberNameError, memberNameFrom, responsibilityWords, type DraftMember } from '~/utils/crews'
import { avatarTone } from '~/utils/avatar'
import { personPath } from '~/utils/people'
import { runResumable, runSubtitle, runTicketChips, shortRunId } from '~/utils/crewWords'
import { runBarParts, runItems, runStatusLine, type BarSegment } from '~/utils/status'
import { initials, relativeTime } from '~/utils/sessions'
import { commandBody, sendCommand } from '~/utils/tasks'
import type { RelatedTicket } from '~/composables/useDispatch'
import { ticketPath } from '~/utils/dispatch'
import { revisionTag } from '~/utils/dispatchPlans'
import { ACCOUNT_UNREAD, accountUnreadable, addMemberProblem, boundWords, readNamedAccounts } from '~/utils/crewLaunch'
import { runOffer } from '~/utils/runs'
import { comparisonLink, runCompare } from '~/utils/compare'
import { packageSourceWords } from '~/utils/publication'
import { runOriginLine, TIME_LIMIT_PASSED, timeLimitPassed } from '~/utils/runOrigin'

/**
 * The run page's header: the run named by its crew and its start, how many
 * members need input and run (a managed run: its tasks done, failed, running
 * and checking, whether its dispatch is paused, with the words of a pause the
 * run made itself, and whether its trees are being cleaned up), how long it has been
 * up, and its actions: add an agent mid-run, share the run (a run link),
 * pause or resume a managed run's dispatch (a command with a receipt), stop
 * every member, and once stopped resume it as a new run. Each action calls
 * the server and hands the run as it is after it to the page (`changed`).
 * Beside its name, the Dispatch ticket the run is linked to, as a chip that
 * opens the ticket's page, "(deleted)" on it when the ticket is a tombstone;
 * for a run launched from a ticket's plan revision (DS2), "Launched from
 * CND-12 · plan r3" from the run's own `ticket`, which its continuations
 * carry, opening the ticket, with who launched it (the launch's own record:
 * Brakeman, for a launch it ran under a grant) and the launch's receipt,
 * read from its chip. Under the bar, the run's people as the crew was
 * launched (HM1), each a chip with the responsibilities they hold, opening
 * their page: never started, they own its decisions and its tasks' outcomes. Under them, each member's
 * login and model as the launch bound them (docs/plans/capacity.md, PR-7): an account by its label (the
 * accounts are the admin's), any other login by where it comes from; for a run launched from a ticket's
 * revision that named no account for a member, that it took the default at the launch. A comparison's run (Program item CP1) links to
 * its ticket's compare view after the sentence, and from the run's menu. A run a registered provider launched says so in row two:
 * "Launched by <label> for <subject> at <start commit>", the subject a link when it is an https link the page may open.
 */
const props = withDefaults(
  defineProps<{
    run: RunInfo | null
    runId: string
    now: number
    chatUnread?: number
    chatOpen?: boolean
    chatOffline?: boolean
    /** A record of a run the server no longer holds (#127): read-only, so no agent added, nothing stopped, no resume until a run can be resumed from its record. */
    record?: boolean
    /** The Dispatch ticket the run is linked to (GET /api/dispatch/links?run=), null when none. */
    ticket?: RelatedTicket | null
  }>(),
  { chatUnread: 0, chatOpen: false, chatOffline: false, record: false, ticket: null },
)
const emit = defineEmits<{ changed: [run: RunInfo]; chat: [] }>()

const api = useSessions()
const toast = useToast()
const live = useAttention()
const router = useRouter()

// Who launched a run from a ticket, from the launch it names (GET /api/dispatch/launches/{id}): once, a launch never changes who made it.
const dispatchApi = useDispatchApi()
const launchBy = ref('')
// The linked item the launch's package names as its source (IN1a): "GitHub acme/api#612" in the launched-from chip; '' for none.
const launchSource = ref('')
watch(
  () => props.run?.ticket?.launch,
  async (id) => {
    launchBy.value = ''
    launchSource.value = ''
    if (!id) return
    try {
      const l = await dispatchApi.launchRead(id)
      if (props.run?.ticket?.launch === id) {
        launchBy.value = l.createdBy
        launchSource.value = packageSourceWords(l.package)
      }
    } catch {
      /* the launch's chip reads it when opened */
    }
  },
  { immediate: true },
)

// The accounts the members are bound to, by their labels; and what the ticket's revision named for each (its body never changes).
const accounts = useAccounts()
onMounted(() => void accounts.store.load())
// A failed read is said in the Logins, and read again as they open: the lines never pass for complete without it.
const named = ref<{ plan: string; members?: Record<string, string>; error?: string } | null>(null)
async function readNamed() {
  const plan = props.run?.ticket?.plan
  if (!plan || (named.value?.plan === plan && named.value.members)) return
  // Under way: the Logins say so, and no line passes for complete until it answers, or fails at its deadline.
  named.value = { plan }
  try {
    const members = await readNamedAccounts((signal) => dispatchApi.plan(plan, signal))
    if (props.run?.ticket?.plan === plan) named.value = { plan, members }
  } catch (e) {
    if (props.run?.ticket?.plan === plan) named.value = { plan, error: (e as Error).message }
  }
}
watch(() => props.run?.ticket?.plan, readNamed, { immediate: true })
const namedNow = computed(() => (named.value && named.value.plan === props.run?.ticket?.plan ? named.value : null))
/** The revision's read is under way: the run was launched from a ticket's, and it has neither answered nor failed. */
const namedReading = computed(() => !!props.run?.ticket?.plan && !namedNow.value?.members && !namedNow.value?.error)
// A registered account while the accounts are being read, or could not be: said in words, never by its id.
const unread = computed(() => (accounts.problem.value || accounts.unavailable.value ? accountUnreadable(accounts.problem.value || accounts.unavailable.value) : ACCOUNT_UNREAD))
const logins = computed(() => (props.run?.members ?? []).map((m) => ({ name: m.name, ...boundWords(m, accounts.doc.value, namedNow.value?.members?.[m.name], props.run?.class === 'brakeman', unread.value) })))
/** The Logins opened: a read that failed or ran out, the revision's or the accounts', is read again. */
function loginsOpened() {
  if (namedNow.value?.error) void readNamed()
  if (accounts.problem.value) void accounts.store.load()
}

// Where a registered provider's launch came from (the run's origin): the words are runOrigin.ts's.
const origin = computed(() => runOriginLine(props.run?.origin))
// A provider's run past its launch's time limit takes no continuation: the header says so where Resume would be.
const limitPassed = computed(() => timeLimitPassed(props.run?.origin, props.now))

const stopped = computed(() => !!props.run?.stoppedAt)
/** Resume as new run: a stopped run the server holds, or a record (#153) not resumed yet. */
const resumable = computed(() => !!props.run && !limitPassed.value && runResumable(props.run, !stopped.value && !props.record))
const uptime = computed(() => {
  const r = props.run
  if (!r) return ''
  const where = `server · ${r.isolation === 'worktree' ? 'worktrees' : 'shared'}`
  if (r.stoppedAt) return `stopped · ran ${relativeTime(r.startedAt, Date.parse(r.stoppedAt))} · ${where}`
  return `up ${relativeTime(r.startedAt, props.now)} · ${where}`
})

// Add agent: one row of the crew editor's members table.
const addOpen = ref(false)
const agents = ref<AgentInfo[]>([])
const member = ref<DraftMember[]>([])
const adding = ref(false)
const addError = ref('')
const runNames = computed(() => props.run?.members.map((m) => m.name) ?? [])
// The run's people's names, which the server refuses for a member added to the run (a person is never a member).
const peopleNames = computed(() => props.run?.people?.map((p) => p.name) ?? [])
// The table's own check of the account it marks refused holds Add too.
const addTable = ref<{ accountProblem: string } | null>(null)
const addInvalid = computed(() => !!addMemberProblem(member.value[0], runNames.value, peopleNames.value, addTable.value?.accountProblem))

async function openAdd() {
  addError.value = ''
  addOpen.value = true
  try {
    if (!agents.value.length) agents.value = await api.catalog()
  } catch (e) {
    addError.value = (e as Error).message
  }
  const first = agents.value[0]
  member.value = first ? [draftMember({ name: memberNameFrom(first.id, [...runNames.value, ...peopleNames.value]), agentId: first.id, prompt: '', start: { when: 'immediately' } })] : []
}

async function add() {
  const m = member.value[0]
  if (!m || addInvalid.value) return
  adding.value = true
  addError.value = ''
  try {
    const run = await api.addRunMember(props.runId, m)
    emit('changed', run)
    addOpen.value = false
    toast.add({ title: `${m.name} joined the run`, icon: 'i-lucide-user-plus', color: 'success' })
  } catch (e) {
    addError.value = (e as Error).message
  } finally {
    adding.value = false
  }
}

const shareOpen = ref(false)

// Resume as new run: a new run of the crew in which every member with a conversation continues it in its kept worktree.
const resuming = ref(false)
async function resumeRun() {
  resuming.value = true
  try {
    const next = await api.resumeRun(props.runId)
    live.applyRun(next)
    toast.add({ title: next.managed ? 'Run continued' : 'Run resumed', description: next.managed ? `${next.id}: the results kept, the rest taken up.` : `${next.id}: the conversations continue in their worktrees.`, icon: 'i-lucide-play', color: 'success' })
    await router.push(`/runs/${encodeURIComponent(next.id)}`)
  } catch (e) {
    // Resumed already (#126): the continuation the server names is the run to open.
    if (e instanceof ApiError && e.code === 'already_resumed' && typeof e.details.run === 'string') {
      await router.push(`/runs/${encodeURIComponent(e.details.run)}`)
      return
    }
    toast.add({ title: 'Resume failed', description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    resuming.value = false
  }
}

// The ticket chips beside the name: one for one ticket (the launch's), both only when they name two.
const chips = computed(() => runTicketChips(props.run?.ticket, props.ticket))

// What the run is doing, in row two (utils/status.ts): its bar, its tasks counted by part (runBarParts), and its one sentence
// (runStatusLine), whose "N need you" is the run's items of the one list (useNeedsYou): each thing a person can act on, once, a plan
// gate's decision that holds no task among them.
const needsYou = useNeedsYou()
const statusLine = computed(() => (props.run ? runStatusLine(props.run, live.sessions.value, runItems(needsYou.items.value, props.runId), props.now) : null))
const bar = computed(() => (statusLine.value ? runBarParts(statusLine.value) : []))
/** A segment's fill: its mark's tone (amber for what waits on a person, green for a live agent, red for failed), else neutral ink. */
const SEGMENT: Record<BarSegment['part'], string> = {
  done: 'bg-(--ui-text-muted)',
  working: 'bg-(--ui-success)',
  checking: 'bg-(--ui-success)',
  finished: 'bg-(--ui-text-dimmed)',
  running: 'bg-(--ui-success)',
  needs: 'bg-(--ui-warning)',
  failed: 'bg-(--ui-error)',
  waiting: 'bg-accented',
  cancelled: 'bg-accented',
}

// A managed run's Pause / Resume of its dispatch.
const pausing = ref(false)
async function togglePause() {
  const r = props.run
  if (!r?.managed) return
  const paused = !r.paused
  pausing.value = true
  try {
    await sendCommand((body) => api.pauseRun(props.runId, paused, body), commandBody())
    toast.add({ title: paused ? 'Scheduling paused' : 'Scheduling resumed', description: paused ? 'Attempts in flight go on; no task starts until it is resumed.' : undefined, icon: paused ? 'i-lucide-pause' : 'i-lucide-play', color: 'neutral' })
  } catch (e) {
    toast.add({ title: paused ? 'Not paused' : 'Not resumed', description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    pausing.value = false
  }
}

// Stop asks first.
const stopOpen = ref(false)
const stopping = ref(false)

async function stopAll() {
  stopping.value = true
  try {
    const run = await api.stopRun(props.runId)
    emit('changed', run)
    stopOpen.value = false
    toast.add({ title: 'Run stopped', description: 'Every member was stopped; the worktrees and branches stay.', icon: 'i-lucide-square', color: 'neutral' })
  } catch (e) {
    toast.add({ title: 'Stop failed', description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    stopping.value = false
  }
}

// A comparison's run (Program item CP1): its ticket's compare view, linked in row two and in the menu, never in row one.
const comparison = computed(() => (runCompare(props.run) ? comparisonLink(props.run?.ticket?.key ?? props.ticket?.key, props.runId) : null))

// Add agent and Share, in the run's one menu: each disabled where its button was; Comparison for a comparison's run.
const menu = computed(() => [
  [
    { label: 'Add agent', icon: 'i-lucide-user-plus', disabled: !runOffer(props.run).add, onSelect: () => void openAdd(), 'data-run-add': '' },
    { label: 'Share', icon: 'i-lucide-share-2', disabled: !props.run, onSelect: () => (shareOpen.value = true), 'data-run-share': '' },
    ...(comparison.value ? [{ label: 'Comparison', icon: 'i-lucide-columns-3', to: comparison.value, 'data-run-comparison-menu': '' }] : []),
  ],
])

// The run page's command palette (#302) runs the header's actions: Add agent, Share, Stop (asked in the palette's row), Resume.
defineExpose({ openAdd, share: () => (shareOpen.value = true), stopNow: stopAll, resume: resumeRun, togglePause })
</script>

<template>
  <!-- Two rows: the name, its ticket and the run's actions; under them, at every width, what the run is doing and who is in it. -->
  <div class="flex-none border-b border-default" data-crew-run-header>
    <!-- A low window (480 px tall or less) gives the page's tiles their rows: row one is lower, row two gives way, and the run's bar
         and sentence move into row one, after the name and its chips. -->
    <UDashboardNavbar :toggle="false" :ui="{ root: 'h-14 border-b-0 [@media(max-height:480px)]:h-11', left: 'min-w-0 flex-1', right: 'flex-none gap-1.5' }" data-run-row-one>
      <template #leading>
        <IconButton icon="i-lucide-arrow-left" color="neutral" variant="ghost" label="Back to the list" class="lg:hidden" to="/sessions" data-back-to-list />
      </template>
      <template #title>
        <span class="flex min-w-0 items-baseline gap-2">
          <!-- The name is whole from lg up (a crew's name is at most 60 characters); below it, the label and the short id give way first. -->
          <NuxtLink v-if="run" :to="`/crews/${encodeURIComponent(run.crewId)}`" class="min-w-[12ch] shrink truncate hover:underline lg:min-w-0 lg:flex-none" :title="`The saved crew ${run.name}`" data-run-name>{{ run.name }}</NuxtLink>
          <span v-else class="truncate">Run</span>
          <span v-if="run" class="min-w-0 shrink-[4] truncate text-[13px] font-normal text-muted" :class="!run.label && 'hidden sm:inline'" :title="runSubtitle(run)" data-run-subtitle>{{ runSubtitle(run) }}</span>
          <span class="hidden flex-none font-mono text-[11px] font-normal text-dimmed xl:inline" :title="runId">{{ shortRunId(runId) }}</span>
          <UButton
            v-if="chips.launched"
            :to="ticketPath(chips.launched.key)"
            icon="i-lucide-ticket"
            size="xs"
            color="neutral"
            variant="soft"
            class="min-w-0 shrink-[2] self-center"
            :ui="{ label: 'truncate' }"
            :title="`Launched from ${chips.launched.key} at its revision ${chips.launched.rev}, by plan revision ${chips.launched.planN}${launchSource ? `, its source ${launchSource}` : ''}: the run follows the ticket as launched`"
            data-run-launched-from
            :data-run-launched-key="chips.launched.key"
          >
            <span class="truncate">Launched from {{ chips.launched.key }} · plan {{ revisionTag(chips.launched.planN) }}<span v-if="launchSource" data-run-source> · {{ launchSource }}</span></span>
          </UButton>
          <UButton
            v-if="chips.linked"
            :to="ticketPath(chips.linked.key)"
            :label="chips.linked.deletedAt ? `${chips.linked.key} (deleted)` : chips.linked.key"
            icon="i-lucide-ticket"
            size="xs"
            color="neutral"
            :variant="chips.linked.deletedAt ? 'ghost' : 'soft'"
            class="flex-none self-center font-mono"
            :class="chips.linked.deletedAt && 'text-muted'"
            :title="`${chips.linked.key} ${chips.linked.title}${chips.linked.deletedAt ? ' (deleted)' : ''}: the ticket this run is linked to`"
            :aria-label="`The ticket ${chips.linked.key}${chips.linked.deletedAt ? ', deleted' : ''}: ${chips.linked.title}`"
            data-run-ticket
            :data-run-ticket-key="chips.linked.key"
            :data-run-ticket-deleted="chips.linked.deletedAt ? '' : undefined"
          />
          <span v-if="statusLine?.text" class="hidden min-w-0 shrink-[3] items-center gap-2 self-center text-xs font-normal text-default [@media(max-height:480px)]:inline-flex" :title="statusLine.text" data-run-progress-low>
            <span v-if="bar.length" class="flex h-1.5 w-16 flex-none overflow-hidden rounded-full bg-elevated" aria-hidden="true">
              <span v-for="b in bar" :key="b.part" class="h-full" :class="SEGMENT[b.part]" :style="{ flexGrow: b.count, flexBasis: 0 }" />
            </span>
            <span class="truncate">{{ statusLine.text }}</span>
          </span>
        </span>
      </template>
      <template #right>
        <!-- The run's chat (design 2e): an icon with the count while the drawer is closed. -->
        <IconButton icon="i-lucide-message-circle" color="neutral" variant="outline" label="Chat" :disabled="!run" :class="chatOpen && 'ring-2 ring-primary/40'" data-run-chat-button :data-run-chat-offline="chatOffline ? '' : undefined" @click="emit('chat')">
          <ChatUnreadPill :count="chatUnread" />
        </IconButton>
        <!-- Icons only on a phone: the labels would push the name off the bar. -->
        <IconButton v-if="resumable" icon="i-lucide-play" color="primary" variant="soft" :label="run?.managed ? 'Continue the run' : 'Resume as new run'" :loading="resuming" data-run-resume @click="resumeRun"><span class="hidden sm:inline">{{ run?.managed ? 'Continue' : 'Resume as new run' }}</span></IconButton>
        <IconButton v-if="run?.managed && !stopped && !record" :icon="run.paused ? 'i-lucide-play' : 'i-lucide-pause'" color="neutral" variant="outline" :label="run.paused ? 'Resume scheduling' : 'Pause scheduling'" :loading="pausing" data-run-pause :data-run-paused="run.paused ? '' : undefined" @click="togglePause"><span class="hidden sm:inline">{{ run.paused ? 'Resume' : 'Pause' }}</span></IconButton>
        <!-- Neutral at rest: red only in its confirm. -->
        <IconButton v-if="!stopped && !record" icon="i-lucide-square" color="neutral" variant="outline" label="Stop" :disabled="!run" data-run-stop @click="stopOpen = true"><span class="hidden sm:inline">Stop</span></IconButton>
        <UDropdownMenu :items="menu" :content="{ align: 'end' }">
          <IconButton icon="i-lucide-ellipsis" color="neutral" variant="ghost" label="More for this run" data-run-menu />
        </UDropdownMenu>
        <FullscreenButton class="hidden sm:inline-flex" />
      </template>
    </UDashboardNavbar>

    <!-- Row two: what the run is doing, its pause, its people and logins, who launched it, how long it has been up. -->
    <div v-if="run" class="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1.5 px-4 pb-2 text-xs [@media(max-height:480px)]:hidden" data-run-summary>
      <UBadge v-if="record" label="record" color="neutral" variant="subtle" size="sm" class="flex-none" title="A run the server no longer holds, as it was when it ended: read-only" data-run-record />
      <span v-if="bar.length" class="flex h-1.5 w-28 flex-none overflow-hidden rounded-full bg-elevated" role="img" :aria-label="bar.map((b) => b.words).join(', ')" data-run-bar>
        <span v-for="b in bar" :key="b.part" class="h-full" :class="SEGMENT[b.part]" :style="{ flexGrow: b.count, flexBasis: 0 }" :title="b.words" :data-part="b.part" :data-count="b.count" />
      </span>
      <span v-if="statusLine?.text" class="min-w-0 text-default" :data-needs="statusLine.needs" data-run-progress data-run-state>{{ statusLine.text }}</span>
      <UButton v-if="comparison" :to="comparison" label="Comparison" icon="i-lucide-columns-3" size="xs" color="neutral" variant="ghost" class="flex-none" :title="`The comparison of this run's candidates, on ${run.ticket?.key ?? ticket?.key}`" data-run-comparison />
      <span v-if="run.paused" class="inline-flex min-w-0 max-w-full items-center gap-1 text-warning" :title="run.pausedWhy ? `Paused: ${run.pausedWhy}` : 'Paused by a person'" data-run-paused-note>
        <UIcon name="i-lucide-pause" class="size-3.5 flex-none" />
        <span class="flex-none">scheduling paused</span>
        <span v-if="run.pausedWhy" class="min-w-0 truncate" data-run-paused-why>{{ run.pausedWhy }}</span>
      </span>
      <span v-if="(run.cleaning ?? 0) > 0" class="inline-flex flex-none items-center gap-1 text-muted" :title="`${run.cleaning} ${run.cleaning === 1 ? 'tree is' : 'trees are'} being ended: services stopped, cleanup steps run`" data-run-cleaning>
        <UIcon name="i-lucide-loader-circle" class="size-3.5 animate-spin" />cleaning up
      </span>
      <!-- The run's people as launched: names, never sessions. -->
      <span v-if="run.people?.length" class="inline-flex min-w-0 max-w-full flex-wrap items-center gap-1.5" data-run-people>
        <span class="text-muted">People</span>
        <NuxtLink
          v-for="p in run.people"
          :key="p.name"
          :to="personPath(p.name)"
          class="inline-flex min-w-0 max-w-full items-center gap-1.5 rounded-md px-1.5 py-0.5 ring ring-default hover:bg-elevated"
          :title="p.note || undefined"
          :data-run-person="p.name"
          :data-run-person-responsibilities="(p.responsibilities ?? []).join(' ')"
        >
          <span class="grid size-4 flex-none place-items-center rounded-full text-[8px] font-semibold" :class="avatarTone(p.name)" aria-hidden="true">{{ initials(p.name) }}</span>
          <span class="min-w-0 truncate font-medium text-highlighted">{{ p.name }}</span>
          <span v-if="p.responsibilities?.length" class="truncate text-muted" data-run-person-words>{{ responsibilityWords(p.responsibilities) }}</span>
        </NuxtLink>
      </span>
      <!-- Each member's login and model as the launch bound them, behind a button. -->
      <UPopover v-if="logins.length" :content="{ align: 'start' }" @update:open="(o: boolean) => o && loginsOpened()">
        <UButton icon="i-lucide-key-round" size="xs" color="neutral" variant="ghost" class="flex-none" aria-label="Logins" title="Each member's login and model, as the launch bound them" data-run-logins-button>Logins</UButton>
        <template #content>
          <ul class="flex max-w-[min(32rem,calc(100vw-2rem))] flex-col gap-1.5 p-3 text-xs" data-run-logins>
            <li v-if="namedNow?.error" class="text-warning [overflow-wrap:anywhere]" data-run-logins-unread>Plan revision {{ run?.ticket?.planN }} could not be read, so which member took the default at the launch is not said: {{ namedNow.error }}</li>
            <li v-if="namedReading" class="flex items-center gap-1.5 text-muted" data-run-logins-reading><UIcon name="i-lucide-loader-circle" class="size-3.5 animate-spin" />Reading plan revision {{ run?.ticket?.planN }}: each member's login is said once it is read.</li>
            <li v-for="l in namedReading ? [] : logins" :key="l.name" class="flex min-w-0 flex-wrap items-baseline gap-x-1.5" :data-run-login="l.name">
              <span class="font-mono font-medium text-highlighted">{{ l.name }}</span>
              <span class="[overflow-wrap:anywhere]" data-run-login-account>{{ l.account }}</span>
              <span v-if="l.atLaunch" class="text-muted" data-run-login-at-launch>(the default at the launch)</span>
              <span class="text-muted [overflow-wrap:anywhere]" data-run-login-model>· {{ l.model }}</span>
            </li>
          </ul>
        </template>
      </UPopover>
      <span v-if="run.ticket?.launch" class="inline-flex flex-none items-center gap-1 text-muted" data-run-launch :data-run-launch-by="launchBy || undefined">
        <template v-if="launchBy">launched by {{ launchBy }}</template>
        <BrakemanRefChip :ref-name="`launch:${run.ticket.launch}`" />
      </span>
      <!-- A run a registered provider launched: by its label, for its subject (its link when the page may open it), at its start commit. -->
      <span v-if="origin" class="inline-flex min-w-0 max-w-full flex-wrap items-baseline gap-x-1 text-muted" :title="origin.text" :aria-label="origin.text" data-run-origin :data-run-origin-continued="run.origin?.continued ? '' : undefined">
        <UIcon name="i-lucide-plug" class="size-3.5 flex-none self-center" />
        <span class="flex-none">{{ origin.lead }}</span>
        <bdi class="min-w-0 truncate font-medium text-highlighted" data-run-origin-label>{{ origin.label }}</bdi>
        <template v-if="origin.subject">
          <span class="flex-none">for</span>
          <a v-if="origin.href" :href="origin.href" target="_blank" rel="noopener noreferrer" :title="origin.href" :aria-label="`${origin.subject}, a link that opens ${origin.href}`" class="min-w-0 truncate font-mono text-primary underline underline-offset-2 [unicode-bidi:isolate]" data-mono="id" data-run-origin-subject data-run-origin-link>{{ origin.subject }}</a>
          <span v-if="origin.host" class="max-w-full flex-none whitespace-normal text-default [overflow-wrap:anywhere]" :title="origin.href" data-run-origin-host>(opens {{ origin.host }})</span>
          <bdi v-else class="min-w-0 truncate font-mono text-default" data-mono="id" data-run-origin-subject>{{ origin.subject }}</bdi>
        </template>
        <template v-if="origin.commit">
          <span class="flex-none">at</span>
          <span class="flex-none font-mono text-default" data-mono="commit" data-run-origin-commit>{{ origin.commit }}</span>
        </template>
      </span>
      <!-- Past its launch's time limit: what Resume, Retry and Request changes would do is refused, and the page says why. -->
      <span v-if="limitPassed" class="inline-flex min-w-0 max-w-full items-center gap-1 text-warning" :title="TIME_LIMIT_PASSED" data-run-time-limit>
        <UIcon name="i-lucide-timer-off" class="size-3.5 flex-none" />
        <span class="min-w-0">{{ TIME_LIMIT_PASSED }}</span>
      </span>
      <span class="ml-auto flex-none text-muted" data-run-uptime>{{ uptime }}</span>
    </div>
  </div>

  <USlideover v-model:open="addOpen" title="Add an agent" description="It joins this run, not the saved crew. One that starts at launch starts now; its prompt is typed once it is ready." :ui="{ content: 'sm:max-w-4xl' }">
    <template #body>
      <div class="flex flex-col gap-4">
        <UAlert v-if="addError" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="addError" />
        <!-- Asked as the server binds a member added to the run: under the run's yolo choice (a managed run takes none by hand). -->
        <CrewMembersTable v-if="member.length" ref="addTable" v-model="member" :agents="agents" :others="runNames" :people="peopleNames" :accounts="accounts.doc.value" :yolo="run?.yolo" single />
        <p v-else-if="!addError" class="text-sm text-muted">No agents in the catalog.</p>
      </div>
    </template>
    <template #footer>
      <div class="flex w-full justify-end gap-2">
        <UButton label="Cancel" color="neutral" variant="ghost" @click="addOpen = false" />
        <UButton label="Add to the run" icon="i-lucide-user-plus" :loading="adding" :disabled="addInvalid" @click="add" />
      </div>
    </template>
  </USlideover>

  <ShareLinksModal v-model:open="shareOpen" :run-id="runId" :session-name="run?.name" />

  <UModal v-model:open="stopOpen" :title="`Stop this run of ${run?.name || 'the crew'}?`" description="Every member's session is stopped; a member not started yet never starts. The worktrees and their branches stay, and the run can be resumed as a new run.">
    <template #footer>
      <div class="flex w-full justify-end gap-2">
        <UButton label="Cancel" color="neutral" variant="ghost" @click="stopOpen = false" />
        <UButton label="Stop the run" icon="i-lucide-square" color="error" :loading="stopping" data-confirm-stop @click="stopAll" />
      </div>
    </template>
  </UModal>
</template>
