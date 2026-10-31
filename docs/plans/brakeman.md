# Brakeman: observation and drafting, the operating guide, authorized actions and the background role (Program items BK1, BK0, BK2, BK3)

Status: proposed 2026-10-31, for the owner's review; the owner sets it to
Planned. Main at a4026597 (PR #507, 2026-10-31). Every fact below about the
code was read from that commit on 2026-10-31; file, function and constant
names are main's.

Brakeman is Conductor's sidekick agent: an agent of the person's own
choosing, on the person's own agent subscription, that Yardmaster runs for
bounded turns to read what the crews, the runs and Dispatch hold, explain
it, and draft and propose what a person may then do. It observes,
explains, drafts and proposes; it is never another scheduler, and nothing
it writes becomes work until a person (or, after BK2, a grant the person
made) acts through the same typed operations every client uses. This plan
is four Program items in one file (the Program, sections 4.3 and 4.5):

- **BK1**, observation and drafting: a configured agent, account and model;
  a dedicated identity and session; invoked, bounded turns that brief,
  explain, draft and propose, with facts, interpretation, proposals and
  verified outcomes kept apart.
- **BK0**, capability discovery: a version-matched operating guide that the
  running server serves over MCP, the CLI and HTTP, so an agent learns what
  this build supports without a large static prompt.
- **BK2**, authorized actions: a short list of operations Brakeman may be
  granted, run through the same backend operations as every client, with
  scope, state checks and receipts.
- **BK3**, the background role: Brakeman woken by events, requests and
  configured briefings, batching, cooling down and keeping a budget, and
  rebuilt from Conductor's records after a restart or a change of model.

The Program puts BK0 at `docs/plans/capabilities.md` and BK3 at
`docs/plans/brakeman-role.md`; this one file stands for both, so the four
items share one design and one set of names (Owner decisions, 1). It is
reviewed before any of its code; the PRs at the end are cut into issues
after the review, one issue, one branch `issue-N`, one PR each
(AGENTS.md).

The names follow the railroad (the Program, section 1.2): Conductor is the
application; Yardmaster the agent harness, whose engine (`internal/crew`)
runs every Brakeman turn as a run; Brakeman the sidekick; Switchman the
routing of decisions to people, which arrives with Program item SW1 and
which Brakeman never stands in for; Dispatch the ticketing, whose items
are tickets. "Workspace" is only the paid tenant: a run's checkout is a
worktree. A Brakeman **turn** is one bounded session of Brakeman's agent
doing one **request**; what it hands back is a **brief**. "Recipe" here
always means the catalog's agent recipes (yolo, trust, resume); this plan
touches no project recipe (Program item EN1's project manifests).

The other Program items this plan meets, in plain words:

- **DS1** made Dispatch (`internal/dispatch`, `docs/plans/dispatch.md`):
  projects, tickets with stable keys, comments, relations, links, typed
  operations with receipts. Its last pages are in flight (PR #501 and PR
  #503, issues #410 and #411).
- **DS2** joins a ticket to its evidence (`docs/plans/dispatch-evidence.md`,
  issues #458 to #471): plan revisions a person approves, launches from an
  approved revision with a frozen package, a ticket's runs and their exact
  candidates with check receipts. Its first PRs are being written today
  (PR #515 for #460). DS2 is BK1's prerequisite; the first PRs below build
  on what `main` already has and the DS2 parts wait for DS2 (PR-10, PR-11).
- **C1 to C9**, the extension seams (`docs/plans/ext.md`, issues #473 to
  #489): the public package `ext`, its registry and free defaults, the
  provider's one door into the app (`ext.Host`, ext's PR-4, #476), and, as C9, `GET
  /api/capabilities` (#488), which names the registered providers. BK0's
  guide reads it once it exists (PR-9); until then the guide says so.
- **C10**, the Runtime seam (`docs/plans/runtime.md`): where an attempt's
  process and checks run, and a `where` on every evidence row.
- **DR1** (durable decision records, the operator as the single approver)
  and **RV1** (review of a candidate and a repair round as a Yardmaster
  transition) have no plan or code yet. BK2 needs both.
- Later items that build on Brakeman: AU1 (a scheduled morning briefing,
  paid, through `ext.Host`), CP1 (a Brakeman comparison of candidates), PV1
  (an "Ask Brakeman" destination for a browser capture), AC1 (Brakeman may
  propose delaying work for capacity). None is part of this plan.

## Articles it serves

1. No premium code and no licence check: Brakeman ships whole in this
   repository; nothing asks for a plan, a tier or an entitlement. The guide
   names the providers C9 reports and branches on nothing else (PR-9).
2. Fully useful alone: every part of the four items runs on the free build
   with the person's own agent CLI and login, the local ledger and the local
   Dispatch store, with no account and no network beyond the agent's own.
3. Paid features gated by hosted dependencies: not touched. Nothing here is
   paid (decision D1); routing a brief to a named person, push delivery,
   graph-enriched context and shared briefings are paid later and live
   behind the providers of `ext`, which this plan neither calls nor defines.
4. Stop, inspect, revoke, export, required approvals: a Brakeman turn is an
   ordinary run that Stop ends; its requests, briefs and the drafts made
   from them are plain files in the data directory (inspected on the page
   and through `GET /api/brakeman/requests`, carried by C6's export); its
   token is its session's, which dies with the session, and turning
   Brakeman off stops the turn in progress; it answers no question,
   approves no plan revision and passes no gate, and its writes never
   reach a route that would (Events, orderings and invariants).
5. Dependencies one way: no new module, no new Go or npm dependency; the
   new package `internal/brakeman` imports the standard library and
   `internal/store` alone (what it cannot check itself, an agent or a crew
   body, it asks of `Facts`, an interface `internal/api` implements), so it
   never imports `internal/crew` and `internal/crew` never imports it;
   `internal/api` wires the two (PR-14's role loop). Nothing of `ext` until
   PR-17, where `internal/api`, never `internal/brakeman`, calls `ext.Host`.
6. No feature build tag: none added. The one test tier used is the existing
   `recipes` tag, for the agents' model flags (Tests per surface).
7. The free default is `main`: every route, tool and page added here is new
   behaviour beside `main`'s; no existing route changes its answer, and the
   MCP server's existing tools list byte for byte as before (PR-2's golden
   test). Brakeman is off until a person configures it.
8. A hosted dependency unreachable: not touched; nothing here reaches a
   hosted service.
9. `CGO_ENABLED=0`: no cgo; no SQLite schema changes; Brakeman's records are
   JSON documents through `internal/store`.
10. The repository contract: this plan before code; one issue, branch and
    PR per row of The PRs; Go race tests per package, vitest and Playwright
    for the pages; no WebSocket frame or control message, so
    `internal/proto` and `web/app/utils/protocol.ts` are untouched, and
    `docs/protocol.md` gains the HTTP routes and one Server-Sent Event,
    `brakeman`, with its size limit and its test. A Server-Sent Event is
    typed as the `run` and DS1's `dispatch` events are: in
    `docs/protocol.md` and the composable that reads it
    (`web/app/composables/useAttention.ts` reads `dispatch`), since
    `protocol.ts` mirrors `internal/proto`'s frames and control messages
    and holds no event of `GET /api/events`; no new dependency, so
    `make notices` has nothing to regenerate.
11. Names: Brakeman, Yardmaster, Dispatch and Switchman keep their meaning;
    "brief", "request" and "turn" are new words with one meaning each
    (above); "graph" stays the crew and task view, and Brakeman's reads are
    never called a graph.
12. Execution behind one seam: a Brakeman turn starts only through the
    engine (`Engine.LaunchNamedWith`, then `crew.Launcher`), as every run's
    member does, and from PR-17 through `ext.Host`; Brakeman runs no
    command itself; Boxcar constraints below takes the eight one by one.

## Why and what happens

The person running crews overnight comes back to many runs, tasks,
attempts, check receipts, blockers, questions, messages and tickets. Most
facts are already recorded durably: the ledger (`runs/managed.sqlite`)
keeps each run's transitions, events, receipts and check logs with their
SHA-256; Dispatch keeps tickets, comments and each field's history; the
touched index keeps the files agents touched. A session's attention (OPEN,
CLAIMED, RESOLVED) and activity live only in the server's memory (the last
`MaxActivity`, 200, entries a session, while the session is listed), so
Brakeman reads them as a live window and says where the window has a gap
(What Brakeman reads); durable decision records are Program item DR1's,
not this plan's. What is missing is someone who reads
it all and says, with links, what happened, what failed and how we know,
what only looks stuck, what needs the person and what answering would
authorize. The Program asks for that someone to be an agent the person
chooses and pays for already, run by Yardmaster, using the tools, context
and permission mechanisms the other agents use, with operational visibility
granted explicitly (the Program, sections 1.3 and 1.4; decision D1).

What `main` has that this builds on, and what it lacks:

- An agent session reaches the server over HTTP with its own token
  (`CONDUCTOR_NOTIFY_TOKEN`, `Local.AgentTokenOK`): the task tools
  (`internal/api/tasks.go`, `taskSession`), the messages, and DS1's
  `ticket_read` and `ticket_comment` (`internal/api/dispatch_session.go`).
  `conductor mcp` (`internal/cli/mcp.go`) serves sixteen tools on stdio and
  is registered at launch with Claude Code (`--mcp-config`) and Codex (`-c
  mcp_servers.conductor…`, with `env_vars` since #354). Every other read is
  the admin's (`requireAdmin`, `launching`): no agent can read the runs,
  the tickets or the sessions list, which is right for a worker and is
  exactly the visibility Brakeman needs granted.
- The engine launches an unsaved crew as a run (`Engine.LaunchNamed(ctx,
  crew.Crew, label)`, `internal/crew/run.go:623`) through `crew.Launcher`
  (`api.Server.Launch`, `internal/api/runs.go:55`) and types a member's
  prompt once its session is ready, through the guarded submission
  (`Local.Submit`), so a startup question is never typed into.
- Nothing describes the server's own operations to an agent beyond the
  tools' one-line descriptions and the skill (`internal/agents/skill.go`);
  there is no `GET /api/capabilities` (C9, #488, is not started).
- There is no notion of an account: an agent's login is wherever its CLI
  keeps it, which its adapter's `ConfigDirEnv` moves (Claude Code's
  `CLAUDE_CONFIG_DIR`, Codex's `CODEX_HOME`). A model is chosen today only
  by a member's arguments.

After the first slice (PR-1 to PR-8) ships, a person sees:

- **Settings, Brakeman.** Off until set: the agent (an installed catalog
  agent whose adapter registers Conductor's MCP server: Claude Code or
  Codex today), the account (the folder that agent's login lives in, or its
  default login on this machine), the model (for an agent whose model flag
  Conductor knows; empty for its default), the folder its turns run in
  (always in the agent's own read-only mode, never yolo), what it may see
  (runs, tasks and checks; check logs; Dispatch; sessions; touched files;
  terminal screens, off by default), its scope (all of it, or chosen
  projects, repositories, crews, runs and sessions), what it may do
  (observe only, or also draft and propose), and its budget (minutes a
  turn, reads a turn, turns a day).
- **Ask Brakeman.** On the Brakeman page (`/brakeman`), from the palette,
  and from a run's or a task's menu: "Brief me since…", "Explain this",
  "Prepare a decision", "Draft tickets", "Propose a crew", "Prepare a
  handoff", "Find…", "Overlapping changes", or a free question. The request
  shows as queued, then running with its run and session (the person can
  watch the terminal, stop it, or cancel the request), then briefed.
- **A brief**, as Conductor renders it: a title, Brakeman's reading (marked
  as its interpretation), the **facts**, each with the evidence it rests on
  as links (a run, a task, an attempt, a check receipt with its log hash, a
  ticket or comment, a session, a question, a file, an earlier brief),
  **interpretations** naming the facts they read, **proposals** naming an
  operation Conductor can run, **verified outcomes** naming a receipt, and
  where asked: **decision cards** (what work occurred, why a person is
  needed, the precise question, the alternatives and their tradeoffs, the
  recommendation and its uncertainty, what is blocked, what answering
  authorizes, the evidence and the revision it was read at) whose controls
  are Conductor's own, read from the live record; **draft tickets** and
  **draft crews** as forms the person edits and creates; **handoff
  packages**; **overlapping changes**. Every brief says which agent, model
  and version produced it, for which request, as of when.
- Nothing Brakeman does takes the focus: a brief arriving adds a count to
  the sidebar's Brakeman row and to the page, with no dialog, no toast that
  takes the keyboard and no navigation. A question Brakeman's own session
  asks (a trust question, a tool approval) is shown in Needs you labelled
  "Brakeman · br-n", and notifies (a browser notification, Chime, a
  webhook) only for a request a person made; a background turn's question
  (BK3) notifies nothing, and its deadline ends it unanswered.
- After a restart of the server, or with another agent or model chosen,
  "What changed since your last brief?" reads the earlier briefs and their
  coverage from the data directory, not from any conversation: every turn is
  a fresh session.

An agent in any Conductor session (BK0) can call the `guide` tool or run
`conductor guide`: a small overview first (this build's version and commit,
who the caller is and what it may do, the sections and their sizes), then
each section on request: the operations (name, scope, MCP tool, CLI form,
HTTP route, input schema, receipt), the limits, the host (the platform, the
managed-run switch, the ledger's and Dispatch's state, the catalog's agents
and their probed versions), the receipt formats, and the providers (empty,
and said to be unknown, until C9; PR-9).

Later, gated PRs add: DS2's plan revisions, launches and candidates as
Brakeman's evidence and drafts (PR-10, PR-11); `ext.Host` in front of
every Brakeman launch (PR-17); the four granted actions of BK2 (PR-12,
PR-13, PR-18); and BK3's background role (PR-14, PR-15).

## The changes

### `internal/brakeman` (new, PR-1): the configuration, requests, briefs and evidence references

A package with no HTTP, no session and no process, like `internal/dispatch`:
the API, the CLI and the tests are its callers. It imports the standard
library and `internal/store` (atomic JSON documents and the owner lock)
alone; what it cannot check itself it asks of `Facts`, below.

**The configuration** (`config.go`), one document `brakeman/config.json`
in the data directory, written only through the admin's route:

```go
type Config struct {
	Enabled bool   `json:"enabled"`
	AgentID string `json:"agentId"` // a catalog agent
	// Account is the folder the agent's login lives in, set in its
	// session as its adapter's ConfigDirEnv (CLAUDE_CONFIG_DIR,
	// CODEX_HOME): a reference, never read by Conductor beyond a stat.
	// "" is the agent's default login on this machine.
	Account string `json:"account,omitempty"`
	Model   string `json:"model,omitempty"` // "" is the agent's default
	Cwd     string `json:"cwd"`             // the folder its turns run in
	// No yolo field: a turn always runs in its adapter's read-only mode
	// (Adapter.ReadOnlyArgs, PR-3a) and never with the yolo recipe.
	// AllowConductorTools lets the agent call Conductor's MCP tools
	// without asking, where its adapter can say so (Claude Code).
	AllowConductorTools bool        `json:"allowConductorTools"`
	Visibility          Visibility  `json:"visibility"`
	Scope               Scope       `json:"scope"`
	Permissions         Permissions `json:"permissions"`
	Wake                Wake        `json:"wake"`
	Budget              Budget      `json:"budget"`
	Rev                 int64       `json:"rev"`
	UpdatedAt           time.Time   `json:"updatedAt"`
}
type Visibility struct {
	Runs      bool `json:"runs"`      // runs, tasks, attempts, check receipts, messages, blockers
	Logs      bool `json:"logs"`      // check logs' text; without it a receipt carries its exit, subject and SHA-256
	Dispatch  bool `json:"dispatch"`  // projects, tickets, comments, history
	Sessions  bool `json:"sessions"`  // the sessions list: names, states, questions, quiet time
	Touched   bool `json:"touched"`   // the touched index
	Terminals bool `json:"terminals"` // a session's screen text; off by default
}
type Scope struct {
	// A selector left empty is all; one given narrows (at most 50 entries
	// each). Scope.Admits, below, combines them.
	Projects []string `json:"projects,omitempty"` // Dispatch project keys
	Repos    []string `json:"repos,omitempty"`    // absolute repository roots
	Crews    []string `json:"crews,omitempty"`    // crew ids
	Runs     []string `json:"runs,omitempty"`     // run ids
	Sessions []string `json:"sessions,omitempty"` // session ids
}
type Permissions struct {
	// Observe is true whenever Enabled is. Draft (draft_ticket/1,
	// draft_crew/1, handoff/1) and Propose (proposal/1, decision/1) are
	// the person's to turn off; both off is observe only, and a brief
	// with an item they do not allow is refused (invalid_brief). Act
	// names the operations BK2 grants; it must be empty until PR-12.
	Observe bool     `json:"observe"`
	Draft   bool     `json:"draft"`
	Propose bool     `json:"propose"`
	Act     []string `json:"act,omitempty"`
}
type Wake struct {
	Mode string `json:"mode"` // "invoked" until PR-14 adds "background"
}
type Budget struct {
	TurnMinutes  int `json:"turnMinutes"`  // 1 to 60; 15
	ReadsPerTurn int `json:"readsPerTurn"` // 10 to 2,000; 300
	TurnsPerDay  int `json:"turnsPerDay"`  // 1 to 500; 40, counted by the server's local day
	// MinutesPerDay arrives with PR-14 (BK3); the day's charges hold
	// minutes from PR-1 (The store).
}
```

`Validate(c Config, f Facts) error` holds every field to its bound and
asks `Facts` (an interface the API implements over the catalog and the
adapters) what the package cannot know: the agent exists, is not hidden,
has an adapter with an MCP registration (`agents.Adapter.MCP`, today Claude
Code and Codex) and a read-only mode (`Adapter.ReadOnlyArgs`), and takes a
model only when its adapter has `ModelArgs` (PR-3a). `Facts.CheckCrew`
checks a draft crew's body as `POST /api/crews` does (the API answers it
with `crew.Crew`'s strict decoding and `Validate`), so the package needs
no `internal/crew`. The model is `^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,99}$`, an argument
of the adapter's own flag, never free arguments; the account and the folder
are absolute, at most 4,096 bytes, checked by the API (the account a
directory owned by the server's user, the folder through `resolveCwd`). A
save names the `rev` it read (`409 stale_revision` otherwise), bumps it and
stamps `updatedAt`. The defaults, written on the first read: off, no
agent, the server's default folder, visibility on but terminals, all of
scope, observe, draft and propose, the budget above, mode invoked.

`Scope.Admits(item)` is the one function observation, resolution and
(PR-12) actions ask. An item is in scope when it matches every non-empty
selector that applies to its kind: a run and what is under it (tasks,
attempts, checks, commands, events, messages, commits) by `Crews`, `Runs`,
`Repos` (the repository root of the run's folder) and `Projects` (the
project of the ticket its run is linked to; a run linked to none is
outside a non-empty `Projects`); a session by `Sessions`, `Repos` (its
folder's root) and, for a run's member, its run's selectors, while a
session in no run is outside a non-empty `Crews`, `Runs` or `Projects`
unless `Sessions` names it; a ticket, comment or Dispatch receipt by
`Projects`, and by `Runs` and `Crews` through the runs linked to it (a
ticket linked to none is outside either when it is non-empty); a
touched-index entry by its session's; an earlier brief by its footprint,
below. Evidence is narrowed by the visibility: check logs, terminals,
sessions and the rest each by its own switch.

**An earlier brief is derived content and carries its sources' grant.**
Its title, summary, interpretations and drafts can repeat what its turn
read (a ticket's text, a screen, a check log), and checking its refs again
takes none of that back out of the prose. So each kept brief records its
**footprint**, from the server's own records, never the agent's word: the
scope snapshot its turn ran under (`Request.Config`) and the visibility
classes its turn's reads actually used (from its read log: `runs`, `logs`,
`dispatch`, `sessions`, `touched`, `terminals`; a read log that dropped
entries counts every class its snapshot had on). A turn (and the
`brakeman` principal's every read: `…/briefs`, a `brief:` ref, the request
route's "last complete briefing") is given an earlier brief whole only
when its footprint is within the turn's grant: every class it used is
visible now, and the current scope covers its snapshot's,
`Scope.Covers(earlier)` (PR-1): true only when every item `earlier.Admits`
takes, `Admits` now takes too. It is decided clause by clause of `Admits`,
its exceptions included, never selector by selector alone. Each
conjunctive clause (`Projects`, `Repos`, `Crews`, `Runs`, and `Sessions` as
it narrows a run's members) is covered when the current selector is empty,
or the earlier one is non-empty and a subset of it. The standalone-session
clause is not conjunctive: `Sessions` widens there as well as narrows (a
session in no run is admitted when `Sessions` names it, or when
`Sessions`, `Crews`, `Runs` and `Projects` are all empty), so it is
covered only when the earlier scope admitted no standalone session (its
`Sessions` empty and one of the other three non-empty), or the earlier
`Sessions` is non-empty and a subset of the current one, or the current
scope admits every standalone session (all four empty now). So `Runs=[r],
Sessions=[s]` narrowed to `Runs=[r]` withholds a brief whose turn read the
standalone session `s`. Otherwise the brief is served as its projection: its id,
kind, time and the refs the scope and visibility admit now, with
`withheld: "made under a wider scope or visibility"` and none of its prose
or items. A `brief:` ref to a withheld brief is refused (`422 bad_refs`,
outside the scope). The admin's reads are unchanged. Coverage watermarks
are numbers, not prose, and are filtered by the scope now. Tests (PR-4):
`TestAnEarlierBriefUnderAWiderScopeIsWithheld` (scope narrowed between two
turns), `TestTurningTerminalsOffWithholdsBriefsThatReadScreens` (and the
same for check logs), `TestABriefWithinTheGrantIsGivenWhole`,
`TestRemovingAStandaloneSessionWithholdsItsBrief` (`Runs=[r],
Sessions=[s]` to `Runs=[r]`); (PR-1) `TestCoversAgreesWithAdmits` (every
pair of scopes over a small universe, two values per selector and each
empty, against the brute force "every item admitted before is admitted
now" over runs, run members, standalone sessions and tickets linked and
unlinked).

**A request** (`request.go`) is one thing a person asked of Brakeman,
kept as `brakeman/requests/br-<n>.json`:

```go
type Request struct {
	Schema    int       `json:"schema"` // 1
	ID        string    `json:"id"`     // br-<n>, n from 1, never reused
	OpID      string    `json:"opId"`   // the client's, the repeat key
	Kind      string    `json:"kind"`
	Subjects  []Ref     `json:"subjects,omitempty"` // at most 10
	Note      string    `json:"note,omitempty"`     // Markdown, at most 16 KiB
	Since     time.Time `json:"since,omitzero"`     // a briefing's start
	By        string    `json:"by,omitempty"`       // the person's display name
	CreatedAt time.Time `json:"createdAt"`
	State     string    `json:"state"`
	Why       string    `json:"why,omitempty"` // a refusal's or an end's words, at most 500 bytes
	RunID     string    `json:"runId,omitempty"`
	SessionID string    `json:"sessionId,omitempty"`
	Agent     Stamp     `json:"agent"`  // agent id, adapter, probed version, model, whether an account was set
	Config    Snapshot  `json:"config"` // visibility, scope, permissions, budget and rev as the turn began
	Deadline  time.Time `json:"deadline,omitzero"`
	Reads     int       `json:"reads"`
	// The brief, its resolutions and a pending make's input are documents
	// of their own beside the request (below), so the request stays small.
	HasBrief  bool      `json:"hasBrief,omitempty"` // br-<n>-brief.json and br-<n>-refs.json are kept
	BriefOpID string    `json:"briefOpId,omitempty"`
	BriefAt   time.Time `json:"briefAt,omitzero"`
	Footprint *Footprint `json:"footprint,omitempty"` // the classes its reads used, with Config's scope (Scope.Admits)
	Made      []Made     `json:"made,omitempty"`     // drafts made work by a person: pending op, outcome
	Edges     []Edge     `json:"edges,omitempty"`    // draft tickets' blocks: from, to, pending or made
	Acts      []Act      `json:"acts,omitempty"`     // PR-12: BK2's acts, written pending before the operation runs
	StrayRun  string     `json:"strayRun,omitempty"` // a run its launch returned after it closed, stopped then
	Batch     string     `json:"batch,omitempty"`    // a wake request's batch (PR-14)
	Coverage  *Coverage  `json:"coverage,omitempty"` // the server's watermarks of what the turn read
	ClosedAt  time.Time  `json:"closedAt,omitzero"`
}
```

The kinds: `briefing` (what changed since `Since`, else since the last
complete briefing's watermarks, else the last 24 hours), `explain` (one subject: a run,
task, attempt, session, question or ticket), `decision` (a question, a task
that failed or is blocked, a ticket), `draft_tickets` (a ticket or comment,
or the note as the discussion), `propose_crew` (a ticket), `handoff` (a
task, attempt or session), `locate` (the note as the question),
`overlaps`, `ask` (the note, any subjects) and, from PR-14, `wake` (a
batch, no subjects). The states and their
transitions are in Events, orderings and invariants. One of a later
schema is skipped, logged once and never rewritten.

**A request's documents and their bounds.** Every name matches
`internal/store`'s `^[a-z0-9-]+\.json$`; each document is encoded with
`store.Encode` (the indented form `Save` writes) and measured **before any
write**, and a change whose document would pass its bound is refused with
nothing written, so a document written is always one `LoadLimit` reads
back after a restart:

| Document | Holds | Bound (encoded) |
|---|---|---|
| `br-<n>.json` | the request above: no brief, no resolutions, no make input | 256 KiB, reserved by construction for its later updates: `Made` at most one entry per item (100) of at most 600 bytes, `Edges` at most 190 (20 draft tickets without a cycle) of at most 300, `Acts` (PR-12) at most 20 of at most 600, the rest at most 32 KiB; a test fills every list to its largest |
| `br-<n>-brief.json` | the brief as kept | 256 KiB: the brief's own bound is measured on this encoding, not on the body the session sent |
| `br-<n>-refs.json` | one resolution per distinct ref of the brief (`seen` and `now`), each snapshot's fields bounded (ref 512 bytes, url 1 KiB, state 32, rev 64, sha256 64) | 4 MiB; a brief has at most 500 distinct refs across all its items (`invalid_brief` past it), and a brief whose resolutions would still pass 4 MiB is refused `413 bound` |
| `br-<n>-reads.json` | the turn's read log (Resolution, below) | 2 MiB; at most 5,000 entries, the oldest dropped first and the drop said |
| `br-<n>-make-<i>.json` | a pending make's input, as the person edited it (Drafts made work) | 4 MiB; a make whose encoded input passes it is refused `413 bound` |

The siblings are written before the request write that names them and
removed after the request when it is pruned; `List` reads only
`^br-[0-9]+\.json$`; at `Open`, siblings of a request that does not name
them (a crash between the two writes) are removed. Tests (PR-1):
`TestEveryDocumentFitsItsLoadLimit` (each at its largest, written,
reopened), `TestARequestWithEveryListFullStillLoads`,
`TestABriefWhoseRefsPassTheirBoundIsRefusedUnwritten` (100 panels of 50
distinct refs), `TestOrphanSiblingsAreRemovedAtOpen`,
`TestDocumentNamesAreTheStoresOwn`.

**A brief** (`brief.go`), schema `brakeman.brief/1`, is what a turn hands
back, at most 256 KiB as JSON:

```go
type Brief struct {
	Schema      string `json:"schema"`      // "brakeman.brief/1"
	Title       string `json:"title"`       // at most 200 characters
	Summary     string `json:"summary"`     // Brakeman's reading, Markdown, at most 4 KiB
	Uncertainty string `json:"uncertainty,omitempty"` // at most 2 KiB
	Items       []Item `json:"items"`       // at most 100
	// No asOf: what the brief covers is the server's record of the turn's
	// reads (Request.Coverage, What Brakeman reads), never the agent's
	// word; a brief that carries one is refused (invalid_brief).
}
type Item struct {
	Kind string `json:"kind"` // fact/1, interpretation/1, proposal/1, outcome/1, decision/1,
	// comparison/1, evidence/1, draft_ticket/1, draft_crew/1, handoff/1, overlap/1
	// … the kind's fields, below
}
```

Each kind's fields and rules, enforced by `Validate`:

| Kind | Fields | Rule |
|---|---|---|
| `fact/1` | `text` (1 KiB), `evidence` (1 to 10 refs) | A fact without evidence is refused. |
| `interpretation/1` | `text` (2 KiB), `facts` (indexes of fact items, at least one) | Reads only facts of the same brief. |
| `proposal/1` | `text` (2 KiB), `op` (an operation name the guide lists), `target` (a ref), `args` (JSON object, 4 KiB), `expectedRevision` (string; required, and above 0, when the guide marks the operation's route as checking a revision, `Operation.Revision`), `facts` | Rendered as a control the operator presses only for an operation whose route checks the revision; any other is text with a link to the page where the person does it. Brakeman runs it only with a BK2 grant. |
| `outcome/1` | `text` (1 KiB), `receipt` (a `check:`, `command:` or `op:` ref) | A verified outcome names a receipt, or it is a fact or an interpretation. |
| `decision/1` | `about` (a `question:`, `task:` or `ticket:` ref, later `plan:` and `decision:`), `happened`, `whyHuman`, `question` (500 bytes), `alternatives` (2 to 6, each `label` of 80 characters and `tradeoffs` of 1 KiB), `recommendation` (an alternative's index, or -1 for none), `uncertainty`, `blocked` (refs), `authorizes` (1 KiB), `evidence` (1 to 20 refs), `subjectRevision` | Conductor renders the controls from the live record of `about`, never from these fields. |
| `comparison/1` | `title`, `columns` (2 to 6), `rows` (1 to 20, a label and one cell of 500 bytes per column), `evidence` | |
| `evidence/1` | `title`, `refs` (1 to 50) | An evidence panel. |
| `draft_ticket/1` | `project` (a key), `title` (200 characters), `description` (16 KiB), `acceptance` (8 KiB), `priority` (0 to 4), `blocks` (indexes of other draft tickets of the brief), `from` (refs) | At most 20 a brief; `blocks` has no cycle. |
| `draft_crew/1` | `crew` (the body `POST /api/crews` takes), `gates` (up to 10 notes: a task id and the decision a person makes before it), `from` (refs) | The crew passes `Facts.CheckCrew` (the API's `crew.Crew.Validate`) at submission. |
| `handoff/1` | `objective`, `constraints`, `decisions` (up to 20), `revision` (a `commit:` ref), `questions` (up to 20), `evidence` | Built from Conductor's records; never an agent's conversation. |
| `overlap/1` | `paths` (1 to 50), `between` (2 to 8 refs of attempts or sessions), `evidence` | |

The four kinds a brief's reader must never confuse are four kinds: a fact
names evidence, an interpretation names facts, a proposal names an
operation, an outcome names a receipt. Text fields lose their control
characters but newline and tab, as session messages do, through the
package's own `cleanText`, which never cuts: `session.CleanMessage` cuts at
500 bytes (`MaxAttentionMessage`), which would shorten a 4 KiB summary or a
16 KiB description without a word. A field past its own bound is refused
with its name and bound (`invalid_brief`), never shortened.

**Evidence references** (`refs.go`): a ref is a string of a kind and its
parts, at most 512 bytes, parsed once into a typed `Ref`:

| Ref | Names | Resolved (PR-3b) to |
|---|---|---|
| `run:<run>` | a run | the run page; its state and revision |
| `task:<run>/<task>` | a managed task | `?task=` on the run page; its state |
| `attempt:<run>/<task>/<n>` | an attempt | its row; its stage and result commit |
| `check:<run>/<task>/<n>/<id>/<k>` | a check receipt (the ledger's step row) | its log route; exit, subject commit, log SHA-256 |
| `command:<run>/<command>` | a command receipt | `GET /api/runs/{run}/commands/{command}`; its status |
| `event:<run>/<seq>` | a run's ledger event | its kind and time |
| `message:<run>/<seq>` | a members' message | its sender and delivery |
| `session:<id>` | a session | the session page; its state and attention |
| `question:<session>/<question>` | a question the session asks or asked | its state (OPEN, CLAIMED, RESOLVED) and options while the session is listed and the question in its window; else `unavailable`, beside what the turn observed |
| `ticket:<KEY-n>` | a ticket | the ticket page; its `rev` |
| `comment:<KEY-n>/<id>` | a comment | the ticket page at it; its `rev` |
| `op:<actor>/<opId>` | a Dispatch receipt | its status |
| `commit:<run>/<sha>` | a commit on a run's branches | its short id and subject |
| `file:<session>/<path>` | a touched-index entry | the Files tab; its last op and time |
| `brief:<br-n>/<item>` | an earlier brief's item | the brief |

PR-10 adds `plan:<KEY-n>/<revision>`, `launch:<id>` and
`candidate:<run>/<task>/<n>` for DS2's records; DR1's `decision:<id>` comes
with PR-12.

**The store** (`store.go`): `Open(*store.Store)` (and nothing else: it
reaches no other store) takes `brakeman/` under
the data directory (`store.Sub`, 0700) and its owner lock,
`brakeman/owner.lock`: the ledger's lock (`ownerLock`, moved in PR-1 from
`internal/crew/ledger` to `internal/store` as `store.OwnerLock`, the
ledger's behaviour and tests unchanged). Two servers may share a data
directory, as the ledger allows: the one holding the lock owns the desk;
another opens Brakeman read-only (reads served from the files, every write
and every turn answered `503 brakeman_not_owner` with the owner's words),
keeps trying for the lock as the ledger's `awaitLock` does, and concludes
what was left open only once it holds it. Where the lock is unsupported
(`lock_other.go`), the desk behaves as the ledger does there.
`Config`, `SaveConfig`; `Create`
(the next number from `brakeman/state.json`, written before the request's
own file, so a number is never reused, even after a crash between the two);
`Get`, `List(after, limit)` newest first in pages of at most 100;
`Update(id, func(*Request) error)` under one mutex, the request read, changed
and written atomically; `Prune`, which keeps the newest 500 requests and
never one that is not closed or that is pinned. A make works on a briefed,
so closed, request, and its recovery needs the request's claim and input
until the backend's outcome is written: a request is pinned while it holds
a pending `made` claim, an edge whose two ends are tickets and whose
relation is not yet recorded (an edge still waiting for its other end
pins nothing), or, from PR-12, a pending act, so the pinned are kept past
the 500 until their outcome or recovery is written. `Prune` runs under
`Update`'s mutex and checks the pins there, and a make's claim is an
`Update`, which answers `ErrNotFound` for a request pruned before it: a
make either claims a request `Prune` then keeps, or finds it gone, calls
no backend and answers `404` (its unclaimed input removed). Tests (PR-1,
the store; PR-4, the make): `TestPruneKeepsAPinnedRequest` (each pin),
`TestPruneDuringAPendingMakeKeepsItsRecords` (a seam after the claim,
before Dispatch commits: 500 newer closed requests and a `Prune`, then the
commit; `made` is recorded and a restart finds nothing pending),
`TestAMakeOfAPrunedRequestCallsNoBackend`; the day's charges, kept apart from the
requests in `state.json` and keyed by request id, so pruning or
cancelling requests never lowers them; `Open`, once it holds the lock,
concluding what the last owner left open in Brakeman's own documents
(`queued` stays queued; `starting` and `running` become `interrupted`,
with why; orphan siblings removed) and returning nothing to do of any
other store's. The clock is a field, so the tests drive it.

**The day's charges, one reservation per request.** `internal/store.Save`
replaces one document atomically and spans no two, so the charge and the
start are not one write; they are ordered, and the charge is keyed by the
request so a repeat adds nothing. `state.json`'s `days` holds, for each
of the last 8 local days, a map from request id to `{reserved, settled}`
minutes. The dequeue writes, in this order: (1) `state.json` with the
request's id in its start day's map and its reservation (BK1: `reserved`
is `TurnMinutes`; from PR-14, below, at most what the day's minutes have
left); (2) the request `starting`, its deadline its start plus `reserved`.
A crash after (1) and before (2) leaves a charged request still `queued`:
the charge stands (conservative, never an uncounted start) and the next
dequeue of that request finds its id already charged and writes (1)
again as the same entry, so it is never counted twice. After a request
closes, its entry's `settled` is written (the minutes it ran, rounded up;
never more than `reserved`); a crash before that leaves `reserved` as the
charge. `TurnsToday(now)` counts the ids in today's map; `MinutesToday(now)`
sums each entry's `settled`, or its `reserved` while unsettled. A turn is
charged wholly to the local day it started, so midnight neither splits
nor refunds it. Tests (PR-1): `TestAChargeIsWrittenBeforeTheStart` (a
crash seam between the two writes: one charge, the request queued, the
next dequeue counts nothing again), `TestAChargeIsCountedOncePerRequest`,
`TestAnUnsettledChargeCountsItsReservation` (a crash before `settled`),
`TestAChargeBelongsToItsStartDay` (a turn across midnight).

**What another store holds, the desk completes.** `internal/brakeman`
imports no store but its own, so a pending `made` entry or edge (Drafts
made work, below), whose completion needs Dispatch's receipts and
relations and the crew store, is completed by the desk in `internal/api`
(PR-4), never by `Open`: `Store.Pending()` lists them, and the desk, once
its store holds the owner lock (at start, or later when a read-only
server takes the lock over, the same handoff), runs `recoverMakes(ctx)`
over Dispatch and the crew store before it serves any Brakeman route
(`503 brakeman_recovering` until then); other servers' routes are not
held, so the recovery is written to be safe against them (rule 5).

### `internal/api`: the operating guide (PR-2, BK0's first form)

`guide.go` (new) holds the table of Conductor's operations an agent or a
client calls, one entry each, the single source the guide, the MCP tools'
list and the tests read:

```go
type Operation struct {
	Name    string         `json:"name"`    // task_result, observe, ticket_comment, run_pause…
	Summary string         `json:"summary"` // the guide's line, at most 300 bytes
	// Description is the MCP tool's description, verbatim (main's
	// sixteen byte for byte, the longest task_result's 556 bytes): at
	// most 1 KiB, apart from Summary so neither bound breaks the other.
	Description string     `json:"description,omitempty"`
	Scope   string         `json:"scope"`   // admin | session | task | ticket | observe | draft | propose | act
	Method  string         `json:"method"`
	Path    string         `json:"path"`    // the mux pattern
	MCP     string         `json:"mcp,omitempty"` // the tool's name
	CLI     []string       `json:"cli,omitempty"` // conductor task result …
	Input   map[string]any `json:"input"`   // JSON Schema of the body or the tool's arguments
	Receipt string         `json:"receipt,omitempty"` // ledger | dispatch | brakeman
	// Revision names the precondition the route itself enforces: "run"
	// (the task command body's expectedRevision, above 0) or "ticket"
	// (DS1's expectedRev); "" for a route that decodes no revision (a
	// run's stop and resume today), which a proposal never gets a
	// button for.
	Revision string         `json:"revision,omitempty"`
	Bounds  map[string]int `json:"bounds,omitempty"`  // the body's and the reply's limits
}
func Operations() []Operation
```

The first table lists every agent route and tool of `main` (the sixteen
MCP tools and the session routes they call), the admin operations a
proposal may name (a run's pause and unpause, stop and resume; a task's
cancel, retry and accept; a ticket's create, edit, comment and link; a
crew's create; a crew's launch), and the guide itself; PR-3c and PR-4 add
Brakeman's with their routes, MCP names and schemas, so `internal/cli`
never holds a schema of its own. `internal/cli`'s `mcpTools()` builds its
list from the entries with an `MCP` name that it has a handler for, their
`Description` as the description and `Input` as the schema, so the tools
and the guide cannot drift; an entry with an MCP name and no handler is
not listed (`TestAnUnhandledToolIsNotListed` names them: Brakeman's, until
PR-5); a golden test holds the existing tools' `tools/list` reply byte for
byte as it is on `main`.

The guide (`Guide`, one JSON document, computed per caller, never cached
across a configuration change):

- `overview` (at most 4 KiB): the guide's version (1), this build's
  `version.Version` and commit, the caller (`admin`, or a session with its
  kinds: a plain session, an attempt of a managed run, a member of a run
  linked to a ticket, a Brakeman turn) and its scopes, each section's name
  and size, and three lines that hold for every caller: a fact needs
  evidence; nothing an agent writes becomes work by itself; a question goes
  to a person with `ask`.
- `operations`: the entries the caller's scopes reach (an attempt sees the
  task tools, a plain session does not; the admin sees them all).
- `limits`: the named bounds that refuse a body: a task id's pattern, 128
  tasks a crew, 8 checks a task, a check's 1 to 3,600 s, a comment's 16
  KiB, a message's bound, 50 unacknowledged messages, a brief's 256 KiB and
  its items' bounds; each read from the constant that enforces it.
- `host`: the platform (`runtime.GOOS`, `GOARCH`), switchyard or not,
  `crews.managed`, the ledger's state (owned, read-only and why, absent),
  Dispatch's (open, read-only, absent), `checks.maxConcurrent`, and the
  catalog's agents (id, adapter, installed, the probe's `Identity` version
  when known); for a session caller, only the agents it could name.
- `receipts`: the formats of the three receipts a caller meets: a run
  command's (`internal/crew/commands.go`), a Dispatch operation's
  (`dispatch.Receipt`), a Brakeman request's.
- `providers`: `{"known": false, "providers": []}` until C9; PR-9 fills it
  from `GET /api/capabilities`.

Routes: `GET /api/guide?section=<name>` (the admin's) and `GET
/api/sessions/{id}/guide?section=<name>` (the session's own token, or the
admin's, through `taskSession(w, r, false)`): without `section` the
overview, else that section; `404 no_section` for another name. Each
section is at most 64 KiB and the whole at most 256 KiB, measured by a
test with every entry at its largest. A switchyard answers its own guide
(no ledger, no Dispatch, the sessions published to it).

`internal/cli` (PR-2): the MCP tool `guide` (`section`, optional), listed
for every session, which reads the session's guide from the running server
and, when the server's version differs from this binary's, says so first
("this conductor is X, the server is Y: the guide is the server's");
`conductor guide [--section S] [--json]`, in a session through its token,
else with `--server`, `--token` and `--insecure` as `conductor dispatch`
takes them. The MCP server's `initialize` instructions gain one sentence:
"Call guide to learn what you may do here."

### `internal/api`, `internal/agents`, `internal/crew`: a Brakeman turn (PR-3a, PR-3b, PR-3c)

PR-3 is three PRs: PR-3a the adapters (`internal/agents`), PR-3b the
resolution of refs (`brakeman_refs.go`), PR-3c the desk, the turn, its
routes and its refusals; "PR-3" below names the three together.

`brakeman.go` (new, PR-3c) holds the **desk**, the server's one owner of
Brakeman's requests: the store, the request in progress, the queue, the
watchdog. `New` opens it beside the ledger and Dispatch when the server
has a data directory (none on a switchyard, which answers `403` on every
Brakeman route, as `launching` does).

**A turn is a run Yardmaster launches.** The desk takes the oldest queued
request when no turn holds the slot (starting, running, or its run not yet
ended: the watchdog), writes it `starting` with its deadline and its
configuration snapshot, and launches, through the engine, an unsaved
one-member crew:

```go
no := false
crew.Crew{
	ID: "brakeman", Name: "Brakeman", Cwd: cfg.Cwd, Where: "server", Isolation: "none",
	Goal: "Brakeman request " + req.ID, Yolo: &no,
	Members: []crew.Member{{
		// the adapter's ReadOnlyArgs(), then ModelArgs(cfg.Model), then
		// AllowTools("conductor") when AllowConductorTools is set;
		// nothing a person typed
		Name: "brakeman", AgentID: cfg.AgentID, Args: args,
		Start: crew.Start{When: "immediately"},
		Prompt: line, // below
	}},
}
```

through the desk's one launch door, `(*Server).launchTurn(ctx, c, label,
crew.LaunchOpts{Class: crew.ClassBrakeman, Request: req.ID})`, which
resolves the folder through `openCwd`, refuses a crew whose `Yolo` is not
false or whose member lacks its adapter's read-only arguments, and calls
`Engine.LaunchNamedWith` (new: `LaunchNamed` with options; `LaunchNamed`
calls it with none, its callers unchanged). The label is "Brakeman ·
<kind> br-n". PR-17 puts `ext.Host` in front of `launchTurn` (Boxcar
constraints, 1).

**Read-only, whatever the folder.** A worktree is no sandbox and the
folder is the person's choice, so the turn's own agent is held to reading,
not only Conductor's routes. `Adapter.ReadOnlyArgs` (PR-3a) is the
adapter's own read-only mode: Claude Code's `--disallowedTools` rule for
every tool that writes or runs a command (Bash, Edit, Write,
NotebookEdit), its file reads and Conductor's MCP tools left; Codex's
`--sandbox read-only`. Each is checked against the CLI's `--help` in the
PR and nightly under the `recipes` tier, and whether Codex's MCP server
still reaches Conductor under its sandbox is tried in the PR: if it does
not, Codex is refused as Brakeman's agent (`Validate`, through `Facts`)
and Settings says why. No adapter without a read-only mode can be
Brakeman's, and yolo is never applied to a turn, whatever a crew or the
server's default says.

The engine starts the member through `crew.Launcher` (`api.Server.Launch`)
and types the prompt once the session is ready, as for any member, through
`Local.Submit`: a trust or startup question shows as the session's question
and is answered by a person, never typed into. The line, at most 300 bytes:

> Brakeman request br-12 (briefing): read it with the brakeman_request tool
> or "conductor brakeman request", and submit one brief with brief_submit
> or "conductor brakeman brief". Call guide for what you may read.

`LaunchNamedWith` stamps the run with its `LaunchOpts` (its class and its
request, kept in the run's record) before its member starts, and the
engine hands both to `crew.Launcher` in the `LaunchSpec`, so
`Server.Launch` knows a turn's member from the spec itself, never from the
crew's id or goal, which a person's crew could match. For such a member it
sets Conductor's own names in the session (`createSessionRequest.inject`,
unfiltered as an attempt's are): `CONDUCTOR_BRAKEMAN=<request id>`; and the
account as the adapter's `ConfigDirEnv` in `Env` (the launch already
installs the skill under that folder, `InstallSkillIn`). The run id and the
session id are written into the request (`running`) once `LaunchNamedWith`
returns; the session routes find a session's request by its run's
`Request` and serve it while the request is `starting` or `running`, so a
member that calls before that write is served. A launch that fails writes
`failed` with the engine's words. A Brakeman run is an ordinary run: the
run page, the sidebar's run block (labelled "Brakeman · …"), Stop, the
records; it is never a managed run, so the ledger holds nothing of it and
the 409 `managed_run` boundary is untouched. The desk knows its runs by
their class and request, after a restart too, and a person's crew named
like it is never taken for one. The engine keeps Brakeman's runs apart
from the person's: `evict` and the records' `prune` bound each class on
its own, the default class by `maxRuns` (100) and `maxRecords` (500) as
today, `brakeman` by 10 runs and 50 records, the oldest ended turn first,
so a day of turns never pushes a person's run out of the engine or out of
the records that `run:` refs and `GET /api/runs/{run}` read. Four refusals
keep a turn Brakeman's and never a scheduler: a run of class `brakeman` is
not resumed, takes no member (`POST /api/runs/{run}/resume`, `…/members` and
`…/members/{name}/resume` answer `409 brakeman_run`, "ask Brakeman again")
and is never linked to a ticket (Dispatch's run link answers `409
brakeman_run`);
the crew store reserves `brakeman` beside `new` (`reservedID`, which no
derived id takes), so a crew a person names Brakeman is saved as
`brakeman-2` (one saved under that
id before PR-3 keeps it, and its runs list then shows the turns too, each
labelled); and a Brakeman session cannot form a crew or add a member to one
(`POST /api/sessions/{id}/crew` and `…/run/members` answer `403
brakeman_cannot`, and the MCP server does not list `form_crew` and
`add_member` there); and a Brakeman session reaches no ticket through the
session routes every run member has (`GET /api/sessions/{id}/ticket` and
its comment route answer `403 brakeman_cannot`, and the MCP server does
not list `ticket_read` and `ticket_comment` there), so even a run linked
to a ticket gives Brakeman's token no write.

`internal/agents` (PR-3a): `Adapter.ReadOnlyArgs func() []string` (above);
`Adapter.ModelArgs func(model string) []string`,
set for Claude Code (`--model <m>`) and Codex (`--model <m>`), each checked
against the CLI's `--help` on this machine in the PR and nightly by the
`recipes` tier; nil for every other adapter, so a model cannot be set for
them. `Adapter.AllowTools func(server string) []string`, set for Claude Code
alone (`--allowedTools mcp__conductor`, its rule for every tool of one MCP
server), checked the same way; applied only with `AllowConductorTools`.
Codex's handling of MCP tool approvals is read from the CLI in the PR and
written into `docs/features.md`; without a verified flag, Brakeman under
Codex asks before each Conductor tool (yolo is never applied to a turn),
and Settings says so.

**The watchdog**: the deadline (`TurnMinutes` after `starting`) covers
`starting` as well as `running`. The launch runs under a context the desk
cancels at the deadline, on a person's cancel and when Brakeman is turned
off; the request is written `timed_out` or `cancelled` first, then the run
is stopped (`Engine.Stop`). A run that `LaunchNamedWith` returns after its
request closed (the launch finished as it was cancelled, its prompt
perhaps already on its way) is stopped at once and its id written into the
closed request as `strayRun`; one whose launch never returned is found by
its class and request through the session change hook and stopped the same
way. A member session that ends with no brief makes it `ended` (the desk
reads the session's end from the server's session change hook, the one
that feeds `Engine.OnChange`), unless the agent never read its request
(no call to the request route), which makes it `failed` with the
session's exit and last line ("the agent exited before it read its
request: is it logged in?"), so a logged-out agent is told from a turn
that read and gave up.

The slot (one turn at a time) is held from `starting` until the turn's
processes are confirmed gone, not until its request closes and not until
its run reads as ended: `Engine.stop` sets `stoppedAt` and calls `noteEnd`
even when a member's `Local.Stop` fails, and `Local.Stop` marks the
session `stopped` whatever its process did, so neither the run's state,
`OnEnd` nor the session's status proves the process exited. The slot is
released only when both hold: the turn's `launchTurn` has returned (no
launch outstanding that could still start a member), and every member
session of the turn's run reports its process reaped through
`Local.ProcessDone()` (new, PR-3c, `internal/session`: the
`session.Process`'s own `Done()` channel, closed when the process is
waited for). `state.json` names the run draining, and the next queued
request waits for it. A stop is retried
every 10 s, and a retry reaches the process. Today it would not:
`Local.Stop` returns nil at once for a session whose status has ended, and
it marks the session `stopped` even when `Process.Stop` failed, so a
second `Engine.Stop`, the watchdog's or the run page's, would signal
nothing. PR-3c moves `Local.Stop`'s guard from the status to the process:
it returns nil at once only once `ProcessDone()` is closed; while the
process is not reaped every call runs `Process.Stop` again
(`pty.Process.Stop` may be called again: it signals the group and waits
once more) and answers its error, the status staying `stopped` as now (a
stop never makes a session running again). `Engine.stop` reaches each
member through the registry, so the run page's Stop retries the process
too; the desk retries through `Engine.Stop` and, for a member the registry
no longer lists, through the `*Local` it keeps for `ProcessDone`. After six
failed retries the desk takes no new turn (`503 brakeman_stuck`, "the last
turn's run did not stop: stop it from its run page") until every member's
process is reaped (a later retry, or the run page's Stop, succeeding); at
the server's shutdown the run ends as every run does, and a restart finds
it interrupted. Tests (PR-3c, `internal/session`):
`TestAStopRetriedAfterAFailureReachesTheProcess` (a `session.Process`
double whose first `Stop` fails and whose second succeeds and closes
`Done`: the second `Local.Stop` calls it and answers nil, and the desk's
slot is then released), `TestAStopAfterTheProcessIsReapedSignalsNothing`.

**Admin routes** (`brakeman.go`), every one `requireAdmin` and a
switchyard's 403:

| Route | Body and answer |
|---|---|
| `GET /api/brakeman` | `{config, current, queue, turnsToday}` |
| `PUT /api/brakeman/config` | `{rev, config}` → `{config}`; `409 stale_revision`; `400 invalid_config` with the field's words |
| `POST /api/brakeman/requests` | `{opId, kind, subjects, note, since}` → `201 {request}`, `200` for the same `opId` and content, `409 op_conflict` for other content; `409 brakeman_off`, `429 queue_full` (8 waiting), `429 budget_spent` (the day's turns), `400` for a bad kind, subject or note |
| `GET /api/brakeman/requests?after=&limit=` | a page of requests, newest first, without their briefs' items |
| `GET /api/brakeman/requests/{id}` | the request whole, with its brief and what was made from it; at most 512 KiB (its two documents' bounds) |
| `GET /api/brakeman/requests/{id}/refs` | the brief's resolved refs (`seen` and `now`), from `br-<n>-refs.json`; at most 4 MiB |
| `POST /api/brakeman/requests/{id}/cancel` | `{opId}` → `{request}`; a closed request answers as it stands |

**Session routes** (`brakeman_session.go`), the Brakeman session's own
token only (`agentTokenOK`) for the request its run names, while it is
`starting` or `running`. The brief's route looks its `opId` up first: the
same `opId` and brief answer `200` with the request whatever its state now
(a lost reply asked again after the request became `briefed`); only a new
submission is held to the state, the bounds and the refs. The admin's token is refused for the brief (`403
agent_only`, as a task result is: a brief is always Brakeman's own) and
taken for the reads, so a person can inspect what Brakeman was given:

| Route | Body and answer |
|---|---|
| `GET /api/sessions/{id}/brakeman/request` | `{request}`: kind, subjects as resolved, note, since, the last complete briefing's coverage, the scope and visibility it may use, the budget left; `404 not_brakeman` for another session |
| `POST /api/sessions/{id}/brakeman/brief` | `{opId, brief}` → `201 {request}`; the same `opId` and brief `200`; another brief `409 brief_exists`; `422 bad_refs` naming each ref that resolves to nothing or lies outside the scope or visibility; `400 invalid_brief` with the item's index and why; `413 bound` past 256 KiB as `store.Encode` writes it, or with resolutions past their 4 MiB document; nothing written on a refusal |

**Resolution** (`brakeman_refs.go`): each ref of a submitted brief is
looked up in the record it names (the ledger's reads, Dispatch's
`Ticket`, `Comment` and `Receipt`, the registry and the session's
attention, the touched index, git through `internal/gitcli` for a
commit, the desk's own requests) and checked against the request's scope
and visibility. Every answer a turn reads (PR-4) is noted in the
request's read log (`brakeman/requests/br-<n>-reads.json`, a name
`internal/store` takes: each item's ref, its visibility class, revision
or state, and time; at most 5,000 entries and 2 MiB, the oldest dropped
and the drop said), and each ref of the brief is kept, in
`br-<n>-refs.json`, with two
snapshots of `{ref, url, state, rev, sha256, at}`: `seen`, what the turn
read (from the log; `unobserved` for a ref it never read, which
resolution then reads), and `now`, what it is as the brief is kept. A
later reader sees what Brakeman read, what was true when it wrote, and
whether it has changed since. A `decision/1`'s `subjectRevision` and a
proposal's `expectedRevision` must equal their target's `seen` revision
(`422 bad_refs`, "you read revision 7"), and Conductor's controls send
that revision to a route that checks it itself, so advice from an older
read is refused by the backend, never applied to a newer state; an
operation whose route checks no revision gets no control (PR-7). A ref to something deleted since is shown as such, never
dropped.

**The `brakeman` event** on `GET /api/events`: `{id, state}` after each
change of a request's state, at most 200 bytes, state a page cannot do
without (like `run` and `dispatch`), never a webhook. `docs/protocol.md`
gains a Brakeman section: the routes above with their bounds and refusals,
the event, the request's states, the brief's schema and the refs.

### `internal/api`: what Brakeman reads, and drafts made work (PR-4)

Brakeman reads through the routes every client reads, not a second set
of its own. A request whose bearer is a turn's session token and whose
`X-Conductor-Session` header names that session (the session's
`AgentTokenOK`, its run's request `starting` or `running`) authenticates
as the principal `brakeman`, and `s.reader(w, r)` (new) takes the admin
or that principal on exactly these existing reads, each answered by its
own handler, the principal's reply filtered by `Scope.Admits` and the
visibility and counted against the request's `ReadsPerTurn` (`429
reads_spent` past it), every item carrying the ref a brief cites it by:
`GET /api/runs` and `GET /api/runs/{run}` with its tasks', attempts',
commands' and messages' reads (runs), the step log route with its
`X-Conductor-Log-SHA256` (check logs), `GET /api/dispatch/projects`,
`…/tickets`, `…/tickets/{key}`, `…/history` and `…/comments` (Dispatch),
`GET /api/sessions` (sessions) and `GET /api/touched` (touched files).
Each keeps its own bound (a ticket's read its 3 MiB, `maxTicketReply`,
with its comments past the last 50 through `…/comments`), which the guide
lists; the admin's answers do not change. Every other admin route
refuses the principal. What no route serves today is new and typed, the
admin's too, under `GET /api/brakeman/`, each answer at most 256 KiB:

| Route | Query | Answer |
|---|---|---|
| `…/changes` | `since` (a time) or none (the last complete briefing's watermarks), `cursor`, `limit` (at most 200) | what changed: runs launched, ended, paused or continued; tasks that changed state, with how Conductor knows (below); questions opened and answered, from each listed session's activity window, with a `gap` where the window starts after `since` or the session is gone; messages; tickets created, edited or closed, and comments added or edited, from Dispatch's change sequence (below); each with its ref and time; `next` for the rest |
| `…/status` | `run` or `session` | each task's, or the session's, status as Conductor computes it (below) |
| `…/screen` | `session`, `lines` (at most 200) | a server session's screen's last lines; `403 not_visible` unless terminals are visible; `409 unavailable` for a hosted session, whose screen the server does not hold |
| `…/overlaps` | none | paths changed in two or more live attempts' or sessions' git work trees, each tree's committed changes since its base (`gitcli.ChangedSince`, new: `git diff --name-only -z <base>...HEAD`) with its working tree's (`gitcli.GetStatus`), at most 32 trees and 2,000 paths, each with the refs |
| `…/briefs` | `q`, `after` | earlier briefs, title, summary and items' refs, newest first, at most 20 a page |

The MCP tool `observe` (PR-5) and `conductor brakeman observe` send each
`what` to its route by the guide's table, on the client side: the server
has no dispatcher of its own.

**What a briefing covers is the server's record.** The desk notes, per
turn, how far the `changes` pages it delivered reach: for each run the
last event seq delivered with nothing skipped before it, Dispatch's
change seq likewise, each session's window. A page cut by its limit and
not followed, a run marked `truncated` or a window with a `gap` leaves
that part `partial`. The request's `Coverage` is that record, never the
agent's word, and the next briefing starts from each part's last complete
watermark, so pages Brakeman never read are read again. A run, project or
session newly in scope (the scope widened) has no watermark and starts at
the earlier briefing's time, labelled newly in scope.

**How Conductor knows, computed, never inferred by the agent.** Each task
and session in `…/changes` and `…/status` carries `status`, one of:
`failed` with the receipt that proves it (a check's non-zero exit or
timeout, a refused result, attempts spent); `blocked` with the blocker the
attempt reported; `waiting_on_person` with the open question; `quiet` with
how long the terminal has written nothing (`LastOutputAt`, at least 10
minutes) and `proof: none`, for a server session only (a hosted session's
output is the host's, so its quiet time is `unknown`, never `quiet`);
`running`; `succeeded` with its result commit;
`stopped` or `interrupted` with the record's words. Brakeman's brief can
then tell inactivity from proven failure because Conductor already did, and
its fact cites the receipt or says there is none.

The ledger gains one read, no schema change: `Ledger.LastEvent(ctx, run)
(int64, error)` (`SELECT MAX(seq)`), so `changes` reads each run's events
after the last briefing's watermark, or, given a time, the newest 5,000 of
a run's events (`Events` after `LastEvent - 5000`) filtered by their time
in Go; a run with more since then is marked `truncated`. Dispatch gains
one read, no schema change: `Store.ChangesAfter(ctx, after, limit)` over
its `changes` table by `seq`, every project's, so a comment added to or
edited on an old ticket is seen, which a ticket's `updatedAt` does not
record (a comment's change bumps no ticket); given a time, the newest
5,000 changes filtered by their time, `truncated` past them.

**Drafts made work, by a person** (admin): `POST
/api/brakeman/requests/{id}/items/{i}/make` with `{opId, by, edit}`, `edit`
the form as the person changed it (the same shape as the draft):

- **Derived operation ids.** Dispatch keys an operation by `(actor,
  opId)` across every client, and its `OpIDPattern` and the ledger's
  `CommandIDPattern` are both `^[A-Za-z0-9._~-]{1,32}$`, so an id the desk
  passes on is never a client's id with a suffix. It is
  `bk` + the first 30 hex digits of the SHA-256 of its parts joined by
  NUL (32 characters): a make's `derive("make", request, item, opId)`, an
  edge's `derive("edge", request, from, to)` (the same whichever end is
  made last, and never another request's), BK2's act
  `derive("act", request, opId)` for Dispatch, the ledger's commands and
  DR1 alike. The request records the derived `op` beside the client's
  `opId`. Tests (PR-4): `TestDerivedOpIDsFitTheStoresPatterns` (32-character
  inputs), `TestTwoRequestsWithOneBriefOpIDMakeTwoRelations`,
  `TestARepeatedMakeDerivesTheSameOp`.
- **One make of an item at a time: the claim.** `Store.Update` makes each
  request write atomic, not the make's whole sequence (its input, its
  claim, the backend's operation), so the desk holds a mutex per request
  across the input and the claim, and the claim decides. Only the server
  holding the owner lock serves a make (`503 brakeman_not_owner`
  elsewhere), so an in-process mutex is the whole of it. Under it the desk
  reads the request and, for the item: made already answers from its
  receipt for the same `opId` and `409 already_made` with its key or crew
  for another; a pending claim under another `opId`
  answers `409 make_pending` with that `opId` and writes nothing; a
  pending claim under the same `opId` compares the SHA-256 of the
  canonical input with the claim's `input` digest and, equal, rejoins the
  make (the same derived `op`, which the backend answers once), else
  answers `409 op_conflict`, the input document left as it was; no claim
  (or a `refused` one) writes the input to `br-<n>-make-<i>.json` and
  then claims, in one `Update`, `Made{item, pending: opId, op, input}`.
  So no two `opId`s ever run one item's operation, and a claim's input is
  never rewritten while the claim stands. The mutex is released before the
  backend's operation; the outcome is written after it by `Update` on the
  claim it names. Test (PR-4): `TestConcurrentMakesOfOneItemMakeOne` (a
  gate after the first make's claim, before Dispatch commits: a second
  `opId` answers `409 make_pending`, the first `opId` with other content
  `409 op_conflict` with the input unchanged, the first `opId` with the
  same content rejoins; one ticket, and the same for a draft crew).
- a `draft_ticket/1` becomes a ticket through Dispatch's own operation
  (`Store.CreateTicket` under the actor `admin`, the person's `by`, the
  derived `op`); the desk writes the canonical input (project, title,
  description, acceptance, priority and the display name, as the person
  edited them) to `br-<n>-make-<i>.json` and then claims the item
  (above) before the store's operation, and `{key, receipt}` after it
  (recovery: rule 5 of Events, orderings and invariants); a repeat with
  the same `opId` answers from Dispatch's receipt, or from `OpTicket` once
  that is pruned, and an item already made answers `409 already_made`
  with its key. The brief's `blocks` are written into the request as
  edges (`Edges: {from, to, pending, op}`) by the first make, and every
  make then adds each pending edge whose two ends are tickets now
  (`AddRelation` with the edge's derived op), so A then B and B then A
  give the one relation; an edge is marked made after Dispatch's receipt;
- a `draft_crew/1` becomes a saved crew through the crew store's
  `CreateFrom(c, origin)` (new), as `POST /api/crews` saves one (validated
  again): the crew document gains `origin`
  (`brakeman:<request>/<item>/<op>`), which only `CreateFrom` sets, so at
  most one crew holds an origin: `Update` keeps the stored crew's origin
  whatever the body holds, and `Create` and `Duplicate` clear it (today
  `Duplicate` clones the source and changes only its id, name, times and
  manifest revision, which would give the copy the origin too: two crews
  answering one make, and deleting either writing the tombstone of both);
  `Put`, which only tests call, stores what it is given. A create whose
  origin a crew holds answers that crew. Deleting a crew
  that carries an origin first writes its **origin tombstone**
  (`crew-origins/<first 32 hex digits of the origin's SHA-256>.json`, a
  `store.Sub` beside the crews, `{origin, crew, deletedAt}`) and only then
  removes the crew's document, so whichever server deletes it, a crew
  with an origin is never gone without its tombstone; `CreateFrom` with
  an origin that a tombstone holds answers `deleted` and saves nothing.
  The request records the input (`br-<n>-make-<i>.json`, the crew as the
  person edited it) and the item's claim (above) before the save
  and `{crew, rev}` after; a crew is never launched by this route. Tests
  (PR-4): `TestADuplicateOfAMadeCrewCarriesNoOrigin` (the save, a crash
  seam before `made`, the crew duplicated and the duplicate deleted: no
  tombstone is written, and recovery finds the original by its origin),
  `TestCreateClearsAnOriginInItsBody`,
  `TestADraftCrewDeletedBeforeMadeIsNotRecreated` (two servers on
  one data directory: the save, the second server's delete, then the
  crash seam before `made`; recovery marks it `deleted`),
  `TestAPendingTicketIsCompletedFromItsInput` (a crash seam before
  Dispatch commits: recovery creates it once, with the person's edits and
  name), `TestAPendingTicketWithAPrunedReceiptFindsItsKey`,
  `TestARefusedPendingTicketCanBeMadeAgain`;
- every other kind answers `400 not_a_draft`.

Brakeman's own token reaches none of these: it reads the routes above and
writes only its brief.

### `internal/cli` and `internal/agents`: Brakeman's tools and command (PR-5)

MCP tools, listed only in a session whose environment has
`CONDUCTOR_BRAKEMAN` (set by the server, PR-3c): `brakeman_request` (the
request), `observe` (`what` and its query, one tool for the reads of
PR-4, its schema a `oneOf` from the guide's table, each `what` sent to its
own route), and `brief_submit` (`opId`
minted by the tool when absent, and `brief`; a reply lost to the network
is asked once more with the same `opId`, as the Dispatch writes do).
`conductor brakeman request|observe WHAT [--k v]…|brief --file F` in a
session, the same calls for an agent without MCP; and with the workbench
token, `conductor brakeman ask KIND [--subject REF]… [--note-file F]
[--since T]`, `status`, `list` and `show br-n [--json]`.

`internal/agents`: `codexMCPEnv` gains `CONDUCTOR_BRAKEMAN` (Codex starts
`conductor mcp` with only the names it lists, #354); the skill gains one
short section, "If you are Brakeman" (six lines: `CONDUCTOR_BRAKEMAN` is
set; call `guide` first; read before you write; every fact cites evidence;
nothing you write becomes work; one brief, then your turn ends), and nothing
else, so no agent carries a large static prompt. In PR-6, the UI surface
whose specs run it, `web/e2e/stub-agent.sh`
gains `STUB_BRAKEMAN=1`: on its request line the stub reads its request with
`conductor brakeman request`, observes `changes`, and submits a brief whose
fact cites its own session and run (and a check receipt when
`STUB_BRAKEMAN_CHECK` names one), through `STUB_WORKER`'s CLI or MCP path.

### `web/app`: Settings, the Brakeman page and a brief (PR-6)

- `composables/useBrakeman.ts`: the config, the requests (a page, then the
  `brakeman` events), `ask`, `cancel`, `make`; ids minted by the browser and
  a lost reply asked once more with the same id (`utils/tasks.ts`'s rule).
- `utils/brakeman.ts` (pure, vitest): the kinds' words, the states' words
  ("queued", "running in <session>", "timed out after 15 min", "the session
  ended with no brief"), a ref to its route and label, a brief's items
  grouped in the order facts, interpretations, decisions, proposals,
  outcomes, drafts, handoffs, overlaps, evidence; the config form's checks
  mirroring the server's.
- `components/BrakemanSettings.vue` (a card on `pages/settings.vue`): the
  fields of the configuration; agents offered are those whose adapter
  registers MCP; the model field only for an agent with a model flag; the
  account folder through `DirInput`; terminals off with its words ("lets
  Brakeman read what a session's screen shows").
- `pages/brakeman.vue`: the composer (`BrakemanAsk.vue`: a kind, subjects
  picked from runs, tasks, sessions and tickets, a note, a since), the
  request in progress with its session's link and Stop, the queue, and the
  briefs newest first.
- `components/BrakemanBrief.vue`: a brief: the provenance line (agent,
  model, version, request, as of), the title, "Brakeman's reading" (the
  summary), then `BrakemanFact.vue` (text and evidence chips, each chip a
  link labelled by `utils/brakeman.ts`, marked "changed since" when its
  resolved `rev` or state differs from the live one the page has),
  interpretations ("reads facts 2 and 3"), proposals as text with the
  operation named (their controls are PR-7's), outcomes with their receipt,
  comparison and evidence panels.
- The sidebar's foot gains a Brakeman row (`data-sidebar-brakeman`) with
  the count of briefs not yet opened, kept per browser; no toast, no dialog,
  no navigation when a brief arrives.

### `web/app`: decision cards, drafts, handoffs and the entry points (PR-7)

- `components/BrakemanDecision.vue`: the card's text from the brief, and
  the controls from the live record of `about`, read through the existing
  routes: for an open question, its options as the session reports them and
  "Answer in the session", which opens the session page at its reply bar (the
  one place a question is answered, with the control role); for a failed or
  blocked task, the commands its state allows, by the run page's own rules
  (`taskActions` in `utils/tasks.ts`, which follow
  `internal/crew/commands.go`): a failed task Retry, and Accept anyway
  (asked first, as on the run page) only for a work task with a submitted
  result; a blocked task Cancel; sent through `useTaskCommands` with
  `expectedRevision` the run's revision the turn read (`seen`), so a run
  that moved since is refused `409 stale_revision` and the card says what
  changed; for a ticket, its page. The
  card says who may answer ("you, the operator"; DR1 and HM1 add named
  people) and what answering authorizes, from the brief.
- `components/BrakemanProposal.vue`: a proposal's text and its operation
  as a button only for the operations whose route itself refuses a stale
  revision (`Operation.Revision` set): a run's pause and unpause and a
  task's cancel, retry and accept (the task command body's
  `expectedRevision`, which `internal/crew/commands.go` checks under the
  run's lock), a ticket's edit, comment and link (DS1's `expectedRev`),
  sent through the same client calls as their own pages with the
  proposal's revision, asked first where the page asks first (Accept
  anyway, Cancel). A run's stop and resume (`handleStopRun` and
  `handleResumeRun` decode no body and check no revision), a crew's create
  and launch, and any other operation are shown as text with a link to
  the page that does them, where the person acts on the live state; a
  client-side comparison would leave a check-then-use race, so none is
  offered. Adding a revision check to stop or resume is not this plan's
  (stop is a safety control that must always work).
- `components/BrakemanDraftTicket.vue` and `BrakemanDraftCrew.vue`: the
  draft as an editable form, Create (the `make` route), and once made the
  ticket or crew it became, linked; a crew made is opened in the crew
  editor, never launched.
- `components/BrakemanHandoff.vue`: the package as Markdown, Copy, and
  "Comment on the ticket" for a ticket in its refs (Dispatch's comment
  operation as the admin, the person's name).
- Entry points: the palette gains a Brakeman group (`usePaletteRows`: "Ask
  Brakeman: brief me since…", "…explain this run", "…this task", "…draft
  tickets from this ticket", "…propose a crew for this ticket", "Open
  Brakeman"); the run page's task actions (`taskActions`) gain "Ask
  Brakeman why"; the ticket page's menu gains "Draft tickets" and "Propose a
  crew" once DS1's PR #501 and PR #503 have merged (the file they change).

### Docs (PR-8)

`docs/features.md` gains the first slice's entry: what each PR verified and
the by-hand turn (Acceptance). The README gains a Brakeman paragraph;
`docs/repository-map.md` a row for `internal/brakeman` and the desk;
`docs/architecture.md` a Brakeman section (a turn is a run; the guide; the
records). `docs/protocol.md` is each PR's own.

### Later PRs: DS2's records, BK2's actions, BK3's role (PR-9 to PR-18)

**PR-9 (C9's providers).** Once C9's `GET /api/capabilities` (#488) has
merged, the guide's `providers` section is that route's answer (`known:
true`, each provider's kind, name and state) for the admin and for a
session, and `host` gains what C9 reports beside it; a plain build answers
`{"known": true, "providers": []}`. The guide still calls nothing of `ext`
itself and branches on nothing (article 1).

**PR-10 and PR-11 (DS2's records).** Once DS2's plan revisions, launches
and candidates are served (its PR-6 to PR-8, #463 to #465): the refs
`plan:`, `launch:` and `candidate:` resolve; `observe` gains `plans`
(a ticket's revisions, the approved one, whether it still holds) and
`candidates` (a run's candidates with their receipts, as DS2's route
reads them); a `draft_plan/1` item drafts a revision (a saved crew with
tasks, the inputs, the decisions a person will make), which the person
proposes through DS2's own propose route ("Propose as plan revision"),
then approves as DS2 requires of the operator; a `decision/1` about a
`plan:` ref renders DS2's Approve as its control. PR-11 is the pages'
part, once DS2's ticket pages (#467, #468) have merged.

**PR-12 and PR-13 (BK2, authorized actions).** Once DR1 and RV1 serve
their operations and BK1 is Done:

- `Permissions.Act` names grants from a closed list, each one operation:
  `launch_approved_plan` (DS2's launch from a revision the operator
  approved and that still holds), `request_repair` (RV1's Request changes
  on a candidate, a Yardmaster repair transition), `prepare_followup`
  (DS2's proposal of a follow-up revision from the approved one, which a
  person still approves), `route_decision` (DR1's routing of a decision
  record to the operator; to a human member's label once HM1 serves them,
  PR-18; never its answer). Only the admin's configuration route writes
  grants; no route Brakeman's token reaches does.
- No act route of Brakeman's own: each granted operation is sent to that
  operation's own typed route (DS2's launch from a revision, #464; RV1's
  request changes; DS2's follow-up proposal; DR1's routing), which takes
  the `brakeman` principal (PR-4) when the configuration grants it that
  operation, checks the op is in scope and its target in the state the
  operation requires, and runs its one handler under the actor
  `brakeman`, its idempotency key the derived `derive("act", request,
  opId)` (Drafts made work: 32 characters, never colliding across
  requests), answering its own receipt, which the request
  copies. The launch goes through `ext.Host` (`Host.LaunchApproved`,
  declared by PR-17 and wired by PR-12 to DS2's launch function, never
  `launchSaved`, which launches a saved crew and not an approved
  revision). Approving a plan revision, answering
  a decision, changing a gate, accepting a task, editing the
  configuration and every Dispatch close are not operations it can be
  granted; DS2 refuses an approval by any actor but the operator, DR1 an
  answer by any but the approver.
- Visibility and grants stay two fields checked apart: a grant gives no
  read, a read gives no grant; with visibility narrowed, an act whose
  target is no longer visible is refused.
- PR-13: the MCP tool `act` and `conductor brakeman act`, which send each
  operation to its route by the guide's table; Settings' grants;
  a brief's proposals that Brakeman ran show "Brakeman did this" with the
  receipt; the guide lists the granted operations.

**PR-14 and PR-15 (BK3, the background role; O2's default, Owner decisions,
2).** Yardmaster's engine gains a role loop (`internal/crew/role.go`),
generic: it knows a role's lifecycle, wakes, window, cooldown and budget,
and asks an interface, `crew.RoleRecords`, for its records and
`Fire(batch) error` to start a turn. The desk in `internal/api`
implements both over `internal/brakeman`'s store, so `internal/crew`
imports neither `internal/brakeman` nor `internal/api` and
`internal/brakeman` imports no `internal/crew`. A role is a record, not a
process: an identity (`brakeman`), its preferences,
its durable notes (at most 64 KiB, kept by Conductor: a brief's fact or
interpretation the person chooses to keep, readable and clearable on the
page, never something Brakeman writes there by itself), and its lifecycle,
`idle`, `gathering` (a wake source fired; the batch window open),
`briefing` (a turn running), `cooling` (until the cooldown ends), `paused`
(by a person), `unavailable` (the agent cannot start: not installed, its
login refused, the budget spent), each change written before what it
starts. Wake sources, all event-driven, none a poll of a model: a task
failed with proof, a question open longer than a threshold, a run ended or
paused, a ticket moved to ready, a person's explicit request, and
configured briefing times in the server's local zone. No source fires for
Brakeman's own runs and sessions (class `brakeman`) or for what a turn's
request made, so a briefing never wakes the next. Each wake is a row in
the role's log; the batch window (default 120 s, 10 to 900) gathers the
wakes that fire in it into one batch (`wb-<n>`, its id written to the
role's record when it opens), fired as one request of kind `wake` naming
the batch (`Batch`), not its wakes as subjects: the turn reads the batch
through its request, at most 200 wakes, the same kind and subject
coalesced with a count, past 200 an `overflow` count with the first and
last times; the cooldown (default 30 min, 0 to 1,440) holds the next batch;
the budget (turns a day, minutes a day) refuses a turn and says so.
Minutes are the day's charges of PR-1 (The store), whatever started the
turn: `Budget.MinutesPerDay` (PR-14: 10 to 1,440; 240) is admitted at the
dequeue, where the reservation is `min(TurnMinutes, MinutesPerDay -
MinutesToday)`; under 2 minutes left closes the request `over_budget`
("the day's minutes are spent"); otherwise the turn's deadline is its
start plus the reservation, so a turn never runs past what the day has
left (one minute left never buys fifteen). At the close the charge is
settled to the minutes run; a crash before that keeps the reservation as
the charge; a turn is charged to the day it started, so midnight neither
splits nor refunds an in-flight charge, and the new day's allowance is
whole. Tests: `TestATurnIsBoundedByTheDaysMinutesLeft` (5 minutes left:
a deadline 5 minutes on), `TestTooFewMinutesLeftIsOverBudget`,
`TestAChargeIsSettledAtTheClose`, `TestARestartKeepsAnUnsettledReservation`,
`TestMidnightLeavesAnInFlightChargeOnItsDay`. A turn
is BK1's turn, unchanged: a fresh run of a fresh session that rebuilds
everything from the records. After a restart the role's state, its open
batch and its log are read back; a batch open when the server stopped is
fired once. While the role is unavailable or paused nothing else waits on
it: crews, decisions and inspection never depend on Brakeman. PR-15: the
role's controls on the Brakeman page and in Settings (background on or off,
pause, briefing times, window, cooldown, budget), its log, and its
unavailable state in words.

**PR-16** adds the features entries of BK0 to BK3 and the by-hand records
of each acceptance.

## Events, orderings and invariants

No SQLite schema changes: the ledger gains one read (`LastEvent`) and
Dispatch none; Brakeman's records are new JSON documents of their own
(schema 1). A record of a later schema is skipped, logged once and never
rewritten, so an older binary started on newer data loses nothing
(the conservative rule the ledger's migrations follow). No WebSocket frame
or control message changes; the one new Server-Sent Event is `brakeman`.

**A request's states.**

| State | Meaning | Goes to |
|---|---|---|
| `queued` | written, waiting for the desk | `starting`, `cancelled`, `over_budget` |
| `starting` | taken by the desk; deadline and configuration snapshot written; the launch under way (the engine types the prompt asynchronously, so the member may read, submit or exit before the launch returns) | `running`, `briefed`, `ended`, `failed`, `timed_out`, `cancelled`, `interrupted` |
| `running` | its run and session recorded; the session may read and submit | `briefed`, `timed_out`, `ended`, `failed`, `cancelled`, `interrupted` |
| `briefed` | a brief kept | none |
| `timed_out` | the deadline passed with no brief | none |
| `ended` | the session ended with no brief | none |
| `failed` | the launch failed (the engine's words), or the agent exited before it read its request | none |
| `over_budget` | at the dequeue, the day's turns were spent (after midnight, or a lowered `TurnsPerDay`) | none |
| `cancelled` | a person cancelled it, or turned Brakeman off | none |
| `interrupted` | the server stopped while it was starting or running | none |

Every other transition is refused by `brakeman.Request.To` (a table test
walks every pair). The last seven are closed: `closedAt` is set and
nothing of the request changes again but its `made` list, its edges, its
acts' outcomes (PR-12; no new act on a closed request), its
`strayRun`, and its `runId` and `sessionId` while they are still empty.
A close while `starting` (a brief, the member's end, a failure, a
deadline, a cancel) records the run and session the closing write knows
(the submitting session and its run's id; the ended session's
`CrewRef`), so nothing waits for the launch to return. The launch's
return writes `running` only through `Update` on a request still
`starting`; on a closed one it writes no state: it fills an empty `runId`
and `sessionId`, and, when the close was a deadline, a cancel or turning
Brakeman off, records the run as `strayRun` and stops it (a briefed or
ended request's run is the turn's own and is stopped by rule 3 or has
ended). Every ordering is a test (PR-3c).

**What is written before what.**

1. A request's number is written to `brakeman/state.json` before the
   request's file, and the file (`queued`) before the reply: a number is
   never reused and a reply never names a request that is not kept.
2. The day's charge for the request (`state.json`, keyed by its id) is
   written first, then `starting` with its deadline and snapshot, both
   before `launchTurn`; `running`, with the run and session, after it
   returns, and only if still `starting`. A server that dies between them leaves `starting`, which the
   next start makes `interrupted`; the engine's record of the run is
   interrupted at start as every legacy run's is, and the run, known by
   its class and request, is never resumed (the `brakeman_run` refusal).
3. A brief is validated, its refs resolved, and the request written
   `briefed` with the brief and the resolutions before the reply; the run is
   stopped after the reply, after a grace of 20 s so the agent's last output
   reaches its terminal. The slot stays held until the run is seen to end,
   a failed stop retried (the watchdog); the record is the truth.
4. A deadline, a cancel and turning Brakeman off write the closing state
   first and stop the run after.
5. A draft made work: its input (`br-<n>-make-<i>.json`) and then `made
   {item, pending: opId, op}` are written before Dispatch's operation (or
   the crew's save), and `{key | crew, receipt}` after it. A server that
   dies between them leaves the pending entry, which the desk's
   `recoverMakes` completes (the store, above) before Brakeman's routes
   serve; other servers' crew and Dispatch routes may still run, so each
   case is decided from a durable record, never from "nobody touched it".
   A ticket: Dispatch's receipt for `(admin, op)` done gives its key; a
   refused receipt marks the entry `refused` with its words (the item can
   be made again with a new `opId`); no receipt, but `OpTicket(admin, op)`
   names a ticket (the receipt pruned), gives that key; neither, the
   operation never ran, and `CreateTicket` runs again from the recorded
   input with the same op id, which Dispatch answers once. A pending edge
   the same way by its op id (`AddRelation` again from the recorded ends).
   A crew: the crew holding its `origin` gives it; an origin tombstone
   (the crew store, Drafts made work) says it was saved and then deleted,
   and the entry is marked `deleted`, never saved again; neither, the
   save never happened, and the recorded input is saved under the origin.

**Invariants.**

- At most one request is `starting` or `running` (the desk's mutex); the
  queue holds at most 8 and is taken in number order.
- A request's `opId` (the admin's) names one request: the same content
  again answers it (`200`), other content `409 op_conflict`;
  `state.json` keeps the last 1,000 `opId`s with their request.
- A brief is kept once. Its `opId` again with the same brief answers it;
  any other brief `409 brief_exists`, whatever the request's state. Only
  the session of the request's run submits a new one, only while
  `starting` or `running`, only with its own token.
- Every fact names at least one ref; every ref of a kept brief resolved,
  inside the request's scope and visibility, when it was kept; what the
  turn read (`seen`) and what was true then (`now`) are kept beside it
  and never rewritten.
- A turn's reads use the snapshot taken at `starting` intersected with the
  configuration now: a person narrowing what Brakeman may see takes effect
  at once; widening takes effect at the next request.
- Brakeman's token reaches the routes any session's token reaches today
  (its own attention, reports and view link) but the ticket routes, the
  guide, its request and brief routes, and as the `brakeman` principal
  the reads of PR-4; it is refused by every other admin route, by the
  self-crew routes, by `…/ticket` and its comment route whether or not its
  run is linked, and, until PR-12's grants, by anything that changes a
  run, a ticket or a crew. A test walks every route of the mux with it,
  each with the answer it must give.
- The day's budget: a request is refused at creation when the turns
  started today plus those queued reach `TurnsPerDay`; a turn counts once
  its charge is written (before `starting`), in `state.json`'s `days`,
  keyed by its request id, kept apart from the requests, so no prune or
  cancel lowers it and no crash counts it twice; the dequeue checks the
  day's count (and, from PR-14, its minutes) again and closes a request
  that would pass it as `over_budget`.
- A question a Brakeman session asks is shown in Needs you labelled
  "Brakeman · br-n"; it reaches browser notifications, Chime and webhooks
  only when the request's kind is not `wake`, so a background turn pages
  no one. The classification is the session's own, set before it can ask
  anything: `Server.Launch` writes `crew.brakeman: {request, quiet}` into
  the session's `CrewRef` from the `LaunchSpec` (PR-3c), at creation, so
  every snapshot of the session that can need input already carries it;
  `quiet` is true only for a `wake` request (PR-14), false before. The
  browser does not take its alerts from the server's sinks:
  `useAttention.ts`'s `react` notifies and chimes from newly-needing-input
  snapshots, so it reads the mark itself and skips a `quiet` session's
  notification and Chime while Needs you still lists it (PR-6, before any
  background turn exists: `useSessions.ts`'s `SessionInfo` gains the
  field, `utils/attention.ts` gains the pure filter `react` applies to
  `newlyNeedingInput`'s list, and vitest `utils/attention.test.ts` holds a
  quiet session that newly needs input counted in Needs you and not
  notified or chimed, and a person's request's session notified); the
  webhook sink skips it on the
  server (PR-3c). PR-14 sets `quiet` and its Go test
  (`TestAWakeTurnsSessionIsMarkedQuiet`); PR-15's `brakeman-role.spec.ts`
  checks a wake turn's question raises no browser notification. The
  sessions JSON gains the field in `docs/protocol.md` (PR-3c).
- Nothing Brakeman submits changes a ticket's status, a run's state, a
  task's outcome, a verification or an acceptance: those stay separate
  facts with their own owners (the Program, section 1.4), and a brief only
  cites them.

**The `brakeman` event**: `{id, state}`, sent after each state is written,
at most 200 bytes (an id of at most 12 bytes and a state of at most 12),
measured by `TestTheBrakemanEventIsBounded`. Like a `run` event it is state
a page cannot do without: a client that cannot keep up is dropped and
reads again on its next snapshot.

**BK2 (PR-12).** An act first looks up its idempotency key: a receipt the
operation's store holds for it answers the repeat, whatever the target's
state now, so a retry after a lost reply is never refused because the act
itself moved the target. A new act is checked, in this order, against: the session
being the running request's; the grant (snapshot intersected with now);
the scope and visibility of its target; the target's state as the
operation requires (the revision `expectedRevision` names, or the
operation's own state check). Then, before the operation runs, the desk
writes the act pending into the request, under the desk's per-request
mutex (Drafts made work) and in one `Update`: `Act{opId, operation, target, run, op, args, pending}`
(the client's `opId`, the operation's name, the target ref, the target's
run where the operation's receipt is keyed by one, as the ledger's
commands are, the derived key `derive("act", request, opId)`, and the
SHA-256 of the arguments; at most 600 bytes, at most 20 a request, `409
acts_spent` past them). The same `opId` with the same operation, target
and arguments rejoins its entry; other content answers `409 op_conflict`.
Only then it runs through the operation's one function under the actor
`brakeman` with the derived key as its idempotency key, whose receipt is
written by the operation's own store (the ledger's commands, Dispatch's
ops, DR1's records) and copied into the entry after. A crash between the
two leaves the entry pending, and the derived key is a digest nothing can
be read back from, so the entry is the correlation: the desk's
`recoverActs`, run with `recoverMakes` before Brakeman's routes serve,
asks each pending act's store by what the entry holds (Dispatch's receipt
for `(brakeman, op)`; the ledger's command receipt for `run` and `op` as
the command id; DR1's record by `op`). A receipt is copied, done or
refused, with its words. None means the operation never committed: the
entry is marked `not_run` ("the server stopped before it ran") and is
never run by recovery, since its turn has ended and the grant was that
turn's. A request with a pending act is pinned against `Prune` (The
store). Tests (PR-12): `TestAnActCommittedBeforeItsCopyIsCompletedAfterARestart`
(a crash seam after the operation's commit, before the copy),
`TestAnActThatNeverRanIsMarkedNotRun` (a seam after the pending write,
before the operation), `TestAnActsOpIDNamesOneAct`. No
operation in the grant list changes an approval, a gate, a decision's
answer or the configuration.

**BK3 (PR-14).** The role's lifecycle, written to `brakeman/role.json`
before what each change starts: `idle` → `gathering` when a wake source
fires (the wake appended to the role's log, at most 1,000 kept) → after
the window, a request of kind `wake` is created with the op id
`wake-<batch>` (rule 1 above; a create again with it answers the request
already made) and the role is `briefing` → when that request closes,
`cooling` until the cooldown ends → `idle`. `paused` from any state by a
person (a request already running runs to its end); `unavailable` when a
turn cannot start (the request `failed`: a launch refused, or an agent
that exited before it read its request, as a logged-out CLI does), until
a person's request or the next configured briefing tries again. Wakes
that fire while `briefing` or `cooling` join the next batch. An invoked
request and a wake batch share the one queue. At start the role is read
back: `gathering` keeps its open batch and, once its window has passed,
creates its request with the same op id, which finds the one a crash
left between the request's write and the role's, so a batch fires once;
`briefing` follows its request, which rule 2 made `interrupted`, to
`cooling`. A `wake` turn's own question notifies no one (the invariant
above), and its deadline ends it unanswered. The engine's loop holds no process between
turns and polls no model: it is woken by the activity sink, the engine's
run changes, Dispatch's `OnChange` and one timer for the next configured
briefing or window end.

## Out of scope

- Brakeman as a scheduler: it launches nothing, adds no member, forms no
  crew and rewrites no task graph; BK2's launch is of a plan revision a
  person approved, through DS2's own launch by way of `ext.Host` (PR-17).
  Supervisor lifecycles beyond
  BK3's role, dynamic crew reshaping and live plan revision are the
  Program's later work (section 7).
- Answering a question, approving a plan revision, accepting a task or a
  ticket, closing a ticket, passing a gate, changing a required approval:
  never Brakeman's, granted or not.
- An agent's private conversation: Brakeman never reads another agent's
  transcript or session files, and a handoff package never pretends to
  carry one provider's conversation to another; it is built from
  Conductor's records.
- Brakeman listening by default: terminal screens are off until a person
  turns them on, and are read only when Brakeman asks, a bounded number of
  lines; huddles (Program item CL1) are not touched.
- The paid parts (decision D1): routing a brief to a named person, push
  delivery, graph-enriched context, workspace-shared briefings, and a
  briefing scheduled from the cloud while the machine sleeps (Program item
  AU1). BK3's briefing times are local and fire while the server runs.
- An account registry, usage windows and reset times (Program item AC1):
  the account here is a folder reference.
- A2UI, AG-UI and MCP Apps: candidates the Program names; none is adopted
  (Owner decisions, 15).
- More than one Brakeman, a Brakeman per project, Brakeman on a host or a
  switchyard: one per server, on the server.
- Hibernation of idle sessions (Program item HB1).

## Tests per surface

Per `docs/checks.md`: every Go PR runs `make lint`, `go test -race
-count=1 -timeout 30m ./...` and `make lint-static`; every UI PR runs
`npm --prefix web run typecheck`, `npm --prefix web test` and `make
test-e2e` (the PR's specs alone first, with `--repeat-each 10` for a new
spec); a PR touching both runs both. The named tests each PR adds:

**PR-1, `internal/brakeman`** (Go race): `config_test.go`:
`TestConfigBoundsEachField` (every field at its bound and one past),
`TestAModelNeedsAnAdapterThatTakesOne`, `TestAnAgentWithoutAReadOnlyModeIsRefused`,
`TestAConfigSaveNamesItsRevision`, `TestTheDefaultsAreOffAndTerminalsHidden`,
`TestActMustBeEmptyInThisBuild`, `TestScopeAdmits` (each selector alone and
together, a session in no run, a ticket linked to no run, a repository
root), `TestCoversAgreesWithAdmits` (the footprint's containment, its
standalone-session clause among them), `TestObserveOnlyRefusesDraftsAndProposals`.
`request_test.go`: `TestEveryRequestTransition` (the table, every pair, `starting` to
`briefed`, `ended` and `failed` and `running` to `failed` among them),
`TestAClosedRequestOnlyGainsMade` (and an empty run or session id, never
a state), `TestEachKindTakesItsSubjects`.
`brief_test.go`: `TestBriefBounds` (each field and count at its bound and
one past, the whole at 256 KiB), `TestAFactWithoutEvidenceIsRefused`,
`TestAnInterpretationReadsFactsOfItsBrief`, `TestAnOutcomeNamesAReceipt`,
`TestADecisionCardIsWhole`, `TestDraftTicketsBlockWithoutACycle`,
`TestADraftCrewIsCheckedByFacts`, `TestTextIsCleanedBeforeItIsKept`,
`TestTextPastByte500Survives` (a summary of 4 KiB and a description of
16 KiB kept whole), `TestAFieldPastItsBoundIsRefusedNotCut`,
`TestABriefCarryingAsOfIsRefused`.
`refs_test.go`: `TestRefGrammar` (each kind's valid and invalid forms, the
512-byte bound), `TestARefPrintsAsParsed`. `store_test.go`:
`TestANumberIsNeverReused` (the state written, the request's file failing),
`TestPruneKeepsTheNewest500AndEveryOpenOne`, `TestPruneKeepsAPinnedRequest`,
`TestOpenInterruptsWhatWasStartingOrRunning`,
`TestALaterSchemaIsSkippedNotRewritten`,
`TestTurnsTodayCountsByTheLocalDay` (a fake clock across midnight),
`TestTheDaysCountSurvivesPruneAndCancel` (600 cancelled requests),
`TestUpdateIsAtomicUnderConcurrency` (32 goroutines),
`TestASecondOpenIsReadOnlyUntilItHoldsTheLock` (two stores on one
directory: the second refuses writes, interrupts nothing, and takes the
desk once the first closes), `TestPendingListsUnfinishedMakes` (Open
completes nothing of another store's; the desk's recovery is PR-4's).
`internal/store`: `TestOwnerLock` (the ledger's lock tests, moved);
`internal/crew/ledger`'s own tests unchanged.

**PR-2, `internal/api` and `internal/cli`** (Go race):
`api/guide_test.go`: `TestTheGuideNamesThisBuild`,
`TestEveryOperationIsARegisteredRoute` (each table entry's pattern answers
other than the mux's 404), `TestEverySessionRouteIsInTheTable`,
`TestEachSectionIsBounded` (every entry at its largest),
`TestASessionsGuideListsItsScopesOnly` (a plain session, an attempt, a
member of a run with a ticket), `TestTheGuideRefusesAnotherSessionsToken`,
`TestASwitchyardAnswersItsOwnGuide`, `TestProvidersAreUnknownBeforeC9`.
`cli/mcp_test.go`: `TestToolsListIsUnchanged` (the sixteen existing
tools' entries against a golden file of `main`'s reply, their
descriptions from `Description`), `TestSummaryAndDescriptionAreBounded`,
`TestEveryToolIsAnOperation`, `TestAnUnhandledToolIsNotListed`,
`TestTheGuideToolReadsTheServer`,
`TestTheGuideToolSaysWhenVersionsDiffer`. `cli/guide_test.go`:
`TestGuideInASession`, `TestGuideWithTheWorkbenchToken`, `TestGuideJSON`.

**PR-3a, `internal/agents`** (Go race): `agents/adapter_test.go`:
`TestOnlyVerifiedAdaptersTakeAModel`, `TestClaudeAllowsConductorsTools`,
`TestOnlyClaudeAndCodexHaveAReadOnlyMode`; under the `recipes` tag
(nightly, `make test-recipes`): `TestModelFlagsMatchTheCLIs`,
`TestClaudeAllowedToolsMatchesTheCLI`, `TestReadOnlyArgsMatchTheCLIs`.

**PR-3b, `internal/api`** (Go race): `api/brakeman_refs_test.go`:
`TestEachRefKindResolves` (every kind against its store),
`TestARefOutsideTheScopeIsRefused`, `TestADeletedTargetResolvesAsDeleted`,
`TestAnEvictedQuestionIsUnavailable`.

**PR-3c, `internal/api`, `internal/crew`** (Go race):
`api/brakeman_test.go`: `TestARequestIsRefusedWhileBrakemanIsOff`,
`TestATurnIsAOneMemberRunThatTypesItsLine` (the API tests' scripted
agent, which submits its brief over HTTP with its own token),
`TestTheTurnCarriesItsRequestAndAccount` (`CONDUCTOR_BRAKEMAN` and the
adapter's `ConfigDirEnv` in the session's environment),
`TestARequestRepeatsByItsOpID`, `TestOneTurnAtATimeAndABoundedQueue`,
`TestTheDeadlineEndsTheTurn` (the desk's clock seam, never a sleep),
`TestASessionThatEndsWithNoBriefEndsTheRequest`,
`TestABriefIsKeptBeforeTheRunStops` (a seam between the write and the
stop), `TestABriefNamesOnlyRefsThatResolve` (`422 bad_refs` with each ref),
`TestABriefOutsideTheScopeIsRefused`, `TestTheWorkbenchTokenSubmitsNoBrief`,
`TestABriefFromAnotherSessionIsRefused`, `TestABriefIsKeptOnce`,
`TestARestartInterruptsTheTurn` (a server of its own, stopped and started
on the same data), `TestABrakemanRunIsNotResumed`,
`TestABrakemanSessionFormsNoCrew`, `TestBrakemansTokenReachesNoAdminRoute`
(walks every `requireAdmin` and `launching` route with it),
`TestTheDaysBudgetRefuses`, `TestTheDequeueRechecksTheBudget` (a fake
clock across midnight, a lowered `TurnsPerDay`), `TestCancelStopsTheTurn`,
`TestTurningBrakemanOffStopsTheTurn`, `TestTheBrakemanEventIsBounded`,
`TestASwitchyardRefusesBrakeman`, `TestATurnLaunchesReadOnly` (the
adapter's read-only arguments in the argv), `TestBrakemanNeverTakesYolo`
(a crew's and the server's yolo on: no yolo argument),
`TestTheDeadlineCoversStarting` and `TestCancelDuringLaunchStopsTheRunItReturns`
(a gate inside the launch seam), `TestTheNextTurnWaitsForTheLastRunToEnd`,
`TestAStopThatFailsHoldsTheSlot` (the real engine and `session.Local` over
a `session.Process` double whose `Stop` fails and whose `Done` stays open:
the run reads stopped, the slot stays held, the next turn waits until
`Done` closes), `session/local_test.go`'s
`TestAStopRetriedAfterAFailureReachesTheProcess` and
`TestAStopAfterTheProcessIsReapedSignalsNothing` (the watchdog),
`TestTheSlotWaitsForAnOutstandingLaunch`,
`TestAnAgentThatExitsBeforeReadingFails` (from `starting` and from
`running`), `TestASessionThatEndsDuringLaunchEnds`,
`TestALateLaunchReturnLeavesAClosedRequest` (briefed, ended and cancelled
while `starting`: the return writes no state, and only a cancelled one's
run is a `strayRun`),
`TestABriefWhileStartingIsTaken`, `TestALostBriefReplyIsAnsweredAfterBriefed`,
`TestAProposalForARevisionCheckedOpNeedsItsRevision` (the brief route asks
the guide's `Operation.Revision` through `Facts`),
`TestAPersonsCrewNamedBrakemanIsNotATurn`,
`TestBrakemanCannotReachATicket` (its run linked through a seam: `…/ticket`
and the comment route refused), `TestABrakemanRunIsNotLinkedToATicket`,
`TestABrakemanQuestionIsLabelledAndNotifiesOnlyForAPerson`,
`TestASecondServerRunsNoTurn` (two servers on one data directory).
`crew/store_test.go`: `TestBrakemanIsATakenID`; `crew/run_test.go`:
`TestLaunchNamedWithStampsClassAndRequest` (in the `LaunchSpec` before
the member starts), `TestBrakemanRunsEvictNoPersonsRun` (101 turns beside
one person's run), `TestBrakemanRecordsArePrunedApart`.

**PR-4, `internal/api`, `internal/crew/ledger`** (Go race):
`api/brakeman_observe_test.go`: `TestChangesSinceTheLastBriefUseItsWatermarks`,
`TestTheAdminsReadsAreUnchanged` (each shared route's existing answers),
`TestBrakemansPrincipalReadsOnlyTheNamedRoutes` (every admin route walked),
`TestASharedReadIsTheRoutesOwnAnswerFiltered`,
`TestACommentOnlyChangeIsSeen` (a comment on an old ticket),
`TestAHostedSessionsScreenIsUnavailable`, `TestAHostedSessionIsNeverQuiet`,
`TestOverlapsSeeCommittedChanges` (two clean worktrees that committed the
same file), `TestCoverageIsTheServers` (an unread page, a truncated run, a
gap: the next briefing reads them again), `TestAWidenedScopeStartsAtTheLastBriefing`,
`TestASeenRevisionIsKeptBesideNow`, `TestAStaleSubjectRevisionIsRefused`,
`TestDraftTicketsKeepTheirBlocksInEitherOrder` (A then B, B then A, and a
crash seam between a create and an edge), `TestADraftCrewIsFoundByItsOrigin`
(a crash seam between the save and `made`),
`TestChangesSinceATimeAreBoundedAndSayTruncated`,
`TestAFailedTaskCarriesItsReceipt`, `TestAQuietSessionIsNotAFailure`
(`LastOutputAt` through a seam), `TestAnOpenQuestionIsWaitingOnAPerson`,
`TestObserveKeepsToTheScope`, `TestTheScreenNeedsTerminals`,
`TestReadsAreCountedAndBounded`, `TestOverlapsNameBothTreesAndThePath` (two
worktrees of a real repository in `t.TempDir()`), `TestALogTailCarriesItsSHA`
(against the step log route's `X-Conductor-Log-SHA256`),
`TestEachReplyIsBounded`, `TestADraftTicketIsMadeOnce` (the same `opId`
twice; a crash seam between Dispatch's commit and `made`'s write),
`TestADraftCrewIsSavedNotLaunched`, `TestBrakemansTokenMakesNothing`,
`TestConcurrentMakesOfOneItemMakeOne`, `TestPruneDuringAPendingMakeKeepsItsRecords`,
`TestAMakeOfAPrunedRequestCallsNoBackend`, `TestADuplicateOfAMadeCrewCarriesNoOrigin`,
`TestRemovingAStandaloneSessionWithholdsItsBrief`.
`ledger/ledger_test.go`: `TestLastEvent`; `dispatch/dispatch_test.go`:
`TestChangesAfter`; `gitcli/gitcli_test.go`: `TestChangedSince`;
`crew/store_test.go`: `TestCreateFromAnswersByOrigin`,
`TestAnUpdateKeepsTheOrigin`, `TestDuplicateAndCreateClearTheOrigin`, `TestDeletingACrewWithAnOriginWritesItsTombstoneFirst`
(a crash seam between the tombstone and the removal),
`TestCreateFromATombstonedOriginSavesNothing`.

**PR-5, `internal/cli`, `internal/agents`** (Go race): `cli/mcp_test.go`:
`TestBrakemansToolsOnlyInABrakemanSession`, `TestNoCrewToolsForBrakeman`,
`TestObservePassesItsQuery`, `TestBriefSubmitResendsTheSameOpID`.
`cli/brakeman_test.go`: `TestBrakemanAskWithTheWorkbenchToken`,
`TestBrakemanBriefFromAFile`, `TestBrakemanOutsideASessionIsLoud`.
`agents/codex_test.go`: `TestCodexMCPEnvNamesBrakeman`;
`agents/skill_test.go`: `TestTheSkillsBrakemanSectionIsShort` (at most six
lines, names `guide`). The stub's Brakeman mode is PR-6's, with the spec
that drives it.

**PR-6, `web/app`, `web/e2e`**: vitest `utils/brakeman.test.ts` (the
kinds' and states' words, every ref kind to its route and label, the
items' order, the config form's checks against the server's bounds),
`composables/useBrakeman.test.ts` (an id kept across a lost reply, the
event folding into the list). Playwright `brakeman.spec.ts`: Settings saves
a configuration with the stub as the agent and terminals off; a briefing
asked from the page is queued, then running with its session's link, then
briefed; the brief shows the provenance line, the reading, the fact with
its evidence chips linking to the run and the session, and the
interpretation naming the fact; the run is stopped; a brief arriving while
a terminal has the focus leaves `document.activeElement` and the URL as
they were and adds the sidebar's count; a server of its own restarted on
the same data shows the same brief.

**PR-7, `web/app`, `web/e2e`**: vitest `utils/brakemanCards.test.ts` (a
card's controls from the live record of each `about` kind and state, by
`taskActions`' rules: a failed work task with a submitted result, one
without, a blocked task; the stale revision's words). Playwright
`brakeman-cards.spec.ts`: a crew whose check always fails (the
`tasks.spec.ts` fixture) gets a decision card with Retry and, its result
submitted, Accept anyway, and no Cancel; a blocked task's card offers
Cancel and no Retry; Retry with the brief's revision after the run
moved is refused with words; a draft of two tickets, one blocking the
other, edited and made: both tickets exist with the relation, Make again
makes nothing; a draft crew is saved and opens in the editor, not
launched; the handoff copies; the palette's Brakeman rows and the run
page's "Ask Brakeman why"; a proposal to stop or resume a run is text
with the run page's link and no button, and a proposal to pause with the
brief's revision after the run moved is refused by the route with words.
`palette-rows.spec.ts` gains the new rows.

**PR-8** docs only; no check applies.

**PR-9** (Go race): `api/guide_test.go`: `TestTheGuideListsC9sProviders`
(with a package-local fake provider of `internal/api`'s own tests
registered; `ext/exttest` arrives only with ext's PR-17, C9's contract suite, after #488),
`TestAnEmptyRegistryIsKnownAndEmpty`.

**PR-10 and PR-11**: Go race `TestPlanRefsResolve`,
`TestObservePlansSaysWhetherTheApprovalHolds`,
`TestADraftPlanIsProposedByTheOperator` (DS2's route, the actor `admin`),
`TestBrakemanCannotApprove`; Playwright `brakeman-plans.spec.ts` (a draft
plan revision proposed from a brief, approved on the ticket page, the
card's Approve control).

**PR-12 and PR-13**: Go race `TestAnUngrantedActIsRefused`,
`TestAGrantGivesNoRead`, `TestAReadGivesNoGrant`,
`TestAnActRunsTheOperationsOwnFunction` (the same receipt as the admin's
route, actor `brakeman`), `TestAnActIsKeptOnce`, `TestAStaleTargetIsRefused`,
`TestAnActRetryAfterTheTargetMovedAnswersItsReceipt`,
`TestAnActCommittedBeforeItsCopyIsCompletedAfterARestart`,
`TestAnActThatNeverRanIsMarkedNotRun`, `TestAnActsOpIDNamesOneAct`,
`TestPruneKeepsARequestWithAPendingAct`,
`TestNoGrantApprovesAnswersOrAccepts` (each refused operation),
`TestBrakemanCannotGrantItself` (every route its token reaches, none writes
`Permissions`); Playwright `brakeman-act.spec.ts` (a granted
`launch_approved_plan` from a brief, its receipt on the brief and the run
page; an ungranted one shown as a proposal only).

**PR-14 and PR-15**: Go race `crew/role_test.go`:
`TestAWakeGathersThenBriefsThenCools` (fake clock),
`TestWakesInTheWindowAreOneBatch`, `TestTheCooldownHoldsTheNextBatch`,
`TestTheBudgetStopsTurnsAndSaysSo`, `TestNoModelIsPolled` (no turn without
a wake), `TestARestartFiresAnOpenBatchOnce`,
`TestAnUnavailableRoleHoldsNothingElse` (a crew runs, a question is
answered, while the role is unavailable),
`TestBriefingTimesInTheLocalZone`, `TestABriefingWakesNothing` (one
external failure: one turn, and the turn's own run ending wakes nothing),
`TestACrashBetweenTheRequestAndTheRoleFiresOnce`,
`TestABatchPastTwoHundredWakesOverflows` (eleven failures, then 201),
`TestALoggedOutAgentMakesTheRoleUnavailable` (a stub that exits at once),
`TestAWakeTurnsQuestionNotifiesNobody` (no webhook),
`TestAWakeTurnsSessionIsMarkedQuiet` (the `CrewRef` mark in its first
snapshot); Playwright `brakeman-role.spec.ts` (a task failing wakes
Brakeman, one brief, the role cooling, the log; a wake turn whose stub
asks a question is listed in Needs you and raises no browser
notification, read off the page's `Notification` stub; paused, nothing
wakes it).

**What tests cannot cover, by hand** (Acceptance): a real agent on the
owner's own subscription (Claude Code and Codex) doing real turns against a
night of real crews; Claude Code's `--allowedTools` and `--disallowedTools`
rules, Codex's read-only sandbox and its MCP approvals as the CLIs of the
day behave; the Windows app (WSL) and the Mac
rows of `make preflight` for PR-3c, PR-6 and PR-14, the PRs that change a
launch, the pages and the engine.

## Boxcar constraints

1. **The engine starts an attempt only through `crew.Launcher`; Brakeman
   goes through `ext.Host`, never exec.** A turn is a run the engine
   launches (`LaunchNamedWith`), its member started through
   `crew.Launcher`; Brakeman runs no command and nothing of it calls
   `os/exec`. `ext.Host.Launch` is `launchSaved`, the launch of a saved
   crew (`docs/plans/ext.md`, Host and Start), which neither a turn (an
   unsaved crew) nor BK2's launch (DS2's launch from an approved
   revision, #464) is. PR-17, once ext's PR-4 (#476) has merged, gives
   `ext.Host` the two launches Brakeman makes, `LaunchTurn` (answered by
   `launchTurn`) and `LaunchApproved` (declared there, answered by DS2's
   launch function from PR-12, which waits for DS2's PR-7),
   with ext's own request types, and the desk then holds an `ext.Host`
   from the composition root and launches through it alone; BK2's PR-12
   waits for PR-17, so every Brakeman launch of BK2 goes through
   `ext.Host` from its first PR. Until PR-17, PR-3c's turns call
   `launchTurn` directly, the same function the Host method will answer
   with: a departure from this constraint for BK1's first slice, which
   is the owner's to approve at this plan's review (Owner decisions, 22).
2. **`session.Local` drives a `session.Process`.** A turn's session is an
   ordinary server session; nothing here reaches a pid or a tty; a turn is
   stopped through `Engine.Stop`.
3. **An attempt's context is files.** Not touched: a turn is not an
   attempt and adds nothing to one. Its request lives in the data
   directory, a file, and reaches the agent over the network (4).
4. **The agent talks to the server over the network.** Every Brakeman tool
   is HTTP with the session's `CONDUCTOR_NOTIFY_TOKEN`, through `conductor
   mcp` or the CLI, as the task tools are.
5. **Checks go through `internal/proc`.** Not touched: Brakeman runs no
   check. Its git reads (`overlaps`, `commit:` refs) go through
   `internal/gitcli`, argv arrays, as the Files tab's do.
6. **Platform splits stay platform tags; the UI shows a capability.** No
   tag added; the guide and the pages show what the server reports, never
   a plan (PR-9 reads C9).
7. **Evidence rows gain `where`.** No evidence row is added; a check
   receipt Brakeman cites is resolved from the ledger as it stands, and
   once C10 lands its `where` is carried in the resolution as it is.
8. **`hostagent` is a launch site.** Not touched: Brakeman runs only on the
   server and starts nothing on a host. A hosted session is observed
   through what the server holds of it (the registry's `Info`, its
   attention as relayed): its screen and its quiet time are the host's and
   are answered as unavailable (`…/screen` `409 unavailable`, quiet time
   `unknown`), with no new host request or protocol.

## Open-core checklist

- [x] Article 1, no premium code and no licence check: Brakeman is
  open-source code with no plan, tier or entitlement check; the guide reads
  C9's registry answer and branches on nothing.
- [x] Article 2, the free build is fully useful: briefs, drafts, the guide,
  BK2's actions and BK3's role all work on the free build with the
  person's own agent, the local ledger and Dispatch, offline from any
  Conductor service.
- [x] Article 3, paid features gated by hosted dependencies: nothing here is
  paid; the paid extras of decision D1 are out of scope and would arrive as
  providers.
- [x] Article 4, stop, inspect, revoke, export, required approvals stay free
  and never degrade: a turn stops like any run, every request and brief is
  inspectable and in the data directory export carries, Brakeman's token
  dies with its session, and it answers, approves and accepts nothing.

## The PRs

Each PR is one issue and one branch cut from `main`, a draft PR at its
first commit, the checks for its surfaces before every push, its body
ticking the open-core checklist (articles 1 to 4) and naming the agent
that made it; each leaves `main` working and green on its own (a package
without its routes is reached by its tests, a route without its page by the
CLI and the tests, a page without its later parts says what is not there
yet). Parallel means the two touch no common Go package, no common
directory under `web/`, and no common file under `docs/` or at the root.
Sizes are estimates of the first push's changed lines with tests; DS1's and
DS2's PRs grew past their estimates in review, which is why none here is
planned larger than about 1,500.

**The cut for Monday 2026-11-02.** PR-1 to PR-8 need nothing that is not on
`main` today and can ship before Monday: in waves, PR-1, PR-2 and PR-3a
together, then PR-3b, then PR-3c, then PR-4, then PR-5, then PR-6, then
PR-7, then PR-8. Each of PR-3b to PR-6 needs the one before it: PR-3b and
PR-3c share `internal/api` with PR-2, PR-5's tools are listed from the
table entries PR-4 adds and call its routes, and PR-6's spec drives a
stub that reads them. After them the owner configures Brakeman with the
owner's own agent and uses it. PR-9 to PR-18 wait, each on the
prerequisite named in its row.

PR numbers are names, not an order: PR-17 and PR-18 were added in
review after PR-16 was named. The table lists the PRs in an order in
which every dependency comes first (PR-17 before PR-12, which waits for
it), and the dependency graph has no cycle.

| PR | Title | Touches | Depends on | Parallel with | Size | When |
|---|---|---|---|---|---|---|
| PR-1 | brakeman, store: the configuration, requests, briefs (schema 1) and evidence references, their records, and the owner lock shared with the ledger | `internal/brakeman`, `internal/store`, `internal/crew/ledger` | none | PR-2, PR-3a | ~1,500 | before Monday |
| PR-2 | api, cli, mcp: the operating guide from the running server, and the tools listed from one table (BK0, first form) | `internal/api`, `internal/cli`, `docs/protocol.md` | none | PR-1, PR-3a | ~1,200 | before Monday |
| PR-3a | agents: the read-only mode, the model flag and the Conductor tools rule, each checked against the CLIs | `internal/agents` | none | PR-1, PR-2 | ~400 | before Monday |
| PR-3b | api: a brief's refs resolved against the records, the scope and what the turn read | `internal/api` | PR-1, PR-2 | PR-3a | ~700 | before Monday |
| PR-3c | api, crew, session: a Brakeman turn as a read-only one-member Yardmaster run, its requests over HTTP and the brief its session submits | `internal/api`, `internal/crew`, `internal/session`, `docs/protocol.md` | PR-3a, PR-3b | none | ~1,300 | before Monday |
| PR-4 | api, crew, ledger, dispatch, gitcli: what Brakeman reads through the shared routes and the new ones, the status Conductor computes, the server's coverage, and a draft made work by the operator | `internal/api`, `internal/crew`, `internal/crew/ledger`, `internal/dispatch`, `internal/gitcli`, `docs/protocol.md` | PR-3c | none | ~1,500 | before Monday |
| PR-5 | cli, agents: Brakeman's MCP tools and `conductor brakeman`, the skill's section | `internal/cli`, `internal/agents` | PR-4 | none | ~700 | before Monday |
| PR-6 | web: Brakeman's settings, the Brakeman page, a brief as Conductor renders it, and the e2e stub's Brakeman | `web/app`, `web/e2e` | PR-4, PR-5 | none | ~1,500 | before Monday |
| PR-7 | web: decision cards with Conductor's controls, drafts made work, handoffs, the palette and run page entries | `web/app`, `web/e2e` | PR-4, PR-6 | none | ~1,300 | before Monday |
| PR-8 | docs: the first slice's features entry, the README, the repository map, the architecture | `docs`, `README.md` | PR-1 to PR-7 | none | ~250 | before Monday |
| PR-9 | api, cli: the guide's providers and host capabilities from C9 | `internal/api`, `internal/cli`, `docs/protocol.md` | PR-2; waits for C9 (`GET /api/capabilities`, #488) | PR-11 | ~400 | waits: C9 |
| PR-10 | brakeman, api: plan revisions, launches and candidates as evidence, and a plan revision drafted for the operator | `internal/brakeman`, `internal/api`, `docs/protocol.md` | PR-4; waits for DS2's PR-6 to PR-8 (#463 to #465) | none | ~1,100 | waits: DS2 |
| PR-11 | web: DS2's records in briefs, "Propose as plan revision", Approve on a card, the ticket page's entries | `web/app`, `web/e2e` | PR-7, PR-10; waits for DS2's PR-10 and PR-11 (#467, #468) | PR-9 | ~900 | waits: DS2 |
| PR-17 | ext, api: `ext.Host` launches a turn and declares the approved revision's launch (PR-12 wires it), and the desk launches through it | `ext`, `internal/api`, `docs/plans/ext.md` | PR-3c; waits for ext's PR-4 (`ext.Host`, #476) | none | ~500 | waits: C1 |
| PR-12 | brakeman, api: authorized actions: the grants, each operation's own route taking Brakeman's principal, four operations with scope, state checks and receipts (BK2) | `internal/brakeman`, `internal/api`, `docs/protocol.md` | PR-8, PR-11, PR-17; waits for DS2's PR-7 (#464), DR1 and RV1 and BK1 Done | none | ~1,400 | waits: DS2, DR1, RV1 |
| PR-13 | cli, agents, web: the act tool and command, grants in Settings, actions and their receipts in a brief | `internal/cli`, `internal/agents`, `web/app`, `web/e2e` | PR-12 | none | ~1,100 | waits: DR1, RV1 |
| PR-14 | crew, brakeman, api: the background role in Yardmaster: wake sources, batching, cooldowns, budgets, records (BK3) | `internal/crew`, `internal/brakeman`, `internal/api`, `docs/protocol.md` | PR-13; waits for BK2 Done | none | ~1,500 | waits: BK2 |
| PR-15 | web, cli: the role's controls, its wake log and its unavailable state | `web/app`, `web/e2e`, `internal/cli` | PR-14 | none | ~1,000 | waits: BK2 |
| PR-16 | docs: the features entries of BK0 to BK3 and the by-hand records of their acceptance | `docs`, `README.md` | PR-9, PR-15, PR-17 | none | ~250 | last |
| PR-18 | api, web: `route_decision` to a human member's label | `internal/api`, `web/app`, `web/e2e` | PR-13; waits for HM1 | none | ~400 | waits: HM1 |

**PR-1** (`internal/brakeman`, `internal/store`, `internal/crew/ledger`):
`config.go`, `request.go`, `brief.go`, `refs.go`, `store.go` as The changes
describe them, with `Facts` (`CheckCrew` among it) as an interface the API
implements in PR-3c; the ledger's owner lock moved to `store.OwnerLock`,
the ledger calling it. No caller yet; its tests reach all of it. Go only.

**PR-2** (`internal/api`, `internal/cli`, `docs/protocol.md`): `guide.go`
with the operations table for `main`'s agent routes and tools and the admin
operations a proposal may name; the two guide routes; `mcpTools()` built
from the table, held byte for byte by the golden test; the MCP tool
`guide`, `initialize`'s one sentence, `conductor guide`; the protocol's
"Guide" rows. Go only.

**PR-3a** (`internal/agents`): `Adapter.ReadOnlyArgs`, `Adapter.ModelArgs`
and `Adapter.AllowTools` for Claude Code and Codex, each checked against
the CLI in the PR and under the `recipes` tier. Go only.

**PR-3b** (`internal/api`): `brakeman_refs.go`, the resolution of every
ref kind against its store, `Scope.Admits` and the visibility, with `seen`
read from a request's read log (empty until PR-4) and `now`; no route
yet. Go only.

**PR-3c** (`internal/api`, `internal/crew`, `internal/session`,
`docs/protocol.md`):
`brakeman.go` (the desk, the owner lock's read-only mode, the admin
routes, the turn as a read-only run through `launchTurn`, the watchdog and
the slot, the restart, the refusals that keep a turn Brakeman's),
`brakeman_session.go` (the request and brief routes), `Server.Launch`'s
names for a turn, the label and notification rule for its questions, the
`brakeman` event; `Engine.LaunchNamedWith`, the run's class and request,
the classes' own caps, the crew store's taken id; `Local.ProcessDone()`,
`Local.Stop`'s guard on the process rather than the status, and the session's `crew.brakeman` mark (below); the guide's table gains
the routes with their MCP names; the protocol's Brakeman section. Go only;
the Mac and Windows preflight rows run here (a launch changes).

**PR-4** (`internal/api`, `internal/crew`, `internal/crew/ledger`,
`internal/dispatch`, `internal/gitcli`, `docs/protocol.md`): the
`brakeman` principal and `s.reader` on the shared reads, the new reads
under `/api/brakeman/` with the computed status, the read log and the
server's coverage, the `make` route for drafts with its edges, the
crew's origin and its tombstone, the derived op ids, the desk's
`recoverMakes` over Dispatch and the crew store (with the owner lock's
handoff), and the earlier briefs' footprints; `Ledger.LastEvent`, `dispatch.Store.ChangesAfter`,
`gitcli.ChangedSince`, `crew.Store.CreateFrom`; the table's entries with
`observe`'s MCP name; the protocol's rows. Go only.

**PR-5** (`internal/cli`, `internal/agents`): the three MCP tools'
handlers and their listing rule, the crew and ticket tools left out for
Brakeman, `conductor brakeman`, its usage and completion; `codexMCPEnv`;
the skill's section. Go only (PR-3c's tests drive a turn with the API
tests' own scripted agent, submitting the brief over HTTP with the
session's token); it changes no file a Playwright spec runs.

**PR-6** (`web/app`, `web/e2e`): `useBrakeman.ts`, `utils/brakeman.ts`,
`BrakemanSettings.vue` on the Settings page, `pages/brakeman.vue`,
`BrakemanAsk.vue`, `BrakemanBrief.vue`, `BrakemanFact.vue`, the sidebar's
Brakeman row; `useSessions.ts`'s `crew.brakeman` and `useAttention.ts`'s
`react` skipping a `quiet` session's notification and Chime (the
invariant on Brakeman's questions); `web/e2e/stub-agent.sh`'s `STUB_BRAKEMAN` mode (moved here
from PR-5, since every spec runs the stub); the e2e catalog gains a Brakeman stub agent
(`web/e2e/conductor.e2e.json`: the stub under Claude Code's adapter, so the
configuration takes it, the MCP, read-only and model arguments it is launched with
ignored by the stub, which uses the CLI path). Proposals show their
operation's name and say the controls come next. UI only; the Mac and
Windows preflight rows run here.

**PR-7** (`web/app`, `web/e2e`): `BrakemanDecision.vue`,
`BrakemanProposal.vue`, `BrakemanDraftTicket.vue`, `BrakemanDraftCrew.vue`, `BrakemanHandoff.vue`,
the palette's group, the run page's task action, `utils/brakemanCards.ts`.
The ticket page's entries wait for PR-11 if DS1's PR #501 and PR #503 have
not merged by then. UI only.

**PR-8** (docs): the features entry for the first slice, written from what
PR-1 to PR-7 verified and from the owner's first turns (Acceptance), or
saying they are pending; the README's paragraph; the repository map's row;
the architecture's section. Docs only; no check applies.

**PR-9** (`internal/api`, `internal/cli`, `docs/protocol.md`), after #488:
the guide's `providers` and `host` from C9's route. Go only.

**PR-10** (`internal/brakeman`, `internal/api`, `docs/protocol.md`), after
DS2's PR-6 to PR-8: the refs `plan:`, `launch:`, `candidate:`; the reads
`plans` and `candidates`; the item `draft_plan/1` and its `make` through
DS2's propose route under the actor `admin`; a decision card about a
`plan:` ref. Go only.

**PR-11** (`web/app`, `web/e2e`), after DS2's PR-10 and PR-11: the draft
plan's form and "Propose as plan revision", the card's Approve from DS2's
own control, the ticket page's Brakeman entries. UI only.

**PR-12** (`internal/brakeman`, `internal/api`, `docs/protocol.md`), once
DR1 and RV1 serve their operations, DS2's launch (its PR-7, #464) and
PR-17 have merged and BK1 is Done:
`Permissions.Act` and its closed list; each granted operation's own route
taking the `brakeman` principal, its one handler and its receipt; the
pending act and `recoverActs`; `ext.Host.LaunchApproved`, declared by
PR-17, wired to DS2's launch function (its PR-7, #464), and the launch
through it; `route_decision` to the operator only. Go only; its
launch tests are `TestLaunchApprovedRefusesAnUnapprovedRevision`,
`TestLaunchApprovedRetryAfterALostReplyIsOneRun` (the same `OpID` through
the Host twice: one launch, the same receipt) and
`TestLaunchApprovedCarriesItsExpectedRev` (a moved plan refused).

**PR-13** (`internal/cli`, `internal/agents`, `web/app`, `web/e2e`): the MCP
tool `act` (listed only with a grant) and `conductor brakeman act`, the
grants in Settings with what each authorizes in words, and a brief's
proposal that Brakeman ran shown with its receipt. Go and UI.

**PR-14** (`internal/crew`, `internal/brakeman`, `internal/api`,
`docs/protocol.md`), once BK2 is Done: `crew/role.go` (the role loop, its
records in `brakeman/role.json`, its wake sources, window, cooldown and
budget), `Wake.Mode` `background` with its fields, the request kind
`wake`, the desk's sources wired (the activity sink, the engine's run
changes, Dispatch's `OnChange`, the briefing timer). Go only; the Mac and
Windows preflight rows run here.

**PR-15** (`web/app`, `web/e2e`, `internal/cli`): the role's controls,
log and unavailable state on the Brakeman page and in Settings;
`conductor brakeman role on|off|pause|status`. Go and UI.

**PR-16** (docs): the features entries of BK0, BK1, BK2 and BK3 with each
acceptance's by-hand record; the README. Docs only.

**PR-17** (`ext`, `internal/api`, `docs/plans/ext.md`), once ext's PR-4
(#476) has merged: `ext.Host` gains `LaunchTurn(ctx, ext.TurnLaunch)` and
`LaunchApproved(ctx, ext.ApprovedLaunch)`, with ext's own types (an agent
id, a model, a folder, the line, the label, the request id; for an
approved launch: a ticket, the plan revision, its `ExpectedRev`, the
operation's `OpID` (the derived `derive("act", request, opId)`, at most 32
characters, which DS2's launch takes as its `opId` so a retry answers the
same launch and never starts a second run) and a `By`). `LaunchTurn` is
answered by `launchTurn`; `LaunchApproved` is declared here and answers
`ErrUnsupported`, since DS2's launch (its PR-7, #464) is not a
prerequisite of this PR: PR-12 wires it to DS2's launch function and holds
its behavioural tests. The desk is given an `ext.Host` by the composition
root and launches through it alone. Go race:
`TestTheDeskLaunchesThroughTheHost` (a recording `ext.Host` written in
`internal/api`'s own tests: `ext/exttest` is ext's PR-17 and not a
prerequisite here), `TestHostLaunchTurnIsLaunchTurn` (the same run as
before), `TestLaunchApprovedIsUnsupportedUntilPR12` (the declared method
answers `ErrUnsupported` and starts nothing). Go only.

**PR-18** (`internal/api`, `web/app`, `web/e2e`), once HM1 serves human
members' labels: `route_decision` to a label as well as to the operator,
and Settings' words for it. Go race `TestRouteToALabel`; Playwright in
`brakeman-act.spec.ts`. Go and UI.

## Owner decisions

Dated 2026-10-31. Each is a default the owner can overturn with a dated row
in the Program's status log, its convention for a decision the owner did not
make (the Program, section 3.1); the reasoning is why it was taken.

1. **One plan file for the four items.** BK0, BK1, BK2 and BK3 share the
   desk, the records, the guide's table and the names, so they are planned
   together here; the Program's rows for BK0 and BK3 point at this file in
   place of `docs/plans/capabilities.md` and `docs/plans/brakeman-role.md`
   (a status-log row records it).
2. **O2, the role primitive: a dedicated loop over ordinary bounded turns,
   in Yardmaster's engine** (`internal/crew/role.go`), not a general "role
   session" member. A turn is already BK1's bounded run of a fresh session;
   the loop only decides when to start one, from records, so the role
   never needs a process between turns, never polls a model, rebuilds from
   Conductor's records by construction, and asks nothing of the managed
   engine's member rules (the 409 `managed_run` boundary stands until live
   plan revision is designed). A role session would keep a conversation
   alive for hours, whose state is the provider's and lost on a restart or
   a model change, which BK3 forbids relying on. The owner would overturn
   this if a long-lived session proves worth its cost in context and tokens.
3. **BK1 and BK0 start before their prerequisites are Done, if the owner
   approves it at this plan's review.** The Program takes an item whose
   prerequisites are all Done (section 2.2), and its default is to wait;
   starting early is a departure that only the owner's approval of this
   plan makes, recorded then as a status-log row, not a precedence this
   plan claims. The reason to ask: the owner's request of 2026-10-31 for a
   Brakeman usable before Monday. PR-1 to PR-8 use only what `main` has;
   the parts that need DS2 (PR-10, PR-11) and C9 (PR-9) wait for them, and
   BK1 and BK0 are Done only after those PRs and their acceptance. Without
   the approval, PR-1 to PR-8 wait for DS2 and C9 as the rest do.
4. **A turn is a one-member run Yardmaster launches**, not a managed task
   and not a bare session. A run gives Brakeman Yardmaster's launch path,
   the guarded typing of its line, Stop, the run page and the records with
   no new execution code; a managed task would need a worktree and a
   commit as its result, which a brief is not.
5. **Every turn is a fresh session.** Nothing Brakeman knows lives in a
   conversation: it reads its request, the earlier briefs and their coverage
   from the data directory, so a restart or another model changes nothing
   it can know. The cost is the agent's start-up on each request.
6. **Brakeman's records are JSON documents in the data directory**
   (`brakeman/`), the newest 500 requests kept, not a SQLite database:
   they are written a few times a turn by one server, read as a list of
   hundreds, and carried by the export as plain files.
7. **Brakeman writes nothing to Dispatch or the crews in BK1.** Its drafts
   are forms a person edits and makes work (`make`, under the actor
   `admin` with the person's name), as DS1's and DS2's plans already
   require of agents' drafts (DS2's decision 14). BK2's grants add four
   operations and nothing else.
8. **Every fact names evidence, resolved when the brief is kept and beside
   what the turn read.** A brief
   with a ref that names nothing, or lies outside the scope, is refused
   with the ref, so the agent fixes it in its turn; a brief is never kept
   with claims a person cannot follow.
9. **What Brakeman may see by default**: runs, Dispatch, sessions and
   touched files on; terminal screens off, since a screen can show what no
   record holds (a secret typed, a private file); scope all of it. The
   owner turns screens on in Settings.
10. **The account is a folder reference**: the agent's own login folder
    (its adapter's `CLAUDE_CONFIG_DIR` or `CODEX_HOME`), or its default
    login; Conductor stores no credential and keeps no account registry
    (AC1's later).
11. **The model through the adapter's own flag**, for Claude Code and Codex
    only, each verified against the CLI; no free arguments, so a model is
    never passed to an agent that would read it as something else.
12. **The agent CLI's permissions: read-only, never yolo.** A worktree is
    not a sandbox and Brakeman's folder is the person's choice, so a turn
    always runs in its adapter's read-only mode (Claude Code's rule
    against the tools that write or run commands, Codex's read-only
    sandbox, each checked against the CLI), an agent without one cannot
    be Brakeman's, and yolo is never applied; under Claude Code,
    Conductor's MCP tools allowed without asking when the person ticks it
    (the CLI's per-server rule); under Codex, what the PR finds the CLI
    does, said in Settings.
13. **One turn at a time**, 8 queued, and a budget of 15 minutes and 300
    reads a turn and 40 turns a day, each changeable in Settings within
    its bounds.
14. **A decision card's controls are Conductor's**: a question is answered
    on its session page, the one place a question is answered today, until
    DR1 and Program item MB0 give decisions a page of their own; a task's
    commands are those its state allows by the run page's rules, and carry
    the revision Brakeman read, so a run that moved is refused rather than
    acted on blind.
15. **MCP is Brakeman's boundary and the cards are Conductor's own**: a
    small versioned set (`decision/1`, `comparison/1`, `evidence/1`, the
    drafts and the handoff) rendered by the web app. A2UI, AG-UI and MCP
    Apps are not adopted now; they are worth evaluating once the cards
    have been used.
16. **Brakeman's tools only in Brakeman's sessions, and one `observe`**:
    a worker never sees Brakeman's tools, and Brakeman reads through one
    tool whose variants the guide describes, each sent to its own typed
    route, so neither carries a tool per read in its context.
17. **The guide is per caller and in sections**, its providers unknown
    until C9: an agent reads a 4 KiB overview and asks for more, rather
    than a large static prompt.
18. **Brakeman forms no crew and adds no member**: the self-crew routes and
    tools refuse a Brakeman session, so the sidekick never schedules.
19. **BK2's grants are a closed list of four**: launching an approved plan,
    requesting a supported repair, preparing a follow-up, routing a
    decision (to the operator, and to an HM1 label once HM1 serves them,
    PR-18); never approving, answering, accepting, closing or changing
    the configuration, whatever is granted.
20. **BK3's briefing times are local**: they fire while the server runs, in
    its local zone; a briefing that must fire while the machine sleeps is
    Program item AU1's (paid, from the cloud).
21. **The Monday cut is PR-1 to PR-8** (The PRs, PR-3 as three); the rest
    waits on the prerequisite each names.
22. **BK1's first turns launch before `ext.Host` exists, if the owner
    approves it at this plan's review.** Boxcar constraint 1 sends
    Brakeman through `ext.Host`, which ext's PR-4 (#476) has not built;
    PR-3c to PR-8 launch through `launchTurn`, the engine and
    `crew.Launcher`, never `os/exec`, and PR-17 puts `ext.Host` in front
    of it once #476 merges, before any BK2 launch. Without the approval,
    PR-3c waits for #476 and PR-17 folds into it.

No question is left open: each answer above lets the plan stand, and the
owner overturns any of them with one row.

## Acceptance

**BK1's boundary**, in this plan's words: a briefing produced by the
person's own agent, with linked evidence and a precise decision card, that
is still useful after a restart from Conductor's records. It is observed,
not ticked:

- Once PR-7 is merged, on the owner's `conductor serve` with Claude Code
  (and once with Codex) on the owner's own subscription, configured in
  Settings: after an evening of real managed crews (one with a check that
  fails until a retry, one with a task whose attempts are spent, one whose
  member asks a question and is left unanswered overnight), "Brief me
  since 18:00" yields a brief whose facts link to the runs, the tasks, the
  failing check's receipt (its log hash equal to the step log route's) and
  the open question; that tells the quiet session from the failed task;
  and whose decision card for the spent task offers the commands its state
  allows (Retry, and Accept anyway when it submitted a result), one of
  which the owner presses with its receipt.
- "Draft tickets" from a real ticket's discussion yields drafts the owner
  edits and makes, with their blocking relation; "Propose a crew" yields a
  crew that saves and opens in the editor.
- The server is restarted and the agent's model changed; "What changed
  since your last brief?" reads the earlier brief and its coverage and
  answers from them, citing only what happened since.
- The observation, and what was missing, slow or wrong, goes into
  `docs/features.md` (PR-8, or a later docs PR of the stream); a bug the
  stream caused is fixed in the stream, a defect outside it filed.
- BK1 is Done when that observation is made again after PR-11, with a
  ticket launched from an approved plan revision among the evidence (DS2,
  its prerequisite), and the owner says the brief answered what happened,
  how Conductor knows, and what needs the owner.

**BK0's boundary**, in this plan's words: an agent told nothing but to ask
the running server learns from it what this build can do and which
providers it has registered. Observed after PR-2 for the
first part: a Codex session with nothing but "call the guide tool" lists
what it may do there and the limits it must keep, and a plain session's
guide differs from an attempt's; after PR-9 for the second: the official
app's build lists its providers, and a plain build says there are none.
BK0 is Done then.

**BK2's boundary**, in this plan's words: a granted proposal runs and
leaves its receipt, a gate stays in force when Brakeman's act would pass
it, and what Brakeman may read never lets it change anything. Observed
after PR-13: Brakeman, granted `launch_approved_plan`,
launches a ticket's approved revision and the run page and the brief show
the receipt; asked to launch an unapproved revision, it is refused and says
so; with every read granted and no grant to act, every act is refused; with
a grant and a narrowed visibility, the act on what it cannot see is
refused.

**BK3's boundary**, in this plan's words: an event wakes Brakeman, it
briefs and goes quiet again without passing its budget, and after a
restart it carries on from Conductor's records rather than from any
agent's conversation. Observed after PR-15: a task failing overnight wakes it once, a
second failure inside the window joins the same brief, the cooldown holds
the next, the day's budget stops it with words; the server restarted while
a batch was gathering fires that batch once; with Brakeman's agent logged
out, the role says it is unavailable while the crews run and their
questions are answered as before.

What needs the owner: the review of this plan and its decisions, with
decisions 3 and 22 approved or refused in that review; cutting
the issues; the owner's own agent and subscription for every observation
above; the merges. A2UI, AG-UI and MCP Apps, and decision 2's role
session, are the owner's to reopen.
