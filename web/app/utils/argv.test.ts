import { describe, expect, it } from 'vitest'
import { hasOpenQuote, joinArgv, pasteArgs, slugId, splitArgs } from './argv'

describe('slugId', () => {
  it('lowercases, dashes and trims to 32', () => {
    expect(slugId('Claude (opus)')).toBe('claude-opus')
    expect(slugId('  Aider ')).toBe('aider')
    expect(slugId('x'.repeat(40))).toHaveLength(32)
    expect(slugId('!!!')).toBe('')
  })
  it('collapses runs of separators and drops the ones at the ends', () => {
    expect(slugId('My  Tool -- v2')).toBe('my-tool-v2')
    expect(slugId('--x--')).toBe('x')
  })
  it('never ends in a dash when the cut falls on one', () => {
    expect(slugId('a'.repeat(31) + ' b')).toBe('a'.repeat(31))
  })
})

describe('splitArgs', () => {
  it('splits on whitespace and honours quotes', () => {
    expect(splitArgs(`aider --model "gpt 5" 'a b'`)).toEqual(['aider', '--model', 'gpt 5', 'a b'])
  })
  it('returns nothing for blank input and keeps an empty quoted argument', () => {
    expect(splitArgs('')).toEqual([])
    expect(splitArgs('   ')).toEqual([])
    expect(splitArgs('""')).toEqual([''])
  })
})

describe('hasOpenQuote', () => {
  it('is true while a quoted argument is still open', () => {
    expect(hasOpenQuote('aider --model "gpt')).toBe(true)
    expect(hasOpenQuote(`'a b`)).toBe(true)
    expect(hasOpenQuote('"')).toBe(true)
  })
  it('is false when every quote is closed or is not at the start of an argument', () => {
    expect(hasOpenQuote('')).toBe(false)
    expect(hasOpenQuote('aider --model "gpt 5"')).toBe(false)
    expect(hasOpenQuote(`it's`)).toBe(false)
    expect(hasOpenQuote(`"a b" 'c d'`)).toBe(false)
  })
})

describe('pasteArgs', () => {
  it('keeps a pasted open quote typed instead of making a chip that holds the quote', () => {
    expect(pasteArgs('', 'aider --model "gpt')).toEqual({ chips: [], pending: 'aider --model "gpt' })
    expect(pasteArgs('--x ', '"a b')).toEqual({ chips: [], pending: '--x "a b' })
  })
  it('lets a pasted closing quote finish the argument typed before it', () => {
    expect(pasteArgs('--model "gpt', ' 5"')).toEqual({ chips: ['--model', 'gpt 5'], pending: '' })
  })
  it('turns a paste with every quote closed into chips', () => {
    expect(pasteArgs('', `aider --model "gpt 5" 'a b'`)).toEqual({ chips: ['aider', '--model', 'gpt 5', 'a b'], pending: '' })
  })
})

describe('joinArgv', () => {
  it('quotes what needs quoting', () => {
    expect(joinArgv(['sh', '-c', 'echo hi'])).toBe("sh -c 'echo hi'")
  })
  it('shows empty and quote-bearing arguments unambiguously', () => {
    expect(joinArgv([])).toBe('')
    expect(joinArgv(['echo', ''])).toBe("echo ''")
    expect(joinArgv(['echo', "it's"])).toBe("echo 'it'\\''s'")
  })
})

describe('what follows a closing quote', () => {
  it('is part of the same argument up to the next white space, as a shell reads it', () => {
    expect(splitArgs('"a b"c')).toEqual(['a bc'])
    expect(splitArgs("'a b'c d")).toEqual(['a bc', 'd'])
    expect(splitArgs('"a b" c')).toEqual(['a b', 'c'])
    expect(splitArgs('--add-dir "/r/a b"/sub --x')).toEqual(['--add-dir', '/r/a b/sub', '--x'])
    // A quote after the closing one opens nothing; a backslash is never special.
    expect(splitArgs('"a"\'b c\'')).toEqual(["a'b", "c'"])
    expect(splitArgs('"a\\" b')).toEqual(['a\\', 'b'])
    expect(hasOpenQuote('"a b"c')).toBe(false)
    expect(hasOpenQuote('"a b"c "d')).toBe(true)
  })
})
