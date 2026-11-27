import type { Locator, Page } from '@playwright/test'
import { DAILY, atRest, budget, expect, openMore, test, useTheme, type Api } from './fixtures'
import { noSideways, probeColour } from './layout'

// New agent trial, the form on Crews (CP1, docs/plans/compare-crew.md, "The template", where it is called New comparison): it opens on
// its task with the cursor in it, two agents picked, the Check line and the folder, Yolo beside its one filled button, nothing marked;
// a press with nothing typed marks each field in its words and puts the cursor in the first; Ctrl with Enter presses it; the body it
// posts is read on its way to the server's template route; the draft lives across Escape and the X and ends with Cancel.

const DESK = { width: 1440, height: 900 }
const PHONE = { width: 390, height: 844 }
/** An expect's own bound (playwright.config.ts): what each step's budget counts per expect. */
const EXPECT_MS = 15_000

/** The crews a test saved, deleted after it. */
const made: string[] = []
test.afterEach(async ({ api }) => {
  for (const id of made.splice(0)) await api.call('DELETE', `/api/crews/${encodeURIComponent(id)}`)
})

/** Opens New agent trial on Crews and returns the dialog and its form, the dialog's opening animation ended. */
async function openForm(page: Page): Promise<{ dialog: Locator; form: Locator }> {
  await page.locator('[data-new-comparison]').click()
  const form = page.locator('[data-compare-template]')
  const dialog = page.getByRole('dialog').filter({ has: form })
  await expect(form).toBeVisible()
  await atRest(dialog)
  return { dialog, form }
}

/** The server's Yolo default (GET /api/catalog), which the switch shows until it is moved. */
async function yoloDefault(api: Api): Promise<boolean> {
  return (await api.ok<{ yoloDefault: boolean }>('GET', '/api/catalog')).yoloDefault
}

/** What the form posts to the template route, in order. */
function posted(page: Page): Array<Record<string, unknown>> {
  const seen: Array<Record<string, unknown>> = []
  page.on('request', (r) => {
    if (r.method() === 'POST' && new URL(r.url()).pathname === '/api/crews/templates/compare') seen.push(r.postDataJSON() as Record<string, unknown>)
  })
  return seen
}

test('New agent trial opens on its task with nothing marked, two agents picked, Yolo as the server says and its button in view, at every size in both themes', async ({ page, api }) => {
  const yolo = await yoloDefault(api)
  for (const size of [DAILY, DESK, PHONE]) {
    await page.setViewportSize(size)
    await page.goto('/crews')
    for (const theme of ['dark', 'light'] as const) {
      // This pass's caps: the theme's reload, the dialog's opening, about fifteen expects and measures, its closing.
      budget(18 * EXPECT_MS)
      const at = `${size.width} x ${size.height}, ${theme}`
      await useTheme(page, theme, '[data-new-comparison]')
      const { dialog, form } = await openForm(page)
      await expect(dialog.getByRole('heading', { name: 'New agent trial' })).toBeVisible()
      await expect(dialog).toContainText('Two to four agents each try the same task on their own copy. You keep the best result.')
      await expect(form.locator('[data-compare-goal] textarea'), at).toBeFocused()
      await expect(form.locator('[data-compare-goal] textarea')).toHaveAttribute('placeholder', 'What should each agent do?')
      await expect(form.locator('[data-trial-agent]')).toHaveCount(2)
      await expect(form.locator('[data-compare-check] input')).toHaveValue('')
      await expect(form.locator('[data-compare-check] input')).not.toHaveAttribute('placeholder', /./)
      // Nothing is marked before a press.
      await expect(dialog.locator('[aria-invalid="true"]'), at).toHaveCount(0)
      // The one filled button and Yolo beside it, in view as the form opens.
      const save = dialog.locator('[data-compare-create]')
      await expect(save).toHaveText('Save trial')
      await expect(save, at).toBeInViewport({ ratio: 1 })
      const yoloSwitch = dialog.getByRole('switch', { name: /^Yolo/ })
      await expect(yoloSwitch, at).toBeInViewport({ ratio: 1 })
      await expect(yoloSwitch).toHaveAttribute('aria-checked', String(yolo))
      await expect(dialog.locator('[data-compare-yolo]')).toContainText('Yolo skips permission prompts')
      // On, it is the switch's own colour, never the warning's; its words do not change.
      if (!yolo) await yoloSwitch.click()
      await expect(yoloSwitch).toHaveAttribute('aria-checked', 'true')
      await atRest(yoloSwitch)
      const fill = await probeColour(page, await yoloSwitch.evaluate((n) => getComputedStyle(n).backgroundColor))
      expect(fill, at).not.toBe(await probeColour(page, 'var(--ui-warning)'))
      await expect(dialog.locator('[data-compare-yolo]')).toContainText('Yolo skips permission prompts')
      // The title and the description stay clear of the close button.
      const close = (await dialog.locator('[data-slot="close"]').boundingBox())!
      for (const part of ['title', 'description']) {
        const text = (await dialog.locator(`[data-slot="${part}"]`).boundingBox())!
        const clear = text.x + text.width <= close.x || text.y >= close.y + close.height
        expect(clear, `the ${part} ends at ${text.x + text.width}, the close button starts at ${close.x} (${at})`).toBe(true)
      }
      await noSideways(page)
      // Cancel ends the draft: the next pass opens a new one.
      await dialog.locator('[data-compare-cancel]').click()
      await expect(dialog).toHaveCount(0)
    }
  }
})

