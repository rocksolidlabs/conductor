import type { Page } from '@playwright/test'
import { budget, dayPage, expect, test, useTheme } from './fixtures'
import { noSideways } from './layout'
import { SETTLE_MS, checkScript, comparison, launchComparison, selectionUntil } from './compare-select-helpers'
import { decisionUntil } from './decisions-helpers'
import { ownServer } from './own-server'

// Review the selected candidate (Program item CP1, docs/plans/compare-crew.md, PR-8b), on a server of the spec's own: two stub
// candidates of one base. Before the selection a Request changes is held, the view saying why ("select a candidate first"); then
// approach-b is selected with a rationale through DR1's route. From the ticket's Decisions and from the comparison, RV1's Review view
// opens on approach-b; its picker says selected and not selected, and opens on the selected one by itself. On approach-a the view says
// before anything is written that it was not selected, in not_selected's words, and holds Request changes, nothing sent; one of
// approach-b makes the repair, whose new candidate the picker shows and the view then opens on, while the comparison still labels
// approach-b selected at the commit the selection named. Every wait is on the server's reads or on what the page draws.

const LOAD_MS = 15_000
/** The bound of a view that reads the candidate's diff and the comparison before it is drawn: a hang's bound on a loaded machine. */
const VIEW_MS = 60_000

/** A review draft of `candidate` kept in the browser as the Review view keeps one: Request changes, one note on the whole candidate. */
async function draft(page: Page, ticket: string, candidate: string, base: string, message: string) {
  await page.evaluate(
    ([k, v]) => localStorage.setItem(k!, v!),
    [`conductor.reviewDraft:${ticket}:${candidate}`, JSON.stringify({ v: 1, base, verdict: 'request_changes', summary: '', notes: [{ key: 'n1', kind: 'general', intent: 'change', severity: 'major', message }] })],
  )
}

