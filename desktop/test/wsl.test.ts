import { mkdtempSync, readFileSync as readFile, writeFileSync, chmodSync, existsSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { describe, expect, it, vi } from 'vitest'
import { chooseDistro, decode, INSTALL_SCRIPT, parseList, parseWslconfigNetworking, windowsPathToWsl } from '../src/wsl'
import { SHELL_PATH_WAIT, wslAvailable, WslLauncher } from '../src/launcher-wsl'

const LIST = '  NAME            STATE           VERSION\r\n* Ubuntu-24.04    Running         2\r\n  Debian          Stopped         1\r\n'

describe('wsl', () => {
  it('decodes UTF-16LE and UTF-8 output', () => {
    expect(decode(Buffer.from(LIST, 'utf16le'))).toBe(LIST)
    expect(decode(Buffer.from(LIST, 'utf8'))).toBe(LIST)
  })
  it('lists the distributions with the default marked', () => {
    expect(parseList(LIST)).toEqual([
      { name: 'Ubuntu-24.04', default: true, state: 'Running', version: 2 },
      { name: 'Debian', default: false, state: 'Stopped', version: 1 },
    ])
  })
  it('chooses the named or default WSL 2 distribution and names what is wrong', () => {
    const list = parseList(LIST)
    expect(chooseDistro(list, '')).toEqual({ ok: true, distro: 'Ubuntu-24.04' })
    expect(chooseDistro(list, 'Debian')).toMatchObject({ ok: false, reason: expect.stringContaining('WSL 1') })
    expect(chooseDistro(list, 'Arch')).toMatchObject({ ok: false, reason: expect.stringContaining('not installed') })
    expect(chooseDistro([], '')).toMatchObject({ ok: false })
  })
  it('turns Windows paths into /mnt paths and reads .wslconfig', () => {
    expect(windowsPathToWsl('C:\\Users\\me\\Conductor')).toBe('/mnt/c/Users/me/Conductor')
    expect(windowsPathToWsl('/already/linux')).toBe('/already/linux')
    expect(parseWslconfigNetworking('[wsl2]\nmemory=8GB\nnetworkingMode=mirrored\n')).toBe('mirrored')
    expect(parseWslconfigNetworking('[experimental]\nnetworkingMode=mirrored\n')).toBe('')
    expect(INSTALL_SCRIPT).toContain('.local/share/conductor/bin')
    expect(INSTALL_SCRIPT).not.toContain('${')
  })
  it('detects WSL through a fake wsl.exe runner', async () => {
    const calls: string[][] = []
    const fake = async (args: string[]) => {
      calls.push(args)
      if (args[0] === '--status') return { ok: true, out: 'Default Version: 2' }
      if (args[0] === '-l') return { ok: true, out: LIST }
      if (args.some((a) => a.includes('__HOME__'))) return { ok: true, out: 'motd\n__HOME__/home/me\n__SHELL__/usr/bin/zsh\n' }
      if (args.some((a) => a.includes('__CONDUCTOR_PATH__'))) return { ok: true, out: 'zsh noise\n__CONDUCTOR_PATH__/home/me/.nvm/versions/node/v22/bin:/usr/bin\n' }
      return { ok: true, out: '' }
    }
    expect(await wslAvailable('', fake)).toEqual({ ok: true, distro: 'Ubuntu-24.04' })
    expect(await wslAvailable('', async () => ({ ok: false, out: '' }))).toMatchObject({ ok: false, reason: expect.stringContaining('not installed') })
    const l = new WslLauncher({ source: 'C:\\Program Files\\Conductor\\resources\\conductor-linux', distro: 'Ubuntu-24.04', version: 'v1', windowsHome: false, log: () => {}, run: fake })
    await l.prepare()
    const install = calls.find((c) => c[4] === '-c' && c[3] === 'sh')!
    expect(install.slice(0, 5)).toEqual(['-d', 'Ubuntu-24.04', '--exec', 'sh', '-c'])
    expect(install[install.length - 2]).toBe('/mnt/c/Program Files/Conductor/resources/conductor-linux')
    expect(l.linuxHome()).toBe('/home/me')
    expect(l.distro()).toBe('Ubuntu-24.04')
    // The PATH comes from the person's own shell, interactively, merged with the tool directories.
    const probe = calls.find((c) => c.some((a) => a.includes('__CONDUCTOR_PATH__')))!
    expect(probe.slice(0, 5)).toEqual(['-d', 'Ubuntu-24.04', '--exec', '/usr/bin/zsh', '-ilc'])
    const path = '/home/me/.nvm/versions/node/v22/bin:/usr/bin:/home/me/.local/bin:/home/me/go/bin:/usr/local/bin:/bin'
    expect(l.shellPath()).toBe(path)
    // Windows-shaped paths from an earlier build become the distribution's defaults; the shell PATH rides along.
    expect(l.linuxEnv({ CONDUCTOR_WORKBENCH_TOKEN: 't', CONDUCTOR_DATA_DIR: 'C:\\x' }, true, 'C:\\Users\\me')).toEqual({ CONDUCTOR_WORKBENCH_TOKEN: 't', CONDUCTOR_DATA_DIR: '/home/me/.local/share/conductor/data', CONDUCTOR_ALLOWED_ROOTS: '/home/me,/mnt/c/Users/me', CONDUCTOR_DEFAULT_CWD: '/home/me', CONDUCTOR_PATH: path })
    // Linux paths saved in Settings are the server's.
    const saved = l.linuxEnv({ CONDUCTOR_DATA_DIR: '/home/me/.conductor', CONDUCTOR_ALLOWED_ROOTS: '/home/me/code,/srv', CONDUCTOR_DEFAULT_CWD: '/home/me/code' }, false, 'C:\\Users\\me')
    expect(saved).toMatchObject({ CONDUCTOR_DATA_DIR: '/home/me/.conductor', CONDUCTOR_ALLOWED_ROOTS: '/home/me/code,/srv', CONDUCTOR_DEFAULT_CWD: '/home/me/code' })
    // The app's own directory, in the Windows profile, is named as the distribution mounts it; one it cannot name there is left out.
    expect(l.linuxEnv({ CONDUCTOR_DESKTOP_DIR: 'C:\\Users\\me\\AppData\\Roaming\\conductor-desktop' }, false, 'C:\\Users\\me').CONDUCTOR_DESKTOP_DIR).toBe('/mnt/c/Users/me/AppData/Roaming/conductor-desktop')
    expect(l.linuxEnv({ CONDUCTOR_DESKTOP_DIR: '\\\\server\\profiles\\me\\conductor-desktop' }, false, '')).not.toHaveProperty('CONDUCTOR_DESKTOP_DIR')
    expect(l.linuxEnv({}, false, '')).not.toHaveProperty('CONDUCTOR_DESKTOP_DIR')
    expect(WslLauncher.linuxRoots(true, 'C:\\Users\\me')).toEqual(['$HOME', '/mnt/c/Users/me'])
    expect(WslLauncher.linuxRoots(true, 'C:\\Users\\me', '/home/me', ['/home/me/code'])).toEqual(['/home/me/code', '/mnt/c/Users/me'])
    // The start script puts the shell's PATH first, then runs the server.
    expect(WslLauncher.startScript()).toContain('export PATH="$CONDUCTOR_PATH"')
    expect(WslLauncher.startScript()).toContain('unset CONDUCTOR_PATH')
    expect(WslLauncher.startScript()).toMatch(/exec "\$HOME\/.local\/share\/conductor\/bin\/conductor" "\$@"$/)
  })
  it('a shell that prints no PATH leaves the login shell\'s', async () => {
    const quiet = async (args: string[]) => {
      if (args.some((a) => a.includes('__HOME__'))) return { ok: true, out: '__HOME__/home/me\n__SHELL__/bin/bash\n' }
      return { ok: true, out: '' }
    }
    const l = new WslLauncher({ source: 'C:\\x\\conductor-linux', distro: 'Ubuntu', version: 'v1', windowsHome: false, log: () => {}, run: quiet })
    await l.prepare()
    expect(l.shellPath()).toBe('')
    expect(l.linuxEnv({}, false, '').CONDUCTOR_PATH).toBeUndefined()
  })
  it('a shell that prints nothing in time is asked again with more time, and again before a start of the server (#363)', async () => {
    // An interactive zsh that loads nvm takes seconds; one that printed nothing within the wait is asked once more with twice
    // the time, and a start of the server asks again while nothing was read, so that one slow start does not leave every
    // session without the agents under nvm.
    const waits: number[] = []
    let asked = 0
    const slow = async (args: string[], timeoutMs?: number) => {
      if (args.some((a) => a.includes('__HOME__'))) return { ok: true, out: '__HOME__/home/me\n__SHELL__/usr/bin/zsh\n' }
      if (args.some((a) => a.includes('__CONDUCTOR_PATH__'))) {
        waits.push(timeoutMs ?? 0)
        asked++
        return asked >= 3 ? { ok: true, out: '__CONDUCTOR_PATH__/home/me/.nvm/versions/node/v22/bin:/usr/bin\n' } : { ok: false, out: '' }
      }
      return { ok: true, out: '' }
    }
    const lines: string[] = []
    const l = new WslLauncher({ source: 'C:\\x\\conductor-linux', distro: 'Ubuntu', version: 'v1', windowsHome: false, log: (s) => lines.push(s), run: slow })
    await l.prepare()
    expect(waits).toEqual([SHELL_PATH_WAIT, SHELL_PATH_WAIT * 2])
    expect(l.shellPath()).toBe('')
    expect(lines.filter((s) => s.includes('printed no PATH within'))).toHaveLength(2)
    expect(lines.some((s) => s.includes("the login shell's PATH stands"))).toBe(true)
    // The server's start asks again, and the answer stands: the next start asks no more.
    await l.beforeStart()
    expect(waits).toHaveLength(3)
    expect(l.shellPath()).toBe('/home/me/.nvm/versions/node/v22/bin:/usr/bin:/home/me/.local/bin:/home/me/go/bin:/usr/local/bin:/bin')
    expect(l.linuxEnv({}, false, '').CONDUCTOR_PATH).toBe(l.shellPath())
    await l.beforeStart()
    expect(waits).toHaveLength(3)
  })
})

describe('WebRTC from inside WSL', () => {
  const fake = async (args: string[]) => {
    if (args.includes('hostname')) return { ok: true, out: '172.26.16.42 fe80::1 \n' }
    return { ok: true, out: '' }
  }

  it('reads the distribution address after a start, and passes the ICE variables only when forwarding', async () => {
    const l = new WslLauncher({ source: 'C:\\x\\conductor-linux', distro: 'Ubuntu', version: 'v1', windowsHome: false, log: () => {}, run: fake })
    expect(await l.address()).toBe('172.26.16.42')
    const none = new WslLauncher({ source: 'C:\\x\\conductor-linux', distro: 'Ubuntu', version: 'v1', windowsHome: false, log: () => {}, run: async () => ({ ok: false, out: '' }) })
    expect(await none.address()).toBe('')
    const env = l.linuxEnv({ CONDUCTOR_WORKBENCH_TOKEN: 't' }, false, '', { port: 7877, publicIp: '192.168.1.127' })
    expect(env.CONDUCTOR_ICE_UDP_PORT).toBe('7877')
    expect(env.CONDUCTOR_ICE_PUBLIC_IP).toBe('192.168.1.127')
    const plain = l.linuxEnv({ CONDUCTOR_WORKBENCH_TOKEN: 't' }, false, '')
    expect(plain.CONDUCTOR_ICE_UDP_PORT).toBeUndefined()
    expect(l.linuxEnv({}, false, '', { port: 7877, publicIp: '' }).CONDUCTOR_ICE_PUBLIC_IP).toBeUndefined()
  })

  it('the installer script adds the firewall rule for the ICE port and removes it', async () => {
    const { readFileSync } = await import('node:fs')
    const nsh = readFileSync(new URL('../build/installer.nsh', import.meta.url), 'utf8')
    expect(nsh).toContain('customInstall')
    expect(nsh).toContain('protocol=UDP localport=7877')
    expect(nsh).toContain('customUnInstall')
    expect(nsh).toContain('delete rule name="Conductor WebRTC (UDP 7877)"')
  })
})

describe('Allow this folder inside WSL', () => {
  const KEY = 'key-' + 'e'.repeat(60)
  it('keeps a folder added in the roots', () => {
    const l = new WslLauncher({ source: 'C:\\x\\conductor-linux', distro: 'Ubuntu', version: 'v1', windowsHome: false, log: () => {}, run: async () => ({ ok: true, out: '' }) })
    const env = l.linuxEnv({ CONDUCTOR_ALLOWED_ROOTS: '/home/me,/srv/added' }, false, '')
    expect(env.CONDUCTOR_ALLOWED_ROOTS).toBe('/home/me,/srv/added')
  })
  it.skipIf(process.platform === 'win32')("hands wsl.exe the first line on its stdin, and no secret in its environment", async () => {
    // A stand-in for wsl.exe that writes the first line of its stdin and the environment it was given.
    const dir = mkdtempSync(join(tmpdir(), 'wslenv-'))
    const line = join(dir, 'line.txt')
    const out = join(dir, 'env.txt')
    const exe = join(dir, 'wsl.sh')
    writeFileSync(exe, `#!/bin/sh\nIFS= read -r first\nprintf '%s' "$first" > '${line}'\nenv > '${out}'\n`)
    chmodSync(exe, 0o755)
    const l = new WslLauncher({ source: 'C:\\x\\conductor-linux', distro: 'Ubuntu', version: 'v1', windowsHome: false, log: () => {}, run: async () => ({ ok: true, out: '' }), exe })
    const child = l.spawn(['serve', '--secrets-stdin'], { CONDUCTOR_DATA_DIR: '/d' })
    child.stdin?.write(`{"workbenchToken":"tok-${KEY}","desktopKey":"${KEY}"}\n`)
    await new Promise((r) => child.once('exit', r))
    await vi.waitFor(() => expect(existsSync(out)).toBe(true))
    expect(readFile(line, 'utf8')).toBe(`{"workbenchToken":"tok-${KEY}","desktopKey":"${KEY}"}`)
    const env = readFile(out, 'utf8')
    expect(env).not.toContain(KEY)
    const names = env.split('\n').find((x) => x.startsWith('WSLENV='))?.split('=')[1]?.split(':') ?? []
    expect(names).toContain('CONDUCTOR_DATA_DIR')
    expect(names.filter((n) => n === 'CONDUCTOR_WORKBENCH_TOKEN' || n === 'CONDUCTOR_DESKTOP_KEY')).toEqual([])
  })

})
