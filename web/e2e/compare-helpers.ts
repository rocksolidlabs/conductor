import { randomBytes } from 'node:crypto'
import type { Page } from '@playwright/test'
import { expect, type Api } from './fixtures'
import { freeProjectKey, makeProject, makeTicket, opOf, storedTicket } from './dispatch-helpers'

// An agent trial (a comparison, in the code: CP1, docs/plans/compare-crew.md): its crew saved through the server's template route,
// the body New agent trial posts; the crew proposed as a ticket's plan revision and approved, through the API (DS2's own routes,
// unchanged); then Launch from that revision, its statement read and its count sent from the dialog. What other specs of a trial
// start from; trial-form.spec.ts drives the form itself.

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

/** A line for a good result's id, as the trial form writes it from its words (utils/compareTemplate.ts criterionId). */
function criterionId(text: string, i: number): string {
  const words = text.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 34).replace(/-+$/, '')
  return words || `criterion-${i + 1}`
}

/**
 * Saves the crew of an agent trial with `o` through the server's template route (POST /api/crews/templates/compare), the body New
 * agent trial posts, and opens it on Crews; the crew's id. The specs that do not test the form start here: trial-form.spec.ts drives
 * the form itself.
 */
export async function createComparison(page: Page, api: Api, o: ComparisonOptions): Promise<string> {
  const criteria = o.criteria ?? ['no new dependency']
  const body: Record<string, unknown> = {
    name: o.name ?? `e2e comparison ${randomBytes(3).toString('hex')}`,
    cwd: o.cwd,
    goal: o.goal ?? 'Make the cache read fast',
    instructions: o.instructions ?? 'Keep the API as it is.',
    criteria: criteria.map((text, i) => ({ id: criterionId(text, i), text })),
    candidates: o.candidates.map((c) => ({ agentId: c.agentId, ...(c.args?.length ? { args: c.args } : {}), ...(c.approach ? { approach: c.approach } : {}) })),
  }
  if (o.checks?.length) body.checks = o.checks
  if (o.manifestFile) body.manifest = { file: true }
  if (o.reviewAgent) Object.assign(body, { reviews: true, reviewAgent: o.reviewAgent })
  if (o.brakeman) body.brakeman = true
  const { crew } = await api.ok<{ crew: { id: string } }>('POST', '/api/crews/templates/compare', body)
  await page.goto(`/crews/${encodeURIComponent(crew.id)}`)
  await expect(page.locator('[data-crew-compare-block]')).toBeVisible({ timeout: 30_000 })
  return crew.id
}

/**
 * A trial made and launched: its crew saved (createComparison), proposed as revision 1 of a
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
