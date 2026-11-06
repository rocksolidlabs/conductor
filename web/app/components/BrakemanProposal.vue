<script setup lang="ts">
import type { BrakemanRequest, BriefItem, RefResolution } from '~/utils/brakeman'
import { LAUNCH_FOLLOW_FIRST_MS, LAUNCH_FOLLOW_MAX_MS, actFor, actReceiptRef, actShown, actWords, followLaunch, readsWords, refRoute, refTicket } from '~/utils/brakeman'
import type { LaunchSummary } from '~/composables/useDispatch'
import { isStale, proposalArgs, proposalControl, proposalSends, staleWords } from '~/utils/brakemanCards'
import { commandBody, mintCommandId, sendCommand } from '~/utils/tasks'

/**
 * A brief's proposal (docs/plans/brakeman.md, PR-7): its text and the operation it names, as a button only for an operation whose
 * route itself refuses a stale revision (the guide's Operation.Revision: a run's pause and unpause, a task's cancel, retry and accept,
 * a ticket's edit, comment and link), sent through the client calls their own pages use with the proposal's revision and asked first
 * where those pages ask first. A ticket's operation sends the proposal's arguments, which its text need not match: the card shows each
 * field it sends, and the button asks first with them, so nothing goes out under the person's name that the person did not see. A
 * run's stop and resume, a crew's create and launch and any other operation are text with a link to the page that does them, where
 * the person acts on the live state: no comparison in the browser stands in for the route's check.
 *
 * A proposal Brakeman ran itself, under a grant the person made (PR-13), says so: "Brakeman did this", with its receipt and a link to
 * the run it launched or the ticket it changed, from the request's acts as the server keeps them, never from the brief's words. A
 * launch is done by its launch, not by its receipt, which Dispatch keeps before the run starts: the launch is read, and read again
 * while it is pending, and only a launch started says "Brakeman did this" and links its run; a launch that failed says so with its
 * words. A proposal it was not granted, or did not run, stays a proposal.
 */
const props = defineProps<{ item: Extract<BriefItem, { kind: 'proposal/1' }>; items: readonly BriefItem[]; refs: ReadonlyMap<string, RefResolution>; request?: BrakemanRequest }>()

const bk = useBrakeman()
const live = useAttention()
const sessions = useSessions()
const dispatch = useDispatchApi()
const identity = useIdentity()
const toast = useToast()

// The guide's operations, read once for the app (the admin's guide: every operation with the revision its route checks).
const ops = useGuideOps()

const control = computed(() => proposalControl(props.item, ops.value))

/** The act Brakeman ran for this proposal, from the request as the page holds it now (its events fold the acts' outcomes in). */
const req = computed(() => (props.request ? (bk.requests.value.find((r) => r.id === props.request!.id) ?? props.request) : null))
const act = computed(() => actFor(props.item, req.value?.acts))
const receiptRef = computed(() => (act.value ? actReceiptRef(act.value) : null))
const actTicket = computed(() => (act.value ? refTicket(act.value.target) : ''))
/** The launch an act's receipt names (GET /api/dispatch/launches/{id}), read until it has started or failed (followLaunch). */
const launch = ref<LaunchSummary | null>(null)
let following = 0
let gone = false
onBeforeUnmount(() => (gone = true))
watch(
  receiptRef,
  (ref) => {
    const g = ++following
    launch.value = null
    if (!ref) return
    void followLaunch({
      read: () => dispatch.launchRead(ref.slice('launch:'.length)),
      show: (l) => (launch.value = l),
      stop: () => gone || g !== following,
      wait: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
      firstMs: LAUNCH_FOLLOW_FIRST_MS,
      maxMs: LAUNCH_FOLLOW_MAX_MS,
    })
  },
  { immediate: true },
)
/** What the card shows of the act: a launch's by its launch (actShown). */
const shown = computed(() => (act.value ? actShown(act.value, launch.value) : ''))
/** The run the act's launch started: only a launch started has one. */
const launchedRun = computed(() => (shown.value === 'done' && launch.value?.state === 'started' ? launch.value.runId : ''))
/** What a ticket's operation sends, every field of it, shown on the card and in the dialog before it is sent. */
const sends = computed(() => (control.value.kind === 'button' && control.value.target === 'ticket' ? proposalSends(props.item.args) : []))
const to = computed(() => refRoute(props.item.target))
const busy = ref(false)
const asking = ref(false)
const refusal = ref('')

async function send() {
  const c = control.value
  if (c.kind !== 'button') return
  asking.value = false
  busy.value = true
  refusal.value = ''
  try {
    if (c.target === 'run') {
      const { receipt } = await sendCommand((body) => sessions.pauseRun(c.run!, c.op === 'run_pause', body), commandBody(c.expectedRevision))
      toast.add({ title: c.op === 'run_pause' ? 'The run is paused' : 'The run dispatches again', description: receipt.repeat ? 'It was done already.' : undefined, icon: 'i-lucide-check', color: 'success' })
    } else if (c.target === 'task') {
      const command = c.op === 'task_retry' ? 'retry' : c.op === 'task_accept' ? 'accept' : 'cancel'
      await sendCommand((body) => sessions.taskCommand(c.run!, c.task!, command, body), commandBody(c.expectedRevision))
      toast.add({ title: `${c.task}: ${command === 'retry' ? 'one more attempt allowed' : command === 'accept' ? 'the result accepted' : 'cancelled'}`, icon: 'i-lucide-check', color: 'success' })
    } else {
      const args = proposalArgs(props.item.args)
      const body = { ...args, opId: mintCommandId(), expectedRev: c.expectedRevision, ...(identity.name.value.trim() ? { byName: identity.name.value.trim() } : {}) }
      const key = c.ticket!
      if (c.op === 'ticket_edit') await sendCommand((b) => dispatch.editTicket(key, b as never), body)
      else if (c.op === 'ticket_comment_admin') await sendCommand((b) => dispatch.addComment(key, b as never), body)
      else await sendCommand((b) => dispatch.addLink(key, b as never), body)
      toast.add({ title: `${key}: done`, icon: 'i-lucide-check', color: 'success' })
    }
  } catch (e) {
    refusal.value = await refusedWords(e)
  } finally {
    busy.value = false
    if (c.run) void live.refreshRun(c.run).catch(() => null)
  }
}

