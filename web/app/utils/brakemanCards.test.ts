import { describe, expect, it } from 'vitest'
import type { RunInfo, RunTask, SessionInfo } from '~/composables/useSessions'
import type { BriefItem } from './brakeman'
import {
  blocksWords,
  COMMENT_MAX,
  draftName,
  dropKeptMake,
  KEPT_MAX,
  KEPT_PREFIX,
  decisionControls,
  handoffMarkdown,
  handoffTickets,
  isStale,
  madeWords,
  makeAgain,
  PENDING_ELSEWHERE,
  planCardControl,
  proposalArgs,
  proposalControl,
  proposalSends,
  readKeptMakes,
  reconcileMade,
  revisionOf,
  sendKept,
  staleWords,
  storeKeptMake,
  unfinishedMake,
  utf8Bytes,
  type GuideOperation,
  type KeptMake,
  type KeptStorage,
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
    expect(c).toMatchObject({ kind: 'task', run: RUN, task: 'build', commands: ['retry', 'accept'], expectedRevision: 7, to: `/runs/${RUN}?view=tasks&task=build` })
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
    expect(decisionControls(`task:${RUN}/gone`, '7', records(run([])))).toMatchObject({ kind: 'none', to: `/runs/${RUN}?view=tasks&task=gone` })
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
    expect(proposalControl(p('ticket_edit', 'ticket:CND-4', '3'), ops)).toMatchObject({ kind: 'button', ticket: 'CND-4', label: 'Edit CND-4…', expectedRevision: 3 })
  })

  it("asks first for every ticket operation, which sends the proposal's arguments rather than its text", () => {
    expect(proposalControl(p('ticket_edit', 'ticket:CND-4', '3'), ops)).toMatchObject({ kind: 'button', ask: true })
    expect(proposalControl(p('ticket_comment_admin', 'ticket:CND-4', '3'), ops)).toMatchObject({ kind: 'button', label: 'Comment on CND-4…', ask: true })
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

  it('lists every field it sends, by its path, with its value as sent', () => {
    // A text that says "fix the typo" cannot hide an edit of the description: every field is on the page.
    expect(proposalSends({ set: { title: 'Fix the typo', description: 'Replaced whole.', priority: 1 }, add: { labels: ['bug'] }, opId: 'mine', byName: 'Brakeman' })).toEqual([
      { field: 'set.title', value: 'Fix the typo' },
      { field: 'set.description', value: 'Replaced whole.' },
      { field: 'set.priority', value: '1' },
      { field: 'add.labels', value: '["bug"]' },
    ])
    // A comment's body as its Markdown, every line of it.
    expect(proposalSends({ body: '## Why\n\n- the check' })).toEqual([{ field: 'body', value: '## Why\n\n- the check' }])
    expect(proposalSends({ kind: 'blocks', to: 'CND-9', meta: {} })).toEqual([
      { field: 'kind', value: 'blocks' },
      { field: 'to', value: 'CND-9' },
      { field: 'meta', value: '{}' },
    ])
    expect(proposalSends(undefined)).toEqual([])
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
    expect(madeWords({ state: 'pending' })).toBe(PENDING_ELSEWHERE)
    const title = (i: number) => `draft ${i + 1}`
    expect(blocksWords(0, [1], undefined, title)).toBe('Blocks draft 2.')
    expect(blocksWords(0, [1], [{ from: 0, to: 1, state: 'pending' }], title)).toBe('Blocks draft 2 (related once both are made).')
    expect(blocksWords(0, [1], [{ from: 0, to: 1, state: 'made' }], title)).toBe('Blocks draft 2 (related).')
    expect(blocksWords(0, [], undefined, title)).toBe('')
  })
  it('names a made draft by the ticket it became and the title it was made with, never the brief\'s title the person changed', () => {
    // The live check: draft 7's title edited, both made as BK-2 and BK-3; draft 6's line named BK-3 by the brief's title.
    const items: BriefItem[] = [
      { kind: 'fact/1', text: 'f', evidence: ['run:r-00000000'] },
      { kind: 'draft_ticket/1', project: 'BK', title: 'Add a CHANGELOG.md entry for RETRY.txt and the lint check', blocks: [2] },
      { kind: 'draft_ticket/1', project: 'BK', title: 'Add a CHANGELOG.md entry for RETRY.txt and the lint check' },
    ]
    const asMade: Record<string, string> = { 'BK-3': 'Add a CHANGELOG.md entry for RETRY.txt and the newline lint check' }
    const made = [
      { item: 1, state: 'made', key: 'BK-2' },
      { item: 2, state: 'made', key: 'BK-3' },
    ]
    const edges = [{ from: 1, to: 2, state: 'made' }]
    const name = (m: typeof made | undefined, titles: Record<string, string>) => (i: number) => draftName(i, items, m, (key) => titles[key])
    expect(blocksWords(1, [2], edges, name(made, asMade))).toBe('Blocks BK-3 "Add a CHANGELOG.md entry for RETRY.txt and the newline lint check" (related).')
    // Until the ticket's title is read from Dispatch: the key alone, never the brief's title.
    expect(blocksWords(1, [2], edges, name(made, {}))).toBe('Blocks BK-3 (related).')
    // Not made yet: the brief's title, as drafted; a refused make is no ticket.
    expect(blocksWords(1, [2], undefined, name(undefined, asMade))).toBe('Blocks "Add a CHANGELOG.md entry for RETRY.txt and the lint check".')
    expect(draftName(2, items, [{ item: 2, state: 'refused' }], () => 'x')).toBe('"Add a CHANGELOG.md entry for RETRY.txt and the lint check"')
    expect(draftName(0, items, made, () => undefined)).toBe('item 1')
  })
})

