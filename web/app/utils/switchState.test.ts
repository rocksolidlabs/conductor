import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { CREW_WAY_IS_RUNS, ENDS_TERMINAL_WORK, FIRST_REPLY, MOVES_TO_CHAT, MOVES_TO_TERMINAL, MOVING, NOT_QUALIFIED, RUNS_AS_CHAT_FOR_RUN, crewChatOf, crewMemberOf, firstReplyOf, pressFor, refusalStands, switchState, type Refusal, type SwitchFacts } from './switchState'

const at = (f: Partial<SwitchFacts>): SwitchFacts => ({
  hasProtocol: true, way: 'terminal', noted: '', why: '', canMove: true, firstReply: true, qualified: true, inTurn: false, moving: false, ...f,
})

describe('switchState: which sentence the switch shows', () => {
  it('shows no switch for an agent with no protocol', () => {
    expect(switchState(at({ hasProtocol: false })).shown).toBe(false)
  })
  it('before the first reply: Chat waits for it, and is not pressable', () => {
    const s = switchState(at({ firstReply: false }))
    expect(s).toMatchObject({ shown: true, selected: 'terminal', pressable: false, line: FIRST_REPLY })
    expect(FIRST_REPLY).toBe("Chat opens after the agent's first reply.")
  })
  it('an unqualified version stays in its terminal', () => {
    expect(switchState(at({ qualified: false })).line).toBe(NOT_QUALIFIED)
    expect(switchState(at({ qualified: false })).pressable).toBe(false)
  })
  it('during a turn: the other side may be pressed, and nothing is said until it is (the noted move says it)', () => {
    expect(switchState(at({ inTurn: true }))).toMatchObject({ pressable: true, line: '' })
    expect(switchState(at({ way: 'protocol', inTurn: true })).line).toBe('')
    expect(switchState(at({ way: 'protocol', inTurn: true, noted: 'terminal' })).line).toBe(MOVES_TO_TERMINAL)
    expect(MOVES_TO_CHAT).toBe('Moves to Chat when this turn ends.')
  })
  it('noted: the way message sentence, else the move words', () => {
    expect(switchState(at({ inTurn: true, noted: 'protocol' }))).toMatchObject({ noted: 'protocol', line: MOVES_TO_CHAT })
    expect(switchState(at({ way: 'protocol', noted: 'terminal', why: 'Background work is still running.' })).line).toBe('Background work is still running.')
  })
  it('refused: the route words, over the rest', () => {
    expect(switchState(at({ refused: "A crew member's way is its run's.", inTurn: true })).line).toBe("A crew member's way is its run's.")
  })
  it('back the way it came: the start failure sentence', () => {
    expect(switchState(at({ why: 'Claude Code did not start a chat.' })).line).toBe('Claude Code did not start a chat.')
  })
  it('moving: nothing pressable, its sentence', () => {
    expect(switchState(at({ moving: true }))).toMatchObject({ pressable: false, line: MOVING })
  })
  it("a link's viewer: a label of the way, no action, no line", () => {
    expect(switchState(at({ canMove: false, way: 'protocol', noted: 'terminal' }))).toMatchObject({ shown: true, selected: 'protocol', pressable: false, line: '', noted: '' })
  })
  it('at rest after a turn, to Chat: pressable, and the line says what the move ends', () => {
    expect(switchState(at({}))).toMatchObject({ pressable: true, line: ENDS_TERMINAL_WORK })
    expect(switchState(at({ way: 'protocol' }))).toMatchObject({ pressable: true, line: '' })
  })
})

describe("crewChatOf: a Chat run member's switch is its run's label", () => {
  const member = { crew: { runId: 'api-sweep-0000beef', crewId: 'api-sweep', member: 'lead', task: 'build', attempt: 1 } }
  it('a crew member in its chat: the label, nothing pressable', () => {
    expect(crewChatOf(member, 'protocol')).toBe(true)
    const s = switchState(at({ way: 'protocol', crewChat: crewChatOf(member, 'protocol') }))
    expect(s).toMatchObject({ shown: true, selected: 'protocol', pressable: false, line: RUNS_AS_CHAT_FOR_RUN, noted: '' })
    expect(pressFor(s, 'terminal')).toBeNull()
    expect(RUNS_AS_CHAT_FOR_RUN).toBe('Runs as a chat for its run.')
  })
  it("the session page gives its switch and its menu's way entries the fact", () => {
    const page = readFileSync(new URL('../pages/sessions/[id].vue', import.meta.url), 'utf8')
    expect(page).toContain('crewChat: crewChatOf(current.value, way.value)')
    expect(page).toContain('!!switchFacts.value.crewChat ||')
  })
  it("a crew member in its terminal: no side to press and the crew's sentence, whatever its turns", () => {
    expect(crewMemberOf(member)).toBe(true)
    for (const firstReply of [false, true]) {
      const s = switchState(at({ way: 'terminal', firstReply, crewMember: crewMemberOf(member), crewChat: crewChatOf(member, 'terminal') }))
      expect(s).toMatchObject({ shown: true, selected: 'terminal', pressable: false, line: CREW_WAY_IS_RUNS, noted: '' })
      expect(pressFor(s, 'protocol')).toBeNull()
    }
    expect(CREW_WAY_IS_RUNS).toBe("A crew member's way is its run's.")
    const page = readFileSync(new URL('../pages/sessions/[id].vue', import.meta.url), 'utf8')
    expect(page).toContain('crewMember: crewMemberOf(current.value)')
    expect(page).toContain('!!switchFacts.value.crewMember ||')
  })
  it("a person's session keeps the switch", () => {
    expect(crewMemberOf({})).toBe(false)
    expect(crewChatOf(member, 'terminal')).toBe(false)
    expect(crewChatOf({}, 'protocol')).toBe(false)
    expect(crewChatOf(undefined, 'protocol')).toBe(false)
    expect(switchState(at({ way: 'protocol', crewChat: crewChatOf({}, 'protocol') })).line).not.toBe(RUNS_AS_CHAT_FOR_RUN)
  })
})

