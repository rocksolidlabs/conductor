import { expect, test, type Session } from './fixtures'

// One viewer sizes a session (round 14). The owner's own window holds the
// size; a coworker's laptop on a control link follows it, the session's grid
// scaled to the laptop's window, with "Sized by Nate · cols × rows" and Fit
// to my window. Neither window changing size moves the session; only the
// button does, which hands the size over (the other window then scales and
// offers it back). A view-only link scales with no button.
test("a control link follows the owner's size, scaled, until Fit to my window; the owner takes it back", async ({ page, api, browser }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.addInitScript(() => localStorage.setItem('conductor.displayName', 'Nate'))
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'sizing' })
  const size = async () => {
    const { cols, rows } = (await api.ok<{ session: { cols: number; rows: number } }>('GET', `/api/sessions/${s.id}`)).session
    return { cols, rows }
  }
  const contexts: Array<{ close: () => Promise<void> }> = []
  try {
    await page.goto(`/sessions/${s.id}`)
    await expect(page.locator('.xterm-screen')).toBeVisible({ timeout: 30_000 })
    // The owner's window sizes the session: attached (the session counts it as a viewer, which the attach that applies its hello's
    // size makes under one lock), the session at what the window fits (its terminal's drawn grid, data-cols and data-rows), and still
    // so a second later, past the refit the bundled font may bring (FIT_DEBOUNCE_MS and a frame). The session starts at 80 × 24, which
    // a read made before the window attaches sees: a threshold it meets, or two reads a moment apart, are no proof the window sized it.
    const owner = page.locator('.terminal-host')
    await page.evaluate(() => document.fonts.ready.then(() => undefined))
    let big = { cols: 0, rows: 0 }
    let seen = ''
    await expect
      .poll(
        async () => {
          const { session } = await api.ok<{ session: { cols: number; rows: number; viewers: number } }>('GET', `/api/sessions/${s.id}`)
          const grid = await owner.evaluate((h) => `${h.dataset.cols} × ${h.dataset.rows}`)
          const now = `${session.viewers} ${session.cols} × ${session.rows} ${grid}`
          const held = session.viewers > 0 && grid === `${session.cols} × ${session.rows}` && now === seen
          seen = now
          big = { cols: session.cols, rows: session.rows }
          return held
        },
        { timeout: 15_000, intervals: [1000], message: "the owner's window sizes the session" },
      )
      .toBe(true)
    await expect(page.locator('[data-terminal-sizer]')).toHaveCount(0)

    // A coworker's laptop joins with a control link: the size stays, scaled to the laptop.
    const { token } = await api.ok<{ token: string }>('POST', `/api/sessions/${s.id}/links`, { role: 'control' })
    const laptopCtx = await browser.newContext({ viewport: { width: 900, height: 600 } })
    contexts.push(laptopCtx)
    await laptopCtx.addInitScript(() => localStorage.setItem('conductor.displayName', 'Jane'))
    const laptop = await laptopCtx.newPage()
    await laptop.goto(`/join/${token}`)
    await laptop.getByRole('button', { name: 'Join session' }).click()
    const chip = laptop.locator('[data-terminal-sizer]')
    await expect(chip).toBeVisible({ timeout: 30_000 })
    await expect(chip.locator('[data-sizer-name]')).toHaveText('Nate')
    await expect(chip).toContainText(`${big.cols} × ${big.rows}`)
    await expect(laptop.locator('.terminal-host.terminal-scale')).toHaveCount(1)
    expect(await size()).toEqual(big)
    // The laptop's window changing size moves nothing.
    await laptop.setViewportSize({ width: 820, height: 560 })
    await laptop.waitForTimeout(1500)
    expect(await size()).toEqual(big)

    // Fit to my window: the laptop's size, and the owner's window now scales and says who sizes it.
    await chip.locator('[data-fit-mine]').click()
    await expect.poll(async () => (await size()).cols, { timeout: 15_000 }).toBeLessThan(big.cols)
    const small = await size()
    await expect(chip).toHaveCount(0)
    await expect(laptop.locator('.terminal-host.terminal-scale')).toHaveCount(0)
    const nateChip = page.locator('[data-terminal-sizer]')
    await expect(nateChip.locator('[data-sizer-name]')).toHaveText('Jane', { timeout: 15_000 })
    await expect(nateChip).toContainText(`${small.cols} × ${small.rows}`)
    await expect(page.locator('.terminal-host.terminal-scale')).toHaveCount(1)
    // The owner's window changing size moves nothing either.
    await page.setViewportSize({ width: 1360, height: 860 })
    await page.waitForTimeout(1500)
    expect(await size()).toEqual(small)

    // Taken back: the owner's window fills again, and the laptop scales with the button.
    await nateChip.locator('[data-fit-mine]').click()
    await expect.poll(async () => (await size()).cols, { timeout: 15_000 }).toBeGreaterThan(small.cols)
    await expect(nateChip).toHaveCount(0)
    await expect(page.locator('.terminal-host.terminal-scale')).toHaveCount(0)
    await expect(chip.locator('[data-sizer-name]')).toHaveText('Nate', { timeout: 15_000 })

    // A view-only link scales the session to its window with no button: it cannot take the size.
    const view = await api.ok<{ token: string }>('POST', `/api/sessions/${s.id}/links`, { role: 'view' })
    const phoneCtx = await browser.newContext({ viewport: { width: 600, height: 700 } })
    contexts.push(phoneCtx)
    await phoneCtx.addInitScript(() => localStorage.setItem('conductor.displayName', 'Priya'))
    const phone = await phoneCtx.newPage()
    await phone.goto(`/join/${view.token}`)
    await phone.getByRole('button', { name: 'Join session' }).click()
    await expect(phone.locator('.terminal-host.terminal-scale')).toHaveCount(1, { timeout: 30_000 })
    await expect(phone.locator('[data-terminal-sizer]')).toHaveCount(0)
  } finally {
    for (const c of contexts) await c.close()
    await api.stopSession(s.id)
  }
})
