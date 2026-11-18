# A decision router's policy on decision records (SW1's open-source rows, issue #997)

Status: proposed 2026-11-18, for the owner's review before any of its
code. The owner sets it to Planned by approving it; its rows (PR-7a,
PR-7, PR-7b, PR-8, PR-9a and PR-9, The PRs) are units of work, grouped
into pull requests as `AGENTS.md` (Working an issue) says at the time
they are built.

Today a durable decision (DR1, `docs/plans/decisions.md`) has one
policy, `local/1`: one response from the operator, or from a holder of
the decide grant when the operator has given it the decision
(`docs/plans/decide-grant.md`). A registered decision router
(`ext.DecisionRouter`, C5, `docs/plans/ext.md`) is told of each
record, may name where it went, and that is all: its destinations are a
`routed` history row, and an answer it holds is refused (DR1's Owner
decisions, 8: a later item brings the policy that lets a routed person
answer). This plan is that policy's seam. A registered router may
answer a **policy** for a decision record: one or more groups, each
with a quorum and the persons eligible to fill it, a rule line that
explains each, and whether the server's operator may override. The
server then lets only those persons answer, asks the router to
**confirm** the policy at each response and again at the application,
falls back to `local/1` whenever the router cannot confirm it, counts
approvals until each group's quorum is met, keeps room in every bound
for what the policy still requires, and keeps every router-policy
decision's outcome until the router has taken it. With no router
registered, nothing here runs and every decision is answered as today.

Switchman, the team routing of decisions (Program item SW1), is one
such router; this plan is SW1's open-source part, written in public
before its code, as the repository requires. It says what the
open-source build does for **any** registered decision router, and
nothing of how a router decides or what it does with what it is told:
that is the business of the module that registers the router.

It was written against `main` at `f10eb297` (2026-11-18), after
reading `ext/decisions.go`, `ext/host.go`, `ext/notify.go`,
`ext/identity.go`, `ext/exttest/fakes.go` and `ext/exttest/checks.go`;
`internal/api` (`router.go`, `extensions.go`, `exthost.go`,
`decisions.go`, `decide.go`, `inbox.go`, `inbox_decisions.go`,
`lane_decisions.go`, `capabilities.go`); `internal/crew`
(`decisions.go`, `decisions_admit.go`, `decisions_wait.go`,
`decisions_for.go`) and `internal/crew/ledger` (`ledger.go`,
`decisions.go`, schema version 12); `internal/cli/decision.go`;
`web/app` (`components/DecisionCard.vue`,
`components/DecisionRespond.vue`, `utils/decisions.ts`,
`composables/useDecide.ts`, `pages/decide/`); and the plans it meets:
`docs/plans/decisions.md` (DR1 and HM1), `docs/plans/decide-grant.md`,
`docs/plans/decision-inbox.md` (MB0) and `docs/plans/ext.md` (C1 to
C9).

**Where this starts: on `main` today.**

- C5, the decision router seam: `Route` told of a record
  (`Decision.Request` set, `Kind: "decision"`), answered within
  `ext.RouteWait` (5 s) through the guard, which answers the empty
  route for a call that is late, fails, panics or is invalid;
  `Resolved` told of a record's end best-effort, never owed; and
  `Host.Decision` to read a record's state. The ledger's owner tells
  the router of every open record by a **pass** (at its taking of the
  ledger, at the provider's start, and after a tell that failed or was
  not made), so a router may be told of one record more than once.
  Records are not counted against `maxUnresolved` (the bound of the
  sessions' questions held in memory): every open record is told.
- DR1 with every row: the record, its `policy` column (`{revision:
  "local/1", approver: "operator", quorum: 1, authority: [{kind:
  "operator"}]}`, at most `ledger.MaxDecisionPolicy`, 1 KiB, never
  written after the opening), its routes, its rechecks at response and
  application, its bounds (1,000 decisions per run, a replacement's
  place among them; 32 response rows of which 31 refused; 256 history
  rows), and the router's `routed` row (`Engine.RecordRoute`, once per
  decision).
- HM1: a crew's people as local labels, the four responsibilities
  (architecture, product, security and acceptance owner) and a
  decision's `for` (`{label, person, responsibility}`), written only by
  `ledger.Tx.RouteFor` while the decision is open.
- The decide grant (`ext.GrantDecide`, issue #776): a decide
  principal's three decision routes (`GET /api/me/decisions`, the decide
  view of `GET /api/decisions/{did}`, `POST
  /api/decisions/{did}/respond`), judged by one function,
  `crew.AdmissionOf`, over the record's **entries** (`crew.EntriesOf`:
  the policy's `authority`, and the grant's entry while the record is
  given to the grant). Its comment says a later kind of entry adds its
  rule to `PolicyEntry.admits` and nowhere else: this plan adds one.
- MB0's decision page and its read model (`routedTo`, `routedRule` on
  an item, `internal/api/inbox.go`).

The names follow the railroad: Conductor is the application, Yardmaster
the harness that owns a decision's wait and applies its answer,
Switchman the routing a decision router provides. "The operator" is
the admin grant, as DR1 has it: the workbench token, or an identity
provider's principal holding `ext.GrantAdmin`.

## Articles it serves

The articles are the Program's (section 2.1).

1. **No paid code, licence check or plan check.** Every change here is a
   seam any registered router uses. The one question the server asks
   is whether a decision router is registered, and whether it
   implements the new optional interface (`ext.PolicyConfirmer`); the
   route to the router answers `409 no_router` when none is, a
   registration check like `no_identity`.
2. **The free build is fully useful.** A plain build registers no
   router: every record opens and stays under `local/1`, the operator
   answers every decision, the decide grant works as on `main`, and no
   new route, field or row changes what anyone sees. The new reply
   field `policyNow` is absent and no outbox row is written.
3. **Gated by hosted dependencies.** Nothing here is a flag, a setting
   or a tag: a policy exists only when a registered router answers one,
   and is in force only while that router confirms it.
4. **Stop, inspect, revoke, export and required approvals stay free.**
   No code here answers a decision, lowers a quorum or ends a wait by a
   timer. Whenever a router's policy is not in force, the operator
   answers under `local/1`. Under a policy in force, the operator can
   override where the policy allows it, and can always stop the run or
   cancel the decision. Export carries the new rows unchanged.
5. **Dependencies point one way.** `ext` stays standard library only;
   no import changes.
6. **No feature build tag.** None added.
7. **The free default is what `main` does.** With no router, every
   path here is `main`'s, byte for byte: the tests of DR1, HM1, MB0 and
   the decide grant pass unchanged.
8. **A router that cannot answer is the free default, mid-session.** A
   `Confirm` that is late, fails, panics, is invalid or finds no call
   slot is "not in force": the operator answers under `local/1`, the
   receipt says why, and the record's outcome is kept for the router to
   report. A router that comes back routes the records opened meanwhile
   through `Host.Reroute`, with no restart.
9. **CGO off.** No change; the new table is in the ledger
   (`modernc.org/sqlite`).
10. **The repository contract.** This plan before its code; race tests
    for every new path, a refusal tested beside each success; vitest
    and Playwright for the app's rows; `docs/protocol.md` for every
    route and field. No WebSocket frame changes (below), so
    `internal/proto` and `web/app/utils/protocol.ts` are unchanged: the
    routes' types and the decide page's words are in
    `web/app/utils/decisions.ts`.
11. **Names keep their meaning.** A router routes; Yardmaster applies;
    a "delegate" stands beside a person, never in their place; nothing
    here is called "continuity" or "sync".
12. **Execution behind one seam.** Not touched: nothing here starts a
    process or routes a task.

## Why and what happens

On `main` a decision a crew's work waits on has one approver, the
operator, or anyone the operator gives it to through the decide grant.
That is right for one person with agents and wrong for a team: the
schema change should be the architecture owner's to approve, the login
change the security owner's, and nobody else's. Who holds those roles,
and where that is kept, is the router's; what the server must give a
router is a way to say who may answer one record, and a guarantee that
only they do while the router stands behind it.

After this plan, on a server with a decision router registered that
implements `ext.PolicyConfirmer`:

- **A decision reaches the persons its policy names.** A run's gate
  `schema`, for `role:architecture`, opens as dr-7. The server tells
  the router, which answers within `ext.RouteWait` with a policy: one
  group, the architecture owner, quorum 1, Ana eligible (`via: role`),
  rule `r-1` with its line, override `operator`. The server writes it
  into dr-7's record. The operator's card says "For the architecture
  owner: Ana (rule r-1: architecture decisions in CND)", MB0's page
  shows "routed to Ana", and Ana's decide page lists dr-7.
- **Only the eligible answer.** Ana answers Approve through the decide
  page. The server asks the router to confirm, with her subject, before
  the response's transaction: eligible, in the architecture group. The
  receipt says "by Ana (architecture owner, rule r-1)". At the
  application the server asks again, beside DR1's identity recheck,
  and Yardmaster applies the answer once; `build` starts. Ben, a decide
  principal the policy does not name, does not see dr-7 in his list,
  reads `404`, and a response is refused. A share link's controller,
  a decision link, an agent and Brakeman are refused as on `main`.
- **Nobody but the policy chooses.** Under a policy in force, the
  operator's route of dr-7 to a person, a role or the decide grant
  (HM1's route, Brakeman's `route_decision` through it, the grant's
  route) is refused `409 routed_by_policy`. An agent's `for` naming a
  person, and a route made before the router was told, reach the
  router as recommendations and add nobody.
- **The operator overrides only explicitly.** The operator's card says
  "This is Ana's to answer" and, where the policy allows it,
  Override…, which needs a written reason of at least 10 characters and
  is recorded as an override with its subject.
- **A gap is kept, never filled.** A policy whose group has nobody
  eligible carries the gap's words ("Waiting for an architecture owner:
  none is assigned in CND"). The decision stays open until the router
  re-routes it to someone or the operator overrides.
- **Silence is never approval.** Nothing on the server answers, expires
  or lowers a requirement. The router may change who is eligible
  (`Host.Reroute`), never what is required.
- **Several approvals where the policy asks for them** (PR-9). A policy
  of two groups (architecture and security, quorum 1 each, distinct
  persons) keeps dr-8 open after Ana's approval, "1 of 2"; Cam's
  approval for security completes it; one eligible Reject rejects it at
  once.

**When the router cannot stand behind its policy** (it is not
started, it fails, it is late, it says not in force):

- A decision opened then stays `local/1`: its card says "Who may answer:
  the operator", and the operator answers with the workbench token as
  in the free build.
- A decision routed before keeps its policy in its history, and its
  card says "Routed to Ana: not in force now (the router's why); the
  operator answers". Ana's response is refused; the operator answers
  without an override, recorded under `local/1` with that why, and the
  record's outcome is kept in the ledger's outbox until the router has
  taken it, so the router can report how its decision was answered.
- When the router stands behind its policy again, the decisions opened
  meanwhile and still open are routed then (`Host.Reroute`), with no
  restart.

A policy binds only while its router confirms it. An operator who
stops the router from confirming (stops it, or keeps it from answering)
answers its decisions under `local/1`, and nothing here prevents that:
article 8 asks for exactly this fallback. What the server promises is
that every such answer is recorded as one, with its why, and kept for
the router.

## The policy, as the server knows it

What a router sends and the server keeps. The server checks every
bound and every relation below; it never interprets the rule ids, the
role names, the `via` refs or the `Scope`, which are the router's.

