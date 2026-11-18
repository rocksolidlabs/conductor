import { budget, dayPage, expect, test } from './fixtures'
import { fakeGitHub, type FakeGitHub } from './fake-github'
import { freeProjectKey, makeProject, makeTicket, opOf } from './dispatch-helpers'
import { ownServer, type OwnServer } from './own-server'
import { START_WAIT_MS } from './server'

// GitHub's links read (Program item IN1a, docs/plans/tracker-linking.md, PR-8), against the e2e's fake GitHub (fake-github.ts) on a
// server of the spec's own in loopback mode: a delivery link to a pull request read as open, then merged; the project's policy `merged`;
// Close refused, then met. And first the fake itself, as the server's adapter asks it: every route answered, the token sent to the API
// host alone, never with an image.

const TOKEN = 'ghp_e2eFakeToken0123456789abcdefLINKS'
const PNG = Buffer.from('89504e470d0a1a0a0000000d4948445200000001000000010806000000', 'hex')

/** An expect's bound and an action's (the config's expect timeout). */
const STEP_MS = 15_000

let srv: OwnServer
let gh: FakeGitHub

test.beforeAll(async () => {
  test.setTimeout(START_WAIT_MS + 30_000)
  gh = await fakeGitHub()
  srv = await ownServer('tracker-links', { env: { CONDUCTOR_TRACKER_LOOPBACK: '1' } })
  await srv.api.ok('POST', '/api/trackers/github', { host: gh.host, token: TOKEN })
})

test.afterAll(async () => {
  await gh?.stop()
  await srv?.stop()
})

test("the fake answers what the server's adapter asks, and the token goes to its API alone", async () => {
  const api = srv.api
  const listed = await api.ok<{ github: Array<{ host: string; login: string; state: string }> }>('GET', '/api/trackers')
  expect(listed.github.find((e) => e.host === gh.host)).toMatchObject({ login: 'mona-example', state: 'ok' })
  gh.set({ n: 7, kind: 'issue', title: 'The list crashes when empty', body: 'It crashes.\n\n### Steps to reproduce\n\n1. Open the list\n2. Empty it\n', state: 'open', labels: ['bug'], images: [{ path: 'a.png', type: 'image/png', bytes: PNG }, { path: 'b.png', type: 'image/png', bytes: PNG }] })
  const { preview } = await api.ok<{ preview: { title: string; images: string[]; repro?: string; state: string; labels: string[] } }>('POST', '/api/dispatch/external/preview', { url: gh.url(7) })
  expect(preview).toMatchObject({ title: 'The list crashes when empty', state: 'open', labels: ['bug'] })
  expect(preview.images).toEqual([`http://${gh.host}/_img/7/a.png`, `http://${gh.host}/_img/7/b.png`])
  expect(preview.repro).toContain('Open the list')
  const key = 'FKG'
  await makeProject(api, key)
  const imported = await api.ok<{ ticket: { key: string; attachments: Array<{ contentType: string }> }; images: Array<{ attachmentId?: string }> }>('POST', `/api/dispatch/projects/${key}/import`, { opId: opOf(key, 'import'), url: gh.url(7) })
  expect(imported.images.filter((i) => i.attachmentId)).toHaveLength(2)
  expect(gh.count('image')).toBe(2)
  // A pull request's read, its checks at its head commit.
  gh.set({ n: 8, kind: 'pull', title: 'Fix the empty list', body: 'Fixes #7', state: 'open', headSha: 'a'.repeat(40) })
  gh.checks('a'.repeat(40), [
    { kind: 'job', id: 2000001, name: 'test', status: 'completed', conclusion: 'success', log: 'ok\n' },
    { kind: 'run', id: 2100001, name: 'lint', status: 'completed', conclusion: 'failure' },
    { kind: 'status', id: 3000001, name: 'ci/build', status: 'completed', conclusion: 'success' },
  ])
  const ticket = await makeTicket(api, key, '2', { title: 'A delivery' })
  const added = await api.ok<{ ticket: { links: Array<{ id: string; url: string }> } }>('POST', `/api/dispatch/tickets/${ticket}/links`, { opId: opOf(key, 'l1'), kind: 'delivery', url: gh.url(8, 'pull') })
  const link = added.ticket.links.find((l) => l.url === gh.url(8, 'pull'))!
  const { read } = await api.ok<{ read: { state: string; merged: boolean; checks: Array<{ id: string; conclusion: string }> } }>('POST', `/api/dispatch/tickets/${ticket}/links/${link.id}/read`, {})
  expect(read.state).toBe('open')
  expect(read.checks.map((c) => c.id).sort()).toEqual(['job:2000001', 'run:2100001', 'status:3000001'])
  // The token went in the API's requests' header alone: never with an image, never anywhere but the fake's API routes.
  const reqs = gh.requests()
  expect(reqs.filter((r) => r.route === 'image').every((r) => r.token === '')).toBe(true)
  expect(reqs.filter((r) => r.route !== 'image').every((r) => r.token === TOKEN)).toBe(true)
})

