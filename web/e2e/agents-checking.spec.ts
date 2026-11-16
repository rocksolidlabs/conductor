import type { Page } from '@playwright/test'
import { expect, test, viaMore } from './fixtures'

// The Agents page draws its list at once and fills each agent's state in as
// the server's check of it answers (#704). The page's first read waits for
// nothing: here it is the server's own answer with the Codex stub marked as
// still being checked, as when its probe is under way, and the Codex stub's
// own read, which waits on the server for that check, is held at the browser
// until the test lets it go.
test("the Agents page lists its agents while one agent's check is held, that agent marked as being checked", async ({ page }) => {
  let release = () => {}
  const held = new Promise<void>((r) => (release = r))
  let asked = () => {}
  const reading = new Promise<void>((r) => (asked = r))
  await page.route(
    (url) => url.pathname === '/api/catalog' && url.searchParams.get('wait') === '0',
    async (route) => {
      const response = await route.fetch()
      const body = (await response.json()) as { agents: Array<{ id: string; checking?: boolean; identity?: unknown }> }
      for (const a of body.agents) {
        if (a.id !== 'codex') continue
        a.checking = true
        delete a.identity
      }
      await route.fulfill({ response, json: body })
    },
  )
  await page.route(
    (url) => url.pathname === '/api/catalog/codex',
    async (route) => {
      asked()
      await held
      await route.continue()
    },
  )
  await page.goto('/agents')
  const codex = page.locator('[data-agent="codex"]')
  await expect(codex.locator('[data-checking]')).toContainText('Checking…')
  // The page asked for the Codex stub's state, and the browser holds it.
  await reading
  // Meanwhile the other agents are listed with their states.
  await expect(page.locator('[data-agent="claude"] [data-identity="ok"]')).toContainText('Claude Code 2.1.287')
  await expect(page.locator('[data-agent="goose"] [data-not-installed][data-not-agent]')).toBeVisible()
  await expect(page.locator('[data-agent="plain"]')).toBeVisible()
  await expect(page.locator('[data-agent="claude"] [data-checking]')).toHaveCount(0)
  // The held one is still being checked, with no state of its own yet.
  await expect(codex.locator('[data-checking]')).toBeVisible()
  await expect(codex.locator('[data-identity]')).toHaveCount(0)
  release()
  await expect(codex.locator('[data-identity="ok"]')).toContainText('Codex CLI 0.159.0')
  await expect(codex.locator('[data-checking]')).toHaveCount(0)
})

/** The page's quick read, each agent in `ids` changed by `f` as the server's answer is handed to the page. */
async function quickRead(page: Page, ids: readonly string[], f: (a: Agent) => void) {
  await page.route(
    (url) => url.pathname === '/api/catalog' && url.searchParams.get('wait') === '0',
    async (route) => {
      const response = await route.fetch()
      const body = (await response.json()) as { agents: Agent[] }
      for (const a of body.agents) if (ids.includes(a.id)) f(a)
      await route.fulfill({ response, json: body })
    },
  )
}
type Agent = { id: string; checking?: boolean; recheck?: boolean; available?: boolean; identity?: { pending?: boolean } }
/** Whether a request is one agent's read of its check, GET /api/catalog/{id}, and of which agent. */
const agentRead = (url: URL) => /^\/api\/catalog\/[^/]+$/.exec(url.pathname) && url.pathname.slice('/api/catalog/'.length)

// A program missing when the server last looked, installed since, its answer
// past its time: the server's quick read shows it missing, from that answer,
// and marks it for a re-read (here the test's route stands in for that
// answer: the Codex stub is installed). The page reads it again on the same
// opening, and the card is right without a second one. The server never sends
// `recheck` beside `checking` (an agent still being checked has no answer to
// keep), so the test's answer has no `checking`, whatever the server's own
// lookup of the stub was doing when the page asked: the badge is decided by
// the test's answer alone, never by how warm the server's cache was.
test('an agent shown from an answer past its time is read again on the same opening', async ({ page }) => {
  await quickRead(page, ['codex'], (a) => {
    a.available = false
    a.recheck = true
    delete a.checking
    delete a.identity
  })
  let release = () => {}
  const held = new Promise<void>((r) => (release = r))
  let reads = 0
  await page.route(
    (url) => agentRead(url) === 'codex',
    async (route) => {
      reads++
      await held
      await route.continue()
    },
  )
  await page.goto('/agents')
  const codex = page.locator('[data-agent="codex"]')
  // The kept answer is shown, not a spinner, while the read is held.
  await expect(codex.locator('[data-not-installed]')).toBeVisible()
  await expect(codex.locator('[data-checking]')).toHaveCount(0)
  await expect.poll(() => reads).toBe(1)
  release()
  await expect(codex.locator('[data-identity="ok"]')).toContainText('Codex CLI 0.159.0')
  await expect(codex.locator('[data-not-installed]')).toHaveCount(0)
  expect(reads).toBe(1)
})

