/**
 * The places of the seeded day already under their contrast floor when the drawn walk (contrast.spec.ts) landed, each with the row
 * of the UX review that removes it. The list only shrinks: a page's test fails on an entry for its own page, theme, width and pass
 * that its walk did not meet, so the row that fixes a place deletes its entry, and a later row that redraws a walked page keeps the
 * spec green by editing this list, never by adding to it what it drew itself. A failure the stream's own tokens caused is fixed in
 * main.css, not listed.
 */

export type WalkedPage = 'yard' | 'session' | 'needs' | 'agents' | 'dispatch' | 'run' | 'hosts'

export interface Known {
  page: WalkedPage
  theme: 'dark' | 'light'
  width: 1440 | 390
  /** At rest, or with the page's first row hovered (its walk is of that row alone). */
  at: 'rest' | 'hover'
  /** A part of the failure's `data-*` path (contrast.ts), enough to name the element. */
  path: string
  /** Why it is under its floor. */
  reason: string
  /** The row of the plan (docs/plans/ux-review.md) that removes it. */
  removedBy: string
}

// Empty since PR-26 drew the unavailable agent card and the exited sidebar row with muted words instead of fading them.
export const KNOWN: Known[] = []
