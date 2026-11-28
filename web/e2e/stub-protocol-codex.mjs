// stub-protocol-codex.mjs is stub-protocol.mjs's second mode: it stands in for Codex's app server on stdio (a chat's child of
// Codex, the protocol way) in the e2e suite and in the server's and internal/agentproto's tests. It speaks the shapes the
// driver was written to (internal/agentproto/testdata/codex, from the stable schema of Codex 0.167.2): JSON-RPC 2.0 with the
// header left out, one message a line. It holds no account, reads no file of anyone's and runs nothing it reads.
//
//   stub-protocol.mjs --version           (STUB_PROTOCOL_AGENT=codex) prints "codex-cli 0.167.2" (STUB_PROTOCOL_VERSION
//                                         changes the number)
//   stub-protocol.mjs app-server --listen stdio://   speaks the app server on stdin and stdout
//
// It answers initialize (a user agent that names its version), resumes the thread it is asked to (what thread/resume
// carried is written to $HOME/.stub-sessions/<thread>.resume, and every line it read to <thread>.wire, when that folder is
// there), and answers each turn/start with a turn: the user's message as an item, the words "stub chat: <the message>"
// streamed in two pieces and whole, the thread's token use and the turn's completion. Each turn's message goes into
// $HOME/.stub-sessions/<thread>.txt as "chat: <message>" (the terminal stub's transcript, which a terminal resumed on the
// thread prints). turn/interrupt ends the turn under way interrupted. Its input's end ends it.
//
// What the environment chooses:
//   STUB_PROTOCOL_VERSION   the version said by --version and the user agent (0.167.2 unless set)
//   STUB_PROTOCOL_INIT      "none": initialize is not answered; "bad": it is answered without a user agent
//   STUB_PROTOCOL_SIGNED_OUT=1  the first turn fails unauthorized, as a signed-out Codex's does
//   STUB_PROTOCOL_HOLD      a file: each turn's words wait until it exists (a test's gate on a turn)
//
// A turn is scripted by words in its message:
//   "tool"        one command (`ls`) with its output, before the words
//   "edit"        a file change that really writes notes.txt in the folder (a line added), its diff the change's
//   "hold"        the turn's words wait at a gate of its own: <thread>.held is written as it waits, and it goes on once
//                 <thread>.go exists (or the turn is interrupted)
//   "ask command" a command announced, then put to the client as item/commandExecution/requestApproval, held until
//                 answered; "ask edit" a file change put as item/fileChange/requestApproval; "ask withdraw" a command
//                 asked about and taken back (serverRequest/resolved with no answer) once <thread>.withdraw exists.
//                 Accepted, the step runs (its output "ran: <command>", or the file written) and <thread>.allowed is
//                 written with the request's id; declined, the step is declined and <thread>.denied holds the request's
//                 id and its decision. serverRequest/resolved follows either way.
import { appendFileSync, existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { createInterface } from 'node:readline'

const version = process.env.STUB_PROTOCOL_VERSION || '0.167.2'
const args = process.argv.slice(2)
if (args.includes('--version')) {
  process.stdout.write(`codex-cli ${version}\n`)
  process.exit(0)
}
if (!args.includes('app-server')) {
  process.stderr.write('stub-protocol (codex): the app server alone (app-server)\n')
  process.exit(2)
}

const send = (m) => process.stdout.write(`${JSON.stringify(m)}\n`)
const notify = (method, params) => send({ method, params })
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
let n = 0
const next = (prefix) => `${prefix}_stub_${++n}`

const kept = join(process.env.HOME || '/nonexistent', '.stub-sessions')
let thread = ''
const keptFile = (ext) => join(kept, `${thread}.${ext}`)
const keep = (ext, text) => {
  if (thread && existsSync(kept)) writeFileSync(keptFile(ext), text)
}
let wire = []

let signedOutLeft = process.env.STUB_PROTOCOL_SIGNED_OUT === '1'
let interrupted = false
let turnId = ''
let turns = Promise.resolve()
let total = { inputTokens: 0, cachedInputTokens: 0, outputTokens: 0, reasoningOutputTokens: 0, totalTokens: 0 }

// The server's own requests waiting for the client's answer, by id.
let reqSeq = 0
const waiting = new Map()
function request(method, params) {
  const id = ++reqSeq
  return new Promise((resolve) => {
    waiting.set(id, resolve)
    send({ id, method, params })
  })
}

const turn = (status, error = null) => ({ id: turnId, items: [], status, error, startedAt: null, completedAt: null, durationMs: null })
const started = (item) => notify('item/started', { threadId: thread, turnId, item, startedAtMs: Date.now() })
const completed = (item) => notify('item/completed', { threadId: thread, turnId, item, completedAtMs: Date.now() })

async function hold() {
  const gate = process.env.STUB_PROTOCOL_HOLD
  while (gate && !existsSync(gate) && !interrupted) await sleep(20)
}

function command(id, cmd) {
  return { type: 'commandExecution', id, command: cmd, cwd: process.cwd(), processId: null, source: 'agent', status: 'inProgress', commandActions: [{ type: 'unknown', command: cmd }], aggregatedOutput: null, exitCode: null, durationMs: null }
}

/** A file change that adds a line to notes.txt in the folder: written only when `write`. */
function noteChange(write) {
  const path = join(process.cwd(), 'notes.txt')
  const before = existsSync(path) ? readFileSync(path, 'utf8') : ''
  const old = before.split('\n').filter(Boolean)
  if (write) writeFileSync(path, `${before}gamma\n`)
  const diff = `@@ -1,${old.length} +1,${old.length + 1} @@\n${old.map((l) => ` ${l}\n`).join('')}+gamma\n`
  return { path, kind: { type: 'update', move_path: null }, diff }
}

/** A step put to the client to approve: its answer applied, the request resolved. */
async function ask(kind) {
  if (kind === 'edit') {
    const id = next('call')
    const item = { type: 'fileChange', id, changes: [noteChange(false)], status: 'inProgress' }
    started(item)
    const answer = await request('item/fileChange/requestApproval', { threadId: thread, turnId, itemId: id, reason: null, grantRoot: null, startedAtMs: Date.now() })
    const decision = answer?.result?.decision ?? 'decline'
    if (answer?.result) notify('serverRequest/resolved', { threadId: thread, requestId: reqSeq })
    if (decision === 'accept') {
      keep('allowed', `${reqSeq}\n`)
      completed({ ...item, changes: [noteChange(true)], status: 'completed' })
    } else {
      keep('denied', `${reqSeq} ${decision}\n`)
      completed({ ...item, status: 'declined' })
    }
    return
  }
  const id = next('call')
  const cmd = kind === 'withdraw' ? 'echo withdrawn' : 'echo allowed'
  const item = command(id, cmd)
  started(item)
  const asked = request('item/commandExecution/requestApproval', { threadId: thread, turnId, itemId: id, approvalId: null, reason: null, command: cmd, cwd: process.cwd(), commandActions: [], startedAtMs: Date.now() })
  const askedId = reqSeq
  if (kind === 'withdraw') {
    let answered = false
    void asked.then(() => (answered = true))
    while (!answered && !existsSync(keptFile('withdraw')) && !interrupted) await sleep(20)
    if (!answered) {
      waiting.delete(askedId)
      notify('serverRequest/resolved', { threadId: thread, requestId: askedId })
      completed({ ...item, status: 'declined' })
      return
    }
  }
  const answer = await asked
  const decision = answer?.result?.decision ?? 'decline'
  if (answer?.result) notify('serverRequest/resolved', { threadId: thread, requestId: askedId })
  if (decision === 'accept') {
    keep('allowed', `${askedId}\n`)
    completed({ ...item, status: 'completed', aggregatedOutput: `ran: ${cmd}\n`, exitCode: 0, durationMs: 3 })
  } else {
    keep('denied', `${askedId} ${decision}\n`)
    completed({ ...item, status: 'declined' })
  }
}

async function run(text) {
  started({ type: 'userMessage', id: next('user'), content: [{ type: 'text', text, text_elements: [] }] })
  if (signedOutLeft) {
    signedOutLeft = false
    const error = { message: 'unexpected status 401 Unauthorized', codexErrorInfo: 'unauthorized', additionalDetails: null }
    notify('error', { threadId: thread, turnId, willRetry: false, error })
    notify('turn/completed', { threadId: thread, turn: turn('failed', error) })
    return
  }
  interrupted = false
  if (/\btool\b/.test(text)) {
    const item = command(next('call'), 'ls')
    started(item)
    completed({ ...item, status: 'completed', aggregatedOutput: 'notes.txt\n', exitCode: 0, durationMs: 2 })
  }
  if (/\bedit\b/.test(text) && !/\bask edit\b/.test(text)) {
    const id = next('call')
    started({ type: 'fileChange', id, changes: [noteChange(false)], status: 'inProgress' })
    completed({ type: 'fileChange', id, changes: [noteChange(true)], status: 'completed' })
  }
  const m = /\bask (command|edit|withdraw)\b/.exec(text)
  if (m) await ask(m[1])
  if (/\bhold\b/.test(text) && existsSync(kept)) {
    keep('held', 'held\n')
    while (!existsSync(keptFile('go')) && !interrupted) await sleep(20)
  }
  const words = `stub chat: ${text}`
  const msg = next('msg')
  started({ type: 'agentMessage', id: msg, text: '', phase: null })
  const half = Math.ceil(words.length / 2)
  notify('item/agentMessage/delta', { threadId: thread, turnId, itemId: msg, delta: words.slice(0, half) })
  await hold()
  if (interrupted) {
    notify('turn/completed', { threadId: thread, turn: turn('interrupted') })
    return
  }
  notify('item/agentMessage/delta', { threadId: thread, turnId, itemId: msg, delta: words.slice(half) })
  completed({ type: 'agentMessage', id: msg, text: words, phase: 'final_answer' })
  total = { ...total, inputTokens: total.inputTokens + 14, cachedInputTokens: total.cachedInputTokens + 10, outputTokens: total.outputTokens + 7, totalTokens: total.totalTokens + 21 }
  notify('thread/tokenUsage/updated', { threadId: thread, turnId, tokenUsage: { total, last: total, modelContextWindow: 200000 } })
  if (existsSync(kept)) appendFileSync(keptFile('txt'), `chat: ${text.slice(0, 200)}\n`)
  notify('turn/completed', { threadId: thread, turn: turn('completed') })
}

const lines = createInterface({ input: process.stdin })
lines.on('line', (line) => {
  let m
  try {
    m = JSON.parse(line)
  } catch {
    return
  }
  if (thread && existsSync(kept)) appendFileSync(keptFile('wire'), `${line}\n`)
  else wire.push(line)
  if (m.method === undefined && m.id !== undefined) {
    const resolve = waiting.get(m.id)
    waiting.delete(m.id)
    if (resolve) resolve(m)
    return
  }
  switch (m.method) {
    case 'initialize':
      if (process.env.STUB_PROTOCOL_INIT === 'none') return
      send({ id: m.id, result: process.env.STUB_PROTOCOL_INIT === 'bad' ? { platformOs: 'linux' } : { userAgent: `codex_cli_rs/${version} (stub)`, codexHome: '/codex-home', platformFamily: 'unix', platformOs: 'linux' } })
      return
    case 'initialized':
      return
    case 'thread/resume': {
      thread = m.params?.threadId ?? ''
      keep('resume', `${JSON.stringify(m.params ?? {})}\n`)
      if (existsSync(kept) && thread) {
        appendFileSync(keptFile('wire'), wire.map((l) => `${l}\n`).join(''))
        wire = []
      }
      send({ id: m.id, result: { thread: { id: thread, preview: '', ephemeral: false, modelProvider: 'stub', createdAt: 0, updatedAt: 0, status: { type: 'idle' }, cwd: process.cwd(), cliVersion: version, source: 'cli', sessionId: thread, projectId: null, turns: [] },
        model: m.params?.model ?? 'stub', modelProvider: 'stub', cwd: process.cwd(), approvalPolicy: m.params?.approvalPolicy ?? 'on-request', approvalsReviewer: 'user', sandbox: { type: 'workspaceWrite' } } })
      return
    }
    case 'turn/start': {
      const input = Array.isArray(m.params?.input) ? m.params.input : []
      const text = input.filter((i) => i?.type === 'text').map((i) => i.text).join('\n')
      turns = turns.then(async () => {
        turnId = next('turn')
        send({ id: m.id, result: { turn: turn('inProgress') } })
        notify('turn/started', { threadId: thread, turn: turn('inProgress') })
        await run(text)
      })
      return
    }
    case 'turn/interrupt':
      interrupted = true
      send({ id: m.id, result: {} })
      // The turn's open requests are cleared with it, as Codex clears them.
      for (const [id, resolve] of waiting) {
        waiting.delete(id)
        notify('serverRequest/resolved', { threadId: thread, requestId: id })
        resolve({})
      }
      return
    default:
      if (m.id !== undefined) send({ id: m.id, error: { code: -32601, message: `stub: no method ${m.method}` } })
  }
})
lines.on('close', () => {
  turns.then(() => process.exit(0))
})
