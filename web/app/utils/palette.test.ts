import { describe, expect, it } from 'vitest'
import type { SessionInfo } from '~/composables/useSessions'
import type { Decision } from './decisions'
import type { InboxSummary } from './inbox'
import { needsYou } from './needsYou'
import { DEFAULT_FOLDS, sidebarModel } from './sidebar'
import { foldActions, sessionGroups, capGroup, dropPrefix, frecency, matchRange, MODE_PREFIX, noteUse, GO_PAGES, parsePrefix, parseVerb, placeAfter, rank, readRecent, RECENT_KEY, RECENT_MAX, rowExact, rowMatches, VERBS, writeRecent, type Named, type PaletteGroup } from './palette'

const names: Named[] = [
  { kind: 'session', id: 's1', name: 'api sweep', words: ['/home/dev/conductor', 'claude'] },
  { kind: 'session', id: 's2', name: 'docs', words: ['/home/dev/boxcar', 'codex'] },
  { kind: 'session', id: 's3', name: 'review', words: ['/home/dev/conductor', 'codex'] },
  { kind: 'run', id: 'r1', name: 'api sweep', words: ['nightly'] },
  { kind: 'crew', id: 'c1', name: 'api sweep' },
  { kind: 'crew', id: 'c2', name: 'docs crew' },
  { kind: 'agent', id: 'claude', name: 'Claude Code' },
]

describe('parsePrefix', () => {
  it('picks the mode from the first character and trims the rest', () => {
    expect(parsePrefix('')).toEqual({ mode: 'search', rest: '' })
    expect(parsePrefix('  api')).toEqual({ mode: 'search', rest: 'api' })
    expect(parsePrefix('> stop')).toEqual({ mode: 'actions', rest: 'stop' })
    expect(parsePrefix('#')).toEqual({ mode: 'runs', rest: '' })
    expect(parsePrefix(':src/main.go:12')).toEqual({ mode: 'files', rest: 'src/main.go:12' })
    expect(parsePrefix('@nate')).toEqual({ mode: 'people', rest: 'nate' })
    expect(parsePrefix('! done')).toEqual({ mode: 'events', rest: 'done' })
    expect(parsePrefix('?')).toEqual({ mode: 'shortcuts', rest: '' })
    expect(dropPrefix('> x')).toBe('')
    expect(dropPrefix('x')).toBe('x')
  })
})

describe('parseVerb', () => {
  it('reads a verb with a name that matches exactly one thing of the kinds it takes', () => {
    expect(parseVerb('stop docs', names)).toEqual({ verb: 'stop', target: names[1] })
    expect(parseVerb('reply docs looks good', names)).toEqual({ verb: 'reply', target: names[1], text: 'looks good' })
    expect(parseVerb('answer review 2', names)).toEqual({ verb: 'answer', target: names[2], choice: 2 })
    expect(parseVerb('share review control', names)).toEqual({ verb: 'share', target: names[2], role: 'control' })
    expect(parseVerb('share review', names)).toEqual({ verb: 'share', target: names[2], role: 'view' })
    expect(parseVerb('mark review working', names)).toEqual({ verb: 'mark', target: names[2], state: 'working' })
    expect(parseVerb('broadcast nightly ship it', names)).toEqual({ verb: 'broadcast', target: names[3], text: 'ship it' })
    expect(parseVerb('run docs crew as tonight', names)).toEqual({ verb: 'run', target: names[5], label: 'tonight' })
    expect(parseVerb('launch claude in /tmp/x', names)).toEqual({ verb: 'launch', target: names[6], dir: '/tmp/x' })
    expect(parseVerb('go yard', names)).toEqual({ verb: 'go', page: 'yard' })
    expect(parseVerb('open src/main.go:12', names)).toEqual({ verb: 'open', path: 'src/main.go', line: 12 })
  })

  it('is a search for an ambiguous name, a name of several kinds a verb takes, a bad argument, or no verb', () => {
    // "api sweep" is a session, a run and a crew: stop takes sessions and runs, so it is ambiguous.
    expect(parseVerb('stop api sweep', names)).toBeNull()
    // broadcast takes runs alone: one match.
    expect(parseVerb('broadcast api sweep go', names)).toEqual({ verb: 'broadcast', target: names[3], text: 'go' })
    // "co" matches conductor's sessions and more: ambiguous.
    expect(parseVerb('stop co', names)).toBeNull()
    expect(parseVerb('answer review ten', names)).toBeNull()
    expect(parseVerb('share review admin', names)).toBeNull()
    expect(parseVerb('mark review tired', names)).toBeNull()
    expect(parseVerb('go nowhere', names)).toBeNull()
    expect(parseVerb('reply docs', names)).toBeNull()
    expect(parseVerb('stop', names)).toBeNull()
    expect(parseVerb('hello docs', names)).toBeNull()
    expect(parseVerb('stop nothing-here', names)).toBeNull()
  })
})

