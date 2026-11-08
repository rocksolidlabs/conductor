import { describe, expect, it } from 'vitest'
import { saveParts, saveWords } from './settingsSave'

/** A part that records its call in `calls` and answers `value`, or throws `fail`. */
function part<T>(calls: string[], name: string, value: T, fail?: string) {
  return async () => {
    calls.push(name)
    if (fail) throw new Error(fail)
    return value
  }
}

describe('saveParts', () => {
  it("saves Brakeman's part before the app's, which may restart the server and move the page", async () => {
    const calls: string[] = []
    const out = await saveParts({ brakeman: part(calls, 'brakeman', undefined), app: part(calls, 'app', 'restarted' as const) })
    expect(calls).toEqual(['brakeman', 'app'])
    expect(out).toEqual({ brakeman: 'saved', app: 'restarted' })
  })

  it('sends no part that is unchanged', async () => {
    const calls: string[] = []
    expect(await saveParts({ brakeman: part(calls, 'brakeman', undefined), app: null })).toEqual({ brakeman: 'saved', app: 'unchanged' })
    expect(await saveParts({ brakeman: null, app: part(calls, 'app', 'saved' as const) })).toEqual({ brakeman: 'unchanged', app: 'saved' })
    expect(calls).toEqual(['brakeman', 'app'])
  })

  it("holds the app's part back when Brakeman's is refused, so no restart follows a refusal", async () => {
    const calls: string[] = []
    const out = await saveParts({ brakeman: part(calls, 'brakeman', undefined, 'Saved elsewhere meanwhile.'), app: part(calls, 'app', 'restarted' as const) })
    expect(calls).toEqual(['brakeman'])
    expect(out).toEqual({ brakeman: 'failed', app: 'held', error: 'Saved elsewhere meanwhile.' })
  })

  it("keeps Brakeman's part saved when the app's fails after it", async () => {
    const calls: string[] = []
    const out = await saveParts({ brakeman: part(calls, 'brakeman', undefined), app: part(calls, 'app', 'saved' as const, 'the server did not start') })
    expect(out).toEqual({ brakeman: 'saved', app: 'failed', error: 'the server did not start' })
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
    expect(saveWords({ brakeman: 'saved', app: 'failed', error: 'the server did not start' })).toEqual({
      ok: false,
      title: 'Saved in part',
      description: "Brakeman's settings were saved. The app's settings were not: the server did not start. Your edits are kept.",
    })
    expect(saveWords({ brakeman: 'failed', app: 'held', error: 'Saved elsewhere meanwhile.' })).toEqual({
      ok: false,
      title: 'Not saved',
      description: "Brakeman's settings were not saved: Saved elsewhere meanwhile. The app's settings were held back with them, so nothing changed. Your edits are kept.",
    })
    expect(saveWords({ brakeman: 'failed', app: 'unchanged', error: 'cwd: not a folder' })).toEqual({ ok: false, title: 'Not saved', description: "Brakeman's settings were not saved: cwd: not a folder. Your edits are kept." })
  })

  it("says the app's refusal in its own words when Brakeman's part was unchanged, as before", () => {
    expect(saveWords({ brakeman: 'unchanged', app: 'failed', error: 'the server did not start' })).toEqual({ ok: false, title: 'Not saved', description: 'the server did not start' })
  })
})
