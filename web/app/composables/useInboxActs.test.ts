import { describe, expect, it } from 'vitest'
import { InboxActs, sessionFor, type ActSends, type ActsFeedback, type SessionKnown } from './useInboxActs'
import { InboxLive, type InboxReads, type InboxState } from './useInbox'
import { ReplyError } from '~/utils/reply'
import { CommandRefused } from '~/utils/tasks'
import { stepsFinal, trackKey, UNCONFIRMED, unknownWords, type DeliveryStep, type InboxAction, type InboxItem, type RecentEntry, type StepState } from '~/utils/inbox'
import type { AnswerOutcome } from './useQuickReply'

// Every send of the decision page has one outcome, of four (#647, review round 2): sent, confirmed by a receipt, refused by a
// receipt, unknown. A lost reply is unknown, never a refusal; what the steps say past the send is the server's track's, read before
// and after the receipt commits; an explicit refusal ends the steps whatever the track says of the item; the session's kind is known
// before an answer goes; and an action's feedback is its item's alone.

const steps = (...states: StepState[]): DeliveryStep[] => (['available', 'recorded', 'validated', 'resumed'] as const).map((step, i) => ({ step, state: states[i]!, words: `${step} ${states[i]}` }))
const stateOf = (s: DeliveryStep[] | undefined) => s?.map((x) => x.state)
const settle = () => new Promise((r) => setTimeout(r, 0))

/** A server whose tracks the test sets, by receipt: what each read of an action's steps answers now. */
function fakeServer() {
  const tracks = new Map<string, DeliveryStep[]>()
  const reads: InboxReads = {
    list: () => Promise.resolve({ items: [], total: 0, at: '' }),
    track: (e) => Promise.resolve({ ref: e.ref, receipt: e.receipt, steps: tracks.get(e.receipt) ?? steps('done', 'unknown', 'unknown', 'unknown') }),
  }
  return { tracks, reads }
}

/** A send the test settles: held until it is let go with a value or a failure. */
function held<T>() {
  let done!: (v: T) => void
  let fail!: (e: unknown) => void
  const p = new Promise<T>((resolve, reject) => {
    done = resolve
    fail = reject
  })
  return { p, done, fail }
}

const question = (ref = 'question:s1/q1'): { it: InboxItem; a: InboxAction } => ({
  it: { ref, subject: ref, kind: 'question', since: '', title: 'asked', brief: '', briefBy: 'conductor', question: 'Which?', answerer: 'you', revision: 'q1', blocking: { words: '', refs: [] }, actions: [], recommended: -1 },
  a: { name: 'answer', label: 'Postgres', input: '1', expect: ref.slice(ref.lastIndexOf('/') + 1), scope: '', route: '/ws/sessions/s1' },
})
const task: { it: InboxItem; a: InboxAction } = {
  it: { ref: 'task:r1/build', subject: 'task:r1/build', kind: 'task', since: '', title: 'build failed', brief: '', briefBy: 'conductor', question: '', answerer: 'you', revision: '7', blocking: { words: '', refs: [] }, actions: [], recommended: -1 },
  a: { name: 'retry', label: 'Retry', expect: '7', scope: '', route: '/api/runs/r1/tasks/build/retry' },
}
// Check again of a continuation's task: its route is the candidate's own run (r0), its steps the item's (r1).
const recheck: { it: InboxItem; a: InboxAction } = {
  it: task.it,
  a: { name: 'recheck', label: 'Check again', expect: '7', scope: "Checks attempt 3 of run r0's commit 0123456789ab again", route: 'POST /api/runs/r0/tasks/build/recheck', attempt: 3, commit: '0123456789abcdef0123456789abcdef01234567' },
}
const plan: { it: InboxItem; a: InboxAction } = {
  it: { ref: 'plan:DEC-1/1', subject: 'plan:DEC-1/1', kind: 'plan', since: '', title: 'its plan', brief: '', briefBy: 'conductor', question: '', answerer: 'you', revision: '3', blocking: { words: '', refs: [] }, actions: [], recommended: -1 },
  a: { name: 'approve', label: 'Approve', expect: '3', scope: '', route: '/api/dispatch/plans/pl-1/approve' },
}

