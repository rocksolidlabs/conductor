# Runtime: where an attempt's process and its checks run (Program item C10)

The Runtime is the one place where the processes of Conductor's sessions
and of a managed run's trees start: native processes on the machine that
runs the session, as the user, unless a registered provider takes a
managed run (decision RT7). This plan
is Program item C10 (the Program, section 4.2), written against `main` at
`293bd7f6` (2026-10-30) after reading the launch paths, the engine's tree
executor, the checks runner, the host agent and the ledger. It is reviewed
before any of its code; the PRs at the end are cut into issues after the
review, one issue, one branch `issue-N`, one PR each (AGENTS.md). Line
numbers are `main`'s at that commit.

The names follow the railroad (the Program, section 1.2): Yardmaster is the
agent harness whose engine changes here, and an attempt's checkout is a
worktree, never a workspace (decision D5).

The other Program items this plan meets, in plain words: C1 makes the
public package `ext` (the eight extension interfaces, a registry, the free
defaults in `ext/local` with `local.Fill`, which puts a default in every
slot nothing is registered in) and the composition root that reads the
registry once at start, `docs/plans/ext.md` (PR #453); this plan builds
on its names and needs it merged. C2 to C9 wire the other seven
interfaces into the packages this item changes too, and this item's PRs
come after theirs (The PRs, decision RT15). C6 adds `conductor export`, a
zip of the data directory. C9 adds `GET /api/capabilities` and
`ext/exttest`, a fake and a contract suite per interface; its
`FakeRuntime` and `Runtime` check answer only a `Where`, and the rest of
the Runtime's fake and checks are this item's, as `docs/plans/ext.md`
says in its C9 section: PR-1 here gives that fake the methods it adds to
the interface, and PR-5 their behaviour and the execution checks. DS2 shows a ticket's runs, candidates and
checks, this item's `where` among them. EN1 is project manifests: its PR
#441 (issue #396), in review as this is written, gives the engine's tree
executor the `serve` and `probe` this plan puts behind the Runtime, and is
to merge first. F is the Boxcar integration, a Linux microVM running an
attempt; it comes after this item and is not part of it. P0 is the
private premium repository's first PR, the first code outside this
repository that imports `ext`.

## Articles it serves

1. No premium code, no licence check: the only question asked is whether a
   Runtime is registered, once, by the composition root; nothing in this
   repository branches on a plan or a subscription.
2. Fully useful alone: the native Runtime is what `main` does today; every
   session, crew, managed run, check, service and probe keeps running with
   nothing registered, which is every build of this repository.
3. Paid features gated by hosted dependencies: not touched. The Runtime is
   no paid feature and has no hosted dependency; Boxcar (item F) is free and
   runs on the user's own machine.
4. Stop, inspect, revoke, export, required approvals: stop still ends a
   session through its process's `Stop` (native: `pty.Process.Stop`, the
   same signals) and a run's checks and services through their contexts;
   inspect gains `where` on the task read and the two receipt routes;
   revoke, export (the ledger file, C6) and the approval paths (`Submit`,
   the control role, the held question) are untouched.
5. Dependencies one way: no new module; the Runtime's part of `ext`
   imports the standard library alone; `ext/local`'s native Runtime
   imports `internal/pty` and `internal/proc`, which import nothing of
   `ext`. C1's `private-module` rule holds unchanged; its `ext-std` rule
   is amended by PR-1 to let `ext/local`, and no other package under
   `ext/`, import exactly those two (`ext/local`, below).
6. No feature build tag: none added; `internal/pty` and `internal/proc`
   keep their `unix` and `!unix` splits.
7. The free default is `main`: the native Runtime is `pty.Start`,
   `proc.Run`, `proc.Serve`, the probe and the port draw, moved behind one
   interface unchanged. A registered Runtime only adds: it is offered
   managed runs alone, and takes one or declines it (`Takes`); a session a
   person starts or resumes, a legacy crew's members, a hosted session and
   every managed run it declines start natively, as on `main` (decision
   RT7).
8. A provider whose hosted dependency is unreachable: not touched; the
   native Runtime depends on nothing, and the composition root takes what is
   registered once at start, waiting on no service.
9. `CGO_ENABLED=0`: no cgo; the ledger stays `modernc.org/sqlite`; the
   migration is three `ALTER TABLE … ADD COLUMN`.
10. The repository contract: this plan before code; one issue, branch and
    PR per row of The PRs; race tests per package; no WebSocket protocol
    change (`internal/proto` and `web/app/utils/protocol.ts` untouched), the
    HTTP additions in `docs/protocol.md`; no new dependency, so no `make
    notices`.
11. Names: no new cloud name. The new field `where` names a runtime;
    `docs/protocol.md` tells it apart from a crew's `where` (`server` or
    `host`, where its members run) and an integration's (the file an
    adapter installs into).
12. Execution behind one seam: this item is that seam. After it every
    process of a session or of a managed run's trees starts through an
    `ext.Runtime` (a managed run's all through one), and every attempt and
    evidence row in the ledger says which.

## Why and what happens

Yardmaster is the execution authority, native execution on macOS, Linux and
WSL stays foundational, and the seam that would let a Linux microVM run an
attempt is built now as code rather than kept as a rule (the Program,
sections 1.3 and 2.5, decision D7). On `main` the places a process of a
session or a run starts are few and known:

- A server session: `createLocalSession` calls `pty.Start`
  (`internal/api/sessions.go:308`). It is every server session's path: a
  person's launch, a resume, a legacy crew member's and a managed attempt's
  (`crew.Launcher.Launch`, implemented at `internal/api/runs.go:55`).
- A hosted session: `hostagent.Run` calls `pty.Start`
  (`internal/hostagent/agent.go:237`).
- A managed run's trees: the engine's tree executor `treeExec`
  (`internal/crew/steps.go:179`) runs a task's checks (`verify.go`), a
  project manifest's steps and prerequisites (`runStep`, through
  `runCommand`, `steps.go:1319`) with `proc.Run`, its services with
  `proc.Serve`, its readiness probes, and draws its ports. Its comment
  already says the Runtime seam (C10) wraps it.

After this item:

- `ext.Runtime` is the one interface behind all three: a terminal process
  (`StartTerminal`), a bounded command (`Run`), a service (`Serve`), one try of a
  probe (`Probe`), a port (`Port`), whether it runs a managed run
  (`Takes`), and its names (C1's `Name`, and `Where`). `ext/local`'s
  native Runtime is `main`'s code and takes every run. A Runtime may also
  implement the optional `ext.PipeStarter`, a long-lived child whose input
  and output are pipes (a session's protocol way,
  `docs/plans/chat-or-terminal.md`): the native Runtime does, the server's
  guard offers it only when the registered Runtime itself has it, and a
  Runtime that lacks it is never handed such a child.
- The composition root (`runServe`) hands the server C1's filled set
  (`local.Fill`), whose Runtime is the registered one or the native one.
  The server and its engine start every process through a Runtime: a
  managed run's (its attempts, their sessions, trees and checks, and its
  prerequisites) through the set's Runtime when it takes the run, and
  everything else, and every run it declines, through the native one.
  `conductor host` starts its command through the native Runtime, since a
  host runs no managed run.
- The ledger's attempts, checks (a task's checks and a manifest's steps
  share the table) and prerequisites rows say where they ran (`where_ran`,
  schema version 6); the task read, the steps route and the prerequisites
  route report it as `where`. Every row a build of this repository writes,
  and every row an older ledger held, says `native`.

What a person sees: nothing different. Sessions open, take input, resize
and stop as they do; crews and managed runs run as they do; Pause, Cancel,
Retry, Accept anyway and Stop act as they do. Reading a task's attempts
(`GET /api/runs/{run}/tasks/{task}`) shows `"where": "native"` on each
attempt and each receipt. The run page shows nothing new: DS2's evidence
view is where a person will read it, and the run page has a reason to show
it only once item F gives it a second value. A developer finds the Runtime
in `ext`, and a test that fails any change starting or signalling a process
outside the runners that already do.

The harness plan asked for a narrow internal seam and ruled out a provider
registry and any schema field for Boxcar (`docs/plans/agent-harness.md`,
sections 5 and 18). The Program, which a plan does not override, puts the
Runtime in `ext`'s registry now (decision D7). Nothing here is Boxcar's:
the registry's Runtime and the `where` column are provider-neutral, and
`native` is their one value; PR-1 adds a line to that plan's errata saying
so.

## The changes

### `ext`: the Runtime's contract

C1 declares the Runtime's slot (`docs/plans/ext.md`, "`ext`: the eight
interfaces and their records", Runtime, and its decision E4): `type
Runtime interface { Provider; Where() Where }`, `type Where string`,
`WhereNative Where = "native"` and `Where.Valid` (1 to 32 of a-z, 0-9 and
'-', beginning with a letter), with `RegisterRuntime`,
`RuntimeProvider()`, `Providers.Runtime` and `Freeze()` in its registry.
It leaves the methods and the native default to this item, before
anything implements it; PR-1 adds them to that declaration and keeps every
name C1 gives (decision RT13). The types are `ext`'s own, with no alias to
an internal type. They are in-process Go types (a writer, a callback, an
open directory), not wire DTOs: no hosted service implements a Runtime.

```go
// Runtime is where the processes of Conductor's sessions and of a managed
// run's trees run: native processes on this machine, as the user, unless
// a registered Runtime takes the managed run.
type Runtime interface {
	Provider // Name, as C1 declares it
	Where() Where
	Takes(ref ProcessRef) bool
	StartTerminal(ctx context.Context, s TerminalSpec) (Terminal, error)
	Run(ctx context.Context, s CommandSpec) CommandResult
	Serve(ctx context.Context, s CommandSpec) (ServiceProcess, error)
	Probe(ctx context.Context, s ProbeSpec) (status int, err error)
	Port(ctx context.Context, ref ProcessRef) (int, error)
}

