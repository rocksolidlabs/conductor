import { expect, logged, task, test, type Run } from './fixtures'

// Messages and checkpoints in a managed run (#334, rows 9 and 10): the
// builder saves a checkpoint before it submits and sends the writer a
// message after; the message is kept with the run, typed into the writer's
// session once its attempt has its task, and read from its inbox; the
// checkpoint is on the attempt and on the page.
test.describe.configure({ mode: 'serial' })

let crewId = ''
const runs: string[] = []

const crewBody = {
  name: 'e2e tasks messages',
  goal: 'pass the word',
  cwd: '',
  where: 'server',
  isolation: 'worktree',
  openAfterLaunch: false,
  yolo: null,
  members: [
    { name: 'builder', agentId: 'worker-chatty', prompt: '', start: { when: 'task' } },
    { name: 'writer', agentId: 'worker', prompt: '', start: { when: 'task' } },
  ],
  tasks: [
    { id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.' },
    { id: 'docs', member: 'writer', kind: 'work', instructions: 'Document it.', needs: ['build'] },
  ],
}

test.beforeAll(async ({ api }) => {
  crewId = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', crewBody)).crew.id
})

test.afterAll(async ({ api }) => {
  for (const id of runs) await api.stopRun(id)
  if (crewId) await api.call('DELETE', `/api/crews/${encodeURIComponent(crewId)}`)
})

test("the builder's message reaches the writer once it has its task, and its checkpoint is on the attempt", async ({ api, page }) => {
  const run = await api.launchCrew(crewId)
  runs.push(run.id)
  let last: Run = run
  await expect
    .poll(async () => {
      last = await api.run(run.id)
      return last.outcome ?? ''
    }, { timeout: 120_000, intervals: [500] })
    .toBe('succeeded')
  // The message: kept, delivered to the writer after its task line, in its inbox. Its delivery is
  // recorded once its line is entered, a submit pause after the writer's task line: a writer as quick
  // as the stub has submitted, and the run has succeeded, before that (#281). So the spec waits for
  // the delivery itself, and for the log line that says so, before it reads either.
  type Message = { seq: number; from: string; to: string; text: string; deliveredAt?: string }
  let messages: Message[] = []
  await expect
    .poll(async () => {
      messages = (await api.ok<{ messages: Message[] }>('GET', `/api/runs/${encodeURIComponent(run.id)}/messages`)).messages
      return messages.some((m) => m.seq === 1 && !!m.deliveredAt)
    })
    .toBe(true)
  expect(messages.map((m) => [m.seq, m.from, m.to, m.text])).toEqual([[1, 'builder', 'writer', 'stub builder says: build is submitted, attempt 1']])
  await expect
    .poll(async () => {
      last = await api.run(run.id)
      return logged(last, 'message 1 from builder delivered to writer')
    })
    .toBe(true)
  expect(logged(last, 'message 1 from builder to writer: stub builder says: build is submitted, attempt 1')).toBe(true)
  const writer = task(last, 'docs')
  const inbox = await api.ok<{ messages: Array<{ seq: number; text: string }> }>('GET', `/api/sessions/${writer.attempt?.sessionId}/inbox`)
  expect(inbox.messages.map((m) => m.seq)).toEqual([1])
  const builder = task(last, 'build')
  expect((await api.ok<{ messages: unknown[] }>('GET', `/api/sessions/${builder.attempt?.sessionId}/inbox`)).messages).toEqual([])
  // The checkpoint: on build's attempt, tied to its result commit.
  const { attempts } = await api.ok<{ attempts: Array<{ n: number; result?: string; checkpoint?: { done: string; remaining: string; commit: string } }> }>('GET', `/api/runs/${encodeURIComponent(run.id)}/tasks/build`)
  expect(attempts[0].checkpoint?.done).toBe('stub-builder.txt written')
  expect(attempts[0].checkpoint?.commit).toBe(attempts[0].result)
  expect(logged(last, 'task build: attempt 1 checkpoint at')).toBe(true)
  // The page: the checkpoint under the attempt.
  await page.goto(`/runs/${encodeURIComponent(run.id)}?view=tasks&task=build`)
  await expect(page.locator('[data-task-detail-id="build"] [data-attempt-checkpoint]')).toContainText('done: stub-builder.txt written')
})
