import { describe, expect, it } from 'vitest'
import type { AgentInfo } from '~/composables/useSessions'
import { agentItem, checkingBadge, identity, isAvailable, launchListNote, misidentified, notInstalled, notInstalledBadge, notInstalledTitle, serverAgents, settleChecks } from './agents'

const agent = (over: Partial<AgentInfo>): AgentInfo => ({ id: 'a', name: 'Agent', command: ['a'], allowArgs: true, ...over })

describe('agent availability', () => {
  it('treats a missing flag (an older server) as available', () => {
    expect(isAvailable({ available: undefined })).toBe(true)
    expect(isAvailable({ available: true })).toBe(true)
    expect(isAvailable({ available: false })).toBe(false)
  })

  it('keeps only the installed agents for the server tab', () => {
    expect(serverAgents([agent({ id: 'x', available: true }), agent({ id: 'y', available: false }), agent({ id: 'z' })]).map((a) => a.id)).toEqual(['x', 'z'])
  })

  it('names the host, or the server when it is unknown', () => {
    expect(notInstalled('build-1')).toBe('Not installed on build-1')
    expect(notInstalled('')).toBe('Not installed on the server')
  })

  it('marks an unavailable agent in a select without disabling it, and shows the bundled icon or the generic one', () => {
    expect(agentItem(agent({ id: 'claude', name: 'Claude Code', icon: 'i-lucide-sparkles', available: false }), 'build-1')).toEqual({ label: 'Claude Code · not installed on build-1', value: 'claude', icon: 'i-lucide-circle-off' })
    expect(agentItem(agent({ id: 'claude', name: 'Claude Code', icon: 'i-lucide-sparkles', available: true }), 'build-1')).toEqual({ label: 'Claude Code', value: 'claude', icon: 'i-lucide-sparkles' })
    expect(agentItem(agent({ id: 'sh', name: 'Shell', icon: 'i-lucide-not-bundled' }), 'build-1')).toEqual({ label: 'Shell', value: 'sh', icon: 'i-lucide-bot' })
  })

  it('keeps the host name as the server gives it', () => {
    expect(agentItem(agent({ name: 'Cursor CLI', available: false }), 'Build-PC').label).toBe('Cursor CLI · not installed on Build-PC')
    expect(agentItem(agent({ name: 'Cursor CLI', available: false }), '').label).toBe('Cursor CLI · not installed on the server')
  })

  it('says how the server judged it: a name on its PATH, any program of that name counting, or a path', () => {
    expect(notInstalledTitle('cursor-agent')).toBe("No program named cursor-agent is on the server's PATH (any program of that name counts as installed)")
    expect(notInstalledTitle('/opt/agent/bin/agent')).toBe('/opt/agent/bin/agent was not found on the server')
  })
})

describe('agent identity', () => {
  const probed = (over: Partial<NonNullable<AgentInfo['identity']>>) => ({ command: ['goose'], identity: { ran: true, identified: false, verified: false, name: 'Goose', ...over } })
  it('names an identified agent with its version', () => {
    const r = identity({ command: ['claude'], identity: { ran: true, identified: true, verified: true, name: 'Claude Code', version: '2.1.287' } }, 'build-1')
    expect(r).toMatchObject({ state: 'ok', label: 'Claude Code 2.1.287' })
    expect(r.title).toContain('claude on build-1 is Claude Code 2.1.287')
  })
  it('names an impostor by what it printed', () => {
    const r = identity(probed({ impostor: true, output: 'goose version: v3.22.1' }), 'build-1')
    expect(r.state).toBe('impostor')
    expect(r.label).toBe('Not Goose')
    expect(r.title).toContain('"goose version: v3.22.1"')
    expect(misidentified(probed({ impostor: true }).identity)).toBe(true)
  })
  it('refuses nothing on an unverified pattern, and says pending, failed or unprobed', () => {
    expect(identity(probed({ output: 'goose 1.0.0-beta' }), '').state).toBe('unidentified')
    expect(misidentified(probed({ output: 'goose 1.0.0-beta' }).identity)).toBe(false)
    expect(misidentified(probed({ verified: true }).identity)).toBe(true)
    expect(identity(probed({ ran: false, pending: true }), '').state).toBe('pending')
    expect(identity(probed({ ran: false, error: 'timeout' }), '').state).toBe('failed')
    expect(identity({ command: ['sh'] }, '').state).toBe('unprobed')
  })
  it('marks a misidentified agent in a select', () => {
    expect(agentItem(agent({ id: 'goose', name: 'Goose', available: true, identity: { ran: true, identified: false, impostor: true, verified: false, name: 'Goose' } }), 'build-1').label).toBe('Goose · not Goose on build-1')
  })
})

describe('a program found on Windows from inside WSL', () => {
  it('is named, not called installed', () => {
    expect(notInstalled('box', '/mnt/c/Users/me/AppData/Roaming/npm/codex')).toBe('Found on Windows, not in WSL')
    expect(notInstalledTitle('codex', '/mnt/c/Users/me/AppData/Roaming/npm/codex')).toContain('install codex inside the WSL distribution')
    expect(notInstalled('box')).toBe('Not installed on box')
  })
})

