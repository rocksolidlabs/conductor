import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import type { Browser, BrowserContext, Page } from '@playwright/test'
import { expect, git, test } from './fixtures'
import { scratchRepo } from './server'
import { ownServer, type OwnServer } from './own-server'
import { fakeGitHub, type FakeCheck, type FakeGitHub } from './fake-github'
import { freeProjectKey, makeProject, makeTicket, opOf } from './dispatch-helpers'
import { noSideways } from './layout'

// A delivery pull request's CI job as a failed-check request (IN1a PR-10, docs/plans/tracker-linking.md): on a server of the spec's own
// in loopback mode, with the fake GitHub the spec starts and a made-up token saved for it, a ticket's run whose build attempt 1
// succeeds (the candidate) and a delivery link to the fake's pull request 7 naming that candidate, the pull request's head at the
// candidate's commit with a failed GitHub Actions job (its log), a passing one, and more check runs than a read keeps (the read is cut).
// The Review view of the candidate lists the delivery's jobs, failed first, says the read was incomplete in its own words and never
// that every job passed; Prepare a repair on the failed job shows it as GitHub's (its name, how it ended, the commit it tested, the end
// of its log, Check again not offered); added to the draft and sent as a comment, the review keeps the finding's receipt external, read
// again from GitHub by the server. When the pull request's head moves on, its jobs tested another commit and none is offered. The
// panel holds a phone's width with nothing sideways, in both themes.
//
// The spec counts only what it made (its project by a free key, its run by its id), waits on what the server or the page must reach,
// and raises its timeout by its own waits' caps (AGENTS.md, Tests that do not flake). No real token and no real host: the token is made
// up and every request goes to the fake.
test.describe.configure({ mode: 'serial' })

/** A made-up token the fake takes (it answers 401 only for one it is told to refuse). */
const TOKEN = 'ghp_e2eREVIEWexternalTOKEN0123456789WXYZ'
/** The failed job's id and the log's last lines, which the panel shows. */
const FAILED = 2000001
const LOG_END = '--- FAIL: TestEmptyList (0.00s)\nFAIL\tpackage/list\t0.012s\n'

let own: OwnServer
let gh: FakeGitHub
const opened: BrowserContext[] = []
/** The runs this spec launched: stopped before its server stops (AGENTS.md: end what a test started before its directories go). */
const runs: string[] = []

test.beforeAll(async () => {
  test.setTimeout(test.info().timeout + 120_000)
  gh = await fakeGitHub()
  own = await ownServer('review-external', { env: { CONDUCTOR_TRACKER_LOOPBACK: '1' } })
  await own.api.ok('POST', '/api/trackers/github', { host: gh.host, token: TOKEN })
})

test.afterAll(async () => {
  await Promise.all(opened.splice(0).map((c) => c.close()))
  for (const id of runs.splice(0)) await own?.api.stopRun(id)
  await gh?.stop()
  await own?.stop()
})

/** Raises the running test's timeout by the cap of a wait as the step is about to make it. */
function within(ms: number) {
  const info = test.info()
  info.setTimeout(info.timeout + ms)
  return { timeout: ms }
}

/** A page on the spec's own server in the theme given, at the width given (a phone's with touch). */
async function pageOn(browser: Browser, o: { theme: 'light' | 'dark'; width?: number }): Promise<Page> {
  const ctx = await browser.newContext({
    baseURL: own.started.baseURL,
    viewport: { width: o.width ?? 1440, height: o.width ? 844 : 900 },
    ...(o.width ? { hasTouch: true, isMobile: true } : {}),
    storageState: {
      cookies: [],
      origins: [{ origin: new URL(own.started.baseURL).origin, localStorage: [{ name: 'conductor-color-mode', value: o.theme }, { name: 'conductor.workbenchToken', value: own.started.token }] }],
    },
  })
  opened.push(ctx)
  return ctx.newPage()
}

interface Candidate {
  attempt: number
  commit?: string
  standing: boolean
  receipts: Array<{ phase: string; state: string; exit?: number }>
}

/**
 * A repository of the spec's own, a crew of a `worker-review` builder doing `build` with a check that passes, a project and a ticket,
 * revision 1 proposed, approved and launched; once build's attempt 1 stands with its check passed: the ticket's key, the run and the
 * candidate's commit.
 */
