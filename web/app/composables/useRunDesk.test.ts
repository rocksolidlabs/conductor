import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, nextTick, ref, watch, type EffectScope } from 'vue'
import type { JoinInfo } from '~/composables/useSessions'
import { CloseCode, type ControlMessage, type Welcome } from '~/utils/protocol'
import type { CloseInfo } from '~/utils/transport/types'
import { deskRefused, deskTarget, DESK_RETRY_FIRST_MS, useRunDesk } from './useRunDesk'

// A run decision link's desk on the join page (docs/plans/decision-inbox.md, PR-7), over fake connections the test opens and
// closes: the connection is the hosted desk wherever the join names one (a switchyard, or a server a publisher publishes to); a
// failed first try is joined again, as is a desk that served the lane and dropped, the page's Decide shown throughout; a refusal of
// the link, or an owner that serves it no decisions, ends it. The wait before a join again is the fake clock's.

class FakeDesk {
  decisions = false
  closed = false
  private controlCbs: Array<(m: ControlMessage) => void> = []
  private closeCbs: Array<(i: CloseInfo) => void> = []
  private settle!: (w: Welcome) => void
  private fail!: (e: unknown) => void
  readonly welcome = new Promise<Welcome>((resolve, reject) => {
    this.settle = resolve
    this.fail = reject
  })
  constructor(readonly to: string) {}
  connect() {
    return this.welcome
  }
  onControl(cb: (m: ControlMessage) => void) {
    this.controlCbs.push(cb)
  }
  onClose(cb: (i: CloseInfo) => void) {
    this.closeCbs.push(cb)
  }
  close() {
    this.closed = true
  }
  /** The owner's welcome. */
  welcomes(decisions: boolean) {
    this.decisions = decisions
    this.settle({ t: 'welcome', decisions } as Welcome)
  }
  /** The connection's end, as the transport tells it: its welcome refused first when it had none. */
  ends(info: CloseInfo) {
    this.decisions = false
    this.fail(new Error(info.reason))
    for (const cb of this.closeCbs) cb(info)
  }
  control(m: ControlMessage) {
    for (const cb of this.controlCbs) cb(m)
  }
}

const flush = async () => {
  for (let i = 0; i < 3; i++) {
    await Promise.resolve()
    await nextTick()
  }
}

describe('deskTarget', () => {
  const run = { id: 'run-1', name: 'Run', members: [] }
  it('is the hosted desk wherever the join names one, a server that is no switchyard included', () => {
    expect(deskTarget({ run: { ...run, desk: 'h-desk' }, role: 'control', switchyard: true } as JoinInfo)).toEqual({ kind: 'hosted', id: 'h-desk' })
    expect(deskTarget({ run: { ...run, desk: 'h-desk' }, role: 'control', switchyard: false } as JoinInfo)).toEqual({ kind: 'hosted', id: 'h-desk' })
  })
  it("is the server's own desk route where the join names none, and none at a switchyard", () => {
    expect(deskTarget({ run, role: 'control', switchyard: false } as JoinInfo)).toEqual({ kind: 'local', run: 'run-1' })
    expect(deskTarget({ run, role: 'control', switchyard: true } as JoinInfo)).toBeNull()
    expect(deskTarget({ role: 'control' } as JoinInfo)).toBeNull()
  })
})

describe('deskRefused', () => {
  it("is a refusal of the link by close code or by the owner's error, never the network's", () => {
    expect(deskRefused({ code: CloseCode.Forbidden, reason: '' })).toBe(true)
    expect(deskRefused({ code: CloseCode.Unauthorized, reason: '' })).toBe(true)
    expect(deskRefused({ code: 1000, reason: '', error: { code: 'not_decision_link', message: '' } })).toBe(true)
    expect(deskRefused({ code: 1006, reason: 'peer connection lost' })).toBe(false)
    expect(deskRefused({ code: 4400, reason: 'no welcome from server' })).toBe(false)
  })
})

