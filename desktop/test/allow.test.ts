import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { describe, expect, it, vi } from 'vitest'

// The preload's bridge is handed to Electron's contextBridge: here it is kept, to look at as the page sees it.
const { exposed, invoked } = vi.hoisted(() => ({ exposed: {} as Record<string, unknown>, invoked: [] as unknown[][] }))
vi.mock('electron', () => ({
  contextBridge: { exposeInMainWorld: (name: string, api: unknown) => (exposed[name] = api) },
  ipcRenderer: { invoke: async (...args: unknown[]) => void invoked.push(args), on: () => {}, removeListener: () => {} },
}))
import { ALLOW_ON_WINDOWS, allowOffered, covered, FolderAllow, keyFor, listRefusal, MAX_ROOTS, pathRefusal, type AllowDeps } from '../src/allow'
import { ALLOW_FOLDER_ON_WINDOWS, allowFolderCall } from '../src/preload'
import { secretsLine, serverEnv } from '../src/env'
import { defaultSettings, keepIn, type DesktopSettings } from '../src/settings'

const TOKEN = 'tok-' + 'a'.repeat(60)
const KEY = 'key-' + 'b'.repeat(60)

interface Asked {
  url: string
  headers: Record<string, string>
  body: { path: string; confirmed?: string }
  redirect: string
}

/**
 * A fake app around FolderAllow: its settings store (which can be made to fail), its server (url, pid, running) and a fake
 * fetch answering each request with reply (a 200 with the folder by default), every request kept.
 */
function fake(o: { reply?: (a: Asked) => { status: number; body: unknown } | Promise<{ status: number; body: unknown }>; roots?: string[]; whenRunning?: AllowDeps['whenRunning']; set?: (s: DesktopSettings, keep: (s: DesktopSettings) => void) => void } = {}) {
  let stored: DesktopSettings = { ...defaultSettings('/home/me', '/home/me/.config/conductor-desktop'), allowedRoots: o.roots ?? ['/home/me'] }
  const asked: Asked[] = []
  const saves: DesktopSettings[] = []
  let told = 0
  const server = { url: 'http://127.0.0.1:4312', pid: 100 as number | undefined, running: true }
  let failSave: Error | null = null
  const reply = o.reply ?? ((a: Asked) => ({ status: 200, body: { root: a.body.path, added: true } }))
  const d: AllowDeps = {
    get: () => stored,
    set: (s) => {
      if (o.set) return o.set(s, (k) => (stored = k))
      if (failSave) throw failSave
      stored = s
      saves.push(s)
    },
    server: () => ({ ...server }),
    token: () => TOKEN,
    key: () => KEY,
    fetch: async (url, init) => {
      const a: Asked = { url, headers: init.headers, body: JSON.parse(init.body), redirect: init.redirect }
      asked.push(a)
      const r = await reply(a)
      return { status: r.status, json: async () => r.body }
    },
    posix: false,
    told: () => void told++,
    whenRunning: o.whenRunning,
  }
  return {
    allow: new FolderAllow(d),
    asked,
    saves,
    server,
    stored: () => stored,
    told: () => told,
    failSave: (e: Error | null) => (failSave = e),
  }
}

