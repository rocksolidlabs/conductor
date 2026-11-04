/** What a launch opens with (#303): Launch <agent> here, the `launch` verb; and a clone's (#434, useCloneModal). */
export interface LaunchPrefill {
  agentId?: string
  cwd?: string
  args?: string
  /** The arguments `args` shows, as recorded: launched as they are while `args` is left as it was (#434). */
  argv?: string[]
  yolo?: boolean
  /** Clone with options… (#434): the dialog is titled for the clone of this session, and clones it. */
  clone?: { id: string; name: string; attempt: boolean }
}

/** Open state of the Launch agent dialog, shared by the sidebar button, the N shortcut and the palette; `prefill` is taken as it opens. */
export function useLaunchModal() {
  const open = useState<boolean>('launchOpen', () => false)
  const prefill = useState<LaunchPrefill | null>('launchPrefill', () => null)
  return {
    open,
    prefill,
    show: (p?: LaunchPrefill) => {
      prefill.value = p ?? null
      open.value = true
    },
  }
}
