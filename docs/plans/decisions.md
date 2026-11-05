# Durable decisions: a person's answer that outlasts the session that asked (Program item DR1)

A decision request is a durable record: a question a crew's agent, or a
ticket's approved plan revision, puts to a person, with its exact scope,
the response the person gives, and the receipt of Yardmaster applying
that response to the work that waited on it. This plan is Program item
DR1 (the Program, section 4.3), written against `main` at `9723fdc6`
(2026-11-05) after reading the session's questions and their claims
(`internal/session`), the decision router seam (`internal/api/router.go`,
`ext/decisions.go`), the managed-run engine and its ledger
(`internal/crew`, `internal/crew/ledger`), Dispatch's plan revisions and
launch packages (`internal/dispatch`), Brakeman's decision cards
(`internal/brakeman/brief.go`), the MCP server (`internal/cli/mcp.go`) and
the operations table (`internal/api/guide.go`). It is reviewed before any
of its code; the PRs at the end are cut into issues after the review, one
issue, one branch `issue-N`, one PR each (AGENTS.md).

The names follow the railroad (the Program, section 1.2): Conductor is the
application where the person answers, Yardmaster is the agent harness that
owns the wait and applies the answer, Dispatch is the ticketing component
whose tickets and plan revisions name the decisions a run must have, and
Switchman is the routing of decisions, whose seam (`ext.DecisionRouter`,
Program item C5) this item feeds and whose team routing is Program item
SW1, a later and paid item. An attempt's checkout is a worktree, never a
workspace (decision D5). "Recipes" does not occur in this plan: nothing of
the catalog's agent recipes or EN1's project manifests changes.

**Prerequisites.** The Program gives DR1 two: DS2 (a ticket's plan
revisions, launch packages and evidence, `docs/plans/dispatch-evidence.md`)
and C5 (the decision router seam, `docs/plans/ext.md`). Both are Done and
on `main`: DS2's fourteen PRs merged by 2026-11-03 and its boundary was
observed on 2026-11-04; C5's router, `Local.AnswerAs` and the
`answer_as` control message are on `main` with C1 to C9. So every PR
below builds on `main` as it is, and none waits on another item.
C10 (the Runtime, `docs/plans/runtime.md`) is still In review for its
Windows observation, but its code is on `main` and DR1 adds nothing that
starts a process, so it does not matter here.

**The items this one meets, and who owns what they share.**

- **HM1** (people as crew members, local labels): its plan path is this
  file too, and its parts are its own subsections and PR rows, marked
  HM1, after DR1's. DR1 ships with one approver, the operator; the record
  keeps a destination kind and a policy so that HM1 can add a person's
  label as a destination, and whom the operator answered for, without
  changing what DR1's fields mean. DR1's text and PRs presume nothing of
  HM1's, and HM1's PRs come after DR1's.
- **MB0** (the "Needs your decision" page, `docs/plans/decision-inbox.md`,
  planned beside this one): DR1 owns the record, its routes, the engine's
  wait and application, the four states the record can report, and the
  in-context controls (the run page, the session's question bar, the
  ticket page, the palette) through two components, `DecisionCard.vue`
  and `DecisionRespond.vue`. MB0 owns the page itself, its phone layout,
  and how it lays out the brief and the evidence; it renders DR1's
  components and calls DR1's routes, adding no record and no route of its
  own unless its plan says why.
