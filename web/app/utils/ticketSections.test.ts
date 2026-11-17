import { describe, expect, it } from 'vitest'
import { summaryStatusLine } from './status'
import { ticketSections, ticketWaits, type TicketSectionsInput, type WaitItem } from './ticketSections'

// A ticket's strip of sections (UX1 PR-16, utils/ticketSections.ts): the ten names in the page's order, each with its state where it has
// one, and the ticket's items of the one list, filtered by its key and its runs, never counted again.

const close = { closed: false, ready: false, items: [{ met: true }, { met: false }] }
function input(p: Partial<TicketSectionsInput> = {}): TicketSectionsInput {
  return { plan: { state: 'approved' }, waits: [], runs: 0, runDone: '', decisions: [], close, comments: [], ...p }
}
const words = (i: TicketSectionsInput, id: string) => ticketSections(i).find((s) => s.id === id)!

describe("a ticket's strip of sections", () => {
  it('names the ten sections in the page’s order, links, relations and attachments not among them', () => {
    expect(ticketSections(input()).map((s) => s.label)).toEqual(['Description', 'Acceptance', 'Plan', 'Runs', 'Captures', 'Decisions', 'To close', 'People', 'Comments', 'History'])
    expect(ticketSections(input()).map((s) => s.id)).toEqual(['description', 'acceptance', 'plan', 'runs', 'captures', 'decisions', 'close', 'people', 'comments', 'history'])
  })

  it("says the featured revision's state, or none, as the Plan section's badge does", () => {
    expect(words(input(), 'plan').words).toBe('approved')
    expect(words(input({ plan: { state: 'approved', stale: { words: 'the title changed', remedy: 'approve' } } }), 'plan').words).toBe('approval stale')
    expect(words(input({ plan: { state: 'proposed' } }), 'plan').words).toBe('proposed')
    expect(words(input({ plan: null }), 'plan').words).toBe('none')
    expect(words(input(), 'plan').tone).toBe('neutral')
  })

  it('counts only open decision records', () => {
    const d = words(input({ decisions: [{ state: 'open' }, { state: 'answered' }, { state: 'open' }, { state: 'applied' }] }), 'decisions')
    expect([d.words, d.tone, d.count]).toEqual(['2 open', 'warning', 2])
    expect([words(input({ decisions: [{ state: 'applied' }] }), 'decisions').words, words(input(), 'decisions').tone]).toEqual(['none open', 'neutral'])
  })

  it('says the close card as closed, ready or how many of its requirements are met', () => {
    expect(words(input(), 'close').words).toBe('1 of 2 met')
    expect(words(input({ close: { ...close, ready: true } }), 'close').words).toBe('ready')
    expect(words(input({ close: { ...close, closed: true } }), 'close').words).toBe('closed')
  })

  it("names GitHub first, by GitHub's state alone, only for a ticket with a linked item", () => {
    expect(ticketSections(input()).map((s) => s.id)).not.toContain('github')
    expect(ticketSections(input({ github: null })).map((s) => s.id)).not.toContain('github')
    const linked = ticketSections(input({ github: { state: 'open' } }))
    expect(linked.map((s) => s.id)).toEqual(['github', 'description', 'acceptance', 'plan', 'runs', 'captures', 'decisions', 'close', 'people', 'comments', 'history'])
    expect([linked[0]!.label, linked[0]!.words, linked[0]!.tone]).toEqual(['GitHub', 'open', 'neutral'])
    expect(words(input({ github: { state: 'closed', stateReason: 'completed' } }), 'github').words).toBe('closed')
    expect(words(input({ github: { state: 'closed', merged: true } }), 'github').words).toBe('merged')
    // A closed item is GitHub's word, never a warning: the ticket's status is execution's.
    expect(words(input({ github: { state: 'closed' } }), 'github').tone).toBe('neutral')
  })

  it('says the publications that need a person after GitHub\'s state, in neutral ink: they are not in the one list, so never amber', () => {
    const w = words(input({ github: { state: 'open' }, publications: '2 publications need you' }), 'github')
    expect([w.words, w.tone]).toEqual(['open · 2 publications need you', 'neutral'])
    expect(words(input({ github: { state: 'open' }, publications: '' }), 'github').words).toBe('open')
  })

  it('counts the live comments, and says "+" when an earlier page exists', () => {
    const three = [{ n: 1 }, { n: 2 }, { n: 3, deletedAt: '2026-11-16T00:00:00Z' }, { n: 4 }]
    expect(words(input({ comments: three }), 'comments').words).toBe('3')
    const page = (first: number) => Array.from({ length: 50 }, (_, i) => ({ n: first + i }))
    expect(words(input({ comments: page(1) }), 'comments').words).toBe('50')
    expect(words(input({ comments: page(4) }), 'comments').words).toBe('50+')
    expect(words(input({ comments: [] }), 'comments').words).toBe('0')
  })
})

