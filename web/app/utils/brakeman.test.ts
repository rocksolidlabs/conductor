import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import type { BrakemanAct, BrakemanConfig, BriefItem } from './brakeman'
import { renderMarkdown } from './markdown'
import { BOUNDS, BRIEFING_TIME, GRANTS, KINDS, KIND_SUBJECTS, MAX_BY, NO_AGENT, actFor, actsApart, readingBlocks, actLaunchRef, actMade, actReceiptRef, actShown, actShownFrom, actLaunchId, keepLaunch, MAX_KEPT_LAUNCHES, proposalOffered, actWords, followLaunch, agentChoice, agentIdOf, agentItems, askProblem, briefingLine, briefingList, changedFromBaseline, changedSince, configProblems, displayBy, foldersLine, formField, grantHeld, grantLabel, groupItems, keptBrief, kindWords, observesOnly, openedMark, parseRef, provenance, readsWords, receiptPath, refLabel, refPage, refRoute, refTicket, sameConfig, scopeJSONBytes, servedGrants, stateRank, stateWords, stopAction, unopened, withGrant } from './brakeman'

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
    wake: { mode: 'invoked', windowSeconds: 120, cooldownMinutes: 30, questionMinutes: 15 },
    budget: { turnMinutes: 15, readsPerTurn: 300, turnsPerDay: 40, minutesPerDay: 240 },
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
    expect(say('over_budget')).toBe("not started: today's budget was spent")
    expect(say('over_budget', { why: "the day's 40 turns were spent" })).toBe("not started: the day's 40 turns were spent")
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
    ['plan:CND-4/2', '/dispatch/CND/4', 'plan revision 2 of CND-4'],
    ['launch:00112233aabbccdd', null, 'launch 00112233aabbccdd'],
    [`candidate:${RUN}/build/2`, `/runs/${RUN}?view=tasks&task=build`, 'candidate build, attempt 2'],
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
    for (const bad of ['', 'run', 'run:', 'run:Nope-0a1b2c3d', `task:${RUN}`, `attempt:${RUN}/build/0`, `attempt:${RUN}/build/01`, 'session:0123', 'ticket:cnd-4', `file:${SESSION}/relative`, `file:${SESSION}//a/../b`, 'brief:br-1/100', 'brief:br-0/1', 'plan:CND-4/0', 'plan:CND-4', 'launch:0011', `candidate:${RUN}/build`, `candidate:${RUN}/build/0`, `run:${RUN}\n`, 'nope:x', `run:${'a'.repeat(600)}-0a1b2c3d`]) {
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

describe("a decision's evidence against its baseline (changedFromBaseline)", () => {
  const at = (ref: string, seen: { state?: string; rev?: string; sha256?: string }, now: { state?: string; rev?: string; sha256?: string }) => changedFromBaseline({ ref, seen, now })
  it('says changed when a state, a revision or a digest moved since the question, and nothing when each stayed', () => {
    expect(at('ticket:CND-4', { state: 'open', rev: '3' }, { state: 'done', rev: '3' })).toBe('changed')
    expect(at('ticket:CND-4', { state: 'open', rev: '3' }, { state: 'open', rev: '4' })).toBe('changed')
    expect(at(`check:${RUN}/build/1/go/1`, { state: 'passed', sha256: 'a'.repeat(64) }, { state: 'passed', sha256: 'b'.repeat(64) })).toBe('changed')
    expect(at('ticket:CND-4', { state: 'open', rev: '3' }, { state: 'open', rev: '3' })).toBe('')
    expect(at(`check:${RUN}/build/1/go/1`, { state: 'passed', sha256: 'a'.repeat(64) }, { state: 'passed', sha256: 'a'.repeat(64) })).toBe('')
  })
  it("reads a run's and a task's state alone: their revision moves with every transition, the decision's own opening among them", () => {
    expect(at(`run:${RUN}`, { state: 'running', rev: '4' }, { state: 'running', rev: '9' })).toBe('')
    expect(at(`task:${RUN}/build`, { state: 'running', rev: '4' }, { state: 'succeeded', rev: '4' })).toBe('changed')
  })
  it('says a baseline read nothing when its store could not be read then, and a record gone since deleted', () => {
    expect(at('ticket:CND-4', { state: 'unavailable' }, { state: 'open', rev: '3' })).toBe('unread')
    expect(at('ticket:CND-4', { state: 'open', rev: '3' }, { state: 'deleted' })).toBe('deleted')
    expect(at('ticket:CND-4', { state: 'deleted' }, { state: 'deleted' })).toBe('')
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
    // The folder the turn ran in, as its stamp names it (#705).
    const folder = '/home/me/.local/share/conductor/data/brakeman/folders/default'
    expect(provenance({ ...r, agent: { ...r.agent, folder } }, 'Claude Code')).toBe(`Claude Code · model sonnet · version 2.1.287 · request br-3 · as of 2026-11-02T09:00:00Z · ran in ${folder}`)
  })

  it('say where a turn runs and what it reads besides (#705)', () => {
    expect(foldersLine({ turn: '/srv/code/app', reads: [] }, '/srv/code/app')).toBe('Its turns run in /srv/code/app.')
    expect(foldersLine({ turn: '/srv/code/app', reads: ['/srv/other'] }, '/srv/code/app')).toBe('Its turns run in /srv/code/app, and read /srv/other besides.')
    expect(foldersLine({ turn: '/d/brakeman/folders/default', reads: ['/home/me'] }, '/home/me')).toBe('Its turns run in /d/brakeman/folders/default, a folder of their own you trust once, and read /home/me besides.')
    expect(foldersLine({ turn: '/d/f', reads: ['/home/me', '/srv/a', '/srv/b'] }, '/home/me')).toBe('Its turns run in /d/f, a folder of their own you trust once, and read /home/me, /srv/a and /srv/b besides.')
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
    expect(BOUNDS.minutesPerDay).toEqual([n('MinMinutesPerDay'), n('MaxMinutesPerDay')])
    expect(BOUNDS.windowSeconds).toEqual([n('MinWindowSeconds'), n('MaxWindowSeconds')])
    expect(BOUNDS.cooldownMinutes).toEqual([n('MinCooldownMinutes'), n('MaxCooldownMinutes')])
    expect(BOUNDS.questionMinutes).toEqual([n('MinQuestionMinutes'), n('MaxQuestionMinutes')])
    expect(BOUNDS.briefings).toBe(n('MaxBriefings'))
    expect(BRIEFING_TIME.source).toBe(/briefingPattern = regexp\.MustCompile\(`([^`]+)`\)/.exec(src)![1])
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

  it('takes a configuration the server takes', () => {
    expect(configProblems(config(), 'claude')).toEqual({})
    expect(configProblems(config({ enabled: false, agentId: '', cwd: '' }), '')).toEqual({})
    expect(configProblems(config({ model: 'claude-sonnet-4.5', allowConductorTools: true }), 'claude')).toEqual({})
    expect(configProblems(config({ model: 'gpt-5.1-codex' }), 'codex')).toEqual({})
    expect(configProblems(config({ budget: { turnMinutes: 60, readsPerTurn: 2000, turnsPerDay: 500, minutesPerDay: 1440 } }), 'claude')).toEqual({})
    expect(configProblems(config({ budget: { turnMinutes: 1, readsPerTurn: 10, turnsPerDay: 1, minutesPerDay: 10 } }), 'claude')).toEqual({})
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
    expect(Object.keys(configProblems(config({ budget: { turnMinutes: 0, readsPerTurn: 300, turnsPerDay: 40, minutesPerDay: 240 } }), 'claude'))).toEqual(['budget.turnMinutes'])
    expect(Object.keys(configProblems(config({ budget: { turnMinutes: 15, readsPerTurn: 9, turnsPerDay: 40, minutesPerDay: 240 } }), 'claude'))).toEqual(['budget.readsPerTurn'])
    expect(Object.keys(configProblems(config({ budget: { turnMinutes: 15, readsPerTurn: 300, turnsPerDay: 501, minutesPerDay: 240 } }), 'claude'))).toEqual(['budget.turnsPerDay'])
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

describe("the background role's controls, the server's", () => {
  it('holds the wake and the day to their bounds', () => {
    expect(configProblems(config(), 'claude')).toEqual({})
    expect(configProblems(config({ wake: { mode: 'background', windowSeconds: 10, cooldownMinutes: 0, questionMinutes: 1440, briefings: ['07:00', '18:30'] } }), 'claude')).toEqual({})
    const bad = configProblems(config({ wake: { mode: 'always', windowSeconds: 9, cooldownMinutes: 1441, questionMinutes: 0, briefings: ['7:00'] }, budget: { turnMinutes: 15, readsPerTurn: 300, turnsPerDay: 40, minutesPerDay: 9 } }), 'claude')
    expect(Object.keys(bad).sort()).toEqual(['budget.minutesPerDay', 'wake.briefings', 'wake.cooldownMinutes', 'wake.mode', 'wake.questionMinutes', 'wake.windowSeconds'])
    expect(bad['wake.briefings']).toBe('7:00 is not a time of day: HH:MM, 00:00 to 23:59.')
    expect(configProblems(config({ wake: { mode: 'background', windowSeconds: 120, cooldownMinutes: 30, questionMinutes: 15, briefings: ['07:00', '07:00'] } }), 'claude')['wake.briefings']).toBe('07:00 is named twice.')
    const many = Array.from({ length: 25 }, (_, i) => `${String(i % 24).padStart(2, '0')}:${String(i).padStart(2, '0')}`)
    expect(configProblems(config({ wake: { mode: 'background', windowSeconds: 120, cooldownMinutes: 30, questionMinutes: 15, briefings: many } }), 'claude')['wake.briefings']).toBe('At most 24 briefing times.')
  })

  it('reads the briefing times from the text the form holds, and back', () => {
    expect(briefingList(' 07:00, 18:30  12:00,')).toEqual(['07:00', '18:30', '12:00'])
    expect(briefingList('')).toEqual([])
    expect(briefingLine(['07:00', '18:30'])).toBe('07:00, 18:30')
    expect(briefingLine(undefined)).toBe('')
  })

  it('names a wake request and an over-budget one', () => {
    expect(kindWords('wake')).toBe('Woken by events')
    expect(KINDS.some((k) => k.value === 'wake')).toBe(false)
    const r = { state: 'over_budget', why: 'the day\'s minutes are spent: 1 of 240 left', config: { budget: { turnMinutes: 15 } } } as never
    expect(stateWords(r)).toBe("not started: the day's minutes are spent: 1 of 240 left")
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

describe("the grants in Settings and what Brakeman did under them (PR-13)", () => {
  it("are the server's closed list, each with what it authorizes in words, none an approval", () => {
    const src = readFileSync(new URL('../../../internal/brakeman/config.go', import.meta.url), 'utf8')
    const names = Object.fromEntries([...src.matchAll(/\b(Act[A-Za-z]+)\s*=\s*"([a-z_]+)"/g)].map((m) => [m[1], m[2]]))
    const list = /var Grantable = \[\]string\{([^}]*)\}/.exec(src)![1]!.split(',').map((k) => names[k.trim()])
    expect(GRANTS.map((g) => g.value)).toEqual(list)
    for (const g of GRANTS) {
      expect(g.words.length).toBeGreaterThan(30)
      expect(g.words).not.toMatch(/^Approve/)
    }
    expect(GRANTS.find((g) => g.value === 'launch_approved_plan')!.words).toContain('never approves')
    expect(grantLabel('prepare_followup')).toBe('Prepare a follow-up')
    expect(grantLabel('nope')).toBe('nope')
  })

  it('say observe only when nothing is drafted, proposed or granted: a grant runs by itself, with drafting and proposing off', () => {
    expect(observesOnly({ draft: false, propose: false })).toBe(true)
    expect(observesOnly({ draft: false, propose: false, act: [] })).toBe(true)
    expect(observesOnly({ draft: false, propose: false, act: ['launch_approved_plan'] })).toBe(false)
    expect(observesOnly({ draft: true, propose: false, act: [] })).toBe(false)
    expect(observesOnly({ draft: false, propose: true })).toBe(false)
  })

  it('save a grant list in their order, each once, and a save with none as no grant', () => {
    expect(withGrant(undefined, 'prepare_followup', true)).toEqual(['prepare_followup'])
    expect(withGrant(['prepare_followup'], 'launch_approved_plan', true)).toEqual(['launch_approved_plan', 'prepare_followup'])
    expect(withGrant(['launch_approved_plan', 'prepare_followup'], 'launch_approved_plan', false)).toEqual(['prepare_followup'])
    expect(withGrant(['launch_approved_plan'], 'launch_approved_plan', true)).toEqual(['launch_approved_plan'])
    // The server leaves an empty grant list out: a form with [] is the one saved without it.
    expect(sameConfig(config({ permissions: { observe: true, draft: true, propose: true, act: [] } }), config())).toBe(true)
    expect(sameConfig(config({ permissions: { observe: true, draft: true, propose: true, act: ['prepare_followup', 'launch_approved_plan'] } }), config({ permissions: { observe: true, draft: true, propose: true, act: ['launch_approved_plan', 'prepare_followup'] } }))).toBe(true)
    expect(sameConfig(config({ permissions: { observe: true, draft: true, propose: true, act: ['launch_approved_plan'] } }), config())).toBe(false)
  })

  it("offer only the grants the server's guide lists under the scope act, and none while it is unread", () => {
    expect(servedGrants(null)).toBeNull()
    const served = servedGrants([
      { name: 'act', scope: 'act' },
      { name: 'launch_approved_plan', scope: 'act' },
      { name: 'prepare_followup', scope: 'act' },
      { name: 'run_pause', scope: 'admin' },
    ])!
    expect([...served].sort()).toEqual(['launch_approved_plan', 'prepare_followup'])
    expect(served.has('request_repair')).toBe(false)
  })

  it('let a grant that is on be turned off whatever the guide, even unread or failed, and turned on only when the guide serves it', () => {
    const served = servedGrants([{ name: 'launch_approved_plan', scope: 'act' }])
    // The guide unread, or its read failed: a grant on can be turned off and saved without it; one off cannot be turned on.
    expect(grantHeld(null, true, 'launch_approved_plan')).toBe(false)
    expect(grantHeld(null, false, 'launch_approved_plan')).toBe(true)
    expect(withGrant(['launch_approved_plan'], 'launch_approved_plan', false)).toEqual([])
    // Read: a served grant either way; one not served only off, once on.
    expect(grantHeld(served, false, 'launch_approved_plan')).toBe(false)
    expect(grantHeld(served, true, 'launch_approved_plan')).toBe(false)
    expect(grantHeld(served, false, 'request_repair')).toBe(true)
    expect(grantHeld(served, true, 'request_repair')).toBe(false)
  })

  const proposal = (op: string, target: string, args?: Record<string, unknown>): Extract<BriefItem, { kind: 'proposal/1' }> => ({ kind: 'proposal/1', text: 'Launch it.', op, target, args, expectedRevision: '3' })
  const OP = 'bk' + '0'.repeat(30)
  const act = (over: Partial<BrakemanAct>): BrakemanAct => ({ opId: 'a1', operation: 'launch_approved_plan', target: 'ticket:CND-4', op: OP, state: 'done', receipt: '0123456789abcdef', at: '2026-11-05T10:00:00Z', ...over })

  it("finds the act Brakeman ran for a proposal by the opId the proposal names: its operation on the proposal's ticket, and no other", () => {
    const acts = [act({ opId: 'a1', at: '2026-11-05T10:00:00Z' }), act({ opId: 'a2', at: '2026-11-05T10:05:00Z', receipt: 'fedcba9876543210' }), act({ opId: 'f1', operation: 'prepare_followup', receipt: '1111111111111111' })]
    expect(actFor(proposal('launch_approved_plan', 'ticket:CND-4', { opId: 'a1' }), acts)?.opId).toBe('a1')
    expect(actFor(proposal('launch_approved_plan', 'plan:CND-4/2', { plan: '0123456789abcdef', opId: 'a2' }), acts)?.opId).toBe('a2')
    expect(actFor(proposal('prepare_followup', 'plan:CND-4/1', { opId: 'f1' }), acts)?.opId).toBe('f1')
    // Named by no opId, or by one of no act of its operation on its ticket: a proposal, though Brakeman ran another of the same kind there.
    expect(actFor(proposal('launch_approved_plan', 'ticket:CND-4'), acts)).toBeUndefined()
    expect(actFor(proposal('launch_approved_plan', 'plan:CND-4/2', { plan: '0123456789abcdef' }), acts)).toBeUndefined()
    expect(actFor(proposal('prepare_followup', 'plan:CND-4/1', { opId: 'never-ran' }), acts)).toBeUndefined()
    expect(actFor(proposal('launch_approved_plan', 'ticket:CND-4', { opId: 'f1' }), acts)).toBeUndefined()
    expect(actFor(proposal('launch_approved_plan', 'ticket:CND-5', { opId: 'a1' }), acts)).toBeUndefined()
    expect(actFor(proposal('launch_approved_plan', 'ticket:CND-4', { opId: 7 }), acts)).toBeUndefined()
    // Not run for this proposal: another operation, no acts, a target of no ticket.
    expect(actFor(proposal('ticket_edit', 'ticket:CND-4', { opId: 'a1' }), acts)).toBeUndefined()
    expect(actFor(proposal('launch_approved_plan', 'ticket:CND-4', { opId: 'a1' }), undefined)).toBeUndefined()
    expect(actFor(proposal('run_pause', `run:${RUN}`, { opId: 'a1' }), acts)).toBeUndefined()
    expect(refTicket('comment:CND-4/00112233aabbccdd')).toBe('CND-4')
    expect(refTicket(`run:${RUN}`)).toBe('')
  })

  it('tells two proposals of one operation on one ticket apart: only the one Brakeman ran is done', () => {
    const acts = [act({ opId: 'r1', receipt: '0123456789abcdef' })]
    const ran = proposal('launch_approved_plan', 'plan:CND-4/1', { plan: '0123456789abcdef', opId: 'r1' })
    const other = proposal('launch_approved_plan', 'plan:CND-4/2', { plan: 'fedcba9876543210' })
    expect(actFor(ran, acts)?.state).toBe('done')
    expect(actFor(other, acts)).toBeUndefined()
    const followups = [act({ opId: 'f1', operation: 'prepare_followup', receipt: '1111111111111111' })]
    expect(actFor(proposal('prepare_followup', 'ticket:CND-4', { crew: 'a', opId: 'f1' }), followups)?.opId).toBe('f1')
    expect(actFor(proposal('prepare_followup', 'ticket:CND-4', { crew: 'b', opId: 'f2' }), followups)).toBeUndefined()
  })

  it('shows apart every act of the request that no proposal shows, so an act the brief never names still shows', () => {
    const acts = [act({ opId: 'a1' }), act({ opId: 'a2', receipt: 'fedcba9876543210' }), act({ opId: 'r1', state: 'refused', receipt: undefined })]
    const fact: BriefItem = { kind: 'fact/1', text: 'CND-4 was launched.', evidence: ['ticket:CND-4'] }
    // The live check's briefs: a fact and an outcome, no proposal naming the act; every act shows apart.
    expect(actsApart([fact, { kind: 'outcome/1', text: 'Launched.', receipt: 'op:brakeman/bk' + '0'.repeat(30) }], acts).map((a) => a.opId)).toEqual(['a1', 'a2', 'r1'])
    // A proposal that names one draws it; the rest show apart, in the order they ran.
    expect(actsApart([fact, proposal('launch_approved_plan', 'ticket:CND-4', { opId: 'a2' })], acts).map((a) => a.opId)).toEqual(['a1', 'r1'])
    expect(actsApart([fact, proposal('launch_approved_plan', 'ticket:CND-4', { opId: 'never-ran' })], acts)).toHaveLength(3)
    expect(actsApart([fact], undefined)).toEqual([])
    expect(actsApart([fact], [])).toEqual([])
  })

  it('says "Brakeman did this" with its receipt only for an act done, and what became of any other', () => {
    expect(actWords(act({}), { state: 'started', runId: 'r-1' })).toBe(`Brakeman did this, under the grant "Launch an approved plan" you gave it: receipt ${OP}; the launch 0123456789abcdef of CND-4.`)
    expect(actWords(act({ operation: 'prepare_followup', target: 'plan:CND-4/1', receipt: 'fedcba9876543210' }))).toBe(`Brakeman did this, under the grant "Prepare a follow-up" you gave it: receipt ${OP}; the plan revision fedcba9876543210 of CND-4.`)
    expect(actLaunchRef(act({}))).toBe('launch:0123456789abcdef')
    expect(actWords(act({ state: 'pending', receipt: undefined }))).toContain('is doing this')
    expect(actWords(act({ state: 'pending', receipt: undefined }))).toContain('is doing this')
    expect(actWords(act({ state: 'refused', receipt: undefined, why: 'revision 1 of CND-4 is proposed' }))).toBe('Brakeman tried this, under the grant "Launch an approved plan", and it was refused: revision 1 of CND-4 is proposed. Nothing was done.')
    expect(actWords(act({ state: 'not_run', receipt: undefined }))).toContain('not run again')
    for (const a of [act({ state: 'refused' }), act({ state: 'pending' }), act({ operation: 'prepare_followup' }), act({ receipt: 'nope' })]) expect(actLaunchRef(a)).toBeNull()
  })

  it("names the act's receipt as the outcome's chip does, and the launch's id as the launch of its ticket (#715)", () => {
    // The live check's brief: an outcome citing the act's receipt by the ref the act's answer gave. The act's card shows that same ref,
    // labelled as the outcome's chip labels it, and the launch's id as the launch, never as the receipt.
    const a = act({})
    const outcome = { kind: 'outcome/1', text: 'Launched.', receipt: `op:brakeman/${a.op}` } as const
    expect(actReceiptRef(a)).toBe(outcome.receipt)
    expect(actWords(a, { state: 'started', runId: 'r-1' })).toContain(`: ${refLabel(outcome.receipt)}; `)
    expect(actWords(a, { state: 'started', runId: 'r-1' })).not.toContain('receipt 0123456789abcdef')
    expect(refLabel(actLaunchRef(a)!)).toBe('launch 0123456789abcdef')
    expect(actMade(a)).toBe('the launch 0123456789abcdef of CND-4')
    expect(actMade(act({ operation: 'request_repair', target: `run:${RUN}`, receipt: 'r-1' }))).toBe('r-1, what it made')
    // Not done, or with an op that is no op ref: no receipt.
    for (const x of [act({ state: 'refused' }), act({ state: 'pending', receipt: undefined }), act({ op: 'no/op' }), act({ op: '' })]) expect(actReceiptRef(x)).toBeNull()
    expect(actMade(act({ state: 'refused' }))).toBe('')
    expect(actWords(act({ op: '', operation: 'prepare_followup', receipt: undefined }))).toBe('Brakeman did this, under the grant "Prepare a follow-up" you gave it.')
  })

  it('says a launch is done only once its run has started: pending while it is being started or unread, failed with its words', () => {
    const a = act({})
    // Dispatch keeps the launch, and its receipt done, before the run is started: the launch says what came of it.
    expect([actShown(a, { state: 'started', runId: 'r-1' }), actWords(a, { state: 'started', runId: 'r-1' })]).toEqual(['done', `Brakeman did this, under the grant "Launch an approved plan" you gave it: receipt ${OP}; the launch 0123456789abcdef of CND-4.`])
    expect([actShown(a, { state: 'pending', runId: 'r-1' }), actWords(a, { state: 'pending', runId: 'r-1' })]).toEqual(['pending', `Brakeman launched this, under the grant "Launch an approved plan": receipt ${OP}; the launch 0123456789abcdef of CND-4. Its run is still being started.`])
    expect([actShown(a, null), actWords(a, null)]).toEqual(['pending', `Brakeman launched this, under the grant "Launch an approved plan": receipt ${OP}; the launch 0123456789abcdef of CND-4. Whether its run has started is not read yet.`])
    const failed = { state: 'failed', runId: 'r-1', why: 'the agent could not be started' }
    expect([actShown(a, failed), actWords(a, failed)]).toEqual(['failed', `Brakeman launched this, under the grant "Launch an approved plan": receipt ${OP}, but the launch 0123456789abcdef of CND-4 failed: the agent could not be started. No run was started.`])
    for (const l of [{ state: 'started' }, { state: 'pending' }, failed, null]) expect(actWords(a, l)).not.toBe(actWords(act({ state: 'refused' })))
    // Not a launch's act, or not done: its own state, whatever launch is given.
    expect(actShown(act({ operation: 'prepare_followup', receipt: 'fedcba9876543210' }), null)).toBe('done')
    expect(actShown(act({ state: 'refused' }), { state: 'started' })).toBe('refused')
    expect(actShown(act({ state: 'pending', receipt: undefined }), failed)).toBe('pending')
  })

  it("draws a proposal's act afresh from the act and its launch as kept by the launch's id, so a re-read of the same act changes nothing", () => {
    const a = act({})
    expect(actLaunchId(a)).toBe('0123456789abcdef')
    expect(actLaunchId(act({ operation: 'prepare_followup', receipt: 'fedcba9876543210' }))).toBe('')
    const failed = { '0123456789abcdef': { state: 'failed', why: 'the agent could not be started' } }
    // The request read again hands the same act as a new object: it shows failed still, and the proposal is offered again.
    expect(actShownFrom(a, failed)).toBe('failed')
    expect(actShownFrom({ ...a }, failed)).toBe('failed')
    expect(proposalOffered(actShownFrom(structuredClone(a), failed))).toBe(true)
    // Under way (the launch unread or pending, an act's receipt not in): no second control.
    for (const [x, kept] of [
      [a, {}],
      [a, { '0123456789abcdef': { state: 'pending' } }],
      [act({ state: 'pending', receipt: undefined }), failed],
    ] as const) {
      expect(actShownFrom({ ...x }, kept)).toBe('pending')
      expect(proposalOffered(actShownFrom({ ...x }, kept))).toBe(false)
    }
    // Done: done, with no control; another launch's read does not count for it.
    expect(actShownFrom({ ...a }, { '0123456789abcdef': { state: 'started', runId: 'r-1' } })).toBe('done')
    expect(proposalOffered('done')).toBe(false)
    expect(actShownFrom(act({ receipt: 'fedcba9876543210' }), failed)).toBe('pending')
    // Refused or never run: the proposal is offered; no act: "".
    expect(proposalOffered(actShownFrom(act({ state: 'refused', receipt: undefined }), {}))).toBe(true)
    expect(actShownFrom(undefined, failed)).toBe('')
    expect(proposalOffered('')).toBe(true)
  })

  it('keeps at most MAX_KEPT_LAUNCHES launches, the latest read of each, the oldest dropped first', () => {
    let kept: Record<string, { state: string }> = {}
    for (let i = 0; i < MAX_KEPT_LAUNCHES + 5; i++) kept = keepLaunch(kept, `l${i}`, { state: 'pending' })
    expect(Object.keys(kept)).toHaveLength(MAX_KEPT_LAUNCHES)
    expect(kept.l0).toBeUndefined()
    expect(kept.l5).toEqual({ state: 'pending' })
    kept = keepLaunch(kept, 'l5', { state: 'failed' })
    expect(kept.l5).toEqual({ state: 'failed' })
    expect(Object.keys(kept).at(-1)).toBe('l5')
  })

  it('reads a launch again while it is pending, and after a failed read, until it has started or failed, and stops when not wanted', async () => {
    const answers: Array<{ state: string } | Error> = [{ state: 'pending' }, new Error('lost'), { state: 'pending' }, { state: 'failed' }, { state: 'started' }]
    const shown: string[] = []
    const waits: number[] = []
    const settled = await followLaunch({
      read: async () => {
        const a = answers.shift()!
        if (a instanceof Error) throw a
        return a
      },
      show: (l) => shown.push(l.state),
      stop: () => false,
      wait: async (ms) => void waits.push(ms),
      firstMs: 2000,
      maxMs: 8000,
    })
    expect(settled).toEqual({ state: 'failed' })
    expect(shown).toEqual(['pending', 'pending', 'failed'])
    expect(waits).toEqual([2000, 4000, 8000])
    expect(answers).toEqual([{ state: 'started' }])
    // No longer wanted (the card shows another act): an answer that comes after is not shown, and nothing more is read.
    let wanted = true
    let reads = 0
    const shownAfter: string[] = []
    const stopped = await followLaunch({
      read: async () => {
        reads++
        wanted = false
        return { state: 'started' }
      },
      show: (l) => shownAfter.push(l.state),
      stop: () => !wanted,
      wait: async () => {},
      firstMs: 2000,
      maxMs: 8000,
    })
    expect([stopped, reads, shownAfter]).toEqual([null, 1, []])
  })

})

describe("Brakeman's reading", () => {
  it('renders what it is unsure of as Markdown, as the summary is: a list stays a list', () => {
    const blocks = readingBlocks({ summary: 'Two runs:\n\n- one failed\n- one passed', uncertainty: '- My read of the logs is partial.\n- This turn read no screen.' })
    expect(blocks.map((b) => b.key)).toEqual(['summary', 'uncertainty'])
    expect(blocks[1]!.label).toBe('Uncertain')
    for (const b of blocks) {
      const { html } = renderMarkdown(b.text, { dir: '', relativeLinks: false })
      expect(html).toMatch(/<ul>\s*<li>/)
    }
    expect(readingBlocks({ summary: 'Quiet.' }).map((b) => b.key)).toEqual(['summary'])
    expect(readingBlocks({ summary: 'Quiet.', uncertainty: '  ' }).map((b) => b.key)).toEqual(['summary'])
  })
})