/** A refusal in words: a stale revision says what the record is at now, read again; any other is the route's own. */
async function refusedWords(e: unknown): Promise<string> {
  const c = control.value
  if (c.kind !== 'button' || !isStale(e)) return (e as Error).message
  if (c.target === 'ticket') {
    const t = await dispatch.ticket(c.ticket!).catch(() => null)
    return staleWords('ticket', c.expectedRevision, t?.rev)
  }
  const run = await live.refreshRun(c.run!).catch(() => null)
  return staleWords('run', c.expectedRevision, run?.managed?.revision, run?.paused ? 'paused' : undefined)
}

function press() {
  const c = control.value
  if (c.kind !== 'button') return
  if (c.ask) asking.value = true
  else void send()
}
</script>

<template>
  <div class="flex flex-col gap-1.5" data-brief-proposal :data-proposal-op="item.op" :data-proposal-act="shown">
    <p class="text-sm">{{ item.text }}</p>
    <div class="flex flex-wrap items-center gap-1.5 text-xs text-muted">
      <span>Operation <code class="font-mono">{{ item.op }}</code> on</span>
      <BrakemanRefChip :ref-name="item.target" :resolution="refs.get(item.target)" />
      <span v-if="item.expectedRevision">· at revision {{ item.expectedRevision }}</span>
      <span v-if="readsWords(item.facts, items)">· {{ readsWords(item.facts, items) }}</span>
    </div>
    <dl v-if="sends.length" class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 rounded-md border border-default p-2 text-xs" data-proposal-args>
      <template v-for="a in sends" :key="a.field">
        <dt class="font-mono text-muted">{{ a.field }}</dt>
        <dd class="whitespace-pre-wrap break-words font-mono" :data-proposal-arg="a.field">{{ a.value }}</dd>
      </template>
    </dl>
    <div v-if="act" class="flex flex-col gap-1 rounded-md border px-2 py-1.5" :class="shown === 'done' ? 'border-primary/40 bg-primary/5' : 'border-default bg-elevated/40'" data-proposal-did>
      <div class="flex flex-wrap items-center gap-1.5 text-sm">
        <UBadge :label="shown === 'done' ? 'Brakeman did this' : shown === 'pending' ? 'Brakeman is doing this' : 'Not done'" :color="shown === 'done' ? 'primary' : shown === 'refused' || shown === 'failed' ? 'warning' : 'neutral'" variant="subtle" size="sm" data-proposal-did-badge />
        <span class="text-xs text-muted" data-proposal-did-words>{{ actWords(act, launch) }}</span>
      </div>
      <div v-if="act.state === 'done'" class="flex flex-wrap items-center gap-1.5 text-xs text-muted">
        <BrakemanRefChip v-if="receiptRef" :ref-name="receiptRef" data-proposal-did-receipt />
        <UButton v-if="launchedRun" :to="`/runs/${encodeURIComponent(launchedRun)}`" label="The run it launched" icon="i-lucide-arrow-up-right" size="xs" color="neutral" variant="ghost" :data-proposal-did-run="launchedRun" />
        <UButton v-if="actTicket" :to="refRoute(`ticket:${actTicket}`) ?? undefined" :label="`${actTicket}, the ticket it changed`" icon="i-lucide-ticket" size="xs" color="neutral" variant="ghost" data-proposal-did-ticket />
      </div>
    </div>
    <!-- Done, or under way: Brakeman's act is the proposal's outcome, and no control sends it again. A launch that failed started
         nothing: the proposal's control is offered again. -->
    <template v-if="shown !== 'done' && shown !== 'pending'">
      <div v-if="control.kind === 'button'" class="flex flex-wrap items-center gap-2">
        <UButton :label="control.label" size="sm" color="neutral" variant="outline" :loading="busy" data-proposal-button @click="press" />
        <span class="text-xs text-muted">Its route refuses it if what it acts on has moved since Brakeman read it.</span>
      </div>
      <p v-else class="text-xs text-muted" data-proposal-text>
        {{ control.words }}
        <NuxtLink v-if="to" :to="to" class="underline" data-proposal-link>Its page</NuxtLink>
      </p>
    </template>
    <UAlert v-if="refusal" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="refusal" data-proposal-refused />

    <UModal v-model:open="asking" :title="control.kind === 'button' ? `${control.label.replace(/…$/, '')}?` : ''" description="Brakeman proposed it; you decide. Its route refuses it if the record has moved on since Brakeman read it.">
      <template v-if="sends.length" #body>
        <p class="mb-2 text-sm">It sends, under your name:</p>
        <dl class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs" data-proposal-confirm-args>
          <template v-for="a in sends" :key="a.field">
            <dt class="font-mono text-muted">{{ a.field }}</dt>
            <dd class="whitespace-pre-wrap break-words font-mono" :data-proposal-arg="a.field">{{ a.value }}</dd>
          </template>
        </dl>
      </template>
      <template #footer>
        <div class="flex w-full justify-end gap-2">
          <UButton label="Keep it" color="neutral" variant="ghost" @click="asking = false" />
          <UButton label="Do it" color="warning" :loading="busy" data-proposal-confirm @click="send" />
        </div>
      </template>
    </UModal>
  </div>
</template>
