# Chat or terminal: a second way to run an agent's session (issue #1088)

Status: planned 2026-11-24. A maintainer answered the one decision this
plan asks that day (Decision before any builder): Claude Code's
interface, with any sign-in the program already has. Step 1 (rows 1 to
7) is in work; nothing of it is built yet. Its rows (The rows) are units
of work, grouped into pull requests as `AGENTS.md` (Working an issue)
says, with the boundaries kept apart. Codex and each Agent Client
Protocol agent wait for an answer of their own (rows 10 and 11). The
rest of what the plan chooses is a table of defaults a maintainer may
overturn (Defaults this plan is written to).

Written against `main` at `0a60d09f` after reading `internal/session`
(`local.go`, `submit.go`, `attention.go`, `answeras.go`, `chat.go`,
`info.go`, `agentsession.go`, `runtime.go`, `activity.go`), `ext/runtime.go`
and `ext/local`, `internal/api` (`sessions.go`, `resume.go`,
`attention.go`, `ws_viewer.go`, `ws_host.go`, `runs.go`, `router.go`,
`output_guard.go`, `output_index.go`, `inbox.go`, `exthost.go`,
`catalog.go`, `dispatch_plans.go`, `extruntime.go`, `extensions.go`,
`compare_doors.go`, `guide.go`, `publish.go`, `server.go`),
`internal/signal` (`hosted.go`, `hub.go`), `internal/hostagent`
(`agent.go`, `peer.go`, `uplink.go`), `internal/proto` (`frame.go`,
`control.go`, `hostmsg.go`), `internal/catalog`, `internal/agents`
(`claude.go`, `codex.go`, `usage*.go`, `registry.go`, `assets.go`),
`internal/cli` (`mcp.go`, `crew.go`, `notify.go`), `internal/notify`,
`internal/crew` (`run.go`, `task.go`, `handoff.go`, `decisions.go`,
`firstmessage.go`, `messages.go`, `runtime.go`), `internal/crew/ledger`,
`internal/journal`, `internal/history/kinds.go`, the app's session page
and its pieces (`web/app/pages/sessions/[id].vue`,
`components/TerminalView.vue`, `ChatThread.vue`, `ChatComposer.vue`,
`QuickReplyBar.vue`, `utils/transport/`, `utils/protocol.ts`,
`utils/attention.ts`, `utils/inspector.ts`, `utils/runOrigin.ts`), the
browser tests' fixtures (`web/e2e/stub-agent.sh`, `server.ts`,
`conductor.e2e.json`), and the plans this one meets:
`docs/plans/agent-harness.md`, `runtime.md`, `decisions.md`,
`decide-grant.md`, `history.md`, `sync-seams.md`, `host-connection.md`,
`capacity.md`. The agents' technical facts were read in their authors'
public documentation on 2026-11-24 and are cited by address.

The names follow the railroad: Conductor is the application; Yardmaster
the agent harness, which runs a crew with tasks as its managed run;
Dispatch the ticketing; Brakeman the sidekick agent; Switchman the task
router. A session's **way** is how Conductor runs its agent: the
**terminal way** is today's (the agent's own terminal program in a PTY);
the **protocol way** is the agent's own documented non-interactive
interface, a child with pipes. In the app the two are **Terminal** and
**Chat**; the people's chat beside a session, which the app calls Chat
today, is called **People** (its labels and its palette row only; the
wire's and the code's names, `chat`, stay). In code, a protocol
session's typed entries are its **transcript**.

**The terminal is the door.** Every session starts in its terminal, as
today: a launch, a resume, a crew's member. There the agent's own
program asks its own trust question and loads what it loads. A chat is
only what a person gets by pressing Chat on a session at rest in its
terminal. Conductor asks no trust question and keeps no trust.

## What a person sees when it is done

A person opens a Claude Code session as today. It runs in its terminal.

Every session's header has a switch: **Chat | Terminal**. Once the
agent has replied once, pressing Chat turns the session into a chat when
the agent's turn ends; before that, the switch says "Chat opens after
the agent's first reply." It is the same session: same link, same
people, same history.

The chat is quiet. It shows what the person wrote and what the agent
wrote. Each turn's work is one line, "12 steps", that opens to the
steps. A changed file is one line that opens to its diff. Below is a box
to write in. While the agent works there is a Stop button; it stops the
turn, not the session.

When the agent asks for approval, the chat asks a plain question: "Run a
command?" with the command on one line, and two buttons, **Allow once**
and **Deny**. Only the person who runs the server can answer. It waits
until they answer.

The first time a person presses Chat, one sentence says the agent's own
terms decide how this mode may be used and counted.

Pressing Terminal moves the session back. In a terminal session, the
agent and its terminal work as today; its messages are still typed into
it. Every session's side rail now has one **People** item, where the
people's chat and who is here sit together.

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
  sequence (`AttachWith`, `local.go:2941`). A session shows one question
  at a time: the shown one, the agent's own kept aside, and the one a
  held turn holds over; another of the agent's questions replaces the
  one shown (`Ask`, `local.go:1939`).
- **The line path.** `Local.Submit` (`submit.go:426`) is the one way
  Conductor types into a session: role, length, one at a time, the
  question and decision rules, then a bracketed paste, a 250 ms pause
  and an Enter, with screen checks between. A viewer's submit runs under
  a 10 s bound (`submitTimeout`, `internal/api/ws_viewer.go:248`) with
  the connection's read loop waiting for it (`heldTyping.typeIn`,
  `:303`).
- **How a session starts.** `createLocalSession` (`internal/api/sessions.go:193`):
  the catalog entry, the folder through `openCwd` (`:669`) and
  `resolveCwd` (`:726`), the argv as an array (the session recipe's
  `StartArgs` or `ResumeArgs`, `:296-326`), the environment (`pty.Inject`
  and the launch's own `CONDUCTOR_AGENT`, `:405-414`), the agent token,
  then `rt.StartTerminal` (`:472`) and `session.NewLocal(info,
  session.RuntimeProcess(term), opts)` (`:555`). One live session per
  agent conversation: `takeConversation` (`internal/api/resume.go:123`).
  Claude Code's own trust question is a startup question on the
  terminal's screen (`defaults.go:121-123`), which a person answers and
  a managed run holds at (`run.go:3097`).
- **The Runtime.** `ext.Runtime` (`ext/runtime.go:46-92`) starts a
  session only as a terminal (`StartTerminal`); `Run` and `Serve` give
  no stdin and no stream to read. A registered Runtime is offered
  managed runs alone (`ext/runtime.go:17-20`), always behind the
  server's guard (`guardRuntime`, `internal/api/extruntime.go:107`, which
  declares `ext.Runtime`'s methods only; its `StartTerminal` recovers a
  panic, quiets the error and wraps the terminal, `:137`, `:340`), and
  the engine chooses a run's Runtime once (`runtimeFor`,
  `internal/crew/runtime.go:54`). `ext/local/boundary_test.go` fails a
  process started outside the runners (`internal/pty`, `internal/proc`)
  or a runner used outside `ext/local`.
- **Structured reads already shipped.** The usage readers run Claude
  Code's `-p ... --output-format stream-json --verbose`
  (`internal/agents/usage_claude.go:40`) and Codex's `app-server
  --listen stdio://` (`usage_codex.go:23`) as bounded one-shot reads,
  each under a version gate (`claudeUsageQualified`, `codexUsageQualified`).
- **Conductor's MCP server.** `conductor mcp` (`internal/cli/mcp.go:25-31`)
  is registered with Claude Code by `--mcp-config` (`internal/agents/claude.go:85,127`,
  server name `conductor`) and with Codex by `-c mcp_servers.conductor.*`
  (`codex.go:148`). It reaches the server with the session's agent token
  through a client whose requests end at 60 s (`crewClientFromEnv`,
  `internal/cli/crew.go:67-68`). It reads one line at a time and answers
  each call before reading the next; a notification is dropped
  (`mcpServer.serve`, `mcp.go:161`, "nothing to answer" at `:175`). Its
  tools are listed from the guide's table (`api.Operations`,
  `internal/api/guide.go:192`), and a comparison lane reaches only the
  routes `compareDoors` lists (`internal/api/compare_doors.go`,
  `laneServes` at `:149`).
- **Conductor's own hooks in the person's settings.** Settings'
  integration merges the terminal way's hook list into the person's
  `~/.claude/settings.json` (`claudeSteps`, `internal/agents/claude.go:88`),
  so those hooks run wherever that file is read, and report through
  `conductor notify` with the session's `CONDUCTOR_*` environment.
- **The catalog.** `catalog.Agent` (`internal/catalog/catalog.go:23`),
  validated by `validate` (`:336`), with its session recipe
  (`SessionRecipe`, `:126`; Claude Code's `--session-id {id}` and
  `--resume {id}`, `defaults.go:132`). A ticket's approved agent is
  digested by `agentSelection` (`internal/api/dispatch_plans.go:330`).
  The app reads the catalog as `AgentInfo` (`composables/useSessions.ts:143`).
- **The wire.** Frame types 0x01 to 0x0A and 0x10 (`internal/proto/frame.go:19-29`);
  `DecodeRelay` takes a fixed list of inner types, and a server that
  meets another closes the host's connection (`frame.go:171`,
  `internal/api/ws_host.go:268-278`). The relay's guard for a viewer
  whose role is not control is a deny list (`internal/signal/hosted.go:1884-1903`).
  A server tells a host what it takes through words in `proto.Registered`
  (`hostmsg.go`: `Decisions`, `SessionInfo`, `AgentSessions`,
  `ActivitySeq`, `Renews`), each absent from an older server. A server's
  own session published to a switchyard is served there by the same
  peer code a host uses (`Uplink.Publish` builds the host's `agent`
  around the server's `Local`; `peer.handle`, `peer.go:334`).
- **The copied-output check.** `outputArrived` feeds a session's output
  to the index as the source `ring:<id>`, keeping `ScrollbackBytes` of
  it (`internal/api/output_guard.go:151`).
- **The app.** The session page's one connection belongs to the
  terminal component (`components/TerminalView.vue:656-659`): there is
  no connection without an xterm. `TerminalView` is also mounted by the
  join page (`pages/join/[token].vue`), a run's join grid
  (`components/JoinCrewGrid.vue`), the yard, the roundhouse and the
  paste page. The rail already has a **People** item, the roster of who
  is here, beside **Chat**, the people's chat (`utils/inspector.ts:45-56`);
  the chat's panel already shows who is here (`ChatPanel.vue`). A
  private set in `utils/runOrigin.ts` (`HIDDEN`, `:45`) marks the
  characters a person cannot see.

## The rules it keeps

- **The terminal way is not degraded.** A terminal session's frames,
  attention, submissions and records, and the terminal's own components
  and DOM marks, are `main`'s. What every session's page gains is named:
  one People item on the rail, and the header's switch on a session of
  an agent that has a protocol; the existing tests that change for them
  are listed in row 7 (CONTRIBUTING.md, The open-core rules, 2).
- **Stop, inspect, revoke, export and required approvals stay free.**
  Stop session, Stop (the turn) and the permission question are in the
  free build; no permission request is ever answered allow by a timer or
  a default of the vendor's (The open-core rules, 4).
- **Commands are argv arrays.** Every child is started from an argv
  array, never a shell string.
- **Folders.** A protocol session's working directory is its terminal's,
  which went through `openCwd` and `resolveCwd` as every launch's does;
  the floor of the permission path reads the server's deny list
  (`session.DenyList`).
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
  a line bound, a queue bound, a bound of requests in flight.
- **Execution behind one seam.** Every child of a session starts through
  an `ext.Runtime` (`docs/plans/runtime.md`, Why and what happens); the
  pipes start is an optional method of a Runtime (design 2), and a run
  is never moved out of the Runtime it was given.
- **Windows and WSL.** The child runs where the server runs and talks
  over its pipes: nothing crosses a network, and nobody configures one.
- **Few words for people.** Every new string a person reads is one short
  plain sentence; counts of tokens, timings and ids stay off the chat's
  surface.

## The design

### 1. One session, two ways

`*session.Local` stays the one session type: 35 sites in
`internal/api`, the engine and the uplink hold it, and a second type
would need a third arm at each. It gains a second way of being run; the
terminal way's code is not moved behind an interface in this plan.

A protocol session's `Local` holds, instead of a `Process`:

- a **child** (`session.Child`): `Done() <-chan struct{}`, `Exit()
  pty.ExitStatus`, `Stop(ctx, grace) error`. Its `Stop` closes the
  agent's input first (which cancels a waiting permission prompt:
  https://code.claude.com/docs/en/headless.md, Stop a run with SIGTERM),
  waits `grace`, then ends the process group as a terminal's `Stop`
  does. `session.PipesChild(ext.Pipes)` adapts the Runtime's pipes
  (design 2).
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
	// Verbatim says whether the protocol has a documented mark that the
	// agent takes a message's text as written (no @path expanded, no
	// /command run). Constant for the driver.
	Verbatim() bool
	// Begin writes the protocol's initialize request and returns nil once
	// the agent has answered it and its answer passed the driver's shape
	// check, or the error that says why not. It returns by ctx's end at
	// the latest. It never waits for anything the agent writes only once
	// a first message has arrived.
	Begin(ctx context.Context) error
	// Send writes one message as a new user turn, with the protocol's
	// as-written mark where Verbatim is true, and returns once its bytes
	// are written to the agent's input, or the write failed. A written
	// message is not a taken one: the agent's acknowledgement arrives as
	// ProtocolEvents.Accepted with m.Ref.
	Send(ctx context.Context, m Message) error
	// Interrupt asks the agent to end the turn it runs and keep the
	// conversation. It returns once the ask is written; the turn's end
	// arrives as ProtocolEvents.TurnEnded with outcome "interrupted".
	// ErrNoInterrupt says the agent offers none (design 4): the session
	// then ends the child and resumes the same conversation.
	Interrupt(ctx context.Context) error
	// Read reads the agent's output until it ends or ctx ends, and hands
	// each event to e in order. It returns io.EOF at the output's end. A
	// line past MaxProtocolLine is skipped to its end with a Notice; a
	// line that is not the protocol's at all returns an error (the
	// session then ends the child: design 13). Read never waits on a
	// person: a request it meets is handed over and Read reads on.
	Read(ctx context.Context, e ProtocolEvents) error
	// Reply writes the session's verdict on an in-stream request (Codex,
	// the Agent Client Protocol) under the request's own id, or the
	// protocol's own cancel of it for a withdrawal. Called at most once
	// per request, on a goroutine of the session's.
	Reply(ctx context.Context, ref string, v Verdict) error
	// CloseInput ends the agent's input. Nothing is written after it.
	CloseInput() error
}
```

and the session's side, implemented by `Local` (an unexported adapter,
so no caller outside the driver reaches it):

```go
// ProtocolEvents is what a driver tells the session. Each call returns
// once the session has applied it (state first, viewers after: I6), and
// none waits on a person.
type ProtocolEvents interface {
	Conversation(id string)                    // the agent's own conversation id, as its stream names it
	Accepted(ref string)                       // the agent took the message Send wrote under ref
	TurnStarted(agentTurn string)              // a turn began ("" when the agent names none)
	Text(ref string, delta string, final bool) // the agent's words, streamed; final closes ref
	ToolStarted(t ToolStep)                    // a tool call announced, with its name and whole input
	ToolEnded(ref string, r ToolResult)        // its result: status, exit code and output where the agent gives them
	FileChanged(f FileChange)                  // a file a tool changed: path, op, counts, diff where given
	Permission(r PermissionRequest)            // an in-stream request (Codex, ACP); the verdict goes back through Reply
	PermissionWithdrawn(ref string)            // the agent took back an in-stream request it made
	Background(running bool)                   // work the agent started still runs after its turn (or no longer does)
	TurnEnded(outcome string, u Usage)         // "done", "interrupted", "error", "limit"
	Notice(code, words string)                 // something the agent said that is none of the above
}
```

What each existing call of `Local` does on a protocol session:

| Call | On a protocol session |
|---|---|
| `Submit` (`submit.go:426`) | the guards that are not the terminal's stay (role, length, withdrawn, ended, the question and decision rules). A submission whose `Question` names a permission request is refused `ErrPermissionCard` before any claim, whoever calls it. A controller's message is put in the session's own queue (at most `MaxQueuedMessages`, 16; past it `ErrTooManyQueued`, wire code `too_many_requests`) and `Submit` returns `ErrSubmitPending` at once: outside a turn it is written at once, during a turn at `TurnEnded`, in order, each written once. `Finished` is told `Entered` at its `Accepted`, or `Unconfirmed` once `AcceptWait` (30 s) has passed from the write; an unconfirmed message is never written again (I4). A submission with `UnlessWaiting` (the deliverer's) writes nothing and returns a zero result while a question shows and, on a protocol session, while a turn runs; its caller sends it again when woken at the turn's end, as today. Line breaks are kept; other control characters are dropped. `Typed`, `Reentered` and `NotAtPrompt` stay false. |
| `Input`, `Resize`, `ResizeWith` | refused `ErrNotTerminal`, wire code `not_terminal`, connection kept |
| `AnswerInput`, `AnswerInputReq`, `AnswerAs` | for a question of source `protocol` and kind `permission`: refused `ErrPermissionCard` (design 7). For any other question the session asked (a blocker, a decision's mirror, the agent's own `ask`): as today, the choice's text sent as a message through `Submit`'s path |
| `LastLine`, `ScreenLines`, `ScreenRead` | empty answers; Brakeman's screen words find none (its turns stay terminal: Out of scope) |
| `LastOutputAt` | the time of the last event the driver handed over |
| `BracketedPaste` | `(false, false)` |
| `Info` | gains `Way` (`"protocol"`; absent for a terminal session), `Protocol` (the protocol's name) and `Verbatim`; `Cols` and `Rows` are 0 |
| `Stop` (`local.go:5967`) | answers every open permission request deny (design 7), then `Child.Stop` with the session's grace |
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
change an entry `file`, as the hook mappers make them
(`internal/notify/mappers.go`), so Activity, Touched, the journal, the
webhooks and the crew engine see one session whichever way it runs. The
attention state machine's screen rules (`local.go:2015-2111`) see no
screen and so never fire for a protocol source (I7).

A protocol session's way is entered only by `SwapWay` from a terminal
session at rest (design 9): no constructor starts a session in the
protocol way. Its read goroutine runs the driver's `Read`, and `Begin` is
called before the first message. Every byte the driver captured (the
agent's words, a tool's output, a diff) is handed to
`Options.OnCaptured`, the copied-output check's feed for a transcript
(design 10).

### 2. How the child starts

`ext` gains an optional interface beside `ext.Runtime`, as `ext.Starter`
is optional beside it:

```go
// PipeStarter is a Runtime that can start a long-lived child with its
// input and output as pipes: a session's protocol way. The native
// Runtime implements it.
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
(`os/exec` with two pipes, its own process group, stderr read to its
end and counted, the group signalled as `proc.Serve`'s is), wrapped in
`ext/local`. The boundary test's `runners` list gains `StartPipes`,
used only in `ext/local` and `sessiontest`'s fake (a reviewed edit of
the list, as its comment asks). `ext/exttest`'s `RuntimeExec` gains the
pipes' checks (argv, directory, environment, input, output, exit, the
two stops, a context ended before and during the start) for a Runtime
that implements it, and `FakeRuntime` scripts it.

