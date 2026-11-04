import { existsSync, mkdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Page } from '@playwright/test'
import { expect, test, type Session } from './fixtures'

// The desktop app's zoom, shown in the workbench (#431). The app takes the zoom keys before the page sees them and tells the page
// each move (the bridge's onZoom), and once per start a level kept from before (zoom()'s announce). The app is a stub bridge here,
// as in update.spec.ts: a move is the stub's event fired, the kept level its getter's answer, handed out once per start as the app's
// ZoomStart does. What the page drew is read in the page, through a MutationObserver and in the same task as the move, so a loaded
// machine cannot let the indicator's time run out between the move and the look.
test.describe.configure({ mode: 'serial' })

type Zoom = { level: number; percent: number; bound: '' | 'largest' | 'smallest' }
/** What the page keeps for the test: the stub's event, its asks, when the last move was told, and the last indicator's leave. */
type Stubbed = { e2eZoom?: (z: Zoom) => void; e2eZoomAsks?: number; e2eZoomLastAt?: number; e2eZoomLeave?: { at: number; transition: string } }

/** The app, stubbed: the zoom now is kept, and said to the first page that asks (once per "start", the session's storage) when not 100%. */
async function stubApp(page: Page, token: string, url: string, kept: Zoom, opts: { event?: boolean } = {}) {
  await page.addInitScript(
    ({ token, url, kept, event }) => {
      const w = window as unknown as { conductorDesktop: unknown } & Stubbed
      const bridge: Record<string, unknown> = {
        version: 'e2e',
        platform: 'linux',
        token: async () => token,
        openExternal: async () => {},
        settings: { get: async () => ({}), set: async (p: object) => p },
        restartServer: async () => {},
        openInBrowser: async () => {},
        showLog: async () => {},
        serverState: async () => ({ state: 'running', url, failures: 0 }),
        notice: async () => '',
        ice: async () => ({ forwarding: false, port: 0, publicIp: '', wslAddress: '', firewall: 'unknown' }),
        allowIceFirewall: async () => 'unknown',
        versions: async () => ({ app: 'e2e', electron: '', node: '', chrome: '', server: '' }),
        onServerState: () => () => {},
        zoom: async () => {
          const announce = !sessionStorage.getItem('e2e.zoomSaid') && kept.level !== 0
          sessionStorage.setItem('e2e.zoomSaid', '1')
          w.e2eZoomAsks = (w.e2eZoomAsks ?? 0) + 1
          return { ...kept, announce }
        },
      }
      // An older app's bridge has no zoom event.
      if (event)
        bridge.onZoom = (cb: (z: Zoom) => void) => {
          w.e2eZoom = cb
          return () => {}
        }
      w.conductorDesktop = bridge
    },
    { token, url, kept, event: opts.event ?? true },
  )
}

const AT_100: Zoom = { level: 0, percent: 100, bound: '' }

/** The page has its listener: the stub's event is held by the indicator. */
async function listening(page: Page) {
  await expect(page.locator('[data-zoom-status]')).toHaveCount(1)
  await expect.poll(() => page.evaluate(() => typeof (window as unknown as Stubbed).e2eZoom)).toBe('function')
}

/**
 * The moves, told in the page one by one; after each the page's drawing is awaited (a MutationObserver: a microtask, so no timer of
 * the page's can run between a move and its look). For each: how many indicators there are, the one's text, whether it is the same
 * element as the first move's, whether the focus is where it was before the moves, and whether a point at its middle hits it.
 * The time of the last move is taken before it is told (the indicator's timer starts inside it), and in the same evaluation the
 * indicator's leave is watched from then on: when its leave begins (the Transition's leave-active class, before the 300 ms fade,
 * which would otherwise hide a timer that short), and the transition its style then runs (`leave`).
 */