describe('frecency', () => {
  const memory = (init: Record<string, string> = {}) => {
    const m = new Map(Object.entries(init))
    return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v), map: m }
  }

  it('reads what is kept, ignores a corrupt value, and keeps at most 200 entries', () => {
    expect(readRecent(null)).toEqual({})
    expect(readRecent(memory({ [RECENT_KEY]: 'not json' }))).toEqual({})
    expect(readRecent(memory({ [RECENT_KEY]: '[1,2]' }))).toEqual({})
    expect(readRecent(memory({ [RECENT_KEY]: JSON.stringify({ a: { count: 2, last: 5 }, b: { count: 'x' } }) }))).toEqual({ a: { count: 2, last: 5 } })
    let recent = {}
    for (let i = 0; i < RECENT_MAX + 5; i++) recent = noteUse(recent, `k${i}`, i)
    expect(Object.keys(recent)).toHaveLength(RECENT_MAX)
    expect(recent).not.toHaveProperty('k0')
    expect(recent).toHaveProperty('k204')
    const s = memory()
    writeRecent(s, { a: { count: 1, last: 1 } })
    expect(readRecent(s)).toEqual({ a: { count: 1, last: 1 } })
  })

  it('decays by the day', () => {
    const now = 10 * 86_400_000
    const recent = { fresh: { count: 4, last: now }, old: { count: 4, last: now - 2 * 86_400_000 } }
    expect(frecency(recent, 'fresh', now)).toBe(4)
    expect(frecency(recent, 'old', now)).toBe(1)
    expect(frecency(recent, 'never', now)).toBe(0)
  })
})

describe('rank', () => {
  const groups: PaletteGroup[] = [
    { id: 'goto', label: 'Go to', rows: [{ key: 'go:yard', label: 'Yard' }, { key: 'go:crews', label: 'Crews' }] },
    { id: 'page', label: 'This page', rows: [{ key: 'page:share', label: 'Share…' }, { key: 'page:stop', label: 'Stop…' }], pinned: true },
    { id: 'running', label: 'Running', rows: [{ key: 'session:s1', label: 'api sweep', detail: 'claude · conductor', words: ['/home/dev/conductor'] }, { key: 'session:s2', label: 'docs', detail: 'codex · boxcar' }] },
    { id: 'needs', label: 'Needs you', rows: [{ key: 'session:s3', label: 'review', detail: 'Allow Bash?' }], pinned: true },
  ]

  it('puts Needs you first, the page next, keeps pinned groups whole with no query, and drops empty groups', () => {
    expect(rank(groups, '', {}).map((g) => g.id)).toEqual(['needs', 'page', 'goto', 'running'])
    const q = rank(groups, 'cre', {})
    expect(q.map((g) => `${g.id}:${g.rows.map((r) => r.key).join(',')}`)).toEqual(['goto:go:crews'])
    const d = rank(groups, 'docs', {})
    expect(d.map((g) => g.id)).toEqual(['running'])
    expect(rowMatches(groups[2]!.rows[0]!, 'conductor')).toBe(true)
  })

  it('orders rows used lately first within a group, then as the group lists them; a corrupt recent value changes nothing', () => {
    const recent = { 'go:crews': { count: 3, last: Date.now() } }
    expect(rank(groups, '', recent)[2]!.rows.map((r) => r.key)).toEqual(['go:crews', 'go:yard'])
    expect(rank(groups, '', readRecent({ getItem: () => '{' } as unknown as Storage))[2]!.rows.map((r) => r.key)).toEqual(['go:yard', 'go:crews'])
  })

  it('finds the match to highlight', () => {
    expect(matchRange('API sweep', 'sweep')).toEqual([4, 9])
    expect(matchRange('api sweep', 'x')).toBeNull()
    expect(matchRange('api sweep', '')).toBeNull()
  })
})

