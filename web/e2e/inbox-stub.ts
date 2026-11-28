import type { Page, Route } from '@playwright/test'
import { runInfo, type StubRun, type StubTask } from './run-fixtures'

// The inbox answered in the browser (UX1's PR-1, docs/plans/ux-review.md, "Stubs in the browser"): for the shapes the day cannot make
// exactly or cheaply (a title of 200 characters, an item answered and not yet applied, a read that fails). A stub proves how a shape
// is drawn, never that the server sends it. It answers as the server does (internal/api/inbox.go, inbox_decisions.go,
// inbox_track.go): each item is made whole first, with the actions, refs and words the server makes for its kind, and listed as the
// server folds it; the list in the server's order; an item past one of the server's bounds refused. stubs.spec.ts holds every key of
// it to the day's real items, so that a field the server gains fails there, not in a layout spec.

export type InboxKind = 'question' | 'plan' | 'task' | 'decision'

/** An item the stub answers: what the list shows of it (the fields a spec may set), and what its other reads are made from. */
export interface StubItem {
  ref: string
  kind: InboxKind
  since: string
  title: string
  brief: string
  question: string
  answerer: string
  /** A question's choices, a decision's options: each one action, in this order. */
  choices: string[]
  /** A decision's state: open, or answered and not yet applied (a tracking item, its one action a comment). */
  state?: 'open' | 'answered'
  /** A question that takes a line (the reply line), as a prompt does: true when not given. */
  line?: boolean
  /** A plan revision approved whose approval no longer holds (it says why in `now`), not one proposed. */
  stale?: boolean
  /** A failed task's Check again of its last attempt's commit: true when not given. */
  recheck?: boolean
  /**
   * A decision's type and who asked it: an agent's attempt (approval when not given), a plan's gate, or a comparison's selection
   * (`compare`: Yardmaster's approval whose options are the candidates, `candidates`, then None of these; compareRecord).
   */
  type?: 'approval' | 'clarification'
  origin?: 'agent' | 'plan' | 'compare'
  /** A selection's candidates, in the block's order: each its task, attempt and commit (two when not given). */
  candidates?: StubCandidate[]
  /** A task's or a decision's run (a run runInfo makes; one of six tasks, made once, when not given) and its task there. */
  run?: StubRun
  task?: string
  /** Fields over the item GET /api/inbox/item gives. */
  item?: Record<string, unknown>
  /** Fields over a decision's record, as GET /api/decisions and /api/decisions/{id} give it. */
  decision?: Record<string, unknown>
}

/** A selection's candidate (a comparison's lane with a standing result): its task, its attempt and its commit. */
export interface StubCandidate {
  task: string
  n: number
  commit: string
}

/** The candidates a selection names when the spec gives none: approach-a and approach-b, each at a commit of its own. */
const CANDIDATES: StubCandidate[] = [
  { task: 'approach-a', n: 1, commit: '1f3a5c7e9b2d4f6a8c0e1f3a5c7e9b2d4f6a8c0e' },
  { task: 'approach-b', n: 2, commit: '4b825dc642cb6eb9a060e54bf8d69288fbee4904' },
]

/** What a selection's `authorizes` says (docs/plans/compare-crew.md, PR-6a, "Which record"). */
export const SELECTION_AUTHORIZES = 'Continuing the selected candidate into review and repair, and delivering it. Final: the candidates not selected are kept and cannot be continued; a new trial tries them again.'

/** The fields a spec sets on an item: what the list derives (its actions, its count of blocked work, tracking) it does not. */
export type ItemOver = Partial<Omit<StubItem, 'kind'>>

// The server's bounds, encoded (inbox.go): a summary's title, brief, question and answerer, an item's brief and question, a ref, the
// actions and their labels, a page of the list.
export const BOUNDS = { title: 200, brief: 2 << 10, summaryBrief: 280, question: 500, summaryQuestion: 280, answerer: 120, ref: 512, actions: 10, label: 80, page: 150 }

const ANSWERER = 'you, the operator'

/** What Go's encoding/json escapes in six bytes that JSON.stringify does not: <, >, &, and the two line and paragraph separators. */
const SIX = new RegExp(`[<>&${String.fromCharCode(0x2028, 0x2029)}]`, 'g')

