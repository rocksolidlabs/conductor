import { existsSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Page } from '@playwright/test'
import { expect, member, rowAnswer, test, type Api } from './fixtures'

// A Codex member's Needs you while Codex works (#186). Conductor launches
// Codex with tui.terminal_title=["activity","run-state"], and the stub, so
// launched, writes Codex 0.161's window title: a frame and "Working" while it
// answers or spins, "[ ! ] Action Required" while a dialog or an async
// question waits on a person, "Ready" at rest. A member that asks its own
// question and goes on (`stub ask`) shows Needs you, and Working once the
// question's second has passed, with the spinner's frames or with its title
// written once (animations off); a question Codex asks without waiting
// (`stub async`) never shows Needs you; one the agent asks while such a
// question is pending (`stub async-ask`) shows Needs you, on the member's row
// and the Yard, until the pending question resolves, then Working; Codex's
// approval still shows with its choices, and Working follows its answer.
// What the session went through is read from the admin event stream (each
// attention entry with the state it records) and the stub's files, never
// from the terminal's rows.
test.describe.configure({ mode: 'serial' })

const nonce = Date.now().toString(36)
const ASK = `Keep the items in one JSON file ${nonce}?`
const STILL_ASK = `Keep the items in one JSON file, still ${nonce}?`
const ASYNC_ASK = `Which branch holds the core part ${nonce}?`
const ASYNC = `Which file should the tests go in ${nonce}?`
let crewId = ''
let runId = ''
const runs: string[] = []
let watch: Watch | undefined

/** An attention entry of the admin event stream: when, the state it records, its words. */
interface Entry {
  at: number
  state: string
  message?: string
}

interface Watch {
  /** The attention entries of session id so far, oldest first. */
  of: (id: string) => Entry[]
  close: () => void
}

/** Reads the admin event stream (`event: activity`, type attention) until closed: every attention entry of every session. */
function watchAttention(baseURL: string, token: string): Watch {
  const seen = new Map<string, Entry[]>()
  const ctl = new AbortController()
  void (async () => {
    try {
      const r = await fetch(`${baseURL}/api/events`, { headers: { Authorization: `Bearer ${token}` }, signal: ctl.signal })
      const reader = r.body!.getReader()
      const dec = new TextDecoder()
      let buf = ''
      for (;;) {
        const { value, done } = await reader.read()
        if (done) break
        buf += dec.decode(value, { stream: true })
        let i: number
        while ((i = buf.indexOf('\n\n')) >= 0) {
          const block = buf.slice(0, i)
          buf = buf.slice(i + 2)
          if (/^event: (.*)$/m.exec(block)?.[1] !== 'activity') continue
          const data = block
            .split('\n')
            .filter((l) => l.startsWith('data: '))
            .map((l) => l.slice(6))
            .join('\n')
          const p = JSON.parse(data) as { sessionId: string; type: string; at: string; state?: string; message?: string }
          if (p.type !== 'attention' || !p.state) continue
          const list = seen.get(p.sessionId) ?? []
          list.push({ at: Date.parse(p.at), state: p.state, message: p.message })
          seen.set(p.sessionId, list)
        }
      }
    } catch {
      /* closed */
    }
  })()
  return { of: (id) => [...(seen.get(id) ?? [])], close: () => ctl.abort() }
}

/** The stub agent whose transcript holds line: its id names its files under the server's HOME. */
function stubOf(home: string, line: string): string {
  const dir = join(home, '.stub-sessions')
  for (const f of existsSync(dir) ? readdirSync(dir) : []) {
    if (f.endsWith('.txt') && readFileSync(join(dir, f), 'utf8').split('\n').includes(line)) return f.slice(0, -4)
  }
  return ''
}

/** One of the stub's files (`spin`, `async`), '' while there is none. */
function stubFile(home: string, stub: string, ext: string): string {
  const file = join(home, '.stub-sessions', `${stub}.${ext}`)
  return existsSync(file) ? readFileSync(file, 'utf8') : ''
}

/** Ends the stub's spin at its next dot: its turn goes on to its done. */
function stopSpin(home: string, stub: string) {
  writeFileSync(join(home, '.stub-sessions', `${stub}.spin`), 'stop\n')
}

/** The session of a member of the run, once it has one. */
async function sessionOf(api: Api, name: string): Promise<string> {
  let id = ''
  await expect.poll(async () => (id = member(await api.run(runId), name).sessionId ?? ''), { timeout: 45_000, message: `${name}'s session` }).not.toBe('')
  return id
}

