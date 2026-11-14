import type { AgentInfo, Identity } from '~/composables/useSessions'
import { agentIcon } from './agentIcons'

export type IdentityState = 'ok' | 'impostor' | 'unidentified' | 'pending' | 'failed' | 'unprobed'

/** What the identity probe says of an agent, for a badge: the state, its words, and a longer title. */
export function identity(a: Pick<AgentInfo, 'identity' | 'command'>, host: string): { state: IdentityState; label: string; title: string } {
  const id = a.identity
  const program = a.command[0] ?? ''
  if (!id) return { state: 'unprobed', label: '', title: '' }
  if (id.pending) return { state: 'pending', label: 'Checking the version…', title: `${program} --version is being run on ${where(host)}` }
  if (!id.ran) return { state: 'failed', label: 'Version check failed', title: id.error ?? `${program} could not be run on ${where(host)}` }
  if (id.identified) return { state: 'ok', label: `${id.name ?? 'Identified'}${id.version ? ` ${id.version}` : ''}`, title: `${program} on ${where(host)} is ${id.name ?? 'the agent'}${id.version ? ` ${id.version}` : ''}` }
  if (id.impostor) return { state: 'impostor', label: `Not ${id.name ?? 'the agent'}`, title: `${program} on ${where(host)} is another program: it printed ${quote(id.output)}` }
  if (id.verified) return { state: 'impostor', label: `Not ${id.name ?? 'the agent'}`, title: `${program} on ${where(host)} did not identify as ${id.name ?? 'the agent'}: it printed ${quote(id.output)}` }
  return { state: 'unidentified', label: 'Version unrecognised', title: `${program} on ${where(host)} printed ${quote(id.output)}, which Conductor does not know as ${id.name ?? 'the agent'}'s yet` }
}

/** The badge of an agent the server is still checking (#704): its program being looked up, or run with its version flag. */
export function checkingBadge(a: Pick<AgentInfo, 'identity' | 'command'>, host: string): { label: string; title: string } {
  if (a.identity?.pending) return identity(a, host)
  return { label: 'Checking…', title: `Looking for ${a.command[0] ?? ''} on ${where(host)}` }
}

/**
 * Reads each agent the server is still checking until it has its answer (#704), `workers` at a time so the page's other requests are
 * not queued behind them, and hands each answer to `apply`. A read waits on the server for the agent's checks (about 4 s at most); an
 * agent still checking after it is read again, `reads` times in all. `stop` ends it (a newer read of the list, the page left); a read
 * that fails is handed to `failed`.
 */
export async function settleChecks<T extends { checking?: boolean }>(
  ids: readonly string[],
  read: (id: string) => Promise<T>,
  apply: (a: T) => void,
  opts: { stop: () => boolean; failed: (id: string) => void; workers?: number; reads?: number },
): Promise<void> {
  const queue = [...ids]
  const reads = opts.reads ?? 10
  async function work() {
    for (let id = queue.shift(); id !== undefined; id = queue.shift()) {
      for (let n = 0; n < reads; n++) {
        let a: T
        try {
          a = await read(id)
        } catch {
          if (!opts.stop()) opts.failed(id)
          break
        }
        if (opts.stop()) return
        apply(a)
        if (!a.checking) break
      }
      if (opts.stop()) return
    }
  }
  await Promise.all(Array.from({ length: Math.min(opts.workers ?? 2, queue.length) }, work))
}

function quote(s?: string): string {
  return s ? `"${s}"` : 'nothing'
}

/** Whether the probe says the program is not the agent (a known impostor, or a verified check that matched nothing): a crew with it is refused. */
export function misidentified(id?: Identity): boolean {
  return !!id && id.ran && (!!id.impostor || (id.verified && !id.identified))
}

/** Whether the server can launch the agent now. A server that does not say (older) is taken to mean yes. */
export function isAvailable(a: Pick<AgentInfo, 'available'>): boolean {
  return a.available !== false
}

/** Where an agent is not installed: the server's host name as the server gives it, or "the server" when it is unknown. */
function where(host: string): string {
  return host || 'the server'
}

/** The note on an agent whose program the server did not find; host is the server's host name, or empty; onWindows where a Windows copy was seen from WSL. */
export function notInstalled(host: string, onWindows?: string): string {
  if (onWindows) return 'Found on Windows, not in WSL'
  return `Not installed on ${where(host)}`
}

/** The note's tooltip: how the server judged it. A bare name is looked up on the server's PATH, where any program of that name counts. */
export function notInstalledTitle(program: string, onWindows?: string): string {
  if (onWindows) return `${onWindows} is Windows's, seen through interop; install ${program} inside the WSL distribution (where the server runs) and it is found there`
  if (program.includes('/')) return `${program} was not found on the server`
  return `No program named ${program} is on the server's PATH (any program of that name counts as installed)`
}

/**
 * The not-installed badge of an agent the server cannot launch: its words,
 * its tooltip, and notAgent when a program of the agent's name is there but
 * is another one (the Go migrations tool called goose is not Goose), which
 * the server never offers.
 */
export function notInstalledBadge(a: Pick<AgentInfo, 'identity' | 'command' | 'onWindows'>, host: string): { label: string; title: string; notAgent: boolean } {
  const program = a.command[0] ?? ''
  const id = a.identity
  if (id?.ran && id.impostor) {
    const name = id.name ?? 'the agent'
    return { label: notInstalled(host), title: `${program} on ${where(host)} is another program, not ${name}: it printed ${quote(id.output)}. Install ${name} to launch it.`, notAgent: true }
  }
  return { label: notInstalled(host, a.onWindows), title: notInstalledTitle(program, a.onWindows), notAgent: false }
}

/** The agents the Launch dialog's server tab offers. */
export function serverAgents<T extends Pick<AgentInfo, 'available'>>(list: readonly T[]): T[] {
  return list.filter(isAvailable)
}

/**
 * What the Launch dialog says where its agents go when it lists none (round 14): `checking` while the first catalog of this page is
 * under way (a blank grid read as "no agents" for seconds on the Windows app's first open), `none-installed` when the catalog has agents
 * but none on this server, `none` when it has none; null when it lists some.
 */
export function launchListNote(s: { loading: boolean; known: number; offered: number; onServer: boolean }): 'checking' | 'none-installed' | 'none' | null {
  if (s.offered > 0) return null
  if (s.loading) return 'checking'
  return s.onServer && s.known > 0 ? 'none-installed' : 'none'
}

/** A select item for an agent, marked when it is not installed; never disabled, since a crew may be edited before its agents are installed. */
export function agentItem(a: AgentInfo, host: string): { label: string; value: string; icon: string } {
  if (!isAvailable(a)) return { label: `${a.name} · not installed on ${where(host)}`, value: a.id, icon: 'i-lucide-circle-off' }
  if (misidentified(a.identity)) return { label: `${a.name} · not ${a.identity?.name ?? a.name} on ${where(host)}`, value: a.id, icon: 'i-lucide-circle-off' }
  return { label: a.name, value: a.id, icon: agentIcon(a.icon) }
}