- **BK2** (Brakeman's authorized actions, `docs/plans/brakeman.md`, its
  PR-12 and PR-13): Brakeman's `route_decision` grant calls DR1's route
  operation (`POST /api/decisions/{id}/route`, below) under the
  `brakeman` principal, and Brakeman's `decision:<id>` ref resolves to
  DR1's record. DR1 owns the operation and the record; Brakeman's plan
  owns the grant, the principal's acceptance on that route and the ref.
  Its PR-18 (routing to a person's label) waits for HM1, as that plan
  says.
- **RV1** (review and repair, `docs/plans/review-repair.md`, planned
  beside this one): review findings are a durable object of their own
  (the Program, section 1.4), not decisions. DR1 and RV1 share DS2's
  candidates: a decision may name a candidate as its subject (below), and
  RV1's repair transition, like any new candidate of a task, supersedes an
  approval of the old one (Events, S3). Both add ledger tables; whichever
  merges second takes the next schema version (The changes, the ledger).
- **CS1** (sync, cloud): it replicates decision records later. Every
  change of a decision here is a transition of a run, so the ledger's
  `OnCommit` hook already tells the sync adapter of it (`internal/api/
  sync.go` hands a run's pointer over); the decision document kind on the
  sync allowlist is CS1's plan's.
- **SW1** (Switchman team routing, premium): its open decision O4 (which
  roles, groups, delegates and overrides) is not DR1's (Owner decisions,
  1). DR1 records a policy revision, `local/1`, so that SW1's policies
  are a later revision of the same field.

## Articles it serves

1. No premium code and no licence check: the decision router is asked
   only whether one is registered, once, through `local.Fill`, as C5
   left it; nothing here branches on a plan or a subscription.
2. Fully useful alone: durable decisions, their gates, the operator's
   answer and its application are all here, with nothing registered and
   no network; this is the free build's "required approvals" made
   durable (the Program, sections 2.1 and 5).
3. Paid features gated by hosted dependencies: not touched. Nothing here
   is paid; team routing, quorum, delegation and escalation stay SW1's,
   behind the `ext.DecisionRouter` and `ext.IdentityProvider` seams
   (decision D4).
4. Stop, inspect, revoke, export, required approvals: required approvals
   gain durability and never degrade: a gate holds its tasks until the
   operator answers, whatever is registered and whether or not a provider
   is reachable; silence is never approval (no timer ends a decision). A
   run's stop, pause, cancel, retry and accept act as before; a decision
   is inspected at `GET /api/decisions/{id}`; export carries it in the
   ledger file (C6); revoking a link takes away nothing a decision needs,
   because no link can answer one.
5. Dependencies one way: no new module; `ext` gains one field on two
   records and stays standard library only.
6. No feature build tag: none added.
7. The free default is `main`: with no router registered, the operator
   answers every decision; a session question that no record holds is
   answered first-response-wins exactly as today. A registered router is
   told of each decision and may name destinations, which are recorded;
   it removes no free behaviour, and its answer is not the operator's
   (Owner decisions, 8).
8. A provider whose hosted dependency is unreachable: the router is told
   through C5's guard, which answers the free default within
   `ext.RouteWait` and never blocks a decision's opening; the decision is
   recorded before the router is told.
9. `CGO_ENABLED=0`: no cgo; the records are new tables of the ledger,
   `modernc.org/sqlite`, with a migration of `CREATE TABLE` statements.
10. The repository contract: this plan before code; one issue, branch and
    PR per row of The PRs; race tests per package; the protocol change
    (the attention's `decision` field, one answer refusal, the `decision`
    event) in `internal/proto`, `web/app/utils/protocol.ts` and
    `docs/protocol.md` together, each with its bound and test; no new
    dependency, so no `make notices`.
11. Names: "decision request" (the record), "response" (what a person
    gives), "application" (what Yardmaster does with it), "gate" (a
    decision a plan revision requires). No cloud name; "workspace" never
    means a worktree here.
12. Execution behind one seam: not touched. No process starts here; a
    task released by a decision is dispatched through the engine's
    existing dispatch and `crew.Launcher`.

## Why and what happens

Today a person's answer lives only as long as the session that asked. A
question an agent asks (`ask`, `conductor notify --ask`) is the session's:
open, claimed, resolved, first response wins, with no timer (the shipped
"Needs you", `docs/plans/codex-needs-you.md`). That is right for a
terminal, and wrong for work that waits on a person for hours: a restart
loses the question; an attempt that ends while it waits has spent one of
its task's three attempts; nothing ties the answer to the commit or the
plan revision it was about; and a ticket's plan revision can name
"decisions a person makes" (DS2's `decisions`, `internal/dispatch/
model.go`, `PlanDecision`), but DS2 records and shows them without
holding anything, saying so on the page (`dispatch-evidence.md`, Owner
decisions, 19). The Program asks for the decision as a durable object
with explicit scope (section 1.4), owned by Yardmaster's wait (section
1.4, who owns what), answered by the operator in the free build
(decision D4).

After DR1 ships:

- **An agent asks, and is not held on the line.** In a managed run, an
  attempt's agent calls the MCP tool `decision_request` (or `conductor
  task decide`) with a type (clarification, approval or assignment), the
  question, what happened, the alternatives with their tradeoffs, a
  recommendation and its uncertainty, the evidence, and what a yes would
  authorize. The tool answers at once with the request's id, `dr-7`, and
  `open`, and tells the agent to end its turn or carry on with work that
  does not depend on the answer. Its session shows the question as needing
  the person, and the run page shows the task "waiting for decision dr-7".
- **A plan revision's decisions hold their tasks.** A run launched from a
  ticket's approved plan revision opens one gate per decision the
  revision names, before any attempt, and the tasks each gate names do
  not start until the operator has answered it; the run's other tasks run
  meanwhile.
- **The operator answers where the question is.** On the run page's task,
  in the session's question bar, on the ticket page's Decisions section,
  from the palette, or with `conductor decision respond dr-7`: the card
  shows the question, the alternatives, the recommendation, the evidence
  and the subject (the commit, the candidate, the file's digest or the
  plan revision it is about), and the person picks or writes an answer
  with a rationale. The response is recorded first ("Recorded"), then
  Yardmaster validates and applies it ("Applied: typed into build's
  attempt 2", or "Applied: deploy released"), and the card says each step
  with its receipt. A second press of the button, or a retry after a lost
  reply, gets the same receipt; a response after someone else's is told
  who answered.
- **Nothing is lost or done twice by a restart.** The record is in the
  ledger. A server that restarts keeps every open decision; one answered
  while its run is interrupted is recorded and applied once when the run
  is continued; an answer whose typing was under way at the crash is never
  typed again, and reaches the task's next attempt through its task file.
- **An unanswered decision costs nothing.** An attempt that ends while its
  decision is open is not counted against its task's attempts; the task
  waits, and once the answer is applied its next attempt starts at the
  checkpoint with the answer in its task file. No member is resumed: the
  continuation is a fresh attempt, the harness's supported way (the
  Program, section 1.5).
- **A decision about something that moved says so.** An approval of a
  candidate is superseded when the task gets another candidate, an
  approval of a file when the file's digest changes, and a decision of a
  task that a review reopened or that was cancelled ends with why; the
  card says what changed ("the candidate is now 9f2c1ab, attempt 3"), the
  history stays, and a response to it is refused with the same words.

What a person does not see change: a question no record holds (any
session outside a managed run, a bell, a trust question, the agent's own
`ask`) works exactly as on `main`. The free build's only approver is the
operator: a guest on a share link, even one with control, sees a decision
but cannot answer it.

## The changes

### `internal/crew/ledger`: the decision tables (schema version 8)

The ledger is where decisions live, not a store of their own (Owner
decisions, 2): a decision's opening, its response, its application and
the hold it puts on a task are each one transaction of the run, with the
task's row and the run's event, so a hold and its release can never
disagree with the task they hold.

`Version` goes from 7 to 8; migration 7 runs four `CREATE TABLE`
statements and three `CREATE INDEX` statements, and derives nothing: no
decision existed before it, so a version 7 database's rows are all
correct as they are (the conservative migration rule the owner set for
the version 3 to 4 change). A version 8 database opened by an older
binary opens read-only, as any newer one does today. If RV1's ledger
change merges first and takes version 8, this one is 9 and its migration
is keyed 8; the PR says which it took.

```sql
CREATE TABLE decisions (
  n INTEGER PRIMARY KEY AUTOINCREMENT,      -- the id is "dr-" || n; never reused
  run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE, -- the run it was opened in
  current_run TEXT NOT NULL,                -- the run that holds it now (a continuation's)
  origin TEXT NOT NULL,                     -- agent | plan
  type TEXT NOT NULL,                       -- clarification | approval | assignment
  gate TEXT NOT NULL DEFAULT '',            -- a plan decision's id (origin plan)
  task_id TEXT NOT NULL DEFAULT '',         -- the asking task (origin agent)
  attempt INTEGER NOT NULL DEFAULT 0,       -- the asking attempt
  session_id TEXT NOT NULL DEFAULT '',      -- the asking attempt's session
  member TEXT NOT NULL DEFAULT '',          -- the requesting actor: the member's name, or ''
  ticket TEXT NOT NULL DEFAULT '',          -- Dispatch's ticket id, from the run
  plan TEXT NOT NULL DEFAULT '',            -- the plan revision's id, from the run's package
  plan_digest TEXT NOT NULL DEFAULT '',
  request_id TEXT NOT NULL DEFAULT '',      -- the requester's idempotency key
  fingerprint TEXT NOT NULL,                -- SHA-256 of the request as sent
  request TEXT NOT NULL,                    -- JSON, below
  policy TEXT NOT NULL,                     -- JSON, below
  state TEXT NOT NULL,                      -- open | answered | applied | superseded | cancelled | expired
  rev INTEGER NOT NULL DEFAULT 1,
  why TEXT NOT NULL DEFAULT '',             -- the words of a superseded, cancelled or expired one
  superseded_by TEXT NOT NULL DEFAULT '',   -- "dr-n" of the request that took its place
  applied TEXT NOT NULL DEFAULT '',         -- JSON, the application receipt
  created_at TEXT NOT NULL,
  answered_at TEXT NOT NULL DEFAULT '',
  closed_at TEXT NOT NULL DEFAULT ''        -- set with a final state
);
CREATE UNIQUE INDEX decisions_request ON decisions(run_id, member, request_id) WHERE request_id != '';
CREATE INDEX decisions_current ON decisions(current_run, state);
CREATE TABLE decision_responses (
  decision INTEGER NOT NULL REFERENCES decisions(n) ON DELETE CASCADE,
  k INTEGER NOT NULL,                       -- 1, 2, … in the order received
  actor TEXT NOT NULL,                      -- "admin" or "user:<subject>" (actorOf)
  response_id TEXT NOT NULL,                -- the client's idempotency key
  fingerprint TEXT NOT NULL,
  status TEXT NOT NULL,                     -- accepted | refused
  receipt TEXT NOT NULL,                    -- JSON, the response receipt
  at TEXT NOT NULL,
  PRIMARY KEY (decision, k),
  UNIQUE (decision, actor, response_id)
);
CREATE TABLE decision_history (
  decision INTEGER NOT NULL REFERENCES decisions(n) ON DELETE CASCADE,
  seq INTEGER NOT NULL,
  at TEXT NOT NULL,
  kind TEXT NOT NULL,                       -- below
  by TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL DEFAULT '',          -- JSON, at most 2 KiB
  PRIMARY KEY (decision, seq)
);
CREATE TABLE decision_holds (
  run_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  decision INTEGER NOT NULL REFERENCES decisions(n) ON DELETE CASCADE,
  PRIMARY KEY (run_id, task_id, decision),
  FOREIGN KEY (run_id, task_id) REFERENCES tasks(run_id, id) ON DELETE CASCADE
);
CREATE INDEX decision_holds_decision ON decision_holds(decision);
```

`request` (JSON, at most 16 KiB as encoded): `question` (one line, 1 to
500 characters, Dispatch's `MaxDecisionQuestion` and the attention's
`MaxAttentionMessage`), `context` (Markdown, at most 4,000 characters:
what happened and why a person is needed), `options` (each `{label,
tradeoffs}`, a label of 1 to 80 characters and tradeoffs of at most
1 KiB, Brakeman's `Alternative` bounds; 2 to 6 for an approval, 0 to 6
for a clarification, none for an assignment, whose two answers are fixed:
"Done" and "Not done"), `recommendation` (an option's index, or -1),
`uncertainty` (at most 1 KiB), `authorizes` (what a yes allows, at most
1 KiB, required for an approval), `evidence` (at most 20 refs in
Brakeman's ref syntax, `docs/plans/brakeman.md`, each at most 300 bytes,
checked for shape, resolved when shown), `expertise` (at most 4 labels of
at most 40 characters, the expertise the requester says the answer
needs: recorded and shown; nothing routes by it in DR1), `subject` (below)
and `supersedes` (`dr-n`, an open decision of the same task the request
takes the place of).

`subject`, the exact thing the decision is about, one of: `{kind:
"commit", commit}` (the asking attempt's worktree at its `HEAD` when it
asked, filled in by the server when the request names none), `{kind:
"candidate", run, task, n, commit}` (a task's submitted result, a DS2
candidate), `{kind: "artifact", path, sha256}` (a file of the asking
attempt's worktree, by its path relative to it and its SHA-256, which
the server reads through the worktree's `os.Root` and refuses when it
differs), and `{kind: "plan", plan, digest}` (a gate's: the run's plan
revision).

`policy` (JSON): `{revision: "local/1", approver: "operator", quorum: 1,
authority: [{kind: "operator"}]}`. The only policy of this repository is
`local/1`: one response from a principal with the admin grant (the
workbench token, or an identity provider's principal with
`ext.GrantAdmin`) decides it. The record keeps it so that a response and
an application check the policy the decision was opened under against
the one in force, and so that a later policy is a later revision of the
same field.

`applied` (JSON), the execution owner's application receipt: `{at, by:
"yardmaster", run, task?, attempt?, response, validated: true, how,
tasks?, words}`, where `how` is `typed` (the answer goes to the asking
attempt's live session), `task_file` (its task's next attempt reads it),
`released` (a gate's tasks may start), `cancelled` (a rejected gate's
tasks are cancelled) or `noted` (a gate that holds no task).

`decision_history` kinds, each written in the transaction of the change
it records: `opened`, `routed` (`{to: [{kind, name}], rule, by}`; the
operator by policy, and a registered router's destinations), `notified`
(`{sinks, attention}`: what was told), `responded` (`{k, status}`),
`applied`, `delivery.typing`, `delivery.entered`, `delivery.untyped`,
`delivery.uncertain`, `delivery.task_file`, `commented` (`{text, by}`),
`info_requested` (`{text, by}`), `superseded`, `cancelled`, and, for a
provider only, `assigned`, `delegated`, `escalated` and `expired`, which
no code of this repository writes. At most 256 rows per decision: past
that a `notified` or `delivery.*` row is not written (counted in the
next written row's detail as `dropped`), and every other kind is.

Bounds of the store, checked in the transaction that would pass them
(`decision_limit`): at most 4 decisions open per attempt, 64 open per
run, 1,000 per run in all; at most 32 response rows per decision
(refusals included: past it a response is answered `decision_limit`
without a row).

**Writers and readers** (`decisions.go`, new): `Tx.OpenDecision(row)`
(answers the new `n`, or the existing row for a repeated `request_id`),
`Tx.PutDecision(row)` (state, rev, why, applied, current_run),
`Tx.Respond(n, row)`, `Tx.History(n, kind, by, detail)`, `Tx.Hold(run,
task, n)` and `Tx.Release(run, task, n)`; `Ledger.Decision(ctx, n)` (the
row with its responses and history), `Ledger.Decisions(ctx, Filter)`
(by state, current run, ticket, task; newest first; pages of at most 100
by `n`), `Ledger.Holds(ctx, run)`. `Prune` refuses a run that holds or
opened a decision that is not final (`ErrNotPrunable`: "decision dr-7
is open"); a final one goes with its run. `Recover` changes no decision:
open stays open, answered stays answered. `Snapshot` and `SnapshotFile`
copy the new tables with the rest (`VACUUM INTO`), so an export carries
them unchanged.

### `internal/crew`: Yardmaster's wait and application

A new file `decisions.go`, and small changes where the engine already
decides what dispatches, what an attempt's end costs, what a
continuation carries and what a task file says.

**Types.** `DecisionRequest` (the request above, as decoded),
`Decision` (the record as a run and the API report it: id, origin, type,
refs, request, policy, state, rev, why, superseded by, the responses'
receipts, the application receipt, the history), `Response` (`{responseID,
choice *int, text, rationale, expectedRev, byName}`), `ResponseReceipt`
(`{decision, k, status, code?, words, actor, byName, choice?, text?,
rationale?, at, rev, repeat?}`), `Application` (the `applied` JSON). The
run keeps its open and answered decisions in memory (`run.decisions`,
loaded with the run), and every task its holds (`task.holds`).

**Opening.**

- `Engine.RequestDecision(ctx, sessionID, req)`: the session's attempt
  (`attemptOf`, as `Blocked` finds it) must be running; the request is
  cleaned (`cleanText`) and checked against the bounds above; a
  `subject` of kind `artifact` is read through the worktree; a `candidate`
  must name a submitted result of the run. One transaction: the decision
  row (`origin: agent`, the asking task, attempt, session and member, the
  run's ticket and plan revision, `policy` `local/1`), its `opened` and
  `routed` (to the operator, rule "local/1: the operator answers") history,
  the hold of the asking task, a `supersedes` target set `superseded`
  with `superseded_by` and its hold moved, and the run's event
  `decision.opened`. A repeated `requestId` with the same fingerprint
  answers the existing record and changes nothing; with another,
  `decision_conflict`. After the commit, in this order: the session's
  question mirrors the decision (`session.Local.TryAskDecision`, below), the
  router is told (`internal/api`, below), the browsers and sinks are told.
  The tool's reply never waits for any of them.
- **Gates.** `startManaged`'s first transaction (`transactFirst`, whose
  run row is the one `CreateRun` puts) opens one
  decision per decision the run's launch package names (`Package.Decisions`,
  already decoded in `package.go`: id, question, kind, before), `origin:
  plan`, `gate` the plan decision's id, `subject` the plan revision and its
  digest, an approval's options "Approve" and "Reject", with a hold on
  every task its `before` names. A continuation does not open them again;
  it carries the run's (below). A crew launched without a ticket has no
  gates (Owner decisions, 6).

**Holding.** `Engine.pump` gains one rule beside its `TaskReady` check:
a task with a hold is not dispatched, whatever its needs, and `run.ready`
gives it the reason "waits for decision dr-7", which the Tasks view's
words show (`taskWords`, `web/app/utils/tasks.ts`). A held task's state stays what it is
(`blocked` or `ready`): a hold is a fact beside the state, as a blocker
is, so the ledger's task states, the run's outcome rules and every
reader of them are unchanged. A run whose only unfinished tasks are held
is running and idle; it ends only when its tasks do, so a decision is
never closed by a run ending under it. Pause holds the dispatch as
before; a decision may still be answered while paused, and its released
tasks wait for Unpause.

**An attempt that ends while it waits.** In the transaction that closes
an attempt (`closeAttempt`, `endAttempt`), when the attempt asked a
decision that is open or answered and not applied: the attempt closes
with stage `decision` and is not counted (`t.extra++`, the withdrawal's
mechanism), the task stays held, and `attempt.waited` is the event. The
task's next attempt is dispatched only after the application, at the
checkpoint's commit when there is one (`startCommit`), with the answer in
its task file. A session that ends with its decision applied and typed is
an ordinary end.

**Responding.** `Engine.RespondDecision(ctx, id, resp, actor)`, under the
actor the API names (`actorOf`). With the decision's current run held by
the engine, through its `transact`; with it not held (stopped,
interrupted, not continued), through `Ledger.Transition` on that run
directly, changing no engine state. In one transaction:

1. A row for `(decision, actor, responseId)` already there: its receipt,
   `repeat: true`, nothing else (the same content), or
   `response_conflict` (another).
2. The rechecks, each refusing with a kept receipt whose words say what
   changed: the actor's authority under the decision's policy
   (`not_approver`: "dr-7 is the operator's to answer"); the policy
   revision still the one in force (`stale_policy`); the state `open`
   (`decision_answered`, naming who and when, or `decision_closed`, with
   its `why`); `expectedRev` the decision's (`stale_decision`, with the
   history rows since that rev in words); the subject still current (S1 to
   S4 below; `stale_decision`, the decision superseded in the same
   transaction); the asking attempt still a member of the run chain's task
   (`stale_decision` otherwise). The answer must fit the type: an approval
   and an assignment take `choice`, a clarification `choice` or `text` (at
   most `ext.MaxAnswerText`, 4,096 bytes, cleaned as a submission is) or
   both; a rationale is at most 2,000 characters (`invalid`, no row).
3. Accepted: the response row and its receipt, the decision `answered`
   with `answered_at`, rev + 1, `responded` history, the event
   `decision.answered`.

The reply is the receipt (Recorded). The application follows.

**Applying.** `Engine.applyDecisions(r)`, run after every transaction of a
held run that answered a decision, and by a continuation's first steps.
One transaction per decision: the decision `answered`; the rechecks again
(the subject, the policy, the asking attempt's membership); then:

- origin agent, its attempt live (the same run chain, task, attempt and
  session, the session not ended): `how: typed`; the hold released; the
  delivery intent `delivery.typing`; after the commit the answer is typed
  as one line, "Decision dr-7 (approval) answered by Nate: Approve. Why:
  … (receipt dr-7/1)", through the member's handoff queue, answering the
  mirrored question (`session.Submission.Question` its question id and
  `Decision` its id) when it shows, else as a plain handoff line after
  any held question (`UnlessWaiting`, the messages' rule); `delivery.
  entered` once the Enter is written, `delivery.untyped` when the line
  never went in (the next attempt's task file then carries it);
- origin agent, its attempt gone: `how: task_file`; the hold released, so
  the task's next attempt is dispatched with the answer in its task file
  (`delivery.task_file` when that attempt's files are written);
- origin plan: an approval's "Approve", a clarification's answer and an
  assignment's "Done": `how: released`, every hold released, the answer
  in each released task's task file; an approval's "Reject" and an
  assignment's "Not done": `how: cancelled`, each held task cancelled as
  `CancelTask` cancels it, with the reason "rejected in dr-4 by Nate: …";
  a gate with no task: `how: noted`;
- the application receipt written, the state `applied` with
  `closed_at`, the event `decision.applied`.

A recheck that fails at application supersedes or cancels the decision
with its words (`answered` → `superseded`), keeps the response, and
releases nothing; the asking attempt, when live, is told in one line
("Decision dr-7's answer was not applied: the candidate is now 9f2c1ab").

An agent's approval answered "no" is delivered as words: the agent
decides what follows; only a gate's rejection cancels tasks (Owner
decisions, 9).

**Superseding and cancelling**, each in the transaction of its cause,
with its words in `why` and a history row:

- S1, a candidate subject: a task's new submitted result (another attempt,
  a repair after a review's `needs_changes`, RV1's repair) supersedes
  every open or answered decision whose subject is that task's earlier
  candidate.
- S2, an artifact subject: the file's SHA-256 is read again at response
  and at application while the asking attempt's worktree exists; another
  digest supersedes ("README.md changed: sha256 3c1f… was 9ad0…").
- S3, the asking task withdrawn (a withdrawal, `withdraw`) or reopened
  (`reopen`): its decisions are cancelled ("the review reopened build").
- S4, a commit subject is the tree the question was asked about: shown,
  never rechecked (an agent may go on working on other files).
- A plan subject never moves inside a run: a run is bound to its plan
  revision and a ticket's later edits never change it (DS2's rule), and a
  continuation carries the same package.
- The asking task cancelled (`CancelTask`), or the run's attempt of it
  failing with no attempt left: its decisions are cancelled.
- The operator cancels a decision (`CancelDecision`, a command with a
  receipt): a gate's tasks are released as "cancelled by the operator, not
  approved" only when the operator says so (`release: true`), else
  cancelled; an agent's decision's holds are released and the agent told.
- The asking agent withdraws its own open decision
  (`WithdrawDecision`, the tool `decision_withdraw`): cancelled, its hold
  released.

**Comments and more information.** `Engine.CommentDecision(ctx, id,
opID, kind, text, actor)`: `kind` `comment` (kept in the history) or
`more_info` (kept, and typed to the asking attempt's live session as
"The operator asks about dr-7: …"; the agent answers by a new request
that `supersedes` dr-7). Neither changes the state; both bump `rev`, so
a response made against the card before them is `stale_decision` with
the comment's words. MB0's page offers both; DR1's card offers both too.

**Routing.** `Engine.RouteDecision(ctx, id, opID, to, note, actor)`
records a `routed` history row. DR1's only destination is `{kind:
"operator"}` (a re-route to the operator, with a note: what Brakeman's
`route_decision` grant does in BK2); any other kind is `invalid` until
HM1 adds a person's label. It changes no state.

**Continuations and recovery** (`continue.go`). A continuation's first
transaction moves every open or answered decision of the run it
continues to the continuation (`current_run`), puts its holds on the
continuation's tasks of the same ids, and for a task whose last attempt
was interrupted while it waited on one of them gives that attempt back
(`extra++`), since the crash left no close row to do it. Its next steps
apply the answered ones. `crew.Recovered` (the record that
`internal/api`'s `syncRecords` writes from the ledger at start) shows the
holds. The continuation's note now says "N uncertain launches waiting
for a retry" where it said "waiting for a decision" of something that was
not one.

**The task file** (`taskfile.go`). `task.md` gains "## Decisions" when
the task has any: each decision of the task's run chain that holds it or
that it asked, with its question, its state and, once applied, the
answer, the rationale and who gave it; `task.json` gains `decisions`
with the same, in the record's words. The section is bounded like the
others and cut first by `fitTaskFile` into `task-more.json` after the
references. The package's "Decisions a person makes" section stays and
now ends "Conductor holds the tasks they name until the operator
answers" instead of asking the agent to ask.

**What the run reports.** `TaskState` gains `holds` (`[{id, type,
state, question}]`) and the run `decisions` (open and answered counts);
`Engine.Decision`, `Engine.Decisions` read through the ledger.

### `internal/session`: the mirrored question

- `Attention` gains `Decision` (the record's id, `dr-<n>`, at most 16
  bytes), sent to viewers and in `Info` (`internal/proto`, below).
- `Local.TryAskDecision(id, message, options)` asks as the agent's own
  question does (`TryAskID`, its id answered) with `Decision` set and
  quick-reply options none: the answer is the decision's, never keys.
- A question with `Decision` set follows the agent's own question's rules
  (set aside while the agent works, shown again at its turn's done) with
  three differences: a person's keys, the screen moving on and a line
  naming no decision never take it away; an `answer` (`AnswerInput`,
  `AnswerAs`) or a `Submit` naming its id is refused with
  `ErrDecisionQuestion` ("dr-7 is answered as a decision") unless the
  submission's `Decision` is that id, which only the engine's application
  sets; and it leaves the session with the decision's application
  (answered by the typed line), its supersession or cancellation
  (`WithdrawQuestion`), or the session's end.

### `internal/proto`, `web/app/utils/protocol.ts`, `docs/protocol.md`

- The `attention` control message gains `decision` (`dr-<n>`, at most
  `MaxDecisionRef`, 16 bytes; absent from every other question and from
  an older owner). `TestAttentionDecisionBound` (in `internal/proto`)
  holds the bound; the message's own bound (`MaxAttentionMessage` for its
  text) is unchanged.
- An `answer` naming a decision's question is refused with the existing
  answer-result frame and a new code, `decision` (its words naming the
  decision): `TestAnswerToADecisionIsRefused` in `internal/api` (server
  WebSocket) and `internal/hostagent` (whose peer refuses the same way,
  though no hosted session asks one: the code is shared).
- The event stream (`GET /api/events`) gains `event: decision`, data
  `{id, run, task?, state, rev}` (at most 256 bytes;
  `TestDecisionEventIsBounded`), sent after each committed change of a
  decision, so a page reads it again.
- `docs/protocol.md`: the Attention section (the field), Answering a
  question (the refusal), Events (the event and the webhook event below),
  and a new HTTP API section "### Decisions" with every route, body,
  bound, reply and refusal code.

No hosted-session message changes: a managed run's attempts are server
sessions, so nothing of a decision crosses to a host; `answer_as` (C5) is
unchanged.

### `ext` and `ext/exttest`: the router told of decisions

- `ext.Decision` gains `Request string` (`json:"request,omitempty"`): the
  durable decision this is, `dr-<n>`, "" for a question no record holds.
  `ext.Resolution` gains the same. Additive: a provider built against
  C5's records compiles and behaves unchanged.
- `internal/api/router.go`: a session question with `Decision` set is not
  routed as a session question (its record is). The decision record is
  routed once, when it opens, through the same guard (`routeSent`, its
  wait and its slots): `ext.Decision{ID: "dr-7", Request: "dr-7",
  Session: <the asking session, or an empty id for a gate, with run and
  task>, Message: the question cut to `ext.Decision`'s 500 bytes at a
  character's end, Kind: "decision" (a new value beside `permission` and
  `prompt`, documented on the field), Options: the labels, Since}`; its
  destinations become a `routed` history row; `Resolved` is told when the
  decision reaches a final state, through the existing ordered list. An
  answer the router holds is offered to `RespondDecision` under
  `user:<By.Subject>` with no grant, which `local/1` refuses
  (`not_approver`): recorded as a refused response and a `routed` row's
  `answer: "refused under local/1"`, never applied (Owner decisions, 8).
- `ext/exttest`'s fake router records `Request`; the `Router` contract
  check gains a case with it set and one with it empty.

### `internal/api`: the routes

`decisions.go` (new), registered in `server.go`, each route's body read
with `DisallowUnknownFields` and bounded:

| Route | Who | Body → reply |
|---|---|---|
| `POST /api/sessions/{id}/decisions` | the attempt's own token (scope task) | `{requestId, type, question, context?, options?, recommendation?, uncertainty?, authorizes?, evidence?, expertise?, subject?, supersedes?}` (24 KiB) → `201 {decision}` at once; `200` the same for a repeat; `409 decision_conflict`, `409 not_running`, `429 decision_limit`, `400 invalid` |
| `GET /api/sessions/{id}/decisions` | the attempt's own token | its task's decisions (`{decisions}`, at most 100) |
| `GET /api/sessions/{id}/decisions/{did}` | the attempt's own token | one of its task's, whole |
| `POST /api/sessions/{id}/decisions/{did}/withdraw` | the attempt's own token | `{}` → `{decision}`; `409 decision_closed` |
| `GET /api/decisions` | admin | `?state=&run=&ticket=&after=&limit=` (100) → `{decisions, next?}`, newest first, without history |
| `GET /api/decisions/{did}` | admin | the record whole (at most 512 KiB: 16 KiB request, 32 receipts, 256 history rows) |
| `POST /api/decisions/{did}/respond` | admin | `{responseId, choice?, text?, rationale?, expectedRev, byName?}` (8 KiB) → `201 {receipt, decision}`; `200` repeat; `409 decision_answered`, `decision_closed`, `stale_decision`, `stale_policy`, `response_conflict`; `403 not_approver`; `429 decision_limit` |
| `POST /api/decisions/{did}/cancel` | admin | `{commandId, why, release?, expectedRev}` → `{receipt, decision}` |
| `POST /api/decisions/{did}/comment` | admin | `{opId, kind: comment\|more_info, text, expectedRev}` (8 KiB) → `{receipt, decision}` |
| `POST /api/decisions/{did}/route` | admin | `{opId, to: {kind: "operator"}, note?, expectedRev}` → `{receipt, decision}` |

`responseId`, `commandId` and `opId` take the shape the run commands'
ids take (1 to 32 of `A-Z a-z 0-9 . _ ~ -`); the web mints them and
retries a lost reply once with the same id, as `utils/tasks.ts` does.
A share link's controller, an agent's token and a Brakeman turn's token
are refused every admin route (`requireAdmin`'s `401 unauthorized`), so
the free build's one approver is the operator; `403 not_approver` is the
policy's refusal of a principal that got past it without the authority
`local/1` names (a router's answer, below).

- The guide (`guide.go`, the one table of operations): `decision_request`,
  `decision_read` and `decision_withdraw` (scope task, MCP tools with
  their schemas), and `decision_respond`, `decision_cancel`,
  `decision_comment` and `decision_route` (scope admin, `Receipt:
  "ledger"`, `Revision: "decision"`, a new precondition value the guide
  documents). `guideRules`' third line becomes "A question goes to a
  person with ask; in a managed run, one whose answer the work must wait
  for goes with decision_request."
- The run and task reads (`GET /api/runs/{run}`, `GET /api/runs/{run}/
  tasks/{task}`) carry `holds` and `decisions` from the engine.
- **Notifications.** After each committed change of a decision the server
  hands the notifier one `ext.Notification` (`Event: "decision"`, `Type:
  "decision"`, `Session` the asking session or the run and task with no
  id, `State` the decision's state, `Message` the question, `URL` the run
  page's task with `&decision=dr-7`, `By` the responder's actor): the
  webhooks and the registered sinks take it as they take an activity
  entry (`nt.offer`'s queue, never blocking the hub). `config.
  ValidWebhookEvent` accepts `decision` (`EventDecision`). An agent's
  decision also shows as its session's attention, so a webhook on
  `needs_input` fires for it as it does today; the `decision` event is
  the one a gate has.
- **The ticket page's decisions.** `GET /api/dispatch/tickets/{key}/
  decisions` (admin): the decisions whose `ticket` column is the ticket's
  id, newest first, at most 100. The `decision` link kind stays reserved
  (Owner decisions, 12).
- **Brakeman's reads.** The shared reads Brakeman's `observe` reaches are
  unchanged; `observe_decisions` and the `decision:<id>` ref are BK2's
  PR-12 (`docs/plans/brakeman.md`).

### `internal/cli` and `internal/agents`

- MCP tools `decision_request`, `decision_read` and `decision_withdraw`
  in `mcpHandled`, listed only in an attempt's session (scope task, as
  `task_blocked`), each one call to its route; `decision_request`'s reply
  is the record's id, its state and one line: "Pending. End your turn, or
  go on with work that does not depend on it: the answer is typed into
  this session as 'Decision dr-7 … answered', or, if this session has
  ended, written into your task's next attempt's task file."
- `conductor task decide --type T --question Q [--context TEXT]
  [--option "LABEL[: TRADEOFFS]"]... [--recommend N] [--uncertainty
  TEXT] [--authorizes TEXT] [--evidence REF]... [--subject commit|
  candidate:RUN/TASK/N|artifact:PATH] [--supersedes dr-N] [--file
  JSON]`, `conductor task decision [dr-N]` (read) and `conductor task
  withdraw dr-N`, loud outside a session as the other `task` commands
  are.
- `conductor decision list [--state S] [--run R] [--ticket KEY]`,
  `conductor decision show dr-N`, `conductor decision respond dr-N
  (--choice N | --text TEXT) [--why TEXT]`, `conductor decision cancel
  dr-N --why TEXT [--release]`: the admin's, with `--server` and the
  workbench token as `conductor dispatch` takes them; `respond` reads the
  record's `rev` first and sends it, printing the receipt.
- The skill (`internal/agents/skill.go`), "Your task" section: a
  paragraph on decisions (when to use `decision_request` rather than
  `ask` or `task_blocked`, that the tool returns at once, and how the
  answer arrives), in the same table-built words the guide uses.
- Completion text for the new commands (`completion.go`).

### `web/app`

- `utils/decisions.ts`: the record's types; `decisionWords` (the state in
  words, and the four states shown apart: open, response recorded,
  validated by Yardmaster, applied or why not, the last three each with
  its receipt and time); `respondDecision`, `cancelDecision`,
  `commentDecision` with the id minted and a lost reply asked once more
  with the same id; a refusal's words from its receipt.
- `composables/useDecisions.ts`: the open and answered decisions from
  `GET /api/decisions`, read again on a `decision` event from the one
  events stream `useAttention.ts` holds (no second stream).
- `components/DecisionCard.vue`: the record: the question, the context
  (Markdown, as Dispatch renders a description), the alternatives with
  their tradeoffs and the recommended one marked, the uncertainty, what a
  yes authorizes, the evidence refs as chips (`BrakemanRefChip.vue`'s
  resolution of refs, reused), the subject (a commit or a candidate with
  its short id linking to the task, a file with its digest, a plan
  revision as `r3`), who may answer ("the operator", from the policy),
  the history, and the four states. `components/DecisionRespond.vue`:
  the answer controls by type (a button per option, a text field for a
  clarification, a rationale field, Comment and Ask for more), an
  approval's "Approve"/"Reject" asked first in a confirm, the receipt
  shown, a refusal's words (who answered first; what changed) with Read
  again. Brand palette and Nuxt UI only; the junction mark is not used.
- The run page (`RunTasks.vue`, `taskWords`, `utils/tasks.ts`): a held
  task's words and an Answer action opening the card in a modal; the
  task detail lists its decisions; the header counts "1 decision open".
- The session: `AttentionBar.vue` shows a question with `decision` as
  the decision's question with "Answer dr-7", opening the card, and offers
  no quick reply or number key for it (`utils/attention.ts`).
- Needs you: `NeedsYouPill.vue` and the sidebar's Needs you count include
  open decisions that no shown session question mirrors (a gate's), each
  once; the palette (`usePaletteRows.ts`) gains a row per open decision
  ("Answer dr-7: …"), Enter opening its card.
- The ticket page (`DispatchDecisions.vue`, `useTicketRuns.ts`'s
  neighbour `useTicketDecisions.ts`): the ticket's decision records with
  their states and receipts, replacing DS2's "recorded and shown, not
  held" note.
- `utils/protocol.ts`: the attention's `decision`, the answer refusal
  code, the `decision` event.

### `web/e2e`

- The stub worker (`stub-agent.sh`) gains `STUB_DECISION=approval|
  clarification`: before committing, it asks one decision through
  `conductor mcp` (or the CLI under `STUB_WORKER=cli`), writes its id to
  `stub-decision.txt`, ends its turn, and on the typed line "Decision
  dr-N … answered" writes the answer into `stub-answer.txt` and goes on
  to commit and submit; `STUB_DECISION_EXIT=1` makes it exit after asking,
  so its attempt ends while it waits.

### Docs

- `docs/protocol.md` as above; `docs/repository-map.md`'s rows for
  `internal/crew` (`decisions.go`) and `internal/crew/ledger` (schema
  version 8 and the decision tables); `docs/architecture.md`'s crew-runs
  section gains a paragraph on decisions; `docs/features.md` the item's
  entry, in the last PR.

## Events, orderings and invariants

**States of a decision.** `open` → `answered` (a response accepted) →
`applied` (Yardmaster validated and applied it). `open` or `answered` →
`superseded` (its subject moved, or a new request took its place) or
`cancelled` (its task cancelled, withdrawn or reopened; the operator; the
asking agent, while open). `open` → `expired` is a provider's only; no
code of this repository sets it. `applied`, `superseded`, `cancelled` and
`expired` are final: nothing changes a final decision but a `commented`
history row. Every change bumps `rev`, writes one history row and one
run event (`decision.opened`, `decision.answered`, `decision.applied`,
`decision.superseded`, `decision.cancelled`, `attempt.waited`) in the
same transaction as the task and hold rows it changes.

**Orderings.**

- O1, an agent's request: the decision row, its history, the hold and the
  event commit; then the reply (`201`); then, after the commit and never
  before it, the session's mirrored question, the router's Route (through
  C5's guard), the `decision` event and the notification. A crash after
  the commit and before the reply leaves an open decision the agent's
  retry with the same `requestId` finds (`200`, the same record).
- O2, a gate: opened in the run's first transaction with its tasks' rows,
  so no attempt of a held task can be reserved before its hold exists.
- O3, a response: authenticated (`requireAdmin`), decoded, then one
  transaction: the repeat check, the rechecks, the row and its receipt,
  `answered`. The reply follows the commit. The application is a later
  transaction, so "recorded" and "applied" are two facts with two times.
- O4, the application: one transaction rechecks and writes the receipt,
  `applied`, the hold's release (or the gate's cancellations) and the
  delivery intent; the typing or the next dispatch follows its commit,
  and `delivery.entered` or `delivery.untyped` is written after.
- O5, an attempt that ends while it waits: the close transaction gives the
  attempt back (`extra++`) and keeps the hold; the next attempt is
  reserved only after O4 released it.
- O6, a continuation: its first transaction re-points the decisions and
  their holds and gives back an interrupted waiting attempt; the
  application of the answered ones runs after it, in their own
  transactions.
- O7, a supersession or cancellation is in the transaction of its cause
  (the new result's, the withdrawal's, the cancel's), so no application
  can run between the cause and its effect on the decision.

**Invariants.**

- I1, applied at most once: the application transaction requires
  `answered` and sets `applied`; two applications of one decision cannot
  both commit, and a repeated response changes nothing.
- I2, a held task is never dispatched: `pump` reads the holds under the
  engine's lock, and a hold is released only in O4 or by a
  cancellation.
- I3, an unanswered decision consumes no attempt: an attempt closed while
  its task's decision is open or answered and unapplied is given back, in
  its close (O5) or in the continuation (O6).
- I4, an answer reaches only the work it is for: it is typed only into
  the session of the attempt that asked, while that attempt is live and a
  member of the same task in the same run chain; otherwise it reaches that
  task's next attempt through its task file; never another task's
  attempt, never a member resumed.
- I5, silence is never approval: no timer, no default option and no
  router answer under `local/1` moves a decision out of `open`; a gate's
  tasks start only after an accepted response's application.
- I6, a typing is never repeated: `delivery.typing` is written before the
  line; a restart that finds it without `delivery.entered` or
  `delivery.untyped` writes `delivery.uncertain` and never types it
  again; the task's next attempt reads it from its task file.
- I7, a response is accepted only from a principal the decision's policy
  names, under the policy revision it was opened with, against the rev
  the person saw.
- I8, a decision that is not final is never deleted: `Prune` refuses its
  run.
- I9, one question, one answer path: a mirrored session question is
  answered only through its decision; the session refuses every other
  answer to it.

**What the ledger holds** after each step is the record above; the run
record (`runs/<id>.json`) is a cache of the ledger, as it is today, and
gains the tasks' `holds`.

**Migration.** Version 7 to 8: the four tables and three indexes, in one
transaction with the version's change; nothing read, nothing derived; a
version 7 database's runs, tasks and attempts are unchanged and hold no
decision. Tested by opening a version 7 database made by `main`'s code
(`TestMigrationSevenToEightAddsOnlyTables`).

**Conflict rules.** None of replication: a decision is a local record of
one ledger. Inside one server, the run's transactions order every change;
first response wins between people, by the order the transactions
commit.

## Out of scope

- A durable record for questions outside managed runs (a plain session's
  questions, the bell, a trust question, the agent's `ask`): they keep
  `main`'s first-response-wins (Owner decisions, 5).
- People as crew members and routing a decision to a person's label:
  HM1, in its own subsections of this file.
- The "Needs your decision" page and its phone layout: MB0
  (`docs/plans/decision-inbox.md`).
- Authenticated responders beyond the admin grant, domain roles, quorum,
  delegation, escalation, quiet hours and expiry: SW1 and ID1 (decision
  D4).
- Replicating decisions and keeping a managed copy while the owner is
  offline: CS1.
- Push notifications: MB1 (the sinks take the `decision` notification
  already).
- Brakeman's `route_decision` grant, its `decision:` ref and
  `observe_decisions`: BK2's PR-12 and PR-13 (`docs/plans/brakeman.md`).
- Review findings and Request changes: RV1.
- Gates defined on a saved crew without a ticket (Owner decisions, 6), and
  gates added to a running run: a plan revision is the one place gates
  come from, and a running run is bound to its revision (the Program,
  section 1.5).
- Live plan revision and member resume of a managed run: still refused
  (`409 managed_run`).

## Tests per surface

Per `docs/checks.md`. Every PR runs `make lint`, `go test -race -count=1
-timeout 30m ./...` and `make lint-static` for its Go; a PR with web
changes `npm --prefix web run typecheck`, `npm --prefix web test` and
`make test-e2e` (its specs alone first while iterating); the DCO check
before the push. No Python, desktop, network or recipe surface changes,
so the scripts suite, `make desktop-test`, `make test-network` and `make
test-recipes` are not affected. The tests follow AGENTS.md's rules: each
waits on the record (`decisionWhen`, polling the ledger for the state it
needs) never on a clock, reads the ledger after the commit it waits for,
and holds a step at a seam to order what must be ordered.

**Go, race, by package.**

- `internal/crew/ledger` (`decisions_test.go`):
  `TestMigrationSevenToEightAddsOnlyTables` (a version 7 database from
  `main`'s schema, its rows unchanged, the tables empty);
  `TestDecisionIDsAreNeverReused` (a pruned run's `n` not given again);
  `TestARepeatedRequestIDIsTheSameDecision`;
  `TestAResponseIDAnswersItsReceipt` (accepted and refused alike);
  `TestAnOpenDecisionBlocksThePrune`; `TestDecisionBounds` (4 per
  attempt, 64 open per run, 1,000 per run, 32 responses, 256 history
  rows with `dropped`); `TestRecoverChangesNoDecision`;
  `TestSnapshotCarriesDecisions`.
- `internal/crew` (`decisions_test.go`, `decisions_restart_test.go`, with
  the fake ledger and fake sessions of `managed_test.go`):
  `TestAnAgentsDecisionHoldsOnlyItsTask` (a sibling task dispatches and
  finishes meanwhile); `TestAGateHoldsItsBeforeTasks` (gate on `deploy`;
  `build` runs; `deploy` starts only after the application);
  `TestTheReplyComesBeforeTheMirror` (a seam holding the session's ask:
  the request's reply returned); `TestAResponseIsAppliedOnce` (two
  application passes, one receipt, one typed line);
  `TestAnAttemptEndedWhileWaitingIsNotCounted` (attempts 1 to 3 each end
  waiting; the task never fails for attempts; attempt 4 starts after the
  answer at the checkpoint, the answer in its task file);
  `TestAnAnswerRecordedWhileInterruptedIsAppliedOnContinue`;
  `TestAnInterruptedWaitingAttemptIsGivenBack`;
  `TestATypingCutByARestartIsNeverTypedAgain` (`delivery.typing` without
  its end; the continuation writes `delivery.uncertain` and the task
  file); `TestAnAnswerNeverReachesAnotherAttempt` (the asking attempt
  gone, another task's attempt live: nothing typed into it);
  `TestANewCandidateSupersedesItsApproval` (S1);
  `TestAnArtifactThatChangedSupersedes` (S2, the file rewritten between
  the response and the application); `TestAReopenCancelsTheTasksDecisions`
  (S3); `TestARejectedGateCancelsItsTasks`;
  `TestTheRunWaitsOnAHeldTask` (no outcome while only held tasks remain);
  `TestPauseHoldsReleasedTasks`; `TestTheTaskFileCarriesTheAnswer`
  (golden, with `taskfile_golden_test.go`'s fixtures);
  `TestASupersedingRequestMovesTheHold`; `TestMoreInfoIsTypedAndBumpsRev`.
- `internal/session` (`decision_test.go`):
  `TestADecisionQuestionRefusesEveryOtherAnswer` (`answer`, `AnswerAs`,
  `Submit` with its id); `TestADecisionQuestionOutlastsKeysAndTheScreen`;
  `TestADecisionQuestionIsAnsweredByItsToken` (the engine's submission
  answers it, recorded by the responder's name);
  `TestADecisionQuestionIsSetAsideAndShownAgain`.
- `internal/proto`: `TestAttentionDecisionBound`.
- `internal/api` (`decisions_test.go`, `decisions_router_test.go`):
  `TestADecisionRequestAnswersAtOnce` (the reply before the mirror and
  the router, through a seam holding both);
  `TestOnlyTheOperatorResponds` (the workbench token and an identity
  principal with the admin grant accepted; a share link's controller,
  the agent's token and a principal without the grant refused);
  `TestASecondResponseNamesTheFirst`; `TestARetriedResponseIsTheSameReceipt`;
  `TestAStaleRevExplainsWhatChanged`; `TestTheDecisionEventFollowsEachCommit`
  and `TestDecisionEventIsBounded`; `TestASinkGetsTheDecisionNotice` (with
  `ext/exttest`'s fake sink, never blocking the hub);
  `TestTheRouterIsToldEachDecisionOnce` (and never the mirrored session
  question); `TestARoutersAnswerIsRefusedUnderLocalPolicy`;
  `TestAnswerToADecisionIsRefused` (the WebSocket's `answer`);
  `TestTheGuideListsTheDecisionOperations` (and its bounds, measured by
  `TestEachSectionIsBounded`); `TestATicketListsItsDecisions`;
  `TestTheWebhookTakesDecision`.
- `internal/hostagent`: `TestAnswerToADecisionIsRefused` (the peer's
  shared refusal).
- `internal/config`: `TestDecisionIsAWebhookEvent`.
- `ext/exttest`: the `Router` check with `Request` set and empty.
- `internal/cli` (`decision_test.go`, `task_test.go`, `mcp_test.go`):
  `TestTaskDecideAndRead`, `TestTaskWithdraw`,
  `TestDecisionRespondSendsTheRev`, `TestMCPListsDecisionToolsOnlyInATask`,
  `TestDecisionRequestToolAnswersPending`.
- `internal/agents`: the skill test gains the decisions paragraph.

**vitest.** `utils/decisions.test.ts` (the words of every state; the four
states apart, each with its receipt; the id minted once and a lost reply
retried once with it; a refusal's words), `utils/attention.test.ts` (a
decision's question offers no quick reply or number key),
`utils/tasks.test.ts` (a held task's words and its Answer action),
`composables/useDecisions.test.ts` (read again on a `decision` event,
not on others), the palette rows' test.

**Playwright** (one run per worktree; `make test-e2e` builds the web app
and the binary):

- `decisions.spec.ts`: a crew of two tasks, `build` (the stub worker with
  `STUB_DECISION=approval`) and `docs` (no decision); `docs` finishes
  while `build` waits; the run page shows "waits for decision dr-1"; the
  session's bar shows the question with Answer and no number keys; the
  operator answers in the card; the card shows Recorded, then Applied with
  "typed into build's attempt 1"; `stub-answer.txt` holds the answer;
  `build` succeeds; pressing Approve again in a second tab shows "answered
  by" the first. The palette's row opens the card.
- `decisions-gate.spec.ts`: a ticket's plan revision with a gate on
  `deploy` (DS2's propose and approve through the pages, as
  `dispatch-plan.spec.ts` does); the launch; `build` runs and succeeds;
  `deploy` waits with its words; the ticket page's Decisions section lists
  dr-1 open; Approve; `deploy` starts and the run succeeds; a second run's
  Reject cancels `deploy` with the reason.
- `decisions-restart.spec.ts`: a server of its own
  (`tasks-resume.spec.ts`'s pattern); the stub with `STUB_DECISION_EXIT=1`
  asks and exits: its attempt says "waiting for decision", attempts 1 of
  3 not spent; the server is ended and started again on the same data;
  the decision is still open; the operator answers (Recorded, "waiting
  for the run to be continued"); Continue; the task's next attempt starts
  with the answer in its task file (read through the task route), the
  decision Applied once (the record's history has one `applied` row),
  and the task succeeds.

Each spec waits for what the listener draws before its first key, waits
on the record's state through the API rather than on the page's clock,
ends its strays first (`stopStrays`) and finds its run by id, and raises
its timeout by the caps of its steps (`testInfo.setTimeout`), per
AGENTS.md.

**By hand** (what tests cannot cover): the acceptance check below, with
real Claude Code and Codex, which a stub cannot stand in for: that a real
agent uses `decision_request` from the skill and the guide alone, ends its
turn, and takes the typed answer as the person's; and the run page's card
read by a person on the desktop app and a phone's browser on the LAN
(MB0's page is MB0's check).

## Boxcar constraints

1. The engine starts an attempt only through `crew.Launcher`: honoured; a
   released task is dispatched by the engine's existing dispatch, which
   reserves and launches through the Launcher; nothing here spawns a
   process.
2. `session.Local` drives a `session.Process`: honoured; the mirrored
   question and the typed answer go through `Local`'s attention and
   `Submit`, never past `Process`; no new `syscall.Kill`.
3. An attempt's context is files relative to its worktree: honoured; an
   answer that reaches a next attempt is in its `.conductor/task.md` and
   `task.json`, written through the worktree's `os.Root` as today; an
   artifact subject is read through the same root; nothing an attempt
   needs is only in the server's memory (the record is in the ledger, the
   answer in the file).
4. The agent talks to the server over the network: honoured; the tools
   and the CLI reach the new routes over HTTP with
   `CONDUCTOR_NOTIFY_TOKEN`, as `task_blocked` does.
5. Checks go through `internal/proc`: not touched; no check is run here.
6. Platform splits stay platform tags: not touched; nothing
   platform-specific is added.
7. Evidence rows gain `where`: not touched; a decision is not evidence of a
   process and assumes none (an artifact's digest is read through the
   worktree, wherever the worktree is mounted); no row here assumes a
   native process.
8. `hostagent` is a launch site: not touched for launching; the host's
   peer only shares the session package's refusal of an answer to a
   decision's question.

## Open-core checklist

- [x] Article 1: no premium code and no licence check; the router slot is
      read once through `local.Fill`, as C5 left it.
- [x] Article 2: durable decisions, gates, the operator's answer and the
      application all work in a plain build with nothing registered.
- [x] Article 3: nothing here is paid; team routing and authenticated
      approvers stay behind the existing seams (SW1, ID1).
- [x] Article 4: required approvals become durable and hold their tasks
      whatever is registered or reachable; stop, inspect (`GET
      /api/decisions/{id}`), revoke and export are untouched or extended,
      and none can lapse.

## The PRs

Each PR is one issue and one branch `issue-N` from `main`, cut after the
PRs it depends on have merged; it leaves `main` working and green on its
own, ticks the checklist above, names its checks, and updates the
`docs/repository-map.md` and `docs/protocol.md` rows it changes. Sizes
are changed lines including tests. Every PR builds on `main` as it is
(DS2 and C5 are Done); no PR waits on another item. "May run beside"
names only PRs that share no package or directory with it.

| PR | Title | Touches | Depends on | May run beside | Size |
|---|---|---|---|---|---|
| PR-1 | ledger: the decision tables (schema version 8), their writers, reads, bounds, prune and migration | `internal/crew/ledger`, `docs` | none | none | ~1,000 |
| PR-2 | crew: decisions opened by an attempt and by a plan's gates, the holds, the response, the application to a live attempt | `internal/crew`, `internal/session` | PR-1 | none | ~1,400 |
| PR-3 | crew: an attempt that ends while it waits, continuations and recovery, supersession and cancellation, comments, the task file | `internal/crew` | PR-2 | PR-4 | ~1,300 |
| PR-4 | api: the decision routes, the guide's operations, the event, the notifications, the ticket's decisions, the run's holds | `internal/api`, `internal/config`, `docs` | PR-2 | PR-3, PR-6 | ~1,400 |
| PR-5 | session, proto, ext: the mirrored question and its refusal, the attention's `decision`, the router told of decision records | `internal/session`, `internal/proto`, `internal/api`, `internal/hostagent`, `ext`, `ext/exttest`, `web/app/utils`, `docs` | PR-3, PR-4 | PR-6 | ~1,100 |
| PR-6 | cli, agents: the MCP tools, `conductor task decide`, `conductor decision`, the skill | `internal/cli`, `internal/agents` | PR-4 | PR-3, PR-5 | ~900 |
| PR-7 | web: the decision card and its answer, the run page, the session's bar, Needs you, the palette, the ticket page; the stub worker and two specs | `web/app`, `web/e2e` | PR-5, PR-6 | none | ~1,400 |
| PR-8 | e2e and docs: the restart spec, the gate's rejection, `docs/architecture.md`, the features entry | `web/e2e`, `docs` | PR-7 | none | ~500 |

**PR-1** (`internal/crew/ledger`, `docs/repository-map.md`): the schema
and migration of The changes, `decisions.go`'s `Tx` writers and the
`Ledger` reads, the bounds, `Prune`'s refusal, `Recover` and the
snapshot unchanged; no caller yet, so no behaviour changes. Tests: the
ledger's list above. If RV1's ledger PR has merged, this one takes the
next version and says so.

**PR-2** (`internal/crew`, `internal/session`): `decisions.go`'s types,
`RequestDecision` with its bounds and subjects, the gates in the first
transaction, `pump`'s hold rule and the held task's words,
`RespondDecision` (through `transact` or the ledger directly) with every
recheck but the ones PR-3 adds, `applyDecisions` for a live attempt and
for a gate (typed through the handoff queue; released; cancelled), and
`session.Local.TryAskDecision` with the session's refusal of every other
answer to it (the attention's field stays unexported to viewers until
PR-5). No route yet: the engine is driven by its tests. Tests: the
`internal/crew` list's opening, holding, applying and gate tests, and the
`internal/session` list.

**PR-3** (`internal/crew`): the attempt given back while it waits (O5),
the continuation's carry and give-back (O6), the
typing's intent and its uncertain end (I6), S1 to S3 and every
cancellation, `WithdrawDecision`, `CancelDecision`, `CommentDecision`,
`RouteDecision`, the task file's section and the package section's new
last line, the continuation note's words, `crew.Recovered`'s holds. Tests: the rest of the
`internal/crew` list, `decisions_restart_test.go`, the task file golden.

**PR-4** (`internal/api`, `internal/config`, `docs/protocol.md`): the
routes table, the guide's seven operations and its rule line, the run and
task reads' `holds`, the `decision` event, the notification and
`EventDecision`, the ticket's decisions route. Tests: the `internal/api`
list but the router's and the WebSocket's, and `internal/config`'s.

**PR-5** (`internal/session`, `internal/proto`, `internal/api/router.go`,
`internal/hostagent`, `ext`, `ext/exttest`, `web/app/utils/protocol.ts`,
`docs/protocol.md`): the attention's `decision` in the three places, the
`answer` refusal code, `ext.Decision.Request` and `ext.Resolution.Request`,
the router seam's skip of mirrored questions and its routing of records,
a router's answer refused under `local/1`, the fake router and its check.
Between PR-4 and this PR a viewer's reply to a mirrored question is
refused with the session's general refusal; this PR gives it its code and
its words, and takes the reply box off the question.
Tests: `TestAttentionDecisionBound`, the two
`TestAnswerToADecisionIsRefused`, `decisions_router_test.go`, the
`ext/exttest` check.

**PR-6** (`internal/cli`, `internal/agents`): the three MCP tools, the
`task` subcommands, `conductor decision`, completion, the skill
paragraph. Tests: the `internal/cli` and `internal/agents` lists.

**PR-7** (`web/app`, `web/e2e`): `utils/decisions.ts`,
`useDecisions.ts`, `useTicketDecisions.ts`, `DecisionCard.vue`,
`DecisionRespond.vue`, the run page, `AttentionBar.vue`, Needs you, the
palette rows, `DispatchDecisions.vue`; the stub worker's
`STUB_DECISION`; `decisions.spec.ts` and the approve half of
`decisions-gate.spec.ts`. Tests: the vitest list and those two specs.

**PR-8** (`web/e2e`, `docs`): `decisions-restart.spec.ts` with
`STUB_DECISION_EXIT`, the rejection half of `decisions-gate.spec.ts`,
`docs/architecture.md`'s paragraph, and the item's `docs/features.md`
entry (what shipped, the tests, and the acceptance observation once it is
made).

## Owner decisions

Dated 2026-11-05. Each is a default the owner may overturn with a row in
the Program's status log, its convention for a decision the owner did
not choose (the Program, section 3.1).

1. **The open decisions named with this item are not its own.** O4
   (human policy v1) blocks SW1, and O10 (Dispatch's merge rules) blocks
   CS1; DR1 is local and has one approver (decision D4), so it resolves
   neither. It records the policy as `local/1` and the record's fields so
   that SW1's policy and CS1's replication are later revisions, not
   migrations of meaning. No question is left open by this plan.
2. **Decisions live in the ledger**, as new tables of `runs/managed.
   sqlite`, not a store of their own: a hold, its release and the task it
   holds commit together, which a second database could not promise.
3. **The id is `dr-<n>`**, from the ledger's `AUTOINCREMENT`, never
   reused in a data directory; a replicated id is CS1's to qualify.
4. **Three types:** clarification (a question with an answer), approval
   (a yes or no, or one of the requester's alternatives, about an exact
   subject) and assignment (something a person does by hand, answered
   Done or Not done).
5. **Only managed runs raise durable decisions**: an attempt's agent
   through `decision_request`, and a plan revision's gates. Every other
   question keeps `main`'s first-response-wins, unchanged.
6. **Gates come only from an approved plan revision**, opened at the
   launch and carried by continuations; a saved crew launched without a
   ticket has none.
7. **The approver is the operator**: a principal with the admin grant
   (the workbench token, or an identity provider's admin principal). A
   share link's controller, an agent and Brakeman may read a decision
   where they may read the run, and never answer it.
8. **A router's held answer is not the operator's** under `local/1`: it
   is recorded as a refused response and never applied; the router's
   destinations are recorded as routing history. SW1 brings the policy
   that lets a routed person answer.
9. **A rejection cancels only a gate's tasks**: an agent's own approval
   answered "no" is delivered as words, and the agent decides; a gate's
   Reject (or an assignment's Not done) cancels the tasks it held, as
   Cancel does.
10. **No expiry in this repository**: no timer ends a decision, as no
    timer ends a question today; the `expired` state is kept for a
    provider's escalation and written by no code here.
11. **An agent's decision holds only its own task**; other tasks of its
    run go on. Dependents of that task already wait on its result.
12. **No Dispatch link per decision**: the record names its ticket, and
    the ticket page lists decisions by that column; the `decision` link
    kind DS2 reserved stays reserved and refused locally, for CS1's plan
    to use or drop.
13. **Application and delivery are two things**: applied is the one
    transition that validates the answer and releases the hold (at most
    once); delivery is the line typed into the asking attempt, or the
    task file of the task's next attempt (at most once per attempt, never
    retyped after a restart).
14. **Subjects and staleness**: a candidate's approval is superseded by
    the task's next candidate; an artifact's by a change of its digest; a
    commit subject is shown and not rechecked; a plan subject never moves
    inside a run.
15. **The bounds** of The changes (500-character question, 4,000-character
    context, 2 to 6 alternatives, 20 evidence refs, 16 KiB request, 4
    open per attempt, 64 per run, 1,000 per run, 32 responses, 256
    history rows), each the existing bound of the nearest record
    (Dispatch's decision question, Brakeman's decision card, the
    blocker's message, the run commands' receipts) where there is one.
16. **More information is a new request**: the operator's "Ask for more"
    is typed to the agent, which answers with a request that supersedes
    the first, so the history shows both and the answer binds to the
    second.

## Acceptance

The boundary (the Program, section 4.3, DR1): one authorized person
answers a decision bound to a revision; the response survives a restart,
is applied once, and releases only the work that depended on it; an
unanswered decision consumes no retry.

How it will be observed, by hand, after PR-8 has merged, with real
Claude Code and Codex against a server of its own on the WSL box (no
networking configured by anyone) and the desktop app, in a scratch
repository both agents trust:

1. A Dispatch ticket's plan revision with two tasks and one gate,
   `deploy-approval` before `deploy`; its launch. `build` (Claude Code)
   runs and succeeds while `deploy` shows "waits for decision dr-1"; the
   gate's subject is the run's plan revision `r1`.
2. During `build`, its agent asks an approval about a file (an artifact
   subject) through `decision_request` from the skill alone: the tool
   answers at once with `dr-2` and `open`, the agent ends its turn, the
   session shows the question, and the run's other task goes on.
3. The server is stopped and started on the same data with dr-2 open;
   the operator answers dr-2 from the run page: Recorded, "waiting for the
   run to be continued". Continue: `build`'s next attempt starts at its
   checkpoint with the answer in its task file; the record shows one
   `applied` row, and the attempt that was interrupted is not counted
   (the task reads "attempt 2 of 3", not 3).
4. A second Approve of dr-2 from another tab gets the first's receipt
   (the same id) or, with a new id, "answered by" the operator.
5. The operator approves dr-1: `deploy` starts and the run succeeds. A
   second run of the same revision rejects dr-1: `deploy` is cancelled
   with the rejection's words, `build` unaffected.
6. A share link with control opened on `build`'s session shows the
   question and cannot answer it.
7. Codex as the asking agent in one of the runs, so both agents' use of
   the tool is seen.

Done means: the eight PRs merged, the observation above made with every
clause holding and recorded in the item's `docs/features.md` entry (what
ran, against which `main`, what was not exercised), and every defect the
observation finds fixed in the stream (AGENTS.md). What needs the owner:
the review of this plan and its decisions; cutting the issues; the
merges; setting the item's Status to Planned on approval, In progress at
PR-1 and Done once the observation is recorded; and, if the owner wants
it, the observation on the Windows desktop app, which is the owner's
machine.
