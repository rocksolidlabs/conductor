import type { InjectionKey } from 'vue'
import { bindDraft, type ReplyOutcome } from './reply'

/** A row's reply line: its text, the question it was begun for, and whether it came back refused (bound anew at its next change). */
export interface RowDraft {
  text: string
  question?: string
  kept?: boolean
}

/**
 * The sidebar rows' reply lines, by session (#654): kept by the sidebar rather than by the field that draws one, so that a line and
 * the question it was typed for outlive the field going (its prompt cleared as answered, the row moving between the sidebar's
 * lists) and are shown again when a field next shows for the session, nothing sent meanwhile. A line leaves when it is sent, when
 * the person empties the field, or when its session is no longer listed: at most one per session the sidebar lists, and one on its
 * way. Pure.
 */
export class RowDrafts {
  private readonly drafts = new Map<string, RowDraft>()
  /** A line that left the page, by session, until the word on it: put back if it is refused. */
  private readonly away = new Map<string, RowDraft>()

  get(id: string): RowDraft | undefined {
    return this.drafts.get(id)
  }

  /** What the field shows for the session. */
  text(id: string): string {
    return this.drafts.get(id)?.text ?? ''
  }

  /**
   * The person's edit of the session's line, `current` the question the row shows: bound as bindDraft binds (to the question shown
   * as the line is begun, or at the first change of a line that came back refused); an emptied field drops the line.
   */
  edit(id: string, after: string, current: string | undefined): void {
    const d = this.drafts.get(id)
    if (after === '') {
      this.drafts.delete(id)
      return
    }
    this.drafts.set(id, { text: after, question: bindDraft(d?.text ?? '', after, d?.question, current, d?.kept) })
  }

  /**
   * What became of the line `line` sent for the session (ReplyOutcome): left, it leaves the field, if the field still holds it, and
   * is kept aside with its question; refused, it comes back into the field if the field is empty (else the person's newer text
   * stays), bound to its question until its next change; sent, it is let go; held (it never left the page), nothing changes.
   */
  settle(id: string, line: string, outcome: ReplyOutcome): void {
    if (outcome === 'held') return
    const d = this.drafts.get(id)
    const aside = this.away.get(id)
    if (outcome === 'left') {
      if (d?.text !== line) return
      this.away.set(id, { text: line, question: d.question })
      this.drafts.delete(id)
      return
    }
    this.away.delete(id)
    if (outcome === 'sent') {
      if (!aside && d?.text === line) this.drafts.delete(id)
      return
    }
    if (aside?.text === line && !d) this.drafts.set(id, { ...aside, kept: true })
    else if (d?.text === line) this.drafts.set(id, { ...d, kept: true })
  }

  /**
   * The line bound to `from` is bound to `to` now (UX3 R5): a line begun while its question had no id yet takes the id when it comes.
   * Any other line is left as it is.
   */
  rebind(id: string, from: string, to: string): void {
    const d = this.drafts.get(id)
    if (d && d.question === from) this.drafts.set(id, { ...d, question: to })
  }

  /** The line is bound to no question now (UX3 R5): its question was already answered from this page, and it goes as a message. */
  free(id: string): void {
    const d = this.drafts.get(id)
    if (d) this.drafts.set(id, { text: d.text })
  }

  /** Drops the lines of sessions no longer listed. */
  keep(ids: Iterable<string>): void {
    const listed = new Set(ids)
    for (const id of [...this.drafts.keys()]) if (!listed.has(id)) this.drafts.delete(id)
    for (const id of [...this.away.keys()]) if (!listed.has(id)) this.away.delete(id)
  }
}

/** What a row's field uses of the sidebar's drafts. */
export type RowDraftsView = Pick<RowDrafts, 'get' | 'text' | 'edit' | 'settle'>

/** The sidebar's row drafts, provided by SessionSidebar to the row fields it draws (SidebarPrompt). */
export const ROW_DRAFTS: InjectionKey<RowDraftsView> = Symbol('rowDrafts')
