# Compare approaches: one ticket, one base, isolated candidates checked alike, a Brakeman comparison and a person's recorded selection (Program item CP1)

Status: proposed 2026-11-06, for the owner's review; the owner sets it
to Planned. Main at a43d5c56 (PR #676, 2026-11-06). Every fact below
about the code was read from that commit on 2026-11-06, and every fact
about work not yet merged from the branch of its open pull request on
the same day; file, function and constant names are those.

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
  #671, issue #642; PR #674, issue #643; their other rows are issues
  #637 to #641, #644 and #645). The selection is a DR1 record, so PR-6
  waits for DR1's #634, #635, #636 and #637 and PR-7 for #640;
  continuing into review is RV1's review, so PR-8 waits for RV1's #627
  and #631, and for HM1's #645 for whom the selection is for. Each
  waiting PR names its rows in The PRs.

**The items this one meets, and who owns what they share.**

- **DS2** owns plan revisions, launches, packages, candidates and
  deliveries. This plan adds no Dispatch record: a compare crew is a
  crew, so its revision body carries the `compare` block inside the crew
  JSON it already freezes (`PlanBody.Crew`, a `json.RawMessage`); this
  plan adds a statement of what a compare launch starts and one field
  the launch must send (`candidates`), and refuses a delivery that names
  a candidate not selected.
- **EN1** owns the project manifest; this plan reads which manifest ran
  each check (the ledger row's `Manifest`, the tree's
  `manifest.resolved`) to say whether two receipts are comparable, and
  changes nothing of manifests.
- **RV1** (`docs/plans/review-repair.md`) owns reviews, findings, marks,
  Request changes, Check again and the Review view. The selected
  candidate is reviewed there as any candidate is; this plan adds two
  refusals to a `request_changes` review of a compare run's candidate
  (before the selection; of a candidate not selected) and the labels the
  Review view's candidate picker shows.
- **DR1 and HM1** (`docs/plans/decisions.md`) own the decision record,
  its routes, its card, Yardmaster's wait and application, and whom a
  decision is for. The selection is one DR1 record; this plan opens it
  from the engine and applies it, through DR1's writers, and names in
  Owner decisions and its result what DR1's text does not yet allow.
- **Brakeman** (`docs/plans/brakeman.md`) owns request kinds, brief
  kinds, reads and grants. This plan adds one request kind (`compare`)
  with its brief rule and one shared read (the comparison route), in
  Brakeman's tables, as that plan lets a later item add them.
- **AC1** (`docs/plans/capacity.md`, PR #668) owns the capacity view a
  launch shows: accounts, usage windows, resets. This plan owns the
  statement of how many candidates, sessions, attempts, checks and
  Brakeman turns a compare launch starts; it sits above AC1's member
  lines in the launch sheet and says, until AC1's PR-7 merges, that
  usage per account is not shown in this build.
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
   DR1 record does; context blocks from a registered `ContextProvider`
   are filtered, never asked differently; the checks run through the
   run's Runtime as every check does.
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
  revision shows, before anything starts: "This comparison starts 3
  candidates at once: 3 agent sessions (Claude Code, Codex, Claude
  Code), up to 9 attempts, 2 checks on each result; 3 agent reviews; 1
  Brakeman turn on your Brakeman account, when all candidates are done.
  Usage per account: shown below" (AC1's member lines, once its PR-7 has
  merged; until then: "Usage per account is not shown in this build").
  The button says **Launch 3 candidates**, and the launch is refused
  unless it says the same number. Launching the crew from the crews page
  is refused in words: "a comparison is launched from a ticket's
  approved plan revision".
- **The candidates run apart.** Each candidate's attempt starts at the
  run's base with the same instructions and its own approach; its task
  file says it is one of three candidates working alone, lists the
  criteria, and names no other lane. A message from one lane's member to
  another lane's is refused ("approach-b's member is in another lane of
  this comparison"). The run page groups the tasks by lane.
- **The comparison.** The ticket page's run card and the run page link
  to **Comparison** (`/dispatch/CND/12?view=compare&run=…`): the base
  commit, the objective, the criteria; one column per candidate (its
  agent and arguments, its approach, its standing commit, its attempts,
  its state) and these rows, each said to be **measured by Conductor**:
  - **Checks**: one row per check and per manifest verify step, each
    cell the receipt's outcome (passed, failed with its exit, timed
    out), its duration and where it ran, linked to the receipt and its
    log; a row is marked **comparable** or **not comparable, because…**
    ("approach-b's commit changes `conductor.manifest.json`: its
    checkout ran another manifest", "approach-a changed
    `scripts/test.sh`, which this check runs", "approach-c's receipt was
    interrupted").
  - **Size**: files changed, lines added and removed, files added and
    deleted, against the base, with the list of files.
  - **Dependency changes**: the dependency files each candidate changed
    and, for `go.mod` and `package.json`, each module added, removed or
    moved to another version.
  - **Independence**: "independent", or "shares 2 commits with
    approach-b", or "approach-a's session touched a file in approach-b's
    worktree".

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
  said) and writes the rationale, which is required; the answer is
  recorded, then applied ("Applied: approach-b selected at 4b825dc, by
  Nate"). The same record shows on the ticket page's Decisions section,
  in Needs your decision (MB0) and from `conductor decision respond`. A
  candidate's result that changes before the answer (a Check again, a
  retry, an Accept anyway) replaces the open selection with one listing
  the new candidate, and the person answers that one.
- **What becomes of the others.** The candidates not selected keep their
  branches, worktrees, attempts and receipts; Conductor deletes nothing
  (as for every run: worktrees and branches are removed by hand). They
  are labelled **not selected** on the comparison, the run page and the
  ticket page; their tasks refuse Retry, Check again and Accept anyway
  after the selection, a delivery naming one is refused, and Request
  changes on one is refused.
- **Into review.** **Review the selected candidate** opens RV1's Review
  view on it; a Request changes reopens the selected task at the
  selected commit (in the run, or in the continuation that carries the
  repair), so the repair builds on exactly what was compared; the
  comparison then shows the selected approach's later candidates beside
  the selected one. A selection of **None of these** leaves the ticket
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
	Selector   string      `json:"selector,omitempty"`   // whom the selection is for: "" (the operator), "role:<r>" or "person:<name>" (PR-8)
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
  project manifest has a verify step: comparable verification needs
  something to compare;
