import { readFileSync } from 'node:fs'
import { describe, expect, it, vi } from 'vitest'
import type { KnownPerson } from '~/composables/usePeople'
import type { TicketSummary, TicketsPage } from '~/composables/useDispatch'
import type { PaletteGroup } from './palette'
import type { PaletteItem } from '~/composables/usePalette'
import type { PersonWork } from '~/composables/usePeople'
import { SerialReads } from '~/composables/useDecisions'
import { boundedRead, capPeople, freshTicketLists, keepsTicketLists, noReplyWords, oneReadAtATime, ownedHow, PEOPLE_IDLE, PEOPLE_MATCHED, PERSON_READ_MS, personDetail, personFromSegment, personLists, personPath, personRows, personSegment, plainPersonName, readTicketsPage, ticketsPageQuery, TICKETS_PAGE, type PersonTicketList } from './people'

const nate: KnownPerson = { name: 'nate', dispatch: true, crews: [{ id: 'api', name: 'api', responsibilities: ['architecture'] }] }
const ana: KnownPerson = {
  name: 'ana',
  dispatch: false,
  crews: [
    { id: 'api', name: 'api', responsibilities: ['acceptance', 'product'] },
    { id: 'web', name: 'web', responsibilities: [] },
  ],
  crewsMore: true,
}
const zed: KnownPerson = { name: 'zed', dispatch: true, crews: [] }

describe('personRows', () => {
  it('words a row by what the person holds in each crew', () => {
    const rows = personRows([nate], () => {})
    expect(rows).toHaveLength(1)
    expect(rows[0]).toMatchObject({ key: 'person:nate', label: 'nate', detail: 'architecture owner in api', href: '/people/nate' })
    expect(personDetail(ana)).toBe('product owner, acceptance owner in api, in web, and more crews')
    expect(personDetail(zed)).toBe("in Dispatch's people")
    expect(personRows([{ name: 'kai', dispatch: false, crews: [] }], () => {})[0]!.detail).toBeUndefined()
  })

  it("keys a row by the person's name and opens their page", () => {
    const open = vi.fn()
    const [row] = personRows([{ ...nate, name: 'Ana María/2' }], open)
    expect(row!.key).toBe('person:Ana María/2')
    expect(row!.href).toBe('/people/~QW5hIE1hcsOtYS8y')
    expect(personPath('Ana María/2')).toBe(row!.href)
    row!.run()
    expect(open).toHaveBeenCalledWith('/people/~QW5hIE1hcsOtYS8y')
  })

  it('orders the rows by name, as the server pages them', () => {
    expect(personRows([zed, nate, ana, { ...zed, name: 'Zoe' }], () => {}).map((r) => r.label)).toEqual(['Zoe', 'ana', 'nate', 'zed'])
  })
})

describe('ownedHow', () => {
  it('says the fallback to the acceptance owner, and nothing for an owner the task names', () => {
    expect(ownedHow({ ownerFrom: 'acceptance' })).toBe('as the acceptance owner')
    expect(ownedHow({ ownerFrom: 'task' })).toBe('')
  })
})

/** The cases the server's rule is tested against too (internal/api/people_path_test.go, internal/web's embed_test.go). */
const segmentCases: Array<{ name: string; segment: string }> = JSON.parse(
  readFileSync(new URL('../../../internal/api/testdata/person-segments.json', import.meta.url), 'utf8'),
).cases

/** The people rules (crew.checkPersonName, Dispatch's person.name): 1 to 60 characters, no control character. */
const allowed = (name: string) => name.length > 0 && [...name].length <= 60 && !/\p{Cc}/u.test(name)

/** The characters names are drawn from: every kind the rule treats apart, as the server's test draws them. */
const nameChars = [...'aZ09-_. ~/\\%?#;+\'&=:@!$,()[]*', '\u00e9', '\u00df', '\u65e5', '\u{1f600}', '\u00a0', '\u2215', '\uff0f', '\u202e', '\ufeff']

