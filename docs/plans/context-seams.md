# Context seams: what an attempt is handed about its repository, what an agent may look up, and what each attempt was given (Program item CG1, its open-source rows; issue #1034)

Status: proposed 2026-11-19, for the owner's review before any of its
code. The owner sets it to Planned by approving it; its rows (CT-1,
CT-2, CT-3a and CT-3, The PRs) are units of work, grouped into pull
requests as `AGENTS.md` (Working an issue) says at the time they are
built. This file is the row CT-0, with the one line it adds to
`docs/plans/ext.md`.

Today a context provider (C4, `ext.ContextProvider`,
`docs/plans/ext.md`) is asked once, as an attempt of a managed run is
about to start, for the blocks that attempt should read; what it
answers goes into the attempt's task files and nowhere else, and it is
told the files the agents touched. That is all it can know: it is not
told the ticket the run works, the checks the task must pass, the
commit the run started from or the commit an attempt submitted; it has
no way to read the repository at a commit by the server's own rules; an
agent cannot ask it anything once it has started; and nothing records
which blocks an attempt was given, so neither a person nor a reviewer
can say afterwards what the agent knew. These rows are the seams that
close those gaps for **any** registered context provider:

- **What an attempt is handed about its repository at its exact
  commit.** The query a provider is asked gains the run's base, the
  ticket and plan revision the run was launched from, the task's
  checks, the evidence refs and, for each input, the attempt, start
  and review it came from (CT-1). A provider is told the run's base
  when the run starts and each candidate commit as it is submitted
  (commit notes, CT-1), and reads the repository's committed tree and
  files at a commit through `ext.Host`, by full object id, under the
  server's safe git and its deny list (CT-1). Each block may carry the
  provider's mark of the record it came from (its **pack mark**), shown
  in the task file.
- **What an agent may look up while it works.** A provider may answer
  lookups (`ext.ContextLookup`): an attempt's agent calls the tool
  `context_lookup`, listed only when a provider answers lookups, and
  the server fills in which run, task, attempt and commit the question
  is about, never the agent (CT-2).
- **What the run page and a ticket show of the context each attempt
  was given.** Every block an attempt is given, at its start or by a
  lookup, is recorded against the attempt before the agent has it: a
  row in the ledger per block (its source, title, mark, digest and
  size), its text in a context file in the data directory, which
  `conductor export` carries. The run page's attempt panel and the
  ticket's candidate row show the list; a person can open a block and
  read what the agent read (CT-3, after its ledger step CT-3a).

With no context provider registered (every build of this repository),
nothing here runs: task files are byte for byte `main`'s, the tool is
not listed, no row is written and no list is drawn.

This plan says what the open-source build does and offers. It says
nothing of how a provider builds its blocks or answers its lookups,
where it keeps what it derives or what leaves the machine it runs on:
that is the business of the module that registers it.

It was written against `main` at `c3a98a6e` (2026-11-19), after reading
`ext` (`context.go`, `host.go`, `ext.go`, `registry.go`, `exttest/`
with `checks.go`, `host.go` and `fakes.go`), `internal/gitcli`
(`run.go`: `Run`, `RunToEnv`, `RunBounded`, the safe configuration;
`gitcli.go`: `isObjectID`, `Show`), `internal/crew` (`context.go`,
`task.go`'s `dispatch` and its `attempt.workspace` transaction and the
submit's `result.submitted`, `taskfile.go`'s `TaskContext`, `run.go`,
`package.go`, `crew.go`'s bounds), `internal/crew/ledger` (`ledger.go`,
schema version 13; `rows.go`), `internal/api` (`extcontext.go`,
`extensions.go`, `exthost.go`, `tasks.go`, `guide.go`,
`compare_doors.go`, `brakeman_observe.go`'s `filterTask`, `sync.go`'s
allowlist, `capabilities.go`), `internal/export` (`export.go`'s order,
`leftout.go`), `internal/hostagent/exthost.go`, `internal/cli/mcp.go`,
`internal/agents/skill.go`, `internal/brakeman/refs.go`, and in
`web/app` the run page's tasks (`components/RunTasks.vue`,
`RunAttemptSteps.vue`) and the ticket's candidates
(`components/DispatchCandidates.vue`).

**Where this starts: on `main` today.**

- C4: `ext.ContextProvider` with `NoteTouch` and `Context`;
  `ContextQuery{Run, Crew, Task, Attempt, Kind, Goal, Instructions,
  Worktree, Dir, Start, Inputs}`, `ContextInput{Task, Commit}`,
  `ContextBlock{Title, Ref, Body}` with `Valid`; the bounds
  `MaxContextBlocks` (8 a provider), `MaxContextTitle` (120),
  `MaxContextRef` (1,024), `MaxContextBody` (8 KiB), `MaxTaskContext`
  (16 KiB every provider's blocks together), `ContextWait` (5 s),
  `TouchQueue` (1,024). The engine asks once per attempt, outside its
  lock (`askContext`), never for a comparison's lane; the server asks
  every provider at once through the guard and drops, from the last,
  blocks past `MaxTaskContext` with a note in the run's log
  (`contextFor`); the blocks go into `task.json` and `task.md` as
  `crew.TaskContext{Source, Title, Ref, Body}`, cut first into
  `task-more.json` when the task file must shrink (`fitTaskFile`).
- `ext.Host`, a provider's one door, with no way to read a repository.
  Its implementations: the server's (`internal/api`'s `extHost`, also
  Brakeman's desk with `desk` set), `conductor host`'s
  (`internal/hostagent`, which answers `ErrUnsupported` to what a host
  cannot do) and `ext/exttest.FakeHost`.
- `internal/gitcli`: the one way Conductor runs git, under a safe
  configuration (no hook, file system monitor, pager, external diff,
  text conversion, filter, editor, credential helper or transport);
  `RunToEnv` with `GIT_NO_REPLACE_OBJECTS` for a blob read by the id a
  tree records; `RunBounded(ctx, dir, limit, args...)`, which keeps at
  most `limit` bytes of standard output and 4 KiB of standard error and
  ends git past the limit; every other run keeps all of git's output.
- The ledger at schema version 13 (`const Version = 13`), each version a
  barrier: a binary that writes an earlier one opens a later ledger
  read-only.
- The task routes (`internal/api/tasks.go`): `GET /api/sessions/{id}/task`
  for an attempt's session or the admin, `POST .../task/result` for the
  session's own token alone (`taskSession(w, r, true)`, the admin's
  `403 agent_only`); `GET /api/runs/{run}/tasks/{task}` (admin, and a
  Brakeman turn through `filterTask`, which today passes every field of
  an attempt through); `GET /api/runs/{run}/candidates` (admin).
- The guide's table of operations (`internal/api/guide.go`), from which
  the MCP server's `tools/list` is made; an operation is listed when the
  server allows what it `needs` (`opNeeds`), and the MCP server lists
  `act` only when the turn's guide does (`granted`).
- A comparison's doors (`compare_doors.go`): a route with no entry is
  refused to a lane's session (`409 lane`).
- The sync adapter (C6, `internal/api/sync.go`) hands a sync provider
  only the data directory's documents its allowlist matches,
  `catalog.json`, `crews/<id>.json` and `runs/<id>.json`.
- `conductor export` snapshots the ledger before it copies the data
  directory's other files (`writer.write`: records, ledger, Dispatch,
  then the rest), and leaves out every `*.tmp` (`leftOut`).

**Not on `main`, and what this plan takes from elsewhere.** Nothing
this plan needs is unmerged. Two other public plans step the ledger
too (`docs/plans/decision-policy.md`, `docs/plans/sync-seams.md`);
each step takes the next version at its merge, whatever took one
first (Migrations, below).

