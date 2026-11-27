import { describe, expect, it } from 'vitest'
import { commandAt, commandRows, fileRows, fromFolder, MENU_ROWS, pickCommand, wholeCommand, mentionAt, outsideMention, pickMention, safePath, startsWithSlash } from './chatBoxMenu'

describe("the chat box's list for @", () => {
  it('triggerAtAWordsStart: an @ word at the start or after white space, the cursor in it or at its end', () => {
    expect(mentionAt('@no', 3)).toEqual({ start: 0, end: 3, words: 'no' })
    expect(mentionAt('Look at @no', 11)).toEqual({ start: 8, end: 11, words: 'no' })
    expect(mentionAt('Look at @', 9)).toEqual({ start: 8, end: 9, words: '' })
    expect(mentionAt('Look at\n@src/ma please', 12)).toEqual({ start: 8, end: 15, words: 'src' })
    // The cursor past the word, after a space: no list.
    expect(mentionAt('Look at @notes.txt ', 19)).toBeNull()
  })

  it('noTriggerInsideAWord: an address, an @ inside a word, words past the bound', () => {
    expect(mentionAt('mail a@b', 8)).toBeNull()
    expect(mentionAt('x@', 2)).toBeNull()
    expect(mentionAt('@a@b', 4)).toBeNull()
    expect(mentionAt(`@${'a'.repeat(201)}`, 202)).toBeNull()
    expect(mentionAt(`@${'a'.repeat(200)}`, 201)).not.toBeNull()
  })

  it('pickReplacesTheWordOnly: the @ word becomes @<path> and a space, the cursor after it, the rest as typed', () => {
    const text = 'Look at @no then tell me'
    const m = mentionAt(text, 11)!
    expect(pickMention(text, m, 'notes.txt')).toEqual({ text: 'Look at @notes.txt then tell me', caret: 19 })
    const end = mentionAt('Look at @no', 11)!
    expect(pickMention('Look at @no', end, 'notes.txt')).toEqual({ text: 'Look at @notes.txt ', caret: 19 })
    // The whole word goes, the part after the cursor too.
    const mid = mentionAt('@notxx', 3)!
    expect(pickMention('@notxx', mid, 'notes.txt').text).toBe('@notes.txt ')
  })

  it('unsafeNamesCannotBePicked: a space, a newline, a quote, a backslash, an @, a leading - or /, a .. part, a bidi mark', () => {
    for (const ok of ['notes.txt', 'src/main.go', 'a-b_c+d=e,f%g.txt', 'ünïcode/naïve.md']) expect(safePath(ok), ok).toBe(true)
    for (const bad of ['my notes.txt', 'a\nb', 'it"s', "it's", 'a\\b', 'a@b', '-rf', '/etc/hosts', 'a/../b', '..', `abc${String.fromCodePoint(0x202e)}def`, `x${String.fromCodePoint(0x200b)}y`, '', 'a'.repeat(1025)]) {
      expect(safePath(bad), JSON.stringify(bad)).toBe(false)
    }
    const rows = fileRows(['notes.txt', 'my notes.txt', 'notes.txt'])
    expect(rows.map((r) => [r.value, r.pickable])).toEqual([['notes.txt', true], ['my notes.txt', false]])
    expect(fileRows(Array.from({ length: 20 }, (_, i) => `f${i}.txt`))).toHaveLength(MENU_ROWS)
  })

  it('fromFolder: a touched file as its path from the folder; none outside it', () => {
    expect(fromFolder('/home/a/shop', '/home/a/shop/src/x.go')).toBe('src/x.go')
    expect(fromFolder('/home/a/shop/', '/home/a/shop/x.go')).toBe('x.go')
    expect(fromFolder('/home/a/shop', '/home/a/shopping/x.go')).toBeNull()
    expect(fromFolder('/home/a/shop', '/home/a/shop')).toBeNull()
    expect(fromFolder(undefined, '/x')).toBeNull()
  })

  it('leadingSlashIsSaid: a line that begins with /, after white space', () => {
    expect(startsWithSlash('/compact')).toBe(true)
    expect(startsWithSlash('  /model')).toBe(true)
    expect(startsWithSlash('see /etc')).toBe(false)
  })
})

describe('outsideMention (UX3 R10)', () => {
  it('names the first @ word outside the folder: from /, from ~, through ..; none inside it', () => {
    expect(outsideMention('look at @notes.txt')).toBe('')
    expect(outsideMention('look at @/etc/hosts')).toBe('/etc/hosts')
    expect(outsideMention('@~/.ssh/config please')).toBe('~/.ssh/config')
    expect(outsideMention('@src/../../x')).toBe('src/../../x')
    expect(outsideMention('mail a@/etc/hosts')).toBe('')
  })
})

describe("the chat box's list for / (UX3 R11)", () => {
  const list = [
    { name: 'compact', words: 'Shorten the conversation so far' },
    { name: 'clear', words: 'Start a new conversation', aliases: ['reset'] },
    { name: 'model', hint: '<name>' },
    { name: 'fix-issue', hint: '<number>' },
    { name: 'bad name' },
    { name: '-x' },
  ]
  it('commandAt: the first word of a line that begins with /, the cursor in it', () => {
    expect(commandAt('/fi', 3)).toEqual({ end: 3, typed: 'fi' })
    expect(commandAt('/', 1)).toEqual({ end: 1, typed: '' })
    expect(commandAt('/fix-issue 12', 13)).toBeNull()
    expect(commandAt('see /fix', 8)).toBeNull()
    expect(commandAt('/fix', 0)).toBeNull()
  })
  it('commandsByPrefixThenWithinThenAlias: names that start with it, then hold it, then an alias; at most eight', () => {
    expect(commandRows(list, '').map((r) => r.value)).toEqual(['compact', 'clear', 'model', 'fix-issue'])
    expect(commandRows(list, 'c').map((r) => r.value)).toEqual(['compact', 'clear'])
    expect(commandRows(list, 'is').map((r) => r.value)).toEqual(['fix-issue'])
    expect(commandRows(list, 'res').map((r) => r.value)).toEqual(['clear'])
    expect(commandRows(Array.from({ length: 20 }, (_, i) => ({ name: `c${i}` })), 'c')).toHaveLength(MENU_ROWS)
    expect(commandRows(list, 'mod')[0]).toMatchObject({ value: 'model', hint: '<name>' })
  })
  it('unsafeCommandNamesAreDropped: a name of another shape is never offered', () => {
    expect(commandRows(list, '').map((r) => r.value)).not.toContain('bad name')
    expect(commandRows(list, 'x').map((r) => r.value)).toEqual(['fix-issue'])
  })
  it('wholeNameSendsAtOnce, and a pick completes to /name and a space', () => {
    expect(wholeCommand(list, 'compact')).toBe(true)
    expect(wholeCommand(list, 'comp')).toBe(false)
    expect(wholeCommand(list, '')).toBe(false)
    expect(pickCommand('/fi', commandAt('/fi', 3)!, 'fix-issue')).toEqual({ text: '/fix-issue ', caret: 11 })
    expect(pickCommand('/fi 12', commandAt('/fi 12', 3)!, 'fix-issue')).toEqual({ text: '/fix-issue 12', caret: 11 })
  })
})
