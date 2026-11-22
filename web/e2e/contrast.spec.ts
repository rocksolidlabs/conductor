import type { Locator, Page } from '@playwright/test'
import { walk, worst, type Failure } from './contrast'
import { KNOWN, type Known, type WalkedPage } from './contrast-known'
import type { Day } from './day'
import { budget, dayPage, expect, showView, test } from './fixtures'
import { MEASURE_WAIT_MS } from './layout'

// The contrast actually drawn (the UX review's PR-25): every text the browser draws on seven pages of the seeded day, in both themes,
// at 1440 x 900 and at 390 x 844 with touch, judged over its real ground with opacity and tints counted (contrast.ts), against
// CONTRAST_FLOORS. A page's test fails on a text under its floor that contrast-known.ts does not list, and on an entry of that list
// for its own page, theme and width that its walk did not meet. At 1440 the page's first row is hovered and walked again: hover is
// where a tone on the canvas can vanish. The phone has no hover (Tailwind's hover: is for a pointer that hovers), so it walks at
// rest only. First, the walker is shown what it must judge.

test('the walker judges what it is shown', async ({ page }) => {
  await page.setContent(`<!doctype html><html><body style="margin:0;background:#fff;color:#000;font:400 14px sans-serif">
    <p data-case="767676" style="color:#767676">#767676 on white</p>
    <p data-case="777777" style="color:#777777">#777777 on white</p>
    <div data-case="faded" style="opacity:0.6"><p style="color:#666e67">muted ink, faded</p></div>
    <p><span data-case="tint" style="background:color-mix(in oklab, #c98a1b 10%, transparent);color:#805000">a warning badge</span></p>
    <p data-case="large" style="color:#949494;font-size:24px">large and light</p>
    <p data-case="small" style="color:#949494;font-size:14px">small and light</p>
    <button data-case="disabled" disabled style="color:#dddddd">disabled</button>
    <p data-case="hidden" style="position:absolute;width:1px;height:1px;overflow:hidden;clip-path:inset(50%);color:#eeeeee"><span>clipped to nothing</span></p>
  </body></html>`)
  const { judged, failures } = await walk(page)
  const cases = (fs: Failure[]) => fs.map((f) => f.path.match(/data-case="(\w+)"/)?.[1]).sort()
  expect(judged, 'the disabled and the clipped texts are not judged').toBe(6)
  expect(cases(failures), worst(failures)).toEqual(['777777', 'faded', 'small'])
  const by = Object.fromEntries(failures.map((f) => [f.path.match(/data-case="(\w+)"/)![1]!, f]))
  expect(by['777777']!.ratio).toBe(4.48)
  expect(by.small!.floor).toBe(4.5)
})

const SIZES = [
  { width: 1440 as const, height: 900, touch: false },
  { width: 390 as const, height: 844, touch: true },
]
const THEMES = ['dark', 'light'] as const

interface Walked {
  path: string
  /** The page's own marker, drawn once its content is. */
  ready: string
  /** The first row of the page's list, hovered at 1440. */
  row: string
  /** What the walk needs done first (a section unfolded). */
  prepare?: (page: Page) => Promise<void>
}

/** The day's parts, every one: each page is walked on the whole day, whatever an earlier spec of the worker built (the sidebar on
 *  every page lists what every part made). */
const PARTS = ['states', 'dispatch', 'waiting', 'runs', 'long', 'busy'] as const

/** Where each page is on the whole day. */
async function where(day: Day, name: WalkedPage, width: number): Promise<Walked> {
  const sidebarRow = '#dashboard-sidebar-main [data-row-kind="session"] a'
  for (const p of PARTS) await day.part(p)
  switch (name) {
    case 'yard': {
      return {
        path: '/yard',
        ready: '[data-yard-header]',
        row: sidebarRow,
        // The sidebar's Exited section starts folded: unfolded, its rows are walked too.
        prepare: async (page) => {
          if (width < 1024) return
          const exited = page.locator('#dashboard-sidebar-main [data-sidebar-section="exited"]')
          await expect(exited).toBeVisible()
          if ((await exited.getAttribute('data-folded')) === 'true') await exited.locator('button').first().click()
          await expect(exited).toHaveAttribute('data-folded', 'false')
          const row = exited.locator('[data-row-kind="session"]').first()
          await expect(row).toBeVisible()
          // The walk judges what is in view: the section sits at the list's foot, below the sidebar's scroll.
          await row.scrollIntoViewIfNeeded()
        },
      }
    }
    case 'session': {
      const n = await day.part('states')
      return {
        path: `/sessions/${n.driver}`,
        ready: width < 1024 ? '.terminal-host' : '[data-inspector]',
        row: sidebarRow,
        // The inspector opens closed: its People view opened from its rail, as a person opens it.
        prepare: async (page) => {
          if (width < 1024) return
          await showView(page, 'people')
          await expect(page.locator('[data-inspector-open="true"]')).toBeVisible()
        },
      }
    }
    case 'needs': {
      const ref = [...day.refs.keys()].find((r) => r.startsWith('question:'))!
      return { path: `/decisions?item=${encodeURIComponent(ref)}`, ready: `[data-inbox-item="${ref}"]`, row: '[data-inbox-row]' }
    }
    case 'agents':
      return { path: '/agents', ready: '[data-agent]', row: '[data-agent]' }
    case 'dispatch':
      return { path: '/dispatch/CND', ready: '[data-ticket-row]', row: '[data-ticket-row]' }
    case 'run': {
      const n = await day.part('runs')
      return { path: `/runs/${n['Ledger migration']}`, ready: '[data-run-view]', row: '[data-member]' }
    }
    case 'hosts':
      return { path: '/hosts', ready: '[data-hosts-section="runs"]', row: '[data-host-card]' }
  }
}

