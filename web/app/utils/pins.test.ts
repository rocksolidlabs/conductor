import { describe, expect, it } from 'vitest'
import type { SessionInfo } from '~/composables/useSessions'
import { isPinned, PIN_GONE_MS, pinRows, pinSession, PINS_KEY, PINS_MAX, readPins, unpinSession, writePins, type Pin } from './pins'

const session = (id: string, name = id): SessionInfo => ({ id, name, agentId: 'claude' }) as SessionInfo

describe('pins', () => {
  it('pins a session first, once, and unpins it', () => {
    let pins = pinSession([], session('s1', 'one'), 1000)
    pins = pinSession(pins, session('s2', 'two'), 2000)
    expect(pins.map((p) => p.session)).toEqual(['s2', 's1'])
    expect(pins[1]).toEqual({ session: 's1', name: 'one', agent: 'claude', at: 1000 })
    expect(pinSession(pins, session('s1'), 3000)).toEqual(pins)
    expect(isPinned(pins, 's1')).toBe(true)
    expect(unpinSession(pins, 's1').map((p) => p.session)).toEqual(['s2'])
  })

  it(`keeps at most ${PINS_MAX}, the oldest pin going`, () => {
    let pins: Pin[] = []
    for (let i = 0; i <= PINS_MAX; i++) pins = pinSession(pins, session(`s${i}`), i)
    expect(pins).toHaveLength(PINS_MAX)
    expect(isPinned(pins, 's0')).toBe(false)
    expect(pins[0]!.session).toBe(`s${PINS_MAX}`)
  })

  it('draws a pin no longer listed greyed, and lets it go a week past its pinning', () => {
    const now = 10 * PIN_GONE_MS
    const pins: Pin[] = [
      { session: 'live', name: 'live', agent: 'claude', at: 0 },
      { session: 'recent', name: 'ended lately', agent: 'codex', at: now - 1000 },
      { session: 'old', name: 'ended long ago', agent: 'codex', at: now - PIN_GONE_MS - 1 },
    ]
    const { rows, kept } = pinRows(pins, [session('live')], now)
    expect(rows.map((r) => [r.pin.session, r.gone])).toEqual([
      ['live', false],
      ['recent', true],
    ])
    expect(rows[0]!.session?.id).toBe('live')
    expect(kept.map((p) => p.session)).toEqual(['live', 'recent'])
  })

  it('reads what it writes, and nothing of another shape', () => {
    const m = new Map<string, string>()
    const s = { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v) }
    const pins = pinSession([], session('s1'), 5)
    writePins(s, pins)
    expect(readPins(s)).toEqual(pins)
    m.set(PINS_KEY, JSON.stringify([{ session: '' }, { session: 's2', name: 'x', agent: 'a', at: 'soon' }, ...pins]))
    expect(readPins(s)).toEqual(pins)
    m.set(PINS_KEY, 'not json')
    expect(readPins(s)).toEqual([])
    expect(readPins(null)).toEqual([])
  })
})
