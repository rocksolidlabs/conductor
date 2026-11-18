import type { Page } from '@playwright/test'
import { brakemanServer, type BrakemanServer } from './brakeman-server'
import { walk, worst } from './contrast'
import { atRest, expect, test, useTheme } from './fixtures'
import { noSideways, tapTarget } from './layout'

// A brief led by what needs a person (the Brakeman design review's first feature), on a server of its own with the Brakeman stub (the
// seeded day holds no Brakeman records). The stub, asked to explain a ticket, holds at its gate until the spec has read its run and
// session, then submits the spec's brief: a decision about the ticket, a proposal, three facts, a reading of facts 1 and 2, and an
// evidence panel. The brief draws its regions in order (Needs you, Proposed, What changed, the fold), the reading under fact 2, Keep as
// an icon with its tooltip, the provenance folded and opened by a link to an item inside it, Copy as Markdown to the clipboard, and
// Comment on a ticket refusing past 16 KiB in words; at 390 x 844 its sections fold into rows a thumb can tap and nothing goes sideways;
// in both themes every text passes its floor, and the order holds after the theme's redraw.
test.describe.configure({ mode: 'serial' })

const PROJECT = 'BKB'
const KEY = `${PROJECT}-1`
let srv: BrakemanServer
let id = ''
let runId = ''
let sessionId = ''

/** The brief the stub submits: its items' indexes are what the page keeps, whatever order it draws them in. */
function brief() {
  return {
    schema: 'brakeman.brief/1',
    title: 'The ticket waits on a person',
    summary: 'Two facts say the ticket is ready; one says how this turn read it.',
    uncertainty: 'Only the ticket was read.',
    items: [
      { kind: 'evidence/1', title: 'What this turn read', refs: [`run:${runId}`, `session:${sessionId}`] },
      { kind: 'fact/1', text: 'The ticket is open.', evidence: [`ticket:${KEY}`] },
      { kind: 'interpretation/1', text: 'The ticket can be closed once a person agrees.', facts: [1, 3] },
      { kind: 'fact/1', text: 'This turn runs as its own run.', evidence: [`run:${runId}`] },
      { kind: 'proposal/1', text: 'Stop this turn once the ticket is looked at.', op: 'run_stop', target: `run:${runId}`, facts: [1] },
      {
        kind: 'decision/1',
        about: `ticket:${KEY}`,
        happened: 'The ticket has no owner.',
        whyHuman: 'Only a person takes a ticket on.',
        question: `Take ${KEY} on, or leave it?`,
        alternatives: [
          { label: 'Take it on', tradeoffs: 'It moves today.' },
          { label: 'Leave it', tradeoffs: 'It waits.' },
        ],
        recommendation: 0,
        authorizes: 'Nothing: the ticket is decided on its page.',
        evidence: [`ticket:${KEY}`],
        blocked: [`run:${runId}`],
      },
      { kind: 'fact/1', text: 'This turn has its own session.', evidence: [`session:${sessionId}`] },
    ],
  }
}

test.beforeAll(async () => {
  test.setTimeout(180_000)
  srv = await brakemanServer({ name: 'brakeman-brief' })
  await srv.api.ok('POST', '/api/dispatch/projects', { opId: 'bkb-p', key: PROJECT, name: 'Brakeman brief' })
  await srv.api.ok('POST', '/api/dispatch/tickets', { opId: 'bkb-t1', project: PROJECT, title: 'A ticket with no owner' })
  const asked = await srv.ask({ kind: 'explain', subjects: [`ticket:${KEY}`] })
  id = asked.id
  // The turn running, at the gate: its run and its session are what the brief cites.
  const running = await srv.until(id, ['running'])
  runId = running.runId!
  sessionId = running.sessionId!
  srv.setBrief(brief())
  srv.openGate()
  // A brief the server refuses leaves the turn running: the stub's transcript says why, printed with the failure.
  await srv.until(id, ['briefed']).catch(async (e) => {
    const { readdirSync, readFileSync } = await import('node:fs')
    const d = `${srv.home}/.stub-sessions`
    for (const f of readdirSync(d)) if (f.endsWith('.txt')) console.log('DEBUG', readFileSync(`${d}/${f}`, 'utf8').slice(-800))
    throw e
  })
})

test.afterAll(async () => {
  await srv?.stop()
})

/** The brief on the page, drawn: its title shown. */
async function opened(page: Page, path = `/brakeman?request=${id}`) {
  await srv.open(page, path, { name: 'Nate' })
  const b = page.locator(`[data-brief="${id}"]`)
  await expect(b.locator('[data-brief-title]')).toHaveText('The ticket waits on a person', { timeout: 30_000 })
  return b
}

