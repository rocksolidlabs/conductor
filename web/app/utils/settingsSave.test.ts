import { describe, expect, it } from 'vitest'
import type { DesktopSettings } from './desktop'
import { ReplyOrder, followSaved, formMove, holds, holdsFields, saveParts, saveWords, together, type AppPart } from './settingsSave'

/** A part that records its call in `calls` and answers `value`, or throws `fail`. */
function part<T>(calls: string[], name: string, value: T, fail?: string) {
  return async () => {
    calls.push(name)
    if (fail) throw new Error(fail)
    return value
  }
}
/** The app's part: answers `value` or throws `fail`; asked after a refusal, says the app kept the settings (`kept`), or cannot tell. */
function app(calls: string[], value: 'saved' | 'restarted', fail?: string, kept: boolean | 'unread' = false): AppPart {
  return {
    send: part(calls, 'app', value, fail),
    kept: async () => {
      calls.push('kept')
      if (kept === 'unread') throw new Error('the bridge did not answer')
      return kept
    },
  }
}

const settings: DesktopSettings = { dataDir: '/d', allowedRoots: ['/r'], defaultCwd: '/r', yolo: false, reach: 'off', closeToTray: true, wslDistro: '', wslWindowsHome: false, switchyardEnabled: false, switchyardServer: '', switchyardToken: '', switchyardName: '' }

describe('saveParts', () => {
  it("saves Brakeman's part before the app's, which may restart the server and move the page", async () => {
    const calls: string[] = []
    const out = await saveParts({ brakeman: part(calls, 'brakeman', undefined), app: app(calls, 'restarted') })
    expect(calls).toEqual(['brakeman', 'app'])
    expect(out).toEqual({ brakeman: 'saved', app: 'restarted' })
  })

  it('sends no part that is unchanged', async () => {
    const calls: string[] = []
    expect(await saveParts({ brakeman: part(calls, 'brakeman', undefined), app: null })).toEqual({ brakeman: 'saved', app: 'unchanged' })
    expect(await saveParts({ brakeman: null, app: app(calls, 'saved') })).toEqual({ brakeman: 'unchanged', app: 'saved' })
    expect(calls).toEqual(['brakeman', 'app'])
  })

  it("holds the app's part back when Brakeman's is refused, so no restart follows a refusal", async () => {
    const calls: string[] = []
    const out = await saveParts({ brakeman: part(calls, 'brakeman', undefined, 'Saved elsewhere meanwhile.'), app: app(calls, 'restarted') })
    expect(calls).toEqual(['brakeman'])
    expect(out).toEqual({ brakeman: 'failed', app: 'held', error: 'Saved elsewhere meanwhile.' })
  })

  it("keeps Brakeman's part saved when the app's fails after it", async () => {
    const calls: string[] = []
    const out = await saveParts({ brakeman: part(calls, 'brakeman', undefined), app: app(calls, 'saved', 'the data directory must be absolute') })
    expect(calls).toEqual(['brakeman', 'app', 'kept'])
    expect(out).toEqual({ brakeman: 'saved', app: 'failed', error: 'the data directory must be absolute' })
  })

  it("tells a restart that failed after the app kept the settings from a refusal, and one it cannot read from both (Codex's review)", async () => {
    // The app keeps the settings before it restarts the server (desktop/src/save.ts): its refusal then says nothing of what it kept.
    expect(await saveParts({ brakeman: null, app: app([], 'restarted', 'the server did not start', true) })).toEqual({ brakeman: 'unchanged', app: 'unrestarted', error: 'the server did not start' })
    expect(await saveParts({ brakeman: part([], 'brakeman', undefined), app: app([], 'restarted', 'the server did not start', 'unread') })).toEqual({
      brakeman: 'saved',
      app: 'unknown',
      error: 'the server did not start',
    })
  })
})

describe('holds', () => {
  it("compares the fields the page edits, not the app's own", () => {
    expect(holds({ ...settings, noticed: ['publishing'] }, settings)).toBe(true)
    expect(holds({ ...settings, yolo: true }, settings)).toBe(false)
    expect(holds({ ...settings, allowedRoots: ['/r', '/s'] }, settings)).toBe(false)
  })
})

