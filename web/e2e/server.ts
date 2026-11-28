import { execFileSync, spawn } from 'node:child_process'
import { closeSync, existsSync, lstatSync, mkdirSync, mkdtempSync, openSync, readdirSync, readFileSync, renameSync, rmSync, statSync, symlinkSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:net'
import { homedir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { createHash, randomBytes } from 'node:crypto'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))

/** The checkout's root, where `make build-go` leaves bin/conductor. */
export const repoRoot = resolve(here, '..', '..')
/** The stub agent the test config gives the claude and codex ids. */
export const stubPath = join(here, 'stub-agent.sh')

/** The ports the suite may listen on: the first free one from 18400 + (pid mod 50), wrapping. */
export const PORT_MIN = 18400
export const PORT_MAX = 18499
/** How long the server has to answer /api/health. */
export const HEALTH_WAIT_MS = 20_000
/**
 * How long a try's child has to end on SIGTERM before it is killed: the rest of its start-up (HEALTH_WAIT_MS is what one is given)
 * and then the 10 s serve's shutdown gives itself. It only bounds a hang.
 */
export const STOP_WAIT_MS = HEALTH_WAIT_MS + 10_000

/** The server binary: CONDUCTOR_E2E_BIN, else bin/conductor of this checkout. */
export function serverBinary(): string {
  const bin = process.env.CONDUCTOR_E2E_BIN || join(repoRoot, 'bin', 'conductor')
  if (!existsSync(bin)) throw new Error(`${bin} is missing: build it first (make build-go), or run make test-e2e`)
  return bin
}

/**
 * Find a bash with fractional read timeouts and the stub's numeric microsecond clock: bash 5's EPOCHREALTIME,
 * or bash 4 plus GNU date. Probe with the PATH the server will use, including any candidate directory first.
 * macOS's bash 3.2 and bash 4 with BSD date cannot run the stub reliably; fail once with the requirement.
 */
export function stubBash(path = process.env.PATH ?? '/usr/local/bin:/usr/bin:/bin'): { command: string; path: string } {
  // This is the same clock selection as stub-agent.sh's now_us. Static program; no caller data enters shell text.
  const probe = `printf '%s\n' "$BASH" "\${BASH_VERSINFO[0]}"
if [[ -n \${EPOCHREALTIME:-} ]]; then
  printf '%s\n' "\${EPOCHREALTIME/[.,]/}"
else
  date +%s%6N
fi`
  const usable = (bash: string, childPath: string): string | undefined => {
    try {
      const output = execFileSync(bash, ['-c', probe], {
        env: { PATH: childPath, LANG: 'C.UTF-8' },
        encoding: 'utf8',
        stdio: ['ignore', 'pipe', 'ignore'],
      })
      const result = /^(\/[^\n]+)\n([0-9]+)\n([0-9]{16,})\n?$/.exec(output)
      return result !== null && Number(result[2]) >= 4 ? result[1] : undefined
    } catch {
      return undefined
    }
  }
  const command = usable('bash', path)
  if (command) return { command, path }
  for (const dir of ['/opt/homebrew/bin', '/usr/local/bin', join(homedir(), '.local', 'bin')]) {
    const candidatePath = `${dir}:${path}`
    const command = usable(join(dir, 'bash'), candidatePath)
    if (command) return { command, path: candidatePath }
  }
  throw new Error('the stub agent (web/e2e/stub-agent.sh) needs bash 5 or bash 4 with GNU date (a numeric microsecond clock); none works on PATH or at /opt/homebrew/bin, /usr/local/bin or ~/.local/bin (macOS ships bash 3.2): install one and run the suite again')
}

/**
 * The programs the built-in catalog (internal/catalog/defaults.go) runs by a bare name: the real agent CLIs. A test server never
 * finds one (agentFreePath), so it never runs one, not even its version probe (#961); the suite's agents are the stubs of
 * conductor.e2e.json. agent-path.spec.ts fails when the built-in catalog names a program this list does not.
 */
export const CATALOG_PROGRAMS: readonly string[] = ['claude', 'codex', 'agy', 'copilot', 'cursor-agent', 'opencode', 'pi', 'omp', 'aider', 'goose', 'amp', 'dsh']

/** Whether path is an entry of its directory, of any kind (a file, a link, a link whose target is missing), its link not followed. */
function isEntry(path: string): boolean {
  try {
    lstatSync(path)
    return true
  } catch {
    return false
  }
}

/** Each PATH directory's judgement, with the modification time it was made at: a directory changed since is judged again. */
const judged = new Map<string, { mtime: bigint; holds: string[] }>()