**Names.** A **context provider** is any provider registered in the
context slot. A **block** is one piece of context, as C4 has it. A
**pack** is the set of blocks a provider built for one attempt and
commit; its **pack mark** (`ContextBlock.Pack`) is the provider's own
name for the record a block comes from, opaque to the server, which
stores, shows and compares it and never reads it. A **commit note**
tells a provider of a run's base or of a candidate. A **lookup** is a
question an attempt's agent asks a provider while it works; a **late
pack** is the blocks a lookup answers. The **attempt's context** is
every block an attempt was given, at its start (phase `task`) or by a
lookup (phase `lookup`), as the ledger and the **context file** record
it. Conductor is the application, Yardmaster the harness that runs the
attempts, Dispatch the ticketing component, Brakeman the sidekick that
observes runs.

**The items this one meets, and who owns what they share.**

| Shared piece | Owner | This plan |
|---|---|---|
| The context seam: `ext.ContextProvider`, the query, the block, the guard, the task file's Context section | C4, `docs/plans/ext.md` | widens the query and the block, adds two optional interfaces (`ext.CommitNoter`, `ext.ContextLookup`) and three `Host` methods; adds one line to C4 |
| The task file and `fitTaskFile` | Yardmaster, `docs/plans/agent-harness.md` | writes a block's pack mark; the cut is unchanged |
| The guide and the MCP tools | `internal/api/guide.go`, `internal/cli/mcp.go` | one operation and tool, listed only when a provider answers lookups |
| The ledger | Yardmaster | one table, in a step of its own |
| What Brakeman reads of a run | `docs/plans/brakeman.md` | the attempt's context, filtered, under the class that shows the attempt |
| A run's candidates on a ticket | DS2, `docs/plans/dispatch-evidence.md` | a count per candidate and the list when opened |
| What a sync provider is handed | C6, `docs/plans/ext.md`; `docs/plans/sync-seams.md` | nothing: the context file is outside the allowlist, and a test holds it there |

## Articles it serves

The articles are the Program's (section 2.1).

1. **No paid code, licence check or plan check.** Every change is a seam
   any registered context provider uses. The only questions the server
   asks are whether a context provider is registered, whether it is
   ready, and whether it implements an optional interface; the lookup
   route answers `404 no_context_lookup` when none answers lookups, a
   registration check like `no_router`.
2. **The free build is fully useful.** With no provider, an attempt's
   task file, the tools an agent lists, the guide, the task route and
   the run page are `main`'s, byte for byte where a test can say so.
3. **Gated by hosted dependencies, never by a flag.** Nothing here is a
   flag, a setting or a tag: a block, a lookup and a recorded row exist
   only when a registered provider answers.
4. **Stop, inspect, revoke, export and required approvals stay free.**
   No code here starts, stops, approves or waits on anything but a
   provider's answer within its bound. What an attempt was given is
   recorded and shown to the operator (inspect), and `conductor export`
   carries it (export). An attempt never waits on a lookup it did not
   make, and never fails for a provider.
5. **Dependencies point one way.** `ext` stays standard library only;
   no import changes anywhere.
6. **No feature build tag.** None added.
7. **The free default is what `main` does.** No provider: no call,
   no queue, no row, no file, no list (`TestFreeBuildUnchanged`,
   `TestNoContextNoRows`).
8. **A provider that cannot answer is the free default.** A provider
   late, failing, panicking, invalid or not yet started adds no block
   at an attempt's start (as on `main`) and answers a lookup with
   `503 lookup_unavailable`, which the tool says in words; the attempt
   goes on. Nothing waits past `ContextWait`, `LookupWait` or
   `TreeWait`, and no answer is invented in a provider's place.
9. **CGO off.** The new table is in the ledger (`modernc.org/sqlite`).
10. **The repository contract.** This plan before its code; race tests
    for every new path, a refusal tested beside each success; vitest and
    Playwright for the app's rows; `docs/protocol.md` for every route
    and field. No WebSocket frame changes, so `internal/proto` and
    `web/app/utils/protocol.ts` are unchanged; the routes' types are in
    `web/app/utils/attemptContext.ts`.
11. **Names keep their meaning.** "Context" for what a provider gives;
    the app's words say "context", never a provider's product word.
12. **Execution behind one seam.** Nothing here starts a process but
    git, through `internal/gitcli`, on the server's own terms.

## Why and what happens

An agent that starts an attempt knows what its task file says. A
provider that knows the repository (which functions call which, what a
change is likely to touch, what the tests of a package are) can say
more, but only if it is told what the attempt is for and at which
commit, can read that commit by the server's rules, and can be asked
again when the agent meets something it did not expect. A person
reviewing the candidate later needs to know what the agent was told,
or the review cannot tell a wrong instruction from a wrong reading.

After these rows, on a server with a context provider registered:

- **The attempt starts with context of its commit.** Run `api-7`
  launched from ticket CND-12's plan revision 3 starts at base
  `4f2c…`. The provider was told the base when the run started, read
  the commit's tree and the files it needed through the Host, and
  answers the implementing attempt's query (base, ticket CND-12 rev 9,
  plan 3, the task's checks `unit` and `lint`, the evidence refs) with
  three blocks, each with its pack mark. The task file shows them under
  "Context", each with "Pack: p-4f2c-1" beneath its title.
- **The reviewer starts with context of the candidate.** The
  implementer submits `9ab1…`; once the submit is recorded the provider
  is told the candidate. The review attempt's query has `Start` the
  candidate, `Base` the run's base, and its input's attempt and start;
  its blocks are of the candidate.
- **The agent asks while it works.** The reviewer calls
  `context_lookup` with `{op: "references", symbol: "store.Open"}`. The
  server answers for this session's attempt alone, with the worktree's
  HEAD as the commit: twelve references, each marked `supported`,
  `inferred` or `unknown`, and the commit the answer was made from. An
  `op: "pack"` answers a late pack: its blocks are recorded against the
  attempt first, then handed to the agent.
- **The person sees what the agent was given.** The run page's attempt
  panel lists "Context: 3 at start, 2 looked up", each row its source,
  title, pack mark (with Copy) and digest; opening one shows the text
  the agent read. The ticket's candidate row says "context: 5 blocks"
  and opens the same list.
- **The provider is gone.** With the provider stopped, failing or not
  started, an attempt starts with no Context section, as in the free
  build, the run's log says nothing new, and a lookup answers "context
  is unavailable now; carry on without it". Nothing stale is shown as
  current: what the list shows is what each attempt was given when it
  was given it.

## The changes

### What a provider is handed, and what it never is

This section is a boundary, to be read adversarially at the review.

**Handed, by the query (CT-1).** What `main` hands (run, crew, task,
attempt, kind, the crew's goal, the task's instructions, the worktree's
path and working directory, the start, the inputs), and now: the run's
base commit; the ticket and plan revision the run was launched from
(key, revision, plan id and number, the package's digest, the ticket's
owner and assignees by their display names); the task's checks (each
check's id and its argv as the crew holds it, bounded); the evidence
refs, in Brakeman's ref grammar (`internal/brakeman/refs.go`), of what
the attempt rests on; the run's launcher's name when the run keeps
one; and for each input the attempt, start, verdict and findings of the
result it comes from. Every one of these the server already keeps for
the run, and none is a credential the server holds for itself (the
workbench token, a tracker's token, a session's link, the notify
token). A check's argv is the crew's as its author wrote it, and may
hold whatever the author put there; it is handed because the provider
runs in the server's process and already has the worktree's path
(Owner decisions, 2).

**Handed, by commit notes (CT-1).** The run's id, the task and attempt
of a candidate, the kind (`base` or `candidate`), the commit's full id,
when.

