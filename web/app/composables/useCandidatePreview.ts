import { ref } from 'vue'
import { mintCommandId, sendCommand, type Receipt } from '~/utils/tasks'

/**
 * A candidate's preview (docs/plans/browser-evidence.md, "A candidate's preview"; docs/protocol.md, the preview commands): Preview this
 * candidate makes a preview checkout at the candidate's commit with the run's project manifest, `POST
 * /api/runs/{run}/tasks/{task}/attempts/{n}/preview {commandId}` answering its `seq` and state; Close ends it, `POST
 * …/preview/{seq}/close {commandId}`. Each is a command with its receipt in the run's ledger: a reply lost to the network is asked for
 * once more with the same command id, which the server answers with the first's receipt (sendCommand). A refusal is said in the
 * server's words; two checkouts open already (`429 previews_busy`) names them.
 */

export interface CandidateOpened {
  receipt?: Receipt
  seq: number
  state: 'resolved' | 'ready' | 'ending'
}

/** The words of a refusal of a preview command: the server's, and for `previews_busy` the checkouts open now. */
export function previewRefusal(e: unknown): string {
  type Open = Array<{ run: string; task: string; attempt: number; seq: number }>
  const err = e as { code?: string; message?: string; body?: { open?: Open }; details?: { open?: Open } }
  if (err.code === 'previews_busy') {
    const open = (err.body?.open ?? err.details?.open ?? []).map((p) => `${p.task} attempt ${p.attempt}`).join(' and ')
    return open ? `Two candidates' previews are open already (${open}): close one first.` : (err.message ?? 'Two candidates are previewed already: close one first.')
  }
  return err.message || 'the preview command was refused'
}

export function useCandidatePreview(runId: () => string) {
  const { request } = useApi()
  /** The command under way, `<task>:<n>:open` or `<task>:<n>:close`; '' for none. */
  const busy = ref('')
  /** The last refusal's words, by `<task>:<n>`. */
  const refused = ref<Record<string, string>>({})
  const base = (task: string, n: number) => `/api/runs/${encodeURIComponent(runId())}/tasks/${encodeURIComponent(task)}/attempts/${n}/preview`

  /** Preview this candidate: its checkout's `seq`, or null when the server refused (its words in `refused`). */
  async function open(task: string, n: number): Promise<CandidateOpened | null> {
    const key = `${task}:${n}`
    busy.value = `${key}:open`
    try {
      const r = await sendCommand((b: { commandId: string }) => request<CandidateOpened & { receipt: Receipt }>(base(task, n), { method: 'POST', body: b }), { commandId: mintCommandId() })
      refused.value = { ...refused.value, [key]: '' }
      return r
    } catch (e) {
      refused.value = { ...refused.value, [key]: previewRefusal(e) }
      return null
    } finally {
      busy.value = ''
    }
  }

  /** Close: the checkout of `seq` ends (its services stopped, its cleanup run); false when the server refused. */
  async function close(task: string, n: number, seq: number): Promise<boolean> {
    const key = `${task}:${n}`
    busy.value = `${key}:close`
    try {
      await sendCommand((b: { commandId: string }) => request<{ receipt: Receipt; state: string }>(`${base(task, n)}/${seq}/close`, { method: 'POST', body: b }), { commandId: mintCommandId() })
      refused.value = { ...refused.value, [key]: '' }
      return true
    } catch (e) {
      refused.value = { ...refused.value, [key]: previewRefusal(e) }
      return false
    } finally {
      busy.value = ''
    }
  }

  return { busy, refused, open, close }
}
