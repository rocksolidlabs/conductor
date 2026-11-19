import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Page } from '@playwright/test'
import { expect, showView, test, type Api, type Session } from './fixtures'

// A link in a session's terminal opens through the app's own handler, never through a dialog (#1020). An agent prints a hyperlink
// with OSC 8 (its words a link to an address they need not show); xterm, given no handler of its own for those, asked "Do you want
// to navigate to …? WARNING: This link could potentially be dangerous" with the page's confirm, which the desktop app shows as a
// native dialog titled with its name. Now a hyperlink is handled as an address the terminal finds in the text is: a click (plain,
// or with Ctrl or ⌘) opens it in a new tab, once, and a toast says where it went with Preview in pane; the pointer over a link shows
// its real address; a drag that selects is no click; a hyperlink to any other scheme than http or https is no link at all. The stub agent prints the links (`stub links`, web/e2e/stub-agent.sh) on the
// first rows of a cleared screen; window.open is recorded and opens nothing, so no address is ever fetched.
test.skip(process.platform === 'win32', 'the stub agent is a bash script')

const MR = 'https://example.com/group/project/-/merge_requests/95'
/** The rows the stub prints, from the first: three hyperlinks to schemes that are not the web's, then the web's three kinds. */
const ROW = { file: 0, script: 1, custom: 2, hyperlink: 3, hyperlinkShown: 4, address: 5 }
const LINKS = [
  'file:///etc/passwd a file on this computer',
  'javascript:void(0) a script',
  'vscode://file/etc/passwd an editor',
  `${MR} the merge request`,
  `${MR} ${MR}`,
  `- ${MR}`,
]

type Seen = { e2eOpens?: string[][] }
const sessions: string[] = []

test.afterAll(async ({ api }) => {
  for (const id of sessions) await api.stopSession(id)
})

function transcript(home: string, s: Session): string {
  const file = join(home, '.stub-sessions', `${s.agentSession?.id ?? ''}.txt`)
  return existsSync(file) ? readFileSync(file, 'utf8') : ''
}

/** Every dialog the page raises, recorded (and dismissed), and every window.open, recorded (opening nothing). */
async function watch(page: Page) {
  const dialogs: string[] = []
  page.on('dialog', (d) => {
    dialogs.push(`${d.type()}: ${d.message()}`)
    void d.dismiss()
  })
  await page.addInitScript(() => {
    const w = window as unknown as Seen
    w.e2eOpens = []
    window.open = (...args: unknown[]) => {
      w.e2eOpens!.push(args.map(String))
      return null
    }
  })
  const opens = () => page.evaluate(() => (window as unknown as Seen).e2eOpens ?? [])
  return { dialogs, opens }
}

/** The session page with the stub's links on its first rows, watched. */
async function linksPage(page: Page, api: Api, home: string, name: string, links: string[] = LINKS) {
  const { dialogs, opens } = await watch(page)
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name })
  sessions.push(s.id)
  await page.goto(`/sessions/${encodeURIComponent(s.id)}`)
  await expect(page.locator('[data-transport-state]').first()).toHaveAttribute('data-transport-state', 'open', { timeout: 30_000 })
  const terminal = page.locator('.terminal-host .xterm-helper-textarea').first()
  await terminal.focus()
  await page.keyboard.type(`stub links ${links.join('|')}`)
  await page.keyboard.press('Enter')
  await expect.poll(async () => transcript(home, await api.session(s.id)), { timeout: 30_000, message: 'the stub printed its links' }).toContain(`links: printed ${links.length}\n`)
  const host = page.locator('.terminal-host').first()
  const box = (await host.boundingBox())!
  const rows = Number(await host.getAttribute('data-rows'))
  const cols = Number(await host.getAttribute('data-cols'))
  const cell = (row: number, col: number) => ({ x: box.x + (box.width / cols) * (col + 0.5), y: box.y + (box.height / rows) * (row + 0.5) })
  const at = (row: number) => cell(row, 3)
  // xterm marks the element the pointer is over with this class while it is over a link.
  const pointer = host.locator('.xterm-cursor-pointer')
  /** The pointer over the row's link: moved there until xterm shows it is over one (the output may be drawn after the first move). */
  const overLink = async (row: number) => {
    const p = at(row)
    await expect(async () => {
      await page.mouse.move(p.x - 1, p.y)
      await page.mouse.move(p.x, p.y)
      await expect(pointer).toHaveCount(1, { timeout: 1000 })
    }).toPass({ timeout: 15_000 })
  }
  const click = async (row: number, ctrl = false, o: { button?: 'left' | 'middle' | 'right'; clickCount?: number } = {}) => {
    const p = at(row)
    if (ctrl) await page.keyboard.down('Control')
    if (o.clickCount === 2) await page.mouse.dblclick(p.x, p.y, { button: o.button })
    else await page.mouse.click(p.x, p.y, { button: o.button })
    if (ctrl) await page.keyboard.up('Control')
  }
  /** A drag that selects, from one cell to another, in steps over what lies between. */
  const drag = async (from: { row: number; col: number }, to: { row: number; col: number }, fromLink = false) => {
    const a = cell(from.row, from.col)
    const b = cell(to.row, to.col)
    if (fromLink) {
      // Pressed on the link itself: xterm holds it as the link under the pointer before the press.
      await expect(async () => {
        await page.mouse.move(a.x + 1, a.y)
        await page.mouse.move(a.x, a.y)
        await expect(pointer).toHaveCount(1, { timeout: 1000 })
      }).toPass({ timeout: 15_000 })
    } else await page.mouse.move(a.x, a.y)
    await page.mouse.down()
    await page.mouse.move(b.x, b.y, { steps: 8 })
    await page.mouse.up()
  }
  return { s, dialogs, overLink, click, drag, opens, at, cell }
}

