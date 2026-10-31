import type { NvimEcho } from './nvimEcho'
import type { NvimViewState } from './nvimSwap'
import { closedByQuit } from './nvimLost'

/**
 * A Neovim kept for a tab not in front: its editor id, what its status line showed, and where its cursor was. A Neovim lost (not quit,
 * `utils/nvimLost`) is kept too, `state.lost` its reason: its tab's text is what it last held, until Reopen or a close. `stop` ends the
 * following of its changes into the tab's text while it is held (the editor's `follow`): a change on its way as the tab was left, one a
 * plugin makes later, a lost Neovim's last ones, which can come after its end. `echo` is the local echo as it left, finished: the
 * numbers of the keys sent and acknowledged, and the characters typed that Neovim had not acknowledged, laid over its text and
 * reconciled with each of those changes (NvimEcho.lateLines); the next editor goes on with it.
 */
export interface HeldNvim {
  id: string
  /** The file its Neovim shows, as it names it (its last `opened`). */
  path?: string
  state: NvimViewState
  cursor: { line: number; col: number }
  /** Neovim's cursor as it last said (its line, its byte column), which the cursor shows once no typed character does. */
  at?: { line: number; byteCol: number }
  stop?: () => void
  echo?: NvimEcho | null
}

/**
 * Whether a held Neovim's tab holds changes not written, as the page tells: Neovim said so, characters typed it has not had show, or
 * keys refused on their way are not dismissed.
 */
export function heldChanges(h: HeldNvim): boolean {
  return h.state.modified || !!h.echo?.showing || !!h.state.refused
}

/**
 * Keys refused on their way to a held Neovim (an `error` with their `seq` as `ack`, `text` the end it did not take): noted against the
 * echo as it left (their guess comes off the text, they are named) and said on the tab's status line when it is in front, its Neovim
 * kept until then. Not once its Neovim is lost, as in front: keys refused after its end (its editor let go) are among those it never
 * had, which the tab keeps over its text and names (unconfirmed); taken off, one was gone from the tab (#429). Returns whether the
 * hold changed.
 */
export function heldRefusal(h: HeldNvim, echo: NvimEcho | null | undefined, ev: { ack: number; text?: string; message?: string; code?: string }): boolean {
  if (h.state.lost) return false
  const keys = echo?.noteRefused(ev.ack, ev.text) ?? ev.text ?? ''
  h.state.refused = (h.state.refused ?? '') + keys
  h.state.message = `Keys did not reach Neovim (${ev.message ?? ev.code ?? 'refused'}): ${h.state.refused}`
  h.state.messageKind = 'emsg'
  return true
}

/**
 * The Neovims of tabs not in front (the Neovim keymap). A tab whose buffer holds changes not written keeps its Neovim while another
 * tab is in front: the editor hands it here as it leaves and takes it back when its tab is in front again, so the changes stay where
 * they were typed. Closing it instead ended it as a lost connection does, and the next open of the file stumbled on its swap file.
 * A tab left while keys Neovim has not said it has done are on their way is kept too: they may be making a change Neovim has not said
 * yet (its follower lets it go once they are done, if they changed nothing). A tab that closes takes its held Neovim with it (`orphans`: the person said Don't save, or Neovim kept nothing).
 * Keyed by the file's path; one per page connection (`useEditorBridge`). A Neovim lost stays held as lost, whether it was in front
 * (its editor hands it here as it leaves, a tab switched or the area folded) or not (`ended`), so the next showing keeps its text.
 * Whatever leaves the holds stops being followed. `listen` is told of each change to a hold (the area keeps its tab's unsaved dot).
 */
export class NvimHolds {
  private held = new Map<string, HeldNvim>()
  private listeners = new Set<(path: string, h: HeldNvim | undefined) => void>()

  /** cb is told, with the path, of every hold made, changed (`changed`) or let go (undefined then); the returned function stops it. */
  listen(cb: (path: string, h: HeldNvim | undefined) => void): () => void {
    this.listeners.add(cb)
    return () => this.listeners.delete(cb)
  }
  private tell(path: string) {
    const h = this.held.get(path)
    for (const cb of this.listeners) cb(path, h)
  }
  hold(path: string, h: HeldNvim): void {
    const was = this.held.get(path)
    if (was && was !== h) was.stop?.()
    this.held.set(path, h)
    this.tell(path)
  }
  /** The hold for path changed in place (its follower: Neovim's word on changes not written, a late change). */
  changed(path: string): void {
    if (this.held.has(path)) this.tell(path)
  }
  /** The Neovim held for path, left held. */
  peek(path: string): HeldNvim | undefined {
    return this.held.get(path)
  }
  /** The Neovim held for path, no longer held nor followed: its editor follows it again. */
  take(path: string): HeldNvim | undefined {
    const h = this.held.get(path)
    if (!h) return undefined
    this.held.delete(path)
    h.stop?.()
    this.tell(path)
    return { id: h.id, path: h.path, state: h.state, cursor: h.cursor, at: h.at, echo: h.echo }
  }
  /**
   * Neovim id ended on its own: quit, nothing to take back; lost (reason `error: …`), its tab keeps what it held, as lost, still
   * following the changes it sent before its end.
   */
  ended(id: string, reason?: string): void {
    for (const [path, h] of this.held) {
      if (h.id !== id || h.state.lost) continue
      if (closedByQuit(reason)) {
        h.stop?.()
        this.held.delete(path)
      } else {
        h.echo?.markLost()
        const unconfirmed = h.echo?.unconfirmed || undefined
        this.held.set(path, { ...h, state: { ...h.state, lost: reason, cmdline: '', unconfirmed, modified: h.state.modified || !!h.echo?.showing || !!unconfirmed } })
      }
      this.tell(path)
    }
  }
  /**
   * The held Neovims whose file has no tab open now, no longer held: the caller ends them, but for a lost one, which has none to end.
   * `seq`: keys up to it were sent, which it may not have done yet (`unsettled`; its echo's last number, 0 when none).
   */
  orphans(open: Iterable<string>): Array<{ path: string; id: string; lost: boolean; seq: number; unsettled: boolean }> {
    const keep = new Set(open)
    const out: Array<{ path: string; id: string; lost: boolean; seq: number; unsettled: boolean }> = []
    for (const [path, h] of this.held) {
      if (keep.has(path)) continue
      h.stop?.()
      out.push({ path, id: h.id, lost: !!h.state.lost, seq: h.echo?.seq ?? 0, unsettled: !!h.echo?.unsettled })
      this.held.delete(path)
      this.tell(path)
    }
    return out
  }
  /** Whether a Neovim kept for a tab, not lost, has keys it has not said it has done. */
  busy(): boolean {
    for (const h of this.held.values()) if (!h.state.lost && h.echo?.unsettled) return true
    return false
  }
  clear(): void {
    const paths = [...this.held.keys()]
    for (const h of this.held.values()) h.stop?.()
    this.held.clear()
    for (const path of paths) this.tell(path)
  }
  get size(): number {
    return this.held.size
  }
}
