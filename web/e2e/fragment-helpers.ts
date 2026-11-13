import type { Page } from '@playwright/test'
import { expect } from './fixtures'

// What the browser holds of an address, for the specs of a credential in the URL fragment (decide-only.spec.ts,
// token-fragment.spec.ts): read off the page itself, and recorded through a function the test exposes, so the record outlives a
// reload.

/** Everything of the page that could hold an address: the address bar, the history's state, the Navigation API's entries, and the router's route and the one it was redirected from. */
export const addressHeld = (page: Page) =>
  page.evaluate(() => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const route = (document.querySelector('#__nuxt') as any).__vue_app__.config.globalProperties.$router.currentRoute.value
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const entries = ((window as any).navigation?.entries() ?? []).map((e: { url: string }) => e.url)
    return [location.href, JSON.stringify(history.state), ...entries, route.fullPath, route.redirectedFrom?.fullPath ?? ''].join(' ')
  })

/**
 * Records, in every document the page loads and before its own scripts run: each load (`load <address>`), each address and state
 * the page writes to the history (`pushState …`, `replaceState …`), each `popstate` listener the window is given, in order, with
 * its phase (`listener <n> capture|bubble`), and each run of one (`ran <n>`). `routes(page)` adds the router's navigations
 * (`route <fullPath> <redirectedFrom>`) in the document drawn now.
 */
export async function recorder(page: Page): Promise<{ lines: string[]; since: () => string; loads: () => number; routes: () => Promise<void> }> {
  const lines: string[] = []
  await page.exposeFunction('__conductorRecord', (line: string) => void lines.push(line))
  await page.addInitScript(() => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const rec = (line: string) => void (window as any).__conductorRecord(line)
    rec(`load ${location.href}`)
    for (const k of ['pushState', 'replaceState'] as const) {
      const write = history[k].bind(history)
      history[k] = (state: unknown, unused: string, url?: string | URL | null) => {
        rec(`${k} ${String(url ?? '')} ${JSON.stringify(state)}`)
        write(state, unused, url)
      }
    }
    const add = window.addEventListener.bind(window)
    let n = 0
    window.addEventListener = ((type: string, fn: EventListenerOrEventListenerObject, opts?: boolean | AddEventListenerOptions) => {
      if (type !== 'popstate' || typeof fn !== 'function') return add(type, fn, opts)
      const i = ++n
      rec(`listener ${i} ${opts === true || (typeof opts === 'object' && opts.capture) ? 'capture' : 'bubble'}`)
      add(type, (e: Event) => {
        rec(`ran ${i}`)
        fn.call(window, e)
      }, opts)
    }) as typeof window.addEventListener
  })
  return {
    lines,
    /** Everything recorded after the first load, whose address is the one the test opened. */
    since: () => lines.slice(1).join('\n'),
    loads: () => lines.filter((l) => l.startsWith('load ')).length,
    routes: () =>
      page.evaluate(() => {
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        const router = (document.querySelector('#__nuxt') as any).__vue_app__.config.globalProperties.$router
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        router.beforeEach((to: any) => void (window as any).__conductorRecord(`route ${to.fullPath} ${to.redirectedFrom?.fullPath ?? ''}`))
      }),
  }
}

/**
 * Sets the address's fragment in a page that has drawn (`location.hash`, a navigation the browser starts, as a link to the same
 * address opened in the same tab is), then waits for what follows: the page loaded again (`reload`), or the router's navigation to
 * it. Returns the `popstate` listeners of the document it was set in (the indices of those given in the capture phase, then of the
 * others), and the lines recorded from the change on.
 */
export async function fragmentChanged(page: Page, rec: Awaited<ReturnType<typeof recorder>>, fragment: string, reload: boolean) {
  const doc = rec.lines.slice(rec.lines.map((l) => l.startsWith('load ')).lastIndexOf(true))
  const of = (phase: string) => doc.filter((l) => l.startsWith('listener ') && l.endsWith(phase)).map((l) => l.split(' ')[1]!)
  const from = rec.lines.length
  const loads = rec.loads()
  await rec.routes()
  await page.evaluate((f) => {
    location.hash = f
  }, fragment)
  if (reload) await expect.poll(rec.loads).toBe(loads + 1)
  else await expect.poll(() => rec.lines.slice(from).some((l) => l.startsWith('route '))).toBe(true)
  return { capture: of('capture'), bubble: of('bubble'), after: rec.lines.slice(from) }
}
