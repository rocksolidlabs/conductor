import type { Locator, Page, Route } from '@playwright/test'
import { DAILY, atRest, dayPage, expect, openMore, test } from './fixtures'
import { noSideways, probeColour } from './layout'
import { ownServer, type OwnServer } from './own-server'

// The Launch agent dialog (the six screens' Launch agent, UX3 R26): its face holds the agents, the folder, the account with its usage
// in one line, `What should it do?` with the cursor in it, More options folded, and Yolo beside Launch. Enter in the task launches,
// once; Ctrl+Enter launches from any field. On a server of its own, with a copy of the Claude Code stub whose prompt the server knows,
// so that the dialog offers it a task. Launches are held by a route and refused, so no session is made.

let own: OwnServer

test.describe.configure({ mode: 'serial' })

/** The stub's own prompt, "> " and what is typed after it as the last row: what the server recognises before it types a first message. */
const STUB_PROMPT = String.raw`(?m)^> ?[^\n]*\s*\z`
const PROMPTED = 'Claude Code (e2e stub), prompted'
const PLAIN = 'Plain (e2e stub)'

test.beforeAll(async () => {
  test.setTimeout(120_000)
  // The half dozen agents a person has (the e2e catalog's sixteen would make any dialog scroll).
  own = await ownServer('launch-dialog', { agents: [{ id: 'prompted', from: 'claude', env: { STUB_TUI: '1' }, promptShape: STUB_PROMPT }], keep: ['claude', 'codex', 'goose', 'plain', 'yolo-login'] })
})

test.afterAll(async () => {
  await own.stop()
})

/** The Launch dialog opened from the page's Launch agent button, once its Launch button is drawn. */
async function openLaunch(page: Page): Promise<Locator> {
  await page.getByRole('button', { name: 'Launch agent' }).first().click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('button', { name: 'Launch', exact: true })).toBeVisible()
  return dialog
}

/** Picks the agent named in the open dialog: its radio's name ends with the agent's (the avatar's letters come first). */
async function pick(dialog: Locator, name: string) {
  const radio = dialog.getByRole('radiogroup', { name: 'Agent' }).getByRole('radio', { name: new RegExp(`${name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`) })
  await radio.click()
  await expect(radio).toHaveAttribute('aria-checked', 'true')
}

/**
 * The dialog opened on the prompted stub as a person meets it every day: picked once, the dialog closed, and opened again, so that
 * the agent of the last opening is the one picked and nothing the test did has moved the focus. Returns it at rest.
 */
async function openOnPrompted(page: Page): Promise<Locator> {
  let dialog = await openLaunch(page)
  await pick(dialog, PROMPTED)
  await dialog.getByRole('button', { name: 'Cancel' }).click()
  await expect(dialog).toHaveCount(0)
  dialog = await openLaunch(page)
  await expect(dialog.getByRole('radio', { name: new RegExp(`${PROMPTED.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`) })).toHaveAttribute('aria-checked', 'true')
  await expect(dialog.locator('[data-launch-message-input]')).toBeVisible()
  await atRest(dialog)
  return dialog
}

/** Holds every launch the page sends (POST /api/sessions) until `refuse`, which aborts them all: no session is made. */
async function holdLaunches(page: Page): Promise<{ held: Route[]; refuse: () => Promise<void> }> {
  const held: Route[] = []
  const match = (url: URL) => url.pathname === '/api/sessions'
  await page.route(match, (route) => (route.request().method() === 'POST' ? void held.push(route) : route.fallback()))
  return {
    held,
    refuse: async () => {
      for (const r of held) await r.abort().catch(() => {})
      await page.unroute(match)
    },
  }
}

test('the cursor is in What should it do? as the dialog opens, Shift+Enter is a new line, and Enter launches once', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started })
  const dialog = await openOnPrompted(page)
  const task = dialog.locator('[data-launch-message-input]')
  await expect(task).toBeFocused()

  const launches = await holdLaunches(page)
  await page.keyboard.type('say hello')
  await page.keyboard.press('Shift+Enter')
  await page.keyboard.type('twice')
  await expect(task).toHaveValue('say hello\ntwice')
  expect(launches.held, 'Shift+Enter sent nothing').toHaveLength(0)

  await page.keyboard.press('Enter')
  await expect.poll(() => launches.held.length, { message: 'Enter sent the launch' }).toBe(1)
  expect(launches.held[0]!.request().postDataJSON()).toMatchObject({ agentId: 'prompted', message: 'say hello\ntwice' })
  // Pressed again while the launch is out: nothing more is sent. The held launch is then refused, and the refusal shown.
  await page.keyboard.press('Enter')
  await launches.refuse()
  await expect(dialog.locator('[data-launch-error]')).toBeVisible()
  expect(launches.held, 'the second Enter sent nothing').toHaveLength(1)
  // The dialog stays, with what was typed.
  await expect(task).toHaveValue('say hello\ntwice')
})