/** The page's sends, each a held promise the test settles, with what each was called with. */
function sendsOf(o: { listed?: SessionKnown[]; read?: (id: string) => Promise<SessionKnown> } = {}) {
  const calls: { answer: Array<{ s: SessionKnown; reqId: string }>; command: string[]; recheck: Array<{ run: string; task: string; attempt: number; commit: string; expectedRevision?: number; commandId: string }>; plan: string[] } = {
    answer: [],
    command: [],
    recheck: [],
    plan: [],
  }
  let next: ReturnType<typeof held<unknown>> = held()
  const sends: ActSends = {
    session: (id) => sessionFor(o.listed ?? [{ id: 's1', kind: 'server', name: 'one' }], id, o.read ?? (() => Promise.reject(new Error('no such session')))),
    answer: (s, _what, x) => {
      calls.answer.push({ s, reqId: x.reqId })
      return next.p as Promise<AnswerOutcome>
    },
    command: (_run, _task, _name, x) => {
      calls.command.push(x.commandId)
      return next.p
    },
    recheck: (run, task, x) => {
      calls.recheck.push({ run, task, ...x })
      return next.p
    },
    plan: (_a, _id, x) => {
      calls.plan.push(x.opId)
      return next.p
    },
    comment: () => next.p,
    movedOn: () => Promise.resolve('The run moved on. Nothing was done.'),
  }
  return {
    sends,
    calls,
    /** The next send's reply, held until settled. */
    hold: () => (next = held()),
    get reply() {
      return next
    },
  }
}

/** The page: its live inbox tracking (its first read answered), its sends and its feedback. */
async function page(o: Parameters<typeof sendsOf>[0] = {}) {
  const srv = fakeServer()
  let state: InboxState | null = null
  const l = new InboxLive()
  l.attach(srv.reads, { apply: (s) => (state = s) })
  const stop = l.track()
  const s = sendsOf(o)
  let feedback: ActsFeedback = {}
  let n = 0
  const acts = new InboxActs(l, s.sends, (f) => (feedback = f), { request: () => `d${++n}`, command: () => `c${++n}` })
  await l.changed()
  const entry = (ref: string) => state!.recent.find((e) => e.ref === ref)!
  return { srv, l, s, acts, stop, state: () => state!, feedback: () => feedback, entry, track: (e: RecentEntry) => state!.tracks[trackKey(e)] }
}

/** The three paths, each with its track before the receipt commits and after, by outcome. */
const paths = [
  {
    name: 'an answer',
    item: question(),
    receipt: (_e: RecentEntry) => 'q1',
    before: steps('done', 'waiting', 'waiting', 'waiting'),
    taken: steps('done', 'done', 'done', 'waiting'),
    refusedTrack: steps('done', 'refused', 'refused', 'not_applicable'),
    confirm: 'taken',
    refusal: new ReplyError('question_answered', 'this question was already answered by Nate: nothing was sent'),
  },
  {
    name: "a task's command",
    item: task,
    receipt: (e: RecentEntry) => e.receipt,
    before: steps('done', 'unknown', 'unknown', 'unknown'),
    taken: steps('done', 'done', 'waiting', 'waiting'),
    refusedTrack: steps('done', 'done', 'refused', 'not_applicable'),
    confirm: undefined,
    refusal: new CommandRefused('the run moved on', { status: 'failed', refusal: 'stale_revision' } as never),
  },
  {
    name: 'a Check again',
    item: recheck,
    receipt: (e: RecentEntry) => e.receipt,
    before: steps('done', 'unknown', 'unknown', 'unknown'),
    taken: steps('done', 'done', 'done', 'done'),
    refusedTrack: steps('done', 'done', 'refused', 'not_applicable'),
    confirm: undefined,
    refusal: new CommandRefused("the candidate is no longer the task's standing one: attempt 4 failed since, at 0123456789ab: check that one again", {
      status: 'failed',
      refusal: 'stale_candidate',
    } as never),
  },
  {
    name: "a plan's approval",
    item: plan,
    receipt: (e: RecentEntry) => e.receipt,
    before: steps('done', 'unknown', 'unknown', 'unknown'),
    taken: steps('done', 'done', 'done', 'not_applicable'),
    // A refused approval's receipt names no revision: the server cannot say whose it was.
    refusedTrack: steps('done', 'unknown', 'unknown', 'unknown'),
    confirm: undefined,
    refusal: new CommandRefused('the revision moved on', { status: 'failed', refusal: 'stale_revision' } as never),
  },
]