test('a delivery link to a pull request is read open, then merged; a policy requiring merged refuses Close, then is met', async ({ browser }) => {
  budget(30 * STEP_MS)
  const api = srv.api
  const key = await freeProjectKey(api, 'GHL')
  await makeProject(api, key, [])
  const head = 'b'.repeat(40)
  gh.set({ n: 21, kind: 'pull', title: 'Fix the empty list', body: 'Fixes it.', state: 'open', headSha: head })
  gh.checks(head, [
    { kind: 'job', id: 2000011, name: 'test', status: 'completed', conclusion: 'success', log: 'ok\n' },
    { kind: 'run', id: 2100011, name: 'lint', status: 'completed', conclusion: 'failure' },
  ])
  const ticket = await makeTicket(api, key, '1', { title: 'The empty list' })
  const page = await dayPage(browser, { path: `/dispatch/${key}`, server: srv.started })

  // The project's policy: merged, offered and explained, ticked and saved on its settings.
  await page.locator('[data-project-settings-open]').click()
  // The checkbox carries the requirement's name; its label and its words are beside it.
  const policy = page.locator('[data-project-close-policy]')
  await expect(policy).toContainText('Merged on GitHub')
  await expect(policy).toContainText('A delivery pull request GitHub says is merged')
  await policy.locator('[data-close-requirement="merged"]').click()
  await page.locator('[data-close-policy-save]').click()
  await expect(page.locator('[data-project-close-policy]')).toHaveAttribute('data-policy', 'merged')
  await page.keyboard.press('Escape')

  // The delivery link added on the ticket's page: read in the background once added, then shown as GitHub says it.
  await page.goto(`/dispatch/${key}/${ticket.slice(key.length + 1)}`)
  await expect(page.locator('[data-ticket-strip]')).toBeVisible()
  // No GitHub section for a ticket with no linked item: a link's read is the Links' own.
  await expect(page.locator('[data-strip-item="github"]')).toHaveCount(0)
  await page.locator('[data-link-add-open]').first().click()
  const form = page.locator('[data-link-form]').first()
  await form.locator('input[data-link-url-input]').fill(gh.url(21, 'pull'))
  await form.locator('[data-link-submit]').click()
  const row = page.locator(`[data-link="${gh.url(21, 'pull')}"]`).first()
  await expect(row).toBeVisible()
  const read = row.locator('[data-link-read]')
  // The read after the link's add is the server's; Read again asks for one at once, until the state it leads to is shown.
  await expect(async () => {
    if ((await read.getAttribute('data-link-state')) !== 'open') await read.locator('[data-link-read-again]').click({ timeout: 2_000 })
    await expect(read).toHaveAttribute('data-link-state', 'open', { timeout: 2_000 })
  }).toPass({ timeout: STEP_MS })
  await expect(read.locator('[data-link-check-counts]')).toContainText('1 passed · 1 failed')
  await expect(read.locator('[data-link-checks] [data-link-check]')).toHaveCount(2)
  await expect(read.locator('[data-link-check="run:2100011"]')).toContainText('lint: failed')
  // The fake's check URLs are http on a loopback host: drawn as text, never as links.
  await expect(read.locator('[data-link-checks] a')).toHaveCount(0)
  await expect(read.locator('[data-link-incomplete]')).toHaveCount(0)

  // Close, refused by the policy: the pull request is not merged.
  const close = page.locator('[data-close-policy]').first()
  await expect(close.locator('[data-close-item="merged"]')).not.toHaveAttribute('data-met', '')
  await close.locator('[data-close]').click()
  await expect(close.locator('[data-close-refused]')).toContainText('a delivery GitHub says is merged')
  expect((await api.ok<{ ticket: { status: string } }>('GET', `/api/dispatch/tickets/${ticket}`)).ticket.status).not.toBe('done')

  // Merged on GitHub: Read again shows it, with its merge commit; Close is read again by the server and met.
  gh.set({ ...gh.item(21)!, state: 'closed', merged: true, mergeSha: 'c'.repeat(40), updatedAt: undefined })
  await read.locator('[data-link-read-again]').click()
  await expect(read).toHaveAttribute('data-link-state', 'merged')
  await expect(read.locator('[data-link-merge-sha]')).toHaveText('ccccccc')
  // A read whose checks GitHub does not answer keeps the last read's, said with the time they were read, never as this read's (#989).
  gh.checksDown(true)
  const asked = gh.count('checks')
  await read.locator('[data-link-read-again]').click()
  await expect(read.locator('[data-link-checks-kept]')).toContainText(/^checks as read .+ ago: not read again with the last read$/)
  expect(gh.count('checks')).toBeGreaterThan(asked)
  await expect(read.locator('[data-link-check-counts]')).toContainText('1 passed · 1 failed')
  // GitHub ran the lint again, and it passed: the close reads the checks with the link, and the row shows what GitHub says now.
  gh.checksDown(false)
  gh.checks(head, [
    { kind: 'job', id: 2000011, name: 'test', status: 'completed', conclusion: 'success', log: 'ok\n' },
    { kind: 'run', id: 2100011, name: 'lint', status: 'completed', conclusion: 'success' },
  ])
  await close.locator('[data-close]').click()
  await expect(close).toHaveAttribute('data-close-closed', '')
  expect((await api.ok<{ ticket: { status: string } }>('GET', `/api/dispatch/tickets/${ticket}`)).ticket.status).toBe('done')
  await expect(read.locator('[data-link-check-counts]')).toContainText('all 2 passed')
  await expect(read.locator('[data-link-incomplete]')).toHaveCount(0)
})
