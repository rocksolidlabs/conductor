// Asking Brakeman from where a person is (the Brakeman design review's ask bar): the chips each place offers, the request a typed
// line or a chip makes, the `@` list's matches among what the page already holds, and the toast a door that sends shows. The checks
// are the server's (askProblem, internal/brakeman/request.go): what is refused here would be refused there. Pure: no Nuxt, no fetch.

import { KINDS, askProblem, parseRef, refLabel, type BrakemanRequest } from './brakeman'
import { clockOf } from './brakemanProgress'

/** The most subjects a request names (brakeman.MaxSubjects): a typed line's `ask` takes the page's subject and its mentions up to it. */
export const MAX_SUBJECTS = 10

/**
 * One chip of the bar. A chip with no `more` sends at once. `more: 'subject'` sets its kind as a token at the field's start and opens
 * the `@` list narrowed to the refs the kind takes; the line typed after it is the note. `more: 'note'` sets the token and the line is
 * the request (Find…). `more: 'since'` sends at once like a chip without, and its caret opens a time to brief from instead.
 */
export interface AskChip {
  key: string
  label: string
  kind: string
  subjects?: string[]
  since?: string
  more?: 'subject' | 'since' | 'note'
}

/** Where the bar is: the Brakeman page (with the last briefing kept), a run (its selected task, the first that needs a person), a ticket. */
export type AskWhere = { page: 'brakeman'; lastBriefAt?: string } | { run: string; selectedTask?: string; needsTask?: string } | { ticket: string }

/** A request's body as useBrakeman().ask takes it (the opId and the name are added by the sender). */
export interface AskBody {
  kind: string
  subjects?: string[]
  note?: string
  since?: string
}

/** A kind's words as the page says them (KINDS), with no "…": a chip adds its own where it opens a picker. */
export function kindLabel(kind: string): string {
  return (KINDS.find((k) => k.value === kind)?.label ?? kind).replace(/…$/, '')
}

/** "Brief me since 02:55" (the last briefing kept, in the browser's time), or "Brief me since yesterday" with none kept. */
export function briefingChipLabel(lastBriefAt: string | undefined, timeZone?: string): string {
  const at = Date.parse(lastBriefAt ?? '')
  return Number.isNaN(at) ? 'Brief me since yesterday' : `Brief me since ${clockOf(at, timeZone)}`
}

/**
 * The chips a place offers, in order. On the Brakeman page: a briefing since the last one, sent at once (its caret: another time);
 * the kinds that need a subject, each opening the `@` list; Overlapping changes, sent at once; and Find…, whose line is the request. On
 * a run: Explain this run, a handoff for the selected task (else the first that needs a person; none with neither), Overlapping
 * changes. On a ticket: Explain, Prepare a decision, Draft tickets and Propose a crew, each of the ticket, each sent at once. A
 * comparison is asked on its own page.
 */
export function askChips(where: AskWhere, timeZone?: string): AskChip[] {
  if ('page' in where) {
    const picks = (kind: string): AskChip => ({ key: kind, label: `${kindLabel(kind)}…`, kind, more: 'subject' })
    return [
      { key: 'briefing', label: briefingChipLabel(where.lastBriefAt, timeZone), kind: 'briefing', more: 'since' },
      picks('explain'),
      picks('decision'),
      picks('draft_tickets'),
      picks('propose_crew'),
      picks('handoff'),
      { key: 'overlaps', label: kindLabel('overlaps'), kind: 'overlaps' },
      { key: 'locate', label: `${kindLabel('locate')}…`, kind: 'locate', more: 'note' },
    ]
  }
  if ('run' in where) {
    const task = where.selectedTask || where.needsTask
    return [
      { key: 'explain', label: 'Explain this run', kind: 'explain', subjects: [`run:${where.run}`] },
      ...(task ? [{ key: 'handoff', label: `Prepare a handoff for ${task}`, kind: 'handoff', subjects: [`task:${where.run}/${task}`] }] : []),
      { key: 'overlaps', label: kindLabel('overlaps'), kind: 'overlaps' },
    ]
  }
  const subjects = [`ticket:${where.ticket}`]
  return [
    { key: 'explain', label: 'Explain this ticket', kind: 'explain', subjects },
    { key: 'decision', label: kindLabel('decision'), kind: 'decision', subjects },
    { key: 'draft_tickets', label: kindLabel('draft_tickets'), kind: 'draft_tickets', subjects },
    { key: 'propose_crew', label: kindLabel('propose_crew'), kind: 'propose_crew', subjects },
  ]
}

/** Refs named once each, in the order first named (the server refuses a subject named twice). */
export function uniqueRefs(refs: readonly (string | undefined)[]): string[] {
  return [...new Set(refs.filter((r): r is string => !!r))]
}

function checked(body: AskBody): AskBody | { problem: string } {
  const problem = askProblem(body.kind, body.subjects ?? [], body.note ?? '', body.since ?? '')
  return problem ? { problem } : body
}

/**
 * A typed line with no chip: always a question (`ask`, which takes any subjects and needs its note), about the page's subject and each
 * ref the line names with `@`, each once. The bar never guesses another kind from the words.
 */
