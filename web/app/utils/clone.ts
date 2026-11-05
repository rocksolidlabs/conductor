import type { LaunchPrefill } from '~/composables/useLaunchModal'
import type { AgentInfo, SessionInfo, SessionLaunch } from '~/composables/useSessions'
import { argsText } from './argv'
import { MAX_SESSION_NAME } from './protocol'
import { renameDraft, type RenameDraft } from './sessionName'

/**
 * Cloning a session (#434): Clone… starts the same session again under another name, Clone with options… opens the Launch dialog
 * filled in with the source's launch. Both go through POST /api/sessions/{id}/clone, which refuses a name in use; the dialogs say so
 * before they send.
 */

/** Why a hosted session cannot be cloned: Conductor starts no session on a host yet (#162). */
export function hostedReason(machine: string | undefined): string {
  return `Runs on ${machine || 'another machine'}: Conductor cannot start a session there yet`
}

/** Why Clone… is not offered for a Yardmaster attempt: Clone with options… is, with its warning. */
export const ATTEMPT_REASON = "A Yardmaster attempt's worktree must stay clean apart from .conductor/: use Clone with options…"

/** What Clone with options… says of an attempt's clone. */
export const ATTEMPT_WARNING = "This session is a Yardmaster attempt: its result needs its worktree clean apart from .conductor/, and the clone works in that worktree."

/** What both dialogs say of a directory under .conductor/worktrees/. */
export const SHARED_TREE_WARNING = 'This is a run\'s worktree: two agents will work in one working tree.'

/**
 * Whether a directory is a run's worktree (under `.conductor/worktrees/`): a clone there shares one working tree with the member. A
 * relative path that starts with the folder counts too (#434 review round 3): the server resolves it against its own directory.
 */
export function inRunWorktree(cwd: string | undefined): boolean {
  return !!cwd && /(^|[\\/])\.conductor[\\/]worktrees[\\/]/.test(cwd + '/')
}

/** Whether a session is a Yardmaster attempt (its run's task set). */
export function isAttempt(s: Pick<SessionInfo, 'crew'>): boolean {
  return !!s.crew?.task
}

/** Whether a name, ignoring case, is a listed session's (the server's rule: running or ended, server or hosted). */
export function nameTaken(name: string, sessions: readonly Pick<SessionInfo, 'name'>[]): boolean {
  const lower = name.toLowerCase()
  return sessions.some((s) => s.name.toLowerCase() === lower)
}

/** A clone's name as a dialog holds it: the rename's rule (cleaned, at most MAX_SESSION_NAME bytes, not empty), and no listed session's. */
export interface CloneName extends Omit<RenameDraft, 'problem'> {
  problem?: RenameDraft['problem'] | 'taken'
}

export function cloneName(draft: string, sessions: readonly Pick<SessionInfo, 'name'>[]): CloneName {
  const d = renameDraft(draft)
  if (d.problem) return d
  if (nameTaken(d.name, sessions)) return { ...d, problem: 'taken' }
  return d
}

/** What a dialog says of a name it cannot send; undefined for an empty field, which only disables the button. */
export function cloneNameError(n: CloneName): string | undefined {
  if (n.problem === 'long') return `Too long: a name is at most ${MAX_SESSION_NAME} bytes, and this one is ${n.bytes}`
  if (n.problem === 'taken') return `A session is named ${n.name} already`
  return undefined
}

/** The Launch dialog's prefill for Clone with options…: the source's agent, directory, arguments as recorded and yolo choice, sent as given. */
export function clonePrefill(s: Pick<SessionInfo, 'id' | 'name' | 'crew'>, launch: SessionLaunch): LaunchPrefill {
  return {
    agentId: launch.agentId,
    cwd: launch.cwd,
    args: argsText(launch.args),
    argv: [...launch.args],
    yolo: launch.yolo === true,
    clone: { id: s.id, name: s.name, attempt: isAttempt(s), cwd: launch.cwd },
  }
}

/**
 * Clone with options…'s arguments (#434 review round 1). The line shows while the picked agent takes arguments, and also while it is
 * the source's agent with arguments recorded although it takes none now (launched before its arguments were turned off, or a
 * Brakeman member's own flags): they are never dropped unseen, and a clone sent with them is refused by the server, in the dialog,
 * until the line is cleared. Left as it was, the line launches the recorded arguments themselves; changed, it is split; hidden,
 * there are none.
 */
export function cloneArgsShown(o: { allowArgs: boolean; sameAgent: boolean; recorded: readonly string[] | undefined }): boolean {
  return o.allowArgs || (o.sameAgent && !!o.recorded?.length)
}

export function cloneLaunchArgs(o: { shown: boolean; line: string; recorded?: { text: string; argv: readonly string[] } | null; split: (line: string) => string[] }): string[] {
  if (!o.shown) return []
  if (o.recorded && o.line === o.recorded.text) return [...o.recorded.argv]
  return o.line.trim() ? o.split(o.line) : []
}

/** A clone's directory as sent: the field exactly as it holds it (a name may end in a space), left out only when blank. */
export function cloneCwd(line: string): string | undefined {
  return line.trim() ? line : undefined
}

/**
 * The directory a clone launches in (#434 review round 2): the field's, or the source's own when the field is blank and the server
 * takes the source's. The run's-worktree warning is judged by it, so a field cleared on a worktree's clone keeps the warning.
 */
export function cloneLaunchCwd(line: string, source: string): string {
  return cloneCwd(line) ?? source
}

/**
 * Hands out Clone with options…'s reads in the order they are asked (#434 review round 2): `next()` before a read, `current(n)`
 * once it has answered. A read asked earlier that answers after a later one is superseded, and opens nothing.
 */
export function requestOrder(count: { value: number }) {
  return {
    next: () => ++count.value,
    current: (n: number) => n === count.value,
  }
}

/**
 * The source's agent when Clone with options… cannot offer it, once this opening's own read of the catalog has answered (a cached
 * catalog may still list an agent since removed or no longer installed); '' while it is offered or not yet known.
 */
export function missingSourceAgent(source: string, offered: readonly Pick<AgentInfo, 'id'>[], read: boolean): string {
  return source && read && !offered.some((a) => a.id === source) ? source : ''
}

/**
 * The yolo choice the switch stores. A launch keeps "the server's default" while the switch shows it; a clone always keeps the
 * choice itself, sent as given: the default the switch is compared with may still be the cache's or none, before the catalog answers.
 */
export function yoloStored(on: boolean, serverDefault: boolean, cloning: boolean): boolean | undefined {
  return cloning || on !== serverDefault ? on : undefined
}

/** What a server answers a clone with, as the dialogs say it. */
export function cloneRefusal(code: string | undefined, message: string): string {
  switch (code) {
    case 'invalid_agent':
      return "The source's agent is no longer in the catalog"
    case 'invalid_cwd':
      return `The directory cannot be used: ${message}`
    case 'name_taken':
      return message
    default:
      return code ? `${message} (${code})` : message
  }
}
