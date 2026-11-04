import { describe, expect, it, vi } from 'vitest'
import { EventEmitter } from 'node:events'
import { asBrowserChoice, browserArgv, candidates, findBrowser, installedBrowsers, openUrl, refusedProgram, type BrowserDeps } from '../src/browsers'

const URL_ = 'https://claude.ai/artifact/0f1e2d3c?x=1&y=$(id)'

/** A computer with the files given, whose spawn records every start and starts (or fails) as told. */
function computer(platform: NodeJS.Platform, files: string[], o: { dirs?: string[]; fail?: boolean; links?: Record<string, string> } = {}) {
  const spawned: Array<{ file: string; args: string[]; options: unknown }> = []
  const external: string[] = []
  const d: BrowserDeps = {
    platform,
    env: { PATH: '/usr/local/bin:/usr/bin:relative', ProgramFiles: 'C:\\Program Files', 'ProgramFiles(x86)': 'C:\\Program Files (x86)', LOCALAPPDATA: 'C:\\Users\\me\\AppData\\Local' },
    home: '/Users/me',
    isFile: (p) => files.includes(p),
    isDir: (p) => (o.dirs ?? []).includes(p),
    realpath: (p) => o.links?.[p] ?? p,
    spawn: (file, args, options) => {
      spawned.push({ file, args, options })
      const child = new EventEmitter() as EventEmitter & { unref: () => void }
      child.unref = vi.fn()
      queueMicrotask(() => child.emit(o.fail ? 'error' : 'spawn', o.fail ? new Error('ENOENT') : undefined))
      return child as never
    },
    openExternal: async (url) => {
      external.push(url)
    },
  }
  return { d, spawned, external }
}

describe('the argv for each browser on each OS', () => {
  const chrome = { name: 'chrome' as const }
  const edge2 = { name: 'edge' as const, profile: 'Profile 2' }
  it('is the program, the profile switch, then the URL as one argument (Windows, Linux)', () => {
    for (const platform of ['win32', 'linux'] as const) {
      expect(browserArgv('/p/chrome', chrome, URL_, platform)).toEqual({ file: '/p/chrome', args: [URL_] })
      expect(browserArgv('/p/msedge', edge2, URL_, platform)).toEqual({ file: '/p/msedge', args: ['--profile-directory=Profile 2', URL_] })
      expect(browserArgv('/p/brave', { name: 'brave', profile: 'Default' }, URL_, platform)).toEqual({ file: '/p/brave', args: ['--profile-directory=Default', URL_] })
      expect(browserArgv('/p/firefox', { name: 'firefox' }, URL_, platform)).toEqual({ file: '/p/firefox', args: [URL_] })
    }
  })
  it('is open with the app bundle on macOS, -n and --args for a profile', () => {
    expect(browserArgv('/Applications/Safari.app', { name: 'safari' }, URL_, 'darwin')).toEqual({ file: '/usr/bin/open', args: ['-a', '/Applications/Safari.app', URL_] })
    expect(browserArgv('/Applications/Google Chrome.app', chrome, URL_, 'darwin')).toEqual({ file: '/usr/bin/open', args: ['-a', '/Applications/Google Chrome.app', URL_] })
    expect(browserArgv('/Applications/Microsoft Edge.app', edge2, URL_, 'darwin')).toEqual({
      file: '/usr/bin/open',
      args: ['-n', '-a', '/Applications/Microsoft Edge.app', '--args', '--profile-directory=Profile 2', URL_],
    })
  })
  it('never passes a profile to Firefox or Safari', () => {
    expect(browserArgv('/p/firefox', { name: 'firefox', profile: 'work' }, URL_, 'linux').args).toEqual([URL_])
  })
})