/** The regions in the order they are drawn, and What changed's items in theirs. */
async function order(b: ReturnType<Page['locator']>) {
  await expect(b.locator('[data-brief-region]')).toHaveCount(4)
  expect(await b.locator('[data-brief-region]').evaluateAll((els) => els.map((e) => e.getAttribute('data-brief-region')))).toEqual(['needs', 'proposed', 'changed', 'fold'])
  // The reading of facts 1 and 2 is drawn under fact 2, the last it reads; fact 3 follows it. Each keeps its index in the brief.
  expect(await b.locator('[data-brief-region="changed"] [data-brief-item]').evaluateAll((els) => els.map((e) => e.getAttribute('data-brief-item')))).toEqual(['1', '3', '2', '6'])
}

test('a brief leads with what needs a person, reads each reading under the last fact it reads, and keeps a fact by an icon', async ({ page }) => {
  const b = await opened(page)
  await order(b)
  await expect(b.locator('[data-brief-reading]')).toContainText('Two facts say the ticket is ready')
  await expect(b.locator('[data-brief-caveat]')).toHaveText(/^Brakeman's reading, not a fact\.\s+Uncertain:\s+Only the ticket was read\.\s*$/)
  await expect(b.locator('[data-brief-region="needs"] h3')).toHaveText('Needs you · 1')
  // A ticket's decision is answered on its page: no command waits here, so the heading carries no mark.
  await expect(b.locator('[data-brief-region="needs"] [data-status-mark]')).toHaveCount(0)
  await expect(b.locator('[data-brief-recommends]')).toHaveText('Brakeman recommends: Take it on')
  await expect(b.locator('[data-decision-lead]')).toHaveText(/^The ticket has no owner\. Waiting on it: run .+\.$/)
  await expect(b.locator('[data-brief-region="changed"] h3')).toHaveText('What changed · 3 facts')
  await expect(b.locator('[data-brief-interpretation]')).toHaveText('Brakeman reads facts 1 and 2: The ticket can be closed once a person agrees.')
  // The proposal's operation is said under its row (a turn's own run has no revision to carry).
  await expect(b.locator('[data-proposal-op="run_stop"] [data-proposal-line]')).toContainText('Operation run_stop on')

  // Keep is an icon button whose tooltip says what keeping does.
  const keep = b.locator('[data-keep-note="3"]')
  await expect(keep).toHaveAttribute('data-icon-button', '')
  await expect(keep).toHaveAttribute('aria-label', 'Keep fact 2 as a note: given to every later turn, whatever it may see')
  await keep.hover()
  const tip = page.locator('[data-slot="content"] [data-slot="text"]').filter({ hasText: /^Keep fact 2 as a note: given to every later turn, whatever it may see$/ })
  await expect(tip).toBeVisible()
  await atRest(tip)
  await expect(tip).toBeInViewport()

  // The decision's Details and the fold keep their content mounted, unseen until opened.
  const reads = (await srv.request(id)).reads ?? 0
  await expect(b.locator('[data-brief-provenance]')).toContainText(`request ${id}`)
  await expect(b.locator('[data-brief-provenance]')).toBeHidden()
  await expect(b.locator('[data-brief-fold]')).toHaveText(`Provenance and all evidence: brakeman-stub, its default model, request ${id}, ${reads} ${reads === 1 ? 'read' : 'reads'}`)
  await expect(b.locator('[data-decision-answerer]')).toBeHidden()
  await b.locator('[data-brief-decision-details]').click()
  await expect(b.locator('[data-decision-answerer]')).toBeVisible()
})

test('a link to an item inside the fold opens the fold before the page scrolls to it', async ({ page }) => {
  await page.addInitScript(() => {
    const w = window as Window & { __scrolled?: string[] }
    w.__scrolled = []
    const into = Element.prototype.scrollIntoView
    Element.prototype.scrollIntoView = function (this: Element, arg?: boolean | ScrollIntoViewOptions) {
      w.__scrolled!.push(this.id)
      return into.call(this, arg)
    }
  })
  const b = await opened(page, `/brakeman?request=${id}#item-0`)
  await expect(b.locator('#item-0')).toBeVisible()
  await expect(b.locator('[data-brief-provenance]')).toBeVisible()
  await expect.poll(() => page.evaluate(() => (window as Window & { __scrolled?: string[] }).__scrolled)).toContain('item-0')
})

test('on a phone the brief folds its sections into rows a thumb taps, and nothing goes sideways', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const b = await opened(page)
  const rows = b.locator('[data-phone-section]')
  await expect(rows).toHaveCount(2)
  expect(await rows.evaluateAll((els) => els.map((e) => e.getAttribute('data-phone-section')))).toEqual(['proposed', 'changed'])
  await expect(b.locator('[data-brief-region="changed"] [data-brief-fact]').first()).toBeHidden()
  await expect(b.locator('[data-brief-decision-details]')).toBeHidden()
  await expect(b.locator('[data-brief-copy]')).toBeHidden()
  await tapTarget(rows)
  await b.locator('[data-phone-section="changed"]').click()
  await expect(b.locator('[data-phone-section="changed"]')).toHaveAttribute('aria-expanded', 'true')
  await expect(b.locator('[data-brief-region="changed"] [data-brief-fact]').first()).toBeVisible()
  await tapTarget(b.locator('[data-keep-note]'))
  await tapTarget(b.locator('[data-decision-ticket], [data-brief-fold]'))
  await noSideways(page)
})

