import { rmSync } from 'node:fs'
import { join } from 'node:path'
import type { Locator, Page } from '@playwright/test'
import { atRest, budget, dayPage, expect, test, type Session } from './fixtures'
import { ownServer, type OwnServer } from './own-server'

// What Launch agent and New agent trial start from comes back on a page of a new origin, as the desktop app's page is after each
// start (its server takes a fresh port, so the page's own storage is empty): each agent's Run as, and the last trial's agents, folder
// and Check line, kept by the server in its record of the last launch (GET /api/launch/last). Each test starts with no record and
// proves the memory in a NEW browser context against the same server.
test.describe.configure({ mode: 'serial' })

let own: OwnServer
const made: string[] = []
const crews: string[] = []

const STUB_PROMPT = String.raw`(?m)^> ?[^\n]*\s*\z`
const CHAT_AGENT = 'Claude Code (e2e stub), claude-proto'
const STEP_MS = 15_000

test.beforeAll(async () => {
  test.setTimeout(120_000)
  own = await ownServer('launch-kept', {
    agents: [{ id: 'claude-proto', from: 'claude', env: { STUB_CLAUDE_VERSION: '2.1.331', STUB_NODE: process.execPath, STUB_TUI: '1' }, promptShape: STUB_PROMPT, protocol: { name: 'claude-stream-json' } }],
  })
})

test.afterAll(async () => {
  for (const id of made) await own.api.stopSession(id).catch(() => {})
  for (const id of crews) await own.api.call('DELETE', `/api/crews/${encodeURIComponent(id)}`).catch(() => {})
  await own.stop()
})

/** No record of a last launch: each test proves what it kept itself. */
function noRecord() {
  rmSync(join(own.root, 'data', 'launch', 'last.json'), { force: true })
}

/** A page in a new browser context: its storage is empty, as the desktop app's page is after a start. */
async function freshPage(browser: Parameters<typeof dayPage>[0]): Promise<Page> {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  // The page waits for the server's record as long as this test may run: only a hang bounds the read (the product's 3 s is not the test's).
  await page.addInitScript((ms) => ((window as Window & { conductorLastLaunchMs?: number }).conductorLastLaunchMs = ms), test.info().timeout)
  await page.reload()
  await expect(page.locator('#dashboard-sidebar-main')).toBeVisible()
  expect(await page.evaluate(() => [localStorage.getItem('conductor.launch.runAs'), localStorage.getItem('conductor-trial')])).toEqual([null, null])
  return page
}

async function openLaunch(page: Page): Promise<Locator> {
  budget(2 * STEP_MS)
  await page.getByRole('button', { name: 'Launch agent' }).first().click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('button', { name: 'Launch', exact: true })).toBeVisible()
  return dialog
}

async function pick(dialog: Locator, name: string) {
  const radio = dialog.getByRole('radio', { name: new RegExp(`${name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`) })
  await radio.click()
  await expect(radio).toHaveAttribute('aria-checked', 'true')
}

const runAs = (dialog: Locator) => dialog.locator('[data-launch-run-as]')

test('Run as comes back as each agent was last launched on a page of a new origin', async ({ browser }) => {
  budget(8 * STEP_MS)
  noRecord()
  let page = await freshPage(browser)
  try {
    const dialog = await openLaunch(page)
    await pick(dialog, CHAT_AGENT)
    await expect(runAs(dialog).locator('[data-run-as-side="terminal"]')).toHaveAttribute('aria-checked', 'true')
    await runAs(dialog).locator('[data-run-as-side="protocol"]').click()
    const created = page.waitForResponse((r) => r.request().method() === 'POST' && new URL(r.url()).pathname === '/api/sessions')
    await dialog.getByRole('button', { name: 'Launch', exact: true }).click()
    const answer = await created
    const s = (await answer.json()) as Session
    if (s.id) made.push(s.id)
    expect(answer.status()).toBe(201)
  } finally {
    await page.context().close()
  }
  expect((await own.api.ok<{ models: Record<string, { way?: string }> }>('GET', '/api/launch/last')).models['claude-proto']?.way).toBe('protocol')
  page = await freshPage(browser)
  try {
    const dialog = await openLaunch(page)
    await pick(dialog, CHAT_AGENT)
    await expect(runAs(dialog).locator('[data-run-as-side="protocol"]')).toHaveAttribute('aria-checked', 'true')
    await dialog.getByRole('button', { name: 'Cancel' }).click()
  } finally {
    await page.context().close()
  }
})

test("New agent trial comes back as the last trial left it, its agents, folder and Check line, on a page of a new origin", async ({ browser }) => {
  budget(12 * STEP_MS)
  noRecord()
  let page = await freshPage(browser)
  let agents: Array<string | null> = []
  try {
    await page.goto('/crews')
    const form = await openForm(page)
    // Agent B moved to another agent than the form picked, so what comes back is the person's, not the form's own start.
    await form.locator('[data-trial-agent="1"]').click()
    const items = page.getByRole('menuitemcheckbox')
    await expect(items.nth(2)).toBeVisible()
    await items.nth(2).click()
    await expect(page.getByRole('menu')).toHaveCount(0)
    agents = await form.locator('[data-trial-agent]').evaluateAll((els) => els.map((e) => e.getAttribute('data-agent-id')))
    await form.locator('[data-compare-goal]').fill('A kept trial')
    await form.locator('[data-compare-check]').fill('go test "./..."')
    const folder = form.locator('[data-compare-cwd]').getByRole('combobox')
    await folder.fill(own.repo)
    await folder.press('Tab')
    const saved = page.waitForResponse((r) => r.request().method() === 'POST' && new URL(r.url()).pathname === '/api/crews/templates/compare')
    await page.locator('[data-compare-create]').click()
    const answer = await saved
    expect(answer.status()).toBe(201)
    const id = ((await answer.json()) as { crew?: { id?: string } }).crew?.id
    if (id) crews.push(id)
  } finally {
    await page.context().close()
  }
  page = await freshPage(browser)
  try {
    await page.goto('/crews')
    const form = await openForm(page)
    // The server's record is read as the form opens: the draft takes it while nothing was changed.
    await expect.poll(() => form.locator('[data-trial-agent]').evaluateAll((els) => els.map((e) => e.getAttribute('data-agent-id'))), { timeout: STEP_MS }).toEqual(agents)
    await expect(form.locator('[data-compare-cwd]').getByRole('combobox')).toHaveValue(own.repo)
    await expect(form.locator('[data-compare-check]')).toHaveValue('go test ./...')
    await expect(form.locator('[data-compare-goal]')).toHaveValue('')
    await page.locator('[data-compare-cancel]').click()
  } finally {
    await page.context().close()
  }
})

/** Opens New agent trial on Crews and returns its form, the dialog's opening ended and the agents on it. */
async function openForm(page: Page): Promise<Locator> {
  await page.locator('[data-new-comparison]').click()
  const form = page.locator('[data-compare-template]')
  await expect(form).toBeVisible()
  await atRest(page.getByRole('dialog').filter({ has: form }))
  await expect(form.locator('[data-trial-agent]').first()).toBeVisible()
  return form
}