/**
 * The sidebar's first exited row (a session that ended: a run's member, or a loose one), shown the way a person shows it, wherever
 * the list puts it: while none is drawn, the first run's folded line ("and 2 more · 1 working · 1 exited", the sidebar's rows) is
 * unfolded, then the first folded section, each click waited on until it took, until an exited row is drawn. It is found by its
 * state, never by the section that holds it.
 */
async function showExited(page: Page): Promise<Locator> {
  const list = page.locator('#dashboard-sidebar-main')
  const exited = list.locator('[data-row-kind="session"][data-row-state="exited"]')
  // The list has measured itself and drawn the day's sessions before anything is judged absent.
  await expect(list.locator('[data-session-list][data-list-width]')).toBeVisible()
  await expect(list.locator('[data-row-kind="session"]').first()).toBeVisible()
  for (;;) {
    if ((await exited.count()) > 0) return exited.first()
    const more = list.locator('[data-row-kind="more"]').first()
    if ((await more.count()) > 0) {
      const id = await more.getAttribute('data-sidebar-row')
      await more.locator('[data-run-more]').click()
      await expect(list.locator(`[data-sidebar-row="${id}"]`)).toHaveCount(0)
      continue
    }
    const section = list.locator('[data-sidebar-section][data-folded="true"]').first()
    if ((await section.count()) > 0) {
      const key = await section.getAttribute('data-sidebar-section')
      await section.locator('button[aria-expanded]').first().click()
      await expect(list.locator(`[data-sidebar-section="${key}"]`)).toHaveAttribute('data-folded', 'false')
      continue
    }
    throw new Error('the sidebar draws no exited row with every run line and section unfolded: the day lists exited sessions')
  }
}

/** The failures not on the known list, and the known entries of this walk that it did not meet. */
function against(failures: Failure[], known: Known[]): { unknown: Failure[]; stale: Known[] } {
  return {
    unknown: failures.filter((f) => !known.some((k) => f.path.includes(k.path))),
    stale: known.filter((k) => !failures.some((f) => f.path.includes(k.path))),
  }
}

async function judgeWalk(page: Page, name: WalkedPage, theme: 'dark' | 'light', width: 1440 | 390, at: 'rest' | 'hover', root?: Locator): Promise<void> {
  const { judged, failures } = await walk(page, root)
  expect(judged, `${at}: the walk judged some text`).toBeGreaterThan(0)
  const known = KNOWN.filter((k) => k.page === name && k.theme === theme && k.width === width && k.at === at)
  const { unknown, stale } = against(failures, known)
  expect(unknown, `${name}, ${theme}, ${width}, ${at}: text under its floor (the ten worst):\n${worst(unknown)}`).toEqual([])
  expect(stale.map((k) => k.path), `${name}, ${theme}, ${width}, ${at}: known places the walk no longer finds: delete them from contrast-known.ts`).toEqual([])
}

const PAGES: WalkedPage[] = ['yard', 'session', 'needs', 'agents', 'dispatch', 'run', 'hosts']

for (const name of PAGES) {
  for (const size of SIZES) {
    for (const theme of THEMES) {
      test(`the ${name} page drawn in ${theme} at ${size.width}: every text over its floor`, async ({ day, browser }) => {
        budget(2 * MEASURE_WAIT_MS)
        const w = await where(day, name, size.width)
        const page = await dayPage(browser, { path: w.path, theme, width: size.width, height: size.height, touch: size.touch })
        await expect(page.locator(w.ready).first()).toBeVisible({ timeout: MEASURE_WAIT_MS })
        // The walk judges what is in view: at 1440 the sidebar's first exited row (a run's member that ended, folded into its run's
        // line in Needs you, or a loose one in the folded Exited section) is unfolded and scrolled to, so that every page walks one,
        // wherever the list's order and folds put it.
        if (!size.touch) {
          const exited = await showExited(page)
          await expect(exited).toBeVisible()
          await exited.scrollIntoViewIfNeeded()
        }
        await w.prepare?.(page)
        await judgeWalk(page, name, theme, size.width, 'rest')
        if (size.touch) return
        const row = page.locator(w.row).first()
        await row.hover()
        await judgeWalk(page, name, theme, size.width, 'hover', row)
      })
    }
  }
}

// The rail (the sidebar collapsed at 1440): the one number of what needs you on its amber fill, the squares and capsules, the foot's
// menus. Walked on the whole day, in both themes, the rail's root alone; nothing of it is a known place.
for (const theme of THEMES) {
  test(`the rail drawn in ${theme} at 1440: every text over its floor`, async ({ day, browser }) => {
    budget(2 * MEASURE_WAIT_MS)
    for (const p of PARTS) await day.part(p)
    const page = await dayPage(browser, { path: '/crews', theme })
    await page.keyboard.press('Alt+b')
    const rail = page.locator('#dashboard-sidebar-main')
    await expect(rail.locator('[data-rail]')).toBeVisible()
    await expect(rail.locator('[data-rail-count="needs"]')).toBeVisible({ timeout: MEASURE_WAIT_MS })
    const { judged, failures } = await walk(page, rail)
    expect(judged, 'the walk judged some text').toBeGreaterThan(0)
    expect(failures, `the rail, ${theme}: text under its floor (the ten worst):\n${worst(failures)}`).toEqual([])
  })
}
