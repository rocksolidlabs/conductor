#!/usr/bin/env node
// stub-protocol.mjs stands in for Claude Code's print mode with stream-json in and out (a chat's child, the protocol way) in
// the e2e suite and in internal/agentproto's tests. It speaks the shapes the driver was qualified against
// (internal/agentproto/testdata/claude, recorded from Claude Code 2.1.331 with every account field removed): it holds no
// account, reads no file of anyone's and runs nothing it reads.
//
//   stub-protocol.mjs --version          prints "2.1.331 (Claude Code)" (STUB_PROTOCOL_VERSION changes the number)
//   stub-protocol.mjs -p ... --resume ID speaks stream-json on stdin and stdout, on the conversation ID
//
// Each user message it is written is replayed (its uuid kept), then answered in one turn: the system init, the words
// "stub chat: <the message>" streamed and whole, and a result with usage. A message that holds the word "tool" runs one
// Bash step first (`ls`, never run: its result is fixed). The interrupt request is answered with its receipt and the
// turn ends error_during_execution, aborted_streaming. Its input's end ends it.
//
// What the environment chooses:
//   STUB_PROTOCOL_VERSION   the version said by --version and the initialize answer (2.1.331 unless set)
//   STUB_PROTOCOL_INIT      "none": the initialize request is not answered; "bad": it is answered without its
//                           pending_permission_requests list (the shape check fails)
//   STUB_PROTOCOL_SIGNED_OUT=1  the first message is answered as a signed-out Claude Code answers it
//   STUB_PROTOCOL_HOLD      a file: each turn's words wait until it exists (a test's gate on a turn)
//   STUB_PROTOCOL_NO_RECEIPT=1  the init offers no interrupt receipt
//   STUB_PROTOCOL_CHILD=1   it starts a process of its own in its group (a sleep, its standard streams closed) that
//                           outlives it, as a tool's process may: what a move proves gone with the group
import { spawn } from 'node:child_process'
import { existsSync } from 'node:fs'
import { createInterface } from 'node:readline'

const version = process.env.STUB_PROTOCOL_VERSION || '2.1.331'
const args = process.argv.slice(2)
if (args.includes('--version')) {
  process.stdout.write(`${version} (Claude Code)\n`)
  process.exit(0)
}
if (!args.includes('-p')) {
  process.stderr.write('stub-protocol: a chat child alone (-p)\n')
  process.exit(2)
}
const resume = args.indexOf('--resume')
const session = resume >= 0 ? args[resume + 1] : '00000000-0000-4000-8000-000000000001'
const permissionMode = args.includes('--dangerously-skip-permissions') ? 'bypassPermissions' : 'default'

if (process.env.STUB_PROTOCOL_CHILD === '1') spawn('sleep', ['600'], { stdio: 'ignore' }).unref()

let n = 0
const id = (prefix) => `${prefix}_01Stub${String(++n).padStart(18, '0')}`
const uuid = () => `00000000-0000-4000-8000-${String(++n).padStart(12, '0')}`
const send = (m) => process.stdout.write(`${JSON.stringify(m)}\n`)
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

let cost = 0
let signedOutLeft = process.env.STUB_PROTOCOL_SIGNED_OUT === '1'
let interrupted = false
let turns = Promise.resolve()

async function hold() {
  const gate = process.env.STUB_PROTOCOL_HOLD
  while (gate && !existsSync(gate) && !interrupted) await sleep(20)
}