test('Ctrl+Enter launches from the folder field', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started })
  const dialog = await openOnPrompted(page)
  const launches = await holdLaunches(page)
  const folder = dialog.locator('[data-dir-input] input[role="combobox"]')
  await folder.focus()
  await expect(folder).toBeFocused()
  await page.keyboard.press('ControlOrMeta+Enter')
  await expect.poll(() => launches.held.length, { message: 'Ctrl+Enter sent the launch' }).toBe(1)
  await launches.refuse()
  await expect(dialog.locator('[data-launch-error]')).toBeVisible()
})

test('Yolo sits beside Launch, reads the same on and off, is in no warning colour, and is off and disabled for an agent that cannot skip its prompts', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started })
  const dialog = await openOnPrompted(page)
  const footer = dialog.locator('[data-slot="footer"]')
  const yolo = footer.locator('[data-yolo-switch]')
  const words = footer.locator('[data-yolo-words]')
  await expect(yolo).toBeVisible()
  await expect(words).toHaveText('skips permission prompts')
  // Never inside the fold.
  await expect(dialog.locator('[data-more] [data-yolo-switch]')).toHaveCount(0)

  const was = await yolo.getAttribute('aria-checked')
  await yolo.click()
  await expect(yolo).not.toHaveAttribute('aria-checked', was!)
  await expect(words).toHaveText('skips permission prompts')
  if ((await yolo.getAttribute('aria-checked')) !== 'true') {
    await yolo.click()
    await expect(yolo).toHaveAttribute('aria-checked', 'true')
  }
  await expect(words).toHaveText('skips permission prompts')
  await atRest(yolo)
  const on = await yolo.evaluate((el) => getComputedStyle(el).backgroundColor)
  expect(await probeColour(page, on), 'Yolo on is not drawn in the warning colour').not.toBe(await probeColour(page, 'var(--ui-warning)'))
  expect(await probeColour(page, await words.evaluate((el) => getComputedStyle(el).color))).not.toBe(await probeColour(page, 'var(--ui-warning-text)'))

  // An agent with no recipe: the switch off and disabled, saying why in place of the words.
  await pick(dialog, PLAIN)
  await expect(yolo).toBeDisabled()
  await expect(yolo).toHaveAttribute('aria-checked', 'false')
  await expect(words).toHaveText(`${PLAIN} cannot skip its prompts`)
  await expect(dialog.locator('[data-yolo-missing]')).toHaveCount(0)
})

test('a model and arguments set inside More options are named on its closed row, whole, and nothing is named when all is as it comes', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started })
  const dialog = await openOnPrompted(page)
  const button = dialog.locator('[data-more-options]')
  await expect(button).toHaveAttribute('aria-expanded', 'false')
  await expect(dialog.locator('[data-more-line]')).toHaveCount(0)
  const more = await openMore(dialog)
  await more.locator('[data-launch-model-input]').fill('opus')
  await more.locator('[data-launch-args]').fill('--add-dir ../shared "two words"')
  await button.click()
  await expect(button).toHaveAttribute('aria-expanded', 'false')
  const line = dialog.locator('[data-more-line]')
  await expect(line).toHaveText('opus · --add-dir ../shared "two words"')
  await expect(line).toBeVisible()
  // Cleared again, the row names nothing.
  await openMore(dialog)
  await more.locator('[data-launch-model-input]').fill('')
  await more.locator('[data-launch-args]').fill('')
  await button.click()
  await expect(dialog.locator('[data-more-line]')).toHaveCount(0)
})

for (const size of [DAILY, { width: 1440, height: 810 }]) {
  test(`at ${size.width} x ${size.height} the usage is in view under the account with More options closed, and Launch with it`, async ({ browser }) => {
    const page = await dayPage(browser, { server: own.started, ...size })
    const dialog = await openOnPrompted(page)
    await expect(dialog.locator('[data-more-options]')).toHaveAttribute('aria-expanded', 'false')
    const usage = dialog.locator('[data-capacity-panel] [data-capacity-line]')
    await expect(usage).toBeInViewport()
    await expect(dialog.locator('[data-launch-account]')).toBeInViewport()
    await expect(dialog.getByRole('button', { name: 'Launch', exact: true })).toBeInViewport()
  })
}

test('on a phone the footer wraps, Yolo above the buttons, and nothing scrolls sideways', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, path: '/sessions', width: 390, height: 844, touch: true })
  const dialog = await openOnPrompted(page)
  const footer = dialog.locator('[data-slot="footer"]')
  const yolo = footer.locator('[data-launch-yolo]')
  const launch = footer.getByRole('button', { name: 'Launch', exact: true })
  await expect(yolo).toBeVisible()
  await expect(launch).toBeVisible()
  const y = (await yolo.boundingBox())!
  const l = (await launch.boundingBox())!
  expect(y.y + y.height, 'Yolo sits above the buttons').toBeLessThanOrEqual(l.y + 0.5)
  await noSideways(page, { root: dialog })
})
