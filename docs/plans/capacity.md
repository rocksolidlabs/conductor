# Capacity: the agent, model, account, host and usage windows shown before a launch, and work bound to its account (Program item AC1)

Status: proposed 2026-11-05, for the owner's review; the owner sets it to
Planned. Main at 43abe4f7 (PR #656, 2026-11-05). Every fact below about
the code was read from that commit on 2026-11-05; every fact about an
agent's command-line tool was read on 2026-11-05 from the tool installed
on the planning machine, through its documented commands and its own
output, never from a credential or login file.

A person who launches a session, a crew or a ticket's approved plan today
chooses an agent and nothing else about where its work is paid from.
Conductor does not know which login the agent will use, whether that
login is near a provider's limit, or when the limit resets; it learns of
a limit only when the agent stops in its terminal. The only account
Conductor knows is Brakeman's: a folder its configuration names, set in
the turn's session as the adapter's `ConfigDirEnv` (`CLAUDE_CONFIG_DIR`,
`CODEX_HOME`), and the Brakeman plan left an account registry and usage
windows to this item (`docs/plans/brakeman.md`, Out of scope and Owner
decisions, 10).

This plan gives Conductor **account references** (a label and the
folder an agent's own login lives in, or that agent's default login on
the server), a **default account** per agent, and a **capacity view**
read before a launch: the agent and its version, the model, the account,
the execution host, each **usage window** the provider reports with the
share used and its reset, the provider's own credit figures, Conductor's
own counts kept apart as estimates, how fresh the reading is, and an
explicit state for everything it cannot know. A session, a crew run and
every attempt of a managed run are **bound** at launch to the account and
model they were launched with; changing the default afterwards moves
nothing that runs. Brakeman can read the capacity view and propose
waiting for a reset, holding work, or a permitted alternative; it never
moves work to another account. Capacity informs a person's planning: it
never refuses, delays or reroutes a launch.

It is Program item AC1 (the Program, section 4.5), written after reading
the catalog and the adapters (`internal/catalog/catalog.go`,
`internal/agents/adapter.go`, `claude.go`, `codex.go`, `probe.go`,
`registry.go`, `skill.go`), the identity probe's cache
(`internal/api/probe.go`), the session launch and resume
(`internal/api/sessions.go`, `resume.go`, `sessionclone.go`,
`internal/session/agentsession.go`, `info.go`), the crews and their runs
(`internal/crew/crew.go`, `run.go`, `task.go`, `continue.go`, the ledger's
schema in `internal/crew/ledger/ledger.go`), the launcher
(`internal/api/runs.go`, `Server.Launch`), Brakeman's configuration, desk,
reads and refs (`internal/brakeman/config.go`, `refs.go`, `kinds.go`,
`internal/api/brakeman.go`, `brakeman_observe.go`,
`brakeman_account_unix.go`), the capabilities route and the operating
guide (`internal/api/capabilities.go`, `guide.go`), `ext.Host`
(`ext/host.go`), the process boundary (`ext/local/boundary_test.go`), and
the launch dialogs (`web/app/components/LaunchSessionModal.vue`,
`DispatchLaunch.vue`, `BrakemanSettings.vue`, `web/app/pages/crews/[[id]].vue`).
It is reviewed before any of its code; the PRs at the end are cut into
issues after the review, one issue, one branch `issue-N`, one PR each
(AGENTS.md).

The names follow the railroad (the Program, section 1.2): Conductor is
the application; Yardmaster the agent harness, whose engine
(`internal/crew`) runs crews and managed runs and never reads capacity;
Brakeman the sidekick, which may read it and propose; Dispatch the
ticketing, whose items are tickets and whose approved plan revisions
launch runs. Four words are kept apart here: an **account** is a
reference to an agent's own login on this server (never a credential,
never Conductor's own account, never a paid Workspace); a **reading** is
what an agent's own tool reported of that login's usage at one moment; a
**window** is one usage limit the provider reports (a five-hour window, a
weekly one, a weekly one for one model); an **estimate** is a figure
Conductor counts itself. "Recipe" means the catalog's agent recipes only;
nothing here touches a project manifest (Program item EN1).

What the other items give this one, and where they meet it:

- **The prerequisite the Program names, adapter capabilities, is on
  `main`.** Each adapter declares its probe (`Adapter.Probe`), where its
  login folder is moved (`ConfigDirEnv`: Claude Code and Codex), its
  model flag (`ModelArgs`: Claude Code and Codex) and its read-only mode;
  the catalog says which program each agent runs. BK0's capabilities
  document is `GET /api/capabilities` (C9, #488) and the operating guide
  (`internal/api/guide.go`), both merged; the Program names
  `docs/plans/capabilities.md` for BK0, but BK0 is planned in
  `docs/plans/brakeman.md`, which says so, and there is no
  `capabilities.md`. Capacity is not a provider: no `ext` interface
  carries it, and `GET /api/capabilities` is unchanged.
- **Brakeman** (`docs/plans/brakeman.md`, BK1 and BK2 merged): its
  configuration's `account` folder, its visibility classes, its shared
  reads (`brakemanReads`), its evidence refs and its `proposal/1` items
  are on `main`. That plan owns them; this one adds one visibility class
  (`capacity`), one ref kind (`capacity:`), one guide section and one
  field per agent in the guide's host section (PR-6). **Not on `main`:**
  BK3's background role (PR #655, issue #536, in review). PR-8 adds a
  capacity warning to its wake sources and waits for it; the wake list is
  that plan's.
- **DS2** (`docs/plans/dispatch-evidence.md`, merged): a ticket's
  approved plan revision freezes a crew body in its launch package. That
  plan owns the package; this one adds two optional member fields,
  `account` and `model`, which the body carries like any other field, so
  an approved revision names the account and model it approved.
- **C10** (`docs/plans/runtime.md`, merged): a session's and an
  attempt's `ext.Where`. That plan owns it; this one reads it, and a
  launch whose Runtime is not the native one shows its usage as
  unknown.
- **DR1 and RV1** (`docs/plans/decisions.md`, `docs/plans/review-repair.md`)
  take the ledger's next schema versions (DR1's version 8, PR #664). This
  plan changes no ledger schema: a run's binding is a field of the crew
  JSON the ledger already keeps for each run (`runs.crew`).

## Articles it serves

1. No premium code, no licence check: capacity ships whole in this
   repository; nothing asks for a plan, a tier or an entitlement. The
   "plan" a reading names is the agent's provider subscription as its own
   tool reports it, shown and never branched on.
2. Fully useful alone: account references, readings, binding and the
   views run on the free build with the person's own agent tools and
   logins, no Conductor account and no network beyond the agent tool's
   own call to its provider.
3. Paid features gated by hosted dependencies: not touched. Nothing here
   is paid; a Workspace-wide view of members' capacity would be a paid
   item with its own plan and is out of scope.
4. Stop, inspect, revoke, export, required approvals: untouched and never
   weakened. Capacity refuses no stop and gates no launch; the accounts
   document is a plain file in the data directory, inspected through
   `GET /api/accounts` and carried by C6's export; an approved plan
   revision's account and model are part of what the operator approved,
   so capacity never stands in for an approval.
5. Dependencies one way: no new module and no new Go or npm dependency.
   The new package `internal/capacity` imports the standard library and
   `internal/store` alone; `internal/crew` never imports it (a test
   holds that, PR-3).
6. No feature build tag: none added. The readers' checks against the
   real tools run in the existing `recipes` test tier.
7. The free default is `main`: a launch that names no account takes the
   agent's default, which is the agent's own login until a person sets
   another, exactly what `main` launches; no existing route changes its
   answer for a request that names no account or model; the main MCP
   tools list is unchanged (no new tool; a Brakeman turn's `observe`
   tool gains `capacity` in its `what` enum, PR-6).
8. A hosted dependency unreachable: not touched; nothing here reaches a
   hosted service of Conductor's. A provider the agent's tool cannot
   reach gives a reading in state `error`, never a guess.
9. `CGO_ENABLED=0`: no cgo; no SQLite schema change; the accounts are one
   JSON document through `internal/store`; readings are kept in memory.
10. The repository contract: this plan before code; one issue, branch and
    PR per row of The PRs; Go race tests per package, vitest and
    Playwright for the pages, the `recipes` tier for the tools. No
    WebSocket frame or control message, so `internal/proto` and
    `web/app/utils/protocol.ts` are untouched; `docs/protocol.md` gains
    the HTTP routes, the launch fields and one Server-Sent Event,
    `capacity`, with its size limit and its test (an event is typed in
    `docs/protocol.md` and the composable that reads it, as the `run`,
    `dispatch` and `brakeman` events are).
11. Names: the railroad names as above; "account" is an agent's login,
    never the paid tenant, which stays "Workspace"; nothing here says
    workspace.
12. Execution behind one seam: readings are no part of any session or
    run; they run where the agent's login is, which is the server for
    every launch Conductor makes natively, and a launch on another
    Runtime is shown unknown rather than read on the server (Boxcar
    constraints below).

## Why and what happens

The Program asks (section 4.5) that before a launch a person sees the
agent, the model, the account, where it runs, the usage windows with
their resets and how fresh that is, with unknown said as unknown and
never as zero; that a running session keeps its account whatever the
default becomes; and that capacity helps a person plan without ever
authorising, delaying or rotating anything. People running several
agents on their own subscriptions meet a limit in the middle of a run
today, find out from a stopped terminal, and cannot tell before a launch
which login a crew member will use.

What each supported agent's own tool reports, read on 2026-11-05 (the
qualification this plan's readers start from):

| Agent (adapter) | Provider | Version read | What its tool reports before a launch, and how | Known | Fresh | Unknown |
|---|---|---|---|---|---|---|
| Claude Code (`claude`) | Anthropic | 2.1.318 | `claude -p /usage --output-format stream-json --verbose --no-session-persistence --setting-sources local --strict-mcp-config --max-turns 1` runs the local `/usage` command with no model turn (its result line says `num_turns` 0, `total_cost_usd` 0, `local_command` `usage`) and prints a line carrying `usage_report`: `rate_limits.limits[]` (each `kind`: `session` for the five-hour window, `weekly_all`, `weekly_scoped` with `scope.model.display_name`; `group`, `percent`, `resets_at` in ISO 8601, `severity` `normal`, `warning` or `critical`, `is_active`) and `rate_limits.extra_usage` (`is_enabled`, `monthly_limit`, `used_credits`, `utilization`, `currency`). The init line's `apiKeySource` says whether a subscription (`none`) or a key pays. `claude auth status --json` reports `loggedIn`, `authMethod`, `apiProvider` and `subscriptionType`. About 23 s for both on the planning machine. | the windows the provider reports, their share used and reset, extra usage, the subscription type | as the provider answers at the moment of the read | a window not in the list (that day no five-hour window was listed: only the two weekly ones); any figure for a key-paid login; the text the command prints for people (which names the person's folders and habits) is never read |
| Codex (`codex`) | OpenAI | 0.163.0 | `codex app-server` (marked experimental in `codex --help`) speaks JSON-RPC on stdio; after `initialize` and `initialized`, `account/read` answers the login's `type` (`chatgpt`, `apiKey`, `amazonBedrock`) and `planType`, and `account/rateLimits/read` answers `rateLimits` and `rateLimitsByLimitId`, each with `primary` and `secondary` windows (`usedPercent`, `windowDurationMins`, `resetsAt` in Unix seconds), `planType`, `credits` (`hasCredits`, `unlimited`, `balance`), `rateLimitReachedType`, `spendControlReached`, and `ordinaryUsageAllowed`. Both methods are in the stable protocol schema (`codex app-server generate-json-schema` without `--experimental`). About 2 s. | the windows (that day one, of 10,080 minutes, and no secondary), share used, reset, plan type, credit balance, whether ordinary use is allowed | as the provider answers at the moment of the read | a window reported as null; any figure for a key login; the schema also offers `config/read`, which would return the person's configuration (it can hold a secret) and which the reader never sends |
| GitHub Copilot CLI (`copilot`) | GitHub | 1.0.94 | `copilot help billing` documents the remaining credit budget in the interactive footer, the `/statusline` `quota` option and `/usage` inside a session, and `--usage-output-file` after a run; nothing reads it before a launch | nothing before a launch | — | all of it: shown unknown |
| agy (`agy`) | Google | 1.4.0 | `agy --help` lists no usage or quota command | nothing | — | all of it |
| goose (`goose`) | the provider goose is configured with | 1.54.0 | `goose --help` lists no usage or quota command; its limits are its provider's | nothing | — | all of it |
| Cursor, OpenCode, pi, omp, aider, Amp, dsh | various | not installed on the planning machine | not checked: whether their tools offer a way is not known yet; PR-1 reads the `--help` of the four the nightly job installs (OpenCode, aider, Amp, pi), in a scratch prefix, and records what each lists in its body | nothing | — | all of it, said as "Conductor knows no way to read this agent's usage" |

After the item ships, a person sees:

- **Settings, Accounts.** One card per agent whose adapter can move its
  login folder (Claude Code, Codex): its **default login** on this server
  (always there, no folder), the accounts added (a label such as "Work",
  and the folder that login lives in, picked with the folder button), the
  **default** for that agent, and for each account its plan as the tool
  reported it and its last reading. Add, rename, set as default, retire
  (it stays for the work bound to it and leaves every choice), and
  remove (refused while anything is bound to it, with what is). "Read
  now" reads that account's usage, at most once in five minutes. An
  agent of another adapter has its default login only and its card says
  its usage is unknown and why.
- **Launching a session.** After the agent, an **Account** choice (the
  default chosen) and a **Model** field (empty for the agent's default),
  for an agent whose adapter takes them; below them the **capacity
  panel**: "Runs on this server" with its name, or "On this computer:
  your own login there, usage unknown here" when the person runs it
  themselves; the plan ("Claude Max", "ChatGPT Pro") as the tool reported
  it; each window as a bar with its words ("Weekly, all models: 40% used,
  resets Wed 11 Nov 03:00, in 5 days"), coloured by the provider's
  severity, a window the provider did not report not drawn and never
  drawn as empty; the provider's credit figures in their own row
  ("Extra usage: off", "Credits: 1,250"), marked as the provider's;
  Conductor's own count in a separate row marked as an estimate ("2
  sessions running on this account here, 5 started in the last 5
  hours, counted by this server since it started"); and the freshness
  ("Read 3 minutes ago by Claude Code 2.1.318", "Reading…", "Stale:
  read 40 minutes ago", "A reset has passed since this reading").
  Every state that is not a reading says what it is: unknown for an
  agent no reader knows, not installed, signed out, a login paid by key
  (no windows reported), a version the reader is not qualified for,
  another runtime, or the error with the tool's words. Launch is never
  disabled by capacity; at the provider's warning or critical severity,
  or 90% used, the panel says the agent may stop before the reset.
- **Crews and tickets.** The crew editor gives each member an account and
  a model beside its agent. Launching a crew opens a sheet that lists each
  member with its agent, model, account and a compact capacity line, then
  launches. A ticket's approved plan revision shows the same lines, read
  only, before Launch: the revision's accounts and models are what the
  operator approved. The run page shows each member's bound account and
  model.
- **A change of default moves nothing that runs.** A person sets Codex's
  default from the default login to "Work" while a run goes: the run's
  next attempt, a retry, a repair, a continuation after a restart and a
  member resumed all keep the account they were launched with; the next
  new launch takes "Work". A session resumed keeps its account; when that
  account was removed, the resume is refused with words, never moved to
  another login.
- **Brakeman.** Its settings pick its account from the list (the
  default login or an added one), and a new visibility switch,
  "Capacity", lets it read the capacity view. Asked to brief, or to
  propose, it may say "the weekly window of Work resets Wed 03:00; launch
  CND-12 after it", "hold run r-14 while the five-hour window is at
  95%", or "the run's review member could use another model on the same
  account, whose weekly window has room", each fact citing the reading it
  rests on; it never proposes another account of the same provider to get
  round a limit. With BK3's role, a reading at warning or critical wakes
  it (PR-8).
- **The command line.** `conductor accounts` lists, adds, renames,
  retires, removes and sets defaults; `conductor capacity` prints the
  capacity view of an agent and account (`--read` to read now, `--json`).

## The changes

### `internal/agents`: the usage readers (PR-1)

`Adapter` gains `Usage *UsageReader`, nil for every adapter but Claude
Code's and Codex's: an adapter without one is "Conductor knows no way to
read this agent's usage", never zero. In `usage.go`:

```go
// UsageReader reads an agent's usage through its own tool.
type UsageReader struct {
	// Qualified is the version the reader was checked against
	// (the recipes tier and the fixtures); MinVersion the oldest it reads.
	Qualified, MinVersion string
	Read func(ctx context.Context, in UsageInput) UsageReport
}

type UsageInput struct {
	Command []string          // the catalog agent's program, resolved, then its argv
	Env     []string          // built as the launch builds it (below), then ConfigDirEnv=<folder> for an account with one
	Dir     string            // <dataDir>/capacity/run, made 0700, empty
	// Admit is asked, with the version this read itself found, before
	// Claude Code's `/usage` run is started, whatever the version
	// (Qualification, and One owner in PR-2); nil admits none. A read
	// that ends at `auth status` (signed out, key-paid) asks none.
	Admit func(version string) (Admission, error)
}

// Admission is one run's leave to start. Done is called exactly once,
// with what the run's own output proved.
type Admission interface{ Done(o Outcome) }

// Outcome is what one Claude Code run proved. Local only for a result
// line with num_turns 0 and local_command "usage"; Turned for a result
// line with num_turns above 0 or another local_command; Inconclusive for
// every other end: a timeout, end of file or the child's exit before a
// result line, a malformed line, an output bound passed.
type Outcome int

const (
	Inconclusive Outcome = iota // the zero value: proves nothing
	Local
	Turned
)

type UsageReport struct {
	State    string        // ok | signed_out | key_paid | unqualified | error
	Words    string        // why, for every state but ok; at most 300 bytes
	Version  string        // the version this read's own check found; "" when none
	Outcome  Outcome       // what the run proved (Claude Code); Inconclusive for Codex
	Plan     string        // the subscription or plan type as the tool says it; at most 40 bytes
	Windows  []UsageWindow // at most 16
	Credits  []UsageCredit // at most 4
	Limits   UsageLimits   // the provider's refusal flags, apart from any window
	ReadAt   time.Time
}

// UsageLimits are the provider's own flags; nil (or "") is not reported,
// never false.
type UsageLimits struct {
	Reached         string // Codex's rateLimitReachedType as it says it; at most 40 bytes
	SpendControl    *bool  // Codex's spendControlReached
	OrdinaryAllowed *bool  // Codex's ordinaryUsageAllowed
}

type UsageWindow struct {
	Kind      string     // the provider's word: session, weekly_all, weekly_scoped, primary, secondary
	Name      string     // for people: "5-hour", "Weekly, all models", "Weekly, <model>"; at most 80 bytes
	Minutes   int        // the window's length when the provider says it, else 0
	Percent   int        // 0 to 100, the provider's share used
	ResetsAt  *time.Time // nil when the provider gave none
	Severity  string     // normal | warning | critical | "" when not reported
	Active    *bool      // Claude Code's is_active; nil when not reported
	Limit     string     // Codex's limit id ("codex"); at most 40 bytes
}

type UsageCredit struct {
	Kind     string  // extra_usage | credits
	Enabled  *bool
	Used     *string // as the provider wrote it, a decimal string; at most 32 bytes
	Limit    *string
	Balance  *string
	Unlimited bool
	Currency string
}
```

A window the provider did not report is not in `Windows`; a figure it
gave as null stays nil. No reader fills a missing value.

Qualification: the version a reader is judged by is never the identity
probe's cache, which keeps an identity ten minutes by command, adapter
and environment and so can describe a program that updated itself at the
same path since. Each read, for both readers, keeps the catalog's whole
command, as the identity probe does (`RunProbe` runs `Command[0]` with
`Command[1:]` before its own arguments, `internal/agents/probe.go`), so a
wrapped command such as the e2e catalog's `[@BASH@, @STUB@]`, a
`[bash, script]` or an `[npx, codex]` is asked as the tool it wraps,
never as the wrapper. The read resolves `Command[0]` to its file
(`exec.LookPath`, then `filepath.EvalSymlinks`) and every later element
of the command that names an existing regular file (the script a
wrapper runs) to its file the same way, notes each of those files'
identity (device, inode, size, modification time), runs the resolved
prefix followed by `--version` with the probe's parsing and bounds,
notes the identities again, and starts the read with that same resolved
prefix, followed by the reader's own arguments, only when every note
agrees. The version checked is the one the whole prefix prints, parsed
by the adapter's probe (`Probe.Match`, after `Probe.Reject`): the
wrapped tool's, never the wrapper's; a prefix whose answer the probe
does not identify as the adapter's tool (a wrapper printing its own
version) is `unqualified` ("<command> does not answer as Claude Code").
What a wrapper reaches without naming it as a file (a package runner's
cache) has no identity to note: there the version the prefix prints is
the gate, and a change between that print and the start is bounded by
the turn guard below. A version it cannot parse, a `--version` that
fails, or a file whose identity changed between the notes ends the read
`unqualified` ("the program changed while it was checked", or the
tool's words) with nothing started: it fails closed.
The version found so is the one compared below, recorded, and named in
the view (`ReadBy`). What the notes cannot see, a replacement in the
moment between the second note and the start, is bounded by the turn
guard below: such a start can spend at most one turn (`--max-turns 1
--tools ""`), and that turn is recorded.

A version below `MinVersion` is `unqualified` with "read only from
<MinVersion>". Codex's methods cost no turn whatever the version, so a
Codex newer than `Qualified` is read, and the view says both versions
("read by 0.164.0, checked on 0.163.0"); a format that changed shows as
`error` with the field that failed, never as a guess. Every Claude Code
`/usage` run, of any version, is started only under an admission
(`UsageInput.Admit`), since any run can answer `/usage` with a model
turn, which the record must then hold: the caller answers it from the
durable record of versions (`internal/capacity`'s `Versions`, PR-2), and
only on the server that owns that record (One owner, PR-2). A version
from `MinVersion` to `Qualified`, the ones the reader was checked
against, is admitted for every read unless recorded `turned`; a newer
version recorded `checked` is admitted for every read; one recorded
`turned` or `interrupted`, or not recorded, is refused, and the view is
`unqualified` with "2.1.320 is newer than the 2.1.318 the reader was
checked on"; only the admin's explicit check (`POST /api/capacity/read`
with `check: true`, the panel's "Check this version" with those words,
`conductor capacity --read --check`) asks for a version not recorded, or
recorded `interrupted`, to be admitted once. The reader ends every run
with exactly one `Done`, with the `Outcome` the run's own output proved,
and reports it (`UsageReport.Outcome`). A version becomes `checked` only
from `Local`, a result line seen saying no turn was taken. `Turned` on
any version, the `Qualified` one included, records it `turned`, and it
is never started again. `Inconclusive` proves nothing either way: a
check that ends so (killed at its timeout, its output ended or its child
gone before the result line, a malformed line, a bound passed) records
the version `interrupted`, refused to every read until the admin checks
it again, and answers `error` with "the check of 2.1.320 ended without
an answer: check it again"; an `Inconclusive` read of a version already
admitted for every read leaves its record as it is (that version was
already seen answering with no turn) and answers `error` with the tool's
words. The invariant: no Claude Code program is started without the
admin's explicit check unless a run of that version was seen answering
`/usage` with no turn, and every run's outcome is recorded, by the
server that admitted it, before the read answers. `internal/agents`
keeps no record itself: what is kept, and how it survives a restart, a
crash and two accounts at once, is `Versions`'.

What runs where: the nightly `recipes` tier installs each tool with no
login (`.github/workflows/nightly.yml`), so it checks what a signed-out
tool can say: for Codex, `app-server generate-json-schema` holds both
methods and every field the reader reads; for Claude Code, the flags the
reader passes are in `--help`, and `auth status --json` signed out
answers `loggedIn` false. The `/usage` report itself needs a login: it is
checked by hand on a logged-in machine, the check above, for every
version `Qualified` is moved to (the PR that moves it records the read).
The recipes tests are named `TestRecipe…` so `make test-recipes`'s
`-run 'Recipe|TheCLI'` selects them.

**Claude Code's reader** (`usage_claude.go`) runs two commands through
`exec.CommandContext`, as the identity probe does (internal/agents is one
of the places `ext/local`'s boundary test allows a raw start, for the
probe; the readers are the same kind of short, bounded, server-side
question to the agent's own tool): `<command…> auth status --json`, decoded
into a struct of `loggedIn`, `authMethod`, `apiProvider` and
`subscriptionType` alone, so the email, the organisation and the login's
expiry are never held. `auth status` runs with the launch's environment
and the account folder's own settings, as the session reads its login,
so it decides payment: `loggedIn` false is `signed_out`, and an
`authMethod` that is a key or an `apiProvider` that is not Anthropic's
own is `key_paid`, answered from `auth status` alone with `/usage` never
sent. Only a subscription login of a checked version (above) then runs
`<command…> -p /usage --output-format stream-json --verbose
--no-session-persistence --setting-sources local --strict-mcp-config
--tools "" --max-turns 1` (`--setting-sources local` so no hook or
plugin of the person's runs in a read; `--tools ""` so no tool can run
whatever the version does). From its
lines it keeps the init line's `apiKeySource`, the `usage_report`'s
`rate_limits` (never its `session` part, never the message text, which
is the `/usage` command's words for people and names the person's
folders), and the result line's `num_turns` and `local_command`. A result
with `num_turns` above 0 or another `local_command` means this version
answered `/usage` with a model turn: the report is `unqualified` with
`Outcome` `Turned`, and the caller records the version `turned`
(`Versions`, PR-2), never run again. Only a result line with `num_turns`
0 and `local_command` `usage` is `Local`; a run that gives no result
line, for any reason, is `Inconclusive`, never `Local`. `apiKeySource` other than `none` on the init line is a second
guard: the report is `key_paid` and its windows are dropped.

**Codex's reader** (`usage_codex.go`) starts `<command…> app-server
--listen stdio://` and sends, in this order and nothing else, `initialize`
(`clientInfo` `{name: "conductor", version}`), the `initialized`
notification, `account/read` (`{}`) and `account/rateLimits/read`; it
reads replies by id, skips notifications, then closes stdin and ends the
process group. The list of methods is a constant (`codexUsageMethods`)
that a test holds whole: `config/read`, which would answer the person's
configuration, is never sent. From `account/read` it keeps `type` and
`planType` (never `email`); from the limits, each snapshot of
`rateLimitsByLimitId` (or `rateLimits` when that is absent) as windows
named by length (300 minutes "5-hour", 10,080 "Weekly", otherwise "N-hour"
or "N-minute"), with its `limitId` and `limitName`, and its `credits`
when present; `rateLimitReachedType`, `spendControlReached` and
`ordinaryUsageAllowed` go into `Limits` as the provider gave them (absent
stays nil, never false), whether or not any window or credit was
reported, and a window the reached type names is also `critical`;
`accountId` is never kept. `type` `apiKey` is `key_paid`; `account` null with
`requiresOpenaiAuth` is `signed_out`.

Both readers run with the environment a session of that catalog agent
gets, built by the same function over the same lists: `pty.BuildEnv(
pty.ParentEnv(), passthrough, env, nil)` with `passthrough` the server's
`EnvPassthrough` plus the agent's, and `env` the adapter's env and the
agent's own (`CONDUCTOR_*` stripped, as `BuildEnv` does), then
`TERM=dumb`, `NO_COLOR=1` and the account's folder as `ConfigDirEnv`. One
function in `internal/api` (`agentEnv`) builds that part for a launch and
for a reader, so the two cannot drift. A `ConfigDirEnv` that this
environment already sets (the entry's own env, or the server's
passthrough) pins the agent's login: such an agent has the `default`
account only (PR-4), and its reader reads the pinned folder. What a
single launch adds on top (yolo's env, an attempt's approved names, a
request's env) is not read: a launch whose additions set or pass the
adapter's `ConfigDirEnv` or one of its login variables (`AuthEnv`, a
fixed list per adapter: Claude Code's `ANTHROPIC_API_KEY`,
`ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL`, `CLAUDE_CODE_OAUTH_TOKEN`
(added in PR-3's review: the long-lived token login Claude Code documents),
`CLAUDE_CODE_USE_BEDROCK`, `CLAUDE_CODE_USE_VERTEX`; Codex's `OPENAI_API_KEY`, `CODEX_API_KEY`,
`OPENAI_BASE_URL`) is shown `env_set` ("this launch's environment sets
the agent's login: its usage is not read"). Each reading keeps a SHA-256
of the variables it read with (in memory, never their values); a view
whose environment now hashes otherwise is `stale` with "the agent's
environment changed since this reading". The reader runs in
`<dataDir>/capacity/run` (one folder for every read, so Claude Code's list
of projects gains one entry and not one per read), stdin from nothing but
Codex's pipe, each output line at most 1 MiB and all of it at most 4 MiB
(the rest drops the read with `error`), within a timeout (Claude Code's
90 s, Codex's 30 s; variables a test shortens, as `probeTimeout` is), the
process group ended past it. The tools write what they write in the
account's folder (Claude Code's and Codex's own state); Conductor reads
nothing there.

### `internal/capacity`: accounts, readings and the cache (PR-2)

A new package, standard library and `internal/store` only, with no
process, no HTTP and no session: what it cannot know it asks of `Facts`,
which `internal/api` implements (as `internal/brakeman` does).

**Accounts** (`accounts.go`), one document `capacity/accounts.json`
(directory 0700, file 0600) through `internal/store`:

```go
type Accounts struct {
	Rev      int64             `json:"rev"`
	Accounts []Account         `json:"accounts"`
	Defaults map[string]string `json:"defaults,omitempty"` // adapter -> account id; absent is "default"
	Removed  []string          `json:"removed,omitempty"`  // ids removed, never minted again; at most 256
}

type Account struct {
	ID        string    `json:"id"`      // minted by the server: "a-" and 12 base32 characters; never "default"
	Adapter   string    `json:"adapter"` // an adapter with a ConfigDirEnv
	Label     string    `json:"label"`   // 1 to 60 characters
	Folder    string    `json:"folder"`  // absolute, at most 4,096 bytes; fixed once added
	Retired   bool      `json:"retired,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}
```

The account `default` exists for every adapter without being stored: the
agent's own login on this server, no folder. At most 32 accounts, retired
ones included; the document at most 64 KiB. `Validate` asks `Facts` that
the adapter exists and has a `ConfigDirEnv`, and that the folder passes
the check Brakeman's account folder passes (`checkAccountDir`: a
directory the server's user owns; the API's existing function). A
folder never changes once added: a person who means another folder adds
another account. An id is minted by the server at the add (random,
never taken from the label), checked against every id the document holds
and every id in `Removed`, and never minted again once removed, so an id
means one folder for good. A binding keeps the folder beside the id
(PR-4), and every later use of it (an attempt, a resume, a continuation,
an approved launch) refuses `account_gone` when the id is gone or now
names another folder, or when the effective login the use would get is
not the bound one (PR-4). Every write names the `expectedRev` the caller
read and is refused `stale_rev` otherwise. `Retire` keeps the record for
what is bound to it and leaves it out of every choice and default;
`Remove` asks `Facts.Bound(id)` and is refused while a session the
server lists, an engine run, a ledger run in state `running` or
`interrupted`, or a launch under way (a reservation, below) names it. A
run `stopped` or `finished` is no bar: continuing it later is refused
`account_gone`, never moved; so is an approved Dispatch revision that
names a removed account. A default that names a retired or removed
account reads as `default`.

**One owner.** The document is written by one server: the one holding
`capacity/owner.lock` (`store.OwnerLock`, taken and waited for as
Brakeman's records take theirs). A server without it (another server
over the same data directory holds it, or a platform with no owner lock)
reads the document, answers `GET /api/accounts` with `owner: false` and
why, refuses every write (`409 accounts_not_owner`), and refuses a
launch whose account would resolve to a registered account (not
`default`, not Brakeman's `unlisted` folder, PR-4), with those words; so
no server binds an account whose removal it cannot see. The same owner
alone writes `capacity/versions.json` (below), and since any Claude Code
run can change what that record must hold (a turn on any version, a
`checked` or the `Qualified` one included, records it `turned`), the
owner alone starts one: a server without the lock starts no Claude Code
program at all, for a read or a check, of any version; it answers the
view `not_owner` ("another server over this data directory reads Claude
Code's usage: <why>") and a check `409 accounts_not_owner`. Codex's
reads, which cost no turn and change no record, are made by any server.
So every Claude Code run is admitted, and its outcome recorded, by the
one process that holds the record, and neither server, restarted or
not, starts a version a run of it was seen to turn on.

**Versions** (`versions.go`), one document `capacity/versions.json`
(0600, at most 16 KiB) through `internal/store`: a record per adapter and
version, in one of four states, `checking`, `checked`, `turned` or
`interrupted`. Admission is one transition under one server-wide lock
(`versionsMu`), whichever agent and account ask: a version with no
record, or `interrupted`, under the admin's check, becomes `checking`, and
the document is saved (the store's atomic write) before `Admit` returns,
so the record is on disk before the program starts; a second ask for a
version in `checking` (another account at the same moment, or the same
one) is refused `check_under_way` with words; `Done(Local)` moves it to
`checked`, `Done(Turned)` to `turned` and `Done(Inconclusive)` to
`interrupted`, each saved before the read answers, so a check becomes
`checked` only from a run seen answering with no turn. A read of a
version admitted for every read (`checked`, or from `MinVersion` to
`Qualified`) takes no exclusive record: `Done(Turned)` records it
`turned` the same way (a record is made for a `Qualified` version then),
and `Local` or `Inconclusive` leave its record as it is. At open, every
`checking` record is a check no process finished (a crash, a kill): it
becomes `interrupted`, is never admitted by a read, and its view says "a
check of 2.1.320 was interrupted: check it again"; only the admin's
explicit check admits it again. Bounds: at most 64 `checked` records, the
oldest dropped (a dropped version is only asked to be checked again); the
`checking`, `turned` and `interrupted` records are never dropped, at most
64 of them, and once that is full a check of a version not recorded is
refused (`versions_full`, with words) rather than forgetting a
prohibition.

**Export.** C6's table of what an archive leaves out and a restore
refuses (`internal/export/leftout.go`, which names each lock file by its
path) gains `capacity/owner.lock` (`WhyLock`) and `capacity/run`, the
readers' working folder (`WhyTemporary`), in PR-3, which makes both;
`capacity/accounts.json` and `capacity/versions.json` are exported and
restored as the plain files they are. These four are every path this item
adds under the data directory.

**Reservations.** A launch that resolves an account takes a reservation
on its id (`Reserve(id)`, under `accountsMu`), which `Bound` counts, and
lets it go only once the session is registered or the run's record (the
ledger's first row, or a legacy run's record) holds the binding, and on
every failure before that (a deferred release). A launch paused between
its resolution and its registration therefore holds `Remove` off.

**Readings** (`reading.go`): a `Reading` is the report of one read, for
one agent (a catalog agent id) and one account id, with the agent's
probed version, the reader's qualified version and the time read. A
`View` is what a page and the CLI show:

```go
type View struct {
	Agent   ViewAgent   `json:"agent"`   // id, name, adapter, version
	Model   ViewModel   `json:"model"`   // chosen ("" for the agent's default), and the words shown
	Account ViewAccount `json:"account"` // id, label, default, retired, plan
	Host    ViewHost    `json:"host"`    // native, the server's name; undetermined; or remote
	State   string      `json:"state"`   // ok | reading | unsupported | not_installed | signed_out | key_paid | unqualified | remote | env_set | unlisted | not_owner | error | never_read
	Words   string      `json:"words,omitempty"`
	Windows []Window    `json:"windows"` // source "provider"
	Credits []Credit    `json:"credits"` // source "provider"
	Limits  Limits      `json:"limits"`  // source "provider": reached, spend control, ordinary use allowed; absent when not reported
	Counts  Counts      `json:"counts"`  // source "conductor": running, started in 5 h, started in 7 days, since
	ReadAt  *time.Time  `json:"readAt,omitempty"`
	ReadBy  *ReadBy     `json:"readBy,omitempty"` // the program, its version, the qualified version
	Fresh   string      `json:"fresh"`            // fresh | stale | reset_passed | none
	NextRead *time.Time `json:"nextReadAfter,omitempty"`
}
```

A window carries `source: "provider"`; the counts carry `source:
"conductor"` and `estimate: true`, and live in their own field: no code
adds, compares or merges a count with a provider's figure, and no figure
is money Conductor computed. A missing window is absent from `windows`;
the JSON has no `percent` for it.

**The cache** (`cache.go`): readings per (agent, account) key, kept in
memory only, the last 8 per key and at most 128 keys (the least recently
asked dropped), so a restart starts at `never_read`. One read at a time
per key (askers of a read under way join it) and at most 2 across the
server; an asker waits at most 2 s and otherwise gets the last reading
with `reading`. A key is read at most once in five minutes (`MinInterval`)
whoever asks. The time of each key's last read is kept apart from the
readings, in a cooldown table whose entries leave only once their five
minutes have passed, so a key dropped from the readings is still not
read again early; the table holds at most 1,024 keys, and a read asked
while it is full of unexpired entries is not made (the view answers
`nextReadAfter`, the soonest entry's end). A reading is `fresh` for 15
minutes, then `stale`, and `reset_passed` once a window's reset is
behind the clock, whatever its age. The cache reads only when `Read` is
called (`View` answers from what is kept); there is no background poll.
It
calls `OnRead(key)` after a reading is kept, which the API turns into the
`capacity` event. The clock and the reader are injected.

### `internal/api` and `internal/cli`: the routes, the event, the commands (PR-3)

`internal/api/accounts.go` and `internal/api/capacity.go`, each route the
admin's and wrapped in `launching` (a switchyard answers `403
switchyard`, as every launch route does):

| Route | Body or query | Answer |
|---|---|---|
| `GET /api/accounts` | — | the document, the implicit `default` of each adapter with a `ConfigDirEnv`, the defaults, each account's last reading's state and time; at most 64 KiB |
| `POST /api/accounts` | `{adapter, label, folder, expectedRev}` | `201` the account and the new rev; `400 invalid_account` with the field; `409 stale_rev` |
| `PATCH /api/accounts/{id}` | `{label, expectedRev}` | the account; a folder in the body is refused |
| `POST /api/accounts/{id}/retire` | `{expectedRev}` | the account, retired |
| `DELETE /api/accounts/{id}?expectedRev=n` | — | `204`; `409 account_in_use` naming at most 10 sessions and runs bound to it |
| `PUT /api/accounts/defaults/{adapter}` | `{account, expectedRev}` | the defaults; a retired account refused |
| `GET /api/capacity` | `agent`, `account` (default: the agent's default), `model`, `launch` (`session`, the default, or `managed`), `yolo` | one `View`, from what is kept, reading nothing; at most 32 KiB |
| `GET /api/capacity` | `after`, `limit` (at most 16) | every installed agent's view for each of its accounts, ordered by agent and account, from what is kept, reading nothing: a page holds views until the next would pass 128 KiB (at least one), and `next` names the key to continue after; at most 128 KiB |
| `POST /api/capacity/read` | `{agent, account, check}` | `202` the view, `reading` or answered within the 2 s wait; a read inside `MinInterval` answers the kept one with `nextReadAfter`; `check` (Claude Code only) runs a version newer than `Qualified` once (Qualification) |

Only `POST /api/capacity/read` starts a read, and only the admin's: the
page's "Read now", the read the dialog and the crew sheet ask as they
open, and `conductor capacity --read`. Every `GET`, the principal
`brakeman`'s among them, answers from what the cache keeps.

The `View`'s host follows the launch the query names, since the Runtime
is chosen per managed run when the run is made (`Takes`): a session and
a legacy crew's member always run natively (`native` and the server's
name); so does a managed run in which the binding this agent and
account would get is not `own` (a registered account, an entry pinned
to a folder, a launch's own login variables: PR-4, Native until F); one
in which it would be `own` is `native` when no
Runtime other than the native one is registered, and otherwise
`undetermined`, with the server's reading shown under "this server's
login: if <Runtime> takes the run, its usage there is unknown", never as
the run's. A session on another computer is `remote` with "read where it
runs: not on this server". The counts come from the session
registry (sessions running bound to the key, and starts in the last 5
hours and 7 days, kept as at most 512 start times per key in memory).
`Facts` is the server: the catalog, the adapters, `checkAccountDir`, and
`Bound`. In PR-3 nothing can bind an account yet (the bindings are
PR-4's), so every session and run is on `default`: `Bound` answers the
reservations alone (none), the counts count the agent's sessions under
`default`, and `Remove` is never refused for a binding; PR-4 makes
`Bound` and the counts read the bindings and carries the tests that
need them. The readers are reached through a server option the tests
replace (`usageReaders`), never by the cache running a program itself.

The **`capacity` event** on `/api/events` names the key and the reading's
state and time, `{agent, account, state, readAt}`, at most 512 bytes,
sent when a reading lands; a page reads the view again on it.
`docs/protocol.md` gains the routes, the event and its bound.

The guide's operations table (`guide.go`) gains the routes, admin
scope, with their input schemas; no MCP name, so `tools/list` is
unchanged.

`internal/cli`: `conductor accounts` (`list`, `add --agent <adapter>
--label <words> --folder <dir>`, `rename <id> <words>`, `retire <id>`,
`remove <id>`, `default <adapter> <id>`) and `conductor capacity
[--agent <id>] [--account <id>] [--model <name>] [--read [--check]]
[--json]`, the
text form printing every state in words and an absent window as "not
reported"; usage and completion text.

### `internal/api`, `internal/session`, `internal/crew`: a launch bound to its account and model (PR-4)

**Sessions.** `createSessionRequest` gains `account` (an account id;
empty for the agent's default) and `model` (empty for the agent's
default). The handler resolves the account once, before the process
starts: empty takes the adapter's default at this moment; a retired or
unknown account is refused (`400 invalid_account`); an account with a
folder sets `ConfigDirEnv=<folder>` in the session's environment, as a
Brakeman turn's does, and the launch installs the Conductor skill under
it (`InstallSkillIn`, as for Brakeman). An agent whose environment as
built for its launch (`agentEnv`: the entry's own env, or the server's
or the entry's passthrough) already sets its adapter's `ConfigDirEnv` (a
copy of an agent under a configuration of its own, #291, or a server
that passes `CLAUDE_CONFIG_DIR` through) has its login pinned: it takes
the `default` account only (another is refused, naming where the login
is set), and its view says the login is set there. A launch whose own
additions (yolo's env, approved names, the request's env) set or pass
the `ConfigDirEnv` takes `default` only too.

**A binding names the login, not only the account.** `BoundAccount` is
`{ID, Label, Folder, Source, Login}`, and `Source` says where the login
the process will use comes from: `account` (a registered account, its
folder), `own` (`default` with nothing setting the folder: the tool's own
login, `Folder` empty), `pinned` (`default` on an entry whose built
environment sets the folder: `Folder` is that folder), `unlisted`
(Brakeman's folder not in Accounts, below) or `launch_env` (the launch's
own additions set the folder or one of the adapter's `AuthEnv`
variables: no folder recorded, since those values may be a key).
`Login` is a fingerprint of the authentication context the process
gets: a SHA-256, under the fixed prefix `conductor-login/1`, of the
adapter, then the value of its `ConfigDirEnv` and of each of its
`AuthEnv` variables (or a mark that it is unset) in a fixed order, read
from the whole environment the process is started with (the entry's,
the server's passthrough and the launch's own additions alike, yolo's
included). Only the hash is kept, beside the folder and, like it, in no
reply; the values, a key among them, never are. The **effective login**
of a binding is its adapter, its `Folder` (`own` meaning the tool's
default) and its `Login`; two bindings are the same login only when all
three match, so two catalog entries that share a folder but carry
different keys are two logins. Every later use of a binding (an attempt,
a resume, a continuation, a member started later) builds the whole
environment it would start with again and refuses `account_gone`, with
words, when the effective login it would get differs from the bound
one: a registered account removed or naming another folder, an `own`
entry that now pins a folder, a `pinned` entry that now pins another or
none, a yolo recipe whose environment now sets another folder or key, a
key that is now another. This holds for every `Source`, `launch_env`
included: no use is compared on its `Source` alone. The invariant:
every process started under a binding gets an environment whose
fingerprint is the binding's `Login`, compared before it starts.
**Brakeman's turn** keeps its
configuration's `account` as a folder (`docs/plans/brakeman.md` owns it)
and is bound from it: an empty `account` is `default` (the tool's own
login, as today, never the adapter's registry default); a folder that a
registered account of the adapter holds binds that account's id; any
other folder binds as `unlisted` (`{ID: "unlisted", Folder, Source:
"unlisted"}`), whose view
says "a folder not in Accounts: add it to read its usage", so the
process's environment, the binding and the view always name the same
folder. A server that does not own the accounts document (One owner,
above) refuses a launch whose account resolves to a registered account.
The resolution takes a reservation (above) released once the
session is registered or the launch failed. A
model goes through the adapter's `ModelArgs` after the launch's own
arguments, held to the pattern Brakeman's configuration and
`ext.TurnLaunch` hold a model to (`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,99}$`,
at most 100 bytes); an adapter without `ModelArgs` refuses a model, and
arguments that already carry the model flag beside a model are refused
("set the model in one place"). `session.Launched` gains `Account
BoundAccount` (above) and `Model`; the admin's `launch`
block of `GET /api/sessions/{id}` shows the id, the label and the model,
never the folder to anyone but the admin and never to a link's viewer,
whose `session` info is unchanged. **Resume** (`resume.go`) launches from
`Launched` as today, so it keeps the account and model the session was
launched with, whatever the default is now; a resume whose effective
login is not the bound one (an account removed since, an id naming
another folder, an entry pinned otherwise) is refused `409
account_gone` with words. A clone takes the source's account and model
as its defaults, which the person may change.

**Crews.** `crew.Member` gains `Account` and `Model`, both optional
(`omitempty`, so a crew saved before reads unchanged), checked by
`Validate` against the same patterns and by the API against the accounts
and the adapters, as `CheckAgents` checks agents. At launch, one function
in the API (`bindMembers`) resolves every member's account on every path
that starts or adds members: `POST /api/crews/{id}/launch`, a ticket's
approved launch (`LaunchApproved`), `ext.Host.Launch`, a Brakeman act
that launches, an agent's own crew (`POST /api/sessions/{id}/crew`,
`handleSelfCrew`, MCP `form_crew`), an agent's added member (`POST
/api/sessions/{id}/run/members`, `handleSelfAddMember`, MCP
`add_member`), the admin's added member (`POST /api/runs/{run}/members`,
`handleAddRunMember`) and a continuation (`ContinueRun`, which copies
the earlier run's bindings and resolves nothing). An agent never picks an
account: on the two self routes a member that names one is refused
(`400 invalid_account`, "an agent does not choose accounts"); the
adopted member (`form_crew`'s `self`, already running) takes the calling
session's own binding from its `Launched`, never today's default. A
member of the calling session's adapter takes the calling session's
effective login, compared, never copied by id: the member's entry is
built (`agentEnv`) and its binding resolved as a launch would resolve it
with the calling session's account; when the effective login that gives
differs from the calling session's (another folder, or the same folder
with another key: the fingerprints differ), the member is refused (`400
invalid_account`, "this agent's entry uses another login than the
session's: <where it is set>"). So a session of an entry pinned to
`/work` (`claude-work`) may add another `claude-work` member, whose
binding is `pinned` to `/work`, but not a plain `claude` member, whose
`default` would be the tool's own login; a session on a registered
account may add a plain member (bound to that account) but not one of an
entry pinned elsewhere; a session whose login its launch's additions set
(`launch_env`) adds no member of its adapter. A member of another adapter
takes that adapter's default at the moment of the request, resolved
against its own entry (an entry pinned there binds `pinned`, as an
admin's launch of it would). The admin's added member takes the account
it names, or the adapter's default at the moment of the add, resolved
the same way. Every member is resolved before the run admits it, and a
member added to start later keeps the binding resolved at its add. The
engine records the crew as launched with each member's `Account`
resolved to a concrete binding (`BoundAccount`, never empty) and its
`Model` as given. That copy is what the ledger keeps already
(`runs.crew`) for a managed run, and the engine's own copy of the crew
for a legacy one, with the legacy run's record once the engine has
forgotten it (below), so every attempt, retry, repair, member added or
started later, resume and continuation reads the account and model from
the run, never from the accounts document's default.

**A legacy run's record keeps its bindings.** A legacy run has no
`runs.crew` row: its record (`internal/crew/record.go`,
`runs/<id>.json`) is the run as `GET /api/runs/{run}` answers it, and
`Engine.ResumeRecord` rebuilds the crew from the record's members once
the engine or the server has forgotten the run. That reply type carries
no folder (below), so the record gains one storage-only field beside the
run: the file is the `Run` with one more top-level field, `bound`
(member name to `BoundAccount`, for every member), written by
`Records.Save` from the engine's bound crew at every save, at most 128
entries, inside the record's 4 MiB bound and never trimmed by `fit`.
Every load of a record decodes this storage form (the store refuses an
unknown field), and `Records.Get` and `Records.List` return the public
`Run` alone, as every route and Brakeman's reads use it; a new `Records.Bound(runID)` returns
the bindings, and only `ResumeRecord` reads it: it sets each member's
`Account` from them and compares each member's effective login as every
later use does (`account_gone` when it differs, never moved). A record
from before this item has no `bound`: its members resume as a launch of
`default` resolves at the resume, which is what `main` resumes. The
invariant: every place a run is kept so it can be started again (the
ledger's `runs.crew`, the engine's copy, a legacy record) keeps each
member's whole binding, and every reply made from those places is made
from a type with no binding field.

**A binding's folder stays out of every run reply.** `MemberState` is
also the run as routes return it: the admin's run routes, the session
token's self routes (`handleSelfCrew`, `handleSelfAddMember` and
`handleSelfRun`, `internal/api/selfservice.go`, which return the run
whole) and Brakeman's run reads (`filterRun`, `filterRuns`, which keep
the members' fields). So the folder is never in it: `MemberState` gains
`Account` as `{id, label, source}` and `Model`, a reply type with no
folder field, and the folder and the fingerprint live only in the run's
bound crew (the ledger's `runs.crew`, the engine's copy and a legacy
record's `bound`), which no route marshals, and in `session.Launched`. The saved crew a self route returns carries
account ids only, as every saved crew does; a `BoundAccount` in any
request body (a crew, a member, an approved revision's body) is refused,
since only the engine binds. `LaunchSpec` gains `Account
BoundAccount` and `Model`; `Server.Launch` sets the environment and the
arguments from them. A run whose member's effective login is no longer
the bound one (its account removed, its id naming another folder, its
entry pinned otherwise) refuses an attempt with words: the attempt fails at its launch (`account_gone`)
before any process exists, and is never launched under another login.
**Native until F.** A login that depends on this server (the folder a
registered account, a pinned entry or Brakeman's unlisted folder names,
or the login variables a launch's own additions set or pass) reaches the
process as a path or a value on this server, which a guest would not
have, and handing a login into a guest is item F's decision, not this
one's. So eligibility is read from the effective binding, never from
the id: a managed run is offered to a Runtime only when every member's
binding has `Source` `own` (the tool's own login, with nothing setting
its folder or a login variable at the launch); a run with any member
bound `account`, `pinned`, `unlisted` or `launch_env` (a `pinned` one
has the id `default`) is offered to no Runtime (the engine's
`runtimeFor` asks no `Takes` for it, as for a legacy crew, from the
members' bound `Source` alone; `internal/crew` reads no capacity) and
runs natively until item F defines how a Runtime receives a login. The
Runtime is chosen once, when the run is made, so a member added later
cannot move the run: a run a Runtime took admits an added member only
when its binding is `own`, and refuses another (`400 invalid_account`,
"this run's Runtime cannot take a login from this server"). The
invariant: no process on a Runtime other than the native one starts
under a binding other than `own`. A crew JSON in the
ledger from before this item has no `account`: it reads as `default`,
which is what those runs ran under. A ticket's approved plan revision
whose body names no account launches with the default at that launch,
and the run page says so; one whose body names an account launches with
that account or is refused when it is retired or removed.

### `internal/brakeman` and `internal/api`: Brakeman reads capacity (PR-6)

`brakeman.Visibility` gains `Capacity bool` (`capacity`, in `Classes`):
on in `DefaultConfig`, absent (off) in a configuration saved before, so
Brakeman sees nothing new until the person turns it on. `GET
/api/capacity` joins `brakemanReads` under that class: the principal
`brakeman` gets the view without any account's folder or the server's
name, labels and figures only, scoped as every shared read, and always
what the cache keeps: Brakeman never starts a read (Owner decisions,
5). A turn reaches it through its existing `observe` tool:
`observeWhats` gains `capacity`, and the guide gains an
`observe_capacity` operation (route `GET /api/capacity`, parameters
`agent`, `account`, `launch`, `after` and `limit`, the route's bounds,
and words saying the answer is the last reading kept, never a new one),
so `conductor brakeman observe capacity` and the MCP tool send it as
every other observation. A new ref
kind `capacity:<agent>/<account>@<readAt>` (in `refs.go` and `kinds.go`'s
table) cites a reading; at a brief's submit the API copies into the
ref's resolution (BK2's `br-<n>-refs.json`) a snapshot of every fact the
principal's view of that reading holds, so any fact Brakeman may cite
stays checkable after the reading leaves the cache: the agent, its
version and the reader's qualified version (`readBy`), the account's id
and label, the state and its words (signed out, key-paid, unqualified,
error), the plan, the windows, the credits, the limits
(`ordinaryUsageAllowed`, spend control, the reached type, absent ones
absent), `readAt`, and Conductor's counts with their own time and their
`estimate` mark; never a folder or the server's name. The server makes
the snapshot from the cache, never from the turn's text; it is at most 8
KiB (the view's own bounds keep it there) and counts in BK2's 4 MiB
bound for a brief's resolutions; a cited reading no longer in the cache
at the submit resolves `unavailable`, as BK2's `question:` ref does
once its question has left the window. The guide gains a `capacity` section: what the view holds, that a
missing window is unknown, and the rule for proposals (Owner decisions,
11); the guide's host section gains, per agent, `usage` (`reads`, or
`unknown`) and the qualified version. Brakeman's own request shows its
agent and account's view at the time it is queued.

### `web/app`: Accounts, the capacity panel and the launch dialogs (PR-5, PR-7)

- `composables/useAccounts.ts` (the document, its rev, the writes),
  `composables/useCapacity.ts` (a view per key, one fetch per key at a
  time, read again on the `capacity` event, `read()` respecting
  `nextReadAfter`), `utils/capacity.ts` (window words, the reset in the
  person's zone and as "in 5 days", freshness words, every state's words,
  severity to colour from the brand palette in `app.config.ts`).
- `components/CapacityPanel.vue` (full and compact), Nuxt UI's
  `UProgress` per window, the provider's figures and Conductor's count in
  separate rows with their marks; `components/AccountsCard.vue` in
  `pages/settings.vue`; `components/AccountSelect.vue`.
- `LaunchSessionModal.vue`: Account and Model fields for an adapter that
  takes them, the panel below, when the session runs on this server. For
  "this computer" both fields are hidden (the host's own login is used,
  and a model goes in the pasted command through the existing Extra
  arguments field, as today) and the panel shows its remote state. Launch
  stays enabled whatever the panel says.
- PR-7: `CrewMembersTable.vue` and `CrewEditor.vue` gain each member's
  account and model; `pages/crews/[[id]].vue`'s launch opens
  `CrewLaunchSheet.vue` (each member's compact panel, then Launch);
  `DispatchLaunch.vue` shows the revision's members' lines;
  `CrewRunHeader.vue` shows the bound accounts and models;
  `BrakemanSettings.vue` picks its account from `AccountSelect` (the
  tool's own login writes an empty `account`, an added account writes its
  folder, as the configuration stores it today; a folder not in the list
  shows as "a folder not in Accounts", PR-4's `unlisted`) and gains the
  Capacity switch; `BrakemanRefChip.vue`
  renders a `capacity:` ref.
- The e2e stub (`web/e2e/stub-agent.sh`) answers `auth status --json`,
  `-p /usage …` and `app-server` as the two tools do, from fixtures under
  `web/e2e/fixtures/usage/`, a state chosen by `STUB_USAGE` (`ok`,
  `signed_out`, `key_paid`, `turn`, `slow`, `error`).

### Docs (PR-3, PR-4, PR-6, PR-8)

`docs/protocol.md` (the routes, the launch fields, the event, Brakeman's
class and ref) in the PRs that add them; PR-8: `docs/architecture.md`
(accounts and capacity beside the launch), `docs/repository-map.md` (a row
for `internal/capacity`; `internal/agents`' readers),
`README.md` (accounts and capacity in the features list) and
`docs/features.md` (the entry).

## Events, orderings and invariants

**The accounts document.** One writer: the server holding
`capacity/owner.lock`, and in it the API under one lock (`accountsMu`);
another server over the same directory only reads, refuses writes, and
binds only `default`, so `expectedRev` is a compare-and-swap in the one
process that writes. Every write reads the document, checks
`expectedRev`, validates, bumps `rev` once and saves through
`internal/store`'s atomic write before it answers. An account's folder
never changes, and an id is never minted twice. Retire is a field;
remove adds the id to `Removed` and deletes the record only after
`Bound` found nothing, under `accountsMu`, which a launch also takes to
resolve an account and take its reservation; the reservation lasts until
the session is registered or the run's record holds the binding, so no
launch is between resolution and publication when `Bound` answers
none. The defaults name accounts of
their adapter; a default left naming a retired or removed account reads
as `default`. Migration: none (a new document; a server without it has
every adapter on `default`, which is `main`'s behaviour).

**A reading.** `never_read` → `reading` (a read began for the key) →
one of `ok`, `signed_out`, `key_paid`, `unqualified`, `error`, written
into the cache, then `OnRead`, then the `capacity` event: an event is
never sent for a reading not yet kept, so a page that reads the view on
the event sees it. A reading never goes to disk, never to a session,
never into the ledger or Dispatch, and nothing in `internal/crew` reads
it. `unsupported`, `not_installed`, `remote`, `env_set`, `unlisted` and
`not_owner` are answered without a read.

**A launch's binding.** For a session: resolve the account (under
`accountsMu`, reading the default once) → build the environment and
arguments → start the process → `Launched` holds the binding for the
session's life and its resume. For a crew: `bindMembers` resolves every
member under `accountsMu` → the engine makes the run with the resolved
crew → for a managed run, the run's first ledger row (`CreateRun`) holds
that crew in `runs.crew` before any attempt is reserved → every attempt's
`LaunchSpec` takes its member's account and model from the run. Nothing
after the first row reads the accounts document's defaults for that run.
Each attempt checks that the bound id still names the bound folder, and
refuses `account_gone` otherwise. A continuation copies the earlier
run's crew, bindings included. Every use of a binding after its launch
compares the effective login it would get with the bound one and refuses
`account_gone` when they differ. A member added to a run, by the admin or
by an agent, is resolved (Crews, above) before the run admits it, under
a reservation, and the run's record holds its binding before it can
start. A managed run is offered to a Runtime only when every member's
binding is `own`, and a run a Runtime took admits only `own` members: no
process on a non-native Runtime starts under a login that depends on
this server. Every process started under a binding is compared, before
it starts, with the binding's `Login` fingerprint. A ticket's launch freezes the revision's package as
today; the binding is in the run's crew, never written into the package.

**What never happens.** No reading refuses, delays, pauses or reorders a
launch, an attempt or a check. No code switches a session or a run to
another account, and no agent chooses one. No `GET` starts a read. No reading is shown as zero for a window not reported,
and no estimate is added to a provider's figure. No reader sends a
method or flag outside its fixed list, and none starts a program whose
version it did not find itself at that read. No run reply, to anyone,
carries a bound folder or a login's fingerprint. No `turned` version is
forgotten, no check becomes `checked` without a run seen answering with
no turn, and no server without the owner lock starts Claude Code.

## Out of scope

- Reading on a schedule, in the background or across a restart; a history
  of readings or a chart of use over time.
- Readers for Copilot, agy, goose, Cursor, OpenCode, pi, omp, aider, Amp
  and dsh: Copilot, agy and goose were read and document no way to read
  usage before a launch without a model turn; the others were not
  installed on the planning machine, and PR-1 records what the help of
  OpenCode, aider, Amp and pi lists. Each stays unknown until a reading
  of its tool finds such a way; each such reader is a later small PR with
  its fixture and recipes check.
- Reading usage on another computer: a session a person runs themselves
  (`conductor host`, "this computer") and a launch on a non-native
  Runtime (Program item F) show unknown; reporting it from there would
  need a host message and is not planned here.
- Any billing: Conductor shows no bill, computes no money, keeps no
  prices and never names a provider's credit as a charge.
- Gating, delaying, pausing, throttling or rerouting work on capacity;
  rotating accounts; any automatic action on a limit. Brakeman proposes,
  a person acts through the existing controls.
- Reading a limit message from an agent's terminal in the middle of a
  run.
- Rebinding a running session or run to another account or model: stop
  it and launch again.
- Listing a provider's models, checking a model exists, or reading the
  agent's configured default model.
- Logging in, logging out or holding any credential; reading anything in
  an account's folder; keeping a login's email, organisation or ids.
- A capacity provider in `ext`, a hosted capacity service, or a
  Workspace view of several people's accounts (paid, and an amendment
  first).

## Tests per surface

Every Go package's tests run under `go test -race -count=1 -timeout 30m
./...` with `make lint` and `make lint-static`; the web PRs run `npm
--prefix web run typecheck`, `npm --prefix web test` and `make test-e2e`
(the new spec alone first). No test waits on a clock: the cache's clock,
the readers' timeouts and the stub's answers are seams.

`internal/agents` (PR-1): `TestClaudeUsageReadsReport` (the 2.1.318
fixture: two weekly windows, extra usage off, plan from `auth status`),
`TestClaudeUsageKeepsNoText` (no byte of the fixture's people's text, and
no email or organisation from `auth status`, in the report's JSON),
`TestClaudeUsageRefusesModelTurn` (`num_turns` 1: `unqualified` with
`Outcome` `Turned`, and the fake admission's `Done` called once with
`Turned`), `TestClaudeUsageInconclusive` (a run killed at its shortened
timeout once its pid file is written, one whose output ends before the
result line, one whose child exits before it, a malformed line, and an
output past its bound: each calls `Done` once with `Inconclusive`, never
`Local`, and answers `error`; only a result line with `num_turns` 0 and
`local_command` `usage` gives `Local`),
`TestClaudeUsageNewerVersionNotStarted` (a version past `Qualified`: a
nil `Admit`, or one that refuses, starts no process and answers
`unqualified`; an admitting fake: one run, `Done(Local)`; the
`Qualified` version too is started only once `Admit` answers),
`TestUsageVersionFromProgram` (a fake program on disk at a path whose
identity probe is cached as `Qualified`, replaced at the same path by one
that prints a newer version: the read runs `--version` on the new file,
asks `Admit`, and with none starts nothing; the file replaced between
the two identity notes through a seam (`afterVersion`): `unqualified`,
nothing started; a `--version` that prints no version: `unqualified`;
the same for the Codex reader's `MinVersion`),
`TestUsageWrappedCommand` (a catalog command `[<bash>, <script>]`, the
e2e catalog's shape, whose script answers `--version` as Claude Code
2.1.318: the version checked is the script's, not Bash's; the read's
argv is the resolved Bash, the resolved script, then the reader's own
arguments; the script replaced between the two notes through
`afterVersion`: `unqualified`, nothing started; a wrapper that prints
its own version: `unqualified` with "does not answer as Claude Code";
the same for Codex's `app-server` through a wrapper),
`TestClaudeUsageSignedOut` (`loggedIn` false: `/usage` never run),
`TestClaudeUsageKeyPaid` (an `authMethod` that is a key and a provider
that is not Anthropic's: `key_paid`, and `/usage` never run; an init line
with `apiKeySource` other than `none`: `key_paid`, no window kept),
`TestClaudeUsageArgv` (the exact argv with `--tools ""`, the directory,
`CLAUDE_CONFIG_DIR` from the input, no `CONDUCTOR_*` variable), `TestCodexUsageExchange` (a helper process speaking the
protocol, run by the test binary: the methods arrive exactly as
`codexUsageMethods`, in order, and the process group is ended),
`TestCodexUsageMethodsClosed` (the constant is the four entries and
none is `config/read`), `TestCodexUsageWindows` (10,080 and 300 minutes
named, a null secondary absent, `accountId` and `email` never in the
report), `TestCodexUsageLimitsWithoutWindows` (a refusal with
`rateLimitReachedType`, `spendControlReached` and `ordinaryUsageAllowed`
false and no window or credit: `Limits` holds all three; absent ones are
nil, not false), `TestCodexUsageKeyPaid`, `TestCodexUsageSignedOut`,
`TestUsageBounds` (a line past 1 MiB and output past 4 MiB end the read
with `error` and the group killed), `TestUsageTimeout` (the helper writes
its pid file, the test waits for it, then for the kill, with the
timeout variable shortened), `TestUsageReadersDeclared` (only `claude`
and `codex` have a reader, each with a qualified version). The `recipes`
tier (`usage_recipes_test.go`, `make test-recipes`, every test named
`TestRecipe…` so the target's `-run 'Recipe|TheCLI'` selects it):
`TestRecipeCodexUsageSchema` (`app-server generate-json-schema` written
to a temporary folder holds both methods and every field the reader
reads; runs signed out, nightly), `TestRecipeClaudeUsageFlags` (every
flag the reader passes is in `--help`; `auth status --json` answers
`loggedIn` and the three other fields; runs signed out, nightly),
`TestRecipeClaudeUsageRead` and `TestRecipeCodexUsageRead` (the reader
against the installed tool's login, skipped with its reason when the
tool is absent or signed out, so they run on a logged-in machine only:
the by-hand check of each version `Qualified` is moved to).

`internal/capacity` (PR-2): `TestAccountsValidate` (each bound, the id
pattern, `default` refused as an id, an adapter without `ConfigDirEnv`
refused, a folder `Facts` refuses), `TestAccountsExpectedRev`,
`TestAccountFolderFixed`, `TestRetireKeepsRecord`,
`TestRemoveRefusedWhileBound`, `TestDefaultsReadDefaultWhenRetired`,
`TestAccountsDocumentBounded`, `TestCacheOneReadPerKey` (two askers, one
read, the fake reader held at a gate until both asked),
`TestCacheTwoAtOnce`, `TestCacheMinInterval` and `TestCacheFreshness`
(the fake clock: fresh, stale, a reset passed), `TestCacheKeepsEight`,
`TestCacheDropsLeastAsked`, `TestCooldownOutlivesEviction` (129 keys
read, the first dropped from the readings, read again at once: no
read, `nextReadAfter` its five minutes; the fake clock past them: one
read), `TestCooldownTableFull` (1,024 unexpired entries: a new key's read
is not made), `TestViewReadsNothing` (`View` on a key never read: the
fake reader's count stays 0), `TestAccountIDsNeverReissued` (an id
removed is in `Removed` and the mint refuses it; ids are the server's,
never the label's), `TestVersionsAdmitOnce` (two accounts ask a check of one version at
once, the first admission held at a gate: the `checking` record is on
disk before `Admit` returns, the second ask is refused
`check_under_way`; `Done(Local)`: `checked`, admitted for every read
after), `TestVersionsInconclusiveCheck` (a check ended
`Done(Inconclusive)`: `interrupted` on disk before the read answers; the
next read with no check is refused and starts nothing; after the
document is opened again still `interrupted`; the admin's check admits
it once; an `Inconclusive` read of a `checked` version leaves it
`checked`, and a `Turned` one of the `Qualified` version records it
`turned`), `TestVersionsInterrupted` (a `checking` record on disk at open:
`interrupted`, refused to a read, admitted to the admin's check),
`TestVersionsSurviveRestart` (`checked` and `turned` read back after the
document is opened again; a `turned` version refused even to a check),
`TestVersionsTurnedNeverDropped` (65 `checked`: the oldest dropped;
`turned` records never dropped; 64 kept records: a check of a new
version refused `versions_full`),
`TestReservationHoldsRemove` (a reservation taken:
`Remove` refused; released: allowed), `TestOnReadAfterKept` (the hook sees the
reading in the cache), `TestMissingWindowIsAbsent` (the view's JSON has
no window and no `percent` for one not reported),
`TestCountsApartFromProvider` (counts only under `counts`, marked
`estimate`), `TestEveryStateHasWords`.

`internal/api` (PR-3): `TestAccountsRoutesAdminOnly` (a session token and
a link refused; a switchyard `403 switchyard`), `TestAccountsWrites`
(each route, `stale_rev`, a folder in `PATCH` refused, an id in `POST`
refused), `TestAccountsNotOwner` (two servers over one data directory:
the second's `GET` says `owner: false`, its writes are refused
`accounts_not_owner`, and the first's write is the one kept),
`TestCapacityGetStartsNoRead` (every `GET`, the single view, a page of
the listing: the fake reader's count stays 0; only `POST
/api/capacity/read` reads), `TestCapacityListingPaged` (the most
accounts, the most windows and the longest strings the bounds allow,
over several catalog entries: every page within 128 KiB, `next` walks
every view once), `TestCapacityHostByLaunch` (a session and a legacy
member `native`; a managed run on `default` with a registered Runtime
`undetermined`, with none `native`; a managed run of an entry pinned to
a folder `native` with a Runtime registered), `TestUsageEnvMatchesLaunch` (a
server whose passthrough names `CLAUDE_CONFIG_DIR` and `https_proxy`:
the reader's environment holds both as the session's would, and the
`default` account reads the folder the session would get; a launch
whose yolo env sets `ANTHROPIC_API_KEY` is `env_set`),
`TestCapacityViewStates` (unsupported, not installed, never read,
remote, `env_set`, `not_owner` for Claude Code on a server without the
lock, a changed environment `stale`), `TestCapacityReadThenEvent` (the
fake reader answers once the test opens its gate; the test waits for the
`capacity` event and reads the view: `ok`), `TestCapacityEventBounded`,
`TestCapacityRepliesBounded`, `TestCapacityMinInterval`,
`TestGuideListsCapacityRoutes` (the rows; the tools golden unchanged),
`TestEngineImportsNoCapacity` (with `go/parser`: no file of
`internal/crew` or `internal/crew/ledger` imports `internal/capacity`),
`TestCapacityTurnedSurvivesRestart` (a fake program that answers with a
turn on a check: the version recorded `turned`; a new server over the
same data directory: neither a read nor a check of it starts the
program), `TestCapacityInconclusiveCheckSurvivesRestart` (checks of
three new versions, the first killed at the shortened timeout, the
second's output ending before its result line, the third's past the
output bound: each recorded `interrupted`; the dialog's next read of
each starts no program; a new server over the same data directory: the
same), `TestCapacityCheckNotOwner` (two servers over one data
directory; a version the first checked, whose fake program now answers
with a turn: the second server's read of it, of the `Qualified` version
and its check start no Claude Code program, answering `not_owner` and
`409 accounts_not_owner`; the first's read records it `turned`; neither
server, nor a third opened after both end, starts it again; a Codex read
on the second server is made). `internal/export`
(PR-3): `TestLeftOutCapacity` (an export holds `capacity/accounts.json`
and `capacity/versions.json` and neither `capacity/owner.lock` nor
anything under `capacity/run`; a restore of an archive holding either is
refused, as for `runs/owner.lock`). `internal/cli` (PR-3): `TestAccountsCommand`, `TestCapacityCommand` (the
text says "not reported" and each state in words; `--json` is the view).

PR-4: `internal/api` `TestRemoveAccountInUse` (a running session, a
managed run `running` and one `interrupted` named; a `stopped` one no
bar, and its continuation refused `account_gone`),
`TestRemoveWhileLaunchHeld` (a session launch held at its `launchHook`
between resolution and registration: `Remove` refused; the launch failed
at the hook: the reservation released), `TestRemovedAndReaddedRefused`
(remove an account, add one with the same label and another folder:
the old run's continuation and the old session's resume are refused
`account_gone`), `TestSelfCrewRefusesAccount` (`form_crew` and
`add_member` naming an account: `invalid_account`),
`TestSelfCrewAdoptsSessionBinding` (the default changed after the
session launched: `form_crew`'s `self` keeps the session's account, a
member of its adapter takes it too, a member of another adapter takes
that adapter's default), `TestSelfCrewEffectiveLogin` (two catalog
entries of `claude`, a plain one and one whose env pins
`CLAUDE_CONFIG_DIR` to a second folder: a session of the pinned entry
adds a pinned member (bound `pinned` to that folder) and is refused a
plain one; a session of the plain entry on a registered account adds a
plain member (that account) and is refused the pinned one; a session
whose launch set the login adds no `claude` member; on `form_crew` and
`add_member` both), `TestBindingLoginChanged` (the pinned entry's
folder changed in the catalog after the launch: the resume and the run's
next attempt refused `account_gone`; a session of a yolo recipe whose
env sets `CLAUDE_CONFIG_DIR=/A`, the recipe changed to `/B`: the resume
refused, and the same with its `ANTHROPIC_API_KEY` changed; two entries
sharing one folder with different `ANTHROPIC_API_KEY`: a session of one
adds no member of the other; a `launch_env` session's resume with its
additions changed refused), `TestBindingFingerprint` (the same variables
give the same `Login`; a changed key, folder or base URL another; no
value of any variable in the binding's JSON), `TestLegacyRecordKeepsBindings`
(a legacy run with a member on a registered account and one `pinned`,
ended, forgotten by the engine, then a new server over the same data
directory: the record's `bound` reads back whole; `ResumeRecord` with
the pinned entry unchanged resumes with the bound folder, read from the
stub's transcript; with the pinned entry's folder changed in the
catalog, that member refused `account_gone`; a record without `bound`
resumes on `default`), `TestRunRepliesCarryNoFolder` (a
run with a member on a registered account and one `pinned`: no folder
byte in `GET /api/runs`, `GET /api/runs/{run}`, the replies of
`form_crew`, `add_member` and `GET /api/sessions/{id}/run`, a run
answered from its legacy record, or Brakeman's `observe runs` and
`observe run`, with its Capacity class off and on, and no `bound` field
and no fingerprint in any; a `BoundAccount` in a crew body refused), `TestAddMemberBindsDefaultNow` (the admin's
add with no account: the default at the add, kept when the member starts
later after the default changed), `TestLaunchPinnedByPassthrough` (a
passthrough `CLAUDE_CONFIG_DIR`: another account refused, `default`
taken), `TestBrakemanTurnBinding` (an empty account `default`, a
registered folder its id, another folder `unlisted`: the process's
environment, the binding and the view name the same folder),
`TestNotOwnerBindsDefaultOnly`, `TestBoundRunNotOffered` (managed runs
with a member on a registered account, a member of an entry pinned to a
folder (bound `pinned` under the id `default`), a member whose yolo env
sets `CLAUDE_CONFIG_DIR` (`launch_env`): the registered Runtime's
`Takes` is never asked for any; a run whose members all bind `own` is
offered as today; to a run the Runtime took, a member added with a
`pinned` binding is refused and an `own` one admitted),
`TestLaunchBindsAccount` (the session's environment
has the folder; `Launched` the binding; the `launch` block has no folder
for a link), `TestLaunchResolvesDefaultOnce`, `TestLaunchRefusesRetired`,
`TestLaunchModel` (the adapter's flag; a model for an adapter without
one refused; a model beside the flag in arguments refused),
`TestLaunchPinnedEntry`, `TestResumeKeepsAccount` (the default changed,
the resume has the first folder), `TestResumeAccountGone`,
`TestApprovedLaunchBindsAccounts` (a revision naming an account, one
naming none), `TestHostLaunchBindsAccounts` (`ext.Host.Launch`);
`internal/session` `TestLaunchedCarriesAccount`; `internal/crew`
`TestMemberAccountValidate`, `TestRunRecordsBoundCrew` (the ledger's
`runs.crew` holds resolved ids before the first attempt's reservation),
`TestDefaultChangeMovesNoAttempt` (attempt 1 bound to A; the default set
to B; a retry: attempt 2's `LaunchSpec` has A, waited for through the
ledger's `attemptWhen`), `TestRepairKeepsAccount`,
`TestContinuationKeepsAccounts` (a restart, a continuation, its first
attempt on A), `TestLegacyMemberStartedLaterKeepsAccount`,
`TestOldCrewReadsDefault` (a stored crew without `account`),
`TestAttemptAccountGone` (the account removed under a run: the attempt
fails with words, no launch under another login).

PR-6: `internal/brakeman` `TestVisibilityCapacityDefaults` (on for a new
configuration, off for a saved one without the field),
`TestCapacityRefKind`; `internal/api` `TestBrakemanReadsCapacity` (the
class required; no folder and no server name in the principal's reply;
the fake reader's count stays 0), `TestGuideObserveCapacity` (the
`observe_capacity` entry, `capacity` in `observeWhats`); `internal/cli`
`TestBrakemanObserveCapacity` (`conductor brakeman observe capacity`
under a turn's token reaches the route and prints the kept view),
`TestBriefCopiesCapacityRef` (the reading dropped from the cache after
the submit; the brief's ref still has its windows, plan, `readBy` and
counts), `TestBriefCapacityRefWithoutWindows` (a Codex reading with
`ordinaryUsageAllowed` false and no window, and one with credits only,
and a `signed_out` one: after their eviction each ref still holds its
limits, its credits, its state and words; no folder in any), `TestGuideCapacity`
(the section and the per-agent `usage`).

PR-8: `internal/api` `TestCapacityWarningWakesRole` (a reading at warning
fires one wake; a second inside the batch window coalesces with a count;
a reading at normal fires none; Brakeman's own reading wakes nothing).

Web (PR-5): vitest `utils/capacity.test.ts` (window words, resets in a
fixed zone, freshness, every state's words, an absent window never drawn,
the estimate row apart), `composables/useCapacity.test.ts` (one fetch per
key, a refetch on the event, `nextReadAfter` respected),
`composables/useAccounts.test.ts`; Playwright `capacity.spec.ts`: add an
account in Settings with a folder under the test HOME, set it default,
retire it; the session dialog for the Claude Code stub shows two windows
with resets, the plan, the estimate row and "Read … by"; the stub's
`signed_out`, `key_paid`, `turn` and `error` each show their words; an
agent without a reader shows unknown; launch, change the default, and the
session's launch block and its resume keep the first account (the stub's
transcript names the folder it was given); with "this computer" chosen
the Account and Model fields are not shown and the pasted command
carries no account. PR-7: vitest
`CrewLaunchSheet.test.ts`; Playwright `capacity-crew.spec.ts`: a managed
crew of a Claude Code and a Codex member, the launch sheet's two lines,
the default changed during the run and a retried attempt still on the
first account (read from the stub's transcript by attempt id), a
ticket's approved launch showing the revision's accounts, Brakeman's
account chosen from the list.

By hand (PR-8, the owner's machine, the real tools): Claude Code and
Codex each read with the default login and with a second folder; the
windows and resets equal what Claude Code's `/usage` and Codex's
`/status` show in their own terminals at the same minute; a folder never
logged in shows signed out; a key-paid login, where one is at hand,
shows no windows; a session launched on one account and resumed after
the default changed resumes its own conversation (which only its own
folder holds). Tests cannot reach the providers' real answers.

## Boxcar constraints

1. The engine starts an attempt only through `crew.Launcher`: honoured;
   the binding rides `LaunchSpec`, and a read starts no attempt and is no
   part of the engine.
2. `session.Local` drives a `session.Process`: honoured; a binding is an
   environment variable and arguments of the start the Runtime makes,
   and nothing reaches a pid or a tty.
3. An attempt's context is files: honoured; the account is an
   environment variable the session's start sets, the folder the agent's
   own; nothing an attempt needs is only in memory (the binding is in
   `runs.crew`). The folder is a path on this server that a guest would
   not have, and handing a login into a guest is item F's to decide: a
   managed run with any member whose binding is not `own` (a registered
   account, a folder an entry pins under the id `default`, an unlisted
   folder, a launch's own login variables) is offered to no Runtime and
   runs natively until F defines how a Runtime receives a login (PR-4);
   before a launch, a run that could go to a registered
   Runtime shows its host as `undetermined`.
4. The agent talks to the server over the network: not touched; no new
   path between an agent and the server.
5. Checks go through `internal/proc`: not touched; a read is not a check,
   is no evidence of a run, and runs in `internal/agents` beside the
   identity probe, the place the boundary test allows for it.
6. Platform splits stay platform tags, the UI shows a capability: no tag
   is added; capacity is shown from readings, never from a plan or a
   provider registration.
7. Evidence rows gain `where`: not touched; no evidence row is added, and
   a view says where its launch would run from the Runtime's `Where`.
8. `hostagent` is a launch site: not touched, and said so: a hosted
   session's agent uses that computer's own login, which this server
   cannot read, so its view says unknown; nothing here binds a session on
   the server only in a way the host's launch would break.

## Open-core checklist

- [x] Article 1: no licence, plan or entitlement check anywhere in the
  item; a provider's plan type is shown, never branched on.
- [x] Article 2: accounts, readings, bindings, the panel and Brakeman's
  proposals all ship in the open-source build with the person's own
  tools and logins.
- [x] Article 3: nothing is gated; there is no flag a fork could flip.
- [x] Article 4: no control is weakened: capacity refuses no stop and
  gates no launch; the accounts document is exported whole; an approved
  revision's accounts are part of its approval.

## The PRs

Each PR is one issue and one branch cut from `main`, a draft PR at its
first commit, the checks for its surfaces before every push, its body
ticking the open-core checklist (articles 1 to 4) and naming the agent
that made it; each leaves `main` working and green on its own (a package
without its routes is reached by its tests, a route without its page by
the CLI and the tests, a page without its later parts says nothing of
them). Parallel means the two touch no common Go package, no common
directory under `web/`, and no common file under `docs/` or at the root.

What is on `main` and what is not: the adapters' declarations, the
catalog, the identity probe, BK0's capabilities route and guide, BK1's
and BK2's Brakeman, DS2's launches, C10's `Where` and `ext.Host` are; PR-1
to PR-7 build on them alone. BK3's background role is not (PR #655, issue
#536): PR-8 waits for it to merge.

| PR | Title | Contains | Touches | Tests | Depends on | Parallel with |
|---|---|---|---|---|---|---|
| PR-1 | agents: the usage readers of Claude Code and Codex, qualified against their tools | `Adapter.Usage`, `UsageReader`, `UsageInput`, `UsageReport`, the two readers, the version each read finds from the catalog's whole resolved command prefix, `Admit`, `Done` and `Outcome` (with fake admissions; nothing kept), their fixtures (written by hand to the shapes read, no real account's ids or figures), the bounds and timeouts, the recipes checks | `internal/agents` | the PR-1 tests above, the `recipes` tier | none | PR-2 |
| PR-2 | capacity: account references, readings and the cache | `internal/capacity`: `Accounts`, `Account`, `Validate` with `Facts`, the store document, retire and remove, `Versions` (the per-version record, its admission, the three outcomes, recovery and bounds, on disk), `Reading`, `View`, the states and their words, `Cache` with its clock, reader and `OnRead` | `internal/capacity` | the PR-2 tests above | none | PR-1 |
| PR-3 | api, cli: the accounts and capacity routes, the `capacity` event and the commands | `accounts.go`, `capacity.go` (the routes, `Facts` with `Bound` answering reservations alone until PR-4, the owner lock, the readers wired with `agentEnv` and admitted from `Versions`, Claude Code run by the owner alone (`not_owner`), the counts from the registry under `default`), the export table's two rows, the event and its bound, the guide rows, `conductor accounts` and `conductor capacity`, `docs/protocol.md`'s rows | `internal/api`, `internal/export`, `internal/cli`, `docs/protocol.md` | the PR-3 tests above | PR-1, PR-2 | none |
| PR-4 | api, session, crew: a launch bound to its account and model | the session's `account` and `model`, `BoundAccount` and its effective login with the `Login` fingerprint, `Launched`'s binding, a legacy record's `bound` and `Records.Bound`, resume and clone, `Member.Account` and `Model`, `bindMembers` on every launch path (the self routes, compared by effective login, and added members included), the reservations, `Bound` and the counts from the bindings, Brakeman's turn bound from its folder, a run with any binding but `own` offered to no Runtime and admitting no other member, `MemberState`'s account without a folder, `LaunchSpec`, an attempt refused when its account is gone, the guide's launch schemas, `docs/protocol.md`'s launch fields | `internal/api`, `internal/session`, `internal/crew`, `docs/protocol.md` | the PR-4 tests above | PR-3 | none |
| PR-4b | api: whatever a launch runs in an account's folder holds it from before its process starts | the folder a launch runs in, kept in memory beside the binding (which stays PR-4's), every registered account on that folder held from before the process starts by one predicate the remove asks too, the start count and the counts on one account, a home not whole read as the process reads it, no path in the lines about the home | `internal/api`, `internal/capacity`, `internal/agents`, `internal/session`, `docs/protocol.md` | `internal/api`'s folder tests | PR-4 | none |
| PR-5 | web: Accounts in Settings, the capacity panel and the session launch dialog | `useAccounts`, `useCapacity`, `utils/capacity.ts`, `CapacityPanel`, `AccountsCard`, `AccountSelect`, `LaunchSessionModal`'s fields and panel, the stub's readers and fixtures, `capacity.spec.ts` | `web/app/components`, `web/app/composables`, `web/app/utils`, `web/app/pages`, `web/e2e` | vitest and Playwright above | PR-3, PR-4 | PR-6 |
| PR-6 | brakeman, api, cli: Brakeman reads capacity and proposes from it | the `capacity` class, the `capacity:` ref and its copy at submit, `GET /api/capacity` in `brakemanReads`, `observe_capacity` and `capacity` in `observeWhats`, the guide's section and per-agent `usage`, Brakeman's request showing its view, `docs/protocol.md`'s rows | `internal/brakeman`, `internal/api`, `internal/cli`, `docs/protocol.md` | the PR-6 tests above | PR-4 | PR-5 |
| PR-7 | web: crews, ticket launches and Brakeman's account | the members' fields in the crew editor, `CrewLaunchSheet`, `DispatchLaunch`'s lines, the run header, `BrakemanSettings`' account choice and Capacity switch, `BrakemanRefChip`'s `capacity:` ref, `capacity-crew.spec.ts` | `web/app/components`, `web/app/composables`, `web/app/pages`, `web/e2e` | vitest and Playwright above | PR-5, PR-6 | none |
| PR-8 | brakeman, api, docs: a capacity warning wakes the background role; the features entry and the docs | the wake source in BK3's role (a reading at warning or critical, or 90%, outside Brakeman's own runs), its row in the role's log; `docs/architecture.md`, `docs/repository-map.md`, `README.md`, `docs/features.md` with the by-hand record | `internal/api`, `internal/brakeman`, `docs`, `README.md` | the PR-8 test above, the by-hand check | PR-7; waits for BK3's role (PR #655, issue #536) | none |

Sizes, as the owner asked (2026-11-05: a row is a batch, 300 to 800
changed lines, at most about 1,500 with its tests): PR-1 about 650 lines
and 750 of tests; PR-2 about 650 and 700; PR-3 about 750 and 650; PR-4
about 700 and 750; PR-5 about 800 and 550; PR-6 about 450 and 450; PR-7
about 650 and 500; PR-8 about 250 and 250, with the docs.

## Owner decisions

Dated 2026-11-05. Each is a default the owner can overturn with a dated
row in the Program's status log, its convention for a decision the owner
did not make (the Program, section 3.1); the reasoning is why it was
taken.

1. **The open decisions named with this item.** None of O4 (SW1's human
   policy), O5 (the graph's tooling), O8 (pricing) or O10 (Dispatch's
   merge rules) bears on capacity: nothing here is paid, routed or
   replicated. The two member fields ride the plan body as any field does
   under DS1's and DS2's rules.
2. **Two readers first: Claude Code and Codex.** They are the agents
   whose tools say their limits without a model turn, and the two whose
   login folder Conductor can move. Every other agent is unknown with its
   reason (the table in Why and what happens) until its tool documents a
   way; chosen over screen-scraping an interactive status bar, which no
   tool documents as an interface.
3. **Codex through `app-server`, and four methods only.** Marked
   experimental in its help, but its two methods are in the stable
   schema, answer in about 2 s and cost no turn; `codex exec --json`
   reports limits only after a model turn. The fixed method list keeps
   `config/read` out, since a configuration can hold a secret.
4. **Claude Code through `/usage` in print mode, and the structured
   report only.** Sent only for a subscription login (`auth status`
   decides payment first) of a checked version, with no tools; a version
   newer than the one the reader was checked on runs only on the admin's
   explicit check, once; only a run seen answering with no turn makes a
   version checked, one that ends without an answer leaves it
   interrupted, and a turn records that version as turned on disk, never
   run again, Claude Code being run only by the server that holds that
   record; the people's text the command prints is
   dropped unread; `auth status` is decoded into its four
   non-identifying fields. Chosen over reading every newer version on
   its own (a turn could be spent unasked) and over refusing every
   version until a PR moves `Qualified` (Claude Code updates itself
   often, and the panel would be unknown most days).
5. **On demand, in memory.** A read only when the admin asks (the
   page's Read now, the dialog or the crew sheet opening, `conductor
   capacity --read`); every `GET`, Brakeman's included, answers what is
   kept. At most once in five minutes per agent and account (each read
   reaches the provider), fresh for 15 minutes, the readings lost at a
   restart (the versions checked are kept); no background poll. Chosen
   over a poller, which would read for nobody and spend the providers'
   patience.
6. **An account is a reference.** An adapter, a label and the folder the
   agent's own login lives in, fixed once added, under an id the server
   mints and never mints again; retire rather than delete; remove only
   when nothing is bound or being launched; no credential, email,
   organisation or provider id is kept, and nothing in the folder is
   read.
7. **The default is per adapter, and resolved once.** A launch binds a
   concrete account; the binding lives in the run's crew and the
   session's launch, so a changed default moves no running work, and a
   resume or continuation keeps it; an account gone refuses, never
   moves. Chosen per adapter, since a login folder serves every catalog
   entry of that tool.
8. **The model is bound like the account**, through the adapter's own
   flag (Claude Code, Codex), empty for the agent's default, which
   Conductor does not read and shows as "the agent's default".
9. **Estimates are Conductor's counts, never money.** The provider's
   figures are shown as the provider's at the moment of the read; no
   bill, no price, no sum of the two.
10. **Capacity never acts.** It refuses, delays, pauses and reroutes
    nothing; the panel warns at the provider's warning or critical
    severity, or at 90% used, and the launch stays enabled; the engine
    does not import it.
11. **What Brakeman may propose.** Waiting for a named reset, holding or
    pausing named runs or launches, a lower number of members at once,
    or a permitted alternative: another model on the same account, or
    another agent the crew or the person already configured for the
    work. Never another account of the same provider chosen because this
    one is near its limit. Brakeman's Capacity switch is on for a new
    configuration and off for a saved one until the person turns it on.
12. **Elsewhere is unknown, and a bound account stays native.** A
    session on another computer and a launch on a non-native Runtime
    show unknown rather than the server's reading; a managed run any of
    whose members' logins depends on this server (any binding but `own`,
    a pinned folder under the id `default` included) runs natively until
    item F defines how a Runtime receives a login.
13. **The admin sees folders; nobody else does.** Accounts and capacity
    are the admin's routes; a link's viewer sees no account; a run's
    members show an account's id and label, never its folder, on every
    route; Brakeman sees labels and figures, never a folder or the
    server's name.
14. **When the code starts.** Now for PR-1 to PR-7; PR-8 once BK3's role
    is merged. AC1's Status follows the Program's rules: In progress at
    its first PR, In review when the last is ready, Done when the
    boundary below is observed.

No question is left open: each answer above lets the plan stand, and the
owner overturns any of them with one row.

## Acceptance

The boundary, in this plan's words: a launch shows the quota and the
reset of each supported provider, or says it does not know, and a changed
default moves no running work. It is observed, not ticked:

- Once PR-7 is merged, on the owner's `conductor serve` with the owner's
  own Claude Code and Codex logins: the session dialog, for each of the
  two, shows the windows, their share used and their resets, the plan,
  the provider's credit row, Conductor's count marked as an estimate, and
  "Read … ago by" with the tool's version; the same minute, Claude Code's
  `/usage` and Codex's `/status` in their own terminals show the same
  windows and resets. An agent with no reader (Copilot, agy or goose on
  the owner's machine) shows unknown with its reason; a folder never
  logged in shows signed out.
- A managed crew of a Claude Code member and a Codex member, launched
  from a ticket's approved revision, its launch sheet showing both lines;
  during the run the owner adds a second account for one adapter and
  sets it as the default; the run's next attempt and a retried attempt
  are read in the ledger's `runs.crew` and the sessions' launch blocks to
  be on the first account; a new session launched after shows the second.
  A session launched before the change and resumed after resumes its own
  conversation.
- Brakeman, asked for a briefing with the Capacity switch on, cites a
  `capacity:` ref in a fact and proposes nothing that moves work to
  another account.
- The observation, and what was missing, slow or wrong, goes into
  `docs/features.md` (PR-8); a bug the stream caused is fixed in the
  stream, a defect outside it filed.
- Done means: every PR merged and the observation recorded with the
  owner's word that the panel told the truth about both logins; the
  Program's status row moves to Done then. AC1 is part of the R3 gate
  (the Program, section 6).

What needs the owner: the review of this plan and its decisions; cutting
the eight issues; the observation, whose readings come from the owner's
own logins; the merges.
