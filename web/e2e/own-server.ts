import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { Api } from './fixtures'
import { renderConfig, scratchRepo, startServer, stopServer, type Started } from './server'

// A server of a spec's own (decisionServer's body, moved here and named by UX1's PR-1, docs/plans/ux-review.md): its own HOME, data
// directory and scratch repository under one temporary root, the e2e catalog with copies of its agents added, its log copied into
// test-results when it stops. The seeded day (day.ts) runs on one; decisions-helpers.ts's decisionServer is a call of it.

/** A copy of the catalog's agent `from` under the id `id`, with `env` over the copied agent's own and, when given, a prompt shape. */
export interface AgentCopy {
  id: string
  from: string
  env: Record<string, string>
  /** The copy's prompt shape (the catalog's promptShape): what the server recognises as the agent's own prompt. */
  promptShape?: string
  /** The copy's protocol (the catalog's protocol): the agent's own interface a session of it moves to as a chat. */
  protocol?: { name: string }
  /** The copy's name, as a person's catalog names it (`Claude Code`); `<the copied agent's name>, <id>` when not given. */
  name?: string
}

export interface OwnServerOptions {
  /** Copies of the catalog's agents, added to the e2e catalog. */
  agents?: AgentCopy[]
  /**
   * The ids of the e2e catalog's own agents to keep, the rest left out (the copies are made first, from the whole catalog): a dialog
   * measured with the half dozen agents a person has, not the e2e catalog's sixteen. Every one when not given.
   */
  keep?: string[]
  /**
   * Where the scratch repository is, under the server's root: `repo` when not given. The day gives a nested path about as long as a
   * real checkout's, so that a cut in a meta line is measured against a real length.
   */
  repoPath?: string
  /** Variables set over the server's own (startServer's `env`), such as CONDUCTOR_EXITED_RETENTION. */
  env?: Record<string, string>
  /** A host token the server accepts (CONDUCTOR_HOST_TOKENS), so that a spec can bring in a hosted session. */
  hostToken?: string
}

export interface OwnServer {
  started: Started
  api: Api
  /** The temporary root: HOME, data, the repository and the log are under it, and it is the server's only allowed root. */
  root: string
  home: string
  repo: string
  log: string
  /** The host token the server accepts, when one was given. */
  hostToken?: string
  /**
   * Ends the server at once, as a crash or a kill does (nothing of its state written on the way out: tasks-resume.spec.ts's pattern),
   * and starts another on the same configuration, HOME and data directory; `started` and `api` are the new server's from then on (a
   * new port and workbench token).
   */
  restart: () => Promise<void>
  /** Stops the server (SIGTERM, SIGKILL after 5 s: stopServer), copies its log to test-results/<name>-server.log, removes the root. */
  stop: () => Promise<void>
}

/** Ends the process at once (SIGKILL) and waits for it to be gone (reaped: it is this worker's child). By pid, never by name. */
async function crash(pid: number): Promise<void> {
  const alive = () => {
    try {
      process.kill(pid, 0)
      return true
    } catch {
      return false
    }
  }
  process.kill(pid, 'SIGKILL')
  while (alive()) await new Promise((r) => setTimeout(r, 50))
}

/**
 * Starts a server of the spec's own, over a scratch repository, with the e2e catalog and `agents` added to it. It returns once the
 * server answers (startServer: up to START_WAIT_MS); `name` names its temporary root and the log copied at `stop`.
 */
export async function ownServer(name: string, o: OwnServerOptions = {}): Promise<OwnServer> {
  // Resolved: the server reports paths with their links resolved, and on macOS the temporary directory is under a link.
  const root = realpathSync(mkdtempSync(join(tmpdir(), `conductor-e2e-${name}-`)))
  const log = join(root, 'server.log')
  // The log kept in test-results and the root removed: at stop, and when a step of the start fails (startServer ends each child it
  // started before it throws), so that a start that fails leaves nothing behind.
  const remove = () => {
    mkdirSync('test-results', { recursive: true })
    if (existsSync(log)) copyFileSync(log, `test-results/${name}-server.log`)
    rmSync(root, { recursive: true, force: true })
  }
  try {
    return await startOwn(o, root, log, remove)
  } catch (e) {
    remove()
    throw e
  }
}

async function startOwn(o: OwnServerOptions, root: string, log: string, remove: () => void): Promise<OwnServer> {
  const here = dirname(fileURLToPath(import.meta.url))
  const home = join(root, 'home')
  const data = join(root, 'data')
  const repo = join(root, o.repoPath ?? 'repo')
  for (const d of [home, data, repo]) mkdirSync(d, { recursive: true })
  scratchRepo(repo)
  const config = join(root, 'conductor.json')
  renderConfig(join(here, 'conductor.e2e.json'), config)
  const cfg = JSON.parse(readFileSync(config, 'utf8')) as { catalog: { agents: Array<{ id: string; name: string; env?: Record<string, string> }> } }
  for (const a of o.agents ?? []) {
    const base = cfg.catalog.agents.find((x) => x.id === a.from)
    if (!base) throw new Error(`the e2e catalog has no agent ${a.from} to copy as ${a.id}`)
    cfg.catalog.agents.push({ ...JSON.parse(JSON.stringify(base)), id: a.id, name: a.name ?? `${base.name}, ${a.id}`, env: { ...base.env, ...a.env }, ...(a.promptShape ? { promptShape: a.promptShape } : {}), ...(a.protocol ? { protocol: a.protocol } : {}) })
  }
  if (o.keep) {
    const copies = new Set((o.agents ?? []).map((a) => a.id))
    cfg.catalog.agents = cfg.catalog.agents.filter((x) => copies.has(x.id) || o.keep!.includes(x.id))
  }
  writeFileSync(config, JSON.stringify(cfg, null, 2) + '\n')
  const start = () => startServer({ config, home, data, allowedRoot: root, defaultCwd: repo, log, env: o.env, hostToken: o.hostToken })
  const started = await start()
  const srv: OwnServer = {
    started,
    api: new Api(started),
    root,
    home,
    repo,
    log,
    hostToken: o.hostToken,
    restart: async () => {
      await crash(srv.started.pid)
      srv.started = await start()
      srv.api = new Api(srv.started)
    },
    stop: async () => {
      await stopServer(srv.started.pid)
      remove()
    },
  }
  return srv
}