/**
 * path less the built-in catalog's programs: a directory on it that holds one (a Node version manager's bin with an agent CLI in it,
 * ~/.local/bin) is replaced by a directory under into of links to everything else it holds, so git, node, nvim and the rest are found
 * as before and no agent CLI is. A directory that holds none is kept as it is: on a machine with no agent CLI the PATH is unchanged.
 * Each call stats each directory once: one changed since it was judged (an agent CLI or a tool put in it) is judged again, and its
 * links, named by its path and that time, are made again. The time is taken before the directory is read, so a change during the
 * read is seen by the next call. Links are made whole before they are put in place. A directory that holds a catalog program and
 * cannot be listed fails the start: none of its other programs could be kept.
 */
export function agentFreePath(path: string, into: string): string {
  const hidden = (name: string) => CATALOG_PROGRAMS.includes(name.toLowerCase())
  return path
    .split(':')
    .map((entry) => {
      if (entry === '') return entry
      const dir = resolve(entry)
      let mtime: bigint
      try {
        mtime = statSync(dir, { bigint: true }).mtimeNs
      } catch {
        return entry
      }
      // A time within the file system's tick of now can be a later change's too: such a judgement and its links are not kept.
      const settled = BigInt(Date.now()) * 1_000_000n - mtime > 2_000_000_000n
      let j = judged.get(dir)
      if (j?.mtime !== mtime || !settled) {
        // By the entry itself (lstat), never what it points to: a link whose target is missing just now is a program once it is back.
        j = { mtime, holds: CATALOG_PROGRAMS.filter((name) => isEntry(join(dir, name))) }
        if (settled) judged.set(dir, j)
      }
      if (j.holds.length === 0) return entry
      const links = join(into, `${createHash('sha256').update(dir).digest('hex').slice(0, 16)}-${mtime}`)
      if (settled && existsSync(links)) return links
      let names: string[]
      try {
        names = readdirSync(dir)
      } catch (e) {
        const code = (e as NodeJS.ErrnoException).code ?? String(e)
        throw new Error(`${dir} on the PATH holds ${j.holds.join(', ')} (a program the built-in catalog runs) and cannot be listed (${code}): a test server cannot be given its other programs without it; take it off the PATH or make it readable`)
      }
      mkdirSync(into, { recursive: true })
      const making = mkdtempSync(`${links}.`)
      for (const name of names) if (!hidden(name)) symlinkSync(join(dir, name), join(making, name))
      if (!settled) return making
      try {
        renameSync(making, links)
      } catch {
        // Another server of this HOME put the same links there first.
        rmSync(making, { recursive: true, force: true })
      }
      return links
    })
    .join(':')
}

/**
 * The PATH a server is given, from the one stubBash chose (the caller's, with a directory put ahead for its bash on a machine whose
 * first bash cannot run the stub): o.first ahead of all of it, then the real agent CLIs taken off (agentFreePath) unless o.realAgents.
 */
export function serverPath(o: { home: string; first?: string; realAgents?: boolean }, bashPath: string): string {
  const path = o.first ? `${o.first}:${bashPath}` : bashPath
  return o.realAgents ? path : agentFreePath(path, join(o.home, '.conductor-e2e-path'))
}

function portFree(port: number): Promise<boolean> {
  return new Promise((done) => {
    const s = createServer()
    s.once('error', () => done(false))
    s.listen(port, '127.0.0.1', () => s.close(() => done(true)))
  })
}

export async function pickPort(): Promise<number> {
  const span = PORT_MAX - PORT_MIN + 1
  const first = process.pid % 50
  for (let i = 0; i < span; i++) {
    const port = PORT_MIN + ((first + i) % span)
    if (await portFree(port)) return port
  }
  throw new Error(`no free port from ${PORT_MIN} to ${PORT_MAX}`)
}

/** git as argv, in cwd; its output. */
export function git(cwd: string, ...args: string[]): string {
  return execFileSync('git', args, { cwd, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] })
}

/** Makes dir a git repository on branch main with one commit, its identity in its own config (the server's HOME has none). */
export function scratchRepo(dir: string): void {
  git(dir, 'init', '-q')
  // git before 2.28 has no init -b.
  git(dir, 'symbolic-ref', 'HEAD', 'refs/heads/main')
  git(dir, 'config', 'user.name', 'conductor-e2e')
  git(dir, 'config', 'user.email', 'e2e@conductor.invalid')
  git(dir, 'config', 'commit.gpgsign', 'false')
  writeFileSync(join(dir, 'README.md'), '# e2e scratch repository\n')
  git(dir, 'add', 'README.md')
  git(dir, 'commit', '-q', '-m', 'init')
}

