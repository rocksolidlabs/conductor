import { describe, expect, it } from 'vitest'
import type { RunInfo, RunTask, SessionInfo } from '~/composables/useSessions'
import type { BriefItem } from './brakeman'
import {
  blocksWords,
  decisionControls,
  handoffMarkdown,
  handoffTickets,
  isStale,
  madeWords,
  proposalArgs,
  proposalControl,
  revisionOf,
  staleWords,
  type GuideOperation,
  type LiveRecords,
} from './brakemanCards'

const RUN = 'e2e-tasks-fail-0123abcd'
const SESSION = '0123456789abcdef'
const QUESTION = 'fedcba9876543210'

const task = (p: Partial<RunTask>): RunTask => ({ id: 'build', member: 'builder', kind: 'work', state: 'ready', attempts: 0, ...p })
const run = (tasks: RunTask[], p: Partial<RunInfo> = {}) => ({ id: RUN, managed: { revision: 9 }, tasks, ...p }) as Pick<RunInfo, 'id' | 'stoppedAt' | 'record' | 'resumedBy' | 'managed' | 'tasks'>
const records = (r?: ReturnType<typeof run>, s?: Pick<SessionInfo, 'id' | 'name' | 'status' | 'attention'>): LiveRecords => ({
  run: (id) => (r && r.id === id ? r : undefined),
  session: (id) => (s && s.id === id ? s : undefined),
})

describe("a decision card's controls from the live record", () => {
  it('offers a failed work task with a submitted result Retry and Accept anyway, and no Cancel, with the revision the turn read', () => {
    const c = decisionControls(`task:${RUN}/build`, '7', records(run([task({ state: 'failed', attempts: 3, attempt: { n: 3, state: 'failed', result: 'abc' } })])))
    expect(c).toMatchObject({ kind: 'task', run: RUN, task: 'build', commands: ['retry', 'accept'], expectedRevision: 7, to: `/runs/${RUN}?task=build` })
  })

  it('offers a failed task without a submitted result Retry alone, and a failed review task no Accept anyway', () => {
    expect(decisionControls(`task:${RUN}/build`, '7', records(run([task({ state: 'failed', attempts: 1, attempt: { n: 1, state: 'failed' } })])))).toMatchObject({ commands: ['retry'] })
    expect(decisionControls(`task:${RUN}/build`, '7', records(run([task({ state: 'failed', kind: 'review', attempt: { n: 1, state: 'failed', result: 'abc' } })])))).toMatchObject({ commands: ['retry'] })
  })

  it('offers a blocked task Cancel and no Retry', () => {
    const c = decisionControls(`task:${RUN}/docs`, '7', records(run([task({ id: 'docs', state: 'blocked', reason: 'build will not succeed' })])))
    expect(c).toMatchObject({ kind: 'task', task: 'docs', commands: ['cancel'], expectedRevision: 7 })
  })

  it("follows the run page's rules: nothing that changes a stopped run, a record or a run continued", () => {
    const failed = task({ state: 'failed', attempt: { n: 1, state: 'failed', result: 'abc' } })
    for (const p of [{ stoppedAt: 'x' }, { record: true }, { resumedBy: 'next' }]) {
      const c = decisionControls(`task:${RUN}/build`, '7', records(run([failed], p)))
      expect(c).toMatchObject({ kind: 'task', commands: [] })
      expect(c.words).toBe('build is failed: nothing can be done to it from here now.')
    }
    // A task that succeeded since the turn read it: no command.
    expect(decisionControls(`task:${RUN}/build`, '7', records(run([task({ state: 'succeeded' })])))).toMatchObject({ commands: [] })
  })

  it('offers no command without the revision the turn read, and none for a task the page does not hold', () => {
    const c = decisionControls(`task:${RUN}/build`, undefined, records(run([task({ state: 'failed' })])))
    expect(c).toMatchObject({ kind: 'task', commands: [], expectedRevision: 0 })
    expect(c.words).toContain('named no revision')
    expect(decisionControls(`task:${RUN}/build`, '0', records(run([task({ state: 'failed' })])))).toMatchObject({ commands: [] })
    expect(decisionControls(`task:${RUN}/gone`, '7', records(run([])))).toMatchObject({ kind: 'none', to: `/runs/${RUN}?task=gone` })
    expect(decisionControls(`task:${RUN}/build`, '7', records())).toMatchObject({ kind: 'none' })
  })

  it("offers an open question's options as the session reports them and the session page; a question answered since, none", () => {
    const asking = { id: SESSION, name: 'builder', status: 'running' as const, attention: { state: 'needs_input' as const, id: QUESTION, options: [{ label: 'Yes', input: '1' }, { label: 'No', input: '2' }] } }
    const open = decisionControls(`question:${SESSION}/${QUESTION}`, undefined, records(undefined, asking))
    expect(open).toMatchObject({ kind: 'question', open: true, to: `/sessions/${SESSION}`, options: [{ label: 'Yes' }, { label: 'No' }] })
    // Another question now, or none, or one keys typed in the terminal answered (#258): not this card's.
    for (const attention of [{ ...asking.attention, id: 'aaaaaaaaaaaaaaaa' }, { state: '' as const }, { ...asking.attention, answered: 'terminal' as const }]) {
      const c = decisionControls(`question:${SESSION}/${QUESTION}`, undefined, records(undefined, { ...asking, attention }))
      expect(c).toMatchObject({ kind: 'question', open: false, options: [], to: `/sessions/${SESSION}` })
    }
    expect(decisionControls(`question:${SESSION}/${QUESTION}`, undefined, records())).toMatchObject({ kind: 'question', open: false })
  })

  it("leads a ticket's card to its page, and says so of a ref it has no control for", () => {
    expect(decisionControls('ticket:CND-12', '4', records())).toMatchObject({ kind: 'ticket', to: '/dispatch/CND/12' })
    expect(decisionControls('nonsense', undefined, records())).toMatchObject({ kind: 'none', to: null })
  })
})

