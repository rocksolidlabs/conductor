import { describe, expect, it, vi } from 'vitest'

// registerIpc hands its handlers to Electron's ipcMain: here they are kept, to call as a page would.
const { handlers } = vi.hoisted(() => ({ handlers: new Map<string, (e: unknown, ...args: unknown[]) => unknown>() }))
vi.mock('electron', () => ({
  app: { getVersion: () => '0.0.0-test' },
  ipcMain: { handle: (channel: string, f: (e: unknown, ...args: unknown[]) => unknown) => handlers.set(channel, f) },
  shell: { openExternal: async () => {} },
}))
import { registerIpc } from '../src/ipc'
import { FolderAllow } from '../src/allow'
import { defaultSettings, type DesktopSettings } from '../src/settings'

const origin = 'http://127.0.0.1:4312'
const TOKEN = 'tok-' + 'c'.repeat(60)
const KEY = 'key-' + 'd'.repeat(60)
let stored: DesktopSettings = defaultSettings('/home/me', '/ud')
const asked: string[] = []
let answer: () => { status: number; body: unknown } = () => ({ status: 200, body: { root: '/srv/proj', added: true } })
const restarts: number[] = []
// While set, every request the press sends is held until it resolves: the press is under way.
let gate: Promise<void> | null = null
const folders = new FolderAllow({
  get: () => stored,
  set: (s) => void (stored = s),
  server: () => ({ url: origin, pid: 7, running: true }),
  token: () => TOKEN,
  key: () => KEY,
  fetch: async (url) => {
    asked.push(url)
    if (gate) await gate
    const a = answer()
    return { status: a.status, json: async () => a.body }
  },
  posix: false,
  told: () => {},
})
// The origin the app trusts: its server's address while it runs, '' while none does (trustedOrigin).
let trusted = origin
registerIpc({
  origin: () => trusted,
  token: () => TOKEN,
  supervisor: { restart: async () => void restarts.push(1) } as never,
  settings: { get: () => stored, set: (s) => void (stored = s) },
  showLog: () => {},
  notice: () => '',
  mainWindow: () => null,
  serverVersion: () => '',
  ice: () => ({}) as never,
  allowIceFirewall: async () => 'unknown',
  folders,
})

interface Frame {
  origin: string
  url: string
  parent: Frame | null
  frameTreeNodeId: number
}
let ids = 1
const frame = (o: string, url: string, parent: Frame | null): Frame => ({ origin: o, url, parent, frameTreeNodeId: ids++ })
function win(page: string, o = new URL(page).origin) {
  const main = frame(o, page, null)
  const sender = { id: ids++, mainFrame: main }
  return { main, sender, from: (f: Frame | null) => ({ sender, senderFrame: f }) }
}

const call = (e: unknown, ...args: unknown[]) => handlers.get('conductor:allowFolder')!(e, ...args)

