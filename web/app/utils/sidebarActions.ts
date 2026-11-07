import type { DropdownMenuItem } from '@nuxt/ui'
import { accountActions, rowActions, toMenuItems, type AccountHandlers, type RowActionHandlers, type RowActionTarget } from './actions'
import { decisionOf, noReply } from './attention'
import type { AttentionOption } from './protocol'
import type { SessionRow } from './sidebar'

export { stopQuestion, type AccountHandlers, type RowActionHandlers, type RowActionTarget } from './actions'

/**
 * The sidebar's menus (design 3c): views over the action descriptors in
 * `actions.ts`, which the command palette renders too (#302): a row's More,
 * right-click and long-press menu, and the account menu.
 */
export function rowMenuItems(t: RowActionTarget, on: RowActionHandlers): DropdownMenuItem[][] {
  return toMenuItems(rowActions(t, on))
}

/** Whether a session in this state can be stopped (and so shared). */
export function sessionLive(status: string): boolean {
  return status === 'running' || status === 'starting'
}

/** What a row's prompt offers (design 3b, 3c): choices as numbered buttons, or a reply field; waiting, disabled, while a host is away. */
export interface RowPrompt {
  message: string
  /** The choices, numbered 1..9 in the row; a prompt with none takes a typed line. */
  choices: AttentionOption[]
  /** A hosted session whose host is away: nothing reaches it until the host is back. */
  away: boolean
  /**
   * The question takes no typed reply (noReply): the session waits for someone to type in its terminal, a reply would only wait;
   * its question is answered there, a reply's Enter would pick what its dialog highlights; or it mirrors a durable decision,
   * answered as that decision. None is taken.
   */
  held: boolean
  /** What the row says in place of a reply when it takes none (noReply's words), else what its field says. */
  placeholder: string
  /** The durable decision the question mirrors (`dr-<n>`): the row says where it is answered, and offers no field or choice. */
  decision?: string
  /** The question's id: an answer from the row names it, and the owner refuses it once the question no longer waits. */
  question?: string
}

export const HOST_AWAY = 'The host is away'

export function rowPrompt(row: Pick<SessionRow, 'prompt' | 'session'>): RowPrompt | null {
  if (!row.prompt) return null
  const away = row.session.status === 'host_disconnected'
  const none = noReply(row.session.attention)
  const decision = decisionOf(row.session.attention)
  const placeholder = away ? HOST_AWAY : (none ?? 'Reply…')
  return { message: row.prompt.message, choices: decision ? [] : row.prompt.options, away, held: none !== undefined, placeholder, question: row.prompt.question, decision }
}

export function accountItems(hasToken: boolean, on: AccountHandlers): DropdownMenuItem[][] {
  return toMenuItems(accountActions(hasToken, on))
}
