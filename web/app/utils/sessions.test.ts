import { describe, expect, it } from 'vitest'
import type { SessionInfo } from '~/composables/useSessions'
import { ApiError } from '~/composables/useApi'
import { agentInitials, filterSessions, gonePage, groupSessions, homeLeavesFor, initials, listLeavesFor, relativeTime, sessionHeadline, sessionMeta, sessionWorthOpening, shortCwd } from './sessions'

function s(p: Partial<SessionInfo>): SessionInfo {
  return { id: 'x', name: 'n', kind: 'server', agentId: 'claude', command: ['claude'], cwd: '/srv', status: 'running', cols: 80, rows: 24, viewers: 0, createdAt: '2026-09-28T10:00:00Z', ...p }
}

describe('groupSessions', () => {
  it('puts needs_input first, then running, then ended, newest attention first', () => {
    const g = groupSessions([
      s({ id: 'a', status: 'running' }),
      s({ id: 'b', attention: { state: 'needs_input', since: '2026-09-28T10:05:00Z' } }),
      s({ id: 'c', status: 'exited' }),
      s({ id: 'd', attention: { state: 'needs_input', since: '2026-09-28T10:06:00Z' } }),
    ])
    expect(g.needs.map((x) => x.id)).toEqual(['d', 'b'])
    expect(g.running.map((x) => x.id)).toEqual(['a'])
    expect(g.exited.map((x) => x.id)).toEqual(['c'])
  })
  it('an ended session that still carries needs_input is exited, not needs', () => {
    const g = groupSessions([s({ id: 'a', status: 'stopped', attention: { state: 'needs_input' } })])
    expect(g.needs).toEqual([])
    expect(g.exited.length).toBe(1)
  })
})

describe('filterSessions', () => {
  const list = [s({ id: 'a', name: 'auth-refactor', cwd: '/home/jd/src/api', hostName: 'mac-jd' }), s({ id: 'b', name: 'flaky-e2e', cwd: '/srv/web' })]
  it('matches name, cwd, host and agent case-insensitively', () => {
    expect(filterSessions(list, 'AUTH').map((x) => x.id)).toEqual(['a'])
    expect(filterSessions(list, 'srv/web').map((x) => x.id)).toEqual(['b'])
    expect(filterSessions(list, 'mac-').map((x) => x.id)).toEqual(['a'])
    expect(filterSessions(list, 'claude').length).toBe(2)
  })
  it('empty query returns everything', () => {
    expect(filterSessions(list, '  ').length).toBe(2)
  })
})

describe('agentInitials', () => {
  it('maps known agents and falls back to two letters', () => {
    expect(agentInitials('claude')).toBe('CC')
    expect(agentInitials('claude-code')).toBe('CC')
    expect(agentInitials('codex')).toBe('CX')
    expect(agentInitials('agy')).toBe('AG')
    expect(agentInitials('bash')).toBe('$_')
    expect(agentInitials('aider')).toBe('AI')
    expect(agentInitials('')).toBe('?')
  })
})

describe('relativeTime', () => {
  const now = Date.parse('2026-09-28T12:00:00Z')
  it('formats short durations', () => {
    expect(relativeTime('2026-09-28T11:59:40Z', now)).toBe('20s')
    expect(relativeTime('2026-09-28T11:56:00Z', now)).toBe('4m')
    expect(relativeTime('2026-09-28T10:56:00Z', now)).toBe('1h 4m')
    expect(relativeTime('2026-09-26T10:56:00Z', now)).toBe('2d')
    expect(relativeTime('2026-09-28T11:22:00Z', now, { suffix: true })).toBe('38m ago')
  })
})

describe('shortCwd', () => {
  it('collapses home directories to ~', () => {
    expect(shortCwd('/home/jd/src/api')).toBe('~/src/api')
    expect(shortCwd('/Users/jd')).toBe('~')
    expect(shortCwd('/srv/web')).toBe('/srv/web')
  })
})

describe('sessionMeta', () => {
  const now = Date.parse('2026-09-28T12:00:00Z')
  it('shortens the home directory and names the host', () => {
    expect(sessionMeta(s({ cwd: '/home/jd/src/api', kind: 'hosted', hostName: 'mac-jd', viewers: 3, createdAt: '2026-09-28T11:38:00Z' }), now)).toBe('~/src/api · hosted · 3 here')
    expect(sessionMeta(s({ cwd: '/srv/web', kind: 'server', viewers: 0, createdAt: '2026-09-28T11:56:00Z' }), now)).toBe('/srv/web · server · 4m')
  })
})

