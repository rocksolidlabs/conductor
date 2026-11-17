import { existsSync, readdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { randomBytes } from 'node:crypto'
import { expect } from '@playwright/test'
import { budget, DAY_RUNS_END_MS, DAY_SESSIONS_END_MS, type Api, type Run, type RunTask, type Session } from './fixtures'
import { ownServer, type AgentCopy, type OwnServer } from './own-server'

// The seeded day (UX1's PR-1, docs/plans/ux-review.md, the chapter Tests): a day of work on a server of its own, built in named parts
// on demand and read by name, so that a spec can assert exact numbers. A part builds the parts it stands on first, once; each of its
// waits polls the API for what it made and raises the running test's timeout by its cap (budget). No state is held on a timer: the
// working and finished sessions are the stub's STUB_TURN, a question is the asker's, a failed task's check exits 1, and an exited
// session is kept listed for a day (CONDUCTOR_EXITED_RETENTION). The day is read-only, and the guard has one rule: what the day's
// server lists (snapshot) is the same at a test's end as the day's start and its parts' builds left it. The `day` fixture
// (fixtures.ts) fails a test naming each difference (drift), and ends the sessions and runs it added (guard).

/** The parts, as the chapter Tests lists them. */
export type PartName = 'states' | 'dispatch' | 'waiting' | 'runs' | 'long' | 'busy'
/** A part's names (a session's, a run's, a ticket's key, a decision's role) mapped to the ids the server gave them. */
export type Names = Record<string, string>

/** One wait's cap in a part: an attention shown, a decision opened, a launch answered (its route's bound is 2 min). */
export const DAY_STEP_MS = 30_000
/** The cap of a wait on a run's attempts: Paging spike's three failed attempts (decision-page.spec.ts's bound). */
export const DAY_RUN_MS = 180_000
/** The cap of a launch from a ticket: the route answers once the run's first ledger transaction is committed, within 2 min. */
export const DAY_LAUNCH_MS = 130_000

/** The live sessions `busy` holds, its own and every other part's together. */
export const BUSY_LIVE = 18

/** The repository's path under the server's root: about as long as a real checkout's. */
export const DAY_REPO = 'src/github.com/rocksolidlabs/conductor'

/** The `long` part's lengths: a session's name at its bound (proto.MaxSessionName, bytes), a run's label at ValidateLabel's, a title at Dispatch's. */
export const LONG_NAME = 'reconcile-the-ledger-after-the-schema-5-cutover-'.repeat(3).slice(0, 120)
export const LONG_LABEL = 'Reconcile the ledger after the schema 5 cutover on each host'
export const LONG_TITLE =
  'Make every page of the workbench say what it holds, why it is empty and the one action that fills it, at 1440, 1280 and 390 wide, in both themes, with the whole text one press away where it is cut off'

/** The copies of the catalog's agents the day runs: a session's state is fixed by its agent, never by what a browser typed. */
export const DAY_AGENTS: AgentCopy[] = [
  { id: 'day-asker', from: 'asker', env: {} },
  { id: 'day-asker-long', from: 'asker-long', env: {} },
  { id: 'day-working', from: 'claude', env: { STUB_TURN: 'working' } },
  { id: 'day-finished', from: 'claude', env: { STUB_TURN: 'finished' } },
  { id: 'day-member-working', from: 'worker-cli', env: { STUB_TURN: 'working' } },
  { id: 'day-clarifier', from: 'worker-cli', env: { STUB_DECISION: 'clarification' } },
  { id: 'day-approver', from: 'worker-cli', env: { STUB_DECISION: 'approval' } },
]

/**
 * What a session of the day promises: its status, its attention's state ('' for none), and whether its agent reports its turns (the
 * listing's `turns`, which the status walk reads: working and finished need it, running is an agent that reports none; not held
 * for a question, which needs you whatever its agent tells).
 */
interface Promised {
  status: string
  attention: string
  turns?: boolean
}
const ASKS: Promised = { status: 'running', attention: 'needs_input' }
const WORKING: Promised = { status: 'running', attention: 'working', turns: true }
const FINISHED: Promised = { status: 'running', attention: 'done', turns: true }
const SILENT: Promised = { status: 'running', attention: '', turns: false }
const EXITED: Promised = { status: 'stopped', attention: '', turns: false }

/** The `states` part's five loose sessions, one in each state of the plan's status table. */
export const STATES: Record<string, { agent: string; promise: Promised }> = {
  'mac-only-flake': { agent: 'day-asker-long', promise: ASKS },
  'yard-tile-resize': { agent: 'day-working', promise: WORKING },
  'flaky-vim-spec': { agent: 'day-finished', promise: FINISHED },
  driver: { agent: 'plain', promise: SILENT },
  'old-bisect': { agent: 'plain', promise: EXITED },
}

/** The `busy` part's loose sessions: with every other part's live sessions, BUSY_LIVE in a fixed mix of working and finished. */
export const BUSY: Record<string, 'working' | 'finished'> = {
  'lint-the-tree': 'working',
  'bump-node-24': 'finished',
  'proto-size-limits': 'working',
  'docs-checks-day': 'finished',
  'rail-squares-light': 'working',
}

/**
 * Dispatch's people, labels, milestones and tickets on the day: CND's fourteen tickets with their owners, assignees and priorities, one
 * of them with no owner ('' here), so that a list's owner column has a cell drawn empty.
 */
export const PEOPLE = ['Nate', 'Ana', 'Priya']
const LABELS: Array<[string, string]> = [['bug', 'red'], ['ui', 'blue'], ['api', 'teal'], ['docs', 'gray'], ['perf', 'amber'], ['infra', 'indigo']]
const MILESTONES = ['v1', 'v2']
export interface DayTicket {
  title: string
  owner: string
  assignees: string[]
  priority: number
  status: string
  labels: string[]
  milestone?: string
}
export const TICKETS: DayTicket[] = [
  { title: 'Pair a phone with a session token', owner: 'Nate', assignees: ['Nate'], priority: 1, status: 'ready', labels: ['api'], milestone: 'v1' },
  { title: "Show a run's tasks as a table", owner: 'Ana', assignees: ['Ana'], priority: 2, status: 'backlog', labels: ['ui'], milestone: 'v1' },
  { title: 'Migrate the ledger to schema 5', owner: 'Priya', assignees: ['Priya', 'Nate'], priority: 1, status: 'ready', labels: ['infra'], milestone: 'v1' },
  { title: 'Write the release notes', owner: 'Nate', assignees: [], priority: 3, status: 'ready', labels: ['docs'], milestone: 'v1' },
  { title: 'Fix the flaky vim spec', owner: 'Ana', assignees: ['Ana'], priority: 2, status: 'ready', labels: ['bug'] },
  { title: "Index the paging spike's results", owner: 'Priya', assignees: ['Priya'], priority: 2, status: 'in_progress', labels: ['perf'] },
  { title: "Resize the Yard's tiles", owner: 'Nate', assignees: ['Ana'], priority: 3, status: 'in_progress', labels: ['ui'], milestone: 'v2' },
  { title: 'Bisect the old resume failure', owner: 'Ana', assignees: [], priority: 4, status: 'backlog', labels: ['bug'] },
  { title: 'Drive the stub through the trust dialog', owner: 'Priya', assignees: ['Priya'], priority: 3, status: 'in_review', labels: [] },
  { title: 'The Mac-only flake in the PTY tests', owner: 'Nate', assignees: ['Nate', 'Priya'], priority: 1, status: 'in_progress', labels: ['bug', 'infra'] },
  { title: 'Cut the e2e job into two shards', owner: '', assignees: [], priority: 3, status: 'backlog', labels: ['infra'], milestone: 'v2' },
  { title: 'Contrast floors for dimmed text', owner: 'Priya', assignees: ['Ana'], priority: 2, status: 'ready', labels: ['ui'], milestone: 'v2' },
  { title: 'One status language for sessions', owner: 'Nate', assignees: ['Nate'], priority: 1, status: 'in_review', labels: ['ui'], milestone: 'v1' },
  { title: 'Document the seeded day', owner: 'Ana', assignees: ['Ana'], priority: 4, status: 'done', labels: ['docs'] },
]

/** A run's crew as POST /api/crews takes it. */
function crew(name: string, members: Array<{ name: string; agentId: string; when?: string }>, tasks?: Array<Record<string, unknown>>): Record<string, unknown> {
  return {
    name,
    goal: name,
    cwd: '',
    where: 'server',
    isolation: tasks ? 'worktree' : 'none',
    openAfterLaunch: false,
    yolo: null,
    members: members.map((m) => ({ name: m.name, agentId: m.agentId, prompt: '', start: { when: m.when ?? (tasks ? 'task' : 'immediately') } })),
    ...(tasks ? { tasks } : {}),
  }
}

/** A session or a run on the day's server that no part made, as guard() names it: the `day` fixture ends it. */
export interface Leftover {
  kind: 'session' | 'run'
  id: string
  words: string
}

interface Part {
  needs: PartName[]
  build: (d: Day) => Promise<Names>
}

function listed<T>(r: { sessions?: T[] } | T[]): T[] {
  return Array.isArray(r) ? r : (r.sessions ?? [])
}

/**
 * The kinds of what the day's server lists, as snapshot() keys them (`<kind> <its id>`): each kind's change has its proof in
 * day.spec.ts's table, which fails on a kind without one.
 */
export const SNAPSHOT_KINDS = ['person', 'project', 'label', 'milestone', 'view', 'ticket', 'plan', 'crew', 'share link', 'session', 'run', 'inbox'] as const
export type SnapshotKind = (typeof SNAPSHOT_KINDS)[number]

/**
 * What the day's server lists, each item under a key that names it (`session <id>`, `ticket CND-2`) with the JSON of what is compared.
 * Left out, as what moves by itself (docs/checks.md): every time (since, createdAt, endedAt, startedAt, stoppedAt) and a run's log;
 * a session's viewers, cols and rows, which every page attached moves; a member's diff, which the server counts again in the
 * background (it moved within a minute on an idle day); and what follows a field compared: a run's needsInput and ledger revision,
 * a member's attempt, branch and worktree.
 */
export type Snapshot = Map<string, string>

/** An item's label in what the guard says: its key, and its name when it has one (`session 3cb0 (flaky-vim-spec)`). */
function named(key: string, v: string): string {
  const name = (JSON.parse(v) as { name?: unknown }).name
  return typeof name === 'string' ? `${key} (${name})` : key
}

/** An item's compared fields, a nested object's flattened to dotted names (`attention.source`, `tasks.ship.holds.dr-2`). */
function fields(v: unknown, at = '', out: Record<string, string> = {}): Record<string, string> {
  if (v && typeof v === 'object' && !Array.isArray(v)) for (const [k, x] of Object.entries(v)) fields(x, at ? `${at}.${k}` : k, out)
  else out[at] = JSON.stringify(v)
  return out
}

/** Every page of a list read by `next` (a cursor), as Dispatch pages them. */
async function pages<T>(api: Api, path: string, list: (r: Record<string, unknown>) => T[]): Promise<T[]> {
  const out: T[] = []
  let next = ''
  do {
    const r = await api.ok<Record<string, unknown>>('GET', `${path}${next ? `&cursor=${encodeURIComponent(next)}` : ''}`)
    out.push(...list(r))
    next = (r.next as string | undefined) ?? ''
  } while (next)
  return out
}

/** The day: its server, its parts as built, what they made, and how it ends. */
export class Day {
  readonly server: OwnServer
  /** The runs and the inbox refs the parts made. */
  readonly runs = new Set<string>()
  readonly refs = new Map<string, PartName>()
  /** What the server listed as the day's start left it, with each part's build applied: what drift() compares. */
  base: Snapshot = new Map()
  private readonly parts = new Map<PartName, Names>()
  private readonly building = new Map<PartName, Promise<Names>>()

  constructor(server: OwnServer) {
    this.server = server
  }

  get api(): Api {
    return this.server.api
  }

  /** The parts built so far, in the order they were built. */
  built(): PartName[] {
    return [...this.parts.keys()]
  }

  /**
   * The part's names mapped to ids, building it (and the parts it stands on, first) inside the running test when it is not built yet.
   * Each wait of a build raises the test's timeout by its cap.
   */
  async part(name: PartName): Promise<Names> {
    const done = this.parts.get(name)
    if (done) return done
    let p = this.building.get(name)
    if (!p) {
      p = (async () => {
        for (const n of PARTS[name].needs) await this.part(n)
        // What the build changes, and only that, joins the day: a test's own write made before the build stays the test's.
        const before = await this.snapshot()
        const names = await PARTS[name].build(this)
        const after = await this.snapshot()
        for (const [k, v] of after) if (before.get(k) !== v) this.base.set(k, v)
        for (const k of before.keys()) if (!after.has(k)) this.base.delete(k)
        this.parts.set(name, names)
        return names
      })()
      this.building.set(name, p)
      p.catch(() => this.building.delete(name))
    }
    return p
  }

  /** Every session the server lists, whatever its status. */
  async listSessions(): Promise<Session[]> {
    return listed(await this.api.ok<{ sessions?: Session[] } | Session[]>('GET', '/api/sessions'))
  }

  /** The inbox's open items, every page. */
  async inbox(): Promise<Array<{ ref: string; kind: string } & Record<string, unknown>>> {
    const out: Array<{ ref: string; kind: string } & Record<string, unknown>> = []
    let after = ''
    do {
      const r = await this.api.ok<{ items: Array<{ ref: string; kind: string }>; next?: string }>('GET', `/api/inbox${after ? `?after=${encodeURIComponent(after)}` : ''}`)
      out.push(...r.items)
      after = r.next ?? ''
    } while (after)
    return out
  }

  /** Ends what guard() named left: each run stopped, each session stopped, waited ended and removed (a second DELETE). */
  async endLeftovers(left: Leftover[]): Promise<void> {
    for (const l of left) if (l.kind === 'run') await this.api.stopRun(l.id)
    const sessions = left.filter((l) => l.kind === 'session')
    for (const l of sessions) await this.api.stopSession(l.id)
    for (const l of sessions) {
      await expect.poll(() => this.statusOf(l.id), { timeout: DAY_SESSIONS_END_MS, message: `${l.words} ends` }).toMatch(/^(exited|stopped|gone)$/)
      await this.api.stopSession(l.id)
    }
  }

  /** A session's status, or 'gone' once the server lists it no more. */
  async statusOf(id: string): Promise<string> {
    const r = await this.api.call<{ session?: Session } & Partial<Session>>('GET', `/api/sessions/${encodeURIComponent(id)}`)
    if (r.status === 404) return 'gone'
    return r.body.session?.status ?? r.body.status ?? `unreadable (${r.status})`
  }

  /**
   * Everything the day's server lists, as the guard compares it: Dispatch's people, projects (archived too), labels, milestones,
   * saved views, tickets of every project (deleted too: title, status, priority, owner, assignees, labels, milestone, parent,
   * revision) and each live ticket's plans (state, stale, withdrawn, revision); the crews; the share links of every listed session
   * and run; every session (name, status, exit code, agent, folder, run and member, its attention whole: state, source, kind,
   * message, id, decision); every run (name, label, state, outcome, paused, each member's status and session, each task's state,
   * attempts and the decisions that hold it by id and state); and the inbox's items (kind, state). About fifty reads on the busy day.
   */
  async snapshot(): Promise<Snapshot> {
    const api = this.api
    const out: Snapshot = new Map()
    const put = (key: `${SnapshotKind} ${string}`, v: unknown) => out.set(key, JSON.stringify(v))
    for (const p of (await api.ok<{ people: Array<{ name: string }> }>('GET', '/api/dispatch/people')).people) put(`person ${p.name}`, {})
    type ProjectRow = { key: string; name: string; archivedAt?: string; rev: number }
    const projects = await pages(api, '/api/dispatch/projects?archived=1', (r) => r.projects as ProjectRow[])
    const labels = new Map<string, string>()
    for (const p of projects) {
      put(`project ${p.key}`, { name: p.name, archived: !!p.archivedAt, rev: p.rev })
      const v = await api.ok<{ labels: Array<{ id: string; name: string; rev: number }>; milestones: Array<{ name: string; state: string; rev: number }> }>('GET', `/api/dispatch/projects/${p.key}`)
      for (const l of v.labels) {
        labels.set(l.id, l.name)
        put(`label ${p.key}/${l.name}`, { rev: l.rev })
      }
      for (const m of v.milestones) put(`milestone ${p.key}/${m.name}`, { state: m.state, rev: m.rev })
    }
    for (const v of (await api.ok<{ views: Array<{ id: string; name: string; rev: number }> }>('GET', '/api/dispatch/views')).views) put(`view ${v.name} (${v.id})`, { rev: v.rev })
    type TicketRow = { key: string; title: string; status: string; priority: number; owner: string; assignees: string[]; labels: string[]; milestoneId: string; parentId: string; deletedAt?: string; rev: number }
    for (const t of await pages(api, '/api/dispatch/tickets?deleted=1&limit=200', (r) => r.tickets as TicketRow[])) {
      const { title, status, priority, owner, milestoneId, parentId, rev } = t
      put(`ticket ${t.key}`, { title, status, priority, owner, assignees: [...t.assignees].sort(), labels: t.labels.map((id) => labels.get(id) ?? id).sort(), milestoneId, parentId, deleted: !!t.deletedAt, rev })
      if (t.deletedAt) continue
      type PlanRow = { n: number; state: string; stale?: unknown; withdrawnAt?: string; rev: number }
      for (const p of (await api.ok<{ plans: PlanRow[] }>('GET', `/api/dispatch/tickets/${t.key}/plans`)).plans) {
        put(`plan ${t.key}/${p.n}`, { state: p.state, stale: !!p.stale, withdrawn: !!p.withdrawnAt, rev: p.rev })
      }
    }
    for (let offset = 0; ; ) {
      const r = await api.ok<{ crews: Array<{ id: string; name: string; updatedAt: string }>; total: number }>('GET', `/api/crews?offset=${offset}&limit=500`)
      for (const c of r.crews) put(`crew ${c.name} (${c.id})`, { updatedAt: c.updatedAt })
      offset += r.crews.length
      if (!r.crews.length || offset >= r.total) break
    }
    type LinkRow = { id: string; role: string; revoked: boolean; decide?: boolean }
    const link = (l: LinkRow) => ({ role: l.role, revoked: l.revoked, decide: !!l.decide })
    for (const s of await this.listSessions()) {
      const a = s.attention
      const attention = { state: a?.state, source: a?.source, kind: a?.kind, message: a?.message, id: a?.id, decision: a?.decision }
      put(`session ${s.id}`, { name: s.name, status: s.status, exit: s.exitCode, agent: s.agentId, cwd: s.cwd, run: s.crew?.runId, member: s.crew?.member, attention })
      const r = await api.call<{ links?: LinkRow[] }>('GET', `/api/sessions/${s.id}/links`)
      for (const l of r.body.links ?? []) put(`share link ${l.id} of session ${s.id}`, link(l))
    }
    for (const run of (await api.ok<{ runs: Run[] }>('GET', '/api/runs')).runs ?? []) {
      const members = Object.fromEntries(run.members.map((m) => [m.name, { status: m.status, session: m.sessionId }]))
      const holds = (t: RunTask) => Object.fromEntries((t.holds ?? []).map((h) => [h.id, h.state]))
      const tasks = Object.fromEntries((run.tasks ?? []).map((t) => [t.id, { state: t.state, attempts: t.attempts, holds: holds(t) }]))
      put(`run ${run.id}`, { name: run.name, label: run.label, state: run.state, outcome: run.outcome, paused: !!run.paused, members, tasks })
      const r = await api.call<{ links?: LinkRow[] }>('GET', `/api/runs/${run.id}/links`)
      for (const l of r.body.links ?? []) put(`share link ${l.id} of run ${run.id}`, link(l))
    }
    for (const it of await this.inbox()) put(`inbox ${it.ref}`, { kind: it.kind, state: it.state })
    return out
  }

  /**
   * What the server lists that is not as the day's start and its parts' builds left it (`now`, or a snapshot read for it), one line
   * each: an item added, an item gone, or a field changed, from what to what, by the item's name.
   */
  async drift(now?: Snapshot): Promise<string[]> {
    now ??= await this.snapshot()
    const out: string[] = []
    for (const [k, v] of now) {
      const was = this.base.get(k)
      if (was === undefined) out.push(`${named(k, v)} is not the day's: ${v}`)
      else if (was !== v) {
        const [a, b] = [fields(JSON.parse(was)), fields(JSON.parse(v))]
        for (const f of new Set([...Object.keys(a), ...Object.keys(b)])) if (a[f] !== b[f]) out.push(`${named(k, was)}: ${f} is ${b[f] ?? 'nothing'} where the day left ${a[f] ?? 'nothing'}`)
      }
    }
    for (const [k, v] of this.base) if (!now.has(k)) out.push(`${named(k, v)} is gone (the day left ${v})`)
    return out
  }

  /**
   * What the `day` fixture judges at a test's end, from one snapshot, nothing changed by reading it: `problems`, drift()'s lines, and
   * `left`, the sessions and runs among them that no part made (the fixture ends them). A test of the guard calls it on a day of its own.
   */
  async guard(): Promise<{ left: Leftover[]; problems: string[] }> {
    const now = await this.snapshot()
    const left: Leftover[] = []
    for (const [k, v] of now) {
      const m = /^(session|run) (\S+)$/.exec(k)
      if (m && !this.base.has(k)) left.push({ kind: m[1] as Leftover['kind'], id: m[2]!, words: named(k, v) })
    }
    return { left, problems: await this.drift(now) }
  }

  /**
   * Ends the day in the order AGENTS.md asks (end what a test started before its directories go): the runs stopped and waited
   * ended, the sessions stopped and waited ended, `stop` written into every hold file, then the server stopped (which ends its agent
   * probes and waits for them), its log copied to test-results/<name>-server.log (startDay's name) and its root removed. What could not be
   * waited for is said after the server is gone.
   */
  async end(): Promise<void> {
    const problems: string[] = []
    try {
      const runs = [...this.runs]
      for (const id of runs) await this.api.stopRun(id).catch(() => undefined)
      await Promise.all(
        runs.map((id) =>
          expect
            .poll(async () => (await this.api.run(id)).state, { timeout: DAY_RUNS_END_MS, message: `the day's run ${id} ends` })
            .not.toMatch(/^(starting|running|needs_input|pending)$/)
            .catch((e: Error) => problems.push(e.message.split('\n')[0]!)),
        ),
      )
      const live = (await this.listSessions()).filter((s) => !/^(exited|stopped)$/.test(s.status))
      for (const s of live) await this.api.stopSession(s.id)
      await Promise.all(
        live.map((s) =>
          expect
            .poll(async () => (await this.api.session(s.id)).status, { timeout: DAY_SESSIONS_END_MS, message: `the day's session ${s.name} ends` })
            .toMatch(/^(exited|stopped)$/)
            .catch((e: Error) => problems.push(e.message.split('\n')[0]!)),
        ),
      )
    } catch (e) {
      problems.push(String(e))
    } finally {
      // The server's agent probes are not waited for here: its stop ends them before any of its waits and waits for them (#961).
      const holds = join(this.server.home, '.stub-sessions')
      if (existsSync(holds)) for (const f of readdirSync(holds)) if (f.endsWith('.hold')) writeFileSync(join(holds, f), 'stop\n')
      await this.server.stop()
    }
    if (problems.length) throw new Error(`the day ended, with what did not end in time: ${problems.join('; ')}`)
  }

  /** A loose session of the day's: launched on `agent`, named `name`, noted as the day's. */
  async launch(name: string, agent: string): Promise<string> {
    return (await this.api.ok<Session>('POST', '/api/sessions', { agentId: agent, name })).id
  }

  /** The session read until it is as promised (within DAY_STEP_MS, the test's timeout raised by it). */
  async sessionIs(id: string, what: string, p: Promised): Promise<Session> {
    budget(DAY_STEP_MS)
    let s = await this.api.session(id)
    await expect.poll(async () => promised((s = await this.api.session(id)), p), { timeout: DAY_STEP_MS, message: `${what} is to be ${p.status} with attention ${p.attention || 'none'}${p.turns ? ', its turns told' : ''}` }).toBe('')
    return s
  }

  /** The run read until `done` holds of it, within `cap`, the test's timeout raised by it. */
  async runUntil(id: string, what: string, done: (r: Run) => boolean, cap = DAY_STEP_MS): Promise<Run> {
    budget(cap)
    let r = await this.api.run(id)
    await expect.poll(async () => done((r = await this.api.run(id))), { timeout: cap, intervals: [500], message: what }).toBe(true)
    return r
  }

  /** A crew made and launched (POST /api/crews/{id}/launch, with `label` when given); its run noted as the day's. */
  async launchCrew(body: Record<string, unknown>, label?: string): Promise<string> {
    const id = (await this.api.ok<{ crew: { id: string } }>('POST', '/api/crews', body)).crew.id
    const run = (await this.api.ok<{ run: Run }>('POST', `/api/crews/${encodeURIComponent(id)}/launch`, label ? { label } : undefined)).run
    this.runs.add(run.id)
    return run.id
  }

  /** The open decision a task of the run holds, read until there is one; its id. */
  async heldBy(run: string, taskId: string, what: string): Promise<string> {
    let id = ''
    await this.runUntil(run, what, (r) => (id = r.tasks?.find((t) => t.id === taskId)?.holds?.find((h) => h.state === 'open')?.id ?? '') !== '', DAY_RUN_MS)
    return id
  }

  /** Notes the inbox ref as `part`'s, and waits for the inbox to list it. */
  async listedRef(part: PartName, ref: string): Promise<void> {
    this.refs.set(ref, part)
    budget(DAY_STEP_MS)
    await expect.poll(async () => (await this.inbox()).some((it) => it.ref === ref), { timeout: DAY_STEP_MS, message: `the inbox lists ${ref}` }).toBe(true)
  }

  /** The live session of a run's member, read until `ok` holds of it (within DAY_STEP_MS, the test's timeout raised by it). */
  async sessionsOf(run: string, member: string, ok: (s: Session) => boolean, what: string): Promise<Session> {
    budget(DAY_STEP_MS)
    let found: Session | undefined
    await expect
      .poll(async () => {
        found = (await this.listSessions()).find((s) => s.crew?.runId === run && s.crew.member === member && s.status === 'running')
        return !!found && ok(found)
      }, { timeout: DAY_STEP_MS, message: what })
      .toBe(true)
    return found!
  }

  /** Waits until each member of the run has one live session at most: the session of an attempt before its member's last has ended. */
  async oneSessionPerMember(run: string): Promise<void> {
    budget(DAY_STEP_MS)
    await expect
      .poll(async () => {
        const live = (await this.listSessions()).filter((s) => s.crew?.runId === run && s.status === 'running').map((s) => s.crew!.member)
        return live.length === new Set(live).size
      }, { timeout: DAY_STEP_MS, message: `each member of ${run} has one live session` })
      .toBe(true)
  }
}

/** '' when the session is as promised, else what it is. */
function promised(s: Session, p: Promised): string {
  const att = s.attention?.state ?? ''
  const turns = s.turns === true
  return s.status === p.status && att === p.attention && (p.turns === undefined || turns === p.turns) ? '' : `${s.status} with attention ${att || 'none'}, turns ${turns}`
}

const PARTS: Record<PartName, Part> = {
  states: {
    needs: [],
    async build(d) {
      const n: Names = {}
      for (const [name, { agent }] of Object.entries(STATES)) n[name] = await d.launch(name, agent)
      // old-bisect has exited: stopped once, and kept listed for the day (CONDUCTOR_EXITED_RETENTION).
      await d.api.stopSession(n['old-bisect']!)
      for (const [name, { promise }] of Object.entries(STATES)) {
        const s = await d.sessionIs(n[name]!, name, promise)
        if (promise === ASKS) await d.listedRef('states', `question:${s.id}/${s.attention!.id}`)
      }
      return n
    },
  },

  dispatch: {
    needs: [],
    async build(d) {
      const api = d.api
      const n: Names = {}
      for (const p of PEOPLE) await api.ok('POST', '/api/dispatch/people', { opId: `day-person-${p.toLowerCase()}`, name: p })
      // No close policy on the day's projects, so that a ticket can be made done as it is made.
      for (const [key, name] of [['CND', 'Conductor'], ['OPS', 'Operations']] as const) {
        n[key] = (await api.ok<{ project: { id: string } }>('POST', '/api/dispatch/projects', { opId: `day-project-${key}`, key, name, closeRequires: [] })).project.id
      }
      const labels: Names = {}
      for (const [name, color] of LABELS) labels[name] = (await api.ok<{ label: { id: string } }>('POST', '/api/dispatch/projects/CND/labels', { opId: `day-label-${name}`, name, color })).label.id
      const milestones: Names = {}
      for (const [i, name] of MILESTONES.entries()) {
        milestones[name] = (await api.ok<{ milestone: { id: string } }>('POST', '/api/dispatch/projects/CND/milestones', { opId: `day-milestone-${name}`, name, target: `2026-12-0${i + 1}` })).milestone.id
      }
      for (const [i, t] of TICKETS.entries()) {
        const r = await api.ok<{ ticket: { key: string; id: string } }>('POST', '/api/dispatch/tickets', {
          opId: `day-ticket-${i + 1}`,
          project: 'CND',
          title: t.title,
          ...(t.owner ? { owner: t.owner } : {}),
          assignees: t.assignees.map((a) => `person:${a}`),
          priority: t.priority,
          status: t.status,
          labels: t.labels.map((l) => labels[l]),
          ...(t.milestone ? { milestone: milestones[t.milestone] } : {}),
        })
        expect(r.ticket.key).toBe(`CND-${i + 1}`)
        n[r.ticket.key] = r.ticket.id
      }
      const comments: Array<[string, string, string]> = [
        ['CND-1', 'Nate', 'The token is shown once, as a code to scan.'],
        ['CND-1', 'Ana', 'And it ends when the session does?'],
        ['CND-3', 'Priya', 'Schema 5 adds the where column to the checks rows.'],
        ['CND-5', 'Ana', 'It fails one run in ten on the Mac.'],
      ]
      for (const [i, [key, by, body]] of comments.entries()) await api.ok('POST', `/api/dispatch/tickets/${key}/comments`, { opId: `day-comment-${i + 1}`, byName: by, body })
      await api.ok('POST', '/api/dispatch/tickets/CND-3/relations', { opId: 'day-relation-1', kind: 'blocks', to: 'CND-4' })
      await api.ok('POST', '/api/dispatch/tickets/CND-1/relations', { opId: 'day-relation-2', kind: 'relates', to: 'CND-10' })
      // The crew CND-1's and CND-5's plans name: one task, `ship`, which CND-1's plan holds behind its gate decision.
      n['crew:Pairing rollout'] = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', crew('Pairing rollout', [{ name: 'shipper', agentId: 'worker-cli' }], [{ id: 'ship', member: 'shipper', kind: 'work', instructions: 'Ship the pairing.' }]))).crew.id
      // CND-1: a revision with a gate, approved. CND-5: a revision approved, then the ticket edited, so that it is approved no more.
      n['plan:CND-1'] = (await api.ok<{ plan: { id: string } }>('POST', '/api/dispatch/tickets/CND-1/plans', { opId: 'day-plan-1', crew: n['crew:Pairing rollout'], decisions: [{ id: 'gate', question: 'May the pairing ship?', kind: 'approval', before: ['ship'] }] })).plan.id
      await api.ok('POST', `/api/dispatch/plans/${n['plan:CND-1']}/approve`, { opId: 'day-approve-1', note: 'Go.' })
      n['plan:CND-5'] = (await api.ok<{ plan: { id: string } }>('POST', '/api/dispatch/tickets/CND-5/plans', { opId: 'day-plan-5', crew: n['crew:Pairing rollout'] })).plan.id
      await api.ok('POST', `/api/dispatch/plans/${n['plan:CND-5']}/approve`, { opId: 'day-approve-5' })
      const five = (await api.ok<{ ticket: { rev: number } }>('GET', '/api/dispatch/tickets/CND-5')).ticket
      await api.ok('POST', '/api/dispatch/tickets/CND-5/edit', { opId: 'day-edit-5', expectedRev: five.rev, set: { description: 'Edited after its plan was approved.' } })
      // An approved revision no longer approved waits for the operator again.
      await d.listedRef('dispatch', 'plan:CND-5/1')
      return n
    },
  },

  waiting: {
    needs: ['dispatch'],
    async build(d) {
      const api = d.api
      const n: Names = {}
      // A loose question with three choices.
      n['proc-race-tests'] = await d.launch('proc-race-tests', 'day-asker')
      const q = await d.sessionIs(n['proc-race-tests'], 'proc-race-tests', ASKS)
      await d.listedRef('waiting', `question:${q.id}/${q.attention!.id}`)
      // A run member's question: Token pairing's navigator.
      n['Token pairing'] = await d.launchCrew(crew('Token pairing', [{ name: 'navigator', agentId: 'day-asker' }]))
      const run = await d.runUntil(n['Token pairing'], "Token pairing's navigator has a session", (r) => !!r.members.find((m) => m.name === 'navigator')?.sessionId)
      n.navigator = run.members.find((m) => m.name === 'navigator')!.sessionId!
      const nav = await d.sessionIs(n.navigator, 'navigator', ASKS)
      await d.listedRef('waiting', `question:${nav.id}/${nav.attention!.id}`)
      // Paging spike: index out of its three attempts (a check that exits 1), naming's clarification mirrored by its asker's session.
      n['Paging spike'] = await d.launchCrew(
        crew('Paging spike', [{ name: 'indexer', agentId: 'worker-cli' }, { name: 'clarifier', agentId: 'day-clarifier' }], [
          { id: 'index', member: 'indexer', kind: 'work', instructions: 'Index the pages.', checks: [{ id: 'index-built', argv: ['sh', '-c', 'echo no index was built; exit 1'] }] },
          { id: 'naming', member: 'clarifier', kind: 'work', instructions: 'Name the index.' },
        ]),
      )
      n.clarification = await d.heldBy(n['Paging spike'], 'naming', "naming's attempt asks its clarification")
      await d.runUntil(n['Paging spike'], "Paging spike's index fails its check three times", (r) => r.tasks?.find((t) => t.id === 'index')?.state === 'failed', DAY_RUN_MS)
      await d.listedRef('waiting', `task:${n['Paging spike']}/index`)
      await d.listedRef('waiting', `decision:${n.clarification}`)
      // CND-1's approved revision launched: its gate decision opened with the run, held by no session, ship waiting for it.
      budget(DAY_LAUNCH_MS)
      n['Pairing rollout'] = (await api.ok<{ launch: { runId: string } }>('POST', '/api/dispatch/tickets/CND-1/launch', { opId: 'day-launch-1', plan: (await d.part('dispatch'))['plan:CND-1'], label: 'Pairing rollout' })).launch.runId
      d.runs.add(n['Pairing rollout'])
      n.gate = await d.heldBy(n['Pairing rollout'], 'ship', 'ship waits for the gate')
      await d.listedRef('waiting', `decision:${n.gate}`)
      // CND-2's revision 1, proposed and waiting for approval.
      n['plan:CND-2'] = (await api.ok<{ plan: { id: string } }>('POST', '/api/dispatch/tickets/CND-2/plans', { opId: 'day-plan-2', crew: (await d.part('dispatch'))['crew:Pairing rollout'] })).plan.id
      await d.listedRef('waiting', 'plan:CND-2/1')
      // The clarifier's session shows the decision it asked, as its own question.
      await d.sessionsOf(n['Paging spike'], 'clarifier', (s) => s.attention?.state === 'needs_input' && s.attention?.decision === n.clarification, "naming's session mirrors its clarification")
      // Each member's one session left: index's attempts before its last have ended.
      await d.oneSessionPerMember(n['Paging spike'])
      return n
    },
  },

  runs: {
    needs: ['dispatch'],
    async build(d) {
      const api = d.api
      const n: Names = {}
      // Ledger migration, launched from CND-3's approved revision: two tasks done, one working (its member's turn held), one waiting
      // on the approval its asker's session mirrors, two waiting behind them.
      n['crew:Ledger migration'] = (
        await api.ok<{ crew: { id: string } }>(
          'POST',
          '/api/crews',
          crew('Ledger migration', [{ name: 'builder', agentId: 'worker-cli' }, { name: 'migrator', agentId: 'day-member-working' }, { name: 'approver', agentId: 'day-approver' }], [
            { id: 'schema', member: 'builder', kind: 'work', instructions: 'Write schema 5.' },
            { id: 'backfill', member: 'builder', kind: 'work', instructions: 'Backfill the where column.' },
            { id: 'migrate', member: 'migrator', kind: 'work', instructions: 'Migrate the ledger.' },
            { id: 'verify', member: 'approver', kind: 'work', instructions: 'Verify the migration.' },
            { id: 'cutover', member: 'builder', kind: 'work', instructions: 'Cut over.', needs: ['migrate'] },
            { id: 'cleanup', member: 'builder', kind: 'work', instructions: 'Clean up.', needs: ['verify'] },
          ]),
        )
      ).crew.id
      const plan = (await api.ok<{ plan: { id: string } }>('POST', '/api/dispatch/tickets/CND-3/plans', { opId: 'day-plan-3', crew: n['crew:Ledger migration'] })).plan.id
      await api.ok('POST', `/api/dispatch/plans/${plan}/approve`, { opId: 'day-approve-3', note: 'Go.' })
      budget(DAY_LAUNCH_MS)
      n['Ledger migration'] = (await api.ok<{ launch: { runId: string } }>('POST', '/api/dispatch/tickets/CND-3/launch', { opId: 'day-launch-3', plan, label: 'Ledger migration' })).launch.runId
      d.runs.add(n['Ledger migration'])
      n.approval = await d.heldBy(n['Ledger migration'], 'verify', "verify's attempt asks its approval")
      await d.runUntil(n['Ledger migration'], "Ledger migration's tasks are as promised", (r) => ledgerProblem(r, n.approval!) === '', DAY_RUN_MS)
      await d.listedRef('runs', `decision:${n.approval}`)
      await d.sessionsOf(n['Ledger migration'], 'migrator', (s) => s.attention?.state === 'working', "migrate's session is working")
      await d.sessionsOf(n['Ledger migration'], 'approver', (s) => s.attention?.state === 'needs_input' && s.attention?.decision === n.approval, "verify's session mirrors its approval")
      // Release notes, succeeded.
      n['Release notes'] = await d.launchCrew(crew('Release notes', [{ name: 'writer', agentId: 'worker-cli' }], [{ id: 'notes', member: 'writer', kind: 'work', instructions: 'Write the release notes.' }]))
      await d.runUntil(n['Release notes'], 'Release notes succeeds', (r) => r.outcome === 'succeeded', DAY_RUN_MS)
      // Each member's one session left: the attempts before a member's last have ended.
      await d.oneSessionPerMember(n['Ledger migration'])
      await d.oneSessionPerMember(n['Release notes'])
      return n
    },
  },

  long: {
    needs: ['dispatch'],
    async build(d) {
      const api = d.api
      const n: Names = {}
      // A session renamed to its name's bound: 120 bytes.
      n.long = await d.launch('long', 'day-finished')
      await d.sessionIs(n.long, 'the long session', FINISHED)
      await api.ok('PATCH', `/api/sessions/${n.long}`, { name: LONG_NAME })
      // A run whose label is exactly the bound a launch accepts (60 characters); its one member waits to be started by hand.
      n['Long label'] = await d.launchCrew(crew('Long label', [{ name: 'idle', agentId: 'plain', when: 'manual' }]), LONG_LABEL)
      // A ticket's title at its bound, 200 characters, in OPS.
      n['OPS-1'] = (await api.ok<{ ticket: { id: string } }>('POST', '/api/dispatch/tickets', { opId: 'day-ticket-ops-1', project: 'OPS', title: LONG_TITLE, owner: 'Priya' })).ticket.id
      return n
    },
  },

  busy: {
    needs: ['states', 'dispatch', 'waiting', 'runs', 'long'],
    async build(d) {
      const n: Names = {}
      for (const [name, turn] of Object.entries(BUSY)) n[name] = await d.launch(name, turn === 'working' ? 'day-working' : 'day-finished')
      for (const [name, turn] of Object.entries(BUSY)) await d.sessionIs(n[name]!, name, turn === 'working' ? WORKING : FINISHED)
      budget(DAY_STEP_MS)
      await expect.poll(async () => (await d.listSessions()).filter((s) => s.status === 'running').length, { timeout: DAY_STEP_MS, message: `the day holds ${BUSY_LIVE} live sessions` }).toBe(BUSY_LIVE)
      return n
    },
  },
}

/** '' when Ledger migration's six tasks are as promised, else their states. */
function ledgerProblem(r: Run, approval: string): string {
  const want: Record<string, string> = { schema: 'succeeded', backfill: 'succeeded', migrate: 'running', verify: 'running', cutover: 'blocked', cleanup: 'blocked' }
  const got = Object.fromEntries((r.tasks ?? []).map((t) => [t.id, t.state]))
  const held = r.tasks?.find((t) => t.id === 'verify')?.holds?.some((h) => h.id === approval && h.state === 'open')
  return JSON.stringify(got) === JSON.stringify(want) && held ? '' : `${JSON.stringify(got)}${held ? '' : `, verify not held by ${approval} open`}`
}

/**
 * Starts a day's server: an own server with the day's agents, its repository at DAY_REPO, exited sessions kept a day, a host token;
 * Dispatch, the crews and the links as it starts are the guard's first snapshot. `name` names its root and its log in test-results.
 * `started`, a test's seam, is called with the server once it answers, before the first snapshot. A step that fails after the server
 * has started ends the day (end: the server stopped, its log kept, its root removed) before its error is thrown: no caller holds a
 * day to end yet (AGENTS.md, end what a test started before its directories go).
 */
export async function startDay(name = 'day', started?: (s: OwnServer) => void | Promise<void>): Promise<Day> {
  const server = await ownServer(name, {
    agents: DAY_AGENTS,
    repoPath: DAY_REPO,
    env: { CONDUCTOR_EXITED_RETENTION: '24h' },
    hostToken: 'e2e-day-host-' + randomBytes(12).toString('hex'),
  })
  const day = new Day(server)
  let step = "the test's seam"
  try {
    await started?.(server)
    step = 'its first snapshot'
    day.base = await day.snapshot()
  } catch (e) {
    const ended = await day.end().then(() => 'it was ended', (x: Error) => `ending it: ${x.message}`)
    throw new Error(`the day ${name} did not start: ${step} failed: ${(e as Error).message} (${ended})`, { cause: e })
  }
  return day
}

/**
 * Lets a STUB_TURN=working session's held turn go, and returns it once its attention is done: writes `stop` into its hold file,
 * `$HOME/.stub-sessions/<agent session id>.hold` under the server's HOME (the stub's own session id, the one the server lists as
 * `agentSession.id`, never the session's id on the server), and waits for the turn's end. For a session a test made itself: a day
 * session let go is a change of the day, and the guard names it.
 */
export async function letGo(server: OwnServer, sessionId: string): Promise<Session> {
  const agent = (await server.api.session(sessionId)).agentSession?.id
  if (!agent) throw new Error(`letGo: session ${sessionId} lists no agent session id to name its hold file`)
  const hold = join(server.home, '.stub-sessions', `${agent}.hold`)
  if (!existsSync(hold)) throw new Error(`letGo: session ${sessionId} holds no turn (no ${hold})`)
  writeFileSync(hold, 'stop\n')
  budget(DAY_STEP_MS)
  let s = await server.api.session(sessionId)
  await expect.poll(async () => (s = await server.api.session(sessionId)).attention?.state, { timeout: DAY_STEP_MS, message: `session ${sessionId}'s held turn ends` }).toBe('done')
  return s
}
