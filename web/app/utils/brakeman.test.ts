import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import type { BrakemanConfig, BriefItem } from './brakeman'
import { BOUNDS, KINDS, KIND_SUBJECTS, askProblem, changedSince, configProblems, groupItems, kindWords, parseRef, provenance, readsWords, refLabel, refRoute, stateRank, stateWords, unopened } from './brakeman'

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
    [`task:${RUN}/build`, `/runs/${RUN}?task=build`, 'task build'],
    [`attempt:${RUN}/build/2`, `/runs/${RUN}?task=build&attempt=2`, 'build, attempt 2'],
    [`check:${RUN}/build/2/unit/1`, `/runs/${RUN}?task=build&attempt=2`, 'check unit of build, attempt 2'],
    [`check:${RUN}/build/2/unit/3`, `/runs/${RUN}?task=build&attempt=2`, 'check unit of build, attempt 2 (run 3)'],
    [`command:${RUN}/c-1`, `/runs/${RUN}`, 'command c-1'],
    [`event:${RUN}/12`, `/runs/${RUN}`, `event 12 of ${RUN}`],
    [`message:${RUN}/3`, `/runs/${RUN}`, `message 3 of ${RUN}`],
    [`session:${SESSION}`, `/sessions/${SESSION}`, `session ${SESSION}`],
    [`question:${SESSION}/fedcba9876543210`, `/sessions/${SESSION}`, `question in ${SESSION}`],
    ['ticket:CND-4', '/dispatch/CND/4', 'CND-4'],
    ['comment:CND-4/00112233aabbccdd', '/dispatch/CND/4#comment-00112233aabbccdd', 'comment on CND-4'],
    ['op:admin/abc.1', null, 'receipt abc.1'],
    [`commit:${RUN}/${'a'.repeat(40)}`, `/runs/${RUN}`, 'commit aaaaaaaa'],
    [`file:${SESSION}//home/me/repo/main.go`, `/sessions/${SESSION}?tab=files&open=%2Fhome%2Fme%2Frepo%2Fmain.go`, 'main.go'],
    ['brief:br-7/2', '/brakeman?request=br-7#item-2', 'br-7, item 3'],
  ]
  it.each(cases)('%s', (ref, route, label) => {
    expect(parseRef(ref)).not.toBeNull()
    expect(refRoute(ref)).toBe(route)
    expect(refLabel(ref)).toBe(label)
  })

  it('covers every kind of ref the server names', () => {
    const src = readFileSync(new URL('../../../internal/brakeman/refs.go', import.meta.url), 'utf8')
    const kinds = [...src.matchAll(/\bRef[A-Z][a-z]+\s*=\s*"([a-z]+)"/g)].map((m) => m[1])
    expect(kinds.length).toBe(15)
    expect([...new Set(cases.map(([ref]) => ref.slice(0, ref.indexOf(':'))))].sort()).toEqual([...kinds].sort())
  })

  it('uses the names the page knows', () => {
    expect(refLabel(`session:${SESSION}`, { session: () => 'Brakeman · br-1' })).toBe('session Brakeman · br-1')
    expect(refLabel(`run:${RUN}`, { run: () => 'Brakeman · briefing br-1' })).toBe('run Brakeman · briefing br-1')
  })

  it('takes no ref the server would refuse', () => {
    for (const bad of ['', 'run', 'run:', 'run:Nope-0a1b2c3d', `task:${RUN}`, `attempt:${RUN}/build/0`, `attempt:${RUN}/build/01`, 'session:0123', 'ticket:cnd-4', `file:${SESSION}/relative`, `file:${SESSION}//a/../b`, 'brief:br-1/100', 'brief:br-0/1', `run:${RUN}\n`, 'nope:x', `run:${'a'.repeat(600)}-0a1b2c3d`]) {
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
    const list = [{ id: 'br-3', hasBrief: true }, { id: 'br-2', hasBrief: false }, { id: 'br-1', hasBrief: true }]
    expect(unopened(list, new Set())).toEqual(['br-3', 'br-1'])
    expect(unopened(list, new Set(['br-1']))).toEqual(['br-3'])
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

describe("the configuration form's checks, the server's", () => {
  it('has the bounds of internal/brakeman/config.go', () => {
    const src = readFileSync(new URL('../../../internal/brakeman/config.go', import.meta.url), 'utf8')
    const n = (name: string) => Number(new RegExp(`\\b${name}\\s*=\\s*(\\d+)`).exec(src)![1])
    expect(BOUNDS.turnMinutes).toEqual([n('MinTurnMinutes'), n('MaxTurnMinutes')])
    expect(BOUNDS.readsPerTurn).toEqual([n('MinReadsPerTurn'), n('MaxReadsPerTurn')])
    expect(BOUNDS.turnsPerDay).toEqual([n('MinTurnsPerDay'), n('MaxTurnsPerDay')])
    expect(BOUNDS.selector).toBe(n('MaxSelector'))
    expect(BOUNDS.path).toBe(n('MaxPath'))
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
