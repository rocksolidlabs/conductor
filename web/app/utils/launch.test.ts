import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { chipStep, cutAtWord, firstMessageLine, isLoopbackHost, launchKey, launchRefusal, messageProblem, namePreview, noTaskLine, optionsSummary, plainOffMachine, showsRunsOn, takesFirstMessage } from './launch'

describe('showsRunsOn', () => {
  it('never asks in the desktop app', () => {
    expect(showsRunsOn(true, 'conductor.example.net')).toBe(false)
    expect(showsRunsOn(true, 'localhost')).toBe(false)
  })
  it('never asks on a workbench opened at this computer', () => {
    for (const h of ['localhost', 'LOCALHOST', 'app.localhost', '127.0.0.1', '127.1.2.3', '::1', '[::1]', '']) {
      expect(showsRunsOn(false, h), h).toBe(false)
    }
  })
  it('asks when the server is another machine, a LAN one too', () => {
    for (const h of ['conductor.example.net', '192.168.1.57', 'switchyard.rslabs.net', 'box.local']) {
      expect(showsRunsOn(false, h), h).toBe(true)
    }
  })
  it('isLoopbackHost', () => {
    expect(isLoopbackHost('127.0.0.1')).toBe(true)
    expect(isLoopbackHost('10.0.0.1')).toBe(false)
  })
})

// The host's rule for a server it refuses without --insecure: the table of
// TestPlainOffMachine (internal/config/config_test.go), kept in step with it,
// then the spellings the URL parser would have normalized away.
describe('plainOffMachine', () => {
  it('matches the host (config.PlainOffMachine) case for case', () => {
    const table: Record<string, boolean> = {
      'http://yard.example': true,
      'ws://yard.example/ws/host': true,
      'HTTP://yard.example': true,
      'http://192.168.1.10:8080': true,
      'http://0.0.0.0:8080': true,
      'http://127.1:8080': true,
      'http://localhost.yard.example': true,
      'http://127.0.0.1.yard.example': true,
      'http://[fe80::1]:8080': true,
      'https://yard.example': false,
      'wss://yard.example': false,
      'http://localhost:8080': false,
      'ws://LocalHost:8080': true,
      'http://127.0.0.1:8080': false,
      'http://127.255.0.1': false,
      'http://[::1]:8080': false,
      'http://[::ffff:127.0.0.1]:8080': false,
      'http://user:pw@localhost:8080': false,
      'ftp://yard.example': false,
      '::not a url': false,
    }
    for (const [server, want] of Object.entries(table)) expect(plainOffMachine(server), server).toBe(want)
  })
  it('reads the host as the host does, not as the URL parser rewrites it', () => {
    // The parser would write these as localhost and 127.0.0.1, which the host, reading them as given, refuses without the flag.
    for (const s of ['http://LOCALHOST:8080', 'http://localhost.:8080', 'http://127.0.0.01:8080', 'http://127.000.0.1', 'http://app.localhost:8099', 'http://conductor.test:8099']) {
      expect(plainOffMachine(s), s).toBe(true)
    }
    for (const s of ['http://[0:0:0:0:0:0:0:1]:8080', 'ws://[::1]:8099', 'http://[::1%25lo]:8080', 'http://127.0.0.1', '']) {
      expect(plainOffMachine(s), s).toBe(false)
    }
  })
})

// The server's one table of cuts at a word (internal/session/testdata/cut-at-word.json): the app's copy of the rule gives every `want`.
const cuts = JSON.parse(readFileSync(fileURLToPath(new URL('../../../internal/session/testdata/cut-at-word.json', import.meta.url)), 'utf8')) as {
  cases: Array<{ name: string; s: string; keep: number; chars: number; bytes: number; want: string }>
}

