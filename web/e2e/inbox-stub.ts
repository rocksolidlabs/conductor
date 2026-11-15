import type { Page, Route } from '@playwright/test'

// The inbox answered in the browser (UX1's PR-1, docs/plans/ux-review.md, "Stubs in the browser"): for the shapes the day cannot make
// exactly or cheaply (a title of 200 characters, an item answered and not yet applied, a read that fails). A stub proves how a shape
// is drawn, never that the server sends it; stubs.spec.ts holds every item kind to the keys the day's real items carry, so that a
// field the server gains fails there, not in a layout spec. Modelled on the list the server gives (internal/api/inbox.go).

export type InboxKind = 'question' | 'plan' | 'task' | 'decision'

/** An item as GET /api/inbox lists it (inboxSummary), with what the stub answers its other reads from. */
export interface StubItem {
  ref: string
  kind: InboxKind
  since: string
  title: string
  brief: string
  question: string
  answerer: string
  actions: string[]
  blocks?: number
  routed?: number
  card?: boolean
  state?: string
  tracking?: boolean
  /** Fields over the item GET /api/inbox/item gives (its subject, actions, blocking and the rest are made from the summary). */
  item?: Record<string, unknown>
  /** A decision's record: fields over the one GET /api/decisions and /api/decisions/{id} give. */
  decision?: Record<string, unknown>
}

let made = 0

/** The keys of a StubItem the stub keeps for its other reads, never listed. */
const OWN = ['item', 'decision']

/**
 * An item of `kind`, shaped as the server's (a question asked by a session, a plan revision waiting for approval, a task out of its
 * attempts, an agent's decision open), each with a ref of its own, waiting five minutes; `over` sets any field.
 */
export function inboxItem(kind: InboxKind, over: Partial<StubItem> = {}): StubItem {
  const n = ++made
  const since = new Date(Date.now() - 5 * 60_000).toISOString()
  const base: StubItem =
    kind === 'question'
      ? { ref: `question:${hex(n)}/${hex(n + 1000)}`, kind, since, title: `Asker (e2e stub) in stub-${n} asks`, brief: `Asker (e2e stub) in stub-${n} asks`, question: 'Which database?', answerer: 'you, the operator', actions: ['Postgres', 'SQLite', 'Keep both', 'Reply with a line'] }
      : kind === 'plan'
        ? { ref: `plan:STB-${n}/1`, kind, since, title: `STB-${n} revision 1: A stub ticket`, brief: `Revision 1 of STB-${n} (A stub ticket) waits for the operator's approval`, question: '', answerer: 'you, the operator', blocks: 1, actions: ['Approve', 'Withdraw'] }
        : kind === 'task'
          ? { ref: `task:stub-run-${hex(n)}/build`, kind, since, title: 'build failed in run Stub run', brief: 'Task build of run Stub run is failed after 3 attempts: attempt 3 failed at check never: exited 1', question: '', answerer: 'you, the operator', actions: ['Retry', 'Accept anyway', 'Check again'] }
          : { ref: `decision:dr-${900 + n}`, kind, since, title: `Approval dr-${900 + n}: May build commit its change?`, brief: `Worker in attempt build.1 asks dr-${900 + n}, a durable decision of run Stub run`, question: 'May build commit its change?', answerer: 'you, the operator', blocks: 1, actions: ['Approve', 'Reject', 'Ask for more information', 'Comment'], state: 'open' }
  return { ...base, ...over }
}

function hex(n: number): string {
  return n.toString(16).padStart(16, '0')
}

/** The item as GET /api/inbox lists it: the stub's own keys left out. */
export function summary(it: StubItem): Record<string, unknown> {
  return Object.fromEntries(Object.entries(it).filter(([k]) => !OWN.includes(k)))
}

/** The action names of a kind, by its labels' place. */
const NAMES: Record<InboxKind, string[]> = {
  question: ['answer', 'answer', 'answer', 'answer'],
  plan: ['approve', 'withdraw'],
  task: ['retry', 'accept', 'recheck'],
  decision: ['approve', 'reject', 'more_info', 'comment'],
}

