import { describe, expect, it } from 'vitest'
import type { AgentInfo } from '~/composables/useSessions'
import { AGENT_ID_PATTERN, MASK, agentPayload, browserOut, cloneId, commandCheck, commandOf, formErrors, formForClone, formFromAgent, signalOut, siteError, takesProfile, yoloOut } from './agentForm'
import { slugId } from './argv'

function counter() {
  let n = 0
  return () => n++
}

const keyed: AgentInfo = {
  id: 'keyed',
  name: 'Keyed',
  command: ['aider', '--model', 'x'],
  allowArgs: true,
  env: { REGION: MASK, API_KEY: MASK },
  envPassthrough: ['HTTP_PROXY'],
  cwd: '/srv',
  icon: 'i-lucide-sparkles',
  adapter: 'claude',
  signal: { kind: 'hook', toolEvents: true },
}

describe('formFromAgent and agentPayload', () => {
  it('round-trips an agent: stored values stay masked, passthrough names stay names', () => {
    const f = formFromAgent(keyed, counter())
    expect(f.env.map((r) => [r.key, r.masked])).toEqual([
      ['API_KEY', true],
      ['REGION', true],
      ['HTTP_PROXY', false],
    ])
    expect(agentPayload(f, keyed)).toEqual({
      id: 'keyed',
      name: 'Keyed',
      description: undefined,
      command: ['aider', '--model', 'x'],
      allowArgs: true,
      env: { API_KEY: MASK, REGION: MASK },
      envPassthrough: ['HTTP_PROXY'],
      cwd: '/srv',
      icon: 'i-lucide-sparkles',
      adapter: 'claude',
      signal: { kind: 'hook', toolEvents: true },
    })
  })
  it('sends a value typed in a new row as it is, next to the masked ones', () => {
    const f = formFromAgent(keyed, counter())
    f.env.push({ uid: 99, key: 'TOKEN', value: 's3cret', masked: false })
    expect(agentPayload(f, keyed).env).toEqual({ API_KEY: MASK, REGION: MASK, TOKEN: 's3cret' })
  })
  it('leaves a removed stored value out of the payload', () => {
    const f = formFromAgent(keyed, counter())
    f.env = f.env.filter((r) => r.key !== 'API_KEY')
    expect(agentPayload(f, keyed)).toMatchObject({ env: { REGION: MASK }, envPassthrough: ['HTTP_PROXY'] })
    expect(agentPayload(f, keyed).env).not.toHaveProperty('API_KEY')
  })
  it('starts a new agent empty, ringing the bell', () => {
    const f = formFromAgent(undefined, counter())
    expect(f).toMatchObject({ name: '', id: '', command: [], pendingCommand: '', env: [], allowArgs: true, signal: 'bell' })
    expect(agentPayload({ ...f, name: 'X', id: 'x', command: ['x'] }).signal).toBeUndefined()
  })
  it('counts what is typed in the command field and not yet an argument', () => {
    const f = { ...formFromAgent(undefined, counter()), command: ['aider'], pendingCommand: '--model "gpt 5"' }
    expect(commandOf(f)).toEqual(['aider', '--model', 'gpt 5'])
    expect(agentPayload({ ...f, name: 'A', id: 'a' }).command).toEqual(['aider', '--model', 'gpt 5'])
  })
})

describe('formForClone and cloneId', () => {
  it('fills the form from the agent copied, under a new name and a free id, the stored values as masked rows', () => {
    const f = formForClone(keyed, ['keyed', 'keyed-copy'], counter())
    expect(f.name).toBe('Keyed (copy)')
    expect(f.id).toBe('keyed-copy-2')
    expect(f.env.map((r) => [r.key, r.masked])).toEqual([
      ['API_KEY', true],
      ['REGION', true],
      ['HTTP_PROXY', false],
    ])
    expect(f.command).toEqual(['aider', '--model', 'x'])
    // The clone route's body: the masked values mean the source's, and the fields the form has no control for are the source's.
    expect(agentPayload(f, keyed)).toMatchObject({ id: 'keyed-copy-2', name: 'Keyed (copy)', env: { API_KEY: MASK, REGION: MASK }, envPassthrough: ['HTTP_PROXY'], adapter: 'claude', cwd: '/srv', icon: 'i-lucide-sparkles' })
  })
  it("suggests an id of the server's shape whatever the length of the source's", () => {
    expect(cloneId('claude', [])).toBe('claude-copy')
    expect(cloneId('claude', ['claude-copy', 'claude-copy-2'])).toBe('claude-copy-3')
    const id = cloneId('a'.repeat(32), [])
    expect(id).toBe('a'.repeat(27) + '-copy')
    expect(AGENT_ID_PATTERN.test(id)).toBe(true)
    expect(cloneId('abc' + '-'.repeat(29), [])).toBe('abc-copy')
  })
})

