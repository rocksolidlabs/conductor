import type { Page, Request } from '@playwright/test'
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
 * The workbench token's store in the page drawn now (useWorkbenchToken: a property of the Nuxt app, outside the payload): the token
 * the page sends ('' none) and what the store knows of it.
 */
export const workbenchHeld = (page: Page) =>
  page.evaluate(() => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const h = (document.querySelector('#__nuxt') as any).__vue_app__.config.globalProperties.$nuxt._conductorWorkbenchToken
    return { token: h.token.value as string, standing: h.standing.value as string }
  })

/**
 * Records, in every document the page loads and before its own scripts run: each load (`load <address>`), each address and state
 * the page writes to the history (`pushState …`, `replaceState …`), each `popstate` listener the window is given, in order, with
 * its phase (`listener <n> capture|bubble`), and each run of one (`ran <n>`). `routes(page)` adds the router's navigations
 * (`route <fullPath> <redirectedFrom>`) in the document drawn now. The document also keeps, in itself, the type of each navigation
 * the Navigation API's `navigate` event announced in it (`navigationTypes`): a `location.reload()` is one, announced as it is called,
 * and recorded then with the address it is called at (`reload <address>`). Each selector of `watch` is recorded the first time a
 * document draws an element it names (`drawn <selector>`).
 */
export async function recorder(page: Page, watch: string[] = []): Promise<{ lines: string[]; since: () => string; loads: () => number; routes: () => Promise<void> }> {
  const lines: string[] = []
  await page.exposeFunction('__conductorRecord', (line: string) => void lines.push(line))
  await page.addInitScript((watch: string[]) => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const w = window as any
    const rec = (line: string) => void w.__conductorRecord(line)
    rec(`load ${location.href}`)
    const types: string[] = (w.__conductorNavigationTypes = [])
    w.navigation?.addEventListener('navigate', (e: { navigationType: string }) => {
      types.push(e.navigationType)
      if (e.navigationType === 'reload') rec(`reload ${location.href}`)
    })
    const unseen = new Set(watch)
    if (unseen.size)
      new MutationObserver(() => {
        for (const s of unseen)
          if (document.querySelector(s)) {
            unseen.delete(s)
            rec(`drawn ${s}`)
          }
      }).observe(document, { childList: true, subtree: true })
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
  }, watch)
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

/** The types of the navigations announced in the document drawn now (`recorder`). */
export const navigationTypes = (page: Page) => page.evaluate(() => (window as unknown as { __conductorNavigationTypes: string[] }).__conductorNavigationTypes)

/** Sets the address's fragment (`location.hash`, a navigation the browser starts, as a link to the same address opened in the same tab is). */
export const setFragment = (page: Page, fragment: string) =>
  page.evaluate((f) => {
    location.hash = f
  }, fragment)

/**
 * Sets the address's fragment in a page that has drawn, then waits for what follows: the page loaded again (`reload`), the router's
 * navigation to it (`route`), or the address rewritten without it in the same document, no load announced in it (`kept`: the
 * listener that rewrote it decides on a load in the same turn, a `location.reload()` announces itself as it is called, and a load
 * that went through has drawn a document without the mark set before the change). Returns the `popstate` listeners of the document
 * it was set in (the indices of those given in the capture phase, then of the others), and the lines recorded from the change on.
 */
export async function fragmentChanged(page: Page, rec: Awaited<ReturnType<typeof recorder>>, fragment: string, then: 'reload' | 'route' | 'kept') {
  const doc = rec.lines.slice(rec.lines.map((l) => l.startsWith('load ')).lastIndexOf(true))
  const of = (phase: string) => doc.filter((l) => l.startsWith('listener ') && l.endsWith(phase)).map((l) => l.split(' ')[1]!)
  const from = rec.lines.length
  const loads = rec.loads()
  await rec.routes()
  // The document marked: one drawn by a load since has no mark.
  await page.evaluate(() => void ((window as unknown as { __conductorMarked: boolean }).__conductorMarked = true))
  await setFragment(page, fragment)
  if (then === 'reload') await expect.poll(rec.loads).toBe(loads + 1)
  else if (then === 'route') await expect.poll(() => rec.lines.slice(from).some((l) => l.startsWith('route '))).toBe(true)
  else {
    await expect.poll(() => page.evaluate(() => location.hash)).toBe('')
    expect(await page.evaluate(() => (window as unknown as { __conductorMarked?: boolean }).__conductorMarked), `${fragment}: the same document`).toBe(true)
    expect(await navigationTypes(page), fragment).not.toContain('reload')
    expect(rec.loads(), fragment).toBe(loads)
  }
  return { capture: of('capture'), bubble: of('bubble'), after: rec.lines.slice(from) }
}

/**
 * The page's document requests held while `on`: each waits for `release()`. `asked` resolves, with the request, at the first one held,
 * the moment a load of the page has started and nothing of it has been drawn: the page that asked for it is still the one drawn, and
 * goes on running. A page whose load is under way answers no devtools message until the load commits (Chromium: Playwright's
 * `evaluate` and locators, and a CDP `Runtime.evaluate` alike, wait for it), so what that page does meanwhile is read off what it
 * sends and what it reports through `recorder`'s function; input reaches it as a person's does (`page.mouse`).
 */
export async function holdDocuments(page: Page) {
  let release = () => {}
  let asked: (r: Request) => void = () => {}
  const h = {
    on: false,
    released: new Promise<void>((r) => (release = r)),
    asked: new Promise<Request>((r) => (asked = r)),
    release: () => release(),
  }
  await page.route(
    (u) => !u.pathname.startsWith('/api/'),
    async (route) => {
      if (!h.on || route.request().resourceType() !== 'document') return route.fallback()
      asked(route.request())
      await h.released
      return route.continue()
    },
  )
  return h
}

/** The requests to the API in `sent` from index `from` on, each with its Authorization header. */
export const apiSent = (sent: Request[], from: number) =>
  Promise.all(
    sent
      .slice(from)
      .filter((r) => new URL(r.url()).pathname.startsWith('/api/'))
      .map(async (r) => ({ path: new URL(r.url()).pathname, url: r.url(), auth: (await r.headerValue('authorization')) ?? '' })),
  )
