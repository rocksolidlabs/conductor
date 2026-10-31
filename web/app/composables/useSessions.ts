import type { ChatMessage, ActivityEntry, Attention, AttentionKind, AttentionOption, Role } from '~/utils/protocol'
import type { Receipt } from '~/utils/tasks'
import { toCrewInput, toCrewMember } from '~/utils/crews'
import type { TouchedReply } from '~/utils/touchedIndex'

export type SessionKind = 'server' | 'hosted'
export type SessionStatus = 'starting' | 'running' | 'exited' | 'stopped' | 'host_disconnected'

export interface SessionInfo {
  id: string
  name: string
  kind: SessionKind
  agentId: string
  command: string[]
  cwd: string
  status: SessionStatus
  exitCode?: number
  cols: number
  rows: number
  viewers: number
  hostName?: string
  /** OS user running `conductor host` (hosted sessions). */
  hostUser?: string
  /** Git branch of the working directory when known. */
  branch?: string
  /** The crew run the session is a member of; missing for a session launched on its own. */
  crew?: { runId: string; crewId: string; member: string }
  attention?: Attention
  /** Who last answered a needs-input prompt (server-reported). */
  lastAnswer?: { by?: string; byName: string; at: string; message?: string }
  createdAt: string
  endedAt?: string
  /** Launched with its agent's yolo recipe applied: the agent skips its permission prompts. Missing when no recipe was applied. */
  yolo?: boolean
  /** The agent's own session (its conversation), when its agent has a session recipe: what Resume resumes. */
  agentSession?: AgentSession
  /** The session this one resumed or relaunched. */
  resumedFrom?: string
}

/** An agent's own session as Conductor knows it: the id it chose at launch (`set`), the one the agent's hooks reported (`hook`), or the one a resume took up (`resumed`). */
export interface AgentSession {
  id: string
  /** The agent has had a turn: there is a conversation to resume. Before one, Resume relaunches plainly. */
  resumable: boolean
  source: 'set' | 'hook' | 'resumed'
}

/** What a launch with yolo on adds to the agent's: arguments after its command and the launch's own, variables over its own. Not secret: shown as they are. */
export interface YoloRecipe {
  args?: string[]
  env?: Record<string, string>
}

/** How Conductor names an agent's own session and resumes it (catalog.SessionRecipe); `{id}` stands for the id, a whole argument. */
export interface SessionRecipe {
  startArgs?: string[]
  newId?: 'uuid' | 'name'
  idFrom?: 'hook'
  idPolicy?: 'latest' | 'lowest'
  resumeArgs?: string[]
  idPattern?: string
  resumeNeedsCwd?: boolean
}

/** Reply of the resume routes: the new session, whether it resumed the agent's conversation, and why not when it did not. */
export interface ResumeResult {
  session: SessionInfo
  resumed: boolean
  notice?: string
}

/** How an agent tells Conductor it needs a human. No signal means the bell default. */
export interface AgentSignal {
  kind: 'hook' | 'bell' | 'pattern' | 'none'
  /** RE2 on the last screen line; only with kind 'pattern'. */
  pattern?: string
  /** Ask hook adapters to report tool use as events (chatty; off by default). */
  toolEvents?: boolean
  /** RE2 on the window title the agent writes while it works (Codex's "⠋ Working"): a title it matches shows the session working. Any kind. */
  workingTitle?: string
}

export interface AgentInfo {
  id: string
  name: string
  description?: string
  command: string[]
  allowArgs: boolean
  cwd?: string
  icon?: string
  /** Hook adapter id; empty or missing means none. */
  adapter?: string
  signal?: AgentSignal
  /** Names of server environment variables this agent may inherit. */
  envPassthrough?: string[]
  /** Variable names the server sets for this agent. Every value is masked as "***": it means "set on the server", never the real value. Send it back unchanged to keep the stored value. */
  env?: Record<string, string>
  /** Where the catalog took the agent from: built in, the config file (or catalog file), or saved from the Agents page. */
  source?: 'built-in' | 'config' | 'saved'
  /** For a saved agent that replaces a built-in or configured one: where that one came from. Deleting the saved agent brings it back. */
  replaces?: 'built-in' | 'config'
  /** Whether command[0] resolves on the server (the check of POST /api/catalog/check, cached 30 s). Missing from an older server. */
  available?: boolean
  /** Where command[0] was found when that is a Windows program seen from inside WSL (under /mnt): not installed in the distribution. */
  onWindows?: string
  /** The agent's website, an https URL, when known: a built-in's, or what was saved with the agent. */
  site?: string
  /** Its yolo recipe; `{}` is none. Missing: no recipe. */
  yolo?: YoloRecipe
  /** RE2 matched against its screen's text: its workspace-trust question, while which a crew types no prompt. */
  trustPrompt?: string
  /** That question's answers, as the choices a page offers. */
  trustAnswers?: AgentAnswer[]
  /** Its other startup questions (round 13: Codex's "Hooks need review"), held like the trust question. */
  questions?: AgentQuestion[]
  /** Its session recipe, for Resume. */
  session?: SessionRecipe
  /** `false` turns the server's identity probe off for this agent. */
  probe?: boolean
  /** What the adapter's probe found the program on the server to be; absent for an agent that is not probed. */
  identity?: Identity
}

/** The identity probe's answer: the program run with its version flag and its output matched against what the agent prints. */
export interface Identity {
  /** The probe ran and answered; `pending` says it is still under way (ask again). Neither, with `error`, when it could not run. */
  ran: boolean
  pending?: boolean
  /** The program is the agent (`version` is what it said), or a known other program of the same name. */
  identified: boolean
  impostor?: boolean
  /** The adapter's expectation was checked against the real CLI: then a program that matches nothing is not the agent. */
  verified: boolean
  /** The agent's name, as its adapter gives it. */
  name?: string
  version?: string
  /** The first line the program printed, when it was not identified. */
  output?: string
  error?: string
}