**The guard.** A registered Runtime reaches the server and the engine
only behind `guardRuntime` (`internal/api/extruntime.go`), which today
forwards `ext.Runtime`'s methods alone. `installRuntimes` wraps an inner
Runtime that implements `PipeStarter` in a second guard type,
`guardPipes`, whose `StartPipes` is guarded as `StartTerminal` is (a
panic recovered, the error quieted) and never under `extCall`'s wait,
and whose returned `Pipes` is wrapped in a guard as `guardTerminal`
wraps a Terminal; an inner Runtime without it keeps the plain guard, so
a type assertion on what the server holds tells the truth.

**Who starts through it.** `SwapWay`'s start of a protocol child (design
9), through the Runtime the session's terminal was started on: a
person's session runs natively, as today. A managed run's members (step
2) are offered to a registered Runtime exactly as today (`runtimeFor`,
`Takes`): a run is never moved to native for the protocol way, and when
the run's Runtime is not a `PipeStarter`, its members stay in their
terminals. This leaves `docs/plans/runtime.md`'s "every process through
a Runtime" standing, with one change in words: the pipes method is
optional, and a Runtime that lacks it is never handed a pipes child.

**The environment** is the terminal's (`launchEnv`, `pty.Inject`'s
`CONDUCTOR_*`, `CONDUCTOR_AGENT`), with `CONDUCTOR_PROTOCOL=1` added,
and `TERM` and `COLORTERM`, which `pty.BuildEnv` always sets, taken out
of its result; `NO_COLOR=1` is set.