/** How many bytes s takes in the server's JSON, its quotes left out (Go's encoding/json escapes <, >, &, U+2028 and U+2029 in six). */
export function jsonLen(s: string): number {
  return Buffer.byteLength(JSON.stringify(s).replace(SIX, 'x'.repeat(6))) - 2
}

/** The server's fitText: s whole when it fits `max` encoded bytes, else cut at a character's end with "…" until it does. */
export function fitText(s: string, max: number): string {
  if (jsonLen(s) <= max) return s
  const cs = [...s]
  let lo = 0
  let hi = cs.length
  while (lo < hi) {
    const mid = Math.floor((lo + hi + 1) / 2)
    if (jsonLen(cs.slice(0, mid).join('') + '…') <= max) lo = mid
    else hi = mid - 1
  }
  return cs.slice(0, lo).join('') + '…'
}

let made = 0
let shared: StubRun | undefined
const hex = (n: number) => n.toString(16).padStart(16, '0')
const short = (c: string) => c.slice(0, 12)

/** The run a task or a decision belongs to when the spec names none: one of six tasks, made once. */
function defaultRun(): StubRun {
  return (shared ??= runInfo({ tasks: 6 }))
}

function taskOf(run: StubRun, id: string | undefined, state: string): StubTask {
  const t = id ? run.tasks.find((x) => x.id === id) : run.tasks.find((x) => x.state === state)
  if (!t) throw new Error(`inbox-stub: run ${run.id} has no task ${id ?? `in state ${state}`}`)
  return t
}

/**
 * An item of `kind`, shaped as the server makes it, each with a ref of its own, waiting five minutes: a question a session asks (three
 * choices and the reply line), a plan revision waiting for approval (or `stale`), a task out of its attempts (Retry, Accept anyway,
 * Check again), an agent's approval open (or a clarification, or a plan's gate, or answered). `over` sets any field it names.
 * Refuses, naming the field, an item past one of the server's bounds: a title, a brief, a question, an answerer, a ref, more actions
 * than the server keeps or a label longer than it keeps.
 */
export function inboxItem(kind: InboxKind, over: ItemOver = {}): StubItem {
  const n = ++made
  const since = new Date(Date.now() - 5 * 60_000).toISOString()
  let base: StubItem
  switch (kind) {
    case 'question':
      base = { ref: `question:${hex(0xa000 + n)}/${hex(0xb000 + n)}`, kind, since, title: `Asker (e2e stub) in stub-${n} asks`, brief: `Asker (e2e stub) in stub-${n} asks`, question: 'Which database?', answerer: ANSWERER, choices: ['Postgres', 'SQLite', 'Keep both'] }
      break
    case 'plan':
      base = { ref: `plan:STB-${n}/1`, kind, since, title: `STB-${n} revision 1: A stub ticket`, brief: `Revision 1 of STB-${n} (A stub ticket) waits for the operator's approval: crew Stub crew at base ${short('a'.repeat(40))}`, question: '', answerer: ANSWERER, choices: [] }
      if (over.stale) base.brief = `Revision 1 of STB-${n} (A stub ticket) was approved; the approval no longer holds: the description changed since revision 1 was approved`
      break
    case 'task': {
      const run = over.run ?? defaultRun()
      const t = taskOf(run, over.task, 'failed')
      base = { ref: `task:${run.id}/${t.id}`, kind, since: t.attempt?.endedAt ?? since, title: `${t.id} ${t.state} in run ${run.name}`, brief: `Task ${t.id} of run ${run.name} is ${t.state} after ${t.attempts} attempts${t.reason ? `: ${t.reason}` : ''}`, question: '', answerer: ANSWERER, choices: [], run, task: t.id }
      break
    }
    case 'decision': {
      const run = over.run ?? defaultRun()
      if (over.origin === 'compare') {
        // Yardmaster's selection, titled by its question and briefed from the run (inbox_decisions.go names no task or gate for it).
        const id = `dr-${900 + n}`
        const cands = over.candidates ?? CANDIDATES
        const question = `Which approach does STB-${n} continue with?`
        base = { ref: `decision:${id}`, kind, since, title: question, brief: `A decision for you, from the trial of run ${run.name}`, question, answerer: ANSWERER, choices: [...cands.map((c) => `${c.task} at ${c.commit.slice(0, 7)}`), 'None of these'], state: 'open', type: 'approval', origin: 'compare', run, candidates: cands }
        break
      }
      const plan = over.origin === 'plan'
      const t = taskOf(run, over.task, plan ? 'ready' : 'running')
      const id = `dr-${900 + n}`
      const type = over.type ?? 'approval'
      const question = type === 'approval' ? `May ${t.id} commit its change?` : `Which name should ${t.id} take?`
      // In a person's words, as inbox_decisions.go's decisionItem says it: titled by its question, briefed by where it comes from.
      const from = plan ? `the plan of run ${run.name}` : `Worker, CLI (e2e stub) in attempt ${t.id}.${t.attempt?.n ?? 1} of run ${run.name}`
      base = { ref: `decision:${id}`, kind, since, title: question, brief: `A decision for you, from ${from}`, question, answerer: ANSWERER, choices: type === 'approval' ? ['Approve', 'Reject'] : ['stub-a', 'stub-b'], state: 'open', type, origin: plan ? 'plan' : 'agent', run, task: t.id }
      break
    }
  }
  const it = { ...base, ...over }
  refuseOutOfBounds(it)
  return it
}