/** Body of POST /api/catalog: the whole agent, replacing any agent with the same id. */
export interface AgentInput {
  id: string
  name: string
  description?: string
  command: string[]
  allowArgs: boolean
  /**
   * A value of "***", as read from the catalog, means "unchanged": the value the saved agent holds for that key or, for an agent that
   * replaces a built-in or configured one, the replaced agent's value, which then follows the config. The server rejects it for a key
   * neither has.
   */
  env?: Record<string, string>
  envPassthrough?: string[]
  cwd?: string
  icon?: string
  /** An https:// URL with a host, at most 200 bytes; left out, an agent that replaces a built-in keeps the built-in's. */
  site?: string
  adapter?: string
  signal?: AgentSignal
  /** Left out, an agent that replaces a built-in keeps the built-in's; `{}` has none. */
  yolo?: YoloRecipe
  trustPrompt?: string
  trustAnswers?: AgentAnswer[]
  questions?: AgentQuestion[]
  session?: SessionRecipe
  /** `false` turns the identity probe off; left out, a saved agent of its own is probed (a copy keeps its source's; #291). */
  probe?: boolean
}

/** One answer to an agent's startup question: the label a page shows and the keys that pick it. */
export interface AgentAnswer {
  label: string
  input: string
}

/** One of an agent's startup questions: the RE2 of its words on the screen and its answers. */
export interface AgentQuestion {
  prompt: string
  /** RE2 picking the words a page shows out of the same screen (its groups joined); without it, what `prompt` matched. */
  words?: string
  /** Asked at any time in the session's life (Codex's command approval), not only as it starts: held with source `question`. */
  anytime?: boolean
  answers?: AgentAnswer[]
  /** Its answers read off the screen each time it is asked (Claude Code's dialogs, #288): its dialog's numbered list, in place of `answers`. */
  answersFromScreen?: boolean
  /** RE2 matching the labels of its answers that change the session's permission mode: no number key, never offered first. */
  modeAnswers?: string
}

/** Body of POST /api/sessions/{id}/events: something the agent did, or an attention word. */
export interface EventInput {
  type: 'progress' | 'artifact' | 'handoff' | 'tool_use' | 'tool_denied' | 'error' | 'needs_input' | 'working' | 'done' | 'clear'
  message?: string
  /** `artifact`: where the result lives (≤ 2048 bytes). */
  url?: string
  /** `handoff`: who the work goes to (≤ 40 characters). */
  to?: string
  /** `tool_use`, `tool_denied`, `error`: the tool involved (≤ 100 bytes). */
  tool?: string
  /** With the attention words: the shape of the prompt and its quick replies. */
  kind?: AttentionKind
  options?: AttentionOption[]
}

/** One hook adapter as GET /api/integrations lists it, checked against the home of the user running the server. */
export interface Integration {
  id: string
  name: string
  /** What the adapter can report, such as needs_input, done or tool_use. */
  events: string[]
  /** A launch from this server wires the hooks in (flags or environment). */
  launchInjection: boolean
  /** The adapter has a file Conductor can install its hooks into. */
  installable: boolean
  /** The agent reads skills: installing its hooks also puts the Conductor skill in its skills directory. */
  installsSkill: boolean
  /** The hooks are in place as an install would leave them: false after an upgrade moved the binary, until installed again. */
  installed: boolean
  /** The install's main file; empty when there is no file to install into. */
  where: string
  /** What to paste to wire the agent by hand. */
  snippet: string
  /** The agent's hook interface is still changing (a developer preview). */
  experimental: boolean
}

/** A webhook from conductor.json, as GET /api/integrations lists it: never its secret, user info or query string. */
export interface WebhookInfo {
  /** scheme://host[:port]/path */
  url: string
  /** The event types the server POSTs to it. */
  events: string[]
}

/** GET /api/integrations: every hook adapter, the machine the server runs on, and the webhooks it sends events to. */
export interface Integrations {
  integrations: Integration[]
  /** The server's host name, where Install on this machine writes; empty when the server cannot tell. */
  host: string
  webhooks: WebhookInfo[]
}

export interface ShareLink {
  id: string
  /** The session a session link opens; empty for a run link. */
  sessionId: string
  /** The crew run a run link opens: its role on the session of every member of the run, a member added later included. */
  runId?: string
  label?: string
  role: Role
  createdAt: string
  expiresAt?: string
  revoked: boolean
  /** Viewers currently attached through this link (server-reported). */
  active?: number
  /** Minted at the switchyard the session is published to: listed and revoked from here, viewers counted there. */
  remote?: boolean
}

/** A member of a run as the join page sees it. */
export interface JoinRunMember {
  name: string
  /** hosted for a member of a run shared through a switchyard: its session is reached over WebRTC. */
  kind?: SessionKind
  /** Its session, only while that runs; `agentId` and `status` are then the session's. */
  sessionId?: string
  agentId: string
  /** Without a session: its state in the run (`pending`, `starting` until its session exists, `ended`). */
  status: 'pending' | 'starting' | 'running' | 'ended'
}

export interface JoinInfo {
  /** The session a session link opens; missing for a run link, whose reply has `run` in its place. */
  session?: Pick<SessionInfo, 'id' | 'name' | 'agentId' | 'kind' | 'status' | 'cols' | 'rows' | 'hostName' | 'hostUser'> & { cwd?: string }
  /** The run a run link opens, with every member in the run's order. */
  run?: { id: string; name: string; members: JoinRunMember[] }
  role: Role
  label?: string
  /** Whether the server answering is a switchyard (a coordinator that launches nothing), which the join page names. */
  switchyard?: boolean
}

/**
 * Why a broadcast skipped a member: waiting on a prompt, not running (no session yet, its prompt not typed yet, or ended), not a member, or
 * typed without its Enter (a prompt came up during the pause before it; the line waits in the member's input).
 */
export type BroadcastSkipReason = 'needs_input' | 'not_running' | 'unknown' | 'no_enter'

/** Reply of POST /api/runs/{run}/broadcast, both lists in the order asked. */
export interface BroadcastResult {
  sent: string[]
  skipped: { member: string; reason: BroadcastSkipReason }[]
}

/** When a crew member starts in a run. */
export interface CrewStart {
  /** `task`: in a crew with tasks, the run starts the member for each of its tasks as their needs are met; required there, refused elsewhere. */
  when: 'immediately' | 'after' | 'manual' | 'task'
  /** Only with `after`: the member whose first `done` starts this one. Following these never goes round in a cycle. */
  member?: string
}

/** One agent of a crew. */
export interface CrewMember {
  /** `^[a-z0-9][a-z0-9._-]{0,39}$` without `..` or a `.` or `.lock` at the end, unique in the crew: it becomes a branch and a worktree name. */
  name: string
  /** A catalog agent id (`^[a-z0-9-]{1,32}$`) the catalog has when the crew is saved. */
  agentId: string
  /** Typed once the member is ready; `$GOAL` and `${GOAL}` stand for the crew's goal. At most 4000 characters. */
  prompt: string
  /** At most 32, of at most 4096 bytes each and 8 KiB in all. */
  args?: string[]
  start: CrewStart
}