test('Review the selected candidate opens RV1’s view on it; a Request changes of a candidate not selected is refused, of the selected one makes its repair', async ({ browser }) => {
  budget(10 * LOAD_MS + 60_000)
  const s = await ownServer('compare-review')
  try {
    checkScript(s.repo)
    const c = await launchComparison(s.api, s.repo, 'CREV', [{ agentId: 'worker-cli', approach: 'Key the cache by locale' }, { agentId: 'worker-cli', approach: 'Memoise in the handler' }])
    const sel = await selectionUntil(s.api, c.run, 'open')
    const [a, b] = [sel.options[0]!.subject!, sel.options[1]!.subject!]
    const base = ((await comparison(s.api, c.run)) as unknown as { base: string }).base
    const aRef = `${c.run}/approach-a/${a.n}@${a.commit}`
    const bRef = `${c.run}/approach-b/${b.n}@${b.commit}`
    const page = await dayPage(browser, { server: s.started, path: '/agents' })

    // Every review the page sends: none may go while Request changes is held.
    const posted: string[] = []
    page.on('request', (r) => {
      if (r.method() === 'POST' && /\/reviews$/.test(new URL(r.url()).pathname)) posted.push(r.url())
    })

    // Before the selection, a Request changes of either candidate is held, and why is said before anything is sent: a repair would make
    // them unlike (the server refuses it in the same words).
    budget(2 * VIEW_MS + 6 * LOAD_MS)
    await draft(page, c.ticket, bRef, base, 'Before the selection.')
    await page.goto(`${c.path}?view=review&candidate=${encodeURIComponent(bRef)}`)
    await expect(page.locator(`[data-review-view][data-review-candidate="${bRef}"]`)).toHaveAttribute('data-review-diff-base', base, { timeout: VIEW_MS })
    await expect(page.locator('[data-review-hold-words]')).toHaveText('Select a candidate first: a repair before the selection would make the candidates unlike.', { timeout: VIEW_MS })
    await expect(page.getByRole('radio', { name: /^Request changes/ })).toBeDisabled()
    await expect(page.locator('[data-review-draft-problems]')).toContainText('Request changes is not sent: select a candidate first')
    await expect(page.locator('[data-review-send]')).toBeDisabled()
    expect(posted).toEqual([])
    expect((await s.api.ok<{ reviews: unknown[] }>('GET', `/api/dispatch/tickets/${c.ticket}/reviews`)).reviews).toEqual([])
    await page.evaluate((k) => localStorage.removeItem(k), `conductor.reviewDraft:${c.ticket}:${bRef}`)

    const rev = (await s.api.ok<{ decision: { rev: number } }>('GET', `/api/decisions/${sel.decision}`)).decision.rev
    await s.api.ok('POST', `/api/decisions/${sel.decision}/respond`, { responseId: 'crev-select-b', choice: 1, rationale: 'approach-b keeps the handler simple', expectedRev: rev })
    await decisionUntil(s.api, sel.decision, 'applied', SETTLE_MS)

    // From the ticket's Decisions: the selection's record row offers it.
    budget(VIEW_MS + 2 * LOAD_MS)
    await page.goto(c.path)
    await page.locator(`[data-ticket-decision-review="${sel.decision}"]`).click()
    await expect(page.locator(`[data-review-view][data-review-candidate="${bRef}"]`)).toBeVisible({ timeout: VIEW_MS })
    expect(new URL(page.url()).searchParams.get('candidate')).toBe(bRef)

    // From the comparison: the same.
    budget(VIEW_MS + 2 * LOAD_MS)
    await page.goto(`${c.path}?view=compare&run=${c.run}`)
    await page.locator('[data-compare-select] [data-compare-review-selected]').click()
    await expect(page.locator(`[data-review-view][data-review-candidate="${bRef}"]`)).toBeVisible({ timeout: VIEW_MS })

    // Opened with no candidate named, the view settles on the selected one; its picker says selected and not selected.
    budget(VIEW_MS + 6 * LOAD_MS)
    await page.goto(`${c.path}?view=review`)
    await expect(page.locator(`[data-review-view][data-review-candidate="${bRef}"]`)).toBeVisible({ timeout: VIEW_MS })
    await page.locator('button[data-review-candidate]').click()
    await expect(page.getByRole('option', { name: /^approach-b · attempt \d+ .* · result · selected$/ })).toHaveCount(1)
    await expect(page.getByRole('option', { name: /^approach-a · attempt \d+ .* · result · not selected$/ })).toHaveCount(1)
    await page.keyboard.press('Escape')
    await expect(page.getByRole('listbox')).toHaveCount(0)

    // approach-a, not selected: said so before a word is written, wide and on a phone, in both themes; its Request changes held.
    for (const size of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
      await page.setViewportSize(size)
      for (const theme of ['dark', 'light'] as const) {
        // This pass's caps: the page and its theme's reload, the view's wait, eight expects and measures.
        budget(VIEW_MS + 10 * LOAD_MS)
        await page.goto(`${c.path}?view=review&candidate=${encodeURIComponent(aRef)}`)
        await useTheme(page, theme, '[data-review-draft]')
        const at = `${size.width} wide, ${theme}`
        await expect(page.locator('[data-review-draft]'), at).toHaveAttribute('data-review-draft-notes', '0')
        await expect(page.locator('[data-review-hold]')).toContainText('approach-a was not selected', { timeout: VIEW_MS })
        await expect(page.locator('[data-review-hold-words]')).toHaveText(`Approach-a was not selected in ${sel.decision}: launch a new comparison to try it again.`)
        await expect(page.getByRole('radio', { name: /^Request changes/ })).toBeDisabled()
        // Comment and Approve stay.
        await expect(page.getByRole('radio', { name: /^Comment/ })).toBeEnabled()
        const hold = (await page.locator('[data-review-hold]').boundingBox())!
        const verdict = (await page.locator('[data-review-verdict]').boundingBox())!
        expect(hold.y, `the reason above the verdict, ${at}`).toBeLessThan(verdict.y)
        // The draft, where the reason is said (the diff's editor scrolls inside itself by design).
        await page.locator('[data-review-hold]').scrollIntoViewIfNeeded()
        await noSideways(page, { root: page.locator('[data-review-draft]') })
      }
    }
    await page.setViewportSize({ width: 1440, height: 900 })
    // A draft that asks for changes is not sent: Send review is held, and nothing reaches Dispatch.
    budget(VIEW_MS + 6 * LOAD_MS)
    await draft(page, c.ticket, aRef, base, 'Handle the empty locale.')
    await page.goto(`${c.path}?view=review&candidate=${encodeURIComponent(aRef)}`)
    await expect(page.locator(`[data-review-view][data-review-candidate="${aRef}"]`)).toHaveAttribute('data-review-diff-base', base, { timeout: VIEW_MS })
    await expect(page.locator('[data-review-draft]')).toHaveAttribute('data-review-draft-notes', '1')
    await expect(page.locator('[data-review-draft-problems]')).toContainText(`Request changes is not sent: approach-a was not selected in ${sel.decision}`)
    await expect(page.locator('[data-review-send]')).toBeDisabled()
    expect(posted).toEqual([])
    expect((await s.api.ok<{ reviews: unknown[] }>('GET', `/api/dispatch/tickets/${c.ticket}/reviews`)).reviews).toEqual([])
    // The server's refusal stays the last guard: a request for changes of approach-a sent past the page is refused in the same words.
    const past = await s.api.call<{ error: { code: string; message: string } }>('POST', `/api/dispatch/tickets/${c.ticket}/reviews`, {
      opId: 'crev-past-the-page',
      candidate: aRef,
      base,
      verdict: 'request_changes',
      via: 'web',
      findings: [{ kind: 'general', intent: 'change', severity: 'major', message: 'Handle the empty locale.' }],
    })
    expect([past.status, past.body.error]).toEqual([409, { code: 'not_selected', message: `approach-a was not selected in ${sel.decision}: launch a new comparison to try it again` }])
    expect((await s.api.ok<{ reviews: unknown[] }>('GET', `/api/dispatch/tickets/${c.ticket}/reviews`)).reviews).toEqual([])
    await page.evaluate((k) => localStorage.removeItem(k), `conductor.reviewDraft:${c.ticket}:${aRef}`)

    // Request changes of approach-b: the repair is made; its new candidate is in the picker, and the comparison still says approach-b is
    // the selected lane.
    budget(VIEW_MS + 2 * SETTLE_MS + 6 * LOAD_MS)
    await draft(page, c.ticket, bRef, base, 'Name the memo after the handler.')
    await page.goto(`${c.path}?view=review&candidate=${encodeURIComponent(bRef)}`)
    await expect(page.locator(`[data-review-view][data-review-candidate="${bRef}"]`)).toHaveAttribute('data-review-diff-base', base, { timeout: VIEW_MS })
    await page.locator('[data-review-send]').click()
    await page.locator('[data-review-confirm-send]').click()
    await expect(page.locator('[data-review-sent]')).toHaveAttribute('data-review-application', 'applied', { timeout: SETTLE_MS })
    await page.locator('button[data-review-candidate]').click()
    await expect(page.getByRole('option', { name: new RegExp(`^approach-b · attempt ${b.n + 1} `) })).toHaveCount(1, { timeout: SETTLE_MS })
    await page.keyboard.press('Escape')
    await expect(page.getByRole('listbox')).toHaveCount(0)
    // Opened with no candidate named, the view now settles on the selected candidate's latest attempt: the repair.
    budget(VIEW_MS + 5 * LOAD_MS)
    await page.goto(`${c.path}?view=review`)
    await expect(page.locator('[data-review-view]')).toHaveAttribute('data-review-candidate', new RegExp(`/approach-b/${b.n + 1}@[0-9a-f]{40}$`), { timeout: VIEW_MS })
    await expect(page.locator('[data-review-hold]')).toHaveCount(0)
    await page.goto(`${c.path}?view=compare&run=${c.run}`)
    await expect(page.locator('[data-compare-candidate="approach-b"] [data-compare-selection]')).toHaveText('selected')
    // The comparison keeps showing the commit the selection named; the repair's candidates are the Review view's.
    await expect(page.locator('[data-compare-candidate="approach-b"] [data-compare-candidate-commit]')).toHaveAttribute('data-commit', b.commit)
    await page.context().close()
  } finally {
    await s.stop()
  }
})