describe('where each browser is looked for', () => {
  it('Windows: Program Files, then the x86 one, then the user\'s own; Safari nowhere', () => {
    const { d } = computer('win32', [])
    expect(candidates('edge', d)).toEqual([
      'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe',
      'C:\\Program Files\\Microsoft\\Edge\\Application\\msedge.exe',
      'C:\\Users\\me\\AppData\\Local\\Microsoft\\Edge\\Application\\msedge.exe',
    ])
    expect(candidates('chrome', d)[0]).toBe('C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe')
    expect(candidates('safari', d)).toEqual([])
  })
  it('macOS: the app bundle in /Applications, then ~/Applications', () => {
    const { d } = computer('darwin', [])
    expect(candidates('brave', d)).toEqual(['/Applications/Brave Browser.app', '/Users/me/Applications/Brave Browser.app'])
  })
  it('Linux: the program names on PATH, its absolute directories alone', () => {
    const { d } = computer('linux', [])
    expect(candidates('chrome', d)).toEqual(['/usr/local/bin/google-chrome-stable', '/usr/bin/google-chrome-stable', '/usr/local/bin/google-chrome', '/usr/bin/google-chrome'])
    expect(candidates('safari', d)).toEqual([])
  })
  it('finds the first there, and marks the browsers installed', () => {
    const { d } = computer('linux', ['/usr/bin/firefox', '/usr/bin/google-chrome'])
    expect(findBrowser('chrome', d)).toBe('/usr/bin/google-chrome')
    expect(installedBrowsers(d)).toEqual(['chrome', 'firefox'])
    const mac = computer('darwin', [], { dirs: ['/Users/me/Applications/Firefox.app', '/Applications/Safari.app'] }).d
    expect(installedBrowsers(mac)).toEqual(['firefox', 'safari'])
  })
})

describe('a program that resolves to a batch file', () => {
  it('is refused, as a .bat, a .cmd, or a link to one', () => {
    expect(refusedProgram('C:\\x\\chrome.cmd')).toBe(true)
    expect(refusedProgram('C:\\x\\CHROME.BAT')).toBe(true)
    expect(refusedProgram('C:\\x\\chrome.exe')).toBe(false)
    const chrome = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe'
    const { d } = computer('win32', [chrome], { links: { [chrome]: 'C:\\evil\\run.cmd' } })
    expect(findBrowser('chrome', d)).toBe(null)
    const linux = computer('linux', ['/usr/bin/firefox'], { links: { '/usr/bin/firefox': '/opt/ff/firefox.bat' } }).d
    expect(findBrowser('firefox', linux)).toBe(null)
  })
})

describe('openUrl', () => {
  it('starts the browser named once, without a shell, detached', async () => {
    const msedge = 'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe'
    const c = computer('win32', [msedge])
    expect(await openUrl(URL_, { name: 'edge', profile: 'Profile 2' }, c.d)).toBe('browser')
    expect(c.spawned).toEqual([{ file: msedge, args: ['--profile-directory=Profile 2', URL_], options: { shell: false, detached: true, stdio: 'ignore', windowsHide: false } }])
    expect(c.external).toEqual([])
  })
  it('falls back to the default browser when none is named, the one named is not here, or it did not start', async () => {
    const none = computer('linux', ['/usr/bin/firefox'])
    expect(await openUrl(URL_, null, none.d)).toBe('default')
    expect(await openUrl(URL_, { name: 'chrome' }, none.d)).toBe('missing')
    expect(none.spawned).toEqual([])
    expect(none.external).toEqual([URL_, URL_])
    const broken = computer('linux', ['/usr/bin/firefox'], { fail: true })
    expect(await openUrl(URL_, { name: 'firefox' }, broken.d)).toBe('failed')
    expect(broken.spawned).toHaveLength(1)
    expect(broken.external).toEqual([URL_])
  })
  it('refuses a link that is not http(s), starting nothing', async () => {
    const c = computer('linux', ['/usr/bin/firefox'])
    for (const bad of ['file:///etc/passwd', 'javascript:alert(1)', 'ms-settings:', 'not a url']) {
      await expect(openUrl(bad, { name: 'firefox' }, c.d)).rejects.toThrow(/refused/)
    }
    expect(c.spawned).toEqual([])
    expect(c.external).toEqual([])
  })
})

describe('asBrowserChoice', () => {
  it('takes the server\'s browsers and refuses anything else', () => {
    expect(asBrowserChoice({ name: 'edge', profile: 'Profile 2' })).toEqual({ name: 'edge', profile: 'Profile 2' })
    expect(asBrowserChoice({ name: 'firefox' })).toEqual({ name: 'firefox' })
    for (const bad of [null, 'edge', {}, { name: 'opera' }, { name: 'firefox', profile: 'work' }, { name: 'edge', profile: 'a"b' }, { name: 'edge', profile: 'a\nb' }, { name: 'edge', profile: ' x' }, { name: 'edge', profile: 'p'.repeat(101) }, { name: 'edge', profile: 3 }]) {
      expect(asBrowserChoice(bad)).toBe(null)
    }
  })
})
