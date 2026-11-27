import type { Locator, Page, Route } from '@playwright/test'
import { budget, dayPage, expect, test } from './fixtures'
import { BOX_SLACK, noSideways, tapTarget, uncut } from './layout'
import { ownServer, type OwnServer } from './own-server'
import { START_WAIT_MS } from './server'

// The welcome says what it found (UX3): while it looks, a disabled button where the main one will be; with no coding agent, so in plain
// words, or that the agent is on Windows alone, with Choose an agent to install and Look again, which asks the machine now and answers
// in place; with the agents not read, the server's words, Launch agent and Read again. Each step waits on a route the test holds and
// lets go, never on a time; the routes are set over dayPage's standing route, before the page loads again.

/** An expect's bound and an action's (the config's expect timeout). */
const STEP_MS = 15_000

const WORDS = {
  looking: 'Looking for agents on this machine…',
  none: 'No coding agent is installed on this machine yet.',
  windows: (name: string) => `${name} is installed on Windows, not in WSL, where Conductor runs. Install it in WSL, then press Look again.`,
  looked: 'Looked again: still none found.',
  failed: (words: string) => `The agents could not be read: ${words}`,
}
/** The tooltips of the two buttons these states add a tooltip to, in the words of utils/welcome.ts. */
const TIPS = {
  agents: 'Opens Agents: the agents Conductor knows, and which of them this machine has.',
  launch: 'Opens the Launch dialog, which looks for the agents on this machine again.',
}

/** Hovered, `button` shows its tooltip, whose words are its description; the pointer moved away, it closes. */
async function saysOnHover(page: Page, button: Locator, tip: string, at: string) {
  await button.hover()
  await expect(page.locator('[data-slot="content"]').filter({ has: page.getByRole('tooltip', { name: tip, exact: true, includeHidden: true }) }), `${at}: the tooltip`).toBeVisible()
  await expect(button, at).toHaveAccessibleDescription(tip)
  await page.mouse.move(1, 1)
  await expect(page.getByRole('tooltip', { includeHidden: true }), `${at}: closed`).toHaveCount(0)
}

/** An agent as GET /api/catalog lists one, as this spec makes them: a name no test server has. */
function stubAgent(o: { available: boolean; onWindows?: string }) {
  return { id: 'welcome-stub', name: 'Welcome Stub Agent', command: ['welcome-stub-agent'], allowArgs: true, available: o.available, ...(o.onWindows ? { onWindows: o.onWindows } : {}) }
}

let one: OwnServer | undefined
async function server(): Promise<OwnServer> {
  if (!one) {
    budget(START_WAIT_MS)
    one = await ownServer('welcome-states')
  }
  return one
}
test.afterAll(async () => {
  await one?.stop()
  one = undefined
})

/**
 * A route on `pathname` the test holds and answers: every request waits at the gate while it is shut, then gets `answer`. `seen` counts
 * the requests that reached it, `bodies` what each sent.
 */
async function heldRoute(page: Page, pathname: string, answer: (route: Route) => Promise<void>) {
  const r = {
    seen: 0,
    bodies: [] as unknown[],
    answer,
    gate: Promise.resolve(),
    open: () => {},
    /** Shuts the gate: the requests from now on wait until `open`. */
    shut() {
      r.gate = new Promise<void>((resolve) => (r.open = resolve))
    },
  }
  await page.route(
    (u) => u.pathname === pathname,
    async (route) => {
      r.seen++
      r.bodies.push(route.request().postDataJSON())
      await r.gate
      await r.answer(route)
    },
  )
  return r
}

/** The welcome page with GET /api/catalog held by the test, loaded again so that its first read meets the route. */
async function welcomeWith(page: Page, answer: (route: Route) => Promise<void>, held = false) {
  const catalog = await heldRoute(page, '/api/catalog', answer)
  if (held) catalog.shut()
  await page.reload()
  const card = page.locator('[data-welcome-card]')
  await expect(card).toBeVisible()
  return { catalog, card }
}

const json = (body: unknown, status = 200) => (route: Route) => route.fulfill({ status, json: body })

