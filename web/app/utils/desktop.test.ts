import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { reactive } from 'vue'
import { asDesktopZoom, zoomWords, plainCopy, desktopBridge, noticeWords, PLAIN_SWITCHYARD, SWITCHYARD_EXTRA, SWITCHYARD_SHAPE, switchyardProblem, tokenFromFragment, withoutTokenFragment, restartWords, updateBusyWords, updateFailedWords, updateGoneWords, updateReadyWords } from './desktop'

describe('desktop bridge', () => {
  it('is absent in a browser', () => {
    expect(desktopBridge()).toBeNull()
  })
  it('reads a token out of the fragment and drops it from the URL', () => {
    expect(tokenFromFragment('#token=abcdefghijklmnop0123456789')).toBe('abcdefghijklmnop0123456789')
    expect(tokenFromFragment('#other=1&token=abcdefghijklmnop0123456789')).toBe('abcdefghijklmnop0123456789')
    expect(tokenFromFragment('#token=short')).toBe('')
    expect(tokenFromFragment('')).toBe('')
    expect(tokenFromFragment('#token=../evil')).toBe('')
    expect(withoutTokenFragment('http://127.0.0.1:4312/#token=abcdefghijklmnop0123456789')).toBe('http://127.0.0.1:4312/')
    expect(withoutTokenFragment('http://127.0.0.1:4312/wall#other=1&token=abcdefghijklmnop0123456789')).toBe('http://127.0.0.1:4312/wall#other=1')
  })
})

// The Settings page's check before a save: the words the app's validate
// says (desktop/src/settings.ts PLAIN_SWITCHYARD, SWITCHYARD_EXTRA), for the
// same switchyards.
describe('switchyardProblem', () => {
  const words = 'the switchyard must be https:// unless it runs on this machine (localhost): over plain http:// what is published there travels in the clear and unverified'
  it('refuses a plain switchyard on another machine with the words why', () => {
    expect(PLAIN_SWITCHYARD).toBe(words)
    for (const server of ['http://yard.example:8080', 'http://192.168.1.10:8080', 'HTTP://yard.example', 'http://localhost.yard.example', 'http://127.0.0.1.yard.example', 'http://127.1:8080', 'http://0x7f.0.0.1', 'http://127.000.0.1', 'http://0.0.0.0:8080', 'http://[fe80::1]:8080', 'http://LOCALHOST:8080', 'http://LocalHost:8080/']) {
      expect(switchyardProblem({ switchyardEnabled: true, switchyardServer: server }), server).toBe(words)
    }
  })
  it('keeps one on this machine, an https one anywhere, and anything while publishing is off', () => {
    for (const server of ['', 'http://localhost:8080', 'http://localhost:8080/', 'http://127.0.0.1:8080', 'http://127.20.30.40', 'http://[::1]:8080', 'http://[::ffff:127.0.0.1]:8080', 'https://yard.example', 'https://192.168.1.10:8443']) {
      expect(switchyardProblem({ switchyardEnabled: true, switchyardServer: server }), server).toBe('')
    }
    expect(switchyardProblem({ switchyardEnabled: false, switchyardServer: 'http://yard.example' })).toBe('')
  })
  it('refuses one named with more than its address, before the plain http rule, and nothing of it while publishing is off', () => {
    const extra = 'the switchyard must be named by its address alone, with no user name, password, query or fragment; a host token has a field of its own'
    expect(SWITCHYARD_EXTRA).toBe(extra)
    for (const server of ['https:///me:pw@yard.example', 'https://me:pw@yard.example', 'https://me@yard.example', 'https://:pw@yard.example', 'https://@yard.example', 'https://:@yard.example', 'https://yard.example/?token=t', 'https://yard.example?', 'https://yard.example/#f', 'https://yard.example#', 'http://me:pw@localhost:9090', 'http://:pw@yard.example', 'http://127.0.0.1\\@yard.example']) {
      expect(switchyardProblem({ switchyardEnabled: true, switchyardServer: server }), server).toBe(extra)
      expect(switchyardProblem({ switchyardEnabled: false, switchyardServer: server }), server).toBe('')
    }
    for (const server of ['https://yard.example/prefix/', 'https://yard.example/team@two']) {
      expect(switchyardProblem({ switchyardEnabled: true, switchyardServer: server }), server).toBe('')
    }
  })
  // The URLs the app and its server must agree on: the app's tests (desktop/test/settings.test.ts) and internal/config's read the same list.
  it('says of each switchyard what the app says, and nothing of one the server takes', () => {
    const urls = JSON.parse(readFileSync(new URL('../../../internal/config/testdata/switchyard-urls.json', import.meta.url), 'utf8')) as Array<{ url: string; app: '' | 'plain' | 'extra' | 'shape' }>
    const words = { '': '', plain: PLAIN_SWITCHYARD, extra: SWITCHYARD_EXTRA, shape: SWITCHYARD_SHAPE }
    expect(SWITCHYARD_SHAPE).toBe('the switchyard must be an http(s) URL with a host, written as the server reads it: no space around it, no backslash, and a % only in an escape')
    expect(urls.length).toBeGreaterThan(40)
    for (const { url, app } of urls) {
      expect(switchyardProblem({ switchyardEnabled: true, switchyardServer: url }), url).toBe(words[app])
      expect(switchyardProblem({ switchyardEnabled: false, switchyardServer: url }), url).toBe('')
    }
  })
})

