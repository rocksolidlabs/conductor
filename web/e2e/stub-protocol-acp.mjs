// stub-protocol-acp.mjs is stub-protocol.mjs's third mode: it stands in for an agent's Agent Client Protocol server on stdio
// (a chat's child of an ACP agent, the protocol way) in the e2e suite and in the server's and internal/agentproto's tests.
// It speaks protocol 1 in the shapes of its schema (internal/agentproto/testdata/acp): newline-delimited JSON-RPC 2.0. It holds
// no account, reads no file of anyone's and runs nothing it reads.
//
//   stub-protocol.mjs --version              (STUB_PROTOCOL_AGENT=acp) prints "stub-acp 1.0.0"
//   stub-protocol.mjs acp | --acp | --profile acp   speaks the protocol on stdin and stdout
//
// It answers initialize (protocol 1, loadSession, and session/resume unless STUB_ACP_NO_RESUME=1), takes up the session it is
// asked to (what it carried kept in $HOME/.stub-sessions/<session>.resume, every line it read in <session>.wire, when that
// folder is there; a load replays one old message first), and answers each session/prompt with a turn: the words "stub chat:
// <the message>" in two chunks and the prompt's answer end_turn. Each turn's message goes into <session>.txt as
// "chat: <message>". session/cancel ends the turn under way cancelled. It advertises the command "compact"
// (available_commands_update). Its input's end ends it.
//
// What the environment chooses:
//   STUB_ACP_NO_RESUME=1    no session/resume: the session is taken up by session/load
//   STUB_ACP_NO_LOAD=1      neither: no session can be taken up
//   STUB_ACP_VERSION        the protocol version initialize answers (1 unless set)
//   STUB_ACP_AUTH=1         the session is refused auth_required, as an agent not signed in refuses it
//   STUB_ACP_OPTIONS        the permission options' ids, by kind: "opencode" gives OpenCode's (once, always, reject); the
//                           default's are allow-once-id, allow-always-id, reject-once-id
//
// A turn is scripted by words in its message:
//   "tool"        one execute step (`ls`) with its output, before the words
//   "edit"        an edit step that really writes notes.txt in the folder (a line added), its diff the step's content
//   "hold"        the turn's words wait: <session>.held is written, and it goes on once <session>.go exists (or the turn is
//                 cancelled)
//   "ask"         an execute step announced, then session/request_permission with the options, held until answered; the
//                 outcome is written to <session>.outcome; selected allow_once, the step runs; anything else, it fails.
//                 "ask withdraw": the request is taken back ($/cancel_request) once <session>.withdraw exists
//   "fs"          the agent asks fs/read_text_file and terminal/create of the client and writes the answers' error codes to
//                 <session>.refused
import { appendFileSync, existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { createInterface } from 'node:readline'

const args = process.argv.slice(2)
if (args.includes('--version')) {
  process.stdout.write('stub-acp 1.0.0\n')
  process.exit(0)
}

const send = (m) => process.stdout.write(`${JSON.stringify({ jsonrpc: '2.0', ...m })}\n`)
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
let n = 0
const next = (prefix) => `${prefix}_stub_${++n}`

const kept = join(process.env.HOME || '/nonexistent', '.stub-sessions')
let sessionId = ''
const keptFile = (ext) => join(kept, `${sessionId}.${ext}`)
const keep = (ext, text) => {
  if (sessionId && existsSync(kept)) writeFileSync(keptFile(ext), text)
}
let wire = []

let cancelled = false
let turns = Promise.resolve()

let reqSeq = 1000
const waiting = new Map()
function request(method, params) {
  const id = ++reqSeq
  return { id, done: new Promise((resolve) => { waiting.set(id, resolve); send({ id, method, params }) }) }
}
const update = (u) => send({ method: 'session/update', params: { sessionId, update: u } })

function options() {
  if (process.env.STUB_ACP_OPTIONS === 'opencode') {
    return [{ optionId: 'once', name: 'Allow once', kind: 'allow_once' }, { optionId: 'always', name: 'Always allow', kind: 'allow_always' }, { optionId: 'reject', name: 'Reject', kind: 'reject_once' }]
  }
  return [{ optionId: 'allow-once-id', name: 'Allow', kind: 'allow_once' }, { optionId: 'allow-always-id', name: 'Always', kind: 'allow_always' }, { optionId: 'reject-once-id', name: 'Reject', kind: 'reject_once' }]
}

async function ask(kind) {
  const call = next('call')
  const rawInput = { command: kind === 'withdraw' ? 'echo withdrawn' : 'echo allowed' }
  update({ sessionUpdate: 'tool_call', toolCallId: call, title: rawInput.command, kind: 'execute', status: 'pending', rawInput })
  const r = request('session/request_permission', { sessionId, toolCall: { toolCallId: call }, options: options() })
  if (kind === 'withdraw') {
    let answered = false
    void r.done.then(() => (answered = true))
    while (!answered && !existsSync(keptFile('withdraw')) && !cancelled) await sleep(20)
    if (!answered) send({ method: '$/cancel_request', params: { requestId: r.id } })
  }
  const answer = await r.done
  const outcome = answer?.result?.outcome ?? { outcome: 'error', code: answer?.error?.code }
  keep('outcome', `${JSON.stringify(outcome)}\n`)
  const allowed = outcome.outcome === 'selected' && options().some((o) => o.optionId === outcome.optionId && o.kind === 'allow_once')
  update({ sessionUpdate: 'tool_call_update', toolCallId: call, status: allowed ? 'completed' : 'failed',
    content: [{ type: 'content', content: { type: 'text', text: allowed ? `ran: ${rawInput.command}` : 'denied' } }] })
}

async function run(text) {
  cancelled = false
  if (/\btool\b/.test(text)) {
    const call = next('call')
    update({ sessionUpdate: 'tool_call', toolCallId: call, title: 'ls', kind: 'execute', status: 'in_progress', rawInput: { command: 'ls' } })
    update({ sessionUpdate: 'tool_call_update', toolCallId: call, status: 'completed', content: [{ type: 'content', content: { type: 'text', text: 'notes.txt' } }] })
  }
  if (/\bedit\b/.test(text)) {
    const path = join(process.cwd(), 'notes.txt')
    const before = existsSync(path) ? readFileSync(path, 'utf8') : ''
    writeFileSync(path, `${before}gamma\n`)
    const call = next('call')
    update({ sessionUpdate: 'tool_call', toolCallId: call, title: 'Edit notes.txt', kind: 'edit', status: 'in_progress', rawInput: { path } })
    update({ sessionUpdate: 'tool_call_update', toolCallId: call, status: 'completed', content: [{ type: 'diff', path, oldText: before, newText: `${before}gamma\n` }] })
  }
  if (/\bask\b/.test(text)) await ask(/\bask withdraw\b/.test(text) ? 'withdraw' : 'command')
  if (/\bfs\b/.test(text)) {
    const a = await request('fs/read_text_file', { sessionId, path: join(process.cwd(), 'notes.txt') }).done
    const b = await request('terminal/create', { sessionId, command: 'ls' }).done
    keep('refused', `${a?.error?.code} ${b?.error?.code}\n`)
  }
  if (/\bhold\b/.test(text) && existsSync(kept)) {
    keep('held', 'held\n')
    while (!existsSync(keptFile('go')) && !cancelled) await sleep(20)
  }
  const words = `stub chat: ${text}`
  const msg = next('msg')
  const half = Math.ceil(words.length / 2)
  update({ sessionUpdate: 'agent_message_chunk', messageId: msg, content: { type: 'text', text: words.slice(0, half) } })
  if (cancelled) return 'cancelled'
  update({ sessionUpdate: 'agent_message_chunk', messageId: msg, content: { type: 'text', text: words.slice(half) } })
  if (existsSync(kept)) appendFileSync(keptFile('txt'), `chat: ${text.slice(0, 200)}\n`)
  return 'end_turn'
}

const lines = createInterface({ input: process.stdin })
lines.on('line', (line) => {
  let m
  try {
    m = JSON.parse(line)
  } catch {
    return
  }
  if (sessionId && existsSync(kept)) appendFileSync(keptFile('wire'), `${line}\n`)
  else wire.push(line)
  if (m.method === undefined && m.id !== undefined) {
    const resolve = waiting.get(m.id)
    waiting.delete(m.id)
    if (resolve) resolve(m)
    return
  }
  switch (m.method) {
    case 'initialize': {
      const sessionCapabilities = process.env.STUB_ACP_NO_RESUME === '1' || process.env.STUB_ACP_NO_LOAD === '1' ? {} : { resume: {} }
      send({ id: m.id, result: { protocolVersion: Number(process.env.STUB_ACP_VERSION || 1), agentInfo: { name: 'stub-acp', version: '1.0.0' },
        agentCapabilities: { loadSession: process.env.STUB_ACP_NO_LOAD !== '1', promptCapabilities: { image: false, audio: false, embeddedContext: false }, sessionCapabilities }, authMethods: [] } })
      return
    }
    case 'session/resume':
    case 'session/load': {
      if (process.env.STUB_ACP_AUTH === '1') {
        send({ id: m.id, error: { code: -32000, message: 'Authentication required' } })
        return
      }
      sessionId = m.params?.sessionId ?? ''
      keep('resume', `${JSON.stringify({ method: m.method, ...m.params })}\n`)
      if (existsSync(kept) && sessionId) {
        appendFileSync(keptFile('wire'), wire.map((l) => `${l}\n`).join(''))
        wire = []
      }
      if (m.method === 'session/load') update({ sessionUpdate: 'agent_message_chunk', messageId: 'old', content: { type: 'text', text: 'an old answer' } })
      update({ sessionUpdate: 'available_commands_update', availableCommands: [{ name: 'compact', description: 'Compact the conversation' }] })
      send({ id: m.id, result: {} })
      return
    }
    case 'session/prompt': {
      const prompt = Array.isArray(m.params?.prompt) ? m.params.prompt : []
      const text = prompt.filter((b) => b?.type === 'text').map((b) => b.text).join('\n')
      turns = turns.then(async () => {
        const stopReason = await run(text)
        send({ id: m.id, result: { stopReason } })
      })
      return
    }
    default:
      if (m.method === 'session/cancel') {
        cancelled = true
        return
      }
      if (m.id !== undefined) send({ id: m.id, error: { code: -32601, message: `stub: no method ${m.method}` } })
  }
})
lines.on('close', () => {
  turns.then(() => process.exit(0))
})
