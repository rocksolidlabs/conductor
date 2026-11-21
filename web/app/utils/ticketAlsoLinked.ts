// A ticket linked to a tracker's item another ticket of a shared project is linked to as well (docs/plans/sync-seams.md, OS-1a's
// alsoLinked, OS-4's words): both are kept, never folded, and the ticket's page says so. Pure.

/** The page's words for a ticket's `alsoLinked` keys; '' for none. */
export function alsoLinkedWords(keys: readonly string[] | undefined): string {
  const ks = (keys ?? []).filter(Boolean)
  if (!ks.length) return ''
  const named = ks.length === 1 ? `${ks[0]} is` : `${ks.slice(0, -1).join(', ')} and ${ks[ks.length - 1]} are`
  return `${named} linked to the same GitHub issue, imported on another machine: keep one, and unlink or close the other${ks.length > 1 ? 's' : ''}`
}
