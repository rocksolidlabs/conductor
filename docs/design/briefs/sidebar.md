# Design brief: the sidebar, simpler

For Claude Design, project "Conductor Mockups v2". The mockup to work from
is **Conductor UI.dc.html** in that project:
<https://claude.ai/design/p/57dbb4ce-bb1c-40be-aed4-96d5c57ce623?file=Conductor+UI.dc.html>. Its 1-series is every screen as built today, dark and light; the
sidebar is on 1a Session, 1b Yard, 1d Run and 1j Crews home, and the phone
screens are in the project's uploads (`390-*.png`). Add the new sidebar as
a 3-series (3a, 3b, …) in that file, in its style and tokens, and leave
the 1-series as it is. Read `brand.md` first. Nuxt UI 4; dark default,
light exists. The live app is the reference for what exists today; this
brief says what is there and what confuses.

## What is there today

The sidebar is the session list. Three sections by state, **Needs you**,
**Running** and **Exited**, each holding three kinds of rows: loose
sessions (an avatar of the agent's initials, the name, a status dot, a meta
line such as "codex · hosted · 12m", an attention badge), **crew runs**
under a run header (the crew's name or the run's own name, the start time,
a link to the run page, its members as rows beneath), and **hosted
sessions** from another machine under a laptop header with the machine's
name. Above the sections, **Shared with you**: links this person opened
elsewhere, with a globe. On top: **Launch agent** (N) and a filter box (/).
At the foot: Yard, Roundhouse, Agents, Crews, Events, Settings, then a row
of icon buttons (notifications, your name, shortcuts, links, theme, sign
out). Collapsed, the sidebar is a **rail** of avatars grouped the same way.

A session row opens the session page. A run header opens the run page (the
tiles, the graph, stop, share). Stopping a session, sharing it or answering
it happens on its page, not in the list. Keyboard: G then a letter jumps to
a page; digits answer a prompt on a session page.

## What confuses

- Three nouns at once: a **crew** (a saved plan), a **run** (one launch of
  it, with its own name or the crew's and a time), and the **sessions**
  inside it. The owner, who built it: "still a bit confusing".
- A run's members are split across sections when one needs you and the
  others run, so a run shows twice.
- Where to act: nothing can be stopped, shared or answered from the list.
- The rail: it is not clear what the avatars stand for, or that a laptop
  icon is a machine.
- Shared with you sits above everything though it is used rarely.

## What to design

1. **One mental model**, drawn: how a person should think of sessions, runs,
   crews and machines, and which of these the sidebar lists. Propose it in
   a sentence before the screens.
2. **The sidebar, desktop (1440 × 900), on 1a's page, with everything at
   once:** two
   loose sessions (one needs you), a run of three members (one needs you,
   one exited), a hosted session from another machine, one shared link, and
   four exited sessions folded away. Show the attention first, without
   tearing a run apart.
3. **Actions on a row without opening it:** answer, stop, share, open the
   run; on hover and on a long press; keyboard too.
4. **The rail**, redrawn so that a glance tells a run from a session from a
   machine, with the unread and attention counts.
5. **Phone (390 × 844):** the list as the home screen and how a row's
   actions work there.
6. **What to drop**, with reasons.

## Rules

- The junction mark is artwork, never a status light; a session's state
  is drawn as the status section of [the design system](../system.md#status)
  says.
- Train words stay: Yard, Roundhouse, Switchyard, Conductor.
- Name every screen with an id in the 3-series (3a, 3b, …) inside
  Conductor UI.dc.html, so the build can be checked against it.
