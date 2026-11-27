import { describe, expect, it } from 'vitest'
import type { AgentInfo } from '~/composables/useSessions'
import { agentItem, CHECK_READ_DEADLINE_MS, checkBadge, identity, isAvailable, launchListNote, misidentified, notInstalled, notInstalledBadge, notInstalledTitle, pageRequests, serverAgents, settleChecks, SHELL_ID, stillLookedUp, welcomeOffer } from './agents'
import type { After } from './capacity'

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
  it('says what is checked, and that a check given up did not answer, with no state half found', () => {
    const pending = { ran: false, pending: true, identified: false, verified: true }
    expect(checkBadge(agent({ command: ['codex'], checking: true }), 'build-1', false)).toEqual({ state: 'checking', label: 'Checking…', title: 'Looking for codex on build-1' })
    expect(checkBadge(agent({ command: ['codex'], checking: true, identity: pending }), '', false)?.label).toBe('Checking the version…')
    // Given up, the card says so, whatever the last read left: no spinner, no "Checking the version…".
    for (const a of [agent({ command: ['codex'], checking: true, identity: pending }), agent({ command: ['codex'] })]) {
      expect(checkBadge(a, 'build-1', true)).toEqual({
        state: 'unanswered',
        label: 'Check did not answer',
        title: 'The server did not answer the check of codex on build-1; the page checks again when it is opened or refreshed',
      })
    }
    // Settled, or shown from an expired answer while it is read again: the agent's own state is shown.
    expect(checkBadge(agent({ command: ['codex'] }), '', false)).toBeNull()
    expect(checkBadge(agent({ command: ['codex'], recheck: true, available: false }), '', false)).toBeNull()
  })

  /** A read the test answers by hand: one deferred answer per call, in the order they were asked, with the signal it was given. */
  function reads() {
    const asked: Array<{ id: string; signal: AbortSignal; answer: (a: { id: string; checking?: boolean }) => void; fail: () => void }> = []
    const read = (id: string, signal: AbortSignal) =>
      new Promise<{ id: string; checking?: boolean }>((resolve, reject) => {
        asked.push({ id, signal, answer: resolve, fail: () => reject(new Error('gone')) })
        signal.addEventListener('abort', () => reject(new Error('aborted')))
      })
    return { asked, read }
  }
  /** The deadlines' timers, which the test fires by hand. */
  function timers() {
    const set: Array<{ ms: number; fn: () => void; on: boolean }> = []
    const after: After = (ms, fn) => {
      const t = { ms, fn, on: true }
      set.push(t)
      return () => (t.on = false)
    }
    return { set, after }
  }
  const tick = () => new Promise((r) => setTimeout(r, 0))
  const live = () => new AbortController().signal

  it('fills each agent in as its read answers, two at a time, while a held one waits', async () => {
    const { asked, read } = reads()
    const applied: string[] = []
    const done = settleChecks(['held', 'b', 'c'], read, (a) => applied.push(`${a.id}:${a.checking ? 'checking' : 'known'}`), { signal: live(), gaveUp: () => {} })
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

  it('puts an agent still checking behind the agents not yet read: two slow ones do not hold a ready one', async () => {
    const { asked, read } = reads()
    const applied: string[] = []
    const done = settleChecks(['slow1', 'slow2', 'ready'], read, (a) => applied.push(`${a.id}:${a.checking ? 'checking' : 'known'}`), { signal: live(), gaveUp: () => {} })
    await tick()
    asked[0]!.answer({ id: 'slow1', checking: true })
    asked[1]!.answer({ id: 'slow2', checking: true })
    await tick()
    // ready is read before either slow one is read again.
    expect(asked.map((r) => r.id)).toEqual(['slow1', 'slow2', 'ready', 'slow1'])
    asked[2]!.answer({ id: 'ready' })
    await tick()
    expect(applied).toEqual(['slow1:checking', 'slow2:checking', 'ready:known'])
    expect(asked.map((r) => r.id)).toEqual(['slow1', 'slow2', 'ready', 'slow1', 'slow2'])
    asked[3]!.answer({ id: 'slow1' })
    asked[4]!.answer({ id: 'slow2' })
    await done
    expect(applied.slice(3)).toEqual(['slow1:known', 'slow2:known'])
  })

  it('gives an agent up when its reads run out or one fails, and the others go on', async () => {
    const { asked, read } = reads()
    const applied: string[] = []
    const gaveUp: string[] = []
    const done = settleChecks(['a', 'b', 'c'], read, (a) => applied.push(a.id), { signal: live(), gaveUp: (id) => gaveUp.push(id), reads: 2 })
    await tick()
    asked[1]!.fail()
    await tick()
    expect(gaveUp).toEqual(['b'])
    asked[0]!.answer({ id: 'a', checking: true })
    await tick()
    expect(asked.map((r) => r.id)).toEqual(['a', 'b', 'c', 'a'])
    asked[2]!.answer({ id: 'c' })
    asked[3]!.answer({ id: 'a', checking: true })
    await done
    // Two reads of a, both still checking: it is given up.
    expect(gaveUp).toEqual(['b', 'a'])
    expect(applied).toEqual(['a', 'c', 'a'])
  })

  it('aborts a read past its deadline and gives the agent up', async () => {
    const { asked, read } = reads()
    const t = timers()
    const gaveUp: string[] = []
    const done = settleChecks(['never', 'b'], read, () => {}, { signal: live(), gaveUp: (id) => gaveUp.push(id), after: t.after })
    await tick()
    expect(t.set.map((x) => x.ms)).toEqual([CHECK_READ_DEADLINE_MS, CHECK_READ_DEADLINE_MS])
    asked[1]!.answer({ id: 'b' })
    await tick()
    // b's deadline was cancelled by its answer; never's fires.
    expect(t.set.map((x) => x.on)).toEqual([true, false])
    t.set[0]!.fn()
    await done
    expect(asked[0]!.signal.aborted).toBe(true)
    expect(gaveUp).toEqual(['never'])
  })

  it('aborts the reads under way when its signal is, and applies, gives up or asks nothing after', async () => {
    const { asked, read } = reads()
    const ctl = new AbortController()
    const late: string[] = []
    const done = settleChecks(['x', 'y', 'z'], read, (a) => late.push(a.id), { signal: ctl.signal, gaveUp: (id) => late.push(`gave up:${id}`) })
    await tick()
    ctl.abort()
    await done
    expect(asked.map((r) => [r.id, r.signal.aborted])).toEqual([
      ['x', true],
      ['y', true],
    ])
    expect(late).toEqual([])
  })

  it("starts each read's generation over the one before, and hands on answers only while the page lives", async () => {
    const requests = pageRequests()
    const first = requests.next()
    const second = requests.next()
    expect([first.aborted, second.aborted]).toEqual([true, false])
    await expect(requests.live(Promise.resolve('hidden'))).resolves.toBe('hidden')
    await expect(requests.live(Promise.reject(new Error('refused')))).rejects.toThrow('refused')
    expect(requests.left()).toBe(false)
  })

  it('once the page is left, aborts its reads, starts none, and lets no answer still to come settle (Codex round 2)', async () => {
    const requests = pageRequests()
    const gen = requests.next()
    let answer = (_: string) => {}
    let refuse = (_: Error) => {}
    const settled: string[] = []
    requests.live(new Promise<string>((r) => (answer = r))).then((v) => settled.push(`answered:${v}`), () => settled.push('failed'))
    requests.live(new Promise<string>((_, r) => (refuse = r))).then(() => settled.push('answered'), (e: Error) => settled.push(`failed:${e.message}`))
    requests.end()
    expect(requests.left()).toBe(true)
    expect(gen.aborted).toBe(true)
    expect(requests.next().aborted).toBe(true)
    answer('hidden')
    refuse(new Error('refused'))
    await tick()
    expect(settled).toEqual([])
  })
})

