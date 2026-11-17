import { randomBytes } from 'node:crypto'
import type { Locator, Page } from '@playwright/test'
import { expect, type Api } from './fixtures'
import { freeProjectKey, makeProject, makeTicket, opOf, storedTicket } from './dispatch-helpers'

// A comparison made the way a person makes one (CP1, docs/plans/compare-crew.md): New comparison on Crews, filled and created; the
// crew proposed as a ticket's plan revision and approved, through the API (DS2's own routes, unchanged); then Launch from that
// revision, its statement read and its count sent from the dialog. What other specs of the comparison start from.

/** A candidate as the form takes it: an agent of the e2e catalog, its arguments, its approach. */
export interface ComparisonCandidate {
  agentId: string
  args?: string[]
  approach?: string
}

export interface ComparisonOptions {
  /** The crew's name; one of the test's own when not given. */
  name?: string
  /** The project folder: the e2e repository (state.repo) or a repository of the test's own. */
  cwd: string
  goal?: string
  instructions?: string
  criteria?: string[]
  /** The checks every candidate's result must pass, each an id and an argv. */
  checks?: Array<{ id: string; argv: string[] }>
  /** The project's conductor.manifest.json verify steps run too. */
  manifestFile?: boolean
  /** Two to four. */
  candidates: ComparisonCandidate[]
  /** The agent that reviews each candidate that succeeds; no reviews when not given. */
  reviewAgent?: string
  brakeman?: boolean
  /** The project the ticket is made in; one of the test's own when not given. */
  project?: string
}

export interface MadeComparison {
  crewId: string
  /** The project's key and the ticket's (KEY-1). */
  project: string
  ticket: string
  planId: string
  /** The run the launch started: the chain's launched run. */
  runId: string
}

