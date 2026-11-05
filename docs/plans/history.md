# History: what was said, decided and checked, found from the palette (Program item HS1)

A person who comes back to a project today can see what is running and what
needs them, but not what happened before: a plain session leaves nothing
behind once the server restarts, a managed run's evidence is read run by
run, Dispatch searches its own tickets, and Brakeman's briefs sit on their
own page. Nothing answers "which session investigated this failure", "which
design was rejected", "what was last verified" or "did another crew solve
this". This plan adds **history**: a durable record of every session the
server runs or hosts (its agent and that agent's own session, its host, its
folder and commits, and the words Conductor saw pass through it), and one
**index** over that record, the ledger, the run records, Dispatch and
Brakeman's briefs, searched from the command palette with a `~` prefix and
from a History page. Every row says where it came from, on which machine, in
which project, at which revision, when, and whether the thing it names can
still be opened or resumed; and every row is one of three kinds of evidence
that never mix: what was **said** (conversation), what was **decided** (a
person's recorded act), what was **checked** (a receipt the server made by
running something), beside the plain **records** the three are about.

It is Program item HS1 (the Program, section 4.3), written against `main` at
9723fdc6 (PR #601, 2026-11-05) after reading the session, registry,
activity, attention and resume code (`internal/session`,
`internal/api/resume.go`), the event hub and its sinks
(`internal/api/events.go`), the sync adapter's commit hooks
(`internal/api/sync.go`), the ledger (`internal/crew/ledger`), the engine's
failed-check handoff (`internal/crew/failedcheck.go`), the run records
(`internal/crew/record.go`), Dispatch's store and its FTS5 search
(`internal/dispatch`, `query.go`), DS2's candidates and ticket evidence
(`internal/api/candidates.go`, `dispatch_evidence.go`), Brakeman's store,
briefs and evidence references (`internal/brakeman`,
`internal/api/brakeman_refs.go`), the export's table of what it leaves out
(`internal/export/leftout.go`), the operating guide
(`internal/api/guide.go`) and the palette (`web/app/utils/palette.ts`,
`usePalette.ts`, `usePaletteRows.ts`, `CommandPalette.vue`). It is reviewed
before any of its code; the PRs at the end are cut into issues after the
review, one issue, one branch `issue-N`, one PR each (AGENTS.md).

**What is on main and what is not.** HS1's one prerequisite, DS2 (ticket to
evidence, `docs/plans/dispatch-evidence.md`), is Done and on main, and so is
everything PR-1 to PR-8 build on: DS1's Dispatch with its search, the ledger
with its check receipts and their `where` (C10, `docs/plans/runtime.md`),
the export (C6, `docs/plans/ext.md`), the event hub's sinks (C3) and
Brakeman's first slice with its briefs and reference grammar (BK1's PR-1 to
PR-8, `docs/plans/brakeman.md`). Three things HS1 indexes are not on main:
DR1's decision records (`docs/plans/decisions.md`, Planned), RV1's review
batches and findings (`docs/plans/review-repair.md`, Planned) and Brakeman's
own reads past its first slice (`docs/plans/brakeman.md`, PR-9 to PR-16,
being built now). PR-9, PR-10 and PR-11 wait on them, each named there; the
acceptance boundary needs none of them.

**Names.** The railroad names keep their meaning (the Program, section 1.2):
Conductor is the application, Yardmaster the agent harness whose ledger
holds a managed run's attempts and receipts, Dispatch the ticketing, whose
items are tickets, Brakeman the sidekick agent. "History" is this item's
word for the record and the search; it is told apart from three things the
repo already calls history: a Dispatch ticket's field history
(`DispatchHistory.vue`, the `changes` log), a session's activity log (the
inspector's Activity) and the shell's history. Where this plan says "recipe"
it means the catalog's agent recipes, in particular an agent's **session
recipe** (`catalog.SessionRecipe`: how an agent names and resumes its own
conversation); EN1's project recipes (project manifests, D6) are not
touched. An attempt's checkout is a worktree, never a workspace (D5).
"Provider" in this plan means the agent's maker as Conductor knows it (its
catalog entry, adapter, version and session recipe), never an `ext`
provider; the plan says "ext provider" for those.

## Articles it serves

1. No licence check: nothing here asks for a plan, a tier or an ext
   provider. The one switch, `history.sessions`, is a privacy choice with
   both values free.
2. Useful with nothing else: history is local evidence over the local stores
   (article 2 names local evidence among the free build's parts), with no
   account and no network; it makes Yardmaster's, Dispatch's and Brakeman's
   records findable together.
3. Nothing is gated by a flag: there is no paid part. Shared team history is
   TS1's (`docs/plans/team-history.md`), a hosted service's work.
4. Inspection and export grow, and stop, revoke and required approvals are
   unchanged: the search is an inspection surface; the session records are
   plain JSON files in the data directory that `conductor export` carries; a
   record can be forgotten (deleted) by the person; resuming from a record
   goes through the same checks as the resume routes and launches nothing a
   person did not ask for. Nothing here answers a question, approves,
   accepts or stops anything.
5. Dependencies point one way: no new module. `internal/history` becomes the
   third importer of `modernc.org/sqlite` (beside the ledger and Dispatch)
   and imports nothing else of this module; `internal/journal` imports the
   standard library and `internal/store`.
6. No feature build tag: a plain `go build` carries all of it.
7. Not touched: no `ext` interface is called, defined or changed. The
   ContextProvider's free default (the touched index) is read as it is;
   nothing here is spliced into a task file.
8. Not touched: no hosted dependency exists in this item.
9. `CGO_ENABLED=0` stays: the index is `modernc.org/sqlite`, as the ledger
   is.
10. The repository contract holds: this plan before the code; race tests per
    package; vitest and Playwright for the palette and the pages; no new
    dependency, so `make notices` has nothing to regenerate; no frame or
    control message, so `internal/proto` and `web/app/utils/protocol.ts` are
    untouched and `docs/protocol.md` gains the History routes (PR-1, PR-4,
    PR-5).
11. The railroad names keep their meaning, and "history" is told from the
    three existing uses (above); "workspace" is not used.
12. Execution stays behind one seam: a resume from a record starts its
    session the way every session starts, through `createLocalSession` and
    the server's `ext.Runtime`; a managed attempt is never resumed from
    history, only continued through the run's own route (the engine and
    `crew.Launcher`). "Boxcar constraints" below takes the eight one by one.

## Why and what happens

The Program's product questions (section 1.1) include which code, tests and
prior decisions matter, and what result was produced, what checks ran and
what was accepted; its work model (section 1.4) keeps a ticket's status, a
process's state, a task's outcome, its verification and a person's
acceptance as separate facts, and says an idle terminal or a conversational
"done" establishes none of the later ones. History is where those facts are
found again later, side by side and never merged. On `main` today:

- A server session lives in the registry (`session.Registry`, in memory).
  Once it ends it stays listed until the server stops; after a restart
  nothing of it is left but, for a crew member, its run record's
  `MemberState` (the session id and the agent's own session,
  `AgentSession`). A plain session's agent, folder, the lines typed into it,
  the questions it asked and what its agent reported are gone.
- An agent's own conversation is known by its id (`AgentSession`: set at
  launch, reported by its hooks, or resumed) and its session recipe says how
  to resume it; Resume works only on a session still in the registry
  (`handleResumeSession`), so the conversation of a session that ended
  before a restart cannot be resumed from Conductor, though the agent still
  has it.
- A managed run's evidence is complete in the ledger: each attempt (its
  session, worktree, start and result commits, its submitted result), each
  check receipt (argv, the commit it checked, the exit, a log of at most 1
  MiB with its SHA-256 and the last 2 KiB as `tail`, `where_ran`), each
  command's receipt (pause, cancel, retry, accept, with its actor), the
  members' messages, blockers and checkpoints. And the engine already says
  which attempt was handed a failed check: the attempt after one that failed
  at a check gets that receipt in `task.json`'s `previous.check` and its
  whole log (DS2's PR-5, `failedcheck.go`). That attempt is the session that
  investigated the failure, but nothing lets a person find it from the
  failure.
- Dispatch searches tickets and comments (FTS5, `tickets_fts`) and keeps
  plan revisions, their approval and withdrawal, launches, deliveries with
  their verification receipt and acceptances; Brakeman keeps its briefs with
  every fact's evidence references. Each is searched, if at all, on its own
  page.

After this item ships:

- **The palette's History.** Typing `~` in the palette (the seventh prefix,
  beside `> # : @ ! ?`) searches history. `~ TestLoginRedirect` lists,
  newest relevant first, every row whose text holds those words: the failed
  check receipt whose log names the test, the attempt that was handed it,
  the session whose agent reported it, the ticket comment that mentions it.
  With nothing typed after `~`, the four questions are rows of their own
  (Failures and who investigated them; Rejected designs; Last verified here;
  Sessions on this machine), each a ready query. In the plain search, from
  three characters typed, a History group shows the five best rows after the
  Tickets group, with "All history matches (~)" to switch.
