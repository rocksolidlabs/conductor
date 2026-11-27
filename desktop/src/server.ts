import { EventEmitter } from 'node:events'
import type { ChildProcess } from 'node:child_process'
import { HandshakeReader, type Handshake } from './handshake'
import type { Launcher } from './launcher'

export type ServerState = 'stopped' | 'starting' | 'running' | 'failed'

export interface ServerStatus {
  state: ServerState
  url: string
  /**
   * The address the server bound, as an origin (http://127.0.0.1:<port>): what the app loads its windows from, trusts and talks to,
   * so that no name is resolved to reach its server. '' when the server bound no loopback address written as an address.
   */
  origin?: string
  pid?: number
  version?: string
  failures: number
  lastError?: string
}

export interface SupervisorOptions {
  launcher: Launcher
  args: string[]
  env: () => Record<string, string>
  /** The first line written on the server's stdin as it is spawned (secretsLine): its secrets, which no environment holds. */
  firstLine?: () => string
  /** Runs before each start of the server, before env(): the app reads the shell's PATH again while it has none (#363). */
  beforeStart?: () => Promise<void>
  /** How long beforeStart may take (60 s): one that does not settle by then is left behind and the start goes on without it. */
  prepareMs?: number
  log: (kind: 'main' | 'server', line: string) => void
  /** How long the handshake may take (60 s: WSL may have to boot). */
  handshakeMs?: number
  /** Health poll period and the failures that trigger a restart. */
  healthMs?: number
  /** The restart backoff, from min doubling to max; after `giveUpAfter` failures within `giveUpWindowMs` the supervisor stops. */
  backoffMinMs?: number
  backoffMaxMs?: number
  giveUpAfter?: number
  giveUpWindowMs?: number
  /** fetch, replaceable in tests. */
  fetch?: typeof fetch
  stopGraceMs?: number
}

/**
 * ServerSupervisor keeps one `conductor serve` running: it spawns it through the launcher, reads the handshake from its stdout, polls
 * its health, restarts it with backoff when it dies or stops answering, and gives up after too many failures in a row. The admin
 * token travels on the server's stdin (firstLine), never in its environment, its arguments or on disk.
 */
/** The start was stopped (stop) while it prepared, before the server spawned: nothing runs, and nothing is restarted. */
/**
 * loopbackOrigin is http://<listen> for a listen address that is a loopback address written as one (127.x.x.x or [::1]) with a port,
 * '' for anything else (a name, another address, no port): the app talks to its server by the socket it bound, never by a name.
 */
export function loopbackOrigin(listen: string): string {
  const m = /^(127\.\d{1,3}\.\d{1,3}\.\d{1,3}|\[::1\]):(\d{1,5})$/.exec(listen)
  if (!m) return ''
  const port = Number(m[2])
  if (port < 1 || port > 65535) return ''
  if (m[1]!.startsWith('127.') && m[1]!.split('.').some((o) => Number(o) > 255)) return ''
  return `http://${m[1]}:${port}`
}

/**
 * trustedOrigin is the origin the app trusts now: its server's bound address while that server runs, '' otherwise. While the server
 * stops, starts or has failed, no page is answered and nothing is loaded or sent: a port it freed may be another program's.
 */
export function trustedOrigin(st: ServerStatus): string {
  return st.state === 'running' ? (st.origin ?? '') : ''
}

export class StartStopped extends Error {
  constructor() {
    super('the server was stopped before it started')
    this.name = 'StartStopped'
  }
}

export class ServerSupervisor extends EventEmitter {
  status: ServerStatus = { state: 'stopped', url: '', failures: 0 }
  handshake: Handshake | null = null
  private child: ChildProcess | null = null
  private wanted = false
  /**
   * Each start is numbered, and the newest start's promise kept (current): a start superseded while it prepared, by a stop or by
   * another start (Restart server during a slow shell read, #363), spawns nothing; it reports the newer start's outcome, or
   * StartStopped (Codex's review: two servers from overlapping starts, and a stop that saw no child yet and let the start go on).
   */
  private generation = 0
  private current: Promise<Handshake> | null = null
  private stopping: Promise<void> | null = null
  private healthTimer: NodeJS.Timeout | null = null
  private restartTimer: NodeJS.Timeout | null = null
  private failTimes: number[] = []
  private backoff: number