/** Throws, naming the field, for an item the server never sends: one past a bound it keeps. */
function refuseOutOfBounds(it: StubItem): void {
  const past = (field: string, n: number, bound: number) => {
    if (n > bound) throw new Error(`inbox-stub: ${it.ref}'s ${field} is ${n} bytes, past the server's ${bound}`)
  }
  past('ref', jsonLen(it.ref), BOUNDS.ref)
  past('title', jsonLen(it.title), BOUNDS.title)
  past('brief', jsonLen(it.brief), BOUNDS.brief)
  past('question', Buffer.byteLength(it.question), BOUNDS.question)
  past('answerer', jsonLen(it.answerer), BOUNDS.answerer)
  const actions = wholeItem(it).actions as Array<{ label: string }>
  if (actions.length > BOUNDS.actions) throw new Error(`inbox-stub: ${it.ref}'s actions are ${actions.length}, past the server's ${BOUNDS.actions}`)
  for (const a of actions) past(`action label "${a.label.slice(0, 20)}…"`, jsonLen(a.label), BOUNDS.label)
}

/** The keys a choice is typed as, in a question action's scope (inbox.go's keysWords). */
const keysWords = (label: string) => `${JSON.stringify(label)} and Enter`

/** The item whole, as GET /api/inbox/item gives it (inbox.go's questionItem, planItem, taskItem; inbox_decisions.go's decisionItem). */
export function wholeItem(it: StubItem): Record<string, unknown> {
  const common = { ref: it.ref, subject: it.ref, kind: it.kind }
  const head = { since: it.since, title: it.title, brief: it.brief, briefBy: 'conductor', question: it.question, answerer: it.answerer }
  let out: Record<string, unknown>
  switch (it.kind) {
    case 'question': {
      const [sess, qid] = it.ref.slice('question:'.length).split('/') as [string, string]
      const name = it.title.replace(/^.* in (.*) asks$/, '$1')
      const route = `/ws/sessions/${sess}`
      const actions: Array<Record<string, unknown>> = it.choices.map((label) => ({ name: 'answer', label, input: `${label}\r`, expect: qid, scope: `Types ${keysWords(label)} into the terminal of ${name}: the choice '${label}'`, route }))
      if (it.line ?? true) actions.push({ name: 'answer', label: 'Reply with a line', text: true, expect: qid, scope: `Types the line you write into the terminal of ${name} and enters it`, route })
      out = { ...common, ...head, revision: qid, blocking: { words: 'Asker (e2e stub) does nothing until it is answered', refs: null }, actions, recommended: -1, evidence: [{ ref: `session:${sess}`, label: `the session ${name}'s terminal` }] }
      break
    }
    case 'plan': {
      const [key, n] = it.ref.slice('plan:'.length).split('/') as [string, string]
      const plan = `${hex(0xc000 + Number(n))}`
      const ticketTitle = it.title.replace(/^.*?: /, '')
      out = {
        ...common,
        ...head,
        revision: '1',
        ...(it.stale ? { now: 'The approval no longer holds: the description changed since revision 1 was approved; approve again' } : {}),
        blocking: { words: `Nothing runs for ${key} until a revision is approved and launched`, refs: [`ticket:${key}`] },
        actions: [
          { name: 'approve', label: 'Approve', expect: '1', ticketRev: 1, scope: `Approves revision ${n} of ${key}: crew Stub crew with the tasks build and the agents worker-cli, at base aaaaaaaaaaaa, against the ticket at revision 1; yolo off: the agents ask before they act, as they do on their own; launching is a separate step on the ticket page`, route: `POST /api/dispatch/plans/${plan}/approve` },
          { name: 'withdraw', label: 'Withdraw', expect: '1', scope: `Withdraws revision ${n} of ${key} at its revision 1: it can no longer be approved or launched`, confirm: true, route: `POST /api/dispatch/plans/${plan}/withdraw` },
        ],
        recommended: -1,
        evidence: [
          { ref: `ticket:${key}`, label: `${key}: ${ticketTitle}` },
          { ref: it.ref, label: `revision ${n}` },
        ],
      }
      break
    }
    case 'task': {
      const run = it.run ?? defaultRun()
      const t = taskOf(run, it.task, 'failed')
      const a = t.attempt
      const rev = String(run.managed.revision)
      const deps = run.tasks.filter((x) => x.needs?.includes(t.id)).map((x) => x.id)
      const waits = deps.length ? `${deps.join(', ')} wait for it` : 'nothing else waits for it'
      const route = `POST /api/runs/${run.id}/tasks/${t.id}/`
      const actions: Array<Record<string, unknown>> = []
      for (const c of t.commands ?? []) {
        if (c === 'retry') actions.push({ name: 'retry', label: 'Retry', expect: rev, scope: `Allows ${t.id} one more attempt (attempt ${t.attempts + 1}), started at commit ${short(a?.result ?? '')} with what its last attempt found; ${waits}; at run revision ${rev}`, route: route + 'retry' })
        if (c === 'accept') actions.push({ name: 'accept', label: 'Accept anyway', expect: rev, scope: `Takes attempt ${a?.n}'s commit ${short(a?.result ?? '')} as ${t.id}'s result though it failed its checks; ${waits} and start from it; at run revision ${rev}`, confirm: true, route: route + 'accept' })
      }
      if ((it.recheck ?? true) && a?.result) actions.push({ name: 'recheck', label: 'Check again', expect: rev, scope: `Checks attempt ${a.n}'s commit ${short(a.result)} again as it is, nothing changed, in a new attempt not counted against ${t.id}; if it passes it is ${t.id}'s result and ${waits}; at run revision ${rev}`, route: route + 'recheck', attempt: a.n, commit: a.result })
      const evidence = [
        { ref: `run:${run.id}`, label: `the run ${run.name}` },
        { ref: it.ref, label: `the task ${t.id}` },
        ...(a ? [{ ref: `attempt:${run.id}/${t.id}/${a.n}`, label: `attempt ${a.n}` }, { ref: `check:${run.id}/${t.id}/${a.n}/never/1`, label: `check never failed (exit 1, ran native, log sha256 e3b0c44298fc)` }] : []),
        ...(a?.result ? [{ ref: `commit:${run.id}/${a.result}`, label: `the commit ${short(a.result)}` }] : []),
      ]
      const words = `${deps.length === 0 ? 'No task waits' : deps.length === 1 ? '1 task waits' : `${deps.length} tasks wait`} for ${t.id}, and the run does not end until it is decided`
      out = { ...common, ...head, revision: rev, blocking: { words, refs: [...deps.map((d) => `task:${run.id}/${d}`), `run:${run.id}`] }, actions, recommended: -1, evidence }
      break
    }
    case 'decision': {
      const run = it.run ?? defaultRun()
      const id = it.ref.slice('decision:'.length)
      if (it.origin === 'compare') {
        out = compareItem(it, run, id, common, head)
        break
      }
      const t = taskOf(run, it.task, it.origin === 'plan' ? 'ready' : 'running')
      const agent = it.origin !== 'plan'
      const n = t.attempt?.n ?? 1
      const respond = `POST /api/decisions/${id}/respond`
      const comment = `POST /api/decisions/${id}/comment`
      const applies = (choice: number | null) => (agent ? `Yardmaster then types the answer into attempt ${t.id}.${n}'s session` : choice === 1 ? `Yardmaster then cancels ${t.id}` : `Yardmaster then lets ${t.id} start`)
      const commentAction = { name: 'comment', label: 'Comment', text: true, expect: '1', scope: `Keeps your comment on ${id}'s record at its rev 1; nothing else changes, and its rev moves on`, route: comment }
      const actions: Array<Record<string, unknown>> = []
      if (it.state === 'answered') actions.push(commentAction)
      else {
        it.choices.forEach((label, i) => {
          const name = it.type !== 'clarification' && i < 2 ? ['approve', 'reject'][i]! : 'answer'
          actions.push({ name, label, choice: i, expect: '1', scope: `Answers ${id} '${label}' at its rev 1, as the operator: ${applies(i)}`, ...(it.type === 'clarification' ? {} : { confirm: true }), route: respond })
        })
        if (it.type === 'clarification') actions.push({ name: 'answer', label: 'Reply with words', text: true, expect: '1', scope: `Answers ${id} with the words you write at its rev 1, as the operator: ${applies(null)}`, route: respond })
        if (agent) actions.push({ name: 'more_info', label: 'Ask for more information', text: true, expect: '1', scope: `Types your question into Worker, CLI (e2e stub) in attempt ${t.id}.${n}'s session and keeps ${id} open; its rev moves past 1, so an answer made before it is refused stale`, route: comment })
        actions.push(commentAction)
      }
      const deps = run.tasks.filter((x) => x.needs?.includes(t.id)).map((x) => x.id)
      const evidence = [
        { ref: `run:${run.id}`, label: `the run ${run.name}` },
        ...(agent
          ? [
              { ref: `task:${run.id}/${t.id}`, label: `the task ${t.id}` },
              { ref: `attempt:${run.id}/${t.id}/${n}`, label: `attempt ${n}, which asked` },
              { ref: `session:${t.attempt?.sessionId ?? hex(1)}`, label: `the session ${t.member} · ${t.id} #${n}'s terminal` },
            ]
          : []),
      ]
      const words = `Yardmaster holds ${t.id} until ${id} is answered and applied${it.state === 'answered' ? '; it is answered, and what it holds waits for Yardmaster to apply it' : ''}`
      out = {
        ...common,
        state: it.state ?? 'open',
        ...head,
        revision: '1',
        ...(it.state === 'answered' ? { now: `${id} is answered and not yet applied: Yardmaster applies it next` } : {}),
        blocking: { words, refs: [`task:${run.id}/${t.id}`, ...deps.map((d) => `task:${run.id}/${d}`), `run:${run.id}`] },
        actions,
        recommended: -1,
        evidence,
        delivery: [
          { step: 'available', state: 'done', at: it.since, words: `asked: ${id} was opened`, receipt: it.ref },
          it.state === 'answered' ? { step: 'recorded', state: 'done', at: it.since, words: 'answered', receipt: `${id}/1` } : { step: 'recorded', state: 'waiting', words: "waits for the operator's answer" },
          { step: 'validated', state: 'waiting', words: it.state === 'answered' ? 'Yardmaster applies it next' : 'not yet: nothing is answered' },
          { step: 'applied', state: 'waiting', words: 'not yet' },
        ],
      }
      break
    }
  }
  return { ...out, ...it.item }
}

