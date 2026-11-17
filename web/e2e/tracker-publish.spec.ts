import type { Page } from '@playwright/test'
import { dayPage, expect, test } from './fixtures'
import { fakeGitHub, type FakeGitHub } from './fake-github'
import { makeProject, opOf } from './dispatch-helpers'
import { ownServer, type OwnServer } from './own-server'

// Publish to GitHub (Program item IN1a, docs/plans/tracker-linking.md, PR-9), against the e2e's fake GitHub (fake-github.ts) on a server
// of the spec's own in loopback mode, with an issue imported as a ticket whose title holds markup. The composer starts with every part
// and the close off and draws the key part as text; Preview draws the server's preview byte for byte with the marker said; Publish
// pressed twice quickly posts one comment, its text the preview's; a reload shows it Published. A post GitHub holds past the send's own
// cap is not confirmed: no Send is offered for it, only Check and Publish as new; once GitHub shows the comment, Check makes it
// Published, and nothing more was posted.
test.describe.configure({ mode: 'serial' })

const TOKEN = 'ghp_e2eFakeToken0123456789abcdefPUBLISH'
const KEY = 'PUB'
const ISSUE = 12
const TITLE = 'The list crashes <b>when</b> empty'
/** The send's own deadline (internal/api, PublishTimeout): the most a Publish can take to answer. */
const PUBLISH_MS = 315_000
/** A prepare's and a Check's deadlines (PrepareTimeout, CheckTimeout). */
const PREPARE_MS = 165_000
const CHECK_MS = 75_000
/** The quiet time after a send begins before a listing without its comment makes it not confirmed (dispatch.PublicationQuiet). */
const QUIET_MS = 30_000
const PULL = 13

let srv: OwnServer
let gh: FakeGitHub
let ticket = ''
let path = ''

test.beforeAll(async () => {
  gh = await fakeGitHub()
  srv = await ownServer('tracker-publish', { env: { CONDUCTOR_TRACKER_LOOPBACK: '1' } })
  const api = srv.api
  await api.ok('POST', '/api/trackers/github', { host: gh.host, token: TOKEN })
  // The fake's clock at the server's: a comment's time is then in the window the server lists from (a day before its first send).
  gh.set({ n: ISSUE, kind: 'issue', title: TITLE, body: 'It crashes on an empty list.', state: 'open', updatedAt: new Date().toISOString().replace(/\.\d{3}Z$/, 'Z') })
  await makeProject(api, KEY)
  const imported = await api.ok<{ ticket: { key: string } }>('POST', `/api/dispatch/projects/${KEY}/import`, { opId: opOf(KEY, 'import'), url: gh.url(ISSUE) })
  ticket = imported.ticket.key
  path = `/dispatch/${KEY}/${ticket.split('-')[1]}`
})

test.afterAll(async () => {
  await gh?.stop()
  await srv?.stop()
})

/** The ticket's page, its Publications drawn. */
async function ticketPage(browser: Parameters<typeof dayPage>[0]): Promise<Page> {
  const page = await dayPage(browser, { server: srv.started, path })
  await expect(page.locator('[data-publications]')).toBeVisible()
  return page
}

/** The composer opened from the Publications, its offer read. */
async function openComposer(page: Page) {
  await page.locator('[data-publish-open]').click()
  const dialog = page.locator('[data-dispatch-publish]')
  await expect(dialog.locator('[data-publish-target]')).toBeVisible({ timeout: 30_000 })
  return dialog
}

/** The publication's own read, as the server holds it. */
async function publicationOf(id: string) {
  return srv.api.ok<{ publication: { state: string; marker: string; body: string }; preview: string; postSha256: string }>('GET', `/api/dispatch/publications/${id}`)
}