**Read, through the Host (CT-1).** A run's repository's committed
objects, by full object id: a commit's tree listed, a file's bytes at a
commit, whether paths are denied. **Only a provider in the context slot
reads them** (Owner decisions, 1): every other provider's Host,
Brakeman's desk's and `conductor host`'s answer `ErrUnsupported`.

**Never handed or read by anything here.** A working tree's
uncommitted files (no read takes a path on disk; only objects by id); a
ref or a revision expression (only literal ids); a path the server's
deny list refuses (left out of a listing, refused at a read, asked at
each call); a session's output, scrollback or activity words; the
ledger's rows; another run's repository than the one the request names;
any token or credential. Git runs no program of the repository's
(`internal/gitcli`'s safe configuration) and reads no replacement
object.

**From an agent to a provider (CT-2).** A lookup's `op` (one of five
words), `query`, `path`, `symbol` and `limit`, each bounded, one line,
no control character, and nothing else of the agent's: which run, task,
attempt, session, start and commit the question is about are the
server's, from the session's own attempt and its worktree.

**From a provider to an agent (C4, CT-2, CT-3).** Blocks, each `Valid`,
within C4's bounds, framed in the task file as reference material
(C4's fence); a lookup's items, each bounded. A late pack's blocks
reach the agent only after they are recorded (CT-3); until CT-3 a
lookup hands no blocks at all.

**Who may see an attempt's context (CT-3).** The operator (the admin
grant), on the task route, the body route and the run and ticket pages;
a Brakeman turn, filtered to each block's phase, source, title, mark
and digest, under the class that already shows the attempt
(`ClassRuns`), never the text. No share link, decision link, decide
principal or agent reads it, and the context file is never handed to a
sync provider.

### `ext` (CT-1)

`ext/context.go`:

```go
type ContextQuery struct {
	// ... main's fields ...
	Base       string         `json:"base,omitempty"`       // the run's base commit, full id
	Ticket     *ContextTicket `json:"ticket,omitempty"`     // the run's ticket and plan revision; nil for none
	Checks     []ContextCheck `json:"checks,omitempty"`     // the task's checks, at most MaxContextChecks
	Evidence   []string       `json:"evidence,omitempty"`   // refs, at most MaxContextEvidence of MaxContextEvidenceRef bytes
	LaunchedBy string         `json:"launchedBy,omitempty"` // at most MaxContextPerson characters
}
type ContextTicket struct {
	Key       string   `json:"key"`       // a ticket's key, KEY-n
	Rev       int64    `json:"rev"`       // the ticket's revision as launched
	Plan      string   `json:"plan"`      // the plan revision's id, PlanIDLen lower-case hex
	PlanN     int      `json:"planN"`     // its number, from 1
	Digest    string   `json:"digest"`    // the launch package's SHA-256, lower-case hex
	Owner     string   `json:"owner,omitempty"`
	Assignees []string `json:"assignees,omitempty"` // at most MaxContextAssignees
}
type ContextCheck struct {
	ID   string   `json:"id"`
	Argv []string `json:"argv,omitempty"` // at most MaxContextArgv entries of MaxContextArg bytes
	Cut  bool     `json:"cut,omitempty"`  // the argv passed a bound and is left out
}
type ContextInput struct {
	Task     string           `json:"task"`
	Commit   string           `json:"commit"`
	Attempt  int              `json:"attempt,omitempty"`  // the attempt the result is
	Start    string           `json:"start,omitempty"`    // where that attempt started
	Verdict  string           `json:"verdict,omitempty"`  // a review's: approved | needs_changes
	Findings []ContextFinding `json:"findings,omitempty"` // a review's, at most MaxContextFindings
}
type ContextFinding struct {
	Severity string `json:"severity"` // blocker | major | minor | nit
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
	Message  string `json:"message"` // at most MaxContextFinding bytes
}
type ContextBlock struct {
	// ... main's Title, Ref, Body ...
	Pack string `json:"pack,omitempty"` // the provider's mark, at most MaxContextPack bytes
}

const (
	MaxContextPack        = 128    // printable ASCII, one line
	MaxContextChecks      = 8      // a task's checks (the crew's own bound)
	MaxContextArgv        = 32     // entries of one check's argv handed
	MaxContextArg         = 512    // bytes of one entry handed
	MaxContextEvidence    = 32
	MaxContextEvidenceRef = 512    // Brakeman's MaxRef
	MaxContextAssignees   = 8      // Dispatch's MaxAssignees
	MaxContextPerson      = 60     // characters, Dispatch's MaxPersonName
	MaxContextFindings    = 50     // a result's (crew's maxFindings)
	MaxContextFinding     = 1000   // bytes of a finding's message
	CommitQueue           = 256    // a provider's queue of commit notes, the oldest dropped
)

// CommitNoter is an optional interface of a context provider: told of a
// run's base as the run starts and of each candidate as its submit is
// recorded. Called in order, from one goroutine of the provider's own;
// it must return within DeliveryWait and never blocks the engine.
type CommitNoter interface {
	NoteCommit(ctx context.Context, n CommitNote)
}
type CommitNote struct {
	Run     string    `json:"run"`
	Task    string    `json:"task,omitempty"`    // a candidate's
	Attempt int       `json:"attempt,omitempty"` // a candidate's
	Kind    string    `json:"kind"`              // base | candidate
	Commit  string    `json:"commit"`            // full id, 40 or 64 lower-case hex
	At      time.Time `json:"at"`
}
```

`ContextQuery.Valid` (new) checks every field against its bound, as the
server builds it; `ContextBlock.Valid` checks `Pack` (empty, or 1 to
`MaxContextPack` bytes of printable ASCII with no space at either end);
`CommitNote.Valid` its kind and commit. A field a provider built against
an earlier tag does not know is ignored by its decoder, and one it does
not set is the zero value: a provider of C4 works unchanged.

`ext/host.go`, three methods on `Host`:

```go
// ReadTree lists the committed tree of r.Commit in the repository of
// the run r.Run, recursively, files, links and submodule entries, by
// full path; with Roots, the history's root commits too.
ReadTree(ctx context.Context, r TreeRequest) (Tree, error)
// ReadBlob answers the bytes of the file r.Path holds at r.Commit, at
// most MaxBlob of them; a link's target as git keeps it, never followed.
ReadBlob(ctx context.Context, r BlobRequest) ([]byte, error)
// Denied answers, for each path, whether the server's deny list refuses
// it now, under the run's repository; no git runs and no file is read.
Denied(ctx context.Context, r DeniedRequest) ([]bool, error)

type TreeRequest struct {
	Run    string `json:"run"`
	Commit string `json:"commit"` // a full commit id, never a ref or an expression
	Roots  bool   `json:"roots,omitempty"`
}
type Tree struct {
	Commit    string      `json:"commit"`
	ID        string      `json:"id"`              // the root tree's object id
	Roots     []string    `json:"roots,omitempty"` // at most MaxTreeRoots, sorted
	Shallow   bool        `json:"shallow,omitempty"`
	Entries   []TreeEntry `json:"entries"`
	Denied    int         `json:"denied,omitempty"` // entries the deny list refused, left out
	Truncated bool        `json:"truncated,omitempty"`
}
type TreeEntry struct {
	Path   string `json:"path"`   // slash-separated from the top, at most MaxTreePath bytes
	Mode   string `json:"mode"`   // 100644 | 100755 | 120000 | 160000
	Kind   string `json:"kind"`   // file | link | submodule
	Object string `json:"object"` // the blob's id; a submodule's commit id
	Size   int64  `json:"size"`   // bytes; -1 for a submodule
}
type BlobRequest struct {
	Run    string `json:"run"`
	Commit string `json:"commit"`
	Path   string `json:"path"`
}
type DeniedRequest struct {
	Run   string   `json:"run"`
	Paths []string `json:"paths"` // at most MaxDeniedPaths
}

const (
	MaxTreeEntries = 100_000
	MaxTreeBytes   = 64 << 20 // git's listing, as read
	MaxTreePath    = 4096
	MaxTreeRoots   = 16
	MaxBlob        = 1 << 20
	MaxDeniedPaths = 1000
	TreeWait       = 60 * time.Second
)
```

