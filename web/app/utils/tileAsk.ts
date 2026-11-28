// What a run's tile that asks says between its terminal and its foot (RunTileAsk): who asks, the question whole, and how it is
// answered there. Pure.
import type { SessionInfo } from '~/composables/useSessions'
import type { ChatMessage } from './protocol'
import { answersInChat, decisionOf, liveAttention } from './attention'
import { memberAsk } from './crewWords'

/** A question whose choices the page may press on the tile. */
export interface TileAskChoices {
  kind: 'choices'
  line: ChatMessage
}
/** One the tile cannot answer: where it is answered, said in one line or as the button that goes there. */
export type TileAskElsewhere =
  | { kind: 'terminal'; words: string }
  | { kind: 'decision'; decision: string; words: string }
  | { kind: 'open'; words: string }

export type TileAsk = {
  /** `lead asks:` */
  who: string
  /** The question whole, as the agent asked it. */
  question: string
} & (TileAskChoices | TileAskElsewhere)

/** Where a question the tile cannot answer is answered: one wording with the session's own card. */
export const ANSWER_IN_ITS_TERMINAL = 'Answer it in its terminal.'
export const OPEN_TO_ANSWER = 'Open it to answer'

/**
 * The bar of a member's tile while it asks: who asks and the question whole, then what answers it. With choices the page may press
 * (`answerable`, the run chat's own rule): the choices. Without, where it is answered: a durable decision's question by its decision
 * (`Answer dr-7`, its card), a member that runs as a chat by its own view, where its Chat answers it (`Open it to answer`: a permission
 * request, a question with no choices), and every other (no choices, a startup screen, a dialog whose choices were not read, a line
 * typed and not entered) in its terminal. Null while it does not ask.
 */
export function tileAskOf(name: string, s: Pick<SessionInfo, 'agentId' | 'attention' | 'way'> | undefined, answerable: (line: ChatMessage) => boolean): TileAsk | null {
  const a = liveAttention(s?.attention)
  if (a?.state !== 'needs_input') return null
  const { question, line } = memberAsk(name, s)
  const who = `${name} asks:`
  if (line && answerable(line)) return { who, question, kind: 'choices', line }
  const decision = decisionOf(a)
  if (decision) return { who, question, kind: 'decision', decision, words: `Answer ${decision}` }
  if (answersInChat(a) || s?.way === 'protocol') return { who, question, kind: 'open', words: OPEN_TO_ANSWER }
  return { who, question, kind: 'terminal', words: ANSWER_IN_ITS_TERMINAL }
}
