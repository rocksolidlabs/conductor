import { afterEach, describe, expect, it, vi } from 'vitest'
import type { HostRow, HostsLedger, HostsReply, HostsRun } from './protocol'
import { DecisionsLive } from '../composables/useDecisions'
import {
  AVAILABILITY_ORDER,
  hostAvailabilityWords,
  cardSessions,
  coalesce,
  hostDecisionWords,
  holderNote,
  holderWords,
  hostsReader,
  hostsRefreshOn,
  platformWords,
  remoteRunView,
  remoteSection,
  remoteWords,
  reportedWords,
  runRowView,
  serverRunLine,
  sessionWords,
  sortHosts,
  waitingLink,
  hostWaitingWords,
  timeWords,
  whereWords,
} from './hosts'
import { atWords } from './dayTime'
import { heldRunStatus } from './status'

// The Hosts page's model (docs/plans/hosts.md, PR-3 to PR-5): the words it says, the order it draws, where its counts link, the other
// machines' section, and the reader that never draws a reply older than one drawn.

const at = '2026-11-05T14:02:00Z'
/** A time of the reply, said against the reply's own moment. */
const clock = (iso: string) => atWords(iso, Date.parse(at))

describe('a platform in words', () => {
  it('says each form, WSL among them, and an unknown one as the host sent it', () => {
    expect(platformWords('darwin/arm64')).toBe('macOS · arm64')
    expect(platformWords('windows/amd64')).toBe('Windows · x64')
    expect(platformWords('linux/amd64')).toBe('Linux · x64')
    expect(platformWords('linux/amd64+wsl')).toBe('Linux · x64 · WSL')
    expect(platformWords('linux/386')).toBe('Linux · x86')
    expect(platformWords('plan9/mips')).toBe('plan9 · mips')
  })

  it('says nothing of a platform not of the form, or none', () => {
    for (const p of [undefined, '', 'linux', 'Linux/amd64', 'linux/amd64+WSL', 'linux/amd64/x', 'a'.repeat(16) + '/amd64']) expect(platformWords(p), String(p)).toBe('')
  })
})

describe('a time of the reply', () => {
  const local = (y: number, m: number, d: number, h: number, min: number) => new Date(y, m, d, h, min).toISOString()

  it("says the time alone on the reply's local day, and the day and month with it on any other", () => {
    const replyAt = local(2026, 10, 22, 9, 30)
    expect(timeWords(local(2026, 10, 22, 0, 5), replyAt)).toBe('00:05')
    expect(timeWords(local(2026, 10, 22, 14, 2), replyAt)).toBe('14:02')
    // The day before: an old report never reads as today's.
    expect(timeWords(local(2026, 10, 21, 23, 59), replyAt)).toBe('21 Nov 23:59')
    // A week before.
    expect(timeWords(local(2026, 10, 15, 14, 5), replyAt)).toBe('15 Nov 14:05')
  })

  it("says the day and month across a month's end, and a year's", () => {
    expect(timeWords(local(2026, 9, 31, 22, 40), local(2026, 10, 1, 8, 0))).toBe('31 Oct 22:40')
    expect(timeWords(local(2026, 11, 31, 23, 0), local(2027, 0, 1, 0, 30))).toBe('31 Dec 23:00')
    expect(timeWords(local(2026, 10, 1, 8, 0), local(2026, 10, 1, 8, 0))).toBe('08:00')
  })

  it('says every time on the page through it: away, seen, reported, a run reported, the holder\'s since', () => {
    const replyAt = local(2026, 10, 22, 9, 30)
    const before = local(2026, 10, 3, 14, 5)
    expect(hostAvailabilityWords('away', before, replyAt)).toBe('Away since 3 Nov 14:05')
    expect(hostAvailabilityWords('seen', before, replyAt)).toBe('Last seen 3 Nov 14:05')
    expect(remoteWords(before, replyAt)).toBe('Last reported 3 Nov 14:05')
    expect(reportedWords('studio', before, replyAt)).toBe('Reported by studio at 3 Nov 14:05')
    expect(holderNote({ holder: 'other', note: 'Another server holds the lock, as its lock file says.', pid: 7, since: before }, replyAt)).toBe('pid 7, since 3 Nov 14:05, as that server wrote')
    expect(holderNote({ holder: 'none', note: 'This server is taking the lock.', since: before }, replyAt)).toBe('This server is taking the lock · since 3 Nov 14:05')
    expect(timeWords(undefined, replyAt)).toBe('')
  })
})