describe('together', () => {
  const card = (cwdChanged: boolean) => ({ cwdChanged })

  it("refuses Brakeman's settings with a change that moves the server off the data directory they are kept in (the review's first finding)", () => {
    expect(together(settings, { ...settings, dataDir: '/e' }, card(false))).toBe(
      "Brakeman's settings are kept in the server's data directory, and this Save moves the server off it: save the data directory alone first, then Brakeman's settings",
    )
    expect(together(settings, { ...settings, wslDistro: 'Debian' }, card(false))).toContain('save the WSL distribution alone first')
    // Alone, either is saved.
    expect(together(settings, { ...settings, dataDir: '/e' }, null)).toBe('')
    expect(together(settings, settings, card(true))).toBe('')
  })

  it("refuses Brakeman's folder with a change of the roots it is checked against (the review's second finding)", () => {
    expect(together(settings, { ...settings, allowedRoots: ['/r', '/s'] }, card(true))).toBe(
      "Brakeman's folder is checked against the allowed roots the server has now, and this Save changes them: save the allowed roots alone first, then Brakeman's folder",
    )
    // The roots with Brakeman's other settings, or Brakeman's folder with the app's other settings: saved together.
    expect(together(settings, { ...settings, allowedRoots: ['/r', '/s'] }, card(false))).toBe('')
    expect(together(settings, { ...settings, yolo: true, defaultCwd: '/r/a' }, card(true))).toBe('')
  })

  it("refuses Brakeman's folder with the Windows profile switch, which adds or takes away a root the list does not name (Codex's second round)", () => {
    const on = { ...settings, wslWindowsHome: true }
    expect(together(on, { ...on, wslWindowsHome: false }, card(true))).toBe(
      "Brakeman's folder is checked against the roots the server allows now, and this Save adds or takes away your Windows profile under /mnt among them: save that switch alone first, then Brakeman's folder",
    )
    expect(together(settings, on, card(true))).toContain('save that switch alone first')
    // The switch with Brakeman's other settings: saved together.
    expect(together(on, { ...on, wslWindowsHome: false }, card(false))).toBe('')
  })
})

describe('followSaved', () => {
  it('sets every field from the first read', () => {
    const form = { ...settings, yolo: true }
    followSaved(form, null, settings)
    expect(form).toEqual(settings)
  })

  it("keeps a field changed while the save was out, for the next Save (Codex's review)", () => {
    const sent = { ...settings, closeToTray: false }
    // Sent with the tray switch off; yolo turned on while the save was out.
    const form = { ...sent, yolo: true }
    followSaved(form, sent, { ...sent, reach: 'auto' })
    expect(form).toEqual({ ...sent, reach: 'auto', yolo: true })
  })
})

// The one rule (utils/settingsSave.ts): what the person typed after the click is theirs, on each path that answers the form.
describe('what answers an edit', () => {
  /** Brakeman's form, as its card holds it: nested fields, the revision read. */
  const conf = (reads: number, rev: number) => ({ rev, cwd: '/srv', budget: { turnMinutes: 15, readsPerTurn: reads }, scope: { repos: ['/srv/app'] } })

  it("a save's answer: a field changed back while the save was out keeps its edit, against the revision saved (Codex's second round)", () => {
    // Read at 300, saved at 400; 300 typed again while the save was out.
    const sent = conf(400, 1)
    const form = conf(300, 1)
    const followed = followSaved(form, sent, conf(400, 2), conf(300, 1))
    expect(form).toEqual(conf(300, 2))
    expect(followed).toEqual(['rev', 'cwd', 'scope'])
    // Nothing typed meanwhile: the form is what was saved, a copy, never the answer's own objects.
    const untouched = conf(400, 1)
    const saved = conf(400, 2)
    followSaved(untouched, sent, saved)
    expect(untouched).toEqual(saved)
    expect(untouched.budget).not.toBe(saved.budget)
  })

  it("a save the app may answer by moving the page holds the fields from the click; Brakeman's alone leaves them open (Codex's second round)", () => {
    // The app restarts the server and loads the page on its new port, another origin: nothing typed meanwhile could follow it.
    expect(holdsFields({ ...settings, yolo: true })).toBe(true)
    expect(holdsFields(null)).toBe(false)
  })

  it("the configuration's watcher: a form is kept while its own save is out, changed back or not (Codex's second round)", () => {
    // Changed back to what it was set from while its save was out: the store's answer, taken first, would otherwise set it.
    expect(formMove({ gen: 1, edited: false, saving: true }, 1, true)).toBe('keep')
    expect(formMove({ gen: 1, edited: false, saving: false }, 1, true)).toBe('set')
    // A new token's form is the new server's, whatever save is out.
    expect(formMove({ gen: 0, edited: false, saving: true }, 1, true)).toBe('set')
    expect(formMove({ gen: 0, edited: true, saving: true }, 1, false)).toBe('drop')
  })

  it("never keeps a form set under the token before, though the new server's revision is the same", () => {
    // Settings' form, edited, read at revision 2 of the first server; a reset, then the second server's configuration, also revision 2.
    expect(formMove({ gen: 0, edited: true }, 1, true)).toBe('set')
    expect(formMove({ gen: 0, edited: false }, 1, true)).toBe('set')
    // Before the second server has answered, the first one's form is not left to save.
    expect(formMove({ gen: 0, edited: true }, 1, false)).toBe('drop')
    // Under one token, a form follows what is saved until it is changed here.
    expect(formMove(null, 1, true)).toBe('set')
    expect(formMove({ gen: 1, edited: false }, 1, true)).toBe('set')
    expect(formMove({ gen: 1, edited: true }, 1, true)).toBe('keep')
    expect(formMove(null, 1, false)).toBe('keep')
    expect(formMove({ gen: 1, edited: true }, 1, false)).toBe('keep')
  })

  it("a reply that arrives after one asked later is dropped (Codex's second round)", () => {
    const order = new ReplyOrder()
    const older = order.ask()
    const fresh = order.ask()
    expect(order.take(fresh)).toBe(true)
    expect(order.take(older)).toBe(false)
    // Asked after the one taken: taken.
    expect(order.take(order.ask())).toBe(true)
  })
})

