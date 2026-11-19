import type { Locator, Page } from '@playwright/test'
import { budget, expect, stopStrays, test, type Session } from './fixtures'

// The Roundhouse's rotation goes on after the strip's thumbnails are opened and closed (#1021). Closing them shrinks the strip, the
// slide grows under the pointer that pressed the button, and the browser sends the slide a mouseenter with no move: the autoplay
// plugin's own hover stop took that for a hover and stopped the rotation while the header still said Rotating. The rotation is
// driven by a fake clock: every timeout of the rotation's interval (an interval no other timer of the app uses) is held by the page
// and fired by `advance`, so the test counts the advances over a known span and never waits out an interval.

/** The rotation's interval: kept in the page's settings, and the delay the fake clock holds. */
const ROT = 47_000
/** One expect's cap; each step raises the test's timeout by its own waits (AGENTS.md, Tests that do not flake). */
const STEP_MS = 15_000

declare global {
  interface Window {
    __rotation: { advance: (ms: number) => number; live: () => number }
  }
}

/** Moves the fake clock `ms` ahead, firing each held timer as it falls due (one set meanwhile included); the number fired. */
const advance = (page: Page, ms: number) => page.evaluate((ms) => window.__rotation.advance(ms), ms)
/** The rotation timers set and not yet fired or cleared: one while the rotation runs, none while it is stopped. */
const live = (page: Page) => page.evaluate(() => window.__rotation.live())