describe("a machine's availability", () => {
  it('says here, connected, away since and last seen, and keeps that order', () => {
    expect(hostAvailabilityWords('here')).toBe('Here')
    expect(hostAvailabilityWords('connected', at, at)).toBe('Connected')
    expect(hostAvailabilityWords('away', at, at)).toBe(`Away since ${clock(at)}`)
    expect(hostAvailabilityWords('seen', at, at)).toBe(`Last seen ${clock(at)}`)
    expect(hostAvailabilityWords('away')).toBe('Away')
    expect(AVAILABILITY_ORDER).toEqual(['here', 'connected', 'away', 'seen'])
  })

  it("says another machine's as the time it last reported, never connected", () => {
    expect(remoteWords(at, at)).toBe(`Last reported ${clock(at)}`)
    expect(remoteWords(undefined)).toBe('Not reported yet')
    expect(remoteWords(at, at)).not.toMatch(/connected|here|online/i)
  })

  it('sorts the cards connected, away, then seen, each by name', () => {
    const row = (name: string, availability: HostRow['availability'], key = name): HostRow => ({ key, kind: 'host', name, availability, sessions: { running: 0, waiting: 0 }, sessionIds: [], keyFrom: 'machine' })
    const sorted = sortHosts([row('zed', 'seen'), row('mac', 'away'), row('box', 'connected'), row('ann', 'seen'), row('alb', 'connected'), row('alb', 'connected', 'k0')])
    expect(sorted.map((h) => `${h.name}/${h.availability}/${h.key}`)).toEqual(['alb/connected/alb', 'alb/connected/k0', 'box/connected/box', 'mac/away/mac', 'ann/seen/ann', 'zed/seen/zed'])
  })
})

describe('who holds the runs', () => {
  it("says the five holders in the page's words", () => {
    expect(holderWords('this')).toBe('Held by this server')
    expect(holderWords('this_read_only')).toBe('Held by this server, read-only')
    expect(holderWords('other')).toBe('Held by another server on this data directory')
    expect(holderWords('none')).toBe('Held by no server')
    expect(holderWords('unknown')).toBe('Not known who holds these runs')
  })

  it("says another server's pid and time from their fields, the server's sentence as given, and no server's time beside it", () => {
    const t = '2026-11-05T13:00:00Z'
    expect(holderNote({ holder: 'other', note: 'Another server holds the lock, as its lock file says.', pid: 4242, since: t }, at)).toBe(`pid 4242, since ${clock(t)}, as that server wrote`)
    expect(holderNote({ holder: 'other', note: 'Another server holds the lock, as its lock file says.', pid: 4242 }, at)).toBe('pid 4242, as that server wrote')
    expect(holderNote({ holder: 'other', note: "Another server holds the lock; its lock file's line is not readable." }, at)).toBe("Another server holds the lock; its lock file's line is not readable.")
    expect(holderNote({ holder: 'this_read_only', note: 'This server holds the lock, and reads its database only.' }, at)).toBe('This server holds the lock, and reads its database only.')
    expect(holderNote({ holder: 'none', note: 'This server stopped waiting for the lock.', since: '2026-11-05T13:02:10Z' }, at)).toBe(`This server stopped waiting for the lock · since ${clock('2026-11-05T13:02:10Z')}`)
    expect(holderNote({ holder: 'none', note: "The data directory's filesystem has no locks." }, at)).toBe("The data directory's filesystem has no locks.")
    expect(holderNote({ holder: 'unknown', note: 'The lock file could not be read: permission denied.' }, at)).toBe('The lock file could not be read: permission denied.')
    expect(holderNote({ holder: 'this', note: 'This server holds the lock.' }, at)).toBe('')
    // A time is never said as the stamp the server sent.
    expect(holderNote({ holder: 'none', note: 'This server is taking the lock.', since: '2026-11-05T13:02:10Z' }, at)).not.toContain('2026-11-05T')
  })

  it("says this server's run line, and none for a holder that is not this server", () => {
    expect(serverRunLine('this', 2)).toBe('Holds 2 runs')
    expect(serverRunLine('this', 1)).toBe('Holds 1 run')
    expect(serverRunLine('this_read_only', 1)).toBe('Holds 1 run, read-only')
    expect(serverRunLine('this', 0)).toBe('Holds no runs')
    for (const h of ['other', 'none', 'unknown'] as const) expect(serverRunLine(h, 3)).toBe('Holds no runs')
  })
})