/** What the state draws is whole and nothing scrolls sideways; under touch each control of its start block is big enough to tap. */
async function stateWhole(page: Page, card: Locator, parts: Locator, touch: boolean, at: string) {
  for (let i = 0; i < (await parts.count()); i++) await uncut(parts.nth(i))
  await noSideways(page)
  if (!touch) return
  const controls = card.locator('[data-welcome-start] button, [data-welcome-start] a, [data-welcome-expander]')
  expect(await controls.count(), `${at}: controls`).toBeGreaterThan(0)
  for (let i = 0; i < (await controls.count()); i++) await tapTarget(controls.nth(i))
}

test('while the first read is held the card shows a disabled looking button in the box the main button then takes, and nothing below moves', async ({ browser }) => {
  const srv = await server()
  budget(8 * STEP_MS)
  const page = await dayPage(browser, { width: 1440, height: 900, path: '/', server: srv.started })
  const { catalog, card } = await welcomeWith(page, (route) => route.continue(), true)
  const looking = card.locator('[data-welcome-looking]')
  await expect(looking).toHaveText(WORDS.looking)
  await expect(looking).toBeDisabled()
  await expect.poll(() => catalog.seen, { message: 'the card asked for the agents' }).toBeGreaterThan(0)
  const before = [await looking.boundingBox(), await card.locator('[data-welcome-expander]').boundingBox()]
  catalog.open()
  const primary = card.locator('[data-welcome-primary]')
  await expect(primary).toBeVisible()
  await expect(looking).toHaveCount(0)
  const after = [await primary.boundingBox(), await card.locator('[data-welcome-expander]').boundingBox()]
  for (const [i, what] of ['the main button', 'the fold row'].entries()) {
    for (const k of ['x', 'y', 'width', 'height'] as const) expect(Math.abs(after[i]![k] - before[i]![k]), `${what}'s ${k}`).toBeLessThanOrEqual(BOX_SLACK)
  }
})

test('with no agent the card says so in plain words, offers Choose an agent to install and Look again, and keeps the fold, in both themes and on a phone', async ({ browser }) => {
  const srv = await server()
  budget(3 * 12 * STEP_MS)
  for (const o of [{ theme: 'dark', touch: false }, { theme: 'light', touch: false }, { theme: 'dark', touch: true }] as const) {
    const at = `${o.theme}${o.touch ? ', phone' : ''}`
    const page = o.touch
      ? await dayPage(browser, { width: 390, height: 844, touch: true, theme: o.theme, path: '/sessions', server: srv.started })
      : await dayPage(browser, { width: 1440, height: 900, theme: o.theme, path: '/', server: srv.started })
    const { card } = await welcomeWith(page, json({ agents: [] }))
    const none = card.locator('[data-welcome-none]')
    await expect(none, at).toHaveText(WORDS.none)
    // A standing fact, never amber.
    expect(await none.evaluate((el) => el.className), at).not.toMatch(/text-warning/)
    await expect(card.locator('[data-welcome-agents]'), at).toHaveAccessibleName('Choose an agent to install')
    await expect(card.locator('[data-welcome-again]'), at).toHaveAccessibleName('Look again')
    await expect(card.locator('[data-welcome-expander]'), at).toBeVisible()
    await expect(card.locator('[data-welcome-primary], [data-welcome-failed]'), at).toHaveCount(0)
    await stateWhole(page, card, card.locator('[data-welcome-none], [data-welcome-agents], [data-welcome-again]'), o.touch, at)
    if (!o.touch) await saysOnHover(page, card.locator('[data-welcome-agents]'), TIPS.agents, at)
    await page.context().close()
  }
})

test('Look again asks the machine about each program the server cannot launch, then reads the agents again: one found turns the card to Launch it, with no reload', async ({ browser }) => {
  const srv = await server()
  budget(8 * STEP_MS)
  const page = await dayPage(browser, { width: 1440, height: 900, path: '/', server: srv.started })
  const { catalog, card } = await welcomeWith(page, json({ agents: [stubAgent({ available: false })] }))
  await expect(card.locator('[data-welcome-none]')).toHaveText(WORDS.none)
  const check = await heldRoute(page, '/api/catalog/check', json({ found: true, path: '/usr/bin/welcome-stub-agent' }))
  check.shut()
  const reads = catalog.seen
  await card.locator('[data-welcome-again]').click()
  await expect.poll(() => check.seen, { message: 'the check asked' }).toBe(1)
  expect(check.bodies[0]).toEqual({ command: ['welcome-stub-agent'] })
  // While it asks, the button spins and takes no second press; the agents are not read before the check answered.
  await expect(card.locator('[data-welcome-again]')).toBeDisabled()
  expect(catalog.seen).toBe(reads)
  catalog.answer = json({ agents: [stubAgent({ available: true })] })
  check.open()
  await expect(card.locator('[data-welcome-primary]')).toHaveText('Launch Welcome Stub Agent')
  await expect(card.locator('[data-welcome-none], [data-welcome-looked]')).toHaveCount(0)
})

