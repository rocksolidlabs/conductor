import { execFile } from 'node:child_process'

/** The inbound rule for the ICE UDP port, as the installer names it (build/installer.nsh). */
export function firewallRuleName(port: number): string {
  return `Conductor WebRTC (UDP ${port})`
}

export type Runner = (file: string, args: string[], timeoutMs: number) => Promise<{ ok: boolean; out: string }>

/** runExe runs a program with fixed arguments and returns its output; '' and not ok on failure. */
export const runExe: Runner = (file, args, timeoutMs) =>
  new Promise((resolve) => {
    execFile(file, args, { timeout: timeoutMs, windowsHide: true }, (err, stdout, stderr) => {
      resolve({ ok: !err, out: `${stdout ?? ''}${stderr ?? ''}` })
    })
  })

/**
 * firewallRuleExists asks the Windows firewall for the rule by name. 'present', 'missing', or 'unknown' when netsh did not answer
 * (not Windows, or no netsh).
 */
export async function firewallRuleExists(port: number, run: Runner = runExe): Promise<'present' | 'missing' | 'unknown'> {
  const r = await run('netsh', ['advfirewall', 'firewall', 'show', 'rule', `name=${firewallRuleName(port)}`], 10_000)
  if (r.ok) return 'present'
  return /no rules match|No rules match/i.test(r.out) ? 'missing' : 'unknown'
}

/**
 * allowIceThroughFirewall adds the rule through an elevated netsh (one UAC prompt), with fixed arguments: nothing of the person's
 * input goes into the command line. It resolves when PowerShell has started the elevated process; the rule shows up a moment later.
 */
export function allowIceThroughFirewall(port: number, run: Runner = runExe): Promise<boolean> {
  const rule = firewallRuleName(port)
  const args = `advfirewall firewall add rule name="${rule}" dir=in action=allow protocol=UDP localport=${port}`
  return run('powershell.exe', ['-NoProfile', '-Command', `Start-Process netsh.exe -ArgumentList '${args}' -Verb RunAs -Wait`], 120_000).then((r) => r.ok)
}

/** What Settings shows of the forwarder. */
export interface IceStatus {
  /** Whether the app forwards ICE into WSL (Windows in NAT mode, a LAN address found). */
  forwarding: boolean
  port: number
  /** The Windows address the server advertises; '' when none was found. */
  publicIp: string
  /** The distribution's address packets go to; '' until the server has started. */
  wslAddress: string
  firewall: 'present' | 'missing' | 'unknown'
  /** WSL shares Windows' network (mirrored mode): viewers connect straight, nothing to forward, nothing said. */
  mirrored?: boolean
  /** Why viewers cannot connect straight yet, when they cannot: the app's own words, never a program's raw text (the log has it). */
  reason?: string
}
