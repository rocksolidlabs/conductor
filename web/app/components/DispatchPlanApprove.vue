<script setup lang="ts">
import type { DispatchTicket, PlanBody, PlanSummary } from '~/composables/useDispatch'
import type { TicketPlans } from '~/composables/useTicketPlans'
import { PLAN_BOUNDS, revisionTag, shortCommit, yoloWords } from '~/utils/dispatchPlans'

/**
 * Approving a plan revision (Program item DS2, docs/plans/dispatch-evidence.md, "web/app"): the operator's act, with a note, the
 * rationale, kept beside the approval. The dialog says what the approval covers before it is made: the revision's crew, its commit and its
 * yolo choice, and the ticket's title, description and criteria as they stand now, an edit of which makes it stale until it is approved
 * again. It supersedes the ticket's approval before it. A refusal is said in place, in the store's words.
 */
const props = defineProps<{
  revision: PlanSummary | null
  body: PlanBody | null
  ticket: DispatchTicket
  plans: TicketPlans
}>()
const open = defineModel<boolean>('open', { default: false })

const note = ref('')
const error = ref('')
const sending = ref(false)
watch(open, (o) => {
  if (o) {
    note.value = ''
    error.value = ''
  }
})

const yolo = computed(() => yoloWords(props.body?.crew.yolo))
const other = computed(() => props.plans.approved.value && props.plans.approved.value.id !== props.revision?.id ? props.plans.approved.value : null)
const again = computed(() => props.revision?.state === 'approved')

async function submit() {
  const p = props.revision
  if (!p || sending.value) return
  sending.value = true
  error.value = ''
  try {
    await props.plans.approve(p, note.value)
    open.value = false
  } catch (e) {
    error.value = props.plans.failureWords(e)
  } finally {
    sending.value = false
  }
}
</script>

<template>
  <UModal v-model:open="open" :title="revision ? `${again ? 'Approve again' : 'Approve'} revision ${revision.n}` : 'Approve'" :ui="{ content: 'max-w-lg' }">
    <template #body>
      <form v-if="revision" class="flex flex-col gap-3 text-sm" data-plan-approve-dialog :data-plan-revision="revision.n" @submit.prevent="submit">
        <p class="text-muted">
          Approved, {{ revisionTag(revision.n) }} is what a launch from {{ ticket.key }} runs: the crew {{ revision.crewName }} from commit
          <code class="font-mono text-xs" :title="revision.base">{{ shortCommit(revision.base) }}</code>.
          <template v-if="other">It supersedes the approval of revision {{ other.n }}.</template>
        </p>
        <p v-if="body" class="font-medium" :class="yolo.tone === 'warning' ? 'text-warning' : 'text-muted'" data-plan-approve-yolo>{{ yolo.label }}. {{ yolo.words }}</p>
        <p class="text-xs text-muted">
          The approval covers the ticket's title, description and acceptance criteria as they are now (its revision {{ ticket.rev }}): an edit of
          any of them makes it stale, and nothing launches until it is approved again.
        </p>
        <UFormField label="Note" hint="the rationale, kept with the approval" size="sm">
          <UTextarea v-model="note" :rows="3" autoresize :maxlength="PLAN_BOUNDS.approvalNote" placeholder="Why this revision…" class="w-full" data-plan-approve-note />
        </UFormField>
        <UAlert v-if="error" color="warning" variant="subtle" icon="i-lucide-triangle-alert" title="Not approved" :description="error" data-plan-approve-error />
        <div class="flex justify-end gap-2">
          <UButton label="Cancel" color="neutral" variant="ghost" :disabled="sending" @click="open = false" />
          <UButton type="submit" :label="again ? 'Approve again' : 'Approve'" icon="i-lucide-badge-check" :loading="sending" data-plan-approve-submit />
        </div>
      </form>
    </template>
  </UModal>
</template>