function move(page: Page, moves: Zoom[]) {
  return page.evaluate(async (moves) => {
    const w = window as unknown as Stubbed
    const before = document.activeElement
    const all = () => Array.from(document.querySelectorAll<HTMLElement>('[data-zoom-indicator]'))
    const drawn = (want: string) =>
      new Promise<void>((resolve) => {
        const done = () => all().length === 1 && all()[0]!.textContent?.trim() === want
        if (done()) return resolve()
        const o = new MutationObserver(() => {
          if (!done()) return
          o.disconnect()
          resolve()
        })
        o.observe(document.body, { subtree: true, childList: true, characterData: true })
      })
    let first: HTMLElement | undefined
    const seen: Array<{ count: number; text: string; same: boolean; focusKept: boolean; hit: boolean }> = []
    for (const z of moves) {
      w.e2eZoomLastAt = performance.now()
      w.e2eZoom!(z)
      await drawn(`${z.percent}%${z.bound ? `, the ${z.bound}` : ''}`)
      const el = all()[0]!
      first ??= el
      const r = el.getBoundingClientRect()
      const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2)
      seen.push({ count: all().length, text: el.textContent!.trim(), same: el === first, focusKept: document.activeElement === before, hit: !!at?.closest('[data-zoom-status]') })
    }
    const el = all()[0]!
    w.e2eZoomLeave = undefined
    const leaving = () => !el.isConnected || el.classList.contains('transition-opacity')
    const left = () => (w.e2eZoomLeave = { at: performance.now(), transition: getComputedStyle(el).transitionProperty })
    if (leaving()) left()
    else {
      const o = new MutationObserver(() => {
        if (!leaving()) return
        o.disconnect()
        left()
      })
      o.observe(document.body, { subtree: true, childList: true, attributes: true, attributeFilter: ['class'] })
    }
    return seen
  }, moves)
}

/** The last move's indicator, gone: how long after the move its leave began, and the transition it ran. */
async function leave(page: Page) {
  await expect(page.locator('[data-zoom-indicator]')).toHaveCount(0, { timeout: 15_000 })
  const l = await page.evaluate(() => {
    const w = window as unknown as Stubbed
    return w.e2eZoomLeave && { after: w.e2eZoomLeave.at - w.e2eZoomLastAt!, transition: w.e2eZoomLeave.transition }
  })
  expect(l).toBeTruthy()
  return l!
}

test('moves show their level in one indicator, which goes after its time, on every workbench page', async ({ page, state }) => {
  await stubApp(page, state.token, state.baseURL, AT_100)
  await page.goto('/yard')
  await expect(page.locator('[data-sidebar-menus]').first()).toBeVisible()
  await listening(page)
  const status = page.locator('[data-zoom-status]')
  await expect(status).toHaveAttribute('role', 'status')
  await expect(status).toHaveAttribute('aria-live', 'polite')
  // Nothing said at 100% on opening.
  await expect.poll(() => page.evaluate(() => (window as unknown as Stubbed).e2eZoomAsks)).toBe(1)
  await expect(page.locator('[data-zoom-indicator]')).toHaveCount(0)

  // Three presses of Ctrl+= from 100%: one indicator, updated in place, reading 131% at the end.
  const seen = await move(page, [
    { level: 0.5, percent: 110, bound: '' },
    { level: 1, percent: 120, bound: '' },
    { level: 1.5, percent: 131, bound: '' },
  ])
  expect(seen.map((s) => s.text)).toEqual(['110%', '120%', '131%'])
  expect(seen.every((s) => s.count === 1 && s.same)).toBe(true)
  await expect(status).toHaveAttribute('data-zoom-last', '131%')
  // Gone after its 1.5 s, which run from the last move, never before them (the page's own clock, read in the page), with a fade.
  const gone = await leave(page)
  expect(gone.after).toBeGreaterThanOrEqual(1500)
  expect(gone.transition).toBe('opacity')

  // At a bound a move still says the level, and that it is the largest or the smallest; a reset says 100%.
  const bounds = await move(page, [
    { level: 5, percent: 249, bound: 'largest' },
    { level: 5, percent: 249, bound: 'largest' },
    { level: 0, percent: 100, bound: '' },
    { level: -3, percent: 58, bound: 'smallest' },
  ])
  expect(bounds.map((s) => s.text)).toEqual(['249%, the largest', '249%, the largest', '100%', '58%, the smallest'])

  // The settings page, under the same app: the indicator is there too.
  await page.goto('/settings')
  await listening(page)
  expect((await move(page, [{ level: -0.5, percent: 91, bound: '' }]))[0]!.text).toBe('91%')
})

