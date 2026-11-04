import type { LaunchPrefill } from '~/composables/useLaunchModal'
import type { SessionInfo, SessionLaunch } from '~/composables/useSessions'
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

/** Whether a directory is a run's worktree (under `.conductor/worktrees/`): a clone there shares one working tree with the member. */
export function inRunWorktree(cwd: string | undefined): boolean {
  return !!cwd && /[\\/]\.conductor[\\/]worktrees[\\/]/.test(cwd + '/')
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
    clone: { id: s.id, name: s.name, attempt: isAttempt(s) },
  }
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