describe('every send of the decision page', () => {
  for (const p of paths) {
    describe(p.name, () => {
      it('whose reply is lost is unknown, never refused, with the track read before the receipt commits; the next read finds it taken', async () => {
        const pg = await page()
        pg.s.hold()
        const out = pg.acts.act(p.item.it, p.item.a)
        await settle()
        const e = pg.entry(p.item.it.ref)
        pg.srv.tracks.set(p.receipt(e), p.before)
        pg.s.reply.fail(p.name === 'an answer' ? new ReplyError('closed', 'the connection closed first') : Object.assign(new TypeError('Failed to fetch'), { status: 0 }))
        expect(await out).toEqual({ how: 'unconfirmed' })
        expect(pg.entry(p.item.it.ref).outcome).toBe(UNCONFIRMED)
        expect(pg.feedback()[p.item.it.ref]).toMatchObject({ busy: '', words: '', unknown: '' })
        expect(stateOf(pg.track(e))).not.toContain('refused')
        expect(stepsFinal(pg.track(e))).toBe(false)
        expect(unknownWords(pg.entry(p.item.it.ref), pg.track(e), false)).not.toBe('')
        // It was taken after all: the next read of the list reads its track again, and the steps say so; nothing was sent again.
        pg.srv.tracks.set(p.receipt(e), p.taken)
        await pg.l.changed()
        expect(stateOf(pg.track(e))).toEqual(stateOf(p.taken))
        expect(unknownWords(pg.entry(p.item.it.ref), pg.track(e), false)).toBe('')
        expect(pg.s.calls.answer.length + pg.s.calls.command.length + pg.s.calls.recheck.length + pg.s.calls.plan.length).toBe(1)
      })

      it('refused by its receipt is refused with its words, the steps ended there, read before or after the refusal commits', async () => {
        for (const read of ['before', 'after'] as const) {
          const pg = await page()
          pg.s.hold()
          const out = pg.acts.act(p.item.it, p.item.a)
          await settle()
          const e = pg.entry(p.item.it.ref)
          pg.srv.tracks.set(p.receipt(e), read === 'before' ? p.before : p.refusedTrack)
          pg.s.reply.fail(p.refusal)
          const came = await out
          expect(came?.how).toBe('refused')
          expect(pg.entry(p.item.it.ref).outcome).toMatch(/^refused: /)
          expect(pg.feedback()[p.item.it.ref]!.words).not.toBe('')
          expect(pg.track(e)!.find((x) => x.step === 'validated')!.state).toBe('refused')
          expect(pg.track(e)!.find((x) => x.step === 'resumed')!.state).toBe('not_applicable')
          expect(stepsFinal(pg.track(e))).toBe(true)
        }
      })

      it('confirmed by its receipt fills nothing the server can know: the track says recorded and validated, before and after the commit', async () => {
        const pg = await page()
        pg.s.hold()
        const out = pg.acts.act(p.item.it, p.item.a)
        await settle()
        const e = pg.entry(p.item.it.ref)
        pg.srv.tracks.set(p.receipt(e), p.before)
        pg.s.reply.done(p.confirm)
        expect(await out).toEqual({ how: 'confirmed' })
        expect(pg.entry(p.item.it.ref).outcome).toBe('accepted')
        expect(pg.feedback()[p.item.it.ref]).toMatchObject({ busy: '', words: '' })
        // Before the commit: as the server said it, nothing made up from the reply.
        expect(stateOf(pg.track(e))).toEqual(stateOf(p.before))
        expect(stepsFinal(pg.track(e))).toBe(false)
        expect(unknownWords(pg.entry(p.item.it.ref), pg.track(e), false)).toBe('')
        pg.srv.tracks.set(p.receipt(e), p.taken)
        await pg.l.changed()
        expect(stateOf(pg.track(e))).toEqual(stateOf(p.taken))
      })
    })
  }
})

