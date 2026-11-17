import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { homedir, tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { test as base, expect, type Page } from '@playwright/test'
import { Api, logged, member, type Run, type Session } from './fixtures'
import { git, renderConfig, scratchRepo, startServer, stopServer, type Started } from './server'

// The live tier: the real Claude Code and Codex, launched as one-member crews
// by a server of their own. Skipped unless CONDUCTOR_E2E_LIVE=1 and
// CONDUCTOR_E2E_LIVE_REPO names a git repository both agents already trust
// (a worktree of a trusted repository needs no trust question), and the
// agent is on the PATH. It runs with the real HOME, where the agents keep
// their logins (or the keys in ANTHROPIC_API_KEY and OPENAI_API_KEY, which
// pass through); it writes nothing there itself beyond what the agents
// write as they always do, and takes the trust it made them save off again.
// The worktrees and branches it makes in the repository are removed after.
// A machine nobody has used the agents on (a CI runner) is brought there
// first by scripts/live_setup.py; in CI an agent whose key the run does not
// hold is skipped, and Codex's hooks row with it (see there). A row whose
// agent is held by a first-run, sign-in or trust screen says so in one
// line instead of waiting its time out.
//
// What it checks, per agent: a typed prompt answered without a person
// pressing Enter (and, for Codex, no false "needs input" from its hidden
// title thread meanwhile); the trust question held in a repository the
// agent has not seen, answered by Enter in its terminal; yolo (Codex's trust
// override makes the question not appear and its config gains nothing;
// Claude Code still asks); and a conversation resumed after its session
// ended.

const here = dirname(fileURLToPath(import.meta.url))
const repo = process.env.CONDUCTOR_E2E_LIVE_REPO ?? ''
const live = process.env.CONDUCTOR_E2E_LIVE === '1' && repo !== ''
const PROMPT = 'Reply with the single word READY and nothing else'
// A write and a read through the agent's own file tools, so its hooks report both (design 4e); the reply says it is done.
// The file is the agent's own: the trusted repository promises no file of its own at HEAD.
const FILES_PROMPT = 'Create a file named touched.txt containing the single word TOUCHED with your file editing tool (not a shell command), then read touched.txt back with your file reading tool, then reply with the single word TOUCHED and nothing else'
const TRUST_WORDS: Record<string, RegExp> = { claude: /project you created or one you trust/i, codex: /trust this folder/i }
// What answers the trust question with "yes": Claude Code 2.1.288 highlights "No, exit" first, so its yes is Down then Enter
// (verified in a PTY on 2026-10-03); Codex 0.159 highlights the trusting answer, so Enter alone.
const TRUST_YES: Record<string, string[]> = { claude: ['ArrowDown', 'Enter'], codex: ['Enter'] }
// What the live server's sessions get beside the keys: the cheapest model Claude Code takes from its environment, by its alias
// (it follows the newest release of that tier; CONDUCTOR_E2E_LIVE_CLAUDE_MODEL names another). Codex runs its default. Claude
// Code's demo switch (IS_DEMO) is NOT set: with it the tool asked no trust question in a repository it had not seen (#988).
const SESSION_ENV: Record<string, string> = {
  ANTHROPIC_MODEL: process.env.CONDUCTOR_E2E_LIVE_CLAUDE_MODEL ?? 'haiku',
}
// The key each agent runs on where nobody is signed in.
const KEY: Record<string, string> = { claude: 'ANTHROPIC_API_KEY', codex: 'OPENAI_API_KEY' }

function onPath(program: string): boolean {
  try {
    execFileSync('sh', ['-c', 'command -v "$1"', 'sh', program], { stdio: 'ignore' })
    return true
  } catch {
    return false
  }
}

/** A file event of the admin stream (`event: activity` with `type: file`): what the agent's hooks reported it touched. */
interface FileEvent {
  sessionId: string
  op: string
  path: string
  tool?: string
}

/** Reads the admin event stream into `into` until closed: every file event of every session, as the live feed gets them. */
function watchFileEvents(server: Started, into: FileEvent[]): { close: () => void } {
  const ctl = new AbortController()
  void (async () => {
    try {
      const r = await fetch(`${server.baseURL}/api/events`, { headers: { Authorization: `Bearer ${server.token}` }, signal: ctl.signal })
      const reader = r.body!.getReader()
      const dec = new TextDecoder()
      let buf = ''
      for (;;) {
        const { value, done } = await reader.read()
        if (done) break
        buf += dec.decode(value, { stream: true })
        let i: number
        while ((i = buf.indexOf('\n\n')) >= 0) {
          const block = buf.slice(0, i)
          buf = buf.slice(i + 2)
          const event = /^event: (.*)$/m.exec(block)?.[1]
          const data = block
            .split('\n')
            .filter((l) => l.startsWith('data: '))
            .map((l) => l.slice(6))
            .join('\n')
          if (event !== 'activity' || !data) continue
          const p = JSON.parse(data) as { sessionId: string; type: string; op?: string; path?: string; tool?: string }
          if (p.type === 'file' && p.op && p.path) into.push({ sessionId: p.sessionId, op: p.op, path: p.path, tool: p.tool })
        }
      }
    } catch {
      /* closed */
    }
  })()
  return { close: () => ctl.abort() }
}

/** A fresh git repository neither agent has seen, under the live root beside the trusted one. */
function untrustedRepo(root: string): string {
  const dir = mkdtempSync(join(root, 'untrusted-'))
  scratchRepo(dir)
  return dir
}

/** Takes the trust the agents saved for dir off again: Codex's config.toml block, Claude Code's project entry. */
function forgetTrust(dir: string) {
  const toml = join(homedir(), '.codex', 'config.toml')
  if (existsSync(toml)) {
    const text = readFileSync(toml, 'utf8')
    const block = new RegExp(`\\n?\\[projects\\.${JSON.stringify(dir).replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\]\\n(?:[^\\[\\n][^\\n]*\\n?)*`, 'g')
    const next = text.replace(block, '\n')
    if (next !== text) writeFileSync(toml, next)
  }
  const claude = join(homedir(), '.claude.json')
  if (existsSync(claude)) {
    try {
      const j = JSON.parse(readFileSync(claude, 'utf8')) as { projects?: Record<string, unknown> }
      if (j.projects && dir in j.projects) {
        delete j.projects[dir]
        writeFileSync(claude, JSON.stringify(j, null, 2))
      }
    } catch {
      /* left as it is */
    }
  }
}

const test = base.extend<object, { server: Started & { root: string; untrusted: string } }>({
  server: [
    // eslint-disable-next-line no-empty-pattern
    async ({}, use) => {
      const root = mkdtempSync(join(tmpdir(), 'conductor-e2e-live-'))
      const config = join(root, 'conductor.json')
      renderConfig(join(here, 'conductor.e2e-live.json'), config)
      mkdirSync(join(root, 'data'))
      const liveRoot = dirname(repo)
      const untrusted = untrustedRepo(liveRoot)
      const started = await startServer({
        config,
        home: homedir(),
        data: join(root, 'data'),
        allowedRoot: `${repo},${untrusted}`,
        defaultCwd: repo,
        log: join(root, 'server.log'),
        realAgents: true,
        passEnv: ['USER', 'LOGNAME', 'SHELL', 'TMPDIR', 'XDG_RUNTIME_DIR', 'XDG_CONFIG_HOME', 'XDG_DATA_HOME', 'XDG_STATE_HOME', 'CODEX_HOME', 'ANTHROPIC_API_KEY', 'OPENAI_API_KEY'],
        env: SESSION_ENV,
      })
      try {
        await use({ ...started, root, untrusted })
      } finally {
        await stopServer(started.pid)
        forgetTrust(untrusted)
        if (process.env.CONDUCTOR_E2E_KEEP !== '1') {
          rmSync(root, { recursive: true, force: true })
          rmSync(untrusted, { recursive: true, force: true })
        }
      }
    },
    { scope: 'worker' },
  ],
})

test.skip(!live, 'set CONDUCTOR_E2E_LIVE=1 and CONDUCTOR_E2E_LIVE_REPO to an already-trusted git repository')

/** The workbench token where the workbench keeps it, so a page can type into a session. */
async function signIn(page: Page, server: Started) {
  await page.addInitScript((token) => {
    try {
      localStorage.setItem('conductor.workbenchToken', token)
    } catch {
      /* the page asks */
    }
  }, server.token)
}

async function pressInTerminal(page: Page, server: Started, sessionId: string, keys: string[]) {
  await page.goto(`${server.baseURL}/sessions/${encodeURIComponent(sessionId)}`)
  const screen = page.locator('.terminal-host .xterm-screen').first()
  await expect(screen).toBeVisible()
  await screen.click()
  for (const key of keys) {
    await page.keyboard.press(key)
    await page.waitForTimeout(300)
  }
}

/** How long a startup question must stay on a member's screen before its row fails: one a row has just answered is gone sooner. */
const HELD_MS = 5_000

/**
 * The words a first run, a sign-in, a hub, a refusal of the key or a tool at work is known by. A row that got no answer says which
 * of them its member's screen holds, and nothing else of the screen: what a screen says beside them can name an account.
 */
const KNOWN_WORDS = [
  'Choose the text style', 'Select login method', 'Detected a custom API key', 'Do you want to use this API key', 'project you created or one you trust',
  'Quick safety check', 'Welcome to Claude Code', 'Invalid API key', 'Credit balance', 'Please run /login', 'API Usage Billing',
  'Sign in with ChatGPT', 'Sign in with Device Code', 'Use an OpenAI API key', 'Sign in another way', 'Welcome to Codex', 'Agent Command Center', 'Folder access',
  'Trust this folder', 'Do you trust', 'Update available', 'Update now', 'Hooks need review', 'incompatible feature settings', 'Try new model', 'bubblewrap', 'bwrap',
  'Incorrect API key', '401 Unauthorized', 'status 401', 'status 403', '429 Too Many', 'insufficient_quota', 'exceeded your current quota', 'rate limit',
  'does not exist', 'not supported', 'not have access', 'must be verified', 'stream error', 'stream disconnected', 'Reconnecting', 'retrying', 'unexpected status',
  'usage limit', 'billing', 'interrupted', 'esc to interrupt', 'Working', 'Worked for', 'tokens used', 'context left', 'Reply with the single word',
]

/** The test's page, for a row that fails to say what its member's screen holds (screenHolds). */
let look: ((sessionId: string, want: string) => Promise<string[]>) | undefined

/**
 * Which of KNOWN_WORDS the session's screen holds: the page opens the session, the terminal's bytes are read off the page's own
 * socket as they arrive (the replay a viewer is sent, then what follows) until a second passes with none, and the words are looked
 * for with the escapes and the spaces taken out (a full-screen program moves the cursor where a line has a space).
 */
async function screenHolds(page: Page, server: Started, sessionId: string, want: string): Promise<string[]> {
  let bytes = ''
  let last = Date.now()
  page.on('websocket', (ws) => {
    if (!ws.url().includes(`/ws/sessions/${sessionId}`)) return
    ws.on('framereceived', (f) => {
      const b = typeof f.payload === 'string' ? Buffer.from(f.payload) : f.payload
      // The protocol's frames: a type byte, then the payload; 0x01 is output, 0x04 the scrollback a new viewer is sent.
      if (b[0] !== 0x01 && b[0] !== 0x04) return
      bytes += b.subarray(1).toString('utf8')
      last = Date.now()
    })
  })
  await signIn(page, server)
  await page.goto(`${server.baseURL}/sessions/${encodeURIComponent(sessionId)}`)
  await expect(page.locator('.terminal-host .xterm-screen').first()).toBeVisible()
  const until = Date.now() + 15_000
  while (Date.now() < until && (bytes === '' || Date.now() - last < 1_000)) await new Promise((done) => setTimeout(done, 200))
  // eslint-disable-next-line no-control-regex
  const plain = bytes.replace(/\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)/g, '').replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, '').replace(/\x1b[@-Z\\-_]/g, '').replace(/\s+/g, '').toLowerCase()
  const words = KNOWN_WORDS.filter((w) => plain.includes(w.replace(/\s+/g, '').toLowerCase()))
  // What the program itself signalled, as counts and its own state words (a notification's text is the agent's and is not printed):
  // the status sequence Codex sends (OSC 7501, "state=..."), the notifications (OSC 9) and the bells outside any sequence.
  const states: string[] = []
  // eslint-disable-next-line no-control-regex
  for (const m of bytes.matchAll(/\x1b\]7501;([^\x07\x1b]*)(?:\x07|\x1b\\)/g)) {
    const s = /state=([a-z_-]+)/.exec(m[1] ?? '')?.[1] ?? '?'
    if (states[states.length - 1] !== s) states.push(s)
  }
  // eslint-disable-next-line no-control-regex
  const notes = [...bytes.matchAll(/\x1b\]9;[^\x07\x1b]*(?:\x07|\x1b\\)/g)].length
  // eslint-disable-next-line no-control-regex
  const bells = (bytes.replace(/\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)/g, '').match(/\x07/g) ?? []).length
  const times = plain.split(want.replace(/\s+/g, '').toLowerCase()).length - 1
  return [...words, `"${want}" ${times} time(s)`, `program states: ${states.join(' > ') || 'none'}`, `notifications: ${notes}`, `bells: ${bells}`, `${bytes.length} bytes read`]
}

