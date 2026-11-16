import type { Locator, Page } from '@playwright/test'
import { budget, dayPage, expect, test, useTheme } from './fixtures'
import { inside, noSideways, tapTarget, uncut } from './layout'
import { ownServer, type OwnServer } from './own-server'
import { START_WAIT_MS } from './server'

// The first screens of a new install (the UX review's P17; #906 before it): the welcome says what Conductor is in one sentence, then
// three steps (the agents this machine has, each with its version; another machine's terminal brought in; the example crews), then
// "Where things are", each page of the nav with the sentence its tooltip says. Every choice says what it does, here and where the
// examples are offered elsewhere (Crews, the palette). And the workbench token's dialog, which says where the token is as the server
// says it.

/** An expect's bound and an action's (the config's expect timeout). */
const STEP_MS = 15_000

const SENTENCE = 'Conductor runs your coding agents in terminals on this machine, and lets you watch, answer and share them from any browser.'
const STEPS = ['Launch an agent', 'Bring one in from another machine', 'Try a crew']

/** An agent of GET /api/catalog, the fields this spec reads. */
interface CatalogAgent {
  id: string
  name: string
  available?: boolean
  checking?: boolean
  identity?: { identified?: boolean; version?: string }
}

// A server with no session at all, shared by the cases that only read it; the case that loads the examples starts one of its own,
// so that the crews it adds reach no other case.
let emptyOne: OwnServer | undefined
async function empty(): Promise<OwnServer> {
  if (!emptyOne) {
    budget(START_WAIT_MS)
    emptyOne = await ownServer('welcome')
  }
  return emptyOne
}
test.afterAll(async () => {
  await emptyOne?.stop()
  emptyOne = undefined
})

/** The catalog once the server has checked every agent: GET /api/catalog waits for the checks (about 4 s at most), read until none is still checking. */
async function settledCatalog(srv: OwnServer): Promise<CatalogAgent[]> {
  let agents: CatalogAgent[] = []
  await expect
    .poll(
      async () => {
        agents = (await srv.api.ok<{ agents: CatalogAgent[] }>('GET', '/api/catalog')).agents
        return agents.filter((a) => a.checking).map((a) => a.id)
      },
      { message: 'every agent checked', timeout: 30_000 },
    )
    .toEqual([])
  return agents
}

/** The welcome drawn with its agents settled: nothing still looked for, no agent still checked. */
async function settled(page: Page): Promise<Locator> {
  const card = page.locator('[data-welcome-card]')
  await expect(card).toBeVisible()
  await expect(card.locator('[data-welcome-looking]')).toHaveCount(0)
  await expect(card.locator('[data-welcome-checking]')).toHaveCount(0)
  return card
}

/** Each match of `parts`, scrolled into view in its list, whole and inside `card`: a card taller than the window is read part by part. */
async function wholeIn(parts: Locator, card: Locator, at: string) {
  const n = await parts.count()
  expect(n, `${at}: ${parts} drawn`).toBeGreaterThan(0)
  for (let i = 0; i < n; i++) {
    const part = parts.nth(i)
    await part.scrollIntoViewIfNeeded()
    await uncut(part)
    await inside(part, card)
  }
}

/**
 * The welcome read whole at the window's size: the sentence, the three steps by their labels and "Where things are"; each step's label
 * and each agent's name whole and inside the card; nothing scrolls sideways; under touch each control is big enough to tap.
 */
async function welcomeWhole(page: Page, at: string, touch = false) {
  const card = await settled(page)
  await expect(card.locator('[data-welcome-sentence]'), at).toHaveText(SENTENCE)
  await expect(card.locator('[data-welcome-step-label]'), at).toHaveText(STEPS)
  await expect(card.locator('[data-welcome-where] h3'), at).toHaveText('Where things are')
  await wholeIn(card.locator('[data-welcome-step-label]'), card, at)
  await wholeIn(card.locator('[data-welcome-agent-name]'), card, at)
  await wholeIn(card.locator('[data-welcome-agent], [data-welcome-examples], [data-welcome-host]'), card, at)
  await noSideways(page)
  if (touch) {
    const controls = card.locator('button, a')
    for (let i = 0; i < (await controls.count()); i++) {
      await controls.nth(i).scrollIntoViewIfNeeded()
      await tapTarget(controls.nth(i))
    }
  }
}

