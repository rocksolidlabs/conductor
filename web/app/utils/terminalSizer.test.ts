import { describe, expect, it } from 'vitest'
import { holdsSize, SCALE_FONT, scaledFont, showsScaled, sizedElsewhere, sizerChip, sizesSession, type SizerView } from './terminalSizer'

const base: SizerView = { fit: 'fill', mayFit: true, welcomed: true, sizer: true, sizedBy: 'me', me: 'me', compact: false, ended: false, roster: [{ id: 'me', name: 'Nate' }, { id: 'j', name: 'Jane' }], cols: 212, rows: 54 }

describe('who sizes the terminal', () => {
  it('fills the pane and sizes the session while this view holds the size', () => {
    expect(holdsSize(base)).toBe(true)
    expect(sizesSession(base)).toBe(true)
    expect(showsScaled(base)).toBe(false)
    expect(sizerChip(base)).toBe(null)
  })

  it("scales the session's grid and offers Fit to my window when another viewer holds it", () => {
    const v = { ...base, sizedBy: 'j' }
    expect(holdsSize(v)).toBe(false)
    expect(sizesSession(v)).toBe(false)
    expect(showsScaled(v)).toBe(true)
    expect(sizerChip(v)).toEqual({ who: 'Jane', cols: 212, rows: 54 })
  })

  it('names a viewer it does not know, and nobody holding the size', () => {
    expect(sizerChip({ ...base, sizedBy: 'gone' })?.who).toBe('another window')
    expect(sizerChip({ ...base, sizedBy: '' })?.who).toBe('a window that left')
  })

  it('holds the size for every controller of an older owner, as before', () => {
    const v = { ...base, sizer: false, sizedBy: '' }
    expect(holdsSize(v)).toBe(true)
    expect(showsScaled(v)).toBe(false)
    expect(sizerChip(v)).toBe(null)
  })

  it('scales a view-only or read-only full view, with no chip: it cannot take the size', () => {
    const v = { ...base, mayFit: false }
    expect(sizesSession(v)).toBe(false)
    expect(showsScaled(v)).toBe(true)
    expect(sizerChip(v)).toBe(null)
  })

  it('leaves tiles and scale views as they are, and shows nothing before the welcome or after the end', () => {
    expect(showsScaled({ ...base, fit: 'scale' })).toBe(true)
    expect(showsScaled({ ...base, fit: 'tile', sizedBy: 'j' })).toBe(false)
    expect(sizerChip({ ...base, fit: 'tile', sizedBy: 'j' })).toBe(null)
    expect(showsScaled({ ...base, welcomed: false, sizedBy: 'j' })).toBe(false)
    expect(sizerChip({ ...base, welcomed: false, sizedBy: 'j' })).toBe(null)
    expect(sizerChip({ ...base, ended: true, sizedBy: 'j' })).toBe(null)
    expect(sizerChip({ ...base, compact: true, sizedBy: 'j' })).toBe(null)
  })
})

describe('the font a scaled pane draws the grid at (#906)', () => {
  it("never draws a following full view's grid larger than its own font: a tile's six lines in a wide pane stay at 13 px", () => {
    // A guest's pane four times the grid drawn at 13 px: before #906 the grid was drawn at 28 px.
    expect(scaledFont(13, 4, 'fill', 13)).toBe(13)
    expect(scaledFont(13, 1.2, 'fill', 13)).toBe(13)
    // A view whose own font is larger keeps that as its bound, never past SCALE_FONT's.
    expect(scaledFont(13, 4, 'fill', 16)).toBe(16)
    expect(scaledFont(13, 4, 'fill', 40)).toBe(SCALE_FONT.max)
  })

  it('still draws a grid larger than the pane smaller, down to the least size', () => {
    expect(scaledFont(13, 0.5, 'fill', 13)).toBe(7)
    expect(scaledFont(13, 0.1, 'fill', 13)).toBe(SCALE_FONT.min)
  })

  it('lets a scale view (a view-only tile) fill its pane', () => {
    expect(scaledFont(13, 4, 'scale', 13)).toBe(SCALE_FONT.max)
    expect(scaledFont(13, 1.5, 'scale', 13)).toBe(20)
  })
})

describe('a view that cannot take the size says another window sets it', () => {
  const viewOnly: SizerView = { ...base, mayFit: false, sizedBy: 'j' }
  it('on a full view that may not size the session, whoever holds the size, an older owner too', () => {
    expect(sizedElsewhere(viewOnly)).toBe(true)
    expect(sizedElsewhere({ ...viewOnly, sizedBy: '' })).toBe(true)
    expect(sizedElsewhere({ ...viewOnly, sizer: false })).toBe(true)
    // The view-only line and the controller's chip never show together.
    expect(sizerChip(viewOnly)).toBe(null)
  })

  it('never on a view that may take the size (it has the chip), a tile, a scale view, before the welcome, or once ended', () => {
    expect(sizedElsewhere({ ...base, sizedBy: 'j' })).toBe(false)
    expect(sizerChip({ ...base, sizedBy: 'j' })).not.toBe(null)
    expect(sizedElsewhere(base)).toBe(false)
    expect(sizedElsewhere({ ...viewOnly, fit: 'scale' })).toBe(false)
    expect(sizedElsewhere({ ...viewOnly, fit: 'tile' })).toBe(false)
    expect(sizedElsewhere({ ...viewOnly, compact: true })).toBe(false)
    expect(sizedElsewhere({ ...viewOnly, welcomed: false })).toBe(false)
    expect(sizedElsewhere({ ...viewOnly, ended: true })).toBe(false)
  })
})