describe('formErrors', () => {
  const ok = () => ({ ...formFromAgent(undefined, counter()), name: 'Aider', id: 'aider', command: ['aider'] })
  it('accepts a complete form', () => {
    expect(formErrors(ok())).toEqual({})
  })
  it('catches *** typed as a new value: the server would read it as the stored one', () => {
    const f = ok()
    f.env.push({ uid: 1, key: 'API_KEY', value: MASK, masked: false })
    expect(formErrors(f).env).toBe('API_KEY: *** stands for a stored value; type the real value')
  })
  it('refuses an unclosed quote in the command', () => {
    expect(formErrors({ ...ok(), pendingCommand: '--model "gpt' }).command).toBe('Close the quote, or remove it')
  })
  it("wants a command, a name and an ID of the server's shape", () => {
    expect(formErrors({ ...ok(), command: [] }).command).toBe('Add the command to run')
    expect(formErrors({ ...ok(), name: ' ' }).name).toBe('Give the agent a name')
    expect(formErrors({ ...ok(), id: 'Bad ID' }).id).toBe('Use lowercase letters, digits and dashes, up to 32')
    expect(formErrors({ ...ok(), id: 'a'.repeat(33) }).id).toBeDefined()
    expect(formErrors({ ...ok(), id: '' }).id).toBe('An ID is required')
  })
  it('names a variable listed twice, and a value without a name', () => {
    const f = ok()
    f.env.push({ uid: 1, key: 'A', value: 'x', masked: false }, { uid: 2, key: 'A', value: 'y', masked: false })
    expect(formErrors(f).env).toBe('A is listed twice')
    expect(formErrors({ ...ok(), env: [{ uid: 3, key: ' ', value: 'v', masked: false }] }).env).toBe('Give every variable a name')
  })
})

describe('signalOut', () => {
  it('leaves the bell out unless the agent had a signal', () => {
    expect(signalOut('bell', '')).toBeUndefined()
    expect(signalOut('bell', '', { kind: 'pattern', pattern: 'x' })).toEqual({ kind: 'bell' })
  })
  it('keeps the tool-events flag the form has no control for', () => {
    expect(signalOut('pattern', '^> $', { kind: 'hook', toolEvents: true })).toEqual({ kind: 'pattern', pattern: '^> $', toolEvents: true })
    expect(signalOut('none', '', { kind: 'hook', toolEvents: true })).toEqual({ kind: 'none', toolEvents: true })
  })
  it("keeps Codex's working title, which the form has no control for, whatever the kind saved", () => {
    const codex = { kind: 'hook' as const, workingTitle: '^(?:\\S+\\s+)?(?:Working|Thinking|Waiting)$' }
    for (const kind of ['hook', 'bell', 'none'] as const) {
      expect(signalOut(kind, '', codex)).toStrictEqual({ kind, toolEvents: undefined, workingTitle: codex.workingTitle })
    }
    expect(signalOut('pattern', '^> $', codex)).toStrictEqual({ kind: 'pattern', pattern: '^> $', toolEvents: undefined, workingTitle: codex.workingTitle })
    // The Codex agent edited on the Agents page (an env var added) saves its working title back.
    const agent: AgentInfo = { id: 'codex', name: 'Codex CLI', command: ['codex'], allowArgs: true, adapter: 'codex', signal: codex }
    expect(agentPayload(formFromAgent(agent, counter()), agent).signal).toStrictEqual({ kind: 'hook', toolEvents: undefined, workingTitle: codex.workingTitle })
    expect(signalOut('hook', '', { kind: 'hook' })).toStrictEqual({ kind: 'hook', toolEvents: undefined, workingTitle: undefined })
  })
})

