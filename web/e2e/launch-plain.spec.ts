import { expect, openMore, test } from './fixtures'

// A workbench opened over plain http from another machine. Chromium is told
// that `conductor.test` is this computer (a host resolver rule), so the page
// at http://conductor.test:<port> is the e2e server seen under a name that is
// not localhost or a loopback address, as a LAN address is. The `conductor
// host` command the launch dialog offers for "On this computer" then carries
// --insecure, which the host needs for such a server (it refuses one without
// it), and the dialog says why (#213). At this computer's own address the
// question is not asked at all: launch.spec.ts.
test.use({ launchOptions: { args: ['--no-sandbox', '--host-resolver-rules=MAP conductor.test 127.0.0.1'] } })

test('on a plain workbench from another machine the host command carries --insecure and the dialog says why', async ({ page, state }) => {
  const origin = `http://conductor.test:${state.port}`
  await page.goto(`${origin}/`)
  await page.getByRole('button', { name: 'Launch agent' }).first().click()
  const dialog = page.getByRole('dialog')
  // Where it runs is the first item of More options.
  await openMore(dialog)
  await expect(dialog.locator('[data-runs-on]')).toBeVisible()
  await dialog.getByRole('button', { name: 'On this computer' }).click()
  await dialog.getByLabel('Name', { exact: true }).fill('plain-host')
  const command = dialog.locator('[data-code-block] li').first()
  await expect(command).toContainText(`conductor host --server ${origin} --token `)
  await expect(command).toContainText(' --insecure --name plain-host ')
  const notice = dialog.locator('[data-insecure-notice]')
  await expect(notice).toBeVisible()
  await expect(notice).toContainText('--insecure')
  await expect(notice).toContainText('in the clear')
})
