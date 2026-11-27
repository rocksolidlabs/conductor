import type { Locator, Page } from '@playwright/test'
import { expect, logged, member, test, type Run } from './fixtures'
import { noOverlap } from './layout'

// The crew graph: the run page draws the members with their start rules and
// the handoffs as they are delivered; the editor sets a start rule by
// drawing an edge and refuses a second parent; a phone gets the list.
test.describe.configure({ mode: 'serial' })

let crewId = ''
let runId = ''

const crewBody = {
  name: 'e2e graph',
  goal: 'graph',
  cwd: '',
  where: 'server',
  isolation: 'none',
  openAfterLaunch: false,
  members: [
    { name: 'lead', agentId: 'claude', prompt: 'say hello', start: { when: 'immediately' } },
    { name: 'core', agentId: 'claude', prompt: 'build the core', start: { when: 'after', member: 'lead' } },
    { name: 'tests', agentId: 'codex', prompt: 'write the tests', start: { when: 'after', member: 'core' } },
    { name: 'docs', agentId: 'claude', prompt: 'write the docs', start: { when: 'manual' } },
  ],
}

test.beforeAll(async ({ api }) => {
  crewId = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', crewBody)).crew.id
})

test.afterAll(async ({ api }) => {
  if (runId) await api.stopRun(runId)
  if (crewId) await api.call('DELETE', `/api/crews/${encodeURIComponent(crewId)}`)
})

test('the run page draws the members and their start rules, and a handoff as it is delivered', async ({ page, api }) => {
  const run = await api.launchCrew(crewId)
  runId = run.id
  let r: Run = run
  await expect
    .poll(async () => {
      r = await api.run(runId)
      return logged(r, 'core is done: starting tests')
    }, { timeout: 60_000, message: 'tests started after core' })
    .toBe(true)

  await page.goto(`/runs/${encodeURIComponent(runId)}`)
  await page.locator('[data-run-view]').getByRole('tab', { name: 'Graph' }).click()
  const canvas = page.locator('[data-graph-canvas]')
  await expect(canvas).toBeVisible()
  await expect(canvas.locator('[data-graph-node]')).toHaveCount(4)
  await expect(canvas.locator('[data-graph-node="lead"]')).toHaveAttribute('data-status', 'running')
  await expect(canvas.locator('[data-graph-node="docs"]')).toHaveAttribute('data-status', 'pending')
  // Each node draws its state in the status language (UX1 PR-10): lead its session's key, docs, not started, the waiting mark.
  // lead's mark is its session's, the one the sidebar's row draws for that session: both read at once, the state may move.
  const leadSession = r.members.find((m) => m.name === 'lead')?.sessionId ?? ''
  expect(leadSession, "lead's session").not.toBe('')
  const keys = () =>
    page.evaluate((id) => {
      const node = document.querySelector('[data-graph-canvas] [data-graph-node="lead"] [data-status-mark]')?.getAttribute('data-status-mark') ?? ''
      const row = document.querySelector(`[data-sidebar-row="s:${id}"] [data-status-mark]`)?.getAttribute('data-status-mark') ?? ''
      return node && node === row ? node : `${node} against ${row}`
    }, leadSession)
  await expect.poll(keys, { message: "lead's node draws its session's mark" }).toMatch(/^(working|finished|running|needs)$/)
  await expect(canvas.locator('[data-graph-node="docs"] [data-status-mark]')).toHaveAttribute('data-status-mark', 'waiting')
  // The Crews pages' one vocabulary (memberLook): a member not started yet.
  await expect(canvas.locator('[data-graph-node="docs"] [data-status-word]')).toHaveText('Waiting to start')
  await expect(canvas.locator('[data-graph-edge-kind="after"]')).toHaveCount(2)
  await expect(canvas.locator('[data-graph-edge="lead-core"][data-graph-edge-kind="after"]')).toHaveCount(1)
  await expect(canvas.locator('[data-graph-edge="core-tests"][data-graph-edge-kind="after"]')).toHaveCount(1)
  // Roots left: lead's node sits left of core's, which sits left of tests'.
  const x = async (name: string) => (await canvas.locator(`[data-graph-node="${name}"]`).boundingBox())!.x
  expect(await x('lead')).toBeLessThan(await x('core'))
  expect(await x('core')).toBeLessThan(await x('tests'))

  // A handoff from lead to core (the stub reports one for a line that names it) is drawn once delivered.
  const lead = member(r, 'lead').sessionId ?? ''
  await page.goto(`/sessions/${encodeURIComponent(lead)}`)
  await page.locator('.terminal-host .xterm-screen').first().click()
  await page.keyboard.type('please --event handoff --to core now')
  await page.keyboard.press('Enter')
  await expect.poll(async () => logged(await api.run(runId), 'handoff delivered from lead to core'), { timeout: 30_000 }).toBe(true)
  await page.goto(`/runs/${encodeURIComponent(runId)}`)
  await page.locator('[data-run-view]').getByRole('tab', { name: 'Graph' }).click()
  const handoff = page.locator('[data-graph-canvas] [data-graph-edge="lead-core"][data-graph-edge-kind="handoff"]')
  await expect(handoff).toHaveCount(1, { timeout: 15_000 })
  const handoffLabel = page.locator('[data-graph-edge-label="lead-core"][data-graph-label-kind="handoff"]')
  await expect(handoffLabel.locator('[data-graph-handoff-count]')).toHaveText('1')
  // The handoff arcs off the "after" edge between the same two: its badge never covers the "when done" label.
  const afterBox = (await page.locator('[data-graph-edge-label="lead-core"][data-graph-label-kind="after"]').boundingBox())!
  const handoffBox = (await handoffLabel.boundingBox())!
  expect(Math.abs(afterBox.y - handoffBox.y)).toBeGreaterThanOrEqual(10)

  // Start now on the manual member works from the node.
  await page.locator('[data-graph-node="docs"]').getByRole('button', { name: 'Start now' }).click()
  await expect.poll(async () => member(await api.run(runId), 'docs').status, { timeout: 30_000 }).not.toBe('pending')
})

