import type { Browser, Page } from '@playwright/test'
import { walk } from './contrast'
import { expect, test, useTheme } from './fixtures'
import { noSideways, tapTarget } from './layout'
import { fakeShell, serverTraffic, shellDo, shellRecord, type FakeConfig } from './shell-fake'

// The decision page drawn from a shell's inbox (docs/plans/phone-shell.md, OSS-2b, Tests per surface, shell-decisions.spec.ts), with
// a fake provider of the tests' own whose replies the test holds and releases: the four rows exactly as given; a record with no
// history, comments or evidence contents drawn whole; `elsewhere` with its words and no Answer; a double tap sending once; another's
// answer, a changed decision and an offline wait each before any send; the ways in that do not open a decision; and the page's
// measures and contrast at a phone's size in both themes. The decision page outside a shell is the decision page specs' own.

const PHONE = { width: 390, height: 844 }
const STEPS = [
  { step: 'available', state: 'done', words: 'Request available', at: '2026-11-19T09:00:00Z' },
  { step: 'recorded', state: 'waiting', words: 'No answer yet' },
  { step: 'validated', state: 'waiting', words: 'Waiting for an answer' },
  { step: 'resumed', state: 'waiting', words: 'Not yet' },
]

function decision(type: 'clarification' | 'approval', rev: number, extra: Record<string, unknown> = {}) {
  return {
    id: 'dr-7', run: 'ship-0000aaaa-11112222', openedIn: 'ship-0000aaaa-11112222', origin: 'agent', type, policy: {}, state: 'open', rev,
    createdAt: '2026-11-19T09:00:00Z', asker: { task: 'build', attempt: 1, live: true },
    request: { question: 'Which region ships first?', options: [{ label: 'Europe', tradeoffs: 'quieter' }, { label: 'Americas' }], evidence: ['check:ship-0000aaaa-11112222/build/1/tests/1'] },
    ...extra,
  }
}

function item(extra: Record<string, unknown> = {}, d = decision('clarification', 3)) {
  return { id: 'i1', decision: d, rows: STEPS, owner: { name: 'Studio desk', reportedAt: '2026-11-19T09:58:00Z' }, asOf: '2026-11-19T09:59:00Z', offline: false, ...extra }
}

const signedIn = { signedIn: true, who: 'ana@example.test', notifications: 'on' }
const cfg = (items: Record<string, unknown>, extra: Partial<NonNullable<FakeConfig['provider']>> = {}): FakeConfig => ({ provider: { name: 'Desk relay', state: signedIn, items, ...extra } })

async function shellTab(browser: Browser, baseURL: string, c: FakeConfig): Promise<{ page: Page; api: string[]; close: () => Promise<void> }> {
  const ctx = await browser.newContext({ baseURL, viewport: PHONE, hasTouch: true, isMobile: true })
  const page = await ctx.newPage()
  await fakeShell(page, c)
  const api = serverTraffic(page)
  // Closed only once it is shown to have sent the server nothing, whatever route it visited.
  return {
    page,
    api,
    close: async () => {
      expect(api, 'no request or socket to the server from a shell').toEqual([])
      await ctx.close()
    },
  }
}

/** Opens the decision page of i1 and waits for its answer to be drawn. */
async function openItem(page: Page, id = 'i1') {
  await page.goto(`/shell/decisions/${id}`)
  await expect(page.locator('[data-shell-decision]')).toBeVisible()
}

