import { describe, expect, it } from 'vitest'
import type { SessionInfo } from '~/composables/useSessions'
import { alerting, answerDigit, liveAttention, liveSession, ANSWER_IN_TERMINAL, ATTENTION_DOT_COLOR, TERMINAL_FIRST, answersInTerminal, choiceKey, choiceTitle, decisionAnsweredBy, decisionOf, isActive, needingInput, newlyNeedingInput, noReply, offeredChoices, recommendedChoice, startupInTerminal, takesReply, titleWithCount, waitingHeading, waitsForTerminal } from './attention'
import type { Attention, Role } from './protocol'

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
    expect(waitingHeading({ state: 'needs_input', source: 'api' }, 'claude')).toBe('claude asks')
    expect(waitingHeading(undefined, 'codex')).toBe('codex asks')
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

  it('fills only the choice the agent marks "(Recommended)", never a mode choice, and none when two say it', () => {
    expect(recommendedChoice(bash)).toBe(-1)
    expect(recommendedChoice(plan)).toBe(-1)
    expect(recommendedChoice(undefined)).toBe(-1)
    const merge = [
      { label: 'Merge commit', input: '1' },
      { label: 'Squash (Recommended)', input: '2' },
      { label: 'Rebase', input: '3' },
    ]
    expect(recommendedChoice(merge)).toBe(1)
    expect(recommendedChoice([{ label: 'Merge commit (Recommended)', input: '1' }, ...merge.slice(1)])).toBe(-1)
    expect(recommendedChoice([{ label: 'Yes, and use auto mode (Recommended)', input: '1', mode: true }, { label: 'No', input: '2' }])).toBe(-1)
    expect(recommendedChoice([{ label: 'merge', input: '1' }, { label: 'squash (recommended) ', input: '2' }])).toBe(1)
    // Said in the middle of a label, or cut off, it marks nothing.
    expect(recommendedChoice([{ label: 'The (Recommended) way, or not', input: '1' }, { label: 'Squash (Recomm', input: '2' }])).toBe(-1)
  })

  it("never offers first Codex's tool approval choices that remember it, and gives them no number key (#716)", () => {
    const form = [
      { label: 'Allow', input: '1' },
      { label: 'Allow for this session', input: '2', mode: true },
      { label: 'Always allow', input: '3', mode: true },
      { label: 'Cancel', input: '\u001b' },
    ]
    // The form marks no choice as recommended, so none is drawn filled.
    expect(recommendedChoice(form)).toBe(-1)
    expect(form.map(choiceKey)).toEqual([1, undefined, undefined, 4])
    // One that remembers the approval is never the filled one, whatever its label says or wherever it is listed.
    expect(recommendedChoice([{ ...form[1]!, label: 'Allow for this session (Recommended)' }, form[0]!, form[3]!])).toBe(-1)
    expect(recommendedChoice([form[0]!, { ...form[2]!, label: 'Always allow (Recommended)' }, form[3]!])).toBe(-1)
    // Allow itself, marked by the agent, is.
    expect(recommendedChoice([{ ...form[0]!, label: 'Allow (Recommended)' }, form[1]!, form[2]!, form[3]!])).toBe(0)
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

describe("a durable decision's question (Program item DR1)", () => {
  const roles: Role[] = ['control', 'view']
  const yesNo = [
    { label: 'Yes', input: '1' },
    { label: 'No', input: '2' },
  ]
  const mirror: Attention = { state: 'needs_input', source: 'api', id: '0123456789abcdef', message: 'Ship the schema change?', decision: 'dr-7' }

  it('is the decision the question mirrors, and only a shown question has one', () => {
    expect(decisionOf(mirror)).toBe('dr-7')
    expect(decisionOf({ state: 'needs_input' })).toBeUndefined()
    expect(decisionOf({ state: '', decision: 'dr-7' })).toBeUndefined()
    expect(decisionOf(undefined)).toBeUndefined()
    // Where it is answered, never that it was: it waits for the operator.
    expect(decisionAnsweredBy('dr-7')).toBe('The operator answers dr-7 in Conductor')
  })

  it('offers no reply field, choice or number key, whatever the role', () => {
    // Even with choices given (a mirror has none; an older page's bar must offer none all the same).
    for (const a of [mirror, { ...mirror, options: yesNo }, { ...mirror, kind: 'prompt' as const, options: yesNo }]) {
      expect(offeredChoices(a)).toEqual([])
      expect(offeredChoices(a).map(choiceKey).filter((k) => k !== undefined)).toEqual([])
      for (const role of roles) expect(takesReply(a, role)).toBe(false)
    }
  })

  it("leaves every other question's reply as it was", () => {
    const own: Attention = { state: 'needs_input', source: 'api', kind: 'prompt', options: yesNo }
    expect(offeredChoices(own)).toEqual(yesNo)
    expect(takesReply(own, 'control')).toBe(true)
    expect(takesReply(own, 'view')).toBe(false)
    expect(takesReply({ state: 'needs_input', source: 'submit' }, 'control')).toBe(false)
    expect(takesReply({ state: 'needs_input', kind: 'permission' }, 'control')).toBe(false)
    expect(offeredChoices({ state: 'working', options: yesNo })).toEqual([])
  })

  it('is the one answer every reply field reads: why none is taken, in its words, else undefined', () => {
    expect(noReply(mirror)).toBe('The operator answers dr-7 in Conductor')
    expect(noReply({ state: 'needs_input', source: 'submit' })).toBe(TERMINAL_FIRST)
    expect(noReply({ state: 'needs_input', kind: 'permission' })).toBe(ANSWER_IN_TERMINAL)
    expect(noReply({ state: 'needs_input', source: 'api', kind: 'prompt', options: yesNo })).toBeUndefined()
    expect(noReply({ state: 'needs_input', source: 'api' })).toBeUndefined()
    expect(noReply(undefined)).toBeUndefined()
  })
})

describe('a startup question with no choices (#902)', () => {
  const signIn: Attention = { state: 'needs_input', source: 'trust', kind: 'prompt', id: 'q1', message: "Browser didn't open? Use the url below to sign in" }
  const trust: Attention = { state: 'needs_input', source: 'trust', kind: 'prompt', id: 'q2', message: 'Is this a project you created or one you trust?', options: [{ label: 'Yes, I trust this folder', input: '\x1b[B\r' }, { label: 'No, exit', input: '\r' }] }

  it('is answered in the terminal, and takes no typed reply anywhere', () => {
    expect(startupInTerminal(signIn)).toBe(true)
    expect(noReply(signIn)).toBe(ANSWER_IN_TERMINAL)
    for (const role of ['control', 'view'] as Role[]) expect(takesReply(signIn, role)).toBe(false)
    expect(startupInTerminal({ ...signIn, options: [] })).toBe(true)
  })

  it('is none of the others: one with choices is answered with them, and a question of any other source is as before', () => {
    expect(startupInTerminal(trust)).toBe(false)
    expect(noReply(trust)).toBeUndefined()
    expect(offeredChoices(trust)).toEqual(trust.options)
    expect(startupInTerminal({ ...signIn, source: 'question' })).toBe(false)
    expect(startupInTerminal({ ...signIn, source: 'api' })).toBe(false)
    expect(noReply({ ...signIn, source: 'api' })).toBeUndefined()
    expect(startupInTerminal({ ...signIn, state: '' })).toBe(false)
    expect(startupInTerminal(undefined)).toBe(false)
  })
})

describe('choiceTitle', () => {
  it("gives a plain choice its label, and a mode answer its label and what it does", () => {
    expect(choiceTitle({ label: 'Yes' })).toBe('Yes')
    expect(choiceTitle({ label: 'Yes, and switch to BYPASS PERMISSIONS', mode: true })).toBe('Yes, and switch to BYPASS PERMISSIONS (allows from now on, not this request alone)')
  })
})

describe('the number a key press answers with', () => {
  const key = (key: string, o: Partial<KeyboardEvent> = {}) => ({ key, repeat: false, isComposing: false, altKey: false, ctrlKey: false, metaKey: false, ...o })
  it('is 1 to 9 for a fresh press of a digit alone', () => {
    for (let n = 1; n <= 9; n++) expect(answerDigit(key(String(n)))).toBe(n)
    for (const k of ['0', 'a', 'Enter', '10', ' ']) expect(answerDigit(key(k))).toBe(0)
  })
  it("answers nothing for a held key's repeats, a key of a composition, or a press with a modifier", () => {
    expect(answerDigit(key('1', { repeat: true }))).toBe(0)
    expect(answerDigit(key('1', { isComposing: true }))).toBe(0)
    for (const m of ['altKey', 'ctrlKey', 'metaKey'] as const) expect(answerDigit(key('1', { [m]: true }))).toBe(0)
  })
})