describe('pressFor: what a press of each side sends', () => {
  it('the other side moves; the selected side sends nothing while nothing is noted', () => {
    const s = switchState(at({}))
    expect(pressFor(s, 'protocol')).toBe('protocol')
    expect(pressFor(s, 'terminal')).toBeNull()
  })
  it('the selected side takes a noted move back', () => {
    const s = switchState(at({ inTurn: true, noted: 'protocol' }))
    expect(pressFor(s, 'terminal')).toBe('terminal')
    expect(pressFor(switchState(at({ way: 'protocol', noted: 'terminal' })), 'protocol')).toBe('protocol')
  })
  it('nothing while moving, before the first reply, or for a link viewer', () => {
    expect(pressFor(switchState(at({ moving: true, noted: 'protocol' })), 'terminal')).toBeNull()
    expect(pressFor(switchState(at({ firstReply: false })), 'protocol')).toBeNull()
    expect(pressFor(switchState(at({ canMove: false })), 'protocol')).toBeNull()
  })
})

describe('firstReplyOf: the offer is the route\'s own fact', () => {
  it('a first line entered (the conversation resumable) is not the first reply ended: no Chat, the sentence stays', () => {
    const entered = { agentSession: { resumable: true } }
    expect(firstReplyOf(entered, 'terminal')).toBe(false)
    expect(switchState(at({ firstReply: firstReplyOf(entered, 'terminal'), inTurn: true }))).toMatchObject({ pressable: false, line: FIRST_REPLY })
  })
  it('the first turn ended: Chat offered at rest, the sentence gone', () => {
    const ended = { firstTurn: true, agentSession: { resumable: true } }
    expect(firstReplyOf(ended, 'terminal')).toBe(true)
    expect(switchState(at({ firstReply: firstReplyOf(ended, 'terminal') }))).toMatchObject({ pressable: true, line: ENDS_TERMINAL_WORK })
  })
  it('an older server sends no firstTurn: no move offered, nothing worse', () => {
    expect(firstReplyOf({}, 'terminal')).toBe(false)
    expect(firstReplyOf(undefined, 'terminal')).toBe(false)
    expect(switchState(at({ firstReply: firstReplyOf({}, 'terminal') }))).toMatchObject({ shown: true, pressable: false, line: FIRST_REPLY })
  })
  it('a chat is past its first reply', () => {
    expect(firstReplyOf({}, 'protocol')).toBe(true)
  })
})

describe('refusalStands: a refusal says what is true now', () => {
  const basis = { way: 'terminal' as const, firstReply: false, resumable: true }
  const first: Refusal = { words: FIRST_REPLY, basis }
  it('stands while what it was judged on stands', () => {
    expect(refusalStands(first, { ...basis })).toBe(true)
    expect(refusalStands(null, basis)).toBe(false)
  })
  it('the first reply ended: the refusal goes, and the line with it', () => {
    expect(refusalStands(first, { ...basis, firstReply: true })).toBe(false)
    expect(switchState(at({ firstReply: true, refused: refusalStands(first, { ...basis, firstReply: true }) ? first.words : undefined })).line).toBe(ENDS_TERMINAL_WORK)
  })
  it('the conversation became known, or the way changed: it goes', () => {
    const unknown: Refusal = { words: 'This agent cannot move its conversation yet.', basis: { ...basis, firstReply: true, resumable: false } }
    expect(refusalStands(unknown, { ...unknown.basis, resumable: true })).toBe(false)
    expect(refusalStands(first, { ...basis, way: 'protocol' })).toBe(false)
  })
  it('one true for the session\'s life stays while nothing it was judged on changes', () => {
    const folder: Refusal = { words: 'Sessions launched with an added folder stay in their terminal.', basis: { ...basis, firstReply: true } }
    expect(refusalStands(folder, { ...basis, firstReply: true })).toBe(true)
  })
})
