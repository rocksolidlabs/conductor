import { execFileSync } from 'node:child_process'
import { chmodSync, existsSync, mkdirSync, readdirSync, readFileSync, statSync, symlinkSync, utimesSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, test } from './fixtures'
import { agentFreePath, CATALOG_PROGRAMS, renderConfig, repoRoot, serverPath, START_WAIT_MS, startServer, stopServer, stubBash, type Started } from './server'

// #961: an e2e server ran with the caller's PATH, so on a machine with an agent CLI installed the built-in catalog found it and the
// server ran its version probe with the test's HOME; stopped soon after it started, the server left the probe running, and it wrote
// into the test's folder after the folder was removed. Every program the built-in catalog names is faked here on a PATH the test
// gives: a fake writes its pid as it starts, then waits on a FIFO nothing writes; the test ends what is left by its process group.
// This spec proves that a test server's PATH holds no agent CLI, and the control that a server given the agent CLIs finds the fakes,
// runs their probes, and never runs a program of a catalog name behind them, so the machine's real CLIs are never run: the fakes come
// first on that PATH, ahead of any directory stubBash puts there for its bash, and cover every program the built-in catalog names
// before its server starts. It does not prove that a server's stop ends its probes: a probe ends by itself at the product's own bound
// (3 s), which a spec cannot move, so a probe gone after the stop says nothing of the stop. The Go tests prove that
// (internal/api/probe_stop_test.go, internal/agents/probe_group_test.go), with a probe whose bound is an hour.

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
  const o = { config, home, data, allowedRoot: root, defaultCwd: root, log: join(root, 'server.log'), first: bin }
  return { o, bin, marks, hold }
}

