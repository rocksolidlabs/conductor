import type { ChatMessage } from '~/utils/protocol'
import { replyFailure } from '~/utils/reply'
import type { SessionInfo } from './useSessions'

/** A run's member as a page that shows the run's chat knows it: its name in the run, and its session while that runs. */
export interface RunChatAsker {
  name: string
  /** The member's session while it starts or runs, else none: a choice's keys go to it and nowhere else. */
  session?: Pick<SessionInfo, 'id' | 'kind'>
}

/** A run chat's question can be answered from this page only through the one member of its name with a running session. */
export function runChatAsker(m: Pick<ChatMessage, 'kind' | 'on' | 'id'>, members: readonly RunChatAsker[]): RunChatAsker | undefined {
  if (m.kind !== 'question' || !m.on || !m.id) return undefined
  const named = members.filter((x) => x.name === m.on)
  // Two of one name is no member this page can tell: nothing is sent to either.
  return named.length === 1 && named[0]!.session ? named[0] : undefined
}

/** The page's words for a choice of the run's chat that sent nothing: no member's session to send it to, or a reply already sent. */
export function runChatNotSent(why: 'unreachable' | 'sent', name: string | undefined): { title: string; description: string; color: 'warning' } {
  const whose = name ? `${name}'s` : "the member's"
  if (why === 'sent') return { title: 'Already sent', description: `Your reply to ${whose} question went from this page, or is on its way. Nothing more was sent.`, color: 'warning' }
  return { title: 'Not sent', description: `This page cannot reach ${whose} session to answer its question, so nothing was sent. Answer it in its terminal.`, color: 'warning' }
}

/**
 * A member's question answered from a run's chat (#1009), on the run page and on a run link's page. The question's line names the
 * member it is on (`on`, the member's name in the run): the choice's keys (its `input`, as the line gives it) go to that member's
 * running session on a short control connection of their own (useQuickReply, with the page's credential: the workbench's, or the
 * run link's), naming the question by the line's id, as the member's session page answers the same choice over its terminal's
 * connection; the session takes them only while that question still waits, and its refusal is said as the session page says it.
 * A line whose member is not one running member of the run, or a page that may not answer (`may`: a view link), offers its choices
 * as text (`answerable`); a press that sends nothing says why.
 */
export function useRunChatAnswer(o: { members: () => readonly RunChatAsker[]; may: () => boolean; via?: () => { token?: string; server?: string } }) {
  const quick = useQuickReply()
  const toast = useToast()

  function answerable(m: ChatMessage): boolean {
    return o.may() && !!runChatAsker(m, o.members())
  }

  async function answer(m: ChatMessage, index: number): Promise<void> {
    const choice = m.options?.[index]
    const who = o.may() ? runChatAsker(m, o.members()) : undefined
    if (!choice || !who?.session) {
      toast.add(runChatNotSent('unreachable', m.on))
      return
    }
    try {
      const sent = await quick.send(who.session, choice.input, { ...(o.via?.() ?? {}), question: m.id })
      if (!sent) toast.add(runChatNotSent('sent', who.name))
    } catch (e) {
      toast.add(replyFailure(e, who.name))
    }
  }

  return { answerable, answer }
}