const OPENED: string[][] = [[MR, '_blank', 'noopener,noreferrer']]
/** The toasts that say a link of the terminal's was opened, with its preview: "Opened <host>", Preview in pane alone. */
const said = (page: Page) => page.locator('li').filter({ hasText: 'Opened example.com' })

test('a plain click opens once and raises no dialog, then the toast offers Preview in pane, which opens the preview and no second tab', async ({ page, api, state }) => {
  // The preview pane's frame loads the address: it is answered here, nothing leaves the machine.
  await page.context().route('https://example.com/**', (r) => r.fulfill({ status: 200, contentType: 'text/html', body: '<p>e2e</p>' }))
  const t = await linksPage(page, api, state.home, 'e2e terminal hyperlink')
  await t.overLink(ROW.hyperlink)
  await t.click(ROW.hyperlink)
  // Whichever the click brings, a dialog or the open, comes from the click's own handler.
  await expect.poll(async () => t.dialogs.length + (await t.opens()).length, { timeout: 15_000, message: 'the click was handled' }).toBeGreaterThan(0)
  expect(t.dialogs, 'a click on a link raises no dialog').toEqual([])
  expect(await t.opens(), 'one click opens once').toEqual(OPENED)
  const toast = said(page)
  await expect(toast).toHaveCount(1)
  await expect(toast).toContainText(MR)
  await expect(toast.getByRole('button', { name: 'Open in new tab' })).toHaveCount(0)
  await toast.getByRole('button', { name: 'Preview in pane' }).click()
  await expect(page.locator(`[data-editor-link-pane="url:${MR}"]`)).toBeVisible({ timeout: 15_000 })
  expect(await t.opens(), 'the preview opened no second tab').toEqual(OPENED)
  expect(t.dialogs).toEqual([])
})

test("Ctrl+click opens each kind of web link once, a hyperlink, one that shows its address and an address in the text, with no dialog", async ({ page, api, state }) => {
  const t = await linksPage(page, api, state.home, 'e2e terminal ctrl links')
  const want: string[][] = []
  for (const row of [ROW.hyperlink, ROW.hyperlinkShown, ROW.address]) {
    await t.overLink(row)
    await t.click(row, true)
    want.push(OPENED[0]!)
    // The open is made in the click's own handler: a second handler of the same click would have opened by the time the first shows.
    await expect.poll(async () => t.dialogs.length + (await t.opens()).length, { timeout: 15_000, message: `the click on row ${row} was handled` }).toBeGreaterThanOrEqual(want.length)
    expect(t.dialogs, 'a click on a link raises no dialog').toEqual([])
    expect(await t.opens(), `row ${row} opened once`).toEqual(want)
  }
})

