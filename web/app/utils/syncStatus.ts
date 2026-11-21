// The Sync row of Settings → The app (docs/plans/sync-seams.md, OS-4), from GET /api/sync: the sync store's state in words, the
// changes it kept back beneath, and what this server keeps on the machine of its own judgement. Pure.
import type { SyncStatusReply } from './protocol'

/** A time as the row says it: 14:02, in the viewer's zone. */
export function clock(iso: string | undefined): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

function waiting(n: number | undefined): string {
  if (!n) return ''
  return n === 1 ? '1 change waiting' : `${n} changes waiting`
}

/** The state's line: "current", "behind: 37 changes waiting", "unreachable since 14:02; 37 changes waiting", …; '' with no report. */
export function syncLine(r: SyncStatusReply | null | undefined): string {
  if (!r?.state) return ''
  const since = clock(r.since)
  const w = waiting(r.waiting)
  switch (r.state) {
    case 'current':
      return 'current'
    case 'behind':
      return w ? `behind: ${w}` : 'behind'
    case 'unreachable':
      return [since ? `unreachable since ${since}` : 'unreachable', w].filter(Boolean).join('; ')
    case 'lapsed':
      return `${since ? `lapsed since ${since}` : 'lapsed'}: this machine works alone and shares nothing new`
    case 'signed_out':
      return 'signed out: nothing is shared'
  }
  return ''
}

/** The lines beneath it: each change kept back ("CND-12's comment: quotes captured output"), then what this server keeps itself. */
export function syncKeptLines(r: SyncStatusReply | null | undefined): string[] {
  if (!r) return []
  const out = (r.withheld ?? []).map((w) => (w.words ? `${w.ref}: ${w.words}` : w.ref))
  const here = r.here
  if (here?.check) out.push(here.check)
  if (here?.cut) {
    out.push(
      here.cut === 1
        ? 'A decision has parts cut as output: a person elsewhere sees "[output kept on this machine]" in their place'
        : `${here.cut} decisions have parts cut as output: a person elsewhere sees "[output kept on this machine]" in their place`,
    )
  }
  if (here?.hosted) {
    out.push(
      `${here.hosted === 1 ? 'A text' : `${here.hosted} texts`} a hosted session's agent wrote ${here.hosted === 1 ? 'stays' : 'stay'} on this machine: this server does not see that agent's output`,
    )
  }
  return out
}
