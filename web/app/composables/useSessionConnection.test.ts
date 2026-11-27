import { describe, expect, it } from 'vitest'
import { ref } from 'vue'
import { useSessionConnection } from './useSessionConnection'
import type { ControlMessage, TranscriptMessage, Welcome } from '~/utils/protocol'
import type { CloseInfo, TerminalTransport } from '~/utils/transport/types'

/** A transport in memory: the test hands it the owner's frames; it records what the page sent. */
function fakeTransport() {
  let output: (data: Uint8Array, replay: boolean) => void = () => {}
  let control: (msg: ControlMessage) => void = () => {}
  let transcript: (msg: TranscriptMessage) => void = () => {}
  let close: (info: CloseInfo) => void = () => {}
  const sent: string[] = []
  const t = {
    kind: ref('ws'),
    state: ref('open'),
    rtt: ref(null),
    decisions: false,
    connect: async () => ({ t: 'welcome' }) as Welcome,
    sendInput: (d: Uint8Array) => void sent.push(`input ${new TextDecoder().decode(d)}`),
    submit: (text: string) => (sent.push(`submit ${text}`), true),
    ping: () => {},
    resize: () => {},
    close: () => close({ code: 1000, reason: '' }),
    onOutput: (cb: typeof output) => void (output = cb),
    onControl: (cb: typeof control) => void (control = cb),
    onTranscript: (cb: typeof transcript) => void (transcript = cb),
    onClose: (cb: typeof close) => void (close = cb),
    transcriptGet: async () => ({ t: 'page', entries: [], first: 0, last: 0 }),
    interrupt: async () => (sent.push('interrupt'), true),
    transcriptFrom: () => {},
  }
  return {
    transport: t as unknown as TerminalTransport,
    sent,
    owner: {
      output: (s: string) => output(new TextEncoder().encode(s), false),
      control: (m: ControlMessage) => control(m),
      transcript: (m: TranscriptMessage) => transcript(m),
    },
  }
}

describe('useSessionConnection', () => {
  it('oneConnectionForBothViews: a move between ways keeps the one connection, the terminal and the chat its consumers', async () => {
    const made: ReturnType<typeof fakeTransport>[] = []
    const conn = useSessionConnection(() => {
      const f = fakeTransport()
      made.push(f)
      return f.transport
    })
    const terminal: string[] = []
    const chat: TranscriptMessage[] = []
    conn.onOutput((d) => terminal.push(new TextDecoder().decode(d)))
    conn.onTranscript((m) => chat.push(m))
    await conn.connect({ cols: 80, rows: 24 })
    const f = made[0]!
    f.owner.control({ t: 'welcome', role: 'control', fileView: true } as unknown as Welcome)
    expect(conn.way.value).toBe('terminal')
    f.owner.output('$ hello')
    expect(conn.sendInput(new TextEncoder().encode('x'))).toBe(true)
    // Pressed during the turn: noted on every viewer.
    f.owner.control({ t: 'way', way: 'terminal', noted: 'protocol', why: 'Moves to Chat when this turn ends.' })
    expect([conn.way.value, conn.noted.value, conn.why.value]).toEqual(['terminal', 'protocol', 'Moves to Chat when this turn ends.'])
    // Made at the turn's end: the chat's frames on the same connection.
    f.owner.control({ t: 'way', way: 'protocol', protocol: 'claude-stream-json' })
    f.owner.transcript({ t: 'entry', entry: { seq: 1 } } as unknown as TranscriptMessage)
    expect([conn.way.value, conn.noted.value]).toEqual(['protocol', ''])
    await conn.interrupt()
    // And back.
    f.owner.control({ t: 'way', way: 'terminal' })
    f.owner.output('$ resumed')
    expect(made).toHaveLength(1)
    expect(terminal).toEqual(['$ hello', '$ resumed'])
    expect(chat).toHaveLength(1)
    expect(f.sent).toEqual(['input x', 'interrupt'])
    expect(conn.transport.value).toBe(f.transport)
  })

  it('takes no way message past its bounds, and a reconnect reads the welcome noted', async () => {
    const made: ReturnType<typeof fakeTransport>[] = []
    const conn = useSessionConnection(() => {
      const f = fakeTransport()
      made.push(f)
      return f.transport
    })
    await conn.connect({ cols: 0, rows: 0 })
    made[0]!.owner.control({ t: 'way', way: 'protocol', why: 'w'.repeat(201) })
    expect(conn.way.value).toBe('terminal')
    made[0]!.owner.control({ t: 'way', way: 'chat' } as unknown as ControlMessage)
    expect(conn.way.value).toBe('terminal')
    await conn.connect({ cols: 0, rows: 0 })
    expect(made).toHaveLength(2)
    // The first connection's frames no longer reach the views.
    made[0]!.owner.control({ t: 'way', way: 'protocol' })
    expect(conn.way.value).toBe('terminal')
    made[1]!.owner.control({ t: 'welcome', role: 'view', fileView: false, way: 'protocol', noted: 'terminal' } as unknown as Welcome)
    expect([conn.way.value, conn.noted.value, conn.role.value]).toEqual(['protocol', 'terminal', 'view'])
  })
})

