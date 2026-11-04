import type { DesktopBridge } from '~/utils/desktop'
import type { AgentBrowser, OpenEvent } from '~/utils/protocol'

/** The browsers an agent's links may open in (internal/catalog/browser.go), in the order the Agents page offers them, by their names. */
export const BROWSERS = [
  { name: 'chrome', label: 'Chrome' },
  { name: 'edge', label: 'Edge' },
  { name: 'firefox', label: 'Firefox' },
  { name: 'brave', label: 'Brave' },
  { name: 'safari', label: 'Safari' },
] as const
export type BrowserName = (typeof BROWSERS)[number]['name']

/** The browsers that take a profile: the Chromium ones. */
export const PROFILED: readonly string[] = ['chrome', 'edge', 'brave']

/** The server's bound on a profile (catalog.MaxBrowserProfile), in characters, and on a link opened (api.MaxOpenURL), in bytes. */
export const MAX_PROFILE = 100
export const MAX_OPEN_URL = 2048

/** browserLabel names a browser as people do: Edge, Chrome. */
export function browserLabel(name: string | undefined): string {
  return BROWSERS.find((b) => b.name === name)?.label ?? 'the default browser'
}

/** browserWords names a browser and its profile: "Edge (Profile 2)". */
export function browserWords(b: AgentBrowser | undefined): string {
  if (!b?.name) return 'the default browser'
  return b.profile ? `${browserLabel(b.name)} (${b.profile})` : browserLabel(b.name)
}

/** asOpenEvent reads an `open` event's data; null for anything that is not one, which the page ignores. */
export function asOpenEvent(v: unknown): OpenEvent | null {
  if (!v || typeof v !== 'object') return null
  const { id, sessionId, url, browser, state } = v as Record<string, unknown>
  if (typeof id !== 'string' || !id || typeof sessionId !== 'string' || typeof url !== 'string' || (state !== 'pending' && state !== 'offered')) return null
  if (new TextEncoder().encode(url).length > MAX_OPEN_URL || !/^https?:\/\//i.test(url)) return null
  try {
    const u = new URL(url)
    if (u.protocol !== 'http:' && u.protocol !== 'https:') return null
  } catch {
    return null
  }
  let b: AgentBrowser | undefined
  if (browser && typeof browser === 'object') {
    const { name, profile } = browser as Record<string, unknown>
    if (typeof name === 'string' && BROWSERS.some((x) => x.name === name)) b = { name: name as BrowserName, ...(typeof profile === 'string' && profile ? { profile } : {}) }
  }
  return { id, sessionId, url, state, ...(b ? { browser: b } : {}) }
}

/**
 * Which way a link an agent opened goes, on this page: a page in the desktop app that can open one (the bridge's openUrl) claims a
 * pending link and opens it; a pending link is nothing to a page in a plain browser tab, which never starts anything on its own; an
 * offered link (no desktop app took it, or a managed run's attempt opened it) is offered in a toast by every page.
 */
export function openAction(ev: OpenEvent, canOpen: boolean): 'claim' | 'offer' | 'none' {
  if (ev.state === 'offered') return 'offer'
  return canOpen ? 'claim' : 'none'
}

/** What the page says of a link the app opened: only that the browser named is not on this computer, or did not start; null otherwise. */
export function outcomeWords(outcome: string, b: AgentBrowser | undefined): { title: string; description: string } | null {
  if (!b?.name) return null
  if (outcome === 'missing') return { title: `${browserLabel(b.name)} is not installed on this computer`, description: 'The link opened in the default browser.' }
  if (outcome === 'failed') return { title: `${browserLabel(b.name)} did not start`, description: 'The link opened in the default browser.' }
  return null
}

/** The toast's words for a link offered: who opened it, and where Open takes it. */
export function offerWords(sessionName: string, ev: OpenEvent, inDesktop: boolean): { title: string; description: string } {
  const where = inDesktop && ev.browser?.name ? ` Open shows it in ${browserWords(ev.browser)}.` : ''
  return { title: `${sessionName} opened a link`, description: `${ev.url}${where}` }
}

/** What routeOpen takes of the page: the bridge (null outside the app), the server's open routes, and the toasts. */
export interface OpenRoute {
  bridge: Pick<DesktopBridge, 'openUrl'> | null
  claim(id: string): Promise<unknown>
  result(id: string, outcome: string): Promise<unknown>
  /** A toast with the link and Open. */
  offer(ev: OpenEvent): void
  /** A toast that says what became of the link (outcomeWords). */
  warn(words: { title: string; description: string }): void
}

/**
 * routeOpen does with an open event what openAction says: in the desktop app it claims a pending link and, once it has it, opens it
 * through the bridge, once, and tells the server what became of it; a claim refused (another window of the app took it, or it was
 * offered) opens nothing, and a bridge that refused the link has it offered. An offered link is offered; a plain tab does nothing
 * with a pending one, and so does a page of an app older than the bridge's openUrl.
 */
export async function routeOpen(ev: OpenEvent, r: OpenRoute): Promise<void> {
  const open = r.bridge?.openUrl
  const action = openAction(ev, !!open)
  if (action === 'offer') return r.offer(ev)
  if (action !== 'claim' || !open) return
  try {
    await r.claim(ev.id)
  } catch {
    return
  }
  let outcome: string
  try {
    outcome = (await open(ev.url, ev.browser ?? null)).outcome
  } catch {
    return r.offer(ev)
  }
  await r.result(ev.id, outcome).catch(() => {})
  const w = outcomeWords(outcome, ev.browser)
  if (w) r.warn(w)
}