describe('AGENT_ID_PATTERN and slugId', () => {
  it('a suggested ID is empty or one the server takes, never ending in a dash', () => {
    for (const name of ['Claude (opus)', 'a'.repeat(31) + ' b', '--x--', 'My  Tool -- v2', '!!!', 'x'.repeat(40)]) {
      const id = slugId(name)
      expect(id === '' || AGENT_ID_PATTERN.test(id)).toBe(true)
      expect(id.endsWith('-')).toBe(false)
    }
  })
})

describe('the site field', () => {
  it('round-trips the site, and sends none when it is empty', () => {
    const f = formFromAgent({ ...keyed, site: 'https://example.com/keyed' }, counter())
    expect(f.site).toBe('https://example.com/keyed')
    expect(agentPayload(f, keyed).site).toBe('https://example.com/keyed')
    expect(agentPayload({ ...f, site: '  ' }, keyed).site).toBeUndefined()
  })
  it('accepts an empty or https site and refuses the rest, as the server does', () => {
    expect(siteError('')).toBe('')
    expect(siteError('https://example.com/x')).toBe('')
    expect(siteError('https://example.com:8443/x')).toBe('')
    for (const bad of ['http://example.com', 'example.com', 'https://user:pw@example.com', 'javascript:alert(1)', `https://example.com/${'a'.repeat(200)}`, 'https://:443', 'https://example.com:99999', 'https://example.com:0', 'https://exa\u00a0mple.com']) {
      expect(siteError(bad)).toBe('An https:// address, or nothing')
    }
    const f = formFromAgent(keyed, counter())
    expect(formErrors({ ...f, site: 'http://example.com' }).site).toBe('An https:// address, or nothing')
  })
})

describe('commandCheck', () => {
  it('says where a found program is, falling back to the program sent', () => {
    expect(commandCheck({ found: true, path: '/usr/bin/aider' }, 'aider')).toEqual({ state: 'found', path: '/usr/bin/aider' })
    expect(commandCheck({ found: true }, 'aider')).toEqual({ state: 'found', path: 'aider' })
  })
  it('says not found only when the server judged it', () => {
    expect(commandCheck({ found: false }, 'ghost')).toEqual({ state: 'missing' })
  })
  it('leaves a relative program to the launch, as the catalog counts it available', () => {
    expect(commandCheck({ found: false, unknown: 'relative' }, './agent.sh')).toEqual({ state: 'atLaunch' })
  })
  it('says a lookup that took too long was not judged', () => {
    expect(commandCheck({ found: false, unknown: 'timeout' }, 'aider')).toEqual({ state: 'slow' })
  })
})

describe('the yolo recipe', () => {
  const copilot: AgentInfo = {
    id: 'copilot',
    name: 'Copilot CLI',
    command: ['copilot'],
    allowArgs: true,
    yolo: { args: ['--yolo'], env: { COPILOT_ALLOW_ALL: 'true' } },
    trustPrompt: 'Do you trust',
    trustAnswers: [{ label: 'Yes', input: '\r' }],
    questions: [{ prompt: 'Hooks\\s*need\\s*review', answers: [{ label: 'Trust all and continue', input: '2\r' }] }],
    session: { startArgs: ['--session-id', '{id}'], resumeArgs: ['--session-id', '{id}'], idPattern: '^x$' },
    probe: false,
  }

  it('round-trips a recipe, and carries the trust prompt, the session recipe and the probe switch the form has no control for', () => {
    const f = formFromAgent(copilot, counter())
    expect(f.yoloArgs).toEqual(['--yolo'])
    expect(f.yoloEnv.map((r) => [r.key, r.value])).toEqual([['COPILOT_ALLOW_ALL', 'true']])
    // probe: false stays with the agent across a save (a copy with its probe off would be probed again otherwise; #291).
    expect(agentPayload(f, copilot)).toMatchObject({ yolo: copilot.yolo, trustPrompt: 'Do you trust', trustAnswers: copilot.trustAnswers, questions: copilot.questions, session: copilot.session, probe: false })
    expect(agentPayload(f, { ...copilot, probe: undefined })).not.toHaveProperty('probe', false)
  })

  it('saves {} for no recipe, and nothing for empty fields, which keeps a built-in recipe', () => {
    const f = formFromAgent(copilot, counter())
    expect(yoloOut({ ...f, yoloNone: true })).toEqual({})
    expect(yoloOut({ ...f, yoloArgs: [], yoloEnv: [] })).toBeUndefined()
    expect(formFromAgent({ ...copilot, yolo: {} }, counter()).yoloNone).toBe(true)
  })

  it('counts what is typed and not yet an argument', () => {
    const f = { ...formFromAgent(undefined, counter()), yoloPending: '--yes "a b"' }
    expect(yoloOut(f)).toEqual({ args: ['--yes', 'a b'] })
  })

  it("refuses what the server refuses: Conductor's variables, bad names, an open quote", () => {
    const ok = () => ({ ...formFromAgent(undefined, counter()), name: 'X', id: 'x', command: ['x'] })
    expect(formErrors({ ...ok(), yoloEnv: [{ uid: 1, key: 'CONDUCTOR_TOKEN', value: 'x' }] }).yolo).toBeTruthy()
    expect(formErrors({ ...ok(), yoloEnv: [{ uid: 1, key: 'BAD NAME', value: 'x' }] }).yolo).toBeTruthy()
    expect(formErrors({ ...ok(), yoloPending: '"open' }).yolo).toBeTruthy()
    expect(formErrors({ ...ok(), yoloEnv: [{ uid: 1, key: 'GOOSE_MODE', value: 'auto' }] })).toEqual({})
  })
})