describe('noticeWords', () => {
  it('words the publishing notice and nothing else', () => {
    const words = noticeWords('publishing')
    expect(words?.title).toBe('Sessions are published to a switchyard')
    expect(words?.description).toContain('switchyard.rslabs.net')
    expect(words?.description).toContain('whose operator you trust')
    expect(words?.description).toContain('Settings → Switchyard')
    expect(noticeWords('')).toBeNull()
    expect(noticeWords('later')).toBeNull()
  })
})

describe('the update the desktop app downloaded (#290)', () => {
  it('says it is ready, and that it installs on quit or by a restart', () => {
    expect(updateReadyWords('0.7.0-rc.12')).toEqual({ title: 'Conductor 0.7.0-rc.12 is ready', description: 'It installs when you quit Conductor. Restart to update now, or later.' })
  })

  it('says an update that did not install, and where to look', () => {
    const w = updateFailedWords('0.7.0-rc.12')
    expect(w.title).toBe('Conductor 0.7.0-rc.12 did not install')
    expect(w.description).toContain("The app's log says why")
  })

  it('says an update offered that the app no longer holds, and that nothing restarted', () => {
    const w = updateGoneWords('0.7.0-rc.12')
    expect(w.title).toBe('Conductor 0.7.0-rc.12 is no longer ready')
    expect(w.description).toContain('nothing restarted')
  })

  it('says a restart put off while the app checks for updates, and that nothing restarted', () => {
    expect(updateBusyWords()).toEqual({ title: 'Conductor is checking for updates', description: 'Nothing restarted. Restart to update again in a moment.' })
  })

  it('says what a restart to update ends: the server, and the sessions running on this computer', () => {
    expect(restartWords('0.7.0-rc.12', 0)).toBe('Conductor quits to install 0.7.0-rc.12, and its server stops. No session runs on this computer, so none ends.')
    expect(restartWords('0.7.0-rc.12', 1)).toBe('Conductor quits to install 0.7.0-rc.12, and its server stops. The session running on this computer ends.')
    expect(restartWords('0.7.0-rc.12', 3)).toBe('Conductor quits to install 0.7.0-rc.12, and its server stops. The 3 sessions running on this computer end.')
    // Not counted is never none.
    expect(restartWords('0.7.0-rc.12', -1)).toBe('Conductor quits to install 0.7.0-rc.12, and its server stops. The sessions running on this computer, if any, end: they could not be counted.')
  })
})

describe('plainCopy', () => {
  it('turns a reactive form into a value the bridge can clone (#348)', () => {
    const form = reactive({ allowedRoots: ['/home/me', '/srv'], defaultCwd: '/home/me', yolo: false, nested: { a: [1, 2] } })
    expect(() => structuredClone(form)).toThrow()
    const plain = plainCopy(form)
    expect(() => structuredClone(plain)).not.toThrow()
    expect(plain).toEqual({ allowedRoots: ['/home/me', '/srv'], defaultCwd: '/home/me', yolo: false, nested: { a: [1, 2] } })
    expect(plain).not.toBe(form)
  })
})

describe("the desktop app's zoom (#431)", () => {
  it('reads what the app sends, and nothing else', () => {
    expect(asDesktopZoom({ level: 0.5, percent: 110, bound: '' })).toEqual({ level: 0.5, percent: 110, bound: '' })
    expect(asDesktopZoom({ level: 5, percent: 249, bound: 'largest' })).toEqual({ level: 5, percent: 249, bound: 'largest' })
    expect(asDesktopZoom({ level: -3, percent: 58, bound: 'smallest', announce: true })).toEqual({ level: -3, percent: 58, bound: 'smallest' })
    expect(asDesktopZoom({ level: 1, percent: 120, bound: 'huge' })).toEqual({ level: 1, percent: 120, bound: '' })
    for (const v of [null, undefined, 1, 'x', [], {}, { level: 1 }, { level: '1', percent: 120 }, { level: 1, percent: 12.5 }, { level: 1, percent: -1 }, { level: Infinity, percent: 120 }]) expect(asDesktopZoom(v)).toBeNull()
  })
  it('says the percent, and the bound when at one', () => {
    expect(zoomWords({ level: 0, percent: 100, bound: '' })).toBe('100%')
    expect(zoomWords({ level: 1.5, percent: 131, bound: '' })).toBe('131%')
    expect(zoomWords({ level: 5, percent: 249, bound: 'largest' })).toBe('249%, the largest')
    expect(zoomWords({ level: -3, percent: 58, bound: 'smallest' })).toBe('58%, the smallest')
  })
})
