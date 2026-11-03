import { describe, expect, it } from 'vitest'
import type { SessionInfo } from '~/composables/useSessions'
import { alerting, liveAttention, liveSession, ATTENTION_DOT_COLOR, answersInTerminal, choiceKey, firstChoice, isActive, needingInput, newlyNeedingInput, titleWithCount, waitingHeading, waitsForTerminal } from './attention'

function s(id: string, state: '' | 'needs_input' | 'working' = '', since = '', status: SessionInfo['status'] = 'running'): SessionInfo {
  return {
    id,
    name: id,
    kind: 'server',
    agentId: 'shell',
    command: ['sh'],
    cwd: '/',
    status,
    cols: 80,
    rows: 24,
    viewers: 0,
    attention: { state, since: since || undefined },
    createdAt: '2026-09-24T00:00:00Z',
  }
}

describe('attention helpers', () => {
  it('lists sessions needing input, newest first, ignoring ended ones', () => {
    const list = needingInput([s('a', 'needs_input', '2026-01-01T00:00:01Z'), s('b', 'needs_input', '2026-01-01T00:00:05Z'), s('c', 'needs_input', '2026-01-01T00:00:09Z', 'exited'), s('d', 'working')])
    expect(list.map((x) => x.id)).toEqual(['b', 'a'])
  })

  it('detects new requests exactly once per signal', () => {
    const prev = new Map([['a', s('a', 'needs_input', 't1')], ['b', s('b')]])
    const next = new Map([['a', s('a', 'needs_input', 't1')], ['b', s('b', 'needs_input', 't2')]])
    expect(newlyNeedingInput(prev, next).map((x) => x.id)).toEqual(['b'])
    // a new signal on the same session (different since) counts again
    const next2 = new Map([['a', s('a', 'needs_input', 't3')]])
    expect(newlyNeedingInput(next, next2).map((x) => x.id)).toEqual(['a'])
    // first snapshot fires for everything already waiting
    expect(newlyNeedingInput(new Map(), next).map((x) => x.id)).toEqual(['a', 'b'])
  })

  it("counts a quiet Brakeman turn's question in Needs you and alerts no one for it; a person's request's turn alerts", () => {
    // docs/plans/brakeman.md, the invariant on Brakeman's questions: the mark is the session's own, from its creation.
    const brakeman = (id: string, quiet: boolean) => ({ ...s(id, 'needs_input', 't1'), name: `Brakeman · ${id}`, crew: { runId: 'brakeman-0a1b2c3d', crewId: 'brakeman', member: 'brakeman', brakeman: { request: id, quiet } } })
    const prev = new Map<string, SessionInfo>()
    const next = new Map<string, SessionInfo>([['br-1', brakeman('br-1', true)], ['br-2', brakeman('br-2', false)], ['p', s('p', 'needs_input', 't2')]])
    expect(needingInput(next.values()).map((x) => x.id).sort()).toEqual(['br-1', 'br-2', 'p'])
    const fresh = newlyNeedingInput(prev, next)
    expect(fresh.map((x) => x.id).sort()).toEqual(['br-1', 'br-2', 'p'])
    expect(alerting(fresh).map((x) => x.id).sort()).toEqual(['br-2', 'p'])
  })

  it('formats the title and active state', () => {
    expect(titleWithCount('Conductor', 0)).toBe('Conductor')
    expect(titleWithCount('Conductor', 3)).toBe('(3) Conductor')
    expect(isActive(s('a'))).toBe(true)
    expect(isActive(s('a', '', '', 'stopped'))).toBe(false)
  })
})

describe('attention dot colour', () => {
  it('is the amber status colour, never the brand terracotta', () => {
    expect(ATTENTION_DOT_COLOR).toBe('#c98a1b')
    expect(ATTENTION_DOT_COLOR).not.toBe('#d26b3f')
  })
})

