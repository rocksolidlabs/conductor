# Durable decisions and the people they are for (Program items DR1 and HM1)

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

**HM1's prerequisite** is DR1 itself (the Program, section 4.3), which is
not on `main`: it is the part of this file above HM1's. HM1's parts are
the last subsection of each section, "HM1, human members", and its PRs
are PR-9 to PR-12. PR-9 to PR-11 (a crew's people and their
responsibilities, a task's accountable owner, a person's work) build on
`main` as it is, using Dispatch's people as local labels (DS1, decision
D4) and nothing of DR1; PR-12 (whom a decision is for, and the
operator's answer for them) waits for DR1's eight PRs. HM1 does not
need ID1 or SW1, and does not presume BK3's role session (decision D18):
a person is never a session.

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

### HM1, human members

1. **No premium code, no licence check.** A person in a crew is a local
   label in the open-source repository; nothing asks whether a provider or
   a plan is there to list one, assign one work or answer for one.
2. **The open-source build is useful alone.** A crew names the people who
   own its decisions and its tasks' outcomes, and the operator answers a
   gate for them, with no account and no service.
3. **Paid features gated by hosted dependencies.** The paid layer (SW1:
   authenticated people, domain roles, quorum, delegation) arrives through
   `ext.IdentityProvider` and `ext.DecisionRouter` from a hosted identity;
   HM1 adds no flag that would stand in for it.
4. **Stop, inspect, revoke, export, required approvals free.** A gate that
   names a person is still answered by the operator with the control role
   (decision D4); nothing about a label makes an approval optional, and the
   crew's people go into an export with the crew's file.
5. **Dependencies one way.** Not touched: HM1 imports nothing outside this
   module.
6. **No feature build tag.** Not touched: no build constraint is added.
7. **The free default is what main does.** The empty route of
   `ext/local`'s `Router` is unchanged: a task's owner is shown beside its
   questions, and nothing routes or holds a question because a crew lists
   a person (the routing is DR1's and, for a team, SW1's).
8. **An unreachable provider answers as the free default.** Not touched:
   HM1 calls no provider; a label resolves from the crew as launched.
9. **CGO_ENABLED=0; the local store is modernc.org/sqlite.** Not touched:
   people live in the crew's JSON file and the run row that already keeps
   the crew as launched; no store is added.
10. **The repository contract.** This plan before the code; one issue per
    PR; race tests, vitest and Playwright per surface; the HTTP API's rows
    in `docs/protocol.md` beside the routes; no new dependency.
11. **The names.** "Person" and "people" in the crew, "accountable owner",
    and the four responsibilities in plain words; Yardmaster, Dispatch and
    Switchman keep their meanings; "workspace" is not used.
12. **Execution behind one seam.** A person is never started: no session,
    no `ext.Runtime` call, no worktree, no launch slot. The engine starts
    attempts for members only, through `crew.Launcher`, as it does today.

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

### HM1, human members

A crew today is agents only: every `crew.Member` has an `AgentID`, a
prompt and a start rule, and becomes a session. The people who decide
whether the schema is right, whether the feature is what was asked, or
whether the change is safe appear nowhere in it; they are whoever is
watching a terminal. Dispatch already treats people as names (decision
D4: a ticket's `owner` and `person:<name>` assignees, its people
directory in `internal/dispatch/people.go`), but a crew and its run do
not know them, so a gate cannot say whose decision it is and a task
cannot say who answers for its outcome apart from the agent doing it.

After HM1:

- The crew editor has a **People** section: each person a name (offered
  from the names Conductor knows: Dispatch's directory and the people of
  every saved crew, or typed) with the responsibilities they hold in this
  crew, from four: **architecture owner**, **product owner** (the domain
  or product owner), **security reviewer** and **acceptance owner**, and
  a short note. A person is never started: the editor says so beside the
  section, and the run never makes a session, a worktree or a branch for
  one.
- Each task of a crew with tasks may name its **owner**, a person of the
  crew, in a column of its own beside the member (the agent that does
  it). A task with no owner of its own is owned by the crew's acceptance
  owner when it has one; the run page says which ("owner ana, the
  acceptance owner").
- The run page's header lists the run's people and their
  responsibilities as the crew was launched; each task row and detail
  names its owner; an attempt's task file names its owner and who holds
  each responsibility, so the agent knows whose decision a question is.
- A person's name is a link to **their work**: the crews where they hold
  a responsibility, the tasks they own in the runs the server holds, the
  Dispatch tickets they own or are assigned, and (once DR1's records
  exist) the decisions waiting for them. The palette's `@` mode lists
  the people Conductor knows.
- Once DR1's decision records exist (above), a plan revision's decision
  names whom it is for: a responsibility (`role:architecture`) or a
  person (`person:nate`); so may an agent's `decision_request`. The
  record keeps the person the label named when it opened; the decision
  card, the run page and the person's page say "for nate, the
  architecture owner"; the operator answers it, and the response
  receipt keeps who answered (the operator) and for whom (nate, the
  architecture owner) side by side. A plan revision may also give a
  person work of their own: a decision of kind `assignment` ("run the
  migration on staging") for them, before the tasks that need it done,
  answered Done or Not done, as DR1 applies an assignment.

What does not change: who may answer (the operator, with the control
role, decision D4), the empty route of the free `DecisionRouter`, the
engine's launch and scheduling, and every crew saved without people,
which reads, saves and runs byte for byte as before.

## The changes

### `internal/crew/ledger`: the decision tables (schema version 8)

The ledger is where decisions live, not a store of their own (Owner
decisions, 2): a decision's opening, its response, its application and
the hold it puts on a task are each one transaction of the run, with the
task's row and the run's event, so a hold and its release can never
disagree with the task they hold.

`Version` goes from 7 to 8; migration 7 runs five `CREATE TABLE`
statements and four `CREATE INDEX` statements, and derives nothing: no
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
  evidence TEXT NOT NULL DEFAULT '[]',      -- JSON, each evidence ref as it was at the opening (below)
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
  actor TEXT NOT NULL,                      -- "admin" or "user:<subject>" (actorOf), the audit's words
  authority TEXT NOT NULL,                  -- JSON {grant, via, subject?, boot} (authorityOf), what the policy checks
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
  op_id TEXT NOT NULL DEFAULT '',           -- an operator operation's opId or commandId
  fingerprint TEXT NOT NULL DEFAULT '',     -- SHA-256 of that operation's body as sent
  PRIMARY KEY (decision, seq)
);
CREATE UNIQUE INDEX decision_ops ON decision_history(decision, by, op_id) WHERE op_id != '';
CREATE TABLE decision_holds (
  run_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  decision INTEGER NOT NULL REFERENCES decisions(n) ON DELETE CASCADE,
  released_at TEXT NOT NULL DEFAULT '',     -- set when the hold is released; the row stays
  PRIMARY KEY (run_id, task_id, decision),
  FOREIGN KEY (run_id, task_id) REFERENCES tasks(run_id, id) ON DELETE CASCADE
);
CREATE INDEX decision_holds_decision ON decision_holds(decision);
CREATE TABLE decision_refunds (
  run_id TEXT NOT NULL,                     -- the run the attempt ran in (an ancestor's, for an interrupted one)
  task_id TEXT NOT NULL,
  n INTEGER NOT NULL,                       -- the attempt given back
  decision INTEGER NOT NULL REFERENCES decisions(n) ON DELETE CASCADE, -- the decision it waited on then
  at TEXT NOT NULL,
  PRIMARY KEY (run_id, task_id, n),         -- an attempt is given back once, whatever decision it was for
  FOREIGN KEY (run_id, task_id, n) REFERENCES attempts(run_id, task_id, n) ON DELETE CASCADE
);
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
checked for shape, resolved once at the opening into the row's
`evidence` baseline, below, and again when shown), `expertise` (at most 4 labels of
at most 40 characters, the expertise the requester says the answer
needs: recorded and shown; nothing routes by it in DR1), `subject` (below)
and `supersedes` (`dr-n`, an open decision of the same task the request
takes the place of).

`subject`, the exact thing the decision is about, one of: `{kind:
"commit", commit}` (the asking attempt's worktree at its `HEAD` when it
asked, filled in by the server when the request names none), `{kind:
"candidate", run, task, n, commit}` (a task's submitted result, a DS2
candidate), `{kind: "artifact", path, commit, sha256}` (a file as
committed: its path relative to the worktree, the commit at the asking
attempt's `HEAD` when it asked, and the SHA-256 of the file's blob at
that commit, read from the repository's object store with `git
cat-file` as an argv; the request is refused `artifact_uncommitted`
when the worktree's file differs from that blob, so the agent commits
what it asks about, and no recheck ever reads a live file), and `{kind:
"plan", plan, digest}` (a gate's: the run's plan revision).

`evidence` (JSON, at most 20 entries of at most 512 bytes each as
encoded, 10 KiB): the baseline of the request's evidence refs, one
entry per ref in its order, `{ref, seen}`, where `seen` is the ref's
record at the opening in `brakeman.RefState`'s shape (its state, its
revision or digest, its time; `unavailable` when its store could not be
read then), made by the server, never sent by the requester, and kept
out of the request's `fingerprint`. A mutable ref (`ticket:CND-1`)
names no revision, so without it nothing could say what such a ref was
when the question was asked: the resolutions of `GET /api/decisions/
{did}` compare the record now with this baseline (The changes, the
evidence's refs). A repeated request keeps the first baseline. A gate
names no evidence, so its baseline is empty.

`policy` (JSON): `{revision: "local/1", approver: "operator", quorum: 1,
authority: [{kind: "operator"}]}`. The only policy of this repository is
`local/1`: one response from a principal with the admin grant (the
workbench token, or an identity provider's principal with
`ext.GrantAdmin`) decides it. The record keeps it so that a response and
an application check the policy the decision was opened under against
the one in force, and so that a later policy is a later revision of the
same field.

A response's `authority` is what the policy checks, kept apart from its
`actor`, the audit's words: `grant` (`admin`, or none), `via`
(`workbench`, `identity` or `router`) and the identity's `subject`, all
from the principal the request authenticated (`authorityOf` in
`internal/api`, beside `actorOf`), never from a body; `boot` is the
server process's id, minted at its start. A router's answer offered to
the engine has `via: router` and no grant, so it can never pass for an
admin's answer whose subject it shares.

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
`applied`, `refunded` (`{run, task, n}`: the attempt given back, I3,
written with its `decision_refunds` row, which is what a later give-back
looks for),
`delivery.typing`, `delivery.entered`, `delivery.untyped`,
`delivery.uncertain`, `delivery.task_file` (written once, by the first
attempt whose files carry the answer), `commented` (`{text, by}`),
`info_requested` (`{text, by}`), `superseded`, `cancelled`, and, for a
provider only, `assigned`, `delegated`, `escalated` and `expired`, which
no code of this repository writes. A row's `detail` is at most 2 KiB as
encoded.

At most 256 rows per decision, a cap each kind's own bound keeps: one
`opened`; two `routed` at the opening (the operator by policy, a
registered router); at most 32 of the operator's operations together
(`commented`, `info_requested` and a re-route's `routed`, on a final
decision too; past them the operation is answered `decision_limit` with
no row); at most 32 `responded`, one per response row (a router's
refused answer is one of them); one `applied`; one `superseded` or
`cancelled` (an applied approval's supersession, S1, included); at most
one `refunded` (a decision holds its task, so no second attempt
of it can wait on it, and a refund is never repeated, I3); at most three
`delivery.*` (the typing, its end, and the task file). That is at most
73 rows; `notified` rows take the rest, and past 256 a `notified` row is
not written (counted in the next written row's detail as `dropped`).
`TestTheHistoryCapHolds` writes every kind to its bound.

Bounds of the store, checked in the transaction that would pass them
(`decision_limit`): at most 4 decisions open per attempt, 64 open per
run, 1,000 per run in all; at most 32 response rows per decision, of
which at most 31 refused: the accepted one always has its place (a
decision accepts one response in its life, `open` → `answered` once),
so no run of refusals (stale revs, a router's answers) can leave a
gate that nobody can approve. Past 31 refused rows a further refusal
is answered with its receipt's words and `kept: false`, and keeps no
row; its retry is judged again, and since a decision's state and rev
only move forward it is refused again, so a repeat still gets the same
answer; at most 32 operator operations per decision (above). A
response's receipt is at most 8 KiB as encoded and the application
receipt at most 4 KiB; a response whose receipt would pass its bound is
refused `invalid` with no row, so every part of a record keeps its
bound and the record its sum (the routes, `GET /api/decisions/{did}`).

**Writers and readers** (`decisions.go`, new): `Tx.OpenDecision(row)`
(answers the new `n`, or the existing row for a repeated `request_id`),
`Tx.PutDecision(row)` (state, rev, why, applied, current_run),
`Tx.Answer(n, run, rev)` (the conditional write of a response: it
changes the row only while its `current_run` is `run`, its state `open`
and its rev `rev`, and says whether it did), `Tx.MoveDecisions(from,
to)` (a continuation's `UPDATE … RETURNING`: it moves every open or
answered decision whose `current_run` is `from` and answers the rows as
moved), `Tx.Respond(n, row)`, `Tx.History(n, kind, by, detail)`,
`Tx.Hold(run, task, n)` and `Tx.Release(run, task, n)` (which sets
`released_at` and keeps the row), `Tx.Refund(run, task, attempt,
decision)` (an `INSERT … ON CONFLICT DO NOTHING` into
`decision_refunds`, answering whether it wrote: false when that attempt
was given back already, for this decision or any other);
`Ledger.Decision(ctx, n)` (the
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
  `subject` of kind `artifact` is bound to its blob at the attempt's
  `HEAD` (above); a `candidate`
  must name a submitted result of the run. One transaction: the decision
  row (`origin: agent`, the asking task, attempt, session and member, the
  run's ticket and plan revision, `policy` `local/1`), its `opened` and
  `routed` (to the operator, rule "local/1: the operator answers") history,
  the hold of the asking task, a `supersedes` target set `superseded`
  with `superseded_by` and its hold moved, and the run's event
  `decision.opened`. A repeated `requestId` with the same fingerprint
  answers the existing record and changes nothing; with another,
  `decision_conflict`. After the commit, in this order: the session's
  question mirrors the decision (`session.Local.TryAskDecision`, below),
  on the attempt's `askMu` as a blocker's question is asked
  (`checkpoints.go`): the decision is read as open in the engine before
  the ask and again after it, the mirror's question id is kept on the
  engine's copy of the decision, and a decision answered, superseded or
  cancelled meanwhile withdraws the mirror at once by that id; the
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
  gates (Owner decisions, 6). `startManaged` calls the gates' opener only
  from PR-4 on, the PR that ships the response route with it (The PRs):
  Dispatch's launch routes are on `main` already, so a gate opened before
  a person can answer it would hold a real launch's tasks with no way to
  release them but a cancel.

**Holding.** `Engine.pump` gains one rule beside its `TaskReady` check:
a task with a hold is not dispatched, whatever its needs, and `run.ready`
gives it the reason "waits for decision dr-7", which the Tasks view's
words show (`taskWords`, `web/app/utils/tasks.ts`). A held task's state stays what it is
(`blocked` or `ready`): a hold is a fact beside the state, as a blocker
is, so the ledger's task states and every reader of them are unchanged.
A run whose unfinished tasks are held and could still start is running
and idle. The outcome rule (`run.settle`) counts a blocked task behind a
need that failed or was cancelled (`doomed`) as finished, and a held
task can be one, so the engine never leaves a decision open under work
that has ended:

- a decision whose held tasks are all finished or doomed is cancelled in
  the transaction that finishes or dooms the last of them ("build
  failed, so deploy will not start"), and its holds are released;
- a gate that holds no task (a plan decision whose `before` is empty)
  holds nothing and keeps no run open: it may be answered while the run
  lasts (`how: noted`);
- the transaction in which `settle` gives the run its outcome cancels
  every decision of the run that is not final ("the run ended, failed,
  before dr-3 was answered"), so a finished run holds no open decision,
  shows none in Needs you, and `Prune` takes it.

**A gate's requirement outlives its decision.** Cancelling a gate
because its work ended, or cancelling its tasks because the operator
rejected it, closes that decision, not the plan revision's requirement:
the run keeps, for each plan decision of its package, whether its run
chain has released it (`r.gates`, made at launch and, in a
continuation, from the chain's decision rows: a gate applied with `how:
released`, or cancelled by the operator with `release: true`, the one
explicit waiver). Every transaction that makes a task runnable again
(`retryTask`, `acceptTask`, which re-arms what needed the accepted
task, a reopen, and a continuation's first transaction) ends with
`regate(r)`: for each plan decision the chain has not released and no
decision of the run that is not final stands for, when any task its
`before` names is unfinished and not doomed, it opens in that same
transaction a fresh decision of the same plan decision (`origin: plan`,
the same `gate`, request and plan subject) holding each such task, its
`opened` history naming the decision it follows ("dr-9 asks again: build
was retried after dr-1 was cancelled"; "deploy was retried after dr-4
rejected it"). So a retry of `build` after `deploy`'s gate was cancelled,
a retry of a `deploy` that Reject cancelled, and either after a
continuation, all wait for an answer again; a gate the operator approved
stays released for the run chain, since its plan subject never moves. A
retry that would pass the run's 1,000 decisions is refused
`decision_limit` and leaves the task as it was. `pump` keeps a backstop:
a task a plan decision names that the chain has not released is not
dispatched without a hold, and `regate` runs for it (I14).

Pause holds the dispatch as before; a decision may still be answered
while paused, and its released tasks wait for Unpause.

**A waiting attempt gives up its place.** An attempt that waits on its
decision keeps its session, so that the answer can be typed into it,
and so it keeps its member and one of the run's slots too: `pump`
dispatches no second attempt of a member (`activeAttempt`) and at most
`maxActiveAttempts`, four, at once. So when `pump` finds a ready, unheld
task that it does not dispatch only because its member, or the run's
slots, are taken by attempts that wait on a decision and whose agents
have ended their turn since asking, it parks the oldest such attempt:
the attempt is closed as one that ends while it waits (below: given
back, its hold kept), its session stopped through the attempt's
ordinary end, and the answer then reaches the task's next attempt
through its task file. Whether the agent has ended its turn is the
engine's own mark on the attempt (`a.turnEnded`), never read off the
mirror: the mirror shows as soon as it is asked (`TryAskID` puts the
agent's question up at once), is set aside by a working report and
shown again at the done, and a mirror the session's rate limit refused
never shows at all. The mark is set by the first `done` activity the
attempt's session reports after the decision's opening committed
(`managedActivity`, which already receives it for the reminder),
cleared by a `working` report after it, and set again by the next done;
setting it wakes `pump`, as an attempt's end does, so a ready task
waiting for the member or the slot is dispatched at that done, not at
some later event. An attempt whose agent is still working is never
parked, whether or not its mirror shows, and an attempt nothing waits
for keeps its session. The tool's reply tells the agent to save a
checkpoint before it asks, since a next attempt starts at it.

**An attempt that ends while it waits.** In the transaction that closes
an attempt (`closeAttempt`, `endAttempt`), when the attempt asked a
decision that is open or answered and not applied, and its session
ended without a result (the agent exited, the attempt was parked or
stopped; an interruption is the continuation's, below): the attempt
closes with stage `decision` and is not counted (`t.extra++`, the
withdrawal's mechanism), the attempt's `decision_refunds` row and the
decision's `refunded` history row naming it are written, the task stays
held, and `attempt.waited` is the
event. An attempt that ends any other way is counted as today. The
task's next attempt is dispatched only after the application, at the
checkpoint's commit when there is one (`startCommit`), with the answer in
its task file. A session that ends with its decision applied and typed is
an ordinary end.

**A result while a decision waits.** A `task_result` from an attempt
that has a decision open, or answered and not applied, is refused (`409
decision_open`: "dr-7 is not answered yet: wait for its answer, or
withdraw it with decision_withdraw"), so no dependent starts on work
whose approval is still open (silence is never approval), and an attempt
never succeeds or fails its checks while its decision waits. The
engine's done-without-result reminder (`managedActivity`, typed once
per attempt) is neither typed nor spent while the attempt has such a
decision: the agent that ended its turn to wait is never told to submit,
whether or not the mirrored question shows (an ask the session's rate
limit refused, or one set aside while the agent works), and is reminded
at its next done without a result once the decision is applied.

**Responding.** `Engine.RespondDecision(ctx, id, resp, actor,
authority)`, under the actor the API names (`actorOf`) and the authority
it authenticated (`authorityOf`, the ledger above). With the decision's
current run held by the engine, through its `transact`; with it not held
(stopped, interrupted, not continued), through `Ledger.Transition` on
that run directly, changing no engine state, and for the workbench token
only: an identity principal's response to a run not held is refused
(`409 not_held`: "dr-7's run is not running: continue it, or answer with
the workbench token"), so no identity's answer waits for an application
that its grant may not outlast (Applying, below). The choice between the
two is made before the transaction, and a continuation (O6) may move the
decision between that choice and the commit, so the transaction does not
trust it: its answer is the conditional `Tx.Answer(n, run, rev)`, which
changes nothing once the decision's `current_run` is another run, and
`RespondDecision` then looks again and answers through the run that
holds the decision now, at most three times (`503 decision_moved`
after them, a reply the web asks once more). In one transaction:

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
   both; a rationale is at most 2 KiB as encoded (`invalid`, no row).
3. Accepted: the response row with its authority and its receipt, the
   decision `answered` with `answered_at` (`Tx.Answer`), rev + 1,
   `responded` history, the event `decision.answered`.

The reply is the receipt (Recorded). The application follows.

**Applying.** `Engine.applyDecisions(r)`, run after every transaction of a
held run that answered a decision, and by a continuation's first steps.
One transaction per decision: the decision `answered`; the rechecks again
(the subject, the policy, the asking attempt's membership, and the
responder's authority, below); then:

- origin agent, its attempt live (the same run chain, task, attempt and
  session, the session not ended): `how: typed`; the hold released; the
  delivery intent `delivery.typing`; after the commit the answer is typed
  as one line, "Decision dr-7 (approval) answered by Nate: Approve. Why:
  … (receipt dr-7/1)", by the member's deliverer (`handoff.go`) ahead of
  every handoff it holds: an ordinary handoff waits while a question
  shows (`UnlessWaiting` puts it back at the queue's head), and the
  decision's mirrored question is one, so an answer queued behind such a
  handoff would wait for itself. The answer goes in at the queue's head
  marked as the decision's, and the deliverer submits it answering the
  mirrored question (`session.Submission.Question` its question id and
  `Decision` its id, never `UnlessWaiting`) when the mirror shows, else
  as a plain line with `Decision` its id and `UnlessWaiting`; the
  ordinary handoffs follow it. A session keeps one question of the
  agent's own (`s.own`: a later ask replaces it), so of an attempt's up
  to four decisions only the newest one's mirror can show, and two
  answers can be queued at once. So decision answers never wait on one
  another: at each pass the deliverer takes first the decision answer
  whose mirror is the question shown (read on `askMu`, below), then the
  other decision answers in their order, then the handoffs, never only
  the queue's head (`handoff.go` puts a blocked item back at the head,
  which would leave the shown question's answer behind it); and a line
  with `Decision` set waits under `UnlessWaiting` only for a question
  that no decision mirrors (the session, below), never for another
  decision's mirror, which is Conductor's record and not the screen
  asking. Whether a mirror was
  put up is read on the attempt's `askMu`, after any ask under way
  (Opening, above), so a mirror asked late never outlives the answer;
  `delivery.entered` once the Enter is written, `delivery.untyped` when
  the line never went in (the next attempt's task file then carries it);
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

The authority is rechecked from the response's `authority`, never from
its actor: a `workbench` response holds (the operator's token is the
server's, decision D4); a `router` one never got here; an `identity`
one is applied only once the identity provider has admitted its token
again after the response committed, since a provider may revoke a
grant between the response's transaction and the application's with
the server still running (what `principalWatch` rechecks a connection
for). The response route is the only holder of the token, in its
memory for that request and never in the ledger or a log: once the
response has committed it asks the provider again through the guard
(`guardIdentity.recheck`, bounded by `ext.IdentityWait`, as
`principalWatch` asks) and hands the engine the answer,
`Engine.ConfirmAuthority(id, k, admitted)`; until then
`applyDecisions` leaves that decision `answered` and applies nothing of
it. Admitted again as the same subject with the admin grant: the
application proceeds in its own transaction, which checks that `k` is
still the decision's accepted response and `boot` this process's. Not
admitted, or no answer in time (a provider that fails, is late or is
busy has not said yes, and a gate is never released on what nobody
said): the decision is superseded by a fresh one, the same request and
holds ("user:nate's answer could not be rechecked; dr-9 asks again"),
and the operator answers again, with the workbench token if the
provider is away. One found after a restart (the crash fell between the
response and its application; its `boot` another process's) has no
recheck coming, since the token was never kept: superseded the same
way. A run not held never takes an identity's response (`not_held`,
above), so no such response waits for a continuation.

A recheck that fails at application supersedes the decision with its
words (`answered` → `superseded`) and keeps the response; what it held
is disposed of in the same transaction, as every supersession's is
(Disposing of the holds, below): an agent's decision's hold is
released and its asking attempt, when live, told in one line
("Decision dr-7's answer was not applied: the candidate is now
9f2c1ab"), else its task's next attempt told in its task file; the
holds of a gate, and of any decision whose answer's authority could not
be rechecked (its question still stands), move to the fresh decision
that takes its place.

An agent's approval answered "no" is delivered as words: the agent
decides what follows; only a gate's rejection cancels tasks (Owner
decisions, 9).

**Superseding and cancelling**, each in the transaction of its cause,
with its words in `why` and a history row:

- S1, a candidate subject: a task's new submitted result (another attempt,
  a repair after a review's `needs_changes`, RV1's repair) supersedes
  every open or answered decision whose subject is that task's earlier
  candidate, and every applied approval of it: an applied approval keeps
  its response and application receipts and its history, and becomes
  `superseded` with the words of what moved (`applied` → `superseded`,
  the one change of a final decision but a comment), so neither the card
  nor a task file shows it as current any more.
- S2, an artifact subject: the blob of its path at the commit the
  answer's work rests on (the asking attempt's `HEAD` while that attempt
  is live; otherwise the start commit of the attempt that will read the
  answer, its checkpoint's when it has one) is read from the
  repository's object store, never from a live file, at response and at
  application; another digest supersedes ("README.md changed: sha256
  3c1f… was 9ad0…"), and so does a commit or path that cannot be read
  ("README.md cannot be read at 9f2c1ab: …"): an unreadable subject is
  never counted as validated.
- S3, the asking task withdrawn (a withdrawal, `withdraw`) or reopened
  (`reopen`): its decisions are cancelled ("the review reopened build").
- S4, a commit subject: a clarification's and an assignment's are the
  tree the question was asked about, shown and never rechecked (an agent
  may go on working on other files); an approval's is rechecked at
  response and at application against the commit the answer's work
  rests on, read as S2 reads it, and another commit supersedes ("HEAD is
  now 9f2c1ab, was 3c1f2d0"). After its application an approval of a
  commit or of a file is not rechecked again: acting on it moves both.
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
- The work under it ended: its held tasks all finished or doomed, or the
  run given its outcome (Holding, above): cancelled, its holds released.

**Disposing of the holds.** Every supersession and cancellation disposes
of what the decision held in its own transaction, so no final decision
keeps a hold. A supersession by a request (`supersedes`) moves the hold
to the new decision. A gate superseded (its authority or policy failed
its recheck at application; its plan subject never moves), or any
decision whose answer's authority could not be rechecked, is replaced
in the same transaction by a fresh decision of the same request (for a
gate, the same plan decision) with the same holds, `superseded_by`
naming it, so its tasks stay held for an answer. Any other supersession
of an agent's decision releases its hold: the asking attempt, when
live, is told in one line, else its task's next attempt reads the words
in its task file ("dr-7 was superseded: the candidate is now 9f2c1ab;
ask again if you still need it"), whether or not the attempt that asked
has ended. A cancellation releases or cancels as its cause says, above.
A released hold keeps its row with `released_at` set (the ledger,
above), so a decision stays tied to the tasks it held.

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

**An operation repeats as a response does.** A comment, a more_info, a
route and a cancel each write one history row (`commented`,
`info_requested`, `routed`, `cancelled`) that keeps the operation's id
(`op_id`: its `opId`, or a cancel's `commandId`) and the SHA-256 of its
body as sent (`fingerprint`), `by` its actor, unique per decision
(`decision_ops`). Each operation's transaction looks for its
`(decision, actor, id)` first, before any recheck: found with the same
fingerprint, it answers the receipt that row makes (`{decision, op,
seq, at, rev, words}`, rebuilt from the row, whose `detail` keeps the
rev it made), `repeat: true`, and changes nothing, so a retry after the
decision moved on (answered, applied, closed, or moved to a
continuation) still gets its first receipt, and a repeated `more_info`
is typed once (its typing follows its first commit only); found with
another fingerprint, `409 op_conflict`. The lookup is by the
decision's `n`, which a continuation keeps (it moves `current_run`,
never the rows), so no run-scoped command table is read and a
continuation needs no rule of its own. A refused operation keeps no
row; its retry is judged again and, state and rev only moving forward,
refused again.

**Continuations and recovery** (`continue.go`). A continuation's first
transaction moves every open or answered decision of the run it
continues to the continuation (`current_run`) with `Tx.MoveDecisions`,
and the continuation's engine copy of them is made from the rows that
write answers, never from what `readHistory` read before the
transaction (the ledger's one connection orders the two writers): a
response that committed first on the old run is moved as answered and
applied next; one that comes after finds the row moved, changes
nothing, and is answered through the continuation (Responding, above).
The transaction puts the decisions' holds, released ones included, on
the continuation's tasks of the same ids, and for a task whose last
attempt was interrupted while it waited on one of them gives that
attempt back (`extra++`, since the crash left no close row to do it)
only when `Tx.Refund` writes its `decision_refunds` row, keyed by the
attempt itself (its run, task and number), never by the decision: a
continuation of a continuation, which finds the same interrupted
attempt last (`readHistory` walks the ancestors when a continuation
made none), never gives it back twice, even when the decision it
waited on was replaced in between (a fresh decision taking the place
of one whose identity answer could not be rechecked, or of a gate) and
the replacement's history names no refund. The decision's `refunded`
history row is written with it, for the card.
Its next steps apply the answered ones. `crew.Recovered` (the record that
`internal/api`'s `syncRecords` writes from the ledger at start) shows the
holds. The continuation's note now says "N uncertain launches waiting
for a retry" where it said "waiting for a decision" of something that was
not one.

**The task file** (`taskfile.go`). `task.md` gains "## Decisions" when
the task has any: each decision of the task's run chain that holds or
held it (its hold rows, released ones included, so an applied gate's
answer stays in every task it released) or that it asked, with its
question, its state and, once applied, the
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
  four differences: a person's keys, the screen moving on, a line
  naming no decision and a line naming another decision never take it
  away; a `Submit` with `Decision` set and `UnlessWaiting` waits only
  for a shown question that no decision mirrors, so one decision's
  answer is typed past another decision's mirror (the engine's
  delivery, Applying); an `answer` (`AnswerInput`,
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
  the actor `user:<By.Subject>` and the authority `{via: router}` with
  no grant, which `local/1` refuses (`not_approver`) whatever principal
  the subject also names: recorded as a refused response, its
  `responded` history row saying "refused under local/1", never applied
  (Owner decisions, 8).
- `ext/exttest`'s fake router records `Request`; the `Router` contract
  check gains a case with it set and one with it empty.

### `internal/api`: the routes

`decisions.go` (new), registered in `server.go`, each route's body read
with `DisallowUnknownFields` and bounded:

| Route | Who | Body → reply |
|---|---|---|
| `POST /api/sessions/{id}/decisions` | the attempt's own token (scope task) | `{requestId, type, question, context?, options?, recommendation?, uncertainty?, authorizes?, evidence?, expertise?, subject?, supersedes?}` (24 KiB) → `201 {decision}` at once; `200` the same for a repeat; `409 decision_conflict`, `409 not_running`, `409 artifact_uncommitted`, `429 decision_limit`, `400 invalid` |
| `GET /api/sessions/{id}/decisions` | the attempt's own token | its task's decisions (`{decisions}`, at most 100) |
| `GET /api/sessions/{id}/decisions/{did}` | the attempt's own token | one of its task's, whole |
| `POST /api/sessions/{id}/decisions/{did}/withdraw` | the attempt's own token | `{}` → `{decision}`; `409 decision_closed` |
| `GET /api/decisions` | admin | `?state=&run=&ticket=&after=&limit=` (100) → `{decisions, next?}`, newest first, without history |
| `GET /api/decisions/{did}` | admin | the record whole with its evidence's `refs` (at most 1 MiB as encoded, the sum of its parts at their bounds: the 16 KiB request, its 10 KiB evidence baseline, 32 receipts of at most 8 KiB (256 KiB), 256 history rows of at most 2 KiB of detail and 256 bytes of envelope (576 KiB), the 4 KiB application receipt and 20 ref resolutions of at most 2 KiB (40 KiB), 902 KiB in all; `TestTheFullestDecisionFitsItsBound` builds one with every part at its bound) |
| `POST /api/decisions/{did}/respond` | admin | `{responseId, choice?, text?, rationale?, expectedRev, byName?}` (8 KiB) → `201 {receipt, decision}`; `200` repeat; `409 decision_answered`, `decision_closed`, `stale_decision`, `stale_policy`, `response_conflict`, `not_held`; `403 not_approver`; `503 decision_moved` |
| `POST /api/decisions/{did}/cancel` | admin | `{commandId, why, release?, expectedRev}` → `{receipt, decision}`; `200` the same for a repeat; `409 op_conflict` |
| `POST /api/decisions/{did}/comment` | admin | `{opId, kind: comment\|more_info, text, expectedRev}` (8 KiB) → `{receipt, decision}`; `200` the same for a repeat; `409 op_conflict`; `429 decision_limit` |
| `POST /api/decisions/{did}/route` | admin | `{opId, to: {kind: "operator"}, note?, expectedRev}` → `{receipt, decision}`; `200` the same for a repeat; `409 op_conflict`; `429 decision_limit` |

`responseId`, `commandId` and `opId` take the shape the run commands'
ids take (1 to 32 of `A-Z a-z 0-9 . _ ~ -`); the web mints them and
retries a lost reply once with the same id, as `utils/tasks.ts` does.
A share link's controller, an agent's token and a Brakeman turn's token
are refused every admin route (`requireAdmin`'s `401 unauthorized`), so
the free build's one approver is the operator; `403 not_approver` is the
policy's refusal of a principal that got past it without the authority
`local/1` names (a router's answer, below).

- The result route (`task_result`, `POST /api/sessions/{id}/task/result`)
  gains the refusal `409 decision_open` (A result while a decision
  waits, above), its words in the operation's description.
- The guide (`guide.go`, the one table of operations): `decision_request`,
  `decision_list`, `decision_read` and `decision_withdraw` (scope task,
  one per session route above, MCP tools with their schemas, so
  `TestEverySessionRouteIsInTheTable` finds each; their MCP names and
  their handlers in `mcpHandled` land in one PR, PR-4, since
  `TestAnUnhandledToolIsNotListed` in `internal/cli` fails a table
  tool with no handler), and
  `decision_respond`, `decision_cancel`,
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
  (Owner decisions, 12), and its refusal's words in
  `internal/dispatch/links.go`, which say DR1 adds such records and would
  be false once it has, become "a decision link is reserved: a decision
  names its ticket itself, and the ticket's decisions are listed at
  /api/dispatch/tickets/{key}/decisions"; `TestADecisionLinkIsRefused`
  in `internal/dispatch` holds them.
- **The evidence's refs.** `GET /api/decisions/{did}` carries `refs`,
  one resolution per evidence ref, in the shape Brakeman's resolutions
  have (`brakeman.Resolution`: the record now, its state, its digest or
  revision, its receipt route; at most 2 KiB each as encoded), made by
  `decision_refs.go` (new) through the per-kind lookups that
  `brakeman_refs.go`'s `refResolver` uses, factored out of it so both
  call the same ones; a decision's reader is the admin, so no Brakeman
  grant narrows what it resolves. Each resolution's `seen` is the
  record's `evidence` baseline for that ref, never a read log: the
  request route resolves the refs through the same lookups before it
  calls `RequestDecision`, which writes the baseline in the opening's
  transaction (Brakeman's resolver answers `unobserved` for a ref its
  turn never read, and a decision has no such log, so without the
  baseline a ref changed before the card's first read would look
  unchanged). A ref whose record now differs from its baseline (its
  state, revision or digest) says `changed`, with the baseline's state
  and revision as what it was; one gone says `deleted`, with the same;
  one whose store could not be read at the opening says so rather than
  `changed`. Comments carry text, never refs.
  `TestADecisionResolvesItsEvidence`,
  `TestEvidenceChangedBeforeTheFirstReadIsShown` (a ticket ref's ticket
  edited after the request and before any `GET`: the first read says
  `changed` with the ticket's revision at the request).
- **Brakeman's reads.** The shared reads Brakeman's `observe` reaches are
  unchanged; `observe_decisions` and the `decision:<id>` ref are BK2's
  PR-12 (`docs/plans/brakeman.md`).

### `internal/cli` and `internal/agents`

- MCP tools `decision_request`, `decision_list`, `decision_read` and
  `decision_withdraw` in `mcpHandled` (in PR-4, with the guide's
  operations that name them), listed only in an attempt's
  session (scope task, as `task_blocked`), each one call to its route;
  `decision_request`'s reply is the record's id, its state and one line:
  "Pending. End your turn, or go on with work that does not depend on
  it; do not submit your result until it is answered. The answer is
  typed into this session as 'Decision dr-7 … answered', or, if this
  session has ended, written into your task's next attempt's task file,
  which starts at your last checkpoint: save one before you end your
  turn." The skill and the guide say the same (save a checkpoint, then
  ask).
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
  events stream `useAttention.ts` holds (no second stream), and on that
  stream's `snapshot` (its reconnect), as `useAttention.ts` reads the
  runs, Dispatch's pages and Brakeman's there: the stream replays no
  event it missed while away, so a gate opened or a decision answered
  meanwhile is read then. While the stream is down, the list is read
  again on the cadence `useAttention.ts` reads the sessions at then.
- `components/DecisionCard.vue`: the record: the question, the context
  (Markdown, as Dispatch renders a description), the alternatives with
  their tradeoffs and the recommended one marked, the uncertainty, what a
  yes authorizes, the evidence refs as chips (`BrakemanRefChip.vue`,
  reused, each handed its resolution from the record's `refs`: the chip
  resolves nothing itself), the subject (a commit or a candidate with
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

### HM1, human members

**What main has and what it does not.** On main (9723fdc6): the crew
model and Yardmaster (`internal/crew`), Dispatch's people as local
labels with a ticket's owner and assignees (DS1, `internal/dispatch/
people.go`, `PersonPicker.vue`), DS2's plan revisions with their
`decisions` (`dispatch.PlanDecision {id, question, kind, before}`,
recorded and shown, not held), the decision router's seam (C5,
`ext/decisions.go`, `internal/api/router.go`) and the receipts' actor
(`actorOf` in `internal/api/identity.go`: `admin` for the workbench
token). Not on main: DR1's durable decision records and gates (this
file's DR1 part, PR-1 to PR-8 above), and the authenticated layer (ID1,
SW1), which HM1 does not need. HM1's PR-9 to PR-11 build on main alone;
PR-12 waits for DR1's eight PRs.

**Shared pieces and their owners.** The decision record, its tables,
states, routes, receipts and cards are DR1's (above in this file); HM1
owns the label: the crew's people, their responsibilities, a task's
owner, the `for` a decision carries, its resolution to a person, and the
`person` and `role` destinations DR1's `RouteDecision` refuses until
HM1 (DR1, The changes, Routing). The decision page that shows "for
nate" is MB0's (`docs/plans/decision-inbox.md`); it reads
the fields HM1 adds. `route_decision` to a label is Brakeman's PR-18
(`docs/plans/brakeman.md`), which waits for HM1's PR-12 and calls the
`person` and `role` destinations PR-12 adds; the grant that lets
Brakeman use them is Brakeman's.

**`internal/crew` (crew.go, task.go, run.go, taskfile.go, continue.go).**

- A new type and a new field of the crew:

  ```go
  // Person is a person of a crew: a local label (decision D4), never
  // started. The name is the person, as in Dispatch's directory.
  type Person struct {
  	Name             string   `json:"name"`
  	Responsibilities []string `json:"responsibilities,omitempty"`
  	Note             string   `json:"note,omitempty"`
  }
  ```

  `Crew.People []Person` (JSON `people`, omitted when empty): a list
  of its own, never inside `Members`, so every loop of the engine over a crew's members (launch,
  start rules, `Reserve`, the attempts' dispatch, the checks) is
  untouched and cannot start a person.
- The responsibilities, a closed list in this order:
  `RespArchitecture = "architecture"`, `RespProduct = "product"`,
  `RespSecurity = "security"`, `RespAcceptance = "acceptance"`
  (`Responsibilities`), with their words in one table
  (`ResponsibilityWords`: "architecture owner", "product owner",
  "security reviewer", "acceptance owner").
- `Task.Owner string` (JSON `owner`, omitted when empty): the
  accountable person, a name of `Crew.People`, apart from
  `Task.Member`, the agent.
- `Crew.Validate` gains `checkPeople`: at most `maxPeople = 12`; a name
  of 1 to 60 characters of valid UTF-8 with no control character,
  exactly Dispatch's `person.name` rule (`checkField`, `MaxPersonName`),
  so a crew's name and a ticket's owner are the same label (the editor
  trims what is typed; the two bounds are held equal by a test in
  `internal/api`, which imports both packages, since neither imports
  the other); names unique in the crew and none a member's name, so "lead" is never both; each responsibility one of the
  four, at most once per person, and held by at most one person of the
  crew (the single approver of decision D4); a note of at most 500
  characters on one line; a task's owner empty or a person of the crew.
  A crew without tasks may list people (a crew with start rules has
  owners too). `ValidateSave`'s `taskTextBytes` counts the people's
  lines a task file carries, so the never-cut part stays within
  `maxTaskText`.
- `TaskOwner(c Crew, t Task) (Person, string, bool)`: the task's owner,
  or else the crew's acceptance owner, with why (`"task"` or
  `"acceptance"`), or none. The one rule the task file, the run's state
  and the person's work use.
- `Holder(c Crew, label string) (Person, string, error)`: the person a
  label names in the crew, `role:<responsibility>` its one holder and
  `person:<name>` that person, with the responsibility or "", or why
  not ("no person of crew api holds security"). Defined here with its
  tests; PR-12's decisions are its callers.
- `Run.People []Person` (JSON `people`, omitted when empty) and
  `TaskState.Owner string`, `TaskState.OwnerFrom string` (`task` |
  `acceptance`), set from the crew as launched. A run's record
  (`runs/<id>.json`) carries them only when the run is managed, whose
  record the ledger can write again; a run without tasks keeps its
  people in memory while the engine holds it and writes none into its
  record (Events, HM1, Migration and downgrade). A managed run's record rebuilt from the ledger
  (`Recovered`, `syncRecords`) and a continuation (`ContinueRun`) read
  them from the crew as launched of the run chain's launch row (the
  first run of its `continued_from` chain, `launchCrew`), never from a
  continuation's own row, so they are the launch's, never the saved
  crew's now, and never what an older build wrote into a continuation
  it made. A continuation copies the crew of the run it continues into
  its row, and a pre-HM1 build decodes that crew permissively
  (`json.Unmarshal` into its `Crew`, `continue.go`) and writes it again
  (`addRun`) without `people` and owners; the launch row is the one a
  continuation never rewrites, and `Prune` refuses a run a continuation
  cites, so it is there while any of its continuations is. HM1's
  `ContinueRun` writes the launch's people and owners into its own row
  too, so a chain an older build continued is whole again from the next
  continuation on.
- The task file: `TaskFile.Owner *TaskPerson` and `TaskFile.People
  []TaskPerson` (`{name, responsibilities}`), and in `task.md` a section
  "Who answers for this work" after the instructions: the owner and how
  they came to own it, then each responsibility and its holder, then one
  line: "Ask for a person's decision through Conductor; never act or
  answer as them." Never cut (at most 12 lines); a crew without people
  writes the task file byte for byte as today (the goldens unchanged).

**`internal/api` (crews.go, runs, people.go new).**

- The crew routes take and answer `people` and a task's `owner`
  (`POST /api/crews`, `PUT /api/crews/{id}`, `GET /api/crews/{id}`); the
  list (`GET /api/crews`) gains each crew's people with their
  responsibilities, no notes. The bodies' bounds stay `crew.MaxEncoded`.
- One conversion of a crew body. A `crewInput` becomes a `crew.Crew`
  today in three hand-written copies: `readCrew` (`crews.go`, the crew
  routes), `crewOfBody` (`brakeman_make.go`, the crew a Brakeman draft
  makes) and `brakemanFacts.CheckCrew` (`brakeman.go`, the draft's
  check). PR-9 replaces the three with one method, `crewInput.crew()`,
  which carries `people` and each task's `owner` with every other field,
  so a Brakeman draft's check runs `checkPeople` and making the draft
  keeps its people; a field added later is added once.
- The run routes (`GET /api/runs`, `GET /api/runs/{run}`, `GET /api/runs/
  {run}/tasks/{task}`) answer `people` and each task's `owner` and
  `ownerFrom`.
- `GET /api/people?q=&after=&limit=` (admin): the names Conductor
  knows, each once, by name, in pages: `{people: [{name, dispatch: bool,
  crews: [{id, name, responsibilities}], crewsMore?: true}], next?,
  dispatchUnavailable?: true}`; at most `limit` names (1 to 200, 200
  when absent), `next` the last name when more follow, which the next
  page passes as `after`; `q` (at most 60 characters) keeps the names
  that contain it, ignoring case, for the pickers and the palette; a
  person's `crews` at most 20, with `crewsMore` past them (their page
  lists up to 100, below). Neither store has a bound on its
  names, so the reply has one of its own. Read from Dispatch's
  directory and the crew store, written to neither. A name
  typed into a crew is not added to Dispatch's directory: the two stores
  stay apart, and the list is their union.
- `GET /api/people/{name}` (admin): a person's work: `{name, crews:
  [...], tasks: [{run, runLabel, task, state, member, ownerFrom}],
  tickets: {owner, assignee, href}, decisions: []}`, the tasks from the
  runs the engine holds and the managed runs' records, at most 200, the
  newest first, with `more: true` past them; `tickets` the counts of
  Dispatch's own query (`owner:` and `assignee:person:`) and the link to
  that view; `decisions` empty until PR-12; `crews` at most 100, with
  `crewsMore`. `404 not_found` only for a name that no source of the page
  knows: neither store, nor a run the engine holds or a managed run's
  record (its `people` and its tasks' owners, as launched), nor, from
  PR-12, a decision's `for`; so a person removed from the saved crew
  after a launch still has a page, with that run's work on it. The name
  is matched exactly, as Dispatch matches it.
- `docs/protocol.md`: the crew body's `people` and `owner`, the run's
  `people`, `owner` and `ownerFrom`, the two people routes. No WebSocket
  frame or control message changes, so `internal/proto` and
  `web/app/utils/protocol.ts` are untouched; the HTTP types are in
  `web/app/composables/useSessions.ts`. The two people routes' rows give
  their bounds: the page of 200 names, `q`, the 20 and 100 crews, the 200
  tasks.

**`web/app`.**

- `components/CrewPeopleTable.vue` (new): a row per person: the name
  (`PersonPicker.vue` with a new optional `names` prop fed by
  `composables/usePeople.ts`, `GET /api/people`; without the prop it
  reads Dispatch's directory as today), the responsibilities (a
  multi-select of the four, one held by another row disabled with "held
  by ana"), the note, remove. Under the table: "A person is never
  started: no session, worktree or agent. They own decisions and
  outcomes."
- `components/CrewTasksTable.vue`: an **Owner** column (a select of the
  crew's people and "none"; with none, the hint names the acceptance
  owner when there is one).
- `components/CrewEditor.vue`: the People section between the members
  and the tasks.
- `utils/crews.ts`: `toCrewInput` carries `people` and `owner`;
  `peopleProblems` mirrors `checkPeople` under each row and in the Save
  gate; `ownersFollowPeople` renames a task's owner with its person, by
  key, as `tasksFollowMembers` does for members; `ownerOf` is
  `TaskOwner`'s rule.
- The run page: `CrewRunHeader.vue` the people as chips with their
  responsibilities; `RunTasks.vue` the owner in each row and in the
  detail; `DispatchPeople.vue` the people of the approved revision's
  crew with their responsibilities ("as r3 was approved"), since the
  revision's body keeps the crew as the store did.
- `pages/people/[name].vue` (new): the person's work from `GET /api/
  people/{name}`; every person chip links to it. One column on a phone.
- `composables/usePaletteRows.ts`: the `@` mode's groups
  (`peopleGroups`) are today the page's people (a session's viewers,
  under the page's label) and "You" ("Your name…"); a group **People**
  goes between them: a row per person Conductor knows, from `usePeople`
  (the first page of `GET /api/people`, with what follows the `@` as
  `q`), "nate · architecture owner in api", Enter opening their page.
  The rows are made by a pure function, `personRows` in `utils/people.ts`
  (new), which `peopleGroups` calls, so a vitest reaches them.

**Whom a decision is for (PR-12, once DR1's PR-1 to PR-8 are on main).**
No ledger schema change: what HM1 adds lives in the JSON columns DR1's
`decisions` table already has (`request`, the receipts, the history's
`detail`).

- `internal/dispatch` (model.go, plans.go): `PlanDecision.For string`
  (JSON `for`, omitted when empty): `role:<responsibility>` or `person:<name>`,
  empty for none (the operator alone, as today), and a third kind,
  `DecisionAssignment = "assignment"`, beside approval and
  clarification: work a person does by hand before the tasks it names.
  The store checks the label's shape only: `role:` or `person:`, then 1
  to 60 characters with no control character; `internal/dispatch` does
  not import `internal/crew`, so it keeps no copy of the four
  responsibilities and judges none. The API's proposal (`POST
  /api/dispatch/tickets/{key}/plans`, in `internal/api`, which imports
  both) resolves the label in the revision's crew with `crew.Holder`,
  the one list of the four: an unknown responsibility and a
  responsibility nobody holds are both `400 invalid` ("no person of
  crew api holds architecture"; "role:legal is not one of
  architecture, product, security, acceptance"). Both live in the immutable body, so an older
  revision's digest is unchanged (`for` omitted when empty) and a label
  resolves against the crew the revision was approved with, never the
  saved crew now. `conductor dispatch plan propose` gains
  `--decision-for ID=role:architecture`, and `assignment` as a
  `--decision` kind.
- `internal/crew` (DR1's `decisions.go`, `package.go`): a gate opened in
  `CreateRun`'s transaction from `Package.Decisions` carries its `for`,
  and an assignment gate DR1's type `assignment`; `RequestDecision`
  takes `for` from an agent. The server resolves the label with
  `Holder` over the run's crew as launched and writes it into the
  request JSON as `for: {label, person, responsibility}` (the label as
  given, the person it named, the responsibility or ""), in the same
  transaction, and DR1's opening `routed` row names the person beside
  the operator, one row and not a second, so DR1's history count is
  unchanged: `{to: [{kind: "operator"}, {kind: "person", name}], rule:
  "local/1: the operator answers, for the architecture owner
  (role:architecture)", by: "yardmaster"}`. An agent's label no person of the run's crew holds
  is refused (`400 invalid`, naming the crew's responsibilities and
  their holders); a decision with no `for` is routed to the operator as
  DR1 routes it. `RouteDecision` takes `{kind: "person", name}` and
  `{kind: "role", name: <responsibility>}` once they resolve in the
  run's crew as launched (what Brakeman's PR-18 calls). Such a route
  changes whom the decision is for, so it is taken only while the
  decision is `open` and at the `expectedRev` its body carries
  (`decision_answered`, `decision_closed` or `stale_decision`
  otherwise, the words a response gets, and no row); in one transaction
  it rewrites the request's `for`, writes the `routed` row with the
  label before and after, and bumps `rev`. A response made against a
  card that said "for nate" is then `stale_decision` ("dr-7 is now for
  ana, the security reviewer: routed by the operator"), so no answer is
  recorded for a person its responder did not see. The request's
  `fingerprint` is of the request as sent and does not change; DR1's
  route to the operator is unchanged.
- The response receipt (DR1's `ResponseReceipt`) gains `for` (`{person,
  responsibility}`, copied from the request at the rev the response was
  accepted at; once a decision is answered no route changes `for`, and
  the application, the typed line and the task file read it from the
  receipt), beside `actor` and
  `byName`, which stay who answered; the typed line and the task file's
  "Decisions" say "answered by the operator for nate, the architecture
  owner". The policy stays `local/1`: the label never changes who may
  answer (decision D4).
- `internal/crew/ledger` (DR1's `decisions.go`): `Filter` gains `For` (a
  person's name), read with `json_extract(request, '$.for.person')`
  (JSON1 is compiled in, `docs/dependencies.md`), bounded as DR1's other
  filters are.
- `internal/api`: `GET /api/decisions?for=<name>`; `GET /api/people/
  {name}` fills `decisions` with the person's open and answered ones (at
  most 100); the decision event's data gains nothing.
- `internal/cli`, `internal/agents`: `decision_request`'s schema and
  `conductor task decide` gain `for` (`--for role:architecture`); the
  skill's decisions paragraph gains one sentence: the task file's "Who
  answers for this work" names the holders, and `for` names the one the
  decision is for.
- `web/app`, the proposal form: `composables/useDispatch.ts`'s
  `PlanDecision` gains `for?: string` and the kind `'assignment'`;
  `components/DispatchPlanPropose.vue` offers Assignment beside Approval
  and Clarification (`kindItems`) and, on each decision row, a **For**
  select: none, each responsibility held in the picked crew with its
  holder ("architecture owner: nate"), and each person of it, read from
  the crew (`GET /api/crews/{id}`). Revising keeps a decision's `for`:
  the prefill (`fill`) already copies the body's decision whole, and
  `submit` stops rebuilding it field by field: it sends what
  `planDecisionsOut` in `utils/dispatchPlans.ts` (new) makes, `{id,
  question, kind, before, for?}`, so no field is dropped. A `for` the
  picked crew no longer resolves (the crew edited since, or another crew
  picked) blocks Save with the words the server would refuse with
  (`decisionsProblem` gains the check, handed the crew's people). The
  revision's page shows each decision's `for` and the assignment kind.
- `web/app`: `DecisionCard.vue`'s "who may answer" says "the operator,
  for nate (architecture owner)"; `DecisionRespond.vue`'s confirm says
  "You answer as the operator, for nate"; an assignment's card says
  what nate is to do; the person's page lists their decisions with the
  card; MB0's page (`docs/plans/decision-inbox.md`) shows the same
  through DR1's components.
- Docs: `docs/protocol.md` (the decision bodies' `for`, the receipt's
  `for`, the filter), the features entry, the repository map's
  `internal/crew` row, `docs/architecture.md`'s crew section.

## Events, orderings and invariants

**States of a decision.** `open` → `answered` (a response accepted) →
`applied` (Yardmaster validated and applied it). `open` or `answered` →
`superseded` (its subject moved, or a new request took its place) or
`cancelled` (its task cancelled, withdrawn or reopened; the operator; the
asking agent, while open). `open` → `expired` is a provider's only; no
code of this repository sets it. `applied`, `superseded`, `cancelled` and
`expired` are final: nothing changes a final decision but a `commented`
history row and one transition, `applied` → `superseded`, of an applied
approval whose candidate a new result replaced (S1), which keeps its
response and application receipts. Every change bumps `rev`, writes one history row and one
run event (`decision.opened`, `decision.answered`, `decision.applied`,
`decision.superseded`, `decision.cancelled`, `attempt.waited`) in the
same transaction as the task and hold rows it changes.

**Orderings.**

- O1, an agent's request: the decision row, its history, the hold and the
  event commit; then the reply (`201`); then, after the commit and never
  before it, the session's mirrored question, the router's Route (through
  C5's guard), the `decision` event and the notification. The mirror is
  asked on the attempt's `askMu` with the decision read as open before
  and after the ask; the application's delivery and every withdrawal of
  the mirror take the same lock after it, so an ask that the reply
  overtook either finds the decision resolved and asks nothing, or puts
  up a mirror that the delivery answers or the withdrawal takes away,
  never one left behind on a final decision. A crash after
  the commit and before the reply leaves an open decision the agent's
  retry with the same `requestId` finds (`200`, the same record).
- O2, a gate: opened in the run's first transaction with its tasks' rows,
  so no attempt of a held task can be reserved before its hold exists.
- O3, a response: authenticated (`requireAdmin`), decoded, then one
  transaction: the repeat check, the rechecks, the row and its receipt,
  `answered` by the conditional `Tx.Answer`, which changes nothing once
  a continuation has moved the decision (O6), and the response is then
  answered through the run that holds it now. The reply follows the
  commit. The application is a later
  transaction, so "recorded" and "applied" are two facts with two times.
- O4, the application: one transaction rechecks and writes the receipt,
  `applied`, the hold's release (or the gate's cancellations) and the
  delivery intent; the typing or the next dispatch follows its commit,
  and `delivery.entered` or `delivery.untyped` is written after.
- O5, an attempt that ends while it waits, without a result (its agent
  exited, or `pump` parked it for a ready task of its member or for a
  slot): the close transaction gives the attempt back (`extra++`),
  writes its `decision_refunds` row and `refunded`, and keeps the hold;
  the next attempt is reserved only after O4 released it. The parking
  waits for the attempt's own `turnEnded` mark (its session's done
  after the request), never for the mirror's showing.
- O6, a continuation: its first transaction moves the decisions
  (`Tx.MoveDecisions`, whose returned rows make the engine's copy) and
  their holds, and gives back an interrupted waiting attempt that no
  `decision_refunds` row names (keyed by the attempt, whatever decision
  it waited on), and ends with `regate`; the application of the answered ones runs after
  it, in their own transactions. A response and a continuation are both
  writes of the one ledger connection, so one commits first: a response
  first is moved as answered and applied; a continuation first leaves
  the response's conditional write with nothing to change, and the
  response is answered through the continuation.
- O7, a supersession or cancellation is in the transaction of its cause
  (the new result's, the withdrawal's, the cancel's), so no application
  can run between the cause and its effect on the decision.

**Invariants.**

- I1, applied at most once: the application transaction requires
  `answered` and sets `applied`; two applications of one decision cannot
  both commit, and a repeated response changes nothing.
- I2, a held task is never dispatched: `pump` reads the holds under the
  engine's lock, and a hold is released only in O4, by a cancellation or
  by a supersession's disposal (Disposing of the holds).
- I3, an unanswered decision consumes no attempt: an attempt whose
  session ended without a result while its task's decision was open or
  answered and unapplied is given back once, in its close (O5) or in the
  continuation (O6), and the `decision_refunds` row that records it,
  keyed by the attempt and not by the decision, keeps any later
  continuation from giving it back again, even after the decision it
  waited on was replaced by another. An attempt never ends
  by its result while its decision waits (`decision_open`), so no
  attempt that succeeded or failed its checks is given back.
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
- I10, no final decision holds a task: every supersession and
  cancellation disposes of its holds in its own transaction, moving them
  to the decision that takes its place or releasing them.
- I11, no decision outlives the work under it: a decision whose held
  tasks are all finished or doomed is cancelled then, and the
  transaction that gives a run its outcome cancels every decision of it
  that is not final.
- I12, a waiting attempt holds back no other work: an attempt waiting on
  its decision, its agent's turn ended (its session's done after the
  request, the engine's `turnEnded` mark, which wakes `pump`; never the
  mirror's showing), is parked as soon as its member or its slot is what
  keeps a ready, unheld task from starting; one whose agent still works
  is never parked.
- I13, a response is applied under the authority it was accepted with,
  rechecked at application from the response's `authority`, never from
  its actor; an identity's response is applied only after the provider
  admitted its token again after the response's commit, in the process
  that accepted it, and is otherwise superseded, never applied.
- I14, a gate's requirement outlives its decision: a task a plan
  decision names starts only after the run chain released that plan
  decision (an applied approval, answer or Done, or the operator's
  cancel with `release: true`); work made runnable again after the gate
  was cancelled or rejected is held by a fresh gate in the same
  transaction (`regate`).
- I15, decision answers never wait on one another: the answer whose
  mirror shows is delivered first, and another decision's answer is
  typed past a mirror, never behind it.

**What the ledger holds** after each step is the record above; the run
record (`runs/<id>.json`) is a cache of the ledger, as it is today, and
gains the tasks' `holds`.

**Migration.** Version 7 to 8: the five tables and four indexes, in one
transaction with the version's change; nothing read, nothing derived; a
version 7 database's runs, tasks and attempts are unchanged and hold no
decision. Tested by opening a version 7 database made by `main`'s code
(`TestMigrationSevenToEightAddsOnlyTables`).

**Conflict rules.** None of replication: a decision is a local record of
one ledger. Inside one server, the run's transactions order every change;
first response wins between people, by the order the transactions
commit.

### HM1, human members

PR-9 to PR-11 touch no state machine and no schema: the crew store's
file gains two fields, and the ledger's run row already keeps the crew as
launched. PR-12 adds no state and no column either: Dispatch's plan body
gains one optional field and one kind, and what a decision records of a
person goes into the JSON of DR1's rows, in DR1's transactions.

- **A person is never executed.** Nothing reads `Crew.People` to start,
  reserve, add, resume or stop anything. The engine's loops over
  `Members` and `Tasks` are unchanged, so no session, worktree, branch,
  launch slot, `CONDUCTOR_NOTIFY_TOKEN` or `ext.Runtime` call is ever
  made for a person; `form_crew` and `add_member` take members only, so
  an agent never adds a person (a Brakeman draft may carry people as any
  crew body does; making it work stays the operator's).
- **The run's people are the launch's.** `Run.People`, `TaskState.Owner`
  and `OwnerFrom` are set at launch from the crew as launched and never
  change during the run; a continuation and a recovered record read them
  from the crew JSON of the chain's launch row, never a continuation's
  row, which an older build may have written without them. Editing the saved crew's people never
  changes a live run (the Program's stable-plan rule): the next launch
  takes the edit.
- **One owner rule, one label rule.** `TaskOwner` is the only function
  that says who owns a task (the task file, the run's state and the
  person's work call it), and `Holder` the only one that says whom a
  label names (the proposal's check, a gate, an agent's request and a
  route call it), so no two views disagree.
- **A label resolves when it is set, and an answer keeps the one it
  saw.** A decision's `for` resolves in the transaction that opens it (a
  gate's in `CreateRun`'s, an agent's in `RequestDecision`'s), or in a
  route's that changes it, always against the run's crew as launched,
  which for a launch from a ticket is the revision's crew byte for byte;
  the request keeps the person it named, written before the commit and
  before anyone is told. A route that changes `for` is taken only while
  the decision is open, at the rev its sender saw, and bumps `rev`, so a
  response, which DR1 accepts only at the rev its responder saw
  (`expectedRev`), is always for the person that rev named, and its
  receipt keeps that `for`. Once answered, nothing changes `for`: the
  application, the typed line and the task file read the receipt's. A
  continuation carries the record as DR1 carries it and resolves nothing
  again; a later edit of the saved crew changes nothing about it. In
  the open-source build the operator (the admin grant, DR1's `local/1`)
  answers any label (decision D4), and the rechecks at response and
  application are DR1's (state, revision, subject, policy, membership);
  HM1 adds none, since `expectedRev` is the recheck a route needs.
- **Migration and downgrade.** None to run: a crew file without
  `people` reads as none, a run record without them reads as none, a
  revision without `for` reads as none, and none of them is written
  again to add the field (the conservative-migration rule). An older
  Conductor decodes the crew store's files and the run records strictly
  (`store.DecodeStrict`), so it treats a file that holds `people` or
  `owner` as unreadable and leaves it out of its lists. For a crew file
  that is all: the crew store never deletes or writes over a file it
  cannot read (`Create` skips its id; nothing prunes the store), and a
  newer Conductor reads it again. A run record is not so safe:
  `Records.prune` counts a record it cannot read as a person's and
  deletes such records first once more than 500 are kept. So only a
  managed run's record carries people and owners: it is a cache of the
  ledger, whose run row keeps the crew as launched, and one an older
  build deleted is written again by `syncRecords` at the newer one's
  next start (the ledger's schema is unchanged by HM1, so the older
  build opens it as its own). Opening the ledger as its own, an older
  build may also continue such a run, and its continuation's row then
  holds the crew without `people` and owners (`json.Unmarshal` drops
  what its `Crew` lacks, and `addRun` writes the rest): nothing is lost
  by it, because HM1 reads a run's people and owners only from the
  chain's launch row, which no continuation rewrites and `Prune` keeps
  while a continuation cites it, and HM1's next continuation writes them
  into its own row again (The changes, HM1, `internal/crew`). A run without tasks writes no people into
  its record, which every older build reads as before; once it has
  ended, its page shows no people. The features entry says what an
  older build does with these files.
- **Conflicts.** None in HM1: crews are not replicated, and a revision's
  body is immutable. CS1, which replicates DR1's records, carries `for`
  and `assignee` as written.

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

### HM1, human members

- A crew task done by a person (a task row whose doer is a person, with
  attempts and a result): a person's work is an assignment decision for
  them (PR-12), which DR1 already holds and applies, and a person's
  review of a candidate is RV1's (`docs/plans/review-repair.md`).
- Authenticated people, a person bound to a principal, domain roles past
  the four, several holders of one responsibility, groups, quorum,
  delegation and escalation: SW1 and ID1 (decision D4).
- Routing a session's ordinary question to a label, and Brakeman's
  `route_decision` to one (its PR-18, which calls PR-12's destinations);
  Switchman's team routing (SW1). The free router's empty route is
  unchanged.
- Notifying a person: there is no one to notify but the operator until
  identity exists; the notifications stay DR1's and MB0's.
- Adding a crew's names to Dispatch's directory, or Dispatch's names to
  crews: the two stores stay apart and `GET /api/people` reads both.
- People added to a live run, and editing a run's people: a managed run
  keeps refusing a member change (`409 managed_run`), and a person change
  is the same kind of change.

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
  attempt, 64 open per run, 1,000 per run, 32 responses, 32 operator
  operations, an 8 KiB receipt); `TestRefusalsNeverTakeTheAnswersPlace`
  (31 stale responses kept, a 32nd answered `kept: false` with no row,
  then the operator's valid answer accepted, applied, and its gate's
  task released); `TestAnOperationIDIsUniquePerDecision`; `TestTheHistoryCapHolds` (every kind
  written to its own bound, the mandatory rows all kept, `notified`
  dropped past 256 with `dropped` counted); `TestAnAnswerIsConditionalOnItsRun`
  (`Tx.Answer` changes nothing once `current_run` moved);
  `TestMoveDecisionsAnswersTheMovedRows`; `TestAReleasedHoldKeepsItsRow`;
  `TestARefundIsOncePerAttempt` (`Tx.Refund` for one attempt under two
  decisions: the second writes nothing);
  `TestRecoverChangesNoDecision`; `TestSnapshotCarriesDecisions`.
- `internal/crew` (`decisions_test.go`, `decisions_restart_test.go`, with
  the fake ledger and fake sessions of `managed_test.go`):
  `TestAnAgentsDecisionHoldsOnlyItsTask` (a sibling task of the same
  member dispatches and finishes meanwhile: the waiting attempt, its
  turn ended, is parked, given back, and its task's next attempt starts
  after the answer); `TestAWorkingAttemptIsNeverParked` (the same, the
  agent still working with its mirror shown at once after the request:
  not parked, and the sibling dispatches at the agent's done, woken by
  it); `TestARefusedMirrorStillParks` (the mirror's ask refused by the
  session's rate limit, so it never shows: the agent's done parks the
  attempt and the sibling dispatches);
  `TestWaitingAttemptsFreeTheRunsSlots` (four tasks of four members
  each wait on a decision, a fifth task is ready: one waiting attempt is
  parked and the fifth dispatches); `TestAGateHoldsItsBeforeTasks` (gate on `deploy`;
  `build` runs; `deploy` starts only after the application);
  `TestTheReplyComesBeforeTheMirror` (a seam holding the session's ask:
  the request's reply returned); `TestALateMirrorNeverOutlivesItsAnswer`
  (the same seam held while the decision is answered and applied, then
  cancelled in a second case; released: no question shows, the answer
  typed once); `TestADecisionAnswerPassesAQueuedHandoff` (a handoff
  queued while the mirror shows; the answer is typed first, the handoff
  after); `TestTwoAnswersQueuedBeforeDeliveryAreBothTyped` (dr-1 and
  dr-2 of one attempt open, dr-2's mirror shown; both answered, in each
  order, and queued before the deliverer runs: dr-2's answers its
  mirror, dr-1's is typed as a line past it, each once, and a handoff
  queued between them follows both); `TestAResponseIsAppliedOnce` (two
  application passes, one receipt, one typed line);
  `TestAResultWhileADecisionWaitsIsRefused` (`decision_open`, and
  accepted once the decision is applied);
  `TestNoReminderWhileADecisionWaits` (a done without a result while the
  decision is open types nothing and leaves the reminder unspent; one
  after the application is reminded);
  `TestAnAttemptEndedWhileWaitingIsNotCounted` (attempt 1 asks dr-1 and
  exits: given back, and no attempt is reserved while dr-1 is open; dr-1
  answered, attempt 2 starts at the checkpoint with the answer in its
  task file, asks dr-2 and exits; the same for attempt 3 and dr-3; after
  dr-3's answer attempt 4 starts, so the task never failed for
  attempts, and each refund has its `refunded` row);
  `TestAnAttemptThatSubmittedIsCounted` (an attempt that failed its
  checks after its decision was applied is counted as today);
  `TestAnAnswerRecordedWhileInterruptedIsAppliedOnContinue`;
  `TestAResponseDuringAContinuationIsAppliedOnce` (a seam holding the
  continuation between `readHistory` and its first transaction, a
  response committed there on the old run: moved as answered and
  applied once; and a seam holding the response between its choice of
  run and its transaction while the continuation commits: retried
  against the continuation, applied once);
  `TestAnInterruptedWaitingAttemptIsGivenBack`;
  `TestAnInterruptedAttemptIsGivenBackOnce` (continued, stopped before
  any dispatch, continued again: the task's `extra` is one more, not
  two); `TestAReplacedDecisionKeepsTheRefund` (an identity's response
  interrupted by a crash; the continuation gives the attempt back and
  supersedes dr-1 by dr-2; stopped before any dispatch and continued
  again: `extra` still one more, dr-2's history with no refund of its
  own);
  `TestADoomedGateIsCancelled` (`build` fails for good while `deploy`
  is held: the gate is cancelled with the words, the run ends, `Prune`
  takes it); `TestARetriedTaskAsksItsGateAgain` (then `build` retried:
  a fresh gate holds `deploy` in the retry's transaction, `build`
  succeeds, `deploy` waits for the answer); `TestARetriedRejectedTaskAsksAgain`
  (Reject cancels `deploy`; its retry opens a fresh gate, and Approve
  starts it); `TestAnAcceptedTaskAsksItsGateAgain` (`build` failed and
  accepted anyway); `TestARetryAfterAContinuationAsksAgain` (both
  retries in a continuation of the run whose gate was cancelled);
  `TestAnApprovedGateStaysReleased` (approved, `deploy` failed and
  retried: no fresh gate); `TestAReleasingCancelIsAWaiver` (cancelled
  with `release: true`, then a retry: no fresh gate);
  `TestTheRunsEndCancelsItsDecisions`;
  `TestAGateWithNoTaskKeepsNoRunOpen`;
  `TestATypingCutByARestartIsNeverTypedAgain` (`delivery.typing` without
  its end; the continuation writes `delivery.uncertain` and the task
  file); `TestAnAnswerNeverReachesAnotherAttempt` (the asking attempt
  gone, another task's attempt live: nothing typed into it);
  `TestANewCandidateSupersedesItsApproval` (S1);
  `TestAnAppliedApprovalIsSupersededByANewCandidate` (approve candidate
  A, its application done, candidate B submitted: `superseded`, the
  response and application receipts kept, the history holding both);
  `TestAnArtifactThatChangedSupersedes` (S2, the file committed again
  with other bytes between the response and the application);
  `TestAnUnreadableArtifactIsNeverValidated` (the asking worktree gone
  and the next attempt's checkpoint holding other bytes, and a path
  missing at that commit: superseded, never applied as validated);
  `TestAnApprovalOfAMovedCommitIsSuperseded` (S4);
  `TestASupersededDecisionLeavesNoHold` (its asking attempt already
  ended: the task's next attempt dispatches, the words in its task
  file); `TestASupersededGateMovesItsHolds`;
  `TestAnIdentityAnswerIsNotAppliedAfterARestart` (an identity's
  response committed, the server ended before its application: on the
  continuation the decision is superseded by a fresh one with its holds,
  nothing applied); `TestARevokedIdentityIsNotApplied` (the boot
  unchanged: an identity's response committed, the application held
  until `ConfirmAuthority` says not admitted: superseded by a fresh
  decision with its holds, nothing applied; and not confirmed at all
  while the run goes on: nothing applied); `TestAReopenCancelsTheTasksDecisions`
  (S3); `TestARejectedGateCancelsItsTasks`;
  `TestTheRunWaitsOnAHeldTask` (no outcome while only held tasks remain);
  `TestPauseHoldsReleasedTasks`; `TestTheTaskFileCarriesTheAnswer`
  (golden, with `taskfile_golden_test.go`'s fixtures);
  `TestAReleasedGatesAnswerStaysInItsTasksFiles` (an applied
  clarification gate's answer in each task it released, in a retried
  attempt of one of them, and again in their attempts after a
  continuation);
  `TestASupersedingRequestMovesTheHold`; `TestMoreInfoIsTypedAndBumpsRev`;
  `TestAnOperationRepeatsItsReceipt` (a comment, a more_info, a route
  and a cancel each sent again after a lost reply: the same receipt, no
  second row, the more_info typed once; sent again after the decision
  was answered, and after a continuation moved it: the same receipt;
  the same id with another body: `op_conflict`).
- `internal/session` (`decision_test.go`):
  `TestADecisionQuestionRefusesEveryOtherAnswer` (`answer`, `AnswerAs`,
  `Submit` with its id); `TestADecisionQuestionOutlastsKeysAndTheScreen`;
  `TestADecisionQuestionIsAnsweredByItsToken` (the engine's submission
  answers it, recorded by the responder's name);
  `TestADecisionQuestionIsSetAsideAndShownAgain`;
  `TestADecisionLinePassesAnotherDecisionsMirror` (a `Submit` with
  `Decision` dr-1 and `UnlessWaiting` while dr-2's mirror shows is typed
  at once and leaves dr-2's mirror up; the same while an ordinary
  question shows waits).
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
  question); `TestARoutersAnswerIsRefusedUnderLocalPolicy` (its
  authority `via: router` with no grant, though its subject is an admin
  principal's); `TestAnIdentityAnswerToARunNotHeldIsRefused`
  (`not_held`, the workbench token's accepted);
  `TestAnIdentityIsRecheckedBeforeItsAnswerApplies` (the fake identity
  provider admits the token at the response and revokes it before the
  recheck, the server not restarted: the decision is superseded and
  nothing applied; admitted at the recheck: applied; a provider late
  past `ext.IdentityWait`: superseded; the token in no ledger row and
  no log line);
  `TestTheFullestDecisionFitsItsBound`; `TestADecisionResolvesItsEvidence`;
  `TestAnswerToADecisionIsRefused` (the WebSocket's `answer`);
  `TestTheGuideListsTheDecisionOperations` (and its bounds, measured by
  `TestEachSectionIsBounded`); `TestATicketListsItsDecisions`;
  `TestTheWebhookTakesDecision`.
- `internal/hostagent`: `TestAnswerToADecisionIsRefused` (the peer's
  shared refusal).
- `internal/config`: `TestDecisionIsAWebhookEvent`.
- `internal/dispatch`: `TestADecisionLinkIsRefused` (the new words).
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
not on others; read again on the stream's `snapshot` after a decision
changed while it was down, and on the sessions' cadence while it is
down), the palette rows' test.

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

### HM1, human members

**Go, race** (`go test -race -count=1 -timeout 30m ./...`, `make lint`,
`make lint-static`):

- `internal/crew`: `TestValidateTakesPeople` (four responsibilities, a
  task owner, a crew with start rules and people);
  `TestValidateRefusesBadPeople` (a table: thirteen people, a name of 61
  characters, a control character, a name used twice, a person named as a
  member, an unknown responsibility, one held twice, one held by two
  people, a note of 501 characters, a task owner not in the crew);
  `TestTaskOwnerFallsBackToTheAcceptanceOwner`;
  `TestAPersonIsNeverStarted` (a crew of two members and three people
  launched, managed and not: the fake sessions made are the members'
  attempts only, no worktree under the people's names, the reservations
  counted for members only); `TestTheRunsPeopleAreTheLaunchs` (the saved
  crew edited after launch; the run, its recovered record and its
  continuation keep the launch's people and owners);
  `TestTaskFileNamesWhoAnswers` (a new golden) and the existing goldens
  unchanged for a crew without people; `TestStoreRoundTripsPeople` and a
  legacy crew's JSON unchanged; `TestARecordWithoutTasksKeepsNoPeople`
  (a crew with start rules and people, run and ended: its record's JSON
  has no `people` key); `TestAManagedRecordIsRebuiltWithItsPeople` (its
  file deleted, as an older build's prune would: the next start's
  `syncRecords` writes it again with the launch's people and owners);
  `TestAnOlderBuildsContinuationLosesNoPeople` (the downgrade: a run
  launched with people and owners, a continuation row written with its
  crew decoded and encoded through a copy of the pre-HM1 `Crew` type, as
  an older build's `ContinueRun` and `addRun` do, and that
  continuation's record deleted; the upgrade: the next start's
  `syncRecords` rebuilds it with the launch's people and owners, the run
  routes answer them, and a further continuation carries them in its
  own row).
- `internal/api`: `TestCrewRoutesCarryPeople`, `TestRunRoutesCarryOwners`,
  `TestPeopleIsTheUnionOfTheStores` (a name in Dispatch only, in a crew
  only, in both; Dispatch unavailable), `TestPeopleIsPaged` (201 names:
  a page of 200 with `next`, then one; `q`; a person in 21 crews: 20
  and `crewsMore`), `TestAPersonsWork` (tasks owned in two runs, owned
  by fallback, tickets counted, `404` for a name no source knows, a
  person removed from the saved crew after the launch still answered
  with that run's tasks, the bound of 200 with `more`),
  `TestPersonNameBoundIsDispatchs`, `TestADraftsPeopleAreCheckedAndKept`
  (a Brakeman draft whose people break `checkPeople` refused by its
  check with those words; a good one made into a crew whose file holds
  its people and owners).
- PR-12: `internal/dispatch` `TestPlanDecisionFor` (the shape only:
  `role:anything` passes the store, `role:` and a control character do
  not; an older body's digest unchanged; the `assignment` kind);
  `internal/crew` `TestAGateNamesItsLabel` (the request's `for`, the one
  opening `routed` row naming both, in `CreateRun`'s transaction), `TestALabelResolvesFromTheCrewAsLaunched`
  (the saved crew edited after approval, and a continuation: the record's
  person unchanged), `TestAnAgentsLabelNoPersonHoldsIsRefused`,
  `TestAnAssignmentGateForAPerson` (Done releases, Not done cancels, as
  DR1's gate does), `TestRouteDecisionToAPersonAndARole`,
  `TestARouteChangesForOnlyWhileOpen` (a route to ana bumps rev and a
  response at the older rev is `stale_decision` naming ana; a route
  between the response and the application refused `decision_answered`,
  the receipt's, the typed line's and the task file's `for` still nate;
  a route of an applied decision refused `decision_closed`);
  `internal/crew/ledger` `TestDecisionsFilterByPerson`; `internal/api`
  `TestTheOperatorAnswersForALabel` (the receipt's `actor` `admin` and
  `for` nate, architecture), `TestProposalRefusesALabelTheCrewLacks`
  (nobody holds it; a responsibility not among the four),
  `TestAPersonsDecisions`; `internal/cli` `--for`, `--decision-for` and
  the tool schema golden (`testdata/tools-list-main.json`);
  `internal/agents` the skill's sentence.

**vitest** (`npm --prefix web run typecheck`, `npm --prefix web test`):
`utils/crews.test.ts` (`peopleProblems`, `toCrewInput` with people,
`ownersFollowPeople`, `ownerOf`), `utils/people.test.ts` (`personRows`:
the words, the key, the page it opens, the order), and in PR-12 the
"for" words in DR1's `utils/decisions.test.ts` and
`utils/dispatchPlans.test.ts` (`planDecisionsOut` keeps `for` and the
assignment kind; `decisionsProblem` names a `for` the crew no longer
resolves).

**Playwright** (`make test-e2e`, the specs alone first):
`crew-people.spec.ts` (new): a crew given an architecture owner and an
acceptance owner saved, read back after a reload, the second
responsibility holder refused in the editor and by the server; launched
with the stub worker: the header's people, each task row's owner and the
fallback's words, the sessions of the run counted by its id equal to the
attempts (none for a person), the person's page listing their tasks, the
palette's `@` row opening it. `tasks-editor.spec.ts`, `tasks.spec.ts`
and `dispatch-plan.spec.ts` unchanged and green. PR-12:
`decisions-people.spec.ts`: a plan revision whose approval is `for:
role:architecture` before `build` and whose assignment is `for:
person:ana` before `docs`, proposed through the page (the For select
and the Assignment kind), then revised through the page with only its
note changed, the revised revision's body read through the API still
holding both `for`s; the revision approved and launched; the gates hold
both tasks; the card
says "for nate, the architecture owner"; the operator approves and the
receipt's words say for whom; `build` starts; ana's page lists her
assignment until it is answered Done, and `docs` then starts; DR1's
`decisions.spec.ts` and `decisions-gate.spec.ts` unchanged and green.

**Scripts, desktop:** not touched.

**By hand**, what the tests cannot show: a real agent (Claude Code or
Codex) reading "Who answers for this work" in its task file and asking
for the architecture owner's decision by its responsibility, not by
typing in the terminal; the person's page in a phone's browser, against
a server on the LAN Linux box or the Mac, never the WSL box (a phone
reaches a server inside WSL only through networking that AGENTS.md
forbids asking anyone to configure).

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
   artifact subject is the blob at a commit, read from the repository's
   object store, never the server's copy of a live file, so a guest's
   copied tree cannot leave it stale; a live attempt's `HEAD` (the commit
   an S2 or S4 recheck reads the subject at) is read from its worktree
   natively, and under F the Runtime gives it, its objects coming back
   as a result's commits do; nothing an attempt
   needs is only in the server's memory (the record is in the ledger, the
   answer in the file).
4. The agent talks to the server over the network: honoured; the tools
   and the CLI reach the new routes over HTTP with
   `CONDUCTOR_NOTIFY_TOKEN`, as `task_blocked` does.
5. Checks go through `internal/proc`: not touched; no check is run here.
6. Platform splits stay platform tags: not touched; nothing
   platform-specific is added.
7. Evidence rows gain `where`: not touched; a decision is not evidence of a
   process and assumes none (an artifact's digest is its blob's at a
   commit, wherever the worktree is mounted); no row here assumes a
   native process.
8. `hostagent` is a launch site: not touched for launching; the host's
   peer only shares the session package's refusal of an answer to a
   decision's question.

### HM1, human members

1. **Attempts only through `crew.Launcher`.** Honoured: a person is
   started by nothing; the launcher's callers are unchanged.
2. **`session.Local` drives a `session.Process`.** Not touched: no
   session code changes.
3. **An attempt's context is files under its worktree.** Honoured: the
   owner and the people go into `.conductor/task.md` and `task.json`,
   written through the `os.Root` as today; nothing an attempt needs about
   people lives only in the server's memory.
4. **The agent reaches the server over the network.** Honoured: the one
   thing an agent sends about people, `decision_request`'s `for`, goes
   over DR1's HTTP route with `CONDUCTOR_NOTIFY_TOKEN`.
5. **Checks through `internal/proc`, with where they ran.** Not touched.
6. **Platform splits stay platform tags; capabilities, never plans.** Not
   touched: no platform code, and nothing is shown by plan.
7. **Evidence rows carry `where`.** Not touched: HM1 adds no evidence
   row; a decision's receipt is DR1's.
8. **`hostagent` is a launch site too.** Not touched: a person launches
   nothing on a server or a host.

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

### HM1, human members

- [x] Article 1, no premium code or licence check: people, owners and
  labels are plain open-source data; no provider is asked.
- [x] Article 2, the free build useful alone: a crew's owners and a gate
  answered for a person work with no account.
- [x] Article 3, paid features gated by hosted dependencies: the
  authenticated layer is SW1's and ID1's, behind their interfaces; HM1
  adds no flag a fork could flip.
- [x] Article 4, required approvals free and never degraded: a gate
  naming a person still needs the operator's answer with the control
  role, and the crew's people are exported with its file.

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
| PR-2 | crew: decisions opened by an attempt and by a plan's gates (the opener not yet called at launch), the holds, the response, the application to a live attempt | `internal/crew`, `internal/session` | PR-1 | none | ~1,600 |
| PR-3 | crew: an attempt that ends while it waits and the parking, the run's end, continuations and recovery, supersession and cancellation, comments, the task file | `internal/crew` | PR-2 | none | ~1,700 |
| PR-4 | api, cli: the decision routes, the guide's operations with the four MCP tools' handlers, the event in the three places, the notifications, the ticket's decisions, the evidence's refs and baseline, the identity's recheck, the run's holds; the gates opened at launch | `internal/api`, `internal/cli` (the four MCP handlers), `internal/config`, `internal/dispatch`, `internal/crew` (the gates' one call), `web/app/utils/protocol.ts`, `docs` | PR-3 | none | ~1,800 |
| PR-5 | session, proto, ext: the mirrored question and its refusal, the attention's `decision`, the router told of decision records | `internal/session`, `internal/proto`, `internal/api`, `internal/hostagent`, `ext`, `ext/exttest`, `web/app/utils`, `docs` | PR-4 | PR-6 | ~1,100 |
| PR-6 | cli, agents: `conductor task decide`, `conductor decision`, the skill | `internal/cli`, `internal/agents` | PR-4 | PR-5 | ~700 |
| PR-7 | web: the decision card and its answer, the run page, the session's bar, Needs you, the palette, the ticket page; the stub worker and two specs | `web/app`, `web/e2e` | PR-5, PR-6 | none | ~1,400 |
| PR-8 | e2e and docs: the restart spec, the gate's rejection, `docs/architecture.md`, the features entry | `web/e2e`, `docs` | PR-7 | none | ~500 |

**PR-1** (`internal/crew/ledger`, `docs/repository-map.md`): the schema
and migration of The changes (the `evidence` column and
`decision_refunds` with the rest), `decisions.go`'s `Tx` writers
(`Tx.Refund` among them) and the `Ledger` reads, the bounds, `Prune`'s refusal, `Recover` and the
snapshot unchanged; no caller yet, so no behaviour changes. Tests: the
ledger's list above. If RV1's ledger PR has merged, this one takes the
next version and says so.

**PR-2** (`internal/crew`, `internal/session`): `decisions.go`'s types,
`RequestDecision` with its bounds and subjects (the artifact's blob),
the gates' opener for the first transaction, which `startManaged` does
not call yet (PR-4 does, with the response route), `pump`'s hold rule
and the held task's words, `RespondDecision` (through `transact` or the
ledger directly, the conditional `Tx.Answer`, `not_held`) with every
recheck but the ones PR-3 adds, the authority's recheck at application
with `ConfirmAuthority` (an identity's response applied only once
confirmed), `applyDecisions` for a live attempt and for a gate (typed by
the member's deliverer ahead of its handoffs, the shown mirror's answer
first and other decisions' answers past it; released; cancelled), the
mirror on the attempt's `askMu`, the result's `decision_open` refusal
and the reminder held back, and `session.Local.TryAskDecision` with the
session's refusal of every other answer to it and its `UnlessWaiting`
rule for a line naming a decision (the attention's field
stays unexported to viewers until PR-5). No route yet and no gate at a
launch: the engine is driven by its tests, and nothing on `main`
changes for a person. Tests: the `internal/crew` list's opening,
holding, applying, mirror, handoff, result and gate tests, and the
`internal/session` list, `TestTwoAnswersQueuedBeforeDeliveryAreBothTyped`
and `TestARevokedIdentityIsNotApplied`.

**PR-3** (`internal/crew`): the attempt given back while it waits (O5)
with its `refunded` row and its `decision_refunds` row, the parking of
a waiting attempt on its `turnEnded` mark (I12), the cancellations at a
doomed gate and at the run's end (I11), `regate` and `r.gates` in
`retryTask`, `acceptTask`, a reopen and a continuation (I14), the
continuation's move and give-back (O6, `Tx.MoveDecisions`, the refund
once per attempt), the typing's intent and its uncertain end (I6), S1 to S4 with an
applied approval's supersession, the disposal of the holds (I10), every
cancellation, `WithdrawDecision`, `CancelDecision`, `CommentDecision`,
`RouteDecision`, the task file's section (released holds included) and
the package section's new last line, the continuation note's words,
`crew.Recovered`'s holds. Tests: the rest of the `internal/crew` list,
`decisions_restart_test.go`, the task file golden.

**PR-4** (`internal/api`, `internal/cli`, `internal/config`,
`internal/dispatch`, `internal/crew`, `web/app/utils/protocol.ts`,
`docs/protocol.md`),
after PR-3, whose `WithdrawDecision`, `CancelDecision`,
`CommentDecision` and `RouteDecision` its routes and operations call:
the routes table, the guide's eight operations and its rule line, with
the four session operations' MCP tools handled in `internal/cli/mcp.go`
(`mcpHandled`) and the tool schema golden (`testdata/tools-list-main.json`)
in this same PR, since `TestAnUnhandledToolIsNotListed` fails a table
tool with no handler and every PR passes the whole module's tests, the
result route's `decision_open`, the run and task reads' `holds`, the
`decision` event in all three places at once (`internal/api`'s stream,
its type in `protocol.ts`, its row in `docs/protocol.md`), the
notification and `EventDecision`, the ticket's decisions route, the
decision link's refusal words, the evidence's `refs` (`decision_refs.go`
and the lookups factored out of `brakeman_refs.go`) and the baseline
the request route resolves for `RequestDecision`, `authorityOf`, the
identity's recheck after a response's commit and its `ConfirmAuthority`,
and
`startManaged`'s call of the gates' opener, so that a gate first holds a
launch's tasks on `main` in the PR that lets the operator answer it.
Tests: the `internal/api` list but the router's and the WebSocket's,
`internal/config`'s and `internal/dispatch`'s, and `internal/cli`'s
`TestMCPListsDecisionToolsOnlyInATask` and
`TestDecisionRequestToolAnswersPending`.

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

**PR-6** (`internal/cli`, `internal/agents`): the `task` subcommands,
`conductor decision`, completion, the skill paragraph (the four MCP
tools are PR-4's). Tests: the `internal/cli` list but PR-4's two and the
`internal/agents` list.

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

### HM1, human members

HM1's PRs are numbered after DR1's. PR-9 to PR-11 need only `main` and
may be cut at any time, before, between or after DR1's; they share
`internal/crew`, `internal/api` and `web/app` with DR1's PR-2 to PR-7,
so none runs beside those, and whichever of two such PRs merges second
merges `main` in first. PR-12 waits for all of DR1. Brakeman's PR-18
(`docs/plans/brakeman.md`) waits for PR-12.

| PR | Title | Touches | Depends on | May run beside | Size |
|---|---|---|---|---|---|
| PR-9 | crew, api: people in a crew, a task's owner, the run's people, the task file's "Who answers", `GET /api/people` | `internal/crew`, `internal/api`, `docs` | none (main) | PR-6 | ~1,100 |
| PR-10 | web: the crew editor's People and Owner, the run page's people and owners, the revision's people | `web/app`, `web/e2e` | PR-9 | PR-1, PR-2, PR-3, PR-6 | ~900 |
| PR-11 | api, web: a person's work, the people page, the palette's `@` people | `internal/api`, `web/app`, `web/e2e`, `docs` | PR-10 | PR-2, PR-3, PR-6 | ~800 |
| PR-12 | dispatch, crew, ledger, api, cli, agents, web: whom a decision is for, an assignment for a person, the operator's answer for them, the features entry | `internal/dispatch`, `internal/crew`, `internal/crew/ledger`, `internal/api`, `internal/cli`, `internal/agents`, `web/app`, `web/e2e`, `docs` | PR-11, PR-8 (all of DR1) | none | ~1,300 |

**PR-9** (`internal/crew`, `internal/api`, `docs/protocol.md`): `Person`,
the responsibilities and their words, `Task.Owner`, `checkPeople` and the
task-text count, `TaskOwner`, `Run.People` and the tasks' owners from the
crew as launched (the launch, `Recovered`, `ContinueRun`, read from
the chain's launch row; in a managed run's record only), the task
file's section, the crew and run
routes' fields, `crewInput.crew()` in place of the three copies, `GET
/api/people` with its pages, the protocol rows. Go only. Tests: the
`internal/crew` tests and the `internal/api` tests above but
`TestAPersonsWork`.

**PR-10** (`web/app`, `web/e2e`): `CrewPeopleTable.vue`,
`PersonPicker`'s `names` prop and `usePeople`, the Owner column, the
editor's section, `utils/crews.ts`'s functions, the run header's chips,
`RunTasks.vue`'s owner, `DispatchPeople.vue`'s revision people. UI only.
Tests: `crews.test.ts`; `crew-people.spec.ts` without the person's page.

**PR-11** (`internal/api`, `web/app`, `web/e2e`, `docs/protocol.md`):
`GET /api/people/{name}` and its row, `pages/people/[name].vue`, the
chips' links, the palette's `@` people (`usePaletteRows.ts`,
`utils/people.ts`). Go and UI. Tests: `TestAPersonsWork`,
`people.test.ts`, the person's page and the palette row in
`crew-people.spec.ts`.

**PR-12** (`internal/dispatch`, `internal/crew`, `internal/crew/ledger`,
`internal/api`, `internal/cli`, `internal/agents`, `web/app`, `web/e2e`,
`docs`), once DR1's PR-8 has merged: `PlanDecision.For` and the
`assignment` kind, the proposal's check, the CLI's flags, the gate's and
the request's `for` resolved in their transactions, `RouteDecision`'s
`person` and `role`, the receipt's `for`, the ledger filter, the routes'
`for`, the route's `for` taken only while open, the person's decisions,
the tool schema and the skill's sentence, the proposal form's For select
and Assignment kind and `planDecisionsOut`, the cards' words; the
features entry, the repository map and the
architecture. Go and UI. Tests: the PR-12 tests above and
`decisions-people.spec.ts`.

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
    run go on. Dependents of that task already wait on its result, which
    the task cannot submit while its decision waits. A waiting attempt
    whose agent has ended its turn gives up its member and its slot when
    a ready task needs them (it is parked, and given back), so a member's
    other tasks and the run's other work are never held by it.
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
    the task's next candidate, even once applied; an artifact is a blob
    at a commit, and its approval is superseded by a change of the blob
    the work rests on, or by its being unreadable; an approval's commit
    subject is rechecked until it is applied, and a clarification's or
    an assignment's is shown and not rechecked; a plan subject never
    moves inside a run.
15. **The bounds** of The changes (500-character question, 4,000-character
    context, 2 to 6 alternatives, 20 evidence refs, 16 KiB request, 4
    open per attempt, 64 per run, 1,000 per run, 32 responses, 32
    operator operations, 256 history rows, a 1 MiB record), each the
    existing bound of the nearest record
    (Dispatch's decision question, Brakeman's decision card, the
    blocker's message, the run commands' receipts) where there is one.
16. **More information is a new request**: the operator's "Ask for more"
    is typed to the agent, which answers with a request that supersedes
    the first, so the history shows both and the answer binds to the
    second.

### HM1, human members

Dated 2026-11-05; defaults the owner can overturn with a row in the
Program's status log (the Program, section 3.1).

17. **One plan with DR1.** The Program gives DR1 and HM1 the same plan
   path; HM1's part sits under DR1's in each section and its PRs follow
   DR1's. HM1 resolves no open decision: O4 (human policy) blocks SW1 and
   O10 (Dispatch's conflict rules) blocks CS1, and neither is needed for
   a local label (decision D4).
18. **People are a list of their own, never members.** A person in
   `Members` would need an `AgentID` and a start rule and would pass
   through every launch loop; a separate list makes "never started" true
   by construction.
19. **Four responsibilities, closed, one holder each per crew.** The four
   the Program names, so a gate's label always names one person in the
   open-source build (the single approver); more roles, several holders
   and groups are SW1's.
20. **A task's owner falls back to the crew's acceptance owner.** A task
   with no owner of its own is owned by whoever accepts the crew's work;
   computed, never stored, so changing the acceptance owner before the
   next launch changes it.
21. **The names are shared by reading.** `GET /api/people` is the union of
   Dispatch's directory and the crews' people, and neither store writes
   the other: a cross-store write would need a second receipt for one
   save, and a typed name is still offered everywhere.
22. **A person's work is an assignment, not a task row.** A person owns
   tasks, answers decisions, does assignments (DR1's `assignment` type,
   declared in a plan revision for them) and, with RV1, reviews; a crew
   task whose doer is a person would need attempts and results the engine
   cannot ask of a person, so none is added.
23. **The operator answers for any label.** Decision D4's single approver
   under DR1's `local/1`; the receipt keeps who answered and for whom
   apart, so ID1 and SW1 can later let the person answer for themselves
   as a new policy revision.
24. **A label is resolved and recorded when the decision opens**, in the
   request JSON DR1 keeps, with no ledger column: one person per label
   in this build, and the person's page reads it through a filter on
   that JSON.

## Acceptance

The boundary (the Program, section 4.3, DR1): one authorized person
answers a decision bound to a revision; the response survives a restart,
is applied once, and releases only the work that depended on it; an
unanswered decision consumes no retry.

How it will be observed, by hand, after PR-8 has merged, with real
Claude Code and Codex against a server of its own on the WSL box (no
networking configured by anyone) and the desktop app, in a scratch
repository both agents trust:

1. A Dispatch ticket's plan revision with three tasks and one gate:
   `build` and `lint`, independent of each other and both done by the
   same member, `dev` (Claude Code), and `deploy` after `build`, with
   the gate `deploy-approval` before `deploy`; its launch. `deploy`
   shows "waits for decision dr-1" from the start; the gate's subject is
   the run's plan revision `r1`. `build` comes first in the plan's
   order, so `dev` takes it, and `lint` waits for `dev`.
2. During `build`, before its result, its agent commits a file, saves a
   checkpoint, and asks an approval about that file (an artifact
   subject) through `decision_request` from the skill alone: the tool
   answers at once with `dr-2` and `open`, and the agent ends its turn;
   the session shows the question. `lint` is ready and needs `dev`, so
   the engine parks `build`'s waiting attempt: attempt 1 closes with
   stage `decision`, the run's events show `attempt.waited`, and dr-2's
   history a `refunded` row naming attempt 1; `lint` then runs on `dev`
   and succeeds while dr-2 is open, so the run's other task goes on with
   the same member.
3. The server is stopped and started on the same data with dr-2 open;
   the operator answers dr-2 from the run page: Recorded, "waiting for the
   run to be continued". Continue: `build`'s next attempt starts at its
   checkpoint with the answer in its task file; the record shows one
   `applied` row; attempt 1 still reads stage `decision`, with its one
   `refunded` row, so it was not counted (an attempt's number says
   nothing of that: the next one is attempt 2 either way). `build` then
   submits and succeeds.
4. A second Approve of dr-2 from another tab gets the first's receipt
   (the same id) or, with a new id, "answered by" the operator.
5. The operator approves dr-1: `deploy` starts and the run succeeds. A
   second run of the same revision rejects dr-1: `deploy` is cancelled
   with the rejection's words, `build` and `lint` unaffected.
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

### HM1, human members

The boundary (the Program, section 4.3, HM1), in this plan's words: a
person is in a crew as its architecture owner, and a gate for that
responsibility is answered by the operator on the person's behalf.
Observed by hand on the open-source build with no account (a plain
build of this repository, nothing registered), with a real agent, on a
server of its own on the LAN Linux box (or the Mac), steps 1 to 5 in a
desktop browser and step 4's person page read again from a phone's
browser on the same LAN; not on the WSL box, whose server a phone
reaches only through networking that AGENTS.md forbids asking for:

1. In the crew editor, add a person (nate) holding architecture owner and
   another (ana) holding acceptance owner; save; reload: both read back.
2. Propose and approve a plan revision of a Dispatch ticket from that
   crew, with a decision `schema` of kind approval `for: role:
   architecture` before `build`; launch it.
3. The run's header lists nate and ana with their words; `build`'s row
   says its owner; the sessions list and `.conductor/worktrees/<run>/`
   hold nothing for either person.
4. The gate holds `build`; the decision views and nate's page say "for
   nate, the architecture owner".
5. Answer it as the operator; the receipt says by the operator, for nate;
   `build` starts; nate's page no longer lists it.

Done means those five observed and recorded in the features entry, with
PR-9 to PR-12 merged and green and every defect the check finds fixed in
the stream (AGENTS.md). The owner reviews this plan and its
decisions, and either observes the check or reads its record; the rest is
the agents'.
