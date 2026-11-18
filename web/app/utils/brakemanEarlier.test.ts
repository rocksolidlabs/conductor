import { describe, expect, it } from 'vitest'
import type { BrakemanListQuery } from '~/composables/useBrakeman'
import type { BrakemanRow } from './brakeman'
import { FilteredList, filteredRows } from './brakemanEarlier'

// Earlier's filtered list (issue 993, F7): a filter or a search changed while a read is out is read fresh after it, its cursor kept
// and "reading" said meanwhile (never an empty list that reads as no match); and only requests that are done are its rows.

function req(id: string, state: string, over: Partial<BrakemanRow> = {}): BrakemanRow {
  return { id, opId: `op-${id}`, kind: 'briefing', createdAt: '2026-11-17T09:00:00Z', state, agent: {}, config: {} as never, reads: 0, ...over } as BrakemanRow
}
const settle = () => new Promise((r) => setTimeout(r, 0))

/** A list read whose every call waits for the test to answer it. */
function heldReads() {
  const calls: Array<{ q: BrakemanListQuery; answer: (v: { requests: BrakemanRow[]; next: string; filtered: boolean }) => void }> = []
  const list = (q: BrakemanListQuery) => new Promise<{ requests: BrakemanRow[]; next: string; filtered: boolean }>((answer) => calls.push({ q, answer }))
  return { calls, list }
}

describe("Earlier's filtered list", () => {
  it('reads a query changed while a read is out fresh after it: its first page, its cursor, and reading meanwhile', async () => {
    const h = heldReads()
    const l = new FilteredList(h.list, () => {}, () => {})
    l.set({ has: 'decision' })
    expect(l.reading).toBe(true)
    // Briefings pressed before With a decision is answered.
    l.set({ kind: ['briefing', 'wake'] })
    expect(h.calls).toHaveLength(1)
    // The first answer is the query before's: dropped, and the new query read at once, still reading (no "No brief matches.").
    h.calls[0]!.answer({ requests: [req('br-9', 'briefed')], next: '', filtered: true })
    await settle()
    expect(l.ids).toEqual([])
    expect(l.reading).toBe(true)
    expect(h.calls.map((c) => c.q)).toEqual([{ has: 'decision' }, { kind: ['briefing', 'wake'] }])
    h.calls[1]!.answer({ requests: [req('br-8', 'briefed'), req('br-7', 'briefed')], next: 'br-7', filtered: true })
    await settle()
    expect([l.ids, l.next, l.reading]).toEqual([['br-8', 'br-7'], 'br-7', false])
    // Older requests reads on from its cursor.
    const more = l.more()
    expect(h.calls[2]!.q).toEqual({ kind: ['briefing', 'wake'], after: 'br-7' })
    h.calls[2]!.answer({ requests: [req('br-6', 'briefed')], next: '', filtered: true })
    await more
    expect([l.ids, l.next]).toEqual([['br-8', 'br-7', 'br-6'], ''])
  })

  it('reads its first page again on an event, the rows shown keeping their place and its cursor its own', async () => {
    const h = heldReads()
    const l = new FilteredList(h.list, () => {}, () => {})
    l.set({ has: 'draft' })
    h.calls[0]!.answer({ requests: [req('br-5', 'briefed')], next: 'br-5', filtered: true })
    await settle()
    l.refresh()
    expect(l.reading).toBe(false)
    h.calls[1]!.answer({ requests: [req('br-6', 'briefed'), req('br-5', 'briefed')], next: 'br-4', filtered: true })
    await settle()
    expect([l.ids, l.next]).toEqual([['br-6', 'br-5'], 'br-5'])
  })

  it("tells the page when the server takes no filter, and says a read's failure", async () => {
    const h = heldReads()
    let older = 0
    const l = new FilteredList(h.list, () => {}, () => older++)
    l.set({ q: 'cache' })
    h.calls[0]!.answer({ requests: [], next: '', filtered: false })
    await settle()
    expect(older).toBe(1)
    const failing = new FilteredList(() => Promise.reject(new Error('broken')), () => {}, () => {})
    failing.set({ q: 'cache' })
    await settle()
    expect([failing.failed, failing.reading]).toEqual(['broken', false])
  })

  it("draws the requests done alone: a turn queued or under way that a filter matches is In progress's", () => {
    const store = [req('br-4', 'running'), req('br-3', 'queued'), req('br-2', 'briefed', { hasBrief: true }), req('br-1', 'timed_out')]
    expect(filteredRows(['br-4', 'br-3', 'br-2', 'br-1', 'br-0'], store).map((r) => r.id)).toEqual(['br-2', 'br-1'])
    // Once the store has it closed, it is drawn where the list put it.
    expect(filteredRows(['br-4', 'br-2'], [{ ...store[0]!, state: 'briefed', hasBrief: true }, store[2]!]).map((r) => r.id)).toEqual(['br-4', 'br-2'])
  })
})
