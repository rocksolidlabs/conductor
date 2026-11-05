import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import type { BrakemanConfig, BriefItem } from './brakeman'
import { BOUNDS, KINDS, KIND_SUBJECTS, MAX_BY, NO_AGENT, agentChoice, agentIdOf, agentItems, askProblem, changedSince, configProblems, displayBy, formField, formMove, groupItems, keptBrief, kindWords, openedMark, parseRef, provenance, readsWords, receiptPath, refLabel, refPage, refRoute, sameConfig, scopeJSONBytes, stateRank, stateWords, stopAction, unopened } from './brakeman'

// Brakeman's words, refs and checks on the app's pages (docs/plans/brakeman.md, PR-6).

const RUN = 'brakeman-0a1b2c3d'
const SESSION = '0123456789abcdef'

function config(over: Partial<BrakemanConfig> = {}): BrakemanConfig {
  return {
    enabled: true,
    agentId: 'claude',
    cwd: '/home/me/code',
    allowConductorTools: false,
    visibility: { runs: true, logs: true, dispatch: true, sessions: true, touched: true, terminals: false },
    scope: {},
    permissions: { observe: true, draft: true, propose: true },
    wake: { mode: 'invoked' },
    budget: { turnMinutes: 15, readsPerTurn: 300, turnsPerDay: 40 },
    rev: 1,
    ...over,
  }
}

describe('the kinds and the states, in words', () => {
  it('names every kind the server takes, and only those', () => {
    const src = readFileSync(new URL('../../../internal/brakeman/request.go', import.meta.url), 'utf8')
    const kinds = /var Kinds = \[\]string\{([^}]*)\}/.exec(src)![1]!.split(',').map((k) => k.trim())
    const names = kinds.map((k) => new RegExp(`\\b${k}\\s*=\\s*"([a-z_]+)"`).exec(src)![1])
    expect(KINDS.map((k) => k.value)).toEqual(names)
    expect(Object.keys(KIND_SUBJECTS).sort()).toEqual([...names].sort())
    expect(kindWords('briefing')).toBe('Brief me since…')
    expect(kindWords('explain')).toBe('Explain this')
    expect(kindWords('decision')).toBe('Prepare a decision')
    expect(kindWords('draft_tickets')).toBe('Draft tickets')
    expect(kindWords('propose_crew')).toBe('Propose a crew')
    expect(kindWords('handoff')).toBe('Prepare a handoff')
    expect(kindWords('locate')).toBe('Find…')
    expect(kindWords('overlaps')).toBe('Overlapping changes')
  })

  it("says each state as the page shows it", () => {
    const base = { why: '', sessionId: SESSION, config: { budget: { turnMinutes: 15, readsPerTurn: 300, turnsPerDay: 40 } } } as never
    const say = (state: string, extra: Record<string, unknown> = {}, name?: string) => stateWords({ ...(base as object), state, ...extra } as never, name)
    expect(say('queued')).toBe('queued')
    expect(say('starting')).toBe('starting')
    expect(say('running', {}, 'Brakeman · br-3')).toBe('running in Brakeman · br-3')
    expect(say('running')).toBe(`running in ${SESSION}`)
    expect(say('briefed')).toBe('briefed')
    expect(say('timed_out')).toBe('timed out after 15 min')
    expect(say('timed_out', { config: { budget: { turnMinutes: 5 } } })).toBe('timed out after 5 min')
    expect(say('ended')).toBe('the session ended with no brief')
    expect(say('failed', { why: 'the agent exited before it read its request: is it logged in?' })).toBe('failed: the agent exited before it read its request: is it logged in?')
    expect(say('over_budget')).toBe("not started: today's turns were spent")
    expect(say('cancelled', { why: 'a person cancelled it' })).toBe('cancelled: a person cancelled it')
    expect(say('interrupted')).toBe('interrupted: the server stopped during the turn')
  })

  it('orders the states as a request goes through them, every closed one last', () => {
    expect([stateRank('queued'), stateRank('starting'), stateRank('running'), stateRank('briefed'), stateRank('cancelled')]).toEqual([0, 1, 2, 3, 3])
  })
})

