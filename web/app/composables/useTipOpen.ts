/**
 * The key last pressed anywhere on the page, cleared by any press of a pointer: a focus that follows a Tab is a person moving through
 * the page by the keyboard; any other focus that lands on a button (a menu or a dialog handing it back as it closes, after an Escape,
 * an Enter or a tap) is the app's own, and opens no tooltip.
 */
let lastKey = ''
if (typeof window !== 'undefined') {
  window.addEventListener('keydown', (e) => (lastKey = e.key), true)
  window.addEventListener('pointerdown', () => (lastKey = ''), true)
}

/**
 * When a button's tooltip opens (docs/design/system.md, Tooltips): while a mouse or a pen rests on the button, and when Tab brings the
 * focus to it; never for a focus the app hands back, and never at a touch. Bind `open` and `onOpen` to a UTooltip (with
 * `ignore-non-keyboard-focus`) and `onPointer` to its button's pointerenter and pointerleave. The tooltip of IconButton and of
 * TipButton.
 */
export function useTipOpen() {
  const open = ref(false)
  /** A mouse or a pen rests on the button (a touch never counts: it has no hover). */
  const hovered = ref(false)

  function onPointer(e: PointerEvent, over: boolean) {
    if (e.pointerType !== 'touch') hovered.value = over
  }

  /** The tooltip asks to open on hover or on focus: it opens for a hover, and for a focus Tab brought; it closes whenever asked. */
  function onOpen(want: boolean) {
    open.value = want && (hovered.value || lastKey === 'Tab')
  }

  return { open, onPointer, onOpen }
}
