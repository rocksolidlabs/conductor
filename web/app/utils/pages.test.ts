import { describe, expect, it } from 'vitest'
import { GLOBAL_SHORTCUTS } from '~/composables/useShortcuts'
import { PAGES, navPages, navTiles, pageActive, pageById, pageTip, palettePages, phoneMore, phoneTabs, yardRoute, type PageId } from './pages'

const ids = (ps: ReadonlyArray<{ id: string }>) => ps.map((p) => p.id)
const labels = (ps: ReadonlyArray<{ label: string }>) => ps.map((p) => p.label)

describe('the pages in one table', () => {
  it("gives the sidebar's foot: Brakeman while the server has it, Settings in the desktop app, and no view of another page", () => {
    const base = ['Needs you', 'Yard', 'Crews', 'Dispatch', 'Agents', 'History']
    expect(labels(navPages())).toEqual(base)
    expect(labels(navPages({ brakeman: true }))).toEqual(['Needs you', 'Yard', 'Crews', 'Dispatch', 'Brakeman', 'Agents', 'History'])
    expect(labels(navPages({ desktop: true }))).toEqual([...base, 'Settings'])
    expect(labels(navPages({ brakeman: true, desktop: true }))).toEqual(['Needs you', 'Yard', 'Crews', 'Dispatch', 'Brakeman', 'Agents', 'History', 'Settings'])
    expect(navPages().map((p) => p.to)).toEqual(['/decisions', '/yard', '/crews', '/dispatch', '/agents', '/history'])
    for (const view of ['roundhouse', 'history-feed', 'agents-reporting', 'paste']) expect(ids(navPages({ brakeman: true, desktop: true }))).not.toContain(view)

    // Needs you's row leaves when nothing waits and nothing was decided that day (needsRowShown); the rest keep their places.
    expect(labels(navPages({ needs: false, desktop: true }))).toEqual([...base.slice(1), 'Settings'])
  })

  it("gives the phone's bar and its More, the Roundhouse in neither", () => {
    expect(labels(phoneTabs())).toEqual(['Needs you', 'Sessions', 'Yard', 'Crews'])
    expect(phoneTabs().map((p) => p.to)).toEqual(['/decisions', '/sessions', '/yard', '/crews'])
    expect(labels(phoneMore())).toEqual(['Dispatch', 'Agents', 'History'])
    expect(labels(phoneMore({ desktop: true }))).toEqual(['Dispatch', 'Agents', 'History', 'Settings'])
  })

  it("gives the phone's More Brakeman after Dispatch while the server has it, so every page is reached from the phone", () => {
    expect(labels(phoneMore({ brakeman: true }))).toEqual(['Dispatch', 'Brakeman', 'Agents', 'History'])
    expect(labels(phoneMore({ brakeman: true, desktop: true }))).toEqual(['Dispatch', 'Brakeman', 'Agents', 'History', 'Settings'])
    expect(labels(phoneMore({ brakeman: false }))).toEqual(['Dispatch', 'Agents', 'History'])
  })

  it("gives the full sidebar's foot as six tiles, Brakeman's slot held while the server has not said, the rest under More", () => {
    const cells = (s: Parameters<typeof navTiles>[0]) => navTiles(s).tiles.map((t) => (t.kind === 'page' ? t.page.label : t.kind))
    // While the server has not said: an empty cell of a tile's size in Brakeman's place, so More does not move when it fills.
    expect(cells({})).toEqual(['Needs you', 'Yard', 'Crews', 'Dispatch', 'held', 'more'])
    expect(cells({ brakeman: true })).toEqual(['Needs you', 'Yard', 'Crews', 'Dispatch', 'Brakeman', 'more'])
    // A server without Brakeman: More moves into its slot.
    expect(cells({ brakeman: false })).toEqual(['Needs you', 'Yard', 'Crews', 'Dispatch', 'more'])
    // A switchyard serves no inbox: no Needs you tile, the next ones close up.
    expect(cells({ brakeman: false, switchyard: true })).toEqual(['Yard', 'Crews', 'Dispatch', 'more'])
    expect(cells({ brakeman: true, switchyard: true })).toEqual(['Yard', 'Crews', 'Dispatch', 'Brakeman', 'more'])
    // More holds the rail's other pages in the rail's order, Settings in the desktop app alone, never the Roundhouse or Brakeman.
    expect(labels(navTiles({ brakeman: true }).more)).toEqual(['Agents', 'History'])
    expect(labels(navTiles({ brakeman: false }).more)).toEqual(['Agents', 'History'])
    expect(labels(navTiles({}).more)).toEqual(['Agents', 'History'])
    expect(labels(navTiles({ brakeman: true, desktop: true }).more)).toEqual(['Agents', 'History', 'Settings'])
    // Every page of the rail is a tile or under More: nothing the rail offers is lost.
    for (const s of [{ brakeman: true }, { brakeman: true, desktop: true }, { brakeman: false, desktop: true }]) {
      const { tiles, more } = navTiles(s)
      const shown = [...tiles.flatMap((t) => (t.kind === 'page' ? [t.page.id] : [])), ...ids(more)]
      expect(shown.sort()).toEqual(ids(navPages(s)).sort())
    }
  })

  it("makes the Roundhouse the Yard's second view: a palette row with G R, the Yard current on both routes", () => {
    const rh = palettePages().find((p) => p.id === 'roundhouse')
    expect(rh?.chord).toEqual(['G', 'R'])
    expect(rh?.to).toBe('/roundhouse')
    expect(rh?.about).toBe("The Yard's second view: one terminal at a time, on rotation.")
    expect(pageById('roundhouse').viewOf).toBe('yard')
    const cases: Array<[string, boolean]> = [
      ['/yard', true],
      ['/yard?focus=s1', true],
      ['/roundhouse', true],
      ['/roundhouse?x=1', true],
      ['/yardx', false],
      ['/roundhousex', false],
      ['/runs/r1', false],
      ['/sessions/s1', false],
    ]
    expect(cases.filter(([path, want]) => yardRoute(path) !== want)).toEqual([])
    expect(pageActive('yard', '/roundhouse')).toBe(true)
    expect(pageActive('sessions', '/roundhouse')).toBe(false)
  })

  it("gives the palette's page rows in their order, by the page's own name", () => {
    const go = ['decisions', 'yard', 'roundhouse', 'crews', 'dispatch', 'agents', 'agents-reporting', 'history', 'history-feed', 'paste']
    expect(ids(palettePages())).toEqual(go)
    expect(ids(palettePages({ desktop: true }))).toEqual([...go, 'settings'])
    expect(palettePages()[0]!.title).toBe('Needs you')
    expect(palettePages().map((p) => p.title)).toEqual(['Needs you', 'Yard', 'Roundhouse', 'Crews', 'Dispatch', 'Agents', 'Agents · Reporting', 'History', 'History · Live', 'Paste invite'])
    expect(palettePages().map((p) => p.chord?.join(' '))).toEqual(['G N', 'G Y', 'G R', 'G C', 'G D', 'G A', undefined, 'G H', 'G E', undefined])
  })

  it("retires the Events page: no entry of it, G E is History's Feed, and the Feed is a view of History", () => {
    expect(PAGES.map((p) => p.id as string)).not.toContain('events')
    expect(PAGES.filter((p) => p.to.startsWith('/events'))).toEqual([])
    const feed = PAGES.find((p) => p.chord?.join(' ') === 'G E')!
    expect(feed.id).toBe('history-feed')
    expect(feed.to).toBe('/history?view=feed')
    expect(feed.viewOf).toBe('history')
    expect(GLOBAL_SHORTCUTS.rows.find((r) => r.keys.join(' ') === 'G E')!.label).toBe("Go to History · Live")
  })

  it('lights a page on its own paths and on no path that only starts with its letters', () => {
    const cases: Array<[PageId, string, boolean]> = [
      ['crews', '/crews', true],
      ['crews', '/crews/c-1', true],
      ['crews', '/runs/r-1', true],
      ['crews', '/crewsx', false],
      ['crews', '/runsx', false],
      ['dispatch', '/dispatch', true],
      ['dispatch', '/dispatch/CND', true],
      ['dispatch', '/dispatch/CND/4', true],
      ['dispatch', '/dispatchx', false],
      ['history', '/history', true],
      ['history', '/history/s-1', true],
      ['history', '/historyx', false],
      ['sessions', '/', true],
      ['sessions', '/sessions', true],
      ['sessions', '/sessions/s-1', true],
      ['sessions', '/sessionsx', false],
      ['sessions', '/yard', false],
      ['yard', '/yard', true],
      ['yard', '/yardx', false],
      ['yard', '/roundhouse', true],
      ['decisions', '/decisions', true],
      ['decisions', '/decide', false],
      ['settings', '/settings', true],
      ['history', '/', false],
    ]
    expect(cases.filter(([id, path, want]) => pageActive(id, path) !== want)).toEqual([])
  })

  it('gives every page one sentence of its own, ending in a full stop', () => {
    for (const p of PAGES) {
      expect(p.about, p.id).toMatch(/^[A-Z][^.]*\.$/)
      expect(p.about.toLowerCase().startsWith(p.label.toLowerCase()), p.id).toBe(false)
    }
    expect(new Set(PAGES.map((p) => p.about)).size).toBe(PAGES.length)
    expect(new Set(PAGES.map((p) => p.id)).size).toBe(PAGES.length)
  })

  it("says the page in the nav's tooltip", () => {
    expect(pageTip(pageById('yard'))).toBe('Yard: every live terminal on one screen')
    expect(pageTip(pageById('agents'))).toBe('Agents: the agents this machine can launch, and how each reports to Conductor')
    expect(pageTip(pageById('decisions'))).toBe('Needs you: everything that waits for your answer, in one list')
  })

  it('gives Brakeman the key G B, and no place in Go to: its rows are its own group', () => {
    expect(pageById('brakeman').chord).toEqual(['G', 'B'])
    expect(ids(palettePages())).not.toContain('brakeman')
  })

  it("gives every page's key as a row of the shortcuts dialog, and no two pages one key", () => {
    const rows = GLOBAL_SHORTCUTS.rows.map((r) => r.keys.join(' '))
    const chords = PAGES.filter((p) => p.chord).map((p) => p.chord!.join(' '))
    expect(chords.filter((c) => !rows.includes(c))).toEqual([])
    expect(new Set(chords).size).toBe(chords.length)
    // Each page's key row names the page it goes to.
    for (const p of PAGES.filter((p) => p.chord)) {
      const row = GLOBAL_SHORTCUTS.rows.find((r) => r.keys.join(' ') === p.chord!.join(' '))!
      expect(row.label.toLowerCase(), p.id).toContain(p.label.toLowerCase())
    }
  })
})