describe('a make that did not finish', () => {
  const kept = { opId: 'op-1', body: { by: 'Nate', edit: { title: 'As sent' } } }
  it('is kept for a lost reply or a failure of the server, and settled by any other answer', () => {
    expect(unfinishedMake({ status: 0 })).toBe(true)
    expect(unfinishedMake({ status: 500, code: 'make_failed' })).toBe(true)
    expect(unfinishedMake({ status: 503, code: 'store_unavailable' })).toBe(true)
    for (const status of [400, 404, 409, 410, 413]) expect(unfinishedMake({ status })).toBe(false)
    expect(unfinishedMake(new Error('x'))).toBe(false)
  })
  it('is kept for a refusal before the make route ran, which establishes nothing of its claim', () => {
    // requireAdmin's 401 (and its 429 for too many), and a request that timed out on its way: the make handler never ran.
    for (const status of [401, 408, 429]) expect(unfinishedMake({ status })).toBe(true)
  })
  it('is kept before it is sent, so a page read again while its reply is out still holds it', async () => {
    const storage = new Map<string, string>()
    let release: (v: string) => void = () => {}
    const out = sendKept(
      () => storage.set('br-1/11', 'op-1'),
      () => storage.delete('br-1/11'),
      () => new Promise<string>((done) => (release = done)),
    )
    // The reply is held: what a reload reads now is the kept make.
    expect(storage.get('br-1/11')).toBe('op-1')
    release('made')
    await expect(out).resolves.toBe('made')
    expect(storage.has('br-1/11')).toBe(false)
  })
  it('stays kept through a token refused on the finish, and is finished once the token is right again', async () => {
    const storage = new Map<string, string>()
    const keep = () => storage.set('br-1/11', 'op-1')
    const forget = () => storage.delete('br-1/11')
    const fail = (status: number) => () => Promise.reject(Object.assign(new Error(`status ${status}`), { status }))
    // The backend failed after the claim: kept.
    await expect(sendKept(keep, forget, fail(500))).rejects.toThrow('status 500')
    expect(storage.get('br-1/11')).toBe('op-1')
    // The finish is refused before the route runs (the token was cleared): still kept, the claim still pending.
    await expect(sendKept(keep, forget, fail(401))).rejects.toThrow('status 401')
    expect(storage.get('br-1/11')).toBe('op-1')
    // The token restored, the finish is answered: forgotten.
    await expect(sendKept(keep, forget, () => Promise.resolve('made'))).resolves.toBe('made')
    expect(storage.has('br-1/11')).toBe(false)
    // An answer that settles it forgets it too.
    await expect(sendKept(keep, forget, fail(409))).rejects.toThrow('status 409')
    expect(storage.has('br-1/11')).toBe(false)
  })
  it('is finished as it was sent while it is pending under its id, or not claimed at all', () => {
    expect(makeAgain({ state: 'pending', opId: 'op-1' }, kept)).toMatchObject({ kind: 'finish' })
    expect(makeAgain(undefined, kept)).toMatchObject({ kind: 'finish' })
    // Refused once under another id, then this ask failed before its claim: it is still this browser's to finish.
    expect(makeAgain({ state: 'refused', opId: 'op-0' }, kept)).toMatchObject({ kind: 'finish' })
  })
  it('is dropped once made, deleted, refused under its id, or held by another make', () => {
    expect(makeAgain({ state: 'made', opId: 'op-1' }, kept).kind).toBe('drop')
    expect(makeAgain({ state: 'deleted', opId: 'op-1' }, kept).kind).toBe('drop')
    expect(makeAgain({ state: 'refused', opId: 'op-1' }, kept).kind).toBe('drop')
    expect(makeAgain({ state: 'pending', opId: 'op-2' }, kept)).toEqual({ kind: 'drop', words: PENDING_ELSEWHERE })
  })
  it('says a pending make it holds nothing of is finished where it was asked, or at the next start, never by a step the page lacks', () => {
    expect(makeAgain({ state: 'pending', opId: 'op-2' }, undefined)).toEqual({ kind: 'none', words: PENDING_ELSEWHERE })
    expect(PENDING_ELSEWHERE).not.toContain('same id')
    expect(makeAgain(undefined, undefined)).toEqual({ kind: 'none', words: '' })
  })
})

