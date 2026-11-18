import { watch } from 'vue'
import type { ChatMessage } from '~/utils/protocol'
import { replyFailure, ReplyError } from '~/utils/reply'
import type { SessionInfo } from './useSessions'

/** A run's member as a page that shows the run's chat knows it: its name in the run, and its session while that runs. */
export interface RunChatAsker {
  name: string
  /** The member's session while it starts or runs, else none: a choice's keys go to it and nowhere else. */
  session?: Pick<SessionInfo, 'id' | 'kind'>
}

/**
 * A run chat's question can be answered from this page only through the one member of its name with a running session. An entry
 * of that name without one (a resumed member's earlier session, ended and still listed) is no place to answer and is passed over;
 * two running sessions of one name are no member this page can tell, and nothing is sent to either.
 */
export function runChatAsker(m: Pick<ChatMessage, 'kind' | 'on' | 'id'>, members: readonly RunChatAsker[]): RunChatAsker | undefined {
  if (m.kind !== 'question' || !m.on || !m.id) return undefined
  const running = members.filter((x) => x.name === m.on && x.session)
  return running.length === 1 ? running[0] : undefined
}

/** The page's words for a choice of the run's chat that sent nothing: no member's session to send it to, the members not read again, or a reply already sent. */
export function runChatNotSent(why: 'unreachable' | 'sent' | 'unread', name: string | undefined): { title: string; description: string; color: 'warning' } {
  const whose = name ? `${name}'s` : "the member's"
  if (why === 'unread') return { title: 'Not sent', description: `This page could not read the run's members again to find ${whose} session, so nothing was sent. Try again in a moment.`, color: 'warning' }
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
 *
 * A page whose members are a read of its own (a run link's: `refresh` reads them again, true when the read landed) reads them again
 * as a new question comes into the run's chat (`lines`), so that a member's next attempt, a session of its own, is the one its
 * buttons answer through, and again as a choice is pressed, so that the keys go to the member's session as it is then; a read that
 * fails sends nothing.
 */
export function useRunChatAnswer(o: {
  members: () => readonly RunChatAsker[]
  may: () => boolean
  via?: () => { token?: string; server?: string }
  refresh?: () => Promise<boolean>
  lines?: () => readonly ChatMessage[]
}) {
  const quick = useQuickReply()
  const toast = useToast()

  if (o.refresh && o.lines) {
    const seen = new Set<string>()
    watch(
      () => o.lines!().filter((m) => m.kind === 'question').map((m) => m.id),
      (ids) => {
        const fresh = ids.filter((id) => !seen.has(id))
        for (const id of fresh) seen.add(id)
        if (fresh.length && o.may()) void o.refresh!()
      },
      // Not at once: the page reads its members as it loads, and its read is not set up yet while this runs.
    )
  }

  function answerable(m: ChatMessage): boolean {
    return o.may() && !!runChatAsker(m, o.members())
  }

  async function answer(m: ChatMessage, index: number): Promise<void> {
    const choice = m.options?.[index]
    if (!choice || !o.may()) {
      toast.add(runChatNotSent('unreachable', m.on))
      return
    }
    if (o.refresh && !(await o.refresh())) {
      toast.add(runChatNotSent('unread', m.on))
      return
    }
    const who = runChatAsker(m, o.members())
    if (!who?.session) {
      toast.add(runChatNotSent('unreachable', m.on))
      return
    }
    try {
      const sent = await quick.send(who.session, choice.input, { ...(o.via?.() ?? {}), question: m.id })
      if (!sent) toast.add(runChatNotSent('sent', who.name))
    } catch (e) {
      // The session's own word (answered meanwhile, refused) as the session page says it; a connection that never reached the
      // session (it ended, or left the server) sent nothing to it.
      toast.add(e instanceof ReplyError ? replyFailure(e, who.name) : runChatNotSent('unreachable', who.name))
    }
  }

  return { answerable, answer }
}
