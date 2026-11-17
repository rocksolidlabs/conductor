import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { Page } from '@playwright/test'
import { Api, expect } from './fixtures'
import { renderConfig, scratchRepo, startServer, stopServer, type Started } from './server'

// A server of a Brakeman spec's own (the seeded day holds no Brakeman records): its own HOME, data directory and scratch repository
// under one temporary root, the e2e catalog's `brakeman-stub` agent given the spec's gate and brief files, Brakeman turned on with
// the stub as its agent (unless the spec asks it off), and its log copied into test-results when it stops. The stub reads its
// request and what changed (one counted read), then waits for the gate's file before it submits: a spec holds a turn running for as
// long as it looks at it, and opens the gate to let the brief in. Factored from brakeman.spec.ts's own server, for every Brakeman
// spec of the page and its doors.

export interface BrakemanServerOptions {
  /** Names the temporary root and the log kept in test-results (`<name>-server.log`). */
  name: string
  /** Over the stub's own variables (STUB_BRAKEMAN_ASK, STUB_BRAKEMAN_TICKET, STUB_BRAKEMAN_CHECK, …). */
  env?: Record<string, string>
  /** Brakeman on with the stub as its agent (the default), or left as a fresh server has it: off. */
  enabled?: boolean
  /** Fields of the configuration over the server's defaults, saved with Brakeman on (the wake mode, the budget, …). */
  config?: Record<string, unknown>
  /** Variables set over the server's own (startServer's `env`). */
  serverEnv?: Record<string, string>
}

/** A Brakeman request as the API answers it, with the fields a spec reads. */
export interface BrakemanRequestSeen {
  id: string
  kind: string
  state: string
  subjects?: string[]
  runId?: string
  sessionId?: string
  hasBrief?: boolean
  reads?: number
  createdAt: string
  briefAt?: string
}

export interface BrakemanServer {
  started: Started
  api: Api
  /** The temporary root: HOME, data, the repository, the gate and the log are under it, and it is the server's only allowed root. */
  root: string
  home: string
  repo: string
  log: string
  /** The stub's gate: it submits its brief once this file exists. */
  gate: string
  /** The brief the stub submits when the file holds one (setBrief); its own stub brief while it is empty. */
  briefFile: string
  /** Lets every turn at the gate, and every later one, submit its brief. */
  openGate: () => void
  /** Holds the next turns at the gate again (a turn past it already goes on). */
  shutGate: () => void
  /** The brief the stub submits from now on (`brakeman.brief/1`, as JSON), or null for the stub's own. */
  setBrief: (brief: unknown) => void
  /** The page on this server at `path`, with its workbench token (and a display name, when given) set before the first load. */
  open: (page: Page, path: string, o?: { name?: string }) => Promise<void>
  /** A request as the server holds it now. */
  request: (id: string) => Promise<BrakemanRequestSeen>
  /** Asks Brakeman through the API, as a person would: the request kept. */
  ask: (body: { kind: string; subjects?: string[]; note?: string; since?: string; by?: string }) => Promise<BrakemanRequestSeen>
  /** Waits until the request is in one of `states`, read through the API (a state that holds, never one passed through), and returns it. */
  until: (id: string, states: readonly string[], timeout?: number) => Promise<BrakemanRequestSeen>
  /** Stops the server (its runs first), copies its log to test-results, and removes the root. */
  stop: () => Promise<void>
}

/** An id for a write's opId, unique in the spec's process. */
let ops = 0
const opId = (what: string) => `e2e-${what}-${process.pid}-${++ops}`