/**
 * A command the controller runs (#334): against a task's result (`CrewTask.checks`), or a step of a project manifest (`CrewManifest`, whose setup
 * phase `CrewInfo.setup` was). An argv array, never a shell line.
 */
export interface CrewCheck {
  /** `^[a-z0-9][a-z0-9_-]{0,39}$`, unique among a task's checks; a manifest's step's unique across the manifest and none of the tasks' check ids. */
  id: string
  /** The program and its arguments: at most 32 of 4096 bytes, 8 KiB in all. */
  argv: string[]
  /** 1 to 3600; 600 when missing. */
  timeoutSeconds?: number
}

/**
 * A project manifest's readiness probe: a command run until it exits 0, or a URL on this machine fetched until it answers under 300, every
 * `intervalSeconds`, for at most `timeoutSeconds`. It has `argv` or `url`, never both.
 */
export interface CrewProbe {
  /** As a check's: unique across the manifest. */
  id: string
  argv?: string[]
  /** http or https on 127.0.0.1, localhost or [::1], at most 2048 bytes; `${CONDUCTOR_PORT_<NAME>}` and `${CONDUCTOR_STATE_DIR}` expand in it. */
  url?: string
  /** 1 to 3600; 60 when missing. */
  timeoutSeconds?: number
  /** 1 to 60; 2 when missing. */
  intervalSeconds?: number
}

/**
 * A project manifest (docs/plans/project-manifests.md): what makes a fresh tree of the project (an attempt's worktree, or the checkout of its
 * result) usable, how it is checked and how it is cleaned up. Not an agent's recipes (the catalog's yolo, trust and session recipes).
 * `${CONDUCTOR_PORT_<NAME>}` and `${CONDUCTOR_STATE_DIR}` expand inside one argv element, a probe's URL and the preview. Every phase
 * runs: the prerequisites once per run, then in each tree setup, services, readiness, (in a result's checkout) verify, and cleanup at its end.
 */
export interface CrewManifest {
  /** Only in conductor.manifest.json (1); never in a crew's. */
  schema?: number
  /** Read conductor.manifest.json from the crew's directory at each tree's commit; nothing else is set then. */
  file?: boolean
  /** At most 8, run once per run before any tree, with no placeholders; 60 s each when no timeout is given. */
  prerequisites?: CrewCheck[]
  /** At most 32 names of the host's variables (`^[A-Za-z_][A-Za-z0-9_]{0,127}$`, never CONDUCTOR_*, never a loader); the values stay the host's. */
  env?: string[]
  /** At most 8 names (`^[a-z][a-z0-9_]{0,15}$`, unique), each `CONDUCTOR_PORT_<NAME>` in a tree. */
  ports?: string[]
  /** At most 8 steps that make a fresh tree usable; 600 s each by default. */
  setup?: CrewCheck[]
  /** At most 4 commands that run beside the tree as long as it lives, with no timeout. */
  services?: CrewCheck[]
  /** At most 8. */
  ready?: CrewProbe[]
  /** The URL a person opens: as a probe's, at most 512 bytes. */
  preview?: string
  /** At most 8 steps run after the task's checks in the checkout of a result; 600 s each by default. */
  verify?: CrewCheck[]
  /** At most 8 steps run once a tree ends, however it ends; 60 s each by default. */
  cleanup?: CrewCheck[]
}

/** A task of a managed crew (#334): a member does it once the tasks it needs have results, in a worktree of its own, and submits a result the checks decide. */
export interface CrewTask {
  /** `^[a-z0-9][a-z0-9_-]{0,39}$`, unique in the crew: an attempt's branch is `crew/<run>/<task>.<n>`. */
  id: string
  /** The member who does it. */
  member: string
  /** `work` submits a commit; `review` reviews one work task's result and submits a verdict, with no checks of its own. */
  kind: 'work' | 'review'
  /** At most 8000 characters; written into the attempt's task file, never typed. */
  instructions: string
  /** The tasks whose results it starts from, with no cycle; a review's one work task. */
  needs?: string[]
  /** At most 8, none for a review. */
  checks?: CrewCheck[]
}

/** A saved crew in full, as GET /api/crews/{id} answers it and the crew routes reply. */
export interface CrewInfo {
  /** Set by the server from the name when the crew is created; never changes. */
  id: string
  /** At most 60 characters, no control characters; the server trims surrounding space. */
  name: string
  /** At most 2000 characters. */
  goal: string
  cwd: string
  where: 'server' | 'host'
  /** `worktree`: every member gets a git worktree of `cwd`. */
  isolation: 'none' | 'worktree'
  openAfterLaunch: boolean
  /** Lifetime of the view link a launch creates; none when missing. */
  viewLinkTtlSeconds?: number
  /** Overrides the server's yolo default for the crew's runs: missing follows it. A launch fixes the choice on the run. */
  yolo?: boolean
  /** At most 12. The whole crew is at most 1 MiB as its file. */
  members: CrewMember[]
  /** At most 128; present, the crew is managed (#334): every member starts by its tasks and `isolation` is `worktree`. */
  tasks?: CrewTask[]
  /** At most 8 commands run in every new attempt worktree before its member starts; only with tasks. The project manifest's setup phase as a crew saved before manifests holds it: never beside `manifest`. */
  setup?: CrewCheck[]
  /** The crew's project manifest; only with tasks. The whole manifest is at most 64 KiB as JSON. */
  manifest?: CrewManifest
  /** The server's count of the crew's manifest changes (`crew:<revision>` in a receipt): never sent back. */
  manifestRevision?: number
  createdAt: string
  updatedAt: string
}

/** A crew as GET /api/crews lists it: what the list shows, without the prompts. GET /api/crews/{id} answers it in full. */
export interface CrewSummary {
  id: string
  name: string
  goal: string
  cwd: string
  where: 'server' | 'host'
  isolation: 'none' | 'worktree'
  members: Array<{ name: string; agentId: string; start: CrewStart }>
  updatedAt: string
}

/** Body of POST /api/crews and PUT /api/crews/{id}: a crew without what the server sets. The server rejects unknown fields, these four included. */
export type CrewInput = Omit<CrewInfo, 'id' | 'createdAt' | 'updatedAt' | 'manifestRevision'>

