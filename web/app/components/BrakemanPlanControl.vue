<script setup lang="ts">
import type { DispatchTicket } from '~/composables/useDispatch'
import { ticketWatch } from '~/composables/useDispatchLive'
import { planCardControl } from '~/utils/brakemanCards'
import { ticketPath } from '~/utils/dispatch'
import { revisionTag } from '~/utils/dispatchPlans'

/**
 * A decision card's control for a plan revision (docs/plans/brakeman.md, PR-11): DS2's own Approve, rendered from the revision's live
 * record as the ticket page reads it (useTicketPlans: the ticket, its revisions, the revision's body), never from the brief's fields, and
 * offered by DS2's own rule. Pressed, it opens DS2's own approval dialog (DispatchPlanApprove), which says what the approval covers (the
 * crew, its commit, its yolo choice, the ticket as it stands) before the operator approves it, through DS2's own route at the ticket's
 * revision and the revision's own: the person approves as DS2 requires of the operator. Brakeman never approves, granted or not. While
 * the card is drawn it is read again as the ticket page is, on Dispatch's events (ticketWatch: its ticket at once, its project after the
 * window) and on a reconnect's snapshot, which first forgets what the gate and the plans knew; a read that fails is asked again by the
 * live store. So an approval, a withdrawal or a ticket edit made elsewhere while the brief is open is what the card shows.
 */
const props = defineProps<{ ticketKey: string; n: number }>()

const ds = useDispatch()
const ticket = ref<DispatchTicket | null>(null)
const plans = useTicketPlans({ gate: ds.gate, key: () => props.ticketKey, ticket: () => ticket.value })
const error = ref('')
const read = ref(false)

/**
 * The ticket and its revisions read again, as the ticket page reads them, through the page's ordering gate: rejects when the read failed
 * (the live store asks it again), its words shown meanwhile.
 */
async function reload() {
  try {
    await ds.read(`ticket:${props.ticketKey}`, () => ds.api.ticket(props.ticketKey), (t) => (ticket.value = t), (t) => t.rev)
    await plans.read()
    error.value = ''
    const r = revision.value
    if (r) await plans.readBody(r.id).catch(() => {})
  } catch (e) {
    error.value = (e as Error).message
    throw e
  } finally {
    read.value = true
  }
}
const project = () => props.ticketKey.slice(0, props.ticketKey.lastIndexOf('-'))
// The first read is made once the card is subscribed to the events (useDispatchLive's `first`), so none falls between them.
const live = useDispatchLive(
  {
    ...ticketWatch(() => ticket.value?.id ?? '', project, reload),
    reset: () => {
      ds.gate.reset()
      plans.reset()
    },
  },
  () => live.ask(reload),
)

const revision = computed(() => plans.plans.value.find((p) => p.n === props.n) ?? null)
const control = computed(() => (read.value ? planCardControl(revision.value, props.ticketKey, props.n) : null))
const body = computed(() => (revision.value ? (plans.bodies.get(revision.value.id) ?? null) : null))
const approving = ref(false)
watch(approving, (open, was) => {
  // Closed after an approval or not: what Dispatch holds now is what the card shows.
  if (was && !open) live.ask(reload)
})
</script>

<template>
  <div class="flex flex-col gap-1.5" data-plan-control :data-plan-revision="n" :data-plan-state="revision?.state ?? ''">
    <p v-if="!control" class="text-xs text-muted">Reading {{ ticketKey }} {{ revisionTag(n) }} as Dispatch holds it…</p>
    <template v-else>
      <p class="text-xs text-muted" data-plan-control-words>{{ control.words }}</p>
      <div class="flex flex-wrap items-center gap-1.5">
        <UButton v-if="control.approve && ticket" :label="control.approve" icon="i-lucide-badge-check" size="sm" data-card-approve @click="approving = true" />
        <UButton :to="ticketPath(ticketKey)" :label="`Open ${ticketKey}`" icon="i-lucide-ticket" size="sm" color="neutral" variant="ghost" data-decision-ticket />
      </div>
    </template>
    <UAlert v-if="error" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="error" data-plan-control-error />
    <DispatchPlanApprove v-if="ticket" v-model:open="approving" :revision="revision" :body="body" :ticket="ticket" :plans="plans" />
  </div>
</template>
