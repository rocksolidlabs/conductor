import type { ChildProcess } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { existsSync, readFileSync } from 'node:fs'
import { test as base, expect, type Page } from '@playwright/test'
import { git } from './server'
import { readState, type E2EState } from './state'

export { expect, git }

export interface RunMember {
  name: string
  agentId: string
  status: 'pending' | 'starting' | 'running' | 'ended'
  sessionId?: string
  branch?: string
  startedAt?: string
  needsInput?: boolean
  error?: string
}

export interface Run {
  /** GET /api/runs/{run} only: the run's chat, oldest first. */
  chat?: Array<{ id: string; kind: string; text?: string; to?: string }>
  id: string
  crewId: string
  name: string
  state: string
  stoppedAt?: string
  members: RunMember[]
  log: Array<{ at: string; type: string; message?: string }>
  /** The run this one resumed or continued, and the run that resumed or continued this one. */
  resumedFrom?: string
  resumedBy?: string
  /** A record of a run the server no longer holds, read-only. */
  record?: boolean
  /** A managed run (a crew with tasks): its ledger revision, its tasks, whether its dispatch is paused, and its outcome once every task is terminal. */
  managed?: { revision: number }
  paused?: boolean
  /** Why the run paused itself (a project manifest's step that failed, EN1); absent for a person's pause. */
  pausedWhy?: string
  /** The run's trees being ended (their services stopped, their cleanup run), EN1. */
  cleaning?: number
  outcome?: string
  tasks?: RunTask[]
}

export interface RunAttempt {
  n: number
  sessionId?: string
  worktree?: string
  branch?: string
  start?: string
  result?: string
  state: string
  stage?: string
  error?: string
  /** The attempt's worktree's preview URL, from its tree.ready until the attempt's close (EN1). */
  preview?: string
  /** GET /api/runs/{run}/tasks/{task} only: what the attempt submitted. */
  submitted?: { summary?: string; commit?: string; issues?: string[]; verdict?: string; findings?: Array<{ severity: string; path?: string; line?: number; message: string }> }
}

export interface RunTask {
  id: string
  member: string
  kind: string
  needs?: string[]
  state: string
  reason?: string
  attempts: number
  attempt?: RunAttempt
  result?: { attempt: number; commit?: string; summary?: string; issues?: string[]; verdict?: string; findings?: Array<{ severity: string; path?: string; line?: number; message: string }>; at: string }
}

export interface Session {
  id: string
  name: string
  status: string
  command?: string[]
  cwd?: string
  cols?: number
  crew?: { runId: string; crewId: string; member: string }
  /** `id` names the question (a reply to it names it). */
  attention?: { state: string; message?: string; source?: string; kind?: string; options?: Array<{ label: string; input: string }>; id?: string; answered?: string; plan?: string }
  /** Who answered the last prompt, and its words. */
  lastAnswer?: { byName: string; message?: string }
  /** The agent's own session (its conversation), as the server captured or chose it. */
  agentSession?: { id: string; resumable?: boolean; source?: string }
  /** The session this one resumed or relaunched. */
  resumedFrom?: string
}

/**
 * The admin API of a test server: fetch with the token in the Authorization header. (No parameter properties: Node 23+ loads the
 * specs with its own type stripping, which refuses them.)
 */
export class Api {
  readonly server: { baseURL: string; token: string }

  constructor(server: { baseURL: string; token: string }) {
    this.server = server
  }

