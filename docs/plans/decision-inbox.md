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
  not cut yet. This plan's PR-1, PR-2, PR-5 and PR-6 need none of it;
  PR-3 and PR-4 wait for DR1's PRs that serve the record, and the lane of
  PR-5 carries DR1's records once PR-3 has put them in the inbox.
- **HM1**, human members as local labels (same plan as DR1): once it
  ships, a decision can name a label ("architecture owner") the operator
  answers for. The page shows the label where the record carries one;
  nothing here waits for it.
- **Brakeman's PR-10 to PR-18** (in flight): the `plan:` ref (PR-10), the
  `decision:` ref (PR-12), and `route_decision` (PR-12, PR-18), Brakeman
  routing a decision record. The page shows a card about any of these
  once the card's ref is one the server resolves, and the routing a
  record carries; it owns neither.

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
   calls no provider; what a router said is read from the session's
   activity, never asked of the router on the read's path.
9. **CGO_ENABLED=0, cross-compiled.** No new dependency; the QR code uses
   the app's own encoder (`web/app/utils/qr.ts`).
10. **The repository contract.** This plan before the code; one issue per
    PR; the race tests, vitest and Playwright per surface; the protocol
    change of PR-5 in `internal/proto`, `web/app/utils/protocol.ts` and
    `docs/protocol.md` together, each new frame and message with a size
    limit and a test.
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
  **Recent**, the items acted on in the last day with their receipts. A
  browser notification for a question, tapped on a phone, opens that
  item there; at `lg` and up it opens the session as today.
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
    commit 1a2b3c4 with attempt 3's findings; run revision 12");
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
  this link** (control links only). The link opened on the phone (its QR
  code) shows the same cards for what the link covers: the session's
  question and its attempt's task, or every item of the run, and their
  receipts, carried end to end over the link's own connection; the
  switchyard stores nothing of them.

A responsive page is all this is: no installable app, no web push, no
service worker (Owner decisions, 14); the Capacitor app with push is MB1
(D19).

## The changes

### `internal/api`: the inbox read model (PR-1)

A new file `inbox.go` builds the page's items from what the server holds,
on each read, with no state of its own (before DR1) and no wait on any
provider.

**Item identity.** An item is named by the ref of what it is about, in
Brakeman's ref grammar (`docs/plans/brakeman.md`, Evidence references),
so a card and an item about the same thing share a name:

