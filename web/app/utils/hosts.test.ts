import { afterEach, describe, expect, it, vi } from 'vitest'
import type { HostRow, HostsReply, HostsRun } from './protocol'
import {
  AVAILABILITY_ORDER,
  hostAvailabilityWords,
  cardSessions,
  coalesce,
  hostDecisionWords,
  holderNote,
  holderWords,
  hostsReader,
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
  whereWords,
} from './hosts'
import { clock } from './syncStatus'
import { heldRunStatus } from './status'

// The Hosts page's model (docs/plans/hosts.md, PR-3 to PR-5): the words it says, the order it draws, where its counts link, the other
// machines' section, and the reader that never draws a reply older than one drawn.

const at = '2026-11-05T14:02:00Z'

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

describe("a machine's availability", () => {
  it('says here, connected, away since and last seen, and keeps that order', () => {
    expect(hostAvailabilityWords('here')).toBe('Here')
    expect(hostAvailabilityWords('connected', at)).toBe('Connected')
    expect(hostAvailabilityWords('away', at)).toBe(`Away since ${clock(at)}`)
    expect(hostAvailabilityWords('seen', at)).toBe(`Last seen ${clock(at)}`)
    expect(hostAvailabilityWords('away')).toBe('Away')
    expect(AVAILABILITY_ORDER).toEqual(['here', 'connected', 'away', 'seen'])
  })

  it("says another machine's as the time it last reported, never connected", () => {
    expect(remoteWords(at)).toBe(`Last reported ${clock(at)}`)
    expect(remoteWords(undefined)).toBe('Not reported yet')
    expect(remoteWords(at)).not.toMatch(/connected|here|online/i)
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

  it("says another server's lock line as that server wrote it, and any other note as the server made it", () => {
    expect(holderNote('other', 'pid 4242 since 2026-11-05T13:00:00Z')).toBe(`pid 4242, since ${clock('2026-11-05T13:00:00Z')}, as that server wrote`)
    expect(holderNote('other', 'its line is not readable')).toBe('its line is not readable')
    expect(holderNote('this_read_only', 'a newer Conductor wrote the database')).toBe('a newer Conductor wrote the database')
    expect(holderNote('none', 'this server stopped waiting for the lock at 13:02:10')).toBe('this server stopped waiting for the lock at 13:02:10')
    expect(holderNote('this', 'anything')).toBe('')
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
  const ledger = { holder: 'this' as const, note: '' }
  const run = (over: Partial<HostsRun> = {}): HostsRun => ({ id: 'team-0001', name: 'Release', crew: 'release', state: 'running', paused: false, waiting: 0, heldBy: 'this', ...over })

  it('links the run to its page and its count to its item', () => {
    const v = runRowView(run({ waiting: 1, waitingRef: 'decision:dr-1' }), ledger)
    expect(v).toMatchObject({ id: 'team-0001', title: 'Release', link: '/runs/team-0001', waiting: '1 needs you', waitingCount: 1, waitingLink: '/decisions?item=decision%3Adr-1', holder: 'Held by this server', note: '' })
  })

  it("draws each holder from that reply alone: from none on, the former holder's pid and time are nowhere in the row", () => {
    const replies: Array<{ holder: HostsRun['heldBy']; note: string }> = [
      { holder: 'this', note: '' },
      { holder: 'other', note: 'pid 4242 since 2026-11-05T13:00:00Z' },
      { holder: 'none', note: 'this server stopped waiting for the lock at 13:02:10' },
      { holder: 'unknown', note: 'the lock file could not be read: permission' },
      { holder: 'this_read_only', note: 'a newer Conductor wrote the database' },
      { holder: 'this', note: '' },
    ]
    const seen = replies.map((l, i) => {
      const v = runRowView(run({ heldBy: l.holder, waiting: l.holder === 'this' ? 0 : null }), l)
      // A second redraw that changes only the waiting count says the same holder.
      const again = runRowView(run({ heldBy: l.holder, waiting: l.holder === 'this' ? 2 : null }), l)
      expect(again.holder).toBe(v.holder)
      expect(again.note).toBe(v.note)
      const text = JSON.stringify(v) + JSON.stringify(again)
      if (i >= 2) expect(text).not.toContain('4242')
      return `${v.holder} | ${v.note}`
    })
    expect(seen).toEqual([
      'Held by this server | ',
      `Held by another server on this data directory | pid 4242, since ${clock('2026-11-05T13:00:00Z')}, as that server wrote`,
      'Held by no server | this server stopped waiting for the lock at 13:02:10',
      'Not known who holds these runs | the lock file could not be read: permission',
      'Held by this server, read-only | a newer Conductor wrote the database',
      'Held by this server | ',
    ])
  })

  it('says a count made elsewhere, and links nowhere for it', () => {
    const v = runRowView(run({ heldBy: 'other', waiting: null }), { holder: 'other', note: 'pid 1 since 2026-11-05T13:00:00Z' })
    expect(v.waiting).toBe('counted on the server that holds it')
    expect(v.waitingLink).toBeUndefined()
  })

  it("says another machine's run as reported, with no link and no holder", () => {
    const v = remoteRunView({ id: 'team-0099', crew: 'nightly', state: 'running', waiting: 2, reportedBy: 'studio', reportedAt: at })
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
    })
    r.refresh()
    r.bump()
    reads[0]!.reject(new Error('old'))
    reads[1]!.reject(new Error('new'))
    await flush()
    expect(failed).toEqual(['new'])
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
