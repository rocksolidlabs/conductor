import { spawn, type ChildProcess } from 'node:child_process'
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { describe, expect, it, vi } from 'vitest'
import type { Launcher } from '../src/launcher'
import { FolderAllow } from '../src/allow'
import { SettingsSaver } from '../src/save'
import { loopbackOrigin, ServerSupervisor, StartStopped, trustedOrigin } from '../src/server'
import { defaultSettings } from '../src/settings'

/** A fake server: a node script that prints the handshake (after optional noise), serves /api/health, and exits when stdin closes. */
function fakeServer(opts: { noise?: boolean; crashAfterMs?: number; silent?: boolean }): string {
  const dir = mkdtempSync(join(tmpdir(), 'cd-fake-'))
  const file = join(dir, 'server.js')
  writeFileSync(
    file,
    `
const http = require('http')
const srv = http.createServer((req, res) => { res.end(JSON.stringify({ ok: true, instance: 'x' })) })
srv.listen(0, '127.0.0.1', () => {
  const port = srv.address().port
  if (${!!opts.noise}) process.stdout.write('Welcome to the shell\\n')
  if (!${!!opts.silent}) process.stdout.write(JSON.stringify({ listen: '127.0.0.1:' + port, publicUrl: 'http://127.0.0.1:' + port, pid: process.pid, version: 'fake', workbenchToken: process.env.CONDUCTOR_WORKBENCH_TOKEN }) + '\\n')
  process.stderr.write('serving\\n')
  if (${opts.crashAfterMs ?? 0}) setTimeout(() => process.exit(3), ${opts.crashAfterMs ?? 0})
})
process.stdin.on('end', () => { srv.close(); process.exit(0) })
process.stdin.resume()
`,
  )
  return file
}

class ScriptLauncher implements Launcher {
  spawned = 0
  constructor(private script: string) {}
  describe() {
    return 'fake'
  }
  prepare() {
    return Promise.resolve(this.script)
  }
  spawn(args: string[], env: Record<string, string>): ChildProcess {
    this.spawned++
    return spawn(process.execPath, [this.script, ...args], { env, stdio: ['pipe', 'pipe', 'pipe'] })
  }
  kill(child: ChildProcess) {
    child.kill('SIGKILL')
  }
}

const quiet = () => {}