describe("the stale revision's words", () => {
  it('says the record moved on, from what to what, and that nothing was done', () => {
    expect(staleWords('run', 7, 9, 'build is failed')).toBe('The run has moved on since Brakeman read it (revision 7; it is at revision 9 now, and build is failed). Nothing was done: look at it as it is before you act.')
    expect(staleWords('ticket', 3)).toBe('The ticket has moved on since Brakeman read it (revision 3). Nothing was done: look at it as it is before you act.')
    expect(staleWords('run', 7, 7)).toContain('(revision 7)')
  })
  it("tells the route's stale refusal from any other", () => {
    expect(isStale({ receipt: { refusal: 'stale_revision' } })).toBe(true)
    expect(isStale({ code: 'stale_revision' })).toBe(true)
    expect(isStale({ receipt: { refusal: 'task_state' } })).toBe(false)
    expect(isStale(new Error('x'))).toBe(false)
  })
  it('reads a revision as a positive whole number only', () => {
    expect(revisionOf('7')).toBe(7)
    expect([revisionOf(undefined), revisionOf(''), revisionOf('0'), revisionOf('07'), revisionOf('7a')]).toEqual([0, 0, 0, 0, 0])
  })
})

describe("a proposal's control", () => {
  const ops: GuideOperation[] = [
    { name: 'run_pause', revision: 'run' },
    { name: 'run_unpause', revision: 'run' },
    { name: 'run_stop' },
    { name: 'run_resume' },
    { name: 'task_retry', revision: 'run' },
    { name: 'task_cancel', revision: 'run' },
    { name: 'task_accept', revision: 'run' },
    { name: 'ticket_edit', revision: 'ticket' },
    { name: 'ticket_comment_admin', revision: 'ticket' },
    { name: 'crew_launch' },
  ]
  const p = (op: string, target: string, expectedRevision?: string): Extract<BriefItem, { kind: 'proposal/1' }> => ({ kind: 'proposal/1', text: 't', op, target, expectedRevision })

  it('is a button for an operation whose route checks a revision, with the revision the turn read', () => {
    expect(proposalControl(p('run_pause', `run:${RUN}`, '7'), ops)).toEqual({ kind: 'button', op: 'run_pause', target: 'run', run: RUN, task: undefined, ticket: undefined, label: 'Pause the run', ask: false, expectedRevision: 7 })
    expect(proposalControl(p('task_cancel', `task:${RUN}/build`, '7'), ops)).toMatchObject({ kind: 'button', run: RUN, task: 'build', label: 'Cancel build…', ask: true })
    expect(proposalControl(p('task_accept', `task:${RUN}/build`, '7'), ops)).toMatchObject({ kind: 'button', ask: true })
    expect(proposalControl(p('ticket_edit', 'ticket:CND-4', '3'), ops)).toMatchObject({ kind: 'button', ticket: 'CND-4', label: 'Edit CND-4', expectedRevision: 3 })
  })

  it("is text with the page's link for a stop, a resume, a launch, an operation not listed, and while the guide is unread", () => {
    for (const op of ['run_stop', 'run_resume']) {
      const c = proposalControl(p(op, `run:${RUN}`, '7'), ops)
      expect(c).toMatchObject({ kind: 'text', to: `/runs/${RUN}` })
      expect((c as { words: string }).words).toContain('checks no revision')
    }
    expect(proposalControl(p('crew_launch', 'run:' + RUN), ops)).toMatchObject({ kind: 'text' })
    expect(proposalControl(p('rm_rf', `run:${RUN}`, '7'), ops)).toMatchObject({ kind: 'text', words: 'rm_rf is no operation this server lists: do what it says from its page.' })
    expect(proposalControl(p('run_pause', `run:${RUN}`, '7'), null)).toMatchObject({ kind: 'text', to: `/runs/${RUN}` })
    // An operation this server's guide does not list: text, whatever this page could send.
    expect(proposalControl(p('ticket_link', 'ticket:CND-4', '3'), ops)).toMatchObject({ kind: 'text' })
  })

  it('is text without a revision, or on a target of another kind', () => {
    expect(proposalControl(p('run_pause', `run:${RUN}`), ops)).toMatchObject({ kind: 'text' })
    expect(proposalControl(p('task_retry', `run:${RUN}`, '7'), ops)).toMatchObject({ kind: 'text' })
  })

  it('sends its arguments without the fields the page sets itself', () => {
    expect(proposalArgs({ title: 'x', opId: 'mine', expectedRev: 1, byName: 'Brakeman' })).toEqual({ title: 'x' })
    expect(proposalArgs(undefined)).toEqual({})
  })
})

