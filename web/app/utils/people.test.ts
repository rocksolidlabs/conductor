import { describe, expect, it, vi } from 'vitest'
import type { KnownPerson } from '~/composables/usePeople'
import { ownedHow, personDetail, personPath, personRows } from './people'

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