/** A member of a crew run, as the run routes report it. */
export interface RunMember {
  name: string
  agentId: string
  start: CrewStart
  /** The member's session, once it has started. */
  sessionId?: string
  /** With worktree isolation, once it has started: its branch (`crew/<run>/<member>`) and worktree. */
  branch?: string
  worktree?: string
  /** `starting` once its session exists, until its prompt is typed; `running` after. */
  status: 'pending' | 'starting' | 'running' | 'ended'
  startedAt?: string
  endedAt?: string
  /** Why it ended before it ran: it could not start, or its process ended before its prompt was typed. */
  error?: string
  /** Starting or running, and its session waits on a prompt (a trust question before its prompt, or its agent's question), as last read. */
  needsInput?: boolean
  /** The agent session of its latest session, kept once that session has left the server: what its Resume resumes. */
  agentSession?: AgentSession
  /** In a managed run: the task and attempt its latest session is (#334). */
  task?: string
  attempt?: number
  /**
   * GET /api/runs/{run} only: lines of tracked files its worktree adds and removes against the commit it began from,
   * committed or not (`git diff --shortstat <base>`; untracked files do not count, a branch merged into its own does).
   * Read at most every 10 s; the last value stays when a read fails.
   */
  diff?: { added: number; removed: number }
}

/** A task of a managed run as the run reports it (#334). */
export interface RunTask {
  id: string
  member: string
  kind: 'work' | 'review'
  needs?: string[]
  /** blocked (its needs have no result yet, or never will: `reason` says), ready, running (an attempt is in flight), verifying (a result is checked), succeeded, failed, cancelled. */
  state: 'blocked' | 'ready' | 'running' | 'verifying' | 'succeeded' | 'failed' | 'cancelled'
  reason?: string
  /** Attempts made so far. */
  attempts: number
  /** The latest attempt. */
  attempt?: RunAttempt
  /** The result that stands, once an attempt succeeded. */
  result?: RunTaskResult
  /** A continuation's result taken by reference from the run it continues, as `<run>/<task>/<n>`. */
  importedFrom?: string
  /** What the attempt in flight says stops it (#334, row 10), until it works on or ends. */
  blocker?: { reason: string; message?: string; by: string; at: string }
}

/** Where an attempt got to, as it saved it: tied to its worktree's commit. */
export interface AttemptCheckpoint {
  done: string
  remaining: string
  notes?: string
  commit: string
  at: string
}

/** One attempt at a task: one member session in one worktree. */
export interface RunAttempt {
  n: number
  sessionId?: string
  worktree?: string
  branch?: string
  /** The commit the worktree was made at, and the result commit once submitted. */
  start?: string
  result?: string
  state: 'reserved' | 'launched' | 'running' | 'submitting' | 'verifying' | 'succeeded' | 'failed' | 'cancelled' | 'stopped' | 'interrupted' | 'uncertain'
  /** What it was doing when it failed or was cut short, and why. */
  stage?: string
  error?: string
  startedAt?: string
  endedAt?: string
  /** What the attempt submitted, in a task's own read (`runTask`); the run's read leaves it out. */
  submitted?: SubmittedResult
  /** The attempt's last checkpoint (#334, row 10). */
  checkpoint?: AttemptCheckpoint
  /** The attempt's receipts (its trees' steps and its checks, by when each started), in a task's own read alone (`runTask`). */
  steps?: RunStep[]
  /**
   * The URL a person opens to look at the attempt's work (EN1): its worktree's preview, from the worktree's `tree.ready` until the first
   * of the attempt's close and one of the worktree's services exiting on its own; absent otherwise, while the run stops, and for a run
   * the server has let go. On the server's loopback, not proxied: it opens in a new tab.
   */
  preview?: string
}

/**
 * A receipt (EN1, project manifests): a step of a manifest's phase in one of an attempt's trees, a task's check, or one of the run's
 * prerequisites, as the ledger keeps it but for its log, whose size and SHA-256 it says (`stepLog`, `prerequisiteLog`).
 */
export interface RunStep {
  phase: 'check' | 'setup' | 'service' | 'ready' | 'verify' | 'cleanup' | 'prerequisite'
  id: string
  /** The k-th run of this id on the attempt (or, for a prerequisite, in the run), whatever its phase. */
  k: number
  state: 'started' | 'running' | 'finished' | 'interrupted'
  argv?: string[]
  /** A readiness probe over HTTP's expanded URL: its command, with no argv. */
  url?: string
  /** Where it ran: the tree's working directory, or the crew's for a prerequisite. */
  dir?: string
  /** The commit it ran at. */
  subject?: string
  /** The manifest that ran it, `crew:<revision>` or `file:<blob>`; absent for a task's check. */
  manifest?: string
  /** Once it ended. */
  exit?: number
  timedOut?: boolean
  /** Milliseconds, once it ended. */
  duration?: number
  /** The words of its failure. */
  error?: string
  ports?: Record<string, number>
  tries: number
  startedAt: string
  finishedAt?: string
  logBytes: number
  /** The log was not kept (the run's logs were at their bound): its route answers 410; the hash stays. */
  logDropped?: boolean
  logSha256?: string
}

/** A prerequisite of a run as it goes (`RunInfo.manifest`): pending, running while its generation runs, passed, failed or interrupted. */
export interface RunPrerequisite {
  id: string
  /** Its latest run's k, absent before its first. */
  k?: number
  state: 'pending' | 'running' | 'passed' | 'failed' | 'interrupted'
  exit?: number
  error?: string
  /** Milliseconds. */
  duration?: number
}

/** A run's project manifest: the crew's at its revision, or `conductor.manifest.json` at the run's base (its blob). */
export interface RunManifest {
  source: 'crew' | 'file'
  revision?: number
  digest?: string
  prerequisites?: RunPrerequisite[]
}

/**
 * A receipt's log as `stepLog` and `prerequisiteLog` read it: its text, its bytes as the server sent them (what Save downloads,
 * so the file matches the hash), and the SHA-256 the server sent beside it.
 */
export interface StepLog {
  text: string
  bytes: Uint8Array
  sha256: string
}

/** A finding of a review. */
export interface Finding {
  severity: 'blocker' | 'major' | 'minor' | 'nit'
  path?: string
  line?: number
  message: string
}

