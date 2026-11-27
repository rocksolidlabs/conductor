import { describe, expect, it } from 'vitest'
import { SettingsSaver } from '../src/save'
import { defaultSettings, type DesktopSettings } from '../src/settings'

/** An app's settings store and supervisor for the saver: what is stored, and each restart asked. */
function app(restart: () => Promise<void> = async () => {}) {
  let stored = defaultSettings('/home/me', '/home/me/.config/conductor-desktop')
  const restarts: number[] = []
  return {
    restarts,
    stored: () => stored,
    d: {
      get: () => stored,
      set: (s: DesktopSettings) => {
        stored = s
      },
      restart: () => {
        restarts.push(restarts.length + 1)
        return restart()
      },
      posix: false,
    },
  }
}

describe('SettingsSaver', () => {
  it('saves a change the server does not read without a restart, and says saved, once', async () => {
    const s = new SettingsSaver()
    const a = app()
    await s.save(a.d, { closeToTray: false })
    expect(a.restarts).toHaveLength(0)
    expect(a.stored().closeToTray).toBe(false)
    expect(s.take()).toBe('saved')
    expect(s.take()).toBe('')
  })

  it('restarts the server for a server setting and says restarted once the restart completed, once', async () => {
    let release!: () => void
    const s = new SettingsSaver()
    const a = app(() => new Promise<void>((r) => (release = r)))
    const saving = s.save(a.d, { yolo: true })
    expect(a.restarts).toHaveLength(1)
    // While the restart is pending nothing is owed: an ask from the page that saved (its delayed mount, say) takes nothing
    // from the page that follows the restarted server.
    expect(s.take()).toBe('')
    release()
    expect((await saving).yolo).toBe(true)
    expect(s.take()).toBe('restarted')
    expect(s.take()).toBe('')
  })

  it('leaves an earlier outcome untaken behind when a new save begins', async () => {
    let release!: () => void
    const s = new SettingsSaver()
    const a = app(() => new Promise<void>((r) => (release = r)))
    await s.save(a.d, { closeToTray: false })
    // Not taken; a server setting is saved next: the pending restart owes nothing until it completes.
    const saving = s.save(a.d, { yolo: true })
    expect(s.take()).toBe('')
    release()
    await saving
    expect(s.take()).toBe('restarted')
  })

  it('takes the outcome back when the restart fails, keeps the settings, and the retry says saved without restarting', async () => {
    let fails = true
    const s = new SettingsSaver()
    const a = app(async () => {
      if (fails) throw new Error('the server did not start')
    })
    const failing = s.save(a.d, { yolo: true })
    expect(s.take()).toBe('') // nothing to take while it is pending
    await expect(failing).rejects.toThrow('did not start')
    expect(a.stored().yolo).toBe(true)
    expect(s.take()).toBe('') // and nothing after the failure
    fails = false
    // The same form again: nothing the server reads differs from what was kept, so nothing restarts, and the toast must not say so.
    await s.save(a.d, { yolo: true })
    expect(a.restarts).toHaveLength(1)
    expect(s.take()).toBe('saved')
  })

  it('refuses invalid settings before changing anything, and keeps the app\'s zoom level', async () => {
    const s = new SettingsSaver()
    const a = app()
    await expect(s.save(a.d, { dataDir: 'relative' })).rejects.toThrow('absolute')
    expect(a.restarts).toHaveLength(0)
    expect(a.stored().dataDir).not.toBe('relative')
    expect(s.take()).toBe('')
    await s.save(a.d, { closeToTray: false, zoomLevel: 3 } as Partial<DesktopSettings>)
    expect(a.stored().zoomLevel).toBe(0)
  })

  it("keeps the updates the app told (#290): a form loaded before one was told does not tell it again", async () => {
    const s = new SettingsSaver()
    const a = app()
    await s.save(a.d, { closeToTray: false })
    // The app tells an update, then a page loaded before it saves its form.
    const told = { ...a.stored(), updateAnnounced: '0.7.0-rc.12', updateDownloaded: '0.7.0-rc.12', updateFailedNoticed: '0.7.0-rc.11' }
    a.d.set(told)
    await s.save(a.d, { closeToTray: true, updateAnnounced: '', updateDownloaded: '', updateFailedNoticed: '' } as Partial<DesktopSettings>)
    expect(a.stored().closeToTray).toBe(true)
    expect(a.stored().updateAnnounced).toBe('0.7.0-rc.12')
    expect(a.stored().updateDownloaded).toBe('0.7.0-rc.12')
    expect(a.stored().updateFailedNoticed).toBe('0.7.0-rc.11')
  })

  it('a save made after Allow this folder holds the folder and does not restart for it', async () => {
    const s = new SettingsSaver()
    const a = app()
    // The press kept /srv/proj (allow.ts), with no restart: the Settings page then saves its form, which holds it (onSettings).
    a.d.set({ ...a.stored(), allowedRoots: [...a.stored().allowedRoots, '/srv/proj'] })
    await s.save(a.d, { closeToTray: false, allowedRoots: ['/home/me', '/srv/proj'] })
    expect(a.restarts).toHaveLength(0)
    expect(a.stored().allowedRoots).toEqual(['/home/me', '/srv/proj'])
    expect(s.take()).toBe('saved')
  })
})
