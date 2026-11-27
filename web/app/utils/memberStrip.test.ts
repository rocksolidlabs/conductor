import { describe, expect, it } from 'vitest'
import type { RunInfo, RunMember, SessionInfo } from '~/composables/useSessions'
import type { Attention } from './protocol'
import { backTitle, BACK_TO_CREW, stripRunOf, stripsRun } from './memberStrip'

const NOW = Date.parse('2026-11-26T12:00:00Z')
const MIN = 60_000
const ago = (ms: number) => new Date(NOW - ms).toISOString()
const RUN = 'refund-1a2b3c4d'
const sess = (id: string, attention?: Attention, over: Partial<SessionInfo> = {}) => ({ id, status: 'running', turns: true, attention, ...over }) as unknown as SessionInfo
const member = (name: string, over: Partial<RunMember> = {}): RunMember => ({ name, agentId: 'claude', start: { when: 'immediately' }, status: 'running', ...over }) as RunMember
const run = (members: RunMember[], over: Partial<RunInfo> = {}): RunInfo =>
  ({ id: RUN, crewId: 'refund', name: 'refund', label: 'Refund split', goal: '', cwd: '/', isolation: 'none', startedAt: ago(30 * MIN), members, log: [], state: 'running', needsInput: 0, yolo: false, ...over }) as RunInfo

const LEAD = sess('s-lead', { state: 'working', since: ago(MIN) }, { crew: { runId: RUN, crewId: 'refund', member: 'lead' } })
const THREE = run([member('lead', { sessionId: LEAD.id }), member('core', { status: 'pending' }), member('review')])

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

describe('backTitle: where Back to the crew goes', () => {
  it("names the run by its title once read, else says it plainly", () => {
    expect(backTitle(THREE)).toBe('Back to Refund split')
    expect(backTitle(run([], { label: undefined }))).toBe('Back to refund')
    expect(backTitle(undefined)).toBe(BACK_TO_CREW)
  })
})
