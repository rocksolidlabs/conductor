import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, stopStrays, task, test } from './fixtures'
import { decision, decisionServer, operatorPage, type DecisionServer } from './decisions-helpers'
import { PREVIEW_SERVICE, pick, previewFrame, previewSeam, scratchDir, made, endMade } from './preview-fixtures'

// A capture attached to a decision (docs/plans/browser-evidence.md, PR-11, #799), on a server of its own: a managed crew whose stub
// worker serves the preview fixture and asks the operator one approval before it commits; a capture of its attempt saved, then
// Attach to a decision offers the run's open decision, and the send is a comment on it naming the capture's ref and none of the
// page's text; the decision's card lists the capture; answered afterwards, the card still lists it. Every wait is on what the page
// draws or what the server answers.
test.describe.configure({ mode: 'serial', timeout: 300_000 })

const m = made()
let srv: DecisionServer
let runId = ''
let origin = ''
let gate = ''

test.beforeAll(async () => {
  srv = await decisionServer('capture-decision', [{ id: 'worker-decides', from: 'worker', env: { STUB_DECISION: 'approval' } }])
  await stopStrays(srv.api, [])
  gate = join(scratchDir(m, 'capture-decision'), 'gate')
  const crew = await srv.api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
    name: 'e2e capture decision',
    goal: 'ask the operator, previewed',
    cwd: '',
    where: 'server',
    isolation: 'worktree',
    openAfterLaunch: false,
    yolo: null,
    members: [{ name: 'builder', agentId: 'worker-decides', prompt: '', start: { when: 'task' } }],
    tasks: [
      {
        id: 'build',
        member: 'builder',
        kind: 'work',
        instructions: 'Build it, once the operator approves.',
        checks: [{ id: 'gate', argv: ['sh', '-c', 'until [ -e "$1" ]; do sleep 0.2; done; echo gate open', 'sh', gate], timeoutSeconds: 900 }],
      },
    ],
    manifest: {
      ports: ['web'],
      services: [{ id: 'web', argv: ['python3', PREVIEW_SERVICE, '${CONDUCTOR_PORT_WEB}'] }],
      ready: [{ id: 'up', url: 'http://127.0.0.1:${CONDUCTOR_PORT_WEB}/ready', intervalSeconds: 1, timeoutSeconds: 60 }],
      preview: 'http://127.0.0.1:${CONDUCTOR_PORT_WEB}/',
    },
  })
  runId = (await srv.api.launchCrew(crew.crew.id)).id
  // The attempt has asked its decision: open, its preview served.
  await expect
    .poll(async () => (await decision(srv.api, 'dr-1').catch(() => null))?.state ?? 'absent', { timeout: 120_000, intervals: [250], message: 'dr-1 asked' })
    .toBe('open')
  await expect
    .poll(async () => task(await srv.api.run(runId), 'build').attempt?.preview ?? '', { timeout: 120_000, intervals: [250], message: 'the preview served' })
    .not.toBe('')
  origin = new URL(task(await srv.api.run(runId), 'build').attempt!.preview!).origin
})

test.afterAll(async () => {
  if (gate) writeFileSync(gate, '')
  if (!srv) return
  if (runId) await srv.api.stopRun(runId)
  await endMade(srv.api, m)
  await srv.stop()
})

test('Attach to a decision: a comment naming the capture, listed on the decision’s card, kept once it is answered', async ({ browser }) => {
  const page = await operatorPage(browser, srv.started)
  await previewSeam(page)
  await page.goto(`${srv.started.baseURL}/preview?run=${encodeURIComponent(runId)}&task=build&attempt=1`)
  await expect(page.locator('[data-preview-pane]')).toHaveAttribute('data-preview-bridge', 'ready')
  const frame = await previewFrame(page, origin)
  const editor = page.locator('[data-capture-editor]')
  await page.locator('[data-preview-capture]').click()
  await pick(page, frame, '#under', 'button#under.under', { downs: 1 })
  await editor.locator('[data-annotation="1"] textarea').fill('PAGE-NOTE the save button is covered')
  await editor.locator('[data-capture-save]').click()
  const saved = editor.locator('[data-capture-saved]')
  await expect(saved).toBeVisible()
  const id = (await saved.getAttribute('data-capture-saved'))!

  const send = page.locator('[data-capture-send]')
  await send.locator('[data-send-kind="decision"]').click()
  await expect(send.locator('[data-send-decision]')).toContainText('dr-1')
  await send.locator('[data-capture-send-go]').click()
  const row = send.locator('[data-send-row]').first()
  await expect(row).toHaveAttribute('data-send-state', 'sent')
  await expect(row).toContainText('sent as r1 to decision dr-1')
  const sendId = (await row.getAttribute('data-send-row'))!

  // The comment names the capture and its ref, and none of the page's text.
  const rec = await srv.api.ok<{ decision: { history: Array<{ kind: string; detail?: { text?: string } }> } }>('GET', '/api/decisions/dr-1')
  const comments = rec.decision.history.filter((h) => h.kind === 'commented').map((h) => h.detail?.text ?? '')
  expect(comments).toHaveLength(1)
  expect(comments[0]).toContain(`capture:${id}/${sendId}`)
  expect(comments[0]).not.toContain('PAGE-NOTE')

  // The decision's card lists it; answered, it still does.
  await page.goto(`${srv.started.baseURL}/decisions?item=decision%3Adr-1`)
  const card = page.locator('[data-decision-card][data-decision-id="dr-1"]')
  await expect(card.locator(`[data-decision-capture="${sendId}"]`)).toContainText(`capture:${id}/${sendId}`)
  const open = await decision(srv.api, 'dr-1')
  await srv.api.ok('POST', '/api/decisions/dr-1/respond', { responseId: 'cd-yes', choice: 0, expectedRev: open.rev })
  await expect(card).not.toHaveAttribute('data-decision-state', 'open')
  await expect(card.locator(`[data-decision-capture="${sendId}"]`)).toBeVisible()
  await page.context().close()
})