test('the welcome shows the sentence, three steps and Where things are, each whole, at 1440 and 1024 wide and in both themes, and again as the window changes', async ({ browser }) => {
  const srv = await empty()
  budget(3 * 12 * STEP_MS)
  const page = await dayPage(browser, { width: 1440, height: 900, path: '/', server: srv.started })
  await welcomeWhole(page, 'at 1440 × 900, dark')
  // The theme switched as the app keeps it: the card drawn again, every part read again after that redraw.
  await useTheme(page, 'light')
  await welcomeWhole(page, 'at 1440 × 900, light')
  // The window resized: the card is drawn again at the new width, and its parts still read whole.
  await page.setViewportSize({ width: 1024, height: 768 })
  await expect.poll(() => page.evaluate(() => window.innerWidth)).toBe(1024)
  await welcomeWhole(page, 'resized to 1024 × 768')
})

test("a phone's Sessions page on a server with no session shows the welcome in place of the list's empty line, each control a target to tap", async ({ browser }) => {
  const srv = await empty()
  budget(2 * 20 * STEP_MS)
  for (const theme of ['dark', 'light'] as const) {
    const page = await dayPage(browser, { width: 390, height: 844, touch: true, theme, path: '/sessions', server: srv.started })
    await welcomeWhole(page, `at 390 × 844, ${theme}`, true)
    await expect(page.locator('[data-sidebar-empty]')).toHaveCount(0)
    await page.context().close()
  }
})

test("step 1 offers the first four agents the server can launch, in the catalog's order with their versions, and the rest in one line", async ({ browser }) => {
  const srv = await empty()
  budget(8 * STEP_MS)
  const catalog = await settledCatalog(srv)
  const found = catalog.filter((a) => a.available !== false)
  const first = found.slice(0, 4)
  expect(first.length, 'the e2e catalog has agents the server can launch').toBeGreaterThan(0)

  const page = await dayPage(browser, { width: 1440, height: 900, path: '/', server: srv.started })
  const card = await settled(page)
  const buttons = card.locator('[data-welcome-agent]')
  await expect(buttons).toHaveCount(first.length)
  expect(await buttons.evaluateAll((els) => els.map((e) => e.getAttribute('data-welcome-agent')))).toEqual(first.map((a) => a.id))
  for (const [i, a] of first.entries()) {
    await expect(buttons.nth(i).locator('[data-welcome-agent-name]')).toHaveText(a.name)
    const version = a.identity?.identified ? a.identity.version : undefined
    if (version) await expect(buttons.nth(i).locator('[data-welcome-version]'), `${a.id}'s version`).toHaveText(version)
    else await expect(buttons.nth(i).locator('[data-welcome-version]'), `${a.id} has no version`).toHaveCount(0)
  }
  const rest = found.length - first.length
  if (rest) await expect(card.locator('[data-welcome-more]')).toHaveText(`and ${rest} more`)
  else await expect(card.locator('[data-welcome-more]')).toHaveCount(0)

  // The first agent's button opens the Launch dialog with that agent picked.
  await buttons.first().click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()
  // The radio holds the agent's avatar beside its name.
  await expect(dialog.getByRole('radio', { checked: true })).toContainText(first[0]!.name)
})