- A **group** is a requirement: a role (1 to 40 of `a-z`, `0-9` and
  `-`, any name of that form: the four responsibilities HM1 names, or a
  router's own), its label for people ("architecture owner"), a quorum
  of 1 to 4, the id of the router's rule that made it, and its
  **eligible** persons, 0 to 16. A group with nobody eligible carries
  its **gap**, one line of at most 200 bytes, and is kept: never
  dropped, never answered.
- An **eligible** person is a principal's subject (`ext.Principal`'s,
  at most `ext.MaxSubject`), the name to show, how they are eligible
  (`via`: `assigned` by name, the role's holder `role`, a `delegate`
  standing in beside someone, a `backup` added later), for whom when a
  delegate, and the router's ref for it (at most 32 bytes). At most 32
  eligible across the groups. Only an identity provider's principal
  can be one: the workbench token is the operator, never a subject.
- `distinct`: one person may fill only one group (PR-9).
- `override`: `operator` (the server's operator may answer in the
  policy's place, explicitly) or `none`.
- The **pinned requirement** (`Pinned`) is what must be answered and
  when anyone is told: the rule's id, the groups' roles and quorums in
  order, `distinct`, `override`, `notify` (`now` or `batch`) and
  `escalateAfter` (0, or 15 minutes to 7 days). It is written with the
  first policy and kept for the record's life: no later route may
  change it (I4, I13). The server stores `notify` and `escalateAfter`
  and acts on neither; they are kept so that a router reads them back
  from the record, never from its own state.
- The **groups** are the pinned requirement's groups (the same roles
  and quorums, in order), or, when the router cannot route the whole
  requirement, the **unsupported form**: one group of quorum 1 with
  nobody eligible and its gap, flagged `unsupported`. The one change of
  form a re-route may make is from the unsupported form to the
  requirement's own groups.
- The **revision** (1 to 40 bytes of printable ASCII) is the router's
  name for who may answer: two policies with the same revision are the
  same policy. A response is judged against the revision it was made
  under (I14).
- The **scope** (1 to 64 bytes of printable ASCII) is the router's own
  name of whatever it routed the record for. The server stores it,
  returns it with the policy and never reads it; a re-route may not
  change it.
- **Reroutes** and **Recent**: the count of changes since the first
  route, and the latest 16 of them past the history's 16, each `{why,
  revision, at}`. They are the server's: a router sends them empty
  (one sent otherwise is `invalid`) and reads them back with the
  record (`Host.OpenDecisions`, `Host.Outcomes`), as it reads
  `RoutedAt`.

At most 14 KiB as encoded, `Scope` and `Recent` included.

## The changes

### `ext` (PR-7)

Additive throughout: a router built against C5's records compiles and
behaves unchanged, and a router that does not implement
`PolicyConfirmer` gets `main`'s behaviour whatever it answers (a
`Route.Policy` from it is refused by the guard as `invalid`).

- `decisions.go`: `Decision.Record *DecisionRecord`
  (`json:"record,omitempty"`), set only for a durable record
  (`Request` set), never for a session's question:
  - `Type` (`clarification`, `approval`, `assignment`), `Origin`
    (`agent`, `plan`, or `compare` for a comparison's selection, which
    only the operator answers: a router may route it for the record's
    history, and any `Policy` it answers for it is written nothing for),
    `Gate` (at most 40 bytes), `Ticket` (a ticket's key), `Project` (its
    project's key), `Crew` (at most `ext.MaxCrewID`), `Task` (at most 40
    bytes);
  - `For` (`{Label, Person, Responsibility}`, HM1's, the label at most
    60 characters), and `Origin` says whose it is: a plan's is the
    approved revision's, an agent's is the requester's own, so a router
    can tell an assignment a person approved from a recommendation an
    agent made;
  - `Expertise` (at most 4 labels of at most 40 characters, DR1's);
  - `Subject` (`{Kind, Key}`: DR1's subject kind, and `Key`, the first
    32 hex digits of the SHA-256 of DR1's canonical JSON of the subject:
    the commit; the candidate's run, task, attempt and commit; the
    artifact's path and digest; the plan revision). A router tells two
    subjects apart, and finds one subject asked about twice, without
    holding a path. `DecisionSubject.Valid` checks the kind and 32
    lower-case hex digits;
  - `Recommended` (at most 4 `Destination`s: the `routed` rows written
    before the router was told, by the operator or by Brakeman).
- `Route.Policy *RoutePolicy` (`json:"policy,omitempty"`), valid only
  with a `Record`:
  `RoutePolicy{Revision, Scope, Pinned PinnedRequirement, Groups
  []Requirement, Distinct bool, Override string, Unsupported bool,
  Reroutes int, Recent []RouteChange}`, with
  `Requirement{Role, Label, Quorum, Rule, Eligible []Eligible, Gap}`,
  `Eligible{Subject, Name, Via, For, Ref}`,
  `PinnedRequirement{Rule, Groups []PinnedGroup{Role, Quorum},
  Distinct, Override, Notify, EscalateAfter time.Duration}` and
  `RouteChange{Why, Revision, At}` (each at most 96 bytes as encoded),
  every bound of The policy, above. `Distinct` and `Override` equal
  `Pinned`'s; `Groups` are `Pinned.Groups` (roles and quorums, in
  order) or, with `Unsupported`, the unsupported form. A `Name` is at
  most 64 characters, a `Label` 1 to 60, a `Rule` and a `Ref` at most
  32 bytes, a `Gap` one line of at most 200 bytes and required exactly
  when `Eligible` is empty. `RoutePolicy.Valid(Record)` checks all of
  it and the 14 KiB.
  Constants: `MaxPolicyGroups` (4), `MaxGroupQuorum` (4),
  `MaxGroupEligible` (16), `MaxPolicyEligible` (32), `MaxPolicyRecent`
  (16), `MaxPolicy` (14 KiB), `MaxPolicyScope` (64), `MaxPolicyRevision`
  (40).
- `PolicyConfirmer`, an optional interface a `DecisionRouter`
  implements:
  `Confirm(ctx, ConfirmQuery{Decision Decision, Revision string,
  Subject string, Since time.Time}) (Confirmation, error)`, answered
  from what the router holds, never a round trip, within `ConfirmWait`
  (1 s, as `IdentityWait`). `Subject` is "" when the server asks only
  whether the policy is in force. `Since` (a counted response's time,
  PR-9) asks whether the subject has stayed eligible, without a break,
  since then; a router that cannot establish it answers not eligible.
  `Confirmation{InForce bool, Behind bool, Routes bool, Revision
  string, Groups []string, Why string}`:
  - `InForce`: the router stands behind its policy for this record now;
  - `Behind`: in force, but its own state is being refreshed: the
    server answers "try again", within the bound of I10;
  - `Routes`: the record has no router policy yet, and the router will
    route it now (after a restart, a record it reads on its start);
  - `Revision`: the router's current revision when it differs from the
    one asked about;
  - `Groups`: every group role the subject is eligible in now, at most
    `MaxPolicyGroups`; none for none;
  - `Why`: one line of at most 200 bytes, shown on the card.
  `Confirmation.Valid` checks it. Through the guard, a late, failing,
  panicking or invalid answer, or one with no call slot free, is
  `{InForce: false, Why: "the router did not answer"}`. `MaxBehind`
  (120 s) is the interface's promise, written on it: a router answers
  `Behind` or `Routes`, or a `Revision` other than the record's, for
  one record for at most that long, counted from the first such answer
  after one that was none of them, and then answers in force at the
  record's revision, or not in force; the server holds it to that
  (I10).
- `host.go`, four methods for the registered router; every other
  provider's `Host` answers `ErrUnsupported`, and so does the router's
  where the server keeps no ledger (a switchyard):
  - `OpenDecisions(ctx, after string) (OpenDecisionsPage, error)`:
    every open record, a page at a time (`{Decisions, Next}`: at most
    64 in decision-number order after `after`, each page one read
    transaction, `Next` empty after the last), each `OpenDecision{
    Decision (with Record), Policy *RoutePolicy (nil under local/1),
    RoutedAt (when a router's policy was first written), Why (the last
    route's), Rev, Counted []CountedResponse}` with
    `CountedResponse{Subject, Groups, At, Confirmed}` (PR-9; none
    before it). No count cut: a router reads every page before it
    first acts on what it holds (I12). It differs from
    `Host.OpenDecisionRefs` (`docs/plans/phone-shell.md`), which any
    provider with a `Host` may call and which returns only the refs of
    the open decisions, a page at a time: this read is the registered
    router's alone and returns each open record whole with its policy,
    `RoutedAt`, rev and counted responses.
  - `Outcomes(ctx, after uint64) (OutcomesPage, error)` and
    `AckOutcomes(ctx, through uint64) error`: the **outcome outbox**,
    one row per record that ever carried a router's policy, read in
    `Seq` order 32 at a time (`{Outcomes, Next}`, each `OutboxEntry{Seq,
    Decision (with Record), Policy (its last router policy, with its
    `Scope`), RoutedAt, Outcome DecisionOutcome, ClosedAt}`), and
    deleted up to `through` by the router's acknowledgement alone (I9).
  - `Reroute(ctx, RerouteRequest{Decision string, Route Route, Why
    string, OpID string, ExpectedRev int}) (RerouteResult, error)`:
    the router changes a record's policy. `Why` is `assigned`,
    `delegated`, `escalated` or `rerouted`; `OpID` is 1 to
    `MaxOpID` of DR1's op id shape, kept on the history row of each of
    the record's first 16 changes; `ExpectedRev` is the record's rev
    the router computed from. `RerouteResult{Rev int, Unchanged bool,
    Superseded string}`. Its answers, in this order:
    `ErrQuestionAnswered` for a decision no longer open; `{Rev (the
    current), Unchanged: true}`, writing nothing, when the request's
    revision is already the record's (I14); `ErrStale` (new,
    `errors.New("ext: stale")`, wrapped with the current rev and
    revision) when the record moved past `ExpectedRev`; `ErrRefused`
    with the words why for a comparison's selection (never given a
    router's policy) and for a policy the server refuses (its `Pinned`
    or `Scope` changed, its groups following neither the requirement nor
    the unsupported form, a bound passed); else `{Rev}`, with
    `Superseded` naming the fresh decision only in PR-9's one case
    (Groups, quorum and distinct persons, below).
- `Resolution.Outcome *DecisionOutcome` (`json:"outcome,omitempty"`),
  set for a record: `DecisionOutcome{State (DR1's final state:
  applied, superseded, cancelled, expired), By (a subject, "operator",
  or "" when no response decided it), Via (eligible, override, local,
  or "" with an empty By), SupersededBy (the fresh decision, for
  superseded), Why}`, so a router can report how each routed decision
  ended, answered or not. `DecisionOutcome.Valid` checks that `By` and
  `Via` are empty together.
- `notify.go`: `Notification.Recipient` (a principal's subject, at most
  `MaxSubject`, "" for none), set by a provider on a notice meant for
  one person. Such a notice reaches only a sink that implements
  `RecipientSink` (`DeliversToRecipients() bool`, answering true), which
  delivers it to that person alone; every other sink never receives it,
  so a decision's question never reaches a webhook. `Notification.Key`
  (at most 64 bytes of printable ASCII, "" for none): a provider's
  stable id of one notice, by which a sink that keeps its own outbox
  drops a notice handed to it twice. `Notification.Valid` checks both.
  `Host.Notify` still returns once the notice is queued to the sinks
  and promises no delivery: the sinks' queues are in memory and drop
  their oldest when full, as on `main`.

### `ext/exttest` (PR-7)

- `FakeRouter` gains `SetPolicy(id, RoutePolicy)` (its next `Route` for
  that record answers it), `SetConfirm(id, func(ConfirmQuery)
  Confirmation)` and `Confirms() []ConfirmQuery`, and implements
  `PolicyConfirmer`; a router with no confirm set answers not in force.
  It gains `Reroute` and the outbox through the `Host` it is started
  with, so a test drives a re-route and an acknowledgement.
- The `Router` check gains a record with a policy (valid for its
  record), answered with the network blocked.