describe('a launch with a first message (P19)', () => {
  it("cuts at a word as the server does, case by case of the server's table", () => {
    expect(cuts.cases.length).toBeGreaterThan(0)
    for (const c of cuts.cases) expect(cutAtWord(c.s, c.keep, c.chars, c.bytes), c.name).toBe(c.want)
  })
  it('names the session by the first words, as the server does', () => {
    expect(namePreview('fix the flaky vim spec on the mac runner please')).toBe('fix the flaky vim spec on the mac…')
    expect(namePreview('fix it\nnow')).toBe('fix it now')
    expect(namePreview('  say\thello  ')).toBe('say hello')
    expect(namePreview('   ')).toBe('')
  })
  it('refuses a message past 4,096 bytes as sent', () => {
    expect(messageProblem('a'.repeat(4096))).toBe('')
    expect(messageProblem('a'.repeat(4097))).toBe('At most 4,096 bytes: this is 4,097.')
    expect(messageProblem('é'.repeat(2049))).toBe('At most 4,096 bytes: this is 4,098.')
  })
  it('says above the terminal what the message waits for, and nothing once it no longer waits', () => {
    expect(firstMessageLine({ firstMessage: 'waiting', attention: { state: 'needs_input', source: 'trust' } }, 'Codex')).toBe('Your first message is typed once its question is answered.')
    expect(firstMessageLine({ firstMessage: 'waiting', attention: { state: '' } }, 'Codex')).toBe('Your first message is typed once Codex is ready.')
    expect(firstMessageLine({ attention: { state: 'needs_input', source: 'trust' } }, 'Codex')).toBe('')
    expect(firstMessageLine(null, 'Codex')).toBe('')
  })
  it('says the message is typed and not sent, and how to send it; a word it does not know shows nothing', () => {
    expect(firstMessageLine({ firstMessage: 'typed', attention: { state: 'working', source: 'api' } }, 'Codex')).toBe(
      "Your first message is typed in the agent's input and not sent: press Enter in the terminal to send it.",
    )
    expect(firstMessageLine({ firstMessage: 'typed', attention: { state: 'needs_input', source: 'trust' } }, 'Codex')).toBe(
      'Your first message is typed: answer the question, then press Enter in the terminal to send it.',
    )
    expect(firstMessageLine({ firstMessage: 'later' }, 'Codex')).toBe('')
  })
})

describe('which agents take a first message', () => {
  it('is the catalog\'s word alone: an older server, which says nothing, offers none', () => {
    expect(takesFirstMessage({ firstMessage: true })).toBe(true)
    expect(takesFirstMessage({})).toBe(false)
    expect(takesFirstMessage({ firstMessage: false })).toBe(false)
    expect(takesFirstMessage(undefined)).toBe(false)
  })
  it('says, in place of the task, that an agent which takes none starts at its prompt, and where its task goes', () => {
    expect(noTaskLine({ name: 'Plain', allowArgs: true })).toBe('Plain starts at its prompt. Type your task in its terminal.')
    expect(noTaskLine({ name: 'Shell' })).toBe('Shell starts at its prompt.')
    // The old line, which explained why, is gone.
    expect(noTaskLine({ name: 'aider', allowArgs: true })).not.toContain('Conductor types a first message')
  })
})

describe("the Agent row's keys", () => {
  const ids = ['claude', 'codex', 'shell']
  it('moves to the next and the previous chip, round at the ends, and to the first and the last', () => {
    expect(chipStep(ids, 'claude', 'ArrowRight')).toBe('codex')
    expect(chipStep(ids, 'claude', 'ArrowDown')).toBe('codex')
    expect(chipStep(ids, 'shell', 'ArrowRight')).toBe('claude')
    expect(chipStep(ids, 'claude', 'ArrowLeft')).toBe('shell')
    expect(chipStep(ids, 'codex', 'ArrowUp')).toBe('claude')
    expect(chipStep(ids, 'codex', 'Home')).toBe('claude')
    expect(chipStep(ids, 'codex', 'End')).toBe('shell')
  })
  it('starts from the first when nothing offered is picked, and moves nothing for another key or no chips', () => {
    expect(chipStep(ids, '', 'ArrowRight')).toBe('codex')
    expect(chipStep(ids, 'claude', 'Tab')).toBeNull()
    expect(chipStep(ids, 'claude', 'a')).toBeNull()
    expect(chipStep([], '', 'ArrowRight')).toBeNull()
  })
})