| Kind | Ref | Source | Open while |
|---|---|---|---|
| `question` | `question:<session>/<question>` | the session's `Info().Attention` | the session is listed, its attention is `needs_input` with that id, and it is not `answered:"terminal"` |
| `task` | `task:<run>/<task>` | the engine's `RunInfo`, live managed runs | the task is failed, or has an open blocker report, and `crew.TaskCommands` (below) allows at least one command |
| `plan` | `plan:<KEY-n>/<revision>` (Brakeman PR-10's form) | Dispatch's proposed revisions (`ProposedPlans`, below) | the revision is proposed, neither approved nor withdrawn, and its ticket not deleted |
| `decision` | `decision:<id>` (Brakeman PR-12's form) | DR1's records (PR-3) | DR1's record is open |

A task blocked only because an input failed is not its own item: it is
named in the failed input's blocking (Owner decisions, 13).

**Types** (JSON, every bound checked as built and held by a test at the
bound):

```go
// inboxItem is one thing a person must decide, as the page shows it.
type inboxItem struct {
	Ref       string    `json:"ref"`            // at most 512 bytes (a Brakeman ref)
	Kind      string    `json:"kind"`           // question | task | plan | decision
	Since     time.Time `json:"since"`
	Title     string    `json:"title"`          // at most 200 bytes
	Brief     string    `json:"brief"`          // at most 1 KiB
	BriefBy   string    `json:"briefBy"`        // conductor | brakeman
	Card      string    `json:"card,omitempty"` // brief:<br-n>/<item>, when Brakeman's
	Question  string    `json:"question"`       // at most 500 bytes
	Answerer  string    `json:"answerer"`       // "you, the operator"; a label with HM1
	RoutedTo  []inboxDestination `json:"routedTo,omitempty"` // at most 8, with the rule
	Revision  string    `json:"revision"`       // what every action carries (below)
	Blocking  inboxBlocking      `json:"blocking"`
	Actions   []inboxAction      `json:"actions"`  // at most 10
	// Detail only (GET /api/inbox/item):
	WhyHuman     string             `json:"whyHuman,omitempty"`     // the card's, 1 KiB
	Considered   []cardAlternative  `json:"considered,omitempty"`   // the card's alternatives and tradeoffs, text only
	Recommended  int                `json:"recommended"`            // index into Considered, -1 for none
	Uncertainty  string             `json:"uncertainty,omitempty"`  // the card's, 1 KiB
	Evidence     []inboxRef         `json:"evidence,omitempty"`     // at most 20
	Delivery     []deliveryStep     `json:"delivery"`               // always four
}

type inboxAction struct {
	Name    string `json:"name"`    // answer | retry | accept | cancel | approve | withdraw | ask_brakeman | comment_ticket; DR1's from PR-3
	Label   string `json:"label"`   // at most 80 bytes: the choice's label, or the command's
	Input   string `json:"input,omitempty"` // a question's choice: its keys (at most proto.MaxAnswerInput)
	Text    bool   `json:"text,omitempty"`  // a line is typed (a free-text question)
	Scope   string `json:"scope"`   // at most 1 KiB: exactly what it does, to what, at which revision
	Confirm bool   `json:"confirm,omitempty"` // asked first (Accept anyway, Cancel, Withdraw), as their pages ask
	Route   string `json:"route"`   // the operation's own route, which the page calls
	do      func(ctx context.Context, actor principal, a decideArgs) (receipt string, err error) // never serialized: the same operation, for the lane (PR-5)
}

type deliveryStep struct {
	Step    string     `json:"step"`    // available | recorded | validated | resumed
	State   string     `json:"state"`   // done | waiting | refused | not_applicable
	At      *time.Time `json:"at,omitempty"`
	Words   string     `json:"words"`   // at most 500 bytes
	Receipt string     `json:"receipt,omitempty"` // a ref: command:, op:, question:, later DR1's
}
```

`inboxBlocking` is `{words (500 bytes), refs (at most 20 task, run, session
or ticket refs), more (how many past 20)}`; `inboxRef` is `{ref, label (200
bytes), to (the page's route, as `refRoute` gives it)}`;
`inboxDestination` is `{kind, name, rule}` as `ext.Destination` and
`ext.Route.Rule` bound them.

**What each kind carries.**

- *Question*: `Revision` is the question's id, which an answer names
  (`proto.Answer.Question`), so an answer to a question gone or replaced is
  refused by the session, never typed into the next. Actions: each of
  the attention's options as `answer` with its `Input` (a `Mode` choice
  last, as the reply bar orders it), and `answer` with `Text` for a
  question of the `prompt` kind or none; `Scope` names the keys and the
  label ("Types `2` into the terminal of api-fix: the choice 'Yes, and
  don't ask again …'"; a line's scope says it is typed and entered), and for a plan's approval question
  (`Attention.Plan`) the plan's path. `RoutedTo` is read from the newest
  `routed: …` status entry of the session's activity after the
  question's `since` (`router.go` writes it), never asked of the router.
  `Blocking`: "Claude Code does nothing until it is answered", and when
  the session is an attempt's, its task and the tasks that need it.
- *Task*: `Revision` is the run's revision as read; every command carries
  it as `expectedRevision`, so a run that moved since is refused by the
  engine (`409 stale_revision`), nothing done. Actions from
  `crew.TaskCommands` (Retry; Accept anyway for a work task with a
  submitted result, asked first; Cancel, asked first), each with the
  route the run page uses (`POST /api/runs/{run}/tasks/{task}/retry` and
  the others). `Scope` says which attempt starts at which commit with
  which findings, which result becomes the task's and which tasks start
  from it, or which tasks are blocked by a cancel. `Blocking` lists the
  task's dependents through the crew's needs, transitively, and the run's
  end. `Evidence`: the run, the task, its last attempt, that attempt's
  failed checks (`check:` refs with their exit, where they ran and the
  log's SHA-256, from the ledger's rows), the result commit, the
  blocker's words, and the ticket when the run was launched from one.
- *Plan*: `Revision` is the ticket's `rev`, the expected revision
  Dispatch's approval takes (`ApprovePlan`'s `ticketRev`). Actions:
  Approve (with an optional note, `POST /api/dispatch/plans/{id}/approve`)
  and Withdraw (asked first). `Scope` names the crew, its tasks and
  agents, the base commit and the ticket's revision approved against,
  and says that launching is a separate step on the ticket page.
  `Blocking`: "Nothing runs for CND-12 until a revision is approved and
  launched." Evidence: the ticket and the revision.

**Brakeman's card.** The newest `decision/1` item, among the newest 20
requests with a kept brief (`brakeman.Store.List` and `Brief`, read-only),
whose `about` is the item's ref, gives `Brief` (its `happened`),
`WhyHuman`, `Considered` (its alternatives, text), `Recommended`,
`Uncertainty`, and its evidence refs appended after Conductor's own; its
blocked refs join `Blocking`. Its `authorizes` text is shown beside
Conductor's `Scope`, labelled as Brakeman's, never in its place. A card
read at a revision other than the item's (its `subjectRevision`) is shown
with "Brakeman read this at revision 7; it is at 9 now". With Brakeman
unconfigured or its store read-only, items have Conductor's brief alone.

**`crew.TaskCommands`** (`internal/crew/commands.go`): the commands a
task's state allows, exported from the rules `cancellable`, `retryable`
and `acceptable` already apply, and the run's liveness, so the inbox and
the engine never disagree; `taskActions` in `web/app/utils/tasks.ts`
keeps its own copy for the run page, and a vitest case and a Go test hold
the two to the same table.

**`dispatch.Store.ProposedPlans(ctx, limit)`** (`internal/dispatch/plans.go`):
the proposed revisions of every ticket not deleted, oldest first, at most
`limit` (100), each as `PlanSummary` with its ticket's key and `rev`; one
query over the existing plan rows by state, no schema change.

**`session.Local.QuestionResult(id) (Answer, bool)`** (`internal/session`):
the kept result of a question by id (the existing `results`, the last 64),
read under the session's lock; for a hosted session the server holds no
claim, and the result is the activity's `input` entry that names the
question, which the host reports.

**Routes** (admin only, `requireAdmin`; on a switchyard `403 switchyard`,
"decisions are answered where the work runs, or through a decision
link"):

- `GET /api/inbox`: `{items: [...summaries], more: n, at}`; the open items
  without their detail fields, at most 150 (oldest first, `more` counting
  the rest), each summary at most 4 KiB, so the reply is at most 640 KiB
  (`TestInboxReplyBounded` builds 150 items at their bounds).
- `GET /api/inbox/item?ref=<ref>`: one item with its detail, at most 32
  KiB, `404 not_found` for a ref that names nothing the server holds, and
  for a ref of something no longer open, the item as it is now with its
  delivery (so a page that acted reads what followed).
- `GET /api/inbox/track?ref=<ref>&receipt=<id>`: the four delivery steps
  of one action, from its durable receipt: a command id (the ledger's
  receipt, then the task's state and attempts since), a Dispatch op id
  (the operation's receipt, the revision's state, whether its approval
  still holds), a question id (the session's kept result, then its
  attention since: `working` or a new question is resumed; withdrawn,
  ended, another's answer is the reason). At most 8 KiB.

None of the three routes writes. The page acts through each operation's own route
(the `Route` of the action); the inbox adds no write path (the Program,
section 1.5: one typed operation per change, whoever the client).

**Events.** None new before DR1: the page reads `GET /api/inbox` again on
the `session`, `run`, `dispatch` and `brakeman` events it already
receives (`/api/events`), at most one read in flight and one queued
behind it, so a burst of events costs two reads.

### `web/app`: the page (PR-2)

- `pages/decisions.vue` (route `/decisions`, `?item=<ref>` opens one): the
  open items, then Recent; one column below `sm`, the list and the open
  item side by side at `lg`; every target at least 44 px tall, as the
  bottom bar's are.
- `components/DecisionItem.vue`: the card (title, brief, question,
  choices with their scope, recommendation and uncertainty, blocking, who
  answers and routed to, evidence folded, actions), its actions confirmed
  where the item says so with the same question the run page asks
  (`useTaskCommands.question`).
- `components/DecisionBrief.vue`: the text part of a decision card (what
  happened, why a person, the alternatives considered with Brakeman's
  recommendation, the uncertainty, what answering authorizes), extracted
  from `BrakemanDecision.vue`, which then uses it; Brakeman's controls and
  its file's behaviour are unchanged (`brakeman-cards.spec.ts` passes as
  is). The Program asks that the decision page and Brakeman render the
  same cards (the Program, section 4.3, BK1).
- `components/DecisionDelivery.vue`: the four steps, each with its state,
  time, words and receipt link; a step's state never goes back once
  `done` or `refused` in a page's view (`reconcileDelivery`, below),
  whatever order the reads come back in.
- `components/DecisionEvidence.vue`: the folded evidence; a check's log
  tail (its last 4 KiB, from the step log route the run page reads) loads
  only when unfolded.
- `composables/useInbox.ts`: reads the routes through their client calls
  in `composables/useSessions.ts` (where every route's call is, as the
  repository map says), re-reads on the events above (one in flight, one
  queued), and keeps this browser's recent actions (below).
- `utils/decisions.ts` (pure): the order (oldest first), the words of each
  step, `reconcileDelivery`, the recent list's storage (`conductor.decisions.recent`:
  at most 50 entries of `{ref, receipt, at}`, newest kept, none older
  than 24 hours, every read and write in `try`), the count for the bar.
- **Acting.** A question's choice or line through `useQuickReply` with the
  question's id (first response wins; a refusal's words from
  `replyFailure`); a task's command through `useTaskCommands` with the
  item's `Revision` as `expectedRevision` and a fresh command id; a plan
  revision's Approve and Withdraw through `useDispatchOps` with a fresh op
  id and the ticket's `rev`; Ask Brakeman through `BrakemanAsk.vue` with
  the item's ref as its subject; **Comment on the ticket** for an item
  whose run was launched from a ticket (Dispatch's comment operation).
  Each action's receipt id goes into the recent list before the reply
  comes back, so a reload while it is out still tracks it.
- **Entry points.** `BottomBar.vue`: Decide first (`/decisions`, the open
  count), then Sessions, Yard, Crews, More; Events moves under More
  (Owner decisions, 10). The sidebar's header gains a Decide row with the
  count beside Needs you (`SidebarHeaderMenus.vue`). The palette gains "Go
  to Needs your decision" and one row per open item ("Decide: Claude Code
  in api-fix asks…", `usePaletteRows.ts`). The chord `G` `N` goes there
  (no Alt chord in the terminal: Alt+N launches an agent). A question's
  browser notification, tapped below `lg`, opens `/decisions?item=…`
  (`useAttention.ts`, its `onclick`).
- **Open on your phone.** A button on the page at `lg` and up shows a QR
  code of the page's address as the browser has it (`location.origin` +
  `/decisions`), the URL alone, never the token (Owner decisions, 7). On
  a loopback origin (the desktop app listens on 127.0.0.1) it says that a
  phone cannot reach this address and offers the decision link instead
  (PR-6).

### DR1's records in the inbox (PR-3, after DR1)

Ownership of the shared piece, decided here (Owner decisions, 3): DR1's
plan (`docs/plans/decisions.md`) owns the decision record, its store, its
routes, every response and its delivery to the work, its receipts and its
event; HM1 (the same plan) owns the human labels; this plan owns the
inbox read model, the page, the four-step view of a record's receipts,
the phone's two paths and decision links. PR-3 reads DR1's record through
the functions DR1's PRs give the API, under the names DR1's plan gives
them; the names below are this plan's for what it needs, and PR-3 follows
DR1's where they differ.

What the inbox needs of DR1's record, each a field DR1's requirements
already list (the Program, section 4.3, DR1): its id, its interaction type
(clarification, approval, human assignment), its state (open, resolved,
superseded, cancelled, expired), its refs (ticket, plan revision, run,
task, attempt, session and question), the question and the requesting
actor, the subject revision (a commit, a digest), its options, its
recommendation and uncertainty when the requester gave them, its
evidence refs, its required authority (the operator; HM1's label), its
routing history, its responses (kind, responder, rationale, time, the
response receipt), the execution owner's application receipt (taken, or
refused with its reason), and its resolution.

PR-3:

- **`decision` items** from DR1's open records, and Recent from DR1's
  records resolved in the last 24 hours (at most 50), kept on the
  server, not in the browser. A question DR1 holds a record for is that record's
  item: the derived `question` item for the same session and question id
  is dropped, so the two never both show (`TestADecisionRecordReplacesItsQuestion`).
- **Actions** from the record's type and state: `answer` (an option, or a
  line), `approve` and `reject` (an approval), `more_info` (a question
  back to the requester: the decision stays open), `comment` (a note on
  the record that changes nothing), each through DR1's respond operation
  with a client-minted idempotency key and the record's expected state
  and subject revision, as DR1's route takes them; a duplicate returns
  DR1's receipt. `Scope` for each from the record (what the response
  authorizes, on which subject revision, for which work). If DR1's
  reviewed plan leaves out `more_info` or `comment`, PR-3 adds them to
  DR1's code as two more response kinds (a text of at most 4 KiB, at most
  50 per record, the record's state unchanged, delivered as DR1 delivers
  an answer for `more_info`, delivered nowhere for `comment`), an
  additive change to DR1's schema made by DR1's migration rules
  (conservative: a database without them reads as having none).
