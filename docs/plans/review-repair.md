# Review and repair: a person's batched review of a candidate, one repair round, findings kept across revisions (Program item RV1)

A managed run's candidate (a task's result commit, with the receipts of
the checks that ran on it) is shown on its ticket page since Program item
DS2, and an agent's review task can already send it back: a review's
`needs_changes` reopens the task it reviewed, and its next attempt starts
at the reviewed commit with the findings in its task file. A person
cannot do the same. A person's only way to say "change these lines" is
the Files tab's Comment and Ask the agent (#58's F7), which types words
into whichever session is open, says nothing of which commit was read,
and leaves no record a later candidate can be checked against. This plan
gives the person the review the agent already has, and more: a **review**
of one exact candidate against a named base, made of notes on lines,
ranges, files and failed checks gathered in a draft and sent as **one
batch**; **Request changes** applied by Yardmaster as a repair
transition of the task that made the candidate (or, for a run the server
no longer runs, as the continuation that carries the repair); a
**failed-check request** prepared from a receipt with everything the
repair needs; **findings** that stay open across candidates until a
person marks each one against a later candidate; and a **Review view**
on the ticket that puts the requirements, the plan, the candidate's
diff, its checks, the findings, the decisions and the delivery side by
side. Nothing merges, pushes or closes by itself.

It is Program item RV1 (the Program, section 4.3), written against
`main` at 9723fdc6 (PR #601, 2026-11-05) after reading the engine's
review gate and withdrawal (`internal/crew/task.go`: `accept`, `reopen`,
`withdraw`, `startCommit`), its commands with receipts
(`internal/crew/commands.go`), the task file (`taskfile.go`,
`failedcheck.go`), continuations (`continue.go`), the ledger
(`internal/crew/ledger`, schema version 7), Dispatch's store (schema
version 2: plan revisions, launches, a delivery's candidate,
`delivery.go`, `links.go`, `rules.go`), the API's task, candidate and
launch routes (`internal/api/tasks.go`, `candidates.go`,
`dispatch_launch.go`, `dispatch_evidence.go`), git for the Files tab
(`internal/gitcli`, `internal/gitrepo`), and the pages that show
candidates and comments (`web/app/components/DispatchCandidates.vue`,
`DispatchRepairs.vue`, `DispatchTicketPage.vue`, `DiffEditor.vue`,
`utils/quote.ts`, `utils/dispatchEvidence.ts`). It is reviewed before
any of its code; the PRs at the end are cut into issues after the
review, one issue, one branch `issue-N`, one PR each (AGENTS.md).

The names follow the railroad (the Program, section 1.2): Conductor is
the application, Yardmaster the agent harness that owns a run's tasks,
attempts, checks and repairs, Dispatch the ticketing, whose items are
tickets. Three words are kept apart here: a **review** is a person's
batch of notes on one candidate (a Dispatch record, new); a **review
task** is a crew's task of kind `review`, done by an agent (Yardmaster's,
on `main`); a **finding** is one note of either, but only a review's
findings have an identity of their own and a state across candidates.
"The review and delivery view" is called the **Review view** in the app.
A worktree is never a "workspace" (decision D5).

## Articles it serves

1. No licence check: nothing here asks for a plan, a tier or a
   provider; the conditions are those of a managed run (the data
   directory, the ledger, `crews.managed`) and of Dispatch.
2. Useful on its own: review and repair are free work on the local
   Dispatch store and the ledger, with no account and no network; the
   Program names review and CI repair among what the free build must do
   (the Program, section 2.1, article 2).
3. Nothing is gated by a flag: there is no paid part, and no switch.
4. Required approvals, stop, inspect, revoke and export stay free and
   never degrade: a review is a person's decision recorded with its
   receipt; Request changes needs the admin's token and refuses an agent's
   session token, so an agent can never ask for its own repair through
   it; every record is a row of the two plain stores of the data
   directory, carried whole by C6's export; stop, pause, cancel, retry
   and accept are the run's own, unchanged.
5. Dependencies point one way: no new module; `internal/dispatch` still
   imports the standard library and `modernc.org/sqlite` alone, and
   `internal/crew` never imports `internal/dispatch` (the API joins them,
   as DS2 does).
6. No feature build tag: a plain `go build` carries all of it.
7. The free defaults stand: no extension interface is called to decide
   anything. The actor of a review is the one C2's identity seam already
   gives every receipt (`actorOf`: `admin`, or `user:<subject>` when a
   provider recognises the token); Dispatch's new entities reach a
   registered sync store through the existing adapter (`api/sync.go`,
   one reference per entity a commit changed), unchanged; a check run
   again goes through the run's Runtime like every check.
8. Not touched: no hosted dependency exists in this item, so nothing
   waits on a service, and nothing degrades without one.
9. `CGO_ENABLED=0` stays: both stores stay `modernc.org/sqlite`, and
   the diffs are read with go-git and the git binary as the Files tab
   reads them.
10. The repository contract holds: this plan before the code; race tests
    per package; vitest and Playwright for the pages; no new dependency,
    so `make notices` has nothing to regenerate; no frame or control
    message, so `internal/proto` and `web/app/utils/protocol.ts` are
    untouched, and `docs/protocol.md` gains the HTTP rows, the task
    file's new parts and two `dispatch` event entities, each with its
    bound.
11. The railroad names keep their meaning: tickets and reviews are
    Dispatch's; runs, tasks, attempts, candidates, receipts, repairs and
    continuations Yardmaster's; "review", "review task" and "finding" are
    told apart above.
12. Execution stays behind one seam: a repair is the engine's next
    attempt, dispatched through `crew.Launcher` like every attempt; Check
    again verifies a candidate through the run's Runtime like every
    check; "Boxcar constraints" below takes the eight one by one.

## Why and what happens

The Program's work model (section 1.4) keeps a review finding as a record
of its own, tied to the candidate it was made on, and keeps verification,
human acceptance and delivery as separate facts; section 1.5 asks that
every client change a run only through typed operations that name the
actor, the target, the expected state and an idempotency key and answer
a durable receipt, and that nobody adds a second way to type into a
session. On `main`:

- A review task's `needs_changes` is the only repair a review can cause:
  `Engine.reopen` drops the reviewed task's result, sets its `repair`
  (the review's id, attempt, the commit it reviewed, its summary and up
  to 50 findings of severity, path, line and message), withdraws what
  rested on the result (`withdraw`), and the task's next attempt starts
  at the reviewed commit (`startCommit`) with "Findings to address" in
  `task.md` and `repair` in `task.json`.
