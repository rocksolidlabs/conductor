/** What a launch opens with (#303): Launch <agent> here, the `launch` verb; and a clone's (#434, useCloneModal). */
export interface LaunchPrefill {
  agentId?: string
  cwd?: string
  args?: string
  /** The arguments `args` shows, as recorded: launched as they are while `args` is left as it was (#434). */
  argv?: string[]
  yolo?: boolean
  /** Clone with options… (#434): the dialog is titled for the clone of this session, and clones it. */
  clone?: { id: string; name: string; attempt: boolean; /** The source's directory as launched: a clone with the field blank runs there. */ cwd: string }
}

/** Open state of the Launch agent dialog, shared by the sidebar button, the N shortcut and the palette; `prefill` is taken as it opens. */
export function useLaunchModal() {
  const open = useState<boolean>('launchOpen', () => false)
  const prefill = useState<LaunchPrefill | null>('launchPrefill', () => null)
  // Counts the prefills given (#434 review round 1): one given while the dialog is open already (the palette's Clone with options…
  // of another session, over the dialog) is taken as a new opening; a bare show() while it is open changes nothing in it.
  const asked = useState<number>('launchAsked', () => 0)
  // Counts every show() that opens the dialog or gives it a prefill (#434 review round 3): a Clone with options… read asked before
  // a later opening, an ordinary Launch among them, opens nothing over it when it answers.
  const shown = useState<number>('launchShown', () => 0)
  return {
    open,
    prefill,
    asked,
    shown,
    show: (p?: LaunchPrefill) => {
      if (!p && open.value) return
      prefill.value = p ?? null
      if (p) asked.value++
      shown.value++
      open.value = true
    },
  }
}