Each answers `ErrNotFound` for a run the server and its ledger do not
hold, a repository not readable here now, a commit the repository does
not hold, and (`ReadBlob`) a path the commit has no file at;
`ErrRefused` with the words why for a request past its bounds, a commit
id of another shape, a path with an empty, `.` or `..` part, a leading
`/` or a NUL, a denied path, and a blob past `MaxBlob` ("too large",
refused before it is read); `ErrUnsupported` on every Host but a
context provider's (Owner decisions, 1); `context.Canceled` once the
provider's try is given up, as every Host call.

### `ext` (CT-2)

```go
// ContextLookup is an optional interface of a context provider: an
// attempt's agent asks it while it works. Safe for concurrent use.
type ContextLookup interface {
	Lookup(ctx context.Context, r LookupRequest) (LookupAnswer, error)
}
type LookupRequest struct {
	Op     string `json:"op"`               // symbol | references | dependencies | impact | pack
	Query  string `json:"query,omitempty"`  // at most MaxLookupQuery; for pack, a pack mark or empty for the attempt's own
	Path   string `json:"path,omitempty"`   // at most MaxLookupPath, relative to the repository's top
	Symbol string `json:"symbol,omitempty"` // at most MaxLookupSymbol
	Limit  int    `json:"limit"`            // 1 to MaxLookupLimit
	// The server's, never the agent's:
	Run     string `json:"run"`
	Task    string `json:"task"`
	Attempt int    `json:"attempt"`
	Kind    string `json:"kind"`    // work | review
	Session string `json:"session"`
	Start   string `json:"start"`   // the attempt's start
	Base    string `json:"base"`    // the run's base
	Head    string `json:"head,omitempty"` // the worktree's HEAD, "" when it could not be read
}
type LookupAnswer struct {
	Items    []LookupItem   `json:"items,omitempty"`  // at most MaxLookupItems
	Blocks   []ContextBlock `json:"blocks,omitempty"` // op pack: at most MaxLookupBlocks, each with its Pack
	Coverage LookupCoverage `json:"coverage"`
	More     bool           `json:"more,omitempty"`
	Note     string         `json:"note,omitempty"` // at most MaxLookupNote, one line
}
type LookupItem struct {
	Kind     string `json:"kind"`               // symbol | file | test | package
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
	Symbol   string `json:"symbol,omitempty"`
	Relation string `json:"relation,omitempty"` // calls | called_by | imports | imported_by | tests | changes
	Mark     string `json:"mark"`               // supported | inferred | unknown
	Why      string `json:"why,omitempty"`      // at most MaxLookupWhy, one line
	Excerpt  string `json:"excerpt,omitempty"`  // at most MaxLookupExcerpt
}
type LookupCoverage struct {
	Commit   string    `json:"commit"`             // the commit the answer was made from, full id
	Snapshot string    `json:"snapshot,omitempty"` // the provider's own name for what it read, at most MaxContextPack
	Files    int       `json:"files"`
	Excluded int       `json:"excluded,omitempty"`
	Unknown  int       `json:"unknown,omitempty"`
	Partial  bool      `json:"partial,omitempty"`
	At       time.Time `json:"at,omitzero"` // when what it answered from was read
}

const (
	MaxLookupQuery   = 200
	MaxLookupPath    = 1024
	MaxLookupSymbol  = 512
	MaxLookupLimit   = 200
	MaxLookupItems   = 200
	MaxLookupWhy     = 200
	MaxLookupExcerpt = 2 << 10
	MaxLookupNote    = 300
	MaxLookupBlocks  = 8
	MaxLookupAnswer  = 64 << 10 // the answer as encoded
	LookupWait       = 3 * time.Second
)
```

`LookupRequest.Valid` and `LookupAnswer.Valid` check every bound, every
word's set, `Coverage.Commit` a full id, each block `Valid` and, in an
answer, carrying a `Pack`; the answer as encoded at most
`MaxLookupAnswer`. An item's `Mark` is how sure the provider is of the
relation: `supported` (it read the relation in the code), `inferred`
(it derived it), `unknown` (it could not tell). The server passes each
mark through and adds none.

### `internal/gitcli` (CT-1)

`RunEach(ctx, dir string, env map[string]string, lim Limits, each
func(record []byte) bool, args ...string) (truncated bool, err error)`,
beside `RunBounded`, whose output is held whole up to its limit: git's
standard output is read from a pipe, split on NUL, and handed record by
record to `each`, which keeps what it takes and answers false when it
has all it may take; standard error is kept to its first 4 KiB, as
`RunBounded` keeps it. When `each` answers false, or the output passes
`lim.Out` bytes, the run's own context is cancelled, which ends git
(`exec.CommandContext`), the pipe is drained to its end, and the run
answers `truncated`. No more than one record and 4 KiB of standard
error are ever held by `RunEach` itself. `env` is set beside the safe
environment, as `RunToEnv` sets it.

The Host's reads run git only through `RunEach`, `RunBounded` and
`RunToEnv`, with `GIT_NO_REPLACE_OBJECTS=1` in the environment, the
global option `--no-replace-objects`, and `GIT_LITERAL_PATHSPECS=1`:

- `ReadTree`: `rev-parse --verify --quiet <commit>^{commit}` (the id
  must come back equal to itself: an object that is not a commit is
  `ErrNotFound`); `ls-tree -r -z --long --full-tree <commit>` through
  `RunEach` with `Out` `MaxTreeBytes`, `each` stopping at
  `MaxTreeEntries` entries kept; a path the deny list refuses is counted
  in `Denied` and not kept. `Truncated` is set by either bound.
  `rev-parse --is-shallow-repository` and, with `Roots`, `rev-list
  --max-parents=0 <commit>` through `RunBounded` with 4 KiB; the roots
  are cached by the server per repository and commit, at most 256
  entries, the oldest dropped.
- `ReadBlob`: the entry first, `ls-tree --long --full-tree <commit> --
  <path>` through `RunBounded` (4 KiB): no entry is `ErrNotFound`, a
  submodule entry is refused, a size past `MaxBlob` is refused before
  any blob is read; then `cat-file blob <the entry's blob id>` through
  `RunToEnv` into a writer that keeps at most `MaxBlob` + 1 bytes, one
  past it refused.
- Every read is bounded by `TreeWait` and by the try's life (`bound`,
  as `Launch` is).

The run's repository is found as the diff route finds it: the crew's
working directory as launched, through the allowed roots
(`docs/protocol.md`, the diff route); a run whose repository is not on
this server answers `ErrNotFound`.

### `internal/crew`, `internal/api`: the query, the commit notes, the reads (CT-1)