/** The member's row in the sidebar. */
function row(page: Page, id: string) {
  return page.locator(`[data-session-list="sidebar"] [data-sidebar-row="s:${id}"]`).first()
}

test.beforeAll(async ({ api, state }) => {
  // No startup question in the way: those are the trust spec's.
  for (const m of ['stub-hooks-review', 'stub-background', 'stub-update']) rmSync(join(state.home, '.codex', m), { force: true })
  watch = watchAttention(state.baseURL, state.token)
  crewId = (
    await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: 'e2e codex working',
      goal: 'codex working',
      cwd: '',
      where: 'server',
      isolation: 'none',
      openAfterLaunch: false,
      yolo: false,
      members: [
        { name: 'asks', agentId: 'codex', prompt: `stub ask ${ASK}`, start: { when: 'immediately' } },
        // Codex with animations off writes "Working" once a turn, no frames.
        { name: 'still', agentId: 'codex', args: ['-c', 'tui.animations=false'], prompt: 'say hello', start: { when: 'immediately' } },
      ],
    })
  ).crew.id
})

test.afterAll(async ({ api }) => {
  watch?.close()
  for (const id of runs) await api.stopRun(id)
  if (crewId) await api.call('DELETE', `/api/crews/${encodeURIComponent(crewId)}`)
})

/** A key pressed in the session's terminal on its page, as a person at the keyboard. */
async function pressInTerminal(page: import('@playwright/test').Page, sessionId: string, key: string) {
  await page.goto(`/sessions/${encodeURIComponent(sessionId)}`)
  const screen = page.locator('.terminal-host .xterm-screen').first()
  await expect(screen).toBeVisible()
  await screen.click()
  await page.keyboard.press(key)
}

/**
 * The session's entry of the question asked in `words` and its first working entry after it (both since `since`), once the stream has
 * brought that entry: a state the session shows says nothing of its entry, which the session records after the change and the
 * stream brings after that.
 */
async function workedAfter(id: string, words: string, since = 0): Promise<{ asked: Entry; worked: Entry }> {
  let entries: Entry[] = []
  const askedIn = (list: Entry[]) => list.find((e) => e.state === 'needs_input' && e.message === words)
  const workedIn = (list: Entry[], asked: Entry) => list.find((e) => e.state === 'working' && e.at >= asked.at)
  await expect
    .poll(
      () => {
        entries = watch!.of(id).filter((e) => e.at >= since)
        const asked = askedIn(entries)
        return asked && workedIn(entries, asked) ? 'kept' : JSON.stringify(entries)
      },
      { timeout: 15_000, message: 'a working entry after the question' },
    )
    .toBe('kept')
  const asked = askedIn(entries)!
  return { asked, worked: workedIn(entries, asked)! }
}

/** The member asked, and gave way to working a settle (a second) later, while its stub still spins. */
async function askedThenWorked(api: Api, home: string, id: string, words: string): Promise<string> {
  await expect.poll(() => watch!.of(id).some((e) => e.state === 'needs_input' && e.message === words), { timeout: 45_000, message: 'the question is asked' }).toBe(true)
  await expect.poll(async () => (await api.session(id)).attention, { timeout: 15_000, message: 'working, from the status' }).toMatchObject({ state: 'working', source: 'status' })
  const { asked, worked } = await workedAfter(id, words)
  expect(worked.at - asked.at, 'the question stood its second').toBeGreaterThanOrEqual(950)
  const stub = stubOf(home, `stub ask ${words}`)
  expect(stub).not.toBe('')
  expect(stubFile(home, stub, 'spin'), 'the stub works on').toBe('spinning\n')
  return stub
}

test("a Codex member that asks and goes on shows Needs you, then Working while it works, then done", async ({ page, api, state }) => {
  const run = await api.launchCrew(crewId)
  runId = run.id
  runs.push(run.id)
  const id = await sessionOf(api, 'asks')
  const stub = await askedThenWorked(api, state.home, id, ASK)
  // The launch asked Codex for its title.
  expect((await api.session(id)).command).toEqual(expect.arrayContaining(['-c', 'tui.terminal_title=["activity","run-state"]']))
  // What a person sees while it works: the member's row runs, it is not in Needs you, and the run counts no one waiting.
  await page.goto('/crews')
  await expect(row(page, id)).toHaveAttribute('data-row-state', /^(working|finished|running)$/, { timeout: 30_000 })
  await expect(row(page, id).locator('[data-row-prompt]')).toHaveCount(0)
  expect(member(await api.run(runId), 'asks').needsInput ?? false).toBe(false)
  await page.waitForTimeout(1_500)
  await expect(row(page, id)).toHaveAttribute('data-row-state', /^(working|finished|running)$/)
  // The row's mark says working, from Codex's program status (source status): the status walk's one proof from a real agent's signal.
  await expect(row(page, id).locator('[data-status-mark]')).toHaveAttribute('data-status-mark', 'working')
  expect(stubFile(state.home, stub, 'spin')).toBe('spinning\n')
  // Its turn ends: done, and the mark says finished.
  stopSpin(state.home, stub)
  await expect.poll(async () => (await api.session(id)).attention?.state, { timeout: 15_000 }).toBe('done')
  await expect(row(page, id).locator('[data-status-mark]')).toHaveAttribute('data-status-mark', 'finished')
  await expect.poll(() => watch!.of(id).map((e) => e.state)).toEqual(['working', 'needs_input', 'working', 'done'])
})

