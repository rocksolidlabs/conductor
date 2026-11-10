import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { UUID } from 'builder-util-runtime'
import { describe, expect, it } from 'vitest'

const desktop = join(__dirname, '..')

describe('packaging', () => {
  it('ships the licence, the notice and the third-party notices beside the app', () => {
    const yml = readFileSync(join(desktop, 'electron-builder.yml'), 'utf8').replace(/\r\n/g, '\n')
    for (const f of ['LICENSE', 'NOTICE', 'THIRD_PARTY_NOTICES']) {
      expect(yml).toContain(`  - from: ../${f}\n    to: ${f}\n`)
      expect(existsSync(join(desktop, '..', f))).toBe(true)
    }
    // Chromium's notices, which electron-builder leaves out of a macOS app on its own.
    expect(yml).toContain('  - from: node_modules/electron/dist/LICENSES.chromium.html\n    to: LICENSES.chromium.html\n')
    expect(existsSync(join(desktop, 'node_modules/electron/dist/LICENSES.chromium.html'))).toBe(true)
    // The notices name the app's own runtime, as scripts/notices.py writes them.
    const notices = readFileSync(join(desktop, '..', 'THIRD_PARTY_NOTICES'), 'utf8')
    expect(notices).toContain('The desktop app: Electron and its packages')
    expect(notices).toMatch(/\n {2}electron-updater \d/)
  })
})

const read = (p: string) => readFileSync(p, 'utf8').replace(/\r\n/g, '\n')
const script = () => read(join(desktop, 'build', 'installer.nsh'))
const templates = join(desktop, 'node_modules', 'app-builder-lib', 'templates', 'nsis')

/**
 * compiled is what makensis compiles of script for an installer of appId: the lines its !if, !else and !endif keep, with APP_ID
 * as electron-builder defines it. Any other conditional is refused, so that one added later is read here before it is trusted.
 */
function compiled(text: string, appId: string): string {
  const kept: string[] = []
  const stack: { on: boolean; parent: boolean }[] = []
  const on = () => stack.every((s) => s.on)
  for (const line of text.split('\n')) {
    const t = line.trim()
    const cond = /^!if\s+"([^"]*)"\s+(==|!=)\s+"([^"]*)"\s*$/.exec(t.replaceAll('${APP_ID}', appId))
    if (cond) {
      stack.push({ on: (cond[1] === cond[3]) === (cond[2] === '=='), parent: on() })
    } else if (t === '!else') {
      stack[stack.length - 1]!.on = !stack[stack.length - 1]!.on
    } else if (t === '!endif') {
      stack.pop()
    } else if (/^!(if|else|elseif)/.test(t)) {
      throw new Error(`a conditional this reader does not know: ${t}`)
    } else if (on() && !t.startsWith(';')) {
      kept.push(line)
    }
  }
  expect(stack).toEqual([])
  return kept.join('\n')
}

/** The body of the macro name in text, between its !macro and its !macroend. */
function macro(text: string, name: string): string {
  const m = new RegExp(`^\\s*!macro ${name}\\b[^\\n]*\\n([\\s\\S]*?)^\\s*!macroend`, 'm').exec(text)
  if (!m) throw new Error(`no macro ${name}`)
  return m[1]!
}

