import type { SessionInfo } from '~/composables/useSessions'
import { clonePrefill, hostedReason, isAttempt, requestOrder } from '~/utils/clone'

/** The session the Clone… dialog is open on (#434): its id, name and directory as the dialog opened, and whether it is an attempt. */
export interface CloneTarget {
  id: string
  name: string
  cwd: string
  attempt: boolean
}

/**
 * The two ways to clone a session (#434), shared by a sidebar row's menu, its sheet on a phone and the palette: Clone… opens the
 * one name dialog, in the layout; Clone with options… reads what the session was launched with and opens the Launch dialog filled
 * in with it. Called in a component's setup, which the handlers then run outside of.
 */
export function useCloneModal() {
  const open = useState<boolean>('cloneOpen', () => false)
  const target = useState<CloneTarget | null>('cloneTarget', () => null)
  // Counts the dialog's openings, as the Rename dialog's do: a reply of an earlier opening leaves a later one alone.
  const opening = useState<number>('cloneOpening', () => 0)
  // Counts Clone with options…'s reads, shared by every caller (#434 review round 2): a read asked before another opens nothing
  // once the later one is asked, whichever answers first. A Launch dialog opened after the read was asked (an ordinary Launch,
  // the launch verb) supersedes it too (#434 review round 3): its answer would replace what is being typed there.
  const reads = requestOrder(useState<number>('cloneRead', () => 0))
  const launch = useLaunchModal()
  const api = useSessions()
  const toast = useToast()
  const refuse = (s: SessionInfo, why: string) => toast.add({ title: `${s.name} cannot be cloned`, description: why, color: 'warning', icon: 'i-lucide-copy' })
  return {
    open,
    target,
    opening,
    show: (s: SessionInfo) => {
      if (s.kind !== 'server') return refuse(s, hostedReason(s.hostName))
      target.value = { id: s.id, name: s.name, cwd: s.cwd, attempt: isAttempt(s) }
      opening.value++
      open.value = true
    },
    withOptions: async (s: SessionInfo) => {
      if (s.kind !== 'server') return refuse(s, hostedReason(s.hostName))
      const read = reads.next()
      const since = launch.shown.value
      const current = () => reads.current(read) && launch.shown.value === since
      try {
        const { launch: l } = await api.get(s.id)
        if (!current()) return
        if (!l) throw new Error('the server did not say what it was launched with')
        launch.show(clonePrefill(s, l))
      } catch (e) {
        if (current()) refuse(s, (e as Error).message)
      }
    },
  }
}