// TakesWait is the most the server waits for Takes (C1's guard).
const TakesWait = 100 * time.Millisecond

// StopKillWait is the most a Terminal's Stop waits for its process after
// forcing it: Stop returns within grace plus StopKillWait.
const StopKillWait = 5 * time.Second
```

The terminal launch is `StartTerminal`, not `Start`: C1's `ext.Starter`
(`docs/plans/ext.md`, "`ext`: Host and Start") is `Start(ctx, h Host)
error`, and one Go type cannot have two methods named `Start`, so a
Runtime that needs the server (item F's, to reach `Host` or its data
directory) implements `Starter` beside `Runtime` and gets C1's lifecycle
unchanged: `StartExtensions` calls its `Start(ctx, host)` once it is
registered, and until that has succeeded its slot answers the free
default (C1's rule), so `Takes` is not asked and a run made meanwhile runs
natively, as a declined one does (decision RT7).

- `ProcessRef{Session, Run, Task string; Attempt int; Tree string}`: what a
  process is for. A session's terminal names its session, and a managed
  attempt's also its run, task, number and tree `worktree`; a legacy crew
  member's its run alone beside the session; a check names its attempt and
  tree `checkout`; a manifest step its attempt and its tree's kind; a
  prerequisite its run alone; a probe and a port their attempt and tree.
  The native Runtime ignores it; a provider may group an attempt's
  processes by it (item F).
- `TerminalSpec{Argv []string; Dir string; DirFile *os.File; Env []string;
  Cols, Rows uint16; Ref ProcessRef}`: `pty.Spec`'s fields
  (`internal/pty/pty.go:21`). `Env` is the whole environment as Conductor
  built it (`pty.BuildEnv`); `DirFile` is the folder `openCwd` opened and
  checked, which the native Runtime starts the process in (on Linux by its
  descriptor, elsewhere by its path checked to still name it, as `main`).
- `Terminal`: `io.Reader`, `io.Writer`, `Resize(cols, rows uint16) error`,
  `Done() <-chan struct{}`, `Exit() ExitStatus`, `Stop(ctx, grace) error`,
  `Process() ProcessInfo`: `session.Process`'s members
  (`internal/session/local.go:27`) and the process as reported.
- `ExitStatus{Code int; Signal string; Err string}`; `ProcessInfo{PID,
  Group int; StartedAt time.Time}`, recorded and logged, never acted on.
- `CommandSpec{Argv []string; Dir string; Env []string; Timeout
  time.Duration; Head, Tail int; Log io.Writer; LogLimit int64; Started
  func(ProcessInfo); Ref ProcessRef}`: `proc.Spec`'s bounds with the
  environment whole, built by `proc.Prepare` (below), so the filter that
  keeps `CONDUCTOR_*` tokens and blocked loaders out stays in this
  repository whatever runs the command (decision RT8).
- `CommandResult`: `proc.Result`'s fields one for one
  (`internal/proc/run.go:108`).
- `ServiceProcess`: `Done() <-chan struct{}`, `Result() CommandResult`,
  `Stop(ctx) error`: `proc.Service`'s (#441).
- `ProbeSpec{URL string; Interval time.Duration; Ref ProcessRef}`: one try,
  no redirect followed, as #441's `probeURL` makes it.

Each method's doc states the contract, which is what the native one does:
`Takes` answers from the ref alone, with no I/O and no wait, whether this
Runtime runs the managed run the ref names (`{Run}`), and is asked once
for a run, when the run is made or continued (a provider that cannot run
now, its machine's KVM gone, declines, and the run runs natively);
`StartTerminal` starts the process in `DirFile`'s directory with exactly
`Env` on a terminal of `Cols` by `Rows`, under the cancellation rules
below; `Terminal.Stop` under the stop rules below; `Run` and `Serve` keep `proc`'s semantics (argv, never a shell; a
timeout `Run` requires, at most `proc.MaxTimeout`; the output's head and
tail; the log within `LogLimit`; `Started` once the process exists and
before its output is read; what the command left in its process group
ended with it); `Probe` tries once from where the tree's services listen;
`Port` gives a number free there; `Name` and `Where` are constant for the
Runtime's life. A Runtime receives a session's environment whole, its
agent token among it (the address-based path of constraint 4), and logs
and keeps none of it.

`StartTerminal`'s context bounds the start, never the process's life:

- A context already ended when the call is made is answered with its
  error and nothing is started.
- A context that ends while the start is under way is answered one of
  two ways, never both. Each Runtime has a commitment point of its own:
  a context that ends before it abandons the start, whatever the Runtime
  began (a process, a guest) ended and reaped before the call returns,
  and the context's error returned; a context that ends past it changes
  nothing, the start completes and its `Terminal` is returned. A
  `Terminal` returned is the caller's from then on whether or not the
  context has ended since, so a process is always owned by exactly one
  side: the Runtime until `StartTerminal` returns, the caller once it has
  returned a `Terminal`. Callers take a `Terminal` returned under an
  ended context as a started session, as `main` takes what `pty.Start`
  returns.
- `DirFile` is lent for the call: the Runtime neither closes it nor keeps
  it once `StartTerminal` returns, and the caller closes it then, as
  `createLocalSession` and `hostagent.Run` do on `main`.

The native Runtime meets them with `pty.Start` unchanged, which takes no
context and runs synchronously, its directory resolution and its fork
and exec (`creack.StartWithSize`) included (`internal/pty/pty.go:56` to
`:94`). Its commitment point is the call of `pty.Start`: it checks the
context, then runs a test seam of `ext/local` (a gate before the second
check, a start function that is `pty.Start` outside the tests, and a hook
after it returns, the gate and the hook nil outside them), checks the
context again, and calls the start function; an ended context at either
check returns its error with no `pty.Start` made. A context that ends
after the second check, while `pty.Start` runs or after it has returned,
is past the commitment point: the start completes and its `Terminal` is
returned with no error, the contract's second answer. That answer is the
native one because `pty.Start` cannot be interrupted (a fork and an
exec, bounded by the kernel, as on `main`), and ending the process it
made would only turn a start that succeeded into a refusal `main` never
gives; there is no window in which a process exists that neither side
owns. `pty.Start` is done with `DirFile` when it returns (its
`runtime.KeepAlive` holds it until the child has changed into it). What
callers pass keeps `main`'s behaviour (I4): `handleCreateSession` and the
resume pass `context.WithoutCancel` of the request's context, since `main`
starts a person's session whose client has gone; `Launch` passes its own,
which it already refuses when ended (`internal/api/runs.go:56`), the
refusal now also made at the Runtime's checks before its commitment
point; `hostagent.Run` passes its context, an
ended one taking the existing start-failed path (the connection closed
"start failed").

`Terminal.Stop(ctx, grace)` promises the process `StartTerminal` started,
its leader, and is what `pty.Process.Stop` does (`internal/pty/pty.go:155`),
unchanged:

- A process that has exited is not signalled: `Stop` closes the terminal
  and returns nil.
- Otherwise it asks the process's group to end (natively SIGTERM to the
  group), waits for the leader's exit until `grace` passes or `ctx` ends,
  then forces the group (SIGKILL to the group) and waits at most
  `StopKillWait` for the leader to be reaped: nil once it has been, an
  error if not. It closes the terminal in every case and returns within
  `grace` plus `StopKillWait`.
- It promises nothing of a descendant that outlives the leader: one that
  ignores the ask and the hangup when the leader exits on the ask, or one
  left behind by a leader that exited on its own before `Stop`, is left as
  `main` leaves it (Out of scope). The force reaches every member still in
  the group when it is sent.

A panic does not change who owns a process. Until `StartTerminal`, `Run`
or `Serve` has returned normally, whatever it began is the Runtime's, and
a panic that leaves the method, the Runtime's own or one raised by a
callback it calls on the caller's goroutine (`CommandSpec.Started`, the
caller's code), leaves nothing running: before the panic leaves, the
Runtime ends what it began and reaps it, then lets the panic go on
unchanged, swallowing no caller's panic. A `Log` writer is called on a
goroutine of the Runtime's, where no caller could recover a panic: it
must not panic, and one that does ends the program, as on `main`. The
native Runtime meets this with deferred cleanup:

- In `StartTerminal`, everything between `pty.Start`'s return and its own
  (the wrapping, and the seam's after-start hook in its tests) runs under
  a deferred function that, on a panic, stops the process with no grace
  (`Stop(ctx, 0)`: SIGKILL to the group, the leader reaped within
  `StopKillWait`), closes the terminal and re-raises.
- In `proc.Run` and `proc.Serve` (PR-1), `Started` is called under a
  deferred function that, on a panic, ends the group as the command's own
  end does (`g.signal`), reaps the leader, closes the output pipe and
  re-raises. On `main` a panicking `Started` leaves the command running
  (`internal/proc/run.go:246`); nothing else of `proc`'s changes.

The registry holds one Runtime (section 2.4), read once by the
composition root through C1's `Freeze()` and `local.Fill`. PR-1 adds two
panics to `RegisterRuntime`'s (C1's: nil, a name `ValidName` refuses, a
second Runtime, after `Freeze`): a `Where` that `Where.Valid` refuses, and
a `Where` of `WhereNative`, which only the free default reports, so a
`native` row always means the native code ran it. `local.Fill` stays the
one place that asks whether a Runtime is registered (C1's decision E7);
nothing in this item asks again.

### `ext/local`: the native Runtime

`local.Native()` (`ext/local/runtime.go`): `Name` `local`, as C1's
defaults are named; `Where` `ext.WhereNative`; `Takes` true for every
ref; `StartTerminal` is `pty.Start` behind the two context checks above,
its `*pty.Process` wrapped (`Exit`'s error as words, `Process` from `PID()`
with the group the same number, as `creack/pty`'s setsid makes it, and the
time `pty.Start` returned, `Stop` its `Stop`); `Run` is
`proc.Run` with `Spec.Environ`; `Serve` is `proc.Serve` likewise; `Probe`
is `proc.Probe`; `Port` is `proc.FreePort`. PR-1 names `pty.Process.Stop`'s literal
five-second wait after SIGKILL `pty.KillWait`, the same value, so that
`ext.StopKillWait` can be held to it by a test. PR-1 adds it to `local.Fill`,
which puts it in the Runtime slot when nothing is registered, as C1's
`Fill` does for the other slots. `internal/api`, `internal/crew` and
`internal/hostagent` take the native Runtime from it, so `ext/local` must
import nothing that imports one of them (Go would refuse the cycle); the
native Runtime needs `ext`, `internal/pty` and `internal/proc` alone, and
neither of those two imports `ext` or any of the three (`internal/proc`
imports `internal/pty`, which imports `creack/pty` and the standard
library). Go's internal rule lets `ext/local` import `internal/…`;
another module imports `ext/local`, never `internal/…`.

C1's lint (`docs/plans/ext.md`, "The lint: `internal/opencore`") has an
`ext-std` rule: a file under `ext/` that imports anything but the
standard library and `ext/` fails `make lint`. The native Runtime cannot
be written in the standard library (a terminal is `creack/pty`, through
`internal/pty`), and the Program puts every free default in `ext/local`
(section 2.4), so PR-1 amends the rule rather than move the default
outside it: a file of `ext/local` may also import `internal/pty` and
`internal/proc`, those two and no other package of this module or any
other module, while `ext` and `ext/exttest` keep C1's rule unchanged, so
the public interface package, the one the cloud repository takes its
records from, stays the standard library's alone. PR-1 adds the plants
that hold the amendment to `TestEachRuleCatchesItsPlant`: a file of
`ext/local` importing `internal/session` and a file of `ext` importing
`internal/pty`, each still reported, and a file of `ext/local` importing
the two runners, reported nothing. `docs/repository-map.md`'s package
table (C1's "May import" column) says so for `ext/local` (decision
RT13).

### `internal/proc`

- `Prepare(s Spec, service bool) ([]string, error)`: the checks `bounds`
  makes, in its order (no command, the timeout, a relative directory, a
  `CONDUCTOR_`, blocked or malformed name in `Env`, a name in `Inject` that
  is not one of Conductor's own), then the environment `environment()`
  builds (the session's allowlist and `Allow`, `Env`, `Inject`, `TERM=dumb`,
  `NO_COLOR=1`). Its error is the words `Result.Err` carries for the same
  spec on `main`.
- `Spec.Environ []string`: the whole environment `Prepare` built. With it
  set, `Allow`, `Env` and `Inject` must be empty (refused: "proc: Environ
  beside Allow, Env or Inject"), and each entry must be `NAME=value`, the
  name non-empty and free of `=` and NUL; the names were held to their
  rules when `Prepare` built it (its `CONDUCTOR_RUN` or `CONDUCTOR_PORT_WEB`
  is Conductor's own, which `checkName` would refuse in `Env`). `launch`
  sets `cmd.Env` to it.
- `Probe(ctx, url string, interval time.Duration) (int, error)` and
  `FreePort(ctx) (int, error)`: `crew`'s `probeURL`, `probeClient` and
  `freePort` (`steps.go:191`, and #441's), moved unchanged with their tests;
  `crew`'s `nativeExec` calls them until PR-2 replaces it.

### `internal/session`

- `session.RuntimeProcess(t ext.Terminal) Process`: the adapter a session
  drives; `Exit`'s words become a `pty.ExitStatus` again (`Local` reads only
  its `Code`, `local.go:669`). `Local` stays the only writer to a session's
  process (the guarded submission and a person's input, as today): the
  server and the host hand the `Terminal` to it and keep no other handle,
  so no second raw write path appears (the Program, section 1.5).
- `sessiontest.FakeRuntime`: the contract's fake for every package's tests.
  Its `Where` is its own (`fake` unless set) and its `Takes` true unless
  set; it records every call with its
  spec and ref; each method answers as the test sets it (a `FakeProc`-backed
  `Terminal`, a scripted result, maps for `Probe` and `Port`) or, with
  `Base` set, passes the call to it (`local.Native()`), so a run's real
  work can be done under a `where` that is not `native`. With
  `PanicAfterStart` set, its `StartTerminal` starts through `Base`, then
  ends and reaps what `Base` started, as the contract asks of every
  Runtime, and panics. It imports `ext`,
  `ext/local` and `internal/pty`, never `internal/session`, so the
  session's own tests may use it.

### `internal/crew`

- The engine's executor is the Runtime's: `execFor(rt ext.Runtime)
  treeExec` replaces `nativeExec`. Its `run` builds the environment with
  `proc.Prepare` and calls `rt.Run`; a spec `Prepare` refuses is answered as
  `proc.Run` answers it on `main` (`Exit` -1, the same words); the result
  comes back field for field. `serve`, `probe` and `port` go through
  `rt.Serve`, `rt.Probe` and `rt.Port`, a `ServiceProcess` wrapped as the
  engine's `service`. The executor's functions take the `ProcessRef` first:
  `runCommand`'s callers know the attempt and the tree (`verify.go:176` for
  a check, `runStep` for a step or a prerequisite, the service's start and
  its probe, `resolveTree`'s port draw).
- `Engine.SetRuntime(rt ext.Runtime) error`, beside `SetCheckConcurrency`
  and `SetEnvPassthrough` and set like them before the first launch: the
  Runtime the engine offers its managed runs. It refuses nil, a `Where`
  that `Where.Valid` refuses, and any call once the engine has launched a
  run, a continued one included. It writes it under `e.mu`; the first
  launch, which starts every goroutine that reads it, comes after.
  `NewEngine` starts with `local.Native()` offered, so every engine test
  runs its checks and steps through the adapter, as production does. The
  engine keeps `local.Native()` beside it, for what no offered Runtime
  takes.
- A managed run's Runtime is chosen when the run is made (`Engine.Launch`,
  `run.go:563`, and the launches beside it) or continued (`ContinueRun`,
  `continue.go:95`), before the run takes `e.mu`: the offered Runtime when
  its `Takes` answers true for the run's ref `{Run}`, the native one
  otherwise. It is fixed for the run's life in that engine, and every
  process of the run goes through it: its prerequisites, its attempts'
  sessions (`LaunchSpec.Where`, below), their trees' setup steps, services,
  probes and ports, and their checks. A legacy crew's run is offered
  nothing, and its members start natively.
- An attempt's `where` is its run's Runtime's `Where`, set where the
  attempt is made (`task.go:1148` in the pump; `continue.go:632` for a
  candidate verified again) and never changed. `attemptRow`
  (`task.go:1320`) writes it, and so does every receipt of the attempt
  (`verify.go:158` for a check, `steps.go:654` for a step); a
  prerequisite's row (`steps.go:828`) takes the run's.
- `LaunchSpec.Where ext.Where`: the attempt's, which the Launcher starts
  the session in; empty for a legacy member, started natively.
- `AttemptState.Where` (`json:"where,omitempty"`: a run record saved before
  this item has none) and `StepState.Where` (`json:"where"`), filled by
  `attemptStateOf`, `attemptStateOfRow` (`continue.go:908`), `StepStates`
  and `PrerequisiteStates`.

### `internal/crew/ledger`: schema version 6

`Version` becomes 6: EN1 took 5 for its receipts (#394), and its plan
already says the second of the two to land takes 6 (decision RT1). The
migration from 5 is SQL alone, in `migrate`'s one transaction with the
version's update:

```sql
ALTER TABLE attempts ADD COLUMN where_ran TEXT NOT NULL DEFAULT 'native'
ALTER TABLE checks ADD COLUMN where_ran TEXT NOT NULL DEFAULT 'native'
ALTER TABLE prerequisites ADD COLUMN where_ran TEXT NOT NULL DEFAULT 'native'
```

The `schema` a new database is made with has the same three columns,
default included, so a migrated database and a new one are alike. A
migration's statements are history and never change with the schema:
`prerequisitesTable` is today both `schema`'s definition and migration
4's (`internal/crew/ledger/ledger.go:71`, `:104`, `:668`), so adding the
column to it would give a version 4 database `where_ran` on its way to 5
and then fail its step to 6 on a duplicate column, undoing the upgrade.
PR-4 freezes today's text as `prerequisitesTableV5`, which migration 4
keeps, and gives `schema` a definition of its own with the column
(decision RT4). The column is `where_ran` because `WHERE` is a keyword,
as `pgroup`, `commit_id` and `from_member` are the ledger's names for
theirs; the Go fields are `AttemptRow.Where`, `CheckRow.Where` and
`PrerequisiteRow.Where` (decision RT2). `PutAttempt`, `PutCheck` and
`PutPrerequisite` write it and refuse a row with none (`ErrNoWhere`) or
one past 32 bytes (`ErrTooLarge`) (decision RT5). `WhereNative =
"native"` is the ledger's constant. `Recover`, `Prune`, `Events` and the
trees table are unchanged.

Reads do not assume the migration ran. A version 5 database stays one
when its migration fails (opened read-only, with why) and while a new
server waits for the owner lock the server it replaces still holds
(`attach` opens it read-only and unmigrated, `ledger.go:253`, and
`awaitLock` migrates it once the lock is let go). The ledger keeps the
version of the database it opened (`schema_version`, read at `attach`
under `mu`, read again after a migration), and `Attempts`, `checkCols`
and `prerequisiteCols` (`transition.go:565`, made functions of the
version) read `where_ran` at version 6 and the literal `'native'` in its
place below it. A read of such a database answers as before, every row
`native`, and writes nothing.

### `internal/api`

- `Server` keeps two Runtimes, both set in `New` and never changed after:
  the native one (`local.Native()`) and the one its engine offers managed
  runs, from `p.Runtime` of C1's constructor option
  `api.WithExtensions(p)` (`docs/plans/ext.md`, "The composition root, and
  the guard"): the registered one, or the native one `local.Fill` put in
  the empty slot; a server made without the option, as most tests make
  it, has `local.Fill(ext.Providers{})`'s, the native one. `New` wraps it
  in C1's guard where it wraps every provider: `Takes` goes through
  `extCall` within `ext.TakesWait`, its fallback false, so a late, failing
  or panicking answer runs the run natively; a process method has no wait
  and no fallback (a process that started is not answered for by a
  default). A panic in a process method is recovered only once the method
  has unwound, by when the contract has had the Runtime end and reap what
  it began (above); the guard logs it at error, naming the Runtime, and
  answers it as a refused start, or as a command's result with `Exit` -1
  and the words. `New` then calls `Engine.SetRuntime` with the wrapped
  Runtime, once both the guard's wrapping and `crew.NewEngine`
  (`server.go:298`) have run, beside `SetCheckConcurrency` and
  `SetEnvPassthrough` and before `UseLedger` and the ledger's recovery: it
  is inside `New`, so before the listeners serve and before any run is
  launched or continued (a continuation is a route's, `runs.go:604` and
  `:673`); a refusal from it is `New`'s error. C1's other providers keep
  their injection in `New` as C1 has it.
- `createLocalSession(ctx, req, crewRef, where)` starts the process
  through a Runtime's `StartTerminal` instead of `pty.Start`: the offered one when
  `where` is its `Where` (an attempt of a run it took), the native one when
  `where` is empty or `native` (a person's session, a resume, a legacy
  crew member, an attempt of a run it declined); a `where` that is neither
  is refused. The `ProcessRef` is the one its `crewRef` gives; a refused
  start is `502 start_failed` as today. The "session started" log line
  keeps its `pid`, from `Terminal.Process()` (`sessions.go:375`). Its
  three callers pass their context and their `where`: the request's, made
  `context.WithoutCancel` (`handleCreateSession`, the resume at
  `resume.go:173`, both empty; above) and `Launch`'s own (a crew launch's, bounded by `runTimeout`, or an
  attempt's, which ends with the attempt; `LaunchSpec.Where`).
- No route is added or changed: the task read, the steps route and the
  prerequisites route report `where` through `crew`'s types.

### `internal/hostagent`

`Options.Runtime ext.Runtime`: nil is `local.Native()`, as a nil `Log` is
`slog.Default()`. `Run` starts its command through it with the ref
`{Session: <registered id>}`; the exit code `Run` returns comes from the
`Terminal`'s `Exit` (`:316`). The `agent.proc` field (`agent.go:327`,
written at `:279` and read nowhere) is dropped rather than made a
`Terminal`, so nothing on the host holds the process but its `Local`.

### `internal/cli`

- `runServe`: nothing beyond C1's `api.New(…,
  api.WithExtensions(local.Fill(extensions())))` (`docs/plans/ext.md`,
  "The composition root, and the guard"), whose set now carries the
  Runtime; a plain build's line at start stays as C1 has it.
- `runHost`: C1's `extensions()` closes the registry there; the host's
  `Options.Runtime` is left nil, the native one, whatever is registered,
  since a host runs no managed run and a hosted session is a person's
  (decision RT7).

### `web`

`web/app/composables/useSessions.ts`: `RunAttempt.where?: string` and
`RunStep.where: string`, so the client's types stay the server's; no
component reads them yet.

### The boundary test

`ext/local/boundary_test.go`, `TestOnlyTheRunnersStartOrSignalAProcess`,
makes constraints 1, 2 and 5 of the Program's section 2.5 a failing test
(decision RT12). It walks the module from its root (`../..`), skipping
`testdata`, every directory whose name starts with `.` or `_` (the
worktrees under `.claude/` among them) and every directory with a `go.mod`
of its own, parses each non-test Go file whatever its build tags
(`go/parser`, the imports matched by path whatever their local names) and
fails, naming the file, the line, the call and article 12, on two kinds of
site:

- A raw start or signal: a call of `os/exec`'s `Command` or
  `CommandContext`, `os.StartProcess`, `os.FindProcess`, `syscall`'s
  `ForkExec`, `Exec` or `Kill`, `golang.org/x/sys/unix`'s `Kill`, or
  `Kill` or `Signal` called on a selector ending in `.Process`
  (`cmd.Process.Kill()`), outside the packages that do so today:
  `internal/pty` (sessions), `internal/proc` (checks, steps, services),
  `internal/gitcli` (git, and `gitclitest`), `internal/nvim` (the editor),
  `internal/agents` (the identity probe), `internal/reach` (the gateway
  lookup on darwin) and `internal/cli/up.go` (opening the browser).
  `exec.LookPath` is allowed anywhere.
- A runner the Runtime wraps, used past it: `internal/pty`'s `Start` and
  `internal/proc`'s `Run` and `Serve`, called or taken as a value (`run:
  proc.Run` is a use), outside `ext/local` (the native Runtime) and the
  two runners' own packages. On `main` three files use them
  (`internal/api/sessions.go:308`, `internal/hostagent/agent.go:237` and
  `internal/crew/steps.go:185`; #441 adds `proc.Serve`'s in
  `internal/crew`). PR-1's allowlist names the files that use them when it
  is cut; PR-2 removes `internal/crew`'s as it removes `nativeExec`, and
  PR-3 the other two as sessions start through the Runtime, so from PR-3 on
  the allowlist is `ext/local` and the runners alone, and a session or a
  command that bypasses the Runtime fails the test.

`main` passes it as it stands, under PR-1's allowlist; a new site is a
reviewed edit of the allowlist. Planted cases run the walker over files in
a temporary tree and expect each report: an `exec.Command` under
`internal/api`, a `pty.Start` under `internal/api`, a `proc.Run` under
`internal/crew`, a `proc.Serve` taken as a value under
`internal/hostagent`; and a file of `ext/local` using all three runners,
reported nothing.

### Docs

`docs/protocol.md`: `where` in the task read's attempts and in the steps
and prerequisites routes' receipts (rows at lines 1781 to 1784), its rule
and bound, and the line that tells it from a crew's `where`; the Managed
runs paragraph on what a run reports (line 2541), whose latest attempt
carries it.
`docs/architecture.md`: the server-hosted sessions diagram's start line
("start through the Runtime: native, creack/pty"), a paragraph in Crew runs
on where an attempt and its checks run and what the ledger records, and the
Packages rows. `docs/repository-map.md`: the `ext` row's Runtime and
`ext/local`'s native one, `internal/proc` (`Prepare`, `Environ`, `Probe`,
`FreePort`), `internal/crew` (the executor is the Runtime's),
`internal/pty` and `internal/session` (one implementation of a
`session.Process`; the adapter), `internal/hostagent`, and
`internal/crew/ledger` (schema version 6, `where_ran` on attempts, checks
and prerequisites); the `app` row is C1's and unchanged.
`docs/plans/agent-harness.md`: one errata line under its 2026-10-22
errata, that the execution seam is `ext.Runtime` in `ext`'s registry by
decision D7 (this plan), its section 18 line on Boxcar standing until item
F. `docs/features.md`: the item's entry, beside C1's.

## Events, orderings and invariants

States and transitions: none change. An attempt still runs reserved →
launched → running → submitting → verifying → succeeded or failed (or ends
cancelled, stopped, interrupted, uncertain); a receipt started → running →
finished or interrupted; a tree resolved → ready → ended; a run's states as
they are. No event kind is added and no payload changes: the rows carry
`where`, the events do not.

What is written before what:

1. The Runtime a server's engine offers is set before the first launch:
   `api.New` calls `Engine.SetRuntime` from its `WithExtensions` set
   before it returns, so before a listener serves and before a run is
   launched or continued, and `Engine.SetRuntime` refuses
   once a run has launched, so every managed run of a server is offered
   the same Runtime.
2. A managed run's Runtime is chosen once, when the run is made or
   continued, before any attempt or prerequisite of it exists; every other
   session starts in the native one.
3. An attempt's `where` is in memory from the moment the attempt is made
   and is first written in its reservation's transaction (`attempt.reserved`,
   `task.go`), before its worktree, its session or any process of it exists,
   as every row of the attempt is today.
4. A receipt carries its `where` from its first row (`check.started`,
   `step.started`), committed before its process starts, and keeps it
   through its running and finished rows.
5. Nothing rewrites a `where`: `Recover` concludes states and leaves it; a
   continuation's new attempts (a candidate verified again among them) take
   the `where` of the Runtime the continued run was given; an imported
   result keeps its source run's rows.

What the ledger holds after the change: `where_ran`, NOT NULL, at most 32
bytes, on every attempts, checks and prerequisites row. The trees table is
unchanged: a tree's lifecycle row is its attempt's, whose `where` covers it
(decision RT3).

The migration, version 5 to 6: three `ADD COLUMN` statements with
`DEFAULT 'native'` in `migrate`'s one transaction with the version's
update, or nothing; one that fails leaves the database at version 5,
opened read-only with why, as every migration does today, and read
through the version-5 columns (`internal/crew/ledger`, above), so its runs
can still be inspected. Migration 4 creates the prerequisites table from
its frozen version-5 text, so a database of any earlier version reaches 6
through every step. It is conservative in the owner's sense of the version
3 to 4 migration (claim nothing the rows cannot support; decision RT4):
before this item nothing could run a process of a session or a run but the
native code, so `native` is what happened to every row a version 5
database holds, not a guess. It says `native` of an attempt that never
started a process (reserved, then uncertain or failed before its launch),
which the column means as the runtime the engine dispatched it to. A
version 6 database opened by an older Conductor opens read-only
(`ledger.go`, a later version), and that Conductor's readers name their
columns, so it reads the rows and never writes one without `where`.

Invariants, each held by a test (Tests per surface):

- I1. Every attempts, checks and prerequisites row has a `where`: the
  ledger refuses one without; the crew tests' fake ledger refuses it too, so
  every engine test checks it.
- I2. An attempt's receipts have the attempt's `where`, and a run's
  attempts and prerequisites the run's.
- I3. A managed run's processes (its prerequisites, its attempts'
  sessions, trees and checks) run in one Runtime, chosen once when the run
  is made or continued; every other process of a server or a host runs in
  the native one.
- I4. Every refusal, environment, exit code and log line a native start or
  run gives is `main`'s, byte for byte.

Conflict rules: none. The ledger is one writer's (the owner lock); a
replication of it (C6's continuity store, CS1) carries the column as part
of the rows.

## Out of scope

- Boxcar, any VM, KVM or container code, a second Runtime or a placeholder
  of one, and a provenance class on the evidence rows: item F (the
  Program, sections 2.5, 4.6 and 7).
- Amending "checks run natively as the user" and the harness plan's
  section 18 line on Boxcar: item F does both (the Program, section 4.6).
- What a guest would need beyond this seam, each of which works on the
  server's machine today and keeps doing so, for item F to propose: a
  `where` reported per process rather than per Runtime; hooks marking the
  start and end of an attempt's place, and a Runtime's own shutdown; the
  notify URL a guest reaches the server at, and the path of the binary the
  hooks run (`CONDUCTOR_BIN`); the skill installed into the agent's own
  configuration and the agent's plans directory outside its folder (#289);
  the Files tab and the editor (`internal/nvim`) over a guest's tree; the
  preview URL a person opens, an address on the server's machine; the
  engine's comparison of a checkout after each check (`checkoutIntact`, git
  on the server's machine), which sees a guest's changes only through a
  mounted tree; and the catalog's availability lookup and identity probe
  asking the Runtime instead of the server's machine.
- Managed runs on a host: `Crew.Launchable` refuses a crew whose `where` is
  `host` (`internal/crew/crew.go:599`), unchanged.
- Ending a session's descendants that outlive its leader on `Stop`: on
  `main` `pty.Process.Stop` returns once the leader has exited, and a
  descendant that ignores SIGTERM and SIGHUP, or one left by a leader that
  exited on its own, survives it. Changing that changes `main`'s behaviour,
  which this item's boundary forbids, so the contract states it rather
  than mends it; it is a defect outside this stream (this item neither
  causes nor needs it), filed as its own issue when this plan is cut, for
  the owner to place (AGENTS.md).
- Offering a registered Runtime a session that is not a managed run's: a
  person's, a resume, a legacy crew member's, a hosted one (item F's first
  surface is `conductor host`). Item F proposes it, with how a person
  chooses it (decision RT7).
- `GET /api/capabilities` naming a registered Runtime: item C9, whose route
  lists `ext.Registered()`, the Runtime's kind among them. `ext.Host`:
  item C1.
- Showing `where` in the web app: DS2's evidence view, then item F.
- Open decisions O9 (where Boxcar's code lives; item F answers it), O12
  (the names `ext` and `app`; item C1) and O16 (paid features' UI; not
  touched): see Owner decisions.

## Tests per surface

Every PR runs, before each push, the checks of `docs/checks.md` for its
surfaces: Go, `make lint`, `go test -race -count=1 ./...` (the whole
module) and `make lint-static`; PR-4 also the web's `npm --prefix web run
typecheck`, `npm --prefix web test` and `make test-e2e`. PR-2 and PR-3 run
`make test-e2e` too, though they change no web file: every session and
check of the e2e stub starts through the Runtime from then on, so the
browser suite is evidence of no behaviour change. The Mac's row
(`python3 scripts/preflight.py --machines mac --only go-test-race`, under
the preflight lock) runs on every PR, since Darwin's PTY start (by path)
and exit watch (kqueue) are not Linux's and CI's race tests run on Linux
alone. No Python script changes, so the scripts suite is not affected. A
package's tests cannot see a plain build's registry (a test binary runs
only its own imports' `init`s), so the plain-build test is C1's
`TestPlainBuildRegistersNothing` in `cmd/conductor`, whose binary links
everything `main` links; every PR runs it in `go test ./...`, and PR-1
extends it to the Runtime slot (below). Every engine, session and host
test a PR touches runs on the native default. The tests wait on the
mechanism, never the clock (AGENTS.md, "Tests that do not flake"): a
process's `Done`, a row the ledger holds, a call the fake recorded.

Go, by package, the tests the PRs add:

- `ext` (PR-1): `TestRegisterRuntimeRefusesItsWhere` (a `Where`
  `Where.Valid` refuses and `WhereNative` each panic, naming the slot, as
  C1's other refusals do), `TestARuntimeMayBeAStarter` (one type with
  `StartTerminal` and C1's `Start(ctx, Host)` is both an `ext.Runtime` and
  an `ext.Starter`, and registers as a Runtime).
- `ext/local` (PR-1): `TestNativeStartsATerminalInItsFolder` (the folder
  renamed after it was opened, the process's working directory still it on
  Linux, as `internal/pty`'s start tests have it; output, input, resize, exit
  0, a `Process` with a pid; `DirFile` closed as soon as it returns),
  `TestNativeStartRefusesAnEndedContext` (a context ended before the call:
  its error, and the seam's count of `pty.Start` calls still 0),
  `TestNativeStartCancelledWhileGated` (the seam's gate holds the start,
  the context is cancelled, the gate let go: the context's error and no
  `pty.Start` made), `TestNativeStartCancelledDuringTheForkIsTheCallers`
  (the seam's start function cancels the context and then calls
  `pty.Start`, so the context ends while the start runs: the `Terminal`
  is returned with no error, its process runs and its `Stop` ends it),
  `TestNativeStartCompletedIsTheCallers` (the context
  cancelled from the seam's hook after `pty.Start`: the `Terminal` is
  returned, its process runs and its `Stop` ends it),
  `TestNativeStartThatPanicsAfterTheForkLeavesNothing` (the seam's hook
  after `pty.Start` panics: the same panic leaves `StartTerminal`, and
  when it is recovered the process, whose pid its pid file holds, and its
  group have been killed and the leader reaped),
  `TestNativeStopAsksTheGroup` (a leader and a child in its group, both
  ending on SIGTERM: nil within the grace, each pid gone, read from the pid
  files they write), `TestNativeStopForcesALeaderThatIgnoresTheAsk` (the
  leader ignores SIGTERM: SIGKILL after the grace, nil, returned within
  `grace` plus `StopKillWait`, the test's timeout above that sum),
  `TestNativeStopAfterExitSignalsNothing`,
  `TestNativeStopPromisesTheLeaderOnly` (a descendant ignoring SIGTERM and
  SIGHUP and a leader that exits on the ask: `Stop` returns nil and the
  descendant, read from its pid file, still runs, as on `main`; the test
  ends it itself), `TestStopKillWaitIsPtys` (`ext.StopKillWait` equals
  `pty.KillWait`),
  `TestNativeRunIsProcRun` (exit, signal, head and tail, a log within its
  limit, `Started` before the output, a timeout told from a cancel),
  `TestNativeServeStartsAndStops`, `TestNativeProbeAndPortAreProcs` (the
  native's `Probe` and `Port` answer as `proc`'s), `TestNativeTakesEveryRun`,
  `TestFillGivesTheNativeRuntime` (an empty set filled: `Name` `local`,
  `Where` `ext.WhereNative`; a set holding a fake keeps it),
  `TestOnlyTheRunnersStartOrSignalAProcess` with its planted cases.
- `cmd/conductor` (PR-1): C1's `TestPlainBuildRegistersNothing` gains the
  Runtime slot: `ext.RuntimeProvider()` answers false and
  `local.Fill(ext.Freeze()).Runtime` is the native one.
- `internal/opencore` (PR-1): `TestEachRuleCatchesItsPlant` with the
  `ext-std` amendment's three plants (`ext/local`, above).
- `internal/proc` (PR-1): `TestPrepareRefusesAsRunDoes` (for each refusal,
  `Prepare`'s words are the `Result.Err` `Run` gives the same spec, in
  `bounds`' order), `TestRunWithEnvironGivesExactlyIt` (the command prints
  its environment, Conductor's own `CONDUCTOR_RUN` and `CONDUCTOR_PORT_WEB`
  among it, and nothing else), `TestEnvironBesideAllowIsRefused`,
  `TestAStartedThatPanicsEndsTheCommand` (`Run` and `Serve` of a command
  that writes its pid and a child's into files and waits, given a
  `Started` that panics: the same panic leaves, and when it is recovered
  both are gone and the leader reaped), and the probe's and
  the port's tests moved from `internal/crew` with the code (#441's "the
  native executor's probe asks the loopback through a client of its own"
  and its redirect case).
- `internal/session` (PR-2): `TestFakeRuntimeRecordsWhatItIsAsked`
  (`sessiontest`); (PR-3) `TestARuntimeTerminalDrivesALocal` (a fake
  terminal's output, input, resize, stop and exit code through
  `RuntimeProcess`).
- `internal/crew` (PR-2): `TestEveryProcessOfARunGoesThroughTheRuntime` (a
  crew whose manifest has a prerequisite, a setup step, a service with a
  ready probe and a port, and a task with a check, under an offered fake
  that takes it: the fake records `Takes` once with `{Run}`, and each call
  with its ref, the prerequisite's `{Run}`, the worktree's setup
  `{Run, Task, Attempt, Tree: worktree}`, the checkout's setup and the check
  `{…, Tree: checkout}`), `TestADeclinedRunRunsNatively` (the offered fake
  declines: it records `Takes` and nothing else, and the run ends as it
  does natively), `TestALegacyRunIsOfferedNothing`,
  `TestAResultCrossesTheRuntimeWhole` (every
  exported field of `proc.Result` has its `CommandResult` field, by
  reflection, and a scripted result lands in the check's row unchanged),
  `TestARefusedSpecKeepsItsWords` (the executor's `run`, given a spec
  `Prepare` refuses, an `Inject` name that is not Conductor's, answers
  `main`'s result, `Exit` -1 and the same words, and never calls the
  Runtime), `TestSetRuntimeRefusals`
  (nil, a `where` `Where.Valid` refuses, after a launch),
  `TestAnEngineRunsNativelyByDefault`; the existing tests' executor hooks
  take the ref, unchanged otherwise.
  (PR-4) `TestAnAttemptAndItsChecksReportTheSameWhere` (with the fake
  Runtime taking the run, every attempts, checks and prerequisites row and
  every `AttemptState` and `StepState` say `fake`; declined, or with the
  native, `native`),
  `TestAContinuationsAttemptsTakeTheirRunsNewWhere` (a run continued by an
  engine offered another Runtime: the old rows keep theirs, the new attempts
  and the candidate verified again take the new), `TestARecordWithoutWhereReadsWithout`
  (a run record saved before PR-4: no `where` on its attempts; the task read
  from the ledger says `native`); the fake ledger refuses a row with no
  `where` (I1 in every engine test).
- `internal/crew/ledger` (PR-4): `TestAVersion5DatabaseIsBroughtToVersion6`
  (a database with an attempt, a check, a step and a prerequisite made
  version 5 by `version6Added`, its three `DROP COLUMN`s: reopened, version
  6, each row's `Where` native, its events as they were);
  `TestAVersion4DatabaseIsBroughtToVersion5` and
  `TestAVersion1DatabaseIsBroughtToTheVersionThisConductorWrites` reach
  `Version` with `version6Added` added to their fixtures (the former's
  `Version != 5` reads `Version`), and so does every other downgrade
  fixture, `asVersion3` (`backfill_test.go:58` and its twin at `:315`) and
  `ledger_test.go:827`'s among them, each dropping the version-6 columns
  before it replays a migration; `TestAVersion4DatabaseGainsWhereOnce` (a
  version 4 database reaches 6 through migration 4's frozen prerequisites
  table: one `where_ran` column, no duplicate, every row `native`);
  `TestAVersion5LedgerThatCannotMigrateIsRead` (the step to 6 made to fail
  through a seam of the tests, as `takeLock` is one: read-only at version
  5, why given, `Attempts` and the checks and prerequisites reads answer,
  each row `native`, nothing written);
  `TestAnUpgradeBesideThePreviousOwnerReads` (the owner lock held by
  another: the version 5 database opened read-only and unmigrated, its
  reads answer `native`; the lock let go: migrated to 6, the reads read the
  column); `TestARowWithNoWhereIsRefused` (each of
  the three puts: empty refused with `ErrNoWhere`, 33 bytes with
  `ErrTooLarge`, the transaction undone); `TestARowKeepsItsWhere`;
  `TestWhereNativeIsExtNative` (the ledger's constant is
  `ext.WhereNative`'s text). The rows the ledger's tests and the crew
  tests that seed a ledger build by hand (about 40 literals) gain `Where:
  WhereNative`.
- `internal/api` (PR-3): `TestASessionStartsThroughTheNativeRuntime` (`POST
  /api/sessions`, the server's native Runtime replaced by a fake through a
  test seam: the argv, the directory, the folder, the environment's names
  with no `CONDUCTOR_` from the server's own beside Conductor's, the size,
  `{Session}`), `TestAPersonsSessionIsNotOffered` (a fake offered: a
  person's session, a resume and a legacy crew member start natively, the
  fake asked nothing), `TestAnAttemptStartsInItsRunsRuntime` (an attempt
  of a run the fake took starts in it; of a run it declined, natively),
  `TestARefusedStartIsStartFailed` (`502 start_failed`, nothing
  registered), `TestWithExtensionsOffersTheEngineTheRuntime` (`New` given
  `WithExtensions` with a fake taking runs: an attempt's session and its
  check in the same fake; `New` without the option: the engine offered
  the native one), `TestAStartThatPanicsAfterItsProcessIsRefusedWithNoneLeft`
  (the offered Runtime a `FakeRuntime` with `Base` native and
  `PanicAfterStart`, taking the run: the attempt's launch is refused as a
  start that failed, the panic logged at error, and by then the process
  `Base` started, whose pid its pid file holds, is gone and reaped; the
  server serves on and the next attempt's start is made),
  `TestATakesThatPanicsOrStallsRunsNatively`
  (the guard: a panic, and an answer past `ext.TakesWait`, each run the run
  natively, logged as C1's guard logs), `TestAServerWithNothingRegisteredStartsNatively`,
  `TestARuntimeThatIsAStarterIsStartedFirst` (a fake that is both, given
  as the set's Runtime: `StartExtensions` calls its `Start(ctx, host)`
  once; a run launched before that has succeeded is not offered it and
  runs natively; one launched after is offered it, and its attempt's
  session starts through its `StartTerminal`),
  `TestAPersonsSessionOutlivesItsRequest` (the request's context cancelled
  as the start is made, through `launchHook`: the session starts, as on
  `main`).
  (PR-4) `TestAnAttemptAndItsChecksRanWhereTheyReport` (a managed run of the
  stub worker on a real ledger under a `FakeRuntime` with `Base` native and
  `where` `fake`, taking the run: the fake recorded the attempt's `StartTerminal`
  and its check's `Run`, and the task read's attempts and steps, the steps
  route and the prerequisites route say `fake`), `TestTheTaskReadReportsWhere`
  (the same run with nothing registered: `native`),
  `TestADeclinedRunsRowsSayNative`.
- `internal/hostagent` (PR-3): `TestAHostStartsThroughTheRuntime` (the
  fake's spec: argv, directory, environment, `{Session}` the registered id;
  the exit code from its terminal), `TestAHostWithNoRuntimeRunsNatively`.
- `internal/cli` (PR-3): `TestRunServeOffersTheRegisteredRuntime` (a fake
  through the test's `extensions`: the server's engine is offered it) and
  `TestRunHostRunsNatively` (the same fake: the host's `Options.Runtime` is
  nil, the native one).
- `ext/exttest` (PR-1): C9's `TestFakes` and `TestSuitePassesEveryFake`
  pass with the `FakeRuntime` PR-1 gives the new methods (each recorded,
  each unscripted one answering its refusal), and the package compiles
  `var _ ext.Runtime = (*FakeRuntime)(nil)`.
- `ext/exttest` (PR-5): `TestTheContractHoldsForTheNativeRuntime`
  (`exttest.Runtime` and `exttest.RuntimeExec` against `local.Native()`) and
  `TestTheContractHoldsForTheFake` (against `exttest.FakeRuntime`, with and
  without `Base`), `TestRunAllChecksTheRuntime` (a `Providers` holding a
  Runtime that breaks one rule fails `RunAll` with that rule's words),
  `TestRunAllTakesTheRuntimeCase` (a Runtime that records the argv it is
  asked to run, wrapping the native one, through `RunAll` with
  `WithRuntimeCase` naming a helper argv: every process the execution
  checks start is that helper's, and `RunAll` passes) and
  `TestRunAllWithoutACaseRefusesAnotherRuntime` (the same Runtime with no
  `WithRuntimeCase`: `RunAll` fails, naming the missing case; the native
  one with none: passes, its static checks run).

Web (PR-4): the type change; `npm --prefix web run typecheck`, `npm
--prefix web test` and `make test-e2e`, the existing suites unchanged (no
component reads the field, so no new spec).

By hand, what the tests do not reach (the Acceptance section's runs): a
real agent's PTY (Claude Code and Codex: the trust question, resize, Stop
ending its group) through the native Runtime on WSL and on the Mac;
`example-todo-app-tasks` run to its end there; an upgrade in place of a
copy of a data directory whose ledger a pre-PR-4 build wrote; `conductor
host` against a `conductor serve` on the same machine (the Mac's own, and
WSL's own at `127.0.0.1`, so nobody configures networking, AGENTS.md); the
Windows desktop app starting its WSL server and a session.

## Boxcar constraints

The Program's section 2.5 lists eight; each, for this item:

1. The engine starts an attempt only through `crew.Launcher`: honoured and
   made narrower. The attempt's session is still `Launcher.Launch`, which
   now starts it in its run's Runtime, and everything else a run starts
   (prerequisites, checks, steps, services) goes through the same Runtime
   as the engine's executor. The boundary test fails a new process started
   outside the runners, and a runner used past the Runtime. Not touched:
   the scheduler client and Brakeman reach `ext.Host` (C1, and their own
   items).
2. `session.Local` drives a `session.Process`: honoured. The Runtime's
   `Terminal` reaches `Local` through `session.RuntimeProcess`, and
   `internal/pty` is the native Runtime's implementation of it. The pid is
   read once, for the log line it is in today, from what the Runtime
   reports; nothing acts on it. The only `syscall.Kill` of a session's
   process stays in `internal/pty` (`pty.go:163` and `:172`); `ext/local`
   adds none, and the boundary test keeps the others (`proc`'s for checks,
   `nvim`'s and the agents probe's, none a session's) where they are.
3. An attempt's context is files under its worktree: not touched. The task
   files are still written through an `os.Root` before the session starts;
   the Runtime receives a directory, never an in-memory context; git (which
   makes and compares the trees) stays Conductor's, on the machine that
   holds the repository (decision RT10).
4. The agent talks to the server over the network: honoured. The Runtime
   receives the session's environment with `CONDUCTOR_NOTIFY_URL` and
   `CONDUCTOR_NOTIFY_TOKEN` as `main` builds them (`pty.Inject`); nothing
   adds a Unix-socket or in-process channel.
5. Checks go through `internal/proc` and carry where they ran: honoured.
   A check is still a `proc.Spec` the engine builds, run by `proc.Run` under
   the native Runtime, with its environment prepared by `proc.Prepare`; its
   row carries `where`. A provider keeps `proc`'s semantics by the contract
   on `Run`.
6. Platform splits stay platform tags: honoured. No tag is added; nothing
   registers a Runtime in this repository; the web app shows nothing of it.
7. The evidence rows gain `where`, native the only value until F: this is
   PR-4. No evidence assuming a native process is added: the existing pid,
   group and start time columns stay as they are, holding what the Runtime
   reports.
8. hostagent is a launch site as much as api: honoured. PR-3 wires
   `conductor host` and the server in the same PR, through the same
   `ext.Runtime`: the host's command starts through `Options.Runtime`, the
   native one, as a hosted session is a person's and a host runs no managed
   run (decision RT7); offering it a registered one is item F's.

## Open-core checklist

- [x] Article 1: no premium code and no licence check; the composition root
      asks only whether a Runtime is registered, once.
- [x] Article 2: the native Runtime is `main`'s behaviour; everything runs
      with nothing registered.
- [x] Article 3: nothing here is paid or gated; the Runtime has no hosted
      dependency.
- [x] Article 4: stop (a session's `Stop`, a run's stop, pause, cancel,
      retry, accept) acts as before; inspect gains `where`; revoke, export
      and approvals are untouched; none can lapse.

## The PRs

Each PR is one issue and one branch `issue-N` from `main`, cut after the
one it depends on has merged; it leaves `main` working and green on its
own, ticks the checklist above, and names its checks. Every PR updates
`docs/repository-map.md`'s rows it changes and adds its sentences to the
item's `docs/features.md` entry, which PR-1 opens and PR-5 completes.
Sizes are changed lines including tests. None runs beside another: each
edits that entry, and PR-2 to PR-5 each build on the one before.

**Order with C2 to C9.** The Program gives C10 C1 alone as its
prerequisite (section 4.2), so its PRs could run beside C2 to C9's, and
they would meet in the same packages: C4's PR-8 in `internal/crew` and
`internal/session`, C5's PR-9 and PR-10 in `internal/session` and
`internal/hostagent`, C6's PR-11 and PR-12 in `internal/crew/ledger`,
C7's PR-14 in `internal/opencore`, C9's PR-17 making `ext/exttest`, and
nearly all of them in `internal/api`, which `docs/plans/ext.md` already
runs one after another (PR numbers as in its table). This plan's default
is that none of its PRs runs beside theirs: PR-1 is cut once C2 to C9 are
Done (`docs/plans/ext.md`'s PR-18 merged), so every PR here is made on
top of theirs (decision RT15). PR-4 then changes the ledger after C6's
export and restore exist, and their tests run against version 6 in
PR-4's own checks; PR-5 adds to the `ext/exttest` that C9 made.

**PR-1: the Runtime's contract and its native default.** `ext`'s Runtime
members, `TakesWait`, `StopKillWait` and `RegisterRuntime`'s two refusals,
added to C1's declaration; every implementation of the slot C1 to C9
left given the new methods in the same PR, so the whole module still
builds: C9's `exttest.FakeRuntime` (each method recorded and answering
as a test sets it, an unscripted one its refusal: `StartTerminal`,
`Serve`, `Probe` and `Port` an error naming the method, `Run` `Exit` -1
with those words, `Takes` true unless set; its behaviour is PR-5's) and
the Runtime doubles of C1 to C9's tests (`ext`'s registry tests among
them), each found by the module's build; `ext/local`'s `Native()` (its
context checks, start seam and panic cleanup) and its place in `Fill`;
`proc`'s cleanup of a panicking `Started`; `internal/pty`'s `KillWait`
named; the
`ext-std` amendment and its plants in `internal/opencore`;
`TestPlainBuildRegistersNothing`'s Runtime slot; `internal/proc`'s
`Prepare`, `Spec.Environ`, and `Probe` and `FreePort` moved from
`internal/crew/steps.go` with their tests, `crew`'s `nativeExec` calling
them; the boundary test with its allowlist as it stands; the errata line
in `docs/plans/agent-harness.md`; `docs/repository-map.md` (`ext`,
`ext/local` and its "May import", `internal/proc`); the features entry
opened. Nothing calls the Runtime yet: behaviour is `main`'s. Touches
`ext`, `ext/local`, `ext/exttest` (the fake's methods), `internal/opencore`,
`cmd/conductor`, `internal/pty` (the constant's name), `internal/proc`,
`internal/crew` (the move), the test doubles of other packages that
implement the slot, `docs`. Tests: the `ext`, `ext/local`,
`ext/exttest` (PR-1), `cmd/conductor`, `internal/opencore` and
`internal/proc` tests above.
Depends on C1 to C9 (decision RT15) and #441. About 1,100 lines.

**PR-2: a managed run's trees run through the Runtime.** `execFor`, the
executor's functions taking the `ProcessRef`, `Engine.SetRuntime`, a run's
Runtime chosen by `Takes`, `NewEngine`'s native default, `nativeExec`
removed and `internal/crew` out of the boundary test's allowlist;
`sessiontest.FakeRuntime`; `docs/repository-map.md` (`internal/crew`),
`docs/architecture.md` (Crew runs); the features entry's sentence. Touches
`internal/crew`, `internal/session` (`sessiontest`), `ext/local` (the
allowlist), `docs`. Tests: the PR-2 tests of `internal/crew` and
`sessiontest` above; every existing crew test, its checks and steps now
through the adapter. Depends on PR-1. About 850 lines.

**PR-3: sessions start through the Runtime, on the server and on a host.**
`session.RuntimeProcess`; `Server`'s two Runtimes, set in `New`, which
wraps the `WithExtensions` set's in the guard (a process method's panic
logged and refused) and offers it to the engine, `LaunchSpec.Where`; `createLocalSession(ctx, …,
where)` and its three callers; `hostagent.Options.Runtime`; `runHost`
leaving it native; `internal/api` and `internal/hostagent` out of the
boundary test's allowlist; `docs/architecture.md` (the sessions diagram,
Packages), `docs/repository-map.md` (`internal/pty`, `internal/session`,
`internal/hostagent`); the features entry's sentence. Touches
`internal/session`, `internal/api`, `internal/hostagent`, `internal/cli`,
`internal/crew` (`LaunchSpec`), `ext/local` (the allowlist), `docs`.
Tests: the PR-3 tests of `internal/session`, `internal/api`,
`internal/hostagent` and `internal/cli` above; the existing PTY, viewer and
host tests on the native Runtime. Depends on PR-1 and PR-2 (the engine's
`SetRuntime`). About 800 lines.

**PR-4: every attempt and its evidence say where they ran (ledger schema
6).** The migration and the columns, migration 4's frozen prerequisites
table, the version-aware reads; the rows' `Where`, the puts' refusals, the
readers; the attempt's `where`, its rows', `AttemptState.Where` and
`StepState.Where`; the fake ledger's refusal; every downgrade fixture; the
web types; `docs/protocol.md`'s rows; `docs/architecture.md` and
`docs/repository-map.md` (`internal/crew/ledger`: schema version 6).
Touches `internal/crew/ledger`, `internal/crew`, `internal/api` (its
tests), `web/app/composables`, `docs`. Tests: the PR-4 tests of
`internal/crew/ledger`, `internal/crew` and `internal/api` above; the web
checks. Depends on PR-3. About 1,000 lines.

**PR-5: the Runtime's public fake and contract check.** The behaviour of
`ext/exttest`'s `FakeRuntime`, whose methods PR-1 added (the standard
library, `ext` and `ext/local` alone, as C1's table has `ext/exttest`: a
terminal over pipes, scripted results, `Takes` as set, every call
recorded, `Base` passing a call through). C9's `exttest.Runtime(t, r)`
keeps its signature and gains the checks that start nothing: a valid
`Name` and `Where`, constant; `Takes` within `TakesWait` from 32
goroutines at once. The checks that start processes are a new
`exttest.RuntimeExec(t, r, c RuntimeCase)`, and `RunAll` reaches them
through an option: `RunAll(t, p ext.Providers, opts ...RunOption)`,
variadic so C9's callers compile unchanged, with
`exttest.WithRuntimeCase(c RuntimeCase) RunOption`. `RunAll` runs
`Runtime(t, p.Runtime)` as C9 has it, and `RuntimeExec(t, p.Runtime, c)`
with the case given; a Runtime whose `Where` is not `native` with no case
fails `RunAll`, naming the option, so another module's CI cannot leave
the execution checks out by leaving the case out, while the native one
with none runs its static checks alone (`ext/local`'s tests and
`TestTheContractHoldsForTheNativeRuntime` run its processes). C9's
`TestSuitePassesEveryFake` passes `WithRuntimeCase` with the test
binary's helper; `TestSuitePassesEveryDefault` is unchanged; the package
documentation says the premium repository passes its case. The
execution checks are the rules this plan writes, checked as PR-1 checks
the native one: `StartTerminal` in the directory
given with exactly `Env`, output, input, resize and exit, `DirFile`
closed by the check as soon as it returns, an ended context answered with
its error and no `Terminal`, a context cancelled while the start runs
answered either way the contract allows (a `Terminal` is stopped by the
check); a `Started` that panics leaving `Run` and `Serve` as that same
panic; `Stop` by the rules above, asked (a helper
that exits on the ask: nil within its grace) and forced (a helper that
ignores it: nil within `grace` plus `StopKillWait`), and after an exit
(nil), descendants not checked, as the contract promises none; `Run` with argv never a shell (an argument with
`;` printed as it is), a timeout told from a cancel, head and tail, the
log within `LogLimit`, `Started` once and before the output; `Serve`
started and stopped; `Probe` one try, no redirect followed; `Port` a free
number; a cancelled context answered promptly. `RuntimeCase` gives the
argv of the helper program the checks drive (what it does on which
argument is in the package documentation; `exttest`'s own test runs its
test binary as the helper, Go's helper-process pattern), so another
module's CI runs the checks against its Runtime where that Runtime runs.
`docs/repository-map.md` (`ext/exttest`); the features entry completed
with what the Acceptance runs observed. Touches `ext/exttest`, `docs`.
Tests: the `ext/exttest` tests above. Depends on PR-4 and C9's PR-17
(`docs/plans/ext.md`). About 700 lines.

## Owner decisions

Dated 2026-10-30. The defaults below are this plan's, inside the twelve
articles; the owner may overturn any at the plan's review, and one
overturned is rewritten here before the PRs are cut.

- **Open decisions.** None is this plan's to answer. O9 (where Boxcar's
  code lives) blocks item F alone, so F's plan answers it; this contract
  serves either answer, since everything a provider needs, in this
  repository under a `linux` tag or in a module of its own, is in `ext`.
  O12 (the names `ext` and `app`) is C1's; this plan uses the Program's
  names. O16 (where paid features' UI lives) is not touched.
- **RT1, schema version 6.** The Program says version 5; it was written
  when `main` wrote 4, and EN1 has since taken 5 (#394), its plan saying
  the second of the two takes 6. Proposed status-log row: "2026-10-30 | C10
  | where lands as ledger schema 6: EN1 (#394) took 5".
- **RT2, the names.** Column `where_ran`, Go field `Where`, JSON `where`.
  Reason: `WHERE` is an SQL keyword, and the ledger already renames
  keyword columns that way (`pgroup`, `commit_id`, `from_member`); Go and
  JSON keep the Program's word.
- **RT3, which rows.** Attempts, checks (a task's checks and a manifest's
  steps) and prerequisites; not trees. Reason: those are the evidence rows
  (constraint 7) and the acceptance compares an attempt with its checks; a
  tree's lifecycle row belongs to its attempt.
- **RT4, the migration.** SQL alone, `DEFAULT 'native'` on every row a
  version 5 database holds, no backfill. Reason: only native code could
  run anything before this item, so the value is what happened; the
  owner's conservative rule (the version 3 to 4 migration) is met because
  nothing is claimed that the rows cannot support.
- **RT5, a row without `where` is refused, not defaulted.** Reason: a
  default would label any future runtime's omission native; a refusal makes
  it a failing test. `phase` keeps its default.
- **RT6, one Runtime per managed run, `where` per Runtime.** A managed
  run's Runtime is chosen once, when the run is made or continued, and its
  prerequisites and its attempts, their sessions, trees and checks, all
  run in it; an attempt's `where` is fixed when it is made. Reason: an
  attempt and its checks then report the same `where` by construction,
  and nothing mixes runtimes within a run; a `where` per process is item
  F's to propose if a guest needs it.
- **RT7, a registered Runtime is offered managed runs alone, and may
  decline one.** A session a person starts or resumes, a legacy crew's
  members and a hosted session start through the native Runtime whatever
  is registered; a managed run goes to the registered Runtime only when
  its `Takes` says so within the guard's wait, and runs natively
  otherwise. Every process still starts through an `ext.Runtime`, so the
  seam is one either way. Reason: article 7, a provider adds and never
  removes: every session that is not a managed run's stays native, a
  managed run is the provider's only when it takes it, and a stalled or
  failing `Takes` is answered as the free default by C1's guard. Offering
  a registered Runtime a person's or a hosted session (item F's first
  surface is `conductor host`) is item F's to propose, with how a person
  chooses it.
- **RT8, Conductor builds the environment.** A Runtime receives it whole.
  Reason: the filter that keeps tokens and loaders out stays in one place.
- **RT9, the whole tree executor.** `Serve`, `Probe` and `Port` beside
  `StartTerminal` and `Run`. Reason: a service is a process of a run (constraint
  1), and a tree's probe or port must be the tree's runtime's, not the
  server's loopback (EN1's executor).
- **RT10, git stays Conductor's.** Reason: the tree is made and compared
  where the repository is, and handed to a runtime mounted or copied
  (constraint 3).
- **RT11, no UI change.** Reason: the acceptance asks for no behaviour
  change; DS2 shows the evidence; a column saying `native` on every row
  tells a person nothing before item F.
- **RT12, the boundary test.** Reason: constraints 1, 2 and 5 become a
  failing test, so the boundary is code (decision D7), and a new process
  site is a reviewed edit.
- **RT13, C1's names.** This plan completes C1's Runtime declaration
  (`docs/plans/ext.md`, its Runtime paragraph and decision E4) rather than
  declaring its own: `Provider`'s `Name`, `Where`, `WhereNative`,
  `Where.Valid`, `RegisterRuntime`, `RuntimeProvider()`, `Freeze()`,
  `local.Fill` as the one place that asks, and `api.New`'s
  `WithExtensions` option as the server's one door; it adds the methods, `TakesWait`, two refusals to
  `RegisterRuntime`, the native default in `Fill`, and the `ext-std`
  amendment for `ext/local`. Reason: C1's plan gives the Runtime its slot
  and leaves these to this item, and nothing implements `ext` before P0.
- **RT14, the fakes.** Both are this item's: `sessiontest.FakeRuntime` for
  this repository's tests (PR-2), and the methods and behaviour of
  `ext/exttest`'s `FakeRuntime` (the methods with the interface in PR-1,
  the behaviour in PR-5) and the `exttest.Runtime` and `RuntimeExec`
  checks, reached from `RunAll` through `WithRuntimeCase`, for another
  module's CI (PR-5), as `docs/plans/ext.md` says in its C9 section (C9
  ships the fake and the check answering a `Where`). Reason: one owner,
  the one that writes the contract.
- **RT15, after C2 to C9.** PR-1 is cut once C2 to C9 are Done, so no PR
  here runs beside theirs (The PRs). Reason: they share `internal/api`,
  `internal/crew`, `internal/session`, `internal/hostagent`,
  `internal/crew/ledger`, `internal/opencore` and `ext/exttest` with this
  item's, `docs/plans/ext.md` already runs its `internal/api` PRs one after
  another, PR-4's schema change then lands on C6's export and restore, and
  PR-5 needs C9's `ext/exttest`. The owner may instead let PR-1 to PR-3 go
  as soon as C1 is Done, each rebased on whatever of C2 to C9 merged
  before it, with PR-4 after C6's PR-12 and PR-5 after C9's PR-17.

## Acceptance

The boundary, "an attempt and its checks report the same where; no
behaviour change on any platform", is observed three ways:

1. By test, in CI and on the Mac's row: `TestAnAttemptAndItsChecksRanWhereTheyReport`
   (through the server and a real ledger, the attempt's session and its
   check run in the fake Runtime, and every row and report says its
   `where`), `TestAnAttemptAndItsChecksReportTheSameWhere` (the engine's
   rows and reports, fake and native), `TestTheTaskReadReportsWhere` (a
   server with nothing registered: `native`) and
   `TestAVersion5DatabaseIsBroughtToVersion6`.
2. By hand on WSL and on the Mac, on the build of PR-4: the example crew
   `example-todo-app-tasks` (its prerequisite included), with real agents
   in a scratch repository both trust, run to its end; then `GET
   /api/runs/{run}/tasks/{task}` for each task, every attempt and receipt
   `"where": "native"`, and `sqlite3 runs/managed.sqlite "SELECT DISTINCT
   where_ran FROM attempts UNION SELECT where_ran FROM checks UNION SELECT
   where_ran FROM prerequisites"` printing `native` alone. A copy of a data
   directory whose ledger an earlier build wrote (version 5) opened by the
   same build: version 6, its old runs' attempts and receipts `native`, an
   interrupted run continued.
3. No behaviour change: every existing suite green, unchanged, on each PR
   (CI's Go race tests, e2e, desktop smoke and the build matrix; the Mac's
   race tests); and by hand, a real agent's session on WSL and the Mac
   (trust question, typing, resize, Stop), `conductor host` against a
   server on the same machine (the Mac, and WSL at `127.0.0.1`), and the
   Windows desktop app starting its server and a session, each as before.
   Windows runs the Linux server in WSL through the desktop app (ci.yml's
   build matrix has no windows row), so Linux's tests are its server's.

Done means PR-1 to PR-5 merged and the runs of 2 and 3 recorded in the
item's `docs/features.md` entry; Status moves to Done then, not when the
checklist is ticked. The owner merges each PR, decides RT1 to RT15 (and
the status-log row RT1 proposes) at the plan's review, and the runs on the
Mac and the Windows app use the owner's machines and the agents' logins
there, by the owner or with the owner's word.
