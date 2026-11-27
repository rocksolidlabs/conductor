import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import type { Locator, Page, Route } from '@playwright/test'
import { DAILY, atRest, dayPage, expect, openMore, test } from './fixtures'
import { ownServer, type OwnServer } from './own-server'

// Launch agent's Extra arguments offers the agent's own flags as the person types, then the values of the flag picked (UX3 R48,
// issue 1172): read by the server from the agent's own help (GET /api/catalog/{id}/flags). On a server of its own, with copies of
// the Claude Code and Codex stubs that print a help in each tool's form (STUB_HELP, in our own words), and the Plain stub, whose
// agent has no recipe. Launches are held by a route and refused, so no session is made.

let own: OwnServer

test.describe.configure({ mode: 'serial' })

const CLAUDE = 'Claude Code (e2e stub), flags'
const CODEX = 'Codex CLI (e2e stub), codex-flags'
const PLAIN = 'Plain (e2e stub)'

test.beforeAll(async () => {
  test.setTimeout(120_000)
  own = await ownServer('launch-flags', {
    agents: [
      // A version with the chat way, so that a flag a chat refuses is marked.
      { id: 'flags', from: 'claude', env: { STUB_HELP: 'commander', STUB_CLAUDE_VERSION: '2.1.332' }, protocol: { name: 'claude-stream-json' } },
      { id: 'codex-flags', from: 'codex', env: { STUB_HELP: 'clap' } },
    ],
    keep: ['plain'],
  })
  // The server's read of each help ends before a page asks: the field's own asks of a list still being read are proved apart.
  for (const id of ['flags', 'codex-flags']) {
    await expect.poll(async () => (await own.api.call<{ state: string }>('GET', `/api/catalog/${id}/flags`)).body.state, { timeout: 60_000 }).toBe('read')
  }
})

test.afterAll(async () => {
  await own.stop()
})

async function openLaunch(page: Page): Promise<Locator> {
  await page.getByRole('button', { name: 'Launch agent' }).first().click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('button', { name: 'Launch', exact: true })).toBeVisible()
  return dialog
}

async function pick(dialog: Locator, name: string) {
  const radio = dialog.getByRole('radiogroup', { name: 'Agent' }).getByRole('radio', { name: new RegExp(`${name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`) })
  await radio.click()
  await expect(radio).toHaveAttribute('aria-checked', 'true')
}

/** The dialog on agent `name`, More options open, and its Extra arguments field once the agent's flags have been answered. */
async function argsOf(page: Page, name: string, id: string): Promise<{ dialog: Locator; field: Locator }> {
  // Asked as the agent is picked, or as the dialog opens on it: waited for from before either.
  const answered = page.waitForResponse((r) => new URL(r.url()).pathname === `/api/catalog/${id}/flags`)
  const dialog = await openLaunch(page)
  await pick(dialog, name)
  await answered
  const more = await openMore(dialog)
  const field = more.locator('input[data-launch-args]')
  await expect(field).toBeVisible()
  return { dialog, field }
}

/** The rows the list shows, by the flag or the value each puts in, once the list is open and at rest. */
async function rows(page: Page): Promise<string[]> {
  return rowsOf(page, '[data-arg-row]')
}

/** The marks `mark` names in the open list, at rest: each row's own word. */
async function rowsOf(page: Page, mark: string): Promise<string[]> {
  const list = page.getByRole('listbox')
  await expect(list).toBeVisible()
  await atRest(list)
  const name = mark.slice(1, -1)
  return list.locator(mark).evaluateAll((els, n) => els.map((e) => e.getAttribute(n) ?? e.textContent ?? ''), name)
}

