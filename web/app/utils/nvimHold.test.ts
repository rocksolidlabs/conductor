import { describe, expect, it } from 'vitest'
import { NvimEcho, type EchoDoc, type EchoLines } from './nvimEcho'
import { heldRefusal, NvimHolds } from './nvimHold'
import type { NvimViewState } from './nvimSwap'

const state: NvimViewState = { mode: 'n', cmdline: '', message: '', messageKind: '', swap: null, recovered: false, modified: true }

/** A follower's stop, counting its calls. */
function stopper() {
  const s = { calls: 0, stop: () => void s.calls++ }
  return s
}

describe('NvimHolds', () => {
  it('keeps a Neovim for its path until taken back, and stops following it then', () => {
    const h = new NvimHolds()
    const f = stopper()
    h.hold('/w/a.txt', { id: 'e1', state, cursor: { line: 3, col: 2 }, stop: f.stop })
    expect(h.peek('/w/a.txt')?.id).toBe('e1')
    expect(h.size).toBe(1)
    expect(f.calls).toBe(0)
    expect(h.take('/w/a.txt')).toEqual({ id: 'e1', state, cursor: { line: 3, col: 2 } })
    expect(f.calls).toBe(1)
    expect(h.take('/w/a.txt')).toBeUndefined()
    expect(h.size).toBe(0)
  })

  it("gives back Neovim's last cursor with the hold, for the next hold after the tab is shown and left again", () => {
    const h = new NvimHolds()
    h.hold('/w/a.txt', { id: 'e1', state, cursor: { line: 1, col: 4 }, at: { line: 1, byteCol: 3 } })
    const taken = h.take('/w/a.txt')
    expect(taken?.at).toEqual({ line: 1, byteCol: 3 })
    expect(taken?.cursor).toEqual({ line: 1, col: 4 })
  })

  it('forgets a Neovim that ended on its own', () => {
    const h = new NvimHolds()
    const f = stopper()
    h.hold('/w/a.txt', { id: 'e1', state, cursor: { line: 1, col: 1 }, stop: f.stop })
    h.hold('/w/b.txt', { id: 'e2', state, cursor: { line: 1, col: 1 } })
    h.ended('e1')
    expect(h.peek('/w/a.txt')).toBeUndefined()
    expect(f.calls).toBe(1)
    expect(h.peek('/w/b.txt')?.id).toBe('e2')
  })

  it('keeps a Neovim lost, not quit, as lost: its state with the reason, still followed for the changes it sent before its end', () => {
    const h = new NvimHolds()
    const f = stopper()
    h.hold('/w/a.txt', { id: 'e1', state: { ...state, cmdline: ':w' }, cursor: { line: 2, col: 3 }, stop: f.stop })
    h.ended('e1', 'error: neovim ended')
    expect(h.peek('/w/a.txt')).toMatchObject({ id: 'e1', state: { ...state, cmdline: '', lost: 'error: neovim ended' }, cursor: { line: 2, col: 3 } })
    expect(f.calls).toBe(0)
    // A second end, or a quit, changes nothing: it is lost.
    h.ended('e1', 'quit')
    expect(h.peek('/w/a.txt')?.state.lost).toBe('error: neovim ended')
    // Its tab closed: handed back as lost (nothing to end), no longer followed.
    expect(h.orphans([])).toEqual([{ path: '/w/a.txt', id: 'e1', lost: true, seq: 0, unsettled: false }])
    expect(f.calls).toBe(1)
  })

  it('counts the characters a lost Neovim never had among its changes not written', () => {
    const h = new NvimHolds()
    const text = ['alpha']
    const echo = new NvimEcho({ line: (n) => text[n - 1] ?? '', setLine: (n, t) => void (text[n - 1] = t), applyLines: () => {} })
    echo.cursor({ line: 1, col: 6, mode: 'i', ack: 0 }, (_l, c) => c)
    echo.send('x', { line: 1, col: 6 })
    echo.finish()
    h.hold('/w/a.txt', { id: 'e1', state: { ...state, modified: false }, cursor: { line: 1, col: 7 }, echo })
    h.ended('e1', 'error: neovim ended')
    expect(h.peek('/w/a.txt')?.state.modified).toBe(true)
  })

  it('forgets a Neovim that quit', () => {
    const h = new NvimHolds()
    h.hold('/w/a.txt', { id: 'e1', state, cursor: { line: 1, col: 1 } })
    h.ended('e1', 'quit')
    expect(h.peek('/w/a.txt')).toBeUndefined()
  })

  it('hands back the Neovims whose tab closed, and only those, with what their end needs', () => {
    const h = new NvimHolds()
    const clean = { ...state, modified: false }
    const text = ['alpha']
    const echo = (typed: string, settledTo = 0) => {
      const e = new NvimEcho({ line: (n) => text[n - 1] ?? '', setLine: (n, t) => void (text[n - 1] = t), applyLines: () => {} })
      e.cursor({ line: 1, col: 6, mode: typed === 'j' ? 'n' : 'i', ack: 0 }, (_l, c) => c)
      e.send(typed, { line: 1, col: 6 })
      e.noteSettled(settledTo)
      return e
    }
    h.hold('/w/a.txt', { id: 'e1', state, cursor: { line: 1, col: 1 } })
    h.hold('/w/b.txt', { id: 'e2', state, cursor: { line: 1, col: 1 } })
    expect(h.orphans(['/w/b.txt', '/w/c.txt'])).toEqual([{ path: '/w/a.txt', id: 'e1', lost: false, seq: 0, unsettled: false }])
    expect(h.peek('/w/a.txt')).toBeUndefined()
    expect(h.orphans(['/w/b.txt'])).toEqual([])
    // Keys it has not done: it ends once it has (a character typed among them, or not).
    h.hold('/w/typed.txt', { id: 'e3', state: clean, cursor: { line: 1, col: 1 }, echo: echo('x') })
    h.hold('/w/moved.txt', { id: 'e4', state: clean, cursor: { line: 1, col: 1 }, echo: echo('j') })
    h.hold('/w/done.txt', { id: 'e5', state: clean, cursor: { line: 1, col: 1 }, echo: echo('j', 1) })
    expect(h.orphans([])).toEqual([
      { path: '/w/b.txt', id: 'e2', lost: false, seq: 0, unsettled: false },
      { path: '/w/typed.txt', id: 'e3', lost: false, seq: 1, unsettled: true },
      { path: '/w/moved.txt', id: 'e4', lost: false, seq: 1, unsettled: true },
      { path: '/w/done.txt', id: 'e5', lost: false, seq: 1, unsettled: false },
    ])
    expect(h.size).toBe(0)
  })

  it('tells its listeners of each hold made, changed, ended and let go', () => {
    const h = new NvimHolds()
    const told: Array<[string, boolean | undefined]> = []
    const off = h.listen((path, held) => told.push([path, held?.state.modified]))
    const s = { ...state, modified: false }
    h.hold('/w/a.txt', { id: 'e1', state: s, cursor: { line: 1, col: 1 } })
    s.modified = true
    h.changed('/w/a.txt')
    h.changed('/w/none.txt')
    h.ended('e1', 'error: neovim ended')
    expect(h.take('/w/a.txt')?.id).toBe('e1')
    off()
    h.hold('/w/b.txt', { id: 'e2', state, cursor: { line: 1, col: 1 } })
    expect(told).toEqual([
      ['/w/a.txt', false],
      ['/w/a.txt', true],
      ['/w/a.txt', true],
      ['/w/a.txt', undefined],
    ])
  })

  it('tells its listeners of the holds its orphans and clear let go', () => {
    const h = new NvimHolds()
    const told: string[] = []
    h.hold('/w/a.txt', { id: 'e1', state, cursor: { line: 1, col: 1 } })
    h.hold('/w/b.txt', { id: 'e2', state, cursor: { line: 1, col: 1 } })
    h.listen((path, held) => told.push(`${path} ${held ? 'held' : 'gone'}`))
    h.orphans(['/w/b.txt'])
    h.clear()
    expect(told).toEqual(['/w/a.txt gone', '/w/b.txt gone'])
  })

  it('says whether a kept Neovim, not lost, has keys it has not said it has done', () => {
    const h = new NvimHolds()
    const text = ['alpha']
    const echo = new NvimEcho({ line: (n) => text[n - 1] ?? '', setLine: (n, t) => void (text[n - 1] = t), applyLines: () => {} })
    echo.send('j', { line: 1, col: 1 })
    h.hold('/w/a.txt', { id: 'e1', state, cursor: { line: 1, col: 1 }, echo })
    expect(h.busy()).toBe(true)
    echo.noteSettled(1)
    expect(h.busy()).toBe(false)
    echo.send('k', { line: 1, col: 1 })
    h.ended('e1', 'error: neovim ended')
    expect(h.busy()).toBe(false)
  })

  it('stops following what it clears, and what another hold of the same path replaces', () => {
    const h = new NvimHolds()
    const a = stopper()
    const b = stopper()
    h.hold('/w/a.txt', { id: 'e1', state, cursor: { line: 1, col: 1 }, stop: a.stop })
    h.hold('/w/a.txt', { id: 'e2', state, cursor: { line: 1, col: 1 }, stop: b.stop })
    expect(a.calls).toBe(1)
    h.clear()
    expect(b.calls).toBe(1)
    expect(h.size).toBe(0)
  })
})