describe('useRunDesk', () => {
  let opened: FakeDesk[]
  let info = ref<JoinInfo | null>(null)
  let joined = ref(false)
  const refresh = vi.fn(async () => {})
  /** Each test's desk lives in a scope of its own, ended with the test, as the page's is with the page. */
  let scope: EffectScope
  let unmount: Array<() => void>

  beforeEach(() => {
    vi.useFakeTimers()
    opened = []
    info = ref<JoinInfo | null>({ run: { id: 'run-1', name: 'Run', members: [], desk: 'h-desk' }, role: 'control', switchyard: false } as JoinInfo)
    joined = ref(false)
    refresh.mockClear()
    scope = effectScope()
    unmount = []
    vi.stubGlobal('ref', ref)
    vi.stubGlobal('watch', watch)
    vi.stubGlobal('onBeforeUnmount', (f: () => void) => unmount.push(f))
    vi.stubGlobal('useTerminalTransport', () => ({
      create: (spec: { sessionId: string; kind: string }) => {
        const d = new FakeDesk(`${spec.kind}:${spec.sessionId}`)
        opened.push(d)
        return d
      },
      desk: (run: string) => {
        const d = new FakeDesk(`local:${run}`)
        opened.push(d)
        return d
      },
    }))
  })
  afterEach(() => {
    for (const f of unmount) f()
    scope.stop()
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  function start() {
    return scope.run(() => useRunDesk({ info, token: ref('tok'), server: ref(''), joined, refresh }))!
  }

  it('joins the hosted desk the join names at a server that is no switchyard, and lists for its welcome', async () => {
    const d = start()
    joined.value = true
    await flush()
    expect(opened.map((x) => x.to)).toEqual(['hosted:h-desk'])
    opened[0]!.welcomes(true)
    await flush()
    expect([d.shown.value, d.live.value, d.seq.value]).toEqual([true, true, 1])
    opened[0]!.control({ t: 'decisions_changed' } as ControlMessage)
    expect(d.seq.value).toBe(2)
  })

  it('joins again after a first try that failed before any welcome (the network), and shows the Decide once it serves', async () => {
    const d = start()
    joined.value = true
    await flush()
    opened[0]!.ends({ code: 1006, reason: 'the peers could not connect' })
    await flush()
    expect(d.shown.value).toBe(false)
    expect(opened).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(DESK_RETRY_FIRST_MS)
    await flush()
    expect(refresh).toHaveBeenCalledTimes(1)
    expect(opened).toHaveLength(2)
    opened[1]!.welcomes(true)
    await flush()
    expect([d.shown.value, d.live.value]).toEqual([true, true])
  })

  it('keeps the Decide shown through a desk that dropped after serving, and joins it again; Refresh joins it at once', async () => {
    const d = start()
    joined.value = true
    await flush()
    opened[0]!.welcomes(true)
    await flush()
    opened[0]!.ends({ code: 1006, reason: 'peer connection lost' })
    await flush()
    expect([d.shown.value, d.live.value], 'shown, not live').toEqual([true, false])
    // Refresh, before the wait is out: joined again at once, the wait's own join dropped.
    d.retry()
    await flush()
    expect(opened).toHaveLength(2)
    await vi.advanceTimersByTimeAsync(DESK_RETRY_FIRST_MS * 4)
    await flush()
    expect(opened, 'no second join from the wait').toHaveLength(2)
    opened[1]!.welcomes(true)
    await flush()
    expect([d.shown.value, d.live.value, d.seq.value]).toEqual([true, true, 2])
  })

  it('ends at a refusal of the link, or at an owner that serves it no decisions, and joins nothing again', async () => {
    const d = start()
    joined.value = true
    await flush()
    opened[0]!.ends({ code: 1000, reason: '', error: { code: 'not_decision_link', message: "a desk is a run decision link's" } })
    await vi.advanceTimersByTimeAsync(DESK_RETRY_FIRST_MS * 30)
    await flush()
    expect(opened).toHaveLength(1)
    expect(d.shown.value).toBe(false)

    joined.value = false
    await flush()
    joined.value = true
    await flush()
    expect(opened).toHaveLength(2)
    opened[1]!.welcomes(false)
    await flush()
    expect(opened[1]!.closed).toBe(true)
    await vi.advanceTimersByTimeAsync(DESK_RETRY_FIRST_MS * 30)
    expect(opened).toHaveLength(2)
    expect(d.shown.value).toBe(false)
  })

  it('drops what it showed when the page leaves', async () => {
    const d = start()
    joined.value = true
    await flush()
    opened[0]!.welcomes(true)
    await flush()
    joined.value = false
    await flush()
    expect([d.shown.value, d.live.value, opened[0]!.closed]).toEqual([false, false, true])
  })
})