describe("Dispatch's tickets in the palette (docs/plans/dispatch.md, Owner decisions, 11)", () => {
  // The Tickets group as usePaletteRows makes it: each ticket's key and title, its key its exact name; E2E-10 used lately.
  const tickets: PaletteGroup = {
    id: 'tickets',
    label: 'Tickets',
    rows: [
      { key: 'ticket:E2E-10', label: 'E2E-10 Tidy the login page', words: ['E2E-10'], exact: ['E2E-10'] },
      { key: 'ticket:E2E-11', label: 'E2E-11 Fix the logout', words: ['E2E-11'], exact: ['E2E-11'] },
      { key: 'ticket:E2E-1', label: 'E2E-1 Fix the login', words: ['E2E-1'], exact: ['E2E-1'] },
      { key: 'ticket:API-1', label: 'API-1 Rate limits', words: ['API-1'], exact: ['API-1'] },
    ],
  }
  const recent = { 'ticket:E2E-10': { count: 9, last: Date.now() }, 'ticket:E2E-11': { count: 4, last: Date.now() } }

  it('ranks the ticket a key typed whole names first in Tickets, whatever was used lately, in any case', () => {
    for (const q of ['E2E-1', 'e2e-1', ' E2E-1 ']) {
      const [g] = rank([tickets], q, recent)
      expect(g!.rows.map((r) => r.key)).toEqual(['ticket:E2E-1', 'ticket:E2E-10', 'ticket:E2E-11'])
    }
    expect(rowExact(tickets.rows[2]!, 'e2e-1')).toBe(true)
    expect(rowExact(tickets.rows[2]!, 'e2e-')).toBe(false)
    expect(rowExact(tickets.rows[2]!, '')).toBe(false)
  })

  it('finds a ticket by a word of its title, those used lately first, and by a part of a key, with no exact name to lift one', () => {
    expect(rank([tickets], 'login', recent)[0]!.rows.map((r) => r.key)).toEqual(['ticket:E2E-10', 'ticket:E2E-1'])
    expect(rank([tickets], 'e2e-', recent)[0]!.rows.map((r) => r.key)).toEqual(['ticket:E2E-10', 'ticket:E2E-11', 'ticket:E2E-1'])
    expect(rank([tickets], 'rate', recent)[0]!.rows.map((r) => r.key)).toEqual(['ticket:API-1'])
  })

  it('goes to Dispatch with go dispatch, and keeps twelve verbs', () => {
    expect(GO_PAGES).toContain('dispatch')
    expect(parseVerb('go dispatch', names)).toEqual({ verb: 'go', page: 'dispatch' })
    expect(parseVerb('go Dispatch', names)).toEqual({ verb: 'go', page: 'dispatch' })
    expect(VERBS).toHaveLength(12)
    expect(parsePrefix('E2E-1')).toEqual({ mode: 'search', rest: 'E2E-1' })
  })

  it('goes to History with go history, under no prefix and under `>` (Program item HS1, PR-7)', () => {
    expect(GO_PAGES).toContain('history')
    expect(parseVerb('go history', names)).toEqual({ verb: 'go', page: 'history' })
    expect(parseVerb('go History', names)).toEqual({ verb: 'go', page: 'history' })
    // `> go history`: the actions mode reads the rest as the verb, as under no prefix.
    const { mode, rest } = parsePrefix('> go history')
    expect(mode).toBe('actions')
    expect(parseVerb(rest, names)).toEqual({ verb: 'go', page: 'history' })
  })
})