test('composes with every part off, previews the exact bytes with the marker said, and posts once however often Publish is pressed', async ({ browser }) => {
  test.setTimeout(test.info().timeout + PUBLISH_MS)
  const page = await ticketPage(browser)
  const dialog = await openComposer(page)

  // Every part and the close start off; only the offered parts are drawn; the key part's title, written on GitHub, is drawn as text.
  const parts = dialog.locator('[data-publish-part]')
  await expect(parts).toHaveCount(1)
  const keyPart = dialog.getByRole('checkbox', { name: "The ticket's key and title" })
  await expect(keyPart).not.toBeChecked()
  await expect(dialog.getByRole('checkbox', { name: 'Close the issue as completed' })).not.toBeChecked()
  await expect(dialog.locator('[data-publish-part-text="key"]')).toHaveText(`Conductor ticket ${ticket}: ${TITLE}`)
  await expect(dialog.locator('[data-publish-part-text="key"] b')).toHaveCount(0)
  await expect(dialog.locator('[data-publish-body]')).toHaveValue('')

  await dialog.locator('[data-publish-body]').fill('Fixed in the release.')
  await keyPart.click()
  await expect(keyPart).toBeChecked()
  await dialog.locator('[data-publish-prepare]').click()

  // The preview is the server's preview, byte for byte, drawn as text; the marker is said in words.
  const preview = dialog.locator('[data-publish-preview]')
  await expect(preview).toBeVisible({ timeout: 30_000 })
  const listed = await srv.api.ok<{ publications: Array<{ id: string; state: string }> }>('GET', `/api/dispatch/tickets/${ticket}/publications`)
  const id = listed.publications[0]!.id
  const read = await publicationOf(id)
  expect(read.publication.state).toBe('draft')
  expect(read.preview).toBe(`Fixed in the release.\n\nConductor ticket ${ticket}: ${TITLE}\n\n${read.publication.marker}`)
  expect(await preview.evaluate((el) => el.textContent)).toBe(read.preview)
  await expect(preview.locator('b')).toHaveCount(0)
  await expect(dialog.locator('[data-publish-marker]')).toContainText(read.publication.marker)
  await expect(dialog.locator('[data-publish-marker]')).toContainText('hidden on GitHub')
  await expect(dialog.locator('[data-publish-digest]')).toHaveText(read.postSha256)
  expect(gh.count('comment')).toBe(0)

  // Pressed twice quickly: one comment, its text the preview's, as the token's account.
  await dialog.locator('[data-publish-send]').dblclick()
  const result = dialog.locator('[data-publish-result]')
  await expect(result).toHaveAttribute('data-publication-state', 'sent', { timeout: PUBLISH_MS })
  await expect(result).toHaveText(new RegExp(`^Published to ${gh.owner}/${gh.repo}#${ISSUE} by @mona-example at \\S+`))
  expect(gh.count('comment')).toBe(1)
  expect(gh.comments(ISSUE).map((c) => c.body)).toEqual([read.preview])

  // A reload shows it Published, and posts nothing.
  await page.reload()
  const row = page.locator(`[data-publication="${id}"]`)
  await expect(row).toHaveAttribute('data-publication-state', 'sent')
  await expect(row.locator('[data-publication-words]')).toHaveText(new RegExp(`^Published to ${gh.owner}/${gh.repo}#${ISSUE} by @mona-example`))
  await expect(row.locator('button')).toHaveCount(0)
  expect(gh.count('comment')).toBe(1)
})

test('a post GitHub holds is being sent, then not confirmed: never sent again; Publish as new warns; Check makes it Published once GitHub shows it', async ({ browser }) => {
  test.setTimeout(test.info().timeout + PUBLISH_MS + QUIET_MS + 3 * CHECK_MS)
  const page = await ticketPage(browser)
  const dialog = await openComposer(page)
  await dialog.locator('[data-publish-body]').fill('Shipped, at last.')
  await dialog.locator('[data-publish-prepare]').click()
  await expect(dialog.locator('[data-publish-preview]')).toBeVisible({ timeout: 30_000 })
  const before = gh.count('comment')
  const held = gh.hold('comment')
  await dialog.locator('[data-publish-send]').click()
  await held.reached
  await expect(dialog.locator('[data-publish-busy]')).toHaveText('Posting to GitHub…')
  // The post runs out its cap with GitHub silent: no answer says whether it was made, so it is being sent, and its comment looked for.
  const result = dialog.locator('[data-publish-result]')
  await expect(result).toHaveAttribute('data-publication-state', 'sending', { timeout: PUBLISH_MS })
  await dialog.locator('[data-publish-done]').click()

  const listed = await srv.api.ok<{ publications: Array<{ id: string; state: string }> }>('GET', `/api/dispatch/tickets/${ticket}/publications`)
  const id = listed.publications[0]!.id
  const row = page.locator(`[data-publication="${id}"]`)
  const check = row.locator('[data-publication-check]')
  await expect(row).toHaveAttribute('data-publication-state', 'sending')
  // Being sent: Check alone, never Send, Publish as new or Discard.
  await expect(row.locator('button')).toHaveCount(1)
  await expect(check).toBeVisible()

  // Check, pressed until the quiet time after the send began has passed with no comment holding its marker: not confirmed.
  await expect(async () => {
    if ((await row.getAttribute('data-publication-state')) === 'sending') await check.click({ timeout: 2_000 })
    await expect(row).toHaveAttribute('data-publication-state', 'unconfirmed', { timeout: CHECK_MS })
  }).toPass({ timeout: QUIET_MS + 2 * CHECK_MS })
  await expect(row.locator('[data-publication-words]')).toContainText('not confirmed: GitHub has not shown this comment; it may still appear')
  // Never sent again, by any button: Check, Publish as new and Discard.
  await expect(row.locator('[data-publication-send-again]')).toHaveCount(0)
  await expect(row.locator('[data-publication-check]')).toBeVisible()
  await expect(row.locator('[data-publication-discard]')).toBeVisible()
  // Publish as new: its text, and the warning that the first may still appear; nothing prepared until Preview.
  await row.locator('[data-publication-new]').click()
  await expect(dialog.locator('[data-publish-as-new]')).toHaveText('The first publication may still appear on GitHub: publishing this again can leave two comments.')
  await expect(dialog.locator('[data-publish-body]')).toHaveValue('Shipped, at last.')
  await dialog.getByRole('button', { name: 'Cancel' }).click()
  await expect(dialog).toHaveCount(0)

  // GitHub shows the comment the held post made: Check finds it, and nothing more is posted.
  held.release()
  await expect.poll(() => gh.comments(ISSUE).length, { timeout: 30_000 }).toBe(before + 1)
  await row.locator('[data-publication-check]').click()
  await expect(row).toHaveAttribute('data-publication-state', 'sent', { timeout: CHECK_MS })
  await expect(row.locator('[data-publication-words]')).toHaveText(/^Published to /)
  expect(gh.count('comment')).toBe(before + 1)
  const after = await srv.api.ok<{ publications: Array<{ id: string; state: string }> }>('GET', `/api/dispatch/tickets/${ticket}/publications`)
  expect(after.publications.map((p) => p.state)).toEqual(['sent', 'sent'])
})

