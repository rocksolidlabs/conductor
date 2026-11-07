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
  run's link works with no member alive (the last attempt failed; a gate
  before the first attempt once DR1's records are items, PR-3, and DR1
  accepts a decision link's actor). The switchyard stores nothing of
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

**Bounds.** A field is one of two kinds, bounded two ways.

- An **exact** field is a value an operation takes back or a person must
  read whole: a choice's `Input` (the keys `AnswerAs` checks against the
  question's choices byte for byte), the item's `Question`, a ref, a
  subject, an action's expected revision, a receipt. It is never cut or
  changed. It carries its source's raw bound, which the source already
  enforces (`MaxOptionInput`, 48 raw bytes, in `CleanOptions`;
  `MaxAttentionMessage`, 500 raw bytes, for the question; `MaxRef`, 512,
  for a ref), and its encoded budget is that bound's worst case: six
  bytes for each raw byte (`encoding/json` writes a control character, a
  byte of invalid UTF-8 and, with HTML escaping, `<`, `>` and `&` as six;
  U+2028 and U+2029, three bytes, as six), so an input is budgeted at
  288 encoded bytes and the question at 3,000. The item's own `Ref` and
  `Subject` are of kinds whose grammar escapes nothing (`question`,
  `task`, `plan`, `ticket`, `decision`: ids, keys and digits); an
  evidence or blocking ref whose encoded form passes 512 bytes (a
  `file:` path or an `op:` actor with escaping characters) is left out of
  its list and counted in its `more`, never cut.
- A **display** field (a title, a brief, a scope, a step's words, a
  label) is bounded on its JSON-encoded bytes, escaping included: it is
  cut on a rune boundary, with "…", until its encoded form fits.

Each object's encoded size is checked as built against the sum of these
budgets. `TestInboxItemBounded` fills every field at its bound with
characters that escape to six bytes; `TestInboxExactFieldsRoundTrip`
encodes and decodes an item whose choices' inputs are 47 `<` and a CR
(48 raw bytes, 284 encoded) and 48 bytes of control characters, and whose
question is 500 bytes of `<`, `&` and quotes, and asserts each decoded
value equal to the session's byte for byte and the decoded input taken by
`AnswerAs` as the choice.

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
	Question   string             `json:"question"`   // exact: 500 raw bytes, never cut (3,000 encoded at worst)
	Answerer   string             `json:"answerer"`   // 120
	RoutedTo   []inboxDestination `json:"routedTo,omitempty"`  // at most 8
	RoutedRule string             `json:"routedRule,omitempty"` // 200 (ext.MaxRouteRule)
	Revision   string             `json:"revision"`   // 64: the item's subject's revision as read, shown; each action carries its own (Expect)
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
	Input   string `json:"input,omitempty"` // exact: a question's choice, its keys (at most proto.MaxAnswerInput, 48 raw bytes; 288 encoded at worst), never cut
	Text    bool   `json:"text,omitempty"`  // a line is typed (a free-text question)
	Expect  string `json:"expect"`  // 64, exact: the revision this action's own operation checks: the question's id for an answer, the run's revision for a task command, the plan revision's rev for Approve and Withdraw
	TicketRev int64 `json:"ticketRev,omitempty"` // Approve only: the ticket's rev, which ApprovePlan checks beside Expect
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

