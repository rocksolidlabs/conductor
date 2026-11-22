import type { Browser, Locator, Page } from '@playwright/test'
import { budget, expect, type Api, type Run } from './fixtures'
import { ownServer, type AgentCopy, type OwnServer } from './own-server'
import type { Started } from './server'

// What the durable decisions' specs share (Program item DR1, docs/plans/decisions.md, web/e2e): a server of their own (as the Brakeman
// specs have, brakeman-cards.spec.ts), so that its decisions are the spec's alone (dr-1 its first) and its stub agents can be given
// what they ask (STUB_DECISION); the launch from a ticket's approved plan revision; the record read through the API.

/** A decision as the operator's routes give it, what the specs read of it. */
export interface DecisionRecord {
  id: string
  run: string
  origin: string
  type: string
  gate?: string
  asker?: { task: string; attempt: number; live: boolean }
  state: string
  rev: number
  answer?: string
  applied?: { how: string; words: string; tasks?: string[] }
  history?: Array<{ kind: string }>
}

/** Raises the running test's timeout by `ms`: the one budget() of fixtures.ts, kept under this name for the decisions' specs. */
export { budget }

/**
 * The cap of a page's first draw after a load, which only bounds a hang: every load boots the app again before its first read, and a
 * loaded machine takes seconds over it (eleven at a load average above 70), past a step's own cap.
 */
export const DRAW_MS = 60_000

/**
 * Waits for `first`, the first thing a page just loaded draws that the next step needs, within DRAW_MS, raising the test's timeout by
 * as much: the steps after it then keep their own caps for their own work.
 */
export async function firstDraw(first: Locator) {
  budget(DRAW_MS)
  await expect(first).toBeVisible({ timeout: DRAW_MS })
}

/** A server of the decisions' specs' own: ownServer's (own-server.ts). */
export type DecisionServer = OwnServer

/**
 * Starts a server of the spec's own, over a scratch repository, with the e2e catalog and `agents` added to it (each a copy of the
 * catalog's agent `from` under a new id, with `env` over its own). Its log is copied into test-results as `<name>-server.log` at stop.
 */
export function decisionServer(name: string, agents: AgentCopy[] = []): Promise<DecisionServer> {
  return ownServer(name, { agents })
}

/** A page of the spec's server, as the operator: its workbench token where the workbench keeps it, and a display name. */
export async function operatorPage(browser: Browser, s: Started, name = 'Nate'): Promise<Page> {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  await ctx.addInitScript(
    ({ t, n }) => {
      localStorage.setItem('conductor.workbenchToken', t)
      localStorage.setItem('conductor.displayName', n)
    },
    { t: s.token, n: name },
  )
  return ctx.newPage()
}

/** A project and its ticket 1, through Dispatch's routes; the ticket's key. */
export async function makeTicket(api: Api, project: string, title: string): Promise<string> {
  await api.ok('POST', '/api/dispatch/projects', { opId: `${project.toLowerCase()}-p`, key: project, name: `E2E ${project}` })
  const r = await api.ok<{ ticket: { key: string } }>('POST', '/api/dispatch/tickets', { opId: `${project.toLowerCase()}-t1`, project, title })
  return r.ticket.key
}

/** Revision 1 of the ticket proposed from the crew (with the plan's `decisions`, when given) through the propose route; its id. */
export async function propose(api: Api, ticket: string, crew: string, decisions?: Array<{ id: string; question: string; kind: string; before: string[] }>): Promise<string> {
  const r = await api.ok<{ plan: { id: string } }>('POST', `/api/dispatch/tickets/${ticket}/plans`, { opId: `${ticket.toLowerCase()}-r1`, crew, ...(decisions ? { decisions } : {}) })
  return r.plan.id
}

/** The decisions of the run that holds them, through the operator's list. */
export async function decisionsOf(api: Api, run: string): Promise<DecisionRecord[]> {
  return (await api.ok<{ decisions: DecisionRecord[] }>('GET', `/api/decisions?run=${encodeURIComponent(run)}`)).decisions
}

/** One decision whole. */
export async function decision(api: Api, id: string): Promise<DecisionRecord> {
  return (await api.ok<{ decision: DecisionRecord }>('GET', `/api/decisions/${id}`)).decision
}

/** The run read until `done` holds of it, within `timeout`, which the test's timeout is raised by. */
export async function runUntil(api: Api, id: string, done: (r: Run) => boolean, message: string, timeout = 120_000): Promise<Run> {
  budget(timeout)
  let last = await api.run(id)
  await expect.poll(async () => done((last = await api.run(id))), { timeout, intervals: [500], message }).toBe(true)
  return last
}

/** The decision read until it is in `state`, within `timeout`. */
export async function decisionUntil(api: Api, id: string, state: string, timeout = 60_000): Promise<DecisionRecord> {
  budget(timeout)
  let last = await decision(api, id)
  await expect.poll(async () => (last = await decision(api, id)).state, { timeout, intervals: [250], message: `${id} is ${state}` }).toBe(state)
  return last
}