/** The item as GET /api/inbox lists it: folded from the whole item as the server folds it (inbox.go, summary). */
export function summary(it: StubItem): Record<string, unknown> {
  const w = wholeItem(it) as { blocking: { refs: string[] | null; more?: number }; actions: Array<{ label: string }>; routedTo?: unknown[]; card?: string; state?: string }
  const refs = w.blocking.refs ?? []
  const blocks = refs.length + (w.blocking.more ?? 0) - refs.filter((r) => r.startsWith('run:')).length
  return {
    ref: it.ref,
    kind: it.kind,
    since: it.since,
    title: fitText(it.title, BOUNDS.title),
    brief: fitText(it.brief, BOUNDS.summaryBrief),
    question: fitText(it.question, BOUNDS.summaryQuestion),
    answerer: fitText(it.answerer, BOUNDS.answerer),
    ...(w.routedTo?.length ? { routed: w.routedTo.length } : {}),
    ...(blocks ? { blocks } : {}),
    actions: w.actions.map((a) => fitText(a.label, BOUNDS.label)),
    ...(w.card ? { card: true } : {}),
    ...(w.state ? { state: w.state } : {}),
    ...(w.state === 'answered' ? { tracking: true } : {}),
  }
}

/**
 * A selection's item as inbox_decisions.go makes an approval with no task held: an action per option at its rev (the first two named
 * approve and reject, as the server names an approval's), each confirmed, then a comment; nothing held but the run.
 */
