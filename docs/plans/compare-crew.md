# Compare approaches: one ticket, one base, isolated candidates checked alike, a Brakeman comparison and a person's recorded selection (Program item CP1)

Status: proposed 2026-11-06, for the owner's review; the owner sets it
to Planned. Main at a43d5c56 (PR #676, 2026-11-06). Every fact below
about the code was read from that commit on 2026-11-06, and every fact
about work not yet merged from the branch of its open pull request on
the same day; file, function and constant names are those. Revised in
review round 1 the same day, after reading again the decisions plan on
`main` with its rows' branches (`issue-634`, PR #664; `issue-635`, PR
#672; `issue-636`, PR #678; `issue-637`, PR #683), the review plan on
`main` with its first row's branch (`issue-618`, PR #665) and the
capacity plan at its branch head (`plan/capacity`, PR #668); revised
again in review round 2 after reading `internal/api`'s agent-token
paths, the engine's refunds (`withdrawFrom`, `steps.go`), `checkTree`,
the candidates route's revision rule, DR1's `room` and
`MaxDecisionsPerRun` (`issue-636`) and `decisionSession` (`issue-637`);
and again in review round 3 after reading `settleRun`, `commit` with
DR1's `endedWork`, `startContinued`'s marks, `regate` and
`readDecisionHistory`'s `maxChain` (`issue-636`), `gitcli`'s runner,
Brakeman's `filterCandidates`, and the `authenticate` of the files and
session reads; and once more after the final check, after reading
`endedWork`, `recordStop` and `CancelDecision` (`issue-636`) and the
server's `Shutdown` (`main`); the lists of what changed are at the end.

Some changes have no single right design until two have been tried: a
cache keyed one way or another, a hard bug with two plausible causes, a
refactor that could go two ways. Today a person who wants two attempts
at one ticket launches two runs by hand, from whatever commit `HEAD` is
at each launch, with whatever checks each crew happens to have, and
compares them by reading two run pages side by side; nothing records
which one was chosen, by whom, or why, and nothing stops the second
run's agent from reading the first one's branch. This plan gives
Conductor a **compare-approaches crew**: a template that makes a crew
with two to four **candidate** tasks for one ticket, launched as an
ordinary approved plan revision (Program item DS2), all starting at
**one base revision**, under **one objective and one set of criteria**
fixed at launch, each in its own lane that no other lane's work reaches
through Conductor, every candidate checked by **the same checks**; a
**comparison** page that puts the candidates side by side with what
Conductor measured itself (the checks' receipts and whether they are
comparable, the diff, the dependency changes, whether a candidate is
independent of the others), kept apart from what agents say (their
summaries, their reviews, **Brakeman's drafted comparison**); and a
**selection** a person makes, with a rationale, recorded as a durable
decision (Program item DR1). The selected candidate continues into
review and repair (Program item RV1) as the commit it is, never rebuilt;
the others stay as they are, labelled not selected.

It is Program item CP1 (the Program, section 4.5), written after reading
the crew model and its validation (`internal/crew/crew.go`), the managed
run's engine, its base and its bounds (`run.go`, `task.go`: `prepare`,
`startManaged`, `maxActiveAttempts`), the planned launch (`planned.go`,
`package.go`), the checks and the project manifest at each tree's commit
(`verify.go`, `manifest.go`, `steps.go`: `resolveAt`, `checkTree`), the
members' messages (`messages.go`), the attempts' context (`context.go`),
the example crews (`examples.go`), the ledger's check rows
(`internal/crew/ledger/rows.go`), the API's candidates, launch and
Brakeman read routes (`internal/api/candidates.go`,
`dispatch_launch.go`, `dispatch.go`, `brakeman_observe.go`), Brakeman's
request kinds and brief kinds (`internal/brakeman/request.go`,
`kinds.go`), Dispatch's plan revision body
(`internal/dispatch/model.go`, `PlanBody`), the crew editor, run page
and ticket page (`web/app/pages/crews/[[id]].vue`, `runs/[run].vue`,
`components/DispatchLaunch.vue`, `DispatchCandidates.vue`,
`DispatchRuns.vue`), and the plans this one builds on:
`docs/plans/dispatch-evidence.md` (DS2),
`docs/plans/project-manifests.md` (EN1), `docs/plans/review-repair.md`
(RV1) with its first row's branch (`issue-618`, PR #665),
`docs/plans/decisions.md` (DR1 and HM1) with its first rows' branches
(`issue-634`, PR #664; `issue-635`, PR #672; `issue-636`, PR #678;
`issue-642`, PR #671), `docs/plans/brakeman.md` (BK0 to BK3),
`docs/plans/runtime.md` (C10), `docs/plans/ext.md` with `ext/` and
`ext/exttest`, and the capacity plan as it stands on its pull request
(`docs/plans/capacity.md` on `plan/capacity`, PR #668, final and
awaiting the owner's approval; this plan follows its final text). It is
reviewed before any of its code; the PRs at the end are cut into issues
after the review, one issue, one branch `issue-N`, one PR each
(AGENTS.md).

The names follow the railroad (the Program, section 1.2): Conductor is
the application where the person compares and selects; Yardmaster the
agent harness that runs the candidates, checks them, holds the
selection's wait and applies it; Brakeman the sidekick that drafts the
comparison and decides nothing; Dispatch the ticketing, whose ticket the
comparison is for and whose approved plan revision launches it;
Switchman the routing of the selection to the person it is for, whose
team routing is Program item SW1. Five words are kept apart here: a
**candidate** is a task's standing result, a commit with its receipts,
as DS2 shows it; a **lane** is one candidate task with the review task
that reviews it, if any, and the member who does each; a **comparison**
is what Conductor measures and shows of a compare run's candidates; a
**comparison draft** is Brakeman's brief about them; the **selection**
is the person's decision. "Template" means the crew shape this plan's
generator makes, an open-source crew template; it is not a shared or
versioned workflow template (Program item TS1's). An attempt's checkout
is a worktree, never a "workspace" (decision D5). "Recipe" does not
occur in this plan except to say that nothing of the catalog's agent
recipes changes.

**What is on `main` and what is not.** CP1's prerequisites are DS2, RV1
and BK1 (the Program, section 4.5).

- On `main`: DS2 whole (plan revisions made from a saved crew with
  tasks, the planned launch at the revision's base with its frozen
  package, a run's candidates with their receipts, a delivery link's
  candidate: `docs/plans/dispatch-evidence.md`); EN1's project manifest
  and its checks (`docs/plans/project-manifests.md`); C10's Runtime with
  `where` on every check row (`docs/plans/runtime.md`); the extension
  seams C1 to C9 (`docs/plans/ext.md`); Brakeman whole, BK0 to BK3, with
  DS2's records as its evidence and drafts (`docs/plans/brakeman.md`).
  PR-1 to PR-5 below build on these alone.
- Not on `main`: RV1 (its first row is PR #665, issue #618; its other
  rows are issues #620 to #633) and DR1 with HM1 (their first rows are
  PR #664, issue #634; PR #672, issue #635; PR #678, issue #636; PR
  #683, issue #637; PR #671, issue #642; PR #674, issue #643; their
  other rows are issues #638 to #641, #644 and #645), and AC1 (the
  capacity plan, PR #668, final and awaiting the owner; its rows are not
  yet issues). The selection is a DR1 record, so PR-6a waits for DR1's
  #634, #635 and #636 and PR-6b for #637, and PR-7 for #640;
  continuing into review is RV1's review, so PR-8a waits for RV1's #623
  and #627 and HM1's #645 and PR-8b for RV1's #631; PR-9 waits for AC1's PR-7.
  Each waiting PR names its rows in The PRs.
- **No row waits for another plan to be amended.** Those plans are
  followed as they stand. Where this item needs their code to do more
  (a third origin in DR1's engine, an option bound to a candidate, a
  selection that keeps its run open, a required rationale, the card's
  subject, a refusal step in RV1's review routes, labels in its picker,
  a session count in the launch sheet, a request kind and a shared read
  in Brakeman), the change is this plan's own: made in one of this
  plan's PR rows, placed after the row of theirs it builds on, with its
  migration, its invariant and its test (What this plan changes in other
  items' code, below).

**The items this one meets, and who owns what they share.**

- **DS2** owns plan revisions, launches, packages, candidates and
  deliveries. This plan adds no Dispatch record: a compare crew is a
  crew, so its revision body carries the `compare` block inside the crew
  JSON it already freezes (`PlanBody.Crew`, a `json.RawMessage`); this
  plan adds a statement of what a compare launch starts and one field
  the launch must send (`candidates`), kept in the launch's fingerprint
  (`dispatch.LaunchArgs`), and refuses a delivery that names a
  comparison's candidate other than the one selected.
- **EN1** owns the project manifest; this plan reads which manifest ran
  each check (the ledger row's `Manifest`, the tree's
  `manifest.resolved`) to say whether two receipts are comparable, and
  changes nothing of manifests.
- **RV1** (`docs/plans/review-repair.md`) owns reviews, findings, marks,
  Request changes, Check again and the Review view. The selected
  candidate is reviewed there as any candidate is; this plan adds, in
  its own rows, one refusal step to the submit and retry of a
  `request_changes` review (#627's `dispatch_reviews.go`) for a
  comparison's candidate other than the selected one, a Check again
  arm (#620) and the labels the Review view's candidate picker shows
  (#631's `ReviewCandidate.vue`).
- **DR1 and HM1** (`docs/plans/decisions.md`) own the decision record,
  its routes, its card, Yardmaster's wait and application, and whom a
  decision is for. The selection is one DR1 record; this plan opens it
  from the engine and applies it through DR1's writers, and makes in its
  own rows the changes to DR1's code a third origin needs (PR-6a after
  #636, PR-6b after #637, PR-7 after #640, PR-8a after #645), the
  decisions plan followed as it stands.
- **Brakeman** (`docs/plans/brakeman.md`) owns request kinds, brief
  kinds, reads and grants. This plan adds one request kind (`compare`)
  with its brief rule and one shared read (the comparison route), in
  Brakeman's tables, in its own PR-5, after the rows that made those
  tables (#520, #525, #531) and the act (#534); Brakeman's plan is
  followed as it stands.
- **AC1** (`docs/plans/capacity.md`, PR #668) owns the capacity view a
  launch shows: accounts, usage windows, resets. This plan owns the
  statement of how many candidates, sessions, attempts, checks and
  Brakeman turns a compare launch starts; it sits above AC1's member
  lines in the launch sheet and says, until AC1's PR-7 merges, that
  usage per account is not shown in this build. This plan's PR-9, after
  AC1's PR-7, makes those member lines count the sessions one launch
  starts on each account; the capacity plan is followed as it stands.
- **MB0** (`docs/plans/decision-inbox.md`) lists DR1's records, so an
  open selection reaches the "Needs your decision" page through DR1 with
  no part of this plan.
- **SW1** (Switchman team routing, paid) later routes the selection to
  the role or person the template names; this plan records whom it is
  for and lets the operator answer, as decision D4 gives the free build.

## Articles it serves

1. No licence check: nothing here asks for a plan, a tier or a provider;
   a comparison needs what any managed launch from a ticket needs (the
   data directory, the ledger, Dispatch, `crews.managed`).
2. Useful on its own: comparing approaches is free work on the local
   ledger, Dispatch and git, with no account and no network; Brakeman's
   draft runs on the person's own agent subscription (decision D1), and
   the comparison stands without it.
3. Nothing is gated by a flag: there is no paid part and no switch; the
   template's own `brakeman` field says whether a draft is asked, which
   is the person's choice of their own subscription, never a tier.
4. Required approvals, stop, inspect, revoke and export stay free and
   never degrade: the selection is a required decision of a person,
   recorded with its rationale and receipt; the launch is DS2's approved
   revision; stop, pause, cancel and retry are the run's own; every
   record is in the ledger and Dispatch, carried whole by C6's export.
5. Dependencies point one way: no new module; `internal/crew` still
   never imports `internal/dispatch` or `internal/brakeman`; the
   dependency files are parsed with the standard library.
6. No feature build tag: a plain `go build` carries all of it.
7. The free defaults stand: no extension interface is defined or
   changed; the selection reaches a registered `DecisionRouter` as every
   DR1 record does; a registered `ContextProvider` is not asked for a
   comparison's attempts (its blocks' provenance cannot be checked, and
   the free default asks no provider), and is asked as today for every
   other attempt; the checks run through the run's Runtime as every
   check does.
8. Not touched: no hosted dependency exists in this item, so nothing
   waits on a service and nothing degrades without one.
9. `CGO_ENABLED=0` stays: no store is added; the ledger stays
   `modernc.org/sqlite`; git is read through the git binary with argv
   arrays, as the engine reads it today.
10. The repository contract holds: this plan before the code; race tests
    per package; vitest and Playwright for the pages; no new dependency,
    so `make notices` has nothing to regenerate; no frame or control
    message, so `internal/proto` and `web/app/utils/protocol.ts` are
    untouched and `docs/protocol.md` gains the HTTP rows, the crew's
    `compare` field, the task file's `compare` part and two run events,
    each with its bound.
11. The railroad names keep their meaning: Yardmaster runs, checks and
    applies; Brakeman drafts; Dispatch holds the ticket and its
    revision; Switchman routes; "candidate", "lane", "comparison",
    "comparison draft" and "selection" are told apart above; "template"
    is never TS1's shared templates.
12. Execution stays behind one seam: every candidate's attempt starts
    through `crew.Launcher`, every check goes through `internal/proc`
    under the run's Runtime with its `where`, and the comparison reads
    `where` to judge comparability; "Boxcar constraints" below takes the
    eight one by one.

## Why and what happens

The Program's work model (section 1.4) keeps a run, its tasks, attempts,
candidates, verification receipts and decision requests as distinct
records, and keeps verification and human acceptance as separate facts;
section 1.5 binds execution to an approved plan revision and asks every
client to act through typed operations with receipts; section 7 rules
out reshaping a managed crew while it runs, so a comparison has to be a
crew shape launched as an ordinary approved plan, not a run that grows
candidates. What `main` has that this builds on, and what it lacks:

- A managed crew's tasks with no needs all start at the run's base,
  which `prepare` pins once (`HEAD`, or a planned launch's base), and
  each attempt works in a worktree of its own at
  `<cwd>/.conductor/worktrees/<run>/<task>.<n>` on the branch
  `crew/<run>/<task>.<n>`; up to four attempts run at once
  (`maxActiveAttempts`), three per task (`maxAttemptsPerTask`).
- A task's checks are the crew's as launched (`r.plan`); a project
  manifest of the file form is read at each tree's own commit
  (`resolveAt`), so a candidate that edits `conductor.manifest.json`
  runs its own verify steps; each check row keeps its argv, the commit
  it checked (`Subject`), the manifest that ran it (`Manifest`), whether
  its checkout stayed intact (`Stable`) and where it ran (`Where`).
- Members of a managed run may message each other
  (`Engine.SendMessage`), with no rule about who may write to whom; a
  context provider's blocks for an attempt are taken as they come
  (`askContext`).
- Nothing measures two results against each other; the ticket page lists
  each run's candidates (`DispatchCandidates.vue`) one task after
  another.
- Brakeman can already draw a table (`comparison/1`) and cite candidates
  (`candidate:<run>/<task>/<n>`), but no request asks it to compare
  candidates, and it may decide nothing.

After this item ships, a person sees:

- **The template.** On the crews page, **New comparison** opens a form:
  the ticket's project folder (the crew's working directory), the
  objective (the crew's goal; the ticket's own intent travels in the
  launch package), the instructions every candidate gets, the criteria
  (one to twelve, each a line such as "no new dependency", "p99 of the
  cache read under 2 ms in `bench_test.go`"), the checks every
  candidate's result must pass (or the project manifest's verify steps),
  two to four candidates (each an agent, its arguments, which is how a
  model is chosen, and an optional approach: "key the cache by locale",
  "drop the cache and memoise in the handler", or nothing for
  independent tries of one approach), whether each candidate gets an
  agent's review, and whether Brakeman drafts the comparison. **Create**
  saves an ordinary crew with tasks (`approach-a`, `approach-b`, …) and
  a `compare` block, which the crew editor shows read-only beside its
  tasks. `conductor crews compare spec.json` does the same from the CLI,
  and Brakeman's `draft_crew/1` may draft one, which a person saves.
- **The launch says what it costs.** The crew is proposed as a plan
  revision of the ticket and approved (DS2, unchanged). Launch from that
  revision shows, before anything starts, what it multiplies:
  "This comparison starts 3 candidates (Claude Code, Codex, Claude
  Code): 3 agent sessions, once the plan's decisions and the project's
  prerequisites let the work start; then an agent review of each
  candidate that succeeds: up to 3 more sessions, none for a candidate
  that fails or is cancelled; then what 3 runs of one candidate's crew
  would take, since each candidate is worked as a task of its own crew is
  (each attempt a session of its own; 3 counted attempts a task, and a
  review's request for changes, an attempt that could not start and
  each Retry you give add more); 2 checks on each submitted candidate
  attempt (the task's checks and conductor.manifest.json's verify steps
  at the base; a candidate that changes the manifest runs its own);
  1 Brakeman turn on your Brakeman account, when all candidates are
  done. Usage per account: shown below" (AC1's member
  lines, once its PR-7 has merged, with PR-9's count of this launch's
  sessions on each account; until then: "Usage per account is not shown
  in this build"). The button says **Launch 3 candidates**, and the
  launch is refused unless it says the same number. Launching the crew
  from the crews page is refused in words: "a comparison is launched
  from a ticket's approved plan revision", and so is a launch by
  Brakeman or any provider through `ext.Host`: "a comparison is launched
  by a person, who reads what it starts".
- **The candidates run apart.** Each candidate's attempt starts at the
  run's base with the same instructions and its own approach; its task
  file says it is one of three candidates working alone, lists the
  criteria, and names no other lane. Nothing Conductor gives an agent
  carries another lane's work: a message to another lane's member is
  refused ("approach-b's member is in another lane of this
  comparison"), the run status tool shows the caller's lane only, the
  ticket tools show no other lane's comments and take none from a
  candidate before the selection, and no derived context is given (the
  doors are listed once, in Isolation, below). The run page groups the
  tasks by lane.
- **The comparison.** The ticket page's run card and the run page link
  to **Comparison** (`/dispatch/CND/12?view=compare&run=…`): the base
  commit, the objective, the criteria; one column per candidate (its
  agent and arguments, its approach, its standing commit or, for a lane
  that failed, its last submitted commit marked "failed: not
  selectable", its attempts, its state) and these rows, each said to be
  **measured by Conductor**:
  - **Checks**: one row per check and per manifest verify step, each
    cell the receipt's outcome (passed, failed with its exit, timed
    out), its duration and where it ran, linked to the receipt and its
    log; a row is marked **comparable**, **not comparable, because…**
    ("approach-b's commit changes `conductor.manifest.json`: its
    checkout ran another manifest", "approach-a changed
    `scripts/test.sh`, which this check runs", "approach-c's receipt was
    interrupted") or **comparable only if their test changes are fair**
    ("approach-a changed `internal/cache/cache_test.go`, a test file;
    approach-a changed `Makefile`, which may define what `make test`
    runs"), with each candidate's changed test and build files listed.
  - **Size**: files changed, lines added and removed, files added and
    deleted, against the base, with the list of files.
  - **Dependency changes**: the dependency files each candidate changed
    and, for `go.mod` and `package.json`, each module added, removed or
    moved to another version.
  - **Independence**: "no shared commit and no same patch", or "shares
    2 commits with approach-b", or "1 commit carries the same change as
    one of approach-b's" (a cherry-pick), with what it cannot see said
    beside it ("a read of another lane's folder, or of what an agent
    keeps for itself under its HOME, is not observed").

  Below them, under **Supporting information, not evidence**: each
  candidate's own summary of its result, each agent review's verdict and
  findings, and **Brakeman's comparison draft** (its table of behaviour,
  complexity, dependency changes, checks and unresolved tradeoffs, its
  facts with their evidence, its reading), each marked with who said it.
  No count of agreement is drawn anywhere, and nothing there colours a
  column or a cell of the measured part.
- **Brakeman's draft.** With the template's draft on and Brakeman set
  up, Conductor queues one Brakeman request when every lane is done; the
  comparison shows it queued, running, then briefed, or says why there
  is none ("Brakeman is not set up", "over today's budget"). **Ask
  Brakeman to compare** queues one by hand.
- **The selection.** Once every lane is done, Yardmaster opens the
  selection: a decision record (DR1) whose answers are the candidates
  with a standing result, each naming its exact commit, and **None of
  these**. The comparison shows the selection panel: the person picks a
  candidate (nothing is preselected, whatever the agents or Brakeman
  said) and writes the rationale, which is required; the panel says
  before Send that the answer is final ("Final: the candidates not
  selected cannot be continued; a new comparison tries them again"),
  and Send says **Select approach-b (final)**; the answer is recorded,
  then applied ("Applied: approach-b selected at 4b825dc, by Nate").
  The same record, with its options' commits and its required
  rationale, shows on the decision card: the ticket page's Decisions
  section, Needs your decision (MB0) and `conductor decision respond`.
  The selection names exact commits. Any change to what a lane shows
  before it is applied (a retry of a failed lane, a Check again, an
  Accept anyway) closes it, open or answered, and a new one opens when
  every lane has ended again; the person answers that one. Nothing is
  ever selected by default. Stopping the run, or quitting or updating
  the app while the person is still reading the comparison, leaves the
  selection as it was: after the restart it is there, open, with the
  same commits; the person answers it on the stopped run, and the
  answer is applied when the run is continued, as DR1 keeps every
  decision over a stop. It is never cancelled: **None of these**, with
  its rationale, is the one way to end a comparison with no candidate.
- **What becomes of the others.** The candidates not selected keep their
  branches, worktrees, attempts and receipts; Conductor deletes nothing
  (as for every run: worktrees and branches are removed by hand). They
  are labelled **not selected** on the comparison, the run page and the
  ticket page; their tasks refuse Retry, Check again and Accept anyway
  after the selection, a delivery naming one is refused, and Request
  changes on one is refused. Until a selection is applied (while the
  lanes run, and while the selection is open or answered, the run
  stopped or not) a comparison delivers nothing and repairs nothing
  ("select a candidate first"), and one ended with no candidate never
  does: "no candidate was selected: launch a new comparison".
- **Into review.** **Review the selected candidate** opens RV1's Review
  view on it; a Request changes reopens the selected task at the
  selected commit (in the run, or in the continuation that carries the
  repair), so the repair builds on exactly what was compared; the
  comparison keeps showing the commits the selection named, and the
  repair's later candidates are RV1's Review view's and the ticket's
  candidates list's. A selection of **None of these** leaves the ticket
  to DS2's Revise and a follow-up launch.
- **For whom.** The template may name who the selection is for (a
  responsibility such as the crew's architecture owner, or a person,
  HM1's labels); the selection says "for ana, the architecture owner",
  and the operator answers for them, the receipt keeping both (decision
  D4); with SW1, the paid team routing sends it to them.

## The changes

### `internal/crew`: the compare block, its lanes and its checks (new file `compare.go`; PR-1)

**The model.** `Crew` gains `Compare *Compare` (JSON `compare`, omitted
when nil, so every crew saved before reads, saves and runs byte for byte
as before):

```go
type Compare struct {
	Criteria   []Criterion `json:"criteria"`             // 1 to 12
	Candidates []Candidate `json:"candidates"`           // 2 to 4, in their column order
	Brakeman   bool        `json:"brakeman,omitempty"`   // queue Brakeman's comparison draft when every lane is done
	Selector   string      `json:"selector,omitempty"`   // whom the selection is for: "" (the operator), "role:<r>" or "person:<name>" (PR-8a)
}
type Criterion struct {
	ID   string `json:"id"`   // taskIDPattern
	Text string `json:"text"` // 1 to 500 characters, one line
}
type Candidate struct {
	Task     string `json:"task"`               // a work task of the crew
	Approach string `json:"approach,omitempty"` // at most 2,000 characters
}
```

Bounds, each tested at its edge: `maxCompareCriteria` 12,
`maxCriterionText` 500 characters, `minCandidates` 2 and `maxCandidates`
4 (`maxActiveAttempts`, so every candidate's first attempt runs at
once), `maxApproach` 2,000 characters; the block counts toward the crew
file's bound (`MaxEncoded`) like every other field.

**Validation** (`Compare.validate`, called from `checkTasks` when the
block is set; each refusal `ErrInvalid` with the rule in words):

- the crew has tasks (a compare block on a crew without tasks is
  refused);
- each candidate names a work task of the crew, no task twice; a
  candidate task has no needs, so it starts at the run's base;
- every candidate task has the same instructions and the same checks
  (id, argv and timeout, in order), and at least one check unless the
  crew has a project manifest: comparable verification needs something
  to compare. A crew-form manifest (`Manifest.Verify` in the crew) must
  then have a verify step, checked here; a file-form manifest
  (`{file: true}`, whose verify steps live in
  `conductor.manifest.json` at a commit, and which `Manifest.validate`
  forbids embedding beside `File`) passes this structural check, and its
  steps are required at the launch, when the base is known: `prepare`
  resolves the manifest at the planned base (`resolveAt`) and refuses,
  before any ledger row, a comparison whose candidates have no check and
  whose manifest at the base has no verify step ("nothing to compare:
  the candidates have no checks and conductor.manifest.json at 9f8e7d6
  has no verify step");
- the **lanes**: every task of the crew is a candidate or a review task
  whose one need is a candidate (at most one review per candidate);
  every member does tasks of one lane only, so no session, no
  conversation and no member's prompt carries from one lane to another;
  a crew whose tasks do not split so is refused ("task merge needs
  approach-a and approach-b: a comparison never builds on two
  candidates");
- criteria ids distinct, texts one line, the approach without NUL;
- `selector` empty until PR-8a, which admits HM1's labels.

**The template** (`CompareTemplate(spec CompareSpec) (Crew, error)`):
`CompareSpec{Name, Cwd, Goal, Instructions string; Criteria []Criterion;
Checks []Check; Manifest *Manifest; Candidates []CandidateSpec{AgentID
string; Args []string; Approach string}; Reviews bool; ReviewAgent
string; Brakeman bool; Yolo *bool}` makes the crew: members `cand-a` to
`cand-d` (and `rev-a` to `rev-d` with reviews), tasks `approach-a` to
`approach-d` (work, the shared instructions, the shared checks, no
needs) and `review-a` to `review-d` (review, each needing its candidate,
instructions saying to judge the result against the criteria and the
objective and to change nothing), each member's prompt the role sentence
the template writes ("You are candidate A of 3 …"), isolation
`worktree`, `where` `server`, the block filled, and the result checked
by `ValidateSave` like any crew. The spec's own bounds are the crew's.

**A compare crew needs a ticket.** `prepare(ctx, c, base)` is called by
every launch and by `ContinueRun`, and takes no package; so the rule
keys on the package each caller holds, passed to `prepare` as one more
argument: `LaunchPlanned`'s, or the continuation's, read from the run's
row (`packageOfRow`, as `ContinueRun` reads it today). A crew with a
compare block and no package is refused: "a comparison is launched from
a ticket's approved plan revision"; a continuation of a compare run,
whose row carries its package, passes, so RV1's repair continuation
(#623) and every restart's Continue are untouched. In PR-1, which
ships the block before the launch's statement and count, every launch of
a compare crew is refused, package or none ("comparisons arrive with
their launch statement"), and so is every continuation (none can exist
yet); PR-3 lifts the refusal for `LaunchPlanned` and continuations
together with the count it requires. The approach, the criteria and the
candidates are then frozen three times over: in the plan revision's body
(DS2's `PlanBody.Crew`), in the run's ledger row (`runs.crew`) and in
every continuation, which carries the run's crew unchanged; a later edit
of the saved crew reaches no run.

**Lanes in the engine.** `run.lanes` (task id to lane, made from the
block at launch and on a continuation) and `laneOf(member)`:

- `send` refuses a message whose sender and recipient are in two lanes:
  `ErrLane` ("approach-b's member is in another lane of this
  comparison"), answered `409 lane` by the message route; nothing is
  written.
- `act` (`commands.go`), the one path of every task and run command,
  takes on a compare run only the intents listed for it in
  `compareIntents`: in PR-1 `pause`, `unpause`, `cancel`, `retry` and
  `accept`; any other intent, including one a later row or another
  plan adds (RV1's `recheck` of #620 and `request_changes` of #622), is
  refused before any write (`ErrNotSelected`, "this command is not open
  to a comparison's candidates yet"), until a row of this plan lists it
  with its rule (PR-6a: `recheck`; PR-8a: `request_changes`). So a
  repair made in the run can never reach a comparison's candidate on a
  `main` where RV1's command exists and this plan's rule does not.
- `askContext` asks no provider for an attempt of a compare run: a
  block's `Ref` is optional and may be a URL, and its `Body` is text a
  provider derived from whatever it has seen, another lane's work
  included, so no filter can establish that a block carries nothing of
  another lane. The task file's context section says "No derived
  context: a comparison's candidates and reviews read their task, the
  base and their own work". The free default asks no provider, so the
  free build loses nothing; every other run's attempts ask as today.
- the task file of a candidate's attempt (`taskfile.go`) gains a
  `compare` part: in `task.json`, `compare: {candidate, of, criteria:
  [{id, text}], approach}`, and in `task.md` a section "A comparison":
  "You are approach-b, one of 3 candidates for this objective, each
  starting at 9f8e7d6 and working alone. Do not read, fetch or merge
  another candidate's branch or worktree; nothing of theirs is an input
  of yours. Your approach: … The criteria your result is compared on:
  …". A review task's file names its own candidate only. `fitTaskFile`
  never cuts the part: it is at most 8,000 characters (12 criteria of
  500 and an approach of 2,000), counted in `taskTextBytes`' never-cut
  part beside the goal, the role and the instructions, so `maxTaskText`
  (40 KiB) still bounds them together, and a crew whose goal, role,
  instructions and comparison part would pass it is refused at save.

The worktree is change isolation, not an operating-system sandbox (the
Program, section 1.5): an agent may still run `git log --all` in the
shared object store or open a sibling folder. So isolation is enforced
on every door Conductor owns, listed once below, and what it cannot
prevent it partly observes: shared commits and equivalent patches
(PR-2) are shown as independence findings, and a read of a sibling
folder is said not to be observed. The touched index is not used: it
holds what an agent's own hooks report (`reportEvent` into
`noteTouched`), which an agent can leave out or make up, so it is no
measure of independence.

### Isolation: every door a candidate has, listed once

A **door** is anything through which an agent session of a compare
run's lane (a candidate's or its reviewer's) could read or receive
another lane's work, or hand its own to another lane. Brakeman, asked
for a comparison, reads every lane and is not a candidate: its reads are
door 11, and nothing it writes reaches a lane. Each door, what it
returns to a session of a comparison's lane, the row that builds it and
its test:

| # | Door | What a lane's session gets in a comparison | Row | Test |
|---|---|---|---|---|
| 1 | Its worktree (`<cwd>/.conductor/worktrees/<run>/<task>.<n>`, branch `crew/<run>/<task>.<n>`) and `GET /api/sessions/{id}/files` | its own tree at the base (a review's at its candidate), read with the agent's own tools; the files route takes no agent token (`handleGetFile` calls `authenticate`, which resolves none), so a lane's token is refused `401` there, as today; a sibling lane's worktree is a folder the worktree does not fence off (door 14) | PR-1; the route's `401` tested in PR-1b | `TestEveryCandidateStartsAtTheBase`, `TestTheSessionReadsTakeNoAgentToken` |
| 2 | Its task file (`task.md`, `task.json`, `task-more.json`) and `task_context` (`GET /api/sessions/{id}/task`) | its own task; the comparison part names its own approach, the criteria and the count of candidates, never another lane's task, member, branch, path or approach | PR-1 | `TestATaskFileNamesNoOtherLane` |
| 3 | Inputs and handoffs (needs' results) | within its lane only: a candidate has no needs, a review needs its own candidate (validation) | PR-1 | `TestCompareValidates` |
| 4 | Derived context (`ContextProvider` blocks) | none: no provider is asked for a comparison's attempts | PR-1 | `TestAComparisonTakesNoDerivedContext` |
| 5 | Messages (`message_send`, `inbox_read`, `inbox_ack`) | within its lane; a message across lanes refused `409 lane`, nothing written, so no inbox holds one | PR-1 | `TestMessagesStayInTheirLane` |
| 6 | `run_status` (`GET /api/sessions/{id}/run`, `handleSelfRun`) | a lane view: the run's id, goal, state and outcome, and its own lane's members and tasks with their attempts; no other lane's member, prompt, task, attempt, worktree, branch, commit, summary, issues, findings or diff; no run log (its lines name every lane's members and messages); the compare block with its own candidate's entry only | PR-1b | `TestRunStatusShowsOneLane` |
| 7 | `ticket_read` (`GET /api/sessions/{id}/ticket`) | the ticket view without the comments any other lane's member of this run made (actor `agent:<member>`), and without links naming another lane's branch or candidate; a person's comments and earlier runs' stay | PR-1b | `TestTicketReadHidesOtherLanes` |
| 8 | `ticket_comment` (`POST /api/sessions/{id}/ticket/comments`) | refused `409 lane` ("a candidate of a comparison does not comment on its ticket before the selection: say it in your result's summary") for every lane until a selection is applied (PR-1b: always, since no selection exists yet); from PR-6b, taken from the selected lane's sessions after the selection, still refused to the others | PR-1b, PR-6b | `TestTicketCommentIsRefusedToALane` |
| 9 | DR1's decision tools (#637: `decision_request`, `decision_list`, `decision_read`, `decision_withdraw`) | refused, always: listed `refused` in `compareDoors`, so `decisionSession`'s `agentTokenOK` takes no lane's token for them; a candidate asks the people watching with `ask` (door 13), which opens no record, so a comparison's run holds no agent's decision and its selection never shares the run's decision allowance with one (PR-6a) | PR-1b | `TestEveryAgentDoorIsListed` |
| 10 | Evidence, receipts and logs (`GET /api/runs/{run}/candidates`, the check rows and their logs, `GET /api/runs/{run}/comparison`) | refused: these are admin routes (and Brakeman's shared reads); a session's token is refused `401` | PR-3 | `TestALaneReadsNoEvidenceRoute` |
| 11 | Brakeman's comparison (its request, its turn, its brief) | nothing: Brakeman's tools (`brakeman_request`, `brief_submit`, `observe`, `act`) answer only a Brakeman turn's token, so a lane's token is refused them; the brief is read by the admin and by Brakeman, and reaches no task file, context, message, ticket comment or decision of a lane | PR-5 | `TestTheDraftReachesNoLane` |
| 12 | Self-service that forms or grows a crew (`form_crew`, `add_member`) | refused to a managed run's member already (`needNoRun`, `needUnmanaged`) | PR-1b (test only) | `TestEveryAgentDoorIsListed` |
| 13 | Its own session's tools (`report`, `set_state`, `ask`, `link`, `open_url`, `guide`) and the session read (`GET /api/sessions/{id}`) | the tools: its own session only, what it reports going to the people watching, never to another session; the session read takes no agent token (`handleGetSession` calls `authenticate`), so a lane's token is refused `401` there, as today | PR-1b (test only) | `TestEveryAgentDoorIsListed`, `TestTheSessionReadsTakeNoAgentToken` |
| 14 | What Conductor does not own: the shared object store (`git log --all`), sibling folders, the network, and an agent's own state under its HOME (the memory, history and session stores an agent CLI keeps for itself, which every session of one HOME can read and which an agent may key by repository rather than by worktree) | partly observed, not prevented: shared commits and equivalent patches (PR-2), shown as independence findings; a read of a sibling folder, of the network or of an agent's own stored state is not observed, and the page says so | PR-2 | `TestSharedCommitsAreFlagged`, `TestACherryPickIsFlagged` |
| 15 | RV1's review (#627, #631) | no agent tool: a person's Request changes reaches the selected lane's next attempt through its task file (door 2); for every other lane, and for every lane before an applied selection, the engine refuses it on any `main`: `request_changes` is not in `compareIntents` until PR-8a (PR-1), and a comparison's continuation repairs no lane but the selected one (PR-3) | PR-1, PR-3; the route's words in PR-6b or PR-8a | `TestACompareRunTakesOnlyItsListedCommands`, `TestAComparisonsContinuationRepairsOnlyTheSelected`, `TestRequestChangesOnAnUnselectedCandidateIsRefused` |
| 16 | Any door a later row adds | closed until listed: `compareDoors` in `internal/api` (new, `compare_doors.go`) holds one entry per route pattern an agent's token may call, each with its rule (`own`, `lane`, `refused`, or a function). Every route that takes an agent's token takes it through one predicate, `agentTokenOK` (`attention.go`; called by `attentionPrincipal`, `taskSession`, `brakeman_session.go`, `brakeman_observe.go`, and #637's `decisionSession`); `authenticate` resolves no agent token. PR-1b gives `agentTokenOK` the request, and for a session of a compare lane it answers false for a pattern (`r.Pattern`) with no entry, or listed `refused`: the route answers as it does for a token it does not take (its `401`), so a route added later, by any plan, is closed to a comparison's lanes until its rule is written | PR-1b | `TestEveryAgentDoorIsListed`, `TestEveryAgentTokenPathIsGuarded`, `TestAnUnlistedDoorIsClosedToALane` |

`TestEveryAgentDoorIsListed` walks the guide's operations
(`guideOperations`) of every scope an agent's token may call (`session`,
`task`, `ticket`, `observe`, `act`) and fails for any pattern without an
entry in `compareDoors`; DR1's four decision tools are listed from PR-1b
as `refused`, so #637 lands with them already closed to a comparison's
lanes and adds nothing for this plan. The files route and the session
read are not agent-token routes: `TestTheSessionReadsTakeNoAgentToken`
calls each with a candidate's token and with an ordinary run's member's
and finds each refused `401`, as `authenticate` refuses every agent
token today, so a later change that lets an agent's token in there
fails it and must give the routes their entries.
`TestEveryAgentTokenPathIsGuarded` parses `internal/api`'s files
(`go/parser`, the test files left out) and fails for any call of a
session's `AgentTokenOK` outside `agentTokenOK`, so a route cannot take
an agent's token past the guard. `TestAnUnlistedDoorIsClosedToALane`
registers in the test's server three routes the list does not name,
one authenticating through `taskSession`, one through
`attentionPrincipal`, one calling `agentTokenOK` directly (PR-6b adds
one through `decisionSession`), and calls each with a candidate's token
(refused `401`) and an ordinary run's member's token (served).

### `internal/crew`: the comparison measured, and the lanes settled (new files `comparison.go`, `comparedeps.go`; PR-2)

**What it measures.** `Engine.Comparison(ctx, runID) (Comparison,
error)` takes any run of a comparison's chain (the launched run or a
continuation of it), walks the chain to its launched run (the first
whose crew has the block and which continues no run; at most
`maxCompareChain`, 256 runs, PR-3's bound), and reads the
crew as launched, its base, and for each lane the candidate it shows:
the task's standing result along the chain (a continuation's imported
result keeps its source run's reference, `candidate:<run>/<task>/<n>`,
as DS2's candidates do), or, for a lane with none, its last submitted
candidate (DS2's `internal/api/candidates.go` lists a failed task's
submitted commits with their receipts; the engine reads the same
attempt rows), shown as evidence and marked not selectable, so a failed
commit's diff and receipts explain why the approach failed. Once a
selection is applied (PR-6a), each lane shows the commit the
selection's option named, never a later one. The ledger is read first,
then git:

- **One revision.** The chain's runs, their task and attempt rows, the
  check rows on each shown candidate and, from PR-6b, the chain's
  selection record are read under the rule the candidates route keeps
  (`internal/api/candidates.go`): each run's revision (`Pointer`) read
  before and after the reads, all read again while any moved, at most
  `candidateReadTries` times, then `ErrRunChanging` (the route's `503
  run_changing`, Retry-After 1), never rows of two revisions. Every
  ledger write that changes them, a continuation's included (its
  `continued_by`) and DR1's (each a run transition), moves a run's
  revision, so columns, rows and selection are of one moment.
- **Then git**, on the commits those rows named: commits do not change,
  so nothing read from git can disagree with the rows.

It answers:

```go
type Comparison struct {
	Run, Base, Objective string // Run: the chain's launched run
	Criteria   []Criterion
	Candidates []ComparedCandidate // in the block's order
	Checks     []ComparedCheck     // one row per check id and phase
	Settled    bool                // every lane ended (compare.settled written in the chain)
}
type ComparedCandidate struct {
	Task, Member, Agent string; Args []string; Approach string
	State string                 // running, ready (a standing result), failed, cancelled
	Run string; Attempt int; Commit, Start string // the candidate shown: its run (a continuation's, or its source's), attempt, commit and start
	Selectable bool              // a standing result; false for a failed lane's last submitted commit
	Attempts int                  // attempts made of the task along the chain
	AcceptedAnyway bool
	Size   *DiffSize              // files, added, removed, filesAdded, filesDeleted, Files []FileStat (at most 100, Total)
	Deps   *DepChanges            // Files []string (at most 20); Modules []ModuleChange{File, Name, From, To} (at most 50); Total
	VerifyFiles *FileList         // the test and build files it changed (at most 20, Total, each with its kind: test | build)
	Shared []SharedCommits        // {With string; Count int; Commits []string (at most 10)}
	Equivalent []SharedCommits    // commits whose patch equals one of another lane's (a cherry-pick), same shape
	ManifestChanged bool          // file form: conductor.manifest.json differs from the base's blob
	Unavailable string            // words, when git could not be read; the rest of the reply stands
}
type ComparedCheck struct {
	Phase, ID string; Argv []string; ArgvCut bool; ArgvDigest string; TimeoutSeconds int
	Cells []CheckCell  // one per candidate shown: {Task, Attempt, K, State, Exit, Signal, TimedOut, DurationMS, Where, Manifest, Stable, Receipt, LogSHA256}; State not_run when it has no receipt
	Verdict string     // comparable | unknown | not_comparable
	Why []string       // words, at most 8, when not comparable or unknown
}
```

- `Size` is one `git diff --no-renames --raw --numstat -z <base>
  <commit> --` in the run's repository (argv, the C locale, through
  `gitcli`'s bounded runner, below): the raw part gives each path's
  status (`A` added, `D` deleted, `M` or `T` changed), which the line
  counts cannot (a new file of 5 lines and 5 lines appended to a file
  both read `5 0`; an empty file added and one deleted both `0 0`), and
  the numstat part its lines; renames are off, whatever `diff.renames`
  says, so a renamed file counts as one deleted and one added; a binary
  file counts its file and no lines.
- `Deps` reads the paths `Size` found against one table of dependency
  files (`dependencyFiles`: `go.mod`, `go.sum`, `package.json`,
  `package-lock.json`, `npm-shrinkwrap.json`, `yarn.lock`,
  `pnpm-lock.yaml`, `requirements*.txt`, `pyproject.toml`,
  `poetry.lock`, `Pipfile`, `Pipfile.lock`, `Cargo.toml`, `Cargo.lock`,
  `Gemfile`, `Gemfile.lock`, `pom.xml`, `build.gradle`,
  `build.gradle.kts`, `composer.json`, `composer.lock`, at any depth),
  and for each changed `go.mod` (its `require` lines, single and in
  blocks) and `package.json` (its `dependencies`, `devDependencies`,
  `peerDependencies` and `optionalDependencies`, with `encoding/json`)
  reads the file at the base and at the commit (its size asked first
  with `git cat-file -s`, then `git cat-file blob` through the bounded
  runner, at most 1 MiB each, else listed without modules) and lists each module
  added, removed or at another version. Anything else is listed as a
  changed file only: the page never claims a dependency change it did
  not parse.
- `Shared` is, for each other lane with a candidate, the commits
  reachable from both candidates and not from the base (`git rev-list
  <base>..<a>` against `<base>..<b>`, each at most 10,000 commits, past
  that "more than 10,000" and no list): a candidate that merged another
  lane's commit, or built on it, shares its object, and the page says
  so.
- `Equivalent` catches what ancestry cannot: a cherry-pick makes a new
  commit with another parent and id, so no object is shared. For each
  pair of lanes whose candidates each have at most 200 commits in
  `<base>..<commit>` (counted first with `git rev-list --count`; past
  that "not checked: more than 200 commits"), one `git cherry <b> <a>
  <base>`, git's own comparison of patch ids, lists a's commits since
  the base with `-` before each whose change one of b's carries: one
  argv process, nothing piped by the server, at most 200 lines. Such a
  commit is listed with that lane ("1 commit carries the same change as
  one of approach-b's"). A change copied by hand in other words, or squashed
  with other edits, has another patch id and is not caught; the page
  says "same patches: checked; a change rewritten by hand is not
  detected".
- `VerifyFiles` lists, from the full numstat (not the cut list), each
  file the candidate changed that is a **test file** (by name: `*_test.go`,
  `test_*.py`, `*_test.py`, `*.test.*`, `*.spec.*`, `conftest.py`, or
  any path with a `test`, `tests`, `__tests__` or `testdata` segment) or
  a **build file** that can define what a check's command runs
  (`Makefile`, `GNUmakefile`, `*.mk`, `justfile`, `Taskfile.yml`,
  `Taskfile.yaml`, `tox.ini`, `noxfile.py`, `pytest.ini`, `setup.cfg`,
  `pyproject.toml`, `jest.config.*`, `vitest.config.*`,
  `playwright.config.*`, `go.work`, and `package.json` when its
  `scripts` map, read at the base and at the commit with
  `encoding/json`, differs; a `package.json` whose dependencies alone
  changed is not a build file here), at any depth.
- `ManifestChanged` compares the file-form manifest's blob at the commit
  with the base's (`git rev-parse <commit>:<path>`), the crew form never
  changing.
- `Checks` has one row per **expected** check, then one per other id a
  receipt shows: expected are the crew's task checks as launched, in
  order, and the verify steps of the manifest at the base (the crew
  form's, or `conductor.manifest.json` read at the base with `git
  cat-file`, as `prepare` read it); a step a candidate's own manifest
  added has a row of its own from its receipt. Each cell is the shown
  attempt's row of that phase and id (the latest `K`; a Check again of
  RV1's #620 is a new attempt at the same commit, so it is simply the
  one shown), or `not_run` when there is none: `checkTree` stops at a
  tree's first failure, so a check after a failed lint has no receipt,
  and a candidate that removed a verify step has none for it; the cell
  says which it was ("not run: an earlier check failed", "not run: its
  manifest has no such step"). A row is never dropped for having no
  receipt.

**Comparability**, the rule the plan's acceptance names ("checked
alike"), has three verdicts, each with its words in `Why`:

- **not comparable** when any of these fails, for every candidate shown:
  its cell is not `not_run`, finished (passed or failed, not interrupted,
  cancelled or still running), is `Stable`, checked the candidate's own
  commit (`Subject`); every cell has the same argv (compared whole, by
  `ArgvDigest`, the SHA-256 of the argv as recorded) and timeout, the
  same `Where`, and the same manifest identity (the row's `Manifest` for
  a verify step; for a task's check, the tree's `manifest.resolved`
  identity, so a candidate whose checkout ran another manifest's setup
  is not comparable either); and no candidate changed a file the
  check's argv names (an argv entry that is a path relative to the
  crew's directory naming a file at the base, looked up in one `git
  ls-tree` per row; at most 32 entries, the argv bound). A lane with no
  submitted commit at all makes every row "not comparable: approach-c
  has no result".
- **unknown** ("comparable only if their test changes are fair") when
  all of that holds but a candidate's `VerifyFiles` is not empty: most
  checks name no file (`go test ./...`, `make test`, `npm test`,
  `pytest`), and a candidate that edited a test, a `Makefile` target or
  `package.json`'s `scripts` may have changed what the same argv runs.
  Each such file is listed with its candidate ("approach-a changed
  `internal/cache/bench_test.go`, a test file; approach-b changed
  none"). Conductor cannot tell a test added with the change from a
  test weakened, so it says which, and the person reads the diffs.
- **comparable** when all of it holds and no candidate changed a test
  or build file.

Durations are shown, never compared: checks run one at a time across
the server (`checkSlots`), but a loaded machine still varies.

**Bounds**, each a part of the reply cut as encoded (every string
measured after `encoding/json`'s escaping, so a text of `<` counts six
bytes a character), with the sum of the parts at their bounds as the
reply's bound:

| Part | Bound | At most, encoded |
|---|---|---|
| criteria and objective | 12 criteria of 500 characters; the crew's goal as the crew bounds it | 48 KiB |
| a candidate's header | task, member, agent, args (each cut at 200 bytes, at most 32), approach (2,000 characters) | 20 KiB |
| a candidate's files | 100 `FileStat`, each path cut at 300 bytes, and `Total` | 35 KiB |
| a candidate's dependencies | 20 files and 50 modules (name cut at 300 bytes, versions at 64) | 30 KiB |
| a candidate's test and build files | 20 entries, each path cut at 300 bytes | 7 KiB |
| a candidate's shared and equivalent | 3 lanes × (10 shared + 10 equivalent commits, each 40 hex digits) and each lane's task id | 4 KiB |
| 4 candidates | the five lines above | 384 KiB |
| check rows | 48 rows (8 task checks, 8 verify steps of the base's manifest, and up to 8 more from each of 4 candidates' manifests, whose ids may all differ from the base's; the expected rows among them whether or not any receipt exists), each with its argv preview cut at an entry's end to 1 KiB (`ArgvCut`; the comparison itself uses the whole argv's digest), 8 reasons of 300 bytes and 4 cells of 800 bytes (with the receipt's ref and its log's SHA-256) | 320 KiB |
| supporting (PR-5) | 4 claims (summary cut at 4 KiB), 4 reviews (verdict and at most 20 findings' titles cut at 300 bytes), Brakeman's state | 48 KiB |
| selection (PR-6b) | 5 options' labels and tradeoffs, the rationale, who and when | 16 KiB |
| envelope | the launched run, the base, flags (no history: no list of the chain's runs and no later candidates, which are the Review view's and DS2's candidates list's, each paged there) | 8 KiB |

So the reply is at most 824 KiB, and `MaxComparison` is 1 MiB; no
part of it grows with the chain's length or the number of continuations
and repairs;
`TestTheFullestComparisonFitsItsBound` builds one with every part at its
bound, every string of `<`, and four manifests whose verify ids all
differ. A receipt cut from a preview is reached by its link, whole.
Every git call runs under the request's context with a 20 s budget for
the whole measure, through a bounded runner PR-2 adds to `internal/gitcli`
(the one package that may start git, `ext/local`'s boundary test), since
`Run` and `RunEnv` take no input and keep all of git's output in memory
before a caller can measure it: `RunBounded(ctx, dir, limit, args...)`
keeps at most `limit` bytes of standard output and 4 KiB of standard
error, and past the limit ends the process and answers
`ErrOutputLimit`; `Run` and `RunEnv` are unchanged. Each call names its
limit: the diff 4 MiB (past it "the diff passes 4 MiB: not measured"),
`rev-list` with `--max-count=10001`, `cherry` 200 lines, `ls-tree` one
entry, and a dependency file's blob 1 MiB, its size asked first. A
failed, late or over-limit call fills `Unavailable` for that candidate
with git's words and leaves the other candidates and the checks table
whole. Results are cached in memory by run, base and the
candidates' commits (at most 32 entries, least recently used out),
commits being immutable; the check rows are read from the ledger each
time.

**Settling.** A lane has **ended** when its candidate task is terminal
(finished, failed or cancelled) and its review task is terminal or
doomed: a review blocked behind a candidate that failed or was
cancelled is never changed to failed or cancelled (`run.doomed`,
`internal/crew/task.go`, which the run's outcome rule already counts as
finished), so the predicate reuses `doomed` rather than waiting for a
state that never comes. A review's `needs_changes` reopening its
candidate is not an end. A lane's **shown candidate** is its standing
result (task, run, attempt, commit; an imported one keeps its source
run), and the **settled set** is the shown candidates of the lanes that
have one, in the block's order.

The engine keeps one thing for it, `r.cmp.settled`: the set last
written in `compare.settled`, or none while a lane has not ended since.
It changes in one place, **the compare step** (`compareStep`, new),
which `commit` calls for a compare run after every change's mutation and
before the drain (and, once DR1's #636 is in, before its `endedWork`), so
it sees each change whole,
whatever command, continuation or engine step made it, and reads the
lanes as they are, not which rows the change marked:

1. while a lane has not ended, `settled` is none;
2. when every lane has ended, no selection is applied in the chain, and
   the settled set is not `settled` (none, or another set), the step
   writes the run event `compare.settled` with the set (`[{task, run,
   attempt, commit}]`, at most 4 entries, about 500 bytes) and sets
   `settled` to it.

The step runs in every change of a compare run, a stop's and a
continuation's first transaction included, with no exception for a run
being stopped: a lane that ends while its run is being stopped settles
the lanes in its own change, as any lane's end does. A stop's own
change ends no lane (an attempt it stops leaves its task ready,
`closeAttempt`), so it writes nothing. A continuation's first
transaction starts with `settled` set to the chain's newest
`compare.settled` (read before that transaction with the chain's other
reads, as `Engine.Comparison` reads it for `Settled`), and the step then
runs on the lanes it finds, as after a lane's change: a continuation
whose lanes are imported as the chain settled them writes none, and one
that finds every lane ended with no settling of them recorded writes it
in that first transaction. So `compare.settled` is written once for
each settling of the lanes, in the change that ends the last one (or in
the first change that finds them ended unsettled): again after a lane
runs again (a retry of a failed lane, even one that fails again) or its
shown candidate changes (an Accept anyway), never twice with no such
change between, never by a stop's own change, and never once a
selection is applied in the chain; no ordering of a stop and a lane's
end leaves ended lanes unsettled. Each is a ledger event of its run, in
the run's history and its export. PR-6a adds the selection to the same
step.

### `internal/api` and `internal/cli`: the template, the comparison, the launch's statement (PR-3)

**Routes** (`compare.go`, new; registered beside the crew and run
routes, with their guide rows in `guide.go`):

| Route | Who | What | Bounds |
|---|---|---|---|
| `POST /api/crews/templates/compare` | `launching` (the admin) | a `CompareSpec` (`DisallowUnknownFields`) made a saved crew through `CompareTemplate` and the store's create; answers the crew as `POST /api/crews` does | body at most 64 KiB; `400 invalid_crew` with the rule's words |
| `GET /api/runs/{run}/comparison` | `readable` (the admin; Brakeman's principal by its shared read, PR-5); an agent's session token refused `401` | `Engine.Comparison` of the chain `{run}` belongs to (any of its runs), with the independence the API adds (below) and, from PR-5, the supporting part, from PR-6b the selection | reply at most `MaxComparison` (1 MiB, the sum above); `404` for a run that is not of a comparison |

**The comparison does not read the touched index.** It holds what an
agent's hooks report, not what Conductor saw (Isolation, door 14), and
Brakeman's grant keeps it under a class of its own (`ClassTouched`), so
the reply carries nothing from it; the page's independence row says
what is not observed.

**The launch says what it multiplies, not a total it cannot keep.** On
`main` every attempt is an agent session of its own
(`AttemptState.SessionID`) and a task has three counted attempts
(`maxAttemptsPerTask`), but the engine gives more than that without a
person's Retry: a review's `needs_changes` reopens its candidate and
refunds the review's attempt (`withdrawFrom`, `t.extra++`), an attempt
that could not be made usable is not counted (`steps.go`, `t.extra++`),
and a Retry raises the bound by one; and the checks a candidate runs are
those of the manifest at its own commit (`resolveAt`), which it may
change. So no total of sessions or check runs is a ceiling the engine
keeps, and none is stated. What is stated is exact: DS2's plan revision
reads (`GET /api/dispatch/plans/{id}`, `GET
/api/dispatch/tickets/{key}/plans`) gain, for a revision whose crew has
a compare block, `compare: {candidates, reviews, checksAtBase,
brakemanTurn}`, computed from the body, never stored: `candidates` the
lanes, each starting one candidate session once the plan's decisions
and the project's prerequisites let the run's work start; `reviews` the
review tasks, each starting a session only after its candidate succeeds
(none for a candidate that fails or is cancelled, whose review the
engine leaves blocked behind it, `doomed`), so the words say "up to";
`checksAtBase` the task's checks and the manifest's
verify steps at the base (the crew form's, or the file form's at the
revision's base, read when the revision is read); `brakemanTurn`
whether the template asks a draft. The words say the multiplier: the
comparison is `candidates` runs of one lane's crew, each worked as a
task of its own crew is, with the refunds and Retries named, and a
candidate that changes the manifest running its own checks. The launch
route (`POST /api/dispatch/tickets/{key}/launch`, `ticketLaunchBody`)
gains `candidates int`: required and equal to the revision's count for a
compare revision (`409 confirm_candidates`, "this revision starts 3
candidates: send candidates 3 to launch them"), refused for any other
revision (`400 invalid`). It enters the operation's fingerprint through
DS2's store: `dispatch.LaunchArgs` gains `Candidates int` (JSON
`candidates`, omitted when 0, so every fingerprint made before is
unchanged) and `Store.LaunchAdmitted` takes it with the key, plan and
label it builds the arguments from, so a resend with the same body is
the same launch and one with another count is `409 op_conflict`.

**Launches that are not a person's.** Brakeman's granted launch act
(`internal/api/brakeman_act.go`) decodes the same body but carries
only the op id, the expected revision, the plan and the label into
`ext.ApprovedLaunch`, which has no count, and `ext.Host`'s
`LaunchApproved` is the one door from any provider (Brakeman's desk
among them) into a ticket's launch. A comparison is launched by a
person who has read its statement, so `LaunchApproved` refuses a
revision whose crew has a compare block, before any write
(`ext.ErrRefused`, "a comparison is launched by a person, who reads what
it starts: open the ticket's revision"), and the act's receipt carries
those words; `ext.ApprovedLaunch` gains no field. The HTTP route is the
only launch of a comparison, and its check sits in its handler, where
the person's count arrives.

**Deliveries before the selection exists.** From PR-3, when a
comparison can first be launched, DS2's delivery link (`POST
/api/dispatch/tickets/{key}/links`, kind delivery) naming a candidate
of a compare run's chain is refused before any write (`409
not_selected`, "a comparison's candidate is delivered once it is
selected"); PR-6b narrows the refusal to every candidate but the one
an applied selection names.

**A comparison's continuation repairs only the selected lane.** From
PR-3, `startContinued` (`continue.go`), once it has decided each task's
import and before its first transaction, refuses a continuation of a
compare chain (`ErrNotSelected`, "a comparison's candidate is repaired
once it is selected", no run made) in which a lane task that succeeded
in its source is not imported, or a lane task starts with a repair,
unless the chain's applied selection (PR-6a; none before it) selects
that lane. A plain Continue imports every succeeded task and carries no
repair, so it is untouched; RV1's repair continuation (#623,
`ContinueRunWith`, which leaves the repaired task unimported and gives
it the repair in `startContinued`) meets the rule whichever of the two
merges first. With `compareIntents` (PR-1) refusing `request_changes`
in a run, no `main` repairs a comparison's candidate before a selection,
whatever order RV1's rows and this plan's land in.

**A comparison's chain is bounded.** DR1's continuation reads the
decisions of at most 256 runs back (`maxChain`, `readDecisionHistory`,
#636), and an applied record stays in the run it was applied in
(`MoveDecisions` moves open and answered ones only), so a selection
more than 256 runs back would not be read again and the lanes it left
unselected would lose their refusals. The chain is bounded instead of
adding a second lookup: from PR-3, `startContinued` refuses, before its
first transaction and with no run made, a continuation of a compare
chain that already holds `maxCompareChain` runs (256, DR1's number; "this
comparison has been continued 255 times: launch a new comparison"), and
`Engine.Comparison`'s walk stops at the same bound. So every run of a
comparison's chain, the one its selection was applied in included, is
inside the window DR1's reload reads, and PR-6a's test holds the two
numbers equal.

**A barrier against an older binary.** `ContinueRun` decodes
`runs.crew` with `json.Unmarshal`, which drops an unknown field, so a
binary from before this item would continue a comparison as an
ordinary crew, without its lanes, its refusals or its selection; and a
binary's ledger opens read-only only when the ledger's version passes
its own. So PR-3, the row whose binary first writes a compare run, moves
the ledger's version up by one with a migration that changes no table
and rewrites no row, made only as a barrier (RV1's PR-2b does the same
for its repair request); with DR1 at 8 and RV1's barrier beside it, the
PR says which number it took. An older binary then opens such a ledger
read-only, so it launches, continues and settles nothing. The crew file
(`crews/<id>.json`) and DS2's revision body need no barrier: the crew
store reads its files with `store.DecodeStrict` (unknown fields
refused), and a revision is launched only through the ledger.

**The CLI.** `conductor crews compare SPEC.json` (or `-` for stdin)
makes a compare crew and prints its id and tasks; `conductor dispatch
comparison KEY-n [--run RUN] [--json]` prints the comparison (`--run`
any run of its chain; the ticket's newest comparison when it is left
out): the base, the criteria, a column per candidate and the rows, with
each row's verdict and its reasons; `conductor dispatch launch KEY-n
--plan rN --candidates 3` sends the count, and without it on a compare
revision prints the statement and exits 2. Usage and completion text in
`internal/cli`.

### `internal/brakeman` and `internal/api`: Brakeman's comparison draft (PR-5)

Brakeman's plan lets a later item add a request kind, a brief rule and
a shared read in its tables; this row does so in Brakeman's code, after
the rows that made those tables: the request kinds and the brief kinds
(#520, `request.go`, `kinds.go`), the shared reads (#525,
`brakemanReads`), DS2's candidates as evidence (#531, the `candidate:`
ref) and the act (#534). Brakeman's plan is followed as it stands.

- **The request kind** `compare` (`request.go`): one subject, a `run:`
  ref of a compare run; no note required. It is in `Kinds`, so a person
  may ask it from the Brakeman page or the comparison.
- **The brief rule** (`kinds.go`, checked at `brief_submit` for a
  request of kind compare, `CheckCompareBrief`): the brief holds exactly
  one `comparison/1` whose `columns` are the candidates' task ids in the
  block's order (a candidate without a standing result included, its
  cells saying so) and whose rows include, by these labels, `Behaviour`,
  `Complexity`, `Dependency changes`, `Checks` and `Unresolved
  tradeoffs` (other rows allowed, 20 at most, the kind's bound); its
  evidence and the brief's facts cite the candidates and their receipts
  (`candidate:`, `check:` refs). A `decision/1` item is refused in a
  compare brief ("the selection is the person's, on the comparison"),
  and so is a `proposal/1` whose op is a task command on a lane;
  Brakeman's other kinds stand. The guide's briefs section, made from
  the kind table, says the rule.
- **The read** `GET /api/runs/{run}/comparison` joins `brakemanReads`
  under the `runs` class (`brakeman_observe.go`), with its reply bound
  (`MaxComparison`, 1 MiB, under a turn's 4 MiB `maxTurnReply`), so
  Brakeman reads exactly what the page measures; a turn whose grant
  lacks the class is refused the read as any shared read is. Every part
  of the reply is runs data (the crew as launched, the ledger's task,
  attempt and check rows, git of the run's repository): it carries
  nothing of the touched class (`ClassTouched`), whose index it does not
  read, so the runs class alone is the right grant. A run's footprint
  does not say which candidates and receipts a turn saw, and Brakeman
  resolves a citation's `Seen` by its exact reference
  (`brakeman_refs.go`), so the read has a filter of its own,
  `filterComparison`, as the candidates route has `filterCandidates`:
  for each column it notes the candidate's `candidate:<run>/<task>/<n>`
  ref (the run its attempt ran in, an imported one's source run) at the
  state and commit `candidateRefState` reads, and for each check cell
  with a receipt its `check:<run>/<task>/<n>/<id>/<k>` ref with
  `noteHashed` (the receipt's state, the commit it checked, its log's
  SHA-256), and puts each `ref` into the reply beside what it names. So
  a brief that cites them after reading only the comparison cites what
  was observed, with the observed state as its baseline. The reply
  carries what the filter reads (each column's `Start`, each cell's
  `LogSHA256`). From PR-6b, Brakeman's read leaves out the `selection`
  part: the person's answer is never Brakeman's input.
- **Queued once.** On `compare.settled` (the engine's run change,
  `OnRunChange`), when the block's `brakeman` is set, the API queues one
  Brakeman request of kind `compare` about the run, after the event's
  commit and never inside it. Its op id must match Brakeman's
  `opIDPattern` (`^[A-Za-z0-9._~-]{1,32}$`, `internal/brakeman/refs.go`,
  checked when the request is made), and a run id alone may pass that
  (a crew id of up to 64 characters, a dash and 8 hex digits), so the op
  id is `cmp-` and the first 24 hex digits of the SHA-256 of the run id,
  a NUL and the event's sequence in decimal: 28 characters, the same for
  the same event, so a restart or a second wake makes no second
  request. `by` is the launch's actor. `brakeman.Request` has no `via`
  field and gains none: the request is told apart by its kind and its
  `run:` subject.
- **Not queued, and lost.** With Brakeman not set up, its runs class
  off, or its day's budget spent, nothing is queued (or the request goes
  `over_budget`, as any request does), and the comparison says why when
  it is read ("Brakeman is not set up", "over today's budget"). A crash
  after `compare.settled` commits and before Brakeman's `Create` loses
  the request: nothing re-queues it. The comparison then shows no draft
  and offers **Ask Brakeman to compare**, which queues one by hand (the
  same kind and brief rule). The draft is supporting information the
  selection never waits for, so a lost one costs a click, and no
  start-time reconciliation is kept for it.
- **Shown as supporting.** The comparison reply gains `supporting:
  {claims: [{task, attempt, summary}], reviews: [{task, attempt,
  verdict, findings}], brakeman: {request, state, why, brief}}`: the
  candidates' own result summaries and their review tasks' verdicts,
  read from the ledger, and the newest compare request's state with its
  brief's id. These are never read into `candidates` or `checks`, and
  the reply has no field that counts agreement.

### `internal/crew/ledger` and `internal/crew`: the selection, a DR1 record (PR-6a, after DR1's #636 on #634 and #635)

**Which record.** The selection is one row of DR1's `decisions` table
(`docs/plans/decisions.md`, The changes, the ledger) with its
`decision_responses` and `decision_history`: `type` `approval`, `origin`
`compare` (a third origin beside `agent` and `plan`, opened by
Yardmaster itself), no asking task, attempt or session, no gate and no
hold; the run's ticket and plan revision as DR1 fills them from the run;
`policy` `local/1`. Its request:

- `question`: "Which approach does CND-12 continue with?";
- `options`: one per lane with a selectable candidate, in the block's
  order, its label the task and short commit ("approach-b at 4b825dc"),
  its tradeoffs the measured facts in words ("checks 2/2 passed,
  comparable; 12 files, +340 −90; no dependency change; independent"),
  and its `subject` the candidate (`{kind: "candidate", run, task, n,
  commit}`, `run` the run its attempt ran in, a continuation's source for
  an imported one); then **None of these**, with no subject;
- `subject`, the record's own: `{kind: "comparison", run, base,
  digest}`, the chain's launched run, its base, and the SHA-256 of the
  options' subjects in their order; a candidate subject would bind the
  record to one lane, and S1 to S4 of DR1 key on the kinds `commit`,
  `candidate`, `artifact` and `plan`, so none of them reads this one;
- `recommendation` -1, always: Yardmaster never fills it, from Brakeman,
  a review or a count of agents;
- `authorizes`: "Continuing the selected candidate into review and
  repair, and delivering it. Final: the candidates not selected are kept
  and cannot be continued; a new comparison tries them again.";
- `context`: the comparison at the opening in words, within DR1's 4,000
  characters: the base, the criteria, per candidate its commit, attempts
  and its checks' outcomes with each row's verdict and why;
- `evidence`: none, so no baseline. The options' subjects name the
  exact commits the person chooses between, and the selection's own
  rule (At most one, and always current, below) closes the record when
  any lane changes; a second, weaker freshness check through DR1's
  evidence baseline would only have to imitate `candidateRefState`'s
  reading of a candidate (`standing`, no end time) from inside the
  engine. A check's receipt is reached from the comparison. Brakeman's
  draft is never in the record: it does not exist at the opening (O3,
  O4), and the comparison's supporting part links it.

A lane without a selectable candidate is no option; with none, there is
no selection, the comparison says "no candidate has a result: nothing to
select", and the run settles. With one, the record has that candidate
and None of these.

**What this row changes in DR1's code**, each with its invariant and its
test (Tests per surface), the decisions plan followed as it stands:

1. The origin and the shapes (`internal/crew/ledger/decisions.go`,
   #634, PR #664): `OriginCompare = "compare"`; `checkOpening` admits it
   with no task, attempt, session or gate, type `approval` only;
   `requestShape.Options` entries gain `Subject` (JSON `subject`,
   omitted when nil), admitted only on an `origin: compare` record and
   only of kind `candidate`, so a request of any other origin with an
   option's subject is refused `invalid` as before ("an option's
   subject is a comparison's"), and DR1's agent requests, decoded
   strictly, read as they do; `checkSubject` admits the kind
   `comparison` for `origin: compare` alone. All of it lives in the JSON
   columns DR1's table already has (`request`, `applied`), so no table
   or column changes.
2. The application (`internal/crew/decisions.go`, #636, PR #678): an arm
   for `origin: compare` ahead of the fallback a record with no holds
   falls to today (`HowNoted`, "noted: the plan's decision holds no
   task"): `how: selected`, and `Application` gains `Selected`
   (`{task, run, attempt, commit}`, JSON `selected`, omitted otherwise)
   or `None` (`none: true`) for None of these.
3. The run's end (`settleRun`, `run.settle`, and DR1's work-ended rule,
   #636). `settleRun` derives the run's outcome inside the change's
   mutation, before the compare step runs (`closeAttempt` calls it as the
   last lane's attempt closes), so a hold that waited for the record to
   exist would come too late: the outcome, `endPending` and
   `run.finished` would be set, and DR1's `endedWork` would cancel what
   the run holds. So `settleRun` derives no outcome for a compare run
   with no applied selection in its chain while either holds: a
   selection of the chain that it holds is open or answered (the run is
   running and idle, the run page saying "waiting for the selection
   dr-9"), or every lane has ended and the compare step has yet to
   settle them or to open their selection (Opening, below), in this same
   change. The compare step ends every change of a compare run by
   calling `settleRun`, so the change that closes the comparison (an
   application, a settling that opens none) derives the outcome in that
   change. The work-ended rule (a decision whose held tasks are all
   finished or doomed is cancelled) never fires for an `origin: compare`
   record, which holds no task by design. A stop cancels nothing of it,
   as a stop cancels no decision in DR1's engine: `recordStop` closes
   the attempts in flight as stopped and records the stop, and sets no
   outcome, and `endedWork` cancels a decision only once the run has its
   outcome or every task the decision holds has ended; so an open or
   answered selection stays as it was over a person's Stop and over the
   server's graceful shutdown, which stops every run it holds
   (`Server.Shutdown`), and is the same record after the restart and in
   the continuation (item 4). Once it is applied, `settleRun` runs as it
   does today.
4. The continuation's reload (`decisions_continue.go`, #636, which
   branches on `OriginPlan` and `OriginAgent` only): an arm for
   `OriginCompare`. Open and answered records move with
   `Tx.MoveDecisions` as DR1 moves every one; and the chain's applied
   compare record is read into `r.selection` (the selected lane, or
   none) before the continuation's first transaction schedules
   anything, so the refusals below hold in every continuation: a plain
   Continue, RV1's repair continuation (#623, `ContinueRunWith`) and a
   restart's. An imported candidate keeps its source run's reference, so
   the selected one is the same candidate in every run of the chain.
   The chain is at most 256 runs (PR-3's bound), so the applied record
   is always inside the window `readDecisionHistory` reads (`maxChain`).
   An answered record not yet applied (a run interrupted by a restart
   while selecting, answered on the ledger as DR1 takes any answer for
   a run it does not hold) moves as answered and is applied right after
   the continuation's first transaction, in a transaction of its own and
   before anything is dispatched (`startContinued` calls DR1's
   `applyDecisions` after `transactFirst` and before `pumpLocked`), by
   the `selected` arm, as DR1 applies every moved answer; the first
   transaction's compare step finds every lane as its options name it
   and keeps it answered, and `settleRun` holds the run open until the
   application; an open one moves and stays open: the compare step reads the
   lanes, not the rows `startContinued` marks, and an imported candidate
   keeps its source run, so it is its option's and nothing supersedes
   the record. A record left open or answered by a stop (the person's,
   or the shutdown's) moves the same way, with its options and their
   commits unchanged, and an answer recorded while the run was stopped
   is applied the same way, right after that first transaction.
5. The rationale (`Engine.RespondDecision`, #635, PR #672): a response
   to an `origin: compare` record without a non-empty rationale is
   refused `invalid` with no row ("say why: the rationale is the record
   of this selection"), at most DR1's 2 KiB; checked in the engine, so
   the route (#637), the CLI (#639) and the card (#640) meet the same
   rule.
6. S1 (DR1's supersession of an approval of a candidate when its task
   gets another): it reads the record's `subject`, which for a selection
   is the comparison, never an option's; so a later candidate of the
   selected task (a Retry of it, RV1's repair) leaves the applied
   selection applied: it continues the approach. Nothing in S1's code
   changes; the test proves it holds.
7. The barrier: PR-6a moves the ledger's
   version up by one more, a migration that changes no table and
   rewrites no row: a binary with comparisons (PR-3) but without this
   row would read an `origin: compare` row through DR1's code as a
   record with no holds, noted, and cancel it at the run's end. Such a
   binary then opens the ledger read-only.
8. The operator's cancel (`CancelDecision`, #636, reached by DR1's
   `POST /api/decisions/{did}/cancel`, #637, and `conductor decision
   cancel`, #639): refused for an `origin: compare` record before any
   write, `invalid` with the words "a selection is not cancelled: answer
   None of these, with a rationale, to end the comparison with no
   candidate"; checked in the engine, so every path meets it, and DR1's
   card offers no cancel. Refused rather than given final words of its
   own because None of these already ends a comparison with no
   candidate, as an answer with its required rationale, so a second way
   would end one with no recorded why (decision 12; I7) and would need
   its own finality words on the route, the CLI and the card; and with
   no cancel there is no act a shutdown could be taken for, since a stop
   closes no decision (item 3).

**Opening.** The compare step (Settling, PR-2) opens the selection,
after the change's mutation and before its drain, in a change that
finds every lane ended: when no selection of the chain is applied, open
or answered, the settled set has a candidate, and the run has room
(`decisionRoom(1)`, DR1's count). That is the change that writes
`compare.settled` (the one that ends the last lane, a lane that ends
while the run is stopping included), or a continuation's first
transaction that finds the lanes ended and no selection (a chain that
settled them with no room: the continuation's records are counted in
its own run). The engine opens it through DR1's
writers in that change (`Tx.OpenDecision` from `r.later`, as `openGate`
does, its `opened` history and its `routed` one, to the operator with
rule "local/1: the operator answers", the run's `decision.opened`
event); DR1's routes, notifications and router are told after the
commit, as for any record. With no candidate, or no room, none opens and
the comparison has **ended with none** ("no candidate has a result:
nothing to select"; "this run has recorded 1,000 decisions: no room for
a selection: continue the run to select in its continuation, or
launch a new comparison"), and the step's `settleRun`
derives the run's outcome in that change. So the change that ends the
last lane records no `run.finished` and calls no `OnEnd` unless it opens
no selection.

**Room for it.** `Tx.OpenDecision` refuses past `MaxDecisionsPerRun`
(1,000 in a run, closed records counted, with the places DR1 holds for
replacements). Nothing is reserved for the selection: a compare run's
other records are its plan's decisions, which `regate` opens again when
a Retry or an Accept makes their tasks runnable (`decisions_wait.go`),
so no count kept apart from DR1's could stay right. The opener asks
DR1's own count at the opening, and a run without room opens none: the
comparison ends with none, said in words, nothing is selected by
default, and the person continues the run (its continuation, with room
of its own, opens the selection in its first transaction) or launches a
new comparison. A continuation's
records are counted in its own run, as DR1 counts them.

**At most one, and always current** (I8). The selection's states are
DR1's: `open` → `answered` (a response) → `applied` (the application);
`open` or `answered` → `superseded` (a lane changed); never
`cancelled`: a stop, a shutdown and a restart leave it as it was, and
the operator's cancel is refused for it (item 8). The rule, one sentence: **an open or
answered selection stays so only while every lane has ended and shows
the candidate its option names.** The compare step keeps it, after
every change of a compare run and before its own settling: when a lane
has not ended (a Retry, a reopen, a Check again) or a lane's shown
candidate is not its option's (an Accept anyway of a failed lane), it
supersedes the record in that change with its words ("approach-c was
retried: dr-9 no longer lists what is compared"; an answered record
keeps its response); then, in the same step, when every lane has ended
again, `compare.settled` is written and a new record opened, its
`supersedes` naming the last. It reads the lanes as they are, not which
rows the change marked: a continuation's first transaction marks every
imported task, and an imported candidate keeps its source run, so it is
its option's and an open record moved there stays open. A stop changes
no lane and closes no selection, so the selection open or answered
before it is the one there after it, in the stopped run and in its
continuation; and since nothing but an answer or a lane's change ends a
selection, no closed record is ever opened again over lanes that did
not change. A command RV1 or a later
row adds changes a lane only through the engine's changes, each
followed by the step, so it is covered without a line of its own. The
invariant: at most one selection
of a chain is open or answered, and while one is, every lane is as its
options name it. So the application needs no check of its own: DR1
applies an `answered` record, and a record whose lanes changed is no
longer answered.

**Responding.** The operator answers through DR1's route (`POST
/api/decisions/{id}/respond`), its card, `conductor decision respond`,
or the comparison's panel, which calls the same route: a `choice` (an
option's index) and the required `rationale`. Only a principal with the
admin grant answers (DR1's `local/1`); an agent's session token,
Brakeman's principal (whose BK2 grants include no response) and a share
link are refused (`requireAdmin`'s `401`, or `403 not_approver` for a
principal past it without the authority). The answer is final (decision
15): the panel and the card say so before Send, and the CLI prints the
record's `authorizes`, which says it.

**Applying.** The application, in its own transaction after the
response's (DR1's order), records `how:
selected` with the chosen candidate (`selected`, or `none`) in the
application receipt, writes the run event `compare.selected`
(`{decision, task, run, attempt, commit}` or `{decision, none: true}`,
about 300 bytes) and lets the run settle with its usual outcome.

**A run not held: a stop or a restart.** After a stop (the person's,
or the server's graceful shutdown when the app quits or updates) a
selecting comparison's run is stopped, and after a crash it is
interrupted; either way it is not held. Nothing of this plan's is added
for it: DR1 keeps the record open (a stop closes no decision, item 3),
takes a workbench token's answer on the ledger (its Responding, which
DR1's own tests drive on a stopped run), supersedes an identity's as it
does for every run not held, and applies an answer right after the
first transaction of the run's continuation (the reload arm above). So a
selection answered while its run is stopped or interrupted, or answered
before the stop and not yet applied, is recorded and applied when the
person continues the run; the comparison, read from the ledger, says
"Recorded: applied when the run is continued" with the run page's
Continue beside it. A continuation keeps every ended lane as it was (a
succeeded task imported, a failed or cancelled one kept failed or
cancelled, `startContinued`), so it keeps the record; one that changes
a lane even so (a succeeded task whose result's attempt the ledger no
longer has, which it marks failed) supersedes the record in its first
transaction, an answered one included, and DR1's application after it
then applies nothing (the rule above). Applied, the selection refuses
the lanes not selected before anything is dispatched, so they start
nothing.

**Stopped, none.** A stop of the run leaves its open or answered
selection as it was (above), and the operator's cancel is refused for
it (item 8). A comparison ends with no candidate selected only by a
person's answer, None of these, applied (for good), or by a settling
with no candidate or no room, which opens none; each is a comparison
**ended** with no candidate selected. After a settling that opened
none, a lane that runs again (a Retry reopens a finished run, as for
every run) settles the lanes anew at its end and opens a new selection,
and a continuation with room that finds the lanes ended opens it in its
first transaction.

**One rule for a comparison's candidates.** Only an applied selection
with a candidate selects a lane. With none (running, selecting,
answered and not applied, stopped before or while selecting, None of
these, ended with none, never settled) no lane is selected, and no candidate is delivered or
repaired. Then:

- once a selection is applied (a candidate, or None of these), every
  task command on a task of a lane not selected (Retry, Accept, RV1's
  Recheck and `RequestChanges`, and any intent a later row adds to
  `commands.go`'s one command path) is refused there before any write
  (`409 not_selected`, "approach-a was not selected in dr-9: launch a
  new comparison to try it again"); the selected lane's tasks keep every
  command. Before an applied selection a lane's commands stay open and
  replace the selection (above), so a person can retry a failed lane
  while the comparison is selecting, or after a stop through Continue;
- a later candidate of the selected task (a Retry of it, a repair's,
  RV1) does not touch the selection: it was of the approach; the
  comparison keeps showing the commit the selection named, and the later
  ones are RV1's Review view's and DS2's candidates list's;
- the delivery and Request changes refusals in `internal/api` (PR-6b,
  PR-8a) read this same rule.

A continuation of a stopped comparison, made with Continue, finds one
of three things: lanes not ended, which it runs again, the change that
ends the last of them settling the lanes and opening the selection as
the first run would; every lane ended and a selection open or answered,
which it moves and keeps (an answer applied right after its first
transaction);
or every lane ended and no selection, which its first transaction
settles (when the chain had not) and opens when it has a candidate.

### `internal/api`: the selection on the routes, one rule for delivery (PR-6b, after DR1's #637)

- **Delivery** (DS2's `POST /api/dispatch/tickets/{key}/links`, kind
  delivery): a link naming a candidate of a compare run's chain is
  taken only for the selected lane's candidate of an applied selection,
  or a later candidate of its task; any other, in every state, is
  refused before any write (`409 not_selected`), with the state's
  words: "select a candidate first: dr-9 is open" while the comparison
  is running, selecting or answered and not applied; "no candidate was
  selected: launch a new comparison" once it has ended with none. PR-3's
  refusal of every compare candidate is narrowed to this.
- **The comparison's reply** gains `selection: {decision, state,
  options, chosen, rationale, by, for, at, applied}` read from DR1's
  record, read in the same revision as the rest (PR-2's rule), and the
  supporting part says, beside Brakeman's draft and the reviews, which
  candidate each favoured, never next to the selection. Brakeman's read
  of the route leaves `selection` out (PR-5).
- **Door 8** (Isolation): `ticket_comment` taken from the selected
  lane's sessions after the application. Door 9 stays closed: DR1's
  decision tools are `refused` for every lane, before and after the
  selection, and `TestAnUnlistedDoorIsClosedToALane` gains its route
  authenticated through `decisionSession`.
- **RV1's `request_changes`**, when #627 is on `main` at this row's cut:
  the refusal step of PR-8a, below, made here instead (the route's
  words before Dispatch's commit; the engine refuses it already, PR-1
  and PR-3), and #627 named in this row's Depends on.

### `internal/api`, `internal/crew`: into review, and for whom (PR-8a, after RV1's #623, #627 and HM1's #645)

- **Request changes on a comparison** (unless PR-6b took it): RV1's
  submit and retry of a `request_changes` review (`dispatch_reviews.go`,
  #627) gain one refusal step, `compareReviewRefusal`, after step 1's
  check of the candidate against the ledger and the ticket and before
  step 2's commit to Dispatch, so nothing is kept: a candidate of a
  compare run's chain other than the selected lane's (the one rule
  above) is refused `409 not_selected` with that rule's words ("select a
  candidate first: a repair before the selection would make the
  candidates unlike"; "approach-a was not selected in dr-9"). Comment
  and Approve stay allowed on every candidate. A `request_changes` on
  the selected candidate is RV1's as it stands: on a run the server runs
  it reopens the selected task at the selected commit; on a finished
  run (the usual case, since the run settles at the selection) RV1's
  continuation carries the repair (`ContinueRunWith`, #623), and that
  continuation carries the crew, its compare block, its lanes and, from
  PR-6a's reload, its selection, so the lanes' rules hold there too.
  Nothing is rebuilt and no new launch is made. In the engine this row
  lists `request_changes` in `compareIntents` with PR-6a's rule (taken
  on the selected lane after an applied selection, refused otherwise);
  until it does, the engine refuses every `request_changes` on a
  comparison (PR-1), and PR-3's continuation rule already takes the
  selected lane's repair continuation, so this row adds no line there.
  The tests that drive RV1's `ContinueRunWith` (#623) are this row's,
  whose Depends on names #623.
- **Check again on a comparison.** RV1's `Engine.Recheck` (#620) goes
  through `act` with the intent `recheck`, refused on a comparison until
  listed (PR-1). The first of this plan's rows cut after #620 is on
  `main` (PR-6a, or else this one), which names #620 in its Depends on,
  lists it in `compareIntents` with PR-6a's rule and its test,
  `TestCheckAgainSupersedesTheSelection`: before an applied selection a
  Check again of a lane makes a new attempt, which closes the record
  through the compare step; after it, one of a lane not selected is refused
  `not_selected` before any write.
- **For whom** (HM1's #645): `Compare.Selector` admits HM1's labels
  (`role:<responsibility>`, `person:<name>`), checked at save with
  `crew.Holder` over the crew's people (#642), as #645 checks a plan
  decision's `for`; the opener of PR-6a resolves it over the run's crew
  as launched and writes `for: {label, person, responsibility}` into the
  request and the person into the opening's `routed` row, as #645 does
  for a gate, so the card and the comparison say "for ana, the
  architecture owner", and the operator's response receipt keeps who
  answered and for whom, #645's receipt. The change sits in #645's
  opener path (`internal/crew` `decisions.go`), extended for an
  engine-opened record.

### `web/app`: the template, the launch's statement, the comparison, the selection (PR-4, PR-7, PR-8b, PR-9)

- `utils/compare.ts`: a row's words (its verdict, the reasons, each
  candidate's test and build files), a cell's words, size and
  dependency words, independence words (with what is not observed), the
  launch statement's words from the plan's `compare` part: the
  candidates' sessions, up to as many review sessions after them, the
  multiplier and its refunds, the checks at the base;
  `composables/useComparison.ts`: the comparison of a run, read
  again on the run's change event, on `dispatch` events naming its
  ticket, on `brakeman` events naming the comparison's request (a
  request's transitions change neither the run, idle while it selects,
  nor its ticket, so without them the supporting panel would stay at
  "queued" after the brief arrived), and on the event stream's reconnect
  snapshot; one fetch at a time. From PR-7 it also reads again on
  `decision` events (DR1's `decisionTold`, #637) naming a record of the
  chain: an answer made on the card, the CLI or another
  client changes neither the run's events while the run is not held
  nor the ticket's, and without them an open comparison would keep the
  old selection.
- PR-4: `CrewCompareTemplate.vue` (the form above, Nuxt UI's `UForm`,
  `UInput`, `UTextarea`, `USelect` of the catalog's agents, the checks
  with the existing `CrewCheckList.vue`), reached by **New comparison**
  on `pages/crews/[[id]].vue`; `CrewCompareBlock.vue`, the block
  read-only in the crew editor, with each lane's tasks;
  `DispatchLaunch.vue` shows the statement and the button "Launch N
  candidates", sending `candidates`, with AC1's member lines below once
  they exist and the words above until then; `DispatchComparison.vue` on
  the ticket page (`?view=compare&run=…`, as RV1's `?view=review`; any
  run of the chain), with `CompareCandidates.vue` (the columns, a failed
  lane's last submitted commit marked not selectable),
  `CompareChecks.vue` (the rows with their verdict's words and the
  receipt dialog DS2 has, `DispatchReceipt.vue`) and
  `CompareMeasured.vue` (size, dependency changes, test and build files,
  independence); `DispatchRuns.vue`'s card and `CrewRunHeader.vue` link
  to it; `RunTasks.vue` groups a compare run's tasks by lane. Until PR-7,
  the page says under the columns "Selecting a candidate arrives with
  durable decisions".
- PR-7: `CompareSupporting.vue` ("Supporting information, not evidence":
  claims, reviews, Brakeman's draft through the existing
  `BrakemanBrief.vue`, each with its source), in its own panel below the
  measured part and never coloured by it; **Ask Brakeman to compare**;
  `CompareSelect.vue`, which renders DR1's `DecisionRespond.vue` (#640)
  with the candidate options, the rationale field required, no option
  preselected, the words "Final: the candidates not selected cannot be
  continued; a new comparison tries them again" above Send, Send
  labelled "Select approach-b (final)", the record's state ("Recorded",
  "Applied: approach-b selected"), and the replacement's words; the
  labels "selected" and "not selected" on the columns, the run page's
  lanes and `DispatchCandidates.vue`. **What this row changes in DR1's
  card** (#640, after it): `DecisionRespond.vue` renders an option's
  `subject` when it has one (the candidate's task and short commit,
  linked to its comparison) and makes the rationale field required, with
  Send disabled until it holds text, when the record's origin is
  `compare`; `DecisionCard.vue` shows the options' subjects and the
  finality words from `authorizes`; so a selection answered from the
  card, the ticket's Decisions section, Needs your decision (MB0) or a
  phone is the same act as on the comparison. Every other origin renders
  as #640 made it.
- PR-8b: **Review the selected candidate** opens RV1's Review view on
  it. **What this row changes in RV1's view** (#631, after it):
  `ReviewCandidate.vue`'s picker takes a label per candidate from the
  run's comparison ("selected", "not selected"), reading
  `useComparison` when the ticket's run is a comparison, and opens on
  the selected candidate; for every other run it is as #631 made it.
  The selection's "for" words. On a phone the comparison is one column:
  each candidate a card with its measured rows, then the supporting
  panel, then the selection; Brakeman's table scrolls inside its card,
  never the page.
- PR-9: **What this row changes in AC1's launch sheet** (AC1's PR-7,
  after it): the member lines of `DispatchLaunch.vue` and
  `CrewLaunchSheet.vue` count the sessions one launch starts on each
  bound account (a member's `account`, AC1's PR-4), "this launch: 3
  sessions on Work", from the members whose tasks start first (for a
  comparison, its candidates; its reviews start after them, said as
  "then up to 3 more"), beside AC1's own figures, which it changes in no
  other way. The capacity plan is followed as it stands; until AC1's
  PR-7 is merged this row waits, and the statement says usage per
  account is not shown.

### What this plan changes in other items' code

Those plans are followed as they stand, and none is asked to change for
this item; each change below is this plan's, in the row named, placed
after the row of theirs it builds on.

| Their code | Their row | The change | This plan's row | Its test |
|---|---|---|---|---|
| DR1's ledger: origins, request shapes, subjects (`internal/crew/ledger/decisions.go`) | #634 (PR #664) | `OriginCompare`; an option's `subject` (kind candidate, compare only); the subject kind `comparison`; the version barrier | PR-6a | `TestTheLedgerAdmitsACompareRecord` |
| DR1's engine: the application, the run's end, the work-ended rule, the continuation's reload, the response (`internal/crew/decisions.go`, `decisions_continue.go`) | #635 (PR #672), #636 (PR #678) | the `selected` arm and its receipt field; the run held open by an open or answered selection, the work-ended rule not applied to it, a stop cancelling nothing of it (as DR1's stop cancels no decision); the operator's cancel refused for it; the `OriginCompare` reload arm with the applied selection, applying an answered one; the required rationale | PR-6a | `TestAnOpenSelectionKeepsTheRunOpen`, `TestAStopKeepsTheSelection`, `TestOnlyAnAnswerEndsTheSelection`, `TestTheSelectionIsApplied`, `TestTheSelectionCrossesTheChain`, `TestASelectionNeedsARationale`, `TestARestartWhileSelecting` |
| DR1's agent decision tools (`internal/api/decisions.go`) | #637 (PR #683) | none in its code: `decisionSession` takes a token through `agentTokenOK`, whose guard (PR-1b) keeps the four tools closed to a comparison's lanes (door 9); the comparison's `selection` is read in this plan's route | PR-1b, PR-6b | `TestEveryAgentDoorIsListed`, `TestAnUnlistedDoorIsClosedToALane`, `TestTheComparisonShowsTheSelection` |
| DR1's card (`DecisionRespond.vue`, `DecisionCard.vue`) | #640 | an option's subject shown; the rationale required for origin compare | PR-7 | `decisionRespondCompare.test.ts` |
| HM1's whom-it-is-for (`decisions.go`'s opener) | #645 | `for` taken from the block's `selector` on an engine-opened record | PR-8a | `TestTheSelectionIsForTheSelector` |
| RV1's review routes (`dispatch_reviews.go`) | #627 | one refusal step for a `request_changes` submit and retry, after the candidate's check and before Dispatch's commit | PR-6b when #627 is on `main` at its cut, else PR-8a | `TestRequestChangesWaitsForTheSelection` |
| RV1's Check again (`commands.go`) | #620 | none in its code: `recheck` refused on a comparison by `compareIntents` (PR-1), then listed with its rule | PR-6a when #620 is on `main` at its cut, else PR-8a | `TestCheckAgainSupersedesTheSelection` |
| RV1's repair in a run (`Engine.RequestChanges`, `act`) | #622 | none in its code: `request_changes` refused on a comparison by `compareIntents` (PR-1), then listed for the selected lane | PR-1, PR-8a | `TestACompareRunTakesOnlyItsListedCommands`, `TestTheSelectedCandidateIsReviewedAsIs` |
| RV1's repair continuation (`ContinueRunWith`, `startContinued`) | #623 | none in its code: `startContinued`'s comparison rule refuses a repair of a lane not selected (PR-3) | PR-3; its `ContinueRunWith` test PR-8a | `TestAComparisonsContinuationRepairsOnlyTheSelected`, `TestTheSelectedCandidateIsReviewedAsIs` |
| RV1's Review view (`ReviewCandidate.vue`) | #631 | the picker's labels and its default | PR-8b | `compareReview.test.ts` |
| Brakeman's request kinds, brief rules and shared reads (`request.go`, `kinds.go`, `brakeman_observe.go`) | #520, #525, #531 | the `compare` kind, its brief rule, the comparison read | PR-5 | `TestCompareRequestTakesACompareRun`, `TestACompareBriefNeedsItsTable`, `TestBrakemanReadsTheComparison` |
| `ext.Host`'s approved launch (`LaunchApproved`, Brakeman's act) | #533, #534 | a compare revision refused | PR-3 | `TestAComparisonIsLaunchedByAPerson` |
| DS2's launch fingerprint (`dispatch.LaunchArgs`, `LaunchAdmitted`) | DS2, on `main` | `candidates` in the arguments | PR-3 | `TestACompareLaunchSaysHowMany` |
| AC1's launch sheet (`DispatchLaunch.vue`, `CrewLaunchSheet.vue`) | AC1's PR-7 (no issue yet; PR #668 awaits the owner) | sessions counted per account | PR-9 | `launchAccounts.test.ts` |

Between one of their rows merging and the row of this plan that extends
it, their code is as their plan made it, and the comparison's rules
still hold, because they sit in the engine paths every such row goes
through, closed until listed: RV1's `request_changes` and `recheck` meet
`compareIntents` in `act` (PR-1) and its repair continuation meets
`startContinued`'s rule (PR-3), so on every `main`, in any order of
merges, no candidate of a comparison is repaired before an applied
selection selects it. Until PR-6b or PR-8a adds the route's step, a
Request changes on a comparison's candidate is kept by Dispatch and
recorded refused with the engine's words, as RV1 records every refused
repair.

### Docs

`docs/protocol.md` in the PRs that add each: the crew's `compare` field
and its rules, the task file's `compare` part, the lane refusal and the
lane views of `run_status` and the ticket tools (PR-1); the run event
`compare.settled` with its bound (PR-2); the routes, the plan's
`compare` part, the launch's `candidates` and the delivery refusal
(PR-3); the `compare` request kind (PR-5); the selection's origin, its
subject kind, the run event `compare.selected` and its refusals (PR-6a,
PR-6b); the review refusal (PR-6b or PR-8a). PR-8b:
`docs/architecture.md` (a comparison beside a run),
`docs/repository-map.md` (`compare.go`, `comparison.go` in
`internal/crew`'s row, `compare_doors.go` in `internal/api`'s),
`README.md` (the features list) and `docs/features.md` (the entry, with
the by-hand observation of Acceptance).

## Events, orderings and invariants

**A comparison's states**, derived from the run chain and its records,
never stored as a state of their own: `running` (a lane has not ended)
→ `settled` (`compare.settled` written; with PR-6a the selection open:
`selecting`) → `answered` (a response recorded, not yet applied) →
`selected` (applied with a candidate) or `none` (applied with None of
these). From `selecting` or `answered`, any change to a lane supersedes
the record and goes back to `running` until every lane has ended again,
then to `selecting` with a new record. A stop of the run, the server's
shutdown and a restart change no state: `selecting` and `answered`
stay so on the stopped or interrupted run, the record answered on the
ledger and applied right after the first transaction of the run's
continuation (PR-6a), and a stopped run whose lanes have not all ended is `running`
until its continuation ends them. The operator's cancel is refused for
a selection, so a comparison goes to `ended` (no candidate selected)
only by a settling with no candidate or with no room in the run's
decisions; from `ended`, a change that runs a lane again or changes
what it shows goes back to `running`, and a continuation with room that
finds the lanes ended goes to `selecting`. A finished or failed run
with no `compare.settled` is `ended` too.

**Orderings.**

- O1. The block is validated at save (`ValidateSave`) and again in
  `prepare` before a run exists; a compare crew with no package (no
  `LaunchPlanned`, no continuation's row) is refused there, before any
  ledger row, and so is one whose file-form manifest has no verify step
  at the base while its candidates have no check.
- O2. The base is read once in `prepare` (the plan revision's), and
  every candidate's first attempt starts at it, having no needs.
- O3. `compare.settled` and the selection (PR-6a, no evidence refs) are
  written by the compare step of the change that ends the last lane (a
  lane that ends while the run is stopping included), with that lane's
  task row, or of a continuation's first transaction that finds every
  lane ended and none settled or selected, after the change's mutation
  and before its drain; `settleRun` derives no outcome in that change unless no
  selection opens (no candidate, no room), so no `run.finished` or
  `OnEnd` comes before the record; every side effect (the router told,
  notifications, Brakeman's request) follows the commit.
- O4. Brakeman's request is queued after `compare.settled` commits,
  keyed by a digest of the run and the event's sequence; a second wake
  finds the op id and queues nothing; one lost in a crash before it is
  made is asked again by hand.
- O5. A response is recorded before it is applied, each in its own
  transaction (DR1's order); `compare.selected` and the run's settle are
  in the application's; on a run not held the application is the
  continuation's, in its own transaction right after its first and
  before it dispatches anything (DR1's `applyDecisions`).
- O6. Every change that leaves a lane not ended, or its shown candidate
  not its option's, before a selection is applied supersedes an open or
  answered selection in that change (its compare step), never after; a
  new one opens only once every lane has ended again. A stop, the
  server's shutdown and a restart close no selection, and the
  operator's cancel is refused for one, so nothing but an answer or a
  lane's change ends it. A settling with no room in the run's decisions
  opens none and ends the comparison with none, until a lane runs again
  or a continuation with room opens it.
- O7. `not_selected` and `lane` refusals are decided before any write,
  and write nothing (the review route's step before Dispatch's commit).
- O8. A continuation's first transaction reads the chain's applied
  selection before it schedules anything.

**Invariants**, each with its test (Tests per surface):

- I1. One base: every candidate's first attempt starts at the run's
  base, and no candidate task has a need.
- I2. Fixed at launch: the objective, the criteria, the approaches, the
  instructions and the checks are the plan revision's, kept in the run's
  crew row and carried by every continuation; an edit of the saved crew
  changes no run.
- I3. Lanes: no door of the Isolation table gives a lane's session
  another lane's work, each door returning what the table says; no
  member works in two lanes; a door not on the list is closed to a
  lane's session.
- I4. Independence is observed, never assumed: shared commits and
  equivalent patches are shown; their absence is said with each check's
  limit (a read of another lane's folder or of an agent's own stored
  state is not observed, a change rewritten by hand is not detected);
  what agents report of their own file activity is not used as a
  measure.
- I5. Alike: candidate tasks have identical checks; a row is comparable
  only when every condition holds and no candidate changed a test or
  build file, `unknown` when only that fails, and the page never shows a
  verdict the server did not give.
- I6. Agreement is not proof: the measured part (`candidates` and
  `checks`) is read only from git and the ledger's rows; agents'
  statements and Brakeman's draft appear only in `supporting`; nothing
  counts agreement; the selection's `recommendation` is -1 and the page
  preselects nothing.
- I7. A person selects: only a principal with the admin grant answers
  the selection; its rationale is required on every path; Brakeman,
  agents and any provider's launch cannot select or launch a
  comparison.
- I8. Current and single: at most one selection of a chain is open or
  answered, and while one is, every lane is as its options name it; a
  response whose lanes changed before its application was superseded by
  that change and applies nothing; a stop, the server's shutdown and a
  restart leave an open or answered selection as it was, with the same
  options and commits, its answer applied when the run is continued;
  the operator's cancel is refused for a selection, so only an answer
  (None of these among them) or a lane's change ends one; a change or a
  continuation that finds every lane ended and no selection of the
  chain open, answered or applied opens one when the run has room; a
  settling that finds no room opens none, and the comparison ends with
  no candidate selected, said in words.
- I9. Nothing is deleted, and only the selected is continued:
  candidates not selected keep their branches, worktrees, attempts and
  receipts; with no applied selection no candidate is delivered or
  repaired; after an applied selection a lane not selected gains no
  attempt, delivery or repair, in every run of the chain.
- I10. Continued as compared: the selected candidate is reviewed and
  repaired as its commit, in its lane, by RV1's transitions; a later
  candidate of the selected task leaves the selection applied; no
  rebuild and no new launch.
- I11. Deliberate: a compare launch states what it multiplies (the
  candidates and their sessions, the reviews' sessions as up to one
  after each candidate that succeeds, that each
  lane is worked as one crew's task is with the refunds named, the
  checks at the base, a Brakeman turn) and no total the engine does not
  keep, before it starts, and is refused unless the person's client
  sends the count; no provider launches one.
- I12. Final: an applied selection is not changed; the panel and the
  card say so before Send.

**Schemas and migrations.** No table or column is added to the ledger
or to Dispatch. The ledger's version moves up twice, each a migration
that changes no table and rewrites no row, made only as a barrier: PR-3,
whose binary first writes a compare run (an older one would continue it
as an ordinary crew, `ContinueRun` decoding `runs.crew` with
`json.Unmarshal`), and PR-6a, whose binary first writes an `origin:
compare` record (a binary with comparisons and DR1 but without PR-6a
would read it as a record with no holds and cancel it at the run's
end). Each PR says which number it took beside DR1's and RV1's. An
older binary opens such a ledger read-only, so it launches, continues,
settles and answers nothing. The crew's `compare` field lives in the
crew file (`crews/<id>.json`), the plan revision's body and the ledger's
`runs.crew`, each a JSON document: a crew without it encodes byte for
byte as before (a golden test); a crew file with it is refused by an
older binary's crew store, which reads with `store.DecodeStrict`. The
selection lives in DR1's tables as they ship: its `origin`, `request`
(an option's `subject`, the subject kind `comparison`) and `applied`
(`selected`, `none`) are text and JSON columns; PR-6a's changes to DR1's
checks admit them (What this plan changes in other items' code).

## Out of scope

- Changing a running comparison: adding, removing or swapping a
  candidate, or a lane building on another's result (the Program,
  section 7, rules out reshaping a managed crew; a new comparison is a
  new revision and launch).
- More than four candidates, and comparing runs launched apart (two
  ordinary runs of one ticket are compared by reading them, as today).
- An operating-system boundary between lanes: worktrees are change
  isolation, and Boxcar (Program item F) is where a guest per attempt
  would come from.
- Any measure of code quality beyond what is listed (no cyclomatic
  complexity, no coverage, no benchmark runner): complexity in words is
  Brakeman's reading, marked as such.
- Merging, pushing or deleting anything: unselected candidates'
  worktrees and branches are removed by hand, as every run's are
  (`docs/features.md`, Automatic worktree cleanup, future work).
- Shared or versioned workflow templates, their ownership and updates
  (Program item TS1); the template here is one generator in this
  repository.
- The capacity view itself, accounts and usage (Program item AC1), and
  routing the selection to a role's holder (Program item SW1).
- A Brakeman background wake on `compare.settled`: the request is queued
  directly when the template asks, and BK3's wake sources stay that
  plan's.

## Tests per surface

Go race tests run per package with the whole module (`go test -race
-count=1 -timeout 30m ./...`), `make lint` (with the open-core lint),
and `make lint-static`; the web with `npm --prefix web run typecheck`,
`npm --prefix web test` and `make test-e2e`; no Python is touched, so
the scripts suite has nothing new.

**`internal/crew` (PR-1, `compare_test.go`, `taskfile_test.go`,
`messages_test.go`, `context_test.go`)**

- `TestCompareValidates`, a table: one candidate, five, a candidate with
  a need, a review task as a candidate, two candidates' instructions or
  checks differing (each field of a check), no check and no manifest, no
  check and a crew-form manifest without a verify step (refused) or with
  one (taken), no check and a file-form manifest (taken at save), a
  member in two lanes, a task outside any lane, a task needing two
  candidates, two reviews of one candidate, a review of a review,
  criteria at 12 and 13, a criterion at 500 and 501 characters and with
  a line break, an approach at 2,000 and 2,001, a selector set before
  PR-8a, a block on a crew without tasks; each refused with its words,
  the good shapes taken.
- `TestCompareTemplateMakesAValidCrew`: two to four candidates, with and
  without reviews, yolo kept, each spec bound at its edge; the crew
  passes `ValidateSave` and its members and tasks are named as above.
- `TestACompareCrewNeedsATicket`: in PR-1 every launch of a compare
  crew refused before any ledger row (`Launch`, `LaunchNamed`,
  `LaunchHeld`, `LaunchPlanned`) with "comparisons arrive with their
  launch statement"; from PR-3, `Launch`, `LaunchNamed` and `LaunchHeld`
  refused, `LaunchPlanned` launched, and `ContinueRun` of that compare
  run, launched and then continued, taken with its package from the
  run's row; a file-form manifest with no verify step at the planned
  base and no checks refused at `prepare` with its words, one with a
  verify step launched.
- `TestEveryCandidateStartsAtTheBase` (I1, door 1): three candidates'
  first attempts start at the planned base while `HEAD` has moved.
- `TestTheComparisonIsTheLaunchedOne` (I2): the saved crew's criteria
  and approach edited after the launch; the run's crew, its task files
  and a continuation's keep the launched ones.
- `TestMessagesStayInTheirLane` (I3, door 5): a message between two
  lanes refused with `ErrLane` and no row, no inbox holding it; within a
  lane (candidate to its reviewer) delivered.
- `TestACompareRunTakesOnlyItsListedCommands` (I9, door 15): through
  `act`, on a compare run's task, `pause`, `unpause`, `cancel`, `retry`
  and `accept` taken, and an intent not in `compareIntents` (the test
  names `request_changes` and `recheck`) refused `ErrNotSelected` with
  no row and no receipt; the same intents taken on an ordinary run.
- `TestATaskFileNamesNoOtherLane` (I3, door 2): golden files
  `testdata/taskfile-compare/{candidate,review}.{md,json}` with the
  comparison part; no other lane's task, member, branch, path or
  approach anywhere in them.
- `TestAComparisonTakesNoDerivedContext` (I3, door 4): a fake context
  provider registered; an attempt of a compare run asks it nothing and
  its task file says there is no derived context; an attempt of an
  ordinary run beside it asks and keeps its blocks.
- `TestTheComparisonPartIsNeverCut`: a task file at `maxTaskFile` cuts
  its other parts and keeps the comparison part whole.
- `TestACrewWithoutCompareIsUnchanged`: every crew of `Examples` and the
  existing fixtures encodes byte for byte as before.

**`internal/api` (PR-1b, `compare_doors_test.go`)**:
`TestRunStatusShowsOneLane` (I3, door 6: approach-b's session calls
`run_status`; the reply holds no other lane's member, prompt, task,
attempt, worktree, branch, commit, summary, issues, findings or diff,
no log, and the block with approach-b's entry only; an ordinary run's
member gets the whole run as today); `TestTicketReadHidesOtherLanes`
(door 7: a comment under approach-a's member's actor, put in
Dispatch's store by the test, absent from approach-b's `ticket_read`; a
person's comment and an earlier run's present);
`TestTicketCommentIsRefusedToALane` (door 8: refused `409 lane` with no
Dispatch write for every lane; from PR-6b, taken from the selected
lane after the application and still refused to the others);
`TestEveryAgentDoorIsListed` (doors 9 and 16: every guide operation of
an agent's scopes and the session routes outside the guide have an
entry in `compareDoors`, DR1's four decision tools listed as `refused`;
doors 12 and 13 asserted to answer as the table says);
`TestTheSessionReadsTakeNoAgentToken` (doors 1 and 13: the files route
and the session read refused `401` to a candidate's token and to an
ordinary run's member's, as today);
`TestEveryAgentTokenPathIsGuarded` (door 16: no call of a session's
`AgentTokenOK` in `internal/api` outside `agentTokenOK`, read with
`go/parser`); `TestAnUnlistedDoorIsClosedToALane` (door 16: three routes
registered in the test, unlisted, authenticating through `taskSession`,
`attentionPrincipal` and `agentTokenOK`, each refused `401` to a
candidate's token and served to an ordinary run's member's; PR-6b adds
one through `decisionSession`). The message route's `409 lane` is PR-1's,
tested with `TestMessagesStayInTheirLane`.

**`internal/crew` (PR-2, `comparison_test.go`, `comparedeps_test.go`)**,
with real git repositories under `t.TempDir()`:

- `TestComparisonMeasuresEachCandidate`: line counts; a file added with
  5 lines beside 5 lines appended to another (both `5 0`), and an empty
  file added beside one deleted (both `0 0`), each told apart by its raw
  status; a rename counted as one deleted and one added with
  `diff.renames` set in the repository's config; a binary file; the
  list cut at 100 with its total; a diff past 4 MiB "not measured".
- `TestRunBoundedStopsAtItsLimit` and `TestRunBoundedEndsWithItsContext`
  (`internal/gitcli`): a git whose output passes the limit ended with
  `ErrOutputLimit` and at most the limit kept; a cancelled context ends
  the process; `Run`'s tests unchanged.
- `TestDependencyChangesAreRead`: `go.mod` single and block `require`
  lines added, removed and moved; `package.json`'s four maps; a lockfile
  only listed; a `go.mod` over 1 MiB listed without modules; a candidate
  changing none.
- `TestSharedCommitsAreFlagged` (I4, door 14): approach-a merges a
  commit of approach-b's branch; reported in `Shared` with its count;
  two independent candidates report none.
- `TestACherryPickIsFlagged` (I4): approach-a makes its own commit, then
  cherry-picks one of approach-b's; `Shared` is empty and `Equivalent`
  names the commit and approach-b, read from `git cherry`; a candidate
  past 200 commits says "not checked".
- `TestComparableNeedsTheSameCheckManifestAndWhere` (I5): one condition
  broken at a time (argv, timeout, `Where` from a fake Runtime, manifest
  identity, `Stable` false, an interrupted, cancelled or running cell, a
  missing cell, a subject not the candidate's commit), each giving "not
  comparable" with its words, all held giving "comparable".
- `TestAChangedManifestIsNotComparable` (I5): the file form,
  approach-b's commit editing `conductor.manifest.json`; its verify rows
  and its task checks not comparable, `ManifestChanged` set.
- `TestAChangedCheckScriptIsNotComparable` (I5): a check `["sh",
  "scripts/test.sh"]`, approach-a editing the script.
- `TestAChangedTestFileIsShown` (I5): a check `["go", "test", "./..."]`,
  approach-a editing a `_test.go` file it runs; the row `unknown`, the
  file listed under approach-a, approach-b's none.
- `TestIndirectDefinitionsMakeARowUnknown` (I5): `["make", "test"]` with
  approach-a editing `Makefile`; `["npm", "test"]` with approach-b
  changing `package.json`'s `scripts` (unknown) and, in another case,
  only its `dependencies` (comparable); each with its words.
- `TestEveryExpectedCheckHasARow` (I5): every candidate fails its lint
  check, so `checkTree` runs no test after it; the test check's row is
  there, each cell `not_run` ("an earlier check failed"), the row not
  comparable. A candidate whose commit removes every verify step of the
  file-form manifest: the base's verify rows there, its cells `not_run`
  ("its manifest has no such step"), not comparable.
- `TestTheComparisonReadsOneRevision`: a seam between the reads
  (`afterRead`) retries a lane, then continues the run, then (PR-6b)
  answers the selection; each time the reply is either all before or
  all after the change, never a column of one with a row or a selection
  of the other; a change at every try gives `ErrRunChanging`.
- `TestAFailedLaneShowsItsSubmission`: approach-c's attempts all failed
  their checks with a submitted commit and no Accept anyway; its column
  shows that commit, its diff and its failed receipts, `Selectable`
  false, and its cells take part in the rows' verdicts.
- `TestTheComparisonWalksTheChain`: a compare run continued (a lane
  re-run in the continuation, the others imported); `Comparison` of
  either run answers the same, the launched run as `Run`, the imported
  candidates with their source run; the reply's size does not grow with
  the number of continuations.
- `TestTheFullestComparisonFitsItsBound`: every part at its bound (the
  table above), every string of `<`, four manifests whose verify ids
  all differ from the base's (48 rows), argv at the 8 KiB bound cut to its preview with
  `ArgvCut` and its digest; the encoded reply within `MaxComparison`;
  each list one past its bound cut with its total.
- `TestAGitFailureLeavesTheRestWhole`: a candidate commit made
  unreadable; that candidate `Unavailable` with git's words, the others
  and the checks table whole; a measure past its 20 s budget (a fake
  clock through the git seam) likewise.
- `TestCompareSettledIsWrittenOnce`: written by the compare step of the
  last lane's change (read from the ledger with that task row), not
  before; a failed lane counts as ended; a review's `needs_changes`
  delays it; a later retry's end writes it again with the new set, and
  a retried lane that fails again writes it again with the same set; a
  pause and an unpause after it write none; a stop's own change writes
  none; a lane whose attempt ends while the run is being stopped (held
  at a seam between the stop's start and `recordStop`) writes it in its
  own change, with that lane's task row; a continuation whose lanes are
  all imported as the chain settled them writes none (its `settled` the
  chain's newest set), and one that finds every lane ended with no
  settling recorded writes it in its first transaction.
- `TestAFailedCandidateEndsItsLane`: reviews on, approach-a succeeded
  and reviewed, approach-b failed with no attempt left and cancelled in
  a third case, their reviews blocked (doomed) and never started (no
  attempt row and no session for approach-b's reviewer);
  `compare.settled` is
  written, with approach-a alone selectable.

**`internal/api` and `internal/dispatch` (PR-3, `compare_test.go`)**:
`TestCompareTemplateRoute` (made, refused with words, an unknown field
refused, the body bound); `TestComparisonRoute` (its shape, any run of
the chain, `404` for a run that is no comparison, the admin and the
share link's refusal, the reply bound); `TestALaneReadsNoEvidenceRoute`
(door 10: a candidate's token refused `401` by the comparison, the
candidates route and a check's log); `TestTheComparisonReadsNoTouches`
(I4: a touched entry of approach-a's session under approach-b's
worktree changes nothing in the reply, whose independence says what is
not observed); `TestThePlanReadStatesTheMultiplier` (I11: for 2 and 4
candidates, with and without reviews, with a crew-form and a file-form
manifest: `candidates`, `reviews` and `checksAtBase`, the words saying
"up to" for the reviews' sessions, and no field of a total);
`TestAComparisonsChainIsBounded` (a compare chain made to 256 runs; the
next continuation refused with its words and no run made; the
comparison of the last run read whole); `TestAComparisonsContinuationRepairsOnlyTheSelected`
(I9, door 15, in `internal/crew`: through a test seam in
`startContinued` that leaves one succeeded lane task unimported, or
gives one a repair, the shape #623's options take, the continuation is
refused with no run made; a plain Continue of the same comparison
taken; from PR-6a, the selected lane's taken after an applied
selection); `TestACompareLaunchSaysHowMany`
(I11: no `candidates`, a wrong count, the right one; a resend with the
same body answered as the same launch; a resend with another count
`op_conflict`; `candidates` on an ordinary revision refused; a
fingerprint made before PR-3 unchanged, in `internal/dispatch`);
`TestAComparisonIsLaunchedByAPerson` (I7: `ext.Host`'s `LaunchApproved`
and Brakeman's granted launch act refused for a compare revision with
the words and no run, an ordinary revision launched by both);
`TestACompareCandidateIsNotDeliveredYet` (PR-3's refusal of every
delivery naming a compare candidate); `TestLedgerVersionIsABarrier`
(the version moved with no table changed; a ledger at it opened by a
binary of the version before is read-only and continues nothing);
`TestComparisonGuideRows` (the guide's operations and the tools
golden).

**`internal/cli` (PR-3)**: `TestCrewsCompare` (a spec file and stdin),
`TestDispatchComparison` (text and `--json`, any run of the chain, the
newest comparison when `--run` is left out), `TestDispatchLaunchCandidates`
(the statement printed and exit 2 without the count).

**`internal/brakeman` and `internal/api` (PR-5)**:
`TestCompareRequestTakesACompareRun` (a `run:` ref of a compare run;
another run, another ref kind or two subjects refused);
`TestACompareBriefNeedsItsTable` (columns in the block's order; each of
the five labels missing in turn; a `decision/1` and a lane command's
`proposal/1` refused; the kind table test updated so the guide and
`brief_submit`'s schema say the rule);
`TestSettledComparisonQueuesOneBrakemanRequest` (once per
`compare.settled`, after its commit; the op id `cmp-` and 24 hex digits,
the same for the same event, with a crew id at its 64-character bound
and a sequence past a million, within `opIDPattern`; the request has no
`via`; none with the block's `brakeman` off, Brakeman not set up or its
runs class off; over budget recorded as such; a restart queues no
second; with no request made, the comparison shows no draft and Ask
Brakeman to compare queues one); `TestBrakemanReadsTheComparison` (the
shared read under the runs class, its bound; a turn whose grant has the
runs class and not the touched class reads the whole reply, which holds
no touched data; a grant narrowed during the turn to drop the runs
class refused the next read); `TestTheDraftReachesNoLane` (door 11: a lane's token
refused Brakeman's four tools; after a brief exists, a re-run lane's
task file, `run_status`, `ticket_read` and inbox carry nothing of it);
`TestTheBriefIsSupportingOnly` (I6, below, the part this PR can show).

**`internal/crew` and `internal/crew/ledger` (PR-6a,
`compare_select_test.go`, `ledger/decisions_compare_test.go`)**:
`TestTheLedgerAdmitsACompareRecord` (an `origin: compare` record with
option subjects and the subject kind `comparison` taken; an option
subject on an `agent` or `plan` record refused `invalid`; the subject
kind `comparison` on another origin refused; a `compare` record with a
task, a gate or a hold refused); `TestSettledComparisonOpensItsSelection`
(one record, origin compare, options the selectable candidates in order
with their subjects and None of these, the record's comparison subject
and digest, recommendation -1, the context's words, no evidence refs and
no baseline, no hold; none when no lane
stands, and the run settles; one candidate and None when one does);
`TestTheLastLaneOpensTheSelectionBeforeTheRunEnds` (F1 of round 3: the
last lane's attempt closes, and in that one transaction, read from the
ledger, the record is opened and `compare.settled` written, with no
`run.finished`, no `OnEnd` called (a counting hook) and the outcome
empty; with no lane standing, the same change records `run.finished`
and opens nothing); `TestAContinuationKeepsAnOpenSelection` (an
unanswered selection, a restart, then Continue with every candidate
imported unchanged: the record moved and still open, not superseded,
no `compare.settled` in the continuation, its run running and idle;
answered from the card and applied there);
`TestAnOpenSelectionKeepsTheRunOpen` (the run running and idle with its
words; the work-ended rule does not cancel it; a Stop leaves it open,
the stopped run with no outcome and no `run.finished`); `TestAnyLaneChangeSupersedesTheSelection` (I8: with approach-c
failed, a Retry of approach-c while selecting, which supersedes and
opens nothing until every lane has ended again, then one record whose
`supersedes` names the old; an Accept anyway of approach-c, which
supersedes and opens the next record in the same change; a failed
lane's task cancelled, which changes no shown candidate and leaves the
record open; the open and answered records counted by origin never
above one; a response to the old refused; the supersession made in the
compare step of the command's own change, read from the ledger with
that command's row); `TestAFullRunOpensNoSelection` (I8: a compare run
brought to `MaxDecisionsPerRun`, 1,000, its last places taken by plan
gates `regate` opened again on Retries; the last lane ends: no record
opened, `compare.settled` written, the comparison ended with none and
its words, the run settled, a delivery refused "no candidate was
selected"; a first settlement with the plan's 10 decisions opened);
`TestAChangeBetweenAnswerAndApplicationAppliesNothing` (I8: a response
recorded, a lane retried before the application's transaction; the
record superseded with its response kept, nothing applied, no
`compare.selected`); `TestTheSelectionIsApplied` (`how: selected` with
`selected`, `compare.selected`, the run's outcome; None of these with
`none`); `TestUnselectedLanesRefuseCommands` (I9: after the
application, Retry and Accept of a lane not selected refused
`not_selected` with no write; after None of these, every lane; the
selected lane's commands kept; before the application, Retry taken and
superseding); `TestARepairOfTheSelectedLaneKeepsTheSelection` (I10:
a Retry and a reopen of the selected task make later candidates; the
applied record stays applied, S1 never touching it, and the comparison
still shows the selected commit); `TestASelectionNeedsARationale` (I7: the engine refuses an empty
rationale `invalid` with no row, the 2 KiB bound kept);
`TestTheSelectionCrossesTheChain` (an applied selection, then a plain
Continue and a restart's Continue; in each continuation the selection
is restored before scheduling and a Retry of a lane not selected is
refused; at PR-3's bound, the selection applied in the launched run is
restored in the 256th run, and `maxCompareChain` equals DR1's
`maxChain`; the `ContinueRunWith` case is PR-8a's, which waits for #623);
`TestARestartWhileSelecting` (a restart between `compare.settled` and
the response: the record open, the comparison read from the ledger as
selecting; a workbench response recorded on the run not held and
nothing applied, the comparison saying it is applied when the run is
continued; Continue applies it right after the continuation's first
transaction, `compare.selected` written in the application's, the lanes
not selected starting nothing; a restart between the response and the
application gives the same; an identity's response superseded as DR1 does);
`TestAStopKeepsTheSelection` (replaces round 3's
`TestCancelledOrStoppedSelectsNone`: a Stop while the record is open,
read from the ledger: the record still open, not cancelled, the same
options and commits; the comparison selecting on the stopped run; a
workbench response recorded there and nothing applied; Continue applies
it right after the continuation's first transaction, `compare.selected`
written in the application's, the lanes not selected starting nothing;
a Stop begun after the response's commit and before its application
(the application held at a seam) applies it once: DR1's application
takes a stopping or stopped run it still holds, so released it applies
there, and a restart before it leaves the record answered for Continue
to apply; never twice and never lost (I1); a Stop of an open
record and Continue with no answer: the record moved, still open, no
`compare.settled` in the continuation);
`TestAContinuationOpensTheSelectionItFinds` (every lane ended and no
selection in the chain: a lane whose attempt ends while the run is
being stopped, held at a seam, opens the record in its own change and
the stop leaves it open; a run settled with no room
(`TestAFullRunOpensNoSelection`'s), continued: the continuation's first
transaction opens the record in its own run, writing no second
`compare.settled`; a continuation whose chain has an applied selection
or None of these opens none);
`TestOnlyAnAnswerEndsTheSelection` (the operator's cancel of an open
and of an answered selection refused `invalid` with its words and no
history row, through the engine (the route's case is PR-6b's); the
record unchanged;
None of these with a rationale applied, ending the comparison with no
candidate, after which a pause and an unpause, a message within a
lane, a Stop and Continue open no record and write no
`compare.settled`); `TestCheckAgainSupersedesTheSelection` when #620 is on
`main` at this row's cut (RV1's Check again of a failed lane before the
selection supersedes the record; after an applied selection, one of a
lane not selected refused `not_selected`); `TestLedgerVersionIsABarrier`
(the second move).

**`internal/api` (PR-6b)**: `TestOnlyTheOperatorSelects` (I7: an agent's
session token, Brakeman's principal through its act route and a share
link with control all refused; the workbench token accepted);
`TestTheRouteNeedsARationale` (I7, the route's `400 invalid` and its
words); `TestAgreementIsNeverEvidence` (I6: a fixture where both
candidates' summaries and both agent reviews favour approach-a,
Brakeman's brief recommends it, and approach-a's `test` receipt failed;
the reply's measured part shows approach-a's failed cell and nothing
derived from the statements, the statements appear only under
`supporting`, the record's recommendation is -1 and its context names
the failed check); `TestBrakemanReadsNoSelection` (Brakeman's read of an
answered comparison has no `selection` part; the admin's has it); `TestADeliveryNamesTheSelectedCandidate` (I9: a
delivery naming a candidate while running, selecting and answered
refused "select a candidate first", and so after a stop while
selecting; after None of these, after a settling with no room, and on
a run that ended with no `compare.settled` refused "no candidate was
selected"; the selected one taken, and a later candidate of its task);
`TestAShutdownKeepsTheSelection` (the server's `Shutdown` with a
comparison selecting, which stops its run as it stops every run: a new
server over the same data directory reads the record open with the
same options and commits and the comparison selecting; the workbench
answers it through the route, the comparison says it is applied when
the run is continued, and Continue applies it; an answer recorded
before the shutdown, its application held at a seam, is applied once,
by the stopping run or by Continue; and DR1's `POST
/api/decisions/{did}/cancel` on the open selection answers `400
invalid` with the engine's words, the record unchanged); `TestAnUnlistedDoorIsClosedToALane`
with its `decisionSession` route, and DR1's four decision tools still
refused to every lane after an applied selection (door 9);
`TestTheComparisonShowsTheSelection`;
and, when #627 is on `main` at this row's cut, the two review tests of
PR-8a.

**`internal/api` and `internal/crew` (PR-8a)**:
`TestRequestChangesWaitsForTheSelection` (`not_selected` on submit and
retry while selecting, with no Dispatch write; Comment and Approve
taken), `TestRequestChangesOnAnUnselectedCandidateIsRefused` (door 15:
after the selection, and after None of these), unless PR-6b took
them; `TestCheckAgainSupersedesTheSelection` unless PR-6a took it;
`TestTheSelectedCandidateIsReviewedAsIs` (I10: a Request changes on the
selected candidate of a finished compare run makes RV1's continuation,
which reopens the selected task at the selected commit, carries the
compare block and the selection, and still refuses a message across
lanes and a Retry of a lane not selected; `ContinueRunWith` with a
repair of a lane not selected refused by PR-3's rule, no run made; in
the run, `request_changes` taken on the selected lane and refused on
the others and before the selection); `TestTheSelectionIsForTheSelector` (HM1's `for` from the block
resolved over the run's crew, the routed row naming the person, the
receipt's who and for whom); `TestSelectorLabels` (in `internal/crew`:
a role and a person admitted as HM1 admits them, others refused).

**Web, vitest**: `utils/compare.test.ts` (each verdict's words and
reasons, sizes, dependency, test-file and independence words, the
launch statement for 2 and 4 candidates with and without reviews and
Brakeman: the candidates' sessions, the reviews' as "up to", the
multiplier with its refunds named,
the checks at the base, and no total) and
`useComparison.test.ts` (read again on the run's event, on a `brakeman`
event naming its request while the run is idle, and on a reconnect
snapshot; one fetch at a time) in PR-4; `compareSelect.test.ts` in PR-7
(no option preselected whatever `supporting` says; the rationale
required before Send; the finality words and Send's label; the measured
and supporting panels kept apart: a cell's colour read only from
`checks`), `useComparison.test.ts`'s PR-7 case (an open comparison
whose run is not held reads again on a `decision` event of its record,
answered from a second client, with no reconnect, and shows the new
state) and `decisionRespondCompare.test.ts` (DR1's card: an option's
subject rendered and linked, the rationale required for origin
`compare` and optional for the others); `compareReview.test.ts` in PR-8b
(the picker's labels and its default, an ordinary run's picker
unchanged); `launchAccounts.test.ts` in PR-9 (sessions counted per
account for a comparison and an ordinary crew, AC1's figures
unchanged).

**Playwright** (each run alone first while iterating, then `make
test-e2e`; the e2e stub agent gains `STUB_COMPARE_EDIT`, which makes one
lane's stub edit the file-form manifest, a check's script or a test
file, so a "not comparable" or "unknown" row is drawn for real):

- `compare.spec.ts` (PR-4): New comparison with two candidates and a
  check; propose and approve the revision; the launch statement with its
  multiplier and "Launch 2 candidates"; two stub candidates finish; the
  comparison shows the base, the criteria, both columns, the checks rows
  (one not comparable with its words), size and dependency rows; a
  message across lanes refused in the run page's messages; the crews
  page's Launch refused with its words.
- `compare-select.spec.ts` (PR-7): Brakeman's stub brief arrives in the
  supporting panel while the run is idle; the selection panel shows no
  choice made; Send is disabled without a rationale and says the answer
  is final; selecting approach-b with a rationale shows Recorded, then
  Applied; the columns and the run page say selected and not selected;
  Retry on approach-a's task refused with its words; a Retry of a failed
  lane while selecting (a second comparison in the spec, one stub
  failing its check) closes the selection, the panel says so, and a new
  one opens when the lane ends; a Stop of the run while selecting
  leaves the panel open with the same options, an answer there says
  "Recorded: applied when the run is continued", and Continue shows it
  applied; the
  same selection answered from the decision card shows the options'
  commits and requires the rationale. After a mode switch and the
  page's own redraw, the labels are checked again.
- `compare-review.spec.ts` (PR-8b): Review the selected candidate opens
  the Review view on it; a Request changes on approach-a is refused with
  `not_selected`'s words; a Request changes on approach-b makes the
  repair, whose new candidate shows in the Review view's picker while
  the comparison still shows the selected commit.
- `capacity-crew.spec.ts` gains, in PR-9, a comparison's launch sheet
  with its sessions per account.

**What a by-hand check covers that tests cannot**: real agents of two
kinds (Claude Code and Codex) on the owner's subscription working the
same ticket apart, Brakeman's real draft, and the person's reading of
the page, in Acceptance.

## Boxcar constraints

1. The engine starts an attempt only through `crew.Launcher`: every
   candidate's and reviewer's attempt is an ordinary attempt; Brakeman's
   comparison is an ordinary turn through `ext.Host`, as its desk
   launches every turn.
2. `session.Local` drives a `session.Process`; nothing here reaches a
   pid or a tty: lanes are a rule of the engine's messages and task
   files, not of processes.
3. An attempt's context is files: the comparison part is written into
   `.conductor/task.md` and `task.json` through the attempt's `os.Root`
   with the rest, and nothing a candidate needs lives only in the
   server's memory.
4. The agent talks to the server over the network: candidates and
   reviewers use the same hooks and MCP over HTTP; the lane refusal is
   the message route's answer.
5. Checks go through `internal/proc` under the run's Runtime with their
   `where`; the comparison reads the ledger's rows and runs no check of
   its own; its git reads are the server's own reads of its repository,
   as `DiffStat` and the Files tab do, not a check.
6. Platform splits stay platform tags: none is added, and the UI shows
   what the server answers.
7. The evidence rows carry `where`: comparability requires the same
   `where` in every cell of a row, so a later Boxcar Runtime's receipts
   are compared only with receipts that ran where they ran.
8. `hostagent` is a launch site: a compare run's attempts start where
   any managed run's do; this item adds no launch site and does not bind
   the comparison to the server's own machine beyond git reads of the
   run's repository, which the server already holds.

## Open-core checklist

- [x] Article 1: no licence check, plan check or paid code; nothing asks
      whether any provider is registered.
- [x] Article 2: the free build compares, measures, drafts with the
      person's own Brakeman and records the selection, with no account
      and no network.
- [x] Article 3: nothing paid is behind a flag; the template's
      `brakeman` field chooses whether to spend the person's own
      subscription, and is no tier.
- [x] Article 4: the selection is a required approval recorded with its
      receipt; stop, pause, cancel, retry and inspect are the run's own;
      every record is in the ledger, Dispatch and the crew files, which
      C6's export carries.

## The PRs

Each PR is one issue and one branch cut from `main`, a draft PR at its
first commit, the checks for its surfaces before every push, its body
ticking the open-core checklist (articles 1 to 4) and naming the agent
that made it; each leaves `main` working and green on its own (a model
without its routes is reached by its tests, a route without its page by
the CLI and the tests, a page without its later parts says what is not
there yet). Parallel means the two touch no common Go package, no common
directory under `web/`, and no common file under `docs/` or at the root.

PR-1 to PR-5 build on `main` as it is (DS2, EN1, C10, the seams and
Brakeman whole; PR-5 after Brakeman's #520, #525, #531 and #534, all
merged). PR-6a builds on DR1's ledger and engine (#634, PR #664; #635,
PR #672; #636, PR #678) and PR-6b on its routes (#637, PR #683); PR-7 on
DR1's card (#640); PR-8a on RV1's repair continuation (#623), its review
routes (#627) and HM1's whom-a-decision-is-for (#645); PR-8b on RV1's Review view (#631); PR-9
on AC1's launch sheet (AC1's PR-7, not yet an issue: the capacity plan,
PR #668, awaits the owner). Each changes those rows' code as this plan's
own work, after them (What this plan changes in other items' code); no
row waits for another plan to be amended. An RV1 arm goes in the first
of this plan's rows cut after its RV1 row is on `main`: `recheck` in
`compareIntents` with its test (#620) in PR-6a or else PR-8a, the
review refusal step (#627) in PR-6b or else PR-8a; the row that takes
it names that RV1 row in its Depends on. Until then the engine refuses
those commands on a comparison (PR-1, PR-3), so no window between
merges opens a rule.

| PR | Title | Contains | Touches | Tests | Depends on | Parallel with |
|---|---|---|---|---|---|---|
| PR-1 | crew: the compare block, its lanes and the template | `Compare`, `Criterion`, `Candidate` on `Crew`, their validation and bounds (the file-form manifest left to the launch), `CompareTemplate` and `CompareSpec`, every launch of a compare crew refused for now, `run.lanes`, the message refusal (`ErrLane`, the route's `409 lane`), `compareIntents` in `act`, no derived context for a comparison, the task file's comparison part and its golden files; `docs/protocol.md`'s crew field and task file part | `internal/crew`, `internal/api` (the message route's code only), `docs/protocol.md` | the PR-1 tests above (`internal/crew`) | none | none |
| PR-1b | api: a comparison's doors | `compare_doors.go` (`compareDoors`, the lane guard in `agentTokenOK`, which every agent-token path calls), `run_status`'s lane view, `ticket_read`'s and `ticket_comment`'s lane rules, DR1's four decision tools listed closed; `docs/protocol.md`'s lane views and refusals | `internal/api`, `docs/protocol.md` | the PR-1b tests above (`compare_doors_test.go`) | PR-1 | none |
| PR-2 | crew: the comparison measured, and the lanes settled | `Engine.Comparison` over the chain and its types, the candidate shown for a failed lane, numstat, `dependencyFiles` and the `go.mod` and `package.json` readers, test and build files, shared commits and equivalent patches, the manifest's blob, the expected check rows with their `not_run` cells and the three verdicts, the one-revision read, the bounds and the git budget, the cache, the lane's end with `doomed`, the compare step in `commit` with `settled` and `compare.settled`, `gitcli.RunBounded` and `git cherry` for equivalent patches; `docs/protocol.md`'s event | `internal/crew`, `internal/gitcli`, `docs/protocol.md` | the PR-2 tests above | PR-1b | none |
| PR-3 | api, cli, dispatch, ledger: the template and comparison routes, the launch's statement | `POST /api/crews/templates/compare`, `GET /api/runs/{run}/comparison`, the plan reads' statement (`candidates`, `reviews`, `checksAtBase`, `brakemanTurn`), the launch's `candidates`, `confirm_candidates` and `LaunchArgs.Candidates`, `LaunchApproved`'s refusal of a compare revision, the launch refusal lifted for `LaunchPlanned` and continuations, `startContinued`'s rule against repairing a lane not selected, the chain's bound (`maxCompareChain`), every compare candidate's delivery refused, the ledger's first barrier, the guide rows, `conductor crews compare`, `conductor dispatch comparison`, `--candidates`, usage and completion; `docs/protocol.md`'s rows | `internal/api`, `internal/cli`, `internal/dispatch`, `internal/crew`, `internal/crew/ledger`, `docs/protocol.md` | the PR-3 tests above | PR-2 | none |
| PR-4 | web: the template form, the launch's statement, the comparison page | `utils/compare.ts`, `useComparison` (with Brakeman's events and the reconnect), `CrewCompareTemplate`, `CrewCompareBlock`, `DispatchLaunch`'s statement and button, `DispatchComparison`, `CompareCandidates`, `CompareChecks`, `CompareMeasured`, the run card's and header's links, `RunTasks`' lanes, the stub's `STUB_COMPARE_EDIT`, `compare.spec.ts` | `web/app/components`, `web/app/composables`, `web/app/utils`, `web/app/pages`, `web/e2e` | vitest (`compare.test.ts`, `useComparison.test.ts`), `compare.spec.ts` | PR-3 | PR-5, PR-6a, PR-6b |
| PR-5 | brakeman, api: Brakeman's comparison draft | the `compare` request kind, `CheckCompareBrief` and its kind-table rule, the guide's words, the comparison in `brakemanReads`, the request queued once on `compare.settled` under its digest op id, the reply's `supporting` part; `docs/protocol.md`'s request kind | `internal/brakeman`, `internal/api`, `docs/protocol.md` | the PR-5 tests above | PR-3; after Brakeman's #520, #525, #531, #534 (merged) | PR-4 |
| PR-6a | ledger, crew: the selection as a decision record | in DR1's ledger the origin `compare`, an option's subject, the subject kind `comparison`; in DR1's engine the selection in the compare step (opened with `compare.settled`, no evidence refs, none when there is no room; superseded when a lane is not as its options name it), `settleRun` deriving no outcome until the step has settled, the work-ended rule not applied, a stop and a shutdown keeping the selection, the operator's cancel refused for it, the selection opened in a continuation's first transaction that finds every lane ended and none open, `how: selected` with `selected` or `none`, `compare.selected`, the continuation's `OriginCompare` reload with the applied selection and an answered one applied there, the required rationale, `not_selected` in the command path, `recheck` in `compareIntents` when #620 is on `main`; the ledger's second barrier; `docs/protocol.md`'s origin, subject kind, event and refusals | `internal/crew/ledger`, `internal/crew`, `docs/protocol.md` | the PR-6a tests above | PR-3; after DR1's #636 (PR #678), on #634 (PR #664) and #635 (PR #672); #620 when it is on `main` at the cut | PR-4 |
| PR-6b | api: the selection on the routes, one rule for delivery | the delivery rule narrowed to the selected candidate, the comparison reply's `selection` (left out of Brakeman's read), `ticket_comment` for the selected lane, the door test's `decisionSession` route; `request_changes`' refusal step when #627 is on `main` at the cut; `docs/protocol.md`'s rows | `internal/api`, `docs/protocol.md` | the PR-6b tests above | PR-5, PR-6a; after DR1's #637 (PR #683); #627 when it is on `main` at the cut | PR-4 |
| PR-7 | web: the supporting panel, Brakeman's draft and the selection | `CompareSupporting`, Ask Brakeman to compare, `useComparison` on `decision` events, `CompareSelect` over DR1's `DecisionRespond` with the finality words, in DR1's card an option's subject and the required rationale for origin compare, the selected and not selected labels on the comparison, the run page and `DispatchCandidates`, the stub brief, `compare-select.spec.ts` | `web/app/components`, `web/app/composables`, `web/app/utils`, `web/e2e` | vitest (`compareSelect.test.ts`, `decisionRespondCompare.test.ts`), `compare-select.spec.ts` | PR-4, PR-6b; after DR1's #640 | PR-8a |
| PR-8a | api, crew: into review, and for whom | RV1's `request_changes` refusal step (unless PR-6b took it), `request_changes` in `compareIntents` for the selected lane, the `ContinueRunWith` tests, `recheck` and its test (unless PR-6a took them), `Compare.Selector` with HM1's labels and the record's `for` on the engine's opener; `docs/protocol.md`'s review refusal | `internal/api`, `internal/crew`, `docs/protocol.md` | the PR-8a tests above | PR-6b; after RV1's #623, #627 and HM1's #645 | PR-7, PR-9 |
| PR-8b | web, docs: Review the selected candidate; the features entry | Review the selected candidate, in RV1's view the picker's labels and default, the selection's "for" words, the phone layout, `compare-review.spec.ts`; `docs/architecture.md`, `docs/repository-map.md`, `README.md`, `docs/features.md` | `web/app/components`, `web/app/utils`, `web/e2e`, `docs`, `README.md` | `compareReview.test.ts`, `compare-review.spec.ts` | PR-7, PR-8a; after RV1's #631 | none |
| PR-9 | web: a launch's sessions per account | in AC1's launch sheet, `DispatchLaunch`'s and `CrewLaunchSheet`'s member lines counting this launch's sessions on each account; `capacity-crew.spec.ts`'s comparison case | `web/app/components`, `web/e2e` | `launchAccounts.test.ts`, `capacity-crew.spec.ts` | PR-4; after AC1's PR-7 (no issue yet) | PR-8a |

Sizes, as the owner asked (2026-11-05: a row is a batch, 300 to 800
changed lines, at most about 1,500 with its tests): PR-1 about 600 lines
and 650 of tests; PR-1b about 350 and 450; PR-2 about 800 and 700; PR-3
about 750 and 700; PR-4 about 800 and 450; PR-5 about 400 and 450;
PR-6a about 550 and 700; PR-6b about 300 and 350; PR-7 about 650 and
450; PR-8a about 300 and 400; PR-8b about 450 and 250, with the docs;
PR-9 about 200 and 200. PR-8b and PR-9 are under the 300-line floor;
each is the change one other plan's row makes possible, and folding
either into another row would make that row wait for a second stream.

## Owner decisions

Dated 2026-11-06. Each is a default the owner can overturn with a dated
row in the Program's status log, its convention for a decision the owner
did not make (the Program, section 3.1); the reasoning is why it was
taken.

1. **The open decisions named with this item.** O4 (the human policy SW1
   ships first) is SW1's: this item records whom the selection is for
   with HM1's labels and lets the operator answer, taking no position on
   roles, groups or delegates. O5 (the graph's tooling) is not touched:
   a graph provider, like any context provider, is not asked for a
   comparison's attempts (decision 5) and is asked as today for every
   other. O8 (packaging and pricing) is not touched: the item is free.
   O10 (Dispatch's merge rules) is not touched: the item adds no
   Dispatch record, and the selection is a ledger record, which is not
   replicated. None blocks this item.
2. **The template is a crew shape, not a new kind of run.** A `compare`
   block on an ordinary crew with tasks, made by a generator and checked
   by the crew's validation, so that DS2 proposes, approves, freezes and
   launches it unchanged and the Program's rule against reshaping a crew
   (section 7) holds. Chosen over a run type of its own, which would
   need its own launch, ledger rows and continuation.
3. **A comparison is launched from a ticket.** Its criteria and
   acceptance are the ticket's revision's, its selection continues into
   RV1's review, which needs a ticket, and its launch is an approved
   revision. Chosen over launching from the crews page, which would
   leave nothing approved and no ticket for the review.
4. **Two to four candidates**, four being the attempts a run runs at
   once, so no candidate waits for another's slot and none starts later
   on a different machine load.
5. **Lanes are enforced on every door Conductor owns and observed
   elsewhere.** The doors are listed once (Isolation): messages, task
   files, inputs, the run and ticket tools, decision tools (closed to
   lanes: a candidate asks with `ask`), evidence routes and Brakeman's
   reads; a door not on the list is closed to a lane's session until its
   rule is written, enforced in the one predicate every agent-token path
   calls. A comparison's attempts take no derived context, since a
   provider's block cannot show where it came from (the free build asks
   no provider, so it loses nothing). Shared commits and equivalent
   patches are shown as findings; the touched index is not, being what
   agents report of themselves. Chosen over claiming isolation the worktree does not
   give (the Program, section 1.5), over a context filter that cannot
   prove a block's provenance, and over an operating-system sandbox,
   which is Boxcar's (Program item F).
6. **Comparable means the same check, manifest, place, commit and an
   intact finished run, an unchanged check script, and no changed test
   or build file**; a changed test or build file makes the row
   "comparable only if their test changes are fair", with the files
   listed, since Conductor cannot tell a test added from a test
   weakened; anything else is said not comparable with its reason,
   never hidden and never failed. Durations are shown and never
   compared.
7. **Measured and said are two panels.** The measured part reads git and
   the ledger only; agents' summaries, reviews and Brakeman's draft are
   supporting information in their own panel; no agreement is counted;
   nothing is preselected. Chosen over a single scored table, which
   would turn agreement into apparent evidence.
8. **Brakeman drafts once, when asked by the template or a person.** One
   request when every lane is done, on the person's own Brakeman account
   and budget, stated at the launch; none when Brakeman is off; one lost
   in a crash is asked again by hand (Ask Brakeman to compare), since
   the selection never waits for it.
   Its brief must carry the five rows; it may not carry a decision card
   or a lane command.
9. **The selection is a DR1 record of a third origin, `compare`**,
   opened by Yardmaster in the transaction that settles the last lane,
   its options bound to exact commits, its own subject the comparison,
   no evidence refs, its recommendation always -1, its rationale
   required, holding the run open while it is open or answered (and
   while the change that ends the last lane has yet to open it), closed
   by the change that leaves a lane not as its options name it, before
   it is applied (at most one at a time), kept open or answered over a
   stop, the server's shutdown and a restart as DR1 keeps every decision
   (its engine cancels a decision only once the run has its outcome or
   the work it holds has ended, and a stop gives no outcome), never
   cancelled by the operator, opened in a continuation's first
   transaction that finds every lane ended and none open, not opened
   when the run has no room for it, applied as `how: selected`, and
   after a stop or a restart applied when the run is continued, as DR1
   applies every answer of a run it does not hold; a later candidate of
   the selected task does not supersede it. The
   changes to DR1's code this needs (the origin and its shapes, the
   application's arm, the run's end, the continuation's reload, the
   required rationale, the cancel's refusal, the card) are this plan's own, in PR-6a, PR-6b
   and PR-7, after DR1's rows; the decisions plan is followed as it
   stands. Chosen over a gate in the plan revision, which opens at
   launch, before any candidate exists, with Approve and Reject as its
   only answers, over a record of this item's own, which the Program
   gives to DR1, and over a stop that cancels the selection, which would
   cancel it whenever the app quits or updates while the person reads
   the comparison, after which nothing could open it again for
   candidates that succeeded (a Retry takes only failed or cancelled
   tasks and an Accept only failed ones) and the person would pay for
   every candidate again in a new comparison.
10. **Nothing is deleted.** Candidates not selected keep their branches,
    worktrees, attempts and receipts, labelled not selected; after the
    selection they gain no attempt, delivery or repair. Chosen over
    removing them, which Conductor does for no run today and which would
    destroy the evidence the selection rests on.
11. **Only a selected candidate is continued.** RV1's Request changes on
    the selected one reopens its task (or RV1's continuation carries the
    repair), and a delivery names it; with no applied selection (running,
    selecting, answered, stopped before it was applied, None of these,
    ended with none) nothing is delivered or repaired. Chosen over letting a person repair or deliver any
    candidate, which would make the candidates unlike before the choice,
    spend work on approaches not taken, and let a delivery skip the
    recorded selection.
12. **None of these** is always an answer; it ends the comparison, and
    the ticket's next step is DS2's Revise and a follow-up launch. It is
    the one way a person ends a comparison with no candidate: the
    operator's cancel is refused for a selection, so every such end
    carries a rationale, and nothing a stop or a shutdown does can be
    taken for it. Chosen over a cancel with final words of its own,
    which would end a comparison with no recorded why and need those
    words on the route, the CLI and the card.
13. **The launch states what it multiplies and must repeat the count.**
    The statement (the candidates and their sessions, the reviews'
    sessions as up to one after each candidate that succeeds, each lane worked as one crew's task with the engine's refunds
    and Retries named, the checks at the base, a Brakeman turn) is this
    plan's, and states no total the engine does not keep; usage per account is AC1's, shown below it
    once AC1's PR-7 merges, with this plan's count of the launch's
    sessions on each account (PR-9), and said to be absent until then
    (this plan follows the capacity plan's final text on PR #668).
    Only a person launches a comparison: Brakeman and any provider
    through `ext.Host` are refused. Chosen over a confirmation dialog
    alone, which a script or a second client would not see, and over
    carrying the count through Brakeman's proposal, whose grant was given
    before any statement was shown.
14. **When the code starts.** Now for PR-1 to PR-5; PR-6a, PR-6b, PR-7,
    PR-8a, PR-8b and PR-9 as the rows of other items they build on
    merge. CP1's Status follows the Program's rules: In progress at its
    first PR, In review when the last is ready, Done when the boundary
    below is observed.
15. **The selection is final.** Once applied it is not changed: the
    panel and the card say so before Send ("Final: the candidates not
    selected cannot be continued; a new comparison tries them again"),
    Send names the candidate and says final, and the record's
    `authorizes` says it for the CLI. Chosen over a "select again" that
    supersedes an applied record, which would let a lane not selected
    gain a repair or a delivery after the others were refused theirs,
    and whose cost (re-running every candidate) is already what a new
    comparison asks.

No question is left open: each answer above lets the plan stand, and the
owner overturns any of them with one row. No other plan is asked to
change for this item: what it needs of their code is made in its own
rows (What this plan changes in other items' code).

## Acceptance

The boundary, in this plan's words: two candidates for one ticket are
checked alike and one is selected, by a person, with the rationale
recorded; agreement among agents is never taken for proof. It is
observed, not ticked:

- Once PR-8b is merged (and PR-9 when AC1's PR-7 is), on the owner's `conductor serve` with real agents
  on the owner's own subscription (Claude Code for one candidate, Codex
  for the other), a scratch repository both trust (as DS2's and RV1's
  observations used), a real ticket with acceptance criteria and a
  design question with two credible answers, and Brakeman set up on the
  owner's account.
- A comparison made with the template (two candidates with different
  approaches, one check of the project, agent reviews on, Brakeman's
  draft on), proposed and approved as a revision, and launched; the
  launch's statement read before Launch and the count sent.
- Both candidates' first attempts started at the revision's base (read
  in the ledger and in each task file); each task file names no other
  lane; a message tried across lanes is refused; each candidate's
  `run_status` shows its own lane only and its `ticket_comment` is
  refused.
- The comparison shows both candidates' receipts for the same check,
  comparable, each linked to its log with its SHA-256; one row made not
  comparable by changing a check's script in one candidate (by hand, in
  a second comparison of the same ticket) says why.
- Brakeman's draft arrives in the supporting panel with the five rows
  and its facts cited; the measured part is unchanged by it; nothing is
  preselected.
- With the selection open, the owner quits the desktop app and starts it
  again: the selection is still open, with the same options and commits,
  on the stopped run. (Or, in a second comparison, the owner stops the
  run while selecting and answers there: the comparison says the answer
  is applied when the run is continued, and Continue applies it.)
- The owner selects one candidate with a rationale; the decision record
  (`GET /api/decisions/{id}`) shows the options with their commits, the
  response with the rationale and the actor, and the application
  `selected` with the commit; the ticket page's Decisions section and
  Needs your decision show the same record.
- The candidate not selected keeps its branch, worktree and receipts, is
  labelled not selected, and refuses a retry and a Request changes with
  words; the selected candidate is reviewed in RV1's Review view as its
  commit, and one Request changes makes one repair in its lane.
- The observation, and what was missing, slow or wrong, goes into
  `docs/features.md` (PR-8b, or a later docs PR of the stream when it
  comes after PR-8b merges); a bug the stream caused is fixed in the
  stream, a defect outside it filed.
- Done means: every PR merged and the observation recorded with the
  owner's word that the comparison showed what was compared, on which
  commits, with which checks, and why the chosen one was chosen; the
  Program's status row moves to Done then, never on the table alone. It
  is part of the R3 gate's free items (the Program, section 6).

What needs the owner: the review of this plan and its decisions;
cutting the twelve issues; the approval of the capacity plan (PR #668),
on which PR-9 alone waits; the observation, whose agents run on the
owner's subscription; the merges. Nothing waits for another plan to be
amended.

## Changed in review round 1

Answers to the independent review (2026-11-06), Codex round 1, and the
needs this plan's first draft named of the decisions, review, capacity
and Brakeman plans, which are carried here as this plan's own work:

- The changes to DR1's code (the origin `compare`, an option's subject,
  the subject kind `comparison`, the application's `selected` arm, the
  run held open and the work-ended rule not applied, the continuation's
  reload with the applied selection, the required rationale, S1 left
  untouched by design, the card's subject and rationale, HM1's `for` on
  an engine-opened record) are rows of this plan: PR-6a after #636, PR-6b
  after #637, PR-7 after #640, PR-8a after #645; Owner decisions 9 no
  longer waits on the decisions plan; a new section lists every change
  this plan makes in other items' code (independent finding 1; the
  decisions plan's eight carried needs).
- RV1's `request_changes` refusal step (PR-6b or PR-8a, after #627) and
  the Review view picker's labels (PR-8b, after #631) are this plan's
  rows (the review plan's two carried needs).
- The launch sheet's sessions per account is a new row, PR-9, after
  AC1's PR-7 (the capacity plan's carried need).
- The `compare` request kind, its brief rule and the comparison read are
  PR-5's, after Brakeman's #520, #525, #531 and #534 (the Brakeman
  plan's two carried needs).
- Isolation: every door a lane's session has is listed once with what
  it returns, its row and its test; `run_status` shows the caller's lane
  only, `ticket_read` hides other lanes' comments, `ticket_comment` is
  refused before the selection, DR1's decision tools get a lane rule,
  evidence routes refuse a session token, Brakeman's draft reaches no
  lane, and a door not on the list is closed to a lane (independent
  finding 2; Codex 1).
- No derived context for a comparison's attempts, instead of a filter on
  an optional ref (Codex 2).
- Two ledger version barriers, PR-3 and PR-6a (Codex 3).
- A lane ends with its review doomed behind a failed candidate (Codex
  4).
- At most one selection open or answered per chain; any lane change
  supersedes it, open or answered; the application checks the lanes
  again (independent 6; Codex 5).
- The applied selection is restored in every continuation; the
  comparison walks the run chain; a restart's answer is applied on the
  ledger (independent 5; Codex 6).
- One rule: only an applied selection's candidate is delivered or
  repaired, in every state (independent 4; Codex 7).
- A failed lane's last submitted commit and receipts are shown, not
  selectable (Codex 8).
- Cherry-picks are caught by patch ids beside shared ancestry, with
  what is not caught said (Codex 9).
- A changed test or build file makes a row `unknown` with the files
  listed (independent 3; Codex 10).
- The bounds recomputed: 40 check rows, argv previews with a digest,
  every string cut as encoded, `MaxComparison` 1 MiB as the sum of its
  parts (Codex 11).
- A file-form manifest's verify steps are required at the launch, not at
  save (Codex 12).
- `candidates` in DS2's launch fingerprint; a comparison is never
  launched by Brakeman or a provider through `ext.Host` (independent 8;
  Codex 13).
- The launch states ceilings: sessions at least and up to, candidate
  and review attempts, check runs, setup steps (independent 7; Codex
  14).
- Brakeman's op id is a 28-character digest; no `via` field
  (independent 9; Codex 15).
- A draft lost in a crash is queued at the next start while the
  selection is open (Codex 16).
- The comparison page reads again on Brakeman's events and on a
  reconnect (Codex 17).
- The selection is final, said before Send (independent 10; Owner
  decisions 15).
- PR-1 refuses every compare launch until PR-3 lifts it with the count
  (independent 11).
- The launch refusal keys on the package, so a continuation passes
  (independent 12).
- RV1's Check again needs no arm of its own: `compareFresh` and the
  command path cover it, its test in the first row after #620
  (independent 13).
- The selection's evidence is the candidates' refs with a baseline from
  the engine's rows; Brakeman's draft is linked from the comparison only
  (independent 14).
- PR-8 split into PR-8a (Go) and PR-8b (web, e2e, docs), and PR-1's API
  doors into PR-1b (independent 15).

## Changed in review round 2

Answers to Codex round 2 (2026-11-06). Seven of its findings were round
1's fixes that did not hold; each was answered by asking first whether
the item needs the mechanism, and where it did not, the mechanism went
and the promise was narrowed to what the code keeps.

**Mechanisms removed:**

- The touched index as an independence finding (`CrossTouch`, its
  2,000-entry words, `TestATouchInAnotherLaneIsFlagged`): it holds what
  agents report of themselves, and it was read under the wrong
  Brakeman class. Independence is shared commits and equivalent patches,
  with what is not observed said (Codex 2, 3).
- The session and check-run totals of the launch statement
  (`maxSessions`, `maxCandidateAttempts`, `maxReviewAttempts`,
  `maxCheckRuns`, `setupSteps`): the engine refunds a review's attempt
  on `needs_changes` and an attempt that could not start, and a
  candidate runs its own manifest's checks, so no such total is kept
  (Codex 4, 5).
- The selection's evidence refs and the baseline the engine wrote to
  imitate `brakeman.RefState` (`TestTheSelectionBaselineIsRefState`):
  the options name exact commits and the lane rule closes the record on
  any change (Codex 7).
- `compareFresh`'s snapshot comparison at the end of every transaction
  and the application's second check: replaced by one rule kept in the
  transaction's drain of changed task and attempt rows, the one way a
  lane changes (Codex round 1's 5, kept simpler).
- The application of an answer on a run not held, and its recovery from
  `ledgerRecovered`: an answer given while the run is interrupted is
  applied when the run is continued, DR1's own path (Codex 13's
  neighbour, the restart case).
- `reconcileCompareDrafts`, the start-time re-queue of a Brakeman draft
  lost in a crash: Ask Brakeman to compare queues one by hand (Codex 13).
- DR1's decision tools' lane rule for candidates (PR-6b,
  `TestADecisionRequestNamesNoOtherLane`): the tools stay closed to a
  comparison's lanes; a candidate asks with `ask`.
- `Runs` and `Later` in the comparison's reply: the history lives in the
  Review view and DS2's candidates list, each paged there (Codex 10).

**Promises narrowed:**

- The launch states the first sessions, the multiplier with its
  refunds, and the checks at the base, never an upper total (Codex 4,
  5).
- After an applied selection the comparison shows the commits the
  selection named, not the selected lane's later ones (Codex 10).
- A draft lost in a crash is asked again by hand (Codex 13).
- A selection answered during a restart is applied when the run is
  continued, not at once.

**Fixed where the mechanism stays:**

- The lane guard sits in `agentTokenOK`, the one predicate every
  agent-token path calls (`attentionPrincipal`, `taskSession`, the
  Brakeman session routes, #637's `decisionSession`), with a parser test
  that no other path takes an agent token and an unlisted-route test
  through the real helpers (Codex 1).
- Brakeman's read carries no touched data and, from PR-6b, no
  selection (Codex 2).
- Check rows come from the expected checks (the crew's and the base
  manifest's) with `not_run` cells, so a check no candidate reached is
  shown and not comparable (Codex 6).
- A lane command that would need another selection when the run has no
  room for one (`MaxDecisionsPerRun`) is refused before any write; the
  first selection always has room, the run's other records being its
  plan's decisions (Codex 8).
- The comparison's ledger reads are of one revision, under the
  candidates route's rule, then git (Codex 9).
- RV1's repair can reach a comparison's candidate on no `main`, in any
  order of merges: `compareIntents` in `act` refuses commands a
  comparison has not listed (PR-1), and `startContinued` refuses a
  comparison's continuation that repairs a lane not selected (PR-3)
  (Codex 12).
- The `ContinueRunWith` tests move to PR-8a, which names #623 (Codex
  11).
- `useComparison` reads again on DR1's `decision` events (PR-7) (Codex
  14).

## Changed in review round 3

Answers to Codex round 3 (2026-11-06), the last. Each was answered by
asking first whether the item needs the mechanism; where one stays, its
states, its invariant and its test are in the row that builds it.

**Mechanisms removed:**

- The supersession and opening in the transaction's drain, keyed on the
  rows a change marked: `startContinued` marks every imported task, so
  an unchanged continuation superseded an open selection, and the drain
  ran after `settleRun` had already ended the run. Replaced by the
  compare step (Settling, PR-2; At most one, PR-6a), which reads the
  lanes as they are after each change, before `endedWork` and the
  drain, with one remembered value, `settled` (Codex 1, 2, 3).
- The `no_room` refusal of lane commands and the room it reserved for
  the next selection: `regate` opens plan gates again on a Retry or an
  Accept, so no count kept apart from DR1's stays right. The opener asks
  DR1's count at the opening and, without room, opens none (Codex 4).
- The server-piped `git patch-id` and its 8 MiB patch bound: `git
  cherry`, one argv process with no input, compares patch ids itself
  (Codex 6).
- `firstSessions`, a count of sessions that start whatever happens:
  reviews start only after their candidate succeeds (Codex 9).
- The claim that the files route and the session read serve a lane's
  token: they take none today, and the plan keeps it so (Codex 10).

**Promises narrowed:**

- A selection always has room becomes: a settling with no room opens
  none, and the comparison ends with no candidate selected, said in
  words (Codex 4).
- A comparison's chain is at most 256 runs (DR1's `maxChain`): the next
  continuation is refused, so the applied selection is always read
  again (Codex 5).
- After the operator's cancel or a stop, no selection opens until a lane
  runs again or changes what it shows (Codex 3).
- A renamed file counts as one deleted and one added (Codex 8).
- The launch states the candidates' sessions and up to as many review
  sessions after them, not a sum that starts at once (Codex 9).

**Fixed where the mechanism stays:**

- `settleRun` derives no outcome for a compare run while the change that
  ends the last lane has yet to open its selection, so that change
  records no `run.finished` and calls no `OnEnd` before the record
  (Codex 1).
- `gitcli` gains `RunBounded`, which ends git past its output limit, so
  the comparison's bounds hold before allocation (Codex 6).
- The raw status beside numstat tells files added and deleted from
  lines appended and removed (Codex 8).
- Brakeman's comparison read notes each candidate and receipt by its
  exact ref (`filterComparison`), so a brief that cites them after that
  read alone cites what was observed (Codex 7).

## Changed in the final check

- I4 no longer says cross-lane touches are shown: round 2 removed the
  touched index as a measure, and I4 had kept its words.
- Door 14 names an agent's own state under its HOME (its memory,
  history and session stores) among what Conductor does not own: it is
  not prevented and not observed, and the comparison's independence
  words say so.

## Changed after the review rounds

The final check found that a stop cancelled the selection: the server's
graceful shutdown stops every run it holds, so quitting or updating the
app while a person read the comparison cancelled it, and with both
candidates succeeded nothing could open it again (a Continue imported
them and opened nothing; a Retry takes only failed or cancelled tasks,
an Accept only failed ones), so the person would launch and pay for
every candidate again. The plan had also called that cancel DR1's,
which it is not: DR1's `endedWork` cancels a decision only once the run
has its outcome or the work it holds has ended, and `recordStop` gives
no outcome, so DR1 keeps every decision over a stop. Changed:

- A stop, the server's shutdown and a restart keep an open or answered
  selection as it is, as DR1 keeps every decision: the same record,
  options and commits on the stopped run and in its continuation, an
  answer recorded there and applied when the run is continued (PR-6a
  items 3 and 4, A run not held: a stop or a restart; the states; O6;
  I8; decisions 9 and 11).
- The compare step has no exception for a run being stopped: a lane
  that ends during a stop settles the lanes and opens the selection in
  its own change; a continuation's first transaction starts from the
  chain's newest `compare.settled` and runs the step on the lanes it
  finds, so one that finds every lane ended and no selection settles
  and opens it there (Settling, PR-2; Opening; O3).
- The operator's cancel is refused for a selection (`invalid`, with
  words that name None of these), so None of these, with its rationale,
  is the one way a person ends a comparison with no candidate, and no
  stop or shutdown can be taken for that act (PR-6a item 8; decision
  12).
- A settling with no room says the run can be continued to select, and
  a continuation with room opens the selection in its first transaction.
- Tests: `TestCancelledOrStoppedSelectsNone` is replaced by
  `TestAStopKeepsTheSelection`, `TestAContinuationOpensTheSelectionItFinds`
  and `TestOnlyAnAnswerEndsTheSelection`; PR-6b gains
  `TestAShutdownKeepsTheSelection`; `TestCompareSettledIsWrittenOnce`,
  `TestAnOpenSelectionKeepsTheRunOpen`,
  `TestADeliveryNamesTheSelectedCandidate`, PR-8a's refusal test and
  `compare-select.spec.ts` say what a stop now leaves.
- Acceptance: the owner quits the desktop app while the selection is
  open and finds it open after the restart, and a selection answered on
  a stopped run is applied by Continue.

## Changed in the second final check

Read against `startContinued` (`issue-636`), which moves a run's open
and answered decisions in the continuation's first transaction and
applies the answered ones after it, each in its own (`applyDecisions`,
before `pumpLocked`):

- An answer left by a stop or a restart is applied right after the
  continuation's first transaction, not in it (PR-6a item 4, A run not
  held, the states, O5, the continuation's three cases, and the tests
  `TestARestartWhileSelecting` and `TestAStopKeepsTheSelection`).
- A continuation keeps every ended lane as it was (a failed task stays
  failed), so the old example of one re-running a failed lane is
  replaced by the one way it can change a lane, a result the ledger no
  longer has, which supersedes the record before DR1's application.
- A Stop between a response and its application applies it once,
  in the stopped run (DR1's application takes a run it still holds) or
  after a restart through Continue (`TestAStopKeepsTheSelection`,
  `TestAShutdownKeepsTheSelection`).
- The operator's cancel through DR1's route is tested in PR-6b, which
  comes after #637; PR-6a tests the engine's refusal.
- `useComparison` reads again on an answer, not on "a cancel", which a
  selection no longer has.