  constructor(private o: SupervisorOptions) {
    super()
    this.backoff = o.backoffMinMs ?? 1000
  }

  /** start runs the server until stop; it resolves with the first handshake, or rejects when the first start fails. */
  async start(): Promise<Handshake> {
    this.wanted = true
    return this.run()
  }

  private setState(state: ServerState, extra: Partial<ServerStatus> = {}) {
    this.status = { ...this.status, ...extra, state }
    this.emit('state', this.status)
  }

  /** run is a start, kept as the newest (current) for a start it supersedes. */
  private run(): Promise<Handshake> {
    const p = this.spawnOnce()
    this.current = p
    return p
  }

  /**
   * prepare runs beforeStart within prepareMs: one that fails is logged, one that does not settle in time (a shell that ignores
   * its signal) is left behind with a line in the log, and the start goes on with what the app has.
   */
  private async prepare(): Promise<void> {
    const before = this.o.beforeStart
    if (!before) return
    const wait = this.o.prepareMs ?? 60_000
    let timer: NodeJS.Timeout | undefined
    const deadline = new Promise<void>((resolve) => {
      timer = setTimeout(() => {
        this.o.log('main', `the start's preparation did not finish within ${Math.round(wait / 1000)} s; starting without it`)
        resolve()
      }, wait)
    })
    try {
      await Promise.race([before().catch((e) => this.o.log('main', `the start's preparation failed: ${(e as Error).message}`)), deadline])
    } finally {
      clearTimeout(timer)
    }
  }