describe('the counts and their links (PR-4)', () => {
  it('says the sessions and what waits, and the count made elsewhere', () => {
    expect(sessionWords({ running: 0 })).toBe('No sessions')
    expect(sessionWords({ running: 1 })).toBe('1 session')
    expect(sessionWords({ running: 3 })).toBe('3 sessions')
    expect(hostWaitingWords(0)).toBe('')
    expect(hostWaitingWords(1)).toBe('1 needs you')
    expect(hostWaitingWords(2)).toBe('2 need you')
    expect(hostWaitingWords(null)).toBe('counted on the server that holds it')
    expect(hostDecisionWords(1)).toBe('1 decision waits')
    expect(hostDecisionWords(2)).toBe('2 decisions wait')
    expect(hostDecisionWords(0)).toBe('')
  })

  it('says why a count is not made, by who holds the runs: only another server counts them there', () => {
    expect(hostWaitingWords(null, 'other')).toBe('counted on the server that holds it')
    expect(hostWaitingWords(null, 'none')).toBe('not counted: no server holds these runs')
    expect(hostWaitingWords(null, 'this_read_only')).toBe("not counted while this server's ledger is read-only")
    expect(hostWaitingWords(null, 'unknown')).toBe('not counted: not known who holds these runs')
    for (const h of ['none', 'this_read_only', 'unknown'] as const) expect(hostWaitingWords(null, h)).not.toContain('counted on the server')
  })

  it('links a count to its one item from the ref alone, to Needs you for more than one, and nowhere at 0', () => {
    expect(waitingLink(1, 'decision:dr-7')).toBe('/decisions?item=decision%3Adr-7')
    expect(waitingLink(1, 'question:s-1/q 2')).toBe('/decisions?item=question%3As-1%2Fq%202')
    expect(waitingLink(2, undefined)).toBe('/decisions')
    expect(waitingLink(1, undefined)).toBe('/decisions')
    expect(waitingLink(0, 'decision:dr-7')).toBeUndefined()
    expect(waitingLink(null, 'decision:dr-7')).toBeUndefined()
  })

  it('says where the sessions run, and names a session the page does not hold by its id', () => {
    expect(whereWords(['native'])).toBe('Runs in native')
    expect(whereWords(['native', 'docker'])).toBe('Runs in native, docker')
    expect(whereWords(undefined)).toBe('')
    expect(cardSessions(['s-1', 's-2'], (id) => (id === 's-1' ? 'build' : undefined))).toEqual([
      { id: 's-1', name: 'build', known: true },
      { id: 's-2', name: 's-2', known: false },
    ])
  })
})

