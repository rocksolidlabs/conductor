import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as vue from 'vue'
import { usePeople, type PeoplePage } from './usePeople'

// The names Conductor knows as the crew editor's name pickers read them (HM1, PR-10): the first page with no search, the names that
// contain the search text when one is typed (a name past the first 200 is found by searching for it), a slow reply never putting back
// names a newer one replaced, a failed read keeping the names read before, and a search longer than any name answered with none, unread.
// usePeople reads Nuxt's auto-imports (ref, useApi) as globals: each is stubbed.

function held<T>() {
  let ok!: (v: T) => void
  let fail!: (e: unknown) => void
  const p = new Promise<T>((resolve, reject) => {
    ok = resolve
    fail = reject
  })
  return { p, ok, fail }
}

let sent: Array<{ query: Record<string, string> } & ReturnType<typeof held<PeoplePage>>>

beforeEach(() => {
  sent = []
  vi.stubGlobal('ref', vue.ref)
  vi.stubGlobal('useApi', () => ({
    request: (path: string, opts: { query: Record<string, string> }) => {
      expect(path).toBe('/api/people')
      const h = held<PeoplePage>()
      sent.push({ query: opts.query, ...h })
      return h.p
    },
  }))
})

afterEach(() => {
  vi.unstubAllGlobals()
})

const page = (...names: string[]): PeoplePage => ({ people: names.map((name) => ({ name, dispatch: true, crews: [] })) })

describe('usePeople', () => {
  it('reads the first page, then the names that contain the search text, past the first page', async () => {
    const people = usePeople()
    const first = Array.from({ length: 200 }, (_, i) => `p${String(i).padStart(3, '0')}`)
    const all = people.load()
    expect(sent[0]!.query).toEqual({})
    sent[0]!.ok({ ...page(...first), next: 'p199' })
    await all
    expect(people.names.value).toHaveLength(200)
    expect(people.names.value).not.toContain('zoe')

    const found = people.load('zo')
    expect(sent[1]!.query).toEqual({ q: 'zo' })
    sent[1]!.ok(page('zoe'))
    await found
    expect(people.names.value).toEqual(['zoe'])

    // The search cleared: the first page again.
    const again = people.load('')
    expect(sent[2]!.query).toEqual({})
    sent[2]!.ok(page('ana'))
    await again
    expect(people.names.value).toEqual(['ana'])
  })

  it('drops a reply older than the last read started, and keeps the names on a failure', async () => {
    const people = usePeople()
    const slow = people.load()
    const fast = people.load('an')
    sent[1]!.ok(page('ana'))
    await fast
    sent[0]!.ok(page('ana', 'nate'))
    await slow
    expect(people.names.value).toEqual(['ana'])

    const failed = people.load('x')
    sent[2]!.fail(new Error('down'))
    await failed
    expect(people.names.value).toEqual(['ana'])
  })

  it('answers a search longer than a name with none, without a read', async () => {
    const people = usePeople()
    const first = people.load()
    sent[0]!.ok(page('ana'))
    await first
    await people.load('é'.repeat(61))
    expect(sent).toHaveLength(1)
    expect(people.names.value).toEqual([])
    // Sixty characters are still a name's: read.
    const sixty = people.load('é'.repeat(60))
    expect(sent[1]!.query).toEqual({ q: 'é'.repeat(60) })
    sent[1]!.ok(page())
    await sixty
  })
})