test("Where things are lists the sidebar's pages, its tiles in order then those under More, each tile's line the sentence its tooltip says", async ({ browser }) => {
  const srv = await empty()
  budget(24 * STEP_MS)
  const page = await dayPage(browser, { width: 1440, height: 900, path: '/', server: srv.started })
  const card = await settled(page)
  const where = card.locator('[data-where-page]')
  const labels = await where.locator('[data-where-label]').allTextContents()
  const abouts = await where.locator('[data-where-about]').allTextContents()
  const sidebar = page.locator('#dashboard-sidebar-main')
  // Brakeman's slot is held until the server has said whether it has Brakeman: the tiles are read once it has.
  await expect(sidebar.locator('[data-sidebar-tile="held"]')).toHaveCount(0)
  const tiles = sidebar.locator('[data-sidebar-tiles] [data-sidebar-tile]:not([data-sidebar-tile="more"])')
  const tileLabels = await tiles.locator('[data-nav-label]').allTextContents()
  // The pages under More, as its menu lists them.
  await sidebar.locator('[data-sidebar-tile="more"]').click()
  const menu = page.getByRole('menu')
  await expect(menu).toBeVisible()
  const moreLabels = (await menu.getByRole('menuitem').locator('[data-slot="itemLabel"]').allTextContents()).map((t) => t.trim())
  await page.keyboard.press('Escape')
  await expect(menu).toHaveCount(0)
  expect(tileLabels.length).toBeGreaterThan(0)
  expect(labels, 'the tiles, then More').toEqual([...tileLabels, ...moreLabels])
  // Each tile's line is its tooltip, read from the page: hovered, the tile says "Label: its sentence".
  const tipText = page.locator('[data-slot="content"] [data-slot="text"]')
  for (const [i, label] of tileLabels.entries()) {
    const about = abouts[i]!.trim().replace(/\.$/, '')
    await tiles.nth(i).hover()
    await expect(tipText, `${label}'s tooltip`).toHaveText(`${label}: ${about.charAt(0).toLowerCase()}${about.slice(1)}`)
    // Away from the tiles, the tooltip closed and gone before the next is hovered: one open at a time.
    await page.mouse.move(1000, 450)
    await expect(tipText, `${label}'s tooltip closed`).toHaveCount(0)
  }
})

test('the examples say what loading them does, on the welcome, on Crews and in the palette, and the welcome loads them onto Crews', async ({ browser }) => {
  budget(START_WAIT_MS + 16 * STEP_MS)
  const srv = await ownServer('welcome-examples')
  try {
    const page = await dayPage(browser, { width: 1440, height: 900, path: '/', server: srv.started })
    const card = await settled(page)
    const line = (await card.locator('[data-examples-line]').textContent())!.trim()
    expect(line).not.toBe('')

    // Crews with nothing on it: the same words under its Load the examples.
    await page.goto('/crews')
    const crewsEmpty = page.locator('[data-crews-empty]')
    await expect(crewsEmpty.getByRole('button', { name: 'Load the examples' })).toBeVisible()
    await expect(crewsEmpty.locator('[data-examples-line]')).toHaveText(line)
    // The palette: on the Crews page its own group's row, and from the home the runs-and-crews mode's (#) row, say them as their detail.
    const palette = page.locator('[data-palette]')
    const paletteSays = async (key: string, query: string) => {
      await page.keyboard.press('Alt+/')
      await expect(palette).toBeVisible()
      const input = palette.locator('input[data-palette-input]')
      await input.fill(query)
      await expect(input).toHaveValue(query)
      await expect(palette.locator(`[data-palette-row="${key}"] [data-palette-detail]`), key).toHaveText(line)
      await page.keyboard.press('Escape')
      await expect(palette).toHaveCount(0)
    }
    await paletteSays('page:examples', 'example')
    await page.goto('/')
    await settled(page)
    await paletteSays('crews:examples', '#example')

    // The welcome's Load the example crews lands on Crews, listing them.
    await page.goto('/')
    await (await settled(page)).locator('[data-welcome-examples]').click()
    await expect(page).toHaveURL(/\/crews$/)
    await expect(page.locator('[data-crew-entry][data-crew-id="example-todo-app"] [data-crew-item]')).toBeVisible()
  } finally {
    await srv.stop()
  }
})

test('the token dialog says where the server keeps a token it made: it prints the file\'s path, never the token', async ({ browser }) => {
  // A browser with no token: the page asks for it.
  const ctx = await browser.newContext()
  try {
    const page = await ctx.newPage()
    await page.goto('/')
    const gate = page.getByRole('dialog', { name: 'Workbench token' })
    await expect(gate).toBeVisible()
    await expect(gate).toContainText('CONDUCTOR_WORKBENCH_TOKEN')
    await expect(gate).toContainText('in the file workbench-token in its data directory: it prints that file\'s path as it starts, never the token.')
    await expect(gate).not.toContainText('printed at server start')
  } finally {
    await ctx.close()
  }
})