describe("the strip's run part from a summary alone (summaryStatusLine's done part)", () => {
  const summary = (tasks: Record<string, number>, over: Record<string, unknown> = {}) => ({ state: 'running' as const, tasks, ...over })
  it('counts the succeeded tasks of all, says nothing of a running or checking task, and keeps the count once the run ended', () => {
    expect(summaryStatusLine(summary({ succeeded: 2, running: 1, ready: 3 }), []).done).toBe('2 of 6 done')
    expect(summaryStatusLine(summary({ verifying: 1, blocked: 1 }), []).done).toBe('0 of 2 done')
    expect(summaryStatusLine(summary({}), []).done).toBe('')
    expect(summaryStatusLine(summary({ succeeded: 6 }, { outcome: 'succeeded', state: 'finished' }), []).done).toBe('6 of 6 done')
    const runs = words(input({ runs: 1, runDone: summaryStatusLine(summary({ succeeded: 2, running: 1, ready: 3 }), []).done }), 'runs')
    expect(runs.words).toBe('2 of 6 done')
    expect(runs.words).not.toMatch(/working|running|checking/)
  })
})

describe("the ticket's items of the one list", () => {
  const ticket = { key: 'CND-7', runs: ['run-a', 'run-b'] }
  const list: WaitItem[] = [
    { kind: 'question', ref: 'question:s1/q1', run: 'run-a' },
    { kind: 'decision', ref: 'decision:dr-3', run: 'run-b' },
    { kind: 'plan', ref: 'plan:CND-7/2' },
    // Another ticket's, one of each kind.
    { kind: 'question', ref: 'question:s9/q1', run: 'run-z' },
    { kind: 'decision', ref: 'decision:dr-9', run: 'run-z' },
    { kind: 'task', ref: 'task:run-z/build', run: 'run-z' },
    { kind: 'plan', ref: 'plan:CND-70/1' },
    // A loose question of no run.
    { kind: 'question', ref: 'question:s5/q2' },
  ]

  it("are a plan item of its key and the items whose run is one of its runs, matched by run and key, never by kind", () => {
    expect(ticketWaits(list, ticket).map((i) => i.ref)).toEqual(['question:s1/q1', 'decision:dr-3', 'plan:CND-7/2'])
  })

  it('make Runs say how many need you and Plan which revision waits, each a warning; with nothing waiting both are neutral', () => {
    const waits = ticketWaits(list, ticket)
    const runs = words(input({ waits, runs: 2, runDone: '2 of 6 done' }), 'runs')
    expect([runs.words, runs.tone, runs.count]).toEqual(['2 of 6 done · 2 need you', 'warning', 2])
    expect(words(input({ waits }), 'plan')).toMatchObject({ words: 'r2 waits for your approval', tone: 'warning' })
    expect(words(input({ waits: [waits[0]!], runs: 1, runDone: '0 of 2 done' }), 'runs').words).toBe('0 of 2 done · 1 needs you')
    const calm = words(input({ runs: 2, runDone: '2 of 6 done' }), 'runs')
    expect([calm.words, calm.tone]).toEqual(['2 of 6 done', 'neutral'])
    expect(words(input(), 'plan').tone).toBe('neutral')
    expect(words(input(), 'runs').words).toBe('none')
  })
})
