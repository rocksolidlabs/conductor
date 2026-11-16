import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { Page } from '@playwright/test'
import { parseColor, ratio, toHex } from '../app/utils/contrast'
import { PAGES } from '../app/utils/pages'
import { collect, composed, floorOf, STILL } from './contrast'
import type { Day } from './day'
import { budget, dayPage, expect, test } from './fixtures'
import { layoutRoot, MEASURE_WAIT_MS } from './layout'
import { ownServer, type OwnServer } from './own-server'

// The design capture (the UX review's PR-38, `make design-capture`): by hand at a release, never in CI. On the seeded day, at
// 1440 x 900 and at 390 x 844 with touch, in each theme: every page of the pages' table (utils/pages.ts), a session, a run, a
// ticket, the Needs you page with an item open, and the Needs you page at zero on a server of its own. Each screen is written to
// web/test-results/look/<page>-<theme>-<width>.png, and web/test-results/look/look.json gets the values the pages computed: every
// `--ui-*` token and `--color-ink-*` step in each theme (read through a probe element), each status mark drawn with its word and
// colour, and the ten texts nearest their contrast floor. scripts/design_drift.py compares that file with docs/design/system.json.
// It asserts nothing of the design: it records it.

const look = process.env.CONDUCTOR_E2E_LOOK === '1'
test.skip(!look, 'set CONDUCTOR_E2E_LOOK=1 (make design-capture)')

const OUT = join(dirname(fileURLToPath(import.meta.url)), '..', 'test-results', 'look')
const SIZES = [
  { width: 1440, height: 900, touch: false },
  { width: 390, height: 844, touch: true },
] as const
const THEMES = ['dark', 'light'] as const
type Theme = (typeof THEMES)[number]

/** A screen: its name in the files, where it is, and what is drawn once its content is (the layout's root when not given). */
interface Screen {
  name: string
  path: string
  ready?: string
}

/** A text near its floor, as the report keeps it. */
interface Near {
  screen: string
  theme: Theme
  width: number
  text: string
  ratio: number
  floor: number
  fg: string
  bg: string
}

/** What the capture gathers over its tests, written as look.json at the end. */
const gathered = {
  pages: new Set<string>(),
  tokens: {} as Record<Theme, Record<string, string>>,
  status: {} as Record<string, { words: Set<string>; colour: Partial<Record<Theme, Set<string>>> }>,
  near: [] as Near[],
}

/** A colour as the report writes it: `#rrggbb`, with the alpha as two more digits when it is below 1. */
function hexOf(value: string): string | undefined {
  const c = parseColor(value)
  if (!c) return undefined
  return c.a < 1 ? `${toHex(c)}${Math.round(c.a * 255).toString(16).padStart(2, '0')}` : toHex(c)
}

/**
 * Every `--ui-*` and `--color-ink-*` custom property the page's style sheets declare, resolved on the root: a colour through a probe
 * element (its computed `color`, under a parent of a colour no token has, so that a value that is no colour shows as that parent's),
 * anything else as the root computes it.
 */
async function tokens(page: Page): Promise<Record<string, string>> {
  const raw = await page.evaluate(() => {
    const names = new Set<string>()
    const visit = (rules: CSSRuleList) => {
      for (const r of Array.from(rules)) {
        if (r instanceof CSSStyleRule) for (const p of Array.from(r.style)) if (p.startsWith('--ui-') || p.startsWith('--color-ink-')) names.add(p)
        if ('cssRules' in r && (r as CSSGroupingRule).cssRules) visit((r as CSSGroupingRule).cssRules)
      }
    }
    for (const s of Array.from(document.styleSheets)) {
      try {
        visit(s.cssRules)
      } catch {
        // A sheet of another origin: none here, and none would declare the app's tokens.
      }
    }
    const parent = document.body.appendChild(document.createElement('div'))
    parent.style.color = 'rgb(1, 2, 3)'
    const probe = parent.appendChild(document.createElement('div'))
    const root = getComputedStyle(document.documentElement)
    const out: Record<string, { colour: string | null; value: string }> = {}
    for (const n of [...names].sort()) {
      probe.style.color = `var(${n})`
      const c = getComputedStyle(probe).color
      out[n] = { colour: c === 'rgb(1, 2, 3)' ? null : c, value: root.getPropertyValue(n).trim() }
    }
    parent.remove()
    return out
  })
  return Object.fromEntries(Object.entries(raw).map(([n, v]) => [n, (v.colour && hexOf(v.colour)) ?? v.value]))
}

/** Each status mark drawn: its key, the word its legend gives (stable, unlike "finished 12m ago") and its glyph's colour. */
async function marks(page: Page): Promise<Array<{ key: string; word: string; colour: string }>> {
  return page.evaluate(() =>
    Array.from(document.querySelectorAll<HTMLElement>('[data-status-mark]')).map((m) => {
      const legend = (m.getAttribute('title') ?? '').split(' · ').pop() ?? ''
      const glyph = m.querySelector('[data-status-glyph]')
      return { key: m.dataset.statusMark!, word: legend.split(':')[0]!.trim(), colour: glyph ? getComputedStyle(glyph).color : '' }
    }),
  )
}

