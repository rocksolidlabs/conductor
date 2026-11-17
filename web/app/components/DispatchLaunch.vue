<script setup lang="ts">
import type { DispatchTicket, PlanBody, PlanSummary } from '~/composables/useDispatch'
import type { TicketPlans } from '~/composables/useTicketPlans'
import type { AgentInfo } from '~/composables/useSessions'
import { PLAN_BOUNDS, revisionTag, shortCommit, yoloWords, type LaunchOutcome } from '~/utils/dispatchPlans'
import { launchLines } from '~/utils/crewLaunch'
import { compareLaunch, launchButtonWords } from '~/utils/compareLaunch'
import { accountCountWords, launchAccounts, memberLogin } from '~/utils/launchAccounts'

/**
 * Launching a managed run from a ticket's approved plan revision (Program item DS2, docs/plans/dispatch-evidence.md, "web/app"). It asks
 * first: the revision, the commit the run starts from, the yolo choice said plainly, and the run's label (a name the person may give; the
 * server names a run left unnamed after the ticket, and the dialog shows no name it cannot know). Then it says what came of it: the run started, with its link; the launch failed, with why; still being started, sent
 * again under the same operation (never a second run); or refused before any run, in the server's words. The operation's id is minted
 * once per launch asked for, with the label it is first sent with, and both are kept until it is settled: a reply lost to the network, or
 * a pending launch sent again, is the same launch, sent unchanged (the label is not edited meanwhile; another would be a conflict, not the
 * launch). Closing the dialog does not settle it: opened again on the same revision while it is unsettled, it shows that launch as it
 * stands (still being sent, pending, or no answer) and sends it again under its id. Nothing is launched before the revision's body is
 * read: the yolo choice is said from it, never assumed. Under it, each of the revision's members, read only, with its agent, its model and
 * its account as the revision names them (docs/plans/capacity.md, PR-7): they are what the operator approved; a member that names no
 * account takes the login the server resolves at this launch, said as its view of the line names it now (launchAccountWords). Each line
 * has its usage as the server last read it. A comparison's revision (CP1) says, above those lines, what it starts (compareStatement, from the
 * revision's own `compare` part: the candidates and their sessions, the reviews' as "up to", the multiplier with its refunds, the checks at
 * the base, the Brakeman turn, no total), and its button says the count, "Launch 3 candidates". That count is sent with the launch, and only
 * from here: taken from the statement drawn when the person presses, kept with the operation so a resend sends the same, and never
 * worked out from the saved crew. A comparison whose statement cannot be drawn is not launched from here. Beside the lines, the sessions
 * this launch starts on each login (launchAccounts, CP1 PR-9): the first tasks' (a comparison's candidates), then up to its reviews'.
 */
const props = defineProps<{
  revision: PlanSummary | null
  body: PlanBody | null
  ticket: DispatchTicket
  plans: TicketPlans
}>()
const open = defineModel<boolean>('open', { default: false })

const label = ref('')
const sending = ref(false)
const outcome = ref<LaunchOutcome | null>(null)
const error = ref('')
/**
 * The operation this launch is, the revision it launches from and the label it was first sent with: kept while it may still be the same
 * launch (being sent, pending, a reply lost), across closes, so it is sent again unchanged; none once it is settled (started, failed or
 * refused), and the next press is another launch.
 */
const op = ref<{ id: string; plan: string; label: string; candidates?: number } | null>(null)

watch(open, (o) => {
  if (!o) return
  // A launch not settled from this revision: shown as it stands, and the next press sends it again.
  if (op.value && op.value.plan === props.revision?.id) return
  label.value = ''
  outcome.value = null
  error.value = ''
  op.value = null
})

const yolo = computed(() => (props.body ? yoloWords(props.body.crew.yolo) : null))