describe("the welcome's offer (UX3)", () => {
  const found = (version?: string) => ({ ran: true, identified: true, verified: true, name: 'Agent', version })
  const ids = (o: ReturnType<typeof welcomeOffer>) => (o.state === 'found' ? { main: o.main.id, named: o.named.map((a) => a.id), rest: o.rest } : o.state)

  it("names the first agent the server can launch, in the catalog's order, with its version, never one not installed", () => {
    const list = [
      agent({ id: 'gone', name: 'Gone', available: false }),
      agent({ id: 'one', name: 'One', available: true, identity: found('2.1.318') }),
      agent({ id: 'two', name: 'Two', available: true }),
    ]
    expect(welcomeOffer(list)).toEqual({ state: 'found', main: { id: 'one', name: 'One', version: '2.1.318' }, named: [{ id: 'two', name: 'Two', version: undefined }], rest: 0 })
  })

  it('never makes the built-in shell the main button: it goes to the Or line, and a machine with only the shell has none', () => {
    const shell = agent({ id: SHELL_ID, name: 'Shell', available: true })
    expect(ids(welcomeOffer([shell, agent({ id: 'one', available: true })]))).toEqual({ main: 'one', named: [SHELL_ID], rest: 0 })
    expect(welcomeOffer([shell, agent({ id: 'gone', available: false })])).toEqual({ state: 'none' })
    expect(welcomeOffer([])).toEqual({ state: 'none' })
  })

  it('holds the answer back while an agent ahead of the first found is still looked up, and not once it was read and gave up', () => {
    const list = [agent({ id: 'slow', available: true, checking: true }), agent({ id: 'one', available: true })]
    expect(welcomeOffer(list)).toEqual({ state: 'looking' })
    // Read once and still looked up: not found for now, and not on the Or line either.
    expect(ids(welcomeOffer(list, { gaveUp: new Set(['slow']) }))).toEqual({ main: 'one', named: [], rest: 0 })
    // One looked up behind the first found holds nothing back, and joins the Or line only once it is found.
    expect(ids(welcomeOffer([agent({ id: 'one', available: true }), agent({ id: 'slow', available: true, checking: true })]))).toEqual({ main: 'one', named: [], rest: 0 })
    // Its version alone still asked: its program was found, so it is known.
    const asked = agent({ id: 'asked', available: true, checking: true, identity: { ...found('1.0'), pending: true } })
    expect(stillLookedUp(asked)).toBe(false)
    expect(welcomeOffer([asked])).toEqual({ state: 'found', main: { id: 'asked', name: 'Agent', version: undefined }, named: [], rest: 0 })
  })

  it('keeps the agent the button already names while the server can launch it, and lets it go once an answer says it is gone', () => {
    const list = [agent({ id: 'one', available: true }), agent({ id: 'two', available: true })]
    expect(ids(welcomeOffer(list, { keep: 'two' }))).toEqual({ main: 'two', named: ['one'], rest: 0 })
    expect(ids(welcomeOffer([agent({ id: 'slow', checking: true }), ...list], { keep: 'two' }))).toEqual({ main: 'two', named: ['one'], rest: 0 })
    expect(ids(welcomeOffer([list[0]!, agent({ id: 'two', available: false })], { keep: 'two' }))).toEqual({ main: 'one', named: [], rest: 0 })
    expect(ids(welcomeOffer([agent({ id: SHELL_ID, available: true }), ...list], { keep: SHELL_ID }))).toEqual({ main: 'one', named: [SHELL_ID, 'two'], rest: 0 })
  })

  it('names three others or fewer on the Or line, else the first two and the count of the rest', () => {
    const many = (n: number) => Array.from({ length: n }, (_, i) => agent({ id: `a${i}`, available: true }))
    expect(ids(welcomeOffer(many(4)))).toEqual({ main: 'a0', named: ['a1', 'a2', 'a3'], rest: 0 })
    expect(ids(welcomeOffer(many(5)))).toEqual({ main: 'a0', named: ['a1', 'a2'], rest: 2 })
    expect(ids(welcomeOffer(many(1)))).toEqual({ main: 'a0', named: [], rest: 0 })
  })

  it('gives no version for a program its check did not identify', () => {
    const odd = { ran: true, identified: false, verified: false, output: 'v9', version: '9' }
    const o = welcomeOffer([agent({ id: 'x', available: true, identity: odd })])
    expect(o.state === 'found' && o.main.version).toBeUndefined()
  })
})
