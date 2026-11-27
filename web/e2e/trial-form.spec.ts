import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import type { Locator, Page } from '@playwright/test'
import { DAILY, atRest, budget, expect, git, openMore, standingRoute, test, useTheme, type Api } from './fixtures'
import { noSideways, probeColour } from './layout'

// New agent trial, the form on Crews (CP1, docs/plans/compare-crew.md, "The template", where it is called New comparison): it opens on
// its task with the cursor in it, two agents picked, the Check line and the folder, Yolo beside its one filled button, nothing marked;
// a press with nothing typed marks each field in its words and puts the cursor in the first; Ctrl with Enter presses it; the body it
// posts is read on its way to the server's template route; the draft lives across Escape and the X and ends with Cancel; More options
// opens downward in five groups; a folder agents may not use is marked as it is picked, a new project's folder says nothing; a press
// holds the form until its answer, and a refusal keeps what was typed.

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
  // The catalog read: the agents are on the form (a form opened before it was read takes them when it comes).
  await expect(form.locator('[data-trial-agent]').first()).toBeVisible()
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
      await expect(form.locator('[data-compare-goal]'), at).toBeFocused()
      await expect(form.locator('[data-compare-goal]')).toHaveAttribute('placeholder', 'What should each agent do?')
      await expect(form.locator('[data-trial-agent]')).toHaveCount(2)
      await expect(form.locator('[data-compare-check]')).toHaveValue('')
      await expect(form.locator('[data-compare-check]')).not.toHaveAttribute('placeholder', /./)
      // Nothing is marked before a press.
      await expect(dialog.locator('[aria-invalid="true"]'), at).toHaveCount(0)
      // The one filled button and Yolo beside it, in view as the form opens.
      const save = dialog.locator('[data-compare-create]')
      await expect(save).toHaveText('Save trial')
      await expect(save, at).toBeInViewport({ ratio: 1 })
      const yoloSwitch = dialog.getByRole('switch', { name: /^Yolo/ })
      await expect(yoloSwitch, at).toBeInViewport({ ratio: 1 })
      await expect(yoloSwitch).toHaveAttribute('aria-checked', String(yolo))
      await expect(dialog.getByRole('switch', { name: 'Yolo skips permission prompts', exact: true })).toBeVisible()
      // On, it is the switch's own colour, never the warning's; its words do not change.
      if (!yolo) await yoloSwitch.click()
      await expect(yoloSwitch).toHaveAttribute('aria-checked', 'true')
      await atRest(yoloSwitch)
      const fill = await probeColour(page, await yoloSwitch.evaluate((n) => getComputedStyle(n).backgroundColor))
      expect(fill, at).not.toBe(await probeColour(page, 'var(--ui-warning)'))
      await expect(dialog.getByRole('switch', { name: 'Yolo skips permission prompts', exact: true })).toBeVisible()
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
  await expect(form.locator('[data-compare-goal]')).toBeFocused()
  await expect(dialog.locator('[aria-invalid="true"]')).toHaveCount(3)
  await expect(form.locator('[data-compare-goal]')).toHaveAttribute('aria-invalid', 'true')
  await expect(form.locator('[data-compare-check]')).toHaveAttribute('aria-invalid', 'true')
  await expect(form).toContainText('Say what each agent should do.')
  await expect(form).toContainText('Give a command that tests each result, such as npm test.')
  await expect(form).toContainText('Pick the folder.')
  expect(seen).toHaveLength(0)

  // Each mark goes as its field is put right.
  await form.locator('[data-compare-goal]').fill('Add rate limiting to the login route')
  await expect(form).not.toContainText('Say what each agent should do.')
  await form.locator('[data-compare-check]').fill('true')
  const folder = form.locator('[data-compare-cwd]').getByRole('combobox')
  await folder.fill(state.repo)
  await folder.press('Tab')
  await expect(folder).toHaveValue(state.repo)
  await expect(dialog.locator('[aria-invalid="true"]')).toHaveCount(0)
  // Yolo moved from the server's default goes as the switch shows it.
  await dialog.getByRole('switch', { name: /^Yolo/ }).click()
  const agents = await form.locator('[data-trial-agent]').evaluateAll((els) => els.map((e) => e.getAttribute('data-agent-id')))

  await form.locator('[data-compare-goal]').focus()
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
  await form.locator('[data-compare-goal]').fill('kept across a close')
  const more = await openMore(form)
  await more.locator('[data-compare-agent-args="1"]').fill('--model e2e')
  await expect(form.locator('[data-trial-agent="1"] [data-trial-agent-args]')).toHaveText('--model e2e')
  await expect(form.locator('[data-trial-agent="0"] [data-trial-agent-args]')).toHaveCount(0)

  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)
  ;({ dialog, form } = await openForm(page))
  await expect(form.locator('[data-compare-goal]')).toHaveValue('kept across a close')
  await expect(form.locator('[data-trial-agent="1"] [data-trial-agent-args]')).toHaveText('--model e2e')

  await dialog.locator('[data-slot="close"]').click()
  await expect(dialog).toHaveCount(0)
  ;({ dialog, form } = await openForm(page))
  await expect(form.locator('[data-compare-goal]')).toHaveValue('kept across a close')

  await dialog.locator('[data-compare-cancel]').click()
  await expect(dialog).toHaveCount(0)
  ;({ dialog, form } = await openForm(page))
  await expect(form.locator('[data-compare-goal]')).toHaveValue('')
  await expect(form.locator('[data-trial-agent-args]')).toHaveCount(0)
})