describe('the earlier app id replaced by the installer', () => {
  it("the earlier id's GUID in installer.nsh is electron-builder's for dev.conductor.app", () => {
    // electron-builder's namespace, as NsisTarget.js holds it, and its own UUID v5.
    const target = read(join(desktop, 'node_modules', 'app-builder-lib', 'out', 'targets', 'nsis', 'NsisTarget.js'))
    const ns = /ELECTRON_BUILDER_NS_UUID = builder_util_runtime_1\.UUID\.parse\("([0-9a-f-]{36})"\)/.exec(target)
    expect(ns).not.toBeNull()
    expect(target).toContain('builder_util_runtime_1.UUID.v5(appInfo.id, ELECTRON_BUILDER_NS_UUID)')
    const guid = UUID.v5('dev.conductor.app', UUID.parse(ns![1]!))
    const s = script()
    expect(s).toContain(`!define CONDUCTOR_EARLIER_GUID "${guid}"`)
    // The keys are the template's for that GUID (multiUser.nsh; UNINSTALL_APP_KEY is the GUID, which has no backslash).
    const multiUser = read(join(templates, 'multiUser.nsh'))
    expect(multiUser).toContain('!define /ifndef INSTALL_REGISTRY_KEY "Software\\${APP_GUID}"')
    expect(multiUser).toContain('!define /ifndef UNINSTALL_REGISTRY_KEY "Software\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\${UNINSTALL_APP_KEY}"')
    expect(s).toContain('!define CONDUCTOR_EARLIER_INSTALL_KEY "Software\\${CONDUCTOR_EARLIER_GUID}"')
    expect(s).toContain('!define CONDUCTOR_EARLIER_UNINSTALL_KEY "Software\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\${CONDUCTOR_EARLIER_GUID}"')
    // Neither the successor's GUID nor staging's: the move looks for the earlier id's entry alone.
    for (const id of ['io.rocksolidlabs.conductor', 'io.rocksolidlabs.conductor.staging']) expect(s).not.toContain(UUID.v5(id, UUID.parse(ns![1]!)))
  })

  it('the move is in customUnInstallCheck, not customInit', () => {
    const s = compiled(script(), 'io.rocksolidlabs.conductor')
    // customInit runs in .onInit, before any page: the rc would be gone before the licence was accepted.
    expect(s).not.toMatch(/customInit|\.onInit/)
    const check = macro(s, 'customUnInstallCheck')
    expect(check).toContain('!insertmacro conductorReplaceEarlier HKCU "/currentuser"')
    expect(check).toContain('!insertmacro conductorReplaceEarlier HKLM "/allusers"')
    // Defining it replaces the template's check of this id's earlier uninstall, which it makes first, unchanged.
    expect(check.indexOf('$(uninstallFailed): $R0')).toBeLessThan(check.indexOf('conductorReplaceEarlier'))
    expect(check).toContain('Uninstall was not successful. Uninstaller error code: $R0.')
    // Where electron-builder inserts it: handleUninstallResult, for the installer's own context, which the install section calls
    // after its running-app check and before any file is copied.
    const util = read(join(templates, 'include', 'installUtil.nsh'))
    const handle = util.slice(util.indexOf('Function handleUninstallResult'), util.indexOf('FunctionEnd', util.indexOf('Function handleUninstallResult')))
    expect(handle).toMatch(/"SHELL_CONTEXT"\n\s*!ifmacrodef customUnInstallCheck\n\s*!insertmacro customUnInstallCheck\n\s*Return/)
    const section = read(join(templates, 'installSection.nsh'))
    const at = (s: string) => {
      const i = section.indexOf(s)
      expect(i, s).toBeGreaterThanOrEqual(0)
      return i
    }
    expect(at('!insertmacro CHECK_APP_RUNNING')).toBeLessThan(at('!insertmacro handleUninstallResult SHELL_CONTEXT'))
    expect(at('!insertmacro handleUninstallResult SHELL_CONTEXT')).toBeLessThan(at('!insertmacro installApplicationFiles'))
  })

  it('the move is compiled only for io.rocksolidlabs.conductor', () => {
    const successor = compiled(script(), 'io.rocksolidlabs.conductor')
    expect(successor).toContain('!macro customUnInstallCheck')
    expect(successor).toContain('3648324d-47e0-5ae4-adc7-00ce07fef04a')
    // The open-source rcs and a staging installer hold none of it, and keep the firewall rule.
    for (const id of ['dev.conductor.app', 'io.rocksolidlabs.conductor.staging', 'io.rocksolidlabs.conductor.other']) {
      const other = compiled(script(), id)
      for (const word of ['customUnInstallCheck', 'CONDUCTOR_EARLIER', 'conductorEarlier', 'conductorReplaceEarlier', 'ExecWait', 'DeleteRegKey', 'Abort', '3648324d']) {
        expect(other, `${id}: ${word}`).not.toContain(word)
      }
      expect(other).toContain('!macro customInstall\n')
      expect(other).toContain('!macro customUnInstall\n')
    }
  })

  it('the move runs a copy of the uninstaller with --updated and _?=', () => {
    const replace = macro(compiled(script(), 'io.rocksolidlabs.conductor'), 'conductorReplaceEarlier')
    // The entry's uninstaller copied into $PLUGINSDIR, the copy run in place against the rc's folder: ExecWait returns once the
    // removal has ended, and --updated takes the move-aside path that restores what it moved at a file it cannot move.
    expect(replace).toContain('!insertmacro copyFile "$conductorEarlierUninstaller" "$PLUGINSDIR\\conductor-earlier-uninstaller.exe"')
    expect(replace).toContain(`ExecWait '"$PLUGINSDIR\\conductor-earlier-uninstaller.exe" /S /KEEP_APP_DATA --updated \${MODE} _?=$conductorEarlierDir' $conductorEarlierExit`)
    expect(replace.match(/ExecWait/g)).toHaveLength(1)
    expect(replace.indexOf('copyFile')).toBeLessThan(replace.indexOf('ExecWait'))
    // The folder: the entry's InstallLocation, else the uninstaller's own folder.
    expect(replace).toContain('ReadRegStr $conductorEarlierDir ${ROOT} "${CONDUCTOR_EARLIER_INSTALL_KEY}" InstallLocation')
    expect(replace).toMatch(/\$\{If\} \$conductorEarlierDir == ""\n\s*\$\{AndIf\} \$conductorEarlierUninstaller != ""\n\s*Push \$conductorEarlierUninstaller\n\s*Call GetFileParent\n\s*Pop \$conductorEarlierDir/)
    // Five tries a second apart, as the template's loop makes, then the install stops.
    expect(replace).toContain('${OrIf} $conductorEarlierTry >= 5')
    expect(replace).toContain('Sleep 1000')
    const failed = 'The earlier Conductor could not be removed; nothing was changed. Close it and run this installer again.'
    expect(script()).toContain(`!define CONDUCTOR_EARLIER_FAILED "${failed}"`)
    expect(replace).toMatch(/\$\{If\} \$conductorEarlierExit != 0\n[^\n]*\n\s*!insertmacro conductorEarlierStop "\$\{CONDUCTOR_EARLIER_FAILED\}"/)
    // A missing uninstaller: a stale entry over a folder gone is removed; with the folder there, the install stops.
    expect(replace).toMatch(/\$\{OrIfNot\} \$\{FileExists\} "\$conductorEarlierDir\\\*\.\*"\n[^\n]*\n\s*DeleteRegKey \$\{ROOT\} "\$\{CONDUCTOR_EARLIER_UNINSTALL_KEY\}"\n\s*DeleteRegKey \$\{ROOT\} "\$\{CONDUCTOR_EARLIER_INSTALL_KEY\}"\n\s*\$\{Else\}\n\s*!insertmacro conductorEarlierStop "\$\{CONDUCTOR_EARLIER_FAILED\}"/)
    // HKLM's needs this installer elevated, checked before anything is changed.
    const check = macro(compiled(script(), 'io.rocksolidlabs.conductor'), 'customUnInstallCheck')
    expect(script()).toContain('!define CONDUCTOR_EARLIER_FOR_EVERYONE "An earlier Conductor is installed for everyone on this computer. Run this installer as administrator to replace it."')
    expect(check).toMatch(/!insertmacro conductorEarlierFound HKLM "\$\{CONDUCTOR_EARLIER_UNINSTALL_KEY\}"\n\s*\$\{IfNot\} \$\{Errors\}\n\s*\$\{AndIfNot\} \$\{UAC_IsAdmin\}\n\s*!insertmacro conductorEarlierStop "\$\{CONDUCTOR_EARLIER_FOR_EVERYONE\}"/)
    expect(check.indexOf('CONDUCTOR_EARLIER_FOR_EVERYONE')).toBeLessThan(check.indexOf('conductorReplaceEarlier HKCU'))
  })

  it('the removal is checked before anything is copied', () => {
    const s = compiled(script(), 'io.rocksolidlabs.conductor')
    const replace = macro(s, 'conductorReplaceEarlier')
    const after = replace.slice(replace.indexOf('${If} $conductorEarlierExit != 0'))
    // Exit 0 is not taken on trust: both keys gone from that root, the rc's Conductor.exe and uninstaller gone from its folder.
    const partly = '!insertmacro conductorEarlierStop "${CONDUCTOR_EARLIER_PARTLY}"'
    expect(script()).toContain('!define CONDUCTOR_EARLIER_PARTLY "The earlier Conductor was not fully removed. Run this installer again."')
    for (const key of ['CONDUCTOR_EARLIER_UNINSTALL_KEY', 'CONDUCTOR_EARLIER_INSTALL_KEY']) {
      expect(after).toMatch(new RegExp(`!insertmacro conductorEarlierFound \\$\\{ROOT\\} "\\$\\{${key}\\}"\\n\\s*\\$\\{IfNot\\} \\$\\{Errors\\}\\n\\s*${partly.replace(/[$.{}"]/g, '\\$&')}`))
    }
    expect(after).toMatch(/\$\{If\} \$\{FileExists\} "\$conductorEarlierDir\\Conductor\.exe"\n\s*\$\{OrIf\} \$\{FileExists\} "\$conductorEarlierUninstaller"\n\s*!insertmacro conductorEarlierStop/)
    // A key is found by enumerating it, which fails only where it is not.
    expect(macro(s, 'conductorEarlierFound')).toMatch(/ClearErrors\n\s*EnumRegKey \$conductorEarlierProbe \$\{ROOT\} `\$\{KEY\}` 0/)
    // A stop ends the install there, before installApplicationFiles: Abort in the install section.
    const stop = macro(s, 'conductorEarlierStop')
    expect(stop).toContain('Abort `${MESSAGE}`')
    expect(stop).toContain('MessageBox MB_OK|MB_ICONSTOP `${MESSAGE}` /SD IDOK')
    // The script copies nothing of the app itself: the template's installApplicationFiles does, after the check.
    expect(s).not.toMatch(/^\s*(File|SetOutPath)\b/m)
  })
})