  async call<T>(method: string, path: string, body?: unknown): Promise<{ status: number; body: T }> {
    const res = await fetch(this.server.baseURL + path, {
      method,
      headers: { Authorization: `Bearer ${this.server.token}`, ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
    const text = await res.text()
    return { status: res.status, body: (text ? JSON.parse(text) : undefined) as T }
  }

  /** call, failing on a status of 300 or more with what the server said. */
  async ok<T>(method: string, path: string, body?: unknown): Promise<T> {
    const r = await this.call<T>(method, path, body)
    if (r.status >= 300) throw new Error(`${method} ${path}: ${r.status} ${JSON.stringify(r.body)}`)
    return r.body
  }

  crew(id: string) {
    return this.ok<{ crew: E2ECrew }>('GET', `/api/crews/${encodeURIComponent(id)}`).then((r) => r.crew)
  }

  launchCrew(id: string) {
    return this.ok<{ run: Run }>('POST', `/api/crews/${encodeURIComponent(id)}/launch`).then((r) => r.run)
  }

  /** A launch as the server answers it, status and body, for a launch the server refuses. */
  launchCrewRaw(id: string) {
    return this.call<{ error: { code: string; message: string } }>('POST', `/api/crews/${encodeURIComponent(id)}/launch`)
  }

  run(id: string) {
    return this.ok<{ run: Run }>('GET', `/api/runs/${encodeURIComponent(id)}`).then((r) => r.run)
  }

  stopRun(id: string) {
    return this.call('POST', `/api/runs/${encodeURIComponent(id)}/stop`)
  }

  session(id: string) {
    return this.ok<{ session: Session }>('GET', `/api/sessions/${encodeURIComponent(id)}`).then((r) => r.session)
  }

  /** Stops a running session (a second call on an ended one removes it). */
  stopSession(id: string) {
    return this.call('DELETE', `/api/sessions/${encodeURIComponent(id)}`)
  }

  /** Resumes (or relaunches) an ended session; the reply is the new session. */
  resumeSession(id: string) {
    return this.call<{ session?: Session; resumed?: boolean; notice?: string; error?: { code: string; message: string } }>('POST', `/api/sessions/${encodeURIComponent(id)}/resume`)
  }

  /** Resumes an ended member of a run. */
  resumeMember(runId: string, name: string) {
    return this.call<{ run?: Run; error?: { code: string; message: string } }>('POST', `/api/runs/${encodeURIComponent(runId)}/members/${encodeURIComponent(name)}/resume`)
  }
}

/**
 * stopStrays ends every session the server still runs that `mine` does not name (#281): one a spec before this one left behind (a
 * spec that failed between making a session and noting it to stop) would otherwise be met by a spec that counts the sessions it
 * sees (the wall's tiles), where the leftover's cause is long gone. The sessions ended are waited for, so that nothing of them is
 * on the wall when the caller looks; their ids are returned for the log.
 */
export async function stopStrays(api: Api, mine: readonly string[]): Promise<string[]> {
  const r = await api.ok<{ sessions?: Session[] } | Session[]>('GET', '/api/sessions')
  const listed = Array.isArray(r) ? r : (r.sessions ?? [])
  const strays = listed.filter((s) => !mine.includes(s.id) && (s.status === 'running' || s.status === 'starting' || s.status === 'pending'))
  for (const s of strays) await api.stopSession(s.id)
  for (const s of strays) {
    await expect.poll(async () => (await api.session(s.id)).status, { timeout: 15_000, message: `the stray session ${s.id} (${s.name}) ends` }).toMatch(/^(exited|stopped|ended)$/)
  }
  return strays.map((s) => s.id)
}

/** Whether a line of the run's log contains text. */
export function logged(run: Run, text: string): boolean {
  return run.log.some((e) => (e.message ?? '').includes(text))
}

/** The member of a run with the given name. */
/** The run's task by id, or a throw. */
export function task(run: Run, id: string): RunTask {
  const t = run.tasks?.find((t) => t.id === id)
  if (!t) throw new Error(`no task ${id} in ${JSON.stringify(run.tasks)}`)
  return t
}

export function member(run: Run, name: string): RunMember {
  const m = run.members.find((x) => x.name === name)
  if (!m) throw new Error(`run ${run.id} has no member ${name}`)
  return m
}

/**
 * Opens the inspector on a rail item (#301) when that item is not already shown: clicking the shown item collapses the panel,
 * which a check that merely wants the view up never means.
 */
export async function showView(page: Page, item: 'people' | 'explorer' | 'changes' | 'commits' | 'activity' | 'chat'): Promise<void> {
  const button = page.locator(`[data-inspector] [data-rail-item="${item}"]`)
  if ((await button.getAttribute('aria-pressed')) !== 'true') await button.click()
}

/** The hrefs of the session and run links of the full sidebar's list, in the order they show. */
export function sidebarLinks(page: Page): Promise<string[]> {
  return page
    .locator('[data-session-list] a[href^="/sessions/"], [data-session-list] a[href^="/runs/"]')
    .evaluateAll((els) => els.map((el) => el.getAttribute('href') ?? ''))
}

/**
 * The id of the session a `conductor host` registered, or '' before it has: it is on the line the host prints once the server has
 * answered (`conductor: hosting session <id> at …`, internal/cli/host.go), read from `log`, which must be this one host's file so
 * that the first such line is its own.
 */
function registeredId(log: string): string {
  if (!existsSync(log)) return ''
  return /conductor: hosting session ([0-9a-f]+) at /.exec(readFileSync(log, 'utf8'))?.[1] ?? ''
}

/**
 * The session a `conductor host` registered (registeredId), read back through the API: it must carry `name`, the name the host was
 * given. A hosted session is found this way, never by its name alone: an earlier run's (a repetition's, its host gone but listed for
 * a minute more) has the same name (#202, #211).
 */
export async function hostedSession(api: Api, log: string, name: string): Promise<Session> {
  let id = ''
  await expect
    .poll(() => {
      id = registeredId(log)
      return id
    }, { timeout: 30_000, message: 'the host is registered' })
    .not.toBe('')
  const session = await api.session(id)
  expect(session.name).toBe(name)
  return session
}

/** A name for a hosted session that this run's host alone is given: `base` and eight random characters. */
export function hostedName(base: string): string {
  return `${base}-${randomUUID().slice(0, 8)}`
}

/**
 * A spec's teardown for its `conductor host`, in place of killing it: every session listed under `name` (this run's own, from
 * hostedName) is ended through the host and removed, then the host is killed, and a session still listed under the name after that
 * fails the teardown, where the leftover begins rather than in whatever spec meets it next. Killed alone, the host leaves its session
 * listed for a minute and more (signal.DisconnectGrace, then the sweep), where a later spec or repetition finds it (#211, #218). The
 * session is found by the name, not by the id on the host's banner, which a host that failed between registering and printing it
 * never printed. Per session, DELETE answers 200: the host was told to stop its command, and the session ends, then is removed; 204:
 * it had ended already, and is removed; 404: gone; anything else (a 500: the host left on its own) fails. The ending is bounded by
 * `ms`, and the check after the kill by 15 s more, so that a server that stops answering cannot hold the hook past its own deadline;
 * the host is killed whatever became of the ending.
 */
export async function endHosted(api: Api, name: string, host: ChildProcess | null, ms = 45_000): Promise<void> {
  try {
    if (name) {
      await within(ms, `ending the hosted session ${name}`, async () => {
        for (const { id } of await listedAs(api, name)) {
          const r = await api.stopSession(id)
          if (r.status === 200) {
            await expect.poll(async () => (await api.session(id)).status, { timeout: 15_000, message: `the hosted session ${id} ends` }).toMatch(/^(exited|stopped)$/)
            await api.ok('DELETE', `/api/sessions/${encodeURIComponent(id)}`)
          } else if (r.status !== 204 && r.status !== 404) {
            throw new Error(`the hosted session ${id} could not be ended: ${r.status} ${JSON.stringify(r.body)}`)
          }
        }
      })
    }
  } finally {
    host?.kill('SIGKILL')
  }
  if (!name) return
  const left = await within(15_000, `listing ${name} once its host was killed`, () => listedAs(api, name))
  if (left.length) throw new Error(`${name} is still listed after its host was killed: ${left.map((s) => `${s.id} (${s.status})`).join(', ')}`)
}

/** The sessions the server lists under `name`, whatever their status. */
async function listedAs(api: Api, name: string): Promise<Session[]> {
  const r = await api.ok<{ sessions?: Session[] } | Session[]>('GET', '/api/sessions')
  return (Array.isArray(r) ? r : (r.sessions ?? [])).filter((s) => s.name === name)
}

/** What `f` returns, or a failure naming `what` once `ms` have passed without it. */
async function within<T>(ms: number, what: string, f: () => Promise<T>): Promise<T> {
  let timer: NodeJS.Timeout | undefined
  const late = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new Error(`${what}: no answer in ${ms / 1000} s`)), ms)
  })
  try {
    return await Promise.race([f(), late])
  } finally {
    clearTimeout(timer)
  }
}