describe('initials', () => {
  it('takes the first letter of the first two words', () => {
    expect(initials('Priya Shah')).toBe('PS')
    expect(initials('jd')).toBe('JD')
    expect(initials('')).toBe('?')
  })
})

describe('the home and the list page at each width (#359)', () => {
  const waiting = s({ id: 'w', attention: { state: 'needs_input', since: '2026-09-28T10:05:00Z' } as SessionInfo['attention'] })
  const older = s({ id: 'r1', createdAt: '2026-09-28T09:00:00Z' })
  const newer = s({ id: 'r2', createdAt: '2026-09-28T11:00:00Z' })
  const ended = s({ id: 'e', status: 'exited' })

  it('opens someone waiting first, else the newest running session, never an ended one', () => {
    expect(sessionWorthOpening([older, waiting, newer])?.id).toBe('w')
    expect(sessionWorthOpening([older, newer, ended])?.id).toBe('r2')
    expect(sessionWorthOpening([ended])).toBe(null)
    expect(sessionWorthOpening([])).toBe(null)
  })
  it('below lg the home goes to the list, which stays', () => {
    expect(homeLeavesFor(false, null)).toBe('/sessions')
    expect(homeLeavesFor(false, newer)).toBe('/sessions')
    expect(listLeavesFor(false, null)).toBe(null)
    expect(listLeavesFor(false, newer)).toBe(null)
  })
  it('at lg and up the list always leaves: to the session worth opening, else to the home, which stays on its welcome', () => {
    expect(listLeavesFor(true, newer)).toBe('/sessions/r2')
    expect(listLeavesFor(true, null)).toBe('/')
    expect(homeLeavesFor(true, newer)).toBe('/sessions/r2')
    expect(homeLeavesFor(true, null)).toBe(null)
  })
})

describe('sessionHeadline', () => {
  it('reads agent, folder and branch, the whole path as its title, and no id', () => {
    const h = sessionHeadline(s({ agentId: 'claude', cwd: '/srv/work/api', branch: 'main', agentSession: { id: 'a1b2c3', source: 'set', resumable: true } }))
    expect(h).toEqual({ line: 'Claude Code · api · main', title: '/srv/work/api' })
    expect(h.line).not.toContain('a1b2c3')
    expect(h.line).not.toContain('server')
  })
  it('keeps a hosted session machine in the line and its user and host in the title', () => {
    const h = sessionHeadline(s({ kind: 'hosted', agentId: 'codex', cwd: '/home/ana/src/web/', hostName: 'build-box', hostUser: 'ana' }))
    expect(h.line).toBe('Codex on build-box · web')
    expect(h.title).toBe('/home/ana/src/web/ · ana on build-box')
  })
  it('names an agent it does not know by its id, and a Windows folder by its last part', () => {
    expect(sessionHeadline(s({ agentId: 'pi', cwd: 'C:\\Users\\ana\\repo' })).line).toBe('pi · repo')
  })
})

describe('gonePage', () => {
  const gone = new ApiError(404, 'not_found', 'no such session')
  it('opens on the record when the server no longer holds the session but kept its record', () => {
    expect(gonePage(gone, { ok: { name: 'auth-refactor' } })).toEqual({ kind: 'record', name: 'auth-refactor' })
  })
  it('says nothing is kept when the record is gone too', () => {
    expect(gonePage(gone, { failed: new ApiError(404, 'not_found', 'no such record') })).toEqual({ kind: 'forgotten', history: true })
  })
  it("gives a record's failed read in its own words, with no way to History where the server keeps none", () => {
    expect(gonePage(gone, { failed: new ApiError(500, 'internal', 'the journal could not be read') })).toEqual({ kind: 'unread', words: 'the journal could not be read', history: true })
    expect(gonePage(gone, { failed: new ApiError(404, 'history_unavailable', 'this server keeps no history') })).toEqual({ kind: 'unread', words: 'this server keeps no history', history: false })
  })
  it('is no gone page for any other error: the alert stays', () => {
    expect(gonePage(new ApiError(500, 'internal', 'boom'), { ok: { name: 'x' } })).toBeNull()
    expect(gonePage(new ApiError(401, 'unauthorized', 'token'), { ok: { name: 'x' } })).toBeNull()
    expect(gonePage(new ApiError(404, 'history_unavailable', 'x'), { ok: { name: 'x' } })).toBeNull()
    expect(gonePage(new Error('network'), { ok: { name: 'x' } })).toBeNull()
  })
})
