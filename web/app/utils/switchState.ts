import type { SessionWay } from './protocol'

// The header's switch, Chat | Terminal (docs/plans/chat-or-terminal.md, designs 9, 11 and 13): which side is selected, whether it
// may be pressed, and the one line it shows. Its words are the plan's, exactly. The page mounts it in row 7; this is its state.

export const FIRST_REPLY = "Chat opens after the agent's first reply."
export const NOT_QUALIFIED = 'This version of Claude Code can only run in its terminal.'
export const MOVES_TO_CHAT = 'Moves to Chat when this turn ends.'
export const MOVES_TO_TERMINAL = 'Moves to the terminal when this turn ends.'
export const MOVING = 'The session is moving; try again in a moment.'
/** A move to Chat at rest ends what the agent left running in its terminal (the move's notice says it was ended). */
export const ENDS_TERMINAL_WORK = 'Moving to Chat ends what the agent left running in its terminal.'

/** What the switch is drawn from: the session's way and its noted move (the welcome, the `way` message), and the page's facts. */
export interface SwitchFacts {
  /** The session's agent has a protocol (AgentInfo.protocol): with none, no switch. */
  hasProtocol: boolean
  way: SessionWay
  noted: SessionWay | ''
  /** The `way` message's sentence: why a noted move waits, or why the session is back the way it came from. */
  why: string
  /** This viewer may move the session (the operator's own connection); a link's viewer sees a label of the way. */
  canMove: boolean
  /**
   * The agent's first reply has ended: the session's `firstTurn`, the one fact the move's route judges a move to Chat by (absent from
   * an older server: no move offered), or a chat already.
   */
  firstReply: boolean
  /** The probed version of the agent may run its chat. */
  qualified: boolean
  /** A turn runs now. */
  inTurn: boolean
  /** A move under way. */
  moving: boolean
  /** The route's refusal of the last press (move_refused's words), while what it was judged on stands (`refusalStands`). */
  refused?: string
}

export interface SwitchState {
  shown: boolean
  /** The side the session runs, selected. */
  selected: SessionWay
  /** The other side may be pressed. */
  pressable: boolean
  /** The switch's one line, '' for none. */
  line: string
  /** The side a noted move goes to, '' for none. */
  noted: SessionWay | ''
}

/** The words of a move to `to` pressed during a turn. */
export function movesWhenTurnEnds(to: SessionWay): string {
  return to === 'protocol' ? MOVES_TO_CHAT : MOVES_TO_TERMINAL
}

/** The switch's state from its facts. */
export function switchState(f: SwitchFacts): SwitchState {
  const off: SwitchState = { shown: false, selected: f.way, pressable: false, line: '', noted: '' }
  if (!f.hasProtocol) return off
  const s: SwitchState = { shown: true, selected: f.way, pressable: f.canMove && !f.moving, line: '', noted: f.noted }
  if (!f.canMove) return { ...s, noted: '' }
  const other: SessionWay = f.way === 'terminal' ? 'protocol' : 'terminal'
  if (f.moving) s.line = MOVING
  else if (f.refused) s.line = f.refused
  else if (f.noted) s.line = f.why || movesWhenTurnEnds(f.noted)
  else if (f.why) s.line = f.why
  else if (other === 'protocol' && !f.firstReply) s.line = FIRST_REPLY
  else if (other === 'protocol' && !f.qualified) s.line = NOT_QUALIFIED
  // During a turn nothing is said until a move is pressed (its `way` message says it is noted, with its sentence); at rest, what a
  // move to Chat ends.
  else if (other === 'protocol' && !f.inTurn) s.line = ENDS_TERMINAL_WORK
  if (other === 'protocol' && (!f.firstReply || !f.qualified) && !f.noted) s.pressable = false
  return s
}

/**
 * What a press of `side` sends (the way to move to), or null for none: the other side while it may be pressed; the selected side while
 * a move is noted, which takes the noted move back (the route's press of the way the session runs).
 */
export function pressFor(s: SwitchState, side: SessionWay): SessionWay | null {
  if (!s.shown || !s.pressable) return null
  if (side !== s.selected) return side
  return s.noted ? side : null
}

/**
 * The switch's first-reply fact from a session as it is listed: its `firstTurn` (the route's own fact), or a chat already. Nothing
 * else stands for it: the conversation turns resumable as the first line is entered, before its reply has ended.
 */
export function firstReplyOf(s: { firstTurn?: boolean; agentSession?: { resumable?: boolean } } | null | undefined, way: SessionWay): boolean {
  return !!s?.firstTurn || way === 'protocol'
}

/** What a refusal of the move was judged on, of what the page knows: the way, the first reply's end, the conversation known. */
export interface RefusalBasis {
  way: SessionWay
  firstReply: boolean
  resumable: boolean
}

/** A refusal the route answered a press with, and what it was judged on. */
export interface Refusal {
  words: string
  basis: RefusalBasis
}

/**
 * Whether the refusal r still says what is true: while every fact it was judged on is as it was. The first reply ended, the
 * conversation known, the way changed: the line goes, and a press asks the route again. A refusal true for the session's life (an
 * added folder, a crew member's, a hosted session's) stands as long as they do.
 */
export function refusalStands(r: Refusal | null, now: RefusalBasis): boolean {
  if (!r) return false
  return r.basis.way === now.way && r.basis.firstReply === now.firstReply && r.basis.resumable === now.resumable
}