describe('Allow this folder', () => {
  it('asks the server with the token and the key, keeps the folder in the settings, restarts nothing, and tells the windows', async () => {
    const f = fake()
    expect(await f.allow.allow('/srv/proj', '')).toEqual({ ok: true, root: '/srv/proj', saved: true })
    expect(f.asked).toHaveLength(1)
    expect(f.asked[0]).toMatchObject({ url: 'http://127.0.0.1:4312/api/roots', redirect: 'error', body: { path: '/srv/proj' } })
    expect(f.asked[0]!.headers.Authorization).toBe(`Bearer ${TOKEN}`)
    expect(f.asked[0]!.headers['X-Conductor-Desktop-Key']).toBe(KEY)
    expect(f.stored().allowedRoots).toEqual(['/home/me', '/srv/proj'])
    expect(f.told()).toBe(1)
  })

  it('saves nothing for a refusal and hands back its code and fields', async () => {
    const f = fake({ reply: () => ({ status: 409, body: { error: { code: 'folder_is_link', message: 'the path goes through a link', real: '/srv/real' } } }) })
    expect(await f.allow.allow('/srv/link', '')).toEqual({ ok: false, code: 'folder_is_link', message: 'the path goes through a link', real: '/srv/real' })
    const w = fake({ reply: () => ({ status: 409, body: { error: { code: 'folder_wide', message: 'wide', wide: 'home' } } }) })
    expect(await w.allow.allow('/home/me2', '')).toMatchObject({ ok: false, code: 'folder_wide', wide: 'home' })
    const r = fake({ reply: () => ({ status: 403, body: { error: { code: 'folder_refused', message: 'no', reason: 'conductor_data' } } }) })
    expect(await r.allow.allow('/srv/data', '')).toMatchObject({ ok: false, code: 'folder_refused', reason: 'conductor_data' })
    for (const x of [f, w, r]) {
      expect(x.saves).toHaveLength(0)
      expect(x.told()).toBe(0)
    }
  })

  it('sends the kind confirmed for a wide folder', async () => {
    const f = fake()
    await f.allow.allow('/srv/wide', 'above_home')
    expect(f.asked[0]!.body).toEqual({ path: '/srv/wide', confirmed: 'above_home' })
  })

  it('says saved false, with why, when the file cannot be written: the server holds the folder until it restarts', async () => {
    const f = fake()
    f.failSave(new Error('EACCES: permission denied'))
    expect(await f.allow.allow('/srv/proj', '')).toEqual({ ok: true, root: '/srv/proj', saved: false, error: 'EACCES: permission denied' })
    expect(f.asked).toHaveLength(1)
  })

  it('asks the server running once more when it is not the one it asked once the file is saved', async () => {
    const f = fake({
      reply: (a) => {
        // The server is replaced while the first request is answered (a crash and the supervisor's start again).
        if (f.asked.length === 1) Object.assign(f.server, { url: 'http://127.0.0.1:5000', pid: 200 })
        return { status: 200, body: { root: a.body.path, added: true } }
      },
    })
    expect(await f.allow.allow('/srv/proj', '')).toEqual({ ok: true, root: '/srv/proj', saved: true })
    expect(f.asked.map((a) => a.url)).toEqual(['http://127.0.0.1:4312/api/roots', 'http://127.0.0.1:5000/api/roots'])
  })

  it('sends the token and the key to a loopback address written as one alone', async () => {
    for (const url of ['http://localhost:4312', 'http://10.0.0.1:4312', 'https://127.0.0.1:4312', 'http://127.0.0.1:4312/x', 'http://evil.example']) {
      const f = fake()
      f.server.url = url
      expect(await f.allow.allow('/srv/proj', ''), url).toMatchObject({ ok: false })
      expect(f.asked, url).toHaveLength(0)
    }
  })

  it('waits for a server restarted meanwhile and asks it, saying so when it does not take the folder or does not come', async () => {
    // The first server answers, and is gone by the save: its successor is still starting.
    const starting = (f: ReturnType<typeof fake>) => Object.assign(f.server, { url: '', pid: undefined, running: false })
    let comes: (s: { url: string; pid?: number; running: boolean } | null) => void = () => {}
    const f = fake({
      reply: (a) => {
        if (f.asked.length === 1) starting(f)
        return { status: 200, body: { root: a.body.path, added: f.asked.length === 1 } }
      },
      whenRunning: () => new Promise((r) => (comes = r)),
    })
    const press = f.allow.allow('/srv/proj', '')
    await vi.waitFor(() => expect(f.stored().allowedRoots).toContain('/srv/proj'))
    expect(f.asked).toHaveLength(1)
    Object.assign(f.server, { url: 'http://127.0.0.1:5000', pid: 300, running: true })
    comes({ ...f.server })
    expect(await press).toEqual({ ok: true, root: '/srv/proj', saved: true })
    expect(f.asked.map((a) => a.url)).toEqual(['http://127.0.0.1:4312/api/roots', 'http://127.0.0.1:5000/api/roots'])
    // None comes in time: kept in Settings, and the answer says a restart may be needed.
    const g = fake({
      reply: (a) => {
        starting(g)
        return { status: 200, body: { root: a.body.path, added: true } }
      },
      whenRunning: async () => null,
    })
    const late = await g.allow.allow('/srv/proj', '')
    expect(late).toMatchObject({ ok: true, root: '/srv/proj', saved: true, error: expect.stringContaining('restart Conductor') })
    // The successor runs and refuses it: the answer says so.
    const h = fake({
      reply: (a) => {
        if (h.asked.length === 1) Object.assign(h.server, { url: 'http://127.0.0.1:5001', pid: 301 })
        return h.asked.length === 1 ? { status: 200, body: { root: a.body.path, added: true } } : { status: 409, body: { error: { code: 'too_many_roots', message: 'agents may use at most 64 folders' } } }
      },
    })
    expect(await h.allow.allow('/srv/proj', '')).toMatchObject({ ok: true, saved: true, error: expect.stringContaining('did not take the folder') })
  })

  it('keeps nothing in the settings it runs with when the file cannot be written', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'cd-keep-'))
    // A file where the settings' folder would be: the write fails.
    writeFileSync(join(dir, 'blocked'), 'x')
    const f = fake({ set: (s, take) => keepIn(join(dir, 'blocked', 'settings.json'), take)(s) })
    const before = f.stored().allowedRoots
    const a = await f.allow.allow('/srv/proj', '')
    expect(a).toMatchObject({ ok: true, root: '/srv/proj', saved: false })
    expect(f.stored().allowedRoots).toEqual(before)
    expect(f.told()).toBe(0)
  })

  it('sends nothing to a server not running, or whose process is not the one its handshake named', async () => {
    const f = fake()
    f.server.running = false
    expect(await f.allow.allow('/srv/proj', '')).toMatchObject({ ok: false, code: 'busy' })
    f.server.running = true
    f.server.pid = undefined
    expect(await f.allow.allow('/srv/proj', '')).toMatchObject({ ok: false, code: 'busy' })
    expect(f.asked).toHaveLength(0)
  })

  it('says so for a server that does not answer, and for one too old to know the route', async () => {
    const gone = fake({ reply: () => Promise.reject(new Error(`connect ECONNREFUSED ${KEY}`)) })
    const a = await gone.allow.allow('/srv/proj', '')
    expect(a).toMatchObject({ ok: false, code: 'no_answer' })
    expect(JSON.stringify(a)).not.toContain(KEY)
    const old = fake({ reply: () => ({ status: 404, body: { error: { code: 'not_found', message: 'not found' } } }) })
    expect(await old.allow.allow('/srv/proj', '')).toMatchObject({ ok: false, code: 'not_supported' })
    expect([...gone.saves, ...old.saves]).toHaveLength(0)
  })

  it('runs twenty presses one at a time', async () => {
    let open = 0
    let most = 0
    const f = fake({
      reply: async (a) => {
        open++
        most = Math.max(most, open)
        await new Promise((r) => setImmediate(r))
        open--
        return { status: 200, body: { root: a.body.path, added: true } }
      },
    })
    const all = await Promise.all(Array.from({ length: 20 }, (_, i) => f.allow.allow(`/srv/p${i}`, '')))
    expect(most).toBe(1)
    expect(all.every((a) => a.ok && a.saved)).toBe(true)
    expect(f.stored().allowedRoots).toHaveLength(21)
  })

  it('is busy during a Settings save, and a save waits for a press under way', async () => {
    let release!: () => void
    const f = fake({ reply: (a) => new Promise((r) => (release = () => r({ status: 200, body: { root: a.body.path, added: true } }))) })
    const press = f.allow.allow('/srv/proj', '')
    // The press is under way (its request held): a save waits for it.
    await vi.waitFor(() => expect(f.asked).toHaveLength(1))
    let sawThePress: boolean | undefined
    const save = f.allow.save(async () => {
      sawThePress = f.stored().allowedRoots.includes('/srv/proj')
      // During the save a press is busy and asks nothing.
      expect(await f.allow.allow('/srv/other', '')).toMatchObject({ ok: false, code: 'busy' })
    })
    // Held: the save has not begun while the press waits for its answer.
    await new Promise((r) => setImmediate(r))
    expect(sawThePress).toBeUndefined()
    release()
    await Promise.all([press, save])
    expect(sawThePress).toBe(true)
    expect(f.asked).toHaveLength(1)
  })

  it('refuses before any request a path the saved list cannot hold, a wrong confirmed, the 65th folder and a list past 8,192 characters', async () => {
    const f = fake()
    for (const p of ['/srv/a,b', '/srv/a\nb', '/srv/a\tb', '/srv/a‎b', '/srv/a ', ' /srv/a', 'srv/a', '/srv/a/', '/srv//a', '/srv/./a', '/srv/../a', '/' + 'a'.repeat(1024), '', 42, null, ['/srv']]) {
      expect(await f.allow.allow(p, ''), JSON.stringify(p)).toMatchObject({ ok: false, code: 'invalid_request' })
    }
    expect(await f.allow.allow('/srv/a', 'all')).toMatchObject({ ok: false, code: 'invalid_request' })
    expect(await f.allow.allow('/srv/a', 1)).toMatchObject({ ok: false, code: 'invalid_request' })
    const full = fake({ roots: Array.from({ length: MAX_ROOTS }, (_, i) => `/srv/r${i}`) })
    expect(await full.allow.allow('/srv/more', '')).toMatchObject({ ok: false, code: 'too_many_roots' })
    const long = fake({ roots: Array.from({ length: 8 }, (_, i) => `/srv/${String(i).repeat(1000)}`) })
    expect(await long.allow.allow('/srv/' + 'x'.repeat(200), '')).toMatchObject({ ok: false, code: 'too_many_roots' })
    expect([...f.asked, ...full.asked, ...long.asked]).toHaveLength(0)
  })

  it('saves nothing more for a folder the saved list already holds', async () => {
    const f = fake({ reply: () => ({ status: 200, body: { root: '/home/me/code', added: false } }) })
    expect(await f.allow.allow('/home/me/code', '')).toEqual({ ok: true, root: '/home/me/code', saved: true })
    expect(f.saves).toHaveLength(0)
  })

  it('hands back no answer and no error holding the token or the key', async () => {
    const f = fake({ reply: () => ({ status: 403, body: { error: { code: 'not_the_app', message: `echo ${TOKEN} ${KEY}` } } }) })
    const a = await f.allow.allow('/srv/proj', '')
    expect(JSON.stringify(a)).not.toContain(TOKEN)
    expect(JSON.stringify(a)).not.toContain(KEY)
  })
})