- `internal/crew/context.go`: `contextQuery` fills `Base` (`r.base`),
  `Ticket` (from the run's launch package, `r.pkg`: the ticket's key and
  revision, the plan's id, number and the package's digest, the
  ticket's owner and assignees), `Checks` (the task's, as the crew
  holds them; an argv past `MaxContextArgv` entries or with an entry
  past `MaxContextArg` bytes is left out, `Cut` set), `Evidence` (the
  input candidates' `candidate:` refs, their receipts' `check:` refs,
  `ticket:` and `plan:` of the package, at most `MaxContextEvidence`,
  in that order), `LaunchedBy` (the package's `launch.by`, else the
  origin's provider name, else none) and each input's attempt, start,
  verdict and findings (from the task's result). The query is built
  under the engine's lock, as today, and checked by `ContextQuery.Valid`
  in the test that builds the largest one.
- `internal/crew/taskfile.go`: `TaskContext` gains `Pack string
  json:"pack,omitempty"`; `taskMarkdown` writes `Pack: <mark>` under
  the block's title when it has one; `TaskContextBytes` counts it. A
  block with no mark is written byte for byte as today.
- `internal/crew`: `Engine.SetCommitNotes(func(ext.CommitNote))`, a
  function that answers nothing until it is set. The engine calls it
  after the commit of a run's first transaction (`base`, a continuation's
  too: a provider takes a repeat) and after the commit of an attempt's
  `result.submitted` of a work task (`candidate`), outside its lock,
  never waiting.
- `internal/api/extcontext.go`: `contextFor` keeps a block's `Pack`;
  `wireContext` sets the commit-note function when a context provider
  implements `ext.CommitNoter`; each such provider gets an `extQueue` of
  `ext.CommitQueue` (the oldest dropped), delivered on its own goroutine
  through `extDeliver`, as touches are.
- `internal/api/exthost.go` (and a file of its own, `exttree.go`): the
  three Host methods, answering a context provider's Host alone. An
  `extHost` knows its provider's name; it answers when that name holds
  the context slot.
- `internal/hostagent/exthost.go`: the three methods, `ErrUnsupported`.
- `ext/exttest`: `FakeHost.ReadTree`, `ReadBlob` and `Denied` over an
  `fs.FS` per commit (`FakeHost.SetTree(run, commit, fsys)`) and a deny
  list the test changes (`FakeHost.SetDenied(func(path string) bool)`),
  with the real Host's refusals; the `Context` check also checks that a
  block with a `Pack` is valid and that `NoteCommit` (when implemented)
  returns within `DeliveryWait` with the network blocked; a new check,
  `Tree`, that a provider's reads of a fake tree stay inside it.

### `internal/api`, `internal/cli`, `internal/agents`: the lookup (CT-2)

**The route** (a boundary): `POST /api/sessions/{id}/context/lookup`,
body `{op, query?, path?, symbol?, limit?, source?}` at most 4 KiB,
decoded strictly (`DisallowUnknownFields`), `source` a provider's name
to ask (the first in registration order that answers lookups when
absent). Only the session's own token: `taskSession(w, r, true)`, so
the workbench token is `403 agent_only` and a hosted or unknown
session is refused as on the other task routes. The server reads the
session's attempt from the engine (`Engine.LookupScope(session)
(crew.LookupScope, error)`: run, task, attempt, kind, start, base,
worktree; `crew.ErrNotAnAttempt`, `crew.ErrNotRunning`), reads the
worktree's HEAD (`gitcli.Run(ctx, worktree, "rev-parse", "--verify",
"--quiet", "HEAD^{commit}")` within 2 s; "" when it cannot), fills the
request's run, task, attempt, kind, session, start, base and head, and
asks the provider through the guard within `LookupWait`, the answer
checked by `LookupAnswer.Valid`.

Answers: `200 {source, items, coverage, more, note?}` (CT-3 adds
`blocks`); `404 not_found` (no such session); `404 not_an_attempt`;
`409 not_running`; `409 lane` for a comparison's lane (no entry in
`compareDoors`: a lane asks no context, as on `main`); `404
no_context_lookup` when no registered provider answers lookups, or
`source` names none that does; `400 invalid_request` (the body past its
bound or not one, an op not one of the five, a field past its bound,
`limit` outside 1 to 200); `429 lookup_busy` when the session has two
lookups in flight (`maxLookupsInFlight`, 2); `503 lookup_unavailable`
when the provider is not started, late, failing, panicking or
answers invalid, with the words "context is unavailable now; carry on
without it". The reply is at most 64 KiB. Until CT-3 the server hands no
block: an answer's `Blocks` are dropped, so no agent is given a block
that is not recorded.

**The guide and the tool.** `guide.go`'s table gains `context_lookup`
(scope `task`, `POST`, `agentOnly`, `Bounds` body 4 KiB and reply 64
KiB) with a new need, `needContextLookup`: listed only when a context
provider that implements `ext.ContextLookup` is registered and ready.
`internal/cli/mcp.go` handles the tool and lists it only when the
session's guide does (`lookupListed`, read once at `tools/list` as
`granted` reads the turn's grants: a guide that cannot be read lists no
lookup), and only for an attempt's session (`CONDUCTOR_TASK` set). The
tool's text is the items one a line (`path:line symbol relation
(mark): why`), then the coverage ("from commit 9ab1c2d, 412 files, 3
excluded, partial"), then `more` and the note; a refusal its words. The
CLI form `conductor task lookup --op OP [--query Q] [--path P] [--symbol
S] [--limit N]` prints the same. `internal/agents/skill.go` names the
tool as optional: "when context_lookup is listed, ask it about a symbol,
its references, dependencies or the impact of a change; its answer
marks what it is sure of".

With no provider the guide, `tools/list` (`tools-list-main.json`) and
the skill's list of tools are unchanged.

### `internal/crew/ledger` (the step CT-3a)

The next schema version, a barrier, with one table and nothing that
reads or writes it:

```sql
CREATE TABLE attempt_context (
  run_id  TEXT    NOT NULL,
  task_id TEXT    NOT NULL,
  n       INTEGER NOT NULL,
  seq     INTEGER NOT NULL,   -- from 1, per attempt, across both phases
  phase   TEXT    NOT NULL CHECK (phase IN ('task', 'lookup')),
  source  TEXT    NOT NULL,   -- the provider's name
  title   TEXT    NOT NULL,
  ref     TEXT    NOT NULL DEFAULT '',
  pack    TEXT    NOT NULL DEFAULT '',
  sha256  TEXT    NOT NULL,   -- of the block's body, lower-case hex
  bytes   INTEGER NOT NULL,   -- the body's
  at      TEXT    NOT NULL,   -- RFC 3339, UTC
  PRIMARY KEY (run_id, task_id, n, seq)
);
```

No attempt before this version was given a recorded block, so a
version 13 database's rows are all true as they are: nothing is
derived. A binary of the version before opens this one read-only.

### `internal/crew`, `internal/api`: the attempt's context recorded (CT-3)

- **The rows** (`ledger.Tx.AddAttemptContext(rows []AttemptContextRow)`,
  `Ledger.AttemptContext(ctx, run, task) (map[int][]AttemptContextRow,
  error)`): one row per block, its body's SHA-256 and size; never the
  body. An attempt holds at most 128 task rows (`MaxContextBlocks` for
  each of at most 16 providers, `ext.MaxPerList`) and 64 lookup rows
  (`maxLookupContext`), at most 8 of them from one answer.
- **The context file**: `runs/context/<run>/<task>.<n>.json` in the data
  directory, written through the data directory's `os.Root`, one JSON
  object `{v: 1, blocks: [{seq, phase, source, title, ref?, pack?,
  body}]}`. The bodies together are at most 528 KiB (`MaxTaskContext`
  and 64 × `MaxContextBody`); the file as encoded at most 1,120 KiB
  (`maxContextFile`: every body at its largest with every byte escaped,
  and every other field at its largest; `TestContextFileBound`). A
  write is `<task>.<n>.json.tmp`, synced, renamed over the file, the
  folder synced; `conductor export` leaves `*.tmp` out (`leftOut`), and
  `restore` refuses one. The readers of run records list only
  `runs/<id>.json` files (`store.ListDocs`, `export.isRecord`, the sync
  adapter's allowlist), so the `runs/context/` folder is passed over by
  each (`TestRunRecordsIgnoreTheContextFolder`).
- **One writer per attempt.** Every write of an attempt's context runs
  from its read of the file to its commit under that attempt's lock in
  the engine (a mutex per attempt; one server holds the data directory,
  by the ledger's owner lock): read the file; keep the entries whose
  `seq` the ledger holds, dropping a body with no row (a crash between
  the rename and the commit); add the new blocks at the next `seq`s;
  write, sync and rename; then commit the rows. A row never names a
  body the file lacks, and a body with no row is never shown.
- **At the start (phase `task`).** In `dispatch`, after the task files
  are written: the blocks the attempt's task files hold (those
  `fitTaskFile` moved to `task-more.json` included) are written to the
  context file, and their rows are added by the `attempt.workspace`
  transaction (`tx.more`), so the attempt's context is in the ledger
  before its session starts. A file that cannot be written fails the
  attempt as a ledger that cannot record its worktree does (stage
  `worktree`), and its session never starts.
- **A late pack (phase `lookup`).** The lookup route hands an answer's
  blocks to `Engine.RecordLookupContext(ctx, run, task, n, source,
  blocks) error` before it answers: the same writer, the rows in a
  transaction of their own. The answer carries the blocks only when
  they were recorded; when the write or the commit fails, the attempt
  has closed, or the attempt's 64 lookup rows would be passed, the
  answer carries none and its note says "this context could not be
  recorded, so it is not given", and nothing is recorded.
- **The task route.** `GET /api/runs/{run}/tasks/{task}` gains, per
  attempt, `context: [{seq, phase, source, title, ref?, pack?, sha256,
  bytes, at}]` from the ledger (absent for an attempt with none), for a
  run the engine holds and one read from the ledger alike.
- **A block's text.** `GET /api/runs/{run}/tasks/{task}/attempts/{n}/context/{seq}`
  (admin): `200 text/plain` the body, at most 8 KiB, its SHA-256 in
  `X-Conductor-Context-SHA256` (exposed to a dev origin's script);
  `404 not_found` for a row the ledger has not, and for a body the file
  lacks or whose digest is not its row's (logged, never shown); `400
  invalid_request` for an id or number not shaped as one. It has no
  entry in Brakeman's observe list, so a turn is refused it.
- **Brakeman.** `filterTask` maps each attempt's `context` to `{phase,
  source, title, pack, sha256}` under `ClassRuns`.
- **The candidates.** `GET /api/runs/{run}/candidates` gains, per
  candidate, `context` (its attempt's row count, absent when 0).

### `web/app` (CT-3)

- `utils/attemptContext.ts`: the types (`AttemptContextRow`,
  `LookupReply`), the words ("at start", "looked up"; "Context: 3 at
  start, 2 looked up"), a digest shortened to 12 characters, the
  body's read.
- `components/AttemptContext.vue`: a list, one row per block: its
  phase, source, title, the pack mark with a Copy `IconButton`, the
  short digest and its size; a row opens the block's text in a
  slideover (`MarkdownPreview` is not used: the text is shown as text,
  monospace, as the agent read it). Nuxt UI components and the brand
  palette; no status colour (the list states facts, not a status);
  one column on a phone.
- `components/RunTasks.vue`: the selected task's attempt panel shows
  the list under its receipts when the attempt has rows, nothing
  otherwise.
- `components/DispatchCandidates.vue`: a candidate with `context`
  says "context: N blocks"; opening it reads the task route (as a
  review's findings are read) and shows the list.

### Protocol

No WebSocket frame changes. `docs/protocol.md` gains the lookup route
(CT-2), the body route, the task route's `context` and the candidates'
`context` (CT-3), each with its bounds; the guide's operation is in the
guide's section.

### Docs

`docs/plans/ext.md`, C4: one line naming this plan (CT-0).
`docs/protocol.md` as above. `docs/features.md` (CT-3): what an attempt
shows of its context, the lookup tool, the commit notes and the reads a
provider has, with what was verified. `docs/repository-map.md`: the
context file in the data directory's map (CT-3).

## Events, orderings and invariants

**I1. Committed objects only, by id.** Every read takes a full object
id and reads the object that id names, with replacement objects off;
no read names a ref, takes a revision expression or opens a working
tree. `TestReadTreeRefusesARevisionExpression`,
`TestReadTreeIgnoresReplaceObjects`.

**I2. No program of the repository's runs.** Every git of the reads
runs under `internal/gitcli`'s safe configuration.
`TestReadTreeRunsNoRepositoryProgram`.

**I3. The deny list as it stands at the call.** A listing leaves out,
and a read refuses, a path the deny list refuses when the call is made;
`Denied` asks the list again each time. `TestDeniedFollowsTheListAsItChanges`.

**I4. Bounded before held.** No read holds more than its bound: a
listing stops at `MaxTreeEntries` entries or `MaxTreeBytes` read, a
blob past `MaxBlob` is refused before it is read, git's standard error
is kept to 4 KiB. `TestReadTreeStopsAtItsBounds`,
`TestReadBlobRefusesLargeBeforeReading`, `TestGitStderrBounded`.

**I5. A given-up try reads nothing.** A Host whose try has ended
answers `context.Canceled` and ends a read in flight.
`TestReadsEndWithTheTry`.

**I6. A note never waits.** The engine hands a commit note to a queue
after its commit and goes on; a provider that never returns from
`NoteCommit` loses the oldest notes past `CommitQueue` and delays
nothing. `TestCommitNotesQueuedNeverBlock`.

**I7. The lookup is about the asker's own attempt.** Run, task,
attempt, kind, session, start, base and head are the server's; a body
that names any of them is refused as unknown fields.
`TestLookupFillsTheAttemptNotTheBody`, `TestLookupFillsHeadFromTheWorktree`.

**I8. Recorded before it is given.** No block reaches an agent whose
row and body are not committed: at the start, the rows commit in the
`attempt.workspace` transaction before the session starts; a late
pack's commit before the route answers. A block that cannot be
recorded is not given. `TestAttemptContextRecordedBeforeTheAgentStarts`,
`TestLookupContextFullAnswersNoBlocks`.

**I9. One writer per attempt.** Two writes of one attempt's context
are taken one after the other; each keeps every entry committed before
it. `TestConcurrentLookupsKeepEveryBlock`.

**I10. A row has its body; an orphan body is never shown.**
`TestCrashBetweenContextFileAndRows`.

**I11. The text stays on the machine's own records.** A block's text is
never in the ledger, never in the run's log, never handed to a sync
provider, and leaves the data directory only in an export.
`TestContextFileNeverHandedToSync`, `TestDeliveredContextExported`.

**I12. The export holds every row's body.** The export snapshots the
ledger before it copies the files, and a file only gains entries a
committed row names, so an archive's rows each have their body.
`TestDeliveredContextExported`.

**I13. Brakeman sees the shape, never the text.**
`TestBrakemanTaskReadCarriesContextAsFiltered`,
`TestBrakemanCannotReadABlock`.

**I14. The free build is `main`.** `TestFreeBuildUnchanged`,
`TestToolListedOnlyWithProvider`, `TestNoContextNoRows`.

### Migrations

The step CT-3a is the ledger's next version at its merge (the ledger is
at 13 on `main` as this is written; `docs/plans/decision-policy.md` and
`docs/plans/sync-seams.md` each plan a step too, and whichever merges
first takes the next number). It adds one table, derives nothing, and
is a barrier: a binary of the version before opens the ledger
read-only. It ships alone, right before the pull request that holds
CT-3, so that it can be reverted alone.

## Out of scope

- **How a provider builds a block or answers a lookup**: what it
  extracts, keeps, indexes, ranks, sends or withholds. The seams say
  what it is handed and how its answers are bounded, recorded and
  shown.
- **Uncommitted work.** No read of a working tree's files, an index or
  a stash: a provider reads committed objects; an agent's own changes
  reach it as a candidate once submitted.
- **A project's tree outside a run.** Reads name a run; a read for a
  Dispatch project with no run is a later row of whatever plan needs it.
- **Decisions and reviews in the query.** DR1's records and RV1's
  reviews named in `ContextQuery` are a later row; nothing here blocks
  it.
- **A person's lookup from the app's screens.** The tool is an agent's.
- **A lookup by a hosted attempt** (`conductor host`): its task routes
  are refused today and this one is too.
- **Removing a run's recorded context.** The ledger keeps runs; the
  context file is kept as long as its rows.
- **Telling a provider to forget a repository.** A later control,
  shown only when a provider offers it.

## Tests per surface

The checks of `docs/checks.md`: `make lint`, `make lint-static`, `go
test -race -count=1 -timeout 30m ./...`; for CT-3 the app's lint, `npm
--prefix web run typecheck`, `typecheck:e2e`, `npm --prefix web test`
and the specs it adds, with `make web-build build-go` first. Every test
of a boundary part (the reads, the lookup route, the recording, who
sees), of the step and of a refusal is proved able to fail.

- CT-0: docs only; no check applies.
- `ext` (CT-1): `TestContextQueryValid` (every new bound just inside and
  just past), `TestContextBlockPackValid`, `TestCommitNoteValid`,
  `TestTreeRequestValid` (a ref, `HEAD`, an abbreviated id, an id with
  upper case, `<id>^`, `<id>:path`, each refused).
- `ext` (CT-2): `TestLookupRequestValid`, `TestLookupAnswerValid`
  (each bound, a block without a mark, the encoded answer one byte past
  64 KiB).
- `internal/gitcli` (CT-1): `TestRunEachStopsWhenTold` (the child
  ended, the pipe drained), `TestRunEachStopsAtItsByteBound`,
  `TestGitStderrBounded` (a git that writes past 4 KiB of standard error,
  through the test seam for the binary: 4 KiB kept).
- `internal/api`, `internal/crew` (CT-1): `TestQueryCarriesTicketBaseAndEvidence`,
  `TestQueryCutsALongArgv`, `TestTaskFileShowsThePackMark`,
  `TestCommitNotesQueuedNeverBlock`, `TestBaseAndCandidateNoted` (a run
  started and continued: two `base` notes; a work attempt submitted: one
  `candidate`; a review submitted: none),
  `TestReadTreeIsTheCommitsTree`, `TestReadTreeRefusesARevisionExpression`,
  `TestReadTreeRunsNoRepositoryProgram` (a repository whose
  configuration names a filter, a hook and a file system monitor, each
  a program that writes a mark: no mark), `TestReadTreeIgnoresReplaceObjects`
  (a `refs/replace/` entry for the commit and for one of its blobs: the
  originals read), `TestReadTreeReportsShallow`,
  `TestReadTreeStopsAtItsBounds` (a tree of `MaxTreeEntries` + 1
  entries, and one whose listing passes `MaxTreeBytes` in fewer: each
  `Truncated`, git ended, the bytes held within the bound),
  `TestReadTreeLeavesDeniedPathsOut`, `TestReadBlobRefusesDeniedAndOutsidePaths`,
  `TestReadBlobRefusesLargeBeforeReading` (a 2 MiB blob: refused, no
  `cat-file` run), `TestReadBlobGivesALinksTarget`,
  `TestDeniedFollowsTheListAsItChanges`, `TestReadsEndWithTheTry`,
  `TestReadsOnlyForTheContextSlot` (a router's Host, the desk's and the
  host agent's: `ErrUnsupported`), `TestFreeBuildUnchanged` (no
  provider: task files byte for byte `main`'s, no queue, no git run).
- `ext/exttest` (CT-1, CT-2): the fake's reads against the real Host's
  refusals (`TestFakeTreeRefusesAsTheServer`), the `Context` check's new
  parts, the `Tree` check, `TestLookupCheckBlocksTheNetwork` (a provider
  that fetches on the lookup's path fails the check).
- `internal/api`, `internal/cli`, `internal/agents` (CT-2):
  `TestLookupRefusedWithoutProvider`, `TestLookupRefusedToTheAdmin`,
  `TestLookupRefusedToALane`, `TestLookupFillsTheAttemptNotTheBody`,
  `TestLookupFillsHeadFromTheWorktree`, `TestLookupAnswerBounded`,
  `TestLookupUnavailableSaysSo` (late, failing, panicking, invalid, not
  started: `503` and its words), `TestLookupBusyPastTwo`,
  `TestLookupHandsNoBlocksYet`, `TestToolListedOnlyWithProvider` (the
  guide and `tools/list` with none, with one that does not implement
  lookups and with one that does; `tools-list-main.json` unchanged),
  `TestLookupToolText`.
- `internal/crew/ledger`, the step CT-3a:
  `TestAttemptContextMigrationFreshAndAgain`,
  `TestOlderBinaryOpensAnAttemptContextLedgerReadOnly`,
  `TestAttemptContextMigrationKeepsEveryRow`.
- `internal/crew`, `internal/api` (CT-3):
  `TestAttemptContextRecordedBeforeTheAgentStarts` (the launcher's
  start held at a seam: the rows and the file are there),
  `TestAttemptContextInTheWorkspaceTransaction`,
  `TestCrashAroundTheWorkspaceTransaction` (stopped before it: no rows,
  and the continuation's rules; after it: the rows kept with the
  attempt), `TestContextFileHoldsTheMovedBlocks` (blocks `fitTaskFile`
  moved to `task-more.json`: in the file), `TestLatePackRecordedAgainstTheAttempt`,
  `TestConcurrentLookupsKeepEveryBlock` (two lookups of one attempt,
  the first held at a seam between its read of the file and its
  rename: both answers' blocks in the file and the ledger),
  `TestCrashBetweenContextFileAndRows` (the file renamed, the process
  stopped before the commit: the next write drops the orphan, nothing
  shows it, every committed row keeps its body),
  `TestLookupContextFullAnswersNoBlocks` (the 65th lookup block:
  none given, the note, nothing recorded), `TestLookupAfterTheAttemptClosed`,
  `TestContextFileBound`, `TestDeliveredContextExported` (a task's
  blocks and a late pack's, `conductor export`, a restore into an empty
  data directory: every block there, its SHA-256 its row's),
  `TestContextFileNeverHandedToSync` (a registered fake sync provider
  through a replay and a commit: no document under `runs/context/`),
  `TestRunRecordsIgnoreTheContextFolder`, `TestBlockRouteChecksTheDigest`, `TestBrakemanTaskReadCarriesContextAsFiltered`,
  `TestBrakemanCannotReadABlock`, `TestCandidatesCountContext`,
  `TestNoContextNoRows`.
- `web/app` (CT-3): vitest `attemptContext.test.ts` (the words, the
  counts, the short digest, a row with no mark, a list empty), and
  Playwright `attempt-context.spec.ts`: a run of the seeded day with
  the task route's reply stubbed to carry a context list (and the body
  route a block's text): the list drawn in the attempt panel and on the
  ticket's candidate, measured to fit at 390 × 844 and on a desktop, in
  both themes, Copy copies the mark, a row opens its text; with no stub,
  no list (the free build). `contrast.spec.ts` and
  `designRules.test.ts` pass as they are.

## Boxcar constraints

The eight of the Program's section 2.5:

1. The engine starts an attempt only through `crew.Launcher`: not
   touched.
2. `session.Local` drives a `session.Process`: not touched.
3. An attempt's context is files in its worktree: kept; the task files
   are written as on `main`, and the record is beside them in the data
   directory.
4. The agent talks to the server over the network: kept; the lookup is
   one more route under the session's token.
5. Checks go through `internal/proc`: not touched.
6. Platform splits are platform tags and the app's screens show a
   capability: kept; the list shows what the ledger holds, never a plan;
   the tool shows when the guide lists it.
7. Evidence rows' `where`: not touched.
8. `hostagent` is a launch site: kept; its Host refuses the reads, and
   a hosted attempt asks no lookup.

## Open-core checklist

- [x] Article 1: no paid code, licence or plan check; the checks are a
  provider's registration, readiness and optional interfaces.
- [x] Article 2: the free build is `main`'s, task files, tools, guide
  and pages.
- [x] Article 3: no flag, setting, tag or test-only binary.
- [x] Article 4: nothing here starts, stops, approves or waits beyond a
  bound; what an attempt was given is inspectable and exported.
- [x] Stored-record rule: no captured output, token or secret is handed
  to a provider; a block's text stays in the data directory and its
  export, never in the ledger, the run's log or a sync provider's hands.

## The PRs

The rows are units of work; how rows are grouped into pull requests
follows `AGENTS.md` (Working an issue) at the time they are built. Each
row leaves `main` working and green on its own, and with no context
provider registered changes nothing. CT-1's reads and CT-2's route are
boundary parts (what a provider may read; what an agent may be handed
and ask), and CT-3's recording decides who may see a repository's
context: each is kept in commits of its own and read adversarially.
**The ledger's step** CT-3a is a row of its own that ships alone, first,
right before the pull request that holds CT-3, so that it can be
reverted alone. A provider in another module builds against a tag (or,
by the maintainers' word, a commit) of this module that holds CT-1 to
CT-3.

| Row | Title | Contains | Touches | Tests | Depends on | Size (code + tests) |
|---|---|---|---|---|---|---|
| CT-0 (#1034) | plans: context seams | this file; the line in `docs/plans/ext.md`'s C4 | `docs/plans` | none: docs only | none | docs ~900 |
| CT-1 | ext, gitcli, crew, api: committed trees, the query's work fields, commit notes, the pack mark | `ContextQuery`'s fields and `Valid`, `ContextTicket`, `ContextCheck`, `ContextFinding`, `ContextInput`'s fields, `ContextBlock.Pack`, `CommitNoter`, `CommitNote`, the bounds; `Host.ReadTree`, `ReadBlob`, `Denied` and their types; `gitcli.RunEach`; the query filled, the commit notes queued, the pack mark in the task file; the three methods in `internal/hostagent`; `FakeHost`'s reads, the `Context` check's parts, the `Tree` check | `ext`, `ext/exttest`, `internal/gitcli`, `internal/crew`, `internal/api`, `internal/hostagent` | the CT-1 lists | CT-0 merged | ~800 + 1,000 |
| CT-2 | ext, api, cli, agents: the lookup | `ContextLookup`, `LookupRequest`, `LookupAnswer`, `LookupItem`, `LookupCoverage`, the bounds; the route, `Engine.LookupScope`, the in-flight bound, the guide's operation and `needContextLookup`, the MCP tool and its listing, `conductor task lookup`, the skill's line, the exttest check; `docs/protocol.md` | `ext`, `ext/exttest`, `internal/api`, `internal/crew`, `internal/cli`, `internal/agents`, `docs` | the CT-2 lists | CT-1 | ~500 + 650 |
| CT-3a | ledger: the next schema version, a barrier, with `attempt_context` | the migration alone | `internal/crew/ledger` | the CT-3a list | CT-2 | ~40 + 150 |
| CT-3 | crew, api, web: the attempt's context recorded and shown (after the step) | `Tx.AddAttemptContext`, `Ledger.AttemptContext`, the context file and its writer, the rows in the workspace transaction, `Engine.RecordLookupContext` and the lookup's blocks, the task route's `context`, the body route, `filterTask`, the candidates' count; `utils/attemptContext.ts`, `AttemptContext.vue`, the run page's and the ticket's lists; `docs/protocol.md`, `docs/features.md`, `docs/repository-map.md` | `internal/crew`, `internal/crew/ledger`, `internal/api`, `web/app`, `web/e2e`, `docs` | the CT-3 lists | CT-3a | ~650 + 900 |

## Owner decisions

Dated 2026-11-19. Each is a default the rows build; the owner's answer
can change rows, and each says which.

1. **Who may read a repository through the Host?** Default: a provider
   registered in the context slot, and no other (a router's Host, a
   sync store's, Brakeman's desk's and `conductor host`'s answer
   `ErrUnsupported`). Reason: the reads exist for context; a narrower
   door is easier to keep. The other answer, any provider's Host: CT-1
   drops `TestReadsOnlyForTheContextSlot`'s refusals; nothing else
   changes.
2. **What the query hands a provider of a run's people and checks?**
   Default: the ticket's owner and assignees by display name, and each
   check's argv, bounded, as the crew holds it. Reason: a provider that
   maps a check to the code it tests needs the argv, and it already
   runs in the server's process with the worktree's path. The other
   answer, ids only: `ContextCheck.Argv` and `Cut` go; CT-1 is smaller
   by a test.
3. **Does an agent get a lookup tool?** Default: yes, `context_lookup`,
   listed only when a provider answers lookups, for an attempt's own
   session alone. The other answer, no tool: CT-2 leaves this plan, CT-3
   records only the task phase, and `attempt_context` keeps its `phase`
   column for later.
4. **Is a late pack given, and on what terms?** Default: given only
   once it is recorded against the attempt, at most 8 blocks an answer
   and 64 an attempt. The other answer, never: a lookup answers items
   only; CT-3 drops `RecordLookupContext` and its tests.
5. **What does Brakeman see of an attempt's context?** Default: each
   block's phase, source, title, mark and digest, under the class that
   shows the attempt; never the text. The other answer, nothing:
   `filterTask` drops the field.
6. **Where is a block's text kept?** Default: in the data directory's
   context file, carried by `conductor export`, never handed to a sync
   provider, kept as long as the run's rows. The other answer, not
   kept (rows and digests only): CT-3 drops the file, the body route and
   the export test, and a person can no longer read what an agent was
   given.
7. **The words.** Default: the app says "Context" and "pack mark", and
   shows the list only for an attempt that has rows. Any other words
   change CT-3's `utils/attemptContext.ts` alone.

## Acceptance

Observed row by row, each when its checks pass in CI and locally:

- CT-1: `TestReadTreeIsTheCommitsTree`, `TestReadTreeRunsNoRepositoryProgram`,
  `TestReadTreeIgnoresReplaceObjects`, `TestReadTreeStopsAtItsBounds`
  and `TestReadBlobRefusesDeniedAndOutsidePaths` show a provider reading
  exactly the commit it names, under the server's rules;
  `TestQueryCarriesTicketBaseAndEvidence` and `TestBaseAndCandidateNoted`
  show what it is told; `TestFreeBuildUnchanged` the free build.
- CT-2: `TestLookupFillsTheAttemptNotTheBody`,
  `TestLookupRefusedToTheAdmin` and `TestToolListedOnlyWithProvider`
  show the lookup scoped to the asker's attempt and listed only where
  it answers; `TestLookupUnavailableSaysSo` shows a provider's absence
  said, not hidden.
- CT-3a: `TestOlderBinaryOpensAnAttemptContextLedgerReadOnly`.
- CT-3: `TestAttemptContextRecordedBeforeTheAgentStarts`,
  `TestConcurrentLookupsKeepEveryBlock`, `TestCrashBetweenContextFileAndRows`
  and `TestDeliveredContextExported` show every block given recorded,
  kept and exported; `TestContextFileNeverHandedToSync` and
  `TestBrakemanTaskReadCarriesContextAsFiltered` show who sees it;
  `attempt-context.spec.ts` shows the lists on the run page and the
  ticket.

Done is every row merged with those checks green. A live check with a
real provider is the provider module's.
