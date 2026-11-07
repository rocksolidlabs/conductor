<script setup lang="ts">
/**
 * The app's one decision card in a modal (Program item DR1, docs/plans/decisions.md, "web/app"): the card (DecisionCard) and its answer
 * (DecisionRespond) of the decision useDecisionModal names, opened from the run page's Answer, the session's question bar in the
 * session page and the Yard, Needs you, the palette's rows and the ticket page. Mounted once, in the layout; it starts the app's store
 * of the open and answered decisions (useDecisions) that the counts and the palette read.
 */
const modal = useDecisionModal()
const decisions = useDecisions()
const record = useDecisionRecord(() => modal.id.value)
const now = ref(Date.now())
let tick: number | undefined
onMounted(() => {
  decisions.start()
  tick = window.setInterval(() => (now.value = Date.now()), 5000)
})
onBeforeUnmount(() => window.clearInterval(tick))

const open = computed({
  get: () => !!modal.id.value,
  set: (v: boolean) => {
    if (!v) modal.close()
  },
})
</script>

<template>
  <UModal v-model:open="open" :title="`Decision ${modal.id.value}`" description="Answered by the operator; Yardmaster applies the answer." :ui="{ content: 'sm:max-w-2xl' }">
    <template #body>
      <div class="flex flex-col gap-4" data-decision-modal :data-decision-modal-id="modal.id.value">
        <UAlert v-if="record.error.value" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="record.error.value" data-decision-modal-error>
          <template #actions><UButton label="Read again" size="xs" color="neutral" variant="outline" @click="record.read()" /></template>
        </UAlert>
        <p v-if="!record.decision.value && !record.error.value" class="text-sm text-muted">Reading {{ modal.id.value }}…</p>
        <template v-if="record.decision.value">
          <DecisionCard :decision="record.decision.value" :refs="record.refs.value" :now="now" />
          <DecisionRespond :decision="record.decision.value" :begin="record.begin" @read="record.read()" />
        </template>
      </div>
    </template>
  </UModal>
</template>
