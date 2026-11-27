import type { Locator, Page } from '@playwright/test'
import { budget, DAILY, dayPage, expect, test, useTheme } from './fixtures'
import { BOX_SLACK, inside, noSideways, tabTo, tapTarget, uncut } from './layout'
import { ownServer, type OwnServer } from './own-server'
import { START_WAIT_MS } from './server'

// The first screens of a new install (the UX review's P17; #906 before it; UX3): the welcome says what Conductor is in one sentence,
// then one main button, Launch <the first agent this machine has>, the other agents on one line (Or: ...), and the other ways to start
// folded (another machine's terminal brought in; the example crews). Every choice says what it does in its tooltip, here and where the
// examples are offered elsewhere (Crews, the palette). The token dialog's words are held in connect.spec.ts.

/** An expect's bound and an action's (the config's expect timeout). */
const STEP_MS = 15_000

const SENTENCE = 'Run your coding agents here, and answer them from anywhere.'
/** The built-in shell: on the Or line, never the main button. */
const SHELL = 'shell'

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

/** The welcome drawn with its agents settled: nothing still looked for. */
async function settled(page: Page): Promise<Locator> {
  const card = page.locator('[data-welcome-card]')
  await expect(card).toBeVisible()
  await expect(card.locator('[data-welcome-looking]')).toHaveCount(0)
  return card
}

/** The other ways to start unfolded (pressed only while folded) and drawn: their body, `data-welcome-fold`. */
async function unfold(card: Locator): Promise<Locator> {
  const row = card.locator('[data-welcome-expander]')
  if ((await row.getAttribute('aria-expanded')) !== 'true') await row.click()
  await expect(row).toHaveAttribute('aria-expanded', 'true')
  const body = card.locator('[data-welcome-fold]')
  await expect(body).toBeVisible()
  return body
}

/** What the welcome offers, from a catalog the server has settled, by the card's rule: the main agent, the names on the Or line, the count of the rest. */
function offerOf(catalog: CatalogAgent[]): { main: CatalogAgent; named: CatalogAgent[]; rest: number } {
  const found = catalog.filter((a) => a.available !== false)
  const main = found.find((a) => a.id !== SHELL)
  if (!main) throw new Error('the e2e catalog has an agent the server can launch')
  const others = found.filter((a) => a.id !== main.id)
  const named = others.length <= 3 ? others : others.slice(0, 2)
  return { main, named, rest: others.length - named.length }
}