/** A seeded draw (mulberry32), so a failing name is found again. */
function draw(seed: number): () => number {
  let a = seed
  return () => {
    a = (a + 0x6d2b79f5) | 0
    let t = Math.imul(a ^ (a >>> 15), 1 | a)
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

/** A name the people rules allow, now and then with "..", "%2F" or "%2E" spelled in it. */
function randomName(r: () => number): string {
  const n = 1 + Math.floor(r() * 60)
  let out = ''
  for (let count = 0; count < n; ) {
    const k = Math.floor(r() * 12)
    if (k === 0 && count + 2 <= n) {
      out += '..'
      count += 2
    } else if (k === 1 && count + 3 <= n) {
      out += ['%2F', '%2E', '%25'][Math.floor(r() * 3)]
      count += 3
    } else {
      out += nameChars[Math.floor(r() * nameChars.length)]
      count++
    }
  }
  return out
}

/**
 * The rule's promises for one name, what breaks them in words ('' when none does): one segment no URL, decoding or router changes, read
 * back as the name, the testdata's when given. Checked without an expect per name, so 20,000 names stay quick on a loaded machine.
 */
function segmentProblem(name: string, want?: string): string {
  const segment = personSegment(name)
  if (want !== undefined && segment !== want) return `segment ${segment}, want ${want}`
  if (!/^[A-Za-z0-9\-_.~%]+$/.test(segment)) return `segment ${segment} holds more than letters, digits, - _ . ~ and escapes`
  const text = decodeURIComponent(segment)
  if (/[/\\%?#;]/.test(text) || text === '.' || text === '..') return `segment ${segment} unescaped is ${text}, which a path reads as more than a name`
  // The browser keeps the path as written: no dot step resolved, no slash added; the API's path the same.
  for (const base of ['/people/', '/api/people/']) {
    const path = new URL(`${base}${segment}`, 'http://conductor.test/').pathname
    if (path !== `${base}${segment}`) return `${base}${segment} is ${path}`
  }
  // The router hands the page the segment unescaped, which reads back as the name.
  const back = personFromSegment(text)
  if (back !== name) return `segment ${segment} reads back as ${back}`
  if (personPath(name) !== `/people/${segment}`) return `path ${personPath(name)}`
  return ''
}

describe('personSegment', () => {
  it("writes the testdata's names as the server's rule does, each with a segment of its own", () => {
    expect(segmentCases.length).toBeGreaterThan(20)
    for (const c of segmentCases) {
      expect(allowed(c.name), c.name).toBe(true)
      expect(segmentProblem(c.name, c.segment), c.name).toBe('')
    }
    expect(personPath('nate')).toBe('/people/nate')
    expect(personPath('..')).toBe('/people/~Li4')
    expect(personPath('../brand/conductor-favicon.svg')).toBe('/people/~Li4vYnJhbmQvY29uZHVjdG9yLWZhdmljb24uc3Zn')
  })

  it('holds for 20,000 names drawn from the whole class the people rules allow', () => {
    const r = draw(644)
    const problems: string[] = []
    for (let i = 0; i < 20000; i++) {
      const name = randomName(r)
      const problem = allowed(name) ? segmentProblem(name) : 'not a name the people rules allow'
      if (problem) problems.push(`${JSON.stringify(name)}: ${problem}`)
    }
    expect(problems.slice(0, 5)).toEqual([])
  }, 60_000)

  it("reads a segment as a name only in the one form written for it, so no address is another name's", () => {
    for (const text of ['', '~', '~~', '~.', '~..', '~zed', '~emVk', '~bmF0ZQ', '~Lg==', '~Lh', 'a/b', '50% c', '.', '..', ' x', 'x ', '~fn4\n']) {
      const name = personFromSegment(text)
      if (name !== null) expect(decodeURIComponent(personSegment(name)), text).toBe(text)
    }
    expect(personFromSegment('~emVk')).toBeNull()
    expect(personFromSegment('~Li4')).toBe('..')
    expect(personFromSegment('a/b')).toBeNull()
    const r = draw(645)
    const others: string[] = []
    for (let i = 0; i < 20000; i++) {
      for (const text of [randomName(r), `~${randomName(r)}`]) {
        const name = personFromSegment(text)
        if (name !== null && decodeURIComponent(personSegment(name)) !== text) others.push(`${JSON.stringify(text)} reads as ${JSON.stringify(name)}`)
      }
    }
    expect(others.slice(0, 5)).toEqual([])
    expect(plainPersonName('nate.r')).toBe(true)
    expect(plainPersonName('.nate')).toBe(false)
  }, 60_000)
})

describe('personLists', () => {
  const work = { crews: nate.crews, tasks: [{ run: 'r-0000000a', runLabel: 'r', task: 't', state: 'ready', member: 'lead', ownerFrom: 'task' as const }], tickets: { owner: 1, assignee: 0, ownerHref: '', assigneeHref: '' } }

  it('says rows, empty, or not read for each list, every source failing alone', () => {
    expect(personLists(work)).toEqual({ crews: { state: 'rows', unread: '' }, tasks: { state: 'rows', unread: '' }, tickets: { state: 'rows', unread: '' } })
    expect(personLists({ ...work, crews: [], tasks: [], tickets: { ...work.tickets, owner: 0 } })).toEqual({
      crews: { state: 'empty', unread: '' },
      tasks: { state: 'empty', unread: '' },
      tickets: { state: 'empty', unread: '' },
    })
    // A crew not read: the crews read are kept and said to be maybe not all; with none read, the list is not drawn as empty.
    expect(personLists({ ...work, crewsUnavailable: true }).crews).toEqual({ state: 'rows', unread: 'Some saved crews could not be read: they may be in more crews than these.' })
    expect(personLists({ ...work, crews: [], crewsUnavailable: true }).crews).toEqual({ state: 'unread', unread: 'Some saved crews could not be read: a crew that lists them may not be shown.' })
    expect(personLists({ ...work, crewsUnavailable: true }).tasks).toEqual({ state: 'rows', unread: '' })
    // A run's record not read: the tasks alone say so.
    expect(personLists({ ...work, recordsUnavailable: true }).tasks.unread).toContain('they may own more tasks than these')
    expect(personLists({ ...work, tasks: [], recordsUnavailable: true }).tasks).toEqual({ state: 'unread', unread: 'Some records of runs the server no longer holds could not be read: a task they own there may not be shown.' })
    expect(personLists({ ...work, recordsUnavailable: true }).crews).toEqual({ state: 'rows', unread: '' })
    // The records' and the crews' directories listed and not searched: the server read none of their files and says so for each, so a
    // person only they knew has a page whose two lists are not read, never empty, beside the tickets Dispatch read.
    expect(personLists({ crews: [], tasks: [], tickets: { ...work.tickets, owner: 0 }, crewsUnavailable: true, recordsUnavailable: true })).toEqual({
      crews: { state: 'unread', unread: 'Some saved crews could not be read: a crew that lists them may not be shown.' },
      tasks: { state: 'unread', unread: 'Some records of runs the server no longer holds could not be read: a task they own there may not be shown.' },
      tickets: { state: 'empty', unread: '' },
    })
    // Dispatch not read: no tickets, said so; the other lists stay.
    expect(personLists({ ...work, tickets: null, dispatchUnavailable: true })).toEqual({
      crews: { state: 'rows', unread: '' },
      tasks: { state: 'rows', unread: '' },
      tickets: { state: 'unread', unread: 'Dispatch could not be read: the tickets they own or are assigned are not shown.' },
    })
  })
})

describe("the palette's People group", () => {
  const many: KnownPerson[] = Array.from({ length: 200 }, (_, i) => ({ name: `p${String(i).padStart(3, '0')}`, dispatch: true, crews: [] }))

  it('keeps a row by its name only, as the server does, never by its detail', () => {
    // "arch" and "api" are in nate's detail (architecture owner in api), not his name: a reply read for "@" offers no row for them.
    expect(personRows([nate, ana], () => {}, 'arch')).toEqual([])
    expect(personRows([nate, ana], () => {}, 'api')).toEqual([])
    expect(personRows([nate, ana], () => {}, ' NA ').map((r) => r.label)).toEqual(['ana', 'nate'])
    expect(personRows([nate, ana], () => {}, '').map((r) => r.label)).toEqual(['ana', 'nate'])
  })

  it('draws at most a few rows with nothing typed and more once something is, of 200 known', () => {
    const groups: PaletteGroup<PaletteItem>[] = [
      { id: 'known-people', label: 'People', rows: personRows(many, () => {}) },
      { id: 'you', label: 'You', rows: [{ key: 'action:name', label: 'Your name…', run: () => {} }] },
    ]
    const idle = capPeople(groups, '')
    expect(idle[0]!.rows).toHaveLength(PEOPLE_IDLE)
    expect(idle[0]!.label).toBe(`People · ${PEOPLE_IDLE} of 200, type to find the rest`)
    expect(idle[1]!.rows).toHaveLength(1)
    expect(capPeople(groups, 'p')[0]!.rows).toHaveLength(PEOPLE_MATCHED)
  })
})

describe('oneReadAtATime', () => {
  it('reads one query at a time, then only the latest asked, and applies only a reply nothing newer was asked after', async () => {
    const held: Array<() => void> = []
    const asked: string[] = []
    const applied: string[] = []
    const load = oneReadAtATime(
      (q) => {
        asked.push(q)
        return new Promise<string>((done) => held.push(() => done(q)))
      },
      (v) => applied.push(v),
    )
    const first = load('n')
    void load('na')
    void load('nat')
    const last = load('nate')
    expect(asked).toEqual(['n'])
    held.shift()!()
    await new Promise((r) => setTimeout(r, 0))
    // The reply for "n" is dropped: "nate" was asked after it; "na" and "nat" are never sent.
    expect(applied).toEqual([])
    expect(asked).toEqual(['n', 'nate'])
    held.shift()!()
    await Promise.all([first, last])
    expect(applied).toEqual(['nate'])
    // A failed read applies nothing; the next is read.
    const failing = oneReadAtATime(
      (q) => (q === 'x' ? Promise.reject(new Error('down')) : Promise.resolve(q)),
      (v) => applied.push(v),
    )
    await failing('x')
    await failing('y')
    expect(applied).toEqual(['nate', 'y'])
  })
})

describe("a person's ticket lists", () => {
  const ticket = (n: number): TicketSummary => ({ id: `id${n}`, key: `PPL-${n}`, title: `t${n}`, status: 'todo' }) as unknown as TicketSummary
  const all = Array.from({ length: 21 }, (_, i) => ticket(i + 1))
  /** Dispatch's list, paged by an opaque cursor: the index after the page. */
  const dispatch = (query: string): Promise<TicketsPage> => {
    const q = new URLSearchParams(query)
    const from = Number(q.get('cursor') ?? 0)
    const limit = Number(q.get('limit'))
    const tickets = all.slice(from, from + limit)
    return Promise.resolve({ tickets, total: all.length, ...(from + limit < all.length ? { next: String(from + limit) } : {}) })
  }
  const list = (): PersonTicketList => ({ id: 'owner', label: 'Owned', href: '/api/dispatch/tickets?owner=nate', total: 21, tickets: [], read: false, error: '' })

  it("reads the list's own filter a page at a time, by its cursor", () => {
    expect(ticketsPageQuery('/api/dispatch/tickets?owner=nate')).toBe(`owner=nate&limit=${TICKETS_PAGE}`)
    expect(ticketsPageQuery('/api/dispatch/tickets?assignee=person%3Anate', 'c2')).toBe(`assignee=person%3Anate&limit=${TICKETS_PAGE}&cursor=c2`)
  })

  it('reaches the twenty-first ticket with a second read', async () => {
    const l = list()
    await readTicketsPage(l, dispatch)
    expect(l.tickets).toHaveLength(20)
    expect(l.next).toBe('20')
    await readTicketsPage(l, dispatch)
    expect(l.tickets.map((t) => t.key)).toEqual(all.map((t) => t.key))
    expect(l.tickets[20]!.key).toBe('PPL-21')
    expect(l.next).toBeUndefined()
  })

  it('keeps what was read when a page fails, says so for the list alone, and reads the same page again', async () => {
    const l = list()
    await readTicketsPage(l, dispatch)
    const cursors: Array<string | null> = []
    await readTicketsPage(l, (q) => {
      cursors.push(new URLSearchParams(q).get('cursor'))
      return Promise.reject(new Error('503 Service Unavailable'))
    })
    expect(l.tickets).toHaveLength(20)
    expect(l.error).toBe('503 Service Unavailable')
    await readTicketsPage(l, (q) => {
      cursors.push(new URLSearchParams(q).get('cursor'))
      return dispatch(q)
    })
    expect(cursors).toEqual(['20', '20'])
    expect(l.tickets).toHaveLength(21)
    expect(l.error).toBe('')
    // A first page that fails leaves nothing read, and is read again from the start.
    const first = list()
    await readTicketsPage(first, () => Promise.reject(new Error('down')))
    expect(first.read).toBe(false)
    expect(first.error).toBe('down')
    // A reply for a page no longer shown changes nothing.
    const gone = list()
    await readTicketsPage(gone, dispatch, () => false)
    expect(gone.tickets).toEqual([])
  })
})

// A person's page reads their work again on every decision's change, one read at a time (SerialReads, HM1, PR-12): each read is
// bounded and cancellable, so one that never gets its reply is given up, says so, and frees the reads queued after it; a reply that comes
// after it was given up is never used.
describe('boundedRead', () => {
  it('gives up a read with no reply, aborting it, and the read queued after it is made', async () => {
    vi.useFakeTimers()
    try {
      const signals: AbortSignal[] = []
      const shown: string[] = []
      const failed: string[] = []
      let calls = 0
      let late: ((v: string) => void) | undefined
      const once = async () => {
        calls++
        const n = calls
        const w = await boundedRead(
          (signal) => {
            signals.push(signal)
            // The first read stalls: its reply comes only after it was given up. The second answers at once.
            return n === 1 ? new Promise<string>((resolve) => (late = resolve)) : Promise.resolve(`read ${n}`)
          },
          PERSON_READ_MS,
        )
        shown.push(w)
      }
      const reads = new SerialReads(once, (e) => failed.push(e.message))
      const first = reads.run()
      // A decision changed while the first read stalls: one more read is queued behind it.
      void reads.run()
      await vi.advanceTimersByTimeAsync(PERSON_READ_MS - 1)
      expect(calls).toBe(1)
      expect(signals[0]!.aborted).toBe(false)
      await vi.advanceTimersByTimeAsync(1)
      await first
      expect(failed).toEqual([noReplyWords(PERSON_READ_MS)])
      expect(failed[0]).toBe('the server sent no reply in 15 s')
      expect(signals[0]!.aborted).toBe(true)
      expect(calls).toBe(2)
      expect(shown).toEqual(['read 2'])
      // The stalled read's reply, late: never shown.
      late!('read 1, late')
      await vi.advanceTimersByTimeAsync(0)
      expect(shown).toEqual(['read 2'])
    } finally {
      vi.useRealTimers()
    }
  })

  it('is given up at once when its caller aborts it, and returns a reply that comes in time', async () => {
    const ctl = new AbortController()
    let seen: AbortSignal | undefined
    const read = boundedRead((signal) => ((seen = signal), new Promise<string>(() => {})), PERSON_READ_MS, ctl)
    ctl.abort()
    await expect(read).rejects.toThrow()
    expect(seen!.aborted).toBe(true)
    await expect(boundedRead(() => Promise.resolve('work'), PERSON_READ_MS)).resolves.toBe('work')
    await expect(boundedRead(() => Promise.reject(new Error('404')), PERSON_READ_MS)).rejects.toThrow('404')
    const gone = new AbortController()
    gone.abort()
    let made = false
    await expect(boundedRead(() => ((made = true), Promise.resolve('x')), PERSON_READ_MS, gone)).rejects.toThrow()
    expect(made).toBe(false)
  })
})

// A decision's change reads the person's work again, and the ticket lists, which no decision changes, keep the pages Show more read: 40
// tickets shown stay 40. A read for another reason, of another person, or after their tickets' counts changed starts the lists again.
describe('keepsTicketLists', () => {
  const tickets: NonNullable<PersonWork['tickets']> = { owner: 0, assignee: 45, ownerHref: '/api/dispatch/tickets?owner=ana', assigneeHref: '/api/dispatch/tickets?assignee=person%3Aana' }
  const summary = (i: number) => ({ id: `t${i}`, key: `CND-${i}`, title: `Ticket ${i}`, status: 'todo' }) as unknown as TicketSummary

  it('keeps the pages read through Show more across a decision event', async () => {
    const lists = freshTicketLists(tickets)
    expect(lists.map((l) => l.id)).toEqual(['assignee'])
    const list = lists[0]!
    const pages: TicketsPage[] = [
      { tickets: Array.from({ length: TICKETS_PAGE }, (_, i) => summary(i)), next: 'c1', total: 45 } as TicketsPage,
      { tickets: Array.from({ length: TICKETS_PAGE }, (_, i) => summary(i + TICKETS_PAGE)), next: 'c2', total: 45 } as TicketsPage,
    ]
    let page = 0
    await readTicketsPage(list, async () => pages[page++]!)
    await readTicketsPage(list, async () => pages[page++]!)
    expect(list.tickets).toHaveLength(40)
    // The decision event's read of the same person, their tickets as they were: the lists, and their 40 tickets, stay.
    expect(keepsTicketLists(true, 'ana', 'ana', tickets, { ...tickets })).toBe(true)
    expect(list.tickets).toHaveLength(40)
  })

  it('starts the lists again for any other read', () => {
    expect(keepsTicketLists(false, 'ana', 'ana', tickets, tickets)).toBe(false)
    expect(keepsTicketLists(true, 'ana', 'nate', tickets, tickets)).toBe(false)
    expect(keepsTicketLists(true, null, 'ana', undefined, tickets)).toBe(false)
    expect(keepsTicketLists(true, 'ana', 'ana', tickets, { ...tickets, assignee: 46 })).toBe(false)
    expect(keepsTicketLists(true, 'ana', 'ana', tickets, null)).toBe(false)
    expect(keepsTicketLists(true, 'ana', 'ana', null, tickets)).toBe(false)
    expect(keepsTicketLists(true, 'ana', 'ana', null, null)).toBe(true)
    expect(freshTicketLists(null)).toEqual([])
  })
})