- the **lanes**: every task of the crew is a candidate or a review task
  whose one need is a candidate (at most one review per candidate);
  every member does tasks of one lane only, so no session, no
  conversation and no member's prompt carries from one lane to another;
  a crew whose tasks do not split so is refused ("task merge needs
  approach-a and approach-b: a comparison never builds on two
  candidates");
- criteria ids distinct, texts one line, the approach without NUL;
- `selector` empty until PR-8, which admits HM1's labels.

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

**A compare crew needs a ticket.** `prepare`, called by every launch,
refuses a crew with a compare block unless it comes through
`LaunchPlanned` (a package is present): "a comparison is launched from a
ticket's approved plan revision". The approach, the criteria and the
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
- `askContext`'s blocks are filtered before they reach the task files: a
  block whose `Ref` lies under another lane's worktree path
  (`<cwd>/.conductor/worktrees/<run>/<task>.`), or names another lane's
  branch, is dropped and counted in the task file's `cut` ("1 context
  block about another lane dropped").
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
on every channel Conductor owns (messages, inputs, task files, context,
members), and what it cannot prevent it observes: shared commits (PR-2)
and touched files (PR-3) are shown as independence findings.

### `internal/crew`: the comparison measured, and the lanes settled (new files `comparison.go`, `comparedeps.go`; PR-2)

**What it measures.** `Engine.Comparison(ctx, runID) (Comparison,
error)` reads the run's crew as launched, its base, each lane's standing
candidate (the task's result; none for a task failed, cancelled or still
running) and the ledger's check rows on it, then git, and answers:

```go
type Comparison struct {
	Run, Base, Objective string
	Criteria   []Criterion
	Candidates []ComparedCandidate // in the block's order
	Checks     []ComparedCheck     // one row per check id and phase
	Settled    bool                // every lane done (compare.settled written)
}
type ComparedCandidate struct {
	Task, Member, Agent string; Args []string; Approach string
	State string                 // running, ready (a standing result), failed, cancelled
	Attempt int; Commit string    // the standing candidate, none when State is not ready
	Attempts int                  // attempts made of the task
	AcceptedAnyway bool
	Size   *DiffSize              // files, added, removed, filesAdded, filesDeleted, Files []FileStat (at most 200, Total)
	Deps   *DepChanges            // Files []string (at most 50); Modules []ModuleChange{File, Name, From, To} (at most 100); Total
	Shared []SharedCommits        // {With string; Count int; Commits []string (at most 10)}
	ManifestChanged bool          // file form: conductor.manifest.json differs from the base's blob
	Unavailable string            // words, when git could not be read; the rest of the reply stands
}
type ComparedCheck struct {
	Phase, ID string; Argv []string; TimeoutSeconds int
	Cells []CheckCell  // one per candidate: {Task, Attempt, K, State, Exit, Signal, TimedOut, DurationMS, Where, Manifest, Stable, Receipt}
	Comparable bool; Why []string // words, at most 8, when not comparable
}
```

- `Size` is `git diff --numstat -z <base> <commit> --` in the run's
  repository (argv, the C locale, through `gitcli`), with renames
  counted as one file; a binary file counts its file and no lines.
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
  reads the file at the base and at the commit (`git cat-file blob`, at
  most 1 MiB each, else listed without modules) and lists each module
  added, removed or at another version. Anything else is listed as a
  changed file only: the page never claims a dependency change it did
  not parse.
- `Shared` is, for each other lane with a candidate, the commits
  reachable from both candidates and not from the base (`git rev-list
  <base>..<a>` against `<base>..<b>`, each at most 10,000 commits, past
  that "more than 10,000" and no list): a candidate that merged or
  cherry-picked another lane's commit is not independent, and the page
  says so.
- `ManifestChanged` compares the file-form manifest's blob at the commit
  with the base's (`git rev-parse <commit>:<path>`), the crew form never
  changing.
- `Checks` takes each lane's standing attempt's rows of phase `check`
  and `verify` (the latest `K` of each id; a Check again of RV1's #620
  is a new attempt at the same commit, so it is simply the standing
  one), one row per phase and id across lanes.

**Comparability**, the rule the plan's acceptance names ("checked
alike"): a row is comparable when, for every candidate with a standing
result, its cell exists, finished (passed or failed, not interrupted,
cancelled or still running), is `Stable`, checked the candidate's own
commit (`Subject`), and every cell has the same argv and timeout, the
same `Where`, and the same manifest identity (the row's `Manifest` for a
verify step; for a task's check, the tree's `manifest.resolved`
identity, so a candidate whose checkout ran another manifest's setup is
not comparable either); and no candidate changed a file the check's argv
names (an argv entry that is a path relative to the crew's directory
naming a file at the base, looked up in one `git ls-tree` per row; at
most 32 entries, the argv bound). Each failed condition adds its words
to `Why`. A candidate without a standing result makes every row "not
comparable: approach-c has no result". Durations are shown, never
compared: checks run one at a time across the server (`checkSlots`), but
a loaded machine still varies.

**Bounds.** At most 4 candidates, 200 listed files, 50 dependency files
and 100 modules per candidate, 24 check rows (8 task checks and the
manifest's verify steps at their bound), 8 reasons a row; the reply
encoded within 256 KiB (`MaxComparison`), measured in a test at every
list's bound. Every git call runs under the request's context with a 20
s budget for the whole measure; a failed or late call fills
`Unavailable` for that candidate with git's words and leaves the other
candidates and the checks table whole. Results are cached in memory by
run, base and the candidates' commits (at most 32 entries, least
recently used out), commits being immutable; the check rows are read
from the ledger each time.

**Settling.** In the transaction that gives the last lane its end (every
candidate task and its review finished, failed or cancelled; a review's
`needs_changes` reopening its candidate is not an end), the engine
writes the run event `compare.settled` with the standing candidates
(`[{task, attempt, commit}]`, at most 4 entries, about 400 bytes). A
lane that ends again later (a retry after a failure, a repair) writes it
again with the new set; each is a ledger event of the run, so it is in
the run's history and its export. PR-6 opens the selection in the same
transaction.

### `internal/api` and `internal/cli`: the template, the comparison, the launch's statement (PR-3)

**Routes** (`compare.go`, new; registered beside the crew and run
routes, with their guide rows in `guide.go`):

| Route | Who | What | Bounds |
|---|---|---|---|
| `POST /api/crews/templates/compare` | `launching` (the admin) | a `CompareSpec` (`DisallowUnknownFields`) made a saved crew through `CompareTemplate` and the store's create; answers the crew as `POST /api/crews` does | body at most 64 KiB; `400 invalid_crew` with the rule's words |
| `GET /api/runs/{run}/comparison` | `readable` (the admin; Brakeman's principal by its shared read, PR-5) | `Engine.Comparison`, with the independence the API adds (below) and, from PR-5, the supporting part, from PR-6 the selection | reply at most `MaxComparison` (256 KiB); `404` for a run that is not a comparison |

**Independence from the touched index.** The route adds to each
candidate `Touched []CrossTouch{With string; Paths []string}` (at most
20 paths a lane): the paths the touched index
(`internal/session/touched.go`) records for any session of the
candidate's lane that lie under another lane's worktree path. The index
holds at most 2,000 entries and forgets, so the reply says "from the
touched index, which keeps the last 2,000 touches": a clean answer is
not proof of independence, and the page says so.

**The launch says how many.** DS2's plan revision reads (`GET
/api/dispatch/plans/{id}`, `GET /api/dispatch/tickets/{key}/plans`)
gain, for a revision whose crew has a compare block, `compare:
{candidates, sessions, maxAttempts, checksPerResult, reviews,
brakemanTurn}` (`sessions` the candidates and reviews; `maxAttempts`
candidates times three; `checksPerResult` the task's checks and the
manifest's verify steps; `brakemanTurn` whether the template asks a
draft), computed from the body, never stored. The launch route (`POST
/api/dispatch/tickets/{key}/launch`, `ticketLaunchBody`) gains
`candidates int`: required and equal to the revision's count for a
compare revision (`409 confirm_candidates`, "this revision starts 3
candidates: send candidates 3 to launch them"), refused for any other
revision (`400 invalid`). It is part of the request DS2's operation
fingerprints, so a resend is the same launch.

**The CLI.** `conductor crews compare SPEC.json` (or `-` for stdin)
makes a compare crew and prints its id and tasks; `conductor dispatch
comparison KEY-n [--run RUN] [--json]` prints the comparison (the newest
compare run of the ticket when `--run` is left out): the base, the
criteria, a column per candidate and the rows, with "not comparable" and
its reasons; `conductor dispatch launch KEY-n --plan rN --candidates 3`
sends the count, and without it on a compare revision prints the
statement and exits 2. Usage and completion text in `internal/cli`.

### `internal/brakeman` and `internal/api`: Brakeman's comparison draft (PR-5)

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
  under the `runs` class (`brakeman_observe.go`), with its reply bound,
  so Brakeman reads exactly what the page measures; a turn whose grant
  lacks the class is refused the read as any shared read is.
- **Queued once.** On `compare.settled` (the engine's run change,
  `OnRunChange`), when the block's `brakeman` is set, the API queues one
  Brakeman request of kind `compare` about the run, after the event's
  commit and never inside it, with the op id `compare-<run>-<seq>` (the
  event's sequence), so a restart or a second wake makes no second
  request; `by` is the launch's actor and `via` `compare`. With Brakeman
  not set up, its runs class off, or its day's budget spent, nothing is
  queued (or the request goes `over_budget`, as any request does) and
  the comparison says why.
- **Shown as supporting.** The comparison reply gains `supporting:
  {claims: [{task, attempt, summary}], reviews: [{task, attempt,
  verdict, findings}], brakeman: {request, state, why, brief}}`: the
  candidates' own result summaries and their review tasks' verdicts,
  read from the ledger, and the newest compare request's state with its
  brief's id. These are never read into `candidates` or `checks`, and
  the reply has no field that counts agreement.

### `internal/crew` and `internal/api`: the selection, a DR1 record (PR-6, waits for DR1's #634 to #637)

**Which record.** The selection is one row of DR1's `decisions` table
(`docs/plans/decisions.md`, The changes, the ledger) with its
`decision_responses` and `decision_history`: `type` `approval`, `origin`
`compare` (a third origin beside `agent` and `plan`, opened by
Yardmaster itself), no asking task, attempt or session, no gate and no
hold; the run's ticket and plan revision as DR1 fills them from the run;
`policy` `local/1`. Its request:

- `question`: "Which approach does CND-12 continue with?";
- `options`: one per lane with a standing result, in the block's order,
  its label the task and short commit ("approach-b at 4b825dc"), its
  tradeoffs the measured facts in words ("checks 2/2 passed, comparable;
  12 files, +340 −90; no dependency change; independent"), and its
  `subject` the candidate (`{kind: "candidate", run, task, n, commit}`);
  then **None of these**, with no subject;
- `recommendation` -1, always: Yardmaster never fills it, from Brakeman,
  a review or a count of agents;
- `authorizes`: "Continuing the selected candidate into review and
  repair, and delivering it; the others are kept, not selected.";
- `context`: the comparison at the opening in words, within DR1's 4,000
  characters: the base, the criteria, per candidate its commit, attempts
  and its checks' outcomes with the rows not comparable and why;
- `evidence`: the candidates' refs and the brief's when one exists (at
  most 20, DR1's bound); a check's receipt is reached from its
  candidate.

A lane without a standing result is no option; with no lane standing
there is no selection, and the comparison says "no candidate has a
result: nothing to select". With one, the record has that candidate and
None of these.

**Opening, holding the run, replacing.** In the transaction that writes
`compare.settled` (PR-2), the engine opens the selection through DR1's
writers (`Tx.OpenDecision`, its `opened` and `routed` history, the run's
`decision.opened` event), and DR1's routes, notifications and router are
told after the commit, as for any record. While it is open the run does
not settle: it is running and idle, the run page saying "waiting for the
selection dr-9", and stop ends it as any run's stop does (the selection
cancelled, "the run was stopped before dr-9 was answered"). A
transaction that gives any option's lane a new standing result (a Retry,
an Accept anyway, and RV1's Check again once #620 is on `main`) or takes
one away replaces the open selection in the same transaction: the old
one `superseded` with its words ("approach-a's result is now attempt 2
at a1b2c3d"), a new one opened with the new options and `supersedes`
naming the old, so a response to the old one is refused with DR1's words
and the person answers what is true now.

**Responding.** The operator answers through DR1's route (`POST
/api/decisions/{id}/respond`), its card, `conductor decision respond`,
or the comparison's panel, which calls the same route: a `choice` (an
option's index) and a `rationale`, required and non-empty for an
`origin: compare` record (`400 invalid`, "say why: the rationale is the
record of this selection"), at most DR1's 2 KiB. Only a principal with
the admin grant answers (DR1's `local/1`); an agent's session token,
Brakeman's principal (whose BK2 grants include no response) and a share
link are refused (`403 not_approver`).

**Applying.** The application, in its own transaction after the
response's (DR1's order), records `how: selected` with the chosen
candidate (`{task, attempt, commit}`, or none) in the application
receipt, writes the run event `compare.selected` (`{decision, task,
attempt, commit}` or `{decision, none: true}`, about 300 bytes) and lets
the run settle with its usual outcome. From then on:

- `Retry` and `Accept` of a task in a lane not selected (and RV1's
  `Recheck`, PR-8, which waits for it) are refused before any write
  (`409 not_selected`, "approach-a was not selected in dr-9: launch a
  new comparison to try it again"), so a lane not selected never gains a
  candidate the record did not see; the selected lane's tasks keep every
  command;
- a later candidate of the selected task (a repair's, RV1) does not
  touch the selection: it was of the approach, and the comparison shows
  the selected candidate and its later ones;
- DS2's delivery link (`POST /api/dispatch/tickets/{key}/links`, kind
  delivery) naming a candidate of a compare run is refused while the
  selection is open (`409 compare_open`, "select a candidate first") and
  for a candidate of a lane not selected (`409 not_selected`).

The comparison reply gains `selection: {decision, state, options,
chosen, rationale, by, for, at, applied}` read from DR1's record, and
the supporting part says, beside Brakeman's draft and the reviews, which
candidate each favoured, never next to the selection.

### `internal/api`, `internal/crew`: into review, and for whom (PR-8, waits for RV1's #627 and #631 and HM1's #645)

- **Check again on a comparison.** RV1's `Engine.Recheck` (#620) gives a
  lane a new standing result, so before the selection it replaces the
  open record as a Retry does, and after it a Check again of a lane not
  selected is refused `not_selected` before any write.
- **Request changes on a comparison.** RV1's submit and retry of a
  `request_changes` review (`dispatch_reviews.go`, #627) of a compare
  run's candidate are refused, after RV1's chain check and before the
  store write: `409 compare_open` while the selection is open or not yet
  made ("select a candidate first: a repair before the selection would
  make the candidates unlike"), `409 not_selected` for a lane not
  selected. Comment and Approve stay allowed on every candidate. A
  `request_changes` on the selected candidate is RV1's as it stands: on
  a run the server runs it reopens the selected task at the selected
  commit; on a finished run (the usual case, since the run settles at
  the selection) RV1's continuation carries the repair
  (`ContinueRunWith`, #623), and that continuation carries the crew, its
  compare block and its lanes, so the lanes' message rule holds there
  too. Nothing is rebuilt and no new launch is made.
- **For whom.** `Compare.Selector` admits HM1's labels
  (`role:<responsibility>` of the crew's people, `person:<name>`),
  checked as HM1 checks a plan decision's (#645); the selection record's
  `for` is the selector, so the card and the comparison say "for ana,
  the architecture owner", and the operator's response receipt keeps who
  answered and for whom, as HM1's do.

### `web/app`: the template, the launch's statement, the comparison, the selection (PR-4, PR-7, PR-8)

- `utils/compare.ts`: a row's words (comparable, the reasons), a cell's
  words, size and dependency words, independence words, the launch
  statement's words from the plan's `compare` part;
  `composables/useComparison.ts`: the comparison of a run, read again on
  the run's change event and on `dispatch` events naming its ticket.
- PR-4: `CrewCompareTemplate.vue` (the form above, Nuxt UI's `UForm`,
  `UInput`, `UTextarea`, `USelect` of the catalog's agents, the checks
  with the existing `CrewCheckList.vue`), reached by **New comparison**
  on `pages/crews/[[id]].vue`; `CrewCompareBlock.vue`, the block
  read-only in the crew editor, with each lane's tasks;
  `DispatchLaunch.vue` shows the statement and the button "Launch N
  candidates", sending `candidates`, with AC1's member lines below once
  they exist and the words above until then; `DispatchComparison.vue` on
  the ticket page (`?view=compare&run=…`, as RV1's `?view=review`), with
  `CompareCandidates.vue` (the columns), `CompareChecks.vue` (the rows
  with comparable words and the receipt dialog DS2 has,
  `DispatchReceipt.vue`) and `CompareMeasured.vue` (size, dependency
  changes, independence); `DispatchRuns.vue`'s card and
  `CrewRunHeader.vue` link to it; `RunTasks.vue` groups a compare run's
  tasks by lane. Until PR-7, the page says under the columns "Selecting
  a candidate arrives with durable decisions".
- PR-7: `CompareSupporting.vue` ("Supporting information, not evidence":
  claims, reviews, Brakeman's draft through the existing
  `BrakemanBrief.vue`, each with its source), in its own panel below the
  measured part and never coloured by it; **Ask Brakeman to compare**;
  `CompareSelect.vue`, which renders DR1's `DecisionRespond.vue` (#640)
  with the candidate options, the rationale field required, no option
  preselected, the record's state ("Recorded", "Applied: approach-b
  selected"), and the replacement's words; the labels "selected" and
  "not selected" on the columns, the run page's lanes and
  `DispatchCandidates.vue`.
- PR-8: **Review the selected candidate** opens RV1's Review view on it;
  `ReviewCandidate.vue`'s picker (#631) shows each compare candidate's
  label and opens on the selected one; the selection's "for" words. On a
  phone the comparison is one column: each candidate a card with its
  measured rows, then the supporting panel, then the selection;
  Brakeman's table scrolls inside its card, never the page.

### Docs

`docs/protocol.md` in the PRs that add each: the crew's `compare` field
and its rules, the task file's `compare` part (PR-1); the run events
`compare.settled` and `compare.selected` with their bounds (PR-2, PR-6);
the routes, the plan's `compare` part and the launch's `candidates`
(PR-3); the `compare` request kind (PR-5); the selection's origin and
its refusals (PR-6, PR-8). PR-8: `docs/architecture.md` (a comparison
beside a run), `docs/repository-map.md` (`compare.go`, `comparison.go`
in `internal/crew`'s row), `README.md` (the features list) and
`docs/features.md` (the entry, with the by-hand observation of
Acceptance).

## Events, orderings and invariants

**A comparison's states**, derived from the run and its records, never
stored as a state of their own: `running` (a lane has not ended) →
`settled` (`compare.settled` written; with PR-6 the selection open:
`selecting`) → `selected` (applied with a candidate) or `none` (applied
with None of these); from `selecting`, a lane's new standing result goes
back to `selecting` with a new record, and a stop or cancel of the run
to `ended` (the selection cancelled). A finished, failed or stopped run
with no `compare.settled` is `ended` too.

**Orderings.**

- O1. The block is validated at save (`ValidateSave`) and again in
  `prepare` before a run exists; a compare crew outside `LaunchPlanned`
  is refused there, before any ledger row.
- O2. The base is read once in `prepare` (the plan revision's), and
  every candidate's first attempt starts at it, having no needs.
- O3. `compare.settled` is written in the transaction that ends the last
  lane, with that lane's task row; the selection is opened in that same
  transaction (PR-6); every side effect (the router told, notifications,
  Brakeman's request) follows the commit.
- O4. Brakeman's request is queued after `compare.settled` commits,
  keyed by the event's sequence; a second wake finds the op id and
  queues nothing.
- O5. A response is recorded before it is applied, each in its own
  transaction (DR1's order); `compare.selected` and the run's settle are
  in the application's.
- O6. The transaction that changes a lane's standing result replaces an
  open selection in that transaction, never after.
- O7. `not_selected`, `compare_open` and `lane` refusals are decided
  before any write, and write nothing.

**Invariants**, each with its test (Tests per surface):

- I1. One base: every candidate's first attempt starts at the run's
  base, and no candidate task has a need.
- I2. Fixed at launch: the objective, the criteria, the approaches, the
  instructions and the checks are the plan revision's, kept in the run's
  crew row and carried by every continuation; an edit of the saved crew
  changes no run.
- I3. Lanes: no message crosses lanes; no task file names another lane's
  task, branch or worktree; no context block about another lane reaches
  a task file; no member works in two lanes.
- I4. Independence is observed, never assumed: shared commits and
  cross-lane touches are shown; their absence is said with the touched
  index's limit.
- I5. Alike: candidate tasks have identical checks; a row is comparable
  only when every condition holds, and the page never calls a row
  comparable that the server did not.
- I6. Agreement is not proof: the measured part (`candidates` and
  `checks`) is read only from git and the ledger's rows; agents'
  statements and Brakeman's draft appear only in `supporting`; nothing
  counts agreement; the selection's `recommendation` is -1 and the page
  preselects nothing.
- I7. A person selects: only a principal with the admin grant answers
  the selection; its rationale is required; Brakeman and agents cannot.
- I8. Current: a response always answers the candidates as they stand; a
  changed standing result replaces the open selection.
- I9. Nothing is deleted: candidates not selected keep their branches,
  worktrees, attempts and receipts; after the selection they gain no
  candidate, no delivery and no repair.
- I10. Continued as compared: the selected candidate is reviewed and
  repaired as its commit, in its lane, by RV1's transitions; no rebuild
  and no new launch.
- I11. Deliberate: a compare launch states its candidates, sessions,
  attempts, checks and Brakeman turn before it starts, and is refused
  unless the client sends the count.

**Schemas and migrations.** No table, column or schema version is added
to the ledger or to Dispatch. The crew's `compare` field lives in the
crew file (`crews/<id>.json`), the plan revision's body and the ledger's
`runs.crew`, each a JSON document: a crew without it encodes byte for
byte as before (a golden test). A crew file or a run with the field,
opened by an older binary, is refused by its decoder as any unknown
field is; the ledger's own data opens read-only under an older binary as
today. The selection uses DR1's tables as they ship (its `origin` and
`request` are text and JSON), so it needs no schema change of its own
once DR1 admits the origin (Owner decisions, 9).

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
  checks differing (each field of a check), no check and no verify step,
  a member in two lanes, a task outside any lane, a task needing two
  candidates, two reviews of one candidate, a review of a review,
  criteria at 12 and 13, a criterion at 500 and 501 characters and with
  a line break, an approach at 2,000 and 2,001, a selector set before
  PR-8, a block on a crew without tasks; each refused with its words,
  the good shapes taken.
- `TestCompareTemplateMakesAValidCrew`: two to four candidates, with and
  without reviews, yolo kept, each spec bound at its edge; the crew
  passes `ValidateSave` and its members and tasks are named as above.
- `TestACompareCrewNeedsATicket`: `Launch`, `LaunchNamed` and
  `LaunchHeld` refuse a compare crew before any ledger row;
  `LaunchPlanned` launches it.
- `TestEveryCandidateStartsAtTheBase` (I1): three candidates' first
  attempts start at the planned base while `HEAD` has moved.
- `TestTheComparisonIsTheLaunchedOne` (I2): the saved crew's criteria
  and approach edited after the launch; the run's crew, its task files
  and a continuation's keep the launched ones.
- `TestMessagesStayInTheirLane` (I3): a message between two lanes
  refused with `ErrLane` and no row; within a lane (candidate to its
  reviewer) delivered.
- `TestATaskFileNamesNoOtherLane` (I3): golden files
  `testdata/taskfile-compare/{candidate,review}.{md,json}` with the
  comparison part; no other lane's task, branch or path anywhere in
  them.
- `TestContextFromAnotherLaneIsDropped` (I3): a fake context function
  answering a block under another lane's worktree and one under the
  attempt's own; the first dropped and counted in `cut`, the second
  kept.
- `TestTheComparisonPartIsNeverCut`: a task file at `maxTaskFile` cuts
  its other parts and keeps the comparison part whole.
- `TestACrewWithoutCompareIsUnchanged`: every crew of `Examples` and the
  existing fixtures encodes byte for byte as before.

**`internal/crew` (PR-2, `comparison_test.go`, `comparedeps_test.go`)**,
with real git repositories under `t.TempDir()`:

- `TestComparisonMeasuresEachCandidate`: numstat counts, renames, a
  binary file, files added and deleted, the list cut at 200 with its
  total.
- `TestDependencyChangesAreRead`: `go.mod` single and block `require`
  lines added, removed and moved; `package.json`'s four maps; a lockfile
  only listed; a `go.mod` over 1 MiB listed without modules; a candidate
  changing none.
- `TestSharedCommitsAreFlagged` (I4): approach-a cherry-picks a commit
  of approach-b's branch, then merges it; each case reported with its
  count; two independent candidates report none.
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
- `TestComparisonBounds`: every list at its bound and one past it; the
  encoded reply within `MaxComparison`.
- `TestAGitFailureLeavesTheRestWhole`: a candidate commit made
  unreadable; that candidate `Unavailable` with git's words, the others
  and the checks table whole; a measure past its 20 s budget (a fake
  clock through the git seam) likewise.
- `TestCompareSettledIsWrittenOnce`: written in the last lane's
  transaction (read from the ledger with that task row), not before; a
  failed lane counts as ended; a review's `needs_changes` delays it; a
  later retry's end writes it again with the new set.

**`internal/api` (PR-3, `compare_test.go`)**: `TestCompareTemplateRoute`
(made, refused with words, an unknown field refused, the body bound);
`TestComparisonRoute` (its shape, `404` for a run that is no comparison,
the admin and the share link's refusal, the reply bound);
`TestATouchInAnotherLaneIsFlagged` (I4, a touched entry of approach-a's
session under approach-b's worktree; and the index's limit words when
none); `TestThePlanReadStatesTheMultiplier`;
`TestACompareLaunchSaysHowMany` (I11: no `candidates`, a wrong count,
the right one; a resend with the same body answered as the same launch;
`candidates` on an ordinary revision refused); `TestComparisonGuideRows`
(the guide's operations and the tools golden).

**`internal/cli` (PR-3)**: `TestCrewsCompare` (a spec file and stdin),
`TestDispatchComparison` (text and `--json`, the newest compare run when
`--run` is left out), `TestDispatchLaunchCandidates` (the statement
printed and exit 2 without the count).

**`internal/brakeman` and `internal/api` (PR-5)**:
`TestCompareRequestTakesACompareRun` (a `run:` ref of a compare run;
another run, another ref kind or two subjects refused);
`TestACompareBriefNeedsItsTable` (columns in the block's order; each of
the five labels missing in turn; a `decision/1` and a lane command's
`proposal/1` refused; the kind table test updated so the guide and
`brief_submit`'s schema say the rule);
`TestSettledComparisonQueuesOneBrakemanRequest` (once per
`compare.settled`, after its commit, the op id; none with the block's
`brakeman` off, Brakeman not set up or its runs class off; over budget
recorded as such; a restart queues no second);
`TestBrakemanReadsTheComparison` (the shared read under the runs class,
its bound); `TestTheBriefIsSupportingOnly` (I6, below, the part this PR
can show).

**`internal/crew` (PR-6, `compare_select_test.go`)**:
`TestSettledComparisonOpensItsSelection` (one record, origin compare,
options the standing candidates in order with their subjects and None of
these, recommendation -1, the context's words, the evidence refs, no
hold; none when no lane stands; one candidate and None when one does);
`TestAnOpenSelectionKeepsTheRunOpen` (the run running and idle; stop
cancels the selection with its words);
`TestANewResultSupersedesTheSelection` (I8: a Retry of a failed lane, an
Accept anyway; the old record superseded and the new one opened in the
same transaction; a response to the old refused);
`TestTheSelectionIsApplied` (how selected with the candidate,
`compare.selected`, the run's outcome; None of these likewise);
`TestUnselectedLanesRefuseCommands` (I9: Retry and Accept refused
`not_selected` with no write; the selected lane's commands kept);
`TestARepairOfTheSelectedLaneKeepsTheSelection` (I10, the engine's half:
a reopen of the selected task changes nothing of the record).

**`internal/api` (PR-6)**: `TestOnlyTheOperatorSelects` (I7: an agent's
session token, Brakeman's principal through its act route and a share
link with control all refused `not_approver`; the workbench token
accepted); `TestASelectionNeedsARationale` (I7);
`TestAgreementIsNeverEvidence` (I6: a fixture where both candidates'
summaries and both agent reviews favour approach-a, Brakeman's brief
recommends it, and approach-a's `test` receipt failed; the reply's
measured part shows approach-a's failed cell and nothing derived from
the statements, the statements appear only under `supporting`, the
record's recommendation is -1 and its context names the failed check);
`TestADeliveryNamesTheSelectedCandidate` (I9: `compare_open`,
`not_selected`, the selected one taken);
`TestTheComparisonShowsTheSelection`.

**`internal/api` and `internal/crew` (PR-8)**:
`TestCheckAgainSupersedesTheSelection` (RV1's Check again on a lane
before the selection replaces the record; on a lane not selected after
it, refused `not_selected`); `TestRequestChangesWaitsForTheSelection`
(`compare_open` on submit and retry; Comment and Approve taken);
`TestRequestChangesOnAnUnselectedCandidateIsRefused` (`not_selected`);
`TestTheSelectedCandidateIsReviewedAsIs` (I10: a Request changes on the
selected candidate of a finished compare run makes RV1's continuation,
which reopens the selected task at the selected commit, carries the
compare block, and still refuses a message across lanes);
`TestTheSelectionIsForTheSelector` (HM1's `for` from the block, the
receipt's who and for whom); `TestSelectorLabels` (in `internal/crew`,
PR-8: a role and a person admitted as HM1 admits them, others refused).

**Web, vitest**: `utils/compare.test.ts` (each reason's words, sizes,
dependency and independence words, the launch statement for 2 and 4
candidates with and without reviews and Brakeman) and
`useComparison.test.ts` (read again on the run's event, one fetch at a
time) in PR-4; `compareSelect.test.ts` in PR-7 (no option preselected
whatever `supporting` says; the rationale required before Send; the
measured and supporting panels kept apart: a cell's colour read only
from `checks`); `compareReview.test.ts` in PR-8 (the picker's labels and
its default).

**Playwright** (each run alone first while iterating, then `make
test-e2e`; the e2e stub agent gains `STUB_COMPARE_EDIT`, which makes one
lane's stub edit the file-form manifest or a check's script, so a "not
comparable" row is drawn for real):

- `compare.spec.ts` (PR-4): New comparison with two candidates and a
  check; propose and approve the revision; the launch statement and
  "Launch 2 candidates"; two stub candidates finish; the comparison
  shows the base, the criteria, both columns, the checks rows (one not
  comparable with its words), size and dependency rows; a message across
  lanes refused in the run page's messages; the crews page's Launch
  refused with its words.
- `compare-select.spec.ts` (PR-7): Brakeman's stub brief arrives in the
  supporting panel; the selection panel shows no choice made; Send is
  disabled without a rationale; selecting approach-b with a rationale
  shows Recorded, then Applied; the columns and the run page say
  selected and not selected; Retry on approach-a's task refused with its
  words; an Accept anyway of a failed lane before the selection (a
  second comparison in the spec, one stub failing its check) replaces
  the selection and the panel says so. After a mode switch and the
  page's own redraw, the labels are checked again.
- `compare-review.spec.ts` (PR-8): Review the selected candidate opens
  the Review view on it; a Request changes on approach-a is refused with
  `not_selected`'s words; a Request changes on approach-b makes the
  repair, whose new candidate shows in the comparison under the selected
  one.

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
Brakeman whole). PR-6 waits for DR1's ledger tables (#634, PR #664), its
engine (#635, PR #672; #636, PR #678) and its routes (#637), and for the
decisions plan to admit what Owner decisions 9 names; PR-7 also waits
for DR1's card (#640); PR-8 waits for RV1's review routes (#627) and
Review view (#631), and for HM1's whom-a-decision-is-for (#645).

| PR | Title | Contains | Touches | Tests | Depends on | Parallel with |
|---|---|---|---|---|---|---|
| PR-1 | crew: the compare block, its lanes and the template | `Compare`, `Criterion`, `Candidate` on `Crew`, their validation and bounds, `CompareTemplate` and `CompareSpec`, the refusal outside `LaunchPlanned`, `run.lanes`, the message refusal (`ErrLane`, the route's `409 lane`), the context filter, the task file's comparison part and its golden files; `docs/protocol.md`'s crew field and task file part | `internal/crew`, `internal/api` (the message route's code only), `docs/protocol.md` | the PR-1 tests above | none | none |
| PR-2 | crew: the comparison measured, and the lanes settled | `Engine.Comparison` and its types, numstat, `dependencyFiles` and the `go.mod` and `package.json` readers, shared commits, the manifest's blob, the check rows and the comparability rule, the bounds and the git budget, the cache, `compare.settled`; `docs/protocol.md`'s event | `internal/crew`, `docs/protocol.md` | the PR-2 tests above | PR-1 | none |
| PR-3 | api, cli: the template and comparison routes, the launch's statement | `POST /api/crews/templates/compare`, `GET /api/runs/{run}/comparison` with the touched index's independence, the plan reads' `compare` part, the launch's `candidates` and `confirm_candidates`, the guide rows, `conductor crews compare`, `conductor dispatch comparison`, `--candidates`, usage and completion; `docs/protocol.md`'s rows | `internal/api`, `internal/cli`, `docs/protocol.md` | the PR-3 tests above | PR-2 | none |
| PR-4 | web: the template form, the launch's statement, the comparison page | `utils/compare.ts`, `useComparison`, `CrewCompareTemplate`, `CrewCompareBlock`, `DispatchLaunch`'s statement and button, `DispatchComparison`, `CompareCandidates`, `CompareChecks`, `CompareMeasured`, the run card's and header's links, `RunTasks`' lanes, the stub's `STUB_COMPARE_EDIT`, `compare.spec.ts` | `web/app/components`, `web/app/composables`, `web/app/utils`, `web/app/pages`, `web/e2e` | vitest (`compare.test.ts`, `useComparison.test.ts`), `compare.spec.ts` | PR-3 | PR-5 |
| PR-5 | brakeman, api: Brakeman's comparison draft | the `compare` request kind, `CheckCompareBrief` and its kind-table rule, the guide's words, the comparison in `brakemanReads`, the request queued once on `compare.settled`, the reply's `supporting` part; `docs/protocol.md`'s request kind | `internal/brakeman`, `internal/api`, `docs/protocol.md` | the PR-5 tests above | PR-3 | PR-4 |
| PR-6 | crew, api: the selection as a decision record | the `origin: compare` record opened with `compare.settled`, its options bound to candidates, the run held open while it is open, its replacement on a new standing result, the required rationale, the application `how: selected` and `compare.selected`, `not_selected` on a lane's commands, the delivery's `compare_open` and `not_selected`, the reply's `selection`; `docs/protocol.md`'s rows | `internal/crew`, `internal/api`, `docs/protocol.md` | the PR-6 tests above | PR-5; waits for DR1's #634, #635, #636 and #637 and for Owner decisions 9 in the decisions plan | PR-4 |
| PR-7 | web: the supporting panel, Brakeman's draft and the selection | `CompareSupporting`, Ask Brakeman to compare, `CompareSelect` over DR1's `DecisionRespond`, the selected and not selected labels on the comparison, the run page and `DispatchCandidates`, the stub brief, `compare-select.spec.ts` | `web/app/components`, `web/app/composables`, `web/app/utils`, `web/e2e` | vitest (`compareSelect.test.ts`), `compare-select.spec.ts` | PR-4, PR-6; waits for DR1's #640 | none |
| PR-8 | api, crew, web, docs: into review, for whom; the features entry | Check again's replacement and refusal, RV1's `request_changes` refusals (`compare_open`, `not_selected`), the continuation's lanes, `Compare.Selector` with HM1's labels and the record's `for`, Review the selected candidate, the Review view picker's labels and default, the phone layout; `docs/architecture.md`, `docs/repository-map.md`, `README.md`, `docs/features.md` | `internal/api`, `internal/crew`, `web/app/components`, `web/app/utils`, `web/e2e`, `docs`, `README.md` | the PR-8 tests above, `compareReview.test.ts`, `compare-review.spec.ts` | PR-7; waits for RV1's #627 and #631 and HM1's #645 | none |

Sizes, as the owner asked (2026-11-05: a row is a batch, 300 to 800
changed lines, at most about 1,500 with its tests): PR-1 about 650 lines
and 750 of tests; PR-2 about 700 and 700; PR-3 about 650 and 600; PR-4
about 800 and 450; PR-5 about 400 and 500; PR-6 about 600 and 750; PR-7
about 600 and 400; PR-8 about 550 and 550, with the docs.

## Owner decisions

Dated 2026-11-06. Each is a default the owner can overturn with a dated
row in the Program's status log, its convention for a decision the owner
did not make (the Program, section 3.1); the reasoning is why it was
taken.

1. **The open decisions named with this item.** O4 (the human policy SW1
   ships first) is SW1's: this item records whom the selection is for
   with HM1's labels and lets the operator answer, taking no position on
   roles, groups or delegates. O5 (the graph's tooling) is not touched:
   a context block from a graph provider is filtered by lane like any
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
5. **Lanes are enforced on Conductor's channels and observed
   elsewhere.** Messages, inputs, task files, context and members never
   cross lanes; shared commits and cross-lane touches are shown as
   findings. Chosen over claiming isolation the worktree does not give
   (the Program, section 1.5) and over an operating-system sandbox,
   which is Boxcar's (Program item F).
6. **Comparable means the same check, manifest, place, commit and an
   intact finished run, and an unchanged check script**; anything else
   is said not comparable with its reason, never hidden and never
   failed. Durations are shown and never compared.
7. **Measured and said are two panels.** The measured part reads git and
   the ledger only; agents' summaries, reviews and Brakeman's draft are
   supporting information in their own panel; no agreement is counted;
   nothing is preselected. Chosen over a single scored table, which
   would turn agreement into apparent evidence.
8. **Brakeman drafts once, when asked by the template or a person.** One
   request when every lane is done, on the person's own Brakeman account
   and budget, stated at the launch; none when Brakeman is off. Its
   brief must carry the five rows; it may not carry a decision card or a
   lane command.
9. **The selection is a DR1 record of a third origin, `compare`**,
   opened by Yardmaster in the transaction that settles the last lane,
   its options bound to exact candidates, its recommendation always -1,
   its rationale required, holding the run open while it is open,
   replaced when a candidate changes, applied as `how: selected`; a
   later candidate of the selected task does not supersede it. DR1's
   text as merged has the origins `agent` and `plan` and options without
   subjects, cancels every open decision when a run settles, makes a
   rationale optional, and supersedes an approval of a candidate when
   its task gets another; each of these needs the decisions plan to
   admit this origin's rule (this plan's result names each as a finding
   for that plan). Chosen over a gate in the plan revision, which opens
   at launch, before any candidate exists, with Approve and Reject as
   its only answers, and over a record of this item's own, which the
   Program gives to DR1.
10. **Nothing is deleted.** Candidates not selected keep their branches,
    worktrees, attempts and receipts, labelled not selected; after the
    selection they gain no attempt, delivery or repair. Chosen over
    removing them, which Conductor does for no run today and which would
    destroy the evidence the selection rests on.
11. **The selected candidate is reviewed as its commit, in its lane.**
    RV1's Request changes on it reopens the selected task (or RV1's
    continuation carries the repair); a repair before the selection, or
    of a lane not selected, is refused. Chosen over letting a person
    repair any candidate, which would make the candidates unlike before
    the choice and spend work on approaches not taken.
12. **None of these** is always an answer; it ends the comparison, and
    the ticket's next step is DS2's Revise and a follow-up launch.
13. **The launch states its multiplier and must repeat it.** The
    statement (candidates, sessions, attempts, checks, reviews, a
    Brakeman turn) is this plan's; usage per account is AC1's, shown
    below it once AC1's PR-7 merges, and said to be absent until then
    (this plan follows the capacity plan's final text on PR #668).
    Chosen over a confirmation dialog alone, which a script or a second
    client would not see.
14. **When the code starts.** Now for PR-1 to PR-5; PR-6 to PR-8 as
    their named rows merge. CP1's Status follows the Program's rules: In
    progress at its first PR, In review when the last is ready, Done
    when the boundary below is observed.

No question is left open: each answer above lets the plan stand, and the
owner overturns any of them with one row. What other plans must admit is
named in this plan's result as findings for those plans, each to be
adopted or declined on its own.

## Acceptance

The boundary, in this plan's words: two candidates for one ticket are
checked alike and one is selected, by a person, with the rationale
recorded; agreement among agents is never taken for proof. It is
observed, not ticked:

- Once PR-8 is merged, on the owner's `conductor serve` with real agents
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
  lane; a message tried across lanes is refused.
- The comparison shows both candidates' receipts for the same check,
  comparable, each linked to its log with its SHA-256; one row made not
  comparable by changing a check's script in one candidate (by hand, in
  a second comparison of the same ticket) says why.
- Brakeman's draft arrives in the supporting panel with the five rows
  and its facts cited; the measured part is unchanged by it; nothing is
  preselected.
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
  `docs/features.md` (PR-8, or a later docs PR of the stream when it
  comes after PR-8 merges); a bug the stream caused is fixed in the
  stream, a defect outside it filed.
- Done means: every PR merged and the observation recorded with the
  owner's word that the comparison showed what was compared, on which
  commits, with which checks, and why the chosen one was chosen; the
  Program's status row moves to Done then, never on the table alone. It
  is part of the R3 gate's free items (the Program, section 6).

What needs the owner: the review of this plan and its decisions; the
decisions plan's answer to the findings this plan names; cutting the
eight issues; the observation, whose agents run on the owner's
subscription; the merges.
