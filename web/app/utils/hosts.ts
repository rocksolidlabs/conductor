// The Hosts page (docs/plans/hosts.md, PR-3): every machine this server knows, what runs and waits on each, and who holds this
// data directory's managed runs, from GET /api/hosts. The pure model the page and its components draw from: a platform in words,
// a machine's availability and its order, a run's holder in words, the links of what waits, the other machines' section, and the
// one-read-at-a-time reader with its queue and generation. It imports no Nuxt module, so the Playwright specs read it too.
import type { HostAvailability, HostRow, HostsHolder, HostsRemoteRun, HostsReply, HostsRun, HostsSessions } from './protocol'
import { clock } from './syncStatus'
import { otherMachinesShown, otherMachinesWhy } from './otherMachinesShown'

const OS_WORDS: Record<string, string> = {
  darwin: 'macOS',
  windows: 'Windows',
  linux: 'Linux',
  freebsd: 'FreeBSD',
  openbsd: 'OpenBSD',
  netbsd: 'NetBSD',
  android: 'Android',
  ios: 'iOS',
}
const ARCH_WORDS: Record<string, string> = { amd64: 'x64', '386': 'x86', arm64: 'arm64', arm: 'arm' }

/**
 * A platform as the page says it: `darwin/arm64` is "macOS · arm64", `windows/amd64` "Windows · x64", `linux/amd64+wsl` "Linux · x64
 * · WSL". A system or an architecture it has no word for is said as the host sent it; anything not of the form `<os>/<arch>` (an
 * older host sends none) is ''.
 */
export function platformWords(platform: string | undefined): string {
  const m = /^([a-z0-9]{1,15})\/([a-z0-9]{1,15})(\+wsl)?$/.exec(platform ?? '')
  if (!m) return ''
  const words = [OS_WORDS[m[1]!] ?? m[1]!, ARCH_WORDS[m[2]!] ?? m[2]!]
  if (m[3]) words.push('WSL')
  return words.join(' · ')
}

/** Here first, then connected, away and seen: the order of the page's cards. */
export const AVAILABILITY_ORDER: readonly HostAvailability[] = ['here', 'connected', 'away', 'seen']

/**
 * A machine's availability in words: "Here" (this server), "Connected" (a link of its host is up), "Away since 14:02" (none is; its
 * last link dropped then, and a session waits in its grace), "Last seen 13:40" (no session left; the last went then). Said only of
 * what this server sees itself; another machine's is "Last reported" (remoteWords), never one of these.
 */
export function hostAvailabilityWords(a: HostAvailability, since?: string): string {
  const at = clock(since)
  switch (a) {
    case 'here':
      return 'Here'
    case 'connected':
      return 'Connected'
    case 'away':
      return at ? `Away since ${at}` : 'Away'
    case 'seen':
      return at ? `Last seen ${at}` : 'Last seen'
  }
  return ''
}

/** Another machine's availability: the time the sync service last heard from it, "Last reported 14:05", never "online". */
export function remoteWords(lastReport: string | undefined): string {
  const at = clock(lastReport)
  return at ? `Last reported ${at}` : 'Not reported yet'
}

/** The host cards in the page's order: connected, away, then seen, each by name, a tie by key. */
export function sortHosts(rows: readonly HostRow[]): HostRow[] {
  const rank = (a: HostAvailability) => AVAILABILITY_ORDER.indexOf(a)
  return [...rows].sort((a, b) => rank(a.availability) - rank(b.availability) || a.name.localeCompare(b.name) || a.key.localeCompare(b.key))
}

const HOLDER_WORDS: Record<HostsHolder, string> = {
  this: 'Held by this server',
  this_read_only: 'Held by this server, read-only',
  other: 'Held by another server on this data directory',
  none: 'Held by no server',
  unknown: 'Not known who holds these runs',
}

/** Who holds the data directory's runs, in the page's words. */
export function holderWords(h: HostsHolder): string {
  return HOLDER_WORDS[h] ?? HOLDER_WORDS.unknown
}

/**
 * The holder's note as the page says it, from that reply's note alone: another server's lock line, `pid 4242 since <time>`, is "pid
 * 4242, since 13:00, as that server wrote" (nothing trusts it); any other note (a read-only ledger's reason, why no server holds
 * the runs, why the lock could not be read, "its line is not readable") is said as the server made it. None for `this`.
 */
export function holderNote(h: HostsHolder, note: string): string {
  if (h === 'this' || !note) return ''
  if (h === 'other') {
    const m = /^pid ([0-9]{1,10}) since (\S+)$/.exec(note)
    const at = m ? clock(m[2]) : ''
    if (m && at) return `pid ${m[1]}, since ${at}, as that server wrote`
  }
  return note
}

