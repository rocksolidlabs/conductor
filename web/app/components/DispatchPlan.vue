<script setup lang="ts">
import type { DispatchTicket, PlanSummary } from '~/composables/useDispatch'
import type { AgentInfo } from '~/composables/useSessions'
import type { TicketPlans } from '~/composables/useTicketPlans'
import { approvable, launchBlockers, launchStateWords, planReasons, planRemedy, planStateWord, revisionTag, shortCommit } from '~/utils/dispatchPlans'
import { relativeTime } from '~/utils/sessions'

/**
 * A ticket's Plan section (Program item DS2, docs/plans/dispatch-evidence.md, "web/app"), after its acceptance criteria: the approved plan
 * revision in full (DispatchPlanBody: the yolo choice said plainly, the commit, the members with their agents, arguments and commands as
 * resolved, the tasks with who does each, the checks expected, the references, what is handed over, the decisions, recorded and not held
 * yet), else the newest proposed one; when its approval no longer holds, why, with the one remedy that cures it (Approve again after an
 * edit of the title, the description or the criteria; Revise after a comment handed over changed or an agent resolves otherwise); Launch
 * from it, asked first, or why Launch is not offered; the launches the ticket's history names, newest first, each with its run's link;
 * the other revisions folded below, newest first. Propose, Revise, Approve and Withdraw; every one an operation of the page. A ticket's
 * runs with their tasks, candidates and checks are not here yet: each run's page has them. One column on a phone.
 */
const props = defineProps<{
  ticket: DispatchTicket
  plans: TicketPlans
  project: string
  archived: boolean
  locked: boolean
  agents: readonly AgentInfo[]
  now: number
}>()

const toast = useToast()

const featured = computed(() => props.plans.featured.value)
const others = computed(() => props.plans.plans.value.filter((p) => p.id !== featured.value?.id))
const blockers = computed(() =>
  launchBlockers(props.plans.approved.value, { ticket: props.ticket, project: props.project, archived: props.archived, server: props.plans.server }),
)
const launchCount = computed(() => props.ticket.launchCount ?? props.plans.known.value.length)

/** The other revisions opened, their bodies read as they open. */
const opened = reactive(new Set<string>())
function toggle(p: PlanSummary) {
  if (opened.has(p.id)) {
    opened.delete(p.id)
    return
  }
  opened.add(p.id)
  void props.plans.readBody(p.id).catch(() => {})
}

// The dialogs, each on the revision it acts on.
const proposeOpen = ref(false)
const proposeFrom = ref<PlanSummary | null>(null)
const approveOpen = ref(false)
const launchOpen = ref(false)
const acting = ref<PlanSummary | null>(null)

/** Propose a revision; from `p` when revising it, its body read first so the dialog is prefilled. */
async function propose(p: PlanSummary | null = null) {
  if (props.locked) return
  proposeFrom.value = p
  if (p) await props.plans.readBody(p.id).catch(() => {})
  proposeOpen.value = true
}
function approve(p: PlanSummary) {
  if (props.locked) return
  acting.value = p
  void props.plans.readBody(p.id).catch(() => {})
  approveOpen.value = true
}
/** Launch, asked first, from the approved revision (or the one named, when it is it); says why not when Launch is not offered. */
function launch(p: PlanSummary | null = props.plans.approved.value): boolean {
  if (!p || p.id !== props.plans.approved.value?.id || blockers.value.length) {
    toast.add({ title: `${props.ticket.key}: not launched`, description: blockers.value[0] ?? 'Only the approved revision launches', icon: 'i-lucide-triangle-alert', color: 'warning' })
    return false
  }
  acting.value = p
  void props.plans.readBody(p.id).catch(() => {})
  launchOpen.value = true
  return true
}
const withdrawing = ref('')
async function withdraw(p: PlanSummary) {
  if (props.locked || withdrawing.value) return
  withdrawing.value = p.id
  try {
    await props.plans.withdraw(p)
  } finally {
    withdrawing.value = ''
  }
}

