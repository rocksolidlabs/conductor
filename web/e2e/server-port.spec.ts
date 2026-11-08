import { mkdirSync, writeFileSync } from 'node:fs'
import { createServer, type Server } from 'node:net'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, test } from './fixtures'
import { HEALTH_WAIT_MS, renderConfig, startServer, STOP_WAIT_MS, stopServer, type Started } from './server'

// A port probed free is not a port owned (#281): another run of the suite on
// the machine can bind it before startServer's child does, and a health answer
// there is that server's, whose token is not the child's. startServer is
// handed, as its first try's port, one a server it started holds: it must not
// return that stranger, but land on a port of its own whose server takes the
// token it returned. A try that loses its port ends its child on SIGTERM and awaits it before the next try starts over the same
// data directory: a child killed mid-start can leave restore.lock empty, which refuses every later serve there.

const servers: Started[] = []
const holders: Server[] = []

test.afterEach(async () => {
  for (const s of servers.splice(0).reverse()) await stopServer(s.pid)
  for (const h of holders.splice(0)) await new Promise((r) => h.close(r))
})

const here = dirname(fileURLToPath(import.meta.url))
const at = (state: { root: string }, name: string) => {
  const root = join(state.root, 'server-port', `${name}-${test.info().repeatEachIndex}`)
  const home = join(root, 'home')
  const data = join(root, 'data')
  for (const d of [home, data]) mkdirSync(d, { recursive: true })
  const config = join(root, 'conductor.json')
  renderConfig(join(here, 'conductor.e2e.json'), config)
  return { config, home, data, allowedRoot: root, defaultCwd: root, log: join(root, 'server.log') }
}
const whoami = async (s: Started, token: string) => (await fetch(`${s.baseURL}/api/whoami`, { headers: { Authorization: `Bearer ${token}` } })).status
// Each startServer below: its tries' health waits, and the lost try's stop.
const budget = (starts: number) => starts * 2 * HEALTH_WAIT_MS + STOP_WAIT_MS + 30_000

test('startServer handed a port another server holds returns its own server, on another port', async ({ state }) => {
  test.setTimeout(budget(2))
  const stranger = await startServer(at(state, 'stranger'))
  servers.push(stranger)
  const mine = await startServer({ ...at(state, 'mine'), port: stranger.port })
  servers.push(mine)

  expect(mine.port).not.toBe(stranger.port)
  // The lost try's child ended on SIGTERM (or had ended on its own, on the address in use) before the retry over its data
  // directory, which then started: never killed mid-start.
  expect(mine.lost).toHaveLength(1)
  expect(mine.lost![0]).toMatch(new RegExp(`^port ${stranger.port} .*the child (ended on SIGTERM|had ended)`))
  expect(mine.lost![0]).not.toMatch(/killed|SIGKILL/)
  expect(mine.pid).not.toBe(stranger.pid)
  expect(await whoami(mine, mine.token)).toBe(200)
  // The stranger still holds its port and refuses the token startServer made for the child.
  expect(await whoami(stranger, mine.token)).toBe(401)
  expect(await whoami(stranger, stranger.token)).toBe(200)
})

test('a child that exits on the address in use is read from the bytes it appended, and the next try starts', async ({ state }) => {
  test.setTimeout(budget(1))
  // A listener that is no server: every connection is closed at once, so no health answers there and the child's bind fails.
  const holder = createServer((c) => c.destroy())
  holders.push(holder)
  await new Promise<void>((r) => holder.listen(0, '127.0.0.1', () => r()))
  const held = (holder.address() as { port: number }).port
  const o = at(state, 'bytes')
  // Text before the try's in a script of two bytes a character: a cut by characters at the try's byte offset would read past the
  // end of the log and miss the child's "address already in use".
  writeFileSync(o.log, 'é'.repeat(100_000) + '\n')

  const mine = await startServer({ ...o, port: held })
  servers.push(mine)

  expect(mine.port).not.toBe(held)
  expect(mine.lost).toHaveLength(1)
  expect(mine.lost![0]).toMatch(new RegExp(`^port ${held} was in use when the server bound it`))
  expect(await whoami(mine, mine.token)).toBe(200)
})
