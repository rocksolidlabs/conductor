import { watch, type Ref } from 'vue'

/** What the gate reads of the workbench token's store (useWorkbenchToken). */
interface Ask {
  needsToken: Ref<boolean>
  kept: () => string
  cancel: () => void
}

/**
 * The workbench token gate's open state (WorkbenchTokenGate.vue), kept with the store's ask from the moment the gate mounts: a gate
 * that mounts while a token is needed (the ask stood while a page of another layout was shown) opens at once, a later refusal opens
 * it, and a token the server has since accepted (a link's) closes it. Its draft starts from the token this browser keeps, never from
 * one only this page holds. Closed (Cancel, Escape, outside it), the ask ends, so the next refusal opens the gate again.
 */
export function followAsk(open: Ref<boolean>, draft: Ref<string>, admin: Ask) {
  watch(open, (v) => {
    if (v) draft.value = admin.kept()
    else admin.cancel()
  })
  watch(
    () => admin.needsToken.value,
    (v) => {
      open.value = v
    },
    { immediate: true },
  )
}
