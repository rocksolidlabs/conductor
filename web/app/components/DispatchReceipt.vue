<script setup lang="ts">
import type { CandidateReceipt } from '~/composables/useDispatch'
import type { RunStep } from '~/composables/useSessions'
import { durationWords, logMatches, receiptChips, receiptDuration, receiptName, stepOf } from '~/utils/dispatchEvidence'
import { shortCommit } from '~/utils/dispatchPlans'
import { stepCommand } from '~/utils/tasks'

/**
 * A receipt in full (Program item DS2, docs/plans/dispatch-evidence.md, "web/app", PR-11): what ran (its argv, from the attempt's own
 * receipts, GET …/attempts/{n}/steps), against which commit, where it ran (the runtime: `native` until another exists), how it ended
 * (exit, signal, timed out, interrupted, whether its tree stayed as it was: `stable`), how long it took, and its log, fetched with the
 * token from the step log route, never as a link the browser follows; the SHA-256 the route sent beside it (`X-Conductor-Log-SHA256`)
 * shown and compared with the one the receipt recorded. Save downloads the log's bytes as the server sent them. Never a pid or a
 * process group: a receipt says what ran and how it ended, never how its process was made.
 */
const props = defineProps<{
  runId: string
  task: string
  attempt: number
  /** The commit the receipt ran against: the candidate's. */
  commit: string
  receipt: CandidateReceipt | null
}>()
const open = defineModel<boolean>('open', { default: false })

const api = useSessions()

const step = ref<RunStep | null>(null)
const stepError = ref('')
const log = ref<{ text: string; bytes: Uint8Array; sha256: string } | null>(null)
const logError = ref('')
const loading = ref(false)
/** Each opening, counted: a read's answer is shown only in the opening it was made for. */
let opened = 0

watch(
  () => [open.value, props.runId, props.task, props.attempt, props.receipt] as const,
  async ([isOpen, run, task, n, r]) => {
    const mine = ++opened
    step.value = null
    stepError.value = ''
    log.value = null
    logError.value = ''
    if (!isOpen || !r) return
    loading.value = true
    const steps = api
      .runSteps(run, task, n)
      .then((all) => {
        if (mine === opened) step.value = stepOf(all, r) ?? null
      })
      .catch((e) => {
        if (mine === opened) stepError.value = (e as Error).message
      })
    const read = r.logDropped
      ? Promise.resolve().then(() => {
          if (mine === opened) logError.value = "The log was not kept: the run's logs were at their bound. Its SHA-256 stays on the receipt."
        })
      : api
          .stepLog(run, task, n, r.id, r.k)
          .then((l) => {
            if (mine === opened) log.value = l
          })
          .catch((e) => {
            if (mine !== opened) return
            const err = e as { status?: number; code?: string; message?: string }
            logError.value = err.status === 410 || err.code === 'log_dropped' ? "The log was not kept: the run's logs were at their bound. Its SHA-256 stays on the receipt." : err.status === 404 ? 'This receipt has no log.' : err.message || 'The log could not be read.'
          })
    await Promise.all([steps, read])
    if (mine === opened) loading.value = false
  },
  { immediate: true },
)

const chips = computed(() => (props.receipt ? receiptChips(props.receipt) : []))
const duration = computed(() => (props.receipt ? durationWords(receiptDuration(props.receipt)) : ''))
const match = computed(() => logMatches(log.value?.sha256 ?? '', props.receipt?.logSha256))

function save() {
  const l = log.value
  if (!l || !props.receipt) return
  const url = URL.createObjectURL(new Blob([l.bytes as BlobPart], { type: 'text/plain' }))
  const a = document.createElement('a')
  a.href = url
  a.download = `${props.runId}-${props.task}-${props.attempt}-${props.receipt.phase}-${props.receipt.id}-${props.receipt.k}.log`
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 60_000)
}
</script>

<template>
  <UModal v-model:open="open" :title="receipt ? `${task} attempt ${attempt}: ${receiptName(receipt)}` : ''" :description="receipt ? `Against ${shortCommit(commit)}, where it ran: ${receipt.where}` : ''" :ui="{ content: 'sm:max-w-3xl' }">
    <template #body>
      <div v-if="receipt" class="flex flex-col gap-3 text-sm" data-receipt-modal :data-receipt-key="`${receipt.phase}:${receipt.id}:${receipt.k}`">
        <div class="flex flex-wrap items-center gap-2">
          <UBadge v-for="c in chips" :key="c.label" :label="c.label" :color="c.color" :icon="c.icon" :title="c.title" variant="subtle" size="sm" />
          <UBadge :label="receipt.where" color="neutral" variant="outline" size="sm" icon="i-lucide-cpu" :title="`Where it ran: the ${receipt.where} runtime`" data-receipt-modal-where />
          <span v-if="duration" class="text-xs text-muted">{{ duration }}</span>
        </div>
        <dl class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
          <dt class="text-muted">Ran</dt>
          <dd class="min-w-0">
            <code v-if="step" class="break-all font-mono" data-receipt-argv>{{ stepCommand(step) }}</code>
            <span v-else-if="stepError" class="text-warning">{{ stepError }}</span>
            <span v-else class="text-dimmed">Reading…</span>
          </dd>
          <dt class="text-muted">Commit</dt>
          <dd class="break-all font-mono" data-receipt-commit>{{ commit }}</dd>
          <dt class="text-muted">Exit</dt>
          <dd data-receipt-exit>{{ receipt.exit ?? (receipt.signal ? `signal ${receipt.signal}` : '—') }}</dd>
          <dt class="text-muted">Stable</dt>
          <dd>{{ receipt.stable ? 'yes: the tree was as it found it' : 'no: it changed tracked files, or the commit moved' }}</dd>
          <template v-if="receipt.error">
            <dt class="text-muted">Error</dt>
            <dd class="whitespace-pre-wrap break-words text-error">{{ receipt.error }}</dd>
          </template>
          <dt class="text-muted">Recorded SHA-256</dt>
          <dd class="break-all font-mono" data-receipt-recorded-sha>{{ receipt.logSha256 || '—' }}</dd>
        </dl>
        <p v-if="loading && !log && !logError" class="text-muted">Reading the log…</p>
        <UAlert v-else-if="logError" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="logError" data-receipt-log-error />
        <template v-else-if="log">
          <pre class="max-h-[50vh] overflow-auto whitespace-pre-wrap break-words rounded bg-elevated/40 p-2 font-mono text-xs" data-receipt-log-text>{{ log.text }}</pre>
          <p class="flex flex-wrap items-center gap-2 break-all font-mono text-[11px] text-muted">
            <span>Served SHA-256 <span data-receipt-log-sha>{{ log.sha256 }}</span></span>
            <UBadge
              :label="match === 'matches' ? 'matches the receipt' : match === 'differs' ? 'differs from the receipt' : 'not compared'"
              :color="match === 'matches' ? 'success' : match === 'differs' ? 'error' : 'neutral'"
              variant="subtle"
              size="sm"
              :data-receipt-sha-match="match"
            />
          </p>
        </template>
      </div>
    </template>
    <template #footer>
      <div class="flex w-full justify-end gap-2">
        <UButton label="Close" color="neutral" variant="ghost" @click="open = false" />
        <UButton label="Save" icon="i-lucide-download" color="neutral" variant="outline" :disabled="!log" data-receipt-log-save @click="save" />
      </div>
    </template>
  </UModal>
</template>
