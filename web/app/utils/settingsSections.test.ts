import { describe, expect, it } from 'vitest'
import { SETTINGS_SECTIONS, sectionFromQuery, sectionInView, shownSections, type SectionBox, type SettingsShown } from './settingsSections'

const ids = (s: SettingsShown) => shownSections(s).map((x) => x.id)
const desktop: SettingsShown = { desktop: true, platform: 'linux', ice: false, agents: 13, brakeman: true }

describe('shownSections', () => {
  it('shows every section of the desktop app on Windows with an ICE status, in page order', () => {
    expect(ids({ ...desktop, platform: 'win32', ice: true })).toEqual(['webrtc', 'switchyard', 'folders', 'running', 'agents', 'accounts', 'brakeman', 'app'])
  })

  it('shows WebRTC from WSL only on Windows, and only once the bridge said what it forwards', () => {
    expect(ids(desktop)).not.toContain('webrtc')
    expect(ids({ ...desktop, ice: true })).not.toContain('webrtc')
    expect(ids({ ...desktop, platform: 'win32', ice: false })).not.toContain('webrtc')
    expect(ids({ ...desktop, platform: 'win32', ice: true })[0]).toBe('webrtc')
  })

  it('shows the agents only for a catalog that lists some', () => {
    expect(ids({ ...desktop, agents: 0 })).toEqual(['switchyard', 'folders', 'running', 'accounts', 'brakeman', 'app'])
    expect(ids({ ...desktop, agents: 1 })).toContain('agents')
  })

  it('shows Brakeman only where the server offers it', () => {
    expect(ids({ ...desktop, brakeman: false })).toEqual(['switchyard', 'folders', 'running', 'agents', 'accounts', 'app'])
  })

  it('shows the accounts unless the server offers none (a switchyard, no data directory), in the browser too', () => {
    expect(ids({ ...desktop, accounts: false })).toEqual(['switchyard', 'folders', 'running', 'agents', 'brakeman', 'app'])
    expect(ids({ ...desktop, accounts: true })).toContain('accounts')
    expect(ids({ desktop: false, ice: false, agents: 0, brakeman: false, accounts: true })).toEqual(['accounts', 'app'])
  })

  it("shows only Accounts, Brakeman and The app on the browser's page, whatever else is known", () => {
    expect(ids({ desktop: false, platform: 'win32', ice: true, agents: 13, brakeman: true })).toEqual(['accounts', 'brakeman', 'app'])
    expect(ids({ desktop: false, ice: false, agents: 0, brakeman: false, accounts: false })).toEqual(['app'])
  })

  it('keeps the ids and titles the address and the docs name', () => {
    expect(SETTINGS_SECTIONS.map((s) => s.id)).toEqual(['webrtc', 'switchyard', 'folders', 'running', 'agents', 'accounts', 'brakeman', 'app'])
    // "Settings → Switchyard" and "Settings → The app" are written in the README, the switchyard's pages and the notice.
    expect(SETTINGS_SECTIONS.find((s) => s.id === 'switchyard')!.title).toBe('Switchyard')
    expect(SETTINGS_SECTIONS.find((s) => s.id === 'app')!.title).toBe('The app')
    for (const s of SETTINGS_SECTIONS) expect(s.icon).toMatch(/^i-lucide-[a-z-]+$/)
  })
})

describe('sectionFromQuery', () => {
  it('reads a known id', () => {
    for (const s of SETTINGS_SECTIONS) expect(sectionFromQuery(s.id)).toBe(s.id)
  })

  it('reads none for an unknown id, an empty one or none at all', () => {
    expect(sectionFromQuery('network')).toBeNull()
    expect(sectionFromQuery('Switchyard')).toBeNull()
    expect(sectionFromQuery('')).toBeNull()
    expect(sectionFromQuery(undefined)).toBeNull()
    expect(sectionFromQuery(null)).toBeNull()
  })

  it('reads the first of an id given twice', () => {
    expect(sectionFromQuery(['switchyard', 'app'])).toBe('switchyard')
    expect(sectionFromQuery(['switchyard', 'switchyard'])).toBe('switchyard')
    expect(sectionFromQuery(['nope', 'app'])).toBeNull()
    expect(sectionFromQuery([null, 'app'])).toBeNull()
    expect(sectionFromQuery([])).toBeNull()
  })
})

describe('sectionInView', () => {
  // Three sections of 600, 400 and 200 px, 20 px apart, in a body 500 px tall that holds 1240 px.
  const boxes: SectionBox[] = [
    { id: 'switchyard', top: 0, bottom: 600 },
    { id: 'folders', top: 620, bottom: 1020 },
    { id: 'app', top: 1040, bottom: 1240 },
  ]
  const view = (scrollTop: number, cover = 0) => ({ scrollTop, clientHeight: 500, scrollHeight: 1240, cover })

  it('marks the first section at the top, and none when there is none', () => {
    expect(sectionInView(boxes, view(0))).toBe('switchyard')
    expect(sectionInView([], view(0))).toBeNull()
  })

  it('marks the last section whose top has passed a quarter of the way down the visible part', () => {
    expect(sectionInView(boxes, view(400))).toBe('switchyard') // the line at 525
    expect(sectionInView(boxes, view(500))).toBe('folders') // the line at 625
    expect(sectionInView(boxes, view(620))).toBe('folders')
  })

  it('marks the last section once the body is scrolled to its end', () => {
    expect(sectionInView(boxes, view(740))).toBe('app')
  })

  it('does not count what the sticky row of links covers as in view', () => {
    expect(sectionInView(boxes, view(470))).toBe('switchyard') // the line at 470 + 125 = 595
    expect(sectionInView(boxes, view(470, 60))).toBe('folders') // the line at 530 + 110 = 640
  })

  it('keeps a section just chosen while any of it is visible, though it cannot reach the top', () => {
    expect(sectionInView(boxes, view(740), 'folders')).toBe('folders')
    expect(sectionInView(boxes, view(620), 'folders')).toBe('folders')
    // Scrolled away from it, the section in view counts again.
    expect(sectionInView(boxes, view(0), 'app')).toBe('switchyard')
    expect(sectionInView(boxes, view(620), 'switchyard')).toBe('folders')
    // A section chosen that is not shown counts for nothing.
    expect(sectionInView(boxes, view(0), 'agents')).toBe('switchyard')
  })

  it('does not take an unscrolled body that holds all as scrolled to its end', () => {
    const short: SectionBox[] = [{ id: 'brakeman', top: 0, bottom: 200 }, { id: 'app', top: 220, bottom: 400 }]
    expect(sectionInView(short, { scrollTop: 0, clientHeight: 500, scrollHeight: 400 })).toBe('brakeman')
  })
})
