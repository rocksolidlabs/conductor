import type { NvimSwapChoice, NvimSwapInfo } from './protocol'

/** What the editor shows of its Neovim: the mode, the command line, the last message, and a swap file found (recovered once its text was read in). */
export interface NvimViewState {
  mode: string
  cmdline: string
  message: string
  messageKind: string
  swap: NvimSwapInfo | null
  recovered: boolean
  /** The buffer holds changes not written: the tab wears its dot, and closing it asks first. */
  modified: boolean
  /** Why Neovim ended when the person did not quit it (`utils/nvimLost`): the tab stays, read only, with Reopen. */
  lost?: string
  /** Keys sent that the lost Neovim may never have had (NvimEcho.unconfirmed): shown beside its text. */
  unconfirmed?: string
  /** Keys sent that Neovim has not said it has done: the keymap waits for them. */
  unsettled?: boolean
  /** Keys refused on their way while the tab was not in front, not yet shown: its Neovim is kept until they are. */
  refused?: string
}

/**
 * The editor's banner for another editor's swap file (round 13, G3): the
 * file opened read-only under the Neovim keymap, the words say whose swap
 * file it is, and the choices are what applies: Edit anyway always; Recover
 * (only when the swap holds unsaved changes) and Delete the swap file once
 * the process that wrote it is gone. Pure.
 */
export function swapWords(s: NvimSwapInfo, recovered = false): string {
  if (recovered) return 'Recovered the swap file\'s text: write the file (:w) to keep it, then delete the swap file.'
  const who = [s.pid ? `process ${s.pid}` : '', s.user && s.host ? `${s.user} on ${s.host}` : s.host ?? ''].filter(Boolean).join(', ')
  const state = s.running ? 'still running' : 'no longer running'
  const changes = s.modified ? ', with changes not written' : ''
  if (s.running) return `Another Vim (${who ? `${who}, ` : ''}${state}) has this file open. Opened read-only.`
  return `A swap file from a Vim that ended (${who ? `${who}, ` : ''}${state}${changes}) was found. Opened read-only.`
}

/** The choices the banner offers, in order. */
export function swapChoices(s: NvimSwapInfo, recovered = false): Array<{ choice: NvimSwapChoice; label: string }> {
  if (recovered) return [{ choice: 'delete', label: 'Delete the swap file' }]
  const out: Array<{ choice: NvimSwapChoice; label: string }> = [{ choice: 'edit', label: 'Edit anyway' }]
  if (!s.running) {
    if (s.modified) out.push({ choice: 'recover', label: 'Recover' })
    out.push({ choice: 'delete', label: 'Delete the swap file' })
  }
  return out
}