/** Resolve stub command placeholders; live-agent configs need no stub shell. */
export function renderConfig(template: string, out: string): void {
  const cfg = JSON.parse(readFileSync(template, 'utf8')) as { catalog?: { agents?: Array<{ command: string[] }> } }
  const agents = cfg.catalog?.agents ?? []
  const bash = agents.some((agent) => agent.command.includes('@BASH@')) ? stubBash().command : undefined
  for (const a of agents) a.command = a.command.map((arg) => (arg === '@STUB@' ? stubPath : arg === '@BASH@' ? bash! : arg))
  // @NODE@ in an entry's env is the node that runs the suite (the stub's chat child, stub-protocol.mjs).
  for (const a of agents as Array<{ env?: Record<string, string> }>)
    for (const [k, v] of Object.entries(a.env ?? {})) if (v === '@NODE@') a.env![k] = process.execPath
  writeFileSync(out, JSON.stringify(cfg, null, 2) + '\n')
}

export interface Started {
  baseURL: string
  token: string
  port: number
  pid: number
  /** The tries before this one, each a port that was not the child's and how that try's child ended. */
  lost?: string[]
}

/** How many ports startServer tries before it gives up: each one another server took is said in the error. */
export const PORT_TRIES = 5
/**
 * The longest one startServer can take: every try it may make, each a health wait and a stop. A spec that starts a server gives
 * that step this bound (a lost port is rare, but it is the case the tries exist for).
 */
export const START_WAIT_MS = PORT_TRIES * (HEALTH_WAIT_MS + STOP_WAIT_MS)

/**
 * Starts `conductor serve` with config and a clean environment: PATH (less the real agent CLIs, agentFreePath), LANG, the HOME given, and the CONDUCTOR_* values that place it
 * (data directory, public URL, allowed root, default working directory, workbench token); of the caller's own variables only the ones
 * passEnv names, never a CONDUCTOR_* one, so a CONDUCTOR_YOLO or a token set in the shell cannot reach it. It returns once the server
 * at the port is the child: /api/health answers and so does an authenticated /api/whoami with the child's own token. A port probed
 * free can be taken before the child binds it (another run of the suite on the machine picks from the same hundred), and then a health
 * answer there is another server's: a refusal of the token, or the child's exit on an address in use, makes it try another port.
 * It kills the server when it does not answer.
 */
export async function startServer(o: {
  config: string
  home: string
  data: string
  allowedRoot: string
  defaultCwd: string
  log: string
  /** Names of the caller's variables to pass on as well, when set (the live check's USER, SHELL, XDG_*). */
  passEnv?: string[]
  /** Variables set last, over the defaults above: a spec's own server, such as one publishing to the spec's switchyard. */
  env?: Record<string, string>
  /** A host token the server accepts (CONDUCTOR_HOST_TOKENS), for a `conductor host` a spec runs against it. */
  hostToken?: string
  /** The first try's port instead of pickPort's (a test's seam: a port another server holds); later tries pick their own. */
  port?: number
  /** A directory put first on the PATH, ahead of any stubBash puts there for its bash (a test's seam: fake agent CLIs). */
  first?: string
  /**
   * The live check's: the PATH as it is, the real agent CLIs on it. Every other server is given agentFreePath's PATH, its links kept
   * under its HOME: it finds the stubs and never a real agent CLI (#961).
   */
  realAgents?: boolean
  /**
   * The desktop app's key for this run, as the app hands its server the secrets (`serve --secrets-stdin`): the workbench token and
   * this key on the first line of stdin, in no environment variable. A server so started lets the app allow a folder while it runs
   * (POST /api/roots with the key in X-Conductor-Desktop-Key), and says so (whoami `folders: app`).
   */
  desktopKey?: string
}): Promise<Started> {
  const lost: string[] = []
  for (let i = 0; i < PORT_TRIES; i++) {
    const port = i === 0 && o.port !== undefined ? o.port : await pickPort()
    const started = await startOn(o, port)
    if (typeof started !== 'string') return { ...started, lost }
    lost.push(started)
  }
  throw new Error(`the server found no port of its own in ${PORT_TRIES} tries: ${lost.join('; ')}`)
}

