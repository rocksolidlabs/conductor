import { describe, expect, it } from 'vitest'
import type { BrakemanRequest, BrakemanRow } from '../utils/brakeman'
import { ABOUT_LIMIT, AboutReads, type AboutList } from './useBrakemanAbout'

// The reads of what Brakeman said about one subject (issue 993, F8): once per subject, again on an event that concerns them, one
// read in flight; an older server's refusal hides the panels and pages nothing.

const RUN = 'run:fix-login-1a2b3c4d'

function row(id: string, o: Partial<BrakemanRow> = {}): BrakemanRow {
  return { id, opId: `op-${id}`, kind: 'explain', createdAt: '2026-11-17T05:00:00Z', state: 'briefed', agent: {} as BrakemanRequest['agent'], config: {} as BrakemanRequest['config'], reads: 0, ...o }
}

/** A list that answers what the test sets, each read held until the test lets it go. */
function fakeList() {
  const asked: Array<{ about: string; limit: number }> = []
  const held: Array<() => void> = []
  let answer: () => Promise<{ requests: BrakemanRow[]; next: string; filtered: boolean }> = async () => ({ requests: [], next: '', filtered: true })
  const list: AboutList = (q) => {
    asked.push(q)
    return new Promise((done, fail) => held.push(() => answer().then(done, fail)))
  }
  return {
    list,
    asked,
    answers: (f: typeof answer) => (answer = f),
    held: () => held.length,
    release: async () => {
      while (held.length) held.shift()!()
      for (let i = 0; i < 5; i++) await new Promise((r) => setTimeout(r, 0))
    },
  }
}

describe('the reads about a subject', () => {
  it('reads a subject once, for the newest fifty, and again for an event of one of its rows or a brief kept', async () => {
    const f = fakeList()
    f.answers(async () => ({ requests: [row('br-9002'), row('br-9001')], next: '', filtered: true }))
    const r = new AboutReads(f.list)
    r.set(RUN)
    r.set(RUN)
    expect(f.asked).toEqual([{ about: RUN, limit: ABOUT_LIMIT }])
    await f.release()
    expect(r.rows.map((x) => x.id)).toEqual(['br-9002', 'br-9001'])
    expect(r.loaded).toBe(true)
    // Another request, not briefed: nothing of this subject's.
    r.event({ id: 'br-9010', state: 'running' })
    expect(f.held()).toBe(0)
    // One of its rows moved; a brief kept, which may cite the subject.
    r.event({ id: 'br-9001', state: 'briefed' })
    expect(f.asked).toHaveLength(2)
    await f.release()
    r.event({ id: 'br-9011', state: 'briefed' })
    expect(f.asked).toHaveLength(3)
    await f.release()
    // The stream started again: what it missed is read.
    r.event({ id: '', state: '' })
    expect(f.asked).toHaveLength(4)
  })

  it('keeps one read in flight, and the events meanwhile are one read after it', async () => {
    const f = fakeList()
    const r = new AboutReads(f.list)
    r.set(RUN)
    for (let i = 0; i < 5; i++) r.event({ id: `br-${9100 + i}`, state: 'briefed' })
    expect(f.asked).toHaveLength(1)
    await f.release()
    expect(f.asked).toHaveLength(2)
    await f.release()
    expect(f.asked).toHaveLength(2)
  })

  it("drops an answer for the subject before, and holds the next subject's rows alone", async () => {
    const f = fakeList()
    f.answers(async () => ({ requests: [row('br-9200')], next: '', filtered: true }))
    const r = new AboutReads(f.list)
    r.set(RUN)
    r.set('ticket:CND-41')
    f.answers(async () => ({ requests: [row('br-9201')], next: '', filtered: true }))
    await f.release()
    expect(r.subject).toBe('ticket:CND-41')
    expect(r.rows.map((x) => x.id)).toEqual(['br-9201'])
  })

  it("is refused by an older server's unfiltered answer, and by a failed read, and then reads nothing more", async () => {
    const f = fakeList()
    f.answers(async () => ({ requests: [row('br-1')], next: 'br-0', filtered: false }))
    const r = new AboutReads(f.list)
    r.set(RUN)
    await f.release()
    expect(r.refused).toBe(true)
    expect(r.rows).toEqual([])
    r.event({ id: 'br-2', state: 'briefed' })
    expect(f.asked).toHaveLength(1)

    const g = fakeList()
    g.answers(async () => {
      throw new Error('403')
    })
    const s = new AboutReads(g.list)
    s.set(RUN)
    await g.release()
    expect(s.refused).toBe(true)
  })

  it('shows a request the page asked at once, and keeps it while the read before its keeping does not hold it', async () => {
    const f = fakeList()
    f.answers(async () => ({ requests: [row('br-9300')], next: '', filtered: true }))
    const r = new AboutReads(f.list)
    r.set(RUN)
    r.add(row('br-9301', { state: 'queued', hasBrief: false }))
    await f.release()
    expect(r.rows.map((x) => x.id)).toEqual(['br-9301', 'br-9300'])
    f.answers(async () => ({ requests: [row('br-9301'), row('br-9300')], next: '', filtered: true }))
    r.event({ id: 'br-9301', state: 'briefed' })
    await f.release()
    expect(r.rows.map((x) => x.id)).toEqual(['br-9301', 'br-9300'])
  })

  it('reads nothing for no subject or one that is no ref', () => {
    const f = fakeList()
    const r = new AboutReads(f.list)
    r.set('')
    r.set('not a ref')
    expect(f.asked).toEqual([])
  })
})
