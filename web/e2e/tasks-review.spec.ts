import { execFileSync } from 'node:child_process'
import { expect, logged, task, test, type Run } from './fixtures'

// A review in a managed run (#334, row 6): the reviewer's needs_changes
// reopens the task it reviewed, whose next attempt starts at the reviewed
// commit with the findings in its task file; the review runs again on the
// new result and approves; the run succeeds with both tasks' results kept.
test.describe.configure({ mode: 'serial' })

let crewId = ''
const runs: string[] = []

const crewBody = {
  name: 'e2e tasks review',
  goal: 'review the harness',
  cwd: '',
  where: 'server',
  isolation: 'worktree',
  openAfterLaunch: false,
  yolo: null,
  members: [
    { name: 'builder', agentId: 'worker-cli', prompt: '', start: { when: 'task' } },
    { name: 'critic', agentId: 'worker', prompt: '', start: { when: 'task' } },
  ],
  tasks: [
    { id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.' },
    { id: 'review', member: 'critic', kind: 'review', instructions: 'Review it.', needs: ['build'] },
  ],
}

test.beforeAll(async ({ api }) => {
  crewId = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', crewBody)).crew.id
})

test.afterAll(async ({ api }) => {
  for (const id of runs) await api.stopRun(id)
  if (crewId) await api.call('DELETE', `/api/crews/${encodeURIComponent(crewId)}`)
})

const git = (cwd: string, ...args: string[]) => execFileSync('git', args, { cwd, encoding: 'utf8' }).trim()

test('needs_changes reopens the reviewed task with the findings, and the review approves the repair', async ({ api, state }) => {
  const run = await api.launchCrew(crewId)
  runs.push(run.id)
  let last: Run = run
  await expect
    .poll(async () => {
      last = await api.run(run.id)
      return last.outcome ?? ''
    }, { timeout: 120_000, intervals: [500] })
    .toBe('succeeded')
  const build = task(last, 'build')
  const review = task(last, 'review')
  expect([build.state, build.attempts, review.state, review.attempts]).toEqual(['succeeded', 2, 'succeeded', 2])
  expect(review.result?.verdict).toBe('approved')
  // The review's first attempt said needs_changes with its finding, kept on the attempt.
  const rev = await api.ok<{ attempts: Array<{ n: number; state: string; start?: string; submitted?: { verdict?: string; findings?: Array<{ message: string }> } }> }>(
    'GET',
    `/api/runs/${encodeURIComponent(run.id)}/tasks/review`,
  )
  expect(rev.attempts.map((a) => [a.n, a.state, a.submitted?.verdict])).toEqual([
    [1, 'succeeded', 'needs_changes'],
    [2, 'succeeded', 'approved'],
  ])
  expect(rev.attempts[0].submitted?.findings?.[0]?.message).toBe('add stub-fixed.txt')
  // build's second attempt started at the reviewed commit (attempt 1's result) and added the file the finding asked for.
  const b = await api.ok<{ attempts: Array<{ n: number; start?: string; result?: string }> }>('GET', `/api/runs/${encodeURIComponent(run.id)}/tasks/build`)
  expect(b.attempts[1].start).toBe(b.attempts[0].result)
  expect(build.result?.commit).toBe(b.attempts[1].result)
  expect(git(state.repo, 'cat-file', '-e', `${build.result?.commit}:stub-fixed.txt`)).toBe('')
  // Each review attempt looked at the build attempt of the same number.
  expect(rev.attempts.map((a) => a.start)).toEqual(b.attempts.map((a) => a.result))
  expect(logged(last, 'task build: review review asked for changes (1 finding); attempt 2 starts at the reviewed commit')).toBe(true)
  expect(logged(last, 'every task is done: the run succeeded')).toBe(true)
})
