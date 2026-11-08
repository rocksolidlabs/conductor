import type { Locator, Page } from '@playwright/test'
import { Api, expect, stopStrays, task, test, type Run, type Session } from './fixtures'

// The palette's History (Program item HS1, docs/plans/history.md, PR-7), its acceptance boundary with the stub agents: "which session
// investigated this failure" answered from the palette, the row opening the evidence itself. A crew whose `fixed` check fails until
// its builder adds stub-fixed.txt on its second attempt: attempt 1's failed receipt is Checked ✗ with its six fields and "Investigated
// by build #2", and Enter opens the run page at that step's log, its SHA-256 the step log route's. A plain session whose chat holds the
// same word (posted there and sent to its agent) is Conversation, never Checked. `~` alone asks the four questions, and "What was last
// verified here" lists attempt 2's passing receipt. Find this elsewhere on the failed receipt lists a second crew's run of the same
// command in the same repository. Every row is found by its key, never by a count or a place.

interface Entry {
  key: string
  kind: string
  class: string
  outcome?: string
  title: string
  source: { label: string }
  host: { name?: string }
  project: { key?: string; repo?: string; tree?: string }
  revision: { label?: string }
  anchor: { ref: string; label: string }
  links: Array<{ rel: string; dir: string; ref: string; label: string }>
  availability: { state: string; words: string }
}

