# Chat or terminal: a second way to run an agent's session (issue #1088)

Status: proposed 2026-11-24, for the maintainers' review before any of
its code. Nothing here is built. The maintainers set it to Planned by
approving it; its rows (The rows) are units of work, grouped into pull
requests as `AGENTS.md` (Working an issue) says, with the boundaries
kept apart. Two decisions come before any builder starts: the vendors'
terms for each agent (Sign-in and what the vendors' own pages say) and
the five asked under Decisions asked of the maintainers. The plan is
written to the default of each.

Written against `main` at `0a60d09f` after reading `internal/session`
(`local.go`, `submit.go`, `attention.go`, `answeras.go`, `chat.go`,
`info.go`, `agentsession.go`, `runtime.go`, `activity.go`), `ext/runtime.go`
and `ext/local`, `internal/api` (`sessions.go`, `resume.go`,
`attention.go`, `ws_viewer.go`, `ws_host.go`, `runs.go`, `router.go`,
`output_guard.go`, `inbox.go`, `exthost.go`, `catalog.go`,
`dispatch_plans.go`), `internal/signal/hosted.go`, `internal/hostagent`
(`agent.go`, `peer.go`, `uplink.go`), `internal/proto` (`frame.go`,
`control.go`, `hostmsg.go`), `internal/catalog`, `internal/agents`
(`claude.go`, `codex.go`, `usage*.go`, `registry.go`), `internal/cli/mcp.go`,
`internal/crew` (`run.go`, `task.go`, `handoff.go`, `decisions.go`,
`firstmessage.go`, `messages.go`), `internal/crew/ledger`, `internal/journal`,
`internal/history/kinds.go`, the app's session page and its pieces
(`web/app/pages/sessions/[id].vue`, `components/TerminalView.vue`,
`ChatThread.vue`, `ChatComposer.vue`, `QuickReplyBar.vue`,
`utils/transport/`, `utils/protocol.ts`, `utils/attention.ts`), the
browser tests' fixtures (`web/e2e/stub-agent.sh`, `server.ts`,
`conductor.e2e.json`), and the plans this one meets:
`docs/plans/agent-harness.md`, `runtime.md`, `decisions.md`,
`decide-grant.md`, `history.md`, `sync-seams.md`, `host-connection.md`,
`capacity.md`. The vendors' facts were read in their public
documentation on 2026-11-24 and are cited by address.

The names follow the railroad: Conductor is the application; Yardmaster
the agent harness, which runs a crew with tasks as its managed run;
Dispatch the ticketing; Brakeman the sidekick agent; Switchman the task
router. Two words are new in this plan. A session's **way** is how
Conductor runs its agent: the **terminal way** is today's (the agent's
own terminal program in a PTY), the **protocol way** is the agent's own
documented non-interactive interface, a child with pipes. A protocol
session's **conversation** is what the app shows of it: typed entries,
the **transcript** in code. "Chat" keeps its meaning: the people's chat
beside a session (`internal/session/chat.go`, `docs/protocol.md`,
Chat), unchanged (Decisions, 1).

## What a person sees when it is done

A person opens Claude Code from the launch dialog and, under **How it
runs**, keeps **Conversation** (the default where the installed Claude
Code is one Conductor has checked) or picks **Terminal** (today's). A
conversation session's page shows, where the terminal was, the
conversation: what the person asked; the agent's words as they stream
in, as text drawn from Markdown with no raw HTML; each tool step as one
row ("Read `README.md`", "Ran `go test ./...`", "Edited `main.go` +3 −1")
that opens to its input and output; each file change with a small diff
and a link to the editor's diff tab; a turn's line ("Working for 12 s")
and its end with what it used. Below it, a composer: Enter sends,
Shift+Enter makes a new line, and **Stop this turn** while a turn runs,
which ends the turn and keeps the session and its conversation. Stop
session stays where it is, in More.

When the agent asks to run something that needs approval, the
conversation shows a **permission card**, amber, with the exact call:
the tool, its whole input in the code font, the folder it runs in. The
operator answers **Allow once** or **Deny** on the card; the session's
row in the sidebar, Needs you and the notifications say it waits, as
they do for a question today. Nobody else can answer it: a share link
sees the card and no buttons. If nobody answers, nothing runs.

The header holds a two-part switch, **Conversation · Terminal**, for an
agent that has both. When the agent's turn has ended, the operator may
move the session to the other way: Conductor ends the agent's process
and starts it again the other way on the same conversation, in the same
Conductor session, with the same links, people and record. The
conversation picks up where the terminal left it, and the terminal
where the conversation left it. During a turn the switch says why it
waits.

A crew launched with tasks runs its members as conversations where
their agent has one; the run's log says which way each attempt ran. A
member's prompt is sent as a message the agent acknowledges, never
typed and hoped for; its turn's end comes from the agent's own stream;
messages, handoffs and answers sent during a turn wait for its end.

Nothing else changes for a terminal session. An agent with no
structured interface, or an installed version Conductor has not
checked, runs in its terminal as today, and the launch dialog says why.

## What exists today that this builds on

- **One session type.** `*session.Local` (`internal/session/local.go:203`,
  `NewLocal` at `:560`) is the session the server and `conductor host`
  run; the registry holds it as `session.Driver` (`info.go:177`), five
  methods. `internal/api` asserts `*session.Local` at 35 sites, the crew
  engine holds it directly (`crew.Launcher`, `internal/crew/run.go:39`;
  the lookup passed to `NewEngine`, `run.go:949`), and `Uplink.Publish`
  takes it (`internal/hostagent/uplink.go:205`). `Local` drives a
  `session.Process` (`local.go:28`: a reader, a writer, `Resize`, `Done`,
  `Exit`, `Stop`), which `session.RuntimeProcess` adapts from a Runtime's
  terminal (`runtime.go:16`). `NewLocal` always builds the screen
  watchers and starts the output pump (`local.go:619-660`, `pump` at
  `:664`).
- **The core a second way reuses.** Attention and its setters
  (`TrySetAttentionTurn` `local.go:1914`, `TryAskID` `:1968`,
  `TryAskDecision` `:1988`, `WithdrawQuestion` `:1955`), the sources
  that report a turn (`ReportsTurn`, `attention.go:65`), the activity
  log (`Record` `local.go:829`; 200 kept, 50 replayed, `activity.go:63,68`),
  the agent's own conversation id (`ReportAgentSession`,
  `agentsession.go:79`), the claim of a question (one question, one
  winner: `docs/protocol.md`, Answering a question), the people's chat
  and its bounded ring (`chatRing`, `chat.go:356`), links, roles
  (`view`, `control`; `ErrReadOnly` `info.go:194`) and the attach
  sequence (`AttachWith`, `local.go:2941`).
- **The line path.** `Local.Submit` (`submit.go:426`) is the one way
  Conductor types into a session: role, length, one at a time, the
  question and decision rules, then a bracketed paste, a 250 ms pause
  and an Enter, with screen checks between. Every crew prompt, handoff,
  decision answer and person's line goes through it.
- **How a session starts.** `createLocalSession` (`internal/api/sessions.go:193`):
  the catalog entry, the folder through `openCwd` (`:669`) and
  `resolveCwd` (`:726`), the argv as an array (the session recipe's
  `StartArgs` or `ResumeArgs`, `:296-326`), the environment, the agent
  token, then `rt.StartTerminal` (`:472`) and `session.NewLocal(info,
  session.RuntimeProcess(term), opts)` (`:555`). One live session per
  agent conversation: `takeConversation` (`internal/api/resume.go:123`).
- **The Runtime.** `ext.Runtime` (`ext/runtime.go:46-92`) starts a
  session only as a terminal (`StartTerminal`); `Run` and `Serve` give
  no stdin and no stream to read. A registered Runtime is offered
  managed runs alone (`ext/runtime.go:17-20`). `ext/local/boundary_test.go`
  fails a process started outside the runners (`internal/pty`,
  `internal/proc`) or a runner used outside `ext/local`.
- **Structured reads already shipped.** The usage readers run Claude
  Code's `-p ... --output-format stream-json --verbose`
  (`internal/agents/usage_claude.go:40`) and Codex's `app-server
  --listen stdio://` (`usage_codex.go:23`) as bounded one-shot reads,
  each under a version gate (`claudeUsageQualified`, `codexUsageQualified`),
  lines bounded at 1 MiB and output at 4 MiB (`usage.go:139-140`).
- **Conductor's MCP server.** `conductor mcp` (`internal/cli/mcp.go:25-31`)
  is registered with Claude Code by `--mcp-config` (`internal/agents/claude.go:85,127`,
  server name `conductor`) and with Codex by `-c mcp_servers.conductor.*`
  (`codex.go:148`). It reaches the server with the session's agent token.
- **The catalog.** `catalog.Agent` (`internal/catalog/catalog.go:23`),
  validated by `validate` (`:336`), with its session recipe
  (`SessionRecipe`, `:126`; Claude Code's `--session-id {id}` and
  `--resume {id}`, `defaults.go:132`). A ticket's approved agent is
  digested by `agentSelection` (`internal/api/dispatch_plans.go:330`).
- **The wire.** Frame types 0x01 to 0x0A and 0x10 (`internal/proto/frame.go:19-29`);
  `DecodeRelay` takes a fixed list of inner types, and a server that
  meets another closes the host's connection (`frame.go:171`,
  `internal/api/ws_host.go:268-278`). The relay's guard for a viewer
  whose role is not control is a deny list (`internal/signal/hosted.go:1884-1903`).
  A server tells a host what it takes through words in `proto.Registered`
  (`hostmsg.go`: `Decisions`, `SessionInfo`, `AgentSessions`,
  `ActivitySeq`, `Renews`), each absent from an older server.
- **The app.** The session page's one connection belongs to the
  terminal component (`components/TerminalView.vue:656-659`): there is
  no connection without an xterm. The people's chat (`ChatThread.vue`,
  `ChatComposer.vue`, `ChatSheet.vue`), the reply bar, the decision
  card and the diff editor are pieces a conversation view reuses.

## The rules it keeps

- **The terminal way is never removed or degraded.** Every terminal
  session behaves byte for byte as on `main`; every test of the terminal
  way passes unchanged (CONTRIBUTING.md, The open-core rules, 2).
- **Stop, inspect, revoke, export and required approvals stay free.**
  Stop session, Stop this turn and the permission card are in the free
  build; no permission request is ever answered by a timer or a default
  of the vendor's (The open-core rules, 4).
- **Commands are argv arrays.** Every child is started from an argv
  array, never a shell string.
- **Folders.** A protocol session's working directory goes through
  `openCwd` and `resolveCwd` as every launch's does; the floor of the
  permission path reads the server's deny list (`session.DenyList`).