describe("a request's makes and edges, as replies bring them", () => {
  const at = (s: number) => `2026-11-02T09:00:${String(s).padStart(2, '0')}.000Z`
  it('never go back: a reply sent before another leaves an entry made and an edge related', () => {
    const newer = { made: [{ item: 6, opId: 'a', state: 'made', key: 'BKC-2', at: at(1) }, { item: 7, opId: 'b', state: 'made', key: 'BKC-3', at: at(2) }], edges: [{ from: 6, to: 7, op: 'e', state: 'made' }] }
    const older = { made: [{ item: 6, opId: 'a', state: 'made', key: 'BKC-2', at: at(1) }, { item: 7, opId: 'b', state: 'pending', at: at(2) }], edges: [{ from: 6, to: 7, op: 'e', state: 'pending' }] }
    expect(reconcileMade(newer, older)).toEqual(newer)
    // In order, each reply moves them on.
    expect(reconcileMade(older, newer)).toEqual(newer)
  })
  it('keep what a reply lacks, and take a newer claim of an item over an older one', () => {
    const had = { made: [{ item: 6, opId: 'a', state: 'refused', why: 'no such project', at: at(1) }], edges: [{ from: 6, to: 7, op: 'e', state: 'pending' }] }
    expect(reconcileMade(had, {})).toEqual(had)
    const again = { made: [{ item: 6, opId: 'c', state: 'pending', at: at(5) }] }
    expect(reconcileMade(had, again).made).toEqual(again.made)
    // A reply from before the new claim does not bring the refused one back.
    expect(reconcileMade({ made: again.made }, had).made).toEqual(again.made)
  })
})

describe("a handoff's comment", () => {
  it("is measured in UTF-8 bytes against Dispatch's bound", () => {
    expect(COMMENT_MAX).toBe(16384)
    expect(utf8Bytes('é')).toBe(2)
    // A valid handoff can pass it: twenty decisions and twenty questions of 500 bytes each.
    const big: Extract<BriefItem, { kind: 'handoff/1' }> = { kind: 'handoff/1', objective: 'o', decisions: Array.from({ length: 20 }, () => 'd'.repeat(500)), questions: Array.from({ length: 20 }, () => 'q'.repeat(500)) }
    expect(utf8Bytes(handoffMarkdown(big))).toBeGreaterThan(COMMENT_MAX)
    expect(utf8Bytes('x'.repeat(COMMENT_MAX))).toBe(COMMENT_MAX)
  })
})

/** A browser's localStorage, shared by every tab of its origin. */
function sharedStorage(): KeptStorage & { raw: Map<string, string> } {
  const raw = new Map<string, string>()
  return {
    raw,
    get length() {
      return raw.size
    },
    key: (i) => [...raw.keys()][i] ?? null,
    getItem: (k) => raw.get(k) ?? null,
    setItem: (k, v) => void raw.set(k, String(v)),
    removeItem: (k) => void raw.delete(k),
  }
}

/** A tab as the composable is one: its own copy read once at start, and keep and forget writing the one make's entry. */
function tab(s: KeptStorage) {
  let kept = readKeptMakes(s)
  return {
    kept: () => kept,
    make: (key: string, k: KeptMake, send: () => Promise<unknown>) =>
      sendKept(
        () => {
          kept = { ...kept, [key]: k }
          storeKeptMake(s, key, k)
        },
        () => {
          const { [key]: _gone, ...rest } = kept
          kept = rest
          dropKeptMake(s, key)
        },
        send,
      ),
  }
}

