import { ref } from 'vue'
import { mintCommandId, type Receipt } from '~/utils/tasks'

/**
 * A candidate's preview (docs/plans/browser-evidence.md, "A candidate's preview"; docs/protocol.md, the preview commands): Preview this
 * candidate makes a preview checkout at the candidate's commit with the run's project manifest, `POST
 * /api/runs/{run}/tasks/{task}/attempts/{n}/preview {commandId}` answering its `seq` and state; Close ends it, `POST
 * …/preview/{seq}/close {commandId}`. Each is a command with its receipt in the run's ledger: a reply lost to the network is asked for
 * once more with the same command id, which the server answers with the first's receipt. A refusal is said in the server's words
 * (`{error, receipt}`, or `{error}` before the command was taken); two checkouts open already (`429 previews_busy`) names them.
 */

export interface CandidateOpened {
  receipt?: Receipt
  seq: number
  state: 'resolved' | 'ready' | 'ending'
}

/** A candidate's checkout open now, as a `previews_busy` refusal names it. */
export interface OpenCheckout {
  run: string
  task: string
  attempt: number
  seq: number
}

/** The checkouts a `previews_busy` refusal names as open (its `open`), none for any other refusal. */
export function openCheckouts(e: unknown): OpenCheckout[] {
  const err = e as { code?: string; body?: { open?: OpenCheckout[] }; details?: { open?: OpenCheckout[] } }
  if (err.code !== 'previews_busy') return []
  return (err.body?.open ?? err.details?.open ?? []).filter((p) => !!p && typeof p.run === 'string' && typeof p.task === 'string' && Number.isInteger(p.attempt) && Number.isInteger(p.seq))
}

/** The words of a refusal of a preview command: the server's, and for `previews_busy` the checkouts open now. */
export function previewRefusal(e: unknown): string {
  type Open = OpenCheckout[]
  const err = e as { code?: string; message?: string; body?: { open?: Open }; details?: { open?: Open } }
  if (err.code === 'previews_busy') {
    const open = (err.body?.open ?? err.details?.open ?? []).map((p) => `${p.task} attempt ${p.attempt}`).join(' and ')
    return open ? `Two candidates' previews are open already (${open}): close one first.` : (err.message ?? 'Two candidates are previewed already: close one first.')
  }
  return err.message || 'the preview command was refused'
}

/** An attempt's open checkout as a run read gives it (`previewing`). */
export interface Previewing {
  seq: number
  state: string
}

/**
 * The open checkout of a candidate (attempt n): the live run's, when it names the task's current attempt as n, else the task route's
 * read of n (its attempts carry `previewing`, an older attempt's and a run's the server no longer holds included); null for none open
 * or one ending.
 */
export function checkoutOfAttempt(n: number, live: { n: number; previewing?: Previewing } | undefined, read: ReadonlyArray<{ n: number; previewing?: Previewing }> | undefined): Previewing | null {
  const p = live?.n === n && live.previewing ? live.previewing : read?.find((a) => a.n === n)?.previewing
  return p && p.state !== 'ending' ? p : null
}

/** How often, and how far apart, a command is asked again while the server says its open is still under way. */
export const OPENING_TRIES = 30
export const OPENING_WAIT_MS = 1000

/**
 * Sends one command and asks it again, the same body with its command id, while asking again is the same question: once when the
 * reply was lost to the network (status 0), and while the server answers `409 preview_opening` (the open that command id asked for is
 * taken and not yet settled: a repeat is answered with the preview's `seq` once it has one), a moment apart and at most `tries`
 * times. Any other refusal, and a `preview_opening` past the tries, is thrown as the server said it: no page is opened without a seq.
 */
export async function sendCommand<R>(send: () => Promise<R>, wait: (ms: number) => Promise<void> = (ms) => new Promise((r) => setTimeout(r, ms)), tries = OPENING_TRIES): Promise<R> {
  let lost = false
  for (let opening = 0; ; ) {
    try {
      return await send()
    } catch (e) {
      const err = e as { status?: number; code?: string }
      if (err.status === 0 && !lost) {
        lost = true
        continue
      }
      if (err.status === 409 && err.code === 'preview_opening' && opening < tries) {
        opening++
        await wait(OPENING_WAIT_MS)
        continue
      }
      throw e
    }
  }
}

export function useCandidatePreview(runId: () => string) {
  const { request } = useApi()
  /** The command under way, `<task>:<n>:open` or `<task>:<n>:close`; '' for none. */
  const busy = ref('')
  /** The last refusal's words, by `<task>:<n>`; its code; and the checkouts a `previews_busy` named, to open or close from there. */
  const refused = ref<Record<string, string>>({})
  const refusedCode = ref<Record<string, string>>({})
  const busyOpen = ref<Record<string, OpenCheckout[]>>({})
  function refuse(key: string, e: unknown) {
    refused.value = { ...refused.value, [key]: e === null ? '' : previewRefusal(e) }
    refusedCode.value = { ...refusedCode.value, [key]: e === null ? '' : ((e as { code?: string }).code ?? '') }
    busyOpen.value = { ...busyOpen.value, [key]: e === null ? [] : openCheckouts(e) }
  }
  /** POSTs body as one command (`sendCommand`): asked again with the same body, its command id, while that is the same question. */
  const post = <R>(path: string, body: { commandId: string }): Promise<R> => sendCommand(() => request<R>(path, { method: 'POST', body }))
  const base = (task: string, n: number, run = runId()) => `/api/runs/${encodeURIComponent(run)}/tasks/${encodeURIComponent(task)}/attempts/${n}/preview`

  /** Preview this candidate: its checkout's `seq`, or null when the server refused (its words in `refused`). */
  async function open(task: string, n: number): Promise<CandidateOpened | null> {
    const key = `${task}:${n}`
    busy.value = `${key}:open`
    try {
      const r = await post<CandidateOpened & { receipt: Receipt }>(base(task, n), { commandId: mintCommandId() })
      refuse(key, null)
      return r
    } catch (e) {
      refuse(key, e)
      return null
    } finally {
      busy.value = ''
    }
  }

  /**
   * Close: the checkout of `seq` ends (its services stopped, its cleanup run); false when the server refused. `run` names another run's
   * checkout (one a `previews_busy` named); a refusal is then said on `on`, the card that was refused, its list of the open ones gone.
   */
  async function close(task: string, n: number, seq: number, run?: string, on?: string): Promise<boolean> {
    const key = on ?? `${task}:${n}`
    busy.value = `${task}:${n}:close`
    try {
      await post<{ receipt: Receipt; state: string }>(`${base(task, n, run)}/${seq}/close`, { commandId: mintCommandId() })
      refuse(key, null)
      return true
    } catch (e) {
      refuse(key, e)
      return false
    } finally {
      busy.value = ''
    }
  }

  return { busy, refused, refusedCode, busyOpen, open, close }
}
