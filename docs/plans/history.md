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
DR1's decision records (its plan, `docs/plans/decisions.md`, is in review
and not yet on main), RV1's review batches and findings (its plan,
`docs/plans/review-repair.md`, likewise in review) and Brakeman's
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
   Program item TS1's, a hosted service's work; TS1 is not planned yet and
   has no plan file.
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
    dependency, so `make notices` has nothing to regenerate;
    `docs/protocol.md` gains the History routes (PR-1, PR-4, PR-5). One
    protocol change, PR-12's (what a host reports of its sessions: bounded
    fields on the register message and one new host message), touches
    `internal/proto`, `web/app/utils/protocol.ts` and `docs/protocol.md`
    together, each field and the message with its size limit and a test.
11. The railroad names keep their meaning, and "history" is told from the
    three existing uses (above); "workspace" is not used.
12. Execution stays behind one seam: a resume from a record starts its
    session the way every session starts, through `createLocalSession` and
    the server's `ext.Runtime`; a managed attempt is never resumed from
    history, only continued through the run's own route (the engine and
    `crew.Launcher`). "Boxcar constraints" below takes the eight one by one.

## Why and what happens

Someone coming back to a project wants to know what was tried, what was
decided and what was proven, and those are different facts with different
weight (the Program, sections 1.1 and 1.4): an agent saying it finished is
something said, a check passing on a commit is something checked, and a
person accepting the result is something decided; none stands in for
another. History is where those facts are found again later, side by side
and never merged. On `main` today:

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
  the run allow it, and why not otherwise), Find this elsewhere (receipts
  of the same command in other crews' runs of the same repository, those
  that hold the failure's own line first, passed before failed: "did
  another crew solve this"; every repository only when asked), Copy the
  reference.
- **Session records.** Every session the server runs or hosts gets a record
  in the data directory (`history/sessions/<id>.json`) from its start: the
  agent with its version and its own session id, the host and where it ran
  (its Runtime), the folder, the repository, the branch and the commit
  before its process started and at its end, its launch (arguments and the
  yolo choice; the variables' names, never their values), its crew, run,
  task and attempt, and its conversation evidence as Conductor saw it: the
  lines Conductor typed into it (a crew's prompt, a broadcast), the
  questions it asked with their choices and how each ended (the choice
  picked, or withdrawn), its chat (what people posted there and what was
  sent from it to the agent), what its agent reported (progress, errors,
  artifacts, handoffs, refused tools) and the files it reported touching.
  What a person typed into its terminal or its reply box is never kept (the
  chat already refuses to repeat a typed line, since it may be a
  credential: `answerWords`, `internal/session/chat.go`). At most 64 KiB a
  record, the newest 2,000 ended sessions kept. The record page
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
	Where    string     `json:"where,omitempty"`  // the Runtime it ran in: always set for a server session ("native" or createSessionRequest.where); a hosted one's as its host reports it (PR-12), "" only when the host reported none: unknown, never read as native
	Cwd      string     `json:"cwd"`
	Repo     string     `json:"repo,omitempty"`   // the worktree's top (--show-toplevel), shown
	RepoCommon string   `json:"repoCommon,omitempty"` // the repository's identity: its common git directory (--git-common-dir, made absolute), one for every linked worktree
	Branch   string     `json:"branch,omitempty"`
	Start    string     `json:"startCommit,omitempty"` // HEAD read before the process started, a full id
	StartFrom string    `json:"startFrom,omitempty"`   // "launch" (read before the start) | "attempt" (the ledger's) | "host" (PR-12)
	End      string     `json:"endCommit,omitempty"`   // HEAD read after the end: an observation, not the launch's
	Launch   *Launch    `json:"launch,omitempty"` // server sessions only
	Crew     *Crew      `json:"crew,omitempty"`
	ResumedFrom string  `json:"resumedFrom,omitempty"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt,omitzero"` // an observed end only: an exit or a stop the server saw
	LostAt    time.Time `json:"lostAt,omitzero"`  // when the server stopped observing it ("lost", "host away"): its process's state unknown
	Ending    string    `json:"ending,omitempty"` // "exited (exit 0)", "stopped", "lost", "host away"
	Reopened  int       `json:"reopened,omitempty"` // a hosted record taken up again by its host (below)
	Entries  []Entry    `json:"entries"`
	Dropped  int        `json:"dropped,omitempty"` // entries the bound let go, oldest first
	Files    []File     `json:"files,omitempty"`
	Tools    map[string]int `json:"tools,omitempty"` // tool_use counts by tool, at most 64 tools
	Resumes  []Resume   `json:"resumes,omitempty"`
}
type Agent struct { ID, Name, Adapter, Version string; Session *AgentSession }
type AgentSession struct { ID, Source string; Resumable bool } // as session.AgentSession
type Host struct { Name, User, Owner string; Hosted bool } // Owner: the host instance's (signal.ownerOf), hosted only
type Launch struct { Args []string; ArgCount int; ArgsSHA256 string; ArgsKept bool; EnvNames []string; EnvSHA256 string; Yolo, Renamed bool }
type Crew struct { RunID, CrewID, Member, Task string; Attempt int; Brakeman string }
type Entry struct { Seq int64; At time.Time; Kind, ID, Ref, Event, ByName, Message, Op, Path, Tool, URL, To string; Choices []string; Outcome string }
type File struct { Path, Op string; Count int; Last time.Time }
type Resume struct { CommandID, Actor, Session, State, Error string; At time.Time } // pending | started | failed | uncertain; Session preallocated
```

An `Entry` keeps the identity its source gave it: `ID` a chat message's id
(a question's is its `Attention.ID`), `Ref` the id of what it answers or
withdraws, `Event` the chat's event, so two questions of the same words are
two entries and each answer names the one it ended.

What each kept entry is taken from, by its source on `main` (PR-1 changes
nothing in `internal/session`; every source below exists today):

| Entry (`Kind`) | Source | What it holds |
|---|---|---|
| `typed` | an activity `input` entry with `Op: typed` (`typedEntry`, `submit.go`: recorded only for a line Conductor types, `By` unset) | the line: a crew's prompt, a broadcast, a handoff typed in |
| `question` | a chat message of kind `question` (`askInChat`, `chat.go`, posted as the session goes needs_input) | its `ID` (the attention's), its text and its choices' labels |
| `answer` | a chat system message with `Event: answered` (`answeredInChat`) | `Ref` the question's id, `Outcome: answered`, `Message` the picked choice's label as `answerWords` gives it, or nothing when the answer was typed; who answered |
| `answer` | a chat system message with `Event: withdrawn` (`leftInChatLocked`, `chat.go`) | `Ref` the question's id; with `Text: ChatLeftTerminal` (keys typed in the terminal answered it) `Outcome: answered_in_terminal` and no answer text (the keys are never kept), as Brakeman's `questionState` reads it (`brakeman_refs.go`: resolved); with `ChatLeftAgent` or no reason `Outcome: withdrawn` (the agent moved on) |
| `chat` | a chat message of kind `message` (a person's or an agent's post) and of kind `sent_to_agent` (`Ref` the message sent) | the text, who posted it |
| `report` | an activity `progress`, `artifact`, `handoff`, `error` or `tool_denied` entry | its message, URL, target or tool |
| `status` | an activity `status` entry; the session's end | the words |

Not kept, and said so on the record page: what a person types into the
terminal (keystrokes reach the PTY and no record), what a person submits
from a reply box (an `input` entry with `By` set is not recorded today, and
an answer's `input` entry holds the question's words, not the answer:
`resolve`, `local.go`), a typed answer's text (the chat's `answered` line
never repeats it, for it may be a credential, and the record follows the
chat). An `attention` entry is not kept either: it holds only the label,
and the question and its ending come from the chat with their ids. `join`,
`leave` and `link` are never kept (who watched is not evidence of the work,
and a link's name may be a person's), nor the chat's join and leave lines.
`tool_use` is counted by tool in `Tools`, not kept one by one. A `file`
entry folds into `Files`, one row per path and op with its count and last
time, at most 500 rows (the oldest by `Last` let go, counted in `Dropped`),
and a path the session's deny list covers is never written (the deny list
`/api/touched` applies, `session.DenyList`). The session's chat reaches no
hub sink today: PR-1 gives the hub's `chat` one call for the journal
(below).

**Bounds** (`bounds.go`): an entry's texts as the session bounds them
already (`MaxAttentionMessage` 500 bytes, `MaxEventPath`, `MaxEventURL`,
`MaxEventTool`); a chat line at most 2 KiB; a record at most 64 KiB as
encoded (`MaxRecord`): past it the oldest entries go first, counted in
`Dropped`, then the oldest `Files` rows. What is not an entry or a file is
bounded to fit beside them: the launch at most 16 KiB encoded
(`MaxLaunch`), at most 16 `Resumes` (at 16 a new command id is
refused and no earlier receipt is ever let go: "Resume from a record"),
`Tools` at most 64, the name, folder,
branch and agent fields at their session bounds (all of it under 24 KiB),
so dropping entries and files always brings a record under 64 KiB. A
session may be launched with arguments past the launch's bound (the API
takes 64 of 4 KiB, 256 KiB): such a launch keeps `ArgCount` and
`ArgsSHA256` with `ArgsKept: false` and no argument, never a cut list, and
Resume from that record is refused (`409 args_not_kept`, below), so no
resume runs with arguments other than the ones launched. The variables'
names at most 64, past it their count only (in the hash all the same).

`MaxRecords` (2,000) counts ended records (a `lost` or `host away` one
among them: it is no session this server runs); a live record, and one with a
`pending` resume, is never pruned and is not counted, and live records are
bounded by the server's `MaxSessions` (config, 32 by default, at most
10,000). Worst case on disk is (2,000 + `MaxSessions`) × 64 KiB, and one more
record per resume in flight: about 127 MiB at the default, said in `history.sessions`'s config text; a typical
record is a few KiB. When live records alone pass `MaxRecords`, every ended
one past the bound is pruned and the live ones stay.

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

- `Begin(r Record) error`: the record of a session as it is added, in
  memory at once (a session's record exists before its first entry is
  kept). A server session's is written at once by its caller
  (`createLocalSession`, not a sink); a hosted one's is handed to the
  writer, which writes it before anything else it holds, since its caller
  is the goroutine that reads the host's connection and must not wait on a
  disk. **Begin on an id that already has a record** reopens it only when
  both are the same host's hosted session (`Host.Hosted`, the same
  `Host.Owner`) and the record is open or marked `host away` or `lost`: a
  hosted session's id is `HostedID(instance, localID)`
  (`internal/signal/hub.go`), the same after a server restart or after its
  host comes back past `DisconnectGrace`, and its process may have lived
  through either. Reopened, `LostAt` and `Ending` are cleared,
  `Reopened` counts it, a `status` entry says "taken up again by its host",
  and every earlier entry and `Seq` stay. Any other existing id is refused
  (`ErrExists`, logged): a server session's id is random and never comes
  back.
- `Note(id string, e Entry)`, `Chat(id string, c Entry)`, `Touch(id string,
  f File)`, `Tool(id, name string)`: appended in memory, the record marked
  dirty; never waits on a disk (the caller is the hub's sink, which must
  never wait). The hub hands entries over concurrently and out of order
  (`eventHub.activity`: "concurrently and out of order"; `Local.record`
  calls `OnActivity` after its lock), so a record's entries are kept in
  order of `At`, then of arrival, never of arrival alone.
- `Changed(id string)`: the session's metadata marked stale and the
  writer woken; the writer reads it as it is then through `Current(id)`
  (the API's callback, "The API's part of PR-1"), never from a snapshot a
  caller hands it, and writes a record whose `Crew` or end it changed at
  once.
- `End(id string, end Ending)`: the end commit, the time and how it ended,
  set in memory and the writer woken to write it at once; never a disk
  write on the caller's goroutine (a sink or the registry's `OnRemove`). An
  entry noted after `End` whose `At` is not after `EndedAt` (one an earlier
  callback still carried) is kept in its place and written at the next
  flush; one after `EndedAt` is dropped and counted in `Dropped`.
- A writer goroutine writes each dirty record at most once every 5 s
  (`flushEvery`), and every one at `Close`, so a crash loses at most the
  last 5 s of entries, and a record is never written more often than that
  however chatty its agent.
- `Get(id)`, `List(after string, limit int)` (newest first by `StartedAt`,
  pages of at most 100), `Forget(id)` (refused for a live session,
  `ErrLive`, and for a record holding a `pending` resume receipt,
  `ErrResumePending`), `ForgetAll()` (every record neither live nor
  pending; those it keeps are counted). Forget takes the record's lock, the
  one a resume's reservation and its settlement take ("Resume from a
  record"), so a record is never deleted between its receipt's reservation
  and its settlement: the launch always runs under its durable receipt, and
  the settlement always finds its record.
- `Prune()`: past `MaxRecords` the oldest ended records go; a live one, and
  one with a `pending` resume, never.
- `Recover()`, at open once the lock is held: a record with no end is
  marked as no longer observed, never as ended: a server session's `lost`,
  a hosted one's `host away`, `LostAt` its last write's time and `EndedAt`
  left empty. That the lock's last holder is gone says nothing of the
  session's process: a Runtime's context bounds its start, never its
  process's life (`ext.Runtime.StartTerminal`, `ext/runtime.go`), nothing
  promises that a guest's terminal ends with the server (Boxcar constraint
  7), and even a native child can outlive a server that crashed. The
  record page says "not observed since <time>; whether its process ended
  is not known", and Resume from it asks first ("Resume from a record").
  A hosted record's host's next register reopens it (`Begin`, above). A `pending` resume receipt is settled by the rule of "Resume from
  a record" below. Run once, before the first `Begin`.
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
opens the journal with its data directory (none without one) and wires it
at these places, each named as it is on `main`:

- **A server session's start**, in `createLocalSession`: `Begin` once the
  session's `Info` is made and before `session.NewLocal` (so the record
  exists before the session can keep an entry; a session the registry then
  refuses, `too_many_sessions`, has its record removed), from its `Info`,
  its `Launched` and `req.where` as `Where`. **The launch revision** is read
  before the process starts: for a session whose `where` is native, one
  `git rev-parse` (argv, the C locale, 2 s; `--show-toplevel`,
  `--git-common-dir`, `HEAD` and the branch's name in the one call, so the
  four describe one revision; the common directory made absolute as
  `crew.RepoRoots` makes it, into `RepoCommon`) in the opened folder, before `rt.StartTerminal`, `StartFrom: launch`; a
  read that times out or finds no repository leaves them empty, "not read"
  on the record page. A crew member's attempt takes the ledger's start
  commit for its attempt instead (the attempt's `StartCommit`, the
  engine's own, `StartFrom: attempt`), read by the indexer from the ledger
  rather than from git. A session whose `where` is not native gets no git
  read on the server at all: its repository and commits are "on <where>"
  and unknown, never the server's folder read natively. The end commit is
  read the same way after the end (native only, on the writer's
  goroutine), and is shown as "HEAD after the end", an observation.
- **A server session's changes**: the `localChange` hook (`OnChange` of
  every server session, `sessions.go`) calls the journal's `Changed(id)`,
  which marks the record's metadata stale and wakes the writer, never
  waiting; the `Info` the hook is handed is never copied. A session reads
  its `Info` before it calls the hook, and its calls are not ordered
  among themselves (`Local.notifyChange`, `internal/session/local.go`; the
  race `hostagent`'s `noteChange` and `forwardChanges` document and
  handle, #482), so a copied snapshot could put an older, higher Codex id
  back over the lowest one the session chose, or a resumable id back to
  not resumable. The writer instead reads the session as it is when it
  gets to it, through a `Current(id)` callback the API hands `Open` (the
  registry's `Info` of that id, as `publishLatest` reads `listedInfo`),
  which is at least as new as every change marked before the read; a
  change after the read marks it again. It answers the journal's own
  `Meta` (the journal imports no session package), and from it the
  record takes the agent's own session (`ID`, `Source`, `Resumable`), the
  name, **the crew** and the status. A session adopted into a crew after
  its start (`handleSelfCrew`, `internal/api/selfservice.go`:
  `LaunchAdopting`, then `Local.SetCrew`) gets its `Crew` this way, and a
  record whose `Crew` goes from none to one is written at once, as an end
  is, not at the next flush. An ended `Info` (`Status.Ended()`, its exit
  code and `EndedAt`) ends the record as `End` does, so an end never rests
  on the status activity entry alone.
- **A hosted session's add**: a new `OnAdded` callback on the hub
  (`internal/signal`), called by `Register` after `registry.Add` succeeds,
  which the API sets and which calls `Begin` from the session's `Info`
  (its host's name and user, `Host.Owner` from the hub's owner, the agent's
  id, command, folder and branch as the host registered them). A hosted
  record's Runtime (`Where`), its agent version, its agent's own session
  id, its repository and its commits are what its host reports and nothing
  else: on `main` a host reports none of them (`proto.HostSession` carries the agent's id, the
  command, the folder and the branch), so they stay empty and are said
  "not reported by the host" until PR-12 adds them (an empty `Where` is
  unknown, never native: `hostagent.Options` takes a Runtime of its own); the server's probe
  cache and a git read on the server would describe the server's machine,
  not the host's, and are never used for a hosted record.
- **A hosted session's changes**: the hub's `OnChange` (`s.hosts.OnChange`,
  `server.go`), which carries a hosted `Info`; `localChange` is never
  called for a hosted session. It calls `Changed(id)` the same way, and
  `Current` reads the hosted session's `Info` as it is then. A hosted
  session's exit reaches the server twice: as the host's `status` activity
  entry, which the host's bounded queue may drop (`activityForwarder`,
  `internal/hostagent/activity.go`: a full queue drops the entry), and as
  the host's status control message, which sets the hosted `Info`'s status
  and exit code and calls `OnChange` with no activity entry
  (`HostedSession.HostStatus`, `internal/signal/hosted.go`). The record's
  end is taken from the ended `Info` whichever of the two arrives.
- **Every session's entries and end**: a sink on the event hub
  (`eventHub.addSink`), which hands every activity entry of every session,
  server and hosted alike (`s.hosts.OnActivity` is the hub's `activity`
  too), to `Note`/`Touch`/`Tool`, and a session's `status` end to `End`;
  the session's removal from the registry (`session.Registry.OnRemove`,
  the registry's hook, not the hub's) to `End` as well, `host away` for a
  hosted session the hub let go past `DisconnectGrace` (`Hub.Expire`),
  which proves nothing of its process (`LostAt`, no `EndedAt`). **A known
  end is kept**: once a record holds an observed end (an exit or a stop,
  from its `Info` or its status entry), a later removal or expiry changes
  nothing, so "exited (exit 0)" never becomes "host away"; only a record
  with no observed end is marked by a removal. The hub's `chat` (the `OnChat` of
  server sessions, `localChat`, and of hosted ones, `s.hosts.OnChat`)
  calls the journal's `Chat` for a session's chat (one call, never
  waiting; run chat stays the run record's).
- **A hosted session's questions reach its chat.** A hosted chat message
  reaches `s.hosts.OnChat` only through `HostedSession.HostChat`
  (`internal/signal/hosted.go`), which on `main` drops what the record
  needs: it refuses the `question` kind and the agent's role
  (`role.Valid()` is view or control), copies no `Options`, and keeps only
  the `join` and `leave` events. PR-1 extends it, the host still
  untrusted: the `question` kind taken with `By.Role` `agent`
  (`session.RoleAgent`), and that role only on a question; its `Options`
  cleaned and bounded as a server session's (`session.CleanOptions`: at
  most `MaxAttentionOptions`, 6, each label at most `MaxOptionLabel`, 60
  runes); the `answered` and `withdrawn` events kept with their `Ref` (a
  cleaned id) and, on `withdrawn`, a `Text` that is one of the chat's own
  reasons (`ChatLeftTerminal`, `ChatLeftAgent`) or none, any other text
  dropped. `proto.ChatMessage` already carries every one of these fields,
  so the protocol does not change.
- **The agent's version** of a server session from the probe cache
  (`cachedIdentity`, "" while it is cold, said so on the record page).

The routes:

| Route | Who | Answers |
|---|---|---|
| `GET /api/history/sessions?after=&limit=` | admin | `{records: [RecordSummary], next?, readOnly?}`: id, name, agent, host, cwd, repo, branch, start and end, how it ended, counts; limit 1 to 100, default 50 |
| `GET /api/history/sessions/{id}` | admin | the record whole (at most 64 KiB) with `live: true` while the registry lists it running |
| `DELETE /api/history/sessions/{id}` | admin | `204`; `409 live_session` for a session still running; `409 resume_pending` for a record whose resume is being started; `404` |
| `DELETE /api/history/sessions` | admin | `{forgotten: n, kept: n}`: every record neither live nor pending |

All four are `requireAdmin`; a share link's viewer, a session's own token
and Brakeman's principal reach none of them. A server with no data directory
or a switchyard answers `404 history_unavailable`.

### `internal/history` (new, PR-2): the index

A package of the index and its search, the standard library and
`modernc.org/sqlite` only (no HTTP, no session, no other store): it is told
entries and asked queries, and knows nothing of where entries come from.

**The database** `history/index.sqlite`, 0600 in a 0700 directory, opened
as the ledger opens its own (WAL, `synchronous=NORMAL` since it is a cache,
foreign keys, a busy timeout) but for its connections: the owner writes
through one and reads through two others beside it, so in WAL a search
never holds a write back and a write never holds a search (a write waits
only for another write; `TestASearchNeverHoldsAWriteBack`), and a reader
reads through two (PR-2's review, 2026-11-05: with the plan's one
connection a one-entry `Replace` waited 780 ms behind a search); schema
version 1:

```sql
CREATE TABLE schema_version (version INTEGER NOT NULL,
  generation INTEGER NOT NULL,   -- bumped by every clear; a file's first drawn at random
  dropped INTEGER NOT NULL DEFAULT 0,   -- entries the budget let go (Stats)
  open INTEGER NOT NULL DEFAULT 0,      -- set by the owner's Open, cleared by its Close
  unchecked INTEGER NOT NULL DEFAULT 0  -- a Check is owed: the last owner did not close it
);
CREATE TABLE sources (
  key TEXT PRIMARY KEY,          -- "run:<id>", "check:<run>/<task>/<n>/<id>/<k>", "session:<id>", "ticket:<KEY-n>", "brief:<br-n>", "dispatch:feed"
  mark TEXT NOT NULL,            -- what the source said when it was read: a revision, a seq
  indexed_at TEXT NOT NULL,
  bytes INTEGER NOT NULL         -- the text its entries hold
);
CREATE TABLE entries (
  id INTEGER PRIMARY KEY,        -- the FTS rowid only: never served, never a handle
  key TEXT NOT NULL UNIQUE,      -- the entry's stable handle (below)
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
  repo TEXT NOT NULL DEFAULT '',       -- a repository's identity: its common git directory (RepoCommon)
  tree TEXT NOT NULL DEFAULT '',       -- the worktree's top, shown
  revision TEXT NOT NULL DEFAULT '',   -- the label: "r2", "rev 14", a branch
  commit_id TEXT NOT NULL DEFAULT '',  -- a full git id when the entry is about one
  agent TEXT NOT NULL DEFAULT '', agent_version TEXT NOT NULL DEFAULT '', agent_session TEXT NOT NULL DEFAULT '',
  crew TEXT NOT NULL DEFAULT '', run TEXT NOT NULL DEFAULT '', ticket TEXT NOT NULL DEFAULT '',
  outcome TEXT NOT NULL DEFAULT '',    -- passed | failed | cancelled | approved | withdrawn | ...
  actor TEXT NOT NULL DEFAULT '',
  subject_key TEXT NOT NULL DEFAULT '',-- a check's SHA-256 of its argv: the same command elsewhere
  digest TEXT NOT NULL,          -- SHA-256 of the body folded: repeats grouped, never merged
  bytes INTEGER NOT NULL,        -- its texts' size, which the budget counts
  origin TEXT NOT NULL DEFAULT ''-- reserved for TS1: '' for this server's own
);
CREATE TABLE texts (             -- an entry's texts, a row beside it by the same id
  id INTEGER PRIMARY KEY REFERENCES entries(id) ON DELETE CASCADE,
  title TEXT NOT NULL, summary TEXT NOT NULL DEFAULT '', body TEXT NOT NULL DEFAULT ''
);
CREATE TABLE links (
  from_ref TEXT NOT NULL, rel TEXT NOT NULL, to_ref TEXT NOT NULL,
  source TEXT NOT NULL REFERENCES sources(key) ON DELETE CASCADE,
  PRIMARY KEY (from_ref, rel, to_ref)
);
CREATE VIRTUAL TABLE entries_fts USING fts5(title, summary, body, content = 'texts', content_rowid = 'id',
  tokenize = 'unicode61 remove_diacritics 2', prefix = '2 3');
CREATE INDEX entries_at ON entries(class, at);
CREATE INDEX entries_when ON entries(at);                       -- the budget's oldest first, filters' newest first
CREATE INDEX entries_outcome ON entries(class, outcome, project); -- the palette's counts from the index alone
CREATE INDEX entries_anchor ON entries(anchor);
CREATE INDEX entries_ref ON entries(ref, key);                  -- a link's end found by its ref (below)
CREATE INDEX entries_source ON entries(source);                 -- every Replace deletes a source's rows
CREATE INDEX entries_subject ON entries(subject_key) WHERE subject_key != '';
CREATE INDEX entries_digest ON entries(digest);
CREATE INDEX links_to ON links(to_ref, rel);
CREATE INDEX links_source ON links(source);
```

PR-2 measured the shape: with an entry's texts (up to 17 KiB) in its
`entries` row, a receipt's row was a page of its own, and a filter's scan
and a search's sort read and copied every match's texts (two words 129 ms,
the filtered page 213 ms); with them in `texts`, 39 ms and 5 ms, and
`entries_outcome` took the palette's counts from 80 ms to 8 ms.

**An entry's handle is its key, never its row id.** `Replace` deletes and
inserts rows and a reindex makes the database again, and SQLite may give a
deleted row's id to a new one, so the integer `id` is the FTS table's rowid
and nothing else: no route, CLI command or page names it. An entry's `key`
is made from what it is, the same on every read of its source and after
every rebuild: its `ref` when the entry has one of its own (a check
receipt's, a comment's, a plan revision's), else the anchor's ref and the
entry's place in it (`session:<id>#e<seq>`; `session:<id>#f<op>/<path-hash>` for
a `Files` row, which is one per path and op, so a path read and then edited
is two rows and two keys; `run:<id>#command/<actor>/<id>`;
`ticket:<KEY-n>#plan.approved/<revision>/<op-id>`, the `op_id` of the
Dispatch change row that recorded the act, since a revision approved again
after its intent changed keeps both approvals in its history
(`ApprovePlan`, `internal/dispatch/plans.go`) and each is a decision of its
own; `plan.withdrawn`, `plan.superseded`, `ticket.accepted`,
`ticket.unaccepted` and `ticket.closed` are keyed the same way). A key the index no longer
holds answers `404 entry_gone` ("this entry is no longer in the history"),
never another entry; a key whose source was read again answers the entry
as it is now. `TestAKeyOutlivesReplaceAndReindex` holds it: keys taken
from a page, then the source replaced, removed and the whole index
rebuilt; each key answers the same evidence or `entry_gone`; among them a
path read and then edited in one session (two `Files` rows, two keys) and
one plan revision approved, its intent changed and approved again (two
`plan.approved` entries, two keys), each through `Replace` and a reindex.

External content (not Dispatch's contentless table) because a row's snippet
is drawn from its text (`snippet()`), which a contentless table cannot give;
the FTS rows are written and deleted in the same transaction as their
entries, by the package, with no trigger. **One file, rebuilt in place.** Other servers on the data directory keep a
read-only connection to the same `history/index.sqlite`, and a file deleted
under them would leave each reading the old database or failing; the
history lock excludes writers, not those readers. So the owner never
deletes a file it can open: a reindex, and an index of another schema
version (an older one, or one a newer Conductor wrote), are cleared in
place, in one `BEGIN IMMEDIATE` transaction that drops every table and
makes this version's schema, `schema_version` written in it. A reader's
connection sees the database before that commit or after it, never between
(WAL), and SQLite prepares its statements again on the schema's change.
Every search reads `schema_version` first, in its own read transaction,
and a reader of another version answers `indexing: {state:
"other_version"}` with no rows rather than read tables it does not know
(the newer server rebuilds the index to its own when it next holds the
lock). Only a file SQLite cannot open as a database, or one that fails its
quick check and cannot be cleared, is moved aside (renamed
`index.sqlite.damaged`, removed at the owner's next start) and made again;
each reader compares the file it opened with the path (`os.SameFile` on a
`stat`, at each search) and opens it again on a change. A file made again
starts at a random generation (never 1 again), so a cursor of the old file
answers `stale_cursor` in the new one. A cancelled context, a busy file or
a full disk is never damage: the file is left as it was. It is a cache:
nothing is lost that the stores do not hold; each of these is logged.

**The quick check is off the start path.** `PRAGMA quick_check` reads the
whole file (8 s for PR-2's 238 MB benchmark index, in memory: the driver
is transpiled C and the check is CPU-bound), so the owner's `Open` does not
run it: it opens at once, reading the schema's version and nothing more,
and marks the index open (`schema_version.open`), which its `Close`
clears. An owner that finds the mark still set (the last one crashed or
was killed) owes the index a check (`unchecked`): until `Check(ctx)` has
read it whole, every search of it, the owner's and a reader's, answers
`indexing: {state: "checking"}` with no rows (`StateChecking`; an entry,
its links and `Similar` answer not ready), never a row of a file not yet
known to be sound, while writes, `Mark`, `Sources` and `Stats` go on.
`Check` reads through one of the owner's readers, so writes go on beside
it; sound, it ends the state; damaged, it clears the index in place, or
moves the file aside when the clear meets the damage too, and the
indexer's catch-up writes it again. A clean stop owes no check, and SQLite
answers `SQLITE_CORRUPT` when a read or write meets a bad page anyway.
`TestAnIndexNotClosedIsCheckedBeforeItIsSearched`,
`TestCheckClearsADamagedIndex`, `TestACancelledOpenKeepsTheIndex`. A
`Check` whose context ends before its move aside has begun answers the
context's error and changes nothing, the index still owed its check; one
that holds the index whole and has begun the move aside finishes it
(`TestACancelledCheckChangesNothing`). `Close` returns only once every
call begun before it has ended, each with its transaction committed or
rolled back (a cancelled call waits for the rollback database/sql runs for
it) and its connection back in its pool, and every connection is closed,
so nothing reads or writes the file after it; so does every other `Close`
made while it runs (`TestCloseWaitsForAWrite`,
`TestCloseWaitsForACancelledRollback`). A clear marks the index open and owes it no
check in its own transaction, so clears that overlap never read the
owner's own mark as a crash (`TestOverlappingClearsOweNoCheck`). The
package's doc says who may call what while what else runs (a write waits
only for the write before it, a read only for a free reader, no lock held
across either wait), and `life_test.go` holds each pair at a seam.

**The kinds and their class** (`kinds.go`): one table, `classOf(kind)`, the
only place an entry's class is decided. An entry's class comes from what
made it, never from its words, how often they recur or who repeats them:

| Class | Kinds | Made by |
|---|---|---|
| `record` | `session`, `run`, `task`, `attempt`, `ticket`, `plan`, `launch`, `step` (a project manifest's setup or service step that failed); PR-9 adds `decision` (a DR1 decision request) | the stores' own objects |
| `said` (Conversation) | `session.typed`, `session.answer`, `session.question`, `session.report`, `session.file`, `session.chat`, `attempt.result` (a submitted summary and issues), `review.verdict` and `review.finding` (an agent reviewer's), `attempt.checkpoint`, `attempt.blocker`, `message`, `comment`, `brief.item` | a person's or an agent's words as Conductor received them |
| `decided` (Decision) | `command` (pause, unpause, cancel, retry, accept, accept anyway, with its actor), `plan.approved`, `plan.withdrawn`, `plan.superseded`, `ticket.accepted`, `ticket.unaccepted`, `ticket.closed`; PR-9 adds `decision.response`, PR-10 `review.batch` | a person's act the server recorded with a receipt |
| `checked` (Checked) | `check` (a task's check), `verify` (a project manifest's verify step), `candidate.verified` (a result commit whose every check passed), `delivery.verified` (a delivery whose candidate's receipt is kept); PR-10 adds `review.resolved` (a finding's resolution verified against a new candidate, by RV1's own receipt) | a receipt of something the server ran on an exact commit |

A review's verdict is an agent's words (`said`) even though the engine acts
on it: what the engine did is the task's next attempt and its `reopened`
link, a record. A Brakeman brief's fact is `said` too (Brakeman's account),
linked by `cites` to the records it names, which are indexed in their own
classes. Nothing in the table is `checked` but a receipt. The table is closed: a
kind it does not hold is refused (`ErrKind`), so the PRs that index new
records (PR-9, PR-10) change `internal/history`'s table, its bounds and its
classification test in the same PR. A review finding stays `said` for good
(`review.finding`); its verified resolution is a separate entry of its own
`checked` kind, never the finding's class changed.

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
| a check's summary: its argv, joined by spaces, cut at a rune boundary with "… (N arguments)" when longer | 1 KiB |
| a check's body: its log alone, never the argv: a log of at most 16,384 bytes whole; a longer one its first 4,096 bytes, the separator `\n…\n` (5 bytes) and its last 12,283 bytes, each slice cut back to a rune boundary (so never longer); the ledger's `tail` alone for a check that passed | 16 KiB (16,384 bytes) |

**The budget.** At most 200,000 entries and 256 MiB of text (`MaxEntries`,
`MaxBytes`); a `Replace` that would pass either drops the oldest entries by
`at` (whole sources first when a source is wholly older than the cut) in the
same transaction, and `Stats()` says the oldest time still indexed and how
many entries were let go. Old history is dropped by age alone, never by
class.

**The search** (`query.go`): `Search(ctx, q Query) (Page, error)`. A `Query`
is parsed from the person's line by `ParseQuery(s)` (at most 512 bytes, at
most 16 terms):

- words, read by the same rule as Dispatch's search reads them
  (`matchTerms`, `internal/dispatch/query.go`): a word with no letter or
  digit is dropped, every other is quoted so that `OR`, `NOT`, `a:b` or
  `CND-12` is a word to match and never FTS5 syntax, a word repeated is
  kept once, and the last word is a prefix at any length; every term must
  match (AND), in the title, the summary or the body. HS1 keeps its own
  copy of that rule (`internal/history` imports nothing else of the
  module), because the search mode shows Tickets and History for the same
  typed line and the two must find the same words in it;
  `TestHistoryAndDispatchReadWordsAlike` (`internal/api`, which imports
  both) holds the two to one table of lines. HS1 adds two things Dispatch's
  search has no use for, and only these differ: `"quoted phrases"` (a
  phrase is one term, its words in order: a failing test's line), and the
  filters below, which Dispatch's search does not take;
- filters, each `name:value`, a comma for alternatives:
  `class:said|decided|checked|record` (also the words `conversation`,
  `decision`), `kind:`, `outcome:passed|failed|…`, `project:CND`, `repo:`
  (words of the repository's path), `host:`, `agent:` (a catalog id),
  `crew:` and `run:` (in `internal/crew`'s grammar: a crew's id of at most
  64 bytes, a run's that id, a dash and eight hex digits),
  `ticket:CND-12`, `session:`, `since:` and `until:` (`7d`, `24h`,
  `2026-11-01`; a span longer than a Go duration holds, past `15250w`,
  refused);
- an unknown filter name is a word (so `http://x` searches for it), and a
  malformed value is refused with words (`ErrQuery`).

Order: with words, FTS5's `bm25(entries_fts, 4, 2, 1)` (title, summary,
body), then newer first; with filters alone, newer first; in both, last by
the entry's `key`, which is unique, so the order is total and entries of
the same text, score and time keep one place each. Class never weighs in
the order. A page holds at most 100 entries (default 20) after a cursor,
with the total when it is at most 10,000 ("more than 10,000" past that,
which the count stops at). **The bounds of a search of words**
(`MaxScanned`, `MaxRanked`, PR-2's review, 2026-11-05): bm25 is computed
for every row it ranks, so a word in nearly every entry (`test`, `go`,
`error`, a package's name, or a prefix of one or two letters) cost what
ranking 85,000 rows does (850 to 940 ms on the benchmark index, and a
write waited behind it). A search first asks the full-text index for its
matches' rowids, newest indexed first, and stops at 10,001 (a walk of the
doclist an `ORDER BY rowid DESC LIMIT` lets FTS5 stop). At most 10,000
matches: it reads them, newest indexed first, with the filters applied,
and ranks the newest 2,000 of those that pass by bm25, so a search that
passes at most 2,000 is ranked whole, exactly as above; more passing is
`cut: "ranked"`, the total still every one that passes. More than 10,000
matches: it reads the newest indexed 10,000 and answers those that pass the
filters newest first, unscored, `cut: "scanned"` (with no filters the
total is "more than 10,000"; with filters it counts those it read that
pass). A bound never makes an answer wrong: it says what was read, and the
palette and the History page show it ("the newest 10,000 matches; add a
word to rank them"). `TestASearchPastItsBoundsSaysSo`. **The cursor**
(`next`, opaque, at most 2 KiB, since it carries the last row's key, which
may itself be 512 bytes, each `"` escaped in its JSON: base64 of JSON
decoded with `DisallowUnknownFields`) holds the last
row's sort values (its score as the float64 it was, which JSON carries
exactly, its `at` and its `key`), a digest of the parsed query and the
index's `generation`; the next page is the rows strictly after that tuple
in the same order, compared as one row value as Dispatch compares its
`(keys…, id)` (`internal/dispatch/query.go`). A cursor that does not
decode, or of another query, is refused (`ErrCursor`, `400 bad_cursor`);
one of an earlier generation (a reindex or a schema rebuild has cleared
the index since) answers `409 stale_cursor` ("the history was rebuilt;
search again"). Within a generation, sources indexed between two pages can
move a row's score, so a row may be seen twice or missed, as a Dispatch
page over tickets that changed may; the History page drops a key it
already shows. Each entry comes with `snippet()` of its best
column (at most 300 bytes, the match marked: cut around its first match,
never through a mark, `…` where it was cut, since `snippet()` bounds its
window in tokens and long tokens before the match would put it past a cut
from the start; a match longer than the window keeps both marks around its
cut start, `[start…]`; the marks are U+E000 and U+E001 until the cut, and a
text's own are indexed as U+FFFD), its `repeats` (how many
entries of the same digest and class exist and in how many anchors: "said in
3 sessions", computed for the page's rows only), and at most 8 of its links
each way, each far end with the entry that stands for its ref: the entry
keyed by it, else the first by key of those whose ref it is (a command's
key, `run:<id>#command/<actor>/<id>`, is not its ref,
`command:<run>/<id>`; `TestALinkFindsItsEndByItsReference`). `Similar(ctx, key, scope, limit)`, for "did another crew solve
this": the receipts of the same command (`subject_key`, the SHA-256 of the
argv) outside the entry's run chain (given by the caller), **in the
entry's repository** (`repo`, or its project when it has one) unless
`scope` is `all`, ranked: first those whose body also holds the entry's
distinctive line (the last non-empty line of a failed check's tail, as a
phrase), passed before failed, then the rest of the same command's, passed
before failed, each group newest first; then, still in the repository,
receipts of other commands that hold the distinctive line. A generic
command (`go test ./...`, `make test`) is run by every crew of every
repository, so matching on the argv alone across repositories would bury
the one receipt that shares the failure under every passing run anywhere;
`scope: all` (the route's `scope=all`, the CLI's `--everywhere`, the
palette's "Find this in every repository") widens the same ranking to
every repository. `TestSimilarStaysInTheRepositoryAndRanksTheLineFirst`
holds it with two repositories whose crews share one argv. `Entry(ctx,
key)` and `Links(ctx, ref, limit)` (at most 100 each way) read one row.

**Measured.** `BenchmarkSearch` over 100,000 entries of mixed kinds (a
fixture written by the test): a two-word search, a filtered page, a phrase,
a prefix of two letters, and the searches that reach the bounds (a prefix
of one letter, the fixture's most frequent word alone, with a second, and
with filters); the target is 50 ms at the median for each on the Linux box
(Dispatch's target and box), measured in PR-2 and recorded in its body,
the numbers past it reported without a target as Dispatch's PR-4 did.

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
without it serves searches from the index as it is, read-only, and says so),
off the server's start path, as the ledger awaits its lock. The owner's
`Open` does not check the file (above); when `Unchecked` says the last
owner did not close it, the indexer runs `Check` in the background at once
(a goroutine of its own, the server's context, never on the start path)
and indexes beside it; searches meanwhile answer `indexing: {state:
"checking"}`, and a `Check` that cleared the index or moved it aside is
followed by a catch-up from nothing. Every write of a pass is of the
index's generation as the pass began (`Source.Generation`; `Replace`
judges it in its own transaction and refuses another, `ErrStale`, and the
source is read again), so no pass that ran across a clear writes a mark
over entries the clear took, and a crash at any point leaves nothing a
restart's catch-up may not trust: no repair is owed that only the process
remembers (PR-3, review rounds 1 and 2). The server's shutdown
calls `Close`, which waits for a write in flight and marks the index
closed, so the next start owes no check.
A dirty set of source keys, filled by the commit hooks (a run's id from the
ledger's commit and prune and from `runs/<id>.json` saves, with
the session sources of the run's members; a ticket's key from Dispatch's
changes, and every session source for a change of a project; a session
record from the journal's writes; a brief from `brakeman/` saves), bounded at 10,000 keys: past that the set is
emptied and a full catch-up is asked instead, so nothing is dropped. One
goroutine takes the oldest key and indexes that source as it is now; a
reconcile whose time has come is made before the next key, so a queue that
is never empty defers it by one pass at most (PR-3, review round 2):

1. the source's mark is read first, then the source (the marks are in
   "Marks" below: a run's is its ledger revision with its record file's
   hash, a ticket's its `rev` and its changes' last seq, a record's `Seq`
   with the projects it was placed by and, for a crew member, its run's
   revision, a brief request's update time);
2. its entries and links are built (the sources below), and `Replace` is
   called with the mark taken before the read, so a change between the two
   leaves the source dirty and read again, never marked as seen;
3. a source that is gone (a run pruned, a record forgotten, a ticket's
   project gone) is `Remove`d, with every source it owns (a run's receipts,
   below);
4. one source at a time, through each store's own read functions.

**A run is read in parts, never whole on every transition.** Every
transition bumps the run's revision, and a pass that read every check row
with its log (`Checks(..., withLog)`) through the ledger's one connection
(`SetMaxOpenConns(1)`, `ledger.go`) would hold the engine's transitions
behind it on every one. So:

- a **finished check receipt is a source of its own**, `check:<run>/<task>/<n>/<id>/<k>`,
  indexed once: a receipt recorded never changes once it is finished
  (`candidateVerified`'s comment, `dispatch_delivery.go`), so its mark is
  `finished` and it is never read again; its log is read by
  `ledger.Check` for that one row, only when it is first indexed. A row
  still `started` or `running` is not indexed as a receipt (it shows in its
  attempt's entry as running) and is read when it finishes; an
  `interrupted` row is indexed once, outcome `interrupted`, never passed or
  failed;
- the **run's own source** (`run:<id>`) is the run, its tasks, attempts,
  commands and messages, read without any log (`Checks(..., false)` to list
  which receipts are new), so its pass reads a few small rows;
- a run source is read **at most once every 2 s** (`minRunInterval`),
  however many transitions marked it, the last change always read after
  the interval;
- `BenchmarkTransitionWhileIndexing` (PR-3) measures a transition's latency
  at the median and the 99th percentile with the indexer idle and with it
  indexing a run of 128 tasks with 1 MiB logs, recorded in PR-3's body; a
  99th percentile past 50 ms more than the idle one is a defect PR-3 fixes
  before it is marked ready.

When the journal holds `history/owner.lock` (at open, or later by its own
`OnOwned` as it takes the lock from a server that let it go), after the
journal's `Recover` and with whatever stores this process has open then, a
**catch-up** compares each source's mark with the index's and marks every
source that differs or is missing dirty, and every indexed source that no
longer exists for removal; an index made empty (first start, a version
change, a reindex, a file moved aside) is filled the same way. It never
waits on the ledger's owner lock: the ledger's `OnOwned` (`openLedger`,
`internal/api/ledger.go`) runs only for the ledger's owner, never while
another process owns it nor when the ledger failed to open, and the two
locks may have different holders. A read-only ledger is read through its
read-only connection, as any of its readers reads it; a ledger that failed
to open in this process leaves the ledger's rows out until a later start
(the run records' files are indexed, each run's mark `R<sha256>` alone,
and `indexing` says `ledger: "unavailable"`). The ledger's own recovery,
when this process takes the ledger's lock later, commits through
`OnCommit` like any transition and is indexed as one. Searches during a catch-up answer what is indexed with
`indexing: {state: "catching_up", behind: n}`. `POST /api/history/reindex`
(admin) clears the index in place ("One file, rebuilt in place", above)
and catches up from nothing: `202 {indexing}`.

**Writes another process makes.** The commit hooks are this process's: the
ledger's `OnCommit`, Dispatch's `OnCommit` and `store.Observe` tell only
the process that committed. Dispatch takes no owner lock and is written by
every server on the data directory (`internal/dispatch/dispatch.go`), and
the ledger's and Brakeman's owners need not be the history lock's holder.
So while it runs the indexer also **reconciles** against what the stores
keep durably, every 30 s (`reconcileEvery`, driven in tests by a seam,
`reconcileNow`): Dispatch's change feed after the last seq it indexed
(`Store.ChangesAfter`, the position kept in the index's `sources` row
`dispatch:feed`), every changed ticket marked dirty; the ledger's run
revisions (`Ledger.Pointers`, paged) against the indexed marks, and,
once every pointer is listed, the runs indexed from the ledger that it no
longer holds (pruned by the ledger's owner, another process, whose
`OnPrune` tells no hook of this one: PR-3, review round 1); the run
records' and the briefs' files by size and modification time, a file whose
stat differs hashed and compared with its mark. The hooks are the fast
path; the reconcile is what makes a write by another server searchable
within 30 s without a restart. PR-3's reconcile reads the ledger's
pointers and the run records' files, the stores of its own sources; PR-4
adds Dispatch's feed and the briefs' files with the ticket and brief
sources.

**The sources** (`history_sources.go`, PR-3 for sessions and runs, PR-4 for
Dispatch and briefs), each a function from what its store holds to entries
and links, every field of the six taken from the record itself:

- **A session record** (source `session:<id>`): a `session` record entry
  (title the name; summary the agent, folder and how it ended; host from
  `Host`; `where_ran` the record's `Where`; repo and branch; `commit_id`
  the launch revision, a crew member's from its attempt's `StartCommit`;
  agent, its version and its own session id, each empty and said so where
  the record has none), then one `said` entry per kept entry
  (`session.typed`, `.question` with its choices, `.answer` with its
  outcome, answered, answered in the terminal or withdrawn, and the
  question it ended, joined by `Ref`, in its summary, `.report`, `.chat`) and one per
  `Files` row (`session.file`, its path as title), each anchored to
  `session:<id>` and placed by `seq`; links `resumed_from` to the session it
  resumed. The project is the Dispatch project whose folder (`Project.Cwd`)
  is in the same repository as the record's, compared by the repository's
  identity and never by a worktree's top: `gitcli.TopOf` answers
  `--show-toplevel`, a linked worktree's own top (`internal/gitcli`), so a
  session in a sibling worktree would miss a project at the main
  checkout. The record's side is its `RepoCommon`, read at launch and
  kept, so a record still maps after its worktree is removed; the
  project's side is its folder's common git directory, read by
  `gitcli.RepoOf` (new in PR-1: one `rev-parse --show-toplevel
  --git-common-dir`, argv, 2 s, the common directory made absolute as
  `crew.RepoRoots` makes it), cached per folder for the pass. A crew
  member's project is its run's. A hosted record's common directory is a
  path on its host's machine and is matched against no server project.
  The index's `repo` holds that identity (a run's from its folder, read the
  same way) and `tree` the worktree's top; `repo:` and `Similar`'s
  repository scope compare `repo`, so crews in sibling worktrees of one
  repository are one repository's. A folder is in no repository only when
  git says so or the folder is gone; any other failed read (git's timeout)
  is not known, never written down as none: the pass fails and is read
  again after its backoff, three times in a row, then is indexed without
  the repository under a mark that says so (`U`), and every reconcile
  reads it again until a read succeeds (PR-3, review round 2).
- **A run** (source `run:<id>`, from its record `runs/<id>.json` and, for a
  managed run, the ledger's rows): a `run` record entry (the crew, the label
  or goal, the base commit, the outcome); per task a `task` entry; per
  attempt an `attempt` entry (its session, worktree, branch, start and
  result commits, its state and stage, `where_ran`) with `session_of` to
  `session:<id>`; its submitted result as `attempt.result` (summary and
  issues), a review's verdict and findings as `review.verdict` and
  `review.finding` with `reviews` to the attempt it reviewed; its checkpoint
  and blocker; each finished check receipt of phase `check` or `verify`, as
  its own source (above), as `check` or `verify` (`checked`), title
  "`<check id>` on `<task> #<n>`",
  `commit_id` its subject, `where_ran` its row's, `subject_key` the SHA-256
  of its argv (summary the argv, body the log, as bounded above), and `outcome` by the engine's own predicate, never by
  `checkState` (Brakeman's display of a row, `brakeman_refs.go`, which
  reads neither `Stable` nor `Error`): PR-3 exports `failedOutcome`
  (`internal/crew/failedcheck.go`) as `crew.FailedOutcome`, and a finished
  row is `failed` when it holds, else `passed`; so an exit-0 check that left
  its checkout changed (`!Stable`) is failed, a leftover child killed after
  the check's process exited (`Killed` alone) is not, as the engine and
  `candidateVerified` read it; an `interrupted` row is `interrupted`, a
  `started` or `running` one is not a receipt yet. Each command receipt of
  the admin's actor (`adminActor`) as `command` (`decided`, its intent and
  actor), ref `command:<run>/<id>`: the ledger keys a command by run, actor
  and id (`Commands` returns every actor's), and Brakeman's grammar names
  only the admin's receipt (`ld.Receipt(ctx, run, adminActor, id)`,
  `brakeman_refs.go`), so another actor's receipt is not indexed until that
  grammar names an actor, a change Brakeman's plan owns; the entry's key
  (below) carries the actor all the same, so two actors' receipts of one id
  never fold into one. Each message as `message`; a work attempt's result
  as `candidate.verified` only by DS2's own verification predicate, the one
  a delivery's receipt is made by (`candidateVerified`,
  `dispatch_delivery.go`: the attempt ended `succeeded`, at least one
  receipt, every receipt finished with exit 0, stable, not timed out and
  with no signal; it does not read a row's `Error`, which the engine's
  `failedOutcome` does), which PR-3 factors into a pure function over the
  rows (`verifiedOf`, that predicate exactly, with no change to what the
  delivery route answers, its tests unchanged), never `candidateOf` alone,
  which only gathers the receipts. The two predicates differ on one row: a
  finished receipt at exit 0, stable, with no timeout or signal, that
  carries an `Error`, which the delivery passes and the engine fails. HS1
  keeps the delivery's predicate as it is (what a delivery keeps is DS2's,
  and changing it is a change to that item's behaviour, named in PR-3's
  issue for its own) and writes `candidate.verified` only when `verifiedOf`
  holds and `crew.FailedOutcome` fails none of the candidate's receipts, so
  no Checked ✓ stands over a receipt the history shows Checked ✗; a failed
  setup, service or readiness step as `step`, judged as Dispatch judges its
  phase (`dispatch.CheckRef.passed`): by its words of failure alone (its
  stable is a checkout's setup's alone, its exit -1 for a probe that did
  not pass and for a service stopped in order), so a step that succeeded
  is none (PR-3, review round 2); its body the row's `Error` alone, never
  its output's tail: a step's output (a service's
  start-up lines, a package manager's log) is terminal output the history
  does not index (PR-3, review round 1). Links: `continues` to the run it continues;
  `launched_from` to its ticket and plan revision (`Run.Ticket`); `handed`
  from a failed receipt to the attempt that was handed it; `passed_after`
  from a failed receipt to the first later receipt of the same check id on
  the same task that passed, in this run or along its continuations;
  `reopened` from a review's `needs_changes` to the task's next attempt.
- **Who owns a link that crosses runs.** A link is written by the source of
  its later end, never by its earlier one: `handed` by the run whose
  attempt was handed the receipt, `passed_after` by the run of the passing
  receipt, which looks back along its `continues` chain for the failed
  receipts it is the first pass after. A run is continued only once it has
  ended, so the earlier run's rows no longer change when its continuation
  is read; the continuation's own passes, as they come, write the links,
  and nothing needs the earlier run read again. A run pruned takes the
  links its source wrote with it (`links.source`).
- **Which attempt was handed which receipt** is the engine's rule, not a
  guess: PR-3 exports the two pure functions the engine's restart and
  continuation paths already use over the ledger's rows (`failedAtCheck` and
  `failingReceipt`, `internal/crew/failedcheck.go`) as `crew.FailedAtCheck`
  and `crew.FailingReceipt`, and the indexer applies them as `previousOf`
  does: the receipt that failed attempt n is handed to the task's next
  counted attempt in the run, or to the first attempt of the task in the
  continuation when attempt n was the run's last. The walk back along the
  continuations is the engine's (`readHistory`): it goes past a run whose
  attempts at the task are all uncounted, as the engine carries the
  receipt through them, and bounds an ancestor's attempts by the continued
  run's withdrawal and count. An attempt a later withdrawal took back
  (its number at or below its task's `Withdrawn`) keeps the `handed` link
  the index wrote while the withdrawal before its launch was still in the
  rows; one rebuilt from nothing after it gets none, since today's rows no
  longer say what it was handed (PR-3, review round 1). A review's subject
  is the task it needs first (`Needs[0]`, the engine's `reopen`), its
  attempt the one whose result the review started from, never another
  task's or attempt's with the same commit: the last whose result stood
  before the review was reserved, one that succeeded or one that failed
  with a result a person accepted anyway (`acceptTask` makes the task
  succeed and leaves the attempt failed; PR-3, review round 2); `reopened` is written by the
  run that makes the repair, the review's own or, when the review's run
  ended before it, the continuation that carries it.
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
`TestHistoryURLsAgreeWithBrakemans` holds for each shared kind, except three
whose `refURL` is not a page of the app that shows that exact evidence
(`internal/api/brakeman_refs.go`):

- a check receipt (`refURL` the raw log route) opens the run page at its
  step
  (`/runs/<run>?view=tasks&task=<task>&attempt=<n>&step=<phase>/<id>/<k>`,
  PR-6);
- a command receipt (`refURL` the API's JSON route,
  `/api/runs/<run>/commands/<id>`) opens the run page at the receipt
  (`/runs/<run>?command=<id>&actor=<actor>`, PR-6): its intent, actor,
  time, state and answer, read from an evidence reader scoped by the
  entry's own actor, `GET /api/history/command?run=&actor=&id=` (admin,
  PR-4), which reads `ld.Receipt(ctx, run, actor, id)` with the actor the
  entry names and answers it with the receipt. The run's command route is
  not that reader: it finds the caller's own receipt (`actorOf(r)`,
  `internal/api/ledger.go`), the read a client that lost a reply makes
  before it sends again, and `requireAdmin` also admits an identity
  provider's administrator, whose actor is `user:<subject>`, so it would
  open that administrator's receipt of the same id, or `404`, and it
  answers no actor. That route and its retry semantics are unchanged;
- a comment (`refURL` the ticket's page, which loads only its latest page
  of comments and locates none by its id; the resolver's `commentURL` is an
  API route) opens the ticket's page at the comment
  (`/dispatch/<project>/<n>?comment=<k>`, PR-6): that comment fetched
  alone by the route `commentURL` names, shown marked at the head of the
  comments with its number among them, or its tombstone.

No row's URL is an API route. A session entry opens `/sessions/<id>` while the registry lists it
and `/history/sessions/<id>#e<seq>` after.

**Availability** (`history_availability.go`, PR-4), computed when a page is
answered, never stored, from what the server holds now and cheap enough for
a page of 100 (no git, no disk read but a `stat`):

| Entry | `availability.state` and its words |
|---|---|
| a session | `live` (listed, running), `listed` (ended, still listed: its page shows its last screen), `record` (only its record), `lost` (only its record, which the server stopped observing while it ran: whether its process ended is not known), `host_away` (a hosted session whose host is not connected now), `forgotten` (a link to a record no longer kept) |
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
(`ResumeNeedsCwd`), checked through `resolveCwd` whatever the record's
`where`, because every launch, in any Runtime, opens its folder on this
server through `openCwd` (`createLocalSession`, `sessions.go`) and lends it
to the Runtime (`ext.TerminalSpec.DirFile`): the folder is the local source
directory a Runtime is handed, not an observation of what ran inside it;
for a `where` other than native, Resume is offered only while this server
still has that Runtime (`runtimeAt`), else "its Runtime <where> is not on
this server" (what stays native-only is what observes a Runtime's
execution: the end commit, an attempt's worktree); the launch's arguments kept (`ArgsKept`,
else "its arguments were too long to keep"); a server session, never a hosted one ("resume it on
<host>: `conductor host` resumes it there"); never a managed attempt ("a
managed run continues from its run page", `409 managed_run`'s words) nor a
Brakeman turn; a `lost` record (its process's state unknown, above) only
with the person's second yes: offered with "the server stopped while this
session ran on <where>; its process may still be running", since
`liveAgentSession` sees only the registry, which lists no process the
server lost; never a crew member of a legacy run from its record, a
member known by the record's `Crew` or, for a record whose adoption the
journal had not yet written when the server stopped, by a run record whose
member state names the session (`MemberState.SessionID`, read through
`crew.Records`):
`handleResumeSession` hands a member to `resumeMember` and the engine
(`Engine.ResumeMember`, `internal/crew/run.go`), which updates the member's
state, the run's session mapping and its lifecycle through `crew.Launcher`
and takes no preallocated session id, so such a record's Resume is
`offered: false` with "resume it as the run's member, from its run" and
the run page's URL, where the member resume already is; the record route
answers `409 crew_member` naming the run's member route, and that route
keeps its own rules, unchanged. `GET /api/history/entry?key=` computes the rest: whether the
result commit is in the repository (`git cat-file -e`, argv, 2 s), the
worktree's branch, a log's size and hash.

**The search routes** (PR-4), all `requireAdmin`, the query in the query
string as Dispatch's search has it (never logged: AGENTS.md):

| Route | Answers |
|---|---|
| `GET /api/history?q=&limit=&after=` | `{entries: [Entry], next?, total, totalCapped?, cut?, indexing: {state, behind?, oldest?, dropped?}, readOnly?}` (`cut` `ranked` or `scanned`, the search's bound, above; `state` also `checking`); `after` at most 2 KiB; `400 bad_query` with words, `400 bad_cursor`, `409 stale_cursor`; the reply at most 2 MiB (rows past it cut, `truncated: true`) |
| `GET /api/history/entry?key=` | one entry by its key (at most 512 bytes), its links each way (at most 100, each with its class, outcome, URL and label) and its full availability; `404 entry_gone` for a key the index no longer holds |
| `GET /api/history/similar?key=&scope=&limit=` | `Similar`'s entries (at most 50), the entry's run chain left out; `scope` `repo` (the default) or `all` |
| `GET /api/history/command?run=&actor=&id=` | `{command: {id, actor, intent, status, receipt, createdAt}}`: the receipt of that actor, never the caller's; `actor` at most 60 bytes, `id` in the command pattern; `404` when the ledger holds no such receipt, `503 ledger_unavailable` as the command route answers |
| `POST /api/history/reindex` | `202 {indexing}` |

An `Entry` as served:

```json
{
  "key": "check:todo-3f2a91c0/build/1/unit/1", "kind": "check", "class": "checked", "outcome": "failed",
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
body `{commandId, withoutEnv?, confirmLost?}` (`DisallowUnknownFields`; `commandId` in the
pattern of run commands, at most 32 characters). It is an operation with
four properties, stated here so the plan stands on its own: **its actor**
is recorded (the admin's principal, `Principal.Subject` as C2 records it,
else the ledger's admin actor); **its scope** is the record and **its
target** the agent's own conversation; **it is idempotent** by its command
id: the same id sent again, whatever happened to the first reply, answers
the first one's receipt and starts nothing; **its receipt is durable**,
kept in the record (`Resumes`), so a restart neither loses it nor turns an
outcome it cannot know into a false one. Its order:

1. under the journal's lock for that record, the command id looked up: a
   receipt of that id answers at once (`200` with the session it started,
   `failed` with why, or `uncertain`, below), whether or not the registry
   lists anything; else, when the record already holds 16 receipts
   (`Resumes` at its bound), the request is refused `409 resume_limit`
   ("this record has been resumed 16 times; resume from the newest
   session's own record") with nothing reserved and nothing launched, as
   the ledger refuses a new command at its cap
   (`internal/crew/ledger/rows.go`): a receipt is never let go to make
   room, since its command id sent again must still answer it and start
   nothing, and the receipts go only with the record (Forget, which a
   `pending` one holds off); else a receipt is **reserved**, `pending`, with a
   session id allocated now for the session to come (`Resume.Session`,
   `session.NewID`) and written to the record before anything else, so
   every later step is under that receipt, the listed case too;
2. `resumeCheck` (PR-4) refuses in the words of the resume routes (`400
   hosted_session`, `409 managed_run`, `409 brakeman_run`, `409
   crew_member` (a legacy run's member: its run's member route resumes
   it, through the engine), `400 invalid_agent`, `409 already_resumed`, `400 invalid_cwd`), a record
   whose arguments were not kept answers `409 args_not_kept`, a record
   whose `where` names a Runtime this server does not have answers `409
   runtime_gone`, and a record whose launch had variables answers `409
   env_not_kept` naming them, unless `withoutEnv: true`, which resumes
   without them and says so in the reply's notice, and a `lost` record
   answers `409 lost_unconfirmed` with the words above unless
   `confirmLost: true`, which the record page sends only once the person
   has read them and asked again; a refusal settles the receipt `failed`
   with its code;
3. the launch is one function both routes share, `resumeLaunch` (factored
   out of `handleResumeSession` in PR-5, the route's answers and tests
   unchanged): it takes what to launch (a listed session's `Launched`, or
   the record's launch, folder and `where`) and the allocated id, and
   makes the session through `createLocalSession` (the agent, the name,
   the folder through `resolveCwd`, the arguments, the yolo choice, the
   record's `where` as the launch's, `resume` the agent's session id,
   `resumedFrom` the record's id, holding the conversation through
   `takeResume`), with the allocated id as the new session's id (a field
   of `createSessionRequest` the resume alone sets), so it starts through
   the server's Runtime like every session. A plain session the registry
   still lists is launched from its own `Launched` by this same function,
   not by calling its route, so the receipt in step 1 covers it; a crew
   member, listed or not, never reaches this step (step 2's `409
   crew_member`): its resume is the engine's;
4. the receipt settled `started` with the new session's id, or `failed`
   with why; `201 {session, resumed, notice?, receipt}`.

**A receipt left `pending` by a stop** (the server stopped between 1 and
4) is settled at `Recover` by what the data directory shows, never assumed:
a record of the allocated session id exists (the new session's `Begin`
wrote it, after its process started and before it could do anything that
reaches the record) → `started`, naming it (its record is then marked
`lost` like any other the stop left); no such record → `uncertain` ("the
server stopped while starting the session; its process may have started
before its record was written"), since `StartTerminal` may have returned
before the stop and `Begin` not yet run. The same command id then answers
that receipt; a new command id may try again, and the record page shows an
`uncertain` receipt with its words so the person can look before trying.
A person resumes from a record only by asking: nothing resumes a session
on its own.

**The CLI** (`internal/cli/history.go`), with the workbench token like
`conductor dispatch`: `conductor history search <words…> [--limit n]
[--json]` (a table: class, kind, title, the six fields; `--json` the reply),
`history show <entry key>` (the entry and its links), `history similar <entry
key> [--everywhere]`, `history sessions`, `history session <id>`, `history forget <id> |
--all`, `history resume <id> [--without-env] [--confirm-lost]` (a fresh command id, sent
again once with the same id if the reply was lost, as `conductor dispatch
launch` does), `history reindex`. Completion and help text in `internal/cli`
as its other commands have them.

**The guide** (`internal/api/guide.go`): the operations table gains the
History operations under scope `admin` (no MCP tool: the table's
`tools/list` stays byte for byte as it is, which `TestToolsListIsUnchanged`
holds), so `GET /api/guide` lists them with their CLI forms and bounds.

### `web/app` (PR-6, PR-7): the record page and the run page's step, then the palette and the History page

The pages a palette row opens land before the palette: PR-6 builds every
page a History row's Enter or → can reach (a session record, the run page
at a step and at a command receipt, the ticket page at a comment) and the
record's Resume, on PR-5's route, so that when PR-7
adds the palette and the History page no row leads to a page `main` does
not have.

**PR-6, the record page and the run page's step.**

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
- `composables/useHistory.ts`: the record routes, `resumeRecord` with a
  minted command id sent again once with the same id on a lost reply
  (`utils/tasks.ts`'s rule), `forgetRecord`.
- `pages/history/sessions/[id].vue`: a session record: its header (name,
  agent with its version and own session, host, folder, repository, branch,
  start and end commits, how it ended), its run, task and attempt with
  links, its entries in order with anchors `#e<seq>` (a typed line, a
  question with its choices and how it ended, a report, a file with its
  count), the dropped count, what a record never keeps (a person's typed
  lines and keystrokes, said in one line), Resume (asks first; for a
  record with variables, the dialog names them and offers to resume
  without them; for a `lost` record, it says its process may still be
  running and sends `confirmLost` only on a second yes),
  Forget (asks first), and the receipts of earlier resumes (an `uncertain`
  one with its words).
- The run page (`pages/runs/[run].vue`, `components/RunTasks.vue`,
  `RunAttemptSteps.vue`): `?attempt=<n>&step=<phase>/<id>/<k>` beside
  `?task=` opens that attempt's steps and that step's log with its SHA-256,
  as the ticket page's receipt does (`DispatchReceipt.vue`, reused); read as
  `?task=` is, never written back. `?command=<id>&actor=<actor>` opens
  that command's receipt (intent, actor, time, state, answer) from
  `GET /api/history/command` (PR-4), the actor the URL names, in a panel of
  the run page, read the same way.
- The ticket page (`pages/dispatch/[project]/[n].vue`): `?comment=<k>`
  fetches that comment alone (the comment route with `after=k-1&limit=1`,
  as Brakeman's `commentURL` names it) and shows it marked at the head of
  the comments, with its number and a way to the comments around it, or
  its tombstone; read, never written back.

**PR-7, the palette and the History page.**

- `utils/palette.ts`: `PaletteMode` gains `history` with prefix `~` in
  `MODE_PREFIX`; `parsePrefix` and `dropPrefix` take it; the `?` modal's
  prefix list shows it.
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
  `Resume` only when `availability.resume.offered`, through the record's
  resume route (PR-5) for a listed session and a record alike, `Find this
  elsewhere` setting the rows to the similar route's answer in the entry's
  repository and `Find this in every repository` to its `scope=all`
  answer, `Copy reference`); a "Loading…" row while
  the first answer is on its way and "Indexing: N sources behind" when the
  reply says so. In the search mode, from three characters, a History group
  of the five best rows after Tickets and before Shared with you, and its
  last row "All history matches (~)" switching the mode with the query kept.
- `components/CommandPalette.vue`: a row's badge (a `UBadge` in the brand
  palette of `app.config.ts`: Checked ✓ success, Checked ✗ error, Decision
  primary, Conversation neutral) and its third line; nothing else of the
  component changes.
- `pages/history/index.vue`: the search box (the palette's grammar), filter
  chips that write the query's filters (class, outcome, project from
  Dispatch's projects, host from the records, agent from the catalog,
  since), the results grouped under their anchors, paged with "Show more"
  (the reply's `next`), the indexing state; the URL keeps the query (`?q=`)
  so a search is a link. On a phone, one column, the detail line wrapped.
- The sidebar's foot of pages and the palette's Go to gain History, with
  `G H` (free today) and a terminal chord only where `chords.ts` has
  `Alt+H` free on every system.

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

### `internal/proto`, `internal/hostagent`, `internal/signal` (PR-12): what a host reports of its sessions

A hosted session runs on its host's machine, so its provenance is the
host's to read: the server's probe cache and a git read of the hosted
folder on the server's disk describe the wrong machine (PR-1 leaves them
empty for that reason). PR-12 makes the host report them, through the
three protocol places together:

- `proto.HostSession` (the register message) gains `where` (the host's
  Runtime, `Options.Runtime.Where()`, at most 64 bytes, in the pattern
  `createSessionRequest.where` takes), `agentVersion` (at most 64 bytes),
  `repo` and `repoCommon` (each at most `MaxCwd`), `startCommit` (40 or 64
  lowercase hex digits), each optional. The host reads `repo`,
  `repoCommon` and `startCommit` in its own folder with `gitcli` (argv, the
  C locale, 2 s, one `rev-parse`) before it starts the agent's process, and
  only when its Runtime is native (`hostagent.Run` already tells the two
  apart, `opts.Runtime.Where()`): a host whose Runtime is another reports
  its `where` and no git fact, as the server does for its own non-native
  sessions; `agentVersion` from its own probe of the agent where it has
  one. A hosted record's `Where` is the reported one; for a host that
  reports none it stays empty, said "not reported by the host", and is
  never read as native.
- one new host message, `session_info` (`proto.HostSessionInfo`, at most
  2 KiB): `endCommit` once its process has ended (read as the start's
  are, native only).
- **A hosted agent's own session is captured where its hook reports it:
  on the server.** A hosted agent's hooks report to the server directly
  (`handleAttention`, `internal/api/attention.go`), which captures the id
  only for a `*session.Local` (`captureAgentSession`), and `hostagent.Run`
  makes its session's `Info` with no `AgentSession` and applies no session
  recipe (it runs the argv it was given), so no host ever has an id to
  report. PR-12 captures it on the server for a hosted session too: the
  checks `captureAgentSession` makes (the catalog entry of the hosted
  `Info`'s agent id, its recipe's `IDFrom: hook`, `ValidAgentSessionID`
  with its pattern; none of them, nothing captured) and the recipe's
  policy (`latest`, or Codex's `lowest`) through one pure function
  factored out of `Local.ReportAgentSession` (`session.NextAgentSession`,
  a server session's behaviour unchanged), the result handed to the
  journal (`Journal.AgentSession(id, as)`), never to the hosted session's
  `Info`, which is its host's. The recipe is the catalog's description of
  the agent's id format and policy, not a fact of the server's machine. The host sends it
  only to a server whose `registered` reply says it takes it (a new
  `sessionInfo: true` field; an older server sends none, and an older
  server's `json.Unmarshal` of the register ignores the new fields).
- the hub checks every field at its bound (a register or a message past
  one refused, `ErrBadRegister` or the message dropped and logged) and
  hands them to the journal through `OnAdded` and a new `OnSessionInfo`
  callback; a host that reports none (an older host) leaves them empty and
  the record says "not reported by the host", never a server's fact in
  their place.
- `web/app/utils/protocol.ts` gains the types, `docs/protocol.md` the
  fields, the message and their bounds; `internal/hostagent` is the launch
  site that reads them, through its own Runtime (Boxcar constraints 7 and
  8).

Resume of a hosted session stays the host's (`400 hosted_session`,
unchanged): the reported agent session is evidence, not a door.

### Later PRs (PR-9 to PR-11): what waits on other items

- **PR-9, DR1's decision records** (waits on DR1's PRs that serve
  DecisionRequest records, `docs/plans/decisions.md`): a source
  `decision:<id>`: the request as `decision` (`record`: its question, its subject
  revision as `revision` and `commit_id`, its scope), each response as
  `decision.response` (`decided`, its responder and rationale, `outcome` the
  option), its application receipt as a link (`applied`), a superseded or
  stale request said in its availability. DR1 owns the record, its states
  and its receipts; HS1 indexes what DR1 serves and adds no field to it.
  `internal/history`'s kinds table gains `decision` (`record`) and
  `decision.response` (`decided`) in this PR, with their bounds and their
  rows in `TestClassComesFromTheKindAlone`. The
  palette's "Rejected designs" question gains `decision.response` with a
  rejecting outcome.
- **PR-10, RV1's review batches and findings** (waits on RV1,
  `docs/plans/review-repair.md`): a batch as `review.batch` (`decided`:
  Request changes or Approve, by the reviewer, bound to its candidate and
  base revision), each finding as `review.finding` (`said`, its anchor in
  the candidate's diff), a finding's resolution verified against a new
  candidate only as RV1's own receipt says, as an entry of its own,
  `review.resolved` (`checked`, linked `resolves` to the finding), while the
  finding stays `said`. RV1 owns findings' identity across revisions; HS1
  shows it. `internal/history`'s kinds table gains `review.batch`
  (`decided`) and `review.resolved` (`checked`) in this PR, with their
  bounds and their rows in `TestClassComesFromTheKindAlone`.
- **PR-11, Brakeman reads history within its grant** (waits on Brakeman's
  PR-9 to PR-13 merging, `docs/plans/brakeman.md`; it lets Brakeman find
  earlier sessions, results and investigations, which Program item BK1
  asks of it): Brakeman's `observe` gains a `history` section answering the
  History search for the `brakeman` principal, each entry filtered by the
  request's grant as Brakeman's refs are (`refGrant`: an entry's class of
  record visible, its project or repository covered by the scope), cited in
  a brief by the entry's own `ref`, never by an index id. A cited session
  must also resolve: `lookupSessionPart` (`internal/api/brakeman_refs.go`)
  reads only the registry and the engine's live members, so after a
  restart a plain session's `session:` ref would read as missing, with no
  project or repository to check a scope against, and `refURL` would name
  its absent session page. PR-11 gives the resolver the journal as a third
  source for a session the registry does not list: the record's
  repository (`Repo`, the worktree's top it read at launch, compared as the
  resolver's `top` compares a live session's; none for a hosted record, as
  for a live hosted one) and its run (`Crew`, through `memberItem`) for the
  grant's scope check, its state (`record`, or `lost`) as the ref's now, a
  line in the turn's read log naming the record as the source read
  (`brakeman.ReadLog`), and `refURL` the record page
  (`/history/sessions/<id>`). Brakeman's plan owns the observe tool, the
  grant, the brief and the resolver's rules; HS1 owns the search and the
  record source it adds to the resolver. No attempt session gets a history
  tool in HS1.

Each of the three adds its paragraph to the features entry PR-8 writes.

## Events, orderings and invariants

**The session record's states.** `open` (begun, its session live) →
`ended` (its `status` end or its removal from the registry: `EndedAt` and
`Ending` set, the writer woken to write it at once, the end commit read
after on the writer's goroutine, native only; from its status entry or
its ended `Info`, whichever comes first, and kept against a later removal)
or `lost` (a server session's record found open by `Recover` after a stop
the session did not see: `LostAt` its last write's time, no `EndedAt`, its
process's state unknown) or `host away` (a hosted record found open by
`Recover`, or let go by `Hub.Expire` past `DisconnectGrace`: `LostAt`, no
`EndedAt`; its process runs on its host and may still be running). `host away` and `lost`
on a hosted record → `open` again when the same host's session registers
under the same id (`Begin`'s reopening: same `Host.Owner`, `Reopened`
counted, entries and `Seq` kept). A record exists in memory before its
session can keep an entry (begun before `session.NewLocal` for a server
session, written at once there; at the hub's add for a hosted one, whose
entries come from its host only once it is registered, written by the
writer before anything else it holds) and is written at its end at once;
between, at most every 5 s.

**Entries reach a record out of order.** The hub calls its sinks on the
goroutines that recorded the entries, concurrently and out of order, after
the session's lock, so a session's end can reach `End` before an earlier
report or chat line reaches `Note`. A record therefore places entries by
`At` (then arrival), and after `End` still takes an entry whose `At` is
not after `EndedAt`, writing it at the next flush; an entry after
`EndedAt` is dropped and counted. An ended record changes only by such an
earlier entry, by a resume's receipt, by a hosted record's reopening, or
by `Forget`. `TestAnEarlierEntryHeldPastTheEndIsKept` holds a report's
callback at a gate in a test sink until `End` has run and been written,
then lets it go: the record holds it, in its place by `At`.

**A resume's receipt.** `pending` (reserved under the record's lock, with
the new session's id allocated, before any check or launch) → `started`
(with that id), `failed` (with why, a refusal's code included), or, settled
by `Recover` after a stop, `started` when the allocated id's record exists
and `uncertain` when it does not. One command id, one receipt: a repeat
answers it and makes nothing, whether or not the session is listed and
whether or not the session it started has ended; two command ids for the
same conversation race on `takeResume`, and the second answers `409
already_resumed`.

**Marks.** Every source's mark is read before the source, and recorded with
the entries only when the read loaded (AGENTS.md, "Mark what you read with
what you saw before reading it"). A **run's mark covers every input its
source reads**: `L<revision>/R<sha256>/J<project>`, the ledger's run revision
(`ledger.Pointer`, every transition bumps it) read before the rows and the
SHA-256 of the record file `runs/<id>.json` as read, because the record
changes without a transition (the engine's `crew.OnChange` sets a member's
`AgentSession` and saves the record, `internal/crew/handoff.go`, with no
ledger write); a legacy run, which has no ledger rows, has `R<sha256>`
alone; `J` the Dispatch project its folder's repository is in (left out
when none), since its entries and its receipts' carry it, so a project
made over the run's repository, moved or archived places the run again
(PR-3, review round 1). A record change with the ledger's revision
unchanged therefore changes the mark, and the catch-up after a restart
sees it.
`TestARecordOnlyChangeIsSeenAfterARestart` changes a member's agent
session with no transition, restarts, and finds the new id indexed. A
finished receipt's mark is `finished` (never read again), with
`/J<project>` when its run is in a project: a run's pass whose project
changed writes each held receipt's entry again under the new one from the
entry the index holds, its log never read again; Dispatch's is the
ticket's `rev` with its changes' last seq, and the feed's position for the
reconcile; a session record's covers every input its source reads, as a run's
does: `S<Seq>/P<projects>`, and `/C<start commit>/J<project>` for a crew
member. `S` is
its `Seq` (from `history/state.json`, written before the record, so it
never goes back); `P` a digest of Dispatch's projects as the pass read
them (the SHA-256 of their keys, their folders' common git directories
and their archived flags, computed once a pass), since a session's
project is derived from them; for a crew member `C` its attempt's
`StartCommit` and `J` its run's project, the two things its entries take
from its run, read before the mark is compared (a failed read is the
pass's error, retried, never a mark over an entry without them). An
earlier draft carried the run's ledger revision (`L`) instead, which
every transition bumps, so every pass of a run read every member's whole
record again for one value that almost never changes; a run's pass now
marks a member's session source only when that member's start commit is
new to it (PR-3, review round 1). None of these changes the journal's
`Seq`, so a project made or moved, or an attempt's start recorded,
changes the session's mark instead: a project's change in Dispatch's
commits or feed asks a catch-up that compares every session's and run's
mark, a run's pass marks the session source of a member whose start
commit changed, and the start's catch-up compares the whole mark, so no
session or run stays under an old project after a restart; a brief's is
its
request's update time. A source changed between its mark and its read is
read again on the next pass, because its commit marked it dirty after the
mark was taken (or the reconcile finds the mark behind).
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
  path its deny list covers, a viewer's arrival, a terminal's output, or a
  line a person typed into the terminal or a reply box.
- The index's text is bounded by kind and in total; history older than the
  budget is dropped by age and the reply says from when it holds.
- A Forget removes the record's file and its entries, the entries before
  the route answers: the route drops the record's sources from the index
  (`dropSessions`) under the lock a pass takes to publish a session
  record, and a pass publishes one only while the journal still answers
  for it, judged under that lock (`publishSession`; a record a Forget,
  ForgetAll or Prune has chosen is hidden from `Get` until the removal
  returns, PR-1's rule). So a forgotten record's rows are gone from the
  index before Forget answers, and no pass publishes a record its source
  no longer answers for; an index that cannot take the drop (not open, a
  write refused) makes the route answer `503 history_index_failed`, the
  drop owed and taken before the index is read again. A record whose file
  went while no server ran is removed by the next start's catch-up (PR-3,
  review rounds 1 and 2); the
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
journal and catches the index up. What the other server writes to the
stores it may write (Dispatch, which takes no owner lock; the ledger and
Brakeman's store when it holds their locks) reaches no hook of the
holder's, so the holder's indexer finds it by the reconcile (above), from
the stores' durable positions, within 30 s.
`TestAnEditByTheOtherServerBecomesSearchable` (PR-4, with the ticket
source and the search route it needs) edits a ticket through a second
server's Dispatch on the same data directory and finds the edit through
the first's search after one reconcile (`reconcileNow`), with no restart;
PR-3's `TestARunRecordByTheOtherServerIsIndexed` does the same for a run
record, read back through the index's own `Entry`.

**The export.** `history/sessions/` and `history/state.json` are carried by
`conductor export` as the data directory's other files are (C6's `rest`), so
a restore brings the records back; `history/index.sqlite` is left out and
refused in an archive (a new `WhyDerived` row in
`internal/export/leftout.go`: "derived; rebuilt at the next start"), and
`history/owner.lock` with the other locks (`WhyLock`). The sync adapter's
allowlist (C6) is unchanged: what of history is shared across machines is
for Program item TS1 to decide, an item not yet planned.

## Out of scope

- Reading an agent's own transcripts or history files (Claude Code's
  projects directory, Codex's sessions directory, any agent's config
  directory beyond what `plans.go` opens). "Provider-aware" is the agent,
  its version, its own session id and its session recipe; a reader of a
  provider's transcript would be a decision of its own, proposed first
  (Owner decisions, 2).
- Keeping a terminal's output, scrollback or screen in a record, or what a
  person types into the terminal or a reply box (Owner decisions, 4).
- Sharing history between machines or people, and conflict rules for it:
  Program item TS1's, over CS1's sync, an item not yet planned and with no
  plan file; HS1 only reserves the `origin` column.
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
surfaces: Go (PR-1 to PR-5, PR-9 to PR-12): `make lint`, `go test -race
-count=1 -timeout 30m ./...` (the whole module), `make lint-static`; web
(PR-6, PR-7, PR-9, PR-10, PR-12's `protocol.ts`): `npm --prefix web run typecheck`, `npm --prefix
web test`, `make test-e2e` (the PR's specs alone first while iterating).
PR-1, PR-5 and PR-12 also run the Mac's row (`python3 scripts/preflight.py
--machines mac --only go-test-race`, under the preflight lock): a record's
files and modes on APFS and `gitcli` on macOS. No PR changes Python tooling,
`desktop/` or a workflow, so the scripts suite, `make desktop-test` and the
Windows rows are named in each body as not applicable. New tests that wait
do so on the mechanism (a seam or the store's own read), never on a clock
(AGENTS.md, Tests that do not flake); the journal's 5 s flush is driven
through a seam (`flushNow`) in every test that needs a write.

**Go, `internal/journal` (PR-1):**
`TestARecordIsWrittenBeforeItsFirstEntry`;
`TestARecordKeepsEvidenceEntriesOnly` (join, leave and link never kept, an
attention entry never kept, tool use counted);
`TestTwoQuestionsOfTheSameWordsAreTwoEntries` (each answer's `Ref` names
its own); `TestAnEarlierEntryHeldPastTheEndIsKept` (a gate holds the
earlier entry until `End` is written; kept in its place by `At`; one after
`EndedAt` dropped and counted); `TestEndWritesOffTheCallersGoroutine` (the
writer's disk held at a gate: `End` returns at once);
`TestAFileFoldsIntoOneRowPerPathAndOp`;
`TestADenyListedPathIsNeverWritten`; `TestAVariableValueIsNeverWritten` (the
names and the hash only; the file grepped for the value);
`TestARecordIsBoundedOldestEntriesFirst` (at 64 KiB exactly and one past);
`TestTheLargestValidLaunchFits` (64 arguments of 4 KiB: `ArgsKept` false,
the count and hash kept, no argument, the record under 64 KiB with every
other field at its bound); `TestPruneKeepsLiveAndPendingRecords` and
`TestLiveRecordsPastMaxRecords` (more live than `MaxRecords`: none pruned,
every ended one past the bound pruned); `TestRecoverMarksAnOpenRecordLost`
(a server session's: `lost`, `LostAt` its last write, `EndedAt` empty) and
`TestRecoverMarksAHostedRecordHostAway` (the same, `host away`);
`TestAnOlderChangeNeverOvertakesANewerOne` (two `Changed` calls whose
snapshots arrive older last, Codex's lowest id then a higher one and a
resumable id then not resumable: the record holds what `Current` answers,
the lowest id, resumable);
`TestBeginReopensTheSameHostsRecord` (marked `host away`, then `lost`; the
same owner reopens it with its entries and `Seq`; another owner, or a
server session's id, refused `ErrExists`); `TestRecoverSettlesAPendingResume`
(the allocated id's record there: `started`; none: `uncertain`);
`TestSeqNeverGoesBack` (a crash between
the state file and the record, by a seam); `TestForgetRefusesALiveSession`
(and a record holding a `pending` receipt, `ErrResumePending`; `ForgetAll`
keeping and counting it);
`TestANewerRecordIsReadNotRewritten`; `TestReadOnlyWithoutTheLock` (two
processes, as the ledger's lock test); `TestFilesAre0600InA0700Directory`
(unix); 32 goroutines noting into 8 records under `-race`, every entry kept
once in order of `Seq`.

**Go, `internal/api` (PR-1):** `TestEveryServerSessionGetsARecord`;
`TestWhatAPersonTypesIsNotKept` (a reply-box submission, keys typed into
the terminal and a typed answer to a question: the record file grepped for
each, none there; a line Conductor typed kept as `typed`);
`TestQuestionsAndTheirEndsComeFromTheChat` (a question with choices, one
answered by a choice, one withdrawn: ids, choices, `Ref` and outcome as the
chat had them); `TestTheChatIsKeptWithWhatWasSentToTheAgent`;
`TestATerminalAnswerIsNotAWithdrawal` (a question answered by keys typed in
the terminal: `answered_in_terminal`, no text kept; one its agent moved
past: `withdrawn`); `TestAHostedQuestionAndItsEndsReachTheRecord` (real
host `chat` messages through `HostChat`: an agent's question with its
choices, its `answered` line, a terminal-left and an agent-left
`withdrawn` line, each in the record with its ids; refused or cut: the
agent's role on a plain message, a seventh option, a label past 60 runes,
a `withdrawn` text not among the chat's reasons);
`TestTheStartCommitIsReadBeforeTheAgentRuns` (a `sessiontest.FakeRuntime`
whose `StartTerminal` commits in the folder before it returns: the record's
`startCommit` is the commit before it); `TestACrewMembersStartIsItsAttempts`;
`TestANonNativeSessionGetsNoGitReadOnTheServer` (a FakeRuntime at another
`where`: the git reader's seam never called, `Where` recorded, the record
page's words "on <where>"); `TestAHostedSessionGetsARecord` (its host's
name, user and owner, its entries through `s.hosts.OnActivity`, its chat
through `s.hosts.OnChat`, its changes through `s.hosts.OnChange`, never
`localChange`); `TestAHostedRecordBorrowsNothingFromTheServer` (agent
version, own session id, repository and commits empty with the server's
probe cache warm and the hosted folder a repository on the server's disk);
`TestAHostedRecordSurvivesAServerRestart` (a host registered with an
instance and a local id, the server restarted, the host registering again:
one record, `host away` then reopened, entries from before and after in
order, `Seq` never back) and `TestAHostedRecordLetGoByExpiryIsReopened`
(`Hub.Expire` past `DisconnectGrace`, then the same register);
`TestTheRecordSurvivesARestart` (a server ended and started again on the
same data directory; the record listed, ended);
`TestACrewAdoptedAfterLaunchIsRecorded` (a session forms a crew around
itself, `POST /api/sessions/{id}/crew`: the record's `Crew` written at
once; the session ended, the server stopped and started again; the
record's Resume answers `409 crew_member`, and a record whose adoption the
journal had not written before the stop, by a seam, is refused the same
way from the run record's member state, nothing launched in either);
`TestAHostedExitIsKeptWithItsActivityDropped` (a host's `status` activity
entry dropped at a full queue, its status message delivered: the record
ends "exited (exit 0)" from its `Info`; `Hub.Expire` after it leaves that
end, never `host away`);
`TestHistorySessionRoutesAreAdminOnly` (a share link, a session token, no
token), `TestHistorySessionsOffKeepsNoRecord`,
`TestASwitchyardKeepsNoHistory`.

**Go, `internal/history` (PR-2):**
`TestSchemaV1AndARebuildOfAnotherVersion` (cleared in place, the file's
identity unchanged), `TestADamagedIndexIsMovedAsideAndReadersReopen`;
`TestAReaderOutlivesAReindexAndASchemaChange` (a read-only connection
opened from a second process and serving searches; the owner reindexes,
then clears the index to another schema version: each later search of the
reader answers the rebuilt index, or `other_version` with no rows, never
an error);
`TestClassComesFromTheKindAlone` (a caller's class refused, every kind of
the table its class); `TestARepeatedSentenceStaysSaid` (the same body in ten
sources: ten `said` entries, `repeats` 10 in 10 anchors, none of another
class); `TestReplaceIsAllOrNothing` (a commit made to fail leaves the old
rows and FTS rows); `TestRemoveCascades`; `TestTermsIntersectAcrossColumns`,
`TestTheLastWordIsAPrefix` (at one character and more),
`TestAWordWithNoLetterOrDigitIsDropped`, `TestAPhraseIsOneTerm`, `TestEachFilter` (a
table over every filter name and its refusals),
`TestAnUnknownFilterIsAWord`; `TestOrderNeverWeighsClass` (two entries of
equal text, a `said` newer than a `checked`: the newer first);
`TestTiedEntriesPageExactlyOnce` (250 entries of one text, one score and
one time, paged by 20 and by 100: each key exactly once, in key order; a
cursor of another query `ErrCursor`; one taken before a reindex
`stale_cursor`);
`TestTheBudgetDropsTheOldestFirst` (by entries and by bytes, `Stats` saying
from when); `TestSimilarFindsTheSameCommandElsewhereAndLeavesTheChainOut`,
`TestSimilarStaysInTheRepositoryAndRanksTheLineFirst` (two repositories
whose crews run one generic argv: the other repository's passes are absent
until `scope=all`; the receipt holding the failure's line ranks first);
`TestAKeyOutlivesReplaceAndReindex`;
`TestSnippetMarksTheMatch` (with long tokens before the match, and text
holding the marks), `TestClipSnippetKeepsItsMarks`;
`TestASearchPastItsBoundsSaysSo`, `TestASearchNeverHoldsAWriteBack`,
`TestCloseWaitsForAWrite`, `TestACancelledOpenKeepsTheIndex`,
`TestAnIndexNotClosedIsCheckedBeforeItIsSearched`,
`TestCheckClearsADamagedIndex`, `TestALinkFindsItsEndByItsReference`,
`TestCloseWaitsForASearch`, `TestCloseWaitsForAClear`,
`TestCloseWaitsForACheck`, `TestCloseDuringAMoveAside`,
`TestAWriteNeverWaitsBehindSearches`, `TestOverlappingClearsOweNoCheck`,
`TestAReaderReopensBesideASearch`, `TestCloseWaitsForACancelledRollback`,
`TestACancelledCheckChangesNothing`, `TestAWriteAndAClearTakeTurns`,
`TestChecksTakeTurns`, `TestASoundCheckClearsAnEarlierCheckDamage`,
`TestAMoveAsideWaitsForTheCallsRunning`;
`BenchmarkSearch` (100,000 entries; the medians in PR-2's body).

**Go, `internal/api` and `internal/crew` (PR-3):**
`TestTheFanCallsEveryObserverInCommitOrder` and the sync adapter's tests
unchanged; `TestTheIndexHandsWhatTheEngineHands` (a managed run with fake
sessions whose check fails at attempt 1; each `handed` link equals the
attempt's own `previous.check`; `passed_after` to attempt 2's receipt);
`TestHandedAcrossAContinuation` (the run stopped after attempt 1 failed,
continued; the continuation's first attempt handed the source run's
receipt); `TestAReviewsNeedsChangesLinksTheReopenedAttempt`;
`TestPassedAfterAContinuationIndexedLater` (the source run indexed while
its continuation still runs; the continuation's pass then writes
`passed_after` from the source's failed receipt; a catch-up after a
restart keeps it); `TestACheckBodyIsTheLogsEnds` (a 1 MiB log and the
API's largest argv: the summary the argv cut to 1 KiB, the body exactly
16,384 bytes as encoded, its first 4,096 and last 12,283 bytes indexed, a
word from the middle not found, a word of the argv found through the
summary); `TestAPassedCheckIndexesItsTailOnly`;
`TestOutcomeIsTheEnginesPredicate` (exit 0 with the checkout changed:
failed; `Killed` alone after an exit 0: passed; an error with exit 0:
failed; `interrupted` kept as such; `started` and `running` not indexed
until finished); `TestVerifiedIsTheDeliverysPredicate` (`verifiedOf`
against `candidateVerified` over a table: a failed attempt whose checks
passed, no receipt, an unstable tree, a cleanup kill, and an exit-0,
stable receipt that carries an `Error`: both pass it, the delivery route's
answer unchanged, `crew.FailedOutcome` fails it, its `check` entry is
failed and no `candidate.verified` entry is indexed for its candidate);
`TestOnlyTheAdminsCommandsAreIndexed` (two actors with one command id in
one run: one entry, its ref opening the admin's receipt);
`TestAFinishedReceiptIsReadOnce` (its log read by `ledger.Check` once; the
run's later transitions read no log); `TestARunIsReadAtMostEvery2s`
(through the indexer's clock seam); `BenchmarkTransitionWhileIndexing`
(the medians and 99th percentiles in PR-3's body);
`TestARecordOnlyChangeIsSeenAfterARestart`;
`TestARunRecordByTheOtherServerIsIndexed` (a second server on the same
data directory writes a run record; after one `reconcileNow` the first's
index holds it, read through `history.Entry`, with no restart);
`TestAChangeBetweenTheMarkAndTheReadIsReadAgain` (`afterMark`);
`TestCatchUpAfterARestart` (runs made while no index existed, then a run
pruned: indexed, then removed); `TestCatchUpRunsOnTheHistoryLockAlone`
(another process holds the ledger's lock, this one the history lock: the
records, the runs from their files and the ledger's rows through its
read-only connection, all indexed at start; PR-4 adds the tickets to it);
`TestCatchUpWithNoLedger` (the ledger fails to open: the session records
and the run records indexed, `indexing` saying the ledger is unavailable;
PR-4 adds the tickets);
`TestSiblingWorktreesAreOneRepository` (a plain session in a linked
worktree and a Dispatch project at the main checkout: the session is the
project's; a crew's run in a sibling worktree found by `Similar`'s
repository scope; the session's worktree removed: still the project's,
still found); `TestAProjectChangeReassignsItsSessions` (a project created
over a record's repository, then its folder moved to another repository,
with no journal write: the session entry's project follows each change by
the hook, and again by a restart's catch-up);
`TestAnAttemptsStartCommitReachesItsSessionEntry` (the member's record
indexed before the ledger holds its attempt's start commit, then the row
written with no journal write: the entry's `commit_id` is the attempt's); `TestAPrunedRunLeavesTheIndex`;
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
log's lines for a search hold no `q`, nor a `key`);
`TestHistoryAndDispatchReadWordsAlike`; `TestAGoneKeyAnswersEntryGone`;
`TestAnEditByTheOtherServerBecomesSearchable` (`reconcileNow`);
`TestAReindexWithASecondServerSearching` (two servers on one data
directory; the second's search, its connection open throughout, answers
after the first's reindex with no restart); the tickets added to PR-3's
`TestCatchUpRunsOnTheHistoryLockAlone` and `TestCatchUpWithNoLedger`;
`TestTheCommandReaderOpensTheIndexedActorsReceipt` (the workbench token
and an identity provider's administrator each made a command of one id in
one run: the indexed admin receipt, opened by the identity provider's
administrator through `/api/history/command`, answers the admin's receipt
with its actor; `/api/runs/{run}/commands/{id}` still answers each caller
its own).

**Go, `internal/api` and `internal/cli` (PR-5):**
`TestResumeFromARecordAfterARestart` (a fake agent with a session recipe:
the new session's arguments are the recipe's resume arguments with the
recorded id, its folder and yolo choice the record's);
`TestResumeFromARecordRefuses` (a table: hosted, managed, Brakeman, no
recipe, not resumable, held by a live session, folder gone with
`ResumeNeedsCwd`, arguments not kept, a Runtime this server lacks,
variables without `withoutEnv`, a legacy run's member (`409 crew_member`,
nothing launched, the run's member state unchanged), a `lost` record
without `confirmLost` (`409 lost_unconfirmed`); each refusal settling its
receipt `failed`);
`TestARuntimeTerminalThatOutlivesTheServer` (a `sessiontest.FakeRuntime`
whose terminal keeps running when the server stops: after the restart the
record is `lost` with no `EndedAt`, its availability and record page say
its process's state is not known, Resume is refused `409
lost_unconfirmed`, and with `confirmLost` it launches);
`TestTheSeventeenthResumeIsRefusedAndTheOldestStillAnswers` (16 receipts,
refused ones among them; a 17th command id `409 resume_limit`, nothing
reserved or launched; the server restarted; the oldest id sent again
answers its first receipt and starts nothing);
`TestForgetWaitsOutAPendingResume` (the launch held at a gate after the
reservation: Forget answers `409 resume_pending`, ForgetAll keeps and
counts the record; the gate let go: the receipt settles `started` on its
record; Forget then removes it); `TestTheSameCommandIDAnswersItsReceipt` (sent again while the
new session runs and again after it has exited, for a listed session and
for a record: the first receipt each time, one session started);
`TestAPendingReceiptAfterAStop` (a stop held at a seam before the process
is made: `uncertain`, nothing claimed; a stop after the new session's
`Begin`: `started`, naming it); `TestAListedSessionAndARecordShareOneLaunch`
(`resumeLaunch`, the session route's answers and tests unchanged);
`TestANonNativeRecordResumesInItsRuntime` (a FakeRuntime at another
`where`: the new session starts there, its folder opened through
`openCwd` and lent as `DirFile` as any launch's is; the folder gone and
the recipe needing it: `400 invalid_cwd` before any `StartTerminal`; no
git read of it on the server);
`TestResumeFromARecordStartsThroughTheRuntime` (`sessiontest.FakeRuntime`
records the start); `TestTheGuideListsHistoryForTheAdminOnly` and
`TestToolsListIsUnchanged`; `cli/history_test.go`:
`TestHistorySearchPrintsTheSixFields`,
`TestHistoryResumeSendsOneCommandIDTwice`, `TestHistoryForgetAll`.

**vitest (PR-6):** `utils/history.test.ts` (the detail line's order and
omissions, every badge and that no `said` or `decided` entry has a ✓, the
link line, repeats' words, the questions' queries, availability words);
`composables/useHistory.test.ts` (the resume's command id sent twice on a
lost reply, never a second id). **vitest (PR-7):** `utils/palette.test.ts`
(`~` parsed and dropped, no other prefix changed);
`composables/useHistorySearch.test.ts` (the debounce through fake timers, an
abort, a slow earlier answer dropped, the cache, the token forgotten).

**Playwright (PR-6):** `history-record.spec.ts`: a plain stub session with
a session recipe ended, the server restarted on its data (the
`tasks-resume.spec.ts` pattern), its record page opened by its URL (no
palette row reaches it yet), Resume asked and answered, the new session's
command line holding the recipe's `--resume` and the recorded id, its
record naming the one it resumed; Forget removes the record; the run page
opened at `?task=&attempt=&step=` shows that step's log and its SHA-256;
the run page opened at `?command=` shows that command's receipt (a pause,
its actor and time); a ticket with more comments than its page's first
page holds, opened at `?comment=1`, shows that first comment marked.

**Playwright (PR-7):** `history.spec.ts`, the acceptance boundary with the
e2e stub worker: the crew of `tasks.spec.ts`, whose `fixed` check (`git
cat-file -e HEAD:stub-fixed.txt`) fails until the builder adds the file on
its second attempt, is launched and succeeds; the palette opened after the
sidebar's root is drawn (AGENTS.md: wait for the listener's component), `~
stub-fixed outcome:failed` typed (a word of git's refusal in the receipt's
log, and the filter, since attempt 2's passing receipt holds the same word
in its argv and bm25 does not fix which of the two comes first); the row
whose key is attempt 1's receipt (`check:<run>/build/1/fixed/<k>`, found by
its key or its title "fixed on build #1", never by its position) shows
Checked ✗, the six fields and "Investigated by build #2"; Enter opens the
run page at attempt 1's `fixed` step with the log's SHA-256 equal to the
step log route's header; a plain session whose chat holds the same word
(posted there and sent to its agent) shows as Conversation, never Checked;
`~` alone lists the four questions, and "What was last verified" lists
attempt 2's passing receipt, found by its key; Find this elsewhere on a
second crew's run of the same command in the same repository lists it,
found by its key. Rows are found by their keys and titles, never by a
count or a place (`stopStrays` first). `history-page.spec.ts`: the record
page of the restarted session reached from the palette's row; the History
page's chips write the query; a phone width shows one column.
`palette.spec.ts` and `palette-keys.spec.ts` stay green with the seventh
prefix. Each new spec run `--repeat-each 10` before its PR is marked
ready.

**Go and vitest (PR-12):** `internal/proto`: each new field and the new
message at its bound and one past it, refused; an old host's register
(none of the fields) accepted; `protocol.ts`'s types beside them (vitest).
`internal/hostagent`: `TestTheHostReadsItsLaunchRevisionBeforeItStarts`
(the host's own folder, before its process starts, native Runtime) and
`TestANonNativeHostReportsItsWhereAndNoGit` (`hostagent.Run` with a
`sessiontest.FakeRuntime` at another `where`: that `where` reported, the
git reader's seam never called, the record's `Where` that one).
`internal/api`: `TestAHostedAgentSessionIsCapturedFromItsHook` (a real
hook request, `POST /api/sessions/{id}/attention` with `agentSession`,
`turn` and the hosted session's notify token, through a registered host:
the record takes the id by the recipe's policy, Codex's lowest kept over a
later higher one; an id not of the pattern dropped; the hosted `Info`
untouched; a server session's capture unchanged);
`TestAHostedRecordTakesWhatItsHostReports` (`where`, version, repository
and its common directory, start and end commits, `StartFrom: host`) and
`TestAnOldHostsRecordSaysNotReported` (`Where` empty and said unknown,
never native).

**By hand (PR-8's record):** what the stub cannot show: real Claude Code and
Codex sessions recorded with their versions and own session ids (Codex's
lowest-id policy), a plain Claude Code session resumed from its record after
a restart (`--resume <id>`) and a Codex one (`resume <id>`, its folder
needed), a hosted session from `conductor host` on the Mac (its host and
user, "not reported by the host" for what an old host does not report,
"host away" once the host stops and the same record taken up again when it
comes back, no Resume from the server; PR-12's own observation adds the
agent's version, own session and launch commit as the Mac reports them), the four
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
   says `native`, and says any other `where` as the row has it. A session
   record keeps the `where` its session was started in
   (`createSessionRequest.where`, `Record.Where`); the server reads git
   (repository, branch, start and end commits) only for a native one and
   says "on <where>" otherwise; the folder a Resume needs is checked
   through `resolveCwd` and opened through `openCwd` for every `where`, as
   every launch's is, since it is the local source directory the Runtime
   is handed (`ext.TerminalSpec.DirFile`), not an observation inside it;
   and a record resumes in the Runtime it names, or is refused (`409
   runtime_gone`). A hosted record's `Where` is its host's report, empty
   (unknown, never native) from a host that reports none.
   `TestANonNativeSessionGetsNoGitReadOnTheServer` and
   `TestANonNativeRecordResumesInItsRuntime` hold it with
   `sessiontest.FakeRuntime`.
8. **`hostagent` is a launch site as much as `api`.** Honoured: PR-1
   records a hosted session on the server from what its host already
   reports and borrows nothing of the server's machine for it; PR-12 makes
   the host, the launch site, report its own Runtime, read its own launch
   revision before its process starts (native only, through its Runtime's
   `where`) and report it with its agent's version, as the server does for
   its own sessions; a hosted agent's own session is captured on the
   server, from the hook that reports it there. Resuming a hosted session stays
   the host's (`400 hosted_session`, unchanged).

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
server and hosted sessions (a hosted record's reopening), the hooks as
named in "The API's part of PR-1" (`localChange` and `s.hosts.OnChange`
through `Changed` and `Current`, the hub's sink and `chat`, the registry's
`OnRemove`), `End`, the agent's
version and the launch revision read before `StartTerminal` (native only),
the four record routes. Touches `internal/journal`, `internal/config`,
`internal/signal` (the `OnAdded` callback, `HostChat`'s questions and
their ends), `internal/gitcli` (`RepoOf`), `internal/api`,
`docs/protocol.md`; nothing in `internal/session` (every entry's source
exists on `main`). Tests: PR-1's of
`internal/journal` and `internal/api` above. About 1,300 lines. Depends on
nothing. May run beside PR-2.

**PR-2: the history index and its search.** `internal/history` whole (the
schema, the kinds table, `Replace`, `Remove`, `Mark`, `Sources` (a page of
the indexed sources and their marks, which PR-3's catch-up compares with
the stores), the bounds and the budget, `ParseQuery`, `Search`, `Similar`,
`Entry`, `Links`, `Stats`, `Check` and `Unchecked`),
`BenchmarkSearch` with its medians in the PR body. Touches
`internal/history` only. Tests: PR-2's above. About 1,400 lines. Depends on
nothing. May run beside PR-1.

**PR-3: the indexer over sessions and runs.** The commit fan-out
(`commits.go`, the sync adapter registered through it unchanged); the
indexer (dirty set, marks, the pass, backoff, catch-up, reindex route); the
session-record and run sources with every link (`session_of`, `continues`,
`launched_from`, `handed`, `passed_after`, `reviews`, `reopened`), each
finished receipt its own source, the run's composite mark, the per-run
interval and the reconcile over the ledger and the run records; `crew.FailedAtCheck`, `crew.FailingReceipt`
and `crew.FailedOutcome` exported, `verifiedOf` factored out of
`candidateVerified`; `BenchmarkTransitionWhileIndexing` with its numbers in
the PR body; the export's `WhyDerived` row and lock row. Touches
`internal/api`, `internal/crew`, `internal/export`. Tests: PR-3's above. About 1,400 lines. Depends on PR-1
and PR-2.

**PR-4: Dispatch's and Brakeman's sources, availability and the search
routes.** The ticket and brief sources, with the reconcile's Dispatch feed
and briefs' files; `historyURL`; availability;
`resumeCheck` factored out of `handleResumeSession`; `GET /api/history`,
`/api/history/entry`, `/api/history/similar`, `/api/history/command`;
`docs/protocol.md`'s History search rows. Touches `internal/api`, `docs/protocol.md`. Tests: PR-4's
above. About 1,200 lines. Depends on PR-3.

**PR-5: resume from a record, the CLI and the guide.** `POST
/api/history/sessions/{id}/resume` with its receipts and `Recover`'s
settling; `resumeLaunch` shared with `handleResumeSession`, and the
allocated session id `createSessionRequest` takes from it; `conductor
history` and its completion; the guide's admin rows. Touches
`internal/api`, `internal/journal` (the receipt's reservation and its
settling at recover), `internal/cli`, `docs/protocol.md`. Tests: PR-5's
above. About 1,100 lines. Depends on PR-4.

**PR-6: the record page and the run page's step.** `utils/history.ts`,
`useHistory`, `pages/history/sessions/[id].vue` with Resume and Forget on
PR-5's routes, the run page's `?attempt=&step=`; `history-record.spec.ts`.
No palette row yet, so nothing on `main` links to a page it does not have.
Touches `web/app/utils`, `web/app/composables`, `web/app/pages/history`,
`web/app/pages/runs`, `web/app/pages/dispatch`, `web/app/components`,
`web/e2e`. Tests: vitest and Playwright of PR-6 above, a record-only session after a restart among
them. About 1,000 lines. Depends on PR-5.

**PR-7: the palette's History and the History page.** `utils/palette.ts`'s
`~`, `useHistorySearch`, the palette's history groups and the search
mode's History group, the row's badge and third line,
`pages/history/index.vue`, Go to's History row and chord;
`history.spec.ts` and `history-page.spec.ts`. Every row it draws opens a
page PR-6 or `main` already has. Touches `web/app/utils`,
`web/app/composables`, `web/app/components`, `web/app/pages/history`,
`web/e2e`. Tests: vitest and Playwright of PR-7 above. About 1,100 lines.
Depends on PR-6.

**PR-8: the docs and the features entry.** `docs/features.md`'s HS1 entry
with each PR's checks and the by-hand record, the README's History section,
`docs/architecture.md`'s History, `docs/repository-map.md`'s rows,
`docs/dependencies.md`'s importers. Touches `docs`, `README.md`. Docs only;
no check applies but the links read. About 400 lines. Depends on PR-7.

**PR-9: DR1's decision records.** As in "Later PRs". Touches `internal/api`,
`internal/history` (the kinds `decision` and `decision.response`, their
bounds and classification rows), `web/app/utils`, `docs`. Tests: the two
kinds' rows in `TestClassComesFromTheKindAlone`, a DR1 request and its
response indexed and found by their keys, a superseded request said in its
availability, and the palette's "Rejected designs" row in vitest. About 500 lines. Depends on PR-8 and on DR1's PRs
that serve its records (`docs/plans/decisions.md`).

**PR-10: RV1's review batches and findings.** As in "Later PRs". Touches
`internal/api`, `internal/history` (the kinds `review.batch` and
`review.resolved`, their bounds and classification rows), `web/app/utils`,
`docs`. Tests: the two kinds' rows in `TestClassComesFromTheKindAlone`, a
batch and its findings indexed and found by their keys, a finding that
stays `said` beside its `review.resolved` entry, and the row's class badge
in vitest. About 500 lines. Depends on PR-9
(both change the sources file and the features entry) and on RV1's PRs that
serve batches and findings (`docs/plans/review-repair.md`); if RV1 lands
before DR1, PR-10 goes first and PR-9 follows it.

**PR-11: Brakeman reads history within its grant.** As in "Later PRs".
Touches `internal/api` (Brakeman's observe, its reference resolver's
record source and the guide), `docs`. Tests:
`TestABriefCitesARecordOnlySessionAfterARestart` (a plain session in a
repository, ended, the server restarted; a request whose grant is scoped
to that repository finds it in its history section, cites it, and the
citation resolves from the record within the scope, its read logged and
its URL the record page; a record in another repository refused as any
ref outside the grant is). About 700
lines. Depends on PR-8 and on Brakeman's PR-9 to PR-13
(`docs/plans/brakeman.md`); runs after PR-9 and PR-10 or before them, never
beside them (`internal/api`).

**PR-12: what a host reports of its sessions.** As in its section above:
the register's new fields (`where` among them), `session_info`, the
`registered` reply's `sessionInfo`, the host's reads before its process
starts through its Runtime, the hub's checks and `OnSessionInfo`, the
server's capture of a hosted agent's session from its hook
(`session.NextAgentSession`), the journal's hosted fields. Touches
`internal/proto`, `internal/hostagent`, `internal/signal`,
`internal/session` (the pure policy function), `internal/journal`
(`Journal.AgentSession` and the hosted fields), `internal/api`,
`web/app/utils/protocol.ts`, `docs/protocol.md`. Tests: PR-12's above and
the Mac's row. About 700 lines. Depends on PR-8; runs before, between or
after PR-9 to PR-11, never beside them (`internal/api`). It adds its own
paragraph and by-hand observation to the features entry; HS1's Done does
not wait on it, since the acceptance boundary is a managed run on the
server's own machine.

## Owner decisions

Dated 2026-11-05. The defaults below are this plan's, inside the twelve
articles; the owner may overturn any at the plan's review. A default the
owner overturns is rewritten in this section, dated, in the owner's words,
and every part of the plan it touches is changed with it before the PRs
are cut into issues; a PR already cut follows the plan as rewritten.

1. **No open decision of the Program's is this item's.** The ones still
   open are about how decisions are routed to people and how synced
   records conflict, which belong to the routing and sync items. HS1 needs
   neither: it is local to one machine, routes nothing, and reserves
   `origin` for team history (Program item TS1) without choosing how
   shared history merges.
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
4. **What a record keeps.** The lines Conductor typed, questions with
   their choices and how each ended (the choice's label, or withdrawn),
   the agent's reports, files touched (folded) and the session's chat;
   never what a person typed into the terminal or a reply box (it may be a
   credential, and the chat already refuses to repeat it), a variable's
   value, a terminal's output, a viewer's arrival or a link. Keeping a
   person's typed lines would be a change to `internal/session` and a
   privacy decision of its own, proposed first. The
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
   first by age; a check's log indexed by its first 4 KiB and its last 12 KiB
   less a 5-byte separator (16 KiB in all) when it failed and by its 2 KiB
   tail when it passed, its argv in the summary.
10. **Resume from a record.** Offered by the resume routes' own rules; never
    for a hosted session (its machine resumes it), a managed attempt (its
    run continues) or a Brakeman turn; the launch's variables are not kept,
    so a record launched with them resumes only with `withoutEnv`, which
    says which were left out; a launch whose arguments were too long to
    keep is not resumed at all, never with a cut list.
11. **Admin only.** The History routes take the workbench token; no attempt
    session gets a history tool; Brakeman reads history only through PR-11,
    within its grant.
12. **The later PRs.** DR1's decisions, RV1's reviews and Brakeman's reads
    are indexed by PR-9, PR-10 and PR-11 once those items serve them, and
    HS1 is Done without them: its acceptance boundary is a failure and its
    investigating session, which main's records already hold.

## Acceptance

The boundary (the Program, section 4.3, HS1): a person asks the palette
which session looked into a given failure, and the row it gives opens the
evidence itself.

**Observed how.** On the owner's machine (or the WSL box with the owner's
agents), on the open-source build with no account, after PR-8 merges:

1. a project with a Dispatch project over its repository; a managed crew
   whose work task's check fails on its first run (a script outside the
   worktree, as Yardmaster's live check used), run with real Claude Code as
   the worker and Codex as the reviewer, so attempt 1 fails the check and
   attempt 2, handed the receipt, passes; beside it a plain Claude Code
   session in the same repository asked about the same failure through its
   chat (a message posted in the session's chat and sent to its agent,
   which the record keeps; words typed into its terminal are not kept, and
   the observation does not rely on them);
2. the server restarted;
3. in the palette, `~` and a word of the failing test's output: among the
   rows (with `outcome:failed` added when the passing receipt shares the
   word, since the ranking does not fix which comes first), the failed
   receipt, found by its title ("<check> on build #1"), Checked ✗, with
   its six fields true (the source, this machine, the project's key,
   attempt 1's result commit, the time, "log kept"), "Investigated by build #2" naming Claude Code and its
   own session, and "passed at" attempt 2's commit; Enter opens the run page
   at that step's log, whose SHA-256 equals the step log route's; the plain
   session's chat line shows as Conversation, and its record opens on its
   record page after the restart, where Resume resumes the Claude Code
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

