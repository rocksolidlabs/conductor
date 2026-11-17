import type { Page } from '@playwright/test'
import { budget, dayPage, expect, test } from './fixtures'
import { SETTLE_MS, checkScript, comparison, launchComparison, selectionUntil } from './compare-select-helpers'
import { decisionUntil } from './decisions-helpers'
import { ownServer } from './own-server'

// Review the selected candidate (Program item CP1, docs/plans/compare-crew.md, PR-8b), on a server of the spec's own: two stub
// candidates of one base. Before the selection a Request changes is refused ("select a candidate first"); then approach-b is selected
// with a rationale through DR1's route. From the ticket's Decisions and from the comparison, RV1's Review view opens on approach-b; its
// picker says selected and not selected, and opens on the selected one by itself. A Request changes of approach-a is refused in
// not_selected's words, nothing kept; one of approach-b makes the repair, whose new candidate the picker shows while the comparison
// still labels approach-b selected. Every wait is on the server's reads or on what the page draws.

const LOAD_MS = 15_000

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

    // Before the selection, a Request changes of either candidate is refused, nothing kept: a repair would make them unlike.
    budget(2 * LOAD_MS)
    await draft(page, c.ticket, bRef, base, 'Before the selection.')
    await page.goto(`${c.path}?view=review&candidate=${encodeURIComponent(bRef)}`)
    await expect(page.locator(`[data-review-view][data-review-candidate="${bRef}"]`)).toHaveAttribute('data-review-diff-base', base, { timeout: 60_000 })
    await page.locator('[data-review-send]').click()
    await page.locator('[data-review-confirm-send]').click()
    await expect(page.locator('[data-review-draft-failure]')).toContainText('select a candidate first', { timeout: 60_000 })
    expect((await s.api.ok<{ reviews: unknown[] }>('GET', `/api/dispatch/tickets/${c.ticket}/reviews`)).reviews).toEqual([])
    await page.evaluate((k) => localStorage.removeItem(k), `conductor.reviewDraft:${c.ticket}:${bRef}`)

    const rev = (await s.api.ok<{ decision: { rev: number } }>('GET', `/api/decisions/${sel.decision}`)).decision.rev
    await s.api.ok('POST', `/api/decisions/${sel.decision}/respond`, { responseId: 'crev-select-b', choice: 1, rationale: 'approach-b keeps the handler simple', expectedRev: rev })
    await decisionUntil(s.api, sel.decision, 'applied', SETTLE_MS)

    // From the ticket's Decisions: the selection's record row offers it.
    budget(LOAD_MS)
    await page.goto(c.path)
    await page.locator(`[data-ticket-decision-review="${sel.decision}"]`).click()
    await expect(page.locator(`[data-review-view][data-review-candidate="${bRef}"]`)).toBeVisible({ timeout: 60_000 })
    expect(new URL(page.url()).searchParams.get('candidate')).toBe(bRef)

    // From the comparison: the same.
    budget(LOAD_MS)
    await page.goto(`${c.path}?view=compare&run=${c.run}`)
    await page.locator('[data-compare-select] [data-compare-review-selected]').click()
    await expect(page.locator(`[data-review-view][data-review-candidate="${bRef}"]`)).toBeVisible({ timeout: 60_000 })

    // Opened with no candidate named, the view settles on the selected one; its picker says selected and not selected.
    budget(LOAD_MS)
    await page.goto(`${c.path}?view=review`)
    await expect(page.locator(`[data-review-view][data-review-candidate="${bRef}"]`)).toBeVisible({ timeout: 60_000 })
    await page.locator('button[data-review-candidate]').click()
    await expect(page.getByRole('option', { name: /^approach-b · attempt \d+ .* · result · selected$/ })).toHaveCount(1)
    await expect(page.getByRole('option', { name: /^approach-a · attempt \d+ .* · result · not selected$/ })).toHaveCount(1)
    await page.keyboard.press('Escape')
    await expect(page.getByRole('listbox')).toHaveCount(0)

    // Request changes of approach-a: refused in not_selected's words, nothing kept in Dispatch.
    budget(2 * LOAD_MS)
    await draft(page, c.ticket, aRef, base, 'Handle the empty locale.')
    await page.goto(`${c.path}?view=review&candidate=${encodeURIComponent(aRef)}`)
    await expect(page.locator(`[data-review-view][data-review-candidate="${aRef}"]`)).toHaveAttribute('data-review-diff-base', base, { timeout: 60_000 })
    await expect(page.locator('[data-review-draft]')).toHaveAttribute('data-review-draft-notes', '1')
    await page.locator('[data-review-send]').click()
    await page.locator('[data-review-confirm-send]').click()
    await expect(page.locator('[data-review-draft-failure]')).toContainText(`approach-a was not selected in ${sel.decision}`, { timeout: 60_000 })
    expect((await s.api.ok<{ reviews: unknown[] }>('GET', `/api/dispatch/tickets/${c.ticket}/reviews`)).reviews).toEqual([])

    // Request changes of approach-b: the repair is made; its new candidate is in the picker, and the comparison still says approach-b is
    // the selected lane.
    budget(2 * LOAD_MS + SETTLE_MS)
    await draft(page, c.ticket, bRef, base, 'Name the memo after the handler.')
    await page.goto(`${c.path}?view=review&candidate=${encodeURIComponent(bRef)}`)
    await expect(page.locator(`[data-review-view][data-review-candidate="${bRef}"]`)).toHaveAttribute('data-review-diff-base', base, { timeout: 60_000 })
    await page.locator('[data-review-send]').click()
    await page.locator('[data-review-confirm-send]').click()
    await expect(page.locator('[data-review-sent]')).toHaveAttribute('data-review-application', 'applied', { timeout: SETTLE_MS })
    await page.locator('button[data-review-candidate]').click()
    await expect(page.getByRole('option', { name: new RegExp(`^approach-b · attempt ${b.n + 1} `) })).toHaveCount(1, { timeout: SETTLE_MS })
    await page.keyboard.press('Escape')
    await expect(page.getByRole('listbox')).toHaveCount(0)
    await page.goto(`${c.path}?view=compare&run=${c.run}`)
    await expect(page.locator('[data-compare-candidate="approach-b"] [data-compare-selection]')).toHaveText('selected')
    await page.context().close()
  } finally {
    await s.stop()
  }
})
