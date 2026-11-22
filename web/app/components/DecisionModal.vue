<script setup lang="ts">
import { inboxKindWords } from '~/utils/inbox'
/**
 * The app's one decision card in a modal (Program item DR1, docs/plans/decisions.md, "web/app"): the card (DecisionCard) and its answer
 * (DecisionRespond) of the decision useDecisionModal names, opened from the run page's Answer, the session's question bar in the
 * session page and the Yard, Needs you, the palette's rows and the ticket page; titled "A decision for you", its id in the card's
 * Details (UX1 PR-20). Mounted once, in the layout; it starts the app's store
 * of the open and answered decisions (useDecisions) that the counts and the palette read.
 */
const modal = useDecisionModal()
const decisions = useDecisions()
const record = useDecisionRecord(() => modal.id.value)
/** Who reads the page: its own responses on a record are judged by the read with it (SW1 PR-9). */
const operatorSubject = useOperatorSubject()
const now = ref(Date.now())
let tick: number | undefined
onMounted(() => {
  decisions.start()
  tick = window.setInterval(() => (now.value = Date.now()), 5000)
})
onBeforeUnmount(() => window.clearInterval(tick))
/** Whether DecisionRespond draws its buttons: the card then leaves out its option rows (UX1 PR-20). */
const controls = ref(false)

const open = computed({
  get: () => !!modal.id.value,
  set: (v: boolean) => {
    if (!v) modal.close()
  },
})
</script>

<template>
  <UModal v-model:open="open" :title="inboxKindWords('decision')" description="Answered by the operator; Yardmaster applies the answer." :ui="{ content: 'sm:max-w-2xl' }">
    <template #body>
      <div class="flex flex-col gap-4" data-decision-modal :data-decision-modal-id="modal.id.value">
        <UAlert v-if="record.error.value" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="record.error.value" data-decision-modal-error>
          <template #actions><UButton label="Read again" size="xs" color="neutral" variant="outline" @click="record.read()" /></template>
        </UAlert>
        <p v-if="!record.decision.value && !record.error.value" class="text-sm text-muted">Reading {{ modal.id.value }}…</p>
        <template v-if="record.decision.value">
          <DecisionCard :decision="record.decision.value" :refs="record.refs.value" :now="now" :controls="controls" captures :policy-now="record.policyNow.value" :quorum="record.quorum.value" />
          <DecisionRespond :decision="record.decision.value" :begin="record.begin" :policy-now="record.policyNow.value" :quorum="record.quorum.value" :subject="operatorSubject" :router="record.router.value" @read="record.read()" @controls="controls = $event" />
        </template>
      </div>
    </template>
  </UModal>
</template>