/** Whether an input's placeholder fits its box whole, measured in the placeholder's own font. */
async function placeholderFits(input: Locator): Promise<{ text: number; room: number }> {
  return input.evaluate((el: HTMLInputElement) => {
    const s = getComputedStyle(el)
    const ph = getComputedStyle(el, '::placeholder')
    const ctx = document.createElement('canvas').getContext('2d')!
    ctx.font = `${ph.fontStyle} ${ph.fontWeight} ${ph.fontSize} ${ph.fontFamily}`
    const room = el.clientWidth - parseFloat(s.paddingLeft) - parseFloat(s.paddingRight)
    return { text: ctx.measureText(el.placeholder).width, room }
  })
}

test('More options opens downward in five groups, the button staying where it was and the footer in view; its closed row names what was changed', async ({ page }) => {
  test.setTimeout(120_000)
  for (const size of [DAILY, DESK]) {
    // This pass's caps: the page, the dialog's opening, the fold's, about twenty expects and measures.
    budget(24 * EXPECT_MS)
    const at = `${size.width} x ${size.height}`
    await page.setViewportSize(size)
    await page.goto('/crews')
    const { dialog, form } = await openForm(page)
    const button = form.locator('[data-more-options]')
    await expect(button).toHaveAttribute('aria-expanded', 'false')
    await expect(form.locator('[data-more-line]')).toHaveCount(0)
    const before = (await button.boundingBox())!
    const more = await openMore(form)
    await atRest(dialog)
    const after = (await button.boundingBox())!
    expect(Math.abs(after.y - before.y), `More options moved from ${before.y} to ${after.y} (${at})`).toBeLessThanOrEqual(1)
    expect(Math.abs(after.x - before.x), at).toBeLessThanOrEqual(1)
    await expect(dialog.locator('[data-compare-create]'), at).toBeInViewport({ ratio: 1 })
    await expect(more.locator('h3')).toHaveText(['Each agent', 'Every agent', 'Checks', 'When they are done', 'Filed as'])
    // One row an agent, its approach and its arguments, each placeholder whole.
    await expect(more.locator('[data-compare-agent-row]')).toHaveCount(2)
    for (const sel of ['[data-compare-agent-args="0"]', '[data-compare-agent-approach="0"]']) {
      const input = more.locator(sel)
      await input.scrollIntoViewIfNeeded()
      const fit = await placeholderFits(input)
      expect(fit.text, `${sel}'s placeholder is ${fit.text} px in ${fit.room} px (${at})`).toBeLessThanOrEqual(fit.room)
    }
    await expect(more.locator('[data-compare-agent-args="0"]')).toHaveAttribute('placeholder', 'Type -- to see its flags')
    await expect(more.locator('[data-compare-agent-approach="0"]')).toHaveAttribute('placeholder', 'Optional: a way to try it')
    await expect(more.locator('[data-compare-criterion="0"]')).toHaveValue('Does what the task asks')
    await expect(more.locator('[data-compare-instructions]')).toHaveAttribute('placeholder', 'Extra instructions, the same for each')
    await expect(more.locator('[data-compare-name]')).toHaveAttribute('placeholder', "The task's first words")
    await expect(more.getByRole('button', { name: 'Add a check' })).toBeVisible()
    // The review's agent comes with its switch, the first installed one picked.
    await expect(more.locator('[data-compare-review-agent]')).toHaveCount(0)
    await more.getByRole('switch', { name: 'Review' }).click()
    await expect(more.locator('[data-compare-review-agent]')).toBeVisible()
    await expect(more.locator('[data-compare-review-agent]')).not.toHaveText('')
    // Changed, then closed: the row names it.
    await more.locator('[data-compare-agent-args="1"]').fill('--model e2e')
    await more.locator('[data-compare-name]').fill('Login rate limit')
    await button.click()
    await expect(button).toHaveAttribute('aria-expanded', 'false')
    await expect(form.locator('[data-more-line]')).toHaveText('arguments · reviewed · named')
    await dialog.locator('[data-compare-cancel]').click()
    await expect(dialog).toHaveCount(0)
  }
})

