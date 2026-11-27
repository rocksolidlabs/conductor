import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { allowWay, answerLine, checkLine, pressTarget, refusedWords, wideQuestion, type RootCheck } from './folderAllow'

// Allow this folder (UX3 R30): the line under a folder field, made from the server's check, what this app can do, and the press.

const outside = (over: Partial<RootCheck> = {}): RootCheck => ({ path: '/home/ada/Downloads', state: 'outside', ...over })
const said = (l: ReturnType<typeof checkLine>) => (l ? [l.words, l.folder ?? '', l.settings ? 'Settings' : '', l.after ?? ''].filter(Boolean).join(' ').replace(/ ([.,?])/g, '$1') : null)

describe('the way on, from the app and the server', () => {
  it('presses only in the desktop app whose bridge offers it and whose server holds its key', () => {
    expect(allowWay({ desktop: true, bridgePress: true, folders: 'app' })).toBe('press')
    expect(allowWay({ desktop: true, bridgePress: false, folders: 'app' })).toBe('settings')
    expect(allowWay({ desktop: false, bridgePress: false, folders: 'app' })).toBe('app')
    expect(allowWay({ desktop: false, bridgePress: false, folders: 'config' })).toBe('config')
    // A bridge with the press, on a server that holds no key: no press.
    expect(allowWay({ desktop: true, bridgePress: true, folders: 'config' })).toBe('settings')
    expect(allowWay({ desktop: true, bridgePress: true, folders: '' })).toBe('settings')
  })
})

describe('the line for a check', () => {
  it('says nothing for a folder agents may use', () => {
    expect(checkLine({ path: '/w', state: 'allowed' }, 'press')).toBeNull()
  })
  it('offers the press for a folder under none agents may use, and only there', () => {
    const l = checkLine(outside(), 'press')!
    expect(l).toMatchObject({ state: 'outside', words: "Agents can't use this folder yet.", button: 'allow', tone: 'error', blocks: true })
    for (const way of ['settings', 'app', 'config'] as const) expect(checkLine(outside(), way)!.button, way).toBe('')
  })
  it('says the way on where this app cannot press', () => {
    expect(said(checkLine(outside(), 'settings'))).toBe("Agents can't use this folder. Add it to Folders agents can use in Settings, or pick another.")
    expect(said(checkLine(outside(), 'app'))).toBe("Agents can't use this folder. Allow it in the Conductor app on the computer that runs it.")
    expect(said(checkLine(outside(), 'config'))).toBe("Agents can't use this folder. This server's folders are set in its configuration.")
  })
  it('never offers the press for a folder refused, and says why by its reason; an unknown reason is no yes', () => {
    for (const reason of ['whole_disk', 'conductor_data', 'name', 'owner', 'something_new']) {
      const l = checkLine(outside({ refused: reason }), 'press')!
      expect(l.button, reason).toBe('')
      expect(l.blocks, reason).toBe(true)
      expect(l.words, reason).toBe(refusedWords(reason))
    }
    expect(refusedWords('whole_disk')).toBe("Agents can't use the whole disk. Pick a folder inside it.")
    expect(refusedWords('conductor_data')).toBe("Agents can't use this folder: it holds Conductor's own data. Pick another.")
    expect(refusedWords('name')).toBe("Agents can't use this folder: its name has a comma, a hidden character or a space at an end. Rename it, or pick another.")
    expect(refusedWords('owner')).toBe("Agents can't use this folder: another user owns it. Pick one of yours.")
    expect(refusedWords('something_new')).toBe("Agents can't use this folder. Pick another.")
  })
  it('names where a link leads and offers that folder, never the press', () => {
    const l = checkLine(outside({ link: true, real: '/srv/real' }), 'press')!
    expect(l).toMatchObject({ state: 'link', folder: '/srv/real', button: 'use' })
    expect(said(l)).toBe("This goes through a link to /srv/real. Agents can't use that folder yet.")
  })
  it('keeps the wide kind for the question, and takes an unknown wide kind as no yes', () => {
    expect(checkLine(outside({ wide: 'home' }), 'press')).toMatchObject({ button: 'allow', wide: 'home' })
    expect(checkLine(outside({ wide: 'whole_world' }), 'press')).toMatchObject({ button: '', state: 'refused', words: "Agents can't use this folder. Pick another." })
  })
  it('says a folder missing, a file and one that cannot be opened; a missing one is not said while it is typed', () => {
    expect(checkLine({ path: '/x', state: 'missing' }, 'press')!.words).toBe('There is no folder at this path.')
    expect(checkLine({ path: '/x', state: 'missing' }, 'press', true)).toBeNull()
    expect(checkLine({ path: '/x', state: 'not_directory' }, 'press')!.words).toBe('This is a file, not a folder.')
    expect(checkLine({ path: '/x', state: 'unreadable' }, 'press')!.words).toBe("Conductor can't open this folder. Pick another.")
  })
  it('takes a state it does not know as no yes', () => {
    expect(checkLine({ path: '/x', state: 'maybe' }, 'press')).toMatchObject({ state: 'unknown', blocks: true, button: '', words: "Agents can't use this folder. Pick another." })
  })
  it('names the folder the press allows whenever it is not the text as typed', () => {
    // A relative text, a `..`, another spelling: the server's own path, on the line.
    expect(checkLine(outside({ path: '/srv/b' }), 'press', false, 'b')).toMatchObject({ words: "Agents can't use", folder: '/srv/b', after: ' yet.', button: 'allow' })
    expect(checkLine(outside({ path: '/srv/b' }), 'press', false, '/srv/a/../b')!.folder).toBe('/srv/b')
    expect(checkLine(outside({ path: '/srv/b', real: '/data/b' }), 'press', false, '/srv/b')!.folder).toBe('/data/b')
    // Letter for letter what was typed: the folder is the field's, not named again.
    expect(checkLine(outside({ path: '/srv/b' }), 'press', false, '/srv/b')!.folder).toBeUndefined()
  })
})