describe('a program that only shares the agent\'s name', () => {
  const impostor = { command: ['goose'], identity: { ran: true, identified: false, impostor: true, verified: false, name: 'Goose', output: 'goose version:v3.5.3' } }
  it('says which program is there instead, and that the agent is not installed', () => {
    expect(notInstalledBadge(impostor, 'box')).toEqual({
      label: 'Not installed on box',
      title: 'goose on box is another program, not Goose: it printed "goose version:v3.5.3". Install Goose to launch it.',
      notAgent: true,
    })
  })
  it('is worded as a plain missing program otherwise', () => {
    expect(notInstalledBadge({ command: ['cursor-agent'] }, '')).toEqual({ label: 'Not installed on the server', title: notInstalledTitle('cursor-agent'), notAgent: false })
    // A verified probe that matched nothing is not an impostor: the agent stays offered, with its warning.
    expect(notInstalledBadge({ command: ['claude'], identity: { ran: true, identified: false, verified: true, name: 'Claude Code', output: 'build 3' } }, 'box').notAgent).toBe(false)
  })
})

describe("the Launch dialog's note where its agents go", () => {
  it('says it is checking while the first catalog is under way, never "no agents"', () => {
    expect(launchListNote({ loading: true, known: 0, offered: 0, onServer: true })).toBe('checking')
  })
  it('says nothing once it lists some, a refresh under way included', () => {
    expect(launchListNote({ loading: true, known: 3, offered: 2, onServer: true })).toBe(null)
    expect(launchListNote({ loading: false, known: 3, offered: 2, onServer: true })).toBe(null)
  })
  it('tells none installed here from none at all', () => {
    expect(launchListNote({ loading: false, known: 3, offered: 0, onServer: true })).toBe('none-installed')
    expect(launchListNote({ loading: false, known: 0, offered: 0, onServer: true })).toBe('none')
    expect(launchListNote({ loading: false, known: 0, offered: 0, onServer: false })).toBe('none')
  })
})

describe('agents still being checked (#704)', () => {
  it('says what is checked: the program looked up, or its version flag run', () => {
    expect(checkingBadge(agent({ command: ['codex'] }), 'build-1')).toEqual({ label: 'Checking…', title: 'Looking for codex on build-1' })
    expect(checkingBadge(agent({ command: ['codex'], identity: { ran: false, pending: true, identified: false, verified: true } }), '').label).toBe('Checking the version…')
  })

  /** A read the test answers by hand: one deferred answer per call, in the order they were asked. */
  function reads() {
    const asked: Array<{ id: string; answer: (a: { id: string; checking?: boolean }) => void; fail: () => void }> = []
    const read = (id: string) =>
      new Promise<{ id: string; checking?: boolean }>((resolve, reject) => asked.push({ id, answer: resolve, fail: () => reject(new Error('gone')) }))
    return { asked, read }
  }
  const tick = () => new Promise((r) => setTimeout(r, 0))

  it('fills each agent in as its read answers, two at a time, while a held one waits', async () => {
    const { asked, read } = reads()
    const applied: string[] = []
    const done = settleChecks(['held', 'b', 'c'], read, (a) => applied.push(`${a.id}:${a.checking ? 'checking' : 'known'}`), { stop: () => false, failed: () => {} })
    await tick()
    expect(asked.map((r) => r.id)).toEqual(['held', 'b'])
    asked[1]!.answer({ id: 'b' })
    await tick()
    // b is known; c takes its place while held is still asked.
    expect(applied).toEqual(['b:known'])
    expect(asked.map((r) => r.id)).toEqual(['held', 'b', 'c'])
    asked[2]!.answer({ id: 'c' })
    await tick()
    expect(applied).toEqual(['b:known', 'c:known'])
    // The held one comes back still checking: it is shown so and asked again.
    asked[0]!.answer({ id: 'held', checking: true })
    await tick()
    expect(applied).toEqual(['b:known', 'c:known', 'held:checking'])
    expect(asked.map((r) => r.id)).toEqual(['held', 'b', 'c', 'held'])
    asked[3]!.answer({ id: 'held' })
    await done
    expect(applied).toEqual(['b:known', 'c:known', 'held:checking', 'held:known'])
  })

  it('stops when told, reports a failed read, and asks no agent more than its reads', async () => {
    const { asked, read } = reads()
    let stopped = false
    const applied: string[] = []
    const failed: string[] = []
    const done = settleChecks(['a', 'b'], read, (a) => applied.push(a.id), { stop: () => stopped, failed: (id) => failed.push(id), reads: 2 })
    await tick()
    asked[1]!.fail()
    await tick()
    expect(failed).toEqual(['b'])
    asked[0]!.answer({ id: 'a', checking: true })
    await tick()
    asked[2]!.answer({ id: 'a', checking: true })
    await done
    // Two reads of a, both still checking: it is left so.
    expect(asked.map((r) => r.id)).toEqual(['a', 'b', 'a'])
    expect(applied).toEqual(['a', 'a'])

    const again = reads()
    const late: string[] = []
    const done2 = settleChecks(['x', 'y', 'z'], again.read, (a) => late.push(a.id), { stop: () => stopped, failed: (id) => late.push(`failed:${id}`) })
    await tick()
    stopped = true
    again.asked[0]!.answer({ id: 'x' })
    again.asked[1]!.fail()
    await done2
    // A newer read of the list took over: nothing is applied, failed or asked after it.
    expect(late).toEqual([])
    expect(again.asked.map((r) => r.id)).toEqual(['x', 'y'])
  })
})
