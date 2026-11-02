<script setup lang="ts">
import type { DispatchTicket, PlanBody, PlanSummary } from '~/composables/useDispatch'
import type { TicketPlans } from '~/composables/useTicketPlans'
import { PLAN_BOUNDS, defaultLabel, revisionTag, shortCommit, yoloWords, type LaunchOutcome } from '~/utils/dispatchPlans'

/**
 * Launching a managed run from a ticket's approved plan revision (Program item DS2, docs/plans/dispatch-evidence.md, "web/app"). It asks
 * first: the revision, the commit the run starts from, the yolo choice said plainly, and the run's label (the ticket's key and title when
 * none is given). Then it says what came of it: the run started, with its link; the launch failed, with why; still being started, sent
 * again under the same operation (never a second run); or refused before any run, in the server's words. The operation's id is minted
 * once per launch asked for, so a reply lost to the network, or a pending launch sent again, is the same launch.
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
/** The operation this launch is: kept while it may still be the same launch (pending, a reply lost), minted again after it is settled. */
let opId = ''

watch(open, (o) => {
  if (!o) return
  label.value = ''
  outcome.value = null
  error.value = ''
  opId = props.plans.mintOpId()
})

const yolo = computed(() => yoloWords(props.body?.crew.yolo))
const placeholder = computed(() => defaultLabel(props.ticket.key, props.ticket.title))
const runPath = computed(() => (outcome.value?.state === 'started' ? `/runs/${encodeURIComponent(outcome.value.launch.runId)}` : ''))

async function send() {
  const p = props.revision
  if (!p || sending.value) return
  // A launch settled (started or failed) or refused is behind us: the next press is another launch.
  if (outcome.value && outcome.value.state !== 'pending') opId = props.plans.mintOpId()
  sending.value = true
  error.value = ''
  try {
    outcome.value = await props.plans.launch(p, label.value, opId)
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
          <p class="font-medium" :class="yolo.tone === 'warning' ? 'text-warning' : 'text-muted'" :data-launch-yolo="body?.crew.yolo ? 'on' : 'off'">{{ yolo.label }}. {{ yolo.words }}</p>
          <UFormField label="The run's label" :hint="`at most ${PLAN_BOUNDS.label} characters`" size="sm">
            <UInput v-model="label" :maxlength="PLAN_BOUNDS.label" :placeholder="placeholder" class="w-full" data-launch-label />
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
              data-launch-confirm
              @click="send"
            />
          </template>
        </div>
      </div>
    </template>
  </UModal>
</template>
