import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Page } from '@playwright/test'
import { expect, showView, test, type Api, type Session } from './fixtures'

// A link in a session's terminal opens through the app's own handler, never through a dialog (#1020). An agent prints a hyperlink
// with OSC 8 (its words a link to an address they need not show); xterm, given no handler of its own for those, asked "Do you want
// to navigate to …? WARNING: This link could potentially be dangerous" with the page's confirm, which the desktop app shows as a
// native dialog titled with its name. Now a hyperlink is handled as an address the terminal finds in the text is: a click offers
// it in a toast (Open in new tab, Preview in pane), Ctrl+click (⌘ on a Mac) opens it at once in a new tab, once; a hyperlink to any
// other scheme than http or https is no link at all. The stub agent prints the links (`stub links`, web/e2e/stub-agent.sh) on the
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
].join('|')

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
async function linksPage(page: Page, api: Api, home: string, name: string) {
  const { dialogs, opens } = await watch(page)
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name })
  sessions.push(s.id)
  await page.goto(`/sessions/${encodeURIComponent(s.id)}`)
  await expect(page.locator('[data-transport-state]').first()).toHaveAttribute('data-transport-state', 'open', { timeout: 30_000 })
  const terminal = page.locator('.terminal-host .xterm-helper-textarea').first()
  await terminal.focus()
  await page.keyboard.type(`stub links ${LINKS}`)
  await page.keyboard.press('Enter')
  await expect.poll(async () => transcript(home, await api.session(s.id)), { timeout: 30_000, message: 'the stub printed its links' }).toContain('links: printed 6\n')
  const host = page.locator('.terminal-host').first()
  const box = (await host.boundingBox())!
  const rows = Number(await host.getAttribute('data-rows'))
  const cols = Number(await host.getAttribute('data-cols'))
  const at = (row: number) => ({ x: box.x + (box.width / cols) * 3.5, y: box.y + (box.height / rows) * (row + 0.5) })
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
  const click = async (row: number, ctrl = false) => {
    const p = at(row)
    if (ctrl) await page.keyboard.down('Control')
    await page.mouse.click(p.x, p.y)
    if (ctrl) await page.keyboard.up('Control')
  }
  return { s, dialogs, overLink, click, opens }
}

/** The toasts that offer an address the terminal showed: titled with it, Open in new tab and Preview in pane. */
const offers = (page: Page, url: string) => page.locator('li').filter({ hasText: url }).filter({ has: page.getByRole('button', { name: 'Open in new tab' }) })

test('a click on a hyperlink an agent printed raises no dialog: it offers the address, and Open in new tab opens it once', async ({ page, api, state }) => {
  const t = await linksPage(page, api, state.home, 'e2e terminal hyperlink')
  await t.overLink(ROW.hyperlink)
  await t.click(ROW.hyperlink)
  // Whichever the click brings, a dialog or the offer, comes from the click's own handler.
  await expect.poll(async () => t.dialogs.length + (await offers(page, MR).count()), { timeout: 15_000, message: 'the click was handled' }).toBeGreaterThan(0)
  expect(t.dialogs, 'a click on a link raises no dialog').toEqual([])
  await expect(offers(page, MR)).toHaveCount(1)
  expect(await t.opens(), 'a plain click opens nothing by itself').toEqual([])
  await offers(page, MR).getByRole('button', { name: 'Open in new tab' }).click()
  await expect.poll(() => t.opens().then((o) => o.length), { timeout: 15_000 }).toBeGreaterThan(0)
  expect(await t.opens()).toEqual([[MR, '_blank', 'noopener,noreferrer']])
  expect(t.dialogs).toEqual([])
})

test("Ctrl+click opens each kind of web link once, a hyperlink, one that shows its address and an address in the text, with no dialog", async ({ page, api, state }) => {
  const t = await linksPage(page, api, state.home, 'e2e terminal ctrl links')
  const want: string[][] = []
  for (const row of [ROW.hyperlink, ROW.hyperlinkShown, ROW.address]) {
    await t.overLink(row)
    await t.click(row, true)
    want.push([MR, '_blank', 'noopener,noreferrer'])
    // The open is made in the click's own handler: a second handler of the same click would have opened by the time the first shows.
    await expect.poll(async () => t.dialogs.length + (await t.opens()).length, { timeout: 15_000, message: `the click on row ${row} was handled` }).toBeGreaterThanOrEqual(want.length)
    expect(t.dialogs, 'a click on a link raises no dialog').toEqual([])
    expect(await t.opens(), `row ${row} opened once`).toEqual(want)
  }
  await expect(offers(page, MR)).toHaveCount(0)
})

test('a hyperlink to a scheme that is not the web’s is no link: a click opens nothing, offers nothing and asks nothing', async ({ page, api, state }) => {
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
  expect(await t.opens()).toEqual([[MR, '_blank', 'noopener,noreferrer']])
  await expect(page.locator('li').filter({ has: page.getByRole('button', { name: 'Open in new tab' }) })).toHaveCount(0)
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
