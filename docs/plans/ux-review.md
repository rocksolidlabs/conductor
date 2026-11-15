# UX1: one status language, one list of what needs you, and screens that fit (the UX review's follow-ups)

Status: proposed 2026-11-15, for the owner's review; the owner sets it
to Planned. Main at 9fb98b06 (PR #895, 2026-11-15). Every fact below
about the code was read from that commit on 2026-11-15, by eleven
readers, one for each surface, and checked by a twelfth; where a fact
was read from an open pull request it says which. Pull request #908
merged after it, on 2026-11-15 (`f3d39b1f`).

A UX review of `main` (at `40eccb5f`, with three proposals added at
`cf6de69a`) ran the app with a busy day seeded (18 live sessions, 6
runs, 14 tickets, 9 things waiting on a person), looked at about sixty
states of the app's screens in both themes, at 1440 and at 400 wide,
and read the code behind each. Its finding is one sentence: each part
of the app is made with care, and the layer above the parts is missing.
There is no one way to say what state a thing is in, no one list of
what waits on a person, no one way to lay out a row. It made
twenty-four proposals (P1 to P24) and listed thirteen defects it met on
the way (D1 to D13).

The owner asked for all of it, planned as one stream of follow-up work
on the app's screens, with good tests. This is that plan. It builds
every proposal and every defect that is not already fixed, in
thirty-eight pull requests of reviewable size, and a thirty-ninth for
the continuous integration job that runs their tests. What it adds above
the parts is small and is each built once: one function that says a
session's or a task's state, one function that lists and counts what
needs a person, one table of the pages, one shape for a page with
nothing in it, and one file of design rules with checks that fail when
the code breaks a rule. What it changes on the screens follows from
those.

The tests are three fifths of the stream by line count, and they are of
one kind: they hold what the review complained of. A seeded day is a
shared fixture. Layout is asserted by measures (nothing cut, nothing
overlapping, cells in a column at one x, so many rows in view), never by
comparing pictures. One table of five states is walked over every
surface. One number is read on every surface for the same items.
Contrast is computed from the colour tokens and read again from the
drawn page. Every new control is driven by the keyboard and by touch.
Every test is proved able to fail before it is trusted.

The proposals keep their ids so that a reader of the review can follow
them. This document stands without it: each change is described here.

## Articles it serves

1. No premium code, no licence check: every row ships whole in this
   repository. Nothing asks for a plan, a tier or an entitlement, and no
   screen is drawn differently for one.
2. Fully useful alone: the stream changes how the free build's screens
   say what they already know. It needs no Conductor account, no hosted
   service and no network beyond what the app uses today.
3. Paid features gated by hosted dependencies: not touched. Nothing here
   is paid, and nothing is hidden behind a flag.
4. Stop, inspect, revoke, export, required approvals: untouched and
   never weakened. Where a row folds detail (a decision's fine print in
   PR-20, an approved plan in PR-16) what a choice authorizes stays in
   view in one plain line and the whole record is one press away. Where
   a row hides a control at rest (Cancel behind a row's menu in PR-11,
   the nav's row at zero in PR-5) the action stays reachable by its key
   and by the palette, and Stop is never moved. No approval is skipped,
   merged with another or answered for a person: a reply choice is drawn
   filled only where the agent itself recommended it (PR-19), and the
   page of what needs a person opens an item by itself only to show it
   (PR-21).

## Why and what happens

The owner named four things, and asked for two more to be looked at.

**"Unclear what needs me."** On the seeded day three places answer the
question with three numbers: the sidebar's section says 6, the nav's row
says 9, the Yard's says 5. Each counts something different. A task that
failed until its attempts ran out, and waits for a person, is listed
under Running with a green dot. And an agent that has finished its turn
and waits for the next message looks exactly like one at work: five
surfaces draw a session's state, each by its own rule, and they disagree
with each other. After the stream there are five states, each with one
mark and one word on every surface (PR-2, PR-3, PR-10); one list of what
needs a person, in the sidebar, with every kind in it, and one amber
number (PR-4, PR-5, PR-13); finished agents apart from working ones
(PR-14); and a page of that list that opens on the question and its
choices (PR-6, PR-20, PR-21).

**"Too much on screen."** At its default width the sidebar shows two of
eighteen sessions and cuts their names, their questions and the app's
own name. A run's header cuts the run's name to make room for badges
that then run under its buttons. A Dispatch project's first ticket
starts 400 px down a 900 px window. A ticket is seven windows tall with
no way to jump within it. After the stream: sidebar rows that fit the
narrow sidebar, with a question on two lines and whole beside its row
(PR-7, PR-8); the pages as six tiles at the sidebar's foot (PR-15); a
run header in two rows and task rows as a table that says the state
once (PR-9, PR-11); one toolbar on a project and columns that line up
(PR-12); a strip of a ticket's sections and plans that fold once
approved (PR-16); Yard tiles that lead with the name, and a Yard that is
its grid (PR-17, PR-18); a session page where the terminal has the room
(PR-19).

**"Hard to find things."** Ten pages stand in one list, and three
everyday jobs are each split over several of them: what waits on a
person, how a person is alerted, how an agent is set up. After the
stream ten destinations are seven, each job ends in one place, and old
links keep working (PR-17, PR-27, PR-28, PR-29).

**"Looks generic."** The brand has a palette of its own and the
workbench uses none of it outside the mark: its greys are a stock
scale. After the stream the neutrals are the brand's (PR-24), the main
action has one colour, mono is for evidence and amber for "someone must
act" (PR-26), a run is drawn as a line diagram, which only a product
named for the railroad would draw (PR-32, PR-33, PR-34), and the design
rules are written down and checked (PR-37, PR-38).

**The first hour.** A person who has never seen the app meets a request
for a token that tells them to look in the wrong place, a welcome of
three buttons one of which is cut, and nine names that mean nothing
yet. After the stream: a page to connect that says what is true
(PR-31), a welcome that teaches (PR-30), a launch that takes a first
message and names the session from it (PR-35, PR-36).

**The quiet day.** With nothing running most pages are an icon and one
line, each made its own way, and the paragraph that explains a page is
shown to the person who has read it every day and not to the one who
needs it. After the stream every page with nothing in it has one shape:
what the page is, why it is empty, the one action that fills it (PR-22,
PR-23).

What is already strong is kept, and the tests hold it: a question
follows a person to wherever they are and can be answered there; the
palette reaches everything; every action has a key, in a terminal and
outside it; panels are flat and a status word stands beside every
colour.

## Invariants

These hold on `main` after every row that touches them, and a test holds
each.

1. **One status language.** A session's state and a task's state are
   each worked out by one function, and every surface draws the mark
   and the word that function gives. The table is in "The status table"
   below. No surface derives a state for itself.
2. **One list, one number.** What needs a person is one list, worked out
   once, never by subtraction. Every count on the desktop reads it. Two
   surfaces that count the same items show the same number, and a
   surface that counts fewer says what it leaves out: until the
   sidebar's section lists every kind (PR-13), its header reads "Needs
   you 6 · +3 on the page", so the desktop never shows two amber
   numbers that disagree without saying why.
3. **One name.** The list, its section, its page, its row in the nav,
   its rows in the palette and the label of its key carry one name,
   except the palette's second group ("Needs your decision", its rows
   "Decide: <title>", `usePaletteRows.ts:948` and `:74`), which keeps
   its name until PR-13 makes the two groups one.
4. **One table of pages.** A page's name, its one sentence, its key and
   where it stands are in one table that the nav, the rail, the phone's
   bar, the palette, the welcome and the page's empty state all read.
5. **One shape for nothing.** A page with nothing in it says what it is,
   why it is empty and the one action that fills it; while it reads and
   when its read failed it has the same shape. Unknown is not zero.
6. **Nothing cut that a person must read.** At 1440 x 900, at 1280 x 720
   and at 390 x 844: a name, a label, a count and a choice are whole or
   are cut by a stated rule with the whole text one press away; nothing
   overlaps; nothing scrolls sideways.
7. **Colour means one thing.** Amber: someone must act. Green is for a
   live agent: `working` is the green dot in its ring, `running` (no
   signal) the plain green dot, and the ring alone tells them apart; no
   other state is green. The forest fill: the main action of the view,
   and nothing else. Each text
   token on each surface token meets its stated minimum contrast in both
   themes.
8. **A key and a tap for every control.** Every control a row adds is
   reached and worked by the keyboard alone and by touch alone.
9. **No proof is lost.** A test that moves keeps what it proved; a proof
   that is dropped is named in the row that drops it, with the reason.

### The status table

1. A session, a run's member and a task's attempt are each in one of
   five states, named by one key: `needs` (it asked, or something of it
   waits for a person's decision), `working` (the agent is in a turn),
   `finished` (the agent ended its turn and waits for the next word),
   `running` (the process is alive and its agent reports no turns) and
   `exited` (the process ended). Their marks and words are PR-3's table.
2. Every surface gets a state from one function of
   `web/app/utils/status.ts`: `sessionStatus` for a session,
   `taskStatus` for a task, `memberStatusOf` for a member,
   `runStatusLine` for a run's sentence. No component reads
   `attention.state` or `status` to pick a colour or a word.
3. Every surface draws a state with `StatusMark.vue`, which carries
   `data-status-mark="<key>"` and, on its word, `data-status-word`. The
   mark carries the colour; the word is drawn in the text's colour.
4. Green is for a live agent: `working` is the green dot in its ring,
   `running` (no signal) the plain green dot, and the ring alone tells
   them apart; no other state is green. Amber is for `needs` alone. A
   task's own states (waiting, ready, done, failed, cancelled) have
   marks that are none of the five; a task that is done is drawn in
   neutral ink with its check.
5. `starting` takes the running mark with the word "starting". A host
   away that asks is `needs` ("needs you · host away"), counted and
   listed in Needs you (PR-4, PR-14); one that does not ask takes the
   exited mark with "host away" and is listed with Exited.
6. What the app cannot guess, the server says (PR-2): a shown `done`
   gives way at the Enter that begins the next turn, and `Info.turns`
   says that the agent reports its turns. An agent that reports nothing
   is `running`, and the legend says so.

## What is already done or under way

What of the UX review is on `main` or in an open pull request, where
every other proposal and defect lands, and what must merge before a row
is cut.

### Where each proposal lands

Each proposal of the UX review (of `main` at `40eccb5f`, with P22, P23
and P24 added at `cf6de69a`) and each of its thirteen defects, with the
row of this stream that builds it, or the open pull request that
already does. A proposal cut in two lands in two rows; the first named
builds the part a person meets first. No proposal is done on `main` as
a whole; where a part of one is, the line says so.

| Id | What it is | Where it lands |
|---|---|---|
| P1 | One status language: five states, one mark and one word each, the same on every surface | PR-3 (sessions) and PR-10 (runs and tasks), on PR-2's turn signal |
| P2 | One amber number in the nav, on Needs you | PR-5, reading PR-4's one list and one number, whole: until PR-13 lists every kind in the sidebar, the section's header says what it leaves out ("Needs you 6 · +3 on the page"), and PR-13 removes the words |
| P3 | One inbox: the sidebar's Needs you holds every kind, under one name | PR-5 (the name, Needs you) and PR-13 (the list). On `main` already: a run's open decision that no shown session mirrors is listed there (`loneDecisions`, `SessionSidebar.vue`) |
| P4 | Finished apart from Working in the sidebar | PR-14 |
| P5 | Sidebar rows that fit 200 px: the slim header, the row, a question on two lines and whole beside its row | PR-7 (gutters, the row, the header) and PR-8 (the two-line question and its panel, together) |
| P6 | The pages as six tiles at the sidebar's foot | PR-15 |
| P7 | A run header that fits, and task rows that say the state once | PR-9 (the header) and PR-11 (the rows) |
| P8 | One toolbar on a Dispatch project, and columns that line up | PR-12 |
| P9 | A ticket's strip of sections; a plan folds once approved | PR-16 |
| P10 | Yard tiles lead with the name; the state at the foot | PR-17. Its third point (the queue's choices wrap) is on `main` (#908), and goes with the queue in PR-18 |
| P11 | A session page where the terminal has the room | PR-19 |
| P12 | Fewer places to look | PR-17 (the Roundhouse a view of the Yard), PR-27 (hooks and the skill to Agents), PR-28 (the rules to Alerts) and PR-29 (the feed into History; Events retired) |
| P13 | Signposts: the nav's tooltips, the palette's page lines, an agent card's report line, the way to the rules | PR-4 (tooltips and page lines, from `utils/pages.ts`), PR-27 (the card's line) and PR-28 (Alerts opens the rules) |
| P14 | The brand's own neutrals in place of zinc | PR-24 |
| P15 | A run drawn as a line diagram | PR-32 (the layout), PR-33 (the Graph view's Tasks mode) and PR-34 (the small drawing in the Crews table and on a run's card) |
| P16 | One colour for the main action; mono for evidence; amber only when someone must act | PR-26 (the rules and their checks, Open run and Copy outline, the code font, amber that asks nothing); Approve and Retry in forest PR-21, Share as an outline button PR-19, Stop neutral at rest PR-9 |
| P17 | A welcome that teaches | PR-30 |
| P18 | A page to connect, in place of a dialog over the app | PR-31. The dialog's corrected words are on `main` (#908, D6) |
| P19 | Launch with a first message | PR-35 (the server) and PR-36 (the dialog) |
| P20 | A design system that rules | PR-37 (the rules in `docs/design/system.md` and their checks) and PR-38 (the capture in the repository). Its third point, the light theme's status text, is PR-24's; the contrast as drawn is PR-25's |
| P21 | Needs you's item: the question, one set of buttons, the fine print folded, a person's words | PR-20 (a run's decision card) and PR-21 (the list and the item). On `main` already: the plain line of what an approval lets start is the server's own (`gateRequest`, `internal/crew/decisions_wait.go`); #912 makes it right for one task and for several |
| P22 | The Yard is its grid: the queue goes | PR-18 |
| P23 | One shape for a page with nothing in it | PR-22 (the shape; home, the Yard, the Roundhouse, the sidebar's line, History) and PR-23 (the other pages, and About this page) |
| P24 | Needs you at zero: a page worth landing on, and a row that leaves | PR-5 (the nav's row hides at zero) and PR-6 (the page) |
| D1 | A long title runs out of its row in the Decide list | #912 (`INBOX_ROW_CLASS`, with `items-stretch`) |
| D2 | The run page's count badges run under its buttons, and the name is cut | PR-9 |
| D3 | The Yard queue cuts a question's third choice | on `main` (#908); the queue itself goes in PR-18 |
| D4 | The welcome's "Load the example crews" reads "Load the ex…" | on `main` (#908); PR-30 replaces the card's content |
| D5 | The sidebar's "Conductor" and "Launch agent" are cut | on `main` (#908), at 268 px; PR-7 keeps both whole at 200 px |
| D6 | The token dialog says the token was printed at start | on `main` (#908); PR-31's page keeps the corrected words |
| D7 | A session the server no longer holds keeps its badge, Share and More, and points nowhere | PR-19 |
| D8 | "Shared with you" is cut, its count wraps, its row's name is cut | on `main` (#908) at 268 px; PR-7 keeps it whole at 200 px |
| D9 | On a phone a session's name is cut to one letter by its header's buttons | PR-19 |
| D10 | A guest's view draws a small grid as very large text | on `main` (#908) |
| D11 | A run's label stops at 60 characters mid-phrase, with no ellipsis | PR-35 (the server's cut) and PR-36 (the app's copy of it, `defaultLabel` in `utils/dispatchPlans.ts`) |
| D12 | An open question prints its title twice | PR-21 |
| D13 | History says "Nothing in the history matches." when nothing was asked | PR-22 |

The stream's shared pieces land with the first row that needs them:
the seeded day and the layout checks in PR-1, the turn signal in PR-2,
`utils/status.ts`, `StatusMark.vue` and `useNow()` in PR-3,
`utils/pages.ts` and `utils/needsYou.ts` in PR-4, `kindLook` and
`OpenOnPhone.vue` in PR-6, `PageEmpty.vue` and `utils/pageState.ts` in
PR-22, and `utils/contrast.ts` in PR-24.

### What must merge first

Read on 2026-11-15 with `gh pr view` and `gh pr diff`: seven pull
requests held files that rows of this stream change. One, #908, has
merged since (below); six are open, drafts against `main`. A row is
not cut on a file an open pull request holds; it is cut once that pull
request has merged, and it builds on what the pull request made (its
widths, its words, its tests). Where a row and an open pull request
share a file only at lines far apart, the row does not wait, and the
list after the pull requests says so.

#### #899: crews, a ticket's launch and Brakeman on their accounts

`Fixes #787`. It holds, of what this stream needs:
`CrewRunHeader.vue` (a Logins popover in the header's bar, which PR-9
moves into the header's second row), `pages/crews/[[id]].vue` (the
launch sheet; one of its edits is the line above the Crews page's
`UEmpty`, which PR-23 replaces, PR-30 writes under its "Load the
examples", and PR-34 draws in the Crews table), `utils/crews.ts`
(PR-30's line, PR-34's `summaryOf`), `composables/useDispatch.ts` and
`DispatchLaunch.vue` (the launch the plan opens, which PR-16 folds),
`composables/useSessions.ts`, `composables/usePaletteRows.ts`, and the
specs `token-fragment.spec.ts` (PR-31), `crews.spec.ts`,
`palette.spec.ts` and `examples.spec.ts`, with `web/e2e/fixtures.ts`
and `stub-agent.sh`.

Rows that wait for it: PR-9, PR-23 and PR-31; through them PR-11,
PR-26, PR-29, PR-30, PR-33, PR-34, PR-37 and PR-38.

#### #907: the touch terminal's paste control

`Fixes #902`. The pull request is being narrowed in review: Conductor
types nothing into a startup screen, and what stays is the touch
terminal's paste control, a button in the question bar that opens it,
their tests and the docs, with no file under `internal/` or `ext/`.
Its list is read again when it merges. Of what it holds today, the
app's files and the docs are: `docs/protocol.md`,
`pages/sessions/[id].vue`, `QuickReplyBar.vue`, `utils/attention.ts`
and its test, `TerminalView.vue` (PR-19; PR-3 draws the session's mark
in the page's header), `pages/yard.vue` (PR-3, PR-17, PR-18 and
PR-22), `pages/join/[token].vue` (PR-3), `composables/useQuickReply.ts`,
`composables/useSessions.ts`, `utils/protocol.ts`, `utils/reply.ts`,
`utils/terminalPaste.ts` and its test, and the e2e's
`agent-signin.spec.ts`, `fixtures.ts`, `stub-agent.sh` and
`conductor.e2e.json`. No row of this stream waits for it for a file of
the server: PR-2 and PR-35 change `internal/session` and
`internal/api` on `main` as it is.

Rows that wait for it, for the app's files they share with it: PR-3,
PR-17, PR-18, PR-19 and PR-22; through them PR-7 to PR-11, PR-13 to
PR-16, PR-23, PR-26, PR-28 to PR-31, PR-33, PR-34 and PR-36 to PR-38.

#### #908: the sidebar opens wide enough for its header, and six screen texts (merged)

Merged on 2026-11-15 (`f3d39b1f`), after the code was read at
`9fb98b06`; no row waits for it now, and the rows build on what it
brought. It closed #702 and #706, and was `Part of #906`, whose rows
1, 6, 8, 10 and 11 stay open. It changed `layouts/default.vue` (the
sidebar's header and the nav), `utils/sidebar.ts` and its test
(`SIDEBAR_SIZE`), `SessionSidebar.vue`, `SidebarSection.vue`,
`SidebarSharedList.vue`, `WallQueue.vue`, `WelcomeCard.vue`,
`WorkbenchTokenGate.vue`, `TerminalView.vue`, `utils/terminalSizer.ts`,
`docs/features.md`, `README.md`, and the specs `sidebar.spec.ts`,
`choices.spec.ts`, `question.spec.ts`, `sizing.spec.ts` and
`welcome.spec.ts` (new). It fixed D3, D4, D5, D6, D8 and D10, rows 2,
3, 4, 5, 7 and 9 of #906. What the rows build on: the 268 px minimum
and `SIDEBAR_SIZE` (PR-7 returns it to 200 px), the welcome card with
its buttons one to a row and `welcome.spec.ts` (PR-22, PR-30), the
token dialog's corrected words (PR-31), the Yard queue's choices that
wrap and their titles (PR-18 moves them to the reply bar), "Shared
with you" whole (PR-7), and a view sized by another window drawn at
its size (D10).

It raised the sidebar's minimum and opening width from 200 px to
268 px (16.75 rem), and the drag's most to never less than 348 px, so
that today's header (the mark, the name, Alerts, your menu, collapse)
is whole. PR-7 returns the minimum and the opening width to 200 px
with the header slimmed to the mark, the name and your menu. By
#908's own measures at 1440 wide (the header's row 225 px, its
padding and border 33 px more), that header, without Alerts (about
26 px with its gap) and the collapse button (about 30 px) and with the
header's padding at 8 px a side as PR-7 sets it, needs about 186 px:
14 px to spare at 200 px, more than the 10 px #908 keeps at 268, and
PR-7's spec asks for at least 8 px. #908's header tests, its
wider-font case among them, move with PR-7 to the new header at 200
px, and its two "Shared with you" tests run at the new minimum. The
queue test #908 added to `choices.spec.ts` goes with the queue in
PR-18, and `welcome.spec.ts` stays as PR-30's base.

#### #909: the package's source, steps to reproduce and evidence

`Fixes #770`. Go only. It holds `internal/dispatch/launches.go` (it
edits the caller of `defaultLabel`; PR-35 changes the cut itself),
`internal/dispatch/model.go`, `internal/crew` files, `internal/quoted`
(new) and `docs/protocol.md`. Nothing on the app's screens.

Rows that wait for it: PR-35; through it PR-36.

#### #911: the Agents page draws its list first

`Fixes #704`. It holds `pages/agents.vue` (the page's read, and the
card's badge block, which gains a Checking badge), `utils/agents.ts`,
`composables/useSessions.ts`, `internal/api/catalog.go` (the catalog
answers at once and each agent is read after it) and
`agents-checking.spec.ts` (new). PR-27 puts an agent's report line
and Install in that badge block, PR-26 turns its "Not installed"
neutral, PR-23 gives the page the reading and empty shape, and PR-30
reads the agents found and their versions from the same catalog read.

Rows that wait for it: PR-23, PR-26, PR-27 and PR-30; through them
PR-28, PR-29, PR-37 and PR-38.

#### #912: the decision page asks for its token, a long title stays in its row

`Part of #653`. It holds `InboxItem.vue` (the heading's time),
`JoinDecide.vue`, `pages/decisions.vue` (the token asked at mount, the
rows' class), `utils/inbox.ts` (`INBOX_ROW_CLASS`, `itemTimeWords`),
`SidebarSessionRow.vue` and `ResumeButton.vue`
(`sessionResumeOffered`), `CrewGraph.vue`, `CrewGraphNode.vue`,
`CrewGraphList.vue`, `pages/runs/[run].vue` and `utils/runs.ts`
(`memberOffer`) with `utils/runs.test.ts`, `CrewRunHeader.vue` (Add
agent offered by `runOffer`), `utils/tasks.ts` and `tasks.test.ts`,
`DispatchPlanBody.vue` and `utils/dispatchPlans.ts` (a member's
model), `composables/useDispatch.ts`, `composables/useSessions.ts`,
`composables/usePaletteRows.ts`, `internal/api/inbox.go`,
`internal/api/people.go`, `internal/crew/decisions_wait.go` (the
gate's words), and the specs `decisions-ask.spec.ts` (new: its browser
stub of the list is the base of PR-1's `inbox-stub.ts`) and
`run-start-now.spec.ts` (new). It fixes D1.

Rows that wait for it: PR-3, PR-6, PR-9, PR-10, PR-16, PR-20, PR-24
and PR-31; through them PR-7, PR-8, PR-11, PR-13 to PR-15, PR-17 to
PR-19, PR-21, PR-23, PR-25, PR-26, PR-28 to PR-30, PR-33, PR-34 and
PR-36 to PR-38.

#### #913: a resume waits for the process of a session whose stop failed

`Fixes #728`. Go only. It holds `internal/api/sessions.go`
(`createSessionRequest`, where PR-35 adds the launch's `message`),
`internal/api/resume.go` and `docs/protocol.md`.

Rows that wait for it: PR-35; through it PR-36.

#### Waits through a file alone

Five rows wait for an open pull request through a file they change,
not through a row before them: PR-9 for #912 (`CrewRunHeader.vue`,
where #912 offers Add agent by `runOffer`), PR-22 for #907 (the Yard's
empty state, a few lines from #907's edit of the focused tile), PR-23
for #899 (the Crews page's empty state) and #911 (the Agents page's
reading), PR-24 for #912 (the sidebar's surfaces take the new
neutrals) and PR-26 for #911 (the Agents card's badge).

#### Shared at lines apart: no wait

- `web/e2e/fixtures.ts`: #899 adds three lines and #907 one; PR-1 adds
  `dayPage` and the one `budget()`. PR-1 waits for neither.
- `composables/usePaletteRows.ts`: #899 (a crew's launch opens its
  sheet) and #912 (Resume only where it is offered) change rows that
  PR-4 does not; PR-4 adds the page rows' line.
- `composables/useSessions.ts` is held by #899, #907, #911 and #912 at
  once; PR-2 adds `turns` to the `Info` type in a line of its own, and
  PR-35 two type lines.
- `docs/protocol.md` is held by #901, #907, #909, #911, #912 and #913;
  PR-2 adds its part on `main` as it is, and PR-35 after #909 and #913.
- `pages/join/[token].vue`: #907's edits are in other lines; PR-5
  renames the decision link's tab.
- `pages/decisions.vue` (#912) and `pages/yard.vue` (#907) for PR-5's
  name and chip tooltip; `composables/usePaletteRows.ts` for PR-13,
  PR-14, PR-27 and PR-30; `internal/api/server.go` (#901, #911) for
  PR-35's seam.
- `internal/session/local.go`: #901 (`Fixes #900`) edits `ResizeWith`
  only, and holds no other file of this stream.
- #888 (Nuxt) and #850 (node-forge), from Dependabot, change only
  `web/package.json` and its lock. No row of this stream changes a
  dependency.

If #912 is still open when PR-1 is cut, PR-1 writes `inbox-stub.ts` on
its own, and the first row cut after #912 merges moves
`decisions-ask.spec.ts`'s own stub onto it.

#### Each row and what it waits for

Directly or through a row before it:

- Nothing open: PR-1, PR-2, PR-4, PR-5, PR-12, PR-32 and PR-39.
- #912 alone: PR-6, PR-20, PR-21, PR-24 and PR-25.
- #907 alone: PR-22.
- #911 alone: PR-27.
- #909 and #913: PR-35.
- #907 and #912: PR-3, PR-7, PR-8, PR-10, PR-13, PR-14, PR-15, PR-16,
  PR-17, PR-18 and PR-19.
- #899, #907 and #912: PR-9, PR-11, PR-33 and PR-34.
- #907, #911 and #912: PR-28.
- #899, #907, #911 and #912: PR-23, PR-26, PR-29, PR-30, PR-31, PR-37
  and PR-38.
- #907, #909, #912 and #913: PR-36.
- Issue #716 fixed: PR-3, and through it PR-7 to PR-11, PR-13 to
  PR-19, PR-26, PR-28, PR-29, PR-33, PR-34 and PR-36 to PR-38.

The order of merges that frees the most rows soonest, now that #908
(the sidebar, the nav, the welcome, the queue) has merged: #912 (Needs
you's page, the run page's graph), then #907 (the session page and the
Yard: twelve rows more, with #716 fixed), then #899 (the run header,
Crews), then #911 (Agents), then #909 and #913 (for PR-35). PR-1 goes
first, with PR-39 beside it; PR-2, PR-4, PR-5, PR-12 and PR-32 can
follow before any of the six open pull requests merges.

#### The issues this stream answers or leaves

**#906**, eleven small defects of the app's screens, met in the same
review. Each of its rows and who takes it:

| #906 row | Defect | Taken by |
|---|---|---|
| 1 | D2: the run page's badges under its buttons | PR-9 |
| 2 | D3: the Yard queue's third choice | #908 (merged) |
| 3 | D4: "Load the ex…" | #908 (merged) |
| 4 | D5: "C…" and "Launch ag…" | #908 (merged) |
| 5 | D6: the token "printed at start" | #908 (merged) |
| 6 | D7: a session the server no longer holds | PR-19 |
| 7 | D8: "SHARED WITH Y…" | #908 (merged) |
| 8 | D9: a phone's session name cut to one letter | PR-19 |
| 9 | D10: a guest's very large text | #908 (merged) |
| 10 | D11: a run's label cut mid-phrase | PR-35 and PR-36 |
| 11 | D12: an open question's title twice | PR-21 |

#908, merged, was `Part of #906`; #906 keeps rows 1, 6, 8, 10 and 11
open. PR-9, PR-19, PR-21, PR-35 and PR-36 each say `Part of #906`, and
the last of them to merge says `Fixes #906`. The
review's twelfth defect, D1, is #912's, under #653, as #906 says. D13
is in no issue: PR-22's issue names it.

**#374**, the welcome card's cut label, what each choice does, and a
tooltip on every button. Its first point (no label cut) is on `main`
(#908, D4).
Its second (each choice says what happens) is PR-30's, for the
welcome's steps and for the two places the issue names beside it:
Crews' "Load the examples" and its palette row. Its third (a tooltip
on every button, written down as a rule) is PR-37's, as a rule with a
check; PR-4's tooltips on the nav are its first part. PR-30 says
`Part of #374` and PR-37 says `Fixes #374`.

**#449**, every crew graph sharing one Vue Flow store. PR-33 says
`Fixes #449`: the Graph view's Tasks mode leaves Vue Flow, and Members
mode and the crew editor keep it, so PR-33 gives each graph its own
store in the same change, with the issue's test of two graphs mounted
at once.

**#716**, Codex's tool-approval prompt shown as working. A defect
outside this stream: its fix is in the server's reading of Codex's
screen, in no file of this stream. The plan asks that it be fixed
before PR-3 merges, because PR-3 draws "working" with a surer mark and
word than today, and a question read as work would then look more
certain than it is.

**#708** (search in the keyboard shortcuts dialog) and **#346** (themes
as files, and a colour per agent) are not in this stream.

Other issues the stream meets: #139 (the sidebar, watched in use) is
walked again once PR-8, PR-13 and PR-14 have merged; #151 (progress
reported by the agents) finds its place in PR-9's bar and sentence;
#437 (closed) set the 200 px width that PR-7 returns to; #660 and #661
(resume by id from the Launch dialog) share the dialog with PR-36 and
are not in this stream; #653 is #912's; a flake that a row of the
stream meets once goes as a line into #281.

#### The Program's other rows on the same screens

Ready Program rows, not yet pull requests, plan changes on screens
this stream reshapes. The owner's order of 2026-11-14 puts the app's
screens before new feature rows: the stream's row on a screen goes
first, and the feature row builds on what it made, reading its own
plan's words about a header, a task row or a section against the new
one. If a feature row is under way when the stream's row is cut, the
one cut second rebases on the other.

| Issue | Program row | What it adds | Meets |
|---|---|---|---|
| #773 | IN1a PR-9 | a GitHub reference in the run page's header, after "Launched from"; Steps to reproduce in a plan's proposal and body | PR-9 (the reference sits in the header's one ticket chip) and PR-16 (the folded plan's one line) |
| #808 | CP1 PR-4 | links to a comparison in the run header and on the run card; lanes in `RunTasks`; a ticket's comparison view; New comparison on Crews | PR-9, PR-10 (the run card), PR-11 (the task table) and PR-16 (the strip) |
| #812 | CP1 PR-7 | in the decision card (`DecisionCard`, `DecisionRespond`) an option's subject and a required reason; "selected" and "not selected" on the run page's lanes and `DispatchCandidates` | PR-20, PR-21 and PR-11 |
| #814 | CP1 PR-8b | Review the selected candidate, from the ticket page's Decisions | PR-16 (the strip's Decisions) and PR-21 |
| #815 | CP1 PR-9 | a launch's sessions per account in `DispatchLaunch` and `CrewLaunchSheet` | no file of a row; it sits behind the plan PR-16 folds |
| #792 | PV1 PR-4 | Open in Conductor on `RunTasks` and the palette's task rows; the run page's New tab | PR-11 (the row's menu holds it) and PR-10 (the palette's task rows) |
| #796 | PV1 PR-8 | Preview this candidate on `RunTasks`, `DispatchCandidates` and the palette's task rows | PR-11 and PR-16 |
| #772 | IN1a PR-8 | Import from GitHub on a project and in the palette's `>` group; the GitHub panel and its words on a ticket | PR-12 (Import joins the project's one toolbar), PR-16 (the strip names the panel) and PR-23 (a project's empty state) |

## The changes

Each row is one issue, one branch and one pull request. Each row is cut
once every row and open pull request it depends on has merged; of two
rows free at once, the lower number first. PR-10 goes before PR-9
because PR-9 depends on it (its header sentence is PR-10's
`runStatusLine`); PR-9 keeps its number.

### PR-1: e2e: a seeded day, layout assertions, one theme and viewport helper (X-seeded-day, X-layout-assertions, X-keys-and-touch)

Nothing a person sees changes. The suite gains the ground every later
row tests on: a day of work on a server of its own, built in parts and
read by name; one module of layout measures; two stubs of the app's
reads; one way to open a page at a size, a theme and with touch; one
`budget()`. One existing check that ran dark twice runs light.

**Today.** Every spec shares one server
(`web/e2e/global-setup.ts:16-44`) and one worker
(`web/playwright.config.ts:10-11`), so specs count by shape
(`sidebar.spec.ts:105-106`), and twelve of them end every session they
did not make (`stopStrays`, `fixtures.ts:175-184`). Nineteen spec files
start a server of their own; `decisionServer`
(`decisions-helpers.ts:66-103`) is the general form, used by five. The
stub ends every turn at once (`answer` in `stub-agent.sh` calls
`report_working`, then `report_done`), so nothing on a fresh server is
working unless a test holds a spin, and an exited session leaves the
list after ten minutes (`internal/config/config.go:790`). Layout checks
live inside the specs that need them (`hscroll.spec.ts:17-31`,
`yardheader.spec.ts:17-38`, `fitsSidebar` at `sidebar.spec.ts:750-761`,
`sideways` in `dispatch-helpers.ts`), and none checks that a text is
whole. Nineteen e2e files hold a `budget()` of their own, one of them
exported (`decisions-helpers.ts:29`). The app prefers
dark and keeps its choice under `conductor-color-mode`
(`nuxt.config.ts:22`), but `rail-squares.spec.ts:75` writes
`nuxt-color-mode`, so its light pass reads the dark colour again.

**The change.**

- `web/e2e/own-server.ts` (new): `ownServer(name, { agents, repoPath,
  env })`, the body of `decisionServer` moved, with its `restart` and
  its `stop` that copies the log into `test-results`. Two options are
  new: `repoPath` puts the scratch repository at a nested path about as
  long as a real checkout's (about 40 characters under the server's
  root), so that a cut in a meta line is measured against a real length;
  `env` is set over the server's own (`startServer` already takes it).
  `ownServer` also takes `hostToken` and passes it to `startServer`
  (`CONDUCTOR_HOST_TOKENS`, `server.ts:190`), and the day's server is
  started with one, so that a spec can bring in a hosted session.
  `decisions-helpers.ts` keeps `decisionServer` as a call of
  `ownServer`, so its five callers do not change.
- `web/e2e/stub-agent.sh`: `STUB_TURN`, read at start, and its lines in
  the header comment. `finished` reports one turn's start and end
  through the agent's hooks, as a typed line does. `working` reports the
  start and holds the turn, reading and drawing nothing, until
  `$HOME/.stub-sessions/<id>.hold` says `stop`.
- `web/e2e/day.ts` (new): the day in the parts the chapter "Tests" lists
  (`states`, `dispatch`, `waiting`, `runs`, `long`, `busy`).
  `part(name)` builds a part once and returns its names mapped to ids; a
  part builds the parts it stands on first. Each wait of a part polls
  the API for what it made (a session's attention, a run's task state,
  an inbox ref) and adds its cap to the running test's timeout. The
  working, finished and run-member agents are copies of `claude` and
  `worker-cli` with `STUB_TURN`, the question agents copies of `asker`
  and `asker-long`, the clarifier and the approver copies of
  `worker-cli` with `STUB_DECISION`; the server runs with
  `CONDUCTOR_EXITED_RETENTION=24h`, the repository at `repoPath`, and
  Brakeman not configured (its role wakes on a failed task and adds a
  request, `brakeman-role.spec.ts`). `leftovers()` lists what the server
  holds that no part made; `end()` stops the runs and the sessions,
  writes `stop` into every hold file, then stops the server, copies its
  log to `test-results/day-server.log` and removes the root, in that
  order (AGENTS.md: end what a test started before its directories go).
- `web/e2e/fixtures.ts`: a worker fixture that starts the day's server
  on a worker's first use and calls `end()` at the worker's end; the
  test fixture `day` over it, which after each test that used it ends
  any of `leftovers()` and fails that test naming them, then rereads,
  by name, every part already built in the state that part promises
  (the reads of `day.spec.ts`) and fails the test that changed one,
  naming the item;
  `dayPage(browser, { path, width, height, theme, touch, server, token,
  ready })`, which sets the workbench token, the display name "Nate"
  and `conductor-color-mode` before load, `hasTouch` and `isMobile` with
  `touch`, goes to `path` and returns once the layout's root is drawn
  (`#dashboard-sidebar-main`, or `[data-bottom-bar]` below 1024 px),
  `html` has the theme's class and `document.fonts.ready` has settled.
  `token: false` sets no workbench token, and `ready` (a selector)
  replaces the layout's root as what it waits for. The theme is set
  through the context's `storageState` (or an init script that writes
  the key only when it is absent), so `useTheme`'s write survives its
  reload. Every context `dayPage` opens is closed at the end of its
  test, before the `day` guard runs. Then `useTheme(page, theme)` for a
  page already open; and `budget(ms)`, exported, which
  `decisions-helpers.ts` re-exports in place of its own. The eighteen
  other copies stay until a row is in their file anyway.
- `web/e2e/layout.ts` (new): `uncut`, `inside`, `noOverlap`, `aligned`,
  `rowsInView`, `topAtMost`, `lines`, `noSideways`, `tapTarget`,
  `tabTo`, `atWidths`, `ageBy` and `probeColour`, each as the chapter
  "Tests" says: each fails on no match, waits for the fonts, keeps its
  slack in one constant and returns what it measured.
- `web/e2e/inbox-stub.ts` (new): `inboxItem(kind, over)` for a question,
  a plan, a task and a decision, and `stubInbox(page, items)`, answering
  `GET /api/inbox`, `/api/inbox/item`, `/api/inbox/track`,
  `/api/decisions` and `/api/decisions/{id}` in the browser.
- `web/e2e/run-fixtures.ts` (new): `runInfo({ tasks, states })` with 6,
  40 and 128 tasks (every task state at once when `states` is left out),
  and `serveRun(page, run)`, which answers the run page's reads under
  `/api/runs/<id>` (the run, its candidates, its links, a task) and
  `/api/dispatch/links?run=`, answers any other read there with a 404,
  and records every `/api/` request the page makes that it did not
  answer; `stubs.spec.ts` fails on any that names the run. The run's
  decisions are answered through `stubInbox`'s `/api/decisions`.
- `web/e2e/rail-squares.spec.ts`: `useTheme(page, mode)` in place of the
  `nuxt-color-mode` write, and the class of `html` asserted before the
  bar's colour is read.
- `docs/checks.md`: one paragraph: the day, its parts, its two rules
  (read-only, held by the `day` fixture; a measure of how much fits is
  made on `busy`), its cost as measured on CI, how to open it by hand
  (`CONDUCTOR_E2E_DAY_OPEN=1 npx playwright test day.spec.ts -g open`,
  which prints the address and the path of a file holding the token),
  and that a `colorScheme` context does not switch this app.

**Tests.**

- `web/e2e/day.spec.ts` (Playwright, new, the day's server, `busy`):
  each part holds what it promises, read through the API by name: the
  five sessions of `states` with attention `needs_input`, `working`,
  `done`, none and status `exited`; the inbox's refs by kind, exactly
  those the parts made; Paging spike's `index` failed with three
  attempts; Ledger migration's six task states; Release notes succeeded;
  the fourteen tickets by key with their owners; the lengths of `long`.
  Without the change: run once with `STUB_TURN` taken off the finished
  agent's copy, it fails naming `flaky-vim-spec` with attention none
  where `done` was promised (quoted in the PR body).
- `web/e2e/day.spec.ts`: the states hold. A page of the Yard on `busy`
  attaches every session, the test waits for each live session's
  `viewers` in `GET /api/sessions` to be above 0, then reads the five
  states again and finds them unchanged (AGENTS.md: wait for a state
  that holds). Without: a viewer's attach that cleared a shown done (a
  resize read as a new turn) fails the second read.
- `web/e2e/day.spec.ts`: the guard. A session the test makes on the
  day's server is named by `leftovers()`; stopped and polled until
  exited, it is still named (an exited session stays listed on the
  day); removed with a second DELETE, it is not. Without: a
  `leftovers()` that reads only live sessions fails the second step.
  Then a day session's state is changed through the API and set back:
  the guard's reread names it at the change. Without: the guard catches
  only extra items.
- `web/e2e/day.spec.ts`: `dayPage`. With `theme: 'light'` `html` has no
  `dark` class, with `'dark'` it has; at 390 x 844 with touch
  `matchMedia('(pointer: coarse)')` matches and
  `navigator.maxTouchPoints` is above 0, at 1440 x 900 neither. Without:
  writing `nuxt-color-mode`, the key `rail-squares.spec.ts` used, leaves
  `html` dark and the light case fails.
- `web/e2e/layout.spec.ts` (Playwright, new, `page.setContent`, no
  server): each helper passes on a good page and fails on a bad one,
  with its
  measure in the message: `uncut` on "Conductor" in a 40 px `truncate`
  box; `inside` on a title in a `flex-col` button with no
  `items-stretch` (the markup of `pages/decisions.vue:315-325`);
  `noOverlap` on two buttons 4 px into each other; `aligned` on a column
  3 px off; `rowsInView` on a list one row short; `topAtMost` 10 px too
  low; `lines` on three lines where two are allowed; `tapTarget` on a 32
  px button; `tabTo` on a button with `outline: none` and no shadow;
  every helper on a locator that matches nothing; `atWidths` running at
  each size it names; `ageBy` moving `Date.now()` by its offset.
  Without: `uncut` reduced to a containment check passes the cut label
  and its case fails; each case is the proof of one helper.
- `web/e2e/stubs.spec.ts` (Playwright, new, the day's server): the run
  page drawn from `serveRun` with 6 and with 40 tasks shows every task
  row by id, and the module recorded no read of the run it did not
  answer; an `inboxItem`
  of each kind carries every key the day's real item of that kind
  carries in `GET /api/inbox`. Without: a read the run page gains, or a
  field the server adds to an item, fails here, not in a layout spec
  that would blame the layout.
- `web/e2e/rail-squares.spec.ts` (changed): the class of `html` is
  asserted for each mode before the colour is read. Without the fix: the
  light pass fails at the class, `html` still `dark`, which is the proof
  that the old pass was dark twice.
- `web/e2e/theme.spec.ts` (unchanged) stays the proof that a light
  system opens dark. The five specs on `decisionServer` run unchanged
  over `ownServer`. No assertion moves and no proof is lost.

Each new spec runs with `--repeat-each 10` and once beside a Go race
package as load; the PR body gives the day's build time on CI and the
e2e job's minutes before and after.

**By hand.** Open the day once (`CONDUCTOR_E2E_DAY_OPEN=1`) and look
that it is the day the chapter "Tests" describes: eighteen live sessions
on the Yard, the waiting items on Decide, the two runs, the CND board
with its fourteen tickets.

**Size.** No app code and about 25 lines of docs; about 1,400 lines of
tests: `day.ts` 380, `layout.ts` 220, `layout.spec.ts` 180,
`day.spec.ts` 140, `run-fixtures.ts` 130, `own-server.ts` 100 (80 of
them moved), `fixtures.ts` 100, `inbox-stub.ts` 70, `stubs.spec.ts` 70,
`stub-agent.sh` 25, `rail-squares.spec.ts` 6.

**Defaults.**

- **Working and finished on the day.** Default: a new stub variable,
  `STUB_TURN` (finished: the hook reports a turn's start and end;
  working: the start reported and the turn held until a hold file says
  stop), not the operator's `POST /api/sessions/{id}/attention`. Why:
  it proves the agent's report path that PR-2 and PR-3 rest on, needs
  no browser typing and holds with no timer; a posted state proves only
  the post.
- **The day in parts.** Default: light named parts built on demand
  (`states`, `dispatch`, `waiting`, `runs`, `long`, `busy`), and any
  measure of how much fits made on `busy`. Why: specs that read by name
  pay seconds, and the rule keeps a measure from depending on the parts
  earlier specs built. Alternative: always build the whole day.
- **One day per worker.** Default: one day a worker, shared and
  read-only, with the test fixture `day` ending and failing on anything
  a test left behind or changed. Alternative: a server per spec file
  (cleaner, one to two minutes more a file).
- **The two browser stubs in PR-1.** Default: `inbox-stub.ts` and
  `run-fixtures.ts` land in PR-1, not with their first user. Why: they
  depend on nothing, every later row can count on them, and
  `stubs.spec.ts` holds them to the real server.
- **If #912 is still open.** Default: PR-1 writes `inbox-stub.ts` on its
  own, and the first row cut after #912 merges moves
  `decisions-ask.spec.ts`'s local stub onto it. Why: PR-1 waits for
  nothing. Alternative: PR-1 waits for #912.
- **Exited sessions kept a day.** Default: the day's server sets
  `CONDUCTOR_EXITED_RETENTION=24h`. Why: the default ten minutes would
  drop the exited session in the middle of a run.
- **A task in "checking".** Default: it comes from `run-fixtures.ts`,
  not the day. Why: a check's hold is capped at 3,600 s and would lapse
  inside a long local run.
- **Themes in the tests.** Default: layout is asserted in the dark theme
  only; both themes run only where colour is asserted (the walk's marks,
  contrast, PR-24 and PR-26). Why: the themes share every box.
- **The phone's size.** Default: 390 x 844 with touch, the suite's size,
  not the review's 400. Why: 390 is the stricter.
- **Old helpers.** Default: the eighteen local `budget()` copies and the
  private layout helpers (`fitsSidebar`, `sideways`) stay until a row is
  in their file anyway. Why: PR-1 then collides with no open pull
  request.
- **Contrast floors.** Default: 4.5:1 for text, 3:1 for a mark or a
  status bar against its panel, and for dimmed text 3:1 on the page and
  2.7:1 on the canvas and raised surfaces (PR-24).
- **The by-hand looks.** Default: each row's section and pull request
  body hold its own; PR-38 writes the standing list into
  `docs/checks.md`. Why: the rows do not all touch `docs/checks.md`.
- **Opening the day by hand.** Default: `CONDUCTOR_E2E_DAY_OPEN=1`
  writes the day's token to a file only its owner reads and prints only
  the file's path, never the token.

**Depends on.** Nothing. Open PRs #899 and #907 add to `fixtures.ts`
and `stub-agent.sh` in hunks apart from this row's; whichever lands
second takes the other in a plain merge.

**Left out.** The status walk (PR-3), the counts spec (PR-5, extended by
PR-13), contrast (PR-24 in vitest, PR-25 in the browser), the look
capture and the standing by-hand list in `docs/checks.md` (PR-38), and
the shards (PR-39). Moving `fitsSidebar` (`sidebar.spec.ts`, which open
PR #908 edits), `sideways` (`dispatch-helpers.ts`) and the eighteen
local `budget()` copies onto the new modules: each moves when a row is
in that file anyway. A task held in "checking" on the day: a check's
hold is capped at 3,600 s (`internal/crew/crew.go:243`) and would lapse
inside a long local run, so that state comes from `run-fixtures.ts`.
Brakeman's brief, which the UX review's day had: it stays off on the
day.

### PR-2: session: a turn's start and end are said in the listing (X-turn-signal)

After this row a session's listing says when a turn begins and whether
its agent reports turns at all. A person sees nothing new yet: PR-3
draws it. What changes underneath is that a `done` no longer stays shown
while the agent works on its next prompt, and that a second `done` at
rest no longer restarts the clock of "finished 12m ago". Attention
states, sources, frames and every other rule of the Attention section
stay as they are.

**Today.** `nextTurn` (`internal/session/local.go:3456-3460`), called
for an Enter typed in the terminal (`:3313-3317`) and for a submission's
Enter (`submit.go:1054`), calls `beginTurn` (`:3474-3483`), which resets
the bell's pace and the turn counter and leaves the shown attention as
it is. So a `done` stays shown until another state arrives, and for an
agent whose adapter reports `done` alone (`Events` of Amp, Pi and
others, `internal/agents/amp.go:59`, `pi.go:74`) none arrives: it reads
`done` through its whole next turn. An answer sets the empty state with
source `input` (`local.go:3697`), and Claude Code's default hooks
(`agents/claude.go:70`) send nothing more until `Stop`, so after an
approved permission a working Claude reads as the empty state. A second
`done` with other words (Claude's `idle_prompt` about a minute after
`Stop`, `notify/mappers.go:299-303`) fails the same-state check
(`local.go:2227`) and is stamped with a new `since` (`:2309-2313`).
`Info` (`info.go:43-81`) has no field that says whether a session's
agent reports turns, and the server's copy of a hosted session stamps
each report on receipt (`internal/signal/hosted.go:1316-1318`).

**The change.**

- `internal/session/info.go`: `Info` gains `Turns bool`
  (`json:"turns,omitempty"`): this session's agent has reported a
  `working` or a `done` of its own, with source `api` (its hooks),
  `title` or `status`. A bell, an OSC notification, the screen pattern
  and an operator's mark (source `admin`) leave it false. It is set in
  `setAttentionLooked` under the session's lock, read with `Attention`
  in one `Info()` snapshot, and never cleared.
- `internal/session/local.go`, the first rule: in `nextTurn`, when the
  shown attention is `done` (not a hold, not the agent's own question
  outlasting its done), the Enter replaces it with the empty state,
  source `input`, through `withdrawShownLocked(SourceInput)` (`:4003`),
  as an Enter withdraws a bell's wait today (`:4598-4604`), and
  broadcasts the `attention` message. The empty state records no
  activity entry (the guard at `:2381`), so no webhook fires and no crew
  reads it as a turn's end.
- `internal/session/local.go`, the second rule: a `done` that follows a
  shown `done` leaves the shown one (its `id`, `since` and words, the
  turn's last words) and is still recorded as its own `attention` entry
  at its own time. The activity log, the webhooks and a crew's reading
  of done entries (`internal/crew/run.go:1925-1946`) see what they see
  today.
- `internal/signal/hosted.go`, the hosted copy: `setAttention` (`:1265`)
  learns `Turns` from the host's reports and from the reports it
  forwards, by the same sources, and keeps a shown `done` against a
  following `done` (its `since`, `id` and words) in both paths. The
  Enter's rule runs on the host's own `Local`, which tells the server
  the empty state as it tells a withdrawn bell (a change of source
  alone is sent, `TestAnAttentionChangeOfSourceAloneIsTold`,
  `internal/hostagent/controlwriter_test.go:399`). No frame and no
  field of the host link change.
- `internal/api/links.go:729-741`: the session of
  `GET /api/join/{token}` gains `turns`, which the guest page reads
  (PR-3 says what it draws from it).
- What crew readiness reads: `readyAt` (`internal/crew/run.go:2786`)
  takes a shown `done` as ready at once. It reads the shown state only
  before a member's first prompt and as a resumed member settles
  (`run.go:1664`, `:1828`). A `done` an Enter has cleared is now the
  empty state there, and the member waits for quiet output (one second
  after two, `run.go:105-111`). Handoffs and broadcasts go by done
  entries, which do not change.
- `docs/protocol.md`: the Attention section (`:1031-1070`) states both
  rules, and its `input` source gains "or ended the turn a `done`
  showed"; the paragraph on `Info`'s fields (`:1418-1426`) gains
  `turns`; the `GET /api/join/{token}` row (`:3039`) gains `turns` in
  `session`; the crew's readiness paragraph (`:3509-3520`) says that a
  `done` an Enter ended is not ready. `Info` is not in `internal/proto`
  and no frame changes, so the protocol's three places are
  `internal/session/info.go`, `web/app/composables/useSessions.ts` and
  `docs/protocol.md`.
- `web/app/composables/useSessions.ts`: `SessionInfo` (`:11`) gains
  `turns?: boolean`, and `JoinInfo.session` (`:326`) picks it. That is
  the app's one line here; PR-3 reads it.
- `internal/session/testdata/status-table.json` (new): the table both
  sides are held to. A row is a name, its steps (`report` with state,
  source and words; `enter`; `answer`), the attention and `turns` the
  session then shows, and the status key PR-3's `sessionStatus` must
  give. Rows: nothing reported (empty, false, `running`); a hook's
  working (`working`); a hook's done (`finished`); done, then an Enter
  (empty with `input`, true, `working`); done, then done with
  `idle_prompt`'s words (the first done, `finished`); working, a hook's
  permission question, answered (empty with `input`, true, `working`); a
  bell's wait, then an Enter (empty with `input`, false, `running`);
  Codex's title (`working`); an operator's mark of working (`working`,
  false).

**Tests.**

- `internal/session/turn_test.go` (Go race test):
  `TestAnEnterAfterADoneBeginsTheTurnShown`. A session shows `done` from
  `SourceAPI`; a control viewer writes `\r`; `Info().Attention` is
  `{state: "", source: "input"}`, the viewer's sink has received an
  `attention` message with the empty state, and the activity log holds
  no new entry. The same through `Submit` (a crew's next prompt).
  Without the change: the state stays `done`.
- `internal/session/turn_test.go` (Go race test):
  `TestADoneAfterADoneKeepsTheFirst`. `done` with Stop's words, then
  `done` with "Claude is waiting for your input": `Since`, `ID` and
  `Message` are the first's, and the log holds two attention entries
  with two stamps. No clock is read: the test compares the stamps the
  session made. Without: `Since` and `Message` are the second's.
- `internal/session/turn_test.go` (Go race test):
  `TestTurnsIsLearnedFromTheAgentsOwnReports`. A bell's wait, a pattern
  wait and an `admin` working leave `Turns` false; a `working` or `done`
  from `api`, `title` or `status` sets it, and it stays set through an
  Enter and an exit. Without: no such field (it does not compile).
- `internal/session/turn_test.go`, `TestADoneIsNotUndoneByItsTurnsBell`
  (`:26-56`) changes: its last block (an Enter, then the same words) now
  asserts that the Enter shows the empty state and that the next `done`
  shows anew with a later `Since`. Its proof that a bell after a turn's
  done is the ended turn's own is kept.
- `internal/session/status_table_test.go` (Go race test, new):
  `TestTheStatusTable` drives a `Local` through each row of
  `testdata/status-table.json` and compares `Info().Attention.State`,
  `.Source` and `.Turns` with the row. Without either rule, the rows
  "done, then an Enter" and "done, then done" fail by name.
- `internal/signal/signal_test.go` (Go race test):
  `TestTheHostedCopyKeepsTheFirstDone` (a host's `done`, then one with
  other words; and a forwarded `done` over a shown one: the copy keeps
  the first's `Since` and words) and `TestTheHostedCopyLearnsTurns` (a
  host's `working` from `api` sets `Info().Turns`, a bell does not, a
  forwarded `admin` mark does not). Without: the copy stamps the second
  done, and `Turns` stays false.
- `internal/hostagent/agent_test.go` (Go race test), beside `:600`:
  `TestAnEnterAfterADoneIsToldTheServer`. A host's session shows `done`,
  an Enter reaches it through the server, and the server's copy shows
  the empty state with `input`. The test polls the copy's `Info` for
  that state (AGENTS.md: wait on the mechanism) and waits for the host's
  peer to hold the subscription first (`awaitSub`). Without: the copy
  stays `done`.
- `internal/crew/run_test.go` (Go race test):
  `TestAnEnterAfterADoneIsNotReadyAtOnce`. A member's session shows
  `done` and is ready (`readyAt`); a control Enter is written; `readyAt`
  on its `Info` is not ready, and the test's fast `readyWait`
  (`run_test.go:249`) returns once the output is quiet. `TestReadiness`
  (`:1104`) does not change: the rule is the same, its input is not.
  Without: ready at once after the Enter.
- `internal/api/links_test.go` (Go race test, new): a join of a
  session whose agent reported a turn carries `turns: true`, and one
  that reported nothing has no `turns`. Without: the key is absent.

**By hand.** On a machine that has them: a Pi or Amp session reads
`done` at its turn's end and the empty state from the Enter of the next
prompt; a Claude Code session that asked permission and was answered in
the app reads the empty state with `input` until its `Stop`; a minute
after `Stop`, `GET /api/sessions/{id}` still has the `since` of the
`Stop`.

**Size.** About 100 lines of code (Go 75, docs 22, the app 3) and 300 of
tests with the table.

**Defaults.**

- **Who teaches `turns`.** Default: only the agent's own working or done
  reports (sources `api`, `title`, `status`); a bell, an OSC
  notification, the screen pattern and an operator's mark leave it
  false. Why: an operator's mark is a person's word, and a bell says
  nothing of a turn.
- **A done after a done.** Default: the shown one keeps the first's id,
  `since` and words, and the second is still recorded as its own entry.
  Why: "finished N ago" counts from the turn's end, and the crew, the
  webhooks and the log read entries as today.
- **The empty state after an answer.** Default: the empty state with
  source `input` is "working" on a session with `turns` and "running"
  otherwise; an empty state from the screen stays "running". Why:
  `input` is the one source the server sets at a turn's start or an
  answer; the screen is a guess.

**Depends on.** Nothing open: it changes `internal/session` and
`internal/api` on `main` as they are. It shares
`web/app/composables/useSessions.ts` and `docs/protocol.md` with open
#907 at lines apart. Owner decision 3 (the server first) puts this row
before PR-3.

**Left out.** Codex's tool-approval prompt read as working (issue #716)
is a defect outside the stream; the plan asks that it be fixed before
PR-3. Claude's default hooks keep no `PreToolUse`; the `input` mapping
of PR-3 covers the time after an answer without it. The launch's first
message (`Info.firstMessage`) is PR-35's, on top of this row's `Info`.

### PR-3: web: one status language for sessions (P1, X-status-walk)

After this row every place that shows a session's state shows one of
five marks and its word, the same on the sidebar's row, the rail, the
phone's list, a Yard tile, the Yard's bar, the session page, the
Roundhouse, the palette and a guest's page. An agent at work is green
with a ring and says "working"; one that has ended its turn is a grey
ring and says "finished 12m ago". The sidebar's sections, their order
and `data-row-state` stay as they are (PR-14 splits Running), and so do
the Yard's filter chips (PR-17). Runs and tasks keep their own marks
until PR-10.

**Today.** Each surface works the state out for itself, and none reads
`working` against `done`. The sidebar's `rowState`
(`web/app/utils/sidebar.ts:194-198`) gives needs, running, idle or
exited, so working, finished and an agent with no hooks are one green
dot (`SidebarSessionRow.vue:107-109`). The Yard tile says "Running" in
green for any running session (`SessionTile.vue:21-26`). The Yard's bar
draws done in blue (`AttentionBar.vue:17-19`), the session badge draws
done in green with a check and working in grey
(`AttentionBadge.vue:18-19`), and a green "done" event badge sits beside
the name until the session is opened (`utils/events.ts:57`,
`sessions/[id].vue:145-153`). Words in a status colour fail contrast in
the light theme (amber 2.9:1, green 4.0:1, as the UX review measured).

**The change.**

`web/app/utils/status.ts` (new) holds the five states, one mark and one
word each:

| State | Mark | Word | Means |
|---|---|---|---|
| Needs you (`needs`) | amber dot | needs you | It asked, or something of it waits for a person's decision. Nothing moves until someone answers. |
| Working (`working`) | green dot in a green ring | working | The agent is in a turn. |
| Finished (`finished`) | grey ring | finished 12m ago | The agent ended its turn and waits for the next word. It is a person's move, when they want it. |
| Running (`running`) | plain green dot | running | The process is alive and its agent reports no turns, so Conductor cannot tell working from waiting. |
| Exited (`exited`) | dashed grey ring | exited | The process ended. |

A session that is starting takes the running mark with the word
"starting"; one whose host is away and that asks is `needs`, "needs
you · host away"; one whose host is away and that does not ask takes
the exited mark with the words "host away"; one that exited with a code
other than 0 says "exit 1". Green is for a live agent: `working` is the
green dot in its ring, `running` (no signal) the plain green dot, and
the ring alone tells them apart; no other state is green.

- `web/app/utils/status.ts` (new): `type StatusKey`,
  `sessionStatus(session, now)` returning
  `{ key, word, since?, legend }`, and `STATUS_LOOK` (for each key the
  mark, its tone and its legend line, such as "running: the agent
  reports no turns; Conductor cannot tell working from waiting"). The
  rules, in order: ended (`exited`, `stopped`) is `exited`;
  `liveAttention(attention).state` `needs_input` is `needs`, with the
  words "needs you · host away" when the status is `host_disconnected`;
  `host_disconnected` otherwise is the exited mark, "host away";
  `working` is `working`; `done` is `finished`, its age from
  `attention.since` ("finished just now" under a minute, then "finished
  12m ago", "finished 3h ago"); the empty state with source `input` on a
  session with `turns` is `working` (after an answer, or after the Enter
  that began the next turn, PR-2); anything else is `running`,
  "starting" while the status is `starting`. Also `blockStatus(keys)`,
  the most urgent of a run's members for the rail's capsule: needs,
  finished, working, running, exited.
- `web/app/components/StatusMark.vue` (new, about 50 lines): the mark at
  8 px, `data-status-mark="<key>"`, `role="img"` with the word as its
  label and the legend as its title; the word beside it when `word` is
  set, in `data-status-word`, in muted or default text, never in a
  status colour; a `quiet` prop that hides the amber dot when the Events
  page's Badge route for needs input is off (`needsDotShown`,
  `sidebar.ts:127`), as today. The component has two forms that read
  one table: this HTML form, the mark with its word, and an SVG form,
  a station of the line diagram, that PR-33 adds; so the mark's shape
  and tone come from `STATUS_LOOK` alone, never from the HTML, and the
  SVG form draws the same marks as circles and rings with no second
  table.
- `web/app/composables/useNow.ts` (new): one clock for the app, a ref
  that ticks every 15 s while any component uses it. The sidebar
  (`SessionSidebar.vue:350`), the palette (`CommandPalette.vue:179`) and
  the Roundhouse read it in place of their own intervals for the status
  words; other clocks stay.
- The sidebar's row (`SidebarSessionRow.vue:107-109`, today the dot of
  `rowState`): `StatusMark` of `sessionStatus`, the mark alone at the
  row's end, its word in its label and title. PR-7 draws the word at the
  end of the row's meta line.
  The run block's member rows are the same component.
- The event mark beside a name (`EventMarkBadge.vue`, drawn at
  `SidebarSessionRow.vue:101` and `SessionTile.vue:51`; today a green
  "done" badge for a finished turn, `events.ts:31`, `:57`): the `done`
  mark is not drawn while the session's status says finished, since the
  status mark says it. Where it is drawn (the Events feed's icon,
  `entryIcon`), `EVENT_INFO.done` is neutral and its label is
  "finished".
- The folded section's preview squares (`SidebarSection.vue:11`, `:31`,
  from `sectionPreview`, `sidebar.ts:302`, today the row state):
  `sectionPreview` returns status keys and each square is a
  `StatusMark`.
- The rail (`SidebarRailSquare.vue:24-29`, `:50`, today the dot of the
  row state; its label from `STATE_WORDS`, `sidebar.ts:461`, and
  `railRunLabel`, `:481`): the square's corner mark is the session's
  `StatusMark`, a run's capsule takes `blockStatus` of its members, and
  the labels read "alone · finished 2m ago". `STATE_WORDS` is deleted.
- The phone's list (`pages/sessions/index.vue:50`, `SessionSidebar` in
  page mode) is the sidebar's row and needs nothing more.
- "Shared with you" (`SidebarSharedList.vue:14`, from `joinedDot`,
  `utils/joined.ts:154`: running green, idle grey, a link that does not
  answer amber): a joined session's status is what the entry knows, so
  `running`, "starting" or `exited`; a link that does not answer draws
  no mark, its line saying "not answering". Amber stays for needs.
- The Yard tile (`SessionTile.vue:21-26`, `:53`, today "Needs input"
  amber, "Running" green, else the status in grey): `StatusMark` with
  its word, in the header where the word stands now. PR-17 moves it to
  the foot. The amber border of a tile that asks (`:40`) stays.
- The Yard's bar (`AttentionBar.vue`, `attentionSlices` in
  `utils/charts.ts:35-44`, today needs amber, working green, done blue,
  the rest grey "with no state yet"): `statusSlices` counts the live
  sessions by key (needs, working, finished, running), drawn amber,
  green, neutral and green at 40 % with a hatch, and its label reads,
  for example, "18 sessions: 5 need you, 4 working, 8 finished, 1
  running". Each slice keeps `data-attention-slice` with the key and
  `data-count`; the walk checks `data-count`, never the sentence.
- The Yard's focused header (`pages/yard.vue:248-249`, today
  `SessionStatusBadge` and `AttentionBadge`), the session page's header
  (`pages/sessions/[id].vue:556-557`, the same two), the Roundhouse's
  slide (`pages/roundhouse.vue:424`, `AttentionBadge`) and the guest's
  page (`pages/join/[token].vue:510-511`, the same two, its attention
  from the `attention` frame, `:59-61`, its status from the join,
  `:289`, and now `turns` from it): one `StatusMark` with its word.
  `AttentionBadge.vue` and `SessionStatusBadge.vue` are deleted, with
  `statusColor` and `statusLabel`
  (`composables/useSessions.ts:1077-1100`). At every width the session
  page draws the mark with its word; PR-19 makes room for the name
  below `sm` (D9). The guest's page draws "finished" with no age, since
  the `attention` frame carries no `since` (`internal/proto/control.go`,
  `Attention`; `join/[token].vue:61` stamps every frame with the time it
  came). `turns` is the join's, read again at each reconnect, and is
  held true from the first `working` or `done` frame of source `api`,
  `title` or `status`.
- The Roundhouse's strip (`CarouselStrip.vue:58-61` for the dot, `:220`
  for the word, today "needs you", "running" or the status):
  `StatusMark` and its word.
- The palette (`CommandPalette.vue:492`, `:527`, the dot from the row
  state of `usePaletteRows.ts:355`; joined entries at `:638`; a run
  block's detail "N running" at `:442`): a session row carries `status`
  and draws `StatusMark`; a run block's detail reads "1 working · 2
  finished". The action "Stop every session that is done" (`:540-548`)
  becomes "Stop every finished session", its detail "None has finished"
  when empty, its question "Stop 3 finished sessions?".
- The guest's run grid (`JoinCrewGrid.vue:46`) keeps its amber border
  for a member that asks; it draws no state word today and gets none.
- `TransportBadge.vue:36`: a direct connection is neutral, not green.
- `docs/design/system.md` (new): its first section, "Status", holds the
  table above, the variants, the rule that every surface calls
  `utils/status.ts` and draws `StatusMark`, and "Decided 2026-11-15 by
  the maintainers"; PR-37 builds the rest of the file on it. The rule
  lines in `docs/design/briefs/sidebar.md:66-67`, `briefs/chat.md` and
  `docs/design/crews-graph-brief.md`, and "Status colours" in
  `docs/features.md:42-44`, become a link to it. `brand.md:87-89` keeps
  its identity rule.

**Tests.**

- `web/app/utils/status.test.ts` (vitest, new): every rule of
  `sessionStatus` with `now` given: running and no state is `running`;
  working is `working`; done 12 minutes before `now` is "finished 12m
  ago", 30 seconds before is "finished just now"; needs input is
  `needs`, and needs input `answered: "terminal"` is `working` with
  `turns` and `running` without; exited with a needs input is `exited`;
  stopped is `exited`; exit code 1 is "exit 1"; starting is the running
  mark with "starting"; `host_disconnected` is the exited mark with
  "host away"; `host_disconnected` with a needs input is `needs`,
  "needs you · host away". Without the change: there is no such module,
  and `rowState` gives `running` for both working and done.
- `web/app/utils/status.test.ts`: the shared table. Each row of
  `internal/session/testdata/status-table.json` (read as
  `tasks.test.ts:124-135` reads the crew's fixture) gives its key from
  its attention and `turns`. Without the `input` mapping, "done, then an
  Enter" and "answered" fail by name; a Go change that moves a row fails
  here or in `TestTheStatusTable`.
- `web/app/utils/status.test.ts`: the distinctness guard. Every pair of
  keys of `STATUS_LOOK` differs in mark and in word, and in tone except
  working and running, which share green and differ by the ring; no
  word is given a status colour. Without: it fails the day a change
  draws finished green again.
- `web/app/utils/sidebar.test.ts` changes: `sectionPreview` (`:305-315`)
  returns keys (`['needs', 'needs', 'running', 'exited']` becomes the
  keys of the sessions it is given); the rail labels (`:441-451`) read
  "alone · finished 2m ago" with `now` given, and "laptop one · starting
  · on laptop". `rowState` (`:203-212`) is kept: it still picks the
  section.
- `web/app/utils/charts.test.ts:48-55` moves to `statusSlices`: the five
  sessions it makes count
  `{ needs: 1, working: 1, finished: 1, running: 1 }` with the ended one
  out. `web/app/utils/events.test.ts` (`:181`, `:208`, `:355`, `:375`):
  the done mark is `{ label: 'finished', color: 'neutral' }`.
- `web/e2e/status-walk.spec.ts` (Playwright, new, on the day's
  `states`): its own table of keys and words (never the app's: a change
  of the app's words fails it), one test per surface and size, so that a
  failure names the surface. Surfaces: the sidebar's row and the folded
  section's squares, the rail (the sidebar folded), the Yard's tile, the
  Yard's bar (`data-count` per key), the Yard's focused header, the
  session page's header, the Roundhouse's slide and strip, the palette's
  session rows (opened, the five names typed one by one), all at 1440 x
  900; the phone's list and the session page's header at 390 x 844 with
  touch. Each test opens the page once with `dayPage`, waits for the
  five sessions by id (a mark drawn, never an absence), reads each key,
  each word (the age by `/^finished (just now|\d+[mh] ago)$/`: it ticks
  while the test reads) and each mark's colour against its token with
  `probeColour`, and checks that a word's colour is the muted text's.
  Where a surface draws the mark alone (the sidebar's row until PR-7,
  the rail, the folded squares), the word is read from the mark's
  `aria-label` and no word colour is checked. Then it switches the theme
  in the page (`useTheme`), waits for the class on `html`, and reads all
  five again (AGENTS.md: check again after a redraw the test causes);
  the rail's test also folds and unfolds a section between reads.
  Without the change: the first test fails on `flaky-vim-spec`, which
  has no `data-status-mark` and draws `data-row-dot="running"` in green
  as `yard-tile-resize` does.
- `web/e2e/status-walk.spec.ts`: the guest page. A view link
  (`POST /api/sessions/{id}/links`) of a session the test makes on the
  day's server with the day's finished agent, opened at `/join/<token>`
  and joined, shows `finished` and its word, exactly "finished" (no
  age: the frame carries no `since`); the test ends the link and
  the session before it ends (the `day` fixture's guard). Without: the
  guest page draws `AttentionBadge` and no mark.
- `web/e2e/status-walk.spec.ts`: "host away", read once, on a hosted
  session of its own as `hostedend.spec.ts` makes one (`hostedSession`,
  `endHosted` in `fixtures.ts`): with its host stopped, the row's mark
  is `exited` with "host away" (the day's server is started with a host
  token, PR-1). The test waits for `status` to be
  `host_disconnected` in the API before it looks (wait for a state that
  holds). Without: the row draws the grey idle dot.
- `web/e2e/codexworking.spec.ts` changes: in "a Codex member that asks
  and goes on" (`:168`), while the stub spins (`:178-183`) the row's
  `[data-status-mark]` is `working`, and after `stopSpin` and the done
  (`:185`) it is `finished`. This is the walk's one proof from a real
  agent's signal: Codex's program status (source `status`). Without: no
  attribute; both reads see the green dot.
- `web/e2e/palette-reach.spec.ts:130-172` changes: the action is "Stop
  every finished session" and its question names the session; the rest
  of the test stands.
- `web/e2e/sidebar.spec.ts:125`, `:175` move from
  `[data-section-preview] [data-state=…]` to `[data-status-mark=…]`,
  with the key of the session each test made. Every `data-row-state`
  assertion of `sidebar.spec.ts`, `phone.spec.ts:82` and
  `codexworking.spec.ts:178-213` stays: the attribute keeps naming the
  section.

No proof is lost: no spec asserts `AttentionBadge`'s words or pulse,
`SessionStatusBadge`, the tile's "Running" or the bar's colours today.

**By hand.**

- The ring against the dot at 8 px on a 1x screen and at 200 % zoom, in
  both themes, and over the ring-2 separator of the rail's corner mark
  and the palette's dot (`SidebarRailSquare.vue:50`,
  `CommandPalette.vue:527`).
- Green against amber, and the ring against the dashed ring, with a
  deuteranopia filter: the shapes must carry the state alone.
- The bar's hatched running slice beside the working slice.

**Size.** About 420 lines of code (`status.ts` 110, `StatusMark.vue` 50,
`useNow.ts` 25, the surfaces 160, the deleted badges and their helpers
about 75 out, the docs 50) and 560 of tests (`status.test.ts` 200, the
changed unit tests 50, `status-walk.spec.ts` 280, the other specs 30).

**Defaults.**

- **The age.** Default: "finished just now" under a minute, then
  "finished 12m ago"; `useNow` ticks every 15 s. Why: a seconds counter
  in the sidebar is noise.
- **The done event mark.** Default: not drawn while the status says
  finished; where it is drawn (the Events feed) it is neutral and says
  "finished". Alternative: a neutral "finished" badge beside the mark.
- **A link that does not answer.** Default: no amber mark; its line says
  "not answering". Why: amber is for needs alone.
- **The rail's run capsule.** Default: its most urgent member, in the
  order needs, finished, working, running, exited. Why: what waits
  hardest on a person first.
- **The session page's header.** Default: at every width the mark with
  its word; PR-19 makes room for the name below `sm`. Why: a word beside
  every colour.
- **Where the status table is written.** Default: PR-3 begins
  `docs/design/system.md` with its Status section, marked decided;
  PR-37 builds the rest on it. Alternative: the table in `brand.md`,
  moved later.

**Depends on.** PR-1: the day's `states`, `dayPage`, `useTheme`,
`probeColour` and the `day` fixture's guard. PR-2: `turns`, the cleared
`done`, the kept `since` and the shared table. Open PRs merged: #907
(the app's files `pages/sessions/[id].vue`, `pages/yard.vue` and
`pages/join/[token].vue`) and #912 (`SidebarSessionRow.vue`,
`usePaletteRows.ts`). It builds on what #908 (merged) made of
`utils/sidebar.ts`, `SidebarSection.vue`, `SidebarSharedList.vue` and
`sidebar.spec.ts`. Issue #716 fixed before
it: this row would draw Codex's misread approval as a confident
"working".

**Left out.** Runs, tasks, members and the graph: PR-10. The sidebar's
Finished and Working sections and a run's "2 working · 2 finished" in
the sidebar: PR-14. The word in the sidebar's row's meta line: PR-7. The
tile's foot and the Yard's chips: PR-17. One number of what needs you,
the tab title and the favicon (`useAttention.ts:158-159`, `:436-455`):
PR-4 and PR-5. A session the server no longer holds: PR-19 (D7). The
light theme's status text tones and the contrast test: PR-24 and PR-25;
this row adds no status-coloured text. Green kept as a health mark
(`EventsAside`'s "hooks installed"): PR-26.

### PR-4: web: the pages in one table; one list and one number for what needs you (X-pages, X-needs-model, P13's nav part)

A person sees two new things: a page's tooltip in the nav says what
the page is ("Yard: every live terminal on one screen"), and the
palette's page rows carry the same sentence under their name. The
pages, their order, their keys and every number on screen stay as
they are; this row builds the two tables the next rows read.

**Today.** The destinations are written four times with their own
words: the nav's `nav` array (`layouts/default.vue:216-240`) and its
rail twin `railNav` (`:245-258`), the phone's `tabs` and `pages`
(`components/BottomBar.vue:22-43`), and the palette's `PAGES`
(`composables/usePaletteRows.ts:52-64`) with its `settingsItem`
(`:470-472`). They disagree on the name of one page: the nav and the
phone say "Decide", the palette and the rail's label "Needs your
decision". The nav's tooltips are Nuxt UI's: `UNavigationMenu` draws
one only while collapsed, and only the label (`NavigationMenu.vue:232`
in `@nuxt/ui` 4.11.2), so the full sidebar has none. What needs you is
counted six ways from three stores:

1. The sidebar's Needs you header (`components/SessionSidebar.vue:370`)
   counts the sessions not ended whose attention asks
   (`model.counts.needs`, `utils/sidebar.ts:194-198`, `:270`), plus the
   open durable decisions no shown question mirrors, of runs that still
   run (`loneDecisions`, `SessionSidebar.vue:45`; `unmirrored` and
   `runRuns`, `utils/decisions.ts:570-598`).
2. The pill's "n of m" (`composables/useNeedsCursor.ts:47-49`) counts
   the same set.
3. The nav's Decide badge (`default.vue:222`), its rail chip and label
   (`:248-250`), the phone's Decide tab (`BottomBar.vue:23`) and the
   palette's "Needs your decision · N waiting" (`usePaletteRows.ts:463`)
   show `useInbox().count`: the server's open items less the records
   answered and not yet applied (`barCount`, `utils/inbox.ts:399-401`;
   the server's sum in `internal/api/inbox.go:953-981`), 0 before the
   first read and on a switchyard (`useInbox.ts:365`).
4. The nav's Yard badge (`default.vue:224`), its rail chip (`:247`,
   `:256`), the phone's Yard tab (`BottomBar.vue:25`), the palette's
   "Yard · N need you" (`usePaletteRows.ts:463`), the tab title's "(N)"
   and the favicon's dot (`useAttention.ts:435-454`) show
   `useAttention().count`: sessions that ask, a host away among them
   (`needingInput`, `utils/attention.ts:107-113`).
5. The rail's own amber count (`components/SidebarRail.vue:36-38`) is
   `railModel`'s `needs`, the sidebar's session count without the
   decisions (`utils/sidebar.ts:558`).
6. The Yard's header chip "Needs you N" (`pages/yard.vue:42`, `:255`)
   counts the sessions that ask and are running or starting, so a host
   away is left out.

On the review's seeded day that is 6 in the sidebar (five questions and
one gate), 9 on Decide (those six, two plans waiting for approval and
one task out of its attempts) and 5 on the Yard. A question answered by
keys typed in its terminal is not a seventh disagreement: the pages
read every session through `liveSession` (`utils/attention.ts:93-104`,
`useAttention.ts:173`), which reads such a question as none, as the
server's inbox does (`inbox.go:454-457`).

**The change.**

- `web/app/utils/pages.ts` (new): `PAGES`, one entry a destination:
  `id`, `label` (the nav's word), `title` (the page's own name), `to`,
  `icon`, `chord`, `about` (its one sentence) and `when` (`always`,
  `desktop`, `brakeman`). The sentences: Yard "Every live terminal on
  one screen.", Roundhouse "One terminal at a time, on rotation.",
  Decide "Everything that waits for your answer, in one list.", Crews
  "Saved teams of agents, and each run of one.", Dispatch "Tickets, the
  plans you approve, the runs they start.", Events "What your agents
  report, as it happens.", History "What was said, decided and checked,
  kept after the work ends.", Brakeman "A sidekick that reads all of it
  and briefs you.", Agents "The agents this machine can launch, and how
  each reports to Conductor.", Sessions (the phone's tab) "Every
  session, the ones that need you first.", Settings "How the desktop
  app runs its server and shares." Labels stay as they are in this
  row; PR-5 changes one.
- In the same file, pure readers of the table: `navPages({ brakeman,
  desktop })`, `phoneTabs()`, `phoneMore({ desktop })`, `palettePages({
  desktop })`, `pageActive(id, path)` (today's rules moved: Crews on
  `/runs/…`, Dispatch on its projects and tickets, History on its
  records, Sessions on `/` and a session's page) and `pageTip(page)`,
  which gives "Yard: every live terminal on one screen".
- `layouts/default.vue`: `nav` and `railNav` are built from `navPages`;
  the badges, the chip rule and the keys are unchanged. The full
  sidebar's menu draws each label in its `#item-label` slot inside a
  `UTooltip` with `pageTip`; the rail passes `tooltip: { text:
  pageTip(page) }` on each item, which Nuxt UI shows on hover and on
  focus (Nuxt UI 4.11.2 uses the label as the text unless the item's
  tooltip object names `text`, `NavigationMenu.vue:232`).
- `components/BottomBar.vue`: `tabs` and `pages` from `phoneTabs` and
  `phoneMore`; `active()` becomes `pageActive`.
- `composables/usePaletteRows.ts`: `PAGES` and `settingsItem` give way
  to `palettePages`; each page row's `detail` is its `about`, so a
  person who types "terminal" finds the Yard (the palette searches a
  row's detail, `utils/palette.ts:237-241`). The palette keeps its
  three rows that are not pages (Events · Routing, Events ·
  Integrations, Paste invite, `usePaletteRows.ts:61-63`) after
  `palettePages`' rows, until PR-27 and PR-28 move the two tabs;
  `palettePages` gives the destinations alone.
- `web/app/utils/needsYou.ts` (new): `needsYou(input)`, a pure function
  over the attention's sessions, the decisions store's open records
  with `runOf`, and the inbox's state. It returns the items in order,
  each with its `kind` (question, decision, task, plan), its `ref`, its
  run when it has one, and its `since`; `answerable`, the items the pill
  and the sidebar's row can answer (questions and decisions); `runs`,
  the ids of the runs that wait; `number`; `known` (false while the
  inbox is unread or its last read failed, when the plans and tasks are
  not known); and `more` (the inbox holds a next page, so plans and
  tasks past its first 150 are not counted, `INBOX_PAGE`,
  `utils/inbox.ts:137`). Its rules: a question is a session not ended
  whose attention asks, a host away included, counted once; a question
  that mirrors an open record is that record's one item; a decision is
  an open record no shown question mirrors, of any run, a stopped
  run's too (as the server counts it, `internal/api/inbox_decisions.go:
  455-472`); a task or a plan is the inbox's summary of that kind; a
  record answered and not yet applied is not counted. The inbox's own
  question and decision summaries are never counted, so a new question
  counts the moment the session says so, not after the next inbox read
  (`useInbox.ts:73-80`), and no number is made by subtracting one store
  from another.
- `web/app/composables/useNeedsYou.ts` (new): the function over
  `useAttention`, `useDecisions` and `useInbox`. In this row it has no
  caller but its test; PR-5 makes every count read it.

**Tests.**

- `web/app/utils/pages.test.ts` (vitest, new): `navPages` gives today's
  nav exactly (Decide, Yard, Roundhouse, Agents, Crews, Dispatch,
  Events, History, then Brakeman while the server has it and Settings in
  the desktop app); `phoneTabs` and `phoneMore` give today's bar and
  More; `palettePages` today's page rows in their order, the three rows
  that are not pages kept after them; `pageActive`
  holds each rule above with a path that must not match beside each one
  (`/crewsx`, `/dispatchx`); every entry has one `about` sentence,
  ending in a full stop and not starting with its own label; every
  `chord` is a row of `GLOBAL_SHORTCUTS` (`composables/useShortcuts.ts:
  26-46`), so the table and the keys cannot drift apart. Without the
  change: the module does not exist; with an entry dropped or moved, the
  order cases fail by name.
- `web/app/utils/needsYou.test.ts` (vitest, new): a table of cases on
  the shape of the review's day and its edges: six sessions that ask
  (one of them a host away, one mirroring an open record), one gate no
  question mirrors, one open record of a stopped run, one record
  answered and not applied, two plans, one failed task, one session
  starting. It asserts the items in order, `answerable`, `runs` (the
  failed task's run, the stopped run) and `number` (11 here: the six
  questions, the gate, the stopped run's record, two plans, the task;
  the mirrored record is its question's item). Then the lag case:
  attention holds a new question the inbox has not read, and the number
  is one higher at once; the inbox then reads it, and the number does
  not move. Then the inbox unread, failed, past its first page and on a
  switchyard: `known` false, false, true and true, `more` true for the
  third, and the sessions and decisions counted in all four. Without the
  change: the module does not exist; a number built as the inbox's total
  less what attention lacks fails the lag case by one.
- `web/e2e/sidebar.spec.ts` (Playwright), a new test "a page's tooltip
  says what it is", at 1440 x 900 on `dayPage`: the pointer on the
  Yard's label in the full sidebar shows a tooltip reading "Yard: every
  live terminal on one screen"; the sidebar folded to the rail (Alt+B,
  as `chords.spec.ts:77` presses it), `tabTo` the Yard's icon and the
  same text shows on focus; the palette opened with "terminal" typed
  lists `go:yard` with that sentence as its detail. Without the change:
  no tooltip in the full sidebar, the rail's says "Yard", and
  "terminal" does not find the page.
- Unchanged, and kept as the proof that the tables give today's lists:
  `sidebar.spec.ts:435` (the foot's links in order), `phone.spec.ts:68`
  and `:154` (the bar and More), `palette-keys.spec.ts:137-143` (the
  `go:yard` row by its key).

**By hand.** The tooltip's place beside the full sidebar and beside the
rail, in both themes; that no tooltip covers the row below it while the
pointer moves down the nav.

**Size.** About 330 lines of code and 380 of tests.

**Defaults.**

- **A page's sentence in the palette.** Default: it is the row's detail,
  and so searched ("terminal" finds the Yard). Why: the palette searches
  every row's detail, and a second unsearched field adds a rule for
  little. Alternative: shown, not searched.
- **Past the inbox's first page.** Default: the number reads "N+", never
  a total made by subtraction.

**Depends on.** It builds on #908 (merged), which rewrote the header
and the width in `layouts/default.vue` and the specs of
`sidebar.spec.ts` this row adds to. PR-1 for `dayPage` and `tabTo`.
Open pull requests #899 and #912 change other lines of
`usePaletteRows.ts`; whichever merges first, the other rebases.

**Left out.** The numbers and the name (PR-5). The welcome's "Where
things are" (PR-30) and a page's empty state (PR-22, PR-23) read
`about` later. The Agents card's line and the bell's link, the rest of
P13 (PR-27, PR-28). The shortcuts dialog keeps its own words ("Go to
the Yard"); the test above only holds that its chords and the table's
agree. Tasks are counted from the inbox's task summaries, the server's
own rule for a task that waits on a person, not from `taskStatus`,
which comes with PR-10; PR-10 adds the test that holds the two to one
answer.

### PR-5: web: one amber number, a row that leaves, one name (P2, P24's nav row, P3's name, X-counts base)

A person sees one amber number on the desktop: on Needs you, in the
nav, on the rail and in the tab's title. The Yard's nav badge is gone.
The page that was "Decide" in the nav and "Needs your decision" on its
own title is "Needs you" everywhere a person reads its name. Its row in
the nav leaves when nothing waits and nothing was decided in the last
day. The route `/decisions`, the key G N, the phone's tabs and the
decide-only page stay.

**Today.** The two amber badges are Decide's (`layouts/default.vue:
217-223`, `useInbox().count`) and the Yard's (`:224`,
`useAttention().count`); on the rail each gets a warning chip and its
count in its label (`:245-258`), and the rail's top count is a third,
sessions only (`SidebarRail.vue:36-38`). The tab title and the favicon
follow the Yard's (`useAttention.ts:435-454`). The Decide row is always
in the nav and shows its badge above zero only (`default.vue:217-223`).
The name has three forms: "Decide" (the nav, `default.vue:218`; the
phone, `BottomBar.vue:23`; a decision link's tab, `pages/join/
[token].vue:534`), "Needs your decision" (the page's title and navbar,
`pages/decisions.vue:32`, `:288`; the rail's label; the palette,
`usePaletteRows.ts:53`, `:948`; the shortcut, `useShortcuts.ts:33`; the
run's decision card on a link, `join/[token].vue:473`; a ticket's link,
`DispatchDecisions.vue:82`) and "Needs you" (the sidebar's section, the
Yard's chip, the pill). `docs/plans/decision-inbox.md:170` (its owner
decision 11) keeps "Needs your decision".

**The change.**

- `layouts/default.vue`: the nav's first row is "Needs you" (from
  `pages.ts`), its badge the one number (`useNeedsYou().number`, with
  "+" after it when `more`), and every place that draws that number
  carries `data-count-of="needs"`. The Yard's badge goes. On the rail,
  Needs you keeps its chip with the label "Needs you, 9 waiting"; the
  Yard's chip and its count go, and its label is "Yard".
- `utils/needsYou.ts`: `needsRowShown(state, needs, path, now)`. The
  row shows while the inbox is unread or its last read failed (unknown
  is not zero), while any item waits, while a record is answered and
  not yet applied (`tracking`), while something was decided in the last
  day (the server's `closed`, or this browser's `recent` within
  `RECENT_MS`, `utils/inbox.ts:269`), and while `/decisions` is the page
  open. On a switchyard, where the inbox answers 403 `switchyard`
  (`useInbox.ts:235`), it is hidden. `navPages` takes the answer.
- `SidebarRail.vue`: the top count is the one number, its tooltip "9
  need you".
- `BottomBar.vue`: the first tab reads "Needs you" with the one number;
  it stays at zero (the bar's places are fixed). The Yard's tab keeps
  its count of sessions, drawn neutral like the Yard's chip (today every
  tab count is `bg-warning`, `BottomBar.vue:74`), so the phone's one
  amber number is its Needs you tab.
- `usePaletteRows.ts`: the page row reads "Needs you", or "Needs you · 9
  waiting"; the Yard's row is "Yard" with no number.
- `useAttention.ts` (`useAttentionHead`): the title's "(N)" and the
  favicon's dot follow the one number.
- `useNeedsCursor.ts` and `NeedsYouPill.vue`: `]` and `[` step through
  `answerable`, so an open record of a stopped run is reached too; the
  pill says "2 of 4 · 2 more in Needs you" when plans or tasks wait,
  which it cannot answer.
- `pages/yard.vue`: the "Needs you N" chip keeps its number of sessions
  on this grid, and its tooltip says so: "Sessions on this grid that
  need you. Everything that needs you: Needs you (G N)."
- The name "Needs you" in `decisions.vue`'s title and navbar,
  `useShortcuts.ts` ("Go to Needs you"), `DispatchDecisions.vue` ("on
  Needs you"), `join/[token].vue` (the run's card and the tab). The
  decide-only page keeps "Decide" (`pages/decide/[[did]].vue:12`). The
  attributes tests read stay (`data-sidebar-decide`,
  `data-bottom-tab="decide"`).
- The sidebar's section keeps counting the rows it lists (questions and
  the gates of running runs) until PR-13 lists every kind, and its
  header says what it leaves out, as the review's P2 asks:
  `sectionHeader(listed, items)` in `utils/needsYou.ts` gives "Needs
  you 6 · +3 on the page", the "+N" counting every item of the one list
  the section does not list (plans, tasks and the open decisions of
  stopped runs), counted from the list, and "Needs you 6" when there
  are none. The "+3 on the page" is muted text, not a second amber
  number, and links to `/decisions` (`data-section-more`). So the
  desktop never shows two amber numbers that disagree without saying
  why. PR-13 removes the words.
- Docs: the README's sidebar paragraph, an entry in `docs/features.md`,
  and a line under owner decision 11 of `docs/plans/decision-inbox.md`
  that names this row and the decision that replaced it.

**Tests.**

- `web/app/utils/needsYou.test.ts` (vitest), `needsRowShown`'s table:
  unread, failed, an item open, zero items with one record tracking,
  closed 23 hours ago (shown) and 25 hours ago (hidden), a recent entry
  of 23 and 25 hours, the page open at zero, a switchyard (hidden even
  while unread). Without the change: the function does not exist; a
  rule that reads `barCount` alone fails the unread, failed and recent
  cases. `sectionHeader`'s table: four questions and decisions listed
  and a plan and a task in the list gives "Needs you 4 · +2 on the
  page", questions alone give "Needs you 4", and the "+2" counts the
  plan and the task, never the difference of two stores; with an open
  record of a stopped run in the list as well, "+3". Without the
  change: no function.
- `web/e2e/counts.spec.ts` (Playwright, new; X-counts) asks for the
  day's `busy` part. The oracle is the refs `GET /api/inbox` lists,
  asserted equal to the refs of every part built. Every expected number
  (the readers, the Yard's tab and chip, the section's count and its
  "+N", the pill's "of N") is computed in the test from those refs and
  `GET /api/sessions` read at the same moment, never written as a
  constant. A table of readers, each reading `data-count-of="needs"`
  where it can: the nav's badge, the rail's top count and the Needs you
  label (the sidebar folded), the phone's tab (`dayPage` at 390 x 844
  with touch), the palette's page row, the title, and the favicon's dot
  (shown exactly when the number is above 0). One read of them all,
  polled until they agree (`expect.poll(readAll).toEqual`), equals the
  oracle; the Yard's nav badge and rail chip are absent; the phone's
  Yard tab (neutral) and the Yard's chip read the asking sessions on
  the grid; the sidebar's section reads its questions and gates of
  running runs and its header the "+N on the page"
  (`[data-section-more]`) of the rest, whose link opens `/decisions`;
  `]` shows "1 of N · M more in Needs you". Then the test makes its own
  asker on the day's server (the day stays read-only for what it
  seeded), waits on the API for the question, and every reader reads
  one more at the same poll; the question cleared, as before; a reload,
  and the same read after it (check again after a redraw the test
  causes). Without the change: the nav's number and the Yard's badge
  disagree, the title and the rail follow the Yard's.
- `web/e2e/counts.spec.ts`, "the row leaves at zero", on a server of
  its own (`ownServer`: a fresh data directory has nothing decided): no
  `[data-sidebar-decide]` once the inbox has read (wait for the read the
  stub counts, never for a time); G then N opens the page and the row
  shows while it is open; the test's own session asks through the
  attention route and the row shows with 1; the question answered from
  the page's own buttons and the row stays with no number (this
  browser's recent); a second browser context on the same server shows
  no row. With `inbox-stub.ts` holding `/api/inbox` unanswered the row
  shows; answering 500 it shows; answering 403 `switchyard` it is gone.
  At 390 x 844 the phone's tab stays. Without the change: the row is
  always there.
- `web/e2e/sidebar.spec.ts:435` moves: on the shared server something
  may have been decided that day, so it asserts the pages after Needs
  you in order and that Needs you, when present, is first; the row's
  rule is proved above on servers whose state the test knows.
- `web/e2e/phone.spec.ts:68` reads `/Needs you/` for the first tab;
  `:69` and `:71` stand (the first tab's count, the Yard's count).
- `web/e2e/decision-page.spec.ts:147` and `:463-480` stand (a question
  waits, so the row and the tab show).
- `web/e2e/sidebar.spec.ts:476` stands (the rail's count is a positive
  number).
- `web/e2e/decisions-gate.spec.ts:106-136` (the pill's "1 of 2" and "2
  of 2" over a session and a gate) stands: no plan or task waits on that
  server, so the pill adds no "more".

**By hand.** One amber badge in the foot and none on the Yard, in both
themes; the row leaving and coming back moves the rows below it once,
and nothing jumps while the inbox is first read.

**Size.** About 180 lines of code and 400 of tests.

**Defaults.**

- **The row while its page is open.** Default: Needs you's row also
  shows while `/decisions` is the page open. Why: the current page never
  vanishes from the nav under a person.
- **The phone's first tab.** Default: renamed "Needs you"; its hook
  stays `data-bottom-tab="decide"`.

**Depends on.** PR-4 (`pages.ts`, `needsYou.ts`). PR-1 (the day's
`busy` part, `ownServer`, `dayPage`, `inbox-stub.ts`, `tabTo`). Owner
decisions 2 and 5 (the name, one number). Open pull request #907
changes other lines of `join/[token].vue`.

**Left out.** Plans and tasks listed in the sidebar (PR-13, which
removes the header's "+N on the page"). "Open on your phone" in your
menu, and the page at zero (PR-6). The Yard's chips' names (PR-17). A
ticket strip's waits read `useNeedsYou` in PR-16. The palette's group
"Needs your decision" keeps its label until PR-13 makes the palette's
two groups one.

### PR-6: web: the page of what needs you, with nothing on it (P24)

After this row the page at `/decisions`, called Needs you since PR-5,
is worth landing on on a day with nothing waiting. It is one column that
says nothing needs you, says when the last item closed, names the four
kinds of thing that show up here and lists what was decided in the last
day, with a way to every decision in History. "Pick an item" shows only
while there is an item to pick. "Open on your phone" is in your menu as
well as on the page, so it is reached from any page. With an item
waiting the page is as it was: the list beside the open item at `lg`,
the list alone on a phone. The nav's row, the phone's tab and the count
are PR-5's and do not change here.

**Today.** On `main`, and with #912, which changes the rows and the
mount but none of what follows, `pages/decisions.vue:296` lays the page
out in two columns at `lg` whatever it holds. With nothing open,
`:309-312` draw an icon and "Nothing needs your decision now." in the 24
rem left column; `inboxEmpty` (`utils/inbox.ts:385-387`) allows that
only after a read that answered. `:436` draws "Pick an item to see its
brief, its choices and what each authorizes." in the right column
whenever no item is open, even when nothing is listed. The section
headed "Recent" (`:366-395`) holds "Durable decisions closed in the last
day" (the server's `recent`, the same for every viewer,
`internal/api/inbox_decisions.go:476-491`) and "Acted on from this
browser, the last day" (`conductor.inbox.recent`, kept in this browser,
`utils/inbox.ts:265-310`), and nothing in it leads to History. "Open on
your phone" is a button in the page's header at `lg` only (`:290`); its
modal, its QR codes and the decision link it can make live in the page
(`:232-278`, `:440-472`), and your menu (`accountActions`,
`utils/actions.ts:141-146`) does not offer it. The page calls itself
"Needs your decision" twice (`:32`, `:288`).

**The change.**

- `utils/inbox.ts` gains `kindLook(kind)`: for `question`, `plan`,
  `task` and `decision` an icon (`i-lucide-message-circle-question`,
  `i-lucide-clipboard-check`, `i-lucide-list-x`, `i-lucide-scale`, the
  icon a ticket's decisions already use), a word ("Question", "Plan",
  "Task", "Decision") and a line ("A question an agent asks", "A plan
  that waits for your approval", "A task that failed and is out of
  attempts", "A decision a run needs from you"). An unknown kind gets
  `i-lucide-circle-help` and its own name. PR-13 and PR-21 draw from it.
- `utils/inbox.ts` gains the three rules the page draws by.
  `atZero(state, openRef)`: the list was read and answered, `inboxEmpty`
  holds, no row is answered and not yet applied, and no `?item` is open.
  `pickShown(sections, openRef)`: no item is open and the list holds a
  row. `lastClosedWords(closed, recent, now)`: "The last one closed 2h
  ago.", from the newest of the server's closed records (`closed`) and
  this browser's own acts (`at`); nothing when neither holds one, or
  when the newest time is ahead of `now` or not a time.
- `pages/decisions.vue` at zero: the grid's `lg` columns apply only when
  `atZero` is false, and the one column is at most `max-w-2xl` wide. The
  block keeps its hook `data-decisions-empty` and says "Nothing needs
  you now.", then `lastClosedWords` (`data-decisions-last`). Under it
  "What shows up here" (`data-decisions-kinds`) lists the four kinds,
  each with `kindLook`'s icon and line (`data-decisions-kind="<kind>"`).
  The right column is not drawn.
- "Recent" becomes "Decided in the last day". Its two lists keep their
  rows and hooks (`data-decisions-recent`, `data-recent-decision`,
  `data-recent-row`, `data-recent-word`) under the captions "A run's
  decisions" and "From this browser". The section ends with a link
  "Every decision, in History" (`data-decisions-history`) to
  `/history?q=class%3Adecided`, the query of History's own Decision chip
  (`pages/history/index.vue:93-96`). At zero the link shows even when
  the last day holds nothing.
- "Pick an item…" (`:436`) is drawn only when `pickShown`.
- `components/OpenOnPhone.vue` (new) takes the modal, `phoneOffer`, the
  QR codes and `makeDecisionLink` out of `pages/decisions.vue` whole,
  with every hook (`data-phone-offer`, `data-phone-target`,
  `data-phone-make-link`, `data-phone-link`).
  `composables/useOpenOnPhone.ts` (new) holds its open state
  (`useState`) and `show()`. `layouts/default.vue` mounts it once,
  beside `DecisionModal`; it reads the live sessions and runs from
  `useAttention`, which the layout already starts. The page's header
  button calls `show()`. The QR code holds `${origin}/decisions` from
  whatever page it is opened, as `phoneOffer` makes it today.
- `utils/actions.ts`: `AccountHandlers` gains an optional `phone`, and
  `accountActions` adds "Open on your phone" (`i-lucide-smartphone`) to
  the group of Keyboard shortcuts and Toggle theme only when `phone` is
  given. `SidebarHeaderMenus.vue` passes it; `BottomBar.vue`'s More does
  not, so the phone's sheet is as it was.
- The three names, and which row changes each. "Decide" stands on the
  nav row (`layouts/default.vue:218`), the phone's tab
  (`BottomBar.vue:23`), the palette's item rows ("Decide: <title>",
  `usePaletteRows.ts:74`) and a decision link's tab
  (`pages/join/[token].vue:534`). "Needs your decision" stands on the
  page (`decisions.vue:32`, `:288`, and the error's "What needs your
  decision is not known now", `:306`), the rail's label
  (`default.vue:250`), the palette's page row, its count and its group
  (`usePaletteRows.ts:53`, `:463`, `:948`), the shortcut's label
  (`useShortcuts.ts:33`), a decision link's list title
  (`join/[token].vue:473`) and a ticket's link "on Needs your decision"
  (`DispatchDecisions.vue:82`). "Needs you" stands on the sidebar's
  section, the Yard's chip (`yard.vue:215`) and the palette's pinned
  group (`usePaletteRows.ts:940`). Every one but the palette's group
  becomes "Needs you" in PR-5, the row of the name: the nav, the rail,
  the phone's tab, the palette and the shortcut's label through
  `utils/pages.ts`, the page's `useHead` title and navbar, a ticket's
  link ("on Needs you") and a decision link's tab and list title. This
  row changes the one line of the page PR-5 leaves: the error says "What
  needs you is not known now". The palette's group is made one in PR-13.
  The route `/decisions`, G N and the decide pass's own page
  (`pages/decide/[[did]].vue`, titled "Decide", headed "Decisions for
  you") stay.

**Tests.**

- `web/app/utils/inbox.test.ts` (vitest): `kindLook` gives the four
  kinds four different icons and words, and `"ticket"` the fallback.
  Without the change: `kindLook is not a function`.
- `web/app/utils/inbox.test.ts` (vitest): `atZero` is true only for a
  list read and answered with nothing open, nothing answered and not yet
  applied and no `?item`; it is false before the first read, after a
  failed read, on a switchyard (`unavailable`), with one tracking row,
  and with an item in the query. Without the change: no function; a rule
  written as "no rows" takes the failed read for zero and fails here.
- `web/app/utils/inbox.test.ts` (vitest): `pickShown` is false for an
  empty list and with an item open, true for one open row and for one
  tracking row alone. `lastClosedWords` takes the newer of a closed
  record two hours old and an act one hour old ("The last one closed 1h
  ago."), and says nothing for none, for a time an hour ahead and for
  `"not a time"`. Without the change: no functions.
- `web/app/utils/sidebarActions.test.ts` and
  `web/app/utils/actions.test.ts` (vitest): with a `phone` handler,
  `accountItems` lists "Open on your phone" after Toggle theme and its
  `onSelect` calls the handler. The shapes at
  `sidebarActions.test.ts:132-133`, made with no `phone`, stand
  unchanged: they are the proof that the phone's More gains nothing.
  Without the change: the item is missing.
- `web/e2e/decide-zero.spec.ts` (Playwright, new), the inbox stubbed in
  the browser with `stubInbox(page, items)` and `inboxItem` (PR-1's
  `inbox-stub.ts`); the first page's `recent`, the failing read and the
  switchyard's refusal are one `page.route` of the spec's own over
  `/api/inbox`, registered after `stubInbox` (the later route answers).
  Through `dayPage` at 1440 x 900 and at 390 x 844 with touch. (a) The
  empty list: once `[data-decisions-empty]` is drawn (a page not yet
  drawn passes every "hidden"), it reads "Nothing needs you now.",
  `[data-decisions-kind]` holds question, plan, task and decision in
  that order, `[data-decisions-item]` has count 0 and no text says "Pick
  an item"; at 1440 the list column is wider than the grid's 24 rem and
  `inside` the panel; at 390 `uncut` holds for every line of the block.
  The theme is then toggled from your menu and the one column is
  measured again (check again after a redraw the test causes). (b) One
  closed decision two hours old: the heading reads "Decided in the last
  day", its `[data-recent-decision]` row shows, `[data-decisions-last]`
  reads "The last one closed 2h ago." (the stub's time is set from the
  browser's clock at the route), and the link opens
  `/history?q=class%3Adecided` with the query in History's field. (c)
  The read answering 500: the could-not-read alert shows and
  `[data-decisions-empty]` has count 0 (unknown is not zero). (d) The
  read answering 403 with the code `switchyard`: the unavailable alert,
  and no zero page. (e) One open question at 1440: two columns and "Pick
  an item" (PR-21 changes this step). Without the change: (a) fails on
  "Pick an item" visible at 1440 and the list 384 px wide, (b) on the
  heading "Recent" and the missing link.
- `web/e2e/decision-page.spec.ts` (Playwright, its own server), the
  desktop test at `:462`: after the page's button, the same modal opens
  from your menu on `/yard` (`[data-header-account]`, then the item);
  the test waits for the menu to be gone (`toHaveCount(0)`, a menu that
  has closed is not gone) before it reads
  `[data-phone-offer="loopback"]`. On the network origin the menu opens
  it from `/yard`, and `data-phone-url` is still `${origin}/decisions`.
  The steps at `:500-540` stand as written: the hooks moved with the
  modal. Without the change: there is no menu item, and an offer that
  took the page's own address would say `/yard`.
- `web/e2e/sidebar.spec.ts:426` (Playwright): your menu's items gain
  "Open on your phone" after Toggle theme.
- Specs that stand as they are: `decision-page-durable.spec.ts:182` and
  `:210`, and `decision-page.spec.ts:189` and `:256-260`, read the last
  day's rows by hooks that stay; `decisions-ask.spec.ts` (from #912)
  waits for `[data-decisions-empty]` or a row, and the zero block keeps
  that hook. No proof is lost.

**By hand.** The zero page in both themes at 1440 and at 390: the
column's width, the four marks told apart, where the link sits.

**Size.** About 240 lines of code, 90 of them moved from
`pages/decisions.vue` into `OpenOnPhone.vue`, and 330 of tests.

**Defaults.**

- **Where the last-closed time comes from.** Default: the newest of the
  server's closed run decisions and this browser's own acts, and no line
  when neither has one. Why: the server keeps no list of questions,
  tasks or plans answered from another browser, and the line says
  nothing rather than guess.
- **The link to History.** Default: "Every decision, in History" shows
  whenever the last-day section shows, and on the zero page even when
  the last day is empty.

**Depends on.** PR-5: `utils/pages.ts` holds the page's name, and the
nav row that leaves at zero is PR-5's; this row's menu item is how the
phone offer is reached once that row is gone. Open pull request #912
merged: it rewrites the rows and the mount of `pages/decisions.vue` and
adds `INBOX_ROW_CLASS` to `utils/inbox.ts`. PR-1 for `dayPage`,
`web/e2e/layout.ts` and `inbox-stub.ts`.

**Left out.** The nav row hiding at zero, the phone's tab and the count
(PR-5). The zero block drawn with `PageEmpty` and the page's "About this
page" (PR-23, which takes this block as it is). Every kind listed in the
sidebar with `kindLook` (PR-13), and the list's rows carrying it
(PR-21). A question, task or plan answered from another browser is not
in "Decided in the last day": the server keeps no such list, and the
line says nothing rather than guess. "Open on your phone" on the phone's
own More sheet and in the palette: not built.

### PR-7: web: sidebar rows that fit: gutters, the row, the header (P5, first half)

The sidebar opens at 200 px again, with every name in it whole. The
header holds the mark, "Conductor" and your menu; Alerts and the
collapse to the rail are in your menu, and the collapse is also on
Ctrl+B (⌘B on a Mac) and the divider. A row puts its name on one line
across the row; the question and its choices use the row's whole
width; in Needs you a run shows the members that ask and says how many
do not. The question still reads whole in the row; PR-8 changes that.

**Today.** At `9fb98b06` the sidebar opened at 12.5 rem, 200 px
(`SIDEBAR_SIZE`, `utils/sidebar.ts:62`). Pull request #908, merged
since, raises the minimum and the opening width to 16.75 rem, 268 px,
because the header's five parts need 258 px (`SIDEBAR_SIZE = { min:
16.75, maxShare: 0.26, range: 5 }`, a drag reaching at least 348 px); it
keeps the saved width under `conductor.sidebar.width` and reads one
below the minimum as the minimum, the value kept until the next drag.
The header is the mark, the name, `SidebarHeaderMenus` (an update
button when one is ready, Alerts as a popover of `AlertSwitches`, your
menu) and the collapse button (`layouts/default.vue:324-335`,
`SidebarHeaderMenus.vue:37-68`). The gutters are Nuxt UI's `px-4` on
the sidebar's header, body and footer plus the list's `px-1`
(`SessionSidebar.vue:357`, `:367`): 20 px a side. A row draws the
agent's initials in a 24 px column before its name
(`SidebarSessionRow.vue:97`), indents the choices 40 px (`pl-10`,
`SidebarPrompt.vue:37`) and cuts each label (`:50`), which at 200 px
gives "Yes, and d…". A run block lists every member
(`SidebarRunBlock.vue:24-45`).

**The change.**

- `utils/sidebar.ts`: `SIDEBAR_SIZE = { min: 12.5, maxShare: 0.26,
  range: 5 }`. The minimum and the opening width are 200 px; a drag
  reaches at least 280 px, so in a window 1024 px wide the widest drag
  is exactly the width at which PR-8 shows a question whole. A saved
  width stays as saved: a person who dragged keeps that width, and a
  person who never dragged opens at 200 px.
- `layouts/default.vue`, the header: the mark, "Conductor" and your
  menu. `px-1` in place of Nuxt UI's `px-4` on the body, so that with
  the list's `px-1` the list's gutter is 8 px; `px-2` on the header and
  the footer (8 px a side, the padding the 186 px is reckoned with). By
  #908's own measure the header then needs about 186 px of 200.
  The collapse button leaves the header. The update button stays, and
  the rule that moves the mark aside when it shows (#604) is recomputed
  for the new minimum and padding by the sum its comment gives
  (`default.vue:320-323`).
- `SidebarHeaderMenus.vue`: your menu gains a group after Your name:
  "Alerts…", with the bell's icon (ringing while notifications are on),
  and "Collapse to the rail" with ⌘B beside it. "Alerts…" opens
  `AlertsPanel.vue` (new), a slideover holding `AlertSwitches`, mounted
  once in the layout. These two items are the sidebar's alone: the
  phone's More already shows the switches (`BottomBar.vue:89`) and has
  no rail, so they are not added to `accountActions`
  (`utils/actions.ts:141`). On the rail the foot holds your menu and
  the update button.
- `SidebarSessionRow.vue`: the name's line spans the row, the mark from
  PR-3 at its end; the initials leave their column and sit at the start
  of the meta line as a small chip (`[data-row-initials]`), beside
  `[data-row-meta]`, whose text is unchanged. The status word (PR-3's
  `StatusMark` with `word`, e.g. "finished 12m ago") ends the meta line
  in muted text, in `data-status-word`, and is cut after the meta's
  other parts.
- `SidebarPrompt.vue`: the choices start at the row's text edge (no
  `pl-10`); a label is cut only when it is wider than the row, its whole
  text in its title as today.
- `utils/sidebar.ts`: `blockRows(block, section)`. In Needs you a run
  shows its asking members and one line for the rest, "and 2 more · 1
  working · 1 finished" (PR-3's words); elsewhere every member.
  `focusRows` gives that line a row (`m:<runId>`), so J and K land on
  it and Enter unfolds it. The unfolded runs are held for the visit,
  not stored.
- `SidebarRunBlock.vue`: draws `blockRows`, the line as a button
  (`[data-run-more]`).
- The README's sidebar paragraph and `docs/features.md`'s width entry
  say 200 px again, with what moved into your menu.

**Tests.**

- `web/app/utils/sidebar.test.ts` (vitest): #908's width cases written
  for 12.5 rem: the bounds at 1024, 1280, 1440, 1920, 2560 and 3440 px
  wide are 200 to 280, 332, 374, 499, 666 and 894 px; `SIDEBAR_WIDTH`
  is `clamp(12.5rem, var(--width), max(17.5rem, 26vw))`; a saved
  `16.75` reads 16.75, a saved `3` reads 12.5. `blockRows` for a run of
  three with one asking: one member and the line's words; in Running,
  all three; with none asking, all three. `focusRows` puts `m:<runId>`
  after the asking members and none for an unfolded run. Without the
  change: the width cases read 16.75 and `blockRows` does not exist.
- `web/e2e/sidebar.spec.ts` (Playwright), #908's "every part of the
  header shows" rewritten: at 1440 x 900 and 1280 x 720 with no saved
  width the sidebar is 200 px; the mark, the name and your menu are
  `inside` the sidebar and the name is `uncut` and reads "Conductor"
  with at least 8 px to spare; the header holds no
  `[data-header-alerts]` and no `[data-sidebar-collapse]`; "Launch
  agent" is `uncut`; dragged to 340 px and back past the minimum, the
  same checks again. Without the change: the sidebar opens at 268 px
  and the header holds five parts.
- #908's two "Shared with you" tests run at the new 200 px minimum and
  pass: the heading whole with the 8 px gutter and the count on one
  line.
- `web/e2e/sidebar.spec.ts`, #908's "with a wider font" and #604's two
  update cases: their lists of parts lose Alerts and the collapse; the
  update case drags to the minimum plus the button's width by the new
  sum. #908's "in a window 1024 wide" reads 200 to 280 px, and the rest
  of the width group (`sidebar.spec.ts:499-640`, the opening width, a
  drag, a reload, a double-click, the window resized) reads 200 and
  280 px again where #908 wrote 268 and 348. The width a spec asserts
  is written in the spec, never read from the app's table, so a change
  of the number fails it.
- `web/e2e/sidebar.spec.ts:419-436` ("alerts and your menu sit beside
  the name") becomes "your menu holds Alerts and the collapse": its
  items are Your name, Alerts, Collapse to the rail, Keyboard
  shortcuts, Toggle theme, Open on your phone (PR-6's), Workbench
  token, Forget token; Alerts opens
  the panel with "Browser notification", and Escape closes it with the
  focus back on your menu's button; after picking an item the test
  waits for the menu to be gone (`toHaveCount(0)`) before the next step
  (a menu that has closed is not gone); Collapse to the rail shows the
  rail, and Alt+B brings the sidebar back. Without the change: Alerts is
  a header button and the menu has five items.
- `web/e2e/sidebar.spec.ts`, "a row fits 200 px", on `dayPage` at 1440
  x 900: the test's own session named "e2e twenty chars xy" asks with
  three choices ("Yes", "Yes, and don't ask again", "No, tell Claude
  why"); the row's name is `uncut`; the row's (`[data-sidebar-row]`)
  left edge is within 9 px of the sidebar's; `[data-row-initials]` sits
  on the meta line's top; the status word `[data-status-word]` is
  `uncut`; the first choice's left edge is within 2 px of the name's;
  each choice is `uncut`. Then the session asks again (a redraw the test
  causes) and the checks run again. Without the change: the gutter is 20
  px, the first choice is 40 px in, and the second label is cut.
- `web/e2e/sidebar.spec.ts`, "a run in Needs you shows who asks": a crew
  of three on the shared server, one member holding the trust question;
  its block, found by its run id (count only what the test made), holds
  one member row and `[data-run-more]` reading "and 2 more"; a click on
  it shows three rows; reloaded, the block is folded again. J from the
  header lands on the line and Enter unfolds it. Without the change:
  three rows and no line.
- `web/e2e/sidebar.spec.ts:48-112` moves: its run of two (review asks,
  lead runs) now shows one member in Needs you, so the test unfolds the
  line before it reads lead's meta line; every other assertion stands.
- `web/e2e/chat.spec.ts:366`, `rail-squares.spec.ts:38` and
  `sidebar.spec.ts:470`, `:574` clicked `[data-sidebar-collapse]`; they
  press Alt+B instead, after the sidebar's root is drawn (a key a page
  does not yet listen for is lost).
- `web/e2e/phone.spec.ts:74-79` stands: the phone's list keeps its 44
  px choices across its width.

**By hand.** The header at 200 px in both themes, with the update
button and without; the row at 200 and 280 px; the line "and 2 more"
read beside the run's header; your menu's two new items on a Mac and
on Windows.

**Size.** About 250 lines of code and 380 of tests.

**Defaults.**

- **Alerts in your menu.** Default: one item, "Alerts…", that opens a
  small slideover of the two switches (`AlertsPanel.vue`), not two
  checkbox items; PR-28 adds the rules to that panel.
- **Collapse in your menu.** Default: your menu also gains "Collapse to
  the rail" beside the key and the divider. Why: a mouse finds the rail.
- **Widths saved under #908.** Default: kept; only a person who never
  dragged opens at 200 px. No migration.
- **The initials.** Default: a small chip at the start of the meta line,
  outside the meta's text.
- **A run's "and 2 more".** Default: once unfolded it stays unfolded for
  the visit only, not stored.

**Depends on.** It builds on #908 (merged): the header,
`SIDEBAR_SIZE`, `SidebarSection.vue`, `SidebarSharedList.vue` and the
specs this row rewrites, and its fixes of "Shared with you" and of the
name's title stay. PR-3 for the row's mark and the words of "1 working
· 1 finished". PR-6 for your menu's items (its "Open on your phone" is
in the list this row's test asserts). PR-1 for `dayPage`, `inside`,
`uncut`. Owner decision 6 (the 200 px sidebar). Open pull request #912
changes two other lines of `SidebarSessionRow.vue`. PR-7 and PR-8 merge
back to back, since PR-7 undoes #908's 268 px opening, its Alerts in
the header and its 348 px drag, and PR-8 makes the rows short.

**Left out.** The question on two lines and its panel (PR-8). The
sections Finished and Working (PR-14). Rows for plans, tasks and
decisions (PR-13). The pages at the foot (PR-15). The rules of Alerts
in its panel (PR-28). No width saved under #908 is moved back to 200
px.

### PR-8: web: a question on two lines in the narrow sidebar, whole beside its row (P5, second half)

In a sidebar narrower than 280 px, a row's question is two lines, and
the whole question with its choices or its reply field opens in a panel
beside the row, to the right of the sidebar. The panel opens while the
row has the focus, or from the row's Answer button. The digits 1 to 9
answer the focused row as before. At 280 px and wider, on the phone's
list, in the palette, the pill, the Yard and the session page the
question stays whole where it is.

**Today.** The row draws the question whole by design: no clamp, a long
word broken rather than cut, because the server bounds a message at 500
bytes (`session.MaxAttentionMessage`) and a clamp would hide the tail
from a touch screen and the keyboard (`SidebarSessionRow.vue:116-120`,
issue #187). At 200 px a 500-byte question is about two dozen lines.
The choices and the reply field sit in the row (`SidebarPrompt.vue`).
The list's keys act on the focused row (`SessionSidebar.vue:242-317`),
and a row that redraws under the focus takes it back only when nothing
else holds it (`mayRefocusRow`, `utils/sidebar.ts:408-411`, issue
#596). The pill (`NeedsYouPill.vue`) shows a question whole at the top
of the window, with its choices and no reply field.

**The change.**

- `utils/sidebar.ts`: `NARROW_SIDEBAR_PX = 280` and
  `narrowSidebar(px, page)`: true below 280 px, never for the phone's
  list. One answer decides both the clamp and the panel, so neither
  ships without the other.
- `SessionSidebar.vue`: a `ResizeObserver` reads the sidebar's drawn
  width (`#dashboard-sidebar-main`); `narrow` goes to every row and to
  one panel for the list. `panelRow(focused, pressed, rows, narrow)`, a
  pure function beside it, says which row the panel is for: the focused
  row while it asks, or the one whose Answer was pressed, and none when
  the sidebar is wide or the row shows its Stop question.
- `SidebarSessionRow.vue`: when `narrow` and the row asks,
  `[data-row-prompt]` gets `line-clamp-2` and `data-row-clamped`, the
  row draws no choices and no field, and it shows an "Answer" button
  (`[data-row-answer-open]`, with `aria-expanded`). The comment that
  cites #187 says what holds now: whole in the panel, and whole in the
  row at 280 px and wider.
- `SidebarRowPanel.vue` (new): a `UPopover`, not modal, anchored to the
  row by `reference`, on its right (`side: 'right', align: 'start'`).
  It holds the session's name, the whole question and the row's own
  `SidebarPrompt`, its labels wrapped rather than cut. It never takes
  the focus by itself (`onOpenAutoFocus` prevented); Answer, clicked,
  tapped or pressed, moves the focus to its first control; Escape in it
  closes it and gives the focus back to the row. It carries
  `data-row-panel` and `data-panel-for="s:<id>"`.
- The list's `onFocusOut` counts the panel as inside the list, so
  typing in the panel's field keeps the row focused and the panel open;
  the #596 rule is kept as it is: a field that holds the focus keeps it
  while the rows redraw. J and K move the focus and the panel together.
  The list is never hidden from assistive technology (no `aria-hidden`).
- `web/e2e/fixtures.ts`: `rowAnswer(page, row)`, the one way a spec
  reaches a row's answer: when the row is `data-row-clamped` it focuses
  the row (or taps Answer on a touch page), waits for the panel whose
  `data-panel-for` is that row, and returns the panel; otherwise it
  returns the row. The uses of `[data-row-choice]`, `[data-row-reply]`,
  `[data-row-answer]` and `[data-row-decision]` (47 on main, counted
  again when the row is cut) go through it, in `answered`, `question`,
  `sidebar`, `approval`, `choices`, `claudedialog`, `askwait`,
  `codexworking`, `decision-mirror`, `held`, `phone`,
  `dispatch-evidence` and `hosted-question`.
- The README's and `docs/features.md`'s words on the row: two lines
  below 280 px, the panel beside it.

**Tests.**

- `web/app/utils/sidebar.test.ts` (vitest): `narrowSidebar` at 199.9,
  279.9, 280 and 600 px, and the phone's list at 200; `panelRow` for a
  focused asking row (its id), a focused row that does not ask (none),
  a pressed row while another has the focus (the pressed one), a wide
  sidebar (none), a row showing its Stop question (none). Without the
  change: neither exists.
- `web/e2e/sidebar-question.spec.ts` (Playwright, new), at 1440 x 900
  and 1280 x 720 on `dayPage`, the sidebar at 200 px: the test's own
  session asks a question of exactly 500 bytes with three choices, the
  last 60 characters long. Its `[data-row-prompt]` is at most two lines
  (`lines`); the row holds no `[data-row-choice]`. `tabTo` the row: the
  panel for it shows, its left edge at or right of the sidebar's right
  edge and inside the window; its question `toHaveText` the 500 bytes;
  every choice is `uncut`; the focus is still on the row's link. The
  digit 2 answers (the session's attention clears on the API) and the
  panel is gone (`toHaveCount(0)`). Two asking rows: J moves the focus
  to the second and `data-panel-for` follows. Escape closes the panel.
  Dragged to 300 px, the question is whole in the row, its choices in
  it, and focus opens no panel; dragged back to 200 px, clamped again
  (check again after a redraw the test causes). Without the change: the
  prompt is about twenty lines tall and there is no panel.
- `web/e2e/sidebar-question.spec.ts`, the reply field: a question that
  takes a line; Answer moves the focus into the panel's field; the test
  types while a second session of its own asks and the rows redraw; the
  field keeps the focus and every character (#596); Enter sends it, and
  the session's input file holds the line; Escape from a fresh panel
  gives the focus back to the row. Without the change: no panel; with a
  modal panel the list is hidden and J does nothing.
- `web/e2e/sidebar-question.spec.ts`, touch, `dayPage` at 1280 x 800
  with touch: a tap on Answer opens the panel and a tap on a choice
  answers; at 390 x 844 the phone's list shows the question whole with
  no Answer button and 44 px choices (`tapTarget`).
- `web/e2e/sidebar-question.spec.ts`, on the day's `busy` part at 1440
  x 900 and 200 px: at least five asking rows are wholly in view in the
  list (`rowsInView`); the review counted two. Without the change: two.
- `web/e2e/question.spec.ts:48` ("the sidebar row shows the whole
  question, wrapped above its choices") and `:124` ("a question at the
  server's bound reads whole in the row, and the pointer over it and
  onto a choice moves nothing") each split: at 200 px the question reads
  whole in the panel, and the pointer from the panel's question onto a
  choice moves nothing; dragged to 300 px, the tests as they are, in the
  row. `:181` (the phone's list) stands.
- `web/e2e/sidebar.spec.ts:97-102` (each choice fits the sidebar) reads
  through `rowAnswer`: at 200 px the choices are `inside` the panel and
  the window. `sidebar.spec.ts:307-352` (the row's reply field, #596)
  goes through the panel. `sidebar.spec.ts:354-417` (J, K, Escape, a
  digit, X, S, Enter, R on the focused row) stands.
- Proof given up, by owner decision 7: "the question whole in the row"
  at the sidebar's minimum. It is held instead in the panel at 200 px
  and in the row at 280 px and wider.

**By hand.** The panel's border and shadow against the page in both
themes; its place for a row near the window's foot (Nuxt UI flips it
up); the panel and the pill shown at once; a screen reader on the
row's Answer button.

**Size.** About 230 lines of code and 560 of tests, of which about 200
are the uses moved onto `rowAnswer`.

**Defaults.**

- **What the panel holds.** Default: the row's own prompt controls
  (`SidebarPrompt`, labels wrapped), not the pill's body; the pill is
  unchanged and gains no reply field. Why: every `data-row-*` hook is
  kept.
- **The panel and the focus.** Default: focus opens the panel without
  moving the focus; Answer (a click, a tap, Enter) moves the focus into
  it; Escape gives it back to the row.

**Depends on.** PR-7 (the 200 px sidebar and the row's layout). PR-1
(`dayPage`, `lines`, `uncut`, `inside`, `rowsInView`, `tapTarget`,
`tabTo`, the day's `busy` part). Owner decision 7: the clamp never
ships without its panel, with the threshold at 280 px.

**Left out.** The Yard's queue and its specs (PR-18), which this row's
helper makes easier to move. The pill keeps its own look and gains no
reply field. Rows of other kinds in the panel (PR-13 draws plans and
tasks as one-line rows that open their page).

### PR-9: web: a run header that fits (P7, D2)

After this row a run's name is whole on a desktop window and nothing
in the header runs under a button. Row one holds the name, its label,
one ticket chip, then Chat, Pause or Resume, Stop, one menu and the
full-screen toggle. Row two sits under the bar at every width: the
run's bar and its one sentence ("2 of 6 done · 1 working · 1 checking
· 2 need you"), the pause's words, the people, the logins, who
launched the run, and how long it has been up. Add agent and Share move
into the menu. The palette's rows for them, the Stop confirm, the
Add agent slideover and every call to the server stay as they were.

**Today.** `CrewRunHeader.vue:195` gives Nuxt UI's `UDashboardNavbar`
`left: 'min-w-0 flex-1'` and `right: 'flex-none'`. The left side holds
the title and, as its sibling, the `#trailing` block of `:241-255`,
which may shrink to nothing (`min-w-0`) while its badges may not
(`flex-none`: the task badges of `taskBadges` at `:245`, "N need input"
of `runCounts` at `:247`, a legacy run's two counts at `:250-251`). The
badges spill out of their box and are painted under the right side's
controls (`:258-268`: Add agent, Share, Chat, Continue or Resume,
Pause, Stop, full screen), and the name (`:202`, `min-w-[12ch] shrink
truncate`) gives way down to twelve characters: "Ledger migrati…"
(D2). Only a phone has a strip of its own for the counts (`:272-282`,
`sm:hidden`); the people take a third row (`:285-300`). A run launched
from a ticket that is also linked to it shows the key twice (`:206-221`
and `:222-234`). Stop is red at rest (`:267`, `color="error"
variant="soft"`) and the uptime is mono (`:253`). The header counts a
managed run's open decisions once each, a plan gate that holds no task
among them (`openDecisions`, `utils/tasks.ts:212`, fed from
`useDecisions` at `CrewRunHeader.vue:153-155`). `graph.spec.ts:133-137`
checks the phone strip at 390 px; nothing checks 1440.

**The change.**

- `CrewRunHeader.vue`: a wrapper `<div data-crew-run-header>` holds the
  navbar and row two; the attribute moves off the navbar, so every spec
  that scopes to it (`[data-crew-run-header] [data-run-subtitle]`,
  `[data-run-resume]`, the Logins button of open PR 899) still finds
  what moved into row two.
- Row one, left: the name (`[data-run-name]`) takes its whole width
  (`flex-none`, the crew name's 60 characters at most) from `lg` up;
  below `lg` it gives way last, after the label (`[data-run-subtitle]`,
  which still shrinks first) and the short id, the full name in its
  `title`. Then one ticket chip: "Launched from CND-12 · plan r3"
  (`[data-run-launched-from]`) for a run launched from a ticket, the
  linked ticket's chip (`[data-run-ticket]`) for one that was not, and
  both only when they name two tickets (`runTicketChips`, below).
- Row one, right: Chat as an icon button with its unread count
  (`[data-run-chat-button]` and `ChatUnreadPill` as today, its word in
  `aria-label` "Chat"); Continue or "Resume as new run" once stopped;
  Pause or Resume; Stop as a neutral outline button (`[data-run-stop]`),
  red only in its confirm (`[data-confirm-stop]`, unchanged); one
  `UDropdownMenu` behind an icon button (`[data-run-menu]`, `aria-label`
  "More for this run") with "Add agent" (`data-run-add`) and "Share"
  (`data-run-share`), each disabled where its button was; then
  `FullscreenButton`, as on every page header.
- Row two (`[data-run-summary]`), one line that wraps on a phone, in
  this order: the `record` badge (`[data-run-record]`) on a record; the
  bar (`[data-run-bar]`), one segment a part, each `data-part="<key>"`
  with `data-count` and its key's tone from `STATUS_LOOK`; the sentence
  of `runStatusLine` (`[data-run-progress]`); "scheduling paused"
  (`[data-run-paused]`, titled "Paused by a person" or "Paused: <why>")
  with the why in `[data-run-paused-why]`; "cleaning up" with today's
  title; the people (`[data-run-people]`, the chips of `:285-300` as
  they are); the Logins button of open PR 899 with its popover, moved
  as it is; who launched the run with the launch's receipt chip
  (`[data-run-launch]`, moved as it is); and, at the end, muted, in the
  sans face, the uptime ("up 9m · server · worktrees").
- "Need you" counts each task once whose `taskStatus` is `needs`
  (held by a decision, out of attempts with Retry or Accept named, or
  its attempt's session asking), and once each open decision of the run
  in the app's store that holds no task: the header hands
  `runStatusLine(run, sessions, decisions, now)` (PR-10) the store's
  open decisions of the run, as it hands them to `taskBadges` today. A
  run without tasks gets the sentence `runStatusLine` gives it from its
  members; the counts of
  `:250-251` and `:278-279` go.
- Deleted: `#trailing` (`:241-255`) and the phone strip (`:272-282`).
  `taskBadges` leaves the header; Dispatch's run card and run list keep
  it (`DispatchRunCard.vue:46`, `DispatchRuns.vue:81`).
- `utils/crewWords.ts`: `runTicketChips(launched, linked)` returns the
  chips to draw; the linked chip is dropped when it names the ticket the
  run was launched from.
- `utils/status.ts`: `runBarParts(line)` returns the bar's segments in
  one order (done, working, checking, needs you, failed, waiting), the
  parts at zero left out.
- `pages/runs/[run].vue`: the header ref (`:394`) keeps `openAdd` and
  `share`, so the palette's "Add an agent…" and "Share run" rows
  (`:477-479`) work as before.
- `web/e2e/run-fixtures.ts` (PR-1's `runInfo` and `serveRun`) gains
  two builders, shared by PR-11, PR-33 and PR-34. `ledgerRun()` is six
  tasks in this order: `schema` (succeeded); `naming` (succeeded) and
  `backfill` (checking on attempt 2, Cancel named), which need
  `schema`; `soak` (running on attempt 1 with a branch, Cancel named),
  which needs nothing, its attempt's session a real stub session the
  spec starts and marks working through `POST
  /api/sessions/{id}/attention`, waited on the API and stopped at the
  spec's end, so that its state is "working" by `taskStatus`'s own
  rule; `migrate` (failed out of
  attempts, Retry and Accept named) and `release-notes` (ready, held by
  the open decision dr-1), which need `naming`. Every task is owned by
  ana as the acceptance owner but `migrate`, whose task names nate.
  `legacyRun()` is four members and no tasks; a record, a paused run
  and any name are options. `serveRun` takes one run or several and
  gains, beside the run page's reads it answers since PR-1, GET
  `/api/runs` (the real list through `route.fetch()`, with the stubs
  added) and a stub's task commands, which it answers and counts. A
  spec finds each stub by its id, never by a list's length.
- `docs/features.md`: the run page's entry (`:2955-2958`) says the two
  rows.

**Tests.**

- `web/app/utils/crewWords.test.ts` (vitest): `runTicketChips`:
  launched from CND-12 and linked to CND-12 gives the launched chip
  alone; launched from CND-12 and linked to CND-9 gives both; linked
  alone gives the linked chip; neither gives none. Without the change:
  the import fails, and today's template draws CND-12 twice.
- `web/app/utils/status.test.ts` (vitest): `runBarParts` over the
  Ledger counts gives done 2, working 1, checking 1, needs you 2, in
  that order, with no failed part; `runStatusLine` with the store's
  decisions counts a gate's decision that holds no task once, and a
  decision that both holds a task and is in the store once. These are
  the siblings of `tasks.test.ts:370-387`, which stay with
  `taskBadges`. Without the decisions argument the gate's case gives "1
  needs you" for two.
- `web/e2e/run-header.spec.ts` (Playwright, new; the Ledger stub named
  with 60 characters, the crew name's bound): at 1440 x 900 (`dayPage`,
  dark), after `[data-run-name]` has its text and `document.fonts.ready`
  has settled, `uncut('[data-run-name]')`, `noOverlap` over row one's
  pieces and `inside('[data-run-summary]', the header)`; then it polls
  until the page's 5 s tick has changed the uptime's text and measures
  again (AGENTS.md: check again after a redraw the test causes). Without
  the change: the name measures twelve characters in a box wider than
  that, and `noOverlap` names the badges against Add agent.
- The same spec: `[data-run-progress]` holds the sentence, and the
  bar's `[data-part]` counts are done 2, working 1, checking 1, needs
  you 2, with no `failed` part (the failed task offers Retry: it waits
  on a person). Without the change there is no such element.
- The same spec, the menu: no button named "Add agent" or "Share" is in
  the header. Add agent is taken on `legacyRun()` (a run of members,
  where #912's `runOffer(run).add` holds): `tabTo('[data-run-menu]')`,
  Enter opens it, Enter on "Add agent" opens the "Add an agent"
  slideover; the menu's content is waited to count 0 before the
  slideover's name field is read (AGENTS.md: a menu that has closed is
  not gone). On the Ledger run the item is present and disabled, and so
  is the palette's "Add an agent…". At 390 x 844 with touch, a tap on
  the menu and a tap on "Share" open the share dialog. The palette's
  "Add an agent…" (on the legacy run) and "Share run" open the same
  two.
- The same spec: `[data-part="needs"]`'s `data-count` equals the items
  of this run in `needsYou()`, so the run's number and the app's one
  number are held together here.
- The same spec, colour, in light and dark (`dayPage` with `theme`, the
  app's own `localStorage` key): `[data-run-stop]` at rest has no
  computed colour, border or background equal to the resolved
  `--ui-error`; the `needs` segment's background equals the resolved
  `--ui-warning`. Without the change Stop is soft red.
- The same spec at 390 x 844 with touch: the name, Stop and the menu
  button are visible and do not overlap, row two wraps inside the
  window, and `noSideways` holds. A legacy stub run shows the members'
  sentence and no task parts.
- Moved: `tasks.spec.ts:118` and `:210` (the first badge "2/2 tasks")
  read `[data-run-bar] [data-part="done"]` with `data-count` 2;
  `:189` (the second badge "1 failed") reads `[data-part="needs"]` with
  `data-count` 1, the task out of attempts now waiting on a person;
  `:194` and `:197` (a "paused" badge comes and goes) read
  `[data-run-paused]`. `tasks-resume.spec.ts:132` as `:118`.
  `manifest.spec.ts:315-316` (the paused badge titled with the why)
  reads `[data-run-paused]`'s title; `:317`, `:330` and `:353`
  (`[data-run-paused-why]`) stand. `decisions.spec.ts:124` ("1 decision
  open") reads `[data-part="needs"]` 1. `crew-people.spec.ts:510-512`
  and `switchyard.spec.ts:271` and `:499` open `[data-run-menu]` and
  take `[data-run-add]` or `[data-run-share]`. `graph.spec.ts:133-137`
  checks `[data-run-summary]` and row one's `noOverlap` at 390 in place
  of the strip.
- Standing as written: `resume.spec.ts:189-211`, `palette.spec.ts:168`,
  `crews.spec.ts:131`, `dispatch-links.spec.ts` (`[data-run-name]`,
  `[data-run-ticket]`), `dispatch-evidence.spec.ts:270` and
  `brakeman-act.spec.ts:252-258` (the launched-from chip, who launched
  it and its receipt), `chat.spec.ts`, `tasks-resume.spec.ts:135`, and
  open PR 899's `capacity-crew.spec.ts` (its Logins button, still
  inside `[data-crew-run-header]`). No proof is lost: the badges' words
  leave the header, and their unit tests stay with `taskBadges`.

**By hand.**

- Both themes at 1440 x 900, 1280 x 720 and 390 x 844: whether the bar
  reads at a glance beside its sentence, and the menu's order.
- Row two on a phone with four people and a pause's words.
- The focus ring on the menu button and on Chat.

**Size.** About 260 lines of code and 420 of tests.

**Defaults.**

- **The ticket chips.** Default: one chip when the run's launch ticket
  and its linked ticket are the same; both only when they name two
  tickets. Why: hiding a different linked ticket would lose a fact.
- **Logins.** Default: open #899's Logins button moves into row two
  beside the people, not into the run's menu. Why: it is about the
  members, and #899's specs keep their hook in the header.
- **Full screen.** Default: the toggle stays its icon button, as on
  every page header.
- **What the run's number counts.** Default: each task once whose
  `taskStatus` is `needs`, and each open decision in the app's store
  that holds no task, as today's rule. Why: a plan gate that holds no
  task still waits; `run-header.spec.ts` checks it against
  `needsYou()`.

**Depends on.** PR-10: `runStatusLine`, `taskStatus` and `STATUS_LOOK`
(the sentence is PR-10's, so PR-10 is cut before this row, despite its
number). PR-1: `run-fixtures.ts`, `layout.ts`, `dayPage`, `tabTo`.
Open PR 899 merged: it adds the Logins popover and the Add agent
table's accounts to the lines this row moves
(`CrewRunHeader.vue:194-269`). Open #912 merged (`CrewRunHeader.vue`:
Add agent offered by `runOffer`). PR-7 is not needed: the checks read
the sidebar's width off the page and hold at 268 px and at 200 px.

**Left out.** Chat in the menu (kept as a button: an owner decision).
The task rows (PR-11). The other mono sentences and colours of the page
(PR-26). The GitHub reference issue 773 puts after the launched-from
chip, which goes inside the one chip. The Grid view's tiles lose the
height of row two on a run without people; `bestGrid` reads the box
(`[run].vue:368`), so they only shrink.

### PR-10: web: one status language for runs and tasks (P1)

After this row a run's tasks, its members, its graph, its card and
Dispatch's card for it speak the language of PR-3. A task whose attempt
works is green with a ring; a task that waits on a decision or is out of
attempts says "needs you" in amber; a task's own states (waiting, ready,
done, failed, cancelled) have marks of their own that are none of the
five. The run page's layout stays as it is (PR-9 and PR-11 change it),
and so do the sentence of each task row (`data-task-words`), the
engine's state on each row (`data-task-state`) and the member's status
on graph nodes and cards (`data-status`).

**Today.** A task row draws its state three ways: a dot from `taskDot`
(`web/app/utils/tasks.ts:172-186`: running green, checking blue, failed
red, done forest, blocked a grey ring), a badge with the engine's word
(`RunTasks.vue:78-91`, `:248-251`) and the sentence of `taskWords`
(`tasks.ts:128-168`). The attempt's session is never read, so a task
held by a decision is a green "running" dot beside an amber "waits for
decision dr-7", and a task out of attempts is red though the inbox lists
it as waiting on a person (`internal/api/inbox.go:664-691`). The blocked
task's grey ring is the mark PR-3 gives finished. Members are coloured
by `memberDot` (`utils/runs.ts:55-67`) and `memberDotClass`
(`utils/crewWords.ts:239-251`), green for any running member. A run's
badges count "N running" in green (`taskBadges`, `tasks.ts:108-119`;
`CrewRunHeader.vue:251`, `:279`; `summaryBadges`,
`dispatchEvidence.ts:69`), and an ended run says "Finished" (`runBadge`,
`runs.ts:47`; `summaryState`, `dispatchEvidence.ts:80`) with a green bar
on the Crews page (`barClass`, `crewWords.ts:317-327`).

**The change.**

- `web/app/utils/status.ts`: `TASK_LOOK`, the marks of a task's own
  states: waiting (a hollow square, for blocked, "waiting", and ready,
  "ready"), done (a check in neutral ink: the forest fill is for the
  main action alone), failed (a red cross), cancelled (a grey dash).
  The guard of PR-3 covers them.
- `taskStatus(task, run, attemptSession, now)`: a task with a hold still
  open (`taskAnswer`, `tasks.ts:204-206`) is `needs`; a failed task on
  which the engine names a command (`t.commands`, the inbox's own rule
  in `taskItem`) is `needs`; a running task is its attempt session's
  `sessionStatus` (`working`, `finished`, `needs` or `running`), and
  `running` while no session is known; verifying is the working mark
  with the word "checking"; blocked and ready are waiting; succeeded is
  done; failed without a command is failed; cancelled is cancelled. A
  hold answered and not yet applied is Yardmaster's to apply and is not
  `needs`.
- `memberStatusOf(run, member, sessions, now)`: a member with a live
  session takes its session's status; starting is the running mark with
  "starting"; pending is waiting, "waiting to start"; ended is `exited`,
  and ended with an error of its own is failed. It returns the member's
  `memberStatus` (`crews.ts:1094-1101`) beside the key, so `data-status`
  is unchanged. `memberDot` and `memberDotClass` are deleted.
- `runStatusLine(run, sessions, decisions, now)`, where `decisions` are
  the store's open decisions of the run, so that a plan gate's decision
  that holds no task is counted once (a run carries no such decision
  itself): the counts by key and the sentence "2 of 6 done · 1 working
  · 1 checking · 2 need you" for a managed run, "1 working · 2
  finished · 1 needs you" for a run of members alone, and for an ended
  run its outcome: "Succeeded", "Failed", "Stopped", "Interrupted", and
  "Ended" for a run of members that all ended. Its variant for
  Dispatch's run summaries counts the summary's tasks and looks its
  members' sessions up in the store. `runBadge` (`runs.ts:40-52`),
  `summaryState` (`dispatchEvidence.ts:76-83`) and `taskBadges`' counts
  (`tasks.ts:108-119`, `summaryBadges`, `dispatchEvidence.ts:63-73`)
  read it; their badges are neutral but for needs (amber) and failed
  (red). `taskDot` is deleted.
- `RunTasks.vue:248-251`: the dot and the badge become one `StatusMark`
  with its word; `taskWords` stays the sentence; `data-task-state`
  stays.
- The graph: `CrewGraphTaskNode.vue:47`, the Tasks mode's list row
  (`CrewGraph.vue:291`), `CrewGraphNode.vue:66`, `CrewGraphList.vue:39`
  and `CrewMiniDag.vue:57-62`, `:88` draw `StatusMark` from `taskStatus`
  and `memberStatusOf`. The two legends (`CrewGraph.vue:216-231`, the
  app's only legend) are made from `STATUS_LOOK` and `TASK_LOOK`, one
  line a key, and list only the keys the drawn run has.
- The run's card (`CrewRunCard.vue:61`, "N need you" or a green
  "running"): the parts of `runStatusLine`, with no change of layout;
  `data-run-state` stays on the same badge. This row leaves
  `CrewRunHeader.vue` to PR-9. `taskBadges` keeps its labels in this
  row and changes only its colours, so `tasks.spec.ts:189` ("1 failed")
  and `decisions.spec.ts:124` ("1 decision open") stand until PR-9
  moves them.
- Dispatch's run card (`DispatchRunCard.vue:38-46`): `runStatusLine` for
  a live run, its summary variant for a run the ticket only summarises.
- The run page's palette rows (`pages/runs/[run].vue:435` for tasks,
  `:447` for members, each mapping states to the sidebar's row states):
  `taskStatus` and `memberStatusOf`.
- The people page (`pages/people/[name].vue:129`, `:191`, the engine's
  word in its own `stateColor`): `StatusMark` from `taskStatus`.
- The Crews page: a run's outcome (`outcomeBadge`,
  `crewWords.ts:145-158`, used at `CrewRunsPanel.vue:86`) says "ended"
  for "finished", the Runs filter's "Finished" (`CrewRunsPanel.vue:67`)
  reads "Ended", `barClass` draws ended runs neutral (stopped lighter,
  failed red) and a live run's bar in the working green, and the "1
  live run" dot (`CrewsTable.vue:50`) is neutral. The bars' legend
  (`pages/crews/[[id]].vue:659`) and `CrewRunBars.vue:7` name the new
  colours.
- `taskStatus` and `utils/needsYou.ts` (PR-4) must agree on "a task
  needs a person". The one list counts tasks from the inbox's task
  items, the server's own rule, and keeps that source; `taskStatus`
  follows the same rule, and the test below holds the two to one answer
  for the same run.

**Tests.**

- `web/app/utils/status.test.ts` (vitest): `taskStatus` and `needsYou`
  agree. For one run of the `task-commands.json` rows and a task held
  by an open decision, every task `taskStatus` calls `needs` is in
  `needsYou()`'s list for that run, as a task item or under the
  decision that holds it, and every task item of the list is a task
  `taskStatus` calls `needs`. Without the change: no `taskStatus`; a
  `taskStatus` that calls a failed task with no command `needs` fails
  it.
- `web/app/utils/status.test.ts` (vitest): `taskStatus`. A task held by
  an open decision is `needs` and one held by an answered decision is
  not; a running task is the key of its attempt's session (working,
  finished, needs) and `running` with none; verifying is "checking";
  blocked is "waiting"; succeeded is done. The failed rows come from
  `internal/crew/testdata/task-commands.json`, the fixture the engine's
  `TaskCommands` and the inbox are held to: a failed row with a command
  named is `needs`, one with none is failed, so the run page's "needs
  you" is the inbox's rule. Without the change: no such function, and
  the held task's `taskDot` is `bg-success`.
- `web/app/utils/status.test.ts`: `memberStatusOf` (pending, starting,
  each session key, ended, ended with its own error) and `runStatusLine`
  (a six-task run says "2 of 6 done · 1 working · 1 checking · 2 need
  you"; a run of members; each outcome; "Ended"; the summary variant
  gives the same sentence as the run it summarises; and the proof of
  `tasks.test.ts` "counts an open decision that holds no task", kept:
  a plan gate's open decision that holds no task, handed in
  `decisions`, is counted once). The guard: no
  `TASK_LOOK` mark equals a `STATUS_LOOK` mark, so a blocked task's grey
  ring cannot come back as finished's. Without: the guard fails on
  `taskDot('blocked')`'s ring.
- `web/app/utils/runs.test.ts:61-92` changes: `runBadge('finished')` is
  "Ended" or the outcome; the `memberDot` cases move to
  `status.test.ts`. `web/app/utils/tasks.test.ts:30-63`: `taskBadges`'
  colours as above; its labels stand.
  `web/app/utils/dispatchEvidence.test.ts:98-101`: "Running" becomes the
  summary's sentence. `web/app/utils/crewWords.test.ts`: the outcome's
  word and the bars' classes.
- `web/e2e/run-status.spec.ts` (Playwright, new): a managed run of six
  tasks from `runInfo` in `web/e2e/run-fixtures.ts`, served with
  `serveRun`: one held by an open decision, one failed with `retry` and
  `accept` named, one running whose attempt is the day's working session
  (`yard-tile-resize`), one running whose attempt is the day's finished
  one (`flaky-vim-spec`), one verifying, one succeeded. On the Tasks
  view each row's `[data-status-mark]` and word; on the Graph view's
  Tasks mode each node's; the legend lists exactly the keys drawn; the
  run card and Dispatch's run card (not the header, which is PR-9's)
  carry "2 of 6 done" only when the run says so. At
  1440 x 900 in both themes (`dayPage`, then `useTheme`), the mark's
  colour by `probeColour`; at 390 x 844 with touch, the list the phone
  gets. After the reads it switches to Graph and back, and to the other
  theme, and reads again (AGENTS.md: check again after a redraw the test
  causes; the needs edge of #281 was lost only on a redraw). Without the
  change: the held row's mark is absent and its dot is green, and the
  finished attempt's row reads as running.
- `web/e2e/tasks.spec.ts`, "a task that fails its checks three times"
  (`:177`): once Retry and Accept anyway show, the row's mark is `needs`
  with "needs you", and `data-task-words` still holds the engine's
  sentence. This is the one proof from the real engine. Without: the red
  dot and "failed".
- `web/e2e/decisions.spec.ts`, beside `:122`: the held `build` row's
  mark is `needs` while its words read "attempt 1 waits for decision …".
  Without: a green dot.
- `web/e2e/graph.spec.ts:51-52`: `data-status` stays `running` and
  `pending`; the nodes gain `data-status-mark`, `lead`'s the key of its
  session and `docs`'s waiting.
- `web/e2e/crews.spec.ts:84-106` stands: `data-state` and
  `data-run-state` keep their values, and its banner's "1 running" is
  left as it is (below).

No proof is lost: the specs that read `data-task-state`,
`data-task-words` or a member's `data-status` (`decisions-gate.spec.ts`,
`decisions-people.spec.ts`, `decisions-restart.spec.ts`,
`manifest.spec.ts`, `dispatch-evidence.spec.ts`,
`dispatch-links.spec.ts`) read attributes that stay.

**By hand.** The four task marks beside the five at 8 px in both themes;
the legend read once on a run with every state (the 40-task run of
`run-fixtures.ts`).

**Size.** About 340 lines of code (`status.ts` additions 130, the
surfaces 180, the deleted helpers about 60 out, the Crews words 30) and
470 of tests (`status.test.ts` additions 170, the changed unit tests 60,
`run-status.spec.ts` 190, the other specs 50).

**Defaults.**

- **When a task needs you.** Default: while a hold is still open, or
  when it failed with a command the engine names (the inbox's rule); a
  hold answered and not yet applied is not `needs`.
- **The run's sentence.** Default: "2 need you", not the review's
  "waiting on you". Why: the list's name.

**Depends on.** PR-3: `utils/status.ts`, `StatusMark.vue`, `useNow` and
the guard. PR-4 (`utils/needsYou.ts`: the test that holds `taskStatus`
and `needsYou()` to one answer). PR-1: `dayPage`, `useTheme`,
`probeColour`, `run-fixtures.ts` and the day's `states`. Open PR #912
merged: it changes `CrewGraph.vue`, `CrewGraphList.vue`,
`CrewGraphNode.vue`, `pages/runs/[run].vue` and `utils/runs.ts`. PR-9
builds the run header's sentence on `runStatusLine`, so it lands after
this row.

**Left out.** The task table and the row menu: PR-11. The header's
layout, its badges and D2: PR-9. The line diagram's stations: PR-33 and
PR-34. The crew page's live-run banner ("1 running",
`pages/crews/[[id]].vue:678`, under open #899): the first row of the
stream to touch that page after #899 takes it to `runStatusLine`
(PR-34). A run in the sidebar that waits on a person leaving Running:
PR-13. The ticket's strip: PR-16.

### PR-11: web: task rows as a table that says the state once (P7)

After this row the Tasks view is a table: state, task, who, what and
actions, each read straight down. A row says its state once, with the
mark and word of PR-10, says its attempt once, and names an owner only
when its task names one. Cancel, Copy branch and Ask Brakeman why sit
behind a menu on the row, and nothing in a row at rest is red. The same
rows inside a ticket's Runs section (`DispatchRunCard.vue:93`) change
with them. The selected task's detail under the rows, every command
with its receipt, and the two confirms stay as they were.

**Today.** `RunTasks.vue:239-268` draws each task as one `flex
flex-wrap` line, so each item sits where the one before it ended and no
column lines up. A row says its state three ways: the dot (`:248`), the
badge (`:251`, coloured by a local `stateColor` at `:78-90`) and the
words (`:255`, coloured by their tone). PR-10 makes the dot and the
badge one `StatusMark`; the words keep their colour. The attempt is said
twice: in `:254` and in the words `taskWords` gives a running or
checking task ("attempt 2 running", `utils/tasks.ts:149-153`). Every
row names its owner (`:253`), so a crew whose tasks name none says
"owner ana, the acceptance owner" on each of them (`ownerWords`,
`utils/crews.ts:167-170`). The actions (`:257-266`) are all buttons in
the row, and Cancel is a red ghost × at rest (`:263`).

**The change.**

- `utils/tasks.ts`: `taskWords(t, run, { attempt: false })` leaves the
  attempt out of a live attempt's words ("running", "waits for decision
  dr-7", "checking the result", "blocked by core: …"). The default stays
  as it is for the palette's detail (`[run].vue:433`) and the graph's
  phone list (`CrewGraph.vue:294`).
- `utils/tasks.ts`: `taskRow(t, run, { brakeman })` returns the row's
  parts: `words` without the attempt; `attempt`, "attempt N" when N is
  above 1 and the words do not already say "attempt N", else nothing;
  `owner`, the owner's words only when `ownerFrom` is `task`; `shown`,
  the actions in the row in this order (Answer, Retry, Accept anyway,
  Open, Open preview); and `menu` (Cancel, Copy branch, Ask Brakeman
  why), both from `taskActions` and `taskAnswer` as today.
- `RunTasks.vue`: the list is an `@container`, and every row is a grid
  on one template string, each track fixed or a share of the width and
  each cell drawn even when empty, so no cell's content moves a column
  (the fixed-track row that PR-12 brings to Dispatch): state
  (`[data-task-col="state"]`, the `StatusMark` of `taskStatus`), task
  (`[data-task-col="task"]`, the kind icon and the id, mono, cut with
  its title), who (`[data-task-col="who"]`, avatar, member,
  `[data-task-attempt]`, `[data-task-owner]`), what
  (`[data-task-col="what"]`, `[data-task-words]` in the default text
  colour, then `[data-task-commit]`), and actions
  (`[data-task-col="actions"]`). A header row names State, Task, Who
  and What while the container is 48 rem or wider; narrower, a row
  folds into two lines: state, task and actions, then who and what.
- The actions cell: the `shown` buttons as today, then one
  `UDropdownMenu` behind an icon button (`[data-task-menu="<id>"]`,
  `aria-label` "More for <id>") holding "Cancel…" (red inside the menu
  only), "Copy branch" and "Ask Brakeman why", each item with its old
  hook (`data-task-cancel`, `data-task-copy-branch`,
  `data-task-ask-brakeman`; Nuxt UI 4.11.2's `pickLinkProps` hands an
  item's `data-*` keys to its element). The button is drawn only when
  the menu holds an item. Enter on a row still selects it; Enter or
  Space on the menu's button opens it, Escape closes it and the focus
  goes back to the button.
- Deleted from `RunTasks.vue`: `stateColor` (`:78-90`) and the words'
  `tone` map.
- `docs/features.md`: the Tasks view's entry (`:2944-2948`).

**Tests.**

- `web/app/utils/tasks.test.ts` (vitest): `taskWords` with `{ attempt:
  false }`: "attempt 2 running" becomes "running", "attempt 1 waits for
  decision dr-7" becomes "waits for decision dr-7", "attempt 2: checking
  the result" becomes "checking the result", and each default is
  unchanged. Without the change: the option does not exist.
- `tasks.test.ts`, `taskRow` as a table: running on attempt 2 gives
  "attempt 2" and "running"; running on attempt 1 gives no attempt;
  succeeded by attempt 2 gives none (the words say it); failed with the
  engine's reason "attempt 3 failed at check never" gives none; ready
  after a retry with 3 attempts gives "attempt 3"; an owner from the
  task gives "owner nate", one from acceptance nothing; a held running
  task shows Answer and Open with Cancel and Copy branch in the menu; a
  task out of attempts shows Retry and Accept anyway with Ask Brakeman
  why (where the server has Brakeman) and Copy branch in the menu; a
  record shows no Retry, Accept or Cancel. It fails when Cancel goes
  back into the row or an attempt is said twice.
- `web/e2e/run-tasks.spec.ts` (Playwright, new; the Ledger stub of
  `run-fixtures.ts` at `?view=tasks`, its rows found by id): at 1440 x
  900, `aligned` over `[data-task-col="who"]`, `"what"` and
  `"actions"`, within 1 px. Without the change: the member sits wherever
  the badge ended.
- The same spec: each row has one `[data-status-mark]` and no
  `[data-task-state-badge]`, and its text holds "attempt N" at most
  once; the acceptance-owned rows have no `[data-task-owner]` and
  `migrate`, whose task names nate, says "owner nate". Without the
  change: six owners, and "attempt 2" twice on `backfill`.
- The same spec, in light and dark: at rest, no visible element of any
  row outside its `[data-status-mark]` has the resolved `--ui-error` as
  its colour, border or background. Without the change: the ×.
- The same spec, the menu: `[data-task-cancel]` has count 0 until
  `[data-task-menu="soak"]` opens; `tabTo` the button, Enter opens it,
  Escape closes it, the menu's content is waited to count 0 and the
  focus is back on the button (AGENTS.md: a menu that has closed is not
  gone). "Cancel…" opens "Cancel the task", and "Keep it" closes it
  with no command sent (the stub's route counts none). `migrate`'s menu
  holds "Copy branch" and, where the server has Brakeman, "Ask Brakeman
  why", and no "Cancel…" (the engine names none on a failed task). Enter
  on a row writes `?task=<id>`, waited on the URL, and opens its detail.
- The same spec at 390 x 844 with touch: each row folds into two lines
  with the id and the state's word `uncut`; a tap on `soak`'s menu and
  on "Copy branch" shows "Branch copied"; `noSideways` holds.
- Moved: `decisions.spec.ts:122` (`attempt 1 waits for decision <id>`)
  expects `waits for decision <id>`. `crew-people.spec.ts:210` (the docs
  row's "owner ana, the acceptance owner") expects no
  `[data-task-owner]` in that row; `:207` (the people chip "acceptance
  owner") and `:212` (the same words in the task's detail) stand, so the
  fallback is still proved. `brakeman-cards.spec.ts:505-508` open
  `[data-task-menu="build"]` and find `[data-task-ask-brakeman="build"]`
  on the page, since the menu draws in a portal outside the row, then
  the same for docs after Escape. `manifest.spec.ts:378` opens the
  build row's menu before `[data-task-cancel="build"]`.
  `tasks.spec.ts:114` and `dispatch-links.spec.ts:88` (`attempt 2` in
  `[data-task-attempt]` on a succeeded build) expect the row's words to
  contain "succeeded by attempt 2" and its `[data-task-attempt]` to be
  empty. `tasks.spec.ts:115` and `:188` and `tasks-resume.spec.ts:131`
  stand. No proof is lost.

**By hand.**

- Both themes at 1440 and 390, with 2, 6 and 40 tasks: whether one word
  a row is enough to scan down, and the menu's order.
- The rows inside a ticket's Runs section at that column's width.

**Size.** About 230 lines of code and 330 of tests.

**Defaults.**

- **The owner.** Default: shown in a row only when the task names one;
  the acceptance owner is said once, in the header's people and the
  task's detail. Why: the review's six identical owner lines.
- **The attempt.** Default: said once: in the words when they name it,
  else "attempt N" in the who column when N is above 1. Why: "attempt
  2" twice today; attempt 1 is the norm.
- **The words' colour.** Default: uncoloured; the state mark alone
  carries colour. Why: the state is said once.

**Depends on.** PR-9: the Ledger run in `run-fixtures.ts`, and the
header's people, which say the acceptance owner once now that the rows
stop. PR-10:
`taskStatus` and `StatusMark` in the state cell. PR-1: `aligned`,
`uncut`, `tabTo`, `dayPage`.

**Left out.** The palette's task rows (PR-10 gives them `taskStatus`).
The engine's own reasons, which may name an attempt. The graph (PR-33).
Dispatch's run card badges, which keep `taskBadges`.

### PR-12: web: one toolbar on a Dispatch project, and columns that line up (P8)

A project's page opens with its description as one line, then one
toolbar, then the tickets. The eight filters sit behind a Filter
button, and the ones in use show as chips that take themselves out.
The saved views sit behind a Views button that names the view applied.
Each ticket's milestone, priority, owner, assignees and age sit in a
column of their own, drawn empty when the ticket has none, so a column
reads straight down. At 1440 x 900 the first ticket starts about 147 px
down, and the fourteen tickets of a busy project fit with no scroll.
The address and its codec, the count, the sort, the saved views on the
server, the board, the roadmap and the page's keys stay as they are.

**Today.** `pages/dispatch/[project]/index.vue` stacks five things
above the list (586-618). The description is drawn whole by
`TicketMarkdown` in a box up to 288 px tall (`max-h-72`, 588-590),
shown unless folded, which the browser keeps in
`conductor.dispatchAbout` (285-301). A row holds Fold or About, the
List, Board and Roadmap tabs, the tracker's link and the count
(591-607). `DispatchViewsBar` (608) is a row of chips that says only
"No saved views yet." when there are none (`DispatchViewsBar.vue:178`).
`DispatchFilters` (609) keeps its eight controls open at `sm` and up,
each 144 px wide beside a 224 px search, the sort and its direction,
and they wrap to two rows (`DispatchFilters.vue:103-189`); the UX
review's seeded build put the first ticket 400 px down a 900 px
window. A row of `DispatchTicketList.vue` (95-145) is a flex line
whose right part draws the labels, the milestone only when set, the
priority, the owner only when set, the assignees only when any, then
the time (121-144), so only the time lines up from row to row. The
filter's `parent` and `deleted` narrow the list
(`utils/dispatchQuery.ts:51-52`) but only the address sets them and
nothing on the page shows them, and the phone's "Filters · n" counts
the eight and leaves out the search, `parent` and `deleted`
(`DispatchFilters.vue:87-90`).

**The change.**

- `utils/markdown.ts`: `descriptionLine(markdown)`, pure, through the
  `markdown-it` the renderer already uses. It returns the first
  paragraph of prose as plain text (a link gives its text, emphasis
  and code marks go, an image gives nothing), a heading only when the
  description has no paragraph, and `''` for an empty one. The
  paragraph comes back whole; the cut is the CSS's.
- `pages/dispatch/[project]/index.vue`: under the header, one line
  (`data-project-description-line`): the description's line,
  `truncate`, whole in its `title`, with the tracker's link at its end
  (`data-project-tracker`, moved from 594-605) and the About toggle
  (`data-project-about-toggle`). With no description the line reads
  "Add a description of CND…" and opens the editor. A click on the
  line or on About opens today's `TicketMarkdown` in place under it,
  with `data-project-about` and its `data-markdown-*` hooks unchanged.
  `conductor.dispatchAbout` now keeps "opened", so the default is the
  one line; a browser that kept "shown" starts at the line, a
  per-viewer convenience the pull request names.
- `components/DispatchFilters.vue` becomes the toolbar
  (`data-dispatch-toolbar`), one row at `sm` and up, left to right: a
  `#leading` slot the page fills with the view tabs (icons only below
  `sm`); the search, which keeps its 300 ms pause, `MAX_SEARCH` and
  `data-filter-search`; Filter (`data-filter-open`, "Filter" or
  "Filter · 2", with `data-count`), which opens a Nuxt UI `UPopover`
  (`data-filter-popover`) holding the eight controls in two columns,
  each with its `data-filter-*` hook; a chip per filter in use
  (`data-filter-chip="<field>:<value>"`), each with an x
  (`data-filter-chip-remove`), then Clear (`data-filter-clear`, kept);
  a `#trailing` slot the page fills with the count (`data-list-total`
  and `data-count`, kept) and Views; then the sort and its direction
  (`data-filter-sort`, `data-filter-direction`, kept). Chips that do
  not fit wrap onto a second line of the toolbar; with none in use it
  is one row. Below `sm` it is two lines: the tabs and the search,
  then Filter, Views, the sort and the count. The phone's unfold
  (104-118, `data-filter-unfold`) and the `applied` count (87-90) are
  deleted.
- `utils/dispatchQuery.ts`: `filterChips(filter, names, agents)`,
  pure: one chip a value, each with its words and the filter without
  it: "Status: In progress", "Priority: Urgent", "Label: api", "Owner:
  Ann", "Assignee: Ann" (an agent by its catalog name), "Milestone:
  v1", "Horizon: Next", "Blocked", "Parent: CND-4", "Deleted too" and
  "Search: login". Taking a chip out keeps `sort`, `desc` and `group`.
  Filter's number is the count of chips other than the search's, so
  `parent` and `deleted` are on screen for the first time.
- `components/DispatchViewsBar.vue` becomes the Views button and its
  menu. Its root keeps `data-views-bar` with `data-loaded`; the button
  (`data-views-open`) says "Views", or "Views · Doing" with
  `data-applied="<id>"` while a view is applied. A `UPopover`
  (`data-views-menu`) lists each saved view as a row with its kind
  icon and its scope (`data-view-chip`, `data-view-name`,
  `data-view-every`, `data-active`, `data-view-apply`), its own menu of
  Rename… and Delete… (`data-view-menu`), then "Save the filter as a
  view…" (`data-view-save`). With none, the menu's one line is "No
  saved views yet."; the page has no row for it. The two dialogs and
  `defineExpose({ views, openSave, apply })` stay, so the palette's
  view rows (index.vue:543-546) work as before.
- `components/DispatchTicketList.vue`: from a container width of 48rem
  (`@container` with `@min-[48rem]:`, as `CrewPeopleTable.vue:60-61`
  does) each row is a CSS grid on one template for every row: select
  (0, or 1.25rem while any row is selected, so every row moves
  together), status 1rem, key 4.5rem, title `minmax(0,1fr)` (the title
  cut with "…", then the blocked badge, at most two labels and "+n"),
  milestone 8rem, priority 1.25rem, owner 7rem, assignees 4.5rem (three
  avatars overlapped, then "+n"), age 3rem. Every cell is drawn on
  every row, with `data-empty` when the ticket has none:
  `data-row-milestone`, `data-row-priority`, `data-row-owner`,
  `data-row-assignees`, `data-row-updated`. The template is one
  exported constant, `TICKET_ROW_TRACKS` in `utils/dispatch.ts`, which
  PR-37 names as the list row. Below 48rem line one is the status, the
  key and the title, wrapping as today; line two, under a 24 px indent,
  is a grid of priority, the owner as an initials avatar, assignees,
  milestone and labels (cut), and age, so priority and age line up
  there too.
- The page's palette group gains "Filter the tickets…"
  (`page:filter`), which opens the popover. No key is added: F is
  fullscreen and / is the palette on every page
  (`composables/useShortcuts.ts:42`, `:71`; `layouts/default.vue:271`).
- `docs/plans/dispatch.md` (646-650, "the filter bar" and "the saved
  views as chips") says the toolbar and the Views menu; a
  `docs/features.md` entry.

**Tests.**

- `web/app/utils/dispatchQuery.test.ts` (vitest): `filterChips` gives
  one chip a value for each of the eight filters, `q`, `parent` and
  `deleted`, with the words above; each chip's filter is the filter
  without that one value, with `sort`, `desc` and `group` kept; a
  label or milestone the project lacks still gives a chip, named as
  the address names it; Filter's number for a filter of `q`, `parent`
  and `deleted` alone is 2. Without the change: the import fails; with
  `applied`'s rule put back the last case reads 0, and a chip built
  from `emptyFilter()` loses the sort and fails the kept-sort case.
- `web/app/utils/markdown.test.ts` (vitest): `descriptionLine` gives
  "Ship the tracker." for `## Goals\n\nShip the **tracker**.`,
  "Goals" for a description that is one heading, the link's text for
  `[the spec](https://example.com) first`, nothing of an image, the
  first paragraph after blank lines, `''` for empty, and a paragraph
  of 300 characters whole. Without the change: the import fails; a
  rule that takes the first block reads "Goals" in the first case.
- `web/e2e/dispatch-toolbar.spec.ts` (Playwright, new), on the seeded
  day's `dispatch` part (read-only, the worker's own server), each page
  opened by `dayPage`. Filters, chips, the search and the selection
  change only the address and the page, never the server. Before any
  measure or key it waits for `[data-list-total]` to carry the count
  the day's part says it made (AGENTS.md: a page not yet drawn passes
  every "hidden"; count only what the test made):
  - At 1440 x 900: the first `[data-ticket-row]` is at most 160 px down
    (`topAtMost`; the header 64, the padding 12, the description line
    20, the gap 10, the toolbar 32, the padding 8 and the list's border
    1 make 147); `rowsInView` over the list's scroller
    (`[data-ticket-list]`'s parent, index.vue:627) counts all 14 rows;
    the description line's box is at most 24 px tall; the toolbar's
    pieces do not overlap (`noOverlap`) and its box is at most 40 px
    tall; `[data-filter-status]` has count 0 until `[data-filter-open]`
    is pressed. Without the change: the first row is about 400 px
    down, 11 or 12 rows are in view, and the filters are on the page.
  - At 1440 x 900, the tracks: it first asserts that the day's rows
    hold at least one cell with `data-empty` and one without for the
    milestone, the owner and the assignees (so the check is not made on
    rows all alike), then `aligned` holds for `[data-row-milestone]`,
    `[data-row-priority]`, `[data-row-owner]`, `[data-row-assignees]`
    and `[data-row-updated]`: every row has the cell, each at one left
    x within 1 px. Then it presses J once and X once, waits for
    `[data-selection-bar]`, and checks `aligned` again for the five and
    for `[data-row-select]` (AGENTS.md: check again after a redraw the
    test causes; a select track missing from the template shifts the
    selected rows only). Without the change: the cells are missing on
    rows without the field and the priority's x moves with each row's
    labels.
  - At 1280 x 720: at least 12 rows in view, and the toolbar one row.
  - At 390 x 844 with touch: `aligned` holds for `[data-row-priority]`
    and `[data-row-updated]`; each row's title is above its age;
    `noSideways`; `uncut` on `[data-list-total]`; Filter tapped, the
    popover lies `inside` the window. Without the change: line two
    wraps by content and the x of the age differs between rows.
  - Filter and chips at 1440: open Filter, pick "In progress" from
    `[data-filter-status]`, wait for the listbox to be gone
    (`toHaveCount(0)`, AGENTS.md: a menu that has closed is not gone),
    press Escape and wait for `[data-filter-popover]` to have count 0;
    the address says `status=in_progress`, the chip reads "Status: In
    progress", Filter says "Filter · 1", and `[data-list-total]` holds
    the day's in-progress count. Its x takes it out of the address and
    the count is the day's whole again. Typing "login" in the search
    gives a "Search: login" chip once the address has `q=login`
    (waited on the address, not on the pause), and Filter's number
    does not change. `?parent=<key>-1` gives "Parent: <key>-1",
    `?deleted=1` gives "Deleted too". Without the change: there are no
    chips, and `parent` and `deleted` show nowhere.
  - Keys: with the Filter popover open, J moves no row's focus (the
    page's keys pause on `role="dialog"`, `useOverlaid.ts:2`); closed
    and gone, J focuses the first row.
- `web/e2e/dispatch.spec.ts` (Playwright), two tests move. "a filter
  whose first read fails is read again" (343-364) opens Filter first,
  picks Backlog, waits for the listbox to be gone, presses Escape to
  close the popover and waits for it to be gone, then asserts as
  before. "a project's description, labels and milestones…"
  (1418-1437) opens the editor from the line, saves, and expects the
  line to read "Ship the tracker." with the rendered `h2` inside the
  opened editor; opened, it stays opened across a reload; folded, a
  reload shows the line and `[data-project-about]` has count 0. The
  old "folded stays folded" is held by the last step.
- `web/e2e/dispatch-views.spec.ts` (Playwright): the saved-view test
  (46-130) opens `[data-views-open]` before each look at a row and
  reads "applied" from the row's `data-active` and the button's
  `data-applied`; for the view of every project saved by the second
  browser, it opens Views on the first page before the second saves
  and expects the row to appear there with no reload (`mark`,
  `marked`). The held-save test (306-338) opens Views before
  `[data-view-save]` and ends on the button saying "Views · Held".
  The address test (20-44) and the phone test (340-375) stay as they
  are; the phone test's `sideways` is `layout.ts`'s `noSideways`.
- `web/e2e/palette-rows.spec.ts` (Playwright): the project group's
  test (455-475) reads the saved view's id and its applied state from
  `[data-views-open]`'s `data-applied`, and runs the new `page:filter`
  row, which shows `[data-filter-popover]`.
- `web/e2e/dispatch-keys.spec.ts` stays as it is and must stay green:
  its hooks are unchanged.

No proof is lost: no test asserted the phone's "Filters" unfold or the
"No saved views yet." row. No theme pass is added: no colour is new
(the chips are neutral, the applied view keeps its primary tone), and
PR-25's walker measures the toolbar's drawn contrast.

**By hand.**

- The title's room at 1024 and 1280 wide, with the sidebar at its
  narrowest and its widest.
- The Filter popover's two columns on a phone.
- Whether a filter in two presses reads well in a day's use.

**Size.** About 480 lines of code and 450 of tests.

**Defaults.**

- **The opened description.** Default: in place under the line, today's
  `TicketMarkdown`, remembered per browser in `conductor.dispatchAbout`
  (now meaning "opened"). Why: it keeps the editor and its hooks.
- **`descriptionLine`'s rule.** Default: the first prose paragraph as
  plain text; a heading only when there is no paragraph. Why: "##
  Goals" as the project's line says nothing.
- **The owner on a phone.** Default: an initials avatar below 48 rem,
  the name at full width. Why: a name track leaves milestone and labels
  about 80 px at 390.
- **Age.** Default: time since last updated, as the row and the default
  sort say today.
- **A header row naming the tracks.** Default: none; each cell keeps its
  title. Why: the measured height has no room for it.
- **Many chips.** Default: they wrap onto a second line inside the
  toolbar; with none in use the toolbar is one row.
- **Views.** Default: a popover list whose rows keep their own Rename…
  and Delete…, the button saying "Views · <name>". Why: every
  `data-view-*` hook and both dialogs are kept.
- **Where the full tracks start.** Default: a 48 rem container. Why: the
  fixed tracks take about 564 px, so below about 770 px the title has
  no room.
- **Labels in a row.** Default: at most two chips after the title, then
  "+n", the title cut first.

**Depends on.** PR-1: the day's `dispatch` part (fourteen tickets that
mix labels, milestones, owners and assignees), `dayPage`, and
`web/e2e/layout.ts` (`topAtMost`, `rowsInView`, `aligned`,
`noOverlap`, `uncut`, `inside`, `noSideways`, which takes over
`sideways` from `dispatch-helpers.ts:90`). No open pull request
touches its files (#899, #907, #908, #909, #911, #912 and #913
checked).

**Left out.** A Group control: the list draws no groups yet, though
`group` is kept in the address and in views (`dispatchQuery.ts:43`).
The board's colours are PR-24's; the page's empty and reading states
(index.vue:620-626) are PR-23's, which comes after this row; the
toolbar and the list row written down as rules are PR-37's. The
projects page (`pages/dispatch/index.vue`) is not changed.

### PR-13: web: one inbox: the sidebar lists everything that needs you (P3, the list)

The sidebar's Needs you lists every kind of thing that waits on a
person, each with its kind's mark: questions, plans waiting for
approval, tasks out of their attempts and a run's decisions. A run
that waits on you leaves Running at the moment it enters Needs you,
its waiting item under its header. The section's count is the nav's
number. The palette's two groups of what waits become one, "Needs
you", with no question listed twice.

**Today.** Needs you holds session rows from `useAttention` and the
open decisions no question mirrors of running runs
(`SessionSidebar.vue:370-430`, the decision row at `:413-424`); it has
no row for a plan or a task. A run is placed by its members alone
(`blockState`, `utils/sidebar.ts:209-214`), so a run whose task failed
or whose gate no member asks sits in Running with a green mark. The
inbox the sidebar would need is already read for the whole app
(`layouts/default.vue:11-12`; `useInbox.ts:73-80`): its summaries carry
`ref`, `kind`, `title`, `question` and `brief` (`utils/inbox.ts:11-30`),
and a task's ref names its run (`task:<run>/<task>`). The palette has a
"Needs you" group (answer-all, the questions, the decisions as "Answer
dr-7", `usePaletteRows.ts:940`) and a "Needs your decision" group (a
row per inbox item, questions among them, `:948`), so a question shows
in both.

**The change.**

- `utils/sidebar.ts`: `sidebarModel(sessions, runOf, needs)` takes
  `useNeedsYou`'s items. A run in `needs.runs` goes to Needs you in the
  same call that lists its items, and its items sit under its header,
  before its members. A stopped run with an open decision sits there
  too, its members drawn exited. A plan, and a decision whose run the
  store does not hold, are loose rows of the section. `focusRows` gives
  each item a row (`i:<ref>`), in the list's order, none from a folded
  section. The rail's capsule of a waiting run is amber. A run in
  `needs.runs` with no session listed gets a block of its own from
  `runOf` (its header and its items, no member rows), so a gate held
  before any attempt, or a run whose sessions aged out of the listing,
  is drawn as well as counted.
- `SidebarInboxRow.vue` (new): a button row with the kind's mark and
  word from `kindLook` (PR-6), the title on one line and the question or
  brief on one line, cut with its whole text in its title. It carries
  `data-sidebar-item="<ref>"` and `data-sidebar-item-kind`. A task or a
  plan opens `/decisions?item=<ref>`; a decision opens its card
  (`useDecisionModal`), as the decision row does today, and keeps
  `data-sidebar-decision`.
- `SessionSidebar.vue`: the section draws the model's items and counts
  `useNeedsYou().number`, the same number as the nav. PR-5's "+N on the
  page" words in its header go (`sectionHeader` and
  `[data-section-more]` are removed), since the section now lists every
  kind. Below the rows:
  "Showing 150 of 212 · all on Needs you" when the inbox holds a next
  page; "Plans and tasks could not be read" with Read again
  (`inbox.changed()`) after a failed read, the questions still listed;
  the decisions' cut and stale lines as today.
- `usePaletteRows.ts`: one group, "Needs you": answer-all, the
  questions, the decisions as "Answer dr-7", then the plans and tasks as
  "Plan: <title>" and "Task: <title>" (the words from `kindLook`), each
  opening its item on the page. The group "Needs your decision" and
  `decisionRows`' question rows go; `oneRowPerDecision` stays.
- The pill is unchanged: it steps through what it can answer and says
  how many more wait on the page (PR-5).
- `docs/features.md`: the entry for Needs you.

**Tests.**

- `web/app/utils/sidebar.test.ts` (vitest): a run whose members all
  work but whose task failed is in `needs`, not `running`, in one call,
  with the task under its header before its members; a stopped run with
  an open decision is in `needs`; a plan is a loose row; `focusRows`
  orders `i:<ref>` rows with the list and gives none from a folded
  section; `railModel` marks the waiting run amber; a gate's run with
  no session, and a failed task's run whose attempts are no longer
  listed, each draw their item under a header. Without the change: the
  run is in `running` and there are no item rows.
- `web/app/composables/usePaletteRows.test.ts` and
  `web/app/utils/decisions.test.ts:346-358` (vitest): the case "keeps
  the first row of each decision across the groups" becomes one group in
  which a question that is also an inbox item has one row, and a plan
  and a task have one row each with their kind's word. Without the
  change: the question has two rows, in two groups.
- `web/e2e/counts.spec.ts` (Playwright), extended on the day's `busy`
  part: the sidebar's section reads the oracle, computed from the
  inbox's refs and the sessions as PR-5's case computes it, at the same
  poll as the nav, and its header has no `[data-section-more]`; each
  item is listed once, by its ref or its session's id, with its
  `data-sidebar-item-kind`; the failed task's run is in Needs you and
  `[data-sidebar-section="running"] [data-sidebar-run-block="<run>"]`
  has count 0, both read in one evaluate so the answer is of one moment;
  Enter on the plan's focused row opens `/decisions?item=plan:…`; a
  click on the gate's row opens its card. At 390 x 844 with touch the
  phone's list holds the plan and the task rows, each a 44 px target
  (`tapTarget`). The palette lists the loose question once. Without the
  change: the section reads less than the nav, the plan and the task
  are not listed, and the run is in Running.
- `web/e2e/counts.spec.ts`, the reads that fail: `inbox-stub.ts`
  answers `/api/inbox` with 500, and Needs you shows the could-not-read
  line with the questions still listed; the stub let go, Read again, and
  the line is gone once the stub has counted the read (wait on the
  mechanism); the stub answers a first page with a next cursor and a
  total of 212, and the line says so.
- `web/e2e/decisions-gate.spec.ts:106-136` and `:247-258` stand: the
  gate's row keeps `data-sidebar-decision`, the pill still steps over a
  session and a gate, the stale line and Read again are unchanged.
- `web/e2e/sidebar.spec.ts:48-112` stands: a run placed by its asking
  member is placed the same way.

**By hand.** The four kinds' marks told apart at 200 px in both themes;
nine rows of the review's day read top to bottom at 1440 x 900.

**Size.** About 280 lines of code and 360 of tests.

**Defaults.**

- **Where a waiting run's items sit.** Default: under its header, before
  its members; a plan, and a decision whose run the store does not
  hold, are loose rows.
- **The rail.** Default: the capsule of a waiting run turns amber; the
  rail is drawn from the same model.

**Depends on.** PR-5 (the one number, the counts spec, the name). PR-6
(`kindLook`). PR-7 (the run's block with its asking members first) and
PR-8 (the two-line question, which makes the rows short enough for
nine). PR-1 (the day's `busy` part, `inbox-stub.ts`).

**Left out.** The pill does not step through plans and tasks, which it
cannot answer. The page's own rows and their marks (PR-6, PR-21). A
plan of a ticket is not placed under its ticket: tickets are not in the
sidebar.

### PR-14: web: finished apart from working (P4)

Running splits in two. Finished holds the agents that ended their turn
and wait for your next word, grey, "finished 12m ago". Working holds
the agents in a turn, and the agents that send no signal, each with
its own mark. The order is by how hard a thing waits on you: Needs
you, Finished, Working, Shared with you, Exited. A run sits once, where
its busiest member is, and says "2 working · 2 finished".

**Today.** `RowState` is `needs`, `running`, `idle` or `exited`
(`utils/sidebar.ts:140`); `rowState` (`:194-198`) reads only whether a
session asks and whether its process runs, so an agent in a turn and
one that finished it are both "running" in one section. The sections
are `needs`, `running`, `shared`, `exited` (`:142-145`); the folds are
saved under those keys (`:317-320`); a run's subtitle is "started 08:31
· 3 agents" (`blockSubtitle`, `:294-299`). The palette's groups and its
fold actions use the same four (`usePaletteRows.ts:579-583`,
`:936-942`).
The hooks' working and done already reach the client as attention
states (`AttentionState`, `utils/protocol.ts:171`), and PR-2 makes them
true for every agent that reports.

**The change.**

- `utils/sidebar.ts`: a row's state is PR-3's `sessionStatus` key:
  `needs`, `finished`, `working`, `running` (no signal; `starting` takes
  its mark with the word "starting") and `exited`. The sections are
  `needs`, `finished`, `working`, `shared`, `exited`. `working` holds
  the working, running and starting rows. A host away that asks is
  `needs` by PR-3's rules ("needs you · host away") and stays in Needs
  you, counted in the one number, its answer held until the host is
  back, as today; one that does not ask takes the exited mark with
  "host away" and sits in Exited.
- A run's block goes to Needs you when a member asks or the run waits
  (PR-13), else to Working when a member works, runs or starts, else to
  Finished, else to Exited. `blockSubtitle` adds the members' states in
  PR-3's words: "started 08:31 · 2 working · 2 finished".
- Within Finished, the latest finished first (the attention's `since`);
  "finished 12m ago" from PR-3's `useNow`.
- `readFolds` reads a saved `running` fold as `working`; Finished starts
  unfolded.
- `SessionSidebar.vue`, `SidebarSection.vue`: the two sections, each
  drawn only when it has rows; the folded preview's squares and the
  rail's squares from `STATUS_LOOK`.
- `usePaletteRows.ts`: the group "Running" becomes "Finished" then
  "Working", in the sidebar's order; the fold actions name the five
  sections.
- `docs/features.md`: the sidebar's sections.

**Tests.**

- `web/app/utils/sidebar.test.ts` (vitest): each `sessionStatus` key to
  its section, the host away both ways; `sidebarModel` gives the
  sections in their order; a run of two working and two finished sits
  in Working once and its subtitle says "2 working · 2 finished"; a run
  of finished members sits in Finished; `readFolds` of `{"running":
  true}` folds Working; `sectionPreview` gives the new keys. Without the
  change: one Running section, and the old fold reads as nothing.
- `web/app/composables/usePaletteRows.test.ts` (vitest): the groups
  Finished and Working in that order, and the five fold actions.
  Without the change: one group "Running".
- `web/e2e/sidebar.spec.ts` (Playwright), "finished and working apart",
  at 1440 x 900 and on the phone's list at 390 x 844 with touch: two
  sessions of the test's own, one set working and one done through
  `POST /api/sessions/{id}/attention`, each waited for on the API; they
  land in `[data-sidebar-section="working"]` and
  `[data-sidebar-section="finished"]`, with `data-status-mark` working
  and finished, the second's word matching `/^finished( \d+[smh]
  ago)?$/`; Finished is above Working (their boxes' tops). The first set
  done: it moves to Finished, waited for by its id in that section (a
  state that holds). A fold of Finished survives a reload. A crew of two
  set one working and one done sits in Working once, with "1 working · 1
  finished", and nothing of it in Finished. Without the change: both
  sessions in Running with one mark.
- Nine assertions that a row is `running` after its answer move to
  `data-row-state` matching `/^(working|finished|running)$/` (the row
  left Needs you and lives; a stub may report a turn or none):
  `askwait.spec.ts:197`, `choices.spec.ts:73`, `codexworking.spec.ts:
  178`, `:182`, `:213`, `phone.spec.ts:82`, `question.spec.ts:207`,
  `sidebar.spec.ts:320`, `:391`. Where a spec sets the state through the
  attention route it asserts the exact one.
- `web/e2e/sidebar.spec.ts:108-111`, `:171-181`, `:200` (the Running
  section, its count, its fold and its preview) read the Working
  section: the stub sends no signal, so its row is `running` in
  Working.
- `web/e2e/sidebar.spec.ts:482` (the rail capsule's label "review needs
  you · lead running") stands: lead sends no signal.
- `counts.spec.ts` (PR-13's): the failed task's run block is found once
  in the whole sidebar, inside `[data-sidebar-section="needs"]`, in
  place of the absence under `running`, which this row renames away.

**By hand.** The green ring and the grey ring told apart in both themes
at 200 and 280 px; a run moving between Working and Finished as its
members end their turns, with nothing in the list jumping twice.

**Size.** About 190 lines of code and 300 of tests.

**Defaults.**

- **Rows with no signal.** Default: "running" and "starting" rows sit in
  Working under their own marks; a host away that asks stays in Needs
  you, one that does not ask sits in Exited.
- **The palette's groups.** Default: its Running group becomes Finished
  then Working, and its fold actions name the five sections.

**Depends on.** PR-3 (`sessionStatus`, `STATUS_LOOK`, `useNow` and the
marks). PR-13 (a waiting run's place, and the sidebar's model it
extends). PR-2 through PR-3, so that "finished" is true for every agent
that reports.

**Left out.** The Yard's chips Working and Finished (PR-17). A
section of its own for agents that send no signal: they sit in Working
under their own mark.

### PR-15: web: the pages as six tiles (P6)

The full sidebar's foot is six tiles in two rows: Needs you, Yard,
Crews, Dispatch, Brakeman and More. More holds Roundhouse, Agents,
Events, History and, in the desktop app, Settings. The foot takes about
116 px where it took about 305, and the list gains the rest. The Needs
you tile stays at zero and loses its number. The phone's More gains
Brakeman, so every page is reachable from both. The rail keeps every
page as an icon.

**Today.** The full sidebar's foot is a vertical `UNavigationMenu` of
`nav` (`layouts/default.vue:356-357`, `:216-240`): up to ten rows. The
rail draws the same items as icons (`railNav`, `:245-258`). The
phone's bar is Decide, Sessions, Yard, Crews and More, More holding
Roundhouse, Agents, Dispatch, Events, History and Settings
(`BottomBar.vue:22-43`); the phone has no way to Brakeman, and
`phone.spec.ts:154` asserts More without it. `brakeman.available` is
`undefined` until the server answers (`useBrakeman.ts:63`), and the
nav's Brakeman row appears only then (`default.vue:236-238`).

**The change.**

- `utils/pages.ts`: `navTiles({ brakeman, desktop })`: the six, then
  More's list. While `brakeman` is unknown its slot is held, an empty
  cell of a tile's size, so nothing moves when it fills; when the server
  says it has none, More moves into the slot, once. `phoneMore` adds
  Brakeman after Dispatch while the server has it.
- `SidebarTiles.vue` (new): a three-column grid of Nuxt UI `UButton`s,
  an icon over a label, the badge on the corner; More a
  `UDropdownMenu`, lit while one of its pages is open. Each tile sits in
  a `UTooltip` with `pageTip` and its chord, shown on hover and on
  focus. The tiles keep `data-sidebar-decide`, `data-sidebar-brakeman`,
  `aria-current` and `pageActive`'s rules; the foot stays a landmark
  named "Pages".
- `layouts/default.vue`: the full sidebar's foot draws `SidebarTiles`;
  the rail keeps its icon menu. The Needs you tile stays whatever
  `needsRowShown` says, with the number only above zero.
- `BottomBar.vue`: More's list from `phoneMore`.
- The README's sidebar paragraph and `docs/features.md`.

**Tests.**

- `web/app/utils/pages.test.ts` (vitest): `navTiles` with Brakeman,
  without it, and while unknown (the held slot); Settings in More only
  in the desktop app; `phoneMore` with and without Brakeman. Without the
  change: no function, and More lacks Brakeman.
- `web/e2e/sidebar.spec.ts` (Playwright), "the pages as six tiles", at
  1440 x 900 and 1024 x 768 (the narrowest window that shows the
  sidebar) at 200 px on `dayPage`: the foot is at most 120 px tall; six
  tiles, each label `uncut` and each tile `inside` the sidebar, with
  `noOverlap`; More opens by a click and by Enter after `tabTo`, lists
  Roundhouse, Agents, Events and History, and each one opens its page
  (wait for the menu to be gone, `toHaveCount(0)`, after each pick);
  More is lit on `/history`; Crews is lit on a run's page; G then H
  still goes to History; a tile's tooltip shows on focus. On the day's
  `busy` part the list is at least 600 px tall at 1440 x 900. Without
  the change: the foot is about 305 px and has no More.
- `web/e2e/sidebar.spec.ts`, "Brakeman's slot is held": with the
  Brakeman read held unanswered by a route, the slot is an empty cell
  and More's box is noted; the read answered, the Brakeman tile fills
  the slot and More's box is where it was (a starting value is not a
  result: the test compares after the fill, not before); with the read
  answered as a server without Brakeman, More moves into the slot. At
  zero on a server of its own (`ownServer`), the Needs you tile is there
  with no badge. Without the change: no tiles; with a slot that is not
  held, More moves when Brakeman arrives.
- `web/e2e/phone.spec.ts:154` reads More as Roundhouse, Agents,
  Dispatch, Brakeman, Events, History; each is a 44 px target
  (`tapTarget`) at 390 x 844 with touch.
- `web/e2e/sidebar.spec.ts:435` (the foot's links in order) becomes the
  six tiles in order and More's list.
- `web/e2e/settings.spec.ts:1287` (clicks the sidebar's Settings) opens
  More first. `web/e2e/brakeman.spec.ts:169-287`,
  `palette-rows.spec.ts:602` (`[data-sidebar-brakeman]` with or without
  its count) and `decision-page.spec.ts:474` (`[data-sidebar-decide]`)
  stand on the tiles.

**By hand.** The tiles at 200 and 280 px in both themes, the Needs you
badge legible on its tile; More's menu beside a short window's foot.

**Size.** About 160 lines of code and 230 of tests.

**Defaults.**

- **Brakeman's tile.** Default: its slot is held as an empty cell while
  the server has not said, and More moves into it only when the server
  says it has none. Why: nothing moves on the common load.
- **The phone's More.** Default: Brakeman added after Dispatch, the
  order otherwise kept.
- **The rail.** Default: it keeps every page as an icon; P6 is about the
  full sidebar's foot.

**Depends on.** PR-5 (the name, the one number and the rule that a
tile stays at zero). PR-13 (the list the tiles give room to holds
every kind, so the gain is measured on the review's nine rows). PR-4
(`pages.ts`). PR-7 (the 200 px width the tiles are measured at). PR-1.

**Left out.** The rail stays a column of every page. The Roundhouse
becoming a view of the Yard (PR-17) and Events going into History
(PR-29) shorten More later, by editing the table. The phone's bar keeps
its five places.

### PR-16: web: a ticket's strip of sections; a plan folds once approved (P9)

A ticket's page has, under its title, one strip that stays at the top
as the page scrolls: each section by name, with its state where it has
one ("Plan approved", "Runs 2 of 6 done · 2 need you",
"Decisions 2 open", "Comments 3"). A press on a name brings that
section up under the strip. An approved plan whose approval holds
folds to one line of its members, tasks, checks, commit and yolo, with
"Show the whole plan"; a proposed, stale or superseded plan stays
open, since that is when a person reads it. Each member's command
line is printed once, in the plan. The sections, their order, what
they hold and the Review view stay as they are.

**Today.** `components/DispatchTicketPage.vue` draws, under the title
(887-918), the Review view (921-945) or two columns (948-1010): twelve
sections in one column (Description, Acceptance criteria, Plan, Runs,
Decisions, the close card, Links, People, Children and relations,
Attachments, Comments and History, 950-965) and the properties aside,
the only thing that stays as the page scrolls (`lg:sticky lg:top-4`,
968). No section has an id, and nothing on the page links within it.
The panel's body is the scroller (`body: 'p-0 sm:p-0 gap-0'`, 839).
`DispatchPlan.vue` always draws the featured revision's whole body
(`DispatchPlanBody`, 155), approved or not; only the other revisions
fold (204-230). `DispatchPlanBody.vue` prints a member's own arguments
("Arguments:", 66), then the command it runs as, which holds them
again ("Runs as", `data-plan-member-command`, 67-69), and
`DispatchPeople.vue` prints that command a second time for the
approved revision (`data-people-plan-argv`, 59-67). What a strip needs
is read already: the plans (`useTicketPlans`), the runs with their
task counts and questions (`useTicketRuns`; `RunSummary`,
`composables/useDispatch.ts:559-579`), the decision records, read
again on every decision event (`useTicketDecisions`), the close card
(`closeCard`, `utils/dispatch.ts:418`) and the newest 50 comments
(`COMMENT_PAGE`, `utils/dispatch.ts:1006`).

**The change.**

- `utils/ticketSections.ts` (new), pure. `ticketSections(input)`
  returns the nine sections the strip names, in the page's order, each
  `{ id, label, words, tone, count }`: Description and Acceptance,
  names only; Plan, "approved", "approval stale", "proposed" or
  "none", or "r2 waits for your approval" (warning) while PR-4's one
  list holds a plan item of this ticket; Runs, the newest run's done
  part from PR-10's `runStatusLine` summary variant and the ticket's
  waits ("2 of 6 done · 2 need you", the words of `runStatusLine`,
  warning while anything
  waits), its count the ticket's runs; Decisions, "2 open" (warning)
  or "none open", from the records whose state is open; To close, from
  `closeCard`, "closed", "ready" or "1 of 2 met"; People, a name;
  Comments, the live comments' count, or "50+" when an earlier page
  exists (the first comment's `n` above 1); History, a name. Links,
  Children and relations and Attachments are not named.
- In the same file, `ticketWaits(items, ticket)`: the items of
  `useNeedsYou()`'s one list that are this ticket's, a plan item
  `plan:<KEY-n>/<n>` and a question, task or decision whose run is one
  of the ticket's runs. The list is filtered, never counted again
  (invariant 2: one list, one number).
- `components/DispatchTicketStrip.vue` (new, `data-ticket-strip`): a
  `<nav aria-label="The ticket's sections">` of buttons
  (`data-strip-item="<id>"`, `data-tone`, `data-count`), `sticky top-0
  z-10`, 40 px tall, on the page's own background with a bottom
  border, under the title and above the two columns. A press scrolls
  its section into view at once (`scrollIntoView({ block: 'start' })`,
  never a hash, so the router does not scroll the window) and gives it
  the focus. On a phone it is one line that scrolls sideways inside
  itself. The Review view draws no strip.
- `DispatchTicketPage.vue`: each section's component tag gets an `id`
  (`ticket-plan`, `ticket-runs` and so on), `tabindex="-1"` and
  `scroll-mt-14` by attribute fallthrough (each has one `<section>`
  root), so the section components do not change. The aside's
  `lg:top-4` becomes `lg:top-14`. The page builds the strip's input
  from what it reads already; the strip reads nothing of its own.
- `utils/dispatchPlans.ts`: `planFold(revision, body)`, pure:
  `folded` only for an approved revision whose approval holds
  (`planRemedy` null: no blocking `planReasons`), and the line "2
  members · 3 tasks · 4 checks · from 1a2b3c4d5e6f · Yolo off", with
  singulars for one (`expectedChecks`, `shortCommit`, `yoloWords`).
- `DispatchPlan.vue`: a folded revision draws that line
  (`data-plan-fold`) and "Show the whole plan"
  (`data-plan-unfold-body`, `aria-expanded`) in place of
  `DispatchPlanBody` (155); opened, the button says "Fold the plan".
  The state badge, the approval line and its note, the reasons,
  Launch, Revise, Withdraw, the blockers and Launches stay outside the
  fold. Opened lasts the page, and the fold comes back when another
  revision is featured. Nothing is stored.
- `DispatchPlanBody.vue`: the "Arguments:" line (66) goes, since "Runs
  as" holds them.
- `DispatchPeople.vue`: "The agents of r1, as it was approved" keeps
  each member's name and agent (`data-people-plan-agent`,
  `data-agent`) and gains a link "in the plan" (`data-people-plan-link`)
  that brings the Plan up as the strip does; the command
  (`data-people-plan-argv`, 64) goes. Each launch's agents with their
  versions (69-83) stay.
- `docs/plans/dispatch-evidence.md` (1012-1015, People's "the plan's
  agents with their arguments") and the comments at
  `DispatchPeople.vue:10-16`, `DispatchPlanBody.vue:7-12` and
  `DispatchPlan.vue:8-16` say the new shape; a `docs/features.md`
  entry.

**Tests.**

- `web/app/utils/ticketSections.test.ts` (vitest, new): the nine
  sections in the page's order; Plan's words follow `planStateWord`
  for proposed, approved, stale and none, and say "r2 waits for your
  approval" with a plan item of this ticket in the list; Runs from a
  summary of `{ succeeded: 2, running: 1, ready: 3 }` says "2 of 6
  done", the same words `runStatusLine`'s summary variant gives for it
  (the strip says what the run card says); from one list holding a
  member's question and a decision of this ticket's run, a plan item
  of this ticket, and one of each for another ticket, Runs says "2
  need you" with tone warning and Plan is warning, and with
  nothing waiting both are neutral; Decisions counts only open
  records; Comments says "3", "50", and "50+" when the first comment's
  `n` is 4. Without the change: the import fails; a waits rule that
  matches by kind instead of by run and ticket counts the other
  ticket's items and reads 4 where the test wants 2.
- `web/app/utils/dispatchPlans.test.ts` (vitest), beside "a revision's
  words" (56-105): `planFold` folds an approved revision that holds,
  with the line "2 members · 3 tasks · 4 checks · from 1a2b3c4d5e6f ·
  Yolo off", says "1 member" and "Yolo on" where they apply, and does
  not fold a proposed, a superseded, or an approved but stale
  revision. Without the change: the import fails; a fold that tests
  only `state === 'approved'` folds the stale one and fails that case.
- `web/e2e/ticket-strip.spec.ts` (Playwright, new, serial), on a server
  of its own (`ownServer`, PR-1, as `decisions-gate.spec.ts` has one),
  since it answers a decision. Its `beforeAll` makes one ticket with a
  60-line description and three comments, and a crew of two members
  whose task `deploy` waits for `build` behind a decision `ship`; the
  revision is proposed, approved and launched through the routes the
  Plan section calls (the shape of `launchFrom` in
  `dispatch-delivery.spec.ts`), and it waits on the record for the
  gate to be open (`decisionUntil`). Its `afterAll` stops the run and
  then the server (AGENTS.md: end what a test started before its
  directories go). Pages are opened by `dayPage` on this server.
  - The strip stays, at 1440 x 900: the scroller is the nearest
    ancestor of `[data-ticket]` that scrolls; the test asserts its
    `scrollHeight` is at least twice its `clientHeight`, so the page is
    long enough to prove anything, sets `scrollTop` to the end and
    polls until it is there (waited on the mechanism, not a clock).
    Then the title is above the scroller's top (the scroll happened:
    AGENTS.md, a starting value is not a result), the strip's top is
    the scroller's top within 1 px, and `[data-ticket-properties]`'s
    top is at or below the strip's bottom. A comment posted through the
    API makes the strip say "Comments 4" with no reload, and the strip
    is checked again where it was (a redraw the test causes). Without
    the change: there is no strip; with `lg:top-4` kept the aside's top
    is above the strip's bottom.
  - Each name brings its section up: back at the top, a press on
    Comments, then the test polls until `[data-ticket-comments]`'s top
    is at or below the strip's bottom and within 24 px of it, and the
    focus is inside it; the same for Decisions. Without `scroll-mt-14`
    the section's heading lands under the strip.
  - The states follow the page's reads: Plan says "approved";
    Decisions says "1 open" with `data-tone="warning"`; Runs says
    "1 needs you". The gate answered through
    `POST /api/decisions/{id}/respond`, the test waits on the record
    (`decisionUntil`, answered), then the strip says "none open" with
    tone neutral and the window was not loaded again (`mark`,
    `marked`).
  - The fold: `[data-plan-featured] [data-plan-body]` has count 0;
    `[data-plan-fold]` holds "2 members", the commit's 12 characters
    and the yolo words the API reports; `[data-plan-launch]` is
    visible. "Show the whole plan" draws the body; then
    `[data-plan-member-command]` counts 2, `[data-people-plan-argv]`
    counts 0 on the page, and `[data-plan-body]` holds no
    "Arguments:". The ticket's title edited through the API makes the
    approval stale: the featured revision opens with its body and
    `[data-plan-fold]` has count 0, with no reload. Without the change
    the body is always drawn and People prints the command
    (`DispatchPeople.vue:64`).
  - Both themes (`dayPage`'s theme, `conductor-color-mode` set before
    load): scrolled to the end, the strip's computed background is
    opaque (alpha 1). A strip without its own background shows the
    text under it.
  - At 390 x 844 with touch: before a scroll the strip's top is at or
    below the title's bottom; `noSideways`; scrolled, the strip is at
    the scroller's top. In the Review view (`?view=review`) the strip
    has count 0.
- `web/e2e/dispatch-delivery.spec.ts` (Playwright): People's check
  (150-155) expects `[data-people-plan-agent="builder"]` with
  `data-agent` worker and `[data-people-plan-argv]` count 0; the
  command is read from the plan after `[data-plan-unfold-body]`
  (`[data-plan-featured] [data-plan-member="builder"]
  [data-plan-member-command]` not empty). The launch agents' versions
  (156-160) stay.
- These stay as they are and must stay green: `dispatch-plan.spec.ts`
  (118-132 reads a proposed body, open; 141-146 the approval note,
  outside the fold; 150 onward presses Launch, outside the fold;
  303-316 a proposed body at 390); `decisions-people.spec.ts:118-119`
  (read before the approval); `brakeman-plans.spec.ts:250-263` (r2
  proposed under Other revisions, approved from there);
  `decisions-gate.spec.ts` (its required decisions are in
  `DispatchDecisions.vue`); `crew-people.spec.ts:277-284` (the crew's
  people in People, not the command); `dispatch-views.spec.ts:368-374`
  (the properties under the title at 390, the guard that the strip
  makes the page no wider). No proof is lost: the command People
  printed is held in the plan.

**By hand.**

- Whether the nine names fit on one line at 1280 with the sidebar
  wide, and how the strip reads over a scrolled page in both themes.
- Whether the fold line says enough of what an approval covers.

**Size.** About 320 lines of code and 420 of tests.

**Defaults.**

- **What the strip names.** Default: nine sections (Description,
  Acceptance, Plan, Runs, Decisions, To close, People, Comments,
  History), with states on Plan, Runs, Decisions, To close and
  Comments; links, relations and attachments are not named.
- **The Runs item.** Default: the newest run's done part and the
  ticket's whole waiting count, its count the number of runs. Why: one
  line a ticket, the waits from the one list.
- **A fold remembered.** Default: nothing stored; folded on every visit
  while the approval holds, opened for the page.
- **The strip on a phone.** Default: sticky too, one line that scrolls
  sideways inside itself.
- **Comments past one page.** Default: "50+" when an earlier page
  exists; no server field.

**Depends on.** PR-10: `runStatusLine`'s summary variant in
`utils/status.ts`, so that the strip's run words are the run card's.
PR-4: `useNeedsYou()`, so that the waits are the one list's. Open pull
request #912 merged: it edits `DispatchPlanBody.vue`'s member line,
`utils/dispatchPlans.ts` and `dispatchPlans.test.ts`, which this row
edits too. PR-1 (`ownServer`, `dayPage`, `layout.ts`) comes through
PR-10. #899 touches `DispatchLaunch.vue` and `useDispatch.ts`'s
`PlanCrew` and no file of this row.

**Left out.** While an approved revision is featured, a newer proposed
one stays folded under "Other revisions", as today
(`useTicketPlans.ts:76` features the approved one): the strip's Plan
says it waits, and opening it there is an owner decision. An exact
comment count past 50 needs a field on the ticket, a protocol change in
three places, and is not built. The strip draws words and a tone, no
status marks (the marks are in the Runs section, PR-10's). Links,
relations and attachments get no item in the strip.

### PR-17: web: Yard tiles lead with the name; the Roundhouse is a view of the Yard (P10, P12)

On the Yard every tile's header is its name, whole, and the tile's state
sits at its foot as a mark and a word: "needs you", "working", "finished
28m ago", "running" for an agent that sends no signal, "starting". The
header's chips name the same states: All, Needs you, Working, Finished,
and Running while a session is in it. The Needs you chip counts the
sessions on this grid that ask, and says that it counts only those. A
switch in the header, Grid and Roundhouse, makes the Roundhouse the
Yard's second view: it keeps its route `/roundhouse`, its name, G R and
Alt+R, and it leaves the nav, the rail and the phone's More, whose Yard
row is current on both views. The queue beside the grid stays until
PR-18; focus mode, the Roundhouse's rotation, strip, follow mode and
settings, and a run page's branch and diff in its tiles' foot stay as
they are.

**Today.** `SessionTile.vue` spends its header on the state: the agent's
initials, the name (`truncate flex-1`, :50), the event badge with its
word (:51), Yolo, then the state's dot and word (:53), Resume and Open.
Its `status` computed (:21-27) has three outcomes, "Needs input",
"Running" in green for every running session, and the status word in
grey, so a working agent and a finished one both say "Running"; PR-3
draws `StatusMark` in that place, still in the header. The foot (:62-67)
is a slot whose default is the host ("server" or "hosted · <host>") and
the viewer count, in mono; the run page fills it with the branch and the
diff (`pages/runs/[run].vue:566-569`). The Yard's chips are All, Needs
you and Running (`pages/yard.vue:254-256`): Running is every live
session that does not ask (:43), working, finished and silent together,
and Needs you is an amber button counting `needsInput` on the grid (:42)
by a rule of its own; the palette's chip rows repeat the three
(:213-225). The Roundhouse is a page of its own (`pages/roundhouse.vue`,
titled "Roundhouse" at :373 and in `useHead` at :9) with a row in the
nav (`layouts/default.vue:225`, an entry of `utils/pages.ts` once PR-4
lands) and in the phone's More (`BottomBar.vue:37`). G R and Alt+R
(`default.vue:274`, :288), the palette's page row
(`usePaletteRows.ts:55`) and the old `/carousel` (`pages/carousel.vue`)
open it.

**The change.**

- `components/SessionTile.vue`: the header is the initials, the name
  (`[data-tile-name]`, `min-w-0 flex-1 truncate`, its whole text in
  `title`), the event mark, Yolo's icon, Resume and Open; the state
  leaves it. The foot (`[data-tile-foot]`) begins with a cell of its
  own, outside the slot: PR-3's `StatusMark` with its word, in sans at
  11.5 px, its age from `attention.since` on PR-3's `useNow()`, one
  clock for the page however many tiles. The slot follows the cell, so a
  run page's tiles keep their branch and diff and gain the state. The
  default slot shortens the host to "server" or the host's own name,
  with "hosted · <host>" in its `title`, and keeps the viewer count at
  the right; the host is the part that gives way on a narrow tile.
- `components/EventMarkBadge.vue`: an `icon` prop, as `YoloBadge` has:
  the event's icon from `EVENT_INFO` alone, its label and detail in
  `title` and `aria-label`. The tile uses it so that the name keeps the
  header's room; the sidebar row keeps the word.
- `utils/wall.ts`: `YardFilter` (`all`, `needs`, `working`, `finished`,
  `running`, the keys of PR-3's `sessionStatus`); `yardFilter(sessions,
  filter, now)` and `yardCounts(sessions, now)` over the live sessions
  (`isActive`, as today: a host that is away is not on the grid);
  `yardChips(counts, filter)`, the four chips always and Running while
  its count is above 0 or it is the chosen one; `filterOf(query)`, the
  chip a `?filter=` names, else `all`. The Needs you count is the
  grid's sessions whose `sessionStatus` key is `needs`; each such
  session is in PR-4's `needsYou()` list, as a question or as the
  record its question mirrors, so the chip and the one number agree on
  every session they both count.
- `pages/yard.vue`: the lists and the chips come from `utils/wall.ts`;
  `running` (:43) and the chip markup (:254-256) go. Each chip is a
  neutral button with the state's `StatusMark` before its word (All has
  none), `data-yard-filter=<key>` and `data-active` as today. The Needs
  you chip's number is in the default ink, its mark amber, and its
  tooltip and `aria-label` say "5 sessions on this grid ask you. Plans,
  tasks and decisions wait in Needs you."; it does not carry the
  attribute PR-5's counts spec reads for the one number. The queue's
  list is the grid's `needs` set. The palette's chip rows follow
  `yardChips` (`page:filter:<key>`). Below `sm` the chips and the
  "polling" badge move to a `UDashboardToolbar` row under the header,
  wrapping, each at least 44 px tall; the attention bar shows from `sm`
  up.
- `components/YardViews.vue` (new): two links, Grid (`/yard`) and
  Roundhouse (`/roundhouse`), each `data-yard-view=<key>` with
  `aria-current` on the one shown; words from `sm` up, icons with
  `aria-label` below it, 44 px targets on touch. It sits in the Yard's
  header in grid mode (not in focus mode) and in the Roundhouse's
  header.
- `pages/roundhouse.vue`: the header's title is "Yard" (:373), the tab's
  "Yard · Roundhouse" (:9), and `YardViews` goes in its trailing slot.
  Its controls, strip, keys, palette group and
  `conductor.carousel.settings` are unchanged.
- `utils/pages.ts` (PR-4): the Roundhouse entry gains `viewOf: 'yard'`.
  The nav, the rail, the phone's More and PR-15's tiles skip an entry
  that is a view; the palette's page rows keep it with G R, its sentence
  "The Yard's second view: one terminal at a time, on rotation."
  `yardRoute(path)` makes the Yard row, its rail icon and the phone's
  Yard tab current on `/yard` and `/roundhouse`. The chords, Alt+R and
  the `/carousel` redirect are unchanged.
- Docs: `docs/features.md` gets an entry; the README's Yard paragraph
  names the new chips (:579), its Roundhouse section opens with "the
  Yard's second view" (:589-591), and the phone's More list loses the
  Roundhouse (:728).

**Tests.**

- `web/app/utils/wall.test.ts` (vitest): `yardFilter` and `yardCounts`
  over a table of sessions, one for each key (needs, working, finished,
  running, and a starting one), an ended session that still carries
  `needs_input`, a host that is away, and a question answered in the
  terminal. It proves that Working holds `working` alone and Finished
  `finished` alone, that Running holds the silent and the starting, that
  the ended and the away session are on no chip, and that each session
  `yardCounts(...).needs` counts is in `needsYou()`'s list for the same
  table, as a question or as the record its question mirrors (a
  mirrored question included). `yardChips` gives the four chips always
  and Running only while it has a session or is chosen; `filterOf` gives
  each key back and `all` for an unknown word or an array. Without the
  change: a filter written as `yard.vue:43` puts the working and the
  finished session in one list, and a chip that keeps `needingInput`
  fails on the first case where its rule and `needsYou()` differ.
- `web/app/utils/pages.test.ts` (vitest, PR-4's file): `yardRoute` is
  true for `/yard`, `/yard?focus=s1`, `/roundhouse` and
  `/roundhouse?x=1`, false for `/yardx`, `/runs/r1` and `/sessions/s1`;
  the nav's and the phone's More lists hold no Roundhouse, and the
  palette's page rows hold it with G R. Without the change: the
  Roundhouse is a nav entry, and the Yard row is not current on
  `/roundhouse`.
- `web/e2e/yard-tiles.spec.ts` (Playwright, new), on the seeded day at
  1440 x 900 (`dayPage`): every tile's `[data-tile-name]` is `uncut`;
  `[data-tile-header]` holds no `[data-status-mark]`; each of the day's
  `states` sessions, found by its id, has its key and word in
  `[data-tile-foot] [data-status-mark]`; the foot's parts do not overlap
  (`noOverlap`) and lie `inside` the tile. On a run of the day's `runs`
  part, a member's tile has its mark before `[data-branch]`, both inside
  the foot. The same at 390 x 844 with touch. Without the change: the
  header holds the mark, the foot none, and the day's long names are cut
  as the review found them ("mac…").
- `web/e2e/yard-tiles.spec.ts`, a change the test causes, on the shared
  server: two `claude` stubs the test makes, one set `working` and one
  `done` through `POST /api/sessions/{id}/attention` and waited for on
  the API, each tile found by its id (count only what the test made). A
  click on Working shows the working tile and hides the finished one;
  Finished the reverse. With Working chosen, the working session is set
  `done`: its tile leaves the grid, and under Finished its foot says
  "finished" (checked again after the redraw the test caused). Without
  the change: there is no `[data-yard-filter="working"]`.
- `web/e2e/yard-tiles.spec.ts`, both themes (`dayPage` with `theme`, the
  app's own `conductor-color-mode` key set before load): on the day the
  Needs you chip reads "Needs you" and the number of the day's asking
  sessions, and its `aria-label` names "sessions on this grid"; its
  number is not drawn in the theme's `--ui-warning` (read off a probe
  element), and its mark is. Without the change: the chip is a `warning`
  button in both themes and says nothing of what it counts.
- `web/e2e/yard-tiles.spec.ts` at 390 x 844 with touch: the chips sit in
  their row under the header, each `uncut`, `inside` the window,
  `tapTarget` 44, clear of the header's parts (`noOverlap`); a tap on
  Finished makes it `data-active` and shows the day's finished tile; a
  tap on the bottom bar's Yard tab redraws the page and the check runs
  again. Without the change: there is no Finished chip, and the three
  chips and the bar do not fit the header at 390.
- `web/e2e/yard-views.spec.ts` (Playwright, new) at 1440 x 900: on
  `/yard`, once the layout's sidebar root is drawn,
  `[data-yard-view="grid"]` is current; a click on
  `[data-yard-view="roundhouse"]` lands on `/roundhouse` with the
  header's title "Yard", the Roundhouse view current, the nav's Yard
  link `aria-current="page"` and no Roundhouse link in the nav; a click
  on Grid and the same checks again. G R pressed once from `/agents`
  (after the sidebar root is drawn: a key the page does not yet listen
  for is lost) lands on `/roundhouse` with the Yard row current, and
  `/carousel` lands there too. At 390 x 844 with touch the bottom bar's
  Yard tab is current on `/roundhouse`, More holds no Roundhouse, and
  both headers' parts are `inside` the window and `noOverlap`. Without
  the change: there is no view switch, and the nav and More list the
  Roundhouse.
- Existing specs that change: `palette-reach.spec.ts:107-109` opens
  `/yard?filter=finished` (Finished active, All not), then
  `/yard?filter=running` (Running shown and active though it may be
  empty) and `/yard?filter=x` (All); `palette-rows.spec.ts:198-204` runs
  `page:filter:working` in place of `page:filter:running`, whose row now
  shows only while Running has a session; `sidebar.spec.ts:435` and
  `phone.spec.ts:154` drop `/Roundhouse/` from their lists;
  `chords.spec.ts:28-32` reaches the Roundhouse by
  `[data-yard-view="roundhouse"]`, still a link and not a load, which is
  what the step proves.
- Existing specs that stay and keep proving: `shortcuts.spec.ts:32-38`
  (G R to `/roundhouse`); `useShortcuts.test.ts:35` (the G R row, its
  label unchanged); `tasks.spec.ts:132` (a run tile's `[data-branch]`,
  now after the state cell); `tiles.spec.ts:82-104` and
  `examples.spec.ts:139` (a tile found by its name, still in the
  header); PR-3's status walk, whose Yard tile locator is scoped to the
  tile and not to its header. No proof is lost.
- Each new or changed spec runs with `--repeat-each 10`, and once more
  beside a Go race package as load.

**By hand.**

- A run page at 1440 with four members: the state cell, the branch and
  the diff in one foot, and which gives way first.
- The day's 18 tiles at 1x in both themes: the working ring and the
  finished ring told apart at a glance, and the event badge's icon read
  without its word.
- The Roundhouse's header at 1024 and at 1440, where its interval and
  switches show beside the view switch.

**Size.** About 230 lines of code and 450 of tests.

**Defaults.**

- **The old link `?filter=running`.** Default: it opens the new Running
  chip, not All. Why: a link opens the chip it names, and no screen
  writes `?filter=`. Alternative: All.
- **The Needs you chip.** Owner decision 5 for this row: its number in
  the default ink, its mark amber, its tooltip and `aria-label` "N
  sessions on this grid ask you. Plans, tasks and decisions wait in
  Needs you."
- **A run's tiles.** Default: the state cell sits outside the foot's
  slot, so a run page's tiles gain the state before their branch and
  diff. Why: one tile everywhere.
- **The event badge on a tile.** Default: its icon alone, the word in
  its title and `aria-label`; the sidebar row keeps the word. Why: the
  badge would cut the name on 210 px tiles.
- **The host on a tile.** Default: "server" or the host's name, with
  "hosted · <host>" in its title. Why: the host is the part that gives
  way.
- **The chips on a phone.** Default: below `sm` they move to a toolbar
  row under the header with 44 px targets. Why: the chips, the bar and
  the view switch do not fit a 390 px header.
- **Chips on the Roundhouse.** Default: not carried there. Why: the move
  is about where the page lives, not what it shows.

**Depends on.** PR-3 (`sessionStatus`, `StatusMark` and `useNow`: the
foot and the chips draw them). PR-4 (`utils/pages.ts` holds the nav rows
the Roundhouse leaves, and `needsYou()` gives the chip its sessions).
PR-1 (the day, `dayPage`, `web/e2e/layout.ts`). It builds on #908
(merged: the sidebar's width, at which the names must fit, and
`WallQueue.vue`). Open #907 merged, for `pages/yard.vue`, which it
holds. It shares
`utils/pages.ts`, `sidebar.spec.ts:435` and `phone.spec.ts:154` with
PR-5, PR-13, PR-15 and PR-29: whichever lands later edits the table, not
an array.

**Left out.** The queue, the tile's question and the Yard's keys are
PR-18's. The attention bar's colours are PR-3's. The Yard's nav badge
and the palette's "Yard · N need you" lose their number in PR-5. The
Yard's and the Roundhouse's empty pages are PR-22's. A run link's member
tiles (`JoinCrewGrid.vue:55-58`) draw a header of their own and keep
their state there, with PR-3's mark. The Yard's chips are not carried to
the Roundhouse, and `/yard?view=roundhouse` is not made: the route
stays, so every old link, chord and spec still lands.

### PR-18: web: the Yard is its grid (P22)

The Yard loses its queue and the grid takes the room at every width. A
tile that asks says the start of its question at its foot, in place of
the host, with the whole question in its tooltip; a question is answered
from Needs you in the sidebar, from the pill that ] brings up, or in the
tile's own reply bar once the tile is expanded. On the Yard the pill's
Enter expands the tile in place rather than leaving for the session
page. J, K and Enter leave the Yard's keys: on the Yard they no longer
step through questions, and ] and [ (the pill) do it on every page.
Alt+J and Alt+K reach the terminal again on every page. The "Answered"
list goes; a session's Activity and its record in History keep who
answered. The queue does not come back when the sidebar is folded. The
chips, the tile's state and the Roundhouse view stay as PR-17 left them.

**Today.** `WallQueue.vue` is an `aside` 340 px wide with no breakpoint
(:57), drawn beside the grid by `pages/yard.vue:322` while any session
on it asks (`showQueue`, :52), so every tile shrinks while something
waits and a 390 px phone keeps a grid 26 px wide. Each card holds the
name (a click expands the tile), the question whole (:77), a decision's
"answered in Conductor" words (:79), and either the choices (:80-94, cut
at the third at `9fb98b06`, wrapped by #908) or a reply field
(:95-109); its foot lists the eight newest answers under "Answered"
(:112-120, from `yard.vue:45-50`), which no other screen shows. The
page's J, K and Enter step through the cards and open one's field, and
Alt+J and Alt+K do the same from a terminal (`yard.vue:100-112`); for
those two chords `KeyJ` and `KeyK` sit in `ALT_PASSTHROUGH_CODES`
(`composables/useShortcuts.ts:23`), which `TerminalView.vue:773` hands
to the page, so neither ever reaches a terminal on any page.
`WALL_SHORTCUTS` (`useShortcuts.ts:143-151`) lists the three keys, and
the palette offers "Expand <the queue's selected session>"
(`yard.vue:218`, :226). The same questions are answered elsewhere
already: the sidebar's Needs you row, whole with numbered choices and a
field; the pill (`NeedsYouPill.vue`), which takes 1 to 9 and no typed
reply and whose Enter opens `/sessions/<id>` (:57-67); and the focused
tile's `QuickReplyBar` (`yard.vue:299-301`). A tile that is not expanded
says nothing of its question beyond its amber ring.

**The change.**

- `components/WallQueue.vue` is deleted. `pages/yard.vue` deletes its
  import and use (:322), `answered` (:45-50), `showQueue` (:51-52), the
  queue's selection and its watch (:54-59), `queueNext` and `queuePrev`
  (:93-98), the keys `j`, `k`, `enter`, `alt_j` and `alt_k` (:102-106,
  :110-111) and the palette's `page:expand` row (:218, :226). The grid's
  box is the body's only child.
- `composables/useShortcuts.ts`: `WALL_SHORTCUTS` keeps its Escape row
  alone, and `KeyJ` and `KeyK` leave `ALT_PASSTHROUGH_CODES`. The
  shortcuts dialog follows by itself.
- `components/SessionTile.vue`: in the default foot, a tile that asks
  shows `[data-tile-ask]`, one line cut with "…", the start of its
  question in place of the host; the whole question is its `title` and
  joins the tile's `aria-label` ("proc-race-tests asks: Run the tests
  again? Enter opens it in full."). A question that mirrors a decision
  reads "dr-7 · <question>". The viewer count keeps the right; a run
  page's tiles, which fill the slot, are unchanged. The words come from
  `tileAsk(session)` in `utils/wall.ts`, built on
  `rowPrompt(sessionRow(s))` (`utils/sidebarActions.ts:46`), the
  function the sidebar row and the pill read, so the three say the same.
- `utils/needsCursor.ts`: `pillTarget(path, id)` gives
  `/yard?focus=<id>` on the Yard and `/sessions/<id>` everywhere else,
  the Roundhouse included; `NeedsYouPill.vue`'s `open()` (:57-67) goes
  there. A free-text question is then answered in the expanded tile's
  bar without leaving the Yard.
- `utils/attention.ts`: `choiceTitle(option)`, the title #908 gave
  the queue's choices (the whole label, and for a choice that changes
  the permission mode, what it does); `QuickReplyBar.vue` gives it to
  its choices (:108), so the queue's proof moves to the bar whole. The
  comment at :48 no longer names the queue.
- Docs: `docs/features.md`'s Wall entry (:50-52), its "wall's Answered
  list" (:77) and the round-1 line that routes prompts "into the wall
  queue" (:228) are rewritten, with an entry for the change; the
  README's Yard paragraph (:578-582), the palette's sentence (:641),
  "appear in the Yard's queue" (:775) and the choices' sentence (:875)
  follow.

A tile's size with the queue and without it, from the app's own
`bestGrid` (`utils/wall.ts`) with the page's measures: a 56 px header,
12 px around the grid and between tiles, the queue 340 px, the tile as
wide and tall as its cell. PR-7 returns the sidebar to 200 px; until it
lands the sidebar opens at #908's 268 px.

| Window, sessions | Sidebar | With the queue | Without it |
|---|---|---|---|
| 1440 x 900, 18 | 200 px | 4 across, 210 x 154 | 4 across, 295 x 154 |
| 1280 x 720, 18 | 200 px | 4 across, 170 x 118 | 5 across, 201 x 151 |
| 1440 x 900, 8 | 200 px | 2 across, 432 x 196 | 3 across, 397 x 265 |
| 1024 x 768, 8 | 200 px | 2 across, 224 x 163 | 2 across, 394 x 163 |
| 1440 x 900, 18 | 268 px | 3 across, 261 x 126 | 4 across, 278 x 154 |
| 1280 x 720, 18 | 268 px | 3 across, 208 x 96 | 4 across, 238 x 118 |
| 1440 x 900, 18 | the rail, 64 px | 4 across, 244 x 154 | 5 across, 260 x 196 |
| 390 x 844, 5 | none (the bottom bar) | 1 across, 26 x 132 | 1 across, 366 x 132 |
| 390 x 844, 18 | none (the bottom bar) | 1 across, 26 x 28 | 3 across, 114 x 108 |

What a person reads of a question after this row, by where the sidebar
is:

| Where | On the Yard's grid | Read whole in | Answered in |
|---|---|---|---|
| Sidebar open (1024 px and wider) | the tile's mark, "needs you" and the question's start | the sidebar's Needs you row (two lines and a panel beside it below 280 px, from PR-8); the pill; the expanded tile's bar | the row's choices or field; 1 to 9 in the pill; the expanded tile's bar |
| Sidebar folded to the rail | the same | the pill (] and [); the tile's tooltip; the expanded tile's bar | 1 to 9 in the pill; Enter in the pill expands the tile, whose bar takes a typed reply |
| Under 1024 px (the sidebar is a drawer, the bottom bar shows) | the same | the bottom bar's Needs you tab (Decide until PR-5); the pill with a keyboard; the expanded tile's bar | the same; the Yard tab keeps its count |
| A phone, 390 x 844 | the same, on a grid 366 px wide in place of 26 | the Needs you page; the expanded tile (its Open button) or the session page | the expanded tile's bar or the session page |

The Yard's own chip still counts the sessions on its grid that ask, as
PR-17 made it, and says so.

**Tests.**

Seven specs answer through the queue today, as the review counted (a
search of `web/e2e` for `data-wall-queue` and `data-queue-` finds
exactly these), and #908 added an eighth test. Each proof moves:

| Spec | What it proves through the queue today | Where the same proof goes |
|---|---|---|
| `yard-queue.spec.ts:13-30` (deleted) | the queue shows a question; a click on its first choice answers it; the queue then goes and the tile stays (#360: the grid is not shrunk once nothing waits) | `yard-grid.spec.ts` proves the grid is never shrunk, while a question waits and after it; the answer by a click is the moved `choices.spec.ts` test below |
| `choices.spec.ts:50-59` | the card offers the asker's three buttons; a click on SQLite types "SQLite" | `/yard?focus=<id>`: `[data-yard-focus] [data-quick-reply]` has the three buttons, and a click on SQLite types "SQLite" (the stub's transcript) |
| `choices.spec.ts`, #908's wrap test | the card's choices wrap and none runs off it; a cut one has its words in its `title`; the same after the question is asked again with a 58-character mode choice | the same steps on the expanded tile's bar at 1440 x 900: every choice `inside` the bar, each title from `choiceTitle`, checked again after the question is asked again; `phone.spec.ts:188-189` keeps the phone's case |
| `question.spec.ts:69-77` | the 200-character question is whole on the card (`expectWhole`), and the long choice shows | `/yard?focus=<id>` at 1440: `[data-quick-reply-message]` whole and the long choice visible; on the grid the tile's `[data-tile-ask]` holds the question's start and its whole text in `title` |
| `approval.spec.ts:70-84` | a second tab on the Yard holds the card; a key typed in the member's terminal in the first tab makes the card go at once (#258) | the second tab's tile shows `[data-tile-ask]`, then has none after the key, and that tab's sidebar row leaves `data-row-state="needs"`: two surfaces of the other tab in place of one |
| `approval.spec.ts:116-127` | asked again on the Yard: the card has the words and three choices; a click on "No, tell Codex what to do differently" types Escape | `/yard?focus=<id>`'s bar has the words and the three buttons, and the same click types Escape |
| `askwait.spec.ts:166-182` | a question that outlasts its turn shows on the card with its words; after the row's reply the card goes | the second tab's tile: `[data-tile-ask]` has the question in its `title` and its start in its text, then is gone after the reply |
| `codexworking.spec.ts:203-213` | while Codex works the card is absent; on `main` this passes whether or not the queue exists, since the card is never seen first and the queue is not drawn when nothing waits | the tile's `[data-tile-foot] [data-status-mark]` is `working` and it has no `[data-tile-ask]`, read after the API shows `{state: 'working', source: 'status'}` (wait for a state that holds): a real proof in place of an empty one |
| `decision-mirror.spec.ts:93-99` | the card says "The operator answers dr-N in Conductor" and has no input | the tile's `[data-tile-ask]` reads "dr-N · " and the question's start, and the grid holds no input; `/yard?focus=<id>` shows `[data-quick-reply-answer]` "Answer dr-N" and no field; the step's `budget(3 * STEP_MS)` becomes `budget(5 * STEP_MS)`, one cap for each wait it takes |

The proofs that are lost, each by design:

- The long question read whole on the Yard's grid without expanding a
  tile. The sidebar's row, the pill and the expanded tile's bar read it
  whole, and `question.spec.ts` keeps all three.
- The words "The operator answers dr-N in Conductor" on the Yard. The
  same test keeps them in the sidebar's row (:86-91) and the palette
  (:101-107).
- The wrap of three choices inside a 340 px card. The card is gone; the
  bar's wrap is proved at 1440 and at 390.
- No spec ever pressed J, K or Enter on the Yard, ran the palette's
  `page:expand` row or read "Answered", so those behaviours go with no
  test.

The new and changed tests:

- `web/e2e/yard-grid.spec.ts` (Playwright, new), on the seeded day at
  1440 x 900 with its five asking sessions: `[data-wall-queue]` never
  exists; the grid's box starts 12 px inside the panel's left edge and
  is the panel's width less 24 px; every tile is at least the size
  `bestGrid` gives for the measured box and the day's 18 sessions,
  within a pixel; each asking tile's `[data-tile-ask]` holds the start
  of its question and its `title` the whole of it. Then the sidebar is
  folded to the rail (Control+B, once the sidebar root is drawn) and the
  same checks run on the wider box, and again once it is opened (a
  redraw the test causes). Without the change: the grid starts 352 px
  in, and no tile has `[data-tile-ask]`.
- `web/e2e/yard-grid.spec.ts` at 390 x 844 with touch, on the day: the
  grid is wider than 300 px, each asking tile's `[data-tile-ask]` lies
  `inside` its tile, and the page has no sideways scroll. Without the
  change: the grid is 26 px wide.
- `web/e2e/yard-grid.spec.ts`, a question the test asks, on the shared
  server: an `asker` stub the test makes, found by its id; the question
  is answered through the API and a new one asked (`POST
  /api/sessions/{id}/attention`), and `[data-tile-ask]` shows the new
  start while the grid's box keeps its width (checked again after the
  redraw). Without the change: the queue opens and closes and the grid's
  width moves by 340 px.
- `web/e2e/yard-grid.spec.ts`, the pill's Enter: `conductorPillMs` set
  through an init script to the test's timeout (the product's 4 s is not
  the test's bound), strays ended first (`stopStrays`) so that ] shows
  the test's own session; on `/yard`, once the sidebar root is drawn, ]
  once, then Enter: the URL is `/yard?focus=<id>`, and a line typed into
  `[data-yard-focus] [data-quick-reply]`'s field reaches the stub's
  transcript. Without the change: Enter lands on `/sessions/<id>`.
- `web/e2e/chords.spec.ts` (Playwright, extended): in the session page's
  step that already types "a", Alt+Q and "b" (:86-91), Alt+J and Alt+K
  are pressed too, and the transcript holds "a", ESC j, ESC k, ESC q,
  "b"; the same is typed into the Yard's expanded tile. Without the
  change: the page takes both chords and the line is "a", ESC q, "b".
- `web/app/composables/useShortcuts.test.ts` (vitest): the assertion at
  :44 that `WALL_SHORTCUTS`' J row has Alt+J becomes "the Yard's rows
  are Escape alone", and `ALT_PASSTHROUGH_CODES` holds neither `KeyJ`
  nor `KeyK`. It fails if either comes back.
- `web/app/utils/wall.test.ts` (vitest): `tileAsk` gives the question's
  words for an asking session, "dr-7 · " and the words for one that
  mirrors a decision, and nothing for a working session or an ended one
  that still carries `needs_input`. It fails if the tile builds its
  words apart from `rowPrompt`.
- `web/app/utils/needsCursor.test.ts` (vitest): `pillTarget('/yard',
  's1')` is `/yard?focus=s1`, on `/roundhouse` and `/agents` it is
  `/sessions/s1`, and an id with a slash is encoded. It fails while
  Enter always opens the session page.
- `web/app/utils/attention.test.ts` (vitest): `choiceTitle` gives a
  plain choice its label and a mode choice its label with "(changes the
  session's permission mode)". It fails if the bar keeps `MODE_CHOICE`
  alone.
- The moved steps keep each serial spec's order and `budget` steps
  (approval, askwait, decision-mirror, codexworking); a step on a second
  tab reads the tile again where the answer is judged, never a variable
  a wait left behind.
- Each new or changed spec runs with `--repeat-each 10`, and once more
  beside a Go race package as load.

**By hand.**

- The seeded day on the rail at 1440: whether a question's start is
  enough to pick the tile to open.
- Alt+J and Alt+K inside Neovim in an expanded tile, where people bind
  them.
- The pill's Enter on the Yard: whether the expanded tile's field should
  take the focus at once.

**Size.** About 75 lines of code added and 190 deleted, and about 400
lines of tests added and 170 deleted.

**Defaults.**

- **Run tiles.** Default: the question's start replaces the host in the
  default foot only, so a run page's tiles keep their branch and diff.
  Why: the run page has its own card for what asks.
- **The pill's Enter elsewhere.** Default: on `/roundhouse` and every
  page but the Yard it opens the session page as today. Why: the
  Roundhouse has no focus mode.
- **The reply bar's choices.** Default: they take the title rule #908
  gave the queue (`choiceTitle`), so #908's wrap proof moves whole to
  the surface that stays.

**Depends on.** PR-17 (the foot's state cell, after which the question's
start sits, and the grid's `needs` set). PR-1 (the day, `dayPage`,
`web/e2e/layout.ts`). It builds on #908 (merged: it edited
`WallQueue.vue` and added the wrap test that this row deletes again and
moves to the bar). Open #907 merged, for the app's files it holds that
this row edits (`pages/yard.vue`, `QuickReplyBar.vue`,
`utils/attention.ts`). PR-8 is not needed first: on `main` the sidebar's
row shows the question whole, and PR-8 ships its two-line clamp with its
panel. PR-8 moves the row answers of `choices.spec.ts`,
`question.spec.ts`, `approval.spec.ts` and `askwait.spec.ts` onto one
helper; whichever of the two lands second rebases its steps on the
other's. `QuickReplyBar.vue` is shared with PR-19 and PR-26, one line
here.

**Left out.** "Answered" is not carried to the feed: that would be a new
activity event, a protocol change in three places; `lastAnswer` stays on
the server and in the listing, which `answered.spec.ts` reads. The queue
does not return with the sidebar folded. The pill's Enter on the
Roundhouse opens the session page as today. A guest's view of a
Yard-sized session is on `main` (#908, D10). The sidebar's panel for a
cut question is PR-8's.

### PR-19: web: a session page where the terminal has the room (P11, D7, D9)

A session's page opens with the inspector closed: the terminal takes
the panel's 332 px, and the rail with its counts stays on the edge. The
line under the name reads agent, folder and branch, with the whole path
as its tooltip; the ids are in the More menu. Share is an outline
button like its neighbours. The connection is an icon while it is
healthy and words when it is not. The reply bar draws every choice
alike, and fills a choice only when the agent itself marks it
"(Recommended)". A link to a session the server no longer holds opens
on that session's name and a way to its record in History, with no
controls that cannot work. On a phone the name is whole, because
Files, Share and Fullscreen move into More below `sm`. The rail, its
views, `?tab=`, the palette's Open rows, the chords and the reply bar's
keys stay as they are.

**Today.** `pages/sessions/[id].vue:130-143` reads one key,
`conductor.inspector`, and opens the panel unless it holds `0`; the
view starts on People (`:46`, and `SessionInspector.vue:41-42`), a
panel of `w-[332px]` (`SessionInspector.vue:184`) beside the 44 px rail
(`InspectorRail.vue:28`). The meta line (`[id].vue:179-187`) joins the
agent, the word `server` or "hosted by U on H", `shortCwd(cwd)` and the
branch; `shortCwd` (`utils/sessions.ts:103-105`) shortens only a path
under a Linux or a macOS home folder, and the agent's session id follows
as a copy button at `md` and up (`:562-571`). Share (`:582`) has no
colour or variant, so Nuxt UI draws it solid primary; Files, Chat and
More are neutral outline. `TransportBadge.vue:19-31` always draws
words ("WebSocket · 12 ms"). `QuickReplyBar.vue:30` and `:106-107` fill
`firstChoice(options)` (`utils/attention.ts:80-83`), which is only the
first choice that changes no mode; `WallQueue.vue:86-87` does the same.
For D7, `load()` (`:240-255`) puts the 404's "no such session" into an
alert (`:597`) while the header still draws "Session", the badge's
starting `idle` (`:38`), Files, Share and More. For D9, at 390 px the
title (`:555`, `truncate` in a `min-w-0` row) shares the bar with Back,
the state badge, Files, Share, Chat, More and Fullscreen (`:550-591`),
and keeps one letter.

**The change.**

- `pages/sessions/[id].vue`: the panel's choice is kept under a new
  key, `conductor.inspectorOpen`, read in `try` and closed when absent
  or unreadable, so the old key's stored `1` does not keep anyone's
  panel open. The toggle with no view chosen opens on Explorer, not
  People; a rail item, `?tab=`, the header's Files and Chat and the
  palette's Open rows open it as today (`pickItem`, `showView`).
- `utils/sessions.ts`: `sessionHeadline(s)` returns `{line, title}`:
  the line is the agent's label, the folder (the last part of `cwd`)
  and the branch, joined by " · "; a hosted session keeps "on <host>"
  after the agent; the title is the whole path (and the host and its
  user for a hosted one). The page's span gets `data-session-meta` and
  the title as its tooltip. The agent-session button leaves the line;
  the More menu gains "Copy session id" and, when the session has one,
  "Copy the agent's session id".
- Share: `color="neutral" variant="outline"`, as Files and More.
- `utils/transport/look.ts` (new): `transportLook(kind, state, rtt)`
  returns the icon, the words and whether the words show. While the
  state is `open` the badge draws the icon alone, with the words
  ("WebSocket · 12 ms", "WebRTC direct") as its `aria-label` and
  tooltip; every other state draws the words as today.
  `TransportBadge.vue` takes a `compact` prop and keeps
  `data-transport-kind` and `data-transport-state` on its root (18
  specs wait on them). Only the session page passes `compact`.
- `utils/attention.ts`: `recommendedChoice(options)` replaces
  `firstChoice`: the index of the one choice whose label, trimmed, ends
  "(Recommended)" (any case), never a mode choice; -1 when none or more
  than one say it. `QuickReplyBar.vue` fills only that choice, marks it
  `data-recommended`, and draws every other choice outline (mode
  choices keep the warning colour). `WallQueue.vue` takes the same two
  lines if PR-18 has not removed it. `firstChoice` is deleted.
- D7: `utils/sessions.ts` gains `gonePage(err, record)`: `record` for
  an `ApiError` 404 `not_found` whose record answers, `forgotten` for a
  404 whose record answers 404, `unread` for a 404 whose record read
  failed otherwise, and `null` for any other error (today's alert
  stays: a 401 or a token prompt is never "gone"). On a 404 the page
  reads `useHistory().record(id)` once and draws `data-session-gone`:
  the record's name as the title and the tab's title, the exited mark
  of PR-3, no connection badge, viewers, Files, Share, Chat or More,
  and a body over Nuxt UI's `UEmpty`: "This session ended, and this
  server no longer holds it." with "Open its record" to
  `/history/sessions/{id}`; for `forgotten` "Nothing of this session
  is kept." with "Open History"; for `unread` the first sentence, the
  read's own words and "Open History".
- D9: below `sm` the header's Files, Share and Fullscreen are items of
  the More menu, first in it, with the same handlers (`showFiles`,
  `share = true`, the fullscreen toggle); the bar holds Back, the name
  with its mark and word, Chat with its unread count, and More. At `sm`
  and up nothing moves.
- `README.md` (the session page paragraph: the panel is closed until a
  view is opened; a choice is filled only when the agent recommends
  it) and `docs/features.md`.

**Tests.**

- `web/app/utils/attention.test.ts` (vitest): `recommendedChoice` is
  -1 for today's bash and plan lists, 1 for "Merge commit", "Squash
  (Recommended)", "Rebase", -1 when two labels say it, -1 when only a
  mode choice says it, and finds "squash (recommended) " trimmed. The
  `firstChoice` cases (`:119-122`) go. Without the change: the bash
  list gives 0.
- `web/app/utils/sessions.test.ts` (vitest): `sessionHeadline` gives
  "Claude Code · api · main" with the title `/srv/work/api` for a
  server session, keeps "on build-box" for a hosted one, and holds no
  id; `gonePage` gives the four answers above for a 404 with a record,
  a 404 with a 404 record, a 404 with a 500 record, and a 500. Without
  the change: the line holds the whole path and "server"; a 404 gives
  today's alert.
- `web/app/utils/transport/look.test.ts` (vitest, new): `open` shows no
  words and names the transport in its label; `connecting`, `closed`
  and `idle` show their words. Without the change: `open` shows
  "WebSocket · 12 ms".
- `web/e2e/session-page.spec.ts` (Playwright, new; sessions it makes by
  id on the shared server, ended in `afterEach`): at 1440 x 900, a
  fresh context opens a session with `data-inspector-open="false"`, the
  rail visible and no rail item pressed, and the terminal's column
  (`[data-editor-column]`) as wide as the panel's body less the rail,
  within 2 px; People opened, a reload keeps it open. The meta span's
  text is "Claude Code · session-page" and its `title` the whole cwd;
  More holds "Copy session id". Without the change: the panel is open,
  the column is 332 px narrower, the text holds the path.
- Same spec, in both themes (set through `conductor-color-mode` before
  load, waiting for the class on `html`): Share's computed background
  equals More's, and once `[data-transport-state="open"]` the badge has
  no visible text and an `aria-label` naming WebSocket. Without the
  change: Share's background is the primary colour; the badge shows
  "WebSocket · n ms".
- Same spec, the reply bar: a needs_input with three choices, none
  recommended, posted through `POST /api/sessions/{id}/attention` with
  the workbench token, shows three choices and none with
  `data-recommended`, all with one variant; then a question whose
  second label ends "(Recommended)": only that one is filled and
  primary. The theme is switched and the page drawn again, and the
  check is made again (check again after a redraw the test causes).
  Without the change: the first choice is filled each time.
- Same spec, D7: a session stopped, polled until ended, removed with a
  second `DELETE` and polled until `GET /api/sessions/{id}` is 404 (the
  recipe of `rename.spec.ts:166-169`), its record polled until
  `endedAt` is set (`history-record.spec.ts:69`): its page shows
  `[data-session-gone]`, the session's name as the title, no button
  named Share or More, `[data-transport-state]` count 0, and "Open its
  record" leads to `/history/sessions/{id}`. After the record is
  forgotten (`DELETE /api/history/sessions/{id}`): "Nothing of this
  session is kept." and "Open History". Without the change: Share and
  More are drawn and the title is "Session".
- Same spec, D9, at 390 x 844 with touch (PR-1's `dayPage` options): a
  session named "e2e phone title" made to need input; `uncut` holds
  for `[data-session-title]` and its box is at least 100 px wide; the
  bar has no button named Share; More holds Share and Files, and Share
  from More opens the Share dialog once the menu is gone
  (`toHaveCount(0)`: a menu that has closed is not gone); the document
  never scrolls sideways. At 1440 Share and Files are in the bar.
  Without the change: the title's `scrollWidth` passes its
  `clientWidth`.
- `web/e2e/inspector-rail.spec.ts` (Playwright, changed): `:24-27`
  asserted People pressed with its head as the page loaded; it now
  asserts the panel closed, the rail and its six items shown, nothing
  pressed, then a click on People opens it pressed with its head. The
  rest stands, the reload check (`:45-51`) under the new key.
- `web/e2e/hscroll.spec.ts` (Playwright, changed): `:36-39` asserted
  "the panel opens with the page"; the first measure is now of the
  closed panel as loaded, then `showView(page, 'people')` opens it for
  the open measure. Each width starts in a fresh context, so the
  second width does not inherit the first one's remembered choice.
- No proof is lost: no spec reads the People panel or the agent-session
  button without opening them first, and `showView` in
  `web/e2e/fixtures.ts` opens from closed.

**By hand.** The page at 1440 x 900 and 1280 x 720 in both themes: the
outline Share beside its neighbours, the icon-only connection and its
tooltip, the terminal's new width. A real Claude Code's question from
its AskUserQuestion: whether a label ends "(Recommended)" and survives
the 60-character cut of a label (`MaxOptionLabel`). The header at 390
px on an iPhone, where no fullscreen button is drawn, and on Android
Chrome.

**Size.** About 220 lines of code and 480 of tests.

**Defaults.**

- **What "(Recommended)" means.** Default: the label, trimmed, ends with
  it in any case; never a mode choice; none when two say it; a label
  cut at 60 characters loses the mark and the bar draws all alike. Why:
  it fails closed.
- **The meta line.** Default: agent · folder · branch, a hosted session
  keeping "on <host>" in the line; the whole path (and host) is the
  tooltip; both ids go into More. Why: a hosted session's machine is
  what tells it apart.
- **The connection badge.** Default: an icon while open, words
  otherwise, on the session page only (the Yard and the join page keep
  words). Why: the change is kept to the page the review measured.
- **A phone's header (D9).** Default: below `sm`, Files, Share and
  Fullscreen move into More, first in it; the state mark keeps its
  word. Why: a word beside every colour is one of the strengths the
  review named.

**Depends on.** PR-3 (the mark in the header and on the gone page, in
`StatusMark.vue`); open #907 (it edits `QuickReplyBar.vue`,
`pages/sessions/[id].vue` and `utils/attention.ts`); PR-1 (`dayPage`,
`uncut`). If PR-18 has not landed, `WallQueue.vue` changes here too and
the two rows share that file.

**Left out.** The per-session memory of the panel (one choice for all
sessions, by owner decision 12). A recommendation sent by the server
(the label is read in the app; no protocol change). Resume on the gone
page (the record's page has it). A session that the server drops while
its page is open (the page shows it ended, as today). The gone page's
body on `PageEmpty`, which PR-22 brings; PR-23 can move it there.
Share's colour among the page's other actions is PR-26's.

### PR-20: web: a run's decision: the question, one set of buttons, the fine print folded (P21)

After this row a run's decision, on Needs you and in its modal, shows
its question and its choices once: the buttons, the recommended one
marked on its button, each choice's tradeoffs in one muted line under
them. What a yes lets happen is one plain line above the buttons. The
id, the type, the plan's origin, the subject's revision and digest and
the steps' receipts fold under Details, which starts open for a plan's
approval and folded for an agent's request. The four steps read "Asked",
"Your answer", "Checked by Yardmaster", "Applied", and none shows a
loader before anyone answers. A decision link's page and a person's
page, which cannot answer, keep the list of choices they have today.
What the buttons send, the confirm step, the comment box and the History
fold are as they were.

**Today.** `components/DecisionCard.vue:50-58` lists each option as a
row, with a "recommended" badge (`:54`) and its tradeoffs (`:56`);
`components/DecisionRespond.vue:117-130` then draws the same options
again as buttons, the recommended one soft and the picked one solid
(`:123-124`), with "Why (optional…)" after them (`:132`). The card's
header (`DecisionCard.vue:33-38`) leads with the mono id, a type badge
and a "plan gate" badge. "A yes authorizes" sits in a small two-column
list (`:65-68`) beside "About", where `subjectWords`
(`utils/decisions.ts:261-262`) gives "plan revision 2676155cda2f04ea,
digest a1c5a96b2db0" in mono at the weight of the question (`:69-75`).
`decisionWords` (`utils/decisions.ts:354-399`) names the steps "Open",
"Response recorded", "Validated by Yardmaster" and "Applied", and marks
"Response recorded" `waiting` while the record is open (`:366`); the
card's `stepIcon` and `stepTone` maps (`DecisionCard.vue:26-27`) draw
every waiting step as `i-lucide-loader-circle` in amber, so an open
record shows a loader on a step nobody has begun. The card is drawn in
four places: `pages/decisions.vue:423` and
`components/DecisionModal.vue:36`, each with `DecisionRespond` under it,
and `components/JoinDecide.vue:125` and `pages/people/[name].vue:205`
with no controls, where the option rows are the only list of choices.
The modal's title is `Decision dr-7` (`DecisionModal.vue:28`).

**The change.**

- `DecisionCard.vue` gains a prop `controls`, false unless given.
  `pages/decisions.vue` and `DecisionModal.vue`, which draw
  `DecisionRespond` under the card, pass it only while the record is
  open (DecisionRespond's buttons are drawn only then,
  `DecisionRespond.vue:40`), and there the option rows are not drawn; a
  record no longer open keeps its option rows. `JoinDecide.vue` and the
  person's page pass nothing and keep them: a card that cannot answer
  never loses its choices.
- `DecisionRespond.vue`'s buttons carry the recommendation. The
  recommended button keeps its soft look and holds a "recommended"
  badge, and `data-decision-recommended` moves onto that button. An
  option's tradeoffs, when it has any, are one muted line each under the
  buttons ("Approve: …").
- What a yes lets happen leaves the two-column list and becomes one line
  under the question and its context, from `authorizesLine(d)` in
  `utils/decisions.ts`. A plan's gate says its own sentence ("Approve
  lets deploy start; Reject cancels it.", #912's words in
  `internal/crew/decisions_wait.go:722`); an agent's request says "A yes
  authorizes: <its words>". The request's words keep their own span with
  the hook `data-decision-authorizes`, so a spec reads them alone.
- Details: a ghost button "Details" (`data-decision-details-toggle`,
  with `aria-expanded`) that folds as `InboxEvidence.vue` folds the
  evidence. It holds the id (`data-decision-card-id`), the type, "From
  the plan" in place of the badge "plan gate", "About" with the subject
  (`data-decision-subject`), "Needs" (the expertise) and each step's
  receipt (`data-step-receipt`). `detailsOpen(d)` in
  `utils/decisions.ts` says how it starts: open for an approval whose
  origin is the plan, folded for every other. The state words
  (`data-decision-state-words`), the uncertainty, the evidence chips,
  the accepted answer and the History fold stay in view where they are.
- The steps. `decisionWords(d, {runHeld, controls})` names them "Asked";
  then "Your answer" on a card with controls and "The answer" on one
  without, since a link's holder and a person's page do not answer it;
  then "Checked by Yardmaster"; then "Applied", or "Not applied:
  <state>" as today. The keys and the `data-step-status` values stay. A
  pure `stepLook(step)` replaces the two maps: done is a green check; a
  step that waits on a person is a still amber `i-lucide-circle-dot`;
  one that waits on Yardmaster is a muted `i-lucide-hourglass`, the mark
  the inbox's own steps use for waiting; pending is a dimmed ring and
  ended a slashed one. No step is drawn with `i-lucide-loader-circle`.
  Each step carries `data-step-icon` with its icon's name.
- `DecisionModal.vue`'s title is "A decision for you"; the id is in
  Details.

**Tests.**

- `web/app/utils/decisions.test.ts` (vitest): for an open, an answered,
  an applied, a cancelled and an applied-then-superseded record,
  `stepLook` gives no step `i-lucide-loader-circle`; the open record's
  `recorded` step is `i-lucide-circle-dot` in the warning tone, and the
  answered record's `validated` step the hourglass. Without the change:
  no function, and the map at `DecisionCard.vue:26` gives the loader to
  the open record's `recorded`.
- `web/app/utils/decisions.test.ts` (vitest): an open record's labels
  are "Asked", "Your answer", "Checked by Yardmaster", "Applied" with
  `controls`, and the second is "The answer" without. The statuses
  asserted at `:104` and the keys stand; the label asserted at `:115`
  moves from "Validated by Yardmaster" to "Checked by Yardmaster".
  Without the change: the old labels.
- `web/app/utils/decisions.test.ts` (vitest): `detailsOpen` is true for
  a plan's approval and false for a plan's clarification, an agent's
  approval and an assignment; `authorizesLine` gives a gate's sentence
  as it is, "A yes authorizes: …" for an agent's request, and nothing
  without `authorizes`. Without the change: no functions.
- `web/e2e/decisions.spec.ts` (Playwright, its own server, 1440 x 900):
  in the agent's approval's modal, `[data-decision-option]` has count 0,
  `[data-decision-choice]` count 2 and
  `[data-decision-choice][data-decision-recommended]` count 1;
  `[data-decision-authorizes]` still reads "build commits
  stub-builder.txt and submits it" (`:131`); `[data-decision-card-id]`
  has count 0 until Details is clicked, then reads the id; the open
  record's `recorded` step has `data-step-icon="i-lucide-circle-dot"`
  and the label "Your answer". `:130` (`[data-decision-recommended]`
  count 1) stands, now on the button. Once the record is applied, the
  card still shows its `[data-decision-option]` rows. Without the
  change: two option rows beside two buttons, the id in view, and the
  loader; with `controls` passed whatever the state, an applied record
  shows no choices at all.
- `web/e2e/decision-page-durable.spec.ts` (Playwright, its own server,
  390 x 844 with touch): the first gate's item, a plan's approval, draws
  its choices once with Details open, `[data-decision-subject]` in view
  with "plan revision" at once; the agent's clarification (`asked`)
  starts folded, and `[data-decision-card-id]` shows once the toggle is
  tapped. After the gate is approved and applied (the poll at `:143`),
  Details is checked again and is still open: the record's new rev
  redraws the card (check again after a redraw the test causes). Without
  the change: rows and buttons both, and no toggle to tap.
- `web/e2e/decisions-gate.spec.ts` (Playwright, 1440 x 900): the gate's
  modal on the ticket page draws one set of buttons, and its line reads
  "Approve lets deploy start; Reject cancels it.". Without the change:
  the option rows are there too.
- `web/e2e/switchyard.spec.ts:696` (Playwright) stands as it is: a
  decision link's card still draws its two option rows. It is the test
  that fails if the rows are dropped everywhere instead of behind
  `controls`.
- No existing proof is lost: the specs that read
  `data-decision-question`, `data-decision-choice`,
  `data-decision-state-words`, `data-decision-step` and
  `data-decision-history-toggle` (`decisions-gate.spec.ts`,
  `decisions-restart.spec.ts`, `decisions-people.spec.ts`,
  `brakeman-act.spec.ts:477`, `decision-page-durable.spec.ts:168`) read
  hooks that stay in view where they were.

**By hand.** A person's page with two decisions, and a decision link on
a phone: their option rows read as before. The weight of the Details
button beside the evidence chips, in both themes.

**Size.** About 170 lines of code and 220 of tests.

**Defaults.**

- **Where the option rows go.** Default: dropped only where the buttons
  are drawn (the `controls` prop, passed while the record is open); a
  decision link, a person's page and a record no longer open keep them.
  Why: there they are the only list of choices.
- **The step names.** Default: "Asked", then "Your answer" ("The
  answer" on a card with no controls), then "Checked by Yardmaster",
  then "Applied". A step that waits on a person gets a still amber dot,
  one that waits on Yardmaster an hourglass, none a loader. Why: a
  link's holder cannot answer, so "Your" would be untrue there.
- **The decision modal's title.** Default: "A decision for you"; the id
  moves into Details.

**Depends on.** Open pull request #912 merged: its words for a gate
("Approve lets deploy start; Reject cancels it.") are the plain line
this row shows, and it edits `pages/decisions.vue`, where this row adds
one attribute. Nothing else of the stream: PR-20 can go beside PR-6,
with which it shares that one line.

**Left out.** The inbox's own kinds (a question, a plan, a task): their
step names, their scope lines and the colour of Approve and Retry
(PR-21). The list's kind marks and the first item opening by itself
(PR-21). The confirm step's words ("Answer dr-7 Approve?") and the
comment box stay as they are. The rules for button colours across the
app (PR-26).

### PR-21: web, api: the list and the item in a person's words (P21, D12)

After this row each row of the list says what kind it is, and on a wide
window the oldest item opens by itself. An item is headed in a person's
words: "A question for you", "A plan to approve", "A task out of
attempts", "A decision for you". A run's decision is titled by its
question and briefed "A decision for you, from the plan of DEC-1", with
no "durable decision", no "plan gate" and no id in its words. No item
prints its title twice. A question's fine print ("Types 2 into the
terminal of api-fix…") folds under Details; a plan's and a task's stay
in view. Approve and Retry are forest, as Launch and Send are. A
decision link's page says the same words.
What each action sends and what it is checked against are unchanged.

**Today.** The rows (`pages/decisions.vue:314-357`, with #912's
`INBOX_ROW_CLASS`, and the same markup at
`components/JoinDecide.vue:58-74`) carry badges (`:328-333`) and no mark
of their kind; a decision link's row of a run's decision has the badge
"A durable decision" (`JoinDecide.vue:68`). Nothing opens an item by
itself, and after PR-6 the pane says "Pick an item" while one waits. The
item's kind line is `inboxKindWords` (`utils/inbox.ts:422-434`: "A
question", "A failed task", "A plan revision", "A durable decision").
The server titles a run's decision "Approval dr-7: <question>" and
briefs it "<Where> asks dr-7, a durable decision of run <name> for
<KEY>" (`internal/api/inbox_decisions.go:207-212`). A question that
mirrors one is briefed "…; it asks dr-7, a durable decision, answered on
its card, never in the terminal" (`internal/api/inbox.go:494`), and a
decision link's item says "a link cannot answer a durable decision yet"
(`internal/api/lane_decisions.go:51`). The title is printed twice in
three ways (D12). A plain question's title and brief are both "<agent>
in <session> asks" (`inbox.go:468-469`, `:497`), and
`components/InboxItem.vue:115` and `:132` print both. A question no
longer open has its brief set to its `now` (`inbox.go:1141-1142`), which
the item prints again as "Now: …". A run's decision's title holds its
question, which the card prints again (`DecisionCard.vue:40`). Each of a
question's choices shows its scope line (`InboxItem.vue:170`, `:177`).
Approve and Retry are solid in the neutral colour
(`InboxItem.vue:182-183`): white in dark, black in light. The inbox's
step titles are "Request available", "Response recorded", "Received and
validated by the execution owner", "Work resumed"
(`utils/inbox.ts:154-159`).

**The change.**

- The rows. Each row of the page and of `JoinDecide.vue` leads its
  badges with `kindLook`'s icon and word
  (`data-inbox-row-kind="<kind>"`, PR-6's table). The link's badge "A
  durable decision" goes; a link's row answered and not yet applied
  keeps "Answered, not yet applied". The row's second line is
  `rowLine(s)` in `utils/inbox.ts`: the question when it differs from
  the title, else the brief, so a run's decision's row does not say its
  question twice. The palette's `decisionRows` (`usePaletteRows.ts:74`)
  takes `rowLine` for its detail, for the same reason.
- The first item opens by itself. A pure
  `firstToOpen(wide, openRef, sections)` in `utils/inbox.ts` gives the
  oldest open row's ref when the window is at `lg`
  (`useMedia('(min-width: 1024px)')`), no item is in the query and the
  list holds an open row; else nothing. Rows answered and not yet
  applied, alone, open nothing. `pages/decisions.vue` watches it and
  calls `router.replace` with `?item=`, so the query still names the
  open item, which `act`, `comment` and `readItem` rest on. It never
  acts below `lg` and never over an item already in the query, and the
  page's reader (`opened`, `:98-148`) drops a read for an item no longer
  open, so a row picked while the first item is read wins. "Pick an
  item" then shows only for a list of answered rows alone.
- The words, in the app. `inboxKindWords` gives "A question for you", "A
  plan to approve", "A task out of attempts", "A decision for you".
  `STEP_TITLES` become "Asked", "Your answer", "Checked by its owner",
  "Work resumed". `DecisionModal.vue`'s title (PR-20's "A decision for
  you") reads `inboxKindWords('decision')`.
- The words, on the server. No field and no frame changes, and
  `docs/protocol.md` describes these fields, not their words.
  `decisionItem` (`inbox_decisions.go:207-212`) titles the item with its
  question alone. It briefs a plan's gate "A decision for you, from the
  plan of DEC-1" when the run has a ticket and "A decision for you, from
  the plan of run <name>" when it has none, and an agent's request "A
  decision for you, from <agent> in attempt build.2 of run <name>", with
  " for DEC-1" when there is a ticket. The mirrored question's brief
  (`inbox.go:494`) says "…; it asks a decision for you, dr-7, answered
  on its card, never in the terminal": the id stays where it tells the
  person which card to open. `linkCannotAnswer` (`lane_decisions.go:51`)
  says "a link cannot answer a run's decision yet", and the link's cut
  line (`lane_decisions.go:120`) "More than N of a run's decisions are
  in this link's scope…", with its quote in `docs/protocol.md:2336`.
- D12. A pure `briefLine(item)` in `utils/inbox.ts` gives nothing when
  the brief is the title or the item's `now`, and the rest, capitalised,
  when the brief starts with the title and "; " ("Attempt build.1 of run
  Ledger migration for DEC-1"). `InboxItem.vue:132` draws `briefLine`,
  and nothing when it is empty; Brakeman's brief, drawn by
  `BrakemanDecisionText`, is not touched. For a run's decision
  `InboxItem.vue` draws no title heading: the card's question heads the
  item, under the kind line and the time.
- A question's fine print. Its scope lines (`data-action-scope`, each
  choice's and the line field's) fold under one ghost "Details" button
  at the foot of its choices (`data-inbox-scopes-toggle`), folding as
  the evidence folds. A plan's and a task's scope lines stay in view:
  they say what an approval or a retry authorizes.
- Approve and Retry (`InboxItem.vue:182-183`) become `color="primary"`,
  solid: the forest of Launch and Send. Accept anyway stays a warning
  outline, Cancel and Withdraw error outlines. PR-26 leaves this line
  alone.

**Tests.**

- `web/app/utils/inbox.test.ts` (vitest):
  `firstToOpen(true, '', {open: [a, b]})` is `a.ref`; with `wide` false,
  with an item in the query, with an empty list and with tracking rows
  alone it is nothing. Without the change: no function.
- `web/app/utils/inbox.test.ts` (vitest): `briefLine` gives nothing for
  a brief equal to the title and for one equal to `now`, the rest
  capitalised for a crew member's question, and the brief whole when it
  does not start with the title. `rowLine` gives the question for a
  question whose title differs from it and the brief for a decision
  whose title is its question. Without the change: no functions, and the
  item draws the brief whole.
- `web/app/utils/inbox.test.ts` (vitest): `:84` expects the new step
  titles and `:407-408` the new kind words. These edit today's
  assertions; they add no proof and lose none.
- `internal/api/inbox_decisions_test.go` (Go race test, new
  `TestADecisionItemIsInAPersonsWords`): an agent's approval (the
  `newDecisionFixture` of `TestADecisionRecordReplacesItsQuestion`) is
  titled by its question and briefed from "A decision for you, from"
  with its attempt named; a plan's gate of a ticket's run (as
  `TestAGateOverTheDesk` makes one) is briefed "A decision for you, from
  the plan of <KEY>"; for both, neither the title nor the brief holds
  "dr-", "durable" or "gate". Without the change: the title "Approval
  dr-1: …" fails the first check.
- `internal/api/inbox_test.go:303` (Go race test,
  `TestInboxDecisionQuestionOffersNoReply`): the mirrored question's
  brief holds "a decision for you, dr-7, answered on its card" and not
  "durable". `inbox_decisions_test.go:1215` takes the cut line's new
  start; `:974` and `:1110` read `linkCannotAnswer` by its name and
  stand.
- `web/e2e/decide-item.spec.ts` (Playwright, new), the inbox stubbed in
  the browser with `stubInbox(page, items)`: `inboxItem` of an open
  question older than an open run decision, a plan revision, a failed
  task, a run decision answered and not yet applied and a question no
  longer open, each with its item read. Through `dayPage`. At 1440 x
  900: every row carries `data-inbox-row-kind` with its kind;
  `/decisions` becomes `/decisions?item=<the question's ref>` and that
  item is drawn; no text says "Pick an item". The race: a route of the
  spec's own over `/api/inbox/item` holds the question's item read at a
  gate while the plan's row is clicked, then lets it go (wait on the
  mechanism); the plan's item is drawn, the question's has count 0, and
  the address names the plan. Reloaded, it still names the plan (check
  again after a redraw the test causes). At 390 x 844 with touch: once
  the rows are drawn, the address has no `item` and the list shows. D12:
  the plain question's item has one `[data-inbox-title]` and no
  `[data-inbox-brief-text]`; a crew member's question's brief reads
  "Attempt build.1 of run …" without the title's words; the question no
  longer open shows its `now` once; the run decision's item has no
  `[data-inbox-title]`, and its question's text appears once in the
  item. The question's `[data-action-scope]` lines have count 0 until
  `[data-inbox-scopes-toggle]` is pressed, then one a choice; the plan's
  are in view at once. In both themes (`dayPage`'s `theme`, then
  `useTheme` on the open page), Approve on the plan and Retry on the
  task have the computed background that `probeColour` reads for
  `var(--ui-primary)`. Without the change: no kind hook, the address
  stays `/decisions`, the brief repeats the title, the scope lines are
  in view, and Approve is white in dark and black in light.
- `web/e2e/decision-page.spec.ts` (Playwright, its own server): `:475`
  and `:483` expect `/decisions?item=<ref>`, where `<ref>` is the
  `data-inbox-row` of the list's first open row read from the page,
  since the spec's earlier tests leave items open (count only what the
  test made); `:168` presses `[data-inbox-scopes-toggle]` before it
  reads "Types". The phone's steps at `:145` and `:160` keep
  `/decisions$`: below `lg` nothing opens by itself.
- `web/e2e/switchyard.spec.ts` (Playwright): `:689` reads
  `[data-inbox-row-kind="decision"]` with the word "Decision" in place
  of the badge "A durable decision"; `:702` reads "a link cannot answer
  a run's decision yet".
- `web/e2e/decisions-ask.spec.ts` (from #912): at 1440 the page now
  opens `decision:dr-901` by itself; its stub (moved into
  `inbox-stub.ts` by PR-1) answers that item read, and the measures of
  the rows and titles stand.
- No proof is lost: each edited assertion reads the same fact in the new
  words.

**By hand.** The four kind marks in the list's 24 rem column and at 390,
in both themes. A decision link's list on a phone through a switchyard:
its words and its tab.

**Size.** About 200 lines of code, 30 of them Go, and 320 of tests.

**Defaults.**

- **A run decision's words on the server.** Default: its title is the
  question alone, its brief "A decision for you, from the plan of
  DEC-1" or "A decision for you, from <agent> in attempt build.2 of run
  <name>". The mirrored question's brief keeps the id ("a decision for
  you, dr-7"), since it tells the person which card to open. Why: one
  source of words, which the decision link shows too.
- **The first item opens by itself.** Default: through `router.replace`
  with `?item=`, at `lg` only, the oldest open item, never over an item
  already open, and nothing for a list of answered rows alone. Why: the
  page's acting code rests on "the query names the open item".
- **A question's fine print.** Default: its scope lines fold under
  Details; a plan's and a task's stay in view.
- **The inbox's step titles.** Default: "Asked", "Your answer", "Checked
  by its owner", "Work resumed". Alternative: today's titles, which
  spell out "the execution owner".
- **Approve and Retry.** Default: forest, in PR-21, not PR-26. Why: the
  line has one owner, and this row edits it already.
- **A run decision's heading.** Default: its item draws no title
  heading, so the card's question heads it.
- **The decision link's words.** Default: "a link cannot answer a run's
  decision yet", and the cut line "More than N of a run's decisions…",
  with the quote in `docs/protocol.md`. Why: "durable decision" leaves
  every screen.

**Depends on.** PR-6, for `kindLook` and the page's "Pick an item" rule.
PR-20, for the card's controls and the modal's title: the item's heading
leaves the question to the card. Open pull request #912 merged: the
rows, `INBOX_ROW_CLASS`, `itemTimeWords` in the item's header and
`JoinDecide.vue`. It shares no file of the app with open #907, and
does not wait for it.

**Left out.** The sidebar's rows of every kind and the palette's two
groups made one (PR-13). The amber of "Open the terminal" on a row
(PR-26). The page's empty shape (PR-23). A decision link's tab and list
title, named "Needs you" with the rest in PR-5. The decide pass's own
page (`pages/decide/[[did]].vue`), which keeps "Decide".

### PR-22: web: one shape for a page with nothing in it: the session pages and History (P23 first half, D13)

A page with nothing to show draws one shape: the page's own sentence,
why it is empty and what will fill it, and the one action that fills
it. The home, the Yard, the Roundhouse, the sidebar's line and History
take it in this row. None of them draws its empty picture before the
first read any more: while the sessions are being read the page says
so, and a server with sessions never flashes the welcome on the Yard.
History with nothing typed and nothing kept says "No session is kept
yet." The welcome stays on the home, as it is until PR-30. The other
pages keep their own shapes until PR-23.

**Today.** Each page draws its own empty picture, and three of them
draw it before the sessions are read. `pages/yard.vue:314` draws
`WelcomeCard` whenever `attention.sessions` is empty, with no look at
`attention.loaded` (`composables/useAttention.ts:162`, set by the first
snapshot at `:176`), so a Yard opened on a busy server shows the welcome
for the time of the first read. `pages/roundhouse.vue:401-405` and the
sidebar's line (`components/SessionSidebar.vue:51`, `:368`) do the same.
The home waits for the read before its welcome (`pages/index.vue:46`)
but not before its other picture: its `v-else` at `:68-72` says "No
active sessions." while the first read is still out. History asks for
the latest sessions when nothing is typed (`pages/history/index.vue:55`)
and answers an empty reply with "Nothing in the history matches."
(`:184`) though nothing was asked. Its reading line is a bare
"Loading…" (`:178`), and its caption about the search (`:143`) stands
above the results for good. The one page that uses Nuxt UI's `UEmpty`
is Crews (`pages/crews/[[id]].vue:647`).

Every place that shows nothing on `main` at `9fb98b06`, its words, its
words after the stream, and the row that changes it. "About" is the
page's sentence from `utils/pages.ts` (PR-4). The review's twelve are
rows 1 to 4 (the Yard's two in one), 7, 8, 10, 11, 15, 16, 20, 23 and
25; the rest were found in the code, the not-found shapes among them.

| # | Place | Where on `main` | Today | After | Row |
|---|---|---|---|---|---|
| 1 | Home, a new install | `pages/index.vue:46`, `:67`; `WelcomeCard.vue:31-39` | "Welcome to Conductor" / "Nothing is running yet. Start with one of these." Launch agent · Load the example crews · Settings | The welcome, on the home only | PR-30 |
| 2 | Home, sessions but none live | `pages/index.vue:68-72` | "No active sessions. Launch an agent here or run `conductor host` from your machine." (also while reading) | "Nothing is running. Launch an agent, or resume one from Exited in the list." Launch agent (N). The reading shape before the first read | PR-22 |
| 3 | Yard, no session at all | `pages/yard.vue:314` | The welcome, drawn before the first read | About, then "Nothing is running. A terminal shows here when you launch an agent, or bring one in from another machine." Launch agent (N) · How to bring one in | PR-22 |
| 4 | Yard, no live session | `pages/yard.vue:315-319` | "No active sessions. Launch an agent or start one with `conductor host`." | As row 3 | PR-22 |
| 5 | Yard, a chip that matches nothing | `pages/yard.vue:324` | "Nothing matches this filter." | Unchanged: a filter's answer, not an empty page (PR-17 renames the chips) | none |
| 6 | Yard, a focused session that went | `pages/yard.vue:303-307` | "That session is gone." Back to the grid | The not-found shape: "That session is no longer on this server." Back to the grid · Its record | PR-22 |
| 7 | Roundhouse | `pages/roundhouse.vue:401-405` | "No active sessions. Launch an agent or start one with `conductor host`." (also while reading) | About, then "Nothing is running. The first agent you launch shows here." Launch agent (N) | PR-22 |
| 8 | The sidebar | `SessionSidebar.vue:51`, `:368` | "No sessions yet. Launch an agent here or run `conductor host` from your machine." (also while reading) | "No sessions yet.", after the first read only. Launch agent is above it | PR-22 |
| 9 | The phone's Sessions page | `pages/sessions/index.vue:50` | The sidebar's line | The sidebar's line of row 8; with nothing on the server, the welcome (an owner decision) | PR-22, PR-30 |
| 10 | Needs you | `pages/decisions.vue:309-312`, `:436` | "Nothing needs your decision now." / "Pick an item to see its brief, its choices and what each authorizes." Nothing at all before the first read | PR-6's page, in PageEmpty's shape; the reading shape before the first read | PR-6, PR-23 |
| 11 | Crews | `pages/crews/[[id]].vue:647-658` | `UEmpty` "No crews yet" and a paragraph; "Loading crews…" | About, then today's paragraph. New crew · Load the examples. "Reading the crews…" | PR-23 |
| 12 | A crew not found | `pages/crews/[[id]].vue:707-710` | "No crew has the id `x`." All crews | The not-found shape, same words and action | PR-23 |
| 13 | A crew's Runs tab, its chart | `CrewRunsPanel.vue:182`, `:137` | "No runs yet. Launch run starts one." / "No runs yet." | Unchanged: a list inside a page | none |
| 14 | A crew with no member | `CrewGraph.vue:274`, `CrewMembersTable.vue:259` | "No members yet." / "No members yet. A crew needs at least one to launch." | Unchanged: an editor's state | none |
| 15 | Dispatch | `pages/dispatch/index.vue:116-122` | "Reading the projects…" with no spinner; "Dispatch keeps a team's work as tickets: what is asked, who owns it, what became of it. A project holds them under its key." | About, then today's sentence. New project. The reading shape | PR-23 |
| 16 | A project, the list | `pages/dispatch/[project]/index.vue:620-626` | "Reading the tickets…"; "CND has no tickets yet." / "No ticket matches the filter." | "CND has no tickets yet." New ticket (C). Narrowed: "No ticket matches the filter." The reading shape | PR-23 |
| 17 | A project, Board and Roadmap | `pages/dispatch/[project]/index.vue:634-636` | Empty columns, no words | As the list (row 16) | PR-23 |
| 18 | A project not found | `pages/dispatch/[project]/index.vue:579-583` | "No project has the key `X`." Dispatch | The not-found shape, same words and action | PR-23 |
| 19 | A ticket not found | `DispatchTicketPage.vue:867-869` | "No ticket has the key `X-9`." | The not-found shape, same words, a way back to the project | PR-23 |
| 20 | Events, the feed | `EventFeed.vue:27` | "Nothing yet. Events show up here as agents report them." | About, then "Nothing yet since this tab opened. An agent reports when it needs input, finishes a turn, hands work off or hits an error." Which agents report | PR-23 |
| 21 | The feed's last hour | `EventsAside.vue:48` | "Activity per minute appears once two events arrived in the last hour." | Unchanged: a chart's note | none |
| 22 | Events, Integrations | `IntegrationsTable.vue:85` | "No hook adapters listed." | Unchanged (the server always lists its adapters); moves with the table in PR-27 | none |
| 23 | History, nothing typed | `pages/history/index.vue:178-184` | "Loading…"; "The latest sessions" / "Nothing in the history matches." | About, then "No session is kept yet." With a query: "Nothing in the history matches." "Reading the history…" | PR-22 |
| 24 | A session's record, nothing kept | `pages/history/sessions/[id].vue:158` | "Nothing was kept." | Unchanged: a section of a record | none |
| 25 | Brakeman | `pages/brakeman.vue:102-105`, `:155` | A paragraph that never leaves; "No brief yet." (also before the first read) | The paragraph in About once a brief exists; "No brief yet. Ask for the first one above." after the first read | PR-23 |
| 26 | Agents | `pages/agents.vue:163-165`, `:219` | A paragraph that never leaves; "No agents in the catalog." | The paragraph in About once the catalog has an agent; "The catalog has no agent. Add one here, or in the config file's catalog." Add agent. "Reading the catalog…" | PR-23 |
| 27 | A session the server no longer holds | `pages/sessions/[id].vue` (D7); `:653` "Loading session…" | The page's error, with an idle badge, Share and More | The gone state, in the not-found shape | PR-19 |
| 28 | A run not found | `pages/runs/[run].vue:521-525` | "No run has the id `x`. The server forgets runs when it restarts." Crews | The not-found shape, same words and action | PR-23 |

Three kinds of line stay out of PageEmpty on purpose: a filter that
matches nothing (rows 5, and 16 narrowed), a list inside a page (13,
14, 22, 24) and a chart's note (21). Each is a part of a page that has
content. A reading line that already names what it reads ("Reading
CND-1…", `DispatchTicketPage.vue:1013`; "Reading the tickets…" on a
person's page, `pages/people/[name].vue:228`) keeps its words.

**The change.**

- `web/app/utils/pageState.ts` (new): `pageState({ read, error, count
  })` returns `'reading' | 'failed' | 'empty' | 'content'`. Content
  wins whenever something is shown, even with an error beside it (the
  page keeps it and its alert, as `pages/decisions.vue:306` does
  today). `'failed'` is only for a page that has never read anything.
  `'empty'` needs a read: unknown is not zero.
- `web/app/components/PageEmpty.vue` (new), over Nuxt UI's `UEmpty`:
  props `page` (an id of `utils/pages.ts`; its `about` is the first
  line, `data-page-about`), `state` (`'reading' | 'failed' | 'empty' |
  'missing'`), `words` (the second line, `data-page-why`), `error` (the
  server's words when failed), and `actions` (none to two; the first
  filled, each with its key as a `UKbd`). Reading draws the first line
  and "Reading the <thing>…" with a spinner; failed draws the server's
  words and Read again; missing draws the search icon, the words and
  the way back, with no first line. The root carries `data-page-empty`
  and `data-state`, and passes the attributes it is given through, so
  `[data-welcome]`, `[data-history-none]` and the like keep working.
  The not-found shape is a state of PageEmpty and not of `pageState`: a
  page knows it from its own 404, before any count.
- `web/app/components/PageAbout.vue` (new): a ghost icon button with
  the tooltip "About this page" in the page's header, shown only while
  `pageState` is `'content'`, opening the page's standing paragraph in
  a popover (`data-page-about-open`). While the page is not content
  the paragraph stands in its place, as today. The project page's own
  About and Fold (`pages/dispatch/[project]/index.vue:588-592`) is the
  precedent and is not changed: it is the project's description, not
  the page's.
- `pages/index.vue`: the welcome stays where it is (`:46`, `:67`). The
  other branch becomes PageEmpty with no first line (the home is not a
  destination of the table), "Nothing is running. Launch an agent, or
  resume one from Exited in the list." and Launch agent (N), drawn
  only once `attention.loaded` is true; before it, the reading shape.
- `pages/yard.vue`: `WelcomeCard` leaves the Yard (`:314`). Rows 3 and
  4 become one PageEmpty for `yard`: "Nothing is running. A terminal
  shows here when you launch an agent, or bring one in from another
  machine.", Launch agent (N), and "How to bring one in", a link to the
  README's Quick start, where `conductor host` is shown. Both wait for
  `attention.loaded`. The focused session that went (`:303-307`)
  becomes the not-found shape: "That session is no longer on this
  server.", Back to the grid, and Its record (a link to
  `/history/sessions/<id>`, the link PR-19 gives the session page).
- `pages/roundhouse.vue:401-405`: PageEmpty for `roundhouse`, "Nothing
  is running. The first agent you launch shows here.", Launch agent
  (N), after the first read.
- `components/SessionSidebar.vue`: `empty` (`:51`) also needs
  `attention.loaded`; the line (`:368`) says "No sessions yet." and no
  more, since Launch agent stands above it.
- `utils/history.ts`: `historyNoneWords(query)`: with nothing typed,
  "No session is kept yet."; with a query, "Nothing in the history
  matches." `pages/history/index.vue:184` draws it through PageEmpty
  for `history` (the first line only when nothing is typed), keeping
  `data-history-none`; the reading line (`:178`) becomes the reading
  shape, keeping `data-history-loading`; "The latest sessions" (`:182`)
  stays above a list that has entries. The search caption (`:143`)
  goes to PageAbout once the history has entries.
- `web/e2e/welcome.spec.ts` (from #908): its "a phone" case opens
  `/yard`, which no longer draws the welcome. The case is dropped
  here; PR-30 measures the card again where a phone shows it.
- `docs/features.md`: the entry.

**Tests.**

- `web/app/utils/pageState.test.ts` (vitest, new): not read and no
  error is reading; not read with an error is failed; read with zero is
  empty; read with entries and a later error is content; read with zero
  and a later error stays empty. Without the change: the file has
  nothing to import; and a `pageState` written as the Yard decides today
  (`empty` whenever the count is zero) fails the first case.
- `web/app/utils/history.test.ts` (vitest): `historyNoneWords('')` is
  "No session is kept yet."; `'zzz'` and `'class:said'` are "Nothing in
  the history matches." Without the change: one sentence for both, and
  the first case fails.
- `web/e2e/empty-pages.spec.ts` (Playwright, new), "a server with
  nothing on it": one server of its own made by PR-1's `ownServer`
  (nothing seeded; the shared e2e server is never empty, and counting
  there breaks "count only what the test made"), and its pages opened
  by `dayPage` over that server at 1440 x 900 and at 390 x 844 with
  touch, in both themes, set by the app's own key as the chapter Tests
  says. The empty server is only read: each case that makes something
  (the exited session below; PR-23's project) starts its own
  `ownServer`. On the Yard and the Roundhouse it waits for
  `[data-page-empty][data-state="empty"]` (never a "hidden" check
  first), then reads the three parts: the first line is the page's
  sentence, word for word, the second the words above, the action's
  label and its key. One `[data-page-empty]` on the page and no
  `[data-welcome]`. `uncut` on each part, `inside` the panel,
  `tapTarget(action, 44)` and no sideways scroll at 390. The theme is
  then switched and the three parts read again, so a check made only as
  the page first appears cannot pass by racing its redraw. At 390
  `/sessions` shows "No sessions yet." and nothing more. Without the
  change: the Yard draws `[data-welcome]`, the Roundhouse "No active
  sessions…", and the wait for `[data-page-empty]` times out naming the
  page.
- `web/e2e/empty-pages.spec.ts`, "a server whose one session has
  exited", on a server of its own (`ownServer`, not the empty one):
  the test makes one session through the API, stops it and
  waits on the API for `exited`. At 1440 the home shows "Nothing is
  running. Launch an agent, or resume one from Exited in the list.",
  the Yard row 3's shape, and the sidebar no empty line (the session
  is listed under Exited). Without the change: the home says "No
  active sessions. Launch an agent here or run…".
- `web/e2e/empty-pages.spec.ts`, "History with nothing kept": on the
  empty server `/history` shows "No session is kept yet." under
  History's sentence, and not "matches"; typing `zzz` shows "Nothing
  in the history matches." without the first line; clearing the box
  brings the first words back, read again after that redraw. Without
  the change: "Nothing in the history matches." with nothing typed.
- `web/e2e/empty-pages.spec.ts`, "the sessions not yet read" (on the
  seeded `day`, which has sessions; read-only): an init script records
  every `[data-welcome]` and every `[data-page-empty][data-state=
  "empty"]` that is ever added, as the recorder of
  `token-fragment.spec.ts:592-605` records the gate. The event stream
  and `GET /api/sessions` are held by `page.route` until the test lets
  them go; on `/yard`, `/roundhouse` and `/` the reading shape is
  shown while they are held. Released, the Yard's grid shows its
  tiles, and the recorder holds nothing. This waits on the mechanism
  (the held read), never on a clock. Without the change: the recorder
  holds the Yard's `[data-welcome]`, drawn while the read was held.
- `web/e2e/empty-pages.spec.ts`, "About this page" (on `day`): History
  with entries has no caption in its body; About this page opens it in
  a popover, which closes on Escape and is gone (`toHaveCount(0)`)
  before the next step, since a menu that has closed is not gone at
  once. On the empty server the caption stands in place and there is
  no About button. Without the change: the caption is in the body and
  no `data-page-about-open` exists.
- `web/e2e/history-page.spec.ts:134` and `:185` keep passing unchanged
  (`data-history-none` after a filter that matches nothing, with the
  same words; `data-history-loading` gone). `web/e2e/home-wide.spec.ts`
  keeps passing unchanged (the home's welcome).
- Moved, not lost: `welcome.spec.ts`'s "a phone" case at `/yard`
  (above). Its proof that the card's choices are whole at 400 x 800
  has no card to measure on a phone until PR-30. Its check of the
  token dialog's words stays; PR-30's rewrite keeps it, and PR-31,
  cut after PR-30, moves it.

**By hand.**

- The empty Yard, Roundhouse and History in both themes at 1440 and
  390: `UEmpty`'s icon tint and spacing against the brand palette.

**Size.** About 330 lines of code and 420 of tests.

**Defaults.**

- **Not found takes the shape too.** Default: `PageEmpty` gains a
  `missing` state for a focused Yard session that went (PR-22) and a
  crew, a project, a ticket and a run (PR-23); `pageState` keeps its
  four states. Why: the code draws not-found four ways.
- **The sidebar's empty line.** Default: "No sessions yet." alone. Why:
  Launch agent stands above it and the welcome says the rest.
- **D13's issue.** Default: PR-22's own issue names D13, rather than a
  twelfth row on #906. Why: #906 is a closed list of eleven.

**Depends on.** PR-4 (`utils/pages.ts`, whose `about` is the first
line). PR-1 (`ownServer`, `dayPage` over a server of the spec's own,
`day`, `web/e2e/layout.ts`). It builds on #908 (merged: it changed
`WelcomeCard.vue` and `SessionSidebar.vue` and added
`welcome.spec.ts`). Open #907 merged (it changes `pages/yard.vue`). It
shares `pages/yard.vue` and `pages/roundhouse.vue` with PR-17 and PR-18,
and `SessionSidebar.vue` with PR-7, PR-8, PR-13 and PR-14: whichever
lands second rebases on the first.

**Left out.** The other pages, the standing paragraphs of Brakeman and
Agents, and the not-found shapes of a crew, a project, a ticket and a
run (PR-23). The welcome's content (PR-30). The session page's gone
state (PR-19, D7). The rule "one shape for empty, reading and could
not read" written as a design rule (PR-37, in `docs/design/system.md`).
The review's "Copy the host command" on the Yard: the command carries
the workbench token, and the Launch dialog's "On this computer" already
builds it where it is offered (`LaunchSessionModal.vue:314-320`).

### PR-23: web: one shape for a page with nothing in it: the rest, and About this page (P23 second half)

Crews, Dispatch and a project, the Events feed, Brakeman, Agents and
the page of what needs you take PR-22's shape when they have nothing
to show, and its reading and could-not-read shapes while they read. A
crew, a project, a ticket and a run that are not found draw one shape
too. The paragraphs that Brakeman and Agents carry for good fold to
"About this page" once the page has content. A project's Board and
Roadmap say the project has no tickets, where today they draw empty
columns. What a full page shows does not change.

**Today.** Each page writes its empty words inline, in its own markup.
Dispatch reads "Reading the projects…" without a spinner and draws
nothing but an alert when the first read fails
(`pages/dispatch/index.vue:112-116`). Crews says "Loading crews…" with
one (`pages/crews/[[id]].vue:658`). A project's Board and Roadmap draw
their columns empty and say nothing (`pages/dispatch/[project]/
index.vue:634-636`). Brakeman's paragraph (`pages/brakeman.vue:102-105`)
stands above the page whatever it holds, with the Settings link
inside it (`data-brakeman-settings-link`), and "No brief yet." (`:155`)
shows before the first read. Agents' paragraph (`pages/agents.vue:
163-165`) never leaves, and "No agents in the catalog." (`:219`) is
the only empty word. The feed's line (`components/EventFeed.vue:27`)
does not say that the feed is this tab's alone: `useEvents` keeps it in
memory (`composables/useEvents.ts:35`), so it is empty at every load.
Not found is drawn four ways, in four markups (rows 12, 18, 19 and 28
of PR-22's table).

**The change.**

- `pages/crews/[[id]].vue`: the `UEmpty` at `:647-657` becomes
  PageEmpty for `crews`, with today's paragraph as its second line and
  New crew and Load the examples, keeping `data-crews-empty`; "Loading
  crews…" becomes the reading shape, "Reading the crews…". "No crew has
  the id" (`:707-710`) becomes the not-found shape.
- `pages/dispatch/index.vue:116-122`: PageEmpty for `dispatch`, today's
  sentence as the second line, New project; the reading shape; a first
  read that fails draws the failed shape with the server's words and
  Read again, in place of an alert over a blank page. Keeps
  `data-projects-empty`.
- `pages/dispatch/[project]/index.vue`: the list's empty (`:622-626`),
  "CND has no tickets yet." with New ticket and its key C, through
  PageEmpty with no first line (the project's description has its own
  About), keeping `data-list-empty`. Narrowed, "No ticket matches the
  filter." stays a plain line (a filter's answer) and keeps
  `data-list-empty` too. Board and Roadmap draw the same two in place
  of their columns when the shown list is empty. "Reading the
  tickets…" (`:620`) becomes the reading shape. No project with the key
  (`:579-583`) becomes the not-found shape, keeping
  `data-project-missing`.
- `components/DispatchTicketPage.vue:867-869`: the not-found shape,
  keeping `data-ticket-missing`, with a way back to the project.
- `pages/runs/[run].vue:521-525`: the not-found shape, same words.
- `components/EventFeed.vue:27`: PageEmpty for `events` (its sentence
  first), "Nothing yet since this tab opened. An agent reports when it
  needs input, finishes a turn, hands work off or hits an error." and
  Which agents report (to `/events?tab=integrations`; PR-27 points it
  at Agents).
- `pages/brakeman.vue`: the paragraph (`:102-105`) goes to PageAbout
  once a brief exists (the page's content is its briefs); before, it
  stands in place. The Settings link leaves the paragraph for the
  header, a ghost button "Settings" that keeps
  `data-brakeman-settings-link`, so it is never inside a closed
  popover. "No brief yet. Ask for the first one above." shows once the
  first read answered (`bk.state` set), and the reading shape before.
- `pages/agents.vue`: the paragraph (`:163-165`) goes to PageAbout once
  the catalog has an agent; the empty catalog (`:219`) becomes PageEmpty
  for `agents`, "The catalog has no agent. Add one here, or in the
  config file's catalog.", Add agent; the reading shape until the
  first answer (with #911, until its quick first answer).
- `pages/decisions.vue`: PR-6's zero page is drawn by PageEmpty for
  `decisions`, its words and its two lists ("What shows up here",
  "Decided in the last day") in the component's default slot; before
  the first read (`utils/inbox.ts:385` `inboxEmpty` waits for it, and
  the page draws nothing), the reading shape.
- `docs/features.md`: the entry.

**Tests.**

- `web/e2e/empty-pages.spec.ts` (Playwright, PR-22's, extended), "a
  server with nothing on it": on PR-22's own empty server, at 1440 x
  900 and 390 x 844 with touch, Crews, Dispatch, Brakeman and the
  Events feed each wait for their `[data-page-empty]`, then
  read the three parts (the page's sentence, the second line, the
  action's label and key), one empty shape a page, `uncut`, `inside`,
  `tapTarget(action, 44)` at 390. The theme is switched and the parts
  read again after the redraw. Without the change: Dispatch draws its
  `data-projects-empty` with no `data-page-empty`, the feed its plain
  line, and each wait times out naming the page.
- `web/e2e/empty-pages.spec.ts`, "an empty catalog": a server of its
  own still has the e2e catalog, so the catalog's read is answered by
  `page.route` with no agent; Agents shows its empty shape with Add
  agent, and while the route is held, its reading shape. Without the
  change: "No agents in the catalog." under a paragraph, and nothing
  while reading.
- `web/e2e/empty-pages.spec.ts`, "a project with no ticket": the test
  starts a server of its own (`ownServer`; the empty server is only
  read) and makes a project there through the API; the list, then
  Board, then Roadmap each show "<KEY> has no tickets yet." and New
  ticket with C; C opens the new ticket dialog (after waiting for the
  layout's sidebar root, as AGENTS.md asks of a first key). Without the
  change: Board and Roadmap show columns and no `[data-page-empty]`.
- `web/e2e/empty-pages.spec.ts`, "the first read fails": `page.route`
  answers 500 to `GET /api/dispatch/projects`; the failed shape shows
  the server's words and Read again. The route is then let through and
  Read again is pressed until the empty shape is shown (`readAgain`,
  moved from `dispatch-plan.spec.ts:28` into `fixtures.ts`), since the
  page also reads again by itself. Without the change: an alert and a
  blank body; the wait for `[data-state="failed"]` times out.
- `web/e2e/empty-pages.spec.ts`, "not found": `/crews/nope`,
  `/dispatch/NOPE`, `/dispatch/<a key the test made>/999` and
  `/runs/nope` each draw `[data-page-empty][data-state="missing"]`
  with their words and their way back, one shape each. Without the
  change: four markups, no `data-page-empty`.
- `web/e2e/empty-pages.spec.ts`, "About this page": on `day` only
  Agents is checked (the day has a catalog and no Brakeman); Brakeman's
  About is checked on a server of its own with `brakeman-stub`
  configured and one brief made through the API before the page opens.
  On each the paragraph is not in the body; About this page opens it;
  Escape closes it and the test waits for the popover to be gone before
  the next step. The theme is switched and the check made again. On the
  empty server Brakeman's paragraph stands in place. Without the change:
  the paragraph is in the body of both pages.
- Existing specs that keep passing on the kept attributes:
  `examples.spec.ts:18` (Load the examples inside `[data-crews-empty]`),
  `dispatch.spec.ts:115`, `:700`, `:912`, `:924` (`[data-list-empty]`),
  `dispatch.spec.ts:372-379` (`[data-project-missing]`,
  `[data-ticket-missing]`), `brakeman.spec.ts:137` (the Settings link's
  `href`, now in the header). PR-6's own spec of the zero page keeps
  its words; it gains one check that the page's root is
  `[data-page-empty]`.

**By hand.**

- Each converted page empty, in both themes at 1440 and 390, against
  PR-22's: the same spacing and icon size.
- The words page by page, read once by a person: they are product
  copy, and no test judges them.

**Size.** About 420 lines of code and 360 of tests.

**Defaults.**

- **Brakeman's Settings link.** Default: it moves from its paragraph to
  a header button. Why: it is never inside a closed popover once the
  paragraph folds.
- **A project's Board and Roadmap with no ticket.** Default: the
  project's empty shape in place of empty columns; a narrowed list
  keeps its plain line.
- **What stays a plain line.** Default: a filter that matches nothing, a
  list inside a page and a chart's note stay outside `PageEmpty`. Why:
  they are parts of a page with content.
- **Agents' empty words.** Default: "The catalog has no agent. Add one
  here, or in the config file's catalog." with Add agent.

**Depends on.** PR-22 (`PageEmpty`, `pageState`, `PageAbout`, the
spec). PR-12 (it rewrites the project page's toolbar and rows, the
same file). PR-6 (the zero page it converts). #899 (`pages/crews/
[[id]].vue`) and #911 (`pages/agents.vue`) merged first.

**Left out.** The two paragraphs of the Events page's Routing and
Integrations tabs: they move with their tables, to the Alerts panel
(PR-28) and to Agents' Reporting section (PR-27), where each becomes
its section's caption and is not folded. The empty states of lists
inside a page (PR-22's table, rows 13, 14, 21, 22, 24). Crews' section
captions (`pages/crews/[[id]].vue:634`, `:643`) stay as they are.

### PR-24: web: the brand's own neutrals; contrast as a test (P14, P20)

Every screen sits on the brand's greys instead of Nuxt UI's zinc: ink
and its deeper tone in the dark theme, white and sage in the light one.
The sidebar is drawn on a canvas tone of its own, a step off the page,
and the page is the panel beside it. The light theme's amber and green
words become dark enough to read. The terminal's canvas, the Files
editor and the charts follow the same scale. The components, the status
colours of the marks and the forest button stay as they are.

**Today.** `web/app/app.config.ts:13` sets `neutral: 'zinc'`. Nuxt UI
4.11.2's colours plugin writes every `--ui-color-neutral-N` from that
name, and its runtime stylesheet maps the text and surface tokens onto
the steps (light: dimmed 400, muted 500, toned 600, text 700,
highlighted 900, `bg` white, `bg-muted` 50, `bg-elevated` 100; dark:
dimmed 500, muted 400, toned 300, text 200, `bg` 900, `bg-elevated`
800). `web/app/assets/css/main.css:10-83` defines the brand's forest,
terracotta, signal, active, harbor and brick scales and no ink or sage
scale; `:85-94` overrides only the primary and secondary tokens. The
sidebar (`UDashboardSidebar`, `web/app/layouts/default.vue:300`) has no
background of its own, so it and the page are one tone. Zinc is also
written out by hand: 19 times in `web/app/utils/monaco.ts`, the
fallback at `web/app/utils/terminalTheme.ts:49-50` and the neutral
fallback at `web/app/utils/charts.ts:53`. In light, `text-warning` is
signal-500 on white at 2.94:1 and `text-success` active-500 at 3.96:1,
the faintest step (zinc-400 on white) is 2.62:1 and muted text on
`bg-elevated` is about 4.4:1. `docs/design/brand.md:102-103` says the
workbench imports `web/public/brand/tokens.css`; nothing imports it, and
the dark values that file calls references (`:20-24`) are not what the
app draws.

The scale, built from the brand's sage at 100 and its ink at 900. The
steps between are interpolated in OKLCH along the two hues, with each
step's lightness set by the contrast it must give (below), not by eye;
300 is the brand's own border colour.

| Step | Hex | Light theme | Dark theme |
|---|---|---|---|
| 50 | `#F6F9F3` | `bg-muted` | |
| 100 | `#EEF1E9` (sage) | `bg-elevated`, `bg-canvas` | |
| 200 | `#DCDFD7` | `bg-accented`, `border` | `text` |
| 300 | `#BFC9BE` (the brand's border) | `border-accented` | `text-toned` |
| 400 | `#8F948D` | `text-dimmed` | `text-muted` |
| 500 | `#666E67` | `text-muted` | `text-dimmed` |
| 600 | `#4D554E` | `text-toned` | |
| 700 | `#373F39` | `text` | `bg-accented`, `border-muted` |
| 800 | `#242C27` | | `bg-muted`, `bg-elevated`, `border` |
| 900 | `#18211C` (ink) | `text-highlighted`, `bg-inverted` | `bg` |
| 950 | `#0C1410` | | `bg-canvas` |

**The change.**

- `web/app/assets/css/main.css`: an `--color-ink-50` to
  `--color-ink-950` scale in the `@theme static` block, defined as
  forest is. All eleven steps are written: the colours plugin gives a
  name it does not know an empty fallback, so a missing step would draw
  no colour at all, silently.
- `main.css`, new tokens, named so that themes as files (#346) can set
  them later: `--ui-bg-canvas` (neutral-100 in `:root`, neutral-950 in
  `.dark`); `--ui-bg-canvas-hover`, `color-mix(in srgb, var(--ui-bg)
  60%, var(--ui-bg-canvas))` in both themes, so a hovered row is part
  way to the page's tone and the current row is the page's tone with
  its border; `--ui-warning-text` (signal-700 `#805000`, the brand's
  `--conductor-warning`, in light; `var(--ui-warning)` in dark) and
  `--ui-success-text` (`#246B41`, the brand's `--conductor-success`, in
  light; `var(--ui-success)` in dark). An `@theme inline` block maps
  `--background-color-canvas`, `--background-color-canvas-hover`,
  `--text-color-warning` and `--text-color-success` onto them. Tailwind
  reads `--text-color-*` before `--color-*` for a `text-*` class, so
  `text-warning`, the subtle badges and alerts and the outline buttons
  take the text-safe tone, while `bg-warning` dots and fills keep
  signal-500 and active-500.
- `web/app/app.config.ts`: `neutral: 'ink'`; a `dashboardSidebar`
  entry beside the `dashboardPanel` one that puts `bg-canvas` on the
  `root` and `content` slots (the rail, the full sidebar and the phone's
  slideover); `button` and `badge` entries that give a solid warning
  fill ink text (`text-(--ui-color-neutral-900)`), since white on
  signal-500 is 2.94:1. The comment above `colors` names the scale.
- The sidebar's hover and raised states, which use `bg-elevated` and
  would vanish on a sage canvas that is `bg-elevated` itself:
  `hover:bg-elevated/60` becomes `hover:bg-canvas-hover` in
  `SidebarSessionRow.vue:93`, `SidebarRunHeader.vue:60`,
  `SidebarSection.vue:19`, `SessionSidebar.vue:416-417`,
  `SidebarSharedList.vue:41`, `SidebarRail.vue:59,70` and
  `SidebarRailSquare.vue:37`; a square, chip or count drawn on the
  canvas with `bg-elevated` (`SidebarRail.vue:41,55,62`,
  `SidebarRailSquare.vue:47-52`, `SidebarHeaderMenus.vue:61`,
  `SidebarSharedList.vue:46`) takes `bg-default`, the page's tone. The
  current row's `bg-default border shadow-xs` stays: a white card on
  sage, an ink card on the deeper canvas. `designRules.test.ts` also
  fails on `hover:bg-elevated` in `Sidebar*.vue` and
  `SessionSidebar.vue`, so a sidebar component written before or after
  this row (PR-8's `SidebarRowPanel`, PR-13's `SidebarInboxRow`,
  PR-15's `SidebarTiles`) is held to `bg-canvas-hover`.
- `web/app/utils/monacoColors.ts`, new: the two editor themes' colour
  tables as plain data, from the ink scale (the editor's background is
  the page's `bg` in each theme, line numbers at the dimmed step, 400
  in light and 500 in dark, the active one at the toned step, the
  current line 50 and 800). `web/app/utils/monaco.ts` imports them
  and loses its 19 zinc literals; the token rules (forest, terracotta,
  harbor) stay.
- `web/app/utils/terminalTheme.ts:49-50`: the fallback's dark
  background `#18211C` and foreground `#DCDFD7`. The terminal canvas
  already follows `--ui-bg` (`composables/useTerminalTheme.ts`); the
  sixteen ANSI colours stay as they are, held by a test.
- `web/app/utils/charts.ts:53`: the neutral fallback `#666E67`.
- `web/app/utils/contrast.ts`, new: `luminance`, `ratio`, `over` (a
  colour at an alpha over another, for tints and opacity), a parser for
  hex, `rgb()` and `rgba()` that falls back to `cssColorToHex`
  (`terminalTheme.ts:118`) for `oklch()`, and `CONTRAST_FLOORS`, the
  table of minima below as data. It imports nothing from Nuxt, so the
  browser walker of PR-25 can import it too.
- Docs: `docs/design/brand.md`'s palette gains the scale's two anchors
  and says the workbench draws its greys from them; the false sentence
  at `:102-103` says what is true (`main.css` carries the scales;
  `tokens.css` names the same values, and a test holds that they
  agree). `web/public/brand/tokens.css:1` and `:20-24` give the real
  dark values (surface `#0C1410`, panel `#18211C`, text `#DCDFD7`,
  muted `#8F948D`). `docs/design/briefs/chat.md:11` and
  `docs/design/crews-graph-brief.md:184,196` stop saying "zinc
  surfaces". `scripts/brand_assets.py`'s check gains the two dark text
  pairs.

The minima the token test holds, with what this scale measures. A
cell reads floor (measured). Status text is the `text-*` colour of
each role; a tint is that role's colour at 10% over `bg` and over
`bg-canvas`, as a subtle badge draws it.

Light theme:

| Text | `bg` (white) | `bg-muted` | `bg-elevated` | `bg-canvas` |
|---|---|---|---|---|
| `text-highlighted` | 4.5 (16.49) | 4.5 (15.52) | 4.5 (14.45) | 4.5 (14.45) |
| `text` | 4.5 (10.87) | 4.5 (10.23) | 4.5 (9.52) | 4.5 (9.52) |
| `text-toned` | 4.5 (7.71) | 4.5 (7.25) | 4.5 (6.75) | 4.5 (6.75) |
| `text-muted` | 4.5 (5.26) | 4.5 (4.95) | 4.5 (4.61) | 4.5 (4.61) |
| `text-dimmed` | 3.0 (3.09) | 2.7 (2.91) | 2.7 (2.71) | 2.7 (2.71) |
| `text-primary` | 4.5 (11.66) | 4.5 (10.97) | 4.5 (10.21) | 4.5 (10.21) |
| `text-success` | 4.5 (6.45) | 4.5 (6.07) | 4.5 (5.65) | 4.5 (5.65) |
| `text-warning` | 4.5 (6.85) | 4.5 (6.45) | 4.5 (6.00) | 4.5 (6.00) |
| `text-error` | 4.5 (6.93) | 4.5 (6.52) | 4.5 (6.07) | 4.5 (6.07) |
| `text-info` | 4.5 (7.05) | 4.5 (6.63) | 4.5 (6.17) | 4.5 (6.17) |

Dark theme:

| Text | `bg` (ink) | `bg-muted` | `bg-elevated` | `bg-canvas` |
|---|---|---|---|---|
| `text-highlighted` | 4.5 (16.49) | 4.5 (14.34) | 4.5 (14.34) | 4.5 (18.70) |
| `text` | 4.5 (12.24) | 4.5 (10.64) | 4.5 (10.64) | 4.5 (13.87) |
| `text-toned` | 4.5 (9.67) | 4.5 (8.41) | 4.5 (8.41) | 4.5 (10.96) |
| `text-muted` | 4.5 (5.33) | 4.5 (4.64) | 4.5 (4.64) | 4.5 (6.04) |
| `text-dimmed` | 3.0 (3.13) | 2.7 (2.73) | 2.7 (2.73) | 2.7 (3.55) |
| `text-primary` | 4.5 (7.36) | 4.5 (6.40) | 4.5 (6.40) | 4.5 (8.34) |
| `text-success` | 4.5 (6.33) | 4.5 (5.50) | 4.5 (5.50) | 4.5 (7.17) |
| `text-warning` | 4.5 (7.49) | 4.5 (6.51) | 4.5 (6.51) | 4.5 (8.49) |
| `text-error` | 4.5 (5.85) | 4.5 (5.08) | 4.5 (5.08) | 4.5 (6.63) |
| `text-info` | 4.5 (6.22) | 4.5 (5.41) | 4.5 (5.41) | 4.5 (7.05) |

Tints, floor 4.5 everywhere: light primary 9.81 over `bg` and 8.61
over `bg-canvas`, success 5.76 and 5.07, warning 6.21 and 5.51, error
5.94 and 5.21, info 6.08 and 5.35; dark primary 6.11 and 7.12, success
5.37 and 6.24, warning 6.28 and 7.25, error 5.08 and 5.83, info 5.30
and 6.15. Fills, floor 4.5: light white on forest-700 11.66, on
brick-500 6.93, on harbor-500 7.05, ink on signal-500 5.60; dark ink on
forest-300 7.36, brick-400 5.85, harbor-400 6.22, signal-400 7.49,
active-400 6.33. White on light active-500 (3.96) is not held: no
solid success fill carries text in the app, and PR-37's status rule
keeps it so. `bg-accented` is a hover and a track, not a text surface:
only `text-highlighted` and `text` are held on it (8.06 in both
themes). `text-muted` on the hover tone is held at 4.5 (4.98 in light,
5.63 in dark).

The one price: `text-dimmed` cannot be 3:1 on every surface. In light
the 400 step is dimmed text and in dark it is muted text on
`bg-elevated`, so one step cannot be both darker than 3:1 on sage and
lighter than 4.5:1 on 800. The floor is 3:1 on the page and 2.7:1 on
the canvas and raised surfaces, and dimmed text is used only for what
is said again elsewhere (PR-37 writes that rule). On main it is 2.62:1
on white and about 2.3:1 on `bg-elevated` in light, and 3.08:1 on
`bg-elevated` in dark, the one pair that drops.

**Tests.**

- `web/app/utils/contrast.test.ts` (vitest, new): the ratio and the
  composite reproduce `brand.md`'s table (ink on sage 14.45, muted
  `#526454` on sage 5.56, accent on sage 5.24, white on forest 11.66),
  `#767676` on white is 4.54 and `#777777` 4.48, and `over` of
  signal-500 at 10% over white gives the tint the table uses; `oklch()`
  input parses through `cssColorToHex`. Without the change: the module
  is new; a luminance without the sRGB linearisation gives ink on sage
  near 9:1 and fails the first case.
- `web/app/utils/themeContrast.test.ts` (vitest, new), the contrast
  test of the tokens. It reads `main.css` with `?raw`, Nuxt UI's runtime
  `index.css` and its `plugins/colors.js` from `node_modules` with
  `readFileSync` (so an upgrade that remaps the tokens or the 500 and
  400 steps is followed, not hidden), and `app.config.ts` with a
  stubbed `defineAppConfig`. It resolves every `--ui-*` token in both
  themes, `color-mix` through `over`, and asserts each pair of the two
  tables, the tints, the fills and the hover tone against
  `CONTRAST_FLOORS`; that the neutral is `ink` and every one of its
  eleven steps is defined; that `bg-canvas` differs from `bg` in both
  themes; that `tokens.css`'s light and dark values equal the tokens
  they name; and that `monacoColors.ts`'s editor background equals each
  theme's `bg`, its foreground is at least 4.5:1 on it and its line
  numbers at least 3:1. Without the change: on main it fails on
  `zinc`, on light `text-warning` (2.94) and `text-success` (3.96), on
  light dimmed (2.62), on light muted over `bg-elevated` (about 4.4),
  on the missing canvas token and on Monaco's dark line numbers
  (`#52525B` on `#18181B`, 2.29). A scale interpolated at zinc's
  lightnesses fails it too (muted on sage 4.21), so the test, not a
  look at a screenshot, sets the 400 and 500 steps.
- `web/app/utils/terminalTheme.test.ts` (vitest), changed: the
  fallback case at `:69-77` expects `#18211C` and `#DCDFD7` for dark;
  new cases build the theme on ink and on white and hold every ANSI
  base colour except black at 4.5:1 and every bright colour except
  bright black at 3:1. Without the change: the fallback case fails on
  main's `#18181B`. The palette cases pass on main (dark bright black
  3.59 on ink, light bright white 3.04) and are a guard: they fail if a
  later retune for ink goes too far.
- `web/app/utils/designRules.test.ts` (vitest), new, the harness for
  rules read from the sources (`import.meta.glob` of the app's `.ts`
  and `.vue` with `?raw`, test files left out, as `passSites.test.ts:7`
  does) and its first rule: no zinc hex (`#09090B` to `#FAFAFA`, the
  eleven steps) and no word `zinc` in a source, except the ticket label
  colour at `utils/dispatch.ts:127`. Without the change: it fails on
  main with `monaco.ts` (19), `terminalTheme.ts` (2), `charts.ts` (1)
  and `app.config.ts` (1).
- `web/e2e/theme-surfaces.spec.ts` (Playwright, new), on the seeded day
  through `dayPage` at 1440 x 900 and at 390 x 844 with touch, in each
  theme: `html`'s background is `rgb(24, 33, 28)` in dark and white in
  light; the sidebar's root (on the phone, the slideover's content,
  opened by its button) has the canvas's colour, read through a probe
  element styled `var(--ui-bg-canvas)`, and differs from the page
  panel's; a session row hovered reaches the hover tone and differs
  from both the canvas and the current row; the terminal host and
  Monaco's background in Files equal the page's; the Yard's "Needs you"
  filter pressed (a solid amber fill) draws its label in ink at 4.5:1
  or more, by `contrast.ts`. The hover is read by polling until the
  background equals the probe's hover tone, never once after the hover:
  the row's `transition-colors` passes through other colours on the way
  (AGENTS.md: wait for a state that holds, never one passed through).
  Without the change: on main the page is `rgb(24, 24, 27)`, the
  sidebar is the page's tone, and the amber label is white at 2.94:1.
  Monaco equals the page on main too; that case fails if the scale
  changes and the editor's colours do not.
- `scripts/brand_assets.py --check` (CI's brand check): two more pairs,
  dark text and dark muted on the dark panel (12.24 and 5.33). Without
  the change: the pairs read `tokens.css`'s old references and pass;
  they are a guard on the new values.

No spec moves. `web/e2e/crews.spec.ts:26-27,64` asserts the unsaved
bar's class, not its colour, and stays. The theme fix of
`rail-squares.spec.ts:75` is PR-1's.

**By hand.**

- The forest button on sage and on ink, and the reversed mark on the
  deeper dark canvas, at 1440 and 390, in both themes.
- Shiki's `github-dark` code view in Files and the Markdown preview
  (`FileBrowser.vue:257`, `MarkdownPreview.vue:101`), which draw their
  own grey ground on ink.
- A real Claude Code and a real Codex screen in a terminal on ink and on
  white: the sixteen colours are held by numbers, not looked at.
- Exited rows (`opacity-75`) and unavailable agent cards (`opacity-60`)
  on the new canvas; the token test cannot see opacity (PR-25 can).

**Size.** About 230 lines of code (`main.css` 50, `app.config.ts` 15,
the sidebar's classes 25, `monacoColors.ts` 60 with `monaco.ts` 36
fewer, `contrast.ts` 70, the rest 5) and 40 of docs, and about 430 of
tests.

**Defaults.**

- **The dimmed text's floor.** Default: 3:1 on the page and 2.7:1 on the
  canvas and raised surfaces, in both themes. Why: Nuxt UI uses the 400
  step for light dimmed and dark muted text, so no one step is both;
  dimmed text is kept for what is said again elsewhere.
- **The light success text.** Default: the brand's `#246B41`, not
  active-600. Why: active-600 on its own tint over sage is 4.31:1,
  `#246B41` 5.07:1.
- **Solid amber fills.** Default: ink text in both themes. Why: white on
  signal-500 is 2.94:1 in light.
- **The 300 step.** Default: the brand's border colour `#BFC9BE`; the
  other steps between sage and ink are set by the contrast they must
  give, decided by the token test, not a screenshot.
- **The sidebar's hover.** Default: 60 % of the page's tone mixed into
  the canvas, and the current row the page's tone with its border:
  today's strength.
- **The terminal's sixteen colours.** Default: unchanged on ink, held by
  guard tests; changed only if a by-hand look at real Claude Code and
  Codex screens finds one off.

**Depends on.** PR-1: `dayPage` and its theme switch, without which a
"light" pass runs dark. Open PR #912 merged (`SidebarSessionRow.vue`,
whose hover class changes here); it builds on what #908 (merged) made
of `SessionSidebar.vue`, `SidebarSection.vue` and
`SidebarSharedList.vue`.

**Left out.** The desktop's splash (`desktop/src/splash.ts:56`,
`desktop/static/splash.html:10`) and the switchyard's own pages
(`internal/api/switchyard_page.go:352`) keep the zinc palette that
`brand.md:104-108` gives them; moving them is an owner decision and its
own issue. Themes as files (#346). Retuning the terminal's sixteen
colours. The rendered contrast, opacity included (PR-25). The rule
text in `docs/design/system.md` (PR-37).

### PR-25: e2e: the contrast actually drawn (X-rendered-contrast)

Nothing a person sees changes. The suite gains a walk of the text the
browser actually draws, in both themes and at both sizes, that fails
when any text falls under its floor once opacity, tints over tints and
the real background under it are counted. What the tokens promise in
PR-24 is then checked where it is spent.

**Today.** No spec computes a contrast. Six specs read computed styles
for layout (`terminal-height`, `yardheader`, `rail-squares`, `zoom`,
`settings`, `tiles`). `axe-core` is not a dependency and is not added
here. The token test of PR-24 cannot see what the page does on top of
the tokens: an unavailable agent's card at `opacity-60`
(`pages/agents.vue:167`), an exited sidebar row at `opacity-75`
(`SidebarSessionRow.vue:93`), a badge's tint over a tinted row.

**The change.**

- `web/e2e/contrast.ts`, new. In the page, it collects each visible
  element with a non-blank text node of its own: its computed colour,
  font size and weight, and the chain up to the first opaque
  background, with each ancestor's background colour and opacity. It
  skips what has no CSS ground to compose (inside `.xterm`,
  `.monaco-editor`, `canvas`, `img`), text that is disabled
  (`[disabled]`, `[aria-disabled="true"]`, and what they hold), and
  text that is clipped to nothing (`sr-only`). In Node it judges with
  PR-24's `contrast.ts`: the background is composed up the chain, and
  where an ancestor has opacity below 1 both the text and the ground
  composed so far are laid over what lies beneath that ancestor. Floors
  from `CONTRAST_FLOORS`: 4.5:1, or 3:1 for large text (24 px, or
  18.66 px at weight 700), and for `--ui-text-dimmed` 3:1 where the
  first opaque ground is `--ui-bg` and 2.7:1 over `bg-muted`,
  `bg-elevated` or `bg-canvas`. It returns every failure with a path of
  `data-*` attributes and the text, and prints the ten worst.
- `web/e2e/contrast-known.ts`, new: the places already under their
  floor when this lands, each with its page, theme, width, the
  element's path, the reason and the row that removes it. It may only
  shrink: an entry the walk no longer finds fails the run, so the row
  that fixes a place must delete its entry. A later row that redraws a
  walked page keeps `contrast.spec.ts` green and edits
  `contrast-known.ts` (PR-26 the exited row and the Agents card).
- Transitions are switched off for the walk by a style the spec adds
  (`transition: none`, `animation: none`), so no colour is read mid-way.
- A failure that PR-24's own tokens caused is fixed here, in
  `main.css`, as AGENTS.md asks of a bug the stream made; it does not go
  into the known list. The list holds only what main already drew.

**Tests.**

- `web/e2e/contrast.spec.ts` (Playwright, new), first case, "the walker
  judges what it is shown": a page made with `setContent` holds
  `#767676` on white (passes, 4.54), `#777777` on white (fails, 4.48),
  muted ink text in an `opacity: 0.6` box on white (fails), a warning
  badge's text on its tint (passes), 24 px `#949494` on white (passes
  at 3:1, 3.03) and the same at 14 px (fails). Without the change: the
  walker is new; with the opacity step taken out the faded case passes
  and the case fails, and with the large-text rule taken out the 24 px
  case fails.
- `web/e2e/contrast.spec.ts`, the walk: on the seeded day, through
  `dayPage`, at 1440 x 900 and at 390 x 844 with touch, in each theme,
  one test a page: the Yard, a session of the day with its inspector
  open, the Needs you page with a question of the day open, Agents, the
  day's Dispatch project and its run's page. Each waits for the
  layout's sidebar root and the page's own marker before it walks
  (AGENTS.md: a page not yet drawn passes every check), walks, then
  hovers the first row of the page's list and walks that row again,
  since hover is where a tone on the canvas can vanish (AGENTS.md:
  check again after a redraw the test causes). On the phone the
  sidebar's slideover is opened once, on the Yard, and walked. Each
  test raises its timeout by its own waits' caps through `budget()`.
  Without the change: on PR-24's head with the known list emptied it
  fails on the Agents card and the exited row and prints them; on
  main's tokens it fails on every light amber word (2.94) and every
  light dimmed id (2.62).
- `web/e2e/contrast.spec.ts`, the list: an entry of `contrast-known.ts`
  the walk did not meet fails the run. Without the change: a fix that
  forgets its entry would leave the list claiming a failure that is
  gone.

**By hand.** Read the ten worst of one walk in each theme beside the
page, once, to see that the walker names what an eye sees.

**Size.** No app code, unless a fix of PR-24's tokens is needed (a
line or two of `main.css`). About 310 lines of tests (`contrast.ts`
150, the spec 130, the list 30).

**Defaults.**

- **Drawn contrast in CI.** Default: it gates, with a list of known
  places that only shrinks and fails on a stale entry; no `axe-core`,
  which is a new dependency.

**Depends on.** PR-24 (`contrast.ts`, `CONTRAST_FLOORS`, the scale it
walks) and PR-1 (`day`, `dayPage`, `budget()`).

**Left out.** `axe-core` and its rules beyond contrast (a new
dependency, `make notices`, and a second floor table). Focus rings and
borders, which are not text. Text over the terminal, Monaco, images
and the graph's canvas, skipped by name. The two known places are
fixed in PR-26.

### PR-26: web: one colour for the main action; mono for evidence; amber only when someone must act (P16)

Each work area has one filled button, in forest, for its next step;
the others are outlined. Sentences that were set in the code font
("started 07:29 · 6 agents", "up 9m · server · worktrees", "1 here now
· 0 share links") are set in Inter, and ids, paths, commits, commands
and keys stay in the code font. Amber is kept for a thing a person
must act on; a standing fact that asks nothing, such as an agent not
installed, is drawn neutral. Read failures ("could not be read, trying
again") stay amber, as they are.

**Today.** `brand.md:93-94` and `:99` already state two of these
rules; the code breaks them in a few places. Filled where they are not
the area's next step: "Open run" on every live run card
(`CrewRunCard.vue:81`), Copy beside Done in the crew's view-link
dialog (`pages/crews/[[id]].vue:736`, `:742`), the steps of
`ShareLinksModal.vue` (`:286`, `:364`, `:375`, `:380`) and
`pages/paste.vue:106,110,119`. `font-mono` is used 304 times in 109
sources; the sentences among them include `SidebarRunHeader.vue:77`,
`CrewRunCard.vue:77` (a whole line, switched back with `font-sans` at
`:79`), `SessionInspector.vue:192,222,237`, `SessionTile.vue:62`,
`NeedsYouPill.vue:144,180`, `CommandPalette.vue:538,588`,
`FileBrowser.vue:506,524`, `DispatchRunCard.vue:63`,
`pages/roundhouse.vue:425,447`, `HistoryRow.vue:31` and
`CrewsTable.vue:66`. Amber that asks nothing: "Not installed" on
Agents (`pages/agents.vue:180`, on a card already at `opacity-60`,
`:167`), `identityColor`'s `unidentified` (`:7`), "not configured"
(`IntegrationsTable.vue:37`, `EventsAside.vue:37`), and a shared link
"revoked" or "gone" (`SidebarSharedList.vue:16-17`). No test asserts a
font family or a button's variant today.

**The change.**

- Main action: `CrewRunCard.vue:81` "Open run" becomes neutral outline
  with its arrow; the crew's view-link Copy becomes neutral outline, so
  Done is that dialog's one filled button; `ShareLinksModal.vue` keeps
  one filled button a step; `paste.vue` keeps one. Approve and Retry on
  the Needs you page are PR-21's, Share and the reply bar's first
  choice PR-19's, the run header PR-9's; this row's checks hold them.
- Mono: the sentence lines listed lose `font-mono` (and `CrewRunCard`'s
  `font-sans` override goes with it); a line that mixes words with an
  id, a path or a commit keeps a mono span for that part alone, marked
  `data-mono="<kind>"`; a number that ticks ("up 9m", "1 of 3") takes
  `tabular-nums`. The pill's key hints are keys and stay mono, marked.
- Amber: "Not installed" becomes neutral subtle; `unidentified`
  becomes neutral, `failed` stays amber (a person should look); "not
  configured" is a neutral dot and muted words; "revoked" and "gone"
  are muted, and "unreachable", a read failure, stays amber.
- The two places of PR-25's known list: the unavailable agent card
  loses `opacity-60` and is drawn with muted text and a neutral icon;
  the exited sidebar row loses `opacity-75` and draws its name in
  `text-muted`. Both entries leave `contrast-known.ts`.
- `web/e2e/contrast.ts` gains `filledPrimaries(area)`: the visible
  buttons inside a locator whose computed background is the resolved
  `--ui-primary`.

**Tests.**

- `web/e2e/main-action.spec.ts` (Playwright), new, on the seeded day
  through `dayPage` at 1440 x 900 and at 390 x 844 with touch, in each
  theme. The Needs you page with the day's failed task open: the
  background of `[data-inbox-action=retry]` is the resolved
  `--ui-primary`, and with the day's plan revision waiting for approval
  open, so is `[data-inbox-action=approve]`'s (a run's decision item
  has no Approve action, `internal/api/inbox_decisions.go:201`). A
  session page: at most one of `filledPrimaries` in the navbar, and
  Share's background is transparent. A crew page with the day's live
  run: `[data-run-open]` is transparent. The page is only looked at,
  nothing pressed, so the read-only day stays as it was. Without the
  change: it fails at `CrewRunCard.vue:81`; without PR-21 at Retry
  (`bg-inverted`, a white button in dark); without PR-19 at Share.
- `web/e2e/main-action.spec.ts`, the code font: the computed
  `font-family` of the sidebar run header's subtitle, the run card's
  meta line and the inspector's People line does not start with
  "JetBrains Mono", while the run card's short run id
  (`CrewRunCard.vue:59`) and a task's id (`[data-task-id]`) still do.
  Without the change: it fails on the three sentences; an edit that
  sets an id in Inter fails the second half.
- `web/e2e/main-action.spec.ts`, amber: on Agents, the
  `[data-not-installed]` badge of the catalog's uninstalled agent (the
  one `identity.spec.ts:17` finds) is not drawn in the resolved warning
  text colour, while the Yard's "Needs you" filter, with the day's
  questions waiting, still is. Without the change: it fails at
  `agents.vue:180`; a change that greys every amber fails the Yard's
  half.
- `web/app/utils/designRules.test.ts` (vitest), the mono rule. It walks
  each template with `vue/compiler-sfc`'s `parse` (it ships with vue
  3.5.43: no new dependency). `font-mono` is allowed on `code`, `pre`,
  `kbd`, an input's `:ui`, and an element marked `data-mono` with one
  of `id`, `path`, `commit`, `command`, `key`, `log`, `measure`; every
  other use is counted per file against a frozen list that may only
  shrink, and an entry whose count fell must be lowered. Without the
  change: a new sentence in mono fails it; the lines this row moves
  fail it if the list is written as the target.
- `web/app/utils/designRules.test.ts`, one filled action an area: in
  each `UDashboardNavbar` and each `UModal` of a template, at most one
  `UButton` that is solid primary (no `variant` and no `color`, or
  `color="primary"`), the branches of a `v-if` chain counted apart (a
  stepped dialog draws one step at a time); a bound `:variant` is not
  judged. Without the change: it fails on the crew's view-link dialog
  (Copy and Done).
- Existing specs that may need new bounds: Inter at 11 px is narrower
  than the code font, so the cut points of meta lines move;
  `sidebar.spec.ts` and `yardheader.spec.ts:15-35` measure fit and are
  run again, their bounds changed only where the text now fits better.
  `decision-page.spec.ts:218-250,330-334` and `identity.spec.ts:17`
  find their buttons and badges by attribute and stay.

**By hand.** Inter at 11 px in the 200 px sidebar, the Yard tile's
foot and the palette's row detail; a ticking "up 9m" that does not
jitter; forest Approve and Retry on the Needs you page in both themes.

**Size.** About 150 lines of code (buttons 10, the code font 90, amber
15, the two faded places 15, the helper 20) and about 330 of tests (the
spec 180, the two template rules and their list 150).

**Defaults.**

- **What stays amber.** Default: confirms that lose or override work
  ("Save mine", "Read it again", "Resume anyway", Brakeman's "Do it")
  stay amber with ink text; an agent's "unidentified" goes neutral and
  "failed" stays amber; "records unavailable" and a link "unreachable"
  stay amber as read failures; "revoked" and "gone" go muted.
- **Open run.** Default: a neutral outline button with its arrow, not a
  plain link.
- **The faded places.** Default: the unavailable agent card and the
  exited sidebar row are drawn with muted text instead of opacity.
- **What `data-mono` may name.** Default: id, path, commit, command,
  key, log, measure; every other `font-mono` is a count per file that
  only shrinks.

**Depends on.** PR-19 (Share and the reply bar's first choice have one
owner), PR-21 (Approve and Retry in `InboxItem.vue`), PR-9 (the run
header's uptime), PR-24 (the rule harness, ink text on amber fills),
PR-25 (`contrast.ts` and the known list it empties) and PR-14 (the
exited row in `SidebarSessionRow.vue`, which that row redraws). Open PR
#911 rewrites the badges on `agents.vue`: it merges first.

**Left out.** Amber read-failure notices (61 `UAlert`s with
`color="warning"`): an owner decision kept out of this stream.
Confirms that lose or override work ("Save mine", "Read it again",
"Resume anyway", Brakeman's "Do it") stay amber, now with ink text.
The rules' text (PR-37).

### PR-27: web: an agent's hooks and skill are set up on its card (P12 Integrations to Agents, P13's card line)

Each card on Agents says whether its agent reports to Conductor:
"Reports to Conductor", "Not set up", "Experimental" or "No hooks".
Hooks and the Conductor skill are set up on the Agents page, in a
section "Reporting to Conductor" under the cards, which is the table
and the skill card that the Events page's Integrations tab holds
today. A card that is not set up links to its row there, where Install
on this machine sits beside the file it writes. The Events page keeps
its Feed and Routing tabs; an old link to its Integrations tab opens
the new section.

**Today.** Agents says how an agent signals by its catalog entry
(`signalBadge`, `pages/agents.vue:123-135`: "hooks", "pattern", "bell /
OSC", "none") and never reads `GET /api/integrations`, so it cannot say
whether Conductor's hooks are installed. That is judged twice, on the
Events page: `reporting()` in `components/EventsAside.vue:33-38` and
`wired()` with `status()` in `components/IntegrationsTable.vue:27-37`,
two copies of one rule. The table (`IntegrationsTable.vue`) lists
every hook adapter the server knows (`internal/api/integrations.go:
52-91`, in the order of `agents.All`) with Install on this machine.
The Integrations tab (`pages/events.vue:152-181`) holds it with a
paragraph and the skill card. An agent names its adapter in the
catalog (`adapter`, `composables/useSessions.ts:121-122`); one adapter
serves many agents (in the e2e catalog `claude`, `asker`,
`brakeman-stub` and seven more are Claude Code's), and `plain` has
none. The palette's rows "Events · Integrations" (`composables/
usePaletteRows.ts:62`) and the Events page's group with "Install hooks
for X" (`pages/events.vue:113-120`) point there.

**A card gets no Install control of its own.** The code decides it
three ways. An install is per adapter, not per agent
(`api.installIntegration(i.id)`, `POST /api/integrations/{id}/install`):
an Install on each of a dozen cards that share Claude Code's adapter
would read as a dozen installs and do one. An install writes into the
server user's home (`handleInstallIntegration`, `integrations.go:101`),
and the table shows the file it writes ("Config file", from `where`)
before the press; a card has no room for it. And an install can end in
"finish by hand" with a snippet (`no_file_route`, `snippetLeft` in
`utils/integrations.ts`), which opens below the table. So each card
says where it stands and links to its adapter's row, one press away on
the same page; the row holds the one Install. The review's "with
Install beside it" is the row, under the card.

**The change.**

- `utils/integrations.ts`: `reportStatus(integration | undefined)`
  returns `{ key, words }`: `'reports'` "Reports to Conductor" (hooks
  installed, or injected at launch: `installed || launchInjection`),
  `'experimental'` "Experimental", `'unset'` "Not set up", and for no
  adapter `'none'` "No hooks: it reports by the bell or a screen
  pattern". `EventsAside.vue`'s `reporting()` and
  `IntegrationsTable.vue`'s `wired()` read it; each keeps its own
  longer words ("3 hooks installed", "injected at launch") and its
  tone.
- `pages/agents.vue`: reads `api.integrations()` beside the catalog.
  Each card gains one line (`data-agent-reports=<key>`) from
  `reportStatus` of the integration whose id is the agent's `adapter`;
  "Not set up" is neutral (it asks nothing, as the plan rules for
  standing badges, PR-26) and is a link "Set up" to
  `#reporting-<adapter>`.
  `signalBadge` stays (how it signals is another fact).
- `pages/agents.vue`: a section "Reporting to Conductor"
  (`id="reporting"`) under the cards and the Hidden list:
  `IntegrationsTable` (each row gains `id="reporting-<id>"`) and the
  skill card, moved from `pages/events.vue:152-181` with their
  paragraph as the section's caption. The page's Refresh reads both.
  An install that changed files reads the integrations again, so the
  cards' lines follow at once.
- `pages/agents.vue`'s palette group gains "Install hooks for X" for
  each installable adapter not installed (moved from `events.vue:113-
  120`).
- `pages/events.vue`: the Integrations tab goes. `?tab=integrations`
  is answered with `navigateTo('/agents#reporting', { replace: true })`,
  so old links land on the section (PR-29 moves this into the
  redirect). The tab rows of its palette group lose Integrations.
- `utils/pages.ts` and `usePaletteRows.ts`: the row "Events ·
  Integrations" becomes "Agents · Reporting" (`/agents#reporting`).
- `EventFeed.vue`'s Which agents report (PR-23) and `EventsAside.vue`'s
  Reporting list link to `/agents#reporting`.
- README (the hooks paragraphs that name the Events page, around
  `README.md:896-934`), `docs/features.md`.

**Tests.**

- `web/app/utils/integrations.test.ts` (vitest, existing file, new
  cases): installed is `'reports'`; launch injection alone is
  `'reports'`; experimental and not installed is `'experimental'`;
  neither is `'unset'`; `undefined` is `'none'`. The test imports the
  one function `EventsAside.vue`, `IntegrationsTable.vue` and
  `agents.vue` call. Without the change: no `reportStatus` to import;
  a copy left in a component is not caught here but by the next test.
- `web/e2e/agents-reporting.spec.ts` (Playwright, new), on the shared
  server at 1440 x 900 and 390 x 844 with touch: the test reads `GET
  /api/integrations` through the API and, for `claude`, `asker`,
  `brakeman-stub` (all Claude Code's adapter), `goose` and `plain`,
  expects the card's `data-agent-reports` to be the key the answer
  gives (`plain`: `none`). Every card of one adapter says the same.
  Without the change: no `data-agent-reports` on any card.
- `web/e2e/agents-reporting.spec.ts`: a card whose line is "Not set up"
  (chosen from the API's answer, never by name) is pressed; the URL
  ends `#reporting-<adapter>` and that row is in view (`inside` the
  viewport). At 390 the line is `uncut` and its link a 44 px target.
  Without the change: no link.
- `web/e2e/agents-reporting.spec.ts`, on a server of its own
  (`ownServer`; an install writes into its home and must not change
  the shared server's answers for other specs), whose catalog adds a
  second card of an adapter that installs and has no launch injection
  (`goose`, `internal/agents/goose.go:32-36`, with no `Inject`): both
  cards read "Not set up"; Install on this machine on the first; the
  test waits for the install's answer, then for the integrations to be
  read again (the request after it), then expects both cards "Reports
  to Conductor". It proves one install moves every card of the
  adapter. `claude` stays the case that reads "Reports to Conductor"
  with nothing installed (its hooks are injected at launch,
  `internal/agents/claude.go:103`). Without the change: no cards' lines
  to move.
- `web/e2e/events.spec.ts:70-78` ("Integrations lists every agent with
  its status and opens a snippet") moves into
  `agents-reporting.spec.ts`, opened at `/agents#reporting`: the
  `claude` row's status is not empty, its snippet opens, the skill card
  shows. Plus `/events?tab=integrations` lands on `/agents#reporting`
  with the `claude` row visible. Nothing of it is lost.
- `web/e2e/palette-rows.spec.ts:228-245`: the Events part that opens
  the integrations tab and reads "(refreshed)" there moves to Agents:
  the Agents page's Refresh row asks again and the section shows the
  answer. `events.spec.ts:38-53` keeps reading
  `[data-reporting-agent="claude"]` in the feed's aside, now on the one
  rule.

**By hand.**

- The card's line beside the identity and signal badges in both
  themes; the section under a long catalog at 390.

**Size.** About 260 lines of code and 220 of tests.

**Defaults.**

- **An agent with no adapter.** Default: "No hooks: it reports by the
  bell or a screen pattern".

**Depends on.** PR-4 (`utils/pages.ts`, where the palette row lives).
#911 merged (it rewrites `pages/agents.vue`'s read and badge row). If
PR-23 has landed, Agents' paragraph is already in PageAbout and the
section's caption is the only paragraph the move adds.

**Left out.** The tone of the table's "not configured" (amber today,
`IntegrationsTable.vue:37`): PR-26 sets the standing badges' tones.
The Routing tab (PR-28) and the feed (PR-29). Settings' "Agents on this
machine" (`pages/settings.vue:514`) stays as the PATH read and gains a
link to Agents.

### PR-28: web: Alerts opens the rules (P12 Routing to Alerts, P13's link)

Alerts holds both halves of how a person is alerted: the two switches
(a browser notification, a chime) and the rules for where each kind of
event goes. The menu's Alerts opens one panel with both, at the right
of the window; the phone's More keeps its switches and ends them with
"All alert rules", which opens the same panel. The Events page loses
its Routing tab; an old link to it opens the panel over the feed. The
rules themselves, and where they are kept (this browser), do not
change.

**Today.** The bell in the sidebar's header opens a 16 rem popover of
the two switches (`components/SidebarHeaderMenus.vue:49-56`,
`components/AlertSwitches.vue`), whose first line sends the reader to
"the Events page" (`AlertSwitches.vue:8`). The rules are the Routing
tab of Events (`pages/events.vue:147-150`): a paragraph and
`RoutingRows`, ten event types by four destinations and the webhooks
of `conductor.json`, kept in this browser (`composables/useEvents.ts`,
`localStorage` `conductor.events.routes`). The feed's aside has Edit
routing (`EventsAside.vue`, `emit('editRouting')`), the palette a row
"Events · Routing" (`usePaletteRows.ts:61`), and the Roundhouse's
Follow switch a tooltip naming "the Events page" (`pages/
roundhouse.vue:390`). The phone shows the switches inline in More
(`components/BottomBar.vue:89`). PR-7 moves Alerts from the header into
your menu, where it opens `AlertsPanel.vue`, a slideover of the two
switches mounted once in the layout.

**The change.**

- `components/AlertsPanel.vue` (PR-7's): the `USlideover` titled
  "Alerts", at most 44 rem wide at 1440 and the whole width at 390,
  holds the switches (`AlertSwitches`) and now, under them, "Where each
  event goes", with the paragraph
  of `events.vue:148` as its caption ("Where each kind of event goes
  in this browser. The routing is kept here, not on the server;
  webhooks are set in `conductor.json`.") and `RoutingRows` with the
  webhooks. `useAlertsPanel()` (new) holds its open state, so that the
  palette, `?alerts` and the phone's More open the one panel the layout
  mounts.
- The menu's Alerts (PR-7's item in your menu,
  `SidebarHeaderMenus.vue`) opens the panel through `useAlertsPanel()`.
  The palette gains a row "Alerts…" (`action:alerts`) beside "Browser
  notifications", "Chime" and "Needs input → …", which stay
  (`usePaletteRows.ts:554-560`); its "Events · Routing" row goes.
- `AlertSwitches.vue`: the first line becomes "When a session needs
  input, and for the events routed to Browser:". Where it is drawn
  alone (the phone's More) it ends with a link "All alert rules" that
  opens the panel; inside the panel it has none.
- A query `?alerts` on any page of the default layout opens the panel
  and is taken off the address (`router.replace`), so a link can open
  it.
- `pages/events.vue`: the Routing tab goes; `?tab=routing` opens the
  Feed tab with the panel open. `EventsAside.vue`'s Edit routing opens
  the panel.
- `pages/roundhouse.vue:390`: the tooltip reads "Jump to sessions whose
  events are routed to Roundhouse jump in Alerts: input requests and
  handoffs unless changed there".
- README (the Roundhouse jump and the webhooks' words that name the
  Routing tab, `README.md:595`, `:958-1008`), `docs/features.md`.

**Tests.**

- `web/e2e/alerts.spec.ts` (Playwright, new), at 1440 x 900: your menu,
  then Alerts; the panel shows "Browser notification" and
  `[data-routing-matrix]`; the pill `done.browser` toggles, is kept in
  `localStorage` `conductor.events.routes`, and after a reload, with
  the panel opened again from the menu, is still on; `working.badge` is
  a fixed cell. The pill is put back at the end. This is
  `events.spec.ts:55-68` ("Routing toggles a destination as a pill and
  keeps it in this browser") moved, with the panel as its way in;
  nothing of it is lost. Without the change: the menu has no Alerts
  that opens a panel with `[data-routing-matrix]`.
- `web/e2e/alerts.spec.ts`, at 390 x 844 with touch: More shows the
  switches and "All alert rules"; a tap opens the panel; the matrix
  has no sideways scroll and each pill is a 44 px target
  (`tapTarget`); the panel closes on its close button and is gone
  (`toHaveCount(0)`) before More is checked again. Without the change:
  no "All alert rules".
- `web/e2e/alerts.spec.ts`: `/events?tab=routing` shows the feed with
  the panel open; `/crews?alerts` opens the panel and the address
  loses `alerts`. Edit routing in the feed's aside opens the panel.
  Without the change: the Routing tab, and `?alerts` does nothing.
- `web/e2e/sidebar.spec.ts:419-436`: the check that Alerts shows the
  switches (PR-7's, in your menu) now finds them in the panel, with
  `[data-routing-matrix]` under them. `phone.spec.ts:154` keeps
  "Browser notification" in More.

**By hand.**

- The panel in both themes at 1440 and 390: the matrix's pills against
  the panel's surface.

**Size.** About 230 lines of code and 200 of tests.

**Defaults.**

- **The phone's alerts.** Default: More keeps its switches inline and
  ends them with "All alert rules", which opens the panel; on the
  desktop your menu's Alerts opens it.
- **`?alerts`.** Default: on any page of the default layout it opens the
  panel and leaves the address. Why: old Routing links land.

**Depends on.** PR-27 (both remove a tab of `pages/events.vue`, one
after the other). PR-7 (Alerts in your menu: the item this row points
at the panel).

**Left out.** The feed and the Events page itself (PR-29). The phone's
switches stay inline in More, as the phone takes what fits.

### PR-29: web: History gets the feed; Events is retired (P12: Feed to History; ten destinations become seven; old links keep working)

History has two views: Search, which it opens on, as today, and Feed,
the live feed of what agents report with its last hour beside it. The
Events page is gone from the nav, the rail, the phone's More and the
palette; G E opens History's Feed. Every old link to `/events` lands
where its tab lives now. With PR-17's Roundhouse move, the destinations
are seven: Needs you, Yard, Crews, Dispatch, Brakeman, Agents, History,
with Settings in the desktop app.

**Today.** `pages/events.vue` holds the feed (`EventFeed` and
`EventsAside`, `:133-145`) under the title Events, after PR-27 and
PR-28 its one tab. The feed is this tab's: `useEvents` keeps it in
`useState` memory (`composables/useEvents.ts:35`), filled by the live
stream on every page, empty at each load. History
(`pages/history/index.vue`) is one search page with no views. The
Events page is linked from the nav (`layouts/default.vue:234`), the
phone's More (`BottomBar.vue:40`), the palette (`usePaletteRows.ts:59`),
G E and Alt+E (`default.vue:276`, `:290`) with the label "Go to Events"
(`useShortcuts.ts:39`), a crew run's feed card (`CrewFeed.vue:25`, "All
events"), and the words of `SessionInspector.vue:259` and the palette's
"Needs input → the Events feed" (`usePaletteRows.ts:558`). The old
names `/wall` and `/carousel` redirect with `definePageMeta`
(`pages/wall.vue:3`).

**The change.**

- `pages/history/index.vue`: a view switch under the title, Search and
  Feed, in the address as `?view=feed` (Search is the default). Feed
  draws `EventFeed` and `EventsAside` as the Events page did, with the
  caption "Since this tab opened", and reads `api.integrations()` for
  the aside's Reporting list. History registers a page group in the
  palette (it has none today) with the two views as rows, Refresh
  among them while Feed is shown.
- `utils/events.ts`: `eventsRedirect(query)`: `/events` and
  `?tab=feed` to `/history?view=feed`; `?tab=routing` to
  `/history?view=feed&alerts`; `?tab=integrations` to
  `/agents#reporting`; any other tab to `/history?view=feed`.
  `pages/events.vue` becomes a redirect page calling it from
  `definePageMeta`, as `pages/wall.vue` does. The switchyard keeps
  refusing `/events` on the server (`switchyard.spec.ts:189`).
- `utils/pages.ts`: the `events` entry goes; `history` gains the view
  row "History · Feed" with the chord G E. The table's order becomes
  the order of the lists below. `layouts/default.vue`: `g-e` and
  `alt_e` push `/history?view=feed`; `useShortcuts.ts:39` reads "Go to
  History's feed".
- `CrewFeed.vue:25`: "All events" links to `/history?view=feed`.
  `SessionInspector.vue:259` and `usePaletteRows.ts:558` say "the feed
  in History".
- README (`README.md:448`, `:699`, `:784`, `:1352`; the Events page's
  part of "Events and hooks" rewritten as History's Feed),
  `docs/features.md`.

**The lists after this row**, written once, with PR-5, PR-15 and PR-17
in. They are the table's order, each list taking the entries it shows:

- The sidebar's foot (PR-15's six tiles): Needs you, Yard, Crews,
  Dispatch, Brakeman (where the server has it; else More takes the
  slot, PR-15's rule), More. More holds Agents, History, Settings (the
  desktop app only).
- The rail: Needs you (while PR-5's rule shows it), Yard, Crews,
  Dispatch, Brakeman (where the server has it), Agents, History,
  Settings (the desktop app only).
- The phone's bar: Needs you, Sessions, Yard, Crews, More. More holds
  Dispatch, Brakeman (where the server has it), Agents, History,
  Settings (the desktop app only); then the switches with "All alert
  rules"; then your menu.
- The palette's Go to rows: Needs you (G N), Yard (G Y), the Yard's
  Roundhouse view (G R), Crews (G C), Dispatch (G D), Agents (G A),
  Agents · Reporting, History (G H), History · Feed (G E), Paste
  invite, Settings (G S, the desktop app only). Brakeman keeps its own
  group (`usePaletteRows.ts:627`).

**The one test that holds them** is `web/e2e/destinations.spec.ts`,
"every list of the pages, as drawn": at 1440 x 900 the foot's tiles
and More's menu, the rail (the sidebar collapsed), the palette's Go to
rows; at 390 x 844 with touch the bar and More. Each is compared with a
literal list written in the spec, so a change to the table changes the
test in the same pull request. `sidebar.spec.ts:435` (the foot's links
in order) and `phone.spec.ts:154` (More's links) move into it; the
other checks of those two tests stay where they are.

**Tests.**

- `web/app/utils/events.test.ts` (vitest): `eventsRedirect` for no tab,
  `feed`, `routing`, `integrations` and `nonsense`. Without the change:
  no function; with `events.vue` kept, `/events?tab=routing` lands on
  a page that no longer has the tab.
- `web/app/utils/pages.test.ts` (vitest, PR-4's): no entry has the id
  `events`; G E belongs to `history`'s Feed row; every chord in the
  table is bound in `layouts/default.vue`'s chords (the test PR-4
  makes). Without the change: the `events` entry is found.
- `web/e2e/destinations.spec.ts` (Playwright): as above. Without the
  change: Events is in the foot's More, the rail, the phone's More and
  the palette, and each list fails naming its first extra entry.
- `web/e2e/history-feed.spec.ts` (Playwright, new): `events.spec.ts:
  38-53` moved. On `/history?view=feed` the feed names the crew
  members, a `needs_input` row has Answer to `/sessions/…`, the last
  hour fills to 60 bars, the aside says "Browser notification" and
  lists `[data-reporting-agent="claude"]`. The view is switched to
  Search and back to Feed, and the rows are read again after that
  redraw (the feed is kept in the tab's memory, not in the view).
  Without the change: `?view=feed` shows the search page.
- `web/e2e/history-feed.spec.ts`: `/events`, `/events?tab=feed`,
  `/events?tab=routing` and `/events?tab=integrations` each land at
  their address and show their place's own element
  (`[data-event-feed]`; the Alerts panel's `[data-routing-matrix]`;
  the `claude` row of `#reporting`). G E, pressed once after the
  layout's sidebar root is drawn (AGENTS.md: a key a page does not yet
  listen for is lost), opens History's Feed. Without the change: the
  old Events page answers each.
- `web/e2e/touched.spec.ts:52-53` and `timeline.spec.ts:82` open
  `/history?view=feed` and wait for `[data-event-feed]` in place of
  `[data-events-feed-tab]`. `shortcuts.spec.ts` gains G E going to the
  feed. `palette-rows.spec.ts:191-245`'s Events part (the tab rows and
  Refresh) is gone with the page: its tab rows are History's view rows
  now, checked in `history-feed.spec.ts`, and its Refresh moved to
  Agents in PR-27. `events.spec.ts` is deleted, every test of it moved.

**By hand.**

- History's Feed in both themes at 1440 and 390: the aside under the
  feed on the phone.

**Size.** About 380 lines of code (with about 120 of docs and 150
deleted) and 300 of tests.

**Defaults.**

- **G E and Alt+E.** Default: they open History's Feed, labelled "Go to
  History's feed".
- **The table's order.** Default: Needs you, Yard, Crews, Dispatch,
  Brakeman, Agents, History, Settings (desktop): the tiles' order, with
  today's Agents before History kept.

**Depends on.** PR-27 and PR-28 (the two tabs have left). PR-23 (the
feed's empty shape, moved with it). PR-17 (the Roundhouse leaves the
nav) and PR-15 and PR-5 (the tiles, the name, the row's rule): the
lists above are written once, after them, and each edits
`destinations.spec.ts` as it changes a list.

**Left out.** A feed kept on the server, so that History's Feed would
survive a reload: a server change this stream does not make; the view
says "Since this tab opened" instead. The Yard queue's "Answered" list
is dropped (PR-18), not moved here.

### PR-30: web: a welcome that teaches (P17; part of #374)

The first screen of a new install says what Conductor is in one
sentence, then offers three steps, the first filled: launch an agent,
with the agents this machine has and their versions; bring one in from
another machine; try a crew. Under them, "Where things are": each page
of the nav with its one line. Each choice says what will happen, on
the welcome and in the two places issue #374 names beside it: Crews'
"Load the examples" and the palette's row for it. The card shows on
the home and, on a phone, on the Sessions page when the
server has nothing; nowhere else.

**Today.** `components/WelcomeCard.vue:26-41` draws an icon, "Welcome to
Conductor", "Nothing is running yet. Start with one of these.", three
buttons (Launch agent, Load the example crews, Settings) and a line
about `conductor host` and the README. #908 stacks the three buttons
one to a row so that none is cut (D4) and adds
`web/e2e/welcome.spec.ts`; none of P17 is in it. The home shows the card
at `lg` and wider once the sessions are read and none exists
(`pages/index.vue:46`); after PR-22 nothing else does. A phone never
sees it: below `lg` the home goes to `/sessions` (`index.vue:15-43`),
which shows the sidebar's line. The agents the server can launch and
their versions are already read: `GET /api/catalog`, `isAvailable`
(`utils/agents.ts:29`), `identity.version` (shown on Agents' cards,
`pages/agents.vue:195`); #911 makes the catalog answer at once and fill
each agent in as its check answers (`checking`).

**The change.**

- `utils/agents.ts`: `welcomeAgents(list, max = 4)` returns the agents
  the server can launch now (`isAvailable`), each with its version from
  `identity` or none, and `checking` while #911's check is out; agents
  not installed are left out; at most `max`, with the count of the
  rest.
- `components/WelcomeCard.vue`, rewritten (keeping `data-welcome`):
  - The sentence: "Conductor runs your coding agents in terminals on
    this machine, and lets you watch, answer and share them from any
    browser."
  - Step 1, filled, "Launch an agent": one button a found agent, its
    name and version ("Claude Code 2.1.318"), opening the Launch dialog
    with it picked (`useLaunchModal().show({ agentId })`); "and 9 more"
    opens the dialog with none picked; while the catalog is read, the
    step says "Looking for agents on this machine…". With none found:
    "No agent is installed on this machine yet." and a link to Agents.
  - Step 2, "Bring one in from another machine": "Run `conductor host`
    there; its terminal shows here." and a link to the README's Quick
    start, where the command is shown.
  - Step 3, "Try a crew": "Load the example crews" with "Adds five
    ready-made crews to Crews; nothing starts until you launch one."
    (`loadExamples`, as today). The line is one constant,
    `EXAMPLES_LINE` in `utils/crews.ts`, which the next bullet reads
    too.
  - "Where things are": each page `navPages` lists for this window
    (PR-4's helper over `utils/pages.ts`), its label and its `about`,
    one line each, so the welcome and the nav's tooltips say the same.
  - Settings leaves the card: in the desktop app it is in the nav and
    on G S, and in a browser Settings is in no nav and G S does nothing
    (`default.vue:57-58`), so the card offered a page that is not
    there.
- `pages/sessions/index.vue`: below `lg`, once the sessions are read
  and the server has none, the page shows the card in place of the
  sidebar's line.
- The two other places that load the examples say what happens too
  (issue #374's second point): Crews' "Load the examples" in its empty
  page (`pages/crews/[[id]].vue:654`, which PR-23 draws with
  `PageEmpty`) shows `EXAMPLES_LINE` under its button
  (`data-examples-line`), and the palette's two rows that load them
  (`page:examples` in the Crews page's group, `[[id]].vue:552`, and
  `crews:examples` in `usePaletteRows.ts:698`) carry it as their
  detail.
- `docs/features.md`; the README's first-run paragraph.

**Tests.**

- `web/app/utils/agents.test.ts` (vitest, existing file, new cases):
  a catalog of an available agent with a version, one without, one not
  installed and one still checking gives the first, the second and the
  fourth (`checking`), in catalog order, without the third; six
  available with `max` 4 gives four and a rest of two. Without the
  change: no function; a welcome that listed the catalog as it is
  would offer the agent the server cannot launch.
- `web/e2e/welcome.spec.ts` (Playwright, #908's, rewritten), on an
  empty server of its own (`ownServer`): at 1440 x 900, 1024 x 768
  and 390 x 844 with touch (on `/` wide, on `/sessions` on the
  phone) the card shows the sentence, three steps and "Where things
  are"; each step's label and each agent button is `uncut` and
  `inside` the card; no sideways scroll; at 390
  each button is a 44 px target. The window is resized and the
  measures made again, as #908's test does. This keeps #908's proof
  (no label cut) and restores its phone case, dropped in PR-22. Without
  the change: three buttons and no steps; on the phone no card.
- `web/e2e/welcome.spec.ts`: step 1's agent ids equal the ids that
  `GET /api/catalog` answers as available, read from the API in the
  test (never a literal: the stubs' versions come from their identity
  probe, so they are compared with the API's answer); pressing the first
  opens the Launch dialog with that agent picked. Without the change: no
  agent buttons.
- `web/e2e/welcome.spec.ts`: "Where things are" lists the same labels
  and lines, in the same order, as `navPages` gives for this window,
  the lines being the nav's tooltips (`pageTip`) read from the page.
  Without the change: no list; if one copy is edited by hand, the two
  differ.
- `web/e2e/welcome.spec.ts`: #908's check of the token dialog's words
  is kept in this rewrite; PR-31, cut after this row, moves it.
- `web/e2e/welcome.spec.ts`: "Load the example crews" lands on Crews
  listing `example-todo-app` (the id `examples.spec.ts` reads). The
  spec's server is one of its own, so the crews it adds reach no other
  spec. Before the load, on that empty server, Crews' empty page shows
  `[data-examples-line]` with the welcome's own words under "Load the
  examples", and the palette with "example" typed lists the row with
  the same words as its detail. Without the change: no line, and the
  row's detail is empty.
- `web/e2e/home-wide.spec.ts:38-42`: the card's buttons become the
  steps (Launch agent's first agent button, Load the example crews);
  the Settings link check goes, as Settings left the card.

**By hand.**

- The sentence and the steps, read once in both themes: they are
  product copy, and no test judges them.

**Size.** About 255 lines of code and 280 of tests.

**Defaults.**

- **Agents on the welcome.** Default: at most four found, with "and N
  more", agents not installed left out.
- **The welcome on a phone.** Default: on the phone's Sessions page when
  the server has no session at all, once read.
- **"Where things are".** Default: every page the nav shows, not the
  review's six.

**Depends on.** PR-4 (`utils/pages.ts`, `navPages`). PR-22 (the welcome
on the home only; the empty server of its spec). PR-23 (Crews' empty
page in `PageEmpty`, under which the examples' line is drawn), and so
#899 through it, which also holds `pages/crews/[[id]].vue` and
`utils/crews.ts`. It builds on #908 (merged: `WelcomeCard.vue`,
`welcome.spec.ts`). #911 merged (the catalog's quick answer, or the step
waits on the slow read of #704).

**Left out.** #374's first point (no label cut on the welcome card) is
on `main` (#908), and its third (a tooltip on every icon-only button,
written into the design rules) is PR-37's rule with its check, which
says `Fixes #374`; this row says `Part of #374`.

### PR-31: web: a page to connect (P18)

A browser that holds no workbench token, or whose token the server
refused, sees a page of its own: the mark, one sentence, one field and
Connect, and "Where to find it", which says what is true. There is no
Cancel: the page leaves when a token is accepted. The app under it
keeps whatever it held. The dialog stays, with Cancel, only for the
menu's Workbench token…, where the app works. The desktop app, which
gets its token from its shell, never shows the page.

**Today.** `components/WorkbenchTokenGate.vue` is a `UModal` over the
dimmed app, with Cancel (`:25`) and Save, mounted by the layout
(`layouts/default.vue:371`). It opens three ways: the store's ask
(`needsToken`, set by a page that finds no token, as `pages/
index.vue:49` does, and by any 401 for the held token, `answered` in
`composables/useWorkbenchToken.ts:145-157`); the menu's Workbench
token… (`SidebarHeaderMenus.vue` → `default.vue:331`, `BottomBar.vue`,
the palette); and Set token on the home's alert (`index.vue:66`).
`followAsk` (`utils/workbenchGate.ts:16-28`) ties the dialog to the
ask: closed, the ask ends, so the next refusal asks again (#894). Its
words say the token was "printed at server start"
(`WorkbenchTokenGate.vue:18`); the server prints only the file's path
(`internal/cli/serve.go:315`, the file `workbench-token` in the data
directory, `internal/cli/tokenfile.go:22`). #908 corrects the words and
keeps the dialog. In the desktop app the store starts empty until the
shell's bridge answers (`useWorkbenchToken.ts:51-62`).

**The change.**

- `utils/workbenchGate.ts`: `connectShown({ desktop, hasToken, standing
  })`: never in the desktop app; shown when no token is held, or when
  the held one was refused (`standing === 'refused'`); not while a
  link's token is being tried (`'trying'`).
- `components/ConnectPage.vue` (new): the mark (both theme variants,
  as `default.vue:317-318`), "Connect to Conductor", "This server asks
  for its workbench token before it shows anything.", a password field
  "Workbench token" starting from the token this browser keeps (as the
  dialog's draft does, `followAsk`), and Connect. When the kept token
  was refused: "The server refused the token this browser keeps. Paste
  the one it uses now." "Where to find it": "If you set
  `CONDUCTOR_WORKBENCH_TOKEN`, or `workbenchToken` in the config, it is
  that. If not, the server made one as it started and keeps it in the
  file `workbench-token` in its data directory; the terminal it started
  in printed that file's path. With the default data directory: `cat
  ~/.conductor/workbench-token`." The page never shows the real path:
  that would tell anyone who reaches the server where its data
  directory is, before they sign in.
- `layouts/default.vue`: while `connectShown`, the page is drawn and
  the app (`UDashboardGroup`) is hidden with `v-show`, not unmounted,
  so a refusal in the middle of work keeps an open editor's draft and
  a page's state; the page is a landmark of its own.
- `WorkbenchTokenGate.vue`: opened by the menu and the palette only;
  it no longer follows the ask, so `followAsk`'s "a close ends the
  ask" holds for this dialog alone; Cancel stays, and leaves the app as
  it was. Its words are #908's. Set token on the alerts of the home
  and the Sessions page (`pages/index.vue:66`, `pages/sessions/
  index.vue:48`) opens this dialog (the layout's `showToken`, shared
  through a small `useTokenDialog()`), where today it raises the ask:
  with a token held, the ask alone would open nothing now.
- `web/e2e/fixtures.ts`: `tokenAsk(page)`, the locator of the page
  that asks (`[data-connect-page]`), so the specs that meet the ask
  change one line each.
- README (the Quick start's "paste the workbench token when
  prompted"), `docs/features.md`.

**Tests.**

- `web/app/utils/workbenchGate.test.ts` (vitest, existing file, new
  cases): desktop with no token yet is not shown; no token is shown; a
  link's token being tried is not shown; refused is shown; a held,
  accepted token is not shown. Without the change: no function; and
  the case "desktop, no token yet" is the one that fails a rule keyed
  on the token alone, which would flash the page in the desktop app
  before its bridge answers.
- `web/e2e/connect.spec.ts` (Playwright, new): a context with no token
  (`dayPage` with `token: false, ready: '[data-connect-page]'`; the
  theme set by the app's own key) at 1440 x 900 and at 390 x 844 with
  touch, in both themes: the page is drawn, the sidebar (at 1440) and
  the phone's bar (at 390) are hidden, and no dialog is open; its words
  name `workbench-token` and `CONDUCTOR_WORKBENCH_TOKEN` and not
  "printed at server start"; the mark shown is the one for the theme
  (the other variant hidden); there is no Cancel. Connect with the
  server's token shows the app, and the home's reads go out (the test
  waits for `GET /api/sessions` with the token). Without the change: a
  dialog over the app, with Cancel.
- `web/e2e/connect.spec.ts`: a crew page with an unsaved edit (its
  `[data-unsaved-bar]` shown), then a refusal (the `page.route` of
  `token-fragment.spec.ts:541-548` answering 401): the page is shown,
  the field starting from the kept token; Connect with the right token
  shows the crew page with the edit and its bar still there. On a
  session page the same refusal leaves the terminal's size as it was
  (cols and rows read from `GET /api/sessions/<id>` before and after),
  so the hidden app was never fitted to nothing. Without the change:
  the dialog. With the app unmounted in place of hidden, the edit is
  gone and the first check fails.
- `web/e2e/connect.spec.ts`: the menu's Workbench token… opens the
  dialog over the working app, with Cancel, and Cancel leaves it as it
  was.
- `web/e2e/token-fragment.spec.ts`: its 18 uses of the dialog named
  "Workbench token" become `tokenAsk(page)` where the ask opened it,
  and stay the dialog where the menu did (`:40-41`). The two tests that
  cancel the gate (`:535-563`, `:565-646`) become "the page stays until
  a token is accepted; the next refusal shows it again" and "across a
  page of another layout and back, the page is shown again with no new
  refusal", their recorder (`:592-605`) keyed on `[data-connect-page]`.
  The desktop tests (`:814`, `:846`) assert the page never shows and the
  dialog, opened from the menu, starts empty. Nothing they proved is
  lost: Cancel's ending of the ask is still proved, for the dialog, in
  `connect.spec.ts`.
- `history-page.spec.ts:176-182`, `history-record.spec.ts:201-204`,
  `brakeman-act.spec.ts:506-509`, `brakeman-cards.spec.ts:397` and
  `decide-only.spec.ts:437` change one line each to `tokenAsk(page)`.
  #908's check in `welcome.spec.ts` of the dialog's words, which
  PR-30's rewrite kept, moves into `connect.spec.ts`.
- `decisions-ask.spec.ts` (from #912): its ask becomes `tokenAsk(page)`,
  and the decide pass's "asks for nothing" asserts `[data-connect-page]`
  count 0 beside the dialog's.

**By hand.**

- The page in both themes at 1440 and 390: the mark's size and the
  field's place.

**Size.** About 200 lines of code and 320 of tests.

**Defaults.**

- **The app under the connect page.** Default: hidden with `v-show`,
  never unmounted, so a refusal in the middle of work keeps drafts; a
  test holds a crew's unsaved edit and a terminal's size.
- **Set token on the alerts.** Default: on the home's and the Sessions
  page's alerts it opens the dialog directly, through a shared
  `useTokenDialog()`. Why: the ask alone would open nothing with a
  token held.

**Depends on.** It builds on #908 (merged: the dialog's words,
`default.vue`). #912 merged (it adds the decision page's ask); #899
merged (it changes `web/e2e/fixtures.ts`). PR-1 (`dayPage` with `token:
false` and `ready`). PR-30 (it rewrites `welcome.spec.ts`, whose dialog
check this row moves, so this row is cut after it).

**Left out.** The server showing the token file's real path: not
proposed (see above). The bare-layout pages (join, paste, decide)
mount neither the page nor the dialog and are not changed.

### PR-32: web: a run's tasks laid out as lines (P15)

Nothing on screen changes. `web/app/utils/lineDiagram.ts` lays a run's
tasks out as lines and stations, as a pure function with a table of
cases and a property test over random graphs. PR-33 and PR-34 draw what
it returns and test only what is drawn.

**Today.** The Graph view's Tasks mode places tasks with `taskLayout`
(`utils/taskGraph.ts:61-81`): a column per depth (`taskDepths`,
`:28-58`, one past the deepest need) and a row per task in the run's
order within its column. Nothing puts a task beside the task it
needs, nothing knows a chain, and nothing keeps crossings down. A
node is 240 x 96 px with gaps of 96 and 24
(`utils/crewGraph.ts:14-17`), so a column is 336 px and eight of them
are about 2,700 px, which Vue Flow's fit can shrink only to its floor
of 0.4 (`CrewGraph.vue:309`). `CrewMiniDag.vue` lays members out a
second way, by `crewGraph.layout`.

**The change.** `web/app/utils/lineDiagram.ts` (new) exports
`lineLayout(nodes, opts)`. The algorithm, for a reviewer to check:

- Input: `nodes`, in the run's order, each `{ id, needs? }` (a
  `RunTask`, a crew's task shape, or a member as PR-34 gives it), and
  `opts`: `direction` (`LR` or `TB`, `LR` by default) and `size`
  (`full` or `mini`). The geometry's constants are fixed per size and
  direction: `full`, `LR`: a step of 72 px between depths, 48 px between
  lanes, a margin of 24 px, a station's radius 6; `full`, `TB`: 48 px
  between depths, 152 px between lanes; `mini`: 16 px, 10 px, a margin
  of 4, a radius of 3 and no labels. Every coordinate is on the 4 px
  grid.
- Output: `stations` (`{ id, depth, lane, x, y, line, label, title,
  junction?, warning? }`), `lines` (`{ id, stations, lane, from? }`,
  the stations in depth order and the station a branch leaves from),
  `connectors` (`{ from, to, kind: 'branch' | 'merge', points }`),
  `width`, `height` and `warnings`. It never reads a state: the same
  ids and needs give the same layout, so a task that changes state is
  recoloured and never moved.
- Step 1, depth: `taskDepths` as it is. A task that needs nothing in the
  run is at depth 0, any other one past its deepest need. A need the run
  has no task for, and a cycle, give the task its warning, and it is
  drawn at depth 0 with nothing into it. A need that does not go forward
  in depth (only in a cycle, which the server refuses) is not drawn and
  is named in `warnings` for its task.
- Step 2, lines: the tasks are visited by depth, then in the run's
  order. A task with no drawn need starts a new line. Otherwise its
  candidates are its needs whose line ends at them so far; it continues
  the line of the deepest candidate (ties: the first in its `needs`),
  so a line steps one depth where it can. With no candidate it starts a
  new line and its deepest need is the line's `from`: a `branch`
  connector from that need to it. Each other need is a `merge`
  connector into it. A station that two or more tasks need is a `split`
  junction (the first of them in visit order keeps its line, the others
  branch); a station that needs two or more is a `merge` junction; one
  can be both.
- Step 3, lanes: a line's span runs from its `from` station's depth (its
  first station's for a line with no `from`) to the greater of its last
  station's depth and, for each `merge` leaving one of its stations, the
  merge station's depth less one, since that connector runs along this
  line's lane first. Lines are placed in order of where their spans
  start, then in the order they were made. A lane is free for a line
  when every line already in it ends before the new one starts. A line
  takes the free lane nearest its `from` station's lane, the one below
  first on a tie; a root line takes the free lane with the smallest
  index; a new lane is added only when no lane is free. Placed in that
  order, a lane is free exactly when all its lines have ended, so a lane
  is added only when every lane holds a line alive at that depth: the
  lanes number exactly the most lines alive at one depth.
- Step 4, geometry, `LR`: `x = margin + depth × step`, `y = margin +
  lane × laneGap`; `TB` swaps the two axes with its own gaps. A line is
  straight between its stations, which share its lane. A `branch`
  leaves its `from` station on a diagonal and reaches its lane after
  `min(step, |lanes apart| × laneGap)` along the run: 45 degrees when
  that fits in one step, steeper when it does not, never past one step.
  It then runs straight to the line's first station. A `merge` runs
  straight along its source's lane and turns into the merge station on
  the same diagonal in the last step before it. A diagonal lies
  between two depths, and a lane is held by one line over its span, so
  no line or connector passes through a station that is not its own.
- Step 5, labels: a station's label is its id cut to 16 characters
  with "…", its whole id in `title`. In `LR` the sides alternate along a
  line, above first, each box 6.6 px a character (JetBrains Mono at
  11 px) by 12 px, 10 px off the centre. In station order, a box that
  meets a box already placed or another station's circle takes the other
  side; if both meet one, it is cut to 8 characters and tried again; if
  it still meets one it is hidden (`side: 'none'`; the id stays in
  `title` and in the station's `aria-label`). In `TB` a label sits to
  the right, cut to fit 128 px.
- Bounds: `width` is two margins, the deepest depth times the step and
  half the widest label; `height` is two margins, the lanes less one
  times the gap, and the labels' room above the first lane and below
  the last. Forty tasks of depth 8 on six lanes (the fixture below)
  come to about 680 x 330 px, inside the Graph view's pane at 1440 x 900
  (about 1,216 x 680 px with a 200 px sidebar) and at 1280 x 720. A
  chain of 128 tasks, the crew limit (`internal/crew/crew.go:236`), is
  about 9,200 px wide; PR-33 scales it into the pane down to a floor and
  scrolls it inside the pane.
- Cost: depth and lines are linear in tasks and needs; lanes are
  quadratic in lines, at most 128.
- `web/app/utils/testdata/line-forty.json` (new): one forty-task shape
  (`{ id, needs }` in run order: a plan that splits into five chains of
  seven tasks, which merge into a release, and three reviews off the
  chains at three different depths: depth 8, six lanes),
  read by this row's test and by PR-33's fixture, so the unit bound and
  the drawn bound measure the same run.

**Tests.**

- `web/app/utils/lineDiagram.test.ts` (vitest, new), a table of cases,
  each written as `{ id: [depth, lane] }` with its lines, connectors as
  `from>to:kind` and junctions, so no pixel is compared:
  - a chain of four: one line on lane 0, depths 0 to 3, no connector;
  - a split (b and c need a): a and b on lane 0, c on lane 1,
    `a>c:branch`, a split at a;
  - the example crew's diamond (`example-todo-app-tasks`: core and cli
    need plan, tests needs core and cli, review needs tests): plan,
    core, tests and review on lane 0, cli on lane 1, `plan>cli:branch`,
    `cli>tests:merge`, a split at plan and a merge at tests;
  - the Ledger shape of `run-fixtures.ts`: schema, naming and migrate on
    lane 0, soak on lane 1, release-notes on lane 1 again (soak ended at
    depth 0, so the lane is free at depth 1), backfill on lane 2;
  - two independent chains on two lanes;
  - lane reuse: two short branches at depths that do not meet share
    lane 1; it fails if lanes are never reused (the second goes to 2);
  - nearest lane: a branch from lane 2 with lanes 1 and 3 free takes 3;
    it fails with "the lowest free lane";
  - a long need: a task needing one at depth 0 whose line is free and
    one at depth 2 whose line was taken continues the first line and
    takes a merge from the second;
  - a need the run has no task for, and a two-task cycle: roots, no
    line step or connector between them, and the warnings `taskDepths`
    gives, worded as today;
  - `TB`: every station's x and y swapped against `LR` under its own
    gaps, labels to the right;
  - `mini`: no labels, and a five-task crew of depth 3 inside 150 px.
- `lineDiagram.test.ts`, properties over 200 graphs of 1 to 128 nodes
  made by a small seeded generator written in the test (no new
  dependency; a failing seed is printed with its graph): every drawn
  need goes forward in depth; no two stations share a depth and a lane;
  every drawn need is either two stations next to each other on one line
  or exactly one connector; each station is on one line, whose stations
  share its lane and rise in depth; no two lines in a lane have spans
  that meet; the lanes number the most lines alive at one depth; no
  line or connector passes within 8 px of a station that is not its
  own; no two shown labels meet, nor a label and a foreign station; the
  same input twice gives the same output. Each fails with the step it
  names taken out: lanes by run order break the lane count, a line that
  ignores spans breaks "no two lines meet", labels with no flip break
  the label property.
- `lineDiagram.test.ts`, bounds: `line-forty.json` fits 960 x 400 at
  `full`, and today's `taskLayout` over the same shape is printed beside
  it for the reader (about 2,700 px wide); a chain of 128 is drawn with
  one lane.

**Size.** About 280 lines of code and 430 of tests.

**Defaults.**

- **The lane rules.** Default: lines placed by where their spans start,
  each taking the free lane nearest its parent's, below first, a new
  lane only when none is free; labels cut at 16 characters,
  alternating above and below; `TB` labels to the right. Why: the lane
  count is then provably the most lines alive at one depth, which a
  test can hold.

**Depends on.** None. It imports `taskDepths` from
`utils/taskGraph.ts` and changes nothing there.

**Left out.** The drawing (PR-33) and the small drawing (PR-34). Pan and
zoom. Keeping crossings down past the nearest-lane rule. Colour: the
layout knows no state.

### PR-33: web: a run drawn as a line diagram (P15, issue 449)

After this row the Graph view's Tasks mode draws a run as lines: a task
is a station, a chain of tasks is a line, and a line splits where other
tasks wait on a result. A station carries its task's mark from PR-10,
so the run's position is the stations that are lit; every line is the
same neutral ink. Forty tasks fit the window with their names. Members
mode and the crew editor keep Vue Flow, each graph now on a store of its
own (issue 449). A phone keeps today's list.

**Today.** `CrewGraph.vue:113-123` hands Vue Flow one node a task at
`taskLayout`'s place, drawn by `CrewGraphTaskNode.vue` (`h-24 w-60`:
kind, id, dot, a "failed" or "done" badge, member, attempt, words and
an Open button at `:59`), and a dotted `needs` edge per need
(`CrewGraphEdge.vue`). A click selects (`:207-210`), a double click or
Enter opens (`:211-214`, `CrewGraphTaskNode.vue:41`): the attempt's
session, else the task in the Tasks view (`[run].vue:266-270`). The
redraw bug AGENTS.md records (Vue Flow dropped a needs edge at every
redraw after the first) is held by `connectionAllowed`
(`crewGraph.ts:221-230`), `crewGraph.test.ts:160` and
`tasks.spec.ts:143-146`. Every graph's store is `crew-graph-1`, because
`let seq = 0` sits inside `<script setup>` (`CrewGraph.vue:86-89`,
issue 449). Under `md` the Tasks mode is an indented list (`:278-297`).

**The change.**

- `web/app/components/RunLineDiagram.vue` (new): an SVG over
  `lineLayout`, as `RunTimeline.vue` is over `utils/timeline.ts`. It is
  laid out from the tasks' ids and needs alone (a computed keyed on
  them), so the page's 5 s tick and a state change recolour stations
  and never move them.
- A station is `<g role="button" tabindex="0" data-station="<id>"
  data-task-state="<state>" data-status-mark="<key>" data-lane="<n>">`
  with `aria-label` "<id>: <words>" (the words `taskWords` gives, as
  the node's today) and a `<title>` of the same; its circle is the mark
  of `StatusMark`'s `svg` form (new in this row: the same marks of
  `STATUS_LOOK`, drawn as circles and rings in SVG, so a station is the
  same mark and not a sixth). PR-3 shaped the component for this
  second form: both read the one table, and no second table is made. A
  split or merge station also carries `data-junction`; it is a plain
  circle like every other, and nothing borrows the mark's
  parallelogram (AGENTS.md). The selected station has a ring in the
  primary colour; a focused one a visible focus ring.
- A line is `<path data-line="<id>" data-line-stations="<ids>">`; a
  connector `<path data-connector="<from>-<to>"
  data-connector-kind="branch|merge">`; both in one neutral ink
  (`--ui-text-muted`), 2 px, with no arrowhead: a line reads left to
  right, or top to bottom.
- Keys and pointer: a click or Space selects (`selectTask`); Enter or a
  double click opens (`openTask`), as the nodes do. Left and Right move
  the focus along a line, Up and Down to the nearest station on the
  lane above or below, by `stepFrom(layout, id, direction)` (new in
  `utils/lineDiagram.ts`). Tab walks the stations by depth, then lane.
- Under the diagram, for the selected station, one caption line
  (`[data-station-caption]`): the id, the member, the row's words and
  attempt (`taskRow` of PR-11), "Open" (`[data-station-open]`) when its
  attempt has a session, and "Show in Tasks" (`[data-station-show]`).
  It takes the place of the node's Open button, for touch and mouse.
- `CrewGraph.vue`: in Tasks mode, on `md` and up, the pane (which keeps
  `[data-graph-canvas]`, so the specs that scope to it still do) holds
  `RunLineDiagram`; `VueFlow` is mounted for Members mode and the editor
  only. Fit scales the drawing to the pane (never above 1, never below
  0.5; past that the pane scrolls inside itself and the page never
  does); Direction swaps `LR` and `TB` through the layout, sharing the
  graph's saved preference with Members mode. The Tasks legend lists the
  task marks and "a line: tasks that follow one another", "a branch: a
  task that starts from a result another line goes on from", "a join: a
  task that needs more than one line's result".
- Issue 449: `let seq = 0` moves to a plain `<script lang="ts">` block,
  so each graph's store is `crew-graph-<n>` with `n` counting up, and
  the Vue Flow pane carries `data-graph-store="<id>"`.
- Deleted: `CrewGraphTaskNode.vue`; `taskLayout` and the `x`, `y` and
  `row` of `TaskNode` in `utils/taskGraph.ts`. `taskDepths` stays, and
  `taskListRows(tasks)` (by depth, then the run's order) keeps the
  phone's list.
- `web/e2e/run-fixtures.ts` gains `fortyRun()` (`line-forty.json`, with
  states spread over the five marks) and `chainRun(128)`.
- Docs: `docs/features.md` (the Graph view's entry, `:2958-2966`);
  `docs/design/crews-graph-brief.md` (Tasks mode is the line diagram;
  Vue Flow draws members). `docs/dependencies.md:27` is unchanged: Vue
  Flow stays for Members mode and the editor.

**Tests.**

- `web/app/utils/lineDiagram.test.ts` (vitest): `stepFrom` on the Ledger
  layout: Right from schema is naming, Right from migrate stays where it
  is, Down from schema is soak, and Up from backfill is soak (soak and
  release-notes are as near on lane 1; the lower depth wins). Without
  the change the function does not exist.
- `web/app/utils/taskGraph.test.ts` (vitest): the `taskLayout` cases
  (`:29-50`) go, their proofs held by PR-32's table (depth columns,
  `TB`, nothing into a warned task); `:51` ("lists a task after the
  tasks it
  needs") moves onto `taskListRows(tasks)`.
- `web/app/utils/crewGraph.test.ts` (vitest): the `connectionAllowed`
  cases (`:138-178`) build their needs edge by hand instead of from
  `taskLayout`, and the Vue Flow store case (`:160`) runs on a handoff
  edge (`tests` to `lead`, which the start rules refuse and the graph
  draws), so the redraw guard keeps its proof where Vue Flow still runs.
- `web/e2e/line-diagram.spec.ts` (Playwright, new; stubs of
  `run-fixtures.ts` found by id, `?view=graph`):
  - at 1440 x 900, the Ledger run: six `[data-station]`, each with its
    task's `data-task-state`; `migrate` and `release-notes` with
    `data-status-mark="needs"`, `soak` with `working`; the lines'
    `data-line-stations` as the spec's own table says ("schema naming
    migrate", "soak", "release-notes", "backfill"), splits at schema and
    naming. Without the change there is no `[data-station]`.
  - the forty-task run: forty stations, all `inside` the pane, the pane
    not scrolling (its `scrollWidth` and `scrollHeight` within its
    client box), `noOverlap` over the station circles and over the shown
    labels; the same at 1280 x 720. Without the change: Vue Flow at 0.4,
    labels cut to nothing.
  - the 128-task chain: 128 stations, the pane scrolls sideways inside
    itself and `noSideways` holds for the page.
  - a click on `naming` writes `?task=naming` (waited on the URL) and
    shows its caption; "Show in Tasks" opens the Tasks view on it; Enter
    on `soak`, focused by `tabTo`, goes to its attempt's session page;
    ArrowRight from schema moves the focus to naming.
  - after a redraw the test causes, the first case again: Members and
    back to Tasks, then Direction to `TB` (each station's lane order
    now runs across) and back; then the station boxes are read, the
    spec polls until the page's 5 s tick has changed the uptime's text,
    and the boxes are the same (AGENTS.md: check again after a redraw;
    the Vue Flow edge bug it records failed only when the tick came
    first).
  - in light and dark (`dayPage` with `theme`): a `needs` station's
    fill equals the resolved `--ui-warning`, and every line's and
    connector's stroke the resolved `--ui-text-muted`. The pair "muted
    text on the elevated pane at 3:1" is one of the pairs PR-24's
    `themeContrast.test.ts` holds.
  - at 390 x 844 with touch: `[data-graph-task-list]` is shown and no
    `[data-station]`.
  - Members mode: `[data-graph-node]` per member, drawn by Vue Flow.
- `web/e2e/graph.spec.ts` (Playwright): two graphs, two stores. The
  crew editor's graph is read for `data-graph-store`, then the page goes
  to the crew's run by its link in the Runs tab (a client-side change,
  no reload) with the Graph view on Members, and the run's graph has
  another id. Without the fix both read `crew-graph-1`. The rest of the
  file is Members mode and stands.
- Moved: `tasks.spec.ts:133-146` counts `[data-station]` (2), reads
  docs' `data-task-state`, and in place of the needs edge expects one
  `[data-line-stations="build docs"]`, before and after the switch to
  Members and back (the #281 check, kept). `decisions-restart.spec.ts:
  108-112` reads `[data-graph-canvas] [data-station="build"]` for the
  same `aria-label`, before and after the same switch.
- Lost: the dotted needs edge in Vue Flow and its redraw check, for
  tasks, which no longer go through Vue Flow; the check after a redraw
  now holds the line. The node's Open button gives way to the caption's,
  proved above.

**By hand.**

- Both themes at 1440 and 1280: whether the diagram reads as
  Conductor's own; the diagonals at the junctions; labels at forty
  tasks; the focus ring on a station.
- `TB` on a tall run.

**Size.** About 360 lines of code (with about 110 deleted) and 460 of
tests.

**Defaults.**

- **Open on a station.** Default: no button on a station; a caption
  under the diagram for the selected station carries Open and Show in
  Tasks. Why: touch and mouse keep an Open without a box a task.
- **A layout that holds still.** Default: it depends on ids and needs
  only; a state change recolours a station and never moves it. Why: the
  5 s tick and live updates must not shuffle the drawing.
- **The scale.** Default: the drawing fits the pane between 0.5 and 1,
  and past that the pane scrolls inside itself. Why: a 128-task chain
  is about 9,200 px wide and must stay legible.
- **Issue #449.** Default: fixed whole here (`Fixes #449`): each Vue
  Flow graph gets its own store, with the issue's two-graphs test. Why:
  `CrewGraph.vue` is open in this row anyway. Alternative: fix only the
  Tasks mode and leave #449 open.

**Depends on.** PR-32 (`lineLayout`). PR-11 (`taskRow` for the caption,
and `run-fixtures.ts` with the Ledger run). PR-10 (`taskStatus` and
`StatusMark`, which this row gives an `svg` form). PR-1 (`dayPage`,
`inside`, `noOverlap`, `tabTo`). Open PR 912 merged: it edits
`CrewGraph.vue`'s Members mode (a `managed` prop to the member nodes
and `CrewGraphList`), beside the Tasks mode this row replaces.

**Left out.** Members mode and the crew editor (Vue Flow, owner
decision 13). The phone's diagram (it keeps the list, owner decision
13). A colour per line (one ink, owner decision 13). Pan and zoom. The
small drawing (PR-34).

### PR-34: web, crew: the small line drawing in the Crews table and on a run's card (P15)

After this row the Crews table's Shape column draws each crew as lines:
a crew with tasks by its tasks, any other by its members' start rules.
A live run's card on Crews draws the same lines with each station lit by
its task's or member's state. The stack of chips goes. The Shape
column's words ("4 · by tasks"), the card's hidden member lines and
every action stay as they were.

**Today.** `CrewsTable.vue:63` draws `CrewMiniDag` over a crew's members
by their start rules (`crewGraph.layout`) as 32 x 14 initials chips.
A crew with tasks starts every member `{ when: 'task' }`, so each is a
root and the drawing is one column of chips, with the words "4 · by
tasks" (`utils/crewWords.ts:83`). The table cannot draw tasks: a summary
of `GET /api/crews` carries members only (`internal/crew/store.go:
106-138`, `docs/protocol.md:2921`, `CrewSummary` at
`composables/useSessions.ts:510-521`). `CrewRunCard.vue:66` draws the
same component as 96 x 24 name chips with a status dot, from members
alone, though `GET /api/runs` carries each run's tasks. `conductor up`
reads the crew list in pages of 100, each at most 4 MiB
(`internal/cli/up.go:29-33`, `:185`), a bound sized on summaries of
about 26 KB.

**The change.**

- `internal/crew/store.go`: `TaskShape { ID, Member, Kind string; Needs
  []string }` (`needs` omitted when empty) and `Summary.Tasks
  []TaskShape` (`tasks`, omitted for a crew without), filled by
  `summary()` in the crew's order, never with instructions, checks or
  an owner. It is cached with the summary, so nothing new is read.
- `internal/api/crews.go`: the list carries `tasks` only when asked
  with `?tasks=1`; any other value answers `400 invalid_request` "tasks
  must be 1". A task crew's shape is bounded by its file's bound
  (`crew.MaxEncoded`, 1 MiB, `crew.go:261`: at most 128 tasks of ids of
  at most 40 characters, `crew.go:236`, `:250`), far past the 26 KB
  `conductor up` counts on; asked for, it reaches only the app's read.
- `docs/protocol.md:2921`: the row says `?tasks=1`, `tasks?: [{id,
  member, kind, needs?}]` and its bound, and names `goal`, which the
  reply carries and the row leaves out.
- `composables/useSessions.ts`: `CrewSummary.tasks?`, and
  `listCrews(offset, limit, { tasks })` (`:912-913`); the Crews page's
  read asks for the shape, the palette's does not. `utils/crews.ts`:
  `summaryOf` (`:1071-1072`) copies the shape, so a crew saved here
  draws what the server's summary would.
- `utils/lineDiagram.ts`: `memberNodes(members)`: a member that starts
  after another needs it; one started by hand is marked `manual`.
- `RunLineDiagram.vue` gains two sizes: `mini` (PR-32's `mini`
  geometry, no labels, scaled into `maxWidth`) and `card` (the `full`
  geometry with labels, scaled into `maxWidth`), with states optional.
- `CrewsTable.vue:63`: `RunLineDiagram` at `mini`, 150 px at most
  (`[data-crew-shape-drawing]`), over `c.tasks` when the crew has any,
  else `memberNodes(c.members)`; stations hollow, a manual one dashed.
  `[data-crew-shape]` keeps its words.
- `CrewRunCard.vue:66`: `RunLineDiagram` at `card`, 520 px at most: a
  run with tasks draws its tasks lit by `taskStatus` with each attempt's
  session; a run without draws its members by `memberStatusOf`. The
  hidden `[data-run-member]` lines (`:67`) stay.
- Deleted: `CrewMiniDag.vue`.
- Docs: `docs/features.md` (the Crews page's entry) and
  `docs/design/crews-graph-brief.md`.

**Tests.**

- `internal/crew/store_test.go` (Go race test, new
  `TestStoreSummaryCarriesTaskShape`, beside
  `TestStoreRoundTripsPeople`): a crew of three tasks lists `tasks` as
  `[{build, builder, work}, {docs, writer, work, [build]}, {review,
  reviewer, review, [build]}]`; its JSON
  holds no `instructions`, no `checks` and none of the instructions'
  words; a crew without tasks has no `tasks` key; a save that changes
  docs' needs lists the new needs at once (the cache dropped by the
  commit). Without the change it does not compile.
- `internal/api/crews_test.go` (Go race test, new
  `TestCrewsListTaskShapeWhenAsked`): `GET /api/crews` has no `tasks`
  key in any summary; `?tasks=1` has the shape; `?tasks=2` answers 400
  `invalid_request`. `internal/cli/up_test.go:397`, which holds
  `conductor up`'s query to `offset=0&limit=100`, stands, so the CLI's
  pages carry no shape.
- `web/app/utils/crews.test.ts` (vitest): `summaryOf` (`:398`) copies
  each task's id, member, kind and needs and leaves the rest out.
- `web/app/utils/lineDiagram.test.ts` (vitest): `memberNodes` over the
  todo app's members gives the tree's nodes with one split at lead; a
  member by hand is `manual`; a member after one not in the crew is a
  root.
- `web/e2e/examples.spec.ts` (Playwright), once the examples are loaded
  and each entry is found by its crew id: `example-todo-app-tasks`'
  `[data-crew-shape-drawing]` holds five `[data-station]`, its lines
  "plan core tests review" and "cli", a split at plan and a merge at
  tests, `inside` its cell at 1440 x 900; `example-todo-app`'s holds its
  four members with a split at lead. The words at `:29` and `:114`
  stand. The live run card of `:108-121` draws one station per member,
  their ids the spec's `MEMBERS`. Without the change: no drawing, a
  stack of chips.
- `web/e2e/crews.spec.ts` (Playwright): with the Ledger stub of
  `run-fixtures.ts` served as live, its card under "Running now" draws
  six stations, `migrate` and `release-notes` with
  `data-status-mark="needs"`; in light and dark the `needs` station's
  fill equals the resolved `--ui-warning`; at 390 x 844 with touch the
  card's drawing is `inside` the card and `noSideways` holds.
  `crews.spec.ts:49` (the words) and `:87` and `:106` (the hidden
  member lines) stand.
- No proof is lost: `CrewMiniDag` had no test.

**By hand.**

- The `mini` drawing in a 150 px cell, and the card at 520 px with
  twelve members, in both themes.

**Size.** About 170 lines of code (with about 95 deleted) and 230 of
tests, Go and app together.

**Defaults.**

- **The crew list's task shape.** Default: `GET /api/crews` carries it
  only when asked with `?tasks=1` (other values 400). Why: `conductor
  up` reads pages of 100 crews of at most 4 MiB each, sized on 26 KB
  summaries, while a task crew's shape is bounded only by its 1 MiB
  file. Alternative: always send it and raise the CLI's bound.
- **A crew without tasks.** Default: its members' start rules are drawn
  as lines too, and `CrewMiniDag` goes. Why: one drawing everywhere.
- **A run's card.** Default: the full geometry with labels, scaled into
  520 px. Why: the card has room and shows names today.

**Depends on.** PR-33 (`RunLineDiagram.vue`). PR-10 (`taskStatus`,
`memberStatusOf`). PR-1 (`dayPage`, `inside`). Open PR 899 merged: it
edits `pages/crews/[[id]].vue`, `crews.spec.ts` and `examples.spec.ts`.

**Left out.** The Shape column's words. The run bars of `CrewRunBars`.
The crew editor's graph, which stays Vue Flow.

### PR-35: api, session: a launch's first message (P19, D11)

`POST /api/sessions` takes an optional `message`: the server keeps it
in memory, types it into the agent once the session is ready by the
rule a crew's prompt waits on, and holds it behind every startup
question. A session launched with a message and no name is named by the
message's first words. A ticket's launch with no label of its own gets
a label that ends "…" when the title is cut. Nothing changes for a
launch without a message, for a crew, for Brakeman, for a resume or a
clone.

**Today.** `createSessionRequest` (`internal/api/sessions.go:31-105`)
is decoded with unknown fields refused (`decodeJSON`, `errors.go:47`,
64 KiB), so a `message` today is a 400. `createLocalSession` names a
nameless launch `"<agent> #N"` (`sessions.go:220-223`, the counter at
`:601`). A crew types a member's prompt in `promptWith`
(`internal/crew/run.go:1626`): `awaitReady` (`:2683`, the bounds at
`:105-111`) is ready when the agent reports `needs_input` or `done`, or
after 1 s of quiet output once 2 s have passed, never between
bracketed-paste screens; a `needs_input` of source `trust` or
`question` holds it and starts its clock again, and past the 60 s cap
the prompt is typed anyway (`readyWait.at`, `:2782`). `Submit`
(`internal/session/submit.go:347`) refuses Conductor's own text while a
startup question shows (`ErrTrustQuestion`), flattens line breaks and
tabs to spaces, and records an `input` entry with the text. On `main`
Claude Code's sign-in screen is a startup question too (its words are
in `ErrTrustQuestion`'s, `submit.go:53`), held the same way; the
program leaves it by itself once a person signs in through the browser
(`local.go:5017-5019`).
`defaultLabel` (`internal/dispatch/launches.go:346-358`) cuts a
ticket's key and title to 60 characters at a word and says nothing of
the cut.

**The change.**

- `internal/api/sessions.go`: `createSessionRequest` gains `Message
  string json:"message,omitempty"`. `handleCreateSession` refuses a
  message past `proto.MaxSubmit` (4,096 bytes, counted as sent) with
  `400 invalid_request` "message: at most 4096 bytes", before
  `createLocalSession`, so no process starts. A message of spaces alone
  is none. The clone route keeps its own body and refuses `message` as
  an unknown field; the resume route reads no body and so takes none;
  crews, Brakeman and resumes build the request in Go and set none.
- The name: with `name` empty and a message, `session.NameFromMessage`
  names the session: control characters become spaces, runs of spaces
  one, trimmed, then cut at a word to 40 characters and 120 bytes
  (`proto.MaxSessionName`), "…" (U+2026) inside both bounds when cut;
  a first word longer than the bound is cut at a character. Nothing
  left: `"<agent> #N"` as today. Two sessions may share such a name,
  as plain launches may today.
- One rule for a cut at a word: `session.CutAtWord(s, keep, chars,
  bytes)` in `internal/session/name.go` and its copy `cutAtWord` in
  `internal/dispatch/launches.go` (which imports only `ext`): s whole
  when it fits; else cut to `chars - 1` characters (and `bytes - 3`
  bytes when a byte bound is given) at the last space after `keep`
  characters, trailing spaces trimmed, "…" added. One table of cases,
  `internal/session/testdata/cut-at-word.json` (new), is read by both Go
  tests and by the app's tests (PR-36).
- D11: `defaultLabel(key, title)` is `cutAtWord(key+" "+title,
  len(key), MaxLaunchLabel, 0)`: "CND-12 Make the login loop stop when
  the session token…" in place of the same words with no "…". Labels
  already stored keep their text; the label's bound and its check
  (`model.go:1146`) do not change.
- `internal/crew/firstmessage.go` (new): `TypeFirstMessage(ctx, l,
  FirstMessage{Text, Agent, PromptShape, Wait, Note})` waits with
  `Wait` (nil: `AwaitReady`, the exported name of `awaitReady`, the one
  rule a crew's prompt waits on) and then `l.Submit`s the text with
  `ByName` `launch`, `Confirm`, and `Withdrawn` set to
  `l.PersonTyped()`. It retries as `promptWith` does when `Submit`
  finds a question first (`ErrTrustQuestion`, `ErrQuestion`, a poll
  later) and takes `ErrSubmitPending` as typed. Past the cap, an agent
  whose catalog entry has a `promptShape` (Claude Code, Codex) gets the
  message only at its recognised prompt (`OnlyAtPrompt`, tried each
  poll, as a Brakeman turn's line is); an agent without one gets it
  typed anyway, as a crew prompt is. The loop is the first message's
  own, about 60 lines, because `promptWith` carries a member's state;
  the wait is shared.
- What each startup question does: the trust question, an update
  dialog and the sign-in screen each hold the message while they show,
  as `main` holds a crew's prompt today, with no cap (the wait's clock
  starts again while held); each is noted once in the session's
  Activity as a `status` entry: `the first message waits: answer "Trust
  this folder?" first`. `await` calls `held` again when the held
  question's `ID` changes, not only when a hold begins
  (`internal/crew/run.go:2734`, where `held` runs only under `if
  !holding`), so two startup questions in a row give two notes. Every
  key that answers a startup question leaves the message waiting. Once
  the last question is answered the wait runs from the start and the
  message is typed.
- Withdrawn: `session.Local` gains `PersonTyped() <-chan struct{}`,
  closed at the first keys a person writes that reach the program's
  text: a viewer's `Input`, `InputContext` or `InputWaiting` written
  while no startup question shows or may show (`startupMayShow`) and
  no dialog asked during the work is on the screen, a terminal's own
  report aside; and a person's line (`Submission.By` or `From`) naming
  no question. Keys to a startup question, replies to a question and
  Conductor's own text leave it open. Closed before the text's write
  begins, nothing is typed; closed in the pause before its Enter, the
  Enter is left out and the turn held, as `Submission.Withdrawn` does
  for a revoked link. Activity: `the first message was not typed: a
  person typed in the session first`.
- The end: a session that ends first gets nothing (`the first message
  was not typed: the session ended first`). The goroutine runs under
  `s.track`, with a context of the server's own cancelled at the start
  of `Shutdown` and `WaitBackground`, so both wait for it. A message
  typed without its Enter (a question came in the pause) is noted as a
  crew's is: `the first message was typed without its Enter: answer
  the question, then press Enter in the terminal`.
- Where it is kept: in the goroutine alone until typed; then in the
  session's Activity, as every line Conductor types is (`Submit`'s
  input entry, `op: typed`, by `launch`). It is not in `Info`, the
  session's `launch` (`GET /api/sessions/{id}`), the journal or a log
  line, so Resume and Clone never type it again.
- `internal/session/info.go`: `Info` gains `FirstMessage string
  json:"firstMessage,omitempty"`, the one value `waiting`, set in the
  launch's `Info` (so the 201 reply says it) and cleared through
  `l.SetFirstMessage("")`, which tells `OnChange` as `Rename` does,
  once the message is typed, withdrawn or dropped. A hosted launch
  takes no message (the host's command has no such flag), so no hosted
  copy carries it.
- `internal/api/server.go`: a seam `firstWait` (default
  `crew.AwaitReady`) that the API's tests replace with a gate.
- The protocol's places: the request and `Info` are Go types in
  `internal/api` and `internal/session`, not frames of `internal/proto`
  (`MaxSubmit` is the bound used); `web/app/composables/useSessions.ts`
  gains `message?: string` on `create`'s body and `firstMessage?:
  'waiting'` on `SessionInfo`, two type lines, so the three places
  agree in this row; `docs/protocol.md` names `message` in the `POST
  /api/sessions` row (`:3009`: its bound, the name, the wait, the
  withdrawal, none for a clone, a resume or a hosted launch) and
  `firstMessage` in the paragraph on `Info` (`:1418-1426`).
  `docs/features.md` gets the entry.

**Tests.**

- `internal/crew/firstmessage_test.go` (Go race test, new),
  `TestAFirstMessageWaitsForTheTrustQuestion`: a `session.Local` over
  `sessiontest.FakeProc` with a trust pattern draws "Trust this
  folder?"; the wait is a `readyWait` with no quiet and no minimum
  whose ticks the test hands one by one (a new `ticks` field, the seam:
  each send returns only when the loop takes it). The test waits for
  the Activity entry that says the message waits (wait for the entry,
  not the state), hands two ticks, and reads nothing of the message on
  `p.Input`; a person's Enter in the terminal answers the question, the
  fake draws a prompt, ticks are handed until the text and its Enter
  arrive after the person's key; Activity holds one `input` entry by
  `launch`; `Info().FirstMessage` is empty. Without the change: no
  message is ever typed (the old launch has no loop), or, with a loop
  that ignores the hold, `Submit`'s refusal ends it and the text never
  arrives.
- `TestAFirstMessageWaitsForEachStartupQuestion` (same file): an update
  dialog, then the trust question drawn straight after the update
  dialog's answer with no poll between, then the sign-in screen as
  `internal/session/signin_test.go` draws it on `main`, left by the
  program on its own: one Activity entry per question, three, and the
  message typed only after the last; each time `Submit` is refused it
  is `main`'s refusal (`ErrTrustQuestion`). Without the change: one
  entry for the two questions in a row, or the text typed between two
  questions.
- `TestAPersonsKeysWithdrawTheFirstMessage` (same file): no question,
  the wait held by a tick never sent; a viewer types `ls` and Enter;
  the call returns, nothing of the message reaches `p.Input`, the
  Activity says it was not typed, `FirstMessage` is empty. Without the
  change: the message is typed after the person's keys once a tick
  comes.
- `TestAFirstMessageIsDroppedWhenTheSessionEnds` (same file): the fake
  ends while held; the call returns (waited on its return, not a
  clock), nothing is written, one Activity entry. Without the change:
  the goroutine outlives the session.
- `TestAFirstMessagePastTheCapWaitsForTheAgentsPrompt` and
  `TestAFirstMessagePastTheCapIsTypedAnyway` (same file): a `Wait`
  that answers `errNotReady` at once (as `SetNotReadyForTest` does,
  no clock); with a prompt shape the text waits while the screen is not
  that prompt (one Activity entry) and is typed once the fake draws
  it; without one it is typed at once. Without the change: typed into
  the unknown screen, or never.
- `internal/session/persontyped_test.go` (Go race test, new):
  `PersonTyped` closes at a viewer's keys at the prompt and at a
  person's reply-box line naming no question; it stays open for Down
  and Enter to a shown trust question, keys typed to the sign-in screen
  as `signin_test.go` draws it on `main`, a terminal's cursor report, a
  reply to a question and Conductor's own `Submit`. Without the change:
  no channel, or one that a trust answer closes (the message would be
  lost each time a person answers the trust question).
- `internal/session/name_test.go` (Go race test):
  `TestCutAtWordTable` reads `testdata/cut-at-word.json` (fits whole,
  exactly at the bound, one over, cut at a word, a word past the
  bound, never inside a kept key, four-byte characters past the byte
  bound with the "…" kept, trailing spaces trimmed);
  `TestNameFromMessage`: "fix the flaky vim spec on the mac runner
  please" gives "fix the flaky vim spec on the mac…", "fix it\nnow"
  gives "fix it now", spaces give "". Without the change: no "…".
- `internal/dispatch/launches_test.go` (Go race test):
  `TestTheDefaultLabelEndsWithAnEllipsisWhenCut` reads the same table
  (`../session/testdata/cut-at-word.json`) through `cutAtWord` and
  asserts the CND-12 label above, at most 60 characters, ending "…"
  after a whole word. `:136` (a short title, whole) and `:337` (61
  characters refused) stand. Without the change: no "…".
- `internal/api/firstmessage_test.go` (Go race test, new; its own
  server, `/bin/cat` as the agent):
  `TestAFirstMessagePastItsBoundIsRefused` (4,096 bytes taken; 4,097,
  and 2,049 "é", are `400 invalid_request` and the registry holds no
  new session; without the change, 201);
  `TestASessionIsNamedByItsFirstMessage` (the first words with "…", a
  given name wins, spaces give `cat #N`; without the change, always
  `cat #N`); `TestAFirstMessageIsTypedOnceItsSessionIsReady`
  (`firstWait` a gate: the 201 reply and `GET` say `firstMessage:
  "waiting"`; the gate opened, the test waits for the `input` entry in
  Activity, then
  reads `firstMessage` absent and the `launch` of `GET` without the
  text); `TestAFirstMessageEndsWithTheServer` (the gate never opened:
  `WaitBackground` returns within its context and nothing is typed;
  without the change, the goroutine leaks past the test's directories,
  the rule "end what a test started before its directories go");
  `TestTheCloneRouteTakesNoMessage` (`message` on the clone route is
  `400 invalid_request`).
- `internal/crew/run_test.go` gains the same case for a crew's prompt:
  two startup questions in a row give two notes (`held` called again on
  a new question's `ID`); its other tests and `prompt_test.go` stand
  unchanged: the rename of `awaitReady` and the `ticks` field leave the
  crew's prompt as it is, and they prove it.

**By hand.** None for this row: PR-36 holds the check with real
agents.

**Size.** About 305 lines of code (Go about 275, docs about 25, types
2) and 640 of tests.

**Defaults.**

- **Past the readiness cap.** Default: an agent whose catalog entry has
  a `promptShape` (Claude Code, Codex) gets the message only at its
  recognised prompt; others get it typed anyway, as a crew prompt is.
  Why: a message typed into an unknown screen can have its Enter pick a
  dialog's highlighted choice.
- **What withdraws it.** Default: a person's keys that reach the
  program's text first; keys that answer a startup question, the
  sign-in screen's among them, and replies do not. Why: otherwise
  answering the trust question would always lose the message.
- **Who typed it.** Default: Activity records it as Conductor's own line
  by `launch`, as a crew's is by `crew`; the request carries no
  person's display name.
- **Line breaks.** Default: typed as spaces (`Submit` flattens them),
  and the dialog says so.
- **The shared wait.** Default: the readiness wait is the crew's
  (`AwaitReady` exported), but the loop that submits is the first
  message's own, about 60 lines. Why: `promptWith` carries a member's
  state, and the crew's tests then prove it unchanged.
- **Launches that take none.** Default: an "On this computer" launch, a
  clone and a resume take no message, as a hosted launch takes none
  (owner decision 16).
- **D11's bound.** Default: the 60-character bound kept, the "…" inside
  it; labels already stored keep their text.
- **One cut rule.** Default: two Go copies (`internal/session` and
  `internal/dispatch`, which imports only `ext`) and one TypeScript
  copy, held together by one table,
  `internal/session/testdata/cut-at-word.json`, read by all their
  tests.

**Depends on.** PR-2 (the readiness `run.go` reads after an Enter
clears a done, and `info.go`, `useSessions.ts` and `docs/protocol.md`,
which this row edits after it); open #913 (`createLocalSession` in
`internal/api/sessions.go`); open #909
(`internal/dispatch/launches.go`). Not #907: nothing of
`internal/session` is in it.

**Left out.** The field in the Launch dialog, the name's preview, the
waiting line on the page and the app's copy of the cut rule (PR-36).
A `--message` for `conductor host` (a hosted launch takes none). A
message for a clone or a resume.

### PR-36: web: launch with a first message (P19, D11)

The Launch dialog has one more optional field, "What should it do?".
Its text is sent with the launch, and the Name field's placeholder
shows the name it will give. While the message waits, the session's
page says so in one line above the terminal, and says when a question
must be answered first. A ticket's launch dialog shows its default
label with "…" when it is cut, as the server makes it. The rest of the
dialog, a clone, and a launch "On this computer" stay as they are.

**Today.** `LaunchSessionModal.vue` has agent, where it runs, Name
(`:325-327`, placeholder "optional"), working directory, extra
arguments, Account and Model, Yolo and the capacity panel; `submit()`
(`:251-278`) sends `api.create({agentId, name, cwd, args, yolo,
account, model})` (`useSessions.ts:872`), and the layout opens
`/sessions/{id}` on `launched` (`layouts/default.vue:376`). The page
has no line about a first message, which does not exist before PR-35.
`defaultLabel` in `web/app/utils/dispatchPlans.ts:244-252`, the mirror
of the server's, gives `DispatchLaunch.vue:51` its placeholder with no
"…".

**The change.**

- `utils/launch.ts`: `cutAtWord(s, keep, chars, bytes)`, the server's
  rule, and `namePreview(message)`, `NameFromMessage`'s copy;
  `messageProblem(text)` gives "At most 4,096 bytes: this is N." past
  `MAX_SUBMIT` (bytes counted with `TextEncoder`), else "";
  `firstMessageLine(info, agent)` gives "Your first message is typed
  once its question is answered." while `firstMessage` is `waiting`
  and a startup question shows (`attention` `needs_input`, source
  `trust`), "Your first message is typed once <agent> is ready." while
  it waits otherwise, and "" when it does not.
- `LaunchSessionModal.vue`: on the server tab, not for a clone, a
  `UTextarea` (autoresize, two to six rows) labelled "What should it
  do?", hint "optional", help "Typed into the agent once it is ready.
  Line breaks are typed as spaces." It sits above Name. Past the bound
  the field shows `messageProblem`'s words and Launch is disabled. The
  Name placeholder is `namePreview(message)` when it has text, else
  "optional". `submit()` sends `message` when it is not only spaces and
  empties the field after a launch.
- `pages/sessions/[id].vue`: a one-line strip, `data-first-message`,
  above the terminal while `firstMessageLine` has words. It goes when
  the listing clears `firstMessage`.
- D11: `dispatchPlans.ts` `defaultLabel` calls `cutAtWord(key + ' ' +
  title, key.length, PLAN_BOUNDS.label, 0)`; `DispatchLaunch.vue` is
  unchanged.
- `docs/features.md` (the field, the name, the line) and the README's
  sentence on the Launch dialog.

**Tests.**

- `web/app/utils/launch.test.ts` (vitest): `cutAtWord` gives each
  `out` of `internal/session/testdata/cut-at-word.json`, read from the
  repository as `review.test.ts` reads `internal/crew/testdata`;
  `namePreview` agrees with the Go test's cases ("fix the flaky vim
  spec on the mac…"); `messageProblem` is "" at 4,096 bytes and words
  at 4,097 and at 2,049 "é"; `firstMessageLine` gives its three
  answers. Without the change: no such functions; a TypeScript copy
  that drifts from Go fails the table.
- `web/app/utils/dispatchPlans.test.ts` (vitest, changed): `:231-237`
  asserted "CND-12 Make the login loop stop when the session token"
  and `CND-12 ${'x'.repeat(53)}`; they become "CND-12 Make the login
  loop stop when the session token…" and `CND-12 ${'x'.repeat(52)}…`,
  60 characters; the 80 "é" case stays at 60 characters and now ends
  "…"; `:225-229` (whole when it fits) stands. Without the change: the
  old strings, no "…".
- `web/e2e/launch-first-message.spec.ts` (Playwright, new), on a
  server of its own (PR-1's `ownServer`, so that the stub's trust,
  written into `$HOME/.codex/config.toml`, lands in that server's
  HOME and not the one `trust.spec.ts` reads: what a child keeps under
  HOME is shared by every process of that HOME). At 1440 x 900: Launch,
  pick "Codex CLI, untrusted (e2e stub)", fill "What should it do?"
  with "say hello", see "say hello" as Name's placeholder, Launch. The
  page's title is "say hello"; `[data-first-message]` says the
  question comes first; the bar offers "Trust and continue"; click it.
  Then the attention's message comes to hold "got: say hello", the
  strip is gone, and the stub's transcript, read by the id the
  session's `agentSession` names once the stub reports it
  (`<HOME>/.stub-sessions/<id>.txt`, read by its id: a listing is a
  moment), holds exactly one line "say hello". The theme is switched and
  the page drawn again, and the title and the strip's absence are
  checked again. If the message had been typed into the dialog, its
  Enter would have answered it and the transcript would lack the line.
  Without the change: no field; the name is "Codex CLI, untrusted (e2e
  stub) #N".
- Same spec, the Claude Code stub, no question: the message "fix the
  flaky vim spec on the mac runner please" is typed once the session
  is ready (the transcript named by the `agentSession` id set at
  launch holds it once), the title is "fix the flaky vim
  spec on the mac…", and the sidebar's row has the same name.
- Same spec: a message of 4,097 bytes shows the words under the field,
  Launch is disabled, and no `POST /api/sessions` is sent (the page's
  requests are counted from the fill on).
- Same spec at 390 x 844 with touch: the field is `inside` the dialog
  and the dialog does not scroll sideways.
- `web/e2e/launch.spec.ts` (`:14-34`, a launch with no field filled,
  one new session counted by id), `clone-session.spec.ts`,
  `capacity.spec.ts` and `identity.spec.ts` stand unchanged and prove
  the empty field changes nothing; the Name field's selectors and the
  `data-launch-*` attributes stay. `dispatch-plan.spec.ts:155` and
  `:168` read a short title's label, whole, and stand.
- Lost here, held in Go: the withdrawal when a person types first.
  In a browser the person's keys race the readiness wait's own clock,
  so the proof is `TestAPersonsKeysWithdrawTheFirstMessage` in PR-35.

**By hand.** A real Codex in a folder it has not trusted, and a real
Claude Code, signed out, then asking to trust the folder (its sign-in
screen left by signing in through the browser, as on `main`), each
launched with a first message: each startup question is answered by
the bar's choices or by that sign-in only, never an Enter into the
agent's own screen; the message is typed once, after the last
question, and nothing of it lands in a dialog. A launch from a
workbench served from another machine: the field is not on the "On
this computer" tab.

**Size.** About 110 lines of code and 330 of tests.

**Defaults.**

- **The wait shown.** Default: `Info.firstMessage` "waiting" and a
  one-line strip above the terminal, besides Activity. Why: PR-19
  closes the inspector where Activity lives.

**Depends on.** PR-35 (the field, `firstMessage`, the shared table);
PR-19 (the strip sits above the terminal of the page PR-19 lays out,
with the inspector closed, where Activity is no longer on screen);
PR-1 (`ownServer`, `inside`); open #912 (it edits
`utils/dispatchPlans.ts` and its test: a textual conflict only).

**Left out.** A message for a clone or for "On this computer". Which
words of a long message the sidebar shows (the name's 40 characters,
PR-35). Opening or resuming an agent session by id from the dialog
(issues #660 and #661, outside this stream).

### PR-37: docs, web: a design system that rules (P20)

`docs/design/system.md`, which PR-3 began with its Status section,
becomes whole: the layer above Nuxt UI's components: the status
table, the page header, the toolbar, the list row, the rules for
cutting text, the colour, type and contrast rules, and a tooltip on
every icon-only button. Each rule says whether the maintainers decided
it, and when, or only describes what the code does. Each rule that a
machine can hold names the check that fails when the code breaks it,
and those checks run in CI. A person sees one change in the app: every
icon-only button shows a tooltip on hover and on focus.
`docs/design/brand.md` stays the brand's identity and points to the
file.

**Today.** `docs/design/` holds `brand.md` (identity, palette, type),
two briefs and `crews-graph-brief.md`. No status table, page header,
toolbar, list row or rule for cutting text is written anywhere under
`docs/`, and no rule carries a date or who decided it;
`briefs/sidebar.md:67` gives "amber needs you, green running, grey idle
and exited", which P1 changes. The app has 168 components and 23
pages; `UDashboardNavbar` is used in 18 files, `truncate` 181 times in
74 files. About 100 icon-only buttons are drawn, about 77 of them in
45 files with no tooltip and no title (`EditorArea.vue` and
`FileBrowser.vue` six each). The first defect of the UX review (a long
title that runs out of its row on the Needs you page) came from a row
that is a `<button>` laid out as a flex column with no `items-*`: the
browser's style for a button starts its items at the start, so the
title's line is sized to its content, and a `truncate` line's content
is its whole text (`white-space: nowrap`). Open PR #912 fixes it with
`items-stretch` in `INBOX_ROW_CLASS` (`utils/inbox.ts`) and measures
the row; a missing `min-w-0` was not the cause, since a `truncate`
element hides its overflow and its automatic minimum is already 0. The
maintainers also keep a design-system page outside the repository, made
by a capture of the running app; no pull request can change it.

**The change.**

- `docs/design/system.md`, PR-3's, made whole; PR-3's Status section
  becomes the first rule below. Its first lines say how to read it: a
  rule ends with "Decided YYYY-MM-DD by the maintainers, #N" (the date
  the row that made it true merged, and that row's issue) or
  "Described, not decided"; a change to a rule changes its check in the
  same pull request, and a check that reads this file fails when the
  two disagree. The rules, each with its check:
  - Status. The five states of `STATUS_LOOK` (`utils/status.ts`) as a
    table, State, Mark, Word, Means: needs you, working, finished
    ("finished 12m ago"), running, exited; "starting" takes the running
    mark and "host away" the exited mark (one that asks is "needs you ·
    host away"); green is for a live agent: `working` is the green dot
    in its ring, `running` (no signal) the plain green dot, and the
    ring alone tells them apart; no other state is green; a run that
    has ended says its outcome. Every surface draws a state
    through `StatusMark.vue`; no success fill carries text. Checks: the
    doc-and-code table test, the status-map rule.
  - Colour and contrast. The ink scale, the canvas and the page, the
    text-safe status tones, and PR-24's tables of floors. Dimmed text
    says nothing that is not said again elsewhere. Checks:
    `themeContrast.test.ts`, `contrast.spec.ts`, the floors-table test.
  - One main action. One filled forest button a work area (a page's
    header, a dialog, a card), the rest outline or ghost. Checks:
    PR-26's one-filled-action rule, `main-action.spec.ts`.
  - The code font for evidence. Ids, paths, commits, commands, keys,
    logs and measures; sentences in Inter. Check: PR-26's mono rule.
  - Amber when someone must act. A question, a plan to approve, a task
    out of attempts, a decision, a confirm that loses work, a read that
    failed; never a standing fact. Check: `main-action.spec.ts`'s amber
    case.
  - The page header (`UDashboardNavbar`). The page's name first and
    never cut by its own buttons; one filled action; on a phone the
    labels fold into icon buttons with tooltips. Checks: the
    one-filled-action rule, PR-19's header spec at 390 x 844.
  - The toolbar (PR-12's, on a Dispatch project): the views, search,
    Filter with the filters in use as chips, the count, Views, the
    sort, in that order, one row. Check: PR-12's toolbar spec.
  - The list row (PR-7, PR-11, PR-12): a name line, then a meta line;
    cells in fixed tracks, an empty cell kept empty; the state said
    once, by its mark and word. Checks: PR-1's `aligned` and `uncut` in
    those rows' specs. Marked "Described, not decided" until PR-7 and
    PR-11 have merged; the row that lands it changes the line.
  - Cutting text. A text is cut only inside a box whose width comes
    from outside it: a block, a stretched flex item, or a grid track of
    `minmax(0, …)`. In a flex column whose items are not stretched (an
    `items-start`, `-center`, `-end` or `-baseline`, and every
    `<button>`, whose browser style starts its items at the start), the
    line that holds the cut takes `items-stretch` on the column or
    `self-stretch` or `w-full` on itself. In a flex row, a child that
    holds a cut deeper inside it takes `min-w-0`; the cut element
    itself needs none. A name keeps its start, a path keeps its end
    (the file's name), an id keeps its start; a cut text carries its
    whole form in its `title`; a choice is never cut; a question is
    whole except in the narrow sidebar, beside its panel (PR-8).
    Checks: the cut rule, and `uncut` in the layout specs for what
    crosses a component.
  - Tooltips. An icon-only button is an `IconButton`: its label is its
    tooltip and its accessible name. A label hidden at some width
    (`hidden sm:inline`) counts as none. A labelled button gets a
    tooltip where its label alone does not say what happens; that
    part is for review, marked "Described, not decided". Check: the
    tooltip rule.
  - What a pull request cannot do. The design-system page outside the
    repository is published by a maintainer at a release, from the
    capture of PR-38; that is a by-hand step on the release's
    checklist, never a pull request's.
- `web/app/components/IconButton.vue`, new: a `UTooltip` over a
  `UButton`, with `label` required and given as the tooltip's text and
  the button's `aria-label`, and an optional `kbd` for the tooltip's
  key. The icon-only buttons without a tooltip move onto it (about 77
  in 45 files); those already in a `UTooltip` stay.
- `docs/design/brand.md`: "Typography and layout" and the status
  paragraph point to `system.md`; the identity stays.
  `docs/design/briefs/sidebar.md:67` points to the status table.
  `docs/checks.md` names the rule checks under `npm --prefix web test`.

**Tests.**

- `web/app/utils/designRules.test.ts` (vitest), every rule marked: each
  `###` heading under the file's rules has one "Decided … by the
  maintainers, #N." or "Described, not decided." line. It reads
  `docs/design/system.md` with `readFileSync`, as `tasks.test.ts:127`
  reads a file outside `web/`. Without the change: a heading with no
  mark fails it, proved on a two-rule sample inside the test.
- `designRules.test.ts`, the status table is the code's: it parses the
  table under Status and compares it with `STATUS_LOOK`, key by key:
  the mark, the word and the legend line. Without the change: a word
  changed in one place alone fails it (the sample spells "done" for
  finished).
- `designRules.test.ts`, the floors are the test's: the tables under
  Colour and contrast equal `CONTRAST_FLOORS`. Without the change: a
  floor edited in the doc alone fails it.
- `designRules.test.ts`, the status map: no source but
  `utils/status.ts` maps a state's name (running, idle, exited,
  needs_input, working, done and the run and task states) to
  `bg-success`, `bg-warning`, `bg-neutral-400`, `text-success` or
  `text-warning`. The files still doing so when this lands (19 on
  main, fewer once PR-3 and PR-10 merge; Dispatch's ticket states and
  label colours are not statuses) are listed with a reason each; the
  list only shrinks, and a stale entry fails. Without the change: a
  new `{ running: 'bg-success' }` anywhere fails it.
- `designRules.test.ts`, the cut rule: through `vue/compiler-sfc`, for
  each element with `truncate` or `line-clamp-*`, the path up its
  template is checked against the rule above. Classes given by a
  constant (`:class="[INBOX_ROW_CLASS, …]"`) are resolved from the
  exported string constants of `utils/`. It sees one template at a
  time, so the count each file has on this row's base is frozen as a
  list that only shrinks. Without the change: removing `items-stretch`
  from `INBOX_ROW_CLASS` (#912's fix) fails it on `pages/decisions.vue`
  and `JoinDecide.vue`; a wrapper in a flex row without `min-w-0`
  fails it; a `truncate` element with no `min-w-0` of its own does
  not.
- `designRules.test.ts`, the tooltip rule: a `UButton` with an icon and
  no visible label (no `label`, no text in its slot, or text only in a
  span hidden at some width) is a child of a `UTooltip`; anything else
  is an `IconButton`. None is allowed once the buttons have moved.
  Without the change: it fails on main about 77 times; a new bare icon
  button fails it.
- `web/e2e/tooltips.spec.ts` (Playwright), new, through `dayPage` at
  1440 x 900, in each theme, on a session the test makes on the day's
  server (ended and removed after): the session page's More button
  shows "More" in a tooltip on hover, and again when it is reached by
  `tabTo`, the focus first moved off the terminal (a click on the page
  header), since a session page focuses its terminal on load and xterm
  keeps Tab (`chords.spec.ts:73-75`); at 390 x 844 with touch the button
  keeps its accessible name. Each tooltip is waited for by its role, and
  the next is only asked for once the first is gone (AGENTS.md: a menu
  that has closed is not gone). Without the change: More has no tooltip.
- Existing specs that find icon buttons by role and name, or by
  `aria-label`, keep working: `IconButton` keeps the label as the
  name. None asserted the absence of a tooltip. No proof is lost.

**By hand.** Read `system.md` once as a newcomer would, beside the
app. Read the tooltips' words on the pages that gained them: a test
sees that a tooltip exists, not that its words say what happens.

**Size.** About 330 lines of docs, about 200 of code (`IconButton.vue`
30, the buttons moved 170) and about 380 of tests.

**Defaults.**

- **Icon-only buttons.** Default: every one without a tooltip moves onto
  a new `IconButton.vue` (a `UTooltip` over a `UButton`, its label the
  tooltip and the `aria-label`), and none is allowed after it.
  Alternative: today's 77 frozen as a list that only shrinks.
- **A rule's date.** Default: "Decided YYYY-MM-DD by the maintainers,
  #N" carries the merge date of the row that made it true and that row's
  issue; rows not yet merged (the list row of PR-7 and PR-11) are
  "Described, not decided" until they land.

**Depends on.** PR-3 (`STATUS_LOOK`, which the table test reads, and
the file's Status section), PR-12 (the toolbar it writes down), PR-24
(`CONTRAST_FLOORS` and the rule harness) and PR-26 (the three rules it
writes and their checks). Open PR #912's `INBOX_ROW_CLASS`, which the
cut rule's proof removes. PR-10 shrinks the status-map list first. Of
issue #374, #908 (merged) took the first point (no label
cut on the welcome card), PR-30 the second (each choice says what
happens) and this row the third, a tooltip on every icon-only button as
a rule with its check: PR-30 merges first and says `Part of #374`, and
this row says `Fixes #374`.

**Left out.** The capture and the report of what drifted (PR-38).
Publishing to the design-system page, which no pull request can do.
The rules of rows still to come (the empty page of PR-22 and PR-23,
the line diagram of PR-33): each row writes its own rule when it
lands. Search in the shortcuts dialog (#708) and themes as files
(#346) are not in this stream.

### PR-38: e2e, scripts: the design capture in the repository, and what drifted (P20, X-screens-look)

A maintainer can run one command at a release, `make design-capture`,
that drives the built app on the seeded day through every page in
both themes and at both sizes, writes the screens and the values the
page actually computed, and prints what drifted since the last
release: a token, a status word or colour, a component or page added
or gone, a rule that changed its mark. The pictures stay on that
machine; the values the report compares are committed as
`docs/design/system.json`. Nothing changes for CI or in the app.

**Today.** The capture that made the maintainers' design-system page
runs outside the repository; its scripts and outputs are not here. The
e2e suite has one opt-in spec, `web/e2e/live.spec.ts`, skipped unless
`CONDUCTOR_E2E_LIVE=1`. No spec takes screenshots, and this plan keeps
pixel baselines out of CI. The draft release that
`scripts/release_draft.py ensure` makes (`:218`) has no body, so a
release carries no checklist.

**The change.**

- `web/e2e/look.spec.ts`, new, skipped unless `CONDUCTOR_E2E_LOOK=1`,
  as `live.spec.ts` is. On the seeded day through `dayPage`, at 1440 x
  900 and at 390 x 844 with touch, in each theme: every page of
  `utils/pages.ts` (PR-4's table), and a session, a run, a ticket, the
  Needs you page with an item open and the same page at zero. For each
  screen it writes `web/test-results/look/<page>-<theme>-<width>.png`,
  and into `look.json` the resolved value of every `--ui-*` token and
  every `--color-ink-*` step in each theme (read through a probe
  element), each `[data-status-mark]` drawn with its word and computed
  colour, and the ten worst of PR-25's contrast walk.
- `scripts/design_drift.py`, new, standard library only: it reads
  `look.json`, lists `web/app/components/*.vue` and the headings and
  marks of `docs/design/system.md` itself, and compares them with
  `docs/design/system.json`. It prints each drift on a line ("token
  `--ui-text-muted` (light): #666e67 to #5f6760", "component added:
  `IconButton`", "status word: finished to done", "rule Toolbar:
  Described to Decided") or "Nothing drifted." `--check` exits 1 on
  drift; `--write` rewrites the JSON with sorted keys.
- `docs/design/system.json`, new: the first baseline, written by
  `--write` from one capture of this row's head. It holds values, never
  pictures.
- `Makefile`: `design-capture`, which runs `web-build` and `build-go`,
  then the spec with `CONDUCTOR_E2E_LOOK=1`, then the report.
- `scripts/release_draft.py`: `ensure` gives the draft it makes a body,
  a checklist of the by-hand steps, among them: "Run `make
  design-capture` at the release commit, read what drifted, and commit
  `docs/design/system.json` with `--write`." The checklist is removed
  from the body before the release is published. A draft kept from an
  earlier run keeps its body.
- `docs/design/system.md`'s last rule and `docs/checks.md` say where
  the capture is and that it is a by-hand step, not a check.

**Tests.**

- `scripts/design_drift_test.py` (the scripts suite), new, with small
  JSON inputs written by the test: a changed token, a component added
  and one removed, a status word changed, a page added and a rule's
  mark changed are each reported on their own line; equal inputs print
  "Nothing drifted." and exit 0; `--check` exits 1 on any drift;
  `--write` is stable (two writes, one result). Without the change: the
  script is new; a comparison that only looks at tokens misses the
  component and the rule cases.
- `scripts/release_draft_test.py` (the scripts suite), changed: the
  draft `ensure` makes carries the checklist with the capture line, and
  a draft that already exists for the commit is not rewritten. Without
  the change: the made draft has no body.
- `web/e2e/look.spec.ts` (Playwright, opt-in): skipped in CI as
  `live.spec.ts` is, and typechecked by `npm --prefix web run
  typecheck:e2e`. Its proof is one run by hand on this row's head:
  `make design-capture` then `design_drift.py --check` against the
  `system.json` that run wrote says "Nothing drifted."; changing one
  token in `main.css` and running again names that token. The pull
  request says it ran, or that it did not.

**By hand.** The release step itself, by a maintainer, at each
release: run the capture, read the drift report, publish the screens to
the design-system page, and commit the new `system.json` in a pull
request.

**Size.** About 360 lines of code (the spec 170, `design_drift.py`
160, the Makefile, the release body and the docs 30), a generated
`system.json` of about 250 lines, and about 150 of tests.

**Defaults.**

- **Where the publishing step is written.** Default: on a checklist that
  `scripts/release_draft.py ensure` writes as the draft release's body,
  since no release checklist exists today; the checklist is removed
  from the body before the release is published. Alternative: a line in
  the docs only.
- **The drift report.** Default: a report, not a CI gate;
  `docs/design/system.json` (values only) is committed and refreshed at
  each release with `--write`; screens are never committed.

**Depends on.** PR-37 (`system.md`, whose rules and marks it reports),
PR-25 (the contrast walk it records), PR-4 (`utils/pages.ts`, the pages
it walks) and PR-1 (`day`, `dayPage`).

**Left out.** Pictures in the repository and pixel baselines in CI.
Running the capture in CI. Publishing to the design-system page, which
stays a person's step.

### PR-39: ci: the e2e job in two shards (X-run-time)

CI's e2e check runs as two jobs, `e2e (1)` and `e2e (2)`, each about
half the suite, side by side; the pull request's wall time falls back to
the go job's. The gate (`required-checks`) is as it was, a failing shard
uploads its report under its own name, and `make test-e2e` and `make
preflight` still run the whole suite on one machine. This row is cut
at once, beside PR-1: the e2e job is already longer than the go job on
`main` (below), and PR-1's day adds to it.

**Today.** `.github/workflows/ci.yml:165-285` runs one `e2e` job on
ubuntu-22.04: Neovim, the browser's system packages and Chromium, then
`npm run test:e2e` (`playwright test`), with no `timeout-minutes` of its
own, and on failure one artifact named `playwright-report`. The suite
runs one worker with `fullyParallel: false`
(`web/playwright.config.ts:10-11`). On pull requests merged on
2026-11-14 and 15 the e2e job took 48, 60 and 65 minutes (#892, #895,
#905) against a go job of 58, 59 and 46, so it is the longer of the two
on two of those three. `scripts/preflight_test.py`'s
`test_every_ci_job_has_a_linux_step` reads `ci.yml`'s job ids and wants
each covered by a preflight step.

**The change.**

- `ci.yml`, the `e2e` job: `strategy: { fail-fast: false, matrix: {
  shard: [1, 2] } }`; the test step runs `npx playwright test
  --shard=${{ matrix.shard }}/2` in `web/`; the failure artifact is
  named `playwright-report-${{ matrix.shard }}` (`upload-artifact@v4`
  refuses a second artifact of one name in a run); `timeout-minutes: 90`
  on the job, the unsharded run's longest plus its setup, a hang bound
  that no honest shard meets, as the go job's is. `fail-fast: false`
  keeps one shard's failure from cancelling the other, so both reports
  are kept.
- The job id stays `e2e`: `required-checks`' `needs` and the preflight
  test hold as they are. With `fullyParallel: false` Playwright shards
  by file, so a file's serial tests and the server of its own stay in
  one shard; each shard starts its own shared server in `globalSetup`
  and builds its own seeded day.
- `docs/checks.md`: a paragraph: the e2e job runs in two shards; `npx
  playwright test --shard=1/2` reproduces one here; each shard's minutes
  on the pull request that cut this row; the day is built once a shard.

**Tests.**

- `scripts/preflight_test.py` (the scripts' unittest suite):
  `test_the_e2e_shards_cover_the_suite`, which reads the `e2e` job of
  `ci.yml` and asserts its matrix lists every shard from 1 to N and its
  test step shards `${{ matrix.shard }}/N` with the same N. Without the
  change it fails at its first assertion (no matrix); it fails later if
  a shard is dropped from the list or N is raised alone, either of which
  leaves files that no shard runs.
- `test_every_ci_job_has_a_linux_step` (unchanged) holds that the job
  keeps its id.

**By hand.** On the pull request's own CI run: both shards green, and
the two shards' counts of tests (the list reporter's last line) add up
to the unsharded count on `main`'s last run, quoted in the PR body with
each shard's minutes.

**Size.** About 25 lines of workflow and 10 of docs; about 25 lines of
tests.

**Defaults.**

- **The shards' job.** Default: the job keeps its id `e2e` (so
  `required-checks` and the preflight test hold), with `fail-fast:
  false` and `timeout-minutes: 90` as a hang bound.

**Depends on.** Nothing; it is cut first, beside PR-1. It shares
`docs/checks.md` with PR-1 and PR-38, in paragraphs of their own.

**Left out.** More than two shards, and more than one worker a shard:
the suite's specs share one server a shard by design, and two workers
would need a server each, a change to `global-setup.ts` and to every
spec that assumes one. Balancing the shards by time: Playwright 1.44.1
balances shards by the number of tests and keeps each file whole
(`filterForShard` in its `testGroups.js`), and the PR body says how
far apart the two come.

## Tests

The stream changes what nearly every screen draws, and its tests are
most of its work. Each row proves what it changes at the level that
decides it, and no lower or higher:

- A function that decides (a state, a count, which nav rows show, where
  a label is cut, a page's empty words) is a pure function in
  `web/app/utils/`, tested by vitest beside it. vitest runs in node
  (`web/vitest.config.ts:9`), with no DOM.
- What is drawn is tested by Playwright in Chromium 1117
  (`@playwright/test` 1.44.1), against a built server with the stub
  agents, at stated window sizes.
- What the server says (PR-2, PR-21, PR-34, PR-35) is tested by Go race
  tests, and where the app reads the same rule, by one table of cases
  under `testdata` that both read, as `tasks.test.ts` reads
  `internal/crew/testdata/task-commands.json`.

Every row runs the checks of `docs/checks.md` for its surfaces before a
push: `npm --prefix web run typecheck`, `npm --prefix web test`, `npm
--prefix web run typecheck:e2e`, `make web-build build-go` (the e2e
server embeds the web build), its new and changed specs alone with
`--repeat-each 10`, the same once more beside a Go race package as load,
then `make test-e2e`. A row with Go adds `go test -race -count=1
-timeout 60m ./...`, `make lint` and `make lint-static`. A check that
did not run is reported as not run.

### The seeded day

Most of the stream's claims are about a busy day: how many rows fit,
whether a name is whole, whether three surfaces give one number. The
shared e2e server cannot hold such a day. Every spec runs on it
(`web/e2e/global-setup.ts:16-44`, one worker,
`web/playwright.config.ts:10-11`), so it holds whatever earlier specs
left, and twelve specs call `stopStrays`
(`web/e2e/fixtures.ts:175-184`), which ends every running session a spec
did not make. That is why the suite counts by shape today ("the counts
are checked by shape, not by number", `sidebar.spec.ts:105-106`). PR-1
adds a day of work on a server of its own, so that a row can assert
exact numbers.

The pieces: `web/e2e/own-server.ts` (`ownServer(name, { agents,
repoPath, env })`, the body of `decisionServer` in
`decisions-helpers.ts:66-103` moved and named), `web/e2e/day.ts` (the
parts), and in `fixtures.ts` a worker fixture that starts the day's
server, a test fixture `day` over it, and `dayPage`. The best model is
`decision-page.spec.ts`: a server of its own, a crew whose check fails,
every item found by its ref, every step's waits added to the test's
timeout.

The day is built in named parts. A spec asks for the parts it reads
(`await day.part('states')`) and gets back names mapped to ids; a part
asked for again is not built again. The names are test data.

| Part | What it holds | Builds on |
|---|---|---|
| `states` | Five loose sessions, one in each state of the status table below: `mac-only-flake` asks, `yard-tile-resize` is working, `flaky-vim-spec` has finished its turn, `driver` runs an agent with no hooks, `old-bisect` has exited. | none |
| `dispatch` | Projects CND and OPS; Nate, Ana and Priya; six labels, two milestones; fourteen tickets with owners, assignees and priorities in Dispatch's sense; four comments and two relations; CND-1 with an approved plan, CND-5 with a plan edited after its approval. | none |
| `waiting` | One item of each kind the inbox holds: a loose question (`proc-race-tests`, three choices), a run member's question (Token pairing's navigator), a clarification its asker's session mirrors, a plan's gate decision that no session mirrors (`dr-1`, from CND-1's plan), a plan revision waiting for approval (CND-2 revision 1), and a task out of its three attempts (Paging spike's `index`, a check that exits 1). | `dispatch` |
| `runs` | Ledger migration, six tasks: two done, one working, one waiting on a decision its asker's session mirrors, two waiting behind them; Release notes, succeeded. | `dispatch` |
| `long` | A session renamed to 120 characters, a run whose label passes 60 characters, a ticket title of 200 characters. | `dispatch` |
| `busy` | Every part above, and loose sessions enough for eighteen live sessions in a fixed mix of working and finished. | all |

**Working and finished without a browser.** The stub ends every turn at
once: `answer` in `web/e2e/stub-agent.sh` reports the turn's start
(`report_working`) and its end (`report_done`) in one go, so on a fresh
server nothing is working, and whatever was typed is finished a moment
later. PR-1 gives the stub one variable read at its start, `STUB_TURN`.
With `finished` it reports one turn's start and end through its agent's
hooks, as a typed line does. With `working` it reports the start and
holds the turn, reading and drawing nothing, until
`$HOME/.stub-sessions/<id>.hold` says `stop`. The day runs each from a
copy of the catalog's `claude` agent under its own id, the way
`decisionServer` copies agents with `STUB_DECISION`, so a session's
state is fixed by which agent it runs, never by what a browser typed or
when. A run's working task is a member on a copy of `worker-cli` with
`STUB_TURN=working`: its attempt holds while the turn does. An agent
with no hooks is the catalog's `plain` stub (signal `bell`), which
reports nothing. An exited session is stopped once; the server keeps an
exited session listed for `CONDUCTOR_EXITED_RETENTION`, ten minutes by
default (`internal/config/config.go:790`), and the day's server sets it
to 24 hours.

The day does not use the operator's "Mark as working" (`POST
/api/sessions/{id}/attention`). A state posted that way proves the post,
not the agent's report, and it is how the UX review's own day was set by
hand, which is why that day cannot be made again.

**Nothing on the day lapses.** No part holds a state on a timer: no
check held by `sleep`, since a check's timeout is capped at 3,600 s
(`internal/crew/crew.go:243`) and a local run can outlast it. A task
that is checking comes from `run-fixtures.ts` (below), not from the day.
No age is fixed: "finished 12m ago" is wall time from the part's start,
matched by a pattern, and a width check for the longest age moves the
page's clock with `ageBy`.

**Read-only, and guarded.** A spec that uses the day reads it. A spec
that must act (answer, stop, rename) makes its own item on the day's
server and ends and removes it before it ends (a second DELETE removes
an exited session). The test fixture `day` holds the rule: at the end of
every test that used it, it lists the server's sessions, runs and inbox
items, ends any that is not the day's, and fails that test with its
name. It then rereads, by name, every part already built in the state
that part promises (the reads of `day.spec.ts`), and fails the test
that changed one, naming the item. A leftover or a changed item fails
where it begins, not in whatever spec meets it next (the rule of
`endHosted` in `fixtures.ts:297-329`).

**What a spec may measure on it.** The parts a spec did not ask for may
still be there: an earlier spec of the same worker may have asked for
them. A spec that reads its things by name may ask for a light part. A
spec that measures how much fits (rows in view, the size of a tile) asks
for `busy`, so that it always measures the same page whatever ran before
it.

**Its life and its cost.** The server starts at a worker's first `day`;
a part is built inside the test that first asks for it, with each wait's
cap added to that test's timeout (`budget`). At the worker's end the day
is ended in the order AGENTS.md asks: runs stopped and waited ended,
sessions ended, hold files written `stop`, the server stopped, its log
copied to `test-results/day-server.log`, the root removed. The suite
runs one worker, so one day serves a whole run; after PR-39, one serves
each shard. Playwright starts a new worker after a failed test, and the
next spec that asks builds the day again. The cost is not yet measured:
`states` takes seconds; `waiting` is mostly the spike's three failed
attempts, which `decision-page.spec.ts` gives 180 s as a cap and which
take far less; the whole day is expected at one to two minutes. PR-1
measures it on CI and writes the number into `docs/checks.md`.

`dayPage(browser, { path, width, height, theme, touch })` opens a page
on the day's server (or, given `server`, on another, such as the shared
one): the workbench token and a display name where the app keeps them,
the theme (below), the viewport, and `hasTouch` and `isMobile` when
`touch` is set. It goes to `path` and returns once the layout's own root
is drawn (`#dashboard-sidebar-main`, or `[data-bottom-bar]` below 1024
px), `html` carries the theme's class, and `document.fonts.ready` has
settled.

To look at the day by hand, `CONDUCTOR_E2E_DAY_OPEN=1 npx playwright
test day.spec.ts -g open` builds it, prints its address and the path of
a file holding its token, and holds it until Ctrl+C.

`web/e2e/day.spec.ts` proves the day is what it says, through the API:
each part's items exist by name in the state the part promises, and they
hold after a page of the Yard has attached every session. When a stub or
the engine changes what a part makes, this spec fails first and names
the part, so a later layout failure is never a seeding drift in
disguise.

### Stubs in the browser

Some shapes the day cannot make exactly or cheaply: a title of 200
characters in a decision, a run of 40 or 128 tasks, every task state at
once, an item answered and not yet applied, a read that fails. For those
PR-1 adds two modules that answer the app's reads in the browser
(`page.route`), modelled on `longRows` in open PR #912's
`decisions-ask.spec.ts`:

- `web/e2e/inbox-stub.ts`: `inboxItem(kind, over)` and `stubInbox(page,
  items)`, which answer `GET /api/inbox`, `/api/inbox/item`,
  `/api/inbox/track`, `/api/decisions` and `/api/decisions/{id}`.
- `web/e2e/run-fixtures.ts`: `runInfo({ tasks, states })` with 6, 40 and
  128 tasks, and `serveRun(page, run)`, which answers every read the run
  page makes under `/api/runs/<id>` and records any it does not.

A stub proves how a shape is drawn, never that the server sends it. What
a count or a state walk proves is proved on the day.
`web/e2e/stubs.spec.ts` holds each stub to the server: an item of each
kind carries every key the day's real items carry, and the run page
drawn from `serveRun` made no read the module did not answer. A field
the server gains, or a read the run page gains, fails there, not in a
layout spec that would blame the layout.

### Layout by measures, not pixels

`web/e2e/layout.ts` asserts what the UX review complains of as measures
at a stated window size. Each helper fails when it matched nothing (a
page not yet drawn passes every "hidden"), measures after
`document.fonts.ready`, keeps its slack in one place (half a pixel for
boxes, one for alignment), and returns what it measured, so that a
failure reads "`mac-only-…` is 143 px in 118 px".

- `uncut(locator)`: each element's `scrollWidth` is at most its
  `clientWidth`, and its height at most its box's. It holds "Conductor
  reads C…", "Launch ag…", names cut to "mac…", "Load the ex…", "SHARED
  WITH Y…".
- `inside(part, container)`: the part's box lies within the container's.
  It holds a title that runs out of its row across the open item (D1).
- `noOverlap(root)`: the root's visible outermost pieces are pairwise
  clear and on screen. It holds count badges under the run's buttons
  (D2) and a phone header's buttons over the name (D9).
- `aligned(rows, cell)`: every row has the cell, empty or not, and the
  cells' left edges agree. It holds "no column can be read down" on a
  project and the run's task table.
- `rowsInView(scroller, row)`: how many rows lie wholly inside the
  scroller's visible box. It holds "the list shows two of eighteen".
- `topAtMost(locator, y)`: the element starts at most `y` px down the
  window. It holds "the first ticket starts 400 px down".
- `lines(locator)`: the rendered lines, height over line height. It
  holds "a question on two lines" and a count wrapped onto two.
- `noSideways(page)`: the document never scrolls sideways.
- `tapTarget(locator)`: at least 44 px each way under touch.
- `tabTo(page, locator, max)`: Tab reaches the control within `max`
  presses and it draws a focus ring (a computed outline or box shadow).
  `tabTo` starts from a given element, never from a terminal.
- `atWidths(sizes, fn)`: sets each window size, reloads, waits for the
  layout's root and runs `fn`.
- `ageBy(page, ms)`: an init script that moves the page's clock ahead,
  for the longest age a width must hold (Playwright 1.44.1 has no
  `page.clock`).
- `probeColour(page, value)`: the colour a CSS value resolves to on the
  page, through a probe element, so that a drawn colour is compared with
  a token without parsing `oklch`.

The models are `hscroll.spec.ts:17-31` (widths of the row, the body and
the document), `yardheader.spec.ts:17-38` (pairwise overlap at 390 px)
and `fitsSidebar` in `sidebar.spec.ts:750-761`. Containment alone is not
enough: `fitsSidebar` passes a `truncate` label cut to "C…", because the
cut label's box is inside the sidebar. D1 is the opposite case, caught
only by containment: the title is not cut, it runs out of its row (open
PR #912's diagnosis, the row's line not taking the button's width). A
row asserts each claim with the helper that catches it.

`web/e2e/layout.spec.ts` proves every helper on pages made with
`page.setContent`, no server: each passes on a good page and fails on a
bad one (a cut label, a title that runs out of a flex column, two
overlapping buttons, a column off by 3 px, a list one row short, a
button 32 px tall, a button with no focus ring), so a helper that
measures nothing cannot pass.

### The status walk

One table, held by the spec and not imported from the app, so that a
change of the app's words fails it. Every surface that draws a session's
state draws one of five marks with its word; the mark carries
`data-status-mark="<key>"` and its word `data-status-word`
(`data-status` is already the member's status on graph nodes and run
cards).

| Key | Mark | Word | On the day | Made by |
|---|---|---|---|---|
| `needs` | amber dot | needs you | `mac-only-flake` | an `asker` copy, which asks at its start |
| `working` | green dot with a ring | working | `yard-tile-resize` | `STUB_TURN=working`: the turn's start reported and held |
| `finished` | grey ring | finished 12m ago | `flaky-vim-spec` | `STUB_TURN=finished`: a turn's start and end reported |
| `running` | plain green dot | running | `driver` | the `plain` stub: no hooks, nothing reported |
| `exited` | dashed ring | exited | `old-bisect` | stopped once, kept listed |

The surfaces join the walk as the rows that change them land:

| Surface | Row |
|---|---|
| The sidebar's row, the folded sidebar's square, the phone's list at 390 px | PR-3 |
| The Yard's tile and its bar, the session page's badge, the Roundhouse, the palette's session row | PR-3 |
| The sidebar's Finished and Working sections | PR-14 |
| The Yard tile's foot | PR-17 |
| A run's task row, graph station, card and header; Dispatch's run card | PR-10 |
| The line diagram's station, large and small | PR-33, PR-34 |

`web/e2e/status-walk.spec.ts` (PR-3) runs one test per surface and
width, so a failure names the surface. Each loads the page once, waits
for the five sessions' rows (a positive element, never an absence),
reads all five, then causes a redraw (a section folded and unfolded, or
the theme switched) and reads them again. A read checks the key, the
word against the table (the age by a pattern: it ticks while the test
reads), and the mark's computed colour against the state's token through
`probeColour`. On `main` it fails at its first surface: the finished
session's sidebar row says `running` (`utils/sidebar.ts:194-198` has no
working or finished), its tile says "Running" (`SessionTile.vue:22-27`),
and the Yard's bar draws done in blue (`AttentionBar.vue:19`).

The states the walk cannot wait for are proved in vitest:
`utils/status.test.ts` walks every combination of status, attention,
signal kind and turns, from PR-2's
`internal/session/testdata/status-table.json`. "starting" (the running
mark, the word "starting") only flashes by in a browser, and a test that
waits for a state passed through misses it. "host away" (the exited
mark) is also read once in a browser, on a hosted session of PR-3's own,
with `hostedend.spec.ts` as its model.

### The counts

One number of what waits on a person, the same on every surface that
shows it. `web/e2e/counts.spec.ts` (PR-5, extended by PR-13) reads the
whole day (`busy`: a count is a measure of how much). Its oracle is the
server: the refs `GET /api/inbox` lists, asserted first to be exactly
the refs the parts made, so that a change in the server shows as itself.
Each count the app keeps carries `data-count-of="needs"` (PR-5), so the
readers do not parse Nuxt UI's badge markup. The readers: the nav's
Needs you badge, the folded sidebar's rail, the phone's tab bar, the
palette's row, the pill's count, the tab title, the favicon's dot, and
later a ticket strip's count of what needs you (PR-16).

The model is `decision-page.spec.ts`, which finds every item by its ref;
no spec asserts a count today (`sidebar.spec.ts:106` reads it by shape).
The readers are read at one moment, as one tuple, polled until it
settles (`expect.poll(readAll)`): read one by one while the inbox moves,
they give a picture neither order would. Then the test answers one item
it made itself on the day's server, every reader drops by one together,
the page is reloaded and the tuple is read again. On `main` it fails:
the nav's Decide and Yard badges and the sidebar's section give three
numbers for one day (the UX review saw 9, 5 and 6). The rule's parts
(which rows show at zero, the Yard's chip counting its grid) are pure
functions in `utils/needsYou.ts` with vitest tables.

### Contrast

Two halves, one helper. `web/app/utils/contrast.ts` (PR-24) holds the
maths: luminance, ratio, compositing over a parent for a translucent
fill. `themeContrast.test.ts` (vitest) reads `main.css`,
`app.config.ts` and Nuxt UI's runtime `index.css` and
`plugins/colors.js`, and asserts `CONTRAST_FLOORS`: 4.5:1 for text,
3:1 for a mark against its panel (WCAG 1.4.11), and for dimmed text
3:1 on the page and 2.7:1 on the canvas and raised surfaces (PR-24).
On `main` it fails for the light theme's amber text (about
2.9:1) and green text (about 4.0:1), so it lands with the fix. Its model
is `scripts/brand_assets.py:99-146`, which checks four pairs of the
brand's tokens.

`web/e2e/contrast.ts` (PR-25) reads the drawn page in both themes: the
computed colour of each status word, muted line and main action, and of
the first opaque background behind it, compositing translucent layers on
the way up. It fails when the unit table's mapping is wrong for the Nuxt
UI in use, or when a component draws text on a tinted fill that the
table did not list. The suite's only computed-colour read today is
`rail-squares.spec.ts:78`.

### Keyboard and touch

Every control a row adds has a keyboard path and a touch path, each
tested: at 1440 x 900, `tabTo` reaches it with a visible ring, its keys
act, Escape closes what it opened and the focus returns where it was; at
390 x 844 with touch, a tap acts, `tapTarget` holds, and nothing is
reachable only on hover. Models: `sidebar.spec.ts:354` (the list's
keys), `palette-keys.spec.ts`, `dispatch-keys.spec.ts`, and
`phone.spec.ts:20` and `:70` (a phone's context, tab targets of 44 px).
Two rules of AGENTS.md apply to every such test: after a `goto`, wait
for something the listener's own component draws before the first key,
and press once; after picking from a menu, wait for its listbox to be
gone (`toHaveCount(0)`) before typing, and assert the field holds what
was typed (`toHaveValue`).

### Themes and viewports

`dayPage` is the one helper for both. A theme is set by writing the
app's own key, `localStorage['conductor-color-mode']`, before the page
loads, and waiting for `html`'s class. A `colorScheme` context does not
switch this app: `nuxt.config.ts:22` prefers dark whatever the system
says, and `theme.spec.ts:5-16` proves that a light system opens dark. A
check that switched themes by `colorScheme` would run dark twice. One
already did: `rail-squares.spec.ts:75` writes `nuxt-color-mode`, a key
the app does not read, so its light pass read the dark colour again;
PR-1 fixes it. For a page already open, `useTheme(page, theme)` writes
the key, reloads and waits for the class.

Both themes are run only where a colour is asserted: the walk's marks,
contrast, and the look of PR-24 and PR-26. Layout is asserted in the
default dark theme, since the themes share every box.

The windows: 1440 x 900, the suite's default; 1280 x 720 where a row's
claim is about a laptop (the sidebar's list, the Yard's tiles); 390 x
844 with touch, the suite's phone (`phone.spec.ts:20`). The UX review
shot its phone at 400 px; 390 is the stricter, and a row sized at 400
checks its claim at 390. A fresh context opens the sidebar at its
default width (`utils/sidebar.ts:62`; #908, merged, raised the minimum,
and PR-7 returns it to 200 px).

### What is not done, and why

**No pixel baselines in CI.** The terminals are WebGL canvases, drawn
differently by each GPU and driver; fonts hint differently on CI's
ubuntu-22.04 runner, under WSL and on the Mac, so each platform would
need its own baselines; ages tick; ids, ports and link tokens are
random; `AttentionBadge.vue:34` pulses, menus animate, and the run page
redraws on a 5 s tick. And a stream that changes every screen would
regenerate and commit pictures in every pull request. The gate is the
measures, words, attributes and colours above. Pictures are for people:
the look capture of PR-38 attaches them to a report a reviewer opens,
and it never fails a run.

**No component-test level.** vitest here runs in node with no DOM and no
Vue SFC plugin, and a Nuxt UI component needs a Nuxt app (auto-imports,
app config, colour mode, icons). Mounting one would take
`@vue/test-utils`, `@nuxt/test-utils` and `happy-dom`: three new
dependencies, their notices and a line each in `docs/dependencies.md`.
The stream keeps every decision in a pure function, which vitest tests
whole, and leaves drawing to a real browser, which is the only place a
cut label or an overlap exists.

**No accessibility library and no fake clock.** Contrast is computed by
the stream's own small helper, from the app's own tokens. Playwright
1.44.1 has no `page.clock`; ages are matched by pattern and moved with
`ageBy` where a width depends on one.

### How a test is proved able to fail

A test that has never failed proves nothing. For each new or changed
test, the row's pull request body names the change that makes it fail
and quotes what it printed:

- A test of a defect or of a measure the UX review took is written first
  and run against `main`, where it fails (P8's `topAtMost(160)` prints
  about 400 px; D2's `noOverlap` over the run's header names a count
  badge under a button). The body quotes the failure on `main`.
- A test of new behaviour is run once with the row's change reverted in
  the one file that decides it (the function, the attribute, the class),
  and the body quotes the failure. The revert is not committed.
- The ground proves itself: `layout.spec.ts`'s bad pages, `day.spec.ts`
  run once with a part's stub variable taken off, `stubs.spec.ts`
  against the real server.
- A spec that loses an assertion because its proof moved says where the
  proof went (next part). A proof that is lost is named in the body with
  the reason.

### Proofs that move

No proof is dropped silently. The moves the stream makes:

- The `data-row-state` reads of `running` after an answer keep their
  attribute in PR-3 and move to `/^(working|finished|running)$/` in
  PR-14 (nine named there).
- The counts read by shape on the shared server
  (`sidebar.spec.ts:106` and `:111`) stay by shape; exact numbers are
  proved on the day by `counts.spec.ts` (PR-5, PR-13).
- The specs that answer through the Yard's queue (`approval.spec.ts`,
  `askwait.spec.ts`, `choices.spec.ts`, `codexworking.spec.ts`,
  `decision-mirror.spec.ts`, `question.spec.ts`) answer through the
  sidebar's row or the tile's own bar in PR-18. `yard-queue.spec.ts` is
  deleted in PR-18; its #360 proof (the grid is not shrunk once nothing
  waits) moves to `yard-grid.spec.ts`, and the queue's "Answered" lines
  go with the queue (owner decision 8).
- The specs that read the Events page (`events.spec.ts` and those that
  open its tabs) move to Agents, Alerts and History in PR-27 to PR-29.
- Words a spec looks for (empty states, names) move with the row that
  changes them, and the old words are asserted gone.

### The rules for tests that do not flake, as they bite here

All of AGENTS.md's rules apply. These are the ones this stream meets at
every turn:

- **Wait on the mechanism.** A measure is made after the thing the row
  draws is visible and the fonts are loaded, never after a sleep. A
  helper that matched nothing fails.
- **Check again after a redraw the test causes.** Every layout and walk
  check is made, then the test folds a section, switches the theme or
  resizes the window, and checks again: a bug that only a redraw shows
  then fails every run.
- **Count only what the test made.** Things are found by name and ref,
  never by a list's length; a measure of how much fits is made on
  `busy`; the `day` fixture fails the test that left something behind.
- **Read the facts of one answer at one moment.** The counts are read as
  one tuple; a walk reads the five sessions from one page load.
- **Wait for a state that holds.** The day's states hold until its end
  (`day.spec.ts` checks they survive viewers attaching); a state that
  only flashes by is proved in vitest.
- **A starting value is not a result.** A threshold the page meets
  before the row's change draws anything is no proof: each measure is of
  the thing the row draws, read once it is drawn.
- **A menu that has closed is not gone.** Wait for the listbox to be
  gone before typing.
- **A key before its listener is lost.** Wait for the layout's root,
  press once, give the effect the expect's bound.
- **A test's timeout bounds a hang, not its steps.** Each step adds its
  waits' caps with `budget()`.
- **End what a test started before its directories go.** The day's
  teardown stops runs and sessions before its server and its root.
- **One Playwright run per worktree.** Load for a flake check comes from
  a Go race package beside the spec, never a second Playwright run.

### Keeping the e2e job inside its hour

The e2e job already takes 48 to 65 minutes on pull requests merged on
2026-11-14 and 15 (#892 48, #895 60, #905 65), beside a go job of 46 to
59. The stream holds its cost down five ways:

1. **Seed once.** The day is built once per worker and shared,
   read-only, so a row's checks cost page loads, not sessions and runs.
2. **One page load per surface and width.** Every assertion a row makes
   on a surface is made on one load; layout measures take milliseconds.
   Themes are doubled only where colour is asserted.
3. **Move, do not add.** Where a spec already makes a state, the new
   assertion replaces the old one in place (the queue's seven specs in
   PR-18, the Events specs in PR-29), and a spec whose subject goes goes
   with it.
4. **One `budget()`.** `fixtures.ts` exports `budget(ms)`, which raises
   the running test's timeout by the caps of the waits a step is about
   to make (`palette-rows.spec.ts:24-27` is the model). Nineteen files
   have their own today, one of them exported
   (`decisions-helpers.ts:29`); new specs use the one.
5. **Say the minutes.** Each pull request of the stream states the e2e
   job's minutes on `main` and on its head, read from its checks. The
   stream's whole cost is expected at five to eight minutes: the day one
   to two, the walk about one, the counts under one, the layout checks
   seconds a row, and PR-18 and PR-29 remove more than they add.

The e2e job is already longer than the go job (60 minutes against 59
on #895, 65 against 46 on #905), so PR-39 splits it into two shards at
once, cut beside PR-1.

### By hand

What no test holds, for a person to look at on the day
(`CONDUCTOR_E2E_DAY_OPEN=1`, then PR-38's capture) at 1440 x 900 and 390
x 844, in both themes. Each row's section names its own; together:

- The five marks: told apart at a glance at 200 px and on a phone, in
  both themes (a test reads the key and the colour, not whether a ring
  reads as a ring).
- Needs you: the question and its buttons are the first thing the eye
  meets; the fine print is one click behind (P21).
- The sidebar's answer panel standing beside its row over the page (P5),
  and the six tiles against the phone's tab bar (P6).
- The run's header and task table read at a glance (P7); the ticket's
  strip while the page scrolls (P9).
- The brand's neutrals beside the forest button, and the terminal's
  sixteen colours on ink (P14).
- The line diagram (P15).
- The welcome's and the empty pages' words, read as a newcomer reads
  them (P17, P23), and the connect page (P18).
- Every new control's focus ring, seen (a test proves one is drawn).
- The desktop app's own windows, which no e2e spec reaches.

## Out of scope

What a reader might look for in this stream and will not find, and
where each lives:

- **The desktop app's own windows.** The splash, the tray and its
  menu, the setup and log windows, and Settings as the desktop app
  shows it (`desktop/src`) were not judged by the UX review and are
  not changed here. The one question of them the stream meets is the
  splash's ground (`desktop/src/splash.ts`, zinc today), an owner
  decision of PR-24.
- **The guest's join page in depth.** `pages/join/[token].vue` and
  `JoinDecide.vue` change only where a shared piece reaches them: the
  size line of D10 (#908), the row class of D1 (#912), the status
  marks (PR-3), the decision link's tab name (PR-5) and the kind marks
  and a person's words (PR-21). The page's own layout was not
  reviewed.
- **The phone beyond what fits.** Every row is checked at 390 x 844
  with touch, and keeps what fits there: the phone's list keeps a
  question whole, its Yard tab keeps its count, a run's tasks stay a
  list, its bar keeps its places. A design of the phone's own is not
  here; the mobile acceptance work is #653's, and a native app is
  #160.
- **Themes.** The app keeps two themes, light and dark, on the brand's
  neutrals (PR-24). Themes as files and a colour per agent are #346;
  PR-24's scale is the set of tokens such a theme would override.
- **Search in the shortcuts dialog.** #708. No row of this stream
  touches the dialog.
- **Read-failure alerts in amber.** About sixty `UAlert`s say in amber
  that a read failed or a part is not set up. They stay as they are;
  PR-26 turns neutral only the standing badges that ask nothing of
  anyone, such as Agents' "Not installed". A change to the alerts
  would be an issue of its own.
- **The switchyard's own pages.** `internal/api/switchyard_page.go`
  draws them, dark always and on zinc (`docs/design/brand.md`). PR-24
  leaves them as they are (an owner decision there), and PR-37's rules
  name them as apart.
- **A count of what needs you on the server.** `useNeedsYou()` (PR-4)
  makes the one list and the one number in the app, from reads it
  already makes: the sessions' attention, the decisions, the inbox. No
  route counts it, so a client that is not the app's screens (the CLI,
  the desktop's tray) does not show it. A server count would be a
  protocol change, in an issue of its own.
- **A list of answers across sessions.** The Yard queue's "Answered"
  goes with the queue (PR-18); a session's Activity and its record in
  History keep who answered. A feed of answers would need a new event.
- **A rail of page icons beside the list.** Not proposed: it would
  cost the width PR-7 gives back.
- **A "recommended" flag on an agent's choices.** PR-19 draws a reply
  choice filled only when its label ends "(Recommended)", read in the
  app. No protocol change.
- **The design system's published pages.** They are kept outside this
  repository, and no pull request can change them. PR-38 brings the
  capture that makes them into the repository; publishing from it is
  a step by hand at a release, in the release checklist.
- **Pixel baselines and component tests.** No screenshot is compared
  in CI: layout is held by measures. No component-test level is added
  (it would need three new dependencies): a function that decides is
  tested in vitest, what is drawn in Playwright.

## Boxcar constraints

1. The engine starts an attempt only through `crew.Launcher`: not
   touched; no row starts an attempt. PR-2 changes what a member's
   readiness reads (a `done` an Enter cleared is not ready), PR-35
   shares that wait (`AwaitReady`) for a launch's first message, and
   PR-34 adds to the crew list's summary a shape read from the saved
   crew; none is a new path into the engine.
2. `session.Local` drives a `session.Process`: honoured; PR-2's
   `Turns` and its Enter rule, and PR-35's `PersonTyped` and
   `FirstMessage`, live in `Local`, and the first message is typed
   through `Submit` as a crew's prompt is. Nothing reaches a pid or a
   tty.
3. An attempt's context is files: not touched; no row adds to what an
   attempt needs. The first message (PR-35) belongs to a session a
   person launches, is kept in memory only until it is typed, and is
   then in the session's Activity like every line Conductor types.
4. The agent talks to the server over the network: not touched; PR-2
   reads turns from the reports agents already send (their hooks, a
   title, a status), and no new path between an agent and the server is
   made.
5. Checks go through `internal/proc`: not touched; no row runs a check.
6. Platform splits stay platform tags, the UI shows a capability: no tag
   is added; the welcome (PR-30) offers the agents the catalog says are
   available, read from `GET /api/catalog`, and every row draws the same
   on every platform.
7. Evidence rows gain `where`: not touched; no evidence row is added.
   PR-21 changes only the words of a run decision's title and brief in
   `internal/api`, and PR-34 a field of the crew summary that is given
   only when asked for.
8. `hostagent` is a launch site: honoured. PR-2's Enter rule runs on the
   host's own `Local`, and the server's hosted copy learns `Turns` and
   keeps a shown `done` by the same sources, with no frame or field of
   the host link changed; a hosted launch takes no first message
   (PR-35), since the host's command has no such flag, so nothing binds
   a session on the server in a way the host's launch would break.

## Open-core checklist

- [x] Article 1: no licence, plan or entitlement check in any row; every
  row ships whole in this repository.
- [x] Article 2: every screen the stream changes is the free build's,
  useful alone, with no account, hosted service or network beyond
  today's.
- [x] Article 3: nothing is gated; there is no flag a fork could flip.
- [x] Article 4: no control is weakened: Stop is never moved, an action
  folded behind a menu keeps its key and its palette row, what an
  approval authorizes stays in view in one line, and no approval is
  skipped, merged or answered for a person.

## The PRs

Each PR is one issue and one branch cut from `main`, a draft PR at its
first commit, the checks for its surfaces before every push (the
chapter Tests says which), its body ticking the open-core checklist
(articles 1 to 4), naming the agent that made it and quoting, for each
new or changed test, what it printed when it was made to fail; each
leaves `main` working and green on its own (a function without its
caller is reached by its tests, a component without its later parts
says nothing of them). Parallel is derived from Depends on: two rows
are parallel when neither is an ancestor of the other, and both cells
name each other; where two such rows share a file at lines apart, the
one cut second rebases on the other.

What is on `main` and what is not: the app's screens as the UX review
read them, the inbox and its routes, the durable decisions, the
catalog, the crews and their runs, Dispatch and History are, at
`9fb98b06`, and #908's sidebar, welcome and screen texts since it
merged (`f3d39b1f`); PR-1, PR-2, PR-4, PR-5, PR-12, PR-32 and PR-39
build on them alone. Six open pull requests are not (#899, #907, #909,
#911, #912 and #913, in "What must merge first"): a row that changes a
file one of them holds is cut once it has merged. Issue #716 (Codex's
tool-approval prompt shown as working) is asked to be fixed before
PR-3 merges.

| PR | Title | Contains | Touches | Tests | Depends on | Parallel with |
|---|---|---|---|---|---|---|
| PR-1 | e2e: a seeded day, layout assertions, one theme and viewport helper | The seeded day (a server of its own per worker, named read-only parts, a guard that fails the test that leaves something), STUB_TURN in the stub, layout.ts measures, inbox and run stubs, dayPage and useTheme, one exported budget(), the rail-squares light-theme fix | web/e2e (new own-server.ts, day.ts, layout.ts, inbox-stub.ts, run-fixtures.ts, day.spec.ts, layout.spec.ts, stubs.spec.ts; changed fixtures.ts, decisions-helpers.ts, stub-agent.sh, rail-squares.spec.ts), docs/checks.md | day.spec.ts (each part holds what it promises, states survive viewers, the leftover guard, dayPage themes and touch); layout.spec.ts (each helper passes a good page and fails a bad one); stubs.spec.ts (stubs held to the real server); rail-squares.spec.ts light pass really light | none (open PRs 899 and 907 touch fixtures.ts and stub-agent.sh in other hunks; plain merge) | PR-2, PR-20, PR-32, PR-35, PR-39 |
| PR-2 | session: a turn's start and end are said in the listing | Info.turns learned from the agent's own reports; an Enter after a shown done shows the empty state with input; a done after a done keeps the first; the hosted copy and the join reply carry both; docs/protocol.md; the shared status-table.json | internal/session (info.go, local.go, turn_test.go, status_table_test.go, testdata/status-table.json), internal/signal/hosted.go, internal/hostagent tests, internal/api/links.go, internal/crew/run_test.go, docs/protocol.md, web/app/composables/useSessions.ts | Go race tests: Enter after done, done after done, turns learned, the shared table, the hosted copy, the host told, crew readiness after an Enter, the join's turns | none (shares useSessions.ts and docs/protocol.md with open #907 at lines apart) | PR-1, PR-4, PR-5, PR-6, PR-12, PR-20, PR-21, PR-22, PR-23, PR-24, PR-25, PR-27, PR-30, PR-31, PR-32, PR-39 |
| PR-3 | web: one status language for sessions | utils/status.ts (sessionStatus, STATUS_LOOK), StatusMark.vue (shaped for PR-33's SVG form), useNow; every session surface draws one of five marks; AttentionBadge and SessionStatusBadge deleted; the done event mark gives way; docs/design/system.md begun with its Status section | web/app/utils (status.ts, sidebar.ts, charts.ts, events.ts), web/app/components (StatusMark, SidebarSessionRow, SidebarRailSquare, SidebarSection, SidebarSharedList, SessionTile, AttentionBar, CarouselStrip, CommandPalette, EventMarkBadge, TransportBadge; AttentionBadge and SessionStatusBadge removed), web/app/composables (useNow.ts, usePaletteRows.ts, useSessions.ts), web/app/pages (yard, sessions/[id], roundhouse, join/[token]), web/e2e (status-walk.spec.ts, codexworking, palette-reach, sidebar specs), docs/design, docs/features.md | vitest status.test.ts (every rule, the shared table, the distinctness guard) and moved sidebar/charts/events tests; Playwright status-walk.spec.ts per surface at 1440 and 390 in both themes, guest page, host away; codexworking real-signal marks | PR-1 (day states, dayPage, useTheme, probeColour), PR-2 (turns, the cleared done, the table); open PRs #907 (the app's files) and #912 merged; issue #716 fixed first | PR-4, PR-5, PR-6, PR-12, PR-20, PR-21, PR-22, PR-23, PR-24, PR-25, PR-27, PR-30, PR-31, PR-32, PR-35, PR-39 |
| PR-4 | web: the pages in one table; one list and one number for what needs you | utils/pages.ts (every destination with its one sentence) read by the nav, rail, phone bar and palette; utils/needsYou.ts with useNeedsYou (tasks from the inbox's task items; no caller yet but its test); the nav's tooltips and the palette's page lines | web/app/utils/pages.ts (new), web/app/utils/needsYou.ts (new), web/app/composables/useNeedsYou.ts (new), web/app/layouts/default.vue, web/app/components/BottomBar.vue, web/app/composables/usePaletteRows.ts, web/e2e/sidebar.spec.ts | vitest pages.test.ts (today's lists, active rules, chords agree with the shortcuts) and needsYou.test.ts (the day's cases, the inbox lagging attention, unread, failed, switchyard); Playwright tooltip and palette-line test in sidebar.spec.ts | PR-1 (dayPage, tabTo); builds on #908's header (merged); PRs 899 and 912 rebase on usePaletteRows.ts | PR-2, PR-3, PR-12, PR-19, PR-20, PR-24, PR-25, PR-32, PR-35, PR-36, PR-39 |
| PR-5 | web: one amber number, a row that leaves, one name | the one number on the nav, rail, phone tab, palette, tab title, favicon and pill; the Yard's nav badge and rail chip gone; needsRowShown hides the row at zero; the sidebar section's header says what it leaves out ("Needs you 6 · +3 on the page", plans, tasks and stopped runs' records) until PR-13; the phone's Yard count neutral; the name Needs you everywhere but the decide-only page and the palette's second group, a decision link's tab and a ticket's link included | web/app/layouts/default.vue, web/app/utils/needsYou.ts, web/app/components/SidebarRail.vue, SessionSidebar.vue, BottomBar.vue, NeedsYouPill.vue, DispatchDecisions.vue, web/app/composables/useNeedsCursor.ts, useAttention.ts, usePaletteRows.ts, useShortcuts.ts, web/app/pages/decisions.vue, yard.vue, join/[token].vue, README, docs/features.md, docs/plans/decision-inbox.md, web/e2e/counts.spec.ts (new), sidebar.spec.ts, phone.spec.ts | vitest needsRowShown and sectionHeader tables; Playwright counts.spec.ts on busy (every reader, the favicon's dot among them, equals the server's oracle at one poll, every number computed from the server's refs, the section's header names what it leaves out, all move together on the test's own question, again after reload) and the row-leaves test on its own server with inbox-stub unread, failed and switchyard | PR-4 (pages.ts, needsYou.ts); PR-1 (day busy part, ownServer, dayPage, inbox-stub); open PR 907 touches join/[token].vue | every row but PR-1, PR-4, PR-6, PR-7, PR-8, PR-13, PR-14, PR-15, PR-21, PR-23, PR-26, PR-28, PR-29, PR-30, PR-31, PR-37, PR-38 |
| PR-6 | web: the page of what needs you, with nothing on it | Needs you at zero: one column, when the last item closed, the four kinds, "Decided in the last day" with a link to History; "Pick an item" only with an item; kindLook; Open on your phone moved to the layout and into your menu | web/app/pages/decisions.vue, web/app/utils/inbox.ts, web/app/utils/actions.ts, web/app/components/OpenOnPhone.vue (new), web/app/composables/useOpenOnPhone.ts (new), web/app/components/SidebarHeaderMenus.vue, web/app/layouts/default.vue (one mount), web/e2e | vitest kindLook, atZero, pickShown, lastClosedWords, accountItems with phone; Playwright decide-zero.spec.ts (stubbed inbox, 1440 and 390 touch, failed and switchyard reads) and the menu path in decision-page.spec.ts | PR-5 (pages.ts name, the row that leaves), PR-1 (dayPage, layout.ts, inbox-stub.ts), #912 merged (rows and mount of decisions.vue) | every row but PR-1, PR-4, PR-5, PR-7, PR-8, PR-13, PR-14, PR-15, PR-21, PR-23, PR-26, PR-28, PR-29, PR-30, PR-31, PR-37, PR-38 |
| PR-7 | web: sidebar rows that fit: gutters, the row, the header | the sidebar back to 200 px (drag to at least 280); header = mark, name, your menu with Alerts (AlertsPanel, a slideover of the switches) and Collapse to the rail inside it; 8 px gutters; name on one line, initials and the status word on the meta line, choices at the row's edge; a run in Needs you shows its askers and 'and 2 more' | web/app/utils/sidebar.ts, web/app/layouts/default.vue, web/app/components/SidebarHeaderMenus.vue, AlertsPanel.vue (new), SidebarSessionRow.vue, SidebarPrompt.vue, SidebarRunBlock.vue, SessionSidebar.vue, README, docs/features.md, web/e2e/sidebar.spec.ts, chat.spec.ts, rail-squares.spec.ts | vitest widths at six window sizes, blockRows, focusRows' more row; Playwright header at 200 px (rewrite of #908's), your menu holds Alerts and the collapse, a row fits 200 px, a run shows who asks; width group and collapse clicks moved | builds on #908 (merged: header, SIDEBAR_SIZE, specs it rewrites); PR-3 (marks and words); PR-6 (your menu's items); PR-1; owner decision 6 | every row but PR-1, PR-2, PR-3, PR-4, PR-5, PR-6, PR-8, PR-13, PR-14, PR-15, PR-26, PR-28, PR-29, PR-37, PR-38 |
| PR-8 | web: a question on two lines in the narrow sidebar, whole beside its row | below 280 px one decision (narrowSidebar) clamps the question to two lines and opens a non-modal panel beside the row on focus or Answer; 1 to 9 still answer; the spec uses (47 on main) moved onto rowAnswer | web/app/utils/sidebar.ts, web/app/components/SessionSidebar.vue, SidebarSessionRow.vue, SidebarRowPanel.vue (new), SidebarPrompt.vue, web/e2e/fixtures.ts, sidebar-question.spec.ts (new), and 13 specs (answered, question, sidebar, approval, choices, claudedialog, askwait, codexworking, decision-mirror, held, phone, dispatch-evidence, hosted-question) | vitest narrowSidebar and panelRow; Playwright sidebar-question.spec.ts: a 500-byte question two lines in the row and whole in the panel, digits and J K, the #596 reply-field rule through the panel, touch, five asking rows in view, whole again at 300 px | PR-7 (200 px and the row); PR-1 (helpers, busy part); owner decision 7 (the clamp ships with its panel at 280 px) | every row but PR-1, PR-2, PR-3, PR-4, PR-5, PR-6, PR-7, PR-13, PR-14, PR-15, PR-26, PR-29, PR-37, PR-38 |
| PR-9 | web: a run header that fits | P7 (header), D2: the name whole, one ticket chip, Add agent and Share in one menu, Stop neutral at rest; row two at every width with the bar, the one sentence, the pause, people, Logins, who launched, the uptime in sans; the Ledger and legacy runs added to run-fixtures.ts | web/app/components/CrewRunHeader.vue, web/app/utils/crewWords.ts, web/app/utils/status.ts (runBarParts), web/app/pages/runs/[run].vue, web/e2e/run-fixtures.ts (PR-1's, extended), web/e2e/run-header.spec.ts (new), web/e2e/{tasks,tasks-resume,manifest,decisions,crew-people,switchyard,graph}.spec.ts, docs/features.md | vitest runTicketChips, runBarParts, runStatusLine with a gate's decision; Playwright run-header.spec.ts (name uncut and nothing overlapping at 1440 and 390, the menu by key and touch, Add agent on the legacy run and disabled on the managed one, the needs part equal to needsYou(), Stop not red and the needs segment amber in both themes, re-checked after the 5 s tick); seven specs moved off the badges | PR-10 (runStatusLine, taskStatus, STATUS_LOOK; cut before this row), PR-1 (layout.ts, dayPage, tabTo), open PRs 899 and 912 merged (the header's lines; Add agent offered by runOffer) | every row but PR-1, PR-2, PR-3, PR-4, PR-10, PR-11, PR-26, PR-33, PR-34, PR-37, PR-38 |
| PR-10 | web: one status language for runs and tasks | taskStatus, memberStatusOf, runStatusLine(run, sessions, decisions, now) (with Dispatch's summary variant) and TASK_LOOK in utils/status.ts; task rows, graph nodes and legend, mini DAG, the run card (the header left to PR-9), Dispatch's run card, the run page's palette rows, the people page and the Crews outcomes speak PR-3's language | web/app/utils (status.ts, tasks.ts, runs.ts, crewWords.ts, dispatchEvidence.ts), web/app/components (RunTasks, CrewGraph, CrewGraphTaskNode, CrewGraphNode, CrewGraphList, CrewMiniDag, CrewRunCard, DispatchRunCard, CrewRunsPanel, CrewsTable, CrewRunBars), pages/crews/[[id]].vue (the bars' legend), web/app/pages (runs/[run], people/[name]), web/e2e (run-status.spec.ts, tasks, decisions, graph specs) | vitest taskStatus from task-commands.json, memberStatusOf, runStatusLine (a gate's decision that holds no task counted), taskStatus and needsYou agreeing on one run, the task-mark guard, moved runs/tasks/dispatchEvidence/crewWords tests; Playwright run-status.spec.ts on a stubbed run in both themes and at 390, real-engine needs marks in tasks.spec and decisions.spec | PR-3 (status.ts, StatusMark, the guard), PR-4 (needsYou.ts: the test that holds taskStatus and needsYou() to one answer), PR-1 (run-fixtures.ts, dayPage, useTheme, probeColour); open PR #912 merged (CrewGraph*, runs/[run].vue, runs.ts) | every row but PR-1, PR-2, PR-3, PR-4, PR-9, PR-11, PR-16, PR-26, PR-33, PR-34, PR-37, PR-38 |
| PR-11 | web: task rows as a table that says the state once | P7 (rows): one grid template for every row (state, task, who, what, actions), the attempt said once, an owner only when the task names one, words uncoloured; Cancel, Copy branch, Ask Brakeman why behind a row menu; taskRow and taskWords' attempt option | web/app/components/RunTasks.vue, web/app/utils/tasks.ts, web/app/utils/tasks.test.ts, web/e2e/run-tasks.spec.ts (new), web/e2e/{decisions,crew-people,brakeman-cards,manifest,tasks,dispatch-links}.spec.ts, docs/features.md | vitest taskWords option and the taskRow table; Playwright run-tasks.spec.ts (columns aligned at 1440, one mark and one attempt a row, nothing red at rest in both themes, the row menu by key and touch with the closed-menu wait, the fold at 390); six specs moved | PR-9 (the Ledger run in run-fixtures.ts; the people say the acceptance owner once), PR-10 (taskStatus, StatusMark), PR-1 | every row but PR-1, PR-2, PR-3, PR-4, PR-9, PR-10, PR-33, PR-34 |
| PR-12 | web: one toolbar on a Dispatch project, and columns that line up | P8: the description as one line (descriptionLine), one toolbar with Filter behind a popover and chips (filterChips, parent and deleted shown), Views as a menu naming the view applied, fixed tracks for milestone, priority, owner, assignees and age with empty cells drawn, a palette row Filter the tickets… | web/app/pages/dispatch/[project]/index.vue; web/app/components/DispatchFilters.vue, DispatchViewsBar.vue, DispatchTicketList.vue; web/app/utils/dispatchQuery.ts (+test), markdown.ts (+test), dispatch.ts; web/e2e/dispatch-toolbar.spec.ts (new), dispatch.spec.ts, dispatch-views.spec.ts, palette-rows.spec.ts; docs/plans/dispatch.md, docs/features.md | vitest filterChips and descriptionLine; Playwright on the seeded day: first ticket at most 160 px down and all 14 rows in view at 1440 x 900, at least 12 at 1280 x 720, five tracks aligned within 1 px before and after a selection, priority and age aligned and nothing sideways at 390 x 844, chips in and out of the address; moved specs open Filter or Views first | PR-1 (the day's dispatch part, dayPage, layout.ts) | every row but PR-1, PR-23, PR-29, PR-30, PR-31, PR-37, PR-38 (palette-rows.spec.ts is shared with PR-4, PR-10 and PR-13 in other tests; docs/features.md with every row) |
| PR-13 | web: one inbox: the sidebar lists everything that needs you | Needs you lists questions, plans, tasks and decisions with their kind marks; a waiting run leaves Running in the same call; the section counts the nav's number and its "+N on the page" words go; the palette's two groups become one Needs you group | web/app/utils/sidebar.ts, web/app/utils/needsYou.ts, web/app/components/SessionSidebar.vue, SidebarInboxRow.vue (new), SidebarRunBlock.vue, web/app/composables/usePaletteRows.ts, web/app/utils/decisions.test.ts, docs/features.md, web/e2e/counts.spec.ts | vitest sidebarModel with the waiting runs, item focus rows, the palette's one group; Playwright counts.spec.ts extended: section equals the oracle with no "on the page" words, each kind once with its mark, the failed run absent from Running at the same moment, failed and cut inbox reads | PR-5 (number, name, counts spec); PR-6 (kindLook); PR-7 (asking members first) and PR-8 (rows short enough for nine); PR-1 | every row but PR-1, PR-2, PR-3, PR-4, PR-5, PR-6, PR-7, PR-8, PR-14, PR-15, PR-26, PR-29, PR-37, PR-38 |
| PR-14 | web: finished apart from working | Running splits into Finished and Working, in the order Needs you, Finished, Working, Shared, Exited; a run sits once by its busiest member and says '2 working · 2 finished'; old folds read; the palette's groups follow | web/app/utils/sidebar.ts, web/app/components/SessionSidebar.vue, SidebarSection.vue, SidebarRunHeader.vue, SidebarRailSquare.vue, web/app/composables/usePaletteRows.ts, docs/features.md, web/e2e/sidebar.spec.ts, askwait, choices, codexworking, phone, question specs | vitest sections per status key, run placement and subtitle, old fold mapping, palette groups; Playwright two sessions set working and done land in their sections in order and move at once; nine 'running' assertions widened to the live states | PR-3 (sessionStatus, STATUS_LOOK, useNow); PR-13 (waiting run's place); PR-2 so finished is true | every row but PR-1, PR-2, PR-3, PR-4, PR-5, PR-6, PR-7, PR-8, PR-13, PR-26, PR-37, PR-38 |
| PR-15 | web: the pages as six tiles | the full sidebar's foot as six tiles (Needs you, Yard, Crews, Dispatch, Brakeman, More) about 116 px tall; Brakeman's slot held while unknown; Needs you tile stays at zero; the phone's More gains Brakeman; the rail keeps every page | web/app/utils/pages.ts, web/app/components/SidebarTiles.vue (new), web/app/layouts/default.vue, web/app/components/BottomBar.vue, README, docs/features.md, web/e2e/sidebar.spec.ts, phone.spec.ts, settings.spec.ts | vitest navTiles and phoneMore; Playwright foot at most 120 px with six uncut tiles, More by click and Enter, active rules, the held Brakeman slot (More does not move), the zero tile; phone More with Brakeman at 44 px | PR-5 (name, number, tile stays at zero); PR-13; PR-4; PR-7; PR-1 | every row but PR-1, PR-2, PR-3, PR-4, PR-5, PR-6, PR-7, PR-8, PR-13, PR-29 |
| PR-16 | web: a ticket's strip of sections; a plan folds once approved | P9: a sticky strip of nine sections with states on five (ticketSections, ticketWaits over useNeedsYou), sections with ids and scroll margins, the aside under the strip, an approved plan that holds folded to one line (planFold) with Show the whole plan, the Arguments line and People's command lines removed | web/app/components/DispatchTicketPage.vue, DispatchTicketStrip.vue (new), DispatchPlan.vue, DispatchPlanBody.vue, DispatchPeople.vue; web/app/utils/ticketSections.ts (new, +test), dispatchPlans.ts (+test); web/e2e/ticket-strip.spec.ts (new), dispatch-delivery.spec.ts; docs/plans/dispatch-evidence.md, docs/features.md | vitest ticketSections (order, words, waits filtered by run and ticket, 50+) and planFold (stale never folds); Playwright on its own server: strip at the scroller's top after a real scroll and after a live comment, sections land below the strip with focus, states follow a gate answered with no reload, the fold and its stale reopening, one command line per member, opaque strip in both themes, 390 x 844 nothing sideways, none in Review | PR-10 (runStatusLine summary variant), PR-4 (useNeedsYou); open PR 912 merged (DispatchPlanBody.vue, dispatchPlans.ts and its test) | every row but PR-1, PR-2, PR-3, PR-4, PR-10 |
| PR-17 | web: Yard tiles lead with the name; the Roundhouse is a view of the Yard | P10 and P12's first move: the tile's header is its name, its state (StatusMark) moves to a foot cell outside the slot, chips All, Needs you, Working, Finished (and Running while it has a session or is chosen) from utils/wall.ts, the Needs you chip counts the grid's sessions from needsYou() and says so, YardViews (Grid and Roundhouse) with /roundhouse, its name, G R and Alt+R kept and its nav and More rows gone | web/app/components/SessionTile.vue, EventMarkBadge.vue, YardViews.vue (new); web/app/pages/yard.vue, roundhouse.vue; web/app/utils/wall.ts, pages.ts; docs/features.md, README.md; web/e2e/yard-tiles.spec.ts, yard-views.spec.ts (new), palette-reach, palette-rows, sidebar, phone, chords specs; web/app/utils/wall.test.ts, pages.test.ts | vitest yardFilter, yardCounts, yardChips and filterOf against needsYou() and yardRoute; Playwright yard-tiles.spec.ts (names uncut and state in the foot on the seeded day at 1440 and 390, chips by id after a state change, the chip's neutral number in both themes, the phone's chip row) and yard-views.spec.ts (the view switch, nav and More without the Roundhouse, G R and /carousel); five existing specs edited, none lost | PR-3 (sessionStatus, StatusMark, useNow), PR-4 (pages.ts, needsYou), PR-1 (day, dayPage, layout.ts); open #907 (yard.vue) merged | every row but PR-1, PR-2, PR-3, PR-4, PR-18, PR-29 |
| PR-18 | web: the Yard is its grid | P22: WallQueue.vue and its Answered list deleted, the grid takes the 340 px at every width, a tile that asks shows its question's start in its foot (tileAsk on rowPrompt), the pill's Enter on /yard expands the tile (pillTarget), J, K and Enter leave the Yard's keys and KeyJ and KeyK leave ALT_PASSTHROUGH_CODES, QuickReplyBar choices get choiceTitle | web/app/components/WallQueue.vue (deleted), SessionTile.vue, NeedsYouPill.vue, QuickReplyBar.vue (one line); web/app/pages/yard.vue; web/app/composables/useShortcuts.ts; web/app/utils/wall.ts, needsCursor.ts, attention.ts; docs/features.md, README.md; web/e2e/yard-queue.spec.ts (deleted), yard-grid.spec.ts (new), approval, askwait, choices, codexworking, decision-mirror, question, chords specs; vitest useShortcuts, wall, needsCursor, attention | Playwright yard-grid.spec.ts (no queue, grid box and tile sizes against bestGrid on the seeded day, rail and phone, the tile's question start after a re-ask, the pill's Enter expanding in place) and chords.spec.ts (Alt+J and Alt+K reach the terminal); eight queue proofs moved to the tile and the expanded bar, four lost by design; vitest WALL_SHORTCUTS, tileAsk, pillTarget, choiceTitle | PR-17 (the foot cell), PR-1; open #907 (yard.vue, QuickReplyBar.vue, attention.ts) merged; sequenced with PR-8 on four shared specs | every row but PR-1, PR-2, PR-3, PR-4, PR-17 |
| PR-19 | web: a session page where the terminal has the room | Inspector closed until opened (one remembered choice), meta line agent · folder · branch, outline Share, the connection as an icon while healthy, a choice filled only when its label ends (Recommended); D7's gone page with a link to the record; D9's whole name on a phone | web/app/pages/sessions/[id].vue, web/app/components/TransportBadge.vue, QuickReplyBar.vue, WallQueue.vue (if PR-18 has not removed it), web/app/utils/attention.ts, utils/sessions.ts, utils/transport/look.ts (new), web/e2e/session-page.spec.ts (new), inspector-rail.spec.ts, hscroll.spec.ts, README.md, docs/features.md | vitest for recommendedChoice, sessionHeadline, gonePage, transportLook; session-page.spec.ts at 1440 x 900 and 390 x 844 touch, both themes, rechecked after a redraw; inspector-rail and hscroll specs moved to the closed default | PR-1 (dayPage, uncut), PR-3 (StatusMark in the header and the gone page); open #907 (QuickReplyBar.vue, [id].vue, attention.ts) | every row but PR-1, PR-2, PR-3, PR-26, PR-36, PR-37, PR-38 |
| PR-20 | web: a run's decision: the question, one set of buttons, the fine print folded | DecisionCard's option rows dropped where DecisionRespond draws the buttons (controls prop), the recommended mark and tradeoffs on the buttons, the yes line above them, Details (open for a plan's approval), steps Asked, Your answer, Checked by Yardmaster, Applied with no loader, the modal titled A decision for you | web/app/components/DecisionCard.vue, web/app/components/DecisionRespond.vue, web/app/components/DecisionModal.vue, web/app/utils/decisions.ts, web/app/pages/decisions.vue (one attribute), web/e2e | vitest stepLook, step labels, detailsOpen, authorizesLine; Playwright decisions.spec.ts (1440), decision-page-durable.spec.ts (390 touch), decisions-gate.spec.ts; switchyard.spec.ts:696 must hold (a link keeps its rows) | #912 merged (the gate's plain words, decisions.vue) | every row but PR-21, PR-26, PR-37, PR-38 |
| PR-21 | web, api: the list and the item in a person's words | Kind marks on the rows (page and decision link), the oldest item opens by itself at lg, kind words and step titles in a person's words, the server's title and brief of a run's decision and the link's words, D12's briefLine and rowLine, a question's scopes under Details, Approve and Retry in forest | web/app/pages/decisions.vue, web/app/components/InboxItem.vue, web/app/components/JoinDecide.vue, web/app/components/DecisionModal.vue, web/app/utils/inbox.ts, web/app/composables/usePaletteRows.ts (one line), internal/api/inbox.go, internal/api/inbox_decisions.go, internal/api/lane_decisions.go, docs/protocol.md (one quote), web/e2e | vitest firstToOpen, briefLine, rowLine, new kind words and step titles; Go race TestADecisionItemIsInAPersonsWords and TestInboxDecisionQuestionOffersNoReply; Playwright decide-item.spec.ts (stubbed, 1440 and 390 touch, both themes for forest) and edits to decision-page.spec.ts and switchyard.spec.ts | PR-6 (kindLook, the Pick an item rule), PR-20 (controls, the modal's title), #912 merged | every row but PR-1, PR-4, PR-5, PR-6, PR-20, PR-26, PR-37, PR-38 |
| PR-22 | web: one shape for a page with nothing in it: the session pages and History | P23 first half and D13: PageEmpty, pageState, PageAbout; home, Yard, Roundhouse, the sidebar's line and History drawn in one shape and only after the first read; the Yard's not-found; History's caption to About; the table of every empty place | web/app/components/PageEmpty.vue, PageAbout.vue (new); web/app/utils/pageState.ts, history.ts; web/app/pages/index.vue, yard.vue, roundhouse.vue, history/index.vue; web/app/components/SessionSidebar.vue; web/e2e/empty-pages.spec.ts (new), welcome.spec.ts; docs/features.md | vitest pageState and historyNoneWords; Playwright empty-pages.spec.ts on an empty own server at 1440 and 390 in both themes, plus a held first read on the seeded day proving the welcome is never drawn | PR-4 (pages.ts about), PR-1 (ownServer, dayPage, layout.ts); #907 merged | every row but PR-1, PR-4, PR-23, PR-29, PR-30, PR-31, PR-37, PR-38 |
| PR-23 | web: one shape for a page with nothing in it: the rest, and About this page | P23 second half: Crews, Dispatch, a project (list, Board, Roadmap), the feed, Brakeman, Agents and PR-6's page in PageEmpty; reading and failed shapes; not-found for crew, project, ticket, run; Brakeman's and Agents' paragraphs fold to About | web/app/pages/crews/[[id]].vue, dispatch/index.vue, dispatch/[project]/index.vue, runs/[run].vue, brakeman.vue, agents.vue, decisions.vue; web/app/components/DispatchTicketPage.vue, EventFeed.vue; web/e2e/empty-pages.spec.ts, fixtures.ts (readAgain moved); docs/features.md | Playwright empty-pages.spec.ts extended: each page's three parts at 1440 and 390, an empty catalog by page.route, a project with no ticket on all three views, a failed first read with readAgain, four not-found routes, About on Brakeman and Agents | PR-22 (the shape), PR-12 (project page), PR-6 (the zero page); #899 and #911 merged | every row but PR-1, PR-4, PR-5, PR-6, PR-12, PR-22, PR-29, PR-30, PR-31, PR-37, PR-38 |
| PR-24 | web: the brand's own neutrals; contrast as a test | An eleven-step ink scale (sage at 100, ink at 900) as the neutral, a sidebar canvas tone, text-safe light amber and green, ink text on amber fills, Monaco and fallbacks off zinc, utils/contrast.ts with its floors table, the first designRules rule (no zinc) | web/app/assets/css/main.css, web/app/app.config.ts, web/app/utils/{contrast,monacoColors,monaco,terminalTheme,charts}.ts, eight Sidebar*/SessionSidebar components, docs/design (brand.md, two briefs), web/public/brand/tokens.css, scripts/brand_assets.py, web/e2e/theme-surfaces.spec.ts | vitest contrast.test.ts, themeContrast.test.ts (every token pair in both themes against the floors), terminalTheme.test.ts changed, designRules.test.ts zinc ban; Playwright theme-surfaces.spec.ts at 1440 and 390 in both themes | PR-1 (dayPage's theme switch); open PR 912 merged (SidebarSessionRow.vue) | every row but PR-1, PR-25, PR-26, PR-37, PR-38 |
| PR-25 | e2e: the contrast actually drawn | A browser walker (web/e2e/contrast.ts) that composes each drawn text over its real ground, opacity and tints counted, and judges it with contrast.ts; a known-places list that only shrinks and fails on a stale entry | web/e2e/contrast.ts, web/e2e/contrast.spec.ts, web/e2e/contrast-known.ts (main.css only if PR-24's tokens need a fix) | Playwright contrast.spec.ts: a setContent self-test of the walker, then six pages of the seeded day at 1440 and 390, both themes, at rest and with a row hovered | PR-24 (contrast.ts, the floors, the scale), PR-1 (day, dayPage, budget) | every row but PR-1, PR-24, PR-26, PR-37, PR-38 |
| PR-26 | web: one colour for the main action; mono for evidence; amber only when someone must act | One filled forest button an area (Open run and the view-link Copy go outline), sentences out of the code font with data-mono marks on evidence, Not installed, not configured, revoked and gone go neutral, the two faded places of PR-25's list fixed | CrewRunCard, pages/crews/[[id]], ShareLinksModal, pages/paste, pages/agents, IntegrationsTable, EventsAside, SidebarSharedList, SidebarRunHeader, SidebarSessionRow, SessionInspector, SessionTile, NeedsYouPill, CommandPalette, FileBrowser, DispatchRunCard, pages/roundhouse, HistoryRow, CrewsTable; web/e2e/contrast.ts and contrast-known.ts; designRules.test.ts | Playwright main-action.spec.ts (forest Retry and Approve, one filled per header, ids mono and sentences not, amber only where asked) at 1440 and 390 in both themes; vitest designRules mono rule and one-filled-action rule | PR-19 (Share, reply bar), PR-21 (Approve, Retry), PR-9 (run header), PR-24 (harness, ink on amber), PR-25 (filledPrimaries' module, the known list), PR-14 (the exited row); open PR 911 (agents.vue badges) | PR-11, PR-12, PR-15, PR-16, PR-17, PR-18, PR-22, PR-23, PR-27, PR-28, PR-29, PR-30, PR-31, PR-32, PR-33, PR-34, PR-35, PR-36, PR-39 |
| PR-27 | web: an agent's hooks and skill are set up on its card | P12 Integrations to Agents and P13's card line: reportStatus (one rule for three readers), each card says Reports to Conductor, Not set up, Experimental or No hooks and links its adapter's row; the table and skill card move to Agents' Reporting section; no Install on the card | web/app/utils/integrations.ts; web/app/pages/agents.vue, events.vue; web/app/components/EventsAside.vue, IntegrationsTable.vue, EventFeed.vue; web/app/utils/pages.ts; web/app/composables/usePaletteRows.ts; web/e2e/agents-reporting.spec.ts (new), events.spec.ts, palette-rows.spec.ts; README.md; docs/features.md | vitest reportStatus; Playwright agents-reporting.spec.ts: each card's key against GET /api/integrations, the Set up link to its row, one install on an own server moving both cards of goose's adapter from Not set up; events.spec.ts:70-78 moved | PR-4 (pages.ts palette row); #911 merged | every row but PR-1, PR-4, PR-28, PR-29 |
| PR-28 | web: Alerts opens the rules | P12 Routing to Alerts and P13's link: PR-7's AlertsPanel gains the routing matrix and webhooks under the switches, opened by your menu's Alerts, the palette and ?alerts; the phone's More ends its switches with All alert rules; the Routing tab leaves Events | web/app/components/AlertsPanel.vue (PR-7's), AlertSwitches.vue, SidebarHeaderMenus.vue, BottomBar.vue, EventsAside.vue; web/app/composables/useAlertsPanel.ts (new), usePaletteRows.ts; web/app/utils/pages.ts; web/app/layouts/default.vue; web/app/pages/events.vue, roundhouse.vue; web/e2e/alerts.spec.ts (new), events.spec.ts, sidebar.spec.ts; README.md; docs/features.md | Playwright alerts.spec.ts: the pill kept across a reload through the panel (events.spec.ts:55-68 moved), the panel at 390 with 44 px pills and no sideways scroll, /events?tab=routing and ?alerts open it | PR-27 (events.vue tabs, in turn), PR-7 (Alerts in your menu) | every row but PR-1, PR-2, PR-3, PR-4, PR-5, PR-6, PR-7, PR-27, PR-29 |
| PR-29 | web: History gets the feed; Events is retired | P12 Feed to History: History's Search and Feed views, /events redirecting by eventsRedirect, Events out of the nav, rail, phone and palette, G E to the feed; the final lists of the seven destinations written once and held by destinations.spec.ts | web/app/pages/history/index.vue, events.vue; web/app/utils/events.ts, pages.ts; web/app/layouts/default.vue; web/app/components/BottomBar.vue, CrewFeed.vue, SessionInspector.vue; web/app/composables/usePaletteRows.ts, useShortcuts.ts; web/e2e/destinations.spec.ts, history-feed.spec.ts (new), events.spec.ts (deleted), sidebar.spec.ts, phone.spec.ts, touched.spec.ts, timeline.spec.ts, shortcuts.spec.ts, palette-rows.spec.ts; README.md; docs/features.md | vitest eventsRedirect and pages.ts; Playwright destinations.spec.ts (every list as drawn), history-feed.spec.ts (events.spec.ts:38-53 moved, the four old links, G E) | PR-27, PR-28 (the tabs left), PR-23 (the feed's shape), PR-17, PR-15, PR-5 (the lists it writes once) | every row but PR-1, PR-2, PR-3, PR-4, PR-5, PR-6, PR-7, PR-8, PR-12, PR-13, PR-15, PR-17, PR-22, PR-23, PR-27, PR-28 |
| PR-30 | web: a welcome that teaches | P17 (the second point of #374): one sentence, three steps (the agents found with versions, conductor host, the example crews), Where things are from pages.ts; Settings leaves the card; the card on the phone's Sessions page when the server has nothing; each choice says what happens, on the welcome, on Crews' Load the examples and on the palette's row for it | web/app/components/WelcomeCard.vue; web/app/utils/agents.ts, agents.test.ts, crews.ts; web/app/pages/sessions/index.vue, crews/[[id]].vue; web/app/composables/usePaletteRows.ts; web/e2e/welcome.spec.ts, home-wide.spec.ts; README.md; docs/features.md | vitest welcomeAgents; Playwright welcome.spec.ts on an own empty server at 1440, 1024 and 390: nothing cut, agents equal to GET /api/catalog's available, Where things are equal to navPages and the nav's tooltips, #908's dialog-words check kept, the examples land on Crews, the examples' line on Crews and in the palette | PR-4 (pages.ts, navPages), PR-22 (welcome on home only), PR-23 (Crews' empty page in PageEmpty, and so #899 through it); #911 merged | every row but PR-1, PR-4, PR-5, PR-6, PR-12, PR-22, PR-23, PR-31, PR-37, PR-38 |
| PR-31 | web: a page to connect | P18: connectShown and ConnectPage for no token or a refused one (never in the desktop app), true words on where the token is, no Cancel, the app hidden not unmounted; the dialog only for the menu, Set token opening it; tokenAsk(page) for the specs | web/app/components/ConnectPage.vue (new), WorkbenchTokenGate.vue; web/app/utils/workbenchGate.ts, workbenchGate.test.ts; web/app/layouts/default.vue; web/app/pages/index.vue, sessions/index.vue; web/e2e/connect.spec.ts (new), fixtures.ts, token-fragment.spec.ts, history-page.spec.ts, history-record.spec.ts, brakeman-act.spec.ts, brakeman-cards.spec.ts, decide-only.spec.ts, decisions-ask.spec.ts, welcome.spec.ts; README.md; docs/features.md | vitest connectShown; Playwright connect.spec.ts (no token at 1440 and 390 in both themes, a refusal mid-work keeping a crew's unsaved edit and a terminal's size, the menu's dialog with Cancel); token-fragment.spec.ts moved onto tokenAsk | #912, #899 merged; PR-1 (dayPage with token: false and ready); PR-30 (welcome.spec.ts, so this row is cut after it) | every row but PR-1, PR-4, PR-5, PR-6, PR-12, PR-22, PR-23, PR-30 |
| PR-32 | web: a run's tasks laid out as lines | P15 (the layout): lineLayout(nodes, opts) as a pure function: depth by taskDepths, chains as lines, splits and merges as connectors and junctions, lanes by interval packing nearest the parent, 4 px geometry in LR and TB, labels that alternate, flip, cut or hide; the shared forty-task shape | web/app/utils/lineDiagram.ts (new), web/app/utils/lineDiagram.test.ts (new), web/app/utils/testdata/line-forty.json (new) | vitest table of graphs written as {id: [depth, lane]} with lines, connectors and junctions (chain, split, the example diamond, the Ledger, reuse, nearest lane, long need, missing need, cycle, TB, mini); seeded properties over 200 random graphs of 1 to 128 nodes; forty tasks inside 960 x 400 | none | every row but PR-33, PR-34 (which draw it) |
| PR-33 | web: a run drawn as a line diagram | P15 (Graph view's Tasks mode) and issue 449: RunLineDiagram.vue replaces Vue Flow in Tasks mode (stations with StatusMark's new SVG form, one neutral ink, keys and a caption with Open), Fit and Direction on the diagram, CrewGraphTaskNode and taskLayout deleted, each Vue Flow graph on a store of its own | web/app/components/RunLineDiagram.vue (new), web/app/components/CrewGraph.vue, web/app/components/CrewGraphTaskNode.vue (deleted), web/app/components/StatusMark.vue, web/app/utils/lineDiagram.ts, web/app/utils/taskGraph.ts, web/app/utils/{lineDiagram,taskGraph,crewGraph}.test.ts, web/e2e/run-fixtures.ts, web/e2e/line-diagram.spec.ts (new), web/e2e/{tasks,decisions-restart,graph}.spec.ts, docs/features.md, docs/design/crews-graph-brief.md | vitest stepFrom, taskListRows, connectionAllowed on hand-built and handoff edges; Playwright line-diagram.spec.ts (6, 40 and 128 tasks inside the pane, nothing overlapping, keys, the caption, unchanged after a mode switch, a direction switch and the 5 s tick, colours in both themes, the phone's list); graph.spec.ts two graphs two stores; tasks and decisions-restart moved | PR-32 (lineLayout), PR-11 (taskRow, the Ledger fixture), PR-10 (taskStatus, StatusMark), PR-1, open PR 912 merged (CrewGraph.vue) | every row but PR-1, PR-2, PR-3, PR-4, PR-9, PR-10, PR-11, PR-32, PR-34 |
| PR-34 | web, crew: the small line drawing in the Crews table and on a run's card | P15 (the small drawing): the crew summary's task shape in Go, given only with ?tasks=1 so conductor up's 4 MiB page bound holds; RunLineDiagram's mini and card sizes in the Shape column and on a live run's card; members' start rules drawn as lines; CrewMiniDag deleted | internal/crew/store.go, internal/crew/store_test.go, internal/api/crews.go, internal/api/crews_test.go, docs/protocol.md, web/app/composables/useSessions.ts, web/app/utils/crews.ts, web/app/utils/lineDiagram.ts, web/app/components/RunLineDiagram.vue, web/app/components/CrewsTable.vue, web/app/components/CrewRunCard.vue, web/app/components/CrewMiniDag.vue (deleted), web/app/pages/crews/[[id]].vue, web/e2e/{examples,crews}.spec.ts, docs/features.md, docs/design/crews-graph-brief.md | Go race TestStoreSummaryCarriesTaskShape and TestCrewsListTaskShapeWhenAsked; vitest summaryOf and memberNodes; Playwright examples.spec.ts (the tasks crew's diamond and the tree crew's split in the Shape cell, the live card's stations) and crews.spec.ts (the Ledger card lit, amber in both themes, inside the card at 390) | PR-33 (RunLineDiagram.vue), PR-10 (taskStatus, memberStatusOf), PR-1, open PR 899 merged (crews page and its specs) | every row but PR-1, PR-2, PR-3, PR-4, PR-9, PR-10, PR-11, PR-32, PR-33 |
| PR-35 | api, session: a launch's first message | POST /api/sessions takes message (at most proto.MaxSubmit bytes), kept in memory, typed once ready by the crew's readiness rule, held behind every startup question, withdrawn when a person types first, dropped when the session ends; a nameless session named by its first words; Info.firstMessage; one cut-at-a-word rule with … and defaultLabel's … (D11 Go) | internal/api (sessions.go, server.go), internal/crew (firstmessage.go new, run.go), internal/session (name.go, local.go, submit.go, info.go, testdata/cut-at-word.json), internal/dispatch/launches.go, web/app/composables/useSessions.ts (two type lines), docs/protocol.md, docs/features.md | Go race tests: internal/crew firstmessage_test.go on a ticks seam (trust, each startup question, the sign-in screen as main draws it, withdrawal, end, past the cap) and run_test.go's two questions in a row, internal/session PersonTyped and the cut table, internal/dispatch label, internal/api bound, name, typed, shutdown, clone | PR-2 (the readiness after an Enter, info.go, useSessions.ts, protocol.md); open #913 (createLocalSession), #909 (launches.go) | every row but PR-2, PR-36 |
| PR-36 | web: launch with a first message | The Launch dialog's optional 'What should it do?' field with its byte bound and the name's preview, the waiting line above the terminal, the app's copy of the cut rule and a ticket launch's placeholder label ending … (D11 app) | web/app/components/LaunchSessionModal.vue, web/app/pages/sessions/[id].vue, web/app/utils/launch.ts, utils/dispatchPlans.ts, web/e2e/launch-first-message.spec.ts (new), docs/features.md, README.md | vitest on the shared cut table, namePreview, messageProblem, firstMessageLine and defaultLabel; launch-first-message.spec.ts on a server of its own: trust held then typed once, Claude stub typed when ready, the bound, the phone | PR-35 (the field, firstMessage, the table), PR-19 (the page's layout with the inspector closed), PR-1 (ownServer, inside); open #912 (dispatchPlans.ts, textual) | every row but PR-1, PR-2, PR-3, PR-19, PR-35 |
| PR-37 | docs, web: a design system that rules | docs/design/system.md, begun by PR-3, made whole (status table, colour and contrast, main action, mono, amber, page header, toolbar, list row, cutting text from PR 912's diagnosis, tooltips, what a PR cannot do), each rule Decided or Described; IconButton.vue and about 77 icon-only buttons moved onto it | docs/design/system.md (PR-3's), docs/design/brand.md, docs/design/briefs/sidebar.md, docs/checks.md, web/app/components/IconButton.vue (new) and about 45 components and pages with icon-only buttons, web/app/utils/designRules.test.ts, web/e2e/tooltips.spec.ts | vitest designRules: every rule marked, status table equals STATUS_LOOK, floors equal CONTRAST_FLOORS, status-map ratchet, the cut rule (fails with items-stretch removed), the tooltip rule; Playwright tooltips.spec.ts on hover and focus | PR-3 (STATUS_LOOK, system.md), PR-12 (the toolbar), PR-24 (floors, harness), PR-26 (the three rules and checks); open PR 912 (INBOX_ROW_CLASS); PR-30 merged first, so that this row's Fixes #374 closes the issue | PR-11, PR-15, PR-16, PR-17, PR-18, PR-27, PR-28, PR-29, PR-31, PR-32, PR-33, PR-34, PR-35, PR-36, PR-39 |
| PR-38 | e2e, scripts: the design capture in the repository, and what drifted | An opt-in web/e2e/look.spec.ts run by make design-capture (screens and computed values of every page, both themes, both sizes), scripts/design_drift.py against a committed docs/design/system.json, and the by-hand publish step on the draft release's checklist | web/e2e/look.spec.ts, scripts/design_drift.py and _test.py, scripts/release_draft.py and _test.py, docs/design/system.json (new), docs/design/system.md, docs/checks.md, Makefile | scripts suite design_drift_test.py (each kind of drift, no drift, --check, stable --write) and release_draft_test.py (the checklist body); look.spec.ts opt-in, typechecked, run once by hand | PR-37 (system.md's rules and marks), PR-25 (the walk it records), PR-4 (utils/pages.ts), PR-1 (day, dayPage) | PR-11, PR-15, PR-16, PR-17, PR-18, PR-27, PR-28, PR-29, PR-31, PR-32, PR-33, PR-34, PR-35, PR-36, PR-39 |
| PR-39 | ci: the e2e job in two shards | CI's e2e job as a two-shard matrix under the same job id, a report artifact per shard, a 90-minute hang bound, docs on reproducing a shard | .github/workflows/ci.yml, docs/checks.md, scripts/preflight_test.py | preflight_test.py test_the_e2e_shards_cover_the_suite (the matrix lists 1..N and the step shards matrix.shard/N); test_every_ci_job_has_a_linux_step unchanged holds the job id | none; cut first, beside PR-1 | every row (docs/checks.md is shared with PR-1 and PR-38, in paragraphs of their own) |

Sizes, by the guide the owner set (a row is 300 to 800 changed lines of
code, at most about 1,500 with its tests): PR-1 about 25 lines (docs)
and 1,400 of tests; PR-2 about 100 and 300; PR-3 about 420 and 560;
PR-4 about 330 and 380; PR-5 about 180 and 400; PR-6 about 240 and 330;
PR-7 about 250 and 380; PR-8 about 230 and 560; PR-9 about 260 and 420;
PR-10 about 340 and 470; PR-11 about 230 and 330; PR-12 about 480 and
450; PR-13 about 280 and 360; PR-14 about 190 and 300; PR-15 about 160
and 230; PR-16 about 320 and 420; PR-17 about 230 and 450; PR-18 about
265 changed (75 added, 190 deleted) and 570; PR-19 about 220 and 480;
PR-20 about 170 and 220; PR-21 about 200 and 320; PR-22 about 330 and
420; PR-23 about 420 and 360; PR-24 about 270 (40 of them docs) and
430; PR-25 about 5 and 310; PR-26 about 150 and 330; PR-27 about 260
and 220; PR-28 about 230 and 200; PR-29 about 380 and 300; PR-30 about
255 and 280; PR-31 about 200 and 320; PR-32 about 280 and 430; PR-33
about 360 and 460; PR-34 about 170 and 230; PR-35 about 305 and 640;
PR-36 about 110 and 330; PR-37 about 530 (330 of them docs) and 380;
PR-38 about 360 (and a generated `system.json` of about 250) and 150;
PR-39 about 35 and 25. In all about 9,770 lines of code and docs and
15,145 of tests, about 24,915 changed lines; the tests are about three
fifths of it. Twenty-seven of the thirty-nine rows are under 300 lines
of code; with their tests each is a reviewable pull request of its
own.

The rows that change a Go file are PR-2, PR-21, PR-34 and PR-35: the
Mac rows of `docs/checks.md` apply to them, with the Go race suite,
`make lint` and `make lint-static`. Every other row changes the app,
the e2e suite, the docs, a script or the CI workflow alone.

Lanes that can run side by side, each in its own order: the sidebar and
the nav (PR-4, PR-5, PR-7, PR-8, PR-13, PR-14, PR-15); status and runs
(PR-2, PR-3, PR-10, PR-9, PR-11, PR-33, PR-34); the page of what needs
you (PR-6, PR-20, PR-21); Dispatch (PR-12, PR-16); the Yard (PR-17,
PR-18); the session page and launch (PR-19, PR-35, PR-36); pages
(PR-22, PR-23, PR-27, PR-28, PR-29, PR-30, PR-31); the look (PR-24,
PR-25, PR-26, PR-37, PR-38); the line layout (PR-32). PR-1 is first,
with PR-39 beside it. The lanes are not wholly apart: the pages lane
shares `pages/yard.vue`, `pages/roundhouse.vue` and
`SessionSidebar.vue` with the Yard and the sidebar lanes, PR-8 and PR-18
share four specs, PR-24 shares the sidebar's components with the
sidebar lane, and PR-7 adds to your menu after PR-6; each row's
Depends on names its shared files, and the row cut second rebases on
the first.

## Owner decisions

Dated 2026-11-15. Each is a default the owner can overturn with a dated
row in the Program's status log; the reasoning is why it was taken.
This section holds fifty-two decisions, each a choice a person would
see: the first twenty-five are the stream's rulings on what a person
sees and on the pieces every row shares, the other twenty-seven each
row's own, under the row they belong to. Each row's other defaults,
the ones no person would see or overturn, are in its own section under
"Defaults". Some row decisions restate a ruling for their row; each
says which.

### The stream's rulings

1. **The word for an agent that ended its turn.** Default:
   **finished** ("finished 12m ago"). A run that has ended says its
   outcome (Succeeded, Failed, Stopped, Interrupted, or Ended for a run
   of members), never "Finished", and the
   palette's "Stop every session that is done" follows the word. Why:
   the turn is over and the next move is a person's, when they want it;
   today's "done" and "idle" are drawn as "running". Alternative: keep
   "done", or "idle".
2. **The name of the list and its page.** Default: **Needs you**, in
   the nav, the page's title, the rail, the palette, the shortcut's
   label, a decision link's tab and a ticket's link, all in PR-5. The
   route `/decisions`, the key G N and the decide-only page (still
   "Decide") stay. Why: one list has three names today ("Decide",
   "Needs your decision", "Needs you"), and the sidebar and the pill
   already say the shortest. The palette's second group ("Needs your
   decision", its rows "Decide: <title>", `usePaletteRows.ts:948` and
   `:74`) keeps its name until PR-13 makes the two groups one.
   Alternative: "Decide", or "Needs your decision", everywhere.
3. **The server first.** Default: PR-2 (the turn signal) goes before
   PR-3, so that "finished" and "working" are true for every agent that
   reports; an agent that reports nothing is "running", and the legend
   says so. Why: a surer mark drawn on today's signals would show an
   agent at rest as working. Alternative: PR-3 first, on today's
   signals.
4. **Starting, a host that is away, and green.** Default: `starting`
   takes the running mark with the word "starting". A host away that
   asks is `needs` ("needs you · host away"), counted and listed in
   Needs you (decision 29, PR-14); one that does not ask takes the
   exited mark (the dashed ring) with "host away" and is listed with
   Exited. Green is for a live agent: `working` is the green dot in its
   ring, `running` (no signal) the plain green dot, and the ring alone
   tells them apart; no other state is green, so the other greens (the
   feed's done mark, a direct connection, a crew's live-runs dot, a
   finished run's bar) go neutral, and the forest fill stays the main
   action's alone. Why: each of the five marks means one thing.
   Alternative, for a host that is away: the mark the session last had,
   dimmed, with the words "host away", listed where it was, since a host
   can come back with its agent in the same turn and "exited" reads as
   an end that has not happened.
5. **One number.** Default: the desktop's nav and rail carry one amber
   count, on Needs you. The Yard's nav badge and the palette's "Yard ·
   N need you" lose their number; the phone's Yard tab keeps its count
   of sessions, drawn neutral like the Yard's chip, so the phone's one
   amber number is its Needs you tab; the Yard's own chip counts what
   its grid shows (sessions) and says so in neutral ink (PR-17); the tab
   title and the favicon count the one number. PR-5 builds the review's
   P2 whole: while the sidebar's section lists fewer kinds than the page
   (until PR-13), its header says what it leaves out ("Needs you 6 · +3
   on the page": plans, tasks and stopped runs' records), and PR-13
   removes those words when the section lists every kind. Why: two amber
   numbers that disagree without saying why are what the review met.
   Alternative: no words, the section counting only what it lists until
   PR-13.
6. **The sidebar's width.** Default: **200 px**, the minimum and the
   opening width, with the header slimmed as the review proposes (the
   mark, the name, your menu; Alerts moves into your menu; collapse
   stays on its key and the divider). Pull request #908, merged on
   2026-11-15, raised the minimum to 268 px so that the old header is
   whole; PR-7 returns it.
   Why: the review and the maintainers' earlier ask (issue #437) both
   want the narrow sidebar. Alternative: stay at 268 px and drop the
   header part of PR-7.
7. **A question in the narrow sidebar.** Default: "the question whole,
   never cut" is overturned for the narrow sidebar only, and never
   without its panel: below 280 px a question is two lines in the row
   and whole in a panel beside it; at 280 px and wider the row shows it
   whole as today. PR-8 ships the clamp and the panel together. Why:
   at 200 px a question at the server's bound is about two dozen lines,
   and the list shows two of eighteen sessions. Alternative: the
   question whole in the row at every width.
8. **The Yard's queue.** Default: it goes everywhere and does not come
   back when the sidebar is folded. Its "Answered" list is dropped (a
   session's Activity and its History record keep who answered). The
   pill's Enter on the Yard expands the tile in place. On the Yard, J,
   K and Enter no longer step through questions; ] and [ (the pill) do
   it on every page. Alt+J and Alt+K go back to terminals. Why: the
   queue shrinks every tile while anything waits, and the same questions
   are answered in the sidebar, the pill and the tile's own bar.
   Alternative: keep the queue while the sidebar is folded.
9. **The row of Needs you in the nav.** Default: it hides at zero. It
   shows while an item is open, an answer is not yet applied, something
   was decided in the last day, or the list is unread or could not be
   read (unknown is not zero). On a switchyard, where the inbox is not
   served, it is hidden. With the six tiles of PR-15 the tile stays and
   loses its number. Why: P24, a nav row that says nothing at zero.
   Alternative: the row always shown, as today.
10. **A page's standing paragraph.** Default: it folds to "About this
    page" once the page has content. History's empty words: "No
    session is kept yet." Why: the paragraph is for the person who has
    not read it, not for the one who reads it every day. History's
    search caption is one of them. Alternative: the paragraphs go once
    the page has content (the review's other offer), or stay as today.
11. **Fewer places to look (P12).** Default: all three moves, late in
    the stream, each standing alone: hooks and the skill to Agents
    (PR-27), the rules to Alerts as a slideover (PR-28), the feed into
    History as a view with `/events` redirecting (PR-29). The Roundhouse
    becomes a view of the Yard with `/roundhouse`, its name and G R
    kept (PR-17), its header saying "Yard" and its tab "Yard ·
    Roundhouse". History opens on Search. Why: three everyday jobs
    each end in one place, ten destinations become seven, and old links
    keep working. The review put the feed first in History; this opens
    on Search. Alternative: any of the moves left out, or History opens
    on its Feed.
12. **The session page (P11).** Default: the inspector is remembered
    once for all sessions under a new key and is closed until opened. A
    reply choice is drawn filled only when its label ends
    "(Recommended)", read in the app; no protocol change. Why: the
    terminal gets the room, and only the agent's own recommendation is
    filled. Alternative: a choice per session; a recommendation sent by
    the server.
13. **The line diagram (P15).** Default: it replaces Vue Flow in the
    Graph view's Tasks mode; Members mode and the crew editor keep Vue
    Flow. One neutral ink for every line; only stations carry a state's
    colour. The phone keeps the list. Why: forty tasks fit the window
    with their names, and the state is said by the stations alone.
    Alternative: a new layout inside Vue Flow.
14. **Amber (P16).** Default: read-failure alerts in amber stay as they
    are (out of this stream); standing badges that ask nothing go
    neutral. Why: a failed read may ask a person to look; a standing
    fact never does. Alternative: the alerts changed too, in an issue
    of their own.
15. **A page to connect (P18).** Default: the connect page for every
    ask; the dialog stays only for the menu's voluntary change. The
    page never shows the token file's real path. Why: the app under a
    dialog cannot be used anyway, and the path would tell anyone who
    reaches the server where its data is. Alternative: the dialog, with
    the corrected words #908 gave it.
16. **A launch's first message (P19).** Default: the request field is
    `message`, at most `proto.MaxSubmit` bytes; it is typed once the
    session is ready by the crew's readiness rule; withdrawn if a person
    types first; a hosted launch takes none. Why: one rule of readiness
    for every line Conductor types. Alternative: typed at once, or kept
    for a resume.
17. **The design rules (P20).** Default: the rules live in a new
    `docs/design/system.md`; `brand.md` stays identity. Each rule is
    marked "Decided YYYY-MM-DD by the maintainers, #N" or "Described,
    not decided". The design-system page kept outside this repository
    cannot be changed from a pull request: publishing to it is a
    by-hand step at a release, on the release's checklist. Why: a rule
    a machine can hold names its check, and a reader sees which rules
    were chosen. Alternative: the rules in `brand.md`.
18. **D7 and D12.** Default: D7 is built once, in PR-19 (a "gone" state
    entered on the server's 404, the record's name as the title, a link
    to its record in History, not Resume). D12 is fixed in the app
    (`briefLine`).
    Why: one owner each, and no field changes. Alternative: D12 fixed
    in the server's `questionItem`.
19. **The cutting rule.** Default: D1's cause is the row's line not
    taking the button's width (open #912's `items-stretch`), not a
    missing `min-w-0`, and PR-37's rule and its check are written from
    that. Why: a `truncate` element's automatic minimum is already 0.
    Alternative: a rule that asks for `min-w-0` everywhere, which would
    not have caught D1.
20. **Issue #716.** Default: Codex's tool-approval prompt shown as
    working is a defect outside this stream, with its own issue, and the
    plan asks that it be fixed before PR-3 merges; if it is not, PR-3
    waits. Why: PR-3 draws "working" with a surer mark, and a question
    read as work would look more certain than it is. Alternative: PR-3
    ships with the misread known.
21. **Issue #374.** Default: its first point (no label cut on the
    welcome card) is on `main` (#908); its second (each choice says what
    happens: the welcome card, "Load the examples" on Crews, the
    palette's row) is PR-30's, which says `Part of #374`; its third (a
    tooltip on every icon-only button, written into the design rules)
    is PR-37's rule with its check, which says `Fixes #374`. Issues
    #708 (search in the shortcuts dialog) and #346 (themes as files)
    are not in this stream. Why: the issue asks for all three, and #908
    did the first. Alternative: PR-30 keeps to the welcome
    card alone.
22. **The e2e job in two shards.** Default: PR-39 is cut at once,
    beside PR-1. Why: the e2e job is already longer than the go job on
    `main` (60 minutes against 59 on #895, 65 against 46 on #905), and
    PR-1 adds the day to it. Alternative: wait until a row of the stream
    makes it longer.
23. **Tasks in the one list.** Default: `useNeedsYou()` counts tasks
    from the inbox's task items in PR-4, the server's own rule for a
    task that waits on a person; PR-10's `taskStatus` must agree with it
    on "a task needs a person", and a test holds the two to one answer
    for the same run. Why: `taskStatus` arrives after the list, and the
    server's rule is the one the inbox lists by. Alternative: the list
    switched to `taskStatus` once PR-10 lands.
24. **A run's one sentence.** Default: `runStatusLine(run, sessions,
    decisions, now)` takes the store's open decisions of the run, so
    that a plan gate's decision that holds no task is counted, as
    `tasks.test.ts` "counts an open decision that holds no task" proves
    today. Why: a run carries no such decision itself. Alternative: no
    decisions argument, and that proof lost.
25. **One mark in two forms.** Default: `StatusMark` has an HTML form,
    the mark with its word, and an SVG form, a station of the line
    diagram (PR-33), that draws the same `STATUS_LOOK` marks as circles
    and rings; PR-3 shapes the component for the second form. Why: one
    table of marks, never a sixth mark for the diagram. Alternative: a
    second table for the diagram's stations.

### What each row chooses that a person sees

#### PR-2

26. **The guest's page.** Default: `GET /api/join/{token}` carries
    `turns`, so a guest sees working and finished as the owner does
    (finished with no age, PR-3). Alternative: the guest sees "running"
    for an agent at work after an answer.

#### PR-3

27. **The exited word and the old badges.** Default: "exited", or
    "exit 1" for a code other than 0; `AttentionBadge.vue` and
    `SessionStatusBadge.vue` are deleted. Why: one mark and word
    replaces both on every header.
28. **The Yard's bar.** Default: four slices, needs amber, working
    green, finished neutral, running green at 40 % with a hatch.
    Alternative: "running" in the neutral slice, said in the label.

#### PR-4

29. **A host away that asks.** Default: counted in the one number, as
    the sidebar counts it today, though the Yard's chip leaves it out.
    Why: nothing else answers it, and its row says the answer waits for
    the host.
30. **An open record of a stopped run.** Default: counted in the one
    number and (PR-13) listed in Needs you, as the server's inbox counts
    it. Why: today the sidebar and the palette leave it out, and it
    still waits on a person.

#### PR-5

31. **The pill.** Default: it counts what it can answer and says "2 of
    4 · 2 more in Needs you".

#### PR-6

32. **The zero page's words.** Default: "Nothing needs you now." and
    "The last one closed 2h ago." Why: the page is named Needs you, and
    "closed" is still true for a record cancelled or expired.
    Alternative: the review's "Nothing needs your decision" and
    "answered".
33. **Open on your phone.** Default: in the sidebar's account menu at
    every width, not in the phone's More and not in the palette. Why:
    on a phone the page is already open on the phone.

#### PR-9

34. **Chat.** Default: an icon button beside the menu with its unread
    count, not an item of the menu as the review sketches. Why: it
    carries a live unread count, and specs press it.
35. **Stop.** Default: a neutral outline button at rest, red only in
    its confirm. Why: nothing red at rest; the confirm already asks.

#### PR-10

36. **A task's own marks.** Default: blocked "waiting" and ready
    "ready" (a hollow square), succeeded "done" (a check in neutral
    ink, since the forest fill is for the main action alone), failed (a
    red cross), cancelled (a grey dash); verifying is the working mark
    with "checking". Why: "done" is free for tasks once sessions say
    "finished". Alternative: a forest check, named as an exception in
    invariant 7.
37. **An ended run of members.** Default: it says "Ended"; the Crews
    page's Runs filter "Finished" reads "Ended"; ended run bars go
    neutral (stopped lighter, failed red) and a live run's bar is the
    working green. Why: a run that has ended says its outcome, never
    "Finished", and green is for what is live.

#### PR-11

38. **The row's actions.** Default: Cancel, Copy branch and Ask
    Brakeman why behind the row's menu; Answer, Retry, Accept anyway,
    Open and Open preview stay in the row. Why: each visible action is
    what a waiting state asks for.

#### PR-12

39. **No key for the toolbar.** Default: none, with a palette row
    "Filter the tickets…" instead. Why: F is fullscreen and / is the
    palette on every page.

#### PR-16

40. **People without command lines.** Default: each member's name and
    agent with a link to the Plan; the argv dropped, and the plan's
    "Arguments:" line ("Runs as" holds them).
41. **A newer proposed revision while an approved one is featured.**
    Default: as today, folded under Other revisions with its Approve;
    the strip's Plan says "r2 waits for your approval". Alternative:
    its body opened there while it waits.

#### PR-17

42. **The chips.** Default: All, Needs you, Working and Finished
    always, and Running only while it has a session or is the chosen
    chip. Why: Running (no signal, and starting) is common until
    PR-2's agents report, but shown at zero it clutters the header.

#### PR-19

43. **The inspector's first view.** Default: the rail's toggle with no
    view chosen opens on Explorer, not People. Why: People shows you
    to yourself, which the review objected to.

#### PR-20

44. **Details on a run's decision.** Default: open for a plan's
    approval, folded for everything else; the evidence chips, the
    uncertainty, the state words and the History fold stay in view.
    Why: an approval that starts work is read whole.

#### PR-22

45. **The Yard's second action.** Default: a link to the README's Quick
    start, not "Copy the host command". Why: the command carries the
    workbench token, and the Launch dialog's "On this computer"
    already builds it where it is offered.

#### PR-24

46. **The dark sidebar's tone.** Default: a canvas at ink-950
    (`#0C1410`) beside an ink page (`#18211C`), not `tokens.css`'s
    forest panel. Why: a quiet step off the page, and forest is the
    action's colour.
47. **The splash and the switchyard.** Default: the desktop splash and
    the switchyard's own pages stay on their zinc palette (`brand.md`),
    with an issue of their own if the owner wants them on ink. Why:
    PR-24 is kept to the app and the docs.

#### PR-27

48. **No Install on a card.** Default: the card says Reports to
    Conductor, Not set up, Experimental or No hooks and links its
    adapter's row on the same page, where Install sits. Why: an
    install is per adapter (a dozen cards may share one), writes into
    the server's home with the path shown in the row, and may end in a
    snippet to finish by hand.

#### PR-29

49. **History's Feed.** Default: it says "Since this tab opened" and
    stays a feed of the tab. Why: a feed kept on the server is a
    server change this stream does not make.

#### PR-30

50. **Settings.** Default: it leaves the welcome card. Why: in the
    desktop app it is in the nav and on G S, and in a browser Settings
    is in no nav and G S does nothing (`default.vue:57-58`), so the
    card offered a page that is not there.
51. **The welcome's sentence.** Default: "Conductor runs your coding
    agents in terminals on this machine, and lets you watch, answer
    and share them from any browser.", a draft for the owner to
    rewrite.

#### PR-35

52. **The name.** Default: the first words, cut at a word to 40
    characters and 120 bytes with "…"; two sessions may share it.

No question is left open: each answer above lets the plan stand, and
the owner overturns any of them with one row. Two of them ask for the
owner's own words or wish: 47 (whether the splash and the switchyard
move to ink) and 51 (the welcome's sentence).

## Acceptance

The stream is accepted on an observed walk, not by ticks: once its last
row has merged, on the seeded day (`CONDUCTOR_E2E_DAY_OPEN=1`) and on
the owner's own machine with real agents (a Claude Code, a Codex and an
agent that reports nothing), at 1440 x 900, at 1280 x 720 and on a
phone, in both themes. The last row to merge records the walk in
`docs/features.md`, one dated entry for the stream as the other batches
have; the rows before it do not touch that entry. What is observed:

- The five states on every surface for a real agent's turn: an agent
  at work is "working", one that ended its turn is "finished" with its
  age, one that asks is "needs you", an agent that reports nothing is
  "running", an exited one is "exited", the same on the sidebar's row,
  the rail, the phone's list, a Yard tile, the Yard's bar, the session
  page, the Roundhouse, the palette and a guest's page.
- One number while something waits, the same on the nav, the rail, the
  phone's tab, the palette, the pill, the tab's title and the favicon,
  and none at zero, where the nav's row has left.
- Every kind in the sidebar's list (a question, a plan, a task out of
  attempts, a run's decision), each with its mark, and each answered or
  opened from the list.
- The sidebar at its narrowest, 200 px, on a 1280 x 720 window: every
  name whole, a question on two lines and whole in its panel, the six
  tiles at its foot.
- The sidebar's Finished and Working sections, Finished above Working,
  a run once with "2 working · 2 finished".
- Needs you's item: the question and one set of buttons first, Details
  folded for a question and open for a plan's approval, the first item
  opened at 1440.
- Needs you at zero: one column, the four kinds, "Decided in the last
  day" and its way to History.
- Agents' cards saying whether each reports, Alerts holding its rules,
  a page's tooltip in the nav.
- The small line drawing in the Crews table and on a live run's card.
- A run's header (the name whole, nothing under a button, the one
  sentence) and its task rows as a table that says the state once; the
  Graph view as a line diagram of forty tasks inside the window.
- A Dispatch project's one toolbar, its first ticket near the top and
  its columns read straight down.
- A ticket's strip while the page scrolls, and an approved plan folded
  to one line.
- The Yard without its queue: the grid at its full width, a tile that
  asks saying the start of its question, the Roundhouse as its second
  view.
- A session page with the terminal given the room, the inspector
  closed, Share outlined, a choice filled only when the agent
  recommends it, and a gone session's page.
- An empty server's pages, each in one shape: what the page is, why it
  is empty, the one action that fills it; and the reading shape before
  the first read, with no welcome flashing on a busy server's Yard.
- Both themes on the brand's own neutrals, the light theme's amber and
  green words legible, one filled forest button in each area.
- A new install's first screens: the page to connect, saying where the
  token is; the welcome with the agents found, its steps and "Where
  things are".
- A launch with a first message, typed once after a real Codex's trust
  question is answered from the reply bar, and the session named from
  it.
- The old links: `/events` and its tabs, `/roundhouse`, `/carousel`, G
  E and G R, each landing where its place lives now.
- The by-hand looks each row names (its section's "By hand"), read on
  the day by the person who walks the stream.

Done means: every row merged, the walk recorded with the owner's word
that the screens say what is true, and a bug the stream's code caused,
found by the walk, fixed in the stream. What needs the owner: the
review of this plan and its decisions, #716 put on the board as Ready
before PR-3 is cut, the walk on the owner's machine, and the merges.