/** A fake editor: lines, and a lines event applied as the editor applies it. */
function doc(...lines: string[]): EchoDoc & { text: string[] } {
  const d = {
    text: [...lines],
    line: (n: number) => d.text[n - 1] ?? '',
    setLine: (n: number, t: string) => {
      d.text[n - 1] = t
    },
    applyLines: (ev: EchoLines) => {
      const last = ev.last < 0 ? d.text.length : ev.last
      d.text.splice(ev.first, last - ev.first, ...ev.lines)
    },
  }
  return d
}

/** xyz typed in insert mode after alpha, none of them acknowledged: the echo as the tab was left, held. */
function heldWithTyped() {
  const d = doc('alpha')
  const e = new NvimEcho(d)
  e.cursor({ line: 1, col: 6, mode: 'i', ack: 0 }, (_line, byteCol) => byteCol)
  e.send('x', { line: 1, col: 6 })
  e.send('y', { line: 1, col: 7 })
  e.send('z', { line: 1, col: 8 })
  expect(d.text).toEqual(['alphaxyz'])
  const holds = new NvimHolds()
  holds.hold('/w/a.txt', { id: 'e1', state: { ...state, modified: false }, cursor: { line: 1, col: 9 }, echo: e })
  return { d, e, holds }
}

describe('heldRefusal', () => {
  it("takes refused keys off a held Neovim's text and names them on its status line", () => {
    const { d, e, holds } = heldWithTyped()
    const h = holds.peek('/w/a.txt')!
    expect(heldRefusal(h, e, { ack: 2, code: 'too_many_requests', message: 'session: too many requests' })).toBe(true)
    expect(d.text).toEqual(['alphaxz'])
    expect(h.state.refused).toBe('y')
    expect(h.state.message).toBe('Keys did not reach Neovim (session: too many requests): y')
    expect(h.state.messageKind).toBe('emsg')
  })

  it('leaves a lost Neovim alone: a refusal after its end keeps every character it never had over its text, named (#429)', () => {
    const { d, e, holds } = heldWithTyped()
    holds.ended('e1', 'error: neovim ended')
    const h = holds.peek('/w/a.txt')!
    expect(h.state.unconfirmed).toBe('xyz')
    // Keys that reached the server once the dead Neovim's editor was let go: refused, after its end.
    expect(heldRefusal(h, e, { ack: 2, code: 'file_denied', message: 'session: file access denied' })).toBe(false)
    expect(d.text).toEqual(['alphaxyz'])
    expect(e.showing).toBe(true)
    expect(e.unconfirmed).toBe('xyz')
    expect(h.state.refused).toBeUndefined()
    expect(h.state.message).toBe('')
  })
})