/** What a page drawn without the layout draws once it is there, by its id in the pages' table; every other page draws the layout. */
const OWN_READY: Partial<Record<string, string>> = { paste: '[data-paste-page]' }

/** The screens of the day: the pages' table, then a session, a run, a ticket and the Needs you page with an item open. */
async function screens(day: Day): Promise<Screen[]> {
  const states = await day.part('states')
  const runs = await day.part('runs')
  await day.part('dispatch')
  await day.part('busy')
  const question = [...day.refs.keys()].find((r) => r.startsWith('question:'))
  const out: Screen[] = PAGES.map((p) => ({ name: p.id, path: p.to, ready: OWN_READY[p.id] }))
  out.push({ name: 'session', path: `/sessions/${states.driver}`, ready: '.terminal-host' })
  out.push({ name: 'run', path: `/runs/${encodeURIComponent(runs['Ledger migration']!)}`, ready: '[data-run-view]' })
  out.push({ name: 'ticket', path: '/dispatch/CND/1', ready: '[data-ticket-strip]' })
  if (question) out.push({ name: 'needs-item', path: `/decisions?item=${encodeURIComponent(question)}`, ready: `[data-inbox-item="${question}"]` })
  return out
}

/** Goes to the screen, waits for it, and records it: its picture, its marks and its texts nearest their floor. */
async function capture(page: Page, s: Screen, theme: Theme, width: number): Promise<void> {
  budget(2 * MEASURE_WAIT_MS)
  await page.goto(s.path)
  await expect(page.locator(s.ready ?? layoutRoot(width)).first()).toBeVisible({ timeout: MEASURE_WAIT_MS })
  await page.evaluate(() => document.fonts.ready.then(() => undefined))
  await page.addStyleTag({ content: STILL })
  // What the page reads after its first draw: a bound on a hang only; a page that keeps a stream open never goes quiet.
  await page.waitForLoadState('networkidle', { timeout: MEASURE_WAIT_MS }).catch(() => undefined)
  mkdirSync(OUT, { recursive: true })
  await page.screenshot({ path: join(OUT, `${s.name}-${theme}-${width}.png`) })
  gathered.pages.add(s.name)
  for (const m of await marks(page)) {
    const at = (gathered.status[m.key] ??= { words: new Set(), colour: {} })
    if (m.word) at.words.add(m.word)
    const c = hexOf(m.colour)
    if (c) (at.colour[theme] ??= new Set()).add(c)
  }
  for (const d of await collect(page)) {
    const { fg, bg } = composed(d)
    const r = ratio(fg, bg)
    gathered.near.push({ screen: s.name, theme, width, text: d.text.slice(0, 80), ratio: Math.round(r * 100) / 100, floor: floorOf(d), fg: toHex(fg), bg: toHex(bg) })
  }
  // Keep the ten nearest as the walk goes, so the list stays small.
  gathered.near.sort((a, b) => a.ratio / a.floor - b.ratio / b.floor || a.screen.localeCompare(b.screen))
  gathered.near.splice(10)
}

for (const theme of THEMES) {
  for (const size of SIZES) {
    test(`the day's screens, ${theme}, ${size.width} x ${size.height}`, async ({ browser, day }) => {
      const list = await screens(day)
      const page = await dayPage(browser, { width: size.width, height: size.height, touch: size.touch, theme })
      if (size.width === 1440) gathered.tokens[theme] = await tokens(page)
      for (const s of list) await capture(page, s, theme, size.width)
    })
  }
}

test.describe('the Needs you page at zero', () => {
  let own: OwnServer
  test.beforeAll(async () => {
    own = await ownServer('look-zero')
  })
  test.afterAll(async () => {
    await own?.stop()
  })
  for (const theme of THEMES) {
    for (const size of SIZES) {
      test(`${theme}, ${size.width} x ${size.height}`, async ({ browser }) => {
        const page = await dayPage(browser, { server: own.started, width: size.width, height: size.height, touch: size.touch, theme, path: '/decisions' })
        await capture(page, { name: 'needs-zero', path: '/decisions' }, theme, size.width)
      })
    }
  }
})

test.afterAll(() => {
  const status = Object.fromEntries(
    Object.entries(gathered.status)
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([k, v]) => [k, { words: [...v.words].sort(), colour: Object.fromEntries(Object.entries(v.colour).map(([t, cs]) => [t, [...cs!].sort().join(' | ')])) }]),
  )
  mkdirSync(OUT, { recursive: true })
  writeFileSync(join(OUT, 'look.json'), `${JSON.stringify({ pages: [...gathered.pages].sort(), tokens: gathered.tokens, status, contrast: gathered.near }, null, 2)}\n`)
})
