import { describe, expect, it } from 'vitest'
import { clampZoom, moveZoom, nextZoom, zoomKey, zoomPercent, ZoomStart, zoomStatus, ZOOM_MAX, ZOOM_MIN, ZOOM_STEP, type ZoomInput, type ZoomStatus } from '../src/zoom'

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
  it('is still said to the first page after a move made before any workbench opened', () => {
    // The splash showing, no workbench: Zoom In from the menu reaches no page, so the first page to ask says the level.
    const app = fakeApp(0, 0)
    moveZoom('in', app.deps)
    expect(app.told).toEqual([])
    expect(new ZoomStart().ask(app.level())).toEqual({ level: 0.5, percent: 110, bound: '', announce: true })
  })
})

/** The app a move acts on, faked: the level and its saves, the windows it applies to, the workbench pages told, the menu's builds. */
function fakeApp(level: number, workbenches: number) {
  const saved: number[] = []
  const menus: number[] = []
  let applied = 0
  const told: ZoomStatus[][] = Array.from({ length: workbenches }, () => [])
  const deps = {
    level: () => level,
    save: (l: number) => {
      level = l
      saved.push(l)
    },
    apply: () => void applied++,
    workbenches: () =>
      told.map((page) => ({
        send: (channel: string, status: ZoomStatus) => {
          expect(channel).toBe('conductor:zoom')
          page.push(status)
        },
      })),
    menu: (l: number) => void menus.push(l),
  }
  return { deps, saved, menus, told, level: () => level, applied: () => applied }
}

describe('a zoom move', () => {
  it('keeps, applies, tells every workbench window and shows in the menu the level after it', () => {
    const app = fakeApp(0, 2)
    expect(moveZoom('in', app.deps)).toEqual({ level: 0.5, percent: 110, bound: '' })
    expect(app.saved).toEqual([0.5])
    expect(app.applied()).toBe(1)
    expect(app.told).toEqual([[zoomStatus(0.5)], [zoomStatus(0.5)]])
    expect(app.menus).toEqual([0.5])
  })
  it('at a bound changes and saves nothing, yet is told to every workbench window and shown in the menu', () => {
    const top = fakeApp(ZOOM_MAX, 2)
    moveZoom('in', top.deps)
    expect(top.saved).toEqual([])
    expect(top.level()).toBe(ZOOM_MAX)
    expect(top.applied()).toBe(1)
    expect(top.told).toEqual([[{ level: 5, percent: 249, bound: 'largest' }], [{ level: 5, percent: 249, bound: 'largest' }]])
    expect(top.menus).toEqual([5])
    const bottom = fakeApp(ZOOM_MIN, 2)
    moveZoom('out', bottom.deps)
    expect(bottom.saved).toEqual([])
    expect(bottom.told).toEqual([[{ level: -3, percent: 58, bound: 'smallest' }], [{ level: -3, percent: 58, bound: 'smallest' }]])
    expect(bottom.menus).toEqual([-3])
  })
  it('resets to 100% from any level', () => {
    const app = fakeApp(1.5, 1)
    moveZoom('reset', app.deps)
    expect(app.saved).toEqual([0])
    expect(app.told).toEqual([[{ level: 0, percent: 100, bound: '' }]])
    expect(app.menus).toEqual([0])
  })
})
