import { describe, expect, it } from 'vitest'
import { CONTRAST_FLOORS, floorFor, isLargeText, luminance, mixSrgb, over, parseColor, ratio, toHex } from './contrast'
import { cssColorToHex } from './terminalTheme'

const two = (x: number) => Math.round(x * 100) / 100

describe('ratio', () => {
  it("reproduces the brand's table (docs/design/brand.md)", () => {
    expect(two(ratio('#18211C', '#EEF1E9'))).toBe(14.45) // ink on sage
    expect(two(ratio('#526454', '#EEF1E9'))).toBe(5.56) // muted on sage
    expect(two(ratio('#A44727', '#EEF1E9'))).toBe(5.24) // accent text on sage
    expect(two(ratio('#FFFFFF', '#263D35'))).toBe(11.66) // white on forest
  })

  it('puts the floor where WCAG puts it, in either order', () => {
    expect(two(ratio('#767676', '#ffffff'))).toBe(4.54)
    expect(two(ratio('#777777', '#ffffff'))).toBe(4.48)
    expect(ratio('#ffffff', '#767676')).toBe(ratio('#767676', '#ffffff'))
    expect(ratio('#000', '#fff')).toBe(21)
    expect(luminance('#ffffff')).toBe(1)
    expect(luminance('#000000')).toBe(0)
  })
})

describe('over', () => {
  it("lays a tint over its ground as a subtle badge draws it: signal-500 at 10% over white", () => {
    const tint = over('#c98a1b', '#ffffff', 0.1)
    expect(tint.a).toBe(1)
    // 0.1 of the colour and 0.9 of white, channel by channel.
    expect(toHex(tint)).toBe(toHex({ r: (0.1 * 0xc9 + 0.9 * 255) / 255, g: (0.1 * 0x8a + 0.9 * 255) / 255, b: (0.1 * 0x1b + 0.9 * 255) / 255, a: 1 }))
    expect(toHex(tint)).toBe('#faf3e8')
    // The light amber word (signal-700) on it holds the text floor; signal-500, the word before the change, does not.
    expect(ratio('#805000', tint)).toBeGreaterThan(CONTRAST_FLOORS.text)
    expect(ratio('#c98a1b', tint)).toBeLessThan(CONTRAST_FLOORS.text)
  })

  it("counts the top colour's own alpha, and leaves a ground's alpha in the result", () => {
    const half = parseColor('#00000080')!
    expect(toHex(over(half, '#ffffff'))).toBe(toHex(over('#000000', '#ffffff', half.a)))
    expect(over('#ff000000', '#00ff00')).toEqual(parseColor('#00ff00'))
    const onClear = over(parseColor('rgba(0, 0, 0, 0.5)')!, 'transparent')
    expect(two(onClear.a)).toBe(0.5)
  })

  it('mixes in srgb as color-mix does', () => {
    expect(toHex(mixSrgb('#ffffff', '#000000', 60))).toBe('#999999')
    expect(toHex(mixSrgb('#ffffff', '#eef1e9', 100))).toBe('#ffffff')
  })
})

describe('parseColor', () => {
  it('reads hex with and without alpha', () => {
    expect(parseColor('#abc')).toEqual(parseColor('#aabbcc'))
    expect(two(parseColor('#18211c80')!.a)).toBe(0.5)
    expect(parseColor('#12345')).toBeUndefined()
  })

  it('reads rgb() and rgba() and keeps their alpha', () => {
    expect(parseColor('rgb(24, 33, 28)')).toEqual({ r: 24 / 255, g: 33 / 255, b: 28 / 255, a: 1 })
    expect(parseColor('rgba(24, 33, 28, 0.25)')!.a).toBe(0.25)
    expect(parseColor('rgb(24 33 28 / 40%)')!.a).toBe(0.4)
    expect(parseColor('transparent')!.a).toBe(0)
  })

  it('reads color(srgb …) and refuses another space', () => {
    expect(parseColor('color(srgb 1 0 0 / 0.5)')).toEqual({ r: 1, g: 0, b: 0, a: 0.5 })
    expect(parseColor('color(display-p3 1 0 0)')).toBeUndefined()
  })

  it('reads oklch() as cssColorToHex does', () => {
    for (const v of ['oklch(21% 0.006 285.885)', 'oklch(55.2% 0.016 285.938)', 'oklch(0.7 0.1 150)', 'oklch(98.5% 0 none)']) {
      expect(toHex(parseColor(v)!)).toBe(cssColorToHex(v))
    }
    expect(toHex(parseColor('oklch(1 0 0)')!)).toBe('#ffffff')
  })

  it('keeps the alpha of oklab() after the slash, as a browser writes color-mix with transparent', () => {
    const c = parseColor('oklab(0.7 0.02 0.12 / 0.1)')!
    expect(c.a).toBe(0.1)
    expect(toHex(c)).toBe(cssColorToHex('oklab(0.7 0.02 0.12)'))
    // cssColorToHex drops it.
    expect(cssColorToHex('oklab(0.7 0.02 0.12 / 0.1)')).toBe(toHex(c))
  })

  it('refuses what it does not read', () => {
    for (const v of ['var(--ui-bg)', 'hsl(0 100% 50%)', 'currentcolor', '', undefined]) expect(parseColor(v)).toBeUndefined()
  })
})

describe('the floors', () => {
  it('apply the large-text floor at 24 px, or 18.66 px at weight 700', () => {
    expect(isLargeText(24, 400)).toBe(true)
    expect(isLargeText(23.9, 400)).toBe(false)
    expect(isLargeText(18.66, 700)).toBe(true)
    expect(isLargeText(18.66, 600)).toBe(false)
    expect(floorFor({ px: 14, weight: 400 })).toBe(4.5)
    expect(floorFor({ px: 24, weight: 400 })).toBe(3)
    expect(floorFor({ px: 14, weight: 400, dimmed: 'page' })).toBe(3)
    expect(floorFor({ px: 14, weight: 400, dimmed: 'raised' })).toBe(2.7)
  })
})
