import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, nextTick, ref, watch, type EffectScope } from 'vue'
import type { JoinInfo } from '~/composables/useSessions'
import { CloseCode, type ControlMessage, type Welcome } from '~/utils/protocol'
import type { CloseInfo } from '~/utils/transport/types'
import { deskRefused, deskStep, deskTarget, DESK_JOIN_READ_MS, DESK_RETRY_FIRST_MS, DESK_RETRY_MAX_MS, newestRead, useRunDesk } from './useRunDesk'

// A run decision link's desk on the join page (docs/plans/decision-inbox.md, PR-7), over fake connections the test opens and
// closes: the connection is the hosted desk wherever the join names one (a switchyard, or a server a publisher publishes to); a
// failed first try is joined again, as is a desk that served the lane and dropped, the page's Decide shown throughout, and a desk
// a join read again omits for a while; a connection that says it closed is closed by the page before it is let go; a refusal of
// the link, or an owner that serves it no decisions, ends it. Once a join has named a hosted desk the page keeps it (Codex round 3's
// reproductions: a join without it, a 4404 from a switchyard restarting, a join read that never answers), and only a 4403 or the
// page leaving ends it. The wait before a join again is the fake clock's.

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
  /** The connection's end, as the transport tells it (its own close not called, as WebRTC's ICE disconnect): its welcome refused first when it had none. */
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
  it('keeps the hosted desk a join named before when one names none now, at a switchyard or not', () => {
    expect(deskTarget({ run, role: 'control', switchyard: false } as JoinInfo, 'h-desk')).toEqual({ kind: 'hosted', id: 'h-desk' })
    expect(deskTarget({ run, role: 'control', switchyard: true } as JoinInfo, 'h-desk')).toEqual({ kind: 'hosted', id: 'h-desk' })
    expect(deskTarget({ run: { ...run, desk: 'h-2' }, role: 'control' } as JoinInfo, 'h-desk')).toEqual({ kind: 'hosted', id: 'h-2' })
  })
})

describe('deskRefused and deskStep', () => {
  const hosted = { kind: 'hosted', id: 'h-desk' } as const
  const local = { kind: 'local', run: 'run-1' } as const
  it("is a refusal of the link itself only: 4403, or the owner's error that says so, never a desk not there now", () => {
    expect(deskRefused({ code: CloseCode.Forbidden, reason: '' })).toBe(true)
    expect(deskRefused({ code: 1000, reason: '', error: { code: 'not_decision_link', message: '' } })).toBe(true)
    expect(deskRefused({ code: 1000, reason: '', error: { code: 'revoked', message: '' } })).toBe(true)
    expect(deskRefused({ code: CloseCode.NotFound, reason: 'no such session' })).toBe(false)
    expect(deskRefused({ code: CloseCode.Unauthorized, reason: '' })).toBe(false)
    expect(deskRefused({ code: 1006, reason: 'peer connection lost' })).toBe(false)
    expect(deskRefused({ code: 4400, reason: 'no welcome from server' })).toBe(false)
  })
  it('serves on a welcome with decisions, ends at a refusal or an older owner, and joins again for everything else', () => {
    expect(deskStep({ decisions: true }, hosted, false)).toBe('serve')
    expect(deskStep({ close: { code: CloseCode.Forbidden, reason: '' } }, hosted, true)).toBe('end')
    expect(deskStep({ close: { code: CloseCode.NotFound, reason: '' } }, hosted, true)).toBe('again')
    expect(deskStep({ decisions: false }, local, false), 'an owner older than decision links').toBe('end')
    expect(deskStep({ decisions: false }, local, true)).toBe('again')
    expect(deskStep({ decisions: false }, hosted, false)).toBe('again')
  })
})

describe('newestRead', () => {
  it('drops a read that lands after a newer one has landed', () => {
    const reads = newestRead()
    const older = reads.ask()
    const newer = reads.ask()
    expect(newer.land()).toBe(true)
    expect(older.stale()).toBe(true)
    expect(older.land(), 'the late one lands nowhere').toBe(false)
    const third = reads.ask()
    expect(third.stale()).toBe(false)
    expect(third.land()).toBe(true)
  })
})

