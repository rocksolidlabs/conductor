import { describe, expect, it } from 'vitest'
import type { DesktopSettings } from './desktop'
import { followSaved, holds, saveParts, saveWords, together, type AppPart } from './settingsSave'

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