/**
 * The middle of a handle, pointed at in view and at rest: the canvas is scrolled to the middle of the window (the crew page is taller
 * than it, its graph under Setup), the handle's box read twice the same, and the point checked to land on the handle itself, never on
 * what covers it (a toast, the unsaved-changes bar).
 */
async function pointAt(page: Page, canvas: Locator, handle: Locator): Promise<{ x: number; y: number }> {
  await canvas.evaluate((el) => el.scrollIntoView({ block: 'center', behavior: 'instant' }))
  let at = { x: 0, y: 0 }
  await expect(async () => {
    const one = (await handle.boundingBox())!
    await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))))
    const two = (await handle.boundingBox())!
    expect(two, 'the handle is at rest').toEqual(one)
    at = { x: two.x + two.width / 2, y: two.y + two.height / 2 }
    const hit = await handle.evaluate((el, p) => {
      const top = document.elementFromPoint(p.x, p.y)
      return !!top && (el === top || el.contains(top))
    }, at)
    expect(hit, 'the point lands on the handle, in view and uncovered').toBe(true)
  }).toPass({ timeout: 15_000 })
  return at
}

test('the editor sets a start rule by drawing an edge, and refuses a second parent', async ({ page, api }) => {
  await page.goto(`/crews/${encodeURIComponent(crewId)}`)
  await page.locator('[data-members-view]').getByRole('tab', { name: 'Graph' }).click()
  const canvas = page.locator('[data-graph-canvas]')
  await expect(canvas.locator('[data-graph-node]')).toHaveCount(4)
  await expect(page.locator('[data-graph-mode]')).toHaveAttribute('data-graph-mode', 'edit')

  // Drag from tests' source handle to docs' target handle: docs starts after tests.
  const from = canvas.locator('[data-graph-node="tests"] .vue-flow__handle.source')
  const to = canvas.locator('[data-graph-node="docs"] .vue-flow__handle.target')
  const a = await pointAt(page, canvas, from)
  const b = await pointAt(page, canvas, to)
  await page.mouse.move(a.x, a.y)
  await page.mouse.down()
  await page.mouse.move(b.x, b.y, { steps: 12 })
  await page.mouse.up()
  await expect(canvas.locator('[data-graph-edge="tests-docs"][data-graph-edge-kind="after"]')).toHaveCount(1)
  await page.locator('[data-unsaved-bar]').getByRole('button', { name: 'Save', exact: true }).click()
  await expect.poll(async () => (await api.crew(crewId)).members.find((m) => m.name === 'docs')?.start, { timeout: 15_000 }).toEqual({ when: 'after', member: 'tests' })

  // A second parent for docs is refused, and said.
  const from2 = canvas.locator('[data-graph-node="lead"] .vue-flow__handle.source')
  // The save's toast sits over the window's bottom right, where the graph can be: the points are checked uncovered.
  const a2 = await pointAt(page, canvas, from2)
  const b2 = await pointAt(page, canvas, to)
  await page.mouse.move(a2.x, a2.y)
  await page.mouse.down()
  await page.mouse.move(b2.x, b2.y, { steps: 12 })
  await page.mouse.up()
  await expect(page.getByText('docs already starts after tests: delete that edge first').first()).toBeVisible()
  await expect(canvas.locator('[data-graph-edge="lead-docs"]')).toHaveCount(0)

  // The × on the edge removes the rule: docs starts immediately.
  await canvas.locator('[data-graph-edge-label="tests-docs"][data-graph-label-kind="after"] [data-graph-edge-delete]').click()
  await expect(canvas.locator('[data-graph-edge="tests-docs"]')).toHaveCount(0)
  await page.locator('[data-unsaved-bar]').getByRole('button', { name: 'Save', exact: true }).click()
  await expect.poll(async () => (await api.crew(crewId)).members.find((m) => m.name === 'docs')?.start, { timeout: 15_000 }).toEqual({ when: 'immediately' })
})