describe('a ref, to its route and its label', () => {
  const cases: Array<[string, string | null, string]> = [
    [`run:${RUN}`, `/runs/${RUN}`, `run ${RUN}`],
    [`task:${RUN}/build`, `/runs/${RUN}?view=tasks&task=build`, 'task build'],
    [`attempt:${RUN}/build/2`, `/runs/${RUN}?view=tasks&task=build`, 'build, attempt 2'],
    [`check:${RUN}/build/2/unit/1`, `/runs/${RUN}?view=tasks&task=build`, 'check unit of build, attempt 2'],
    [`check:${RUN}/build/2/unit/3`, `/runs/${RUN}?view=tasks&task=build`, 'check unit of build, attempt 2 (run 3)'],
    [`command:${RUN}/c-1`, `/runs/${RUN}`, 'command c-1'],
    [`event:${RUN}/12`, `/runs/${RUN}`, `event 12 of ${RUN}`],
    [`message:${RUN}/3`, `/runs/${RUN}`, `message 3 of ${RUN}`],
    [`session:${SESSION}`, `/sessions/${SESSION}`, `session ${SESSION}`],
    [`question:${SESSION}/fedcba9876543210`, `/sessions/${SESSION}`, `question in ${SESSION}`],
    ['ticket:CND-4', '/dispatch/CND/4', 'CND-4'],
    ['comment:CND-4/00112233aabbccdd', '/dispatch/CND/4', 'comment on CND-4'],
    ['op:admin/abc.1', null, 'receipt abc.1'],
    [`commit:${RUN}/${'a'.repeat(40)}`, `/runs/${RUN}`, 'commit aaaaaaaa'],
    [`file:${SESSION}//home/me/repo/main.go`, `/sessions/${SESSION}?tab=files&open=%2Fhome%2Fme%2Frepo%2Fmain.go`, 'main.go'],
    ['brief:br-7/2', '/brakeman?request=br-7#item-2', 'br-7, item 3'],
    // DS2's records (PR-10): a plan revision on its ticket's page, a candidate on its run's Tasks view, a launch read from the API.
    ['plan:CND-4/2', '/dispatch/CND/4', 'CND-4 plan r2'],
    ['launch:0123456789abcdef', null, 'launch 01234567'],
    [`candidate:${RUN}/build/3`, `/runs/${RUN}?view=tasks&task=build`, 'candidate build, attempt 3'],
  ]
  it.each(cases)('%s', (ref, route, label) => {
    expect(parseRef(ref)).not.toBeNull()
    expect(refRoute(ref)).toBe(route)
    expect(refLabel(ref)).toBe(label)
  })

  it('covers every kind of ref the server names', () => {
    const src = readFileSync(new URL('../../../internal/brakeman/refs.go', import.meta.url), 'utf8')
    const kinds = [...src.matchAll(/\bRef[A-Z][a-z]+\s*=\s*"([a-z]+)"/g)].map((m) => m[1])
    expect(kinds.length).toBe(18)
    expect([...new Set(cases.map(([ref]) => ref.slice(0, ref.indexOf(':'))))].sort()).toEqual([...kinds].sort())
  })

  it("opens a task's page on the Tasks view, as the server's refURL does, whatever view this browser chose last", () => {
    const src = readFileSync(new URL('../../../internal/api/brakeman_refs.go', import.meta.url), 'utf8')
    expect(src).toContain('case brakeman.RefTask, brakeman.RefAttempt, brakeman.RefCandidate:')
    expect(src).toContain('return run + "?view=tasks&task=" + url.QueryEscape(ref.Task)')
    for (const ref of [`task:${RUN}/build`, `attempt:${RUN}/build/2`, `check:${RUN}/build/2/unit/1`, `candidate:${RUN}/build/2`]) {
      const u = new URL(refRoute(ref)!, 'http://x')
      expect(u.pathname).toBe(`/runs/${RUN}`)
      expect([...u.searchParams.entries()]).toEqual([['view', 'tasks'], ['task', 'build']])
    }
  })

  it("links a chip to the page its resolution names, never to an API route, which a link would carry no token to", () => {
    const at = (url: string) => ({ ref: '', seen: {}, now: { url } })
    // A Dispatch receipt has no page of its own: its ticket's, as the server read it.
    expect(refPage('op:admin/abc.1')).toBeNull()
    expect(refPage('op:admin/abc.1', at('/dispatch/CND/4'))).toBe('/dispatch/CND/4')
    expect(refPage(`task:${RUN}/build`, at(`/runs/${RUN}?view=tasks&task=build`))).toBe(`/runs/${RUN}?view=tasks&task=build`)
    // An API route, or what is no path of the app's, leaves the chip on refRoute's page.
    expect(refPage(`check:${RUN}/build/2/unit/1`, at(`/api/runs/${RUN}/tasks/build/attempts/2/steps/unit/1/log`))).toBe(`/runs/${RUN}?view=tasks&task=build`)
    expect(refPage('comment:CND-4/00112233aabbccdd', at('/api/dispatch/tickets/CND-4/comments?after=2&limit=1'))).toBe('/dispatch/CND/4')
    expect(refPage(`run:${RUN}`, at('//elsewhere.example/x'))).toBe(`/runs/${RUN}`)
    expect(refPage(`run:${RUN}`, at('https://elsewhere.example/x'))).toBe(`/runs/${RUN}`)
  })

  it('reads a receipt, a log and a comment from their API routes', () => {
    const at = (url: string) => ({ ref: '', seen: {}, now: { url } })
    expect(receiptPath(`check:${RUN}/build/2/unit/3`)).toBe(`/api/runs/${RUN}/tasks/build/attempts/2/steps/unit/3/log`)
    expect(receiptPath(`command:${RUN}/c-1`)).toBe(`/api/runs/${RUN}/commands/c-1`)
    // GET /api/dispatch/ops/{opId} reads the admin's receipts only.
    expect(receiptPath('op:admin/abc.1')).toBe('/api/dispatch/ops/abc.1')
    expect(receiptPath('op:brakeman/abc.1')).toBeNull()
    // A launch from a ticket: its summary and its package, the route the server's refURL names.
    expect(receiptPath('launch:0123456789abcdef')).toBe('/api/dispatch/launches/0123456789abcdef')
    // A comment by the route its resolution names (its number is the server's), and only its own ticket's.
    expect(receiptPath('comment:CND-4/00112233aabbccdd', at('/api/dispatch/tickets/CND-4/comments?after=2&limit=1'))).toBe('/api/dispatch/tickets/CND-4/comments?after=2&limit=1')
    expect(receiptPath('comment:CND-4/00112233aabbccdd', at('/api/dispatch/tickets/CND-5/comments?after=2&limit=1'))).toBeNull()
    expect(receiptPath('comment:CND-4/00112233aabbccdd', at('/dispatch/CND/4'))).toBeNull()
    expect(receiptPath('comment:CND-4/00112233aabbccdd')).toBeNull()
    for (const ref of [`run:${RUN}`, `task:${RUN}/build`, `session:${SESSION}`, 'ticket:CND-4', 'brief:br-7/2']) expect(receiptPath(ref), ref).toBeNull()
    // The server's routes, as refURL and commentURL write them.
    const src = readFileSync(new URL('../../../internal/api/brakeman_refs.go', import.meta.url), 'utf8')
    expect(src).toContain('"/attempts/" + strconv.Itoa(ref.N) + "/steps/" + esc(ref.Check) + "/" + strconv.Itoa(ref.K) + "/log"')
    expect(src).toContain('return "/api/runs/" + esc(ref.Run) + "/commands/" + esc(ref.Command)')
    expect(src).toContain('return "/api/dispatch/launches/" + esc(ref.Launch)')
    expect(src).toContain('"/api/dispatch/tickets/" + url.PathEscape(key) + "/comments?after="')
  })

  it('uses the names the page knows', () => {
    expect(refLabel(`session:${SESSION}`, { session: () => 'Brakeman · br-1' })).toBe('session Brakeman · br-1')
    expect(refLabel(`run:${RUN}`, { run: () => 'Brakeman · briefing br-1' })).toBe('run Brakeman · briefing br-1')
  })

  it('takes no ref the server would refuse', () => {
    for (const bad of ['plan:CND-4', 'plan:CND-4/0', 'plan:cnd-4/1', 'launch:0123', `candidate:${RUN}/build`, `candidate:${RUN}/build/0`, '', 'run', 'run:', 'run:Nope-0a1b2c3d', `task:${RUN}`, `attempt:${RUN}/build/0`, `attempt:${RUN}/build/01`, 'session:0123', 'ticket:cnd-4', `file:${SESSION}/relative`, `file:${SESSION}//a/../b`, 'brief:br-1/100', 'brief:br-0/1', `run:${RUN}\n`, 'nope:x', `run:${'a'.repeat(600)}-0a1b2c3d`]) {
      expect(parseRef(bad), bad).toBeNull()
    }
    expect(parseRef('op:a/b/c.1')).toEqual({ kind: 'op', actor: 'a/b', op: 'c.1' })
  })

  it('marks a record changed since the brief when its live state or revision differs', () => {
    expect(changedSince({ state: 'running', rev: '4' }, { state: 'running', rev: '4' })).toBe(false)
    expect(changedSince({ state: 'running', rev: '4' }, { state: 'stopped', rev: '4' })).toBe(true)
    expect(changedSince({ state: 'running', rev: '4' }, { state: 'running', rev: '5' })).toBe(true)
    expect(changedSince({ state: 'running' }, { state: 'running', rev: '5' })).toBe(false)
    expect(changedSince(undefined, { state: 'stopped' })).toBe(false)
    expect(changedSince({ state: 'running' }, undefined)).toBe(false)
  })
})

