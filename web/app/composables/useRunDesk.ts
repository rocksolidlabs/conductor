import type { JoinInfo } from '~/composables/useSessions'
import { CloseCode, DecideCode } from '~/utils/protocol'
import { DecisionLane } from '~/utils/transport/decide'
import type { CloseInfo, TerminalTransport } from '~/utils/transport/types'

/** How long the desk waits before it connects again after a close, doubling from the first to the last. */
export const DESK_RETRY_FIRST_MS = 1_000
export const DESK_RETRY_MAX_MS = 15_000
/** How long a try's read of the link's join waits before the desk is joined as last known anyway (the read is then cancelled). */
export const DESK_JOIN_READ_MS = 10_000

/**
 * Which connection a run decision link's desk is, as the link's join names it: the hosted desk the join names (`run.desk`, a
 * hosted session's id at the server that answered, a switchyard or a server a publisher publishes its runs to), else, at a server
 * that is no switchyard, the server's own desk route (`GET /ws/runs/{run}/desk`); none at a switchyard whose join names no desk.
 * `known`, the hosted desk a join of this run named before: kept when a join names none now (it never goes back to local).
 */
export type DeskTarget = { kind: 'hosted'; id: string } | { kind: 'local'; run: string }
export function deskTarget(info: JoinInfo | null | undefined, known = ''): DeskTarget | null {
  const run = info?.run
  if (!run) return null
  if (run.desk || known) return { kind: 'hosted', id: run.desk || known }
  if (info.switchyard) return null
  return { kind: 'local', run: run.id }
}

/** Error codes of a desk's close that refuse this link itself (beside 4403, which they come with): never the network's. */
const REFUSED = new Set<string>([DecideCode.NotDecisionLink, DecideCode.Revoked, DecideCode.Expired])

/**
 * Whether a desk's close refuses this link itself (4403: revoked, expired, or no decision link of this run), after which the desk
 * is not joined again. Any other close is joined again: the network's, the owner's restart, no welcome in time, 4404 (a desk not
 * there now: a switchyard that restarted, its publisher not back yet) and 4401 (a link not known there now) among them.
 */
export function deskRefused(info: CloseInfo): boolean {
  if (info.code === CloseCode.Forbidden) return true
  return !!info.error && REFUSED.has(info.error.code)
}

/**
 * What the desk does next (the one rule of its life): `serve` on a welcome that says `decisions`; `end` at a refusal of the link
 * (deskRefused), or at a welcome without `decisions` from the server's own desk route before any served (an owner older than
 * decision links); `again`, joined again on the backoff, for everything else: any other close, and a welcome without
 * `decisions` from a hosted desk or after one served (the owner on its way back).
 */
export function deskStep(e: { close: CloseInfo } | { decisions: boolean }, to: DeskTarget, shown: boolean): 'serve' | 'again' | 'end' {
  if ('close' in e) return deskRefused(e.close) ? 'end' : 'again'
  if (e.decisions) return 'serve'
  return to.kind === 'local' && !shown ? 'end' : 'again'
}

/**
 * Reads whose results land in the order they were asked: one that lands after a newer one has is stale, and is dropped (a late
 * join read must not overwrite a newer one).
 */
export function newestRead() {
  let asked = 0
  let landed = 0
  return {
    ask() {
      const n = ++asked
      return {
        /** Whether a newer read has landed since this one was asked. */
        stale: () => n < landed,
        /** Marks this read landed, unless it is stale: true when its result is to be used. */
        land: () => (n < landed ? false : ((landed = n), true)),
      }
    },
  }
}

/**
 * A run decision link's desk on the join page (docs/plans/decision-inbox.md, PR-7): the connection that carries the run's decisions
 * with no member alive, owned by the page for as long as it has joined the run. Joined once the page is, as the join names it
 * (deskTarget); only a welcome that says `decisions` makes it the lane. Its life is one rule (deskStep):
 *
 * - Once a join of the run has named a hosted desk, the page keeps it: a join read again that names none, a 4404 for a desk not
 *   there now, a join read that fails (a switchyard restarting answers 503) or that does not answer never makes it the server's
 *   own route and never ends the retries. Only a refusal of the link itself (4403) or the page leaving ends it; Decide stays shown
 *   meanwhile, its words "Connecting again".
 * - A connection the page gives up is closed by the page before it is let go, whatever closed it (a WebRTC transport that says it
 *   closed when its peer connection only disconnected keeps its socket and peer open otherwise), so no abandoned one outlives it.
 * - Any other close, before the first welcome too (a network that failed the first try), is joined again after 1 s, twice as long
 *   each time, 15 s at most, the link's join read again first (`refresh`, given a signal: bounded by DESK_JOIN_READ_MS, cancelled
 *   by the next try, a drop or the end, and the desk joined as last known when it does not answer); `retry` joins it again at once
 *   (the page's Refresh).
 * - `shown` holds from the first welcome that said `decisions` until the page leaves or the link names another run, through every
 *   reconnect, so what the page keeps of the lane (useJoinDecide: an action not yet confirmed, the item open) is kept with it;
 *   `live` says whether the desk carries the lane now. `seq` counts what the lane must list again for (each welcome, each
 *   `decisions_changed`).
 */
