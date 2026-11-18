import type { Locator, Page } from '@playwright/test'
import { dayPage, expect, test, useTheme, type DayPageOptions } from './fixtures'
import { stubInbox } from './inbox-stub'
import { walk, worst } from './contrast'
import { inside, noSideways } from './layout'
import { ledgerRun, serveRun, type StubRun } from './run-fixtures'

// The run page's origin line: a run a registered provider launched says, in the header's second row, "Launched by <label> for
// <subject> at <start commit>", the subject a link when it is an https link with no userinfo, query or fragment, and as text
// otherwise; a continuation of such a run says it continues one. The run's read is the stub's (run-fixtures.ts) with the `origin`
// GET /api/runs/{run} carries for such a run (docs/protocol.md): a stub proves how the line is drawn, never that the server sends it.

/** A run's `origin`, as GET /api/runs/{run} carries it. */
type Origin = {
  provider: string
  ref: string
  label: string
  subject?: string
  subjectUrl?: string
  prior?: string
  project?: string
  startCommit?: string
  deadline?: string
  limitSeconds?: number
  continued?: boolean
}

const COMMIT = 'a1b2c3d4e5f60718293a4b5c6d7e8f9012345678'
const SUBJECT = `change:git.example.com/acme/web#12@${COMMIT}`
const LINK = 'https://git.example.com/acme/web/pull/12'
const ORIGIN: Origin = {
  provider: 'example',
  ref: 'example:42',
  label: 'Nightly checks of the web app',
  subject: SUBJECT,
  subjectUrl: LINK,
  startCommit: COMMIT,
  deadline: '2026-11-18T10:00:00Z',
  limitSeconds: 3600,
}

/** The Ledger run with origin o, served, on a page of its own at the run. */
async function originPage(browser: Parameters<typeof dayPage>[0], o: Origin | undefined, opts: DayPageOptions = {}): Promise<{ page: Page; header: Locator; line: Locator }> {
  const run: StubRun & { origin?: Origin } = { ...ledgerRun(), ...(o ? { origin: o } : {}) }
  const page = await dayPage(browser, { path: '/', ...opts })
  await stubInbox(page, [])
  await serveRun(page, run)
  await page.goto(`/runs/${run.id}`)
  const header = page.locator('[data-crew-run-header]')
  await expect(header.locator('[data-run-name]')).toHaveText(run.name)
  return { page, header, line: header.locator('[data-run-origin]') }
}

test('a run a provider launched says by what, for what and at which commit, with the subject’s link, in both modes', async ({ day, browser }) => {
  const { page, header, line } = await originPage(browser, ORIGIN, { theme: 'light' })
  const whole = `Launched by ${ORIGIN.label} for ${SUBJECT} at ${COMMIT}`
  for (const mode of ['light', 'dark'] as const) {
    if (mode === 'dark') await useTheme(page, 'dark', '#dashboard-sidebar-main')
    await expect(line, mode).toBeVisible()
    await expect(line, mode).toHaveAttribute('title', whole)
    await expect(line.locator('[data-run-origin-label]'), mode).toHaveText(ORIGIN.label)
    await expect(line.locator('[data-run-origin-commit]'), mode).toHaveText(COMMIT.slice(0, 7))
    const link = line.locator('a[data-run-origin-link]')
    await expect(link, mode).toHaveAttribute('href', LINK)
    await expect(link, mode).toHaveAttribute('rel', 'noopener noreferrer')
    await expect(link, mode).toHaveAttribute('target', '_blank')
    // The subject is cut for the eye (48 characters, an ellipsis) and whole in the line's title.
    await expect(link, mode).toHaveText(`${SUBJECT.slice(0, 47)}…`)
    await inside(line, header.locator('[data-run-summary]'))
    // Every text of the line meets its contrast floor in this mode (contrast.ts's table, the one contrast.spec.ts holds pages to).
    const { judged, failures } = await walk(page, line)
    expect(judged, mode).toBeGreaterThanOrEqual(5)
    expect(failures, `${mode}: ${worst(failures)}`).toEqual([])
  }
  void day
})

test('a subject’s link the page may not open is shown as text, and markup in a label is text', async ({ day, browser }) => {
  for (const url of ['javascript:alert(1)', 'https://git.example.com/acme/web/pull/12?token=abc', 'https://u:p@git.example.com/x']) {
    const { page, line } = await originPage(browser, { ...ORIGIN, label: '<b>bold</b> & <i>not</i>', subjectUrl: url })
    await expect(line, url).toBeVisible()
    await expect(line.locator('a'), url).toHaveCount(0)
    await expect(line.locator('[data-run-origin-subject]'), url).toHaveText(`${SUBJECT.slice(0, 47)}…`)
    await expect(line.locator('[data-run-origin-label]'), url).toHaveText('<b>bold</b> & <i>not</i>')
    await expect(line.locator('b, i'), url).toHaveCount(0)
    await page.context().close()
  }
  void day
})

test('a continuation says it continues a provider’s run; a run with no origin has no line', async ({ day, browser }) => {
  const { page, line } = await originPage(browser, { provider: 'example', ref: 'example:42', label: 'Nightly', continued: true })
  await expect(line).toHaveAttribute('title', 'Continues a run launched by Nightly')
  await expect(line).toHaveAttribute('data-run-origin-continued', '')
  await expect(line.locator('[data-run-origin-subject], [data-run-origin-commit]')).toHaveCount(0)
  await page.context().close()
  const none = await originPage(browser, undefined)
  // The header is drawn (its name above) with its second row, and holds no origin line.
  await expect(none.header.locator('[data-run-summary]')).toBeVisible()
  await expect(none.line).toHaveCount(0)
  void day
})

test('on a phone, in both modes, the line wraps inside the header and nothing scrolls sideways', async ({ day, browser }) => {
  const { page, header, line } = await originPage(browser, { ...ORIGIN, label: 'é'.repeat(60) }, { width: 390, height: 844, touch: true, theme: 'dark' })
  for (const mode of ['dark', 'light'] as const) {
    if (mode === 'light') await useTheme(page, 'light', '[data-bottom-bar]')
    await expect(line, mode).toBeVisible()
    // The label at its bound is cut for the eye at 40 characters and whole in the title.
    await expect(line.locator('[data-run-origin-label]'), mode).toHaveText(`${'é'.repeat(39)}…`)
    await expect(line, mode).toHaveAttribute('title', `Launched by ${'é'.repeat(60)} for ${SUBJECT} at ${COMMIT}`)
    await inside(line, header)
    await noSideways(page, { root: header })
  }
  void day
})