/** The programs the built-in catalog's source runs by a bare name, read from internal/catalog/defaults.go: nothing is run. */
function builtInPrograms(): string[] {
  const source = readFileSync(join(repoRoot, 'internal', 'catalog', 'defaults.go'), 'utf8')
  return [...source.matchAll(/Command:\s*\[\]string\{"([^"]+)"/g)].map((m) => m[1]!).filter((program) => !program.includes('/'))
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

// The control: with the agent CLIs on its PATH (the live check's server) the server finds the fakes and runs their probes, which the
// test above would see. A program of each catalog name put behind the fakes stands for the machine's real CLI: none is ever found or run.
test('a server given the agent CLIs finds the fakes ahead of them, runs their probes, and never runs a program behind them', async ({ state }) => {
  // The start, the two polls' bounds and the stop's: each bounds a hang.
  test.setTimeout(START_WAIT_MS + 30_000 + 30_000 + 30_000)
  const { o, bin, marks } = own(state, 'control')
  const behind = join(dirname(bin), 'behind')
  const behindMarks = join(dirname(bin), 'behind-marks')
  for (const d of [behind, behindMarks]) mkdirSync(d, { recursive: true })
  for (const program of CATALOG_PROGRAMS) writeFileSync(join(behind, program), `#!/bin/sh\ntouch ${quote(join(behindMarks, program))}\n`, { mode: 0o755 })
  const first = `${bin}:${behind}`
  // Before a server is given the caller's PATH: the fakes cover every program the built-in catalog runs by name (one added later and
  // not faked fails here, before its real CLI could be found behind them), and they come first on the PATH this server is given.
  const named = builtInPrograms()
  expect(named.length, 'no program read from internal/catalog/defaults.go').toBeGreaterThan(0)
  for (const program of named) expect(CATALOG_PROGRAMS, `the built-in catalog names ${program}: add it to CATALOG_PROGRAMS in web/e2e/server.ts`).toContain(program)
  expect(serverPath({ ...o, first, realAgents: true }, stubBash().path).split(':').slice(0, 2)).toEqual([bin, behind])
  const s = await startServer({ ...o, first, realAgents: true })
  servers.push(s)

  // Every catalog name is found among the fakes, none behind them.
  for (const program of CATALOG_PROGRAMS) {
    const found = await call<{ found: boolean; path?: string }>(s, 'POST', '/api/catalog/check', { command: [program] })
    expect(found.path, `${program} is not found among the fakes`).toBe(join(bin, program))
  }
  // The quick read starts the lookups and the probes and waits for none; Copilot's probe is the fake's.
  await call(s, 'GET', '/api/catalog?wait=0')
  const mark = join(marks, 'copilot')
  await expect.poll(async () => {
    if (!existsSync(mark)) await call(s, 'GET', '/api/catalog?wait=0')
    return existsSync(mark)
  }, { timeout: 30_000 }).toBe(true)

  await stopServer(servers.pop()!.pid)
  expect(readdirSync(behindMarks), 'a program behind the fakes was run').toEqual([])
})

// The PATH's building, with no server started.

/** A folder of the test's own under the suite's root. */
function scratch(state: { root: string }, name: string): string {
  const dir = join(state.root, 'agent-path', `${name}-${test.info().repeatEachIndex}`)
  mkdirSync(dir, { recursive: true })
  return dir
}

const program = (dir: string, name: string) => writeFileSync(join(dir, name), '#!/bin/sh\nexit 0\n', { mode: 0o755 })

test('the fakes come first on a server\'s PATH, ahead of a directory stubBash puts there for its bash', ({ state }) => {
  const home = scratch(state, 'first')
  // The shape stubBash returns on a machine whose first bash cannot run the stub: its bash's directory ahead of the caller's PATH.
  const chosen = `${join(home, 'homebrew-bin')}:/usr/bin:/bin`
  expect(serverPath({ home, first: '/fakes', realAgents: true }, chosen)).toBe(`/fakes:${chosen}`)
  expect(serverPath({ home, first: '/fakes' }, chosen).split(':')[0]).toBe('/fakes')
})

test('a PATH directory changed since it was judged is judged again: an agent CLI put in it is taken off, a tool put in it is found', ({ state }) => {
  const root = scratch(state, 'judged')
  const dir = join(root, 'bin')
  const into = join(root, 'links')
  mkdirSync(dir, { recursive: true })
  // Each change is dated well past (a judgement and its links are kept only then), each later than the one before, as changes are.
  const at = (secondsAgo: number) => {
    const t = new Date(Date.now() - secondsAgo * 1000)
    utimesSync(dir, t, t)
  }
  program(dir, 'tool-a')
  at(90)
  expect(agentFreePath(dir, into), 'a clean directory is handed on as it is').toBe(dir)
  program(dir, 'goose')
  at(60)
  const links = agentFreePath(dir, into)
  expect(links).not.toBe(dir)
  expect(readdirSync(links).sort()).toEqual(['tool-a'])
  expect(agentFreePath(dir, into), 'the links of an unchanged directory are kept').toBe(links)
  program(dir, 'tool-b')
  at(30)
  expect(readdirSync(agentFreePath(dir, into)).sort()).toEqual(['tool-a', 'tool-b'])
  // A change dated now, within the file system's tick, is judged at every call.
  program(dir, 'tool-c')
  expect(readdirSync(agentFreePath(dir, into)).sort()).toEqual(['tool-a', 'tool-b', 'tool-c'])
})

test('a PATH directory is judged by its entries: a catalog program\'s link whose target is missing takes it off from the first call', ({ state }) => {
  const root = scratch(state, 'dangling')
  const dir = join(root, 'bin')
  const targets = join(root, 'targets')
  const into = join(root, 'links')
  for (const d of [dir, targets]) mkdirSync(d, { recursive: true })
  program(dir, 'tool-a')
  // A tool the suite needs, reached by a link: kept. The catalog program's link points at nothing just now.
  program(targets, 'needed')
  symlinkSync(join(targets, 'needed'), join(dir, 'needed'))
  symlinkSync(join(targets, 'copilot'), join(dir, 'copilot'))
  // Dated well past, so the judgement and its links are kept.
  const t = new Date(Date.now() - 90_000)
  utimesSync(dir, t, t)
  const mtime = statSync(dir, { bigint: true }).mtimeNs
  const links = agentFreePath(dir, into)
  expect(links, 'a directory with a dangling catalog link is handed on as it is').not.toBe(dir)
  expect(readdirSync(links).sort()).toEqual(['needed', 'tool-a'])
  // The target comes back elsewhere: the directory itself is unchanged, and still filtered.
  program(targets, 'copilot')
  expect(statSync(dir, { bigint: true }).mtimeNs).toBe(mtime)
  expect(agentFreePath(dir, into)).toBe(links)
  expect(readdirSync(links).sort()).toEqual(['needed', 'tool-a'])
})

test('a PATH directory that holds a catalog program and cannot be listed fails the server\'s start, naming the cause', ({ state }) => {
  test.skip(process.getuid?.() === 0, 'root lists any directory')
  const root = scratch(state, 'locked')
  const dir = join(root, 'bin')
  mkdirSync(dir, { recursive: true })
  program(dir, 'claude')
  chmodSync(dir, 0o311)
  try {
    expect(() => agentFreePath(dir, join(root, 'links'))).toThrow(/holds claude .* cannot be listed \(EACCES\)/)
  } finally {
    chmodSync(dir, 0o755)
  }
})