- **Each row says what it is.** A badge: **Conversation** (said),
  **Decision** (decided), **Checked ✓** or **Checked ✗** (a receipt, passed
  or failed), or none for a record. A detail line with the six fields: the
  source ("Claude Code session", "check receipt", "plan revision"), the host
  ("lab-box", or "nate@mac via conductor host"), the project ("CND", or the
  repository's folder when no Dispatch project holds it), the revision
  ("abc1234 on fix/login", "r2", "rev 14"), when ("2 h ago", the exact time
  on hover) and the availability ("live", "ended, listed", "record only",
  "host away", "log kept", "worktree removed"). Under a failed receipt, its
  links: "Investigated by build #2 (Claude Code, attempt 2) · passed at
  def5678". A sentence said in several sessions is one row with "said in 3
  sessions": still Conversation, never promoted.
- **Enter opens the evidence.** A check receipt opens the run page at its
  task, its attempt and that step's log, with the log's SHA-256 (the run
  page gains `attempt` and `step` in its URL); a session opens its page
  while it is listed and its record page after; a ticket, a comment, a plan
  revision or a brief opens where their own pages show them. → offers Open
  the session, Resume (only where the agent's session recipe, the host and
  the run allow it, and why not otherwise), Find this elsewhere (the same
  command's receipts in other crews' runs, passed first: "did another crew
  solve this"), Copy the reference.
- **Session records.** Every session the server runs or hosts gets a record
  in the data directory (`history/sessions/<id>.json`) from its start: the
  agent with its version and its own session id, the host, the folder, the
  repository, the branch and the commit at its start and end, its launch
  (arguments and the yolo choice; the variables' names, never their values),
  its crew, run, task and attempt, and its conversation evidence as
  Conductor saw it: the lines typed into it, the questions it asked and how
  each was answered, what its agent reported (progress, errors, artifacts,
  handoffs, refused tools), the files it reported touching and its chat, at
  most 64 KiB, the newest 2,000 sessions kept. The record page
  (`/history/sessions/<id>`) shows it, offers Resume from the record where
  the agent's recipe allows it (a resume after a restart), and Forget.
- **The History page.** `/history` runs the same search with filter chips
  (kind of evidence, outcome, project, host, agent, since) and every result,
  paged, grouped under the session or run each row belongs to; on a phone it
  is one column.
- **The CLI.** `conductor history
  search|show|sessions|session|forget|resume` does the same with the
  workbench token, and the operating guide lists those operations for the
  admin.

Nothing here changes what any store holds about runs, tickets or briefs, and
nothing reads the index to decide anything: it is a derived cache that can
be deleted at any time and is rebuilt from the stores. Conductor never opens
an agent's own transcript files (Claude Code's projects directory, Codex's
sessions directory), as it never has (`internal/session/plans.go`: the
agent's config directory is never opened beyond its plans); a session's
conversation evidence is what passed through Conductor.

## The changes

### `internal/journal` (new, PR-1): the session records

A package of the session records, standard library and `internal/store` only
(no HTTP, no session, no process; the API is its caller, as for Dispatch and
Brakeman).

**The record** (`record.go`), one JSON document per session,
`history/sessions/<id>.json` (the session's 16 hex digits, so the name
passes `store`'s name pattern), the directory 0700 and each file 0600 (the
store's own modes):

```go
type Record struct {
	V        int        `json:"v"`        // 1
	ID       string     `json:"id"`       // the session's id
	Seq      int64      `json:"seq"`      // bumped on every write: the index's mark
	Name     string     `json:"name"`
	Kind     string     `json:"kind"`     // session.Kind as the session said it
	Agent    Agent      `json:"agent"`
	Host     Host       `json:"host"`
	Cwd      string     `json:"cwd"`
	Repo     string     `json:"repo,omitempty"`   // the repository's top, gitcli.TopOf
	Branch   string     `json:"branch,omitempty"`
	Start    string     `json:"startCommit,omitempty"` // HEAD at the start, a full id
	End      string     `json:"endCommit,omitempty"`   // HEAD at the end
	Launch   *Launch    `json:"launch,omitempty"` // server sessions only
	Crew     *Crew      `json:"crew,omitempty"`
	ResumedFrom string  `json:"resumedFrom,omitempty"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt,omitzero"`
	Ending    string    `json:"ending,omitempty"` // "exited (exit 0)", "stopped", "lost"
	Entries  []Entry    `json:"entries"`
	Dropped  int        `json:"dropped,omitempty"` // entries the bound let go, oldest first
	Files    []File     `json:"files,omitempty"`
	Tools    map[string]int `json:"tools,omitempty"` // tool_use counts by tool, at most 64 tools
	Resumes  []Resume   `json:"resumes,omitempty"`
}
type Agent struct { ID, Name, Adapter, Version string; Session *AgentSession }
type AgentSession struct { ID, Source string; Resumable bool } // as session.AgentSession
type Host struct { Name, User string; Hosted bool }
type Launch struct { Args []string; EnvNames []string; EnvSHA256 string; Yolo, Renamed bool }
type Crew struct { RunID, CrewID, Member, Task string; Attempt int; Brakeman string }
type Entry struct { Seq int64; At time.Time; Type, ByName, Message, Op, Path, Tool, URL, To string; Choices []string; Answer string }
type File struct { Path, Op string; Count int; Last time.Time }
type Resume struct { CommandID, Actor, Session, State, Error string; At time.Time } // pending | started | failed
```

What an entry is taken from, by the activity entry's type
(`session.ActivityEntry`): `input` (a line typed, `Op: typed`, or an answer,
with the question it answered), `attention` when it sets a question (its
message and choices) and when it ends one (how: answered, withdrawn, held
over), `progress`, `artifact`, `handoff`, `error` and `tool_denied` (the
agent's reports), `status` (start and end), and chat messages (the session's
chat, which reaches no hub sink today: PR-1 gives the hub one call for the
journal, below). `join`, `leave` and `link` are never kept (who watched is
not evidence of the work, and a link's name may be a person's). `tool_use`
is counted by tool in `Tools`, not kept one by one. A `file` entry folds
into `Files`, one row per path and op with its count and last time, at most
500 rows (the oldest by `Last` let go, counted in `Dropped`), and a path the
session's deny list covers is never written (the deny list `/api/touched`
applies, `session.DenyList`).

**Bounds** (`bounds.go`): an entry's texts as the session bounds them
already (`MaxAttentionMessage` 500 bytes, `MaxEventPath`, `MaxEventURL`,
`MaxEventTool`); a chat line at most 2 KiB; a record at most 64 KiB as
encoded (`MaxRecord`): past it the oldest entries go first, counted in
`Dropped`, then the oldest `Files` rows; `Launch.Args` at most 64 elements
of 4 KiB (the member-args bounds); at most 16 `Resumes`; at most 2,000
records (`MaxRecords`). Worst case 128 MiB on disk; a typical record is a
few KiB.

**The launch's variables.** A session's launch may carry variables
(`session.Launched.Env`), which may be secrets. The record keeps their
names, sorted, and one SHA-256 over the sorted `name=value` lines
(`EnvSHA256`), never a value, the rule DS2's `agentSelection` follows. A
resume from the record therefore cannot restore them; PR-5 says what it does
instead.

**The journal** (`journal.go`): `Open(st *store.Store) (*Journal, error)`
takes `history/` (`store.Sub`, 0700) and its owner lock `history/owner.lock`
(`store.OwnerLock`, as Brakeman's store and the ledger take theirs; tried
once, then in the background as the ledger's `awaitLock` does). Held: the
journal writes. Not held (another server owns the data directory):
`ReadOnly()` says so, reads are served from the files, nothing is written,
and the server logs once that its sessions are not recorded. Methods, each
safe for concurrent use:

- `Begin(r Record)`: the record of a session as it is added, written at once
  (a session's record exists before its first entry is kept).
- `Note(id string, e Entry)`, `Chat(id string, c Entry)`, `Touch(id string,
  f File)`, `Tool(id, name string)`: appended in memory, the record marked
  dirty; never waits on a disk (the caller is the hub's sink, which must
  never wait).
- `End(id string, end Ending)`: the end commit, the time and how it ended;
  written at once.
- A writer goroutine writes each dirty record at most once every 5 s
  (`flushEvery`), and every one at `Close`, so a crash loses at most the
  last 5 s of entries, and a record is never written more often than that
  however chatty its agent.
- `Get(id)`, `List(after string, limit int)` (newest first by `StartedAt`,
  pages of at most 100), `Forget(id)` (refused for a live session,
  `ErrLive`), `ForgetAll()` (every record not live).
- `Prune()`: past `MaxRecords` the oldest ended records go; a live one, and
  one with a `pending` resume, never.
- `Recover()`, at open once the lock is held: a record with no `EndedAt`
  whose session no server holds (the lock's holder before this one is gone,
  so none can) is ended `lost` at its last write's time. Run once, before
  the first `Begin`.
- `Seq` on every write, from a counter in `history/state.json` written
  before the record, so a mark never goes back after a restart.

A record of a newer `V` is read as it is, never rewritten (no `Note` reaches
it: its session is not this server's), listed with `newer: true`.

**Config** (`internal/config`): `history.sessions`
(`CONDUCTOR_HISTORY_SESSIONS`, `1`/`0`), on by default. Off: no record is
begun, the records kept stay until forgotten, and the index still covers the
runs, Dispatch and the briefs (which those stores keep anyway). A switchyard
keeps no journal and no index (its sessions are other people's, published to
it), as it keeps no Dispatch.

**The API's part of PR-1** (`internal/api/history_journal.go`): the server
opens the journal with its data directory (none without one); `Begin` in
`createLocalSession` once the session's `Info` is made and before
`session.NewLocal` (so the record exists before the session can keep an
entry; a session the registry then refuses, `too_many_sessions`, has its
record removed), and for a hosted session (where `internal/signal`'s hub
adds it to the registry, `hub.go`: a new `OnAdded` callback the API sets,
called after the add, as `OnRemove` is called after a removal), from its
`Info` and, for a server session, `Local.Launched()`; the agent's version
from the probe cache (`cachedIdentity`, "" while it is cold, said so on the
record page); the repository, branch and HEAD read with `gitcli` (argv, the
C locale, 2 s each, off the session's start path on a goroutine of its own);
a sink on the event hub (`eventHub.addSink`), which hands every activity
entry of every session, server and hosted alike, to `Note`/`Touch`/`Tool`
and the session's end (its `status` entry, or its removal from the registry,
`OnRemove`) to `End`; the hub's `chat` calling the journal's `Chat` for a
session's chat (one call, never waiting; run chat stays the run record's).
The agent's own session id is copied in whenever the session's `Info`
changes it (the `localChange` hook every attention change already calls).
The routes:

| Route | Who | Answers |
|---|---|---|
| `GET /api/history/sessions?after=&limit=` | admin | `{records: [RecordSummary], next?, readOnly?}`: id, name, agent, host, cwd, repo, branch, start and end, how it ended, counts; limit 1 to 100, default 50 |
| `GET /api/history/sessions/{id}` | admin | the record whole (at most 64 KiB) with `live: true` while the registry lists it running |
| `DELETE /api/history/sessions/{id}` | admin | `204`; `409 live_session` for a session still running; `404` |
| `DELETE /api/history/sessions` | admin | `{forgotten: n}`: every record not live |

All four are `requireAdmin`; a share link's viewer, a session's own token
and Brakeman's principal reach none of them. A server with no data directory
or a switchyard answers `404 history_unavailable`.

### `internal/history` (new, PR-2): the index

A package of the index and its search, the standard library and
`modernc.org/sqlite` only (no HTTP, no session, no other store): it is told
entries and asked queries, and knows nothing of where entries come from.

**The database** `history/index.sqlite`, 0600, opened as the ledger opens
its own (WAL, `synchronous=NORMAL` since it is a cache, foreign keys, a busy
timeout, one connection), schema version 1:

```sql
CREATE TABLE schema_version (version INTEGER NOT NULL);
CREATE TABLE sources (
  key TEXT PRIMARY KEY,          -- "run:<id>", "session:<id>", "ticket:<KEY-n>", "brief:<br-n>"
  mark TEXT NOT NULL,            -- what the source said when it was read: a revision, a seq
  indexed_at TEXT NOT NULL,
  bytes INTEGER NOT NULL         -- the text its entries hold
);
CREATE TABLE entries (
  id INTEGER PRIMARY KEY,
  source TEXT NOT NULL REFERENCES sources(key) ON DELETE CASCADE,
  kind TEXT NOT NULL,            -- the kinds table below
  class TEXT NOT NULL,           -- said | decided | checked | record, from the kind alone
  ref TEXT NOT NULL,             -- the evidence reference (below)
  seq INTEGER NOT NULL DEFAULT 0,-- an entry's place in a session record
  anchor TEXT NOT NULL,          -- the ref of the session, task or ticket it belongs to
  at TEXT NOT NULL,              -- RFC 3339, UTC: when it happened
  host TEXT NOT NULL DEFAULT '', host_user TEXT NOT NULL DEFAULT '', hosted INTEGER NOT NULL DEFAULT 0,
  where_ran TEXT NOT NULL DEFAULT '',  -- a receipt's or an attempt's where (C10), as the row says
  project TEXT NOT NULL DEFAULT '',    -- a Dispatch project key
  repo TEXT NOT NULL DEFAULT '',       -- a repository's top
  revision TEXT NOT NULL DEFAULT '',   -- the label: "r2", "rev 14", a branch
  commit_id TEXT NOT NULL DEFAULT '',  -- a full git id when the entry is about one
  agent TEXT NOT NULL DEFAULT '', agent_version TEXT NOT NULL DEFAULT '', agent_session TEXT NOT NULL DEFAULT '',
  crew TEXT NOT NULL DEFAULT '', run TEXT NOT NULL DEFAULT '', ticket TEXT NOT NULL DEFAULT '',
  outcome TEXT NOT NULL DEFAULT '',    -- passed | failed | cancelled | approved | withdrawn | ...
  actor TEXT NOT NULL DEFAULT '',
  subject_key TEXT NOT NULL DEFAULT '',-- a check's SHA-256 of its argv: the same command elsewhere
  digest TEXT NOT NULL,          -- SHA-256 of the body folded: repeats grouped, never merged
  title TEXT NOT NULL, summary TEXT NOT NULL DEFAULT '', body TEXT NOT NULL DEFAULT '',
  origin TEXT NOT NULL DEFAULT ''-- reserved for TS1: '' for this server's own
);
CREATE TABLE links (
  from_ref TEXT NOT NULL, rel TEXT NOT NULL, to_ref TEXT NOT NULL,
  source TEXT NOT NULL REFERENCES sources(key) ON DELETE CASCADE,
  PRIMARY KEY (from_ref, rel, to_ref)
);
CREATE VIRTUAL TABLE entries_fts USING fts5(title, summary, body, content = 'entries', content_rowid = 'id',
  tokenize = 'unicode61 remove_diacritics 2', prefix = '2 3');
CREATE INDEX entries_at ON entries(class, at);
CREATE INDEX entries_anchor ON entries(anchor);
CREATE INDEX entries_ref ON entries(ref);
CREATE INDEX entries_subject ON entries(subject_key) WHERE subject_key != '';
CREATE INDEX entries_digest ON entries(digest);
CREATE INDEX links_to ON links(to_ref, rel);
```

External content (not Dispatch's contentless table) because a row's snippet
is drawn from its text (`snippet()`), which a contentless table cannot give;
the FTS rows are written and deleted in the same transaction as their
entries, by the package, with no trigger. An index of another schema
version, or one that fails its quick check at open, is deleted and made
again empty (it is a cache: nothing is lost that the stores do not hold),
logged; a `history/index.sqlite` a newer Conductor wrote is treated the
same, and the newer one rebuilds it again when it next runs.

**The kinds and their class** (`kinds.go`): one table, `classOf(kind)`, the
only place an entry's class is decided. An entry's class comes from what
made it, never from its words, how often they recur or who repeats them:

| Class | Kinds | Made by |
|---|---|---|
| `record` | `session`, `run`, `task`, `attempt`, `ticket`, `plan`, `launch`, `step` (a project manifest's setup or service step that failed) | the stores' own objects |
| `said` (Conversation) | `session.typed`, `session.answer`, `session.question`, `session.report`, `session.file`, `session.chat`, `attempt.result` (a submitted summary and issues), `review.verdict` and `review.finding` (an agent reviewer's), `attempt.checkpoint`, `attempt.blocker`, `message`, `comment`, `brief.item` | a person's or an agent's words as Conductor received them |
| `decided` (Decision) | `command` (pause, unpause, cancel, retry, accept, accept anyway, with its actor), `plan.approved`, `plan.withdrawn`, `plan.superseded`, `ticket.accepted`, `ticket.unaccepted`, `ticket.closed`; PR-9 adds `decision.response`, PR-10 `review.batch` | a person's act the server recorded with a receipt |
| `checked` (Checked) | `check` (a task's check), `verify` (a project manifest's verify step), `candidate.verified` (a result commit whose every check passed), `delivery.verified` (a delivery whose candidate's receipt is kept) | a receipt of something the server ran on an exact commit |

A review's verdict is an agent's words (`said`) even though the engine acts
on it: what the engine did is the task's next attempt and its `reopened`
link, a record. A Brakeman brief's fact is `said` too (Brakeman's account),
linked by `cites` to the records it names, which are indexed in their own
classes. Nothing in the table is `checked` but a receipt.

**Entries in** (`put.go`): `Replace(ctx, source Source, entries []Entry,
links []Link) error` replaces a source's entries and links in one
transaction (`BEGIN IMMEDIATE`: the old rows and their FTS rows deleted, the
new inserted, the source's mark and bytes written), all or nothing;
`Remove(ctx, key)` deletes a source and, by cascade, its rows; `Mark(ctx,
key) (string, bool, error)` reads a source's mark. Each entry is checked
before the transaction: its kind in the table (an unknown kind refused,
`ErrKind`), its class set by the package (a class given by the caller is
refused: a caller cannot choose one), its ref and anchor at most 512 bytes,
its text bounded by kind (`bounds.go`):

| Text | Bound |
|---|---|
| a title | 200 bytes |
| a summary | 1 KiB |
| a session entry's body, a message, a checkpoint, a blocker, a finding | 4 KiB |
| a submitted result, a ticket's description and criteria, a comment, a brief's item | 16 KiB |
| a check's body: its argv, the log's first 4 KiB and its last 12 KiB (the ledger's `tail` alone for a check that passed) | 16 KiB |

**The budget.** At most 200,000 entries and 256 MiB of text (`MaxEntries`,
`MaxBytes`); a `Replace` that would pass either drops the oldest entries by
`at` (whole sources first when a source is wholly older than the cut) in the
same transaction, and `Stats()` says the oldest time still indexed and how
many entries were let go. Old history is dropped by age alone, never by
class.

**The search** (`query.go`): `Search(ctx, q Query) (Page, error)`. A `Query`
is parsed from the person's line by `ParseQuery(s)` (at most 512 bytes, at
most 16 terms):

- words, each a term (Dispatch's `matchTerms` rule: a term of one character
  is dropped; the last word typed is a prefix from two characters), and
  `"quoted phrases"`; every term must match (AND), in the title, the summary
  or the body;
- filters, each `name:value`, a comma for alternatives:
  `class:said|decided|checked|record` (also the words `conversation`,
  `decision`), `kind:`, `outcome:passed|failed|…`, `project:CND`, `repo:`
  (words of the repository's path), `host:`, `agent:` (a catalog id),
  `crew:`, `run:`, `ticket:CND-12`, `session:`, `since:` and `until:` (`7d`,
  `24h`, `2026-11-01`);
- an unknown filter name is a word (so `http://x` searches for it), and a
  malformed value is refused with words (`ErrQuery`).

Order: with words, FTS5's `bm25(entries_fts, 4, 2, 1)` (title, summary,
body), then newer first; with filters alone, newer first. Class never weighs
in the order. A page holds at most 100 entries (default 20) after a cursor,
with the total when it is at most 10,000 ("more than 10,000" past that,
which the count stops at). Each entry comes with `snippet()` of its best
column (at most 300 bytes, the match marked), its `repeats` (how many
entries of the same digest and class exist and in how many anchors: "said in
3 sessions", computed for the page's rows only), and at most 8 of its links
each way. `Similar(ctx, id, limit)`: the entries whose `subject_key` is the
entry's (the same command's receipts), outside its run's chain of
continuations (given by the caller), passed first then newest, then those
whose body matches the entry's distinctive line (the last non-empty line of
a failed check's tail, as a phrase). `Entry(ctx, id)` and `Links(ctx, ref,
limit)` (at most 100 each way) read one row.

**Measured.** `BenchmarkSearch` over 100,000 entries of mixed kinds (a
fixture written by the test): a two-word search, a filtered page, a phrase,
a prefix of two letters; the target is 50 ms at the median for each on the
Linux box (Dispatch's target and box), measured in PR-2 and recorded in its
body, the numbers past it reported without a target as Dispatch's PR-4 did.

### `internal/api` (PR-3, PR-4): the indexer, the sources, the routes

**The commit hooks shared** (`commits.go`, PR-3). The data directory's
observer (`store.Observe`), the ledger's `OnCommit` and `OnPrune` and
Dispatch's `OnCommit` each hold one function, and the sync adapter (C6)
holds all three. PR-3 puts one small fan-out in front of them (`commitFan`):
it installs the one function each hook takes and calls, in order, every
observer registered with it (the sync adapter first, unchanged, then the
indexer), each of which only marks something pending and never waits, as the
hooks require. The sync adapter's behaviour and tests do not change;
`TestTheFanCallsEveryObserverInCommitOrder` holds the order.

**The indexer** (`history_index.go`, PR-3). Opened once the journal holds
`history/owner.lock` (one writer of the index: the lock's holder; a server
without it serves searches from the index as it is, read-only, and says so).
A dirty set of source keys, filled by the commit hooks (a run's id from the
ledger's commit and prune and from `runs/<id>.json` saves; a ticket's key
from Dispatch's changes; a session record from the journal's writes; a brief
from `brakeman/` saves), bounded at 10,000 keys: past that the set is
emptied and a full catch-up is asked instead, so nothing is dropped. One
goroutine takes the oldest key and indexes that source as it is now:

1. the source's mark is read first (the run's ledger revision,
   `ledger.Pointer`; the ticket's `rev` and its changes' last seq; the
   record's `Seq`; the brief request's update time), then the source;
2. its entries and links are built (the sources below), and `Replace` is
   called with the mark taken before the read, so a change between the two
   leaves the source dirty and read again, never marked as seen;
3. a source that is gone (a run pruned, a record forgotten, a ticket's
   project gone) is `Remove`d;
4. one source at a time, through each store's own read functions, so the
   ledger's one connection is held for one run's reads at a time and a
   transition waits at most that long.

At start, once the stores are open (after the ledger's recovery, inside the
`OnOwned` callback that DS2's reconcile uses), a **catch-up** compares each
source's mark with the index's and marks every source that differs or is
missing dirty, and every indexed source that no longer exists for removal;
an index made empty (first start, a version change, a deletion) is filled
the same way. Searches during a catch-up answer what is indexed with
`indexing: {state: "catching_up", behind: n}`. `POST /api/history/reindex`
(admin) deletes the index and catches up from nothing: `202 {indexing}`.

**The sources** (`history_sources.go`, PR-3 for sessions and runs, PR-4 for
Dispatch and briefs), each a function from what its store holds to entries
and links, every field of the six taken from the record itself:

- **A session record** (source `session:<id>`): a `session` record entry
  (title the name; summary the agent, folder and how it ended; host from
  `Host`; repo and branch; `commit_id` the end commit, else the start;
  agent, its version and its own session id), then one `said` entry per kept
  entry (`session.typed`, `.answer` with the question it answered in its
  summary, `.question` with its choices, `.report`, `.chat`) and one per
  `Files` row (`session.file`, its path as title), each anchored to
  `session:<id>` and placed by `seq`; links `resumed_from` to the session it
  resumed. The project is the Dispatch project whose folder (`Project.Cwd`)
  is in the same repository as the record's (`gitcli.TopOf` of both, cached
  per folder for the pass), as Brakeman's refs scope a project; a crew
  member's project is its run's.
- **A run** (source `run:<id>`, from its record `runs/<id>.json` and, for a
  managed run, the ledger's rows): a `run` record entry (the crew, the label
  or goal, the base commit, the outcome); per task a `task` entry; per
  attempt an `attempt` entry (its session, worktree, branch, start and
  result commits, its state and stage, `where_ran`) with `session_of` to
  `session:<id>`; its submitted result as `attempt.result` (summary and
  issues), a review's verdict and findings as `review.verdict` and
  `review.finding` with `reviews` to the attempt it reviewed; its checkpoint
  and blocker; each check receipt of phase `check` or `verify` as `check` or
  `verify` (`checked`, outcome `passed` or `failed` as `checkState` judges
  it: exit, signal, timeout, stability), title "`<check id>` on `<task>
  #<n>`", body its argv and log as bounded above, `commit_id` its subject,
  `where_ran` its row's, `subject_key` the SHA-256 of its argv); each
  command receipt as `command` (`decided`, its intent and actor); each
  message as `message`; a candidate whose checks all passed (DS2's
  `candidateOf` over the same rows) as `candidate.verified`; a failed setup
  or service step as `step`. Links: `continues` to the run it continues;
  `launched_from` to its ticket and plan revision (`Run.Ticket`); `handed`
  from a failed receipt to the attempt that was handed it; `passed_after`
  from a failed receipt to the first later receipt of the same check id on
  the same task that passed, in this run or along its continuations
  (`continuedBy`); `reopened` from a review's `needs_changes` to the task's
  next attempt.
- **Which attempt was handed which receipt** is the engine's rule, not a
  guess: PR-3 exports the two pure functions the engine's restart and
  continuation paths already use over the ledger's rows (`failedAtCheck` and
  `failingReceipt`, `internal/crew/failedcheck.go`) as `crew.FailedAtCheck`
  and `crew.FailingReceipt`, and the indexer applies them as `previousOf`
  does: the receipt that failed attempt n is handed to the task's next
  counted attempt in the run, or to the first attempt of the task in the
  continuation when attempt n was the run's last.
  `TestTheIndexHandsWhatTheEngineHands` runs a managed run with fake
  sessions and compares each `handed` link with the `previous.check` of the
  attempt's own `task.json`.
