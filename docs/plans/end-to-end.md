# The first end-to-end workflow: one project, two agents, a person who owns the decision, and what the open-source build proves of it (issue #1034)

Status: proposed 2026-11-19, for the owner's review before any of its
code. Its rows (WF-1, WF-2, WF-3, The PRs) are units of work, grouped
into pull requests as `AGENTS.md` (Working an issue) says at the time
they are built. This file is the row WF-0.

Conductor's parts are each planned, built and tested on their own:
Dispatch's tickets and plan revisions, Yardmaster's managed runs, the
durable decision and its wait, the decide grant and the decision page,
Brakeman's briefs, reviews and repairs, and the seams through which a
registered provider routes a decision, answers who a person is, gives
an attempt context or carries records to other machines. This plan is
the first workflow that crosses all of them in one walk, as a team will
meet it:

- one project, its repository and a ticket that says what done means;
- one **implementing agent** and one **reviewing agent**, each an
  attempt of a managed run;
- one person, the **architecture owner**, who owns the architecture
  decision the implementing agent raises and answers it from a phone;
- the **operator**, who runs the server, approves the plan and accepts
  the result.

It says what a person sees at each step, what the server's records
hold, and what must never happen, at each step and under eight
conditions a team meets: a stale card, a duplicate tap, a responder
whose access was withdrawn, context unavailable, the owner
disconnected, an agent restarted, the sidekick away, an entitlement
that ran out. It says what **truthful state** means for each, and how
this repository proves it: one walk, in Go, against the real server
with stub agents and the fakes of `ext/exttest` in the provider slots.

This plan is about what the open-source build does and offers. What a
registered provider does behind its seam (how a router picks the
person, how an identity provider admits a pass, how context is made,
where a record is carried) is the business of the module that
registers it, and its own acceptance.

It was written against `main` at `c3a98a6e` (2026-11-19), after reading
`docs/plans/decisions.md` (DR1 and HM1: the record, its states,
orderings and invariants), `docs/plans/decide-grant.md`,
`docs/plans/decision-inbox.md` (MB0: the decision page and its four
rows), `docs/plans/decision-policy.md`, `docs/plans/sync-seams.md`,
`docs/plans/context-seams.md`, `docs/plans/brakeman.md`,
`docs/plans/dispatch-evidence.md`, `docs/plans/review-repair.md` and
`docs/plans/ext.md`; `ext/registry.go`, `ext/entitlements.go` and
`ext/exttest/fakes.go`; `internal/api`'s test harness
(`newTestEnvAgents`, `WithExtensions`, `withIdentity`, the decide
fixture and its `gatedIdentity`, `decideSeams`) and
`internal/crew`'s `beforeRespond`; and the e2e specs of the screens the
workflow crosses.