/** Moves the highlight with Arrow Down until it is on the row of `key`, then returns it. */
async function highlight(page: Page, key: string): Promise<void> {
  const on = page.locator('[role="option"][data-highlighted] [data-arg-row]')
  for (let i = 0; i < 20; i++) {
    if ((await on.count()) && (await on.getAttribute('data-arg-row')) === key) return
    await page.keyboard.press('ArrowDown')
  }
  throw new Error(`the highlight never reached ${key}`)
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

test("typing -- lists the agent's own flags, Tab and Enter take one, its values follow, and what is typed is sent as typed", async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, ...DAILY })
  try {
    const { dialog, field } = await argsOf(page, CLAUDE, 'flags')
    await expect(field).toHaveAttribute('placeholder', 'Type -- to see its flags')
    // The focus alone opens nothing.
    await field.click()
    await expect(field).toBeFocused()
    await expect(page.getByRole('listbox')).toHaveCount(0)

    await field.pressSequentially('--')
    // The help's order; left out, what the dialog sets itself: the model flag (its Model field), the yolo flag, the session's id,
    // --settings, --help and --version.
    const modelField = (await dialog.locator('[data-launch-model-input]').count()) > 0
    expect(await rows(page)).toEqual(['--add-dir', '--effort', ...(modelField ? [] : ['--model']), '--permission-mode', '--tone', '--verbose'])
    // The flags a chat refuses, so that a session launched with one stays in its terminal, are marked.
    const terminalOnly = page.getByRole('listbox').locator('[role="option"]').filter({ has: page.locator('[data-arg-terminal]') }).locator('[data-arg-row]')
    expect(await terminalOnly.evaluateAll((els) => els.map((e) => e.getAttribute('data-arg-row')))).toEqual(['--add-dir', '--permission-mode', '--verbose'])

    // Tab takes the highlighted row, the first; the focus stays in the field, the cursor after the flag and its space.
    await highlight(page, '--add-dir')
    await page.keyboard.press('Tab')
    await expect(field).toHaveValue('--add-dir ')
    await expect(field).toBeFocused()
    // (Its value lists folders: the test of a folder flag below.)

    // The arrows and Enter take another; after a flag whose help lists its values, they show at once.
    await field.fill('')
    await field.pressSequentially('--')
    await highlight(page, '--effort')
    await page.keyboard.press('Enter')
    await expect(field).toHaveValue('--effort ')
    expect(await rows(page)).toEqual(['low', 'medium', 'high'])
    await highlight(page, 'medium')
    await page.keyboard.press('Enter')
    await expect(field).toHaveValue('--effort medium ')

    // Letters narrow the list; the value that turns asking off is not offered.
    await field.pressSequentially('--perm')
    expect(await rows(page)).toEqual(['--permission-mode'])
    await page.keyboard.press('Tab')
    await expect(field).toHaveValue('--effort medium --permission-mode ')
    expect(await rows(page)).toEqual(['ask', 'plan'])
    // Escape closes the list, and the dialog stays.
    await page.keyboard.press('Escape')
    await expect(page.getByRole('listbox')).toHaveCount(0)
    await expect(dialog).toBeVisible()
    await expect(field).toBeFocused()

    // Typed by hand, the value that turns asking off is taken, and the line under the field points at the Yolo switch.
    await field.pressSequentially('bypassPermissions')
    await expect(dialog.locator('[data-args-yolo]')).toHaveText('Use the Yolo switch for this.')
    await expect(page.getByRole('listbox')).toHaveCount(0)
    // With the list closed, Enter is the form's: the launch carries the arguments exactly as the field holds them.
    const launches = await holdLaunches(page)
    await page.keyboard.press('ControlOrMeta+Enter')
    await expect.poll(() => launches.held.length, { message: 'the launch was sent' }).toBe(1)
    expect(launches.held[0]!.request().postDataJSON()).toMatchObject({ agentId: 'flags', args: ['--effort', 'medium', '--permission-mode', 'bypassPermissions'] })
    await launches.refuse()
  } finally {
    await page.context().close()
  }
})

test("Codex's help in clap's form: its values follow its flag, what the launch takes is not offered, and the list is in view", async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  try {
    const { field } = await argsOf(page, CODEX, 'codex-flags')
    await field.click()
    await field.pressSequentially('--')
    const keys = await rows(page)
    // --cd would move the agent out of the folder the launch checked; the yolo flag is the switch's.
    expect(keys).not.toContain('--cd')
    expect(keys).not.toContain('--dangerously-bypass-approvals-and-sandbox')
    expect(keys).toContain('--sandbox')
    const box = (await page.getByRole('listbox').boundingBox())!
    expect(box.y).toBeGreaterThanOrEqual(0)
    expect(box.y + box.height).toBeLessThanOrEqual(900)
    await field.pressSequentially('sa')
    await page.keyboard.press('Enter')
    await expect(field).toHaveValue('--sandbox ')
    expect(await rows(page)).toEqual(['read-only', 'workspace-write'])
    // A flag the list does not know is typed and kept as it is.
    await field.fill('--sandbox read-only --not-a-flag')
    await expect(field).toHaveValue('--sandbox read-only --not-a-flag')
  } finally {
    await page.context().close()
  }
})

test('an agent whose flags Conductor has not read offers no list, and its typed flags are sent as typed', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, ...DAILY })
  try {
    const { field } = await argsOf(page, PLAIN, 'plain')
    await expect(field).toHaveAttribute('placeholder', 'Flags, as you would type them in a terminal')
    await field.click()
    await field.pressSequentially('--verbose')
    await expect(field).toHaveValue('--verbose')
    // Its answer is in (argsOf waited for it), and a key's list is drawn as it is pressed: no list is a list that was not drawn.
    await expect(page.getByRole('listbox')).toHaveCount(0)
    const launches = await holdLaunches(page)
    await page.keyboard.press('ControlOrMeta+Enter')
    await expect.poll(() => launches.held.length, { message: 'the launch was sent' }).toBe(1)
    expect(launches.held[0]!.request().postDataJSON()).toMatchObject({ agentId: 'plain', args: ['--verbose'] })
    await launches.refuse()
  } finally {
    await page.context().close()
  }
})

