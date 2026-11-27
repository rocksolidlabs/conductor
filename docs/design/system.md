# Conductor's design system

The layer above Nuxt UI's components: the rules every screen of Conductor
follows. The brand itself, its colours, type and mark, is
[brand.md](brand.md); this file says how the screens use it.

How to read it:

- Each rule opens with its mark, on a line of its own: "Decided
  YYYY-MM-DD by the maintainers, #N." (the date the change that made it
  true merged, and that change's issue) or "Described, not decided." (how
  the app does it today, which the maintainers have not chosen).
- A rule the maintainers decided before the app meets it everywhere
  carries the date it was written here, and a line "Not yet:" naming the
  places the app does not meet it yet; those places only become fewer.
- A rule that a machine can hold names its checks, and those checks run in
  CI. A change to a rule changes its check in the same pull request: the
  checks that read this file (`web/app/utils/designRules.test.ts`) fail
  when the file and the code disagree.
- Words in `code` are the names in the code: a key, a class, a file.

## The rules

### Who the screens are for

Decided 2026-11-26 by the maintainers, #1158.

People who use Conductor all day: several agents at once, Yolo on, the
desktop app in a wide, short window (about 2000 x 810). The screens where
a person works with an agent, its Chat view and its Terminal view, come
first: a change elsewhere never makes them slower to use.

Simpler means less to watch and less to hold in the head, never more
presses for what people do all day: one idea a screen, few things on it,
plain words.

Checks: none. What people do all day, and what comes first, are judged in
review, by walking the main task on the rendered screens (`docs/checks.md`,
the seeded day) at that window as well as at 1440 x 900 and 390 x 844. No
layout spec measures at 2000 x 810 yet.

### Status

Decided 2026-11-16 by the maintainers, #918.

A session, a run's member and a task's attempt are each in one of five
states, each with one mark and one word, the same on every surface: the
sidebar's row, its folded sections and its rail, the phone's list, the
"Shared with you" list, a Yard tile, the Yard's bar and its focused
header, the session page, the Roundhouse's slide and strip, the command
palette and a guest's page.

| State | Key | Mark | Word | Means |
|---|---|---|---|---|
| Needs you | `needs` | amber dot | needs you | It asked, or something of it waits for a person's decision. Nothing moves until someone answers. |
| Working | `working` | green dot in a green ring | working | The agent is in a turn. |
| Finished | `finished` | grey ring | finished 12m ago | The agent ended its turn and waits for the next word. It is a person's move, when they want it. |
| Running | `running` | plain green dot | running | The agent reports no turns. Conductor cannot tell working from waiting. |
| Exited | `exited` | dashed grey ring | exited | The process ended. |

The variants:

- A session that is starting takes the running mark with the word
  "starting".
- A session whose host is away and that asks is `needs`, "needs you ·
  host away"; one whose host is away and that does not ask takes the
  exited mark with the words "host away".
- A session with status `exited` and an exit code other than 0 says
  "exit N" ("exit 1"); a `stopped` session, ended by a person, says
  "exited" whatever its code.
- The age of a finished turn is "finished just now" under a minute, then
  "finished 12m ago", "finished 3h ago". It ticks with the app's one
  clock, every 15 seconds. Where no time is known (a guest's page: the
  attention frame carries none) it is "finished".
- A run's mark in the rail is its busiest member's, in the order needs,
  working, running, finished, exited.
- A joined link that does not answer, or one revoked or gone, draws no
  mark; its line says so ("not answering"). Amber stays for needs.

The rules:

- Every surface gets a session's state from one function,
  `sessionStatus` in `web/app/utils/status.ts`, and draws it with
  `StatusMark.vue`. No component reads `attention.state` or `status` to
  pick a colour or a word. The marks' shapes and tones are one table,
  `STATUS_LOOK`, which both forms of the mark read: the mark with its
  word, and a station of a run's line diagram.
- The mark carries `data-status-mark="<key>"`, is an image named by its
  word with the legend line as its title, and is 8 px. Its word, where it
  is drawn, carries `data-status-word` and is in the muted text, never in
  a status colour.
