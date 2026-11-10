import { spawn, type ChildProcess } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { openSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { endHosted, expect, hostedName, hostedSession, test } from './fixtures'
import { serverBinary } from './server'

// A hosted session's record (Program item HS1, docs/plans/history.md, PR-12): `conductor host`, native, in a folder that is no
// repository and with no agent it can probe, reports where it ran and nothing else of its launch; its record page says it ran
// natively and names what its host did not report, never this server's facts in their place.
let host: ChildProcess | null = null
let hostName = ''

test.afterAll(async ({ api }) => {
  await endHosted(api, hostName, host)
})

test('a hosted record says where its host ran it and what its host did not report', async ({ api, state, page }) => {
  const run = randomUUID()
  const start = join(state.root, `history-hosted-start-${run}`)
  const logFile = join(state.root, `history-hosted-${run}.log`)
  const log = openSync(logFile, 'a')
  hostName = hostedName('history-hosted')
  host = spawn(serverBinary(), ['host', '--server', state.baseURL, '--token', state.hostToken, '--host-name', 'e2e-laptop', '--name', hostName, '--cwd', state.home, '--relay-only', '--no-local', '--', '/bin/sh', '-c', 'while [ ! -e "$1" ]; do sleep 0.05; done', 'sh', start], {
    env: { PATH: process.env.PATH ?? '/usr/bin:/bin', LANG: 'C.UTF-8', HOME: state.home, GIT_CEILING_DIRECTORIES: state.home },
    stdio: ['ignore', log, log],
  })
  const id = (await hostedSession(api, logFile, hostName)).id
  writeFileSync(start, '')
  await expect.poll(async () => (await api.ok<{ record: { endedAt?: string } }>('GET', `/api/history/sessions/${id}`)).record.endedAt ?? '', { timeout: 20_000 }).not.toBe('')

  await page.goto(`/history/sessions/${id}`)
  const recordPage = page.locator(`[data-record-page][data-record-id="${id}"]`)
  await expect(recordPage.locator('[data-record-where]')).toHaveText(/^\s*·\s*ran natively$/)
  await expect(recordPage.locator('[data-record-unreported]')).toHaveText("by its host: the agent's version, the repository, the start commit")
  await expect(recordPage.locator('[data-record-ending]')).toHaveText(/exited/)
})