describe("a run's row", () => {
  const ledger: HostsLedger = { holder: 'this', note: 'This server holds the lock.' }
  const run = (over: Partial<HostsRun> = {}): HostsRun => ({ id: 'team-0001', name: 'Release', crew: 'release', state: 'running', paused: false, waiting: 0, heldBy: 'this', ...over })

  it('links the run to its page and its count to its item', () => {
    const v = runRowView(run({ waiting: 1, waitingRef: 'decision:dr-1' }), ledger, at)
    expect(v).toMatchObject({ id: 'team-0001', title: 'Release', link: '/runs/team-0001', waiting: '1 needs you', waitingCount: 1, waitingLink: '/decisions?item=decision%3Adr-1', holder: 'Held by this server', note: '' })
  })

  it("draws each holder from that reply alone: from none on, the former holder's pid and time are nowhere in the row", () => {
    const replies: HostsLedger[] = [
      { holder: 'this', note: 'This server holds the lock.' },
      { holder: 'other', note: 'Another server holds the lock, as its lock file says.', pid: 4242, since: '2026-11-05T13:00:00Z' },
      { holder: 'none', note: 'This server stopped waiting for the lock.', since: '2026-11-05T13:02:10Z' },
      { holder: 'unknown', note: 'The lock file could not be read: permission denied.' },
      { holder: 'this_read_only', note: 'This server holds the lock, and reads its database only.' },
      { holder: 'this', note: 'This server holds the lock.' },
    ]
    const seen = replies.map((l, i) => {
      const v = runRowView(run({ heldBy: l.holder, waiting: l.holder === 'this' ? 0 : null }), l, at)
      // A second redraw that changes only the waiting count says the same holder.
      const again = runRowView(run({ heldBy: l.holder, waiting: l.holder === 'this' ? 2 : null }), l, at)
      expect(again.holder).toBe(v.holder)
      expect(again.note).toBe(v.note)
      const text = JSON.stringify(v) + JSON.stringify(again)
      if (i >= 2) expect(text).not.toContain('4242')
      return `${v.holder} | ${v.note}`
    })
    expect(seen).toEqual([
      'Held by this server | ',
      `Held by another server on this data directory | pid 4242, since ${clock('2026-11-05T13:00:00Z')}, as that server wrote`,
      `Held by no server | This server stopped waiting for the lock · since ${clock('2026-11-05T13:02:10Z')}`,
      'Not known who holds these runs | The lock file could not be read: permission denied.',
      'Held by this server, read-only | This server holds the lock, and reads its database only.',
      'Held by this server | ',
    ])
  })

  it('says a count made elsewhere, and links nowhere for it', () => {
    const v = runRowView(run({ heldBy: 'other', waiting: null }), { holder: 'other', note: 'Another server holds the lock, as its lock file says.', pid: 1 }, at)
    expect(v.waiting).toBe('counted on the server that holds it')
    expect(v.waitingLink).toBeUndefined()
    // Under no server, a read-only ledger, or a holder not known: what is true, never "counted on the server that holds it".
    expect(runRowView(run({ heldBy: 'none', waiting: null }), { holder: 'none', note: 'This server stopped waiting for the lock.' }, at).waiting).toBe('not counted: no server holds these runs')
    expect(runRowView(run({ heldBy: 'this_read_only', waiting: null }), { holder: 'this_read_only', note: 'This server holds the lock, and reads its database only.' }, at).waiting).toBe("not counted while this server's ledger is read-only")
  })

  it("says another machine's run as reported, with no link and no holder", () => {
    const v = remoteRunView({ id: 'team-0099', crew: 'nightly', state: 'running', waiting: 2, reportedBy: 'studio', reportedAt: at }, at)
    expect(v).toMatchObject({ title: 'nightly', waiting: '2 decisions wait there', holder: '', reported: `Reported by studio at ${clock(at)}` })
    expect(v.link).toBeUndefined()
    expect(v.waitingLink).toBeUndefined()
    expect(reportedWords('studio', undefined)).toBe('Reported by studio')
  })

  it("marks a run's state in the status language: running, paused, interrupted", () => {
    expect(heldRunStatus('running', false)).toMatchObject({ key: 'running', word: 'running' })
    expect(heldRunStatus('running', true)).toMatchObject({ key: 'waiting', word: 'paused' })
    expect(heldRunStatus('interrupted', false)).toMatchObject({ key: 'exited', word: 'interrupted, not yet continued' })
  })
})