- A failed check's receipt, its last 2 KiB and its whole log reach the
  next attempt (DS2's PR-5, `failedcheck.go`), and that attempt starts
  at the failed result. When the task's three attempts are spent it
  fails, and a person has Retry (one more attempt, the same start) and
  Accept anyway, but no way to say what the repair should do, or that
  the failure was the machine's and the same commit should be checked
  again.
- A person's comment on lines (F7) is a chat message with a `quote`
  (`path`, `from`, `to`, at most 12 lines of 200 bytes), typed into a
  session as text (`AgentText`); its card says "Lines moved since" or
  "Lines changed since" against the file as it is now. It names no
  commit, keeps no state, and cannot reach a run's next attempt except by
  being typed into a live session, which a managed run's attempts do not
  take as an instruction.
- The ticket page lists each run's candidates with their receipts, a
  review task's verdict and findings, and the repairs that followed
  (`DispatchCandidates.vue`, `DispatchRepairs.vue`), but shows no diff.

After this item ships, on a ticket page (`/dispatch/CND/12`) a person
sees a **Review** button beside each standing candidate, and the page's
**Review view** (`?view=review&candidate=…`):

- **Requirements and plan.** The ticket's acceptance criteria as the run
  was launched with them (the package's, DS2) and as they are now, the
  approved plan revision that launched the run, its expected checks.
- **The candidate and its lineage.** A picker of the ticket's
  candidates, newest first per task; for the one shown: the commit, the
  task, the attempt and the member's agent with the version it reported
  at the launch, the run (and the continuation it is in), the plan
  revision, and how it came to be: "attempt 3 of `build`, the repair of
  review R-2 by Nate on attempt 2 (a1b2c3d)", "attempt 2, after check
  `test` failed on attempt 1". Change provenance stops at the attempt:
  the page never says who wrote a line.
- **The diff.** The candidate against its base (the run's base by
  default; the attempt's start, or an earlier candidate of the same task
  for "what changed since I last looked"), file by file in Monaco's diff
  editor, with each file's counts and its findings marked in the margin.
- **A draft.** Selecting lines on either side shows a bar under the
  selection with **Add a note** (the F7 bar's place and keys); the note
  takes its intent (a change asked for, a question, a note), a severity
  for a change, the words and, optionally, the resolution asked for. A
  note may also be on a whole file or on the candidate as a whole.
  Notes gather in the draft panel ("3 notes"), kept in this browser
  until sent, and **Send review** sends them as one batch with a verdict:
  **Request changes**, **Comment** or **Approve**, and a summary.
- **What became of it.** A sent review says, as separate facts, that it
  was **recorded** (in Dispatch), that Yardmaster **applied** it (the
  repair's receipt: the run, the task reopened, attempt 3 to start at
  a1b2c3d) or **refused** it and why ("the task's result is now attempt
  4: review that candidate"), and what followed (the repair attempt, its
  new candidate, its checks).
- **Findings across candidates.** Every finding of every review of the
  ticket, open ones first, each with the candidate it was made on and
  where its lines are on the candidate shown ("same lines", "moved: now
  41–44", "the lines changed", "the file is gone", "renamed to …"),
  what the repair attempt said of it ("the agent says: done, renamed the
  field"), and **Resolved** / **Still open** / **Withdraw** marks a
  person makes against the candidate shown. A line that moved or
  vanished is said so and never counts as resolved.
- **Checks.** The candidate's receipts (DS2's chips and receipt dialog);
  an old candidate keeps its own receipts, a repair's new candidate has
  its own. On a failed receipt, **Prepare a repair** opens a draft note
  of kind check with the tested commit, the check's identity, the last of
  its output, the log's link and hash, the attempt before, the
  acceptance criteria, and a class the server worked out from the
  receipt (code defect, infrastructure failure, possibly flaky,
  cancelled, evidence unavailable) that the person may change. The panel
  offers **Check again** (the same commit checked again, nothing changed)
  whenever the engine would take it, as the prepared request says
  (`recheck`), whatever the class: first for an infrastructure failure or
  a possibly flaky check, beside Request changes for the rest; where the
  engine would not take it (a run the server does not run, a task that
  has moved on), the panel says why and what does instead ("Continue the
  run first", "review attempt 4's candidate").
- **Decisions and delivery.** DS2's Decisions section (approvals,
  acceptances, Accept anyway receipts, and DR1's decision records when
  DR1 adds them there) and the delivery links with their verification;
  an Approve never accepts the ticket, adds a delivery or closes anything.

A repair attempt's agent finds the person's findings in its task file,
each with its id, its lines and the resolution asked for, and may say
which it addressed in its result (`task_result`'s `addressed`, `conductor
task result --addressed ID[=NOTE]`); that is a claim shown beside the
finding, never its resolution. The run page's task panel has the same
Review and Prepare a repair entries for a run that belongs to a ticket.
`conductor dispatch review`, `findings` and `finding mark` do from the
CLI what the page does. On a phone the Review view is one column: the
findings, their marks and each file's inline diff; a note on lines is
made with the file and line numbers typed, since Monaco's selection is a
desktop gesture.

## The changes

### `internal/dispatch`: reviews, findings and marks (schema version 3)

A review belongs to a ticket and names one candidate in DS2's form,
`<run>/<task>/<n>@<commit>` (`dispatch.Candidate`, `ParseCandidate`).
Dispatch does not know runs: the API checks that the ticket holds the
run's chain (`ChainHolders`, DS2) and that the commit is the attempt's
result before it calls the store, as it does for a delivery's candidate.

**Tables** (`schema.go`, created by the migration and in the version 3
schema whole):

| Table | Columns |
|---|---|
| `reviews` | `id` (16 hex, `newID`), `ticket_id`, `candidate`, `base` (a full commit), `verdict` (`request_changes`, `comment`, `approve`), `summary`, `created_at`, `created_by` (the display name, else the actor), `actor`, `via` (`web`, `cli`, `brakeman`), `fingerprint`, `app_state` (`none`, `pending`, `applied`, `refused`), `app_try`, `app_run`, `app_command`, `app_revision`, `app_attempt`, `app_why`, `app_at`, `rev` |
| `review_findings` | `id`, `review_id`, `ticket_id`, `k` (1 to 50, its place in the batch), `kind` (`line`, `file`, `general`, `check`), `intent` (`change`, `question`, `note`), `severity` (`blocker`, `major`, `minor`, `nit`, for a change; empty otherwise), `path`, `side` (`candidate`, `base`), `from_line`, `to_line`, `anchor_commit`, `lines` (JSON: the lines as the person saw them), `message`, `resolution`, `check_ref` (JSON, below), `created_at` |
| `finding_marks` | `id`, `finding_id`, `ticket_id`, `candidate`, `verdict` (`resolved`, `open`, `withdrawn`), `located` (JSON: what the page showed of the lines on that candidate), `note`, `created_at`, `created_by`, `actor`, `via`, `fingerprint` |

Indexes: `reviews (ticket_id, created_at)`, `reviews (app_state)`,
`review_findings (review_id, k)`, `review_findings (ticket_id)`,
`finding_marks (finding_id, created_at)`; none unique beyond the ids, so
no index refuses a replicated row (DS1's convention).

**Bounds** (`model.go`, each tested at its edge as DS1's are):
`MaxReviews` 500 per ticket; `MaxReviewFindings` 50 per review (the
engine's `maxFindings`); `MaxReviewSummary` 4,000 characters (the
engine's `maxSummary`); `MaxFindingMessage` and `MaxFindingResolution`
1,000 characters each (the engine's `maxFindingText`); `MaxFindingPath`
1,024 bytes; `MaxAnchorLines` 12 and `MaxAnchorLine` 200 bytes (the F7
quote's bounds, numbers kept apart from `internal/proto`, which Dispatch
does not import); a line number 1 to 10,000,000 and `to_line` not before
`from_line`; `MaxMarks` 50 per finding; `MaxMarkNote` 1,000 characters;
`MaxCheckExcerpt` 2 KiB (the ledger's tail, and a check finding's
`quote`); `MaxReviewTries` 5; `MaxReviewDetail` 512 KiB, a review with
its findings as its detail reply encodes them (Go's JSON, its escapes
counted: a `<` is six bytes), measured at the submit. Every
text is checked by DS1's field rules (`checkField`: no control character
but newline and tab in the long texts).

**`check_ref`** is a check finding's receipt as the server read it from
the ledger, with the person's choice and quote beside it: `{source,
receipt, run, task, attempt, phase, id, k, subject, argv, exit, signal,
timedOut, killed, stable, where, logSha256, logBytes, tail, class,
chosen, quote, previous}`. `source` is `ledger` (a Yardmaster receipt,
`receipt` its identity `<run>/<task>/<n>/<phase>/<id>/<k>` as
`previous.check` names it); `external` is reserved for a CI job a linked
pull request reports (Program item IN1a, `docs/plans/tracker-linking.md`)
and refused on a write until that item defines it. A client sends only
`source`, `receipt`, `chosen` and `quote` (any other member is an unknown
field); the API fills the rest from the ledger's row of that identity, as
DS2's delivery reads its verification from the ledger rather than from
what a client sent (`candidateVerified`), and refuses a receipt that is
not the review's candidate's: its run, task and attempt the candidate's,
its `subject` the candidate's commit, finished and failed (`409 invalid`,
"the receipt …/build/2/check/test/1 is of attempt 2 (a7c…), not of the
candidate reviewed"). `class` is the server's (below) and `chosen` the
person's; `previous` the attempt before the failing one (`{attempt,
state, stage, commit}`), read from the ledger too. `tail` is the
receipt's last 2 KiB as the ledger keeps it; `quote` is what the person
chose to quote of the output, kept and shown as theirs, never taken for
the evidence; the log itself stays the ledger's, opened by the receipt's
identity, its hash compared with the log's.

**Operations** (`reviews.go`, new; each a typed operation with DS1's
`Op`, its receipt kept, a resend answered by `Replay` through the row's
`fingerprint` once the receipt is pruned, as DS2's proposals and
launches are):

- `SubmitReview(ctx, op, key, NewReview) (Receipt, Review, error)`
  (`review.submit`): the review and its findings in one transaction,
  each finding its own entity with its create row; a `request_changes`
  review is made with `app_state` `pending` and `app_try` 1, any other
  with `none`. Refused: an archived project (`archived`), a
  `request_changes` on a closed ticket (`invalid`, "reopen the ticket to
  ask for changes", as DS2 refuses a launch from one; a comment or an
  approval of a closed ticket's candidate is kept), a review past
  `MaxReviews`, no finding and no summary, a
  `request_changes` with no finding of intent `change` (nothing to
  repair), a finding whose kind and fields disagree (a `line` with no
  path or lines, a `general` with a path, a `check` with no `check_ref`),
  an `external` source.
- `ApplyingReview(ctx, op, id) (Receipt, Review, error)`
  (`review.retry`): a refused `request_changes` review made `pending`
  again with the next `app_try`, refused past `MaxReviewTries` or for a
  review that is not refused.
- `AppliedReview(ctx, op, id, try, Applied)` and `RefusedReview(ctx, op,
  id, try, why)` (`review.applied`, `review.refused`): the application's
  outcome, written by the API as the ledger's owner; each takes the try
  it settles, so a late answer of an earlier try changes nothing.
  `Applied{Run, Command, Revision, Attempt}`: the run the repair was
  made in (the reviewed run, or the continuation made for it), the
  command id, the run's revision after it, the number of the attempt that
  will start at the candidate.
- `MarkFinding(ctx, op, id, NewMark) (Receipt, Mark, error)`
  (`finding.mark`): a mark of the finding against a candidate. Refused:
  a `resolved` mark on any candidate whose commit is the commit of the
  finding's own candidate, whatever its attempt (a Check again's attempt
  n+1, or a repair that committed nothing, holds the same code: "resolve
  it on a candidate that changed the code"; a question answered without a
  change is withdrawn with a note), a finding past `MaxMarks`, an archived
  project. That the mark's candidate is a later attempt of the finding's
  task in the ticket's chain is the API's check, which reads the ledger.
- Reads: `Reviews(ctx, key, before)` (a page of 20, newest first, each
  with its findings counted by state), `Review(ctx, id)` (whole, its
  findings, each with its marks counted and its latest mark),
  `Marks(ctx, findingID, after)` (a finding's marks, oldest first, a
  page), `Findings(ctx, key, filter, after)` (a page of at most 100:
  `open`, `resolved`, `withdrawn` or all, by candidate's task),
  `PendingReviews(ctx)` (for the reconcile). A page that is measured
  (marks, findings) ends early at its reply's bound with its cursor, and
  one entry at its bounds always fits one page, so every mark and every
  finding is reachable.

A finding's **state** is not stored: it is its latest mark's verdict in
the log's one order (`at`, `actor`, `op_id`, `n`), `open` with none. So a
merge has no state to reconcile and two marks made at once are both kept
and shown, the later in that order deciding.

**Field rules for replication** (`rules.go`, the merge rules DS1 lets this
stream extend; the Program's open decision O10, owned by CS1's plan):

| Class | Fields | Rule |
|---|---|---|
| immutable | a review's id, `ticket_id`, `candidate`, `base`, `verdict`, `summary`, `created_*`, `actor`, `via`, `fingerprint`; every finding field; every mark row | never written twice |
| log | `finding_marks` | append only, in the one order; a finding's state read from it |
| gated | a review's `app_*` fields | written only by the server that owns the run's ledger (the API applies, Dispatch records); a merge takes them as a scalar, last writer per field, and never applies a review again |

**Migration** (`prepare`, DS1's `BEGIN IMMEDIATE` transaction): version
2 to 3 creates the three tables and their indexes and reads or rewrites
no row; nothing is derived, so no `changes` row of `op: migrate`; a
version 3 database opens read-only under a DS2 binary, as every later
version does (the owner's conservative rule of 2026-10-25). `snapshot.go`
copies the new tables with the rest (the whole database), so C6's export
and restore carry reviews with no change to `internal/export`.

**Events.** The new entities `review` and `finding` reach the pages as
`dispatch` events by DS1's path, naming their ticket and project; the
longest event stays `milestone`'s 119 bytes, and `maxDispatchEach`'s
arithmetic becomes eleven kinds. The store only reports the changes
(`OnChange`, PR-1); the event's bound, `maxDispatchEach`'s comment and
their test are `internal/api/events.go`'s (PR-4).

### `internal/crew` and `internal/crew/ledger`: a person's repair, Check again, the failure's class

**A person's change request in the repair** (`task.go`, `taskfile.go`).
`TaskRepair` gains `Request *ChangeRequest` (JSON `request`, omitted for
a review task's repair, so every `repair` the ledger holds reads as it
did): `ChangeRequest{Review, By, Via, Base string; Changes
[]RequestedChange}` and `RequestedChange{ID, Kind, Intent, Severity,
Path, Side string; From, To int; Lines []string; Message, Resolution
string; Check *RequestedCheck}`, `RequestedCheck` the receipt's identity,
outcome, `class`, `chosen` and the excerpt, as Dispatch keeps them. The
engine's `repair` keeps the same; the task row's `repair` column holds it
as JSON, as it holds a review task's repair today, so no table changes;
the ledger's version moves to 8 only as a barrier against an older
binary (Migrations, below). `task.md`'s "Findings to address"
says, for a person's request: "Nate asked for changes to the commit
a1b2c3d (review R-2, against the base 9f8e7d6), where this attempt
starts", then each change with its id, severity, location
(`internal/api/users.go:14–16`, which side), the lines as the person saw
them in a fenced block, the words and "Asked: …" for the resolution; a
question is listed as one to answer in the summary, a note as context;
a check finding names the receipt, the class the person chose and where
its log is when `previous.check` wrote it. It ends: "Say which you
addressed with `addressed` in your result (each change's id, and a note);
a person checks each one on your result." `fitTaskFile`'s cut order cuts
the lines first, then the resolutions, then the messages past their first
200 characters, each cut said in `cut`; ids, kinds, locations and
severities are never cut.

**The size it may be.** `FitChangeRequest(def Task, cr ChangeRequest)
error` (exported) measures two things before anything is recorded
anywhere. First the repair as the task row keeps it, with the largest
attempt and commit, against `ledger.MaxPayload` (64 KiB), as
`Result.fitsLedger` does for a review task's findings. Then the repair
attempt's task file as `fitTaskFile` would write it: the task's own
goal, role and instructions (never cut, up to `maxTaskText`, 40 KiB), the
request after its cuts, and the largest parts an attempt's file carries
beside them (the attempt before, a failed check's tail, the inputs'
references at their bounds), against `maxTaskFile` (64 KiB) for
`task.md` and for `task.json` each. A batch past either is refused whole
("the notes are 71 KiB as the run keeps them, more than 64 KiB: shorten
them"; "with `build`'s own instructions the task file would be 70 KiB,
more than 64 KiB: shorten the notes or their paths"), so an accepted
request never fails at its attempt's start. What is never cut (ids,
kinds, locations, severities) is what can pass the second measure while
the first holds: thirty paths of 1,024 bytes beside 40 KiB of
instructions, a test. The page counts the batch's own bytes as the
person writes (the first measure's arithmetic in `utils/review.ts`,
tested against Go's with a shared fixture under `internal/crew/testdata`);
the server, which knows the task's text, has the last word.

**Request changes as a command** (`commands.go`). `Engine.RequestChanges(ctx,
runID, actor string, cmd Command, rq RepairRequest) (Receipt, error)`,
with `RepairRequest{Task string; Attempt int; Commit, Imported string;
Request ChangeRequest}`, through `act` with the intent
`request_changes` and the arguments `[task, attempt, commit, review]`, so
its receipt, its repeat and its conflict are every command's. The change,
under `e.mu` in the command's transaction:

- the task must be a work task (`ErrTaskState`, "a review task's verdict
  is not repaired: request changes of the candidate it reviewed");
- the candidate must stand: the task `succeeded` with its result that
  attempt and commit (in a continuation, `imported` the candidate's
  reference `<run>/<task>/<n>`), or the task `failed` with its standing
  failed candidate that attempt, having submitted that commit. The
  standing failed candidate (`standingFailed`, new, PR-2a) is the chain's
  last attempt that a withdrawal has not taken back: this run's
  `lastKept()`, else, in a continuation whose task has no attempt of its
  own yet, the source chain's last attempt as `startContinued` read it
  (kept as `t.priorLast` beside `priorFailed`: its run, number, state,
  stage, start, result commit and submitted result), named by its
  reference as `imported`; so "Continue first" leads to a request that is
  taken, not refused. Any other state or result is `ErrStaleCandidate`
  (refusal `stale_candidate`,
  `409`), whose words say what stands now ("attempt 4 is running: request
  changes of its result when it ends, or cancel it"; "the result is now
  attempt 4's a7c…");
- the repair is set (`repair{attempt, commit, summary, request}`), the
  result dropped, and, when the task's attempts are spent, one more is
  granted (`extra++`): the person's request is the authority for it, as a
  Retry is; the task is `ready` ("changes requested by Nate: 2 changes, 1
  question");
- what rested on the result is withdrawn as a review task's
  `needs_changes` withdraws it (`withdraw`): a review task that approved
  it runs again on the repair's result, an attempt in flight on it is
  cancelled at stage `withdrawn` and its session stopped once recorded,
  none of it counted;
- a finished run is open again (`r.outcome` cleared, the prerequisites
  its end interrupted armed again, `reopenPrerequisites`), as Retry and
  Accept anyway already reopen one;
- the events: `task.reopened` gains `review` (the Dispatch review's id)
  and `by` beside the existing `task` and `findings`, then the
  withdrawal's.

`reopen` and this command share one function (`reopenFor`) that takes
either a review task's attempt or a person's request, so the two repairs
are one transition with two sources. A stopped run (`ErrRunStopped`) and
a continued one (`ErrRunContinued`, naming its continuation) refuse it:
the API takes those to the continuation path below. The repair attempt
starts at the candidate (`startCommit` already takes `repair.commit`
first), gets the request in its task file, and is an ordinary attempt:
reserved and launched through `crew.Launcher`, its result checked in a
detached checkout through the run's Runtime, its receipts its own. No
text reaches any session but the task line every attempt's session is
typed (`taskLine`), through the guarded submission that types it today.

**The continuation that carries a repair** (`continue.go`).
`ContinueRunWith(ctx, runID, label string, o ContinueOptions) (*Run,
error)`, with `ContinueRun` its call with no options; `ContinueOptions{
Repair *RepairRequest; Actor string; Command Command}`. For a run the
engine does not run (finished and let go, stopped, or from before a
restart), the continuation is made as today but the repaired task's
result is not imported (the candidate must be the result its history
finds standing, by reference, else `ErrStaleCandidate` and no run is
made), the tasks that need it, transitively, import nothing either (the
withdrawal's rule, applied before anything is dispatched), the repaired
task gets the repair and, when spent, one more attempt; and the
continuation's first transaction (`run.created`) writes the command's row
and its settled receipt under the actor and command id given, so a resend
or the API's reconcile finds the receipt in the continuation as in any
run. A continuation is never a new launch: it carries the source's crew,
base and package (DS2), so the repair works from what the approved plan
revision authorized.

**Check again** (`commands.go`). `Engine.Recheck(ctx, runID, actor, cmd,
rq RecheckRequest) (Receipt, error)`, with `RecheckRequest{Task string;
Attempt int; Commit, Imported string}` (intent `recheck`, the arguments
`[task, attempt, commit]`, so the candidate the person chose is in the
command's fingerprint and a resend naming another is a conflict).
Inside the command's transaction, under `e.mu`: the task must be
`failed` (else `task_state`); its standing failed candidate
(`standingFailed`, above, so a continuation's failed task is checked
again too) must be that attempt with that submitted commit (else
`stale_candidate`, "attempt 4 failed since: check that one again", never
a check of another commit); and that attempt's verification must have
failed (`Recheckable(ledger.AttemptRow) bool`, exported: failed with a
result commit, at a check or verify step, `failedAtCheck`, or before the
checks in the checkout, `refusedBeforeChecks`: `checkout`, `manifest`,
`setup <id>`, `service <id>`, `ready <id>`, `ready`; else `task_state`).
The result then becomes the task's `candidate`, which the pump verifies
again without a session as a continuation verifies a result it found
mid-check today (`reverify`, `dispatchCandidate`), and that is **a new
attempt**: numbered next (n+1), not counted (`extra++`), its result the
same commit, `provisional` until recorded. So the candidate checked again
is `<run>/<task>/<n+1>@<same commit>`, one of its own with its own
receipts (`k` from 1 on attempt n+1, as every attempt's), and attempt n
keeps its failing receipts unchanged. The candidates route
(`candidateOf`, 16 receipts an attempt) and DS2's `candidateVerified`
read each attempt alone, so repeated failures never crowd out a later
pass, and a pass makes attempt n+1's candidate verified for a delivery.
Passing, the task succeeds with attempt n+1's result and what needs it
goes on; failing, it fails again at attempt n+1, which is then the
standing failed candidate. A run the engine does not run refuses it
(`task_state`): a person continues it first (Continue, on `main`), and
Check again then works on the continuation's failed task.

**The repair attempt says what it addressed** (`task.go`). `Result`
gains `Addressed []Addressed` (`{id, note}`, JSON `addressed`, at most
50, a note at most 1,000 characters): refused for a review task's result
and for an id the attempt's repair request does not name (`ErrBadResult`,
"addressed names a change the request does not have"). It is kept with
the attempt's result in the ledger (the `result` column, bounded by
`MaxResult` with the rest) and read with the attempt.

**A failed receipt's class** (`failure.go`, new, pure). `ClassifyFailure(a
ledger.AttemptRow, r ledger.CheckRow, same []ledger.CheckRow) Failure`
answers `{class, words}`, worked out from what the ledger keeps:

| Class | When |
|---|---|
| `cancelled` | the check was killed (`killed`) by a stop, a cancel or a withdrawal, or its attempt was cancelled |
| `infrastructure` | the check never ran (exit -1 with an `error`: the runner or the Runtime could not start it), was `interrupted` (the server ended under it), or failed in a manifest phase that makes the tree (`setup`, `service`, `ready`), not a check or a verify step |
| `flaky` | another receipt of the same check on the same `subject` passed (in `same`: the run's and its continuations' rows of that commit, the same task, phase and id, and the same `argv`, and for a verify step the same manifest identity, so another task's check of the same id, or a check whose command changed, never makes it flaky) |
| `unavailable` | the receipt's row is not in the ledger, or its log was dropped (`log_dropped`) and its tail is empty |
| `code` | it exited non-zero or by a signal, or left the checkout changed (`stable` false), or timed out (the words say "timed out after 600 s: a hang in the code, or a machine too slow") |

The class is a hint; the person's choice is recorded beside it. `same`
is read by `Ledger.ChecksOn(ctx, runs []string, task, phase, id, subject
string)` (new in `internal/crew/ledger`: the rows of those runs of that
task, phase and id whose `subject` is given, without their logs, at most
20, newest first, read by the table's primary key, which begins with
`run_id`; no index and no schema change), and `ClassifyFailure` keeps of
them those whose `argv` (and manifest) are the failing row's. A first
failure has no such row, so `flaky` is mostly said after a Check again
or a continuation's reverify passed; what Check again is offered for
never waits on the class (`recheck`, below).

### `internal/gitrepo` and `internal/api`: the candidate's diff, the prepared repair, Check again

**The diff of two commits** (`internal/gitrepo`, `gitrepo.go`).
`Compare(ctx, dir, from, to string) (Detail, error)`: the files that
differ between two commits' trees, renames found, with their added and
removed counts, as `CommitDetail` answers for one commit against its
parent (it is that function's tree walk with the two trees given, shared
as `diffTrees`); at most `MaxFiles` (500) files, `Truncated` past them; a
file past `MaxDiffed` listed without its lines.

**Routes** (`internal/api/review_diff.go`, `review_repair.go`, new, each
with its test; every one the admin's, `requireAdmin`, and a switchyard's
`403`):

| Route | Reply |
|---|---|
| `GET /api/runs/{run}/tasks/{task}/attempts/{n}/diff[?base=COMMIT]` | `{candidate, base, files: [{path, from?, status, added, removed, binary}], truncated}`; `base` the run's base unless given; a `base` that is not the run's base, the attempt's start or a result commit of the same task in the run's chain is `400 invalid_request`; `404 not_found` for an attempt with no result commit; `410 commit_gone` when a commit is no longer in the repository; at most 500 files, measured under `MaxDiffReply`, 256 KiB |
| `GET /api/runs/{run}/tasks/{task}/attempts/{n}/diff/file?path=P&side=base\|candidate[&base=COMMIT]` | the file's bytes at that side's commit, `text/plain`, at most `gitcli.MaxShowBytes` (1 MiB), `X-Conductor-Truncated: 1` past it, `X-Conductor-Commit` the commit read; `404 not_found` for a path the commit does not have (an added file's base side, a deleted file's candidate side), `415 binary` for a file git calls binary |
| `GET /api/runs/{run}/tasks/{task}/attempts/{n}/steps/{id}/{k}/repair` | the prepared failed-check request, under `MaxRepairReply`, 64 KiB: `{finding, criteria, plan?, ticket?, recheck}` where `finding` is a draft note of kind `check` (its `check_ref` filled from the receipt, the attempt before and the class), `criteria` the acceptance criteria of the run's package when it was launched from a ticket (DS2), else the task's checks as the crew names them, `plan` and `ticket` the launch's references, `recheck` `{allowed, attempt, commit, why?}`: whether Check again would be taken now, by the engine's own rule (the run held, the task failed, this attempt its standing failed candidate, `crew.Recheckable`), and in words why not; `404 not_found` for a receipt the ledger has not, `409 task_state` for a receipt that did not fail |
| `POST /api/runs/{run}/tasks/{task}/recheck` | `{commandId, attempt, commit, expectedRevision?}` → `{receipt}`, the run commands' answers (`writeReceipt`): the candidate is the one the person chose, and `409 stale_candidate` when it is no longer the task's standing failed candidate, nothing checked |

The repository is the run's crew directory as launched, through
`resolveCwd` and `crew.CheckRepo`, as DS2's proposal resolves it; paths
are relative to the repository's top, as the Files tab's Commits are; the
commits are read by object id through `gitcli.Show` and go-git, never as
files on disk, so no worktree is opened and none of a session's files is
read (a path with `..`, a leading `/` or a NUL is refused before git sees
it). A run the engine does not hold is read from the ledger (its base,
its attempts' start and result commits), as `GET /api/runs/{run}/tasks/{task}`
reads one. The routes join `internal/api/guide.go`'s table as read
operations (`diff`, `diff_file`, `repair_prepare`) and `recheck` as a run
command (`runCommandOp`), so BK0's guide lists them for the admin.

### `internal/api`: a review sent, applied and marked

**Routes** (`internal/api/dispatch_reviews.go`, new, with its test; the
admin's, a switchyard's `403`, bodies decoded with
`DisallowUnknownFields` and carrying DS1's `{opId, expectedRev?,
byName?}`; the actor is `actorOf(r)`, the server's, and it is the
review's and the mark's provenance. `via` is `web` or `cli`, the
client's own label, recorded beside the actor as what the client said;
`brakeman` is never taken from a body: the server records it only when
the request's principal is Brakeman's, which BK2 adds, and refuses it
from every other principal, the admin's included (`400
invalid_request`)):

| Route | Body and reply |
|---|---|
| `POST /api/dispatch/tickets/{key}/reviews` | `{opId, candidate, base, verdict, summary?, findings: [...], via?, byName?, expectedRevision?, continueRun?}` (192 KiB) → `201 {receipt, review}`; `review.application` says `none`, `applied` (with the run, the command, the revision and the attempt) or `refused` (with why); `409 not_ticket_run` when the ticket does not hold the run's chain (DS2's `ChainHolders`: none, another ticket, or contested); `409 invalid` for a commit that is not the attempt's result, a base not allowed (above), or a finding not bound to the candidate (below); `413 too_large` when `FitChangeRequest` refuses the batch or its detail passes `MaxReviewDetail`; `409 continuation_needed {run, words}` for a `request_changes` whose run the server does not hold, sent without `continueRun: true` (below); `409 managed_off`, `503 ledger_unavailable` for a `request_changes` this server cannot apply; each of these before anything is recorded |
| `POST /api/dispatch/reviews/{id}/retry` | `{opId, byName?}` → `{receipt, review}`: a refused request applied again as its next try |
| `GET /api/dispatch/tickets/{key}/reviews[?before=CURSOR]` | `{reviews, next?}`, a page of 20, newest first, each with its findings counted by state, under `MaxReviewsReply`, 1 MiB |
| `GET /api/dispatch/reviews/{id}` | `{review}`: the batch whole, each finding with its marks counted and its latest mark, under `MaxReviewReply`, 1 MiB, which the submit's `MaxReviewDetail` (512 KiB) keeps it within whatever marks follow |
| `GET /api/dispatch/findings/{id}/marks[?after=CURSOR]` | `{marks, next?}`: the finding's marks, oldest first, at most 20 a page and fewer when the next would pass `MaxMarksReply`, 256 KiB (a mark at its bounds, its note and `located` escaped, is under 16 KiB, so a page always holds one), so a finding's whole history is read page by page |
| `GET /api/dispatch/tickets/{key}/findings[?state=…&candidate=C&after=CURSOR]` | `{findings, next?}`, a page of at most 100 and fewer when the next would pass `MaxFindingsReply`, 1 MiB (one finding at its bounds always fits): each finding with its review's candidate, its state and last mark, the `addressed` notes of the last three repair attempts for it, and `located` against `candidate` (default: the newest result of the finding's task in the ticket's chain) |
| `POST /api/dispatch/findings/{id}/marks` | `{opId, candidate, verdict, note?, via?, byName?}` → `201 {receipt, mark}`; the server computes `located` and records it with the mark; `409 not_ticket_run` and `409 invalid` as above, and for a candidate that is not a later attempt's result of the finding's task in the ticket's chain, or, for `resolved`, one of the finding's own commit |

**Sending a review, in this order** (the launch's order in DS2, so the
two typed operations of two stores are made and settled the same way):

1. The operation is replayed (`Store.Replay`): a resend is answered from
   its receipt, the review as it stands now; another request under the
   same `opId` is `op_conflict`. For a new `request_changes`, this server
   must own the ledger and have finished taking it over (`crews.managed`
   on, the ledger writable, the reconcile done: the gate DS2's launches
   wait on, `s.launches.ready`), else `409 managed_off` or `503
   ledger_unavailable` and nothing is kept. The candidate is checked
   against the ledger and the ticket (above). Each finding is bound to it:
   its `anchor_commit` is the server's, the candidate's commit for a
   `candidate`-side anchor and the review's base for a `base`-side one
   (never taken from the body), and a check finding's `check_ref` is read
   from the ledger and refused when it is not the candidate's receipt
   (above). The request is built (`crew.ChangeRequest`) and measured
   (`FitChangeRequest`, with the task's definition from the run's crew).
   Whether the server holds the newest run of the candidate's chain is
   read here: when it does not (finished and let go, stopped, from
   before a restart) and the body has no `continueRun: true`, the answer
   is `409 continuation_needed` with the run and, in words, what a
   continuation of it starts ("Yardmaster continues run R as a new run:
   its prerequisites run again, `build`'s repair attempt 3 starts at
   a1b2c3d, `test` and `docs`, unfinished, start when ready, and the
   review task `check` runs again on the repair's result"), and nothing
   is kept.
2. Dispatch's commit: the review, its findings and, for a request,
   `app_state` `pending`, try 1. A comment or an approval stops here.
3. One application at a time per review in this process
   (`reviewsInFlight`, a map of channels, the launches' pattern). The
   command id is `rv.<review id>.<try>` (at most 22 characters, inside
   `CommandIDPattern`), under the review's actor. The run the request
   goes to is the newest of the candidate's chain (walking
   `continued_by` from the candidate's run), since that is where the
   candidate stands now; held by the engine and neither stopped nor
   continued, `Engine.RequestChanges`; otherwise, and only for a body
   that said `continueRun: true`, `ContinueRunWith` with the repair, the
   actor and the command, on `runContext` (a client that goes away does
   not stop it). A run held at step 1 and let go before this step is not
   continued without that word: the review is recorded refused with the
   `continuation_needed` words, and Request again asks for it.
4. Dispatch's commit of the outcome: `AppliedReview` with the run, the
   command id, the revision and the repair attempt's number, or
   `RefusedReview` with the receipt's words (`stale_candidate`,
   `task_state`, `run_continued` when a continuation appeared between)
   or the error's. A failure after the command was taken is read back
   from the ledger by its command id (the receipt in the run, or in the
   continuation that names the run as its source) before anything is
   recorded refused, so an applied repair is never told as refused; a
   command read back `accepted` or `uncertain` made no repair (below) and
   is recorded refused in those words.

**Reconcile** (`reconcileReviews`, run beside `reconcileLaunches` in
`ledgerRecovered`, before `s.launches.ready` is set). Every review still
`pending` whose candidate's run this server's ledger holds is looked for
in the ledger by its command id under its actor, in the candidate's run
and along its continuations, and settled by each of the four statuses a
command row has (`ledger/rows.go`) or its absence: `done` records it
applied; `failed` records it refused with the receipt's words;
`accepted` or `uncertain` (the server ended between `act`'s two
transactions: `Ledger.Recover` turns an `accepted` command of a run it
recovers into `uncertain`, and a finished run's stays `accepted`)
records it refused ("Yardmaster took the request, but the server stopped
before it made the repair, so nothing changed: send it again"), which
is exact because `act` commits a command's change and its settled
receipt in one transaction, so a command with no settled receipt changed
nothing; none records it refused ("the server stopped before Yardmaster
took the request: send it again"). A pending review whose run this
ledger does not hold (a Dispatch replica's, once CS1 replicates) is left
pending and untouched, shown as "pending on the server that runs R":
only the ledger's owner settles it (the gated class above). Nothing is
applied by the reconcile:
a request sent before a restart reaches a run only when a person presses
Request again, which is a new try with its own command id, judged
against the run as it is then.

**Located** (`review_locate.go`, new, pure but for its reads). For a
finding with lines, the file at the asked candidate's commit (the path
followed through a rename that `Compare` finds between the anchor's
commit and that one) is searched for the finding's lines as the F7 card
does in the browser (`utils/quote.ts`'s `relocate`, ported and tested
against the same fixtures): `same`, `moved {from, to}` (the nearest
place), `changed`, `file_gone`, `renamed {path, located}`, or `unknown`
with why (the commit gone, the file binary or past 1 MiB, or more than 50
distinct files asked in one reply, the bound of what one findings page
reads). A base-side anchor (a removed line) is looked for the same way,
and `changed` is what a removal that stayed removed shows. No located
state changes a finding's state; only a mark does.

**Events.** `review` and `finding` entities on `dispatch` events (above);
the run's own `run` events carry the repair (its revision moves), which
the ticket page and the Review view read again on, as they do for every
run they show.

### `internal/cli`, `internal/agents`: the CLI, the tool, the skill

`cli/dispatch.go` gains `conductor dispatch review KEY-n --candidate
RUN/TASK/N@COMMIT [--base COMMIT] --verdict request_changes|comment|approve
[--summary TEXT] [--continue] --file NOTES.json` (the notes as the route
takes them; strict JSON, unknown fields refused; without `--continue` a
`continuation_needed` answer is printed with its words and nothing is
sent again), `reviews KEY-n`, `review show ID`, `review retry ID`,
`findings KEY-n [--state open|resolved|withdrawn|all] [--candidate C]`,
`finding marks ID` (every page) and `finding mark ID --candidate C
--verdict resolved|open|withdrawn [--note TEXT]`, each write minting its op id and
asking once more on a lost reply (DS1's rule), the reads under
`maxDispatchReply`, which takes the new reply bounds; `conductor crew
recheck RUN/TASK/N@COMMIT` (the failed candidate named, as the route
takes it). `conductor task result` gains `--addressed
ID[=NOTE]` (repeatable); the MCP tool `task_result`'s schema gains
`addressed` (`internal/api/guide.go`, the one table the tools are built
from, and the golden `internal/cli/testdata/tools-list-main.json`); no
new agent tool and no agent write to Dispatch (DS1's decisions, 13:
agents read and comment). The skill's "Your task" section
(`agents/skill.go`) says that findings may come from a person, with ids,
lines and the resolution asked for, that `addressed` says which were
done, and that a question is answered in the summary. The usage in
`root.go` and the completion table follow.

### `web/app`: the Review view

- `composables/useReview.ts`: the calls `candidateDiff`, `diffFile`
  (text, with the truncation header), `prepareRepair`, `recheck`,
  `submitReview`, `retryReview`, `ticketReviews`, `review`, `findings`,
  `findingMarks`, `markFinding`, each reply through DS1's ordering gate
  (`utils/dispatchOrder.ts`), the writes minting their op ids and asking
  once more with the same id on a lost reply (`utils/tasks.ts`'s rule).
- `composables/useReviewDraft.ts`: the draft of one candidate (its notes,
  the verdict, the summary), kept in `localStorage` under the ticket and
  the candidate, every read and write in `try`/`catch`, so a reload keeps
  it and a private window works without it; a convenience of this
  browser, never a record (the record is what Send review commits).
- `utils/review.ts` (vitest `utils/review.test.ts`): a note from a
  selection (side, path, range, the lines cut to 12 of 200 bytes, as
  `makeQuote` cuts), the batch's checks and its measured size (the same
  arithmetic as `FitChangeRequest`, against the shared fixture), the
  words of an application (recorded, applied, refused) and of a located
  state, a finding's state from its marks, the candidates of a ticket in
  their lineage (`lineage`: from DS2's candidates and the task route,
  which attempt repaired which review or which failed check, through
  which continuation), the class's words, which actions a failed receipt
  offers (Check again exactly when the prepared request's
  `recheck.allowed` says the engine takes it, put first when the class
  chosen is `infrastructure` or `flaky`, its `why` shown in its place
  otherwise; Request changes always; Retry when the task failed).
- `components/DiffEditor.vue` gains `selectable` (emits `select` with the
  side and the range when the person selects lines, from the mouse or
  Ctrl+Shift+M, the F7 key) and `marks` (decorations in either side's
  margin for findings' lines), unchanged for the Files tab, which passes
  neither.
- New components: `DispatchReview.vue` (the view: requirements and plan,
  the candidate picker and its lineage, the files and the diff, the
  checks, the findings, decisions and delivery; one column on a phone),
  `ReviewCandidate.vue` (the picker and `ReviewLineage`'s strip),
  `ReviewFiles.vue` (each file's status, counts and open findings),
  `ReviewNote.vue` (the bar under a selection and the note's form:
  intent, severity, words, resolution; on a phone, the path and lines
  typed), `ReviewDraft.vue` (the gathered notes, each opened at its
  lines, edited or dropped; Send review with the verdict and the summary,
  which asks first for Request changes: "Yardmaster reopens `build`;
  attempt 3 starts at a1b2c3d with these 4 notes; what rests on it runs
  again", and, on a `continuation_needed` answer, a second confirmation
  with its words before it sends again with `continueRun: true`),
  `ReviewFindings.vue` (every finding across candidates, open
  first, located on the candidate shown, the agent's `addressed` note,
  Resolved / Still open / Withdraw with an optional note),
  `ReviewApplication.vue` (a review's recorded and applied facts, and
  Request again for a refused one), `ReviewCheckFailure.vue` (a prepared
  failed-check note: the receipt, the excerpt, the class and its choice,
  the attempt before, the criteria; Check again, Add to the draft).
- `DispatchTicketPage.vue`: `?view=review` (and `&candidate=`) shows
  `DispatchReview` in place of the sections; the Candidates list gains
  Review on each standing candidate and a count of its open findings;
  `DispatchReceipt.vue` gains Prepare a repair on a failed receipt;
  `DispatchRepairs.vue` lists a person's repairs beside a review task's
  and a failed check's (who asked, the review, the attempt that took it
  up).
- The run page: `RunTasks.vue`'s selected task, for a run a ticket holds,
  gains Review (opening the ticket's Review view at the candidate) and
  `RunAttemptSteps.vue` Prepare a repair on a failed receipt; a run no
  ticket holds says "Link this run to a ticket to review it" with DS1's
  link action.
- The palette (`usePaletteRows.ts`): the ticket page's group gains Review
  the latest candidate, Send review (n notes) and Open findings; no new
  prefix and no new verb (DS1's decisions, 11).
- No new npm package.

### Docs and the rest

`docs/protocol.md`: the rows above in its Crew runs and Dispatch
sections, the review's and the finding's shapes with their bounds, the
`review` and `finding` event entities (PR-3 and PR-4), the task file's
`repair.request` and the result's `addressed` (PR-2b, with the shapes). `docs/architecture.md`: its Dispatch
section gains reviews and how a request reaches Yardmaster.
`docs/repository-map.md`: the `internal/dispatch` row (reviews, schema
version 3), the `internal/crew` row (a person's repair, Check again, the
failure's class). `README.md`: the Dispatch section's review lines and
the CLI's. `docs/features.md`: the entry, in the last PR.
`web/e2e/stub-agent.sh`: the worker treats a repair with a person's
request as it treats a review task's findings (it commits
`stub-fixed.txt`, PR-6), and then names each change's id as addressed in
its result (PR-7, once PR-5's `--addressed` is there). For the located
states PR-7's spec reads, the stub's edits are fixed, under
`STUB_REVIEW_EDITS=1` in the member's environment (as `STUB_CHECKPOINT`
is): its first attempt also writes `stub-review.txt`, eight numbered
lines; an attempt whose task file holds a person's request ("asked for
changes to the commit") inserts two lines at the top of
`stub-review.txt` (a note on its lines 3–4 is then "moved: now 5–6") and
rewrites the first line of `stub-<member>.txt` (a note on that line is
then "the lines changed"), and nothing else of either file. The spec
puts its notes on those lines. No config key, no workflow change, nothing under
`desktop/`.

## Events, orderings and invariants

**A review's application.** `none` for a comment or an approval, for good.
For a request: `pending` (try 1) → `applied` or `refused`; `refused` →
`pending` (the next try, by Request again) at most `MaxReviewTries`
times; `applied` is final. Nothing else. The findings and the verdict
never change after the submit; a different batch is a new review.

**The order of a request** (each step durable before the next begins):

1. Dispatch's commit: the review, its findings, `pending`. No change to
   any run yet.
2. The ledger: the command taken (its row), then the repair made in one
   transaction with its settled receipt (`act`'s two transactions), or,
   through a continuation, the new run's first transaction with the
   repair and the receipt together. Every change of the run is in the
   ledger before its side effect (the engine's rule): the withdrawn
   attempt's session is stopped, and the repair attempt reserved, only
   after the commit.
3. Dispatch's commit: `applied` with the receipt's run, command,
   revision and attempt, or `refused` with why.

A server that ends between 1 and 2 left a pending review and no change:
the reconcile records it refused, in words, and a person sends it again.
One that ends inside 2, between `act`'s two transactions, left a command
taken and no change (the change and its receipt commit together): the
reconcile reads the command `accepted` or `uncertain` and records it
refused in those words.
One that ends between 2 and 3 left the repair made: the reconcile finds
the receipt by its command id and records it applied. A resend of the
operation answers the review as it stands; a resend of the command (the
same try) answers the receipt kept (`repeat: true`), never a second
repair. A second server on the same data directory cannot apply (its
ledger is read-only without the owner lock, step 1 refuses there) and
cannot settle one (only the ledger's owner writes `app_*`).

**Bound to the candidate.** A review names one candidate and one base,
both full commits, fixed at its submit. A request is applied only while
that candidate stands as the task's result (or is the failed task's last
kept attempt): checked inside the command's transaction under `e.mu`, so
an attempt that ends between the person's look and the command is seen.
A request on a candidate that no longer stands is refused, never applied
to the newer result. The repair attempt starts at the candidate's commit
(`startCommit`), whatever the run did meanwhile.

**Checks bind to the commit they ran on.** A repair's new candidate is a
new attempt's result with its own receipts; the old candidate's receipts
are rows of the old attempt with the old commit as `subject`, never
rewritten, and a candidate's receipts are those rows whose `subject` is
its commit (DS2's rule), so a page never shows one candidate's checks
against another. Check again makes a new attempt (n+1) whose result is
the same commit, with receipts of its own; attempt n's failing receipts
stay as they were, its candidate's.

**Findings across candidates.** A finding is made on one candidate and
keeps that binding. Its state is its latest mark in the log's one order:
`open` with none; a mark names the candidate it was made against, and a
`resolved` mark must name a later candidate of the same task in the
ticket's chain than the finding's own: a later attempt by number, and a
different commit, so a candidate of the same code (a Check again's
attempt n+1, a repair that committed nothing) never resolves it. The located words are computed
for the candidate asked, recorded with a mark as what the person saw, and
never change a state: a moved line, a changed line or a deleted file is
shown as such, and the finding stays open until a person marks it. The
agent's `addressed` is shown as its claim. A finding whose task's chain
the ticket no longer holds (a contested chain, DS2) is shown with words
and takes no mark until a person settles the chain.

**No live rewriting.** A request reopens a task of a run only through
the engine's transition, which ends what rested on the old result and
starts a new attempt; it never types into a live session, never adds or
resumes a member (`409 managed_run` stands), never edits the crew, the
package or the plan revision. A change of scope is DS2's Revise and a
follow-up launch, which the Review view offers beside Request changes
for that purpose ("this asks for more than the plan: revise it").

**No merge, no acceptance.** No operation here merges, pushes, opens a
pull request, adds a delivery link, accepts a ticket or moves a status.
Review verdicts, verification, acceptance and delivery stay separate
facts (the Program, section 1.4).

**Migrations.** Dispatch 2 to 3: three tables and five indexes, nothing
read or rewritten (above). The ledger: 7 to 8 (PR-2b), a migration that
changes no table and rewrites no row, made only as a barrier.
`repair.request` and `result.addressed` are new optional members of JSON
the ledger keeps as text; a binary before this item would decode a
repair and drop the request (`decodeRepair` keeps only the members it
knows), write the reduced repair back through `taskRow` when it
continues the run, and launch the repair attempt without the person's
changes. A version 8 ledger opens read-only under that binary (the
ledger's rule: a later version opens read-only), so it shows the runs
but neither continues, retries nor repairs one; every row a version 7
database holds has neither member and reads as it did. The number is
whichever is next when PR-2b lands, should another item bump it first.

**Replication.** The classes in the field rules above: a review, its
findings and its marks are immutable or append only, so two replicas
never disagree about them; the application is written only by the
ledger's owner and merges as a scalar; a mark made on each of two
replicas is kept, both shown, the later in the one order deciding the
state.

## Out of scope

- A reviewer chosen by role, a review required by policy, quorum, a
  delegate, a reviewer pool: role-bound reviews come with Switchman's
  team routing (SW1); here the reviewer is the operator, as decision D4
  makes the single approver.
- A review asked for as a decision record (a gate that waits for a
  person's review): DR1's DecisionRequest records and the holding of
  dependent work are DR1's (`docs/plans/decisions.md`), and its records
  appear in DS2's Decisions section, which this item's view embeds; a
  person's member label as a reviewer is HM1's.
- A CI job outside Conductor (a GitHub check run on a delivery's pull
  request) as a failed-check request: IN1a reads linked pull requests
  (`docs/plans/tracker-linking.md`); the check finding's `source:
  external` is reserved for it here.
- Brakeman asking for a repair: BK2's `request_repair` grant sends this
  item's review route under the `brakeman` principal
  (`docs/plans/brakeman.md`, PR-12); nothing here grants it.
- Line authorship ("who wrote this line"), blame across agents and
  tools, and comparing two candidates of different tasks or runs
  (the Program, section 7; CP1 compares approaches).
- Live revision of a managed run's tasks, a member added or resumed by
  hand, a crew reshaped while it runs (the Program, sections 1.5 and 7).
- Merging, pushing, opening a pull request, publishing a review outside
  Conductor, and a `reviewed` close policy.
- Reviews of a run no ticket holds, drafts kept on the server, threaded
  replies on a finding (a mark's note is the reply), suggested edits
  applied from the page, and notifications or webhooks for a review.

## Tests per surface

Per `docs/checks.md`: a Go change runs `make lint`, `go test -race
-count=1 -timeout 30m ./...` (the whole module) and `make lint-static`;
a UI change runs `npm --prefix web run typecheck`, `npm --prefix web
test` and `make test-e2e` (which builds the web app and the binary
first; the spec alone while iterating); a change of the scripts runs
none of theirs, since none is touched. The tests each PR adds:

**`internal/dispatch`** (PR-1): `TestSubmitReviewKeepsItsCandidateAndBase`,
`TestSubmitReviewRefusesWhatCannotBeRepaired` (no change finding in a
request, kind and fields disagreeing, an external source),
`TestReviewBounds` (each bound at its edge and one past),
`TestReviewReplayAnswersTheSameReceipt` (and once its receipt is pruned,
by the fingerprint), `TestApplicationSettlesOnlyItsTry`,
`TestRetryIsTheNextTryAndStopsAtFive`, `TestMarksAreALog`,
`TestFindingStateIsTheLatestMark` (two marks at one time, the one
order), `TestResolvedNeedsALaterCandidate` (a candidate of the finding's
own commit refused, whatever its attempt), `TestMarksPageEndsAtItsBound`,
`TestReviewDetailBoundAtSubmit` (`MaxReviewDetail`, escapes counted),
`TestReviewOnAClosedTicketOrArchivedProject`,
`TestMigrationTwoToThreeKeepsEveryRow` (a version 2 database from
`testdata`, opened, every row as it was), `TestVersionThreeIsReadOnlyForOlder`,
`TestReviewFieldClasses` (`rules.go`), `TestSnapshotCarriesReviews`,
`TestReviewChangesNameTheirTicket` (`OnChange`).

**`internal/crew`, `internal/crew/ledger`** (PR-2a, Check again and
the class): `TestRecheckVerifiesTheFailedCandidateAgain` (passing:
succeeded at that commit as attempt n+1, a candidate of its own;
failing: failed again at n+1; attempt n's receipts unchanged either way),
`TestRecheckIsBoundToTheChosenCandidate` (another attempt failed
meanwhile: `stale_candidate`, nothing checked; a resend naming another
candidate: a conflict), `TestRecheckAfterAFailureBeforeTheChecks` (a
checkout's setup step failed: checked again),
`TestRecheckRefusesATaskThatDidNotFailInItsVerification`,
`TestRecheckInAContinuation` (the source's failed attempt, the standing
failed candidate, checked again in the continuation),
`TestRecheckRepeatedFailuresThenAPass` (three failing rechecks, then a
pass, past 16 receipts in all: each attempt its own),
`TestClassifyFailure` (a table over every class; two tasks sharing a
check id and a subject with different commands are never flaky for each
other; a check whose `argv` changed neither),
`TestChecksOnReadsTheChainsRows` (ledger: by task, phase, id and
subject).

**`internal/crew`, `internal/crew/ledger`** (PR-2b, a person's repair):
`TestRequestChangesReopensTheStandingCandidate`,
`TestRequestChangesWithdrawsWhatRestedOnIt` (a review task that approved
runs again; an attempt in flight on it is cancelled, uncounted),
`TestRequestChangesOnAFailedTaskGrantsAnAttempt`,
`TestRequestChangesOnAContinuationsFailedTask` (the source's last
attempt, by its reference, after Continue),
`TestRequestChangesRefusesACandidateThatNoLongerStands` (a newer result,
an attempt running, a review task),
`TestRequestChangesRepeatIsTheSameReceipt`,
`TestRequestChangesReopensAFinishedRun`,
`TestRequestChangesTypesNothingIntoALiveSession` (the fake sessions see
only the new attempt's task line),
`TestPersonsRepairInTheTaskFile` (golden `task.md` and `task.json`
under `testdata`), `TestPersonsRepairCutToFit`,
`TestFitChangeRequestAtThePayload` (the shared fixture the web's vitest
reads too), `TestFitChangeRequestWithTheTasksText` (thirty 1,024-byte
paths beside 40 KiB of instructions refused though the repair is under
64 KiB; at the edge accepted, and its attempt's `task.md` and
`task.json` written within 64 KiB each),
`TestAddressedNamesOnlyTheRequestsChanges`,
`TestOldRepairRowsReadAsBefore` (a version 7 row without `request`),
`TestLedgerVersionIsABarrier` (ledger and engine: 7 to 8 keeps every
row; a ledger of a later version than the binary's, holding a person's
repair, opens read-only, and Continue, Retry and Request changes are
refused, nothing written).

**`internal/crew`** (PR-2c, the continuation that carries a repair):
`TestContinuationCarriesAPersonsRepair` (the candidate not imported,
its dependents not imported, the receipt in the first transaction),
`TestContinuationRefusesAStaleCandidate`.

**`internal/gitrepo`, `internal/api`** (PR-3):
`TestCompareListsTheFilesBetweenTwoCommits` (renames, binary, past
`MaxFiles`), `TestCandidateDiffAgainstTheRunsBase`,
`TestCandidateDiffRefusesABaseOutsideTheChain`,
`TestDiffFileReadsEachSide` (an added file's base side `404`, a
truncated one's header), `TestDiffFileRefusesAPathOutsideTheTree`,
`TestDiffOfACommitGoneIs410`, `TestDiffOfARunTheEngineLetGo`,
`TestPrepareRepairNamesTheReceiptAndTheCriteria`,
`TestPrepareRepairClassifies`, `TestPrepareRepairBound`,
`TestPrepareRepairSaysWhetherCheckAgainIsTaken` (a code failure:
allowed; a checkout's setup failure: allowed; a run the server let go and
a task that moved on: not, with why), `TestRecheckRoute` (the candidate
named; one that moved meanwhile `409 stale_candidate`),
`TestRecheckedCandidateIsVerifiedForADelivery` (`candidateVerified` on
attempt n+1 after failures), `TestReviewDiffRoutesAreTheAdmins` (a session token
and a view link refused, a switchyard `403`), and the guide's golden
(`guide_test.go`).

**`internal/api`** (PR-4): `TestSubmitReviewAppliesARepair` (recorded,
applied, the run's revision moved, the repair attempt's number),
`TestSubmitReviewOnARunTheServerLetGoContinuesIt`,
`TestSubmitReviewOnAStoppedRunContinuesIt`,
`TestSubmitReviewRefusedWhenTheCandidateMoved` (recorded, then refused
with the receipt's words), `TestSubmitReviewNeedsTheTicketsRun`
(none, another ticket, contested), `TestSubmitReviewTooLargeKeepsNothing`,
`TestSubmitReviewWithoutTheLedgerKeepsNothing`,
`TestApplicationAfterTheCommandIsReadBack` (a failure past step 2 is
read from the ledger, never told refused),
`TestSubmitReviewBindsEachFindingToItsCandidate` (another candidate's
receipt, a passing receipt and a base-side anchor all bound by the
server; a client's outcome fields are unknown fields),
`TestSubmitReviewOnARunTheServerLetGoAsksFirst` (without `continueRun`:
`409 continuation_needed` with its words, nothing recorded and no run
made; a run let go between the check and the application: recorded
refused, not continued),
`TestReviewsReconciledAtStart` (a pending one whose command is `done`:
applied; `failed`, `accepted`, `uncertain` and none: refused, each in its
words; one whose run this ledger does not hold: left pending; nothing
applied by the reconcile),
`TestReviewReconciledBetweenActsTwoTransactions` (the server ended
through a test hook after `act`'s first transaction, beside
`afterLookup`: on restart recorded refused, the task unchanged),
`TestReviewViaIsTheServers` (`brakeman` from the admin refused; `web`
and `cli` kept as the client's label beside the actor),
`TestRequestAgainIsANewCommand`, `TestFindingsLocatedOnTheNewCandidate`
(same, moved, changed, gone, renamed, unknown past 50 files),
`TestLocatedNeverResolves`, `TestMarkNeedsALaterCandidateOfTheTask`,
`TestOldCandidateKeepsItsReceiptsAfterARepair` (the candidates route
before and after), `TestReviewRoutesAreTheAdmins`,
`TestReviewRepliesStayInTheirBounds`,
`TestReviewRepliesAtTheCombinedBounds` (a review of 50 findings at every
bound, each with 50 marks at theirs: the detail, every marks page and
every findings page within their bounds, and every mark read),
`TestReviewEventsReachTheHub` and `TestEventHubDispatchEvent` with the
two new entities and `maxDispatchEach`'s arithmetic
(`api/events_test.go`), and the sync adapter's test with the new
entities (`sync_test.go`).

**`internal/cli`, `internal/agents`** (PR-5): `TestDispatchReviewFromAFile`,
`TestDispatchReviewRefusesUnknownFields`, `TestDispatchReviewsAndShow`,
`TestDispatchFindingsAndMark` (and every page of `finding marks`),
`TestDispatchReviewRetry`, `TestDispatchReviewContinue` (without
`--continue` the words printed and nothing resent),
`TestCrewRecheck` (the candidate named; one that moved refused),
`TestTaskResultAddressed` (CLI and the MCP tool),
the tools golden (`testdata/tools-list-main.json`),
`TestSkillSaysFindingsMayComeFromAPerson`, the completion table's test.

**`web/app`** (PR-6, PR-7, vitest): `utils/review.test.ts` (a note from
a selection on each side, the cut lines, the batch's size against the
shared fixture, the application's and the located words, a finding's
state from marks, the actions of a failed receipt: Check again by
`recheck.allowed`, whatever the class, and its `why` otherwise),
`utils/reviewLineage.test.ts` (a repair of a person's review, of a
review task, of a failed check, through a continuation),
`composables/useReviewDraft.test.ts` (kept per candidate, a storage that
throws, a sent draft emptied), `composables/useReview.test.ts` (the
ordering gate, one more ask with the same op id on a lost reply).

**Playwright** (`web/e2e`): `review.spec.ts` (PR-6): a ticket launched
from an approved revision of a stub worker crew; the Review view at the
first candidate; lines selected on the candidate side of one file and
the base side of another, a whole-file note and a general note, the
draft kept through a reload, Send review as Request changes after its
confirmation; the review shown recorded then applied with its attempt;
the repair attempt's candidate appearing, its receipts its own and the
first candidate's unchanged; the batch past its size refused with
nothing kept. `review-repair.spec.ts` (PR-7), its stub worker under
`STUB_REVIEW_EDITS=1`: the findings on the new candidate (the note on
`stub-review.txt` 3–4 "moved: now 5–6", the one on `stub-<member>.txt`
line 1 "the lines changed"), one marked resolved, one still open, the
agent's addressed notes shown, a question withdrawn; a second round
requested and applied; a check that fails once (its argv fails while a
marker file the spec owns, outside the repository, is absent, and
writes it), Prepare a repair showing the receipt, its class (`code`)
and the criteria, Check again offered because the prepared request says
the engine takes it, and pressed: the task succeeds at the same commit
as attempt n+1, a candidate of its own listing its passing receipt,
attempt n's candidate its failing one unchanged, and Resolved refused
for a finding of attempt n on attempt n+1, the same commit; a refused
request (the task retried meanwhile)
and Request again; the run page's Review entry; the phone width (one
column, a note by typed lines, a mark). Each spec ends its strays
(`stopStrays`), finds its ticket and run by id, waits on what the page
draws, and raises its timeout by its own waits' caps (AGENTS.md, Tests
that do not flake).

**By hand** (what tests cannot cover): a real agent (Claude Code or
Codex on the owner's subscription) reading a person's findings from its
task file and addressing them; Monaco's selection and the margin marks
in Chromium, Safari and the Electron app; the Review view on a phone on
the LAN (from a machine that is not WSL, or through the desktop app's
own forwarding; no networking is configured by hand).

## Boxcar constraints

1. The engine starts an attempt only through `crew.Launcher`: honoured;
   a repair is the reviewed task's next attempt, reserved and launched as
   every attempt is; a continuation carrying a repair dispatches through
   the same path; Check again starts no session.
2. `session.Local` drives a `session.Process`: not touched; nothing here
   reaches a session, a pid or a tty, and nothing is typed into a session
   but the task line every attempt gets.
3. An attempt's context is files relative to its worktree: honoured and
   extended; the person's request is written into `task.md` and
   `task.json` through the attempt's `os.Root`, from the task row's
   `repair` in the ledger, durable before the attempt is reserved; nothing
   an attempt needs is only in the server's memory.
4. The agent talks to the server over the network: honoured; `addressed`
   travels in `task_result` over HTTP with `CONDUCTOR_NOTIFY_TOKEN` as
   every result does.
5. Checks go through `internal/proc`: honoured; Check again verifies
   through `verify`, which runs every check through the run's Runtime
   (`execFor`) and `internal/proc` as it does for a submitted result; the
   git reads of the diff are argv arrays through `internal/gitcli` and
   go-git, as the Files tab's are, and are no check.
6. Platform splits stay platform tags, the UI shows capabilities: not
   touched; nothing here differs by platform or depends on a provider.
7. Evidence rows gain `where` in C10: honoured; this item adds no
   evidence row to the ledger; a check finding keeps the receipt's
   `where` with its identity, and the Review view shows each receipt's
   `where` as DS2's chips do; no class assumes a native process (the
   infrastructure class reads the receipt's state, error and phase, never
   a pid).
8. `hostagent` is a launch site as much as `api`: not touched; a managed
   run, and so its repair, runs on the server, and the request reaches an
   attempt as files in its worktree, which is where an attempt run
   elsewhere would read it.

## Open-core checklist

- [x] Article 1: no licence or plan check in `internal/dispatch`, the
  engine, the routes or the pages; the conditions are those of a managed
  run and of Dispatch.
- [x] Article 2: the whole of it, reviews, repairs, the failed-check
  request, Check again, findings across candidates and the Review view,
  ships in the open-source build with no account.
- [x] Article 3: nothing is gated; there is no flag a fork could flip.
- [x] Article 4: a review is a person's required decision, recorded with
  its receipt and never weakened; every read is a `GET` the admin makes;
  the records are rows of the two plain stores, exported whole by C6;
  stop, pause, cancel, retry and accept are unchanged.

## The PRs

Each PR is one issue and one branch cut from `main`, a draft PR at its
first commit, the checks for its surfaces before every push, its body
ticking the open-core checklist (articles 1 to 4) and naming the agent
that made it; each leaves `main` working and green on its own (a store
operation without its route is reached by its tests, a route without its
page by the CLI and the tests, a page without its later parts says what
is not there yet). Parallel means the two touch no common Go package, no
common directory under `web/`, and no common file under `docs/` or at the
root.

Every prerequisite is on `main`: DS2 (Done 2026-11-04: candidates,
receipts, the launch package, delivery's candidate, `ChainHolders`), the
engine's review gate and withdrawal, F7's comments, C2's actor (`actorOf`)
and C10's `where` (merged; C10 is In review only for its Windows desktop
observation). No PR here waits on another item. What other items add
later reaches this one through their own PRs, named in Out of scope:
DR1's records in DS2's Decisions section (`docs/plans/decisions.md`,
which owns that part), IN1a's external CI jobs
(`docs/plans/tracker-linking.md`), BK2's `request_repair`
(`docs/plans/brakeman.md`), SW1's role-bound reviewers.

| PR | Title | Contains | Touches | Tests | Depends on | Parallel with |
|---|---|---|---|---|---|---|
| PR-1 | dispatch: reviews, findings and marks (schema version 3) | the three tables and their indexes, the migration 2 to 3, the bounds (`MaxReviewDetail` among them), `SubmitReview`, `ApplyingReview`, `AppliedReview`, `RefusedReview`, `MarkFinding`, the reads (the measured marks and findings pages) and `PendingReviews`, a finding's state from its marks, the field classes in `rules.go`, the store's `review` and `finding` changes (`OnChange`) | `internal/dispatch` | the `internal/dispatch` tests above | none | PR-2a, PR-2b, PR-2c, PR-3 |
| PR-2a | crew: Check again and a failed check's class | `standingFailed` and `t.priorLast` (a continuation's failed task), `Engine.Recheck` bound to its candidate, `Recheckable`, `ClassifyFailure`, `Ledger.ChecksOn` by task, phase, id and subject | `internal/crew`, `internal/crew/ledger` | the PR-2a tests above | none | PR-1 |
| PR-2b | crew: a person's repair | `ChangeRequest` in `TaskRepair`, `task.md`'s person's findings and their cut order, `FitChangeRequest` with the task's text and its fixture, `Engine.RequestChanges` and `reopenFor` (through `standingFailed`), `Result.Addressed`, the ledger's version 8 barrier; `docs/protocol.md`'s task file `repair.request` and result `addressed` | `internal/crew`, `internal/crew/ledger`, `docs/protocol.md` | the PR-2b tests above | PR-2a | PR-1 |
| PR-2c | crew: the continuation that carries a person's repair | `ContinueRunWith` and `ContinueOptions`, `startContinued`'s import of a repaired task and its dependents, the command's row and receipt in the first transaction | `internal/crew` | the PR-2c tests above | PR-2b | PR-1, PR-3 |
| PR-3 | api: a candidate's diff, the prepared failed-check request, Check again | `gitrepo.Compare` (`diffTrees` shared with `CommitDetail`), the two diff routes, the repair-prepare route with `recheck`, the recheck route bound to its candidate, their guide rows, `docs/protocol.md`'s Crew runs rows | `internal/gitrepo`, `internal/api`, `docs/protocol.md` | the PR-3 tests above | PR-2a | PR-1, PR-2c |
| PR-4 | api: reviews sent, applied, reconciled and marked | `dispatch_reviews.go` (the seven routes, the order of a request, the findings' binding, `continueRun`, `reviewsInFlight`, the read-back), `reconcileReviews` in `ledgerRecovered` with every command status, `review_locate.go`, the guide rows, the sync adapter's entities, the `dispatch` event bound and `maxDispatchEach`'s arithmetic (`events.go`), `docs/protocol.md`'s Dispatch rows and event entities | `internal/api`, `docs/protocol.md` | the PR-4 tests above | PR-1, PR-2b, PR-2c, PR-3 | none |
| PR-5 | cli, agents: review from the CLI, `addressed`, the skill | `conductor dispatch review` (with `--continue`), `reviews`, `review show`, `review retry`, `findings`, `finding marks`, `finding mark`, `conductor crew recheck`, `conductor task result --addressed`, `task_result`'s `addressed` in the guide table and the tools golden, the skill's text, usage and completion | `internal/cli`, `internal/agents`, `internal/api` (`guide.go` only) | the PR-5 tests above | PR-4 | PR-6 |
| PR-6 | web: the Review view, its diff and the batch sent | `useReview`, `useReviewDraft`, `utils/review.ts`, `DiffEditor`'s `selectable` and `marks`, `DispatchReview`, `ReviewCandidate`, `ReviewFiles`, `ReviewNote`, `ReviewDraft`, `ReviewApplication`, the ticket page's `?view=review` and Review on candidates, the stub's repair from a person's request, `review.spec.ts` | `web/app/components`, `web/app/composables`, `web/app/utils`, `web/e2e` | vitest above (`review.test.ts`, `useReviewDraft.test.ts`, `useReview.test.ts`), `review.spec.ts` | PR-4 | PR-5 |
| PR-7 | web, docs: findings across candidates, the failed-check panel, the whole view; the features entry | `ReviewFindings` (marks, located words, the agent's claims), `ReviewCheckFailure` (Prepare a repair, Check again), `ReviewLineage` and `utils/reviewLineage.ts`, requirements, plan, checks, decisions and delivery in the view, `DispatchRepairs`' person's repairs, the run page's entries, the palette rows, the phone layout, the stub's `addressed` and its `STUB_REVIEW_EDITS` edits, `review-repair.spec.ts`; `docs/architecture.md`, `docs/repository-map.md`, `README.md`, `docs/features.md` | `web/app/components`, `web/app/composables`, `web/app/utils`, `web/app/pages`, `web/e2e`, `docs`, `README.md` | vitest (`reviewLineage.test.ts`, more of `review.test.ts`), `review-repair.spec.ts` | PR-5, PR-6 | none |

Sizes, as the owner asked (2026-11-05: a row is a batch, 300 to 800
changed lines, at most about 1,500 with its tests): PR-1 about 650 lines
and 750 of tests; PR-2a about 300 and 400; PR-2b about 450 and 500;
PR-2c about 200 and 250; PR-3 about 450 and 550; PR-4 about 800 and 700; PR-5 about 400 and 350; PR-6 about 800 and 450; PR-7
about 750 and 450, with the docs.

## Owner decisions

Dated 2026-11-05. Each is a default the owner can overturn with a dated
row in the Program's status log, its convention for a decision the owner
did not make (the Program, section 3.1); the reasoning is why it was
taken.

1. **The open decisions named with this item.** O4 (the human policy SW1
   ships first) is SW1's, decided at its plan review: this item takes no
   position on roles, groups or delegates, records the reviewer as the
   actor C2 gives and the display name DS1 records, and leaves a role on
   a review to SW1. O10 (Dispatch's merge rules, CS1's) is extended here
   as DS1's plan lets each stream extend it: reviews, findings and marks
   immutable or append only, a review's application gated on the
   ledger's owner and merged as a scalar, a finding's state never stored
   but read from its marks. Neither blocks this item.
2. **Reviews live in Dispatch; the repair lives in the ledger.** A
   review, its findings and its marks are a person's records about a
   ticket's candidate, which a Workspace will share through CS1 (the
   ledger is never replicated, only its runs' pointers), and the ticket
   is where the Review view is; the repair, its command receipt and the
   attempts are execution, Yardmaster's. Chosen over the ledger alone,
   which would leave reviews unreplicated and tie them to the owner lock,
   and over Dispatch alone, which would put execution state outside
   Yardmaster (the Program, section 1.4).
3. **A review needs a ticket.** The run must be one a ticket holds (DS2's
   chain rule); a run without one offers DS1's hand link first. Chosen
   over reviews of any run, which would need a second home and a second
   view.
4. **The draft is the browser's.** Notes gather in this browser's
   storage until Send review commits them; nothing is a record before.
   Chosen over drafts on the server, which would add a record no one
   else needs and a lifecycle with no receipt.
5. **Three verdicts.** Request changes (applied by Yardmaster), Comment
   (recorded, applied to nothing) and Approve (recorded, applied to
   nothing: it never accepts the ticket, adds a delivery, closes,
   merges or pushes).
6. **Request changes is the engine's repair transition, or the
   continuation that carries it.** On a run the server runs, the
   reviewed task is reopened at the candidate as a review task's
   `needs_changes` reopens it, one shared function, with an attempt
   granted when spent (the person's request is the authority, as Retry
   is); on a run it does not run, a continuation of it carries the
   repair, never a new launch, since the approved plan revision still
   covers the work. A change of scope is DS2's Revise and a follow-up
   launch.
7. **Never applied silently.** A request pending at a restart is
   recorded refused, in words, and applied only when a person sends it
   again, as a new try judged against the run as it is then; at most five
   tries a review.
8. **Only a person resolves a finding, against a later candidate.**
   Located words (same, moved, changed, gone, renamed) are shown and
   recorded with a mark, never a resolution; the agent's `addressed` is a
   claim shown beside the finding.
9. **A failed check's class is a hint.** Worked out from the ledger's
   receipt (cancelled, infrastructure, flaky, unavailable, code; a
   timeout counted as code with words saying it may be the machine), the
   person's choice recorded beside it; flaky only where another receipt
   of the same check on the same commit passed in the run's chain.
10. **Check again is a command of its own**, on a run the server runs:
    it verifies the failed candidate the person named again without a
    session, as a new uncounted attempt whose result is the same commit
    and whose receipts are its own, and changes nothing else; for a run
    it does not run, Continue first, then Check again in the
    continuation.
11. **The base is the run's base by default**; the attempt's start or an
    earlier candidate of the same task may be chosen, and the review
    records which.
12. **Bounds follow the review task's.** Fifty notes, 1,000 characters
    of words and of resolution each, twelve lines of 200 bytes, a summary
    of 4,000; the repair as the ledger keeps it, and the repair
    attempt's task file with the task's own text, within 64 KiB each,
    refused whole past either.
13. **Provenance stops at the attempt.** The view says which task,
    attempt, member, agent and version made a candidate and which review
    or failed check it repaired; it never says who wrote a line.
14. **A phone verifies and reads.** Marks, findings and inline diffs on a
    phone; a note on lines is made there by typing the lines, since
    Monaco's selection is a desktop gesture.
15. **No `reviewed` close policy** in this item: approvals and open
    findings are shown beside the close policy, and a project's policy
    is DS1's and DS2's (`accepted`, `delivery`, `verified`). The owner
    can ask for one.
16. **When the code starts.** Now: DS2 is Done and nothing else is
    needed. RV1's Status follows the Program's rules: In progress at its
    first PR, In review when the last is ready, Done when the boundary
    below is observed.

No question is left open: each answer above lets the plan stand, and the
owner overturns any of them with one row.

## Acceptance

The boundary, in this plan's words: a person sends several findings on
one candidate as one review, Yardmaster makes one traceable repair round
of it, and the person checks each finding against the new candidate;
nothing merges by itself. It is observed, not ticked:

- Once PR-7 is merged, on the owner's `conductor serve` with a real agent
  on the owner's own subscription (Claude Code or Codex), a native
  project (a scratch repository the agent trusts, as #350's and DS2's
  checks used) and a real ticket with acceptance criteria; a managed
  crew with a work task, a check, and a review task by an agent; a plan
  revision approved and launched (DS2).
- On the first candidate: the Review view shows the criteria, the plan
  revision, the diff against the run's base and the receipts; the person
  adds at least four notes (a blocker on a range, a minor on one line, a
  question on a file, a general note) and sends them as Request changes.
  The review is shown recorded, then applied with the run, the command
  and the repair attempt's number; the ledger's command receipt, the
  `task.reopened` event naming the review, the task file's request with
  the four ids, and the repair attempt starting at the candidate's commit
  are each read and agree.
- After the repair: the new candidate has its own receipts and the first
  keeps its own; the agent review task ran again on the new result; the
  findings show on the new candidate with their located words and the
  agent's addressed notes; the person marks each one (resolved, still
  open, withdrawn) against the new candidate, and none changed state
  without a mark. No merge, push, delivery or status change happened by
  itself; a delivery link added by the person names the final candidate.
- A check failing on a candidate: Prepare a repair shows the tested
  commit, the receipt's identity, the excerpt, the log's hash equal to
  the step log route's `X-Conductor-Log-SHA256`, the attempt before, the
  criteria and a class.
- The observation, and what was missing, slow or wrong, goes into
  `docs/features.md` (PR-7 when it comes before that PR merges, else a
  later docs PR of the stream); a bug the stream caused is fixed in the
  stream, a defect outside it filed.
- Done means: every PR merged and the observation recorded with the
  owner's word that the ticket answered what was asked, what the person
  found, what Yardmaster did about it, and what was checked on which
  commit; the Program's status row moves to Done then, never on the
  table alone. It is part of the R1 gate, with DR1's decision and MB0's
  page (the Program, section 6).

What needs the owner: the review of this plan and its decisions; cutting
the nine issues; the observation, whose agent runs on the owner's
subscription; the merges.
