import { describe, expect, it } from 'vitest'
import { GLOBAL_SHORTCUTS } from '~/composables/useShortcuts'
import { PAGES, navPages, pageActive, pageById, pageTip, palettePages, phoneMore, phoneTabs, type PageId } from './pages'

const ids = (ps: ReadonlyArray<{ id: string }>) => ps.map((p) => p.id)
const labels = (ps: ReadonlyArray<{ label: string }>) => ps.map((p) => p.label)

describe('the pages in one table', () => {
  it("gives the sidebar's foot as it was: Brakeman while the server has it, Settings in the desktop app", () => {
    const base = ['Needs you', 'Yard', 'Roundhouse', 'Agents', 'Crews', 'Dispatch', 'Events', 'History']
    expect(labels(navPages())).toEqual(base)
    expect(labels(navPages({ brakeman: true }))).toEqual([...base, 'Brakeman'])
    expect(labels(navPages({ desktop: true }))).toEqual([...base, 'Settings'])
    expect(labels(navPages({ brakeman: true, desktop: true }))).toEqual([...base, 'Brakeman', 'Settings'])
    expect(navPages().map((p) => p.to)).toEqual(['/decisions', '/yard', '/roundhouse', '/agents', '/crews', '/dispatch', '/events', '/history'])
    // Needs you's row leaves when nothing waits and nothing was decided that day (needsRowShown); the rest keep their places.
    expect(labels(navPages({ needs: false, desktop: true }))).toEqual([...base.slice(1), 'Settings'])
  })

  it("gives the phone's bar and its More as they were", () => {
    expect(labels(phoneTabs())).toEqual(['Needs you', 'Sessions', 'Yard', 'Crews'])
    expect(phoneTabs().map((p) => p.to)).toEqual(['/decisions', '/sessions', '/yard', '/crews'])
    expect(labels(phoneMore())).toEqual(['Roundhouse', 'Agents', 'Dispatch', 'Events', 'History'])
    expect(labels(phoneMore({ desktop: true }))).toEqual(['Roundhouse', 'Agents', 'Dispatch', 'Events', 'History', 'Settings'])
  })

  it("gives the palette's page rows in their order, by the page's own name", () => {
    expect(ids(palettePages())).toEqual(['decisions', 'yard', 'roundhouse', 'crews', 'dispatch', 'agents', 'events', 'history'])
    expect(ids(palettePages({ desktop: true }))).toEqual(['decisions', 'yard', 'roundhouse', 'crews', 'dispatch', 'agents', 'events', 'history', 'settings'])
    expect(palettePages()[0]!.title).toBe('Needs you')
    expect(palettePages().map((p) => p.chord)).toEqual([['G', 'N'], ['G', 'Y'], ['G', 'R'], ['G', 'C'], ['G', 'D'], ['G', 'A'], ['G', 'E'], ['G', 'H']])
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
      ['decisions', '/decisions', true],
      ['decisions', '/decide', false],
      ['settings', '/settings', true],
      ['events', '/', false],
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

  it("gives every page's key as a row of the shortcuts dialog, and no two pages one key", () => {
    const rows = GLOBAL_SHORTCUTS.rows.map((r) => r.keys.join(' '))
    const chords = PAGES.filter((p) => p.chord).map((p) => p.chord!.join(' '))
    expect(chords.filter((c) => !rows.includes(c))).toEqual([])
    expect(new Set(chords).size).toBe(chords.length)
    // Each page's key row names the page it goes to.
    for (const p of PAGES.filter((p) => p.chord)) {
      const row = GLOBAL_SHORTCUTS.rows.find((r) => r.keys.join(' ') === p.chord!.join(' '))!
      expect(row.label.toLowerCase(), p.id).toContain(p.title.toLowerCase())
    }
  })
})
