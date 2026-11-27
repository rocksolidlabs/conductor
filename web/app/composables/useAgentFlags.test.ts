import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, nextTick, ref } from 'vue'
import type { AgentFlags } from './useSessions'
import { AgentFlagsStore, FLAGS_NONE_MS, FLAGS_REQUEST_MS, FLAGS_RETRIES, FLAGS_RETRY_MS, useAgentFlags } from './useAgentFlags'

// The flags of each agent and version, asked once and kept; `reading` asked again a few times; a request ended after its bound;
// a reply for an agent no longer picked dropped.

function reply(agent: string, state: AgentFlags['state'], version = '1.0.0'): AgentFlags {
  return { agent, name: agent, version, state, flags: state === 'read' ? [{ name: '--x', value: 'none' }] : [], models: [] }
}

/** A server whose answers the test hands out: every ask waits until the test answers it. */
function fakeServer() {
  const asks: Array<{ id: string; signal: AbortSignal; answer: (r: AgentFlags) => void; fail: (e: Error) => void }> = []
  const ask = (id: string, signal: AbortSignal) =>
    new Promise<AgentFlags>((answer, fail) => {
      asks.push({ id, signal, answer, fail })
      signal.addEventListener('abort', () => fail(new Error('aborted')))
    })
  return { asks, ask }
}

const settle = async () => {
  for (let i = 0; i < 5; i++) await Promise.resolve()
}

describe('AgentFlagsStore', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('asks once for an agent and version, and answers from what it kept', async () => {
    const srv = fakeServer()
    const store = new AgentFlagsStore(srv.ask)
    const got: Array<AgentFlags | null> = []
    store.follow('claude', '1.0.0', (r) => got.push(r))
    store.follow('claude', '1.0.0', (r) => got.push(r))
    expect(srv.asks).toHaveLength(1)
    srv.asks[0]!.answer(reply('claude', 'read'))
    await settle()
    expect(got.map((r) => r?.state)).toEqual(['read', 'read'])
    store.follow('claude', '1.0.0', (r) => got.push(r))
    expect(srv.asks).toHaveLength(1)
    // Another version is another list.
    store.follow('claude', '2.0.0', () => {})
    expect(srv.asks).toHaveLength(2)
  })

  it('asks a reading reply again after a second, five times at most', async () => {
    const srv = fakeServer()
    const store = new AgentFlagsStore(srv.ask)
    const got: Array<string | undefined> = []
    store.follow('claude', '1.0.0', (r) => got.push(r?.state))
    for (let i = 0; i <= FLAGS_RETRIES; i++) {
      expect(srv.asks).toHaveLength(i + 1)
      srv.asks[i]!.answer(reply('claude', 'reading'))
      await settle()
      vi.advanceTimersByTime(FLAGS_RETRY_MS)
      await settle()
    }
    expect(srv.asks).toHaveLength(FLAGS_RETRIES + 1)
    expect(got.at(-1)).toBeUndefined()
  })

  it('asks a none again only after a minute', async () => {
    const srv = fakeServer()
    let now = 1_000_000
    const store = new AgentFlagsStore(srv.ask, () => now)
    store.follow('goose', '1.0.0', () => {})
    srv.asks[0]!.answer(reply('goose', 'none'))
    await settle()
    let last: AgentFlags | null = null
    store.follow('goose', '1.0.0', (r) => (last = r))
    expect(srv.asks).toHaveLength(1)
    expect(last).toMatchObject({ state: 'none' })
    now += FLAGS_NONE_MS
    store.follow('goose', '1.0.0', () => {})
    expect(srv.asks).toHaveLength(2)
  })

  it('ends a request with no answer after its bound, and says there is nothing to show', async () => {
    const srv = fakeServer()
    const store = new AgentFlagsStore(srv.ask)
    const got: Array<AgentFlags | null> = []
    store.follow('claude', '1.0.0', (r) => got.push(r))
    expect(srv.asks[0]!.signal.aborted).toBe(false)
    vi.advanceTimersByTime(FLAGS_REQUEST_MS)
    await settle()
    expect(srv.asks[0]!.signal.aborted).toBe(true)
    expect(got).toEqual([null])
    // Nothing was kept: the next field asks again.
    store.follow('claude', '1.0.0', () => {})
    expect(srv.asks).toHaveLength(2)
  })

  it('drops a reply for a follow that was stopped', async () => {
    const srv = fakeServer()
    const store = new AgentFlagsStore(srv.ask)
    const got: Array<AgentFlags | null> = []
    const stop = store.follow('claude', '1.0.0', (r) => got.push(r))
    stop()
    srv.asks[0]!.answer(reply('claude', 'read'))
    await settle()
    expect(got).toEqual([])
  })
})

describe('useAgentFlags', () => {
  it('follows the agent picked, and drops the reply of one no longer picked', async () => {
    const srv = fakeServer()
    const store = new AgentFlagsStore(srv.ask)
    const agent = ref<{ id: string; version?: string } | null>({ id: 'claude', version: '1.0.0' })
    const scope = effectScope()
    const f = scope.run(() => useAgentFlags(agent, store))!
    expect(srv.asks.map((a) => a.id)).toEqual(['claude'])
    agent.value = { id: 'codex', version: '0.1.0' }
    await nextTick()
    expect(srv.asks.map((a) => a.id)).toEqual(['claude', 'codex'])
    srv.asks[0]!.answer(reply('claude', 'read'))
    await settle()
    expect(f.reply.value).toBeNull()
    srv.asks[1]!.answer(reply('codex', 'read', '0.1.0'))
    await settle()
    expect(f.reply.value?.agent).toBe('codex')
    expect(f.flags.value.map((x) => x.name)).toEqual(['--x'])
    agent.value = null
    await nextTick()
    expect(f.reply.value).toBeNull()
    expect(f.flags.value).toEqual([])
    scope.stop()
  })
})
