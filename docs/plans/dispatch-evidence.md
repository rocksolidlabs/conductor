# Ticket to evidence: plan revisions, launch packages and a ticket's runs (Program item DS2)

A Dispatch ticket today says what was asked; a managed run says what was
done; nothing between them says which plan authorized the run, what the
run was handed, or which exact commit passed which check for the ticket.
This plan joins them: a ticket gets **plan revisions** (an immutable
record of who does what, with which agents and arguments, from which
commit, checked how, needing which decisions, approved by the operator),
a run is **launched from an approved revision** with a **launch package**
(the ticket as it stood, the revision, the attachments, the source
references, the constraints and the criteria, frozen and hashed), and the
ticket page shows **every run, its exact candidates with their check
receipts, its review findings and repairs, the decisions and the
delivery**. It is Program item DS2 (the Program, section 4.3), written
against `main` at 86b62cf3 (PR #442, 2026-10-30) after reading Dispatch's
store and routes (DS1's PR-0 to PR-7, merged), the open DS1 work it builds
on (PR-8's ticket page, PR #438; PR-12's session ticket routes and the
`ticket_read` and `ticket_comment` tools, on `issue-412`; PR-9 to PR-11
and PR-13 not opened yet, #409 to #411 and #413), the engine, the ledger,
the task files, the API's run and receipt routes and the run page. It is
reviewed before any of its code; the PRs at the end are cut into issues
after the review, one issue, one branch `issue-N`, one PR each
(AGENTS.md).

The names follow the railroad (the Program, section 1.2): Conductor is the
application, Yardmaster the agent harness that runs a crew's tasks and
owns their execution, Dispatch the ticketing, whose items are tickets.
Two words of the repo's are near the new one, and the plan keeps them
apart: "the agent's plan" is Claude Code's plan file (#289: the Files
tab's Plans and the plan-approval question), and "a change's plan" is a
file in `docs/plans/`; this item's record is a **plan revision**, shown
on a ticket page as "Plan" and named "plan revision 3" (or `r3`)
everywhere else. An attempt's checkout is a **worktree**, never a
"workspace" (decision D5).

## Articles it serves

1. No licence check: nothing here asks for a plan, a tier or a provider;
   the only conditions are the data directory, the ledger and the
   `crews.managed` switch that already gate a managed launch.
2. Useful with nothing else: ticket to plan to run to checked candidate
   to delivery is free work on the local store and ledger, with no
   account and no network.
3. Nothing is gated by a flag: there is no paid part to switch on.
4. Required approvals, inspection and export stay free and never degrade:
   this item adds a required approval (no launch from a ticket without an
   approved, fresh revision), adds inspection (every receipt it shows is
   read from the ledger with its log's SHA-256), and keeps its records in
   the two plain stores of the data directory that C6's export carries;
   stop, pause, cancel, retry and accept are the run's own, unchanged.
5. Dependencies point one way: no new module; `internal/dispatch` still
   imports the standard library and `modernc.org/sqlite` alone, and
   `internal/crew` never imports `internal/dispatch` (the API joins them).
6. No feature build tag: a plain `go build` carries all of it.
7. Not touched: no extension interface is called or defined; the
   package's context part holds Dispatch's own records, and C4's context
   blocks go where C4 puts them (the task file).
8. Not touched: no hosted dependency exists in this item; a replica
   (CS1, later) reads plans and launches from Dispatch's own rows, so
   nothing waits on a service.
9. `CGO_ENABLED=0` stays: both stores stay `modernc.org/sqlite`.
10. The repository contract holds: this plan before the code; race tests
    per package; vitest and Playwright for the pages; no new dependency,
    so `make notices` has nothing to regenerate; no frame or control
    message, so `internal/proto` and `web/app/utils/protocol.ts` are
    untouched and `docs/protocol.md` gains HTTP rows, the package's shape
    and two `dispatch` event entities.
11. The railroad names keep their meaning: tickets are Dispatch's, runs,
    tasks, attempts and their receipts Yardmaster's; "plan revision" is
    told from the agent's plan and from `docs/plans/` (above).
12. Execution stays behind one seam: a launch from a ticket goes through
    the engine and `crew.Launcher` like any managed launch; "Boxcar
    constraints" below takes the eight one by one.

## Why and what happens

The Program's work model (section 1.4) keeps distinct records for a
ticket, the approved plan revision that authorizes work on it, the run
that executes it, the attempts and candidates the run makes, the
verification receipts tied to an exact candidate, the review findings,
and the decisions; and it binds execution to an approved revision, so
that editing a ticket or commenting on it never silently rewrites a run
(section 1.5). On `main` the ticket and the run exist and the rest is
partly there and unjoined:

- A ticket links a run by hand (DS1's `run` link, one ticket per run),
  and the run's sessions will read the ticket as it is now (DS1's
  `ticket_read`, PR-12): a description edited mid-run reaches an agent
  that reads it again, with nothing saying it changed.
- A run is launched from a saved crew, which anyone may edit between two
  launches; the run keeps the crew as launched in the ledger
  (`runs.crew`) and the commit it started from (`runs.base`, whatever
  `HEAD` was at the launch), but nothing records who approved that crew
  and that commit, or against which version of the ticket.
- The evidence is in the ledger already: each check's receipt (argv, the
  commit checked as `subject`, exit, timing, `stable`, a log of at most
  1 MiB with its SHA-256), each attempt's submitted result (summary,
  verdict, findings), each command's receipt (an Accept anyway among
  them), read today task by task on the run page and through the
  receipts routes.
- An attempt whose check failed starts at that result with the check's
  words in its task file, the output cut to about its last 200 bytes
  (`tailWords`), though the ledger keeps the last 2 KiB and the log.

After this item ships, a person sees on a ticket page (`/dispatch/CND/12`):

- **Plan.** "Propose a plan" takes a saved crew with tasks and makes
  revision 1: the crew as it is now (members with their agents and
  arguments, which is how a model is chosen; tasks with their needs and
  checks; the project manifest; the yolo choice, fixed), the input
  revision pinned to a commit of the project's folder, the source
  references at that commit, the attachments and comments to hand over,
  constraints, the decisions a person will make, and a note. A revision
  never changes once made; "Revise" makes the next one from it.
  **Approve** (the operator, with a note) makes it the ticket's approved
  revision, superseding the one before. The page shows what is expected
  to be checked, and says when the approval no longer holds ("approved
  before the description changed") or the crew was edited since.
- **Launch.** "Launch from r2" starts a managed run named after the
  ticket (`CND-12 Fix the login loop`), at the revision's commit, with
  the revision's crew and yolo choice, and hands every attempt the
  package: `.conductor/ticket.md`, `.conductor/package.json` and the
  attachments in `.conductor/attachments/`, beside the task files.
  Launch is refused, in words, while the approval is stale, the ticket
  is closed or the project archived.
- **Runs.** Every launch with its state (a launch that failed says why)
  and its run, the run's continuations after a restart below it; each
  run's tasks with their state, blockers and the questions its members
  wait on; **candidates**: each attempt's result commit with the
  receipts of its checks (passed or failed, exit, duration, the log
  opened with its SHA-256), a review's verdict and findings, the repair
  attempts that followed a failed check or a review's findings, and
  "accepted anyway" where a person took a failed result.
- **Changes since launch.** A run whose ticket changed after it launched
  says so: "The ticket changed after this run launched: the description
  and the acceptance criteria; 2 comments since", with the history
  between; the run keeps working from what it was given, and the card
  offers Revise (a new revision) and Launch a follow-up (which needs the
  approval to hold again), never "update the run".
- **Delivery.** A delivery link (a pull request, a release) may name the
  candidate it delivers; the link keeps that candidate's verification
  receipt, and a project's close policy may require `verified`: a
  delivery whose candidate passed its checks.
- **Decisions and people.** The approvals with their notes, the
  acceptances, the Accept anyway receipts, and the decision records Program
  item DR1 will add (their link kind reserved here); the owner, the
  assignees, the approvers and launchers, and the agents of the plan and
  of each run with the version each reported.

In an attempt, the agent's task file says which ticket and revision it
works for and where the package is; `ticket_read` says what changed since
the launch and that the run follows the package; an attempt after a
failed check gets that check's receipt and its whole log as a file. The
run page's header says "Launched from CND-12 · plan r2". `conductor
dispatch` gains `plans`, `plan`, `launch` and `runs`. Nothing here
changes a ticket's status on its own: status, run state, task outcome,
verification, acceptance and delivery stay separate facts (the Program,
section 1.4).

## The changes

### `internal/dispatch`: plan revisions, launches and the package (schema version 2)

New files `plans.go`, `launches.go` and `package.go`, each with its test;
`schema.go`, `model.go`, `ops.go`, `rules.go`, `links.go`,
`acceptance.go`, `sweep.go` and `attachments.go` extended. The package
still has no HTTP, no session and no process, and imports nothing of the
engine's: the API is what joins it to the ledger.

**Schema version 2.** `Version` becomes 2; the `schema` slice is version
2 whole for a new database, and `migrations[1]` brings a version 1
database to it in the `BEGIN IMMEDIATE` transaction that reads the
version, as DS1 set the shape (`dispatch.go`, `prepare`):

| Table | Columns (bounds in `model.go`) |
|---|---|
| `plans` | `id` PK (16 hex), `ticket_id` FK, `based_on` (a revision of the same ticket, or `''`), `crew_id` (the saved crew it was taken from), `crew_name`, `crew_updated_at` (that crew's `updatedAt` then), `base` (the input revision, a full commit id), `body` (JSON, at most 2 MiB as stored: below), `digest` (the body's SHA-256), `bytes`, `state` (`proposed`, `approved`, `superseded`, `withdrawn`), `approved_at`, `approved_by` (the operator's display name), `approved_note` (at most 1,000 characters of Markdown: the rationale), `approved_rev` (the ticket's `rev` the approval was made at), `approved_intent` (the ticket's intent digest then), `withdrawn_at`, `withdrawn_by`, `created_at`, `created_by`, `fingerprint` (the proposing operation's, as `ops` keeps it: the request's content, so a resend can be checked once its receipt is pruned), `rev`; at most 100 per ticket, whatever their state |
| `launches` | `id` PK, `ticket_id`, `plan_id`, `run_id` (minted here: the crew's id, `-`, 8 hex characters from `crypto/rand`, the engine's shape, `crew.ValidRunID`), `label` (the run's own name, at most 60 characters), `ticket_rev`, `intent`, `criteria` (the acceptance criteria's SHA-256, as an acceptance records it), `package` (JSON, at most 640 KiB as stored; 512 KiB before the package's version 2, Program item IN1a), `package_sha256`, `state` (`pending`, `started`, `failed`), `why` (at most 2,000 characters), `agents` (JSON, at most 4 KiB: each member's agent and the version its identity probe reported at the start), `created_at`, `created_by`, `fingerprint` (the launching operation's, as for a revision), `started_at`, `rev`; at most 200 per ticket |
| `launch_files` | `launch_id`, `sha256`; PK of the two: the attachments a launch's package names, which the sweep keeps |
| `links` | three columns added, each `''` on every version 1 row: `fingerprint` (the adding operation's, as `ops` keeps it, written by every `link.add` from version 2 on: the request's content, so a resend can be checked once its receipt is pruned, as a revision's and a launch's are), `candidate` (a delivery link's alone: `<run>/<task>/<n>@<commit>`, at most 200 bytes, the ledger's own `<run>/<task>/<n>` reference with the commit) and `verified` (that candidate's verification receipt when the link was added, JSON, at most 8 KiB: 16 receipts with their `where` at its 32 bytes) |

Indexes: `plans(ticket_id, created_at)`, `launches(ticket_id,
created_at)`, `launches(plan_id)`, `launches(state)`, `launches(run_id)`,
`launch_files(sha256)`. No new unique index: a launch's run id is unique
by a rule checked on a local write (no other launch names it) and by the
ledger, where a planned run's row is made by an insert that refuses an id
the ledger has (`Tx.CreateRun`, below; `PutRun` upserts, so it is never a
planned run's first write), since a unique index could refuse a
replicated row, which DS1's schema never lets an index do.
The `changes` log gains two entities, `plan` and `launch`, each with its
`rev`, their rows naming their ticket and project as a comment's do; a
plan's or a launch's rows never bump the ticket's `rev`, so proposing,
approving or launching never makes a ticket's edit stale.

**The intent digest.** A ticket's intent is its title, description and
acceptance criteria; `IntentDigest(t)` is the SHA-256 of the three as a
JSON array. An approval records it, a launch requires it unchanged since
the approval, and a launch's package records it, so "changed since" is a
comparison, never a guess.

**A plan revision's body** (`model.go`, `PlanBody`), JSON:

- `crew`: the saved crew as the crew store keeps it (`internal/crew`'s
  JSON, which Dispatch keeps as it is and never decodes beyond the fields
  it shows: `id`, `name`, `members` with `agentId` and `args`, `tasks`
  with `member` and `kind` (the assignment: which member does the task,
  and whether it is work or a review), `needs` and `checks`, `manifest`,
  `cwd`), with its working
  directory set to the project's folder when the project names one, and
  `yolo` set to the choice the launch will make (the crew's, else the
  server's default when the revision was made), so the approval covers
  it;
- `agents`: one entry per agent the crew's members name (at most 12, the
  crew's member bound), the catalog entry as it resolved when the
  revision was made: `{id, command, startArgs, yoloArgs, envNames,
  envSha256, sha256}`, every part of it that `createLocalSession` puts into the
  session's argv and environment (`api/sessions.go`): the command as the
  catalog gives it (where a model is often chosen: `aider --model
  sonnet`), its session recipe's `startArgs` as the catalog gives them
  (`{id}` unexpanded), its yolo recipe's `args` when the revision's yolo
  choice is on and the agent has one (else empty), and the names of the
  environment the session gets from the entry, its `env` overlaid with
  the yolo recipe's `env` when that applies, with the SHA-256 of those
  names and values together, never a value; with the members' `args`
  this is the agent and model selection the approval covers, however the
  entry makes it (a yolo recipe that adds `--model` included), and a
  launch resolves each again the same way and refuses `stale_plan` when
  any part differs (Launching, step 1); `sha256` is the SHA-256 of the
  entry's other parts as `agentSelection` encodes them (`internal/api`,
  one function for the proposal, the approval, the launch and every
  attempt), the selection's identity, which the package carries and every
  attempt's session is checked against when it starts (The approved
  agents at every attempt, below), so the approval holds at each attempt
  a run makes, not only at its launch;
- `manifestFile`: `{path, blob, manifest}`, for a crew whose project
  manifest is the file form (`Crew.Manifest` is then `{file: true}` and
  nothing else): the file's blob at the base and the manifest as the
  engine reads it from that blob (`resolveManifest`, exported for the API
  as `crew.ResolveManifest`, so it is validated as a launch validates
  it), at most `maxManifestJSON` (64 KiB) as encoded, so the body holds
  the `verify` steps the page and the plans list count; the file is read
  again at each tree's commit as on `main`, so an attempt that changes it
  changes its own steps, which its receipts show;
- `references`: at most 20 of `{path, start?, end?, note?, object, kind}`:
  a path relative to the crew's working directory (cleaned, no `..`, at
  most 1,024 bytes), an optional line range (1 to any, start at most
  end), a note of at most 200 characters, and the git object the path
  names at the base (`kind` `blob` or `tree`), checked when the revision
  is made;
- `attachments`: at most 8 attachment ids of the ticket, at most 16 MiB
  in all; a display name need not be a file name on every system (DS1
  allows `Spec.png` beside `spec.png`, and `CON`): each is written in the
  attempt under a name the package derives (The package, below);
- `comments`: at most 20 of `{id, sha256}`, comment ids of the ticket
  with the SHA-256 of each body, which the store sets from the comment's
  row in the proposal's transaction, at most 64 KiB of body in all, so the
  approval covers the words handed over and an edit of one after it is
  seen (`stale_plan` at a launch);
- `constraints`: Markdown, at most 8 KiB;
- `decisions`: at most 10 of `{id, question, kind, before}`: the decisions
  a person will make, an id of the task-id shape, a question of at most
  500 characters, `kind` `approval` or `clarification`, and the tasks
  that wait on it (task ids of the crew, at most 8); recorded and shown
  here, held by Program item DR1's durable decisions when it lands;
- `note`: Markdown, at most 4 KiB: why this revision.

The body's bound, 2 MiB as encoded, is a bound the proposal checks, not
one every part at its own bound fits under: a crew at `crew.MaxEncoded`
(1 MiB) fits, but the catalog lets an agent's command hold 32 arguments
of 4,096 bytes, backslashes included, so twelve agents' commands alone
can encode to about 3 MB (3,050,090 bytes with `echo` and 31 such
arguments each), past the bound before any crew. A body encoded past 2 MiB is refused (`bound`,
naming its largest part: "the agents' commands are 2.9 MiB of the
revision's 3.1 MiB"), and nothing is kept; a revision's read answers the
body as the bytes it is kept as, under `MaxPlanReply`, 3 MiB, which
`dispatch_plans_test.go` measures with a body at exactly 2 MiB. The
expected checks are not stored apart, since they are the crew's tasks'
checks and the manifest's `verify` steps (the crew's own manifest, or
`manifestFile.manifest`), which the body holds.

**Operations** (`plans.go`, `launches.go`; refusals as DS1's, with one
added, `stale_plan`):

- `plan.propose`, `ProposePlan(ctx, op, key, req PlanRequest, body
  PlanBody)`: the operation's fingerprint is the request as the client
  sent it (`req`: the crew's id, the base as given, the selections, the
  texts), not the body the API built from it, so a resend is answered
  by its receipt whatever the saved crew or `HEAD` became since. The
  ticket live (`not_found`), its project not archived (`archived`);
  `based_on` a revision of the ticket (`not_found`); the attachments and
  comments named live and the ticket's (`not_found`), within their
  bounds (`bound`), each comment's digest set from its row; every
  decision's `before` a task of the crew (`invalid`); at most 100
  revisions (`bound`). One `plans` row in state `proposed` and one change
  row (`entity: plan`, `op: create`), whose value is the revision's
  summary (`{id, ticketId, basedOn, crewId, crewName, base, digest,
  bytes, fingerprint, createdAt, createdBy}`), never the body: a body is past
  `MaxChangeValue` (64 KiB), and it is immutable content named by its
  digest, which replication ships beside the row that names it, as it
  ships an attachment's file. Any actor may propose (an agent's proposal
  comes with Program item BK1, under its own actor); the receipt's `id`
  is the revision's.
- `plan.approve`, `ApprovePlan(ctx, op, id, ticketRev, note)`: the
  operator's alone (an actor other than `admin` is `invalid`: an approval
  is the operator's, decision D4); `expectedRev` is the revision's and
  `ticketRev`, when given, the ticket's (`stale_revision` for either); a
  withdrawn revision is `invalid`; a revision a handed-over comment of
  which is gone or edited since it was made (its body's SHA-256 no longer
  the comment's row) is `stale_plan` ("comment 4 was edited since
  revision 3 was made: revise it"), since the digest is the immutable
  body's and an approval cannot change it. Sets `approved_at`, `approved_by`,
  `approved_note`, `approved_rev`, `approved_intent` and `state:
  approved`, one set row each that changes, and sets every other approved
  revision of the ticket `superseded`, one row each. A revision approved
  again (its approval stale, or superseded since) records the new
  approval beside the old in the history.
- `plan.withdraw`, `WithdrawPlan(ctx, op, id)`: any revision not withdrawn;
  `withdrawn_at`, `withdrawn_by`, `state: withdrawn`. An approved one
  withdrawn leaves the ticket with none approved; the launches made from
  it keep naming it.
- `ticket.launch`, `Launch(ctx, op, key, planID, label)`: `expectedRev` is
  the ticket's, so what launches is what the person saw (`stale_revision`
  otherwise). In the transaction: the ticket live (`not_found`) and open
  (`invalid`: "CND-12 is done: reopen it to launch"), the project not
  archived (`archived`); the revision the ticket's (`not_found`) and
  approved (`invalid`, with its state's words: "revision 3 is proposed:
  approve it first", "superseded by revision 4", "withdrawn"); the
  approval holding, its intent the ticket's now (`stale_plan`: "the
  description changed since revision 3 was approved: approve it again or
  revise it"); every attachment and comment it names still live and
  each comment's body the one approved, its SHA-256 the revision's
  (`stale_plan`, naming the first gone or edited: "comment 4 was edited
  since revision 3 was approved: revise it", the remedy a new revision,
  never an approval, since the recorded digest is the body's); at most
  200 launches (`bound`).
  Then the package is built from the rows (below), at most 640 KiB
  (512 KiB before the package's version 2, Program item IN1a)
  (`bound`, naming its largest part), hashed; the run id is minted; the
  `launches` row is written in state `pending`, one `launch_files` row
  per attachment, and one change row (`entity: launch`, `op: create`)
  whose value is the launch's summary without the package (immutable
  content named by its digest, as a body is). Nothing is started here:
  the receipt's `id` is the launch's, and the API starts the run after
  the commit (Events, orderings and invariants).
- `launch.started` and `launch.failed`, `StartedLaunch(ctx, op, id,
  agents)` and `FailedLaunch(ctx, op, id, why)`: from `pending` only
  (`invalid` otherwise), `state`, `started_at` or `why`, and `agents`,
  under the actor `server` with the op ids `<launch>.started` and
  `<launch>.failed`, the same whoever records them (the launch route or
  the reconcile at start: the first commit stands, the other is answered
  by its receipt).

**The package** (`package.go`, `Package`), version 1, built from the rows
inside the launch's transaction, so it is one state of the ticket:

- `ticket`: `{id, key, rev, title, description, acceptance, status,
  priority, horizon, owner, assignees, labels, milestone, parent,
  blockers, blockersTotal, intent, criteria}`: the labels by name, the
  milestone as `{name, target}`, the parent as `{key, title, status}`,
  the live tickets that block it (at most 20, by key, with their titles
  and statuses), every reference read as live or not as DS1 reads it;
- `plan`: `{id, n, digest, basedOn, approvedAt, approvedBy, note, crew:
  {id, name, updatedAt}, base, yolo, agents}`, `agents` the revision's
  approved selection as `[{id, sha256}]` (each agent's id and the
  `sha256` its body entry holds, at most 12, about 1.2 KiB at the ids'
  bound, inside the measured worst);
- `references`, `attachments` (`{id, name, file, size, sha256,
  contentType}`), `comments` (`{id, n, author, by, at, body, sha256}`),
  `constraints`, `decisions`, as the revision names them, read now;
  an attachment's `file` is the name it is written under in the attempt,
  derived here and never the display name: `NN-<portable>`, `NN` its
  place in the revision (`01` to `08`) and `<portable>` the name with
  every byte outside `[A-Za-z0-9._-]` made `_`, cut to 64 bytes, its
  leading and trailing dots trimmed (`_` when nothing is left), so the
  eight are distinct whatever a file system folds (case, Unicode
  normalization), ASCII, and never a name Windows reserves (`CON`,
  `aux.txt`: none begins with two digits and a dash) or one ending in a
  dot;
- `launch`: `{id, run, label, at, by}`.

The crew is not in it: the run keeps the crew as launched in the ledger
(`runs.crew`) and the revision's body has it. The bound holds every part
at its bound with every text at its escaped worst (each Markdown
character at most doubled, U+2028 and U+2029 six bytes), which
`package_test.go` builds and measures, as DS1 measures its replies; it is
encoded as Dispatch encodes values (HTML escaping off).

**Reads.** `Plans(ctx, key)` (every revision's summary, newest first, its
number `n` its 1-based place among the ticket's revisions by
(`created_at`, `id`), derived at read and never stored, as a comment's
number is, so a replicated revision takes its place without renumbering
a row; `stale` with the words and its remedy when an approved
revision's intent is no longer the ticket's (`approve`) or a comment it
hands over is gone or edited (`revise`); `crewChanged` and
`agentsChanged`, whose remedy is `revise` too, are the API's, below), `Plan(ctx, id)`
(the summary and the body), `Launches(ctx, key, limit)`, `LaunchByID(ctx,
id)` (the summary and the package), `PendingLaunches(ctx)`, and
`ChangedSince(ctx, launchID)`: the intent fields set in the ticket's
`changes` rows after the launch's own create row (by the local `seq`),
the comments added since, and the ticket's `rev` now. `TicketView` gains
`approved`, the approved revisions' summaries (empty for none, one on a
local write, two when a merge left two: the field rules), and
`launchCount`; the revisions and the launches are their own reads, so
`maxTicketReply` keeps the measure DS1 gave it.

**Links.** `LinkDecision`, `decision`, joins the kinds and is refused on
a local write ("reserved for durable decision records", Program item
DR1), kept when a merge brings one, as `verified` was in DS1.

A run belongs to one ticket at most, and so does its whole **chain**: the
run, every run it continues and every run that continues it (the
ledger's `continued_by` rows and the run records' `resumedFrom` and
`resumedBy`). A ticket holds a chain when it launched a run of it (a
package) or has a live `run` link to a run of it. A `run` link to a run
whose chain is held is refused `run_linked` naming the holder, another
ticket or the link's own ("launched from CND-12: it is among CND-12's runs
already", "CND-7 links run A, which this run continues"), so linking B, a
continuation of A, is refused while A's link stands on any ticket, and a
second link into one chain on the same ticket is refused as a
duplicate. DS1's `runHolder` compares one `links.ref`; it becomes a
check over the chain's runs and their packages, on `link.add` and on an
undelete that brings links back (`releaseRuns`, which removes a link
whose chain another live ticket holds now, as it removes one whose run
is held). The store's sight of a run's package and of its chain are
functions the API gives it, `SetRunTicket(fn func(run string) (ticketID
string, ok bool))` and `SetRunChain(fn func(run string) []string)`,
beside `SetRunExists`; a chain only grows by a continuation, a new run,
so a check made at a link's write stays true until the link is removed.
That holds for links written by this version; a version 1 store may
already hold a chain on two tickets, since DS1's `runHolder` compared one
run id: T1 links A, T2 links B, and B continues A (or a merge brought
such links). Nothing rewrites them (Migrations, below): such a chain is
**contested**, `ChainHolders(ctx, run)` (new: the live tickets that hold
the run's chain, by launch or by live `run` link, read over
`SetRunChain`'s runs) naming more than one, and while it is, no ticket
owns it: each contesting ticket's runs list shows its own linked run
alone, marked `contested` with the other holders' keys and the words
("run B continues run A, which CND-7 links: remove one of the links"),
the chain's other runs listed under none; a delivery naming a candidate
of any run of it is refused `run_linked` naming every holder (A
delivery's candidate, below), so no close policy is met through it; and a
new link into it is refused as any held chain's is. Removing one of the
links (DS1's `link.remove`, a person's act, kept in the history) ends the
contest, and the remaining ticket holds the chain whole; no rule picks a
winner, since both links were valid when made.
A launched run has no `run` link: its launch is the ticket's reference to
it. A delivery link
takes `candidate`, a field of `NewLink` and so of the request's
fingerprint, and `verified`, an argument of `AddLink` apart from it
(PR-9: the API reads the receipt, A delivery's candidate below; the
store checks their shapes and keeps them; any other kind with either is
`invalid`).

**The close policy.** `verified` joins `CloseRequirements`
(`acceptance.go`): met when a live delivery link's `verified` receipt says
`passed`. `CloseMet` gains `verified`, and `why` names an unmet one ("the
delivered candidate build#2 failed the check test", "no delivery names a
candidate").

**The sweep and the files.** `collectOrphans` keeps a file whose hash a
`launch_files` row names, in the same held transaction that reads the
live attachments and the leases, so a package's attachment outlives its
removal from the ticket. `OpenLaunchFile(ctx, launchID, sha256)` opens
the content a launch's package names, by its hash, and nothing else.

### `internal/crew/ledger` and `internal/crew`: a run carries its package

**The ledger's next schema version.** `main` is at version 5 since EN1's
project manifests (#394); this item's is the next number when its PR
lands, 6, or 7 if C10's `checks.where` lands first (the two take
consecutive numbers, whichever merges second renumbering its own). Two
statements, `ALTER TABLE runs ADD COLUMN ticket TEXT NOT NULL DEFAULT ''`
and `ALTER TABLE runs ADD COLUMN package TEXT NOT NULL DEFAULT ''`;
`RunRow.Ticket` (the id of the ticket the run was launched from) and
`RunRow.Package` (the package's JSON, byte for byte as Dispatch built
it); `MaxPackage = 640 << 10` (`512 << 10` before the package's
version 2, Program item IN1a), which `PutRun` refuses past
(`ErrTooLarge`), as it bounds the crew; the reads carry both. The
migration is conservative (the owner's rule from the version 3 to 4
migration): every row an earlier version holds is a run no ticket
launched, which the empty defaults say; nothing is derived and nothing
reinterpreted; a database of this version opens read-only under an
earlier binary, as every newer one does.

**A planned launch** (`planned.go`, new). `Engine.LaunchPlanned(ctx, c
Crew, label string, p Planned) (*Run, func(), error)`, beside
`LaunchNamed`, with `Planned{RunID, Base, Digest string; Package
[]byte}`:

- the run id is the one Dispatch minted: `ErrInvalid` unless
  `ValidRunID` with the crew's id before its last dash; `ErrRunExists`
  when the engine holds a run of that id (under the engine's lock, as
  `add` keeps the run: a second in this process) or when the run's first
  ledger transaction finds a row of it. That transaction writes the row
  with `Tx.CreateRun` (new in the ledger: an `INSERT` with no `ON
  CONFLICT`, a primary-key conflict answered `ErrRunExists`, which the
  engine's `LedgerTx` interface gains), never
  `PutRun`, which upserts (`transition.go`): `transact` lets go of
  `e.mu` before its ledger I/O (`task.go`), so a check outside the
  statement would be check-then-act, and the insert is the check and the
  write at once, under the ledger's write lock, whatever another process,
  server or restart did. Refused, the engine drops the run before any
  attempt, and the row there is untouched;
- every other new run's first write is the same insert: an ordinary
  managed launch (`LaunchNamed`) and a continuation (`ContinueRun`) mint
  their id against the engine's map alone (`run.go`, `add`), which after
  a restart or an eviction does not hold a planned run the ledger has, so
  their `run.created` goes through `Tx.CreateRun` as well, and `PutRun`
  stays the write of every later transition of a run whose row exists. A
  minted id the ledger refuses (`ErrRunExists`) is re-minted under the
  engine's lock and the first transaction made again, nothing having been
  made yet (no worktree, no session, the refused transaction rolled
  back), at most three times before the launch or the continuation is
  refused with `ErrRunExists`; the row of the id is never written. The
  id is minted through a seam (`newRunID`) a test can force;
- the base is the revision's (`prepare(ctx, c, p.Base)`, the path a
  continuation already takes), never `HEAD` read again;
- the package is decoded into the engine's own reading of it
  (`package.go`, new: the header the engine needs, the attachments to
  copy, what `ticket.md` renders), its SHA-256 compared with `Digest`
  (`ErrInvalid` when they differ), kept on the run (`r.pkg`) and written
  on the run's row, with the ticket's id, by the run's first transaction
  (`run.created` gains the ticket's key and the digest);
- `Run.Ticket`, `*RunTicket{ID, Key, Rev, Plan, PlanN, Launch, Digest}`
  (JSON `ticket`, omitted for a run with no package), on the run's read,
  its record and `GET /api/runs`, so the run page, the sidebar's run and
  a tool know the run's binding without reading Dispatch.

`ContinueRun` takes the source row's package and ticket, so a
continuation's attempts get the same package and its row and its
`Run.Ticket` carry it; a continuation is never a new launch.

**The approved agents at every attempt** (`planned.go`, `task.go`,
`run.go`; the check is `internal/api`'s). Every attempt launches its
member's session through `Launcher.Launch`, which resolves the agent in
the catalog as it is then (`createLocalSession`), and a catalog edit takes
effect at once (`commitOverlay`): a launch-time check alone would let an
edit made after it change a queued attempt, a retry or a continuation,
and leaves even the first attempt a check-then-use gap. So for a run
with a package the engine reads the approved selection from the
package's `plan.agents` (kept in the ledger with the package, so a
continuation and a restart have it), and `LaunchSpec` gains `AgentSHA256`,
the member's agent's approved `sha256`. `createLocalSession` takes the
catalog entry once, computes its selection with the run's yolo choice
(`agentSelection`, the function the proposal used) and starts the session
from that same entry only when its `sha256` is the one given; otherwise
it refuses `crew.ErrAgentChanged` ("the agent claude is not the one plan
revision 3 approved: its catalog entry changed since"), before any
process exists. The engine fails that attempt at stage `agent`,
uncounted, and pauses the run's scheduling with the words, as a package
that cannot be read does (`failUnusable`), since every attempt of that
member would fail the same way: a person restores the entry and unpauses,
or revises the plan and launches a follow-up. A run with no package
passes no `AgentSHA256` and launches as on `main`.

**The package's files in each attempt** (`taskfile.go`, `task.go`,
`package.go`). In `dispatch`, after the worktree is made and before the
session starts, `writeTaskFiles` writes beside `task.md`, `task.json` and
`task-more.json`, when the run has a package: `ticket.md` (rendered by
the engine from the package: the key and title; the revision it stood at
and the plan revision that launched it, with the approval's note; owner,
priority, status, labels, milestone, parent and blockers; the
description; the acceptance criteria; the constraints; the decisions a
person makes; the references with their objects at the base; the
attachments, each display name beside the file it was written as; the
comments handed over), `package.json` (the
package's bytes as launched) and each attachment as `attachments/<file>`,
the package's derived name, which the engine checks again
(`^[0-9]{2}-[A-Za-z0-9._-]{1,64}$`, not ending in a dot, distinct)
before it opens anything, copied from Dispatch's store through
`UsePackageFiles(func(ctx, launch,
sha256 string) (io.ReadCloser, error))`, which the API gives the engine at
start, hashed and counted as it is copied; all through the attempt's
`os.Root` at its working directory, a file there already refused as the
task files are, and the `.gitignore` of `*` keeping them out of the
result. A review's attempt gets them too: it reviews against the same
criteria. A file that cannot be read, or whose hash or size is not the
package's, fails the attempt at stage `package`, uncounted, and pauses the
run's scheduling with why: the path a project manifest's step takes when
it cannot make a tree usable (`failUnusable`), since every attempt would
fail the same way until a person looks; `uncountedRow` counts `package`
and `agent` (The approved agents at every attempt, above) among those
stages, so a continuation passes over it as the run did.
`task.json` gains `ticket` (`{key, rev, plan, planN, digest, files}`,
never cut), and `task.md` a "Your ticket" section after the
instructions: the ticket's key and title, the revision it stood at and
the plan revision, the three files, and the rule that the run follows
them, a change made to the ticket after the launch being a person's to
decide (`task_blocked`, or a comment on the ticket).

**A failed check reaches the next attempt** (`verify.go`, `taskfile.go`,
`task.go`, `continue.go`). `verify` keeps the failing receipt on the
attempt (the row it wrote, without its log) with the tracked paths the
check changed, which the checks row does not hold: they are in the
`check.finished` event the same ledger transaction commits with the row
(`verify.go`, `ev("check.finished", …)` with `more` putting the row),
whose payload gains `k`. After a restart, and for a continuation, they
are read back from that durable event, never from memory:
`Ledger.CheckChanged(ctx, run, task, n, id, k)` (new in the ledger, and
on the engine's `Ledger` interface) reads the run's `events` row of kind
`check.finished` whose payload names the task, attempt, check and `k`,
or for an event written before `k` was added the latest of that task,
attempt and check; a `verify` step's are read from the finished event
EN1's verify steps write (#397), which names the same four. The next attempt's
`task.json` `previous.check` holds `{receipt, run, task, attempt, phase,
id, k, argv, subject, exit, signal, timedOut, stable, changed, tail,
logSha256, logBytes, log}`: the receipt's whole reference, `receipt`
(`<run>/<task>/<n>/<phase>/<id>/<k>`, the ledger's identity of the row)
and its parts, `run` the run whose ledger holds it (the source run's for
a continuation's first attempt, whose own `TaskFile.Run` is the
continuation, so the reference never points into the continuation's
namespace), `task` and `attempt` the attempt that failed it; the check or
`verify` step, the candidate it failed (the commit, which is where this
attempt starts), how it ended, at most 10 of the tracked paths it changed
(each cut to 1,024 bytes), the ledger's last 2 KiB of its output, and
the log's file;
`task.md`'s "The attempt before" says which check failed on which commit,
how, its last output in a fenced block, and where its whole log is. The
log, at most 1 MiB as the ledger keeps it, is written as
`.conductor/previous/check-<id>.log` through the same root, read with
`Ledger.CheckLog(ctx, run, task, n, id, k)` (a method added to the
engine's `Ledger` interface, `sqlLedger` reading `ledger.Check`); a log
the run's bound dropped (`log_dropped`) is said so, its hash and tail
kept. A continuation's first attempt gets the same from the source run's
rows (`Loaded.FailedChecks`, read with the attempts). In `fitTaskFile`'s
cut order, just before the attempt before's error is cut, the parts of
`previous.check` that can be large go into `task-more.json` one after
another, the tail, then `changed`, then `argv`, each cut said in `cut`:
a check's argv is valid at 8 KiB with any byte but NUL
(`validateArgv`), and control characters encode six bytes each, about
49 KiB as JSON, which with the task's text at its bound would pass
`maxTaskFile` and leave `fitTaskFile` nothing to cut. What is never cut
is the receipt's identity (`receipt`, `run`, `task`, `attempt`, `phase`,
`id`, `k`), its outcome (`subject`, `exit`, `signal`, `timedOut`,
`stable`) and the log's `logSha256`, `logBytes` and `log`, a few hundred
bytes at their bounds, so the repair attempt always starts and always
finds the receipt and the whole log. A review's findings reach the repair as they do
today (`repair`: the review, the commit it asked changes of, its summary
and findings).

### `internal/api`: the routes, the launch, the evidence

New files `dispatch_plans.go`, `dispatch_launch.go`, `dispatch_evidence.go`
and `candidates.go`, each with its test; `dispatch.go` (the route list),
`dispatch_session.go` (DS1's PR-12), `server.go` and `events.go`
extended. Every Dispatch route stays the admin's and a switchyard's `403`
(`launching`), its body decoded with `DisallowUnknownFields` and carrying
DS1's `{opId, expectedRev?, byName?}`; the candidates route is the admin's
as the task routes are (`requireAdmin`).

| Route | Body and reply |
|---|---|
| `GET /api/dispatch/tickets/{key}/plans` | `{plans}`, under `MaxPlansReply` (below): every revision's summary, newest first, with `n`, `state`, the approval (`approvedAt`, `approvedBy`, `note`, `rev`), `stale` and its words, `crewChanged` (the saved crew's `updatedAt` later than the snapshot's, or the crew gone), `agentsChanged` and the expected checks' count; never a body |
| `POST /api/dispatch/tickets/{key}/plans` | `{opId, crew, base?, basedOn?, references?, attachments?, comments?, constraints?, decisions?, note?, byName?}` (192 KiB, a ticket's body bound) → `201 {receipt, plan}`; `404 not_found` for a crew, a revision, an attachment or a comment that is not there; `409 invalid` with the words for a crew without tasks, one that is not launchable, a working directory that is no repository, a base that names no commit, a reference whose path is not at the base; the store's refusals as DS1 answers them |
| `GET /api/dispatch/plans/{id}` | `{plan}`: the summary and the body, under `MaxPlanReply` |
| `POST /api/dispatch/plans/{id}/approve` | `{opId, expectedRev?, ticketRev?, note?, byName?}` → `{receipt, plan}` |
| `POST /api/dispatch/plans/{id}/withdraw` | `{opId, expectedRev?, byName?}` → `{receipt, plan}` |
| `POST /api/dispatch/tickets/{key}/launch` | `{opId, plan, expectedRev?, label?, byName?}` (4 KiB) → `201 {receipt, launch, run}`; `409 launch_failed {error, receipt, launch}` when the run could not be started (the launch kept, failed, with why); `409 stale_plan`, `409 managed_off`, `503 ledger_unavailable`; the same `opId` again answers the same, never a second run |
| `GET /api/dispatch/launches/{id}` | `{launch}`: the summary and the package, under `MaxLaunchReply` (below) |
| `GET /api/dispatch/tickets/{key}/runs[?before=CURSOR]` | `{entries, next?}`: a page of the ticket's launches, runs and continuations (below) |
| `GET /api/runs/{run}/candidates[?after=TASK]` | `{run, managed, base, candidates, next?}`: a page of a run's candidates with their receipts (below) |
| `GET /api/sessions/{id}/ticket` | DS1's (PR-12), the reply gaining `launch` (below) |
| `POST /api/dispatch/tickets/{key}/links` | DS1's, a delivery link taking `candidate` (PR-9) |

**Proposing** (`dispatch_plans.go`). The operation is replayed first
(`Store.Replay`, below, with the request as its content), so a resend is
answered from its receipt whatever the crew or `HEAD` became since, and
an id used before with another request is `op_conflict`; then the
saved crew (`s.crews.Get`), refused unless it has tasks, is `Launchable`
and its members' agents are in the catalog
(`CheckAgents`); its working directory, the project's folder when the
project names one, else the crew's own, through `resolveCwd`, and a
repository (`crew.CheckRepo`); the base, `base` as given or `HEAD`, made a
full commit by `git rev-parse --verify <ref>^{commit}` in that directory
(an argv through `internal/gitcli`, the C locale); the crew's prefix in
its repository; each reference, cleaned and refused when it leaves the
directory, resolved at the base (`git rev-parse <base>:<prefix><path>`
and `git cat-file -t`) to its object and kind; a file-form project
manifest's blob at the base; the yolo choice, the crew's, else the
server's default now, fixed in the body. Then the store's `ProposePlan`
with the request as the fingerprint and the body built. `crewChanged` on
a read compares each revision's `crew_updated_at` with the saved crew's,
and `agentsChanged` each body's `agents` with the catalog now.
The approve route resolves the revision's agents as a launch does and
refuses `409 stale_plan` ("revise it") when one differs, so an approval
never covers a selection its immutable body does not hold.

**Replaying an operation** (`internal/dispatch`, `ops.go`, PR-1, so the
proposing route of PR-6 has it): `Store.Replay(ctx, op, intent, args)
(rc Receipt, found bool, err error)` answers a resend before the API
does any work of its own, as `Apply` would answer it and no weaker: a
retained receipt is returned only when the id's intent and fingerprint
are the request's (`op_conflict` otherwise, no row, as `Apply`). A
receipt pruned (`maxReceipts`) takes the `ops` row's fingerprint with it,
so the fingerprint is kept with what the operation made: the `plans` or
`launches` row's `fingerprint`, written in the operation's transaction
and immutable, and in its create row's summary, so a replica has it too.
The pruned operation is found through its create change row (actor, op
id, `n` 0), whose `entity` and `entity_id` name the revision or the
launch it made (the entity also gives the intent: `plan` is
`plan.propose`, `launch` is `ticket.launch`), and that record's
`fingerprint` is compared with the request's: the same, the resend is
answered done with that id and the route reads the record by it; another
(another crew, another base, `main` where `HEAD` was sent though both
resolve to one commit, another plan or label), `op_conflict`, no row, as
before the prune. A `link.add` is found the same way, through its add
row (actor, op id, `n` 0), whose link row keeps the operation's
`fingerprint` (`links.fingerprint`, written in the add's transaction and
immutable, and in the add row's value, so a replica has it too), and is
compared by it, never by the stored link's fields: `AddLink` fingerprints
the `NewLink` as sent, before an empty `ref` takes the URL
(`links.go`), so a request with `ref` omitted and one with `ref` equal to
the URL store one link and differ as requests; the fingerprint answers
the omitted-`ref` retry done and the other `op_conflict`, as before the
prune. A pruned `link.add` whose link row has no fingerprint (added under
version 1) cannot be compared and is `op_conflict` with words ("this
operation is too old to compare: send it as a new one"), which refuses a
retry rather than accept content it cannot check; a new operation for
the same link is then refused as a duplicate, so nothing is added twice.
Neither found means a new operation. `Store.Receipt` stays the plain read it is; the
three routes use `Replay`.

**Launching** (`dispatch_launch.go`), in this order:

1. the operation is replayed first (`Store.Replay`, above): an `opId`
   that made a launch already goes to step 3 with that launch, whether
   its receipt is kept or pruned; one used with another plan or label is
   `op_conflict`, its receipt kept or pruned (the launch's
   `fingerprint`); one the store refused is answered as DS1 answers a
   refusal. A launch started or failed is answered as it stands on any
   server. Anything that may advance a launch, a new one or the replay of
   a pending one alike, needs this server to own the ledger and to have
   finished taking it over: `crews.managed` on, the ledger the server's
   and writable, as a crew launch checks, and `launchReady` set
   (Reconcile, below) (`409 managed_off`, `503 ledger_unavailable`). A
   new one refused here leaves no pending row; a pending one replayed
   here is answered as it stands, pending, with `503
   ledger_unavailable`, and is never recorded failed: only the ledger's
   owner settles a launch, since the server that committed it may still
   be starting its run. Then, for a new one, the revision's `agents` are
   resolved against the catalog again, every part the body recorded
   (`409 stale_plan` naming the agent and what changed: its command, its
   session recipe's start arguments, its yolo recipe's arguments, or its
   environment, the yolo recipe's included; "revise it", since the body
   holds what was approved and only a new revision can cover the new
   selection), before anything is kept; this is the early refusal, in
   words, and not the gate: an edit after it is caught at each attempt's
   own launch (The approved agents at every attempt, above);
2. the store's `Launch`: the pending launch, its package and its run id,
   committed (a repeat of the `opId` finds the same launch);
3. one launch at a time per launch id in this process
   (`launchesInFlight`, a map of channels): a second request for the same
   launch waits for the first, at most the launch's own bound
   (`runTimeout`), then answers from the launch's state;
4. a launch `started` or `failed` already is answered as it stands (`201`,
   or `409 launch_failed`); a pending one whose run id the ledger has
   is adopted only when that row is this launch's (`runRowIsLaunch`: the
   row's `ticket` the launch's ticket and the SHA-256 of its `package`
   the launch's `package_sha256`), then recorded started (step 6) without
   launching; a row of the id that is not the launch's records it failed
   ("the run id is another run's"), that row untouched; else the run is
   launched:
   the revision's crew from its body, its working directory through
   `resolveCwd`, its members' agents checked as a crew launch checks them
   (`checkLaunch`: installed, identified), and `s.runs.LaunchPlanned`
   with the run id, the base, the digest and the package, on
   `runContext`, as `handleLaunchCrew` launches (a client that goes away
   does not stop it);
5. refused anywhere in step 4: the ledger is read for the run id, and a
   row there that is the launch's (`runRowIsLaunch`) means the run exists
   (an error after its first transaction is the run's to show), so the
   launch is recorded started; with none, or with another run's row
   (`ErrRunExists` from `LaunchPlanned`), `FailedLaunch` with the words,
   and `409 launch_failed`;
6. started: `StartedLaunch` with each member's agent and the version its
   identity probe reported (`identityFor`), and `201 {receipt, launch,
   run}`.

**Reconcile.** `reconcileLaunches` runs once the ledger is the
server's, after the ledger's recovery: in `New` right after
`recoverLedger` when the owner lock was taken at open, else inside the
one callback `openLedger` gives `Ledger.OnOwned`, which holds a single
function (`ledger.go`) and is already `recoverLedger`'s
(`api/ledger.go`): `openLedger` gains a parameter, `afterRecover
func()`, which that callback (and the open path) runs after
`recoverLedger`, and `New` passes the reconcile, so neither replaces the
other. It records every pending launch whose run the ledger has and is
the launch's (`runRowIsLaunch`) as started and every other as failed
("the server stopped before the run started", or "the run id is another
run's"). Launch admission waits for it: `launchReady`, an
`atomic.Bool`, is set by `afterRecover` once recovery and the reconcile
have both finished, and until then step 1 answers `503
ledger_unavailable` ("the server is taking over the ledger"), so no
launch is committed, or replayed, while the reconcile could read it
before its `run.created`: `awaitLock` makes the ledger writable and lets
go of its mutex before it calls `OnOwned` (`ledger.go`), and a request
let through in that gap would be marked failed while it ran. While there
is no ledger, launches stay pending, and the page says the server cannot
tell yet. `New` also gives the engine `UsePackageFiles` (Dispatch's
`OpenLaunchFile`) and the store `SetRunTicket` (a run's `Ticket` from the
engine, else from its record).

**A ticket's runs** (`dispatch_evidence.go`). `GET
/api/dispatch/tickets/{key}/runs[?before=CURSOR]` answers `{entries,
next}`, a page of at most 10 entries, newest first, and every launch, run
and continuation the ticket has is on some page: there is no cap and no
count in place of an entry. The entries are of three kinds, ordered
together by their time (a launch's `created_at`, a continuation's start,
a hand link's add) and then id: a launch, `{launch, run?, changed}`
(`run` absent while it is pending or when it failed; `changed` its
`ChangedSince`, `{fields, comments, rev}`); a run DS1 linked by hand,
`{link, run, contested?}` (`contested` the other holders' keys when its
chain is contested, Links, above, and then the chain's other runs are
not entries); and every other run of a chain the ticket holds (Links,
above), `{run, launch?, link?, continues?}`: the launch or the hand link
that holds its chain, and the run it continues (absent for a run before
the linked one in its chain, whose summary's `resumedBy` places it),
found by walking `resumedFrom` and `resumedBy` both ways from the
launched or linked run in the ledger and the run records. Each run of a
chain is one entry, since one ticket holds a chain and holds it once. `next` is an opaque cursor (the
page's last time and id) when there is more, else absent; the order is
computed from the launches, the links and the ledger's `continued_by`
rows, which are small, and only the page's entries are summarized. A
run's summary is `{id, label, state, outcome, paused,
startedAt, stoppedAt, record, resumedFrom, resumedBy, ticket, members,
tasks, questions, blockers}`: `members` at most 12 of `{name, sessionId,
status}`, so the page joins each member to its live session; `tasks` its
tasks counted by state, `questions` how many of its members' sessions
waited on a person when the reply was made (the CLI's figure; the page
counts its own from the live sessions: `web/app`, below), `blockers` at most 20
of `{task, reason, by, at}`; from the engine, else the run's record, as
`GET /api/runs/{run}` answers. A blocker's reason in this reply is cut to 200
characters (the run page has it whole), a launch's `why` is its 2,000.
The largest page, ten entries with every text at its bound and escaped
worst (a launch's `why`, its `agents` and label, twenty blockers),
measured whole as encoded under `MaxTicketRunsReply`, 1 MiB.

**Two reads' bounds.** `GET /api/dispatch/launches/{id}` answers the
summary and the package as the bytes it is kept as (at most 640 KiB since the package's version 2, 512 KiB before):
the largest, a package at its bound with the summary's texts (`why`,
`agents`, the label) at their escaped worst, is measured whole under
`MaxLaunchReply`, 1 MiB. `GET /api/dispatch/tickets/{key}/plans` answers
at most 100 summaries (the revisions' bound), with no body: the largest,
every summary's texts (the approval's note, the stale words, the crew's
name, the approver and author) at their escaped worst, is measured under
`MaxPlansReply`, 1 MiB. Both are exported beside `MaxPlanReply`, and the
CLI's limit takes them (PR-13).

**A run's candidates** (`candidates.go`). `GET
/api/runs/{run}/candidates[?after=TASK]` reads the ledger, so a run the
engine let go, or one a server before this one ran, still answers. A run
the ledger has not is looked for as `GET /api/runs/{run}` looks (the
engine, then the run records): a legacy run found there answers
`{managed: false, candidates: []}`, and only a run found nowhere is `404`.
A page holds at most 8 tasks, in the crew's task order, `next` the last
one's id when more follow, so every task is on some page. Per task, the
attempts that submitted something, newest first, at most 6 a task
(`more`, per task, counts the rest, which `GET /api/runs/{run}/tasks/{task}`
reads): `{task, kind,
attempt, commit, start, state, stage, standing, withdrawn, acceptedAnyway,
verdict, findings, receipts}`. `commit` is a work attempt's result,
`standing` that it is the task's result now (the task row's
`result_attempt`), `withdrawn` that a withdrawal took it back,
`acceptedAnyway` `{actor, at, command}` from the run's commands
(`intent: accept`, done, naming it), `verdict` and `findings` (counts by
severity) a review's, whose text (at most 50 findings, each its path,
line and message) the page reads when a review's candidate is opened
from the existing task route, `GET /api/runs/{run}/tasks/{task}`, which
answers each attempt's result with its findings whole: the candidates
reply carries counts only, so its bound does not hold fifty messages a
review; `receipts` are the attempt's rows of phase `check`
and `verify` whose `subject` is the commit, each `{phase, id, k, where, state,
exit, signal, timedOut, killed, stable, error, startedAt, finishedAt,
logSha256, logBytes, logDropped}`, `error` cut to 120 characters (the
task route has it whole), its log the existing step log route's
(with `X-Conductor-Log-SHA256`). No pid and no process group: a receipt
says what ran against which commit, where, and how it ended, never how
the process was made. `where` is the runtime the check ran in: the
checks row's `where` (`CheckRow.Where`) once C10's ledger version has
landed, and until then `native`, which is what every row is until item
F; whichever of C10's ledger PR and PR-8 merges second wires the column
through. The largest page (8 tasks, 6 attempts each, 16 receipts each,
every text at its bound and escaped worst: 768 receipts of about 1,100
bytes, under 900 KB with the candidates' own fields) is encoded whole and
measured under `MaxCandidatesReply`, 1 MiB.

**The attempt's ticket** (`dispatch_session.go`, DS1's PR-12). The
session's run's `Ticket` comes first: the ticket its package names, by
id, whatever links say, so a continuation's sessions find it too; a
deleted one is `404 no_ticket` with words saying the run follows its
package. Without a package, DS1's lookup by link stands. The reply gains
`launch`, `{id, rev, plan: {id, n}, digest, changed: {fields, comments,
rev}}`; the comment route resolves the ticket the same way.

**A delivery's candidate** (PR-9). On `link.add` with `kind: delivery`
and `candidate`, the API first replays the operation (`Store.Replay`
with `link.add` and the request: the key and the `NewLink` the client
sent, `candidate` among its fields), so a resend is answered from its
receipt, or once pruned through its add row compared by the link's
`fingerprint` (Replaying an operation, above), before the ledger is read: a retry after a lost reply gets the first
answer whatever the ledger holds now, or whether it can be read at all.
Only a new operation goes on: the API parses the candidate, checks that
the run is the ticket's (a run of a chain the ticket holds, and holds
alone: a contested chain is `409 run_linked` naming every holder, Links,
above), reads the attempt from the ledger (`503 ledger_unavailable`, nothing kept, when it
cannot)
(a work attempt whose result is the commit named: `409 invalid`
otherwise) and its receipts as the candidates route reads them, and hands
the store, as an argument apart from the request, `AddLink(ctx, op, key,
l NewLink, verified *Verified)`, whose fingerprint stays DS1's `{key,
NewLink}` and never includes `verified` (computed evidence: its time and
its rows may differ between two tries of one request, and two tries in
flight at once both compute it, the second answered by the first's
receipt as a repeat), the receipt `{at, passed, attempt, receipts: [{phase, id, k,
where, exit, stable, logSha256}]}` (`where` as the candidates route reads
it, so a delivery's recorded receipt tells native checks from a guest's
once item F lands; a receipt recorded is immutable, so it is recorded
now), `passed` when the attempt succeeded and
every receipt finished with exit 0 and stable, and false for a candidate
with no check at all.

**Events.** The store's `plan` and `launch` changes reach the pages as
`dispatch` events by DS1's path, unchanged; the longest is still
`milestone`'s 119 bytes, and the arithmetic of `maxDispatchEach`'s
comment becomes nine kinds, 105,984 bytes for an operation's entry.

### `internal/cli`, `internal/agents`: the CLI, the tools, the skill

`cli/dispatch.go` (DS1's PR-12) gains `conductor dispatch plans KEY-n`,
`plan show KEY-n [N | ID]`, `plan propose KEY-n --crew ID [--base REF]
[--based-on N] [--reference PATH[:START[-END]][=NOTE]]...
[--attachment NAME]... [--comment N]... [--constraints-file F]
[--decision ID:KIND:QUESTION[@TASK,...]]... [--note TEXT]`, `plan approve
KEY-n N [--note TEXT]`, `plan withdraw KEY-n N`, `launch KEY-n [--plan N]
[--label TEXT]` (the approved revision when `--plan` is not given) and
`runs KEY-n`, and `link` takes `--candidate RUN/TASK/N@COMMIT`; each write
mints its op id and asks once more on a lost reply, as DS1's commands do,
and the reads go through DS1's `maxDispatchReply`, which PR-12 derives
from the API's exported reply bounds and which takes this item's too:
`MaxPlanReply`, 3 MiB (a revision's body is at most 2 MiB as stored and
is answered as the bytes it is), `MaxLaunchReply`, `MaxPlansReply` and
`MaxTicketRunsReply`, 1 MiB each, leave it at 4 MiB; `runs` follows a
page's `next` until there is none; the CLI reads no candidates.
`conductor task ticket` (DS1's
PR-12) prints the launch part: the revision the run was launched at, the
plan revision, and what changed since. The MCP tool `ticket_read` says
the same in its text ("As
launched: CND-12 at revision 7, plan revision 3, in .conductor/ticket.md.
Now at revision 9: the description changed; 2 comments since. Your run
follows the ticket as launched."); `ticket_comment` is unchanged. No new
tool and no new agent write: agents read and comment (DS1's decisions,
13). The skill's "Your ticket" section (`agents/skill.go`, with its test)
says where the package is, that the run follows it, and that a change
that matters is told to a person, never acted on alone. The usage in
`root.go` and the completion table follow.

### `web/app`: the plan, the launch, the runs and their evidence

- `composables/useDispatch.ts`: the types of every new reply and the
  calls `plans`, `plan`, `proposePlan`, `approvePlan`, `withdrawPlan`,
  `launch`, `launchRead`, `ticketRuns` and `runCandidates`, every reply
  through DS1's ordering gate (`utils/dispatchOrder.ts`), a revision and a
  launch by id beside the ticket by key.
- `utils/dispatchPlans.ts` (vitest): a revision's words (its state, a
  stale approval's and a changed crew's reasons, and the remedy each
  offers: Approve again for an intent edit, Revise for a handed-over
  comment edited or gone or an agent changed, never Approve again for
  those), the expected checks
  from a body, why Launch is not offered (`launchBlockers`: no approved
  revision, a stale approval, the ticket closed, the project archived,
  managed runs off or no ledger), the reference field's parser
  (`path:12-30=note`), the default label (the key and the title, cut to
  60 characters at a word). As built since issue 770: the app computes
  no default label and this helper and its test are gone; the server
  names a run left unnamed, by the key alone when the title is quoted.
- `utils/dispatchEvidence.ts` (vitest): candidates grouped by task and
  attempt, each receipt's chip (passed, failed with its exit, timed out,
  interrupted, log dropped), the repair history (a review's
  `needs_changes`, then the reviewed task's attempts at the commit it
  reviewed; a failed check, then the attempt that started at the failed
  result), "changed since" in words, the blockers and questions of a run.
- `components/DispatchPlan.vue` (the section: the approved revision in
  full, members with agents and arguments and each agent's command as
  resolved, tasks with their assignee (the member) and kind, needs and
  checks,
  the yolo choice said plainly, the base, the references, what the
  package hands over, the required decisions; the other revisions folded
  below, newest first; Propose, Revise, Approve, Withdraw),
  `DispatchPlanPropose.vue` (the dialog: a saved crew, crews without
  tasks offered greyed with why; the base, `HEAD` by default; references
  as rows with the parser above; the ticket's attachments and recent
  comments as checkboxes; constraints, decisions as rows, a note;
  prefilled from a revision when revising), `DispatchPlanApprove.vue`
  (the note, the rationale), `DispatchLaunch.vue` (asks first: the
  revision, the commit, the yolo choice, the label; then the run's link,
  or the failure's words).
- `components/DispatchRuns.vue` (DS1's PR-11) lists `ticketRuns` page by
  page (Show older follows `next`, so every launch and continuation is
  reachable; a continuation on a later page than its launch names it):
  each launch with its state, its run and continuations, each run's state and
  task counts with `RunTasks.vue` as DS1 embeds it, its blockers, its
  questions with Open on the member's session, and
  `DispatchChangedSince.vue` (the banner, the history rows between the
  launch and now in DS1's history words, Revise and Launch a follow-up);
  `DispatchCandidates.vue` (per run: each task's candidates, newest
  first, a page of tasks at a time with More tasks following `next`,
  commit with Copy, receipts as chips with where they ran, a review's
  verdict and its findings counted, their text read from the task route
  when the review is opened, "accepted anyway"), `DispatchReceipt.vue` (a
  receipt in full: argv, the commit, where it ran, exit, duration,
  `stable`, and the log, fetched
  with the token from the step log route, its `X-Conductor-Log-SHA256`
  compared with the receipt's and shown), `DispatchRepairs.vue`.
- `components/DispatchDecisions.vue` (approvals with their notes,
  acceptances, Accept anyway receipts, and `decision` links when DR1
  makes them), `DispatchPeople.vue` (the owner, assignees, approvers,
  launchers and commenters; the plan's agents by name, each with a link
  to the Plan, where its command is printed once (UX1 PR-16), and each
  launch's agents with the versions they reported); DS1's delivery
  link form gains the candidate it delivers (a select of the ticket's
  standing candidates, with their receipts' summary), the delivery row
  its verification, and the project dialog's close policy `verified`.
- The ticket page (DS1's `DispatchTicketPage.vue`) places Plan after the
  acceptance criteria, then Runs, Decisions, Delivery (DS1's close policy
  and links) and People; it reads its revisions, launches and runs on
  `dispatch` events whose `ticket` is it (DS1's rule, the `plan` and
  `launch` entities among them) and on `run` events for the runs it shows,
  gathered for 250 ms (`RUN_EVENT_WINDOW_MS`'s rule), and everything on a
  `snapshot`; nothing polls. A question needs no run event: a member's
  session that comes to wait on a person changes the session, not the
  run (`Engine.OnRunChange` carries only what session changes do not,
  and `refresh` derives `NeedsInput` from the live attention). So the
  page counts a run's waiting questions as the runs store does, from the
  shared session store (`useSessions`, live on session events) joined to
  the summary's `members` by session id, through `utils/runs.ts`'
  `runState(run, sessions)`, and reads a run's summary again when a
  member session of it changes, as that store gathers the runs of member
  sessions that changed; the summary's own `questions` is used only for
  a run whose record answers (no live session to join).
- The run page: `CrewRunHeader.vue` shows "Launched from CND-12 · plan r3"
  from `run.ticket`, opening the ticket; DS1's chip stays for a run
  linked by hand.
- The palette (`usePaletteRows.ts`): the ticket page's group gains
  Propose a plan revision, Approve revision n, Launch from revision n
  (asks first) and Open run; the Tickets group's `→` gains Launch from
  the approved plan. No new prefix and no new verb (DS1's decisions, 11).
- Phone widths: one column; the candidates table a list; nothing of the
  desktop layout decides for the phone.
- No new npm package.

### Docs and the rest

`docs/protocol.md`: the rows above, "Plan revisions" and "Launches"
paragraphs in its Dispatch section, the package's shape, the `plan` and
`launch` event entities, and in its Crew runs section the attempt's
package files and `previous.check`. `docs/architecture.md`: its Dispatch
section gains plan revisions, launches and the package, and how a run's
evidence is read through its launch. `docs/repository-map.md`: the
`internal/dispatch` row (plans, launches, the package) and the
`internal/crew/ledger` row (the schema version). `README.md`: the
Dispatch section's lines for plans and launches and the CLI's.
`docs/features.md`: the entry, in the last PR. `web/e2e/stub-agent.sh`:
the worker writes the first line of `.conductor/ticket.md`, when there is
one, into the file it commits, so a spec sees that the package reached
the attempt. No config key, no workflow change, nothing under
`desktop/`.

## Events, orderings and invariants

**A plan revision.** `proposed` → `approved` → `superseded`; any state but
`withdrawn` → `withdrawn`; `superseded` → `approved` again, and `approved`
→ `approved` with a new approval when the old one went stale. Nothing
else. A revision's body, base, crew, references and selections never
change after its create: a change is the next revision (`based_on`). On a
local write a ticket has at most one approved revision, since an approval
supersedes the others in its own transaction; a merge may bring two
(below). An approval holds while its `approved_intent` is the ticket's
intent digest: an edit of the title, the description or the criteria
makes it stale, which the page says and which refuses a launch, until the
operator approves again; an edit of a comment the revision hands over
makes it stale too (its body no longer the SHA-256 the revision
recorded), since the approval covers the words the agents are given, and
for good: that digest is the immutable body's, so approving again cannot
make it hold (`ApprovePlan` refuses it `stale_plan`), and only Revise,
which takes the comment's body now into a new revision, does; the same
holds for an agent that resolves otherwise than the body's `agents`. An
intent edit alone is the one staleness an approval cures, since
`approved_intent` is the approval's own field. An
edit of anything else (status, priority, labels, owner, a comment added,
a comment not handed over edited) leaves it holding: the approval covers
what is asked, how it is accepted and what is handed over, and a
launch's package takes the rest as it stands at the launch. A launch is
refused as well while an agent of the revision resolves otherwise than
the body's `agents` recorded (Launching, step 1), and after the launch no
attempt of the run or of its continuations starts a session from an
entry whose selection is not the approved one: the check is made by the
launcher on the entry it starts, at every attempt (The approved agents
at every attempt).

**A launch.** `pending` → `started` or `failed`; nothing else. In order,
each step durable before the next begins:

1. Dispatch's commit: the pending launch, its package, its run id and the
   pins of its attachments. No process, worktree or session exists yet.
2. The engine: the run's first ledger transaction (`run.created`) puts
   the run's row with its package and its ticket, before any attempt is
   reserved (the engine's own rule: every transition goes to the ledger
   before its side effect).
3. Dispatch's commit: `started`, with the agents, or `failed`, with why.

A server that ends between 1 and 2 left no run: `reconcileLaunches`
records the launch failed when the server starts again, and a resend of
the operation answers that; the person launches again, a new operation
and a new run id. A server that ends between 2 and 3 left a run the
ledger has: the reconcile (or a resend) records it started, and the run
itself is what the ledger's recovery makes of a run its owner left open
(interrupted, continued by a person as any managed run is). A resend
while the first launch is in flight waits for it in this process; a
second server on the same data directory cannot launch at all, its ledger
being read-only without the owner lock, so step 1 refuses there, and it
cannot settle a launch either: Dispatch lets two servers write, the
ledger lets one own it, and a replay on the one that does not own it
answers the launch pending, never failed, so a launch the owner is still
starting is not marked failed under it. Nor can a server that has just
taken the lock: it admits no launch until its recovery and reconcile
have finished (`launchReady`). No launch makes two runs, and none adopts
another's: the run id is fixed by the first commit, the ledger's first
write of every new run, planned, ordinary or a continuation, is an
insert that refuses an id it has (`Tx.CreateRun`), so no later launch
overwrites a planned run's row either, the engine refuses an id it holds, and a launch is
recorded started on a ledger row only when that row's ticket and
package digest are the launch's.

**The package and an active run.** The package is built once, in the
launch's transaction, and never written again: its digest is recorded in
Dispatch, checked by the engine, and its bytes kept in the ledger, so
every attempt of the run and of its continuations, a retry made days
later included, gets the same `ticket.md`, `package.json` and
attachments. No Dispatch operation reaches the engine: the launch route is
the one call from a Dispatch route into a run, and it makes a new one. An
edit of the ticket, a comment, a label, a new revision or a withdrawal
changes Dispatch's rows and history and nothing of any run: not its
package, its crew, its tasks, its attempts or its sessions. The engine's
refusals for a managed run stand unchanged (a member added, started or
resumed by hand is `409 managed_run`): a revision applies to the launches
after it, and a change of scope is a new revision and a follow-up launch,
never a run rewritten in flight (the Program, section 1.5; live revision
of a run waits for its own approved design).

**Changed since a launch.** `ChangedSince` compares the launch's create
row with the ticket's later rows by the store's local `seq`: the intent
fields set (title, description, acceptance) and the comments added;
whether the intent changed at all is the package's intent digest against
the ticket's now. On a replica (CS1, later) the log's one order
(`at`, `actor`, `op_id`, `n`) takes the place of `seq`.

**Evidence is read, not copied.** A ticket's runs and their candidates are
read at each request from the engine, the run records and the ledger,
through each launch's run id and DS1's hand links; Dispatch keeps the
launches (what was launched, from which revision, with which package) and
nothing of the execution, with one exception: a delivery link's
verification receipt, recorded when a person names the candidate a
delivery is, so that the close policy stays a function of Dispatch's own
rows, readable on a replica that has no ledger. A finished receipt never
changes, so the recorded one stays true; a withdrawal that later takes
the candidate's result back leaves it (the checks did pass on that
commit), and the page says the result was withdrawn. A candidate's
receipts are the rows of its attempt whose `subject` is its commit, so a
receipt is never shown against a commit it did not check.

**The repair handoff.** After a failed check the next attempt starts at
the failed result, as it does on `main`; its task file names the
receipt (`previous.check.receipt`: the source run, task, attempt, phase,
check id and `k`, the ledger's identity, which resolves to the original
row after a restart and in a continuation, whose own run id is another),
and the log is written beside the task files before the session starts,
its hash the ledger's. After a review's `needs_changes` the reviewed task
starts again at the commit reviewed with the findings, as on `main`.

**Migrations.** Dispatch's version 1 to 2 is one migration in the `BEGIN
IMMEDIATE` transaction that reads the version (DS1's `prepare`): three
tables made, three link columns added whose `''` is true of every older
row (no link had a candidate, and a version 1 add kept no fingerprint
on its row); no row is read or rewritten, so no `changes`
row of `op: migrate` is needed (nothing is derived); a version 2 database
opens read-only under a DS1 binary, as any later version does. What
version 1 allowed and version 2 does not, two tickets holding one chain
by hand links to two of its runs, is kept as it was: the migration cannot
see a chain (the ledger is the API's, not the store's) and does not
guess one, and the rule is applied at read and at write instead (Links,
above): such a chain is contested, owned by neither ticket, refused as a
delivery's candidate and as a new link's target, until a person removes
one of the links. Both links and their history stay. The
ledger's step is the same in kind (two columns, defaults true of every
older run, read-only under an earlier binary). Both follow the
conservative rule the owner set on 2026-10-25: a migration never drops or
reinterprets what an older version kept.

**Field rules for replication.** DS1 settled the merge rules of Dispatch's
fields as a schema decision this stream may extend (the Program's open
decision O10; DS1's plan, Owner decisions, 1); this item's fields take
these classes, `rules.go`'s `Class` and its tests saying the same:

| Class | Fields | Rule |
|---|---|---|
| immutable | every new id; a revision's `ticket_id`, `based_on`, `crew_id`, `crew_name`, `crew_updated_at`, `base`, `body`, `digest`, `bytes`, `created_at`, `created_by`, `fingerprint`; a launch's `ticket_id`, `plan_id`, `run_id`, `label`, `ticket_rev`, `intent`, `criteria`, `package`, `package_sha256`, `created_at`, `created_by`, `fingerprint`; the `launch_files` rows; a link's `fingerprint`; a delivery link's `candidate` and `verified` (its add's payload, as its URL and note are) | never written twice; a body and a package are content named by their digest, shipped beside the create row that names them, as an attachment's file is |
| scalar | a revision's `state`, `approved_at`, `approved_by`, `approved_note`, `approved_rev`, `approved_intent`, `withdrawn_at`, `withdrawn_by`; a launch's `state`, `why`, `agents`, `started_at` | last writer wins per field, DS1's rule |
| gated | a launch made, and a revision approved | the writer applied the rule (an approved, fresh revision; the operator); a merge never applies it again, since the facts it rests on travel in the same log |
| kept | two approved revisions of one ticket, approved on two replicas | both stand; the page shows both, a launch names one, a person withdraws the other; no reconcile rule, since nothing is undone |

A launch's start is recorded only by the server whose ledger has its
run. Launches do not replicate (CS1, `docs/plans/sync-seams.md`): a
replica shows the run a launch made by its pointer, matched to the
ticket the pointer names. A revision replicates as its **view** (the
body less the crew's paths, its setup, every argv and each agent's
command), named by its own digest beside the whole body's, and a
store that holds only the view does not launch it (`409
body_elsewhere`); on the machine that holds the whole body, an
approval last written by another machine does not launch it until this
machine's operator approves it again (`409 approved_elsewhere`), so a
revision is launched only where it was made, on that machine's own
approval.

**Events.** `dispatch` events gain the entities `plan` and `launch`,
naming their ticket and project, by DS1's path and bound (the longest
event is still 119 bytes); a page reads again on them as on any change of
its ticket. A run's `run` events are unchanged; the ticket page takes
them for the runs it shows. No `internal/proto` or `protocol.ts` change:
these are HTTP API rows and event entities, documented in
`docs/protocol.md`'s Dispatch section and Events with their bounds, and
tested in `api/events_test.go` and the routes' tests.

## Out of scope

- Changing a run in flight: live revision of a managed run's tasks, a
  member added or resumed by hand on one (`409 managed_run` stands), a
  crew reshaped while it runs (the Program, sections 1.5 and 7).
- A person's review of a candidate, Request changes as a repair, the
  failed-check action with a CI job's evidence, findings kept across
  revisions (Program item RV1, which builds on these records); this item
  hands a failed check's receipt and a review's findings to Yardmaster's
  own next attempt, as the run already retries.
- Durable decision records, their routing and the holding of dependent
  work (DR1; the `decision` link kind and the revisions' required
  decisions are reserved and recorded here); people as crew members
  (HM1); the decision page (MB0).
- GitHub: importing an issue as a ticket, its images and repro steps in
  the package, a pull request's merged state read for a delivery,
  outcomes published (IN1a).
- Brakeman drafting tickets, splitting work or proposing revisions
  (BK1): the operations take any actor, and the approval stays the
  operator's.
- Context from the Code Context Graph in a package (CG1 through C4), and
  a context provider at all: the package's context is Dispatch's own
  records.
- Replicating revisions as their views, and not launches (CS1,
  `docs/plans/sync-seams.md`): the field rules are written here.
- A ticket's status changed by a run, a launch or a delivery: they stay
  separate facts, and a person moves the status.
- Launching a crew without tasks from a ticket, or a crew whose members
  run on a `conductor host`: a revision is of a managed crew, which runs
  on the server; DS1's hand link still links any run.
- Editing a crew inside a revision: a revision takes a saved crew as it
  is, and the crew editor changes crews.
- Search over revisions and launches; notifications, webhooks or the
  Roundhouse on a launch; an artifact store or provenance classes for
  receipts (item F's classes; C10 defines `where`, which this item only
  carries and shows); line-level authorship.

## Tests per surface

Per `docs/checks.md`: a Go change runs `make lint`, `go test -race
-count=1 ./...` (the whole module) and `make lint-static`; a UI change
runs `npm --prefix web run typecheck`, `npm --prefix web test` and `make
test-e2e` (which builds the web app and the binary first; the spec alone
while iterating, one Playwright run per worktree); a change spanning both
runs both; every PR checks its sign-off (`scripts/dco_check.py`). PR-2
(the sweep's pins), PR-4 (files written into a worktree) and PR-11 (the
pages) also run the Mac and Windows preflight rows. No Python script
changes, so the scripts' suite is not needed. The tests wait on the
mechanism, never on the clock (AGENTS.md): the resent launch is held at a
seam (`beforePlannedLaunch`), the reconcile is given its pending rows and
ledger rows directly, the specs wait on a run's outcome and on the
ledger's rows, and find their tickets by key and their runs by id.

**Go, `internal/dispatch`** (PR-1, PR-2, PR-9):

- `schema_test.go`: `TestSchemaVersion2`; `TestMigratedVersion2EqualsAFreshOne`
  (a version 1 store with projects, tickets, links and attachments
  brought to 2: every row kept, the new columns empty, its schema read
  back equal to a fresh store's, the unique indexes still the keys never
  minted twice); a version above the binary's opening read-only, as DS1
  tests it.
- `plans_test.go`: `TestProposeKeepsTheBodyAndItsDigest`;
  `TestProposeFingerprintsTheRequest` (the same request with another
  body answers the first receipt); `TestProposeBounds` (each bound at and
  one past it); `TestRevisionNumbersArePlaces`; `TestApproveSupersedes`;
  `TestApprovalIsTheOperators` (an agent's actor refused);
  `TestApprovalGoesStaleWithTheIntent` (the title, the description and
  the criteria each make it stale; status, labels, owner, a comment added
  and a comment not handed over edited do not);
  `TestApprovalGoesStaleWithAHandedOverComment` (a selected comment
  edited after the approval: `stale` with its words and the remedy
  Revise, the launch `stale_plan` naming it, approving the revision again
  `stale_plan` too and nothing changed; a revision made from it by Revise,
  with the comment's body now, approved, launches with the new body);
  `TestApproveAgain`; `TestWithdraw`; `ops_test.go`'s
  `TestReplayChecksTheContent` (the same id with another request:
  `op_conflict`, nothing written) and `TestReplayAfterPruningChecksTheContent`
  (receipts pruned past `maxReceipts`: a proposal resent with the same
  request answered done with the revision's id, one resent with another
  crew or `main` where `HEAD` was sent, both one commit, `op_conflict`
  with nothing written; a delivery `link.add` sent with `ref` omitted and
  pruned: resent with `ref` omitted answered done with the link's id, the
  same id sent with `ref` equal to the URL `op_conflict`, as each is
  before the prune; a version 1 link's add pruned: `op_conflict` with
  its words, and a new operation for it refused as a duplicate), PR-1's.
- `launches_test.go`: `TestLaunchMakesAPendingLaunch` (the run id's
  shape, the package's parts, the pins); `TestLaunchRefusals` (a closed
  ticket, an archived project, a revision proposed, superseded or
  withdrawn, a stale approval, an attachment or a comment removed, the
  200th launch, a stale expected revision: each with nothing kept but
  its failed receipt); `TestLaunchStartedOrFailedOnce` (a repeat, a
  conflict, from another state); `TestChangedSince`;
  `TestPendingLaunches`; PR-2's launch cases of the two replay tests
  (the same id with another plan or label: `op_conflict`, nothing
  written, before the prune and after it; the same request after it:
  done with the launch's id).
- `package_test.go`: `TestPackageAtItsWorst` (every part at its bound,
  every text at its escaped worst, under `MaxPackage`);
  `TestThePackageIsTheTicketsRev` (an edit committed before the launch
  and one after: the package holds the first's state and its rev);
  `TestPackageFileNames` (`Spec.png` beside `spec.png`, an NFC and an NFD
  spelling of one name, `CON`, `aux.txt`, a name ending in a dot or a
  space, one of 200 bytes, one of only `/`-free punctuation: each a
  distinct, portable `NN-` name).
- `sweep_test.go`: `TestSweepKeepsALaunchsFiles` (an attachment removed
  after a launch named it survives a sweep; one removed and never
  launched does not); `OpenLaunchFile` opening only a hash its launch
  names.
- `links_test.go`: `TestDecisionLinksAreReserved` (refused locally, kept
  by a merge); `TestALaunchedRunIsNoHandLink` (`run_linked` naming the
  ticket it was launched from, and the same for a continuation of a
  launched run); `TestAChainHasOneTicket` (run A linked to T1 and
  continued as B: B linked to T2 refused `run_linked` naming T1, B linked
  to T1 refused as a duplicate; A's link removed, B linked to T2 taken;
  T1 deleted while T2 links B and then undeleted: its link to A
  released); `TestMigratedContestedChainIsKept` (a version 1 store,
  written as DS1's schema and `runHolder` allowed, in which T1 links run
  A and T2 links run B, a continuation of A, brought to 2: both links
  and their add rows kept byte for byte, `ChainHolders` naming T1 and
  T2, a new link to A or to B refused `run_linked` naming both, from T1,
  T2 or a third ticket; T2's link removed: `ChainHolders` naming T1
  alone, and the removal in T2's history); PR-9's candidate and receipt shapes, `AddLink`'s fingerprint
  without `verified` (the same request with another receipt a repeat),
  `TestDeliveryReceiptKeepsWhere`
  (each receipt's `where` kept, the receipt at its 8 KiB bound).
- `acceptance_test.go` (PR-9): `TestClosePolicyVerified` (met by a
  passed receipt; unmet by a failed one, by a delivery without a
  candidate, each with its words).
- `rules_test.go`: every new field has its class; `model_test.go`: a
  delivery link's add, with a candidate and a receipt at their bounds, is
  still under `MaxChangeValue`.

**Go, `internal/crew/ledger`** (PR-3): `TestMigrationAddsTheRunsPackage`
(a version 5 database with runs, attempts and checks brought to the next
version: every row kept, `ticket` and `package` empty; one of the next
version opening read-only under the version before);
`TestPutRunBoundsThePackage`; `TestCreateRunRefusesAnIDItHas` (a row
of the id there: `ErrRunExists`, the row byte for byte as it was).

**Go, `internal/crew`** (PR-3, PR-4, PR-5):

- `planned_test.go`: `TestLaunchPlannedStartsAtTheBase` (`HEAD` moved
  after the revision: the run's base is the revision's);
  `TestARequestedRunIDIsTakenOnce` (an id held, one the ledger has, one
  of the wrong shape, and a row of the id written by another ledger
  handle after the engine's own check and before its first transaction:
  `ErrRunExists`, no attempt, that row untouched);
  `TestThePackageIsCheckedAgainstItsDigest`;
  `TestRunTicketOnTheRunAndItsRecord`; `TestAContinuationCarriesThePackage`
  (its row's `package` and `ticket`, the digest and `Run.Ticket` are the
  source's; its files are PR-4's test, below);
  `TestAnOrdinaryLaunchNeverOverwritesAPlannedRun` (a planned run's row
  in the ledger and a new engine, as after a restart, that does not hold
  it; `newRunID` forced to give its id first: an ordinary `LaunchNamed`
  and a `ContinueRun` each take another id, the planned row byte for
  byte as it was, its crew, base, ticket and package unchanged; forced to
  give it every time: each refused `ErrRunExists` after three tries, no
  attempt, the row untouched).
- `package_test.go`: `TestTicketMarkdown` (each part; an empty part left
  out); `TestTheAttemptGetsThePackageFiles` (`ticket.md`, `package.json`
  byte for byte, the attachments with their hashes, a review's attempt
  too, a file there already refused);
  `TestTheAttemptWritesTheDerivedNames` (the files are written under the
  package's `file`, never its display name; a `file` not of the derived
  shape refused before anything is opened);
  `TestAnAgentNotTheApprovedOnePausesUncounted` (a launcher that
  refuses `ErrAgentChanged`: the attempt failed at `agent`, uncounted,
  the run paused with the words; each attempt's `LaunchSpec` carrying its
  member's agent's `sha256` from the package, a continuation's too, and a
  run with no package carrying none);
  `TestAPackageFileThatDoesNotMatchPausesUncounted`
  (a short read, a wrong hash, the opener's error: the attempt failed at
  `package`, uncounted, the run paused with why; unpaused, the next
  attempt gets the files); `TestARetryGetsTheLaunchTimePackage`;
  `TestAContinuationsAttemptGetsThePackageFiles` (PR-4: a continuation's
  first attempt's `ticket.md`, `package.json` and attachments byte for
  byte the source run's).
- `taskfile_test.go`: `TestTaskFileNamesTheTicket` (`task.json`'s
  `ticket` never cut, the section in `task.md`); PR-5's
  `TestAFailedCheckReachesTheNextAttempt` (`previous.check` with the
  receipt's identity, tail and hash, the log file byte for byte the
  ledger's as `sqlLedger` reads it, a dropped log said so, the cut
  order, and `previous.check.changed` for a check that changes tracked
  files, read back from its `check.finished` event after the server is
  restarted and the run continued, not from memory),
  `TestAFailedVerifyStepReachesTheNextAttempt`,
  `TestAContinuationsFirstAttemptGetsTheFailedCheck` (its
  `previous.check.receipt` and `run` naming the source run, not the
  continuation, and resolving through `sqlLedger` to the original row
  after a restart, the log byte for byte that row's) and, in
  `ledger_bounds_test.go` beside the task files' existing worst case,
  `TestAFailedCheckHandoffAtItsWorst` (the whole handoff at its valid
  bounds: a check's argv at 8 KiB of control characters, about 49 KiB
  encoded, ten changed paths at 1,024 bytes of the same, the tail at
  2 KiB, with the task's text, inputs, repair and checks at their
  bounds: `fitTaskFile` succeeds, `task.md` and `task.json` within
  `maxTaskFile`, the receipt's identity, outcome and log's hash and name
  in `task.json`, and the tail, `changed` and `argv` in
  `task-more.json`, each cut said).

**Go, `internal/api`** (PR-6 to PR-9):

- `dispatch_plans_test.go`: `TestProposeSnapshotsTheCrew` (the crew edited
  after: the body unchanged, `crewChanged`); `TestProposePinsTheBase` (a
  branch name made its commit; `HEAD` moving after);
  `TestProposeChecksReferencesAtTheBase` (a path at the base taken, one
  only in the working tree refused, `..` refused);
  `TestProposeFixesTheYoloChoice`; `TestProposeRefusals` (a crew without
  tasks, one not launchable, an agent not in the catalog, no
  repository); `TestProposeResentFromItsReceipt` (the crew deleted
  between; the same `opId` with another request `op_conflict`);
  `TestProposeSnapshotsTheAgents` (the command, the start arguments, the
  yolo recipe's arguments when yolo is on and none when off, and the env
  names with the yolo recipe's overlaid recorded, no env value in the
  body; `agentsChanged` after a catalog edit);
  `TestProposeSnapshotsAFileManifest` (a file-form manifest with two
  `verify` steps at the base: the body's `manifestFile.manifest` holds
  them, the plans list counts them, the file then changed at `HEAD` and
  the body unchanged; a file at the base that is not a manifest refused);
  `TestProposeRefusesAnOversizedBody` (twelve agents whose commands are
  at the catalog's bounds with backslashes: `bound` naming the agents'
  commands, nothing kept; a body at exactly 2 MiB taken); the
  plans list's largest reply, 100 summaries at their worst, under
  `MaxPlansReply`; the approve and withdraw routes; the largest body's read
  under `MaxPlanReply`; auth and the switchyard's `403`.
- `dispatch_launch_test.go`: `TestLaunchFromATicket` (`201`; the run's
  package digest the launch's; the run's `ticket`; the launch started
  with its agents); `TestLaunchResentMakesNoSecondRun` (the same `opId`
  while the first is held at `beforePlannedLaunch`, and after it);
  `TestLaunchFailedIsKept` (an agent not installed: `409 launch_failed`,
  the launch failed with why, a resend the same); `TestLaunchGates`
  (managed runs off, no ledger, a read-only ledger: no pending row);
  `TestReconcileLaunches` (pending with the run in the ledger: started;
  without: failed; with another run's row of the id: failed, the row
  untouched; no ledger: still pending);
  `TestReconcileRunsAfterRecoveryInTheOneCallback` (a ledger whose lock
  is taken late: `recoverLedger` and then the reconcile both run, once,
  in that order, from the single `OnOwned` callback);
  `TestNoLaunchWhileTheLedgerIsTakenOver` (the lock taken late, the
  reconcile held at a seam: a new launch is `503 ledger_unavailable`
  with no pending row; released, the launch is `201` and stays started);
  `TestAReplayOnTheServerWithoutTheLedger` (two servers on one data
  directory: A commits a pending launch and is held before
  `LaunchPlanned`; the same `opId` sent to B answers it pending with
  `503`, and records nothing; A released, the run starts and the launch
  is started); `TestLaunchAdoptsOnlyItsOwnRun` (a run of the requested
  id already in the ledger with another package: the launch failed with
  its words, that run untouched); `TestLaunchRefusedWhenAnAgentChanged`
  (the catalog entry's command or env edited after the approval: `409
  stale_plan` naming it, no pending row);
  `TestLaunchRefusedWhenARecipeChanged` (only the yolo recipe's `args`
  (a `--model` changed), only its `env`, only the session recipe's
  `startArgs`, each edited after the approval of a revision with yolo
  on: `409 stale_plan` naming the agent and the part, no pending row; the
  yolo recipe edited for a revision with yolo off: the launch taken);
  `TestAnAttemptStartsOnlyTheApprovedAgent` (through `New`'s own
  wiring, the catalog entry's command edited after the launch was taken,
  with the first attempt held at the engine's seam before
  `Launcher.Launch`: released, the attempt fails at `agent`, uncounted,
  no session started, the run paused with the words; the entry
  restored and the run unpaused, the attempt starts. The same for a
  retry of a failed attempt, and for a continuation after a restart
  with the entry edited before its first attempt; a crew launched from
  the Crews page, with no package, launching on the edited entry as on
  `main`);
  `TestLaunchAttachmentsReachEveryAttempt` (through `New`'s own wiring,
  `OpenLaunchFile` given the engine as `UsePackageFiles`: an attempt gets
  the attachment byte for byte under its derived name; the attachment
  then removed from the ticket and the sweep run; a later attempt, a
  retry, gets it still); the launch read's largest reply under
  `MaxLaunchReply`;
  `TestEditingTheTicketNeverRewritesAnActiveRun` (the acceptance
  boundary's second half: a launched run paused, the ticket's title,
  description and criteria edited and a comment added, the run unpaused:
  the next attempt's `ticket.md` and `package.json` are the launch's byte
  for byte, the run's digest unchanged, `changed` names the three fields
  and the comment, and a follow-up launch is `409 stale_plan` until the
  revision is approved again).
- `dispatch_evidence_test.go`: `TestTicketRunsLaunchesAndLinks` (a launch
  and its continuation after a restart, a run linked by hand, the counts,
  blockers and questions, the page's bound);
  `TestTicketRunsPageThroughEverything`
  (a launch with 21 continuations and more than 200 launches and hand
  links: following `next` reaches every launch, run and continuation once,
  the newest continuation included; a hand link to B, a continuation
  of A, lists A and B once each under that link; a contested chain
  listing on each contesting ticket its own linked run alone, marked
  `contested` with the other's key, and the chain's other runs on
  neither);
  `TestCandidatesAreBoundToTheirCommits` (a check failing at attempt 1
  and passing at 2: each attempt's receipts its own commit's; a review's
  verdict and counts; Accept anyway and a withdrawal marked; 6 a task
  and `more`; a run the
  engine let go, from the ledger; the largest page encoded whole under
  `MaxCandidatesReply`; more than 8 tasks paged by `next`);
  `TestCandidatesOfALegacyRun` (a real unmanaged crew launch, not in the
  ledger: `{managed: false, candidates: []}`; an id found nowhere `404`);
  `TestCandidatesCarryWhere` (each receipt's `where`, `native`);
  `TestSessionTicketFollowsThePackage` (a continuation's
  session finds it; a deleted ticket; a run linked by hand still found by
  its link; `launch.changed`).
- `dispatch_test.go` (PR-9): `TestDeliveryLinkNamesACandidate` (passed,
  failed, accepted anyway; a candidate of another ticket's run, and a
  commit that is not the attempt's result, refused);
  `TestDeliveryLinkResentAfterTheEvidenceChanged` (the same `opId` resent
  after a newer check row of the attempt, and after the ledger was
  closed: the first receipt and the recorded `verified` answered, no
  `op_conflict`, nothing read from the ledger; resent once its receipt
  is pruned, the same; another candidate under that id `op_conflict`);
  `TestDeliveryRefusedForAContestedChain` (two tickets holding one chain
  by version 1 hand links: a delivery naming a candidate of either run
  refused `409 run_linked` naming both, nothing kept);
  `verified` through the project's edit route. `events_test.go`: the longest `plan` and
  `launch` events within the bound.

**Go, `internal/cli`, `internal/agents`** (PR-13): `cli/dispatch_test.go`
(each new command against a test server, a lost reply asked once more,
the largest revision read within the CLI's limit); `cli/task_test.go`
(`conductor task ticket` prints the launch part); `cli/mcp_test.go`
(`ticket_read`'s text); `agents/skill_test.go` (the section).

**UI** (PR-10 to PR-12): vitest `utils/dispatchPlans.test.ts`,
`utils/dispatchEvidence.test.ts`, `utils/dispatch.test.ts` additions (the
delivery's candidate, `verified`'s words) and
`composables/useDispatchLive.test.ts` additions (the `plan` and `launch`
entities, the runs' `run` events, and a member session's attention
changing with no run event: the run's questions counted from the session
store, its summary read again). Playwright:

- `dispatch-plan.spec.ts` (PR-10), only what PR-10 and the PRs before
  it build: a managed crew of stub workers, a ticket with an attachment;
  r1 proposed (its base shown as a commit, each task's assignee shown)
  and approved with a note; Launch: the dialog answers with the launch
  started and its run's link (from PR-7's launch route and `GET
  /api/dispatch/launches/{id}`), and the link opens the run page that
  `main` already has; a second browser sees the approval and the launch
  with no reload; the title edited: Launch offered no more, with "approve
  again"; approved again, launched again: a second launch with another
  run id. `palette-rows.spec.ts` gains the ticket page's rows.
- `dispatch-evidence.spec.ts` (PR-11), which takes the assertions that
  need PR-8's routes and PR-11's pages: a ticket launched as in
  `dispatch-plan.spec.ts`, the run under Runs and its page's header
  "Launched from CND-n · plan r1", the candidate's committed file
  holding the ticket's first line, which the stub read from
  `.conductor/ticket.md`; a crew whose check fails on its
  first run (a script, as `tasks.spec.ts` makes one) and whose review
  asks for changes once (the stub's): attempt 1's candidate with its
  failed receipt and where it ran, attempt 2 at that result passing, the
  review's finding counted and, opened, its message read from the task
  route, and the repair; a receipt's log opened, its shown hash the route's
  header; the run paused, the description edited, the run unpaused: the
  banner names the description and the next candidate's file still holds
  the launch-time line; a member that asks a question while its task
  stays running (the stub's `stub ask`) shows under the run's questions
  with no task transition and no reload, and answered, leaves them; the
  phone width one column.
- `dispatch-delivery.spec.ts` (PR-12): a delivery naming the final
  candidate, verified; a project requiring acceptance, delivery and
  `verified` closing once accepted; a candidate accepted anyway not
  verified, with the words; the approval's note and the acceptance under
  Decisions; the agents and their versions under People.
- `tasks.spec.ts`, `tasks-review.spec.ts` and `tasks-resume.spec.ts`
  unchanged and green: a launch from the Crews page is as it was.

**By hand**, what the tests cannot show: real agents (Claude Code and
Codex on WSL, Codex on the Mac) launched from a real ticket, each reading
`.conductor/ticket.md` and an image attachment and working from them; a
real review citing the criteria; a real pull request delivered; the
ticket page from a phone on the LAN. Recorded in `docs/features.md` with
what each showed.

## Boxcar constraints

1. The engine starts an attempt only through `crew.Launcher`: honoured; a
   launch from a ticket is `Engine.LaunchPlanned`, whose attempts are
   dispatched as every managed run's are, each session through
   `Launcher.Reserve` and `Launch`; Dispatch starts nothing, and the API
   starts runs only through the engine.
2. `session.Local` drives a `session.Process`: not touched; nothing new
   reaches a session, a pid or a tty.
3. An attempt's context is files relative to its worktree: honoured and
   extended; `ticket.md`, `package.json`, the attachments and a failed
   check's log are written beside `task.md` and `task.json` through the
   same `os.Root`, and everything they come from is durable (the package
   in the ledger and in Dispatch, the attachments pinned in Dispatch's
   store, the log in the ledger), never only in the server's memory, so
   the whole tree could be handed to a guest.
4. The agent talks to the server over the network: honoured; the
   `launch` part of `GET /api/sessions/{id}/ticket` travels the path the
   task tools take, HTTP with `CONDUCTOR_NOTIFY_TOKEN`; no channel of
   another kind.
5. Checks go through `internal/proc`: not touched; this item runs no
   check. It reads the receipts the checks runner wrote and hands one to
   the next attempt; the git commands it runs to pin a base and resolve
   references are argv arrays through `internal/gitcli`, as the Files
   tab's are, and are no check.
6. Platform splits stay platform tags, the UI shows capabilities: not
   touched; nothing here differs by platform or depends on a provider.
7. Evidence rows gain `where` in C10: honoured; this item adds no
   evidence row to the ledger and its views assume no native process: a
   receipt is shown by what ran, where, against which commit, and how it
   ended, never by pid or process group. Each receipt carries `where` in
   the candidates route (PR-8: the checks row's once C10's ledger version
   is there, `native` before it, the only value until F),
   `DispatchReceipt.vue` and the candidates' chips show it (PR-11), and
   the delivery's recorded receipt keeps it per receipt with the id,
   exit, `stable` and log hash (PR-9), since a recorded receipt never
   changes and must already tell a guest's checks from native ones when
   F lands; `TestCandidatesCarryWhere`, `TestDeliveryReceiptKeepsWhere`
   and `dispatch-evidence.spec.ts` test it.
8. `hostagent` is a launch site as much as `api`: not touched; a managed
   run, and so a launch from a ticket, runs on the server today, and the
   package reaches an attempt as files in its worktree, which is where an
   attempt run elsewhere (C10, F) would read it.

## Open-core checklist

- [x] Article 1: no licence or plan check in `internal/dispatch`, the
  engine, the routes or the pages; the conditions are the data
  directory, the ledger and `crews.managed`, as for any managed launch.
- [x] Article 2: the whole of it, revisions, launches, the package, the
  evidence views, the delivery's verification, ships in the open-source
  build with no account.
- [x] Article 3: nothing is gated; there is no flag a fork could flip.
- [x] Article 4: the approval this item adds is required and free; every
  read is a `GET` the admin makes; the records are rows of the two plain
  stores in the data directory, exported whole by C6; stop, pause,
  cancel, retry and accept are the run's own, unchanged.

## The PRs

Each PR is one issue and one branch cut from `main`, a draft PR at its
first commit, the checks for its surfaces before every push, its body
ticking the open-core checklist (articles 1 to 4) and naming the agent
that made it; each leaves `main` working and green on its own (a store
operation without its route is reached by its tests, a route without its
page by the CLI and the tests, a page without its later parts says what
is not there yet). Parallel means the two touch no common Go package, no
common directory under `web/`, and no common file under `docs/` or at the
root. Besides the order below, some PRs wait for a DS1 PR in flight,
whose files they build on: PR-6 for DS1's PR-12 (#412: the session ticket
routes, the route list and `docs/protocol.md`), PR-10 for DS1's PR-9 to
PR-11 (#409 to #411: the palette's Tickets group, the ticket page's
comments and attachments, `DispatchRuns.vue`, the close policy and the
delivery form), PR-13 for #412 (`conductor dispatch`), PR-14 for DS1's
PR-13 (#413: its features entry and README section), and PR-5 for EN1's
PR-5 (#397, verify and cleanup): `main` refuses a manifest's `verify`
steps (`Manifest.runnable`, `manifest.go`) and `verify.go` runs only a
task's checks, so `TestAFailedVerifyStepReachesTheNextAttempt` can pass
only once #397 runs them; both change `internal/crew`'s `verify.go`, so
PR-5 is cut from `main` after #397 merges, never beside it. Sizes are estimates
of the first push's changed lines with tests; DS1's store and route PRs,
estimated at 1,000 to 1,500, merged at 2,000 to 5,300 after their review
rounds (#415, #420, #423, #425, #427, #430, #439), and these will grow the
same way, which is why none is planned larger.

| PR | Title | Touches | Depends on | Parallel with | Size |
|---|---|---|---|---|---|
| PR-1 | dispatch: schema version 2, a ticket's plan revisions proposed, approved and withdrawn, and an operation replayed before its route works | `internal/dispatch` | none | PR-3, PR-4, PR-5 | ~1,500 |
| PR-2 | dispatch: launches, the package, the pinned attachments, what changed since, the reserved decision kind | `internal/dispatch` | PR-1 | PR-3, PR-4, PR-5, PR-6 | ~1,400 |
| PR-3 | ledger, crew: a run launched from a plan revision at its base, under its minted id, with its package, which continuations carry | `internal/crew`, `internal/crew/ledger`, `docs/repository-map.md` | none | PR-1, PR-2 | ~1,000 |
| PR-4 | crew: the package's files in every attempt, and a package that cannot be read pauses the run, uncounted | `internal/crew` | PR-3 | PR-1, PR-2, PR-6 | ~1,000 |
| PR-5 | crew: a failed check's receipt and its whole log reach the next attempt | `internal/crew`, `internal/crew/ledger`, `docs/protocol.md` | PR-4, EN1's PR-5 (#397) | PR-1, PR-2 | ~800 |
| PR-6 | api: plan revisions over HTTP | `internal/api`, `docs/protocol.md` | PR-1, PR-3 | PR-2, PR-4 | ~1,200 |
| PR-7 | api: a launch from a ticket, its reconcile at start, and the attempt's files from Dispatch's store | `internal/api`, `docs/protocol.md`, `docs/architecture.md` | PR-2, PR-5, PR-6 | none | ~1,400 |
| PR-8 | api: a ticket's runs, a run's candidates with their receipts, and the attempt's ticket as launched | `internal/api`, `docs/protocol.md` | PR-7 | PR-10 | ~1,300 |
| PR-9 | dispatch, api: a delivery names its candidate, and the close policy's verified | `internal/dispatch`, `internal/api`, `docs/protocol.md` | PR-8 | PR-10, PR-11 | ~900 |
| PR-10 | web: the Plan section, proposing, approving and launching from a ticket | `web/app`, `web/e2e` | PR-7 | PR-8, PR-9, PR-13 | ~1,500 |
| PR-11 | web: a ticket's runs, their candidates, receipts and repairs, and what changed since | `web/app`, `web/e2e` | PR-10, PR-8 | PR-9, PR-13 | ~1,500 |
| PR-12 | web: a delivery's candidate, verified, the decisions and the people | `web/app`, `web/e2e` | PR-11, PR-9 | PR-13 | ~1,100 |
| PR-13 | cli, mcp, skill: `conductor dispatch plans`, `plan`, `launch` and `runs`, and the attempt's ticket as launched | `internal/cli`, `internal/agents`, `README.md` | PR-9 | PR-10, PR-11, PR-12 | ~1,100 |
| PR-14 | docs: the features entry, the README, the architecture, the repository map, the by-hand record | `docs`, `README.md` | every PR above | none | ~300 |

**PR-1** (`internal/dispatch`): schema version 2 whole (the three tables,
the three link columns, the indexes) and `migrations[1]`; `model.go`'s
bounds and types for revisions (launches' types land with PR-2);
`plans.go`'s `ProposePlan` (each handed-over comment's digest set from
its row), `ApprovePlan`, `WithdrawPlan`, `Plans`, `Plan` and
`IntentDigest`, a stale approval for an edited handed-over comment (and
its approval refused, Revise the remedy); the revisions' `fingerprint`;
`ops.go`'s `Store.Replay`, with its pruned path through the create row
and the record's `fingerprint` (PR-6's proposing route replays with it,
so it lands here, not with the launches), and a `link.add`'s
`fingerprint` written on its link row and its add row's value by
`AddLink` and compared by `Replay` for a pruned add; the
entities `plan` and `launch` with their `rev`;
`rules.go`'s classes for every new field; `TicketView`'s `approved` and
`launchCount` (0 until PR-2). Tests: `schema_test.go`, `plans_test.go`,
`ops_test.go`'s two replay tests for a proposal, `rules_test.go`,
`model_test.go`. Go only.

**PR-2** (`internal/dispatch`): `launches.go`'s `Launch` with its checks,
the run id's mint and the pending row, `StartedLaunch`, `FailedLaunch`,
`Launches`, `LaunchByID`, `PendingLaunches` and `ChangedSince`;
`package.go`'s `Package`, its build and its bound; `sweep.go` keeping the
pins, and `OpenLaunchFile`; `links.go`'s reserved `decision` kind and
`SetRunTicket`, `SetRunChain` and the chain rule on `link.add` and in
`releaseRuns`, `ChainHolders` and a contested chain kept as version 1
left it; the package's `plan.agents`; the attachments' derived file names; the comments'
digests checked at a launch; the launches' `fingerprint`, which PR-1's
`Store.Replay` reads for a pruned launch. Tests:
`launches_test.go` with the replay tests' launch cases, `package_test.go`,
`sweep_test.go`, `links_test.go`. Go only; the Mac and Windows preflight
rows run here.

**PR-3** (`internal/crew/ledger`, `internal/crew`, the repository map):
the ledger's next version (`runs.ticket`, `runs.package`, `MaxPackage`)
and `Tx.CreateRun`, the insert-only first write, on the engine's
`LedgerTx` too, made the first write of every new run (an ordinary
launch's and a continuation's, re-minted through `newRunID` on
`ErrRunExists`), `PutRun` kept for later transitions; `planned.go`'s `Planned` and `LaunchPlanned` (the
requested id, refused by that insert when the ledger has it, the base,
the digest); `package.go`'s reading of a package and its header;
`crew.ResolveManifest`, the exported `resolveManifest` that PR-6's
proposal snapshots a file-form manifest with;
`Run.Ticket`; `ContinueRun` carrying the package; `run.created`'s
payload; the repository map's ledger row. No route calls it yet; the
engine's tests drive it. Tests: the ledger's migration and bound,
`TestCreateRunRefusesAnIDItHas`, `planned_test.go` with
`TestAnOrdinaryLaunchNeverOverwritesAPlannedRun`. Go only.

**PR-4** (`internal/crew`): `ticket.md`'s rendering; `UsePackageFiles`
and the copy with its hash and size; `writeTaskFiles`' package files, a
continuation's attempts' among them; `task.json`'s `ticket` and
`task.md`'s section; the `package` stage,
uncounted, pausing the run; `LaunchSpec.AgentSHA256` from the package's
`plan.agents` and the `agent` stage for `ErrAgentChanged`, uncounted,
pausing the run. Tests: `package_test.go`, `taskfile_test.go`.
Go only; the Mac and Windows preflight rows run here.

**PR-5** (`internal/crew`, `internal/crew/ledger`, `docs/protocol.md`),
after EN1's PR-5 (#397): the failing receipt kept on the attempt with its
changed paths; `k` in `check.finished`'s payload and the ledger's
`CheckChanged`; `previous.check` and the log file; the engine's
`Ledger.CheckLog` and `Loaded.FailedChecks`, which `sqlLedger` reads with
the ledger's existing `Check` and `Checks`; `previous.check`'s receipt
reference with its source run; the cut order, the tail, `changed` and
`argv` into `task-more.json`; the Crew runs
section's paragraph on the task files. It changes every managed run's
next attempt, launched from a ticket or not. Tests: `taskfile_test.go`'s
three, the log read through `sqlLedger`, `ledger_bounds_test.go`'s
`TestAFailedCheckHandoffAtItsWorst`. Go only.

**PR-6** (`internal/api`, `docs/protocol.md`): `dispatch_plans.go`, its
four routes and the proposing checks (the replay first, the crew, the
agents as resolved with their recipes' parts (`agentSelection`, `agents`
with each entry's `sha256`, `agentsChanged`), its
directory, the base, the references, the manifest's file and its
manifest through PR-3's `crew.ResolveManifest`, the yolo choice, the
body's encoded bound), `crewChanged`; the approve route's refusal when
an agent resolves otherwise; the route list; `events.go`'s comment; the
protocol's rows and its "Plan revisions" paragraph. Tests:
`dispatch_plans_test.go`, `events_test.go`. Go only.

**PR-7** (`internal/api`, `docs/protocol.md`, `docs/architecture.md`):
`dispatch_launch.go` (the route in its order, the replay and the
ledger's ownership before any launch advances, the agents resolved
again, `runRowIsLaunch`, `launchReady`, `launchesInFlight`,
`reconcileLaunches`, `GET /api/dispatch/launches/{id}` under
`MaxLaunchReply`); the wiring in
`New` (`UsePackageFiles`, `SetRunTicket`, `createLocalSession`'s check of
`LaunchSpec.AgentSHA256` against the entry it starts, the reconcile once the ledger
is the server's, run after `recoverLedger` through `openLedger`'s new
`afterRecover`, inside the one `OnOwned` callback when the lock comes
late); the protocol's "Launches" paragraph, the package's
shape and the attempt's files in its Crew runs section; the
architecture's Dispatch section. Tests: `dispatch_launch_test.go`, with
`TestEditingTheTicketNeverRewritesAnActiveRun` and
`TestAnAttemptStartsOnlyTheApprovedAgent`. Go only.

**PR-8** (`internal/api`, `docs/protocol.md`): `dispatch_evidence.go`
(`GET /api/dispatch/tickets/{key}/runs`, paged), `candidates.go` (`GET
/api/runs/{run}/candidates`, paged by task, each receipt's `where`, a
legacy run resolved as the run route resolves it), the session route's
package-first lookup and `launch` part, a contested chain marked; the
protocol's rows. Tests: `dispatch_evidence_test.go`. Go only.

**PR-9** (`internal/dispatch`, `internal/api`, `docs/protocol.md`): a
delivery's `candidate` and `verified` (each receipt's `where` in it),
their shapes in the store, `AddLink`'s `verified` argument outside the
fingerprint, the route's replay before the ledger is read, and the API's
read of the receipt for a new operation, a contested chain's candidate
refused; the close
policy's `verified`; the protocol's
rows for the link and the policy. Tests: `links_test.go`,
`acceptance_test.go`, `dispatch_test.go`. Go only.

**PR-10** (`web/app`, `web/e2e`): `useDispatch.ts`' revision and launch
calls; `utils/dispatchPlans.ts`; `DispatchPlan.vue`,
`DispatchPlanPropose.vue`, `DispatchPlanApprove.vue`,
`DispatchLaunch.vue`; the ticket page's Plan section; the palette's rows;
`stub-agent.sh`'s ticket line. Tests: vitest `dispatchPlans.test.ts`,
`useDispatchLive.test.ts` additions; Playwright `dispatch-plan.spec.ts`
and the `palette-rows.spec.ts` additions. UI only.

**PR-11** (`web/app`, `web/e2e`): `ticketRuns` and `runCandidates`;
`utils/dispatchEvidence.ts`; `DispatchRuns.vue` extended,
`DispatchChangedSince.vue`, `DispatchCandidates.vue`,
`DispatchReceipt.vue`, `DispatchRepairs.vue`; Show older and More
tasks following the pages' `next`; where each receipt ran; a review's
findings read from the task route; the run page's "Launched from"; the
phone's width. Tests: vitest `dispatchEvidence.test.ts`;
Playwright `dispatch-evidence.spec.ts`. UI only; the Mac and Windows
preflight rows run here.

**PR-12** (`web/app`, `web/e2e`): the delivery form's candidate, the
delivery row's verification, `verified` in the project dialog and on the
ticket's close policy, `DispatchDecisions.vue`, `DispatchPeople.vue`.
Tests: vitest `dispatch.test.ts` additions; Playwright
`dispatch-delivery.spec.ts`. UI only.

**PR-13** (`internal/cli`, `internal/agents`, `README.md`): the
`conductor dispatch` commands, `conductor task ticket`'s launch part,
`ticket_read`'s text, the skill's section, the usage and the completion
table; the README's CLI lines. Tests: `cli/dispatch_test.go`,
`cli/task_test.go`, `cli/mcp_test.go`, `agents/skill_test.go`. Go only.

**PR-14** (docs): the `docs/features.md` entry recording what each PR
verified and the by-hand checks; the README's Dispatch section; the
repository map's `internal/dispatch` row; the acceptance observation
below once it is made, else the entry says it is pending. Docs only; no
check applies.

## Owner decisions

Dated 2026-10-30. Each is a default the owner can overturn with a dated
row in the Program's status log, its convention for a decision the owner
did not make (the Program, section 3.1); the reasoning is why it was
taken.

1. **The open decisions named with this item are not its own.** O9 (where
   the Boxcar code lives) blocks item F, O12 (the seams' package names)
   blocks C1, and O16 (where the UI of paid features lives) blocks CG1's
   and AU1's pages; this item needs none of them, settles none, and
   waits on none.
2. **O10's rules, extended.** The classes in "Field rules for
   replication" are this item's fields' merge rules, as DS1's plan lets
   this stream revise them; two revisions approved at once on two
   replicas are kept, not reconciled, since a launch names its revision
   and nothing needs undoing.
3. **A revision takes a saved crew with tasks, whole.** No crew is edited
   inside a revision, and a crew without tasks is not planned (DS1's hand
   link still links its runs): the approval then covers exactly what
   runs, and the crew editor stays the one place a crew changes. Chosen
   over an editor inside the plan, which would be a second crew editor
   with its own validation.
4. **A revision never changes, and its number is a place.** Revise makes
   the next one; `n` is derived at read as a comment's is, and a revision
   is addressed by its id, so replication renumbers no stored row.
5. **The approval is the operator's, against the ticket's intent.**
   Decision D4's single approver; it records the digest of the title,
   the description and the criteria, and a launch needs that unchanged,
   with each handed-over comment's body and each agent as resolved
   (`agents`) as the revision recorded them; other edits leave it
   holding; after an intent edit approving again is one action, and after
   a handed-over comment's edit or an agent's change Revise is, since
   those are the immutable body's. Chosen
   over staling on every edit, which would block a launch for a priority
   change, and over never staling, which would launch against an
   approval of other criteria.
6. **The input revision is pinned when the revision is made**, a commit
   of the crew's working directory; a newer `HEAD` is a new revision,
   since the approval covers the inputs (the Program, section 1.4).
7. **The yolo choice is fixed in the revision** (the crew's, else the
   server's default then) and said plainly on the plan and in the launch
   dialog: running agents without their permission prompts is what an
   approval most needs to show.
8. **The package**: its parts and bounds (512 KiB, 640 KiB since the
   package's version 2, Program item IN1a; 8 attachments and 16
   MiB; 20 references, 20 comments, 10 decisions, 20 blockers), kept in
   Dispatch, the ticket's record of what was launched, and in the ledger,
   the run's input that continuations carry, the two byte for byte by its
   digest, and written into every attempt as `ticket.md`, `package.json`
   and `attachments/`. Chosen over Dispatch alone, which would leave a
   retry or a continuation depending on Dispatch's store being open, and
   over the ledger alone, which would leave a failed launch, and a
   replica, with no record of what was launched.
9. **A launch is recorded before it runs**, pending, under a run id
   Dispatch mints, and settled by what the ledger holds, so neither a
   resend nor a restart makes a second run: the typed operation with a
   receipt the Program asks of every client (section 1.5).
10. **A launched run is bound by its package, not by a link.** It gets no
    `run` link, a run with a package cannot be linked by hand, and its
    continuations carry the binding; DS1's hand links stay for runs not
    launched from a ticket. One binding, nothing to reconcile.
11. **Evidence is read through the launch and never copied; a delivery's
    verification receipt is the one fact recorded**, when a person names
    the candidate, so that the close policy is a function of Dispatch's
    rows, on a replica too.
12. **`verified` is defined**, as DS1 left to this item (DS1's plan, Owner
    decisions, 6): a live delivery link naming a candidate whose receipts
    all passed on its commit; a candidate with no check is not verified,
    and `verified` implies a delivery.
13. **Link kinds.** `decision` is reserved for DR1, refused on a local
    write and kept by a merge. DS1's model foresaw `plan`, `candidate`,
    `check` and `finding` kinds, which this item does not add: a revision
    is a record of its own, a candidate is named by the delivery that
    delivers it, and checks and findings are the ledger's, read through
    the launch.
14. **Agents still read and comment only** (DS1's decisions, 13). The
    operations take any actor and an approval refuses all but the
    operator's, so Brakeman's drafts (BK1) become work only through the
    operator's approval and launch.
15. **A failed check's receipt and whole log reach the next attempt** of
    every managed run, launched from a ticket or not; RV1's failed-check
    action, for a person and for a CI job, builds on it.
16. **One new ledger version**, the next number when PR-3 lands (6, or 7
    after C10's), conservative.
17. **No status moves by itself.** A launch, a run's outcome and a
    delivery never change a ticket's status: they are separate facts
    (the Program, section 1.4).
18. **The words.** "Plan revision", shown as "Plan" on a ticket page and
    as `r3` beside a run, never "plan" alone where the agent's plan or
    `docs/plans/` could be meant; a launched run's label is the ticket's
    key and title unless the person gives one. (As built since issue
    770: the key alone when the ticket's title is quoted.)
19. **Required decisions are recorded and shown, not held**, until DR1's
    durable decisions hold the work that waits on them; the page says so
    beside them.
20. **When the code starts.** DS2's PRs start once the DS1 PRs each one
    builds on are merged, before DS1 is Done: DS1's Done waits on a week
    of use, which DS1's plan lets be observed on the stream that follows
    it, this one among them. DS2's own Status follows the Program's
    rules: In progress at its first PR, Done when the boundary below is
    observed.

No question is left open: each answer above lets the plan stand, and the
owner overturns any of them with one row.

## Acceptance

The boundary, in this plan's words: a ticket shows its approved plan
revision, every run made from it, each exact candidate with its check
receipts and its review findings, and the delivery; and an edit of the
ticket never changes a run in flight. It is observed, not ticked:

- Once PR-12 and PR-13 are merged, on the owner's `conductor serve` with
  real agents on the owner's own subscriptions: a native project (a
  scratch repository both agents trust, as #350's runs used, or a small
  change of this repository's own) and a real ticket with acceptance
  criteria, an owner, an image attachment and a source reference; a
  managed crew with a work task and two checks, one of which fails on its
  first run, and a review task by the other agent; revision 1 proposed,
  approved with a note, and launched.
- Mid-run: the run paused, the ticket's description edited and a comment
  added, the run unpaused; the next attempt's `.conductor/ticket.md` is
  the one launched (its `package.json` hashing to the launch's digest),
  the ticket page names the change, and a follow-up launch is refused
  until revision 1 is approved again.
- After the run: the ticket page shows revision 1 approved with its note;
  every run (the launch, and its continuation if the server was
  restarted mid-run as #350's third crew was); each candidate with its
  receipts, the failed one and the repair attempt that started at its
  commit, each receipt's log hash equal to the step log route's
  `X-Conductor-Log-SHA256`; the review's findings and the repair that
  followed them; a pull request opened from the final candidate's branch
  added as a delivery naming that candidate, verified; the ticket closed
  under a policy of acceptance, delivery and `verified`.
- The observation, and what was missing, slow or wrong, goes into
  `docs/features.md` (PR-14 when it comes before that PR, else a later
  docs PR of the stream); a bug the stream caused is fixed in the stream,
  a defect outside it filed.
- Done means: every PR merged and the observation recorded with the
  owner's word that the ticket page answered what was asked, what was
  authorized, what ran, what was checked on which commit and what was
  delivered; the Program's status row moves to Done then, never on the
  table alone. The R1 release gate is more than this item (RV1's review,
  DR1's decision, MB0's page).

What needs the owner: the review of this plan and its decisions; cutting
the fourteen issues; the observation, whose agents run on the owner's
subscriptions; the merges.