function compareItem(it: StubItem, run: StubRun, id: string, common: Record<string, unknown>, head: Record<string, unknown>): Record<string, unknown> {
  const respond = `POST /api/decisions/${id}/respond`
  const commentAction = { name: 'comment', label: 'Comment', text: true, expect: '1', scope: `Keeps your comment on ${id}'s record at its rev 1; nothing else changes, and its rev moves on`, route: `POST /api/decisions/${id}/comment` }
  const actions: Array<Record<string, unknown>> = []
  if (it.state !== 'answered')
    it.choices.forEach((label, i) => actions.push({ name: i < 2 ? ['approve', 'reject'][i]! : 'answer', label, choice: i, expect: '1', confirm: true, route: respond, scope: `Answers ${id} '${label}' at its rev 1, as the operator: Yardmaster then notes it; nothing is held for it; what it authorizes: ${SELECTION_AUTHORIZES}` }))
  actions.push(commentAction)
  return {
    ...common,
    state: it.state ?? 'open',
    ...head,
    revision: '1',
    blocking: { words: `Nothing is held for ${id}`, refs: [`run:${run.id}`] },
    actions,
    recommended: -1,
    evidence: [{ ref: `run:${run.id}`, label: `the run ${run.name}` }],
  }
}

/** A selection's record (docs/plans/compare-crew.md, PR-6a): its options the candidates with their subjects, then None of these. */
function compareRecord(it: StubItem, whole: boolean): Record<string, unknown> {
  const id = it.ref.slice('decision:'.length)
  const run = it.run ?? defaultRun()
  const cands = it.candidates ?? CANDIDATES
  const options = [
    ...cands.map((c) => ({ label: `${c.task} at ${c.commit.slice(0, 7)}`, tradeoffs: 'checks 2/2 passed, comparable; 12 files, +340 −90; no dependency change; independent', subject: { kind: 'candidate', run: run.id, task: c.task, n: c.n, commit: c.commit } })),
    { label: 'None of these', tradeoffs: 'Ends the trial with no candidate selected.' },
  ]
  const request = { question: it.question, context: 'Two candidates from one base, checked alike.', options, recommendation: -1, authorizes: SELECTION_AUTHORIZES, subject: { kind: 'comparison', run: run.id, base: 'c'.repeat(40), digest: 'd'.repeat(64) } }
  const opened = { seq: 1, at: it.since, kind: 'opened', by: 'yardmaster' }
  const routed = { seq: 2, at: it.since, kind: 'routed', by: 'local/1', detail: { by: 'policy', rule: 'local/1: the operator answers', to: [{ kind: 'operator' }] } }
  return {
    id,
    run: run.id,
    openedIn: run.id,
    origin: 'compare',
    type: 'approval',
    request,
    policy: { revision: 'local/1', approver: 'operator', quorum: 1, authority: [{ kind: 'operator' }] },
    state: it.state ?? 'open',
    rev: 1,
    ...(whole ? { history: [opened, routed] } : {}),
    createdAt: it.since,
    ...it.decision,
  }
}