describe("a brief's items", () => {
  const items: BriefItem[] = [
    { kind: 'evidence/1', title: 'All', refs: [`run:${RUN}`] },
    { kind: 'fact/1', text: 'one', evidence: [`run:${RUN}`] },
    { kind: 'proposal/1', text: 'retry', op: 'task_retry', target: `task:${RUN}/build`, expectedRevision: '4', facts: [1] },
    { kind: 'interpretation/1', text: 'reading', facts: [1, 5] },
    { kind: 'overlap/1', paths: ['a.go'], between: [`attempt:${RUN}/a/1`, `attempt:${RUN}/b/1`] },
    { kind: 'fact/1', text: 'two', evidence: [`session:${SESSION}`] },
    { kind: 'outcome/1', text: 'passed', receipt: `check:${RUN}/build/1/unit/1` },
    { kind: 'handoff/1', objective: 'carry on' },
    { kind: 'draft_crew/1', crew: { name: 'c' } },
    { kind: 'comparison/1', title: 'vs', columns: ['a', 'b'], rows: [{ label: 'x', cells: ['1', '2'] }] },
    { kind: 'decision/1', about: `task:${RUN}/build`, happened: 'h', whyHuman: 'w', question: 'q?', alternatives: [{ label: 'a', tradeoffs: '' }, { label: 'b', tradeoffs: '' }], recommendation: 0, authorizes: 'a', evidence: [`run:${RUN}`] },
    { kind: 'draft_ticket/1', project: 'CND', title: 't' },
  ]

  it('are grouped facts, interpretations, decisions, proposals, outcomes, drafts, handoffs, overlaps, evidence, each in the brief\'s order', () => {
    const groups = groupItems(items)
    expect(groups.map((g) => g.group)).toEqual(['facts', 'interpretations', 'decisions', 'proposals', 'outcomes', 'drafts', 'handoffs', 'overlaps', 'evidence'])
    expect(groups[0]!.items.map((x) => [x.index, x.fact])).toEqual([[1, 1], [5, 2]])
    expect(groups.find((g) => g.group === 'drafts')!.items.map((x) => x.index)).toEqual([8, 11])
    expect(groups.find((g) => g.group === 'evidence')!.items.map((x) => x.index)).toEqual([0, 9])
    expect(groupItems([{ kind: 'fact/1', text: 'only', evidence: [`run:${RUN}`] }]).map((g) => g.group)).toEqual(['facts'])
  })

  it('of a brief kept with its summary alone are none, though the server writes them null', () => {
    const b = keptBrief({ schema: 'brakeman.brief/1', title: 'Nothing changed', summary: 'Quiet.', items: null })!
    expect(b.items).toEqual([])
    expect(groupItems(b.items)).toEqual([])
    expect(keptBrief({ schema: 'brakeman.brief/1', title: 't', summary: 's' })!.items).toEqual([])
    expect(keptBrief(null)).toBeNull()
    expect(keptBrief({ schema: 'brakeman.brief/1', title: 't', summary: 's', items })!.items).toBe(items)
  })

  it('name the facts an interpretation reads by their numbers', () => {
    expect(readsWords([1], items)).toBe('reads fact 1')
    expect(readsWords([1, 5], items)).toBe('reads facts 1 and 2')
    const three: BriefItem[] = [0, 1, 2].map((i) => ({ kind: 'fact/1', text: String(i), evidence: [`run:${RUN}`] }))
    expect(readsWords([0, 1, 2], three)).toBe('reads facts 1, 2 and 3')
    expect(readsWords([], items)).toBe('')
  })

  it('say who produced them, for which request, as of when', () => {
    const r = { id: 'br-3', agent: { agentId: 'claude', adapter: 'claude', version: '2.1.287', model: 'sonnet' }, briefAt: '2026-11-02T09:00:00Z' }
    expect(provenance(r, 'Claude Code')).toBe('Claude Code · model sonnet · version 2.1.287 · request br-3 · as of 2026-11-02T09:00:00Z')
    expect(provenance({ ...r, agent: { agentId: 'claude' }, briefAt: undefined })).toBe("claude · its default model · request br-3")
  })

  it('count as not opened until this browser opened them', () => {
    const at = '2026-11-02T09:00:00Z'
    const list = [{ id: 'br-3', hasBrief: true, createdAt: at }, { id: 'br-2', hasBrief: false, createdAt: at }, { id: 'br-1', hasBrief: true, createdAt: at }]
    expect(unopened(list, new Set())).toEqual(['br-3', 'br-1'])
    expect(unopened(list, new Set([openedMark(list[2]!)]))).toEqual(['br-3'])
    // Another server's br-1, written at another time, is not the one opened.
    expect(unopened(list, new Set(['br-1@2026-10-30T08:00:00Z']))).toEqual(['br-3', 'br-1'])
  })
})

