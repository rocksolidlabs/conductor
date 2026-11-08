import type { JoinInfo } from '~/composables/useSessions'
import { CloseCode } from '~/utils/protocol'
import { DecisionLane } from '~/utils/transport/decide'
import type { TerminalTransport } from '~/utils/transport/types'

/** How long the desk waits before it connects again after a close, doubling from the first to the last. */
const RETRY_FIRST_MS = 1_000
const RETRY_MAX_MS = 15_000

/**
 * A run decision link's desk on the join page (docs/plans/decision-inbox.md, PR-7): the connection that carries the run's decisions
 * with no member alive. At a switchyard it is the run's desk there (the join's `run.desk`, a hosted session's id reached as a member
 * is, through its relay when ICE fails); at the server itself, `GET /ws/runs/{run}/desk`. It is joined once the page is, and only
 * a welcome that says `decisions` makes it the lane (`decisions`): a link that is no decision link's is closed by the owner (4403)
 * and leaves the page as it was. A close the link did not cause (the network, the owner's restart) connects again, after 1 s and
 * twice as long each time, 15 s at most, reading the link's join again first; `seq` counts what the lane must list again for (each
 * welcome, each `decisions_changed`).
 */
export function useRunDesk(opts: { info: Ref<JoinInfo | null>; token: Ref<string>; server: Ref<string>; joined: Ref<boolean>; refresh: () => Promise<void> }) {
  const { create, desk } = useTerminalTransport()
  const decisions = ref(false)
  const seq = ref(0)
  let t: TerminalTransport | null = null
  let timer: ReturnType<typeof setTimeout> | undefined
  let wait = RETRY_FIRST_MS
  let on = false
  /** The lane over the desk while its welcome said `decisions`. */
  const lane = new DecisionLane(() => (t?.decisions ? t : null))

  /** The desk's connection for the link as the join names it now, or null where there is none to join. */
  function target(): TerminalTransport | null {
    const info = opts.info.value
    const run = info?.run
    if (!run) return null
    if (info.switchyard) return run.desk ? create({ sessionId: run.desk, token: opts.token.value, kind: 'hosted', server: opts.server.value }) : null
    return desk(run.id, opts.token.value, opts.server.value)
  }

  async function connect() {
    if (!on) return
    const c = target()
    t = c
    if (!c) return
    c.onControl((m) => {
      if (m.t === 'decisions_changed' && t === c) seq.value++
    })
    c.onClose((info) => {
      if (t !== c) return
      decisions.value = false
      t = null
      if (!on) return
      // The link is no decision link's, or is gone: nothing to join again.
      if (info.code === CloseCode.Forbidden || info.code === CloseCode.Unauthorized || info.code === CloseCode.NotFound) return
      timer = setTimeout(() => {
        timer = undefined
        void opts
          .refresh()
          .catch(() => {})
          .then(connect)
      }, wait)
      wait = Math.min(RETRY_MAX_MS, wait * 2)
    })
    try {
      const w = await c.connect({ cols: 0, rows: 0 })
      if (t !== c) return
      if (!w.decisions) {
        // An owner that serves this link no decisions (an older one): the page stays as it was.
        stop()
        return
      }
      wait = RETRY_FIRST_MS
      decisions.value = true
      seq.value++
    } catch {
      /* the close says why, and decides whether to connect again */
    }
  }

  function stop() {
    on = false
    if (timer) clearTimeout(timer)
    timer = undefined
    const c = t
    t = null
    decisions.value = false
    c?.close()
  }

  watch(
    [opts.joined, () => opts.info.value?.run?.id ?? '', () => opts.info.value?.run?.desk ?? '', () => !!opts.info.value?.switchyard],
    ([joined, run]) => {
      // Joined, left, or the link names another run or desk now: the desk is joined again as the join names it.
      stop()
      if (joined && run) {
        on = true
        wait = RETRY_FIRST_MS
        void connect()
      }
    },
    { immediate: true },
  )
  onBeforeUnmount(stop)

  return { lane, decisions, seq }
}
