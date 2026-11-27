import type { FileHeader } from './protocol'

/**
 * What the editor knows of each open file's edits (design round 12, F6):
 * the model's version when it was last read or saved, so a model whose
 * version moved on is unsaved. A model lives for its tab, across the tab
 * being switched away from and back; a tab closed without saving forgets
 * its edits.
 */
const savedVersions = new Map<string, number>()

export function markSaved(path: string, version: number): void {
  savedVersions.set(path, version)
}

/** Unsaved: the model's version is not the one last read or saved. A path never marked is not unsaved. */
export function isDirty(path: string, version: number): boolean {
  const v = savedVersions.get(path)
  return v !== undefined && v !== version
}

export function hasModel(path: string): boolean {
  return savedVersions.has(path)
}

/** Forgets a path: its model's text is taken from the next read. */
export function forgetModel(path: string): void {
  savedVersions.delete(path)
}

/**
 * Each open file's model's version now, as its editor saw it last (noteVersion): a model changes only while its editor is up, so this
 * holds while the tab is switched away from too. A save completes against the version it sent (savedAt), not the one on screen when
 * its answer lands: what was typed meanwhile stays, and stays unsaved.
 */
const currentVersions = new Map<string, number>()

export function noteVersion(path: string, version: number): void {
  currentVersions.set(path, version)
}

/**
 * Whether path's model holds edits not saved, as its editor saw it last: true for a tab whose editor is not mounted now (one behind
 * the one in front, a view built again with its tabs kept), so the tab wears its dot, asks before it closes and holds back a keymap's
 * change (UX3 R72).
 */
export function unsavedNow(path: string): boolean {
  const v = currentVersions.get(path)
  return v !== undefined && isDirty(path, v)
}

/**
 * A save of path's model at version was written: that version is the saved one (version undefined: the text sent was not the model's,
 * so the model is forgotten and takes the saved text from the next read). Reports whether the model is unsaved still, edits having
 * been made since that version.
 */
export function savedAt(path: string, version: number | undefined): boolean {
  if (version === undefined) {
    forgetModel(path)
    return false
  }
  markSaved(path, version)
  const now = currentVersions.get(path)
  return now !== undefined && now !== version
}

/** "Changed on disk since you opened it · 08:34:10 · codex (Edit). Saving would overwrite that." */
export function conflictWords(h: Pick<FileHeader, 'mtime' | 'at' | 'by' | 'tool' | 'exists'> | null): string {
  if (!h) return ''
  if (h.exists === false) return 'Deleted on disk since you opened it. Saving would write it again.'
  const when = h.at || h.mtime
  const t = when ? Date.parse(when) : NaN
  const clock = Number.isFinite(t) ? new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }) : ''
  const who = h.by ? `${h.by}${h.tool ? ` (${h.tool})` : ''}` : h.tool || ''
  return `Changed on disk since you opened it${clock ? ` · ${clock}` : ''}${who ? ` · ${who}` : ''}. Saving would overwrite that.`
}

/** The close prompt's words for the tabs with unsaved changes. */
export function closeWords(names: readonly string[]): { title: string; words: string } {
  if (names.length === 1) return { title: `Save ${names[0]}?`, words: "Your changes are lost if you don't save them." }
  return { title: `Close ${names.length} files with unsaved changes?`, words: `${names.join(', ')} have changes that are lost if you don't save them.` }
}
