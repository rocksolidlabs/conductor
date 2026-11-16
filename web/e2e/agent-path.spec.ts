import { execFileSync } from 'node:child_process'
import { constants, closeSync, existsSync, mkdirSync, openSync, readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, test } from './fixtures'
import { CATALOG_PROGRAMS, renderConfig, START_WAIT_MS, startServer, stopServer, type Started } from './server'

// #961: an e2e server ran with the caller's PATH, so on a machine with an agent CLI installed the built-in catalog found it and the
// server ran its version probe with the test's HOME; stopped soon after it started, the server left the probe running, and it wrote
// into the test's folder after the folder was removed. Every program the built-in catalog names is faked here on a PATH the test
// gives: a fake writes its pid as it starts, then waits on a FIFO nothing writes, so it is still there when the server stops unless
// the stop ends it. The real CLIs of the machine are never run: the fakes come first on that PATH.

const here = dirname(fileURLToPath(import.meta.url))
const servers: Started[] = []
const fakes: string[] = []

// The fakes left are ended by their process group (a probe leads one) before the folders go: the test ends what it started.
test.afterEach(async () => {
  for (const s of servers.splice(0).reverse()) await stopServer(s.pid)
  for (const marks of fakes.splice(0)) {
    for (const pid of ranPids(marks)) {
      try {
        process.kill(-pid, 'SIGKILL')
      } catch {
        /* gone */
      }
    }
  }
})

/** The pids the fakes that ran wrote under marks: one file per program run, named after it. */
function ranPids(marks: string): number[] {
  return readdirSync(marks)
    .filter((f) => CATALOG_PROGRAMS.includes(f))
    .map((f) => Number(readFileSync(join(marks, f), 'utf8').trim()))
    .filter((pid) => pid > 0)
}

/** The code of the error f throws, or 'no error'. */
function fails(f: () => unknown): string {
  try {
    f()
    return 'no error'
  } catch (e) {
    return (e as NodeJS.ErrnoException).code ?? String(e)
  }
}