describe('a launch the server refused, in the dialog', () => {
  it("says a folder's refusal under Folder in plain words", () => {
    expect(launchRefusal('invalid_cwd', 'working directory is outside the allowed roots')).toEqual({ at: 'folder', words: "Agents can't use this folder yet.", outside: true })
    expect(launchRefusal('invalid_cwd', 'working directory does not exist')).toEqual({ at: 'folder', words: 'There is no folder at this path.' })
    expect(launchRefusal('invalid_cwd', 'working directory is not a directory')).toEqual({ at: 'folder', words: 'This is a file, not a folder.' })
    expect(launchRefusal('invalid_cwd', 'invalid working directory')).toEqual({ at: 'folder', words: 'Invalid working directory.' })
  })
  it("says any other refusal on top as Not started, in the server's words, with no code in them", () => {
    expect(launchRefusal('invalid_request', 'the arguments choose a model already: set the model in one place')).toEqual({ at: 'top', words: 'Not started: the arguments choose a model already: set the model in one place.' })
    expect(launchRefusal('spawn_failed', 'could not start the agent process.')).toEqual({ at: 'top', words: 'Not started: could not start the agent process.' })
    expect(launchRefusal('', 'Failed to fetch')).toEqual({ at: 'top', words: 'Not started: Failed to fetch.' })
    expect(launchRefusal('x', '').words).toBe('Not started.')
    expect(launchRefusal('invalid_request', 'bad').words).not.toContain('invalid_request')
  })
})

describe("the closed line of Launch agent's More options", () => {
  it('names what is set inside, in order, and nothing when all is as it comes', () => {
    expect(optionsSummary({})).toBe('')
    expect(optionsSummary({ name: '  ', model: '', args: ' ' })).toBe('')
    expect(optionsSummary({ model: 'opus' })).toBe('opus')
    expect(optionsSummary({ name: 'fix the build', model: ' opus ', args: '--add-dir ../shared' })).toBe('"fix the build" · opus · --add-dir ../shared')
    expect(optionsSummary({ args: '--model opus "two words"' })).toBe('--model opus "two words"')
  })
})

describe('the keys that launch from the Launch dialog', () => {
  const key = (over: Partial<Parameters<typeof launchKey>[0]> = {}) => ({ key: 'Enter', shiftKey: false, ctrlKey: false, metaKey: false, altKey: false, isComposing: false, repeat: false, ...over })
  it('Enter in the task box, and Ctrl or Cmd+Enter anywhere', () => {
    expect(launchKey(key(), true)).toBe(true)
    expect(launchKey(key(), false)).toBe(false)
    expect(launchKey(key({ ctrlKey: true }), false)).toBe(true)
    expect(launchKey(key({ metaKey: true }), false)).toBe(true)
    expect(launchKey(key({ ctrlKey: true }), true)).toBe(true)
  })
  it('never Shift+Enter in the task box, a composing Enter, a held key, Alt, or another key', () => {
    expect(launchKey(key({ shiftKey: true }), true)).toBe(false)
    expect(launchKey(key({ isComposing: true }), true)).toBe(false)
    expect(launchKey(key({ keyCode: 229 }), true)).toBe(false)
    expect(launchKey(key({ repeat: true }), true)).toBe(false)
    expect(launchKey(key({ repeat: true, ctrlKey: true }), false)).toBe(false)
    expect(launchKey(key({ altKey: true }), true)).toBe(false)
    expect(launchKey(key({ key: 'a', ctrlKey: true }), true)).toBe(false)
  })
})