const proposeFromBody = computed(() => (proposeFrom.value ? { revision: proposeFrom.value, body: props.plans.bodies.get(proposeFrom.value.id) ?? null } : null))
const actingBody = computed(() => (acting.value ? (props.plans.bodies.get(acting.value.id) ?? null) : null))
/** The revision a dialog acts on, as the section shows it now (its state and `rev` may have moved since the dialog opened). */
const actingNow = computed(() => (acting.value ? (props.plans.plans.value.find((p) => p.id === acting.value!.id) ?? acting.value) : null))

function runOf(runId: string) {
  return `/runs/${encodeURIComponent(runId)}`
}
function planN(planId: string): number | undefined {
  return props.plans.plans.value.find((p) => p.id === planId)?.n
}

defineExpose({ propose, approve, launch })
</script>

<template>
  <section class="flex flex-col gap-3" data-plan-section :data-plan-count="plans.plans.value.length" :data-plan-approved="plans.approved.value?.n ?? ''">
    <div class="flex items-center gap-2">
      <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">Plan</h3>
      <div class="flex-1" />
      <UButton v-if="!locked && !plans.plans.value.length" label="Propose a plan" icon="i-lucide-file-pen-line" size="xs" color="neutral" variant="ghost" data-plan-propose @click="propose()" />
      <UButton v-else-if="!locked" label="New revision" icon="i-lucide-file-plus" size="xs" color="neutral" variant="ghost" data-plan-propose @click="propose()" />
    </div>

    <p v-if="!plans.plans.value.length && plans.listed.value" class="text-sm text-dimmed" data-plan-empty>
      No plan revision yet. A revision takes a saved crew with tasks as it is now, the commit it starts from and what every attempt is
      handed; approved, it is what a launch from this ticket runs.
    </p>
    <p v-else-if="!plans.listed.value && !plans.plans.value.length" class="text-sm text-dimmed">Reading the plan…</p>

    <div v-if="featured" class="flex flex-col gap-3 rounded-lg p-4 ring ring-default" data-plan-featured :data-plan-revision="featured.n" :data-plan-state="featured.state" :data-plan-stale="featured.stale?.remedy ?? undefined">
      <div class="flex min-w-0 flex-wrap items-center gap-2">
        <UBadge :label="planStateWord(featured).label" :color="planStateWord(featured).color" :icon="planStateWord(featured).icon" variant="subtle" size="sm" data-plan-state-badge />
        <span class="font-medium text-highlighted">Revision {{ featured.n }}</span>
        <span class="font-mono text-xs text-dimmed">{{ revisionTag(featured.n) }}</span>
        <span class="min-w-0 truncate text-muted">{{ featured.crewName }}</span>
        <span class="text-xs text-dimmed">proposed by {{ featured.createdBy || 'admin' }} {{ relativeTime(featured.createdAt, now, { suffix: true }) }}</span>
      </div>
      <p v-if="featured.approvedAt" class="text-xs text-muted" data-plan-approval>
        Approved by {{ featured.approvedBy || 'admin' }} {{ relativeTime(featured.approvedAt, now, { suffix: true }) }}, at the ticket's revision {{ featured.approvedRev }}<template v-if="featured.approvedNote">: <span class="whitespace-pre-wrap text-default" data-plan-approval-note>{{ featured.approvedNote }}</span></template>
      </p>

      <UAlert
        v-for="r in planReasons(featured)"
        :key="r.kind"
        :color="r.blocks ? 'warning' : 'neutral'"
        variant="subtle"
        :icon="r.blocks ? 'i-lucide-triangle-alert' : 'i-lucide-info'"
        :title="r.kind === 'stale' ? 'The approval no longer holds' : r.kind === 'agents' ? 'An agent changed' : 'The crew changed'"
        :description="r.words"
        :data-plan-reason="r.kind"
        :data-plan-remedy="r.remedy"
      />
      <div v-if="!locked && planRemedy(featured)" class="flex flex-wrap gap-2">
        <UButton v-if="planRemedy(featured) === 'approve'" label="Approve again" icon="i-lucide-badge-check" size="sm" data-plan-approve-again @click="approve(featured)" />
        <UButton label="Revise" icon="i-lucide-file-pen-line" size="sm" :color="planRemedy(featured) === 'revise' ? 'primary' : 'neutral'" :variant="planRemedy(featured) === 'revise' ? 'solid' : 'outline'" data-plan-revise @click="propose(featured)" />
      </div>

      <DispatchPlanBody v-if="plans.bodies.get(featured.id)" :body="plans.bodies.get(featured.id)!" :ticket="ticket" :agents="agents" />
      <p v-else-if="plans.bodyErrors.get(featured.id)" class="text-sm text-warning">The revision could not be read: {{ plans.bodyErrors.get(featured.id) }}</p>
      <p v-else class="text-sm text-dimmed">Reading revision {{ featured.n }}…</p>

      <div v-if="!locked" class="flex flex-wrap items-center gap-2">
        <UButton
          v-if="featured.state === 'approved'"
          :label="`Launch from ${revisionTag(featured.n)}`"
          icon="i-lucide-play"
          size="sm"
          :disabled="!!blockers.length"
          :title="blockers[0]"
          data-plan-launch
          @click="launch(featured)"
        />
        <UButton v-if="featured.state !== 'approved' && approvable(featured)" label="Approve" icon="i-lucide-badge-check" size="sm" data-plan-approve @click="approve(featured)" />
        <UButton v-if="!planRemedy(featured)" label="Revise" icon="i-lucide-file-pen-line" size="sm" color="neutral" variant="outline" data-plan-revise @click="propose(featured)" />
        <div class="flex-1" />
        <UButton label="Withdraw" icon="i-lucide-ban" size="sm" color="neutral" variant="ghost" :loading="withdrawing === featured.id" data-plan-withdraw @click="withdraw(featured)" />
      </div>
      <ul v-if="featured.state === 'approved' && blockers.length" class="flex flex-col gap-0.5 text-xs text-muted" data-plan-blockers>
        <li v-for="b in blockers" :key="b" class="flex items-start gap-1.5"><UIcon name="i-lucide-circle-slash" class="mt-0.5 size-3 flex-none" /><span>{{ b }}</span></li>
      </ul>
    </div>

    <div v-if="plans.known.value.length || launchCount" class="flex flex-col gap-1.5" data-plan-launches :data-count="launchCount">
      <h4 class="text-xs font-semibold text-muted">Launches</h4>
      <ul class="flex flex-col gap-1">
        <li
          v-for="l in plans.shown.value"
          :key="l.id"
          class="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 text-sm"
          :data-plan-launch-row="l.id"
          :data-launch-run="l.runId"
          :data-launch-state="plans.launches.get(l.id)?.state ?? 'reading'"
        >
          <UIcon name="i-lucide-play" class="size-3.5 flex-none text-muted" />
          <NuxtLink v-if="plans.launches.get(l.id)?.state === 'started'" :to="runOf(l.runId)" class="min-w-0 truncate font-medium text-highlighted hover:underline" data-plan-launch-run>{{ l.label || l.runId }}</NuxtLink>
          <span v-else class="min-w-0 truncate">{{ l.label || l.runId }}</span>
          <span class="font-mono text-xs text-dimmed">{{ planN(l.planId) ? revisionTag(planN(l.planId)!) : '' }}</span>
          <UBadge v-if="plans.launches.get(l.id)" :label="launchStateWords(plans.launches.get(l.id)!).label" :color="launchStateWords(plans.launches.get(l.id)!).color" variant="subtle" size="sm" />
          <span class="text-xs text-dimmed">by {{ l.createdBy || 'admin' }} {{ relativeTime(l.createdAt, now, { suffix: true }) }}</span>
          <span v-if="plans.launches.get(l.id)?.state === 'failed'" class="w-full text-xs text-error" data-plan-launch-why>{{ plans.launches.get(l.id)!.why }}</span>
        </li>
      </ul>
      <p v-if="launchCount > plans.shown.value.length" class="text-xs text-dimmed">{{ launchCount }} launches in all; the newest {{ plans.shown.value.length }} the history read so far names are shown.</p>
      <p class="text-xs text-dimmed">A ticket's runs with their tasks, candidates and checks are not shown here yet: each run's page has them.</p>
    </div>

    <div v-if="others.length" class="flex flex-col gap-1.5" data-plan-others>
      <h4 class="text-xs font-semibold text-muted">Other revisions</h4>
      <ul class="flex flex-col gap-1.5">
        <li v-for="p in others" :key="p.id" class="flex flex-col gap-2 rounded-md p-2 ring ring-default" :data-plan-revision="p.n" :data-plan-state="p.state">
          <div class="flex min-w-0 flex-wrap items-center gap-2">
            <UButton :icon="opened.has(p.id) ? 'i-lucide-chevron-down' : 'i-lucide-chevron-right'" size="xs" color="neutral" variant="ghost" :aria-label="`${opened.has(p.id) ? 'Fold' : 'Open'} revision ${p.n}`" :aria-expanded="opened.has(p.id)" data-plan-unfold @click="toggle(p)" />
            <span class="font-medium">Revision {{ p.n }}</span>
            <UBadge :label="planStateWord(p).label" :color="planStateWord(p).color" variant="subtle" size="sm" />
            <span class="min-w-0 truncate text-muted">{{ p.crewName }}</span>
            <code class="font-mono text-xs text-dimmed" :title="p.base">{{ shortCommit(p.base) }}</code>
            <span class="text-xs text-dimmed">{{ relativeTime(p.createdAt, now, { suffix: true }) }}</span>
            <div class="flex-1" />
            <template v-if="!locked">
              <UButton v-if="approvable(p)" label="Approve" icon="i-lucide-badge-check" size="xs" color="neutral" variant="ghost" data-plan-approve @click="approve(p)" />
              <UButton label="Revise" icon="i-lucide-file-pen-line" size="xs" color="neutral" variant="ghost" data-plan-revise @click="propose(p)" />
              <UButton v-if="p.state !== 'withdrawn'" icon="i-lucide-ban" size="xs" color="neutral" variant="ghost" :aria-label="`Withdraw revision ${p.n}`" :loading="withdrawing === p.id" data-plan-withdraw @click="withdraw(p)" />
            </template>
          </div>
          <template v-if="opened.has(p.id)">
            <p v-for="r in planReasons(p)" :key="r.kind" class="text-xs text-muted">{{ r.words }}</p>
            <DispatchPlanBody v-if="plans.bodies.get(p.id)" :body="plans.bodies.get(p.id)!" :ticket="ticket" :agents="agents" />
            <p v-else-if="plans.bodyErrors.get(p.id)" class="text-sm text-warning">The revision could not be read: {{ plans.bodyErrors.get(p.id) }}</p>
            <p v-else class="text-sm text-dimmed">Reading revision {{ p.n }}…</p>
          </template>
        </li>
      </ul>
    </div>

    <DispatchPlanPropose v-model:open="proposeOpen" :ticket="ticket" :plans="plans" :from="proposeFromBody" />
    <DispatchPlanApprove v-model:open="approveOpen" :revision="actingNow" :body="actingBody" :ticket="ticket" :plans="plans" />
    <DispatchLaunch v-model:open="launchOpen" :revision="actingNow" :body="actingBody" :ticket="ticket" :plans="plans" />
  </section>
</template>