test('a press with nothing typed marks the task, the check and the folder and puts the cursor in the task; Ctrl with Enter then saves the trial as typed', async ({ page, api, state }) => {
  test.setTimeout(120_000)
  const yolo = await yoloDefault(api)
  const seen = posted(page)
  await page.setViewportSize(DAILY)
  await page.goto('/crews')
  const { dialog, form } = await openForm(page)
  await dialog.locator('[data-compare-create]').click()
  await expect(form.locator('[data-compare-goal] textarea')).toBeFocused()
  await expect(dialog.locator('[aria-invalid="true"]')).toHaveCount(3)
  await expect(form.locator('[data-compare-goal] textarea')).toHaveAttribute('aria-invalid', 'true')
  await expect(form.locator('[data-compare-check] input')).toHaveAttribute('aria-invalid', 'true')
  await expect(form).toContainText('Say what each agent should do.')
  await expect(form).toContainText('Give a command that tests each result, such as npm test.')
  await expect(form).toContainText('Pick the folder.')
  expect(seen).toHaveLength(0)

  // Each mark goes as its field is put right.
  await form.locator('[data-compare-goal] textarea').fill('Add rate limiting to the login route')
  await expect(form).not.toContainText('Say what each agent should do.')
  await form.locator('[data-compare-check] input').fill('true')
  const folder = form.locator('[data-compare-cwd]').getByRole('combobox')
  await folder.fill(state.repo)
  await folder.press('Tab')
  await expect(folder).toHaveValue(state.repo)
  await expect(dialog.locator('[aria-invalid="true"]')).toHaveCount(0)
  // Yolo moved from the server's default goes as the switch shows it.
  await dialog.getByRole('switch', { name: /^Yolo/ }).click()
  const agents = await form.locator('[data-trial-agent]').evaluateAll((els) => els.map((e) => e.getAttribute('data-agent-id')))

  await form.locator('[data-compare-goal] textarea').focus()
  await page.keyboard.press('Control+Enter')
  await expect(dialog).toHaveCount(0, { timeout: 30_000 })
  await expect(page).toHaveURL(/\/crews\/(?!new)[^/?]+/, { timeout: 30_000 })
  made.push(decodeURIComponent(new URL(page.url()).pathname.split('/')[2]!))
  expect(seen).toEqual([
    {
      name: 'Add rate limiting to the login route',
      cwd: state.repo,
      goal: 'Add rate limiting to the login route',
      instructions: '',
      criteria: [{ id: 'does-what-the-task-asks', text: 'Does what the task asks' }],
      checks: [{ id: 'check', argv: ['true'] }],
      candidates: agents.map((agentId) => ({ agentId })),
      yolo: !yolo,
    },
  ])
  const crew = await api.crew(made[0]!)
  expect(crew.tasks?.map((t) => t.id)).toEqual(['approach-a', 'approach-b'])
})

test("the draft lives across Escape and the X and ends with Cancel; an agent's arguments show on its chip", async ({ page }) => {
  test.setTimeout(120_000)
  await page.setViewportSize(DESK)
  await page.goto('/crews')
  let { dialog, form } = await openForm(page)
  await form.locator('[data-compare-goal] textarea').fill('kept across a close')
  const more = await openMore(form)
  await more.locator('[data-compare-agent-args="1"] input').fill('--model e2e')
  await expect(form.locator('[data-trial-agent="1"] [data-trial-agent-args]')).toHaveText('--model e2e')
  await expect(form.locator('[data-trial-agent="0"] [data-trial-agent-args]')).toHaveCount(0)

  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)
  ;({ dialog, form } = await openForm(page))
  await expect(form.locator('[data-compare-goal] textarea')).toHaveValue('kept across a close')
  await expect(form.locator('[data-trial-agent="1"] [data-trial-agent-args]')).toHaveText('--model e2e')

  await dialog.locator('[data-slot="close"]').click()
  await expect(dialog).toHaveCount(0)
  ;({ dialog, form } = await openForm(page))
  await expect(form.locator('[data-compare-goal] textarea')).toHaveValue('kept across a close')

  await dialog.locator('[data-compare-cancel]').click()
  await expect(dialog).toHaveCount(0)
  ;({ dialog, form } = await openForm(page))
  await expect(form.locator('[data-compare-goal] textarea')).toHaveValue('')
  await expect(form.locator('[data-trial-agent-args]')).toHaveCount(0)
})
