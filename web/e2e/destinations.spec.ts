import type { Locator, Page } from '@playwright/test'
import { dayPage, expect, test } from './fixtures'

// Every list of the pages, as drawn (the UX review's P12: ten destinations became seven): the sidebar's tiles and their More, the
// rail, the palette's Go to rows, the phone's bar and its More. Each is compared with a literal list written here, so a change to the
// pages' table (web/app/utils/pages.ts) changes this test with it. This server has Brakeman and is no desktop app: no Settings.

/** The list drawn equals `want`, by each entry's name, polled while it fills: a failure names the first entry that differs. */
async function drawn(l: Locator, want: string[], read: (s: string) => string = (s) => s) {
  await expect.poll(async () => (await l.allTextContents()).map((s) => name(read(s).trim())), { message: `${l}` }).toEqual(want)
}

/** The first word group of a label: "Needs you 9" reads "Needs you", "Agents G A" reads "Agents". */
const name = (s: string) => s.replace(/\s*(\d+\+?|G\s*[A-Z])\s*$/, '').replace(/\s*·\s*\d+\+? waiting$/, '').trim()

async function needsRead(page: Page) {
  await expect(page.locator('#dashboard-sidebar-main nav[data-needs-read="true"]')).toHaveCount(1)
}

test('every list of the pages, as drawn', async ({ browser, state }) => {
  const page = await dayPage(browser, { server: state, path: '/crews', width: 1440, height: 900 })
  const sidebar = page.locator('#dashboard-sidebar-main')

  // The sidebar's tiles, once the server has said it has Brakeman (its slot is held until then), and More's menu.
  const tiles = page.getByRole('navigation', { name: 'Pages' })
  await expect(tiles.locator('[data-sidebar-brakeman]')).toBeVisible()
  await drawn(tiles.locator('[data-sidebar-tile] [data-nav-label]'), ['Needs you', 'Yard', 'Crews', 'Dispatch', 'Brakeman', 'More'])
  await tiles.locator('[data-sidebar-tile="more"]').click()
  const menu = page.getByRole('menu')
  await drawn(menu.getByRole('menuitem').locator('[data-slot="itemLabel"]'), ['Agents', 'History'])
  await page.keyboard.press('Escape')
  await expect(menu).toHaveCount(0)

  // The palette's Go to rows, nothing typed. Brakeman keeps its own group.
  await expect(sidebar).toBeVisible()
  await page.keyboard.press('Alt+/')
  const palette = page.locator('[data-palette]')
  await expect(palette).toBeVisible()
  const go = palette.locator('[data-palette-group="goto"] [data-palette-row] [data-palette-label]')
  await drawn(go, ['Needs you', 'Yard', 'Roundhouse', 'Crews', 'Dispatch', 'Agents', 'Agents · Reporting', 'History', 'History · Live', 'Paste invite'])
  await page.keyboard.press('Escape')
  await expect(palette).toBeHidden()

  // The rail, the sidebar folded by its key: every page as an icon, named by its label. Needs you's row shows by its rule (PR-5:
  // it leaves when nothing waits and nothing was decided that day, which this shared server's earlier specs decide), first when it
  // does; the rest are always these, in this order.
  await page.keyboard.press('Alt+b')
  await expect(sidebar.locator('[data-rail]')).toBeVisible()
  await needsRead(page)
  const rail = sidebar.getByRole('navigation').last().getByRole('link')
  const railNames = await rail.evaluateAll((els) => els.map((el) => (el.getAttribute('aria-label') ?? '').replace(/,.*$/, '')))
  const rest = railNames[0] === 'Needs you' ? railNames.slice(1) : railNames
  expect(rest).toEqual(['Yard', 'Crews', 'Dispatch', 'Brakeman', 'Agents', 'History'])
  await page.keyboard.press('Alt+b')
  await expect(sidebar.locator('[data-rail]')).toHaveCount(0)

  // The phone, with touch: the bar's five places, then More's pages.
  const phone = await dayPage(browser, { server: state, path: '/sessions', width: 390, height: 844, touch: true })
  const bar = phone.locator('[data-bottom-bar]')
  await drawn(bar.locator('[data-bottom-tab]'), ['Needs you', 'Sessions', 'Yard', 'Crews', 'More'], (s) => s.replace(/\d+\+?/g, ''))
  // Brakeman shows once the phone's page has read that the server has it.
  await phone.locator('[data-bottom-tab="more"]').tap()
  const more = phone.locator('[data-bottom-more]')
  await expect(more.getByRole('link', { name: 'Brakeman' })).toBeVisible()
  await drawn(more.getByRole('link'), ['Dispatch', 'Brakeman', 'Agents', 'History'])
})
