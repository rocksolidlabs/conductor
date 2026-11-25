import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { hiddenPattern, holdsHidden, isHidden, markHidden } from './hiddenMarks'
import { shownText } from './runOrigin'

// The characters a person cannot see (utils/hiddenMarks.ts). The module is in the chunk every page starts with: it builds no pattern
// as it loads, only at its first use, and a shared pattern answers each call the same whatever the call before it did.

const src = readFileSync(new URL('./hiddenMarks.ts', import.meta.url), 'utf8')

describe('hiddenMarks', () => {
  it('buildsNoPatternAtLoad: no pattern is built at the module\'s top level, only inside the functions that use one', () => {
    // Every line at the module's top level (not indented) that builds a pattern: none.
    const top = src.split('\n').filter((l) => /^\S/.test(l) && !l.startsWith('//') && !l.startsWith(' *') && !l.startsWith('/**'))
    expect(top.filter((l) => /RegExp\(|\/[^/\s].*\/[gimsuyv]*\s*$/.test(l) && !l.includes('String.raw'))).toEqual([])
    expect(src).toContain("return (hidden ??= new RegExp(HIDDEN_SOURCE, 'gu'))")
    expect(src).toContain("return (one ??= new RegExp(`^(?:${HIDDEN_SOURCE})$`, 'u')).test(c)")
  })

  it('answers each call alike: the shared global pattern keeps no place between calls', () => {
    const zw = String.fromCodePoint(0x200b)
    expect(hiddenPattern()).toBe(hiddenPattern())
    for (let i = 0; i < 3; i++) {
      const mark = String.fromCodePoint(0xfffd)
      expect(shownText(`a${zw}b${zw}c`)).toBe(`a${mark}b${mark}c`)
      expect(hiddenPattern().lastIndex).toBe(0)
      expect(isHidden(zw)).toBe(true)
      expect(isHidden('a')).toBe(false)
      expect(holdsHidden(`x${zw}`)).toBe(true)
      expect(markHidden(`x${zw}y`).hidden).toBe(true)
    }
  })
})
