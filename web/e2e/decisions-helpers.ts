import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { Browser, Page } from '@playwright/test'
import { Api, expect, test, type Run } from './fixtures'
import { renderConfig, scratchRepo, startServer, stopServer, type Started } from './server'

// What the durable decisions' specs share (Program item DR1, docs/plans/decisions.md, web/e2e): a server of their own (as the Brakeman
// specs have, brakeman-cards.spec.ts), so that its decisions are the spec's alone (dr-1 its first) and its stub agents can be given
// what they ask (STUB_DECISION); the launch from a ticket's approved plan revision; the record read through the API.

/** A decision as the operator's routes give it, what the specs read of it. */
export interface DecisionRecord {
  id: string
  run: string
  origin: string
  type: string
  gate?: string
  asker?: { task: string; attempt: number; live: boolean }
  state: string
  rev: number
  answer?: string
  applied?: { how: string; words: string; tasks?: string[] }
  history?: Array<{ kind: string }>
}

/** Raises the running test's timeout by `ms`, as a step is about to make waits of those caps (AGENTS.md, Tests that do not flake). */
export function budget(ms: number) {
  const info = test.info()
  info.setTimeout(info.timeout + ms)
}

export interface DecisionServer {
  started: Started
  api: Api
  root: string
  repo: string
  /**
   * Ends the server at once, as a crash or a kill does (nothing of its state written on the way out: tasks-resume.spec.ts's pattern),
   * and starts another on the same configuration, HOME and data directory; `started` and `api` are the new server's from then on (a
   * new port and workbench token).
   */
  restart: () => Promise<void>
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
 * Starts a server of the spec's own, over a scratch repository, with the e2e catalog and `agents` added to it (each a copy of the
 * catalog's agent `from` under a new id, with `env` over its own). Its log is copied into test-results as `<name>-server.log` at stop.
 */
export async function decisionServer(name: string, agents: Array<{ id: string; from: string; env: Record<string, string> }> = []): Promise<DecisionServer> {
  const here = dirname(fileURLToPath(import.meta.url))
  const root = mkdtempSync(join(tmpdir(), `conductor-e2e-${name}-`))
  const home = join(root, 'home')
  const data = join(root, 'data')
  const repo = join(root, 'repo')
  for (const d of [home, data, repo]) mkdirSync(d, { recursive: true })
  scratchRepo(repo)
  const config = join(root, 'conductor.json')
  renderConfig(join(here, 'conductor.e2e.json'), config)
  const cfg = JSON.parse(readFileSync(config, 'utf8')) as { catalog: { agents: Array<{ id: string; name: string; env?: Record<string, string> }> } }
  for (const a of agents) {
    const base = cfg.catalog.agents.find((x) => x.id === a.from)!
    cfg.catalog.agents.push({ ...JSON.parse(JSON.stringify(base)), id: a.id, name: `${base.name}, ${a.id}`, env: { ...base.env, ...a.env } })
  }
  writeFileSync(config, JSON.stringify(cfg, null, 2) + '\n')
  const log = join(root, 'server.log')
  const start = () => startServer({ config, home, data, allowedRoot: root, defaultCwd: repo, log })
  const started = await start()
  const srv: DecisionServer = {
    started,
    api: new Api(started),
    root,
    repo,
    restart: async () => {
      await crash(srv.started.pid)
      srv.started = await start()
      srv.api = new Api(srv.started)
    },
    stop: async () => {
      await stopServer(srv.started.pid)
      mkdirSync('test-results', { recursive: true })
      copyFileSync(log, `test-results/${name}-server.log`)
      rmSync(root, { recursive: true, force: true })
    },
  }
  return srv
}

/** A page of the spec's server, as the operator: its workbench token where the workbench keeps it, and a display name. */
export async function operatorPage(browser: Browser, s: Started, name = 'Nate'): Promise<Page> {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  await ctx.addInitScript(
    ({ t, n }) => {
      localStorage.setItem('conductor.workbenchToken', t)
      localStorage.setItem('conductor.displayName', n)
    },
    { t: s.token, n: name },
  )
  return ctx.newPage()
}

/** A project and its ticket 1, through Dispatch's routes; the ticket's key. */
export async function makeTicket(api: Api, project: string, title: string): Promise<string> {
  await api.ok('POST', '/api/dispatch/projects', { opId: `${project.toLowerCase()}-p`, key: project, name: `E2E ${project}` })
  const r = await api.ok<{ ticket: { key: string } }>('POST', '/api/dispatch/tickets', { opId: `${project.toLowerCase()}-t1`, project, title })
  return r.ticket.key
}

/** Revision 1 of the ticket proposed from the crew (with the plan's `decisions`, when given) through the propose route; its id. */
export async function propose(api: Api, ticket: string, crew: string, decisions?: Array<{ id: string; question: string; kind: string; before: string[] }>): Promise<string> {
  const r = await api.ok<{ plan: { id: string } }>('POST', `/api/dispatch/tickets/${ticket}/plans`, { opId: `${ticket.toLowerCase()}-r1`, crew, ...(decisions ? { decisions } : {}) })
  return r.plan.id
}

/** The decisions of the run that holds them, through the operator's list. */
export async function decisionsOf(api: Api, run: string): Promise<DecisionRecord[]> {
  return (await api.ok<{ decisions: DecisionRecord[] }>('GET', `/api/decisions?run=${encodeURIComponent(run)}`)).decisions
}

/** One decision whole. */
export async function decision(api: Api, id: string): Promise<DecisionRecord> {
  return (await api.ok<{ decision: DecisionRecord }>('GET', `/api/decisions/${id}`)).decision
}

/** The run read until `done` holds of it, within `timeout`, which the test's timeout is raised by. */
export async function runUntil(api: Api, id: string, done: (r: Run) => boolean, message: string, timeout = 120_000): Promise<Run> {
  budget(timeout)
  let last = await api.run(id)
  await expect.poll(async () => done((last = await api.run(id))), { timeout, intervals: [500], message }).toBe(true)
  return last
}

/** The decision read until it is in `state`, within `timeout`. */
export async function decisionUntil(api: Api, id: string, state: string, timeout = 60_000): Promise<DecisionRecord> {
  budget(timeout)
  let last = await decision(api, id)
  await expect.poll(async () => (last = await decision(api, id)).state, { timeout, intervals: [250], message: `${id} is ${state}` }).toBe(state)
  return last
}