for (const theme of ['dark', 'light'] as const) {
  test(`every text of the brief passes its floor in the ${theme} theme, and the order holds after the theme's redraw`, async ({ page }) => {
    const b = await opened(page)
    await useTheme(page, theme)
    await expect(b.locator('[data-brief-title]')).toHaveText('The ticket waits on a person', { timeout: 30_000 })
    await b.locator('[data-brief-fold]').click()
    await b.locator('[data-brief-decision-details]').click()
    await expect(b.locator('[data-brief-provenance]')).toBeVisible()
    await order(b)
    const { judged, failures } = await walk(page, b)
    expect(judged, `${theme}: the walk judged some text`).toBeGreaterThan(0)
    expect(failures, `${theme}: text under its floor (the ten worst):\n${worst(failures)}`).toEqual([])
  })
}

// The head (Copy, Comment) is drawn where the page asks for it (`head`): the comparison page draws the brief without one.
test('Copy as Markdown copies the brief with its reading marked and its kinds apart', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const b = await opened(page)
  await expect(b.locator('[data-brief-head]')).toContainText('Explain this')
  await expect(b.locator('[data-brief-head]')).toContainText(KEY)
  await b.locator('[data-brief-copy]').click()
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toContain('# The ticket waits on a person')
  const md = await page.evaluate(() => navigator.clipboard.readText())
  expect(md).toContain("**Brakeman's reading, not a fact.**")
  for (const h of ['## Needs you', '## Proposed', '## Facts', "## Brakeman's readings (interpretations, not facts)"]) expect(md).toContain(`\n${h}\n`)
  expect(md).toContain('- It reads facts 1 and 2: The ticket can be closed once a person agrees.')
  // Its links are the pages' own paths: none carries a query string.
  const links = [...md.matchAll(/\]\(([^)]*)\)/g)].map((m) => m[1]!)
  expect(links.length).toBeGreaterThan(0)
  for (const l of links) expect(l).toMatch(/^\/[^?]*$/)
  expect(md).not.toContain(srv.started.token)
})

test('Comment on a ticket refuses past a comment\'s 16 KiB in words, and posts what the person shortened under their name', async ({ page }) => {
  const b = await opened(page)
  await b.locator('[data-brief-comment]').click()
  const dialog = page.locator('[data-comment-ticket]')
  await expect(dialog).toHaveAttribute('data-comment-target', KEY)
  await expect(dialog.locator(`[data-comment-key="${KEY}"]`)).toHaveAttribute('aria-pressed', 'true')
  // The confirm says under whose name it posts, and, as the turn could read check logs or terminals, that the brief may quote one.
  const { request } = await srv.api.ok<{ request: { config: { visibility: { logs?: boolean; terminals?: boolean } } } }>('GET', `/api/brakeman/requests/${id}`)
  const quotes = request.config.visibility.logs || request.config.visibility.terminals
  await expect(dialog.locator('[data-comment-confirm]')).toHaveText(`It posts under your name, Nate.${quotes ? ' The brief may quote a terminal screen or a check log.' : ''}`)
  const body = dialog.locator('[data-comment-body] textarea, textarea[data-comment-body]').first()
  await expect(body).toHaveValue(/^# The ticket waits on a person\n/)
  const send = page.locator('[data-comment-send]')
  await body.fill('x'.repeat((16 << 10) + 1))
  await expect(dialog.locator('[data-comment-bound]')).toHaveText("More than a ticket's comment holds: shorten it above to send it. Nothing is cut for you.")
  await expect(send).toBeDisabled()
  await body.fill('The ticket waits on a person, as Brakeman read it.')
  await expect(dialog.locator('[data-comment-bound]')).toHaveCount(0)
  await send.click()
  await expect(dialog).toHaveCount(0)
  const { comments } = await srv.api.ok<{ comments: Array<{ body: string; by: string }> }>('GET', `/api/dispatch/tickets/${KEY}/comments?after=0&limit=50`)
  expect(comments.map((c) => [c.body, c.by])).toEqual([['The ticket waits on a person, as Brakeman read it.', 'Nate']])
})