/** Starts a Brakeman spec's own server (brakemanServer's options); it returns once the server answers and Brakeman is configured. */
export async function brakemanServer(o: BrakemanServerOptions): Promise<BrakemanServer> {
  const here = dirname(fileURLToPath(import.meta.url))
  // A root of its own each time the spec runs (a repeat starts again from no records, so its requests are br-1 on), outside the
  // suite's scratch root: a folder there is one more entry in the listings other specs' pickers read (settings.spec.ts). Resolved: the
  // server reports paths with their links resolved, and on macOS the temporary directory is under a link.
  const root = realpathSync(mkdtempSync(join(tmpdir(), `conductor-e2e-${o.name}-`)))
  const home = join(root, 'home')
  const data = join(root, 'data')
  const repo = join(root, 'repo')
  const log = join(root, 'server.log')
  const gate = join(root, 'gate')
  const briefFile = join(root, 'brief.json')
  // The log kept and the root removed at stop, and when a step of the start fails, so that a start that fails leaves nothing behind.
  const remove = () => {
    mkdirSync('test-results', { recursive: true })
    if (existsSync(log)) copyFileSync(log, `test-results/${o.name}-server.log`)
    rmSync(root, { recursive: true, force: true })
  }
  let started: Started | undefined
  try {
    for (const d of [home, data, repo]) mkdirSync(d, { recursive: true })
    scratchRepo(repo)
    writeFileSync(briefFile, '')
    const config = join(root, 'conductor.json')
    renderConfig(join(here, 'conductor.e2e.json'), config)
    const cfg = JSON.parse(readFileSync(config, 'utf8')) as { catalog: { agents: Array<{ id: string; env?: Record<string, string> }> } }
    const stub = cfg.catalog.agents.find((a) => a.id === 'brakeman-stub')
    if (!stub) throw new Error('the e2e catalog has no brakeman-stub agent')
    stub.env = { ...stub.env, STUB_BRAKEMAN_WAIT: gate, STUB_BRAKEMAN_BRIEF: briefFile, ...o.env }
    writeFileSync(config, JSON.stringify(cfg, null, 2) + '\n')
    started = await startServer({ config, home, data, allowedRoot: root, defaultCwd: repo, log, env: o.serverEnv })
    const api = new Api(started)
    if (o.enabled !== false) {
      const { config: c } = await api.ok<{ config: Record<string, unknown> & { rev: number } }>('GET', '/api/brakeman')
      await api.ok('PUT', '/api/brakeman/config', { rev: c.rev, config: { ...c, ...o.config, enabled: true, agentId: 'brakeman-stub' } })
    }
    const request = (id: string) => api.ok<{ request: BrakemanRequestSeen }>('GET', `/api/brakeman/requests/${encodeURIComponent(id)}`).then((r) => r.request)
    const srv: BrakemanServer = {
      started,
      api,
      root,
      home,
      repo,
      log,
      gate,
      briefFile,
      openGate: () => writeFileSync(gate, ''),
      shutGate: () => rmSync(gate, { force: true }),
      setBrief: (brief) => writeFileSync(briefFile, brief === null ? '' : JSON.stringify(brief)),
      open: async (page, path, opts = {}) => {
        await page.addInitScript(
          ({ token, name }) => {
            localStorage.setItem('conductor.workbenchToken', token)
            if (name) localStorage.setItem('conductor.displayName', name)
          },
          { token: started!.token, name: opts.name ?? '' },
        )
        await page.goto(`${started!.baseURL}${path}`)
      },
      request,
      ask: (body) => api.ok<{ request: BrakemanRequestSeen }>('POST', '/api/brakeman/requests', { opId: opId('ask'), ...body }).then((r) => r.request),
      until: async (id, states, timeout = 60_000) => {
        await expect.poll(async () => (await request(id)).state, { timeout }).toMatch(new RegExp(`^(${states.join('|')})$`))
        return request(id)
      },
      stop: async () => {
        // The turns' runs end first, so nothing of theirs writes into the root as it goes.
        const { requests } = await api.ok<{ requests: BrakemanRequestSeen[] }>('GET', '/api/brakeman/requests').catch(() => ({ requests: [] as BrakemanRequestSeen[] }))
        for (const r of requests) if (r.runId) await api.stopRun(r.runId).catch(() => undefined)
        await stopServer(started!.pid)
        remove()
      },
    }
    return srv
  } catch (e) {
    if (started) await stopServer(started.pid)
    remove()
    throw e
  }
}

/** Presses lowercase letters in turn as a person does, each handed to the page once it has taken the one before (decision-page.spec.ts). */
export async function pressInTurn(page: Page, ...keys: string[]) {
  const cdp = await page.context().newCDPSession(page)
  try {
    const sends: Array<Promise<unknown>> = []
    for (const key of keys) {
      const code = `Key${key.toUpperCase()}`
      const windowsVirtualKeyCode = key.toUpperCase().charCodeAt(0)
      sends.push(cdp.send('Input.dispatchKeyEvent', { type: 'keyDown', key, code, text: key, unmodifiedText: key, windowsVirtualKeyCode }))
      sends.push(cdp.send('Input.dispatchKeyEvent', { type: 'keyUp', key, code, windowsVirtualKeyCode }))
    }
    await Promise.all(sends)
  } finally {
    await cdp.detach()
  }
}
