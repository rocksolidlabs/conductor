import { expect, test } from './fixtures'

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
