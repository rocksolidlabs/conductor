import { CommandRefused, mintCommandId, sendCommand } from '~/utils/tasks'
import type { HistoryCommand, HistoryItem, RecordReply, ResumeReceipt, ResumeReply } from '~/utils/history'

/** What a resume from a record may add to its command id: resume without the launch's variables; a lost record's second yes. */
export interface ResumeOptions {
  withoutEnv?: boolean
  confirmLost?: boolean
}

/**
 * How long a resume answered `resume_pending` (its first send's launch still being started: the first reply lost, the same id sent
 * again) is asked again with the same id: every second, for at most two minutes, a bound on a hang only (the route settles the receipt
 * as the launch ends, and a server that stops leaves it `uncertain`).
 */
export const RESUME_PENDING_EVERY_MS = 1000
export const RESUME_PENDING_TRIES = 120

/** POST /api/history/sessions/{id}/resume's body. */
export interface ResumeBody extends ResumeOptions {
  commandId: string
}

/**
 * The history's record routes (Program item HS1, docs/plans/history.md, PR-6; docs/protocol.md, History), with the workbench token:
 * a session's record, an entry with its full availability, a command's receipt by the actor its link names, a resume from a record
 * and a Forget. A resume is a command with a receipt (utils/tasks.ts's rule): the browser mints its id, a reply lost to the network is
 * asked for once more with the same id, which the server answers with the first one's receipt and starts nothing, and a second loss is
 * the error; a refusal is a CommandRefused carrying the receipt, its `code` the refusal's. A receipt still `pending` is no refusal: the
 * same id is asked again until it settles. Every resume asked is a fresh id: a refused
 * one's receipt is settled, so asking again (without the variables, or with the second yes) is a command of its own.
 */
export function useHistory() {
  const { request } = useApi()
  const enc = encodeURIComponent
  return {
    record: (id: string) => request<RecordReply>(`/api/history/sessions/${enc(id)}`),
    entry: (key: string) => request<{ entry: HistoryItem }>('/api/history/entry', { query: { key } }).then((r) => r.entry),
    command: (run: string, actor: string, id: string) => request<{ command: HistoryCommand }>('/api/history/command', { query: { run, actor, id } }).then((r) => r.command),
    resumeRecord: (id: string, o: ResumeOptions = {}, commandId = mintCommandId()) => {
      const body: ResumeBody = { commandId }
      if (o.withoutEnv) body.withoutEnv = true
      if (o.confirmLost) body.confirmLost = true
      return (async () => {
        for (let tries = 1; ; tries++) {
          try {
            return await sendCommand<ResumeBody, ResumeReply>((b) => request<ResumeReply>(`/api/history/sessions/${enc(id)}/resume`, { method: 'POST', body: b }), body)
          } catch (e) {
            // Still being started: not a refusal. The same id asked again until its receipt settles (started, failed or uncertain).
            if (!(e instanceof CommandRefused) || (e.receipt as ResumeReceipt).state !== 'pending' || tries >= RESUME_PENDING_TRIES) throw e
            await new Promise((r) => setTimeout(r, RESUME_PENDING_EVERY_MS))
          }
        }
      })()
    },
    forgetRecord: (id: string) => request<void>(`/api/history/sessions/${enc(id)}`, { method: 'DELETE' }),
  }
}
