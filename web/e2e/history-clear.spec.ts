import { Api, budget, expect, test, type Session, useTheme } from './fixtures'
import { MEASURE_WAIT_MS, noSideways, tapTarget } from './layout'
import { walk, worst } from './contrast'

// Clearing what narrows a History search (#1023): the words and each filter listed under the search box in the page's words, each
// with its own ×, and Clear all beside them; one removal leaves the others and changes the results, Clear all empties the box, the
// list, the chips' pressed state and the URL, and Back puts back what it took off. An answer with nothing in it offers the same Clear
// all. Tab reaches the removals and Clear all in order; the focus goes to the next removal, then to the box. A phone's controls are
// big enough to tap, and the list's text is over its floor in both themes. Every row is found by its key, never by a count.

const STEP_MS = 15_000 // the config's expect bound, and an action's
const INDEX_MS = 60_000 // a stopped session's record indexed (history-page.spec.ts's bound)
const search = (api: Api, q: string) => api.ok<{ entries: Array<{ key: string }> }>('GET', `/api/history?q=${encodeURIComponent(q)}&limit=100`).then((r) => r.entries)

test('the active filters are listed, each removed by its own ×, and Clear all takes them all off with the words', async ({ page, api }) => {
  const tag = `hc${Date.now().toString(36)}`
  const made: Session[] = []
  try {
    for (const a of ['claude', 'claude', 'codex']) made.push(await api.ok<Session>('POST', '/api/sessions', { agentId: a, name: `e2e history clear ${tag} ${a}` }))
    for (const s of made) await api.stopSession(s.id)
    budget(made.length * INDEX_MS)
    for (const s of made) await expect.poll(async () => (await search(api, `${tag} class:record`)).some((e) => e.key === `session:${s.id}`), { timeout: INDEX_MS, message: 'the record is indexed' }).toBe(true)
    const [claude1, claude2, codex] = made.map((s) => `session:${s.id}`) as [string, string, string]

    budget(2 * 30_000 + 40 * STEP_MS)
    await page.goto('/history')
    const pageEl = page.locator('[data-history-page]')
    const input = pageEl.locator('[data-history-input] input, input[data-history-input]').first()
    const row = (key: string) => pageEl.locator(`[data-history-row="${key}"]`)
    const chip = (v: string) => pageEl.locator(`[data-history-chip="${v}"]`)
    const bar = pageEl.locator('[data-history-active-bar]')
    const active = (key: string) => bar.locator(`[data-history-active="${key}"]`)
    const q = () => new URL(page.url()).searchParams.get('q')
    // The agents' chips are drawn once the catalog is read.
    await expect(chip('agent:claude')).toBeVisible({ timeout: 30_000 })
    await expect(bar).toHaveCount(0)

    // Three filters of three kinds and a word.
    await input.fill(tag)
    await expect(input).toHaveValue(tag)
    for (const [c, v] of [
      ['class:record', `${tag} class:record`],
      ['agent:claude', `${tag} class:record agent:claude`],
      ['since:7d', `${tag} class:record agent:claude since:7d`],
    ] as const) {
      await chip(c).click()
      await expect(input).toHaveValue(v)
    }
    await expect.poll(q).toBe(`${tag} class:record agent:claude since:7d`)
    await expect(row(claude1)).toBeVisible({ timeout: 30_000 })
    await expect(row(claude2)).toBeVisible()
    await expect(row(codex)).toHaveCount(0)

    // Each listed in the page's words, the words first.
    await expect(bar.locator('[data-history-active]')).toHaveCount(4)
    await expect(active('words').locator('[data-history-active-words]')).toHaveText(`Words: ${tag}`)
    await expect(active('class:record').locator('[data-history-active-words]')).toHaveText('Kind of evidence: Records')
    await expect(active('agent:claude').locator('[data-history-active-words]')).toHaveText('Agent: Claude Code (e2e stub)')
    await expect(active('since:7d').locator('[data-history-active-words]')).toHaveText('Since: A week')

    // Tab from the box reaches each removal, then Clear all, in order; each removal is named for what it takes off.
    await input.focus()
    const order = ['words', 'class:record', 'agent:claude', 'since:7d']
    for (const k of order) {
      await page.keyboard.press('Tab')
      await expect(active(k).locator('[data-history-active-remove]')).toBeFocused()
    }
    await page.keyboard.press('Tab')
    await expect(bar.locator('[data-history-clear-all]')).toBeFocused()
    await expect(active('agent:claude').locator('[data-history-active-remove]')).toHaveAccessibleName('Remove the filter Agent: Claude Code (e2e stub)')
    await expect(active('words').locator('[data-history-active-remove]')).toHaveAccessibleName(`Remove the words ${tag}`)

    // One removed by its own ×: the others stay, the box, the URL, the aside's chip and the results follow, the focus on the next ×.
    await page.getByRole('button', { name: 'Remove the filter Agent: Claude Code (e2e stub)' }).click()
    await expect(input).toHaveValue(`${tag} class:record since:7d`)
    await expect.poll(q).toBe(`${tag} class:record since:7d`)
    await expect(active('agent:claude')).toHaveCount(0)
    for (const k of ['words', 'class:record', 'since:7d']) await expect(active(k)).toBeVisible()
    await expect(chip('agent:claude')).not.toHaveAttribute('data-history-chip-on', '')
    await expect(chip('class:record')).toHaveAttribute('data-history-chip-on', '')
    await expect(row(codex)).toBeVisible({ timeout: 30_000 })
    await expect(row(claude1)).toBeVisible()
    await expect(active('since:7d').locator('[data-history-active-remove]')).toBeFocused()

    // Clear all: the words and every filter off; the box, the URL, the list, the aside's chips and the results all reset; the focus in the box.
    await bar.locator('[data-history-clear-all]').click()
    await expect(input).toHaveValue('')
    await expect.poll(q).toBe(null)
    await expect(bar).toHaveCount(0)
    await expect(pageEl.locator('[data-history-chip-on]')).toHaveCount(0)
    await expect(pageEl.locator('[data-history-questions]')).toBeVisible()
    await expect(pageEl.locator('[data-history-total]')).toHaveText('The latest sessions', { timeout: 30_000 })
    await expect(input).toBeFocused()

    // Back puts back what Clear all took off; Forward takes it off again.
    await page.goBack()
    await expect(input).toHaveValue(`${tag} class:record since:7d`)
    await expect(active('since:7d')).toBeVisible()
    await expect(row(codex)).toBeVisible({ timeout: 30_000 })
    await page.goForward()
    await expect(input).toHaveValue('')
    await expect(bar).toHaveCount(0)

    // Nothing matches: the empty answer says the filters are why and offers the same Clear all, never a dead end.
    await input.fill(`${tag} class:said agent:claude`)
    const none = pageEl.locator('[data-history-none]')
    await expect(none).toBeVisible({ timeout: 30_000 })
    await expect(none.locator('[data-page-why]')).toHaveText('Nothing matches these filters.')
    await none.locator('[data-page-action]', { hasText: 'Clear all' }).click()
    await expect(input).toHaveValue('')
    await expect.poll(q).toBe(null)
    await expect(none).toHaveCount(0)
    await expect(pageEl.locator('[data-history-total]')).toHaveText('The latest sessions', { timeout: 30_000 })
    await expect(input).toBeFocused()

    // The list's text over its floor in both themes, and a phone's controls big enough to tap, nothing wider than the screen.
    budget(2 * 30_000 + 2 * MEASURE_WAIT_MS + 6 * STEP_MS)
    await page.goto(`/history?q=${encodeURIComponent(`${tag} class:record agent:claude since:7d`)}`)
    for (const theme of ['light', 'dark'] as const) {
      await useTheme(page, theme, '[data-history-active-bar]')
      await expect(bar.locator('[data-history-active]')).toHaveCount(4)
      const { judged, failures } = await walk(page, bar)
      expect(judged, `${theme}: the walk judged some text`).toBeGreaterThan(0)
      expect(failures, `${theme}: the active filters' text under its floor:\n${worst(failures)}`).toEqual([])
    }
    await page.setViewportSize({ width: 390, height: 844 })
    await expect(row(claude1)).toBeVisible({ timeout: 30_000 })
    await tapTarget(bar.locator('[data-history-active-remove]'))
    await tapTarget(bar.locator('[data-history-clear-all]'))
    // The list's one line scrolls sideways by design (history-rows.spec.ts); nothing else does.
    await noSideways(page, { root: pageEl, allow: bar.locator('[data-history-active-strip]') })
    // On a phone the list sits under the box, the results under it (the chips wait behind Filters).
    const [box, list, results] = [await pageEl.locator('[data-history-search] input').boundingBox(), await bar.boundingBox(), await row(claude1).boundingBox()]
    expect(list!.y).toBeGreaterThan(box!.y)
    expect(results!.y).toBeGreaterThan(list!.y)
  } finally {
    for (const s of made) await api.stopSession(s.id).catch(() => undefined)
  }
})