async function deliveredCandidate(): Promise<{ ticketKey: string; run: string; commit: string; candidate: string }> {
  const api = own.api
  const repo = join(own.root, `ext-${Date.now().toString(36)}`)
  mkdirSync(repo)
  scratchRepo(repo)
  git(repo, 'rev-parse', 'HEAD')
  const crewId = (
    await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: `e2e external ${Date.now().toString(36)}`,
      goal: 'deliver a candidate',
      cwd: repo,
      where: 'server',
      isolation: 'worktree',
      openAfterLaunch: false,
      yolo: null,
      members: [{ name: 'builder', agentId: 'worker-review', prompt: '', start: { when: 'task' } }],
      tasks: [{ id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.', checks: [{ id: 'ok', argv: ['true'] }] }],
    })
  ).crew.id
  const key = await freeProjectKey(api, 'RX')
  await makeProject(api, key)
  const ticketKey = await makeTicket(api, key, '1', { title: 'Deliver the list', description: 'It needs delivering.', acceptance: 'The list is delivered.' })
  const proposed = await api.ok<{ plan: { id: string } }>('POST', `/api/dispatch/tickets/${ticketKey}/plans`, { opId: opOf(key, 'r1'), crew: crewId })
  await api.ok('POST', `/api/dispatch/plans/${proposed.plan.id}/approve`, { opId: opOf(key, 'ap1'), note: 'Go.' })
  const l = await api.ok<{ launch: { runId: string } }>('POST', `/api/dispatch/tickets/${ticketKey}/launch`, { opId: opOf(key, 'l1'), plan: proposed.plan.id })
  const run = l.launch.runId
  runs.push(run)
  let c: Candidate | undefined
  await expect
    .poll(
      async () => {
        const r = await api.ok<{ candidates: Array<{ task: string; candidates: Candidate[] }> }>('GET', `/api/runs/${encodeURIComponent(run)}/candidates`)
        c = r.candidates.find((t) => t.task === 'build')?.candidates.find((x) => x.attempt === 1)
        return !!c?.commit && c.standing && c.receipts.some((x) => x.phase === 'check' && x.state === 'finished' && x.exit === 0)
      },
      { ...within(180_000), intervals: [500], message: "build's attempt 1 stands with its check passed" },
    )
    .toBe(true)
  return { ticketKey, run, commit: c!.commit!, candidate: `${run}/build/1@${c!.commit}` }
}

/** The checks at a commit: the failed Actions job with its log, a passing one, and 101 passing check runs, more than a read keeps. */
function jobsAt(commit: string) {
  const list: FakeCheck[] = [
    { kind: 'job', id: FAILED, name: 'test (ubuntu)', status: 'completed', conclusion: 'failure', log: 'x\n'.repeat(4000) + LOG_END },
    { kind: 'job', id: 2000002, name: 'build', status: 'completed', conclusion: 'success', log: 'ok\n' },
  ]
  for (let i = 0; i < 101; i++) list.push({ kind: 'run', id: 3000000 + i, name: `lint ${i}`, status: 'completed', conclusion: 'success' })
  gh.checks(commit, list)
}

/** The delivery link's last read, once it names checks at the commit (the read a link's add makes in the background, or a later one). */
async function readAt(ticketKey: string, link: string, commit: string) {
  await expect
    .poll(
      async () => {
        const r = await own.api.ok<{ read: { headSha?: string; checks: unknown[] } | null }>('GET', `/api/dispatch/tickets/${ticketKey}/links/${link}/read`)
        return !!r.read && r.read.headSha === commit && r.read.checks.length > 0
      },
      { ...within(60_000), intervals: [250], message: "the delivery link's read at the commit" },
    )
    .toBe(true)
}