test('a press whose first problem is under More options opens it and puts the cursor there', async ({ page, state }) => {
  test.setTimeout(120_000)
  await page.setViewportSize(DAILY)
  await page.goto('/crews')
  const { dialog, form } = await openForm(page)
  await form.locator('[data-compare-goal]').fill('Add rate limiting to the login route')
  await form.locator('[data-compare-check]').fill('true')
  const folder = form.locator('[data-compare-cwd]').getByRole('combobox')
  await folder.fill(state.repo)
  await folder.press('Tab')
  const more = await openMore(form)
  await more.locator('[data-compare-agent-args="0"]').fill('--model "opus')
  await form.locator('[data-more-options]').click()
  await expect(form.locator('[data-more-options]')).toHaveAttribute('aria-expanded', 'false')
  await dialog.locator('[data-compare-create]').click()
  await expect(form.locator('[data-more-options]')).toHaveAttribute('aria-expanded', 'true')
  await expect(more.locator('[data-compare-agent-args="0"]')).toBeFocused()
  await expect(more).toContainText('Agent A: close the quote in its arguments.')
  await dialog.locator('[data-compare-cancel]').click()
})

test("a folder agents may not use is marked as it is picked and holds the button; a new project's folder says nothing", async ({ page, state }) => {
  test.setTimeout(120_000)
  // A folder outside the server's allowed root, and a new project inside it: a repository with no commit yet.
  const outside = mkdtempSync(join(tmpdir(), 'e2e-trial-outside-'))
  const fresh = mkdtempSync(join(state.root, 'e2e-trial-new-'))
  git(fresh, 'init', '--quiet')
  try {
    await page.setViewportSize(DAILY)
    await page.goto('/crews')
    const { dialog, form } = await openForm(page)
    const folder = form.locator('[data-compare-cwd]').getByRole('combobox')
    const checked = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/git/check' && new URL(r.url()).searchParams.get('cwd') === outside)
    await folder.fill(outside)
    await folder.press('Tab')
    expect((await checked).status()).toBe(400)
    await expect(folder).toHaveAttribute('aria-invalid', 'true')
    await expect(dialog.locator('[data-compare-create]')).toBeDisabled()
    // A new project: the server's answer comes and the form says nothing of it.
    const again = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/git/check' && new URL(r.url()).searchParams.get('cwd') === fresh)
    await folder.fill(fresh)
    await folder.press('Tab')
    expect((await again).status()).toBe(200)
    await expect(folder).not.toHaveAttribute('aria-invalid', 'true')
    await expect(dialog.locator('[data-compare-create]')).toBeEnabled()
    for (const words of ['No commits', 'Not a git repository', 'first commit']) await expect(dialog).not.toContainText(words)
    await dialog.locator('[data-compare-cancel]').click()
  } finally {
    rmSync(outside, { recursive: true, force: true })
    rmSync(fresh, { recursive: true, force: true })
  }
})

