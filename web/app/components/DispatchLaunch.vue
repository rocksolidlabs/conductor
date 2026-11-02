<script setup lang="ts">
import type { DispatchTicket, PlanBody, PlanSummary } from '~/composables/useDispatch'
import type { TicketPlans } from '~/composables/useTicketPlans'
import { PLAN_BOUNDS, defaultLabel, revisionTag, shortCommit, yoloWords, type LaunchOutcome } from '~/utils/dispatchPlans'

/**
 * Launching a managed run from a ticket's approved plan revision (Program item DS2, docs/plans/dispatch-evidence.md, "web/app"). It asks
 * first: the revision, the commit the run starts from, the yolo choice said plainly, and the run's label (the ticket's key and title when
 * none is given). Then it says what came of it: the run started, with its link; the launch failed, with why; still being started, sent
 * again under the same operation (never a second run); or refused before any run, in the server's words. The operation's id is minted
 * once per launch asked for, with the label it is first sent with, and both are kept until it is settled: a reply lost to the network, or
 * a pending launch sent again, is the same launch, sent unchanged (the label is not edited meanwhile; another would be a conflict, not the
 * launch). Nothing is launched before the revision's body is read: the yolo choice is said from it, never assumed.
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
 * The operation this launch is and the label it was first sent with: kept while it may still be the same launch (pending, a reply lost),
 * so it is sent again unchanged; none once it is settled (started, failed or refused), and the next press is another launch.
 */
const op = ref<{ id: string; label: string } | null>(null)

watch(open, (o) => {
  if (!o) return
  label.value = ''
  outcome.value = null
  error.value = ''
  op.value = null
})

const yolo = computed(() => (props.body ? yoloWords(props.body.crew.yolo) : null))
const bodyError = computed(() => (props.revision && !props.body ? (props.plans.bodyErrors.get(props.revision.id) ?? '') : ''))
function readBodyAgain() {
  if (props.revision) void props.plans.readBody(props.revision.id).catch(() => {})
}
const placeholder = computed(() => defaultLabel(props.ticket.key, props.ticket.title))
const runPath = computed(() => (outcome.value?.state === 'started' ? `/runs/${encodeURIComponent(outcome.value.launch.runId)}` : ''))

async function send() {
  const p = props.revision
  if (!p || !props.body || sending.value) return
  // No launch on its way: this press is another launch, and what came of the one before is behind us.
  if (!op.value) {
    op.value = { id: props.plans.mintOpId(), label: label.value }
    outcome.value = null
  }
  const sent = op.value
  sending.value = true
  error.value = ''
  try {
    const out = await props.plans.launch(p, sent.label, sent.id)
    outcome.value = out
    if (out.state !== 'pending') op.value = null
  } catch (e) {
    // Both tries lost to the network: the launch may have been made, and the same operation sent again answers it.
    error.value = `${(e as Error).message}: send it again; it is the same launch`
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
          <UFormField label="The run's label" :hint="op ? 'kept as it was sent, until this launch is settled' : `at most ${PLAN_BOUNDS.label} characters`" size="sm">
            <UInput :model-value="op ? op.label : label" :maxlength="PLAN_BOUNDS.label" :placeholder="placeholder" :disabled="!!op" class="w-full" data-launch-label @update:model-value="(v) => (label = String(v ?? ''))" />
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
              :label="outcome?.state === 'pending' || error ? 'Send again' : outcome?.state === 'failed' ? 'Launch again' : 'Launch'"
              icon="i-lucide-play"
              :loading="sending"
              :disabled="!body"
              data-launch-confirm
              @click="send"
            />
          </template>
        </div>
      </div>
    </template>
  </UModal>
</template>
