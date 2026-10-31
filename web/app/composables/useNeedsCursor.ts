import { needingInput } from '../utils/attention'
import { nextNeeding, pillMs } from '../utils/needsCursor'

/**
 * `]` and `[` (#303): a cursor through the sessions that need you, in the
 * order the sidebar lists them, from the page's own session when it is one
 * of them. Each step shows the pill (NeedsYouPill.vue) for four seconds
 * with the question and its choices; the pill goes on its own, and
 * nothing navigates. From a terminal's Alt twins the pill takes the focus,
 * so the digits typed next are the pill's, never the terminal's.
 */
export interface PillShown {
  id: string
  /** 1-based, of `of`. */
  n: number
  of: number
  /** The question the pill showed (the attention's id): a digit answers that one, and the pill goes when it changes. */
  question?: string
  /** The pill takes the focus (the Alt twins, pressed from a terminal). */
  focus: boolean
  /** A counter the pill restarts its clock on. */
  seq: number
}

export function useNeedsCursor() {
  const attention = useAttention()
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
    const ids = needingInput(attention.sessions.value).map((s) => s.id)
    const page = palette.page.value?.sessionId
    const from = shown.value?.id ?? (page && ids.includes(page) ? page : null)
    const i = nextNeeding(ids, from, delta)
    if (i < 0) return
    seq.value++
    const s = attention.sessions.value.find((x) => x.id === ids[i])
    shown.value = { id: ids[i]!, n: i + 1, of: ids.length, question: s?.attention?.id, focus, seq: seq.value }
    window.clearTimeout(timer)
    timer = window.setTimeout(hide, pillMs())
  }

  return { shown, step, hide }
}