// Every check ends in words: an agent whose reads all say "still checking"
// (its version flag pending) and one whose read fails each say the check did
// not answer, with no spinner and no "Checking the version…" left, and the
// other agents are listed with their states meanwhile.
test('a check that never settles and a read that fails each end in words', async ({ page }) => {
  await quickRead(page, ['codex', 'claude'], (a) => {
    a.checking = true
    a.identity = { ...a.identity, pending: true }
  })
  let codexReads = 0
  await page.route(
    (url) => agentRead(url) === 'codex',
    async (route) => {
      codexReads++
      const response = await route.fetch()
      const body = (await response.json()) as { agent: Agent }
      body.agent.checking = true
      body.agent.identity = { ...body.agent.identity, pending: true }
      await route.fulfill({ response, json: body })
    },
  )
  await page.route(
    (url) => agentRead(url) === 'claude',
    (route) => route.fulfill({ status: 500, json: { error: { code: 'internal', message: 'held by the test' } } }),
  )
  await page.goto('/agents')
  for (const id of ['codex', 'claude']) {
    const card = page.locator(`[data-agent="${id}"]`)
    await expect(card.locator('[data-unanswered]')).toContainText('Check did not answer')
    await expect(card.locator('[data-unanswered]')).toHaveAttribute('title', /the page checks again when it is opened or refreshed/)
    await expect(card.locator('[data-checking]')).toHaveCount(0)
    await expect(card.locator('[data-identity]')).toHaveCount(0)
  }
  // The page read the Codex stub its ten times, and no more.
  expect(codexReads).toBe(10)
  await expect(page.locator('[data-agent="goose"] [data-not-installed][data-not-agent]')).toBeVisible()
  await expect(page.locator('[data-agent="plain"] [data-unanswered]')).toHaveCount(0)
})

// A read that never answers is aborted at its deadline (CHECK_READ_DEADLINE_MS
// in web/app/utils/agents.ts, 10 s), and the card says the check did not
// answer. The read is held at the test's route; the wait for the abort is the
// deadline itself, the only cap the page applies, with the expect's and the
// test's timeouts above it.
test('a read that never answers is aborted at its deadline', async ({ page }) => {
  const deadline = 10_000
  test.setTimeout(deadline + 60_000)
  await quickRead(page, ['codex'], (a) => {
    a.checking = true
    delete a.identity
  })
  let asked = () => {}
  const reading = new Promise<void>((r) => (asked = r))
  await page.route(
    (url) => agentRead(url) === 'codex',
    () => asked(),
  )
  const aborted = page.waitForEvent('requestfailed', (r) => agentRead(new URL(r.url())) === 'codex')
  await page.goto('/agents')
  const codex = page.locator('[data-agent="codex"]')
  await expect(codex.locator('[data-checking]')).toBeVisible()
  await reading
  // The page is still open: only the read's deadline aborts it.
  await aborted
  await expect(codex.locator('[data-unanswered]')).toContainText('Check did not answer')
  await expect(codex.locator('[data-checking]')).toHaveCount(0)
})

// Reads leave with the page: leaving it aborts every read still open, and
// opening it again and leaving again never stacks them past two at a time.
test('the reads of a page that was left are aborted, never stacked past two', async ({ page }) => {
  await quickRead(page, ['claude', 'codex', 'goose', 'plain'], (a) => {
    a.checking = true
    delete a.identity
  })
  let open = 0
  let most = 0
  let asked = 0
  let release = () => {}
  const held = new Promise<void>((r) => (release = r))
  await page.route(
    (url) => !!agentRead(url),
    async (route) => {
      asked++
      most = Math.max(most, ++open)
      await held
      await route.continue().catch(() => {})
    },
  )
  page.on('requestfailed', (r) => {
    if (agentRead(new URL(r.url()))) open--
  })
  page.on('requestfinished', (r) => {
    if (agentRead(new URL(r.url()))) open--
  })
  await page.goto('/agents')
  for (let round = 1; round <= 3; round++) {
    await expect(page.locator('[data-agent="codex"] [data-checking]')).toBeVisible()
    await expect.poll(() => asked).toBe(2 * round)
    await expect.poll(() => open).toBe(2)
    await viaMore(page, 'History')
    await expect(page).toHaveURL(/\/history$/)
    // Left: the page's reads are aborted, none left open.
    await expect.poll(() => open).toBe(0)
    if (round < 3) await viaMore(page, 'Agents')
  }
  expect(most).toBe(2)
  release()
})

// Nothing the page started outlives it (Codex round 2): a hide still under
// way when the page is left answers to a page that is gone. It starts no read
// of the catalog (counted at the route) and changes nothing; the page opened
// again draws the agent hidden, as the server now holds it. OpenCode is a
// built-in no other spec uses, restored at the end.
test('a hide that answers after the page was left starts no read, and the page opened again shows it hidden', async ({ page, api }) => {
  let quick = 0
  await page.route(
    (url) => url.pathname === '/api/catalog' && url.searchParams.get('wait') === '0',
    async (route) => {
      quick++
      await route.continue()
    },
  )
  let release = () => {}
  const held = new Promise<void>((r) => (release = r))
  let asked = () => {}
  const hiding = new Promise<void>((r) => (asked = r))
  await page.route(
    (url) => url.pathname === '/api/catalog/opencode',
    async (route) => {
      if (route.request().method() !== 'DELETE') return route.fallback()
      asked()
      await held
      await route.continue()
    },
  )
  try {
    await page.goto('/agents')
    await page.locator('[data-agent="opencode"]').getByRole('button', { name: /^Hide / }).click()
    const dialog = page.getByRole('dialog')
    await dialog.getByRole('button', { name: 'Hide', exact: true }).click()
    await hiding
    await page.keyboard.press('Escape')
    await expect(dialog).toHaveCount(0)
    await viaMore(page, 'History')
    await expect(page).toHaveURL(/\/history$/)
    const before = quick
    const hidden = page.waitForResponse((r) => r.request().method() === 'DELETE' && new URL(r.url()).pathname === '/api/catalog/opencode')
    release()
    expect((await hidden).ok()).toBe(true)
    await viaMore(page, 'Agents')
    await expect(page.getByRole('button', { name: 'Restore opencode' })).toBeVisible()
    await expect(page.locator('[data-agent="opencode"]')).toHaveCount(0)
    // One read of the catalog since the hide was let go: the reopened page's.
    expect(quick).toBe(before + 1)
  } finally {
    await api.ok('POST', '/api/catalog/opencode/unhide')
  }
})
