import { nextTick, onScopeDispose, ref, watch, type Ref } from 'vue'

/** A line of text is cut when it is wider than its box (a `truncate` box then draws the ellipsis). */
export function textCut(box: { scrollWidth: number; clientWidth: number }): boolean {
  return box.scrollWidth > box.clientWidth
}

/**
 * Whether the element in `el` (a template ref) cuts its one line of text, measured when the element is set, whenever its box changes
 * size and after `text` changes. The element may be swapped for another (a plain name for a button that shows it whole, and back): the
 * new one is measured and watched in its place.
 */
export function useTextCut(el: Ref<HTMLElement | null | undefined>, text: () => string): Ref<boolean> {
  const cut = ref(false)
  const measure = () => {
    const e = el.value
    cut.value = !!e && textCut(e)
  }
  const observer = typeof ResizeObserver === 'undefined' ? undefined : new ResizeObserver(measure)
  watch(
    el,
    (now, was) => {
      if (was) observer?.unobserve(was)
      if (now) observer?.observe(now)
      measure()
    },
    { immediate: true, flush: 'post' },
  )
  watch(text, () => nextTick(measure))
  onScopeDispose(() => observer?.disconnect())
  return cut
}