test.describe("the decision page from a shell's inbox", () => {
  test('draws the record whole from what the provider holds: the four rows as given, the chips with their words, the omitted answers and the owner', async ({ browser, baseURL }) => {
    // Rows as a provider may give them: work resumed before the owner's check is shown as given, never merged or reordered.
    const rows = [STEPS[0], { step: 'recorded', state: 'done', words: 'Recorded at the desk' }, { step: 'validated', state: 'unknown', words: 'Not heard yet' }, { step: 'resumed', state: 'done', words: 'Resumed by the desk' }]
    const t = await shellTab(browser, baseURL!, cfg({ i1: item({ rows, responsesOmitted: 3, offline: true }) }))
    const { page } = t
    await openItem(page)
    await expect(page.locator('[data-step]')).toHaveCount(4)
    expect(await page.locator('[data-step]').evaluateAll((els) => els.map((e) => `${e.getAttribute('data-step')}:${e.getAttribute('data-step-state')}`))).toEqual(['available:done', 'recorded:done', 'validated:unknown', 'resumed:done'])
    await expect(page.locator('[data-step="resumed"] [data-step-words]')).toHaveText('Resumed by the desk')
    await expect(page.locator('[data-decision-question]')).toHaveText('Which region ships first?')
    await expect(page.locator('[data-decision-history-toggle]')).toHaveCount(0)
    await expect(page.locator('[data-ref-elsewhere-words]')).toHaveText('· open it on the desktop')
    await expect(page.locator('[data-shell-decision-omitted]')).toHaveText('3 earlier answers are not shown here')
    await expect(page.locator('[data-shell-line]')).toContainText('Held by Studio desk, last heard from at')
    await expect(page.locator('[data-shell-decision-offline]')).toHaveText(/^As of .+, offline$/)
    // The workbench's pages are not the shell's: the run is drawn as text, never as a link that would land on the inbox.
    expect(await page.locator('[data-decision-run]').evaluate((e) => e.tagName)).toBe('SPAN')
    await expect(page.locator('[data-shell-decision] a[href^="/runs/"]')).toHaveCount(0)
    await expect(page.locator('[data-decision-comment]')).toBeDisabled()
    await expect(page.locator('[data-decision-comment-off]')).toHaveText('Comment on the desktop: this phone sends answers, not comments')
    expect(t.api, 'no request to the server in a shell').toEqual([])
    await t.close()
  })

  test("an item answered elsewhere shows its words and the card whole, with no Answer", async ({ browser, baseURL }) => {
    const t = await shellTab(browser, baseURL!, cfg({ i1: item({ elsewhere: 'Answered by the release team on their own page' }) }))
    await openItem(t.page)
    await expect(t.page.locator('[data-shell-decision-elsewhere]')).toHaveText('Answered by the release team on their own page')
    await expect(t.page.locator('[data-decision-respond]')).toHaveCount(0)
    await expect(t.page.locator('[data-decision-option]')).toHaveCount(2)
    await t.close()
  })

  test('a double tap on Answer sends one answer; the rows that come back are shown and the draft is gone', async ({ browser, baseURL }) => {
    const recorded = item({ rows: [STEPS[0], { step: 'recorded', state: 'done', words: 'Recorded: Europe' }, STEPS[2], STEPS[3]] })
    const t = await shellTab(browser, baseURL!, cfg({ i1: { ...item(), reply: { outcome: 'kept', words: 'Kept: the desk is told', item: recorded } } }))
    const { page } = t
    await openItem(page)
    await page.locator('[data-decision-choice="0"]').click()
    await expect.poll(async () => (await shellRecord(page)).drafts.i1?.choice).toBe(0)
    await shellDo(page, 'hold', ['item'])
    await page.locator('[data-decision-send]').dblclick()
    await expect.poll(async () => (await shellRecord(page)).calls.filter((c) => c === 'item').length).toBe(2)
    await shellDo(page, 'release', 'item')
    await expect(page.locator('[data-decision-receipt]')).toContainText('Kept: the desk is told')
    await expect(page.locator('[data-step="recorded"] [data-step-words]')).toHaveText('Recorded: Europe')
    const r = await shellRecord(page)
    expect(r.responds).toHaveLength(1)
    expect(r.responds[0]).toMatchObject({ id: 'i1', expectedRev: 3, choice: 0 })
    expect(r.drafts.i1).toBeUndefined()
    await t.close()
  })

  test("another person's answer, and a changed decision, are shown before anything is sent; the draft is kept", async ({ browser, baseURL }) => {
    const t = await shellTab(browser, baseURL!, cfg({ i1: item() }))
    const { page } = t
    await openItem(page)
    await page.locator('[data-decision-choice="1"]').click()
    await expect.poll(async () => (await shellRecord(page)).drafts.i1?.choice).toBe(1)
    const responseId = (await shellRecord(page)).drafts.i1!.responseId

    // Changed: a new question at rev 4, which the page meets only as Answer reads the decision again.
    await shellDo(page, 'setItem', 'i1', item({}, decision('clarification', 4, { request: { question: 'Which region ships first, now that Asia is ready?', options: [{ label: 'Europe' }, { label: 'Americas' }, { label: 'Asia' }] } })), true)
    await page.locator('[data-decision-send]').click()
    await expect(page.locator('[data-decision-question]')).toHaveText('Which region ships first, now that Asia is ready?')
    await expect(page.locator('[data-decision-answering-words]')).toHaveText('This decision changed after you wrote your answer: read it again')
    let r = await shellRecord(page)
    expect(r.responds).toEqual([])
    expect(r.drafts.i1).toMatchObject({ rev: 4, choice: 1 })
    expect(r.drafts.i1!.responseId).not.toBe(responseId)

    // Answered first by another: its words; Answer held; nothing sent.
    await shellDo(page, 'setItem', 'i1', item({ rows: [STEPS[0], { step: 'recorded', state: 'done', words: 'Bo answered first: Americas' }, STEPS[2], STEPS[3]], own: { responseId: 'someone-else', state: 'answered', words: '' } }, decision('clarification', 4)), true)
    await page.locator('[data-decision-send]').click()
    await expect(page.locator('[data-decision-answering-words]')).toHaveText('Bo answered first: Americas')
    await expect(page.locator('[data-decision-send]')).toBeDisabled()
    r = await shellRecord(page)
    expect(r.responds).toEqual([])
    expect(r.drafts.i1).toMatchObject({ choice: 1 })
    await t.close()
  })

  test('offline, an answer waits; back online Confirm is offered, and the answer is sent once after it', async ({ browser, baseURL }) => {
    const recorded = item({ rows: [STEPS[0], { step: 'recorded', state: 'done', words: 'Recorded: Europe' }, STEPS[2], STEPS[3]] })
    const t = await shellTab(browser, baseURL!, cfg({ i1: { ...item(), reply: { outcome: 'kept', item: recorded } } }))
    const { page } = t
    await openItem(page)
    await page.locator('[data-decision-choice="0"]').click()
    await expect.poll(async () => (await shellRecord(page)).drafts.i1?.choice).toBe(0)
    await shellDo(page, 'offline', true)
    await page.locator('[data-decision-send]').click()
    await expect(page.locator('[data-decision-answering-words]')).toHaveText('Not sent: you are offline. It will be checked and sent when you are back online, if the decision is still as you read it')
    await expect(page.locator('[data-decision-answering-cancel]')).toBeVisible()
    await expect(page.locator('[data-decision-answering-confirm]')).toHaveCount(0)
    await expect(page.locator('[data-decision-choice="1"]')).toBeDisabled()
    // Said once, by the answer's own state: a wait is not a refusal.
    await expect(page.locator('[data-decision-refusal]')).toHaveCount(0)
    await shellDo(page, 'offline', false)
    await page.evaluate(() => window.dispatchEvent(new Event('online')))
    await expect(page.locator('[data-decision-answering-words]')).toHaveText('Back online: confirm to send your answer')
    expect((await shellRecord(page)).responds).toEqual([])
    await page.locator('[data-decision-answering-confirm]').click()
    // Kept: the rows the provider handed back, as given, and the form settled: its receipt, nothing that reads as not sent, no
    // choice left picked, Answer offering no second send.
    await expect(page.locator('[data-step="recorded"]')).toHaveAttribute('data-step-state', 'done')
    await expect(page.locator('[data-decision-answering]')).toHaveCount(0)
    await expect(page.locator('[data-decision-receipt]')).toBeVisible()
    await expect(page.locator('[data-decision-refusal]')).toHaveCount(0)
    await expect(page.locator('[data-decision-choice="0"]')).toHaveAttribute('data-decision-choice-label', 'Europe')
    await expect(page.locator('[data-decision-send]')).toBeDisabled()
    expect((await shellRecord(page)).responds).toHaveLength(1)
    expect((await shellRecord(page)).drafts.i1).toBeUndefined()
    // Pressed anyway, it sends nothing more.
    await page.locator('[data-decision-send]').click({ force: true })
    await page.evaluate(() => new Promise((r) => setTimeout(() => requestAnimationFrame(() => r(null)), 0)))
    expect((await shellRecord(page)).responds).toHaveLength(1)
    await expect(page.locator('[data-decision-receipt]')).toBeVisible()
    await t.close()
  })

  test('a decision page the person has left moves no one when its read answers late, and opens nothing', async ({ browser, baseURL }) => {
    const second = item({}, decision('clarification', 3, { request: { question: 'Which region ships second?', options: [{ label: 'Asia' }, { label: 'Africa' }] } }))
    const t = await shellTab(browser, baseURL!, cfg({ i1: item(), i2: { ...second, id: 'i2' } }))
    const { page } = t
    await page.addInitScript(() => void ((window as unknown as { __shell: { holdIds: string[] } }).__shell.holdIds = ['i1']))
    await page.goto('/shell')
    await expect(page.locator('[data-shell-inbox-row="i2"]')).toBeVisible()
    await page.locator('[data-shell-inbox-row="i1"]').click()
    await expect(page.locator('[data-shell-decision-reading]')).toBeVisible()
    await page.locator('[data-shell-back]').click()
    await page.locator('[data-shell-inbox-row="i2"]').click()
    await expect(page.locator('[data-shell-item="i2"]')).toBeVisible()
    // The first page's read is refused now, after the person left it.
    await shellDo(page, 'offline', true)
    const doneBefore = (await shellRecord(page)).done.filter((c) => c === 'item').length
    await shellDo(page, 'release', 'item:i1')
    await expect.poll(async () => (await shellRecord(page)).done.filter((c) => c === 'item').length).toBe(doneBefore + 1)
    await page.evaluate(() => new Promise((r) => setTimeout(() => requestAnimationFrame(() => r(null)), 0)))
    await expect(page).toHaveURL(/\/shell\/decisions\/i2$/)
    await expect(page.locator('[data-shell-item="i2"]')).toBeVisible()
    await expect(page.locator('[data-shell-inbox-note]')).toHaveCount(0)
    await t.close()
  })

  test("a notification's page offers Back and Open a server while it waits, and once left it moves no one", async ({ browser, baseURL }) => {
    const t = await shellTab(browser, baseURL!, cfg({ i1: item() }, { handles: { 'h-ok': { id: 'i1' } } }))
    const { page } = t
    await page.addInitScript(() => void ((window as unknown as { __shell: { hold: string[] } }).__shell.hold = ['resolve']))
    await page.goto('/shell/open?h=h-ok')
    await expect(page.locator('[data-shell-opening]')).toBeVisible()
    await expect(page.locator('[data-shell-to-server]')).toBeVisible()
    await page.locator('[data-shell-back]').click()
    await expect(page).toHaveURL(/\/shell$/)
    await expect(page.locator('[data-shell-inbox]')).toBeVisible()
    await shellDo(page, 'release', 'resolve')
    await expect.poll(async () => (await shellRecord(page)).done).toContain('resolve')
    await page.evaluate(() => new Promise((r) => setTimeout(() => requestAnimationFrame(() => r(null)), 0)))
    await expect(page).toHaveURL(/\/shell$/)
    await expect(page.locator('[data-shell-decision]')).toHaveCount(0)
    await t.close()
  })

  test('a notification tapped opens its item; one no longer current, one with the provider unreachable and an item not visible each open the inbox with their words', async ({ browser, baseURL }) => {
    const handles = { 'h-ok': { id: 'i1' }, 'h-gone': { code: 'unknown_handle', words: 'gone' }, 'h-far': { code: 'unreachable', words: 'far' } }
    const t = await shellTab(browser, baseURL!, cfg({ i1: item() }, { handles }))
    const { page } = t
    await page.goto('/shell')
    await expect(page.locator('[data-shell-inbox-row="i1"]')).toBeVisible()
    await shellDo(page, 'tap', { h: 'h-ok' })
    await expect(page).toHaveURL(/\/shell\/decisions\/i1$/)
    await expect(page.locator('[data-shell-decision]')).toBeVisible()
    await shellDo(page, 'tap', { h: 'h-gone' })
    await expect(page.locator('[data-shell-inbox-note]')).toHaveText('This notification is no longer current: here is what is waiting')
    await shellDo(page, 'offline', true)
    await shellDo(page, 'tap', { h: 'h-far' })
    await expect(page.locator('[data-shell-inbox-note]')).toHaveText(/^Desk relay cannot be reached: here is the last inbox, as of \d/)
    await expect(page.locator('[data-shell-inbox-row="i1"]')).toBeVisible()
    await expect(page.locator('[data-shell-inbox-asof]')).toHaveText(/, offline$/)
    await shellDo(page, 'offline', false)
    await shellDo(page, 'tap', { h: 'x'.repeat(257) })
    await expect(page).toHaveURL(/\/shell$/)
    await page.goto('/shell/decisions/nobody')
    await expect(page.locator('[data-shell-inbox-note]')).toHaveText('This decision is not one you can see here')
    expect(t.api).toEqual([])
    await t.close()
  })

  test('a link the provider routes opens its item, and one it does not opens the inbox', async ({ browser, baseURL }) => {
    const t = await shellTab(browser, baseURL!, cfg({ i1: item() }, { routes: { 'https://relay.example.test/d/1': 'i1' } }))
    const { page } = t
    await page.goto('/shell')
    await expect(page.locator('[data-shell-inbox]')).toBeVisible()
    await shellDo(page, 'link', 'https://relay.example.test/d/1')
    await expect(page).toHaveURL(/\/shell\/decisions\/i1$/)
    await expect(page.locator('[data-shell-decision]')).toBeVisible()
    await shellDo(page, 'link', 'https://elsewhere.example.test/x')
    await expect(page).toHaveURL(/\/shell$/)
    await t.close()

    const none = await shellTab(browser, baseURL!, { provider: null })
    await none.page.goto('/shell')
    await expect(none.page.locator('[data-shell-open-server]')).toBeVisible()
    await shellDo(none.page, 'link', 'https://desk.example.test/decide/dr-7')
    await expect(none.page.getByLabel('Server address')).toHaveValue('https://desk.example.test/decide/dr-7')
    expect((await shellRecord(none.page)).opened).toEqual([])
    await none.close()
  })

  test("the decision page and the inbox fit a phone and every text clears its floor, in both themes", async ({ browser, baseURL }) => {
    test.setTimeout(300_000)
    for (const theme of ['dark', 'light'] as const) {
      for (const path of ['/shell/decisions/i1', '/shell']) {
        await test.step(`${path} in ${theme}`, async () => {
          const t = await shellTab(browser, baseURL!, cfg({ i1: item({ responsesOmitted: 2, offline: true }) }))
          const { page } = t
          await page.goto(path)
          await useTheme(page, theme, '[data-shell] [data-shell-title]')
          await expect(page.locator(path === '/shell' ? '[data-shell-inbox-row]' : '[data-decision-respond]').first()).toBeVisible()
          await noSideways(page)
          await tapTarget(page.locator(':is([data-shell-inbox-row], [data-decision-choice], [data-decision-send], [data-shell-back], [data-shell-to-settings]):visible'))
          const { judged, failures } = await walk(page, page.locator('[data-shell]'))
          expect(judged).toBeGreaterThan(0)
          expect(failures).toEqual([])
          await t.close()
        })
      }
    }
  })
})
