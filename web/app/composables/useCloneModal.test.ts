import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref, type Ref } from 'vue'
import type { SessionInfo, SessionLaunch } from './useSessions'
import { useCloneModal } from './useCloneModal'
import { useLaunchModal } from './useLaunchModal'

// Clone with options… (#434 review round 2): the read of what a session was launched with, asked for A and then for B, opens B's
// dialog whichever answers first; A's answer, late, leaves it alone. useCloneModal reads Nuxt's auto-imports (useState,
// useLaunchModal, useSessions, useToast) as globals: each is stubbed, the session reads held until the test answers them.

function session(id: string): SessionInfo {
  return { id, name: `name-${id}`, kind: 'server', status: 'running', cwd: `/repo/${id}` } as SessionInfo
}

function launchOf(id: string): SessionLaunch {
  return { agentId: 'claude', cwd: `/repo/${id}`, args: ['--model', id], yolo: false }
}

let states: Map<string, Ref<unknown>>
let held: Map<string, { answer: () => void; fail: (e: Error) => void }>
let toasts: Array<{ title: string; description?: string }>
beforeEach(() => {
  states = new Map()
  held = new Map()
  toasts = []
  vi.stubGlobal('useState', <T>(key: string, init: () => T) => {
    if (!states.has(key)) states.set(key, ref(init()))
    return states.get(key) as Ref<T>
  })
  vi.stubGlobal('useLaunchModal', useLaunchModal)
  vi.stubGlobal('useSessions', () => ({
    get: (id: string) =>
      new Promise((resolve, reject) => held.set(id, { answer: () => resolve({ session: session(id), launch: launchOf(id) }), fail: reject })),
  }))
  vi.stubGlobal('useToast', () => ({ add: (t: { title: string; description?: string }) => toasts.push(t) }))
})
afterEach(() => vi.unstubAllGlobals())

const settle = () => new Promise((r) => setTimeout(r, 0))

describe("Clone with options…'s read of the source's launch", () => {
  it("opens the later session's dialog when the earlier read answers last", async () => {
    const clone = useCloneModal()
    const modal = useLaunchModal()
    const a = clone.withOptions(session('a'))
    const b = clone.withOptions(session('b'))
    held.get('b')!.answer()
    await b
    expect(modal.open.value).toBe(true)
    expect(modal.prefill.value?.clone?.id).toBe('b')
    const asked = modal.asked.value
    held.get('a')!.answer()
    await a
    await settle()
    expect(modal.prefill.value?.clone?.id).toBe('b')
    expect(modal.prefill.value?.args).toBe('--model b')
    expect(modal.asked.value).toBe(asked)
  })

  it('opens the later one when the reads answer in order too', async () => {
    const clone = useCloneModal()
    const modal = useLaunchModal()
    const a = clone.withOptions(session('a'))
    const b = clone.withOptions(session('b'))
    held.get('a')!.answer()
    await a
    expect(modal.prefill.value).toBeNull()
    held.get('b')!.answer()
    await b
    expect(modal.prefill.value?.clone?.id).toBe('b')
  })

  it("says nothing of a superseded read's failure, and says the current one's", async () => {
    const clone = useCloneModal()
    const a = clone.withOptions(session('a'))
    const b = clone.withOptions(session('b'))
    held.get('a')!.fail(new Error('gone'))
    await a
    expect(toasts).toEqual([])
    held.get('b')!.fail(new Error('the network is down'))
    await b
    expect(toasts).toEqual([expect.objectContaining({ title: 'name-b cannot be cloned', description: 'the network is down' })])
  })

  // #434 review round 3: an ordinary Launch opened while the read is out holds a draft the late answer must not replace.
  it('opens nothing over a Launch dialog opened after the read was asked', async () => {
    const clone = useCloneModal()
    const modal = useLaunchModal()
    const a = clone.withOptions(session('a'))
    modal.show()
    expect(modal.open.value).toBe(true)
    expect(modal.prefill.value).toBeNull()
    const asked = modal.asked.value
    held.get('a')!.answer()
    await a
    await settle()
    expect(modal.prefill.value).toBeNull()
    expect(modal.asked.value).toBe(asked)
    expect(toasts).toEqual([])
  })

  it("opens nothing over the launch verb's prefill given after the read was asked, and says nothing of its failure", async () => {
    const clone = useCloneModal()
    const modal = useLaunchModal()
    const a = clone.withOptions(session('a'))
    modal.show({ agentId: 'codex', cwd: '/elsewhere' })
    held.get('a')!.answer()
    await a
    await settle()
    expect(modal.prefill.value).toEqual({ agentId: 'codex', cwd: '/elsewhere' })
    modal.open.value = false
    const b = clone.withOptions(session('b'))
    modal.show()
    held.get('b')!.fail(new Error('gone'))
    await b
    expect(toasts).toEqual([])
  })

  it('opens over a Launch dialog that was open before the read was asked', async () => {
    const clone = useCloneModal()
    const modal = useLaunchModal()
    modal.show()
    const a = clone.withOptions(session('a'))
    modal.show()
    held.get('a')!.answer()
    await a
    expect(modal.prefill.value?.clone?.id).toBe('a')
  })
})
