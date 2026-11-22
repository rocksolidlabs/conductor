// When other machines are shown (docs/plans/hosts.md, PR-5): the web half of the one rule, otherMachinesShown in internal/api, read
// from a reply's `other` field, which the server sends only while a registered sync store has been current in its life and is not
// signed out, and empties of machines whenever the store is not current (or behind past 10 minutes). Every page that shows other
// machines decides by these two; each puts the reason after its own subject ("Other machines are not shown: …"). Pure, no Nuxt import.
import type { SyncState } from './protocol'
import { atWords } from './dayTime'

/** What a reply says of other machines: its `other` field and the time the reply was built. */
export interface MachinesView {
  at?: string
  other?: { state: Exclude<SyncState, 'signed_out'> | string; since?: string } | null
}

/** How long a store may be behind and its machines still be shown, as the server rules. */
export const BEHIND_SHOWN_MS = 10 * 60 * 1000

/**
 * `none` with no `other` field: a plain build's page, nothing said of other machines; `line` while the store is unreachable, lapsed,
 * or behind for 10 minutes or more by the reply's own clock (or since a time it does not say): the page says so in one line; `shown`
 * otherwise.
 */
export function otherMachinesShown(v: MachinesView | null | undefined): 'none' | 'line' | 'shown' {
  const o = v?.other
  if (!o) return 'none'
  if (o.state === 'current') return 'shown'
  if (o.state === 'behind') {
    const at = Date.parse(v?.at ?? '')
    const since = Date.parse(o.since ?? '')
    // As the server rules: behind with no time it began is not shown.
    return !Number.isNaN(at) && !Number.isNaN(since) && at - since < BEHIND_SHOWN_MS ? 'shown' : 'line'
  }
  return 'line'
}

/**
 * Why other machines are not shown, after a page's own subject: "the sync service has not answered since 14:02", "the subscription
 * has lapsed", "the sync service has been behind since 14:02"; '' while they are shown or nothing is said of them.
 */
export function otherMachinesWhy(v: MachinesView | null | undefined): string {
  if (otherMachinesShown(v) !== 'line') return ''
  const o = v!.other!
  const now = Date.parse(v!.at ?? '')
  const at = atWords(o.since, Number.isNaN(now) ? Date.now() : now)
  switch (o.state) {
    case 'lapsed':
      return 'the subscription has lapsed'
    case 'behind':
      return at ? `the sync service has been behind since ${at}` : 'the sync service is behind'
    case 'unreachable':
      return at ? `the sync service has not answered since ${at}` : 'the sync service is not answering'
  }
  return 'the sync service is not current'
}