// The revision's members' lines: the catalog names their agents, the accounts their labels; both read as the dialog opens.
const accounts = useAccounts()
const sessions = useSessions()
const agents = ref<AgentInfo[]>([])
watch(
  open,
  (o) => {
    if (!o) return
    void accounts.store.load()
    sessions.catalog().then(
      (a) => (agents.value = a),
      () => {},
    )
  },
  { immediate: true },
)
const cap = useCapacity()
const lines = computed(() => (props.body ? launchLines(props.body.crew.members ?? [], { agents: agents.value, doc: accounts.doc.value, managed: true, yolo: !!props.body.crew.yolo, view: (q) => cap.entry(q).view }) : []))
/** The sessions this launch starts on each login, each member's login as its line resolves it (memberLogin: never a default of the page's). */
const perAccount = computed(() => {
  const crew = props.body?.crew
  if (!crew) return []
  const members = crew.members ?? []
  return launchAccounts({
    members,
    tasks: crew.tasks,
    compare: crew.compare,
    accountOf: (name) => {
      const m = members.find((x) => x.name === name) ?? { name, agentId: '' }
      const l = lines.value.find((x) => x.name === name)
      return memberLogin(m, { doc: accounts.doc.value, view: l ? cap.entry(l.query).view : null, agent: l?.agent ?? m.agentId })
    },
  })
})
const bodyError = computed(() => (props.revision && !props.body ? (props.plans.bodyErrors.get(props.revision.id) ?? '') : ''))
function readBodyAgain() {
  if (props.revision) void props.plans.readBody(props.revision.id).catch(() => {})
}
/** What this launch is: ordinary, a comparison with its count and its statement, or a comparison not to launch yet (with why). */
const comparison = computed(() => (props.revision && props.body ? compareLaunch(props.revision, props.body, agents.value) : null))
/** Launch is held before the body is read, and for a comparison whose statement is not drawn; a launch on its way is sent again as it was. */
const held = computed(() => !props.body || (!op.value && comparison.value?.kind !== 'ordinary' && comparison.value?.kind !== 'compare'))
const runPath = computed(() => (outcome.value?.state === 'started' ? `/runs/${encodeURIComponent(outcome.value.launch.runId)}` : ''))

async function send() {
  const p = props.revision
  if (!p || !props.body || sending.value || held.value) return
  // No launch on its way: this press is another launch, and what came of the one before is behind us. A comparison's count is the one
  // its statement shows now, kept with the operation.
  if (!op.value) {
    const c = comparison.value
    op.value = { id: props.plans.mintOpId(), plan: p.id, label: label.value, ...(c?.kind === 'compare' ? { candidates: c.count } : {}) }
    outcome.value = null
  }
  const sent = op.value
  sending.value = true
  error.value = ''
  try {
    const out = await props.plans.launch(p, sent.label, sent.id, sent.candidates)
    // Another launch asked for since (the dialog opened on another revision): this one's outcome is not what the dialog shows.
    if (op.value !== sent) return
    outcome.value = out
    if (out.state !== 'pending') op.value = null
  } catch (e) {
    // Both tries lost to the network: the launch may have been made, and the same operation sent again answers it.
    if (op.value === sent) error.value = `${(e as Error).message}: send it again; it is the same launch`
  } finally {
    sending.value = false
  }
}
</script>

