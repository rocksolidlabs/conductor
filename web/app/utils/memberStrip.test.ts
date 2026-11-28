import { describe, expect, it } from 'vitest'
import type { RunInfo, RunMember, SessionInfo } from '~/composables/useSessions'
import type { Attention } from './protocol'
import { backByHistory, backTitle, BACK_TO_CREW, carriedQuery, keepIfHere, memberOfRun, memberPlace, memberTabs, placeOfAddress, READ_SAID, readSaidFor, sessionPlace, stripRunOf, stripsRun } from './memberStrip'

const NOW = Date.parse('2026-11-26T12:00:00Z')
const MIN = 60_000
const ago = (ms: number) => new Date(NOW - ms).toISOString()
const RUN = 'refund-1a2b3c4d'
const sess = (id: string, attention?: Attention, over: Partial<SessionInfo> = {}) => ({ id, status: 'running', turns: true, attention, ...over }) as unknown as SessionInfo
const member = (name: string, over: Partial<RunMember> = {}): RunMember => ({ name, agentId: 'claude', start: { when: 'immediately' }, status: 'running', ...over }) as RunMember
const run = (members: RunMember[], over: Partial<RunInfo> = {}): RunInfo =>
  ({ id: RUN, crewId: 'refund', name: 'refund', label: 'Refund split', goal: '', cwd: '/', isolation: 'none', startedAt: ago(30 * MIN), members, log: [], state: 'running', needsInput: 0, yolo: false, ...over }) as RunInfo

const LEAD = sess('s-lead', { state: 'working', since: ago(MIN) }, { crew: { runId: RUN, crewId: 'refund', member: 'lead' } })
const REVIEW = sess('s-review', { state: 'needs_input', since: ago(MIN), id: 'q1' }, { crew: { runId: RUN, crewId: 'refund', member: 'review' } })
const THREE = run([member('lead', { sessionId: LEAD.id }), member('core', { status: 'pending' }), member('review', { sessionId: REVIEW.id })])

describe('memberTabs: a tab for each member of the run', () => {
  it("draws each member in the run's order with its mark, the open one selected", () => {
    const tabs = memberTabs(THREE, [LEAD, REVIEW], REVIEW.id, NOW)
    expect(tabs.map((t) => [t.name, t.mark.key, t.mark.word, t.selected])).toEqual([
      ['lead', 'working', 'working', false],
      ['core', 'waiting', 'waiting to start', false],
      ['review', 'needs', 'needs you', true],
    ])
  })
  it('gives a member with no session yet no session: its tab cannot be pressed', () => {
    const tabs = memberTabs(THREE, [LEAD, REVIEW], LEAD.id, NOW)
    expect(tabs.map((t) => t.sessionId)).toEqual([LEAD.id, undefined, REVIEW.id])
    expect(tabs.filter((t) => t.selected).map((t) => t.name)).toEqual(['lead'])
  })
  it('finds a member by its crew tag before the run names its session, and by the run alone once the session has left', () => {
    const early = run([member('lead', { status: 'starting' })])
    expect(memberTabs(early, [LEAD], LEAD.id, NOW)[0]).toMatchObject({ sessionId: LEAD.id, selected: true })
    const gone = run([member('lead', { status: 'ended', sessionId: 's-gone' })])
    expect(memberTabs(gone, [], 's-gone', NOW)[0]).toMatchObject({ sessionId: 's-gone', selected: true, mark: { key: 'exited' } })
  })
  it("selects no tab on an earlier attempt's session of a member", () => {
    const earlier = sess('s-lead-1', undefined, { status: 'exited', crew: { runId: RUN, crewId: 'refund', member: 'lead', task: 'build', attempt: 1 } })
    const tabs = memberTabs(THREE, [earlier, LEAD, REVIEW], earlier.id, NOW)
    expect(tabs.some((t) => t.selected)).toBe(false)
  })
})

describe('stripRunOf and stripsRun: which pages carry the strip', () => {
  it("is a member's run, and none for a session of its own or a Brakeman turn", () => {
    expect(stripRunOf(LEAD)).toBe(RUN)
    expect(stripRunOf(sess('s-own'))).toBe('')
    expect(stripRunOf(null)).toBe('')
    expect(stripRunOf(sess('s-turn', undefined, { crew: { runId: 'brakeman-1', crewId: 'brakeman', member: 'brakeman', brakeman: { request: 'r1', quiet: false } } }))).toBe('')
    expect(stripsRun(THREE)).toBe(true)
    expect(stripsRun(run([], { class: 'brakeman' }))).toBe(false)
  })
})