async function turn(m) {
  const text = typeof m.message?.content === 'string' ? m.message.content : ''
  send({ type: 'system', subtype: 'init', cwd: process.cwd(), session_id: session, tools: ['Bash', 'Read'], mcp_servers: [],
    model: 'stub', permissionMode, claude_code_version: version, output_style: 'default',
    capabilities: process.env.STUB_PROTOCOL_NO_RECEIPT === '1' ? ['msg_lifecycle_v1'] : ['interrupt_receipt_v1', 'msg_lifecycle_v1'],
    uuid: uuid() })
  send({ type: 'user', message: { role: 'user', content: text }, session_id: session, parent_tool_use_id: null, uuid: m.uuid, isReplay: true })
  if (signedOutLeft) {
    signedOutLeft = false
    send({ type: 'assistant', message: { model: '<synthetic>', id: id('msg'), type: 'message', role: 'assistant',
      content: [{ type: 'text', text: 'Not logged in · Please run /login' }], stop_reason: 'stop_sequence', usage: { input_tokens: 0, output_tokens: 0 } },
    parent_tool_use_id: null, session_id: session, uuid: uuid(), error: 'authentication_failed' })
    send({ type: 'result', subtype: 'success', is_error: true, api_error_status: 401, num_turns: 1, result: 'Not logged in · Please run /login',
      session_id: session, total_cost_usd: cost, usage: { input_tokens: 0, output_tokens: 0 }, user_message_uuid: m.uuid, uuid: uuid() })
    return
  }
  interrupted = false
  if (/\btool\b/.test(text)) {
    const call = id('toolu')
    send({ type: 'assistant', message: { model: 'stub', id: id('msg'), type: 'message', role: 'assistant',
      content: [{ type: 'tool_use', id: call, name: 'Bash', input: { command: 'ls', description: 'List the folder' } }], stop_reason: null,
      usage: { input_tokens: 2, output_tokens: 5 } }, parent_tool_use_id: null, session_id: session, uuid: uuid() })
    send({ type: 'user', message: { role: 'user', content: [{ tool_use_id: call, type: 'tool_result', content: 'notes.txt', is_error: false }] },
      parent_tool_use_id: null, session_id: session, uuid: uuid(),
      tool_use_result: { stdout: 'notes.txt', stderr: '', interrupted: false, isImage: false } })
  }
  const msg = id('msg')
  const words = `stub chat: ${text}`
  send({ type: 'stream_event', event: { type: 'message_start', message: { id: msg, type: 'message', role: 'assistant', content: [], model: 'stub' } },
    parent_tool_use_id: null, session_id: session, uuid: uuid() })
  send({ type: 'stream_event', event: { type: 'content_block_start', index: 0, content_block: { type: 'text', text: '' } }, parent_tool_use_id: null, session_id: session, uuid: uuid() })
  const half = Math.ceil(words.length / 2)
  send({ type: 'stream_event', event: { type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: words.slice(0, half) } },
    parent_tool_use_id: null, session_id: session, uuid: uuid() })
  await hold()
  if (interrupted) {
    send({ type: 'assistant', message: { model: 'stub', id: msg, type: 'message', role: 'assistant', content: [{ type: 'text', text: words.slice(0, half) }],
      stop_reason: null, usage: { input_tokens: 2, output_tokens: 3 } }, parent_tool_use_id: null, session_id: session, uuid: uuid(), aborted: true })
    send({ type: 'user', message: { role: 'user', content: [{ type: 'text', text: '[Request interrupted by user]' }] }, parent_tool_use_id: null, session_id: session, uuid: uuid() })
    send({ type: 'result', subtype: 'error_during_execution', is_error: true, num_turns: 1, stop_reason: null, terminal_reason: 'aborted_streaming',
      session_id: session, total_cost_usd: cost, usage: { input_tokens: 0, output_tokens: 0 }, errors: [], user_message_uuid: m.uuid, uuid: uuid() })
    return
  }
  send({ type: 'stream_event', event: { type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: words.slice(half) } },
    parent_tool_use_id: null, session_id: session, uuid: uuid() })
  send({ type: 'assistant', message: { model: 'stub', id: msg, type: 'message', role: 'assistant', content: [{ type: 'text', text: words }],
    stop_reason: null, usage: { input_tokens: 2, output_tokens: 7 } }, parent_tool_use_id: null, session_id: session, uuid: uuid() })
  send({ type: 'stream_event', event: { type: 'content_block_stop', index: 0 }, parent_tool_use_id: null, session_id: session, uuid: uuid() })
  send({ type: 'stream_event', event: { type: 'message_stop' }, parent_tool_use_id: null, session_id: session, uuid: uuid() })
  cost += 0.001
  send({ type: 'result', subtype: 'success', is_error: false, num_turns: 1, result: words, stop_reason: 'end_turn', terminal_reason: 'completed',
    session_id: session, total_cost_usd: cost, usage: { input_tokens: 4, cache_creation_input_tokens: 0, cache_read_input_tokens: 10, output_tokens: 7 },
    user_message_uuid: m.uuid, uuid: uuid() })
}

const lines = createInterface({ input: process.stdin })
lines.on('line', (line) => {
  let m
  try {
    m = JSON.parse(line)
  } catch {
    return
  }
  if (m.type === 'control_request') {
    const sub = m.request?.subtype
    if (sub === 'initialize') {
      if (process.env.STUB_PROTOCOL_INIT === 'none') return
      const response = { subtype: 'success', request_id: m.request_id,
        response: { output_style: 'default', pid: process.pid, claude_code_version: version, current_permission_mode: permissionMode, capabilities: ['ui_surface_v1'] },
        pending_user_dialog_requests: [] }
      if (process.env.STUB_PROTOCOL_INIT !== 'bad') response.pending_permission_requests = []
      send({ type: 'control_response', response })
    } else if (sub === 'interrupt') {
      interrupted = true
      send({ type: 'control_response', response: { subtype: 'success', request_id: m.request_id, response: { still_queued: [] } } })
    } else {
      send({ type: 'control_response', response: { subtype: 'error', request_id: m.request_id, error: 'not supported by the stub' } })
    }
    return
  }
  if (m.type === 'user') turns = turns.then(() => turn(m))
})
lines.on('close', () => {
  turns.then(() => process.exit(0))
})
