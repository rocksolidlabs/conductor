import { execFileSync, spawn } from 'node:child_process'
import { existsSync, openSync, readFileSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:net'
import { homedir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { randomBytes } from 'node:crypto'
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
  writeFileSync(out, JSON.stringify(cfg, null, 2) + '\n')
}

export interface Started {
  baseURL: string
  token: string
  port: number
  pid: number
}

/**
 * Starts `conductor serve` with config and a clean environment: PATH, LANG, the HOME given, and the CONDUCTOR_* values that place it
 * (data directory, public URL, allowed root, default working directory, workbench token); of the caller's own variables only the ones
 * passEnv names, never a CONDUCTOR_* one, so a CONDUCTOR_YOLO or a token set in the shell cannot reach it. It returns once /api/health answers, and kills the server when it does not.
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
}): Promise<Started> {
  const bin = serverBinary()
  const port = await pickPort()
  const baseURL = `http://127.0.0.1:${port}`
  const token = randomBytes(24).toString('hex')
  const out = openSync(o.log, 'a')
  const bash = stubBash()
  const child = spawn(bin, ['serve', '--config', o.config, '--listen', `127.0.0.1:${port}`], {
    env: {
      PATH: bash.path,
      LANG: 'C.UTF-8',
      HOME: o.home,
      CONDUCTOR_DATA_DIR: o.data,
      CONDUCTOR_PUBLIC_URL: baseURL,
      CONDUCTOR_ALLOWED_ROOTS: o.allowedRoot,
      CONDUCTOR_DEFAULT_CWD: o.defaultCwd,
      CONDUCTOR_WORKBENCH_TOKEN: token,
      CONDUCTOR_REACH: 'off',
      // The suite's sessions stay here: never published to the public switchyard.
      CONDUCTOR_RENDEZVOUS: '0',
      // As the desktop app sets it: the Settings picker browses outside the roots.
      CONDUCTOR_PATHS_BROWSE: 'any',
      ...(o.hostToken ? { CONDUCTOR_HOST_TOKENS: o.hostToken } : {}),
      ...Object.fromEntries((o.passEnv ?? []).flatMap((k) => (process.env[k] === undefined || k.startsWith('CONDUCTOR_') ? [] : [[k, process.env[k]!]]))),
      ...(o.env ?? {}),
    },
    stdio: ['ignore', out, out],
  })
  const pid = child.pid
  if (pid === undefined) throw new Error(`${bin} did not start`)
  const deadline = Date.now() + HEALTH_WAIT_MS
  for (;;) {
    if (child.exitCode !== null) throw new Error(`the server exited with ${child.exitCode}:\n${tail(o.log)}`)
    try {
      const res = await fetch(`${baseURL}/api/health`)
      if (res.ok) return { baseURL, token, port, pid }
    } catch {
      /* not listening yet */
    }
    if (Date.now() > deadline) {
      child.kill('SIGKILL')
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