describe('memberPlace and memberOfRun: a member opens inside its crew (UX3 R72)', () => {
  const TURN = sess('s-turn', undefined, { crew: { runId: 'brakeman-1', crewId: 'brakeman', member: 'brakeman', brakeman: { request: 'r1', quiet: false } } })
  it("is a member's run page with its session, its tab, open and line carried; none for a session of its own or a Brakeman turn", () => {
    expect(memberPlace(LEAD)).toBe(`/runs/${RUN}?session=s-lead`)
    expect(memberPlace(LEAD, { tab: 'changes', open: 'a b.go', line: '12', view: 'graph' })).toBe(`/runs/${RUN}?session=s-lead&tab=changes&open=a+b.go&line=12`)
    expect(memberPlace(sess('s-own'))).toBe('')
    expect(memberPlace(TURN)).toBe('')
    expect(carriedQuery({ tab: ['a', 'b'], open: '' })).toBe('')
    expect(carriedQuery({ open: 'x.go' })).toBe('?open=x.go')
  })
  it("draws a session in a run's place only when it is that run's member", () => {
    expect(memberOfRun(LEAD, RUN)).toBe(true)
    expect(memberOfRun(LEAD, 'other-run')).toBe(false)
    expect(memberOfRun(LEAD, '')).toBe(false)
    expect(memberOfRun(sess('s-own'), RUN)).toBe(false)
    expect(memberOfRun(TURN, 'brakeman-1')).toBe(false)
  })
  it('goes Back by the history only to the run page with no member open', () => {
    const path = `/runs/${RUN}`
    expect(backByHistory(path, path)).toBe(true)
    expect(backByHistory(`${path}?view=graph`, path)).toBe(true)
    expect(backByHistory(`${path}?session=s-core`, path)).toBe(false)
    expect(backByHistory('/crews', path)).toBe(false)
    expect(backByHistory(null, path)).toBe(false)
  })
})

describe('sessionPlace: every in-app link to a session goes to its place (UX3 R72)', () => {
  const TURN = sess('s-turn', undefined, { crew: { runId: 'brakeman-1', crewId: 'brakeman', member: 'brakeman', brakeman: { request: 'r1', quiet: false } } })
  it("is a member's place inside its crew, so a press on the member open is the address the page is at", () => {
    expect(sessionPlace(LEAD, LEAD.id)).toBe(`/runs/${RUN}?session=s-lead`)
    expect(sessionPlace(LEAD, LEAD.id, { tab: 'changes' })).toBe(`/runs/${RUN}?session=s-lead&tab=changes`)
    expect(sessionPlace(LEAD, LEAD.id, { open: 'a.go', line: 12 })).toBe(`/runs/${RUN}?session=s-lead&open=a.go&line=12`)
  })
  it("is a session's own page for one of its own, a Brakeman turn, and one the page does not know yet", () => {
    expect(sessionPlace(sess('s-own'), 's-own')).toBe('/sessions/s-own')
    expect(sessionPlace(TURN, 's-turn')).toBe('/sessions/s-turn')
    expect(sessionPlace(undefined, 's-x', { open: 'b c.go', tab: '' })).toBe('/sessions/s-x?open=b+c.go')
  })
})

describe('placeOfAddress and keepIfHere: an address the server builds, and the place already open', () => {
  const find = (id: string) => (id === LEAD.id ? LEAD : id === 's-own' ? sess('s-own') : undefined)
  it("maps a session's address to its place, carrying tab, open and line; leaves any other address", () => {
    expect(placeOfAddress('/sessions/s-lead', find)).toBe(`/runs/${RUN}?session=s-lead`)
    expect(placeOfAddress('/sessions/s-lead?tab=files&open=a.go&line=3', find)).toBe(`/runs/${RUN}?session=s-lead&tab=files&open=a.go&line=3`)
    expect(placeOfAddress('/sessions/s-own', find)).toBe('/sessions/s-own')
    expect(placeOfAddress('/history/sessions/s-lead', find)).toBe('/history/sessions/s-lead')
    expect(placeOfAddress('/runs/r?view=graph', find)).toBe('/runs/r?view=graph')
  })
  it("is the page's own address for the member already open, whatever its own tab, open or line", () => {
    const here = { path: `/runs/${RUN}`, query: { session: 's-lead', tab: 'files', open: 'a.go' }, fullPath: `/runs/${RUN}?session=s-lead&tab=files&open=a.go` }
    expect(keepIfHere(`/runs/${RUN}?session=s-lead`, here)).toBe(here.fullPath)
    expect(keepIfHere(`/runs/${RUN}?session=s-core`, here)).toBe(`/runs/${RUN}?session=s-core`)
    expect(keepIfHere('/sessions/s-own', here)).toBe('/sessions/s-own')
    expect(keepIfHere(`/runs/${RUN}?session=s-lead`, { path: `/runs/${RUN}`, query: {}, fullPath: `/runs/${RUN}` })).toBe(`/runs/${RUN}?session=s-lead`)
  })
})

describe('readSaidFor: a move a read made is the landing page\'s to keep', () => {
  it('marks the entry for the session the read was of, and nothing else', () => {
    expect(readSaidFor({ [READ_SAID]: 's-lead', back: '/x' }, 's-lead')).toBe(true)
    expect(readSaidFor({ [READ_SAID]: 's-lead' }, 's-core')).toBe(false)
    expect(readSaidFor(null, 's-lead')).toBe(false)
    expect(readSaidFor({ [READ_SAID]: '' }, '')).toBe(false)
  })
})

describe('backTitle: where Back to the crew goes', () => {
  it("names the run by its title once read, else says it plainly", () => {
    expect(backTitle(THREE)).toBe('Back to Refund split')
    expect(backTitle(run([], { label: undefined }))).toBe('Back to refund')
    expect(backTitle(undefined)).toBe(BACK_TO_CREW)
  })
})