describe('conductor:allowFolder', () => {
  it("is answered for the workbench's main frame", async () => {
    const w = win(origin + '/')
    expect(await call(w.from(w.main), '/srv/proj', '')).toEqual({ ok: true, root: '/srv/proj', saved: true })
    expect(stored.allowedRoots).toContain('/srv/proj')
    expect(asked).toEqual([origin + '/api/roots'])
  })

  it('is refused for a subframe, a file: page, another origin and a window taken elsewhere, asking nothing', async () => {
    asked.length = 0
    const w = win(origin + '/')
    const sub = frame(origin, origin + '/inner', w.main)
    const file = win('file:///opt/Conductor/resources/static/log.html', 'file://')
    const evil = win('https://evil.example/')
    const otherPort = win('http://127.0.0.1:9999/')
    const stranger = win(origin + '/')
    for (const [name, e] of [
      ['a subframe', w.from(sub)],
      ['a file: page', file.from(file.main)],
      ['another origin', evil.from(evil.main)],
      ['another port', otherPort.from(otherPort.main)],
      ["another window's main frame", { sender: w.sender, senderFrame: stranger.main }],
      ['no frame', w.from(null)],
    ] as const) {
      let thrown: unknown
      try {
        await call(e, '/srv/other', '')
      } catch (err) {
        thrown = err
      }
      expect(String(thrown), name).toContain("refused: not the workbench's main frame")
      expect(String(thrown)).not.toContain(KEY)
      expect(String(thrown)).not.toContain(TOKEN)
    }
    expect(asked).toEqual([])
  })

  it('hands back no answer holding the key or the token, whatever the server says', async () => {
    answer = () => ({ status: 403, body: { error: { code: 'not_the_app', message: `${KEY} ${TOKEN}` } } })
    const w = win(origin + '/')
    const a = await call(w.from(w.main), '/srv/third', '')
    expect(JSON.stringify(a)).not.toContain(KEY)
    expect(JSON.stringify(a)).not.toContain(TOKEN)
    answer = () => ({ status: 200, body: { root: '/srv/proj', added: true } })
  })

  it('makes a Settings save and a restart from the page wait for a press, and a press during them busy', async () => {
    let release!: () => void
    answer = () => ({ status: 200, body: { root: '/srv/fourth', added: true } })
    const held = new Promise<void>((r) => (release = r))
    let underWay = false
    const slow = new FolderAllow({
      get: () => stored,
      set: (s) => void (stored = s),
      server: () => ({ url: origin, pid: 7, running: true }),
      token: () => TOKEN,
      key: () => KEY,
      fetch: async () => {
        underWay = true
        await held
        return { status: 200, json: async () => ({ root: '/srv/fourth', added: true }) }
      },
      posix: false,
      told: () => {},
    })
    const press = slow.allow('/srv/fourth', '')
    // The press is under way (its request held) before the restart is asked.
    await vi.waitFor(() => expect(underWay).toBe(true))
    let restarted = false
    const restart = slow.save(async () => void (restarted = true))
    await new Promise((r) => setImmediate(r))
    expect(restarted).toBe(false)
    release()
    await Promise.all([press, restart])
    expect(restarted).toBe(true)
    expect(await press).toMatchObject({ ok: true, root: '/srv/fourth' })
    // Through the IPC: a save from the page goes through the same gate.
    const w = win(origin + '/')
    await handlers.get('conductor:settings:set')!(w.from(w.main), { closeToTray: false })
    await handlers.get('conductor:restartServer')!(w.from(w.main))
    expect(restarts).toHaveLength(1)
  })

  it("answers no call while the app's server is not running, its own page's included", async () => {
    trusted = ''
    try {
      const w = win(origin + '/')
      for (const [ch, args] of [['conductor:token', []], ['conductor:settings:get', []], ['conductor:allowFolder', ['/srv/five', '']]] as const) {
        let thrown: unknown
        try {
          await handlers.get(ch)!(w.from(w.main), ...args)
        } catch (err) {
          thrown = err
        }
        expect(String(thrown), ch).toMatch(/refused/)
      }
    } finally {
      trusted = origin
    }
  })

  it("holds a Settings save and a restart the page asks for behind a press under way, and refuses a press meanwhile", async () => {
    let release!: () => void
    gate = new Promise<void>((r) => (release = r))
    const w = win(origin + '/')
    const before = restarts.length
    const n = asked.length
    // A press through the bridge, its request held.
    const press = call(w.from(w.main), '/srv/held', '') as Promise<unknown>
    await vi.waitFor(() => expect(asked.length).toBe(n + 1))
    // A save that restarts the server, and a restart, both asked while the press is under way.
    const save = handlers.get('conductor:settings:set')!(w.from(w.main), { yolo: !stored.yolo }) as Promise<unknown>
    const restart = handlers.get('conductor:restartServer')!(w.from(w.main)) as Promise<unknown>
    await new Promise((r) => setImmediate(r))
    expect(restarts.length).toBe(before)
    // A press asked while they wait comes after the one under way, and is busy: it asks nothing.
    const second = call(w.from(w.main), '/srv/other', '') as Promise<unknown>
    gate = null
    release()
    await Promise.all([press, save, restart])
    expect(await second).toMatchObject({ ok: false, code: 'busy' })
    expect(asked.length).toBe(n + 1)
    expect(restarts.length).toBe(before + 2)
  })
})