describe('a long group cut to its first rows', () => {
  const rows = Array.from({ length: 12 }, (_, i) => ({ key: `ticket:A-${i + 1}`, label: `A-${i + 1} Ticket` }))
  const groups: PaletteGroup[] = [
    { id: 'running', label: 'Running', rows: rows.slice(0, 3) },
    { id: 'tickets', label: 'Tickets', rows },
  ]
  it('keeps its first n rows as ranked, says how many it holds, and leaves the other groups whole', () => {
    const [running, tickets] = capGroup(groups, 'tickets', 10)
    expect(tickets!.rows.map((r) => r.key)).toEqual(rows.slice(0, 10).map((r) => r.key))
    expect(tickets!.label).toBe('Tickets · 10 of 12, type to find the rest')
    expect(running!.rows).toHaveLength(3)
    expect(running!.label).toBe('Running')
  })
  it('leaves a group of n rows or fewer as it is', () => {
    expect(capGroup(groups, 'tickets', 12)[1]).toEqual(groups[1])
    expect(capGroup(groups, 'none', 1)).toEqual(groups)
  })
})

describe("history's prefix (Program item HS1, docs/plans/history.md, PR-7)", () => {
  it('parses ~ as the history mode, its rest trimmed, and drops it as every prefix is dropped', () => {
    expect(parsePrefix('~')).toEqual({ mode: 'history', rest: '' })
    expect(parsePrefix('~ stub-fixed outcome:failed')).toEqual({ mode: 'history', rest: 'stub-fixed outcome:failed' })
    expect(parsePrefix('  ~TestLoginRedirect ')).toEqual({ mode: 'history', rest: 'TestLoginRedirect' })
    expect(dropPrefix('~ stub-fixed')).toBe('')
    expect(dropPrefix('~')).toBe('')
    // A ~ inside a query is no prefix.
    expect(parsePrefix('a ~b')).toEqual({ mode: 'search', rest: 'a ~b' })
  })

  it('changes no other prefix: the six before it read as they did', () => {
    expect(MODE_PREFIX).toEqual({ actions: '>', runs: '#', files: ':', people: '@', events: '!', shortcuts: '?', history: '~' })
    expect(parsePrefix('> stop')).toEqual({ mode: 'actions', rest: 'stop' })
    expect(parsePrefix('#')).toEqual({ mode: 'runs', rest: '' })
    expect(parsePrefix(':src/main.go:12')).toEqual({ mode: 'files', rest: 'src/main.go:12' })
    expect(parsePrefix('@nate')).toEqual({ mode: 'people', rest: 'nate' })
    expect(parsePrefix('! done')).toEqual({ mode: 'events', rest: 'done' })
    expect(parsePrefix('? ~')).toEqual({ mode: 'shortcuts', rest: '~' })
    expect(parsePrefix('api')).toEqual({ mode: 'search', rest: 'api' })
    expect(dropPrefix('> x')).toBe('')
    expect(dropPrefix('x')).toBe('x')
  })

  it('places the History group after the last of the groups it follows, and not at all when it has no rows', () => {
    const g = (id: string, n = 1): PaletteGroup => ({ id, label: id, rows: Array.from({ length: n }, (_, i) => ({ key: `${id}:${i}`, label: `${id} ${i}` })) })
    const ids = (gs: PaletteGroup[]) => gs.map((x) => x.id)
    const after = ['needs', 'page', 'running', 'exited', 'decide', 'runs', 'tickets']
    expect(ids(placeAfter([g('needs'), g('running'), g('tickets'), g('shared'), g('goto')], g('history'), after))).toEqual(['needs', 'running', 'tickets', 'history', 'shared', 'goto'])
    // Tickets matched nothing: after the runs, still before Shared with you.
    expect(ids(placeAfter([g('runs'), g('shared')], g('history'), after))).toEqual(['runs', 'history', 'shared'])
    expect(ids(placeAfter([g('shared'), g('goto')], g('history'), after))).toEqual(['history', 'shared', 'goto'])
    expect(ids(placeAfter([g('tickets'), g('shared')], g('history', 0), after))).toEqual(['tickets', 'shared'])
  })
})

