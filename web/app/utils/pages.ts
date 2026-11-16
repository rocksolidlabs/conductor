/**
 * The app's pages in one table (the UX review's X-pages): a page's name, its one sentence, its key and where it stands. The nav and its
 * rail, the phone's bar and its More, and the palette's Go to read it through the functions below; none keeps a list of its own.
 */

export type PageId = 'decisions' | 'yard' | 'roundhouse' | 'agents' | 'crews' | 'dispatch' | 'events' | 'history' | 'brakeman' | 'sessions' | 'settings'

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
  /** Its place on each surface, in order; a surface it has no place on leaves it out. */
  place: { nav?: number; tab?: number; more?: number; palette?: number }
}

export const PAGES: readonly PageEntry[] = [
  {
    id: 'decisions',
    label: 'Decide',
    title: 'Needs your decision',
    to: '/decisions',
    icon: 'i-lucide-gavel',
    chord: ['G', 'N'],
    about: 'Everything that waits for your answer, in one list.',
    when: 'always',
    place: { nav: 1, tab: 1, palette: 1 },
  },
  { id: 'sessions', label: 'Sessions', title: 'Sessions', to: '/sessions', icon: 'i-lucide-list', about: 'Every session, the ones that need you first.', when: 'always', place: { tab: 2 } },
  { id: 'yard', label: 'Yard', title: 'Yard', to: '/yard', icon: 'i-lucide-layout-grid', chord: ['G', 'Y'], about: 'Every live terminal on one screen.', when: 'always', place: { nav: 2, tab: 3, palette: 2 } },
  {
    id: 'roundhouse',
    label: 'Roundhouse',
    title: 'Roundhouse',
    to: '/roundhouse',
    icon: 'i-lucide-gallery-horizontal',
    chord: ['G', 'R'],
    about: 'One terminal at a time, on rotation.',
    when: 'always',
    place: { nav: 3, more: 1, palette: 3 },
  },
  {
    id: 'agents',
    label: 'Agents',
    title: 'Agents',
    to: '/agents',
    icon: 'i-lucide-bot',
    chord: ['G', 'A'],
    about: 'The agents this machine can launch, and how each reports to Conductor.',
    when: 'always',
    place: { nav: 4, more: 2, palette: 6 },
  },
  { id: 'crews', label: 'Crews', title: 'Crews', to: '/crews', icon: 'i-lucide-users', chord: ['G', 'C'], about: 'Saved teams of agents, and each run of one.', when: 'always', place: { nav: 5, tab: 4, palette: 4 } },
  {
    id: 'dispatch',
    label: 'Dispatch',
    title: 'Dispatch',
    to: '/dispatch',
    icon: 'i-lucide-ticket',
    chord: ['G', 'D'],
    about: 'Tickets, the plans you approve, the runs they start.',
    when: 'always',
    place: { nav: 6, more: 3, palette: 5 },
  },
  { id: 'events', label: 'Events', title: 'Events', to: '/events', icon: 'i-lucide-radio-tower', chord: ['G', 'E'], about: 'What your agents report, as it happens.', when: 'always', place: { nav: 7, more: 4, palette: 7 } },
  {
    id: 'history',
    label: 'History',
    title: 'History',
    to: '/history',
    icon: 'i-lucide-history',
    chord: ['G', 'H'],
    about: 'What was said, decided and checked, kept after the work ends.',
    when: 'always',
    place: { nav: 8, more: 5, palette: 8 },
  },
  { id: 'brakeman', label: 'Brakeman', title: 'Brakeman', to: '/brakeman', icon: 'i-lucide-notebook-pen', about: 'A sidekick that reads all of it and briefs you.', when: 'brakeman', place: { nav: 9 } },
  {
    id: 'settings',
    label: 'Settings',
    title: 'Settings',
    to: '/settings',
    icon: 'i-lucide-settings',
    chord: ['G', 'S'],
    about: 'How the desktop app runs its server and shares.',
    when: 'desktop',
    place: { nav: 10, more: 6, palette: 9 },
  },
]

/** What decides which pages are offered: the desktop app, and a server that has Brakeman. */
export interface PageScope {
  desktop?: boolean
  brakeman?: boolean
}

function offered(p: PageEntry, s: PageScope): boolean {
  return p.when === 'always' || (p.when === 'desktop' && !!s.desktop) || (p.when === 'brakeman' && !!s.brakeman)
}

function placed(where: keyof PageEntry['place'], s: PageScope): PageEntry[] {
  return PAGES.filter((p) => p.place[where] !== undefined && offered(p, s)).sort((a, b) => a.place[where]! - b.place[where]!)
}

/** The entry of a page by its id. */
export function pageById(id: PageId): PageEntry {
  return PAGES.find((p) => p.id === id)!
}

/** The sidebar's foot (and the rail): Decide, the Yard, …, History, then Brakeman while the server has it and Settings in the desktop app. */
export function navPages(s: PageScope = {}): PageEntry[] {
  return placed('nav', s)
}

/** The phone's bar: its four tabs, in order (More is the bar's own). */
export function phoneTabs(): PageEntry[] {
  return placed('tab', {})
}

/** The pages under the phone's More, in order. */
export function phoneMore(s: Pick<PageScope, 'desktop'> = {}): PageEntry[] {
  return placed('more', s)
}

/** The palette's Go to rows, in order: the destinations alone. */
export function palettePages(s: Pick<PageScope, 'desktop'> = {}): PageEntry[] {
  return placed('palette', s)
}

/** Whether `path` is under the path `to` names: the path itself or a path below it, never one that only starts with its letters. */
function under(path: string, to: string): boolean {
  return path === to || path.startsWith(`${to}/`)
}

/**
 * Whether the page is the one open at `path`: a run's page keeps Crews lit (a run is the Crews page's), Dispatch's projects and tickets
 * keep Dispatch lit, a record keeps History lit, and the home and a session's page keep Sessions lit.
 */
export function pageActive(id: PageId, path: string): boolean {
  if (id === 'crews') return under(path, '/crews') || path.startsWith('/runs/')
  if (id === 'sessions') return path === '/' || under(path, '/sessions')
  return under(path, pageById(id).to)
}

/** The nav's tooltip: the label and the page's sentence as one line, "Yard: every live terminal on one screen". */
export function pageTip(p: Pick<PageEntry, 'label' | 'about'>): string {
  const about = p.about.replace(/\.$/, '')
  return `${p.label}: ${about.charAt(0).toLowerCase()}${about.slice(1)}`
}
