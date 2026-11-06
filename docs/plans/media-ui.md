# Work huddles in the app: the room seam, the room protocol's client, and the screens for two people's audio, then screen sharing, then cameras (Program item CL1, the open-source part)

Status: proposed 2026-11-06, for the owner's review before any code. The
owner sets it to Planned by approving it; its PRs are then cut into
issues, one issue, one branch `issue-N`, one PR each (`AGENTS.md`).

A **huddle** is a live discussion attached to a piece of work, with its
own participants and access (the Program, section 1.4): two people talk
about a ticket, a decision, a review or a run while the work stays on the
page in front of both of them. Today, when two people need to talk a
decision through, they leave Conductor for a call tool and the work is no
longer between them. This plan is the open-source half of Program item
CL1: the seam a media provider uses to offer rooms (`ext`), the protocol
the app speaks to a room (`conductor-room/1`), the server's two routes
and two events, the huddle's one line on the ticket, a fake room service
that lets every screen be built and tested on one machine, and the
screens themselves, in the order the Program sets (section 1.3, 11): two
people's audio, then screen sharing, then optional cameras. Small-group
media is a later item with its own plan (Out of scope).

What runs a room is not in this repository. A build that wants huddles
compiles in a provider of `ext.MediaRoomProvider` from another module,
which answers where a room is and who is in it; this plan calls it "the
provider a build registers through the Media seam", and the service at
the URL its answer names "the room service". **A plain build of this
repository registers no provider, and then no huddle is offered and
nothing else changes**: no control is drawn, no room route is asked, no
device is touched, and the terminals keep the configured STUN servers
and the WebSocket relay exactly as on `main`. Every screen here is drawn
from what the server reports a registered provider answers
(`GET /api/capabilities` and `GET /api/rooms`), never from a plan, a
subscription or a flag (`CONTRIBUTING.md`, The open-core rules; decision
D27 in the Program, section 3.1).

It was written against `main` at `76d3e655`, and brought in its first
review round to its specification (the hosted plan's section on what it
needs from this repository) as that plan's own first round left it,
after reading the Media seam
(`ext/media.go`, `ext/host.go`, `ext/ext.go`, `ext/local/defaults.go`,
`ext/exttest/fakes.go`'s `FakeMedia`, `ext/exttest/checks.go`'s `Media`),
the guard and the ICE sites (`internal/api/extensions.go`'s `guardMedia`,
`internal/api/media.go`, `exthost.go`, `notify.go`), the capabilities
route and its reader (`internal/api/capabilities.go`,
`web/app/composables/useCapabilities.ts`, `web/app/utils/capabilities.ts`),
the open-core lint (`internal/opencore`), the events stream
(`internal/api/events.go`, `docs/protocol.md`), Dispatch's links
(`internal/dispatch/links.go`, `model.go`: `LinkDecision` reserved), the
terminals' WebRTC (`web/app/utils/transport/webrtc.ts`,
`internal/hostagent`, `docs/protocol.md`, Hosted sessions: signaling and
relay), the desktop shell (`desktop/src/window.ts`'s
`permissionAllowed`, `udp-forwarder.ts`, `firewall.ts`,
`build/installer.nsh`, `build/entitlements.mac.plist`,
`electron-builder.yml`), the e2e harness (`web/e2e/global-setup.ts`,
`server.ts`, `web/playwright.config.ts`), and the plans whose pages a
huddle attaches to: `docs/plans/decisions.md` (DR1),
`docs/plans/decision-inbox.md` (MB0), `docs/plans/review-repair.md`
(RV1), `docs/plans/ext.md` (C1 to C9).

Names follow the railroad (the Program, section 1.2): Conductor is the
application, Yardmaster the agent harness, Dispatch the ticketing (its
items are tickets). The app's words say **huddle**; the seam and the
protocol say **room**, the word `ext.MediaRoomProvider.Room` already uses.
Neither collides with a word the repository uses for something else
(article 11).

**What is on `main` and usable now.**

- C8: `ext.MediaRoomProvider` with `ICEServers(ctx, ICERequest)` and
  `Room(ctx, RoomRequest) (RoomJoin, bool, error)`. Three of the four ICE
  sites ask the provider through the guard (`ICEWait`, 2 s; each server
  `Valid`; at most `MaxICEServers`); `GET /api/ice` never does.
  `ICERequest` is `{Purpose, Session, Role, Viewer}` and names no
  principal. `Room` has **no caller**, and `RoomJoin` has **no `Valid`**
  (`ext/ext.go`: "Room has no caller until Program item CL1").
  `RoomRequest` is `{Kind, ID, By}`; `RoomJoin` is `{URL, Token,
  ExpiresAt}`. The free default (`ext/local.Media`) adds no server and
  answers no room. `FakeMedia` and the `Media` check exist.
- C2: `ext.IdentityProvider`, asked after the server's own token chain
  (`internal/api/identity.go`). The workbench token's principal has **no
  subject** (its receipts go under `admin`); only a provider's principal
  has one, and its receipts go under `user:<subject>`.
- C9: `GET /api/capabilities` (admin only) lists each registered provider
  by kind, name and state; `has(caps, 'media')` in the app.
- Dispatch's ticket page (`/dispatch/<project>/<number>`,
  `DispatchTicketPage.vue`), DS2's candidates on it, and the run page
  (`/runs/<run>`). A ticket keeps at most `MaxLinks` (50) live links
  (`internal/dispatch/model.go`, counted at `links.go`'s `link.add`), and
  `DispatchLinks.vue` lists every kind but `run` and `decision` as a
  delivery or an external link.
- The events stream `GET /api/events` with its `snapshot` on reconnect,
  read by `useAttention.ts`; a client that cannot keep up with an event
  the page cannot do without is dropped and starts over from a snapshot
  (`internal/api/events.go`).
- `github.com/pion/webrtc/v4` and `github.com/coder/websocket` in
  `go.mod` already: nothing here adds a Go module.
- The desktop app forwards UDP for the terminals' ICE from Windows into
  WSL (`desktop/src/udp-forwarder.ts`); its installer adds that port's
  firewall rule only when it runs with administrator rights
  (`build/installer.nsh`; `electron-builder.yml` installs per user,
  `perMachine: false`), and otherwise the app adds it through an
  elevated `netsh` (`firewall.ts`); its permission handler allows the
  clipboard and fullscreen only.

**Not on `main`, and which rows wait for it.**

- DR1's decision records and their card (`DecisionCard.vue`), the ticket
  page's Decisions section (`DispatchDecisions.vue`): issues #634 to #641;
  PR-8 waits on #640 (DR1's web row) and #637 (its routes).
- MB0's decision page `/decisions` (`InboxItem.vue`): #647 and #652;
  PR-8 waits on both.
- RV1's Review view (`?view=review&candidate=…`): #631; PR-8 waits on it.
- A **reviewer**: a principal admitted to one decision, and to nothing
  else, for a limited time (its **admission**, which ends on its own),
  and the events stream such a reviewer reads, which carries that
  decision's events only. Both are Program item ID1's open-source rows,
  which have no issue in this repository yet; PR-11 waits on them and
  names them by issue number once they are filed. Every earlier row
  serves the admin grant alone.
- The server inside WSL reachable from outside Windows: issue #140 (the
  desktop app forwards the TLS listener into WSL and maps the router from
  Windows). A clean install has no TLS listener to forward: the app starts
  its server on loopback HTTP alone (`desktop/src/env.ts`), and the server
  opens a TLS listener only when ACME or certificate files are configured
  (`internal/config`'s `TLSEnabled`). PR-14 is that issue's work and the
  listener's, done in this stream because the acceptance's phone needs it.
- A provider: outside this repository. No row here waits on one; the
  item's acceptance (below) is observed with one. On a phone a huddle
  runs in the phone's browser; nothing here waits on a phone app.

**Who owns what this item shares.**

| Shared piece | Owner | The other side |
|---|---|---|
| The Media seam (`ext.MediaRoomProvider`, `RoomLister`, `ICERequest.By` and `Conn`, `ICEEnder`, the three `Host` doors, their records, bounds and waits) | this plan | a provider implements and calls them at a tag holding PR-1, PR-15 and PR-4 |
| The room protocol `conductor-room/1`: its records in `ext/room.go` (aliased in `internal/proto`), its TypeScript in `protocol.ts`, its words in `docs/protocol.md` and its vectors | this plan (PR-2) | a provider's room service speaks it at the tag that defines it and passes the vectors |
| The server's room routes and events, the `huddle` link kind | this plan (PR-4) | Dispatch's store (DS1) keeps the kind; a sync provider (Program item CS1) replicates it as every link |
| The pages a huddle sits on | DS1 (ticket page), the run page, DR1 (`docs/plans/decisions.md`: the card), MB0 (`docs/plans/decision-inbox.md`: `/decisions`), RV1 (`docs/plans/review-repair.md`: the Review view) | this plan adds the huddle control to each, in its own rows placed after the row of theirs it builds on (PR-5; PR-8 after #631, #637, #640, #647 and #652) |
| The reviewer and the reviewer's stream | Program item ID1's open-source rows | PR-11 admits a reviewer to a decision's room and adds the `room` event to that stream |
| A decision's answer, a review's findings | DR1, RV1 | a huddle answers, routes and applies nothing; the outcome is recorded by those pages, as without a huddle |

## Articles it serves

The articles are the Program's (section 2.1).

1. **No premium code, no licence check.** The seam, the protocol's
   records, the fake room service and the screens are open source and
   any provider uses them; the only check is whether a registered
   provider answers (`offered`), never a plan or a tier.
2. **The open-source build is fully useful.** Nothing in a plain build
   changes: no huddle control, no request to the room routes, no device
   asked for (`huddle-offer.spec.ts` proves it on the plain e2e server).
3. **Paid features are gated by hosted dependencies.** A room exists only
   at the URL a registered provider answers, and only its room service
   admits anyone; no flag, setting or build tag here opens one. The fake
   room service is compiled only into a test binary (Owner decisions, 2).
4. **Stop, inspect, revoke, export, approvals free.** Joining grants
   nothing: the join reply is the provider's ticket, which no route of
   this server accepts (`TestJoinGrantsNothing`), and no room message
   reaches the server. A huddle's only durable trace is one Dispatch link
   written through `Host.RecordHuddle`; every safety control is unchanged
   with a call in progress, a provider hanging or a room service gone
   (`TestRoomFaultsTouchNoSession`).
5. **Dependencies point one way.** `ext` stays standard library only; the
   fake is under `internal/`; nothing imports a private module (the
   `private-module` and `ext-std` lint rules hold).
6. **No feature build tag.** None is added. The e2e server with the fake
   provider is built from a `_test.go` file by `go test -c`, never by a
   tag.
7. **The free default is what `main` does.** `ext/local.Media` answers no
   room and implements no `RoomLister`, so `available` is false and the
   screens draw nothing; the ICE sites only say whose connection they ask
   for (`ICERequest.By`), and what they configure is unchanged.
8. **Unreachable or lapsed answers as the free default, mid-session.**
   Every new call goes through the guard (`ICEWait`); late, failing,
   panicking or invalid answers are "no huddle offered", call by call;
   the control disappears at the next read and comes back with no
   restart. A call in progress is ended by its room service's words
   (`ended`) or after 60 s without it; nothing else on the page changes.
9. **CGO off.** No new Go module; the web app adds no npm package; the
   desktop app adds none.
10. **The repository contract.** This plan before code; one issue and one
    PR per row; Go race, vitest and Playwright per surface, desktop tests
    for the shell; the protocol in its three places together,
    `internal/proto`, `web/app/utils/protocol.ts` and `docs/protocol.md`
    (Owner decisions, 1); every new frame, message and route with a size
    limit on its encoded bytes and a test at that limit.
11. **Names keep their meaning.** "Huddle" in the app, "room" in the seam;
    no railroad name is reused; "workspace" is not used for anything
    here.
12. **Execution behind one seam.** Not touched: nothing here starts,
    stops or runs a process, and media never passes through the server.

## Why and what happens

Conductor puts people and agents around the same work. The decision
waiting on a person, a candidate's diff and its checks, a ticket's
requirements: all of it is on a page. What is missing is the
conversation, and the Program's direction is that it happen on the page,
attached to the work (section 1.3, 11), never as a channel that can
approve, launch or stop anything (CL1's requirements: joining grants
neither terminal control nor decision authority; a conversation never
authorizes execution; a media fault never interrupts a session, a run or
a decision; nothing is recorded).

After this item ships, on a server whose build registers a media
provider that answers:

- **A huddle on the work.** The ticket page, its Review view, the run
  page and a decision's card show **Huddle** in their header. Pressed,
  the app shows "Conductor will ask your browser for the microphone",
  then the browser's own prompt, then a device check (the microphone, the
  speaker where the browser can choose one, a level meter, Join muted),
  and only then joins. A second person who opens the same work sees
  "Ana is in a huddle · Join".
- **Two people talk.** The two browsers connect to each other; the work
  stays on the page; a docked strip at the page's foot (a bottom sheet on
  a phone) shows who is in, who is speaking and who is muted, the
  elapsed time, Mute, the devices, Invite, Show them and Leave. The call
  lives in the app's layout, so moving between pages keeps it, and
  "In a huddle on PRJ-12 · mic on" in the sidebar's header names it from
  every page.
