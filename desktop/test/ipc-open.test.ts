import { describe, expect, it, vi } from 'vitest'
import { EventEmitter } from 'node:events'

// registerIpc hands its handlers to Electron's ipcMain: here they are kept, to call as a page would.
const handlers = new Map<string, (e: unknown, ...args: unknown[]) => unknown>()
const openExternal = vi.fn(async () => {})
vi.mock('electron', () => ({
  app: { getVersion: () => '0.0.0-test' },
  ipcMain: { handle: (channel: string, f: (e: unknown, ...args: unknown[]) => unknown) => handlers.set(channel, f) },
  shell: { openExternal },
}))
const { registerIpc } = await import('../src/ipc')

const origin = 'http://127.0.0.1:4312'
const spawned: Array<{ file: string; args: string[] }> = []
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
  browsers: {
    platform: 'linux',
    env: { PATH: '/usr/bin' },
    home: '/home/me',
    isFile: (p) => p === '/usr/bin/microsoft-edge',
    isDir: () => false,
    realpath: (p) => p,
    spawn: (file, args) => {
      spawned.push({ file, args })
      const child = new EventEmitter() as EventEmitter & { unref: () => void }
      child.unref = () => {}
      queueMicrotask(() => child.emit('spawn'))
      return child as never
    },
    openExternal: async (url) => {
      await openExternal(url)
    },
  },
})
const from = (url: string) => ({ senderFrame: { url } })

describe('the bridge\'s openUrl and browsers', () => {
  it('refuse a sender that is not the workbench, starting nothing', async () => {
    const open = handlers.get('conductor:openUrl')!
    expect(() => open(from('https://evil.example/'), 'https://example.test/', { name: 'edge' })).toThrow(/refused: not the workbench/)
    expect(() => handlers.get('conductor:browsers')!(from('https://evil.example/'))).toThrow(/refused/)
    expect(spawned).toEqual([])
  })
  it('open one tab in the browser named for the workbench, and list the browsers found', async () => {
    const open = handlers.get('conductor:openUrl')!
    expect(await open(from(origin + '/sessions/s1'), 'https://example.test/', { name: 'edge', profile: 'Profile 2' })).toEqual({ outcome: 'browser' })
    expect(spawned).toEqual([{ file: '/usr/bin/microsoft-edge', args: ['--profile-directory=Profile 2', 'https://example.test/'] }])
    expect(await open(from(origin + '/'), 'https://example.test/x', null)).toEqual({ outcome: 'default' })
    expect(openExternal).toHaveBeenCalledWith('https://example.test/x')
    expect(handlers.get('conductor:browsers')!(from(origin + '/agents'))).toEqual(['edge'])
  })
  it('refuse a link that is not http(s) and a browser the app does not know', async () => {
    const open = handlers.get('conductor:openUrl')!
    await expect(open(from(origin + '/'), 'file:///etc/passwd', null)).rejects.toThrow(/refused/)
    await expect(open(from(origin + '/'), 'https://example.test/', { name: 'opera' })).rejects.toThrow(/refused/)
    expect(spawned).toHaveLength(1)
  })
})
