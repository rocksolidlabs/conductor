import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { cutAtWord, firstMessageLine, isLoopbackHost, messageProblem, namePreview, noFirstMessageLine, plainOffMachine, showsRunsOn, takesFirstMessage } from './launch'

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
})

describe('which agents take a first message', () => {
  it('is the catalog\'s word alone: an older server, which says nothing, offers none', () => {
    expect(takesFirstMessage({ firstMessage: true })).toBe(true)
    expect(takesFirstMessage({})).toBe(false)
    expect(takesFirstMessage({ firstMessage: false })).toBe(false)
    expect(takesFirstMessage(undefined)).toBe(false)
  })
  it('says why another does not, naming those offered that do', () => {
    const claude = { name: 'Claude Code', firstMessage: true }
    const codex = { name: 'Codex', firstMessage: true }
    const aider = { name: 'aider' }
    expect(noFirstMessageLine([claude, codex, aider])).toBe("Conductor types a first message only where it knows the agent's prompt: Claude Code and Codex here.")
    expect(noFirstMessageLine([claude, aider])).toBe("Conductor types a first message only where it knows the agent's prompt: Claude Code here.")
    expect(noFirstMessageLine([{ name: 'A', firstMessage: true }, { name: 'B', firstMessage: true }, codex])).toBe("Conductor types a first message only where it knows the agent's prompt: A, B and Codex here.")
    expect(noFirstMessageLine([aider])).toBe("Conductor types a first message only where it knows the agent's prompt, and knows none of these agents' prompts.")
  })
})