<template>
  <UModal v-model:open="open" :title="revision ? `Launch from ${revisionTag(revision.n)}` : 'Launch'" :ui="{ content: 'max-w-lg' }">
    <template #body>
      <div v-if="revision" class="flex flex-col gap-3 text-sm" data-launch-dialog :data-launch-revision="revision.n" :data-launch-state="outcome?.state ?? 'asking'">
        <template v-if="outcome?.state !== 'started'">
          <p class="text-muted">
            A managed run of the crew {{ revision.crewName }}, as revision {{ revision.n }} holds it, from commit
            <code class="font-mono text-xs" :title="revision.base" data-launch-base>{{ shortCommit(revision.base) }}</code>. Every attempt is handed the ticket as it
            stands now; an edit of the ticket after the launch never changes the run.
          </p>
          <p v-if="body && yolo" class="font-medium" :class="yolo.tone === 'warning' ? 'text-warning' : 'text-muted'" :data-launch-yolo="body.crew.yolo ? 'on' : 'off'">{{ yolo.label }}. {{ yolo.words }}</p>
          <p v-else-if="bodyError" class="flex flex-wrap items-center gap-2 text-warning" data-launch-body-error>
            <span>Revision {{ revision.n }} could not be read, so its yolo choice is not known: {{ bodyError }}</span>
            <UButton label="Read it again" size="xs" color="neutral" variant="outline" data-launch-body-retry @click="readBodyAgain" />
          </p>
          <p v-else class="text-dimmed" data-launch-body-reading>Reading revision {{ revision.n }}: its yolo choice is said before anything launches…</p>
          <p v-if="comparison?.kind === 'compare'" class="text-default" data-launch-compare :data-launch-candidates="comparison.count">
            <template v-for="(part, i) in comparison.statement" :key="part.key"><span :data-launch-compare-part="part.key">{{ part.words }}</span>{{ i < comparison.statement.length - 1 ? '; ' : '.' }}</template>
          </p>
          <p v-else-if="comparison?.kind === 'unstated'" class="flex flex-wrap items-center gap-2 text-warning" data-launch-compare-unstated>
            <span>{{ comparison.words }}</span>
            <UButton label="Read it again" size="xs" color="neutral" variant="outline" data-launch-compare-retry @click="plans.read().catch(() => {})" />
          </p>
          <ul v-if="perAccount.length" class="flex flex-col gap-0.5 text-xs text-muted" data-launch-accounts>
            <li v-for="a in perAccount" :key="a.key" :data-launch-account="a.key" :data-launch-account-first="a.first" :data-launch-account-later="a.later">{{ accountCountWords(a) }}</li>
          </ul>
          <ul v-if="lines.length" class="flex flex-col gap-2" data-launch-members>
            <li v-for="l in lines" :key="l.name" class="flex flex-col gap-1" :data-launch-member="l.name">
              <div class="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
                <span class="font-mono font-medium text-highlighted">{{ l.name }}</span>
                <span class="text-muted">{{ l.agent }}</span>
                <span class="text-xs text-muted">Model: <span class="text-default" data-launch-member-model>{{ l.model }}</span></span>
                <span class="text-xs text-muted">Account: <span class="text-default" data-launch-member-account>{{ l.account }}</span></span>
              </div>
              <CapacityPanel :query="open ? l.query : null" compact />
            </li>
          </ul>
          <UFormField label="The run's label" :hint="op ? 'kept as it was sent, until this launch is settled' : `at most ${PLAN_BOUNDS.label} characters`" help="A run left unnamed is named after the ticket." size="sm">
            <UInput :model-value="op ? op.label : label" :maxlength="PLAN_BOUNDS.label" placeholder="A name for the run, if you want one" :disabled="!!op" class="w-full" data-launch-label @update:model-value="(v) => (label = String(v ?? ''))" />
          </UFormField>
        </template>

        <UAlert v-if="outcome?.state === 'started'" color="success" variant="subtle" icon="i-lucide-play" :title="outcome.repeat ? 'Launched already' : 'Launched'" data-launch-started>
          <template #description>
            <span class="flex flex-col gap-1">
              <span>The run {{ outcome.launch.label }} started from {{ revisionTag(outcome.launch.planN || revision.n) }}.</span>
              <NuxtLink :to="runPath" class="font-mono text-xs underline" :data-launch-run-link="outcome.launch.runId" @click="open = false">{{ outcome.launch.runId }}</NuxtLink>
            </span>
          </template>
        </UAlert>
        <UAlert v-else-if="outcome?.state === 'failed'" color="error" variant="subtle" icon="i-lucide-circle-x" title="The launch failed" :description="outcome.words" data-launch-failed />
        <UAlert v-else-if="outcome?.state === 'pending'" color="neutral" variant="subtle" icon="i-lucide-hourglass" title="Still being started" :description="outcome.words" data-launch-pending />
        <UAlert v-else-if="outcome?.state === 'refused'" color="warning" variant="subtle" icon="i-lucide-triangle-alert" title="Not launched" :description="outcome.words" data-launch-refused />
        <UAlert v-if="error" color="error" variant="subtle" icon="i-lucide-wifi-off" title="No answer" :description="error" data-launch-error />

        <div class="flex justify-end gap-2">
          <template v-if="outcome?.state === 'started'">
            <UButton label="Close" color="neutral" variant="ghost" @click="open = false" />
            <UButton label="Open the run" icon="i-lucide-arrow-right" :to="runPath" data-launch-open-run @click="open = false" />
          </template>
          <template v-else>
            <UButton label="Cancel" color="neutral" variant="ghost" :disabled="sending" @click="open = false" />
            <UButton
              :label="launchButtonWords(comparison, outcome?.state === 'pending' || error ? 'pending' : outcome?.state === 'failed' ? 'failed' : null)"
              icon="i-lucide-play"
              :loading="sending"
              :disabled="held"
              :data-launch-candidates="op?.candidates ?? (comparison?.kind === 'compare' ? comparison.count : undefined)"
              data-launch-confirm
              @click="send"
            />
          </template>
        </div>
      </div>
    </template>
  </UModal>
</template>
