# Conductor's design system

The rules every screen of Conductor follows, each marked Decided (the
maintainers chose it, with the date and the issue) or Described (how the
app does it today). The brand itself, its colours, type and mark, is
[brand.md](brand.md); this file says how the screens use it.

## Status

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
| Running | `running` | plain green dot | running | The process is alive and its agent reports no turns, so Conductor cannot tell working from waiting. |
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
