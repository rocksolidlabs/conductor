import type { CandidateReceipt } from './useDispatch'
import type { RunStep } from './useSessions'
import { receiptVersion, stepOf } from '~/utils/dispatchEvidence'

/** What a receipt's full view reads for: whether it is open, and the receipt of which attempt of which run. */
export interface ReceiptReadOf {
  open: () => boolean
  runId: () => string
  task: () => string
  attempt: () => number
  receipt: () => CandidateReceipt | null
}

const LOG_DROPPED = "The log was not kept: the run's logs were at their bound. Its SHA-256 stays on the receipt."

/**
 * The reads of a receipt opened in full (DispatchReceipt.vue; Program item DS2, docs/plans/dispatch-evidence.md, "web/app", PR-11): its
 * step from the attempt's receipts (GET …/attempts/{n}/steps), for its argv, and its log from the step log route with the SHA-256 sent
 * beside it. Read when it opens and again when what they are of moves, by one key of plain values (the opening, the run, the task, the
 * attempt and receiptVersion): a candidates read that hands over the same receipt as new objects reads nothing again, so the log shown,
 * its reading position and Save stay. Each opening counted: a read's answer is shown only in the opening it was made for.
 */
export function useReceiptRead(o: ReceiptReadOf) {
  const api = useSessions()
  const step = ref<RunStep | null>(null)
  const stepError = ref('')
  const log = ref<{ text: string; bytes: Uint8Array; sha256: string } | null>(null)
  const logError = ref('')
  const loading = ref(false)
  let opened = 0

  const key = () => {
    const r = o.receipt()
    return JSON.stringify([o.open(), o.runId(), o.task(), o.attempt(), r ? receiptVersion(r) : ''])
  }
  watch(
    key,
    async () => {
      const r = o.receipt()
      const [isOpen, run, task, n] = [o.open(), o.runId(), o.task(), o.attempt()]
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
            if (mine === opened) logError.value = LOG_DROPPED
          })
        : api
            .stepLog(run, task, n, r.id, r.k)
            .then((l) => {
              if (mine === opened) log.value = l
            })
            .catch((e) => {
              if (mine !== opened) return
              const err = e as { status?: number; code?: string; message?: string }
              logError.value = err.status === 410 || err.code === 'log_dropped' ? LOG_DROPPED : err.status === 404 ? 'This receipt has no log.' : err.message || 'The log could not be read.'
            })
      await Promise.all([steps, read])
      if (mine === opened) loading.value = false
    },
    { immediate: true },
  )

  return { step, stepError, log, logError, loading }
}