/** A saved crew as the e2e reads it back: its members' starts, and, in a crew with tasks (#334), its tasks and setup. */
export interface E2ECrew {
  id: string
  cwd: string
  members: Array<{ name: string; start?: { when: string; member?: string } }>
  tasks?: Array<{ id: string; member: string; kind: string; instructions: string; needs?: string[]; checks?: Array<{ id: string; argv: string[]; timeoutSeconds?: number }> }>
  setup?: Array<{ id: string; argv: string[]; timeoutSeconds?: number }>
  /** The crew's project manifest (EN1), as the server keeps it; its count of changes beside it. */
  manifest?: Record<string, unknown>
  manifestRevision?: number
}

export const test = base.extend<{ api: Api }, { state: E2EState }>({
  state: [
    // eslint-disable-next-line no-empty-pattern
    async ({}, use) => {
      await use(readState())
    },
    { scope: 'worker' },
  ],
  api: async ({ state }, use) => {
    await use(new Api(state))
  },
  baseURL: async ({ state }, use) => {
    await use(state.baseURL)
  },
  // Every page starts with the workbench token where the workbench keeps it (useWorkbenchToken: localStorage conductor.workbenchToken).
  page: async ({ page, state }, use) => {
    await page.addInitScript((token) => {
      try {
        localStorage.setItem('conductor.workbenchToken', token)
      } catch {
        /* storage refused: the page asks for the token, and the test fails on what it cannot find */
      }
    }, state.token)
    await use(page)
  },
})