- **A ticket** (source `ticket:<KEY-n>`, PR-4, from Dispatch's composite
  read): a `ticket` record entry (title, description and criteria; its
  project; revision `rev N`; its owner as actor); each comment as `comment`
  (`said`); each plan revision as `plan` (`record`: the crew it was taken
  from, its input commit, `rN`) and its approval, withdrawal and
  supersession as `plan.approved`, `plan.withdrawn`, `plan.superseded`
  (`decided`, with the operator and the note); each launch as `launch` with
  `launched_from` back from its run; the acceptance and its withdrawal and a
  close by a person as `ticket.accepted`, `ticket.unaccepted`,
  `ticket.closed`; a delivery whose candidate's verification receipt is kept
  as `delivery.verified` (`checked`, with `delivers` to the candidate's
  attempt). A tombstoned ticket stays indexed, its record entry's
  availability `deleted`.
- **A Brakeman brief** (source `brief:<br-n>`, PR-4, from Brakeman's store):
  one `brief.item` per item (`said`, its kind, fact, interpretation,
  proposal or outcome, in its title: "Brakeman's interpretation"), with
  `cites` to each evidence reference the item names. Brakeman's own sessions
  and runs are indexed as any (their class `brakeman` on the run record
  entry) and filtered out of the default search unless `crew:brakeman` or
  `kind:brief.item` asks.

**Evidence references.** Each entry's `ref` and every link's ends are
written in the grammar of `docs/plans/brakeman.md` (Evidence references):
`run:`, `task:`, `attempt:`, `check:<run>/<task>/<n>/<id>/<k>`, `command:`,
`message:`, `session:`, `ticket:`, `comment:`, `brief:`, and the three that
plan's PR-10 adds for DS2's records as it spells them
(`plan:<KEY-n>/<revision>`, `launch:<id>`, `candidate:<run>/<task>/<n>`).
That plan owns the grammar; HS1 writes it and parses none of it. The URL of
an entry is the page that shows its evidence (`historyURL`): for every kind
that Brakeman's `refURL` also links, the same page, which
`TestHistoryURLsAgreeWithBrakemans` holds for each shared kind, except a
check receipt, which opens the run page at its step rather than the raw log
route
(`/runs/<run>?view=tasks&task=<task>&attempt=<n>&step=<phase>/<id>/<k>`,
PR-6); a session entry opens `/sessions/<id>` while the registry lists it
and `/history/sessions/<id>#e<seq>` after.

**Availability** (`history_availability.go`, PR-4), computed when a page is
answered, never stored, from what the server holds now and cheap enough for
a page of 100 (no git, no disk read but a `stat`):

| Entry | `availability.state` and its words |
|---|---|
| a session | `live` (listed, running), `listed` (ended, still listed: its page shows its last screen), `record` (only its record), `host_away` (a hosted session whose host is not connected now), `forgotten` (a link to a record no longer kept) |
| an attempt | `worktree` or `worktree_gone` (a `stat` of its worktree through the run's folder, when `where_ran` is `native`; any other `where` is said as the row has it, "on <where>", and not looked at from here) |
| a check | `log` (the ledger holds its log), `log_dropped` (the run's bound dropped it; hash and tail kept), `pruned` (the run's rows are gone) |
| a ticket, a comment | `deleted` for a tombstone; else the record's |
| a brief | `kept`, `pruned` |

and, for a session entry, `resume: {offered, why}` by the same rules the
resume routes apply (`resumeCheck`, factored out of `handleResumeSession` in
PR-4 with no change to what those routes answer): the agent still in the
catalog with a session recipe; its own session id resumable and of the
recipe's shape; no running session holding that conversation
(`liveAgentSession`); the folder there, or the recipe not needing it
(`ResumeNeedsCwd`); a server session, never a hosted one ("resume it on
<host>: `conductor host` resumes it there"); never a managed attempt ("a
managed run continues from its run page", `409 managed_run`'s words) nor a
Brakeman turn; a crew member of a legacy run through its run's member
resume. `GET /api/history/entries/{id}` computes the rest: whether the
result commit is in the repository (`git cat-file -e`, argv, 2 s), the
worktree's branch, a log's size and hash.

**The search routes** (PR-4), all `requireAdmin`, the query in the query
string as Dispatch's search has it (never logged: AGENTS.md):

| Route | Answers |
|---|---|
| `GET /api/history?q=&limit=&after=` | `{entries: [Entry], next?, total, totalCapped?, indexing: {state, behind?, oldest?, dropped?}, readOnly?}`; `400 bad_query` with words; the reply at most 2 MiB (rows past it cut, `truncated: true`) |
| `GET /api/history/entries/{id}` | one entry, its links each way (at most 100, each with its class, outcome, URL and label) and its full availability |
| `GET /api/history/entries/{id}/similar?limit=` | `Similar`'s entries (at most 50), the entry's run chain left out |
| `POST /api/history/reindex` | `202 {indexing}` |

An `Entry` as served:

```json
{
  "id": 1824, "kind": "check", "class": "checked", "outcome": "failed",
  "title": "unit on build #1", "snippet": "--- FAIL: [TestLoginRedirect] (0.02s)",
  "at": "2026-11-05T14:03:11Z",
  "source": {"store": "ledger", "label": "check receipt", "ref": "check:todo-3f2a91c0/build/1/unit/1"},
  "host": {"name": "lab-box", "where": "native"},
  "project": {"key": "CND", "repo": "/home/nate/code/todo"},
  "revision": {"label": "abc1234", "commit": "abc1234…"},
  "agent": {"id": "claude", "name": "Claude Code", "version": "2.1.318"},
  "anchor": {"ref": "task:todo-3f2a91c0/build", "label": "build · todo run", "url": "/runs/todo-3f2a91c0?view=tasks&task=build"},
  "url": "/runs/todo-3f2a91c0?view=tasks&task=build&attempt=1&step=check/unit/1",
  "repeats": {"count": 1, "anchors": 1},
  "links": [{"rel": "handed", "ref": "attempt:todo-3f2a91c0/build/2", "label": "build #2", "class": "record", "url": "…"},
            {"rel": "passed_after", "ref": "check:todo-3f2a91c0/build/2/unit/1", "label": "unit on build #2", "class": "checked", "outcome": "passed", "url": "…"}],
  "availability": {"state": "log", "words": "log kept (14,822 bytes)"}
}
```

`docs/protocol.md` gains a History section (PR-1 its record routes, PR-4
these, PR-5 the resume) with every field and bound above.

### `internal/api`, `internal/cli` (PR-5): resume from a record, the CLI, the guide

**Resume from a record.** `POST /api/history/sessions/{id}/resume` (admin),
body `{commandId, withoutEnv?}` (`DisallowUnknownFields`; `commandId` in the
pattern of run commands, at most 32 characters), a typed operation in the
Program's sense (section 1.5): the actor (the admin's principal,
`Principal.Subject` as C2 records it, else the ledger's admin actor), the
record as its scope, the agent's own session as its target, the command id
for idempotency, and a receipt kept in the record (`Resumes`). Its order:

1. the record read; a session the registry still lists is sent to the
   session's own resume route's logic (`handleResumeSession`), so there is
   one resume, not two;
2. the same command id again answers its receipt: the session it started
   (`200`), or its failure;
3. `resumeCheck` (PR-4) refuses in the words of the resume routes (`400
   hosted_session`, `409 managed_run`, `409 brakeman_run`, `400
   invalid_agent`, `409 already_resumed`, `400 invalid_cwd`), and a record
   whose launch had variables answers `409 env_not_kept` naming them, unless
   `withoutEnv: true`, which resumes without them and says so in the reply's
   notice;
4. the receipt written `pending` in the record (`End` order: before the
   launch);
5. the session made through `createLocalSession` exactly as
   `handleResumeSession` makes one (the agent, the name, the folder through
   `resolveCwd`, the arguments, the yolo choice, `resume` the agent's
   session id, `resumedFrom` the record's id, holding the conversation
   through `takeResume`), so it starts through the server's Runtime like
   every session;
6. the receipt settled `started` with the new session's id, or `failed` with
   why; `201 {session, resumed, notice?, receipt}`.

A server that stopped between 4 and 6 finds the receipt `pending` at
`Recover` and settles it `failed` ("the server stopped before the session
started"); the same command id then answers that failure, and a new id may
try again. A person resumes from a record only by asking: nothing resumes a
session on its own.

**The CLI** (`internal/cli/history.go`), with the workbench token like
`conductor dispatch`: `conductor history search <words…> [--limit n]
[--json]` (a table: class, kind, title, the six fields; `--json` the reply),
`history show <entry id>` (the entry and its links), `history similar <entry
id>`, `history sessions`, `history session <id>`, `history forget <id> |
--all`, `history resume <id> [--without-env]` (a fresh command id, sent
again once with the same id if the reply was lost, as `conductor dispatch
launch` does), `history reindex`. Completion and help text in `internal/cli`
as its other commands have them.

**The guide** (`internal/api/guide.go`): the operations table gains the
History operations under scope `admin` (no MCP tool: the table's
`tools/list` stays byte for byte as it is, which `TestToolsListIsUnchanged`
holds), so `GET /api/guide` lists them with their CLI forms and bounds.

### `web/app` (PR-6, PR-7): the palette's History, the run page's step, the pages

**PR-6, the palette.**

- `utils/palette.ts`: `PaletteMode` gains `history` with prefix `~` in
  `MODE_PREFIX`; `parsePrefix` and `dropPrefix` take it; the `?` modal's
  prefix list shows it.
- `utils/history.ts` (pure, vitest): the reply's types;
  `historyDetail(entry, now)`, the detail line of the six fields in a fixed
  order (source · host · project · revision · when · availability), each
  left out when the entry has none; `classBadge(entry)` (`Conversation`,
  `Decision`, `Checked ✓`, `Checked ✗`, none for a record), never a ✓ on
  anything but a `checked` entry that passed; `linkLine(entry)`
  ("Investigated by build #2 · passed at def5678"); `repeatWords` ("said in
  3 sessions"); `historyQuestions()`, the four question rows with their
  queries (`class:checked outcome:failed`;
  `kind:plan.withdrawn,plan.superseded,review.verdict
  outcome:withdrawn,superseded,needs_changes`; `class:checked
  outcome:passed` with the page's project; `host:<this machine>` and
  `kind:session`); `availabilityWords`.
- `composables/useHistorySearch.ts`: the search as typed, 200 ms after the
  last key, one request in flight (the previous aborted), answers taken in
  the order asked (a sequence number: a slow answer to an earlier query is
  dropped), the last 20 queries' answers cached for 30 s, nothing without
  the workbench token and the cache forgotten with it (as `forgetTouched`
  does).
- `composables/usePaletteRows.ts`: the `history` mode's groups: the four
  question rows and the latest sessions with nothing typed; the answer's
  rows grouped under their anchor (the session or the task: "build · todo
  run", a group header row) otherwise, each row's label its title, detail
  `historyDetail`, its badge, its link line as a third line, Enter opening
  its URL (a page in the app, the router; the raw log route never), → its
  actions (`Open evidence`, `Open the session` or `Open the record`,
  `Resume` only when `availability.resume.offered` and for a listed session
  through the existing resume route, `Find this elsewhere` setting the query
  to the similar route's answer, `Copy reference`); a "Loading…" row while
  the first answer is on its way and "Indexing: N sources behind" when the
  reply says so. In the search mode, from three characters, a History group
  of the five best rows after Tickets and before Shared with you, and its
  last row "All history matches (~)" switching the mode with the query kept.
- `components/CommandPalette.vue`: a row's badge (a `UBadge` in the brand
  palette of `app.config.ts`: Checked ✓ success, Checked ✗ error, Decision
  primary, Conversation neutral) and its third line; nothing else of the
  component changes.
- The run page (`pages/runs/[run].vue`, `components/RunTasks.vue`,
  `RunAttemptSteps.vue`): `?attempt=<n>&step=<phase>/<id>/<k>` beside
  `?task=` opens that attempt's steps and that step's log with its SHA-256,
  as the ticket page's receipt does (`DispatchReceipt.vue`, reused); read as
  `?task=` is, never written back.

**PR-7, the pages.**

- `pages/history/index.vue`: the search box (the palette's grammar), filter
  chips that write the query's filters (class, outcome, project from
  Dispatch's projects, host from the records, agent from the catalog,
  since), the results grouped under their anchors, paged with "Show more"
  (the reply's `next`), the indexing state; the URL keeps the query (`?q=`)
  so a search is a link. On a phone, one column, the detail line wrapped.
- `pages/history/sessions/[id].vue`: a session record: its header (name,
  agent with its version and own session, host, folder, repository, branch,
  start and end commits, how it ended), its run, task and attempt with
  links, its entries in order with anchors `#e<seq>` (a typed line, a
  question with its choices and how it was answered, a report, a file with
  its count), the dropped count, Resume (asks first; for a record with
  variables, the dialog names them and offers to resume without them),
  Forget (asks first), and the receipts of earlier resumes.
- `composables/useHistory.ts`: the record routes, `resumeRecord` with a
  minted command id sent again once with the same id on a lost reply
  (`utils/tasks.ts`'s rule), `forgetRecord`.
- The palette's record rows open the record page; the sidebar's foot of
  pages and the palette's Go to gain History, with `G H` (free today) and a
  terminal chord only where `chords.ts` has `Alt+H` free on every system.

### Docs (PR-8)

`docs/features.md`: the item's entry (what each PR shipped, its checks, the
by-hand check and the acceptance observation). `README.md`: a History
section (the palette's `~`, the classes, records and Resume from a record,
`history.sessions`). `docs/architecture.md`: History (the journal, the index
as a cache of the stores, the classes, the indexer's marks).
`docs/repository-map.md`: rows for `internal/journal` and
`internal/history`, the `internal/api/history*.go` files, `history` in
`cmd/conductor`'s command list, and `modernc.org/sqlite`'s importers named
as three in `docs/dependencies.md`'s row (no version change).
`docs/protocol.md` is already current from PR-1, PR-4 and PR-5.

### Later PRs (PR-9 to PR-11): what waits on other items

- **PR-9, DR1's decision records** (waits on DR1's PRs that serve
  DecisionRequest records, `docs/plans/decisions.md`): a source
  `decision:<id>`: the request as `record` (its question, its subject
  revision as `revision` and `commit_id`, its scope), each response as
  `decision.response` (`decided`, its responder and rationale, `outcome` the
  option), its application receipt as a link (`applied`), a superseded or
  stale request said in its availability. DR1 owns the record, its states
  and its receipts; HS1 indexes what DR1 serves and adds no field to it. The
  palette's "Rejected designs" question gains `decision.response` with a
  rejecting outcome.
- **PR-10, RV1's review batches and findings** (waits on RV1,
  `docs/plans/review-repair.md`): a batch as `review.batch` (`decided`:
  Request changes or Approve, by the reviewer, bound to its candidate and
  base revision), each finding as `review.finding` (`said`, its anchor in
  the candidate's diff), a finding's resolution verified against a new
  candidate only as RV1's own receipt says (`checked` only then). RV1 owns
  findings' identity across revisions; HS1 shows it.
- **PR-11, Brakeman reads history within its grant** (waits on Brakeman's
  PR-9 to PR-13 merging, `docs/plans/brakeman.md`, whose Brakeman capability
  "locate relevant sessions, files, results and prior investigations" this
  serves): Brakeman's `observe` gains a `history` section answering the
  History search for the `brakeman` principal, each entry filtered by the
  request's grant as Brakeman's refs are (`refGrant`: an entry's class of
  record visible, its project or repository covered by the scope), cited in
  a brief by the entry's own `ref`, never by an index id. Brakeman's plan
  owns the observe tool, the grant and the brief; HS1 owns the search. No
  attempt session gets a history tool in HS1.

Each of the three adds its paragraph to the features entry PR-8 writes.

## Events, orderings and invariants

**The session record's states.** `open` (begun, its session live) → `ended`
(its `status` entry or its removal from the registry: the end commit read,
`EndedAt` and `Ending` written at once) or `lost` (found open by `Recover`
after a stop the session did not see: ended at its last write's time). A
record is written before its session can keep an entry (before
`session.NewLocal` for a server session; at the hub's add for a hosted one,
whose entries come from its host only once it is registered) and at its end
at once; between, at most every 5 s. An `ended` or `lost` record changes
only by a resume's receipt or by `Forget`.

**A resume's receipt.** `pending` (written before the session is made) →
`started` (with the new session's id) or `failed` (with why; or settled by
`Recover` after a stop). One command id, one receipt: a repeat answers it
and makes nothing; two command ids for the same conversation race on
`takeResume`, and the second answers `409 already_resumed`.

**Marks.** Every source's mark is read before the source, and recorded with
the entries only when the read loaded (AGENTS.md, "Mark what you read with
what you saw before reading it"): the ledger's run revision (every
transition bumps it), Dispatch's ticket `rev` with its changes' last seq,
the record's `Seq` (from `history/state.json`, written before the record, so
it never goes back), the brief request's update time. A source changed
between its mark and its read is read again on the next pass, because its
commit marked it dirty after the mark was taken.
`TestAChangeBetweenTheMarkAndTheReadIsReadAgain` drives that through a seam
(`afterMark`) in the indexer.

**What is written before what.** The stores commit first; their hooks mark
the source dirty under the store's own commit lock and return; the indexer
reads after. The index is never written inside a store's commit and never
holds a store's lock past one read. A failed `Replace` (the disk full, the
transaction refused) leaves the old entries whole and the source dirty,
retried after a backoff (1 s doubling to 1 min).

**Invariants.**

- The index decides nothing. The engine, Dispatch, Brakeman's refs, the
  decision router and the resume routes read their own stores; only the
  History routes and the palette read the index, and every link they show
  resolves against the stores when it is opened.
- An entry's class is `classOf(kind)`, set inside `internal/history`, and a
  kind's class never changes with its text, its repeats or its rank. There
  is no table of accepted facts: a sentence said in ten sessions is ten
  `said` entries.
- A `checked` entry is a receipt the server made, bound to the commit it
  checked (`commit_id` never empty for `check`, `verify`,
  `candidate.verified`); a `decided` entry names its actor and its receipt;
  a `said` entry names who said it and where.
- Nothing a session record keeps is a variable's value, a share token, a
  path its deny list covers, a viewer's arrival or a terminal's output.
- The index's text is bounded by kind and in total; history older than the
  budget is dropped by age and the reply says from when it holds.
- A Forget removes the record's file and, at the next pass, its entries; the
  ledger's, Dispatch's and Brakeman's own records are never deleted through
  history.

**Migration.** None of an existing store: the ledger's, Dispatch's,
Brakeman's and the run records' schemas and files are unchanged, and each is
only read (the conservative-migration rule holds by touching none). The
journal's records carry `v: 1`; a later version adds fields with defaults
and reads every older record as it is. The index has no migration: another
version is rebuilt.

**Two servers on one data directory.** The holder of `history/owner.lock`
writes the journal and the index; another serves the History routes
read-only (its sessions unrecorded, said on its record list and in its log),
keeps trying for the lock as the ledger does, and on taking it recovers the
journal and catches the index up.

**The export.** `history/sessions/` and `history/state.json` are carried by
`conductor export` as the data directory's other files are (C6's `rest`), so
a restore brings the records back; `history/index.sqlite` is left out and
refused in an archive (a new `WhyDerived` row in
`internal/export/leftout.go`: "derived; rebuilt at the next start"), and
`history/owner.lock` with the other locks (`WhyLock`). The sync adapter's
allowlist (C6) is unchanged: what of history is shared across machines is
TS1's to decide (`docs/plans/team-history.md`).

## Out of scope

- Reading an agent's own transcripts or history files (Claude Code's
  projects directory, Codex's sessions directory, any agent's config
  directory beyond what `plans.go` opens). "Provider-aware" is the agent,
  its version, its own session id and its session recipe; a reader of a
  provider's transcript would be a decision of its own, proposed first
  (Owner decisions, 2).
- Keeping a terminal's output, scrollback or screen in a record.
- Sharing history between machines or people, and conflict rules for it: TS1
  over CS1 (`docs/plans/team-history.md`); HS1 only reserves the `origin`
  column.
- A store of accepted knowledge, embeddings, semantic or fuzzy search, and
  summaries of history written by a model: Brakeman summarizes on request,
  from the records (`docs/plans/brakeman.md`).
- An attempt session's or any agent's access to history over MCP; Brakeman's
  reaches it only through PR-11, within its grant.
- Resuming a managed attempt, a Brakeman turn or a hosted session from the
  server; live plan revision (the Program, section 1.5: `409 managed_run`
  stands).
- Feeding history into a task file or a context pack (C4's ContextProvider
  and CG1).
- DR1's, RV1's and HM1's records before they exist (PR-9, PR-10; HM1's
  person members are labels DR1 and HM1 own, `docs/plans/decisions.md`).
- Line-level authorship of a commit across tools (the Program, section 7).
- Deleting runs, tickets, briefs or their evidence from History.
- Records of sessions that ended before HS1 ships: none exists to read;
  runs, tickets and briefs are indexed back to whatever their stores hold.

## Tests per surface

Every PR runs, before each push, the checks of `docs/checks.md` for its
surfaces: Go (PR-1 to PR-5, PR-9 to PR-11): `make lint`, `go test -race
-count=1 -timeout 30m ./...` (the whole module), `make lint-static`; web
(PR-6, PR-7, PR-9, PR-10): `npm --prefix web run typecheck`, `npm --prefix
web test`, `make test-e2e` (the PR's specs alone first while iterating).
PR-1 and PR-5 also run the Mac's row (`python3 scripts/preflight.py
--machines mac --only go-test-race`, under the preflight lock): a record's
files and modes on APFS and `gitcli` on macOS. No PR changes Python tooling,
`desktop/` or a workflow, so the scripts suite, `make desktop-test` and the
Windows rows are named in each body as not applicable. New tests that wait
do so on the mechanism (a seam or the store's own read), never on a clock
(AGENTS.md, Tests that do not flake); the journal's 5 s flush is driven
through a seam (`flushNow`) in every test that needs a write.

**Go, `internal/journal` (PR-1):**
`TestARecordIsWrittenBeforeItsFirstEntry`;
`TestARecordKeepsEvidenceEntriesOnly` (join, leave and link never kept; tool
use counted); `TestAFileFoldsIntoOneRowPerPathAndOp`;
`TestADenyListedPathIsNeverWritten`; `TestAVariableValueIsNeverWritten` (the
names and the hash only; the file grepped for the value);
`TestARecordIsBoundedOldestEntriesFirst` (at 64 KiB exactly and one past);
`TestPruneKeepsLiveAndPendingRecords`; `TestRecoverEndsAnOpenRecordAsLost`
and settles a pending resume failed; `TestSeqNeverGoesBack` (a crash between
the state file and the record, by a seam); `TestForgetRefusesALiveSession`;
`TestANewerRecordIsReadNotRewritten`; `TestReadOnlyWithoutTheLock` (two
processes, as the ledger's lock test); `TestFilesAre0600InA0700Directory`
(unix); 32 goroutines noting into 8 records under `-race`, every entry kept
once in order of `Seq`.

**Go, `internal/api` (PR-1):** `TestEveryServerSessionGetsARecord`,
`TestAHostedSessionGetsARecord` (its host's name and user, its entries from
the host's reports through the hub), `TestTheRecordSurvivesARestart` (a
server ended and started again on the same data directory; the record
listed, ended), `TestHistorySessionRoutesAreAdminOnly` (a share link, a
session token, no token), `TestHistorySessionsOffKeepsNoRecord`,
`TestASwitchyardKeepsNoHistory`.

**Go, `internal/history` (PR-2):**
`TestSchemaV1AndARebuildOfAnotherVersion`, `TestADamagedIndexIsMadeAgain`;
`TestClassComesFromTheKindAlone` (a caller's class refused, every kind of
the table its class); `TestARepeatedSentenceStaysSaid` (the same body in ten
sources: ten `said` entries, `repeats` 10 in 10 anchors, none of another
class); `TestReplaceIsAllOrNothing` (a commit made to fail leaves the old
rows and FTS rows); `TestRemoveCascades`; `TestTermsIntersectAcrossColumns`,
`TestPrefixFromTwoLetters`, `TestAPhraseIsOneTerm`, `TestEachFilter` (a
table over every filter name and its refusals),
`TestAnUnknownFilterIsAWord`; `TestOrderNeverWeighsClass` (two entries of
equal text, a `said` newer than a `checked`: the newer first);
`TestTheBudgetDropsTheOldestFirst` (by entries and by bytes, `Stats` saying
from when); `TestSimilarFindsTheSameCommandElsewhereAndLeavesTheChainOut`;
`TestSnippetMarksTheMatch`; `BenchmarkSearch` (100,000 entries; the medians
in PR-2's body).

**Go, `internal/api` and `internal/crew` (PR-3):**
`TestTheFanCallsEveryObserverInCommitOrder` and the sync adapter's tests
unchanged; `TestTheIndexHandsWhatTheEngineHands` (a managed run with fake
sessions whose check fails at attempt 1; each `handed` link equals the
attempt's own `previous.check`; `passed_after` to attempt 2's receipt);
`TestHandedAcrossAContinuation` (the run stopped after attempt 1 failed,
continued; the continuation's first attempt handed the source run's
receipt); `TestAReviewsNeedsChangesLinksTheReopenedAttempt`;
`TestACheckBodyIsItsArgvAndTheLogsEnds` (a 1 MiB log: the first 4 KiB and
last 12 KiB indexed, a word from the middle not found);
`TestAPassedCheckIndexesItsTailOnly`;
`TestAChangeBetweenTheMarkAndTheReadIsReadAgain` (`afterMark`);
`TestCatchUpAfterARestart` (runs made while no index existed, then a run
pruned: indexed, then removed); `TestAPrunedRunLeavesTheIndex`;
`TestADirtySetPastItsBoundCatchesUp`;
`TestTheIndexIsLeftOutOfAnExportAndRefusedInOne`;
`TestTheRecordsRideInAnExport` (exported, restored on a fresh directory,
listed, indexed at the next start); `TestTheReadOnlyServerIndexesNothing`;
in `internal/crew`, `TestFailingReceiptIsTheEnginesRule` (the exported
functions against the engine's own handoff over the existing fixtures).

**Go, `internal/api` (PR-4):** `TestTicketSourcesAreClassified` (a comment
`said`, an approval and a withdrawal `decided` with the operator, a verified
delivery `checked` with its candidate, a tombstone `deleted`);
`TestBriefItemsAreSaidAndCiteTheirRefs`;
`TestBrakemansRunsAreLeftOutByDefault`; `TestHistoryURLsAgreeWithBrakemans`
(every kind both link); `TestAvailabilityOfASession` (live, listed, record,
host away, forgotten), `TestAvailabilityOfACheck` (log, dropped, pruned),
`TestAWhereOtherThanNativeIsNotLookedAt` (a row whose `where_ran` a fake
Runtime set); `TestResumeOfferedByTheResumeRoutesRules` (each refusal's
case, and the resume routes' own tests unchanged after `resumeCheck` is
factored out); `TestHistorySearchRoutesAreAdminOnly`;
`TestTheSearchReplyIsBounded` (2 MiB, `truncated`);
`TestABadQueryIsRefusedWithWords`; `TestTheQueryIsNeverLogged` (the request
log's lines for a search hold no `q`).

**Go, `internal/api` and `internal/cli` (PR-5):**
`TestResumeFromARecordAfterARestart` (a fake agent with a session recipe:
the new session's arguments are the recipe's resume arguments with the
recorded id, its folder and yolo choice the record's);
`TestResumeFromARecordRefuses` (a table: hosted, managed, Brakeman, no
recipe, not resumable, held by a live session, folder gone with
`ResumeNeedsCwd`, variables without `withoutEnv`);
`TestTheSameCommandIDAnswersItsReceipt`;
`TestAPendingReceiptIsSettledFailedAtRecover`;
`TestAListedSessionResumesThroughItsOwnRoute`;
`TestResumeFromARecordStartsThroughTheRuntime` (`sessiontest.FakeRuntime`
records the start); `TestTheGuideListsHistoryForTheAdminOnly` and
`TestToolsListIsUnchanged`; `cli/history_test.go`:
`TestHistorySearchPrintsTheSixFields`,
`TestHistoryResumeSendsOneCommandIDTwice`, `TestHistoryForgetAll`.

**vitest (PR-6, PR-7):** `utils/history.test.ts` (the detail line's order
and omissions, every badge and that no `said` or `decided` entry has a ✓,
the link line, repeats' words, the questions' queries, availability words);
`utils/palette.test.ts` (`~` parsed and dropped, no other prefix changed);
`composables/useHistorySearch.test.ts` (the debounce through fake timers, an
abort, a slow earlier answer dropped, the cache, the token forgotten);
`composables/useHistory.test.ts` (the resume's command id sent twice on a
lost reply, never a second id).

**Playwright (PR-6, PR-7):** `history.spec.ts`, the acceptance boundary with
the e2e stub worker: the crew of `tasks.spec.ts`, whose `fixed` check (`git
cat-file -e HEAD:stub-fixed.txt`) fails until the builder adds the file on
its second attempt, is launched and succeeds; the palette opened after the
sidebar's root is drawn (AGENTS.md: wait for the listener's component), `~
stub-fixed` typed (a word of git's refusal in the receipt's log); the first
row is the failed receipt with Checked ✗, the six fields and "Investigated
by build #2"; Enter opens the run page at attempt 1's `fixed` step with the
log's SHA-256 equal to the step log route's header; a plain session whose
stub reported the same word shows as Conversation, never Checked; `~` alone
lists the four questions, and "What was last verified" lists attempt 2's
passing receipt; Find this elsewhere on a second crew's run of the same
command lists it. Rows are found by their ids and titles, never by a count
(`stopStrays` first). `history-pages.spec.ts`: a plain stub session with a
session recipe ended, the server restarted on its data (the
`tasks-resume.spec.ts` pattern), the record page reached from the palette,
Resume asked and answered, the new session's command line holding the
recipe's `--resume` and the recorded id, its record naming the one it
resumed; Forget removes the record and its rows; the History page's chips
write the query and a phone width shows one column. `palette.spec.ts` and
`palette-keys.spec.ts` stay green with the seventh prefix. Each new spec run
`--repeat-each 10` before its PR is marked ready.

**By hand (PR-8's record):** what the stub cannot show: real Claude Code and
Codex sessions recorded with their versions and own session ids (Codex's
lowest-id policy), a plain Claude Code session resumed from its record after
a restart (`--resume <id>`) and a Codex one (`resume <id>`, its folder
needed), a hosted session from `conductor host` on the Mac (its host and
user, "host away" once the host stops, no Resume from the server), the four
questions asked of a real evening's runs, the History page on a phone on the
LAN, and the index rebuilt on a data directory with a few hundred runs, with
the time it took.

## Boxcar constraints

1. **Attempts start only through `crew.Launcher`.** Honoured: history starts
   no attempt. A managed attempt's session is never resumed from history;
   the run continues through its own route (the engine and `crew.Launcher`).
   A resume from a record is a plain session's, made by `createLocalSession`
   as every session is.
2. **`session.Local` drives a `session.Process`.** Honoured: the journal
   reads activity entries from the hub and `Info`; nothing here reaches a
   pid, a tty or a signal.
3. **An attempt's context is files under its worktree.** Not touched:
   history writes nothing into a worktree and reads none of its files; it
   reads the ledger's rows, which hold what the task files were made from.
   The handed receipt is derived from the rows by the engine's own
   functions.
4. **The agent reaches the server over the network.** Not touched: no new
   channel; hosted sessions' entries arrive as they do today, through the
   host's reports.
5. **Checks go through `internal/proc` and carry where they ran.** Honoured:
   history runs no check; it indexes every receipt with its `where_ran` as
   the row holds it. Its own git reads (a record's commits, a commit's
   presence) are `gitcli`'s argv commands with timeouts, as the Files tab's
   are, and run no check.
6. **Platform splits stay platform tags.** Not touched: no platform split,
   no capability.
7. **No evidence assumes a native process.** Honoured: every entry keeps the
   row's `where_ran`; availability looks at a worktree only when the row
   says `native`, and says any other `where` as the row has it.
8. **`hostagent` is a launch site as much as `api`.** Not touched: a hosted
   session is recorded on the server from what its host already reports;
   resuming it stays the host's (`400 hosted_session`, unchanged).
   `internal/hostagent` is not changed.

## Open-core checklist

- [x] **1. No premium code, no licence check.** Nothing asks for a plan, a
      tier or an ext provider; the only switch is the free privacy choice
      `history.sessions`.
- [x] **2. The open-source build is fully useful on its own.** History is
      local evidence over local stores, with no account and no network.
- [x] **3. Paid features are gated by hosted dependencies.** There is no
      paid part; team history (TS1) will be gated by its hosted sync, not by
      a flag here.
- [x] **4. Stop, inspect, revoke, export and required approvals stay free.**
      History adds inspection, its records ride in the export, Forget is the
      person's, and no approval, stop or revoke is touched or weakened.

## The PRs

Each PR is one issue and one branch `issue-N` from `main`, cut once the PRs
it depends on have merged; it leaves `main` working and green on its own,
ticks the checklist above, names its checks (a check not run is named as not
run), and updates the `docs/protocol.md` rows of the routes it adds. Sizes
are changed lines including tests. Two PRs run side by side only where they
touch no common package or directory, as named.

**PR-1: the session journal and its routes.** `internal/journal` whole (the
record, its bounds, the journal with its lock, flush, prune, recover,
forget, `history/state.json`); `history.sessions` in `internal/config`; in
`internal/api` the journal opened with the data directory, `Begin` for
server and hosted sessions, the hub's sink and the chat call, `End`, the
agent's version and the git reads, the four record routes. Touches
`internal/journal`, `internal/config`, `internal/signal` (the `OnAdded`
callback), `internal/api`, `docs/protocol.md`. Tests: PR-1's of
`internal/journal` and `internal/api` above. About 1,300 lines. Depends on
nothing. May run beside PR-2.

**PR-2: the history index and its search.** `internal/history` whole (the
schema, the kinds table, `Replace`, `Remove`, `Mark`, the bounds and the
budget, `ParseQuery`, `Search`, `Similar`, `Entry`, `Links`, `Stats`),
`BenchmarkSearch` with its medians in the PR body. Touches
`internal/history` only. Tests: PR-2's above. About 1,400 lines. Depends on
nothing. May run beside PR-1.

**PR-3: the indexer over sessions and runs.** The commit fan-out
(`commits.go`, the sync adapter registered through it unchanged); the
indexer (dirty set, marks, the pass, backoff, catch-up, reindex route); the
session-record and run sources with every link (`session_of`, `continues`,
`launched_from`, `handed`, `passed_after`, `reviews`, `reopened`);
`crew.FailedAtCheck` and `crew.FailingReceipt` exported; the export's
`WhyDerived` row and lock row. Touches `internal/api`, `internal/crew`,
`internal/export`. Tests: PR-3's above. About 1,400 lines. Depends on PR-1
and PR-2.

**PR-4: Dispatch's and Brakeman's sources, availability and the search
routes.** The ticket and brief sources; `historyURL`; availability;
`resumeCheck` factored out of `handleResumeSession`; `GET /api/history`,
`/entries/{id}`, `/entries/{id}/similar`; `docs/protocol.md`'s History
search rows. Touches `internal/api`, `docs/protocol.md`. Tests: PR-4's
above. About 1,200 lines. Depends on PR-3.

**PR-5: resume from a record, the CLI and the guide.** `POST
/api/history/sessions/{id}/resume` with its receipts and `Recover`'s
settling; `conductor history` and its completion; the guide's admin rows.
Touches `internal/api`, `internal/journal` (the receipt's settling at
recover), `internal/cli`, `docs/protocol.md`. Tests: PR-5's above. About
1,000 lines. Depends on PR-4. May run beside PR-6.

**PR-6: the palette's History and the run page's step.**
`utils/palette.ts`'s `~`, `utils/history.ts`, `useHistorySearch`, the
palette's history groups and the search mode's History group, the row's
badge and third line, the run page's `?attempt=&step=`; `history.spec.ts`.
Touches `web/app/utils`, `web/app/composables`, `web/app/components`,
`web/app/pages/runs`, `web/e2e`. Tests: vitest and Playwright of PR-6 above.
About 1,100 lines. Depends on PR-4. May run beside PR-5.

**PR-7: the History page and the record page.** `pages/history/index.vue`,
`pages/history/sessions/[id].vue`, `useHistory`, Go to's History row and
chord; `history-pages.spec.ts`. Touches `web/app/pages/history`,
`web/app/composables`, `web/app/components`, `web/app/utils`, `web/e2e`.
Tests: PR-7's above. About 1,000 lines. Depends on PR-5 and PR-6.

**PR-8: the docs and the features entry.** `docs/features.md`'s HS1 entry
with each PR's checks and the by-hand record, the README's History section,
`docs/architecture.md`'s History, `docs/repository-map.md`'s rows,
`docs/dependencies.md`'s importers. Touches `docs`, `README.md`. Docs only;
no check applies but the links read. About 400 lines. Depends on PR-7.

**PR-9: DR1's decision records.** As in "Later PRs". Touches `internal/api`,
`web/app/utils`, `docs`. About 500 lines. Depends on PR-8 and on DR1's PRs
that serve its records (`docs/plans/decisions.md`).

**PR-10: RV1's review batches and findings.** As in "Later PRs". Touches
`internal/api`, `web/app/utils`, `docs`. About 500 lines. Depends on PR-9
(both change the sources file and the features entry) and on RV1's PRs that
serve batches and findings (`docs/plans/review-repair.md`); if RV1 lands
before DR1, PR-10 goes first and PR-9 follows it.

**PR-11: Brakeman reads history within its grant.** As in "Later PRs".
Touches `internal/api` (Brakeman's observe and the guide), `docs`. About 600
lines. Depends on PR-8 and on Brakeman's PR-9 to PR-13
(`docs/plans/brakeman.md`); runs after PR-9 and PR-10 or before them, never
beside them (`internal/api`).

## Owner decisions

Dated 2026-11-05. The defaults below are this plan's, inside the twelve
articles; the owner may overturn any at the plan's review, and one
overturned is rewritten here before the PRs are cut (the Program's
convention, section 3.1).

1. **The Program's open decisions.** None is this item's. O4 (human policy
   v1) blocks SW1 alone and O10 (Dispatch's conflict rules) blocks CS1
   alone; both stay with those items' plan reviews as the status log of
   2026-11-03 says. HS1 needs neither: it is local and single-machine, and
   it reserves `origin` for TS1 without choosing how shared history merges.
2. **No provider transcript is read.** "Provider-aware" is taken as the
   agent, its version, its own session id with where it came from, and its
   session recipe, which decides Resume. Reading Claude Code's or Codex's
   own conversation files would open the agent's config directory, which
   Conductor has never done beyond its plans, would read another product's
   private format that changes without notice, and would carry credentials'
   neighbours into a search. A session's conversation evidence is what
   passed through Conductor. Overturning this adds a per-agent reader as its
   own item.
3. **Session records are on by default.** `history.sessions` defaults to on,
   because a record that starts only once someone thinks to turn it on
   misses the session they later want; the records are 0600 files under the
   data directory, admin-only, bounded (64 KiB, 2,000 records), carried by
   the export and forgettable one by one or all at once.
4. **What a record keeps.** Typed lines, answers, questions with their
   choices, the agent's reports, files touched (folded) and chat; never a
   variable's value, a terminal's output, a viewer's arrival or a link. The
   bounds are the plan's guesses at what one session's evidence needs;
   PR-1's tests measure them and PR-1's body says what a real session took.
5. **Three classes and records, named Conversation, Decision and Checked.**
   The class is set by the kind alone, and repetition never changes it;
   Checked shows ✓ only for a receipt that passed. A review verdict by an
   agent is Conversation; the engine's act on it is a record.
6. **"Investigated" means handed.** A session investigated a failure when
   the engine handed its attempt the failed receipt (its `previous.check`),
   derived by the engine's own functions. A session whose words merely
   mention the failure is shown as Conversation that mentions it, never as
   an investigation.
7. **The `~` prefix and the History group.** The seventh prefix is `~`
   (free, one key on most layouts, read as "back in time"); the plain search
   shows History from three characters, five rows, after Tickets. Dispatch's
   plan chose no new prefix for tickets (its decision 11) because tickets
   fit the plain search; history needs its own mode because its answers come
   from the server per query, not from rows the palette already holds.
8. **The index is a cache.** Derived from the stores, rebuilt when missing
   or of another version, left out of the export and refused in a restore,
   never read to decide anything; the session records are the one new source
   of truth.
9. **Budget.** 200,000 entries and 256 MiB of indexed text, oldest dropped
   first by age; a check's log indexed by its first 4 KiB and last 12 KiB
   when it failed and by its 2 KiB tail when it passed.
10. **Resume from a record.** Offered by the resume routes' own rules; never
    for a hosted session (its machine resumes it), a managed attempt (its
    run continues) or a Brakeman turn; the launch's variables are not kept,
    so a record launched with them resumes only with `withoutEnv`, which
    says which were left out.
11. **Admin only.** The History routes take the workbench token; no attempt
    session gets a history tool; Brakeman reads history only through PR-11,
    within its grant.
12. **The later PRs.** DR1's decisions, RV1's reviews and Brakeman's reads
    are indexed by PR-9, PR-10 and PR-11 once those items serve them, and
    HS1 is Done without them: its acceptance boundary is a failure and its
    investigating session, which main's records already hold.

## Acceptance

The boundary (the Program, section 4.3, HS1): "which session investigated
this failure" is answered from the palette with a link to the evidence.

**Observed how.** On the owner's machine (or the WSL box with the owner's
agents), on the open-source build with no account, after PR-8 merges:

1. a project with a Dispatch project over its repository; a managed crew
   whose work task's check fails on its first run (a script outside the
   worktree, as Yardmaster's live check used), run with real Claude Code as
   the worker and Codex as the reviewer, so attempt 1 fails the check and
   attempt 2, handed the receipt, passes; beside it a plain Claude Code
   session in the same repository asked about the same failure;
2. the server restarted;
3. in the palette, `~` and a word of the failing test's output: the first
   rows are the failed receipt, Checked ✗, with its six fields true (the
   source, this machine, the project's key, attempt 1's result commit, the
   time, "log kept"), "Investigated by build #2" naming Claude Code and its
   own session, and "passed at" attempt 2's commit; Enter opens the run page
   at that step's log, whose SHA-256 equals the step log route's; the plain
   session's words show as Conversation, and its record opens on its record
   page after the restart, where Resume resumes the Claude Code
   conversation;
4. the three other questions asked the same way: a plan revision withdrawn
   for the ticket found as a Decision ("which design was rejected"), the
   passing receipt found under "What was last verified" for the project, and
   a second crew's run of the same command found by Find this elsewhere
   ("did another crew solve this").

**Done** is that observation recorded in the features entry with what was
missing, slow or wrong, and every bug the stream's code caused that it found
fixed in the stream (AGENTS.md), not the PRs merged. The Playwright spec
`history.spec.ts` holds the boundary with the stub agents in CI; it is not
the observation.

**The owner's part.** The judgement that the rows answered the four
questions truthfully (no Conversation row read as a result), the by-hand
runs that need the owner's subscriptions and the Mac's `conductor host`, and
a look at the History page on the Windows desktop app. PR-9 to PR-11 each
add their own short observation to the entry when they land; none of them
holds HS1's Done.

