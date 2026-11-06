import { describe, expect, it, vi } from 'vitest'
import type { KnownPerson } from '~/composables/usePeople'
import type { TicketSummary, TicketsPage } from '~/composables/useDispatch'
import type { PaletteGroup } from './palette'
import type { PaletteItem } from '~/composables/usePalette'
import { capPeople, oneReadAtATime, ownedHow, PEOPLE_IDLE, PEOPLE_MATCHED, personDetail, personFromSegment, personPath, personRows, personSegment, readTicketsPage, ticketsPageQuery, TICKETS_PAGE, type PersonTicketList } from './people'

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
    expect(row!.href).toBe('/people/Ana%20Mar%C3%ADa%2F2')
    expect(personPath('Ana María/2')).toBe(row!.href)
    row!.run()
    expect(open).toHaveBeenCalledWith('/people/Ana%20Mar%C3%ADa%2F2')
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

describe('personSegment', () => {
  const names = ['.', '..', '...', 'a/b', '50% c', '~x', '~', 'nate', 'Ana María', '%2E%2E']

  it('gives every name a segment of its own under /people/, which a URL never resolves away', () => {
    for (const name of names) {
      const path = personPath(name)
      const url = new URL(path, 'http://conductor.test/')
      // The browser keeps the path as written: no dot step resolved, no slash added.
      expect(url.pathname, name).toBe(path)
      expect(url.pathname.split('/').length, name).toBe(3)
      // The router hands the page the segment unescaped; one leading ~ removed gives the name back.
      expect(personFromSegment(decodeURIComponent(path.slice('/people/'.length))), name).toBe(name)
      // The API route's path, the same rule.
      expect(new URL(`/api/people/${personSegment(name)}`, 'http://conductor.test/').pathname, name).toBe(`/api/people/${personSegment(name)}`)
    }
    expect(personPath('.')).toBe('/people/~.')
    expect(personPath('..')).toBe('/people/~..')
    expect(personPath('~x')).toBe('/people/~~x')
    expect(personPath('a/b')).toBe('/people/a%2Fb')
    expect(personPath('50% c')).toBe('/people/50%25%20c')
    expect(personPath('nate')).toBe('/people/nate')
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