test('the rotation goes on after the thumbnails are opened and closed, at its own pace, and stays off when turned off or paused', async ({ page, api }) => {
  const made: Session[] = []
  for (const name of ['e2e rotation one', 'e2e rotation two']) made.push(await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name }))
  // Only this test's two sessions rotate: a stray would change which slide comes next.
  await stopStrays(
    api,
    made.map((s) => s.id),
  )
  try {
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.addInitScript((rot) => {
      localStorage.setItem('conductor.carousel.settings', JSON.stringify({ intervalMs: rot, autoplay: true, follow: false, strip: 'chips' }))
      const setTimeout = window.setTimeout.bind(window)
      const clearTimeout = window.clearTimeout.bind(window)
      const held = new Map<number, { due: number; fn: () => void }>()
      let now = 0
      let id = 1 << 30
      window.setTimeout = ((fn: (...a: unknown[]) => void, ms?: number, ...a: unknown[]) => {
        if (ms !== rot) return setTimeout(fn, ms, ...a)
        held.set(++id, { due: now + rot, fn: () => fn(...a) })
        return id
      }) as typeof window.setTimeout
      window.clearTimeout = ((t?: number) => {
        if (t !== undefined && held.delete(t)) return
        clearTimeout(t)
      }) as typeof window.clearTimeout
      window.__rotation = {
        advance(ms: number) {
          const until = now + ms
          let fired = 0
          for (;;) {
            let next: [number, { due: number; fn: () => void }] | undefined
            for (const e of held) if (e[1].due <= until && (!next || e[1].due < next[1].due)) next = e
            if (!next) break
            held.delete(next[0])
            now = next[1].due
            fired++
            next[1].fn()
          }
          now = until
          return fired
        },
        live: () => held.size,
      }
    }, ROT)

    // 1. Go into Yard. 2. Choose Roundhouse.
    // The waits before the first step have caps of their own (the sidebar, two chips, the count, the header's
    // two words): the test's timeout is raised by their sum before the page is asked for.
    budget(4 * 30_000 + 4 * STEP_MS)
    await page.goto('/yard')
    await expect(page.locator('#dashboard-sidebar-main')).toBeVisible({ timeout: 30_000 })
    await page.locator('[data-yard-view="roundhouse"]').click()
    const strip = page.locator('[data-carousel-strip]')
    for (const s of made) await expect(strip.locator(`[data-strip-chip="${s.id}"]`)).toBeVisible({ timeout: 30_000 })
    await expect(strip.locator('[data-strip-chip]')).toHaveCount(2)
    const header = page.locator('[data-roundhouse-header]')
    const state = (word: string) => header.getByRole('button', { name: word, exact: true })
    const current = strip.locator('[data-strip-chip][aria-current="true"]')
    const shown = async () => (await current.getAttribute('data-strip-chip')) ?? ''
    const other = (id: string) => made.find((s) => s.id !== id)!.id
    await expect(state('Rotating')).toBeVisible()
    await expect.poll(() => live(page)).toBe(1)

    /** One interval: nothing a millisecond before it ends, one advance as it ends, the other session shown. */
    const step = async () => {
      budget(STEP_MS)
      const from = await shown()
      expect(await advance(page, ROT - 1), 'nothing advances before the interval ends').toBe(0)
      expect(await advance(page, 1), 'one advance as the interval ends').toBe(1)
      await expect(current).toHaveAttribute('data-strip-chip', other(from))
      expect(await live(page), 'one timer set for the next interval').toBe(1)
    }
    /** The advances over three intervals, and the session shown after them (with two sessions, the other one). */
    const span = async () => {
      budget(STEP_MS)
      const from = await shown()
      const n = await advance(page, 3 * ROT)
      await expect(current).toHaveAttribute('data-strip-chip', other(from))
      return n
    }
    const toggle = page.locator('[data-strip-view]')
    /** Opens the thumbnails and closes them, each by its button as a person presses it; the place pressed last, for the pointer. */
    const openAndClose = async () => {
      budget(2 * STEP_MS)
      await toggle.click()
      await expect(strip.locator('[data-strip-thumb]').first()).toBeVisible()
      const at = (await toggle.boundingBox())!
      await toggle.click()
      await expect(strip.locator('[data-strip-thumb]')).toHaveCount(0)
      return { x: at.x + at.width / 2, y: at.y + at.height / 2 }
    }

    await step()
    const before = await span()
    expect(before).toBe(3)

    // 3. Open the thumbnails. 4. Close them. The slide has grown under the pointer, which has not moved since it pressed the button.
    const pressed = await openAndClose()
    expect(await page.evaluate(({ x, y }) => !!document.elementFromPoint(x, y)?.closest('[data-slot="viewport"]'), pressed), 'the pointer rests over the slide').toBe(true)
    // 5. The rotation goes on, and the header says so.
    await expect(state('Rotating')).toBeVisible()
    await step()
    await step()

    // Again and again: ten more times, then the same number of advances over the same span, each at its interval.
    for (let i = 0; i < 10; i++) await openAndClose()
    expect(await live(page)).toBe(1)
    expect(await span()).toBe(before)
    await step()

    // The hover hold is kept for a pointer that moves over the slide, and ends when it leaves.
    budget(3 * STEP_MS)
    await page.mouse.move(pressed.x - 40, pressed.y - 200)
    await expect.poll(() => live(page)).toBe(0)
    const held = await shown()
    expect(await advance(page, 3 * ROT)).toBe(0)
    await expect(current).toHaveAttribute('data-strip-chip', held)
    await page.mouse.move(5, 5)
    await expect.poll(() => live(page)).toBe(1)
    await step()

    /** Opening and closing the thumbnails leaves a stopped rotation stopped: nothing advances over three intervals. */
    const staysStopped = async (word: string) => {
      budget(3 * STEP_MS)
      await expect.poll(() => live(page)).toBe(0)
      await openAndClose()
      await openAndClose()
      const still = await shown()
      expect(await advance(page, 3 * ROT)).toBe(0)
      await expect(current).toHaveAttribute('data-strip-chip', still)
      await expect(state(word)).toBeVisible()
      expect(await live(page)).toBe(0)
    }

    // A rotation the person paused stays paused.
    await state('Rotating').click()
    await expect(state('Paused')).toBeVisible()
    await staysStopped('Paused')
    await state('Paused').click()
    await expect(state('Rotating')).toBeVisible()
    await expect.poll(() => live(page)).toBe(1)
    await step()

    // Auto-rotate turned off stays off.
    budget(2 * STEP_MS)
    const auto: Locator = header.getByRole('switch', { name: 'Auto-rotate' })
    await auto.click()
    await expect(auto).toHaveAttribute('aria-checked', 'false')
    await expect(state('Manual')).toBeVisible()
    await staysStopped('Manual')
    await expect(auto).toHaveAttribute('aria-checked', 'false')
    await auto.click()
    await expect(state('Rotating')).toBeVisible()
    await expect.poll(() => live(page)).toBe(1)
    await step()
  } finally {
    for (const s of made) await api.stopSession(s.id)
  }
})