describe('the rules of the list', () => {
  it('pathRefusal, listRefusal and covered', () => {
    expect(pathRefusal('/srv/proj')).toBe('')
    expect(pathRefusal('/')).toBe('')
    expect(listRefusal(['/a'], '/a')).toBe('')
    expect(covered(['/home/me'], '/home/me/code')).toBe(true)
    expect(covered(['/home/me'], '/home/meow')).toBe(false)
  })
})

describe('where the press is offered', () => {
  it('on Windows only once its switch is on, the preload and the main process agreeing', () => {
    expect(ALLOW_FOLDER_ON_WINDOWS).toBe(ALLOW_ON_WINDOWS)
    expect(allowOffered('linux')).toBe(true)
    expect(allowOffered('darwin')).toBe(true)
    expect(allowOffered('win32')).toBe(ALLOW_ON_WINDOWS)
    expect(allowFolderCall('linux')).toBeTypeOf('function')
    expect(allowFolderCall('win32') === undefined).toBe(!ALLOW_ON_WINDOWS)
    // The bridge this platform's page sees, and what it sends.
    const bridge = exposed.conductorDesktop as Record<string, unknown>
    expect(typeof bridge.allowFolder === 'function').toBe(allowOffered(process.platform))
    expect(bridge.onSettings).toBeTypeOf('function')
  })
  it('mints no key where the press is off, and hands the server none: it then allows no folder', () => {
    const mint = vi.fn(() => KEY)
    expect(keyFor('win32', mint)).toBe(ALLOW_ON_WINDOWS ? KEY : '')
    if (!ALLOW_ON_WINDOWS) {
      expect(mint).not.toHaveBeenCalled()
      expect(secretsLine('tok', keyFor('win32', mint))).toBe('{"workbenchToken":"tok"}\n')
    }
    expect(keyFor('linux', mint)).toBe(KEY)
    expect(keyFor('darwin', mint)).toBe(KEY)
    const main = readFileSync(join(__dirname, '..', 'src', 'main.ts'), 'utf8')
    expect(main).toContain("const desktopKey = keyFor(process.platform, () => randomBytes(32).toString('hex'))")
  })
})

describe('the key', () => {
  it("goes to the server on its stdin with the token, never to an environment or a log line", () => {
    const defaults = defaultSettings('/home/me', '/ud')
    expect(serverEnv({ HOME: '/home/me', CONDUCTOR_DESKTOP_KEY: 'leaked' }, defaults, '/usr/bin', '/ud')).not.toHaveProperty('CONDUCTOR_DESKTOP_KEY')
    expect(secretsLine('tok', KEY)).toContain(KEY)
    // main.ts mints it beside the token and hands it over in the first line alone; no line of the app's own sets it on
    // process.env or logs it.
    const main = readFileSync(join(__dirname, '..', 'src', 'main.ts'), 'utf8')
    expect(main).toContain('firstLine: () => secretsLine(token, desktopKey)')
    expect(main).not.toMatch(/process\.env\.CONDUCTOR_DESKTOP_KEY|process\.env\[['"]CONDUCTOR_DESKTOP_KEY/)
    for (const line of main.split('\n')) if (/\blog\(/.test(line)) expect(line, line).not.toContain('desktopKey')
  })
})
