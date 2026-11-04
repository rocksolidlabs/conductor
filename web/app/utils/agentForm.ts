import type { AgentInfo, AgentInput, AgentSignal, CommandCheckReply, YoloRecipe } from '~/composables/useSessions'
import type { AgentBrowser } from '~/utils/protocol'
import { hasOpenQuote, splitArgs } from './argv'
import { BROWSERS, MAX_PROFILE, PROFILED, type BrowserName } from './opens'

/** What GET /api/catalog shows in place of every stored env value (catalog.RedactedValue). Sent back, it keeps the stored value. */
export const MASK = '***'

/** The server's rule for an agent ID: idPattern in internal/catalog/catalog.go. */
export const AGENT_ID_PATTERN = /^[a-z0-9-]{1,32}$/

export type SignalKind = AgentSignal['kind']

/** One row of the environment editor. A `masked` row is a value stored on the server, which the form never sees. */
export interface EnvRow {
  uid: number
  key: string
  value: string
  masked: boolean
}

/** One variable of the yolo recipe: shown as it is, never masked (a mode switch, not a secret). */
export interface YoloRow {
  uid: number
  key: string
  value: string
}

/** The server's rule for a yolo variable's name (validateYolo in internal/catalog): never one of Conductor's own. */
export const YOLO_ENV_NAME = /^[A-Za-z_][A-Za-z0-9_]*$/

/** At most this many yolo arguments, and as many variables (internal/catalog). */
export const MAX_YOLO = 16

/** The add-agent form. `pendingCommand` is what is typed in the command field and not yet an argument. */
export interface AgentForm {
  name: string
  id: string
  command: string[]
  pendingCommand: string
  description: string
  /** The agent's website: an https:// address, or empty. */
  site: string
  env: EnvRow[]
  allowArgs: boolean
  signal: SignalKind
  pattern: string
  /** The yolo recipe: its arguments, what is typed and not yet one, and its variables. */
  yoloArgs: string[]
  yoloPending: string
  yoloEnv: YoloRow[]
  /** "No yolo recipe": saved as `{}`, which also drops a built-in's. */
  yoloNone: boolean
  /** Where its links open (#436): a browser of the five, or 'default', the computer's default browser. */
  browser: BrowserName | 'default'
  /** The browser's profile directory (Chrome, Edge, Brave): `Profile 2`; empty for none. */
  profile: string
}

export type Field = 'name' | 'id' | 'command' | 'pattern' | 'env' | 'site' | 'yolo' | 'profile'

/** The form for `a`, or an empty one. Stored env values come in masked, by name; passthrough names follow as rows without a value. */
export function formFromAgent(a: AgentInfo | undefined, uid: () => number): AgentForm {
  return {
    name: a?.name ?? '',
    id: a?.id ?? '',
    command: [...(a?.command ?? [])],
    pendingCommand: '',
    description: a?.description ?? '',
    site: a?.site ?? '',
    env: [
      ...Object.keys(a?.env ?? {})
        .sort()
        .map((key) => ({ uid: uid(), key, value: '', masked: true })),
      ...(a?.envPassthrough ?? []).map((key) => ({ uid: uid(), key, value: '', masked: false })),
    ],
    allowArgs: a?.allowArgs ?? true,
    signal: a?.signal?.kind ?? 'bell',
    pattern: a?.signal?.pattern ?? '',
    yoloArgs: [...(a?.yolo?.args ?? [])],
    yoloPending: '',
    yoloEnv: Object.entries(a?.yolo?.env ?? {})
      .sort(([x], [y]) => x.localeCompare(y))
      .map(([key, value]) => ({ uid: uid(), key, value })),
    yoloNone: !!a?.yolo && !a.yolo.args?.length && !Object.keys(a.yolo.env ?? {}).length,
    browser: a?.browser?.name && BROWSERS.some((b) => b.name === a.browser?.name) ? a.browser.name : 'default',
    profile: a?.browser?.profile ?? '',
  }
}

/** Whether the form's browser takes a profile: Chrome, Edge and Brave. */
export function takesProfile(f: Pick<AgentForm, 'browser'>): boolean {
  return PROFILED.includes(f.browser)
}

/**
 * The browser to save: the one chosen, with its profile when it takes one; for the default browser `{}` when the agent edited (or
 * copied) has a browser, one it names or the `{}` a save of the default left over a configured one, which a save leaving it out
 * would give back to the configured agent; nothing otherwise.
 */