/** A machine's sessions in words: "3 sessions", "1 session", "No sessions". */
export function sessionWords(s: Pick<HostsSessions, 'running'>): string {
  if (!s.running) return 'No sessions'
  return s.running === 1 ? '1 session' : `${s.running} sessions`
}

/** What waits for a person, in words: "1 needs you", "2 need you"; '' at 0; "counted on the server that holds it" for null. */
export function hostWaitingWords(n: number | null | undefined): string {
  if (n === null || n === undefined) return 'counted on the server that holds it'
  if (n <= 0) return ''
  return `${n} ${n === 1 ? 'needs' : 'need'} you`
}

/**
 * Where a count of what waits links (PR-4): the one item on Needs you when the reply names it (`waitingRef`), Needs you itself when
 * more than one waits or the reply names none, nowhere at 0 or for a count made elsewhere (null). The reply carries the target; the
 * page derives none.
 */
export function waitingLink(n: number | null | undefined, ref?: string): string | undefined {
  if (!n || n <= 0) return undefined
  return ref ? `/decisions?item=${encodeURIComponent(ref)}` : '/decisions'
}

/** Open decisions in words: "1 decision waits", "2 decisions wait"; '' at 0. */
export function hostDecisionWords(n: number | undefined): string {
  if (!n || n <= 0) return ''
  return n === 1 ? '1 decision waits' : `${n} decisions wait`
}

/** "and 3 more", for rows a bounded list left out; '' at 0. */
export function moreWords(n: number | undefined, what: string): string {
  return n && n > 0 ? `and ${n} more ${what} not listed` : ''
}

/**
 * This server's run line: how many of the listed runs it holds, "Holds 2 runs", "Holds 1 run, read-only", or "Holds no runs" when it
 * holds none (another server, or no server, holds the data directory's runs: the runs section says which). A host's is always
 * "Holds no runs": a host runs sessions, and only a server's ledger holds a managed run.
 */
export function serverRunLine(holder: HostsHolder, runs: number): string {
  if ((holder !== 'this' && holder !== 'this_read_only') || runs <= 0) return 'Holds no runs'
  const n = runs === 1 ? '1 run' : `${runs} runs`
  return holder === 'this_read_only' ? `Holds ${n}, read-only` : `Holds ${n}`
}

/** A session a card lists: its id, and its name and state when the page holds it (the layout's live list); else its id alone. */
export interface CardSession {
  id: string
  name: string
  known: boolean
}

/** The card's sessions, from the reply's ids and the sessions the page holds, in the reply's order. */
export function cardSessions(ids: readonly string[] | undefined, nameOf: (id: string) => string | undefined): CardSession[] {
  return (ids ?? []).map((id) => {
    const name = nameOf(id)
    return { id, name: name || id, known: !!name }
  })
}

/** The Runtimes a machine's sessions run in, as its host reported them: "Runs in native", "Runs in native, docker"; '' for none. */
export function whereWords(where: readonly string[] | undefined): string {
  return where?.length ? `Runs in ${where.join(', ')}` : ''
}

/** "Reported by studio at 14:05": who sent a remote run's pointer and when; never a holder. */
export function reportedWords(by: string, at: string | undefined): string {
  const t = clock(at)
  return t ? `Reported by ${by} at ${t}` : `Reported by ${by}`
}

/**
 * A run's row as the page draws it, made from that row of that reply alone, so a row redrawn with the next reply shows nothing of the
 * last one (a former holder's pid and time among them): a local run's name linking to its run page, its id, what waits on it and
 * where that links, and its holder's words with the note; a remote run (another machine's) has no link, and says who reported it and
 * when, never a holder. Its state's mark is the status language's (heldRunStatus), drawn by the row from `state` and `paused`.
 */
export interface RunRowView {
  id: string
  title: string
  /** The run page; none for another machine's run. */
  link?: string
  state: string
  paused: boolean
  waiting: string
  /** How many wait, for the mark; 0 for none and for a count made elsewhere. */
  waitingCount: number
  waitingLink?: string
  holder: string
  note: string
  reported: string
}

export function runRowView(run: HostsRun, ledger: { holder: HostsHolder; note: string }): RunRowView {
  const holder = run.heldBy ?? ledger.holder
  return {
    id: run.id,
    title: run.name || run.crew || run.id,
    link: `/runs/${encodeURIComponent(run.id)}`,
    state: run.state,
    paused: run.paused,
    waiting: hostWaitingWords(run.waiting),
    waitingCount: run.waiting ?? 0,
    waitingLink: waitingLink(run.waiting, run.waitingRef),
    holder: holderWords(holder),
    // The note is the ledger's, made for this reply: shown under the holder it was made for, never under another.
    note: holder === ledger.holder ? holderNote(holder, ledger.note) : '',
    reported: '',
  }
}