**The stream's bounds.** A stdout line is bounded at `MaxProtocolLine`,
16 MiB: above the largest single tool result Claude Code documents (an
MCP Apps tool's result "has a limit of 8,388,608 characters",
https://code.claude.com/docs/en/agent-sdk/typescript.md). A line past it
is read to its end without being kept, the step's output part is
marked cut, and a `notice` says "A step's output was too large to
show." The driver's queue of events ahead of the session is bounded at
1,024; past it the driver stops reading until the session catches up,
so nothing is dropped. Stderr is counted and dropped, as the usage
readers drop it.

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
- **Carried.** `inherit`, the deep copy and a clone carry it; the
  catalog route's `AgentInfo` (`composables/useSessions.ts`) shows it,
  read-only, so the page knows where to draw the switch (row 4); the
  Agents page's form sends it from the agent edited
  (`web/app/utils/agentForm.ts`, `AgentInput`) so a save does not drop
  it (row 8); `docs/protocol.md`'s catalog rows say it.
- **The built-in.** Claude Code's entry gets `{"name":
  "claude-stream-json"}` (row 4). Codex's and the Agent Client Protocol
  agents' get theirs in their rows, after the maintainers' decision on
  each agent.
- **The drivers' code** lives in a new package, `internal/agentproto`:
  the closed set (`Names`), `Check`, a driver per name (`claude.go`,
  later `codex.go`, `acp.go`), and each driver's gate: `MinVersion` and
  a start-time shape check (the fields the driver needs, read from the
  agent's `initialize` answer and first events, never cached by
  version). It imports `internal/session` and `internal/catalog`;
  neither imports it.
- **What a version the driver has not qualified does.** The switch's
  Chat side is offered only when the agent's probed version (the
  identity probe, `internal/agents/probe.go`) is at or above
  `MinVersion`; otherwise it says "This version of Claude Code can only
  run in its terminal." A start whose `initialize` answer is missing or
  fails the shape check ends the child before any message is sent and
  starts the terminal way again on the same conversation id, with the
  same sentence (design 9's retry).
- **The digest.** `agentSelection` adds the protocol's name to a
  ticket's approved agent only when a launch runs the protocol way, so
  every approved digest of a terminal launch stands unchanged (row 8,
  for step 2's crews; in step 1 no launch runs the protocol way).
- **Whether an agent moves between ways** is the driver's, not the
  operator's: a constant per driver (`Moves`), true only where resume
  across the two ways is established (design 9).

### 4. Each agent's interface, as its own documents state it

Each fact below is marked **documented** (its authors' public pages say
it), **experimental** (documented, and its authors call it experimental
or preview), or **undocumented** (known from a vendor's open-source code
or a recorded run only). Each undocumented or experimental fact the plan
uses has a gate and a fallback.

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
| a replayed user message carries the `uuid` the client wrote | not stated on the pages read | undocumented | row 4's fixture records it; where it does not, replays are matched to writes in order (one message in flight per session), never by time |
| `client_composed: true` on a user message: "Claude Code then doesn't expand `@path` mentions, and doesn't run text that starts with `/` as a command"; "Requires ... Claude Code v2.1.248 or later" | https://code.claude.com/docs/en/agent-sdk/typescript.md, SDKUserMessage | documented | **gated**: `MinVersion` is at least 2.1.248; the driver's `Verbatim` is true and `Send` sets it on every message |
| an `@` reference "makes no tool call", so `PreToolUse` hooks do not run for it; "Claude Code doesn't include a file that the rule matches" for a `Read` deny rule | https://code.claude.com/docs/en/common-workflows.md | documented | the floor's `Read` and `Edit` deny rules (design 7) |
| a `-p` run uses a folder's settings-file hooks, its `env` block and its `.mcp.json` servers without a trust dialog | https://code.claude.com/docs/en/permissions.md, What runs before you trust a folder | documented | a chat is started only from a session whose terminal ran the agent's own program in that folder to its prompt (design 9), with the same setting sources: it loads nothing the terminal did not |
| `--session-id <uuid>`, `--resume <id>`; a `-p` session resumes by id in the terminal UI: "You can still resume one by passing its session ID to `claude --resume <session-id>`" | https://code.claude.com/docs/en/sessions.md | documented | the move is observed by hand in row 5 before `Moves` is set |
| `--permission-mode`; "`claude -p` ... with no permission mode set start in Manual mode", and a settings file may change the default | https://code.claude.com/docs/en/permission-modes.md | documented | Conductor always passes the mode |
| "Deny rules block in every mode, including `bypassPermissions`." | https://code.claude.com/docs/en/permission-modes.md | documented | the floor's deny rules; a mod the person installed can still approve some calls a deny rule refuses (https://code.claude.com/docs/en/permissions.md, Precedence among rules, hooks, and mods): the floor does not hold against the person's own mods |
| `--permission-prompt-tool mcp__<server>__<tool>`: "an MCP tool to handle permission prompts in non-interactive mode"; its result: "one text block whose text is `{"behavior": "allow"}` or `{"behavior": "deny", "message": "<reason>"}`" | https://code.claude.com/docs/en/cli-reference.md, https://code.claude.com/docs/en/errors.md (Permission prompt tool returned an invalid result) | documented | none needed |
| the arguments Claude Code calls the prompt tool with (the tool's name, its input, the call's id) | not stated on the pages read | undocumented | row 4 records them in a fixture; the gate refuses a version whose call lacks the tool's name and input |
| whether stdout carries the call's `tool_use` before the prompt tool is called | not stated | undocumented | the request waits for the announcement within `AnnounceWait` (design 7); row 4's fixture records the order |
| whether Claude Code cancels the prompt tool's call when a `PermissionRequest` hook decides first ("whichever decides first applies", https://code.claude.com/docs/en/hooks.md) | not stated | undocumented | the call's result in the stream withdraws the request either way (design 7); row 6 records by hand whether a cancellation is also sent |
| a stdio MCP server's tool call: idle timeout 30 minutes by default (`CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT`); a per-server `timeout` "of at least 1000 raises that server's idle window"; overall `MCP_TOOL_TIMEOUT` about 28 hours | https://code.claude.com/docs/en/env-vars.md | documented for `.mcp.json`; for a `--mcp-config` file not stated | Conductor's protocol MCP config sets `timeout` to 8 hours; row 4 checks by hand that `--mcp-config` honours it (design 7) |
| the user settings' hooks run in `-p` (Conductor's own integration's among them) | https://code.claude.com/docs/en/headless.md | documented | `conductor notify` sends nothing for a hook payload when `CONDUCTOR_PROTOCOL` is set, and the routes drop hook-shaped reports for a protocol session (below) |
| the event types on stdout (`system/init`, `assistant`, `user`, `stream_event`, `result` with cost and usage, `system` task events) | https://code.claude.com/docs/en/agent-sdk/typescript.md, Message Types; https://code.claude.com/docs/en/headless.md | documented as the TypeScript SDK's message union, an open set | decoded leniently; unknown events become nothing |
| whether `system/init` is written before a first user message | not stated | undocumented | `Begin` returns at the `initialize` answer alone, never at `system/init` |
| the control envelope (`control_request`, `control_response`, `request_id`), `initialize`, `interrupt`, `pending_permission_requests`, and `system/init.capabilities` ("It is an open set") | https://code.claude.com/docs/en/agent-sdk/typescript.md only ("if you parse the wire protocol yourself"); the request schemas only in the MIT Python SDK's source | documented in one reference only, schemas undocumented | **gated**: no protocol way without it. A version below `MinVersion`, or a start whose `initialize` answer is missing or fails the shape check, stays in its terminal (design 3). When `interrupt_receipt_v1` is missing from the capabilities, `Interrupt` answers `ErrNoInterrupt`, and the session ends the child and resumes the same conversation with a `notice`; that exit is not the session's end |
| `--permission-prompt-tool stdio` (the SDK's own route) | not on any page read | undocumented | **not used** |
| SIGTERM leaves the turn unfinished; closing input cancels a waiting permission prompt | https://code.claude.com/docs/en/headless.md, Stop a run with SIGTERM | documented | `Child.Stop` closes input first |
| "After Claude finishes its turn and stdin has closed, a `claude -p` run can stay open to wait for background work that Claude started." | https://code.claude.com/docs/en/headless.md, Background tasks at exit | documented | a move is not made while the stream reports background work running (design 9); Stop session ends it, as it ends a terminal's |
| "Piped stdin is capped at 10MB" | https://code.claude.com/docs/en/headless.md | documented; whether it holds for a long stream-json input is not stated | the driver counts what it wrote; past 8 MiB it ends the child at the next turn's end and resumes the same conversation |
| `--bare` "never reads OAuth credentials", and "will become the default for `-p` in a future release" | https://code.claude.com/docs/en/headless.md | documented, a future change | Conductor never passes `--bare`. The shape check does not test sign-in: no recorded `system/init` carries an account. A start that reports it is not signed in (an authentication error in the first `result`, or a `system/api_retry` for 401, before the first message has an answer) ends the child, and the session goes back to its terminal on the same id with one sentence, until the driver passes the documented opt-out |
| two writers on one session lose messages; no lock is documented for a foreground session | https://code.claude.com/docs/en/sessions.md | documented | the one-writer rule is Conductor's (design 9) |
| flags not restored on resume (`--mcp-config`, `--settings`, `--tools`, ...) | https://code.claude.com/docs/en/sessions.md | documented | every start passes them again |

**The argv of a chat's child of Claude Code**, every start: the
catalog's `Command`, `-p --input-format stream-json --output-format
stream-json --verbose --include-partial-messages
--replay-user-messages`, the protocol's `Args`, `--resume <id>` (the
conversation read at the move), the model flag the terminal was started
with, `--permission-mode default` (for a session whose terminal was a
yolo launch, the catalog's `Yolo.Args` alone, never the adapter's
`YoloInject` or `InjectFor`), the terminal's launch arguments,
`--mcp-config <hooks>/claude-protocol-mcp.json` (Conductor's MCP server,
`timeout` 8 hours), `--permission-prompt-tool mcp__conductor__permission`,
and exactly one `--settings <hooks>/claude-protocol.json` (the floor:
design 7; for a yolo session `claude-protocol-yolo.json`, the floor and
the yolo key in one file). No `--setting-sources` and no
`--strict-mcp-config`: the child reads the same setting sources the
agent's terminal program read in that folder for this session. The
terminal way's hook settings, trust arguments and screen recipe are not
passed. Conductor sets no environment variable that names another
client.

**Conductor's own hooks are not a second source.** The person's user
settings may hold the terminal way's hooks that Settings' integration
installed (`claudeSteps`). With `CONDUCTOR_PROTOCOL` set, `conductor
notify` given any hook payload (`--claude-hook`, any `--<agent>-hook`,
`--codex`) reads it and exits 0 without sending. As a second line, the
attention route takes no report of kind `permission` and no turn report
from source `api` for a session whose `Way` is `protocol`, and the
events route takes no `file`, `tool_use`, `tool_denied` or `error` event
there: each is answered 200, dropped, and counted once in the activity
log. The agent's own explicit reports (`--ask`, `--event
progress|artifact|handoff`) are still taken.

#### Codex (second, after the maintainers' decision)

The interface: `codex app-server --listen stdio://`, JSON-RPC 2.0 with
the header left out, one message a line. Its authors: "the interface
Codex uses to power rich clients" (https://learn.chatgpt.com/docs/app-server.md);
and also "primarily for development and debugging and may change without
notice" (https://learn.chatgpt.com/docs/developer-commands.md?surface=cli)
and "experimental and isn't supported for production workloads"
(https://learn.chatgpt.com/docs/mcp-server.md). The same app-server
page shows a Go client over stdio with the standard library. `codex
exec --json` rejects every approval request (its source,
`codex-rs/exec/src/approval_warning.rs`) and is no base for this.

| Fact | Address | Standing | Gate and fallback |
|---|---|---|---|
| `initialize` with `clientInfo`, then `initialized`; "Use `clientInfo.name` to identify your client" | app-server.md | experimental (the command) | Conductor names itself `conductor` |
| `thread/start`, `thread/resume`, `turn/start`, `turn/interrupt` ("the turn ends with `status: "interrupted"`") | app-server.md | experimental | stable surface only (`experimentalApi` false: "the server rejects experimental methods/fields") |
| approvals: `item/commandExecution/requestApproval` (with its `cwd`) and `item/fileChange/requestApproval`, answered `accept`, `acceptForSession`, `decline`, `cancel` (and amendments in the schema); `serverRequest/resolved` when one is answered or cleared | app-server.md; the generated schema | experimental | first cut answers `accept` or `decline` only; `serverRequest/resolved` for an open request is `PermissionWithdrawn` |
| types are "specific to the Codex version you ran" (`codex app-server generate-json-schema`) | app-server.md | documented | the row records the schema of `MinVersion` and decodes leniently |
| a TUI resumes an app-server thread by its id (`codex resume <UUID>`); one writer per thread across processes ("already has an active writer") | Codex's source (`codex-rs/tui/src/lib.rs`, `codex-rs/rollout/src/writer_lock.rs`) | undocumented | the move is offered only after its own observation (design 9) |
| several clients of one app server: the first answer to an approval settles it | Codex's source (`outgoing_message.rs`) | undocumented | Conductor never attaches a second client: no `--remote` terminal on a protocol thread |
| whether a message's text is taken as written | not found | not stated | row 10 reads and states it before Codex is declared; `Verbatim` is set from it |
| whether the app server loads the same folder settings the TUI loaded for the session | to be read by row 10 | open | row 10 answers it before Codex is declared |

#### The Agent Client Protocol (one client, third)

Protocol 1 is stable; "This version is only incremented when breaking
changes are introduced"
(https://agentclientprotocol.com/protocol/v1/initialization.md). One
transport, stdio, newline-delimited JSON-RPC
(https://agentclientprotocol.com/protocol/v1/transports.md). A client
must implement `session/request_permission`: the agent offers options,
each with an `optionId` of the agent's own, a `name`, and a `kind`
(`allow_once`, `allow_always`, `reject_once`, `reject_always`), of which
"`kind` is only a UI hint" and "The Agent owns remembered decisions"
(https://agentclientprotocol.com/protocol/v1/tool-calls.md). The client
sends `session/cancel` to the agent to cancel a turn, and then "MUST
respond with the "cancelled" outcome" to every pending request
(tool-calls.md; https://agentclientprotocol.com/protocol/v1/cancellation.md);
an agent may take back a request it made with `$/cancel_request`
(cancellation.md). An agent's slash commands "are run as part of regular
prompt requests where the Client includes the command text in the
prompt" (https://agentclientprotocol.com/protocol/slash-commands.md), and
the protocol has no as-written mark, so the ACP driver's `Verbatim` is
false. Version 2 is a preview, "not frozen"
(https://agentclientprotocol.com/announcements/acp-v2-preview.md). No
official Go library exists (https://agentclientprotocol.com/libraries/community.md);
Conductor writes the small client it needs on `encoding/json`.

The first cut's client: protocol 1; it advertises no file-system and no
terminal capability (optional in 1, removed in 2); it sends
`initialize`, `session/new`, `session/load` or `session/resume` where
advertised, `session/prompt`, `session/cancel`; it answers
`session/request_permission` through design 7: an Allow selects the
offered option whose `kind` is `allow_once` and answers `{"outcome":
{"outcome": "selected", "optionId": <that option's optionId>}}`; a Deny
selects the one whose `kind` is `reject_once`, or answers `cancelled`
when none is offered; a request with no `allow_once` option is put to
the operator with Deny only; an `allow_always` or `reject_always` option
is never selected. It draws `session/update`'s message chunks,
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
none of them; row 11 checks each by hand before its declaration says
`Moves`, and states for each what it does with a message that starts
with `/` or holds `@path`, and whether its ACP start loads the same
folder settings its terminal did.

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
| `mark` | the session's count of changes: one counter, raised by every entry added or revised, the entry carrying the mark of its last change | uint64 |
| `rev` | 0, raised each time an entry is revised | uint32 |
| `at` | when the session applied it (RFC 3339, milliseconds) | |
| `kind` | `prompt`, `text`, `tool`, `file`, `permission`, `turn`, `notice` | closed set |
| `turn` | Conductor's count of the session's turns, from 1 | uint32 |
| `ref` | the agent's own id for the thing (a message's, a tool call's), opaque | 128 bytes |
| `parent` | the `ref` of the tool step a subagent's entry belongs to | 128 bytes |

By kind:

- **`prompt`**: a message sent into the conversation. `by` (`person`,
  `conductor`), `byName` (40 runes), `text` (the message, 4 KiB inline),
  `state` (`queued`, `written`, `accepted`, `unconfirmed`). A message
  that answers a question carries `answers` (the question's id) and no
  `text`: its text is shown to its sender by their own browser alone,
  the people's chat's rule (`docs/protocol.md`, Chat; #364).
- **`text`**: the agent's words. `text` at most 4 KiB an entry, cut at a
  character boundary; a longer message continues in the next entry with
  the same `ref`. Streamed: revised at most once per 100 ms per entry,
  closed by `final`.
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
  question's id), `call` (the agent's id of the call), `tool`, `plain`
  (the closed set of the question's plain words: `command`, `file`,
  `read`, `tool`), `summary`, `cwd` (only where the agent's request
  names its folder: Codex's approval does; Claude Code's prompt tool and
  the Agent Client Protocol do not, and then it is absent), `inputBytes`,
  `inputSha256`, `choices` (Conductor's ids: `allow_once`, `deny`; for
  an ACP agent each beside the agent's own option `name`), `state`
  (`open`, `allowed`, `denied`, `withdrawn`), `byName` (who answered),
  `why` (fixed words for a deny or a withdrawal).
- **`turn`**: `phase` (`start`, `end`); at its end `outcome` (`done`,
  `interrupted`, `error`, `limit`), `durationMs`, `usage`
  (`inputTokens`, `outputTokens`, `costUsd` where the agent reports
  them). These are for later rows and the record; the chat shows none of
  them (design 11).
- **`notice`**: `code` and fixed `words` (500 bytes): a move between
  ways, a fallback to the terminal, a restarted child, a step's output
  too large to show, an error the agent reported.

**Parts.** A tool's whole `input` and `output` and a file's `diff` are
kept beside the ring, each at most 256 KiB (cut with `cut: true` and the
size left out), all of a session's parts at most 8 MiB, the oldest
dropped first. A dropped part is answered `transcript_gone`.

**The ring.** At most 1,000 entries and 2 MiB of entries, the oldest
dropped first; an entry dropped is answered `transcript_gone`. Kept
while the session is listed, gone with it.

**An open request is never dropped.** An open permission request's
entry and its input part are held beside the ring while the request is
open: at most `MaxPendingPermissions` (8) entries and 8 x 256 KiB of
parts, reserved inside the 8 MiB of parts, the ring dropping the next
oldest instead. They become droppable once the request is answered,
withdrawn or denied. The replay after `ready` always sends every open
request's entry, whatever `transcriptAfter` says and outside the replay
budget (design 6).

**Why this is enough for later rows.** A finished piece of work can be
computed from the entries alone, in `seq` order, which is the order the
agent's stream gave them (I5): which commands ran and how they ended
(`tool` entries with `summary`, `status`, `exit`), which files changed
and by how much (`file` entries), what was asked and who allowed it
(`permission` entries with `state` and `byName`), and whether a test
command ran after the last edit (a `tool` entry after the last `file`
entry, within the turn's bounds). What the agent did not report (a
change made by a command, an exit code Claude Code does not structure)
is absent, never invented; a later row reads such a fact from git or
the checks, which already exist.

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
  - `interrupt {reqId}`: the chat's Stop; `reqId` 1 to 32 printable
    ASCII; controllers; answered `interrupt_ok{requestId}` or
    `error{not_protocol|no_turn|read_only|moving}`.
  - `permission_answer {reqId, question, call, sha256, choice, message?}`:
    `question` 1 to 32, `call` at most 128, `sha256` 64 lowercase hex,
    `choice` `allow_once` or `deny`, `message` at most 500; answered
    `answer_ok{requestId}` or
    `error{question_answered|stale_request|not_answerer|read_only|moving}`.
- **Owner to client, a new CONTROL message:** `way {way, protocol?,
  noted?}`, within `MaxControl` (`way` `terminal` or `protocol`;
  `protocol` at most 32 bytes; `noted` `terminal` or `protocol` or
  absent), sent when a move has changed the session's way, and when a
  move is noted, replaced or pressed away (design 9); the welcome
  carries the noted move too, so another window or a reload shows it.
- **`submit` keeps its message and bounds** (`MaxSubmit`, 4096 bytes).
  On a protocol session it is answered through `Finished`, as a slow
  line is today: `submit_ok` once the agent acknowledged the message,
  `error{unconfirmed}` past `AcceptWait`, `error{too_many_requests}` at
  once when the session's queue holds `MaxQueuedMessages` (16),
  `error{moving}` during a move; the socket's read loop is not held past
  the enqueue, so an `interrupt` sent next is read at once.
  `ws_viewer.go` and `peer.go` do this alike.
- **The resume point is a mark.** `hello` gains `transcriptAfter`
  (uint64, optional): the highest `mark` the client holds. The owner
  replays, after `ready` and the activity and chat replays, every open
  permission request's entry, then, in `mark` order, every entry the
  ring holds whose `mark` is above `transcriptAfter`, revisions and
  closed requests included, within a budget of 256 KiB; past the budget,
  or when the ring no longer holds every change after `transcriptAfter`,
  it sends the newest that fit with `more: true`, and the client pages
  back with `transcript_get before`. A request that ended while the
  viewer was away is sent again with its end, so it is never drawn open.
  (The smaller alternative, a welcome list of the requests open now,
  would still leave a tool row drawn running and need a fetch per
  entry; the mark is one counter.)
- **`welcome` gains** `way` (`"protocol"`; absent for a terminal
  session, as from an older owner), `noted` (a noted move), `verbatim`,
  `transcript {first, last, mark}`, `interrupt: true` and
  `permissionAnswer: true`. A protocol session sends no SCROLLBACK and
  `scrollbackBytes` 0.
- **New error codes:** `not_terminal`, `not_protocol`, `no_turn`,
  `transcript_gone`, `stale_request`, `not_answerer`, `permission` (an
  `answer` or a line naming a permission request: "Answer this request
  with its own buttons."), `unconfirmed`, `moving` (a submission, an
  answer or an interrupt while the session moves between ways),
  `move_refused` (a move whose conditions do not hold; its words say
  which). `too_many_requests` gains its meaning on `submit`.
- **The registered word.** `proto.Registered` gains `Transcript bool`
  ("the server takes TRANSCRIPT in relay envelopes, and its join page
  draws a chat"), absent from an older server. A server's protocol
  session is published to a switchyard only when its `registered` says
  `transcript`; otherwise it is not published, and Settings' switchyard
  line says "Chats are not published: the switchyard is older than this
  server." `ProtoVersion` does not move.
- **The switchyard's viewers are served.** `peer.go` serves the
  switchyard's viewers of a published server session as `ws_viewer.go`
  does: it passes the hello's `transcriptAfter` into `AttachOptions`, and
  dispatches `transcript_get` (at most 4 in flight per viewer), `interrupt`
  (controllers only) and a protocol `submit` by the session's way
  (`local.Way()`). On a terminal session, every session of `conductor
  host` among them, `transcript_get` and `interrupt` answer
  `not_protocol`. A host never sends TRANSCRIPT, so no older server
  meets one from a host.
- **The relay's guard becomes an allow list.** For a viewer whose role
  is not control, `RelayToHost` passes CONTROL whose `t` is `hello`,
  `ping`, `chat` (the host refuses one that is for the agent),
  `file_get` or `transcript_get`, and nothing else: INPUT, FILE_WRITE,
  DECIDE and every other CONTROL are refused `read_only`, so a message a
  later row adds fails closed. The host's and the session's own checks
  stay the first line.
- **The session listing.** `SessionInfo` (`composables/useSessions.ts`)
  and `docs/protocol.md`'s sessions listing gain `way`, `protocol` and
  `verbatim` (row 4); `AgentInfo` gains `protocol` (row 4).
- **The new routes.** `POST /api/sessions/{id}/permission` (agent token,
  body at most 256 KiB, design 7) and `POST /api/sessions/{id}/way`
  (admin, body `{"to"}` at most 64 bytes, design 9), each with its body
  bound and its test.

### 7. The permission request (the boundary)

This is a change at "who may answer or act" (AGENTS.md, Working an
issue): its own row (row 6), its own pull request, an adversarial read
of its own. It covers every protocol session in the first cut.

**What reaches Conductor, and what does not.** A call the agent puts to
Conductor's prompt tool is answered by the operator alone. A call the
agent's own configuration allows without asking (the person's settings,
or the folder's own, which its terminal already loaded) never reaches
Conductor, exactly as it never reaches a person in the terminal. That is
the agent's, not Conductor's; the plan says so here and in I10.

**The path, Claude Code.** Claude Code calls Conductor's MCP tool
`permission` (`--permission-prompt-tool mcp__conductor__permission`).
`conductor mcp` posts the call to `POST /api/sessions/{id}/permission`
with the session's agent token (the attention route's authentication,
`agentTokenOK`): `{call, tool, input}`, at most 256 KiB, and holds the
request open until the server answers `{behavior, message?}`, which it
returns as the documented result text. The `permission` tool posts with
an `http.Client` of its own, with no `Timeout` (not `crewClientFromEnv`'s
60 s client) and no context of the session tools' 60 s: its call's
context ends only by the call's `notifications/cancelled`, the end of
stdin, or `PermissionWait` plus one minute. A POST that ends without the
server's answer for any other reason is answered to Claude Code as the
documented deny, `{"behavior":"deny","message":"Conductor lost this
request."}`, never an error result. An input past 256 KiB is answered by
the tool at once, without posting, `{"behavior":"deny","message":"This
call's input is too large for Conductor to show it whole."}`. The tool
is an Operation of the guide's table (so `conductor mcp` lists it, in a
protocol session alone: `CONDUCTOR_PROTOCOL` set), kept out of the
guide's text to the agent, and its route has the `compareDoors` entry
`doorOwn`, so a comparison lane's member asks about its own calls. The
tool's description tells the model it is Conductor's and not to be
called; a call that is not a pending call of the session is denied (the
binding, below).

**`conductor mcp` keeps reading while a call is held.** Each
`tools/call` runs on a goroutine of its own with a context of its own,
at most `MaxPendingPermissions` plus 4 in flight, past which a call is
answered an error at once; replies are written one at a time under one
lock. A `notifications/cancelled` naming a held request id cancels that
call's context, which ends its POST, and no reply is written for it, as
MCP asks; the end of stdin cancels every held call. Its 1 MiB line
bound stays. The server reads the end of a held `/permission` request
before an answer (its request's context done): withdrawn "Answered by
the agent's own rules." when it came with the call's result in the
stream or an MCP cancellation, else withdrawn "The request was lost.";
either is recorded before any later answer can be applied, so a later
answer meets `question_answered`.

**The path, Codex and the Agent Client Protocol.** The driver's `Read`
meets the request in the stream and hands it over with
`ProtocolEvents.Permission`, and reads on; the session's verdict goes
back through `Protocol.Reply` under the request's own id, on a goroutine
of the session's. A withdrawal the agent sends (Codex's
`serverRequest/resolved` for an open request, an ACP `$/cancel_request`)
is `PermissionWithdrawn`.

**What the session does with a request** (`Local.RequestPermission`):

1. **It must name a call the stream announced.** Taken only for a tool
   call of the current turn that the stream showed, with that `call` id
   (or, where the agent's call carries none, exactly one pending call),
   whose name equals `tool` and whose canonical input's digest equals
   the request's, with no result yet, and whose tool is not
   `mcp__conductor__permission` itself. A Claude Code request whose call
   the stream has not yet announced waits, with no question asked, until
   the stream announces it (then it is checked as above), the turn ends,
   the child exits, the session stops, or `AnnounceWait` (10 s) passes;
   only then is it denied "Not a call this session is making." A waiting
   request counts against the bound and is never answered allow. An
   in-stream request (Codex, ACP) is judged at once: the driver hands
   events in order. One request per call: a second for the same call is
   denied "Already asked."
2. **The floor is its deny rules.** Conductor's one settings file
   (`claude-protocol.json`, or `claude-protocol-yolo.json`) carries a
   `Read(...)` and an `Edit(...)` deny rule for every path of the
   server's deny list (`session.DenyList`: the data directory, the
   configuration, the TLS keys, the hooks directory, the machine's
   directory), written at the server's start. "Deny rules block in every
   mode, including `bypassPermissions`" (permission-modes.md), they hold
   beside whatever the folder supplies, and they also keep an `@`
   reference from including such a file. Conductor also denies, before
   any question, a file tool's call (`Read`, `Edit`, `Write`,
   `MultiEdit`, `NotebookEdit`, `Grep`, `Glob`) whose path is under the
   list, and a shell command whose text names such a path: "Conductor
   keeps agents out of its own files." A `PreToolUse` hook on shell
   commands (`conductor notify --protocol-floor`, judging the command's
   text against the list) is a second look. **What the floor does not
   promise:** a command that computes a path (a variable, a `cd`, a
   script) is not caught, so a yolo session's commands can reach any
   file the person's account can read; the hook is a second look that a
   folder's own settings can switch off (`disableAllHooks`); a mod the
   person installed can still approve some calls a deny rule refuses
   (permissions.md); and the folder's own hooks, helpers and MCP servers
   run as the person's account, as they do in the terminal.
3. **The bound.** At most `MaxPendingPermissions` (8) open or waiting
   per session; past it, denied "Too many requests are waiting; this one
   was denied."
4. **The list.** Open permission requests are kept in a list of their
   own on the session, oldest first, apart from the attention's shown
   question, the agent's own question kept aside and the question a held
   turn holds over. Each has its own id, claim and kept result. While
   any is open, the attention shows the oldest one, of kind `permission`
   and source `protocol`, with no quick replies. No other setter
   replaces it or withdraws it: not the agent's ask or clear, a crew
   blocker's `TryAskID`, a decision's mirror (`TryAskDecision`),
   `WithdrawQuestion`, or a report of the turn. A question asked
   meanwhile is kept and shown once the list is empty; one withdrawn
   meanwhile simply goes. Answering or withdrawing the oldest shows the
   next. `waitsLocked` and `claimLocked` take a claim on any question in
   the list, not only the one shown. Each request has a `permission`
   entry carrying the question's id, the call, the tool, its plain
   words, the summary and the digest; the whole input is a part.
5. **The digest.** `inputSha256` is SHA-256 over the tool's name, a
   newline, and the input's canonical JSON (decoded with numbers kept as
   written, objects' keys sorted, no insignificant space), computed by
   the server from the bytes the agent sent. An input that does not
   decode cleanly (not UTF-8, a lone surrogate escape, a key twice) is
   denied "Conductor cannot show this call exactly.", so the digest is
   only ever of one form.

**Who may answer** (a default a maintainer may overturn): the operator
alone, that is the workbench token's own windows and an identity
provider's principal holding `admin`, through `permission_answer` on
the session's socket. Refused `not_answerer`, with the question drawn
without buttons: a share link's viewer of any role, a decision link, a
decide principal (it has no live connection, `docs/plans/decide-grant.md`),
Brakeman, the asking agent and its token. `AnswerAs` and `Submit` with
a question (a registered decision router's held answer, a provider's
`ext.Host.Submit`, a choice or a line), an `answer` message and a
decision link's or a run desk's text answer are refused
`ErrPermissionCard` for a question of source `protocol` and kind
`permission`: a router is told of the question as today
(`ext.Decision.Kind` `permission`) and its answer is recorded refused,
never applied. The terminal's own `permission` dialog (kind
`permission`, a screen's source) is answered as today; a share link with
control keeps answering it.

**The answer is bound where it is written.** `Local.AnswerPermission`
claims the question (the claim of `docs/protocol.md`, Answering a
question), then, under the session's lock and at the moment the verdict
is handed to the waiting request, checks: the question is still open
and this claim holds it; `call` equals the request's; `sha256` equals
the request's digest; the call has no result yet. Any difference answers
`stale_request` and leaves the request open. Who answered is recorded
first (the question's kept result, an activity entry); the `permission`
entry's `state` becomes `allowed` or `denied` only once the held request
has delivered the verdict (the POST's reply written, or `Reply`
returned); a verdict that could not be delivered (the POST already
gone) is recorded withdrawn.

**The choices.** `allow_once` and `deny` (with an optional message to
the agent, 500 bytes). The first cut offers no "always": Claude Code's
documented prompt result has none, and an agent's remembered decision is
a standing grant beyond the call. No mid-session change of the agent's
permission mode is offered (it is a control request documented in one
reference only); the mode is the terminal's. When "always" or a mode
change comes (a later row), it is the operator's alone, and anyone who
may answer may narrow.

**When nobody answers.** It waits. Where the agent would time out by
itself, Conductor answers deny first: `PermissionWait` is the bound in
effect for the agent less one minute (for Claude Code the MCP server's
`timeout` of 8 hours that Conductor writes, or the documented 30-minute
idle default where row 4 finds `--mcp-config` does not honour it); then
denied "Nobody answered in time." No timer ever answers allow.

**When the request ends otherwise.** Every open request is withdrawn or
answered deny, its entry closed with its words, and **its held request
answered at once**: a withdrawal the session makes answers the
`/permission` POST `{"behavior":"deny","message":"Withdrawn: answered
elsewhere."}` (which Claude Code drops for a call already decided), and
for Codex or ACP `Reply` sends the protocol's own cancel, so the held MCP
call returns and frees its slot. It is never answered allow. The ends:

- the agent withdrew it (an MCP cancellation of the held call, a Codex
  `serverRequest/resolved`, an ACP `$/cancel_request`, which protocol 1
  makes optional), or the stream shows the call's result (`ToolEnded`
  for its ref, which covers a `PermissionRequest` hook of the person's or
  the folder's settings deciding first, with or without a cancellation):
  withdrawn "Answered by the agent's own rules.";
- the turn ended (`TurnEnded`) with the request still open: withdrawn
  the same way;
- the turn was interrupted: withdrawn "The turn was stopped."; for an
  ACP agent, Conductor sends `session/cancel` and answers every pending
  `session/request_permission` `{"outcome":{"outcome":"cancelled"}}`,
  not a `reject_once` selection, as tool-calls.md requires;
- the session's `Stop`, the server's stop, the child's exit: denied "The
  session ended before anyone answered.", before the child's input is
  closed;
- a move between ways is not made while any request is open (design 9),
  so none is carried;
- a managed attempt's requests are its session's: the next attempt is a
  fresh session, and nothing of an answered or open request carries
  into it.

A restarted server holds no request: the children ended with it, and an
answer that arrives after has no request to reach.

**Later, a boundary row of its own:** a managed run's permission request
as a durable decision record (a ledger migration, alone and first), then
its routing, the decide grant's surface and the phone's push. Not in
this plan (Out of scope).

### 8. Yardmaster on the protocol way (step 2)

Step 2 waits for a trust design of its own (The rows, Step 2): a crew's
member, like every session, starts in its terminal, and how it comes to
run as a chat without a person pressing Chat is that design's to settle.
The engine work it needs is planned here, so that design changes only
how a member's way is chosen. The engine keeps holding `*session.Local`
(`Launcher`, the lookup). What changes, all through `Local`'s calls
above:

- **The way of an attempt.** `LaunchSpec` gains `Way` (`"protocol"` or
  `""`), set as step 2's design says. A member runs the protocol way
  only where its agent's installed version is qualified and its run's
  Runtime has pipes, else it stays in its terminal. A managed attempt's
  way is fixed at its launch. No ledger migration: attempts are fresh
  sessions and the ledger keeps nothing new; the run's log says which
  way each attempt ran ("build, attempt 2: runs as a chat" or "in its
  terminal: this version of Claude Code can only run in its terminal").
- **A run keeps its Runtime.** A run offered to a registered Runtime
  stays there (design 2); its members stay in their terminals when that
  Runtime has no pipes, and the log says so ("in its terminal: the
  run's Runtime has no pipes").
- **Readiness is the session's answer.** `AwaitReady`
  (`internal/crew/run.go:2981`) waits on `Local.Ready()` for a protocol
  session, bounded by `protocolReadyCap` (60 s); past it, the launch
  fails with the driver's words. Nothing is "typed anyway" (`readyCap`'s
  rule stays the terminal's).
- **A prompt is taken when the agent acknowledges it.** `promptWith`
  (`run.go:1764`) sends the role prompt or the task line (`taskLine`,
  `task.go:3230`) by `Submit` and waits for its `Finished`; the member is
  marked prompted at `Accepted`, never at a write. `Unconfirmed` is the
  attempt's uncertain boundary, as a typed line with no Enter is today:
  it waits for a person and is never resent.
- **A turn's end arrives with its kind.** `working` and `done` from
  `SourceProtocol` reach `OnActivity` (`run.go:2091`), and the
  attention's kind goes with them: `OnActivity` gains a `kind string`
  argument, through `session.Options.OnActivity` (`local.go:83`), the
  api's publisher interface (`publish.go:29`) and its implementer
  `Published.OnActivity` (`internal/hostagent/uplink.go:290`), the
  hosted hub's `OnActivity` and `OnLateActivity` (`internal/signal/hub.go:38-43`,
  called at `hosted.go:2100-2102`) and the server's wiring of them
  (`internal/api/server.go`). `OnActivity` hands no member to
  `startAfter` for a `done` of kind `KindInterrupted`, and sends no
  reminder for it.
- **Messages, handoffs and answers sent during a turn wait for its
  end.** `deliver` (`handoff.go:164`), a decision's applied answer
  (`typeAnswer`, `decisions.go:2937`), the done-without-result reminder
  and the decision wait's notices go through `Submit` with
  `UnlessWaiting`, which on a protocol session writes nothing and
  returns a zero result while a turn runs; the deliverer is woken at the
  turn's end and sends again, decision answers first, its bound (10)
  kept.
- **A turn interrupt distinct from Stop.** The chat's Stop is
  `Local.Interrupt`; it ends the turn and keeps the attempt, its session
  and its conversation. Stop, cancelling a task and pausing stay what
  they are (`docs/plans/agent-harness.md`, 8: distinct actions).
- **Only a task result and its checks finish a task.** A protocol
  turn's end finishes nothing, as a `done` does not today.
- **Brakeman's turns stay in the terminal way** (Out of scope); a
  session adopted by a crew keeps its way.

### 9. The move between ways

Only for a person's own session in the first cut, only when a turn has
ended, in place: the Conductor session, its id, its links, its people,
its record and its listing stay. Never two writers. The move is the only
way a session becomes a chat.

**The route.** `POST /api/sessions/{id}/way {"to": "terminal" |
"protocol"}`, the operator's alone (`requireAdmin`, as Stop's
`DELETE /api/sessions/{id}`), refused on a switchyard. Pressed during a
turn, the move is noted and made at the turn's end (one move noted at a
time; a second press replaces it); the session says so in one line
("Moves to Chat when this turn ends."), and the `way` message tells
every viewer (design 6).

**Offered only once the session has had a turn that ended.** A move to
Chat is offered only once the session's terminal has reported a turn
that ended (attention `done` from a source that reports turns,
`ReportsTurn`): there is then a conversation to continue, and the
agent's own program has run in that folder to its prompt. Before that
the switch says "Chat opens after the agent's first reply." and the
route answers `move_refused` with those words.

**Made only when all hold at the moment of the move**, else it stays
noted and the line says why in one sentence:

- the session is a server session (not hosted), a person's own (no
  crew), not ended;
- its agent's catalog entry has a protocol, its driver's `Moves` is
  true, and the installed version is qualified (for a move to Chat);
- the agent's conversation id is known and resumable
  (`Info.AgentSession`), read **at the move** from the session (the
  stream's latest `session_id` for a chat, the hook's latest report for
  a terminal), never a remembered one;
- the turn has ended: a chat's last `turn` entry is an `end` and no
  message waits for its acceptance; a terminal session's attention is
  `done` from a source that reports turns, with no question open and
  nothing typed held;
- no work the agent started still runs after the turn (a chat's stream
  reports none: `Background(false)`); for a terminal session, nothing in
  the PTY's process group but the agent's own process;
- no permission request is open and no submission waits.

**The steps**, in the session's own order:

1. The session marks itself moving: every submission, answer and
   interrupt is refused `moving` until the move ends.
2. It stops the current child (a chat's input closed, then `Stop`; a
   terminal's `Stop`) and waits for its exit and its group's end: the
   old process proven gone, never a timer (I1).
3. The conversation stays this session's throughout: `takeConversation`
   finds it held by a live session (the registry's `Holders`), so no
   other resume can take it meanwhile.
4. It starts the other way through the same Runtime, folder, binding
   (`docs/plans/capacity.md`: a resume keeps its login binding) and
   environment, with the session recipe's `ResumeArgs` of the id read in
   the conditions (`SwapWay`, built on the parts `createLocalSession` and
   `NewLocal` already have: the terminal's screen watchers and pump are
   started by a function both call, and a chat's child is started with
   the argv of design 4). A chat's child reads the same setting sources
   the terminal program read; a terminal's start is today's.
5. It broadcasts `status` and the `way` control message; viewers draw
   the other view. The terminal's ring and screens begin empty with the
   new process. The transcript stays, with a `notice` entry at the
   boundary: "Moved to the terminal." or "Moved to Chat. Turns made in
   the terminal are not shown here."
6. If the new start fails, the session tries once to resume the way it
   came from; if that fails too, it ends with both failures' words, and
   the conversation stays resumable with Resume.

**Per agent:** Claude Code by its documentation (sessions.md, above) and
a by-hand observation in row 5's acceptance, both directions, before
its `Moves` is set; Codex after its own observation in row 10; an Agent
Client Protocol agent only where its session id is shown to be its
native one. A resume of a Claude Code session running as a background
session attaches to it or exits 1 (sessions.md); Conductor never starts
one with `--bg`, and a resume that meets one is refused "Another Claude
Code session holds this conversation."

**A resume after the session ended** (`POST /api/sessions/{id}/resume`,
or from history) starts the terminal way, as today: every session starts
in its terminal.

No live hand-off of a running turn, and no fork: a fork makes a second
conversation, and a hand-off of a running turn makes two writers.

### 10. Another machine, sync, history and export

- **Hosts.** Server sessions first. `conductor host` offers the terminal
  way only, and a hosted protocol session is a later boundary row (the
  host link). Hosts never send TRANSCRIPT.
- **The switchyard.** A server's protocol session is published only to
  a switchyard whose `registered` says `transcript` (design 6), carried
  opaquely, as output is, and its viewers served by `peer.go` (design 6).
- **Sync** (a default a maintainer may overturn): a chat's transcript
  counts as captured output (`docs/plans/sync-seams.md`, What counts as
  terminal output: the bytes Conductor captured from a session). It is
  never handed to a provider, and the copied-output check sees all of it
  for as long as it is kept: every byte the driver captured is fed to
  the index as a source of its own, `transcript:<id>`
  (`Options.OnCaptured`, `internal/api/output_guard.go`), keeping what
  the transcript keeps (its ring and parts, 10 MiB) and pruned as
  entries and parts drop and when the session is removed, so a paste of
  anything the transcript still shows is withheld or cut as a paste of a
  terminal's would be. Nothing is added to any sync allowlist.
- **History** (a default a maintainer may overturn): the journal keeps
  today's kinds only, and its record gains no field. A chat's tool steps
  and file changes reach it as the activity entries they are
  (`tool_use`, `file`); the agent's words are not kept; what a person
  sent is never kept, as today (`internal/journal/record.go`, its
  package comment).
- **Export.** The transcript is not written to disk, so nothing of it is
  exported or left out.

### 11. The app's screens, first cut

- **The connection is split from the terminal.** A composable,
  `useSessionConnection`, holds what `TerminalView.vue` holds today: the
  transport, the control handling (`TerminalView.vue:573-648`), the
  requests (files, saves, Neovim, chat, answers). `TerminalView` becomes
  a consumer of its output and size; the page, the Files tab, the
  people's chat and the reply bar read the connection. The marks the
  browser tests read (`data-transport-state`, `data-cols`,
  `data-fit-passes`, the xterm DOM) stay where they are.
- **The header's switch: Chat | Terminal**, after the session's name,
  on every session of an agent whose entry has a protocol (`AgentInfo`).
  The side the session runs is selected; the other side moves the
  session (design 9), at once when the turn has ended, else at its end,
  with one line saying which, and before the first reply "Chat opens
  after the agent's first reply." Shown to a link's viewer as a label of
  the way, with no action. Below `sm` it is two items in More (the
  header keeps Back, name, People and More).
- **The chat**, `ChatView.vue`, in the body's main pane where
  `TerminalView` is: quiet by design.
  - What the person wrote, and what the agent wrote, through
    `renderMarkdown` (raw HTML never rendered) and `CodeBlock`.
  - A turn's tool steps folded into one line ("12 steps") that opens to
    the rows (`ToolStepRow.vue`: the tool and its summary in the code
    font, its status as `StatusMark`'s word; opened, its input and output
    read on demand by `transcript_get part`).
  - A file change as one line ("`main.go` changed.") that opens to a
    small diff (`InlineDiff.vue`: at most 40 lines of the part, no
    Monaco, and a link that opens the editor's diff tab).
  - While a turn runs, the steps' line reads "Working." No token counts,
    timings or ids on the surface; they are in the entries for later
    rows.
  - A `notice` as a one-line system message.
- **The permission question**, `PermissionQuestion.vue`, amber: what the
  call is in plain words ("Run a command?", "Change a file?", "Read a
  file?", "Use a tool?"), the exact command or path in one line of the
  code font that opens to the whole input, and **Allow once** and
  **Deny**. Allow once is enabled only once the whole input is drawn: a
  one-line input is drawn whole at once; a longer one once it is opened.
  The input and the summary are drawn as text, never applied:
  `utils/hiddenMarks.ts` holds the set of characters a person cannot see
  (`runOrigin.ts`'s `HIDDEN` classes, which it then imports from there,
  less the line feed, which is drawn as a line break), and every one is
  drawn as a visible mark of its own, its `\u` escape on a marked
  background, a carriage return as `\r`; when any is present, one line
  says "This call holds characters that are not shown as typed." The
  digest and the answer are over the bytes, not over what is drawn.
  Where the agent's request names its folder (Codex), the opened input
  shows it; where it does not, no folder is shown. For anyone but the
  operator: the question, and "Only the person who runs this server can
  answer."
- **The box to write in** is `ChatComposer`'s, bound to 4,096 bytes, its
  Send the work area's one filled button, with **Stop** (an
  `IconButton`, "Stop this turn" in its tooltip) while a turn runs. Its
  help is one line, shown only where the session's `verbatim` is true:
  "Sent as written: `@file` and `/command` are not expanded here."; where
  it is false: "Messages starting with `/` may run as the agent's
  commands." A message sent during a turn shows "Sends when the turn
  ends."; one not acknowledged shows "Sent, not yet confirmed.", never
  sent again.
- **People.** On every session's page, the people's chat and the roster
  are one rail item, **People** (the chat's panel already shows who is
  here), its sheet below xl too; `?tab=chat` and `?tab=people` both open
  it; its palette row reads "Open People". The wire's and the code's
  `chat` stay.
- **What stays.** Activity, Files, Changes, the reply bar for a question
  that is not a permission request (for one, the bar says "Answer it in
  the chat." and scrolls to it), Share, Stop session.
- **The note on the vendor's terms** is shown once, the first time a
  person presses Chat in this browser (kept in the browser's storage, a
  per-viewer convenience): "Chat runs the agent's own non-interactive
  mode with its existing sign-in; the agent's terms decide how that may
  be used and counted." with a link to the agent's site from its catalog
  entry.
- **Other pages that draw a session.** The join page
  (`pages/join/[token].vue`), a run's join grid (`components/JoinCrewGrid.vue`),
  the yard and the roundhouse draw `ChatView` for a protocol session by
  `welcome.way`; a tile (`SessionTile.vue`) shows "Runs as a chat." and
  its last line of words, never an empty terminal.
- **Design rules.** Nuxt UI components; the brand palette; amber only
  for the question that waits on a person; the code font for tools,
  paths, commands and outputs; every new string one short plain
  sentence; nothing cut that must be read, nothing sideways at 1440 x
  900, 1280 x 720 and 390 x 844 (the phone's own chat design is later).
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
  session id and takes `--resume`; the bash stub, in its terminal mode,
  resumes the same file, so a spec opens a terminal session, has one
  turn, presses Chat and back. Catalog entries in
  `web/e2e/conductor.e2e.json`: `claude-proto`, `asker-proto`,
  `proto-long`.

### 12. What a share link sees

A chat's viewer through a link sees what a terminal's viewer sees today,
as a chat: a view link and a control link alike see every entry (what
was written, tool steps and their outputs on open, file changes and
their diffs, permission questions with their state). A message that
answers a question shows no text to anyone but its sender (#364's
rule), and no typed answer is echoed to anyone. A control link may send
messages and press Stop, as it may type and press keys in a terminal
today. No link answers a permission request (design 7) or moves a
session (design 9). A decision link is a control link here. A decide
principal has no live connection and sees nothing of a transcript. The
Share dialog says, for a chat, in one sentence: "People with this link
can read the whole chat, including what its tools read and printed."

### 13. Errors and their words

Every string is one short sentence.

| Where | Words |
|---|---|
| `Input`, `resize` on a chat (`not_terminal`) | "This session is a chat: it has no terminal." |
| `interrupt` on a terminal session (`not_protocol`) | "Stop is for a chat; use the agent's own key in its terminal." |
| `interrupt` with no turn (`no_turn`) | "The agent is not working on anything." |
| the switch for an unqualified version | "This version of Claude Code can only run in its terminal." |
| the switch before the first reply | "Chat opens after the agent's first reply." |
| a start that never became ready (back to the terminal) | "Claude Code did not start a chat." |
| a signed-out agent (back to the terminal) | "Claude Code is not signed in; sign in from its terminal." |
| a line the driver cannot read | "Claude Code sent something Conductor cannot read; the session ended." |
| a line past the bound (`notice`) | "A step's output was too large to show." |
| a message not acknowledged (`unconfirmed`) | "Sent, not yet confirmed." |
| a message during a turn | "Sends when the turn ends." |
| a full queue (`too_many_requests` on `submit`) | "Too many messages are waiting." |
| a move pressed during a turn | "Moves to Chat when this turn ends." / "Moves to the terminal when this turn ends." |
| a noted move that cannot be made (`move_refused`) | "A request is waiting for an answer." / "Background work is still running." / "Crew members stay in their terminals." / "This agent cannot move its conversation yet." / "Sessions on another machine stay in their terminal." |
| the session moving (`moving`) | "The session is moving; try again in a moment." |
| a permission answer from a non-answerer (`not_answerer`) | "Only the person who runs this server can answer." |
| a permission answer that no longer matches (`stale_request`) | "This request changed or was answered." |
| an `answer` or a line naming a permission request (`permission`) | "Answer this request with its own buttons." |
| a request's entry, ended | "Answered by the agent's own rules." / "The request was lost." / "The turn was stopped." |
| what the agent is told when Conductor denies | "Conductor keeps agents out of its own files." / "Too many requests are waiting; this one was denied." / "Nobody answered in time." / "The session ended before anyone answered." / "Not a call this session is making." / "Already asked." / "Conductor cannot show this call exactly." / "This call's input is too large for Conductor to show it whole." / "Conductor lost this request." / "Withdrawn: answered elsewhere." |
| `transcript_gone` | "That part of the chat is no longer kept." |
| an older switchyard | "Chats are not published: the switchyard is older than this server." |
| another Claude Code session holds the conversation | "Another Claude Code session holds this conversation." |

## Sign-in and the vendors' pages

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

**The pages the maintainers read before deciding**, each read on
2026-11-24. This plan quotes none of them, characterises none of them,
and draws no legal conclusion; which interfaces Conductor builds on, and
for which sign-ins, is the maintainers' decision (Decision before any
builder: answered for Claude Code on 2026-11-24).

- Anthropic: Claude Code, Legal and compliance
  (https://code.claude.com/docs/en/legal-and-compliance.md); Agent SDK
  overview (https://code.claude.com/docs/en/agent-sdk/overview.md); Run
  Claude Code programmatically (https://code.claude.com/docs/en/headless.md);
  Consumer Terms of Service (https://www.anthropic.com/legal/consumer-terms);
  Commercial Terms of Service (https://www.anthropic.com/legal/commercial-terms);
  Usage Policy (https://www.anthropic.com/legal/aup).
- OpenAI: Codex app-server (https://learn.chatgpt.com/docs/app-server.md);
  Codex non-interactive mode (https://learn.chatgpt.com/docs/non-interactive-mode.md);
  Sign in with ChatGPT, plan usage for open-source apps
  (https://developers.openai.com/siwc/token-sharing-open-source.md) and
  its quickstart (https://developers.openai.com/siwc/quickstart.md);
  Terms of Use (https://openai.com/policies/terms-of-use/); Using your
  ChatGPT plan in other apps and sites
  (https://help.openai.com/en/articles/20001542-using-your-chatgpt-plan-in-other-apps-and-sites).
- Google: Antigravity FAQ (https://antigravity.google/docs/faq.md).
- GitHub: Copilot CLI licence
  (https://github.com/github/copilot-cli/blob/main/LICENSE.md); Copilot
  CLI ACP server (https://docs.github.com/en/copilot/reference/acp-server).

## Events, orderings and invariants

**A chat's life:**

| From | Event | To |
|---|---|---|
| a terminal session at rest after a turn | the move: the terminal stopped, `StartPipes` returned, `Begin` called | `starting` |
| `starting` | `Begin` returned nil | `ready` (`Ready()` closed) |
| `starting` | `Begin` failed, the shape check failed, a not-signed-in report, or `protocolReadyCap` passed | the terminal way started again on the same id, with one sentence |
| `ready` | a queued message written, then `Accepted` | `in turn` once `TurnStarted` (attention `working`) |
| `in turn` | a permission request taken | `in turn` (attention `needs_input`, kind `permission`, the oldest open request shown) until answered or withdrawn |
| `in turn` | `TurnEnded` | open requests withdrawn, each held request answered; `ready` (attention `done`, or `done` kind `interrupted`); queued messages written in order; a noted move made if its conditions hold |
| `ready` | the move back | `moving`, then the terminal's start |
| any | `Stop`, the child's exit, a line it cannot read | open requests denied, then ended (an exit the session itself caused to restart a child is not an end) |

**Invariants**, each a sentence a test holds:

- **I1. One writer per conversation.** While a session's child runs, or
  while it is moving, no other session can start a process on its agent
  conversation: `takeConversation` refuses, and the move starts the new
  child only after the old one's exit and its group's end.
- **I2. A terminal session is unchanged where it matters.** With no chat,
  a terminal session's frames, attention, submissions and records, and
  the terminal's own components and DOM marks, are `main`'s; the only
  page changes are the People item and the header's switch, and the
  existing tests that change for them are the ones row 7 lists.
- **I3. Terminal-only calls never write to a protocol child.** `Input`,
  `Resize` and `ResizeWith` on a chat return `ErrNotTerminal` and write
  nothing; the connection stays open.
- **I4. A message is sent once.** A message is written to the agent's
  input at most once; it is marked accepted only by the agent's
  acknowledgement naming it (or, where the agent names none, by the
  next replay in write order), and one not acknowledged within
  `AcceptWait` is marked unconfirmed and never written again by anyone.
- **I5. Entries are in the stream's order.** Entries' `seq` rise by one
  in the order the driver handed the events, which is the order the
  agent's output gave them; a revision keeps its `seq` and takes a new
  `mark`.
- **I6. State before viewers.** Each event changes the session's state
  (attention, the questions, the ring) before any viewer is sent its
  frame, so a viewer that acts on a frame meets the state that frame
  shows.
- **I7. No screen rule fires on a chat.** Attention from
  `SourceProtocol` is never overridden or withdrawn by a rule that reads
  a screen.
- **I8. A permission request names a call the stream announced.** A
  request is taken only for a call of the current turn that the stream
  showed, before the request or within `AnnounceWait` after it, with the
  same name and input digest, not yet resulted, one per call; every
  other request is denied, and none waiting is ever answered allow.
- **I9. An answer reaches only the request it was given for.** A verdict
  is handed to a request only when the question, its claim, the call id
  and the digest all match and the call has no result, at the moment of
  the hand-over, after who answered is recorded; the request is recorded
  allowed or denied only once the verdict was delivered.
- **I10. Only the operator answers a request put to Conductor.** No
  share link, decision link, decide principal, router, provider,
  Brakeman or agent token reaches a verdict, by any transport, by a
  choice or by a line. A call the agent's own configuration (the
  person's or the folder's) allows without asking never reaches
  Conductor, exactly as it never reaches a person in the terminal.
- **I11. Nothing silent allows, and no request is left held.** No
  timer, bound, end, restart, move, withdrawal or cancellation ever
  answers a permission request allow; each answers deny, `cancelled`
  (ACP) or withdraws, with its words, and answers the held request at
  once, so no held call keeps a slot.
- **I12. A turn's end finishes no task.** A protocol turn's end, done
  or interrupted, never finishes a task or starts a dependent of a
  managed run; an interrupted turn starts no member of a legacy run.
- **I13. The transcript never leaves the machine, and the check sees all
  it keeps.** No sync document, provider call, export or journal entry
  carries an entry's text or a part; every captured byte reaches the
  copied-output index under `transcript:<id>`, kept as long as the
  transcript keeps it.
- **I14. A frame an older peer would close on is never sent to it.**
  TRANSCRIPT goes to a switchyard only after its `registered` said
  `transcript`; no host sends it.
- **I15. A view role writes nothing through the relay.** A relayed
  viewer whose role is not control passes only `hello`, `ping`, `chat`,
  `file_get` and `transcript_get`.
- **I16. The way a session runs is said.** `Info.Way`, the welcome's
  `way`, the `way` message and the run's log name it; a fallback says
  why in one sentence.
- **I17. Conductor holds no vendor credential.** No route, environment,
  record or log of a chat carries a vendor's credential or token.
- **I18. Open requests hold the attention.** While any permission
  request is open, the attention shows the oldest; no other question
  replaces or withdraws it, and every open one can be answered.
- **I19. A message is sent as written where the driver can say so.** A
  driver whose protocol documents an as-written mark (Claude Code's
  `client_composed`) sets it on every message, and no `@path` is
  expanded and no `/command` runs from it; a driver with no such mark is
  declared only once its row states what its agent does with them, and
  its sessions' `verbatim` is false.
- **I20. An open request's question never disappears, and an ended one
  is never drawn open.** An open request's entry and input part are
  never dropped, every replay carries every open request's entry, and a
  reconnect is sent every entry changed since the mark it holds.
- **I21. Every session starts in its terminal.** No launch, resume or
  constructor starts a session in the protocol way; a chat is entered
  only by a move from a terminal session that has had a turn that ended.

## The rows

Three steps, each usable by itself:

- **Step 1, Chat or Terminal for a person's own Claude Code session**
  (rows 1 to 7). At its end a person presses Chat on a Claude Code
  session after its first reply, works in the chat, answers its
  permission questions, and presses Terminal to go back.
- **Step 2, crews as chats** (rows 8 and 9). It waits for a trust design
  of its own, with an adversarial read of its own. A start that is a
  chat from its first moment has no terminal behind it: no agent program
  has asked its own trust question there, and a `-p` start uses a
  folder's hooks, `env` block and `.mcp.json` servers without one
  (https://code.claude.com/docs/en/permissions.md, What runs before you
  trust a folder). Such a start would need the user's setting sources
  alone (`--setting-sources user --strict-mcp-config`, which keep out
  the project's settings files, `.mcp.json`, `CLAUDE.md` and rules,
  skills, and, by the agent SDK's page, its agents and commands:
  https://code.claude.com/docs/en/agent-sdk/claude-code-features.md;
  https://code.claude.com/docs/en/memory.md), or a design of its own.
  One candidate: a member starts in its terminal to the agent's prompt,
  then moves. At step 2's end a crew's members run as chats, and a
  default of Chat may be offered.
- **Step 3, more agents** (rows 10 and 11), each after the maintainers'
  decision on its agents. At its end Codex, then the first Agent Client
  Protocol agents, run as chats the way step 1 does.

Each row is a unit of work built with the checks of its surfaces
(`docs/checks.md`) and leaves `main` green. Grouping into pull requests
follows `AGENTS.md`: **PR-A** is row 1 alone (a seam of the public
`ext`); **bundle 1** is rows 2, 3, 4 and 5, with row 3's relay allow
list a part of its own with an adversarial review of its own (the
links); **PR-B** is row 6 alone (who may answer); **bundle 2** is row 7,
which ends step 1 (the switch is mounted on the page only by row 7);
**bundle 3** is rows 8 and 9, after step 2's trust design; rows 10 and
11 are later pull requests. No row migrates the ledger, and no row
changes the journal's record.

Sizes are hours of one builder's work with its review and CI, from
comparable merged rows: a wire change across `internal/hostagent`,
`internal/notify` and `internal/api` of about 2,000 to 3,500 lines took
3.5 to 4 hours from first commit to merge (#978, #981); a new question
kind across `internal/catalog` and `internal/session` about a day
(#958); an end-to-end walk on existing seams about 8 hours (#1045); a
page's new flow of about 750 lines about 8 hours (#962); a bundle of
screens with its specs, several builders, about 5 hours of wall time
(#994). The times include waiting, so they are estimates.

| Row | Step | Title | Touches | Depends on | Boundary | Size |
|---|---|---|---|---|---|---|
| 1 | 1 | ext, proc, local, exttest, api: a child with pipes beside the Runtime, and its guard | `ext`, `ext/local`, `ext/exttest`, `internal/proc`, `internal/api/extruntime.go`, `internal/session/sessiontest/fakeruntime.go`, `docs/plans/runtime.md` (one sentence), `docs/repository-map.md` | none | yes (an `ext` seam): PR-A alone | 8 h |
| 2 | 1 | session: the protocol way against a fake child, the transcript ring with its marks and pinning, the message queue, the refusals | `internal/session`, `internal/session/sessiontest` (a fake protocol driver) | none (beside row 1) | no | 14 h |
| 3 | 1 | proto, session, api, hostagent, signal, web: TRANSCRIPT, the mark resume point, the registered word, the switchyard's viewers, a protocol submit through `Finished`, the relay's allow list | `internal/proto`, `internal/session` (attach), `internal/api` (`ws_viewer.go`, `ws_host.go`), `internal/hostagent` (`peer.go`, `uplink.go`), `internal/signal/hosted.go`, `web/app/utils/protocol.ts`, `web/app/utils/transport/`, `docs/protocol.md` | row 2 | the allow list: a part with its own adversarial review | 11 h |
| 4 | 1 | agentproto, agents, catalog, api, cli, notify, web: the Claude Code driver, its gate and fixtures, a chat's child, the hook reports dropped, the listing's fields, the stub agent | `internal/agentproto` (new), `internal/agents` (the protocol MCP config asset), `internal/catalog` (the field, Claude Code's entry), `internal/config`, `internal/session` (`PipesChild`), `internal/api` (`sessions.go`'s child start, `catalog.go`, `attention.go`, `output_guard.go`), `internal/cli` (`notify.go`, `mcp.go`'s listing), `web/app/utils/protocol.ts`, `web/app/composables/useSessions.ts` (`SessionInfo`, `AgentInfo`), `docs/protocol.md`, `web/e2e/stub-protocol.mjs`, `web/e2e/server.ts` | rows 1, 2, 3 | no | 14 h |
| 5 | 1 | session, api, proto, web: the move between ways, the connection split | `internal/session` (`SwapWay`, the terminal start extracted), `internal/api` (the move route), `internal/proto`, `web/app/utils/protocol.ts`, `docs/protocol.md`, `web/app` (`useSessionConnection.ts`, `TerminalView.vue`, the switch's component, not yet mounted), `web/e2e`, `docs` | row 4 | no | 14 h |
| 6 | 1 | session, api, cli, proto: the permission request | `internal/session` (the list, the binding, the withdrawals), `internal/api` (the route, `router.go`, `exthost.go`, `inbox.go`, `lane.go`, `compare_doors.go`, `guide.go`), `internal/cli` (`mcp.go`'s concurrent loop and the tool with its own client, `notify.go`'s `--protocol-floor`), `internal/agents` (the floor's settings assets), `internal/proto`, `web/app/utils/protocol.ts`, `docs/protocol.md` | rows 3, 4, 5 | yes: PR-B alone, adversarial read | 18 h |
| 7 | 1 | web: Chat, the switch mounted, the permission question, People, the other pages | `web/app` (`ChatView.vue`, `ToolStepRow.vue`, `InlineDiff.vue`, `PermissionQuestion.vue`, `utils/hiddenMarks.ts`, `utils/runOrigin.ts`, `utils/transcript.ts`, the session page, `pages/join/[token].vue`, `JoinCrewGrid.vue`, `pages/yard.vue`, `pages/roundhouse.vue`, `SessionTile.vue`, `utils/inspector.ts`, the palette's rows), `web/e2e`, `docs/features.md` | rows 5, 6 | no | 18 h |
| 8 | 2 | catalog, api, web, and step 2's trust design: the declaration on the Agents page, the digest, how a member comes to run as a chat | `internal/catalog`, `internal/api` (`catalog.go`, `dispatch_plans.go`), `web/app` (`utils/agentForm.ts`), `web/e2e`, `docs` | row 7; step 2's trust design approved | the trust design: an adversarial read of its own | 10 h |
| 9 | 2 | crew, session, api, signal, hostagent, web: Yardmaster on the protocol way, with a live crew | `internal/crew` (`run.go`, `task.go`, `handoff.go`, `decisions.go`, `messages.go`, `runtime.go`), `internal/session` (`Options.OnActivity`), `internal/api` (`runs.go`, `publish.go`, `server.go`), `internal/signal` (`hub.go`, `hosted.go`), `internal/hostagent/uplink.go`, `web/e2e`, `docs/protocol.md` (run log rows) | rows 6, 8 | no | 16 h |
| 10 | 3 | agentproto, catalog, agents: Codex's app server | `internal/agentproto/codex.go`, `internal/catalog/defaults.go`, `internal/agents/codex.go` (`codexMCPEnv` gains `CONDUCTOR_PROTOCOL`), `web/e2e` (a second stub mode) | row 7; the maintainers' decision on Codex | no | 18 h |
| 11 | 3 | agentproto, catalog: the Agent Client Protocol client and its first native agents | `internal/agentproto/acp.go`, `internal/catalog/defaults.go`, `web/e2e` | row 7; the maintainers' decision per agent | no | 18 h |

Step 1 (rows 1 to 7): about 97 hours. Step 2 (rows 8 and 9): about 26
hours, after its trust design. Step 3 (rows 10 and 11): about 36 hours.
In all about 159 hours of builders' work.

**Built at the same time.** Row 1 beside row 2 (they share only
`sessiontest`, in different files; the second to merge rebases). After
row 2: row 3 beside row 4's driver work (both touch `internal/api`, in
different files; built in worktrees of their own feeding bundle 1's
branch); row 4's launch work lands after row 3. Row 5 after row 4; row 6
after row 5; row 7 after row 6. Rows 10 and 11 beside each other, after
row 7. Rows 8 and 9 after step 2's trust design.

### Row 1: a child with pipes beside the Runtime, and its guard

**Scope.** `ext.PipeStarter`, `ext.PipeSpec`, `ext.Pipes` (design 2);
`proc.StartPipes` (two pipes, its own group, stderr counted and
dropped, `Stop` with grace then the group's kill, reaped before it
returns); the native Runtime's implementation; `guardPipes` in
`internal/api/extruntime.go`, guarded as `StartTerminal` is and its
`Pipes` wrapped as `guardTerminal` wraps a Terminal; `FakeRuntime`'s
script and `FakeTerminal`'s sibling `FakePipes`; `RuntimeExec`'s checks;
the boundary test's `runners` entry; one sentence in `runtime.md`'s Why
and what happens. **Must not touch** `internal/session`'s `Local`, the
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
  implements it.
- `TestRegisteredPipeStarterIsOfferedPipes` (`internal/api`): a
  registered `FakeRuntime` with pipes is a `PipeStarter` through the
  guard, and one without is not; a `StartPipes` that panics is recovered
  and its error quieted; breaks if the check is made on the bare guard,
  if the guard forwards a method its inner Runtime lacks, or if the
  start runs under `extCall`'s wait.
- `TestRuntimeExecPipes` (`ext/exttest`, through `RunAll` with the
  native Runtime): the contract's checks pass.
- `TestOnlyTheRunnersStartOrSignalAProcess` (the boundary test, as it
  stands, with the new runner): breaks if `StartPipes` is used outside
  `ext/local` and the fake.

No UI. **By hand:** none. **Boundary:** a seam of the public `ext`,
PR-A alone. **8 h.**

### Row 2: the protocol way inside the session, against a fake child

**Scope.** `session.Child`, `session.Protocol`, `session.ProtocolEvents`,
`session.Message`, `ToolStep`, `ToolResult`, `FileChange`, `Usage`,
`PermissionRequest` and `Verdict` types (the last two used in row 6);
the protocol way's state inside `Local` (no constructor starts in it;
row 5's `SwapWay` enters it, and the tests enter it through a
`sessiontest` seam); the read goroutine; `Ready`, `Interrupt` with its
`ErrNoInterrupt` restart, `Way`; the transcript ring with `seq` and
`mark`, parts, their bounds and the pinning of open requests (design
5); `SourceProtocol` in `ReportsTurn`, `KindInterrupted`; `Submit`'s
protocol branch with its queue and its bound, `ErrSubmitPending`,
`Finished` and `AcceptWait`, and its refusal of a permission question;
the refusals `ErrNotTerminal`, `ErrNotProtocol`; `Stop`'s order;
`Options.OnCaptured` fed; activity entries for steps and files;
`Background`. `sessiontest.FakeProtocol` (a driver whose events a test
hands in, and which records what it was sent). **Must not touch** the
terminal path (`pump`, the screen watchers, `submit.go`'s terminal
branch), `internal/api`.

**Tests.** Go race (`internal/session`):
- `TestProtocolSubmitIsAcceptedOnce` (I4): a message written once,
  `Entered` at `Accepted`; a second `Accepted` for it changes nothing;
  breaks if `Submit` writes again on a timeout.
- `TestProtocolUnconfirmedNeverResent` (I4): no `Accepted` within a
  shortened `AcceptWait` given through `Options`: `Unconfirmed`, and the
  fake records one write; breaks on a retry.
- `TestProtocolSubmitDuringTurnIsQueued`: a message sent during a turn
  returns `ErrSubmitPending` at once, is not written until `TurnEnded`,
  then is written once, in order with a second, and is not unconfirmed
  while it waits; breaks if `Submit` blocks or writes during the turn.
- `TestProtocolQueueBound`: the seventeenth queued message answers
  `ErrTooManyQueued`.
- `TestSubmitRefusesAPermissionQuestion` (I10): a submission whose
  `Question` names an open permission question is refused
  `ErrPermissionCard` before any claim, with `UnlessWaiting` and
  without; breaks if the claim is taken first.
- `TestProtocolRefusesTerminalCalls` (I3): `Input`, `Resize`,
  `ResizeWith` answer `ErrNotTerminal` and the fake records nothing.
- `TestProtocolTurnReportsAttention`: `TurnStarted` gives `working`,
  `TurnEnded` `done`, both `SourceProtocol`, `ReportsTurn` true.
- `TestInterruptedTurnIsNotDone` (I12): `TurnEnded("interrupted")` gives
  kind `interrupted`.
- `TestInterruptWithoutReceiptRestarts`: a driver answering
  `ErrNoInterrupt` makes the session end the child and start it again
  on the same conversation with a `notice`, the session still open;
  breaks if that exit ends the session.
- `TestProtocolAttentionIgnoresScreenRules` (I7): with every screen
  rule's trigger fed, a protocol `working` stays.
- `TestTranscriptOrderIsTheStreams` (I5): 1,000 events handed over by
  the fake driver, with a fake clock that runs backwards between some,
  give `seq` 1 to n in hand-over order, and a revision a new `mark` and
  the same `seq`; breaks if entries are ordered by time or a revision
  keeps its mark.
- `TestTranscriptBounds` (I20): past 1,000 entries or 2 MiB the oldest
  go; past a part's 256 KiB it is cut with its size; past 8 MiB of parts
  the oldest part answers `transcript_gone`; an open request's entry
  survives 1,000 later entries and 2 MiB of them, and its input part
  survives 8 MiB of later parts; once the request is answered both go on
  the next overflow.
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
- `TestProtocolCapturedReachesOnCaptured` (I13): every captured byte
  reaches `Options.OnCaptured`.
- `TestProtocolToolStepsAreActivity`: a tool step and a file change make
  the activity entries `tool_use` and `file` the hook mappers make.
- `TestReadIsNotBlockedByARequest`: after an in-stream `Permission` the
  fake's next event is applied before any answer; breaks if
  `Permission` waits on a person.
- I2: no existing test file of `internal/session` changes.

No UI. **By hand:** none. **14 h.**

### Row 3: the wire

**Scope.** Design 6 whole but `permission_answer` (row 6) and `way`,
`noted`, `moving` and `move_refused` (row 5): TRANSCRIPT and its bounds;
`transcript_get`, `interrupt`, the resume point by `mark` with the open
requests first, `welcome`'s fields, the error codes; the protocol replay
in `AttachWith`; dispatch in `ws_viewer.go`, with a protocol submit
answered through `Finished`, its queue's refusal, and the read loop free
past the enqueue; `peer.go` serving a published server session's
viewers by the session's way, the same submit rule, and `not_protocol`
on a terminal session; `Registered.Transcript` set by the server;
`Uplink` publishing a protocol session only to a switchyard that says
it; `DecodeRelay` and `limitFor`; the relay's allow list; the browser's
dispatch (`transport/base.ts`: a TRANSCRIPT frame to `onTranscript`, the
new messages on `TerminalTransport`) and types; `docs/protocol.md`'s
rows. **Must not touch** the session's way code beyond `AttachWith`, the
app's pages.

**Tests.** Go race:
- `TestTranscriptFrameBounds` (`internal/proto`, with
  `testdata/transcript-max.json`): the largest entry fits
  `MaxTranscriptEntry`, the largest page and part fit `MaxTranscript`,
  one byte past is refused by `Decode`.
- `TestTranscriptGetBounds`: `reqId` 1 to 32 for `transcript_get` and
  `interrupt`, `limit` at most 100, `name` of the closed set.
- `TestTranscriptRelayable`: `DecodeRelay` takes TRANSCRIPT.
- `TestRegisteredOmitsTranscript`: an older server's `registered` byte
  for byte without the word.
- `TestReplayAfterResumePoint` (`internal/session`): `transcriptAfter`
  naming mark 40 replays every entry whose mark is above 40, a request
  that was withdrawn and a tool that ended during the drop among them
  (drawn ended, not open or running), after `ready` and the activity and
  chat replays, then live; a point the ring no longer covers sends the
  newest within 256 KiB with `more`; an open request's entry is sent
  first whatever the point and the budget (I20).
- `TestTranscriptGetInFlight` (`internal/api`): a fifth request in
  flight answers `too_many_requests`; a page of a dropped range answers
  `transcript_gone`.
- `TestInterruptRoles`: a view role's `interrupt` answers `read_only`,
  a control's reaches the driver.
- `TestQueuedMessageFreesTheSocket` (`internal/api`): with the turn held
  at the fake's gate, an `interrupt` sent after the `submit` is answered
  `interrupt_ok` before any reply to the submit; `submit_ok` arrives
  only after `TurnEnded` and `Accepted`; nothing waits on
  `submitTimeout`; breaks if the read loop is held by the submission.
- `TestQueuedSubmitBound` (`internal/api`): the seventeenth `submit`
  queued during a turn is answered `error{too_many_requests}`.
- `TestNoTranscriptToAnOlderSwitchyard` (I14, `internal/hostagent`): a
  protocol session is not published to a switchyard whose `registered`
  lacks the word, and Settings' line says so; with the word, its frames
  pass.
- `TestUplinkServesProtocolMessages` (`internal/hostagent`): a peer of a
  published protocol session gets the entries after its
  `transcriptAfter` once `ready` arrives, a `transcript_get` with `part`
  returns a `part` page, a control peer's `interrupt` reaches the driver
  and a view peer's answers `read_only`; breaks on an unconditional
  refusal in `peer.go`, or a `peer.attach` that drops `transcriptAfter`.
- `TestHostRefusesProtocolMessagesOnATerminal` (`internal/hostagent`):
  `interrupt` and `transcript_get` to a terminal session answer
  `not_protocol`.
- `TestRelayAllowListForViewRoles` (I15, `internal/signal`): every
  CONTROL `t` of `proto` (listed from its constants, so a new one joins
  the test) is refused for a view role but the five; breaks if the old
  deny list is kept.
vitest: `transport/base.test.ts` gains `transcriptFrameDispatched` (a
TRANSCRIPT frame reaches `onTranscript`; an unknown type is still
dropped) and `transcriptChunked` (a chunked frame reassembled).
Playwright: none yet (row 5). **By hand:** none. **Boundary:** the
relay's allow list is a part with its own adversarial review (links).
**11 h.**

### Row 4: the Claude Code driver

**Scope.** `internal/agentproto` with `Names`, `Check`, the Claude Code
driver (`claude.go`: the argv of design 4 with exactly one `--settings`
and no `--setting-sources` or `--strict-mcp-config`; the stream's events
mapped to `ProtocolEvents`, the `system` task events to `Background`;
`Begin` with `initialize` alone; `Send` as a stream-json user message
with `client_composed: true` and a ref the replay names, matched in
order where the replay names none; `Verbatim` true; `Interrupt` by the
control request, `ErrNoInterrupt` without the receipt capability; the
line skip past `MaxProtocolLine`; the 8 MiB input count; the version
gate at `MinVersion` (at least 2.1.248) and the shape check; the
not-signed-in detection); the catalog field and its validation, Claude
Code's built-in entry, and `AgentInfo.protocol` (read-only, from the
catalog route); `SessionInfo`'s `way`, `protocol` and `verbatim` in the
three places; the protocol's MCP config (`claude-protocol-mcp.json`,
`timeout` 8 hours) as an asset; `session.PipesChild`; the start of a
chat's child for a session (`internal/api`, the piece `SwapWay` calls in
row 5) through `StartPipes`, with `CONDUCTOR_PROTOCOL=1` and the
environment without `TERM`; `conductor notify` sending nothing for a
hook payload with `CONDUCTOR_PROTOCOL` set, and the attention and events
routes dropping hook-shaped reports for a protocol session; the
copied-output feed `transcript:<id>`; the recorded fixtures under
`internal/agentproto/testdata/claude/` (taken by hand from a real run at
`MinVersion`, with every account, organisation and email field removed);
the stub agent `stub-protocol.mjs`. Until row 6 the permission tool and
the floor are not passed, and a call that needs approval is denied by
Claude Code itself (documented: Manual `-p` with no host "is denied
unless something approves it"). **Must not touch** the terminal way's
launch, the launch route, the app's pages.

**Tests.** Go race:
- `TestClaudeArgvIsTheDocumentedOne` (`internal/agentproto`): the argv
  of a chat's child and of a yolo session's, element for element; breaks
  if `--bare`, `--permission-prompt-tool stdio`, `--setting-sources` or
  `--strict-mcp-config` appear, if `--permission-mode` is missing, or if
  `--settings` appears more than once.
- `TestClaudeSendIsVerbatim` (I19): a sent message (`@<data dir>/x` and
  `/help`) reaches stdin with `client_composed: true` and the text
  unchanged; breaks without the field.
- `TestClaudeReplayNamesTheRef` (I4): a replay carrying the written
  `uuid` is that message's `Accepted`; with the fixture's form where it
  carries none, replays match writes in order; breaks on a time match.
- `TestClaudeFixturesReplay`: each recorded fixture fed to `Read` gives
  its expected events (a turn with words, a tool step with its result, a
  file edit with counts, background work, a result with usage).
- `TestClaudeUnknownEventsIgnored`: an event type and a field the
  fixtures lack change nothing.
- `TestClaudeLineBound`: a line of 16 MiB plus one is skipped to its end
  with the `notice` and the session reads on; a line that is not JSON
  ends `Read` with its error.
- `TestClaudeGate`: a probed version below `MinVersion`, or none, keeps
  the session in its terminal with its sentence; a stub that answers no
  `initialize`, or fails the shape check, goes back to the terminal well
  inside `protocolReadyCap`; the account-free fixture's answers pass the
  shape check; a first `result` with the authentication error ends the
  child and goes back.
- `TestClaudeInterruptWithoutReceipt`: a stub whose `system/init` lacks
  `interrupt_receipt_v1` gives `ErrNoInterrupt`; breaks if SIGINT is
  sent.
- `TestClaudeInputCountResumes`: past 8 MiB written, the next turn's end
  ends the child and resumes the same id.
- `TestFixturesHoldNoAccount`: no fixture holds an email-shaped string
  or an `account` object.
- `TestNoVendorCredentialInEnvOrRecord` (I17, `internal/api`): a chat
  child's environment, `Info`, the session's record and the server's log
  hold no `ANTHROPIC_*`, `CLAUDE_CODE_OAUTH_TOKEN` or `OPENAI_*` value
  Conductor put there, and no credential file's path.
- `TestChatChildThroughPipes` (`internal/api`): the child starts through
  `FakeRuntime`'s `StartPipes`, never `StartTerminal`, in the terminal's
  folder, with `CONDUCTOR_PROTOCOL=1` and no `TERM`.
- `TestOldTranscriptTextIsStillOutput` (I13, `internal/api`): text a
  chat captured more than `ScrollbackBytes` back, still in its
  transcript, is judged output by the copied-output check under
  `transcript:<id>`; once the transcript drops it, and when the session
  is removed, it is pruned; breaks if the transcript is fed as `ring:`.
- `TestProtocolSessionIgnoresHookReports` (`internal/cli`): with
  `CONDUCTOR_PROTOCOL` set and a `PermissionRequest`, `Stop` and
  `PostToolUse --files` payload on stdin, the test target receives
  nothing; breaks without the check.
- `TestProtocolSessionDropsHookShapedReports` (`internal/api`): a
  `needs_input` kind `permission` report and a `file` event with the
  agent token on a protocol session leave the attention and Activity
  unchanged, while a `--ask` report is taken.
- `TestCatalogProtocolValidated` (`internal/catalog`, `internal/config`):
  bounds, an unknown name refused by `agentproto.Check`, `{}` turning it
  off, `inherit` and a clone carrying it; `AgentInfo` shows it.
vitest: none. Playwright: none (row 5 carries the first chat spec).
**By hand, with the real agent:** record the fixtures; a chat child of a
real Claude Code started for a session whose terminal reached its
prompt, one turn with a tool; whether the MCP server's `timeout` in a
`--mcp-config` file raises the idle window (design 7's
`PermissionWait`), written in the row's pull request. **14 h.**

### Row 5: the move between ways, and the connection split

**Scope.** Design 9 whole: `Local.SwapWay`, the terminal's start
extracted from `NewLocal` (called by both), the route and its body
bound, the move noted during a turn and made at its end, the condition
of a first turn that ended, its refusals with background work among
them, the boundary notices, the one retry and the end with both
failures' words; `way` (with `noted`), `moving` and `move_refused` in
the three places, and the welcome's `noted`; `useSessionConnection` and
`TerminalView` as its consumer; the switch's component with its states
(mounted on the page by row 7); Claude Code's `Moves` set once the
by-hand observation is recorded. **Must not touch** a crew member's
session, a hosted session, the journal's record, the launch route.

**Tests.** Go race:
- `TestMoveNeedsAFirstTurn` (I21, `internal/api`): a move to Chat on a
  terminal session that has reported no ended turn answers
  `move_refused` with "Chat opens after the agent's first reply."; after
  a `done` from a turn source it is made.
- `TestMoveWaitsForTheTurnsEnd`: pressed in a turn, the move is noted
  (the `way` message with `noted` sent to every viewer, and in a new
  viewer's welcome) and made at `TurnEnded`; with a request open, with a
  submission pending, with background work running: it stays noted with
  each one's sentence; pressed away, `noted` clears.
- `TestMoveProvesTheOldProcessGone` (I1): the new child starts only
  after the old child's exit and its group's end, held at a seam
  (`afterOldExit`) so starting earlier fails every run.
- `TestMoveReadsTheIdAtTheMove`: an id the stream changed after the
  first move is the one resumed on the move back.
- `TestMoveKeepsTheSession`: id, links, viewers, people's chat, record
  and listing unchanged; `Info.Way` changed; the `way` message sent; a
  `notice` at the boundary.
- `TestMovingRefusesActs`: a submission, an answer and an interrupt
  during the move answer `moving`.
- `TestMoveRefusedForACrewMemberAndAHost`.
- `TestMoveFailureTriesBackOnce`: the new start fails, the old way is
  resumed; both fail, the session ends with both failures' words.
- `TestMoveIsTheOperators`: a link's and an agent token's request
  answer `401`; a body past 64 bytes is refused.
- `TestEverySessionStartsInItsTerminal` (I21): a launch, a resume of an
  ended chat and a resume from history all start the terminal way.
- `TestWayMessageBounds` (`internal/proto`).
vitest: `useSessionConnection.test.ts`'s `oneConnectionForBothViews`;
`switchState.test.ts` (which sentence the switch shows: before the first
reply, during a turn, noted, refused).
Playwright: `chat-move.spec.ts`: a stub terminal session's first turn,
then a move through the route to a chat (the page's welcome reads
`protocol` and its transport receives TRANSCRIPT), then back (the bash
stub resumes the same file and shows the earlier turns); pressed during
a turn held at the gate, the `way` message's `noted` arrives and the
move is made at the turn's end.
**By hand, with the real agent (the acceptance):** a Claude Code
session's first two turns in its terminal, moved to Chat through the
route, a turn there, moved back, the terminal shows the chat's turn in
its context; recorded in the pull request, and in `docs/features.md`
with row 7. **14 h.**

### Row 6: the permission request

**Scope.** Design 7 whole: `POST /api/sessions/{id}/permission` (agent
token, a body bound of 256 KiB, held open, its end read as a withdrawal
with its two words) with its `compareDoors` entry and its Operation in
the guide's table; `conductor mcp`'s concurrent serve loop, its
cancellations, and the `permission` tool with its own client (no
timeout but its context's), its documented result, its deny past
256 KiB and its deny for a lost POST, listed only in a protocol session;
`Local.RequestPermission` (the announcement wait, the binding to an
announced call, the floor, the bound, the list beside the attention, the
entry, the digest, the withdrawals by result, turn end, interrupt, stop
and agent, each answering its held request at once);
`Local.AnswerPermission` (the operator check, the claim on any open
request, the check at the hand-over with "no result yet", the record,
"allowed" only once delivered); `permission_answer` in the three places;
`ErrPermissionCard` for `answer`, `AnswerAs`, `Submit` with a question,
`ext.Host.Submit` and the lane's text answer, keyed on source `protocol`
and kind `permission`; the router told and its answer refused; the
inbox's item for such a question with "Answer it in the chat." and no
action; `PermissionWait` and the deny before it; the floor's settings
assets (`claude-protocol.json`, `claude-protocol-yolo.json`: the `Read`
and `Edit` deny rules for every deny-list path, written at the server's
start, and the `PreToolUse` hook) and `conductor notify --protocol-floor`;
`--permission-prompt-tool` and the floor's `--settings` added to row 4's
argv. The screens are row 7's; this row is proved through the session's
API and sockets. **Must not touch** the terminal way's questions, DR1's
records.

**Tests.** Go race:
- `TestPermissionNamesAnAnnouncedCall` (I8, `internal/session`): a
  request for a call the stream did not show by `AnnounceWait` (a fake
  clock), one already resulted, another turn's, one with another name or
  digest, the permission tool itself, and a second for one call: each
  denied with its words; the matching one taken.
- `TestPermissionBeforeItsAnnouncement` (I8): the fake driver holds
  `ToolStarted` at a seam (`beforeToolStarted`) until the request has
  entered; the gate opened, the request is taken, not denied; breaks on
  a request denied at once.
- `TestPermissionFloor`: a file tool's path under the deny list and a
  command naming one are denied before any question; a command that
  computes the path is not claimed as floored.
- `TestPermissionFloorAssets`: both floor files hold a `Read` and an
  `Edit` deny rule for every `DenyList` entry and one `PreToolUse` hook;
  breaks if a path is missing.
- `TestProtocolFloorHookDenies` (`internal/cli`): `conductor notify
  --protocol-floor` given a command naming a denied path prints the
  documented deny; given another, nothing.
- `TestPermissionBound`: the ninth open or waiting request is denied
  with the bound's words.
- `TestPermissionHoldsTheAttention` (I18): two open requests, then a
  blocker `TryAskID`, an agent `TryAsk` and a `TryAskDecision`: both
  requests stay open and answerable, the oldest is shown, answering it
  shows the second, answering the second shows the most recent other
  question; breaks if each request is asked through `TryAskID`.
- `TestPermissionAnswerBinding` (I9): an answer with another call id, a
  digest of a changed input, or for a call that has a result, answers
  `stale_request` and the request stays open; the matching answer is
  handed over once, after its record; `state` becomes `allowed` only
  after delivery, and a POST gone before delivery records `withdrawn`.
- `TestPermissionFirstAnswerWins`: two operator answers at once: one
  `answer_ok`, one `question_answered`.
- `TestPermissionOnlyTheOperator` (I10, `internal/api`): a view link, a
  control link, a decision link (its lane's choice and its text answer),
  a relayed viewer through a switchyard, `AnswerAs` (a router's held
  answer, through the router's `apply`), an extension's `Host.Submit`,
  the agent's token on the attention route, and a decide principal's
  routes, each sending a choice and a line: none reaches a verdict.
- `TestPermissionNeverSilentlyAllowed` (I11): the session's stop, the
  child's exit, the server's stop, an interrupt, a turn's end with a
  request open, an agent's cancellation and `PermissionWait` (a fake
  clock) each answer deny or withdraw with their words, and each held
  request returns; none answers allow.
- `TestPermissionAnsweredElsewhere`: a `ToolEnded` for the pending call
  with no MCP cancellation withdraws the question with "Answered by the
  agent's own rules.", the held POST returns with the withdrawal's deny,
  the count of open requests goes to 0, and a later `permission_answer`
  answers `question_answered`; a second case with the held POST's client
  closing with no result in the stream records "The request was lost."
- `TestPermissionDigestCanonical`: the same input with keys reordered
  and spaces changed has one digest; one changed byte of a value
  another; an input with a lone surrogate escape or a key twice is
  denied.
- `TestPermissionRouteBodyBound` (`internal/api`): a body past 256 KiB
  is refused `413` and asks no question.
- `TestMCPServesWhilePermissionHeld` (`internal/cli`): while a fake
  server holds a `permission` POST, a `ping` and a `report` call on the
  same stdin are answered; breaks on today's serial loop.
- `TestMCPCancelledEndsTheHeldCall` (`internal/cli`): a
  `notifications/cancelled` for the held id makes the fake server see its
  request's context end, and no reply for that id is written.
- `TestMCPWithdrawnCallsFreeTheirSlots` (`internal/cli`): 13 permission
  calls, each withdrawn by the fake server with no
  `notifications/cancelled`, then a `report` call, which is answered,
  not refused for the bound; breaks if a withdrawal leaves its POST held.
- `TestPermissionHeldPastTheSessionClientsBound` (`internal/cli`): a
  fake server holds the POST past 60 s on a fake clock and the call is
  still waiting; breaks if the tool uses `crewClientFromEnv`.
- `TestPermissionToolResultIsTheDocumentedOne` (`internal/cli`): one text
  block of `{"behavior":"allow"}` or `{"behavior":"deny","message":...}`
  and no structured content; a 300 KiB input gets the too-large deny
  without a POST; a POST that ends without an answer gets "Conductor
  lost this request."
- `TestPermissionToolListedOnlyInProtocol`: outside a protocol session
  the tool is not listed.
- `TestPermissionRouteOpenToALane` (`internal/api`): a protocol lane
  member's permission request reaches the question, not `409 lane`.
- `TestPermissionAnswerFrameBounds` (`internal/proto`): `question` 1 to
  32, `call` at most 128, `sha256` 64 lowercase hex, `choice` of the
  closed set, `message` at most 500.
vitest: `utils/protocol` types typechecked through their first consumer
in row 7.
**By hand, with the real agent:** a Bash call in Manual mode answered
Allow once and Deny from the session's socket; the floor's deny rules in
Manual mode and in a yolo session against a file under the data
directory, and an `@` reference to one; a `PermissionRequest` hook of
the person's user settings deciding first, and whether Claude Code then
sends an MCP cancellation; a question held past an hour, still waiting.
**Boundary:** yes, PR-B alone, with an adversarial read written in the
pull request: every way an answer could reach another request (binding
and replay), every actor and transport that could reach a verdict (who
answers), whether the question shows what will run (what the request
says, the characters a person cannot see), whether any mode or standing
grant widens what runs unasked (modes), whether any end, restart, move
or bound allows or leaves a held request behind (lifetime), and whether
every verdict is recorded before it is handed over, with no secret in a
log (audit). **18 h.**

### Row 7: Chat, the switch mounted, the permission question

**Scope.** Design 11 and 12 whole: `ChatView` with the folded steps
line, `ToolStepRow`, `InlineDiff`, `PermissionQuestion` with its plain
words, its one-line input that opens, its hidden-character marks
(`utils/hiddenMarks.ts` holding the set, `runOrigin.ts` importing it)
and its rule for Allow once; the box to write in with Stop, its help
line by `verbatim`, and its two message lines; the header's switch
mounted; People (the rail's two items made one, `?tab=chat` and
`?tab=people` both opening it, the palette's row); the reply bar's
"Answer it in the chat."; the join page, a run's join grid, the yard and
the roundhouse drawing `ChatView` by `welcome.way`; the tile; the Share
dialog's sentence; the note on the vendor's terms, once;
`utils/transcript.ts` (the entries' parser and bounds, the merge of
revisions by `seq`, the highest `mark` kept per connection); the
features entry. **Must not touch** the server, the terminal's DOM marks.
**The existing tests that change**, for People and the switch, each
named in the pull request: `utils/inspector.test.ts` (the rail's items
and the tooltip), `e2e/inspector-rail.spec.ts`, `e2e/comments.spec.ts`,
`e2e/palette-reach.spec.ts`, `e2e/palette-rows.spec.ts`,
`e2e/session-page.spec.ts` (the rail and the header's measures) and
`e2e/chat.spec.ts` (the people's chat's rail item and label).

**Tests.** vitest:
- `utils/transcript.test.ts`: `mergesRevisionsBySeq`, `keepsResumePoint`
  (the highest `mark` held is the next hello's `transcriptAfter`),
  `refusesPastBound`, `promptAnsweringShowsNoText`.
- `ChatView.test.ts`: `drawsMarkdownWithoutHtml` (an agent's `<img
  onerror>` is text), `foldsStepsIntoOneLine` (a turn's steps are one
  line until opened, and no count of tokens, timing or id is on the
  surface), `stepOpensItsParts` (opening a row sends one `transcript_get
  part`).
- `PermissionQuestion.test.ts`: `plainWordsByTool` (a shell tool reads
  "Run a command?", an edit "Change a file?"), `allowWaitsForTheWholeInput`
  (a long input's Allow once is disabled until opened), `noButtonsForALink`,
  `drawsHiddenCharactersAsMarks` (a shell input holding U+202E, U+2066 to
  U+2069, U+200B, U+FEFF, CR and U+2028 is drawn with none of those code
  points in the question's text and with the warning line, while its
  line feeds are line breaks; breaks on a question that draws the input
  raw, or marks every line feed), `noFolderWithoutASource` (a Claude
  Code request shows no folder).
- `ChatComposer.test.ts`: `helpLineByVerbatim` (the "Sent as written"
  line only where `verbatim` is true).
- `inspector.test.ts`: `peopleIsOneItem` (the rail lists People once, and
  both tabs open it).
Playwright (with the stub agent):
- `chat-view.spec.ts`: a stub session's first turn in its terminal, the
  switch pressed, then in the chat a turn's words, its steps line opened
  to a step and its output, a file change's diff and its editor tab, the
  turn's end; Stop on a turn held at the stub's gate ends it as
  interrupted; a message during a turn shows "Sends when the turn
  ends." and is sent at its end; the box's help line is shown; before
  the first turn the switch says "Chat opens after the agent's first
  reply."
- `chat-permission.spec.ts` (with `asker-proto`): the question with the
  exact call; Allow once lets the stub's call run; Deny's message reaches
  the stub; a view link's join page shows the question and no buttons.
- `chat-link.spec.ts`: a view link and a control link open the session
  on the join page and see the chat, not an empty terminal; the control
  link sends a message and presses Stop.
- `chat-reconnect.spec.ts`: a dropped connection resumes with only the
  entries changed since the mark it held (counted at the server's seam,
  never by the page's list length), and a request withdrawn during the
  drop is drawn withdrawn.
- `chat-layout.spec.ts`: at 1440 x 900, 1280 x 720 and 390 x 844,
  nothing sideways, the header's name whole with the switch, the box's
  controls reachable; measured after the page's own animations end.
**By hand, with the real agent:** a person's Claude Code session turned
to Chat after its first reply, a permission question and its answer, on
the desktop app's server inside WSL and on Linux, with nothing
configured. **18 h.**

### Row 8: the declaration on the Agents page, the digest, and step 2's trust design

**Scope.** Step 2's trust design (how a crew's member comes to run as a
chat without a person pressing Chat, and whether a default of Chat is
offered), written and read adversarially before its code; then the
Agents page's form carrying `protocol` (`utils/agentForm.ts`,
`AgentInput`); `agentSelection` digesting the protocol only for a
protocol launch; and what the trust design asks of a launch. **Must not
touch** the crew engine.

**Tests.** Go race: `TestSelectionDigestUnchangedForTerminal`
(`internal/api`: every terminal launch's digest equals `main`'s, from a
table of the existing test cases), `TestSelectionDigestNamesProtocol`.
vitest: `agentForm.test.ts`'s `carriesProtocol`. The trust design's own
tests, named in it. **10 h**, the trust design's own size to be given
with it.

### Row 9: Yardmaster on the protocol way

**Scope.** Design 8 whole, with the member's way set as step 2's trust
design says: `LaunchSpec.Way`; `runtimeFor` keeping a run in its
registered Runtime and its members in their terminals when that Runtime
has no pipes; `AwaitReady`'s protocol branch with `protocolReadyCap`;
`promptWith` waiting on `Finished` and marking prompted at `Accepted`;
the deliverer woken at a turn's end; `OnActivity`'s `kind` argument
through `session.Options.OnActivity`, the api's publisher interface and
`Published.OnActivity`, the hosted hub's `OnActivity` and
`OnLateActivity` and the server's wiring, and `KindInterrupted` skipped
by `startAfter` and the reminder; the run log's new lines and their rows
in `docs/protocol.md`; `fakeLauncher`'s protocol members over
`sessiontest.FakeProtocol`. **Must not touch** the ledger, Brakeman's
class.

**Tests.** Go race (`internal/crew` unless named):
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
  interrupted turn is not started, and no reminder is sent; breaks if
  `OnActivity` carries no kind.
- `TestProtocolTurnFinishesNoTask` (I12): a turn's end with no
  `task_result` leaves the task running.
- `TestRunLogSaysTheWay` (I16): each attempt's line names its way and a
  fallback its reason.
- `TestPipelessRuntimeKeepsTheRunInATerminal` (`internal/api`): a run a
  registered Runtime without pipes takes stays in it, its members in
  their terminals, the log saying so; breaks if the run is moved to
  native.
- the existing tests of `internal/crew`, `internal/session`,
  `internal/api`, `internal/signal` and `internal/hostagent` change only
  at the `OnActivity` call sites' new argument.
Playwright: `crew-chat.spec.ts`: a managed run of two stub protocol
members, each attempt's way in the run's log, a task finished by its
result alone.
**By hand, with the real agent:** a live crew of the example crew with
Claude Code members as chats, one permission answered, the run to its
end; recorded in the features entry. **16 h.**

### Row 10: Codex's app server (after the maintainers' decision)

**Scope.** `agentproto/codex.go`: `codex app-server --listen stdio://`,
`initialize` with `clientInfo {name: "conductor"}` and no
`experimentalApi`, `thread/resume` of the terminal's thread,
`turn/start`, `turn/interrupt`, the approvals through design 7 answered
`accept` or `decline` with the approval's `cwd` shown,
`serverRequest/resolved` as a withdrawal, `item/*` and `turn/*` mapped
to entries; read and stated before Codex is declared: whether the app
server loads the same folder settings its TUI loaded for the session,
and what Codex does with a message that starts with `/` or holds
`@path` (`Verbatim` set from it); `codexMCPEnv` gaining
`CONDUCTOR_PROTOCOL`; its fixtures and schema of `MinVersion`; Codex's
catalog declaration; `Moves` only after its by-hand observation (thread
to `codex resume <UUID>` and back, the writer lock released). Never a
second client on a thread.
**Tests.** As row 4's, for Codex (`TestCodexFixturesReplay`,
`TestCodexStableSurfaceOnly`: no experimental method is ever sent,
`TestCodexApprovalThroughThePermissionPath`: an approval request is the
session's question and its reply carries the verdict for its id alone,
`TestCodexResolvedWithdraws`, `TestCodexVerbatimIsDeclared`: a sent
`/`-leading message is checked against the declaration, and the box's
"Sent as written" line is drawn only if it holds), and a second mode of
the stub with its Playwright run. **18 h.**

### Row 11: the Agent Client Protocol client

**Scope.** `agentproto/acp.go`: protocol 1 as design 4 says, `Verbatim`
false; for each agent, read and stated before it is declared: whether
its ACP start loads the same folder settings its terminal did, and what
it does with a message that starts with `/` or holds `@path`; the
declarations of the first native agents the maintainers have decided on,
each with its argv and a by-hand check; `Moves` only where the agent's
session id is shown to be its native one.
**Tests.** `TestACPBaselineOnly` (no `fs/*` or `terminal/*` capability
advertised; such a request answered `-32601`), `TestACPPermissionOptions`
(the stub offers optionIds unlike their kinds, OpenCode's `once`,
`always` and `reject`; an Allow answers the `allow_once` option's own
id; breaks if a kind is sent as an id or `always` is ever selected),
`TestACPCancelAnswersPending` (Stop sends `session/cancel` and answers
every pending request `cancelled`; breaks on a `reject_once` answer),
`TestACPCancelRequestWithdraws` (an agent's `$/cancel_request` withdraws
the question and answers its held reply), `TestACPVerbatimIsDeclared`
(the stub advertises `compact` in `available_commands_update`; a sent
"/compact" is checked against the declaration, and the box draws "Messages
starting with `/` may run as the agent's commands."), fixtures per agent,
and the stub's third mode with its Playwright run. **18 h.**

## Decision before any builder

**Answered by a maintainer on 2026-11-24, for Claude Code: (a), any
sign-in the program already has.** Rows 1 to 7 are built to it, and rows
8 and 9 after step 2's trust design. Codex (row 10) and each Agent
Client Protocol agent (row 11) wait for answers of their own. The
decision as it was put:

One decision must come before any builder starts, and only the
maintainers can make it, after reading the pages listed above (Sign-in
and the vendors' pages):

**Which agents' interfaces Conductor builds on, and for which
sign-ins.** For each agent: (a) with any sign-in the program already
has; (b) only where the program reports an API key; or (c) not at all.
This plan is written to (a) for Claude Code (rows 1 to 7, and 8 and 9
after step 2's trust design), and builds Codex (row 10) and each Agent
Client Protocol agent (row 11) only after its own answer. Answer (b) for
Claude Code adds to row 4: the shape check reads `apiKeySource` from
`system/init`, and a start that reports none goes back to the terminal
with one sentence ("Chat needs this agent to use an API key."), about
one more hour. Answer (c) for Claude Code ends rows 2 to 9 before they
begin.

## Defaults this plan is written to

Any of these a maintainer may overturn; one line each, the default and
the other answer.

| What | Default | The other answer |
|---|---|---|
| The names | "Chat" and "Terminal" for a session's two views; the people's chat is labelled People | "Conversation" for the agent's thread, and the people's chat keeps "Chat" |
| Who may answer a request | the operator alone; decide holders once a request is a durable record (a later row) | decide holders now, which adds a decide route for a session's permission question to row 6 |
| Sync | a chat's transcript counts as captured output and never leaves the machine | the agent's words sync and its tools' outputs do not |
| History | the journal keeps today's kinds, not the agent's words | a `session.said` kind with its bound, its test and its share of the record (a row of its own) |

## Out of scope

- **The terminal way's own delivery.** A terminal session's messages are
  still typed (paste, pause, Enter, screen checks), and its state is
  still read from its screen beside its hooks. Delivering them at a
  turn's end through the agent's own hooks and reading a turn only from
  its signals is the work that follows, issue #1092 ("session: the
  terminal way delivers through the agent's own hooks and reads a turn
  from its signals").
- **Expanding `@file` and `/command` in a person's own typed message.**
  A driver that can send as written does so for every message in the
  first cut.
- **Conductor's own folder trust, a start that is a chat from its first
  moment, a Settings choice of where agents run, and a way chosen at
  launch.** Every session starts in its terminal, where the agent asks
  its own trust question; step 2's trust design is where a start as a
  chat would be settled.
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
- **The phone's chat.** The page works at phone width; its design is
  later.
- **The Overview, receipts, the verify queue and rules over a finished
  piece of work.** Later rows, which the entries are designed to feed
  (design 5).
- **Brakeman's turns on the protocol way.** They stay in the terminal.
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

Made with this plan:

- `docs/plans/agent-harness.md`, 3.2 Non-goals and 14 (Agent integration):
  each sentence that rules out an SDK or app-server transport gains a
  pointer: the terminal way is never removed or replaced, and
  `docs/plans/chat-or-terminal.md` adds a second, optional way of
  running a session through an agent's own documented structured
  interface.
- `docs/repository-map.md` lists no plan by name, so it is not changed
  here; rows 1, 2, 4 and 7 add their packages' rows when they land.
- `docs/plans/runtime.md` gets its one sentence with row 1, not here.

## Acceptance

Each step is done when its observations have been made by hand and
recorded in `docs/features.md`'s entry, not when its rows are merged.

**Step 1.**
1. On a Linux server and on the desktop app's server inside WSL, with
   nothing configured by anyone: a Claude Code session opened in its
   terminal, where Claude Code asks its own trust question in a new
   folder; before its first reply the switch says Chat opens after it;
   after a turn, turned to Chat; a turn with a tool step and a file
   change; a permission question answered Allow once and another Deny by
   the operator; Stop ending a turn.
2. The same session through a view link: the chat whole, the question
   without buttons; through a control link: a message sent, no answer
   possible.
3. The session moved back to the terminal after a turn, the terminal
   showing the earlier turns and taking one, moved to Chat again, the
   chat taking one with the terminal's turn in its context; the
   session's id and link unchanged.
4. An installed version below the gate offering no Chat, with its one
   sentence.
5. A terminal session's frames, attention, submissions and records as
   before: the whole Go race suite, vitest and Playwright passing, with
   only the tests row 7 lists changed.

**Step 2.** After its trust design: a managed run of the example crew
with its Claude Code members running as chats, its log naming each
attempt's way, its tasks finished by their results.

**Step 3.** Per agent, after the maintainers' decision: its row's
by-hand checks recorded.