test('with its title written once (animations off) the question gives way all the same', async ({ api, state }) => {
  const id = await sessionOf(api, 'still')
  expect((await api.session(id)).command).toEqual(expect.arrayContaining(['-c', 'tui.animations=false']))
  // Its first turn ("say hello") ends; the next asks and goes on.
  await expect.poll(async () => (await api.session(id)).attention?.state, { timeout: 45_000 }).toBe('done')
  await api.ok('POST', `/api/runs/${encodeURIComponent(runId)}/broadcast`, { text: `stub ask ${STILL_ASK}`, members: ['still'] })
  const stub = await askedThenWorked(api, state.home, id, STILL_ASK)
  stopSpin(state.home, stub)
  await expect.poll(async () => (await api.session(id)).attention?.state, { timeout: 15_000 }).toBe('done')
})

test("a question the agent asks while one of Codex's async questions is pending shows Needs you for its second, then Working while that question is still pending (#197)", async ({ page, api, state }) => {
  const id = await sessionOf(api, 'asks')
  await page.goto('/crews')
  const yard = await page.context().newPage()
  await yard.goto('/yard')
  const tile = yard.locator(`[data-session-tile][data-session-id="${id}"]`)
  await api.ok('POST', `/api/runs/${encodeURIComponent(runId)}/broadcast`, { text: `stub async-ask ${ASYNC_ASK}`, members: ['asks'] })
  // Codex's title says Action Required while its question is pending, but its program status (0.164) says working: the agent's own question shows for its second, then gives way. A second is too short to assert on the row or the Yard's card under load; the entries below hold the words and the timing.
  await expect.poll(() => watch!.of(id).some((e) => e.state === 'needs_input' && e.message === ASYNC_ASK), { timeout: 45_000, message: 'the question is asked' }).toBe(true)
  const stub = stubOf(state.home, `stub async-ask ${ASYNC_ASK}`)
  expect(stub).not.toBe('')
  await expect.poll(async () => (await api.session(id)).attention, { timeout: 15_000 }).toMatchObject({ state: 'working', source: 'status' })
  await expect(row(page, id)).toHaveAttribute('data-row-state', /^(working|finished|running)$/, { timeout: 15_000 })
  // The Yard's tile, read after the server says working: its foot says working, and it asks nothing.
  await expect(tile.locator('[data-tile-foot] [data-status-mark]')).toHaveAttribute('data-status-mark', 'working', { timeout: 15_000 })
  await expect(tile.locator('[data-tile-ask]')).toHaveCount(0)
  // Working came a second or more after the question and before the async question resolved: the entries and the
  // stub's resolution stamp say so, whatever the pace of the polls above.
  await expect.poll(() => stubFile(state.home, stub, 'async'), { timeout: 30_000 }).toMatch(/^resolved \d+$/m)
  const resolved = Number(/resolved (\d+)\n$/.exec(stubFile(state.home, stub, 'async'))![1]) / 1000
  const { asked, worked } = await workedAfter(id, ASYNC_ASK)
  expect(worked.at - asked.at, 'the question stood its second').toBeGreaterThanOrEqual(950)
  expect(worked.at, `Working (${worked.at}) before the async question resolved (${resolved})`).toBeLessThan(resolved)
  // The stub spins on after the resolution; a stop written before it began spinning is lost, and it spins its full thirty seconds.
  await expect.poll(() => stubFile(state.home, stub, 'spin'), { timeout: 15_000, message: 'the stub spins after the resolution' }).toBe('spinning\n')
  stopSpin(state.home, stub)
  await expect.poll(async () => (await api.session(id)).attention?.state, { timeout: 15_000 }).toBe('done')
  await yard.close()
})

