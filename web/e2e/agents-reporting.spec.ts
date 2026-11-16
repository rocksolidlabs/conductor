import type { Locator, Page } from '@playwright/test'
import { budget, dayPage, expect, test, type Api } from './fixtures'
import { noOverlap, noSideways, probeColour, tapTarget, uncut } from './layout'
import { ownServer, type OwnServer } from './own-server'
import { START_WAIT_MS } from './server'

// The Agents page says on each card whether its agent reports to Conductor, by the integration of its adapter as GET
// /api/integrations gives it, and holds the section Reporting to Conductor (the hook adapters' table and the Conductor skill) that
// the Events page's Integrations tab held: a card that is not set up links its adapter's row, where Install on this machine is.

interface Integ {
  id: string
  installed: boolean
  launchInjection: boolean
  experimental: boolean
}

/** The key a card must carry for an adapter's integration, as the plan states the rule (`undefined`: no adapter, or none listed). */
function keyOf(it: Integ | undefined): string {
  if (!it) return 'none'
  if (it.installed || it.launchInjection) return 'reports'
  return it.experimental ? 'experimental' : 'unset'
}

/** Every catalog agent's id with the key its card must carry, in the catalog's order, read through the API. */
async function expectedKeys(api: Api): Promise<Array<{ id: string; adapter: string; key: string }>> {
  const [ints, cat] = await Promise.all([
    api.ok<{ integrations: Integ[] }>('GET', '/api/integrations'),
    api.ok<{ agents: Array<{ id: string; adapter?: string }> }>('GET', '/api/catalog?wait=0'),
  ])
  return cat.agents.map((a) => ({ id: a.id, adapter: a.adapter ?? '', key: keyOf(a.adapter ? ints.integrations.find((i) => i.id === a.adapter) : undefined) }))
}

const card = (page: Page, id: string) => page.locator(`[data-agent="${id}"]`)
const line = (page: Page, id: string) => card(page, id).locator('[data-agent-reports]')

/** The colour an element's background is drawn in, resolved as probeColour resolves a token. */
async function background(page: Page, l: Locator): Promise<string> {
  return probeColour(page, await l.evaluate((el) => getComputedStyle(el).backgroundColor))
}

const SIZES = [
  { width: 1440, height: 900, touch: false },
  { width: 390, height: 844, touch: true },
]

test('each card says whether its agent reports to Conductor as the server answers, every card of one adapter the same', async ({ browser, state, api }) => {
  const want = await expectedKeys(api)
  const named = ['claude', 'asker', 'brakeman-stub', 'goose', 'plain']
  for (const id of named) expect(want.find((w) => w.id === id), `the e2e catalog has ${id}`).toBeTruthy()
  // The cases the plan names: Claude Code's adapter injects its hooks at launch, and plain has no adapter.
  expect(want.find((w) => w.id === 'claude')!.key).toBe('reports')
  expect(want.find((w) => w.id === 'plain')!.key).toBe('none')
  for (const size of SIZES) {
    for (const theme of ['dark', 'light'] as const) {
      const at = `${size.width} px, ${theme}`
      const page = await dayPage(browser, { server: state, path: '/agents', width: size.width, height: size.height, touch: size.touch, theme })
      await expect(line(page, 'claude'), at).toBeVisible()
      for (const id of named) await expect(line(page, id), `${id} at ${at}`).toHaveAttribute('data-agent-reports', want.find((w) => w.id === id)!.key)
      // Every card of an adapter says what its adapter's integration says: one rule, read per adapter. Only the cards of agents the
      // test read are judged: another spec may add or remove an agent of its own meanwhile.
      const drawn = await page.locator('[data-agent] [data-agent-reports]').evaluateAll((els) =>
        els.map((el) => ({ id: el.closest('[data-agent]')!.getAttribute('data-agent')!, key: el.getAttribute('data-agent-reports')! })),
      )
      const shown = new Map(drawn.map((d) => [d.id, d.key]))
      const claudes = want.filter((w) => w.adapter === 'claude' && shown.has(w.id))
      expect(claudes.length, 'Claude Code serves many agents of the catalog').toBeGreaterThan(3)
      for (const w of want) if (shown.has(w.id)) expect(shown.get(w.id), `${w.id}'s card at ${at}`).toBe(w.key)
      expect(new Set(claudes.map((w) => shown.get(w.id))), `every card of Claude Code's adapter at ${at}`).toEqual(new Set([want.find((w) => w.id === 'claude')!.key]))
      // Drawn neutral: green is for a live agent, not for hooks that reach one (the card's dot and the table's).
      const green = await probeColour(page, 'var(--ui-success)')
      expect(await background(page, line(page, 'claude').locator('span').first()), `claude's card dot at ${at}`).not.toBe(green)
      const row = page.locator('[data-integration][data-id="claude"] [data-status]')
      expect(await background(page, row.locator('span').first()), `the claude row's dot at ${at}`).not.toBe(green)
      expect(await row.evaluate((el) => getComputedStyle(el).color).then((c) => probeColour(page, c)), `the claude row's words at ${at}`).not.toBe(green)
      // The header's new Refresh fits beside Add agent and Launch agent, on a phone as on a desktop.
      await noOverlap(page.locator('[data-agents-header]'))
      if (size.touch) await noSideways(page)
      await page.context().close()
    }
  }
})