- **Show them.** One person points the other at a ticket, a run, a
  task, a candidate or a decision; the other's page offers
  "Ana is pointing at task build · Go", which their own app turns into a
  page of their own server, which opens it or refuses ("You can't open
  this on your server"). Go only navigates.
- **Invitations, removal, reconnect.** Invite lists who the room service
  says may join; the open Conductor pages of the server the invitation
  reached, and only its admin pages, show "Ana invites you to a huddle on
  PRJ-12 · Join" (on a server of several people: "Ana sent this server
  an invitation", since the server cannot tell which of them it names). A dropped network shows
  "Reconnecting…" and comes back into the same huddle with the mute state
  kept. A person removed, or whose access to the work ended, is told so
  in plain words, their devices are released at once, and the others'
  browsers close their connections to them, so a removal holds even
  against a client that ignores it.
- **Screen sharing, then cameras.** One person at a time shares a screen
  or a window into a panel beside the work; cameras come last, off by
  default, asked for on their own press.
- **The outcome is recorded by the work.** The decision is answered on its
  card, a review is sent as a review, as without a huddle; when the
  huddle ends, the ticket's history gets one line, "Huddle with Ana and
  Ben · 17 min": who and how long, nothing said.

What does not change: a plain build, a server whose provider is starting,
unreachable or lapsed, and every page that is not one of the four show no
huddle and nothing else moves. The terminals' own WebRTC, its ICE and its
relay are a different connection with a different lifecycle; a huddle
opens none of them and closes none of them.

## The changes

### Who is who: how the seam and the screens tell people apart

A room admits **people**, and the seam names a person by the principal
the server authenticated, as every other seam does (`ext.Principal`):

- **The server's operator** (the workbench token): `By` is the principal
  with an empty `Subject`, an empty `Name` and the admin grant. A
  provider takes it as one person, this server's operator, and names it
  from what it holds of the server. A server whose only credential is the
  workbench token has **one person**: two browsers holding that token are
  the same person, and the second that joins a room ends the first
  ("Elsewhere"). It is the seam's rule, not the e2e's.
- **Several people on one server**: an identity provider's principals
  (C2), each with its own `Subject`. Two of them in one room are two
  people; `By.Subject` is what a provider tells them apart by, and it is
  never the client's word.
- **A reviewer** (PR-11): a principal admitted to one decision; `By` is
  its principal with its subject, and the server asks a room for it only
  for that decision. The room service admits a reviewer only with the
  reviewer's own **pass** (the credential ID1's open-source rows give a
  reviewer's page), which the page sends in `join` and `renew` and the
  room service checks itself; a server's word alone admits only its own
  operator. A reviewer's page with no pass joins nothing and says why.
- **A person vouched for by another server** (a room whose scope admits
  other servers' operators): the room service names them with `via`, the
  name of the server that vouched for them; the screens say "Ben, through
  Ana's server".
- **One person's second device or tab**: the same subject, so the same
  person. Joining the same room from a second device ends the first with
  `ended {why: "elsewhere"}` ("You joined this huddle from another
  window."); joining another room while in one is refused with `4409`
  and the reason `elsewhere` ("You are in another huddle; leave it
  first."). **The other participant drops the old device by itself**,
  whatever the old device does with its end and whatever the room
  service tells it first: the person id is the same on both devices, so
  the client tells a connection apart by the DTLS fingerprint in its
  SDP (each `RTCPeerConnection` makes its own certificate). A device that
  receives `joined` offers to every person it lists, so the new device
  always offers; an offer from a person whose fingerprint is not the one
  of the connection the client already has with them comes from another
  place of theirs, and the client closes that connection **before** it
  answers the offer on a new one (I15). So the old device's media stop
  at the new device's first offer, or at `left {person}` if that comes
  first (I8).
- **A person removed, or whose access ended, mid-call**: the room service
  ends their place (`ended {why: "removed" | "access" | "unconfirmed"}`),
  tells everyone else `left {person, why}`, and refuses their resume
  token and any fresh join of theirs to that room (`4403 removed`). Every other client closes its connection to
  them on that `left` and drops their signals from then on (I8), so a
  removed client that ignores its own end receives nothing more.

Inside a room, the protocol names a person by the room service's id for
them (`person`, 1 to `MaxRoomPerson` (64) bytes of `[A-Za-z0-9_-]`), one
per person and the same for their second device; the app compares only
these ids, never names. Tests, one per case: `TestRoomRequestByOperator`
and `TestRoomRequestBySubject` (`internal/api`: what `By` carries for the
workbench token and for a provider's principal),
`TestFakeSameSubjectIsOnePerson`, `TestFakeTwoSubjectsTwoPeople`,
`TestFakeElsewhereInAnotherRoom`, `TestFakeRemovedCannotResume`
(`internal/roomfake`), and in the browser `huddle-audio.spec.ts` (two
principals of one server talk), `huddle-states.spec.ts`'s "elsewhere"
(two contexts of one principal), "in another huddle" and "removed" (the
removed context made to ignore its end).

### `ext`: what the Media seam gains (PR-1)

The additions are fields, constants, records, one optional interface and
three methods of `Host`. Nothing is removed or renamed, so no provider
breaks (`ext/ext.go`, Stability); `Host` is implemented only by the
server and `exttest.FakeHost`, both changed in the same row.

- **`RoomJoin` checked.** Two new fields, `Room string` (`json:"room"`, 1
  to `MaxRoomID` (64) of `[A-Za-z0-9_-]`) and `Protocol string`
  (`json:"protocol"`, `RoomProtocol` = `"conductor-room/1"`, another value
  refused). `ValidAt(now time.Time) error`: `URL` printable ASCII of at
  most `MaxRoomURL` (512) bytes, scheme `wss`, or `ws` only to a loopback
  host (`127.0.0.1`, `[::1]`, `localhost`: the fake's); `Token` 1 to
  `MaxRoomToken` (4,096) bytes of the token characters `[A-Za-z0-9._~+/=-]`
  (RFC 7235's `token68`, which every JSON encoder writes as they are, so
  a `join` carrying it fits its frame); `ExpiresAt` set, after `now` and
  at most `MaxRoomTicket` (10 minutes) past it. `Valid()` is
  `ValidAt(time.Now())`; the guard calls `ValidAt` with its own clock so
  the tests drive it. The sentence in `ext/ext.go` saying `RoomJoin` has
  no `Valid` goes.
- **`RoomRequest` says where the work is, and a renewal.** Three new
  fields: `Ticket` (the immutable id of the ticket the work belongs to,
  Dispatch's row id of 16 lowercase hex digits, the same on every
  replica, never its key; `""` for none), `Home` (for another machine's record shown here, the device id
  it names; `""` for this server's own work) and `Renew` (the room
  session the asker is already in, from `joined.session`; `""` for a
  join; a provider may let a renewal through where it lets no new join),
  each at most 64 bytes of printable ASCII, `Renew` of `[A-Za-z0-9_-]`;
  `Kind` one of `RoomKinds` (`ticket`, `decision`, `review`, `run`); `ID`
  1 to `MaxRoomWorkID` (200) bytes of printable ASCII without spaces (a
  ticket's id is its immutable id, as `Ticket`; a review's is its
  candidate, `<run>/<task>/<n>@<commit>`). `By` is the
  principal (Who is who). `Valid()`. The server fills `Ticket` from its
  own records, never from the client (Owner decisions, 8); the provider
  decides what the room is.
- **`ICERequest` says whose connection it is.** `By ext.Principal`
  (`json:"by"`): the viewer's principal for a hosted session's viewer
  (the operator's for the workbench token, a provider's principal with
  its subject), the zero `Principal` for a share link's viewer of either
  role, the host's for a registration (a host token's: an empty subject
  with the host grant), and the server's own, the operator's, for a paste
  answer. A provider can then give relay credentials to the server's
  authenticated principals and never to a link's viewer. It is filled at
  the three guarded sites (`ws_viewer.go`, `ws_host.go`, `paste.go`'s
  answer); `GET /api/ice` still asks no provider.
- **Each connection's own servers, and its end** (PR-15). Today the
  guard's context ends when `ICEServers` returns, and a host is given one
  list at registration that it uses for every viewer's peer, so a
  provider can neither tell one connection's credential from another's
  nor learn when a connection ended. `ICERequest` gains `Conn` (an id the
  server makes for that one connection, 1 to 64 of `[A-Za-z0-9_-]`, never
  reused), and a provider may implement an optional `ICEEnder {
  ICEEnded(conn string) }`, which the server calls once when the
  connection it named ends: a viewer's socket or peer connection closed,
  a host's peer to one viewer gone (`viewer_closed`, `viewer_leave`, the
  host's own end), a paste answer's peer closed. The calls go through a
  queue of 1,024 that never blocks and drops its oldest (counted in a
  debug line), each under the guard with `ICEWait`. A host's peer to a
  viewer gets its own servers: at `viewer_join` the server asks the
  provider for that viewer's connection (`Purpose` `host`, `Viewer` and
  `By` the viewer's, so a share link's viewer gets none here either) and
  sends them in `viewer_join.iceServers`, to a host whose `register` says
  it reads that field (its protocol version); the host uses them for that
  peer alone and its registration's list otherwise; an older host keeps
  the registration's list, which then carries the configured servers
  alone. That ask is made on the viewer's own connection before the
  hosted session sends `viewer_join`, never under the session's lock or
  on the host's connection, under the guard with `ICEWait` and cut short
  by the viewer's end, so a provider that hangs delays that one viewer's
  join by at most `ICEWait` and holds nothing of the session, its host or
  its other viewers (`TestHostViewerICEAskHoldsNoSession`: the provider
  hanging in that ask while the host's output reaches a viewer already
  in and another viewer joins). **A host's own explicit list comes first**: a host started with
  `--stun` (`internal/cli/host.go`; `Options.ICEServers`, which
  `internal/hostagent/agent.go` puts over the registration's list today)
  uses that list for every peer, as on `main`, and its `register` does
  not say it reads `viewer_join.iceServers`, so the server asks the
  provider for no per-viewer servers for it and none is minted unused.
  The order is the host's explicit list, then the viewer's own servers,
  then the registration's list (`TestHostICEPrecedence`, one case each).
  The host messages change in the protocol's three places.
- **Presence: `RoomLister`, an optional interface** a provider implements
  beside `MediaRoomProvider`:
  `Rooms(ctx, refs []RoomRef) (RoomsAnswer, error)`, safe for concurrent
  use, answered from what the provider holds. `RoomRef {Kind, ID, Home,
  Ticket}` (`Ticket` filled by the server as for a join, so a provider can
  tell an empty review or run room's scope before anyone is in it; the
  guard matches the answer's rooms to the refs by kind, id and home); at
  most `MaxRoomRefs` (32) per call. `RoomsAnswer {Available bool, Rooms
  []RoomState}`, **one `RoomState` for each ref asked, an empty room
  included**, matched to the refs by kind, id and home in whatever order
  it comes, so an empty room and an unknown ref never look the same. `RoomState {Kind, ID, Home, Room, Since time.Time
  (zero while empty), Scope ("d": only this server's principals may join;
  "w": other servers' operators the provider admits too), InviteURL (an
  `https://` URL of at most 512 bytes that brings a person with no server
  of their own to this work, or `""`), People []RoomPerson, More int}`;
  `RoomPerson {Subject (at most MaxSubject), Name (1 to MaxPrincipalName
  characters, one line), Via (the name of the server that vouched for
  this person when it is not their own, at most MaxPrincipalName
  characters, else ""), Muted, Sharing, Camera bool}`; at most
  `MaxRoomPeople` (8) people, `More` the count past them. Each record has
  `Valid`. A provider that does not implement it, `Available` false, an
  error, a late, panicking or invalid answer: **no huddle offered**.
- **Three `Host` doors**, each never blocking the provider:
  - `RoomChanged(ref RoomRef)`: a room this server shows changed. The
    server coalesces calls per ref, at most one event a second each, and
    sends `room {kind, id, home}` (PR-4).
  - `RoomInvited(inv RoomInvite)`: an invitation that reached this
    server. `RoomInvite {Kind, ID, Home, Room, By {Subject, Name}, Until
    time.Time}`, `Until` at most `MaxRoomInvite` (10 minutes) ahead;
    `Valid`. It names no recipient: the provider is told of invitations
    to this server's person, and the record carries nothing that tells
    one of this server's principals from another, so the server never
    claims to know which of them it is for. The server keeps at most
    `MaxPendingInvitations` (32) pending, one per room (a newer
    invitation of a room replaces the older), the oldest dropped, and
    sends `room_invite {…}` to admin principals' events connections only
    (PR-4). `Host.Notify` hands
    a notice to the notification sinks only (webhooks, a push provider),
    never to the app's pages, so this door, not a notice, is what the
    open app shows.
  - `RecordHuddle(ctx, HuddleRecord) error`: a room session ended.
    `HuddleRecord {Kind, ID, Room, Since, Ended time.Time, People
    []{Subject, Name} (at most 8), More int}`, `Valid` (Ended after Since
    and at most 24 hours later). The server writes one Dispatch link
    (PR-4); idempotent by `Room` and `Since`. It names this server's own
    work only: a room on another device's record is recorded by that
    device's server.
  PR-1 declares the three and the server's `extHost` answers them as
  stubs (the two events dropped with a debug line, `RecordHuddle`
  `ErrUnsupported`), so `main` stays green; PR-4 builds them, as C1's
  PR-1 declared `Host` and its PR-4 built it (`docs/plans/ext.md`).
- **The guard** (`internal/api/extensions.go`, `guardMedia`): `Room`'s
  answer must pass `ValidAt`, else it is ok false; a `Rooms` method on
  the guarded value, through `extCall` with `ICEWait`, answering
  `RoomsAnswer{}` (not available) for a provider without `RoomLister`,
  and refusing (not available) an answer that is not exactly one valid
  state per ref asked, matched by kind, id and home, so a provider can
  neither show a room
  for work this server did not ask about nor leave one out.
- **`ext/exttest`**: `FakeMedia` implements `RoomLister` with `SetRooms`
  and `RoomsRequests()`, and records each `ICERequest`; `FakeHost`
  records `RoomChanged`, `RoomInvited` and `RecordHuddle` (with a
  scripted error); the `Media` check calls `Room` and wants `ValidAt` to
  pass whenever ok is true, calls `ICEServers` with an operator's, a
  subject's and the zero principal, and, for a provider that implements
  `RoomLister`, calls `Rooms` within `ICEWait` from `Goroutines`
  goroutines, with the network open and blocked, and wants a valid answer
  of one state per ref.

Constants (`ext/media.go`): `RoomProtocol`, `RoomKinds`, `MaxRoomURL`
512, `MaxRoomToken` 4096, `MaxRoomTicket` 10 min, `MaxRoomID` 64,
`MaxRoomPerson` 64, `MaxRoomWorkID` 200, `MaxRoomHome` 64, `MaxRoomRefs`
32, `MaxRoomPeople` 8, `MaxRoomInvite` 10 min, `MaxPendingInvitations`
32, `MaxHuddleSpan` 24 h, `MaxRoomPass` 2048 (PR-2).

### The room protocol `conductor-room/1` (PR-2)

The protocol between the app and a room URL, in the three places every
protocol of this repository is in (`AGENTS.md`; Owner decisions, 1): its
records in `ext/room.go`, standard library only, so a provider's room
service in another module uses the same records; `internal/proto/room.go`
naming the same records and constants as aliases of `ext`'s, with
`internal/proto`'s own run of the vectors; its TypeScript in
`web/app/utils/protocol.ts` (a section of its own); its words in a new
section **Rooms** of `docs/protocol.md`. Every message is one JSON text
frame `{"t": "<type>", …}` decoded with `DisallowUnknownFields`.

**Bounds are on encoded bytes, escapes included**, under both encoders
the two ends use (Go's `encoding/json`, which writes `<`, `>`, `&`,
U+2028 and U+2029 as six-byte escapes, and `JSON.stringify`). A string
that may hold such characters (a name, a work id, a message) is counted
at six bytes a character in the frame's budget; an id of
`[A-Za-z0-9_-]`, a ticket or a pass of token characters and a candidate of the ICE
grammar's characters (letters, digits, space and `.:/+-_`) are written as
they are. Each frame's bound is the sum of its fields at their bounds so
counted, rounded up to the KiB, and `TestRoomFrameBudgets` builds every
message at that worst case through Go's encoder and the vectors through
the TypeScript one and wants each inside its bound:

| Frame | Bound |
|---|---|
| `joined` (eight people, eight ICE servers of four 512-byte URLs and 256-byte secrets) | `MaxRoomJoinedFrame`, 48 KiB |
| `ice` (eight ICE servers) | `MaxRoomICEFrame`, 44 KiB |
| `signal` carrying an SDP (the `sdp` at most `MaxRoomSDP`, 32 KiB encoded) | `MaxRoomSignalFrame`, 40 KiB |
| `invitees` from the room service (a page of 50) | `MaxRoomInviteesFrame`, 20 KiB |
| `join`, `renew`, `resume` (a 4,096-byte ticket and a 2,048-byte pass in their envelope) | `MaxRoomTicketFrame`, 8 KiB |
| every other frame (a `candidate` at most 1 KiB) | `MaxRoomFrame`, 4 KiB |

**Bounds on what accumulates** (the frame bounds above say nothing of
how many frames wait): the app sends at most 20 frames a second, holding
the rest in an outgoing queue of at most 256 frames and 1 MiB, and the
socket's own buffer (`bufferedAmount`) is read before each send; a queue
or a buffer past its bound is a socket that does not drain, which the
app closes and resumes (The call, Liveness). On the receiving side each
peer connection holds at most 64 remote candidates waiting for a remote
description (the terminals' own cap, `utils/transport/webrtc.ts`), the
65th and later dropped and counted, and at most 32 signals waiting for
the negotiation step in progress (perfect negotiation runs one at a
time); a 33rd closes that connection, which is then negotiated anew by
the retry rule (The call, ICE). The fake room service holds the same
kind of bounds (PR-3).

`DecodeAppFrame(b []byte) (AppFrame, error)` and
`DecodeServiceFrame(b []byte) (ServiceFrame, error)` check the frame's
size for its type (read from a bounded first look at `t`, the largest
bound applying until then) before decoding, and each field's bound after.

App to room service:

| Message | Fields and bounds |
|---|---|
| `join` | `ticket` (the `RoomJoin.Token`), `pass?` (a reviewer's own pass, 1 to `MaxRoomPass` (2,048) bytes of the token characters; PR-11); the first frame, within 10 s |
| `resume` | `token` (from the last `joined.resume`: 43 characters of base64url, one use); in place of `join`, within 60 s of a drop |
| `renew` | `ticket` (a ticket the server answered for `renew` = `joined.session`), `pass?` (as in `join`); every `joined.renewEvery` seconds |
| `signal` | `to` (a `person`), `sdp?` `{type: offer \| answer, sdp}`, `candidate?` `{candidate, sdpMid?, sdpMLineIndex?}` (`sdpMid` at most 16 bytes); exactly one of the two |
| `state` | `muted`, `sharing`, `camera`, `screenMid?`, `cameraMid?` (the `mid` of the transceiver that sends the screen and of the one that sends the camera, each 1 to 16 bytes of `[A-Za-z0-9_-]`, sent before the offer that carries them: browsers mark nothing in an SDP that tells a screen from a camera, so the room service checks the `mid` of each video section the SDP's writer **sends** (direction `sendrecv` or `sendonly` in that SDP) against these, and the other side shows a track by its `mid`. A section the writer only receives (`recvonly`, as in the answer of a person who shares nothing), one that is `inactive`, and one rejected with port 0 carry nothing of the writer's and are not checked: an answer keeps the offer's sections and says only their direction, RFC 3264, sections 6 and 6.1) |
| `invitees` | `after?` (a `next` from the last page): ask whom one may invite; an operator's place only |
| `invite` | `person`; an operator's place only |
| `remove` | `person`; an operator's place only |
| `point` | `kind`, `id`, a reference, never text; `kind` one of the seven `PointKinds`, each id at most 200 bytes and of its pattern: `ticket` a ticket's immutable id (16 lowercase hex digits); `decision` `dr-` and 1 to 10 digits; `run` a run's id as `crew.ValidRunID` reads it (a crew id `[a-z0-9][a-z0-9-]{0,63}`, `-`, and 8 lowercase hex digits: at most 73 bytes); `task` `<run>/<task>`, the task id `[a-z0-9][a-z0-9_-]{0,39}`; `candidate` `<run>/<task>/<n>@<commit>` (DS2's form, `n` 1 to 3 digits, the commit 7 to 40 lowercase hex digits); `finding` its number on the review shown, 1 to 9999; `file` `<path>@<commit>`, the path relative, 1 to 160 bytes, with no `..` segment, no leading `/` and no `\`, the commit 7 to 40 lowercase hex digits. Anything else is refused by both ends, and the room service relays only these. The wire's grammar is the specification's; what the app sends and opens is narrower (Show them, below) |
| `stats` | `path` (`direct`, `relay`), `rttMs`, `lossPct`, `kbpsOut`, `kbpsIn` (each 0 to 1,000,000), `kinds` (a subset of `audio`, `screen`, `camera`); at most every 30 s |
| `leave` | none |

Room service to app:

| Message | Fields and bounds |
|---|---|
| `joined` | `you` `{person, name}`, `room`, `session` (the room session, 1 to 64 of `[A-Za-z0-9_-]`, the same across a resume), `people` (at most 8 `person` records), `iceServers` (at most `MaxICEServers`, each `ICEServer.Valid`), `resume` (the next resume token), `kinds`, `maxPeople` (2 to 8), `endsAt`, `renewEvery` (30 to 300) |
| `person` | `person`, `name`, `via` (`""` or the vouching server's name), `muted`, `sharing`, `camera`, `screenMid?`, `cameraMid?`, `state` (`in` \| `reconnecting`) |
| `left` | `person`, `why` (one of `EndReasons`) |
| `signal` | `from` (set by the room service: a `person`), `sdp?`, `candidate?` |
| `invitees` | `people`: at most 50 of `{person, name}`; `next?` |
| `invited` | `by` `{person, name}`, `room`, `kind`, `id`, `home` |
| `point` | `from`, `kind`, `id` |
| `ice` | `iceServers` (fresh relay credentials) |
| `ending` | `at` |
| `ended` | `why` (`EndReasons`: `left`, `removed`, `access`, `unconfirmed`, `elsewhere`, `time`, `unavailable`), `by?` `{person, name}` |
| `error` | `code` (`rate`, `media_kind`, `not_allowed`, `no_room`, `bad_frame`), `message` (within the frame's bound; never shown or logged: the screens word an error by its `code`) |

Close codes, constants of `ext`: `4400` bad frame or no `join`, `4401`
bad ticket, pass or resume token, `4403` not allowed (the close reason
`not_allowed`, `removed` or `not_entitled`), `4409` refused (the reason
`room_full` or `elsewhere`), `4410` ended (after an `ended`), `4429` too
many, `4503` unavailable, `1001` service stopping. A `1001` close or a
dropped socket is **not** an end: the app resumes. The protocol has no
keepalive message (a room service's WebSocket pings, which the browser
answers, are invisible to a page), so the app has no way to tell a quiet
room from a room service that stopped answering on a socket it keeps
open, and does not try (The call, Liveness). The room service refuses an
SDP, answering `error {code: "media_kind"}` and relaying nothing, that
sends a kind the room does not allow, a video section whose `mid` its
writer has not declared in its last `state`, a screen while the other
person shares, or more than two sending video sections; it handles one
connection's frames in order, so a `state` sent before an offer is
always read first. This version of the protocol has no forwarded mode: the
messages a group needs are the later groups plan's, added with their own
vectors.

**Vectors**: `ext/testdata/room/app.json` and `service.json`, one valid
frame of each message at its largest with escaped strings (among them a
`join` and a `renew` with a 4,096-byte ticket and a 2,048-byte pass,
which decode), for each bound one frame one byte (or one item) past it,
each of the seven `point` kinds at its pattern's edges and one past (a
`run` with a 64-byte crew id, 73 bytes, alone and in a `task` and a
`candidate`), and `state` with each mid at 16 bytes and 17, each with
the error it must get. `ext`'s `TestRoomVectors`,
`internal/proto`'s `TestRoomVectorsThroughProto` and
`roomProtocol.test.ts` read the same files, so the Go records and the
TypeScript cannot drift; a provider's room service runs them too.

**What the app points at is narrower than the wire** (`pointable(kind,
id)` in `utils/huddle.ts`, used both to offer a point and to offer Go):
a `run` and a `task` as the wire takes them, which are
`crew.ValidRunID`'s and the task id's own grammar already; a `candidate`
only when it is in **both** the wire's grammar and Dispatch's
(`dispatch.ParseCandidate`: the attempt `[1-9][0-9]{0,5}`, the commit in
full, 40 or 64 digits), so a candidate with an abbreviated commit is
never opened and one with a 64-digit commit or an attempt past 999,
which the wire cannot carry, is not offered as a point; `finding` and
`file` never (Show them, below). `ext` imports nothing of the
repository, so the cross-contract check is in the app and in
`internal/api`: `TestPointableMatchesTheValidators` runs a shared list
of `run`, `task` and `candidate` ids (`ext/testdata/room/points.json`:
a 73-byte run, a six-digit attempt, a 64-digit commit, an abbreviated
commit, a three-digit attempt with a 40-digit commit) through the wire's
pattern, `crew.ValidRunID` and `dispatch.ParseCandidate` and wants
`pointable`'s verdict, which `huddlePoint.test.ts` reads from the same
file, to be the wire's and Dispatch's together.

### `internal/roomfake`: the fake room service, the fake provider and the fake people (PR-3)

So that every screen is built and tested here with no provider of the
real kind, `internal/roomfake` holds:

- **`Service`**, an `http.Handler` that speaks `conductor-room/1` on a
  loopback listener with `coder/websocket` and the `ext` records' decoders:
  tickets it mints itself (`fake1.<claims as base64url>.<HMAC-SHA256
  under a key made at start>`, claims `{room, kind, id, home, subject,
  name, renew, exp}`, 60 s of life, one use); one person per subject
  (Who is who: the same subject in the same room ends the earlier
  socket with `elsewhere`, in another room is closed `4409 elsewhere`);
  presence, `session`, renewal (a ticket whose `renew` is not the current
  session refused), resume within 60 s with a one-use token rotated at
  each `joined` (a token past its minute or already used `4401`), a
  removed person's resume and fresh join to that room refused (`4403
  removed`),
  `invitees` from a list the test sets, paged,
  `invite` and `remove` from an operator's place, `point` (the kinds'
  patterns checked), `stats` (counted, dropped), the end reasons, the
  close codes, `maxPeople` 2, `kinds` audio
  (screen from PR-9, camera from PR-10; an SDP's `m=` lines and their
  direction attributes read by a small scanner, a video section the
  SDP's writer sends refused while the kind is off, a received,
  `inactive` or rejected one never). Its own bounds: at most 20 frames a
  second from a connection with a burst of 40 (past it `error {code:
  "rate"}` and the frame dropped), at most 256 frames waiting to be
  written to one person and 1,024 held for them by `hold` (past either,
  that person's socket closed with no `ended`, as `drop` does).
  `iceServers` is empty: no relay, the two browsers on one machine
  connect on host candidates. It never stores or logs a frame's contents.
- **Control seams for tests**, on the same listener under `/control/`,
  each behind a random control token: `drop` (close one person's socket
  with no `ended`, so the app resumes), `refuse-resume`, `end?why=`,
  `remove?person=`, `full`, `ending?in=`, `hold` and `release` (hold
  every frame to one person until released, the socket left open),
  `stop` and `start` (close the listener and every socket with no close
  frame, as a stopped process does, then reopen the listener),
  `expire?person=` (the next renewal refused, so the place ends
  `unconfirmed`), and `GET /control/state` (people, their states, frames
  counted by type). Tests wait on these, never on a clock.
- **`Provider`** (`fake-media`): `MediaRoomProvider`, `RoomLister` and
  `ext.Starter`. `Start` opens the listener on `127.0.0.1:0` and writes
  `<DataDir>/room.json` (`{url, control, controlToken, people}`, mode
  0600, through a `.new` file renamed over it); `Room` mints a ticket for
  the principal (an empty subject with the admin grant is the person
  `operator`, named "Operator") and answers `{URL:
  ws://127.0.0.1:<port>/rooms, Token, ExpiresAt, Room, Protocol}`;
  `Rooms` answers one state per ref from the service's state, scope `d`;
  each presence change calls `Host.RoomChanged`, each `invite`
  `Host.RoomInvited`, and a room left
  empty (closed after a grace the test sets, default 1 s)
  `Host.RecordHuddle` once. `ICEServers` adds none and records the
  requests' `By`.
- **`Identity`** (`fake-people`): an `ext.IdentityProvider` that, at
  start, mints one random bearer token per name in
  `CONDUCTOR_ROOMFAKE_PEOPLE` (default `Ana,Ben`) and answers each with
  the principal `{Subject: "fake-<name>", Name: <name>, Grants: [admin]}`;
  the tokens go only into `room.json`'s `people`. Two people on one
  server for the e2e, beside the workbench token, which stays the
  operator.
- **The e2e server binary**, `internal/roomfake/e2e/main_test.go`: its
  `TestMain` registers `roomfake.Provider` and `roomfake.Identity` and
  calls `app.Main()` when `CONDUCTOR_ROOMFAKE_SERVE=1`, else runs the
  package's tests. `make build-roomfake` builds it with
  `go test -c -o bin/conductor-roomfake ./internal/roomfake/e2e`;
  `make test-e2e` depends on it. No release target, `make build` or
  `cmd/conductor` ever contains the fake (Owner decisions, 2), and
  `internal/opencore`'s `TestReleaseLeavesOutTheFakes` proves it: the
  dependency set of `./cmd/conductor` and `./app`, read with
  `go list -deps`, holds no `internal/roomfake` package, and a planted
  import is caught.

### `internal/api` and `internal/dispatch`: the routes, the events, the huddle's line (PR-4)

`internal/api/rooms.go` (new):

- **`GET /api/rooms?ref=<kind>:<home>:<id>`**, 1 to 32 `ref` parameters,
  each split at its first two colons (`home` empty for this server's own
  work, so `ticket::<the ticket's immutable id>`; an id may hold `@` or
  `:`, as a review's does) → `{available bool, rooms: [RoomState and offered]}`.
  `available` is true only when the registered provider implements
  `RoomLister` and answered `Available` true through the guard. Each
  ref's `offered` is true only when someone other than the asker could
  join: the room's scope is `w`; or someone else is in it; or it is a
  decision's room whose admission admits a reviewer (PR-11); or this
  server authenticates principals through an identity provider, so others
  of its principals may join. A server with the workbench token alone
  and an empty `d` room offers nothing: one person cannot huddle with
  themselves. Each state carries at most three people (the avatars the
  screen draws) and `more` the rest. Each ref must name work the
  principal may read and that exists here (a ticket's immutable id; a run, live or
  recorded; a decision once DR1's records are on `main`, PR-8; a
  **candidate that exists**: Dispatch's `ParseCandidate` takes its form,
  and then, as `internal/api/dispatch_delivery.go`'s `candidateVerified`
  reads it, the ledger holds its run, the run's crew or task rows name
  its task, the task has that attempt, and the attempt's result commit is
  the candidate's commit, whether or not its checks passed; a ledger
  that cannot be read now admits none, `existingCandidate` shared by
  both routes); a ref that does not is dropped from
  the question with no word, so the route tells nobody what exists; the
  server sets each ref's `Ticket` from its own records before it asks. The
  reply is at most 256 KiB, the sum of 32 states at their bounds with
  every string at its worst escaping (`TestRoomsReplyBounded` builds that
  answer). `400 invalid_request` for a malformed or 33rd ref.
- **`POST /api/rooms/join {kind, id, home?, renew?}`** (body at most 512
  bytes, `DisallowUnknownFields`) → `200 {url, token, expiresAt, room,
  protocol}` (at most `MaxJoinReply`, 8 KiB: the URL's 512 bytes at six
  encoded bytes each, 3,072, since a URL may hold `&`, `<` and `>`, which
  Go's encoder escapes; the token's 4,096 written as they are; the room
  id's 64; the time and the keys under 200; 7,343 in all;
  `TestJoinReplyBounded` builds that reply through the route and wants it
  inside the bound) or `409 {error: {code: "no_room"}}` when the
  provider answers ok false, fails, is late, panics or answers a join
  `ValidAt` refuses, and, before any provider is asked, when the work
  does not exist here by the same reading as the rooms route's (a
  candidate that `existingCandidate` does not find, a ledger that cannot
  be read now), so the join tells nobody more than the rooms route does.
  The server builds the `RoomRequest`: `By` the
  principal, `Renew` the body's `renew`, `Ticket` from its own records
  (the ticket's own immutable id; a decision's ticket; the ticket whose
  links hold a run's or a candidate's chain; `""` for none), never from
  the client;
  a body naming a ticket is refused by `DisallowUnknownFields`. At most
  30 a minute per principal (`429 rate_limited`). No receipt is written:
  joining is not an operation on the work.
- **Who may ask** (both routes): the admin grant, for every kind; never
  the host grant, a share link's viewer, an agent's token or an unknown
  token (`401`, as other admin routes). PR-11 admits a reviewer for the
  decision it is admitted to. A `home` ref needs a remote record of that
  device and id held here (Program item CS1's); until such records exist
  on `main`, a ref with a home is dropped.
- **The events**, on `GET /api/events`:
  - `room {kind, id, home}` to admin principals (at most 2 KiB), coalesced
    per ref, at most one a second; the app reads `GET /api/rooms` again.
    The coalescer holds the refs waiting for their second in a set of at
    most 256; a 257th collapses the set into one `room {}` with no ref,
    which tells every page to read all its rooms again, so no change is
    ever lost to a full set. `room` is an event the page cannot do
    without: a client that cannot keep up is dropped and starts over from
    a snapshot, as for sessions and runs (`events.go`), and the app reads
    its rooms again on the snapshot.
  - `room_invite {kind, id, home, room, by: {subject, name}, until}` (at
    most 3 KiB), sent only to the events connections of admin
    principals: the hub keeps each connection's grant beside its
    channel. Never to a share link's viewer or a reviewer, who joins from
    the decision page they were given. It names no recipient, and the app
    words it by what the server knows (the state table, Invitation
    received).
- **`extHost`'s doors**: `RoomChanged` feeds the coalescer;
  `RoomInvited` puts the invitation among the 32 pending (one per room,
  the newer replacing the older, the oldest dropped past 32), neither
  ever blocking the provider.
  `RecordHuddle` replays before it resolves anything, as Dispatch's
  replay contract asks (`Store.Replay(ctx, op, intent, args)`,
  `internal/dispatch/ops.go`, which compares the operation's
  fingerprint of its intent and arguments):
  - **The arguments are the room session, never the destination.**
    `LinkHuddleArgs {Room, Since}`, the room session the provider
    names, is what the operation's fingerprint covers; the rest of the
    `HuddleRecord` (`Kind`, `ID`, `Ended`, `People`, `More`) is the
    link's content, taken from the call that writes it, and the ticket
    the link goes on is resolved after the replay. So a retry finds the
    first answer whatever else it carries: a run moved to another
    ticket, or a person renamed, between a try and its retry changes
    nothing, as the specification asks (a second call for the same room
    and `since` writes nothing). The op id is `h-` and 30 characters of
    base32 of the SHA-256 of `Room` and `Since`.
  - **Only a done receipt answers nil.** `found` with a done receipt (a
    repeat, or after pruning the link's add row found by the changes
    log's first row with the same fingerprint, as `prunedAnswer` does
    for `link.add`): nil, nothing written. `found` with a kept
    refusal: that refusal as an error (`ErrHuddleRefused`
    wrapping its words), so the provider never takes a huddle that was
    not recorded as recorded. Not found (never run, or refused and its
    receipt pruned, which leaves no row): the ticket resolved now as the
    join does; none, nil and nothing written; one, Dispatch's
    `link.huddle` with these arguments and that ticket.

`internal/dispatch`: a link kind `huddle` (`LinkHuddle`), reserved as
`LinkDecision` is: refused on `link.add` (`RefusalInvalid`, "the kind
huddle is written by a huddle's end only"), kept when a merge brings one.
**Huddle links are counted apart, and nothing evicts them**: `MaxLinks`
(50) counts every other kind, so huddles never take a place an ordinary
link needs and a ticket already holding 50 links still records its
huddle. A local `link.huddle` is refused (`RefusalBound`, "this ticket
holds 200 huddles") when the ticket already holds `MaxHuddleLinks`
(200) live `huddle` links: that huddle is not recorded, and
`RecordHuddle` answers the refusal. No operation of a build holding
PR-4 removes a huddle link,
so the links are an add-only observed-remove set that converges in any
delivery order (DS1's set rule, `internal/dispatch/rules.go`) with no
eviction to reconcile; a merge keeps what it brings, as it keeps
ordinary links past `MaxLinks` today, so two replicas may together hold
more than 200. **The read is what is bounded**: a ticket's read and its
history list at most the newest 200 huddle links, ordered by `since`
descending and then by `ref`, with the count of the rest ("and 14
earlier huddles"), so every replica holding the same links shows the
same 200 (`TestHuddleLinksMergeConverge`: two replicas of 150 each,
batches delivered in both orders and repeated, the same stored set and
the same 200 read on each). A new operation `link.huddle`
(`Store.AddHuddle(ctx, ticketID, LinkHuddleArgs, HuddleLink, op Op)`, the link's content beside the arguments) writes it with
one receipt and the link's fingerprint (so a pruned receipt still
replays, as `link.add`'s does), its actor `provider:<name>` (at most 49
bytes, inside `MaxActor`), its op id as above. The link's `ref` is
`<room>@<since as RFC 3339>`; its `note` is "Huddle with Ana and Ben · 17
min" (names Markdown-escaped, "and 3 more" past four, the duration in
whole minutes; at most 200 bytes). A ticket's read grows by at most 200
such links of at most 1 KiB each, however many a merge brought.

**Older builds never write a store that holds huddle links.** A build
from before PR-4 knows no `huddle` kind: its `link.remove`
(`internal/dispatch/links.go`'s `RemoveLink`) removes a link of any kind,
and its `link.add` counts every kind against `MaxLinks`. So the kind
being text is not enough, and PR-4 raises Dispatch's schema `Version`
by one, with a migration step that changes no table. Dispatch's open
already refuses to write a database of a newer version ("written by a
newer Conductor", `internal/dispatch/dispatch.go`, which then opens
read-only): an older binary started on the data directory after PR-4's
has run (a downgrade) reads the tickets and writes nothing, so it can
neither remove a huddle link nor refuse an ordinary link because of one.
Replication between machines (Program item CS1) is not on `main`, so no
replica of an older build exists to receive a huddle link. The rule for
it is the same one: a replica applies no change row written under a
newer Dispatch schema version than its own, so a huddle link never
reaches a build that does not know the kind. Should CS1's apply of
replicated rows reach `main` before PR-4 without that rule, PR-4 adds it
there (the version carried with each row, a newer one refused and kept
for later), with `TestReplicaRefusesNewerSchemaRows`.

`web/app/components/DispatchLinks.vue` leaves `huddle` out as it leaves
out `run` and `decision`, so a huddle never shows as an external link
with an empty URL and a remove control; the history shows it (PR-6).

Docs: `docs/protocol.md`'s HTTP API rows for both routes, the two events
and the link kind, with their bounds; `web/app/utils/protocol.ts` types
and `protocol.test.ts` cases for them.

### `web/app`: the screens (PR-13, PR-5, PR-6, then PR-8, PR-11, PR-9, PR-10)

Components come from Nuxt UI with the brand palette (`app.config.ts`:
forest primary, terracotta only as the restrained accent; operational
status in its own scales); the junction mark appears nowhere in a huddle
and is never a status light (`docs/design/brand.md`). Speaking, muted,
sharing and relayed are each an icon and words, never colour alone; every
state change is announced in an ARIA live region (`HuddleAnnouncer.vue`).

**Files.**

- `utils/protocol.ts` (PR-2): the room records, `encodeAppFrame`,
  `decodeServiceFrame` with the bounds.
- `utils/roomClient.ts` (PR-13): `RoomClient`, the protocol's client and
  the call's state machine (The call, below), with every outside thing
  injected (the WebSocket and `RTCPeerConnection` constructors, a clock,
  the server's join call), so `roomClient.test.ts` drives it whole with
  fakes and fake timers.
- `utils/huddle.ts` (PR-5): the words of every state and end, `offered`,
  `secureEnough()` (`window.isSecureContext`: the desktop app's
  `http://127.0.0.1` origin is a secure context, a LAN `http://` address
  is not); `pointRoute(room, kind, id)` (PR-6, Show them below).
- `composables/useHuddle.ts` (PR-5): the app's one call (`useState`), the
  presence of the refs the shown page registers, the device check's flow.
  **The call belongs to the credential it started under**: the
  workbench token `useWorkbenchToken()` holds when Join was pressed (a
  person's own token for an identity provider's principal; for a
  reviewer, PR-11, the principal its admission names, so a pass renewed
  for the same reviewer keeps the call and one for anyone else ends it).
  A watch on that token, kept for the app's lifetime as
  `useCapabilities.ts`'s `followToken` keeps its own, does on any change,
  a token forgotten or another one taken: the call ends at once (every
  track stopped, every peer connection closed, `leave` sent on the old
  socket and the socket closed), "The huddle ended: you signed out" is
  shown, the pending invitations and the rooms' presence are cleared and
  read again under the new token, if there is one. Every step that waits
  (the join or renewal request, `getUserMedia`, `getDisplayMedia`, the
  socket's opening, the device check) carries the generation of the
  token it began under, and a result that arrives under another
  generation is discarded: a late join reply opens no socket, a late
  track is stopped at once and never shown, as `useCapabilities.ts`
  drops a reply for another token. So Ana's join, answered after the page
  took Ben's token, never opens a call on Ben's page.
- Components: `HuddleControl.vue` (a page header's control and presence),
  `HuddleDeviceCheck.vue` (a modal), `HuddleDock.vue` (the layout's strip),
  `HuddlePeople.vue`, `HuddleIndicator.vue` (the sidebar header's line),
  `HuddleAnnouncer.vue` (PR-5); `HuddleSheet.vue` (the phone),
  `HuddleInvite.vue`, `HuddlePoint.vue` (PR-6); `HuddleScreen.vue` (PR-9);
  `HuddleCameras.vue` (PR-10).
- `layouts/default.vue` mounts `HuddleDock` (below `sm`, `HuddleSheet`)
  and the announcer; `SidebarHeaderMenus.vue` shows `HuddleIndicator`.
- `useAttention.ts` takes the `room` and `room_invite` events from the
  one stream it holds (no second stream).

**Where the control is.** The ticket page's header (`kind ticket`, the
ticket's immutable id) and the run page's header (`kind run`) from PR-5; the
Review view (`kind review`, the candidate shown), each decision's card on
`/decisions` and in the ticket page's Decisions section (`kind decision`,
`dr-n`) from PR-8. A remote record shown "on <device>" gets a control
only when the room is open to other servers' operators (`RoomState`
with people in and scope `w`). A page registers its refs with
`useHuddle().show(refs)`; the composable reads `GET /api/rooms` once for
them (one read in flight, one queued), again on a `room` event for one of
them or a `room {}` for all, on the stream's `snapshot`, when the page
comes back into view, and, while a media provider is registered and the
last answer was `available` false, once a minute, so a provider that
comes back shows its huddles with no presence change.

**Whether anything is drawn.** For an admin principal the app first
reads the capabilities it already reads (`useCapabilities`): with no ready
`media` provider it never asks `GET /api/rooms` and draws nothing. With
one, `available` and each ref's `offered` decide. A reviewer, who cannot
read the capabilities, asks `GET /api/rooms` for its decision (PR-11).

**What each screen shows, in each state.**

| State | What the person sees |
|---|---|
| Not offered (plain build; provider starting, unreachable or lapsed; `available` false; the ref not `offered` because no one else could join) | nothing: no control, no empty space, no message |
| Offered, page not a secure context | the control disabled, its tooltip "Huddles need a secure address (https)" |
| Offered, room empty | **Huddle**, a microphone icon |
| Offered, people in | "Ana is in a huddle · Join", up to three avatars and "+n"; muted and sharing marks |
| Device check (after Join, before any media) | "Conductor will ask your browser for the microphone." Then the browser's prompt; the microphone list, the speaker list where `setSinkId` exists, a level meter, Join muted, Join and Cancel. Cancel stops the track at once |
| Microphone blocked | "The microphone is blocked for this page. Allow it in the browser's site settings, then try again." In the desktop app on macOS: "Allow the microphone for Conductor in System Settings, Privacy & Security." Try again |
| No microphone | "No microphone was found." Join to listen only, or Cancel |
| Joining | "Joining…" then "Connecting audio…"; "relayed" beside a person whose selected candidate pair is a relay (from `getStats`, PR-6) |
| In the huddle | the people (name, speaking, muted, sharing; a person vouched by another's server as "Ben, through Ana's server"), the elapsed time; Mute (the M key while the focus is in no text field, terminal or editor, listed in the `?` list), the device menu, Invite (an operator's place only), Show them, Leave; Share screen from PR-9, Camera from PR-10. A docked strip at the page's foot that never covers a decision's answer buttons (the page gains the strip's height as bottom padding) |
| One person's audio not connecting | beside that person, "Can't connect to Ben's audio · Try again" once the client's tries are spent (The call); the rest of the call goes on |
| Invite (an operator's place: an admin principal, this server's own work) | the people the room service lists (`invitees`, a page at a time, "More"); "Invited Ben" for 10 minutes, then gone; "Copy link" when the room has an `InviteURL` |
| Invitation received | a toast from `room_invite`, worded by what the server knows: on a server whose capabilities list no identity provider, its one person is the one invited, "Ana invites you to a huddle on PRJ-12 · Join · Dismiss"; on a server with one, "Ana sent this server an invitation to a huddle on PRJ-12 · Join · Dismiss"; no toast for the room the page's own call is in. Join opens the work's page in its device check when this server holds the work, else "This huddle is on another server; you can't join it from this server." The invitation grants nothing: the join that follows is the person's own, under their own grant |
| Show them | sent: "Pointed Ben at task build"; received: "Ana is pointing at task build · Go"; Go navigates to the page the app builds itself, or shows "You can't open this on your server"; a kind or an id this app does not open (a finding, a file, a candidate Dispatch would refuse): "Ana pointed at something this app can't open yet", with no Go |
| Room full | "This huddle is full (2 people)." Nothing else changes |
| In another huddle | "You are in another huddle; leave it first." (`4409 elsewhere`) |
| Removed | "Ana removed you from the huddle." Media stopped, devices released |
| Access ended | "Your access to this huddle has ended." (`access`, `unconfirmed`); a reviewer whose admission ended sees the decision page's own words (PR-11) |
| Access ending (a reviewer, five minutes before the admission ends; PR-11) | "Your access ends in 5 minutes · Renew access": opens the decision page's own renewal in a new tab; the renewed admission reaches this tab's next renewal and the call goes on; the new tab says "Access renewed; you can close this tab" |
| Elsewhere | "You joined this huddle from another window." |
| Reconnecting | "Reconnecting…" with the mute state kept and the call silent both ways until the room service answers again; back within 60 s, or "Could not reconnect · Rejoin". On a phone: "Your phone paused this page; the huddle comes back if you return within a minute" when a locked screen or another app in front suspends it |
| Service unreachable | "Reconnecting…", then 60 s after the socket to the room service closed with no answer since, or at `ended {why: unavailable}`: "The huddle ended: the huddle service is unreachable." |
| Ending soon | "This huddle ends in 5 minutes (its <n>-hour limit)." (`ending`; the limit from `endsAt` and the room's `since`), then `time`: "The huddle reached its time limit." |
| Screen sharing (PR-9) | Share screen opens the browser's picker (the desktop app's own); "Starting to share…" until the other side's answer takes the screen (The call, One screen at a time), then "You are sharing your screen · Stop"; the others see it in a resizable panel beside the work, fit or actual size; Share screen disabled with "Ben is sharing; ask them to stop first" while the other's latest `person` says sharing; a claim refused or given up: "Only one screen can be shared at a time; sharing didn't start · Try again", or "Sharing didn't start · Try again" when no answer came; "Sharing stopped: the huddle reconnected" after a reconnect; not offered on a phone |
| Cameras (PR-10) | Camera off by default, asked for on its press (blocked and missing as the microphone's); small tiles beside the people; never on a phone's collapsed sheet |
| Phone (below `sm`, 640 px) | a bottom sheet above the bottom bar: collapsed, the names, Mute and Leave; expanded, the people, the devices, Invite; a shared screen full width above the sheet; the page scrolls above it; audio continues with the sheet collapsed while the page is in front |

**Deliberate device access.** `getUserMedia` is called only from a press
(Join, Try again, Camera, Share screen through `getDisplayMedia`), with
the browser's own echo cancellation, noise suppression and gain control
left on. Every track the app made is stopped at Leave, at every end, at
Cancel and at `pagehide` (which also sends `leave`), and the browser's
indicator goes off with it. The chosen microphone and speaker ids are
kept for this browser in `localStorage` (`conductor.huddle.devices`, every
read and write in `try`); nothing else of a call is stored anywhere.

**Show them only navigates, and only to a page that exists.** A
received `point` is checked against `PointKinds`' patterns again, and
`pointRoute(room, kind, id)` builds the route from the room the call is
on and the kind and id alone, never from a path or URL in the message,
and only to a page that is on `main` when its row merges. PR-6: `ticket`
the ticket page of the ticket with that immutable id; `run` the run page
(`/runs/<run>`); `task` the run page at that task (`?view=tasks&task=`,
which `pages/runs/[run].vue` reads today). PR-8, after RV1's Review view
(#631) and DR1's and MB0's pages: `decision` its card on `/decisions`;
`candidate` the Review view of that candidate, when `pointable`.
`finding` and `file` are never sent and never opened: a finding's number
names no one finding once a candidate has two reviews (RV1 gives each
review its own findings, each numbered from 1), and no page of this
repository shows a run's committed file (the run page has no Files view;
a session's Files tab shows a live worktree, not a commit); a received
one answers "Ana pointed at something this app can't open yet" with no
Go, and a later row that gives either a page and an id that names one
thing adds it. Before PR-8, `decision` and `candidate` answer the same
words. A room on another device's record that
this server does not hold, or a kind the room gives no run, answers
"You can't open this on your server" and offers no Go. Go calls the router's `push` and nothing else: no route that changes
anything is ever called from a point, and the page it opens is read with
the person's own grant, which refuses what they may not read.

**The call** (`RoomClient`, PR-13). After `joined`, one
`RTCPeerConnection` per other person `in`, with
`joined.iceServers`, an audio transceiver, trickled candidates through
`signal`, and perfect negotiation (the polite side the one whose `person`
sorts higher). Mute sets the track's `enabled` false and sends `state`;
nothing is renegotiated. **Who offers a new connection**: the client
that receives `joined` offers to each person it lists; a client already
in waits for that offer; a failed connection is made anew by the
impolite side alone (ICE, below), the polite side closing its own and
waiting. Every other offer is a renegotiation of a connection both
sides hold. The work this takes is bounded (Bounds on what accumulates,
in the protocol section).

- **One place per person** (I15): the client keeps the DTLS
  fingerprint of each connection's remote description. An offer from a
  person whose fingerprint differs from the one the client's connection
  with them took comes from another place of that person (a second
  device, or the same client after its own reconnect): the client closes
  that connection, stopping its senders, **before** it answers the offer
  on a new one. When the client's own offer on a new connection is
  waiting for its answer, perfect negotiation decides first (the
  impolite side ignores the other offer; the polite side gives up its
  own new connection and answers).

- **Someone leaves, or is removed** (I8): on `left {person}`, whatever its
  `why`, and on its own `ended`, the client stops its senders to that
  person and closes the peer connection with them **before** it draws the
  change, and drops every `signal` whose `from` is not in its people list
  as the room service last gave it. Since a direct call's media goes
  browser to browser, this is what stops a removed person's audio,
  whatever their own client does.
- **No connection outlives a gap in the room service's word** (I13).
  A peer connection carries media only while both its ends are `in` by
  the room service's latest word, and none is kept across a gap in that
  word, so a removal, a revocation or a replacement by the person's
  other device that the client missed while it could not hear the room
  service never leaves media flowing:
  - on entering `reconnecting` the client sets every sender's track to
    `null` (`replaceTrack`) and mutes every received track, so nothing
    is sent or played while it cannot hear the room service;
  - on the `joined` that ends it (after a `resume` or a fresh `join`)
    the client closes **every** peer connection it had and negotiates a
    new one with each person `joined.people` lists `in`, and with no one
    else: `joined` is the room's roster, and a person missing from it
    (removed, or gone, while the client was away) gets no connection; a
    person who changed device is reached on their current device, since
    the room service routes signals only to a person's current place;
  - on `person {state: "reconnecting"}` for someone else, the client
    closes its connection with that person and negotiates anew only
    when they are `in` again, so a gap on either side ends the media
    between them.
  The price is a moment of silence after every reconnect, and a call
  with a lost connection is negotiated again from nothing: an
  inconvenience, where a connection kept to someone no longer in the
  room is the defect.
- **ICE.** A new `ice` updates every connection through
  `setConfiguration` and restarts ICE on a connection whose selected pair
  is a relay. An ICE failure restarts ICE; a connection that fails again
  is closed and negotiated anew after 2 s, then 4 s, then 8 s while both
  people are `in`, whether or not the room service reports a change (the
  impolite side offers; I15 makes the other side drop its old one); an
  offer with no answer within 10 s is rolled back and counts as a
  failure; after the third, the person shows "Can't connect to Ben's
  audio · Try again", whose press starts the tries over.
- **One screen at a time** (PR-9). The room service refuses a screen
  while the other person shares (an offer carrying it answered `error
  {code: "media_kind"}` and not relayed); the app adds no claim message
  and waits for no echo, and it shows or sends a screen only when it is
  certain:
  - *Asking.* Share screen is offered only while the other person's
    latest `person` says `sharing` false and the client has no offer of
    its own waiting. After the picker, the client checks that again
    (else it stops the capture and says so), sends `state {sharing:
    true, screenMid}`, and then the offer that adds the screen on a
    transceiver of its own, made `sendonly`.
  - *The grant* is the other side's answer to that offer, applied, with
    the screen's section accepted (its port not 0). Only then does the
    app say "You are sharing your screen"; a browser sends nothing on a
    section before its answer is applied, so nothing of the screen
    leaves before the grant.
  - *Refused or given up*, every case ending the same way (the capture
    stopped, the transceiver stopped, the offer rolled back if it is
    still waiting, `state {sharing: false}` without `screenMid`, and the
    words of the state table): `error {code: "media_kind"}` while that
    offer waits (a client has one offer waiting at a time, so the error
    is that offer's); the other person's `person {sharing: true}` before
    the grant; the polite side's own offer given up in a collision; no
    answer within 10 s. Two presses at once can thus end with one sharer
    or with none, never with two.
  - *Released* at Stop, at the capture's own end (the browser's stop
    button), at Leave, at every end, and on entering `reconnecting`:
    the capture is stopped and not taken up again after the reconnect
    (a new press is needed). Each sends `state {sharing: false}` when
    the socket is open.
  - *Shown* by the other side only from the person whose latest
    `person` says `sharing` with a `screenMid` equal to the track's
    `mid`, on the connection it holds with that person now; any other
    video track is not drawn.
  The client never puts a track on a transceiver the other side opened,
  so every video section it sends carries the `mid` it declared, and a
  section it only receives is `recvonly` in its answer. Cameras (PR-10)
  follow the same rules with `cameraMid`, one per person, with no
  one-at-a-time rule.
- **Renewal.** Every `renewEvery` the client asks
  `POST /api/rooms/join` again with `renew` set to `joined.session` and
  sends `renew` with that ticket; a refused renewal is tried at the next
  tick, and the room service decides when to end (`unconfirmed`).
- **Liveness.** A dropped socket, a `1001` close, a `join` or `resume`
  with no `joined` within 10 s (the socket then closed by the client),
  and a socket that does not drain (the outgoing queue or
  `bufferedAmount` past its bound, closed by the client) start
  **reconnecting**: a new socket with `resume` (backoff 0.5 s doubling to
  8 s; a first `join` that got no `joined` has no token to resume with,
  so the client takes the `4401` branch below at once: one fresh ticket
  and `join`). 60 s after the socket closed with no `joined` since, the call
  ends with the unreachable words. A socket that stays open is the room
  service's word: the app sends no keepalive (the protocol has none) and
  never ends a call because a room is quiet. So a room service that
  stops answering while its socket stays open is not noticed by the
  app; one that stops (its process ended, its machine's connection
  closed) closes the socket, and the call ends at 60 s (I11). A person
  is never held by a quiet room service: the two browsers' connection
  carries on as the room service last left it, Leave ends the call on
  their side at once with no answer needed from the room service (every
  track stopped, every peer connection closed, the socket closed), and
  nothing of a session, a run or a decision waits on the room service
  or its socket (I4).
- **Which refusals end the call** (I14). A resume's answer is read by
  its close code, and only one is recoverable:
  - `4401` (the resume token unknown, used or past its minute): the
    client asks its server for a fresh ticket once and sends `join`; a
    second `4401`, or a `409` from the server, ends the call ("Could not
    reconnect · Rejoin");
  - `4403` with any reason (`removed`, `not_allowed`, `not_entitled`),
    `4409` with any reason (`elsewhere`, `room_full`), `4410`, and any
    `ended`: **terminal**. The client shows that end's words, stops
    every track, and asks its server for nothing; Rejoin is a person's
    press, never the client's own;
  - a dropped socket, `1001`, `4503` or `4429`: another `resume` after
    the backoff, within the minute.
  So a person removed while their socket was down meets `4403 removed`
  at their resume and stops there, and the room service refuses a fresh
  join of theirs to that room all the same (`4403 removed`).
- **Ends.** Each end stops every track and closes every peer connection.
  `stats` is sent every 30 s from `getStats` (PR-6). The terminal's
  transport (`utils/transport/webrtc.ts`) and the room client share no
  object, no socket and no timer.

### `desktop`: the microphone, then the screen and the camera, in the desktop app; the server inside WSL reachable (PR-7, PR-9, PR-10, PR-14)

- **The permission handler** (`desktop/src/window.ts`): `permissionAllowed`
  takes the request's details. `media` is allowed only when the
  requesting origin is the app's own server origin, its `mediaTypes` are
  among those turned on (`audio` from PR-7, `video` for a camera from
  PR-10), and the page reported a press of that kind within the last 10 s
  through the bridge (`conductorDesktop.mediaPress(kind)`, a new
  `ipcRenderer.invoke` in `preload.ts`, recorded per web contents); any
  other origin, any request without a press, any other permission is
  refused as today. `setPermissionCheckHandler` answers `media` the same
  way for the server origin only, so device labels show after a grant.
  The web side calls the bridge when it exists (`utils/desktop.ts`), and
  nothing when it does not (a browser).
- **macOS**: `NSMicrophoneUsageDescription` (PR-7) and
  `NSCameraUsageDescription` (PR-10) through `electron-builder.yml`'s
  `mac.extendInfo`; the entitlements `com.apple.security.device.audio-input`
  (PR-7) and `com.apple.security.device.camera` (PR-10) in
  `build/entitlements.mac.plist`. The bridge's `mediaAccess(kind)` reads
  `systemPreferences.getMediaAccessStatus` so the page shows the System
  Settings words when the system, not the page, refused. Screen
  recording has no usage key in macOS; the system's own prompt names the
  app, and `mediaAccess('screen')` tells the page when it was refused
  (PR-9).
- **Screen sharing** (PR-9): `session.setDisplayMediaRequestHandler` with
  the app's own picker window (`desktop/src/picker.ts`, `static/picker.html`)
  of screens and windows from `desktopCapturer.getSources`, with
  thumbnails; nothing is shared without a choice, and only for the server
  origin after a Share screen press.
- **Windows** (PR-7): `build/installer.nsh` adds an inbound program rule,
  "Conductor huddles", for the app's own executable (UDP), and removes it
  at uninstall, **only when it runs with administrator rights**, as it
  does the ICE port's rule. The app installs per user
  (`perMachine: false`), and a per-user install or update adds no rule
  and needs none: the window's WebRTC sockets send first, and the
  firewall lets the replies in. Nothing about it is ever asked of the
  person. The by-hand check of a per-user install confirms that a call
  raises no firewall prompt; a prompt seen there is a defect of this
  stream, fixed in a fix PR of it.
- **The server inside WSL reachable from outside Windows** (PR-14, issue
  #140). Four things, all made by the app, none asked of the person:
  - *A TLS listener.* A clean install has none to forward: the app
    starts its server on loopback HTTP alone (`desktop/src/env.ts`),
    and `conductor serve` opens a TLS listener only when ACME or
    certificate files are configured (`internal/config`'s
    `TLSEnabled`). With the app's reach setting at `auto`, its default,
    and the server inside WSL, the WSL launcher (`launcher-wsl.ts`)
    starts the server with three variables the server already reads:
    `CONDUCTOR_TLS_ACME=1` with no domains (a certificate for the public
    address, `tls-alpn-01` on 443, the six-day profile Let's Encrypt
    gives an IP address: `internal/certs` as a Linux server uses it
    today), `CONDUCTOR_TLS_LISTEN=:0` (a free port in the distribution,
    which the `--print-listen` handshake's `tlsListen` already reports
    to the app), and `CONDUCTOR_REACH=manual` (the public address by
    STUN, the port forwarded by someone other than the server: here the
    app). The server's code does not change. The settings `manual` and
    `off` start the server as today, with no TLS listener; the macOS and
    Linux desktop apps are unchanged.
  - *Its firewall rule.* A phone's connection is inbound and
    unsolicited, which no outbound socket's reply lets in, so the
    forward's port needs an inbound TCP rule, provisioned the way the
    ICE port's UDP rule is: by the installer when it runs elevated, else
    by the app's own elevated `netsh` with fixed arguments (`firewall.ts`;
    the system's own consent, once; nothing typed, no setting to find).
  - *The forward.* A TCP forwarder beside the UDP one, from a Windows
    port to the distribution's address and the handshake's `tlsListen`
    port, followed as that address changes; it listens on Windows' LAN
    interfaces only once the rule exists, so Windows' own "allow access"
    dialog never appears, and closes a connection that comes before the
    handshake.
  - *The router's mapping,* made from Windows (UPnP IGD, PCP or
    NAT-PMP, what `internal/reach` does on Linux, made here by the app
    because the distribution sits behind Hyper-V's NAT): the public 443
    to the forward's Windows port, a lease of two hours where the router
    allows it, renewed at half, deleted at the app's quit.
- **In what order, and what happens when a step fails.** The app makes
  the rule, the forward's Windows port and the mapping first, then
  starts the server and points the forward at the handshake's port, so
  the certificate authority's first challenge finds the path open. If the
  rule is refused or the router takes no mapping, the app starts the
  server as today (`auto`, no TLS listener), the Settings page says in
  one line why other devices cannot reach this server, and the app tries
  again at its next start; the app's own window works in every case,
  over loopback. The certificate manager serves nothing before its first
  issuance, and the server offers its `https://` address (links,
  `GET /api/reach`) only once the certificate is issued: the authority
  issues it only after reaching the listener through the mapping and
  the forward, so an address offered is one that was reached. A refused
  order is tried again on the manager's backoff (one hour, doubling to a
  day), the reach status giving its words.
- **The server is told nothing after its start.** No channel from the
  app to the server carries the mapping (round 2's lines on the server's
  standard input are gone): the server's reach learns the public
  address by STUN, and the port is the one the app mapped. So a mapping
  the router drops later is not seen by the server. This is the narrower
  promise: its `https://` address may stay offered until the
  certificate's next renewal fails (six days at most) while a phone
  cannot load it, until the app has mapped the port again (at its
  lease's half, or at its next start). On Linux the server's own mapper
  withdraws the address at once, as on `main`. What a person sees in that
  time: on the phone, the browser's own words that the address cannot
  be reached, never a Conductor page that half works; in the desktop
  app, from the moment its renewal of the mapping is refused, the same
  one line on the Settings page as for a mapping refused at start, until
  the router takes it again; the app's own window, over loopback, works
  throughout. No huddle, session, run or decision on this server depends
  on the address (`router-map.test.ts`: a renewal refused later
  reported as at start).
- **Tests** (PR-14): desktop, `launcher-wsl.test.ts`,
  `tcp-forwarder.test.ts`, `firewall.test.ts`, `router-map.test.ts`;
  Go, `TestServeTLSFromAnEmptyDataDir` (Tests per surface). The words
  in `docs/architecture.md`.

### Windows with the server inside WSL

A person configures no networking, ever (`AGENTS.md`). Every media path
of a huddle starts in the person's browser, which on Windows is the
desktop app's window (or a browser) running on Windows, not inside WSL;
the one path that comes in from outside Windows is a phone's or another
machine's request for the server's page:

| Path | From, to | Crosses the Hyper-V NAT? | What the desktop app does |
|---|---|---|---|
| The pages, `GET /api/rooms`, `POST /api/rooms/join`, the events, from the window | the window to the server in WSL | yes, as every page request today, over the connection the app already makes | nothing new |
| The same, from a phone or another machine (the acceptance's second person) | the phone, on the LAN or a mobile network, to the server's `https://` address | yes, inbound to Windows and on into WSL; from a mobile network, inbound through the router too | PR-14: the TLS listener turned on and forwarded into WSL, its firewall rule, and the router's mapping, all made by the app |
| The room service's WebSocket | the window, or the phone's browser, to the room service | no: outbound | nothing |
| Direct media (UDP) | the window to the other person's browser | no: Windows' own interfaces gather the candidates | nothing to forward; the installer's program rule when it ran elevated (PR-7) |
| A relay | the window, or the phone, to the relay the room service names | no: outbound | nothing |
| The provider's own traffic | the server in WSL to its service | outbound through the NAT, as every outbound connection today | nothing |
| The e2e fake (tests and the by-hand check) | the window to `127.0.0.1` in WSL | yes, TCP to loopback, which WSL forwards to the distribution on its own | nothing |

So the media need no forward the app does not make today; the phone's
request for the page does, and PR-14 makes it before the acceptance. A
phone on a plain `http://` LAN address is not a secure context, so its
huddle control is disabled there ("Huddles need a secure address"): the
phone reaches the server's `https://` address. A browser run inside the
distribution itself (WSLg) gathers its candidates behind the NAT; its
direct path may fail and it then uses the relay, outbound, with nothing
to configure. Should the by-hand check find another path that crosses
the NAT, the forward is built in `desktop/` as a fix PR of this stream,
never written as a step for a person.

### Docs

`docs/protocol.md` (the Rooms section, PR-2; the routes, events and link
kind, PR-4; the reviewer's rows, PR-11); `docs/repository-map.md`'s
rows for `internal/roomfake` and `ext/room.go` (PR-3);
`docs/architecture.md`'s lines on the forwarded TLS listener and the
mapping made from Windows (PR-14); the Extensions section of `README.md`
naming what a media provider adds (PR-11); `docs/features.md` the item's
entry (PR-11, extended by PR-9 and PR-10).

### The hosted plan

The provider's plan (Program item CL1's other plan file, kept with the
provider and not in this repository) holds this plan's specification,
and is final: two people's calls, direct with the relay behind them;
group calls a later item with its own plan; a reviewer admitted only
with their own pass; relay credentials only for the server's signed-in
principals, never a share link's viewer; on a phone, a huddle in the
browser. This plan follows it, and nothing here waits on it. The Media
seam and the room protocol are this repository's (Who owns what): a
provider implements them, and its room service passes their vectors, at
the tag that defines them. This plan asks of a provider and its room
service nothing the specification does not give: review round 3 took
out the three places where it did (`ping` and `pong`, a recipient on
`RoomInvite`, and an echo confirming one sharer) and the final check
three more (`Rooms` answering in the order asked, a retry of
`RecordHuddle` carrying the same record, and a 200-byte bound on an
`error`'s message), and each promise that rested on one is now narrower
(Changed in review round 3, Changed in the final check). Where it
states more than the specification, the more is the open-source side's
alone and asks nothing of a provider: `offered` on a server of several
principals, the reply bounds of this server's own routes, the client's
rules (I13 to I18), the host's explicit ICE list first, huddle links
without eviction behind a raised schema version, a candidate's
existence checked before its room is offered, and the TLS listener the
desktop app turns on.

## Events, orderings and invariants

**The call's states** (`RoomClient`, the app's own; the room service has
its own, which the client follows):

| State | Entered on | Next |
|---|---|---|
| `idle` | start, or after `ended` is shown and dismissed | `checking` |
| `checking` | Join pressed: the device check, a track held | `joining` (Join), `idle` (Cancel: the track stopped) |
| `joining` | `POST /api/rooms/join` answered, the socket open, `join` sent | `in` (`joined`), `ended` (a close code, a `409`), `reconnecting` (no `joined` within 10 s) |
| `in` | `joined` | `reconnecting`, `ended` |
| `reconnecting` | the socket dropped, `1001`, or a socket that does not drain; every sender's track set to `null`, every received track muted and a screen capture stopped on entry | `in` (`joined` after `resume`, or after one fresh `join` when the resume was refused `4401`; every old peer connection closed, new ones to `joined.people`), `ended` (60 s after the socket closed; a terminal refusal: `4403`, `4409`, `4410`, `ended`; a second `4401`) |
| `ended` | `ended`, a close code, 60 s, Leave, `pagehide`, the credential changed or forgotten | `idle`; every track stopped and every peer connection closed on entry |

**What is done before what.**

- No device is opened before a press: `checking` is entered only from a
  click or a key, and `getUserMedia` is called only there
  (`huddle-audio.spec.ts` counts the calls from an init script before the
  first press).
- The app sends no frame before `join` (or `resume`), and treats nothing
  before `joined` as presence.
- On `left` and on every end, the senders to that person are stopped and
  the peer connection closed before the change is drawn (I8); on every
  end the tracks are stopped before the words are shown, so no one reads
  "You left" with a microphone still open.
- The server sends `room` after the provider's `RoomChanged` returns, and
  the app reads `GET /api/rooms` after the event; a page that misses the
  event (the stream down, or dropped for falling behind) reads again on
  the `snapshot`; a full coalescer set sends `room {}` rather than drop a
  ref.
- `RecordHuddle`: the operation's earlier answer looked for first, by
  its arguments (the room session, never the ticket); a done answer is nil, a
  kept refusal is that refusal; only then the ticket resolved, then
  Dispatch's `link.huddle` committed with its receipt, then nil to the
  provider; a provider that calls again after a crash, after the run
  moved to another ticket, after a person in it was renamed, or after the
  receipt was pruned, gets the first answer and nothing is written twice (`TestRecordHuddleOnce`).
- `room_invite` events are pending at most 32, one per room, the oldest
  dropped; the provider's call never waits on a client.
- On a reconnect: senders silenced and received tracks muted on entering
  `reconnecting`; on its `joined`, every old peer connection closed
  before any new one is made (I13).
- On an offer from another place of a person (another DTLS
  fingerprint): the connection with their old place closed before the
  offer is answered (I15).
- On a credential changed or forgotten: the call ended, then the
  invitations and presence cleared; a result of a step begun under the
  old credential discarded (and a track it brings stopped) whenever it
  arrives (I16).
- A screen: `state` before the offer that carries it; the grant (the
  answer applied) before "You are sharing"; on a refusal, the capture
  stopped before the words are shown (I17).

**Invariants**, each a named test:

- I1. A plain build draws no huddle control and makes no request to
  `/api/rooms` (`huddle-offer.spec.ts`; `TestRoomsNotOfferedByTheFreeDefault`).
- I2. Joining grants nothing: the reply's token is accepted by no route of
  the server, and the principal's grants are the same after a join
  (`TestJoinGrantsNothing`).
- I3. A room is offered, and joined, only for work the principal may
  read and that exists here, a review's candidate by its run, task,
  attempt and result commit in the ledger; a provider's answer that is
  not one state per ref asked is refused (`TestRoomsFilteredToReadable`,
  `TestRoomsCandidateMustExist`, `TestJoinCandidateMustExist`,
  `TestGuardRoomsOneStatePerRef`).
- I4. A media fault touches no session, run or decision: with the provider
  hanging, failing or panicking in `Room` and `Rooms`, a session's input
  echoes, a stub run's task proceeds and a question can be answered
  (`TestRoomFaultsTouchNoSession`); with the room service held, dropped
  and stopped, the page's terminal still echoes (`huddle-terminal.spec.ts`).
- I5. Every device track the app made is stopped at every end
  (`roomClient.test.ts`, `huddle-audio.spec.ts`, `huddle-states.spec.ts`).
- I6. A `huddle` link is written only through `RecordHuddle`, once per
  room session, never takes an ordinary link's place, is never removed
  by a build that knows the kind, is never written by one that does not
  (the schema version), and a ticket's read shows the same newest 200 on
  every replica that holds the same links
  (`TestHuddleLinkRefusedOnLocalWrite`, `TestRecordHuddleOnce`,
  `TestHuddleLinksApartFromLinks`, `TestHuddleLinksMergeConverge`,
  `TestOlderBuildOpensHuddleStoreReadOnly`).
- I7. Every frame, record, route body and reply is within its bound on
  its encoded bytes (`TestRoomVectors`, `TestRoomFrameBudgets`,
  `roomProtocol.test.ts`, `TestRoomsReplyBounded`, `TestJoinBodyBounded`).
- I8. A person who leaves or is removed is cut off by the others: on
  `left` and on its own `ended` the client closes that person's peer
  connection before drawing the change and drops signals from anyone not
  in the room (`roomClient.test.ts`; `huddle-states.spec.ts`: the removed
  context, made to ignore its own end, sees its inbound audio bytes stop
  growing, read from `getStats` twice an interval apart, while the other
  context's client is honest).
- I9. Show them only navigates: a `point` is checked against its kind's
  pattern, its route is built from the room, the kind and the id, and Go
  calls the router alone (`huddlePoint.test.ts`: each kind in each room
  kind, a refused kind, a foreign home, a path with `..`; no request but
  the page's own reads after Go).
- I10. Two people on one server are two, one person's two devices are
  one, and an invitation reaches admin principals' pages only, worded by
  what the server knows (Who is who's tests;
  `TestInvitationNotSentToOtherPrincipals`: a reviewer's and a link
  viewer's streams get none; `huddle.test.ts`: the two wordings).
- I11. A closed room service ends the call: with the socket closed and no
  `joined` since, the call ends 60 s after the close; a quiet open socket
  ends nothing (`roomClient.test.ts` on fake timers: a close, then
  resumes unanswered, ends at 60 s; ten quiet minutes on an open socket
  end nothing, and Leave then ends the call at once with no frame
  answered; `huddle-states.spec.ts` through the fake's `stop`).
- I12. No release build contains a fake (`TestReleaseLeavesOutTheFakes`).
- I13. No peer connection outlives a gap in the room service's word:
  nothing is sent or played while the client is `reconnecting`, every
  connection is closed at the `joined` that ends it and made again only
  to the people that `joined` lists `in`, and a person's own
  `reconnecting` closes the others' connections with them
  (`roomClient.test.ts`: a removal missed while disconnected, the person
  absent from `joined.people`, no connection made to them; a second
  device's replacement during the gap, the new connection signalled to
  the current place and the old one closed; `huddle-states.spec.ts`:
  after the fake's `drop`, a new `RTCPeerConnection` and the old one
  `closed`, counted by the init script).
- I14. Only a resume refused `4401` leads the client to a fresh join, and
  once; a removal, an access end, `elsewhere` and every `ended` are
  terminal (`roomClient.test.ts`, each close code and reason;
  `huddle-states.spec.ts`: removed while disconnected, through the
  fake's `drop` then `remove`, shows "Ana removed you" and makes no
  `POST /api/rooms/join` after the drop, counted by the page's request
  log).
- I15. One place per person: an offer from a person's other place (a
  DTLS fingerprint other than the one the connection took) closes the
  connection with the old place before it is answered, so a displaced
  device that ignores its own end is cut off while the other participant
  stays connected throughout (`roomClient.test.ts`: Ben's second device
  offers while Ana's socket never dropped, the old connection closed
  before the answer; a renegotiation with the same fingerprint keeps the
  connection; a collision of two new connections settled by politeness;
  `huddle-states.spec.ts`'s "elsewhere while the other stays": Ben's
  first context made to ignore its end, its inbound audio bytes stop
  growing, read from `getStats` twice an interval apart, while Ana's
  page never reconnects).
- I16. A call belongs to the credential it started under: a token
  forgotten or replaced ends it, and nothing begun under the old one
  opens a call, a socket or a device afterwards (`useHuddle.test.ts`:
  the token forgotten and replaced during the join request, during
  `getUserMedia` and during a call; a late join reply and a late track
  discarded, the track stopped; `huddle-states.spec.ts`'s "signed out
  mid-call": every track `ended`, no socket left open).
- I17. Never two screens: a screen is sent only after the other side's
  answer takes it, a refusal or a collision stops the capture, and a
  screen is drawn only from the person whose `person` declares its
  `mid` (`roomClient.test.ts`: `media_kind` while the offer waits, the
  other's `sharing` before the grant, no answer in 10 s, the polite
  side's collision, a reconnect while sharing; `huddle-screen.spec.ts`).
- I18. What waits is bounded: pending candidates, waiting signals, the
  outgoing queue and the socket's buffer each have a bound and an
  overflow rule (`roomClient.test.ts`: 1,000 valid candidates before any
  offer, 64 kept; 100 offers while one is applied, the connection closed
  at the 33rd and made anew; a socket whose `bufferedAmount` never
  falls, closed and resumed; 1,000 frames to send, at most 20 a second
  leave; `TestFakeBoundsWhatWaits`).

**Stores.** The server stores nothing of a room but the `huddle` link.
Dispatch's schema version goes up by one in PR-4, with a migration that
changes no table, so that a build that does not know the kind opens the
store read-only and writes nothing to it (`internal/dispatch`, The
changes, PR-4). The browser keeps the two device ids. The fake keeps
everything in memory.

**Protocol.** The room protocol in `ext/room.go`, its aliases in
`internal/proto/room.go`, its TypeScript in `protocol.ts` and its words
in `docs/protocol.md`'s Rooms (PR-2); the routes and events in
`internal/api`, `protocol.ts` and `docs/protocol.md`'s HTTP API (PR-4).
The records live in `ext` because a provider's room service in another
module must import them and cannot import `internal/proto`; the aliases
keep the repository's rule that a protocol is in its three places (Owner
decisions, 1).

## Out of scope

- The room service, its relay, its admission, its feed of presence and
  any group forwarding: the provider's.
- **Small-group media** (three people or more): a later item with its own
  plan, which decides the group topology, adds the messages a group needs
  to the protocol with their own vectors, and cuts its own rows here.
  This version has no forwarded mode, and a room holds two people.
- Measuring relay needs, bandwidth costs and a group forwarder before any
  unlimited-media promise (CL1's requirement): the provider's. This
  repository's part is `stats`, which gives the room service each
  connection's path, round trip, loss and bitrate by kind.
- The phone app's huddle (its microphone and camera, an invitation's
  alert on a phone with no Conductor page open): a later row with its own
  plan. On a phone a huddle runs in the browser.
- A page that shows a run's committed file at a commit, for `point`'s
  `file` kind: a later row with its own authorized read.
- Recording, transcription, summaries, Brakeman joining a call: later and
  opt-in, with their own participation, retention and access rules (the
  Program, section 7). Nothing of a call is kept, and a transcript or a
  summary would never satisfy a gate.
- Text chat in a room: the sessions' chat exists; a room carries
  references only (`point`).
- A huddle without a piece of work, or any link that admits someone the
  server cannot vouch for: general conferencing is out.
- Huddles on the shared-session and decision-link pages a share link
  opens: a share link's viewer has no grant the room routes take.
- Audio processing settings beyond the device choice and Join muted.

## Tests per surface

**Go** (`make lint`, `go test -race -count=1 -timeout 30m ./...`,
`make lint-static`; `internal/opencore`'s rules hold, no new module, so no
notices change):

- PR-1, `ext`: `TestRoomJoinValidAt` (each field at its bound and one
  past; a ticket character outside `token68`; `ws://` to a loopback host
  only; `ExpiresAt` past and too far), `TestRoomRequestValid` (`Renew`
  among the fields), `TestRoomsAnswerValid`, `TestRoomStateValid`
  (`Scope`, `InviteURL` `https://` only and at 512 bytes and one past,
  `Via`), `TestRoomInviteValid`, `TestHuddleRecordValid`.
  `ext/exttest`: `TestMediaCheckCatchesInvalidJoin`,
  `TestMediaCheckRoomLister` (a lister that answers late, invalid, or
  not one state per ref is caught; one answering in another order
  passes), `TestFakeMediaRooms`,
  `TestFakeHostRecordsRoomDoors`. `ext/local`: `TestLocalMediaListsNoRooms`.
  `internal/api`: `TestGuardMediaJoinValid` (an invalid join is ok
  false), `TestGuardRoomsLateFailingPanicking`,
  `TestGuardRoomsOneStatePerRef`, `TestHostRoomDoorsStubbed`,
  `TestICERequestNamesThePrincipal` (each of the three sites: the
  operator's, a provider principal's subject, the host's, the paste's),
  `TestLinkViewerICERequestHasNoPrincipal` (a share link's viewer of
  either role), `TestRoomRequestByOperator`, `TestRoomRequestBySubject`.
- PR-15, `internal/api` and `internal/hostagent`: `TestICEEndedAtEverySite`,
  `TestICEEndedQueueBounded`, `TestICEConnNeverReused`,
  `TestHostViewerGetsItsOwnServers` (two viewers, the second joining long
  after registration), `TestHostPeerToLinkViewerGetsNoProviderTURN`,
  `TestOlderHostGetsNoPerViewerServers`, `TestHostViewerICEAskHoldsNoSession`,
  `TestHostICEPrecedence` (a
  host with `--stun` uses it for every peer and is asked no per-viewer
  servers; a host that reads them uses the viewer's; one that does not,
  the registration's); `internal/proto`: the host messages' new fields
  at their bounds.
- PR-2, `ext`: `TestRoomVectors` (every vector decodes or is refused with
  its error), `TestRoomFrameBudgets` (each message at its worst case
  through Go's encoder inside its bound; a `join` and a `renew` with a
  4,096-byte ticket and a 2,048-byte pass inside 8 KiB),
  `TestRoomFramesRefuseUnknownFields`, `TestRoomCloseCodesNamed`,
  `TestPointKindPatterns` (the seven, a `run` at 73 bytes),
  `TestStateMidsBounded`; `internal/proto`: `TestRoomVectorsThroughProto`;
  `internal/api`: `TestPointableMatchesTheValidators`.
- PR-3, `internal/roomfake`: `TestTwoPeopleTalkThroughTheFake` (two
  `pion/webrtc` peers in the test process join one room with the fake's
  tickets, exchange an Opus track through its signaling, and each reads
  RTP from the other: two people's audio end to end with no browser),
  `TestFakeSpeaksTheVectors`, `TestFakeProviderPassesTheSuite`
  (`exttest.Media` and `exttest.Starter` on the provider),
  `TestFakeIdentityGivesEachPersonASubject`, `TestFakeTicketsOneUse`,
  `TestFakeResumeWithinAMinute` (its clock seam), `TestFakeResumeTokenRotates`,
  `TestFakeRenewNamesTheSession`, `TestFakeSameSubjectIsOnePerson`,
  `TestFakeTwoSubjectsTwoPeople`, `TestFakeElsewhereInAnotherRoom`,
  `TestFakeRemovedCannotResume` (and cannot join that room afresh),
  `TestFakeSDPCheckByDirection` (an answer whose video section is
  `recvonly`, from a person who shares nothing, taken; an `inactive` and
  a port-0 section taken; a `sendonly` or `sendrecv` section with an
  undeclared `mid` refused; a screen while the other shares refused),
  `TestFakeBoundsWhatWaits` (the rate's `error`, the write queue and
  `hold` at their bounds and one past, the socket closed),
  `TestFakeStopClosesEverySocket`,
  `TestFakeControlSeams`, `TestFakeRoomClosedRecordsOnce`,
  `TestFakeKeepsNoFrames` (its logs searched for the SDPs and tickets);
  `internal/opencore`: `TestReleaseLeavesOutTheFakes`.
- PR-4, `internal/api`: `TestRoomsOfferedOnlyWhenAvailable`,
  `TestOfferedOnlyWhenSomeoneElseCouldJoin` (scope `w`, someone else in,
  an identity provider registered; the workbench token alone and an empty
  `d` room not offered), `TestRoomsNotOfferedByTheFreeDefault`,
  `TestRoomsRefsBounded`, `TestRoomRefParse` (a review's id with `@` and
  `:`, with and without a home), `TestRoomsFilteredToReadable`,
  `TestRoomsCandidateMustExist` and `TestJoinCandidateMustExist` (a known
  run with an unknown task, an attempt that does not exist, a result
  commit that is not the candidate's, and the ledger unavailable: each
  dropped from the rooms reply and `409 no_room` at the join, the
  provider never asked; a candidate whose checks failed still offered),
  `TestRoomsReplyBounded`, `TestJoinReplyBounded` (a URL of 512 `&`, a
  4,096-byte token and a 64-byte room id, inside 8 KiB),
  `TestJoinNeedsReadAccess`, `TestJoinGuarded`
  (late, failing, panicking, invalid), `TestJoinBodyBounded` (at 512 bytes
  and one past; a `ticket` field refused), `TestJoinRateLimited`,
  `TestJoinTicketFromTheServersRecords` (a body naming a ticket refused
  as an unknown field), `TestRoomsRefCarriesTheTicket`,
  `TestJoinRenewPassed`,
  `TestJoinGrantsNothing`, `TestRoomChangedCoalesced` (driven through the
  coalescer's clock seam), `TestRoomCoalescerFullSendsAll`,
  `TestRoomEventDropsASlowClient`, `TestRoomEventsAdminOnly`,
  `TestInvitationReachesTheEventsStream`,
  `TestInvitationNotSentToOtherPrincipals` (every admin principal's
  stream gets it; a reviewer's and a link viewer's get none),
  `TestRoomInvitedBounded`, `TestInvitationsOnePerRoom` (a second
  invitation of one room replaces the first; 33 rooms keep the newest
  32), `TestRecordHuddleOnce` (a retry; a retry after the run moved
  to another ticket, replayed by its arguments; a retry after the
  receipt was pruned, answered from the link's fingerprint; a retry of
  an operation refused, which answers the refusal and never nil; a
  retry carrying other names or another end under the same room
  session, which gets the first answer and writes nothing),
  `TestRecordHuddleNoTicketWritesNothing`,
  `TestRoomFaultsTouchNoSession`; `internal/dispatch`:
  `TestHuddleLinkRefusedOnLocalWrite`, `TestAddHuddleReplays`,
  `TestHuddleLinksApartFromLinks` (a ticket at 50 links records its
  huddle and still refuses the 51st ordinary link; the 201st huddle
  refused, nothing removed), `TestHuddleLinksMergeConverge` (two
  replicas of 150 huddles each, their batches delivered in both orders
  and repeated: the same stored set, and the same newest 200 read, with
  the count of the rest, on each), `TestHuddleNoteBounded`,
  `TestHuddleLinkKeptOnMerge`, `TestOlderBuildOpensHuddleStoreReadOnly`
  (a store at PR-4's schema version opened by a store at the version
  before it, through the open's version seam: read-only, its
  `link.remove` of a huddle link and its `link.add` both refused, the
  links unchanged), `TestSchemaMigrationChangesNoTable` (a store of the
  version before migrated: every table's definition and rows as they
  were).
- PR-8, `internal/api`: `TestRoomsDecisionNeedsItsRecord`,
  `TestJoinDecisionTicketFromRecord`.
- PR-11, `internal/roomfake`: `TestFakeReviewerNeedsItsPass` (a
  reviewer's join without a pass, with another's, or expired: `4401`;
  with its own: admitted; a renewal whose pass ended refused);
  `internal/api`: `TestRoomsReviewerOwnDecisionOnly`,
  `TestJoinReviewerRefusedElsewhere`, `TestReviewerStreamRoomEvent`,
  `TestReviewerStreamNoRoomOutsideItsDecision`,
  `TestReviewerAdmissionReadAtEachRequest`.
- PR-14, `internal/cli` (a test of the server's code as it is, under the
  settings the app now passes): `TestServeTLSFromAnEmptyDataDir` (the
  app's three variables and `--print-listen`, an empty data directory,
  a STUN answer and `internal/certs`' fake issuer: the handshake carries
  `tlsListen`; the listener serves nothing before the first issuance;
  the certificate is ordered for the STUN address and, once issued,
  `GET /api/reach` gives the `https://` base; an order refused leaves
  that base unoffered, the plain listener answering and the reach
  status giving the error's words).

**Vitest** (`npm --prefix web run typecheck`, `npm --prefix web test`):
`roomProtocol.test.ts` (PR-2: the vectors through the TypeScript
encoder and decoder), `protocol.test.ts` (PR-4: the routes' and events'
types), a `DispatchLinks` case (PR-4: a ticket with ordinary links and
huddle links lists only the ordinary ones); `roomClient.test.ts` (PR-13:
each state's transitions, `renew` every `renewEvery` with the session,
`resume` after a drop with the rotated token, a resume refused `4401`
rejoining once and a second `4401` ending, each terminal code and reason
(`4403 removed`, `not_allowed`, `not_entitled`, `4409 elsewhere`,
`room_full`, `4410`, `ended`) ending with no request to the server
(I14), senders silenced and received tracks muted while reconnecting,
every connection closed at the `joined` after a reconnect and made again
only to `joined.people` (a removal missed while disconnected, a second
device's replacement during the gap; I13), another's `reconnecting`
closing the connection with them, `left` closing that person's
connection before the change is drawn, a `signal` from someone not in the
room dropped, a new `ice` restarting ICE on a relayed connection, ICE
failing twice with the room service quiet and the tries then made on
their own by the impolite side, an offer unanswered for 10 s, a closed
socket ending at 60 s and ten quiet minutes on an open one ending
nothing (I11), no `joined` within 10 s, I15's, I17's and I18's cases,
every end stopping every track, all on fake timers and fake sockets);
`useHuddle.test.ts` (PR-5: I16's cases); `deviceCheck.test.ts` (PR-5: no device before the
press, Cancel stopping the track, the blocked and missing words, the
speaker list only with `setSinkId`), `huddle.test.ts` (PR-5: the state
table's words, `secureEnough`, `offered` from capabilities and the
reply; PR-6: the invitation's two wordings by whether an identity
provider is listed, and no toast for the call's own room); `huddlePoint.test.ts` (PR-6: `pointRoute` for `ticket`, `run`
and `task` in each room kind, `decision`, `candidate`, `finding` and
`file` answering "can't open this yet" with no Go, `pointable` against
`points.json`, the refusals, Go calling only the router; PR-8:
`decision` and `candidate` routed, `finding` and `file` still never), the phone sheet's
layout function (PR-6); the screen and camera states (PR-9, PR-10).

**Playwright** (`make test-e2e`; the huddle specs drive a second server
of their own, `bin/conductor-roomfake`, started by `web/e2e/huddle.ts`
with its own root, HOME and data directory and stopped after the file;
`huddle.ts` waits for `room.json` and gives each browser context one
person's token from it, Ana's and Ben's, two principals of one server
with their own subjects, and the workbench token where a spec needs the
operator; Chromium's fake devices through
`--use-fake-device-for-media-stream`; the microphone granted per context
with `permissions`, never the fake-UI flag, so the blocked state is a
context without the grant):

- `huddle-offer.spec.ts` (PR-5): on the plain e2e server, after the
  ticket page's header is drawn and the capabilities read has answered,
  no huddle control, and no request to `/api/rooms` made.
- `huddle-audio.spec.ts` (PR-5): Ana and Ben; no `getUserMedia` call
  before the press (counted by an init script); both join; each page's
  inbound audio's `bytesReceived` and `totalAudioEnergy` grow (polled from
  `getStats`, the mechanism, until they do); mute proven at the sender
  (the track's `enabled` false) and by the other page's muted mark from
  `person`; Leave ends every track the page made (`readyState` `ended`,
  read from the init script's list).
- `huddle-states.spec.ts` (PR-6): blocked, missing (a context with no
  device), full (with its size), in another huddle (Ana in the ticket's
  room, Ana's second context joining the run's), removed (Ben made to
  ignore his `ended` by an init script that drops it; his inbound audio
  bytes stop growing, read twice an interval apart, while Ana's client
  is honest), access ended (the fake's `expire`), elsewhere (two contexts
  of the operator's workbench token in one room), elsewhere while the
  other stays (Ana in, Ben's first context made to ignore its end, Ben's
  second context joining: Ben's first context's inbound audio bytes stop
  growing while Ana's page never reconnects; I15), signed out mid-call
  (the token forgotten on Ana's page: every track `ended`, Ben sees
  Ana leave; I16), reconnecting after the
  fake's `drop` and back in with the mute kept, the old
  `RTCPeerConnection` closed and a new one made (counted by the init
  script), removed while disconnected (the fake's `drop`, then `remove`:
  "Ana removed you from the huddle." and no `POST /api/rooms/join` after
  the drop), "Could not reconnect" after `refuse-resume` and `stop`,
  unreachable after the fake's `stop` (the call ended 60 s after the
  close by the client's own clock) and at the fake's `ended {why:
  unavailable}`, ending soon at the fake's `ending`;
  each driven by a control seam, never a clock of the test's.
- `huddle-invite.spec.ts` (PR-6): Invite lists the fake's people, a page
  at a time; Ben's toast from `room_invite` in the wording of a server
  with an identity provider (the e2e binary registers one), none on
  Ana's page, whose call is in that room, and none in a share link's
  viewer; Join from the toast opens the work and its
  device check; Show them sends and Ben's Go opens the ticket, and a
  `finding` pointed in a ticket's room offers no Go.
- `huddle-phone.spec.ts` (PR-6): at 390 by 844, the sheet collapsed and
  expanded, Mute and Leave reachable, the page scrolling above it.
- `huddle-terminal.spec.ts` (PR-6): a stub session's terminal on the run
  page with a call in progress; with the fake's `hold`, then `drop`, then
  `stop`, the terminal echoes a typed line each time (waited for by its
  text), and the run's task still answers its commands.
- `huddle-history.spec.ts` (PR-6): after both leave and the fake's grace,
  the ticket's history shows the huddle's line (waited for by the
  receipt's arrival in the page, not by time), and the links list does
  not.
- `huddle-decision.spec.ts` (PR-8): the control on a decision's card on
  `/decisions` and in the ticket page's Decisions section, on the Review
  view keyed by the candidate; the dock never covering the answer
  buttons (their boxes checked against the dock's).
- `huddle-reviewer.spec.ts` (PR-11): a reviewer admitted to one decision
  joins its room and no other; its admission brought to its last five
  minutes through the fake's seam shows Access ending, and renewed in a
  second tab keeps the call.
- `huddle-screen.spec.ts` (PR-9: Ana shares while Ben only receives,
  Ben's `recvonly` answer taken and Ana's screen drawn on Ben's page by
  its `mid`; two presses at once, held by the fake and released
  together, never give two sharers, and each refused side's capture is
  `ended` with its words shown; a sharer whose socket the fake drops
  stops sharing and is not sharing after the reconnect),
  `huddle-camera.spec.ts` (PR-10: one camera one way, the receiver's
  answer `recvonly` and taken) with the fake devices.

Every huddle spec follows `AGENTS.md`, Tests that do not flake: it waits
for `room.json` before reading it; it looks for its rooms by the work's
id, never by a count; it raises its timeout by each step's own cap as it
takes it (`testInfo.setTimeout`); after a `goto` it waits for the page's
header before pressing a key; it reads a state again after a redraw it
causes. One Playwright run per worktree.

**Desktop** (`make desktop-test`): `window.test.ts` (PR-7: `media` with
`audio` allowed for the server origin after a press only; another origin,
no press, a stale press, `video` refused; PR-9 the display handler's
choice, PR-10 `video` after a camera press), `packaging.test.ts` (PR-7: the
usage string, the entitlement, the installer's rule added and removed in
its elevated branch only; PR-10 the camera's), `picker.test.ts` (PR-9),
`tcp-forwarder.test.ts` (PR-14: the forward followed as the
distribution's address changes, pointed at the handshake's `tlsListen`
port, connections before the handshake closed, no LAN listen without
the rule), `firewall.test.ts` (PR-14: the TCP rule's fixed arguments and
name), `router-map.test.ts` (PR-14: the lease asked and renewed at half,
deleted at quit, a refusal reported), `launcher-wsl.test.ts` (PR-14:
from the default settings and an empty data directory, the rule, the
forward and the mapping made before the server starts and the three TLS
and reach variables passed; with any of the three missing, the server
started as today with none of them and the reason given to Settings;
the settings `manual` and `off` unchanged). `make desktop-e2e` unchanged.

**By hand, what tests cannot cover**, each made by the owner, who has the
machines and a microphone, on one machine with two browsers against the
e2e binary (PR-6 and PR-7 merged) and then, for the item, with a
provider (Acceptance): the audio to the ear for ten minutes; the
terminal's feel during a call, and its echo time watched with and
without a call (the latency row this plan keeps by hand); the macOS
microphone prompt and System Settings words in the desktop app; on
Windows with the server inside WSL, the desktop app and Edge on that
machine talking with nothing configured, on a per-user install, with no
firewall prompt (a prompt seen is fixed in this stream); a phone on a
mobile network opening the server's `https://` address through PR-14's
forward and mapping, nothing configured, on a clean per-user install and
on a clean elevated one, neither with a rule inherited; Safari and Firefox against
Chromium; the phone layout on a real phone, and the words when its
screen locks; the terminal's feel with a screen shared (PR-9); and,
with a provider, two machines on different networks, one forced to its
relay. An agent may run the Mac's and Windows' automated rows; the
by-hand rows need a person at the machine.

## Boxcar constraints

The eight of the Program's section 2.5:

1. The engine starts an attempt only through `crew.Launcher`: not
   touched; nothing here launches anything.
2. `session.Local` drives a `session.Process`: not touched.
3. An attempt's context is files in its worktree: not touched.
4. The agent talks to the server over the network: respected; no channel
   is added, and media never reaches the server.
5. Checks go through `internal/proc`: not touched.
6. Platform splits are platform tags, and the UI shows a capability:
   respected; the screens are drawn from what the registered provider
   answers (`offered`), never from a plan, and no tag is added.
7. Evidence rows' `where`: not touched; the huddle's line is a Dispatch
   link, not execution evidence.
8. `hostagent` is a launch site: not touched; its ICE site already asks
   the provider (C8), and a huddle never runs through a host.

## Open-core checklist

- [x] Article 1: no paid code and no plan or licence check; the seam, the
  protocol, the fake and the screens serve any registered provider, and
  the screens are drawn from its answers.
- [x] Article 2: a plain build is unchanged and fully useful; no huddle is
  drawn and no room route is asked (`huddle-offer.spec.ts`).
- [x] Article 3: a room exists only at the URL a registered provider
  answers; no flag, setting or tag here opens one, and the fake is in a
  test binary only.
- [x] Article 4: joining grants nothing and authorizes nothing; stop,
  inspect, revoke, export and required approvals are untouched with the
  provider hanging, failing or gone (`TestJoinGrantsNothing`,
  `TestRoomFaultsTouchNoSession`).

## The PRs

Every row is in this repository. Each PR is one issue and one branch cut from `main`, a draft PR at its
first commit, the checks for its surfaces before every push, its body
ticking the open-core checklist and naming the agent that made it. Each
leaves `main` working and green on its own: the seam's doors are stubs
until PR-4 builds them; the screens draw nothing until a provider
answers, and the fake is the only one here. Parallel means the two touch
no common Go package, no common directory under `web/` or `desktop/` and
no common file under `docs/`; two that do are ordered by a dependency, so
the table's order is the merge order wherever they share. Sizes are
changed lines, code and then tests (the owner, 2026-11-05: 300 to 800, at
most about 1,500 with tests).

The row numbers name the PRs; **the table's order is the merge order**
(PR-13 to PR-15 were added in review round 1 and sit where they merge;
review round 2 removed PR-12, small groups, and PR-16, the phone app's
devices: see Out of scope).
The first six rows deliver two people's audio end to end against the
fake: PR-3 with two pion peers in Go, PR-5 with two browsers. PR-8 is
the first row that needs another plan's rows, by issue number; PR-11
the first that needs Program item ID1's; the decision's huddle with its
reviewer (PR-8, PR-11) goes before screen sharing and cameras (PR-9,
PR-10), which touch `web/app` too and so are ordered after it (Owner
decisions, 14).

| PR | Title | Touches | Depends on | Parallel with | Size |
|---|---|---|---|---|---|
| PR-1 | ext, api: the room seam's records, presence, the ICE sites' principal and three Host doors, checked by the guard and the contract suite | `ext`, `ext/exttest`, `ext/local`, `internal/api` | none (C8 on `main`) | PR-7 | ~600 + 700 |
| PR-15 | ext, api, hostagent: one relay credential per connection, and its end | `ext`, `ext/exttest`, `internal/api`, `internal/hostagent`, `internal/proto`, `web/app/utils`, `docs/protocol.md` | PR-1 (`ext`, `internal/api`) | PR-7 | ~400 + 550 |
| PR-2 | ext, proto, web, docs: the room protocol conductor-room/1, its records, aliases, TypeScript, budgets and vectors | `ext`, `internal/proto`, `web/app/utils`, `docs/protocol.md` | PR-15 (`ext`, `internal/proto`, `web/app/utils`, `docs/protocol.md`) | PR-7 | ~650 + 650 |
| PR-3 | roomfake: the fake room service, the fake provider, the fake people and the e2e server binary; the release keeps them out | `internal/roomfake` (new), `internal/opencore`, `Makefile`, `docs/repository-map.md` | PR-2 (the records) | PR-4, PR-13, PR-7 | ~800 + 700 |
| PR-4 | api, dispatch, web: the room routes, the `room` and `room_invite` events, the Host doors built, the huddle's line on the ticket apart from its links | `internal/api`, `internal/dispatch`, `web/app/utils/protocol.ts` and its test, `web/app/components/DispatchLinks.vue`, `docs/protocol.md` | PR-2 (`web/app/utils`, `docs/protocol.md`) | PR-3, PR-7 | ~700 + 800 |
| PR-13 | web: the room client: the call's state machine, perfect negotiation, renewal, liveness, reconnect, ICE retries, I8, I13, I14 | `web/app/utils` | PR-4 (`web/app/utils`) | PR-3, PR-7 | ~600 + 750 |
| PR-5 | web: two people talk: the device check, the control on the ticket and run pages, the dock, mute, devices, presence, leave; the second e2e server | `web/app`, `web/e2e` | PR-3, PR-13 | PR-7 | ~600 + 600 |
| PR-6 | web: the huddle's states, invitations, Show them, the phone sheet, the line in the history, media faults | `web/app`, `web/e2e` | PR-5 | PR-7 | ~650 + 750 |
| PR-7 | desktop: the microphone in the desktop app: the permission handler after a press, macOS usage and entitlement, the Windows program rule when elevated | `desktop` | none | PR-1 to PR-6, PR-13, PR-15 | ~300 + 350 |
| PR-14 | desktop: the server inside WSL reachable from outside Windows (#140): its TLS listener turned on, forwarded, its rule and the router's mapping | `desktop`, `internal/cli` (one test), `docs/architecture.md` | PR-7 (`desktop`) | PR-8, PR-11 | ~550 + 650 |
| PR-8 | web, api: huddles on the decision page, the decision cards and the Review view | `web/app`, `web/e2e`, `internal/api` | PR-6; #637 and #640 (DR1), #647 and #652 (MB0), #631 (RV1) | PR-14 | ~450 + 500 |
| PR-11 | api, web: a reviewer's huddle on its decision; the features entry | `internal/api`, `web/app`, `web/e2e`, `docs/protocol.md`, `docs/features.md`, `README.md` | PR-8; Program item ID1's open-source rows (the reviewer and its stream), by issue number once filed | PR-14 | ~500 + 600 |
| PR-9 | web, desktop, roomfake: screen sharing | `web/app`, `web/e2e`, `desktop`, `internal/roomfake`, `docs/features.md` | PR-11 (`web/app`, `docs`), PR-14 (`desktop`) | none | ~650 + 650 |
| PR-10 | web, desktop, roomfake: cameras | `web/app`, `web/e2e`, `desktop`, `internal/roomfake`, `docs/features.md` | PR-9 | none | ~450 + 450 |

Should DR1's, MB0's or RV1's rows, or Program item ID1's open-source
rows, be late (ID1's have no issue in this repository yet), PR-9 and
PR-10 may go before PR-8 and PR-11, PR-9 then depending on PR-6 (for
`web/app`) and PR-14, the later rows merging `main` in, and the order
is recorded on their PRs; screen sharing and cameras never wait on
another item.

**PR-1** (`ext`, `ext/exttest`, `ext/local`, `internal/api`): `RoomJoin`'s
two fields and `ValidAt`; `RoomRequest`'s three fields, bounds and
`Valid`; `ICERequest.By` and the three sites that fill it; `RoomLister`,
`RoomRef`, `RoomsAnswer`, `RoomState`, `RoomPerson`; `RoomInvite`,
`HuddleRecord`; the three `Host` methods; the constants; `ext/ext.go`'s
Records paragraph; `guardMedia`'s `ValidAt` and `Rooms`; `extHost`'s
stubs; `FakeMedia`'s lister and recorded requests, `FakeHost`'s records,
the `Media` check. `ext/local.Media` unchanged (its test that it lists
no rooms). `cmd/conductor/ext_bounds_test.go` gains the room bounds
beside the server's. Tests: PR-1's above.

**PR-15** (`ext`, `ext/exttest`, `internal/api`, `internal/hostagent`,
`internal/proto`, `web/app/utils`, `docs`): `ICERequest.Conn`, the
optional `ICEEnder` and its bounded queue, called at the three sites'
connection ends; `viewer_join.iceServers` to a host whose `register`
declares it reads them, and the host using them for that viewer's peer
alone; the host messages in the protocol's three places; `FakeMedia`
recording the ends. Tests: PR-15's above.

**PR-2** (`ext`, `internal/proto`, `web/app/utils`, `docs`):
`ext/room.go` (the messages, `DecodeAppFrame`, `DecodeServiceFrame`, the
frame budgets, `PointKinds` and their patterns, `EndReasons`, the error
codes, the close codes), `internal/proto/room.go`'s aliases,
`ext/testdata/room/app.json` and `service.json`, the room section of
`web/app/utils/protocol.ts`, `docs/protocol.md`'s Rooms section. Tests:
`TestRoomVectors` and its siblings, `roomProtocol.test.ts` on the same
files.

**PR-3** (`internal/roomfake`, `internal/opencore`, `Makefile`, `docs`):
the `Service`, its control seams, the `Provider`, the `Identity`,
`internal/roomfake/e2e/main_test.go`, the Makefile's `build-roomfake` and
`test-e2e`'s dependency on it, `TestReleaseLeavesOutTheFakes`,
`docs/repository-map.md`'s row. Tests: PR-3's above, among them
`TestTwoPeopleTalkThroughTheFake`.

**PR-4** (`internal/api`, `internal/dispatch`, `web/app`, `docs`):
`rooms.go` (the two routes, the access rules, the ref's parse, `offered`,
the ticket's resolution, the rate limit), the two events, the coalescer
and the pending invitations, the hub's grant per connection, `extHost`'s
three doors built, `existingCandidate` for both routes, `LinkHuddle`,
`MaxHuddleLinks` and `link.huddle` in Dispatch with its schema version
raised and its no-op migration, `DispatchLinks.vue`'s filter, the
protocol rows and types. Kind
`decision` answers no room until PR-8 (no record to read). Tests: PR-4's
above.

**PR-13** (`web/app/utils`): `roomClient.ts`, the whole of The call
above but One screen at a time (PR-9's), with nothing drawn: who offers,
I15's fingerprint rule, the bounds on what waits (I18). Tests:
`roomClient.test.ts`.

**PR-5** (`web/app`, `web/e2e`): `huddle.ts`, `useHuddle.ts`,
`HuddleControl`, `HuddleDeviceCheck`, `HuddleDock`, `HuddlePeople`,
`HuddleIndicator`, `HuddleAnnouncer`; the control in
`DispatchTicketPage.vue`'s and the run page's headers; the layout's dock;
`useAttention.ts`'s two events; the call bound to its credential and
every waiting step's generation (I16); the bridge call to `mediaPress`
where it exists; `web/e2e/huddle.ts` (the second server and its people);
`huddle-offer.spec.ts`, `huddle-audio.spec.ts`. Tests: the vitest files
and specs named for PR-5.

**PR-6** (`web/app`, `web/e2e`): every state of the table not in PR-5 and
not a later row's (the reconnect's, removal's and liveness's screens on
PR-13's client), `stats` and the relayed mark, `HuddleSheet`,
`HuddleInvite`, `HuddlePoint` and `pointRoute`, the M key and its `?`
line, the `huddle` link's line in `DispatchHistory.vue` (an icon, the
note, the time; no URL; the newest 200 and the count of the rest);
`pointRoute` for `ticket`, `run` and `task` only, every other kind "can't
open this yet"; the specs named for PR-6.

**PR-7** (`desktop`): `permissionAllowed` with details and the press
record, `setPermissionCheckHandler`, `mediaPress` and `mediaAccess` in
`preload.ts` and `ipc`, `mac.extendInfo`'s microphone string, the
audio-input entitlement, `installer.nsh`'s program rule and its removal
in its elevated branch. Tests: `window.test.ts`, `packaging.test.ts`.

**PR-14** (`desktop`, `internal/cli`'s one test, `docs`): issue #140's
work and the listener it needs: the WSL launcher turning on the TLS
listener with the server's own variables, the TCP forwarder pointed at
the handshake's `tlsListen`, the router's mapping from Windows with its
lease, the forward's firewall rule, the order at start and the words
when a step fails, `docs/architecture.md`'s lines. No server code and no
new channel between the app and the server. Tests:
`launcher-wsl.test.ts` (from the default settings and an empty data
directory), `tcp-forwarder.test.ts`, `firewall.test.ts`,
`router-map.test.ts`, `TestServeTLSFromAnEmptyDataDir`; by hand, a phone
on a mobile network on a clean install.

**PR-8** (`web/app`, `web/e2e`, `internal/api`): `HuddleControl` on
DR1's `DecisionCard.vue` and in `DispatchDecisions.vue`, on MB0's
`InboxItem.vue`, and on RV1's Review view keyed by its candidate; the
dock's padding on `/decisions` so it never covers the answer buttons;
`/api/rooms` and the join reading DR1's record for kind `decision` (it
exists; its ticket); `pointRoute` for `decision` and `candidate`, and
Show them offering them. Tests:
`TestRoomsDecisionNeedsItsRecord`, `TestJoinDecisionTicketFromRecord`,
`huddle-decision.spec.ts`, `huddlePoint.test.ts`'s PR-8 cases.

**PR-11** (`internal/api`, `web/app`, `web/e2e`, `docs`, `README.md`): a
reviewer admitted to `/api/rooms` and the join for the decision it is
admitted to, and refused everywhere else; `offered` for a decision's room
whose admission admits a reviewer; the reviewer's stream's `room` event
for that decision only, checked at each send; the admission read at each
request and shared by the origin's tabs, so one renewed in another tab
reaches the call's next renewal; the reviewer's pass sent in `join` and
every `renew`, read again at each send, and only to the URL its own
server's `RoomJoin` named; no pass, no join ("Open this decision from
your link again to join its huddle"); the fake's pass check; the app asking `/api/rooms` without the
capabilities read for such a principal; Access ending and the
access-ended words; the README's Extensions line; `docs/features.md`'s
entry (what a person sees, the observed acceptance's line left for the
owner). Tests: PR-11's Go tests and `huddle-reviewer.spec.ts`.

**PR-9** (`web/app`, `web/e2e`, `desktop`, `internal/roomfake`, `docs`):
Share screen, `HuddleScreen` (the sharer's and the viewers' states, the
panel, fit or actual size), `screenMid` sent in `state` before the offer
that carries the screen and the others' screen shown by `person`'s
`screenMid`, One screen at a time in `roomClient.ts` (asking, the
grant, the refusals, the release, what is shown; I17), the fake's
`screen` kind (each video section its writer sends checked against that
writer's `screenMid`, by direction, and a screen refused while the
other shares) and its one sharer, the desktop's
display handler and picker and `mediaAccess('screen')`, the features
entry extended. Tests: `huddle-screen.spec.ts`, `picker.test.ts`,
`window.test.ts`'s display case.

**PR-10** (`web/app`, `web/e2e`, `desktop`, `internal/roomfake`, `docs`):
Camera, `HuddleCameras`, `cameraMid` as `screenMid`, the fake's `camera`
kind, the camera's usage
string and entitlement, `video` after a camera press, the features entry
extended. Tests: `huddle-camera.spec.ts`, `window.test.ts`'s and
`packaging.test.ts`'s camera cases.


## Owner decisions

Dated 2026-11-06. Each is a default the owner can overturn with a dated
row in the Program's status log (the Program, section 3.1's convention).
The Program leaves this plan no open decision of its own: O7 was closed
by D21, which this plan follows (two people's audio first; the group
topology is measured and decided by a later item's own plan).

1. **The room protocol is in the repository's three protocol places.**
   Its records live in `ext`, because a provider's room service in
   another module must import them and cannot import `internal/proto`;
   `internal/proto` names the same records as aliases and runs the same
   vectors, the TypeScript is in `web/app/utils/protocol.ts`, the words
   in `docs/protocol.md`. So the repository's rule (a protocol change
   touches `internal/proto`, `protocol.ts` and `docs/protocol.md`
   together) holds as written, and no article changes.
2. **The fake room service, provider and people are in
   `internal/roomfake`, and the e2e server that registers them is a test
   binary** (`go test -c`), so no build of `cmd/conductor` can contain
   them (`TestReleaseLeavesOutTheFakes`); the huddle specs start it as a
   second server beside the plain one.
3. **Invitations reach the open app through `Host.RoomInvited` and a
   `room_invite` event** sent to admin principals' connections only,
   beside `Host.Notify`, which reaches only the notification sinks (the
   hosted plan's door, record and event, which name no recipient); the
   toast's words claim no more than the server knows.
4. **One call per tab, in the layout**: moving between pages keeps it;
   closing or reloading the tab leaves the huddle; a person's second tab
   or device that joins the same room ends the first ("elsewhere", the
   room service's rule), and one that joins another room is refused
   ("In another huddle").
5. **The screens read `available` and each ref's `offered`**: the
   capabilities' `media` row only decides whether an admin's page asks at
   all.
6. **The phone layout below `sm` (640 px)** is a bottom sheet above the
   bottom bar; screen sharing is not offered there, a shared screen is
   shown.
7. **Mute on the M key**, only in a huddle and only with the focus in no
   text field, terminal or editor; listed in the `?` list.
8. **The work's ticket for a room is the server's reading**, never the
   client's: a room cannot be attached to another ticket by a request.
   The ticket is its immutable id, never its key. The hosted plan took
   this rule in its second round: the join body has no `ticket` field.
9. **A ref is `<kind>:<home>:<id>`** in `GET /api/rooms`, split at its
   first two colons, `home` empty for this server's work (the hosted
   plan's form since its first round).
10. **`point`'s kinds** are the seven `ticket` (by immutable id),
    `decision`, `run`, `task`, `candidate`, `finding` and `file`, each
    with its id's pattern, refused by both ends otherwise, and each
    resolved against the room's own work (the hosted plan's list and
    grammar). The app sends and opens only what it can open for certain
    (`pointable`): `ticket`, `run`, `task`, `decision`, and a `candidate`
    Dispatch's own validator takes; never `finding` or `file`.
11. **Remove** is offered to an admin principal on the server that holds
    the work; the room service decides whether it is taken.
12. **No audio settings** beyond the microphone, the speaker and Join
    muted; the browsers' own echo cancellation, noise suppression and
    gain control stay on.
13. **The Windows program rule comes with the installer when it runs
    elevated**; a per-user install needs none, and no Settings button
    asks for it.
14. **The decision's huddle, with its reviewer (PR-8, PR-11), goes before
    screen sharing and cameras**, since the acceptance is a decision
    discussed by two people; two people's audio against the fake comes
    first, since it waits on no other item.
15. **On Windows with the server inside WSL, the desktop app's reach
    `auto`, its default, makes the server reachable from the internet**
    (PR-14): a TLS listener with a certificate for the public address,
    the router's mapping and the forward, all made by the app. Today
    every desktop install serves loopback alone, and the macOS and Linux
    desktop apps stay so; reach `manual` or `off` in Settings keeps a
    Windows install so too. The acceptance's phone needs it, and nothing
    is configured by the person (`AGENTS.md`).

## Acceptance

The item's boundary (the Program, section 4.5, CL1) is people discussing
a decision around the same work and recording the outcome through the
normal workflow, with no media fault ever interrupting execution. It is
observed with a registered provider and its room service, which are not
in this repository, on a build at a tag holding PR-11 and PR-14, DR1's
and MB0's records and pages and Program item ID1's open-source rows:

1. A managed run opens a decision. The operator opens it on `/decisions`
   and presses **Huddle**; the device check asks for the microphone only
   then. A second person, a reviewer admitted to that decision, opens it
   and sees "<operator> is in a huddle · Join", joins from a phone on a
   mobile network, and the two talk. One side is the desktop app on
   Windows with the server inside WSL, with nothing configured on
   Windows: the phone reaches that server through PR-14's forward and
   mapping.
2. Each sees the decision as their access allows; the operator points
   the other at the evidence (**Show them**).
3. The decision is answered on its card, its receipt shows DR1's four
   states and the run resumes; the huddle ends, and the ticket's history
   shows its line. Nothing said is kept anywhere in this server's data
   directory.
4. A media fault: during a call the room service is stopped; the run goes
   on, the terminals answer, the decision can be answered; the call says
   "Reconnecting…" and ends at 60 s; with the service back, Huddle is
   offered with no restart.
5. A plain build of this repository at the same tag shows no huddle and
   behaves as before.

What this repository's PRs prove without a provider, on `main`: rows
PR-1 to PR-7, PR-13 and PR-15 give two people's audio end to end against the
fake, two principals of one server, with every state, on a desktop and a
phone layout, and the media-fault and plain-build invariants (`huddle-terminal.spec.ts`, `huddle-offer.spec.ts`,
`TestRoomFaultsTouchNoSession`). Done for this plan is every row merged
with its checks green and the by-hand rows above made; Done for the item
is the five steps observed, recorded in `docs/features.md` and in the
Program's status log. The owner is needed for the by-hand rows, the
second person and the observation. Done covers two people; small groups
are a later item's.

## Changed in review round 1

The independent review's 17 findings and Codex round 1's 13, answered
together, with the hosted plan's specification section read again at its
current head, after its second review round, before this round was
pushed.

- **The specification as it stands.** The seam's records, routes, events,
  message set, frame bounds and states follow the hosted plan's first two
  rounds: `RoomRequest.Renew` and `Ticket` as the ticket's immutable id;
  `RoomRef.Ticket`; one `RoomState` per ref, empty rooms included, with
  `Scope`, `InviteURL` and the people's `Via`; `RoomInvite` and
  `room_invite`, at most 32 pending; `ICERequest.By` at the three ICE
  sites, and `Conn` with the optional `ICEEnder` and a viewer's own
  servers on a host (PR-15, new); the ref `<kind>:<home>:<id>` (was a
  space before the home); `available` and each ref's `offered`; `renew`
  in the join, whose body has no ticket; `join.caps`, `joined.session`, a
  rotated one-use resume token, `invitees` paged, `invite`, `invitees`
  and `remove` an operator's place only, the seven `point` kinds with
  their patterns, `screenMid` and `cameraMid` in `state` and `person`,
  `tracks`, `from: "forwarder"` only after `topology`, `person.via`,
  `ended`'s `media`, `4401` for a resume token and `4409`'s reasons.
  PR-9 and PR-12 no longer wait on the screen's marking or the forwarded
  signalling.
- **Who is who** (new section): the workbench token is the operator, one
  person; two principals of one server are two people by subject; a
  person's second device is the same person; a removed person is cut off
  by the others. The e2e binary registers fake people (`roomfake.Identity`)
  so two browser contexts are Ana and Ben, and the operator's two contexts
  test "elsewhere".
- **Bounds on encoded bytes.** Per-frame budgets (`join`, `renew`,
  `resume` 6 KiB; `joined` 48 KiB; `ice` 44 KiB; `invitees` 20 KiB), a
  ticket of token characters, `person` ids of `[A-Za-z0-9_-]`, candidates
  of the ICE grammar's characters, `TestRoomFrameBudgets`; the rooms reply
  carries three people per room and is bounded at 256 KiB, computed.
- **The client.** I8 (a person who leaves or is removed is cut off by the
  others, proved against a client that ignores its end), I9 (Show them
  only navigates; points resolved against the room's work), I11
  (`ping`/`pong`, a quiet socket reconnects at 25 s and ends at 60 s),
  ICE retried on its own after a second failure, a new `ice` restarting
  ICE on a relayed connection.
- **Server.** `offered` per ref; invitations sent to their invitee's
  connections only (`RoomInvite.To`, proposed; I10); the `room` event a page cannot
  do without, the coalescer collapsing to `room {}` rather than dropping;
  a once-a-minute re-read while not available; `RecordHuddle` replaying
  its first answer before resolving the ticket; huddle links counted
  apart (`MaxHuddleLinks` 200, the oldest evicted); `DispatchLinks.vue`
  leaving huddles out.
- **States.** In another huddle, Access ending, Media failed, a person
  through another's server, Invite for an operator with Copy link, an
  invitation for work held elsewhere, Room full's size, Ending soon's
  limit, a phone's paused page, one person's audio not connecting.
- **Protocol location.** `internal/proto/room.go` aliases and the
  TypeScript in `protocol.ts`, so the three-places rule holds with no
  article change (Owner decisions, 1).
- **Rows.** PR-13 (the room client, with the reconnect and rejoin in one
  row) split out of PR-5 and PR-6; PR-15 (one relay credential per
  connection, and its end) after PR-1; PR-16 (the phone app's microphone
  and camera, on MB1's shell); PR-14 (issue #140: the TLS listener
  forwarded into WSL and the router mapped from Windows, for the
  acceptance's phone) added; PR-11 moved before PR-9 and PR-10; PR-12
  waits on the provider's measurement; `TestReleaseLeavesOutTheFakes` in
  PR-3.
- **Desktop.** The Windows program rule only from an elevated install, a
  per-user install needing none, checked by hand; the WSL table's row for
  a phone; the forwarded TLS listener's inbound TCP rule, listening on
  the LAN only once it exists.
- **Words.** The reviewer and its admission defined here; the latency
  question stated as this plan's choice; "Waiting on the hosted plan"
  rewritten to what is still open there (an invitation's recipient,
  offered on a server of several people, liveness, the encoded budgets,
  one sharer, the rows' order), each with the row that waits; Owner
  decisions 1, 3, 4, 5, 8, 9, 10, 13 and 14 brought to this text.

## Changed in review round 2

Codex round 2's 14 findings, answered after the hosted plan's
specification section was read again at the head of its third review
round. That round took group calls out of the hosted plan, admitted a
reviewer only with their own pass, which the room service checks
itself, and kept a phone's huddle in its browser. This plan follows it,
and where a round-1 fix did not hold it asks first whether two people's
audio, screen sharing and optional cameras need the mechanism at all.
The rule it applies: **whenever the app is not certain a person may
join, or what a connection carries, it does not join or does not keep
it, and says why.** A call that does not start, or a moment of silence
after a reconnect, is an inconvenience; a person who hears a call they
may not is the defect.

- **Removed.** PR-12 (small groups) and everything that existed only for
  it: `join.caps`, `joined.topology`, the `topology` and `tracks`
  messages, `signal` to and from `forwarder` and I8's exception for it,
  `ended {why: "media"}`, `stats.path` `forwarded`, the Media failed
  state, the WSL table's forwarded row, the fake's settable `maxPeople`,
  `TestTracksBounded`, the `tracks` vectors and `huddle-group.spec.ts`
  (findings 10, 11 and, with PR-16, 14). PR-16 (the phone app's
  microphone and camera on a shell not in this repository). Keeping a
  peer connection across a reconnect (finding 3). The eviction of the
  oldest huddle link past 200 (finding 8). `point`'s `finding` and
  `file` from the app's sending and Go (findings 6 and 7). Out of scope now says that
  small-group media is a later item with its own plan, and that the
  phone app's huddle and a committed-file page are later rows.
- **Narrowed promises.** A reconnect keeps no media: nothing is sent or
  played while `reconnecting`, and every connection is made again from
  the roster in `joined` (I13). Only a resume refused `4401` leads to a
  fresh join, once; every other refusal ends the call with no request
  (I14). A ticket records at most 200 huddles of its own, and its read
  shows the newest 200 on every replica, with no promise about how many
  a merge stores. Show them opens only pages that exist when its row
  merges: `ticket`, `run` and `task` in PR-6; `decision` and a
  `candidate` Dispatch takes in PR-8, after their pages; `finding` and
  `file` nowhere yet.
- **Added, each with its states, its invariant and its test.** I13 and
  I14 in the room client; `RecordHuddle`'s replay by its arguments with
  only a done receipt answering nil; the mapping's handoff (one
  authority by `--reach-mapper`, the server's stdin as its channel, a
  lease, its states); the host's explicit ICE list first; `pass` in
  `join` and `renew` (the hosted plan's third round) with its 2 KiB
  bound and the ticket frames at 8 KiB; the join reply at 8 KiB.
- **The findings.** 1 (replay): `LinkHuddleArgs` is the record, the
  ticket resolved after the replay; only a done receipt is nil, a kept
  refusal or `op_conflict` is an error; tested after a move, a prune,
  a refusal. 2 (removal and a fresh join): I14. 3 (reconciliation on
  reconnect): I13. 4 (the join reply): 8 KiB, computed,
  `TestJoinReplyBounded`. 5 (point grammars): `crew.ValidRunID` and
  the wire keeps the specification's grammar, and the app sends and
  opens a candidate only when Dispatch's own validator takes it too
  (`pointable`, `TestPointableMatchesTheValidators` across the
  contracts). 6 (a finding's number): the app never sends or opens a
  `finding`, since a number names no one finding once a candidate has
  two reviews. 7 (the file destination): `file` never sent or opened;
  `decision` and `candidate` moved to PR-8, after their pages. 8 (huddle retention): no eviction, a bounded read,
  `TestHuddleLinksMergeConverge`. 9 (a host's `--stun`): kept first,
  `TestHostICEPrecedence`. 10 and 11: removed with the group row. 12
  (the mapping's handoff): defined in the desktop section, in PR-14
  with `internal/cli`. 13 (invitations): coalesced per room and
  recipient, `TestInvitationsPerRoomAndRecipient`. 14 (PR-12 and PR-16
  in parallel): both rows removed.
- **Waiting on the hosted plan** is gone: that plan is final, nothing
  here waits on it, and "The hosted plan" says where this plan states
  more than its specification, which the rows build and a provider's
  service is checked against.

## Changed in review round 3

Codex round 3's nine findings, answered after the hosted plan's
specification section was read at its final head. The rule of round 2
holds, said once more: **whenever the app is not certain a person may
join, what a track is, or that a connection is the one it asked for, it
does not join, does not show it, or ends the connection, and says why.**
Where a mechanism has now failed review twice, it is gone and the
promise is stated narrower, in words a reader can check. Nothing here
narrows the item's boundary: two people discuss a decision around the
same work, audio first, then screen sharing, then optional cameras; a
media fault never interrupts a session, a run or a decision; no device
is opened before a press; nothing is recorded.

- **Removed.**
  - `ping` and `pong`, the client's 25 s silence rule, the fake's
    answer and `TestFakeAnswersPing`: the specification has no
    keepalive message, and a room service is not asked for one.
  - `RoomInvite.To`, invitations kept per room and recipient, and
    `room_invite` addressed by subject
    (`TestInvitationsPerRoomAndRecipient`): the specification's record
    names no recipient.
  - Any claim message or echo for the one sharer: the client reads the
    room service's refusal of a second screen (`media_kind`, which the
    specification gives) and the other side's answer, nothing else.
  - The mapping's handoff on the server's standard input
    (`--reach-mapper`, `reach.AppMapper`, its lines, states and seven Go
    tests): no channel runs from the desktop app to the server, so no
    new wire needs a place in the protocol's three places.
  - "No operation removes a huddle link" as a claim about every build.
- **Narrowed promises.**
  - A call ends 60 s after its socket to the room service closes with no
    `joined` since. A room service that stops answering on a socket it
    keeps open is not noticed by the app (I11).
  - An invitation is shown on every admin page of the server it reached,
    never to a reviewer or a share link's viewer; it says "invites you"
    only on a server with one person, else "sent this server an
    invitation" (I10).
  - Never two screens at once; two presses at the same moment may give
    none, and the refused side is told; a reconnect stops sharing (I17).
  - The phone's `https://` address is offered only once its certificate
    was issued through the router's mapping and the app's forward; a
    mapping the router drops later is not seen by the server, whose
    address may stay offered, unreachable, until the certificate's next
    renewal fails, six days at most.
  - Huddle links are never removed by a build that knows the kind, and
    never written by one that does not (the schema version).
- **Added, each with its order, its invariant and its test.** I15 (one
  place per person, by the DTLS fingerprint, and who offers a new
  connection); I16 (the call bound to its credential, every waiting step
  to its generation); I17 (one screen at a time: asking, the grant, the
  refusals, the release, what is drawn); I18 and "Bounds on what
  accumulates" (pending candidates 64, waiting signals 32, 20 frames a
  second out, an outgoing queue of 256 frames and 1 MiB, the socket's
  buffer, and the fake's own bounds); `existingCandidate` in both room
  routes; Dispatch's schema version raised with a migration that changes
  no table; the SDP check by direction (`TestFakeSDPCheckByDirection`);
  the TLS listener the desktop app turns on, the order at start, and the
  words when a step fails (`TestServeTLSFromAnEmptyDataDir`,
  `launcher-wsl.test.ts` from the default settings).
- **The findings.** 1 (TLS on a clean install): the WSL launcher turns
  the TLS listener on with the server's own variables, after the rule,
  the forward and the mapping; a failed step leaves the server as today
  and says why. 2 (the call and the principal): I16. 3 (the old device
  while the other stays connected): I15. 4 (one sharer): One screen at a
  time, I17, with no message the specification lacks. 5 (the SDP check
  and received sections): the check reads only the sections the
  writer sends; a `recvonly`, `inactive` or rejected section is never
  checked; tested with one side only receiving. 6 (older Dispatch
  writers): the schema version, and the replication rule for CS1. 7 (a
  candidate's existence): `existingCandidate` in both routes. 8 (what
  accumulates): I18. 9 (the handoff's wire): the handoff is removed, so
  there is no wire to place.

## Changed in the final check

The plan read again against the hosted plan's specification section at
its final head, for any requirement this plan still placed on a
provider or its room service that the specification does not meet.

- **Taken out.** `RoomsAnswer` in the order asked: the guard and the
  `Media` check match the states to the refs by kind, id and home, as
  the specification has it. A retry of `RecordHuddle` carrying the same
  record: the operation's arguments are the room session alone, so a
  retry with other names or another end gets the first answer and writes
  nothing, as the specification's idempotence asks. An `error`'s
  `message` at most 200 bytes: it is bounded by its frame, and never
  shown or logged.
- **Said where it was missing.** A quiet room service holds no one:
  Leave ends the call at once with no answer from it, and nothing of a
  session, a run or a decision waits on it (Liveness, I11). What a person
  sees while the phone's address is offered but unreachable (The server
  is told nothing after its start). A first `join` with no `joined` has
  no token to resume with and takes the fresh-ticket branch. The ask for
  a host's per-viewer servers holds nothing of the hosted session
  (`TestHostViewerICEAskHoldsNoSession`).
- **Rows.** Every row is in this repository; screen sharing and
  cameras may go before the decision rows when Program item ID1's
  open-source rows are late too.
- **Owner decisions.** 15, the internet reachability the desktop app's
  default gives a Windows install with the server inside WSL.
