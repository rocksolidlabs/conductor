# Needs your decision: the decision page on a phone (Program item MB0)

Conductor already knows when a person is needed: a session's agent asks
a question (Needs you), a managed task fails its checks until its
attempts are spent, a Dispatch ticket has a plan revision waiting for the
operator's approval, and Brakeman may have written a decision card that
explains one of them. Each of these lives on its own page (the session's
reply bar, the run page's task actions, the ticket page's Plan section,
the Brakeman page), and none of them says, after a person acts, whether
what they did was recorded, whether the part of Conductor that runs the
work took it, and whether the work went on. This plan adds one page,
**Needs your decision** (`/decisions`), built for a phone first: every
item a person must decide, each with a short brief, the exact question,
the choices, Brakeman's recommendation and its uncertainty when there is
one, what is blocked, exactly what each choice authorizes, the evidence
folded away, and the actions; after an action, the item's receipt in four
separate steps: the request was there, the response was recorded, the
execution owner took it and checked it, and the work resumed (or why it
did not).

It is Program item MB0 (the Program, section 4.3), free in the
open-source build (decision D9), written against `main` at 9723fdc6
(PR #601, 2026-11-05) after reading the attention and answer code
(`internal/session`: `attention.go`, `answeras.go`, `submit.go`,
`local.go`'s claims and kept results), the decision router seam
(`ext/decisions.go`, `internal/api/router.go`), the engine's task
commands and receipts (`internal/crew/commands.go`), Dispatch's plan
revisions (`internal/dispatch/plans.go`, `model.go`), Brakeman's store,
refs and decision cards (`internal/brakeman`, `web/app/utils/brakemanCards.ts`,
`components/BrakemanDecision.vue`), the auth chain and links
(`internal/api/auth.go`, `links.go`, `publish.go`, `internal/share`), the
host's viewer dispatch (`internal/hostagent/peer.go`), the event hub
(`internal/api/events.go`), the phone's layout (`components/BottomBar.vue`,
`layouts/default.vue`), the quick reply (`composables/useQuickReply.ts`)
and the e2e specs it extends (`phone.spec.ts`, `switchyard.spec.ts`,
`brakeman-cards.spec.ts`). It is reviewed before any of its code; the PRs
at the end are cut into issues after the review, one issue, one branch
`issue-N`, one PR each (AGENTS.md).

**The Program's references used here**, each in a line, so the plan
reads without the Program: *D4*, the free build has one approver, the
operator, and delegation, quorum and roles are the Team tier's; *D9*,
local decisions and this page are free, push and the managed inbox paid;
*D19*, mobile push comes with the Capacitor app (MB1); *D26*, a
self-hosted switchyard is in the free tier; *C5*, the decision router
seam (`ext.DecisionRouter`, `docs/plans/ext.md`); *DR1* and *HM1*,
durable decision records and human members as labels
(`docs/plans/decisions.md`); *BK1*, Brakeman's briefs and decision cards
(`docs/plans/brakeman.md`); *MB1*, the paid mobile app with push; *SW1*,
Switchman's team routing; *ID1*, accounts and identity; *CS1*, sync and
continuity across devices; *O4* and *O10*, the Program's open owner
questions (Owner decisions, 1); the Program's section 1 is its product
directions and work model, section 4.3 the items' requirements, section
5 the tiers (Community, the free one), section 6 the release gates.

The names follow the railroad (the Program, section 1.2): Conductor is
the application, Yardmaster the agent harness that runs a crew's tasks and
owns their execution, Dispatch the ticketing, whose items are tickets,
Brakeman the sidekick agent, Switchman the routing still to come. "The
decision page" and "Needs your decision" are this item's page; "Needs
you" stays the name of the sessions' attention it reads; "the inbox" in
this plan is the page's read model on the server, never a member's crew
message inbox (`GET /api/sessions/{id}/inbox`, row 9 of the harness),
which keeps its meaning. No project manifest and no agent recipe is
touched.

**What `main` has**, which the first PRs build on:

- **Questions** (Needs you): a session's attention with an id, its words,
  its kind and up to six choices (`session.Attention`); one way to answer
  it, the `answer` control message naming the question, through the
  question's claim, first response wins, no timer ends a question
  (`docs/protocol.md`, "Answering a question"); the session keeps the
  result of its last 64 questions by id (`resultsKept`, `local.go`). The
  page sends answers the way the Yard's queue does (`useQuickReply`).
- **The decision router seam** (C5, `docs/plans/ext.md`): a registered
  `ext.DecisionRouter` is told each question and may say whom it went to;
  a route with destinations records one `routed: …` status entry in the
  session's activity. The free build registers none and records nothing.
- **Task commands with receipts** (Yardmaster, `internal/crew/commands.go`):
  retry, accept anyway and cancel, each with a client-minted command id
  and an expected revision, the receipt written in the change's
  transaction, read again at `GET /api/runs/{run}/commands/{command}`.
- **Plan revisions** (DS2, `docs/plans/dispatch-evidence.md`): proposed,
  approved by the operator against the ticket's revision, withdrawn, each
  write a Dispatch operation with a kept receipt (`op:` refs); the
  decisions a revision names are recorded and shown, not held
  (`DispatchDecisions.vue` says so), and Dispatch's link kind `decision`
  is reserved for DR1 (`internal/dispatch/links.go`).
- **Brakeman's decision cards** (BK1, `docs/plans/brakeman.md`): a
  `decision/1` item of a brief, about a `question:`, `task:` or `ticket:`
  ref, with what happened, why a person is needed, the question, two to
  six alternatives with their tradeoffs, a recommendation or none, the
  uncertainty, what is blocked, what answering authorizes and its
  evidence; Conductor renders the controls from the live record, never
  from the card.

**What `main` does not have**, which the later PRs wait for:

- **DR1**, durable decision records (Program item DR1, plan
  `docs/plans/decisions.md`, being written beside this one): the record,
  its responses and receipts, the Yardmaster decision adapter that holds
  only dependent work and applies a response once, gates a plan requires
  even when no agent asks. MB0's prerequisite in the Program; its PRs are
  not cut yet. This plan's PR-1, PR-2, PR-5, PR-6 and PR-7 need none of
  it; PR-3 and PR-4 wait for DR1's PRs that serve the record, and the
  lane of PR-5 and PR-6 carries DR1's records once PR-3 has put them in
  the inbox. DR1's plan is in review beside this one (PR #608); where the
  two meet, this plan follows DR1's names, routes and components (DR1's
  records in the inbox, below), and what is still to settle between them
  is said there.
- **HM1**, human members as local labels (same plan as DR1): once it
  ships, a decision can name a label ("architecture owner") the operator
  answers for. The page shows the label where the record carries one;
  nothing here waits for it.
- **Brakeman's PR-10 to PR-18** (in flight): the `plan:` ref (PR-10), the
  `decision:` ref (PR-12), and `route_decision` (PR-12, PR-18), Brakeman
  routing a decision record. On `main` the ref grammar rejects the `plan`
  kind (`internal/brakeman/refs.go`) and `web/app/utils/brakeman.ts` has
  no parser or `refRoute` case for it. The page shows a card about any of
  these once the card's ref is one the server resolves, and the routing a
  record carries; it owns neither. Until Brakeman's PR-10 merges, a plan
  item's name is the inbox's own string (below) and nothing of the item
  is handed to Brakeman's resolver: its Ask Brakeman is about the ticket
  (`ticket:<KEY>`, a kind `main` resolves) and its evidence links to the
  ticket page; with PR-10 merged, both use the `plan:` ref. The server
  decides which, in the item's `subject` (below): `plan:` once
  `brakeman.ParseRef` takes the kind, `ticket:` before, so the page has no
  version check of its own.

## Articles it serves

The twelve articles of the Program, section 2.1:

1. **No premium code, no licence check.** Nothing here asks a plan or an
   entitlement; the one conditional control, delegation, is drawn only
   when the server lists it as an action of the item (Owner decisions,
   6), which no code of this repository does.
2. **The open-source build is useful alone.** The page is free (D9): local
   decisions and the decision page are the Community tier's (the Program,
   section 5), and a phone on the LAN or through a self-hosted switchyard
   answers with no cloud account.
3. **Paid features gated by hosted dependencies.** Push delivery, the
   managed inbox that survives the owner going offline and escalation are
   MB1's (D9, D19), behind cloud; this item adds no hook for them beyond
   the existing `NotificationSink`.