const quote = (s: string) => `'${s.replaceAll("'", `'\\''`)}'`

/** A server of the test's own: its folder, the fakes' bin (with e2e-tool, a program no catalog names, beside them) and their marks. */
function own(state: { root: string }, name: string) {
  const root = join(state.root, 'agent-path', `${name}-${test.info().repeatEachIndex}`)
  const home = join(root, 'home')
  const data = join(root, 'data')
  const bin = join(root, 'bin')
  const marks = join(root, 'marks')
  for (const d of [home, data, bin, marks]) mkdirSync(d, { recursive: true })
  const hold = join(root, 'hold')
  execFileSync('mkfifo', [hold])
  for (const program of CATALOG_PROGRAMS) {
    const mark = join(marks, program)
    const fake = [
      '#!/bin/sh',
      `echo $$ > ${quote(mark + '.new')} && mv ${quote(mark + '.new')} ${quote(mark)}`,
      `read line < ${quote(hold)}`,
      `echo late > ${quote(mark + '.late')}`,
      `echo '${program} 1.0.0'`,
    ]
    writeFileSync(join(bin, program), fake.join('\n') + '\n', { mode: 0o755 })
  }
  writeFileSync(join(bin, 'e2e-tool'), '#!/bin/sh\nexit 0\n', { mode: 0o755 })
  fakes.push(marks)
  const config = join(root, 'conductor.json')
  renderConfig(join(here, 'conductor.e2e.json'), config)
  const o = { config, home, data, allowedRoot: root, defaultCwd: root, log: join(root, 'server.log'), path: `${bin}:${process.env.PATH ?? '/usr/bin:/bin'}` }
  return { o, bin, marks, hold }
}

interface Entry {
  id: string
  source: string
  command: string[]
  available: boolean
  identity?: { ran: boolean }
  checking?: boolean
}

async function call<T>(s: Started, method: string, path: string, body?: unknown): Promise<T> {
  const r = await fetch(`${s.baseURL}${path}`, {
    method,
    headers: { Authorization: `Bearer ${s.token}`, ...(body ? { 'Content-Type': 'application/json' } : {}) },
    body: body ? JSON.stringify(body) : undefined,
  })
  expect(r.ok, `${method} ${path}: ${r.status}`).toBe(true)
  return (await r.json()) as T
}

const builtIns = (agents: Entry[]) => agents.filter((a) => a.source === 'built-in')

test('a test server finds none of the built-in catalog\'s programs, and a fake agent CLI on the caller\'s PATH is never run', async ({ state }) => {
  test.setTimeout(START_WAIT_MS + 60_000)
  const { o, bin, marks } = own(state, 'hidden')
  const s = await startServer(o)
  servers.push(s)

  // The lookups answered (a slow PATH entry can keep one past the read's wait): every built-in agent is missing, never probed.
  let agents: Entry[] = []
  await expect
    .poll(async () => {
      agents = (await call<{ agents: Entry[] }>(s, 'GET', '/api/catalog')).agents
      return builtIns(agents).filter((a) => a.checking).map((a) => a.id)
    }, { timeout: 30_000 })
    .toEqual([])
  for (const a of builtIns(agents)) {
    const program = a.command[0]
    if (program.includes('/')) continue
    // The list is the built-in catalog's: a program it names that the list does not is one a test server would find.
    expect(CATALOG_PROGRAMS, `the built-in catalog names ${program}: add it to CATALOG_PROGRAMS in web/e2e/server.ts`).toContain(program)
    expect(a.available, `${a.id} (${program}) is found on a test server's PATH`).toBe(false)
    expect(a.identity, `${a.id} (${program}) was probed`).toBeUndefined()
  }
  expect(builtIns(agents).map((a) => a.id)).toContain('copilot')
  // The directory the fakes are in is still on the PATH, less them: e2e-tool beside them is found, in the server's links.
  const tool = await call<{ found: boolean; path?: string }>(s, 'POST', '/api/catalog/check', { command: ['e2e-tool'] })
  expect(tool.found).toBe(true)
  expect(tool.path).not.toBe(join(bin, 'e2e-tool'))
  expect((await call<{ found: boolean }>(s, 'POST', '/api/catalog/check', { command: ['copilot'] })).found).toBe(false)

  await stopServer(servers.pop()!.pid)
  expect(readdirSync(marks), 'a fake agent CLI was run').toEqual([])
})

// The control, and the stop's own rule: with the agent CLIs on its PATH (the live check's server) the server finds the fakes and runs
// their probes, which the test above would see; its stop ends every probe under way, so none is left to write after it.
test('a server stopped while its probes run leaves none of them running', async ({ state }) => {
  test.setTimeout(START_WAIT_MS + 60_000)
  const { o, marks, hold } = own(state, 'stopped')
  const s = await startServer({ ...o, realAgents: true })
  servers.push(s)

  // The quick read starts the lookups and the probes and waits for none; Copilot's probe is the fake's, which waits on the FIFO.
  await call(s, 'GET', '/api/catalog?wait=0')
  const mark = join(marks, 'copilot')
  await expect.poll(async () => {
    if (!existsSync(mark)) await call(s, 'GET', '/api/catalog?wait=0')
    return existsSync(mark)
  }, { timeout: 30_000 }).toBe(true)

  await stopServer(servers.pop()!.pid)

  const ran = ranPids(marks)
  expect(ran.length).toBeGreaterThan(0)
  for (const pid of ran) expect(fails(() => process.kill(pid, 0)), `a probe (pid ${pid}) is still running after the server's stop`).toBe('ESRCH')
  // Nothing reads the FIFO any more, so nothing can go on to write.
  expect(fails(() => closeSync(openSync(hold, constants.O_WRONLY | constants.O_NONBLOCK))), 'a probe still reads its FIFO after the server\'s stop').toBe('ENXIO')
  expect(readdirSync(marks).filter((f) => f.endsWith('.late'))).toEqual([])
})