describe('saveWords', () => {
  it('says saved, and restarted when the server was', () => {
    expect(saveWords({ brakeman: 'saved', app: 'unchanged' })).toEqual({ ok: true, title: 'Saved' })
    expect(saveWords({ brakeman: 'saved', app: 'saved' })).toEqual({ ok: true, title: 'Saved' })
    expect(saveWords({ brakeman: 'unchanged', app: 'restarted' })).toEqual({ ok: true, title: 'Saved; the server restarted' })
    expect(saveWords({ brakeman: 'saved', app: 'restarted' })).toEqual({ ok: true, title: 'Saved; the server restarted' })
  })

  it('says which part was saved and which was not, and that the edits are kept', () => {
    expect(saveWords({ brakeman: 'saved', app: 'failed', error: 'the data directory must be absolute' })).toEqual({
      ok: false,
      title: 'Saved in part',
      description: "Brakeman's settings were saved. The app's settings were not: the data directory must be absolute. Your edits are kept.",
    })
    expect(saveWords({ brakeman: 'failed', app: 'held', error: 'Saved elsewhere meanwhile.' })).toEqual({
      ok: false,
      title: 'Not saved',
      description: "Brakeman's settings were not saved: Saved elsewhere meanwhile. The app's settings were held back with them, so nothing changed. Your edits are kept.",
    })
    expect(saveWords({ brakeman: 'failed', app: 'unchanged', error: 'cwd: not a folder' })).toEqual({ ok: false, title: 'Not saved', description: "Brakeman's settings were not saved: cwd: not a folder. Your edits are kept." })
  })

  it("never says the app's settings were not saved when the app kept them, nor either way when it cannot tell (Codex's review)", () => {
    expect(saveWords({ brakeman: 'saved', app: 'unrestarted', error: 'the server did not start' })).toEqual({
      ok: false,
      title: 'Saved; the server did not restart',
      description: "Brakeman's settings were saved. The app's settings were saved, but the server did not restart with them: the server did not start.",
    })
    expect(saveWords({ brakeman: 'unchanged', app: 'unrestarted', error: 'the server did not start' }).description).toBe(
      "The app's settings were saved, but the server did not restart with them: the server did not start.",
    )
    expect(saveWords({ brakeman: 'saved', app: 'unknown', error: 'the server did not start' })).toEqual({
      ok: false,
      title: 'Saved in part',
      description: "Brakeman's settings were saved. The app refused the settings (the server did not start), and whether it kept them could not be read. Your edits are kept.",
    })
  })

  it("says a Save refused before anything was sent, Brakeman's held with it (the review's third finding)", () => {
    expect(saveWords({ brakeman: 'held', app: 'refused', error: 'the switchyard must be https://' })).toEqual({
      ok: false,
      title: 'Not saved',
      description: "the switchyard must be https://. Nothing was sent, Brakeman's settings with the rest. Your edits are kept.",
    })
    // The app's alone: its own words, as before.
    expect(saveWords({ brakeman: 'unchanged', app: 'refused', error: 'the switchyard must be https://' })).toEqual({ ok: false, title: 'Not saved', description: 'the switchyard must be https://' })
  })

  it("says the app's refusal in its own words when Brakeman's part was unchanged, as before", () => {
    expect(saveWords({ brakeman: 'unchanged', app: 'failed', error: 'the server did not start' })).toEqual({ ok: false, title: 'Not saved', description: 'the server did not start' })
  })
})