test("a card that is not set up links its adapter's row, which comes into view; on a phone its line is whole and its link a 44 px target", async ({ browser, state, api }) => {
  // Chosen from the server's answer, never by name.
  const unset = (await expectedKeys(api)).find((w) => w.key === 'unset')
  expect(unset, 'an agent of the catalog whose adapter is not set up on the shared server').toBeTruthy()
  const { id, adapter } = unset!
  for (const size of SIZES) {
    const at = `${size.width} px`
    const page = await dayPage(browser, { server: state, path: '/agents', width: size.width, height: size.height, touch: size.touch })
    const l = line(page, id)
    await expect(l, at).toHaveAttribute('data-agent-reports', 'unset')
    await expect(l, at).toContainText('Not set up')
    const row = page.locator(`#reporting-${adapter}`)
    await expect(row, `${adapter}'s row at ${at}`).toHaveAttribute('data-integration', '')
    // Below the cards, out of view before the press: what follows is the link's doing.
    await expect(row, `${adapter}'s row is not in view at first, at ${at}`).not.toBeInViewport()
    const link = l.locator('[data-agent-setup]')
    await expect(link).toHaveText('Set up')
    await l.scrollIntoViewIfNeeded()
    if (size.touch) {
      await uncut(l)
      await tapTarget(link)
      await link.tap()
    } else {
      await link.click()
    }
    await expect(page, at).toHaveURL(new RegExp(`/agents#reporting-${adapter}$`))
    await expect(row, `${adapter}'s row in view at ${at}`).toBeInViewport({ ratio: 1 })
    // Its name whole, not cut by the page's scroller or the window.
    await uncut(row.locator('.truncate').first())
    // Install on this machine sits in the row, beside the file it writes.
    await expect(row.getByRole('button', { name: 'Install on this machine' }), at).toBeVisible()
    await page.context().close()
  }
})