describe('the browser its links open in (#436)', () => {
  const work: AgentInfo = { id: 'work', name: 'Work', command: ['claude'], allowArgs: true, browser: { name: 'edge', profile: 'Profile 2' } }
  it('keeps the browser and the profile through load and save, and a clone', () => {
    const f = formFromAgent(work, counter())
    expect([f.browser, f.profile]).toEqual(['edge', 'Profile 2'])
    expect(agentPayload(f, work).browser).toEqual({ name: 'edge', profile: 'Profile 2' })
    const c = formForClone(work, ['work'], counter())
    expect(agentPayload(c, work).browser).toEqual({ name: 'edge', profile: 'Profile 2' })
  })
  it('saves the default browser as {} over one the agent named, and nothing for an agent that named none', () => {
    const f = formFromAgent(work, counter())
    f.browser = 'default'
    expect(agentPayload(f, work).browser).toEqual({})
    const plain = formFromAgent(keyed, counter())
    expect(plain.browser).toBe('default')
    expect(agentPayload(plain, keyed).browser).toBeUndefined()
    expect(agentPayload(formFromAgent(undefined, counter())).browser).toBeUndefined()
  })
  it('keeps the default saved over a configured browser through a second load and save', () => {
    // The server answers the first save's {} as browser: {}; a later edit of that agent saves {} again, or inheritance gives the configured browser back.
    const saved: AgentInfo = { ...work, browser: {} }
    const f = formFromAgent(saved, counter())
    expect(f.browser).toBe('default')
    f.name = 'Work, renamed'
    expect(agentPayload(f, saved).browser).toEqual({})
    expect(agentPayload(formForClone(saved, ['work'], counter()), saved).browser).toEqual({})
  })
  it('sends a profile only for Chrome, Edge and Brave, trimmed', () => {
    expect(browserOut({ browser: 'firefox', profile: 'Profile 2' })).toEqual({ name: 'firefox' })
    expect(browserOut({ browser: 'brave', profile: '  Default ' })).toEqual({ name: 'brave', profile: 'Default' })
    expect(browserOut({ browser: 'chrome', profile: '' })).toEqual({ name: 'chrome' })
    expect(takesProfile({ browser: 'safari' })).toBe(false)
    expect(takesProfile({ browser: 'edge' })).toBe(true)
  })
  it("refuses a profile the server refuses: too long, a quote or a control character", () => {
    const f = formFromAgent(work, counter())
    expect(formErrors(f).profile).toBeUndefined()
    f.profile = 'a"b'
    expect(formErrors(f).profile).toBeTruthy()
    f.profile = 'p'.repeat(101)
    expect(formErrors(f).profile).toBeTruthy()
    f.browser = 'firefox'
    expect(formErrors(f).profile).toBeUndefined()
  })
  it('reads a browser the page does not know as the default', () => {
    expect(formFromAgent({ ...work, browser: { name: 'opera' as never } }, counter()).browser).toBe('default')
  })
})
