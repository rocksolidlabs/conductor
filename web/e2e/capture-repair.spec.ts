import { copyFileSync, existsSync, mkdirSync, readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { Locator, Page } from '@playwright/test'
import { expect, git, stopStrays, task, test } from './fixtures'
import { endMade, made, pick, previewFrame, previewSeam, scratchDir, stepIn } from './preview-fixtures'
import { scratchRepo } from './server'

// The repair loop from a capture (docs/plans/browser-evidence.md, PR-9, #797), on stubs: a managed crew of the stub worker under
// STUB_CAPTURE_FIX (worker-capture) in a repository of the spec's own whose app/page.html has a Save covered by a transparent
// overlay, served from the tree by web/e2e/fixtures/repair/serve.py with the bridge the tree names. A first run's attempt serves the
// broken page; a capture of the covered Save, noted "Broken", names the overlay in its hit test and the page's own file first among
// its sources (a library component under node_modules after it); Create a ticket; a plan revision of the ticket with the capture's
// two attachments ticked at first, approved and launched; the stub worker reads the capture's document in its package
// (.conductor/attachments/NN-capture-*.json), removes the
// overlay from the file its first source names and submits, its check passing; Preview this candidate; Capture again of the first
// capture, its retakeOf read back as the first's id (two runs, retakeVia ticket); the ticket's Captures showing the first alone, then
// Attach to a ticket with the run's ticket offered first, and both shown, the retake frozen at its revision. Every wait is on what
// the page draws or what the server answers.
test.describe.configure({ mode: 'serial' })

const REPAIR_SERVICE = fileURLToPath(new URL('./fixtures/repair/serve.py', import.meta.url))
const REPAIR_PAGE = fileURLToPath(new URL('./fixtures/repair/page.html', import.meta.url))

/**
 * The waits this walk takes, each bounded by the product's own or a spec's: the app's start with the bridge (60 s, as
 * preview-capture.spec.ts's BOOT_MS), the launch from the ticket to a submitted candidate (a fresh attempt's session, the stub's
 * turn, its check: 240 s), the candidate's checkout made with its manifest (120 s). The test's timeout is above their sum.
 */
const BOOT_MS = 60_000
const REPAIR_MS = 240_000
const CHECKOUT_MS = 120_000

const m = made()
let repo = ''
let gate = ''
let crewId = ''
let crewName = ''
let firstRun = ''
let origin = ''
let project = ''

interface Source {
  path: string
  line?: number
  found: boolean
  class: string
}

interface Capture {
  id: string
  rev: number
  retakeOf?: string
  retakeVia?: string
  origin: { kind: string; run?: string; tree?: string; commit?: string }
  annotations: Array<{ k: number; note: string; element?: { id?: string; hit: { self: boolean } }; sources?: Source[] }>
}

test.beforeAll(async ({ api, state }) => {
  await stopStrays(api, [])
  // A repository of the spec's own, under the server's allowed root: the broken page, and node_modules left out as a project does.
  repo = join(state.root, `capture-repair-${Date.now().toString(36)}`)
  mkdirSync(join(repo, 'app'), { recursive: true })
  m.dirs.push(repo)
  scratchRepo(repo)
  copyFileSync(REPAIR_PAGE, join(repo, 'app', 'page.html'))
  writeFileSync(join(repo, '.gitignore'), 'node_modules/\n')
  git(repo, 'add', 'app/page.html', '.gitignore')
  git(repo, 'commit', '-q', '-m', 'the settings page, its Save covered')
  gate = join(scratchDir(m, 'capture-repair'), 'gate')
  project = `CR${Date.now().toString(36).slice(-4).toUpperCase()}`.replace(/[^A-Z0-9]/g, 'X')
  await api.ok('POST', '/api/dispatch/projects', { opId: `cr-${project}`, key: project, name: 'Capture repair' })

  // The crew: one task done by the capture-fixing stub. Its check holds an attempt open while the page is still broken and the gate
  // is shut (the first run's, so its preview stays up until the capture is sent), and passes once the overlay is gone (the repair's).
  crewName = `e2e capture repair ${project}`
  const body = {
    name: crewName,
    goal: 'repair the settings page',
    cwd: repo,
    where: 'server',
    isolation: 'worktree',
    openAfterLaunch: false,
    yolo: null,
    members: [{ name: 'fixer', agentId: 'worker-capture', prompt: '', start: { when: 'task' } }],
    tasks: [
      {
        id: 'build',
        member: 'fixer',
        kind: 'work',
        instructions: 'Repair what the capture shows.',
        checks: [
          {
            id: 'fixed',
            argv: ['sh', '-c', 'if grep -q stub-capture-broken app/page.html; then until [ -e "$1" ]; do sleep 0.2; done; fi; echo checked', 'sh', gate],
            timeoutSeconds: 900,
          },
        ],
      },
    ],
    manifest: {
      ports: ['web'],
      services: [{ id: 'web', argv: ['python3', REPAIR_SERVICE, '${CONDUCTOR_PORT_WEB}'] }],
      ready: [{ id: 'up', url: 'http://127.0.0.1:${CONDUCTOR_PORT_WEB}/ready', intervalSeconds: 1, timeoutSeconds: 60 }],
      preview: 'http://127.0.0.1:${CONDUCTOR_PORT_WEB}/',
    },
  }
  crewId = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', body)).crew.id
  m.crews.push(crewId)
  const run = await api.launchCrew(crewId)
  m.runs.push(run.id)
  firstRun = run.id
  // The first attempt submitted and its check waits: the broken page is served behind the preview.
  await stepIn(api, firstRun, 1, 'check:fixed:1', 'running')
  origin = new URL(task(await api.run(firstRun), 'build').attempt!.preview!).origin
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

/** The transcript of the stub's session that read capture's document, or "" before one has. */
function fixTranscript(home: string, capture: string): string {
  const dir = join(home, '.stub-sessions')
  if (!existsSync(dir)) return ''
  const line = `capture-${capture}-r1.json, fixed app/page.html`
  for (const f of readdirSync(dir).filter((x) => x.endsWith('.txt'))) {
    const text = readFileSync(join(dir, f), 'utf8')
    if (text.includes(line)) return text
  }
  return ''
}

const ticketPage = (page: Page, key: string) => {
  const [p, n] = key.split('-')
  return page.goto(`/dispatch/${p}/${n}`)
}

test('a capture of a broken control, a ticket, a plan with its attachments, the stub repairing from it, the candidate captured again and attached', async ({ page, api, state }) => {
  test.setTimeout(BOOT_MS * 3 + REPAIR_MS + CHECKOUT_MS + 240_000)

  // The broken page in the pane: the covered Save, by stepping Down from the overlay, noted "Broken" and saved.
  await previewSeam(page)
  await page.goto(`/preview?run=${encodeURIComponent(firstRun)}&task=build&attempt=1`)
  await expect(page.locator('[data-preview-label]')).toContainText('changes as the agent works', { timeout: BOOT_MS })
  await expect(page.locator('[data-preview-pane]')).toHaveAttribute('data-preview-bridge', 'ready', { timeout: BOOT_MS })
  const frame = await previewFrame(page, origin)
  const editor = page.locator('[data-capture-editor]')
  await page.locator('[data-preview-capture]').click()
  await pick(page, frame, '#save', 'button#save.save', { downs: 1 })
  await expect(editor.locator('[data-annotation="1"] [data-annotation-hit]')).toHaveText('a click at its centre reaches #cover')
  await editor.locator('[data-annotation="1"] textarea').fill('Broken')
  await editor.locator('[data-capture-save]').click()
  await expect(editor.locator('[data-capture-saved]')).toBeVisible()
  const first = (await editor.locator('[data-capture-saved]').getAttribute('data-capture-saved'))!
  const c1 = await api.ok<Capture>('GET', `/api/captures/${first}`)
  expect(c1.origin).toMatchObject({ kind: 'attempt', run: firstRun, tree: 'worktree' })
  // Its sources as the server checked them in the tree: the page's own tracked file first, the library's component after it.
  expect(c1.annotations[0]!.sources?.map((s) => [s.path, s.class])).toEqual([
    ['app/page.html', 'tracked'],
    ['node_modules/widgets/Form.vue', 'vendor'],
  ])
  expect(c1.annotations[0]!.sources?.[0]).toMatchObject({ line: 33, found: true })

  // Create a ticket: the screenshot and the document attached, the note the title.
  const send = page.locator('[data-capture-send]')
  await expect(send.locator('[data-send-kind="ticket-new"]')).toHaveAttribute('data-send-chosen', '')
  await choose(page, send.locator('[data-send-project]'), `${project} ·`)
  await expect(send.locator('[data-send-title]')).toHaveValue('Broken')
  await send.locator('[data-capture-send-go]').click()
  const sent = send.locator('[data-send-row]').first()
  await expect(sent).toHaveAttribute('data-send-state', 'sent')
  const key = (await sent.getAttribute('data-send-target'))!
  expect(key).toMatch(new RegExp(`^${project}-\\d+$`))
  // The first run's preview has served its part: its check is let go, since a result's checks run one at a time on the server and
  // the repair's must have its turn.
  writeFileSync(gate, '')

  // A plan revision of the ticket from the crew: the capture's two attachments ticked at first, handed over as they are.
  await ticketPage(page, key)
  const plan = page.locator('[data-plan-section]')
  await plan.locator('[data-plan-propose]').click()
  const propose = page.locator('[data-plan-propose-dialog]')
  await expect(propose).toBeVisible()
  await propose.locator('[data-plan-propose-crew]').click()
  await page.getByRole('option').filter({ hasText: crewName }).click()
  await expect(page.getByRole('listbox')).toHaveCount(0)
  for (const name of [`capture-${first}-r1.json`, `capture-${first}-r1.png`]) await expect(propose.getByRole('checkbox', { name, exact: true })).toBeChecked()
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
  const launched = await api.ok<{ run: { id: string } }>('POST', `/api/dispatch/tickets/${key}/launch`, { opId: `cr-launch-${project}`, plan: approved.id })
  const repairRun = launched.run.id
  m.runs.push(repairRun)
  expect(repairRun).not.toBe(firstRun)

  // The stub worker reads the capture in its package, fixes the file its first source names, submits, and its check passes.
  await expect.poll(async () => task(await api.run(repairRun), 'build').state, { timeout: REPAIR_MS, intervals: [500], message: 'the repair succeeded' }).toBe('succeeded')
  const commit = task(await api.run(repairRun), 'build').result!.commit!
  const fixed = git(repo, 'show', `${commit}:app/page.html`)
  expect(fixed).not.toContain('stub-capture-broken')
  expect(fixed).toContain('<button id="save"')
  expect(git(repo, 'diff', '--name-only', `${commit}~1`, commit).split('\n').filter(Boolean).sort()).toEqual(['app/page.html', 'stub-fixer.txt'])
  await expect.poll(() => fixTranscript(state.home, first), { message: "the stub's transcript says what it read and fixed" }).not.toBe('')

  // Preview this candidate: the checkout as committed, the page without the overlay.
  await page.goto(`/runs/${encodeURIComponent(repairRun)}?view=tasks`)
  await expect(page.locator('[data-task-row="build"]')).toHaveAttribute('data-task-state', 'succeeded')
  await page.locator('[data-task-menu="build"]').click()
  await page.locator('[data-task-candidate-preview="build"]').click()
  await expect(page).toHaveURL(new RegExp(`/preview\\?run=${repairRun}&task=build&attempt=1&seq=\\d+$`))
  await expect(page.locator('[data-preview-label]')).toHaveText(`candidate ${commit.slice(0, 7)} of build · attempt 1 · as committed`, { timeout: CHECKOUT_MS })

  // Capture again of the first capture there: its element described again, now reached by its own click, kept as a retake.
  await page.goto(`${page.url()}&retake=${first}`)
  await expect(page.locator('[data-preview-pane]')).toHaveAttribute('data-preview-bridge', 'ready', { timeout: BOOT_MS })
  await page.locator('[data-preview-retake]').click()
  await expect(editor.locator('[data-annotation="1"] [data-capture-element-summary]')).toContainText('button#save')
  await expect(editor.locator('[data-annotation="1"] [data-annotation-hit]')).toHaveCount(0)
  await editor.locator('[data-capture-save]').click()
  await expect(editor.locator(`[data-capture-saved]:not([data-capture-saved="${first}"])`)).toBeVisible()
  const second = (await editor.locator('[data-capture-saved]').getAttribute('data-capture-saved'))!
  const c2 = await api.ok<Capture>('GET', `/api/captures/${second}`)
  expect([c2.retakeOf, c2.retakeVia, c2.origin.kind, c2.origin.run]).toEqual([first, 'ticket', 'candidate', repairRun])
  expect(c2.annotations[0]!.element).toMatchObject({ id: 'save', hit: { self: true } })

  // The ticket's Captures, in a second tab (the retake's editor kept in this one): the first alone, since a capture reaches a ticket
  // by a send and the retake has none yet.
  const ticketTab = await page.context().newPage()
  await ticketPage(ticketTab, key)
  const captures = ticketTab.locator('[data-ticket-captures]')
  await expect(captures.locator(`[data-ticket-capture="${first}"]`)).toBeVisible({ timeout: BOOT_MS })
  await expect(captures.locator('[data-ticket-capture]')).toHaveCount(1)

  // Attach to a ticket from the retake: the run's own ticket offered first, and chosen.
  await send.locator('[data-send-kind="ticket"]').click()
  await expect(send.locator('[data-send-run-ticket]')).toHaveText(`${key} · the run's ticket`)
  await expect(send.locator('[data-send-ticket]')).toContainText(`To ${key}`)
  await send.locator('[data-capture-send-go]').click()
  await expect(send.locator('[data-send-row]').first()).toContainText(`sent as r1 to ${key}`)

  // Both on the ticket, the retake as its send froze it.
  await ticketPage(ticketTab, key)
  await expect(captures.locator('[data-ticket-capture]')).toHaveCount(2)
  await expect(captures.locator(`[data-ticket-capture="${second}"] [data-send-rev="1"]`)).toBeVisible()
  const both = await api.ok<{ captures: Array<{ id: string }> }>('GET', `/api/captures?ticket=${encodeURIComponent(key)}`)
  expect(both.captures.map((c) => c.id).sort()).toEqual([first, second].sort())
  await ticketTab.close()
})
