import { describe, expect, it } from 'vitest'
import { ref } from 'vue'
import { MAX_OWN_ANSWERS, useSessionConnection } from './useSessionConnection'
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
    // An answer the owner takes, unless its text says "refused".
    answer: async (r: { question?: string; text?: string }) => (sent.push(`answer ${r.question} ${r.text}`), r.text !== 'refused'),
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

  it('keepsOwnAnswers: the text of an answer this browser sent is kept once the owner took it, for its chat to show its sender alone', async () => {
    const conn = useSessionConnection(() => fakeTransport().transport)
    await conn.connect({ cols: 80, rows: 24 })
    expect(await conn.answer({ question: 'q-1', text: 'Yes, go on.' })).toBe(true)
    expect(conn.ownAnswer('q-1')).toBe('Yes, go on.')
    // Refused (another's answer came first): never shown as the one the chat holds.
    expect(await conn.answer({ question: 'q-2', text: 'refused' })).toBe(false)
    expect(conn.ownAnswer('q-2')).toBeUndefined()
    // A choice's keys carry no text; and only the last MAX_OWN_ANSWERS are kept.
    await conn.answer({ question: 'q-3', input: '1' })
    expect(conn.ownAnswer('q-3')).toBeUndefined()
    for (let i = 0; i < MAX_OWN_ANSWERS; i++) await conn.answer({ question: `n-${i}`, text: `a${i}` })
    expect(conn.ownAnswer('q-1')).toBeUndefined()
    expect(conn.ownAnswer(`n-${MAX_OWN_ANSWERS - 1}`)).toBe(`a${MAX_OWN_ANSWERS - 1}`)
  })
})
