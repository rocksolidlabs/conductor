import { describe, expect, it } from 'vitest'
import { clampZoom, nextZoom, zoomKey, zoomPercent, ZoomStart, zoomStatus, ZOOM_MAX, ZOOM_MIN, ZOOM_STEP, type ZoomInput } from '../src/zoom'

const key = (k: string, code: string, mods: Partial<ZoomInput> = {}): ZoomInput => ({ type: 'keyDown', key: k, code, control: false, meta: false, alt: false, ...mods })

describe('zoom keys', () => {
  it('takes Ctrl+= on Windows and Linux, with or without Shift, and the numpad', () => {
    for (const p of ['win32', 'linux'] as const) {
      expect(zoomKey(key('=', 'Equal', { control: true }), p)).toBe('in')
      expect(zoomKey(key('+', 'Equal', { control: true }), p)).toBe('in')
      expect(zoomKey(key('+', 'NumpadAdd', { control: true }), p)).toBe('in')
      expect(zoomKey(key('-', 'Minus', { control: true }), p)).toBe('out')
      expect(zoomKey(key('-', 'NumpadSubtract', { control: true }), p)).toBe('out')
      expect(zoomKey(key('0', 'Digit0', { control: true }), p)).toBe('reset')
      expect(zoomKey(key('0', 'Numpad0', { control: true }), p)).toBe('reset')
    }
  })
  it('takes Cmd on macOS and leaves Ctrl to the terminal there', () => {
    expect(zoomKey(key('=', 'Equal', { meta: true }), 'darwin')).toBe('in')
    expect(zoomKey(key('-', 'Minus', { control: true }), 'darwin')).toBeNull()
    expect(zoomKey(key('=', 'Equal', { meta: true }), 'win32')).toBeNull()
  })
  it('leaves everything else to the page', () => {
    expect(zoomKey(key('=', 'Equal'), 'win32')).toBeNull()
    expect(zoomKey(key('=', 'Equal', { control: true, alt: true }), 'win32')).toBeNull()
    expect(zoomKey({ ...key('=', 'Equal', { control: true }), type: 'keyUp' }, 'win32')).toBeNull()
    expect(zoomKey(key('c', 'KeyC', { control: true }), 'win32')).toBeNull()
    expect(zoomKey(key('1', 'Digit1', { control: true }), 'win32')).toBeNull()
  })
})

describe('zoom level', () => {
  it('steps by half a level within its bounds, and resets to 0', () => {
    expect(nextZoom(0, 'in')).toBe(0.5)
    expect(nextZoom(0, 'out')).toBe(-0.5)
    expect(nextZoom(ZOOM_MAX, 'in')).toBe(ZOOM_MAX)
    expect(nextZoom(ZOOM_MIN, 'out')).toBe(ZOOM_MIN)
    expect(nextZoom(2, 'reset')).toBe(0)
  })
  it('clamps what a settings file holds', () => {
    expect(clampZoom(undefined)).toBe(0)
    expect(clampZoom('2')).toBe(0)
    expect(clampZoom(Number.NaN)).toBe(0)
    expect(clampZoom(99)).toBe(ZOOM_MAX)
    expect(clampZoom(-99)).toBe(ZOOM_MIN)
    expect(clampZoom(1.2)).toBe(1)
  })
})

describe('zoom percent', () => {
  it('reads every step as Chromium draws it, 100% at 0', () => {
    const steps: number[] = []
    for (let l = ZOOM_MIN; l <= ZOOM_MAX; l += ZOOM_STEP) steps.push(zoomPercent(l))
    expect(steps).toEqual([58, 63, 69, 76, 83, 91, 100, 110, 120, 131, 144, 158, 173, 189, 207, 227, 249])
    expect(zoomPercent(-3)).toBe(58)
    expect(zoomPercent(-0.5)).toBe(91)
    expect(zoomPercent(0)).toBe(100)
    expect(zoomPercent(0.5)).toBe(110)
    expect(zoomPercent(5)).toBe(249)
  })
  it('says the largest and the smallest at the bounds, nothing between', () => {
    expect(zoomStatus(ZOOM_MAX)).toEqual({ level: 5, percent: 249, bound: 'largest' })
    expect(zoomStatus(ZOOM_MIN)).toEqual({ level: -3, percent: 58, bound: 'smallest' })
    expect(zoomStatus(1.5)).toEqual({ level: 1.5, percent: 131, bound: '' })
    expect(zoomStatus(0)).toEqual({ level: 0, percent: 100, bound: '' })
    // A move at a bound changes nothing, and is still told as the bound.
    expect(zoomStatus(nextZoom(ZOOM_MAX, 'in')).bound).toBe('largest')
    expect(zoomStatus(nextZoom(ZOOM_MIN, 'out')).bound).toBe('smallest')
  })
})

describe('a zoom level kept from before', () => {
  it('is said to the first page that asks after a start, never again', () => {
    const z = new ZoomStart()
    expect(z.ask(1)).toEqual({ level: 1, percent: 120, bound: '', announce: true })
    expect(z.ask(1).announce).toBe(false)
    expect(z.ask(1).announce).toBe(false)
  })
  it('is not said at 100%, nor later once it changes', () => {
    const z = new ZoomStart()
    expect(z.ask(0).announce).toBe(false)
    expect(z.ask(1).announce).toBe(false)
  })
  it('is not said once a move has been shown', () => {
    const z = new ZoomStart()
    z.moved()
    expect(z.ask(1.5).announce).toBe(false)
  })
})