describe('useRunDesk', () => {
  let opened: FakeDesk[]
  let info = ref<JoinInfo | null>(null)
  let joined = ref(false)
  const refresh = vi.fn(async (_signal: AbortSignal) => {})
  /** Each test's desk lives in a scope of its own, ended with the test, as the page's is with the page. */
  let scope: EffectScope
  let unmount: Array<() => void>

  beforeEach(() => {
    vi.useFakeTimers()
    opened = []
    info = ref<JoinInfo | null>({ run: { id: 'run-1', name: 'Run', members: [], desk: 'h-desk' }, role: 'control', switchyard: false } as JoinInfo)
    joined = ref(false)
    refresh.mockReset()
    refresh.mockImplementation(async () => {})
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

  it('closes a connection that told of its close, before and after its welcome, so none it gave up stays open', async () => {
    start()
    joined.value = true
    await flush()
    opened[0]!.ends({ code: 1006, reason: 'the peers could not connect' })
    await flush()
    expect(opened[0]!.closed, 'the first try, closed by the page').toBe(true)
    await vi.advanceTimersByTimeAsync(DESK_RETRY_FIRST_MS)
    await flush()
    opened[1]!.welcomes(true)
    await flush()
    opened[1]!.ends({ code: 1006, reason: 'peer connection lost' })
    await flush()
    expect(opened[1]!.closed, 'the desk that served, closed by the page').toBe(true)
    await vi.advanceTimersByTimeAsync(DESK_RETRY_FIRST_MS * 2)
    await flush()
    expect(opened).toHaveLength(3)
    expect(opened[2]!.closed).toBe(false)
  })

  it('keeps the desk a join named when a join read again omits it (a switchyard whose desk is not back yet), and joins it as known', async () => {
    const withDesk = { run: { id: 'run-1', name: 'Run', members: [], desk: 'h-desk' }, role: 'control', switchyard: true } as JoinInfo
    const noDesk = { run: { id: 'run-1', name: 'Run', members: [] }, role: 'control', switchyard: true } as JoinInfo
    info.value = withDesk
    const d = start()
    joined.value = true
    await flush()
    opened[0]!.welcomes(true)
    await flush()
    opened[0]!.ends({ code: 1006, reason: 'peer connection lost' })
    await flush()

    // The wait's read of the join names no desk: the desk it named is joined as known.
    refresh.mockImplementationOnce(async () => {
      info.value = noDesk
    })
    await vi.advanceTimersByTimeAsync(DESK_RETRY_FIRST_MS)
    await flush()
    expect(refresh).toHaveBeenCalledTimes(1)
    expect(opened.map((x) => x.to)).toEqual(['hosted:h-desk', 'hosted:h-desk'])
    expect([d.shown.value, d.live.value], 'shown, not live').toEqual([true, false])
    opened[1]!.welcomes(true)
    await flush()
    expect([d.shown.value, d.live.value]).toEqual([true, true])
  })

  it('never goes back to the local desk when a join at a server that is no switchyard omits the hosted desk (Codex round 3, 3)', async () => {
    const noDesk = { run: { id: 'run-1', name: 'Run', members: [] }, role: 'control', switchyard: false } as JoinInfo
    const d = start()
    joined.value = true
    await flush()
    opened[0]!.welcomes(true)
    await flush()
    // The join read again omits the desk (its publisher re-registering): no local connection, which would be refused 4403.
    info.value = noDesk
    await flush()
    expect(opened.map((x) => x.to)).toEqual(['hosted:h-desk'])
    expect(opened[0]!.closed, 'the hosted desk joined is kept').toBe(false)
    opened[0]!.ends({ code: 1006, reason: 'peer connection lost' })
    await vi.advanceTimersByTimeAsync(DESK_RETRY_FIRST_MS)
    await flush()
    expect(opened.map((x) => x.to)).toEqual(['hosted:h-desk', 'hosted:h-desk'])
    expect(d.shown.value).toBe(true)
    opened[1]!.welcomes(true)
    await flush()
    expect([d.shown.value, d.live.value]).toEqual([true, true])
  })

  it("joins again through a switchyard's restart: its join refused 503, the desk not there yet (4404) (Codex round 3, 4)", async () => {
    const d = start()
    joined.value = true
    await flush()
    opened[0]!.welcomes(true)
    await flush()
    opened[0]!.ends({ code: 1006, reason: 'the switchyard went away' })
    await flush()
    // The join read fails (503): the desk it knew is joined, and is not there yet.
    refresh.mockImplementation(async () => {
      throw new Error('503 no publisher registered')
    })
    await vi.advanceTimersByTimeAsync(DESK_RETRY_FIRST_MS)
    await flush()
    expect(opened).toHaveLength(2)
    opened[1]!.ends({ code: CloseCode.NotFound, reason: 'no such session' })
    await flush()
    expect([d.shown.value, d.live.value], 'Decide shown, connecting again').toEqual([true, false])
    // The publisher back, its desk under the same id: the next try joins it, and it serves.
    refresh.mockImplementation(async () => {})
    await vi.advanceTimersByTimeAsync(DESK_RETRY_FIRST_MS * 2)
    await flush()
    expect(opened.map((x) => x.to)).toEqual(['hosted:h-desk', 'hosted:h-desk', 'hosted:h-desk'])
    opened[2]!.welcomes(true)
    await flush()
    expect([d.shown.value, d.live.value]).toEqual([true, true])
  })

  it('bounds a join read that never answers: cancelled, the desk joined as known, and a later answer joins nothing (Codex round 3, 5)', async () => {
    const d = start()
    joined.value = true
    await flush()
    opened[0]!.welcomes(true)
    await flush()
    opened[0]!.ends({ code: 1006, reason: 'peer connection lost' })
    await flush()
    const signals: AbortSignal[] = []
    let answer!: () => void
    refresh.mockImplementation((signal: AbortSignal) => {
      signals.push(signal)
      return new Promise<void>((resolve) => (answer = resolve))
    })
    await vi.advanceTimersByTimeAsync(DESK_RETRY_FIRST_MS)
    await flush()
    expect(refresh).toHaveBeenCalledTimes(1)
    expect(opened, 'waiting on the read, not yet past its bound').toHaveLength(1)
    await vi.advanceTimersByTimeAsync(DESK_JOIN_READ_MS)
    await flush()
    expect(signals[0]!.aborted, 'the read cancelled at its bound').toBe(true)
    expect(opened.map((x) => x.to)).toEqual(['hosted:h-desk', 'hosted:h-desk'])

    // That one fails too; the next try's read hangs, and Refresh cancels it and joins at once.
    opened[1]!.ends({ code: 1006, reason: 'peer connection lost' })
    await flush()
    await vi.advanceTimersByTimeAsync(DESK_RETRY_FIRST_MS * 2)
    await flush()
    expect(refresh).toHaveBeenCalledTimes(2)
    d.retry()
    await flush()
    expect(signals[1]!.aborted, 'the read cancelled by the next try').toBe(true)
    expect(opened).toHaveLength(3)
    // The cancelled read answers late: nothing more is joined.
    answer()
    await vi.advanceTimersByTimeAsync(DESK_JOIN_READ_MS)
    await flush()
    expect(opened).toHaveLength(3)
    opened[2]!.welcomes(true)
    await flush()
    expect([d.shown.value, d.live.value]).toEqual([true, true])
  })

  it('ends only at a 4403 once a hosted desk is known, whatever came before', async () => {
    const d = start()
    joined.value = true
    await flush()
    opened[0]!.welcomes(true)
    await flush()
    for (const code of [CloseCode.NotFound, CloseCode.Unauthorized, 1006]) {
      opened.at(-1)!.ends({ code, reason: '' })
      await vi.advanceTimersByTimeAsync(DESK_RETRY_MAX_MS)
      await flush()
    }
    expect(opened).toHaveLength(4)
    expect(d.shown.value).toBe(true)
    opened.at(-1)!.ends({ code: CloseCode.Forbidden, reason: 'revoked', error: { code: 'revoked', message: '' } })
    await vi.advanceTimersByTimeAsync(DESK_RETRY_MAX_MS * 4)
    await flush()
    expect(opened).toHaveLength(4)
    expect(d.shown.value).toBe(false)
  })

  it('ends at a refusal of the link, or at a server whose own desk route serves it no decisions, and joins nothing again', async () => {
    const d = start()
    joined.value = true
    await flush()
    opened[0]!.ends({ code: 1000, reason: '', error: { code: 'not_decision_link', message: "a desk is a run decision link's" } })
    await vi.advanceTimersByTimeAsync(DESK_RETRY_FIRST_MS * 30)
    await flush()
    expect(opened).toHaveLength(1)
    expect(d.shown.value).toBe(false)

    joined.value = false
    info.value = { run: { id: 'run-1', name: 'Run', members: [] }, role: 'control', switchyard: false } as JoinInfo
    await flush()
    joined.value = true
    await flush()
    expect(opened.map((x) => x.to)).toEqual(['hosted:h-desk', 'local:run-1'])
    opened[1]!.welcomes(false)
    await flush()
    expect(opened[1]!.closed).toBe(true)
    await vi.advanceTimersByTimeAsync(DESK_RETRY_FIRST_MS * 30)
    expect(opened).toHaveLength(2)
    expect(d.shown.value).toBe(false)
  })

  it('joins a hosted desk again whose welcome says no decisions (its owner on its way back), never ending there', async () => {
    const d = start()
    joined.value = true
    await flush()
    opened[0]!.welcomes(false)
    await flush()
    expect(opened[0]!.closed).toBe(true)
    await vi.advanceTimersByTimeAsync(DESK_RETRY_FIRST_MS)
    await flush()
    expect(opened).toHaveLength(2)
    opened[1]!.welcomes(true)
    await flush()
    expect([d.shown.value, d.live.value]).toEqual([true, true])
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
