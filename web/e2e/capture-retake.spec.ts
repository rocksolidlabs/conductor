import { copyFileSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { Locator, Page } from '@playwright/test'
import { expect, git, stopStrays, task, test } from './fixtures'
import { noSideways } from './layout'
import { endMade, made, pick, previewFrame, previewSeam, scratchDir, stepIn } from './preview-fixtures'
import { scratchRepo } from './server'

// Capture again across crews (docs/plans/browser-evidence.md, "A candidate's preview": the first capture "may be of another run, as a
// ticket's repair is"). The capture that makes a ticket is taken in a run of one crew whose task is `look`; the ticket's repair is a
// run of ANOTHER crew whose one task is `fix`, launched from the ticket's plan. The ticket's capture offers Capture again on fix#1, the
// retake is kept with the first as its retakeOf (retakeVia ticket), and once it is sent to the ticket the two captures are shown side
// by side, before and after, on a wide screen and one above the other on a phone, nothing scrolling sideways. The broken page, its
// service and the capture-fixing stub are the repair loop's (web/e2e/fixtures/repair/, read here as they are). Every wait is on what
// the page draws or what the server answers.
test.describe.configure({ mode: 'serial' })

const REPAIR_SERVICE = fileURLToPath(new URL('./fixtures/repair/serve.py', import.meta.url))
const REPAIR_PAGE = fileURLToPath(new URL('./fixtures/repair/page.html', import.meta.url))

/**
 * The waits this walk takes, each bounded as capture-repair.spec.ts bounds them: the app's start with the bridge (60 s), the launch from
 * the ticket to a submitted candidate (240 s), the candidate's checkout made with its manifest (120 s). The test's timeout is above
 * their sum. The setup's: stopStrays 15 s for each stray session, stepIn 120 s, the git and API calls beside them 60 s.
 */
const BOOT_MS = 60_000
const REPAIR_MS = 240_000
const CHECKOUT_MS = 120_000
const STRAY_MS = 15_000
const STEP_MS = 120_000
const SETUP_MS = 60_000

const m = made()
let repo = ''
let gate = ''
let lookRun = ''
let fixCrew = ''
let origin = ''
let project = ''

interface Capture {
  id: string
  retakeOf?: string
  retakeVia?: string
  origin: { kind: string; run?: string; task?: string }
  annotations: Array<{ element?: { hit: { self: boolean } } }>
}

/** A crew of one task, `id`, done by the capture-fixing stub, serving the repository's page on the tree's port. */
function crewBody(name: string, id: string, check: string[]) {
  return {
    name,
    goal: 'the settings page',
    cwd: repo,
    where: 'server',
    isolation: 'worktree',
    openAfterLaunch: false,
    yolo: null,
    members: [{ name: 'fixer', agentId: 'worker-capture', prompt: '', start: { when: 'task' } }],
    tasks: [{ id, member: 'fixer', kind: 'work', instructions: 'Repair what the capture shows.', checks: [{ id: 'fixed', argv: check, timeoutSeconds: 900 }] }],
    manifest: {
      ports: ['web'],
      services: [{ id: 'web', argv: ['python3', REPAIR_SERVICE, '${CONDUCTOR_PORT_WEB}'] }],
      ready: [{ id: 'up', url: 'http://127.0.0.1:${CONDUCTOR_PORT_WEB}/ready', intervalSeconds: 1, timeoutSeconds: 60 }],
      preview: 'http://127.0.0.1:${CONDUCTOR_PORT_WEB}/',
    },
  }
}

test.beforeAll(async ({ api, state }) => {
  const r = await api.ok<{ sessions?: Array<{ status: string }> } | Array<{ status: string }>>('GET', '/api/sessions')
  const strays = (Array.isArray(r) ? r : (r.sessions ?? [])).filter((s) => ['running', 'starting', 'pending'].includes(s.status)).length
  test.setTimeout(SETUP_MS + strays * STRAY_MS + STEP_MS)
  await stopStrays(api, [])
  repo = join(state.root, `capture-retake-${Date.now().toString(36)}`)
  mkdirSync(join(repo, 'app'), { recursive: true })
  m.dirs.push(repo)
  scratchRepo(repo)
  copyFileSync(REPAIR_PAGE, join(repo, 'app', 'page.html'))
  writeFileSync(join(repo, '.gitignore'), 'node_modules/\n')
  git(repo, 'add', 'app/page.html', '.gitignore')
  git(repo, 'commit', '-q', '-m', 'the settings page, its Save covered')
  gate = join(scratchDir(m, 'capture-retake'), 'gate')
  project = `RT${Date.now().toString(36).slice(-4).toUpperCase()}`.replace(/[^A-Z0-9]/g, 'X')
  await api.ok('POST', '/api/dispatch/projects', { opId: `rt-${project}`, key: project, name: 'Capture retake' })

  // The crew that shows the fault: task `look`, its check holding the attempt (and its preview) open until the gate is written.
  const look = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', crewBody(`e2e retake look ${project}`, 'look', ['sh', '-c', 'until [ -e "$1" ]; do sleep 0.2; done; echo looked', 'sh', gate]))).crew.id
  m.crews.push(look)
  // The crew that repairs it: another name, task `fix`, its check passing once the overlay is gone.
  fixCrew = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', crewBody(`e2e retake fix ${project}`, 'fix', ['sh', '-c', '! grep -q stub-capture-broken app/page.html']))).crew.id
  m.crews.push(fixCrew)
  const run = await api.launchCrew(look)
  m.runs.push(run.id)
  lookRun = run.id
  await stepIn(api, lookRun, 1, 'check:fixed:1', 'running', 'look')
  origin = new URL(task(await api.run(lookRun), 'look').attempt!.preview!).origin
})

test.afterAll(async ({ api }) => {
  if (gate) writeFileSync(gate, '')
  await endMade(api, m)
})

/** Chooses the option whose label begins with `label` in the select `field` opens, and waits for its menu to be gone. */
async function choose(page: Page, field: Locator, label: string) {
  await field.click()
  await page.getByRole('option', { name: new RegExp(`^${label}`) }).click()
  await expect(page.getByRole('listbox')).toHaveCount(0)
}

const ticketPage = (page: Page, key: string) => {
  const [p, n] = key.split('-')
  return page.goto(`/dispatch/${p}/${n}`)
}

test("a ticket's capture is captured again on the repair of another crew whose task has another name, and the two are shown side by side", async ({ page, api }) => {
  test.setTimeout(BOOT_MS * 4 + REPAIR_MS + CHECKOUT_MS + 240_000)

  // The broken page in the look crew's pane: the covered Save picked, noted and saved; Create a ticket from it.
  await previewSeam(page)
  await page.goto(`/preview?run=${encodeURIComponent(lookRun)}&task=look&attempt=1`)
  await expect(page.locator('[data-preview-pane]')).toHaveAttribute('data-preview-bridge', 'ready', { timeout: BOOT_MS })
  const frame = await previewFrame(page, origin)
  const editor = page.locator('[data-capture-editor]')
  await page.locator('[data-preview-capture]').click()
  await pick(page, frame, '#save', 'button#save.save', { downs: 1 })
  await editor.locator('[data-annotation="1"] textarea').fill('Save cannot be clicked')
  await editor.locator('[data-capture-save]').click()
  await expect(editor.locator('[data-capture-saved]')).toBeVisible()
  const first = (await editor.locator('[data-capture-saved]').getAttribute('data-capture-saved'))!
  const send = page.locator('[data-capture-send]')
  await choose(page, send.locator('[data-send-project]'), `${project} ·`)
  await send.locator('[data-capture-send-go]').click()
  const sent = send.locator('[data-send-row]').first()
  await expect(sent).toHaveAttribute('data-send-state', 'sent')
  const key = (await sent.getAttribute('data-send-target'))!
  writeFileSync(gate, '')

  // The ticket's plan names the OTHER crew, its capture attachments ticked at first; approved and launched: the repair run.
  await ticketPage(page, key)
  const plan = page.locator('[data-plan-section]')
  await plan.locator('[data-plan-propose]').click()
  const propose = page.locator('[data-plan-propose-dialog]')
  await expect(propose).toBeVisible()
  await propose.locator('[data-plan-propose-crew]').click()
  await page.getByRole('option').filter({ hasText: `e2e retake fix ${project}` }).click()
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await expect(propose.getByRole('checkbox', { name: `capture-${first}-r1.json`, exact: true })).toBeChecked()
  await propose.locator('[data-plan-propose-submit]').click()
  await expect(propose).toBeHidden()
  const featured = plan.locator('[data-plan-featured]')
  await expect(featured).toHaveAttribute('data-plan-state', 'proposed')
  await featured.locator('[data-plan-approve]').click()
  const approve = page.locator('[data-plan-approve-dialog]')
  await approve.locator('[data-plan-approve-submit]').click()
  await expect(approve).toBeHidden()
  await expect(featured).toHaveAttribute('data-plan-state', 'approved')
  const { plans } = await api.ok<{ plans: Array<{ id: string; state: string }> }>('GET', `/api/dispatch/tickets/${key}/plans`)
  const approved = plans.find((p) => p.state === 'approved')!
  const repairRun = (await api.ok<{ run: { id: string } }>('POST', `/api/dispatch/tickets/${key}/launch`, { opId: `rt-launch-${project}`, plan: approved.id })).run.id
  m.runs.push(repairRun)
  await expect.poll(async () => task(await api.run(repairRun), 'fix').state, { timeout: REPAIR_MS, intervals: [500], message: 'the repair succeeded' }).toBe('succeeded')
  const commit = task(await api.run(repairRun), 'fix').result!.commit!

  // The ticket's capture offers Capture again on the repair's candidate, though its task is fix and the capture's look; its checkout
  // is opened by the press itself.
  await ticketPage(page, key)
  const again = page.locator(`[data-ticket-captures] [data-ticket-capture-again="${first}"]`)
  await expect(again).toHaveText('Capture again on fix#1', { timeout: BOOT_MS })
  await again.click()
  await expect(page).toHaveURL(new RegExp(`/preview\\?run=${repairRun}&task=fix&attempt=1&seq=\\d+&retake=${first}$`))
  await expect(page.locator('[data-preview-label]')).toHaveText(`candidate ${commit.slice(0, 7)} of fix · attempt 1 · as committed`, { timeout: CHECKOUT_MS })
  await expect(page.locator('[data-preview-pane]')).toHaveAttribute('data-preview-bridge', 'ready', { timeout: BOOT_MS })
  await page.locator('[data-preview-retake]').click()
  await expect(editor.locator('[data-annotation="1"] [data-capture-element-summary]')).toContainText('button#save')
  await editor.locator('[data-capture-save]').click()
  await expect(editor.locator(`[data-capture-saved]:not([data-capture-saved="${first}"])`)).toBeVisible()
  const second = (await editor.locator('[data-capture-saved]').getAttribute('data-capture-saved'))!
  const c2 = await api.ok<Capture>('GET', `/api/captures/${second}`)
  expect([c2.retakeOf, c2.retakeVia, c2.origin.kind, c2.origin.run, c2.origin.task]).toEqual([first, 'ticket', 'candidate', repairRun, 'fix'])

  // Attach to a ticket: the run's ticket, offered first.
  await send.locator('[data-send-kind="ticket"]').click()
  await expect(send.locator('[data-send-run-ticket]')).toHaveText(`${key} · the run's ticket`)
  await send.locator('[data-capture-send-go]').click()
  await expect(send.locator('[data-send-row]').first()).toContainText(`sent as r1 to ${key}`)

  // On the ticket, side by side: the first on the left, the retake on its right at one height, each saying which it is.
  await ticketPage(page, key)
  const pair = page.locator(`[data-ticket-captures] [data-ticket-capture-pair="${first}"]`)
  await expect(pair.locator('[data-ticket-capture]')).toHaveCount(2, { timeout: BOOT_MS })
  const before = pair.locator(`[data-ticket-capture="${first}"]`)
  const after = pair.locator(`[data-ticket-capture="${second}"]`)
  await expect(before.locator('[data-ticket-capture-role="first"]')).toBeVisible()
  await expect(after.locator('[data-ticket-capture-role="retake"]')).toContainText(first.slice(0, 8))
  // The retake's run is the newest of the ticket's: its card offers no Capture again pointing back.
  await expect(after.locator('[data-ticket-capture-again]')).toHaveCount(0)
  const sideBySide = async () => {
    const [a, b] = [(await before.boundingBox())!, (await after.boundingBox())!]
    return { sameTop: Math.abs(a.y - b.y) <= 1, rightOf: b.x >= a.x + a.width }
  }
  await expect.poll(sideBySide, { message: 'the two captures side by side' }).toEqual({ sameTop: true, rightOf: true })
  // Dark as light: the same layout, the words drawn.
  await page.emulateMedia({ colorScheme: 'dark' })
  await expect.poll(sideBySide, { message: 'side by side in the dark theme' }).toEqual({ sameTop: true, rightOf: true })
  await expect(after.locator('[data-ticket-capture-role="retake"]')).toBeVisible()
  await page.emulateMedia({ colorScheme: 'light' })

  // A phone: one above the other, before first, nothing scrolling sideways.
  await page.setViewportSize({ width: 390, height: 844 })
  await pair.scrollIntoViewIfNeeded()
  await expect
    .poll(
      async () => {
        const [a, b] = [(await before.boundingBox())!, (await after.boundingBox())!]
        return b.y >= a.y + a.height
      },
      { message: 'the retake below the first on a phone' },
    )
    .toBe(true)
  await noSideways(page, { root: page.locator('[data-ticket-captures]') })
})