test('the section lists every adapter with its status, opens a snippet and shows the skill; an old link to the Integrations tab lands on it', async ({ browser, state }) => {
  // Moved from events.spec.ts with the table: the Integrations tab's test.
  const page = await dayPage(browser, { server: state, path: '/agents#reporting' })
  const section = page.locator('#reporting')
  const claude = section.locator('[data-integration][data-id="claude"]')
  await expect(claude).toBeVisible()
  await expect(section).toBeInViewport()
  await expect(claude.locator('[data-status]')).not.toBeEmpty()
  await claude.locator('[data-snippet-toggle]').click()
  await expect(section.locator('[data-snippet-for="claude"] [data-snippet]')).toBeVisible()
  await expect(section.locator('[data-skill-card]')).toBeVisible()
  // An old link to the Events page's Integrations tab opens the section, in place of the page in the history.
  await page.goto('/events?tab=integrations')
  await expect(page).toHaveURL(/\/agents#reporting$/)
  await expect(page.locator('#reporting [data-integration][data-id="claude"]')).toBeInViewport()
  // History's Feed (the retired Events page's feed) has the Reporting list beside it, which links here.
  await page.goto('/history?view=feed')
  await expect(page.locator('[data-event-feed]')).toBeVisible()
  await page.locator('[data-reporting-link]').click()
  await expect(page).toHaveURL(/\/agents#reporting$/)
  await expect(page.locator('#reporting [data-integration]').first()).toBeInViewport()
})

test("the palette's Agents · Reporting row opens the section, from another page and from the top of Agents itself", async ({ browser, state }) => {
  const page = await dayPage(browser, { server: state, path: '/history?view=feed' })
  const go = async () => {
    // The layout's keys: pressed once its sidebar is drawn.
    await expect(page.locator('#dashboard-sidebar-main')).toBeVisible()
    await page.keyboard.press('Alt+/')
    const palette = page.locator('[data-palette]')
    await expect(palette).toBeVisible()
    await palette.locator('input[data-palette-input]').fill('Reporting')
    const row = palette.locator('[data-palette-row="go:agents-reporting"]')
    await expect(row).toHaveText(/Agents · Reporting/)
    await row.click()
    await expect(palette).toBeHidden()
  }
  const title = page.locator('#reporting-title')
  await go()
  await expect(page).toHaveURL(/\/agents#reporting$/)
  await expect(title).toBeInViewport()
  // On Agents at its top, with the section below the cards: the row moves the page to it.
  await page.goto('/agents')
  await expect(page.locator('#reporting [data-integration]').first()).toBeAttached()
  await expect(title).not.toBeInViewport()
  await go()
  await expect(page).toHaveURL(/\/agents#reporting$/)
  await expect(title).toBeInViewport()
})

test.describe('on a server of its own', () => {
  let srv: OwnServer | undefined
  test.afterAll(async () => {
    await srv?.stop()
  })

  test("one install of an adapter moves every card of it to Reports to Conductor; Claude Code's reports with nothing installed", async ({ browser }) => {
    budget(START_WAIT_MS)
    // An install writes into the server's home: on the shared server it would change other specs' answers.
    srv = await ownServer('agents-reporting', { agents: [{ id: 'goose-two', from: 'goose', env: {} }] })
    const before = await expectedKeys(srv.api)
    // Goose installs and is not injected at launch: both its cards are not set up.
    expect(before.filter((w) => w.adapter === 'goose').map((w) => [w.id, w.key])).toEqual([
      ['goose', 'unset'],
      ['goose-two', 'unset'],
    ])
    const claudeInt = (await srv.api.ok<{ integrations: Integ[] }>('GET', '/api/integrations')).integrations.find((i) => i.id === 'claude')!
    expect(claudeInt.installed, 'nothing of Claude Code is installed in the fresh home').toBe(false)
    const page = await dayPage(browser, { server: srv.started, path: '/agents' })
    await expect(line(page, 'goose')).toHaveAttribute('data-agent-reports', 'unset')
    await expect(line(page, 'goose-two')).toHaveAttribute('data-agent-reports', 'unset')
    await expect(line(page, 'claude')).toHaveAttribute('data-agent-reports', 'reports')
    await expect(line(page, 'claude')).toContainText('Reports to Conductor')
    // The install's answer, then the integrations' read after it.
    let installAnswered = false
    const install = page.waitForResponse((r) => {
      const hit = r.request().method() === 'POST' && new URL(r.url()).pathname === '/api/integrations/goose/install'
      if (hit) installAnswered = true
      return hit
    })
    const reread = page.waitForResponse((r) => installAnswered && r.request().method() === 'GET' && new URL(r.url()).pathname === '/api/integrations')
    await page.locator('#reporting-goose').getByRole('button', { name: 'Install on this machine' }).click()
    const answer = await install
    expect(answer.status()).toBe(200)
    expect(((await answer.json()) as { changed: string[] }).changed.length).toBeGreaterThan(0)
    await reread
    for (const id of ['goose', 'goose-two']) {
      await expect(line(page, id), id).toHaveAttribute('data-agent-reports', 'reports')
      await expect(line(page, id), id).toContainText('Reports to Conductor')
      await expect(line(page, id).locator('[data-agent-setup]'), id).toHaveCount(0)
    }
    await page.context().close()
  })
})
