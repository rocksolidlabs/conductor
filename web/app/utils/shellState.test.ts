import { describe, expect, it, vi } from 'vitest'
import { createAnswer } from './answer'
import type { ShellItem, ShellProvider, ShellState } from './shell'
import { createShellState } from './shellState'
import { shellAnswering } from './transport/shell'

// A sign-out seen any way (docs/plans/phone-shell.md, A4): through the provider's own report or through a read of its state, it is
// cleaned up once and the same way: the answer machine moved on, so nothing begun before it writes a view, and the inbox kept for a
// provider that cannot be reached dropped.

/** A provider whose state the test sets, and whose reports it fires itself, late or never. */
function provider() {
  let now: ShellState = { signedIn: true, notifications: 'on' }
  const listeners: Array<(s: ShellState) => void> = []
  const p: Pick<ShellProvider, 'state' | 'onState'> = {
    state: async () => now,
    onState: (cb) => (listeners.push(cb as (s: ShellState) => void), () => {}),
  }
  return { p, set: (s: ShellState) => (now = s), report: (s: ShellState) => listeners.forEach((l) => l(s)) }
}

const inbox = { items: [] as ShellItem[], asOf: '2026-11-19T10:00:00Z' }

describe('a sign-out', () => {
  it('seen only through a read of the state (a provider that never reports) does the cleanup', async () => {
    const pr = provider()
    const signedOut = vi.fn()
    const st = createShellState(pr.p, { name: 'Desk relay', signedOut })
    await st.start()
    st.lastInbox.value = inbox
    pr.set({ signedIn: false, notifications: 'unknown' })
    await st.refresh()
    expect(signedOut).toHaveBeenCalledTimes(1)
    expect(st.lastInbox.value).toBeNull()
  })

  it('reported after the read that saw it does the cleanup once', async () => {
    const pr = provider()
    const signedOut = vi.fn()
    const st = createShellState(pr.p, { name: 'Desk relay', signedOut })
    await st.start()
    st.lastInbox.value = inbox
    pr.set({ signedIn: false, notifications: 'unknown' })
    await st.refresh()
    pr.report({ signedIn: false, notifications: 'unknown' })
    expect(signedOut).toHaveBeenCalledTimes(1)
    expect(st.lastInbox.value).toBeNull()
  })

  it('reported by the provider does the cleanup, and a send begun before it writes nothing after', async () => {
    const pr = provider()
    let release!: () => void
    const held = new Promise<void>((r) => (release = r))
    const it0 = { id: 'i1', decision: { id: 'dr-7', state: 'open', rev: 1, type: 'clarification', request: { question: 'q', options: [] } }, rows: [], owner: { name: 'desk' }, asOf: '', offline: false } as unknown as ShellItem
    const machine = createAnswer({
      inbox: {
        list: async () => ({ items: [], asOf: '' }),
        item: async () => it0,
        respond: async () => (await held, { outcome: 'kept', item: it0 }),
        answerOf: async () => ({ state: 'none' }),
        resolve: async () => ({ id: 'i1' }),
        watch: () => () => {},
      },
      drafts: { get: async () => null, list: async () => [], put: async () => ({ ok: true }), remove: async () => {} },
      name: 'Desk relay',
      mint: () => 'r1',
    })
    const st = createShellState(pr.p, { name: 'Desk relay', signedOut: () => machine.signedOut() })
    await st.start()
    st.lastInbox.value = inbox
    await machine.open('i1', it0)
    const sending = machine.answer('i1', { text: 'yes' })
    await vi.waitFor(() => expect(machine.view('i1').step).toBe('sending'))
    pr.report({ signedIn: false, notifications: 'unknown' })
    release()
    await sending
    expect(machine.view('i1')).toMatchObject({ step: 'editing', draft: null })
    expect(st.lastInbox.value).toBeNull()
  })

  it('a state read that does not answer within its wait is said in words, the last state kept', async () => {
    vi.useFakeTimers()
    try {
      const st = createShellState({ state: () => new Promise(() => {}), onState: () => () => {} }, { name: 'Desk relay', signedOut: () => {}, wait: 1000 })
      const read = st.start()
      await vi.advanceTimersByTimeAsync(1000)
      await read
      expect(st.stateWords.value).toBe('Desk relay did not say whether this phone is signed in.')
      expect(st.state.value).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  it("empties every view the pages draw from: no card, no draft and no state of the signed-out person stays", async () => {
    const pr = provider()
    const it0 = { id: 'i1', decision: { id: 'dr-7', state: 'open', rev: 1, type: 'clarification', request: { question: 'Ship it?', options: [] } }, rows: [], owner: { name: 'desk' }, asOf: '', offline: false } as unknown as ShellItem
    const drafts = new Map<string, unknown>()
    const ans = shellAnswering(
      {
        inbox: { list: async () => ({ items: [], asOf: '' }), item: async () => it0, respond: async () => ({ outcome: 'kept' }), answerOf: async () => ({ state: 'none' }), resolve: async () => ({ id: 'i1' }), watch: () => () => {} },
        drafts: { get: async () => null, list: async () => [], put: async (d) => (drafts.set(d.id, d), { ok: true }), remove: async () => {} },
      },
      'Desk relay',
      { mint: () => 'r1', now: () => 0 },
    )
    const st = createShellState(pr.p, { name: 'Desk relay', signedOut: () => ans.signedOut() })
    await st.start()
    await ans.machine.open('i1', it0)
    const onPage = ans.answeringFor('i1')
    onPage.draft!.changed('dr-7', { text: 'typed before the sign-out' })
    await vi.waitFor(() => expect(onPage.draft!.initial('dr-7')).toEqual({ text: 'typed before the sign-out' }))
    expect(ans.views.value.i1?.item?.decision.request.question).toBe('Ship it?')
    // The sign-out, through the one cleanup.
    st.apply({ signedIn: false, notifications: 'unknown' })
    expect(ans.views.value).toEqual({})
    expect(onPage.draft!.initial('dr-7')).toBeNull()
    expect(onPage.state!('dr-7')).toMatchObject({ lock: true })
  })
})