test("the Model field offers the model names the agent's help gives, narrows as typed, and takes any other name as typed", async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  try {
    const { dialog } = await argsOf(page, CLAUDE, 'flags')
    const model = dialog.locator('input[data-launch-model-input]')
    await expect(model).toBeVisible()
    // The names list opens on the focus.
    await model.click()
    const names = () => rowsOf(page, '[data-model-name]')
    expect(await names()).toEqual(['alpha', 'beta'])
    await model.pressSequentially('be')
    expect(await names()).toEqual(['beta'])
    await page.keyboard.press('Enter')
    await expect(model).toHaveValue('beta')
    await expect(page.getByRole('listbox')).toHaveCount(0)
    // A name the help does not give is taken as typed.
    await model.fill('gamma-2')
    await expect(page.getByRole('listbox')).toHaveCount(0)
    const launches = await holdLaunches(page)
    await page.keyboard.press('ControlOrMeta+Enter')
    await expect.poll(() => launches.held.length, { message: 'the launch was sent' }).toBe(1)
    expect(launches.held[0]!.request().postDataJSON()).toMatchObject({ agentId: 'flags', model: 'gamma-2' })
    await launches.refuse()
  } finally {
    await page.context().close()
  }
})

test("Brakeman's Model field in Settings offers the names its agent's help gives", async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, path: '/settings', width: 1440, height: 900 })
  try {
    const c = page.locator('[data-brakeman-settings]')
    await expect(c).toBeVisible()
    const on = c.getByRole('switch', { name: 'Brakeman on' })
    if ((await on.getAttribute('aria-checked')) !== 'true') await on.click()
    const answered = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/catalog/flags/flags')
    await c.locator('[data-brakeman-agent]').click()
    await page.getByRole('option', { name: CLAUDE }).click()
    await expect(page.getByRole('listbox')).toHaveCount(0)
    await answered
    const model = c.locator('input[data-brakeman-model]')
    await expect(model).toBeVisible()
    await model.click()
    expect(await rowsOf(page, '[data-model-name]')).toEqual(['alpha', 'beta'])
    await page.keyboard.press('Escape')
    await expect(page.getByRole('listbox')).toHaveCount(0)
  } finally {
    await page.context().close()
  }
})

test("each trial agent's Arguments offers its own flags, the model flag and its names among them, and its chip shows the line", async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, path: '/crews', width: 1440, height: 900 })
  try {
    await page.locator('[data-new-comparison]').click()
    const form = page.locator('[data-compare-template]')
    await expect(form).toBeVisible()
    await expect(form.locator('[data-trial-agent="1"]')).toBeVisible()
    const more = await openMore(form)
    // Agent A is the Claude Code stub's copy: the form has no Model field, so its model flag and the names its help gives are offered.
    const a = more.locator('input[data-compare-agent-args="0"]')
    await expect(a).toHaveAttribute('placeholder', 'Type -- to see its flags')
    await a.click()
    await a.pressSequentially('--mo')
    expect(await rows(page)).toEqual(['--model', '--permission-mode'])
    await highlight(page, '--model')
    await page.keyboard.press('Tab')
    expect(await rows(page)).toEqual(['alpha', 'beta'])
    await page.keyboard.press('Escape')
    await expect(page.getByRole('listbox')).toHaveCount(0)
    // Agent B is the Codex stub's copy: its own flags, and its chip shows the line as typed.
    const b = more.locator('input[data-compare-agent-args="1"]')
    await b.click()
    await b.pressSequentially('--sa')
    await page.keyboard.press('Enter')
    await expect(b).toHaveValue('--sandbox ')
    await highlight(page, 'read-only')
    await page.keyboard.press('Enter')
    await expect(b).toHaveValue('--sandbox read-only ')
    await expect(form.locator('[data-trial-agent="1"] [data-trial-agent-args]')).toHaveText('--sandbox read-only')
  } finally {
    await page.context().close()
  }
})