/**
 * answerOrExit waits for the member's answer. It fails at once, in one line, when the member ended before answering, and when its
 * session has been held for HELD_MS by a startup question the catalog knows (a first-run, sign-in or trust screen: the agent is not
 * ready, and no wait changes that). A value handed to expect.poll never ends its wait early, so this is a loop with a deadline.
 */
async function answerOrExit(api: Api, runId: string, read: () => Promise<string>, want: string, timeout: number) {
  const until = Date.now() + timeout
  let held: { words: string; since: number } | undefined
  let last = ''
  for (;;) {
    const m = member(await api.run(runId), 'solo')
    if (m.status === 'ended') throw new Error(`the agent ended before answering (${m.error || 'it exited'}): is it signed in? run it once by hand in the repository`)
    const asks = m.sessionId ? (await api.session(m.sessionId)).attention : undefined
    if (asks?.state === 'needs_input' && asks.source === 'trust') {
      const words = asks.message ?? ''
      if (held?.words !== words) held = { words, since: Date.now() }
      else if (Date.now() - held.since >= HELD_MS) throw new Error(`the agent is not ready: its screen asks "${words}", which nobody here answers`)
    } else held = undefined
    last = await read()
    if (last.includes(want)) return
    if (Date.now() >= until) {
      // Conductor's own lines about the member, and which known words its screen holds: never the screen itself.
      const log = (await api.run(runId)).log.slice(-5).map((e) => e.message ?? e.type).join(' | ')
      const holds = m.sessionId && look ? await look(m.sessionId, want).catch(() => ['(its screen could not be read)']) : []
      const s = m.sessionId ? await api.session(m.sessionId) : undefined
      throw new Error(
        `no "${want}" from the agent in ${timeout / 1000} s; its last words: ${JSON.stringify(last.slice(0, 200))}; the run's log ends: ${log}; ` +
          `the session: attention ${s?.attention?.state ?? 'none'}/${s?.attention?.source ?? '-'}, reports its turns: ${s?.turns ?? false}; ` +
          `of the words this suite knows a screen by, its screen holds: ${holds.join(', ') || 'none'}`,
      )
    }
    await new Promise((done) => setTimeout(done, 1_000))
  }
}

