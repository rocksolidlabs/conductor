import { ERR_MOVE_REFUSED, MAX_SESSION_NAME, MAX_SUBMIT } from './protocol'
import { FOLDER_CANT, outsideWords, type AllowWay } from './folderAllow'
import { moreLine } from './moreOptions'
import { accountWords, clockWords, limited, WARN_PERCENT, type CapacityView } from './capacity'

/**
 * Whether the launch dialog asks where the agent runs. There are two answers
 * only when the workbench is served from another machine: in the desktop
 * app, or on a workbench opened at this computer's own address, "this
 * server" and "this computer" are the same place, and the question is noise.
 */
export function showsRunsOn(isDesktop: boolean, hostname: string): boolean {
  if (isDesktop) return false
  return !isLoopbackHost(hostname)
}

/** A hostname that names the computer the browser runs on. */
export function isLoopbackHost(hostname: string): boolean {
  const h = hostname.trim().toLowerCase().replace(/^\[|\]$/g, '')
  return h === '' || h === 'localhost' || h.endsWith('.localhost') || h === '::1' || /^127\.\d+\.\d+\.\d+$/.test(h)
}

/**
 * Whether `conductor host` refuses `server` without --insecure: a plain
 * `http://` (or `ws://`) server that is not `localhost` or a loopback address
 * (config.PlainOffMachine on the server side, the host's rule, whose test
 * table launch.test.ts repeats). What passes between the host and such a
 * server, the session's output and its viewers' keys among it, travels in
 * the clear and unverified. The host reads the string as given, so the host
 * part is read raw here, not through the URL parser, which writes LOCALHOST
 * and 127.1 as localhost and 127.0.0.1, names the rule exempts and the host
 * does not: `localhost` alone is this machine by name, and an address is one
 * the host parses (netip.ParseAddr). Any other name is another machine, as
 * it is for the host, which does not resolve it before deciding.
 */