describe('handoffs and drafts', () => {
  const h: Extract<BriefItem, { kind: 'handoff/1' }> = { kind: 'handoff/1', objective: 'Finish the build', constraints: 'No new dependency.', decisions: ['Keep the check'], questions: ['Who reviews?'], evidence: [`task:${RUN}/build`, 'ticket:CND-4', 'comment:CND-4/0123456789abcdef', 'ticket:CND-9'] }
  it('is Markdown from its fields, and offers the tickets its refs name', () => {
    const md = handoffMarkdown(h, (r) => r.toUpperCase())
    expect(md).toContain('## Handoff: Finish the build')
    expect(md).toContain('### Constraints\n\nNo new dependency.')
    expect(md).toContain('- Keep the check')
    expect(md).toContain('### Open questions\n\n- Who reviews?')
    expect(md).toContain(`- TICKET:CND-4 (\`ticket:CND-4\`)`)
    expect(handoffTickets(h)).toEqual(['CND-4', 'CND-9'])
  })
  it("says what a draft became, and its blocks' relations", () => {
    expect(madeWords({ state: 'made', key: 'CND-5' })).toBe('Made as CND-5.')
    expect(madeWords({ state: 'made', crew: 'fix-it' })).toBe('Saved as the crew fix-it, not launched.')
    expect(madeWords({ state: 'refused', why: 'no such project' })).toBe('Refused: no such project. It can be made again.')
    expect(madeWords(undefined)).toBe('')
    const title = (i: number) => `draft ${i + 1}`
    expect(blocksWords(0, [1], undefined, title)).toBe('Blocks draft 2.')
    expect(blocksWords(0, [1], [{ from: 0, to: 1, state: 'pending' }], title)).toBe('Blocks draft 2 (related once both are made).')
    expect(blocksWords(0, [1], [{ from: 0, to: 1, state: 'made' }], title)).toBe('Blocks draft 2 (related).')
    expect(blocksWords(0, [], undefined, title)).toBe('')
  })
})