function cleanup(api: Api, run: Run, repoDir: string) {
  return (async () => {
    await api.stopRun(run.id)
    const solo = member(await api.run(run.id), 'solo')
    if (solo.branch) {
      try {
        git(repoDir, 'worktree', 'remove', '--force', join(repoDir, '.conductor', 'worktrees', run.id, 'solo'))
        git(repoDir, 'branch', '-D', solo.branch)
      } catch {
        /* left for a person to remove: the run's id names them */
      }
    }
  })()
}

for (const agent of ['claude', 'codex']) {
  test.describe(agent, () => {
    test.skip(!onPath(agent), `${agent} is not on the PATH`)
    // In CI nobody is signed in: an agent runs on its key or not at all. On a person's machine its own login may stand in.
    test.skip(!!process.env.CI && !process.env[KEY[agent]!], `${agent}: this run holds no ${KEY[agent]}`)
    test.beforeEach(({ page, server }) => {
      look = (sessionId, want) => screenHolds(page, server, sessionId, want)
    })

    const crewBody = (name: string, cwd: string, prompt: string, yolo: boolean | null, isolation: 'none' | 'worktree') => ({
      name: `Live ${agent} ${name}`,
      goal: '',
      cwd,
      where: 'server',
      isolation,
      openAfterLaunch: false,
      yolo,
      members: [{ name: 'solo', agentId: agent, prompt, start: { when: 'immediately' } }],
    })

    const answerOf = async (api: Api, runId: string) => {
      const id = member(await api.run(runId), 'solo').sessionId
      return id ? ((await api.session(id)).attention?.message ?? '') : ''
    }

    test('answers a typed prompt without a person pressing Enter', async ({ server }) => {
      test.setTimeout(180_000)
      const api = new Api(server)
      const crew = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', crewBody('prompt', repo, PROMPT, null, 'worktree'))
      const run: Run = await api.launchCrew(crew.crew.id)
      try {
        let sawNeedsInput = false
        await answerOrExit(
          api,
          run.id,
          async () => {
            const r = await api.run(run.id)
            if (member(r, 'solo').needsInput) sawNeedsInput = true
            return answerOf(api, run.id)
          },
          'READY',
          150_000,
        )
        const r = await api.run(run.id)
        expect(logged(r, "typed solo's prompt")).toBe(true)
        if (agent === 'codex') expect(sawNeedsInput, "Codex's title thread raises no false needs_input").toBe(false)
      } finally {
        await cleanup(api, run, repo)
        await api.call('DELETE', `/api/crews/${encodeURIComponent(crew.crew.id)}`)
      }
    })

    test('reports the files it reads and writes through its hooks', async ({ server }) => {
      // Codex reports files through hooks a person installs once (`conductor hooks install codex`) and then reviews in Codex itself
      // ("Hooks need review"): a machine nobody has used has neither, and no setup answers that review for a person.
      test.skip(agent === 'codex' && !!process.env.CI, "Codex's hooks are installed and reviewed by a person: a fresh runner has none")
      test.setTimeout(240_000)
      const api = new Api(server)
      const seen: FileEvent[] = []
      const stream = watchFileEvents(server, seen) // open before the launch: nothing is replayed
      const crew = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', crewBody('files', repo, FILES_PROMPT, true, 'worktree'))
      const run: Run = await api.launchCrew(crew.crew.id)
      try {
        await answerOrExit(api, run.id, () => answerOf(api, run.id), 'TOUCHED', 200_000)
        const sid = member(await api.run(run.id), 'solo').sessionId ?? ''
        const mine = () => seen.filter((e) => e.sessionId === sid)
        await expect
          .poll(() => mine().filter((e) => (e.op === 'write' || e.op === 'edit') && e.path.endsWith('touched.txt')).length, { timeout: 30_000, intervals: [1_000] })
          .toBeGreaterThan(0)
        // Claude Code's Read is a tool call its hooks report; Codex reads with the shell, which names no file.
        if (agent === 'claude') expect(mine().some((e) => e.op === 'read' && e.path.endsWith('touched.txt')), JSON.stringify(mine())).toBe(true)
        for (const e of mine()) expect(e.tool, JSON.stringify(e)).toBeTruthy()
      } finally {
        stream.close()
        await cleanup(api, run, repo)
        await api.call('DELETE', `/api/crews/${encodeURIComponent(crew.crew.id)}`)
      }
    })

    test('is held by the trust question in a repository it has not seen, and Enter in its terminal answers it', async ({ page, server }) => {
      test.setTimeout(240_000)
      const api = new Api(server)
      await signIn(page, server)
      const crew = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', crewBody('trust', server.untrusted, PROMPT, false, 'none'))
      const run: Run = await api.launchCrew(crew.crew.id)
      try {
        let solo: Session | undefined
        await expect
          .poll(async () => {
            const m = member(await api.run(run.id), 'solo')
            if (!m.sessionId) return ''
            solo = await api.session(m.sessionId)
            return solo.attention?.source === 'trust' ? solo.attention.state : solo.attention?.state ?? ''
          }, { timeout: 90_000, intervals: [500], message: 'the trust question holds the member' })
          .toBe('needs_input')
        expect(solo?.attention?.message ?? '').toMatch(TRUST_WORDS[agent]!)
        // The session's detector holds the member first; the engine's readiness poll notes the question in the run log a moment later.
        let r = await api.run(run.id)
        await expect
          .poll(async () => {
            r = await api.run(run.id)
            return logged(r, 'solo asks')
          }, { timeout: 15_000, intervals: [250], message: 'the run log notes the question' })
          .toBe(true)
        expect(logged(r, "typed solo's prompt")).toBe(false)
        // The trusting answer (TRUST_YES): verify with each release which one is highlighted.
        await pressInTerminal(page, server, solo!.id, TRUST_YES[agent]!)
        await expect
          .poll(async () => {
            r = await api.run(run.id)
            const m = member(r, 'solo')
            if (m.status === 'ended') return 'ended'
            return logged(r, "typed solo's prompt") ? 'typed' : 'held'
          }, { timeout: 60_000, intervals: [500] })
          .not.toBe('held')
        expect(member(r, 'solo').status, `${TRUST_YES[agent]!.join('+')} must trust, not exit`).not.toBe('ended')
        await answerOrExit(api, run.id, () => answerOf(api, run.id), 'READY', 150_000)
      } finally {
        await cleanup(api, run, server.untrusted)
        await api.call('DELETE', `/api/crews/${encodeURIComponent(crew.crew.id)}`)
      }
    })

    test('with yolo on, Codex asks nothing and saves nothing; Claude Code still asks', async ({ server }) => {
      test.setTimeout(240_000)
      forgetTrust(server.untrusted)
      const api = new Api(server)
      const crew = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', crewBody('yolo', server.untrusted, PROMPT, true, 'none'))
      const run: Run = await api.launchCrew(crew.crew.id)
      try {
        const toml = join(homedir(), '.codex', 'config.toml')
        const before = existsSync(toml) ? readFileSync(toml, 'utf8') : ''
        if (agent === 'codex') {
          let sawTrust = false
          await expect
            .poll(async () => {
              const r = await api.run(run.id)
              const m = member(r, 'solo')
              if (m.sessionId && (await api.session(m.sessionId)).attention?.source === 'trust') sawTrust = true
              return logged(r, "typed solo's prompt")
            }, { timeout: 60_000, intervals: [500] })
            .toBe(true)
          expect(sawTrust, 'the yolo trust override makes the question not appear').toBe(false)
          const solo = await api.session(member(await api.run(run.id), 'solo').sessionId ?? '')
          expect(solo.command).toContain('--dangerously-bypass-approvals-and-sandbox')
          expect((solo.command ?? []).some((a) => a.startsWith('projects={') && a.includes('trust_level="trusted"'))).toBe(true)
          await answerOrExit(api, run.id, () => answerOf(api, run.id), 'READY', 150_000)
          const after = existsSync(toml) ? readFileSync(toml, 'utf8') : ''
          expect(after.includes(server.untrusted), 'config.toml gains no trust entry').toBe(false)
          expect(after === before || !after.includes(server.untrusted)).toBe(true)
        } else {
          // The trust question itself: every startup screen the catalog knows raises the same source, a first-run one too.
          await expect
            .poll(async () => {
              const m = member(await api.run(run.id), 'solo')
              const asks = m.sessionId ? (await api.session(m.sessionId)).attention : undefined
              return asks?.source === 'trust' ? (asks.message ?? '') : ''
            }, { timeout: 90_000, intervals: [500] })
            .toMatch(TRUST_WORDS[agent]!)
        }
      } finally {
        await cleanup(api, run, server.untrusted)
        forgetTrust(server.untrusted)
        await api.call('DELETE', `/api/crews/${encodeURIComponent(crew.crew.id)}`)
      }
    })

    test('resumes its conversation after its session ended', async ({ server }) => {
      test.setTimeout(300_000)
      const api = new Api(server)
      const crew = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', crewBody('resume', repo, 'Remember the word PINEAPPLE. Reply with the single word OK and nothing else', null, 'worktree'))
      const run: Run = await api.launchCrew(crew.crew.id)
      try {
        await answerOrExit(api, run.id, () => answerOf(api, run.id), 'OK', 150_000)
        const first = member(await api.run(run.id), 'solo')
        await expect.poll(async () => (await api.session(first.sessionId ?? '')).agentSession?.resumable, { timeout: 30_000 }).toBe(true)
        await api.stopSession(first.sessionId ?? '')
        await expect.poll(async () => member(await api.run(run.id), 'solo').status, { timeout: 30_000 }).toBe('ended')
        const r = await api.resumeMember(run.id, 'solo')
        expect(r.status, JSON.stringify(r.body)).toBe(201)
        await expect.poll(async () => member(await api.run(run.id), 'solo').status, { timeout: 60_000 }).toBe('running')
        const next = member(await api.run(run.id), 'solo')
        expect(next.sessionId).not.toBe(first.sessionId)
        expect(next.branch).toBe(first.branch)
        const resumed = await api.session(next.sessionId ?? '')
        expect(resumed.agentSession?.source).toBe('resumed')
        // A resumed member is running at once, but its agent takes a few seconds to show its prompt and no hook says when
        // (docs/features.md, deferred: a readiness wait for resumed members); a broadcast typed into it meanwhile is lost.
        await new Promise((f) => setTimeout(f, 10_000))
        await api.ok('POST', `/api/runs/${encodeURIComponent(run.id)}/broadcast`, { text: 'Which word did I ask you to remember? Reply with that word only' })
        await answerOrExit(api, run.id, () => answerOf(api, run.id), 'PINEAPPLE', 150_000)
      } finally {
        await cleanup(api, run, repo)
        await api.call('DELETE', `/api/crews/${encodeURIComponent(crew.crew.id)}`)
      }
    })
  })
}