export function lineRequest(line: string, mentions: readonly string[], subject?: string): AskBody | { problem: string } {
  const subjects = uniqueRefs([subject, ...mentions])
  if (subjects.length > MAX_SUBJECTS) return { problem: `A question names at most ${MAX_SUBJECTS} subjects.` }
  return checked({ kind: 'ask', ...(subjects.length ? { subjects } : {}), note: line })
}

/**
 * A chip's request: its kind, its own subjects and the ones picked from the `@` list (each once), the line as its note when there is
 * one, and its time. Refused, as the server would, when the kind needs a subject none gives, or names one it takes none of.
 */
export function chipRequest(chip: AskChip, picked: readonly string[], line = '', since?: string): AskBody | { problem: string } {
  const subjects = uniqueRefs([...(chip.subjects ?? []), ...picked])
  const at = since ?? chip.since
  return checked({ kind: chip.kind, ...(subjects.length ? { subjects } : {}), ...(line.trim() ? { note: line } : {}), ...(at ? { since: at } : {}) })
}

/** Whether a request's body came back refused. */
export function refused(b: AskBody | { problem: string }): b is { problem: string } {
  return 'problem' in b
}

/** One thing the `@` list offers: its ref, what it is called, the ref's kind, and a second line. */
export interface Mention {
  ref: string
  label: string
  kind: string
  detail?: string
}

/** What a page holds that a person may name: the runs with their tasks, the sessions, the questions waiting, the open tickets. */
export interface MentionSources {
  runs: ReadonlyArray<{ id: string; name: string; label?: string; tasks?: ReadonlyArray<{ id: string }> }>
  sessions: ReadonlyArray<{ id: string; name: string }>
  questions: ReadonlyArray<{ ref: string; title: string; session?: string }>
  tickets: ReadonlyArray<{ key: string; title: string }>
}

/** How many matches the `@` list shows. */
export const MENTIONS_SHOWN = 12

/**
 * The `@` list's matches for what follows the `@`: among what the page holds, of the ref kinds asked (none: every kind), whose name,
 * id, ref or second line holds the words, the page's own run first (its tasks, on a run's page), at most MENTIONS_SHOWN. A ref typed whole that the
 * grammar takes is offered as it is, first, though the page does not hold it: the server resolves every subject. Nothing here judges
 * what Brakeman may see.
 */
export function mentionMatches(src: MentionSources, query: string, kinds: readonly string[] = [], near?: string): Mention[] {
  const q = query.trim().toLowerCase()
  const takes = (k: string) => !kinds.length || kinds.includes(k)
  const nearRun = near ? (parseRef(near)?.run ?? '') : ''
  const all: Mention[] = []
  const runsFirst = [...src.runs].sort((a, b) => Number(b.id === nearRun) - Number(a.id === nearRun))
  for (const r of runsFirst) {
    const name = r.label || r.name
    if (takes('run')) all.push({ ref: `run:${r.id}`, label: name, kind: 'run', detail: `run ${r.id}` })
    if (takes('task')) for (const t of r.tasks ?? []) all.push({ ref: `task:${r.id}/${t.id}`, label: `${t.id} of ${name}`, kind: 'task', detail: 'task' })
  }
  if (takes('session')) for (const s of src.sessions) all.push({ ref: `session:${s.id}`, label: s.name, kind: 'session', detail: 'session' })
  if (takes('question')) for (const x of src.questions) all.push({ ref: x.ref, label: x.title, kind: 'question', detail: x.session ? `question in ${x.session}` : 'question' })
  if (takes('ticket')) for (const t of src.tickets) all.push({ ref: `ticket:${t.key}`, label: `${t.key} ${t.title}`, kind: 'ticket', detail: 'ticket' })
  const hits = q ? all.filter((m) => [m.label, m.ref, m.detail ?? ''].some((w) => w.toLowerCase().includes(q))) : all
  const typed = parseRef(query.trim())
  const whole: Mention[] = typed && takes(typed.kind) && !hits.some((m) => m.ref === query.trim()) ? [{ ref: query.trim(), label: refLabel(query.trim()), kind: typed.kind, detail: 'as typed' }] : []
  return [...whole, ...hits].slice(0, MENTIONS_SHOWN)
}

/** The toast a door shows once its ask is kept: the request's id, and Open, which leads to it on the Brakeman page. */
export function askedToast(r: Pick<BrakemanRequest, 'id'>): { title: string; icon: string; color: 'neutral'; actions: Array<{ label: string; to: string; color: 'neutral'; variant: 'outline' }> } {
  return { title: `Asked as ${r.id}`, icon: 'i-lucide-notebook-pen', color: 'neutral', actions: [{ label: 'Open', to: `/brakeman?request=${encodeURIComponent(r.id)}`, color: 'neutral', variant: 'outline' }] }
}

/** The words the bar and the doors say when they cannot ask: Brakeman off, or another server owning its records; "" when they can. */
export function askDisabled(state: { config?: { enabled?: boolean }; readOnly?: boolean } | null | undefined): string {
  if (!state) return ''
  if (!state.config?.enabled) return 'Brakeman is off: turn it on in Settings'
  if (state.readOnly) return "Another server owns Brakeman's records"
  return ''
}