describe('the press', () => {
  it('allows the folder the check named, its real path where it gave one, never the text read again', () => {
    expect(pressTarget({ path: '/home/ada/dl', state: 'outside' })).toBe('/home/ada/dl')
    expect(pressTarget({ path: '/home/ada/dl', real: '/srv/dl', state: 'outside' })).toBe('/srv/dl')
  })
  it('asks first for a wide folder, in the words for its kind', () => {
    expect(wideQuestion('home', '/home/ada')).toEqual({ words: 'Allow your whole home folder,', folder: '/home/ada', after: '? Agents could read and change everything in it.' })
    expect(wideQuestion('above_home', '/home')).toEqual({ words: 'Allow all of', folder: '/home', after: '? Your home folder is inside it, and agents could read and change everything there.' })
    expect(wideQuestion('holds_data', '/srv')).toEqual({ words: 'Allow all of', folder: '/srv', after: '? Conductor keeps its own data inside it, and agents could read it.' })
  })
  it('says what became of the folder', () => {
    const c = outside()
    expect(answerLine({ ok: true, root: '/r', saved: true }, c)).toMatchObject({ state: 'allowed_now', words: 'Agents can use this folder now.', blocks: false, tone: 'success' })
    expect(answerLine({ ok: true, root: '/r', saved: true }, c).settings).toBeFalsy()
    expect(answerLine({ ok: true, root: '/r', saved: false, error: 'the settings file could not be written' }, c).words).toBe('Agents can use this folder until Conductor restarts. It was not kept: the settings file could not be written.')
    expect(answerLine({ ok: true, root: '/r', saved: true, error: 'Conductor restarted meanwhile and is not running yet' }, c)).toMatchObject({ words: 'Restart Conductor to use this folder.', tone: 'warning', blocks: false })
  })
  it('says a refusal in the line of its state, and keeps the press for a server restarting or another failure', () => {
    const c = outside()
    expect(answerLine({ ok: false, code: 'folder_refused', message: '', reason: 'owner' }, c)).toMatchObject({ button: '', words: refusedWords('owner') })
    expect(answerLine({ ok: false, code: 'folder_refused', message: '', reason: 'brand_new' }, c)).toMatchObject({ button: '', words: "Agents can't use this folder. Pick another." })
    expect(answerLine({ ok: false, code: 'folder_is_link', message: '', real: '/srv/r' }, c)).toMatchObject({ state: 'link', folder: '/srv/r', button: 'use' })
    expect(answerLine({ ok: false, code: 'folder_wide', message: '', wide: 'above_home' }, c)).toMatchObject({ state: 'wide', button: 'wide', wide: 'above_home' })
    expect(answerLine({ ok: false, code: 'folder_wide', message: '', wide: 'nope' }, c)).toMatchObject({ button: '', state: 'refused' })
    expect(answerLine({ ok: false, code: 'folder_missing', message: '' }, c).words).toBe('There is no folder at this path.')
    expect(answerLine({ ok: false, code: 'busy', message: 'the settings are being saved: try again' }, c)).toMatchObject({ button: 'allow', words: 'Conductor is busy. Press Allow this folder again in a moment.' })
    expect(answerLine({ ok: false, code: 'no_answer', message: '' }, c)).toMatchObject({ button: 'allow', words: 'Conductor did not answer. Press Allow this folder again in a moment.' })
    expect(answerLine({ ok: false, code: 'rate_limited', message: '' }, c).button).toBe('allow')
    expect(answerLine({ ok: false, code: 'folder_changed', message: '' }, c)).toMatchObject({ button: 'allow', words: 'The folder changed as it was allowed. Press Allow this folder again.' })
    expect(answerLine({ ok: false, code: 'too_many_roots', message: 'agents may use at most 64 folders' }, c)).toMatchObject({ button: '', settings: true, words: 'Agents can use no more folders. Remove one in', after: ', or pick a folder agents can use.' })
    expect(answerLine({ ok: false, code: 'not_the_app', message: '' }, c).button).toBe('')
    // A refusal this page does not know offers no button that could not help.
    expect(answerLine({ ok: false, code: 'invalid_request', message: "the folder's path is longer than 1024 characters" }, c)).toMatchObject({ button: '', words: "The folder was not allowed: the folder's path is longer than 1024 characters. Pick another." })
    expect(answerLine({ ok: false, code: 'something_new', message: 'x' }, c).button).toBe('')
  })
})

describe('FolderAllowLine.vue', () => {
  const src = readFileSync(new URL('../components/FolderAllowLine.vue', import.meta.url), 'utf8')
  it('asks the app alone: the press goes through the bridge, never a request of the page', () => {
    expect(src).toContain('b.allowFolder(pressTarget(c), confirmed)')
    expect(src).not.toMatch(/\/api\/roots['"`]/)
    expect(src).not.toMatch(/method:\s*['"]POST/)
  })
  it('never puts the focus on the press: the line exposes no way to focus its button', () => {
    expect(src).not.toContain('focusAction')
  })
})
