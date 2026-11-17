import { getCurrentInstance, onBeforeUnmount, ref, watch } from 'vue'
import { TREE_SECRET_NAME, type LabelRead, type PreviewLabel } from '~/utils/capture'

/** What a label is read for: an attempt's preview, its candidate's checkout by `seq`. */
export interface LabelTarget {
  run: string
  task: string
  attempt: number
  seq?: number
}

/** How often the label is read while the page shows. */
export const LABEL_EVERY_MS = 5000

/** The label route of target. */
export function labelPath(t: LabelTarget): string {
  const base = `/api/runs/${encodeURIComponent(t.run)}/tasks/${encodeURIComponent(t.task)}/attempts/${t.attempt}/preview`
  return t.seq ? `${base}?seq=${t.seq}` : base
}

/**
 * The revision label of an attempt's preview (docs/protocol.md, The preview label), read every 5 s and at every move of the run's
 * revision (`revision`, which its run events bring), never while the page is hidden, and at once when it shows again; `read()` asks
 * now, for a selection's moment. `missing` once the route says the preview is not open (`404 no_preview`). Reads overlapping each
 * other are answered in any order: only the last one started is kept.
 */
export function usePreviewLabel(target: () => LabelTarget | null, revision: () => unknown = () => 0) {
  const { request } = useApi()
  const label = ref<PreviewLabel | null>(null)
  const error = ref('')
  const missing = ref(false)
  let started = 0
  let timer: ReturnType<typeof setTimeout> | null = null

  const hidden = () => typeof document !== 'undefined' && document.visibilityState === 'hidden'

  async function read(): Promise<PreviewLabel | null> {
    const t = target()
    if (!t) return null
    const mine = ++started
    try {
      const l = await request<PreviewLabel>(labelPath(t))
      if (mine === started) {
        label.value = l
        error.value = ''
        missing.value = false
      }
      return l
    } catch (e) {
      const err = e as { status?: number; code?: string; message?: string }
      if (mine === started) {
        if (err.status === 404 && err.code === 'no_preview') {
          missing.value = true
          label.value = null
          error.value = ''
        } else error.value = err.message ?? 'the label could not be read'
      }
      return null
    }
  }

  function schedule() {
    if (timer) clearTimeout(timer)
    timer = null
    if (hidden() || !target()) return
    timer = setTimeout(async () => {
      timer = null
      await read()
      schedule()
    }, LABEL_EVERY_MS)
  }

  /** The label's revision read now, as a selection's moment keeps it. */
  async function moment(): Promise<LabelRead | null> {
    const l = await read()
    return l ? { commit: l.commit, content: l.content, changed: l.changed, readAt: l.readAt } : null
  }

  const onVisible = () => {
    if (hidden()) {
      if (timer) clearTimeout(timer)
      timer = null
      return
    }
    void read()
    schedule()
  }

  watch(
    () => [JSON.stringify(target()), revision()] as const,
    () => {
      void read()
      schedule()
    },
    { immediate: true },
  )
  if (typeof document !== 'undefined') document.addEventListener('visibilitychange', onVisible)
  const stop = () => {
    if (timer) clearTimeout(timer)
    timer = null
    if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', onVisible)
  }
  if (getCurrentInstance()) onBeforeUnmount(stop)

  return { label, error, missing, read, moment, stop }
}

/**
 * New tab for an attempt's preview: a preview that names its tree's secret opens at the label's admin URL, the secret put in (read
 * now: the run's read keeps the name); any other at its own address. Opened with no opener and no referrer.
 */
export function usePreviewOpen() {
  const { request } = useApi()
  async function newTab(t: LabelTarget, preview: string) {
    let url = preview
    if (preview.includes(TREE_SECRET_NAME)) {
      try {
        url = (await request<PreviewLabel>(labelPath(t))).url
      } catch {
        return
      }
    }
    window.open(url, '_blank', 'noopener,noreferrer')
  }
  return { newTab }
}