/** A decision item's record, as GET /api/decisions lists it (and /api/decisions/{id} gives it, with its history): its run the item's. */
export function decisionRecord(it: StubItem, whole = false): Record<string, unknown> {
  if (it.origin === 'compare') return compareRecord(it, whole)
  const id = it.ref.slice('decision:'.length)
  const run = it.run ?? defaultRun()
  const t = taskOf(run, it.task, it.origin === 'plan' ? 'ready' : 'running')
  const agent = it.origin !== 'plan'
  const n = t.attempt?.n ?? 1
  const request = {
    question: it.question,
    options: it.choices.map((label) => ({ label })),
    subject: agent ? { kind: 'commit', commit: 'a'.repeat(40) } : { kind: 'plan', plan: hex(0xd000), digest: 'b'.repeat(64) },
  }
  const opened = { seq: 1, at: it.since, kind: 'opened', by: agent ? t.member : 'plan', detail: agent ? { attempt: n, task: t.id } : { before: [t.id], gate: 'gate' } }
  const routed = { seq: 2, at: it.since, kind: 'routed', by: 'local/1', detail: { by: 'policy', rule: 'local/1: the operator answers', to: [{ kind: 'operator' }] } }
  return {
    id,
    run: run.id,
    openedIn: run.id,
    origin: agent ? 'agent' : 'plan',
    type: it.type ?? 'approval',
    ...(agent ? { asker: { task: t.id, attempt: n, live: true }, member: t.member, requestId: `stub-${t.id}-${n}` } : { gate: 'gate', ticket: hex(0xe000), plan: hex(0xd000), planDigest: 'b'.repeat(64) }),
    request,
    policy: { revision: 'local/1', approver: 'operator', quorum: 1, authority: [{ kind: 'operator' }] },
    state: it.state ?? 'open',
    rev: 1,
    ...(whole ? { history: [opened, routed] } : {}),
    createdAt: it.since,
    ...it.decision,
  }
}