test("with Codex 0.163's status the question stands until the async question resolves, as before", async ({ api, state }) => {
  // The no-status shape (Codex 0.162 and older, the title alone) is not staged here: this session has read
  // statuses in the tests above and, as a program never stops writing its status mid-session, stops weighing
  // the title for good; the title path keeps its Go tests (title_test.go) and status_test.go's no-status case.
  for (const mode of ['0.163']) {
    const id = await sessionOf(api, 'asks')
    const since = Date.now()
    const words = `${ASYNC_ASK} ${mode}`
    await api.ok('POST', `/api/runs/${encodeURIComponent(runId)}/broadcast`, { text: `stub status ${mode}`, members: ['asks'] })
    await api.ok('POST', `/api/runs/${encodeURIComponent(runId)}/broadcast`, { text: `stub async-ask ${words}`, members: ['asks'] })
    await expect.poll(() => watch!.of(id).some((e) => e.at >= since && e.state === 'needs_input' && e.message === words), { timeout: 45_000, message: `${mode}: the question is asked` }).toBe(true)
    const stub = stubOf(state.home, `stub async-ask ${words}`)
    expect(stub).not.toBe('')
    // 0.163's status says blocked while the async question is pending: the question stands.
    await expect.poll(() => stubFile(state.home, stub, 'async'), { timeout: 30_000 }).toMatch(/^resolved \d+$/m)
    const resolved = Number(/resolved (\d+)\n$/.exec(stubFile(state.home, stub, 'async'))![1]) / 1000
    await expect.poll(async () => (await api.session(id)).attention?.state, { timeout: 15_000, message: `${mode}: working once resolved` }).toBe('working')
    const { worked } = await workedAfter(id, words, since)
    expect(worked.at, `${mode}: Working only once the async question resolved`).toBeGreaterThanOrEqual(resolved - 5)
    await expect.poll(() => stubFile(state.home, stub, 'spin'), { timeout: 15_000, message: `${mode}: the stub spins after the resolution` }).toBe('spinning\n')
    stopSpin(state.home, stub)
    const stopped = Date.now()
    // On a miss the received string carries the state, its source and the turn's entries.
    await expect.poll(async () => {
      const a = (await api.session(id)).attention
      if (a?.state === 'done') return 'done'
      return `${a?.state}/${a?.source} after ${JSON.stringify(watch!.of(id).filter((e) => e.at >= since).map((e) => `${e.state}:${(e.message ?? '').slice(0, 24)}`))}`
    }, { timeout: 45_000 }).toBe('done')
    console.log(`[diag] ${mode}: done ${Date.now() - stopped} ms after stopSpin`)
    await api.ok('POST', `/api/runs/${encodeURIComponent(runId)}/broadcast`, { text: 'stub status 0.164', members: ['asks'] })
  }
})

test("a form no pattern knows shows Needs you from Codex's status alone, with its words, and Working after the answer (#197)", async ({ page, api, state }) => {
  const id = await sessionOf(api, 'asks')
  const since = Date.now()
  await page.goto('/crews')
  await api.ok('POST', `/api/runs/${encodeURIComponent(runId)}/broadcast`, { text: 'stub auth', members: ['asks'] })
  await expect.poll(async () => (await api.session(id)).attention, { timeout: 30_000, message: 'the sign-in is asked' }).toMatchObject({ state: 'needs_input', source: 'status', message: 'The agent needs you to sign in' })
  await expect(row(page, id)).toHaveAttribute('data-row-state', 'needs', { timeout: 30_000 })
  // A key in the terminal answers it: the stub signs in and works on.
  await pressInTerminal(page, id, 'x')
  await expect.poll(async () => (await api.session(id)).attention?.state, { timeout: 15_000 }).toBe('done')
  // The done shown says nothing of its entry: the stream brings it after.
  await expect.poll(() => watch!.of(id).filter((e) => e.at >= since).map((e) => e.state), { timeout: 15_000 }).toEqual(['working', 'needs_input', 'working', 'done'])
  await api.ok('POST', `/api/runs/${encodeURIComponent(runId)}/broadcast`, { text: `stub form ${ASYNC_ASK}`, members: ['asks'] })
  await expect.poll(async () => (await api.session(id)).attention, { timeout: 30_000, message: 'the form is asked' }).toMatchObject({ state: 'needs_input', source: 'status', message: 'The agent asks a question', kind: 'prompt' })
  await pressInTerminal(page, id, 'Enter')
  await expect.poll(async () => (await api.session(id)).attention?.state, { timeout: 15_000 }).toBe('done')
})