export function useRunDesk(opts: { info: Ref<JoinInfo | null>; token: Ref<string>; server: Ref<string>; joined: Ref<boolean>; refresh: (signal: AbortSignal) => Promise<void> }) {
  const { create, desk } = useTerminalTransport()
  const shown = ref(false)
  const live = ref(false)
  const seq = ref(0)
  let t: TerminalTransport | null = null
  let timer: ReturnType<typeof setTimeout> | undefined
  /** The join read of the try under way, cancelled by the next try, a drop or the end. */
  let reading: AbortController | null = null
  let wait = DESK_RETRY_FIRST_MS
  let on = false
  /** The hosted desk a join of this run has named since the page joined it: kept through a join that names none. */
  let known = ''
  /** The target joined now, as a key: the desk is dropped and joined again only when it changes. */
  let current = ''
  /** The lane over the desk while its welcome said `decisions`. */
  const lane = new DecisionLane(() => (t?.decisions ? t : null))

  const target = () => deskTarget(opts.info.value, known)

  function open(to: DeskTarget): TerminalTransport {
    if (to.kind === 'hosted') return create({ sessionId: to.id, token: opts.token.value, kind: 'hosted', server: opts.server.value })
    return desk(to.run, opts.token.value, opts.server.value)
  }

  /** Joins the desk as the join names it now (deskTarget, the known hosted desk kept), unless one is joined or there is none. */
  async function connect() {
    if (!on || t) return
    const to = target()
    if (!to) return
    const c = open(to)
    t = c
    c.onControl((m) => {
      if (m.t === 'decisions_changed' && t === c) seq.value++
    })
    c.onClose((info) => {
      if (t !== c) return
      t = null
      live.value = false
      // Closed here too: a transport's close can leave what it holds open (WebRTC's on a peer that only disconnected).
      c.close()
      if (!on) return
      if (deskStep({ close: info }, to, shown.value) === 'end') end()
      else again()
    })
    try {
      const w = await c.connect({ cols: 0, rows: 0 })
      if (t !== c) return
      const step = deskStep({ decisions: !!w.decisions }, to, shown.value)
      if (step === 'end') {
        // An owner that serves this link no decisions (an older one): the page stays as it was.
        end()
        return
      }
      if (step === 'again') {
        // A welcome without decisions from a desk that serves them: the owner on its way back, joined again on the wait.
        t = null
        live.value = false
        c.close()
        again()
        return
      }
      wait = DESK_RETRY_FIRST_MS
      shown.value = true
      live.value = true
      seq.value++
    } catch {
      /* the close says why, and decides whether to join again */
    }
  }

  /** The desk joined again after the wait, the link's join read again first, that read bounded and cancelled by the next try. */
  function again() {
    if (timer) return
    timer = setTimeout(() => {
      timer = undefined
      void tryAgain()
    }, wait)
    wait = Math.min(DESK_RETRY_MAX_MS, wait * 2)
  }
  async function tryAgain() {
    reading?.abort()
    const ctl = new AbortController()
    reading = ctl
    const bound = setTimeout(() => ctl.abort(), DESK_JOIN_READ_MS)
    const given = new Promise<void>((resolve) => ctl.signal.addEventListener('abort', () => resolve(), { once: true }))
    try {
      await Promise.race([opts.refresh(ctl.signal).catch(() => {}), given])
    } finally {
      clearTimeout(bound)
    }
    // A newer try, a drop or the end took over meanwhile; otherwise the desk is joined as the join names it, or as last known.
    if (reading !== ctl) return
    reading = null
    void connect()
  }

  /** Closes the connection and drops a join waiting to be made, its read cancelled; what the page shows stays. */
  function drop() {
    if (timer) clearTimeout(timer)
    timer = undefined
    reading?.abort()
    reading = null
    const c = t
    t = null
    live.value = false
    c?.close()
  }

  /** Nothing joins the desk again: the page stays as it was before it had one. */
  function end() {
    on = false
    drop()
    shown.value = false
  }

  /** Joins the desk again now, when the page holds it and it is not joined (the page's Refresh). */
  function retry() {
    if (!on || t) return
    drop()
    wait = DESK_RETRY_FIRST_MS
    void connect()
  }

  watch(
    [opts.joined, () => opts.info.value?.run?.id ?? '', () => opts.info.value?.run?.desk ?? '', () => !!opts.info.value?.switchyard],
    ([joined, run, named], prev) => {
      // Joined, left, or the link names another run or desk now: the desk is joined again as the join names it. Only a page that
      // left, or a link that names another run, drops what it showed and the desk it knew; a join that names no desk keeps it.
      const [wasJoined, wasRun] = prev ?? [false, '']
      const fresh = !joined || !run || !wasJoined || run !== wasRun
      if (fresh) {
        shown.value = false
        wait = DESK_RETRY_FIRST_MS
        known = ''
      }
      if (joined && run && named) known = named
      const key = joined && run ? JSON.stringify(target()) : ''
      if (!fresh && key === current) return
      current = key
      drop()
      on = !!(joined && run)
      if (on) void connect()
    },
    // Sync, so a read of the join that names another desk joins it before the wait's own join looks (connect: one at a time).
    { immediate: true, flush: 'sync' },
  )
  onBeforeUnmount(end)

  return { lane, shown, live, seq, retry }
}