describe('the Other machines section (PR-5)', () => {
  const machine = { device: 'dev-studio', name: 'studio', own: true, runs: [], decisions: { open: 0 } }
  const other = (state: NonNullable<HostsReply['other']>['state'], since?: string, machines = [machine]) => ({ at, other: { state, since, machines, more: { machines: 0, remoteRuns: 0 } } })

  it('draws nothing with no other field', () => {
    expect(remoteSection({ at })).toEqual({ kind: 'none' })
    expect(remoteSection(null)).toEqual({ kind: 'none' })
  })

  it("says the state's one line while other machines are not shown", () => {
    expect(remoteSection(other('unreachable', '2026-11-05T14:00:00Z', []))).toEqual({ kind: 'line', words: `Other machines are not shown: the sync service has not answered since ${clock('2026-11-05T14:00:00Z')}` })
    expect(remoteSection(other('lapsed', '2026-11-05T14:00:00Z', []))).toEqual({ kind: 'line', words: 'Other machines are not shown: the subscription has lapsed' })
    expect(remoteSection(other('behind', '2026-11-05T13:40:00Z', []))).toMatchObject({ kind: 'line' })
  })

  it('shows the machines while current, or behind under its bound', () => {
    expect(remoteSection(other('current'))).toMatchObject({ kind: 'shown', machines: [machine], more: 0 })
    expect(remoteSection({ at, other: { state: 'current', machines: [machine], more: { machines: 2, remoteRuns: 5 } } })).toMatchObject({ kind: 'shown', more: 2, moreRuns: 5 })
    expect(remoteSection(other('behind', '2026-11-05T13:58:00Z'))).toMatchObject({ kind: 'shown' })
  })
})