test("a turn Codex's status says ended in an error shows Needs you in Conductor's words until a key in the terminal (#991)", async ({ page, api }) => {
  const id = await sessionOf(api, 'asks')
  const since = Date.now()
  await page.goto('/crews')
  await api.ok('POST', `/api/runs/${encodeURIComponent(runId)}/broadcast`, { text: 'stub fail', members: ['asks'] })
  await expect
    .poll(async () => (await api.session(id)).attention, { timeout: 30_000, message: 'the failed turn waits' })
    .toMatchObject({ state: 'needs_input', source: 'status', message: "The agent's turn ended in an error; its terminal says why" })
  await expect(row(page, id)).toHaveAttribute('data-row-state', 'needs', { timeout: 30_000 })
  // Its one entry is the failed turn's, after the working its status said first.
  await expect.poll(() => watch!.of(id).filter((e) => e.at >= since).map((e) => e.state), { timeout: 15_000 }).toEqual(['working', 'needs_input'])
  // The person's Enter in the terminal takes it away.
  await pressInTerminal(page, id, 'Enter')
  await expect.poll(async () => (await api.session(id)).attention, { timeout: 15_000, message: 'the Enter took it away' }).toMatchObject({ state: '', source: 'input' })
})

test("a question Codex asks without waiting never shows Needs you", async ({ api, state }) => {
  const id = await sessionOf(api, 'asks')
  const since = Date.now()
  await api.ok('POST', `/api/runs/${encodeURIComponent(runId)}/broadcast`, { text: `stub async ${ASYNC}`, members: ['asks'] })
  await expect.poll(() => stubOf(state.home, `stub async ${ASYNC}`), { timeout: 30_000 }).not.toBe('')
  const stub = stubOf(state.home, `stub async ${ASYNC}`)
  // Pending, then resolved, and the stub spins on.
  await expect.poll(() => stubFile(state.home, stub, 'async').trim().split('\n').length, { timeout: 30_000 }).toBe(2)
  await expect.poll(() => stubFile(state.home, stub, 'spin'), { timeout: 15_000 }).toBe('spinning\n')
  await expect.poll(async () => (await api.session(id)).attention, { timeout: 15_000 }).toMatchObject({ state: 'working', source: 'status' })
  stopSpin(state.home, stub)
  await expect.poll(async () => (await api.session(id)).attention?.state, { timeout: 15_000 }).toBe('done')
  const states = () => watch!.of(id).filter((e) => e.at >= since).map((e) => e.state)
  await expect.poll(states, { message: 'no Needs you through the async question' }).toEqual(['working', 'done'])
})

test("Codex's approval still shows with its three choices, and Working follows its answer", async ({ page, api, state }) => {
  const id = await sessionOf(api, 'asks')
  const since = Date.now()
  await page.goto('/crews')
  await api.ok('POST', `/api/runs/${encodeURIComponent(runId)}/broadcast`, { text: `stub approve ls -la ${nonce}`, members: ['asks'] })
  await expect.poll(async () => (await api.session(id)).attention?.source ?? '', { timeout: 30_000, message: 'the approval is asked' }).toBe('question')
  // Its title says Action Required (blinking): past the bell's second the approval stands.
  await page.waitForTimeout(2_000)
  const asked = (await api.session(id)).attention
  expect(asked?.source).toBe('question')
  expect(asked?.options?.map((o) => o.label)).toEqual(['Yes, proceed', "Yes, and don't ask again", 'No, tell Codex what to do differently'])
  await expect(row(page, id)).toHaveAttribute('data-row-state', 'needs', { timeout: 30_000 })
  const answers = await rowAnswer(page, row(page, id))
  await expect(answers.locator('[data-row-choice]')).toHaveCount(3)
  await answers.locator('[data-row-choice="1"]').click()
  const stub = stubOf(state.home, `stub approve ls -la ${nonce}`)
  await expect.poll(() => readFileSync(join(state.home, '.stub-sessions', `${stub}.txt`), 'utf8'), { timeout: 15_000 }).toContain('approval: approved\n')
  await expect.poll(async () => (await api.session(id)).attention?.state, { timeout: 15_000 }).toBe('done')
  // The turn: working, the bell, the dialog's question, working once answered, done.
  const after = () => {
    const states = watch!.of(id).filter((e) => e.at >= since)
    const q = states.findIndex((e) => e.state === 'needs_input' && e.message === `Would you like to run the following command? $ ls -la ${nonce}`)
    return q < 0 ? JSON.stringify(states) : states.slice(q + 1).map((e) => e.state)
  }
  await expect.poll(after).toEqual(['working', 'done'])
})
