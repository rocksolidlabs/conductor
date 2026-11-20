// What other machines hold (docs/plans/sync-seams.md, OS-4), as GET /api/remote answers it: read only, drawn as another machine's
// ("on studio"), never taken for this machine's own. Pure.
import type { RemoteRecord, SyncStatusReply } from './protocol'

/** "on studio": where a record is from. */
export function onDevice(r: Pick<RemoteRecord, 'deviceName' | 'device'>): string {
  return `on ${r.deviceName || r.device}`
}

/** The records of another machine than this server's own (the report's `device`, when it names one). */
export function notOwn(records: readonly RemoteRecord[], report?: SyncStatusReply | null): RemoteRecord[] {
  const own = report?.device
  return records.filter((r) => !own || r.device !== own)
}

/** A remote decision as its rows show it. */
export interface RemoteDecision {
  key: string
  id: string
  device: string
  where: string
  /** The question, or null when the document keeps its request on its machine. */
  question: string | null
  state: string
  open: boolean
  ticket?: string
  at: string
}

/** The decisions among the records, newest first; a record that is not a decision's document is left out. */
export function remoteDecisions(records: readonly RemoteRecord[]): RemoteDecision[] {
  const out: RemoteDecision[] = []
  for (const r of records) {
    if (r.kind !== 'decision' || !r.data || typeof r.data !== 'object') continue
    const d = r.data as {
      id?: unknown
      state?: unknown
      request?: { question?: unknown } | null
      ticket?: unknown
    }
    if (typeof d.id !== 'string' || typeof d.state !== 'string') continue
    const question = d.request && typeof d.request.question === 'string' ? d.request.question : null
    out.push({
      key: `${r.device}/${d.id}`,
      id: d.id,
      device: r.device,
      where: onDevice(r),
      question,
      state: d.state,
      open: d.state === 'open',
      ticket: typeof d.ticket === 'string' ? d.ticket : undefined,
      at: r.at,
    })
  }
  return out.sort((a, b) => b.at.localeCompare(a.at))
}

/** A run's state words on another machine: "running on studio", "finished on studio". */
export function pointerWords(state: string, outcome: string | undefined, paused: boolean, where: string): string {
  if (paused && state === 'running') return `paused ${where}`
  if (state === 'finished' && outcome === 'failed') return `failed ${where}`
  const word =
    (
      {
        running: 'running',
        finished: 'finished',
        stopped: 'stopped',
        interrupted: 'interrupted',
      } as Record<string, string>
    )[state] ?? state
  return `${word} ${where}`
}

/** A remote run as its rows show it. */
export interface RemoteRun {
  key: string
  id: string
  where: string
  words: string
  /** The one status language's tone (StatusMark's): running is active, a failure an error, the rest done. */
  tone: 'active' | 'error' | 'done'
  ticket?: string
}

/** The run pointers among the records. */
export function remoteRuns(records: readonly RemoteRecord[]): RemoteRun[] {
  const out: RemoteRun[] = []
  for (const r of records) {
    if (r.kind !== 'pointer' || !r.data || typeof r.data !== 'object') continue
    const p = r.data as {
      id?: unknown
      state?: unknown
      outcome?: unknown
      paused?: unknown
      ticket?: unknown
    }
    if (typeof p.id !== 'string' || typeof p.state !== 'string') continue
    const outcome = typeof p.outcome === 'string' ? p.outcome : undefined
    const where = onDevice(r)
    out.push({
      key: `${r.device}/${p.id}`,
      id: p.id,
      where,
      words: pointerWords(p.state, outcome, p.paused === true, where),
      tone: p.state === 'running' ? 'active' : outcome === 'failed' ? 'error' : 'done',
      ticket: typeof p.ticket === 'string' ? p.ticket : undefined,
    })
  }
  return out
}

/** Another machine's pin as the sidebar shows it: greyed, opening nothing. */
export interface RemotePin {
  key: string
  session: string
  name: string
  where: string
}

/** The pins of the records' pins documents, each machine's in its order. */
export function remotePins(records: readonly RemoteRecord[]): RemotePin[] {
  const out: RemotePin[] = []
  for (const r of records) {
    if (r.kind !== 'pins' || !r.data || typeof r.data !== 'object') continue
    const pins = (r.data as { pins?: unknown }).pins
    if (!Array.isArray(pins)) continue
    for (const p of pins) {
      if (!p || typeof p !== 'object') continue
      const { session, name } = p as { session?: unknown; name?: unknown }
      if (typeof session !== 'string') continue
      out.push({
        key: `${r.device}/${session}`,
        session,
        name: typeof name === 'string' && name ? name : session,
        where: onDevice(r),
      })
    }
  }
  return out
}

/**
 * Where an open remote decision is answered: the report's answer page with the decision's id, an https address alone; null when the
 * report names none, the decision is not open, or its request is kept on its machine (nothing elsewhere answers it).
 */
export function answerPage(report: SyncStatusReply | null | undefined, d: Pick<RemoteDecision, 'id' | 'open' | 'question'>): string | null {
  if (!report?.answerUrl || !d.open || d.question === null) return null
  let u: URL
  try {
    u = new URL(report.answerUrl)
  } catch {
    return null
  }
  if (u.protocol !== 'https:') return null
  u.searchParams.set('decision', d.id)
  return u.toString()
}
