import { describe, expect, it } from 'vitest'
import {
  addRecent,
  barCount,
  firstLandingInTab,
  inboxOrder,
  keptRecent,
  movedOnWords,
  notificationTarget,
  phoneLanding,
  phoneOffer,
  planIdOf,
  readRecentActions,
  reconcileDelivery,
  RECENT_KEY,
  RECENT_MAX,
  RECENT_MS,
  refParts,
  showingWords,
  stateWords,
  STEP_TITLES,
  stepsFinal,
  ticketOf,
  itemOf,
  type InboxItem,
  withOutcome,
  writeRecentActions,
  mintRequestId,
  type DeliveryStep,
  type RecentEntry,
  type StepState,
} from './inbox'

// The decision page's pure parts (docs/plans/decision-inbox.md, PR-2).

const steps = (...states: StepState[]): DeliveryStep[] =>
  (['available', 'recorded', 'validated', 'resumed'] as const).map((step, i) => ({ step, state: states[i] ?? 'unknown', words: `${step} ${states[i]}` }))
const stateOf = (s: DeliveryStep[]) => s.map((x) => x.state)

/** A storage in memory, or one that throws on every access. */
function memory(): Storage {
  const m = new Map<string, string>()
  return { getItem: (k) => m.get(k) ?? null, setItem: (k, v) => void m.set(k, v), removeItem: (k) => void m.delete(k), clear: () => m.clear(), key: () => null, length: 0 }
}
const throwing = { getItem: () => { throw new Error('denied') }, setItem: () => { throw new Error('denied') } }

describe('the order', () => {
  it('lists the oldest first, by since then ref, the server order', () => {
    const items = [
      { ref: 'task:r/b', since: '2026-11-06T10:00:02Z' },
      { ref: 'question:s/q2', since: '2026-11-06T10:00:01Z' },
      { ref: 'question:s/q1', since: '2026-11-06T10:00:01Z' },
      { ref: 'plan:CND-1/1', since: '2026-11-06T09:59:00Z' },
    ]
    expect(inboxOrder(items).map((i) => i.ref)).toEqual(['plan:CND-1/1', 'question:s/q1', 'question:s/q2', 'task:r/b'])
  })
  it('says how much of the list it shows when it holds fewer than there are', () => {
    expect(showingWords(150, 212)).toBe('Showing 150 of 212')
    expect(showingWords(3, 3)).toBe('')
  })
})

describe('the steps in words', () => {
  it('names the four steps apart and says each state', () => {
    expect(Object.values(STEP_TITLES)).toEqual(['Request available', 'Response recorded', 'Received and validated by the execution owner', 'Work resumed'])
    expect(['done', 'waiting', 'refused', 'not_applicable', 'unknown'].map(stateWords)).toEqual(['Done', 'Waiting', 'Refused', 'Does not apply', 'Unknown'])
  })
  it("says a stale refusal's words with what the item is now and that nothing was done", () => {
    expect(movedOnWords('task', null)).toBe('The run moved on. Nothing was done.')
    expect(movedOnWords('plan', { now: 'revision 2 is approved', title: '' })).toBe('The revision or its ticket moved on: revision 2 is approved. Nothing was done.')
  })
})