- Green is for a live agent: `working` is the green dot in its ring,
  `running` the plain green dot, and the ring alone tells them apart; no
  other state is green. Amber is for `needs` alone. Two states of one
  colour differ in shape, so the shapes carry the state without colour.
- A finished turn's event badge is not drawn beside a session's name
  while its status says finished; where the Events feed draws it, it is
  neutral and says "finished".
- What the app cannot guess, the server says: an agent's turn signal
  (`turns` in the session listing, docs/protocol.md, "A turn's start and
  end"). The table of what a session shows after each step, and the key
  the app gives it, is `internal/session/testdata/status-table.json`,
  which the server's and the app's tests both read.
- No success fill carries text: a green fill is a mark, never the ground
  of a word.

Checks: `designRules.test.ts`, the table and the code (this table against
`STATUS_LOOK`, key by key: the mark, the word and the legend's line) and
the status map (no source but `utils/status.ts` maps a state's name to
`bg-success`, `bg-warning`, `bg-neutral-400`, `text-success` or
`text-warning`; the places that still do are listed there, each with its
reason, and the list only shrinks); `status.test.ts`;
`web/e2e/status-walk.spec.ts`.

### Runs and tasks

Decided 2026-11-16 by the maintainers, #925.

A run's tasks, its members, its graph, its cards and Dispatch's card for
it speak the same language. A task's attempt that works takes its
session's mark; a task's own states have marks of their own, none of the
five:

| State | Key | Mark | Word | Means |
|---|---|---|---|---|
| Waiting | `waiting` | hollow grey square | waiting, or ready | It waits for the tasks it needs (blocked), or can start when a member is free (ready). |
| Done | `done` | check in neutral ink | done | It succeeded and its result stands. The forest fill is for the main action alone. |
| Failed | `failed` | red cross | failed | It ended without a result, and nothing is left to do on it. |
| Cancelled | `cancelled` | grey dash | cancelled | A person, or a decision, called it off. |

- A task held by a decision still open, and a failed task on which the
  engine names a command (Retry, Accept anyway: the inbox's own rule),
  is `needs`. A decision answered and not yet applied is Yardmaster's to
  apply, not a person's.
- A running task is its attempt's session's state: working, finished,
  needs or running; running while no session is known. A task checking
  its result takes the working mark with the word "checking".
- A member takes its session's state; a member starting takes the
  running mark with "starting", one not started the waiting mark with
  "waiting to start", one that ended `exited`, and one that ended with
  an error of its own the failed mark.
- A run says one sentence: "2 of 6 done · 1 working · 1 checking · 2
  need you" for a run with tasks, "1 working · 2 finished · 1 needs you"
  for a run of members, and an ended run its outcome: "Succeeded",
  "Failed", "Cancelled", "Stopped", "Interrupted", or "Ended" for a run
  of members that all ended. "N need you" counts the things a person can
  act on in the run, each once: a decision that holds two tasks is one.
  A ticket's card for a run the page holds only as the ticket's summary
  says what a summary carries: "2 of 6 done · 2 need you".
- Every surface gets these from `taskStatus`, `memberStatusOf`,
  `runStatusLine` and `summaryStatusLine` in `web/app/utils/status.ts`,
  and draws a mark with `StatusMark.vue`; the marks are `TASK_LOOK`
  beside `STATUS_LOOK`. A count of a run's tasks is neutral but for what
  waits on a person (amber) and what failed (red). A run that ended is
  never green: on the Crews page its bar is neutral (stopped lighter,
  failed red) and only a live run's bar is the working green.

Checks: `designRules.test.ts`, the table and the code (the marks and words
of this table against `TASK_LOOK`); `status.test.ts`;
`web/e2e/run-status.spec.ts`.

### A run's member opens inside its run

Decided 2026-11-26 by the maintainers, #1158.

A crew's member has no page of its own that a person can get lost on: it
opens inside its run, with the way back to the run in view and in words
(the run's name), wherever it is opened from: the sidebar, the run's page,
the Yard.

Not yet: today a member opens on its session's own page (`/sessions/<id>`).

Checks: none yet; a spec beside `web/e2e/run-tasks.spec.ts` comes with the
change that makes the rule true.

### Colour and contrast

Decided 2026-11-16 by the maintainers, #939.

Every grey is a step of one scale, the brand's `ink` (`neutral` in
`web/app/app.config.ts`, the steps in `web/app/assets/css/main.css`):
sage at 100, ink at 900, deep ink at 950, the steps between set by the
contrast they must give. No grey comes from Tailwind's zinc.

- The page and the canvas. Light pages are white (`--ui-bg`) beside a sage
  sidebar (`--ui-bg-canvas`, `bg-canvas`); dark pages are ink beside a
  deep-ink sidebar. A row on the canvas hovers to `bg-canvas-hover`, never
  to a raised tone, which vanishes into the light canvas; a chip or a
  square on the canvas takes `bg-default`.
- The text-safe status tones. Words in amber and green take
  `--ui-warning-text` and `--ui-success-text` (`text-warning`,
  `text-success`): in light the darker `#805000` and `#246B41`, in dark
  the fills themselves. A solid amber fill carries ink text. A status
  mark draws with the fills (`--ui-warning`, `--ui-success`), never the
  word tones.
- Dimmed text (`text-dimmed`) says nothing that is not said again
  elsewhere: a hint, a placeholder, a count beside its words.

The floors every text meets on the surface it is drawn on, in both themes
(`CONTRAST_FLOORS` in `web/app/utils/contrast.ts`):

| Text | Floor |
|---|---|
| Text | 4.5:1 |
| Large text (24 px, or 18.66 px at weight 700 and over) | 3:1 |
| Dimmed text on the page | 3:1 |
| Dimmed text on the canvas and the raised surfaces | 2.7:1 |

Dimmed text has two floors because Nuxt UI draws light dimmed text and
dark muted text with one step, which cannot be both. A mark (a status dot)
is held by no floor: the light amber mark is the brand's signal amber.

Checks: `themeContrast.test.ts` (every text token on every surface token
of both themes against its floor); `web/e2e/contrast.spec.ts` (each drawn
text over its real ground, on seven pages of the seeded day, both themes, at
1440 and 390, at rest and with a row hovered; `contrast-known.ts` lists
what is known faded, and it only shrinks); `designRules.test.ts`, the
floors (this table equals `CONTRAST_FLOORS`), no zinc, and the sidebar's
hover.

### One main action

Decided 2026-11-16 by the maintainers, #941.

One filled forest button a work area: a page's header, a dialog, a card.
The rest are outline or ghost. The forest fill means the view's main
action and nothing else.

Checks: `designRules.test.ts`, one filled action an area (in each
`UDashboardNavbar` and `UModal` at most one solid primary `UButton`, a
`v-if` chain's branches counted apart, a split button as one, a bound
variant not judged); `main-action.spec.ts`, the forest fill read off the
seeded day (`filledPrimaries` in `web/e2e/contrast.ts`).

### The code font for evidence

Decided 2026-11-16 by the maintainers, #941.

Ids, paths, commits, commands, keys, logs and measures are in the code
font (JetBrains Mono); sentences, names and labels are in Inter.

A line that mixes words with an id, a path or a commit keeps the code
font for that part alone, marked `data-mono="<kind>"` (id, path, commit,
command, key, log, measure); a number that ticks takes `tabular-nums`.

Checks: `designRules.test.ts`, the mono rule (`font-mono` only on code,
pre, a key, a field or an element marked `data-mono`; every other use a
count per source in `designRules.mono.json`, a list that only shrinks);
`main-action.spec.ts` reads the drawn family of sentences and ids.

### Words

Decided 2026-11-26 by the maintainers, #1158.

Conductor is used in its apps, the desktop app and the phone, from
anywhere. Its words never say it is used in a browser: they say "from
anywhere", "in the app" or "on this device". The word stays where a real
browser is meant: Open in browser, an agent's browser profile.

Not yet: the places `designRules.test.ts` lists as to be reworded, and
words built in scripts (toasts, the palette, the events table), which the
check does not read yet.

Checks: `designRules.test.ts`, the words (no "browser" in a template's text
or in an attribute written as text that a person reads: a label, a
description, a placeholder, a title; each place that still says it, or
that means a real browser, is listed there with its reason, and a new one
fails).

### Amber when someone must act

Decided 2026-11-16 by the maintainers, #941.

Amber is for what waits on a person: a question, a plan to approve, a
task out of attempts, a decision, a confirm that loses work, a read that
failed. Never a standing fact: a setting that is on, an agent that is not
installed, a count. The yolo badge says a setting is on, so it is
neutral wherever it is drawn, and a Yolo switch that is on is never amber.

Checks: `main-action.spec.ts`, amber: Not installed and an identified
agent's badge are neither amber nor green, the Yard's Needs you chip's
mark is amber; `yolo-badge.spec.ts`: the yolo badge in a session's header
and its sidebar row is not amber, in both themes.

### The page header

Decided 2026-11-16 by the maintainers, #934.

A page's header is `UDashboardNavbar`: the page's name first, never cut by
its own buttons (the buttons fold first); one filled action at most; on a
phone the labels fold into icon buttons, each an `IconButton` with its
tooltip, and the less used ones move into More.

Checks: `web/e2e/session-page.spec.ts` and `web/e2e/run-header.spec.ts`
(the name whole and nothing overlapping at 1440 x 900 and at 390 x 844);
the one-filled-action rule above; the tooltip rule below.

### What stays in view

Decided 2026-11-26 by the maintainers, #1158.

What a person uses all the time stays in view, one press away, under the
name people already use: Yolo, Accounts. How often a thing is used decides
whether it folds away, never how technical it looks; what is rarely used
may wait behind More or a fold, at the place it is needed.

A settings page folds nothing: its settings are the page. It is made
calmer with plainer words and a better order, never by hiding sections.

Checks: `designRules.test.ts`, what stays in view (no control bound to a
Yolo value inside a `UCollapsible`, a `UAccordion` or the forms' own fold
`MoreOptions`, and neither of the first two
on the settings page or in a card it draws; the places that still do are
listed there, each with its reason, and the list only shrinks).

### The toolbar

Decided 2026-11-16 by the maintainers, #927.

A list's toolbar (a Dispatch project's) is one row, in this order: the
views (List, Board, Roadmap), search, Filter (its filters in a popover,
the filters in use as chips beside it), the count, Views (the saved views,
naming the one applied), the sort. On a phone it may take a second line;
it never scrolls sideways.

Checks: `web/e2e/dispatch-toolbar.spec.ts`.

### The list row

Decided 2026-11-16 by the maintainers, #922.

A row of a list is a name line, then a meta line. Its cells sit in fixed
tracks, so that a column lines up down the list; an empty cell is kept
empty, never closed up. A state is said once, by its mark and its word,
never also by a badge or a tint. The sidebar's rows (#922), a run's task
rows (#926) and a Dispatch project's ticket rows (#927) are drawn so.

Checks: `aligned` and `uncut` (`web/e2e/layout.ts`) in
`web/e2e/sidebar.spec.ts`, `web/e2e/run-tasks.spec.ts` and
`web/e2e/dispatch-toolbar.spec.ts`.

### Cutting text

Decided 2026-11-15 by the maintainers, #653.

A text is cut (`truncate`, `line-clamp-*`) only inside a box whose width
comes from outside it: a block, a stretched flex item, or a grid track of
`minmax(0, …)`.

- In a flex column whose items are not stretched (`items-start`,
  `-center`, `-end` or `-baseline`, and every `<button>`, whose browser
  style starts its items at the start), the line that holds the cut takes
  `items-stretch` on the column, or `self-stretch` or `w-full` on itself.
  Otherwise the line is as wide as its content, and a `truncate` line's
  content is its whole text: it runs out of its row (the Needs you page's
  long titles, fixed by #912).
- In a flex row, a child that holds a cut deeper inside it takes
  `min-w-0`. The cut element itself needs none: it hides its overflow, so
  its automatic minimum is already 0.
- A name keeps its start, a path keeps its end (the file's name), an id
  keeps its start. A cut text carries its whole form in its `title`.
- A choice is never cut. A question is whole, except in the narrow
  sidebar, where it takes two lines beside its panel (#923).

Checks: `designRules.test.ts`, the cut rule (each cut's path up its
template, through the SFC parser, with the classes a `:class` names from
the string constants of `utils/`; it sees one template at a time, so what
it cannot see is listed there, and the list only shrinks); `uncut` in the
layout specs, for what crosses a component.

### Tooltips

Decided 2026-11-15 by the maintainers, #374.

An icon-only button is an `IconButton` (`web/app/components/IconButton.vue`):
a `UTooltip` over a `UButton`, whose `label` is the tooltip's text and the
button's accessible name, with an optional `kbd` for the key that does the
same. The tooltip shows while a mouse rests on the button and when Tab
brings the focus to it; never for a focus the app hands back to the button
when a menu, a popover or a dialog it opened closes (after an Escape, a
chosen item or a tap), and never at a touch, where the label stays the
button's name. A label hidden at some width (`hidden sm:inline`,
`max-sm:sr-only`, any variant of `hidden` or `sr-only`) counts as none, so
such a button is an `IconButton` too, its label inside it for the widths
that show it. An icon-only button already
inside a `UTooltip` of its own (a key in its tooltip, a side of its own)
may stay so.

A labelled button gets a tooltip where its label alone does not say what
happens. That part is for review, not for a machine: described, not
decided.

Checks: `designRules.test.ts`, the tooltip rule (a `UButton` with an icon
and no label a person reads at every width is inside a `UTooltip`; none is
allowed outside one); `web/e2e/tooltips.spec.ts` (the session page's More:
its tooltip on hover and on focus by Tab, in both themes, its menu by
Enter and by a click with no tooltip when the menu hands the focus back,
and its name and menu at a tap on a phone, no tooltip after; a Dispatch
project's Views, a popover's trigger, the same by click and by Enter).

### Fields that take an agent's words

Decided 2026-11-26 by the maintainers, #1158.

A field for an agent's arguments or its model (the Launch dialog's, Add
agent's, a crew member's) offers that agent's own flags and model names as
the person types, from what Conductor knows of the agent.

Not yet: these fields take free text today.

Checks: none yet; they come with the change that builds it.

### What a pull request cannot do

Decided 2026-11-16 by the maintainers, #953.

The maintainers keep a design-system page outside the repository, made
from a capture of the running app. No pull request can change it: a
maintainer publishes it at a release, by hand, a step on the release's
checklist (the body `scripts/release_draft.py ensure` gives the draft).

The capture is in the repository: `make design-capture` builds the app and
the server, drives the seeded day through every page of the pages' table,
a session, a run, a ticket and the Needs you page (with an item open, and
at zero on a server of its own), in both themes, at 1440 x 900 and at 390
x 844 (`web/e2e/look.spec.ts`, skipped unless `CONDUCTOR_E2E_LOOK=1`). The
screens stay on that machine, in `web/test-results/look/`; the values the
pages computed (every `--ui-*` token and ink step in each theme, each
status mark's word and colour, the ten texts nearest their floor) go to
`look.json` beside them. `scripts/design_drift.py` then prints what
drifted since the last release against `docs/design/system.json`: a token,
a status word or colour, a component or page added or gone, a rule whose
mark changed. At the release the maintainer reads it, publishes the
screens, and commits the new baseline written by `--write`.

Checks: none, by design: the capture is a step by hand, not a check, and
no picture is compared in CI. `scripts/design_drift_test.py` and
`scripts/release_draft_test.py` hold the report and the checklist.
