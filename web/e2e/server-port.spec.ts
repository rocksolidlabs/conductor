import { mkdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, test } from './fixtures'
import { renderConfig, startServer, stopServer, type Started } from './server'

// A port probed free is not a port owned (#281): another run of the suite on
// the machine can bind it before startServer's child does, and a health answer
// there is that server's, whose token is not the child's. startServer is
// handed, as its first try's port, one a server it started holds: it must not
// return that stranger, but land on a port of its own whose server takes the
// token it returned.

const servers: Started[] = []

test.afterEach(async () => {
  for (const s of servers.splice(0).reverse()) await stopServer(s.pid)
})

test('startServer handed a port another server holds returns its own server, on another port', async ({ state }) => {
  const here = dirname(fileURLToPath(import.meta.url))
  const at = (name: string) => {
    const root = join(state.root, 'server-port', `${name}-${test.info().repeatEachIndex}`)
    const home = join(root, 'home')
    const data = join(root, 'data')
    for (const d of [home, data]) mkdirSync(d, { recursive: true })
    const config = join(root, 'conductor.json')
    renderConfig(join(here, 'conductor.e2e.json'), config)
    return { config, home, data, allowedRoot: root, defaultCwd: root, log: join(root, 'server.log') }
  }
  const whoami = async (s: Started, token: string) => (await fetch(`${s.baseURL}/api/whoami`, { headers: { Authorization: `Bearer ${token}` } })).status

  const stranger = await startServer(at('stranger'))
  servers.push(stranger)
  const mine = await startServer({ ...at('mine'), port: stranger.port })
  servers.push(mine)

  expect(mine.port).not.toBe(stranger.port)
  expect(mine.pid).not.toBe(stranger.pid)
  expect(await whoami(mine, mine.token)).toBe(200)
  // The stranger still holds its port and refuses the token startServer made for the child.
  expect(await whoami(stranger, mine.token)).toBe(401)
  expect(await whoami(stranger, stranger.token)).toBe(200)
})