test('Look again that finds nothing says so in place, as a status, and leaves the focus on its button', async ({ browser }) => {
  const srv = await server()
  budget(8 * STEP_MS)
  const page = await dayPage(browser, { width: 1440, height: 900, path: '/', server: srv.started })
  const { catalog, card } = await welcomeWith(page, json({ agents: [stubAgent({ available: false })] }))
  const again = card.locator('[data-welcome-again]')
  await expect(again).toBeVisible()
  const check = await heldRoute(page, '/api/catalog/check', json({ found: false }))
  const reads = catalog.seen
  await again.click()
  await expect.poll(() => catalog.seen, { message: 'the agents read again' }).toBeGreaterThan(reads)
  expect(check.seen).toBe(1)
  const looked = card.locator('[data-welcome-looked]')
  await expect(looked).toHaveText(WORDS.looked)
  await expect(looked).toHaveRole('status')
  await expect(card.locator('[data-welcome-none]')).toHaveText(WORDS.none)
  await expect(again).toBeFocused()
})

test('an agent the server found on Windows alone is named, with what to do about it', async ({ browser }) => {
  const srv = await server()
  budget(6 * STEP_MS)
  const page = await dayPage(browser, { width: 1440, height: 900, path: '/', server: srv.started })
  const { card } = await welcomeWith(page, json({ agents: [stubAgent({ available: false, onWindows: '/mnt/c/Users/someone/welcome-stub-agent.exe' })] }))
  await expect(card.locator('[data-welcome-none]')).toHaveText(WORDS.windows('Welcome Stub Agent'))
  await expect(card.locator('[data-welcome-none]')).toHaveAttribute('data-welcome-windows', '')
  await expect(card.locator('[data-welcome-agents]')).toBeVisible()
  await expect(card.locator('[data-welcome-again]')).toBeVisible()
})

test('a read of the agents that failed says the server words, Launch agent opens the dialog, and Read again shows the card, in both themes and on a phone', async ({ browser }) => {
  const srv = await server()
  budget(3 * 14 * STEP_MS)
  const refused = json({ error: { code: 'catalog_failed', message: 'the catalog could not be read' } }, 500)
  for (const o of [{ theme: 'dark', touch: false }, { theme: 'light', touch: false }, { theme: 'dark', touch: true }] as const) {
    const at = `${o.theme}${o.touch ? ', phone' : ''}`
    const page = o.touch
      ? await dayPage(browser, { width: 390, height: 844, touch: true, theme: o.theme, path: '/sessions', server: srv.started })
      : await dayPage(browser, { width: 1440, height: 900, theme: o.theme, path: '/', server: srv.started })
    const { catalog, card } = await welcomeWith(page, refused)
    const failed = card.locator('[data-welcome-failed]')
    await expect(failed, at).toHaveText(WORDS.failed('the catalog could not be read'))
    expect(await failed.evaluate((el) => el.className), at).toMatch(/text-warning/)
    await stateWhole(page, card, card.locator('[data-welcome-failed] span, [data-welcome-launch], [data-welcome-read-again]'), o.touch, at)
    if (!o.touch) await saysOnHover(page, card.locator('[data-welcome-launch]'), TIPS.launch, at)
    const launch = card.locator('[data-welcome-launch]')
    if (o.touch) await launch.tap()
    else await launch.click()
    const dialog = page.getByRole('dialog')
    await expect(dialog, at).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(dialog, at).toHaveCount(0)
    // Read again, the server answering now: the looking state while the read is held, then the card.
    catalog.answer = (route) => route.continue()
    catalog.shut()
    const reads = catalog.seen
    await card.locator('[data-welcome-read-again]').click()
    await expect(card.locator('[data-welcome-looking]'), at).toBeVisible()
    await expect.poll(() => catalog.seen, { message: `${at}: read again` }).toBeGreaterThan(reads)
    catalog.open()
    await expect(card.locator('[data-welcome-primary]'), at).toBeVisible()
    await expect(failed, at).toHaveCount(0)
    await page.context().close()
  }
})
