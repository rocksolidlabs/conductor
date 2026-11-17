import type { Locator, Page } from '@playwright/test'
import { budget, dayPage, expect, test, useTheme } from './fixtures'
import { inside, noSideways, tabTo, tapTarget, uncut } from './layout'
import { ownServer, type OwnServer } from './own-server'
import { START_WAIT_MS } from './server'

// The first screens of a new install (the UX review's P17; #906 before it): the welcome says what Conductor is in one sentence, then
// three steps (the agents this machine has, each with its version; another machine's terminal brought in; the example crews), then
// "Where things are", each page of the nav with the sentence its tooltip says. Every choice says what it does, here and where the
// examples are offered elsewhere (Crews, the palette). The token dialog's words are held in connect.spec.ts.

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

// The tooltips of the welcome's buttons and of Crews' Load the examples (#374): each says what pressing it does, in the words of
// utils/welcome.ts, written out here so that a change of them is seen.
const EXAMPLES_TIP = 'Adds five ready-made crews to Crews; nothing starts until you launch one.'
const TIPS = {
  agent: (name: string) => `Opens the Launch dialog with ${name} picked; nothing starts until you launch.`,
  more: 'Opens the Launch dialog, to pick one of the other agents this machine has.',
  agents: 'Opens Agents: the agents Conductor knows, and which of them this machine has.',
  launch: 'Opens the Launch dialog, which looks for the agents on this machine again.',
  host: 'Opens the Quick start in a new tab: how to run conductor host on another machine.',
  examples: EXAMPLES_TIP,
}

/** Any tooltip drawn (reka keeps the role on a copy of the words for the button's description, hidden from the tree). */
function anyTip(page: Page): Locator {
  return page.getByRole('tooltip', { includeHidden: true })
}

/** The box drawn around the tooltip whose words are `text`. */
function tipBox(page: Page, text: string): Locator {
  return page.locator('[data-slot="content"]').filter({ has: page.getByRole('tooltip', { name: text, exact: true, includeHidden: true }) })
}

/** The page has drawn what a focus it just took asked for: a tooltip opens in the focus's own handler, drawn at the next render. */
async function drawnAfterFocus(page: Page): Promise<void> {
  await page.evaluate(() => new Promise<void>((r) => requestAnimationFrame(() => requestAnimationFrame(() => r()))))
}

/** `button`'s tooltip is open, says `tip` whole (wrapped inside its box, none of it cut), and is the button's description. */
async function tipShown(page: Page, button: Locator, tip: string, at: string) {
  const box = tipBox(page, tip)
  await expect(box, `${at}: the tooltip "${tip}"`).toBeVisible()
  await uncut(box.locator('[data-slot="text"]'))
  await expect(button, at).toHaveAccessibleDescription(tip)
}

/** The pointer away from every button, and every tooltip closed and gone (one closing is drawn through its animation). */
async function noTip(page: Page) {
  await page.mouse.move(1, 1)
  await expect(anyTip(page)).toHaveCount(0)
}

/**
 * The buttons of `controls`, in the order Tab walks them, each with its label (its accessible name, which a tooltip never changes)
 * and its tooltip: hovered, each shows its tooltip; reached by Tab from the one before, each shows it too (the first by Shift+Tab from
 * the second).
 */
async function tipsOnHoverAndTab(page: Page, controls: { button: Locator; name: string | RegExp; tip: string }[], at: string) {
  for (const c of controls) {
    await expect(c.button, at).toHaveAccessibleName(c.name)
    await c.button.hover()
    await tipShown(page, c.button, c.tip, `${at}, hovered`)
    await noTip(page)
  }
  if (controls.length < 2) throw new Error('tipsOnHoverAndTab: give two controls or more, the first reached from the second')
  for (const [i, c] of controls.entries()) {
    if (i === 0) continue
    await tabTo(page, c.button, 3, controls[i - 1]!.button)
    await tipShown(page, c.button, c.tip, `${at}, by Tab`)
    if (i === 1) {
      await page.keyboard.press('Shift+Tab')
      await expect(controls[0]!.button).toBeFocused()
      await tipShown(page, controls[0]!.button, controls[0]!.tip, `${at}, by Shift+Tab`)
      await page.keyboard.press('Tab')
      await expect(c.button).toBeFocused()
    }
  }
  // Off the card, nothing is left open.
  await page.locator('[data-welcome-sentence], [data-page-why]').first().click()
  await noTip(page)
}

/** The welcome's buttons as the card draws them with agents found: each agent, "and N more" when there are more, the host's, the examples'. */
async function welcomeButtons(card: Locator) {
  const out: { button: Locator; name: string | RegExp; tip: string }[] = []
  const agents = card.locator('[data-welcome-agent]')
  for (let i = 0; i < (await agents.count()); i++) {
    const name = (await agents.nth(i).locator('[data-welcome-agent-name]').textContent())!.trim()
    out.push({ button: agents.nth(i), name: new RegExp(`^${name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}( |$)`), tip: TIPS.agent(name) })
  }
  const more = card.locator('[data-welcome-more]')
  if (await more.count()) out.push({ button: more, name: (await more.textContent())!.trim(), tip: TIPS.more })
  out.push({ button: card.locator('[data-welcome-host]'), name: 'How to bring one in', tip: TIPS.host })
  out.push({ button: card.locator('[data-welcome-examples]'), name: 'Load the example crews', tip: TIPS.examples })
  return out
}