- A new `Confirmer` check: answered within `ConfirmWait` with the
  network blocked, from several goroutines at once, each answer valid.
  `RunAll` runs it for a router that implements `PolicyConfirmer`.

### `internal/crew/ledger` (the steps PR-7a and PR-9a; the code PR-7, PR-9)

- `MaxDecisionPolicy` goes from 1 KiB to 16 KiB: a router's policy at
  its bound (14 KiB) with the server's own fields. Every bound derived
  from it moves with it, and their tests hold (below):
  `internal/api`'s `maxDecisionListed` and its list page, and the
  decision document `docs/plans/sync-seams.md` hands a sync provider,
  which carries the record's policy and whose total bound is the sum of
  its parts' bounds with the policy at `MaxDecisionPolicy`
  (`TestDecisionDocWithTheMostResponsesFits` there, built with the
  policy at this bound).
- `policyShape` (the policy column, decoded with unknown fields
  refused) gains the fields the server writes: `approver` (`operator`
  or `router`), `router` (the provider's name), `scope`, `pinned`,
  `groups`, `distinct`, `override`, `unsupported`, `reroutes`,
  `recent`, `routedAt`, `spare` and `fallback` (`local/1`). The server
  writes a policy only in its canonical form (decoded and encoded
  again, as `authorityOf` does for an authority), so SQL that reads
  one member (`spare`, below) reads what the decoder reads.
- `Tx.PutPolicy(n int64, policy string, rev int) (bool, error)` (new):
  the one writer of the column after the opening. It writes only while
  the decision is `open` and at `rev` (answers false otherwise, writing
  nothing), refuses a comparison's selection (origin `compare`, which
  only the operator answers, as `Tx.RouteFor` refuses it) with
  `ErrInvalidDecision`, checks the policy's shape and bound, and leaves
  every other
  column; the rev and the history are the caller's, in the same
  transaction. So, as on `main`, **a decision's entries change only
  while it is open** (I16).
- History (PR-7): a kind `rerouted` (new) beside the reserved
  `assigned`, `delegated` and `escalated`, the router's four kinds.
  On `main`, `Tx.History` refuses an op id on any kind but
  `commented`, `info_requested`, `routed` and `cancelled` ("a %s row is
  no operation"), and puts every op row that is not a cancel in the
  operator's group (`ops`, `MaxDecisionOperations`) or a Brakeman
  turn's (`turn`); the `rest` count holds `assigned`, `delegated` and
  `escalated`. So: the op check also takes an op id on the router's
  four kinds; their rows (by `router:<name>`, never `TurnActor`) count
  in a group of their own, `MaxDecisionRouterChanges` (16), a branch
  beside `turn` in that group choice, never in `ops`, `Tx.Operations`
  or `turn`; the `rest` count drops the three and keeps `notified` and
  `expired`; and the repeat lookup by `(decision, by, op_id)` answers
  a router's row sent again as it answers the others. The group is
  added to `mandatoryHistory` (77 rows on `main`: 1 + 2 + 32 + 4 + 32 +
  1 + 1 + 1 + 3; 93 after). A router's 17th change and later write no
  history row (I1, `Host.Reroute`).
- `decision_responses.status` takes `counted` (PR-9) beside `accepted`
  and `refused` (`Tx.Respond` accepts it; no column changes).
- **The step PR-7a: the ledger's next schema version**, at its merge
  (13 if no other migration merges first; `main` is at 12), shipped
  alone right before PR-7. PR-7 writes shapes a binary before the step
  misjudges: a router's policy (such a binary answers every response to
  it `stale_policy`, the operator's too), a spare (it counts no place
  for it), an outbox row and the router's history rows. So the version
  is a barrier, as versions 10 and 12 are on `main`: an older binary
  opens the ledger read-only. Its migration creates the outcome outbox,
  the one table PR-7 and PR-7b need; nothing is derived, since no row
  of an earlier version holds any of these shapes. PR-7b writes no
  shape PR-7 does not, so a build of PR-7 alone reads correctly what a
  build of PR-7b wrote, and the one step covers both.
- **The outcome outbox**, created by that migration, one `CREATE
  TABLE` and one index:

  ```sql
  CREATE TABLE decision_outbox (
    seq INTEGER PRIMARY KEY AUTOINCREMENT, -- never reused
    -- the record's n; no foreign key: the row outlives a pruned run
    decision INTEGER NOT NULL,
    -- JSON: the ext.OutboxEntry as encoded, at most 24 KiB
    outcome TEXT NOT NULL,
    at TEXT NOT NULL
  );
  CREATE UNIQUE INDEX decision_outbox_decision
    ON decision_outbox(decision);
  ```

  `Tx.Outcome(n, outcome)` (PR-7) writes the row in the transaction
  that gives a router-policy record its final state (applied,
  superseded, cancelled, expired), one per record;
  `Ledger.Outcomes(ctx, after, limit)` reads in `seq` order;
  `Ledger.AckOutcomes(ctx, through)` deletes up to `through` (both
  read and served from PR-7b). A record under `local/1` alone writes no
  row, so a server with no router writes none. A row is
  self-contained: nothing in it is read from another table when it is
  served, so a run pruned since loses nothing. Rows are bounded by the
  records that ever held a router's policy, one each; they leave only
  by an acknowledgement. `Snapshot` and `SnapshotFile` copy the table
  with the rest.
- **The step PR-9a: the barrier**, the schema version after PR-7a's,
  shipped alone right before PR-9, with a migration of no statements,
  as RV1's (version 10) is, so that an older binary (PR-7 to PR-8
  among them, which count no approvals) opens a ledger that may hold
  `counted` rows read-only.
- `placeCond` (PR-7; the condition of a decision holding a place for
  its replacement) gains one clause: an `open` or `answered` decision
  whose policy holds a spare (`json_extract(d.policy, '$.spare') = 1`)
  holds one place, and an identity's accepted or counted response to it
  takes that place, never a second one (Every routed decision keeps
  room, below). `Tx.room` counts it so.
- `MaxVoidedRoutedPerRun` (1,000) and the count it bounds: the
  decisions of one run that were superseded with a replacement while
  under a router's policy (below).

### `internal/crew` (PR-7; PR-7b and PR-9 where they say)

- `decisions_admit.go`: a kind of entry `eligible` (`EntryEligible`,
  with `Subject`), which admits an identity's principal holding the
  decide grant or the admin grant whose subject is `Subject`.
  `PolicyEntry.admits` takes `via: sync` as it takes `via: identity`
  for the operator entry with the admin grant and the grant entry with
  the decide grant, each with the grant the sync provider's door
  checked (`AdmitsAnswer`); `Admittee.Sent` takes a `sync` response as
  its subject's own. The `eligible` entry admits no held answer (a held
  answer is never judged under a router's policy, Responding, Held
  answers), and no held answer answers a comparison's selection (Owner
  decisions, 10).
  `EntriesOf` of a record under a router's policy is the operator's
  entry and one `eligible` entry per distinct subject among the
  groups' eligible, never the grant's entry, whatever the request's
  `for` says (only the policy chooses, I8). `AdmissionOf` is unchanged
  in shape: it is still pure on the record. Whether the policy is in
  force, and the subject still eligible, is asked of the router by the
  routes that read or answer (below), outside the ledger, so the decide
  list and the decide read still judge each record by one function
  (`TestAdmitsIsTheOneCheck`, extended).