describe('a Check again (#713)', () => {
  it("names its candidate to the candidate's run's route at the run's revision, its steps kept under the item's ref", async () => {
    const pg = await page()
    pg.s.hold()
    const out = pg.acts.act(recheck.it, recheck.a)
    await settle()
    expect(pg.s.calls.recheck).toEqual([{ run: 'r0', task: 'build', attempt: 3, commit: recheck.a.commit, expectedRevision: 7, commandId: pg.entry(recheck.it.ref).receipt }])
    expect(pg.s.calls.command).toEqual([])
    expect(pg.entry(recheck.it.ref)).toMatchObject({ ref: 'task:r1/build', label: 'Check again' })
    pg.s.reply.done(undefined)
    expect(await out).toEqual({ how: 'confirmed' })
  })

  it('whose candidate no longer stands is refused in the words that say what stands now, shown where a refused command is', async () => {
    const pg = await page()
    pg.s.hold()
    const out = pg.acts.act(recheck.it, recheck.a)
    await settle()
    pg.s.reply.fail(paths.find((p) => p.name === 'a Check again')!.refusal)
    expect(await out).toMatchObject({ how: 'refused' })
    expect(pg.feedback()[recheck.it.ref]!.words).toContain('attempt 4 failed since, at 0123456789ab: check that one again')
    expect(pg.entry(recheck.it.ref).outcome).toContain('attempt 4 failed since')
  })

  it('that names no candidate sends nothing', async () => {
    const pg = await page()
    expect(await pg.acts.act(recheck.it, { ...recheck.a, attempt: undefined })).toBeNull()
    expect(pg.s.calls.recheck).toEqual([])
  })
})

describe("an answer's session", () => {
  it('is known before anything is sent: a session the item names before the stream lists it is read, and its answer goes as a hosted one', async () => {
    let read!: (s: SessionKnown) => void
    const pg = await page({ listed: [], read: () => new Promise((r) => (read = r)) })
    pg.s.hold()
    const q = question('question:h1/q9')
    const out = pg.acts.act(q.it, q.a)
    await settle()
    // The kind not known yet: nothing sent, nothing kept as sent.
    expect(pg.s.calls.answer).toEqual([])
    expect(pg.state().recent).toEqual([])
    expect(pg.acts.pending(q.it.ref)).toBe(true)
    read({ id: 'h1', kind: 'hosted', name: 'on the laptop' })
    await settle()
    expect(pg.s.calls.answer.map((c) => c.s.kind)).toEqual(['hosted'])
    pg.s.reply.done('taken')
    expect(await out).toEqual({ how: 'confirmed' })
  })

  it('that cannot be known sends nothing and keeps no action, and says why', async () => {
    const pg = await page({ listed: [] })
    const q = question('question:gone/q1')
    expect((await pg.acts.act(q.it, q.a))?.how).toBe('not_sent')
    expect(pg.s.calls.answer).toEqual([])
    expect(pg.state().recent).toEqual([])
    expect(pg.feedback()[q.it.ref]!.words).toContain('Not sent')
  })

  it("is the stream's listing when it holds the session, read by its id otherwise", async () => {
    const listed: SessionKnown[] = [{ id: 's1', kind: 'hosted', name: 'a' }]
    expect(await sessionFor(listed, 's1', () => Promise.reject(new Error('not read')))).toEqual(listed[0])
    expect(await sessionFor([], 's2', (id) => Promise.resolve({ id, kind: 'server', name: 'b' }))).toEqual({ id: 's2', kind: 'server', name: 'b' })
    expect(await sessionFor([], 's3', () => Promise.reject(new Error('404')))).toBeNull()
  })
})

