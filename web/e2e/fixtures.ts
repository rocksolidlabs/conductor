import type { ChildProcess } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { existsSync, readFileSync } from 'node:fs'
import { test as base, expect, type Browser, type BrowserContext, type Locator, type Page } from '@playwright/test'
import { startDay, type Day } from './day'
import { git, START_WAIT_MS } from './server'
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
  /** The login it was bound to at the launch, by its id and source (docs/protocol.md, Capacity), and its model. */
  account?: { id: string; source: string }
  model?: string
}

export interface Run {
  /** GET /api/runs/{run} only: the run's chat, oldest first. */
  chat?: Array<{ id: string; kind: string; text?: string; to?: string }>
  id: string
  crewId: string
  name: string
  /** The label it was launched with, if any. */
  label?: string
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
  /** The crew's people as launched (HM1): never started; absent for a crew without. */
  people?: Array<{ name: string; responsibilities?: string[]; note?: string }>
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
  /** GET /api/runs/{run}/tasks/{task} only: the context it was given, block by block, never the text (CT-3). */
  context?: Array<{ seq: number; phase: 'task' | 'lookup'; source: string; title: string; ref?: string; pack?: string; sha256: string; bytes: number; at: string }>
}

export interface RunTask {
  id: string
  member: string
  kind: string
  needs?: string[]
  state: string
  reason?: string
  /** A blocked task that will never start, as the server says it (`blockedForGood`); absent otherwise. */
  blockedForGood?: boolean
  attempts: number
  attempt?: RunAttempt
  result?: { attempt: number; commit?: string; summary?: string; issues?: string[]; verdict?: string; findings?: Array<{ severity: string; path?: string; line?: number; message: string }>; at: string }
  /** The person accountable for its outcome as launched, and how (`task`, or `acceptance` for the crew's acceptance owner); absent for no one. */
  owner?: string
  ownerFrom?: string
  /** The decisions that hold it (a task waiting for a decision). */
  holds?: Array<{ id: string; type: string; state: string; question: string }>
}

export interface Session {
  id: string
  name: string
  status: string
  /** The catalog agent it runs, and its exit code once it has exited. */
  agentId?: string
  exitCode?: number
  command?: string[]
  cwd?: string
  cols?: number
  /** How many viewers are attached. */
  viewers?: number
  crew?: { runId: string; crewId: string; member: string }
  /** `id` names the question (a reply to it names it). */
  attention?: { state: string; message?: string; source?: string; kind?: string; options?: Array<{ label: string; input: string }>; id?: string; answered?: string; plan?: string; decision?: string }
  /** Who answered the last prompt, and its words. */
  lastAnswer?: { byName: string; message?: string }
  /** The agent's own session (its conversation), as the server captured or chose it. */
  agentSession?: { id: string; resumable?: boolean; source?: string }
  /** The session this one resumed or relaunched. */
  resumedFrom?: string
  /** Launched with its agent's yolo recipe applied. */
  yolo?: boolean
  /** Its agent reports its turns (a working or a done of its own); missing when it has not. */
  turns?: boolean
  /** The agent's first reply in this session has ended (its terminal reported a turn's end): what a move to Chat waits for. */
  firstTurn?: boolean
  /** `protocol` while it runs as a chat (the agent's own protocol); missing in its terminal. */
  way?: string
  /** A chat's messages reach its agent as written (the listing's `verbatim`). */
  verbatim?: boolean
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

