// What a run's tile that asks says between its terminal and its foot (RunTileAsk): who asks, the question whole, and how it is
// answered there. Pure.
import type { SessionInfo } from '~/composables/useSessions'
import type { ChatMessage } from './protocol'
import { memberAsk } from './crewWords'

export interface TileAsk {
  /** `lead asks:` */
  who: string
  /** The question whole, as the agent asked it. */
  question: string
  /** The question with its choices, which the page answers with a press. */
  line: ChatMessage
}

/**
 * The bar of a member's tile while it asks: its words and its choices, where the page may answer them with a press (`answerable`,
 * the run chat's own rule); null while it does not ask, or asks a question the page cannot answer from the tile.
 */
export function tileAskOf(name: string, s: Pick<SessionInfo, 'agentId' | 'attention'> | undefined, answerable: (line: ChatMessage) => boolean): TileAsk | null {
  const { question, line } = memberAsk(name, s)
  if (!line || !answerable(line)) return null
  return { who: `${name} asks:`, question, line }
}