describe('Stop on the request in progress', () => {
  it('cancels an open request, and stops the run of a closed one the desk still holds', () => {
    expect(stopAction({ state: 'running', runId: RUN })).toBe('cancel')
    expect(stopAction({ state: 'starting' })).toBe('cancel')
    // The brief's grace, or a stop that left a process: a cancel of a closed request does nothing.
    expect(stopAction({ state: 'briefed', runId: RUN })).toBe('stop_run')
    expect(stopAction({ state: 'timed_out', runId: RUN })).toBe('stop_run')
    expect(stopAction({ state: 'cancelled' })).toBeNull()
  })
})

describe("the composer's checks, the server's", () => {
  it('holds each kind to its subjects and its note', () => {
    expect(askProblem('briefing', [], '', '')).toBe('')
    expect(askProblem('briefing', [`run:${RUN}`], '', '')).toContain('names no subject')
    expect(askProblem('explain', [], '', '')).toContain('one subject')
    expect(askProblem('explain', [`run:${RUN}`], '', '')).toBe('')
    expect(askProblem('explain', ['op:admin/x'], '', '')).toContain('names a run')
    expect(askProblem('decision', [`run:${RUN}`], '', '')).toContain('question, a task, a ticket')
    expect(askProblem('locate', [], '', '')).toContain('is its note')
    expect(askProblem('locate', [], 'where is the config read?', '')).toBe('')
    expect(askProblem('draft_tickets', [], '', '')).toContain('Name a ticket')
    expect(askProblem('draft_tickets', ['ticket:CND-4'], '', '')).toBe('')
    expect(askProblem('ask', [], 'x'.repeat(16 << 10), '')).toBe('')
    expect(askProblem('ask', [], 'x'.repeat((16 << 10) + 1), '')).toContain('16 KiB')
    expect(askProblem('explain', [`run:${RUN}`], '', '2026-11-02T09:00:00Z')).toContain('Only a briefing')
    expect(askProblem('ask', Array.from({ length: 11 }, (_, i) => `ticket:CND-${i + 1}`), 'q', '')).toContain('at most 10')
  })
})

