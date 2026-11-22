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
 * the person empties the field, or when its session is no longer listed: at most one per session the sidebar lists. Pure.
 */
export class RowDrafts {
  private readonly drafts = new Map<string, RowDraft>()

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
   * What became of the line `line` sent for the session: sent, it leaves, if the field still holds it; refused, it stays, bound to
   * its question until its next change; held (it never reached the owner), nothing changes.
   */
  settle(id: string, line: string, outcome: ReplyOutcome): void {
    const d = this.drafts.get(id)
    if (outcome === 'held' || d?.text !== line) return
    if (outcome === 'sent') this.drafts.delete(id)
    else this.drafts.set(id, { ...d, kept: true })
  }

  /** Drops the lines of sessions no longer listed. */
  keep(ids: Iterable<string>): void {
    const listed = new Set(ids)
    for (const id of [...this.drafts.keys()]) if (!listed.has(id)) this.drafts.delete(id)
  }
}

/** What a row's field uses of the sidebar's drafts. */
export type RowDraftsView = Pick<RowDrafts, 'get' | 'text' | 'edit' | 'settle'>

/** The sidebar's row drafts, provided by SessionSidebar to the row fields it draws (SidebarPrompt). */
export const ROW_DRAFTS: InjectionKey<RowDraftsView> = Symbol('rowDrafts')