test('under prefers-reduced-motion the indicator goes without its fade', async ({ page, state }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await stubApp(page, state.token, state.baseURL, AT_100)
  await page.goto('/yard')
  await expect(page.locator('[data-sidebar-menus]').first()).toBeVisible()
  await listening(page)
  expect((await move(page, [{ level: 0.5, percent: 110, bound: '' }]))[0]!.text).toBe('110%')
  const gone = await leave(page)
  expect(gone.after).toBeGreaterThanOrEqual(1500)
  expect(gone.transition).toBe('none')
})

test('the indicator leaves the focus in the terminal and takes no clicks; typing goes on', async ({ page, api, state }) => {
  const cwd = join(state.root, 'zoom-focus')
  mkdirSync(cwd, { recursive: true })
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'shell', name: 'zoom-focus', cwd })
  try {
    await stubApp(page, state.token, state.baseURL, AT_100)
    await page.goto(`/sessions/${s.id}`)
    await expect(page.locator('[data-transport-state]').first()).toHaveAttribute('data-transport-state', 'open', { timeout: 30_000 })
    await listening(page)
    const terminal = page.locator('.terminal-host .xterm-helper-textarea').first()
    await terminal.focus()
    await expect(terminal).toBeFocused()
    const seen = await move(page, [
      { level: 0.5, percent: 110, bound: '' },
      { level: 1, percent: 120, bound: '' },
    ])
    expect(seen.map((x) => x.text)).toEqual(['110%', '120%'])
    expect(seen.every((x) => x.focusKept)).toBe(true)
    expect(seen.every((x) => !x.hit)).toBe(true)
    await expect(terminal).toBeFocused()
    await page.keyboard.type('echo typed > typed.txt\n')
    const typed = join(cwd, 'typed.txt')
    await expect.poll(() => (existsSync(typed) ? readFileSync(typed, 'utf8') : ''), { timeout: 15_000 }).toBe('typed\n')
  } finally {
    await api.stopSession(s.id)
  }
})

test('a level kept from before is said once per start of the app, not on every page load', async ({ page, state }) => {
  await stubApp(page, state.token, state.baseURL, { level: 1, percent: 120, bound: '' })
  await page.goto('/yard')
  await listening(page)
  await expect.poll(() => page.evaluate(() => (window as unknown as Stubbed).e2eZoomAsks)).toBe(1)
  await expect(page.locator('[data-zoom-status]')).toHaveAttribute('data-zoom-last', '120%')
  // The page loaded again, the app still running: asked, and nothing said.
  await page.reload()
  await listening(page)
  await expect.poll(() => page.evaluate(() => (window as unknown as Stubbed).e2eZoomAsks)).toBe(1)
  await expect(page.locator('[data-zoom-status]')).toHaveAttribute('data-zoom-last', '')
  await expect(page.locator('[data-zoom-indicator]')).toHaveCount(0)
})

test('a browser page, and an app whose bridge has no zoom event, show no indicator and throw nothing', async ({ page, state }) => {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  // A plain browser: no bridge.
  await page.goto('/yard')
  await expect(page.locator('[data-sidebar-menus]').first()).toBeVisible()
  await expect(page.locator('[data-zoom-status]')).toHaveCount(0)
  await expect(page.locator('[data-zoom-indicator]')).toHaveCount(0)
  // An older app: its bridge, without onZoom.
  await stubApp(page, state.token, state.baseURL, { level: 1, percent: 120, bound: '' }, { event: false })
  await page.goto('/yard')
  await expect(page.locator('[data-sidebar-menus]').first()).toBeVisible()
  await expect(page.locator('[data-zoom-status]')).toHaveCount(0)
  await expect(page.locator('[data-zoom-indicator]')).toHaveCount(0)
  expect(errors).toEqual([])
})