describe("the Agent select's items", () => {
  it('give None a value of its own, since a select item may not be "", and save it as no agent', () => {
    const items = agentItems([{ id: 'claude', name: 'Claude Code' }, { id: 'codex', name: 'Codex', available: false }])
    expect(items).toEqual([
      { label: 'None', value: NO_AGENT },
      { label: 'Claude Code', value: 'claude' },
      { label: 'Codex (not installed)', value: 'codex' },
    ])
    expect(items.every((i) => i.value !== '')).toBe(true)
    // No catalog id can be None's (ids are ^[a-z0-9-]{1,32}$).
    expect(/^[a-z0-9-]{1,32}$/.test(NO_AGENT)).toBe(false)
    expect([agentChoice(''), agentChoice(undefined), agentChoice('claude')]).toEqual([NO_AGENT, NO_AGENT, 'claude'])
    expect([agentIdOf(NO_AGENT), agentIdOf('claude')]).toEqual(['', 'claude'])
  })
})

describe("the configuration form's checks, the server's", () => {
  it('has the bounds of internal/brakeman/config.go', () => {
    const src = readFileSync(new URL('../../../internal/brakeman/config.go', import.meta.url), 'utf8')
    const n = (name: string) => Number(new RegExp(`\\b${name}\\s*=\\s*(\\d+)`).exec(src)![1])
    expect(BOUNDS.turnMinutes).toEqual([n('MinTurnMinutes'), n('MaxTurnMinutes')])
    expect(BOUNDS.readsPerTurn).toEqual([n('MinReadsPerTurn'), n('MaxReadsPerTurn')])
    expect(BOUNDS.turnsPerDay).toEqual([n('MinTurnsPerDay'), n('MaxTurnsPerDay')])
    expect(BOUNDS.selector).toBe(n('MaxSelector'))
    expect(BOUNDS.path).toBe(n('MaxPath'))
    const shift = /\bMaxScopeBytes\s*=\s*(\d+)\s*<<\s*(\d+)/.exec(src)!
    expect(BOUNDS.scopeBytes).toBe(Number(shift[1]) << Number(shift[2]))
  })

  it("counts a scope's bytes as Go's json.Marshal writes it", () => {
    // The lengths encoding/json gives these (go run, Go 1.26): <, > and & and U+2028 escaped long, ", \, \b, \f, \n, \r, \t short.
    expect(scopeJSONBytes({})).toBe(2)
    expect(scopeJSONBytes({ repos: [] })).toBe(2)
    expect(scopeJSONBytes({ repos: ['/a<b>&c', '/q"\\', '/é', '/\u2028', '/\u0001\b\f\n\r\t', '/😀'] })).toBe(89)
    expect(scopeJSONBytes({ projects: ['CND', 'AB'], runs: ['x-0a1b2c3d'], sessions: ['0123456789abcdef'] })).toBe(79)
  })

  it('refuses a scope past its bound as JSON, whose every entry is good', () => {
    const repos = (last: number) => [...Array.from({ length: 49 }, (_, i) => `/home/me/code/${String(i).padStart(146, '0')}`), `/home/me/code/${'9'.repeat(last - 14)}`]
    expect(scopeJSONBytes({ repos: repos(191) })).toBe(8192)
    expect(configProblems(config({ scope: { repos: repos(191) } }), 'claude')).toEqual({})
    expect(scopeJSONBytes({ repos: repos(192) })).toBe(8193)
    expect(Object.keys(configProblems(config({ scope: { repos: repos(192) } }), 'claude'))).toEqual(['scope'])
  })

  it("names the form's field a refusal of an entry belongs to", () => {
    expect(formField('scope.repos[3]')).toBe('scope.repos')
    expect(formField('scope.runs')).toBe('scope.runs')
    expect(formField('scope')).toBe('scope')
    expect(formField('permissions.observe')).toBe('permissions.observe')
  })

  it('tells a form left untouched from one being edited, whatever the revision saved since', () => {
    const read = config({ rev: 1, enabled: true })
    // Saved elsewhere at revision 2, off: the form still holds what it was read as, so it follows; it is no edit of its own.
    const savedSince = config({ rev: 2, enabled: false })
    expect(sameConfig(read, read)).toBe(true)
    expect(sameConfig({ ...read, rev: 1 }, savedSince)).toBe(false)
    expect(sameConfig(config({ rev: 1 }), config({ rev: 7, updatedAt: '2026-11-02T09:00:00Z' }))).toBe(true)
    // Empty and absent are the same: an account or model left empty, a selector with no entry, keys in another order.
    expect(sameConfig(config({ account: '', model: '' }), config({ scope: { repos: [] } }))).toBe(true)
    const reordered = Object.fromEntries(Object.entries(read).reverse()) as typeof read
    expect(sameConfig(read, reordered)).toBe(true)
    expect(sameConfig(read, config({ scope: { repos: ['/home/me'] } }))).toBe(false)
  })

  it("never keeps a form set under the token before, though the new server's revision is the same", () => {
    // Settings' form, edited, read at revision 2 of the first server; a reset, then the second server's configuration, also revision 2.
    expect(formMove({ gen: 0, edited: true }, 1, true)).toBe('set')
    expect(formMove({ gen: 0, edited: false }, 1, true)).toBe('set')
    // Before the second server has answered, the first one's form is not left to save.
    expect(formMove({ gen: 0, edited: true }, 1, false)).toBe('drop')
    // Under one token, a form follows what is saved until it is changed here.
    expect(formMove(null, 1, true)).toBe('set')
    expect(formMove({ gen: 1, edited: false }, 1, true)).toBe('set')
    expect(formMove({ gen: 1, edited: true }, 1, true)).toBe('keep')
    expect(formMove(null, 1, false)).toBe('keep')
    expect(formMove({ gen: 1, edited: true }, 1, false)).toBe('keep')
  })

  it('takes a configuration the server takes', () => {
    expect(configProblems(config(), 'claude')).toEqual({})
    expect(configProblems(config({ enabled: false, agentId: '', cwd: '' }), '')).toEqual({})
    expect(configProblems(config({ model: 'claude-sonnet-4.5', allowConductorTools: true }), 'claude')).toEqual({})
    expect(configProblems(config({ model: 'gpt-5.1-codex' }), 'codex')).toEqual({})
    expect(configProblems(config({ budget: { turnMinutes: 60, readsPerTurn: 2000, turnsPerDay: 500 } }), 'claude')).toEqual({})
    expect(configProblems(config({ budget: { turnMinutes: 1, readsPerTurn: 10, turnsPerDay: 1 } }), 'claude')).toEqual({})
    expect(configProblems(config({ scope: { projects: ['CND'], runs: [RUN], sessions: [SESSION], crews: ['brakeman'], repos: ['/home/me/code'] } }), 'claude')).toEqual({})
  })

  it('refuses each field the server refuses, by its name', () => {
    expect(Object.keys(configProblems(config({ agentId: '' }), ''))).toEqual(['agentId'])
    expect(Object.keys(configProblems(config({ agentId: 'aider' }), 'aider'))).toEqual(['agentId'])
    expect(Object.keys(configProblems(config({ model: '-x' }), 'claude'))).toEqual(['model'])
    expect(Object.keys(configProblems(config({ model: 'm', agentId: '', enabled: false }), ''))).toEqual(['model'])
    expect(Object.keys(configProblems(config({ cwd: '' }), 'claude'))).toEqual(['cwd'])
    expect(Object.keys(configProblems(config({ cwd: 'code' }), 'claude'))).toEqual(['cwd'])
    expect(Object.keys(configProblems(config({ cwd: '/home/me/../me' }), 'claude'))).toEqual(['cwd'])
    expect(Object.keys(configProblems(config({ account: 'rel' }), 'claude'))).toEqual(['account'])
    expect(Object.keys(configProblems(config({ budget: { turnMinutes: 0, readsPerTurn: 300, turnsPerDay: 40 } }), 'claude'))).toEqual(['budget.turnMinutes'])
    expect(Object.keys(configProblems(config({ budget: { turnMinutes: 15, readsPerTurn: 9, turnsPerDay: 40 } }), 'claude'))).toEqual(['budget.readsPerTurn'])
    expect(Object.keys(configProblems(config({ budget: { turnMinutes: 15, readsPerTurn: 300, turnsPerDay: 501 } }), 'claude'))).toEqual(['budget.turnsPerDay'])
    expect(Object.keys(configProblems(config({ scope: { projects: ['cnd'] } }), 'claude'))).toEqual(['scope.projects'])
    expect(Object.keys(configProblems(config({ scope: { runs: [RUN, RUN] } }), 'claude'))).toEqual(['scope.runs'])
    expect(Object.keys(configProblems(config({ scope: { sessions: Array.from({ length: 51 }, (_, i) => i.toString(16).padStart(16, '0')) } }), 'claude'))).toEqual(['scope.sessions'])
    // Until the catalog is read the agent is the server's to check.
    expect(configProblems(config({ agentId: 'aider' }), null)).toEqual({})
  })
})

