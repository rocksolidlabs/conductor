import { describe, expect, it } from 'vitest'
import { fileRows, fromFolder, MENU_ROWS, mentionAt, pickMention, safePath, startsWithSlash } from './chatBoxMenu'

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
