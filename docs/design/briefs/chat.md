# Design brief: chat beside the terminal

For Claude Design, project "Conductor Mockups v2". The mockup to extend is
**Conductor UI.dc.html** in that project:
<https://claude.ai/design/p/57dbb4ce-bb1c-40be-aed4-96d5c57ce623?file=Conductor+UI.dc.html>. It holds every screen as built today, dark and light, with ids: 1a
Session (the inspector tabs People, Files, Activity), 1b Yard, 1c
Roundhouse, 1d Run (grid, graph, timeline, the broadcast bar), 1e Launch
agent, 1f Share, 1g Join, 1h Paste, 1i Agents, 1j Crews home, on to 1q
Settings. Add the chat screens to that file as a new series (2a, 2b, …)
drawn on 1a, 1g and 1d, in the same style, tokens and density; change
nothing in the 1-series. Read `brand.md` first (zinc surfaces, forest
`#263D35` for actions, terracotta on the junction mark only, Inter and
JetBrains Mono). Nuxt UI 4 components; dark is the default, light exists.

## What it is

A chat for the people watching a terminal together. One chat per session,
one per shared crew run. It rides the same connection as the terminal, so
it exists wherever a share link works: in the owner's workbench, on a
guest's join page, on a phone. The agent is not in the chat; a person with
control can send a message on to the agent, and the thread says so.

People are the join roster: a name they typed, a role (view or control), an
avatar of initials, a colour per person. Messages are plain text, at most
2 KiB, with links clickable; no reactions, threads, images or markdown in
this version. The last 200 messages are kept for a session, 500 for a run;
a session's chat ends with the session, a run's stays in its record.

## Screens to design

1. **Session page, desktop (1440 × 900), from 1a.** The terminal with the
   inspector open on a new **Chat** tab beside People, Files and Activity. Show: a
   thread of eight messages from three people (one view-only), one system
   line ("Jane joined · control"), one message marked **sent to agent** by
   Nate with the moment it went, the composer at the bottom (Enter sends,
   Shift+Enter is a new line, a counter past 1.5 KiB), and an unread count
   on the tab while it is closed. Also the closed state: the tab with "3"
   on it.
2. **Session page, phone (390 × 844).** The chat as a sheet over the
   terminal, opened from a chat button in the header with the unread count;
   the composer above the keyboard.
3. **Guest's join page, from 1g** (bare, no sidebar) with the chat open: the same
   panel for a person who only has the link, view-only, with the
   "sent to agent" action absent.
4. **Crew run page, from 1d,** with the run-wide chat as a right-hand drawer beside
   the member tiles: messages name which member a person is looking at when
   it matters ("on review"); a message sent to a member says which. The
   crew join page (the guest's grid of members) gets the same drawer.
5. **Unread elsewhere:** the sidebar row of a session and of a run with an
   unread count beside the attention badge, and the collapsed rail's avatar
   with a dot. A session's state is drawn as the status section of
   [the design system](../system.md#status) says; chat is neutral, never a
   status light.
6. **Empty and edge states:** an empty chat ("Nobody has said anything.
   Everyone on this link sees this chat."), a message that failed to send
   (offline), a chat on an ended session (read-only, "This session ended").

## Rules

- The junction mark is artwork, never a status light.
- No new colours; a person's colour comes from the roster's existing avatar
  palette.
- Phone first for the sheet; a 16 px gutter; no horizontal scroll.
- Name every screen with an id in the 2-series (2a, 2b, …) inside
  Conductor UI.dc.html, so the build can be checked against it.