test('two graphs, two stores: the crew editor’s and its run’s, reached without a reload, read different Vue Flow stores (#449)', async ({ page }) => {
  // The run page opens on its Graph view: a run of members draws Members mode (it has no tasks).
  await page.addInitScript(() => localStorage.setItem('conductor.runView', 'graph'))
  await page.goto(`/crews/${encodeURIComponent(crewId)}`)
  await page.locator('[data-members-view]').getByRole('tab', { name: 'Graph' }).click()
  const canvas = page.locator('[data-graph-canvas]')
  await expect(canvas.locator('[data-graph-node]')).toHaveCount(4)
  const editor = await canvas.getAttribute('data-graph-store')
  expect(editor).toMatch(/^crew-graph-\d+$/)
  // To the crew's run by its link in the Runs tab: a change of route in the page, never a load (a load would count from 1 again).
  await page.evaluate(() => ((window as unknown as { loadedOnce: boolean }).loadedOnce = true))
  await page.locator('[data-crew-tabs]').getByRole('tab', { name: /^Runs/ }).click()
  await page.locator(`[data-run="${runId}"] [data-run-open]`).click()
  await expect(page).toHaveURL(new RegExp(`/runs/${encodeURIComponent(runId)}`))
  await expect(page.locator('[data-run-view]').getByRole('tab', { name: 'Graph' })).toHaveAttribute('aria-selected', 'true')
  await expect(canvas.locator('[data-graph-node]')).toHaveCount(4)
  const run = await canvas.getAttribute('data-graph-store')
  expect(run).toMatch(/^crew-graph-\d+$/)
  expect(run).not.toBe(editor)
  expect(await page.evaluate(() => (window as unknown as { loadedOnce?: boolean }).loadedOnce)).toBe(true)
})

test('a phone gets the list, indented by depth', async ({ browser, state }) => {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 } })
  const page = await context.newPage()
  await page.addInitScript((token) => localStorage.setItem('conductor.workbenchToken', token), state.token)
  await page.goto(`/runs/${encodeURIComponent(runId)}`)
  await page.locator('[data-run-view]').getByRole('tab', { name: 'Graph' }).click()
  const list = page.locator('[data-graph-list]')
  await expect(list).toBeVisible()
  await expect(page.locator('[data-graph-canvas]')).toHaveCount(0)
  await expect(list.locator('[data-graph-list-row="core"]')).toHaveAttribute('data-depth', '1')
  await expect(list.locator('[data-graph-list-row="tests"]')).toHaveAttribute('data-depth', '2')
  // The run header keeps the name clear of the buttons; what the run is doing takes its own row, under the bar.
  await expect(page.locator('[data-crew-run-header] [data-run-summary]')).toBeVisible()
  await noOverlap(page.locator('[data-crew-run-header] [data-run-row-one]'))
  await context.close()
})
