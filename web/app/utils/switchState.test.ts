import { describe, expect, it } from 'vitest'
import { ENDS_TERMINAL_WORK, FIRST_REPLY, MOVES_TO_CHAT, MOVES_TO_TERMINAL, MOVING, NOT_QUALIFIED, pressFor, switchState, type SwitchFacts } from './switchState'

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
    expect(switchState(at({ refused: 'Crew members stay in their terminals.', inTurn: true })).line).toBe('Crew members stay in their terminals.')
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
