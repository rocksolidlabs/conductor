/**
 * What the Launch agent dialog remembers of the last launch, per viewer, in this app's storage under one key: the agent (never the
 * built-in shell), the folder, for each agent its last model and the last model names typed, and whether More options was left
 * open. Written only after a launch the server took; read as the dialog opens. Never kept: the arguments (they can hold a key), the
 * task, the name, the account picked and Yolo (which follows Settings' default at every opening). In the desktop app this lasts while
 * Conductor runs: the app's server takes a fresh port at each start, and what a page keeps goes with it. Every read and write is
 * guarded: storage that throws, is full or holds something else gives an empty memory, and the dialog works without one.
 */

/** The storage key. */
export const LAST_LAUNCH_KEY = 'conductor.launch.last'
/** The model names kept for an agent, newest first. */
export const NAMES_KEPT = 5
/** The agents whose models are kept; the one launched longest ago goes first. */
export const AGENTS_KEPT = 32
/** The built-in shell's id: never remembered as the agent to pick. */
export const SHELL_ID = 'shell'

/** The longest text kept for a folder or a model name: anything longer is not something a person typed here. */
const LONGEST = 4096

export interface AgentMemory {
  /** The model of its last launch, '' for none. */
  model: string
  /** The model names typed at its launches, newest first, each once. */
  names: string[]
}

export interface LastLaunch {
  agent?: string
  folder?: string
  /** By agent id, in the order they were last launched (the oldest first). */
  models: Record<string, AgentMemory>
  moreOpen?: boolean
}

type Readable = Pick<Storage, 'getItem'> | null | undefined
type Writable = Pick<Storage, 'getItem' | 'setItem'> | null | undefined

const text = (v: unknown): v is string => typeof v === 'string' && v.length <= LONGEST

/** The memory as kept, or an empty one for anything that cannot be read or is not what this module writes. */
export function readLast(storage: Readable): LastLaunch {
  let v: unknown
  try {
    v = JSON.parse(storage?.getItem(LAST_LAUNCH_KEY) ?? 'null')
  } catch {
    return { models: {} }
  }
  if (!v || typeof v !== 'object' || Array.isArray(v)) return { models: {} }
  const o = v as Record<string, unknown>
  const out: LastLaunch = { models: {} }
  if (text(o.agent) && o.agent && o.agent !== SHELL_ID) out.agent = o.agent
  if (text(o.folder)) out.folder = o.folder
  if (typeof o.moreOpen === 'boolean') out.moreOpen = o.moreOpen
  if (o.models && typeof o.models === 'object' && !Array.isArray(o.models)) {
    for (const [id, m] of Object.entries(o.models as Record<string, unknown>).slice(-AGENTS_KEPT)) {
      if (!m || typeof m !== 'object' || Array.isArray(m)) continue
      const r = m as Record<string, unknown>
      const names = Array.isArray(r.names) ? [...new Set(r.names.filter((n): n is string => text(n) && !!n.trim()))].slice(0, NAMES_KEPT) : []
      out.models[id] = { model: text(r.model) ? r.model : '', names }
    }
  }
  return out
}

/** A launch the server took, as the memory keeps it. */
export interface Launched {
  agent: string
  folder: string
  model: string
  moreOpen: boolean
}

/** The memory after a launch: its agent (unless the shell), its folder, its model at the front of the agent's names, the fold's state. */
export function afterLaunch(prev: LastLaunch, l: Launched): LastLaunch {
  const model = l.model.trim()
  const before = prev.models[l.agent]?.names ?? []
  const names = model ? [model, ...before.filter((n) => n !== model)].slice(0, NAMES_KEPT) : before
  // The launched agent's entry moves to the end: the oldest launched is the first let go.
  const rest = Object.entries(prev.models).filter(([id]) => id !== l.agent)
  const models = Object.fromEntries([...rest, [l.agent, { model, names }] as const].slice(-AGENTS_KEPT))
  return {
    agent: l.agent === SHELL_ID ? prev.agent : l.agent,
    folder: l.folder.trim(),
    models,
    moreOpen: l.moreOpen,
  }
}

/** Keeps the memory; storage that refuses (private, full, blocked) is told nothing and the next opening starts as it comes. */
export function writeLast(storage: Writable, v: LastLaunch): void {
  try {
    storage?.setItem(LAST_LAUNCH_KEY, JSON.stringify(v))
  } catch {
    // Kept nowhere: the dialog works without it.
  }
}

/**
 * The memory with the server's record of the last launch over it (GET /api/launch/last, kept in the server's data directory, so it
 * outlasts a restart of the server, which this app's storage does not): the server's agent, folder and models win, since it saw
 * every launch; the fold's state is this viewer's own. No record (an older server, a refusal): the memory as it is.
 */
export function withRecord(local: LastLaunch, rec: { agent?: string; folder?: string; models?: Record<string, { model?: unknown; names?: unknown }> } | null | undefined): LastLaunch {
  if (!rec || typeof rec !== 'object') return local
  const read = readLast({ getItem: () => JSON.stringify({ agent: rec.agent, folder: rec.folder, models: rec.models }) })
  if (!read.agent && read.folder === undefined && !Object.keys(read.models).length) return local
  return { ...read, ...(local.moreOpen !== undefined ? { moreOpen: local.moreOpen } : {}) }
}

/**
 * How long a page waits for the server's record (GET /api/launch/last) before it goes on with its own memory alone: LAST_LAUNCH_WAIT_MS,
 * or what the page was given as `conductorLastLaunchMs` (a positive number on the window, an e2e spec's, so that only a hang bounds
 * its read).
 */
export const LAST_LAUNCH_WAIT_MS = 3000
export function lastLaunchWaitMs(scope: { conductorLastLaunchMs?: unknown } = globalThis as { conductorLastLaunchMs?: unknown }): number {
  const ms = scope.conductorLastLaunchMs
  return typeof ms === 'number' && Number.isFinite(ms) && ms > 0 ? ms : LAST_LAUNCH_WAIT_MS
}

/** The server's record as `ask` gives it, or null for a refusal or none within lastLaunchWaitMs. */
export function recordWithin<T>(ask: () => Promise<T>): Promise<T | null> {
  let timer: ReturnType<typeof setTimeout> | undefined
  const late = new Promise<null>((r) => (timer = setTimeout(() => r(null), lastLaunchWaitMs())))
  return Promise.race([ask().catch(() => null), late]).finally(() => clearTimeout(timer))
}

/** Each agent's way of its last launch on the server (its record's `way`): Terminal or Chat; an agent with none is left out. */
export function recordWays(rec: { models?: Record<string, { way?: unknown }> } | null | undefined): Map<string, 'terminal' | 'protocol'> {
  const out = new Map<string, 'terminal' | 'protocol'>()
  if (!rec?.models || typeof rec.models !== 'object') return out
  for (const [id, m] of Object.entries(rec.models)) if (m && (m.way === 'terminal' || m.way === 'protocol')) out.set(id, m.way)
  return out
}

/** This app's storage, or null where reading it throws (blocked site data, a sandboxed page). */
export function launchStorage(): Storage | null {
  try {
    return typeof localStorage === 'undefined' ? null : localStorage
  } catch {
    return null
  }
}