describe('a question kept to show again', () => {
  it("that refuses an answer has it refused, its steps ended, whatever its track's waiting says, and says where it can be answered", async () => {
    const pg = await page()
    pg.s.hold()
    const q = question()
    const out = pg.acts.act(q.it, q.a)
    await settle()
    const e = pg.entry(q.it.ref)
    // The kept question's track (questionSteps): set aside, recorded and validated waiting.
    const kept = steps('done', 'waiting', 'waiting', 'waiting').map((s) => (s.step === 'available' ? s : { ...s, words: "set aside: the session keeps it to show again when the agent's turn is done or the held turn ends" }))
    pg.srv.tracks.set('q1', kept)
    pg.s.reply.fail(new ReplyError('question_answered', 'this question was already answered: nothing was sent'))
    expect((await out)?.how).toBe('refused')
    expect(stateOf(pg.track(e))).toEqual(['done', 'refused', 'refused', 'not_applicable'])
    expect(pg.track(e)![1]!.words).toContain('this question was already answered')
    expect(stepsFinal(pg.track(e))).toBe(true)
    const words = pg.feedback()[q.it.ref]!.words
    expect(words).toContain('This question was already answered: nothing was sent. The question still waits (set aside')
    expect(words).toContain("the session's terminal")
  })
})

describe("an action's feedback", () => {
  it("is its item's alone: B opened while A's send is out shows nothing of it, then or once A is refused", async () => {
    const pg = await page()
    pg.s.hold()
    const a = question('question:s1/qa')
    const outA = pg.acts.act(a.it, a.a)
    await settle()
    const sentA = pg.s.reply
    expect(pg.acts.pending(a.it.ref)).toBe(true)
    // A second press on A while it is out does nothing.
    expect(await pg.acts.act(a.it, a.a)).toBeNull()
    // B, opened meanwhile, is free: nothing of A's on it.
    const b = plan
    expect(pg.acts.pending(b.it.ref)).toBe(false)
    expect(pg.feedback()[b.it.ref]).toBeUndefined()
    sentA.fail(new ReplyError('question_answered', 'this question was already answered by Nate: nothing was sent'))
    await outA
    expect(pg.feedback()[b.it.ref]).toBeUndefined()
    expect(pg.feedback()[a.it.ref]!.words).toContain('answered by Nate')
  })

  it("is the item's latest action's: an earlier action's late outcome never writes over the one taken after it", async () => {
    const pg = await page()
    pg.s.hold()
    const out1 = pg.acts.comment(task.it, 'DEC-1', 'first')
    await settle()
    const first = pg.s.reply
    first.fail(new CommandRefused('closed ticket', { status: 'failed', refusal: 'closed' } as never))
    await out1
    expect(pg.feedback()[task.it.ref]!.words).toContain('Not commented')
    // The command after it is out, and the feedback is the command's: the comment's words went with it.
    pg.s.hold()
    const out2 = pg.acts.act(task.it, task.a)
    await settle()
    expect(pg.feedback()[task.it.ref]).toMatchObject({ busy: 'Retry', words: '' })
    pg.s.reply.done(undefined)
    await out2
    expect(pg.feedback()[task.it.ref]).toMatchObject({ busy: '', words: '' })
  })
})
