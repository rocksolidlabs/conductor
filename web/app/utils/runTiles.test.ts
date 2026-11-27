import { describe, expect, it } from 'vitest'
import { ACTIVITY_AT_FIRST, RUN_TILES_CREWS, RUN_TILES_KEY, choiceFor, choose, crewOfRun, parseRunTiles, readRunTiles, writeRunTiles } from './runTiles'

describe('the run tiles record', () => {
  it('is empty with Activity as at first when nothing is kept or it does not read', () => {
    for (const raw of [null, '', 'not json', '[]', '7', '{"last":{"activity":"yes"},"crews":"x"}']) {
      const r = parseRunTiles(raw)
      expect(r.crews).toEqual([])
      expect(r.last).toEqual({ activity: ACTIVITY_AT_FIRST })
    }
    expect(ACTIVITY_AT_FIRST).toBe(false)
  })

  it('leaves out an entry that does not read, a crew id that is not one, and a crew named twice', () => {
    const r = parseRunTiles(
      JSON.stringify({
        last: { activity: true },
        crews: [{ crew: 'refunds', activity: true }, { crew: 'Bad Id', activity: true }, { crew: 'docs' }, null, { crew: 'refunds', activity: false }, { crew: 'web', activity: false }],
      }),
    )
    expect(r.last).toEqual({ activity: true })
    expect(r.crews).toEqual([
      { crew: 'refunds', activity: true },
      { crew: 'web', activity: false },
    ])
  })

  it('gives a crew not seen before the last choice made anywhere', () => {
    let r = parseRunTiles(null)
    expect(choiceFor(r, 'refunds')).toEqual({ activity: false })
    r = choose(r, 'refunds', { activity: true })
    expect(choiceFor(r, 'refunds')).toEqual({ activity: true })
    expect(choiceFor(r, 'never-opened')).toEqual({ activity: true })
    r = choose(r, 'docs', { activity: false })
    // refunds keeps its own; a new crew takes docs', the last made.
    expect(choiceFor(r, 'refunds')).toEqual({ activity: true })
    expect(choiceFor(r, 'another')).toEqual({ activity: false })
  })

  it('keeps at most 30 crews, the one chosen longest ago going first', () => {
    let r = parseRunTiles(null)
    for (let i = 0; i < RUN_TILES_CREWS + 5; i++) r = choose(r, `crew-${i}`, { activity: i % 2 === 0 })
    expect(r.crews).toHaveLength(RUN_TILES_CREWS)
    expect(r.crews[0]!.crew).toBe(`crew-${RUN_TILES_CREWS + 4}`)
    expect(r.crews.some((e) => e.crew === 'crew-0')).toBe(false)
    // A crew chosen again moves to the front and is kept once.
    r = choose(r, 'crew-10', { activity: true })
    expect(r.crews[0]).toEqual({ crew: 'crew-10', activity: true })
    expect(r.crews.filter((e) => e.crew === 'crew-10')).toHaveLength(1)
    // A stored record longer than the bound reads as the bound.
    const long = JSON.stringify({ last: { activity: true }, crews: Array.from({ length: 40 }, (_, i) => ({ crew: `c-${i}`, activity: true })) })
    expect(parseRunTiles(long).crews).toHaveLength(RUN_TILES_CREWS)
  })

  it('names the crew of a run by its crew id, else by the run id without its hex', () => {
    expect(crewOfRun('refund-split-b38a2385', 'refund-split')).toBe('refund-split')
    expect(crewOfRun('refund-split-b38a2385')).toBe('refund-split')
    expect(crewOfRun('odd')).toBe('odd')
  })

  it('reads and writes the page storage under its key, and a storage that throws loses nothing but the choice', () => {
    const kept = new Map<string, string>()
    const storage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v) }
    writeRunTiles(storage, choose(parseRunTiles(null), 'refunds', { activity: true }))
    expect(kept.has(RUN_TILES_KEY)).toBe(true)
    expect(choiceFor(readRunTiles(storage), 'refunds')).toEqual({ activity: true })
    const refusing = {
      getItem: () => {
        throw new Error('denied')
      },
      setItem: () => {
        throw new Error('denied')
      },
    }
    expect(readRunTiles(refusing).crews).toEqual([])
    expect(() => writeRunTiles(refusing, parseRunTiles(null))).not.toThrow()
    expect(readRunTiles(null).crews).toEqual([])
  })
})