**Where this starts: on `main` today.** Every step but the routing to
one eligible person and the recorded context is built:
DS1 and DS2 (tickets, acceptance criteria, plan revisions, their
approval, launches, candidates, acceptance, delivery links), Yardmaster
with EN1 and C10, DR1 and HM1 (the decision record, its gates, its
response and application, the people and their responsibilities), the
decide grant (#776), MB0 (the decision page and its four rows), BK0 to
BK2 (Brakeman's requests, briefs, draft plans and acts), RV1 (a
person's review, findings, a repair), C1 to C9 (every provider seam,
with `ext/exttest`'s fakes). Each has its own tests. None has been
walked end to end with every provider slot filled.

**Not on `main`, and what waits for it.** Three public plans add what
the walk's last parts need, and those parts (WF-2) wait for their rows:

- `docs/plans/context-seams.md` (CT-1 to CT-3): the context each
  attempt was given, recorded and shown, and the lookup.
- `docs/plans/decision-policy.md` (its rows PR-7a, the ledger's step,
  and PR-7, one group): a registered router's policy names who may
  answer, and only they do.
- `docs/plans/sync-seams.md` (OS-3): an answer given while the owning
  server is out of reach, held by a sync provider and handed in once.

**Names.** The **owning server** is the server whose ledger holds the
run: the execution owner, as `docs/plans/decision-inbox.md` calls it.
"The owner disconnected" is that server out of the person's reach. The
**card** is a decision as a person reads it: the decision page's card,
the decide page's, the run page's. A **fake** is a provider of
`ext/exttest`; a **stub agent** is a catalog agent whose program is a
shell line, which the walk speaks for through its session's own token.
Conductor is the application, Yardmaster the harness that runs the
tasks, holds a decision's wait and applies its answer, Dispatch the
ticketing component, Brakeman the sidekick that briefs.

## Articles it serves

The articles are the Program's (section 2.1).

1. **No paid code.** The plan adds a test and a record; the walk fills
   the provider slots with `ext/exttest`'s fakes, which a registered
   provider is checked against.
2. **The free build is fully useful.** The walk is also run with no
   provider at all (the free walk), and every step but those a provider
   gives holds there.
3. **Gated by hosted dependencies.** The walk's providers are fakes
   registered through `ext`; no flag, setting or tag.
4. **Stop, inspect, revoke, export and required approvals stay free.**
   Every condition is judged by these: a required approval is never
   skipped, a lapse never runs gated work ungated, the operator can
   always stop the run or answer under the free policy.
5. **Dependencies point one way.** The walk lives in `internal/api`'s
   tests and imports `ext/exttest`, as the existing tests do.
6. **No feature build tag.** The walk runs in the ordinary race suite.
7. **The free default is what `main` does.** The free walk is that.
8. **A provider that fails is the free default.** Four of the eight
   conditions are this article, observed.
9. **CGO off.** Unchanged.
10. **The repository contract.** A plan before its code; race tests;
    the walk proved able to fail (Tests per surface).
11. **Names keep their meaning.** "Decision", "card", "context",
    "sidekick" as the plans above use them.
12. **Execution behind one seam.** The walk launches only through the
    engine: Dispatch's launch from an approved revision.

## What truthful state means

Truthful state is the promise that what a person is shown, and what the
server's records hold, is what happened and nothing more: a step done is
shown done when its record says so, a step not yet done is shown
waiting, a step the server cannot know is shown unknown with its words,
and nothing is ever inferred from silence, from a timer or from a
provider that did not answer.

**For a decision**, the four facts the decision page shows as four rows
that never merge (`docs/plans/decision-inbox.md`):

1. **Available**: the request is in the ledger, open at its revision,
   with who may answer it now, and it is the one the card shows.
2. **Recorded**: a response is in the ledger with its receipt (who, for
   whom, when, which revision they answered), or the refusal is, with
   its words.
3. **Validated**: Yardmaster took the response and rechecked it at
   application: the responder's authority asked again of the identity
   provider, the subject still current, the policy still in force; or
   the recheck refused, and the decision asks again.
4. **Resumed**: the held work started (the gate released, the answer
   typed into the asking attempt or handed to its next attempt), or the
   reason it did not.

Each is done, waiting, refused, does not apply, or unknown; a step once
done or refused is never shown going back. Beside them, five promises
the walk checks after every step and every condition:

- **No accepted response but an eligible person's.** A response from
  anyone the decision does not admit now is refused; its refusal may be
  kept as a refused row, never counted, never applied. On `main` the
  refusals the walk checks are a principal whose access was withdrawn,
  a principal without the decide grant, a share link, an agent and
  Brakeman; while a decision is given to the decide grant every holder
  of that grant is eligible. A person the decision was not routed to is
  refused only under a router's policy (WF-2b).
- **Applied at most once.** One accepted response, one application, one
  release of the held work, however many taps, retries, restarts or
  hand-ins (DR1's I1).
- **Silence is never approval.** No timer, default choice, unanswered
  router or absent provider moves a decision out of `open` (DR1's I5).
- **A lapse never lets gated work run ungated.** A provider that stops
  (late, failing, lapsed, withdrawn) leaves every gate where it was;
  the free policy (the operator answers) takes over, and a held task
  starts only after an accepted response's application (DR1's I2, I14).
- **No work runs twice.** No attempt is started twice for one step, no
  answer typed twice (DR1's I6), no candidate recorded twice.

**For context**, truthful is: what the run page lists is what each
attempt was given when it was given it, recorded before the agent had
it; an attempt given nothing shows nothing; a provider away is said,
never filled in (`docs/plans/context-seams.md`).

**For the work**, truthful is: a plan approval that no longer holds says
so; a candidate is shown with the receipts of the checks it passed or
failed; a ticket's acceptance is the operator's, recorded for the
ticket's owner (`accepted_for`) against its criteria as they stand
(`accepted_criteria`), and reads unmet once either changes.

## The workflow, step by step

Each step: what a person sees, what the records hold, what must never
happen, and what holds it on `main`. "The walk" is WF-1's test.

1. **A ticket says what done means.** The operator files CND-12 in the
   project, with its acceptance criteria and its owner. *Sees*: the
   ticket with its criteria. *Holds*: Dispatch's ticket at revision 1,
   its operation's receipt. *Never*: a ticket changed by anything but an
   operation with a receipt. *On main*: DS1.
2. **Context of the exact commit is prepared.** The run's base is a
   commit. *Sees*: nothing yet. *Holds*: nothing of the server's until
   an attempt starts (the provider is told the base when the run starts,
   context-seams CT-1). *Never*: context of another commit shown as
   this one's. *On main*: C4's seam; the base note waits for CT-1.
3. **Brakeman drafts the plan.** Asked about CND-12, Brakeman's brief
   carries a draft plan revision: the crew (implementer, then
   reviewer), the checks, and the people with their responsibilities
   (ana the architecture owner). It names no required decision: a
   plan's required decision is a gate DR1 opens in the run's first
   transaction, held until it is answered the way steps 8 to 11 answer
   one, and this walk follows the one decision the agent raises.
   *Sees*: the brief and its
   draft. *Holds*: Brakeman's request and brief, never a revision: a
   draft becomes a revision only when a person proposes it. *Never*: a
   draft approved, launched or proposed by Brakeman. *On main*: BK1,
   BK2.
4. **The operator approves the plan revision.** *Sees*: the revision,
   its decisions, Approve. *Holds*: the revision `approved` with its
   receipt, against the ticket's revision then. *Never*: an approval
   that no longer holds (the ticket edited since) launched; it says so
   and asks again. *On main*: DS2.
5. **Yardmaster starts the managed run.** Launched from the approved
   revision. *Sees*: the run, its tasks, its people. *Holds*: the run's
   first transaction, its launch recorded against the revision.
   *Never*: a run launched from a revision whose approval no longer
   holds. *On main*: DS2, Yardmaster.
6. **The implementing agent raises an architecture question.** Its
   attempt calls `decision_request` (approval, for
   `role:architecture`, with its alternatives, recommendation,
   uncertainty and evidence). *Sees*: the task "waiting for decision
   dr-7"; the card. *Holds*: the decision `open` at rev 1, its hold, its
   history row, `decision.opened`. *Never*: the agent told it may go on
   with the dependent work. *On main*: DR1.
7. **Yardmaster keeps it and holds only the asking task.** *Sees*: the
   asking task waiting for dr-7; the run's other tasks not held by it
   (the reviewer's waits on its needs, as it would anyway). *Holds*: the
   hold on the asking task alone (DR1's Opening). *Never*: the asking
   task dispatched again before the application; another task held by
   dr-7 (DR1's I2, I12). *On main*: DR1.
8. **It is given to someone who may answer.** On `main`: opened for
   `role:architecture`, the card says "for ana, the architecture
   owner", and the operator answers; the operator then gives it to the
   decide grant. *Sees*: the card "for the decide grant"; any decide
   principal may answer, beside the operator. *Holds*: the request's
   `for` rewritten to the grant with no person, the `routed` row with
   what it was for before, rev + 1; the router told of it (C5).
   *Never*: a principal without the decide grant, or whose access was
   withdrawn, able to answer. With a router's policy (decision-policy's
   PR-7a and PR-7, WF-2b) the card says "for ana, the architecture
   owner" and only ana may answer. *On main*: HM1, C5, the decide grant;
   the policy waits for its rows (WF-2b).
9. **Brakeman prepares a brief.** *Sees*: on the operator's decision
   page, the item with the brief: what happened, the evidence, the
   uncertainty, a recommendation marked as Brakeman's. The decide page
   a decide principal reads shows the decision's own words and no
   brief. A link's page of the run keeps Conductor's brief on the
   record's item as built in #1046; the decision inbox's plan gives a
   link's item Brakeman's card, and whether a record's item does the
   same on a link is the owner's to say. *Holds*: the brief in
   Brakeman's records, its card about the decision record (a
   `decision/1` whose `about` is `decision:dr-7`, at the rev its turn
   read, of what its request is about: an unknown, closed or other
   run's record refused), its refs to the decision and the run.
   *Never*: the brief standing in for the decision's own words, or
   answering. *On main*: BK1, with the decision record's card (#1046);
   context's lookup once CT-2 lands.
10. **The person opens the card on the phone and responds.** The
    architecture owner opens the decide page with their pass and
    approves, with a rationale. *Sees*: the card at the rev read; after
    Approve, Recorded. *Holds*: the response row with its authority
    (the decide grant, the subject), its receipt, the decision
    `answered`. *Never*: the pass in a URL or a log; a response at a rev
    the card did not read. *On main*: the decide grant, MB0.
11. **Yardmaster validates and applies it once.** *Sees*: Validated,
    then Resumed. *Holds*: the identity provider asked again after the
    response's commit (`ConfirmAuthority`), the application receipt,
    `applied`, the hold released, `decision.applied`. *Never*: an
    application before the recheck says yes; a second application.
    *On main*: DR1.
12. **The work goes on to a candidate, its checks and its review.** The
    held task starts, the implementer submits `9ab1…`, its checks run,
    the reviewing agent's attempt starts at the candidate (with the
    context of that commit, CT-1) and submits `approved` with no
    findings. *Sees*: the candidate with its receipts; the review's
    verdict. *Holds*: attempts, receipts, the result, the review
    attempt's verdict; with CT-3, each attempt's context rows. (A
    review that submits `needs_changes` reopens the task at once with
    every one of its findings, before any person acts: that is a repair
    of its own, not this walk's step 13.) *Never*: a review of another commit than the candidate; an
    attempt started twice. *On main*: Yardmaster, RV1; context waits for
    CT-3 (WF-2).
13. **A person batches their findings into a repair.** The operator
    reviews the candidate in the ticket's Review view and sends Request
    changes with two notes of their own. *Sees*: the review, its notes,
    the repair attempt. *Holds*: Dispatch's review with its two notes
    and its application, the repair attempt with exactly those two
    changes in its task file. *Never*: a note the person did not send
    handed to the repair. *On main*: RV1.
14. **Dispatch records the outcome.** The repaired candidate passes its
    checks; the operator accepts it for the ticket's owner against its
    criteria; a delivery link is added. *Sees*: the ticket with its
    candidate, checks, decisions, acceptance and delivery. *Holds*:
    every one of those as Dispatch's and the ledger's rows, the
    acceptance with `accepted_for` the owner and `accepted_criteria` the
    criteria's hash. *Never*: an acceptance read as met for an owner or
    criteria other than the ticket's now; a ticket closed against its
    close policy. *On main*: DS2.

## The conditions

Each condition: how the walk makes it, what a person sees, what the
record holds, what must never happen.

**A stale card.** The person reads dr-7 at rev 2; before they tap, the
decision moves (the operator comments, or a new candidate supersedes an
approval of the old one). *Made by*: a comment between the read and
the response, held at `decideSeams.afterRead`. *Sees*: "dr-7 changed
since you read it", with what changed, and the card at its new rev.
*Holds*: the response refused `stale_decision`, a refused receipt with
the history since rev 2; the decision still `open`. *Never*: the
answer applied to a decision the person did not read.

**A duplicate tap.** The person taps Approve twice; the reply of the
first is lost and the phone retries; the operator answers too. *Made
by*: two sends with one `responseId`, then another principal's.
*Sees*: one receipt, the same each time; the operator is told who
answered first. *Holds*: one accepted response, one application, the
repeat answered from the first receipt, the later one refused
`decision_answered`. *Never*: two applications; two releases.

**A responder whose access was withdrawn.** The identity provider stops
admitting the architecture owner's pass: before they respond, and, in a
second run of it, between their response's commit and its application.
*Made by*: `FakeIdentity.Remove`, the second time with the recheck held
at the `gatedIdentity` gate. *Sees*: the first, the decide page says
the access has ended; the second, the card says "ana's answer could not
be rechecked; dr-8 asks again", for the operator to answer.
*Holds*: the first, no row; the second, the response kept, the decision
superseded by a fresh one with its holds, nothing applied (DR1's I13).
*Never*: an answer applied that the provider did not admit again after
it was recorded.

**Context unavailable.** The context provider fails, is late or is not
started when the reviewer's attempt starts, and when it looks up.
*Made by*: `FakeContext.SetErr`, a provider held past its wait. *Sees*:
no Context section in the task file; the lookup's words "context is
unavailable now; carry on without it"; the run page lists nothing for
that attempt. *Holds*: no context row (CT-3); the attempt started on
time. *Never*: the attempt waiting past `ext.ContextWait`, failing for
the provider, or shown context it was not given.

**The owner disconnected.** The owning server is out of the person's
reach when they open the card (stopped, or its address unreachable).
*Made by*: the walk's server stopped and started again over its data
directory, the same fakes registered with the new server (the later
servers ended first, `AGENTS.md`). *Sees*: the
decide page cannot reach the server and says so; nothing is shown as
sent. When the server is back, the card at its rev, still open.
*Holds*: nothing while it is away; the decision open, its holds kept,
across the restart. With a sync provider (sync-seams OS-3, WF-2): an
answer the provider held is handed in once, recorded through DR1 only
when the identity provider admits its statement, and applied once; on a
server whose router confirms policies it is refused
`answered_at_server`, the card saying the owning server must be
reachable. *Never*: an answer shown applied that the owning server has
not applied; one applied twice. The person being away from their phone
is the plain case of silence: nothing is recorded, nothing is approved.

**An agent restarted.** The implementing agent's session ends while
dr-7 is open; and, in two more runs of it, the server restarts between
a response and its application. *Made by*: the stub's session stopped;
the server stopped and its run continued (a new server over the same
data directory, the fakes registered again: a restart drops what a
provider was given, and the walk hands the new server the same fakes).
*Sees*: "waiting for decision dr-7"; the attempt given back, not
counted. When the operator answered (the workbench token), the answer
is moved to the continuation and applied once there, and the next
attempt has it in its task file. When ana answered (her decide pass, an
identity), the card says her answer could not be rechecked and a fresh
decision asks again, for the operator to answer. *Holds*: the refund
row; the operator's answer moved and applied once; ana's response kept,
its decision superseded by a fresh one carrying its holds, nothing
applied (DR1's I13). *Never*: an attempt counted against the task for
waiting; an answer typed twice; an identity's response applied after a
restart (DR1's `TestAnIdentityAnswerIsNotAppliedAfterARestart`).

**The sidekick away.** Brakeman is not configured, or its turn ends
with no brief. *Made by*: the walk once with no Brakeman desk, once
with a turn whose session ends. *Sees*: the card with the decision's
own words, no brief, and (for the turn) "Brakeman's turn ended with no
brief". *Holds*: Brakeman's request `ended` (or none); the decision
unchanged. *Never*: a brief invented; the decision waiting on Brakeman
to be answerable.

**An entitlement that ran out.** The providers stop giving what they
gave, as providers whose entitlement ran out would: the router answers
no destination, the context provider no block, the identity provider no
longer admits the decide pass. No code of this repository asks the
entitlement source, so the lapse reaches the server only as these
answers. *Made by*: the decide pass removed (`FakeIdentity.Remove`),
the router and the context provider set to their empty answers.
*Sees*: the decision still given to the decide grant, which no pass now
reaches; the operator answers it on the operator's card, or takes it
back (a route to the operator); the run page's state as it was.
*Holds*: the gates and holds unchanged; no response but the
operator's accepted; no code of this repository asked the entitlement
source (`FakeEntitlements.Asked` empty). *Never*: a held task started
because a provider stopped; a decision closed or approved by the lapse;
stop, inspect, export or the operator's answer refused.

## The walk (WF-1, WF-2)

**Where.** `internal/api/workflow_test.go`, package `api`, in the
ordinary race suite: the real server through `newTestEnvAgents` with
`WithExtensions` and `local.Fill`, over a data directory and one git
repository the test makes (`gitRepo`), with a Dispatch project.

**The provider slots**, each a fake of `ext/exttest`:

| Slot | Fake | What the walk makes it do |
|---|---|---|
| identity | `FakeIdentity` | admits the operator's pass (`GrantAdmin`), the architecture owner's (`GrantDecide`), an unrelated person's (`GrantDecide`: eligible on `main` while a decision is given to the decide grant; their refusal is WF-2b's check, skipped in WF-1 naming that row); removes one for the withdrawn responder; wrapped as the decide tests' `gatedIdentity` to hold a recheck |
| decision router | `FakeRouter` | is told of every decision (C5) and answers a destination; with WF-2b, a policy |
| context | `FakeContext` | answers blocks with a pack mark; `SetErr` for context unavailable; with WF-2a, lookups and commit notes |
| notifications | `FakeSink` | records the decision's notices |
| sync | `FakeContinuity` | is told of every commit; with WF-2c, holds and hands in an answer |
| entitlements | `FakeEntitlements` | registered so that `Asked()` can be checked empty: no code on `main` asks it |

The runtime and media slots keep their free defaults: the workflow does
not use media, and the attempts must run on this machine, which a fake
runtime does not do.

**The agents** are stub agents (the managed-run tests' `ready-cat`, a
shell line that stays alive), one per member. The walk speaks for each
attempt's agent through its session's own token and the routes an
agent's tools call (`task_context`, `decision_request`, `task_result`,
`brief_submit` for Brakeman's turn), as the existing tests do. No real
agent program runs.

**The person** is the decide routes under the architecture owner's
pass; the operator is the workbench token. Every wait is on a record
(the ledger's rows, the decision's state, the run's events) or a step
held at a seam the code offers (`decideSeams`, the engine's
`beforeRespond`, `gatedIdentity`), never on a clock (`AGENTS.md`, Tests
that do not flake).

**What every step asserts** (`assertTruthful`): the decision's four
facts read from its record (`GET /api/decisions/{did}`: state, responses
with their receipts, application receipt, history) and the run's
events, never from the test's own memory; one accepted response, by an
eligible principal; at most one `decision.applied`; no held task's
attempt before its decision's application; no attempt number started
twice; no refused row counted.

**The tests.** `TestWorkflowEndToEnd` (the fourteen steps, with the
parts WF-2 adds skipped until their seams are on `main`, each skip
naming the row it waits for), and one test per condition:
`TestWorkflowStaleCard`, `TestWorkflowDuplicateTap`,
`TestWorkflowWithdrawnResponder`, `TestWorkflowContextUnavailable`,
`TestWorkflowOwnerDisconnected`, `TestWorkflowAgentRestarted`,
`TestWorkflowSidekickAway`, `TestWorkflowEntitlementEnded`; and
`TestWorkflowFree`, the steps with no provider registered (the operator
answers, no context, no routing), every promise held.

**What it cannot show without a real provider.** A real router's choice
of who holds a role; a real identity provider's revocation and how soon
it reaches the server; a phone on another network reaching the owning
server, and a real loss of that network; a held answer kept off this
machine and handed in after a real outage; context made from a real
repository, and whether it helps; a lapse decided by a real account;
real agents' behaviour at each step. The walk proves the server keeps
its promises whatever a provider answers; each of these is the
provider module's own acceptance, and the owner's observation (WF-3).

**The screens.** The walk is the server's. Each screen it crosses has
its own Playwright specs on `main`, against the shipped build with no
provider (`dispatch-plan.spec.ts`, `decisions-gate.spec.ts`,
`decisions-restart.spec.ts`, `decision-page-durable.spec.ts`,
`decide-only.spec.ts`, `brakeman-brief.spec.ts`, `review-repair.spec.ts`,
`dispatch-delivery.spec.ts`), and context-seams adds
`attempt-context.spec.ts`. No spec is added here: the shipped build
registers no provider, so a whole walk in the browser could only show
the free walk again.

## Out of scope

- What a provider does behind its seam, and its own acceptance.
- A person's or an agent's real behaviour: the agents are stubs.
- Groups, quorum and several approvers: the workflow has one
  architecture owner (decision-policy's later rows).
- A second platform or app for the phone: the person uses the decide
  page in a phone's browser.
- How an entitlement comes to run out: the lapse is an entitlement
  source's answer, however it was made.

## Tests per surface

`go test -race -count=1 ./internal/api/ -run 'TestWorkflow'`, and the
repository's checks for a test-only change (`make lint`, `make
lint-static`); `internal/api` whole is CI's. The walk is proved able to
fail: for each promise, the change that breaks it (an application run
twice, a hold released before the application, the recheck skipped, a
refused row counted) fails it, each named in the row's pull request.
Each test runs with `-count=10` before it is merged, and once more on a
loaded machine (`AGENTS.md`).

## The PRs

| Row | Title | Contains | Touches | Depends on | Size |
|---|---|---|---|---|---|
| WF-0 (#1034) | plans: the first end-to-end workflow | this file | `docs/plans` | none | docs ~500 |
| WF-1 | api: the workflow's walk on `main`'s seams | `workflow_test.go`: the fixture with every slot a fake, `assertTruthful`, `TestWorkflowEndToEnd` with its waiting parts skipped, the eight condition tests as `main` can show them, `TestWorkflowFree` | `internal/api` (tests only) | WF-0 | ~1,300 (tests) |
| WF-2 | api: the walk widened as its seams land | three parts, each its own commits: (a) context recorded, the lookup and its absence (after context-seams CT-3); (b) eligibility by a router's policy: the card "for ana, the architecture owner", only ana admitted, the unrelated decide principal refused, and the router away (after decision-policy's PR-7a and PR-7); (c) the held answer for the owner disconnected (after sync-seams OS-3); each removes its skips | `internal/api` (tests only) | WF-1 and the named rows | ~250 each |
| WF-3 | docs: the workflow in the features record | `docs/features.md`'s entry: the walk, what it holds, and the owner's by-hand observation with real providers and agents, in words that name no provider | `docs` | WF-1; the observation | docs ~80 |

A part of WF-2 that finds a defect in the code of the row it waits for
fixes it there, before that stream is called done (`AGENTS.md`).

## Owner decisions

Dated 2026-11-19. Each is a default the rows build.

1. **Where is the walk proved?** Default: in Go, against the real
   server with fakes and stub agents, in the race suite. The other
   answer, in the browser as well: a server built for tests with fakes
   registered, a row of its own, which this repository does not have.
2. **What does "the owner disconnected" mean here?** Default: the
   owning server out of the person's reach; a person away from their
   phone is silence, which approves nothing. The other answer, the
   person's device offline: the walk is the same, without the restart.
3. **One walk or one per step?** Default: one walk for the steps and
   one test per condition, sharing the fixture and `assertTruthful`.
4. **The record.** Default: one entry in `docs/features.md` once the
   owner has observed the workflow with real providers and agents, in
   words that name none of them.

## Acceptance

- WF-1: `TestWorkflowEndToEnd`, the eight condition tests and
  `TestWorkflowFree` green, each promise's breaking change failing it.
- WF-2: each part's skips removed and its tests green.
- WF-3: the entry merged with the owner's observation.

Done is every row merged with those checks green.