  private async spawnOnce(): Promise<Handshake> {
    this.setState('starting')
    const gen = ++this.generation
    await this.prepare()
    if (gen !== this.generation && this.current) {
      // Started again while this one prepared (a restart): that start spawns, and this one is its outcome.
      return this.current
    }
    if (!this.wanted) throw new StartStopped()
    const env = this.o.env()
    const child = this.o.launcher.spawn(this.o.args, env)
    this.child = child
    // A server that exits before it read the line closes its stdin: nothing to do but let it fail as it does.
    child.stdin?.on('error', () => {})
    const first = this.o.firstLine?.()
    if (first) child.stdin?.write(first)
    const reader = new HandshakeReader()
    const handshakeMs = this.o.handshakeMs ?? 60_000
    const handshake = new Promise<Handshake>((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error(`the server did not report its address within ${handshakeMs / 1000} s`)), handshakeMs)
      child.stdout?.setEncoding('utf8')
      child.stdout?.on('data', (chunk: string) => {
        const h = reader.feed(chunk)
        if (h) {
          clearTimeout(timer)
          for (const n of reader.noise) this.o.log('server', n)
          resolve(h)
        } else if (reader.found) {
          for (const line of chunk.split('\n')) if (line.trim()) this.o.log('server', line)
        }
      })
      child.stderr?.setEncoding('utf8')
      child.stderr?.on('data', (chunk: string) => {
        for (const line of chunk.split('\n')) if (line.trim()) this.o.log('server', line)
      })
      child.once('error', (err) => {
        clearTimeout(timer)
        reject(err)
      })
      child.once('exit', (code, signal) => {
        clearTimeout(timer)
        reject(new Error(`the server exited before it listened (${signal ?? code})`))
      })
    })
    child.on('exit', (code, signal) => this.onExit(child, code, signal))
    try {
      const h = await handshake
      this.handshake = h
      this.backoff = this.o.backoffMinMs ?? 1000
      this.setState('running', { url: h.publicUrl, origin: loopbackOrigin(h.listen), pid: h.pid, version: h.version, lastError: undefined })
      this.o.log('main', `server listening at ${h.listen} (${h.publicUrl}), pid ${h.pid}, ${h.version}`)
      this.startHealth()
      return h
    } catch (e) {
      const err = e as Error
      this.setState('failed', { lastError: err.message })
      this.o.log('main', `server start failed: ${err.message}`)
      throw err
    }
  }

  private onExit(child: ChildProcess, code: number | null, signal: NodeJS.Signals | null) {
    if (this.child !== child) return
    this.child = null
    this.stopHealth()
    const why = signal ? `signal ${signal}` : `exit code ${code}`
    if (!this.wanted) {
      this.setState('stopped', { pid: undefined })
      this.o.log('main', `server stopped (${why})`)
      return
    }
    this.o.log('main', `server died (${why}); restarting`)
    this.scheduleRestart(why)
  }

  private scheduleRestart(why: string) {
    const now = Date.now()
    const window = this.o.giveUpWindowMs ?? 5 * 60_000
    this.failTimes = this.failTimes.filter((t) => now - t < window)
    this.failTimes.push(now)
    const failures = this.failTimes.length
    this.setState('failed', { failures, lastError: why, pid: undefined })
    if (failures >= (this.o.giveUpAfter ?? 6)) {
      this.o.log('main', `the server failed ${failures} times in ${window / 60000} min; not restarting`)
      this.emit('gaveUp', this.status)
      return
    }
    const wait = this.backoff
    this.backoff = Math.min(this.backoff * 2, this.o.backoffMaxMs ?? 30_000)
    this.restartTimer = setTimeout(() => {
      this.restartTimer = null
      if (!this.wanted) return
      this.run().catch((e) => {
        if (!(e instanceof StartStopped)) this.scheduleRestart(this.status.lastError ?? 'start failed')
      })
    }, wait)
  }

  private startHealth() {
    this.stopHealth()
    const period = this.o.healthMs ?? 5000
    const f = this.o.fetch ?? fetch
    let misses = 0
    this.healthTimer = setInterval(async () => {
      if (!this.handshake || !this.child) return
      try {
        const res = await f(`${loopbackOrigin(this.handshake.listen) || this.handshake.publicUrl}/api/health`, { signal: AbortSignal.timeout(period) })
        misses = res.ok ? 0 : misses + 1
      } catch {
        misses++
      }
      if (misses >= 3) {
        this.o.log('main', 'the server stopped answering; restarting it')
        misses = 0
        this.restart().catch(() => {})
      }
    }, period)
  }

  private stopHealth() {
    if (this.healthTimer) clearInterval(this.healthTimer)
    this.healthTimer = null
  }

  /** stop ends the server: its stdin closed (it shuts down on that), then a kill after the grace period. */
  stop(): Promise<void> {
    if (this.stopping) return this.stopping
    this.wanted = false
    if (this.restartTimer) clearTimeout(this.restartTimer)
    this.restartTimer = null
    this.stopHealth()
    const child = this.child
    if (!child) {
      this.setState('stopped', { pid: undefined })
      return Promise.resolve()
    }
    this.stopping = new Promise<void>((resolve) => {
      const grace = this.o.stopGraceMs ?? 12_000
      const timer = setTimeout(() => {
        this.o.log('main', 'the server did not stop in time; killing it')
        this.o.launcher.kill(child, this.handshake?.pid)
      }, grace)
      child.once('exit', () => {
        clearTimeout(timer)
        this.stopping = null
        resolve()
      })
      try {
        child.stdin?.end()
      } catch {
        this.o.launcher.kill(child, this.handshake?.pid)
      }
    })
    return this.stopping
  }

  /** restart stops and starts again, with the same settings read afresh (env()). */
  async restart(): Promise<Handshake> {
    await this.stop()
    this.failTimes = []
    return this.start()
  }
}