test("the pointer over a hyperlink shows where it goes, its real address whatever its words, and nothing once it leaves", async ({ page, api, state }) => {
  const t = await linksPage(page, api, state.home, 'e2e terminal link hover')
  const tip = page.locator('[data-terminal-link-tip]')
  await t.overLink(ROW.hyperlink)
  await expect(tip).toHaveText(MR)
  // The row below holds no link (a file: hyperlink is none): the address goes.
  const p = t.at(ROW.file)
  await page.mouse.move(p.x, p.y)
  await expect(tip).toHaveCount(0)
  // An address in the text shows itself too.
  await t.overLink(ROW.address)
  await expect(tip).toHaveText(MR)
  expect(await t.opens()).toEqual([])
})

test('a drag that selects text across a link, or inside its words, is not a click: nothing opens', async ({ page, api, state }) => {
  const t = await linksPage(page, api, state.home, 'e2e terminal link drag')
  await t.overLink(ROW.hyperlink)
  // Inside the link's words alone ("the merge request"): from its second letter to its fourteenth, as a part is selected to copy.
  await t.drag({ row: ROW.hyperlink, col: 1 }, { row: ROW.hyperlink, col: 13 }, true)
  // From the row above (no link there) to after the link's words, over the link; and from after them back onto the link.
  await t.drag({ row: ROW.custom, col: 30 }, { row: ROW.hyperlink, col: 30 })
  await t.drag({ row: ROW.hyperlink, col: 30 }, { row: ROW.hyperlink, col: 3 })
  // A gate: a Ctrl+click on the link, handled after the drags were.
  await t.overLink(ROW.hyperlink)
  await t.click(ROW.hyperlink, true)
  await expect.poll(async () => t.dialogs.length + (await t.opens()).length, { timeout: 15_000, message: 'the gate was handled' }).toBeGreaterThan(0)
  expect(t.dialogs).toEqual([])
  expect(await t.opens(), 'only the click opened').toEqual(OPENED)
})

test('a double-click opens a link once, and a middle or a right press opens nothing', async ({ page, api, state }) => {
  const t = await linksPage(page, api, state.home, 'e2e terminal link presses')
  await t.overLink(ROW.hyperlink)
  // A double-click: its first press opens, its second (which selects a word) does nothing more.
  await t.click(ROW.hyperlink, false, { clickCount: 2 })
  await expect.poll(async () => t.dialogs.length + (await t.opens()).length, { timeout: 15_000, message: 'the double-click was handled' }).toBeGreaterThan(0)
  expect(await t.opens(), 'a double-click opens once').toEqual(OPENED)
  // A middle press, then a right press (its menu shown and closed).
  await t.overLink(ROW.hyperlink)
  await t.click(ROW.hyperlink, false, { button: 'middle' })
  await t.overLink(ROW.hyperlink)
  await t.click(ROW.hyperlink, false, { button: 'right' })
  await page.keyboard.press('Escape')
  await expect(page.getByRole('menu')).toHaveCount(0)
  // A gate: a click, handled after both presses were.
  await t.overLink(ROW.hyperlink)
  await t.click(ROW.hyperlink, true)
  await expect.poll(async () => (await t.opens()).length, { timeout: 15_000, message: 'the gate was handled' }).toBeGreaterThan(1)
  expect(t.dialogs).toEqual([])
  expect(await t.opens(), 'the presses opened nothing').toEqual([...OPENED, ...OPENED])
})

test('the hover draws where a link goes from its address: a long host whole and first, no sign-in part, a long path cut at its end', async ({ page, api, state }) => {
  // A narrow window, as a tile's: the host is longer than the line.
  await page.setViewportSize({ width: 640, height: 800 })
  const host = `${'part.'.repeat(30)}example.net`
  const path = `/${'segment/'.repeat(30)}the-end`
  const t = await linksPage(page, api, state.home, 'e2e terminal link tip', [`https://example.com@${host}${path} the merge request`])
  await t.overLink(0)
  const tip = page.locator('[data-terminal-link-tip]')
  const origin = tip.locator('[data-link-tip-origin]')
  const rest = tip.locator('[data-link-tip-rest]')
  await expect(origin).toHaveText(`https://${host}`)
  await expect(tip).not.toContainText('example.com')
  await expect(rest).toHaveText(/^\/segment\/segment\/.*…$/)
  // The host is all drawn: inside the tip, inside the window, nothing of it hidden.
  const m = await origin.evaluate((el) => {
    const r = el.getBoundingClientRect()
    const box = el.parentElement!.getBoundingClientRect()
    return { hidden: el.scrollWidth - el.clientWidth, left: r.left - box.left, right: box.right - r.right, inWindow: r.right <= window.innerWidth && r.left >= 0, lines: Math.round(r.height / parseFloat(getComputedStyle(el).lineHeight)) }
  })
  expect(m.hidden, 'nothing of the host is cut').toBeLessThanOrEqual(1)
  expect(m.left).toBeGreaterThanOrEqual(0)
  expect(m.right).toBeGreaterThanOrEqual(0)
  expect(m.inWindow).toBe(true)
  expect(m.lines, 'the host longer than the line wraps').toBeGreaterThan(1)
  expect(await t.opens()).toEqual([])
})