describe('server supervisor', () => {
  it('starts the server, reads its handshake past the shell\'s noise, and stops it by closing stdin', async () => {
    const launcher = new ScriptLauncher(fakeServer({ noise: true }))
    const lines: string[] = []
    // beforeStart runs before the environment is built, at each start (#363: the shell's PATH read again).
    const order: string[] = []
    const s = new ServerSupervisor({
      launcher,
      args: ['serve'],
      beforeStart: async () => {
        order.push('before')
      },
      env: () => {
        order.push('env')
        return { ...process.env, CONDUCTOR_WORKBENCH_TOKEN: 'tok' } as Record<string, string>
      },
      log: (k, l) => lines.push(`${k}: ${l}`),
    })
    const h = await s.start()
    expect(order).toEqual(['before', 'env'])
    expect(h.workbenchToken).toBe('tok')
    expect(s.status.state).toBe('running')
    expect(s.status.url).toBe(h.publicUrl)
    const res = await fetch(h.publicUrl + '/api/health')
    expect(res.ok).toBe(true)
    expect(lines).toContain('server: Welcome to the shell')
    await s.stop()
    expect(s.status.state).toBe('stopped')
    await expect(fetch(h.publicUrl + '/api/health')).rejects.toThrow()
  })
  it('writes the first line on the server\'s stdin before anything else, and its secrets go in no environment', async () => {
    // A server that reads one line of stdin before it listens, and writes down what it read and what its environment held.
    const dir = mkdtempSync(join(tmpdir(), 'cd-fake-'))
    const seen = join(dir, 'seen.json')
    const file = join(dir, 'server.js')
    writeFileSync(
      file,
      `
const http = require('http')
const fs = require('fs')
let buf = ''
const onData = (c) => {
  buf += c
  const i = buf.indexOf('\\n')
  if (i < 0) return
  process.stdin.off('data', onData)
  fs.writeFileSync(${JSON.stringify(seen)}, JSON.stringify({ line: buf.slice(0, i), env: Object.keys(process.env).filter((k) => k.startsWith('CONDUCTOR_')) }))
  const srv = http.createServer((req, res) => res.end('{}'))
  srv.listen(0, '127.0.0.1', () => {
    const port = srv.address().port
    process.stdout.write(JSON.stringify({ listen: '127.0.0.1:' + port, publicUrl: 'http://localhost:' + port, pid: process.pid, version: 'fake' }) + '\\n')
  })
  process.stdin.on('end', () => process.exit(0))
}
process.stdin.setEncoding('utf8')
process.stdin.on('data', onData)
`,
    )
    const s = new ServerSupervisor({
      launcher: new ScriptLauncher(file),
      args: [],
      env: () => ({ PATH: process.env.PATH ?? '', CONDUCTOR_DATA_DIR: '/d' }),
      firstLine: () => '{"workbenchToken":"tok","desktopKey":"key"}\n',
      log: quiet,
    })
    const h = await s.start()
    // The app's origin is the address the server bound, never the name in its publicUrl.
    expect(h.publicUrl).toMatch(/^http:\/\/localhost:/)
    expect(s.status.origin).toBe('http://' + h.listen)
    expect(trustedOrigin(s.status)).toBe('http://' + h.listen)
    const got = JSON.parse(readFileSync(seen, 'utf8')) as { line: string; env: string[] }
    expect(got.line).toBe('{"workbenchToken":"tok","desktopKey":"key"}')
    expect(got.env).toEqual(['CONDUCTOR_DATA_DIR'])
    await s.stop()
    // A server that runs no more is trusted no more.
    expect(trustedOrigin(s.status)).toBe('')
  })
  it('trusts the server no more from the moment its stop begins, before the server has exited', async () => {
    // A server that, told to stop, exits only once the test lets it.
    const dir = mkdtempSync(join(tmpdir(), 'cd-fake-'))
    const gate = join(dir, 'gate')
    const file = join(dir, 'server.js')
    writeFileSync(
      file,
      `
const http = require('http')
const fs = require('fs')
const srv = http.createServer((req, res) => res.end('{}'))
srv.listen(0, '127.0.0.1', () => {
  process.stdout.write(JSON.stringify({ listen: '127.0.0.1:' + srv.address().port, publicUrl: 'http://localhost:' + srv.address().port, pid: process.pid, version: 'fake' }) + '\\n')
})
process.stdin.on('end', () => {
  srv.close()
  const t = setInterval(() => { if (fs.existsSync(${JSON.stringify(gate)})) { clearInterval(t); process.exit(0) } }, 10)
})
process.stdin.resume()
`,
    )
    const s = new ServerSupervisor({ launcher: new ScriptLauncher(file), args: [], env: () => ({ PATH: process.env.PATH ?? '' }), stopGraceMs: 60_000, log: quiet })
    const h = await s.start()
    expect(trustedOrigin(s.status)).toBe('http://' + h.listen)
    const states: string[] = []
    s.on('state', (st) => states.push(st.state))
    const stopping = s.stop()
    // The stop is under way and the server has not exited: trusted no more all the same.
    expect(trustedOrigin(s.status)).toBe('')
    expect(states[0]).toBe('stopped')
    writeFileSync(gate, '')
    await stopping
    expect(trustedOrigin(s.status)).toBe('')
  })
  it('never trusts a server told to stop while it starts, whatever it prints after', async () => {
    // A server that prints its handshake only once the test lets it, told to stop before that, and exits a moment after.
    const dir = mkdtempSync(join(tmpdir(), 'cd-fake-'))
    const gate = join(dir, 'gate')
    const file = join(dir, 'server.js')
    writeFileSync(
      file,
      `
const http = require('http')
const fs = require('fs')
const srv = http.createServer((req, res) => res.end('{}'))
srv.listen(0, '127.0.0.1', () => {
  const t = setInterval(() => {
    if (!fs.existsSync(${JSON.stringify(gate)})) return
    clearInterval(t)
    process.stdout.write(JSON.stringify({ listen: '127.0.0.1:' + srv.address().port, publicUrl: 'http://localhost:' + srv.address().port, pid: process.pid, version: 'fake' }) + '\\n')
    setTimeout(() => process.exit(0), 200)
  }, 10)
})
process.stdin.resume()
`,
    )
    const s = new ServerSupervisor({ launcher: new ScriptLauncher(file), args: [], env: () => ({ PATH: process.env.PATH ?? '' }), stopGraceMs: 60_000, log: quiet })
    const states: string[] = []
    s.on('state', (st) => states.push(st.state))
    const started = s.start()
    started.catch(() => {})
    await vi.waitFor(() => expect(states).toContain('starting'))
    const stopping = s.stop()
    writeFileSync(gate, '')
    await expect(started).rejects.toBeInstanceOf(StartStopped)
    await stopping
    expect(states).not.toContain('running')
    expect(states).not.toContain('failed')
    expect(trustedOrigin(s.status)).toBe('')
  })
  it('a restart while the server starts reports the next start, and trusts only it', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'cd-fake-'))
    const gate = join(dir, 'gate')
    const file = join(dir, 'server.js')
    // The first spawn waits for the gate before its handshake; every later one prints at once.
    const count = join(dir, 'count')
    writeFileSync(count, '0')
    writeFileSync(
      file,
      `
const http = require('http')
const fs = require('fs')
const n = Number(fs.readFileSync(${JSON.stringify(count)}, 'utf8')) + 1
fs.writeFileSync(${JSON.stringify(count)}, String(n))
const srv = http.createServer((req, res) => res.end('{}'))
srv.listen(0, '127.0.0.1', () => {
  const print = () => process.stdout.write(JSON.stringify({ listen: '127.0.0.1:' + srv.address().port, publicUrl: 'http://localhost:' + srv.address().port, pid: process.pid, version: 'fake' }) + '\\n')
  if (n > 1) return print()
  const t = setInterval(() => { if (fs.existsSync(${JSON.stringify(gate)})) { clearInterval(t); print(); setTimeout(() => process.exit(0), 200) } }, 10)
})
process.stdin.on('end', () => { if (n > 1) process.exit(0) })
process.stdin.resume()
`,
    )
    const s = new ServerSupervisor({ launcher: new ScriptLauncher(file), args: [], env: () => ({ PATH: process.env.PATH ?? '' }), stopGraceMs: 60_000, log: quiet })
    const states: Array<{ state: string; pid?: number }> = []
    s.on('state', (st) => states.push({ state: st.state, pid: st.pid }))
    const started = s.start()
    started.catch(() => {})
    await vi.waitFor(() => expect(states.map((x) => x.state)).toContain('starting'))
    // The restart: its stop waits for the first server, which the gate lets print and exit; then the next one starts.
    const restarted = s.restart()
    writeFileSync(gate, '')
    const h = await restarted
    expect(await started).toEqual(h)
    const running = states.filter((x) => x.state === 'running')
    expect(running).toHaveLength(1)
    expect(running[0]!.pid).toBe(h.pid)
    expect(trustedOrigin(s.status)).toBe('http://' + h.listen)
    await s.stop()
  })
  it("fails a first start whose server exits before its handshake with the server's words, never as a stop", async () => {
    const dir = mkdtempSync(join(tmpdir(), 'cd-fake-'))
    const file = join(dir, 'server.js')
    writeFileSync(file, `process.stderr.write('the data folder is in use\\n'); process.exit(4)\n`)
    const s = new ServerSupervisor({ launcher: new ScriptLauncher(file), args: [], env: () => ({ PATH: process.env.PATH ?? '' }), backoffMinMs: 60_000, log: quiet })
    const started = s.start()
    // What the app's first start takes for a failure to say (main.ts: anything but StartStopped is the dialog and the log).
    const e = await started.then(
      () => null,
      (x: unknown) => x,
    )
    expect(e).toBeInstanceOf(Error)
    expect(e).not.toBeInstanceOf(StartStopped)
    expect((e as Error).message).toMatch(/exited before it listened \(4\)/)
    expect(s.status.state).toBe('failed')
    expect(trustedOrigin(s.status)).toBe('')
    await s.stop()
  })
  it('fails a restart whose server exits before its handshake: the save answers, and a press after it is not held', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'cd-fake-'))
    const count = join(dir, 'count')
    writeFileSync(count, '0')
    const file = join(dir, 'server.js')
    // The first spawn serves; every later one exits at once.
    writeFileSync(
      file,
      `
const http = require('http')
const fs = require('fs')
const n = Number(fs.readFileSync(${JSON.stringify(count)}, 'utf8')) + 1
fs.writeFileSync(${JSON.stringify(count)}, String(n))
if (n > 1) process.exit(5)
const srv = http.createServer((req, res) => res.end('{}'))
srv.listen(0, '127.0.0.1', () => process.stdout.write(JSON.stringify({ listen: '127.0.0.1:' + srv.address().port, publicUrl: 'http://127.0.0.1:' + srv.address().port, pid: process.pid, version: 'fake' }) + '\\n'))
process.stdin.on('end', () => process.exit(0))
process.stdin.resume()
`,
    )
    const s = new ServerSupervisor({ launcher: new ScriptLauncher(file), args: [], env: () => ({ PATH: process.env.PATH ?? '' }), backoffMinMs: 60_000, healthMs: 60_000, log: quiet })
    await s.start()
    // The settings in the distribution's form, as the app keeps them on Windows too (posix): no path joined on this system.
    let stored = { ...defaultSettings('/home/me', '/ud'), dataDir: '/ud/conductor' }
    const allow = new FolderAllow({
      get: () => stored,
      set: (x) => void (stored = x),
      server: () => ({ url: s.status.origin ?? '', pid: s.status.pid, running: s.status.state === 'running' }),
      token: () => 'token-0123456789',
      key: () => 'key-0123456789',
      fetch: async () => ({ status: 500, json: async () => ({}) }),
      posix: true,
      told: () => {},
    })
    const saver = new SettingsSaver()
    // A Settings save that restarts the server, as ipc.ts runs it: behind the press, through the supervisor's restart.
    const saved = allow.save(() => saver.save({ get: () => stored, set: (x) => void (stored = x), restart: () => s.restart(), posix: true }, { reach: 'off' }))
    await expect(saved).rejects.toThrow(/exited before it listened \(5\)/)
    expect(s.status.state).toBe('failed')
    // The press after it is not held behind a save: it is told no server runs.
    const pressed = await allow.allow('/srv/proj', '')
    expect(pressed).toMatchObject({ ok: false, code: 'busy', message: 'Conductor is restarting: try again' })
    await s.stop()
  })
  it("a stop during a restart's start fails the restart as stopped", async () => {
    const dir = mkdtempSync(join(tmpdir(), 'cd-fake-'))
    const count = join(dir, 'count')
    writeFileSync(count, '0')
    const gate = join(dir, 'gate')
    const file = join(dir, 'server.js')
    // The first spawn serves at once; the second waits for the gate before its handshake.
    writeFileSync(
      file,
      `
const http = require('http')
const fs = require('fs')
const n = Number(fs.readFileSync(${JSON.stringify(count)}, 'utf8')) + 1
fs.writeFileSync(${JSON.stringify(count)}, String(n))
const srv = http.createServer((req, res) => res.end('{}'))
srv.listen(0, '127.0.0.1', () => {
  const print = () => process.stdout.write(JSON.stringify({ listen: '127.0.0.1:' + srv.address().port, publicUrl: 'http://127.0.0.1:' + srv.address().port, pid: process.pid, version: 'fake' }) + '\\n')
  if (n === 1) return print()
  const t = setInterval(() => { if (fs.existsSync(${JSON.stringify(gate)})) { clearInterval(t); print() } }, 10)
})
process.stdin.on('end', () => process.exit(0))
process.stdin.resume()
`,
    )
    const s = new ServerSupervisor({ launcher: new ScriptLauncher(file), args: [], env: () => ({ PATH: process.env.PATH ?? '' }), backoffMinMs: 60_000, healthMs: 60_000, log: quiet })
    const states: string[] = []
    await s.start()
    s.on('state', (st) => states.push(st.state))
    const restarted = s.restart()
    restarted.catch(() => {})
    await vi.waitFor(() => expect(readFileSync(count, 'utf8')).toBe('2'))
    const stopping = s.stop()
    writeFileSync(gate, '')
    await expect(restarted).rejects.toBeInstanceOf(StartStopped)
    await stopping
    expect(states).not.toContain('running')
    expect(trustedOrigin(s.status)).toBe('')
  })
  it('asks for its first line again at every start, so a server answers only the token of its own line', async () => {
    // A server that takes its token from its first line and answers 401 to any other.
    const dir = mkdtempSync(join(tmpdir(), 'cd-fake-'))
    const file = join(dir, 'server.js')
    writeFileSync(
      file,
      `
const http = require('http')
let buf = ''
const onData = (c) => {
  buf += c
  const i = buf.indexOf('\\n')
  if (i < 0) return
  process.stdin.off('data', onData)
  const token = JSON.parse(buf.slice(0, i)).workbenchToken
  const srv = http.createServer((req, res) => {
    res.statusCode = req.headers.authorization === 'Bearer ' + token ? 200 : 401
    res.end('{}')
  })
  srv.listen(0, '127.0.0.1', () => {
    process.stdout.write(JSON.stringify({ listen: '127.0.0.1:' + srv.address().port, publicUrl: 'http://localhost:' + srv.address().port, pid: process.pid, version: 'fake' }) + '\\n')
  })
  process.stdin.on('end', () => process.exit(0))
}
process.stdin.setEncoding('utf8')
process.stdin.on('data', onData)
`,
    )
    let n = 0
    const tokens: string[] = []
    const s = new ServerSupervisor({
      launcher: new ScriptLauncher(file),
      args: [],
      env: () => ({ PATH: process.env.PATH ?? '' }),
      firstLine: () => {
        const t = `token-${++n}`
        tokens.push(t)
        return JSON.stringify({ workbenchToken: t }) + '\n'
      },
      log: quiet,
    })
    await s.start()
    const first = await s.restart()
    void first
    expect(tokens).toEqual(['token-1', 'token-2'])
    const origin = trustedOrigin(s.status)
    const ask = (t: string) => fetch(origin + '/api/health', { headers: { authorization: 'Bearer ' + t } }).then((r) => r.status)
    expect(await ask('token-2')).toBe(200)
    expect(await ask('token-1')).toBe(401)
    await s.stop()
  })
  it('takes for its origin a loopback address written as one, and nothing else', () => {
    expect(loopbackOrigin('127.0.0.1:4312')).toBe('http://127.0.0.1:4312')
    expect(loopbackOrigin('127.1.2.3:1')).toBe('http://127.1.2.3:1')
    expect(loopbackOrigin('[::1]:4312')).toBe('http://[::1]:4312')
    for (const x of ['localhost:4312', '0.0.0.0:4312', '10.0.0.1:4312', '127.0.0.1', '127.0.0.1:0', '127.0.0.1:70000', '127.0.0.300:1', '[::]:4312', 'evil.example:80', '127.0.0.1:1@evil.example', '']) {
      expect(loopbackOrigin(x), x).toBe('')
    }
    for (const state of ['stopped', 'starting', 'failed'] as const) expect(trustedOrigin({ state, url: 'http://localhost:1', origin: 'http://127.0.0.1:1', failures: 0 })).toBe('')
    expect(trustedOrigin({ state: 'running', url: 'http://localhost:1', failures: 0 })).toBe('')
  })
  it("a stop during the start's preparation spawns nothing (#363, Codex's review)", async () => {
    const launcher = new ScriptLauncher(fakeServer({}))
    let release!: () => void
    const gate = new Promise<void>((r) => (release = r))
    const s = new ServerSupervisor({ launcher, args: [], beforeStart: () => gate, env: () => process.env as Record<string, string>, log: quiet })
    const started = s.start()
    started.catch(() => {})
    await s.stop()
    release()
    await expect(started).rejects.toBeInstanceOf(StartStopped)
    expect(launcher.spawned).toBe(0)
    expect(s.status.state).toBe('stopped')
  })
  it('a restart during the preparation leaves one server, which the first start reports too', async () => {
    const launcher = new ScriptLauncher(fakeServer({}))
    let calls = 0
    let release!: () => void
    const gate = new Promise<void>((r) => (release = r))
    const s = new ServerSupervisor({ launcher, args: [], beforeStart: () => (++calls === 1 ? gate : Promise.resolve()), env: () => process.env as Record<string, string>, log: quiet })
    const first = s.start()
    first.catch(() => {})
    const second = s.restart()
    release()
    const h2 = await second
    const h1 = await first
    expect(h1).toBe(h2)
    expect(launcher.spawned).toBe(1)
    await s.stop()
  })
  it('a preparation that never settles is bounded, and the server starts without it', async () => {
    const launcher = new ScriptLauncher(fakeServer({}))
    const lines: string[] = []
    const s = new ServerSupervisor({ launcher, args: [], beforeStart: () => new Promise<void>(() => {}), prepareMs: 100, env: () => process.env as Record<string, string>, log: (_k, l) => lines.push(l) })
    const h = await s.start()
    expect(h.version).toBe('fake')
    expect(lines.some((l) => l.includes('did not finish within'))).toBe(true)
    await s.stop()
  })
  it('restarts a server that dies, with backoff, and gives up after too many failures', async () => {
    const launcher = new ScriptLauncher(fakeServer({ crashAfterMs: 150 }))
    const states: string[] = []
    const s = new ServerSupervisor({ launcher, args: [], env: () => process.env as Record<string, string>, log: quiet, backoffMinMs: 50, backoffMaxMs: 100, giveUpAfter: 3, healthMs: 10_000 })
    s.on('state', (st) => states.push(st.state))
    const gaveUp = new Promise<void>((resolve) => s.once('gaveUp', () => resolve()))
    await s.start()
    await gaveUp
    expect(launcher.spawned).toBeGreaterThanOrEqual(3)
    expect(s.status.failures).toBe(3)
    expect(states).toContain('failed')
    await s.stop()
  })
  it('reports a server that never listens', async () => {
    const launcher = new ScriptLauncher(fakeServer({ silent: true }))
    const s = new ServerSupervisor({ launcher, args: [], env: () => process.env as Record<string, string>, log: quiet, handshakeMs: 300 })
    await expect(s.start()).rejects.toThrow(/did not report its address/)
    await s.stop()
  })
  it('restart stops and starts again with the environment read afresh', async () => {
    const launcher = new ScriptLauncher(fakeServer({}))
    let token = 'one'
    const s = new ServerSupervisor({ launcher, args: [], env: () => ({ ...process.env, CONDUCTOR_WORKBENCH_TOKEN: token } as Record<string, string>), log: quiet })
    const first = await s.start()
    token = 'two'
    const second = await s.restart()
    expect(first.workbenchToken).toBe('one')
    expect(second.workbenchToken).toBe('two')
    expect(second.pid).not.toBe(first.pid)
    await s.stop()
  })
})
