import { needingInput } from '../utils/attention'
import { nextNeeding, pillMs } from '../utils/needsCursor'
import { unanswerable } from '../utils/needsYou'

/**
 * `]` and `[` (#303): a cursor through the sessions that need you, in the
 * order the sidebar lists them, from the page's own session when it is one
 * of them. Each step shows the pill (NeedsYouPill.vue) for four seconds
 * with the question and its choices; the pill goes on its own, and
 * nothing navigates. From a terminal's Alt twins the pill takes the focus,
 * so the digits typed next are the pill's, never the terminal's. After the
 * sessions come the open durable decisions no shown question mirrors (a
 * gate's, Program item DR1), a stopped run's too, each once: the pill shows
 * its question, and Enter opens its card. It steps through what the one
 * list (useNeedsYou) says can be answered here, and says how many more wait
 * in Needs you (plans and tasks, answered on the page).
 */
export interface PillShown {
  /** The session's id, or the decision's (`decision`) for a decision of its own. */
  id: string
  /** A durable decision no session question mirrors: its id (`dr-7`). */
  decision?: string
  /** 1-based, of `of`. */
  n: number
  of: number
  /** The items of the one list the pill cannot answer (plans and tasks): "2 of 4 · 2 more in Needs you". */
  more: number
  /** The question the pill showed (the attention's id): a digit answers that one, and the pill goes when it changes. */
  question?: string
  /** The pill takes the focus (the Alt twins, pressed from a terminal). */
  focus: boolean
  /** A counter the pill restarts its clock on. */
  seq: number
}

export function useNeedsCursor() {
  const attention = useAttention()
  const needs = useNeedsYou()
  const palette = usePalette()
  const shown = useState<PillShown | null>('needsPill', () => null)
  const seq = useState<number>('needsPillSeq', () => 0)
  let timer: number | undefined

  function hide() {
    window.clearTimeout(timer)
    timer = undefined
    shown.value = null
  }

  /** One step: the next (1) or the previous (-1) session that needs you; nothing with none waiting. */
  function step(delta: 1 | -1, focus = false) {
    // The one list's answerable items: its questions in the sidebar's order (the newest asked first), then its decisions.
    const lone = needs.answerable.value.flatMap((it) => (it.kind === 'decision' && it.decision ? [it.decision.id] : []))
    const ids = [...needingInput(attention.sessions.value).map((s) => s.id), ...lone]
    const more = unanswerable(needs.items.value)
    const page = palette.page.value?.sessionId
    const from = shown.value?.id ?? (page && ids.includes(page) ? page : null)
    const i = nextNeeding(ids, from, delta)
    if (i < 0) return
    seq.value++
    const id = ids[i]!
    if (lone.includes(id)) shown.value = { id, decision: id, n: i + 1, of: ids.length, more, focus, seq: seq.value }
    else shown.value = { id, n: i + 1, of: ids.length, more, question: attention.sessions.value.find((x) => x.id === id)?.attention?.id, focus, seq: seq.value }
    window.clearTimeout(timer)
    timer = window.setTimeout(hide, pillMs())
  }

  return { shown, step, hide }
}
