import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { budget, expect, type Api } from './fixtures'
import { makeProject, makeTicket, opOf, storedTicket } from './dispatch-helpers'
import { git } from './server'

// A comparison launched on a server of the spec's own (Program item CP1, docs/plans/compare-crew.md, PR-7 and PR-8b), through the
// routes a person's page uses (the template, DS2's propose and approve, the launch with its count), and its selection waited for on
// the server's own reads: the comparison's `selection` (PR-6b) and DR1's record. compare.spec.ts and compare-helpers.ts drive the
// form and the dialog; these specs start where the candidates are working.

/** How long every lane takes to end on a loaded machine: the bound of a hang, the stub's own steps being seconds. */
export const SETTLE_MS = 240_000

/** The check every candidate is held to: `sh scripts/check.sh`, which fails once a lane's stub has edited it (STUB_COMPARE_EDIT=script). */
export function checkScript(repo: string): string {
  mkdirSync(join(repo, 'scripts'), { recursive: true })
  // The pattern's [s] keeps the line from matching itself: only the stub's appended line does.
  writeFileSync(join(repo, 'scripts', 'check.sh'), "#!/bin/sh\n# fails once a candidate's stub has appended its line\ngrep -q 'edited this [s]cript' \"$0\" && exit 1\nexit 0\n")
  git(repo, 'add', 'scripts/check.sh')
  git(repo, 'commit', '-q', '-m', 'the check')
  return git(repo, 'rev-parse', 'HEAD').trim()
}

export interface Launched {
  project: string
  /** KEY-1 */
  ticket: string
  /** The ticket page's path. */
  path: string
  /** The chain's launched run. */
  run: string
}

/**
 * A comparison's crew made by its template route, proposed as revision 1 of a ticket of project `project` and approved, then launched
 * with its count: the run it started. `candidates` name agents of the server's catalog (an ownServer copy among them).
 */
export async function launchComparison(api: Api, repo: string, project: string, candidates: Array<{ agentId: string; approach?: string }>, o: { reviews?: boolean; brakeman?: boolean } = {}): Promise<Launched> {
  const { crew } = await api.ok<{ crew: { id: string } }>('POST', '/api/crews/templates/compare', {
    name: `Compare for ${project}`,
    cwd: repo,
    goal: 'Make the check pass',
    instructions: 'Keep the API as it is.',
    criteria: [{ id: 'no-dep', text: 'no new dependency' }],
    checks: [{ id: 'unit', argv: ['sh', 'scripts/check.sh'] }],
    candidates,
    ...(o.reviews ? { reviews: true, reviewAgent: 'worker-cli' } : {}),
    ...(o.brakeman ? { brakeman: true } : {}),
  })
  await makeProject(api, project)
  const ticket = await makeTicket(api, project, '1', { title: 'Compare approaches' })
  const { plan } = await api.ok<{ plan: { id: string; rev: number } }>('POST', `/api/dispatch/tickets/${ticket}/plans`, { opId: opOf(project, 'r1'), crew: crew.id })
  await api.ok('POST', `/api/dispatch/plans/${plan.id}/approve`, { opId: opOf(project, 'ap1'), expectedRev: plan.rev, ticketRev: (await storedTicket(api, ticket)).rev })
  const launched = await api.ok<{ launch: { runId: string } }>('POST', `/api/dispatch/tickets/${ticket}/launch`, { opId: opOf(project, 'l1'), plan: plan.id, candidates: candidates.length })
  return { project, ticket, path: `/dispatch/${project}/1`, run: launched.launch.runId }
}

export interface ServedSelection {
  decision: string
  state: string
  options: Array<{ label: string; subject?: { task: string; n: number; commit: string } }>
  applied?: { selected?: { task: string; run: string; attempt: number; commit: string }; none?: true; words?: string }
}

/** The comparison as the server reads it now. */
export async function comparison(api: Api, run: string): Promise<{ settled: boolean; selection?: ServedSelection | null; candidates: Array<{ task: string; run: string; attempt: number; commit: string; state: string }> }> {
  return api.ok('GET', `/api/runs/${run}/comparison`)
}

/**
 * Waits for the comparison's selection to be `state` (and, with `not`, another record than that one: the one that replaced it), on the
 * comparison's own read; the selection as it is then. Raises the test's timeout by its bound.
 */
export async function selectionUntil(api: Api, run: string, state: string, o: { not?: string; timeout?: number } = {}): Promise<ServedSelection> {
  const timeout = o.timeout ?? SETTLE_MS
  budget(timeout)
  let last: ServedSelection | null | undefined
  let seen = ''
  try {
    await expect
      .poll(
        async () => {
          const c = await comparison(api, run)
          last = c.selection
          seen = JSON.stringify({ settled: c.settled, state: (c as { state?: string }).state, selection: last ? { decision: last.decision, state: last.state } : null, lanes: c.candidates.map((x) => `${x.task}:${x.state}`) })
          return !!last && last.state === state && last.decision !== o.not
        },
        { timeout, intervals: [1000], message: `the comparison's selection is ${state}${o.not ? `, not ${o.not}` : ''}` },
      )
      .toBe(true)
  } catch (e) {
    throw new Error(`${(e as Error).message}\nthe comparison's last read: ${seen}`)
  }
  return last!
}
