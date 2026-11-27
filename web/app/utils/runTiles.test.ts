import { describe, expect, it } from 'vitest'
import { ACTIVITY_AT_FIRST, RUN_TILES_CREWS, RUN_TILES_KEY, choiceFor, choose, crewOfRun, crewsForDoc, largeOf, parseRunTiles, readRunTiles, takeKept, withShares, writeRunTiles } from './runTiles'

describe('the run tiles record', () => {
  it("takes the server's last choice up, and a crew with a choice of its own keeps it", () => {
    const own = choose(parseRunTiles(null), 'refunds', { layout: 'large', activity: false })
    const lost = { ...own, last: { layout: 'grid' as const, activity: false } }
    const back = takeKept(lost, { layout: 'large', activity: true })
    expect(back.last).toEqual({ layout: 'large', activity: true })
    expect(choiceFor(back, 'docs')).toEqual({ layout: 'large', activity: true })
    expect(choiceFor(back, 'refunds')).toEqual({ layout: 'large', activity: false })
    // A layout this page does not draw, or nothing kept, leaves the record as it was.
    expect(takeKept(lost, { layout: 'mosaic' }).last).toEqual({ layout: 'grid', activity: false })
    expect(takeKept(lost, undefined)).toBe(lost)
    expect(takeKept(back, { layout: 'large', activity: true })).toBe(back)
  })

  it("takes each crew's choice from the server's copy, an entry that does not read left out", () => {
    const here = choose(parseRunTiles(null), 'refunds', { layout: 'grid', activity: false })
    const kept = {
      layout: 'rows',
      activity: true,
      crews: [
        { crew: 'docs', layout: 'large', activity: true, large: 'core', sizes: { large: { cols: [0.7, 0.3] } } },
        { crew: 'Bad Id', layout: 'grid', activity: true },
        { crew: 'web', layout: 'grid', activity: false, sizes: { grid: { cols: [1.5, 0.5] } } },
      ],
    }
    const back = takeKept(here, kept)
    expect(back.crews).toEqual([
      { crew: 'docs', layout: 'large', activity: true, large: 'core', sizes: { large: { cols: [0.7, 0.3] } } },
      { crew: 'web', layout: 'grid', activity: false, sizes: { grid: { cols: [1.5, 0.5] } } },
    ])
    expect(choiceFor(back, 'docs')).toMatchObject({ layout: 'large', large: 'core' })
    expect(choiceFor(back, 'refunds')).toEqual({ layout: 'rows', activity: true })
    expect(takeKept(back, kept)).toBe(back)
    // The layout document takes shares as fractions of their sum.
    expect(crewsForDoc(back.crews)[1]).toEqual({ crew: 'web', layout: 'grid', activity: false, sizes: { grid: { cols: [0.75, 0.25] } } })
    // The crew's own layout's shares alone; its other layouts' stay in the page and survive the copy's return.
    const mixed = [{ crew: 'a', layout: 'rows' as const, activity: false, sizes: { rows: { rows: [1, 2] }, grid: { cols: [0.5, 0.5] } } }]
    expect(crewsForDoc(mixed)[0]!.sizes).toEqual({ rows: { rows: [0.3333, 0.6667] } })
    expect(crewsForDoc([{ crew: 'b', layout: 'grid', activity: false, sizes: { rows: { rows: [1, 2] } } }])[0]).toEqual({ crew: 'b', layout: 'grid', activity: false })
    const mine = { last: back.last, crews: mixed }
    expect(takeKept(mine, { crews: crewsForDoc(mixed) }).crews[0]!.sizes).toEqual({ grid: { cols: [0.5, 0.5] }, rows: { rows: [0.3333, 0.6667] } })
    // A copy without crews (an older page wrote it) keeps the page's own.
    expect(takeKept(here, { layout: 'grid', activity: false }).crews).toEqual(here.crews)
  })

  it('is empty with Activity as at first when nothing is kept or it does not read', () => {
    for (const raw of [null, '', 'not json', '[]', '7', '{"last":{"activity":"yes"},"crews":"x"}']) {
      const r = parseRunTiles(raw)
      expect(r.crews).toEqual([])
      expect(r.last).toEqual({ layout: 'grid', activity: ACTIVITY_AT_FIRST })
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
    expect(r.last).toEqual({ layout: 'grid', activity: true })
    expect(r.crews).toEqual([
      { crew: 'refunds', layout: 'grid', activity: true },
      { crew: 'web', layout: 'grid', activity: false },
    ])
  })

  it('gives a crew not seen before the last choice made anywhere', () => {
    let r = parseRunTiles(null)
    expect(choiceFor(r, 'refunds')).toMatchObject({ activity: false })
    r = choose(r, 'refunds', { activity: true })
    expect(choiceFor(r, 'refunds')).toMatchObject({ activity: true })
    expect(choiceFor(r, 'never-opened')).toMatchObject({ activity: true })
    r = choose(r, 'docs', { activity: false })
    // refunds keeps its own; a new crew takes docs', the last made.
    expect(choiceFor(r, 'refunds')).toMatchObject({ activity: true })
    expect(choiceFor(r, 'another')).toMatchObject({ activity: false })
  })

  it('keeps at most 30 crews, the one chosen longest ago going first', () => {
    let r = parseRunTiles(null)
    for (let i = 0; i < RUN_TILES_CREWS + 5; i++) r = choose(r, `crew-${i}`, { activity: i % 2 === 0 })
    expect(r.crews).toHaveLength(RUN_TILES_CREWS)
    expect(r.crews[0]!.crew).toBe(`crew-${RUN_TILES_CREWS + 4}`)
    expect(r.crews.some((e) => e.crew === 'crew-0')).toBe(false)
    // A crew chosen again moves to the front and is kept once.
    r = choose(r, 'crew-10', { activity: true })
    expect(r.crews[0]).toMatchObject({ crew: 'crew-10', activity: true })
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
    expect(choiceFor(readRunTiles(storage), 'refunds')).toMatchObject({ activity: true })
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

  it('keeps the layout and the member drawn large for each crew, and a crew with none takes the last', () => {
    let r = parseRunTiles(null)
    expect(choiceFor(r, 'refunds')).toEqual({ layout: 'grid', activity: false })
    r = choose(r, 'refunds', { layout: 'large', large: 'core' })
    expect(choiceFor(r, 'refunds')).toEqual({ layout: 'large', activity: false, large: 'core' })
    expect(choiceFor(r, 'docs').layout).toBe('large')
    r = choose(r, 'docs', { layout: 'grid' })
    expect(choiceFor(r, 'refunds').layout).toBe('large')
    // Read back from its text, a layout that is not one is Grid and a name that is not one is none.
    const back = parseRunTiles(JSON.stringify(r))
    expect(choiceFor(back, 'refunds')).toEqual({ layout: 'large', activity: false, large: 'core' })
    const odd = parseRunTiles(JSON.stringify({ last: { activity: true, layout: 'spiral', large: 'Not A Name!' }, crews: [] }))
    expect(odd.last).toEqual({ layout: 'grid', activity: true })
  })

  it("draws large the member chosen while it is still a member, else the run's first", () => {
    expect(largeOf({ layout: 'large', activity: false, large: 'core' }, ['lead', 'core'])).toBe('core')
    expect(largeOf({ layout: 'large', activity: false, large: 'gone' }, ['lead', 'core'])).toBe('lead')
    expect(largeOf({ layout: 'large', activity: false }, ['lead', 'core'])).toBe('lead')
    expect(largeOf({ layout: 'large', activity: false }, [])).toBe('')
  })

  it("keeps the sizes of each layout's tracks for the crew, and leaves out a size that does not read", () => {
    let r = parseRunTiles(null)
    r = choose(r, 'refunds', withShares(choiceFor(r, 'refunds'), 'grid', 'cols', [0.123456, 0.876544]))
    r = choose(r, 'refunds', withShares(choiceFor(r, 'refunds'), 'grid', 'rows', [0.5, 0.5]))
    r = choose(r, 'refunds', withShares(choiceFor(r, 'refunds'), 'large', 'cols', [0.6, 0.4]))
    expect(choiceFor(r, 'refunds').sizes).toEqual({ grid: { cols: [0.1235, 0.8765], rows: [0.5, 0.5] }, large: { cols: [0.6, 0.4] } })
    expect(choiceFor(parseRunTiles(JSON.stringify(r)), 'refunds').sizes).toEqual(choiceFor(r, 'refunds').sizes)
    const odd = parseRunTiles(
      JSON.stringify({ last: { activity: false, sizes: { grid: { cols: [1], rows: [0.5, -1] }, spiral: { cols: [1, 1] }, rows: { rows: Array(13).fill(1) }, large: { cols: [1, 2] } } }, crews: [] }),
    )
    expect(odd.last.sizes).toEqual({ large: { cols: [1, 2] } })
  })
})
