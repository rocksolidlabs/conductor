import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { dayPage, expect, test, type RunAttempt } from './fixtures'
import { fakeGitHub, type FakeGitHub } from './fake-github'
import { makeProject, opOf } from './dispatch-helpers'
import { ownServer, type OwnServer } from './own-server'

// A plan revision's choices of a linked issue (Program item IN1a, docs/plans/tracker-linking.md, PR-9), against the e2e's fake GitHub on a
// server of the spec's own in loopback mode: an issue with a Steps to reproduce section imported as a ticket, a link to a pull request
// beside it. The proposal offers the steps, on by default, and the link as evidence; proposed, the revision's body says what it hands
// over; approved and launched with the stub agent, the attempt's ticket.md has the Source, the steps quoted and the evidence link, and
// the run page's launched-from chip names the issue on GitHub.
test.describe.configure({ mode: 'serial' })

const TOKEN = 'ghp_e2eFakeToken0123456789abcdefPACKAGE'
const KEY = 'PKG'
const ISSUE = 21
const PULL = 22
const STEPS = '1. Open the list\n2. Empty it'
/** A launch's own bound (the route answers once the run's first transaction is committed): two minutes. */
const LAUNCH_MS = 120_000

let srv: OwnServer
let gh: FakeGitHub
let ticket = ''
let crewName = ''
let runId = ''