/** What an attempt submitted: a work task's commit, summary and issues, or a review's verdict and findings. */
export interface SubmittedResult {
  summary?: string
  commit?: string
  issues?: string[]
  verdict?: 'approved' | 'needs_changes'
  findings?: Finding[]
}

/** A task's accepted result: a work task's commit and summary, or a review's verdict and findings. */
export interface RunTaskResult {
  attempt: number
  commit?: string
  summary?: string
  issues?: string[]
  verdict?: 'approved' | 'needs_changes'
  findings?: Finding[]
  at: string
}

/** A launch of a crew. Runs live in the server's memory: a restart forgets them. */
export interface RunInfo {
  /** `<crew id>-<8 hex>`. */
  id: string
  crewId: string
  /** The crew's name. */
  name: string
  /** The run's own name, given at launch and kept by a resume; absent when it has none. */
  label?: string
  goal: string
  cwd: string
  isolation: 'none' | 'worktree'
  startedAt: string
  stoppedAt?: string
  members: RunMember[]
  /** The run's own log, oldest first, at most 200 entries: launched, member started, prompt typed, run links created and revoked, stopped. */
  log: ActivityEntry[]
  /**
   * As last read: running while a member is pending, starting or running and none waits; needs_input while a starting or running member's
   * session waits on a prompt (`needsInput` of them); stopped after a stop; finished once every member has ended. The live view is
   * `runState` (utils/runs.ts), which brings it up to date with the sessions.
   */
  state: 'running' | 'needs_input' | 'stopped' | 'finished' | 'interrupted'
  needsInput: number
  /** The run's yolo choice, fixed at launch: every member, one added later included, follows it. */
  yolo: boolean
  /** `GET /api/runs/{run}` and the record only: the run's chat, oldest first, at most 500 messages. */
  chat?: ChatMessage[]
  /** The stopped run this one resumed, and the run that resumed this one (Resume as new run). */
  resumedFrom?: string
  resumedBy?: string
  /** A managed run (a crew with tasks, #334): its revision in the ledger, which every transition bumps. Absent for a legacy run. */
  managed?: { revision: number }
  /** A managed run whose dispatch is paused: attempts in flight go on, none starts. */
  paused?: boolean
  /** Why the run paused itself (a project manifest's step that failed, the ledger), absent for a person's pause. */
  pausedWhy?: string
  /** A managed run's project manifest and how its prerequisites go; absent without one. */
  manifest?: RunManifest
  /** The run's trees being ended: an outcome means every attempt is closed, not that every tree has ended. */
  cleaning?: number
  /** A managed run's outcome once every task is terminal: it is `finished` then, its idle sessions kept for a look. */
  outcome?: 'succeeded' | 'failed' | 'cancelled'
  /** A managed run's tasks, in the crew's order. */
  tasks?: RunTask[]
  /**
   * A record of a run the server no longer holds (it ended before the server last started, or was evicted), as `GET /api/runs/{run}`
   * marks it and the crew's runs list it: read-only, nothing of it live, its `state` as recorded (`interrupted` for a run that was running
   * when the server last stopped); Resume as new run resumes it from the record, once.
   */
  record?: boolean
}

/** GET /api/reach: what the server knows about being reached from outside its network, and whether its TLS listener has a certificate. */
export interface ReachInfo {
  mode: 'off' | 'auto' | 'manual'
  /** A port on the public address reaches the server (mapped on the router, or forwarded by hand in manual mode). "Mapped" is not "verified": see `verified`. */
  mapped: boolean
  method?: 'upnp' | 'pcp' | 'nat-pmp' | 'manual'
  /** `https://<externalIp>[:port]`, the base links take once `tls.ready`. */
  publicUrl?: string
  externalIp?: string
  externalPort?: number
  listenPort?: number
  expiresAt?: string
  renewedAt?: string
  /** `ok` when the server reached itself through the public URL, `unverified` when it did not (usual from inside the network). */
  verified?: 'ok' | 'unverified'
  error?: string
  /** The TLS listener's certificate; absent without a TLS listener. `ready` says one is served. */
  tls?: { mode: 'acme' | 'files'; ready: boolean; identifiers?: string[]; challenge?: string; notAfter?: string; renewAt?: string; lastError?: string; nextTry?: string }
}

export interface PathGit {
  repo: boolean
  commits: boolean
}
/** One directory GET /api/paths lists: its name, its full path, and whether it is a git repository with a commit. */
export interface PathEntry {
  name: string
  path: string
  git: PathGit
}
/**
 * What POST /api/catalog/check says of a program: found, and where, or not.
 * `unknown` says the server did not judge it, as GET /api/catalog's
 * `available` counts it installed: `relative` for a relative path with a
 * separator (resolved at launch in the session's directory), `timeout` for a
 * lookup that did not answer within 2 s.
 */
export interface CommandCheckReply {
  found: boolean
  path?: string
  unknown?: 'relative' | 'timeout'
  /** With an adapter in the request: what its probe found the program to be. */
  identity?: Identity
}
export interface PathsReply {
  dir: string
  entries: PathEntry[]
  truncated: boolean
  /** Directories matching what was typed left out past the limit; `moreUnknown`: there may be others besides (round 14). */
  more?: number
  moreUnknown?: boolean
}
/** What GET /api/git/check says of a working directory, by the launch's rules for worktrees. */
export interface GitCheck {
  inRepo: boolean
  toplevel?: string
  hasCommit: boolean
  message: string
}

