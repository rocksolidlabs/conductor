<script setup lang="ts">
import { shortCommit } from '~/utils/dispatchPlans'
import { useRecheckAsk } from '~/composables/useReview'
import { FAILURE_CLASSES, checkNote, classWords, failedReceiptActions, type DraftNote, type FailureClass, type PreparedRepair } from '~/utils/review'

/**
 * A failed check prepared for its repair (Program item RV1, docs/plans/review-repair.md, PR-7), from the server's prepared request
 * (GET …/steps/{id}/{k}/repair): the tested commit, the receipt's identity and how it ended, the last of its output, the log's size and
 * hash, the attempt before, what the candidate was to meet (the ticket's criteria as launched, else the task's checks), and the class
 * the server worked out from the receipt, which the person may change. Its actions are failedReceiptActions': Check again exactly when
 * the prepared request says Yardmaster takes it (first for an infrastructure failure or a possibly flaky check), its why in its place
 * otherwise; Add to the draft, a check note the review sends as Request changes; Retry on the run's page when the task failed.
 */
const props = defineProps<{
  prepared: PreparedRepair | null
  error: string
  loading: boolean
  /** The task failed: Retry is the run page's. */
  taskFailed: boolean
  /** Checks again the candidate the prepared request names: the words of where it was, or throws. */
  recheck: (p: PreparedRepair) => Promise<string>
}>()
const open = defineModel<boolean>('open', { default: false })
const emit = defineEmits<{ add: [note: Omit<DraftNote, 'key'>] }>()

const chosen = ref<FailureClass>('code')
const quote = ref('')
const message = ref('')
// Check again's state, its reply applied only while this panel shows the prepared request it was asked of (useRecheckAsk); declared
// before the watch below, which resets it at once.
const asked = useRecheckAsk(() => props.prepared)
const busy = asked.busy
const rechecked = asked.rechecked
const failure = asked.failure
watch(
  () => props.prepared,
  (p) => {
    chosen.value = (p?.finding.check.chosen as FailureClass) || p?.finding.check.class || 'code'
    quote.value = p?.finding.check.quote ?? p?.finding.check.tail ?? ''
    message.value = p?.finding.message ?? ''
    asked.reset()
  },
  { immediate: true },
)
const ch = computed(() => props.prepared?.finding.check ?? null)
const acts = computed(() => (props.prepared ? failedReceiptActions(props.prepared.recheck, chosen.value, props.taskFailed) : { actions: [], why: '' }))
const classes = FAILURE_CLASSES.map((c) => ({ label: classWords(c).label, value: c }))
const ended = computed(() => {
  const c = ch.value
  if (!c) return ''
  if (c.timedOut) return 'timed out'
  if (c.signal) return `signal ${c.signal}`
  if (c.killed) return 'interrupted'
  return c.exit !== undefined ? `exit ${c.exit}` : c.state
})
function again() {
  void asked.again(props.recheck)
}
function add() {
  if (!props.prepared) return
  emit('add', checkNote(props.prepared, chosen.value, quote.value, message.value))
  open.value = false
}
</script>