- **Delivery** from DR1's receipts: *recorded* is the response receipt;
  *validated* the application receipt (taken, or refused with DR1's
  reason: stale subject, superseded, cancelled, not the authority);
  *resumed* the work DR1 released (the task dispatched again, the
  attempt's session answered and working, the run continued through a
  supported continuation), or the reason it is not yet (no slot, the run
  paused or stopped, the session ended and its continuation not made).
  Where DR1 records no resumption fact, the inbox reads the task's state
  and attempts after the application receipt's time.
- **Brakeman's cards about `decision:<id>`** (Brakeman PR-12) attach to
  the record's item as cards about any other ref do.
- **The event**: the page re-reads on DR1's event. If DR1's PRs add none,
  PR-3 adds `event: decision` with `{id, state}` (at most 128 bytes, a
  `TestDecisionEventBounded`), sent to the clients alone, never to a sink.
- `GET /api/inbox/track` answers a DR1 response id with the record's
  steps.

### `web/app`: DR1's records on the page (PR-4)

- `DecisionItem.vue` renders the `decision` kind: Approve and Reject for
  an approval; Ask for more information and Comment, each a text box of
  at most 4 KiB (the server's bound, counted in UTF-8 bytes as the server
  counts, `utf8Bytes`); the record's notes and responses in order under
  the card.