export function useSessions() {
  const { request, requestText } = useApi()
  /** A receipt's log, with the SHA-256 the server sent in `X-Conductor-Log-SHA256`. */
  const readLog = (path: string): Promise<StepLog> =>
    requestText(path).then((r) => ({ text: r.text, bytes: r.bytes, sha256: r.headers.get('X-Conductor-Log-SHA256') ?? '' }))

  return {
    list: () => request<{ sessions: SessionInfo[] }>('/api/sessions').then((r) => r.sessions ?? []),
    /** The files every session's agent touched (#304): the palette's Touched group; `limit` files per session (the server's 500 when left out). */
    touched: (limit?: number) => request<TouchedReply>('/api/touched', limit ? { query: { limit: String(limit) } } : {}).then((r) => ({ sessions: r.sessions ?? [], truncated: !!r.truncated })),
    get: (id: string, token?: string) => request<{ session: SessionInfo; role: Role; links?: ShareLink[] }>(`/api/sessions/${encodeURIComponent(id)}`, { token }),
    /** `yolo` overrides the server's default for this launch; left out, the default holds. */
    create: (body: { agentId: string; name?: string; cwd?: string; args?: string[]; cols?: number; rows?: number; yolo?: boolean }) =>
      request<SessionInfo>('/api/sessions', { method: 'POST', body }),
    /** Starts an ended session again: resumed with its agent's recipe when it has had a turn, else relaunched (`notice` says why). */
    resumeSession: (id: string) => request<ResumeResult>(`/api/sessions/${encodeURIComponent(id)}/resume`, { method: 'POST' }),
    /** Starts an ended member of a run again, in its worktree, as the session route does for its session. */
    resumeRunMember: (runId: string, name: string) =>
      request<ResumeResult>(`/api/runs/${encodeURIComponent(runId)}/members/${encodeURIComponent(name)}/resume`, { method: 'POST' }),
    stop: (id: string) => request<SessionInfo | void>(`/api/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' }),
    /** Renames a server session, running or ended (#318): the reply is the session under the name the server kept; the events stream tells every page. */
    rename: (id: string, name: string) => request<SessionInfo>(`/api/sessions/${encodeURIComponent(id)}`, { method: 'PATCH', body: { name } }),
    catalog: () => request<{ agents: AgentInfo[] }>('/api/catalog').then((r) => r.agents ?? []),
    /** Answers a viewer's paste invite (a cpi1 blob) for a server session: the answer blob, which the viewer pastes back. */
    paste: (id: string, body: { offer: string; role: Role; label?: string }) =>
      request<{ answer: string; role: Role }>(`/api/sessions/${encodeURIComponent(id)}/paste`, { method: 'POST', body }),
    /** The STUN servers a viewer gathers its candidates with before it has any session (a paste invite's offer); no token needed. */
    ice: () => request<{ stun: string[] }>('/api/ice', { token: '' }),
    /** A crew's runs, newest first: the live ones, then the records of those that ended (`live` counts the first). */
    crewRuns: (id: string) => request<{ runs: RunInfo[]; live: number }>(`/api/crews/${encodeURIComponent(id)}/runs`),
    /** The launchable agents and the server's yolo default (`yolo` in its config, CONDUCTOR_YOLO, serve --yolo). */
    catalogInfo: () => request<{ agents: AgentInfo[]; yoloDefault?: boolean }>('/api/catalog').then((r) => ({ agents: r.agents ?? [], yoloDefault: !!r.yoloDefault })),
    /** The launchable agents plus the ids hidden from the catalog, which `unhideAgent` brings back. */
    catalogWithHidden: () =>
      request<{ agents: AgentInfo[]; hidden?: string[] }>('/api/catalog').then((r) => ({ agents: r.agents ?? [], hidden: r.hidden ?? [] })),
    /** Adds an agent or replaces the one with the same id (a built-in too); the server saves it in its data directory. */
    saveAgent: (a: AgentInput) => request<{ agent: AgentInfo }>('/api/catalog', { method: 'POST', body: a }).then((r) => r.agent),
    /** Makes a copy of the agent `from` under the body's id (#291): what the body leaves out that the form has no control for is the source's, and an env value of `***` is the source's value, copied and never shown; 409 `id_taken` for an id an agent has. */
    cloneAgent: (from: string, a: AgentInput) => request<{ agent: AgentInfo }>(`/api/catalog/${encodeURIComponent(from)}/clone`, { method: 'POST', body: a }).then((r) => r.agent),
    /** Removes the saved override with this id, restoring a built-in it replaced; an agent without one is hidden instead. */
    deleteAgent: (id: string) => request<void>(`/api/catalog/${encodeURIComponent(id)}`, { method: 'DELETE' }),
    /** Takes a hidden id off the hidden list and returns its agent, back as it was; undefined when no agent has that id any more. */
    unhideAgent: (id: string) =>
      request<{ agent?: AgentInfo }>(`/api/catalog/${encodeURIComponent(id)}/unhide`, { method: 'POST' }).then((r) => r.agent),
    /** Whether the program resolves on the server; with an adapter, its identity probe runs on the program found (its version flag). */
    checkCommand: (program: string, adapter?: string) =>
      request<CommandCheckReply>('/api/catalog/check', { method: 'POST', body: { command: [program], ...(adapter ? { adapter } : {}) } }),
    /** One page of the saved crews' summaries, ordered by name, and how many there are in all. `limit` is 1 to 500. */
    listCrews: (offset = 0, limit = 100) =>
      request<{ crews: CrewSummary[]; total: number }>('/api/crews', { query: { offset: String(offset), limit: String(limit) } }).then((r) => ({ crews: r.crews ?? [], total: r.total ?? 0 })),
    /** One crew in full, for the editor. 404 for an unknown id; 409 `crew_unreadable` for a file the server cannot use, with why. */
    getCrew: (id: string) => request<{ crew: CrewInfo }>(`/api/crews/${encodeURIComponent(id)}`).then((r) => r.crew),
    /**
     * Without an id, creates a crew: the server derives its id from the name (then `-2`, `-3`… when taken). With an id, replaces that crew's
     * fields; its id and createdAt stay. Only CrewInput's fields are sent, so a CrewInfo read with getCrew can be passed as it is.
     * 400 `invalid_crew` says what is wrong, an agent the catalog does not have included; 404 for an unknown id; 409 `crew_unreadable` for a
     * crew whose file the server cannot use.
     */
    saveCrew: (crew: CrewInput, id?: string) =>
      (id === undefined
        ? request<{ crew: CrewInfo }>('/api/crews', { method: 'POST', body: toCrewInput(crew) })
        : request<{ crew: CrewInfo }>(`/api/crews/${encodeURIComponent(id)}`, { method: 'PUT', body: toCrewInput(crew) })
      ).then((r) => r.crew),
    deleteCrew: (id: string) => request<void>(`/api/crews/${encodeURIComponent(id)}`, { method: 'DELETE' }),
    /** Saves a copy under `<id>-copy` (then `-copy-2`…), named "<name> copy". 400 `invalid_crew` when one of its agents is no longer in the catalog. */
    duplicateCrew: (id: string) =>
      request<{ crew: CrewInfo }>(`/api/crews/${encodeURIComponent(id)}/duplicate`, { method: 'POST' }).then((r) => r.crew),
    /**
     * Seeds the five example crews, as `conductor serve --examples` does: each is saved unless a file with its id exists, which is left
     * alone. Returns the ids added and skipped, in the examples' order. 503 `store_unavailable` without a data directory.
     */
    loadExampleCrews: () => request<{ added: string[]; skipped: string[] }>('/api/crews/examples', { method: 'POST' }),
    /**
     * Launches a saved crew. Resolves once the session of every member that starts immediately exists (the member `starting`);
     * each prompt is typed once its session is ready, and a member whose prompt cannot be typed ends alone.
     * 400 `invalid_crew` (no members, a hosted crew, an agent the catalog lacks or one given arguments it does not take, a symlinked
     * `.conductor`), `invalid_cwd`; 409 `not_a_repo` for worktree isolation outside a git working tree or in one without a commit,
     * `run_stopped` when the run is stopped while it launches; a member's session errors as POST /api/sessions; 500 `launch_failed`.
     * A crew with `viewLinkTtlSeconds` also gets a view link of the run, in `viewLink`: its token is in this reply only, to show once.
     */
    launchCrew: (id: string, body?: { label: string }) =>
      request<{ run: RunInfo; viewLink?: { link: ShareLink; token: string; url: string } }>(`/api/crews/${encodeURIComponent(id)}/launch`, { method: 'POST', body }),
    /** Every run in the server's memory, newest first. */
    listRuns: () => request<{ runs: RunInfo[] }>('/api/runs').then((r) => r.runs ?? []),
    /** One run, with each worktree member's diff. */
    getRun: (id: string) => request<{ run: RunInfo; record?: boolean }>(`/api/runs/${encodeURIComponent(id)}`).then((r) => (r.record ? { ...r.run, record: true } : r.run)),
    /**
     * Adds a member mid-run; one that starts immediately has its session when this resolves. 400 `invalid_crew` (a name the run has,
     * one whose start failed included), 409 `run_stopped`; a session that cannot be created leaves the member in the run, ended.
     */
    addRunMember: (runId: string, member: CrewMember) =>
      request<{ run: RunInfo }>(`/api/runs/${encodeURIComponent(runId)}/members`, { method: 'POST', body: toCrewMember(member) }).then((r) => r.run),
    /** Starts a pending member by hand, whatever its start condition; resolves once its session exists. 409 `member_started` or `run_stopped`. */
    startRunMember: (runId: string, name: string) =>
      request<{ run: RunInfo }>(`/api/runs/${encodeURIComponent(runId)}/members/${encodeURIComponent(name)}/start`, { method: 'POST' }).then((r) => r.run),
    /** Stops every member's session; the worktrees stay. */
    stopRun: (runId: string) => request<{ run: RunInfo }>(`/api/runs/${encodeURIComponent(runId)}/stop`, { method: 'POST' }).then((r) => r.run),
    /**
     * Starts a new run of a stopped run's crew in which every member with a resumable conversation continues it in its kept worktree and
     * branch, without a prompt, and the others start afresh under their start rules. 409 `run_running` while the run goes.
     */
    resumeRun: (runId: string) => request<{ run: RunInfo }>(`/api/runs/${encodeURIComponent(runId)}/resume`, { method: 'POST' }).then((r) => r.run),
    /**
     * Types `text` and a carriage return into the named members, or every member when `members` is empty or missing, recorded as input by
     * `byName`. Line breaks and tabs become spaces and other control characters go; 400 `invalid_request` when nothing is left or it is over
     * 4096 bytes. A member waiting on a prompt is skipped, never typed into.
     */
    broadcastRun: (runId: string, body: { text: string; members?: string[]; byName?: string }) =>
      request<BroadcastResult>(`/api/runs/${encodeURIComponent(runId)}/broadcast`, { method: 'POST', body }),
    /** A managed run's task with every attempt, each with what it submitted (#334) and its receipts; a run the server let go answers from its ledger. */
    runTask: (runId: string, taskId: string) => request<{ task: RunTask; attempts: RunAttempt[] }>(`/api/runs/${encodeURIComponent(runId)}/tasks/${encodeURIComponent(taskId)}`),
    /** An attempt's receipts, by when each started (EN1). */
    runSteps: (runId: string, taskId: string, n: number) =>
      request<{ steps: RunStep[] }>(`/api/runs/${encodeURIComponent(runId)}/tasks/${encodeURIComponent(taskId)}/attempts/${n}/steps`).then((r) => r.steps ?? []),
    /** A run's prerequisites' receipts, by when each started. */
    runPrerequisites: (runId: string) => request<{ steps: RunStep[] }>(`/api/runs/${encodeURIComponent(runId)}/prerequisites`).then((r) => r.steps ?? []),
    /** One receipt's log of an attempt (at most 1 MiB) with its SHA-256; 410 `log_dropped` when the run's logs were at their bound. */
    stepLog: (runId: string, taskId: string, n: number, id: string, k: number) =>
      readLog(`/api/runs/${encodeURIComponent(runId)}/tasks/${encodeURIComponent(taskId)}/attempts/${n}/steps/${encodeURIComponent(id)}/${k}/log`),
    /** One prerequisite receipt's log with its SHA-256. */
    prerequisiteLog: (runId: string, id: string, k: number) => readLog(`/api/runs/${encodeURIComponent(runId)}/prerequisites/${encodeURIComponent(id)}/${k}/log`),
    /**
     * Pauses or resumes a managed run's dispatch, as a command with a receipt (docs/protocol.md, "Commands with receipts"): the same
     * `commandId` again answers with the same receipt; `expectedRevision` refuses the command once the run moved on (409 `stale_revision`).
     */
    pauseRun: (runId: string, paused: boolean, body: { commandId: string; expectedRevision?: number }) =>
      request<{ receipt: Receipt }>(`/api/runs/${encodeURIComponent(runId)}/${paused ? 'pause' : 'unpause'}`, { method: 'POST', body }),
    /** Cancels, retries or accepts a task of a managed run, as a command with a receipt; a refusal is a 409 carrying the receipt. */
    taskCommand: (runId: string, taskId: string, command: 'cancel' | 'retry' | 'accept', body: { commandId: string; expectedRevision?: number }) =>
      request<{ receipt: Receipt }>(`/api/runs/${encodeURIComponent(runId)}/tasks/${encodeURIComponent(taskId)}/${command}`, { method: 'POST', body }),
    /** A run's share links, each with the viewers attached through it to the sessions of its members. */
    listRunLinks: (runId: string) => request<{ links: ShareLink[] }>(`/api/runs/${encodeURIComponent(runId)}/links`).then((r) => r.links ?? []),
    /** The run's links, whether a member is published to the switchyard now, and whether a member's publication there was ended, which a run link made now makes again. */
    runLinkList: (runId: string) =>
      request<{ links: ShareLink[]; republish?: boolean; published?: boolean }>(`/api/runs/${encodeURIComponent(runId)}/links`).then((r) => ({ links: r.links ?? [], republish: r.republish === true, published: r.published === true })),
    /** Creates a run link: its role on the session of every member of the run, those added later included. The token is shown this once. */
    createRunLink: (runId: string, body: { role: Role; label?: string; ttlSeconds?: number }) =>
      request<{ link: ShareLink; token?: string; url: string; invite?: string; remote?: boolean; rendezvous?: { server: string; error: string } }>(`/api/runs/${encodeURIComponent(runId)}/links`, { method: 'POST', body }),
    /** Revokes a run link, closing every viewer attached through it. */
    revokeRunLink: (runId: string, linkId: string) =>
      request<void>(`/api/runs/${encodeURIComponent(runId)}/links/${encodeURIComponent(linkId)}`, { method: 'DELETE' }),
    /** OS user running the server; the default display name for admins. */
    whoami: () => request<{ user: string; host: string; switchyard: boolean; home?: string; version?: string }>('/api/whoami'),
    /** The server's reach: its public address, the mapped port and the certificate's readiness. */
    reach: () => request<ReachInfo>('/api/reach'),
    /**
     * The child directories of the longest existing directory in `prefix`, under the allowed roots (symlinks resolved), at most 50 of the
     * first 2000 entries read, each marked when it is a git repository (`repo`) with a commit (`commits`). Hidden directories show once
     * the typed element starts with a dot. 400 `invalid_cwd` when no part of the prefix is under a root.
     */
    /** `scope: 'any'` lists outside the allowed roots, on a server with paths.browse any (the desktop app's). */
    listPaths: (prefix: string, limit = 50, scope: 'roots' | 'any' = 'roots') =>
      request<PathsReply>('/api/paths', { query: { prefix, limit: String(limit), ...(scope === 'any' ? { scope } : {}) } }),
    /** Whether a crew with worktrees could launch in `cwd` (the server's default when empty). A preview: the launch's 409 `not_a_repo` decides. */
    gitCheck: (cwd: string) => request<GitCheck>('/api/git/check', { query: { cwd } }),
    /** Every hook adapter, in a stable order, with its install checked in the server user's home, the server's host name, and its webhooks. */
    integrations: () =>
      request<Partial<Integrations>>('/api/integrations').then(
        (r): Integrations => ({ integrations: r.integrations ?? [], host: r.host ?? '', webhooks: r.webhooks ?? [] }),
      ),
    /**
     * Installs an adapter's hooks into the server user's home and returns the files it changed, [] when they were in place.
     * 400 `no_file_route`: nothing to install, or a step left to do by hand with the adapter's `snippet` (files changed before it stay changed);
     * 500 `install_failed`.
     */
    installIntegration: (id: string) =>
      request<{ changed: string[] }>(`/api/integrations/${encodeURIComponent(id)}/install`, { method: 'POST' }).then((r) => r.changed ?? []),
    links: (id: string) => request<{ links: ShareLink[] }>(`/api/sessions/${encodeURIComponent(id)}/links`).then((r) => r.links ?? []),
    /**
     * The links, whether the session is published to the switchyard now (`published`, with one configured), and whether the switchyard
     * ended its publication, which a link made now publishes again (`republish`).
     */
    linkList: (id: string) =>
      request<{ links: ShareLink[]; republish?: boolean; published?: boolean }>(`/api/sessions/${encodeURIComponent(id)}/links`).then((r) => ({ links: r.links ?? [], republish: r.republish === true, published: r.published === true })),
    /**
     * The token is shown this once; `invite` is the same link for the desktop app; `remote` says the switchyard minted it (no token here);
     * `rendezvous` says, for a link made here although a switchyard is configured, which one and why the session is not there.
     */
    createLink: (id: string, body: { role: Role; label?: string; ttlSeconds?: number }) =>
      request<{ link: ShareLink; token?: string; url: string; invite?: string; remote?: boolean; rendezvous?: { server: string; error: string } }>(`/api/sessions/${encodeURIComponent(id)}/links`, { method: 'POST', body }),
    revokeLink: (id: string, linkId: string) =>
      request<void>(`/api/sessions/${encodeURIComponent(id)}/links/${encodeURIComponent(linkId)}`, { method: 'DELETE' }),
    /** The link's session or run, from this server, or from `server` (a switchyard, utils/invite.ts joinServer) with no workbench token. */
    join: (token: string, server = '') => request<JoinInfo>(`/api/join/${encodeURIComponent(token)}`, { token, base: server || undefined }),
    /**
     * Sets a session's attention state as the admin. 429 `rate_limited` means the session's limit of 20 reports a second, 40 at once, which its
     * events share, is used up: nothing changed.
     */
    setAttention: (id: string, state: 'needs_input' | 'working' | 'done' | 'clear', message?: string) =>
      request<{ attention: Attention }>(`/api/sessions/${encodeURIComponent(id)}/attention`, { method: 'POST', body: { state, message } }),
    /**
     * Reports an event for a session. 202 means recorded, or on its way to the host of a hosted session (the host's own limit may still drop it).
     * 429 `rate_limited` means the session's limit of 20 reports a second, 40 at once, is used up (events and attention words alike: a server
     * session's own, or for a hosted session what the server sends on to the host), or that this client has been refused too often.
     * 409 `host_disconnected` means a hosted session has no host connected.
     */
    sendEvent: (id: string, body: EventInput) =>
      request<{ accepted: boolean }>(`/api/sessions/${encodeURIComponent(id)}/events`, { method: 'POST', body }),
  }
}

export function statusColor(status: SessionStatus): 'success' | 'neutral' | 'warning' | 'error' | 'info' {
  switch (status) {
    case 'running':
      return 'success'
    case 'starting':
      return 'info'
    case 'host_disconnected':
      return 'warning'
    case 'exited':
      return 'neutral'
    case 'stopped':
      return 'neutral'
  }
  return 'neutral'
}

export function statusLabel(status: SessionStatus): string {
  return status.replace('_', ' ')
}