describe("a browser's unfinished makes, across its tabs", () => {
  it("survive another tab's make that finished: each tab writes its own make's entry alone", async () => {
    const s = sharedStorage()
    // Both tabs read the storage before either makes.
    const a = tab(s)
    const b = tab(s)
    const edited: KeptMake = { opId: 'bkm-a', body: { by: 'Nate', edit: { title: 'Edited in A' } }, at: 1 }
    await expect(a.make('br-1/14', edited, () => Promise.reject(Object.assign(new Error('the make did not finish'), { status: 500, code: 'make_failed' })))).rejects.toThrow()
    expect(a.kept()['br-1/14']).toEqual(edited)
    await b.make('br-1/15', { opId: 'bkm-b', body: { by: 'Nate' }, at: 2 }, () => Promise.resolve({ made: true }))
    expect(b.kept()).toEqual({})
    // A read again still holds its make, with the content it was asked with; B's left nothing.
    expect(tab(s).kept()).toEqual({ 'br-1/14': edited })
    expect([...s.raw.keys()]).toEqual([`${KEPT_PREFIX}br-1/14`])
  })

  it('keep the newest KEPT_MAX, whichever tab asked them, and skip an entry that does not read as one', () => {
    const s = sharedStorage()
    s.setItem(`${KEPT_PREFIX}br-9/1`, '{not json')
    s.setItem(`${KEPT_PREFIX}br-9/2`, JSON.stringify({ opId: 7, body: {} }))
    s.setItem('conductor.other', 'kept')
    for (let i = 0; i < KEPT_MAX + 3; i++) storeKeptMake(s, `br-${i + 1}/0`, { opId: `bkm-${i}`, body: {}, at: 1000 + i })
    const kept = readKeptMakes(s)
    expect(Object.keys(kept)).toHaveLength(KEPT_MAX)
    expect(kept['br-1/0']).toBeUndefined()
    expect(kept[`br-${KEPT_MAX + 3}/0`]?.opId).toBe(`bkm-${KEPT_MAX + 2}`)
    expect(s.getItem('conductor.other')).toBe('kept')
  })
})

describe("a plan revision's card, DS2's Approve from its live record (PR-11)", () => {
  const rev = (over: Record<string, unknown> = {}) => ({ n: 2, state: 'proposed' as const, crewName: 'team', ...over })

  it('is a plan control for a plan: ref, which the card reads live by its ticket and number', () => {
    expect(decisionControls('plan:CND-4/2', undefined, {})).toEqual({ kind: 'plan', ticket: 'CND-4', n: 2, to: '/dispatch/CND/4', words: "Revision 2 of CND-4's plan, as Dispatch holds it now." })
  })

  it("offers Approve by DS2's own rule, the operator's, and Approve again only where an approval cures what changed", () => {
    expect(planCardControl(rev(), 'CND-4', 2)).toMatchObject({ approve: 'Approve' })
    expect(planCardControl(rev(), 'CND-4', 2).words).toContain('Brakeman never does')
    expect(planCardControl(rev({ state: 'superseded' }), 'CND-4', 2)).toMatchObject({ approve: 'Approve' })
    expect(planCardControl(rev({ state: 'approved', stale: { words: 'The title was edited', remedy: 'approve' } }), 'CND-4', 2)).toMatchObject({ approve: 'Approve again' })
    expect(planCardControl(rev({ state: 'approved', stale: { words: 'A comment it hands over was edited', remedy: 'revise' } }), 'CND-4', 2)).toMatchObject({ approve: null })
  })

  it('offers nothing for a revision approved and holding, withdrawn, whose agents changed, or not there', () => {
    expect(planCardControl(rev({ state: 'approved' }), 'CND-4', 2)).toEqual({ approve: null, words: 'CND-4 r2 is approved, and its approval holds.' })
    expect(planCardControl(rev({ state: 'withdrawn' }), 'CND-4', 2)).toMatchObject({ approve: null, words: 'CND-4 r2 was withdrawn: nothing approves it now.' })
    expect(planCardControl(rev({ agentsChanged: true }), 'CND-4', 2)).toMatchObject({ approve: null })
    expect(planCardControl(null, 'CND-4', 2)).toEqual({ approve: null, words: "CND-4 r2 is not among CND-4's revisions now: open the ticket." })
  })

  it("is text for a proposal of an operation a grant names, which the ticket's Plan section does", () => {
    const ops: GuideOperation[] = [{ name: 'launch_approved_plan', scope: 'act', revision: 'ticket' }]
    const c = proposalControl({ kind: 'proposal/1', text: 't', op: 'launch_approved_plan', target: 'ticket:CND-4', expectedRevision: '3' }, ops)
    expect(c).toMatchObject({ kind: 'text', to: '/dispatch/CND/4' })
    expect((c as { words: string }).words).toContain("ticket's Plan section")
  })

  it('says a proposed draft plan is the operator\'s to approve', () => {
    expect(madeWords({ state: 'made', key: 'CND-4', plan: '0123456789abcdef' })).toBe("Proposed as a revision of CND-4's plan, under your name: the operator approves it or not.")
  })
})
