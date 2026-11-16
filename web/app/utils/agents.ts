import type { AgentInfo, Identity } from '~/composables/useSessions'
import { agentIcon } from './agentIcons'
import { withDeadline, type After } from './capacity'

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

/**
 * The badge of an agent's check on the Agents page (#704), or null when the agent's own state is shown: `checking` while the server
 * looks its program up or runs its version flag, `unanswered` once the page gave up reading it (`gaveUp`: its reads ran out or one
 * failed), which hides any state half found.
 */
export function checkBadge(a: Pick<AgentInfo, 'identity' | 'command' | 'checking'>, host: string, gaveUp: boolean): { state: 'checking' | 'unanswered'; label: string; title: string } | null {
  const program = a.command[0] ?? ''
  if (gaveUp) return { state: 'unanswered', label: 'Check did not answer', title: `The server did not answer the check of ${program} on ${where(host)}; the page checks again when it is opened or refreshed` }
  if (!a.checking) return null
  if (a.identity?.pending) return { state: 'checking', label: identity(a, host).label, title: identity(a, host).title }
  return { state: 'checking', label: 'Checking…', title: `Looking for ${program} on ${where(host)}` }
}

/** How long one read of an agent's check may take: the server waits about 4 s at most for its checks. */
export const CHECK_READ_DEADLINE_MS = 10_000

/**
 * Reads each agent the server is still checking, or shows from an answer that has expired, until it has its answer (#704), `workers`
 * at a time so the page's other requests are not queued behind them, and hands each answer to `apply`. An agent still checking goes
 * to the back of the line, behind the agents not yet read, and is read `reads` times at most. A read is aborted past
 * `CHECK_READ_DEADLINE_MS` and when `signal` is (a newer read of the list, the page left), which ends it all; an agent whose read
 * failed or whose reads ran out is handed to `gaveUp`.
 */
export async function settleChecks<T extends { checking?: boolean }>(
  ids: readonly string[],
  read: (id: string, signal: AbortSignal) => Promise<T>,
  apply: (a: T) => void,
  opts: { signal: AbortSignal; gaveUp: (id: string) => void; workers?: number; reads?: number; after?: After },
): Promise<void> {
  const queue = ids.map((id) => ({ id, n: 0 }))
  const reads = opts.reads ?? 10
  async function work() {
    for (let q = queue.shift(); q; q = queue.shift()) {
      const { id } = q
      let a: T
      try {
        a = await withDeadline((s) => read(id, AbortSignal.any([s, opts.signal])), CHECK_READ_DEADLINE_MS, 'The check did not answer', opts.after)
      } catch {
        if (opts.signal.aborted) return
        opts.gaveUp(id)
        continue
      }
      if (opts.signal.aborted) return
      apply(a)
      if (!a.checking) continue
      if (++q.n < reads) queue.push(q)
      else opts.gaveUp(id)
    }
  }
  await Promise.all(Array.from({ length: Math.min(opts.workers ?? 2, queue.length) }, work))
}

/**
 * The life of a page's requests (#704): nothing the page started outlives it. `next()` starts a generation of reads and aborts the one
 * before; `live(p)` hands on p's answer, or its failure, only while the page lives. Once the page is left (`end()`), every generation is
 * aborted, `next()` hands back a signal already aborted, and an answer still to come never settles, so it starts nothing and changes nothing.
 */
export function pageRequests() {
  const life = new AbortController()
  let gen = new AbortController()
  return {
    left: () => life.signal.aborted,
    next(): AbortSignal {
      gen.abort()
      gen = new AbortController()
      if (life.signal.aborted) gen.abort()
      return gen.signal
    },
    live: <T>(p: Promise<T>) =>
      new Promise<T>((resolve, reject) =>
        p.then(
          (v) => !life.signal.aborted && resolve(v),
          (e) => !life.signal.aborted && reject(e),
        ),
      ),
    end() {
      life.abort()
      gen.abort()
    },
  }
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