<template>
  <UModal v-model:open="open" :title="ch ? `Prepare a repair: ${ch.phase} ${ch.id} of ${ch.task} attempt ${ch.attempt}` : 'Prepare a repair'" :ui="{ content: 'sm:max-w-3xl' }">
    <template #body>
      <p v-if="loading" class="text-sm text-muted">Reading the receipt…</p>
      <UAlert v-else-if="error" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="error" data-repair-error />
      <div v-else-if="prepared && ch" class="flex flex-col gap-3 text-sm" data-repair-panel :data-repair-receipt="ch.receipt" :data-repair-class="ch.class" :data-repair-recheck-allowed="prepared.recheck.allowed ? 'true' : 'false'">
        <dl class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
          <dt class="text-muted">Receipt</dt>
          <dd class="break-all font-mono" data-repair-identity>{{ ch.receipt }}</dd>
          <dt class="text-muted">Tested commit</dt>
          <dd class="break-all font-mono" data-repair-subject>{{ ch.subject }}</dd>
          <dt class="text-muted">Ran</dt>
          <dd class="break-all font-mono">{{ ch.argv.join(' ') }}</dd>
          <dt class="text-muted">Ended</dt>
          <dd data-repair-ended>{{ ended }}<template v-if="ch.error">: {{ ch.error }}</template> · where it ran: {{ ch.where }}</dd>
          <dt class="text-muted">Log</dt>
          <dd class="break-all font-mono text-[11px]" data-repair-log-sha>{{ ch.logBytes }} bytes · SHA-256 {{ ch.logSha256 || '—' }}</dd>
          <template v-if="ch.previous">
            <dt class="text-muted">The attempt before</dt>
            <dd data-repair-previous>attempt {{ ch.previous.attempt }} {{ ch.previous.state }}<template v-if="ch.previous.stage"> at {{ ch.previous.stage }}</template><template v-if="ch.previous.commit"> ({{ shortCommit(ch.previous.commit) }})</template></dd>
          </template>
        </dl>
        <div class="flex flex-col gap-1">
          <span class="text-xs font-medium text-muted">The last of its output</span>
          <pre class="max-h-48 overflow-auto whitespace-pre-wrap break-words rounded bg-elevated/40 p-2 font-mono text-xs" data-repair-tail>{{ ch.tail || '(no output)' }}</pre>
        </div>
        <div class="flex flex-col gap-1" data-repair-criteria :data-repair-criteria-source="prepared.criteria.source">
          <span class="text-xs font-medium text-muted">
            What it was to meet: {{ prepared.criteria.source === 'ticket' ? `the acceptance criteria of ${prepared.ticket?.key ?? 'the ticket'} as launched${prepared.plan ? ` (plan r${prepared.plan.n})` : ''}` : "the task's checks" }}
          </span>
          <pre v-if="prepared.criteria.acceptance" class="max-h-40 overflow-auto whitespace-pre-wrap break-words rounded bg-elevated/40 p-2 text-xs" data-repair-acceptance>{{ prepared.criteria.acceptance }}</pre>
          <ul v-else-if="prepared.criteria.checks?.length" class="flex flex-col gap-0.5 text-xs">
            <li v-for="c in prepared.criteria.checks" :key="c.id"><span class="font-mono">{{ c.id }}</span>: <code class="font-mono text-muted">{{ c.argv.join(' ') }}</code></li>
          </ul>
        </div>
        <div class="flex flex-col gap-2 sm:flex-row sm:items-start">
          <UFormField label="Class" size="sm" :hint="`the server's: ${classWords(ch.class).label}`" class="sm:w-72">
            <USelect v-model="chosen" :items="classes" size="sm" class="w-full" data-repair-chosen />
          </UFormField>
          <p class="text-xs text-muted sm:flex-1 sm:pt-6">{{ classWords(chosen).detail }}</p>
        </div>
        <UFormField label="What the repair should do" size="sm">
          <UTextarea v-model="message" :rows="2" autoresize :maxrows="6" :maxlength="1000" class="w-full" data-repair-message />
        </UFormField>
        <UFormField label="What to quote of the output" size="sm" hint="yours; the log stays the evidence">
          <UTextarea v-model="quote" :rows="2" autoresize :maxrows="6" class="w-full font-mono" data-repair-quote />
        </UFormField>
        <p v-if="acts.why" class="text-xs text-muted" data-repair-recheck-why>Check again is not offered: {{ acts.why }}</p>
        <p v-if="rechecked" class="text-xs text-success" data-repair-rechecked>{{ rechecked }}</p>
        <p v-if="failure" class="text-xs text-error" data-repair-recheck-error>{{ failure }}</p>
      </div>
    </template>
    <template #footer>
      <div class="flex w-full flex-wrap justify-end gap-2" data-repair-actions :data-repair-actions-order="acts.actions.join(',')">
        <UButton label="Close" color="neutral" variant="ghost" @click="open = false" />
        <template v-for="a in acts.actions" :key="a">
          <UButton v-if="a === 'recheck'" label="Check again" icon="i-lucide-refresh-cw" color="neutral" variant="outline" :loading="busy" :disabled="!!rechecked" data-repair-recheck @click="again" />
          <UButton v-else-if="a === 'request'" label="Add to the draft" icon="i-lucide-message-square-plus" data-repair-add @click="add" />
          <UButton v-else-if="a === 'retry' && ch" label="Retry on the run's page" icon="i-lucide-rotate-ccw" color="neutral" variant="ghost" :to="`/runs/${encodeURIComponent(ch.run)}`" data-repair-retry />
        </template>
      </div>
    </template>
  </UModal>
</template>