describe('reconcileDelivery', () => {
  it('never moves a step back, whatever order the replies come back in', () => {
    const early = steps('done', 'done', 'waiting', 'waiting')
    const late = steps('done', 'done', 'done', 'done')
    // In order.
    expect(stateOf(reconcileDelivery(reconcileDelivery(undefined, early), late))).toEqual(['done', 'done', 'done', 'done'])
    // The later read's reply first, the earlier's after it: the done steps stay.
    expect(stateOf(reconcileDelivery(reconcileDelivery(undefined, late), early))).toEqual(['done', 'done', 'done', 'done'])
  })
  it('keeps a done over a later unknown (a session that forgot) and a refused over a later waiting', () => {
    const seen = reconcileDelivery(undefined, steps('done', 'done', 'done', 'done'))
    expect(stateOf(reconcileDelivery(seen, steps('done', 'unknown', 'unknown', 'unknown')))).toEqual(['done', 'done', 'done', 'done'])
    const refused = reconcileDelivery(undefined, steps('done', 'done', 'refused', 'not_applicable'))
    expect(stateOf(reconcileDelivery(refused, steps('done', 'done', 'waiting', 'waiting')))).toEqual(['done', 'done', 'refused', 'waiting'])
  })
  it("fills a hosted question's unknown recorded and validated steps with the reply's own word", () => {
    const hosted = steps('done', 'unknown', 'unknown', 'waiting')
    const took = reconcileDelivery(undefined, hosted, 'accepted')
    expect(stateOf(took)).toEqual(['done', 'done', 'done', 'waiting'])
    expect(took[1]!.words).toContain('reply')
    const refused = reconcileDelivery(undefined, hosted, 'refused: Already answered')
    expect(stateOf(refused)).toEqual(['done', 'refused', 'refused', 'waiting'])
    expect(refused[2]!.words).toBe('Already answered')
    // An older owner's silence fills nothing.
    expect(stateOf(reconcileDelivery(undefined, hosted, 'unconfirmed'))).toEqual(['done', 'unknown', 'unknown', 'waiting'])
  })
})

describe('stepsFinal', () => {
  it('is final once resumed is done or does not apply, or validated is refused', () => {
    expect(stepsFinal(steps('done', 'done', 'done', 'done'))).toBe(true)
    expect(stepsFinal(steps('done', 'done', 'done', 'not_applicable'))).toBe(true)
    expect(stepsFinal(steps('done', 'done', 'refused', 'not_applicable'))).toBe(true)
    expect(stepsFinal(undefined)).toBe(false)
  })
  it('keeps an accepted command tracked, and an uncertain one once its resumed step is read from the task', () => {
    // accepted: validated waiting, resumed waiting: its settlement follows.
    expect(stepsFinal(steps('done', 'done', 'waiting', 'waiting'))).toBe(false)
    // uncertain (validated unknown): not final until the task says what it did; then final either way.
    expect(stepsFinal(steps('done', 'done', 'unknown', 'waiting'))).toBe(false)
    expect(stepsFinal(steps('done', 'done', 'unknown', 'done'))).toBe(true)
    expect(stepsFinal(steps('done', 'done', 'unknown', 'not_applicable'))).toBe(true)
  })
})

describe("this browser's recent actions", () => {
  const now = Date.parse('2026-11-06T12:00:00Z')
  const entry = (i: number, ago = 0): RecentEntry => ({ ref: `task:r/t${i}`, receipt: `c${i}`, at: now - ago - i })
  it('keeps the newest 50, none older than a day', () => {
    let list: RecentEntry[] = []
    for (let i = 0; i < 60; i++) list = addRecent(list, entry(i), now)
    expect(list).toHaveLength(RECENT_MAX)
    expect(list[0]!.receipt).toBe('c0')
    expect(keptRecent([entry(1, RECENT_MS + 1), entry(2)], now).map((e) => e.receipt)).toEqual(['c2'])
  })
  it('reads storage that throws, holds nothing or holds something else as empty, and writes through a storage that throws', () => {
    expect(readRecentActions(throwing, now)).toEqual([])
    expect(readRecentActions(null, now)).toEqual([])
    const m = memory()
    m.setItem(RECENT_KEY, '{"not":"a list"}')
    expect(readRecentActions(m, now)).toEqual([])
    m.setItem(RECENT_KEY, 'not json')
    expect(readRecentActions(m, now)).toEqual([])
    expect(() => writeRecentActions(throwing, [entry(1)])).not.toThrow()
    writeRecentActions(m, [entry(1), { bad: true } as unknown as RecentEntry])
    expect(readRecentActions(m, now).map((e) => e.receipt)).toEqual(['c1'])
  })
  it("writes the reply's outcome into its entry, by its receipt and attempt", () => {
    const list = [{ ...entry(1), attempt: 'a1' }, { ...entry(1), attempt: 'a2' }]
    const out = withOutcome(list, 'c1', 'a2', 'accepted')
    expect(out.map((e) => e.outcome)).toEqual([undefined, 'accepted'])
  })
})