describe('useSessionConnection, the server\'s word on expand (UX3 R10)', () => {
  it('expandIsTheServersWordAlone: the welcome says it, a way message without it withdraws it, a new connection starts without it', async () => {
    const made: ReturnType<typeof fakeTransport>[] = []
    const conn = useSessionConnection(() => {
      const f = fakeTransport()
      made.push(f)
      return f.transport
    })
    await conn.connect({ cols: 80, rows: 24 })
    expect(conn.expand.value).toBe(false)
    made[0]!.owner.control({ t: 'welcome', role: 'control', fileView: true, way: 'protocol', expand: true } as unknown as Welcome)
    expect(conn.expand.value).toBe(true)
    // A move away and back: each way message says it again or withdraws it.
    made[0]!.owner.control({ t: 'way', way: 'terminal' } as unknown as ControlMessage)
    expect(conn.expand.value).toBe(false)
    made[0]!.owner.control({ t: 'way', way: 'protocol', expand: true } as unknown as ControlMessage)
    expect(conn.expand.value).toBe(true)
    made[0]!.owner.control({ t: 'way', way: 'protocol' } as unknown as ControlMessage)
    expect(conn.expand.value).toBe(false)
    made[0]!.owner.control({ t: 'way', way: 'protocol', expand: true } as unknown as ControlMessage)
    // A connection made again remembers no yes: false until its own welcome says so.
    await conn.connect({ cols: 80, rows: 24 })
    expect(conn.expand.value).toBe(false)
    made[1]!.owner.control({ t: 'welcome', role: 'control', fileView: true, way: 'protocol' } as unknown as Welcome)
    expect(conn.expand.value).toBe(false)
    // A frame of the connection before is not this one's word.
    made[0]!.owner.control({ t: 'welcome', role: 'control', fileView: true, way: 'protocol', expand: true } as unknown as Welcome)
    expect(conn.expand.value).toBe(false)
  })
})

describe("useSessionConnection, the agent's commands (UX3 R11)", () => {
  it('keepsTheServersListAlone: a higher rev replaces it, an empty one means none, a welcome and a move to the terminal clear it', async () => {
    const made: ReturnType<typeof fakeTransport>[] = []
    const conn = useSessionConnection(() => {
      const f = fakeTransport()
      made.push(f)
      return f.transport
    })
    await conn.connect({ cols: 80, rows: 24 })
    const f = made[0]!
    f.owner.control({ t: 'welcome', role: 'control', fileView: true, way: 'protocol', expand: true } as unknown as Welcome)
    expect(conn.commands.value).toBeNull()
    f.owner.transcript({ t: 'commands', rev: 2, commands: [{ name: 'compact' }] })
    expect(conn.commands.value?.list.map((c) => c.name)).toEqual(['compact'])
    // An older rev is not taken; a higher one replaces, an empty list among them.
    f.owner.transcript({ t: 'commands', rev: 1, commands: [{ name: 'old' }] })
    expect(conn.commands.value?.rev).toBe(2)
    f.owner.transcript({ t: 'commands', rev: 3, commands: [], more: false })
    expect(conn.commands.value).toEqual({ rev: 3, list: [], more: false })
    f.owner.transcript({ t: 'commands', rev: 4, commands: [{ name: 'model' }], more: true })
    f.owner.control({ t: 'way', way: 'terminal' } as unknown as ControlMessage)
    expect(conn.commands.value).toBeNull()
    f.owner.transcript({ t: 'commands', rev: 5, commands: [{ name: 'model' }] })
    f.owner.control({ t: 'welcome', role: 'control', fileView: true, way: 'protocol' } as unknown as Welcome)
    expect(conn.commands.value).toBeNull()
  })
})
