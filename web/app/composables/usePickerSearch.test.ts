import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as vue from 'vue'
import { usePeople, type PeoplePage } from './usePeople'
import { usePickerSearch } from './usePickerSearch'

// Two name pickers of one table sharing one usePeople, as CrewPeopleTable's rows do (HM1, PR-10, Codex round 2 on #674): a picker closed
// mid-search has its search cleared by USelectMenu 100 ms later (resetSearchTermOnBlur, modelled here with the same timer), and that
// clearing, said as a search, read the first page over the names the other picker, opened meanwhile, was searching. A closed picker says
// nothing; one opened again before the clearing says its kept text after `open`. usePeople's auto-imports (ref, useApi) are stubbed.

let sent: Array<{ query: Record<string, string>; ok: (p: PeoplePage) => void }>
/** The newest read the pickers asked for. */
let last: Promise<void> = Promise.resolve()

beforeEach(() => {
  sent = []
  vi.useFakeTimers()
  vi.stubGlobal('ref', vue.ref)
  vi.stubGlobal('useApi', () => ({
    request: (_path: string, opts: { query: Record<string, string> }) =>
      new Promise<PeoplePage>((ok) => {
        sent.push({ query: opts.query, ok })
      }),
  }))
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

const page = (...names: string[]): PeoplePage => ({ people: names.map((name) => ({ name, dispatch: true, crews: [] })) })

/** A picker as USelectMenu drives it: its own open and search text, the text cleared 100 ms after it closes unless it opens again. */
function picker(known: ReturnType<typeof usePeople>) {
  const open = vue.ref(false)
  const term = vue.ref('')
  let reset: ReturnType<typeof setTimeout> | undefined
  usePickerSearch(open, term, {
    open: () => {
      last = known.load()
    },
    search: (q) => {
      last = known.load(q)
    },
  })
  return {
    term,
    async setOpen(o: boolean) {
      open.value = o
      if (o) clearTimeout(reset)
      else reset = setTimeout(() => (term.value = ''), 100)
      await vue.nextTick()
    },
    async type(q: string) {
      term.value = q
      await vue.nextTick()
    },
  }
}

/** Answers the newest read, and waits for its reply to land. */
async function answer(...names: string[]) {
  sent.at(-1)!.ok(page(...names))
  await last
}

describe('usePickerSearch', () => {
  it("leaves another picker's search results alone when a closed picker's search is cleared", async () => {
    const known = usePeople()
    const a = picker(known)
    const b = picker(known)

    await a.setOpen(true)
    expect(sent.at(-1)!.query).toEqual({})
    await a.type('zo')
    expect(sent.at(-1)!.query).toEqual({ q: 'zo' })
    await answer('zoe')
    await a.setOpen(false)

    // The other picker opens and searches before the first one's search is cleared.
    await b.setOpen(true)
    expect(sent.at(-1)!.query).toEqual({})
    await b.type('zz-200')
    expect(sent.at(-1)!.query).toEqual({ q: 'zz-200' })
    await answer('zz-200')
    expect(known.names.value).toEqual(['zz-200'])
    const reads = sent.length

    // The first picker's search cleared, 100 ms after it closed: nothing read, the other's results untouched.
    await vi.advanceTimersByTimeAsync(100)
    expect(a.term.value).toBe('')
    expect(sent).toHaveLength(reads)
    expect(known.names.value).toEqual(['zz-200'])

    // Typing on in the open picker still reads.
    await b.type('zz-20')
    expect(sent.at(-1)!.query).toEqual({ q: 'zz-20' })
  })

  it('says a kept search again when the picker opens before it is cleared', async () => {
    const known = usePeople()
    const a = picker(known)
    await a.setOpen(true)
    await a.type('zo')
    await answer('zoe')
    await a.setOpen(false)
    await a.setOpen(true)
    await vi.advanceTimersByTimeAsync(100)
    expect(a.term.value).toBe('zo')
    // The first page for the opening, then the kept text: the newest read is the search's.
    expect(sent.slice(-2).map((s) => s.query)).toEqual([{}, { q: 'zo' }])
    await answer('zoe')
    expect(known.names.value).toEqual(['zoe'])
  })
})