/** One try of startServer at port: the server, or why the port was not the child's (it is then dead). */
async function startOn(o: Parameters<typeof startServer>[0], port: number): Promise<Started | string> {
  const bin = serverBinary()
  const baseURL = `http://127.0.0.1:${port}`
  const token = randomBytes(24).toString('hex')
  const from = existsSync(o.log) ? statSync(o.log).size : 0
  const out = openSync(o.log, 'a')
  const path = serverPath(o, stubBash().path)
  const child = spawn(bin, ['serve', '--config', o.config, '--listen', `127.0.0.1:${port}`, ...(o.desktopKey ? ['--secrets-stdin'] : [])], {
    env: {
      PATH: path,
      LANG: 'C.UTF-8',
      HOME: o.home,
      CONDUCTOR_DATA_DIR: o.data,
      CONDUCTOR_PUBLIC_URL: baseURL,
      CONDUCTOR_ALLOWED_ROOTS: o.allowedRoot,
      CONDUCTOR_DEFAULT_CWD: o.defaultCwd,
      // With the app's key, the token comes on stdin with it, as the app hands them over; else in its variable.
      ...(o.desktopKey ? {} : { CONDUCTOR_WORKBENCH_TOKEN: token }),
      CONDUCTOR_REACH: 'off',
      // The suite's sessions stay here: never published to the public switchyard.
      CONDUCTOR_RENDEZVOUS: '0',
      // As the desktop app sets it: the Settings picker browses outside the roots.
      CONDUCTOR_PATHS_BROWSE: 'any',
      ...(o.hostToken ? { CONDUCTOR_HOST_TOKENS: o.hostToken } : {}),
      ...Object.fromEntries((o.passEnv ?? []).flatMap((k) => (process.env[k] === undefined || k.startsWith('CONDUCTOR_') ? [] : [[k, process.env[k]!]]))),
      ...(o.env ?? {}),
    },
    stdio: [o.desktopKey ? 'pipe' : 'ignore', out, out],
  })
  if (o.desktopKey) child.stdin?.end(JSON.stringify({ workbenchToken: token, desktopKey: o.desktopKey }) + '\n')
  closeSync(out)
  const exited = new Promise<void>((done) => child.once('exit', () => done()))
  const pid = child.pid
  if (pid === undefined) throw new Error(`${bin} did not start`)
  // A try owns what it starts, and ends it the way the product is ended: SIGTERM, then its exit awaited. The next try starts over the
  // same data directory, which the child may hold mid-start (restore.lock, made by export.Reserve and only then stamped): a SIGKILL
  // there can leave the file empty, which no later serve takes over. Serve catches SIGTERM from before it reserves (app.Main's
  // NotifyContext) and lets the reservation go on every way out of its start, so a child that ended on SIGTERM left the directory as
  // it found it. SIGKILL comes only after STOP_WAIT_MS, for a hang, and said in the try's words.
  const stop = async (): Promise<string> => {
    const ended = () => (child.signalCode !== null ? `on ${child.signalCode}` : `with exit ${child.exitCode}`)
    if (child.exitCode !== null || child.signalCode !== null) return `the child had ended ${ended()}`
    child.kill('SIGTERM')
    let timer: ReturnType<typeof setTimeout> | undefined
    const hung = await Promise.race([exited.then(() => false), new Promise<boolean>((r) => (timer = setTimeout(() => r(true), STOP_WAIT_MS)))])
    clearTimeout(timer)
    if (!hung) return `the child ended on SIGTERM ${ended()}`
    child.kill('SIGKILL')
    await exited
    return `the child did not end within ${STOP_WAIT_MS / 1000} s of SIGTERM and was killed`
  }
  const deadline = Date.now() + HEALTH_WAIT_MS
  for (;;) {
    if (child.exitCode !== null || child.signalCode !== null) {
      // from is a byte offset: the log is cut as bytes, then decoded, so text before it in another script cannot shift the cut.
      const said = readFileSync(o.log).subarray(from).toString('utf8')
      if (/address already in use/.test(said)) return `port ${port} was in use when the server bound it (${await stop()})`
      throw new Error(`the server exited with ${child.exitCode ?? child.signalCode}:\n${tail(o.log)}`)
    }
    let health = false
    try {
      health = (await fetch(`${baseURL}/api/health`)).ok
    } catch {
      /* not listening yet */
    }
    if (health) {
      // Only the child knows its token: any other answer is another server's (401), or its limiter's (429).
      const own = await fetch(`${baseURL}/api/whoami`, { headers: { Authorization: `Bearer ${token}` } }).then((r) => r.status, () => 0)
      if (own === 200) return { baseURL, token, port, pid }
      if (own === 401 || own === 403 || own === 429) {
        return `port ${port} answered with another server's health (its /api/whoami refused the child's token: ${own}; ${await stop()})`
      }
    }
    if (Date.now() > deadline) {
      await stop()
      throw new Error(`the server did not answer /api/health within ${HEALTH_WAIT_MS / 1000} s:\n${tail(o.log)}`)
    }
    await new Promise((r) => setTimeout(r, 200))
  }
}

/** Stops the server: SIGTERM, then SIGKILL after 5 s. By pid, never by name. */
export async function stopServer(pid: number): Promise<void> {
  const alive = () => {
    try {
      process.kill(pid, 0)
      return true
    } catch {
      return false
    }
  }
  if (!alive()) return
  process.kill(pid, 'SIGTERM')
  const deadline = Date.now() + 5_000
  while (alive() && Date.now() < deadline) await new Promise((r) => setTimeout(r, 100))
  if (alive()) process.kill(pid, 'SIGKILL')
}

function tail(file: string): string {
  try {
    return readFileSync(file, 'utf8').split('\n').slice(-30).join('\n')
  } catch {
    return '(no log)'
  }
}
