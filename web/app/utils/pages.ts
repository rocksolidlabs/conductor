/**
 * The app's pages in one table (the UX review's X-pages): a page's name, its one sentence, its key and where it stands. The nav and its
 * rail, the phone's bar and its More, and the palette's Go to read it through the functions below; none keeps a list of its own.
 */

export type PageId =
  | 'decisions'
  | 'yard'
  | 'roundhouse'
  | 'agents'
  | 'agents-reporting'
  | 'crews'
  | 'dispatch'
  | 'history'
  | 'history-feed'
  | 'brakeman'
  | 'sessions'
  | 'paste'
  | 'settings'

export interface PageEntry {
  id: PageId
  /** The nav's word, and the phone's. */
  label: string
  /** The page's own name: the palette's row and the rail's label. */
  title: string
  to: string
  icon: string
  /** The page's key outside a terminal, as the shortcuts dialog lists it (GLOBAL_SHORTCUTS); none for a page without one. */
  chord?: readonly string[]
  /** What the page is, in one sentence: the nav's tooltip and the palette's second line. */
  about: string
  /** Where the page is offered: everywhere, in the desktop app alone, or while the server has Brakeman. */
  when: 'always' | 'desktop' | 'brakeman'
  /**
   * Its place on each surface, in order; a surface it has no place on leaves it out. `nav` is the rail's, `tile` the full sidebar's
   * tiles (a page of the rail with no tile is under the tiles' More).
   */
  place: { nav?: number; tile?: number; tab?: number; more?: number; palette?: number }
  /**
   * The page this one is a view of (the Roundhouse is the Yard's second view, the Feed History's, Reporting a section of Agents): the
   * nav, the rail and the phone's bar and More leave it out, the palette keeps its row and key, and the page it is a view of is
   * current on its route (pageActive).
   */
  viewOf?: PageId
}

export const PAGES: readonly PageEntry[] = [
  {
    id: 'decisions',
    label: 'Needs you',
    title: 'Needs you',
    to: '/decisions',
    icon: 'i-lucide-gavel',
    chord: ['G', 'N'],
    about: 'Everything that waits for your answer, in one list.',
    when: 'always',
    place: { nav: 1, tile: 1, tab: 1, palette: 1 },
  },
  { id: 'sessions', label: 'Sessions', title: 'Sessions', to: '/sessions', icon: 'i-lucide-list', about: 'Every session, the ones that need you first.', when: 'always', place: { tab: 2 } },
  { id: 'yard', label: 'Yard', title: 'Yard', to: '/yard', icon: 'i-lucide-layout-grid', chord: ['G', 'Y'], about: 'Every live terminal on one screen.', when: 'always', place: { nav: 2, tile: 2, tab: 3, palette: 2 } },
  {
    id: 'roundhouse',
    label: 'Roundhouse',
    title: 'Roundhouse',
    to: '/roundhouse',
    icon: 'i-lucide-gallery-horizontal',
    chord: ['G', 'R'],
    about: "The Yard's second view: one terminal at a time, on rotation.",
    when: 'always',
    place: { palette: 3 },
    viewOf: 'yard',
  },
  { id: 'crews', label: 'Crews', title: 'Crews', to: '/crews', icon: 'i-lucide-users', chord: ['G', 'C'], about: 'Saved teams of agents, and each run of one.', when: 'always', place: { nav: 3, tile: 3, tab: 4, palette: 4 } },
  {
    id: 'dispatch',
    label: 'Dispatch',
    title: 'Dispatch',
    to: '/dispatch',
    icon: 'i-lucide-ticket',
    chord: ['G', 'D'],
    about: 'Tickets, the plans you approve, the runs they start.',
    when: 'always',
    place: { nav: 4, tile: 4, more: 1, palette: 5 },
  },
  { id: 'brakeman', label: 'Brakeman', title: 'Brakeman', to: '/brakeman', icon: 'i-lucide-notebook-pen', chord: ['G', 'B'], about: 'A sidekick that reads all of it and briefs you.', when: 'brakeman', place: { nav: 5, tile: 5, more: 2 } },
  {
    id: 'agents',
    label: 'Agents',
    title: 'Agents',
    to: '/agents',
    icon: 'i-lucide-bot',
    chord: ['G', 'A'],
    about: 'The agents this machine can launch, and how each reports to Conductor.',
    when: 'always',
    place: { nav: 6, more: 3, palette: 6 },
  },
  {
    id: 'agents-reporting',
    label: 'Reporting',
    title: 'Agents · Reporting',
    to: '/agents#reporting',
    icon: 'i-lucide-plug',
    about: 'How each agent reports its turns and questions, and the hooks that make it.',
    when: 'always',
    place: { palette: 7 },
    viewOf: 'agents',
  },
  {
    id: 'history',
    label: 'History',
    title: 'History',
    to: '/history',
    icon: 'i-lucide-history',
    chord: ['G', 'H'],
    about: 'What was said, decided and checked, kept after the work ends.',
    when: 'always',
    place: { nav: 7, more: 4, palette: 8 },
  },
  {
    id: 'history-feed',
    label: 'Feed',
    title: 'History · Feed',
    to: '/history?view=feed',
    icon: 'i-lucide-radio-tower',
    chord: ['G', 'E'],
    about: 'What your agents report, as it happens.',
    when: 'always',
    place: { palette: 9 },
    viewOf: 'history',
  },
  { id: 'paste', label: 'Paste invite', title: 'Paste invite', to: '/paste', icon: 'i-lucide-clipboard-paste', about: 'Opens a share link or an invite someone sent you.', when: 'always', place: { palette: 10 } },
  {
    id: 'settings',
    label: 'Settings',
    title: 'Settings',
    to: '/settings',
    icon: 'i-lucide-settings',
    chord: ['G', 'S'],
    about: 'How the desktop app runs its server and shares.',
    when: 'desktop',
    place: { nav: 8, more: 5, palette: 11 },
  },
]