describe('a turn held for the terminal', () => {
  it('is a needs_input of source submit alone', () => {
    expect(waitsForTerminal({ state: 'needs_input', source: 'submit' })).toBe(true)
    expect(waitsForTerminal({ state: 'needs_input', source: 'api' })).toBe(false)
    expect(waitsForTerminal({ state: 'needs_input' })).toBe(false)
    expect(waitsForTerminal({ state: 'done', source: 'submit' })).toBe(false)
    expect(waitsForTerminal({ state: '', source: 'submit' })).toBe(false)
    expect(waitsForTerminal(undefined)).toBe(false)
  })

  it('heads the reply bar with the terminal, else with the agent', () => {
    expect(waitingHeading({ state: 'needs_input', source: 'submit' }, 'claude')).toBe('Type in the terminal to go on')
    expect(waitingHeading({ state: 'needs_input', source: 'api' }, 'claude')).toBe('claude is waiting on you')
    expect(waitingHeading(undefined, 'codex')).toBe('codex is waiting on you')
  })
})

describe('a question answered in the terminal (#258)', () => {
  it('reads as no question to offer, the mark kept, and every other attention as itself', () => {
    const answered = { state: 'needs_input' as const, message: 'Allow?', source: 'question', id: 'q1', options: [{ label: 'Yes', input: 'y' }], answered: 'terminal' as const }
    expect(liveAttention(answered)).toEqual({ state: '', source: 'input', answered: 'terminal' })
    const open = { state: 'needs_input' as const, message: 'Allow?', source: 'question', id: 'q1' }
    expect(liveAttention(open)).toBe(open)
    const working = { state: 'working' as const, source: 'title' }
    expect(liveAttention(working)).toBe(working)
    expect(liveAttention(undefined)).toBeUndefined()
    const s = { id: 'a', name: 'a', agentId: 'codex', status: 'running', createdAt: '', kind: 'server', attention: answered } as unknown as SessionInfo
    expect(liveSession(s).attention).toEqual({ state: '', source: 'input', answered: 'terminal' })
    const t = { ...s, attention: open } as unknown as SessionInfo
    expect(liveSession(t)).toBe(t)
  })
})

describe('the choices of a dialog read off the screen (#288)', () => {
  const bash = [
    { label: 'Yes', input: '1' },
    { label: 'Yes, and always allow access to /home/dev/project from this project', input: '2' },
    { label: 'Yes, and switch to auto mode · auto mode handles these prompts for you', input: '3', mode: true },
    { label: 'No', input: '4' },
  ]
  const plan = [
    { label: 'Yes, and switch to BYPASS PERMISSIONS (no further prompts)…', input: '1', mode: true },
    { label: 'Yes, manually approve edits', input: '2' },
    { label: 'Tell Claude what to change', input: '3' },
  ]

  it('gives each its number as its key, and none to one that changes the permission mode', () => {
    expect(bash.map(choiceKey)).toEqual([1, 2, undefined, 4])
    expect(plan.map(choiceKey)).toEqual([undefined, 2, 3])
    expect(choiceKey({ label: 'tenth', input: 'x' }, 9)).toBeUndefined()
  })

  it('never offers first a choice that changes the permission mode', () => {
    expect(firstChoice(bash)).toBe(0)
    expect(firstChoice(plan)).toBe(1)
    expect(firstChoice([{ label: 'Yes, and use auto mode', input: '1', mode: true }])).toBe(-1)
    expect(firstChoice(undefined)).toBe(-1)
  })

  it('answers a permission dialog without choices in the terminal', () => {
    expect(answersInTerminal({ state: 'needs_input', kind: 'permission' })).toBe(true)
    expect(answersInTerminal({ state: 'needs_input', kind: 'permission', options: [] })).toBe(true)
    expect(answersInTerminal({ state: 'needs_input', kind: 'permission', options: bash })).toBe(false)
    expect(answersInTerminal({ state: 'needs_input', kind: 'prompt' })).toBe(false)
    expect(answersInTerminal({ state: 'working', kind: 'permission' })).toBe(false)
    expect(answersInTerminal(undefined)).toBe(false)
  })
})