/** The four steps GET /api/inbox/track gives for an action on the item whose receipt the server keeps none of (inbox_track.go). */
function noReceipt(it: StubItem, receipt: string): Record<string, unknown>[] | string {
  const id = /^[A-Za-z0-9._~-]{1,32}$/
  switch (it.kind) {
    case 'question': {
      const qid = it.ref.split('/')[1]!
      if (receipt !== qid) return "a question's receipt is its own id; a task command carried by its item is tracked under the action's track ref"
      const waits = (step: string) => ({ step, state: 'waiting', words: 'not answered yet' })
      return [{ step: 'available', state: 'done', at: it.since, words: 'the session asked it', receipt: it.ref }, waits('recorded'), waits('validated'), waits('resumed')]
    }
    case 'decision': {
      const m = /^(response|comment):(.*)$/.exec(receipt)
      if (!m || !id.test(m[2]!)) return 'receipt is response:<responseId> or comment:<opId>'
      const none = m[1] === 'comment' ? 'the record keeps no comment of yours under this id' : 'the record keeps no response of yours under this id'
      return [{ step: 'available', state: 'done', at: it.since, words: `asked: ${it.ref.slice('decision:'.length)} was opened`, receipt: it.ref }, ...['recorded', 'validated', 'applied'].map((step) => ({ step, state: 'unknown', words: none }))]
    }
    case 'task': {
      if (!id.test(receipt)) return "receipt is the command's id"
      const [run, task] = it.ref.slice('task:'.length).split('/')
      const none = 'no receipt of this command is kept here'
      return [{ step: 'available', state: 'done', words: `asked of task ${task} of run ${run}` }, ...['recorded', 'validated', 'resumed'].map((step) => ({ step, state: 'unknown', words: none }))]
    }
    case 'plan': {
      if (!id.test(receipt)) return "receipt is the operation's id"
      const none = 'no receipt of this operation is kept here'
      return [{ step: 'available', state: 'done', words: 'the revision was open for the operator' }, ...['recorded', 'validated', 'resumed'].map((step) => ({ step, state: 'unknown', words: none }))]
    }
  }
}

