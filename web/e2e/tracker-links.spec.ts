import { expect, test } from '@playwright/test'
import { fakeGitHub, type FakeGitHub } from './fake-github'
import { makeProject, makeTicket, opOf } from './dispatch-helpers'
import { ownServer, type OwnServer } from './own-server'

// GitHub's links read (Program item IN1a, docs/plans/tracker-linking.md, PR-8), against the e2e's fake GitHub (fake-github.ts) on a
// server of the spec's own in loopback mode: a delivery link to a pull request read as open, then merged; the project's policy `merged`;
// Close refused, then met. And first the fake itself, as the server's adapter asks it: every route answered, the token sent to the API
// host alone, never with an image.

const TOKEN = 'ghp_e2eFakeToken0123456789abcdefLINKS'
const PNG = Buffer.from('89504e470d0a1a0a0000000d4948445200000001000000010806000000', 'hex')

let srv: OwnServer
let gh: FakeGitHub

test.beforeAll(async () => {
  gh = await fakeGitHub()
  srv = await ownServer('tracker-links', { env: { CONDUCTOR_TRACKER_LOOPBACK: '1' } })
  await srv.api.ok('POST', '/api/trackers/github', { host: gh.host, token: TOKEN })
})

test.afterAll(async () => {
  await gh?.stop()
  await srv?.stop()
})

test("the fake answers what the server's adapter asks, and the token goes to its API alone", async () => {
  const api = srv.api
  const listed = await api.ok<{ github: Array<{ host: string; login: string; state: string }> }>('GET', '/api/trackers')
  expect(listed.github.find((e) => e.host === gh.host)).toMatchObject({ login: 'mona-example', state: 'ok' })
  gh.set({ n: 7, kind: 'issue', title: 'The list crashes when empty', body: 'It crashes.\n\n### Steps to reproduce\n\n1. Open the list\n2. Empty it\n', state: 'open', labels: ['bug'], images: [{ path: 'a.png', type: 'image/png', bytes: PNG }, { path: 'b.png', type: 'image/png', bytes: PNG }] })
  const { preview } = await api.ok<{ preview: { title: string; images: string[]; repro?: string; state: string; labels: string[] } }>('POST', '/api/dispatch/external/preview', { url: gh.url(7) })
  expect(preview).toMatchObject({ title: 'The list crashes when empty', state: 'open', labels: ['bug'] })
  expect(preview.images).toEqual([`http://${gh.host}/_img/7/a.png`, `http://${gh.host}/_img/7/b.png`])
  expect(preview.repro).toContain('Open the list')
  const key = 'FKG'
  await makeProject(api, key)
  const imported = await api.ok<{ ticket: { key: string; attachments: Array<{ contentType: string }> }; images: Array<{ attachmentId?: string }> }>('POST', `/api/dispatch/projects/${key}/import`, { opId: opOf(key, 'import'), url: gh.url(7) })
  expect(imported.images.filter((i) => i.attachmentId)).toHaveLength(2)
  expect(gh.count('image')).toBe(2)
  // A pull request's read, its checks at its head commit.
  gh.set({ n: 8, kind: 'pull', title: 'Fix the empty list', body: 'Fixes #7', state: 'open', headSha: 'a'.repeat(40) })
  gh.checks('a'.repeat(40), [
    { kind: 'job', id: 2000001, name: 'test', status: 'completed', conclusion: 'success', log: 'ok\n' },
    { kind: 'run', id: 2100001, name: 'lint', status: 'completed', conclusion: 'failure' },
    { kind: 'status', id: 3000001, name: 'ci/build', status: 'completed', conclusion: 'success' },
  ])
  const ticket = await makeTicket(api, key, '2', { title: 'A delivery' })
  const added = await api.ok<{ ticket: { links: Array<{ id: string; url: string }> } }>('POST', `/api/dispatch/tickets/${ticket}/links`, { opId: opOf(key, 'l1'), kind: 'delivery', url: gh.url(8, 'pull') })
  const link = added.ticket.links.find((l) => l.url === gh.url(8, 'pull'))!
  const { read } = await api.ok<{ read: { state: string; merged: boolean; checks: Array<{ id: string; conclusion: string }> } }>('POST', `/api/dispatch/tickets/${ticket}/links/${link.id}/read`, {})
  expect(read.state).toBe('open')
  expect(read.checks.map((c) => c.id).sort()).toEqual(['job:2000001', 'run:2100001', 'status:3000001'])
  // The token went in the API's requests' header alone: never with an image, never anywhere but the fake's API routes.
  const reqs = gh.requests()
  expect(reqs.filter((r) => r.route === 'image').every((r) => r.token === '')).toBe(true)
  expect(reqs.filter((r) => r.route !== 'image').every((r) => r.token === TOKEN)).toBe(true)
})
