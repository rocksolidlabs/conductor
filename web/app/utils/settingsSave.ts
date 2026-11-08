/**
 * The Settings page's one Save (#707): Brakeman's configuration, the server's own (PUT /api/brakeman/config), and the desktop app's
 * settings (the bridge), whichever of the two was changed. Brakeman's part goes first: the app's part may restart the server, and the
 * app then loads the page on the new server's port, so a part left for after it would never be sent. A part refused stops the save
 * there: the app's part is not sent after Brakeman's was refused, so no restart takes edits that were not saved, and every edit stays
 * in its form for the next Save.
 */
export interface SaveOutcome {
  brakeman: 'unchanged' | 'saved' | 'failed'
  /** `held`: not sent, since Brakeman's part before it was refused. */
  app: 'unchanged' | 'saved' | 'restarted' | 'failed' | 'held'
  /** The refusal's words, when a part failed. */
  error?: string
}

/** Runs the parts given (null for one unchanged, which is never sent: a Brakeman save drops its pending work, #721) in order. */
export async function saveParts(parts: { brakeman: (() => Promise<void>) | null; app: (() => Promise<'saved' | 'restarted'>) | null }): Promise<SaveOutcome> {
  const out: SaveOutcome = { brakeman: parts.brakeman ? 'saved' : 'unchanged', app: parts.app ? 'held' : 'unchanged' }
  if (parts.brakeman) {
    try {
      await parts.brakeman()
    } catch (e) {
      return { ...out, brakeman: 'failed', error: (e as Error).message }
    }
  }
  if (parts.app) {
    try {
      out.app = await parts.app()
    } catch (e) {
      return { ...out, app: 'failed', error: (e as Error).message }
    }
  }
  return out
}

/** What the page says of a save: which part was saved and which was not, and that the edits are kept. */
export function saveWords(o: SaveOutcome): { ok: boolean; title: string; description?: string } {
  const why = (o.error ?? '').replace(/\.$/, '')
  if (o.brakeman === 'failed') {
    const app = o.app === 'held' ? " The app's settings were held back with them, so nothing changed." : ''
    return { ok: false, title: 'Not saved', description: `Brakeman's settings were not saved: ${why}.${app} Your edits are kept.` }
  }
  if (o.app === 'failed') {
    if (o.brakeman === 'saved') return { ok: false, title: 'Saved in part', description: `Brakeman's settings were saved. The app's settings were not: ${why}. Your edits are kept.` }
    return { ok: false, title: 'Not saved', description: o.error }
  }
  return { ok: true, title: o.app === 'restarted' ? 'Saved; the server restarted' : 'Saved' }
}