- **Stdlib first, with one stated exception.** No new Go dependency. The
  agents' streams are decoded leniently (unknown fields and variants
  ignored), because their authors say so: the Agent Client Protocol's
  v2 migration guide ("Receivers MUST ignore unknown fields",
  https://agentclientprotocol.com/protocol/v2/migration.md), Claude
  Code's TypeScript reference on its capabilities ("It is an open set:
  ignore values you don't recognize",
  https://code.claude.com/docs/en/agent-sdk/typescript.md), and Codex's
  schema "specific to the Codex version you ran"
  (https://learn.chatgpt.com/docs/app-server.md). Conductor's own
  frames and routes keep `DisallowUnknownFields`.
- **Protocol changes touch three places together**, each new frame and
  message with its bound and its test: `internal/proto`,
  `web/app/utils/protocol.ts`, `docs/protocol.md`.
- **Per-connection bounds stay**, and a child's stream gets its own:
  a line bound, an output bound, a queue bound.
- **Execution behind one seam.** Every child of a session starts through
  an `ext.Runtime` (`docs/plans/runtime.md`, Why and what happens); the
  pipes start is an optional method of a Runtime (design 2).
- **Windows and WSL.** The child runs where the server runs and talks
  over its pipes: nothing crosses a network, and nobody configures one.

## The design

### 1. One session, two ways

`*session.Local` stays the one session type: 35 sites in
`internal/api`, the engine and the uplink hold it, and a second type
would need a third arm at each. It gains a second way of being run; the
terminal way's code is not moved behind an interface in this plan.

A protocol session's `Local` holds, instead of a `Process`:

- a **child** (`session.Child`): `Done() <-chan struct{}`, `Exit()
  pty.ExitStatus`, `Stop(ctx, grace) error`. Its `Stop` ends the agent's
  input first (the agent's documented way to end: Claude Code "cancels
  the prompt as soon as the input ends", https://code.claude.com/docs/en/headless.md,
  Stop a run with SIGTERM), waits `grace`, then ends the process group as
  a terminal's `Stop` does. `session.PipesChild(ext.Pipes)` adapts the
  Runtime's pipes (design 2).
- a **protocol driver** (`session.Protocol`), the small interface this
  plan adds. Its methods and their exact meaning:

```go
// Protocol is a protocol way's driver: one agent's documented
// structured interface over the child's pipes. The session calls it;
// it calls the session back only through ProtocolEvents, in the order
// the agent's output gave them.
type Protocol interface {
	// Name is the protocol's name from the catalog's closed set
	// ("claude-stream-json", later "codex-app-server", "acp-v1").
	Name() string
	// Begin writes what the protocol needs before a first message
	// (an initialize request, where the protocol has one) and returns
	// nil once the agent has said it can take a message, or the error
	// that says why it cannot. It returns by ctx's end at the latest.
	Begin(ctx context.Context) error
	// Send writes one message as a new user turn and returns once its
	// bytes are written to the agent's input, or the write failed. A
	// written message is not a taken one: the agent's acknowledgement
	// arrives as ProtocolEvents.Accepted with m.Ref.
	Send(ctx context.Context, m Message) error
	// Interrupt asks the agent to end the turn it runs and keep the
	// conversation. It returns once the ask is written; the turn's end
	// arrives as ProtocolEvents.TurnEnded with outcome "interrupted".
	Interrupt(ctx context.Context) error
	// Read reads the agent's output until it ends or ctx ends, and hands
	// each event to e in order. It returns io.EOF at the output's end,
	// and an error for a line past its bound or one it cannot read (the
	// session then ends the child: design 13).
	Read(ctx context.Context, e ProtocolEvents) error
	// CloseInput ends the agent's input. Nothing is written after it.
	CloseInput() error
}
```

and the session's side, implemented by `Local` (an unexported adapter,
so no caller outside the driver reaches it):

```go
// ProtocolEvents is what a driver tells the session. Each call returns
// once the session has applied it (state first, viewers after: I6).
type ProtocolEvents interface {
	Conversation(id string)                       // the agent's own conversation id, as its stream names it
	Accepted(ref string)                          // the agent took the message Send wrote under ref
	TurnStarted(agentTurn string)                 // a turn began ("" when the agent names none)
	Text(ref string, delta string, final bool)    // the agent's words, streamed; final closes ref
	ToolStarted(t ToolStep)                       // a tool call announced, with its name and whole input
	ToolEnded(ref string, r ToolResult)           // its result: status, exit code and output where the agent gives them
	FileChanged(f FileChange)                     // a file a tool changed: path, op, counts, diff where given
	Permission(ctx context.Context, r PermissionRequest) Verdict // an in-stream request (Codex, ACP); blocks until answered or withdrawn
	TurnEnded(outcome string, u Usage)            // "done", "interrupted", "error", "limit"
	Notice(code, words string)                    // something the agent said that is none of the above
}
```

What each existing call of `Local` does on a protocol session:

| Call | On a protocol session |
|---|---|
| `Submit` (`submit.go:426`) | the guards that are not the terminal's stay (role, length, one at a time, withdrawn, ended, the question and decision rules, `UnlessWaiting`, which also waits while a turn runs); then `Protocol.Send`. Line breaks are kept; other control characters are dropped. `SubmitResult.Entered` is set once `Accepted` names the message, within `AcceptWait` (30 s); past it, `Unconfirmed` is set and the message is never sent again (I4). `Typed`, `Reentered` and `NotAtPrompt` stay false. |
| `Input`, `Resize`, `ResizeWith` | refused `ErrNotTerminal`, wire code `not_terminal`, connection kept |
| `AnswerInput`, `AnswerInputReq`, `AnswerAs` | for a question the session asked (a blocker, a decision's mirror, the agent's own `ask`): as today, with the answer sent as a message by `Submit`'s path; for a permission request: refused `ErrPermissionCard` (design 7) |
| `LastLine`, `ScreenLines`, `ScreenRead` | empty answers; `Brakeman`'s screen words find none (its turns stay terminal: Out of scope) |
| `LastOutputAt` | the time of the last event the driver handed over |
| `BracketedPaste` | `(false, false)` |
| `Info` | gains `Way` (`"protocol"`; absent for a terminal session) and `Protocol` (the protocol's name); `Cols` and `Rows` are 0 |
| `Stop` (`local.go:5967`) | answers every pending permission request deny (design 7), then `Child.Stop` with the session's grace |
| `AttachWith` | the protocol replay instead of scrollback (design 6) |

New calls of `Local`: `Way() string`; `Ready() <-chan struct{}`
(closed once `Begin` returned nil; nil for a terminal session);
`Interrupt(ctx, by) error` (controllers only; `ErrNotProtocol` on a
terminal session, whose turn a person ends with the agent's own key);
`Transcript(after, limit)` and `TranscriptPart(seq, name, offset)`
(design 5); `RequestPermission` and `AnswerPermission` (design 7);
`SwapWay` (design 9).

A protocol session reports through the calls that exist, as a hook
does today: `TurnStarted` is attention `working` and `TurnEnded`
attention `done` (`TrySetAttentionTurn`) with a new source,
`SourceProtocol` (`"protocol"`), added to `ReportsTurn`'s; an
interrupted turn's `done` carries the new kind `KindInterrupted`, so
nothing takes it for a finished turn (design 8). A tool step is an
activity entry `tool_use` (`tool_denied`, `error` as it ended), a file
change an entry `file`, exactly as the hook mappers make them
(`internal/notify/mappers.go`), so Activity, Touched, the journal, the
webhooks and the crew engine see one session whichever way it runs. The
attention state machine's screen rules (`local.go:2015-2111`) see no
screen and so never fire for a protocol source (I7).

A protocol session is made by `session.NewProtocolLocal(info, child,
protocol, opts)`: no screen watcher, no pump; it starts the driver's
`Read` on a goroutine of its own and calls `Begin`. Its `Options.OnOutput`
is handed every byte the driver captured (the agent's words, a tool's
output, a diff), so the copied-output check sees it (design 10).

### 2. How the child starts

`ext` gains an optional interface beside `ext.Runtime`, as `ext.Starter`
is optional beside it:

```go
// PipeStarter is a Runtime that can start a long-lived child with its
// input and output as pipes: a session's protocol way. The native
// Runtime implements it. A Runtime without it is never offered a
// managed run whose members run the protocol way.
type PipeStarter interface {
	// StartPipes starts s.Argv in DirFile's directory with exactly s.Env,
	// its stdin and stdout pipes, its stderr counted and dropped, in a
	// process group of its own. The context bounds the start, never the
	// child's life, with StartTerminal's commitment rule.
	StartPipes(ctx context.Context, s PipeSpec) (Pipes, error)
}

type PipeSpec struct {
	Argv    []string
	Dir     string
	DirFile *os.File
	Env     []string
	Ref     ProcessRef
}

// Pipes is a child StartPipes started. Stdin and Stdout are its pipes;
// Done, Exit, Stop and Process are Terminal's, Stop ending the group
// after grace as Terminal.Stop does.
type Pipes interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	Done() <-chan struct{}
	Exit() ExitStatus
	Stop(ctx context.Context, grace time.Duration) error
	Process() ProcessInfo
}
```

The native one is `internal/proc`'s new runner `proc.StartPipes`
(`os/exec` with two pipes, `Setpgid`, stderr read to its end and
counted, the group signalled as `proc.Serve`'s is), wrapped in
`ext/local`. The boundary test's `runners` list gains `StartPipes`,
used only in `ext/local` and `sessiontest`'s fake (a reviewed edit of
the list, as its comment asks). `ext/exttest`'s `RuntimeExec` gains the
pipes' checks (argv, directory, environment, input, output, exit, the
two stops, a context ended before and during the start) for a Runtime
that implements it, and `FakeRuntime` scripts it.

Who starts through it: `createLocalSession` for a protocol launch, a
person's and a managed attempt's alike, through the Runtime it already
takes (`runtimeAt`). A person's session runs natively, as today. A
managed run whose members run the protocol way is offered to a
registered Runtime only when that Runtime implements `PipeStarter`;
otherwise it runs natively, as a declined run does. This leaves
`docs/plans/runtime.md`'s "every process through a Runtime" standing,
with one change in words: the pipes method is optional, and a Runtime
that lacks it is never handed a pipes child.

The environment is the launch's (`launchEnv`, `pty.Inject`'s
`CONDUCTOR_*`), built by `pty.BuildEnv` with `TERM` and `COLORTERM`
left out and `NO_COLOR=1` set. The child's stdout lines are bounded at
`MaxProtocolLine` (8 MiB: Codex's own Go example sets the same buffer,
https://learn.chatgpt.com/docs/app-server.md) and the whole stream at
nothing, since a conversation is long; the driver's queue of events
ahead of the session is bounded at 1,024. Its stderr is counted and
dropped, as the usage readers drop it.

### 3. The catalog's declaration

`catalog.Agent` gains one field:

```go
// Protocol, when set, says this agent may run the protocol way: the
// protocol's name, from a closed set, and arguments added to Command for
// it. nil inherits a replaced agent's; {} turns it off.
Protocol *ProtocolRecipe `json:"protocol,omitempty"`

type ProtocolRecipe struct {
	Name string   `json:"name"`           // "claude-stream-json"; later "codex-app-server", "acp-v1"
	Args []string `json:"args,omitempty"` // at most 16, each 1 to 4096 bytes, no NUL
}
```

- **Validated as adapters are.** `validate` checks the shape (the name
  of `idPattern`'s form, the arguments as `Yolo.Args` are); membership
  in the closed set is checked outside the package, by
  `agentproto.Check` (the drivers' package, below), from
  `config.LoadCatalog` and the Agents page's save, beside
  `agents.CheckAdapter`.
- **Carried.** `inherit`, the deep copy and a clone carry it; the Agents
  page's form sends it from the agent edited (`web/app/utils/agentForm.ts`)
  so a save does not drop it; `AgentInfo` and `AgentInput`
  (`composables/useSessions.ts`) gain it; `docs/protocol.md`'s catalog
  rows say it.
- **The built-in.** Claude Code's entry gets `{"name":
  "claude-stream-json"}` (row 4). Codex's and the Agent Client Protocol
  agents' get theirs in their rows, after the maintainers' word on each
  agent's terms.
- **The drivers' code** lives in a new package, `internal/agentproto`:
  the closed set (`Names`), `Check`, a driver per name (`claude.go`,
  later `codex.go`, `acp.go`), and each driver's gate: `MinVersion`
  (the version its recorded fixtures were taken from) and a start-time
  shape check (the fields the driver needs, read from the agent's first
  answers, never cached by version). It imports `internal/session` and
  `internal/catalog`; neither imports it.
- **What a version the driver has not qualified does.** At launch the
  identity probe's version (`internal/agents/probe.go`) below
  `MinVersion`, or a probe that did not answer, starts the terminal way,
  and the session's first activity entry says why ("Claude Code 2.1.200
  is not a version Conductor has checked for conversations; this session
  runs in its terminal"). A start whose first answers fail the shape
  check ends the child before any message is sent and starts the
  terminal way on the same conversation id, with the same words.
- **The digest.** `agentSelection` adds the protocol's name to a
  ticket's approved agent only when the launch chose the protocol way,
  so every approved digest of a terminal launch stands unchanged, and a
  plan approved for a terminal launch can never start a protocol one.
- **Whether an agent moves between ways** is the driver's, not the
  operator's: a constant per driver (`Moves`), true only where resume
  across the two ways is established (design 9).

### 4. Each agent's interface, as its own documents state it

Each fact below is marked **documented** (its authors' public pages say
it), **experimental** (documented, and its authors call it experimental
or preview), or **undocumented** (known from a vendor's open-source code
or a recorded run only). Each undocumented or experimental fact the
plan uses has a gate and a fallback.

#### Claude Code (first)

The interface: `claude -p` with stream-json input and output. Its
authors call it "the Agent SDK via the CLI" and tell programs in other
languages to run the CLI as a subprocess
(https://code.claude.com/docs/en/headless.md; "To drive the same agent
loop from a language other than Python or TypeScript, run the CLI as a
subprocess", https://code.claude.com/docs/en/agent-sdk/overview.md).
There is no Go SDK; the Agent SDK spawns this same program over stdio
(https://code.claude.com/docs/en/agent-sdk/hosting.md). Conductor runs
the person's own installed `claude`, never a bundled copy, and no SDK.

| Fact | Address | Standing | Gate and fallback |
|---|---|---|---|
| `-p`, `--input-format stream-json`, `--output-format stream-json` (with `--verbose`), `--include-partial-messages`, `--replay-user-messages` ("Re-emit user messages from stdin back on stdout for acknowledgment") | https://code.claude.com/docs/en/cli-reference.md | documented | none needed |
| `--session-id <uuid>`, `--resume <id>`; a `-p` session resumes by id in the terminal UI: "You can still resume one by passing its session ID to `claude --resume <session-id>`" | https://code.claude.com/docs/en/sessions.md | documented | the move is observed by hand in row 7 before `Moves` is set |
| `--permission-mode`; "`claude -p` ... with no permission mode set start in Manual mode", and a settings file may change the default | https://code.claude.com/docs/en/permission-modes.md | documented | Conductor always passes the mode |
| `--permission-prompt-tool mcp__<server>__<tool>`: "an MCP tool to handle permission prompts in non-interactive mode"; its result: "one text block whose text is `{"behavior": "allow"}` or `{"behavior": "deny", "message": "<reason>"}`" | https://code.claude.com/docs/en/cli-reference.md, https://code.claude.com/docs/en/errors.md (Permission prompt tool returned an invalid result) | documented | none needed |
| the arguments Claude Code calls the prompt tool with (the tool's name, its input, the call's id) | not stated on the pages read | undocumented | row 4 records them in a fixture; the gate refuses a version whose call lacks the tool's name and input, and the binding (design 7) needs the call's id or a unique match |
| a stdio MCP server's tool call: idle timeout 30 minutes by default; a per-server `timeout` raises it; overall `MCP_TOOL_TIMEOUT` about 28 hours | https://code.claude.com/docs/en/env-vars.md (`CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT`, `MCP_TOOL_TIMEOUT`) | documented | Conductor denies before the bound in effect (design 7) |
| a `PermissionRequest` hook runs beside the prompt tool, "and whichever decides first applies" | https://code.claude.com/docs/en/hooks.md | documented | a request answered elsewhere is withdrawn (design 7) |
| the event types on stdout (`system/init`, `assistant`, `user`, `stream_event`, `result` with cost and usage, and more) | https://code.claude.com/docs/en/agent-sdk/typescript.md, Message Types; https://code.claude.com/docs/en/headless.md | documented as the TypeScript SDK's message union, an open set | decoded leniently; unknown events become nothing |
| the control envelope (`control_request`, `control_response`, `request_id`), `initialize`, `interrupt` with `cancel_queued`, and `pending_permission_requests` | https://code.claude.com/docs/en/agent-sdk/typescript.md only ("if you parse the wire protocol yourself"); the request schemas only in the MIT Python SDK's source | documented in one reference only, schemas undocumented | **gated**: used only at or above `MinVersion` with a recorded-fixture contract test; without it, `Begin` treats the first `system/init` as ready, and Interrupt falls back to SIGINT ("To end the turn instead, send SIGINT", headless.md), then to ending the child and resuming the conversation |
| `--permission-prompt-tool stdio` (the SDK's own route) | not on any page read | undocumented | **not used** |
| SIGTERM leaves the turn unfinished; closing input cancels a waiting permission prompt | https://code.claude.com/docs/en/headless.md, Stop a run with SIGTERM | documented | `Child.Stop` closes input first |
| "Piped stdin is capped at 10MB" | https://code.claude.com/docs/en/headless.md | documented; whether it holds for a long stream-json input is not stated | the driver counts what it wrote; past 8 MiB it ends the child at the next turn's end and resumes the same conversation |
| `--bare` "never reads OAuth credentials" and "will become the default for `-p` in a future release" | https://code.claude.com/docs/en/headless.md | documented, a future change | Conductor never passes `--bare`; when the default changes, the gate's shape check fails (no account in the init) and the session runs in its terminal, in words, until the driver passes the documented opt-out |
| two writers on one session lose messages; no lock is documented for a foreground session | https://code.claude.com/docs/en/sessions.md | documented | the one-writer rule is Conductor's (design 9) |
| flags not restored on resume (`--mcp-config`, `--settings`, `--tools`, ...) | https://code.claude.com/docs/en/sessions.md | documented | every start passes them again |

The launch argv of a protocol session of Claude Code: the catalog's
`Command`, `-p --input-format stream-json --output-format stream-json
--verbose --include-partial-messages --replay-user-messages`, the
protocol's `Args`, the session recipe's `--session-id <uuid>` (or
`--resume <id>`), the model flag, `--permission-mode default` (or, for
a yolo launch, the catalog's yolo arguments, as the terminal way), the
launch's arguments, `--mcp-config <hooks>/claude-protocol-mcp.json`
(Conductor's MCP server with `timeout` set to `PermissionWait`, design
7), `--permission-prompt-tool mcp__conductor__permission`, and
`--settings <hooks>/claude-protocol.json` (the floor's deny rules and
its `PreToolUse` hook, design 7). The terminal way's hook settings
(`claude.json`, whose `PermissionRequest` and `Stop` hooks would report
the same facts twice), the trust arguments and the screen recipe are
not passed. Conductor sets no environment variable that names another
client.

#### Codex (second, after the maintainers' word on its terms)

The interface: `codex app-server --listen stdio://`, JSON-RPC 2.0 with
the header left out, one message a line. Its authors: "the interface
Codex uses to power rich clients ... Use it when you want a deep
integration inside your own product: authentication, conversation
history, approvals, and streamed agent events"
(https://learn.chatgpt.com/docs/app-server.md); and also "primarily for
development and debugging and may change without notice"
(https://learn.chatgpt.com/docs/developer-commands.md?surface=cli) and
"experimental and isn't supported for production workloads"
(https://learn.chatgpt.com/docs/mcp-server.md). The same page shows a
Go client over stdio with the standard library. `codex exec --json`
"immediately rejects approval requests it receives" (its source,
`codex-rs/exec/src/approval_warning.rs`) and is no base for this.

| Fact | Address | Standing | Gate and fallback |
|---|---|---|---|
| `initialize` with `clientInfo`, then `initialized`; "Use `clientInfo.name` to identify your client" | app-server.md | experimental (the command) | Conductor names itself `conductor` |
| `thread/start`, `thread/resume`, `turn/start`, `turn/interrupt` ("the turn ends with `status: "interrupted"`") | app-server.md | experimental | stable surface only (`experimentalApi` false: "the server rejects experimental methods/fields") |
| approvals: `item/commandExecution/requestApproval` and `item/fileChange/requestApproval`, answered `accept`, `acceptForSession`, `decline`, `cancel` (and amendments in the schema) | app-server.md; the generated schema | experimental | first cut answers `accept` or `decline` only |
| types are "specific to the Codex version you ran" (`codex app-server generate-json-schema`) | app-server.md | documented | the row records the schema of `MinVersion` and decodes leniently |
| a TUI resumes an app-server thread by its id (`codex resume <UUID>`); one writer per thread across processes ("already has an active writer") | Codex's source (`codex-rs/tui/src/lib.rs`, `codex-rs/rollout/src/writer_lock.rs`) | undocumented | the move is offered only after its own observation (design 9) |
| several clients of one app server: the first answer to an approval settles it | Codex's source (`outgoing_message.rs`) | undocumented | Conductor never attaches a second client: no `--remote` terminal on a protocol thread |

#### The Agent Client Protocol (one client, third)

Protocol 1 is stable; "This version is only incremented when breaking
changes are introduced"
(https://agentclientprotocol.com/protocol/v1/initialization.md). One
transport, stdio, newline-delimited JSON-RPC
(https://agentclientprotocol.com/protocol/v1/transports.md). A client
must implement `session/request_permission`, options of kind
`allow_once`, `allow_always`, `reject_once`, `reject_always`, and "The
Agent owns remembered decisions" (https://agentclientprotocol.com/protocol/v1/tool-calls.md).
Version 2 is a preview, "not frozen"
(https://agentclientprotocol.com/announcements/acp-v2-preview.md). No
official Go library exists (https://agentclientprotocol.com/libraries/community.md);
Conductor writes the small client it needs on `encoding/json`.

The first cut's client: protocol 1; it advertises no file-system and no
terminal capability (optional in 1, removed in 2); it sends
`initialize`, `session/new`, `session/load` or `session/resume` where
advertised, `session/prompt`, `session/cancel`; it answers
`session/request_permission` through design 7, `allow_once` or
`reject_once` only; it draws `session/update`'s message chunks,
`tool_call`, `tool_call_update` (with `diff` content), `plan` and
`usage_update`, and ignores the rest. Never the Node adapters for Claude
Code or Codex.

Which agents of the catalog speak it natively, by their own pages:
GitHub Copilot CLI (`copilot --acp`, "in public preview and subject to
change", https://docs.github.com/en/copilot/reference/acp-server),
Cursor CLI (`agent acp`, https://cursor.com/docs/cli/acp.md), OpenCode
(`opencode acp`, protocol 1, https://opencode.ai/docs/acp/), Goose
(`goose acp`, https://goose-docs.ai/docs/oi-kit/acp/index.md),
Antigravity (`agy acp`, https://antigravity.google/docs/cli/acp.md),
oh-my-pi (`omp acp`, its README) and DeepSeek Harness (`dsh --profile
acp`, its `docs/cli-help.md`, "developer preview"). Whether each one's
session id is its native one, the condition for a move, is stated by
none of them; the row checks each by hand before its declaration says
`Moves`.

### 5. The transcript

A protocol session keeps its conversation as typed **entries** in a
bounded ring in memory (`transcriptRing`, the people's chat's pattern).
Conductor writes no transcript to disk and never opens the agent's own
transcript files (`docs/plans/history.md`, Why and what happens: "never
opens an agent's own transcript files").

Every entry:

| Field | Meaning | Bound |
|---|---|---|
| `seq` | its place in the session, from 1, rising by one, never reused in the session's life (a move included) | uint64 |
| `rev` | 0, raised each time a streaming entry is revised | uint32 |
| `at` | when the session applied it (RFC 3339, milliseconds) | |
| `kind` | `prompt`, `text`, `tool`, `file`, `permission`, `turn`, `notice` | closed set |
| `turn` | Conductor's count of the session's turns, from 1 | uint32 |
| `ref` | the agent's own id for the thing (a message's, a tool call's), opaque | 128 bytes |
| `parent` | the `ref` of the tool step a subagent's entry belongs to | 128 bytes |

By kind:

- **`prompt`**: a message sent into the conversation. `by` (`person`,
  `conductor`), `byName` (40 runes), `text` (the message, 4 KiB inline),
  `state` (`written`, `accepted`, `unconfirmed`, `queued`). A message
  that answers a question carries `answers` (the question's id) and no
  `text`: its text is shown to its sender by their own browser alone,
  the people's chat's rule (`docs/protocol.md`, Chat; #364).
- **`text`**: the agent's words. `text` at most 4 KiB an entry, cut at a
  character boundary; a longer message continues in the next entry with
  the same `ref`. Streamed: revised (`rev`) at most once per 100 ms per
  entry, closed by `final`.
- **`tool`**: a tool step. `tool` (the tool's name, 100 bytes),
  `summary` (1 KiB: the command line of a command, the path of a file
  tool, else the input's compact JSON cut), `inputBytes`, `inputSha256`
  (design 7's digest), `status` (`running`, `ok`, `error`, `denied`,
  `interrupted`), `exit` (the exit code where the agent gives it, else
  absent), `durationMs` (from the agent where it gives one, else from
  `ToolStarted` to `ToolEnded` on the session's clock, with `measured:
  "conductor"`), `outputBytes`. The whole input and output are **parts**
  (below), fetched when a person opens the row.
- **`file`**: a file a tool changed. `path` (relative to the session's
  folder, 1,024 bytes; absent for a path outside it, with `outside:
  true`), `op` (`create`, `edit`, `delete`), `added`, `removed` (lines,
  as the tool's input gives them), `tool` (the step's `ref`). The diff
  is a part. A change a shell command made is no `file` entry: the
  agent's stream does not say it, and Conductor does not guess.
- **`permission`**: design 7's request. `question` (the session
  question's id), `call` (the agent's id of the call), `tool`,
  `summary`, `cwd`, `inputBytes`, `inputSha256`, `choices` (ids:
  `allow_once`, `deny`), `state` (`open`, `allowed`, `denied`,
  `withdrawn`), `byName` (who answered), `why` (fixed words for a deny
  or a withdrawal).
- **`turn`**: `phase` (`start`, `end`); at its end `outcome` (`done`,
  `interrupted`, `error`, `limit`), `durationMs`, `usage`
  (`inputTokens`, `outputTokens`, `costUsd` where the agent reports
  them).
- **`notice`**: `code` and fixed `words` (500 bytes): a move between
  ways, a fallback to the terminal, a restarted child, an error the
  agent reported.

**Parts.** A tool's whole `input` and `output` and a file's `diff` are
kept beside the ring, each at most 256 KiB (cut with `cut: true` and the
size left out), all of a session's parts at most 8 MiB, the oldest
dropped first. A dropped part is answered `transcript_gone`.

**The ring.** At most 1,000 entries and 2 MiB of entries, the oldest
dropped first; an entry dropped is answered `transcript_gone`. Kept
while the session is listed, gone with it.

**Why this is enough for later rows.** A finished piece of work can be
computed from the entries alone, in `seq` order, which is the order the
agent's stream gave them (I5): which commands ran and how they ended
(`tool` entries with `summary`, `status`, `exit`), which files changed
and by how much (`file` entries), what was asked and who allowed it
(`permission` entries with `state` and `byName`), and whether a test
command ran after the last edit (a `tool` entry after the last `file`
entry, within the turn's bounds). What the agent did not report
(a change made by a command, an exit code Claude Code does not
structure) is absent, never invented; a later row reads such a fact
from git or the checks, which already exist.

### 6. The wire

Three places together (`internal/proto`, `web/app/utils/protocol.ts`,
`docs/protocol.md`), each frame and message with its bound and a wire
test (`internal/proto/transcript_test.go`, `testdata/transcript-max.json`).

- **A new frame type, TRANSCRIPT (0x0B), owner to client.** Its payload
  is one JSON object, at most `MaxTranscript` (128 KiB), chunked over a
  data channel by CHUNK as a DECISION frame is:
  - `{"t":"entry","entry":{...}}`: one entry, new or revised, at most
    `MaxTranscriptEntry` (12 KiB) encoded without HTML escaping;
  - `{"t":"page","reqId":"...","entries":[...],"first":n,"last":n,"more":bool}`:
    a reply to `transcript_get`, at most 100 entries;
  - `{"t":"part","reqId":"...","seq":n,"name":"output","offset":o,"total":t,"data":"...","cut":bool}`:
    a piece of a part, at most 96 KiB of data.
  `limitFor` gains it; `DecodeRelay` takes it as relayable.
- **Client to owner, new CONTROL messages:**
  - `transcript_get {reqId, after?, before?, limit?, part?: {seq, name,
    offset}}`: `reqId` 1 to 32 printable ASCII, `limit` at most 100,
    `name` one of `input`, `output`, `diff`; at most 4 in flight per
    connection, past which `error{too_many_requests, requestId}`; every
    role.
  - `interrupt {reqId}`: Stop this turn; controllers; answered
    `interrupt_ok{requestId}` or `error{not_protocol|no_turn|read_only}`.
  - `permission_answer {reqId, question, call, sha256, choice, message?}`:
    design 7's bounds; answered `answer_ok{requestId}` or
    `error{question_answered|stale_request|not_answerer|read_only}`.
- **`submit` keeps its message and bounds** (`MaxSubmit`, 4096 bytes)
  and, on a protocol session, its meaning becomes "sent as a message":
  `submit_ok` once the agent acknowledged it, `error{unconfirmed}` past
  `AcceptWait`, `error{queued}` never (a queued message is answered
  `submit_ok` at its acceptance).
- **The resume point.** `hello` gains `transcriptAfter` (uint64,
  optional). The owner replays, after `ready` and the activity and chat
  replays, the entries after it that the ring still holds, within a
  budget of 256 KiB; past the budget, or when the ring no longer holds
  `transcriptAfter + 1`, it sends the newest that fit with `more: true`
  and the client pages back with `transcript_get before`. A reconnect
  replays only what it lacks.
- **`welcome` gains** `way` (`"protocol"`; absent for a terminal
  session, as from an older owner), `transcript {first, last}`,
  `interrupt: true` and `permissionAnswer: true`. A protocol session
  sends no SCROLLBACK and `scrollbackBytes` 0.
- **New error codes:** `not_terminal`, `not_protocol`, `no_turn`,
  `transcript_gone`, `stale_request`, `not_answerer`, `permission`
  (an `answer` naming a permission request: "This request is answered
  with its card, which names the call"), `unconfirmed`.
- **The registered word.** `proto.Registered` gains `Transcript bool`
  ("the server takes TRANSCRIPT in relay envelopes, and its join page
  draws a conversation"), absent from an older server. A protocol
  session is published to a switchyard only when its `registered` says
  `transcript`; otherwise it is not published, and Settings' switchyard
  line says "Conversations are not published: the switchyard is older
  than this server". A host never sends TRANSCRIPT (hosts stay terminal
  only, design 10), so no older server meets one from a host.
  `ProtoVersion` does not move.
- **The relay's guard becomes an allow list.** For a viewer whose role
  is not control, `RelayToHost` passes CONTROL whose `t` is `hello`,
  `ping`, `chat` (the host refuses one that is for the agent),
  `file_get` or `transcript_get`, and nothing else: INPUT, FILE_WRITE,
  DECIDE and every other CONTROL are refused `read_only`, so a message a
  later row adds fails closed. The host's and the session's own checks
  stay the first line.

### 7. The permission request (the boundary)

This is a change at "who may answer or act" (AGENTS.md, Working an
issue): its own row (row 5), its own pull request, an adversarial read
of its own. It covers every protocol session in the first cut, a
person's and a managed attempt's.

**The path, Claude Code.** Claude Code calls Conductor's MCP tool
`permission` (`--permission-prompt-tool mcp__conductor__permission`).
`conductor mcp` posts the call to `POST /api/sessions/{id}/permission`
with the session's agent token (the attention route's authentication,
`agentTokenOK`): `{call, tool, input}`, at most 256 KiB, and holds the
request open until the server answers `{behavior, message?}`, which it
returns as the documented result text. The tool's description tells the
model it is Conductor's and not to be called; a call that is not a
pending call of the session is denied (the binding, below).
**The path, Codex and the Agent Client Protocol.** The driver's `Read`
meets the request in the stream and calls `ProtocolEvents.Permission`,
which blocks on the same session code; its verdict is written back as
the protocol's reply to that request id.

**What the session does with a request** (`Local.RequestPermission`):

1. **It must name a call the stream announced.** Taken only when the
   protocol way has seen, in this turn, a tool call with that `call` id
   (or, where the agent's call carries none, exactly one pending call)
   whose name equals `tool` and whose canonical input's digest equals
   the request's, with no result yet, and whose tool is not
   `mcp__conductor__permission` itself. One request per call: a second
   for the same call is denied "Already asked". Anything else is denied
   "Not a call this session is making".
2. **The floor.** A call Conductor denies whatever the mode, before any
   person sees it: a file tool's path, or a command's text naming a
   path, under the server's deny list (`session.DenyList`: the data
   directory, the configuration, the TLS keys, the hooks directory, the
   machine's directory). Denied "Conductor denies this call whatever
   the mode: it reaches Conductor's own files". Calls the agent does not
   ask about meet the same list through the deny rules and the
   `PreToolUse` hook Conductor passes in `--settings` (documented: "A
   call that a deny rule refuses stays refused", "A call that a
   `PreToolUse` hook blocked stays blocked",
   https://code.claude.com/docs/en/permissions.md); row 5 checks both by
   hand in Manual mode and in a yolo launch.
3. **The bound.** At most `MaxPendingPermissions` (8) open per session;
   past it, denied "Conductor holds 8 unanswered requests for this
   session; this one was denied. Ask again once those are answered."
4. **The question.** Each request is a session question of kind
   `permission`, source `protocol`, with an id of its own and no quick
   replies; the attention shows the oldest open one. Each has a
   `permission` entry in the transcript carrying the question's id, the
   call, the tool, the summary, the folder and the digest; the whole
   input is a part.
5. **The digest.** `inputSha256` is SHA-256 over the tool's name, a
   newline, and the input's canonical JSON (decoded with numbers kept as
   written, objects' keys sorted, no insignificant space), computed by
   the server from the bytes the agent sent.

**Who may answer** (the default of Decisions, 2): the operator alone,
that is the workbench token's own windows and an identity provider's
principal holding `admin`, through `permission_answer` on the session's
socket. Refused `not_answerer`, with the card drawn without buttons:
a share link's viewer of any role, a decision link, a decide principal
(it has no live connection, `docs/plans/decide-grant.md`), Brakeman, the
asking agent and its token. `AnswerAs` (a registered decision router's
held answer, a provider's `ext.Host.Submit` with a question) and the
`answer` message are refused `ErrPermissionCard` for a permission
request: a router is told of the question as today (`ext.Decision.Kind`
`permission`) and its answer is recorded refused, never applied. A share
link with control keeps answering a terminal session's dialog as it
does today; nothing changes there.

**The answer is bound where it is written.** `Local.AnswerPermission`
claims the question (the claim of `docs/protocol.md`, Answering a
question), then, under the session's lock and at the moment the verdict
is handed to the waiting request, checks: the question is still open
and this claim holds it; `call` equals the request's; `sha256` equals
the request's digest. Any difference answers `stale_request` and leaves
the request open. The verdict and who gave it are recorded (the question's
kept result, an activity entry, the `permission` entry's `state` and
`byName`) before it is handed back (I9).

**The choices.** `allow_once` and `deny` (with an optional message to
the agent, 500 bytes). The first cut offers no "always": Claude Code's
documented prompt result has none, and an agent's remembered decision is
a standing grant beyond the call. No mid-session change of the agent's
permission mode is offered (it is a control request documented in one
reference only); the mode is chosen at launch, by whoever may launch.
When "always" or a mode change comes (a later row), it is the
operator's alone, and anyone who may answer may narrow.

**When nobody answers.** It waits. Where the agent would time out by
itself, Conductor answers deny first: `PermissionWait` is the bound in
effect for the agent (Claude Code: the MCP server's `timeout` Conductor
writes, 8 hours, else the documented 30-minute idle default; row 4
checks by hand which one the agent honours) less one minute; then
denied "Nobody answered in time; Conductor denied the call so the agent
does not decide alone. Ask again to put it to a person." No timer ever
answers allow.

**When the request ends otherwise.** Every open request is withdrawn,
answered deny, and its entry closed with its words:

- the agent withdrew it (an MCP cancellation, a Codex `serverRequest/resolved`,
  an ACP `session/cancel`), or a `PermissionRequest` hook of the
  person's own decided first: withdrawn "Answered by the agent's own
  rules", and the step's result in the stream says what happened;
- the turn was interrupted: withdrawn "The turn was stopped";
- the session's `Stop`, the server's stop, the child's exit: denied
  "The session ended before anyone answered", before the child's input
  is closed;
- a move between ways is refused while any request is open (design 9),
  so none is carried;
- a managed attempt's requests are its session's: the next attempt is
  a fresh session, and nothing of an answered or open request carries
  into it.

A restarted server holds no request: the children ended with it, and an
answer that arrives after has no request to reach.

**Later, a boundary row of its own:** a managed run's permission request
as a durable decision record (a ledger migration, alone and first), then
its routing, the decide grant's surface and the phone's push. Not in
this plan (Out of scope).

### 8. Yardmaster on the protocol way

The engine keeps holding `*session.Local` (`Launcher`, the lookup).
What changes, all through `Local`'s calls above:

- **The way of an attempt.** `LaunchSpec` gains `Way` (`"protocol"` or
  `""`); a run's launch takes one choice for its members, **How members
  run**: "Conversation where the agent has it" (the default) or
  "Terminal for all". A member runs the protocol way when the choice
  says so and its agent's installed version is qualified, else in its
  terminal. A managed attempt's way is fixed at its launch. No ledger
  migration: attempts are fresh sessions and the ledger keeps nothing
  new; the run's log says which way each attempt ran ("build, attempt
  2: runs as a conversation" or "in its terminal: Claude Code 2.1.200
  is not checked for conversations"). A continuation's attempts take
  the default rule again and the log says so: the launch's choice is not
  carried into a continuation (keeping it is a stored value, a ledger
  barrier, later).
- **Readiness is the session's answer.** `AwaitReady`
  (`internal/crew/run.go:2981`) waits on `Local.Ready()` for a protocol
  session, bounded by `protocolReadyCap` (60 s); past it, the launch
  fails with the driver's words. Nothing is "typed anyway" (`readyCap`'s
  rule stays the terminal's).
- **A prompt is taken when the agent acknowledges it.** `promptWith`
  (`run.go:1764`) sends the role prompt or the task line (`taskLine`,
  `task.go:3230`) by `Submit`; the member is marked prompted at
  `Accepted`, never at a write. `Unconfirmed` is the attempt's
  uncertain boundary, as a typed line with no Enter is today: it waits
  for a person and is never resent.
- **A turn's end arrives as today's attention entries.** `working` and
  `done` from `SourceProtocol` reach `OnActivity` (`run.go:2091`)
  unchanged; a `done` of kind `KindInterrupted` starts no member after
  it and sends no reminder.
- **Messages, handoffs and answers sent during a turn wait for its
  end.** `deliver` (`handoff.go:164`), a decision's applied answer
  (`typeAnswer`, `decisions.go:2937`), the done-without-result reminder
  and the decision wait's notices go through `Submit` with
  `UnlessWaiting`, which on a protocol session also waits while a turn
  runs; the deliverer's order (decision answers first) and bound (10)
  stay.
- **A turn interrupt distinct from Stop.** Stop this turn is
  `Local.Interrupt`; it ends the turn and keeps the attempt, its session
  and its conversation. Stop, cancelling a task and pausing stay what
  they are (`docs/plans/agent-harness.md`, 8: distinct actions).
- **Only a task result and its checks finish a task.** A protocol
  turn's end finishes nothing, as a `done` does not today.
- **Brakeman's turns stay in the terminal way** in the first cut (Out of
  scope); a legacy crew's members follow the launch's choice; a session
  adopted by a crew keeps its way.

### 9. The move between ways

Only for a person's own session in the first cut, only when a turn has
ended, in place: the Conductor session, its id, its links, its people,
its record and its listing stay. Never two writers.

**The route.** `POST /api/sessions/{id}/way {"to": "terminal" |
"protocol"}`, the operator's alone (`requireAdmin`, as Stop's
`DELETE /api/sessions/{id}`), refused on a switchyard.

**Refused, with words, unless all hold:**

- the session is a server session (not hosted), a person's own (no
  crew), not ended;
- its agent's catalog entry has a protocol, its driver's `Moves` is
  true, and the installed version is qualified (for a move to the
  protocol way);
- the agent's conversation id is known and resumable
  (`Info.AgentSession`), read **at the move** from the session (the
  stream's latest `session_id` for a protocol session, the hook's latest
  report for a terminal one), never a remembered one;
- the turn has ended: a protocol session's last `turn` entry is an
  `end` and no message waits for its acceptance; a terminal session's
  attention is `done` from a source that reports turns (`ReportsTurn`),
  with no question open and nothing typed held;
- no permission request is open and no submission waits.

**The steps**, in the session's own order:

1. The session marks itself moving: every submission, answer and
   interrupt is refused `moving` until the move ends.
2. It stops the current child (a protocol child's input closed, then
   `Stop`; a terminal's `Stop`) and waits for its exit and its group's
   end: the old process proven gone, never a timer (I11).
3. The conversation stays this session's throughout: `takeConversation`
   finds it held by a live session (the registry's `Holders`), so no
   other resume can take it meanwhile.
4. It starts the other way through the same Runtime, folder, binding
   (`docs/plans/capacity.md`: a resume keeps its login binding) and
   environment, with the session recipe's `ResumeArgs` of the id read in
   the conditions (`SwapWay`, built on the parts `createLocalSession`
   and `NewLocal` already have: the terminal's screen watchers and pump
   are started by a function both constructors call).
5. It broadcasts `status` and a `way` control message; viewers draw the
   other view. The terminal's ring and screens begin empty with the new
   process. The transcript stays, with a `notice` entry at the boundary:
   "Continued in the terminal" or "Continued as a conversation: turns
   made in the terminal are not shown here".
6. If the new start fails, the session tries once to resume the way it
   came from; if that fails too, it ends with both failures' words, and
   the conversation stays resumable with Resume.

**Per agent:** Claude Code by its documentation (sessions.md, above) and
a by-hand observation in row 7's acceptance, both directions, before
its `Moves` is set; Codex after its own observation in row 10; an Agent
Client Protocol agent only where its session id is shown to be its
native one. A resume of a Claude Code session running as a background
session attaches to it or exits 1 (sessions.md); Conductor never starts
one with `--bg`, and a resume that meets one is refused "A background
session of Claude Code holds this conversation".

No live hand-off of a running turn, and no fork: a fork makes a second
conversation, and a hand-off of a running turn makes two writers.

### 10. Another machine, sync, history and export

- **Hosts.** Server sessions first. `conductor host` offers the terminal
  way only, and a hosted protocol session is a later boundary row (the
  host link). Hosts never send TRANSCRIPT.
- **The switchyard.** A server's protocol session is published only to
  a switchyard whose `registered` says `transcript` (design 6), and the
  switchyard carries its frames opaquely, as it carries output.
- **Sync** (Decisions, 3): a protocol session's transcript counts as
  captured output (`docs/plans/sync-seams.md`, What counts as terminal
  output: the bytes Conductor captured from a session): it is never
  handed to a provider, and every byte the driver captured is fed to the
  copied-output check through `Options.OnOutput`, as a terminal's output
  is (`internal/api/output_guard.go:151`), so a paste of it is withheld
  or cut as a paste of a terminal's would be. Nothing is added to any
  sync allowlist.
- **History** (Decisions, 4): the journal keeps today's kinds only. A
  protocol session's tool steps and file changes reach it as the
  activity entries they are (`tool_use`, `file`); the agent's words are
  not kept; what a person sent is never kept, as today
  (`internal/journal/record.go`, its package comment). The record's launch gains `way`
  (absent for a terminal session), so a resume from a record starts the
  way the session last ran.
- **Export.** Nothing is written to disk, so nothing is exported or
  left out.

### 11. The app's screens, first cut

- **The connection is split from the terminal.** A composable,
  `useSessionConnection`, holds what `TerminalView.vue` holds today: the
  transport, the control handling (`TerminalView.vue:573-648`), the
  requests (files, saves, Neovim, chat, answers). `TerminalView` becomes
  a consumer of its output and size; the page, the Files tab, the
  people's chat and the reply bar read the connection. The marks the
  browser tests read (`data-transport-state`, `data-cols`,
  `data-fit-passes`, the xterm DOM) stay where they are: no proof moves.
- **The header's switch.** A two-part control, **Conversation ·
  Terminal**, after the session's name, for an agent whose entry has a
  protocol. The side the session runs is selected. The other side is the
  move (design 9): pressing it asks "Continue in the terminal?" (or "as
  a conversation?") and is enabled only when the move's conditions hold,
  with a tooltip naming the one that does not. Shown to a link's viewer
  as a label of the way, with no action. Below `sm` it is two items in
  More (the header keeps Back, name, Chat and More).
- **The conversation view**, `ConversationView.vue`, in the body's main
  pane where `TerminalView` is: the entries in `seq` order, sticking to
  the bottom as `ChatThread` does; a person's prompt as `ChatThread`'s
  message row; the agent's words through `renderMarkdown` (raw HTML
  never rendered) and `CodeBlock`; a **tool-step row**
  (`ToolStepRow.vue`: an icon, the tool and its summary in the code font,
  its status as `StatusMark` words, folded; opened, its input and output
  read on demand by `transcript_get part`); a **small inline diff**
  (`InlineDiff.vue`: at most 40 lines of the part, no Monaco, "Open in
  the editor" through `openDiff`); the **permission card**
  (`PermissionCard.vue`: amber, the tool, the whole input in the code
  font, read whole before Allow once is enabled, the folder, Allow once
  and Deny for the operator; for anyone else the card and "Only the
  operator answers this request"); the turn's line ("Working for 12 s",
  from the `turn` entry's time on the page's clock) and its end; a
  `notice` as a system line.
- **The composer** is `ChatComposer`'s, bound to 4,096 bytes, its Send
  the work area's one filled button, with **Stop this turn** (an
  `IconButton`, words in its tooltip) while a turn runs. A message sent
  during a turn shows "Queued: sent when the turn ends"; one not
  acknowledged shows "Sent; the agent has not acknowledged it", never
  sent again.
- **What stays.** The people's chat (rail at xl, sheet below), Activity,
  Files, Changes, the reply bar for a question that is not a permission
  request (for one, the bar says "Open the request" and scrolls to its
  card), Share, Stop session.
- **The launch dialog** gains **How it runs** (Conversation · Terminal)
  for an agent with a protocol, with the fallback's words when the
  installed version is not qualified; a crew launch gains **How members
  run**. The first message of a conversation is its first message, sent,
  never typed: "Sent to the agent as the conversation's first message".
- **Tiles.** A protocol session's tile (`SessionTile.vue`) shows "Runs
  as a conversation" and its last line of words, not a terminal.
- **Design rules.** Nuxt UI components; the brand palette; amber only
  for the card that waits on a person; the code font for tools, paths,
  commands and outputs; nothing cut that must be read, nothing sideways
  at 1440 x 900, 1280 x 720 and 390 x 844 (the phone's own conversation
  design is later).
- **The stub agent that speaks the real wire.** `web/e2e/stub-protocol.mjs`,
  Node with no dependency, resolved by a new `@NODE@` placeholder in
  `renderConfig` (`web/e2e/server.ts:194-195`). It speaks Claude Code's
  stream-json as row 4's fixtures record it, scripted by a turns file
  the spec writes (`STUB_PROTO_TURNS`: prompts matched, words to stream,
  tool steps with their inputs and outputs, a file it really writes, a
  permission it asks for by calling `conductor mcp`'s `permission` tool
  as Claude Code would, how the turn ends) and held at a gate file
  (`STUB_PROTO_GATE`) so a spec holds a turn running through a seam,
  never a clock. It keeps its conversation in a file under its HOME by
  session id and takes `--resume`, so the move is tested with the bash
  stub resuming the same file in its terminal mode. Catalog entries in
  `web/e2e/conductor.e2e.json`: `claude-proto`, `asker-proto`,
  `proto-long`.

### 12. What a share link sees

A protocol session's viewer through a link sees what a terminal's viewer
sees today, as a conversation: a view link and a control link alike see
every entry (prompts, the agent's words, tool steps and their outputs on
open, file changes and their diffs, permission cards with their state).
A prompt that answers a question shows no text to anyone but its sender
(#364's rule), and no typed answer is echoed to anyone. A control link
may send messages and Stop this turn, as it may type and press keys in a
terminal today. No link answers a permission request (design 7) or moves
a session (design 9). A decision link is a control link here. A decide
principal has no live connection and sees nothing of a transcript. The
Share dialog says, for a protocol session: "People with this link read
the whole conversation, the tools' inputs and outputs included."

### 13. Errors and their words

| Where | Words |
|---|---|
| `Input`, `resize` on a protocol session (`not_terminal`) | "This session runs as a conversation: it has no terminal to type into or size." |
| `interrupt` on a terminal session (`not_protocol`) | "Stop this turn is for a conversation; press the agent's own key in its terminal." |
| `interrupt` with no turn (`no_turn`) | "The agent is not in a turn." |
| a launch's fallback (activity entry, launch dialog) | "Claude Code <version> is not a version Conductor has checked for conversations; this session runs in its terminal." |
| a start that never became ready | "Claude Code did not start a conversation: it exited with status <n> before it was ready." or "...: it said nothing it could take a message after within 60 s." |
| a signed-out agent (its stream's error) | "Claude Code is not signed in. Sign in by opening Claude Code in a terminal; Conductor never asks for or keeps your sign-in." |
| a line past the bound, or one the driver cannot read | "Claude Code sent a line Conductor cannot read; the session was ended. Resume it to continue the conversation." |
| a message not acknowledged (`unconfirmed`) | "Sent; the agent has not acknowledged it. Conductor does not send it again." |
| a message during a turn | "Queued: sent when the agent's turn ends." |
| the move refused (`move_refused`) | "The agent is in a turn: move the session once the turn has ended." / "A request waits for an answer: answer it first." / "A crew member's way is set when its run launches." / "Conductor has not established that <agent> resumes a conversation the other way." / "A hosted session runs in its terminal." |
| a permission answer from a non-answerer (`not_answerer`) | "Only the operator answers this request." |
| a permission answer that no longer matches (`stale_request`) | "This request changed or was answered; read it again." |
| denies the agent is told (permission result) | "Conductor denies this call whatever the mode: it reaches Conductor's own files." / "Conductor holds 8 unanswered requests for this session; this one was denied. Ask again once those are answered." / "Nobody answered in time; Conductor denied the call so the agent does not decide alone. Ask again to put it to a person." / "The session ended before anyone answered." / "Not a call this session is making." |
| `transcript_gone` | "That part of the conversation is no longer kept." |
| an older switchyard | "Conversations are not published: the switchyard is older than this server." |

## Sign-in and what the vendors' own pages say

**The rule Conductor keeps, every way, every agent.** Conductor runs
the person's own installed, unmodified program on their own machine,
with whatever sign-in that program already has. It never asks for,
stores or passes a vendor credential or token (it reads no
`setup-token`, no `auth.json`, no keychain, and passes none in the
environment it builds). It offers no vendor sign-in of its own: a
signed-out agent is told to the person in words that send them to the
program itself. It names itself honestly to the program (Codex's
`clientInfo.name` is `conductor`; the Agent Client Protocol's
`clientInfo` is Conductor's), and it sets nothing that names another
client. It runs no hosted agent and routes no agent's traffic through a
service.

**What the vendors' pages say** (quoted from the addresses given, read
2026-11-24; this plan draws no legal conclusion, and its authors are not
lawyers):

- **Anthropic, Claude Code.**
  - "Unless previously approved, Anthropic does not allow third party
    developers to offer claude.ai login or rate limits for their
    products, including agents built on the Claude Agent SDK. Use the
    API key authentication methods described in the Quickstart
    instead." (https://code.claude.com/docs/en/agent-sdk/overview.md)
  - "Anthropic does not permit third-party developers to offer Claude.ai
    login into their own applications, or to route requests through
    Free, Pro, Max, or Ultra plan credentials on behalf of their users.
    Moreover, developers may not collect, store, or intermediate
    Claude.ai credentials or session tokens" and "Nor does it prevent an
    end user from signing in to the unmodified Claude Code binary with
    their own Claude subscription"
    (https://code.claude.com/docs/en/legal-and-compliance.md).
  - The same page: "The Claude Code binary must not be modified" and
    "Customers may not pay for, resell, or intermediate Claude usage on
    their end users' behalf"; "Advertised usage limits for Pro, Max, and
    Ultra plans assume ordinary, individual usage of Claude Code and the
    Agent SDK"; and on names: "You can accurately say, in plain text,
    that your product has Claude Code preinstalled or that it runs
    Claude Code. But you can't use the Claude Code or Anthropic names or
    logos as part of your own product, feature, or company name".
  - The headless page calls `claude -p` "the Agent SDK via the CLI"
    (https://code.claude.com/docs/en/headless.md).
  - No sentence read addresses a local tool that drives the person's own
    installed, signed-in `claude`, by its terminal or by `-p`.
- **OpenAI, Codex.**
  - "App-server authentication has never been permitted for commercial
    or hosted services." (https://learn.chatgpt.com/docs/app-server.md,
    Auth endpoints, a section about the app server's sign-in methods)
  - "These docs explain ChatGPT plan usage for open-source and locally
    hosted apps. If you're interested in offering it in a paid or
    remotely hosted app, complete the interest form."
    (https://developers.openai.com/siwc/token-sharing-open-source.md)
  - "You may not share your account credentials or make your account
    available to anyone else" and, under what you cannot do,
    "Automatically or programmatically extract data or Output"
    (https://openai.com/policies/terms-of-use/).
  - No sentence read addresses a local tool driving the person's own
    signed-in `codex`.
- **Google, Antigravity.** "Antigravity CLI headless mode (`agy -p`) is
  intended for direct scripting and CI workflows. Wrapping `agy` inside
  third-party coding agents or applications, or automating account
  rotation, is not supported under consumer plans."
  (https://antigravity.google/docs/faq.md). This bears on today's
  terminal way too, which Conductor already offers for `agy`.
- **GitHub, Copilot CLI.** Its licence covers the program; "Your use of
  each service is subject to the terms ... applicable to that service"
  (`github/copilot-cli`, `LICENSE.md`, section 8). Its ACP page invites
  custom clients and calls the server a public preview.

**Two facts a person should know before choosing the protocol way.**
Headless use may be accounted differently from interactive use: an open
report in Claude Code's public tracker says `claude -p` "uses ~1.8×
more of the 5-hour window per token than interactive" use
(anthropics/claude-code#97074; a user's report, not the vendor's word).
And the vendor has said `-p` will later default to a mode that "never
reads OAuth credentials" (headless.md); the gate turns that into a
fallback to the terminal, in words (design 4). The launch dialog's help
says both in one line each.

**What is the maintainers' decision.** Whether to build on each agent's
interface is the maintainers' (Decisions, 5). Open: Claude Code's
`-p` interface (the plan's default: build it, rows 1 to 9); Codex's
app server (the default: build only after the maintainers have read the
sentences above and said yes, row 10); each Agent Client Protocol
agent's declaration (the default: the client is built, row 11, and an
agent is declared only after its own terms are read; Antigravity is not
declared while its FAQ reads as it does).

## Events, orderings and invariants

**A protocol session's life:**

| From | Event | To |
|---|---|---|
| (none) | launch: `StartPipes` returned, `NewProtocolLocal`, `Begin` called | `starting` |
| `starting` | `Begin` returned nil | `ready` (`Ready()` closed) |
| `starting` | `Begin` failed, the shape check failed, or `protocolReadyCap` passed | ended with words; for a person's launch, the terminal way started on the same id (design 3) |
| `ready` | `Send` written, then `Accepted` | `in turn` once `TurnStarted` (attention `working`) |
| `in turn` | `Permission` request taken | `in turn` (attention `needs_input`, kind `permission`) until answered or withdrawn |
| `in turn` | `TurnEnded` | `ready` (attention `done`, or `done` kind `interrupted`); queued messages sent in order |
| `ready` | the move (design 9) | `moving`, then the other way's start |
| any | `Stop`, the child's exit, a line it cannot read | pending requests denied, then ended |

**Invariants**, each a sentence a test holds:

- **I1. One writer per conversation.** While a session's child runs, or
  while it is moving, no other session can start a process on its agent
  conversation: `takeConversation` refuses, and the move starts the new
  child only after the old one's exit and its group's end.
- **I2. The terminal way is unchanged.** With no protocol session, every
  terminal session's frames, attention, submissions and records are
  `main`'s, and every existing test passes unchanged.
- **I3. Terminal-only calls never write to a protocol child.** `Input`,
  `Resize` and `ResizeWith` on a protocol session return `ErrNotTerminal`
  and write nothing; the connection stays open.
- **I4. A message is sent once.** A message is written to the agent's
  input at most once; it is marked accepted only by the agent's
  acknowledgement naming it, and one not acknowledged within
  `AcceptWait` is marked unconfirmed and never written again by anyone.
- **I5. Entries are in the stream's order.** Entries' `seq` rise by one
  in the order the driver handed the events, which is the order the
  agent's output gave them; a revision keeps its `seq`.
- **I6. State before viewers.** Each event changes the session's state
  (attention, the question, the ring) before any viewer is sent its
  frame, so a viewer that acts on a frame meets the state that frame
  shows.
- **I7. No screen rule fires on a protocol session.** Attention from
  `SourceProtocol` is never overridden or withdrawn by a rule that reads
  a screen.
- **I8. A permission request names a call the stream announced.** A
  request is taken only for a call of the current turn the stream
  showed, with the same name and input digest, not yet resulted, one per
  call; every other request is denied.
- **I9. An answer reaches only the request it was given for.** A verdict
  is handed to a request only when the question, its claim, the call id
  and the digest all match at the moment of the hand-over, and only
  after who answered is recorded.
- **I10. Only the operator answers a permission request.** No share
  link, decision link, decide principal, router, provider, Brakeman or
  agent token reaches a verdict, by any transport.
- **I11. Nothing silent allows.** No timer, bound, end, restart or move
  ever answers a permission request allow; each answers deny or
  withdraws, with its words.
- **I12. A turn's end finishes no task.** A protocol turn's end, done
  or interrupted, never finishes a task or starts a dependent of a
  managed run; an interrupted turn starts no member of a legacy run.
- **I13. The transcript never leaves the machine.** No sync document,
  provider call, export or journal entry carries an entry's text or a
  part; every captured byte reaches the copied-output index.
- **I14. A frame an older peer would close on is never sent to it.**
  TRANSCRIPT goes to a switchyard only after its `registered` said
  `transcript`; no host sends it.
- **I15. A view role writes nothing through the relay.** A relayed
  viewer whose role is not control passes only `hello`, `ping`, `chat`,
  `file_get` and `transcript_get`.
- **I16. The way a session runs is said.** `Info.Way`, the welcome's
  `way`, the run's log and the record's launch name it; a fallback says
  why in words.
- **I17. Conductor holds no vendor credential.** No route, environment,
  record or log of a protocol session carries a vendor's credential or
  token.

## The rows

Each row is a unit of work built with the checks of its surfaces
(`docs/checks.md`) and leaves `main` green. Grouping into pull requests
follows `AGENTS.md`: **PR-A** is row 1 alone (a seam of the public
`ext`); **bundle 1** is rows 2, 3 and 4, with row 3's relay allow list a
part of its own with an adversarial review of its own (the links); **PR-B**
is row 5 alone (who may answer), with an adversarial read; **bundle 2**
is rows 6, 7, 8 and 9; rows 10 and 11 are later pull requests, each
after the maintainers' word on its agents' terms. No row migrates the
ledger.

Sizes are hours of one builder's work with its review and CI, from
comparable merged rows: a wire change across `internal/hostagent`,
`internal/notify` and `internal/api` of about 2,000 to 3,500 lines took
3.5 to 4 hours from first commit to merge (#978, #981); a new question
kind across `internal/catalog` and `internal/session` about a day
(#958); an end-to-end walk on existing seams about 8 hours (#1045); a
page's new flow of about 750 lines about 8 hours (#962); a bundle of
screens with its specs, several builders, about 5 hours of wall time
(#994).

| Row | Title | Touches | Depends on | Boundary | Size |
|---|---|---|---|---|---|
| 1 | ext, proc, local, exttest: a child with pipes beside the Runtime | `ext`, `ext/local`, `ext/exttest`, `internal/proc`, `internal/session/sessiontest/fakeruntime.go`, `docs/plans/runtime.md` (one sentence), `docs/repository-map.md` | none | yes (an `ext` seam): PR-A alone | 6 h |
| 2 | session: the protocol way against a fake child, the transcript ring, the refusals | `internal/session`, `internal/session/sessiontest` (a fake protocol child) | none (beside row 1) | no | 12 h |
| 3 | proto, session, api, hostagent, signal, web: TRANSCRIPT, the resume point, the registered word, the relay's allow list | `internal/proto`, `internal/session` (attach), `internal/api` (`ws_viewer.go`, `ws_host.go`), `internal/hostagent` (`peer.go`, `uplink.go`), `internal/signal/hosted.go`, `web/app/utils/protocol.ts`, `web/app/utils/transport/`, `docs/protocol.md` | row 2 | the allow list: a part with its own adversarial review | 8 h |
| 4 | agentproto, agents, catalog, api, cli: the Claude Code driver, its gate and fixtures, the launch, the stub agent | `internal/agentproto` (new), `internal/agents` (the protocol MCP config and settings assets), `internal/catalog` (the field, Claude Code's entry), `internal/config`, `internal/api` (`sessions.go`, `catalog.go`), `internal/cli/mcp.go` (the tool's listing only), `web/e2e/stub-protocol.mjs`, `web/e2e/server.ts`, `docs` | rows 1, 2 | no | 12 h |
| 5 | session, api, cli, proto: the permission request | `internal/session`, `internal/api` (the route, `router.go`, `exthost.go`, `inbox.go`), `internal/cli/mcp.go` (the tool), `internal/agents` (the floor's settings), `internal/proto`, `web/app/utils/protocol.ts`, `docs/protocol.md` | rows 3, 4 | yes: PR-B alone, adversarial read | 12 h |
| 6 | web: the connection split from the terminal, the conversation view, the header switch, the permission card | `web/app` (`useSessionConnection.ts`, `TerminalView.vue`, `ConversationView.vue`, `ToolStepRow.vue`, `InlineDiff.vue`, `PermissionCard.vue`, the session page, `SessionTile.vue`, `utils/transcript.ts`), `web/e2e`, `docs/features.md` | row 5 | no | 16 h |
| 7 | session, api, web: the move between ways for a person's own session | `internal/session` (`SwapWay`, the terminal start extracted), `internal/api` (the route), `web/app` (the switch's move), `web/e2e`, `docs` | rows 4, 6 | no | 10 h |
| 8 | catalog, api, journal, web: the declaration's carry, the launch choice, the digest, the record | `internal/catalog`, `internal/api` (`catalog.go`, `dispatch_plans.go`, `history_journal.go`), `internal/journal`, `web/app` (`LaunchSessionModal.vue`, `utils/agentForm.ts`, `useSessions.ts`), `web/e2e`, `docs` | row 4 (beside row 6) | no | 6 h |
| 9 | crew, api, web: Yardmaster on the protocol way, with a live crew | `internal/crew` (`run.go`, `task.go`, `handoff.go`, `decisions.go`, `messages.go`), `internal/api` (`runs.go`), `web/app` (How members run), `web/e2e`, `docs/protocol.md` (run log rows) | rows 5, 8 | no | 14 h |
| 10 | agentproto, catalog: Codex's app server | `internal/agentproto/codex.go`, `internal/catalog/defaults.go`, `web/e2e` (a second stub mode) | row 9; the maintainers' word on Codex's terms | no | 16 h |
| 11 | agentproto, catalog: the Agent Client Protocol client and its first native agents | `internal/agentproto/acp.go`, `internal/catalog/defaults.go`, `web/e2e` | row 9; the maintainers' word per agent | no | 16 h |

Rows 1 to 9, the first cut: about 96 hours of builders' work.

**Built at the same time.** Row 1 beside row 2 (they share only
`sessiontest`, in different files; the second to merge rebases). After
row 2: row 3 beside row 4 (both touch `internal/api`, in different
files; built in worktrees of their own feeding bundle 1's branch). After
row 5: row 6 beside row 8 (row 8's launch dialog and row 6's session
page are different files). Row 7 after row 6's connection split. Row 9
after rows 5 and 8. Rows 10 and 11 beside each other, after row 9.

### Row 1: a child with pipes beside the Runtime

**Scope.** `ext.PipeStarter`, `ext.PipeSpec`, `ext.Pipes` (design 2);
`proc.StartPipes` (two pipes, its own group, stderr counted and
dropped, `Stop` with grace then the group's kill, reaped before it
returns); the native Runtime's implementation; `FakeRuntime`'s script
and `FakeTerminal`'s sibling `FakePipes`; `RuntimeExec`'s checks; the
boundary test's `runners` entry; one sentence in `runtime.md`'s Why and
what happens ("a session's protocol child starts through a Runtime's
optional `PipeStarter`; a Runtime without it is never handed one").
**Must not touch** `internal/session`'s `Local`, `internal/api`, the
existing `Runtime` methods.

**Tests.** Go race:
- `TestStartPipesArgvDirEnv` (`internal/proc`): the child sees exactly
  the argv, directory and environment; breaks if the parent's
  environment leaks in.
- `TestStartPipesReadsAndWrites`: a line written to stdin comes back on
  stdout; breaks if stdout is merged with stderr or buffered to the end.
- `TestStartPipesStopEndsTheGroup`: a grandchild that ignores SIGTERM is
  gone after `Stop`, within grace plus `StopKillWait`; breaks if only
  the leader is signalled.
- `TestStartPipesContextBeforeStart`: an ended context starts nothing;
  breaks if a process is left behind.
- `TestStartPipesStderrNeverBlocks`: a child that writes 64 MiB to
  stderr exits; breaks if stderr is not drained.
- `TestNativeRuntimeIsAPipeStarter` (`ext/local`): `local.Native()`
  implements it; breaks if the assertion fails.
- `TestRuntimeExecPipes` (`ext/exttest`, through `RunAll` with the
  native Runtime): the contract's checks pass; breaks on any above.
- `TestOnlyTheRunnersStartOrSignalAProcess` (the boundary test, as it
  stands, with the new runner): breaks if `StartPipes` is used outside
  `ext/local` and the fake.

No UI. **By hand:** none. **Boundary:** a seam of the public `ext`,
PR-A alone. **6 h.**

### Row 2: the protocol way inside the session, against a fake child

**Scope.** `session.Child`, `session.Protocol`, `session.ProtocolEvents`,
`session.Message`, `ToolStep`, `ToolResult`, `FileChange`, `Usage`,
`PermissionRequest` and `Verdict` types (the last two used in row 5);
`NewProtocolLocal`; the read goroutine; `Ready`, `Interrupt`, `Way`; the
transcript ring, parts and their bounds (design 5); `SourceProtocol`
in `ReportsTurn`, `KindInterrupted`; `Submit`'s protocol branch with
`AcceptWait`; the refusals `ErrNotTerminal`, `ErrNotProtocol`; `Stop`'s
order; `OnOutput` fed; activity entries for steps and files.
`sessiontest.FakeProtocol` (a driver whose events a test hands in, and
which records what it was sent). **Must not touch** the terminal path
(`pump`, the screen watchers, `submit.go`'s terminal branch), `internal/api`.

**Tests.** Go race (`internal/session`):
- `TestProtocolSubmitIsAcceptedOnce` (I4): a message written once,
  `Entered` at `Accepted`; a second `Accepted` for it changes nothing;
  breaks if `Submit` writes again on a timeout.
- `TestProtocolUnconfirmedNeverResent` (I4): no `Accepted` within a
  shortened `AcceptWait` given through `Options`: `Unconfirmed`, and the
  fake records one write; breaks on a retry.
- `TestProtocolRefusesTerminalCalls` (I3): `Input`, `Resize`,
  `ResizeWith` answer `ErrNotTerminal` and the fake records nothing.
- `TestProtocolTurnReportsAttention`: `TurnStarted` gives `working`,
  `TurnEnded` `done`, both `SourceProtocol`, `ReportsTurn` true; breaks
  if the source is not a turn source.
- `TestInterruptedTurnIsNotDone` (I12): `TurnEnded("interrupted")` gives
  kind `interrupted`.
- `TestProtocolAttentionIgnoresScreenRules` (I7): with every screen
  rule's trigger fed, a protocol `working` stays.
- `TestTranscriptOrderIsTheStreams` (I5): 1,000 events handed over by
  the fake driver, with a fake clock that runs backwards between some,
  give `seq` 1 to n in hand-over order; breaks if entries are ordered by
  time.
- `TestTranscriptBounds`: past 1,000 entries or 2 MiB the oldest go;
  past a part's 256 KiB it is cut with its size; past 8 MiB of parts
  the oldest part answers `transcript_gone`.
- `TestTextCoalescedPerEntry`: 500 deltas in 50 ms give at most one
  revision per 100 ms, through a fake clock in `Options`.
- `TestStateBeforeViewers` (I6): a sink that reads the session's
  attention as each frame arrives always sees the frame's state; the
  event is held at a seam (`afterApply`) so the old order fails every
  run.
- `TestPromptThatAnswersCarriesNoText`: a submission naming a question
  makes a `prompt` entry with `answers` and no `text` for every viewer.
- `TestProtocolStopClosesInputFirst`: the fake records `CloseInput`
  before the child's `Stop`.
- `TestProtocolOutputReachesOnOutput` (I13): every captured byte
  reaches `Options.OnOutput`.
- `TestProtocolToolStepsAreActivity`: a tool step and a file change make
  the activity entries `tool_use` and `file` the hook mappers make.
- `TestTerminalSessionUnchanged` (I2): the existing suite of
  `internal/session` passes with no change to its files.

No UI. **By hand:** none. **12 h.**

### Row 3: the wire

**Scope.** Design 6 whole: TRANSCRIPT and its bounds; `transcript_get`,
`interrupt`, the resume point, `welcome`'s fields, the error codes
(`permission_answer` waits for row 5); the protocol replay in
`AttachWith`; dispatch in `ws_viewer.go`; `peer.go` refuses the new
messages from a viewer of a hosted session (hosts run no protocol
session); `Registered.Transcript` set by the server; `Uplink` publishes
a protocol session only to a switchyard that says it; `DecodeRelay` and
`limitFor`; the relay's allow list; the browser's dispatch
(`transport/base.ts`: a TRANSCRIPT frame to `onTranscript`, the new
messages on `TerminalTransport`) and types; `docs/protocol.md`'s rows.
**Must not touch** the session's way code beyond `AttachWith`, the app's
pages.

**Tests.** Go race:
- `TestTranscriptFrameBounds` (`internal/proto`, with
  `testdata/transcript-max.json`): the largest entry fits
  `MaxTranscriptEntry`, the largest page and part fit `MaxTranscript`,
  one byte past is refused by `Decode`.
- `TestTranscriptGetBounds`: `reqId` 1 to 32, `limit` at most 100,
  `name` of the closed set; breaks if an out-of-set name is taken.
- `TestTranscriptRelayable`: `DecodeRelay` takes TRANSCRIPT; breaks if a
  newer switchyard would close a publisher.
- `TestRegisteredOmitsTranscript`: an older server's `registered` byte
  for byte without the word.
- `TestReplayAfterResumePoint` (`internal/session`): `transcriptAfter`
  of 40 with the ring holding 1 to 60 replays 41 to 60 after `ready`
  and the activity and chat replays, then live; a point the ring no
  longer holds sends the newest within 256 KiB with `more`.
- `TestTranscriptGetInFlight` (`internal/api`): a fifth request in
  flight answers `too_many_requests`; a page of a dropped range answers
  `transcript_gone`.
- `TestInterruptRoles`: a view role's `interrupt` answers `read_only`,
  a control's reaches the driver.
- `TestNoTranscriptToAnOlderSwitchyard` (I14, `internal/hostagent`): a
  protocol session is not published to a switchyard whose `registered`
  lacks the word, and Settings' line says so; with the word, its frames
  pass.
- `TestRelayAllowListForViewRoles` (I15, `internal/signal`): every
  CONTROL `t` of `proto` (listed from its constants, so a new one joins
  the test) is refused for a view role but the five; breaks if the old
  deny list is kept.
- `TestHostRefusesProtocolMessages` (`internal/hostagent`): `interrupt`
  and `transcript_get` to a hosted session answer `not_protocol`.
vitest: `transport/base.test.ts` gains `transcriptFrameDispatched` (a
TRANSCRIPT frame reaches `onTranscript`; an unknown type is still
dropped) and `transcriptChunked` (a chunked frame reassembled).
Playwright: none yet (row 6). **By hand:** none. **Boundary:** the
relay's allow list is a part with its own adversarial review (links).
**8 h.**

### Row 4: the Claude Code driver

**Scope.** `internal/agentproto` with `Names`, `Check`, the Claude Code
driver (`claude.go`: the argv of design 4, the stream's events mapped to
`ProtocolEvents`, `Begin` with the gated `initialize` or the first
`system/init`, `Send` as a stream-json user message with a ref the
replay names, `Interrupt` gated with its SIGINT fallback, the 8 MiB
input count, the version gate and the shape check); the catalog field
and its validation, Claude Code's built-in entry; the protocol's MCP
config (`claude-protocol-mcp.json`, with `timeout`) and settings
(`claude-protocol.json`, the floor's deny rules and `PreToolUse` hook)
as assets; `createLocalSession`'s protocol branch through `StartPipes`
and `NewProtocolLocal`, with the admin's `way` field on `POST
/api/sessions` (the launch dialog's choice is row 8's); the fallback to
the terminal in words; the recorded fixtures under
`internal/agentproto/testdata/claude/` (taken by hand from a real run at
`MinVersion`, with every account, organisation and email field removed;
a test fails a fixture holding one); the stub agent `stub-protocol.mjs`.
Until row 5 the permission tool is not passed, and a call that needs
approval is denied by Claude Code itself (documented: Manual `-p` with
no host "is denied unless something approves it").
**Must not touch** the terminal way's launch, the app's pages.

**Tests.** Go race:
- `TestClaudeArgvIsTheDocumentedOne` (`internal/agentproto`): the argv
  of a start and a resume, element for element; breaks if `--bare` or
  `--permission-prompt-tool stdio` appear, or `--permission-mode` is
  missing.
- `TestClaudeFixturesReplay`: each recorded fixture fed to `Read` gives
  its expected events (a turn with words, a tool step with its result, a
  file edit with counts, a result with usage); breaks when the mapping
  drifts.
- `TestClaudeUnknownEventsIgnored`: an event type and a field the
  fixtures lack change nothing.
- `TestClaudeLineBound`: a line of 8 MiB plus one ends `Read` with the
  bound's error.
- `TestClaudeGate`: a probed version below `MinVersion`, or none, starts
  the terminal way with the words; a start whose `system/init` lacks a
  needed field is ended before any message and falls back.
- `TestClaudeInterruptFallback`: below the control protocol's gate,
  `Interrupt` sends SIGINT to the child alone.
- `TestClaudeInputCountResumes`: past 8 MiB written, the next turn's end
  ends the child and resumes the same id.
- `TestFixturesHoldNoAccount`: no fixture holds an email-shaped string
  or an `account` object.
- `TestProtocolLaunchThroughPipes` (`internal/api`): a launch with `way`
  goes through `FakeRuntime`'s `StartPipes`, never `StartTerminal`, with
  the folder from `openCwd` and the environment without `TERM`.
- `TestProtocolLaunchTakesTheConversation` (I1): a second launch on the
  same conversation is refused `already_resumed`.
- `TestCatalogProtocolValidated` (`internal/catalog`, `internal/config`):
  bounds, an unknown name refused by `agentproto.Check`, `{}` turning it
  off, `inherit` and a clone carrying it.
vitest: none. Playwright: `conversation-start.spec.ts` (with
`claude-proto`): a session launched with `way` through the API reaches
`ready`, its stub turn's words arrive by TRANSCRIPT (read off the page's
transport seam until row 6 draws them); breaks if the stub's stream does
not reach the session.
**By hand, with the real agent:** record the fixtures; a launch through
the API of a real Claude Code with a signed-in login, one turn with a
tool, the stream's events in the session's activity; whether the MCP
server's `timeout` raises the idle window (design 7's `PermissionWait`),
written in the row's pull request. **12 h.**

### Row 5: the permission request

**Scope.** Design 7 whole: `POST /api/sessions/{id}/permission` (agent
token, bounded body, held open); `conductor mcp`'s `permission` tool,
listed only in a protocol session (`CONDUCTOR_PROTOCOL` set) with its
documented result; `Local.RequestPermission` (the binding to an
announced call, the floor, the bound, the question, the entry, the
digest, the withdrawals); `Local.AnswerPermission` (the operator check,
the claim, the check at the hand-over, the record); `permission_answer`
in the three places; `ErrPermissionCard` for `answer`, `AnswerAs` and
`ext.Host.Submit`; the router told and its answer refused; the inbox's
item for such a question with "Open the request" and no action;
`PermissionWait` and the deny before it; `--permission-prompt-tool`
added to row 4's argv; the floor's assets checked. The card's screen is
row 6's; this row is proved through the session's API and sockets.
**Must not touch** the terminal way's questions, DR1's records.

**Tests.** Go race:
- `TestPermissionNamesAnAnnouncedCall` (I8, `internal/session`): a
  request for a call the stream did not show, one already resulted,
  another turn's, one with another name or digest, the permission tool
  itself, and a second for one call: each denied with its words; the
  matching one taken.
- `TestPermissionFloor`: a file tool's path under the deny list and a
  command naming one are denied before any question is asked.
- `TestPermissionBound`: the ninth open request is denied with the
  bound's words.
- `TestPermissionAnswerBinding` (I9): an answer with another call id, or
  a digest of a changed input, answers `stale_request` and the request
  stays open; the matching answer is handed over once, after its record.
- `TestPermissionFirstAnswerWins`: two operator answers at once: one
  `answer_ok`, one `question_answered`.
- `TestPermissionOnlyTheOperator` (I10, `internal/api`): a view link, a
  control link, a decision link, a relayed viewer through a switchyard,
  `AnswerAs` (a router's held answer, through the router's `apply`), an
  extension's `Host.Submit`, the agent's token on the attention route,
  and a decide principal's routes: none reaches a verdict.
- `TestPermissionNeverSilentlyAllowed` (I11): the session's stop, the
  child's exit, the server's stop, an interrupt, an agent's
  cancellation and `PermissionWait` (through a fake clock) each answer
  deny or withdraw with their words; none answers allow.
- `TestPermissionAnsweredElsewhere`: an MCP cancellation of the held
  call withdraws the question with "Answered by the agent's own rules".
- `TestPermissionDigestCanonical`: the same input with keys reordered
  and spaces changed has one digest; one changed byte of a value
  another.
- `TestPermissionToolResultIsTheDocumentedOne` (`internal/cli`): the
  tool returns one text block of `{"behavior":"allow"}` or
  `{"behavior":"deny","message":...}` and no structured content.
- `TestPermissionToolListedOnlyInProtocol`: outside a protocol session
  the tool is not listed.
- `TestPermissionAnswerFrameBounds` (`internal/proto`): `question` 1 to
  32, `call` at most 128, `sha256` 64 lowercase hex, `choice` of the
  closed set, `message` at most 500.
vitest: `utils/protocol` types typechecked through their first consumer
in row 6. Playwright: row 6 carries the card's spec.
**By hand, with the real agent:** a Bash call in Manual mode answered
Allow once and Deny from the session's socket (a test page or the
CLI's raw socket); the floor's deny rules and hook in Manual mode and in
a yolo launch against a file under the data directory; a person's own
`PermissionRequest` hook deciding first. **Boundary:** yes, PR-B alone,
with an adversarial read written in the pull request: every way an
answer could reach another request (binding and replay), every actor
and transport that could reach a verdict (who answers), whether the
card shows what will run (what the request says), whether any mode or
standing grant widens what runs unasked (modes), whether any end,
restart, move or bound allows (lifetime), and whether every verdict is
recorded before it is handed over, with no secret in a log (audit). **12 h.**

### Row 6: the app's conversation view and the header switch

**Scope.** Design 11 and 12 whole: `useSessionConnection` and
`TerminalView` as its consumer; `ConversationView`, `ToolStepRow`,
`InlineDiff`, `PermissionCard`, the composer with Stop this turn, the
queued and unconfirmed lines; the header's switch (its other side drawn
as a label until row 7, in the same bundle, adds the move); the reply
bar's "Open the request"; the tile; the
Share dialog's line; `utils/transcript.ts` (the entries' parser and
bounds, the merge of revisions by `seq`, the resume point kept per
connection); the features entry.
**Must not touch** the server, the terminal's DOM marks.

**Tests.** vitest:
- `utils/transcript.test.ts`: `mergesRevisionsBySeq` (a revision
  replaces its entry, never adds one), `keepsResumePoint` (the highest
  `seq` held is the next hello's `transcriptAfter`), `refusesPastBound`
  (an entry past `MaxTranscriptEntry` is dropped with a log line),
  `promptAnsweringShowsNoText`.
- `ConversationView.test.ts`: `drawsMarkdownWithoutHtml` (an agent's
  `<img onerror>` is text), `stepOpensItsParts` (opening a row sends one
  `transcript_get part`).
- `PermissionCard.test.ts`: `allowWaitsForTheWholeInput` (Allow once is
  disabled until the part is read whole), `noButtonsForALink`.
- `useSessionConnection.test.ts`: `oneConnectionForBothViews` (the
  terminal and the conversation read one transport).
Playwright (with the stub agent):
- `conversation.spec.ts`: a turn's words, a tool step opened to its
  output, a file change's inline diff and its editor tab, the turn's
  end; Stop this turn on a turn held at the stub's gate ends it with
  "interrupted"; a message during a turn shows queued and is sent at its
  end. Breaks if the page draws entries out of `seq` order or resends.
- `conversation-permission.spec.ts` (with `asker-proto`): the card with
  the exact call; Allow once lets the stub's call run; Deny's message
  reaches the stub; a view link's page shows the card and no buttons.
- `conversation-reconnect.spec.ts`: a dropped connection resumes with
  only the entries after the last held (counted at the server's seam,
  never by the page's list length).
- `conversation-layout.spec.ts`: at 1440 x 900, 1280 x 720 and
  390 x 844, nothing sideways, the header's name whole, the composer's
  controls reachable; measured after the page's own animations end.
- `session-page.spec.ts` and the other terminal specs pass unchanged
  (I2).
**By hand, with the real agent:** a person's conversation session of
Claude Code from launch to a permission and its answer, on the desktop
app's server inside WSL and on Linux, with nothing configured. **16 h.**

### Row 7: the move between ways

**Scope.** Design 9 whole: `Local.SwapWay`, the terminal's start
extracted from `NewLocal` (called by both constructors), the route and
its refusals, the boundary notices, the one retry and the end with both
words; the switch's move in the header and More with its ask; Claude
Code's `Moves` set once the by-hand observation is recorded.
**Must not touch** a crew member's session, a hosted session.

**Tests.** Go race:
- `TestMoveWaitsForTheTurnsEnd` (`internal/api`): in a turn, with a
  request open, with a submission pending: refused with each one's
  words.
- `TestMoveProvesTheOldProcessGone` (I1): the new child starts only
  after the old child's exit and its group's end, held at a seam
  (`afterOldExit`) so starting earlier fails every run.
- `TestMoveReadsTheIdAtTheMove`: an id the stream changed after the
  launch is the one resumed.
- `TestMoveKeepsTheSession`: id, links, viewers, chat, record and
  listing unchanged; `Info.Way` changed; a `notice` at the boundary.
- `TestMoveRefusedForACrewMemberAndAHost`.
- `TestMoveFailureTriesBackOnce`: the new start fails, the old way is
  resumed; both fail, the session ends with both words.
- `TestMoveIsTheOperators`: a link's and an agent token's request
  answer `401`.
Playwright: `conversation-move.spec.ts`: a stub conversation moved to
the terminal (the bash stub resumes the same file and shows the earlier
turn), then back; the switch disabled during a turn held at the gate,
with its tooltip. vitest: `switchState.test.ts` (which condition the
tooltip names).
**By hand, with the real agent (the acceptance):** a Claude Code
conversation with two turns moved to the terminal, the terminal shows
them and takes a third, moved back, the conversation takes a fourth with
the third in its context; recorded in `docs/features.md`. **10 h.**

### Row 8: the declaration's carry, the launch choice, the digest

**Scope.** The Agents page's form carries `protocol`; `AgentInfo`,
`AgentInput`; the launch dialog's How it runs with the fallback's words;
`way` in a launch's record (`internal/journal`'s launch, omitted for a
terminal session) and a resume from a record in that way;
`agentSelection` digesting the protocol only for a protocol launch.
**Must not touch** the crew engine.

**Tests.** Go race: `TestSelectionDigestUnchangedForTerminal`
(`internal/api`: every terminal launch's digest equals `main`'s, from a
table of the existing test cases), `TestSelectionDigestNamesProtocol`,
`TestRecordKeepsTheWay` (`internal/journal`: absent for a terminal
session, an older record read as terminal), `TestResumeFromRecordInItsWay`.
vitest: `agentForm.test.ts`'s `carriesProtocol` (a save of an agent
keeps its protocol). Playwright: `launch-way.spec.ts`: How it runs shown
for `claude-proto`, not for `plain`; a stub version below the gate shows
the fallback's words and launches the terminal. **6 h.**

### Row 9: Yardmaster on the protocol way

**Scope.** Design 8 whole: `LaunchSpec.Way` and the launch's How
members run; `AwaitReady`'s protocol branch with `protocolReadyCap`;
`promptWith` marking prompted at `Accepted`; the deliverer waiting for a
turn's end; `KindInterrupted` skipped by `startAfter` and the reminder;
the run log's new lines and their rows in `docs/protocol.md`; a managed
run offered to a registered Runtime only when it is a `PipeStarter`;
`fakeLauncher`'s protocol members over `sessiontest.FakeProtocol`.
**Must not touch** the ledger, Brakeman's class.

**Tests.** Go race (`internal/crew`):
- `TestProtocolMemberReadyIsTheSessions`: the prompt is sent only after
  `Ready`; past `protocolReadyCap` the launch fails with words, nothing
  sent.
- `TestProtocolPromptedAtAcceptance`: `attempt.prompted` and the member's
  prompted mark follow `Accepted`, never the write; unconfirmed waits for
  a person.
- `TestHandoffWaitsForTheTurnsEnd`: a handoff, a message and a decision
  answer sent during a turn are sent after `TurnEnded`, decision answers
  first.
- `TestInterruptedTurnStartsNoOne` (I12): a legacy run's member after an
  interrupted turn is not started, and no reminder is sent.
- `TestProtocolTurnFinishesNoTask` (I12): a turn's end with no
  `task_result` leaves the task running.
- `TestRunLogSaysTheWay` (I16): each attempt's line names its way, a
  fallback its reason, a continuation the default rule.
- `TestPipelessRuntimeDeclinesProtocolRuns` (`internal/api`): a
  registered Runtime without `PipeStarter` is not offered such a run.
- `TestTerminalMembersUnchanged` (I2): the engine's suite passes with
  terminal members.
Playwright: `crew-conversation.spec.ts`: a managed run of two stub
protocol members, each attempt's way in the run's log, a task finished by
its result alone.
**By hand, with the real agent:** a live crew of the example crew with
Claude Code members as conversations, one permission answered, the run
to its end; recorded in the features entry. **14 h.**

### Row 10: Codex's app server (after the maintainers' word)

**Scope.** `agentproto/codex.go`: `codex app-server --listen stdio://`,
`initialize` with `clientInfo {name: "conductor"}` and no
`experimentalApi`, `thread/start` or `thread/resume`, `turn/start`,
`turn/interrupt`, the approvals through design 7 answered `accept` or
`decline`, `item/*` and `turn/*` mapped to entries; its fixtures and
schema of `MinVersion`; Codex's catalog declaration; `Moves` only after
its by-hand observation (thread to `codex resume <UUID>` and back, the
writer lock released). Never a second client on a thread.
**Tests.** As row 4's, for Codex (`TestCodexFixturesReplay`,
`TestCodexStableSurfaceOnly`: no experimental method is ever sent,
`TestCodexApprovalThroughThePermissionPath`: an approval request is the
session's question and its reply carries the verdict for its id alone),
and a second mode of the stub with its Playwright run. **16 h.**

### Row 11: the Agent Client Protocol client

**Scope.** `agentproto/acp.go`: protocol 1 as design 4 says; the
declarations of the first native agents whose terms the maintainers have
read (the default: Copilot CLI and OpenCode), each with its argv and a
by-hand check; `Moves` only where the agent's session id is shown to be
its native one.
**Tests.** `TestACPBaselineOnly` (no `fs/*` or `terminal/*` capability
advertised; such a request answered `-32601`),
`TestACPPermissionOptions` (`allow_once` and `reject_once` only; an
unknown option never taken for allow), `TestACPCancelAnswersPending`
(`session/cancel` answers every pending request `cancelled`), fixtures
per agent, and the stub's third mode with its Playwright run. **16 h.**

## Decisions asked of the maintainers

Each is the default this plan is written to; an answer that differs
changes the rows it names.

1. **The name in the app.** Default: the agent's thread is
   **Conversation** in the app (the app already says "conversation" for
   the agent's own: Clone "in a new conversation"), the transcript in
   code; the people's chat keeps "Chat"; the stream keeps its title.
   Other answer: rename the people's chat and give "Chat" to the agent's
   thread (row 6 renames the rail item, `?tab=chat`, the palette's row
   and twelve specs' marks).
2. **Who may answer a protocol session's permission request.** Default
   in this plan: the operator alone in the first cut; a share link,
   with control or not, a decision link and a decide principal do not
   (a share link with control keeps answering a terminal's dialog as
   today). Holders of the decide grant answer once a request is a
   durable decision record, the later boundary row, since the decide
   grant's surface is its four routes over records (`docs/plans/decide-grant.md`)
   and a session question is no record (`docs/plans/decisions.md`,
   Owner decisions, 5). Other answer: decide holders now, which adds a
   decide route for a session's permission question to row 5.
3. **Whether a transcript counts as captured output for sync.** Default:
   yes; it is never handed out and feeds the copied-output check (design
   10). Other answer: the agent's words sync, its tools' outputs not
   (row 2 feeds `OnOutput` the outputs alone; the sync allowlist stays).
4. **Whether history keeps the agent's messages.** Default: no; the
   journal keeps today's kinds. Other answer: a `session.said` kind, its
   bound, its test (`TestClassComesFromTheKindAlone`) and the 64 KiB
   record's share (a row of its own).
5. **The vendors' terms, per agent.** Default: Claude Code's `-p`
   interface is built (rows 1 to 9) after the maintainers have read the
   sentences above; Codex's app server only after the maintainers say
   yes (row 10); the Agent Client Protocol client is built and each
   agent declared only after its terms are read, Antigravity not while
   its FAQ reads as it does (row 11).

## Out of scope

- **A conversation drawn over a terminal session from the agent's own
  files.** Two truths of one session, and `docs/plans/history.md` keeps
  Conductor from opening those files.
- **A first prompt passed as a command-line argument.** The launch
  message is kept out of the session's recorded command
  (`internal/api/sessions.go:47-53`).
- **A live hand-off of a running turn, and a fork.** Two writers, or two
  conversations.
- **Hosted services of any kind.** Nothing here runs in or talks to a
  service.
- **A protocol session on another machine.** The host link's boundary
  row, later.
- **A managed run's permission request as a durable decision record**,
  its routing, the decide grant's surface and the phone's push: a
  boundary row of its own after a ledger migration of its own.
- **"Always allow" and a mid-session change of the agent's permission
  mode.** The operator's alone when they come.
- **The phone's conversation view.** The page works at phone width; its
  design is later.
- **The Overview, receipts, the verify queue and rules over a finished
  piece of work.** Later rows, which the entries are designed to feed
  (design 5).
- **Brakeman's turns on the protocol way.** They stay in the terminal.
- **Carrying a run's How members run into its continuation.** A stored
  value, a ledger barrier.
- **Messages sent into a running turn** (Codex's `turn/steer`, the Agent
  Client Protocol's extensions): queued for the turn's end in the first
  cut.
- **The Agent Client Protocol's version 2, and its file-system and
  terminal client methods.**
- **The comments and Settings text in `internal/agents/codex.go` saying
  Codex runs hooks only with `features.hooks`** (Codex's hooks page now
  says "Hooks are enabled by default" and skips untrusted ones): a
  defect of the terminal way outside this stream, for an issue of its
  own.

## Amendments to other documents

Made in the same commit as this plan:

- `docs/plans/agent-harness.md`, 3.2 Non-goals and 14 (Agent integration):
  each sentence that rules out an SDK or app-server transport gains a
  pointer: the terminal way is never removed or replaced, and
  `docs/plans/chat-or-terminal.md` adds a second, optional way of
  running a session through an agent's own documented structured
  interface.
- `docs/repository-map.md` lists no plan by name, so it is not changed
  here; rows 1, 2, 4 and 6 add their packages' rows when they land.
- `docs/plans/runtime.md` gets its one sentence with row 1, not here.

## Acceptance

The first cut (rows 1 to 9) is done when these have been observed by
hand and recorded in `docs/features.md`'s entry, not when the rows are
merged:

1. On a Linux server and on the desktop app's server inside WSL, with
   nothing configured by anyone: a Claude Code session launched as a
   conversation, a turn with a tool step and a file change drawn as
   entries, a permission card answered Allow once and another Deny by
   the operator, Stop this turn ending a turn.
2. The same session through a view link: the conversation whole, the
   card without buttons; through a control link: a message sent, no
   answer possible.
3. The session moved to the terminal after a turn, the terminal showing
   the earlier turns and taking one, moved back, the conversation taking
   one with the terminal's turn in its context; the session's id and
   link unchanged.
4. An installed version below the gate launching the terminal with the
   fallback's words.
5. A managed run of the example crew with Claude Code members as
   conversations, its log naming each attempt's way, its tasks finished
   by their results.
6. Every terminal session behaving as before (the whole Go race suite,
   vitest and Playwright passing unchanged but for the new specs).