4. **Required approvals stay free and never degrade.** The page is one
   more place to give an approval, never a way around one: every action
   goes through the operation that already checks it (the question's
   claim and the control role, the engine's command with its revision,
   Dispatch's approval against the ticket's revision, DR1's response),
   silence is never approval, and no item times out into a default.
5. **Dependencies point one way.** No import of a private module; the
   lint (`internal/opencore`) is unchanged and still passes.
6. **No feature build tag.** None added.
7. **The free default stays.** The empty decision route is unchanged: the
   page shows "routed to" only where a registered router routed, and the
   first answer still wins wherever it comes from.
8. **An unreachable provider answers as the free default.** The inbox
   calls no provider; what a router said is the route the server kept,
   by question id, when the router answered, never asked of the router on
   the read's path.
9. **CGO_ENABLED=0, cross-compiled.** No new dependency; the QR code uses
   the app's own encoder (`web/app/utils/qr.ts`).
10. **The repository contract.** This plan before the code; one issue per
    PR; the race tests, vitest and Playwright per surface; the protocol
    changes of PR-5 and PR-6 in `internal/proto`,
    `web/app/utils/protocol.ts` and `docs/protocol.md` together, each new
    frame and message with a size limit and a test.
11. **The names keep their meaning.** "Needs your decision", Yardmaster,
    Dispatch's tickets, Brakeman; "workspace" is not used.
12. **Execution behind one seam.** The page starts nothing: a retry is the
    engine's, which starts its attempt through `crew.Launcher`; the
    Boxcar constraints below are each honoured or untouched.

## Why and what happens

The Program's product directions put mobile participation at the centre
(the Program, section 1.3, direction 7): a person away from the desk
understands a well-framed decision and resolves it. Today a phone can
answer a question in the sessions list's row (`phone.spec.ts`), and can
retry a task on the run page, but it sees no brief, no statement of what
the answer authorizes, and no word after it beyond a toast. The Program's
work model keeps request, response and execution apart (the Program,
section 1.4): an answer typed is not an answer taken, and an answer taken
is not work resumed. The page makes those three moments visible.

After this item ships, a person sees:

- **On a phone**, a **Decide** tab first in the bar at the foot, with the
  number of items waiting (the Yard's count stays on the Yard). It opens
  `/decisions`: the items that need a person now, oldest first, then
  **Recent**, the items acted on in the last day with their receipts. The
  app opened on a phone (below `lg`, the first load of `/` in a tab) with
  an item waiting lands on `/decisions`, so the decision view is the one
  a phone opens to; with none waiting it shows the sessions as today, and
  the Sessions tab always goes to `/` (Owner decisions, 10). A browser
  notification for a question, tapped on a phone, opens that item there;
  at `lg` and up it opens the session as today.
- **Each item** as a card, folded to its title, its brief and the
  question; opened, it shows:
  - **the brief**: Brakeman's "what happened" when a card of a brief is
    about this item (named "Brakeman's brief", with the brief's link),
    otherwise Conductor's own line from the record ("Claude Code in
    api-fix asks; it is attempt build.2 of run release-7a3f… for
    CND-12");
  - **the question**, whole (500 bytes at most, as the session or the
    record keeps it);
  - **the choices**, from the live record: a question's options, a task's
    commands its state allows, a plan revision's Approve and Withdraw,
    later a decision record's options; each with **what it authorizes**,
    in words Conductor computes from the record ("Types `2` into the
    session's terminal: Claude Code's choice 'Yes, and don't ask again
    for git commands in this project'"; "Starts attempt 4 of build at
    commit 1a2b3c4 with attempt 3's findings; run revision 12"; for a
    plan revision, its yolo choice, read from the revision's body before
    Approve is offered, and the approval it supersedes);
  - **the recommendation and its uncertainty**, from Brakeman's card
    alone, or "No recommendation" (Conductor recommends nothing of its
    own);
  - **what is blocked**: the session's turn, the task and every task that
    needs it, the run's end, the ticket's launch;
  - **who answers**: "you, the operator" (decision D4), with a human
    member's label once HM1 serves one, and "routed to …" where a
    registered router said so;
  - **the evidence**, folded: the session, the run at the task, the
    attempt, its failed checks with their exit and the last lines of
    their logs, the commit, the ticket, the plan revision; each a link,
    so the terminal and every deeper page stay one tap away;
  - **the actions**: the choices; **Ask Brakeman** (a request about this
    item, when Brakeman is configured), and once DR1 serves its records,
    **Ask for more information** and **Comment**; **Approve** and
    **Reject** where the record is an approval.
- **After an action**, the card's **receipt**, four rows that never merge:
  1. *Request available*: when it was asked, by whom, and that it is still
     the one shown or held;
  2. *Response recorded*: who answered, when, and where it is kept (the
     command's receipt, Dispatch's operation, DR1's response; for a
     question before DR1, "kept by the session while it is listed": the
     honest word for an answer only the session's memory holds);
  3. *Received and validated by the execution owner*: the session's claim
     wrote the keys; the engine took the command at the revision it
     carried, or refused it and why (`stale_revision`: the run moved on);
     Dispatch held the approval against the ticket as it is; DR1's
     adapter rechecked the subject's revision and the decision's state;
  4. *Work resumed*: the agent working again, the next attempt started,
     the dependents dispatched; or a clear reason it did not (the question
     withdrawn by the screen, another answer first, the session ended, the
     run paused or stopped, a launch still to make).
  Each row is *done*, *waiting*, *refused* or *does not apply*, with its
  time and its receipt's link.
- **Away from the LAN**, through a self-hosted switchyard: on the desktop,
  the share dialog of a session or a run gains **Answer decisions from
  this link** (control links only; off, with the words why, at a
  switchyard too old to carry decisions). The link opened on the phone
  (its QR code) shows the same cards for what the link covers: the
  session's question and its attempt's task, or every item of the run,
  and their receipts, carried end to end to the server that owns them; a
  run's link works with no member alive (a gate before the first
  attempt, the last attempt failed). The switchyard stores nothing of
  them.

A responsive page is all this is: no installable app, no web push, no
service worker (Owner decisions, 14); the Capacitor app with push is MB1
(D19).

## The changes

### `internal/api`: the inbox read model (PR-1)

A new file `inbox.go` builds the page's items from what the server holds,
on each read, with no state of its own (before DR1) beyond the routes a
router gave (below), and no wait on any provider.

**Item identity.** An item is named by the ref of what it is about, in
Brakeman's ref grammar (`docs/plans/brakeman.md`, Evidence references),
so a card and an item about the same thing share a name:

| Kind | Ref | Source | Open while |
|---|---|---|---|
| `question` | `question:<session>/<question>` | the session's `Info().Attention` | the session is listed, its attention is `needs_input` with that id, and it is not `answered:"terminal"` |
| `task` | `task:<run>/<task>` | the engine's `RunInfo`, live managed runs | the task is failed and `crew.TaskCommands` (below) allows at least one command |
| `plan` | `plan:<KEY-n>/<revision>` (Brakeman PR-10's form; the inbox's own string until PR-10 merges) | Dispatch's open revisions (`OpenPlans`, below) | the revision is proposed, neither approved nor withdrawn; or it is approved and its approval no longer holds (`Stale` set); and its ticket is not deleted |
| `decision` | `decision:<id>` (Brakeman PR-12's form) | DR1's records (PR-3) | DR1's record is open |

One thing needing a person is one item (Owner decisions, 13). A task
blocked only because an input failed is not its own item: it is named in
the failed input's blocking. A task whose attempt reported a blocker is
not its own item either: the engine asks the blocker as that session's
own question (`Engine.Blocked`, `checkpoints.go`), so the question is the
item; it names the task, the blocker's words and the dependents, and
carries the task's commands its state allows (Cancel) after the
question's choices, labelled with the task. A blocked task whose question
is gone (its session ended) is an item once it fails, as any failed task.

**Bounds.** Every text bound below is a bound on the text's JSON-encoded
bytes, escaping included: a text is cut on a rune boundary, with "…",
until its encoded form fits, so `<`, `&` and a control character's
`\u00XX` cannot grow a field past its bound. Each object's encoded size
is checked as built, and a test fills every field at its bound with
characters that escape to six bytes.

**Types.** The summary, which the list carries, and the item, which the
detail and the lane carry, are two types, so the list never carries an
action's scope:

```go
// inboxSummary is one item as the list shows it, folded: at most 4 KiB encoded.
type inboxSummary struct {
	Ref      string    `json:"ref"`      // at most 512 bytes (a Brakeman ref)
	Kind     string    `json:"kind"`     // question | task | plan | decision
	Since    time.Time `json:"since"`
	Title    string    `json:"title"`    // at most 200 bytes
	Brief    string    `json:"brief"`    // at most 280 bytes: the item's brief, cut
	Question string    `json:"question"` // at most 280 bytes: the question, cut
	Answerer string    `json:"answerer"` // at most 120 bytes: "you, the operator"; a label with HM1
	Routed   int       `json:"routed,omitempty"`   // how many destinations a router named
	Blocks   int       `json:"blocks,omitempty"`   // how many tasks, runs or tickets wait
	Actions  []string  `json:"actions"`  // at most 10 labels, each at most 80 bytes
	Card     bool      `json:"card,omitempty"`     // Brakeman wrote a card about it
}

// inboxItem is one thing a person must decide, whole: at most 96 KiB encoded.
type inboxItem struct {
	Ref        string             `json:"ref"`        // 512
	Subject    string             `json:"subject"`    // 512: the ref Ask Brakeman and the evidence use (ticket: for a plan before Brakeman PR-10)
	Kind       string             `json:"kind"`
	Since      time.Time          `json:"since"`
	Title      string             `json:"title"`      // 200
	Brief      string             `json:"brief"`      // 2 KiB: Brakeman's happened whole (its own bound), or Conductor's line
	BriefBy    string             `json:"briefBy"`    // conductor | brakeman
	Card       string             `json:"card,omitempty"`      // 160: brief:<br-n>/<item>, when Brakeman's
	Question   string             `json:"question"`   // 500
	Answerer   string             `json:"answerer"`   // 120
	RoutedTo   []inboxDestination `json:"routedTo,omitempty"`  // at most 8
	RoutedRule string             `json:"routedRule,omitempty"` // 200 (ext.MaxRouteRule)
	Revision   string             `json:"revision"`   // 64: what every action carries as its expected revision (below)
	TicketRev  int64              `json:"ticketRev,omitempty"` // a plan item's ticket revision, which Approve carries beside Revision
	Now        string             `json:"now,omitempty"`       // 500: the current validity of what an earlier action did ("the approval no longer holds: …")
	Blocking   inboxBlocking      `json:"blocking"`
	Actions    []inboxAction      `json:"actions"`    // at most 10
	WhyHuman   string             `json:"whyHuman,omitempty"`   // the card's, 1 KiB
	Considered []cardAlternative  `json:"considered,omitempty"` // the card's, at most 6: label (320) and tradeoffs (1 KiB), text only
	Recommended int               `json:"recommended"`          // index into Considered, -1 for none
	Uncertainty string            `json:"uncertainty,omitempty"` // the card's, 1 KiB
	Authorizes string             `json:"authorizes,omitempty"`  // the card's "what answering authorizes", 1 KiB, shown as Brakeman's beside each Scope
	CardRev    string             `json:"cardRev,omitempty"`     // 64: the subject revision the card was written at
	Evidence   []inboxRef         `json:"evidence,omitempty"`    // at most 20
	Delivery   []deliveryStep     `json:"delivery,omitempty"`    // four, for the latest action this server knows of
}

type inboxAction struct {
	Name    string `json:"name"`    // 32: answer | retry | accept | cancel | approve | withdraw | ask_brakeman | comment_ticket; DR1's from PR-3
	Label   string `json:"label"`   // 80: the choice's label, or the command's
	Input   string `json:"input,omitempty"` // a question's choice: its keys (at most proto.MaxAnswerInput, 48)
	Text    bool   `json:"text,omitempty"`  // a line is typed (a free-text question)
	Scope   string `json:"scope"`   // 1 KiB: exactly what it does, to what, at which revision
	Confirm bool   `json:"confirm,omitempty"` // asked first (Accept anyway, Cancel, Withdraw), as their pages ask
	Route   string `json:"route"`   // 200: the operation's own route, which the page calls
	do      func(ctx context.Context, actor principal, a decideArgs) (receipt string, err error) // never serialized: the same operation, for the lane (PR-5)
}

type deliveryStep struct {
	Step    string     `json:"step"`    // available | recorded | validated | resumed
	State   string     `json:"state"`   // done | waiting | refused | not_applicable | unknown
	At      *time.Time `json:"at,omitempty"`
	Words   string     `json:"words"`   // 500
	Receipt string     `json:"receipt,omitempty"` // 512: a ref: command:, op:, question:, later DR1's
}
```

`inboxBlocking` is `{words (500), refs (at most 20 task, run, session or
ticket refs, 512 each), more (how many past 20)}`; `inboxRef` is `{ref
(512), label (200), session? (the published id of a session in a link's
scope, set only on the lane)}`, its route computed on the page from the
ref by the page's own route function (`refRoute` on the workbench, the
join page's on a link: PR-7), never sent; `inboxDestination` is `{kind
(8), name (240: ext.MaxDestinationName, 60 characters)}`.

The sum, encoded, at every bound: the item's texts and refs about 6 KiB,
`RoutedTo` 2 KiB, blocking 11 KiB, actions 10 × 1.45 KiB = 14.5 KiB, the
card's fields 3 KiB and `Considered` 6 × (a label cut to 320 bytes and
1 KiB of tradeoffs) = 8.5 KiB, evidence 20 × 0.8 KiB = 16 KiB, delivery
4.5 KiB, keys and quotes about 4 KiB: about 70 KiB, under the item's 96
KiB. The summary at every bound is under 3 KiB, under its
4 KiB. `TestInboxSummaryBounded` and `TestInboxItemBounded` build each at
every field's bound with escaping characters and assert both.

**What each kind carries.**

- *Question*: `Revision` is the question's id, which an answer names
  (`proto.Answer.Question`), so an answer to a question gone or replaced is
  refused by the session, never typed into the next. Actions: each of
  the attention's options as `answer` with its `Input` (a `Mode` choice
  last, as the reply bar orders it), and `answer` with `Text` for a
  question of the `prompt` kind or none; `Scope` names the keys and the
  label ("Types `2` into the terminal of api-fix: the choice 'Yes, and
  don't ask again …'"; a line's scope says it is typed and entered), and for a plan's approval question
  (`Attention.Plan`) the plan's path. `RoutedTo` and `RoutedRule` are the
  route the server kept for that question id (below), never asked of the
  router. `Blocking`: "Claude Code does nothing until it is answered",
  and when the session is an attempt's, its task and the tasks that need
  it; a blocker's question also names its task and carries the task's
  commands (Item identity).
- *Task*: `Revision` is the run's revision as read; every command carries
  it as `expectedRevision`, so a run that moved since is refused by the
  engine (`409 stale_revision`), nothing done. Actions from
  `crew.TaskCommands` (Retry; Accept anyway, asked first; Cancel, asked
  first), each with the route the run page uses
  (`POST /api/runs/{run}/tasks/{task}/retry` and the others). `Scope`
  says which attempt starts at which commit with which findings, which
  result becomes the task's and which tasks start from it, or which
  tasks are blocked by a cancel. `Blocking` lists the task's dependents
  through the crew's needs, transitively, and the run's end. `Evidence`:
  the run, the task, its last attempt, that attempt's failed checks
  (`check:` refs with their exit, where they ran and the log's SHA-256,
  from the ledger's rows), the result commit, the blocker's words, and
  the ticket when the run was launched from one.
- *Plan*: two revisions, because Dispatch checks two
  (`internal/dispatch/plans.go`): `Revision` is the plan revision's own
  `rev`, which every plan operation carries as its expected revision
  (`tx.plan` checks it, and it is all `WithdrawPlan` checks), and
  `TicketRev` the ticket's `rev`, which `ApprovePlan` checks besides
  (`ticketRev`). Approve sends both, Withdraw sends `Revision`
  (`useDispatchOps`' `expectedRev` and the approve route's `ticketRev`,
  `internal/api/dispatch_plans.go`), so a ticket edited since refuses an
  approval, and an approval another operator made since (which bumps the
  revision's `rev`, not the ticket's) refuses a withdrawal.
  Actions by the revision's state: a proposed one, Approve (with an
  optional note, `POST /api/dispatch/plans/{id}/approve`) and Withdraw
  (asked first); an approved one whose approval no longer holds, by its
  `Stale.Remedy`: `approve`, Approve again (the ticket's intent changed:
  title, description or criteria; `approvable` in
  `web/app/utils/dispatchPlans.ts` offers the same), and `revise`, no
  Approve, the stale words and "a new revision is needed" with the
  ticket page in its evidence, and Withdraw. **The yolo choice before
  any Approve**, as `DispatchPlanApprove.vue` shows it: the inbox reads
  the revision's body (the route that component reads) when it builds
  the item's detail, and the Approve's `Scope` says the crew, its tasks
  and agents, the base commit, the ticket's revision approved against,
  the yolo choice in `yoloWords`' words ("yolo on: the agents' permission
  prompts are skipped"), and the approved revision it supersedes ("sets
  revision 2's approval superseded"), and that launching is a separate
  step on the ticket page. A body not read (too large, unreadable) gives
  no Approve, only the words why ("revision 3 could not be read, so its
  yolo choice is not known") and the ticket page's link, as that
  component disables its approval. `Blocking`: "Nothing runs for CND-12
  until a revision is approved and launched." Evidence: the ticket and
  the revision.

**Brakeman's card.** The newest `decision/1` item, among the newest 20
requests with a kept brief (`brakeman.Store.List` and `Brief`, read-only),
whose `about` is the item's subject, gives `Brief` (its `happened`,
whole: the field is 2 KiB as the card's is), `WhyHuman`, `Considered` (its
alternatives, text), `Recommended`, `Uncertainty`, `Authorizes` and
`CardRev`, and its evidence refs appended after Conductor's own up to the
20; its blocked refs join `Blocking`. Its `authorizes` text is shown
beside Conductor's `Scope`, labelled as Brakeman's, never in its place. A
card read at a revision other than the item's (its `subjectRevision`) is
shown with "Brakeman read this at revision 7; it is at 9 now". With
Brakeman unconfigured or its store read-only, items have Conductor's
brief alone.

**`crew.TaskCommands`** (`internal/crew/commands.go`): the commands a
task's state allows, computed by the same precondition functions the
commands call, so the inbox and the engine never disagree: Retry from
the rule `retryable` applies; Accept anyway from the search
`acceptTask` makes (a kept attempt, since any withdrawal, that submitted
a commit), moved into one function both call, not from `acceptable`
alone; Cancel from `cancellable`; each with the run's liveness as the
engine judges it (stopped, a record, continued by another run:
`run_continued`). The run page's `taskActions` (`web/app/utils/tasks.ts`)
also asks `kind === 'work'` and an attempt's result for Accept and
judges liveness from the run's fields; the engine is the authority, so
one fixture, `internal/crew/testdata/task-commands.json` (each row a
task's state, kind, attempts and results and the run's liveness, and the
commands allowed), is read by a Go test against `TaskCommands` and the
engine and by a vitest case against `taskActions`, and a row where the
page offers what the engine refuses (or hides what it takes) is fixed in
the page in PR-2.

**`dispatch.Store.OpenPlans(ctx, limit)`** (`internal/dispatch/plans.go`):
the revisions of every ticket not deleted that need the operator, oldest
first, at most `limit` (100): the proposed ones, and each ticket's
approved revision whose approval no longer holds (`planStale` set, read
as the ticket's `GET` reads it), each as `PlanSummary` with its ticket's
key and `rev`; queries over the existing plan rows by state, no schema
change.

**`session.Local.QuestionResult(id) (Answer, bool)`** (`internal/session`):
the kept result of a question by id (the existing `results`, the last
64), read under the session's lock. The kept `Answer` gains `ReqID`, the
winning answer's request id (`proto.Answer.ReqID`, which the page mints
fresh per action), so a browser's own attempt and the winner are told
apart by id (Events, Recent). The result is in memory: a session gone or
past 64 later questions forgets it, and the step then says `unknown`,
never a guess. **A hosted session's question has no result on the
server**: the host's claim takes the answer and the server keeps no
claim; the activity's `input` entry is not a receipt of it (an
`ActivityEntry` has no question field, `Local.resolve` records only the
question's words, identical prompts repeat, and the host's activity
forwarder drops entries when its queue is full). So for a hosted
question the server reports *recorded* and *validated* as `unknown`
("the host holds this answer; this server keeps no record of it"), the
page shows its own reply's word there (the host answered the quick
reply's request, accepted or refused, `useQuickReply`), and *resumed*
reads the hosted session's attention since.

**Routes kept by question id** (`internal/api/router.go`): when a
registered router answers with destinations, the server keeps that route
under the session and the question id it was asked about (a map of the
last 256 routes, in memory, as the router seam keeps everything), set
where the provider's reply is taken, so a route that comes back after the
question was replaced lands on its own question, never on the newer one.
The `routed: …` status entry stays as it is for the activity log; the
inbox never parses it (its words flatten names that may hold commas and
parentheses, and carry no question id).

**Routes** (admin only, `requireAdmin`; on a switchyard `403 switchyard`,
"decisions are answered where the work runs, or through a decision
link"):

- `GET /api/inbox?after=<cursor>`: `{items: [...summaries], next?, total,
  at}`; the open items as summaries, oldest first by `(since, ref)`, at
  most 150 a page, `next` the cursor of the last one when more follow and
  `total` the count, so nothing is cut silently: at most 600 KiB of
  summaries and the reply at most 640 KiB (`TestInboxReplyBounded` builds
  150 at their bounds; `TestInboxPages` 200 items over two pages).
- `GET /api/inbox/item?ref=<ref>`: one item with its detail, at most 96
  KiB, `404 not_found` for a ref that names nothing the server holds, and
  for a ref of something no longer open, the item as it is now with its
  delivery (so a page that acted reads what followed). The page reads it
  before it acts: the list carries no action's input or scope.
- `GET /api/inbox/track?ref=<ref>&receipt=<id>&attempt=<reqId>`: the four
  delivery steps of one action, from its durable receipt: a command id
  (the ledger's receipt of the admin's actor, then the task's state and
  attempts since), a Dispatch op id (the operation's receipt; its
  validation is the operation's own outcome, and the approval's current
  validity is the item's `Now`, apart from the steps), a question id with
  the browser's attempt (the session's kept result: the attempt's
  `ReqID` the winner's or another's; then its attention since: `working`
  or a new question is resumed; withdrawn, ended, another's answer is the
  reason). At most 8 KiB.

None of the three routes writes. The page acts through each operation's
own route (the `Route` of the action); the inbox adds no write path (the
Program, section 1.5: one typed operation per change, whoever the
client). These routes read what no DR1 record holds (a session's
question, a task's commands, a plan revision, Brakeman's cards), which
is why the plan adds them beside DR1's: for a DR1 record the page reads
DR1's routes (PR-3).

**Events.** None new before DR1: the page reads `GET /api/inbox` again on
the `session`, `run`, `dispatch` and `brakeman` events it already
receives (`/api/events`), at most one read in flight and one queued
behind it, so a burst of events costs two reads.

### `web/app`: the page (PR-2)

The names keep clear of DR1's (`docs/plans/decisions.md`, its `web/app`:
`utils/decisions.ts`, `composables/useDecisions.ts`, `DecisionCard.vue`,
`DecisionRespond.vue`, `decisions*.spec.ts` are DR1's record's): this
page's files are named for the inbox.

- `pages/decisions.vue` (route `/decisions`, `?item=<ref>` opens one): the
  open items, then Recent; one column below `sm`, the list and the open
  item side by side at `lg`; every target at least 44 px tall, as the
  bottom bar's are. "Showing 150 of 212" and More when the list has a
  `next`.
- `components/InboxItem.vue`: the card (title, brief, question, choices
  with their scope, recommendation and uncertainty, blocking, who answers
  and routed to, evidence folded, actions), its actions confirmed where
  the item says so with the same question the run page asks
  (`useTaskCommands.question`); its routes from a `routeOf(ref)` prop,
  `refRoute` here, the join page's on a link (PR-7).
- `components/BrakemanDecisionText.vue`: the text part of a Brakeman
  decision card (what happened, why a person, the alternatives considered
  with Brakeman's recommendation, the uncertainty, what answering
  authorizes), extracted from `BrakemanDecision.vue`, which then uses it;
  Brakeman's controls and its file's behaviour are unchanged
  (`brakeman-cards.spec.ts` passes as is). The Program asks that the
  decision page and Brakeman render the same cards (the Program, section
  4.3, BK1).
- `components/InboxDelivery.vue`: the four steps of an action on a
  question, a task or a plan revision, each with its state, time, words
  and receipt link, and the item's `Now` beside them, never as a step; a
  step's state never goes back once `done` or `refused` in a page's view
  (`reconcileDelivery`, below), whatever order the reads come back in. A
  DR1 record's four states are DR1's card's (PR-4).
- `components/InboxEvidence.vue`: the folded evidence; a check's log
  tail (its last 4 KiB, from the step log route the run page reads) loads
  only when unfolded.
- `composables/useInbox.ts`: reads the routes through their client calls
  in `composables/useSessions.ts` (where every route's call is, as the
  repository map says), re-reads on the events above (one in flight, one
  queued), and keeps this browser's recent actions (below).
- `utils/inbox.ts` (pure): the order (oldest first), the words of each
  step, `reconcileDelivery` (a `done` or `refused` seen stays; a later
  `unknown` or `waiting` never replaces it; the reply's own word for a
  hosted question fills recorded and validated), the recent list's
  storage (`conductor.inbox.recent`: at most 50 entries of `{ref,
  receipt, attempt?, outcome?, at}`, `attempt` the request id an answer
  was sent with and `outcome` the owner's word on it when it came, newest
  kept, none older than 24 hours, every read and write in `try`), the
  count for the bar, `phoneLanding(width, open)` and `phoneOffer(origin)`
  (below).
- **Acting.** The item's detail is read first (the list carries no
  input or scope). A question's choice or line through `useQuickReply`
  with the question's id and a fresh request id (first response wins; a
  refusal's words from `replyFailure`); a task's command through
  `useTaskCommands` with the item's `Revision` as `expectedRevision` and a
  fresh command id; a plan revision's Approve through `useDispatchOps`
  with a fresh op id, the item's `Revision` as the op's expected revision
  and its `TicketRev` as `ticketRev`, and Withdraw with `Revision` alone;
  Ask Brakeman through `BrakemanAsk.vue` with the item's `subject`;
  **Comment on the ticket** for an item whose run was launched from a
  ticket (Dispatch's comment operation). Each action's receipt id (and a
  question's request id) goes into the recent list before the reply
  comes back, so a reload while it is out still tracks it, and the
  reply's outcome is written into the entry when it comes.
- **Entry points.** `BottomBar.vue`: Decide first (`/decisions`, the open
  count), then Sessions, Yard, Crews, More; Events moves under More
  (Owner decisions, 10). The first load of `/` in a tab below `lg` goes
  to `/decisions` when an item is open (`phoneLanding`, once per tab, so
  the Sessions tab and a later `/` stay on the sessions). The sidebar's
  header gains a Decide row with the count beside Needs you
  (`SidebarHeaderMenus.vue`). The palette gains "Go to Needs your
  decision" and one row per open item ("Decide: Claude Code in api-fix
  asks…", `usePaletteRows.ts`). The chord `G` `N` goes there (no Alt
  chord in the terminal: Alt+N launches an agent), with its line in the
  `?` list. A question's browser notification, tapped below `lg`, opens
  `/decisions?item=…` (`useAttention.ts`, its `onclick`, through a pure
  `notificationTarget(width, ref)`).
- **Open on your phone.** A button on the page at `lg` and up shows a QR
  code of the page's address as the browser has it (`location.origin` +
  `/decisions`), the URL alone, never the token (Owner decisions, 7). On
  a loopback origin (the desktop app listens on 127.0.0.1) it says that a
  phone cannot reach this address and offers the decision link instead
  (PR-7); `phoneOffer(origin)` says which.

### DR1's records in the inbox (PR-3, after DR1)

Ownership of the shared piece, as DR1's plan in review sets it
(`docs/plans/decisions.md`, PR #608, "The items this one meets") and
this plan takes it (Owner decisions, 3): DR1 owns the decision record,
its store, its routes, every response and comment and their delivery to
the work, its receipts, its event, the four states the record reports,
and the in-context controls through its two components,
`DecisionCard.vue` and `DecisionRespond.vue`; HM1 (the same plan) owns
the human labels; this plan owns the inbox read model, the page, its
phone layout, how the brief and the evidence are laid out around DR1's
card, the four-step view of the items DR1 does not hold (questions,
tasks, plan revisions), the phone's two paths and decision links. MB0
renders DR1's components for DR1's records and calls DR1's routes; it
adds no route for a record. The routes it does add (`/api/inbox`, its
item and track) read what no record holds (PR-1, Routes). Where the two
plans' names differ after review, PR-3 follows DR1's.

What the inbox reads of DR1's record is DR1's own record as its plan
gives it: `GET /api/decisions` (the open and answered ones, newest first,
`?state=&run=&after=`, at most 100 a page) for the list, and
`GET /api/decisions/{did}` for one: its id and type, its state (`open`,
`answered`, `applied`, `superseded`, `cancelled`, `expired`), its `rev`,
its run, task, attempt, session and question, its subject, its options,
recommendation, uncertainty, what it authorizes, its evidence, its
policy (`local/1`: the operator answers), its routing history, its
responses with their receipts, its application receipt (`applied`) and
its history.

PR-3:

- **`decision` items** from DR1's open records, and Recent from DR1's
  records closed in the last 24 hours (at most 50), kept on the server,
  not in the browser. A question DR1 holds a record for is that record's
  item: the derived `question` item for the same session and question id
  is dropped, so the two never both show
  (`TestADecisionRecordReplacesItsQuestion`).
- **Actions** from the record's type and state, each through DR1's
  route for it: `answer`, `approve` and `reject` through
  `POST /api/decisions/{did}/respond` (`responseId`, `choice` or `text`,
  `rationale`, `expectedRev`); `more_info` and `comment` through
  `POST /api/decisions/{did}/comment` (`opId`, `kind`, `text`,
  `expectedRev`), which DR1 has (more information is typed to the asking
  attempt's session and keeps the decision open; a comment changes
  nothing). A repeat with the same id returns DR1's receipt. Each
  action's `do` (for the lane) is the same engine operation the route
  calls. `Scope` from the record (what the response authorizes, on which
  subject, for which work).
- **Delivery** is DR1's four states (`decisionWords`, `utils/decisions.ts`
  in DR1's plan), read from the record, never recomputed: the inbox
  carries the record and the page draws DR1's card.
- **Brakeman's cards about `decision:<id>`** (Brakeman PR-12) attach to
  the record's item as cards about any other ref do.
- **The event**: the page re-reads on DR1's `event: decision`
  (`{id, run, task?, state, rev}`, DR1's plan, its protocol section).
- **On a decision link.** DR1's `local/1` takes one response from a
  principal with the admin grant, and DR1's routes refuse a share link's
  holder. A decision link (PR-5) answers a DR1 record only once DR1's
  reviewed plan accepts a decision link's actor, `link:<id>`, minted by
  the operator, as the operator's under `local/1`; that is to settle
  between the two plans in review. Until it is settled, a DR1 record on
  a link shows its card and its four states with no actions and "Answer
  it on the LAN or the desktop: a link cannot answer a durable decision
  yet" (`TestALinkShowsADecisionRecordWithoutActions`); once DR1 accepts
  it, `TestALinkAnswersADecisionRecord` replaces that test in this PR.

### `web/app`: DR1's records on the page (PR-4)

- `InboxItem.vue` renders a `decision` item as the brief and evidence of
  the inbox around DR1's `DecisionCard.vue` (the record, its
  alternatives, its history and its four states) and
  `DecisionRespond.vue` (the controls by type: an option, a line, Approve
  and Reject asked first, Comment and Ask for more, a rationale), as
  DR1's plan builds them; nothing of the record is drawn twice.
- Recent for DR1 records from the server's Recent; the browser's recent
  list keeps only actions on items DR1 does not hold (questions, tasks,
  plan revisions).
- **The ticket page.** DR1's plan replaces `DispatchDecisions.vue`'s
  "recorded and shown, not held" note with the ticket's records; PR-4
  adds to each a link to its item on the decision page.

### Decision links and the link's lane (PR-5, PR-6, PR-7)

The second path: a phone that cannot reach the server, through a
switchyard the person runs (D26, the self-hosted tier). A switchyard
carries signalling and the relayed terminals, mints and checks links,
serves the join page from its own embedded web build
(`switchyard_page.go`, `/join/`), and tells the publisher each viewer's
role and link id (`proto.ViewerJoin`); it keeps no decisions and runs no
crews (`docs/architecture.md`, Switchyard). So the decisions travel to
the server that owns them, end to end, the way a terminal does: over
WebRTC, through the switchyard's relay only when ICE fails (where the
relay carries them as it carries terminal bytes). The switchyard stores
nothing of a decision. It changes in four places: its relay carries the
lane's frame (`proto.DecodeRelay`'s list of inner types: output, input,
control, scrollback, file, file write, and now `TypeDecision`); its
`RelayToHost` drops the lane's writes and reads from a view-role viewer
(`signal/hosted.go`, the list beside `submit` and `answer`); it keeps a
run's desk (below); and its join page has the Decide tab, which it
serves because the switchyard's embedded build is the same web app.

**Version skew.** The phone gets the switchyard's join page, not the
publisher's, so what decides is the switchyard's build. A switchyard
that carries decisions says so in its `registered` reply
(`decisions: true`; an older one sends no field) and accepts
`register_desk` (an older one closes that connection with `register
expected`, its first-message check in `ws_host.go`, since the message's
`t` is not `register`). A publisher mints a decision link only at a
switchyard that said so: at an older one the share dialog's switch is
off with "This switchyard must be updated to answer decisions through
its links", `POST …/links` with `decide: true` is refused `409
switchyard_too_old`, and nothing is sent to it that it does not know (no
`TypeDecision` in a relay envelope, which both sides decode strictly and
whose unknown inner type closes the host's connection, "bad relay
envelope"). A newer switchyard in front of an older publisher serves a
join page whose Decide tab waits for the welcome's `decisions: true`,
which an older owner never sends, so the page shows the session as
today.

**A decision link** is a control link of a session or a run that the
operator mints with `decide: true` (`POST /api/sessions/{id}/links`,
`POST /api/runs/{run}/links`; refused `400 bad_request` with a view
role). Its scope is the link's: a session link covers that session's
question and the task its attempt runs; a run link covers every item of
the run (its members' questions, its tasks, DR1's records of the run).
A plan revision's approval before any run is in no link's scope; it is
answered on the LAN or the desktop (Owner decisions, 8). The flag lives
where the link's other facts live on the publisher: `share.Link.Decide`
for a link the server minted itself (in memory, as its links are), and
`remoteLink.decide` for one minted at the switchyard (`publish.go`, held
with the publication or the desk, gone with it as the link is). Nothing
about it reaches the switchyard. A link holder's actions are recorded as
the link's actor, `link:<id>` with its label as the name (the engine
command's `actor`, a Dispatch comment's), never as the workbench (Owner
decisions, 9). The owner keeps with the link, in memory as its other
facts, its **Recent**: the last 50 `{ref, receipt, opId, at}` acted on
through it, so an item that closed after its action (a retried task is
no longer failed) stays readable on the link with its four steps for as
long as the link stands. A session decision link lives as its session's
publication does, as every session link does; a run decision link is
carried by the run's desk and outlives every member.

**The run's desk.** A run's decisions must be reachable with no member
alive: a gate before the first attempt (DR1), every attempt failed, the
last member ended. A member's publication cannot carry them: it ends with
its session (`uplink.go`, the session's end closes its peers and then the
publication), and a run link is minted today over a member's publication
(`links.go`, `awaitRunPublished`; with none published, the link is made
locally and says why). So the publisher keeps, for each run with a live
decision link at the switchyard, one more host connection of its uplink,
the run's **desk** (`internal/hostagent/desk.go`): registered with
`register_desk`, it has no PTY, no output, no input and no terminal
frames, only the lane. The switchyard keeps it as the run group's desk
(`proto.RunGroup.Desk`, the desk's id, sent in the run's sync as the
members are), never lists it as a session, counts it against its
session limit, and joins a run link's viewer to it through the same
signalling as a member when the join page opens its Decide tab. The desk
is made with the run's first decision link and ends at the first of: the
run's end, after `endLinger` as a publication lingers; the last decision
link of the run revoked or expired; the server's stop. A run decision
link is minted over the desk, so it needs no published member; the
members' terminals are joined as today, when they are published. On the
server itself (a decision link opened on the LAN, `share.Link.Decide`),
the desk is a route of the API's, `GET /ws/runs/{run}/desk` with the
link's token, which serves the same lane whether or not a member lives.

**The lane** is request and reply, so a phone reads what it needs and
nothing is cut silently (`internal/proto`, `web/app/utils/protocol.ts`,
`docs/protocol.md` together):

| Message | Direction | Content | Bound | Test |
|---|---|---|---|---|
| control `decisions_list` | viewer → owner | `{t, reqId, after?}`: a page of the scope's items | `MaxControl` (8 KiB); `reqId` at most 32 bytes, `after` 600 | `TestDecisionsListValid` |
| control `decision_read` | viewer → owner | `{t, reqId, ref}`: one item's detail | `MaxControl`; `ref` 512 | `TestDecisionReadValid` |
| control `decision_track` | viewer → owner | `{t, reqId, ref, receipt}`: the four steps of one action taken through this link | `MaxControl`; `receipt` 512 | `TestDecisionTrackValid` |
| control `decision_evidence` | viewer → owner | `{t, reqId, ref, n}`: a bounded read of the item's `n`th evidence entry | `MaxControl`; `n` below 20 | `TestDecisionEvidenceValid` |
| control `decide` | viewer → owner | `{t, reqId, ref, action, revision, ticketRev?, input?, text?, opId}`: one action on an item | `MaxControl`; `reqId` and `opId` at most 32 bytes, `ref` 512, `action` one of the item's action names, `revision` 64, `input` at most `MaxAnswerInput`, `text` at most `MaxSubmit` (4 KiB) | `TestDecideValid` |
| frame `TypeDecision` (0x09) | owner → viewer | JSON `{reqId, list?, item?, track?, evidence?}`, one of: a page of at most 25 summaries in `(since, ref)` order with `next` and `total`; one item with its detail; four steps; an evidence read (a check log's last 4 KiB, or a member's published session id) | 128 KiB (`MaxDecision`); fragmented by `TypeChunk` on a data channel as a file frame is | `TestDecisionFrameBounded`, `protocol.test.ts` |
| control `decisions_changed` | owner → viewer | `{t, seq}`: the scope's items changed; the viewer lists again (one list in flight, one queued) | `MaxControl` | `TestDecisionsChangedValid` |
| control `decide_ok` | owner → viewer | `{t, reqId, receipt, repeat?}`: the operation's receipt ref; `repeat` when the operation already held the `opId` | `MaxControl` | `TestDecideOkValid` |
| error `decide_refused` | owner → viewer | the error message with `reqId`, a code (`stale_revision`, `not_in_scope`, `not_decision_link`, `not_found`, `question_answered`, `too_many_requests`, `unsupported`) and words | `MaxControl` | `TestDecideRefusedCodes` |
| `welcome`, field `decisions` | owner → viewer | `true` to a decision link's viewer; absent otherwise and from an older owner | a boolean in the welcome (`MaxControl`) | `TestWelcomeSaysDecisions` |
| host message `registered`, field `decisions` | switchyard → publisher | `true` from a switchyard that relays `TypeDecision`, keeps desks and serves the Decide tab; absent from an older one | a boolean in a message bounded by `MaxHostMessage` (64 KiB) | `TestRegisteredSaysDecisions` |
| host message `register_desk` | publisher → switchyard | `{t, proto, run: {id, name}}`: the first message of a desk's connection | `MaxHostMessage`; `id` and `name` the run group's bounds | `TestRegisterDeskValid` |
| `RunGroup.desk` | publisher → switchyard, in the run's sync | the desk's id, absent with no desk | the run group's bound | `TestRunGroupDeskValid` |

**Paging.** The scope's items in a stable order, `(since, ref)`; `after`
is the last item's `since` and `ref` from the page before; `total` the
count in scope, so the page says "25 of 60" and offers More, never a
silent cut (a run has up to 128 tasks, `crew.go`, and questions and DR1's
records besides). An item that leaves between pages is not shown; one
that arrives is seen at the next list; a list after `decisions_changed`
starts from the top. Recent through the link follows the open items,
whole (at most 50).

**Evidence and navigation on a link.** The workbench's routes are not a
link holder's: `refRoute` gives `/runs`, `/sessions`, `/dispatch` and
`/brakeman` pages, and the step log route admits the admin or a Brakeman
turn alone (`brakeman_observe.go`'s `reader`). So on a link,
`decision_evidence` is the one read: for an entry of an item in the
scope, a check's log tail (its last 4 KiB from the ledger's row, as the
step log route serves it), or, for a session in the link's scope, its
published id (the publisher maps its own id to the published one, the
`SessionID` its publication holds, `PublishedSession`), which the join
page opens as that member's terminal tab. Any other entry (a ticket, a run page, a plan
revision, a session outside the scope) shows its label and ref and "open
it on the desktop or the LAN", no link. A ref outside the scope is
refused `not_in_scope`. `InboxItem.vue` takes the join page's route
function there.

**The owner's side.** Four places serve the lane through one interface,
`hostagent.Decisions` (`List(linkID, sessionID, after)`, `Read`,
`Track`, `Evidence`, `Watch(linkID, sessionID, func())`, `Decide(ctx,
linkID, sessionID, m) (receipt, error)`), which the API implements:
`internal/api/ws_viewer.go` (a session decision link at the server), the
desk route `GET /ws/runs/{run}/desk` (a run decision link at the
server), `internal/hostagent/peer.go` (a session decision link at a
switchyard, through the session's publication) and the desk's
connection (a run decision link at a switchyard). The wiring: a
published session's `hostagent.Options` are built by the uplink itself
(`uplink.go`, `Publish`), not by a caller, so `hostagent.Uplink` gains a
`Decisions` field, which `internal/cli/serve.go` sets where it builds
the uplink (`srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{…}})`)
from the API's implementation, and the uplink passes it to each
publication's options and to its desks. `conductor host`
(`internal/cli/host.go`, which sets `FileDeny` and `LinkMinted` on its
own `Options`) sets none: it sends no frame and refuses every lane
message with `unsupported`. The welcome is built in `session.Local`'s
attach (`local.go`), so `session.AttachOptions` gains `Decisions`, set by
the caller that knows the link (`ws_viewer.go`, `peer.go`); a desk sends
a welcome of its own, with `decisions: true` and no terminal fields. The
owner sends `decisions_changed` as the inbox's sources change for the
scope (compared by a hash of the scope's summaries per connection).

**`decide`** maps to the item's own operation, never a new one. A
question of the connection's own session is answered with the `answer`
message the connection already takes (the viewer is the session's
subscriber; its claim, and first response wins, as ever), never with
`decide`. A question of another member's session in a run link's scope
is a `decide` with `answer`, applied through that question's claim as a
routed answer is (`AnswerAs` for a choice, `Submit` with the question
and the link's name for a line), with the link as the actor; `retry`,
`accept` and `cancel` to the engine's `Retry`, `Accept` and `Cancel`
with the item's revision as the expected revision and `opId` as the
command id; any other action through the operation the inbox names for
it (each action's server-side `do`, never serialized, which PR-3 gives
DR1's kinds once DR1 accepts a link's actor). The actor is the
connection's link, never a field of the message. **A repeat first**: a
`decide` whose `opId` the operation already holds for this link's actor
(the ledger's receipt keyed by run, actor and command id; Dispatch's by
op id) is answered with that receipt, `decide_ok` with `repeat`, before
the item's current actions are judged, so a `decide_ok` lost with the
connection and asked again after a reconnect returns the first receipt,
not `not_in_scope` for an item that closed; the link must still stand.
`decision_track` reads a receipt the same way, by this link's actor, so
a link's steps are never the admin's (Brakeman's `command:` ref resolver
reads the admin's receipt, `brakeman_refs.go`, and is not used here). A
new `decide` is acted on only while the link stands and covers the item,
checked again as the operation is taken (`attachCheck`, as every link
viewer's input is), and, on a host, while the host vouches for its
server (`AttachOptions.Fresh`, `hostagent/liveness.go`), as a line is.
At most 4 `decide` and 4 reads are in flight per connection
(`too_many_requests` past them), the per-connection bound beside the
editor's and the file reads'.

**`web/app` (PR-7).** `ShareLinksModal.vue` and the run page's links gain
the "Answer decisions from this link" switch (control role only; off
with the words at a switchyard without `decisions`), and the link's row
says so. The join page (`pages/join/[token].vue`) of a decision link
opens on a **Decide** tab with the same `InboxItem` cards, fed by the
lane (`utils/transport/base.ts` takes `TypeDecision` and the controls),
paged with More, acting through `decide`, unfolding evidence through
`decision_evidence`, its Recent read through `decision_track`, with the
terminal one tab away; a run link's join page shows the run's items above
its members' grid and joins the desk for them. The decision page's "Open
on your phone" offers, on a loopback origin, a decision link for a run
or a session the person picks.

### Docs

Each PR adds its own rows to `docs/protocol.md` (PR-1 the inbox routes,
PR-3 DR1's items in the inbox, PR-5 the lane's messages, the frame and
the welcome's field, PR-6 `registered`'s field, `register_desk` and the
run group's desk). PR-6 adds an "Answering from a phone" section to
`docs/architecture.md`: the two paths, what the switchyard sees, the
desk, the decision link's scope. PR-8 writes `docs/features.md`'s entry,
the README's paragraph and the repository map's rows (the inbox in
`internal/api`, the page, the desk in `internal/hostagent`).

## Events, orderings and invariants

**The inbox is a read model.** Before DR1 it keeps nothing but the
routes a router gave, by question id: each read derives the items from
the sessions, the engine, Dispatch and Brakeman's store at that moment. An item is open exactly when its source says so at
read time (the table under "Item identity"); a page that read an item and
acts on it carries the item's `Revision`, and the operation, not the page,
judges whether that is still the state: the session refuses an answer
naming a question not shown (`question_answered`), the engine a command
at an old revision (`stale_revision`), Dispatch an approval at an old
ticket revision or an operation at an old plan revision, DR1 a response
at an old record revision. The page
then reads the item again and says what changed ("The run moved on: build
is succeeded now. Nothing was done."). So no check is made on the page and
used later.

**First response wins, unchanged.** Every answer from the page, from a
decision link, from a router and from a person at the terminal goes
through the question's claim; the page's answer is one more reply. DR1
records which response was first.

**The four steps.** For one action they are computed, in order, from the
record of that action's own outcome, never from what is true of its
subject now:

| Item | Available | Recorded | Validated | Resumed |
|---|---|---|---|---|
| question of a server session, before DR1 | the attention showed the id | the session's kept result by id: this attempt's `ReqID` the winner's (done, by, when) or another's (refused: "another answer first", by whom); forgotten (the session gone, 64 questions later): `unknown` | the same claim (the session is the owner: one step, said so) | attention `working`, or a next question; else the reason (withdrawn, another's answer, ended) |
| question of a hosted session | the attention showed the id | `unknown` on the server ("the host holds this answer"); the page shows the host's word on its own request | as recorded | the hosted session's attention since |
| task | the task's state allowed the command | the command's receipt in the ledger | the receipt's status (`done`, or the refusal) | retry: the next attempt running; accept: the dependents dispatched; cancel: does not apply (its dependents are blocked, and the step says so) |
| plan | proposed, or approved and stale | the Dispatch operation's receipt | the operation's own outcome: done, or its refusal (`stale_revision`, withdrawn) | does not apply: "a launch from the ticket page starts it" |
| decision (DR1) | DR1's four states, as DR1's record reports them and DR1's card draws them (PR-3, PR-4) | | | |

Each step is a historical fact: receipts are kept and never rewritten,
so two reads give the same steps or later ones, from any browser. What
can change afterwards is the subject, not the step, and it is shown
apart, as the item's `Now`: an approval that held when it was taken and
no longer holds because the ticket was edited since ("The approval no
longer holds: the description changed since; approve again"), then holds
again when the edit is undone (Dispatch tests both), changes `Now` and
brings the item back (or takes it away), never the validated step. A
source that forgets (a session's 64 kept results, a session gone) gives
`unknown`, the honest word, never `waiting` and never a guess. The page
never shows a step going back: once `done` or `refused` in its view it
stays (`reconcileDelivery`), and a later `unknown` or `waiting` does not
replace it; a later `refused` after `done` cannot come from a kept
receipt. A `refused` validation ends the row: resumed is
`not_applicable` with the refusal's words.

**Recent.** Before DR1, the browser that acted keeps `{ref, receipt,
attempt?, outcome?, at}`, written before the action's reply, the reply's
outcome added when it comes, and reads each entry's steps from
`/api/inbox/track`, so another browser does not see this one's recent
actions (said on the page: "Acted on from this browser"). Two browsers
under one workbench token that answer one question differently send two
request ids: after a reload, the one whose id is not the kept result's
reads "another answer first", never the winner's receipt as its own.
From PR-3 the server lists DR1's closed records for every viewer.

**The lane.** On a decision link's connection: `welcome` first; every
read and `decide` is answered by exactly one `TypeDecision` frame,
`decide_ok` or `decide_refused` naming its `reqId`; `decisions_changed`
in the order the owner sees the changes, carrying no content. A `decide`
whose operation began when the connection closed is not taken back: its
receipt stands, and the same `opId` sent again after a reconnect is
answered with it (`repeat`) before the item's current state is judged;
one not begun is never begun. A link revoked or expired ends the
viewer's connection as today (`link_gone`, `OnRevoke`), and a `decide`
checked after the revocation is refused, so nothing of a link's acts
lands after its end. A run's desk lives while the run and one of its
decision links do, so a run with no member alive (a gate before the
first attempt, the last attempt failed) is still reached through its
link.

**State and migration.** No database or file schema changes in any PR:
the inbox stores nothing but the routes kept by question id (in memory),
`share.Link.Decide`, `remoteLink.decide`, a link's Recent and a run's
desk are in memory as the links and publications are, the session's
kept result gains its request id in memory, and the browser's recent
list is a per-browser convenience that may be empty at any read. PR-3
reads DR1's tables through DR1's routes and changes none of them.

## Out of scope

- Push, the managed inbox that keeps working with the owner offline,
  escalation and any hosted retention of responses: MB1 (D9, D19).
- Delegation, quorum, domain roles, authenticated responders: SW1 and ID1
  (D4); the decisions open there (O4 for SW1, O10 for CS1) are theirs.
- The decision record, its store, its routes, its delivery to the work,
  gates no agent asks for: DR1. Human member labels: HM1.
- A native or installable app, a web app manifest, a service worker: MB1
  (the Capacitor app, #160).
- Review findings and candidate reviews as items: RV1's
  (`docs/plans/review-repair.md`) to add as an item kind if it wants
  them; the inbox's kinds table is the one place a kind joins.
- A CLI or MCP view of the inbox: the operations it lists already have
  their CLI and tools (`conductor crews`, `conductor dispatch`,
  `conductor brakeman`); DR1's plan owns `conductor` commands for its
  records.
- A way for a link holder to see or approve a ticket's plan revision
  before its run exists (Owner decisions, 8).
- A run's pause, unpause and stop as decisions: they are the run page's
  controls (stop is a safety control that must always work, article 4).

## Tests per surface

Per `docs/checks.md`. Every Go change runs `make lint`, `go test -race
-count=1 -timeout 30m ./...` and `make lint-static`; every UI change
`npm --prefix web run typecheck`, `npm --prefix web test` and `make
test-e2e` (the PR's specs alone first). No Python and no desktop file is
changed, so the scripts suite and `make desktop-test` are not affected.
The new tests, by PR:

**PR-1** (Go race and one vitest case; `internal/api`, `internal/crew`,
`internal/dispatch`, `internal/session`):

- `TestInboxListsAQuestionAsTheSessionShowsIt`: the stub's question with
  three choices is an item with three `answer` actions whose inputs are
  the options' and whose scope names each key.
- `TestInboxLeavesOutAQuestionAnsweredInTheTerminal` and
  `…ASessionNoLongerListed`.
- `TestABlockerIsOneItem`: an attempt reports a blocker (`Engine.Blocked`):
  one item, the session's question, naming the task and the blocker and
  carrying Cancel; no task item; the task failing later is then its own.
- `TestInboxTaskItemsFollowTheEngine`: for every row of
  `internal/crew/testdata/task-commands.json`, the item's actions equal
  what the engine then accepts, each command sent at the item's revision
  succeeding and each other refused; Accept anyway only where a kept
  attempt submitted a commit, none after a withdrawal took it back.
- `TestTaskCommandsMatchTheRunPage` (Go) and its pair in `tasks.test.ts`
  (vitest): the same fixture against `TaskCommands` and `taskActions`.
- `TestInboxBlockingNamesTheDependents`: a diamond of four tasks; the
  failed root's item names three dependents; past 20, `more`.
- `TestInboxPlanItems` and `TestOpenPlans` in `internal/dispatch`: a
  proposed revision is an item; an approved one is not, until the
  ticket's description is edited (an item again, Approve again offered,
  its `Now` saying why), and not once the edit is undone; a
  `RemedyRevise` one has no Approve; deleted tickets left out, oldest
  first, the limit.
- `TestInboxPlanActionsCarryBothRevisions`: the ticket edited alone, an
  Approve at the item's revisions is refused `stale_revision` for the
  ticket; the revision approved by another operation alone (its `rev`
  bumped, the ticket's not), a Withdraw at the item's `Revision` is
  refused; both at fresh revisions succeed.
- `TestInboxPlanScopeSaysYolo`: a yolo-on revision's Approve scope says
  so and names the approval it supersedes; a revision whose body cannot
  be read has no Approve and says why.
- `TestInboxCarriesTheNewestBrakemanCard`: two briefs about one task; the
  newer card's fields, its 2 KiB `happened` whole, its `authorizes` in
  `Authorizes` beside Conductor's scope, and a card read at another
  revision says so.
- `TestInboxPlanSubjectBeforeBrakemanPlanRefs`: while `brakeman.ParseRef`
  refuses the `plan` kind, a plan item's `subject` is its `ticket:` ref.
- `TestInboxRoutedToKeyedByQuestion`: a test router routes a question to
  a person; the item names it; with the empty route, nothing; a route
  held at the test router's gate while the question is replaced, then
  let go, names the old question and not the new; destinations at
  `MaxDestinations` with names of 60 characters holding commas and
  parentheses come back whole.
- `TestInboxSummaryBounded`, `TestInboxItemBounded` and
  `TestInboxReplyBounded`: every field at its bound filled with
  characters that escape to six bytes; a summary at most 4 KiB, an item
  at most 96 KiB, 150 summaries and the reply at most 640 KiB, encoded.
- `TestInboxPages`: 200 open items over two pages, `next` and `total`,
  none lost or repeated.
- `TestInboxTrackCommand`: retry at the item's revision: recorded and
  validated done, resumed waiting until the next attempt runs, then done;
  at an old revision: validated refused with `stale_revision`, resumed
  not applicable.
- `TestInboxTrackQuestion`: answered through the claim: recorded and
  validated done at the answer's time, resumed done once the attention is
  `working`; withdrawn by the screen: the reason; two answers with two
  request ids: the loser's track says "another answer first" and the
  winner's done; the result forgotten past 64: `unknown`.
- `TestInboxTrackHostedQuestion`: a hosted session's question: recorded
  and validated `unknown` with the words, resumed from its attention.
- `TestInboxTrackPlanApproval`: the approval's validated step stays done
  after the ticket is edited, while the item's `Now` says it no longer
  holds, and after the edit is undone; two reads, as two browsers, give
  the same steps.
- `TestInboxIsAdminOnly`, `TestInboxOnASwitchyard` (`403 switchyard`).
- `TestQuestionResult` in `internal/session`: the kept result by id with
  its request id, the 65th forgotten.

**PR-2** (vitest, Playwright):

- `utils/inbox.test.ts`: the order; each step's words; `reconcileDelivery`
  never moving a step back over replies in either order and keeping a
  `done` over a later `unknown`; the recent list (50 kept, older than a
  day dropped, storage that throws read as empty, the reply's outcome
  written into its entry); `phoneLanding` (below `lg` with an open item:
  `/decisions`; at `lg`, or with none: stay); `notificationTarget` (below
  `lg` the item, at `lg` the session); `phoneOffer` (a LAN origin: the QR
  code of `origin + /decisions`, no token in it; a loopback origin: the
  decision link offer).
- `composables/useInbox.test.ts`: one read in flight and one queued over a
  burst of 20 events.
- `composables/usePaletteRows.test.ts`: "Go to Needs your decision" and
  one row per open item, by ref.
- `tasks.test.ts`: the shared fixture (above).
- `web/e2e/decision-page.spec.ts`, on a phone viewport (390 × 844, touch,
  as `phone.spec.ts`), after `goto` waiting for the bottom bar's root
  before any tap: the app opened at `/` with the stub's question waiting
  lands on `/decisions`, and the Sessions tab then stays on `/`; the
  question answered from the Decide tab, its four steps shown (recorded
  and validated "by the session", resumed once the stub works again); a
  managed task failing its check, retried from its card with the
  receipt's steps; a retry at an old revision refused with the words and
  nothing done (the revision moved through a seam: a second retry from
  the API first, never a sleep); a plan revision approved, its yolo
  words shown first; the evidence unfolded and the session's terminal
  opened from it; the item counted by its ref, never by the list's
  length (`stopStrays` first). At a desktop viewport, after waiting for
  the layout's sidebar root: the sidebar's Decide row opens the page;
  `G` `N`, pressed once, opens it; the palette's item row opens the
  item; Open on your phone shows the page's URL (its `data-` attribute,
  no token in it). Each step's wait is budgeted with
  `testInfo.setTimeout` as `palette-rows.spec.ts` does.
- `phone.spec.ts`: the bar with Decide first and Events under More.
- `brakeman-cards.spec.ts` unchanged and passing (the extracted
  `BrakemanDecisionText.vue`).

**PR-3** (Go race; `internal/api`):

- `TestADecisionRecordReplacesItsQuestion`.
- `TestInboxDecisionActionsUseDR1sRoutes`: answer, approve and reject
  through the respond operation, more information and a comment through
  the comment operation, each with its id and the record's `rev`; a
  repeat answered with the first receipt.
- `TestADecisionSurvivesARestart`: the record open across a server's
  restart over the same data directory, its item the same; after a
  response, its four states the same after the restart (servers ended
  later first, as AGENTS.md's rule for one data directory says).
- `TestALinkShowsADecisionRecordWithoutActions`, or, once DR1 accepts a
  decision link's actor under `local/1`, `TestALinkAnswersADecisionRecord`
  in its place: a decision link's `decide` on a DR1 record through the
  action's `do`, its responder the link.

**PR-4** (vitest, Playwright): `utils/inbox.test.ts` cases for the
decision kind; `web/e2e/decision-page-durable.spec.ts` on the phone
viewport: a gate a plan requires (DR1) answered from the Decide tab
through DR1's card, the server restarted (a server of the spec's own
over its data directory, stopped and started again as `brakeman.spec.ts`
does), the item's four states the same after; Ask for more information
keeps it open and names the note; a response at a stale record refused
with words; the ticket page's records linking to the item.

**PR-5** (Go race and vitest; `internal/proto`, `internal/share`,
`internal/session`, `internal/api`, `web/app/utils/protocol.ts`):

- `TestDecisionFrameBounded`, `TestDecisionsListValid`,
  `TestDecisionReadValid`, `TestDecisionTrackValid`,
  `TestDecisionEvidenceValid`, `TestDecideValid`, `TestDecideOkValid`,
  `TestDecisionsChangedValid`, `TestDecideRefusedCodes`,
  `TestWelcomeSaysDecisions`; `utils/protocol.test.ts` cases for each in
  the same PR.
- `TestADecisionLinkNeedsControl` (`share`, `api`).
- `TestADecisionLinkReceivesItsScope`: a run link sees the run's items and
  no other run's; a session link its session's question and task.
- `TestADecisionLinkPagesItsScope`: a run of 60 failed tasks over three
  pages, `total` 60, none lost or repeated, an item resolved between
  pages absent from the next.
- `TestAControlLinkWithoutDecideReceivesNone` and is refused
  `not_decision_link`.
- `TestDecideRetryRecordsTheLinkAsActor`: the receipt's actor is
  `link:<id>`, and `decision_track` reads that receipt, not the admin's.
- `TestDecideLostReplyReplays`: a `decide` taken, its connection closed
  before `decide_ok` (a seam holds the reply), a new connection's same
  `opId` answered with the first receipt and `repeat`, the item closed by
  then; nothing done twice.
- `TestALinkKeepsItsRecent`: a task retried through the link leaves the
  open items and stays in the link's Recent with its four steps through
  to resumed.
- `TestDecisionEvidenceIsScoped`: a failed check's log tail read through
  the lane; a ref outside the scope refused `not_in_scope`.
- `TestTheServersDeskServesARunWithNoLiveMember`: a run link at the
  server, `GET /ws/runs/{run}/desk`, before any attempt (a gate's run
  whose first task waits) and after the last member's attempt failed:
  the items listed and a retry taken.
- `TestDecideAfterRevokeIsRefused`: a `decide` held at a gate in the
  owner until the link is revoked, then let go: refused, nothing done
  (the gate is a seam of the test, the order fixed, no sleep).
- `TestDecideInFlightBound`: the fifth refused.
- The lane's tests `-count=30` before the PR is ready (AGENTS.md, Tests
  that do not flake).

**PR-6** (Go race; `internal/proto`, `internal/signal`, `internal/api`,
`internal/hostagent`, `internal/cli`):

- `TestRegisteredSaysDecisions`, `TestRegisterDeskValid`,
  `TestRunGroupDeskValid`.
- `TestPeerSendsDecisionFrames` and `TestPeerRefusesDecideWithoutDecisions`
  in `internal/hostagent`, over its relay as `peer_test.go` drives it;
  `TestConductorHostRefusesTheLane` (`unsupported`).
- `TestUplinkPassesDecisions` (`internal/cli`, `internal/hostagent`): the
  serve command's uplink hands the API's `Decisions` to each publication
  and desk.
- `TestADeskCarriesARunWithNoLiveMember`: a run decision link at a
  switchyard minted with no member published (a gate before the first
  attempt), its items listed and answered over the desk; the last
  member's session ending leaves the desk and the link working; the
  run's end ends the desk after the linger; the last decision link
  revoked ends it.
- `TestRelayCarriesDecisionFrames` (`internal/proto`, `internal/api`): a
  relayed `TypeDecision` reaches the viewer and the host's connection
  stays; `TestRelayDropsTheLaneFromAViewer` (`internal/signal`): a
  view-role viewer's `decide` and reads are dropped at the relay.
- `TestNoDecisionLinkAtAnOlderSwitchyard`: a switchyard whose
  `registered` says nothing of decisions and which closes
  `register_desk`: `decide: true` refused `409 switchyard_too_old`, no
  `TypeDecision` sent over its relay, the host's connection stays.
- The lane's tests `-count=30` before the PR is ready.

**PR-7** (vitest, Playwright): `transport` cases for the frame and the
controls (`utils/transport/base.test.ts`); `web/e2e/switchyard.spec.ts`
gains a decision link test: the second server of the suite's shape that
already publishes to the spec's switchyard mints a decision link for a
managed run whose task fails; a phone context opens the link at the
switchyard, sees the item, unfolds its failed check's log, retries it,
and its steps reach resumed, read again from the link's Recent after the
item closed; the run's member's question answered the same way; a run
whose only member has ended still answered through the desk; over the
switchyard's relay, which the spec forces as it does for its host (a CI
box may have no route for ICE).

**By hand** (the Acceptance below): a real phone's browsers (iOS Safari
and Android Chrome), touch and a real screen's size; real agents' real
questions (Claude Code's permission dialog, Codex's command approval);
the LAN reached by a phone; a self-hosted switchyard on a machine that is
not WSL (a phone cannot reach a server inside WSL without networking the
person would have to configure, which Conductor never asks for).

## Boxcar constraints

The eight of the Program, section 2.5:

1. **Attempts start only through `crew.Launcher`.** Honoured: a retry from
   the page or a link is the engine's `Retry`, whose next attempt starts
   through the launcher as every attempt does; nothing here spawns.
2. **`session.Local` drives a `session.Process`.** Not touched: answers go
   through the claim (`AnswerAs`, `Submit`, the viewer's `answer`).
3. **An attempt's context is files under its worktree.** Not touched:
   nothing an attempt needs is added; DR1 delivers its responses, under
   its own plan's constraints.
4. **The agent reaches the server over the network.** Not touched.
5. **Checks through `internal/proc`, carrying where they ran.** Not
   touched: the page shows a check's `where` as the ledger records it.
6. **Platform splits stay platform tags; the UI shows capabilities.**
   Honoured: no platform split; delegation would show only as an action
   the server lists (Owner decisions, 6).
7. **Evidence rows carry where; none assumes a native process.**
   Honoured: no evidence row is added; the items' evidence reads the rows
   that exist.
8. **`hostagent` is a launch site as much as `api`.** Honoured for what
   this item adds: the lane is served by both owners, the server's
   viewer connection and desk route and the hostagent's peer and desk,
   and the Runtime's wiring is not changed.

## Open-core checklist

- [x] Article 1: no premium code and no licence check; the only
      conditional control is an action the server lists.
- [x] Article 2: the page, its LAN path and its switchyard path are in the
      open-source build and need no account (D9, D26).
- [x] Article 3: nothing paid is added; push and the managed inbox stay
      MB1's, behind cloud.
- [x] Article 4: every approval goes through the operation that checks
      it, at the revision read; nothing times out into an answer; stop,
      inspect and revoke are untouched, and a revoked decision link acts
      no more.

## The PRs

Each PR is one issue and one branch cut from `main`, a draft PR at its
first commit, the checks for its surfaces before every push, its body
ticking the open-core checklist and naming the agent that made it; each
leaves `main` working and green on its own (the inbox route without its
page is reached by its tests; the page before DR1 says "Durable decisions
arrive with DR1" where an action is not there yet). Parallel means the two
touch no common Go package, no common directory under `web/` and no
common file under `docs/`; two PRs that do are ordered by a dependency,
so the order below is the merge order wherever they share. Sizes are the
first push's changed lines with tests; DS2's and Brakeman's PRs grew past
their estimates in review, so none here is planned above about 1,400.

| PR | Title | Touches | Depends on | Parallel with | Size |
|---|---|---|---|---|---|
| PR-1 | api: the decision inbox on what main holds (questions, tasks, plan revisions, Brakeman's cards) and the four steps of an action | `internal/api`, `internal/crew`, `internal/dispatch`, `internal/session`, `web/app/utils/tasks.test.ts` (the shared fixture's case), `docs/protocol.md` | none (a plan item's `subject` is `ticket:` until Brakeman's PR-10, which nothing here waits for) | none | ~1,400 |
| PR-2 | web: Needs your decision, the phone's Decide tab and landing, the palette, the shared card text | `web/app`, `web/e2e` | PR-1 | none | ~1,400 |
| PR-5 | proto, share, session, api: decision links and the lane at the server, the server's desk route | `internal/proto`, `internal/share`, `internal/session`, `internal/api`, `web/app/utils/protocol.ts` and its test, `docs/protocol.md` | PR-1 (`internal/api`, `docs/protocol.md`), PR-2 (`web/app`) | none | ~1,300 |
| PR-6 | proto, signal, api, hostagent, cli: the lane through a switchyard, the run's desk | `internal/proto`, `internal/signal`, `internal/api`, `internal/hostagent`, `internal/cli`, `docs/protocol.md`, `docs/architecture.md` | PR-5 (`internal/proto`, `internal/api`, `docs/protocol.md`) | none | ~1,300 |
| PR-7 | web: a decision link's join page, the share switch, through a switchyard | `web/app`, `web/e2e` | PR-2 and PR-5 (`web/app`), PR-6 (the switchyard's side the spec drives) | PR-3 | ~1,000 |
| PR-3 | api: DR1's records as items, through DR1's routes | `internal/api`, `docs/protocol.md` | PR-6 (`internal/api`, `docs/protocol.md`), DR1's PRs that serve the record, its respond and comment operations and its event (`docs/plans/decisions.md`) | PR-7 | ~700 |
| PR-4 | web: DR1's records on the page, through DR1's card, and the ticket page's links | `web/app`, `web/e2e` | PR-3, PR-7 (`web/app`), DR1's web PR (`DecisionCard.vue`, `DecisionRespond.vue`) | none | ~600 |
| PR-8 | the acceptance check, what it finds fixed, the features entry | `docs`, `README.md`, and the packages a found bug lives in | PR-4, PR-7 | none | ~300 and what the check finds |

The numbers name the PRs; the table's order is the order they merge in.
PR-3 and PR-4 wait for DR1 whatever happens: should DR1's PRs merge
first, PR-3 still comes after PR-6 (one `internal/api` change at a time,
in this order), so nothing waits on a rebase it did not plan for.

**PR-1** (`internal/api`, `internal/crew`, `internal/dispatch`,
`internal/session`): `inbox.go` whole (the two types and their encoded
bounds, the three kinds, the blocker as one item, Brakeman's card, the
routes kept by question id, the blocking walk, the scope words, the
plan's two revisions and yolo words), the three routes and
`inbox_test.go`; `crew.TaskCommands` and its fixture; `dispatch.Store.
OpenPlans`; `session.Local.QuestionResult` and the kept request id; the
routes' rows in `docs/protocol.md` (HTTP API). Tests: PR-1's above.

**PR-2** (`web/app`, `web/e2e`): the page, its components and the
composable, `utils/inbox.ts`, `BrakemanDecisionText.vue` extracted from
`BrakemanDecision.vue`, the bottom bar and the phone's landing, the
sidebar's row, the palette's rows and the `G` `N` chord (and its line in
the `?` list), the notification's target below `lg`, Open on your phone
(the URL's QR code), `taskActions` brought to the fixture where it
differs from the engine; `decision-page.spec.ts`; `phone.spec.ts`'s bar
test updated for Decide first and Events under More. Tests: PR-2's
above.

**PR-5** (`internal/proto`, `internal/share`, `internal/session`,
`internal/api`, `web/app/utils/protocol.ts`, `docs`): `Decide` on a link,
the link routes' `decide` field, the frame, the lane's messages and the
welcome's field in all three protocol places with `protocol.test.ts`,
`session.AttachOptions.Decisions`, `hostagent.Decisions` as an interface
the API implements, `ws_viewer.go`'s handling, the server's desk route,
the scope, paging, evidence reads, a link's Recent, the replay and the
in-flight bounds. Tests: PR-5's above, the lane's `-count=30`.

**PR-6** (`internal/proto`, `internal/signal`, `internal/api`,
`internal/hostagent`, `internal/cli`, `docs`): `registered`'s
`decisions`, `register_desk` and `RunGroup.desk` in all three protocol
places, the switchyard's desk registration and its join, the relay's list
of the frames it carries and `RelayToHost`'s view-role drops,
`remoteLink.decide`, `peer.go`'s handling, `hostagent/desk.go`, the
uplink's `Decisions` and its wiring in `cli/serve.go`, `conductor host`'s
refusal, the `409 switchyard_too_old` refusal, the architecture section.
Tests: PR-6's above, the lane's `-count=30`.

**PR-7** (`web/app`, `web/e2e`): the transport's handling of the frame
and the controls, the join page's Decide tab with paging, evidence and
the link's Recent, the run link's items over the desk, the share switch
on the session's and the run's links with its words at an older
switchyard, the loopback origin's offer of a decision link; the
switchyard spec's decision link test. Tests: PR-7's above.

**PR-3** (`internal/api`): the `decision` kind from DR1's routes, the
dedupe against questions, the actions through DR1's respond and comment
operations, DR1's four states carried as the record reports them, Recent
on the server, cards about `decision:` refs, the re-read on DR1's event,
the link's read-only record (or, once DR1 accepts a link's actor, its
answer), the protocol rows. Waits for DR1's PRs that serve the record;
until they merge it is not cut. Tests: PR-3's above.

**PR-4** (`web/app`, `web/e2e`): the decision kind on the page through
DR1's `DecisionCard.vue` and `DecisionRespond.vue`, the server's Recent
for DR1's records, the ticket page's links to the items;
`decision-page-durable.spec.ts`. Tests: PR-4's above.

**PR-8** (`docs`, `README.md`, and any package a bug the check finds is
in): the by-hand acceptance check (below), every bug of the stream it
finds fixed here (the stream's rule, AGENTS.md), `docs/features.md`'s
entry (what each PR shipped and verified, and the check), the README's
paragraph, the repository map's rows.

## Owner decisions

Dated 2026-11-05. Each is a default the owner can overturn with a dated
row in the Program's status log, its convention for a decision the owner
did not make (the Program, section 3.1); the reasoning is why it was
taken.

1. **O4 and O10 are not this item's.** O4, the Program's open question
   of the first human policy (who may answer what, routing to people),
   blocks SW1, Switchman's team routing; O10, its open question of how
   Dispatch's edits from two devices are reconciled, blocks CS1, sync
   and continuity. The page has one answerer, the operator (D4), and
   replicates nothing, so it settles neither and waits on neither. No
   open decision of the Program blocks MB0.
2. **The page is built on what `main` has first.** PR-1 and PR-2 serve
   questions, tasks, plan revisions and Brakeman's cards today; DR1's
   records join from PR-3. The page and the phone's path are useful
   before durable decisions exist, and DR1 changes only where an item
   comes from, not how it looks.
3. **DR1 owns the record, MB0 the inbox.** DR1 (`docs/plans/decisions.md`,
   in review as PR #608) owns the record, its store, its routes (the
   respond and comment operations among them), its delivery, its
   receipts, its event, its four states and its two components,
   `DecisionCard.vue` and `DecisionRespond.vue`; HM1 the labels; this
   plan the read model, the page and its phone layout, the four steps'
   view of the items DR1 does not hold, the two phone paths and decision
   links. The page renders DR1's components for a record and calls
   DR1's routes; its own files are named for the inbox so the two plans
   never write the same file. One owner per record keeps "applies once"
   in one place. Whether a decision link may answer a DR1 record (DR1's
   `local/1` takes only the admin grant) is settled between the two
   plans in review; until it is, a link shows a record without actions.
   Brakeman's plan owns the `decision/1` card; the page shows a card's
   text and never its alternatives as controls, as Brakeman's own page
   does.
4. **An item is named by Brakeman's ref of what it is about.** One
   grammar for the page, the cards and the evidence, so a card attaches
   to its item by string equality and no new id space is minted.
5. **Conductor recommends nothing itself.** A recommendation and an
   uncertainty come only from Brakeman's card or from DR1's record (its
   requester's); otherwise the card says "No recommendation". A
   heuristic recommendation from the server would be presented as
   Conductor's judgement without evidence behind it.
6. **No delegation in the open-source build.** Delegation is Team (D4).
   The page shows an action only when the server lists it for the item,
   and nothing in this repository lists delegation; SW1's plan may add
   it through the router seam.
7. **On the LAN, the workbench token is entered once.** The phone opens
   the page's address and is asked for the token as any browser is (the
   token gate); the QR code holds the address alone. A QR code holding
   the workbench token would show the server's full credential on a
   screen.
8. **Through a switchyard, decisions ride a decision link's own
   connection.** A session link rides its session's publication; a run
   link rides the run's desk, a registration of the publisher's that
   carries nothing but the lane and lives as long as the run and its
   decision link, so a run with no member alive is still reached. The
   switchyard stores nothing of a decision, relays the lane's frame when
   ICE fails as it relays terminal bytes, and keeps the desk's
   registration as it keeps a session's; the link's scope is its session
   or its run. A plan revision's approval before any run has no link to
   ride and is answered on the LAN or the desktop. Chosen over a lane at
   the switchyard outside any link, which would make the switchyard hold
   or relay decision content no link scopes and change what it is
   trusted with.
9. **A decision link is a control link the operator chose to make one,
   and its acts are the link's.** Minted only by the workbench, control
   role only, shown as such in the links list; a command or a response
   sent through it records the actor `link:<id>` with the link's label,
   never the workbench. The operator stays the single approver (D4) and
   the receipts say which credential acted.
10. **Decide is the phone's first tab and where a phone opens.** The bar
    keeps four tabs and More (Decide, Sessions, Yard, Crews); Events
    moves under More. A tapped question notification opens the decision
    item below `lg`. The Program makes the decision view the primary
    mobile view (the Program, section 4.3, MB0), so the first load of
    `/` in a tab below `lg` lands on `/decisions` when an item is open;
    `/` itself stays the sessions list, and the Sessions tab and every
    later `/` go there, so a link to `/` and the desktop do not change
    under people.
11. **Oldest first.** Items are listed in the order they were asked: no
    timer ends a question, and the oldest has waited longest.
12. **Before DR1, a browser keeps its own recent actions.** At most 50,
    a day at most, read back from the durable receipts (the ledger's,
    Dispatch's) and, for a question, the session's memory by the
    answer's request id, which the page says, and `unknown` where that
    memory is gone. A decision link's recent actions are kept with the
    link by the server that owns it. From PR-3 the server lists DR1's
    closed records for everyone.
13. **One thing needing a person is one item.** A failed task is an
    item; a task blocked only by a failed input is named in that input's
    item; a blocker an attempt reports is its session's question (the
    engine asks it there), which names the task and carries its Cancel,
    never a second item for the task.
14. **A responsive page, nothing more.** No web app manifest, no service
    worker, no install prompt, no web push: MB1 is the Capacitor app with
    APNs and FCM (D19), and a page that claimed to be an app would
    promise what it does not keep.
15. **The route is `/decisions`, the page "Needs your decision", the tab
    "Decide".** Short for the bar's 44 px targets; "Needs you" stays the
    attention's name.

## Acceptance

The boundary, in this plan's words: a decision is resolved from a phone
on the LAN, and from a phone through a self-hosted switchyard, and the
page says whether the response was recorded and whether it was applied.
It is observed, not ticked:

- **On the LAN**, once PR-4 is merged: the owner's `conductor serve` on a
  machine a phone on the LAN reaches (the LAN Linux box or the Mac; not
  inside WSL, which a phone reaches only through networking the person
  would configure: AGENTS.md, Windows and WSL), with Claude Code
  on the owner's subscription in a managed run launched from a Dispatch
  ticket's approved revision whose plan requires a decision before a task
  (DR1's gate), a check that fails on its first run, and a member that
  asks a permission question. On a real phone (iOS Safari, then Android
  Chrome): the Decide tab shows the three items with their briefs; the
  gate answered, its steps read recorded, then applied, then resumed as
  the task starts; the failed task retried, its steps through to the next
  attempt; the question answered with a choice, its steps through to the
  agent working; one response sent at a revision made stale on the
  desktop first, refused with words, nothing done; the server restarted,
  the gate's receipt the same.
- **Through a self-hosted switchyard**, once PR-7 is merged: a
  `conductor switchyard` the owner runs (on the LAN Linux box, or a small
  host of their own), the owner's server publishing to it (the desktop
  app on Windows publishes outward, so no networking is configured), a
  decision link for the run minted and opened on the phone through the
  switchyard (on the LAN, or from mobile data where the switchyard's
  machine is reachable from the internet): the run's items there, one answered, its steps through
  to resumed; the gate answered there before the run's first attempt has
  started, with no member alive, over the run's desk; the link revoked
  on the desktop and the phone's next action refused.
- What was missing, slow or wrong goes into `docs/features.md` (PR-8); a
  bug the stream caused is fixed in the stream before it is called done,
  a defect outside it filed.
- Done means every PR merged and both observations recorded with the
  owner's word that the phone answered what needed deciding and said
  whether it was recorded or applied; the Program's status row moves to
  Done then. The R1 release gate (the Program, section 6) needs this
  item's decision answered from a phone as one of its steps (the
  Program, section 6: the release gates, R1 the first release's).

What needs the owner: the review of this plan and its decisions; cutting
the eight issues; DR1's plan and PRs, which PR-3 waits for; the phone and
the agents' subscriptions for the observation; the merges.