test("every button of the welcome says what it does in a tooltip, on hover and on Tab, at the desktop app's width in both themes", async ({ browser }) => {
  const srv = await empty()
  budget(2 * 40 * STEP_MS)
  for (const theme of ['dark', 'light'] as const) {
    const page = await dayPage(browser, { width: 1440, height: 900, theme, path: '/', server: srv.started })
    const card = await settled(page)
    await expect(anyTip(page)).toHaveCount(0)
    const buttons = await welcomeButtons(card)
    expect(buttons.length, 'an agent at least, with the host and the examples').toBeGreaterThanOrEqual(3)
    await tipsOnHoverAndTab(page, buttons, `${theme}`)
    await page.context().close()
  }
})

test("a tooltip closes as the pointer leaves its button: a press on the agent below one whose tooltip was open opens that agent's Launch dialog", async ({ browser }) => {
  const srv = await empty()
  budget(8 * STEP_MS)
  const page = await dayPage(browser, { width: 1440, height: 900, path: '/', server: srv.started })
  const card = await settled(page)
  const agents = card.locator('[data-welcome-agent]')
  expect(await agents.count(), 'the e2e catalog offers two agents or more').toBeGreaterThanOrEqual(2)
  const [first, second] = [agents.nth(0), agents.nth(1)]
  const name = (await second.locator('[data-welcome-agent-name]').textContent())!.trim()
  await first.hover()
  await tipShown(page, first, TIPS.agent((await first.locator('[data-welcome-agent-name]').textContent())!.trim()), 'the first agent, hovered')
  // Straight down onto the middle of the next agent, where the first's tooltip lies while it is open, and pressed there: the mouse
  // itself, so that a press that lands on the tooltip fails here rather than waiting on the locator's own check.
  const box = (await second.boundingBox())!
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2, { steps: 5 })
  await page.mouse.down()
  await page.mouse.up()
  const dialog = page.getByRole('dialog')
  await expect(dialog, 'the press reached the second agent').toBeVisible()
  await expect(dialog.getByRole('radio', { checked: true })).toContainText(name)
})

test('the welcome with no agent found offers Agents, and with the agents not read offers Launch agent, each saying what it does', async ({ browser }) => {
  const srv = await empty()
  budget(2 * 20 * STEP_MS)
  for (const [state, button, name, tip] of [
    ['none', '[data-welcome-agents]', 'Agents', TIPS.agents],
    ['unread', '[data-welcome-launch]', 'Launch agent', TIPS.launch],
  ] as const) {
    const page = await dayPage(browser, { width: 1440, height: 900, path: '/', server: srv.started })
    // The catalog's quick answer as this state has it, from the first load on.
    await page.route(
      (u) => u.pathname === '/api/catalog',
      (r) => (state === 'none' ? r.fulfill({ json: { agents: [] } }) : r.fulfill({ status: 500, json: { error: 'catalog_failed', message: 'the catalog could not be read' } })),
    )
    await page.reload()
    const card = page.locator('[data-welcome-card]')
    await expect(card.locator(button), state).toBeVisible()
    await tipsOnHoverAndTab(
      page,
      [
        { button: card.locator(button), name, tip },
        { button: card.locator('[data-welcome-host]'), name: 'How to bring one in', tip: TIPS.host },
      ],
      state,
    )
    await page.context().close()
  }
})

test("on a phone the welcome's buttons keep their labels as their names, and a tap opens no tooltip, then or when the dialog it opened closes", async ({ browser }) => {
  const srv = await empty()
  budget(12 * STEP_MS)
  const page = await dayPage(browser, { width: 390, height: 844, touch: true, path: '/sessions', server: srv.started })
  const card = await settled(page)
  for (const c of await welcomeButtons(card)) await expect(c.button).toHaveAccessibleName(c.name)
  const first = card.locator('[data-welcome-agent]').first()
  await first.scrollIntoViewIfNeeded()
  await first.tap()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()
  await drawnAfterFocus(page)
  await expect(anyTip(page)).toHaveCount(0)
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)
  // The dialog hands the focus back to the button: the app's focus, on a touch screen, opens no tooltip.
  await expect(first).toBeFocused()
  await drawnAfterFocus(page)
  await expect(anyTip(page)).toHaveCount(0)
})

test("Crews' Load the examples says what it does in a tooltip on hover and on Tab, and on a phone a tap opens none", async ({ browser }) => {
  const srv = await empty()
  budget(16 * STEP_MS)
  const page = await dayPage(browser, { width: 1440, height: 900, path: '/crews', server: srv.started })
  const crewsEmpty = page.locator('[data-crews-empty]')
  const load = crewsEmpty.getByRole('button', { name: 'Load the examples', exact: true })
  await expect(load).toBeVisible()
  await expect(anyTip(page)).toHaveCount(0)
  await load.hover()
  await tipShown(page, load, EXAMPLES_TIP, 'Crews, hovered')
  await noTip(page)
  // Tab from New crew, the action before it, brings the tooltip too.
  await tabTo(page, load, 3, crewsEmpty.getByRole('button', { name: 'New crew', exact: true }))
  await tipShown(page, load, EXAMPLES_TIP, 'Crews, by Tab')
  await page.context().close()

  // On a phone: the label is the name, and a tap opens no tooltip. The load is answered with a refusal, so that the server's crews stay
  // as they are for the other cases.
  const phone = await dayPage(browser, { width: 390, height: 844, touch: true, path: '/crews', server: srv.started })
  await phone.route(
    (u) => u.pathname === '/api/crews/examples',
    (r) => r.fulfill({ status: 503, json: { error: 'unavailable', message: 'not loaded in this test' } }),
  )
  const tapped = phone.locator('[data-crews-empty]').getByRole('button', { name: 'Load the examples', exact: true })
  await expect(tapped).toBeVisible()
  await tapped.tap()
  await expect(phone.getByText('Loading the examples failed', { exact: true })).toBeVisible()
  await drawnAfterFocus(phone)
  await expect(anyTip(phone)).toHaveCount(0)
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
