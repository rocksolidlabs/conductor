import type { JoinInfo } from '~/composables/useSessions'
import { CloseCode, DecideCode } from '~/utils/protocol'
import { DecisionLane } from '~/utils/transport/decide'
import type { CloseInfo, TerminalTransport } from '~/utils/transport/types'

/** How long the desk waits before it connects again after a close, doubling from the first to the last. */
export const DESK_RETRY_FIRST_MS = 1_000
export const DESK_RETRY_MAX_MS = 15_000

/**
 * Which connection a run decision link's desk is, as the link's join names it: the hosted desk the join names (`run.desk`, a
 * hosted session's id at the server that answered, a switchyard or a server a publisher publishes its runs to), else, at a server
 * that is no switchyard, the server's own desk route (`GET /ws/runs/{run}/desk`); none at a switchyard whose join names no desk.
 */
export type DeskTarget = { kind: 'hosted'; id: string } | { kind: 'local'; run: string }
export function deskTarget(info: JoinInfo | null | undefined): DeskTarget | null {
  const run = info?.run
  if (!run) return null
  if (run.desk) return { kind: 'hosted', id: run.desk }
  if (info.switchyard) return null
  return { kind: 'local', run: run.id }
}

/** Error codes of a desk's close that say the link is not to be joined again (beside the close codes): never the network's. */
const REFUSED = new Set<string>([DecideCode.NotDecisionLink, DecideCode.Revoked, DecideCode.Expired])

/**
 * Whether a desk's close is a refusal of the link (it is no decision link's, it is unknown, revoked, expired, or names nothing
 * here), after which the desk is not joined again; any other close (the network's, the owner's restart, no welcome in time) is
 * joined again.
 */
export function deskRefused(info: CloseInfo): boolean {
  if (info.code === CloseCode.Forbidden || info.code === CloseCode.Unauthorized || info.code === CloseCode.NotFound) return true
  return !!info.error && REFUSED.has(info.error.code)
}

/**
 * A run decision link's desk on the join page (docs/plans/decision-inbox.md, PR-7): the connection that carries the run's decisions
 * with no member alive, owned by the page for as long as it has joined the run. Joined once the page is, as the join names it
 * (deskTarget); only a welcome that says `decisions` makes it the lane. Its life is one rule:
 *
 * - A close that refuses the link (deskRefused) or a welcome without `decisions` (an older owner) ends it: the page stays as it
 *   was, and nothing joins it again.
 * - Any other close, before the first welcome too (a network that failed the first try), is joined again after 1 s, twice as long
 *   each time, 15 s at most, the link's join read again first; `retry` joins it again at once (the page's Refresh).
 * - `shown` holds from the first welcome that said `decisions` until the page leaves or the link names another run, through every
 *   reconnect, so what the page keeps of the lane (useJoinDecide: an action not yet confirmed, the item open) is kept with it;
 *   `live` says whether the desk carries the lane now. `seq` counts what the lane must list again for (each welcome, each
 *   `decisions_changed`).
 */
export function useRunDesk(opts: { info: Ref<JoinInfo | null>; token: Ref<string>; server: Ref<string>; joined: Ref<boolean>; refresh: () => Promise<void> }) {
  const { create, desk } = useTerminalTransport()
  const shown = ref(false)
  const live = ref(false)
  const seq = ref(0)
  let t: TerminalTransport | null = null
  let timer: ReturnType<typeof setTimeout> | undefined
  let wait = DESK_RETRY_FIRST_MS
  let on = false
  /** The lane over the desk while its welcome said `decisions`. */
  const lane = new DecisionLane(() => (t?.decisions ? t : null))

  function open(to: DeskTarget): TerminalTransport {
    if (to.kind === 'hosted') return create({ sessionId: to.id, token: opts.token.value, kind: 'hosted', server: opts.server.value })
    return desk(to.run, opts.token.value, opts.server.value)
  }

  /** Joins the desk as the join names it now, unless one is joined or there is none to join. */
  async function connect() {
    if (!on || t) return
    const to = deskTarget(opts.info.value)
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
      if (!on) return
      if (deskRefused(info)) {
        end()
        return
      }
      again()
    })
    try {
      const w = await c.connect({ cols: 0, rows: 0 })
      if (t !== c) return
      if (!w.decisions) {
        // An owner that serves this link no decisions (an older one): the page stays as it was.
        end()
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

  /** The desk joined again after the wait, the link's join read again first. */
  function again() {
    if (timer) return
    timer = setTimeout(() => {
      timer = undefined
      void opts
        .refresh()
        .catch(() => {})
        .then(connect)
    }, wait)
    wait = Math.min(DESK_RETRY_MAX_MS, wait * 2)
  }

  /** Closes the connection and drops a join waiting to be made; what the page shows stays. */
  function drop() {
    if (timer) clearTimeout(timer)
    timer = undefined
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
    if (timer) clearTimeout(timer)
    timer = undefined
    wait = DESK_RETRY_FIRST_MS
    void connect()
  }

  watch(
    [opts.joined, () => opts.info.value?.run?.id ?? '', () => JSON.stringify(deskTarget(opts.info.value))],
    ([joined, run], prev) => {
      // Joined, left, or the link names another run or desk now: the desk is joined again as the join names it. Only a page that
      // left, or a link that names another run, drops what it showed.
      const [wasJoined, wasRun] = prev ?? [false, '']
      drop()
      if (!joined || !run || !wasJoined || run !== wasRun) {
        shown.value = false
        wait = DESK_RETRY_FIRST_MS
      }
      on = !!(joined && run)
      if (on) void connect()
    },
    // Sync, so a read of the join that names another desk joins it before the wait's own join looks (connect: one at a time).
    { immediate: true, flush: 'sync' },
  )
  onBeforeUnmount(end)

  return { lane, shown, live, seq, retry }
}
