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
      // A message the owner refused while the person wrote another comes back once the box is empty (putBack).
      const back = this.back.get(id)
      if (back?.length) {
        const [first, ...rest] = back
        if (rest.length) this.back.set(id, rest)
        else this.back.delete(id)
        this.drafts.set(id, { text: first! })
      }
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

  /** Messages the owner refused (`not_sent`) while the person had begun another line in that box, by session, in order. */
  private readonly back = new Map<string, string[]>()

  /**
   * A message the owner refused (`not_sent`) comes back into its own session's box, as a message bound to no question (UX3 R10): at
   * once when that box is empty, else as soon as the person empties it, one at a time in the order they were refused. None is
   * dropped for a line typed meanwhile, nor for another refused after it.
   */
  putBack(id: string, text: string): void {
    if (!text) return
    if (this.text(id) === '') this.drafts.set(id, { text })
    else this.back.set(id, [...(this.back.get(id) ?? []), text])
  }

  /** How many refused messages wait for the session's box to be emptied. */
  waiting(id: string): number {
    return this.back.get(id)?.length ?? 0
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
    for (const id of [...this.back.keys()]) if (!listed.has(id)) this.back.delete(id)
  }
}

/** What a row's field uses of the sidebar's drafts. */
export type RowDraftsView = Pick<RowDrafts, 'get' | 'text' | 'edit' | 'settle'>

/** The sidebar's row drafts, provided by SessionSidebar to the row fields it draws (SidebarPrompt). */
export const ROW_DRAFTS: InjectionKey<RowDraftsView> = Symbol('rowDrafts')

/**
 * Which boxes hold a line the person typed or pasted themselves (UX3 R10, I19): only such a line may go for the agent to expand. It
 * records, by session, the text the person's own input or own pick left in the box (`typed`); the line is theirs while the box holds
 * exactly that text (`is`). Any change the person did not make (code that writes the box, a line the page puts back, a drop) leaves
 * a text the record does not hold, or ends the record (`set`), so it is the page's line until the person changes it. A send ends it
 * (`sent`). A line kept for a session while the person was away (useBoxDrafts) keeps its record: it is what they typed, never sent.
 * Pure.
 */
export class OwnLines {
  private readonly by = new Map<string, string>()

  typed(id: string, text: string): void {
    if (text === '') this.by.delete(id)
    else this.by.set(id, text)
  }

  set(id: string): void {
    this.by.delete(id)
  }

  sent(id: string): void {
    this.by.delete(id)
  }

  is(id: string, text: string): boolean {
    return text !== '' && this.by.get(id) === text
  }

  /** Drops the sessions no longer listed. */
  keep(ids: Iterable<string>): void {
    const listed = new Set(ids)
    for (const id of [...this.by.keys()]) if (!listed.has(id)) this.by.delete(id)
  }
}