describe("the phone's paths", () => {
  it('lands a phone on the decision page when an item waits, and nowhere at lg or with none', () => {
    expect(phoneLanding(390, 2)).toBe('/decisions')
    expect(phoneLanding(390, 0)).toBeNull()
    expect(phoneLanding(1024, 2)).toBeNull()
  })
  it('judges the landing once per tab, and each load when the storage throws', () => {
    const m = memory()
    expect(firstLandingInTab(m)).toBe(true)
    expect(firstLandingInTab(m)).toBe(false)
    expect(firstLandingInTab(throwing)).toBe(true)
  })
  it("opens a tapped question's item below lg and its session at lg", () => {
    expect(notificationTarget(390, 'sess1', 'q1')).toBe(`/decisions?item=${encodeURIComponent('question:sess1/q1')}`)
    expect(notificationTarget(1440, 'sess1', 'q1')).toBe('/sessions/sess1')
    expect(notificationTarget(390, 'sess1')).toBe('/sessions/sess1')
  })
  it('counts every open item for the bar, none before the first read', () => {
    expect(barCount({ total: 212 })).toBe(212)
    expect(barCount(null)).toBe(0)
  })
  it("offers a LAN origin's page as a QR code of the address alone, and on a loopback origin the words why not", () => {
    const lan = phoneOffer('http://192.168.1.20:8080')
    expect(lan).toEqual({ kind: 'qr', url: 'http://192.168.1.20:8080/decisions' })
    // Whatever the location carries, the code holds the origin and the page's path: no token, query or fragment.
    const odd = phoneOffer('https://box.lan:8443/?token=secret#t=x')
    expect(odd).toEqual({ kind: 'qr', url: 'https://box.lan:8443/decisions' })
    expect(JSON.stringify(odd)).not.toContain('secret')
    for (const o of ['http://127.0.0.1:8080', 'http://localhost:3000', 'http://[::1]:8080']) expect(phoneOffer(o).kind).toBe('loopback')
  })
})

describe('acting', () => {
  it('mints a fresh request id an answer may carry', () => {
    const a = mintRequestId()
    expect(a).toMatch(/^d[0-9a-f]{16}$/)
    expect(mintRequestId()).not.toBe(a)
  })
  it("reads a plan action's revision id from its route, a ref's parts, and the ticket a run was launched from", () => {
    expect(planIdOf('POST /api/dispatch/plans/pl-12/approve')).toBe('pl-12')
    expect(planIdOf('POST /api/runs/r/tasks/t/retry')).toBe('')
    expect(refParts('question:s1/q1')).toEqual({ kind: 'question', a: 's1', b: 'q1' })
    expect(ticketOf({ kind: 'task', evidence: [{ ref: 'run:r', label: '' }, { ref: 'ticket:CND-12', label: '' }] })).toBe('CND-12')
    expect(ticketOf({ kind: 'plan', evidence: [{ ref: 'ticket:CND-12', label: '' }] })).toBe('')
  })
})

describe('an item as the page draws it', () => {
  it('reads the lists the server leaves null when empty as empty lists', () => {
    const raw = { ref: 'question:s/q', blocking: { words: 'waits', refs: null }, actions: null } as unknown as InboxItem
    const it = itemOf(raw)
    expect(it.blocking.refs).toEqual([])
    expect(it.actions).toEqual([])
    expect(it.evidence).toEqual([])
  })
})
