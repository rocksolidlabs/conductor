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
   answer for a request that names no account or model; the MCP tools
   list is unchanged (no new tool).
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
| Cursor, OpenCode, pi, omp, aider, Amp, dsh | various | not installed on the planning machine | not checked | nothing | — | all of it, said as "Conductor knows no way to read this agent's usage" |

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
	Env     map[string]string // the catalog agent's env, then ConfigDirEnv=<folder> for an account with one
	Dir     string            // <dataDir>/capacity/run, made 0700, empty
}

type UsageReport struct {
	State    string        // ok | signed_out | key_paid | unqualified | error
	Words    string        // why, for every state but ok; at most 300 bytes
	Plan     string        // the subscription or plan type as the tool says it; at most 40 bytes
	Windows  []UsageWindow // at most 16
	Credits  []UsageCredit // at most 4
	ReadAt   time.Time
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

Qualification: a reader is run only for a tool whose probed version (the
identity probe's cache) is at least `MinVersion`, else the view is
`unqualified` with "read only from <MinVersion>"; a tool newer than
`Qualified` is read, and the view says both versions ("read by 2.1.320,
checked on 2.1.318"), so a format that changed shows as `error` with the
field that failed, never as a guess. The nightly `recipes` tier re-reads
the installed tools and fails when a field the reader needs is gone.

**Claude Code's reader** (`usage_claude.go`) runs two commands through
`exec.CommandContext`, as the identity probe does (internal/agents is one
of the places `ext/local`'s boundary test allows a raw start, for the
probe; the readers are the same kind of short, bounded, server-side
question to the agent's own tool): `<command…> auth status --json`, decoded
into a struct of `loggedIn`, `authMethod`, `apiProvider` and
`subscriptionType` alone, so the email, the organisation and the login's
expiry are never held; then, when logged in, `<command…> -p /usage
--output-format stream-json --verbose --no-session-persistence
--setting-sources local --strict-mcp-config --max-turns 1`. From its
lines it keeps the init line's `apiKeySource`, the `usage_report`'s
`rate_limits` (never its `session` part, never the message text, which
is the `/usage` command's words for people and names the person's
folders), and the result line's `num_turns` and `local_command`. A result
with `num_turns` above 0 or another `local_command` means this version
answered `/usage` with a model turn: the report is `unqualified`, and the
reader is not run again for that version until the server restarts (a
`turned` set keyed by the probed version). `apiKeySource` other than
`none`, or an `authMethod` that is a key, is `key_paid`: the provider
reports no windows for it. `loggedIn` false is `signed_out`.

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
when present; `rateLimitReachedType` and `spendControlReached` become the
window's severity (`critical`) and a credit's words; `accountId` is never
kept. `type` `apiKey` is `key_paid`; `account` null with
`requiresOpenaiAuth` is `signed_out`.

Both readers run with the probe's spartan environment (`probeEnv`: PATH,
HOME, LANG, TMPDIR, the XDG directories, `TERM=dumb`, `NO_COLOR=1`), the
catalog agent's own env, and the account's folder as `ConfigDirEnv`, in
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
}

type Account struct {
	ID        string    `json:"id"`      // ^[a-z0-9][a-z0-9-]{0,31}$, never "default"
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
another account, so an account id always means one folder, and a binding
by id is exact. Every write names the `expectedRev` the caller read and
is refused `stale_rev` otherwise. `Retire` keeps the record for what is
bound to it and leaves it out of every choice and default; `Remove` asks
`Facts.Bound(id)` and is refused while a session the server lists, an
engine run or a managed run not closed names it. A default that names a
retired or removed account reads as `default`.

**Readings** (`reading.go`): a `Reading` is the report of one read, for
one agent (a catalog agent id) and one account id, with the agent's
probed version, the reader's qualified version and the time read. A
`View` is what a page and the CLI show:

```go
type View struct {
	Agent   ViewAgent   `json:"agent"`   // id, name, adapter, version
	Model   ViewModel   `json:"model"`   // chosen ("" for the agent's default), and the words shown
	Account ViewAccount `json:"account"` // id, label, default, retired, plan
	Host    ViewHost    `json:"host"`    // where ("native" or another Runtime's), the server's name, or "remote"
	State   string      `json:"state"`   // ok | reading | unsupported | not_installed | signed_out | key_paid | unqualified | remote | error | never_read
	Words   string      `json:"words,omitempty"`
	Windows []Window    `json:"windows"` // source "provider"
	Credits []Credit    `json:"credits"` // source "provider"
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
whoever asks; a reading is `fresh` for 15 minutes, then `stale`, and
`reset_passed` once a window's reset is behind the clock, whatever its
age. The cache never reads on its own: there is no background poll. It
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
| `GET /api/capacity` | `agent`, `account` (default: the agent's default), `model` | one `View`; at most 32 KiB |
| `GET /api/capacity` | none | every installed agent's view for each of its accounts, from what is kept, reading nothing; at most 128 KiB |
| `POST /api/capacity/read` | `{agent, account}` | `202` the view, `reading` or answered within the 2 s wait; a read inside `MinInterval` answers the kept one with `nextReadAfter` |

The `View`'s host: `native` and the server's name for a launch on the
native Runtime; for a Runtime that is not native, `remote` with "read
where it runs: not on this server". The counts come from the session
registry (sessions running bound to the key, and starts in the last 5
hours and 7 days, kept as at most 512 start times per key in memory).
`Facts` is the server: the catalog, the adapters, `checkAccountDir`, and
`Bound`. The readers are reached through a server option the tests
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
[--agent <id>] [--account <id>] [--model <name>] [--read] [--json]`, the
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
it (`InstallSkillIn`, as for Brakeman). A catalog entry whose own env
sets its adapter's `ConfigDirEnv` (a copy of an agent under a
configuration of its own, #291) has its login pinned: it takes the
`default` account only, and its view says the login is the entry's. A
model goes through the adapter's `ModelArgs` after the launch's own
arguments, held to the pattern Brakeman's configuration and
`ext.TurnLaunch` hold a model to (`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,99}$`,
at most 100 bytes); an adapter without `ModelArgs` refuses a model, and
arguments that already carry the model flag beside a model are refused
("set the model in one place"). `session.Launched` gains `Account
BoundAccount` (`{ID, Label, Folder}`) and `Model`; the admin's `launch`
block of `GET /api/sessions/{id}` shows the id, the label and the model,
never the folder to anyone but the admin and never to a link's viewer,
whose `session` info is unchanged. **Resume** (`resume.go`) launches from
`Launched` as today, so it keeps the account and model the session was
launched with, whatever the default is now; an account removed since is
refused `409 account_gone` with words. A clone takes the source's
account and model as its defaults, which the person may change.

**Crews.** `crew.Member` gains `Account` and `Model`, both optional
(`omitempty`, so a crew saved before reads unchanged), checked by
`Validate` against the same patterns and by the API against the accounts
and the adapters, as `CheckAgents` checks agents. At launch, one function
in the API (`bindMembers`) resolves every member's account for every
launch path, `POST /api/crews/{id}/launch`, a ticket's approved launch
(`LaunchApproved`), `ext.Host.Launch` and a Brakeman act that launches, and
the engine records the crew as launched with each member's `Account`
resolved to a concrete id (never empty) and its `Model` as given. That
copy is what the ledger keeps already (`runs.crew`) for a managed run and
the run record's members for a legacy one (`MemberState` gains `Account`
and `Model`), so every attempt, retry, repair, member added or started
later, resume and continuation reads the account and model from the run,
never from the accounts document's default. `LaunchSpec` gains `Account
BoundAccount` and `Model`; `Server.Launch` sets the environment and the
arguments from them. A run whose bound account has been removed refuses
an attempt with words: the attempt fails at its launch (`account_gone`)
before any process exists, and is never launched under another login. A crew JSON in the
ledger from before this item has no `account`: it reads as `default`,
which is what those runs ran under. A ticket's approved plan revision
whose body names no account launches with the default at that launch,
and the run page says so; one whose body names an account launches with
that account or is refused when it is retired.

### `internal/brakeman` and `internal/api`: Brakeman reads capacity (PR-6)

`brakeman.Visibility` gains `Capacity bool` (`capacity`, in `Classes`):
on in `DefaultConfig`, absent (off) in a configuration saved before, so
Brakeman sees nothing new until the person turns it on. `GET
/api/capacity` joins `brakemanReads` under that class: the principal
`brakeman` gets the view without any account's folder or the server's
name, labels and figures only, scoped as every shared read. A new ref
kind `capacity:<agent>/<account>@<readAt>` (in `refs.go` and `kinds.go`'s
table) cites a reading; at a brief's submit the API copies the cited
reading's windows and time into the brief's ref (as BK2 copies a
receipt), so the fact stays checkable after the reading leaves the
cache. The guide gains a `capacity` section: what the view holds, that a
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
  takes them, the panel below; "this computer" shows the panel's remote
  state. Launch stays enabled whatever the panel says.
- PR-7: `CrewMembersTable.vue` and `CrewEditor.vue` gain each member's
  account and model; `pages/crews/[[id]].vue`'s launch opens
  `CrewLaunchSheet.vue` (each member's compact panel, then Launch);
  `DispatchLaunch.vue` shows the revision's members' lines;
  `CrewRunHeader.vue` shows the bound accounts and models;
  `BrakemanSettings.vue` picks its account from `AccountSelect` (writing
  the account's folder into the configuration's `account`, as it is
  stored today; a folder not in the list shows as "a folder not in
  Accounts") and gains the Capacity switch; `BrakemanRefChip.vue`
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

**The accounts document.** One writer, the API under one lock; every
write reads the document, checks `expectedRev`, validates, bumps `rev`
once and saves through `internal/store`'s atomic write before it answers.
An account's folder never changes. Retire is a field; remove deletes the
record only after `Bound` found nothing, under the same lock as launches
take to resolve an account (`accountsMu`), so a launch cannot bind an
account between the check and the delete. The defaults name accounts of
their adapter; a default left naming a retired or removed account reads
as `default`. Migration: none (a new document; a server without it has
every adapter on `default`, which is `main`'s behaviour).

**A reading.** `never_read` → `reading` (a read began for the key) →
one of `ok`, `signed_out`, `key_paid`, `unqualified`, `error`, written
into the cache, then `OnRead`, then the `capacity` event: an event is
never sent for a reading not yet kept, so a page that reads the view on
the event sees it. A reading never goes to disk, never to a session,
never into the ledger or Dispatch, and nothing in `internal/crew` reads
it. `unsupported`, `not_installed` and `remote` are answered without a
read.

**A launch's binding.** For a session: resolve the account (under
`accountsMu`, reading the default once) → build the environment and
arguments → start the process → `Launched` holds the binding for the
session's life and its resume. For a crew: `bindMembers` resolves every
member under `accountsMu` → the engine makes the run with the resolved
crew → for a managed run, the run's first ledger row (`CreateRun`) holds
that crew in `runs.crew` before any attempt is reserved → every attempt's
`LaunchSpec` takes its member's account and model from the run. Nothing
after the first row reads the accounts document's defaults for that run.
An account's folder is resolved from its id at each attempt, and the id
means one folder for good. A continuation copies the earlier run's crew,
bindings included. A ticket's launch freezes the revision's package as
today; the binding is in the run's crew, never written into the package.

**What never happens.** No reading refuses, delays, pauses or reorders a
launch, an attempt or a check. No code switches a session or a run to
another account. No reading is shown as zero for a window not reported,
and no estimate is added to a provider's figure. No reader sends a
method or flag outside its fixed list.

## Out of scope

- Reading on a schedule, in the background or across a restart; a history
  of readings or a chart of use over time.
- Readers for Copilot, agy, goose, Cursor, OpenCode, pi, omp, aider, Amp
  and dsh: each stays unknown until its tool documents a way to read
  usage before a launch without a model turn; each such reader is a
  later small PR with its fixture and recipes check.
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
`TestClaudeUsageRefusesModelTurn` (`num_turns` 1: `unqualified`, and the
next read of that version does not run the program),
`TestClaudeUsageSignedOut` (`loggedIn` false: `/usage` never run),
`TestClaudeUsageKeyPaid`, `TestClaudeUsageArgv` (the exact argv, the
directory, `CLAUDE_CONFIG_DIR` from the input, no `CONDUCTOR_*`
variable), `TestCodexUsageExchange` (a helper process speaking the
protocol, run by the test binary: the methods arrive exactly as
`codexUsageMethods`, in order, and the process group is ended),
`TestCodexUsageMethodsClosed` (the constant is the four entries and
none is `config/read`), `TestCodexUsageWindows` (10,080 and 300 minutes
named, a null secondary absent, `accountId` and `email` never in the
report), `TestCodexUsageKeyPaid`, `TestCodexUsageSignedOut`,
`TestUsageBounds` (a line past 1 MiB and output past 4 MiB end the read
with `error` and the group killed), `TestUsageTimeout` (the helper writes
its pid file, the test waits for it, then for the kill, with the
timeout variable shortened), `TestUsageReadersDeclared` (only `claude`
and `codex` have a reader, each with a qualified version). The `recipes`
tier (`usage_recipes_test.go`, `make test-recipes`): each reader against
the installed tool, skipped with its reason when the tool is absent or
signed out; for Codex, `app-server generate-json-schema` written to a
temporary folder holds both methods and every field the reader reads.

`internal/capacity` (PR-2): `TestAccountsValidate` (each bound, the id
pattern, `default` refused as an id, an adapter without `ConfigDirEnv`
refused, a folder `Facts` refuses), `TestAccountsExpectedRev`,
`TestAccountFolderFixed`, `TestRetireKeepsRecord`,
`TestRemoveRefusedWhileBound`, `TestDefaultsReadDefaultWhenRetired`,
`TestAccountsDocumentBounded`, `TestCacheOneReadPerKey` (two askers, one
read, the fake reader held at a gate until both asked),
`TestCacheTwoAtOnce`, `TestCacheMinInterval` and `TestCacheFreshness`
(the fake clock: fresh, stale, a reset passed), `TestCacheKeepsEight`,
`TestCacheDropsLeastAsked`, `TestOnReadAfterKept` (the hook sees the
reading in the cache), `TestMissingWindowIsAbsent` (the view's JSON has
no window and no `percent` for one not reported),
`TestCountsApartFromProvider` (counts only under `counts`, marked
`estimate`), `TestEveryStateHasWords`.

`internal/api` (PR-3): `TestAccountsRoutesAdminOnly` (a session token and
a link refused; a switchyard `403 switchyard`), `TestAccountsWrites`
(each route, `stale_rev`, a folder in `PATCH` refused),
`TestRemoveAccountInUse` (a running session and a managed run not closed
named), `TestCapacityViewStates` (unsupported, not installed, never
read, remote for a non-native `Where`), `TestCapacityReadThenEvent` (the
fake reader answers once the test opens its gate; the test waits for the
`capacity` event and reads the view: `ok`), `TestCapacityEventBounded`,
`TestCapacityRepliesBounded`, `TestCapacityMinInterval`,
`TestGuideListsCapacityRoutes` (the rows; the tools golden unchanged),
`TestEngineImportsNoCapacity` (with `go/parser`: no file of
`internal/crew` or `internal/crew/ledger` imports `internal/capacity`).
`internal/cli` (PR-3): `TestAccountsCommand`, `TestCapacityCommand` (the
text says "not reported" and each state in words; `--json` is the view).

PR-4: `internal/api` `TestLaunchBindsAccount` (the session's environment
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
class required; no folder and no server name in the principal's reply),
`TestBriefCopiesCapacityRef` (the reading dropped from the cache after
the submit; the brief's ref still has its windows), `TestGuideCapacity`
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
transcript names the folder it was given). PR-7: vitest
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
   `runs.crew`), and a guest would be given the folder as it is given
   the worktree.
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
| PR-1 | agents: the usage readers of Claude Code and Codex, qualified against their tools | `Adapter.Usage`, `UsageReader`, `UsageInput`, `UsageReport`, the two readers, their fixtures (written by hand to the shapes read, no real account's ids or figures), the bounds and timeouts, the recipes checks | `internal/agents` | the PR-1 tests above, the `recipes` tier | none | PR-2 |
| PR-2 | capacity: account references, readings and the cache | `internal/capacity`: `Accounts`, `Account`, `Validate` with `Facts`, the store document, retire and remove, `Reading`, `View`, the states and their words, `Cache` with its clock, reader and `OnRead` | `internal/capacity` | the PR-2 tests above | none | PR-1 |
| PR-3 | api, cli: the accounts and capacity routes, the `capacity` event and the commands | `accounts.go`, `capacity.go` (the routes, `Facts`, the readers wired, the counts from the registry), the event and its bound, the guide rows, `conductor accounts` and `conductor capacity`, `docs/protocol.md`'s rows | `internal/api`, `internal/cli`, `docs/protocol.md` | the PR-3 tests above | PR-1, PR-2 | none |
| PR-4 | api, session, crew: a launch bound to its account and model | the session's `account` and `model`, `Launched`'s binding, resume and clone, `Member.Account` and `Model`, `bindMembers` on every launch path, `MemberState`, `LaunchSpec`, an attempt refused when its account is gone, the guide's launch schemas, `docs/protocol.md`'s launch fields | `internal/api`, `internal/session`, `internal/crew`, `docs/protocol.md` | the PR-4 tests above | PR-3 | none |
| PR-5 | web: Accounts in Settings, the capacity panel and the session launch dialog | `useAccounts`, `useCapacity`, `utils/capacity.ts`, `CapacityPanel`, `AccountsCard`, `AccountSelect`, `LaunchSessionModal`'s fields and panel, the stub's readers and fixtures, `capacity.spec.ts` | `web/app/components`, `web/app/composables`, `web/app/utils`, `web/app/pages`, `web/e2e` | vitest and Playwright above | PR-3, PR-4 | PR-6 |
| PR-6 | brakeman, api: Brakeman reads capacity and proposes from it | the `capacity` class, the `capacity:` ref and its copy at submit, `GET /api/capacity` in `brakemanReads`, the guide's section and per-agent `usage`, Brakeman's request showing its view, `docs/protocol.md`'s rows | `internal/brakeman`, `internal/api`, `docs/protocol.md` | the PR-6 tests above | PR-4 | PR-5 |
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
   report only.** No model turn (checked by the result line each time;
   a turn turns the reader off for that version); the people's text the
   command prints is dropped unread; `auth status` is decoded into its
   four non-identifying fields.
5. **On demand, in memory.** A read when a page, the CLI or Brakeman
   asks, at most once in five minutes per agent and account (each read
   reaches the provider), fresh for 15 minutes, lost at
   a restart; no background poll. Chosen over a poller, which would read
   for nobody and spend the providers' patience.
6. **An account is a reference.** An adapter, a label and the folder the
   agent's own login lives in, fixed once added; retire rather than
   delete; remove only when nothing is bound; no credential, email,
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
12. **Elsewhere is unknown.** A session on another computer and a launch
    on a non-native Runtime show unknown rather than the server's
    reading.
13. **The admin sees folders; nobody else does.** Accounts and capacity
    are the admin's routes; a link's viewer sees no account; Brakeman
    sees labels and figures, never a folder or the server's name.
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