// The UX review's P3: the palette's two groups of what waits are one, "Needs you", and nothing in it is listed twice.
describe('sessionGroups: one Needs you group (UX1 PR-13)', () => {
  const T = (m: number) => `2026-10-01T09:${String(m).padStart(2, '0')}:00Z`
  const session = (id: string, over: Partial<SessionInfo> = {}) =>
    ({ id, name: id, kind: 'server', agentId: 'claude', command: [], cwd: '/w', status: 'running', cols: 80, rows: 24, viewers: 0, createdAt: T(0), ...over }) as SessionInfo
  const asks = (id: string, at: number, decision?: string) => session(id, { attention: { state: 'needs_input', id: `q-${id}`, since: T(at), message: 'May I?', ...(decision ? { decision } : {}) } as SessionInfo['attention'] })
  const record = (id: string, run: string, at: number) => ({ id, run, openedIn: run, origin: 'plan', type: 'approval', gate: 'g', request: { question: `${id}?` }, policy: {}, state: 'open', rev: 1, createdAt: T(at) }) as unknown as Decision
  const summary = (ref: string, kind: string, at: number): InboxSummary => ({ ref, kind, since: T(at), title: `the ${kind}`, brief: '', question: '', answerer: '', actions: [] })

  const sessions = [asks('loose', 3), asks('mirror', 4, 'dr-1'), session('worker'), session('gone', { status: 'exited' })]
  const records = [record('dr-1', 'r1', 1), record('dr-2', 'r2', 2)]
  // The inbox lists the question too, and the gate: never a second row of either.
  const inbox = [summary('question:loose/q-loose', 'question', 3), summary('decision:dr-2', 'decision', 2), summary('plan:CND-2/1', 'plan', 5), summary('task:r3/build', 'task', 6)]
  const list = needsYou({
    sessions: { list: sessions, loaded: true, error: '' },
    decisions: { records, loaded: true, error: '', cut: false },
    inbox: { items: inbox, loaded: true, error: '', next: '', unavailable: '' },
    runOf: () => ({}),
  })
  const make = {
    answerAll: () => ['answer-all'],
    session: (s: SessionInfo) => `session:${s.id}`,
    decisions: (open: readonly Decision[], inSession: (id: string) => string | undefined) => open.map((d) => `decision:${d.id}${inSession(d.id) ? ` in ${inSession(d.id)}` : ''}`),
    item: (it: { kind: string; ref: string }) => `${it.kind === 'plan' ? 'Plan' : 'Task'}: ${it.ref}`,
  }

  it('lists every item once: the question, the decisions (a mirrored question as its record), the plan and the task', () => {
    const groups = sessionGroups(sidebarModel(sessions, () => undefined, list.items), list.items, make)
    expect(groups.needs).toEqual(['answer-all', 'session:loose', 'decision:dr-2', 'decision:dr-1 in mirror', 'Plan: plan:CND-2/1', 'Task: task:r3/build'])
    // The question that is also an inbox item: one row.
    expect(groups.needs.filter((r) => r.includes('loose'))).toHaveLength(1)
    // The mirrored question's session is in Working; nothing of Needs you is listed again.
    expect(groups.working).toEqual(['session:mirror', 'session:worker'])
    expect(groups.finished).toEqual([])
    expect(groups.exited).toEqual(['session:gone'])
  })

  // The UX review's P4: the palette's Running group is Finished then Working, and the fold actions name the five sections.
  it('groups the sessions Finished then Working, a host away that does not ask in Exited', () => {
    const turns = (id: string, state: 'done' | 'working') => session(id, { turns: true, attention: { state, source: 'api', since: T(state === 'done' ? 7 : 8) } as SessionInfo['attention'] })
    const list2 = [turns('ended-turn', 'done'), turns('in-turn', 'working'), session('quiet'), session('away', { status: 'host_disconnected' as SessionInfo['status'] })]
    const groups = sessionGroups(sidebarModel(list2), [], make)
    expect(Object.keys(groups)).toEqual(['needs', 'finished', 'working', 'exited'])
    expect(groups.finished).toEqual(['session:ended-turn'])
    expect(groups.working.sort()).toEqual(['session:in-turn', 'session:quiet'])
    expect(groups.exited).toEqual(['session:away'])
  })

  it('names one fold action per section, in the sidebar\'s order', () => {
    expect(foldActions({ ...DEFAULT_FOLDS }).map((f) => f.label)).toEqual([
      "Fold the sidebar's Pinned",
      "Fold the sidebar's Needs you",
      "Fold the sidebar's Finished",
      "Fold the sidebar's Working",
      "Fold the sidebar's Shared with you",
      "Unfold the sidebar's Exited",
    ])
  })
})