export function plainOffMachine(server: string): boolean {
  const m = /^([a-z][a-z0-9+.-]*):\/\/(?:[^/?#@]*@)?(\[[^\]]*\]|[^/?#:]*)/i.exec(server)
  if (!m) return false
  const scheme = (m[1] ?? '').toLowerCase()
  if (scheme !== 'http' && scheme !== 'ws') return false
  const host = m[2] ?? ''
  if (host === 'localhost') return false
  return !isLoopbackIP(host.startsWith('[') ? host.slice(1, -1) : host)
}

/**
 * Whether h is a loopback address as the host parses one (netip.ParseAddr,
 * then Unmap): four decimal octets without leading zeros, the first 127, or
 * an IPv6 address that is ::1 or 127.x.x.x mapped into IPv6 (the zone of a
 * link-local form set aside, as the host's check sets it aside). Anything
 * else, 127.1 or 127.0.0.01 among it, is no address to the host.
 */
function isLoopbackIP(h: string): boolean {
  const v4 = /^(0|[1-9]\d{0,2})\.(0|[1-9]\d{0,2})\.(0|[1-9]\d{0,2})\.(0|[1-9]\d{0,2})$/.exec(h)
  if (v4) return v4.slice(1).every((o) => Number(o) <= 255) && v4[1] === '127'
  if (!h.includes(':')) return false
  let canonical: string
  try {
    canonical = new URL(`http://[${h.replace(/%.*$/, '')}]/`).hostname.replace(/^\[|\]$/g, '')
  } catch {
    return false
  }
  return canonical === '::1' || /^::ffff:7f[0-9a-f]{2}:[0-9a-f]{1,4}$/.test(canonical)
}

/**
 * The server's one rule for a cut at a word (`session.CutAtWord`, and `cutAtWord` in internal/dispatch), held to it by one table,
 * internal/session/testdata/cut-at-word.json: s whole when it fits in `chars` characters (and `bytes` bytes, when a byte bound is
 * given); else cut to `chars - 1` characters (and `bytes - 3` bytes) at the last space after the first `keep` characters, trailing
 * spaces trimmed, and "…" added, inside both bounds. A first word past the bound is cut at a character.
 */
export function cutAtWord(s: string, keep: number, chars: number, bytes = 0): string {
  const size = (r: string[]) => new TextEncoder().encode(r.join('')).length
  let r = Array.from(s)
  if (r.length <= chars && (bytes === 0 || size(r) <= bytes)) return s
  r = r.slice(0, Math.max(chars - 1, 0))
  if (bytes > 0) while (r.length > 0 && size(r) > bytes - 3) r.pop()
  for (let i = r.length - 1; i > keep; i--) {
    if (r[i] === ' ') {
      r = r.slice(0, i)
      break
    }
  }
  return `${r.join('').replace(/ +$/, '')}…`
}

/** The most characters of a session's name made from its first message (`session.NameFromMessage`). */
export const NAME_FROM_MESSAGE = 40

/**
 * The name a launch with this first message and no name gets (`session.NameFromMessage`): control characters as spaces, runs of
 * spaces as one, trimmed, then cut at a word to 40 characters and the name's 120 bytes with "…". "" when nothing is left, for which
 * the server names the session "<agent> #N".
 */
export function namePreview(message: string): string {
  const flat = message
    .split(/[\s\p{Cc}]+/u)
    .filter(Boolean)
    .join(' ')
  return flat ? cutAtWord(flat, 0, NAME_FROM_MESSAGE, MAX_SESSION_NAME) : ''
}

/** Why a first message cannot be sent, in the words the field shows: past MAX_SUBMIT bytes as sent; "" when it can. */
export function messageProblem(text: string): string {
  const n = new TextEncoder().encode(text).length
  return n > MAX_SUBMIT ? `At most ${MAX_SUBMIT.toLocaleString('en-US')} bytes: this is ${n.toLocaleString('en-US')}.` : ''
}

/**
 * The line above a session's terminal while its first message waits (`firstMessage` "waiting"): that a startup question comes first
 * while one shows (a needs_input of source `trust`), else that the agent must be ready; while it is typed and not sent ("typed"),
 * that Enter in the terminal sends it, after the question when one shows; "" otherwise, a word it does not know among it.
 */
export function firstMessageLine(s: { firstMessage?: string; attention?: { state: string; source?: string } } | null | undefined, agent: string): string {
  if (s?.firstMessage === 'typed') {
    // Typed into the agent's input, its Enter left out: a question came, or Conductor did not recognise the prompt holding it.
    if (s.attention?.state === 'needs_input') return 'Your first message is typed: answer the question, then press Enter in the terminal to send it.'
    return "Your first message is typed in the agent's input and not sent: press Enter in the terminal to send it."
  }
  if (s?.firstMessage !== 'waiting') return ''
  if (s.attention?.state === 'needs_input' && s.attention.source === 'trust') return 'Your first message is typed once its question is answered.'
  return `Your first message is typed once ${agent} is ready.`
}

/** Whether a launch of the agent takes a first message: the catalog's word (`firstMessage`), which an older server never gives. */
export function takesFirstMessage(agent: { firstMessage?: boolean } | null | undefined): boolean {
  return agent?.firstMessage === true
}

/**
 * The line in place of `What should it do?` for an agent that takes no first message: that it starts at its prompt, and, for one that
 * takes a task at all (it takes arguments: an agent, not a shell), where to type it.
 */
export function noTaskLine(agent: { name: string; allowArgs?: boolean }): string {
  return agent.allowArgs ? `${agent.name} starts at its prompt. Type your task in its terminal.` : `${agent.name} starts at its prompt.`
}

/**
 * The agent chip a key moves the Agent row's choice to from `current` (one Tab stop, the arrows within it, as a radio group does):
 * the next or the previous of `ids`, round at the ends, the first or the last for Home and End; null for any other key or no chips.
 */
export function chipStep(ids: readonly string[], current: string, key: string): string | null {
  if (!ids.length) return null
  const at = Math.max(0, ids.indexOf(current))
  switch (key) {
    case 'ArrowRight':
    case 'ArrowDown':
      return ids[(at + 1) % ids.length]!
    case 'ArrowLeft':
    case 'ArrowUp':
      return ids[(at - 1 + ids.length) % ids.length]!
    case 'Home':
      return ids[0]!
    case 'End':
      return ids[ids.length - 1]!
  }
  return null
}

/** How the Launch agent dialog says a launch the server refused: under Folder for the folder's refusal, else in the alert on top. */
export interface LaunchRefusal {
  /** `folder`: said under Folder, the field marked invalid and given the focus; `top`: the alert above the form. */
  at: 'folder' | 'top'
  words: string
  /** The folder is outside the folders agents may use. */
  outside?: boolean
  /** A link to Settings' folders follows the words, then `after` (as the folder line draws it). */
  settings?: boolean
  after?: string
}

/**
 * A refusal of POST /api/sessions in the dialog's words. invalid_cwd is the folder's, in the words the folder's line says (its kinds
 * as the server's resolveCwd says them: outside the allowed roots, said with the way on there is from `way`; no such folder; a file;
 * any other as a folder agents can't use, never the server's words); a launch as a chat its agent cannot run (`move_refused`) says
 * the server's sentence and the way on; every other code is said on top as `Not started: <the server's words>.`, the code kept out
 * of the words (the caller marks it on the alert).
 */
export function launchRefusal(code: string, message: string, way: AllowWay = 'settings'): LaunchRefusal {
  const said = message.trim().replace(/[.\s]+$/, '')
  if (code === 'invalid_cwd') {
    if (/outside the allowed roots/i.test(said)) return { at: 'folder', ...outsideWords(way), outside: true }
    if (/does not exist|no such file/i.test(said)) return { at: 'folder', words: 'There is no folder at this path.' }
    if (/not a directory/i.test(said)) return { at: 'folder', words: 'This is a file, not a folder.' }
    return { at: 'folder', words: FOLDER_CANT }
  }
  if (code === ERR_MOVE_REFUSED) return { at: 'top', words: `${said ? `Not started: ${said}.` : 'Not started.'} Set Run as to Terminal to launch it.` }
  return { at: 'top', words: said ? `Not started: ${said}.` : 'Not started.' }
}

/**
 * The line beside Launch agent's More options while it is closed: what is set inside it, so that nothing folded away goes unseen. A
 * typed name in quotes, the model, the extra arguments as typed, each trimmed, joined with " · "; "" when all is as it comes.
 * The caller passes only what the picked agent takes (no arguments for an agent that takes none, no model where Model is not shown).
 */
export function optionsSummary(o: { name?: string; model?: string; args?: string }): string {
  const name = o.name?.trim() ?? ''
  return moreLine([name && `"${name}"`, o.model, o.args])
}

/** The parts of a key press the Launch dialog reads to launch (a KeyboardEvent's). */
export interface LaunchKey {
  key: string
  shiftKey: boolean
  ctrlKey: boolean
  metaKey: boolean
  altKey: boolean
  isComposing: boolean
  repeat: boolean
  keyCode?: number
}

/**
 * Whether a key press in the Launch dialog launches: Ctrl or Cmd+Enter from any field, and Enter alone in the task box (`inTask`).
 * Never Shift+Enter in the task box (a new line), never while an input method is composing (its Enter takes the word), never a key
 * held down (a repeat), never with Alt. The dialog also sends nothing while a launch is out.
 */
export function launchKey(e: LaunchKey, inTask: boolean): boolean {
  if (e.key !== 'Enter' || e.isComposing || e.keyCode === 229 || e.repeat || e.altKey) return false
  if (e.ctrlKey || e.metaKey) return true
  return inTask && !e.shiftKey
}

/** Whether a launch's arguments name a model themselves: `--model`, `--model=<name>` or `-m`, as the agents' own flags spell it. */
export function argsNameModel(args: readonly string[]): boolean {
  return args.some((a) => a === '--model' || a.startsWith('--model=') || a === '-m')
}

/** Whether a launch's arguments hold one of the agent's yolo recipe arguments: the Yolo switch is the place for those. */
export function argsHoldYolo(args: readonly string[], recipe: readonly string[] | undefined): boolean {
  return !!recipe?.length && args.some((a) => recipe.includes(a))
}

/**
 * The placeholder of Launch agent's Name: the name the task gives once one is typed (namePreview), `Made from the task` before, and
 * for an agent that takes no task the name the server gives it, `<Agent> and a number`.
 */
export function namePlaceholderFor(agent: { name: string } | undefined, takesTask: boolean, task: string): string {
  if (!agent) return ''
  if (!takesTask) return `${agent.name} and a number`
  return namePreview(task) || 'Made from the task'
}

/** The one line under Launch agent's Account when the usage should slow a launch: its words and colour. It never holds Launch. */
export interface LaunchNote {
  words: string
  tone: 'warning' | 'error'
}

/**
 * Whether the account's usage should slow a launch, by the judgement the crew's sheet uses (warningOf): a limit the provider says is
 * reached; a window at warning or critical, or past WARN_PERCENT, named with its share and its reset (the error colour when the
 * provider says critical); the agent signed out on the account. null when all is fine, or nothing is known yet.
 */
export function launchNote(view: Pick<CapacityView, 'state' | 'windows' | 'limits' | 'account'> | null | undefined, agent: { name: string } | undefined, timeZone?: string): LaunchNote | null {
  if (!view || !agent) return null
  const account = accountWords(view.account)
  if (view.state === 'signed_out') return { words: `${agent.name} is signed out on ${account}. It will ask you to sign in when it starts.`, tone: 'warning' }
  if (view.state !== 'ok') return null
  if (limited(view.limits)) return { words: `${account} has reached a limit, its provider says. The agent may stop at once.`, tone: 'error' }
  const near = view.windows.filter((w) => w.severity === 'warning' || w.severity === 'critical' || w.percent >= WARN_PERCENT)
  if (!near.length) return null
  const rank = (w: (typeof near)[number]) => (w.severity === 'critical' ? 2 : 1) * 1000 + w.percent
  const worst = near.reduce((a, b) => (rank(b) > rank(a) ? b : a))
  const at = worst.resetsAt ? Date.parse(worst.resetsAt) : NaN
  const resets = Number.isNaN(at) ? '' : `, resets ${clockWords(at, timeZone)}`
  return { words: `${account} is near a limit: ${worst.name} ${worst.percent}% used${resets}. The agent may stop before then.`, tone: worst.severity === 'critical' ? 'error' : 'warning' }
}