- The engine is told at its start whether the registered router
  implements `PolicyConfirmer` (`Engine.SetRouterPolicies(bool)`, new,
  from `internal/api`'s composition) and is handed one function to ask
  it (`PolicyCheck`: a decision, a revision, a subject and a time to a
  `Confirmation`, the guard's `confirm`), which it calls outside every
  transaction.
- **The opening** (PR-7). A record opened while the engine routes
  policies, but a comparison's selection, holds a **spare** (its
  `local/1` policy written with `spare: true`, which every later policy
  keeps): one place for the replacement its routed answer may need
  (Every routed decision keeps room, below).
- **The policy's transaction** (`Engine.RoutePolicy(ctx, id, by string,
  rev int, policy ext.RoutePolicy, to []Destination, rule string)`,
  new, beside `RecordRoute`): one transaction through the ledger,
  taken whether or not the run is held. A route that carries a
  `Policy` writes the router's single `routed` row here (by
  `router:<name>`, no op id), the already-answered branch below
  included, and `RecordRoute` is not called for it: on `main` every
  opening writes the first of the two `routed` rows a record may hold
  (the operator's by policy, `routedDetail`), so a `RecordRoute` row
  beside this one would be a third, refused `decision_limit`, and the
  policy with it. `RecordRoute` stays for a route with no `Policy`. A
  comparison's selection (origin `compare`) takes no policy: the
  transaction writes nothing for it, not even the `routed` row, and
  its route is kept as on `main` (`RecordRoute` for destinations).
  When the record is `open` at `rev` and its policy is `local/1`, it
  writes the policy
  (`Tx.PutPolicy`, `approver: "router"`, `router: <provider name>`,
  `routedAt` now, the spare kept, `fallback: "local/1"`), rev + 1, the
  router's one `routed` row (`{to, rule, revision, by:
  "router:<name>"}`, cut to the row's 2 KiB) and the `decision` event.
  A record already answered gets the `routed` row alone, saying the
  policy came after the answer, and nothing else changes. A record
  open at another rev is not written, and is told again (a comment
  moved it meanwhile). A record that already carries a router's policy
  is not written: a change of policy is `Host.Reroute`'s.
- **Responding** (`respondTx`, judged after DR1's replay of a repeat,
  in the order of the table The respond route's judgement, under
  Events, orderings and invariants). Every door that hands a response
  in asks `Confirm` first, outside the transaction, and hands its
  answer to the engine with the response: the respond route (the
  operator's and the decide principal's). A sync provider's door for
  held answers asks nothing of the router (Held answers, below).
  - **A comparison's selection** (origin `compare`) never holds a
    router's policy: it is judged under `local/1` as on `main`, with no
    confirmation asked, no window and no `stale_policy`; only the
    operator answers it, and only a principal authenticated at this
    server (the workbench token, or an identity provider's principal
    with the admin grant): a held answer to a selection is refused `403
    not_approver`, kept ("dr-12 is a comparison's selection: it is
    answered only at this server"), whatever grant its subject holds
    (Owner decisions, 10).
  - **A router's own answer** (`via: router`, `Route.Answer`) is
    refused `403 not_approver`, kept, under every policy, as under
    `local/1` on `main` (I3); no confirmation is asked for it.
  - **No confirmation handed in**: on a server whose router confirms
    policies, a record under a router's policy that reaches the engine
    with no confirmation (a door that did not ask) is refused `409
    stale_policy`, unkept, never taken as "not in force" (I5). With no
    such router registered, a record under a router's policy is not in
    force (I7) and is judged so. A record still under the server's own
    policy is answered as on `main`, on any server: by the operator and,
    where it was given to the decide grant, by a decide principal.
  - **The opening window**: a record in its window (`internal/api`,
    `router.go`, below; I10) refuses every response `409
    stale_policy` ("dr-7's policy is being written: try again").
  - A `local/1` record whose router implements `PolicyConfirmer` asks
    `Confirm` with no subject before accepting the operator's response:
    `Routes` is `409 stale_policy` too, and marks the record to be told
    again; any other answer leaves it judged by its entries, as on
    `main`.
  - **Not in force** (a record under a router's policy: no router, no
    answer, `InForce` false):
    `local/1` decides, as DR1 ships it: the operator answers, a decide
    principal is refused `403 not_approver`; the receipt keeps
    `{policy: "local/1", why}` with the router's why.
  - **Stale** (`Confirmation.Revision` differs from the record's) or
    **behind**: `409 stale_policy`, to retry, within I10's bound.
  - **In force, the subject eligible** (entries admit it, and
    `Confirmation.Groups` holds a group of the policy): the response
    counts for every group it is eligible in; the receipt keeps
    `{groups, via, ref}` beside DR1's `for`. An operator's identity
    that is also eligible answers as eligible, never as an override.
  - **In force, a decide principal not eligible**: `403 not_eligible`,
    kept ("dr-7 is for the architecture owner: Ana answers it"). A
    decide principal the record does not admit at all is answered
    `404 not_found` by the decide routes before this, as on `main`.
  - **In force, the operator not eligible**: `403 not_eligible`, kept,
    unless the body says `override: true` with a `rationale` of at
    least 10 characters and the policy's `override` is `operator`
    (else `403 override_refused`, kept). An accepted override is
    recorded as one, with its subject (`workbench`, or the identity's),
    in the receipt (`{override: true}`), the `responded` row and the
    card. Only the admin grant overrides (I6).
  - Every `stale_policy` above is an **unkept** refusal (no row, no
    history, judged again when sent again with the same response id),
    as DR1's bound refusals are: it says "try again", and a kept one
    would answer the retry with itself.
  - **Held answers** (one rule, mirrored in `docs/plans/sync-seams.md`
    at its `RespondDecision` door; Owner decisions, 11). A held answer
    and a router's policy do not combine in the first version. A
    router that confirms policies is one that implements
    `ext.PolicyConfirmer` (the engine's `SetRouterPolicies(true)`,
    I17). On a server whose registered router confirms policies, the
    sync provider's door refuses every held answer: nothing is written
    to the decision, the router is not asked, and the refusal is final
    (the provider does not hand it in again), `403 answered_at_server`
    (a new code, `CodeAnsweredAtServer`, listed with the decisions'
    codes and in `docs/protocol.md`; its words a fixed line of the
    server's) ("dr-7 is answered at its server, by the operator or the
    people its router names, at the server's own routes: an answer given
    elsewhere is not taken here"). The engine refuses a `via: sync`
    response that reaches it for a record under a router's policy while
    such a router is registered with the same code, as a kept refusal
    (DR1's refused row and receipt; the door is not the only guard). On
    a server with no router, or with one that confirms no policies, a
    held answer is judged by the record's own policy and its entries
    (`via: sync` taken as `via: identity`), the store's own check first,
    DR1's rules after; a record left under a router's policy with no
    such router registered is not in force, so `local/1`, and judged so.
    Held by `TestHeldAnswerRefusedWhereTheRouterConfirmsPolicies` (the
    door), `TestEngineRefusesAHeldAnswerUnderAPolicy` (the engine),
    `TestHeldAnswerAdmittedByTheEntries` and
    `TestHeldAnswerToASelectionIsRefused`. A held answer recorded
    before such a router was registered (a restart with a new router)
    stands as recorded: it is applied by its `AdmitsAnswer` recheck
    alone, as an identity's `local/1` answer is (I16); the refusal is
    made at the response only.
  - Then DR1's checks as on `main`: the state, the rev, the
    membership, the bounds.
- **Application** (`applyDecisions`): for a response that counted
  under a router's policy, the engine asks `Confirm` again with its
  subject (and, in PR-9, `Since` its time) before the application's
  transaction, beside DR1's identity recheck. Not confirmed (no longer
  eligible, not in force, no answer) supersedes the decision with DR1's
  fresh decision and words ("Ana's answer could not be confirmed: dr-9
  asks again"), exactly as DR1 does for an identity recheck that fails;
  the fresh decision is told to the router like any opening. An
  override or a `local/1` answer by the workbench token holds (the
  token is the server's); one by an identity's admin principal keeps
  DR1's identity recheck and restart rule whole and needs no router
  confirmation (an override is not an eligibility).
- **Re-route** (`Engine.Reroute`, PR-7b, the internals of
  `Host.Reroute`): one transaction through the ledger, conditional on
  the decision `open` and at `ExpectedRev`, after the repeat check
  (I14). The new policy's `Pinned` and `Scope` must equal the
  record's, its groups follow the requirement or the unsupported form
  (the one change of form allowed is from the unsupported form to the
  requirement's groups), its counted responses keep their places, rev
  + 1, the `decision` event, and one history row of kind `Why` for
  each of the record's first 16 changes, with its `OpID`. **A re-route
  never runs out**: from the 17th change on, the change is applied the
  same way and recorded in the policy itself (`reroutes` + 1, the
  change at the head of `recent`, the oldest of 16 dropped), not in the
  history. A re-route opens no decision and has no count bound, but for
  PR-9's one case.
- **Every routed decision keeps room for its answer and its
  replacement** (I11; the spare and its place PR-7's, a replacement
  chain and the voided bound PR-7b's). DR1 refuses an identity's
  response `decision_limit` when the place its replacement would need
  passes the run's 1,000, the workbench token being its way out. Under a
  router's policy with `override: none` there is no such way out, so
  the room is taken earlier, where DR1 already refuses: at the opening.
  A record opened while the engine routes policies holds its spare
  from its opening, counted in each bound's transaction from the rows
  (`placeCond`, above); the opening is refused `decision_limit` when
  the run's decisions in all and the places, its own spare included,
  would pass 1,000. So a run fills at an opening, where the requester
  is told as DR1 tells it, and an eligible response to a decision with
  a spare is never refused for room. A replacement opened for a routed
  answer (DR1's, for an answer whose recheck failed at application;
  PR-9's, for a decision whose response rows no longer leave room for
  its unmet places) takes the place of the spare and is opened with a
  spare of its own; the decision it supersedes stops counting among
  the run's 1,000 once its replacement stands in its place, and is
  counted instead among the run's **voided routed decisions**, bound
  at 1,000. A chain of replacements so holds one decision and one spare
  of the run's 1,000 whatever its length. Only past that second bound
  is an eligible response refused `decision_limit` ("dr-7's run has
  voided 1,000 routed answers: stop or cancel it"); stop and cancel
  stay the operator's. A spare is held until its decision's final
  state. A re-route, a `local/1` answer and an override by the
  workbench token open none.
- **Who may change a route** (PR-7b). Under a router's policy in force
  (one `Confirm`, no subject), DR1's route operation with `to` a person
  or a role (HM1's `RouteDecision`, and Brakeman's `route_decision`
  through it) or the decide grant (`{kind: "grant"}`) is refused `409
  routed_by_policy`, kept ("dr-7 is routed by its router's policy: ask
  the router again, or override at the answer where the policy
  allows"). Not in force, it is accepted as HM1 and the decide grant
  have it, and any accepted change of the record's `for` tells the
  router again: the record leaves the router's set of records told
  (`decisionRouter.opened`), so a `for` set while routing was away is
  routed by the policy when it returns.
- A new destination `router` (`DestRouter`) for the operator's route
  operation (PR-7b): ask the registered router for a route again now.
  It is one of the operator's operations (a `routed` row saying the
  router was asked, within `MaxDecisionOperations`), moves no rev, and
  after its commit the record is told again. A Brakeman turn may not
  send it.
- **Outcomes**: a router-policy record's final transaction writes its
  outbox row (`Tx.Outcome`, PR-7), every final state, answered or not;
  `Resolution.Outcome` is filled from the same values when its end is
  told (PR-7b).
- **Counted responses** (PR-9): Groups, quorum and distinct persons,
  below.

### `internal/api` (PR-7; PR-7b where it says)

- `extensions.go`: `guardRouter` gains `confirm(ctx, ConfirmQuery)
  Confirmation`, in one of the provider's call slots as every call
  takes, bounded by `ext.ConfirmWait`, its answer checked by
  `Confirmation.Valid`; `routeIn`
  checks a route's `Policy` with `RoutePolicy.Valid(Record)` and
  refuses one from a router that does
  not implement `PolicyConfirmer`, or one past PR-7's shape (below).
  The composition tells the engine `SetRouterPolicies` from the
  registered router.
- `router.go`: the record told to the router gets `Record` filled
  (`recordDecision`), its `Recommended` from the `routed` rows written
  before the router's own. `routeRecord` hands a route with a `Policy`
  to `Engine.RoutePolicy` at the rev the router was shown, which writes
  the route's one `routed` row with it; `RecordRoute` is called only
  for a route with no `Policy`. A comparison's selection is told as on
  `main`, with `Record.Origin` `compare`, and no window opens for it.
  The **opening window** (I10) is kept in memory, per record: it opens
  at the commit of a record opened while the engine routes policies
  (marked in `Server.decisionTold`, which runs under the ledger's lock
  once the commit is made, before it broadcasts the record's `decision`
  event: on `main` the event goes first, so the mark is put ahead of it,
  and a client that answers on the event never beats the router), and
  closes at the first of: the policy's transaction, the router's answer
  with no policy, a tell not made (no call slot, the provider not
  started) or failed, `ext.RouteWait` after the tell began, and twice
  `ext.RouteWait` after the commit (a tell that waited in the queue).
  Past it, a record the router still means to route is held by
  `Confirm`'s `Routes`, under its own bound. A restart forgets the
  windows; a pass tells every open record again, and `Routes` covers
  them. A record re-told by a pass that already carries a router's
  policy is told as on `main`, and a policy in the answer writes
  nothing.
- `exthost.go`: `OpenDecisions`, `Outcomes`, `AckOutcomes` and
  `Reroute` for the router's `Host` (PR-7 declares and answers
  `ErrUnsupported`; PR-7b serves them); `Notify` with a `Recipient`
  hands the notice only to sinks that are `RecipientSink`s answering
  true (PR-7b).
- `decisions.go`, the respond route: the body gains `override`
  (the admin's alone: a decide principal's body is closed, as on
  `main`, so `override` from it is `400 invalid`). Under a router's
  policy the route asks `Confirm` before the engine's transaction, with
  the response's subject ("" for the workbench token), and hands the
  answer in. The refusal codes `not_eligible` and `override_refused`
  are `403`; `routed_by_policy` and `no_router` are `409`.
- `decide.go`: the decide principal's three routes, **visibility
  following eligibility now** (PR-7b). A decide principal is admitted
  to a record under a router's policy only when the policy names its
  subject among a group's eligible (`AdmissionOf`) and `Confirm` with
  its subject answers eligible now. The list asks it per admitted row
  after the ledger's read, from the router's memory, all within one
  `ConfirmWait`; a row not confirmed in time, or with no call slot
  free, is left out and sets `cut`. The read and the respond route ask
  it for their one record; not confirmed is `404 not_found`, as a
  record that does not admit it. So a person the router no longer
  names loses the decision at their next request, before any re-route.
  The decide grant gives no live connection on `main`, so no stream is
  judged here. `decideWords` gains `not_eligible` ("You are not among
  those who may answer dr-7."), and PR-9's `not_needed` and
  `already_counted`, each naming no one.
- `GET /api/decisions/{did}`, the operator's read: `policyNow:
  {inForce, why}` from one `Confirm` with no subject, bounded by
  `ConfirmWait`, for a record under a router's policy; absent
  otherwise. No list asks it.
- `inbox.go`, `inbox_decisions.go` (PR-7b): MB0's read model fills a
  record's item `routedTo` and `routedRule` from its current policy
  (eligible persons as `person`, gaps as `role`, the first group's
  rule line), at their bounds (`routedRule` 200 bytes).
- `lane_decisions.go`: a decision link's lane answers no record, as on
  `main` (`refuseRecordDecide`); its record head (`laneRecordPart`'s
  `Decision`, a `crew.Decision` whose `policy` is the column as kept,
  carried as JSON, so no `internal/proto` type names it) carries the
  larger policy, within `proto.MaxDecision` (128 KiB), which
  `TestTheFullestDecisionRecordThroughTheLane` holds at the new bound.
- The guide's `decision_route` input schema gains `router` among the
  destinations; Brakeman's `route_decision` is unchanged and refused
  `routed_by_policy` under a policy in force.

### `web/app` (PR-8)

- `utils/decisionPolicy.ts` (new): the words of a policy from the record
  alone: "For the architecture owner: Ana, Ben (delegate for Ana)"; a
  gap's words; "Routed to Ana: not in force now (why). The operator
  answers."; an override's line ("Overridden by the operator: reason");
  PR-9's "1 of 2 for the architecture owner".
- `components/DecisionCard.vue` (DR1's): "Who may answer" from the
  policy, each person with why (`via`) and the rule; the gap; the
  `policyNow` line; a `local/1` record keeps `main`'s words.
- `components/DecisionRespond.vue` (DR1's): for the operator under a
  policy in force, the answer buttons disabled with "This is Ana's to
  answer" and, when the policy's `override` is `operator`, Override…:
  a dialog with a required reason of at least 10 characters, which
  sends the answer with `override: true` and the reason as its
  rationale. `not_eligible`, `override_refused` and `routed_by_policy`
  are said in words. The route operation offers "Ask the router again"
  (`to: {kind: "router"}`) when a router is registered.
- `utils/decisions.ts`: `decideWords` and `DECIDE_JUDGING` gain
  `not_eligible`, `not_needed` and `already_counted`, so the decide page
  says each in words naming no one.
- Components come from Nuxt UI; a state is drawn by the one status
  language (`utils/status.ts`, `StatusMark`).

### `internal/cli` (PR-8)

- `conductor decision show` prints the policy (its groups with who is
  eligible and why, a gap, the override, `policyNow`).
- `conductor decision respond --override --why TEXT`: the override,
  its reason the rationale (`--why`, at least 10 characters with
  `--override`).

### `web/e2e` (PR-8)

`web/e2e/decision-policy.spec.ts` (new), on the shipped build, the
server's replies for one record under a policy stubbed in the browser
(`page.route`, as `decide-only.spec.ts` and `decisions-gate.spec.ts`
stub them; Owner decisions, 6): the Override… dialog refusing an empty
or short reason and sending one; the operator's disabled answer and
"This is Ana's to answer"; the gap's words; "not in force now"; and the
decide page's `not_eligible` words. Both themes, a phone at 390 x 844,
layout by measures.

### Docs

- `docs/protocol.md`, its Decisions section (PR-7, PR-7b): the policy
  in the record, the respond body's `override`, the refusal codes
  (`not_eligible`, `override_refused`, `routed_by_policy`,
  `no_router`, `answered_at_server` for a held answer, and PR-9's
  `not_needed`, `already_counted`), `policyNow`, the route operation's
  `router`, the decide routes' admission under a router's policy, the
  response status `counted`. No WebSocket frame changes, and the
  `decision` event is unchanged (its `state` and `rev` move as they
  did).
- `docs/features.md` (PR-9, the last row): the entry for a router's
  policy on decision records.

## Groups, quorum and distinct persons (PR-9)

DR1 accepts one response in a decision's life. Under a router's policy
with several groups or a quorum above one, an eligible approval is
recorded with the status `counted`, with the set of groups its subject
is eligible in (`Confirmation.Groups`), and the decision stays `open`
(rev + 1, "1 of 2 for the architecture owner").

Completion is a matching, decided at each counted response and again
at application. The counted responses' **coverage** is the number of
quorum places they fill at once: without `distinct`, the sum over the
groups of the smaller of the quorum and the counted subjects eligible
in it now (one person may fill several groups); with `distinct`, the
largest assignment of counted subjects to groups, one group each and
at most a quorum per group (a maximum matching, a flow over at most 32
responses and 4 groups: Ana eligible in architecture and security and
Cam in security alone completes as Ana for architecture, Cam for
security). The decision is complete when the coverage reaches the sum
of the quorums (at most 16): it is then `answered`, with the combined
answer. A delegate counts as themselves. One eligible Reject answers it
at once, rejected. A clarification takes the first eligible answer.
An accepted override (the operator, where the policy says `operator`)
answers the decision whole, every group at once, since it stands in
the policy's place; it fills no single group, and the counted
responses before it are kept in the record as counted.

**Every unmet quorum place keeps a row for the response that fills it.**
An eligible approval is counted only when it raises the coverage; one
that does not (a fifth architecture approver once that group's quorum
of four is met and the security group still needs four), or the same
subject again, is refused `409 not_needed` ("dr-7 has the architecture
owners' approvals it needs: it waits for the security owners") or `409
already_counted`, never kept (DR1's unkept refusal: no row, judged
again on a retry), so surplus approvals take no row. A refusal is kept
only while the rows left after it are at least the unmet places (the
sum of the quorums less the coverage); past that it is unkept, so the
32 response rows always hold a row for each place still to fill, the
deciding one included: at most 15 counted and the deciding one while
eligibility holds, the rest for kept refusals. Eligibility that shrinks
(a router's re-route takes someone away) can leave a counted row that
fills nothing; when the rows left fall below the unmet places, in the
transaction of the re-route or the response that makes them so, the
decision is superseded by DR1's fresh decision, which takes the place
of its spare, with the words "dr-7's responses filled its record: dr-9
asks again", and its counted responses are asked again
(`RerouteResult.Superseded` names it).

**A counted response keeps its authority without a credential.** DR1
keeps no pass and supersedes an identity response found unapplied after
a restart. A counted response is written `confirmed: false`; DR1's
identity recheck after its commit (`recheckResponse`, the same as
today) sets it `confirmed`, or refuses it with words ("Ana's approval
could not be rechecked: answer again") and the decision stays open.
One found unconfirmed after a restart (its `boot` another process's) is
refused the same way (a held answer never counts: Responding, Held
answers). A confirmed one keeps its place across restarts. At
application every counted response is rechecked by `Confirm` with its
subject and `Since` its time: a router that cannot say the subject
stayed eligible since then answers not eligible, and the decision is
superseded with DR1's fresh decision and words, as a failing identity
recheck does. No credential is stored. How a router establishes "since"
is its own; the server's promise is that it asks, and that "no" or no
answer voids the approval.

The policy's full shape is taken from this row on: PR-7's server takes
a `Pinned` of any legal shape and `Groups` of one group of quorum 1
(the requirement's own, or the unsupported form), and refuses more as
`invalid` through the guard (the free default); PR-9 takes all of it.
A router that cannot route the whole requirement before PR-9 sends the
unsupported form, keeping the requirement whole in `Pinned`, and
re-routes it in full once the server takes it (the one change of form).

The decide view's `own[].status` shows `counted` for the subject's own
counted response; the operator's card shows each group's count.

## Events, orderings and invariants

**Invariants**, each held by a test named under Tests per surface:

- **I1, a route explains itself.** A policy has its rule per group,
  every eligible person a `via` and a `ref`, every gap its words; the
  history keeps the first route and the first 16 changes with their
  why and `OpID`; the policy keeps the count of all of them and the
  latest 16 (`Reroutes`, `Recent`) (`TestRerouteWritesItsHistoryKind`,
  `TestManyReroutesKeepTheDecision`).
- **I2, a group with nobody eligible keeps its requirement.** It stays
  in the policy with its gap; it is never dropped and never answered by
  the server; the decision stays open until someone eligible answers
  or the operator overrides where the policy allows
  (`TestGapKeepsTheRequirement`).
- **I3, silence is never approval.** No code here writes a response on
  a router's behalf, an `expired` state or a lower quorum; a router's
  held answer (`Route.Answer`) is refused under every policy, as under
  `local/1` (`TestRoutersAnswerRefusedUnderItsPolicy`).
- **I4, a re-route never changes the requirement.** `Pinned` and
  `Scope` are the first route's for the record's life; the groups
  follow `Pinned` or the unsupported form; the one change of form is
  from the unsupported form to the requirement's own groups
  (`TestRerouteCannotChangeTheRequirement`,
  `TestUnsupportedFormToFullIsTheOneChange`).
- **I5, only an eligible subject satisfies a group.** Under a policy in
  force, eligibility is asked of the router at the response (after the
  identity provider admitted the subject) and again at the
  application; a share link, a decision link, an agent's token,
  Brakeman, a decide principal not named and a person the router no
  longer names are refused; a decide principal sees only the decisions
  it is eligible for now. A record under a router's policy that
  reaches the engine with no confirmation is refused, never taken as
  not in force, and a held answer is never judged under a router's
  policy (`TestEligibleSubjectAnswers`,
  `TestIneligibleDecidePrincipalIsRefused`,
  `TestDecidePrincipalSeesOnlyItsDecisions`,
  `TestRespondWithoutConfirmationIsStalePolicy`,
  `TestEngineRefusesAHeldAnswerUnderAPolicy`).
- **I6, the operator's answer under a policy in force is an override**:
  explicit, with its reason, only where the policy's `override` is
  `operator`, recorded as one with its subject. Only the admin grant
  overrides; the opening window gives the operator no plain answer
  (`TestOperatorNeedsAnExplicitOverride`,
  `TestOpeningWindowRefusesTheOperator`).
- **I7, a policy not in force is `local/1`.** No router, no answer,
  `InForce` false: the operator answers as in the free build, the
  receipt says why, and no decide principal is admitted by the policy
  (`TestPolicyNotInForceIsLocal1`).
- **I8, only the policy chooses who may answer.** Under a policy in
  force, the operator's route to a person, a role or the grant is
  refused; a `for` naming a person and an earlier `routed` row reach
  the router only as recommendations; a decision link answers no
  record; the grant's entry is not among a routed record's entries
  (`TestRouteDecisionUnderAPolicyIsRefused`,
  `TestGrantRouteRefusedUnderAPolicy`,
  `TestDecisionLinkCannotAnswerARoutedDecision`).
- **I9, a fallback answer is kept for the router.** Every final state
  of a record that ever carried a router's policy writes its outbox row
  in the same transaction, and the row leaves only by the router's
  acknowledgement: no crash, no outage and no restart loses it
  (`TestOutcomeOutboxWrittenInTheFinalTransaction`,
  `TestOutcomeOutboxAckedOnlyByTheRouter`).
- **I10, every answer to retry has a bound.** `409 stale_policy` is
  answered only in the opening window (below), and while `Confirm`
  answers `Behind`, `Routes` or a `Revision` other than the record's
  (a new revision not yet installed); the server holds the router to
  the same bound it promises, `ext.MaxBehind` (120 s): per record, a
  clock starts at the first such answer after one that was none of
  them, any other answer clears it, and past 120 s on it the server
  treats the next such answer as not in force, and the
  operator answers under `local/1` (I7, I9). No partial failure leaves a
  decision unanswerable (`TestStalePolicyRefused`,
  `TestBehindPastItsBoundIsNotInForce`,
  `TestRevisionMismatchPastItsBoundIsNotInForce`,
  `TestBehindBoundClearedByAnInForceAnswer`).
- **I11, room is kept for what is still required.** A record opened
  under a router that routes policies holds a spare from its opening;
  an eligible answer is never refused for room; every replacement has
  its place; a chain of replacements holds one decision and one spare
  of the run's 1,000; a run fills at an opening, never at an answer;
  only past 1,000 voided routed answers in one run is an answer
  refused; a re-route opens none; the response rows keep a row for
  every unmet quorum place (`TestFirstEligibleResponseAtTheRunsLimit`,
  `TestReplacementChainHoldsOneSpare`, `TestSurplusApprovalsTakeNoRow`).
- **I12, the router can know every open record.** A record is told at
  its opening whatever `maxUnresolved` holds (as on `main`), and
  `Host.OpenDecisions` pages every open record with its policy, no
  count cut (`TestRecordsToldPastMaxUnresolved`,
  `TestOpenDecisionsPagesEveryRecord`).
- **I13, the requirement lives in the record.** The rule's id, the
  groups and quorums, `distinct`, `override`, `notify` and
  `escalateAfter` are written with the first policy and read back from
  the record by whoever needs them (`TestRequirementRequired`,
  `TestRequirementRequiredAndUnchangedByAReroute`).
- **I14, a repeat is answered by what it asked for.** A re-route whose
  revision is already the record's answers `Unchanged` and writes
  nothing, at any count of changes and after a restart; a response
  refused `stale_policy` is judged again when sent again
  (`TestLostRerouteReplyPastTheHistory`,
  `TestStalePolicyRefusalIsNotKept`).
- **I15, every final state has its outcome.** Applied, superseded,
  cancelled and expired, answered or not, each with its `State`, and
  `By` and `Via` empty together when no response decided it
  (`TestOutboxCarriesCancellationsAndSupersessions`,
  `TestResolutionOutcomeValid`).
- **I16, a decision's entries change only while it is open.** The
  policy is written only by `Tx.PutPolicy`, while open and at its rev,
  as the route target is only by `Tx.RouteFor`; so the entries a
  response was judged by are the entries at its recheck and its
  application, as the decide grant has it
  (`TestPolicyAfterAnswerChangesNothing`).
- **I17, nothing changes without a router.** No route, field, row or
  word of this plan appears where no router is registered, or where
  the registered one does not implement `PolicyConfirmer`
  (`TestNoRouterNothingChanges`).

**Orderings.**

1. DR1 commits the decision (`open`, `local/1`, its spare), then tells
   the router through C5's guard. The router answers from memory. The
   server writes the policy in a second transaction, conditional on the
   decision still `open` at the rev the router was shown; between the
   two, a response is `409 stale_policy` (the opening window), and only
   a router that answers no policy, fails or is late leaves `local/1`.
2. A response: the identity provider's admission (as on `main`), then
   `Confirm` (outside the transaction, bounded), then DR1's transaction
   with its rechecks, the policy's revision among them; then DR1's
   identity recheck after the commit, and a second `Confirm` before the
   application's transaction. A held answer on a server whose router
   confirms policies is refused at the door, before any of these.
3. A re-route: the router computes from what it holds and the record's
   rev, then `Host.Reroute` (one transaction, conditional on `open` and
   that rev; `ErrStale` sends the router back to compute again). A
   response made against the earlier revision is `409 stale_policy`,
   or DR1's `stale_decision` when the card's rev is older. Only a
   change of revision is a re-route; a repeat whose revision is in
   place answers `Unchanged`.
4. A router that stands behind its policy again: it reads the open
   records (`Host.OpenDecisions`), then routes each `local/1` one still
   open that it now routes through `Host.Reroute` (`why: rerouted`), as
   it chooses; the outbox drains beside, by `Outcomes` and
   `AckOutcomes`.
5. A decision's end: its final transaction writes its state and, for a
   router-policy record, its outbox row; then the router is told
   `Resolved` (best-effort, with `Outcome`); then the router reads the
   outbox and acknowledges what it has taken. A crash between any two
   steps loses nothing: the outbox is read again.
6. A schema version before a shape: the step PR-7a ships alone, before
   PR-7 can write any router's policy, spare, outbox row or router
   history row, so an older binary opens a ledger that may hold one
   read-only; the step PR-9a does the same for `counted` rows, before
   PR-9. A provider that routes by this seam waits for a tag
   holding PR-7a, PR-7, PR-7b and PR-8 (The PRs).

**The respond route's judgement**, under a router's policy, in order
(the first row that matches answers). It is the engine's; a sync
provider's door for held answers refuses every one on a server whose
router confirms policies before the engine (Responding, Held answers):

| Case | Answer | Kept |
|---|---|---|
| a repeat (same actor and response id) | the saved receipt | as saved |
| a router's own answer (`via: router`) | `403 not_approver` | yes |
| a comparison's selection, a held answer (`via: sync`) | `403 not_approver` (answered only at this server) | yes |
| a comparison's selection, at the route | `local/1`, as on `main`: the operator answers | as DR1 |
| a held answer (`via: sync`) to a record under a router's policy, a router that confirms policies registered | `403 answered_at_server` | yes |
| no confirmation handed in, a record under a router's policy, a router that confirms policies registered | `409 stale_policy` | no |
| the opening window | `409 stale_policy` | no |
| `Confirm` not in force (or no answer), the operator | accepted under `local/1`, `{policy: local/1, why}` | yes |
| `Confirm` not in force, a decide principal | `403 not_approver` | yes |
| `Confirm` `Behind`, or `Revision` other than the record's (within I10's bound; past it, as not in force) | `409 stale_policy` | no |
| in force, the subject eligible | counted (PR-9) or accepted, `{groups, via, ref}` | yes |
| in force, a decide principal not eligible | `403 not_eligible` | yes |
| in force, the operator, no `override` | `403 not_eligible` | yes |
| in force, the operator, `override` with a reason, policy `operator` | accepted as an override | yes |
| in force, the operator, `override`, policy `none` | `403 override_refused` | yes |
| then | DR1's checks (state, rev, membership, bounds) | as DR1 |

**A record's policy over its life** (the server's states; DR1's
decision states are unchanged, PR-9 adds the response status
`counted`, never a decision state):

| Policy | Event | Next | Action |
|---|---|---|---|
| `local/1` | opened, a router that routes policies registered | `local/1` (window) | the spare held; the tell |
| `local/1` (window) | the router's policy, valid, open at the told rev | router(r) | `PutPolicy`, rev + 1, `routed` row, event |
| `local/1` (window) | no policy, an error, late, or the tell not made | `local/1` | the window closes |
| `local/1` (window) | the record moved (a comment) | `local/1` | told again |
| `local/1` | `Confirm` answers `Routes` | `local/1` | `stale_policy`; told again |
| `local/1` | `Host.Reroute` with a full policy | router(r) | as a first route, `why` its kind |
| router(r) | `Host.Reroute` with revision r | router(r) | `Unchanged` |
| router(r) | `Host.Reroute` with r', same `Pinned` and `Scope` | router(r') | rev + 1, a history row (first 16) or `Recent` |
| router(r) | told again by a pass, any answer | router(r) | nothing written |
| router(r) | answered, then final | final | the outbox row in the final transaction |
| any | final | final | the policy kept; no write takes it |

## Out of scope

- **Clearing the outbox without a router.** Rows leave only by a
  router's acknowledgement (I9). A server whose router is no longer
  registered keeps them, one per record that ever held a router's
  policy, at most 24 KiB each, and an export carries them; no command
  clears them in this plan. A way for the operator to clear them is a
  later choice, kept apart so that no outcome is lost while a router
  may still come back to take it.

- **How a router decides**: who holds a role, delegation windows,
  escalation, quiet hours, batching and deduplication of its notices,
  and what it does with outcomes. The server keeps `notify` and
  `escalateAfter` in the record for the router and acts on neither.
- **Delivering a notice to one person**: `Notification.Recipient` and
  `RecipientSink` are the seam; no sink of this repository implements
  `RecipientSink`, so in the free build a notice with a `Recipient`
  reaches no sink.
- **Routing a screen's question** (one no record holds): it keeps
  `main`'s first response wins, unrouted (Owner decisions, 3).
- **Routing tasks, execution hosts and follow-up work**: no seam routes
  a task; that is a later plan of SW1's.
- **A person's batched review with findings** (RV1) by a routed
  reviewer: a review is routed here only as a decision record.
- **Answering for someone**: a delegate answers as themselves.
- **A policy editor in the app's screens** (Owner decisions, 2).
- **A live connection for a decide principal**: the decide grant gives
  none on `main`; visibility is judged at each of its reads.

## Tests per surface

The checks of `docs/checks.md`: `make lint`, `make lint-static`, `go
test -race -count=1 -timeout 30m ./...`; for PR-8 and PR-9 the app's
lint, `npm --prefix web run typecheck`, `typecheck:e2e`, `npm --prefix
web test` and `make test-e2e`. Every new test is proved able to fail.

- PR-6: docs only; no check applies.
- `internal/crew/ledger`, the step PR-7a:
  `TestRoutedLedgerMigrationFreshAndAgain` (the version's table made
  once, a run again a no-op),
  `TestOlderBinaryOpensARoutedLedgerReadOnly` (a binary of version 12
  opens a version 13 ledger read-only).
- `internal/crew/ledger`, the step PR-9a:
  `TestLedgerBarrierOpensOlderBinariesReadOnly`.
- `ext`, `ext/exttest` (PR-7): `TestRoutePolicyValid` (every bound of
  The policy, each just inside and just past),
  `TestPolicyOnlyWithARecord` (a policy on a screen's question is
  invalid), `TestRoutePolicyBound` (the fullest legal policy within 14
  KiB as encoded, escapes and all), `TestConfirmationValid`,
  `TestRerouteRequestValid`, `TestNotificationRecipientValid`,
  `TestNotificationKeyValid`, `TestDecisionRecordSubjectKey` (two
  subjects of each kind: two keys; one subject twice: one key; `Valid`
  refuses upper-case and short keys), `TestRoutePolicyScopeValid`,
  `TestResolutionOutcomeValid` (every final state, `Via` and `By` empty
  together), `TestRouterContractWithAPolicy`, `TestConfirmerContract` (a
  router that fetches on the call's path fails it with the network
  blocked).
- `internal/api`, `internal/crew`, `internal/crew/ledger` (PR-7):
  `TestAPolicyIsRecordedAsTheDecisionsPolicy` (a route with
  destinations and a `Policy`, through `routeRecord`, on a gate-opened
  record with a `for` and on one without: the policy written, rev + 1,
  exactly 2 `routed` rows with no op id),
  `TestCompareSelectionNeverTakesAPolicy` (a router answering a policy
  with `override: none` for every record: the selection keeps
  `local/1`, no `routed` row of the policy, and the workbench token's
  answer is accepted), `TestPutPolicyRefusesASelection`,
  `TestRouterChangesHaveTheirOwnBound` (32 operator comments, then 16
  router rows of `delegated` and `rerouted`, each with an op id: all
  written; a 17th router row refused `decision_limit`; a further
  operator comment refused only by the operator's own 32; a router row
  sent again answered as a repeat),
  `TestPolicyAfterAnswerChangesNothing` (I16),
  `TestRetoldRecordWithAPolicyWritesNothing` (a pass tells it again; the
  router answers another policy: nothing written),
  `TestEligibleSubjectAnswers`, `TestIneligibleDecidePrincipalIsRefused`
  (`404` at the read, `403 not_eligible` kept when its eligibility ends
  between the read and the transaction, held there by the engine's
  `beforeRespond` hook),
  `TestShareLinkControllerCannotAnswerARoutedDecision`,
  `TestOperatorNeedsAnExplicitOverride`,
  `TestOverrideNoneRefusesTheOperator`,
  `TestOverrideByAnAdminIdentityIsRechecked`,
  `TestRespondWithoutConfirmationIsStalePolicy` (a router-policy record
  handed to the engine with no confirmation: `409 stale_policy`,
  nothing written, never `local/1`; on a server whose router confirms
  policies, a `local/1` record given to the decide grant still
  answered by a decide principal, and one of the operator's by the
  operator, as on `main`),
  `TestPolicyNotInForceIsLocal1`, `TestStalePolicyRefused`,
  `TestStalePolicyRefusalIsNotKept` (I14),
  `TestBehindPastItsBoundIsNotInForce` (I10: a fake router answering
  `Behind` for ever; past 120 s of a fake clock the operator answers
  under `local/1`), `TestOpeningWindowRefusesTheOperator` (the router's
  `Route` held at a gate in the fake: the operator's response `409
  stale_policy`; released: routed),
  `TestConfirmFailingAtApplicationSupersedes`,
  `TestRequirementRequired`, `TestRoutersAnswerRefusedUnderItsPolicy`
  (I3), `TestGapKeepsTheRequirement` (I2),
  `TestScreenQuestionPolicyRefusedByTheGuard`,
  `TestMoreThanOneGroupRefusedUntilGroups`,
  `TestPolicyFromARouterWithoutConfirmRefused` (I17),
  `TestNoRouterNothingChanges` (I17: DR1's, HM1's and the decide grant's
  tests' records under a plain build, byte for byte),
  `TestRouterEntryIsTheOneCheck` (`TestAdmitsIsTheOneCheck` extended:
  the list and the read agree on every record under a policy),
  `TestPolicyColumnIsCanonical`,
  `TestOpeningUnderARouterReservesItsSpare` (the opening refused
  `decision_limit` when its spare would pass),
  `TestOutcomeOutboxWrittenInTheFinalTransaction`,
  `TestOutboxCarriesCancellationsAndSupersessions` (I15),
  `TestOutcomeSurvivesAPrunedRun`,
  `TestRevisionMismatchPastItsBoundIsNotInForce` (I10: a fake router
  answering `{InForce: true, Revision: r'}` for ever while the record
  holds r; past 120 s of a fake clock the operator answers under
  `local/1`), `TestBehindBoundClearedByAnInForceAnswer` (`Behind`, then
  in force for longer than 120 s, then `Behind` once: `409
  stale_policy`, not `local/1`), `TestTheHistoryCapHolds` (updated:
  the router group's 16 rows),
  `TestTheFullestDecisionFitsItsBound` (updated: the policy at 16 KiB
  with `Pinned` and `Recent` at their bounds, the receipt's group fields
  and 16 router rows, within `maxDecisionReply`),
  `TestTheOperatorsDecisionListFitsItsBound` and
  `TestTheFullestDecisionRecordThroughTheLane` (updated: the larger
  policy within their bounds); and `docs/plans/sync-seams.md`'s
  `TestDecisionDocWithTheMostResponsesFits`, with the policy at
  `MaxDecisionPolicy`, in whichever of PR-7 and that plan's row lands
  second.
- `internal/api`, `internal/crew`, `internal/crew/ledger` (PR-7b):
  `TestRerouteWritesItsHistoryKind`, `TestManyReroutesKeepTheDecision`
  (40 re-routes of one decision in a run at 1,000 decisions: no new
  decision, 16 history rows, `Reroutes` 40, `Recent` the latest 16, the
  counted responses kept, I11), `TestLostRerouteReplyPastTheHistory`
  (I14: 20 re-routes, the 20th's reply lost through a seam, sent again,
  and again after a restart: `Unchanged`, nothing written),
  `TestLateRerouteCannotRestoreAnOlderPolicy` (an older computation
  completing after a newer one: `ErrStale`),
  `TestRerouteCannotChangeTheRequirement`,
  `TestRequirementRequiredAndUnchangedByAReroute`,
  `TestUnsupportedFormToFullIsTheOneChange`,
  `TestRerouteOnlyForTheRouter`,
  `TestEveryFreshDecisionHasItsPlaceAtTheRunsLimit`,
  `TestFirstEligibleResponseAtTheRunsLimit` (a run at its last place
  opens a routed record with `override: none`; its first eligible
  response is accepted; its recheck fails at application; the
  replacement's first eligible response is accepted too),
  `TestReplacementChainHoldsOneSpare`, `TestRerouteRefusesASelection`,
  `TestVoidedRoutedAnswersBoundPerRun`,
  `TestOpenDecisionsPagesEveryRecord` (more than 1,024 open records
  across runs), `TestOpenDecisionsOnlyForTheRouter`,
  `TestRecordsToldPastMaxUnresolved` (I12),
  `TestOutcomeOutboxAckedOnlyByTheRouter` (I9),
  `TestRouteDecisionUnderAPolicyIsRefused`,
  `TestGrantRouteRefusedUnderAPolicy`,
  `TestDecisionLinkCannotAnswerARoutedDecision`,
  `TestForChangedWhileNotInForceAsksTheRouter`, `TestRouteToTheRouter`
  (and `409 no_router` with none),
  `TestRemovedEligibilityHidesTheDecisionBeforeTheReroute` (the re-route
  held back through a seam; the decide list, read and respond route all
  refuse at once), `TestDecidePrincipalSeesOnlyItsDecisions`,
  `TestDecideListLeavesOutUnconfirmedRows` (`cut` set),
  `TestRecipientNoticeReachesOnlyRecipientSinks`,
  `TestInboxShowsThePolicy`.
- Held answers (one rule of two plans, with `docs/plans/sync-seams.md`;
  the same names there), in `internal/api` and `internal/crew`, built
  in whichever of PR-7 and that plan's held-answer row lands second,
  against the other's code:
  `TestHeldAnswerRefusedWhereTheRouterConfirmsPolicies` (on a server
  whose router confirms policies, an admin's and a decide principal's
  held answers, to a record under the router's policy and to a
  `local/1` one: each `403 answered_at_server`, final, with its words;
  no row of the decision written; the fake router asked nothing; the
  store's `AdmitsAnswer` answering unknown: still
  `answered_at_server`, the store not asked),
  `TestEngineRefusesAHeldAnswerUnderAPolicy` (a `via: sync` response
  handed straight to the engine for a record under a router's policy,
  such a router registered, once with no confirmation and once with an
  in-force confirmation naming its subject eligible: each `403
  answered_at_server`, kept; with no
  router registered, the record not in force: judged under `local/1`
  by its entries), `TestHeldAnswerAdmittedByTheEntries` (table-driven,
  on a server with no router: an admin's `via: sync` answer accepted
  under `local/1`; a decide one accepted on a record given to the grant
  and refused on one that is not; `Sent` takes it as its subject's
  own), `TestHeldAnswerToASelectionIsRefused` (an admin's held answer
  to a comparison's selection: `403 not_approver`, kept, with its
  words; the workbench token's answer to it accepted).
- `web/app` (PR-8): vitest `utils/decisionPolicy.test.ts` (every `via`,
  a gap, not in force, an override),
  `components/DecisionRespond.test.ts` (the operator's disabled answer
  and Override…, its reason required), `utils/decisions.test.ts` (the
  decide page's new codes in words); `internal/cli`
  `TestDecisionRespondOverride`, `TestDecisionShowPrintsThePolicy`.
  Playwright: `web/e2e/decision-policy.spec.ts` (above), once more with
  `--repeat-each 5`, and `make test-e2e` whole in CI.
- PR-9: `TestCombinedGroupsAnswerOnceAllMet`,
  `TestQuorumCountsDistinctSubjects`, `TestDistinctAcrossGroups`,
  `TestDistinctAssignmentFindsTheMatching`,
  `TestOnePersonInTwoGroupsWithoutDistinct`,
  `TestAnEligibleRejectAnswersAtOnce`,
  `TestCountedResponsesKeepTheirPlaces`,
  `TestPartialApprovalSurvivesARestart` (confirmed counted responses),
  `TestUnconfirmedCountedResponseAfterARestartIsRefused`,
  `TestCountedApprovalVoidedByASignInRevocation`,
  `TestCountedApprovalVoidedWhenItsAuthorityCannotBeEstablished` (the
  fake router's `Confirm` with `Since` answering not eligible),
  `TestCountedResponsesRecheckedAtApplication`,
  `TestSurplusApprovalsTakeNoRow` (three groups of quorum four, eight
  eligible in each; all sixteen of the first two groups approve first:
  eight counted, eight `not_needed` and unkept; the third group's four
  complete it, I11), `TestRefusalsLeaveRoomForUnmetPlaces` (refusals
  past the rows the unmet places need are unkept),
  `TestShrinkingEligibilityPastTheRowsSupersedesWithItsPlace` (at the
  run's 1,000 decisions, I11); vitest for "1 of 2" and the decide view's
  `counted`.

Every router in these tests is `exttest.FakeRouter`; every identity is
`exttest.FakeIdentity`. No real provider, account or service.

## Boxcar constraints

The eight of the Program's section 2.5:

1. The engine starts an attempt only through `crew.Launcher`: not
   touched; a decision's application is DR1's, through the engine.
2. `session.Local` drives a `session.Process`: not touched.
3. An attempt's context is files in its worktree: not touched; an
   answer reaches an attempt as DR1 delivers it.
4. The agent talks to the server over the network: not touched.
5. Checks go through `internal/proc`: not touched.
6. Platform splits are platform tags and the app's screens show a
   capability: respected; the router is `GET /api/capabilities`'s
   `decisions` row as on `main`, and the app shows a policy the record
   holds, never a plan.
7. Evidence rows' `where`: not touched.
8. `hostagent` is a launch site: not touched; decision records are a
   managed run's, on the server.

## Open-core checklist

- [x] Article 1: no paid code, licence check or plan check; the one
  check is a router's registration, and whether it implements
  `PolicyConfirmer`.
- [x] Article 2: a plain build has no router, so every decision is
  `local/1` and every path is `main`'s.
- [x] Article 3: no flag, setting, tag or test-only binary; a policy
  exists only in a registered router's answer.
- [x] Article 4: no code here answers, expires or lowers a requirement;
  with no policy in force the operator answers; stop, inspect, revoke,
  cancel and export are untouched.

## The PRs

The open-source rows of SW1, in this repository. The rows are units
of work; how rows are grouped into pull requests follows `AGENTS.md`
(Working an issue) at the time they are built. Each row is built with
the checks of its surfaces and the open-core checklist; each leaves
`main` working and green on its own, and with no router registered
changes nothing. A provider that routes by this seam waits for a tag
holding PR-7a, PR-7, PR-7b and PR-8, and a later one holding PR-9a
and PR-9.

**The ledger's steps.** The ledger holds data on every server, so a
step to it is a row of its own, shipped first and alone, right before
the code that uses it, so that it can be reverted alone. PR-7a is the
one step for PR-7 and PR-7b together: the next schema version, a
barrier, with every table those rows need (the outcome outbox). It
covers what PR-7b writes because PR-7 already writes every shape the
ledger holds by PR-7b's code: the spare and its place, the outbox row
in a final transaction, and the router's history rows in their own
group are PR-7's; PR-7b adds the code that drives them (`Host.Reroute`,
the outbox's reads and acknowledgement) and no shape of its own. PR-9a
is PR-9's barrier, a second step and a version of its own: it exists to
keep a build of PR-7 to PR-8, which counts no approvals, read-only on a
ledger that holds `counted` rows, which a barrier folded into PR-7a
could not do. The code rows say "after the step".

| PR | Title | Contains | Touches | Tests | Depends on | Parallel with | Size (code + tests) |
|---|---|---|---|---|---|---|---|
| PR-6 (#997) | plan: a decision router's policy on decision records | this file | `docs/plans` | none: docs only | none | none | about 1,580 lines of docs |
| PR-7a | ledger: the next schema version, a barrier for a router's policy, with the outcome outbox | the migration alone: `decision_outbox` and its unique index, the version's barrier | `internal/crew/ledger` | `TestRoutedLedgerMigrationFreshAndAgain`, `TestOlderBinaryOpensARoutedLedgerReadOnly` | PR-6 | none: it ships alone, right before PR-7 | about 30 + 120 |
| PR-7 | ext, api, crew: a router's policy on a decision record, one group: eligibility at response and application, the override, the fallback, the opening window | every `ext` addition (the record with its `Subject` key, `RoutePolicy` with `Pinned` and `Scope`, `PolicyConfirmer`, the four `Host` methods declared, `Resolution.Outcome`, `Notification.Recipient`, `Key`, `RecipientSink`), `ext/exttest`'s fakes and checks, `MaxDecisionPolicy` at 16 KiB, `Tx.PutPolicy` (refusing a selection), the spare from the opening and its place, the outbox row written in a final transaction, the router's history group, the `eligible` entry, the policy's transaction, the respond judgement and the override, `stale_policy` unkept, the application's confirm, the server's 120 s bound, `policyNow`, the decide words, `docs/protocol.md` | `ext`, `ext/exttest`, `internal/api`, `internal/crew`, `internal/crew/ledger`, `docs` | the PR-7 lists | after the step PR-7a | none: every later row builds on it | about 850 + 750 |
| PR-7b | api, crew, ledger: re-routes, route changes under a policy, visibility, outcomes and recipients | `Host.Reroute` (its rev check, a repeat by its revision, `Pinned` and `Scope` unchanged, the one change of form, `Reroutes` and `Recent` past the history's 16), `Host.OpenDecisions` page by page, `Host.Outcomes` and `AckOutcomes` over the outbox PR-7 writes, a replacement chain's one place, the voided routed decisions' bound, the route operation's `router`, `routed_by_policy`, a `for` change telling the router again, visibility following eligibility on the decide routes, `Resolution.Outcome` filled, notices with a `Recipient`, MB0's routed fields | `internal/api`, `internal/crew`, `internal/crew/ledger`, `docs` | the PR-7b list | PR-7 (after the step PR-7a) | PR-8 | about 500 + 550 |
| PR-8 | web, cli, e2e: who may answer, the gap, the override, the policy in words | `utils/decisionPolicy.ts`, `DecisionCard.vue`, `DecisionRespond.vue`, the decide page's words, `conductor decision show` and `respond --override`, `web/e2e/decision-policy.spec.ts` | `web/app`, `internal/cli`, `web/e2e` | the PR-8 list | PR-7 | PR-7b | about 450 + 400 |
| PR-9a | ledger: the schema version after PR-7a's, a barrier for counted responses | the migration alone, of no statements | `internal/crew/ledger` | `TestLedgerBarrierOpensOlderBinariesReadOnly` | PR-7b, PR-8 | none: it ships alone, right before PR-9 | about 10 + 60 |
| PR-9 | ext, crew, ledger, api, web: groups, quorum and distinct persons on a decision record; the features entry | the `counted` response, the coverage and the rows kept for unmet places, the combined answer, `RoutePolicy`'s full shape taken, the card's "1 of 2", the decide view's `counted`, `docs/features.md` | `ext`, `internal/crew`, `internal/crew/ledger`, `internal/api`, `web/app`, `docs` | the PR-9 list | after the step PR-9a | none | about 700 + 700 |

PR-6 to PR-8 (with the step PR-7a and PR-7b) give one group end to
end: a decision routed
to a role's holder or a person named by the router, answered only by
them, rechecked when applied, overridden only explicitly, and reported
when it was answered without its policy. PR-9 adds counted approvals
without changing a table, a field or a contract the earlier rows made:
`ext.RoutePolicy` holds the whole model from PR-7.

## Owner decisions

Dated 2026-11-18. Each is a default; the owner's answer can add or
remove rows, and each says which.

1. **The first human policy a router may send** (default, option A):
   groups of the four responsibilities or any role name of the form,
   one group of quorum 1 first (PR-7), then up to four groups with
   quorums up to four and distinct persons (PR-9); delegates eligible
   beside the person they stand for, answering as themselves; an
   override by the server's operator alone (the admin grant),
   explicitly, with a reason, where the policy says `operator`. Reason:
   the smallest policy that routes a plan's gates, keeps one answerer
   per decision until the record can count several, and never lets a
   routed approval be skipped silently. Other answers and the rows
   they change:
   - **One group only for now**: PR-9 leaves this plan for a later one;
     `MaxPolicyGroups` stays 4 in `ext` (the shape stays); PR-6, PR-7,
     PR-7b and PR-8 unchanged.
   - **No override**: PR-7 drops the override path and
     `override_refused`, PR-8 the Override… dialog and `--override`; a
     gap then waits for a re-route. The `override` field stays, fixed
     at `none`. PR-7b and PR-9 unchanged.
   - **An override by a principal of another grant** (beside the admin
     grant): a new grant in `ext` (an identity provider's), accepted by
     PR-7 beside the admin grant's override and offered by PR-8.
     PR-7b and PR-9 unchanged.
   - A router's own set of roles, or no delegates, change no row here:
     `ext` takes any role name of its form, and `via: delegate` stays.
2. **Where a policy is edited** (default, option A): with the router's
   module, outside the app's screens; the app's screens show the
   policy's views only (who may answer, the gap, the override, the
   route's explanation), open source in PR-8. No seam carries a write
   from the app to a provider. The other answer, an editor in the app's
   screens (a Routing screen in Settings for the operator through a new
   seam, `ext.PolicyEditor`: read the policy, set and remove what it is
   made of), adds a new open-source row after PR-6 (`ext`,
   `internal/api`, `web/app`, about 900 lines) planned in this file,
   and changes no other row here.
3. **A router routes decision records only.** A question on a screen
   with no record is answered by whoever controls the terminal (first
   response wins), so a route could not restrict who answers it; a
   policy on one is refused by the guard
   (`TestScreenQuestionPolicyRefusedByTheGuard`).
4. **A policy not in force is `local/1`.** No router, no answer, or
   `InForce` false: the operator answers without an override, recorded
   with the why. Article 8 asks for the free default, and the free
   default is the operator; an approval is still required, never
   skipped, and the outcome is kept for the router (I9).
5. **Silence is never approval, and nothing expires.** No code here
   writes the `expired` state DR1 keeps for a provider, or a response
   on a router's behalf; a router's held answer stays refused (DR1's
   Owner decisions, 8, unchanged).
6. **The app's spec runs on the shipped build** (the decide grant's
   Owner decisions, 15, on `main`): `decision-policy.spec.ts` drives the
   real pages against the server's replies stubbed in the browser, and
   the server's half is held by the Go tests on the real handlers with
   `exttest.FakeRouter`. **This is open**: the other reading is a
   test-only `main` package (new in that reading, `internal/e2erouter`,
   never released, no build tag) that serves the open-source server with
   `exttest.FakeRouter` registered and a loopback-only control route
   for the spec, so the browser meets a real server's refusals end to
   end. Recommended: the shipped build, as `main` decided for the
   decide grant (no machinery for one spec); the other changes PR-8
   alone (about 200 more lines).
7. **`stale_policy` is an unkept refusal under a router's policy.**
   DR1 keeps a `stale_policy` refusal on `main`, where it is final (the
   policy can only be `local/1`); here it means "try again", and a kept
   refusal would answer the retry with itself. So it takes no row and
   is judged again when sent again.
8. **The server holds the retry bound itself.** `Behind`, `Routes` and
   a `Revision` other than the record's are the router's promise to
   settle within 120 s; the server keeps, per record and in memory, a
   clock started at the first such answer after one that was none of
   them and cleared by any other answer, and treats such an answer past
   `ext.MaxBehind` on that clock as not in force, so a router
   that never settles cannot make a decision unanswerable by the
   operator (article 4). A restart starts the count again. The opening
   window has a bound of its own measured from the commit (twice
   `ext.RouteWait`), since on `main` a record's tell waits in a queue
   for a worker and a call slot.
9. **One ledger step for PR-7 and PR-7b, a row of its own (PR-7a).**
   A step to a store that holds data ships alone, right before the code
   that uses it, so that it can be reverted alone. PR-7a's version is a
   barrier (as versions 10 and 12 are) against every binary before it,
   and its migration makes the outbox table. It covers PR-7b because
   every shape the ledger holds by PR-7b's code is written by PR-7's
   code already (the spare and its place, the outbox row, the router's
   history rows), so a build of PR-7 alone reads a ledger PR-7b wrote
   as PR-7b does. PR-9's barrier is a second step (PR-9a), since only a
   version after PR-8's builds keeps them from a ledger that holds
   `counted` rows. The other reading, PR-7 and PR-7b shipped in one tag
   with no build between and never reverted apart, would leave the
   spare and the outbox's writes in PR-7b; it changes PR-7 and PR-7b
   only.
10. **A comparison's selection is never answered by a held answer**
    (default). A selection is the operator's alone, and it is answered
    only by a principal authenticated at this server itself (the
    workbench token, or an identity provider's principal with the admin
    grant); a held answer to one (`via: sync`) is refused `403
    not_approver`, kept, with words that say so, whatever grant its
    subject holds (`TestHeldAnswerToASelectionIsRefused`). On a server
    whose router confirms no policies, every other record judges a held
    answer by its entries, `via: sync` taken as `via: identity`
    (`TestHeldAnswerAdmittedByTheEntries`); where it confirms policies,
    decision 11 holds. The other
    way: an admin subject's held answer counts as the operator's for a
    selection too; it changes PR-7's respond judgement and that test,
    and `docs/plans/sync-seams.md`'s door, and no other row.
11. **A held answer and a router's policy do not combine in the first
    version** (the default; decided by the owner on 2026-11-18: it
    stands). On a server whose registered router confirms
    policies, a sync provider's door refuses every held answer, final,
    `403 answered_at_server`, nothing written and the router not asked,
    and the engine refuses a `via: sync` response to a record under a
    router's policy
    (`TestHeldAnswerRefusedWhereTheRouterConfirmsPolicies`,
    `TestEngineRefusesAHeldAnswerUnderAPolicy`). What a person sees
    meanwhile: on such a server a decision is answered at the server's
    own routes by whoever its policy admits (the operator, or the people
    its router names on the decide page), and an answer given on another
    device is refused with words that say where to answer. The other
    way, a held answer judged under the router's policy, is a later
    design, which must first solve three things: a held answer is bound
    to the revision it was given for, and a policy written after it
    raises the revision; a held answer that waits for a silent router
    needs a bound that nothing else resets; and a response that carries
    no confirmation must be refused without closing the decide grant's
    own route. It changes PR-7's respond judgement and its tests, and
    `docs/plans/sync-seams.md`'s door.

## Acceptance

The boundary, in this plan's words: **under a registered router's
policy in force, only the persons it names answer a decision, every
answer is confirmed again when applied, the operator answers only by an
explicit override where the policy allows it, and whenever the policy
is not in force the operator answers as in the free build, with the
answer kept for the router; with no router, nothing changes.**

Observed in two steps, each when its rows' checks pass in CI and
locally:

1. PR-7, PR-7b and PR-8: `TestNoRouterNothingChanges` and DR1's, HM1's
   and the decide grant's tests unchanged show the free build as
   `main`; with `exttest.FakeRouter`, `TestEligibleSubjectAnswers`,
   `TestIneligibleDecidePrincipalIsRefused`,
   `TestOperatorNeedsAnExplicitOverride`,
   `TestOpeningWindowRefusesTheOperator`,
   `TestPolicyNotInForceIsLocal1`,
   `TestConfirmFailingAtApplicationSupersedes`,
   `TestRouteDecisionUnderAPolicyIsRefused`,
   `TestDecidePrincipalSeesOnlyItsDecisions` and
   `TestOutcomeOutboxAckedOnlyByTheRouter` show the boundary held at the
   routes; `decision-policy.spec.ts` shows the screens saying it.
2. PR-9: `TestCombinedGroupsAnswerOnceAllMet`,
   `TestDistinctAssignmentFindsTheMatching`,
   `TestSurplusApprovalsTakeNoRow` and
   `TestCountedApprovalVoidedWhenItsAuthorityCannotBeEstablished`.

Done is the five rows merged with those checks green, and the features
entry naming what ran. A live check with a real router is the module's
that registers it. Nothing here needs the owner but the review, the
answers above and the merges.
