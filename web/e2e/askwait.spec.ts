import { existsSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Page } from '@playwright/test'
import { expect, member, rowAnswer, test, type Api } from './fixtures'

// An agent's own question outlasts its turn (#193). An agent asks with
// `conductor notify --ask` (the stub's `stub ask-wait`), then waits for the
// answer by ending its turn, which its hooks report as done: the question
// still shows, on the member's row in the sidebar, in the Yard's queue and on
// the run's card, with its words, until a person answers it from the row.
// Codex, whose window title says it works while it goes on after asking,
// shows Working meanwhile, and the question again at its turn's done. What
// the session went through is read from the admin event stream and the
// stub's files, never from the terminal's rows.
test.describe.configure({ mode: 'serial' })

const nonce = Date.now().toString(36)
const ASK = `Which database should the todo app keep, ${nonce}?`
const CODEX_ASK = `Which branch holds the core part, ${nonce}?`
const REPLY = `Postgres ${nonce}`
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

/** One of the stub's files (`txt`, `spin`), '' while there is none. */
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

/** The states the session's attention entries record from the question on. */
function fromTheQuestion(id: string, words: string): string[] | string {
  const entries = watch!.of(id)
  const q = entries.findIndex((e) => e.state === 'needs_input' && e.message === words)
  return q < 0 ? JSON.stringify(entries) : entries.slice(q).map((e) => e.state)
}

test.beforeAll(async ({ api, state }) => {
  // No startup question in the way: those are the trust spec's.
  for (const m of ['stub-hooks-review', 'stub-background', 'stub-update']) rmSync(join(state.home, '.codex', m), { force: true })
  watch = watchAttention(state.baseURL, state.token)
  crewId = (
    await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: 'e2e ask and wait',
      goal: 'ask and wait',
      cwd: '',
      where: 'server',
      isolation: 'none',
      openAfterLaunch: false,
      yolo: false,
      members: [
        // Claude Code's hooks: UserPromptSubmit reports working, Stop done.
        { name: 'asks', agentId: 'claude', prompt: `stub ask-wait ${ASK}`, start: { when: 'immediately' } },
        // Codex: its title says Working while it goes on, its notify reports done. Its first turn only says hello: the test asks
        // with a broadcast when its turn comes, so that the stub's spin does not run out meanwhile.
        { name: 'codex', agentId: 'codex', prompt: 'say hello', start: { when: 'immediately' } },
      ],
    })
  ).crew.id
})

test.afterAll(async ({ api }) => {
  watch?.close()
  for (const id of runs) await api.stopRun(id)
  if (crewId) await api.call('DELETE', `/api/crews/${encodeURIComponent(crewId)}`)
})

test("an agent's question still shows Needs you once its turn ends, on its row, the Yard and the run's card, until a reply from the row answers it", async ({ page, api, state }) => {
  const run = await api.launchCrew(crewId)
  runId = run.id
  runs.push(run.id)
  const id = await sessionOf(api, 'asks')
  await expect.poll(() => fromTheQuestion(id, ASK), { timeout: 45_000, message: 'the question is asked' }).toEqual(['needs_input'])
  const stub = stubOf(state.home, `stub ask-wait ${ASK}`)
  expect(stub).not.toBe('')
  // The agent's turn ends: the Stop hook reports done.
  stopSpin(state.home, stub)
  await expect.poll(() => fromTheQuestion(id, ASK), { timeout: 15_000, message: "the turn's done is recorded" }).toEqual(['needs_input', 'done'])
  const asked = await api.session(id)
  expect(asked.attention).toMatchObject({ state: 'needs_input', message: ASK })
  expect(member(await api.run(runId), 'asks').needsInput).toBe(true)

  // What a person sees: the row, the Yard's queue and the run's card ask, with the question's words.
  await page.goto('/crews')
  await expect(row(page, id)).toHaveAttribute('data-row-state', 'needs', { timeout: 30_000 })
  await expect(row(page, id).locator('[data-row-prompt]')).toHaveText(ASK)
  const card = page.locator(`[data-running-now] [data-run="${runId}"]`)
  await expect(card).toHaveAttribute('data-state', 'needs_input')
  await expect(card.locator('[data-run-ask]')).toContainText(ASK)
  const yard = await page.context().newPage()
  await yard.goto('/yard')
  const queued = yard.locator(`[data-wall-queue] [data-queue-session="${id}"]`)
  await expect(queued).toContainText(ASK, { timeout: 30_000 })
  // It stays: a second later it still asks, the same question.
  await page.waitForTimeout(1_000)
  await expect(row(page, id)).toHaveAttribute('data-row-state', 'needs')
  expect((await api.session(id)).attention?.id).toBe(asked.attention?.id)

  // A reply from the row answers it: the stub reads the line, and the row moves on.
  const field = (await rowAnswer(page, row(page, id))).locator('[data-row-reply] input, input[data-row-reply]').first()
  await field.fill(REPLY)
  await field.press('Enter')
  await expect.poll(() => stubFile(state.home, stub, 'txt').split('\n'), { timeout: 15_000, message: 'the reply was typed' }).toContain(REPLY)
  await expect(row(page, id)).not.toHaveAttribute('data-row-state', 'needs', { timeout: 15_000 })
  await expect(queued).toHaveCount(0, { timeout: 15_000 })
  await expect(card.locator('[data-run-ask]')).toHaveCount(0)
  expect((await api.session(id)).lastAnswer?.message).toBe(ASK)
  await yard.close()
})

test('a Codex member that asks and goes on shows Working while it works, then Needs you at its turn\'s done', async ({ page, api, state }) => {
  const id = await sessionOf(api, 'codex')
  await expect.poll(async () => (await api.session(id)).attention?.state, { timeout: 45_000, message: 'its first turn is done' }).toBe('done')
  await api.ok('POST', `/api/runs/${encodeURIComponent(runId)}/broadcast`, { text: `stub ask-wait ${CODEX_ASK}`, members: ['codex'] })
  await expect.poll(() => watch!.of(id).some((e) => e.state === 'needs_input' && e.message === CODEX_ASK), { timeout: 45_000, message: 'the question is asked' }).toBe(true)
  // Its program status says working (its title too, no longer weighed): a second after the question, working shows, the question set aside.
  await expect.poll(async () => (await api.session(id)).attention, { timeout: 15_000, message: 'working, from the status' }).toMatchObject({ state: 'working', source: 'status' })
  await page.goto('/crews')
  await expect(row(page, id)).toHaveAttribute('data-row-state', 'running', { timeout: 30_000 })
  const stub = stubOf(state.home, `stub ask-wait ${CODEX_ASK}`)
  expect(stub).not.toBe('')
  expect(stubFile(state.home, stub, 'spin'), 'the stub works on').toBe('spinning\n')
  // Its turn ends: notify reports done, and the question shows again.
  stopSpin(state.home, stub)
  await expect(row(page, id)).toHaveAttribute('data-row-state', 'needs', { timeout: 15_000 })
  await expect(row(page, id).locator('[data-row-prompt]')).toHaveText(CODEX_ASK)
  await expect.poll(() => fromTheQuestion(id, CODEX_ASK), { timeout: 15_000 }).toEqual(['needs_input', 'working', 'done', 'needs_input'])
  expect((await api.session(id)).attention).toMatchObject({ state: 'needs_input', message: CODEX_ASK })
})