/** The box of `l` as drawn, inside the window: none of it below, above or beside what the window shows. */
async function inWindow(page: Page, l: Locator, at: string) {
  const box = (await l.boundingBox())!
  const view = page.viewportSize()!
  expect(box.y, `${at}: its top in the window`).toBeGreaterThanOrEqual(-BOX_SLACK)
  expect(box.x, `${at}: its left in the window`).toBeGreaterThanOrEqual(-BOX_SLACK)
  expect(box.y + box.height, `${at}: its foot in the window`).toBeLessThanOrEqual(view.height + BOX_SLACK)
  expect(box.x + box.width, `${at}: its right in the window`).toBeLessThanOrEqual(view.width + BOX_SLACK)
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
 * The welcome read whole at the window's size: the title and the sentence, the main button and each name of the Or line whole and inside
 * the card, then unfolded, the other ways to start whole too; nothing scrolls sideways; under touch each control is big enough to tap.
 */
async function welcomeWhole(page: Page, at: string, touch = false) {
  const card = await settled(page)
  await expect(card.locator('[data-welcome-title]'), at).toHaveText('Welcome to Conductor')
  await expect(card.locator('[data-welcome-sentence]'), at).toHaveText(SENTENCE)
  // Conductor is used in its apps, not in a browser: the word is nowhere on the card. The steps and Where things are left it.
  await expect(card, at).not.toContainText(/browser/i)
  await expect(card.locator('[data-welcome-step-label], [data-welcome-where], [data-welcome-version]'), at).toHaveCount(0)
  await expect(card.locator('[data-welcome-primary]'), at).toHaveText(/^Launch \S/)
  await wholeIn(card.locator('[data-welcome-agent], [data-welcome-more], [data-welcome-expander]'), card, at)
  await unfold(card)
  await wholeIn(card.locator('[data-welcome-fold] h3, [data-welcome-examples], [data-welcome-host]'), card, at)
  await noSideways(page)
  if (touch) {
    const controls = card.locator('button, a')
    for (let i = 0; i < (await controls.count()); i++) {
      await controls.nth(i).scrollIntoViewIfNeeded()
      await tapTarget(controls.nth(i))
    }
  }
}

test('the welcome shows the sentence, the main button, the Or line and the other ways to start, each whole, at 1440 and 1024 wide and in both themes, and again as the window changes', async ({ browser }) => {
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

test("the main button names the first agent the server can launch that is not the shell, the Or line the others, and each opens the dialog with its agent picked", async ({ browser }) => {
  const srv = await empty()
  budget(16 * STEP_MS)
  const { main, named, rest } = offerOf(await settledCatalog(srv))
  expect(named.length, 'the e2e catalog has other agents the server can launch').toBeGreaterThan(0)

  const page = await dayPage(browser, { width: 1440, height: 900, path: '/', server: srv.started })
  const card = await settled(page)
  const primary = card.locator('[data-welcome-primary]')
  await expect(primary).toHaveText(`Launch ${main.name}`)
  await expect(primary).toHaveAttribute('data-welcome-agent', main.id)
  const or = card.locator('[data-welcome-or]')
  expect(await or.locator('[data-welcome-agent]').evaluateAll((els) => els.map((e) => e.getAttribute('data-welcome-agent')))).toEqual(named.map((a) => a.id))
  if (rest) {
    await expect(or.locator('[data-welcome-more]')).toHaveText(`${rest} more`)
    await expect(or.locator('[data-welcome-more]')).toHaveAccessibleName(`${rest} more agents`)
  } else await expect(or.locator('[data-welcome-more]')).toHaveCount(0)

  // Each opens the Launch dialog with its own agent picked: the main button, then each name of the Or line, found by its name.
  const dialog = page.getByRole('dialog')
  const opens = async (button: Locator, name: string) => {
    await button.click()
    await expect(dialog, `${name}: the dialog`).toBeVisible()
    // The radio holds the agent's avatar beside its name.
    await expect(dialog.getByRole('radio', { checked: true }), `${name} picked`).toContainText(name)
    await page.keyboard.press('Escape')
    await expect(dialog).toHaveCount(0)
  }
  await opens(primary, main.name)
  for (const a of named) await opens(or.getByRole('button', { name: a.name, exact: true }), a.name)
  if (rest) {
    await or.locator('[data-welcome-more]').click()
    await expect(dialog, 'N more: the dialog').toBeVisible()
  }
})

test("the card keeps its place: folded and unfolded it is inside the window at the daily window and at 1440, and a press on Other ways to start moves neither the title nor the row", async ({ browser }) => {
  const srv = await empty()
  budget(2 * 12 * STEP_MS)
  for (const size of [DAILY, { width: 1440, height: 900 }]) {
    const at = `at ${size.width} × ${size.height}`
    const page = await dayPage(browser, { ...size, path: '/', server: srv.started })
    const card = await settled(page)
    const title = card.locator('[data-welcome-title]')
    const row = card.locator('[data-welcome-expander]')
    await expect(row).toHaveAttribute('aria-expanded', 'false')
    await expect(card.locator('[data-welcome-fold]')).toHaveCount(0)
    await inWindow(page, card, `${at}, folded`)
    const before = [await title.boundingBox(), await row.boundingBox()]
    await unfold(card)
    await inWindow(page, card, `${at}, unfolded`)
    const after = [await title.boundingBox(), await row.boundingBox()]
    for (const [i, what] of ['the title', 'the row'].entries()) {
      for (const k of ['x', 'y', 'width', 'height'] as const) expect(Math.abs(after[i]![k] - before[i]![k]), `${at}: ${what}'s ${k}`).toBeLessThanOrEqual(BOX_SLACK)
    }
    await noSideways(page)
    // A second press folds it again.
    await row.click()
    await expect(row).toHaveAttribute('aria-expanded', 'false')
    await expect(card.locator('[data-welcome-fold]')).toHaveCount(0)
    await page.context().close()
  }
})

/** The Launch dialog's memory of the last launch, in this app's storage before the page loads (utils/lastLaunch.ts's key). */
async function remembered(page: Page, value: unknown) {
  await page.context().addInitScript((v) => localStorage.setItem('conductor.launch.last', JSON.stringify(v)), value)
  await page.reload()
}

test('someone who launched before reads Nothing is running and the agent they launched last, at the daily window, at 1440 and on a phone, in both themes', async ({ browser }) => {
  const srv = await empty()
  budget(3 * 2 * 8 * STEP_MS)
  const { named } = offerOf(await settledCatalog(srv))
  const lastOne = named.find((a) => a.id !== SHELL)
  if (!lastOne) throw new Error('the e2e catalog has a second agent that is not the shell')
  for (const size of [DAILY, { width: 1440, height: 900 }, { width: 390, height: 844, touch: true }]) {
    const phone = 'touch' in size
    const page = await dayPage(browser, { ...size, path: phone ? '/sessions' : '/', server: srv.started })
    await remembered(page, { agent: lastOne.id, models: {} })
    for (const theme of ['dark', 'light'] as const) {
      const at = `${size.width} × ${size.height}, ${theme}`
      if (theme === 'light') await useTheme(page, 'light')
      const card = await settled(page)
      await expect(card.locator('[data-welcome-title]'), at).toHaveText('Nothing is running')
      await expect(card.locator('[data-welcome-sentence]'), at).toHaveCount(0)
      await expect(card.locator('[data-welcome-primary]'), at).toHaveText(`Launch ${lastOne.name}`)
      await expect(card.locator('[data-welcome-primary]'), at).toHaveAttribute('data-welcome-agent', lastOne.id)
      await uncut(card.locator('[data-welcome-title]'))
      await uncut(card.locator('[data-welcome-primary]'))
      await noSideways(page)
      if (phone) await tapTarget(card.locator('[data-welcome-primary]'))
    }
    await page.context().close()
  }
})

test("an agent remembered that the server does not offer gives the first agent found and the first run's words", async ({ browser }) => {
  const srv = await empty()
  budget(6 * STEP_MS)
  const { main } = offerOf(await settledCatalog(srv))
  const page = await dayPage(browser, { width: 1440, height: 900, path: '/', server: srv.started })
  await remembered(page, { agent: 'no-such-agent', models: {} })
  const card = await settled(page)
  await expect(card.locator('[data-welcome-primary]')).toHaveAttribute('data-welcome-agent', main.id)
  await expect(card.locator('[data-welcome-title]')).toHaveText('Welcome to Conductor')
  await expect(card.locator('[data-welcome-sentence]')).toHaveText(SENTENCE)
  await page.context().close()
})

test("the server's record of the last launch outlasts its restart: with no memory in the app, the welcome names that agent", async ({ browser }) => {
  budget(2 * START_WAIT_MS + 10 * STEP_MS)
  const srv = await ownServer('welcome-last')
  try {
    const { named } = offerOf(await settledCatalog(srv))
    const lastOne = named.find((a) => a.id !== SHELL)!
    const s = await srv.api.ok<{ id: string }>('POST', '/api/sessions', { agentId: lastOne.id, name: 'welcome last launch' })
    await expect.poll(async () => (await srv.api.ok<{ agent?: string }>('GET', '/api/launch/last')).agent, { message: 'the launch recorded' }).toBe(lastOne.id)
    await srv.api.stopSession(s.id)
    await expect.poll(async () => (await srv.api.session(s.id)).status, { timeout: STEP_MS }).toMatch(/exited|stopped/)
    // Started again on its data: the session is no longer listed, and the record is read from its file.
    await srv.restart()
    expect((await srv.api.call('GET', `/api/sessions/${s.id}`)).status).toBe(404)
    const page = await dayPage(browser, { width: 1440, height: 900, path: '/', server: srv.started })
    expect(await page.evaluate(() => localStorage.getItem('conductor.launch.last'))).toBeNull()
    const card = await settled(page)
    await expect(card.locator('[data-welcome-primary]')).toHaveAttribute('data-welcome-agent', lastOne.id)
    await expect(card.locator('[data-welcome-title]')).toHaveText('Nothing is running')
    await page.context().close()
  } finally {
    await srv.stop()
  }
})

// The tooltips of the welcome's buttons and of Crews' Load the examples (#374): each says what pressing it does, in the words of
// utils/welcome.ts, written out here so that a change of them is seen.
const EXAMPLES_TIP = 'Adds five ready-made crews to Crews; nothing starts until you launch one.'
const TIPS = {
  agent: (name: string, version?: string) => `Opens the Launch dialog with ${name}${version ? ` ${version}` : ''} picked.`,
  more: 'Opens the Launch dialog with every agent this machine has.',
  host: 'Opens the Quick start: how to run conductor host on another machine.',
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

/**
 * The welcome's buttons as the card draws them with agents found, the other ways unfolded: the main one, each name of the Or line,
 * "N more" when there are more, the host's, the examples'. An agent's tooltip names its version where its check found one (`catalog`).
 */
async function welcomeButtons(card: Locator, catalog: CatalogAgent[]) {
  const version = (id: string) => {
    const a = catalog.find((c) => c.id === id)
    return a?.identity?.identified ? a.identity.version : undefined
  }
  const out: { button: Locator; name: string | RegExp; tip: string }[] = []
  const agents = card.locator('[data-welcome-agent]')
  for (let i = 0; i < (await agents.count()); i++) {
    const button = agents.nth(i)
    const label = (await button.textContent())!.trim()
    const name = (await button.getAttribute('data-welcome-primary')) === null ? label : label.replace(/^Launch /, '')
    out.push({ button, name: label, tip: TIPS.agent(name, version((await button.getAttribute('data-welcome-agent'))!)) })
  }
  const more = card.locator('[data-welcome-more]')
  if (await more.count()) out.push({ button: more, name: `${(await more.textContent())!.trim()} agents`, tip: TIPS.more })
  await unfold(card)
  out.push({ button: card.locator('[data-welcome-host]'), name: 'How to bring one in', tip: TIPS.host })
  out.push({ button: card.locator('[data-welcome-examples]'), name: 'Load the example crews', tip: TIPS.examples })
  return out
}

test("every button of the welcome says what it does in a tooltip, on hover and on Tab, at the desktop app's width in both themes", async ({ browser }) => {
  const srv = await empty()
  budget(2 * 40 * STEP_MS)
  const catalog = await settledCatalog(srv)
  for (const theme of ['dark', 'light'] as const) {
    const page = await dayPage(browser, { width: 1440, height: 900, theme, path: '/', server: srv.started })
    const card = await settled(page)
    await expect(anyTip(page)).toHaveCount(0)
    const buttons = await welcomeButtons(card, catalog)
    expect(buttons.length, 'an agent at least, with the host and the examples').toBeGreaterThanOrEqual(3)
    await tipsOnHoverAndTab(page, buttons, `${theme}`)
    await page.context().close()
  }
})

test("a tooltip closes as the pointer leaves its button: a press on the Or line's first agent, under the main button whose tooltip was open, opens that agent's Launch dialog", async ({ browser }) => {
  const srv = await empty()
  budget(8 * STEP_MS)
  const { main, named } = offerOf(await settledCatalog(srv))
  expect(named.length, 'the e2e catalog offers an agent on the Or line').toBeGreaterThanOrEqual(1)
  const page = await dayPage(browser, { width: 1440, height: 900, path: '/', server: srv.started })
  const card = await settled(page)
  const first = card.locator('[data-welcome-primary]')
  const second = card.locator('[data-welcome-or]').getByRole('button', { name: named[0]!.name, exact: true })
  const name = named[0]!.name
  await first.hover()
  await tipShown(page, first, TIPS.agent(main.name, main.identity?.identified ? main.identity.version : undefined), 'the main button, hovered')
  // Straight down onto the middle of the Or line's first agent, where the main button's tooltip lies while it is open, and pressed
  // there: the mouse itself, so that a press that lands on the tooltip fails here rather than waiting on the locator's own check.
  const box = (await second.boundingBox())!
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2, { steps: 5 })
  await page.mouse.down()
  await page.mouse.up()
  const dialog = page.getByRole('dialog')
  await expect(dialog, 'the press reached the second agent').toBeVisible()
  await expect(dialog.getByRole('radio', { checked: true })).toContainText(name)
})

test("on a phone the welcome's buttons keep their labels as their names, and a tap opens no tooltip, then or when the dialog it opened closes", async ({ browser }) => {
  const srv = await empty()
  budget(12 * STEP_MS)
  const catalog = await settledCatalog(srv)
  const page = await dayPage(browser, { width: 390, height: 844, touch: true, path: '/sessions', server: srv.started })
  const card = await settled(page)
  for (const c of await welcomeButtons(card, catalog)) await expect(c.button).toHaveAccessibleName(c.name)
  const first = card.locator('[data-welcome-primary]')
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

test("the examples say what loading them does, on the welcome's tooltip, on Crews and in the palette, and the welcome loads them onto Crews", async ({ browser }) => {
  budget(START_WAIT_MS + 20 * STEP_MS)
  const srv = await ownServer('welcome-examples')
  try {
    // Crews with nothing on it: the words under its Load the examples.
    const page = await dayPage(browser, { width: 1440, height: 900, path: '/crews', server: srv.started })
    const crewsEmpty = page.locator('[data-crews-empty]')
    await expect(crewsEmpty.getByRole('button', { name: 'Load the examples' })).toBeVisible()
    const line = (await crewsEmpty.locator('[data-examples-line]').textContent())!.trim()
    expect(line).toBe(EXAMPLES_TIP)
    // The welcome: the same words are its button's tooltip, and no line under it says them again.
    await page.goto('/')
    const card = await settled(page)
    await unfold(card)
    await expect(card.locator('[data-examples-line]')).toHaveCount(0)
    const examples = card.locator('[data-welcome-examples]')
    await examples.hover()
    await tipShown(page, examples, line, 'the welcome, hovered')
    await noTip(page)
    await page.goto('/crews')
    await expect(crewsEmpty.getByRole('button', { name: 'Load the examples' })).toBeVisible()
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

    // The welcome's Load the example crews lands on Crews, listing them, and says how many it added in the words Crews says.
    await page.goto('/')
    await (await unfold(await settled(page))).locator('[data-welcome-examples]').click()
    await expect(page.getByText(/^[1-9]\d* example crews? added$/)).toBeVisible()
    await expect(page).toHaveURL(/\/crews$/)
    await expect(page.locator('[data-crew-entry][data-crew-id="example-todo-app"] [data-crew-item]')).toBeVisible()
  } finally {
    await srv.stop()
  }
})