const crew = (name: string) => ({
  name,
  goal: 'a check that fails once, then passes',
  cwd: '',
  where: 'server',
  isolation: 'worktree',
  openAfterLaunch: false,
  yolo: null,
  members: [{ name: 'builder', agentId: 'worker', prompt: '', start: { when: 'task' } }],
  // The check fails until the builder adds stub-fixed.txt, which it does on its second attempt, handed attempt 1's receipt.
  tasks: [{ id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.', checks: [{ id: 'fixed', argv: ['git', 'cat-file', '-e', 'HEAD:stub-fixed.txt'] }] }],
})

/**
 * The test's timeout is the sum of its waits' caps, each added as its step is about to take it (AGENTS.md: a test's timeout bounds a
 * hang, not its steps), the config's 120 s above them for the work between: a step whose waits each keep their bound never runs the
 * test out on a loaded box.
 */
const STEP_MS = 15_000 // the config's expect bound, and an action's
const SUCCEEDED_MS = 150_000
const INDEXED_MS = 60_000
function budget(ms: number) {
  const info = test.info()
  info.setTimeout(info.timeout + ms)
}

const search = (api: Api, q: string) => api.ok<{ entries: Entry[] }>('GET', `/api/history?q=${encodeURIComponent(q)}&limit=100`).then((r) => r.entries)

async function succeeded(api: Api, id: string): Promise<Run> {
  budget(SUCCEEDED_MS)
  let last!: Run
  await expect
    .poll(
      async () => {
        last = await api.run(id)
        return last.outcome ?? ''
      },
      { timeout: SUCCEEDED_MS, intervals: [500] },
    )
    .toBe('succeeded')
  expect(task(last, 'build').attempts).toBe(2)
  return last
}

/** The `fixed` check's step of an attempt: its k, and the SHA-256 the step log route sends with its log. */
async function fixedStep(api: Api, run: string, n: number): Promise<{ k: number; sha: string }> {
  const base = `/api/runs/${encodeURIComponent(run)}/tasks/build/attempts/${n}`
  const steps = (await api.ok<{ steps: Array<{ phase: string; id: string; k: number }> }>('GET', `${base}/steps`)).steps
  const s = steps.find((x) => x.phase === 'check' && x.id === 'fixed')
  expect(s, `attempt ${n} of ${run} ran the check`).toBeTruthy()
  const res = await fetch(`${api.server.baseURL}${base}/steps/fixed/${s!.k}/log`, { headers: { Authorization: `Bearer ${api.server.token}` } })
  expect(res.status).toBe(200)
  const sha = res.headers.get('X-Conductor-Log-SHA256') ?? ''
  expect(sha).toMatch(/^[0-9a-f]{64}$/)
  return { k: s!.k, sha }
}

/** The palette opened from a page whose layout is drawn (its sidebar root: the listener's own component), its query typed whole. */
async function palette(page: Page, query: string): Promise<Locator> {
  budget(30_000 + 4 * STEP_MS) // the sidebar, the goto, the focus, the fill
  await page.goto('/agents')
  await expect(page.locator('[data-session-list]')).toBeVisible({ timeout: 30_000 })
  await page.keyboard.press('Control+k')
  const p = page.locator('[data-palette]')
  const input = p.locator('input[data-palette-input]')
  await expect(input).toBeFocused()
  await input.fill(query)
  await expect(input).toHaveValue(query)
  return p
}

test("which session investigated a failure is answered from the palette, and the row opens the evidence", async ({ page, api }) => {
  // Two crews of two attempts each, a session's chat, the index catching up with each: every wait below has its own bound, added to
  // the test's timeout as its step takes it (budget).
  await stopStrays(api, [])
  const tag = `hs${Date.now().toString(36)}`
  const crewA = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', crew(`e2e history ${tag}`))).crew.id
  const crewB = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', crew(`e2e history elsewhere ${tag}`))).crew.id
  const runA = await api.launchCrew(crewA)
  await succeeded(api, runA.id)
  // The second crew's run of the same command in the same repository (the server's default folder), what Find this elsewhere finds.
  const runB = await api.launchCrew(crewB)

  const failed = await fixedStep(api, runA.id, 1)
  const passed = await fixedStep(api, runA.id, 2)
  const failedKey = `check:${runA.id}/build/1/fixed/${failed.k}`
  const passedKey = `check:${runA.id}/build/2/fixed/${passed.k}`

  // A plain session whose chat holds the same word: posted there and sent to its agent, which its record keeps.
  await page.addInitScript(() => localStorage.setItem('conductor.displayName', 'Nate'))
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: `e2e history chat ${tag}` })
  try {
    budget(30_000 + 5 * STEP_MS) // the goto, the tab, its click, the fill, the send, the marker
    await page.goto(`/sessions/${s.id}`)
    const tab = page.locator('[data-inspector] [data-chat-tab]')
    await expect(tab).toBeVisible({ timeout: 30_000 })
    await tab.click()
    const chat = page.locator('[data-chat]')
    const field = chat.locator('[data-chat-input] textarea, textarea[data-chat-input]').first()
    await field.fill(`why does stub-fixed fail ${tag}`)
    await expect(field).toHaveValue(`why does stub-fixed fail ${tag}`)
    await chat.locator('[data-chat-to-agent]').click()
    await expect(chat.locator('[data-chat-marker]').first()).toContainText('Sent to agent by Nate', { timeout: 15_000 })

    // The index has what the rows below are about: the failed receipt with the attempt it was handed to, the chat's line.
    budget(2 * INDEXED_MS)
    await expect.poll(async () => (await search(api, 'stub-fixed outcome:failed')).find((e) => e.key === failedKey)?.links.some((l) => l.rel === 'handed' && l.dir === 'out') ?? false, { timeout: INDEXED_MS, message: 'the failed receipt is indexed with its investigator' }).toBe(true)
    let chatKey = ''
    await expect
      .poll(
        async () => {
          chatKey = (await search(api, `stub-fixed ${tag}`)).find((e) => e.kind === 'session.chat' && e.anchor.ref === `session:${s.id}`)?.key ?? ''
          return chatKey
        },
        { timeout: INDEXED_MS, message: "the session's chat line is indexed" },
      )
      .not.toBe('')
    const receipt = (await search(api, 'stub-fixed outcome:failed')).find((e) => e.key === failedKey)!

    // `~ stub-fixed outcome:failed`: a word of git's refusal in the receipt's log, and the filter (attempt 2's passing receipt holds
    // the same word in its argv, and the ranking does not fix which of the two comes first).
    let p = await palette(page, '~ stub-fixed outcome:failed')
    budget(30_000 + 5 * STEP_MS) // the mode, the row, its label, badge and third line
    await expect(p).toHaveAttribute('data-palette-mode', 'history')
    const row = p.locator(`[data-palette-row="${CSS_KEY(`history:${failedKey}`)}"]`)
    await expect(row).toBeVisible({ timeout: 30_000 })
    await expect(row.locator('[data-palette-label]')).toHaveText('fixed on build #1')
    await expect(row.locator('[data-palette-badge]')).toHaveText('Checked ✗')
    // The six fields, in their order, each the receipt's own: the source, this machine, the project, the revision, when, the log kept.
    const detail = (await row.locator('[data-palette-detail]').textContent()) ?? ''
    const fields = detail.split(' · ')
    expect(fields, detail).toHaveLength(6)
    const repo = (receipt.project.repo || receipt.project.tree || '').replace(/\/+$/, '').replace(/\/\.git$/, '').split('/').pop()
    expect(fields.slice(0, 4)).toEqual([receipt.source.label, receipt.host.name, receipt.project.key || repo, receipt.revision.label])
    expect(fields[4]).toMatch(/ ago$/)
    expect(fields[5]).toBe(receipt.availability.words)
    expect(receipt.availability.state).toBe('log')
    await expect(row.locator('[data-palette-third]')).toContainText('Investigated by build #2')

    // Enter opens the run page at attempt 1's `fixed` step, its log's SHA-256 the step log route's.
    budget(5 * STEP_MS) // the focus, Enter, the URL, the SHA, its match
    await row.focus()
    await expect(row).toHaveAttribute('data-palette-focused', '')
    await page.keyboard.press('Enter')
    await page.waitForURL((u) => u.pathname === `/runs/${runA.id}` && u.searchParams.get('attempt') === '1' && u.searchParams.get('step') === `check/fixed/${failed.k}`, { timeout: 15_000 })
    const modal = page.locator('[data-step-log-modal]')
    await expect(modal.locator('[data-step-log-sha]')).toHaveText(failed.sha)
    await expect(modal.locator('[data-step-log-sha-match]')).toHaveAttribute('data-step-log-sha-match', 'matches')

    // The plain session's chat line, said there: Conversation, never Checked.
    p = await palette(page, `~ stub-fixed ${tag}`)
    budget(30_000 + 2 * STEP_MS)
    const said = p.locator(`[data-palette-row="${CSS_KEY(`history:${chatKey}`)}"]`)
    await expect(said).toBeVisible({ timeout: 30_000 })
    await expect(said.locator('[data-palette-badge]')).toHaveText('Conversation')
    await expect(p.locator('[data-palette-row^="history:"] [data-palette-badge]', { hasText: 'Checked' })).toHaveCount(0)

    // `~` alone: the four questions; "What was last verified here" lists attempt 2's passing receipt.
    p = await palette(page, '~')
    budget(30_000 + 10 * STEP_MS) // four questions, the focus, Enter, the query, the row, its badge, Escape
    for (const id of ['failures', 'rejected', 'verified', 'sessions']) await expect(p.locator(`[data-palette-row="history-question:${id}"]`)).toBeVisible()
    const verified = p.locator('[data-palette-row="history-question:verified"]')
    await verified.focus()
    await expect(verified).toHaveAttribute('data-palette-focused', '')
    await page.keyboard.press('Enter')
    await expect(p.locator('input[data-palette-input]')).toHaveValue('~ class:checked outcome:passed')
    const pass = p.locator(`[data-palette-row="${CSS_KEY(`history:${passedKey}`)}"]`)
    await expect(pass).toBeVisible({ timeout: 30_000 })
    await expect(pass.locator('[data-palette-badge]')).toHaveText('Checked ✓')
    await page.keyboard.press('Escape')

    // Find this elsewhere on the failed receipt: the second crew's run of the same command in the same repository.
    await succeeded(api, runB.id)
    const elsewhere = await fixedStep(api, runB.id, 1)
    const elsewhereKey = `check:${runB.id}/build/1/fixed/${elsewhere.k}`
    budget(INDEXED_MS)
    await expect.poll(async () => (await api.ok<{ entries: Entry[] }>('GET', `/api/history/similar?key=${encodeURIComponent(failedKey)}`)).entries.some((e) => e.key === elsewhereKey), { timeout: INDEXED_MS, message: "the second crew's receipt is indexed" }).toBe(true)
    p = await palette(page, '~ stub-fixed outcome:failed')
    budget(2 * 30_000 + 5 * STEP_MS) // the row, its focus, →, the action, the similar row, the receipt's absence
    const again = p.locator(`[data-palette-row="${CSS_KEY(`history:${failedKey}`)}"]`)
    await expect(again).toBeVisible({ timeout: 30_000 })
    await again.focus()
    await expect(again).toHaveAttribute('data-palette-focused', '')
    await page.keyboard.press('ArrowRight')
    await again.locator('[data-palette-action="elsewhere"]').click()
    const similar = p.locator('[data-palette-group="history-similar"]')
    await expect(similar.locator(`[data-palette-row="${CSS_KEY(`history:${elsewhereKey}`)}"]`)).toBeVisible({ timeout: 30_000 })
    await expect(similar.locator(`[data-palette-row="${CSS_KEY(`history:${failedKey}`)}"]`)).toHaveCount(0)
  } finally {
    await api.stopSession(s.id)
  }
})

/** A key as an attribute selector's value: its quotes and backslashes escaped (a check key holds slashes and colons only). */
function CSS_KEY(key: string): string {
  return key.replace(/["\\]/g, '\\$&')
}