export function remoteRunView(run: HostsRemoteRun): RunRowView {
  return {
    id: run.id,
    title: run.crew || run.id,
    state: run.state,
    paused: false,
    // Decisions open on that machine: read only here, answered there.
    waiting: run.waiting > 0 ? `${hostDecisionWords(run.waiting)} there` : '',
    waitingCount: run.waiting,
    holder: '',
    note: '',
    reported: reportedWords(run.reportedBy, run.reportedAt),
  }
}

/** What the page draws of other machines (PR-5): nothing, its one line, or the machines. */
export type RemoteSection = { kind: 'none' } | { kind: 'line'; words: string } | { kind: 'shown'; machines: NonNullable<HostsReply['other']>['machines']; more: number }

/**
 * The Other machines section, from the reply alone: nothing with no `other` field (no sync store, one never current, starting, or
 * signed out: a plain build's page), the one line while other machines are not shown ("Other machines are not shown: the sync
 * service has not answered since 14:02"), else the machines, with how many the reply left out.
 */
export function remoteSection(r: Pick<HostsReply, 'other' | 'at'> | null | undefined): RemoteSection {
  const shown = otherMachinesShown(r)
  if (shown === 'none') return { kind: 'none' }
  if (shown === 'line') return { kind: 'line', words: `Other machines are not shown: ${otherMachinesWhy(r)}` }
  return { kind: 'shown', machines: r!.other!.machines, more: r!.other!.more.machines }
}

/** The timer seam the coalescing and the page's clock take: the window's, or a test's fakes. */
export interface Timers {
  set: (fn: () => void, ms: number) => unknown
  clear: (id: unknown) => void
}
const realTimers: Timers = { set: (fn, ms) => setTimeout(fn, ms), clear: (id) => clearTimeout(id as ReturnType<typeof setTimeout>) }

/**
 * Asks made within `ms` of the first become one call, `ms` after it: the page's reads on the events the layout streams, at most once
 * a second however many arrive. `cancel` drops an ask not yet made.
 */
export function coalesce(fn: () => void, ms: number, timers: Timers = realTimers) {
  let timer: unknown = null
  return {
    ask() {
      if (timer !== null) return
      timer = timers.set(() => {
        timer = null
        fn()
      }, ms)
    },
    cancel() {
      if (timer !== null) timers.clear(timer)
      timer = null
    },
    get pending() {
      return timer !== null
    },
  }
}

/** One read's outcome, handed to the page: its reply, or its failure. */
export interface HostsReaderHooks<T> {
  /** Reads once; the signal aborts it when the token changes. */
  read: (signal: AbortSignal) => Promise<T>
  apply: (reply: T) => void
  fail: (e: Error) => void
}

/**
 * The page's reader: one read at a time, a refresh asked while a read is under way queued (at most one) and started when it ends, so
 * replies arrive in the order their reads began; each read carries the page's generation, bumped when the workbench token changes,
 * and a reply of an older generation, or older than one applied, is dropped. So the page never shows a reply older than one it has
 * shown. `bump` aborts the read under way and starts one of the new generation at once; `end` stops all of it.
 */
export function hostsReader<T>(hooks: HostsReaderHooks<T>) {
  let generation = 0
  let begun = 0
  let applied = 0
  let busy = false
  let queued = false
  let ended = false
  let abort: AbortController | null = null

  async function run() {
    busy = true
    queued = false
    const gen = generation
    const seq = ++begun
    const ac = new AbortController()
    abort = ac
    try {
      const reply = await hooks.read(ac.signal)
      if (!ended && gen === generation && seq > applied) {
        applied = seq
        hooks.apply(reply)
      }
    } catch (e) {
      if (!ended && gen === generation && !ac.signal.aborted) hooks.fail(e as Error)
    } finally {
      // A read of an older generation ending lets nothing start: the new generation's read is already under way.
      if (gen === generation) {
        busy = false
        abort = null
        if (queued && !ended) void run()
      }
    }
  }

  return {
    /** Read now, or once the read under way ends. */
    refresh() {
      if (ended) return
      if (busy) queued = true
      else void run()
    },
    /** The token changed: what is under way is dropped, and a read of the new generation starts. */
    bump() {
      if (ended) return
      generation++
      abort?.abort()
      abort = null
      busy = false
      queued = false
      void run()
    },
    end() {
      ended = true
      abort?.abort()
    },
    get busy() {
      return busy
    },
  }
}