- The four steps from the server's Recent for DR1 records; the browser's
  recent list keeps only actions on items DR1 does not hold (tasks and
  plan revisions).
- **The ticket page.** `DispatchDecisions.vue`'s `record` rows (DS2's
  reserved kind) and its "To be decided" list link each decision to its
  item on the decision page; its note "Recorded and shown, not held"
  becomes, for a revision whose decisions DR1 holds, "Held: the tasks
  before them wait".

### Decision links and the link's lane (PR-5, PR-6)

The second path: a phone that cannot reach the server, through a
switchyard the person runs (D26, the self-hosted tier). A switchyard
carries signalling and the relayed terminals, mints and checks links and
tells the publisher each viewer's role and link id (`proto.ViewerJoin`);
it keeps no decisions and runs no crews (`docs/architecture.md`,
Switchyard). So the decisions travel over the link's own connection to
its publisher, the server that owns them, the way a terminal does: over
WebRTC end to end, through the switchyard's relay only when ICE fails
(where the relay carries them as it carries terminal bytes). The
switchyard stores nothing of a decision; its code changes in one place,
the relay: the frames a relay envelope may carry (`proto.DecodeRelay`:
output, input, control, scrollback, file, file write) gain
`TypeDecision`, and a switchyard says it relays them in its `registered`
reply (`relaysDecisions: true`). Both sides decode a relay envelope
strictly today, and a server or a switchyard that meets an inner type it
does not know closes the host's connection (`ws_host.go`, "bad relay
envelope"); so a publisher sends decision frames over the relay only to a
switchyard that said `relaysDecisions`, and to an older one over WebRTC
alone, the join page then saying on the relay path that the switchyard
must be updated to carry decisions.

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
with the publication, gone with it as the link is). Nothing about it
reaches the switchyard. A link holder's actions are recorded as the
link's actor, `link:<id>` with its label as the name (the engine
command's `actor`, DR1's responder), never as the workbench (Owner
decisions, 9).

**The protocol** (`internal/proto`, `web/app/utils/protocol.ts`,
`docs/protocol.md` together):

| Message | Direction | Content | Bound | Test |
|---|---|---|---|---|
| frame `TypeDecision` (0x09) | owner → viewer | JSON `{seq, item}`: one inbox item in the link's scope, with its detail and delivery | 32 KiB (`MaxDecision`); fragmented by `TypeChunk` on a data channel as a file frame is | `TestDecisionFrameBounded`, `protocol.test.ts` |
| control `decision_gone` | owner → viewer | `{t, ref}`: the item left the scope (resolved more than a day ago, its session or run gone) | `MaxControl` (8 KiB); `ref` at most 512 bytes | `TestDecisionGoneValid` |
| control `decide` | viewer → owner | `{t, reqId, ref, action, revision, input?, text?, opId}`: one action on an item | `MaxControl`; `reqId` and `opId` at most 32 bytes, `ref` 512, `action` one of the item's action names, `input` at most `MaxAnswerInput`, `text` at most `MaxSubmit` (4 KiB) | `TestDecideValid` |
| control `decide_ok` | owner → viewer | `{t, reqId, receipt}`: the operation's receipt ref | `MaxControl` | `TestDecideOkValid` |
| host message `registered`, field `relaysDecisions` | switchyard → publisher | `true` from a switchyard whose relay carries `TypeDecision`; absent from an older one | a boolean in a message bounded by `MaxHostMessage` (64 KiB) | `TestRegisteredSaysRelaysDecisions` |
| error `decide_refused` | owner → viewer | the error message with `reqId`, a code (`stale_revision`, `not_in_scope`, `not_decision_link`, `question_answered`, `too_many_requests`, `unsupported`) and words | `MaxControl` | `TestDecideRefusedCodes` |