/** The list's order (inbox.go, before): oldest first, by (since, ref). */
function inOrder(items: StubItem[]): StubItem[] {
  return [...items].sort((a, b) => Date.parse(a.since) - Date.parse(b.since) || (a.ref < b.ref ? -1 : a.ref > b.ref ? 1 : 0))
}

export interface InboxStub {
  /** The items answered from now on (refused past the server's page, as at the start). */
  set(items: StubItem[]): void
  /** The reads the stub answered, by path and query. */
  readonly reads: string[]
  /** The reads it did not answer, each gone on to the server (or an earlier route): a track or a decision of an item it does not hold. */
  readonly unanswered: string[]
}

const json = (route: Route, status: number, body: unknown) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
const refusal = (code: string, message: string) => ({ error: { code, message } })

/**
 * Answers the page's reads of the inbox and the decisions from `items`, as the server does: GET /api/inbox (the list, oldest first,
 * with how many are answered and not yet applied), /api/inbox/item (an item whole; 404 not_found for a ref it does not hold, recorded),
 * /api/inbox/track (an action's four steps for the item's kind, of a receipt the stub keeps none of; a receipt the server would refuse
 * is refused 400 as it refuses it), /api/decisions (the decision items' records, by the asked state) and /api/decisions/{id} (a record
 * with its history). A track of an item it does not hold, and a decision it does not hold, are recorded and go on to the server, never
 * answered with a refusal the server would not send. Refuses, at the start and at `set`, a list past the server's page. Set before the
 * page loads.
 */
export async function stubInbox(page: Page, items: StubItem[]): Promise<InboxStub> {
  const page150 = (list: StubItem[]) => {
    if (list.length > BOUNDS.page) throw new Error(`inbox-stub: ${list.length} items, past the server's page of ${BOUNDS.page}`)
    return inOrder(list)
  }
  let held = page150(items)
  const reads: string[] = []
  const unanswered: string[] = []
  const find = (ref: string | null) => held.find((it) => it.ref === ref)
  await page.route(
    (u) => u.pathname === '/api/inbox' || u.pathname === '/api/inbox/item' || u.pathname === '/api/inbox/track' || u.pathname === '/api/decisions' || u.pathname.startsWith('/api/decisions/'),
    (route) => {
      const req = route.request()
      if (req.method() !== 'GET') return route.fallback()
      const u = new URL(req.url())
      const read = u.pathname + u.search
      const answer = (status: number, body: unknown) => {
        reads.push(read)
        return json(route, status, body)
      }
      const pass = () => {
        unanswered.push(read)
        return route.fallback()
      }
      if (u.pathname === '/api/inbox') {
        const tracking = held.filter((it) => it.kind === 'decision' && it.state === 'answered').length
        return answer(200, { items: held.map(summary), total: held.length, ...(tracking ? { tracking } : {}), at: new Date().toISOString() })
      }
      if (u.pathname === '/api/inbox/item') {
        const it = find(u.searchParams.get('ref'))
        if (it) return answer(200, { item: wholeItem(it) })
        unanswered.push(read)
        return json(route, 404, refusal('not_found', 'no such item'))
      }
      if (u.pathname === '/api/inbox/track') {
        const it = find(u.searchParams.get('ref'))
        if (!it) return pass()
        const receipt = u.searchParams.get('receipt') ?? ''
        const steps = noReceipt(it, receipt)
        return typeof steps === 'string' ? answer(400, refusal('invalid_request', steps)) : answer(200, { ref: it.ref, receipt, steps })
      }
      const decisions = held.filter((it) => it.kind === 'decision')
      if (u.pathname === '/api/decisions') {
        const state = u.searchParams.get('state')
        return answer(200, { decisions: decisions.map((it) => decisionRecord(it)).filter((d) => !state || d.state === state).reverse() })
      }
      const id = decodeURIComponent(u.pathname.slice('/api/decisions/'.length))
      const it = decisions.find((d) => d.ref === `decision:${id}`)
      return it ? answer(200, { decision: decisionRecord(it, true), refs: [] }) : pass()
    },
  )
  return {
    set: (items) => {
      held = page150(items)
    },
    reads,
    unanswered,
  }
}
