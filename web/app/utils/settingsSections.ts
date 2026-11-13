/**
 * Settings' sections (#433): which show on this machine and in what order, the id each keeps in `/settings?section=<id>`, and which
 * is in view as the panel's body scrolls. The ids are part of the page's address once shipped: keep them, and give a new section
 * (#346, #347) an entry here and nowhere else in this list's logic.
 */
export const SETTINGS_SECTIONS = [
  { id: 'webrtc', title: 'WebRTC from WSL', icon: 'i-lucide-network' },
  { id: 'switchyard', title: 'Switchyard', icon: 'i-lucide-globe' },
  { id: 'folders', title: 'Where agents work', icon: 'i-lucide-folder-tree' },
  { id: 'running', title: 'How agents run', icon: 'i-lucide-square-terminal' },
  { id: 'agents', title: 'Agents on this machine', icon: 'i-lucide-bot' },
  { id: 'accounts', title: 'Accounts', icon: 'i-lucide-key-round' },
  { id: 'brakeman', title: 'Brakeman', icon: 'i-lucide-notebook-pen' },
  { id: 'app', title: 'The app', icon: 'i-lucide-info' },
] as const

/**
 * A setting as one row once the sections' column has room (48rem, a container query: the sidebar resizes and collapses), its label
 * and description on the left and its control on the right, which never stretches past 36rem; stacked below that. A UFormField's
 * `ui`, for the page's fields and the cards it holds (Brakeman's).
 */
export const SETTINGS_FIELD_UI = { root: '@3xl:grid @3xl:grid-cols-[minmax(0,1fr)_minmax(0,32rem)] @3xl:items-start @3xl:gap-x-8', container: 'max-w-xl @3xl:mt-0' }

export type SettingsSection = (typeof SETTINGS_SECTIONS)[number]
export type SettingsSectionId = SettingsSection['id']

/** What decides the sections shown: the desktop app's page or the browser's, and what the bridge, the catalog and Brakeman said. */
export interface SettingsShown {
  /** The desktop app's page; false for a browser's, which shows Accounts, Brakeman and The app alone. */
  desktop: boolean
  /** The bridge's platform ('win32' on Windows). */
  platform?: string
  /** Whether the bridge answered with what it forwards for WebRTC from WSL (asked on Windows only). */
  ice: boolean
  /** How many agents the catalog lists. */
  agents: number
  /** Whether the server offers Brakeman (useBrakeman's available is not false). */
  brakeman: boolean
  /** Whether the server offers accounts (useAccounts' available is not false); absent counts as offered. */
  accounts?: boolean
}

/** The sections shown, in page order. */
export function shownSections(s: SettingsShown): SettingsSection[] {
  const on: Record<SettingsSectionId, boolean> = {
    webrtc: s.desktop && s.platform === 'win32' && s.ice,
    switchyard: s.desktop,
    folders: s.desktop,
    running: s.desktop,
    agents: s.desktop && s.agents > 0,
    accounts: s.accounts !== false,
    brakeman: s.brakeman,
    app: true,
  }
  return SETTINGS_SECTIONS.filter((x) => on[x.id])
}

/**
 * The section `?section=` names: a known id, or null for none or an unknown one. Given twice, the first counts, as Vue Router reads
 * a repeated key as a list.
 */
export function sectionFromQuery(value: unknown): SettingsSectionId | null {
  const first = Array.isArray(value) ? value[0] : value
  if (typeof first !== 'string') return null
  return SETTINGS_SECTIONS.find((x) => x.id === first)?.id ?? null
}

/** Where a section's card sits in the scrolled body, in the body's own coordinates (offsets from the top of what scrolls). */
export interface SectionBox {
  id: SettingsSectionId
  top: number
  bottom: number
}

/** The body's scroll: how far it is scrolled, how tall its visible part is, and how tall all it holds. */
export interface ScrollView {
  scrollTop: number
  clientHeight: number
  scrollHeight: number
  /** What covers the top of the visible part (the phone's sticky row of links), so a section under it is not yet in view. */
  cover?: number
}

/**
 * The section in view: the last whose top has reached the top of the visible part (a quarter of the way down, so a section scrolled
 * to is marked though the one above still shows its foot), the last section once the body is scrolled to its end, and the first
 * before any is reached. A section just chosen (`chosen`) stays marked while any of it is visible, since a short one near the end
 * cannot be scrolled to the top; the page stops passing it at the person's own scroll, key or click.
 */
export function sectionInView(boxes: readonly SectionBox[], view: ScrollView, chosen?: SettingsSectionId | null): SettingsSectionId | null {
  if (!boxes.length) return null
  const top = view.scrollTop + (view.cover ?? 0)
  const bottom = view.scrollTop + view.clientHeight
  if (chosen) {
    const c = boxes.find((b) => b.id === chosen)
    if (c && c.bottom > top && c.top < bottom) return chosen
  }
  if (view.scrollTop + view.clientHeight >= view.scrollHeight - 1 && view.scrollTop > 0) return boxes[boxes.length - 1]!.id
  const line = top + (bottom - top) / 4
  let current = boxes[0]!.id
  for (const b of boxes) if (b.top <= line) current = b.id
  return current
}
