import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { extensionsWords, type Capabilities } from '../utils/capabilities'
import { CAPABILITIES_RETRY_FIRST_MS, CAPABILITIES_RETRY_MOST_MS, CapabilitiesPoll } from './useCapabilities'

// The reads of GET /api/capabilities on fake timers, the route a stubbed fetch: when the reply is read again, and when it never is.

const starting: Capabilities = { providers: [{ kind: 'identity', name: 'cloud', state: 'starting' }, { kind: 'notifications', name: 'push', state: 'ready' }] }
const ready: Capabilities = { providers: [{ kind: 'identity', name: 'cloud', state: 'ready' }, { kind: 'notifications', name: 'push', state: 'ready' }] }

/** The route, stubbed: each call answers the next reply (the last again once they run out), or fails for an Error; `at` the times. */
function route(...replies: Array<Capabilities | Error>) {
  const at: number[] = []
  const fetch = vi.fn(() => {
    at.push(Date.now())
    const r = replies.length > 1 ? replies.shift()! : replies[0]!
    return r instanceof Error ? Promise.reject(r) : Promise.resolve(structuredClone(r))
  })
  return { fetch, at }
}

function shown(fetch: () => Promise<unknown>) {
  const caps = ref<Capabilities | null>(null)
  const poll = new CapabilitiesPoll(fetch, caps)
  poll.token('workbench-token')
  poll.attach()
  return { caps, poll, words: () => (caps.value ? extensionsWords(caps.value) : '') }
}

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(0)
})
afterEach(() => vi.useRealTimers())

describe('a provider starting', () => {
  it('is read again after the first wait, shown ready without "(starting)", and not read after', async () => {
    const { fetch } = route(starting, ready)
    const { poll, words } = shown(fetch)
    await poll.load()
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(words()).toBe('identity: cloud (starting) · notifications: push')
    await vi.advanceTimersByTimeAsync(CAPABILITIES_RETRY_FIRST_MS - 1)
    expect(fetch).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(fetch).toHaveBeenCalledTimes(2)
    expect(words()).toBe('identity: cloud · notifications: push')
    expect(words()).not.toContain('(starting)')
    await vi.advanceTimersByTimeAsync(10 * CAPABILITIES_RETRY_MOST_MS)
    expect(fetch).toHaveBeenCalledTimes(2)
    // Known, with none starting: a later load reads nothing.
    await poll.load()
    expect(fetch).toHaveBeenCalledTimes(2)
  })

  it('that never starts is read again 2 s after the last reply, doubling to a minute, and a read that fails keeps what is known', async () => {
    const { fetch, at } = route(starting, starting, new Error('Cannot reach the server'), starting)
    const { poll, words } = shown(fetch)
    await poll.load()
    await vi.advanceTimersByTimeAsync(5 * 60_000)
    const gaps = at.slice(1).map((t, i) => t - at[i]!)
    expect(gaps.slice(0, 7)).toEqual([2000, 4000, 8000, 16000, 32000, 60000, 60000])
    expect(words()).toContain('(starting)')
  })

  it('is not read again once no reader shows it, and is read on when one shows again', async () => {
    const { fetch } = route(starting, starting, ready)
    const { poll, words } = shown(fetch)
    await poll.load()
    poll.detach()
    await vi.advanceTimersByTimeAsync(10 * CAPABILITIES_RETRY_MOST_MS)
    expect(fetch).toHaveBeenCalledTimes(1)
    poll.attach()
    await poll.load()
    expect(fetch).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(CAPABILITIES_RETRY_FIRST_MS)
    expect(fetch).toHaveBeenCalledTimes(3)
    expect(words()).toBe('identity: cloud · notifications: push')
  })
})

describe('none starting', () => {
  it('is read once and never again', async () => {
    const { fetch } = route({ providers: [] })
    const { poll, words } = shown(fetch)
    await poll.load()
    await poll.load()
    await vi.advanceTimersByTimeAsync(10 * CAPABILITIES_RETRY_MOST_MS)
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(words()).toBe('None: the open-source feature set')
  })

  it('shares a read on its way', async () => {
    const { fetch } = route(ready)
    const { poll } = shown(fetch)
    await Promise.all([poll.load(), poll.load(), poll.load()])
    expect(fetch).toHaveBeenCalledTimes(1)
  })
})

describe('the workbench token', () => {
  it('forgets the cache when it changes, drops a reply on its way for the old one, and reads again for the new', async () => {
    let answer: (c: Capabilities) => void = () => {}
    const fetch = vi.fn(() => new Promise<Capabilities>((r) => (answer = r)))
    const { caps, poll } = shown(fetch)
    const first = poll.load()
    poll.token('another-token')
    answer(ready) // the old token's reply, late
    await first
    expect(caps.value).toBeNull()
    const second = poll.load()
    expect(fetch).toHaveBeenCalledTimes(2)
    answer({ providers: [] })
    await second
    expect(caps.value).toEqual({ providers: [] })
    // The same token again forgets nothing.
    poll.token('another-token')
    expect(caps.value).toEqual({ providers: [] })
  })

  it('ends the re-reads of the old one', async () => {
    const { fetch } = route(starting)
    const { poll } = shown(fetch)
    await poll.load()
    poll.token('')
    await vi.advanceTimersByTimeAsync(10 * CAPABILITIES_RETRY_MOST_MS)
    expect(fetch).toHaveBeenCalledTimes(1)
  })
})