  /**
   * Puts the app's screens' state back as a fresh server holds it (docs/plans/sync-seams.md, OS-2): an empty layout, no recents, no
   * pins. The server keeps them for every browser, so what one test left would be the next test's start; every test begins here
   * (uiStateFresh). A server without the routes answers 404, and nothing is to be put back.
   */
  async freshUiState(): Promise<void> {
    for (const [name, doc] of [
      ['layout', { v: 1 }],
      ['palette', { v: 1, recents: {} }],
      ['pins', { v: 1, pins: [] }],
    ] as const) {
      const r = await this.call('PUT', `/api/ui/${name}`, doc)
      if (r.status >= 300 && r.status !== 404) throw new Error(`PUT /api/ui/${name}: ${r.status} ${JSON.stringify(r.body)}`)
    }
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
 * The two keys of a chord (G then H), handed to the page in one task. Two awaited presses are two round trips to the browser, and
 * on a loaded machine the second lands after the chord's own wait for it has lapsed (the product's 800 ms, right for a person's
 * hands; the presses of a failing run were 1.6 s apart): the page then stays where it was with nothing wrong. Typing both in one
 * call still takes a round trip per key, so the two keydowns are dispatched here, on what holds the focus, one behind the other.
 */
export async function pressChord(page: Page, first: string, second: string): Promise<void> {
  await page.evaluate(
    ([a, b]) => {
      const at = document.activeElement ?? document.body
      for (const key of [a, b]) at.dispatchEvent(new KeyboardEvent('keydown', { key, code: `Key${key.toUpperCase()}`, bubbles: true, cancelable: true }))
    },
    [first, second] as const,
  )
}

/**
 * Opens the inspector on a rail item (#301) when that item is not already shown: clicking the shown item collapses the panel,
 * which a check that merely wants the view up never means.
 */
export async function showView(page: Page, item: 'people' | 'explorer' | 'changes' | 'commits' | 'activity' | 'chat'): Promise<void> {
  // The people's chat is People's (docs/plans/chat-or-terminal.md, design 11): one rail item holds both.
  const button = page.locator(`[data-inspector] [data-rail-item="${item === 'chat' ? 'people' : item}"]`)
  if ((await button.getAttribute('aria-pressed')) !== 'true') await button.click()
}

/**
 * Where a spec answers a row's question (the UX review's P5): the one way to reach a row's choices, reply field or decision words.
 * The list says how wide it is once it has measured itself (`data-list-width`): in a narrow sidebar (below 280 px) the row shows two
 * lines of its question (`data-row-clamped`) and its controls are in the panel beside it, which this opens by focusing the row's link
 * (a tap on its Answer button on a touch screen) and returns once the panel for that row (`data-panel-for`) shows; otherwise, and on
 * the phone's list, it returns the row. `row` is one row (`[data-sidebar-row="s:<id>"]`), asking.
 */
export async function rowAnswer(page: Page, row: Locator): Promise<Locator> {
  const r = row.first()
  await expect(r.locator('[data-row-prompt]')).toBeVisible()
  const list = r.locator('xpath=ancestor::*[@data-session-list][1]')
  await expect(list).toHaveAttribute('data-list-width', /^(narrow|wide)$/)
  if ((await list.getAttribute('data-list-width')) !== 'narrow') return r
  const id = await r.getAttribute('data-sidebar-row')
  if (await page.evaluate(() => matchMedia('(pointer: coarse)').matches)) await r.locator('[data-row-answer-open]').tap()
  else await r.locator('a[href]').first().focus()
  const panel = page.locator(`[data-row-panel][data-panel-for="${id}"]`)
  await expect(panel).toBeVisible()
  // The panel scales in as it opens (the popover's 100 ms): it is returned at rest, its opening animations ended, so that a spec
  // that measures it does not measure a panel still moving.
  await atRest(panel)
  return panel
}

/**
 * Waits until nothing moves what `l` draws: no running animation or transition on it, on an element it lies in (a popover, a
 * tooltip or a dialog that scales in as it opens) or on one inside it, each awaited by its own `finished`, again until none is
 * left. An endless one is never waited for. A box or a text measured after it is the last frame's, not one passed through
 * (AGENTS.md, "Measure a thing at rest"): call it between a thing's appearing and its measure.
 */
export async function atRest(l: Locator): Promise<void> {
  await l.evaluate(async (el) => {
    for (;;) {
      const moving = document.getAnimations().filter((a) => {
        const t = (a.effect as KeyframeEffect | null)?.target
        return !!t && (t.contains(el) || el.contains(t)) && a.playState === 'running' && Number.isFinite(Number(a.effect?.getComputedTiming().endTime))
      })
      if (!moving.length) return
      await Promise.all(moving.map((a) => a.finished.catch(() => undefined)))
    }
  })
}

/**
 * Opens a page from the full sidebar's More tile (the pages that have no tile of their own: SidebarTiles.vue) by clicking it and the
 * page's item, and waits for the menu to be gone, so that nothing typed next lands in it.
 */
export async function viaMore(page: Page, name: string): Promise<void> {
  await page.locator('#dashboard-sidebar-main [data-sidebar-tile="more"]').click()
  await page.getByRole('menuitem', { name: new RegExp(`^${name}`) }).click()
  await expect(page.getByRole('menu')).toHaveCount(0)
}

/**
 * The address of the page of what needs you with `ref` the open item, as the router spells it (it may leave `:` and `/` unencoded).
 * On a wide window the page opens its oldest item by itself through the address (UX1 PR-21), so a spec that means "the page is open"
 * reads the path (`pagePath`), and one that means "this item is open" reads this.
 */
export const itemURL = (ref: string) => new RegExp(`/decisions\\?item=${ref.replace(/[.*+?^${}()|[\]\\]/g, '\\$&').replace(/:/g, '(:|%3A)').replace(/\//g, '(/|%2F)')}$`)

/** The page's path, without its query: what a poll compares where the query may carry the item a wide window opens. */
export const pagePath = (page: Page): string => new URL(page.url()).pathname

/** The count of what needs you that leads the tab's title while something does ("(3) ", "(9+) " when a store was only partly read). */
export const TITLE_COUNT = /^\(\d+\+?\) /

/**
 * The tab's title without its leading count of what needs you (TITLE_COUNT): the page's own part, whatever the count. A spec that
 * anchors a title at its start reads it through this, polled (`expect.poll(() => pageTitle(page)).toMatch(...)`).
 */
export async function pageTitle(page: Page): Promise<string> {
  return (await page.title()).replace(TITLE_COUNT, '')
}

/** The hrefs of the session and run links of the full sidebar's list, in the order they show. */
export function sidebarLinks(page: Page): Promise<string[]> {
  return page
    .locator('[data-session-list] a[href^="/sessions/"], [data-session-list] a[href^="/runs/"]')
    .evaluateAll((els) => els.map((el) => el.getAttribute('href') ?? ''))
}

/**
 * A read of the page made after a call that may move it (the stubbed app's invite, #281), for the expect.poll around it: a
 * rejection in the words Playwright gives an answer the page lost ("Execution context was destroyed", a page load's among them)
 * is "not yet", so the poll reads again until its bound and, ending there, shows that rejection as the value it last saw. Any
 * other rejection (a page function that throws, a wrong name) fails the poll at once in its own words.
 */
export function notYet<T>(read: () => Promise<T>): Promise<T | string> {
  return read().catch((e: unknown) => {
    if (e instanceof Error && e.message.includes('Execution context was destroyed')) return `not yet: ${e.message.split('\n')[0]}`
    throw e
  })
}

let calls = 0
/**
 * The stubbed app calls the page's `callback` with `arg` and the page keeps its answer under the key returned, read by
 * `callAnswer`: the call is started, never awaited across what it does to the page (#281), and a start made again after a lost
 * answer (notYet) calls nothing twice in one page.
 */
export async function startCall(page: Page, callback: string, arg: unknown): Promise<number> {
  const key = ++calls
  const start = () =>
    page.evaluate(
      ({ callback, arg, key }) => {
        const w = window as unknown as Record<string, unknown> & { e2eAnswers?: Record<number, unknown> }
        const answers = (w.e2eAnswers ??= {})
        if (key in answers) return true
        answers[key] = 'started'
        const call = (w[callback] as (arg: unknown) => unknown)(arg)
        void Promise.resolve(call).then((a) => (answers[key] = a), (e) => (answers[key] = `rejected: ${e}`))
        return true
      },
      { callback, arg, key },
    )
  await expect.poll(() => notYet(start)).toBe(true)
  return key
}

/** The answer the page kept for the call `key` (startCall): 'started' until it answers. */
export function callAnswer(page: Page, key: number): Promise<unknown> {
  return notYet(() => page.evaluate((key) => (window as unknown as { e2eAnswers?: Record<number, unknown> }).e2eAnswers?.[key], key))
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

/**
 * A Read again pressed until what it leads to is shown (`shown`). The page asks its failed read again by itself, as a re-read is, and
 * that read may answer first and take the button away: a click on it then waits for a button that never comes back.
 */
export async function readAgain(retry: Locator, shown: Locator) {
  await expect(async () => {
    if (!(await shown.count())) await retry.click({ timeout: 2_000 })
    await expect(shown).toBeVisible({ timeout: 2_000 })
  }).toPass({ timeout: 15_000 })
}

/** Raises the running test's timeout by `ms`, as a step is about to make waits of those caps (AGENTS.md, Tests that do not flake). */
export function budget(ms: number) {
  const info = test.info()
  info.setTimeout(info.timeout + ms)
}

/** The day (day.ts) of the running test, set only while its `day` fixture is in use: dayPage's server when none is given. */
let today: Day | undefined
/** The contexts dayPage opened in the running test: closed at its end, before the day's guard reads the server. */
const dayContexts = new Set<BrowserContext>()

async function closeDayContexts(): Promise<void> {
  for (const c of [...dayContexts]) {
    dayContexts.delete(c)
    await c.close().catch(() => undefined)
  }
}

export interface DayPageOptions {
  /** Where to go: '/' when not given. */
  path?: string
  /** The window: 1440 x 900 when not given. A phone is 390 x 844 with touch. */
  width?: number
  height?: number
  /** The colour mode, written under the app's own key (conductor-color-mode) before the page loads: dark when not given. */
  theme?: 'dark' | 'light'
  /** A touch screen: hasTouch and isMobile, so that `(pointer: coarse)` matches. */
  touch?: boolean
  /** Another server than the day's (the shared one: the `state` fixture). */
  server?: { baseURL: string; token: string }
  /** false: no workbench token is set (a page that asks for it). */
  token?: false
  /** What to wait for in place of the layout's root (a page drawn without the layout). */
  ready?: string
}

/**
 * A route on `ctx` that keeps the context's request interception on from before its first load to its end, for a spec that routes a
 * page while the page works: the test's own routes then come and go on top of it, and interception is never switched on or off
 * while a page has requests out (AGENTS.md, "Tests that do not flake"; #281). Its address is one the app never asks for, so no
 * request is handed to the test by it: Playwright continues every request no route of the test's takes, as with no route at all. A
 * route that took every request and let it through would hand each one to the test and back, and Playwright 1.44.1 sends a file
 * picked in the page without its bytes that way (dispatch.spec.ts's attachment upload reached the server as an empty part).
 * A context route, so that a test's page.unrouteAll leaves it, and it reaches every page of the context.
 */
export function standingRoute(ctx: BrowserContext): Promise<void> {
  return ctx.route('**/e2e-standing-route-no-request-has', (route) => route.fallback())
}

/**
 * A page in a context of its own on the day's server (or `server`): the workbench token, the display name "Nate" and the colour mode
 * set in the context's storage before the first load (so that a later write of the page's, or useTheme's, survives a reload), the
 * window and touch as asked. It goes to `path` and returns once the layout's root is drawn (`#dashboard-sidebar-main`, or
 * `[data-bottom-bar]` below 1024 px wide; `ready` in their place), `html` carries the theme's class, and `document.fonts.ready` has
 * settled. The context holds its standing route (standingRoute) from before that load. The context is closed at the end of the test,
 * before the day's guard.
 */
export async function dayPage(browser: Browser, o: DayPageOptions = {}): Promise<Page> {
  const server = o.server ?? today?.server.started
  if (!server) throw new Error("dayPage: no server: ask for the `day` fixture (the day's server), or give `server`")
  const width = o.width ?? 1440
  const theme = o.theme ?? 'dark'
  const storage = [
    { name: 'conductor-color-mode', value: theme },
    { name: 'conductor.displayName', value: 'Nate' },
    ...(o.token === false ? [] : [{ name: 'conductor.workbenchToken', value: server.token }]),
  ]
  const ctx = await browser.newContext({
    baseURL: server.baseURL,
    viewport: { width, height: o.height ?? 900 },
    ...(o.touch ? { hasTouch: true, isMobile: true } : {}),
    storageState: { cookies: [], origins: [{ origin: new URL(server.baseURL).origin, localStorage: storage }] },
  })
  dayContexts.add(ctx)
  await standingRoute(ctx)
  const page = await ctx.newPage()
  await page.goto(o.path ?? '/')
  await drawn(page, theme, o.ready ?? (width < 1024 ? '[data-bottom-bar]' : '#dashboard-sidebar-main'))
  return page
}

/**
 * The page that asks for the workbench token (the UX review's P18): drawn in place of the app while the page holds no token or the
 * server refused the one it holds. Its field has the placeholder "paste the workbench token" and its button is Connect; it has no Cancel.
 * The dialog named "Workbench token" is the menu's, over a working app.
 */
export function tokenAsk(page: Page): Locator {
  return page.locator('[data-connect-page]')
}

/** Waits for what says the page is drawn, the theme's class on `html`, and the fonts. */
async function drawn(page: Page, theme: 'dark' | 'light', ready: string): Promise<void> {
  await expect(page.locator(ready).first()).toBeVisible()
  if (theme === 'dark') await expect(page.locator('html')).toHaveClass(/\bdark\b/)
  else await expect(page.locator('html')).not.toHaveClass(/\bdark\b/)
  await page.evaluate(() => document.fonts.ready.then(() => undefined))
}

/**
 * Switches an open page to `theme` the way the app keeps it (localStorage conductor-color-mode), reloads it and waits for the layout's
 * root (or `ready`), the class on `html` and the fonts. A `colorScheme` context does not switch this app: it prefers dark whatever the
 * system says (nuxt.config.ts, theme.spec.ts).
 *
 * The theme is stored only once the app has mounted (`#__nuxt` holds its root). A goto returns at the page's load, before the app's
 * plugins have run; the colour-mode module's start then writes the preference the page read as it began back to the storage, over a
 * theme stored before it, and a reload that leaves the page after that write reads the old theme (#1019). The plugins run before the
 * app mounts, so a page with its root drawn has made that write.
 */
export async function useTheme(page: Page, theme: 'dark' | 'light', ready?: string): Promise<void> {
  await expect(page.locator('#__nuxt > *').first()).toBeAttached()
  await page.evaluate((t) => localStorage.setItem('conductor-color-mode', t), theme)
  await page.reload()
  const width = page.viewportSize()?.width ?? 1440
  await drawn(page, theme, ready ?? (width < 1024 ? '[data-bottom-bar]' : '#dashboard-sidebar-main'))
}

/**
 * The caps of the day's end (day.ts, end), each wait over every item at once: its runs stopped, its sessions ended, its server stopped.
 * Kept here, with the worker fixture whose own bound they make (with the server's start, START_WAIT_MS), past any test's.
 */
export const DAY_RUNS_END_MS = 30_000
export const DAY_SESSIONS_END_MS = 15_000
export const DAY_SERVER_END_MS = 15_000
/** The bound of a day's start and end together: the worker fixture's, and a guard test's day of its own. */
export const DAY_WORKER_MS = START_WAIT_MS + DAY_RUNS_END_MS + DAY_SESSIONS_END_MS + DAY_SERVER_END_MS

export const test = base.extend<{ api: Api; day: Day; dayPagesClosed: void; uiStateFresh: void }, { state: E2EState; dayServer: Day }>({
  state: [
    // eslint-disable-next-line no-empty-pattern
    async ({}, use) => {
      await use(readState())
    },
    { scope: 'worker' },
  ],
  // The day's server, started at the worker's first test that asks for the day and ended at the worker's end (day.ts, end).
  dayServer: [
    // eslint-disable-next-line no-empty-pattern
    async ({}, use) => {
      const d = await startDay()
      try {
        await use(d)
      } finally {
        await d.end()
      }
    },
    { scope: 'worker', timeout: DAY_WORKER_MS },
  ],
  // The day, read-only, and dayPage's server while the test runs (never after it, nor for a test that did not ask for it). After
  // the test the contexts dayPage opened are closed, then the guard (day.ts, guard) fails the test naming what the server lists
  // that is not as the day left it (the sessions and runs it added are ended first).
  day: async ({ dayServer }, use) => {
    await dayServer.api.freshUiState()
    today = dayServer
    try {
      await use(dayServer)
    } finally {
      today = undefined
    }
    await closeDayContexts()
    const { left, problems } = await dayServer.guard()
    if (left.length) await dayServer.endLeftovers(left)
    if (problems.length) throw new Error(`the test left or changed on the day's server what no part made: ${problems.join('; ')}`)
  },
  dayPagesClosed: [
    // eslint-disable-next-line no-empty-pattern
    async ({}, use) => {
      await use()
      await closeDayContexts()
    },
    { auto: true },
  ],
  // The app's screens' state, which the server keeps for every browser, as a fresh server holds it at each test's start (Api.freshUiState).
  uiStateFresh: [
    async ({ state }, use) => {
      await new Api(state).freshUiState()
      await use()
    },
    { auto: true },
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