test('a press holds the form while its save is out, sends once, and a save the server refuses keeps what was typed', async ({ page, state }) => {
  test.setTimeout(120_000)
  const seen = posted(page)
  // The save is held at the spec's own route until judged, then answered as the server refuses a crew.
  let release: () => void = () => {}
  const held = new Promise<void>((done) => (release = done))
  // A route kept on the page from before it loads, the spec's own on top (AGENTS.md: switching interception on and off).
  await standingRoute(page.context())
  await page.route('**/api/crews/templates/compare', async (route) => {
    await held
    await route.fulfill({ status: 400, contentType: 'application/json', body: JSON.stringify({ error: { code: 'invalid_crew', message: 'e2e: crews are off here' } }) })
  })
  await page.setViewportSize(DESK)
  await page.goto('/crews')
  const { dialog, form } = await openForm(page)
  await form.locator('[data-compare-goal]').fill('Add rate limiting to the login route')
  await form.locator('[data-compare-check]').fill('true')
  const folder = form.locator('[data-compare-cwd]').getByRole('combobox')
  await folder.fill(state.repo)
  await folder.press('Tab')
  const save = dialog.locator('[data-compare-create]')
  await save.click()
  await expect(save).toHaveAttribute('aria-busy', 'true')
  await expect(form).toHaveAttribute('aria-busy', 'true')
  await expect(form.locator('[data-compare-goal]')).toBeDisabled()
  await expect(dialog.locator('[data-compare-cancel]')).toBeDisabled()
  // A second press and Ctrl with Enter send nothing more.
  await save.click({ force: true })
  await page.keyboard.press('Control+Enter')
  await expect.poll(() => seen.length).toBe(1)
  release()
  const alert = dialog.locator('[data-compare-template-error]')
  await expect(alert).toContainText('Not saved')
  await expect(alert).toContainText('E2e: crews are off here. What you typed is kept.')
  await expect(form.locator('[data-compare-goal]')).toHaveValue('Add rate limiting to the login route')
  await expect(save).toBeEnabled()
  await expect(save).not.toHaveAttribute('aria-busy', 'true')
  expect(seen).toHaveLength(1)
  await dialog.locator('[data-compare-cancel]').click()
})

test("New agent trial comes back as the last saved trial left it: its agents, folder and that folder's check, never its arguments, across a reload too", async ({ page, state }) => {
  test.setTimeout(120_000)
  await page.setViewportSize(DESK)
  await page.goto('/crews')
  let { dialog, form } = await openForm(page)
  // Agent B moved to another agent than the form picked, so what comes back is the person's, not the form's own start.
  await form.locator('[data-trial-agent="1"]').click()
  const items = page.getByRole('menuitemcheckbox')
  await expect(items.nth(2)).toBeVisible()
  await items.nth(2).click()
  await expect(page.getByRole('menu')).toHaveCount(0)
  const agents = await form.locator('[data-trial-agent]').evaluateAll((els) => els.map((e) => e.getAttribute('data-agent-id')))
  await form.locator('[data-compare-goal]').fill('Remembered trial')
  await form.locator('[data-compare-check]').fill('true')
  const folder = form.locator('[data-compare-cwd]').getByRole('combobox')
  await folder.fill(state.repo)
  await folder.press('Tab')
  const more = await openMore(form)
  await more.locator('[data-compare-agent-args="0"]').fill('--model e2e')
  await dialog.locator('[data-compare-create]').click()
  await expect(dialog).toHaveCount(0, { timeout: 30_000 })
  await expect(page).toHaveURL(/\/crews\/(?!new)[^/?]+/, { timeout: 30_000 })
  made.push(decodeURIComponent(new URL(page.url()).pathname.split('/')[2]!))

  // The page wrote what it keeps before anything reloads it.
  await expect.poll(() => page.evaluate(() => localStorage.getItem('conductor-trial'))).toContain(state.repo)
  for (const step of ['again', 'after a reload'] as const) {
    if (step === 'after a reload') await page.reload()
    await page.goto('/crews')
    ;({ dialog, form } = await openForm(page))
    expect(await form.locator('[data-trial-agent]').evaluateAll((els) => els.map((e) => e.getAttribute('data-agent-id'))), step).toEqual(agents)
    await expect(form.locator('[data-compare-cwd]').getByRole('combobox'), step).toHaveValue(state.repo)
    await expect(form.locator('[data-compare-check]'), step).toHaveValue('true')
    await expect(form.locator('[data-compare-goal]'), step).toHaveValue('')
    await expect(form.locator('[data-trial-agent-args]'), step).toHaveCount(0)
    await dialog.locator('[data-compare-cancel]').click()
    await expect(dialog).toHaveCount(0)
  }
})