describe('the reader', () => {
  afterEach(() => vi.useRealTimers())

  function deferred<T>() {
    let resolve!: (v: T) => void
    let reject!: (e: Error) => void
    const promise = new Promise<T>((res, rej) => {
      resolve = res
      reject = rej
    })
    return { promise, resolve, reject }
  }
  const flush = () => new Promise((r) => setTimeout(r, 0))

  it('starts a refresh asked during a read after it ends, never beside it, and queues at most one', async () => {
    const reads: Array<ReturnType<typeof deferred<number>>> = []
    const applied: number[] = []
    const r = hostsReader<number>({
      read: () => {
        const d = deferred<number>()
        reads.push(d)
        return d.promise
      },
      apply: (n) => applied.push(n),
      fail: () => {},
      late: () => {},
    })
    r.refresh()
    r.refresh()
    r.refresh()
    expect(reads.length).toBe(1)
    reads[0]!.resolve(1)
    await flush()
    expect(reads.length).toBe(2)
    reads[1]!.resolve(2)
    await flush()
    expect(reads.length).toBe(2)
    expect(applied).toEqual([1, 2])
  })

  it('drops a reply of an older generation after the token changed, though it arrives last', async () => {
    const reads: Array<ReturnType<typeof deferred<string>>> = []
    const applied: string[] = []
    const r = hostsReader<string>({
      read: () => {
        const d = deferred<string>()
        reads.push(d)
        return d.promise
      },
      apply: (s) => applied.push(s),
      fail: () => {},
      late: () => {},
    })
    r.refresh()
    r.bump()
    expect(reads.length).toBe(2)
    // The newer generation's reply comes first, the older one after it: the older is never drawn.
    reads[1]!.resolve('new token')
    await flush()
    reads[0]!.resolve('old token')
    await flush()
    expect(applied).toEqual(['new token'])
  })

  it("says a read's failure, but not one of an older generation", async () => {
    const reads: Array<ReturnType<typeof deferred<string>>> = []
    const failed: string[] = []
    const r = hostsReader<string>({
      read: () => {
        const d = deferred<string>()
        reads.push(d)
        return d.promise
      },
      apply: () => {},
      fail: (e) => failed.push(e.message),
      late: () => {},
    })
    r.refresh()
    r.bump()
    reads[0]!.reject(new Error('old'))
    reads[1]!.reject(new Error('new'))
    await flush()
    expect(failed).toEqual(['new'])
  })

  it('gives up a read that never answers: the page is told once, a later ask reads, and the late answer is never drawn', async () => {
    // The timers' seam: the bound fires when the test says.
    const pending: Array<{ fn: () => void; ms: number; live: boolean }> = []
    const timers = {
      set: (fn: () => void, ms: number) => {
        const t = { fn, ms, live: true }
        pending.push(t)
        return t
      },
      clear: (t: unknown) => {
        if (t) (t as { live: boolean }).live = false
      },
    }
    const fireBound = () => {
      for (const t of pending.splice(0)) if (t.live) t.fn()
    }
    const reads: Array<ReturnType<typeof deferred<string>>> = []
    const signals: AbortSignal[] = []
    const applied: string[] = []
    let late = 0
    const r = hostsReader<string>(
      {
        read: (signal) => {
          signals.push(signal)
          const d = deferred<string>()
          reads.push(d)
          return d.promise
        },
        apply: (s) => applied.push(s),
        fail: () => {},
        late: () => late++,
      },
      { waitMs: 15_000, timers },
    )
    r.refresh()
    r.refresh() // queued behind the read that will never answer
    expect(pending.map((t) => t.ms)).toEqual([15_000])
    fireBound()
    expect(late).toBe(1)
    expect(signals[0]!.aborted).toBe(true)
    // The queued ask reads at once; the bound is told once, not again for the read it abandoned.
    expect(reads.length).toBe(2)
    reads[1]!.resolve('fresh')
    await flush()
    expect(applied).toEqual(['fresh'])
    // The abandoned read answering late is never drawn.
    reads[0]!.resolve('late')
    await flush()
    expect(applied).toEqual(['fresh'])
    expect(late).toBe(1)
    // A later ask reads again; a read that answers inside its bound tells nothing late.
    r.refresh()
    expect(reads.length).toBe(3)
    reads[2]!.resolve('next')
    await flush()
    fireBound()
    expect(applied).toEqual(['fresh', 'next'])
    expect(late).toBe(1)
  })

  it("reads again on a decision's event, coalesced with the rest, and not on an event that is none of its", () => {
    vi.useFakeTimers()
    const read = vi.fn()
    const c = coalesce(read, 1000)
    const live = new DecisionsLive()
    let bump: (() => void) | undefined
    const stop = hostsRefreshOn(
      [
        (ask) => {
          bump = ask
          return () => (bump = undefined)
        },
        (ask) => live.subscribe(() => ask()),
      ],
      () => c.ask(),
    )
    live.stream('capacity', { agent: 'claude' })
    vi.advanceTimersByTime(2000)
    expect(read).not.toHaveBeenCalled()
    live.stream('decision', { id: 'dr-7', run: 'team-0001', state: 'answered', rev: 2 })
    live.stream('decision', { id: 'dr-7', run: 'team-0001', state: 'applied', rev: 3 })
    // The decision's events alone read once, a second after the first.
    vi.advanceTimersByTime(1000)
    expect(read).toHaveBeenCalledTimes(1)
    // The layout's counters and a decision within the same second: one read more.
    bump!()
    live.stream('decision', { id: 'dr-7', run: 'team-0001', state: 'applied', rev: 4 })
    vi.advanceTimersByTime(1000)
    expect(read).toHaveBeenCalledTimes(2)
    stop()
    live.stream('decision', { id: 'dr-8', run: 'team-0001', state: 'open', rev: 1 })
    vi.advanceTimersByTime(2000)
    expect(read).toHaveBeenCalledTimes(2)
    expect(bump).toBeUndefined()
  })

  it('coalesces the asks of a second into one read, a second after the first', () => {
    vi.useFakeTimers()
    const fn = vi.fn()
    const c = coalesce(fn, 1000)
    c.ask()
    vi.advanceTimersByTime(500)
    c.ask()
    c.ask()
    expect(fn).not.toHaveBeenCalled()
    vi.advanceTimersByTime(500)
    expect(fn).toHaveBeenCalledTimes(1)
    c.ask()
    vi.advanceTimersByTime(999)
    expect(fn).toHaveBeenCalledTimes(1)
    c.cancel()
    vi.advanceTimersByTime(5000)
    expect(fn).toHaveBeenCalledTimes(1)
  })
})