describe('the display name a request and a make keep', () => {
  it("is held to the server's bound in bytes, cut where a character ends", () => {
    const src = readFileSync(new URL('../../../internal/brakeman/request.go', import.meta.url), 'utf8')
    expect(Number(/\bMaxBy\s*=\s*(\d+)/.exec(src)![1])).toBe(MAX_BY)
    // 21 three-byte characters are 63 bytes, which the identity's 40 characters allow and the make route refuses: 20 are sent.
    const cjk = '名'.repeat(21)
    expect(new TextEncoder().encode(cjk).length).toBe(63)
    expect(displayBy(cjk)).toBe('名'.repeat(20))
    // A four-byte character that would cross the bound is left out whole, never cut in half.
    expect(displayBy('a'.repeat(58) + '😀')).toBe('a'.repeat(58))
    expect(displayBy('  Nate  ')).toBe('Nate')
    expect(displayBy('Na\tte\r\n')).toBe('Na te')
  })
})

describe("DS2's records in a brief (PR-11)", () => {
  it("group a draft plan revision with the other drafts", () => {
    const items: BriefItem[] = [
      { kind: 'fact/1', text: 'f', evidence: ['ticket:CND-4'] },
      { kind: 'draft_plan/1', ticket: 'ticket:CND-4', crew: 'team' },
    ]
    expect(groupItems(items).map((g) => [g.group, g.items.map((x) => x.index)])).toEqual([
      ['facts', [0]],
      ['drafts', [1]],
    ])
  })
})