The sum, encoded, at every bound: the item's texts and refs about 8.5
KiB (the question's 3,000 bytes at worst among them), `RoutedTo` 2 KiB,
blocking 11 KiB, actions 10 × 1.75 KiB = 17.5 KiB (each with its input's
288 bytes and its `Expect`), the card's fields 3 KiB and `Considered` 6 ×
(a label cut to 320 bytes and 1 KiB of tradeoffs) = 8.5 KiB, evidence
20 × 0.8 KiB = 16 KiB, delivery 4.5 KiB, keys and quotes about 4 KiB:
about 76 KiB, under the item's 96 KiB. The summary at every bound is under 3 KiB, under its
4 KiB. `TestInboxSummaryBounded` and `TestInboxItemBounded` build each at
every field's bound with escaping characters and assert both.

**What each kind carries.**

Every action carries its own `Expect`, the revision its own operation
checks, and the page and the lane send the action's, never the item's
`Revision`: one item can hold actions of two operations (a blocker's
question carries the task's Cancel), and each is guarded by its own.

- *Question*: `Revision` is the question's id, and so is each `answer`
  action's `Expect`, which an answer names (`proto.Answer.Question`), so
  an answer to a question gone or replaced is refused by the session,
  never typed into the next. Actions: each of
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
  commands (Item identity), each with the run's revision as read as its
  `Expect`, not the question's id, so a Cancel after the run moved is
  refused `stale_revision` while the same question still shows.
  **A turn held for the terminal** (attention `needs_input` whose source
  is `submit`: a submission's text typed and its Enter not confirmed,
  the hold of `submit.go`) has an id and no kind, but it is no question:
  the session refuses every reply naming it (`waitsLocked`, `local.go`),
  as the page's `waitsForTerminal` (`utils/attention.ts`) already tells
  the sessions list. Its item offers no `answer` action, neither a choice
  nor a line; its brief and its one scope say "Claude Code's turn is
  held for keys typed in its terminal: open the terminal to go on", and
  the session's terminal is the first entry of its evidence (on a link,
  the member's terminal tab through `decision_evidence` when the session
  is in the link's scope; outside it, the words alone).
- *Task*: `Revision` is the run's revision as read, and each command's
  `Expect`, which it carries as `expectedRevision`, so a run that moved
  since is refused by the engine (`409 stale_revision`), nothing done.
  Actions from
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
  `rev`, each plan action's `Expect`, which every plan operation carries
  as its expected revision (`tx.plan` checks it, and it is all
  `WithdrawPlan` checks), and the Approve action's `TicketRev` the
  ticket's `rev`, which `ApprovePlan` checks besides (`ticketRev`).
  Approve sends both, Withdraw sends its `Expect`
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
engine in PR-1, and by a vitest case against `taskActions` in PR-2, the
PR that brings `taskActions` to it: the page today differs from the
engine (Accept anyway only for `kind === 'work'` and only from the
latest attempt's result, `web/app/utils/tasks.ts`, where `acceptTask`
searches every kept attempt since a withdrawal and asks no kind), so the
vitest case and the correction land together, with the run page's
checks, and PR-1 changes no file under `web/` and passes its own Go
checks alone.

**`dispatch.Store.OpenPlans(ctx, after, limit) (page, next, total)`**
(`internal/dispatch/plans.go`): the revisions of every ticket not
deleted that need the operator, a page at a time: the proposed ones, and
each ticket's approved revision whose approval no longer holds
(`planStale` set, read as the ticket's `GET` reads it), each as
`PlanSummary` with its ticket's key and `rev`. They are ordered by the
inbox's own key, `(since, ref)`, a plan item's `since` being its
revision's proposed time, so `after` is the inbox's cursor itself; the
page holds at most `limit` after it, `next` is set when more follow, and
`total` counts every open revision, not the page's: the proposed rows by
a count, and the approved ones (at most `MaxTicketApproved` read per
ticket, as the ticket's read does) each judged by `planStale`. Dispatch's
`MaxPlans` bounds a ticket's revisions, not the store's, so nothing caps
the open revisions in all, and none is left out of a page or the count.
Queries over the existing plan rows by state, no schema change. The
inbox builds a page by asking each source for its items after the
cursor (at most a page and one each), merging them by `(since, ref)` and
keeping the first 150, and adds the sources' totals for `total`.

**`session.Local.QuestionResult(id) (Answer, bool)`** (`internal/session`):
the kept result of a question by id (the existing `results`, the last
64), read under the session's lock. The kept `Answer` gains `ReqID`, the
winning answer's request id (`proto.Answer.ReqID`, which the page mints
fresh per action), so a browser's own attempt and the winner are told
apart by id (Events, Recent). The kept result gains its **resumption**
too, `Resumed {at, how}`, set once, under the session's lock, by the
first attention change after the answer was taken: the agent `working`
(`how: working`), a next question (`asked`), the session's end
(`ended`). So the resumed step reads what happened when it happened,
never the attention at the read: a question answered, worked on and its
turn done before the first track read (or a reload) still reads resumed
done at the time the work began, the same from every browser. The
result is in memory: a session gone or past 64 later questions forgets
it, and the step then says `unknown`, never a guess. **A hosted session's question has no result on the
server**: the host's claim takes the answer and the server keeps no
claim; the activity's `input` entry is not a receipt of it (an
`ActivityEntry` has no question field, `Local.resolve` records only the
question's words, identical prompts repeat, and the host's activity
forwarder drops entries when its queue is full). So for a hosted
question the server reports *recorded* and *validated* as `unknown`
("the host holds this answer; this server keeps no record of it"), the
page shows its own reply's word there (the host answered the quick
reply's request, accepted or refused, `useQuickReply`), and *resumed*
reads the same milestone, kept by the server from the hosted session's
attention as its host syncs it: for its last 64 questions by id, the
first change the server saw after the question left `needs_input`
(`working`, a next question, the end), in memory. A question past them,
or one whose leaving the server did not see (the server restarted, the
host's updates lost), reads resumed `unknown`, "it is not known here
whether the work went on", never inferred from the attention now.

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
  (the ledger's receipt of the admin's actor, by its status, `accepted`,
  `done`, `failed` or `uncertain` (The four steps, below), then the
  task's state and attempts since), a Dispatch op id (the operation's receipt; its
  validation is the operation's own outcome, and the approval's current
  validity is the item's `Now`, apart from the steps), a question id with
  the browser's attempt (the session's kept result: the attempt's
  `ReqID` the winner's or another's; then its kept resumption, never its
  attention now: `working` or a next question is resumed, the end is the
  reason, none yet is `waiting`; withdrawn or another's answer is the
  reason). At most 8 KiB.

None of the three routes writes. The page acts through each operation's
own route (the `Route` of the action); the inbox adds no write path (the
Program, section 1.5: one typed operation per change, whoever the
client). These routes read what no DR1 record holds (a session's
question, a task's commands, a plan revision, Brakeman's cards), which
is why the plan adds them beside DR1's: for a DR1 record the page reads
DR1's routes (PR-3).

**Events.** None new before DR1: the page reads `GET /api/inbox` again on
the `session`, `removed` (a session gone, `eventHub.removed`), `run`,
`dispatch` and `brakeman` events it already receives (`/api/events`),
and on the stream's `snapshot`, which it sends on every (re)connect: the
stream replays nothing it missed while away, so a phone back from
suspension reads the whole inbox then, as `useAttention.ts` reads the
runs, Dispatch's pages and Brakeman's on a snapshot. While the stream is
down, the inbox is read again on the cadence `useAttention.ts` reads the
sessions at then, and once when the page becomes visible again
(`visibilitychange`). At most one read in flight and one queued behind
it, so a burst of events costs two reads. Each read of the list also
reads again the track of every recent action whose steps are not final
(below), so a step that moves with no change to the open list (a retried
task's next attempt starting) is seen.

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
  repository map says), re-reads on the events above, the snapshot and
  the page's return to view (one in flight, one queued), with the tracks
  of the recent actions not yet final, and keeps this browser's recent
  actions (below). An action's steps are final when its resumed step is
  `done` or `not_applicable`, or its validated step `refused`; a command
  whose receipt is `accepted` is never final (its settlement, or the
  recovery's `uncertain`, follows), and an `uncertain` one is final once
  its resumed step is read from the task, since the ledger never changes
  it again.
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
  `useTaskCommands` with the action's `Expect` as `expectedRevision` and a
  fresh command id; a plan revision's Approve through `useDispatchOps`
  with a fresh op id, the action's `Expect` as the op's expected revision
  and its `TicketRev` as `ticketRev`, and Withdraw with its `Expect` alone;
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
gives it. For the list, DR1's `GET /api/decisions` and
`Ledger.Decisions` page newest first by the record's number with no
count, which the inbox's merge cannot use: it takes each source's oldest
items after the `(since, ref)` cursor, a page and one, and a complete
total, and with more than 100 open records the oldest are not on a
newest-first first page. So the inbox reads its own query,
`ledger.Ledger.InboxDecisions(ctx, states, after, limit) (page, next,
total)`: DR1's records in the given states (`open`, `answered`), oldest
first by `(created_at, id)`, which is the inbox's key for a `decision`
item (its `since` the record's `created_at`, its ref `decision:` and the
id, so the ids' order is the refs'), at most `limit` after the cursor,
`next` when more follow and `total` a count of them all. It is a
read-only query over DR1's table by state (DR1's index on state serves
it), no schema change, beside DR1's own; PR-3 adds it in
`internal/crew/ledger` unless DR1's plan takes it in review, and then
PR-3 calls DR1's. For one record, `GET /api/decisions/{did}`'s read: its id and type, its state (`open`,
`answered`, `applied`, `superseded`, `cancelled`, `expired`), its `rev`,
its run, task, attempt, session and question, its subject, its options,
recommendation, uncertainty, what it authorizes, its evidence, its
policy (`local/1`: the operator answers), its routing history, its
responses with their receipts, its application receipt (`applied`) and
its history.

PR-3:

- **`decision` items** from DR1's open records. DR1's **answered**
  records, whose response is recorded and not yet applied (DR1 sets
  `answered_at` at the response and `closed_at` only with a final state,
  so an answer to an interrupted run stays `answered` until its
  continuation applies it), are listed with them as tracking items under
  "Answered, not yet applied", whatever their age, until DR1 gives them a
  final state: the summary gains `tracking` (true for these), they are
  paged and counted in `total` with the open ones and left out of the
  bar's number. Recent holds DR1's records closed in the last 24 hours
  (at most 50), kept on the server, not in the browser. So no record is
  in neither list. A question DR1 holds a record for is that record's
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
  in DR1's plan), read from the record, never recomputed. **The inbox
  does not carry the record**: DR1's `GET /api/decisions/{did}` is at
  most 1 MiB (its request, evidence baseline, 32 receipts, 256 history
  rows and ref resolutions, 902 KiB at their bounds), past the item's
  96 KiB and the lane's 128 KiB frame. A `decision` item carries the
  inbox's own fields (its summary, brief, blocking, evidence refs, the
  actions with their `Expect`, the record's `rev`) and its `delivery`
  as DR1's four states, each step's words and receipt ref within the
  step's own bounds, so the item stays at most 96 KiB. On the workbench
  the page reads the record itself from DR1's route, as DR1's card does
  everywhere, and draws `DecisionCard.vue` from it. On a link, the record
  is read through the lane in parts, `decision_record` (PR-5 defines it;
  the owner answers `unsupported` until this PR serves it): `head` (the
  record without its receipts and history: the request, its evidence
  baseline, the state, `rev`, the policy, the subject and the
  application receipt, at most 36 KiB, with its 20 ref resolutions, at
  most 44 KiB with their keys, read with it: at most 80 KiB),
  `responses` (8 receipts a frame, newest first, at most 64 KiB, with
  `next`) and `history` (32 rows a frame, newest first, at most 72 KiB,
  with `next`), each a `TypeDecision` frame under its 128 KiB. **One
  record, one revision.** DR1 bumps a record's `rev` with every change
  and writes one history row with it (DR1's plan: every change bumps
  `rev`), so `rev` marks the record whole. Every part is cut from one
  whole read of the record (DR1's `Ledger.Decision`, the record with its
  responses and history in one read, and its ref resolutions made in the
  same request, as the route makes them) and carries the `rev` it was
  cut from; every request after `head` names the `rev` its `head` had,
  and a part whose read finds another `rev` is not served: the reply is
  `decide_refused` `record_moved` with the new `rev`, and the join page
  drops what it assembled and starts again from `head`. The ref
  resolutions ride in `head`, so they are of the same moment as the
  record's state, as the route's are. The page hands DR1's card only a
  record whose every part carried one `rev`, so DR1's components see a
  record the route could have served at that `rev`, never an open head
  beside an applied history, at most its 1 MiB in the page. A record
  that changes faster than it can be read is bounded: after 4 restarts
  in a row the page shows the last record it assembled whole (or the
  item's own fields when there is none) with "this decision changed
  while it was read; reading again on its next change", and reads again
  at the next `decisions_changed`.
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
- **DR1's card on a link.** `DecisionCard.vue` draws its evidence as
  `BrakemanRefChip.vue` chips, which compute workbench routes
  (`refRoute`) and embed `BrakemanReceipt.vue`, which reads its receipt
  over HTTP (`useApi`); a link holder has neither. So the chip and the
  receipt take their navigation and their reads from an injected
  `refAccess` (`{route(ref), read(ref)}`, Vue's `provide` and `inject`),
  whose default is today's (`refRoute`, `useApi`), so every page that
  shows them now is unchanged and DR1's `DecisionCard.vue` is not edited.
  The join page provides the link's: `route` its route function (a
  member's terminal tab for a session in scope, none otherwise, with
  "open it on the desktop or the LAN"), `read` through
  `decision_evidence` by the chip's ref (a check's log tail; a receipt
  the lane does not serve shown as its label and ref). It is a change to
  Brakeman's two components with their defaults kept;
  `brakeman-cards.spec.ts` and DR1's specs pass as they are.
- Recent for DR1 records from the server's Recent; the browser's recent
  list keeps only actions on items DR1 does not hold (questions, tasks,
  plan revisions). A DR1 record answered and not yet applied stays on the
  page under "Answered, not yet applied", from the server's list (PR-3),
  never dropped with a browser's entry.
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
lane's frames (`proto.DecodeRelay`'s list of inner types: output, input,
control, scrollback, file, file write, and now `TypeDecision` and
`TypeDecide`); its
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
`t` is not `register`). It says so only in a build whose join page has
the Decide tab: PR-6 builds the switchyard's side (the relay's frame,
the view-role drops, desks) and leaves the field unsent, behind a hub
option (`signal.Options.Decisions`) only its tests set; PR-7, the PR
that adds the join page's Decide tab, sets it where the switchyard is
built (`internal/cli`). So a switchyard built at PR-6, in front of a
publisher built at PR-7, says nothing of decisions and gets no decision
link, and no build advertises what its served page lacks. A publisher mints a decision link only at a
switchyard that said so: at an older one the share dialog's switch is
off with "This switchyard must be updated to answer decisions through
its links", `POST …/links` with `decide: true` is refused `409
switchyard_too_old`, and nothing is sent to it that it does not know (no
`TypeDecision` or `TypeDecide` in a relay envelope, which both sides decode strictly and
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
facts, its **Recent**: the last 50 `{ref, receipt, attempt, at}` acted
on through it, `attempt` the action's own identity, its `decide`'s
`opId` (an answer to the link's own session's question included, which
the join page's Decide tab sends as a `decide`), so an item that closed after its action (a
retried task is no longer failed) stays readable on the link with its
four steps for as long as the link stands, and two viewers of one link
who answered one question keep two entries, each judged by its own
attempt. A session decision link lives as its session's
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
signalling as a member when the join page opens its Decide tab.

The desk is the publisher's, by the identity a hosted session has
(`signal.Hub.Register`): `register_desk` carries the uplink's `HostInfo`,
whose `Instance` it requires (a desk without one is refused
`no_instance`, as a run link is, `groupOf`), and a stable `localId`
(`desk:<run id>`); the desk's id is `HostedID(instance, localId)` and
its owner `ownerOf(instance)`. So the same publisher's desk takes the
same id back after its connection drops (a `resume` with the secret its
`registered` gave, within the hold) and after the switchyard restarts (a
fresh `register_desk` with the same instance and local id, which takes a
desk held under that id back and is refused while another connection
holds it live, as a session's is), and `registered` carries the ids of
the live links naming the desk, as `liveLinkIDs` lists a session's, so
the publisher finds its run decision links again. Two publishers with
the same run id have two desks, of two owners: a run group's `Desk`,
and a run link minted over a desk, are checked against the minting
host's owner, as `groupOf` checks every member, so neither can name or
reach the other's. The desk
is made with the run's first decision link and ends at the first of:
the engine taking no command of the run any more, after `endLinger` as a
publication lingers (the run stopped, continued by another run,
`run_continued`, or forgotten, its `run` event with `removed`); the last
decision link of the run revoked or expired; the server's stop. A run's
outcome is not its end here: a run that finished `failed` or `cancelled`
is reopened by a Retry (`retryTask` clears the outcome,
`internal/crew/commands.go`), so its desk stays while its failed tasks
can be retried and its decision link stands, which is when the phone
needs it most. A run decision
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
| control `decision_track` | viewer → owner | `{t, reqId, ref, receipt, attempt}`: the four steps of one action taken through this link, `attempt` that action's identity (the `decide`'s `opId`), apart from `reqId`, which names this request | `MaxControl`; `receipt` 512, `attempt` 32 | `TestDecisionTrackValid` |
| control `decision_record` | viewer → owner | `{t, reqId, ref, part, rev?, after?}`: one part of a DR1 record in scope (`head` with its ref resolutions, `responses`, `history`), a page of the last two, `rev` the record's revision its `head` carried (required after `head`; another `rev` found is `record_moved`); `unsupported` until PR-3 | `MaxControl`; `ref` 512, `part` one of three, `rev` 20 digits, `after` 64 | `TestDecisionRecordValid` |
| control `decision_evidence` | viewer → owner | `{t, reqId, ref, evidence}`: a bounded read of one evidence entry of the item `ref`, named by the entry's own ref as the phone shows it (`evidence`), never by its place in a list | `MaxControl`; `ref` and `evidence` 512 each | `TestDecisionEvidenceValid` |
| frame `TypeDecide` (0x0A) | viewer → owner | JSON `{reqId, ref, action, revision, ticketRev?, input?, choice?, text?, rationale?, opId}`: one action on an item, `revision` the action's `Expect` and `ticketRev` its `TicketRev`; `input` a question's choice keys alone (terminal keys, exact); `choice` a DR1 option's index; `text` a question's line, or a DR1 response's or comment's text; `rationale` a DR1 response's (`decide`, below) | 32 KiB (`MaxDecide`), decoded strictly (`DisallowUnknownFields`): `reqId` and `opId` at most 32 bytes, `ref` 512, `action` one of the item's action names, `revision` 64, `input` at most `MaxAnswerInput` raw bytes, exact (288 encoded at worst), `choice` 0 to 5, `text` at most `MaxSubmit` raw bytes (4 KiB; 24 KiB encoded at worst), `rationale` at most 2 KiB encoded (DR1's bound): about 28 KiB at worst; under a data channel's message cap, as a file write's part (`MaxWritePart`) is | `TestDecideValid`, `TestDecideFrameAtItsBounds` |
| frame `TypeDecision` (0x09) | owner → viewer | JSON `{reqId, list?, item?, track?, evidence?, record?}`, one of: a page of at most 25 summaries in `(since, ref)` order with `next` and `total`; one item with its detail; four steps; an evidence read (a check log's last 4 KiB, or a member's published session id); one part of a DR1 record (from PR-3) | 128 KiB (`MaxDecision`); fragmented by `TypeChunk` on a data channel as a file frame is | `TestDecisionFrameBounded`, `protocol.test.ts` |
| control `decisions_changed` | owner → viewer | `{t, seq}`: the scope's items changed; the viewer lists again (one list in flight, one queued) | `MaxControl` | `TestDecisionsChangedValid` |
| control `decide_ok` | owner → viewer | `{t, reqId, receipt, repeat?}`: the operation's receipt ref; `repeat` when the operation already held the `opId` | `MaxControl` | `TestDecideOkValid` |
| error `decide_refused` | owner → viewer | the error message with `reqId`, a code (`stale_revision`, `not_in_scope`, `not_decision_link`, `not_found`, `question_answered`, `too_many_requests`, `unsupported`, `invalid`, `record_moved` with the record's new `rev`, and DR1's own codes as its operation gives them, `response_conflict` among them) and words | `MaxControl` | `TestDecideRefusedCodes` |
| `welcome`, field `decisions` | owner → viewer | `true` to a decision link's viewer; absent otherwise and from an older owner | a boolean in the welcome (`MaxControl`) | `TestWelcomeSaysDecisions` |
| host message `registered`, field `decisions` | switchyard → publisher | `true` from a switchyard that relays `TypeDecision`, keeps desks and serves the Decide tab (set from PR-7, which adds the tab); absent from an older one and from a PR-6 build | a boolean in a message bounded by `MaxHostMessage` (64 KiB) | `TestRegisteredSaysDecisions` |
| host message `register_desk` | publisher → switchyard | `{t, proto, host, localId, run: {id, name}, resume?}`: the first message of a desk's connection; `host` the uplink's `HostInfo` with its `Instance` required, `localId` the desk's stable local id (`desk:` and the run's id), `resume` the `{sessionId, secret}` of a desk whose connection dropped, as a session's `Register` carries | `MaxHostMessage`; `host` and `resume` as `Register`'s, `localId` `MaxLocalID`, `id` and `name` the run group's bounds | `TestRegisterDeskValid` |
| `RunGroup.desk` | publisher → switchyard, in the run's sync | the desk's id, absent with no desk | the run group's bound | `TestRunGroupDeskValid` |

**Why `decide` is a frame.** A control message is at most `MaxControl`,
8 KiB, checked on its encoded payload before anything reads it
(`proto.Decode`, `frame.go`), and a line `MaxSubmit` permits encodes past
it: 4,096 quotation marks are 8,192 bytes escaped before the message's
other fields, and a DR1 response adds its rationale (2 KiB encoded)
beside a clarification's text. So `decide` travels as a frame of its own
type, `TypeDecide`, bounded by the sum of its fields' encoded worst
cases, and every permitted text arrives unchanged; the reads stay
controls (their fields are ids, refs and cursors, under 2 KiB encoded at
worst). `main`'s own `submit` and `answer` controls, whose `Text` is
also up to `MaxSubmit`, have the same excess for a line of escaping
characters: a defect outside this stream (the reply bar's, on `main`
today), filed as its own issue when this plan is cut. The lane does not
lean on it: the Decide tab sends every action as a `decide`, its own
session's question included (`decide`, below).

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
refused `not_in_scope`. The entry is named by its ref, never by its
index: the inbox computes an item's evidence on each read, so after
another viewer's retry and its next attempt's failure, or a newer
Brakeman card, the third entry can be another check. A ref names one
thing for good (a `check:` ref one ledger row of one attempt, a
`session:` ref one session), so the owner serves what the ref names
when it is in the link's scope, whether or not the item lists it now,
and the reply carries the ref it answers, which the phone checks
against the entry it unfolded; a ref that names nothing is
`not_found`. `InboxItem.vue` takes the join page's route
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
scope: on each source event it computes, per connection, a hash of the
scope's summaries, each open item's actions' `Expect`, and the four
steps of every entry of the link's Recent (read again with `Track`), and
sends `decisions_changed` when the hash differs from the last one sent.
So a receipt that moves while the open list stays the same (a retried
task leaves the list, its receipt says resumed `waiting`, and its next
attempt then starts; a question answered and the agent working again)
still tells the viewer, which lists again and tracks again every Recent
entry not final. The viewer also tracks its Recent again after a
reconnect's welcome.

**`decide`** maps to the item's own operation, never a new one. A
question of the connection's own session is answered by a `decide` with
`answer` that the owner applies exactly as that connection's own
`answer` message (the viewer is the session's subscriber; its claim, and
first response wins, as ever), with the `decide`'s `opId` as the
answer's request id, so a line of any permitted text reaches it whole
and the Decide tab has one path (the terminal tab's reply bar keeps
sending `answer`, as today). A question of another member's session in a run link's scope
is a `decide` with `answer`, applied through that question's claim as a
routed answer is (`AnswerAs` for a choice, `Submit` with the question
and the link's name for a line), with the link as the actor and the
`decide`'s `opId` as the answer's request id: `AnswerAs` gains it
(`session.Actor.ReqID`; today it keeps no request id and leaves
`Answer.By` empty, `answeras.go`), and the kept result records it as
`ReqID` beside `ByName`, as a viewer's `answer` records its `reqId`
(PR-1), and a line's `Submit` the same, so the winner of a question is
known by attempt whichever path answered; `retry`,
`accept` and `cancel` to the engine's `Retry`, `Accept` and `Cancel`
with the action's `Expect` (the message's `revision`) as the expected
revision and `opId` as the command id; any other action through the operation the inbox names for
it (each action's server-side `do`, never serialized, which PR-3 gives
DR1's kinds once DR1 accepts a link's actor). **DR1's response whole.**
For DR1's kinds the frame's fields map one to one onto DR1's own
bodies, nothing dropped or merged (`decideArgs` carries every field of
the frame): `answer`, `approve` and `reject` onto
`POST /api/decisions/{did}/respond`'s `{responseId: opId, choice, text,
rationale, expectedRev: revision}` (an approval's Approve and Reject are
its options, sent as `choice`; a clarification may carry `choice` and
`text` both), and `more_info` and `comment` onto the comment
operation's `{opId, kind, text, expectedRev}`. `input` is never a DR1
field: it is terminal keys, a question's alone, and a `decide` on a DR1
record carrying it is refused `invalid`. DR1 checks each field's bound
and the response's fingerprint as its route does (the same `responseId`
with another body is DR1's `response_conflict`, carried as the
refusal's code), so a response through the lane is the response the
route would record. On the join page, `DecisionRespond.vue`'s body goes
through a transport adapter (`utils/transport/decide.ts`, the lane's
client of PR-7, given DR1's kinds in PR-4) that maps it onto the frame
field for field. The actor is the
connection's link, never a field of the message. **A repeat first**: a
`decide` whose `opId` the operation already holds for this link's actor
(the ledger's receipt keyed by run, actor and command id; Dispatch's by
op id) is answered with that receipt, `decide_ok` with `repeat`, before
the item's current actions are judged, so a `decide_ok` lost with the
connection and asked again after a reconnect returns the first receipt,
not `not_in_scope` for an item that closed; the link must still stand.
`decision_track` reads a receipt the same way, by this link's actor and,
for a question, by its `attempt` against the kept result's `ReqID`
(winner: done; another: "another answer first"), so a link's steps are
never the admin's, and one viewer's never another's, (Brakeman's `command:` ref resolver
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
lane (`utils/transport/base.ts` takes `TypeDecision` and the controls
and sends `TypeDecide`),
paged with More, acting through `decide`, unfolding evidence through
`decision_evidence`, its Recent read through `decision_track`, with the
terminal one tab away; a run link's join page shows the run's items above
its members' grid and joins the desk for them. The decision page's "Open
on your phone" offers, on a loopback origin, a decision link for a run
or a session the person picks.

### Docs

Each PR adds its own rows to `docs/protocol.md` (PR-1 the inbox routes,
PR-3 DR1's items in the inbox, PR-5 the lane's messages, its two frames and
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
acts on it carries the action's `Expect`, and the operation, not the page,
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
| question of a server session, before DR1 | the attention showed the id | the session's kept result by id: this attempt's `ReqID` the winner's (done, by, when) or another's (refused: "another answer first", by whom); forgotten (the session gone, 64 questions later): `unknown` | the same claim (the session is the owner: one step, said so) | the kept result's resumption (`Resumed`, set when the attention first left the question): `working` or a next question, done at its time; the session's end, the reason; none yet, `waiting`; the result forgotten, `unknown`; else the reason (withdrawn, another's answer) |
| question of a hosted session | the attention showed the id | `unknown` on the server ("the host holds this answer"); the page shows the host's word on its own request | as recorded | the resumption the server kept as the hosted session's attention left the question; `unknown` where it saw none |
| task | the task's state allowed the command | the command's receipt in the ledger, whatever its status (the row is the record) | by the receipt's status, of the four the ledger has (`internal/crew/ledger/rows.go`): `accepted`, `waiting` ("the engine took it and is applying it"); `done`, done; `failed`, refused with its code and words; `uncertain` (recovery's word for a command accepted and never settled before a stop, `transition.go`), `unknown`: "the server stopped while the engine applied this; what it did is read from the task" | retry: the next attempt running; accept: the dependents dispatched; cancel: does not apply (its dependents are blocked, and the step says so); for an `uncertain` receipt, read once from the task's record since the command's acceptance: the command's effect there (a next attempt started after it, the task accepted or cancelled after it) is done, "it took effect before the stop"; none is `not_applicable`, "nothing shows it took effect; the task's commands are offered again" |
| plan | proposed, or approved and stale | the Dispatch operation's receipt | the operation's own outcome: done, or its refusal (`stale_revision`, withdrawn) | does not apply: "a launch from the ticket page starts it" |
| decision (DR1) | DR1's four states, as DR1's record reports them and DR1's card draws them (PR-3, PR-4) | | | |

Each step is a historical fact: receipts are kept and settled once (a
command's `accepted` becomes `done` or `failed`, or `uncertain` by the
recovery after a stop) and never rewritten after, so two reads give the
same steps or later ones, from any browser. What
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
viewer's connection as today (`link_gone`, `OnRevoke`), and nothing not
yet begun for a link lands after its revoke or its expiry: an act asks
whether its link stands as it is begun (the answer's write, the
command's commit), fail-closed, and one whose link has ended is refused,
nothing done. An act begun before may land (a write to a PTY cannot be
taken back, a committed command stays committed): the revoke or expiry
waits up to `share.Store.HoldWait` for what was begun, then returns and
says what is still pending (amended in PR-5, review round 3). A run's desk lives while the engine still takes the run's commands
and one of its decision links stands, a finished `failed` run included,
so a run with no member alive (the last attempt failed; from PR-3, a
gate before the first attempt) is still reached through its link.

**State and migration.** No database or file schema changes in any PR:
the inbox stores nothing but the routes kept by question id (in memory),
`share.Link.Decide`, `remoteLink.decide`, a link's Recent and a run's
desk are in memory as the links and publications are, the session's
kept result gains its request id in memory, and the browser's recent
list is a per-browser convenience that may be empty at any read. PR-3
reads DR1's tables through DR1's reads and the inbox's one query
(`InboxDecisions`), and changes none of them.

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
- `TestInboxHeldTurnOffersNoAnswer`: a submission held for the terminal
  (the hold `hold_test.go` makes): one item with no `answer` action, its
  words sending the person to the terminal and the session's terminal
  first in its evidence; an `answer` naming its id sent anyway refused
  by the session, nothing typed.
- `TestABlockerIsOneItem`: an attempt reports a blocker (`Engine.Blocked`):
  one item, the session's question, naming the task and the blocker and
  carrying Cancel; no task item; the task failing later is then its own.
- `TestABlockersCancelCarriesTheRunsRevision`: the blocker's item read,
  its Cancel's `Expect` the run's revision and its answers' the
  question's id; the run moved (another task's command) while the same
  question still shows: the Cancel at its `Expect` refused
  `stale_revision`, nothing cancelled, the item read again carrying the
  new revision, and a Cancel at it taken.
- `TestInboxTaskItemsFollowTheEngine`: for every row of
  `internal/crew/testdata/task-commands.json`, the item's actions equal
  what the engine then accepts, each command sent at its action's `Expect`
  succeeding and each other refused; Accept anyway only where a kept
  attempt submitted a commit, none after a withdrawal took it back.
- `TestTaskCommandsFollowTheFixture` (Go): every row of the fixture
  against `TaskCommands` and the engine; its vitest pair against
  `taskActions` is PR-2's.
- `TestInboxBlockingNamesTheDependents`: a diamond of four tasks; the
  failed root's item names three dependents; past 20, `more`.
- `TestInboxPlanItems` and `TestOpenPlans` in `internal/dispatch`: a
  proposed revision is an item; an approved one is not, until the
  ticket's description is edited (an item again, Approve again offered,
  its `Now` saying why), and not once the edit is undone; a
  `RemedyRevise` one has no Approve; deleted tickets left out, oldest
  first, the limit.
- `TestOpenPlansPages` (`internal/dispatch`) and `TestInboxPagesPlans`
  (`internal/api`): 230 open revisions over five tickets (proposed ones
  and stale approvals among them), read page by page from the cursor:
  every one reached once, in `(since, ref)` order, none repeated, `total`
  230 on every page, and the inbox's pages, merged with questions and
  tasks, the same.
- `TestInboxPlanActionsCarryBothRevisions`: the ticket edited alone, an
  Approve at its action's revisions is refused `stale_revision` for the
  ticket; the revision approved by another operation alone (its `rev`
  bumped, the ticket's not), a Withdraw at its action's `Expect` is
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
- `TestInboxTrackCommand`: retry at its action's `Expect`: recorded and
  validated done, resumed waiting until the next attempt runs, then done;
  at an old revision: validated refused with `stale_revision`, resumed
  not applicable.
- `TestInboxTrackACommandAcrossARestart`: a retry held between its
  acceptance and its settlement (a seam between `Engine.act`'s two
  transactions): recorded done, validated `waiting`, not final; the
  server stopped there and started again over its data directory
  (servers ended later first, AGENTS.md), the recovery making the receipt
  `uncertain`: validated `unknown` with its words, resumed read from the
  task (the continuation's next attempt: done; none: not applicable),
  final, the same from a second read; let go without a stop: validated
  done.
- `TestInboxTrackQuestion`: answered through the claim: recorded and
  validated done at the answer's time, resumed done once the attention is
  `working`; withdrawn by the screen: the reason; two answers with two
  request ids: the loser's track says "another answer first" and the
  winner's done; the result forgotten past 64: `unknown`.
- `TestInboxTrackReadsTheKeptResumption`: a question answered, the stub
  working and its turn done (the attention `idle`) before the first
  track read: resumed done at the time it worked, from two reads as two
  browsers; a hosted question the same from the milestone the server
  kept, and `unknown` for one whose leaving the server did not see.
- `TestInboxTrackHostedQuestion`: a hosted session's question: recorded
  and validated `unknown` with the words, resumed from its attention.
- `TestInboxTrackPlanApproval`: the approval's validated step stays done
  after the ticket is edited, while the item's `Now` says it no longer
  holds, and after the edit is undone; two reads, as two browsers, give
  the same steps.
- `TestInboxIsAdminOnly`, `TestInboxOnASwitchyard` (`403 switchyard`).
- `TestQuestionResult` in `internal/session`: the kept result by id with
  its request id and its resumption, set once at the first attention
  change after the answer and not changed by later ones, the 65th
  forgotten.

**PR-2** (vitest, Playwright):

- `utils/inbox.test.ts`: the order; each step's words; `reconcileDelivery`
  never moving a step back over replies in either order and keeping a
  `done` over a later `unknown`; an `accepted` command still tracked and
  an `uncertain` one final once its resumed step is read; the recent list (50 kept, older than a
  day dropped, storage that throws read as empty, the reply's outcome
  written into its entry); `phoneLanding` (below `lg` with an open item:
  `/decisions`; at `lg`, or with none: stay); `notificationTarget` (below
  `lg` the item, at `lg` the session); `phoneOffer` (a LAN origin: the QR
  code of `origin + /decisions`, no token in it; a loopback origin: the
  decision link offer).
- `composables/useInbox.test.ts`: one read in flight and one queued over a
  burst of 20 events; a `snapshot` and a `removed` each read the list
  again; with the stream down, the question answered and a new one asked
  on the server meanwhile, the stream's return (its snapshot) shows the
  first gone and the second there; a `run` event that leaves the open
  list unchanged reads again the track of a retry still waiting and shows
  its resumed step done.
- `composables/usePaletteRows.test.ts`: "Go to Needs your decision" and
  one row per open item, by ref.
- `tasks.test.ts`: the shared fixture (above) against `taskActions`,
  read from `internal/crew/testdata/task-commands.json`, with
  `taskActions` corrected in this PR where a row differs (Accept anyway
  from any kept attempt's submitted result, of any kind); the run page's
  specs that show task actions (`tasks.spec.ts`) pass with it.
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
  words shown first; a submission held for the terminal (made as
  `held.spec.ts` makes one) shown with no choice and no line, its words
  and its terminal link, which opens the session's terminal; the
  evidence unfolded and the session's terminal opened from it; the item counted by its ref, never by the list's
  length (`stopStrays` first); the events stream cut (the page's route
  to `/api/events` aborted) while the question is answered through the
  API and another asked, then let back: the first card gone and the
  second there, by ref. At a desktop viewport, after waiting for
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
- `TestAnAnsweredDecisionStaysTracked`: a gate of a run answered while
  the run is interrupted (its server stopped mid-run and started again
  over the data directory, no continuation yet): the record `answered`,
  listed as a tracking item with DR1's four states (recorded, not
  applied), on every page read and after another restart, and not in the
  bar's count; the run continued: applied, out of the tracking items and
  into Recent.
- `TestInboxPagesDecisions` (`internal/api`) and `TestInboxDecisions`
  (`internal/crew/ledger`): 230 open DR1 records over three runs, made
  interleaved with questions, failed tasks and plan revisions, read page
  by page from the cursor: every item once, in `(since, ref)` order,
  none repeated, `total` right on every page; the query alone oldest
  first with its count.
- `TestADecisionRecordReadAcrossAChange`: `head` read, then a response
  recorded and applied before the history's first page (the lane held at
  a seam after `head`): the history request at the head's `rev` refused
  `record_moved` with the new `rev`; read again from `head`, the
  assembled record equal to `GET /api/decisions/{did}`'s at the new
  `rev`, never a mix; the ref resolutions those of `head`'s read.
- `TestDecideCarriesADR1ResponseWhole`: a clarification's `decide` with a
  choice, a 4,096-byte text of escaping characters and a rationale at its
  bound mapped onto DR1's respond body field for field, the recorded
  response equal to the same body sent to `/respond` (through the
  action's `do` once DR1 accepts a link's actor; before, the mapping
  against DR1's operation called as the admin); the same `opId` with
  another text refused `response_conflict`; an `input` on a DR1 record
  refused `invalid`.
- `TestInboxDecisionActionsUseDR1sRoutes`: answer, approve and reject
  through the respond operation, more information and a comment through
  the comment operation, each with its id and the record's `rev`; a
  repeat answered with the first receipt.
- `TestTheFullestDecisionRecordThroughTheLane`: DR1's fullest record
  (the one `TestTheFullestDecisionFitsItsBound` builds, every part at its
  bound, its texts escaping) read through `decision_record` part by part
  and page by page: every frame at most `MaxDecision`, the assembled
  record equal to `GET /api/decisions/{did}`'s; its `decision` item at
  most 96 KiB.
- `TestADecisionSurvivesARestart`: the record open across a server's
  restart over the same data directory, its item the same; after a
  response, its four states the same after the restart (servers ended
  later first, as AGENTS.md's rule for one data directory says).
- `TestALinkShowsADecisionRecordWithoutActions`, or, once DR1 accepts a
  decision link's actor under `local/1`, `TestALinkAnswersADecisionRecord`
  in its place: a decision link's `decide` on a DR1 record through the
  action's `do`, its responder the link.
- `TestAGateOverTheDesk`: a gate a plan requires before the run's first
  attempt, with no member alive, listed over the run's desk, at the
  server (`GET /ws/runs/{run}/desk`) and at a switchyard (the desk's
  connection, as PR-6's tests drive it), shown without actions until DR1
  accepts a decision link's actor and, from then, answered there, its
  four states read back through the lane.

**PR-4** (vitest, Playwright): `utils/inbox.test.ts` cases for the
decision kind; `web/e2e/decision-page-durable.spec.ts` on the phone
viewport: a gate a plan requires (DR1) answered from the Decide tab
through DR1's card, the server restarted (a server of the spec's own
over its data directory, stopped and started again as `brakeman.spec.ts`
does), the item's four states the same after; Ask for more information
keeps it open and names the note; a response at a stale record refused
with words; the ticket page's records linking to the item; a gate
answered while its run is interrupted still shown under "Answered, not
yet applied" after a reload, until the run is continued. Vitest: the
record's assembler (parts of one `rev` handed whole to DR1's card, a
`record_moved` starting again from `head`, four restarts then the
words); `utils/transport/decide.test.ts`, `DecisionRespond.vue`'s body of
a choice, a clarification's text and a rationale mapped onto the frame
field for field; `BrakemanRefChip` and `BrakemanReceipt` with an injected
`refAccess`, their link and their read the access's and no `useApi`
call, and with none, today's.
`web/e2e/switchyard.spec.ts` gains the gate over the desk: a run whose
plan requires a decision before its first task, its decision link
opened in a phone context at the spec's switchyard with no member
alive, the gate's card drawn from the assembled record and, once DR1
accepts a decision link's actor, answered there with its four states;
the card's evidence chips there: a check chip's log read through the
lane, a session chip opening the member's terminal tab, the others their
words, and no request from the join page to the server's `/api/` (the
spec watches the page's requests).

**PR-5** (Go race and vitest; `internal/proto`, `internal/share`,
`internal/session`, `internal/api`, `web/app/utils/protocol.ts`):

- `TestDecisionFrameBounded`, `TestDecisionsListValid`,
  `TestDecisionReadValid`, `TestDecisionTrackValid`,
  `TestDecisionRecordValid` (and the owner's `unsupported` for it before
  PR-3), `TestDecisionEvidenceValid`, `TestDecideValid`, `TestDecideOkValid`,
  `TestDecisionsChangedValid`, `TestDecideRefusedCodes`,
  `TestWelcomeSaysDecisions`; `utils/protocol.test.ts` cases for each in
  the same PR.
- `TestDecideFrameAtItsBounds`: the committed fixture
  `internal/proto/testdata/decide-max.json` (a `text` of 4,096 quotation
  marks, a rationale of 2 KiB encoded of escaping characters, an `input`
  of 48 escaping bytes, every id at its bound) read by `proto.Decode` and
  the strict decoder, every field equal byte for byte; Go's encoder's
  frame for the same values at most `MaxDecide`; one byte past it
  refused; `protocol.test.ts` encodes the same values under the bound and
  decodes the fixture equal.
- `TestDecideRefusesAHeldTurn`: a run link's `decide` with `answer` naming
  a member's turn held for the terminal refused `question_answered`,
  nothing typed; the item listed with no answer action.
- `TestDecisionEvidenceByItsRef`: an item's detail read; another viewer
  retries the task and its next attempt fails (the item's evidence now
  leads with attempt 2's check); the first phone unfolds attempt 1's
  check by its ref: attempt 1's log, the reply naming that ref; a check
  of another run refused `not_in_scope`.
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
- `TestALinksLosingViewerReadsAnotherAnswerFirst`: two viewers of one
  run decision link answer one question with two attempts, once through
  a `decide` on the asking session's own link (applied as its `answer`)
  and once through `decide` for another member's question (`AnswerAs`, and a line through
  `Submit`); the loser's connection closed and a new one opened, its
  `decision_track` with its attempt reads recorded refused, "another
  answer first", and the winner's done; `TestAnswerAsKeepsTheRequestID`
  in `internal/session`: the kept result's `ReqID` and `ByName` are the
  actor's.
- `TestDecideLostReplyReplays`: a `decide` taken, its connection closed
  before `decide_ok` (a seam holds the reply), a new connection's same
  `opId` answered with the first receipt and `repeat`, the item closed by
  then; nothing done twice.
- `TestALinkKeepsItsRecent`: a task retried through the link leaves the
  open items and stays in the link's Recent with its four steps through
  to resumed.
- `TestDecisionsChangedOnAReceiptAlone`: a task retried through the
  link, the next attempt's start held at the fake launcher's gate until
  the viewer has listed (the item gone) and tracked (resumed `waiting`),
  then let go: `decisions_changed` arrives with the open list unchanged,
  and the track then reads resumed `done`; the same for a question
  answered, the agent's `working` held until the viewer has tracked.
- `TestDecisionEvidenceIsScoped`: a failed check's log tail read through
  the lane; a ref outside the scope refused `not_in_scope`.
- `TestTheServersDeskServesARunWithNoLiveMember`: a run link at the
  server, `GET /ws/runs/{run}/desk`, before any attempt (the run's first
  task waiting: the desk answers, its list empty, `total` 0) and after
  the last member's attempt failed: the failed task listed and a retry
  taken. No item before an attempt exists until DR1's gates are items;
  the gate over the desk is PR-3's test.
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
- `TestADeskIsItsPublishers` (`internal/signal`, `internal/api`): a
  `register_desk` without an instance refused `no_instance`; two
  publishers with the same run id get two desks of two owners, and one's
  run group naming the other's desk, or a run link over it, is refused;
  a desk whose connection drops resumes with its secret; the switchyard
  restarted over its data directory, the publisher's desk registers
  again with the same id and its `registered` names the run decision
  link, which still opens the desk.
- `TestPeerSendsDecisionFrames` and `TestPeerRefusesDecideWithoutDecisions`
  in `internal/hostagent`, over its relay as `peer_test.go` drives it;
  `TestConductorHostRefusesTheLane` (`unsupported`).
- `TestUplinkPassesDecisions` (`internal/cli`, `internal/hostagent`): the
  serve command's uplink hands the API's `Decisions` to each publication
  and desk.
- `TestADeskCarriesARunWithNoLiveMember`: a run decision link at a
  switchyard minted with no member published (the run's first task
  waiting: the desk lists none), then its only attempt failed and its
  member's session ended: the failed task listed and retried over the
  desk, the desk and the link still working.
- `TestADeskOutlivesAFailedRunsEnd`: the run finished `failed` (every
  task failed or blocked behind one), its last member gone: past
  `endLinger` (the desk's linger timer seen fired through a test hook,
  never a sleep) the desk still serves the failed task, and a retry
  taken through it reopens the run and starts the next attempt; the run
  stopped: the desk ends once its linger has fired; the last decision
  link revoked: it ends.
- `TestRelayCarriesDecisionFrames` (`internal/proto`, `internal/api`): a
  relayed `TypeDecision` reaches the viewer and a relayed `TypeDecide`
  the owner, and the host's connection stays;
  `TestRelayDropsTheLaneFromAViewer` (`internal/signal`): a view-role
  viewer's `decide` and reads are dropped at the relay.
- `TestNoDecisionLinkAtAnOlderSwitchyard`: a switchyard whose
  `registered` says nothing of decisions and which closes
  `register_desk`: `decide: true` refused `409 switchyard_too_old`, no
  `TypeDecision` sent over its relay, the host's connection stays; and a
  switchyard of this PR's build as `cli` makes it (relaying and keeping
  desks, `Options.Decisions` unset), in front of a publisher that knows
  the field: the same refusal, no decision link minted.
- The lane's tests `-count=30` before the PR is ready.

**PR-7** (Go race, vitest, Playwright): `TestTheSwitchyardSaysDecisions`
(`internal/cli`): the switchyard the command builds sets
`Options.Decisions`, its `registered` says `decisions: true`, and a
publisher mints a decision link at it; `transport` cases for the frame and the
controls (`utils/transport/base.test.ts`); `web/e2e/switchyard.spec.ts`
gains a decision link test: the second server of the suite's shape that
already publishes to the spec's switchyard mints a decision link for a
managed run whose task fails; a phone context opens the link at the
switchyard, sees the item, unfolds its failed check's log, retries it,
and its steps reach resumed, read again from the link's Recent after the
item closed; the run's member's question answered the same way; a
member's turn held for the terminal shown with no answer controls, its
terminal tab one tap away; a run
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
      inspect and revoke are untouched, and a revoked or expired decision
      link begins nothing more (an act begun before may land; the revoke
      waits up to `HoldWait` for it, then says what is pending).

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
| PR-1 | api: the decision inbox on what main holds (questions, tasks, plan revisions, Brakeman's cards) and the four steps of an action | `internal/api`, `internal/crew`, `internal/dispatch`, `internal/session`, `docs/protocol.md` | none (a plan item's `subject` is `ticket:` until Brakeman's PR-10, which nothing here waits for) | none | ~1,400 |
| PR-2 | web: Needs your decision, the phone's Decide tab and landing, the palette, the shared card text | `web/app`, `web/e2e` | PR-1 | none | ~1,400 |
| PR-5 | proto, share, session, api: decision links and the lane at the server, the server's desk route | `internal/proto`, `internal/share`, `internal/session`, `internal/api`, `web/app/utils/protocol.ts` and its test, `docs/protocol.md` | PR-1 (`internal/api`, `docs/protocol.md`), PR-2 (`web/app`) | none | ~1,300 |
| PR-6 | proto, signal, api, hostagent, cli: the lane through a switchyard, the run's desk | `internal/proto`, `internal/signal`, `internal/api`, `internal/hostagent`, `internal/cli`, `docs/protocol.md`, `docs/architecture.md` | PR-5 (`internal/proto`, `internal/api`, `docs/protocol.md`) | none | ~1,300 |
| PR-7 | web, cli: a decision link's join page, the share switch, the switchyard's `decisions` turned on, through a switchyard | `web/app`, `web/e2e`, `internal/cli` (the switchyard's `Options.Decisions`) | PR-2 and PR-5 (`web/app`), PR-6 (the switchyard's side the spec drives, `internal/cli`) | PR-3 | ~1,000 |
| PR-3 | api: DR1's records as items, through DR1's routes | `internal/api`, `internal/crew/ledger` (`InboxDecisions`, unless DR1's plan takes it), `docs/protocol.md` | PR-6 (`internal/api`, `docs/protocol.md`), DR1's PRs that serve the record, its respond and comment operations and its event (`docs/plans/decisions.md`) | PR-7 | ~700 |
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
OpenPlans`; `session.Local.QuestionResult`, the kept request id and the kept
resumption (the server's for a hosted question too); the
routes' rows in `docs/protocol.md` (HTTP API). Tests: PR-1's above.

**PR-2** (`web/app`, `web/e2e`): the page, its components and the
composable, `utils/inbox.ts`, `BrakemanDecisionText.vue` extracted from
`BrakemanDecision.vue`, the bottom bar and the phone's landing, the
sidebar's row, the palette's rows and the `G` `N` chord (and its line in
the `?` list), the notification's target below `lg`, Open on your phone
(the URL's QR code), `taskActions` brought to the fixture where it
differs from the engine, with the fixture's vitest case in
`tasks.test.ts`; `decision-page.spec.ts`; `phone.spec.ts`'s bar
test updated for Decide first and Events under More. Tests: PR-2's
above.

**PR-5** (`internal/proto`, `internal/share`, `internal/session`,
`internal/api`, `web/app/utils/protocol.ts`, `docs`): `Decide` on a link,
the link routes' `decide` field, the two frames (`TypeDecision`, `TypeDecide`), the lane's messages and the
welcome's field in all three protocol places with `protocol.test.ts`,
`session.AttachOptions.Decisions`, `AnswerAs` keeping the request id it
is given (`session.Actor.ReqID`) as the kept result's `ReqID`, the link's
Recent by attempt, `hostagent.Decisions` as an interface
the API implements, `ws_viewer.go`'s handling, the server's desk route,
the scope, paging, evidence reads, a link's Recent, the replay and the
in-flight bounds. Tests: PR-5's above, the lane's `-count=30`.

**PR-6** (`internal/proto`, `internal/signal`, `internal/api`,
`internal/hostagent`, `internal/cli`, `docs`): `registered`'s
`decisions`, `register_desk` and `RunGroup.desk` in all three protocol
places (the desk's registration carrying the publisher's `HostInfo` and
a stable local id), the switchyard's desk registration with its owner,
resume and re-registration after a restart, and its join, the relay's list
of the frames it carries and `RelayToHost`'s view-role drops,
`remoteLink.decide`, `peer.go`'s handling, `hostagent/desk.go`, the
uplink's `Decisions` and its wiring in `cli/serve.go`, `conductor host`'s
refusal, the `409 switchyard_too_old` refusal, the architecture section.
Tests: PR-6's above, the lane's `-count=30`.

**PR-7** (`web/app`, `web/e2e`, `internal/cli`): the switchyard's
`Options.Decisions` set where `cli` builds it, so its `registered` says
`decisions: true` in the same cut as the Decide tab it serves; the
transport's handling of the frame and the controls, the join page's Decide tab with paging, evidence and
the link's Recent, the run link's items over the desk, the share switch
on the session's and the run's links with its words at an older
switchyard, the loopback origin's offer of a decision link; the
switchyard spec's decision link test. Tests: PR-7's above.

**PR-3** (`internal/api`, `internal/crew/ledger`): the `decision` kind
from DR1's routes and the inbox's ascending query with its count
(`InboxDecisions`), answered records kept as tracking items, the
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
- **Through a self-hosted switchyard**, once PR-4 is merged and DR1's
  plan accepts a decision link's actor under `local/1` (both are
  prerequisites of this observation, since its gate is a DR1 record
  answered over a link; until then it is not made and the item is not
  Done): a
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
the eight issues; DR1's plan and PRs, which PR-3 waits for, and its
acceptance of a decision link's actor, which the switchyard observation
waits for; the phone and
the agents' subscriptions for the observation; the merges.