/** The item whole, as GET /api/inbox/item gives it. */
export function wholeItem(it: StubItem): Record<string, unknown> {
  return {
    ref: it.ref,
    subject: it.ref,
    kind: it.kind,
    ...(it.state ? { state: it.state } : {}),
    since: it.since,
    title: it.title,
    brief: it.brief,
    briefBy: 'conductor',
    question: it.question,
    answerer: it.answerer,
    revision: '1',
    blocking: { words: '', refs: [] },
    actions: it.actions.map((label, i) => ({ name: NAMES[it.kind][i] ?? 'answer', label, expect: '1', scope: `${label}, as the stub says`, route: 'POST /api/stub', ...(it.kind === 'decision' && i < 2 ? { choice: i } : {}) })),
    recommended: -1,
    ...it.item,
  }
}

/** A decision item's record, as GET /api/decisions lists it and /api/decisions/{id} gives it. */
export function decisionRecord(it: StubItem): Record<string, unknown> {
  const id = it.ref.slice('decision:'.length)
  return {
    id,
    run: 'stub-run-00000001',
    openedIn: 'stub-run-00000001',
    origin: 'agent',
    type: 'approval',
    request: { question: it.question, options: it.actions.slice(0, 2).map((label) => ({ label })) },
    policy: {},
    state: it.state ?? 'open',
    rev: 1,
    createdAt: it.since,
    ...it.decision,
  }
}

export interface InboxStub {
  /** The items answered from now on. */
  set(items: StubItem[]): void
  /** The reads the stub answered, by path and query. */
  readonly reads: string[]
}

const json = (route: Route, status: number, body: unknown) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })

/**
 * Answers the page's reads of the inbox and the decisions from `items`: GET /api/inbox (the list), /api/inbox/item (an item whole,
 * 404 for a ref not held), /api/inbox/track (no steps yet), /api/decisions (the decision items' records, by the asked state) and
 * /api/decisions/{id}. Set before the page loads.
 */
export async function stubInbox(page: Page, items: StubItem[]): Promise<InboxStub> {
  let held = items
  const reads: string[] = []
  const find = (ref: string | null) => held.find((it) => it.ref === ref)
  await page.route(
    (u) => u.pathname === '/api/inbox' || u.pathname === '/api/inbox/item' || u.pathname === '/api/inbox/track' || u.pathname === '/api/decisions' || u.pathname.startsWith('/api/decisions/'),
    (route) => {
      const req = route.request()
      if (req.method() !== 'GET') return route.fallback()
      const u = new URL(req.url())
      reads.push(u.pathname + u.search)
      if (u.pathname === '/api/inbox') {
        return json(route, 200, { items: held.map(summary), total: held.length, ...(held.some((it) => it.tracking) ? { tracking: held.filter((it) => it.tracking).length } : {}), at: new Date().toISOString() })
      }
      if (u.pathname === '/api/inbox/item') {
        const it = find(u.searchParams.get('ref'))
        return it ? json(route, 200, { item: wholeItem(it) }) : json(route, 404, { error: { code: 'not_found', message: 'no such item' } })
      }
      if (u.pathname === '/api/inbox/track') return json(route, 200, { ref: u.searchParams.get('ref'), receipt: u.searchParams.get('receipt') ?? '', steps: [] })
      const records = held.filter((it) => it.kind === 'decision').map(decisionRecord)
      if (u.pathname === '/api/decisions') {
        const state = u.searchParams.get('state')
        return json(route, 200, { decisions: records.filter((d) => !state || d.state === state) })
      }
      const d = records.find((r) => r.id === decodeURIComponent(u.pathname.slice('/api/decisions/'.length)))
      return d ? json(route, 200, { decision: d, refs: [] }) : json(route, 404, { error: { code: 'not_found', message: 'no such decision' } })
    },
  )
  return {
    set: (items) => {
      held = items
    },
    reads,
  }
}
