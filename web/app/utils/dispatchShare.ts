// A project's share in words (docs/plans/sync-seams.md, OS-1b, OS-4). Pure.
import type { DispatchProject } from '~/composables/useDispatch'

/** The share's state and words, as the project page says them. */
export function shareWords(p: Pick<DispatchProject, 'key' | 'share' | 'heldBy'>): string {
  const held = p.heldBy
    ? ` ${p.heldBy.rows === 1 ? '1 change' : `${p.heldBy.rows} changes`} of another machine's ${p.heldBy.key} wait for this key: give this project another key to let them in.`
    : ''
  const s = p.share
  if (!s) return `${p.key} is on this machine alone.${held}`
  const tail = s.words ? `: ${s.words}` : ''
  switch (s.state) {
    case 'pending':
    case 'completing':
      return `${p.key} is being shared${tail}.${held}`
    case 'shared':
      return `${p.key} is shared with your other machines${tail}.${held}`
    case 'dormant':
      return `${p.key} was shared and is on this machine alone now${tail}.${held}`
    case 'refused':
      return `${p.key} could not be shared${tail}.${held}`
  }
  return `${p.key}${tail}.${held}`
}