export function browserOut(f: Pick<AgentForm, 'browser' | 'profile'>, prev?: AgentInfo): AgentBrowser | undefined {
  if (f.browser === 'default') return prev?.browser ? {} : undefined
  const profile = takesProfile(f) ? f.profile.trim() : ''
  return profile ? { name: f.browser, profile } : { name: f.browser }
}

/** The server's rule for a profile (validateBrowser in internal/catalog), in words: '' when it may be saved. */
export function profileError(f: Pick<AgentForm, 'browser' | 'profile'>): string {
  if (!takesProfile(f)) return ''
  const p = f.profile.trim()
  if ([...p].length > MAX_PROFILE) return `A profile is at most ${MAX_PROFILE} characters`
  if (/[\p{Cc}"'`]/u.test(p)) return 'A profile has no quote or control character'
  return ''
}

/** The yolo arguments the form holds: its arguments, then what is typed and not yet one. */
export function yoloArgsOf(f: Pick<AgentForm, 'yoloArgs' | 'yoloPending'>): string[] {
  return [...f.yoloArgs, ...splitArgs(f.yoloPending)]
}

/**
 * The yolo recipe to save: `{}` for "No yolo recipe"; the arguments and variables when there are any; else nothing, so that an agent that
 * replaces a built-in keeps the built-in's and a new agent has none.
 */
export function yoloOut(f: Pick<AgentForm, 'yoloArgs' | 'yoloPending' | 'yoloEnv' | 'yoloNone'>): YoloRecipe | undefined {
  if (f.yoloNone) return {}
  const args = yoloArgsOf(f)
  const env: Record<string, string> = {}
  for (const r of f.yoloEnv) if (r.key.trim()) env[r.key.trim()] = r.value
  if (!args.length && !Object.keys(env).length) return undefined
  return { args: args.length ? args : undefined, env: Object.keys(env).length ? env : undefined }
}

/** The argv the form holds: its arguments, then what is typed and not yet one. */
export function commandOf(f: Pick<AgentForm, 'command' | 'pendingCommand'>): string[] {
  return [...f.command, ...splitArgs(f.pendingCommand)]
}

/** The server's rule for an agent's site (validateSite in internal/catalog), in words: '' when empty or an https URL with a host, a port from 1 to 65535 if any, and no user info. */
export function siteError(site: string): string {
  const s = site.trim()
  if (!s) return ''
  try {
    const u = new URL(s)
    if (u.protocol === 'https:' && u.hostname && u.port !== '0' && !u.username && !u.password && s.length <= 200 && !/\s/.test(s)) return ''
  } catch {
    /* not a URL */
  }
  return 'An https:// address, or nothing'
}

/** What is wrong with the form, by field; empty when it can be saved. */
export function formErrors(f: AgentForm): Partial<Record<Field, string>> {
  const e: Partial<Record<Field, string>> = {}
  if (!f.name.trim()) e.name = 'Give the agent a name'
  if (!AGENT_ID_PATTERN.test(f.id)) e.id = f.id ? 'Use lowercase letters, digits and dashes, up to 32' : 'An ID is required'
  if (hasOpenQuote(f.pendingCommand)) e.command = 'Close the quote, or remove it'
  else if (!commandOf(f)[0]?.trim()) e.command = 'Add the command to run'
  // Spaces count in a regex (a "> " prompt), so trim only to tell whether anything was typed.
  if (f.signal === 'pattern' && !f.pattern.trim()) e.pattern = 'Enter the pattern to look for'
  const site = siteError(f.site)
  if (site) e.site = site
  const profile = profileError(f)
  if (profile) e.profile = profile
  const seen = new Set<string>()
  for (const r of f.env) {
    const key = r.key.trim()
    if (!key) {
      if (r.value) e.env = 'Give every variable a name'
      continue
    }
    if (seen.has(key)) e.env = `${key} is listed twice`
    seen.add(key)
    if (!r.masked && r.value === MASK) e.env = `${key}: ${MASK} stands for a stored value; type the real value`
  }
  if (!f.yoloNone) {
    const yoloKeys = f.yoloEnv.map((r) => r.key.trim()).filter(Boolean)
    if (hasOpenQuote(f.yoloPending)) e.yolo = 'Close the quote in the yolo arguments, or remove it'
    else if (yoloArgsOf(f).length > MAX_YOLO || yoloKeys.length > MAX_YOLO) e.yolo = `At most ${MAX_YOLO} yolo arguments and ${MAX_YOLO} variables`
    else if (yoloKeys.some((k) => !YOLO_ENV_NAME.test(k) || k.startsWith('CONDUCTOR_'))) e.yolo = "A yolo variable's name is letters, digits and _, not starting with a digit nor CONDUCTOR_"
    else if (new Set(yoloKeys).size !== yoloKeys.length) e.yolo = 'A yolo variable is listed twice'
    else if (f.yoloEnv.some((r) => !r.key.trim() && r.value)) e.yolo = 'Give every yolo variable a name'
  }
  return e
}

/** The signal to save: the bell is what an agent without a signal gets, so it is left out unless the agent had one. The tool-events flag and the working title (Codex's), which the form has no control for, stay. */
export function signalOut(kind: SignalKind, pattern: string, prev?: AgentSignal): AgentSignal | undefined {
  const toolEvents = prev?.toolEvents || undefined
  const workingTitle = prev?.workingTitle || undefined
  switch (kind) {
    case 'pattern':
      return { kind: 'pattern', pattern, toolEvents, workingTitle }
    case 'hook':
      return { kind: 'hook', toolEvents, workingTitle }
    case 'none':
      return { kind: 'none', toolEvents, workingTitle }
    default:
      return prev ? { kind: 'bell', toolEvents, workingTitle } : undefined
  }
}

/** The body of POST /api/catalog. A masked row sends the mask, which keeps the stored value; a row without a value is a passthrough name. What the form has no control for comes from `prev`, the agent edited. */
export function agentPayload(f: AgentForm, prev?: AgentInfo): AgentInput {
  const env: Record<string, string> = {}
  const passthrough: string[] = []
  for (const row of f.env) {
    const key = row.key.trim()
    if (!key) continue
    if (row.masked) env[key] = MASK
    else if (row.value !== '') env[key] = row.value
    else passthrough.push(key)
  }
  return {
    id: f.id,
    name: f.name.trim(),
    description: f.description.trim() || undefined,
    site: f.site.trim() || undefined,
    command: commandOf(f),
    allowArgs: f.allowArgs,
    env: Object.keys(env).length ? env : undefined,
    envPassthrough: passthrough.length ? passthrough : undefined,
    cwd: prev?.cwd,
    icon: prev?.icon,
    adapter: prev?.adapter,
    signal: signalOut(f.signal, f.pattern, prev?.signal),
    yolo: yoloOut(f),
    // What the form has no control for comes from the agent edited.
    trustPrompt: prev?.trustPrompt,
    trustAnswers: prev?.trustAnswers,
    questions: prev?.questions,
    session: prev?.session,
    // A copy with its probe off would be probed again by a later save that left it out (Codex's review of #291).
    probe: prev?.probe,
    browser: browserOut(f, prev),
  }
}

/**
 * The add-agent form's line under the command, from POST /api/catalog/check:
 * found and where, not found, or not judged on the server (`atLaunch`: a
 * relative program, resolved at launch in the session's directory; `slow`: a
 * lookup that took too long), which the catalog lists as available.
 */
export type CommandCheck = { state: 'idle' | 'pending' | 'missing' | 'failed' | 'atLaunch' | 'slow' } | { state: 'found'; path: string; identity?: CommandCheckReply['identity'] }

export function commandCheck(r: CommandCheckReply, program: string): CommandCheck {
  if (r.found) return { state: 'found', path: r.path ?? program, ...(r.identity ? { identity: r.identity } : {}) }
  if (r.unknown === 'relative') return { state: 'atLaunch' }
  if (r.unknown) return { state: 'slow' }
  return { state: 'missing' }
}

/**
 * cloneId suggests an id for a copy of the agent `id` (#291): `<id>-copy`, then `-copy-2`, `-copy-3`… past the ids taken, each of the
 * server's shape (AGENT_ID_PATTERN: the id is cut to make room).
 */
export function cloneId(id: string, taken: readonly string[]): string {
  for (let n = 1; n < 1000; n++) {
    const suffix = n === 1 ? '-copy' : `-copy-${n}`
    const candidate = id.slice(0, 32 - suffix.length).replace(/-+$/, '') + suffix
    if (!taken.includes(candidate)) return candidate
  }
  return ''
}

/** formForClone is the form filled from the agent copied (#291): its settings, the stored values as masked rows, under a new name and id. */
export function formForClone(source: AgentInfo, taken: readonly string[], uid: () => number): AgentForm {
  const f = formFromAgent(source, uid)
  f.name = `${source.name} (copy)`.slice(0, 60)
  f.id = cloneId(source.id, taken)
  return f
}