/** Fills one ArgvInput with `args`, each one chip. */
async function fillArgs(field: Locator, args: readonly string[]) {
  const input = field.locator('input')
  for (const a of args) {
    await input.fill(/[\s"']/.test(a) ? `"${a.replace(/"/g, '\\"')}"` : a)
    await input.press('Enter')
  }
}

/** Picks the option labelled `name` of a USelect, and waits for its menu to be gone (AGENTS.md: a menu that has closed is not gone). */
async function pick(page: Page, select: Locator, name: string) {
  await select.click()
  await page.getByRole('option', { name, exact: true }).click()
  await expect(page.getByRole('listbox')).toHaveCount(0)
}

/**
 * Fills New comparison on Crews with `o` and creates it; the crew's id, as the page opened it. The page must be one whose layout has
 * drawn (any page of the app). The agents are named as the catalog names them.
 */
export async function createComparison(page: Page, api: Api, o: ComparisonOptions): Promise<string> {
  const catalog = (await api.ok<{ agents: Array<{ id: string; name: string }> }>('GET', '/api/catalog')).agents
  const nameOf = (id: string) => catalog.find((a) => a.id === id)?.name ?? id
  await page.goto('/crews')
  await page.locator('[data-new-comparison]').click()
  const form = page.locator('[data-compare-template]')
  await expect(form).toBeVisible()
  await form.locator('[data-compare-name]').fill(o.name ?? `e2e comparison ${randomBytes(3).toString('hex')}`)
  const cwd = form.locator('[data-compare-cwd]').getByRole('combobox')
  await cwd.fill(o.cwd)
  await cwd.press('Tab')
  await expect(cwd).toHaveValue(o.cwd)
  await form.locator('[data-compare-goal]').fill(o.goal ?? 'Make the cache read fast')
  await form.locator('[data-compare-instructions]').fill(o.instructions ?? 'Keep the API as it is.')

  const criteria = o.criteria ?? ['no new dependency']
  for (let i = 0; i < criteria.length; i++) {
    if (i > 0) await form.locator('[data-compare-add-criterion]').click()
    await form.locator(`[data-compare-criterion="${i}"]`).fill(criteria[i]!)
  }

  // The form starts with one empty check: removed when the test names none.
  const checks = o.checks ?? []
  const rows = form.locator('[data-compare-checks] [data-crew-check]')
  for (let i = 0; i < checks.length; i++) {
    if (i > 0) await form.locator('[data-compare-checks] [data-add-check]').click()
    const row = rows.nth(i)
    await row.getByLabel(`Id of check ${i + 1}`).fill(checks[i]!.id)
    await fillArgs(row.getByLabel(`Command of check ${i + 1}`), checks[i]!.argv)
  }
  if (!checks.length) await rows.first().getByRole('button', { name: 'Remove check 1' }).click()
  if (o.manifestFile) await form.locator('[data-compare-manifest-file]').click()

  for (let i = 0; i < o.candidates.length; i++) {
    if (i >= 2) await form.locator('[data-compare-add-candidate]').click()
    const c = o.candidates[i]!
    const row = form.locator(`[data-compare-candidate-row="${i}"]`)
    await pick(page, row.locator('[data-compare-candidate-agent]'), nameOf(c.agentId))
    if (c.args?.length) await fillArgs(row.locator('[data-compare-candidate-args]'), c.args)
    if (c.approach) await row.locator('[data-compare-candidate-approach]').fill(c.approach)
  }
  if (o.reviewAgent) {
    await form.locator('[data-compare-reviews]').click()
    await pick(page, form.locator('[data-compare-review-agent]'), nameOf(o.reviewAgent))
  }
  if (o.brakeman) await form.locator('[data-compare-brakeman]').click()

  await expect(form.locator('[data-compare-template-problem]')).toHaveCount(0)
  await form.locator('[data-compare-create]').click()
  await expect(form).toHaveCount(0, { timeout: 30_000 })
  await expect(page).toHaveURL(/\/crews\/(?!new)[^/?]+/, { timeout: 30_000 })
  return decodeURIComponent(new URL(page.url()).pathname.split('/')[2]!)
}

/**
 * A comparison made as a person makes one, then launched: New comparison created (createComparison), proposed as revision 1 of a
 * ticket of its own and approved through the API, then Launch from the ticket page, whose statement says the candidates' count and
 * whose button sends it. The ids of what it made; the run has started.
 */
export async function makeComparison(page: Page, api: Api, o: ComparisonOptions): Promise<MadeComparison> {
  const crewId = await createComparison(page, api, o)
  const project = o.project ?? (await freeProjectKey(api, 'CMP'))
  if (!o.project) await makeProject(api, project)
  const ticket = await makeTicket(api, project, `c${randomBytes(2).toString('hex')}`, { title: 'Compare approaches' })
  const { plan } = await api.ok<{ plan: { id: string; rev: number } }>('POST', `/api/dispatch/tickets/${ticket}/plans`, { opId: opOf(project, `r-${randomBytes(3).toString('hex')}`), crew: crewId })
  const stored = await storedTicket(api, ticket)
  await api.ok('POST', `/api/dispatch/plans/${plan.id}/approve`, { opId: opOf(project, `ap-${randomBytes(3).toString('hex')}`), expectedRev: plan.rev, ticketRev: stored.rev })

  const n = ticket.split('-').pop()!
  await page.goto(`/dispatch/${project}/${n}`)
  const featured = page.locator('[data-plan-section] [data-plan-featured]')
  await expect(featured).toHaveAttribute('data-plan-state', 'approved', { timeout: 30_000 })
  await featured.locator('[data-plan-launch]').click()
  const dialog = page.locator('[data-launch-dialog]')
  const count = String(o.candidates.length)
  await expect(dialog.locator('[data-launch-compare]')).toHaveAttribute('data-launch-candidates', count, { timeout: 30_000 })
  const confirm = dialog.locator('[data-launch-confirm]')
  await expect(confirm).toHaveText(`Launch ${count} candidates`)
  await confirm.click()
  await expect(dialog).toHaveAttribute('data-launch-state', 'started', { timeout: 120_000 })
  const runId = (await dialog.locator('[data-launch-run-link]').getAttribute('data-launch-run-link')) ?? ''
  await page.keyboard.press('Escape')
  return { crewId, project, ticket, planId: plan.id, runId }
}