test('a hyperlink to a scheme that is not the web’s is no link: a click opens nothing, says nothing and asks nothing', async ({ page, api, state }) => {
  const t = await linksPage(page, api, state.home, 'e2e terminal other schemes')
  // The web hyperlink below them is drawn, so theirs are too.
  await t.overLink(ROW.hyperlink)
  for (const row of [ROW.file, ROW.script, ROW.custom]) {
    await t.click(row)
    await t.click(row, true)
  }
  // A gate: the web hyperlink's Ctrl+click, handled after every click above was.
  await t.overLink(ROW.hyperlink)
  await t.click(ROW.hyperlink, true)
  await expect.poll(async () => t.dialogs.length + (await t.opens()).length, { timeout: 15_000, message: 'the gate was handled' }).toBeGreaterThan(0)
  expect(t.dialogs).toEqual([])
  expect(await t.opens()).toEqual(OPENED)
  await expect(said(page)).toHaveCount(1)
})

test("elsewhere on the page a web link opens once with no dialog: Ctrl+click in the editor, a click in the Markdown preview", async ({ page, api, state }) => {
  const cwd = join(state.root, 'terminal-links-elsewhere')
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'NOTES.md'), `# Notes\n\nThe change is ${MR} and [the merge request](${MR}).\n`)
  // The preview's links are anchors that open a new tab of the browser's own: nothing leaves the machine, the address is answered here.
  await page.context().route('https://example.com/**', (r) => r.fulfill({ status: 200, contentType: 'text/plain', body: 'e2e' }))
  const tabs: string[] = []
  page.context().on('page', (p) => tabs.push(p.url()))
  const { dialogs, opens } = await watch(page)
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e links elsewhere', cwd })
  sessions.push(s.id)
  await page.goto(`/sessions/${encodeURIComponent(s.id)}`)
  await showView(page, 'explorer')
  const box = page.locator('[data-files-mode]').locator('[data-files-box] input, input[data-files-box]').first()
  await box.fill('NOTES.md')
  await box.press('Enter')
  const area = page.locator('[data-editor-area]')
  await expect(area).toHaveAttribute('data-editor-area', 'open')
  // The editor finds the address and makes it a link (Ctrl+click follows it): through window.open, once, never a dialog.
  const link = area.locator('.monaco-editor .detected-link').first()
  await expect(link).toBeVisible({ timeout: 30_000 })
  await link.click({ modifiers: ['ControlOrMeta'] })
  await expect.poll(async () => dialogs.length + (await opens()).length, { timeout: 15_000, message: "the editor's link was followed" }).toBeGreaterThan(0)
  expect(dialogs).toEqual([])
  expect((await opens()).map((o) => o[0])).toEqual([MR])
  // The preview: the link is an anchor to a new tab; a click opens one tab, once, with no dialog.
  await area.locator('[data-editor-preview]').click()
  const anchor = area.locator('[data-md-preview]').getByRole('link', { name: 'the merge request' })
  await expect(anchor).toHaveAttribute('href', MR)
  await expect(anchor).toHaveAttribute('target', '_blank')
  await anchor.click()
  await expect.poll(() => dialogs.length + tabs.length, { timeout: 15_000, message: "the preview's link was followed" }).toBeGreaterThan(0)
  expect(dialogs).toEqual([])
  expect(tabs).toHaveLength(1)
  expect((await opens()).map((o) => o[0])).toEqual([MR])
})