/** What decides which pages are offered: the desktop app, and a server that has Brakeman. */
export interface PageScope {
  desktop?: boolean
  brakeman?: boolean
  /** Whether Needs you's row shows in the nav (needsRowShown): it leaves when nothing waits and nothing was decided that day. */
  needs?: boolean
}

function offered(p: PageEntry, s: PageScope): boolean {
  return p.when === 'always' || (p.when === 'desktop' && !!s.desktop) || (p.when === 'brakeman' && !!s.brakeman)
}

function placed(where: keyof PageEntry['place'], s: PageScope): PageEntry[] {
  // A view of another page is reached from that page's own switch, never from the nav or the phone's bar.
  return PAGES.filter((p) => p.place[where] !== undefined && offered(p, s) && (where === 'palette' || !p.viewOf)).sort((a, b) => a.place[where]! - b.place[where]!)
}

/** The entry of a page by its id. */
export function pageById(id: PageId): PageEntry {
  return PAGES.find((p) => p.id === id)!
}

/** The rail: Needs you while its row shows, the Yard, Crews, Dispatch, Brakeman while the server has it, Agents, History, Settings in the desktop app. */
export function navPages(s: PageScope = {}): PageEntry[] {
  return placed('nav', s).filter((p) => p.id !== 'decisions' || s.needs !== false)
}

/** The phone's bar: its four tabs, in order (More is the bar's own). */
export function phoneTabs(): PageEntry[] {
  return placed('tab', {})
}

/**
 * A cell of the full sidebar's tiles: a page; Brakeman's slot, held empty while the server has not said whether it has Brakeman, so
 * that nothing moves when it fills; or More, the menu of the rail's other pages.
 */
export type NavTile = { kind: 'page'; page: PageEntry } | { kind: 'held' } | { kind: 'more' }

/** What decides the tiles: as PageScope, with `brakeman` undefined while the server has not said, and a switchyard's server. */
export interface TileScope extends PageScope {
  /** A switchyard serves no inbox: the Needs you tile is left out there (the nav's row rule hides it), its slot taken by the next. */
  switchyard?: boolean
}

/**
 * The full sidebar's foot: the tiles in order, then More, and the pages More holds (the rail's pages that have no tile, in the
 * rail's order). A page that is a view of another has no tile and is not under More (it is reached from its page's switch). Where
 * the server says it has no Brakeman, or on a switchyard, the tiles after the missing one close up and More moves into its slot.
 */
export function navTiles(s: TileScope = {}): { tiles: NavTile[]; more: PageEntry[] } {
  const tiles: NavTile[] = []
  for (const p of PAGES.filter((p) => p.place.tile !== undefined).sort((a, b) => a.place.tile! - b.place.tile!)) {
    if (p.id === 'decisions' && s.switchyard) continue
    if (p.when === 'brakeman' && s.brakeman === undefined) tiles.push({ kind: 'held' })
    else if (offered(p, s)) tiles.push({ kind: 'page', page: p })
  }
  tiles.push({ kind: 'more' })
  return { tiles, more: placed('nav', s).filter((p) => p.place.tile === undefined) }
}

/** The pages under the phone's More, in order: Dispatch, Brakeman while the server has it, Agents, History, Settings in the desktop app. */
export function phoneMore(s: Pick<PageScope, 'desktop' | 'brakeman'> = {}): PageEntry[] {
  return placed('more', s)
}

/** The palette's Go to rows, in order: the destinations and their views (the Roundhouse, Agents · Reporting, History · Feed), the paste invite. */
export function palettePages(s: Pick<PageScope, 'desktop'> = {}): PageEntry[] {
  return placed('palette', s)
}

/** Whether `path` is under the path `to` names: the path itself or a path below it, never one that only starts with its letters. */
function under(path: string, to: string): boolean {
  return path === to || path.startsWith(`${to}/`)
}

/** Whether `path` (a query or hash may follow) is one of the Yard's views: its grid at `/yard` or the Roundhouse at `/roundhouse`. */
export function yardRoute(path: string): boolean {
  const bare = path.replace(/[?#].*$/, '')
  return PAGES.some((p) => (p.id === 'yard' || p.viewOf === 'yard') && under(bare, p.to))
}

/**
 * Whether the page is the one open at `path`: a run's page keeps Crews lit (a run is the Crews page's), Dispatch's projects and tickets
 * keep Dispatch lit, a record keeps History lit, the home and a session's page keep Sessions lit, and the Yard is lit on both its views.
 */
export function pageActive(id: PageId, path: string): boolean {
  if (id === 'yard') return yardRoute(path)
  if (id === 'crews') return under(path, '/crews') || path.startsWith('/runs/')
  if (id === 'sessions') return path === '/' || under(path, '/sessions')
  return under(path, pageById(id).to)
}

/** The nav's tooltip: the label and the page's sentence as one line, "Yard: every live terminal on one screen". */
export function pageTip(p: Pick<PageEntry, 'label' | 'about'>): string {
  const about = p.about.replace(/\.$/, '')
  return `${p.label}: ${about.charAt(0).toLowerCase()}${about.slice(1)}`
}