test.beforeAll(async () => {
  gh = await fakeGitHub()
  srv = await ownServer('tracker-package', { env: { CONDUCTOR_TRACKER_LOOPBACK: '1' } })
  const api = srv.api
  await api.ok('POST', '/api/trackers/github', { host: gh.host, token: TOKEN })
  gh.set({ n: ISSUE, kind: 'issue', title: 'The list crashes when empty', body: `It crashes.\n\n### Steps to reproduce\n\n${STEPS}\n`, state: 'open' })
  gh.set({ n: PULL, kind: 'pull', title: 'Fix the empty list', body: 'Fixes #21', state: 'open', headSha: 'c'.repeat(40) })
  await makeProject(api, KEY)
  ticket = (await api.ok<{ ticket: { key: string } }>('POST', `/api/dispatch/projects/${KEY}/import`, { opId: opOf(KEY, 'import'), url: gh.url(ISSUE) })).ticket.key
  await api.ok('POST', `/api/dispatch/tickets/${ticket}/links`, { opId: opOf(KEY, 'pr'), kind: 'external', url: gh.url(PULL, 'pull'), note: 'the fix' })
  crewName = `e2e tracker package ${Date.now().toString(36)}`
  await api.ok('POST', '/api/crews', {
    name: crewName,
    goal: 'build it from the issue',
    cwd: '',
    where: 'server',
    isolation: 'worktree',
    openAfterLaunch: false,
    yolo: null,
    members: [{ name: 'builder', agentId: 'worker', prompt: '', start: { when: 'task' } }],
    tasks: [{ id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.' }],
  })
})

test.afterAll(async () => {
  // The run first, then the fake and the server: what a test started ends before its directories go.
  if (runId) await srv?.api.stopRun(runId)
  await gh?.stop()
  await srv?.stop()
})

test("a revision hands over the issue's steps to reproduce and a link as evidence, and the attempt's ticket.md has them", async ({ browser }) => {
  test.setTimeout(test.info().timeout + 2 * LAUNCH_MS)
  const api = srv.api
  const page = await dayPage(browser, { server: srv.started, path: `/dispatch/${KEY}/${ticket.split('-')[1]}` })
  const plan = page.locator('[data-plan-section]')
  await expect(plan.locator('[data-plan-empty]')).toBeVisible({ timeout: 30_000 })
  await plan.locator('[data-plan-propose]').click()
  const propose = page.locator('[data-plan-propose-dialog]')
  await expect(propose).toBeVisible()
  await propose.locator('[data-plan-propose-crew]').click()
  await page.getByRole('option').filter({ hasText: crewName }).click()
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await expect(propose.locator('[data-plan-propose-crew]')).toContainText(crewName)

  // The steps, on by default and drawn as the issue has them; the link offered as evidence, off.
  const repro = propose.getByRole('checkbox', { name: `Steps to reproduce, from ${gh.owner}/${gh.repo}#${ISSUE}` })
  await expect(repro).toBeChecked()
  await expect(propose.locator('[data-propose-repro-text]')).toHaveText(STEPS)
  const evidence = propose.getByRole('checkbox', { name: /^External: / })
  await expect(evidence).not.toBeChecked()
  await evidence.click()
  await expect(evidence).toBeChecked()
  await propose.locator('[data-plan-propose-submit]').click()
  await expect(propose).toBeHidden({ timeout: 30_000 })

  // The revision's body pins the steps and names the link, and says so.
  const { plans } = await api.ok<{ plans: Array<{ id: string; state: string }> }>('GET', `/api/dispatch/tickets/${ticket}/plans`)
  const read = await api.ok<{ plan: { body: { source?: { repro?: { identity: string; sha256: string }; evidence?: string[] } } } }>('GET', `/api/dispatch/plans/${plans[0]!.id}`)
  const t = (await api.ok<{ ticket: { externalId: string; externalSnapshot: { reproSha256: string }; links: Array<{ id: string; url: string }> } }>('GET', `/api/dispatch/tickets/${ticket}`)).ticket
  const link = t.links.find((l) => l.url === gh.url(PULL, 'pull'))!
  expect(read.plan.body.source).toEqual({ repro: { identity: t.externalId, sha256: t.externalSnapshot.reproSha256 }, evidence: [link.id] })
  const featured = plan.locator('[data-plan-featured]')
  await expect(featured.locator('[data-plan-repro]')).toContainText(`The steps to reproduce of ${gh.owner}/${gh.repo}#${ISSUE}`)
  await expect(featured.locator(`[data-plan-evidence-id="${link.id}"]`)).toContainText(`Evidence:`)

  // Approved and launched with the stub agent: the attempt's ticket.md has the Source, the steps quoted and the evidence link.
  await api.ok('POST', `/api/dispatch/plans/${plans[0]!.id}/approve`, { opId: opOf(KEY, 'ap1'), note: 'Go.' })
  runId = (await api.ok<{ launch: { runId: string } }>('POST', `/api/dispatch/tickets/${ticket}/launch`, { opId: opOf(KEY, 'l1'), plan: plans[0]!.id })).launch.runId
  let md = ''
  await expect
    .poll(
      async () => {
        const route = await api.ok<{ attempts: RunAttempt[] }>('GET', `/api/runs/${encodeURIComponent(runId)}/tasks/build`)
        const tree = route.attempts.find((a) => a.n === 1)?.worktree
        const file = tree ? join(tree, '.conductor', 'ticket.md') : ''
        md = file && existsSync(file) ? readFileSync(file, 'utf8') : ''
        return md.includes('## Evidence links')
      },
      { timeout: LAUNCH_MS, intervals: [500], message: "attempt 1's ticket.md is written" },
    )
    .toBe(true)
  expect(md).toContain('## Source')
  expect(md).toContain(gh.url(ISSUE))
  expect(md).toContain('## Steps to reproduce')
  for (const line of STEPS.split('\n')) expect(md).toContain(`> ${line}`)
  expect(md).toContain(`- <${gh.url(PULL, 'pull')}>: the fix`)

  // The run page's launched-from chip names the issue on GitHub.
  await page.goto(`/runs/${encodeURIComponent(runId)}`)
  await expect(page.locator('[data-run-launched-from] [data-run-source]')).toHaveText(`· GitHub ${gh.owner}/${gh.repo}#${ISSUE}`, { timeout: 30_000 })
  await expect(page.locator('[data-run-launched-from]')).toHaveText(`Launched from ${ticket} · plan r1 · GitHub ${gh.owner}/${gh.repo}#${ISSUE}`)
})