test("a folder flag's value lists the folders beside the session's, a pick goes on to its children, and a path outside is sent as typed", async ({ browser }) => {
  const shared = join(own.root, 'shared')
  mkdirSync(join(shared, 'inner'), { recursive: true })
  const page = await dayPage(browser, { server: own.started, ...DAILY })
  try {
    const { field } = await argsOf(page, CLAUDE, 'flags')
    await field.click()
    await field.pressSequentially('--add-dir ')
    // Nothing typed: the folders beside the session's (the server's default folder, the scratch repository, under the root).
    await expect.poll(() => rows(page)).toContain(shared)
    await highlight(page, shared)
    await page.keyboard.press('Enter')
    await expect(field).toHaveValue(`--add-dir ${shared}`)
    // The list goes on to the folder's children.
    await expect.poll(() => rows(page)).toEqual([join(shared, 'inner')])
    // A space ends the value: the list closes.
    await field.pressSequentially(' ')
    await expect(page.getByRole('listbox')).toHaveCount(0)
    // A path outside the folders agents may use lists nothing, and is sent as typed.
    await field.fill('--add-dir /')
    const listed = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/paths' && new URL(r.url()).searchParams.get('prefix') === '/e')
    await field.pressSequentially('e')
    await expect(field).toHaveValue('--add-dir /e')
    // Its listing answered: no list is a list that was not drawn.
    await listed
    const launches = await holdLaunches(page)
    await expect(page.getByRole('listbox')).toHaveCount(0)
    await page.keyboard.press('ControlOrMeta+Enter')
    await expect.poll(() => launches.held.length, { message: 'the launch was sent' }).toBe(1)
    expect(launches.held[0]!.request().postDataJSON()).toMatchObject({ agentId: 'flags', args: ['--add-dir', '/e'] })
    await launches.refuse()
  } finally {
    await page.context().close()
  }
})

test("a crew member's Extra arguments and Model offer its agent's flags and names", async ({ browser }) => {
  const { body } = await own.api.call<{ crew: { id: string } }>('POST', '/api/crews', { name: 'flags-crew', goal: '', cwd: '', where: 'server', isolation: 'none', openAfterLaunch: false, yolo: false, members: [{ name: 'lead', agentId: 'flags', prompt: '', start: { when: 'manual' } }] })
  const page = await dayPage(browser, { server: own.started, path: `/crews/${body.crew.id}`, width: 1440, height: 900 })
  try {
    const row = page.locator('[data-crew-member]').first()
    await expect(row).toBeVisible()
    // The member's fields ask for its agent's flags as the page draws them; the list opens once the answer is in.
    await row.locator('[data-member-menu]').click()
    await page.getByRole('menuitem', { name: 'Extra arguments' }).click()
    await expect(page.getByRole('menu')).toHaveCount(0)
    const args = row.locator('input[data-member-args]')
    await args.click()
    await args.pressSequentially('--')
    // The member has a Model field: its model flag is the field's.
    expect(await rows(page)).toEqual(['--add-dir', '--effort', '--permission-mode', '--tone', '--verbose'])
    await page.keyboard.press('Escape')
    await expect(page.getByRole('listbox')).toHaveCount(0)
    const model = row.locator('input[data-member-model]')
    await model.click()
    expect(await rowsOf(page, '[data-model-name]')).toEqual(['alpha', 'beta'])
    await page.keyboard.press('Escape')
    await expect(page.getByRole('listbox')).toHaveCount(0)
  } finally {
    await page.context().close()
    await own.api.call('DELETE', `/api/crews/${encodeURIComponent(body.crew.id)}`)
  }
})

/** Whether the point at the centre of `l` is `l` itself or inside it: nothing drawn over it there. */
async function uncovered(l: Locator): Promise<boolean> {
  return l.evaluate((el) => {
    const r = el.getBoundingClientRect()
    const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2)
    return !!at && (el === at || el.contains(at))
  })
}

for (const size of [{ width: 1440, height: 900 }, DAILY]) {
  test(`at ${size.width} x ${size.height} a flag's values open above Extra arguments: Yolo and Launch stay uncovered, and the list is whole in the window`, async ({ browser }) => {
    const page = await dayPage(browser, { server: own.started, ...size })
    try {
      const { dialog, field } = await argsOf(page, CLAUDE, 'flags')
      await field.click()
      await field.pressSequentially('--eff')
      await page.keyboard.press('Tab')
      await expect(field).toHaveValue('--effort ')
      expect(await rows(page)).toEqual(['low', 'medium', 'high'])
      const list = page.getByRole('listbox')
      const box = (await list.boundingBox())!
      const at = (await field.boundingBox())!
      expect(box.y + box.height, 'the list opens above the field').toBeLessThanOrEqual(at.y + 1)
      expect(box.y).toBeGreaterThanOrEqual(0)
      expect(box.x).toBeGreaterThanOrEqual(0)
      expect(box.x + box.width).toBeLessThanOrEqual(size.width)
      const yolo = dialog.locator('[data-yolo-switch]')
      const launch = dialog.getByRole('button', { name: 'Launch', exact: true })
      await expect(yolo).toBeVisible()
      expect(await uncovered(yolo), 'the Yolo switch is not covered').toBe(true)
      expect(await uncovered(launch), 'Launch is not covered').toBe(true)
    } finally {
      await page.context().close()
    }
  })
}
