import type { Ref } from 'vue'

const frame = () => new Promise<void>((done) => requestAnimationFrame(() => done()))

/** A frame drawn, then every animation inside `el` ended. */
async function settled(el: HTMLElement) {
  await frame()
  await Promise.all(el.getAnimations({ subtree: true }).map((a) => a.finished.catch(() => undefined)))
}

/**
 * A dialog whose foot stays still while it is open: `pin` (once it has opened) fixes its top and height where the modal centred it,
 * so that what appears inside it on its own later (a folder's line, a check's words) scrolls its body and never moves its buttons
 * under a pointer already on its way to them (the pointer would go down on a button and come up beside it: no press). What the
 * person opens in it (a fold, a field) calls `regrow`: the dialog takes its contents' height again, downward to the window's bottom,
 * then holds it. `inner` is an element inside the dialog's content.
 */
export function useStillDialog(inner: Ref<HTMLElement | null | undefined>, open: Ref<boolean>) {
  let pinned: HTMLElement | null = null

  async function pin() {
    pinned = null
    // The dialog's content once it is drawn, then its opening animation ended: its box is then where the modal centred it.
    for (let i = 0; i < 120 && open.value && !inner.value; i++) await frame()
    const content = inner.value?.closest<HTMLElement>('[data-slot="content"]')
    if (!content) return
    await settled(content)
    if (!open.value || !content.isConnected) return
    const box = content.getBoundingClientRect()
    const top = Math.round(box.top)
    content.style.top = `${top}px`
    content.style.setProperty('--tw-translate-y', '0px')
    content.style.maxHeight = `calc(100dvh - ${top}px - 1rem)`
    content.style.height = `${Math.ceil(box.height)}px`
    pinned = content
  }

  async function regrow() {
    const content = pinned
    if (!content?.isConnected) return
    content.style.removeProperty('height')
    await settled(content)
    if (!open.value || !content.isConnected || pinned !== content) return
    content.style.height = `${Math.ceil(content.getBoundingClientRect().height)}px`
  }

  return { pin, regrow }
}
