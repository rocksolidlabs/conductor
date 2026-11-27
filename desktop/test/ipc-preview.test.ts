import { describe, expect, it, vi } from 'vitest'

// registerIpc hands its handlers to Electron's ipcMain: here they are kept, to call as a page would.
const handlers = new Map<string, (e: unknown, ...args: unknown[]) => unknown>()
vi.mock('electron', () => ({
  app: { getVersion: () => '0.0.0-test' },
  ipcMain: { handle: (channel: string, f: (e: unknown, ...args: unknown[]) => unknown) => handlers.set(channel, f) },
  shell: { openExternal: async () => {} },
}))
const { registerIpc, workbenchFrame } = await import('../src/ipc')

const origin = 'http://127.0.0.1:4312'
const preview = 'http://127.0.0.1:5173'
const fetched: string[] = []
const script = '/* the bridge */'
registerIpc({
  origin: () => origin,
  token: () => 't',
  supervisor: {} as never,
  settings: { get: () => ({}) as never, set: () => {} },
  showLog: () => {},
  notice: () => '',
  mainWindow: () => null,
  serverVersion: () => '',
  ice: () => ({}) as never,
  allowIceFirewall: async () => 'unknown',
  previewFetch: async (url) => {
    fetched.push(url)
    return { ok: true, status: 200, url, headers: { get: (n: string) => (n === 'content-type' ? 'text/javascript' : null) }, text: async () => script }
  },
  contentSize: () => ({ width: 1280, height: 800 }),
})

let ids = 1
interface Frame {
  origin: string
  url: string
  parent: Frame | null
  frameTreeNodeId: number
  ran: string[]
  framesInSubtree: Frame[]
  executeJavaScript(code: string): Promise<void>
}
function frame(o: string, url: string, parent: Frame | null): Frame {
  const f: Frame = { origin: o, url, parent, frameTreeNodeId: ids++, ran: [], framesInSubtree: [], executeJavaScript: async (code) => void f.ran.push(code) }
  return f
}

/** A window whose main frame shows page (its origin o), framing a preview; the calls it is sent from each of its frames. */
function win(page: string, o = new URL(page).origin) {
  const main = frame(o, page, null)
  const child = frame(preview, preview + '/', main)
  main.framesInSubtree = [main, child]
  const capturePage = vi.fn(async () => ({ isEmpty: () => false, getSize: () => ({ width: 100, height: 50 }), resize: () => { throw new Error('no resize') }, toPNG: () => new Uint8Array(8) }))
  const sender = { id: ids++, mainFrame: main, getZoomFactor: () => 1, capturePage }
  return { main, child, sender, capturePage, from: (f: Frame | null) => ({ sender, senderFrame: f }) }
}

const rect = { x: 0, y: 0, width: 100, height: 50 }
const channels = ['conductor:previewBridge', 'conductor:capturePreview'] as const
const args = { 'conductor:previewBridge': preview, 'conductor:capturePreview': rect }

describe("the preview's two channels", () => {
  it("answer the workbench's main frame, acting on its own window", async () => {
    const w = win(origin + '/preview?run=r1&task=build&attempt=1')
    expect(await handlers.get('conductor:previewBridge')!(w.from(w.main), preview)).toEqual({ injected: 1 })
    expect(fetched).toEqual([origin + '/preview-bridge.js'])
    expect(w.child.ran).toEqual([script])
    expect(await handlers.get('conductor:capturePreview')!(w.from(w.main), rect)).toEqual({ png: new Uint8Array(8), width: 100, height: 50, scale: 1 })
    expect(w.capturePage).toHaveBeenCalledTimes(1)
  })

  it('refuse a sender that is another frame of the workbench, a page of the app, another origin or none, doing nothing', async () => {
    fetched.length = 0
    const w = win(origin + '/preview')
    // A frame of the workbench (the preview's, or a frame of its own origin): never its main frame.
    const own = frame(origin, origin + '/', w.main)
    // A page the app opened from a file (the server log), which the other channels trust.
    const file = win('file:///opt/Conductor/resources/static/log.html', 'file://')
    const evil = win('https://evil.example/')
    const otherPort = win('http://127.0.0.1:9999/')
    const localhost = win('http://localhost:4312/')
    // The workbench's page, but at an origin the frame does not hold (an opaque one, a sandboxed document).
    const opaque = win(origin + '/', 'null')
    // A main frame of the workbench's origin, sent in another window's name.
    const stranger = win(origin + '/')
    const cases: Array<[string, { sender: unknown; senderFrame: unknown }]> = [
      ['the preview frame', w.from(w.child)],
      ["a frame at the workbench's origin", w.from(own)],
      ['a file: page', file.from(file.main)],
      ['another site', evil.from(evil.main)],
      ['another port', otherPort.from(otherPort.main)],
      ["the workbench's port under another name", localhost.from(localhost.main)],
      ['an opaque origin', opaque.from(opaque.main)],
      ["another window's main frame", { sender: w.sender, senderFrame: stranger.main }],
      ['no frame', w.from(null)],
      ['no sender', { sender: null, senderFrame: w.main }],
    ]
    for (const [name, e] of cases) {
      for (const ch of channels) expect(() => handlers.get(ch)!(e, args[ch]), `${name} on ${ch}`).toThrow("refused: not the workbench's main frame")
      expect(workbenchFrame(e as never, origin), name).toBe(false)
    }
    expect(fetched).toEqual([])
    for (const x of [w, file, evil, otherPort, localhost, opaque, stranger]) {
      expect(x.capturePage).not.toHaveBeenCalled()
      expect(x.child.ran).toEqual([])
    }
  })

  it("are like every channel of the bridge: the token call too answers the workbench's main frame alone, never a page of the app's own", () => {
    const file = win('file:///opt/Conductor/resources/static/log.html', 'file://')
    expect(() => handlers.get('conductor:token')!(file.from(file.main))).toThrow("refused: not the workbench's main frame")
    const w = win(origin + '/')
    const child = frame(origin, origin + '/', w.main)
    expect(() => handlers.get('conductor:token')!(w.from(child))).toThrow("refused: not the workbench's main frame")
    expect(handlers.get('conductor:token')!(w.from(w.main))).toBe('t')
  })

  it("refuse the workbench's own origin and a remote page as the frame to put the bridge in", async () => {
    fetched.length = 0
    const w = win(origin + '/preview')
    expect(await handlers.get('conductor:previewBridge')!(w.from(w.main), origin)).toEqual({ refused: 'refused: this is Conductor itself' })
    expect(await handlers.get('conductor:previewBridge')!(w.from(w.main), 'https://example.com')).toEqual({ refused: 'refused: only a page on this machine is previewed' })
    expect(fetched).toEqual([])
  })
})