The welcome of a decision link's viewer says `decisions: true`; an older
owner says nothing and sends none, and the page then shows the session as
a join page shows it today.

**The owner's side.** `internal/api/ws_viewer.go` (a link to a server
session, viewed through the server) and `internal/hostagent/peer.go` (a
session the server publishes to a switchyard through its uplink) both
send the frames and take `decide`: the API implements a small interface
`hostagent.Decisions` (`Items(linkID, sessionID) []json.RawMessage`,
`Watch(linkID, sessionID, func())`, `Decide(ctx, linkID, sessionID, m)
(receipt, error)`) set in `hostagent.Options.Decisions` by the uplink,
as its callbacks `FileDeny` and `LinkMinted` are set today; `conductor
host` sets none, sends no frame and refuses `decide` with `unsupported`.
After `welcome`, the owner sends one frame per item in scope, at most 50
(the scope's bound), then a new frame for an item whose JSON changed
(compared by hash per connection) as the inbox's sources change, and
`decision_gone` as an item leaves.

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
DR1's kinds, so the lane takes them with no change of its own). It is
acted on only
while the link stands and covers the item, checked again as the
operation is taken (`attachCheck`, as every link viewer's input is),
and, on a host, while the host vouches for its server
(`AttachOptions.Fresh`, `hostagent/liveness.go`), as a line is. At most
4 `decide` are in flight per connection (`too_many_requests` past it),
the per-connection bound beside the editor's and the reads'.

**`web/app` (PR-6).** `ShareLinksModal.vue` and the run page's links gain
the "Answer decisions from this link" switch (control role only), and the
link's row says so. The join page (`pages/join/[token].vue`) of a
decision link opens on a **Decide** tab with the same `DecisionItem`
cards, fed by the frames (`utils/transport/base.ts` takes `TypeDecision`
and the three controls), acting through `decide`, with the terminal one
tab away; a run link's join page shows the run's items above its members'
grid. The decision page's "Open on your phone" offers, on a loopback
origin, a decision link for a run or a session the person picks.

### Docs

Each PR adds its own rows to `docs/protocol.md` (PR-1 the inbox routes,
PR-3 DR1's items and event if added, PR-5 the frames and messages). PR-5
adds an "Answering from a phone" section to `docs/architecture.md`: the
two paths, what the switchyard sees, the decision link's scope. PR-7
writes `docs/features.md`'s entry, the README's paragraph and the
repository map's rows (the inbox in `internal/api`, the page).

## Events, orderings and invariants

**The inbox is a read model.** Before DR1 it keeps nothing: each read
derives the items from the sessions, the engine, Dispatch and Brakeman's
store at that moment. An item is open exactly when its source says so at
read time (the table under "Item identity"); a page that read an item and
acts on it carries the item's `Revision`, and the operation, not the page,
judges whether that is still the state: the session refuses an answer
naming a question not shown (`question_answered`), the engine a command
at an old revision (`stale_revision`), Dispatch an approval at an old
ticket revision, DR1 a response at an old subject revision. The page
then reads the item again and says what changed ("The run moved on: build
is succeeded now. Nothing was done."). So no check is made on the page and
used later.

**First response wins, unchanged.** Every answer from the page, from a
decision link, from a router and from a person at the terminal goes
through the question's claim; the page's answer is one more reply. DR1
records which response was first.

**The four steps.** For one action they are computed, in order, from the
durable record of that action where one exists:

| Item | Available | Recorded | Validated | Resumed |
|---|---|---|---|---|
| question, before DR1 | the attention showed the id | the session's kept result (by, when): in memory while the session is listed | the same claim (the session is the owner: one step, said so) | attention `working`, or a next question; else the reason (withdrawn, another's answer, ended) |
| task | the task's state allowed the command | the command's receipt in the ledger | the receipt's status (`done`, or the refusal) | retry: the next attempt running; accept: the dependents dispatched; cancel: does not apply (its dependents are blocked, and the step says so) |
| plan | proposed | the Dispatch operation's receipt | the approval holds against the ticket's revision (`Stale` empty) | does not apply: "a launch from the ticket page starts it" |
| decision (DR1) | the record open | DR1's response receipt | DR1's application receipt | DR1's release of the work, or the inbox's read of the task after it |

A step is never shown going back: once `done` or `refused` in a page's
view it stays (`reconcileDelivery`), and the server computes each step
from records that only grow (receipts are kept, states move forward), so
two reads give the same steps or later ones. A `refused` validation ends
the row: resumed is `not_applicable` with the refusal's words.

**Recent.** Before DR1, the browser that acted keeps `{ref, receipt, at}`,
written before the action's reply, and reads each entry's steps from
`/api/inbox/track`, so another browser does not see this one's recent
actions (said on the page: "Acted on from this browser"). From PR-3 the
server lists DR1's resolved records for every viewer.

**The lane.** On a decision link's connection: `welcome` first; then the
scope's frames, in the inbox's order; then frames and `decision_gone` in
the order the owner sees the changes. A `decide` is answered by exactly
one `decide_ok` or `decide_refused` naming its `reqId`; a `decide` whose
operation began when the connection closed is not taken back (its
receipt stands, read again by its id); one not begun is never begun. A
link revoked or expired ends the viewer's connection as today
(`link_gone`, `OnRevoke`), and a `decide` checked after the revocation is
refused, so nothing of a link's acts lands after its end.

**State and migration.** No database or file schema changes in PR-1,
PR-2, PR-5 or PR-6: the inbox stores nothing, `share.Link.Decide` and
`remoteLink.decide` are in memory as the links are, and the browser's
recent list is a per-browser convenience that may be empty at any read.
PR-3 changes DR1's schema only if it adds the two response kinds, then by
DR1's own migration, additive and conservative.

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

**PR-1** (Go race; `internal/api`, `internal/crew`, `internal/dispatch`,
`internal/session`):

- `TestInboxListsAQuestionAsTheSessionShowsIt`: the stub's question with
  three choices is an item with three `answer` actions whose inputs are
  the options' and whose scope names each key.
- `TestInboxLeavesOutAQuestionAnsweredInTheTerminal` and
  `…ASessionNoLongerListed`.
- `TestInboxTaskItemsFollowTheEngine`: for every task state of the crew
  fixtures, the item's actions equal what the engine then accepts, each
  command sent at the item's revision succeeding and each other refused
  (the table `crew.TaskCommands` shares).
- `TestTaskCommandsMatchTheRunPage` (with `tasks.test.ts`): the same table
  of states and actions in Go and TypeScript.
- `TestInboxBlockingNamesTheDependents`: a diamond of four tasks; the
  failed root's item names three dependents; past 20, `more`.
- `TestInboxPlanItemsAreProposedRevisions`, and `TestProposedPlans` in
  `internal/dispatch` (deleted tickets left out, oldest first, the limit).
- `TestInboxCarriesTheNewestBrakemanCard`: two briefs about one task; the
  newer card's fields, its `authorizes` beside Conductor's scope, and a
  card read at another revision says so.
- `TestInboxRoutedToFromTheActivity`: a test router routes a question to
  a person; the item names it; with the empty route, nothing.
- `TestInboxReplyBounded` and `TestInboxItemBounded`: every text at its
  bound, 150 items, the reply at most 640 KiB, an item at most 32 KiB.
- `TestInboxTrackCommand`: retry at the item's revision: recorded and
  validated done, resumed waiting until the next attempt runs, then done;
  at an old revision: validated refused with `stale_revision`, resumed
  not applicable.
- `TestInboxTrackQuestion`: answered through the claim: recorded and
  validated done at the answer's time, resumed done once the attention is
  `working`; withdrawn by the screen: the reason.
- `TestInboxTrackPlanApproval`.
- `TestInboxIsAdminOnly`, `TestInboxOnASwitchyard` (`403 switchyard`).
- `TestQuestionResult` in `internal/session`: the kept result by id, the
  65th forgotten.

**PR-2** (vitest, Playwright):

- `utils/decisions.test.ts`: the order; each step's words; `reconcileDelivery`
  never moving a step back over replies in either order; the recent list
  (50 kept, older than a day dropped, storage that throws read as empty).
- `composables/useInbox.test.ts`: one read in flight and one queued over a
  burst of 20 events.
- `web/e2e/decisions.spec.ts`, on a phone viewport (390 × 844, touch, as
  `phone.spec.ts`), after `goto` waiting for the bottom bar's root before
  any tap: the stub's question answered from the Decide tab, its four
  steps shown (recorded and validated "by the session", resumed once the
  stub works again); a managed task failing its check, retried from its
  card with the receipt's steps; a retry at an old revision refused with
  the words and nothing done (the revision moved through a seam: a
  second retry from the API first, never a sleep); a plan revision
  approved; the evidence unfolded and the session's terminal opened from
  it; the item counted by its ref, never by the list's length
  (`stopStrays` first). Each step's wait is budgeted with
  `testInfo.setTimeout` as `palette-rows.spec.ts` does.
- `brakeman-cards.spec.ts` unchanged and passing (the extracted
  `DecisionBrief.vue`).

**PR-3** (Go race; `internal/api`, and DR1's package if it adds the two
kinds):

- `TestADecisionRecordReplacesItsQuestion`.
- `TestInboxDeliveryFromDecisionReceipts`: recorded, validated, resumed;
  validated refused for a stale subject revision with resumed not
  applicable; a duplicate response answered with the first receipt.
- `TestADecisionSurvivesARestart`: the record open across a server's
  restart over the same data directory, its item the same; after a
  response, its steps the same after the restart (servers ended later
  first, as AGENTS.md's rule for one data directory says).
- `TestMoreInfoKeepsTheDecisionOpen`, `TestACommentChangesNothing` (when
  PR-3 adds them; otherwise DR1's tests cover them and these assert the
  inbox's actions).
- `TestDecisionEventBounded` (when PR-3 adds the event).
- `TestALinkAnswersADecisionRecord`, in whichever of PR-3 and PR-5 merges
  second: a decision link's `decide` on a DR1 record through the action's
  `do`, its responder the link.

**PR-4** (vitest, Playwright): `utils/decisions.test.ts` cases for the
decision kind; `web/e2e/decisions-durable.spec.ts` on the phone viewport:
a gate a plan requires (DR1) answered from the Decide tab, the server
restarted (a server of the spec's own over its data directory, stopped and
started again as `brakeman.spec.ts` does), the item's steps the same
after; Ask for more information keeps it open and names the note; a
response at a stale subject refused with words; the ticket page's
Decisions linking to the item.

**PR-5** (Go race; `internal/proto`, `internal/share`, `internal/api`,
`internal/hostagent`):

- `TestDecisionFrameBounded`, `TestDecideValid`, `TestDecideOkValid`,
  `TestDecisionGoneValid`, `TestDecideRefusedCodes`.
- `TestADecisionLinkNeedsControl` (`share`, `api`).
- `TestADecisionLinkReceivesItsScope`: a run link sees the run's items and
  no other run's; a session link its session's question and task.
- `TestAControlLinkWithoutDecideReceivesNone` and is refused
  `not_decision_link`.
- `TestDecideRetryRecordsTheLinkAsActor`: the receipt's actor is
  `link:<id>`.
- `TestDecideAfterRevokeIsRefused`: a `decide` held at a gate in the
  owner until the link is revoked, then let go: refused, nothing done
  (the gate is a seam of the test, the order fixed, no sleep).
- `TestDecideInFlightBound`: the fifth refused.
- `TestPeerSendsDecisionFrames` and `TestPeerRefusesDecideWithoutDecisions`
  in `internal/hostagent`, over its relay as `peer_test.go` drives it.
- `TestRelayCarriesDecisionFrames` (`internal/proto`, `internal/api`): a
  relayed `TypeDecision` reaches the viewer and the host's connection
  stays; `TestNoDecisionFramesOverAnOlderRelay`: to a switchyard whose
  `registered` says nothing of it, the publisher sends none over the
  relay, and its connection stays; `TestRegisteredSaysRelaysDecisions`.
- The lane's tests `-count=30` before the PR is ready (AGENTS.md, Tests
  that do not flake).

**PR-6** (vitest, Playwright): `utils/protocol.test.ts` and
`transport` cases for the frame and the controls; `web/e2e/switchyard.spec.ts`
gains a decision link test: the second server of the suite's shape that
already publishes to the spec's switchyard mints a decision link for a
managed run whose task fails; a phone context opens the link at the
switchyard, sees the item, retries it, and its steps reach resumed; the
run's member's question answered the same way; over the switchyard's
relay, which the spec forces as it does for its host (a CI box may have
no route for ICE).

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
   viewer connection and the host's peer, and the Runtime's wiring is not
   changed.

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
common file under `docs/`. Sizes are the first push's changed lines with
tests; DS2's and Brakeman's PRs grew past their estimates in review, so
none here is planned above about 1,500.

| PR | Title | Touches | Depends on | Parallel with | Size |
|---|---|---|---|---|---|
| PR-1 | api: the decision inbox on what main holds (questions, tasks, plan revisions, Brakeman's cards) and the four steps of an action | `internal/api`, `internal/crew`, `internal/dispatch`, `internal/session`, `docs/protocol.md` | none | none | ~1,400 |
| PR-2 | web: Needs your decision, the phone's Decide tab, the palette, the shared card text | `web/app`, `web/e2e` | PR-1 | PR-3 | ~1,400 |
| PR-3 | api: DR1's records as items, their responses, notes and receipts | `internal/api`, DR1's package (if it adds the two kinds), `docs/protocol.md` | PR-1, DR1's PRs that serve the record, its respond operation and its receipts (`docs/plans/decisions.md`) | PR-2 | ~1,000 |
| PR-4 | web: DR1's records on the page and the ticket page's decisions | `web/app`, `web/e2e` | PR-2, PR-3 | none | ~900 |
| PR-5 | proto, share, api, hostagent: decision links and the link's lane | `internal/proto`, `internal/share`, `internal/api`, `internal/hostagent`, `web/app/utils/protocol.ts`, `docs/protocol.md`, `docs/architecture.md` | PR-1 (after PR-3 when both are ready: one `internal/api` change at a time) | none | ~1,400 |
| PR-6 | web: a decision link's join page, the share switch, through a switchyard | `web/app`, `web/e2e` | PR-2, PR-5 | none | ~1,000 |
| PR-7 | the acceptance check, what it finds fixed, the features entry | `docs`, `README.md`, and the packages a found bug lives in | PR-4, PR-6 | none | ~300 and what the check finds |

**PR-1** (`internal/api`, `internal/crew`, `internal/dispatch`,
`internal/session`): `inbox.go` whole (the types and their bounds, the
three kinds, Brakeman's card, the routed-to read, the blocking walk, the
scope words), the three routes and `inbox_test.go`; `crew.TaskCommands`;
`dispatch.Store.ProposedPlans`; `session.Local.QuestionResult`; the
routes' rows in `docs/protocol.md` (HTTP API). Tests: PR-1's above.

**PR-2** (`web/app`, `web/e2e`): the page, its five components and the
composable, `utils/decisions.ts`, `DecisionBrief.vue` extracted from
`BrakemanDecision.vue`, the bottom bar, the sidebar's row, the palette's
rows and the `G` `N` chord (and its line in the `?` list), the
notification's target below `lg`, Open on your phone (the URL's QR code);
`decisions.spec.ts`; `phone.spec.ts`'s bar test updated for Decide first
and Events under More. Tests: PR-2's above.

**PR-3** (`internal/api`, and DR1's package only for the two response
kinds if DR1's plan leaves them out): the `decision` kind, the dedupe
against questions, the actions through DR1's respond operation, the steps
from DR1's receipts, Recent on the server, cards about `decision:` refs,
the event if DR1 has none, the protocol rows. Waits for DR1's PRs that
serve the record; until they merge it is not cut. Tests: PR-3's above.

**PR-4** (`web/app`, `web/e2e`): the decision kind on the card, Approve
and Reject, Ask for more information and Comment, the server's Recent for
DR1's records, `DispatchDecisions.vue`'s links and words;
`decisions-durable.spec.ts`. Tests: PR-4's above.

**PR-5** (`internal/proto`, `internal/share`, `internal/api`,
`internal/hostagent`, `web/app/utils/protocol.ts`, `docs`): `Decide` on a
link and on a remote link, the link routes' `decide` field, the frame,
the four messages and `registered`'s `relaysDecisions` in all three
protocol places, the relay's list of the frames it carries,
`hostagent.Decisions` and
the uplink's wiring, `ws_viewer.go`'s and `peer.go`'s handling, the scope
and the in-flight bound, the architecture section. Tests: PR-5's above,
the lane's `-count=30`.

**PR-6** (`web/app`, `web/e2e`): the transport's handling of the frame
and the controls, the join page's Decide tab, the run link's items, the
share switch on the session's and the run's links, the loopback origin's
offer of a decision link; the switchyard spec's decision link test.
Tests: PR-6's above.

**PR-7** (`docs`, `README.md`, and any package a bug the check finds is
in): the by-hand acceptance check (below), every bug of the stream it
finds fixed here (the stream's rule, AGENTS.md), `docs/features.md`'s
entry (what each PR shipped and verified, and the check), the README's
paragraph, the repository map's rows.

## Owner decisions

Dated 2026-11-05. Each is a default the owner can overturn with a dated
row in the Program's status log, its convention for a decision the owner
did not make (the Program, section 3.1); the reasoning is why it was
taken.

1. **O4 and O10 are not this item's.** O4 (human policy v1) blocks SW1 and
   O10 (Dispatch's conflict rules) blocks CS1; the page has one answerer,
   the operator (D4), and replicates nothing, so it settles neither and
   waits on neither. No open decision of the Program blocks MB0.
2. **The page is built on what `main` has first.** PR-1 and PR-2 serve
   questions, tasks, plan revisions and Brakeman's cards today; DR1's
   records join from PR-3. The page and the phone's path are useful
   before durable decisions exist, and DR1 changes only where an item
   comes from, not how it looks.
3. **DR1 owns the record, MB0 the inbox.** DR1 (`docs/plans/decisions.md`)
   owns the record, its store, its routes, every response kind and its
   delivery, its receipts and its event; HM1 the labels; this plan the
   read model, the page, the four steps' view, the two phone paths and
   decision links. Asking for more information and commenting are
   response kinds of DR1's record; if DR1's reviewed plan leaves them
   out, this plan's PR-3 adds them to DR1's code. One owner per record
   keeps "applies once" in one place. Brakeman's plan owns the
   `decision/1` card; the page shows a card's text and never its
   alternatives as controls, as Brakeman's own page does.
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
   connection.** The switchyard stores nothing of a decision and only
   relays one more frame type when ICE fails; the link's scope is its
   session or its run. A plan revision's approval before any run has no
   link to ride and is answered on the LAN or the desktop. Chosen over a
   new lane at the switchyard, which would make the switchyard hold or
   relay decision content outside a link and change what it is trusted
   with.
9. **A decision link is a control link the operator chose to make one,
   and its acts are the link's.** Minted only by the workbench, control
   role only, shown as such in the links list; a command or a response
   sent through it records the actor `link:<id>` with the link's label,
   never the workbench. The operator stays the single approver (D4) and
   the receipts say which credential acted.
10. **Decide is the phone's first tab.** The bar keeps four tabs and More
    (Decide, Sessions, Yard, Crews); Events moves under More. A tapped
    question notification opens the decision item below `lg`. The
    Program makes the decision view the primary mobile view (the
    Program, section 4.3, MB0); the sessions list stays the phone's home
    so `/` does not change under people.
11. **Oldest first.** Items are listed in the order they were asked: no
    timer ends a question, and the oldest has waited longest.
12. **Before DR1, a browser keeps its own recent actions.** At most 50,
    a day at most, read back from the durable receipts (the ledger's,
    Dispatch's) and, for a question, the session's memory, which the
    page says. From PR-3 the server lists DR1's resolved records for
    everyone.
13. **A task is an item when it failed or reports a blocker.** A task
    blocked only by a failed input is named in that input's item, so one
    failure is one item.
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
- **Through a self-hosted switchyard**, once PR-6 is merged: a
  `conductor switchyard` the owner runs (on the LAN Linux box, or a small
  host of their own), the owner's server publishing to it (the desktop
  app on Windows publishes outward, so no networking is configured), a
  decision link for the run minted and opened on the phone through the
  switchyard (on the LAN, or from mobile data where the switchyard's
  machine is reachable from the internet): the run's items there, one answered, its steps through
  to resumed; the link revoked on the desktop and the phone's next action
  refused.
- What was missing, slow or wrong goes into `docs/features.md` (PR-7); a
  bug the stream caused is fixed in the stream before it is called done,
  a defect outside it filed.
- Done means every PR merged and both observations recorded with the
  owner's word that the phone answered what needed deciding and said
  whether it was recorded or applied; the Program's status row moves to
  Done then. The R1 release gate (the Program, section 6) needs this
  item's decision answered from a phone as one of its steps.

What needs the owner: the review of this plan and its decisions; cutting
the seven issues; DR1's plan and PRs, which PR-3 waits for; the phone and
the agents' subscriptions for the observation; the merges.