test('offers only the parts a composer may offer, the close for an issue alone, and no Publish for a preview that is not the bytes its digest names', async ({ browser }) => {
  test.setTimeout(test.info().timeout + 3 * PREPARE_MS)
  gh.set({ n: PULL, kind: 'pull', title: 'Fix the empty list', body: 'Fixes #12', state: 'open', headSha: 'b'.repeat(40) })
  await srv.api.ok('POST', `/api/dispatch/tickets/${ticket}/links`, { opId: opOf(KEY, 'pr'), kind: 'delivery', url: gh.url(PULL, 'pull'), note: 'the fix' })
  const page = await ticketPage(browser)
  // An offer that holds what no composer may insert: a comment, the plan's text.
  await page.route('**/publish/offer', async (route) => {
    const res = await route.fetch()
    const offer = (await res.json()) as { parts: Array<{ name: string; markdown: string }> }
    offer.parts.push({ name: 'comments', markdown: 'A private comment' }, { name: 'plan', markdown: 'The plan, whole' })
    await route.fulfill({ response: res, json: offer })
  })
  const dialog = await openComposer(page)
  await expect(dialog.locator('[data-publish-part]')).toHaveCount(2)
  await expect(dialog.locator('[data-publish-part-text="deliveries"]')).toHaveText(`Delivered in:\n\n- ${gh.url(PULL, 'pull')} the fix`)
  await expect(dialog).not.toContainText('A private comment')
  await expect(dialog).not.toContainText('The plan, whole')
  for (const name of ['The delivery links', "The ticket's key and title"]) await expect(dialog.getByRole('checkbox', { name })).not.toBeChecked()

  // The close is offered for the issue, and not for the pull request.
  const close = dialog.getByRole('checkbox', { name: 'Close the issue as completed' })
  await expect(close).toBeVisible()
  await dialog.locator('[data-publish-target]').click()
  await page.getByRole('option', { name: `The link to ${gh.owner}/${gh.repo}#${PULL}` }).click()
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await expect(dialog.locator('[data-publish-target]')).toContainText(`#${PULL}`)
  await expect(close).toHaveCount(0)

  // A preview that is not the bytes its digest names: drawn, never offered to send; the draft it made is discarded as the person leaves.
  await page.route('**/api/dispatch/tickets/*/publications', async (route) => {
    if (route.request().method() !== 'POST') return route.fallback()
    const res = await route.fetch()
    const got = (await res.json()) as { preview: string }
    got.preview = got.preview.replace('Merged at last.', 'Merged at last, and more.')
    await route.fulfill({ response: res, json: got })
  })
  await dialog.locator('[data-publish-body]').fill('Merged at last.')
  await dialog.locator('[data-publish-prepare]').click()
  await expect(dialog.locator('[data-publish-preview]')).toContainText('Merged at last, and more.', { timeout: PREPARE_MS })
  await expect(dialog.locator('[data-publish-blocked]')).toHaveText('The preview is not the bytes its digest names, so it is not offered to send: close this and prepare it again.')
  await expect(dialog.locator('[data-publish-send]')).toHaveCount(0)
  const drafts = await srv.api.ok<{ publications: Array<{ id: string; state: string; target: string }> }>('GET', `/api/dispatch/tickets/${ticket}/publications`)
  const draft = drafts.publications[0]!
  expect([draft.state, draft.target.startsWith('link:')]).toEqual(['draft', true])
  await dialog.locator('[data-publish-back]').click()
  await expect.poll(async () => (await publicationOf(draft.id)).publication.state, { timeout: 15_000 }).toBe('discarded')
  expect(gh.count('comment')).toBe(2)
})
