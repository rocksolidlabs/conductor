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
people's audio, then screen sharing, then optional cameras, with small
groups after a separate qualification.

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
  Windows). PR-14 is that issue's work, done in this stream because the
  acceptance's phone needs it.
- The phone app's shell (Program item MB1's, not in this repository
  yet): PR-16, the microphone and the camera in that app, waits on it and
  names its rows by issue number once they are filed; until then a
  phone's huddle runs in the phone's browser.
- A provider: outside this repository. No row here waits on one; the
  item's acceptance (below) is observed with one, and PR-12 (groups)
  waits on its group topology being measured and qualified.

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
- **Show them.** One person points the other at a finding, a decision, a
  ticket or a file at a commit; the other's page offers "Ana is pointing
  at finding 3 · Go", which their own app turns into a page of their own
  server, which opens it or refuses ("You can't open this on your
  server"). Go only navigates.
- **Invitations, removal, reconnect.** Invite lists who the room service
  says may join; the invitee's open Conductor, and only theirs, shows
  "Ana invites you to a huddle on PRJ-12 · Join". A dropped network shows
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
  for that decision.
- **A person vouched for by another server** (a room whose scope admits
  other servers' operators): the room service names them with `via`, the
  name of the server that vouched for them; the screens say "Ben, through
  Ana's server".
- **One person's second device or tab**: the same subject, so the same
  person. Joining the same room from a second device ends the first with
  `ended {why: "elsewhere"}` ("You joined this huddle from another
  window."); joining another room while in one is refused with `4409`
  and the reason `elsewhere` ("You are in another huddle; leave it
  first.").
- **A person removed, or whose access ended, mid-call**: the room service
  ends their place (`ended {why: "removed" | "access" | "unconfirmed"}`),
  tells everyone else `left {person, why}`, and refuses their resume
  token (`4403 removed`). Every other client closes its connection to
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
  alone. The host messages change in the protocol's three places.
- **Presence: `RoomLister`, an optional interface** a provider implements
  beside `MediaRoomProvider`:
  `Rooms(ctx, refs []RoomRef) (RoomsAnswer, error)`, safe for concurrent
  use, answered from what the provider holds. `RoomRef {Kind, ID, Home,
  Ticket}` (`Ticket` filled by the server as for a join, so a provider can
  tell an empty review or run room's scope before anyone is in it; the
  guard matches the answer's rooms to the refs by kind, id and home); at
  most `MaxRoomRefs` (32) per call. `RoomsAnswer {Available bool, Rooms
  []RoomState}`, **one `RoomState` for each ref asked, in the order
  asked, an empty room included**, so an empty room and an unknown ref
  never look the same. `RoomState {Kind, ID, Home, Room, Since time.Time
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
  - `RoomInvited(inv RoomInvite)`: an invitation for one person of this
    server. `RoomInvite {Kind, ID, Home, Room, By {Subject, Name}, To
    string, Until time.Time}`: `To` the invitee's subject (`""` for this
    server's operator; proposed to the hosted plan, Waiting on the hosted
    plan), `Until` at most `MaxRoomInvite` (10 minutes) ahead; `Valid`.
    The server keeps at most `MaxPendingInvitations` (32) pending,
    coalesced per room, the oldest dropped, and sends `room_invite {…}`
    to the invitee's events connections only (PR-4). `Host.Notify` hands
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
  state per ref asked, in order, so a provider can neither show a room
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
32, `MaxHuddleSpan` 24 h.

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
`[A-Za-z0-9_-]`, a ticket of token characters and a candidate of the ICE
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
| `join`, `renew`, `resume` (a 4,096-byte ticket in its envelope) | `MaxRoomTicketFrame`, 6 KiB |
| every other frame (a `candidate` at most 1 KiB) | `MaxRoomFrame`, 4 KiB |

`DecodeAppFrame(b []byte) (AppFrame, error)` and
`DecodeServiceFrame(b []byte) (ServiceFrame, error)` check the frame's
size for its type (read from a bounded first look at `t`, the largest
bound applying until then) before decoding, and each field's bound after.

App to room service:

| Message | Fields and bounds |
|---|---|
| `join` | `ticket` (the `RoomJoin.Token`), `caps?` (a subset of `forwarded`, sent from PR-12); the first frame, within 10 s |
| `resume` | `token` (from the last `joined.resume`: 43 characters of base64url, one use); in place of `join`, within 60 s of a drop |
| `renew` | `ticket` (a ticket the server answered for `renew` = `joined.session`); every `joined.renewEvery` seconds |
| `ping` | `n` (0 to 2^31−1); every 10 s, answered by `pong` |
| `signal` | `to` (a `person`, or `forwarder` in the `forwarded` topology), `sdp?` `{type: offer \| answer, sdp}`, `candidate?` `{candidate, sdpMid?, sdpMLineIndex?}` (`sdpMid` at most 16 bytes); exactly one of the two |
| `state` | `muted`, `sharing`, `camera`, `screenMid?`, `cameraMid?` (the `mid` of the transceiver that sends the screen and of the one that sends the camera, each 1 to 16 bytes of `[A-Za-z0-9_-]`, sent before the offer that carries them: browsers mark nothing in an SDP that tells a screen from a camera, so the room service checks a video section's `mid` against these and the other side shows a track by its `mid`) |
| `invitees` | `after?` (a `next` from the last page): ask whom one may invite; an operator's place only |
| `invite` | `person`; an operator's place only |
| `remove` | `person`; an operator's place only |
| `point` | `kind`, `id`, a reference, never text; `kind` one of the seven `PointKinds`, each id at most 200 bytes and of its pattern: `ticket` a ticket's immutable id (16 lowercase hex digits); `decision` `dr-` and 1 to 10 digits; `run` a run's id (`[a-z0-9][a-z0-9-]{0,63}`); `task` `<run>/<task>`, the task id `[a-z0-9][a-z0-9_-]{0,39}`; `candidate` `<run>/<task>/<n>@<commit>` (DS2's form, `n` 1 to 3 digits, the commit 7 to 40 lowercase hex digits); `finding` 1 to 9999, a finding of the review the room is on; `file` `<path>@<commit>`, the path relative, 1 to 160 bytes, with no `..` segment, no leading `/` and no `\`, the commit 7 to 40 lowercase hex digits. Anything else is refused by both ends, and the room service relays only these |
| `stats` | `path` (`direct`, `relay`, `forwarded`), `rttMs`, `lossPct`, `kbpsOut`, `kbpsIn` (each 0 to 1,000,000), `kinds` (a subset of `audio`, `screen`, `camera`); at most every 30 s |
| `leave` | none |

Room service to app:

| Message | Fields and bounds |
|---|---|
| `joined` | `you` `{person, name}`, `room`, `session` (the room session, 1 to 64 of `[A-Za-z0-9_-]`, the same across a resume), `people` (at most 8 `person` records), `iceServers` (at most `MaxICEServers`, each `ICEServer.Valid`), `resume` (the next resume token), `topology` (`direct` \| `forwarded`), `kinds`, `maxPeople` (2 to 8), `endsAt`, `renewEvery` (30 to 300) |
| `person` | `person`, `name`, `via` (`""` or the vouching server's name), `muted`, `sharing`, `camera`, `screenMid?`, `cameraMid?`, `state` (`in` \| `reconnecting`) |
| `left` | `person`, `why` (one of `EndReasons`) |
| `signal` | `from` (set by the room service: a `person`, or `forwarder`, a value no person id takes), `sdp?`, `candidate?` |
| `invitees` | `people`: at most 50 of `{person, name}`; `next?` |
| `invited` | `by` `{person, name}`, `room`, `kind`, `id`, `home` |
| `point` | `from`, `kind`, `id` |
| `ice` | `iceServers` (fresh relay credentials) |
| `topology` | `mode` (`forwarded`, from PR-12) |
| `tracks` | `tracks`: at most 24 of `{mid, person, kind (audio \| screen \| camera)}`; in a forwarded room only, before the forwarder's offer that carries them |
| `pong` | `n` |
| `ending` | `at` |
| `ended` | `why` (`EndReasons`: `left`, `removed`, `access`, `unconfirmed`, `elsewhere`, `time`, `unavailable`, `media`), `by?` `{person, name}` |
| `error` | `code` (`rate`, `media_kind`, `not_allowed`, `no_room`, `bad_frame`), `message` (one line, at most 200 bytes) |

Close codes, constants of `ext`: `4400` bad frame or no `join`, `4401`
bad ticket or resume token, `4403` not allowed (the close reason
`not_allowed`, `removed` or `not_entitled`), `4409` refused (the reason
`room_full` or `elsewhere`), `4410` ended (after an `ended`), `4429` too
many, `4503` unavailable, `1001` service stopping. A `1001` close, a
dropped socket or a silent one (below) is **not** an end: the app
resumes.

**Vectors**: `ext/testdata/room/app.json` and `service.json`, one valid
frame of each message at its largest with escaped strings (among them a
`join` with a 4,096-byte ticket, which decodes), for each bound one frame
one byte (or one item) past it, each of the seven `point` kinds at its
pattern's edges and one past, `state` with each mid at 16 bytes and 17,
and `tracks` at 24 entries and 25, each with the error it must get. `ext`'s
`TestRoomVectors`, `internal/proto`'s `TestRoomVectorsThroughProto` and
`roomProtocol.test.ts` read the same files, so the Go records and the
TypeScript cannot drift; a provider's room service runs them too.

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
  each `joined`, a removed person's resume refused (`4403 removed`),
  `pong` to every `ping`, `invitees` from a list the test sets, paged,
  `invite` and `remove` from an operator's place, `point` (the kinds'
  patterns checked), `stats` (counted, dropped), the end reasons, the
  close codes, `maxPeople` 2 (a setting, for PR-12), `kinds` audio
  (screen from PR-9, camera from PR-10; an SDP's `m=` lines read by a
  small scanner, a `video` section refused while the kind is off).
  `iceServers` is empty: no relay, the two browsers on one machine
  connect on host candidates. It never stores or logs a frame's contents.
- **Control seams for tests**, on the same listener under `/control/`,
  each behind a random control token: `drop` (close one person's socket
  with no `ended`, so the app resumes), `refuse-resume`, `end?why=`,
  `remove?person=`, `full`, `ending?in=`, `hold` and `release` (hold
  every frame to one person, `pong` included, until released, the
  socket left open), `stop` and `start` (close and reopen the listener),
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
  `Host.RoomInvited` with the invitee's subject in `To`, and a room left
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
  recorded; a candidate of DS2's form whose run is known; a decision once
  DR1's records are on `main`, PR-8); a ref that does not is dropped from
  the question with no word, so the route tells nobody what exists; the
  server sets each ref's `Ticket` from its own records before it asks. The
  reply is at most 256 KiB, the sum of 32 states at their bounds with
  every string at its worst escaping (`TestRoomsReplyBounded` builds that
  answer). `400 invalid_request` for a malformed or 33rd ref.
- **`POST /api/rooms/join {kind, id, home?, renew?}`** (body at most 512
  bytes, `DisallowUnknownFields`) → `200 {url, token, expiresAt, room,
  protocol}` (at most 6 KiB) or `409 {error: {code: "no_room"}}` when the
  provider answers ok false, fails, is late, panics or answers a join
  `ValidAt` refuses. The server builds the `RoomRequest`: `By` the
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
    most 3 KiB), sent only to the events connections whose principal's
    subject is the invitation's `To` (the operator's for `""`): the hub
    keeps each connection's subject beside its channel. Never to a share
    link's viewer or a reviewer, who joins from the decision page they
    were given.
- **`extHost`'s doors**: `RoomChanged` feeds the coalescer;
  `RoomInvited` puts the invitation among the 32 pending (one per room,
  the newer replacing the older, the oldest dropped past 32), neither
  ever blocking the provider. `RecordHuddle` first asks Dispatch whether
  its operation id was answered (`Store.Replay`, by actor and op id,
  which keeps a pruned operation's first row for good) and answers nil
  when it was, before it reads which ticket the work belongs to now, so a
  retry after a run was moved from one ticket to another replays the
  first record instead of meeting `op_conflict`; only an operation never
  answered resolves the ticket as the join does, and, with none, answers
  nil and writes nothing; with one, it calls Dispatch's new typed
  operation.

`internal/dispatch`: a link kind `huddle` (`LinkHuddle`), reserved as
`LinkDecision` is: refused on `link.add` (`RefusalInvalid`, "the kind
huddle is written by a huddle's end only"), kept when a merge brings one.
**Huddle links are counted apart**: `MaxLinks` (50) counts every other
kind, and a ticket keeps at most `MaxHuddleLinks` (200) live `huddle`
links, the oldest removed in the same operation when a new one would pass
it, so huddles never take a place an ordinary link needs and a ticket
already holding 50 links still records its huddle. A new operation
`link.huddle` (`Store.AddHuddle(ctx, key, HuddleLink, op Op)`) writes it
(and any eviction) with one receipt, its actor `provider:<name>` (at most
49 bytes, inside `MaxActor`), its op id `h-` and 30 characters of base32
of the SHA-256 of the room and `since`, so a second call for the same room
session replays the first receipt and writes nothing. The link's `ref` is
`<room>@<since as RFC 3339>`; its `note` is "Huddle with Ana and Ben · 17
min" (names Markdown-escaped, "and 3 more" past four, the duration in
whole minutes; at most 200 bytes). A ticket's read grows by at most 200
such links of at most 1 KiB each. The kind is text in the store, so no
schema version changes and no migration runs.

`web/app/components/DispatchLinks.vue` leaves `huddle` out as it leaves
out `run` and `decision`, so a huddle never shows as an external link
with an empty URL and a remove control; the history shows it (PR-6).

Docs: `docs/protocol.md`'s HTTP API rows for both routes, the two events
and the link kind, with their bounds; `web/app/utils/protocol.ts` types
and `protocol.test.ts` cases for them.

### `web/app`: the screens (PR-13, PR-5, PR-6, then PR-8, PR-11, PR-9, PR-10, PR-12)

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
| Invitation received | a toast from `room_invite`: "Ana invites you to a huddle on PRJ-12 · Join · Dismiss"; Join opens the work's page in its device check when this server holds the work, else "This huddle is on another server; you can't join it from this server." |
| Show them | sent: "Pointed Ben at finding 3"; received: "Ana is pointing at finding 3 · Go"; Go navigates to the page the app builds itself, or shows "You can't open this on your server" |
| Room full | "This huddle is full (2 people)." Nothing else changes |
| In another huddle | "You are in another huddle; leave it first." (`4409 elsewhere`) |
| Removed | "Ana removed you from the huddle." Media stopped, devices released |
| Access ended | "Your access to this huddle has ended." (`access`, `unconfirmed`); a reviewer whose admission ended sees the decision page's own words (PR-11) |
| Access ending (a reviewer, five minutes before the admission ends; PR-11) | "Your access ends in 5 minutes · Renew access": opens the decision page's own renewal in a new tab; the renewed admission reaches this tab's next renewal and the call goes on; the new tab says "Access renewed; you can close this tab" |
| Elsewhere | "You joined this huddle from another window." |
| Reconnecting | "Reconnecting…" with the mute state kept; back within 60 s, or "Could not reconnect · Rejoin". On a phone: "Your phone paused this page; the huddle comes back if you return within a minute" when a locked screen or another app in front suspends it |
| Service unreachable | "Reconnecting…", then at 60 s without the room service, or at `ended {why: unavailable}`: "The huddle ended: the huddle service is unreachable." |
| Ending soon | "This huddle ends in 5 minutes (its <n>-hour limit)." (`ending`; the limit from `endsAt` and the room's `since`), then `time`: "The huddle reached its time limit." |
| Media failed (groups, PR-12) | "Your connection to the group failed · Rejoin" (`ended {why: media}`) |
| Screen sharing (PR-9) | Share screen opens the browser's picker (the desktop app's own); "You are sharing your screen · Stop"; the others see it in a resizable panel beside the work, fit or actual size; "Ben is sharing; ask them to stop first"; not offered on a phone |
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

**Show them only navigates.** A received `point` is checked against
`PointKinds`' patterns again, and `pointRoute(room, kind, id)` builds the
route from the room the call is on and the kind and id alone, never from
a path or URL in the message: `ticket` the ticket page of the ticket with
that immutable id; `decision` its card on `/decisions`; `run` the run
page; `task` the run page at that task; `candidate` the Review view of
that candidate; `finding` the Review view of the room's candidate at that
finding (a review's room only); `file` the run's Files view of that path
at that commit, for a review's or a run's room (the run's repository).
Another kind, a finding or a file in a room that
gives it no run, or a room on another device's record that this server
does not hold, answers "You can't open this on your server" and offers no
Go. Go calls the router's `push` and nothing else: no route that changes
anything is ever called from a point, and the page it opens is read with
the person's own grant, which refuses what they may not read.

**The call** (`RoomClient`, PR-13). After `joined`, in the `direct`
topology one `RTCPeerConnection` per other person, with
`joined.iceServers`, an audio transceiver, trickled candidates through
`signal`, and perfect negotiation (the polite side the one whose `person`
sorts higher). Mute sets the track's `enabled` false and sends `state`;
nothing is renegotiated.

- **Someone leaves, or is removed** (I8): on `left {person}`, whatever its
  `why`, and on its own `ended`, the client stops its senders to that
  person and closes the peer connection with them **before** it draws the
  change, and drops every `signal` whose `from` is not in its people list,
  with one exception: `from: "forwarder"`, taken only after `topology
  {mode: "forwarded"}` of the room session the client is in. Since a direct call's
  media goes browser to browser, this is what stops a removed person's
  audio, whatever their own client does.
- **ICE.** A new `ice` updates every connection through
  `setConfiguration` and restarts ICE on a connection whose selected pair
  is a relay. An ICE failure restarts ICE; a connection that fails again
  is closed and negotiated anew after 2 s, then 4 s, then 8 s while both
  people are `in`, whether or not the room service reports a change; after
  the third, the person shows "Can't connect to Ben's audio · Try again",
  whose press starts the tries over.
- **Renewal.** Every `renewEvery` the client asks
  `POST /api/rooms/join` again with `renew` set to `joined.session` and
  sends `renew` with that ticket; a refused renewal is tried at the next
  tick, and the room service decides when to end (`unconfirmed`).
- **Liveness.** The client sends `ping` every 10 s; a socket on which no
  frame has arrived for 25 s, a `join` or `resume` with no `joined` within
  10 s, a dropped socket and a `1001` close all start **reconnecting**, the
  silent socket closed by the client: a new socket with `resume` (backoff
  0.5 s doubling to 8 s), a refused resume a fresh ticket and `join`;
  after a rejoin every peer connection still `connected` to a person who
  is `in` again is kept, and only someone new, or one whose ICE failed, is
  negotiated anew. 60 s after the last frame the room service sent, the
  call ends with the unreachable words, whether the socket closed or only
  went quiet.
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
  #140): the app forwards the server's TLS listener from Windows into the
  distribution itself (a TCP forwarder beside its UDP forwarder, on the
  port the server listens on, from every Windows interface to the
  distribution's address, followed as that address changes), makes the
  router's mapping from Windows (the mapping `internal/reach` makes on
  Linux, made here by the app because the distribution sits behind
  Hyper-V's NAT), and tells the server inside WSL the mapped address so
  its reach reports it. A phone's connection to that listener is inbound
  and unsolicited, which no outbound socket's reply lets in, so the
  forward needs an inbound TCP rule for its port, provisioned the way the
  ICE port's UDP rule is: by the installer when it runs elevated, else by
  the app's own elevated `netsh` with fixed arguments (`firewall.ts`; the
  system's own consent, once; nothing typed, no setting to find). The
  forward listens on Windows' LAN interfaces only once the rule exists,
  so Windows' own "allow access" dialog never appears; until then the
  page is not offered to another device.

### Windows with the server inside WSL

A person configures no networking, ever (`AGENTS.md`). Every media path
of a huddle starts in the person's browser, which on Windows is the
desktop app's window (or a browser) running on Windows, not inside WSL;
the one path that comes in from outside Windows is a phone's or another
machine's request for the server's page:

| Path | From, to | Crosses the Hyper-V NAT? | What the desktop app does |
|---|---|---|---|
| The pages, `GET /api/rooms`, `POST /api/rooms/join`, the events, from the window | the window to the server in WSL | yes, as every page request today, over the connection the app already makes | nothing new |
| The same, from a phone or another machine (the acceptance's second person) | the phone, on the LAN or a mobile network, to the server's `https://` address | yes, inbound to Windows and on into WSL; from a mobile network, inbound through the router too | PR-14: the TLS listener forwarded into WSL, its firewall rule, and the router's mapping, all made by the app |
| The room service's WebSocket | the window, or the phone's browser, to the room service | no: outbound | nothing |
| Direct media (UDP) | the window to the other person's browser | no: Windows' own interfaces gather the candidates | nothing to forward; the installer's program rule when it ran elevated (PR-7) |
| A relay | the window, or the phone, to the relay the room service names | no: outbound | nothing |
| Forwarded media (groups, PR-12) | the window to the room service's forwarder | no: outbound | nothing |
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
entry (PR-11, extended by PR-9, PR-10 and PR-12).

### Waiting on the hosted plan

The provider's plan (Program item CL1's other plan file, kept with the
provider and not in this repository) holds this plan's specification.
Its first two review rounds are settled, and this plan follows it as it
stands: the records (`RoomRequest.Ticket` as the ticket's immutable id
from the server's own records, `Renew`, `RoomRef.Ticket`, `RoomState`'s
`Scope` and `InviteURL`, `RoomPerson.Via`, `RoomInvite`, `ICERequest.By`
and `Conn`, `ICEEnder`), the ref `<kind>:<home>:<id>`, the join body with
no ticket, the `room` and `room_invite` events, the message set
(`joined.session`, the mids in `state` and `person`, `tracks`, the seven
`point` kinds, `from: "forwarder"` after `topology`), the states, the
desktop's rules and the forward of the TLS listener with its firewall
rule. These places depend on an answer still open there; each row named
re-reads that plan when it starts, and a change there changes this
plan's records, TypeScript, vectors and `docs/protocol.md` together in
that row, or in a fix PR of this stream before the next row if the row
has merged.

| Open there | What this plan does until it is settled | Waits |
|---|---|---|
| An invitation's recipient: `RoomInvite` names no invitee and `room_invite` goes to every admin principal, so a server of several people shows Ben's invitation to Ana too | `RoomInvite.To` (the invitee's subject, `""` for the operator) and the event sent to that subject's connections only; proposed | PR-1, PR-4 |
| Offered on one server of several people: a `d` room is offered there only once someone is in, so on a server whose identity provider admits Ana and Ben neither can start a huddle | also offered when the server authenticates principals through an identity provider, so others of its principals may join; proposed | PR-4 |
| Liveness: nothing answers a client on a socket that stays open and goes quiet, so the 60 s cutoff cannot be enforced | `ping` and `pong`, 25 s of silence a drop, 10 s for `joined`; proposed | PR-2, PR-13 |
| Encoded bounds: at their own field bounds, `joined` (eight ICE servers), `ice` and a page of 50 `invitees` pass 4 KiB, a ticket of printable ASCII escapes past 6 KiB, and 32 rooms of eight people pass the rooms reply's 64 KiB | the per-frame budgets above (48, 44 and 20 KiB), a ticket of token characters, a `person` id of `[A-Za-z0-9_-]`, a candidate of the ICE grammar's characters, three people per room in the rooms reply and that reply at 256 KiB; proposed | PR-2, PR-4 |
| One sharer at a time: nothing serializes two people pressing Share screen at once | the room service holds the one sharer: it echoes a `state {sharing: true}` it takes to everyone, the sender included, and answers one it refuses with `error {code: "not_allowed"}`; the app adds the screen track only after its own echo, and on the refusal stops the capture; a sharer who drops loses the claim, which a resume asks for again; proposed | PR-9 |
| The rows' order: that plan puts the decision huddles ahead of the two-person audio screens | two-person audio first (PR-13, PR-5, PR-6), then the decision's huddle with its reviewer (PR-8, PR-11) before screen sharing and cameras: the decision rows wait on DR1's, MB0's, RV1's and ID1's rows, none of which the audio screens need, and the Program puts two people's audio first | nothing |

## Events, orderings and invariants

**The call's states** (`RoomClient`, the app's own; the room service has
its own, which the client follows):

| State | Entered on | Next |
|---|---|---|
| `idle` | start, or after `ended` is shown and dismissed | `checking` |
| `checking` | Join pressed: the device check, a track held | `joining` (Join), `idle` (Cancel: the track stopped) |
| `joining` | `POST /api/rooms/join` answered, the socket open, `join` sent | `in` (`joined`), `ended` (a close code, a `409`), `reconnecting` (no `joined` within 10 s) |
| `in` | `joined` | `reconnecting`, `ended` |
| `reconnecting` | the socket dropped, `1001`, or 25 s with no frame | `in` (`joined` after `resume` or a fresh `join`), `ended` (60 s after the last frame, or a refusal) |
| `ended` | `ended`, a close code, 60 s, Leave, `pagehide` | `idle`; every track stopped and every peer connection closed on entry |

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
- `RecordHuddle`: the operation's earlier answer looked for first; then
  the ticket resolved, then Dispatch's `link.huddle` committed with its
  receipt (and any eviction past 200), then nil to the provider; a
  provider that calls again after a crash, or after the run moved to
  another ticket, replays the first answer and nothing is written twice
  (`TestRecordHuddleOnce`).
- `room_invite` events are pending at most 32, one per room, the oldest
  dropped; the provider's call never waits on a client.

**Invariants**, each a named test:

- I1. A plain build draws no huddle control and makes no request to
  `/api/rooms` (`huddle-offer.spec.ts`; `TestRoomsNotOfferedByTheFreeDefault`).
- I2. Joining grants nothing: the reply's token is accepted by no route of
  the server, and the principal's grants are the same after a join
  (`TestJoinGrantsNothing`).
- I3. A room is offered only for work the principal may read and that
  exists here; a provider's answer that is not one state per ref asked is
  refused (`TestRoomsFilteredToReadable`, `TestGuardRoomsOneStatePerRef`).
- I4. A media fault touches no session, run or decision: with the provider
  hanging, failing or panicking in `Room` and `Rooms`, a session's input
  echoes, a stub run's task proceeds and a question can be answered
  (`TestRoomFaultsTouchNoSession`); with the room service held, dropped
  and stopped, the page's terminal still echoes (`huddle-terminal.spec.ts`).
- I5. Every device track the app made is stopped at every end
  (`roomClient.test.ts`, `huddle-audio.spec.ts`, `huddle-states.spec.ts`).
- I6. A `huddle` link is written only through `RecordHuddle`, once per
  room session, and never takes an ordinary link's place
  (`TestHuddleLinkRefusedOnLocalWrite`, `TestRecordHuddleOnce`,
  `TestHuddleLinksApartFromLinks`).
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
- I10. Two people on one server are two, one person's two devices are one,
  and an invitation reaches only its invitee (Who is who's tests;
  `TestInvitationNotSentToOtherPrincipals`).
- I11. A silent room service ends the call: with the socket open and
  nothing delivered, the client reconnects at 25 s and ends at 60 s
  (`roomClient.test.ts` on fake timers; `huddle-states.spec.ts` through the
  fake's `hold`).
- I12. No release build contains a fake (`TestReleaseLeavesOutTheFakes`).

**Stores.** The server stores nothing of a room but the `huddle` link.
Dispatch's kind is text: no schema version, no migration; a replica
running an older build keeps the link as it keeps an unknown reserved
kind brought by a merge (DS1's rule for `decision`). The browser keeps
the two device ids. The fake keeps everything in memory.

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
- Measuring relay needs, bandwidth costs and a group forwarder before any
  unlimited-media promise (CL1's requirement): the provider's. This
  repository's part is `stats`, which gives the room service each
  connection's path, round trip, loss and bitrate by kind; PR-12 (groups)
  waits on that measurement's result, the group topology qualified.
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
- Huddle invitations on a phone that has no Conductor page open: push is
  Program item MB1's.
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
  `TestMediaCheckRoomLister` (a lister that answers late, invalid, out of
  order, or not one state per ref is caught), `TestFakeMediaRooms`,
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
  `TestOlderHostGetsNoPerViewerServers`; `internal/proto`: the host
  messages' new fields at their bounds.
- PR-2, `ext`: `TestRoomVectors` (every vector decodes or is refused with
  its error), `TestRoomFrameBudgets` (each message at its worst case
  through Go's encoder inside its bound; a `join` with a 4,096-byte ticket
  inside 6 KiB), `TestRoomFramesRefuseUnknownFields`,
  `TestRoomCloseCodesNamed`, `TestPointKindPatterns` (the seven),
  `TestStateMidsBounded`, `TestTracksBounded`; `internal/proto`:
  `TestRoomVectorsThroughProto`.
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
  `TestFakeRemovedCannotResume`, `TestFakeAnswersPing`,
  `TestFakeControlSeams`, `TestFakeRoomClosedRecordsOnce`,
  `TestFakeKeepsNoFrames` (its logs searched for the SDPs and tickets);
  `internal/opencore`: `TestReleaseLeavesOutTheFakes`.
- PR-4, `internal/api`: `TestRoomsOfferedOnlyWhenAvailable`,
  `TestOfferedOnlyWhenSomeoneElseCouldJoin` (scope `w`, someone else in,
  an identity provider registered; the workbench token alone and an empty
  `d` room not offered), `TestRoomsNotOfferedByTheFreeDefault`,
  `TestRoomsRefsBounded`, `TestRoomRefParse` (a review's id with `@` and
  `:`, with and without a home), `TestRoomsFilteredToReadable`,
  `TestRoomsReplyBounded`, `TestJoinNeedsReadAccess`, `TestJoinGuarded`
  (late, failing, panicking, invalid), `TestJoinBodyBounded` (at 512 bytes
  and one past; a `ticket` field refused), `TestJoinRateLimited`,
  `TestJoinTicketFromTheServersRecords` (a body naming a ticket refused
  as an unknown field), `TestRoomsRefCarriesTheTicket`,
  `TestJoinRenewPassed`,
  `TestJoinGrantsNothing`, `TestRoomChangedCoalesced` (driven through the
  coalescer's clock seam), `TestRoomCoalescerFullSendsAll`,
  `TestRoomEventDropsASlowClient`, `TestRoomEventsAdminOnly`,
  `TestInvitationReachesTheEventsStream`,
  `TestInvitationNotSentToOtherPrincipals` (two principals' streams and
  the operator's; a reviewer's and a link viewer's get none),
  `TestRoomInvitedBounded`, `TestRecordHuddleOnce` (a retry, a
  retry after the run moved to another ticket, a retry after the receipt
  was pruned), `TestRecordHuddleNoTicketWritesNothing`,
  `TestRoomFaultsTouchNoSession`; `internal/dispatch`:
  `TestHuddleLinkRefusedOnLocalWrite`, `TestAddHuddleReplays`,
  `TestHuddleLinksApartFromLinks` (a ticket at 50 links records its
  huddle and still refuses the 51st ordinary link; the 201st huddle
  removes the oldest in the same operation), `TestHuddleNoteBounded`,
  `TestHuddleLinkKeptOnMerge`.
- PR-8, `internal/api`: `TestRoomsDecisionNeedsItsRecord`,
  `TestJoinDecisionTicketFromRecord`.
- PR-11, `internal/api`: `TestRoomsReviewerOwnDecisionOnly`,
  `TestJoinReviewerRefusedElsewhere`, `TestReviewerStreamRoomEvent`,
  `TestReviewerStreamNoRoomOutsideItsDecision`,
  `TestReviewerAdmissionReadAtEachRequest`.
- PR-14, `internal/reach`: the mapped address told by the app reported
  as the reach's own mapping is (`TestReachTakesTheAppsMapping`).

**Vitest** (`npm --prefix web run typecheck`, `npm --prefix web test`):
`roomProtocol.test.ts` (PR-2: the vectors through the TypeScript
encoder and decoder), `protocol.test.ts` (PR-4: the routes' and events'
types), a `DispatchLinks` case (PR-4: a ticket with ordinary links and
huddle links lists only the ordinary ones); `roomClient.test.ts` (PR-13:
each state's transitions, `renew` every `renewEvery` with the session,
`resume` after a drop with the rotated token, a refused resume
rejoining, connected peers kept on a rejoin, `left` closing that person's
connection before the change is drawn, a `signal` from someone not in the
room dropped, a new `ice` restarting ICE on a relayed connection, ICE
failing twice with the room service quiet and the tries then made on
their own, a quiet socket reconnecting at 25 s and ending at 60 s, no
`joined` within 10 s, every end stopping every track, all on fake timers
and fake sockets); `deviceCheck.test.ts` (PR-5: no device before the
press, Cancel stopping the track, the blocked and missing words, the
speaker list only with `setSinkId`), `huddle.test.ts` (PR-5: the state
table's words, `secureEnough`, `offered` from capabilities and the
reply); `huddlePoint.test.ts` (PR-6: `pointRoute` for each kind in each
room kind, the refusals, Go calling only the router), the phone sheet's
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
  of the operator's workbench token in one room), reconnecting after the
  fake's `drop` and back in with the mute kept and the same
  `RTCPeerConnection` (counted by the init script), "Could not reconnect"
  after `refuse-resume` and `stop`, a quiet socket through the fake's
  `hold` ending at 60 s by the client's own clock, unreachable at the
  fake's `ended {why: unavailable}`, ending soon at the fake's `ending`;
  each driven by a control seam, never a clock of the test's.
- `huddle-invite.spec.ts` (PR-6): Invite lists the fake's people, a page
  at a time; Ben's toast from `room_invite`, and none in a third
  context of the operator; Join from the toast opens the work and its
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
- `huddle-screen.spec.ts` (PR-9: two claims pressed at once, held by the
  fake and released together, give one sharer and stop the other's
  capture; a sharer dropped during the picker loses the claim),
  `huddle-camera.spec.ts` (PR-10) with the fake devices;
  `huddle-group.spec.ts` (PR-12) with three contexts.

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
distribution's address changes, no LAN listen without the rule),
`firewall.test.ts` (PR-14: the TCP rule's fixed arguments and name),
`router-map.test.ts` (PR-14: the mapping made and released). `make desktop-e2e` unchanged.

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

Each PR is one issue and one branch cut from `main`, a draft PR at its
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
(PR-13 to PR-16 were added in review round 1 and sit where they merge).
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
| PR-13 | web: the room client: the call's state machine, perfect negotiation, renewal, liveness, reconnect and rejoin, ICE retries, I8 | `web/app/utils` | PR-4 (`web/app/utils`) | PR-3, PR-7 | ~600 + 750 |
| PR-5 | web: two people talk: the device check, the control on the ticket and run pages, the dock, mute, devices, presence, leave; the second e2e server | `web/app`, `web/e2e` | PR-3, PR-13 | PR-7 | ~600 + 600 |
| PR-6 | web: the huddle's states, invitations, Show them, the phone sheet, the line in the history, media faults | `web/app`, `web/e2e` | PR-5 | PR-7 | ~650 + 750 |
| PR-7 | desktop: the microphone in the desktop app: the permission handler after a press, macOS usage and entitlement, the Windows program rule when elevated | `desktop` | none | PR-1 to PR-6, PR-13, PR-15 | ~300 + 350 |
| PR-14 | desktop, reach: the server inside WSL reachable from outside Windows (#140) | `desktop`, `internal/reach`, `docs/architecture.md` | PR-7 (`desktop`) | PR-8, PR-11 | ~600 + 600 |
| PR-8 | web, api: huddles on the decision page, the decision cards and the Review view | `web/app`, `web/e2e`, `internal/api` | PR-6; #637 and #640 (DR1), #647 and #652 (MB0), #631 (RV1) | PR-14 | ~450 + 500 |
| PR-11 | api, web: a reviewer's huddle on its decision; the features entry | `internal/api`, `web/app`, `web/e2e`, `docs/protocol.md`, `docs/features.md`, `README.md` | PR-8; Program item ID1's open-source rows (the reviewer and its stream), by issue number once filed | PR-14 | ~500 + 600 |
| PR-9 | web, desktop, roomfake: screen sharing | `web/app`, `web/e2e`, `desktop`, `internal/roomfake`, `docs/protocol.md`, `docs/features.md` | PR-11 (`web/app`, `docs`), PR-14 (`desktop`); the hosted plan's answer on one sharer | none | ~650 + 600 |
| PR-10 | web, desktop, roomfake: cameras | `web/app`, `web/e2e`, `desktop`, `internal/roomfake`, `docs/features.md` | PR-9 | none | ~450 + 450 |
| PR-16 | mobile, web: the microphone and the camera in the phone app | the phone app's shell (Program item MB1's), `web/app/utils` | PR-10; MB1's shell on `main`, by issue number once filed | PR-12 | ~200 + 300 |
| PR-12 | web, roomfake: small groups, up to six people | `web/app`, `web/e2e`, `internal/roomfake`, `docs/features.md` | PR-10; the provider's measurement and its forwarded topology qualified | PR-16 | ~450 + 450 |

Should DR1's, MB0's or RV1's rows be late, PR-9 and PR-10 may go before
PR-8 and PR-11, these then merging `main` in, and the order is recorded
on their PRs.

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
and the pending invitations, the hub's subject per connection, `extHost`'s
three doors built, `LinkHuddle`, `MaxHuddleLinks` and `link.huddle` in
Dispatch, `DispatchLinks.vue`'s filter, the protocol rows and types. Kind
`decision` answers no room until PR-8 (no record to read). Tests: PR-4's
above.

**PR-13** (`web/app/utils`): `roomClient.ts`, the whole of The call
above, with nothing drawn. Tests: `roomClient.test.ts`.

**PR-5** (`web/app`, `web/e2e`): `huddle.ts`, `useHuddle.ts`,
`HuddleControl`, `HuddleDeviceCheck`, `HuddleDock`, `HuddlePeople`,
`HuddleIndicator`, `HuddleAnnouncer`; the control in
`DispatchTicketPage.vue`'s and the run page's headers; the layout's dock;
`useAttention.ts`'s two events; the bridge call to `mediaPress` where it
exists; `web/e2e/huddle.ts` (the second server and its people);
`huddle-offer.spec.ts`, `huddle-audio.spec.ts`. Tests: the vitest files
and specs named for PR-5.

**PR-6** (`web/app`, `web/e2e`): every state of the table not in PR-5 and
not a later row's (the reconnect's, removal's and liveness's screens on
PR-13's client), `stats` and the relayed mark, `HuddleSheet`,
`HuddleInvite`, `HuddlePoint` and `pointRoute`, the M key and its `?`
line, the `huddle` link's line in `DispatchHistory.vue` (an icon, the
note, the time; no URL); the specs named for PR-6.

**PR-7** (`desktop`): `permissionAllowed` with details and the press
record, `setPermissionCheckHandler`, `mediaPress` and `mediaAccess` in
`preload.ts` and `ipc`, `mac.extendInfo`'s microphone string, the
audio-input entitlement, `installer.nsh`'s program rule and its removal
in its elevated branch. Tests: `window.test.ts`, `packaging.test.ts`.

**PR-14** (`desktop`, `internal/reach`, `docs`): issue #140's work: the
TCP forwarder of the TLS listener, the router's mapping from Windows, the
forwarded listener's firewall rule, the mapped address told to the
server's reach, `docs/architecture.md`'s lines. Tests:
`tcp-forwarder.test.ts`, `router-map.test.ts`, the reach's Go race test;
by hand, a phone on a mobile network.

**PR-8** (`web/app`, `web/e2e`, `internal/api`): `HuddleControl` on
DR1's `DecisionCard.vue` and in `DispatchDecisions.vue`, on MB0's
`InboxItem.vue`, and on RV1's Review view keyed by its candidate; the
dock's padding on `/decisions` so it never covers the answer buttons;
`/api/rooms` and the join reading DR1's record for kind `decision` (it
exists; its ticket). Tests: `TestRoomsDecisionNeedsItsRecord`,
`TestJoinDecisionTicketFromRecord`, `huddle-decision.spec.ts`.

**PR-11** (`internal/api`, `web/app`, `web/e2e`, `docs`, `README.md`): a
reviewer admitted to `/api/rooms` and the join for the decision it is
admitted to, and refused everywhere else; `offered` for a decision's room
whose admission admits a reviewer; the reviewer's stream's `room` event
for that decision only, checked at each send; the admission read at each
request and shared by the origin's tabs, so one renewed in another tab
reaches the call's next renewal; the app asking `/api/rooms` without the
capabilities read for such a principal; Access ending and the
access-ended words; the README's Extensions line; `docs/features.md`'s
entry (what a person sees, the observed acceptance's line left for the
owner). Tests: PR-11's Go tests and `huddle-reviewer.spec.ts`.

**PR-9** (`web/app`, `web/e2e`, `desktop`, `internal/roomfake`, `docs`):
Share screen, `HuddleScreen` (the sharer's and the viewers' states, the
panel, fit or actual size), `screenMid` sent in `state` before the offer
that carries the screen and the others' screen shown by `person`'s
`screenMid`, the one-sharer claim as the hosted plan settles it (and
`docs/protocol.md` with the vectors if it changes), the fake's `screen`
kind (a video section's `mid` checked against the sender's `screenMid`)
and its one sharer, the desktop's
display handler and picker and `mediaAccess('screen')`, the features
entry extended. Tests: `huddle-screen.spec.ts`, `picker.test.ts`,
`window.test.ts`'s display case.

**PR-10** (`web/app`, `web/e2e`, `desktop`, `internal/roomfake`, `docs`):
Camera, `HuddleCameras`, `cameraMid` as `screenMid`, the fake's `camera`
kind, the camera's usage
string and entitlement, `video` after a camera press, the features entry
extended. Tests: `huddle-camera.spec.ts`, `window.test.ts`'s and
`packaging.test.ts`'s camera cases.

**PR-12** (`web/app`, `web/e2e`, `internal/roomfake`, `docs`): up to six
people, `caps: ["forwarded"]` in `join`, the `topology` switch (a peer
connection to the forwarder, the direct ones kept until the forwarder's
tracks arrive or 10 s pass, the two told apart by `from`), the client the
polite peer with the forwarder and the maker of the first offer, each
forwarded track shown as the person and kind its `mid` maps to in the
latest `tracks` and not shown when its `mid` is in none, Media failed, the group's dock and phone layout,
the fake's `maxPeople` 6; the features entry extended. Tests:
`huddle-group.spec.ts`, the room client's forwarded cases (a `forwarder`
signal refused before `topology` and taken after it; the switch keeping
the direct connection until the tracks arrive; a `mid` in no `tracks`
not shown).

**PR-16** (the phone app's shell, `web/app/utils`): the phone app's
`NSMicrophoneUsageDescription` and `NSCameraUsageDescription`; its web
view's media capture granted for the app's own origin only after a press
the page reports through an optional `mediaPress(kind)` of the app's
bridge, as the desktop handler's, every other request refused; the page
joins nothing it cannot hear on an older shell without that member.
Tests: the shell's grant test (refused without a press, for another
origin and past 10 s; granted after a press), `bridge.test.ts` (the
member absent).

## Owner decisions

Dated 2026-11-06. Each is a default the owner can overturn with a dated
row in the Program's status log (the Program, section 3.1's convention).
The Program leaves this plan no open decision of its own: O7 was closed
by D21, which this plan follows (two people's audio first; the group
topology is the provider's to measure and qualify).

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
   `room_invite` event** sent to the invitee's connections only, beside
   `Host.Notify`, which reaches only the notification sinks (the hosted
   plan has the door, the record `RoomInvite` and the event; the
   recipient `To` is proposed there).
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
    resolved against the room's own work (the hosted plan's list since its
    second round; the resolution is this plan's).
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
second person and the observation; until the group topology is
qualified, Done covers two people, and PR-12 follows.

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