test("a delivery's failed CI job: listed, the cut read said, prepared as GitHub's, sent as a finding read again by the server, and none when the head moved", async ({ browser }) => {
  const { ticketKey, run, commit, candidate } = await deliveredCandidate()
  gh.set({ n: 7, kind: 'pull', title: 'Deliver the list', body: '', state: 'open', headSha: commit })
  jobsAt(commit)
  const link = (
    await own.api.ok<{ receipt: { id: string } }>('POST', `/api/dispatch/tickets/${ticketKey}/links`, { opId: opOf(ticketKey, 'd1'), kind: 'delivery', url: gh.url(7, 'pull'), candidate })
  ).receipt.id
  await readAt(ticketKey, link, commit)

  const page = await pageOn(browser, { theme: 'dark' })
  await page.goto(`/dispatch/${ticketKey.split('-')[0]}/1?view=review&candidate=${encodeURIComponent(candidate)}`)
  const section = page.locator(`[data-review-external] [data-review-delivery="${link}"]`)
  await expect(section).toBeVisible(within(30_000))
  // The cut read in its own words, and never as passing.
  await expect(section.locator('[data-external-incomplete]')).toHaveText(/^incomplete: \d+ checks not read, see them on GitHub$/)
  await expect(section.locator('[data-external-all-passed]')).toHaveCount(0)
  // The failed job first, preparable; the passing one listed, not preparable.
  const failed = section.locator(`[data-external-job="job:${FAILED}"]`)
  await expect(section.locator('[data-external-job]').first()).toHaveAttribute('data-external-job', `job:${FAILED}`)
  await expect(failed).toHaveAttribute('data-external-job-failed', 'true')
  await expect(section.locator('[data-external-job="job:2000002"] [data-external-prepare]')).toHaveCount(0)

  // Prepared: GitHub's job, the end of its log, Check again not offered.
  await failed.locator('[data-external-prepare]').click()
  const panel = page.locator('[data-repair-panel]')
  await expect(panel.locator('[data-external-repair]')).toBeVisible(within(60_000))
  await expect(panel).toHaveAttribute('data-repair-receipt', `github:${gh.host}/${gh.owner}/${gh.repo}/check-runs/${FAILED}`)
  await expect(panel.locator('[data-repair-job-name]')).toHaveText('test (ubuntu)')
  await expect(panel.locator('[data-repair-subject]')).toHaveText(commit)
  await expect(panel.locator('[data-repair-ended]')).toHaveText('failure')
  await expect(panel.locator('[data-repair-tail]')).toContainText('--- FAIL: TestEmptyList (0.00s)')
  await expect(panel).toHaveAttribute('data-repair-recheck-allowed', 'false')
  await expect(panel.locator('[data-repair-recheck-why]')).toContainText('GitHub runs this job')
  await expect(page.locator('[data-repair-recheck]')).toHaveCount(0)
  // The log came through the redirect, fetched with no token; the token went to the API host's routes alone.
  expect(gh.requests().filter((r) => r.route === 'log' && r.path.startsWith('/_logs/')).every((r) => r.token === '')).toBe(true)
  expect(gh.requests().filter((r) => r.route !== 'other' && !r.path.startsWith('/_logs/')).every((r) => r.token === TOKEN)).toBe(true)

  // Added to the draft and sent as a comment: the server reads the job again and keeps the receipt external.
  await page.locator('[data-repair-add]').click()
  await expect(page.locator('[data-review-draft]')).toHaveAttribute('data-review-draft-notes', '1', within(15_000))
  await expect(page.locator('[data-review-draft-note]')).toContainText('CI job test (ubuntu) failed on GitHub')
  await page.locator('[data-review-verdict]').getByRole('radio', { name: 'Comment' }).click()
  const reads = gh.count('checks')
  await page.locator('[data-review-send]').click()
  await expect(page.locator('[data-review-sent]')).toHaveCount(1, within(60_000))
  const reviews = await own.api.ok<{ reviews: Array<{ id: string }> }>('GET', `/api/dispatch/tickets/${ticketKey}/reviews`)
  const review = await own.api.ok<{ review: { findings: Array<{ kind: string; check?: { source: string; receipt: string; headSha: string; name: string; run: string } }> } }>(
    'GET',
    `/api/dispatch/reviews/${reviews.reviews[0]!.id}`,
  )
  expect(review.review.findings[0]).toMatchObject({ kind: 'check', check: { source: 'external', receipt: `github:${gh.host}/${gh.owner}/${gh.repo}/check-runs/${FAILED}`, headSha: commit, name: 'test (ubuntu)', run: '' } })
  expect(gh.count('checks')).toBeGreaterThan(reads)
  // The Findings list names it by its job's name, as GitHub's.
  await expect(page.locator('[data-review-finding][data-finding-kind="check"]')).toContainText('CI job test (ubuntu) failed on GitHub', within(30_000))

  // The pull request's head moved on: its jobs tested another commit, and none is offered.
  const other = 'e'.repeat(40)
  gh.set({ n: 7, kind: 'pull', title: 'Deliver the list', body: '', state: 'open', headSha: other })
  jobsAt(other)
  await section.locator(`[data-review-delivery-read="${link}"]`).click()
  await expect(section.locator('[data-external-moved]')).toBeVisible(within(60_000))
  await expect(section.locator('[data-external-prepare]')).toHaveCount(0)
  // The run's candidate was the spec's own: nothing else is counted.
  expect(run).toBeTruthy()
})

test("the failed-check panel of a delivery's job holds a phone's width in both themes", async ({ browser }) => {
  const { ticketKey, commit, candidate } = await deliveredCandidate()
  gh.set({ n: 8, kind: 'pull', title: 'Deliver it again', body: '', state: 'open', headSha: commit })
  jobsAt(commit)
  const link = (
    await own.api.ok<{ receipt: { id: string } }>('POST', `/api/dispatch/tickets/${ticketKey}/links`, { opId: opOf(ticketKey, 'd1'), kind: 'delivery', url: gh.url(8, 'pull'), candidate })
  ).receipt.id
  await readAt(ticketKey, link, commit)
  for (const theme of ['light', 'dark'] as const) {
    const page = await pageOn(browser, { theme, width: 390 })
    await page.goto(`/dispatch/${ticketKey.split('-')[0]}/1?view=review&candidate=${encodeURIComponent(candidate)}`)
    const job = page.locator(`[data-review-delivery="${link}"] [data-external-job="job:${FAILED}"]`)
    await expect(job).toBeVisible(within(30_000))
    // The section is below the diff on a phone: measured once it is in view.
    await job.scrollIntoViewIfNeeded()
    await noSideways(page, { root: page.locator('[data-review-external]') })
    await job.locator('[data-external-prepare]').click()
    await expect(page.locator('[data-external-repair]')).toBeVisible(within(60_000))
    await noSideways(page, { root: page.locator('[data-repair-panel]') })
  }
})
