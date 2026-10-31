import type { DropdownMenuItem } from '@nuxt/ui'

/**
 * The one action system (#302, principle 2): what a row of the sidebar, a
 * row of the command palette, the account menu and a page offer is one
 * descriptor each, rendered by whoever shows it. The sidebar's menus are
 * `toMenuItems` over these; the palette renders the same descriptors as
 * row actions, and runs them with their keys.
 */
export type ActionKind = 'session' | 'run' | 'shared' | 'account' | 'page' | 'global'

export interface Action {
  /** Stable within its kind: `open`, `share`, `stop`… The palette keys frecency by kind, id and target. */
  id: string
  label: string
  icon?: string
  /** The keys that run it where its list has the focus, as UKbd values. */
  kbds?: string[]
  kind: ActionKind
  /** What it is about: a session, run or shared link id. */
  target?: string
  /** A destructive action asks this in the row before it runs. */
  ask?: { title: string; detail: string }
  color?: 'error'
  disabled?: boolean
  run: () => void
}

/** The actions as Nuxt UI menu items, grouped as given. */
export function toMenuItems(groups: readonly (readonly Action[])[]): DropdownMenuItem[][] {
  return groups.map((g) =>
    g.map((a) => {
      const item: DropdownMenuItem = { label: a.label, onSelect: a.run }
      if (a.icon) item.icon = a.icon
      if (a.kbds) item.kbds = a.kbds
      if (a.disabled !== undefined) item.disabled = a.disabled
      if (a.color) item.color = a.color
      return item
    }),
  )
}

/**
 * What a row of the sidebar offers without opening it (design 3c): the same
 * list behind More, the right-click menu, a long press on a phone, and the
 * keys. A session: Open, Open the run (a member), Share…, Show in the Yard,
 * Rename… (a server session, #318), Stop…; a run's header: Open run, Share
 * run, Stop run…; a link shared with you: Open, Forget.
 */
export interface RowActionTarget {
  kind: 'session' | 'run' | 'shared'
  name: string
  /** Running or starting: it can be stopped, and shared. */
  live: boolean
  /** A session that is a run's member: its run can be opened. */
  inRun?: boolean
  /** A server session, running or ended: it can be renamed here (a hosted one's host names it). */
  renamable?: boolean
  /** The session's, run's or entry's id, named on the descriptors. */
  id?: string
}

export interface RowActionHandlers {
  open: () => void
  openRun?: () => void
  share?: () => void
  yard?: () => void
  rename?: () => void
  stop?: () => void
  forget?: () => void
}

const none = () => {}

export function rowActions(t: RowActionTarget, on: RowActionHandlers): Action[][] {
  const kind = t.kind
  const target = t.id
  const a = (id: string, label: string, run: (() => void) | undefined, rest: Partial<Action> = {}): Action => ({ id, label, kind, target, run: run ?? none, ...rest })
  if (kind === 'shared') {
    return [[a('open', 'Open', on.open, { icon: 'i-lucide-external-link', kbds: ['enter'] })], [a('forget', 'Forget', on.forget, { icon: 'i-lucide-x' })]]
  }
  if (kind === 'run') {
    return [
      [a('open', 'Open run', on.open, { icon: 'i-lucide-play', kbds: ['enter'] })],
      [a('share', 'Share run', on.share, { icon: 'i-lucide-share-2', kbds: ['S'], disabled: !t.live })],
      [a('stop', 'Stop run…', on.stop, { icon: 'i-lucide-square', kbds: ['X'], color: 'error', disabled: !t.live, ask: stopQuestion(t) })],
    ]
  }
  const first = [a('open', 'Open', on.open, { icon: 'i-lucide-terminal', kbds: ['enter'] })]
  if (t.inRun) first.push(a('open-run', 'Open the run', on.openRun, { icon: 'i-lucide-play', kbds: ['R'] }))
  return [
    first,
    [a('share', 'Share…', on.share, { icon: 'i-lucide-share-2', kbds: ['S'], disabled: !t.live }), a('yard', 'Show in the Yard', on.yard, { icon: 'i-lucide-layout-grid' })],
    ...(t.renamable ? [[a('rename', 'Rename…', on.rename, { icon: 'i-lucide-pencil' })]] : []),
    [a('stop', 'Stop…', on.stop, { icon: 'i-lucide-square', kbds: ['X'], color: 'error', disabled: !t.live, ask: stopQuestion(t) })],
  ]
}

/** The question a row asks before it stops something, in the row itself. */
export function stopQuestion(t: Pick<RowActionTarget, 'kind' | 'name'>): { title: string; detail: string } {
  if (t.kind === 'run') return { title: `Stop ${t.name}?`, detail: "Every member's terminal closes. The run can be resumed as a new run." }
  return { title: `Stop ${t.name}?`, detail: 'Its terminal closes. Resume brings the conversation back from Exited.' }
}

/** What the account menu beside the sidebar's name offers (design 3b): your name, the shortcuts, the theme, the workbench token. */
export interface AccountHandlers {
  name: () => void
  shortcuts: () => void
  theme: () => void
  token: () => void
  forget: () => void
}

export function accountActions(hasToken: boolean, on: AccountHandlers): Action[][] {
  const a = (id: string, label: string, run: () => void, rest: Partial<Action> = {}): Action => ({ id, label, kind: 'account', run, ...rest })
  const token = [a('token', hasToken ? 'Workbench token…' : 'Set workbench token…', on.token, { icon: hasToken ? 'i-lucide-key-round' : 'i-lucide-lock' })]
  if (hasToken) token.push(a('forget-token', 'Forget token', on.forget, { icon: 'i-lucide-log-out', color: 'error' }))
  return [[a('name', 'Your name…', on.name, { icon: 'i-lucide-user-round' })], [a('shortcuts', 'Keyboard shortcuts', on.shortcuts, { icon: 'i-lucide-keyboard', kbds: ['?'] }), a('theme', 'Toggle theme', on.theme, { icon: 'i-lucide-sun-moon' })], token]
}
