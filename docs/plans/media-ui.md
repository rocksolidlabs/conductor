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

It was written against `main` at `76d3e655` after reading the Media seam
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
  `Valid`; at most `MaxICEServers`); `GET /api/ice` never does. `Room` has
  **no caller**, and `RoomJoin` has **no `Valid`** (`ext/ext.go`: "Room has
  no caller until Program item CL1"). `RoomRequest` is `{Kind, ID, By}`;
  `RoomJoin` is `{URL, Token, ExpiresAt}`. The free default
  (`ext/local.Media`) adds no server and answers no room. `FakeMedia` and
  the `Media` check exist.
- C9: `GET /api/capabilities` (admin only) lists each registered provider
  by kind, name and state; `has(caps, 'media')` in the app.
- Dispatch's ticket page (`/dispatch/<project>/<number>`,
  `DispatchTicketPage.vue`), DS2's candidates on it, and the run page
  (`/runs/<run>`).
- The events stream `GET /api/events` with its `snapshot` on reconnect,
  read by `useAttention.ts`.
- `github.com/pion/webrtc/v4` and `github.com/coder/websocket` in
  `go.mod` already: nothing here adds a Go module.
- The desktop app forwards UDP for the terminals' ICE from Windows into
  WSL (`desktop/src/udp-forwarder.ts`) and its installer adds that port's
  firewall rule; its permission handler allows the clipboard and
  fullscreen only.

**Not on `main`, and which rows wait for it.**

- DR1's decision records and their card (`DecisionCard.vue`), the ticket
  page's Decisions section (`DispatchDecisions.vue`): issues #634 to #641;
  PR-8 waits on #640 (DR1's web row) and #637 (its routes).
- MB0's decision page `/decisions` (`InboxItem.vue`): #647 and #652;
  PR-8 waits on both.
- RV1's Review view (`?view=review&candidate=…`): #631; PR-8 waits on it.
- Program item ID1's open-source rows: a principal admitted to one
  decision (the **decide grant**) and the decision stream it reads; no
  issue exists in this repository yet. PR-11 waits on them; every earlier
  row serves the admin grant alone.
- A provider: outside this repository. No row here waits on one; the
  item's acceptance (below) is observed with one, and PR-12 (groups)
  waits on its group topology being qualified.

**Who owns what this item shares.**

| Shared piece | Owner | The other side |
|---|---|---|
| The Media seam (`ext.MediaRoomProvider`, `RoomLister`, the three `Host` doors, their records, bounds and waits) | this plan | a provider implements and calls them at a tag holding PR-1 and PR-4 |
| The room protocol `conductor-room/1`: its records in `ext/room.go`, its words in `docs/protocol.md`, its TypeScript and its vectors | this plan (PR-2) | a provider's room service speaks it at the tag that defines it and passes the vectors |
| The server's room routes and events, the `huddle` link kind | this plan (PR-4) | Dispatch's store (DS1) keeps the kind; a sync provider (Program item CS1) replicates it as every link |
| The pages a huddle sits on | DS1 (ticket page), the run page, DR1 (`docs/plans/decisions.md`: the card), MB0 (`docs/plans/decision-inbox.md`: `/decisions`), RV1 (`docs/plans/review-repair.md`: the Review view) | this plan adds the huddle control to each, after the row of theirs it builds on (PR-5, PR-8) |
| The decide grant and the decision stream | Program item ID1's open-source rows | PR-11 admits that grant to a decision's room and adds the `room` event to that stream |
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
   room and implements no `RoomLister`, so `offered` is false and the
   screens draw nothing; the ICE sites are untouched.
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
    for the shell; the protocol in its three places together (Owner
    decisions, 1); every new frame, message and route with a size limit
    and a test.
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
  candidate or a file at a commit; the other's page offers "Ana is
  pointing at finding 3 · Go", which their own server opens or refuses
  ("You can't open this on your server").
- **Invitations, removal, reconnect.** Invite lists who the room service
  says may join; an invitee's open Conductor shows "Ana invites you to a
  huddle on PRJ-12 · Join". A dropped network shows "Reconnecting…" and
  comes back into the same huddle with the mute state kept. A person
  removed, or whose access to the work ended, is told so in plain words,
  and their devices are released at once.
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
  host (`127.0.0.1`, `[::1]`, `localhost`: the fake's); `Token` one line of
  1 to `MaxRoomToken` (4,096) bytes of printable ASCII; `ExpiresAt` set,
  after `now` and at most `MaxRoomTicket` (10 minutes) past it. `Valid()`
  is `ValidAt(time.Now())`; the guard calls `ValidAt` with its own clock
  so the tests drive it. The sentence in `ext/ext.go` saying `RoomJoin`
  has no `Valid` goes.
- **`RoomRequest` says where the work is.** `Ticket string` (the key of
  the ticket the work belongs to, `""` for none) and `Home string` (for
  another machine's record shown here, the device id it names; `""` for
  this server's own work), each at most 64 bytes of printable ASCII;
  `Kind` one of `RoomKinds` (`ticket`, `decision`, `review`, `run`);
  `ID` 1 to `MaxRoomWorkID` (200) bytes of printable ASCII without
  spaces (a review's id is its candidate, `<run>/<task>/<n>@<commit>`).
  `Valid()`. The server fills `Ticket` from its own records (Owner
  decisions, 8); the provider decides what the room is.
- **Presence: `RoomLister`, an optional interface** a provider implements
  beside `MediaRoomProvider`:
  `Rooms(ctx, refs []RoomRef) (RoomsAnswer, error)`, safe for concurrent
  use, answered from what the provider holds. `RoomRef {Kind, ID, Home}`;
  at most `MaxRoomRefs` (32) per call. `RoomsAnswer {Available bool,
  Rooms []RoomState}`; `RoomState {Kind, ID, Home, Room, Since time.Time,
  People []RoomPerson, More int}`; `RoomPerson {Subject (at most
  MaxSubject), Name (1 to MaxPrincipalName characters, one line), Muted,
  Sharing, Camera bool}`; at most `MaxRoomPeople` (8) people, `More` the
  count past them. Each record has `Valid`. A provider that does not
  implement it, `Available` false, an error, a late, panicking or invalid
  answer: **no huddle offered**.
- **Three `Host` doors**, each never blocking the provider:
  - `RoomChanged(ref RoomRef)`: a room this server shows changed. The
    server coalesces calls per ref, at most one event a second each, and
    sends `room {kind, id, home}` (PR-4).
  - `RoomInvited(inv RoomInvite)`: an invitation for this server's person.
    `RoomInvite {Kind, ID, Home, Room, By RoomPerson (subject and name),
    Until time.Time}` (at most `MaxRoomInvite` (10 minutes) ahead),
    `Valid`. The server sends `room_invite {…}` (PR-4). This door is new
    beside the hosted plan's words: `Host.Notify` hands a notice to the
    notification sinks only (webhooks, a push provider), never to the
    app's pages, so a toast cannot come from it (Waiting on the hosted
    plan, below). A provider calls both: `Notify` for push and webhooks,
    `RoomInvited` for the open app.
  - `RecordHuddle(ctx, HuddleRecord) error`: a room session ended.
    `HuddleRecord {Kind, ID, Home, Room, Since, Ended time.Time, People
    []RoomPerson (at most 8, subject and name), More int}`, `Valid` (Ended
    after Since and at most 24 hours later). The server writes one
    Dispatch link (PR-4); idempotent by `Room` and `Since`.
  PR-1 declares the three and the server's `extHost` answers them as
  stubs (the two events dropped with a debug line, `RecordHuddle`
  `ErrUnsupported`), so `main` stays green; PR-4 builds them, as C1's
  PR-1 declared `Host` and its PR-4 built it (`docs/plans/ext.md`).
- **The guard** (`internal/api/extensions.go`, `guardMedia`): `Room`'s
  answer must pass `ValidAt`, else it is ok false; a `Rooms` method on
  the guarded value, through `extCall` with `ICEWait`, answering
  `RoomsAnswer{}` (not available) for a provider without `RoomLister`, and
  keeping only the rooms whose ref was asked for, so a provider cannot
  show a room for work this server did not ask about.
- **`ext/exttest`**: `FakeMedia` implements `RoomLister` with `SetRooms`
  and `RoomsRequests()`; `FakeHost` records `RoomChanged`, `RoomInvited`
  and `RecordHuddle` (with a scripted error); the `Media` check calls
  `Room` and wants `ValidAt` to pass whenever ok is true, and, for a
  provider that implements `RoomLister`, calls `Rooms` within `ICEWait`
  from `Goroutines` goroutines, with the network open and blocked, and
  wants a valid answer with no room outside the refs.

Constants (`ext/media.go`): `RoomProtocol`, `RoomKinds`, `MaxRoomURL`
512, `MaxRoomToken` 4096, `MaxRoomTicket` 10 min, `MaxRoomID` 64,
`MaxRoomWorkID` 200, `MaxRoomHome` 64, `MaxRoomRefs` 32, `MaxRoomPeople`
8, `MaxRoomInvite` 10 min, `MaxHuddleSpan` 24 h.

### `ext/room.go`: the room protocol `conductor-room/1` (PR-2)

The protocol between the app and a room URL. It lives in `ext`, standard
library only, so a provider's room service in another module uses the
same records (it cannot import `internal/proto`), and its TypeScript in
`web/app/utils/roomProtocol.ts`; its words are a new section **Rooms** of
`docs/protocol.md` (Owner decisions, 1). Every message is one JSON text
frame `{"t": "<type>", …}` decoded with `DisallowUnknownFields`: at most
`MaxRoomFrame` (4 KiB), or `MaxRoomSignalFrame` (40 KiB) for a `signal`
carrying an SDP, whose `sdp` is at most `MaxRoomSDP` (32 KiB); a
candidate at most `MaxRoomCandidate` (1 KiB).
`DecodeAppFrame(b []byte) (AppFrame, error)` and
`DecodeServiceFrame(b []byte) (ServiceFrame, error)` check the size
before decoding and each field's bound after.

App to room service:

| Message | Fields and bounds |
|---|---|
| `join` | `ticket` (the `RoomJoin.Token`, at most 4,096 bytes); the first frame, within 10 s |
| `resume` | `token` (from `joined.resume`, at most 128 bytes); in place of `join`, within 60 s of a drop |
| `renew` | `ticket`; every `joined.renewEvery` seconds |
| `signal` | `to` (a person's subject), `sdp?` `{type: offer \| answer, sdp}`, `candidate?` `{candidate, sdpMid?, sdpMLineIndex?}`; exactly one of the two |
| `state` | `muted`, `sharing`, `camera` |
| `invitees` | none: ask whom one may invite |
| `invite` | `person` |
| `remove` | `person` |
| `point` | `kind` (`PointKinds`: `ticket`, `decision`, `run`, `task`, `candidate`, `finding`, `file`), `id` (at most 200 bytes, printable ASCII; a file is `<path>@<commit>`): a reference, never text |
| `stats` | `path` (`direct`, `relay`, `forwarded`), `rttMs`, `lossPct`, `kbpsOut`, `kbpsIn` (each 0 to 1,000,000), `kinds` (subset of `audio`, `screen`, `camera`); at most every 30 s |
| `leave` | none |

Room service to app:

| Message | Fields and bounds |
|---|---|
| `joined` | `you` `{person, name}`, `room`, `people` (at most 8 of `person`), `iceServers` (at most `MaxICEServers`, each `ICEServer.Valid`), `resume`, `topology` (`direct` \| `forwarded`), `kinds`, `maxPeople` (2 to 8), `endsAt`, `renewEvery` (30 to 300) |
| `person` | `person`, `name`, `muted`, `sharing`, `camera`, `state` (`in` \| `reconnecting`) |
| `left` | `person`, `why` |
| `signal` | `from`, `sdp?`, `candidate?` |
| `invitees` | `people`: at most 50 of `{person, name}` |
| `invited` | `by` `{person, name}`, `room`, `kind`, `id`, `home` |
| `point` | `from`, `kind`, `id` |
| `ice` | `iceServers` (fresh relay credentials) |
| `ending` | `at` |
| `ended` | `why` (`EndReasons`: `left`, `removed`, `access`, `unconfirmed`, `elsewhere`, `time`, `unavailable`), `by?` `{person, name}` |
| `error` | `code` (`rate`, `media_kind`, `not_allowed`, `no_room`, `bad_frame`), `message` (one line, at most 200 bytes) |

Close codes, constants of `ext`: `4400` bad frame or no `join`, `4401`
bad ticket, `4403` not allowed (the close reason `not_allowed`,
`removed` or `not_entitled`), `4409` room full, `4410` ended (after an
`ended`), `4429` too many, `4503` unavailable, `1001` service stopping.
A `1001` close or a dropped socket is **not** an end: the app resumes
(below).

**Vectors**: `ext/testdata/room/app.json` and `service.json`, one valid
frame of each message at its largest and, for each bound, one frame one
byte (or one item) past it, with the error it must get. `ext`'s
`TestRoomVectors` and `roomProtocol.test.ts` read the same files, so the
Go records and the TypeScript cannot drift; a provider's room service
runs them too.

### `internal/roomfake`: the fake room service and the fake provider (PR-3)

So that every screen is built and tested here with no provider of the
real kind, `internal/roomfake` holds:

- **`Service`**, an `http.Handler` that speaks `conductor-room/1` on a
  loopback listener with `coder/websocket` and the `ext` records' decoders:
  tickets it mints itself (`fake1.<claims as base64url>.<HMAC-SHA256
  under a key made at start>`, claims `{room, kind, id, home, subject,
  name, exp}`, 60 s of life, one use); presence, renewal, resume within
  60 s, `invitees` from a list the test sets, `invite`, `remove`,
  `point`, `stats` (counted, dropped), the end reasons, the close codes,
  `maxPeople` 2 (a setting, for PR-12), `kinds` audio (screen from PR-9,
  camera from PR-10; an SDP's `m=` lines read by a small scanner, a `video`
  section refused while the kind is off). `iceServers` is empty: no
  relay, the two browsers on one machine connect on host candidates. It
  never stores or logs a frame's contents.
- **Control seams for tests**, on the same listener under `/control/`,
  each behind a random control token: `drop` (close one person's socket
  with no `ended`, so the app resumes), `refuse-resume`, `end?why=`,
  `full`, `ending?in=`, `hold` and `release` (hold every frame to one
  person until released), `stop` and `start` (close and reopen the
  listener), and `GET /control/state` (people, their states, frames
  counted by type). Tests wait on these, never on a clock.
- **`Provider`** (`fake-media`): `MediaRoomProvider`, `RoomLister` and
  `ext.Starter`. `Start` opens the listener on `127.0.0.1:0` and writes
  `<DataDir>/room.json` (`{url, control, controlToken}`, through a `.new`
  file renamed over it); `Room` mints a ticket for the principal and
  answers `{URL: ws://127.0.0.1:<port>/rooms, Token, ExpiresAt, Room,
  Protocol}`; `Rooms` answers from the service's state; each presence
  change calls `Host.RoomChanged`, each `invite` `Host.RoomInvited`, and a
  room left empty (closed after a grace the test sets, default 1 s)
  `Host.RecordHuddle` once. `ICEServers` adds none.
- **The e2e server binary**, `internal/roomfake/e2e/main_test.go`: its
  `TestMain` registers `roomfake.Provider` and calls `app.Main()` when
  `CONDUCTOR_ROOMFAKE_SERVE=1`, else runs the package's tests.
  `make build-roomfake` builds it with `go test -c -o bin/conductor-roomfake
  ./internal/roomfake/e2e`; `make test-e2e` depends on it. No release
  target, `make build` or `cmd/conductor` ever contains the fake (Owner
  decisions, 2).

### `internal/api` and `internal/dispatch`: the routes, the events, the huddle's line (PR-4)

`internal/api/rooms.go` (new):

- **`GET /api/rooms?ref=<kind>:<id>[ <home>]`**, 1 to 32 `ref`
  parameters, the home after one space (`%20`; ids hold no space, and a
  review's id holds `@`, so `@` cannot separate it) → `{offered bool,
  rooms: [RoomState]}`, at most 64 KiB. `offered` is true only when the
  registered provider implements `RoomLister` and answered `Available`
  true through the guard. Each ref must name work the principal may read
  and that exists here (a ticket's key; a run, live or recorded; a
  candidate of DS2's form whose run is known; a decision once DR1's
  records are on `main`, PR-8); a ref that does not is dropped from the
  question with no word, so the route tells nobody what exists.
  `400 invalid_request` for a malformed or 33rd ref.
- **`POST /api/rooms/join {kind, id, home?}`** (body at most 512 bytes,
  `DisallowUnknownFields`) → `200 {url, token, expiresAt, room, protocol}`
  (at most 6 KiB) or `409 {error: {code: "no_room"}}` when the provider
  answers ok false, fails, is late, panics or answers a join `ValidAt`
  refuses. The server builds the `RoomRequest`: `By` the principal,
  `Ticket` from its own records (the ticket's own key; a decision's
  ticket; the ticket whose links hold a run's or a candidate's chain;
  `""` for none), never from the client. Renewals are the same call.
  At most 30 a minute per principal (`429 rate_limited`). No receipt is
  written: joining is not an operation on the work.
- **Who may ask** (both routes): the admin grant, for every kind; never
  the host grant, a share link's viewer, an agent's token or an unknown
  token (`401`, as other admin routes). PR-11 admits the decide grant for
  a decision that admits it. A `home` ref needs a remote record of that
  device and id held here (Program item CS1's); until such records exist
  on `main`, a ref with a home is dropped.
- **The events**, on `GET /api/events` to admin principals: `room {kind,
  id, home}` (coalesced per ref, at most one a second; the app reads
  `GET /api/rooms` again) and `room_invite {kind, id, home, room, by:
  {subject, name}, until}`. The `snapshot` on a reconnect is unchanged:
  the app reads the rooms of the pages it shows again on it, as it does
  everything else.
- **`extHost`'s doors**: `RoomChanged` and `RoomInvited` put the event on
  a queue of 64 that drops its oldest, never blocking the provider;
  `RecordHuddle` resolves the work's ticket as the join does, and, with
  none, answers nil and writes nothing; with one, it calls Dispatch's new
  typed operation.

`internal/dispatch`: a link kind `huddle` (`LinkHuddle`), reserved as
`LinkDecision` is: refused on `link.add` (`RefusalInvalid`, "the kind
huddle is written by a huddle's end only"), kept when a merge brings one.
A new operation `link.huddle` (`Store.AddHuddle(ctx, key, HuddleLink, op
Op)`) writes it with a receipt, its actor `provider:<name>` (at most 49
bytes, inside `MaxActor`), its op id `h-` and 30 characters of base32 of
the SHA-256 of the room and `since`, so a second call for the same room
session replays the first receipt and writes nothing. The link's `ref` is
`<room>@<since as RFC 3339>`; its `note` is "Huddle with Ana and Ben · 17
min" (names Markdown-escaped, "and 3 more" past four, the duration in
whole minutes; at most 200 bytes). The kind is text in the store, so no
schema version changes and no migration runs.

Docs: `docs/protocol.md`'s HTTP API rows for both routes, the two events
and the link kind, with their bounds; `web/app/utils/protocol.ts` types
and `protocol.test.ts` cases for them.

### `web/app`: the screens (PR-5, PR-6, then PR-8 to PR-12)

Components come from Nuxt UI with the brand palette (`app.config.ts`:
forest primary, terracotta only as the restrained accent; operational
status in its own scales); the junction mark appears nowhere in a huddle
and is never a status light (`docs/design/brand.md`). Speaking, muted,
sharing and relayed are each an icon and words, never colour alone; every
state change is announced in an ARIA live region (`HuddleAnnouncer.vue`).

**Files.**

- `utils/roomProtocol.ts` (PR-2): the records, `encodeAppFrame`,
  `decodeServiceFrame` with the bounds.
- `utils/roomClient.ts` (PR-5): `RoomClient`, the protocol's client and
  the call's state machine, with every outside thing injected (the
  WebSocket and `RTCPeerConnection` constructors, a clock, the server's
  join call), so `roomClient.test.ts` drives it whole with fakes and fake
  timers.
- `utils/huddle.ts` (PR-5): the words of every state and end, `offered`,
  `secureEnough()` (`window.isSecureContext`: the desktop app's
  `http://127.0.0.1` origin is a secure context, a LAN `http://` address
  is not), `pointRoute(kind, id)` (PR-6).
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
ticket's key) and the run page's header (`kind run`) from PR-5; the
Review view (`kind review`, the candidate shown), each decision's card on
`/decisions` and in the ticket page's Decisions section (`kind decision`,
`dr-n`) from PR-8. A remote record shown "on <device>" gets a control
only when the provider reports its room open (`RoomState` present). A
page registers its refs with `useHuddle().show(refs)`; the composable
reads `GET /api/rooms` once for them (one read in flight, one queued),
again on a `room` event for one of them, on the stream's `snapshot`, and
when the page comes back into view.

**Whether anything is drawn.** For an admin principal the app first
reads the capabilities it already reads (`useCapabilities`): with no ready
`media` provider it never asks `GET /api/rooms` and draws nothing. With
one, `offered` decides. A principal that cannot read the capabilities
(the decide grant, PR-11) asks `GET /api/rooms` and `offered` decides.

**What each screen shows, in each state.**

| State | What the person sees |
|---|---|
| Not offered (plain build; provider starting, unreachable or lapsed; `offered` false) | nothing: no control, no empty space, no message |
| Offered, page not a secure context | the control disabled, its tooltip "Huddles need a secure address (https)" |
| Offered, room empty | **Huddle**, a microphone icon |
| Offered, people in | "Ana is in a huddle · Join", up to three avatars and "+n"; muted and sharing marks |
| Device check (after Join, before any media) | "Conductor will ask your browser for the microphone." Then the browser's prompt; the microphone list, the speaker list where `setSinkId` exists, a level meter, Join muted, Join and Cancel. Cancel stops the track at once |
| Microphone blocked | "The microphone is blocked for this page. Allow it in the browser's site settings, then try again." In the desktop app on macOS: "Allow the microphone for Conductor in System Settings, Privacy & Security." Try again |
| No microphone | "No microphone was found." Join to listen only, or Cancel |
| Joining | "Joining…" then "Connecting audio…"; "relayed" beside a person whose selected candidate pair is a relay (from `getStats`, PR-6) |
| In the huddle | the people (name, speaking, muted, sharing), the elapsed time; Mute (the M key while the focus is in no text field, terminal or editor, listed in the `?` list), the device menu, Invite, Show them, Leave; Share screen from PR-9, Camera from PR-10. A docked strip at the page's foot that never covers a decision's answer buttons (the page gains the strip's height as bottom padding) |
| Invite | the people the room service lists (`invitees`); "Invited Ben" for 10 minutes |
| Invitation received | a toast from `room_invite`: "Ana invites you to a huddle on PRJ-12 · Join · Dismiss"; Join opens the work's page and its device check |
| Show them | sent: "Pointed Ben at finding 3"; received: "Ana is pointing at finding 3 · Go"; Go opens it on this server, or "You can't open this on your server" |
| Room full | "This huddle is full." Nothing else changes |
| Removed | "Ana removed you from the huddle." Media stopped, devices released |
| Access ended | "Your access to this huddle has ended." (`access`, `unconfirmed`); with a pass that ended, the decision page's own words (PR-11) |
| Elsewhere | "You joined this huddle from another window." |
| Reconnecting | "Reconnecting…" with the mute state kept; back within 60 s, or "Could not reconnect · Rejoin" |
| Service unreachable | "Reconnecting…", then at 60 s without the room service, or at `ended {why: unavailable}`: "The huddle ended: the huddle service is unreachable." |
| Ending soon | "This huddle ends in 5 minutes." (`ending`), then `time`: "The huddle reached its time limit." |
| Screen sharing (PR-9) | Share screen opens the browser's picker (the desktop app's own); "You are sharing your screen · Stop"; the others see it in a resizable panel beside the work, fit or actual size; "Ben is sharing; ask them to stop first"; not offered on a phone |
| Cameras (PR-10) | Camera off by default, asked for on its press (blocked and missing as the microphone's); small tiles beside the people; never on a phone's collapsed sheet |
| Phone (below `sm`, 640 px) | a bottom sheet above the bottom bar: collapsed, the names, Mute and Leave; expanded, the people, the devices, Invite; a shared screen full width above the sheet; the page scrolls above it; audio continues with the sheet collapsed |

**Deliberate device access.** `getUserMedia` is called only from a press
(Join, Try again, Camera, Share screen through `getDisplayMedia`), with
the browser's own echo cancellation, noise suppression and gain control
left on. Every track the app made is stopped at Leave, at every end, at
Cancel and at `pagehide` (which also sends `leave`), and the browser's
indicator goes off with it. The chosen microphone and speaker ids are
kept for this browser in `localStorage` (`conductor.huddle.devices`, every
read and write in `try`); nothing else of a call is stored anywhere.

**The call** (`RoomClient`). After `joined`, in the `direct` topology one
`RTCPeerConnection` per other person, with `joined.iceServers` (updated
by `ice` through `setConfiguration`), an audio transceiver, trickled
candidates through `signal`, and perfect negotiation (the polite side the
one whose subject sorts higher). Mute sets the track's `enabled` false
and sends `state`; nothing is renegotiated. An ICE failure restarts ICE
once; a second failure closes that connection and negotiates anew when
the person is next seen `in`. Every `renewEvery` the client asks
`POST /api/rooms/join` again and sends `renew`; a refused renewal is
tried at the next tick, and the room service decides when to end
(`unconfirmed`). A dropped socket or a `1001` close starts
**reconnecting**: a new socket with `resume` (backoff 0.5 s doubling to
8 s), a refused resume a fresh ticket and `join`; after a rejoin every
peer connection still `connected` to a person who is `in` again is kept,
and only someone new, or one whose ICE failed, is negotiated anew. 60 s
without a room connection ends the call with the unreachable words. Each
end stops every track and closes every peer connection. `stats` is sent
every 30 s from `getStats` (PR-6). The terminal's transport
(`utils/transport/webrtc.ts`) and the room client share no object, no
socket and no timer.

### `desktop`: the microphone, then the screen and the camera, in the desktop app (PR-7, PR-9, PR-10)

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
- **Windows**: `build/installer.nsh` adds an inbound program rule,
  "Conductor huddles", for the app's own executable (UDP), as it already
  adds the ICE port's rule, and removes it at uninstall, so a call's
  WebRTC sockets never raise the firewall's prompt. An installed app gets
  it at its next update, which runs the installer.

### Windows with the server inside WSL: nothing to forward

A person configures no networking, ever (`AGENTS.md`). Every media path
of a huddle starts in the person's browser, which on Windows is the
desktop app's window (or a browser) running on Windows, not inside WSL:

| Path | From, to | Crosses the Hyper-V NAT? | What the desktop app does |
|---|---|---|---|
| The pages, `GET /api/rooms`, `POST /api/rooms/join`, the events | the window to the server in WSL | yes, as every page request today, over the connection the app already makes | nothing new |
| The room service's WebSocket | the window to the room service | no: outbound from Windows | nothing |
| Direct media (UDP) | the window to the other person's browser | no: Windows' own interfaces gather the candidates | nothing to forward; the installer's program rule (PR-7) |
| A relay | the window to the relay the room service names | no: outbound from Windows | nothing |
| Forwarded media (groups, PR-12) | the window to the room service's forwarder | no: outbound | nothing |
| The provider's own traffic | the server in WSL to its service | outbound through the NAT, as every outbound connection today | nothing |
| The e2e fake (tests and the by-hand check) | the window to `127.0.0.1` in WSL | yes, TCP to loopback, which WSL forwards to the distribution on its own | nothing |

So no huddle path needs a forward the app does not make today, and none
is planned. A browser run inside the distribution itself (WSLg) gathers
its candidates behind the NAT; its direct path may fail and it then uses
the relay, outbound, with nothing to configure. Should the by-hand check
find a path that does cross the NAT, the forward is built in `desktop/`
as a fix PR of this stream, never written as a step for a person. The
forwards owed for other reasons (#140: the TLS listener and the router's
mapping) are not this item's, and nothing here waits for them.

### Docs

`docs/protocol.md` (the Rooms section, PR-2; the routes, events and link
kind, PR-4; the decide grant's rows, PR-11); `docs/repository-map.md`'s
rows for `internal/roomfake` and `ext/room.go` (PR-3); the Extensions
section of `README.md` naming what a media provider adds (PR-11);
`docs/features.md` the item's entry (PR-11, extended by PR-12).

### Waiting on the hosted plan

The provider's plan (Program item CL1's other plan file, kept with the
provider and not in this repository) is in its first review round. These
places follow its final text, and each row that holds one re-reads it
when it starts:

- **The protocol's message set, fields, bounds, close codes and end
  reasons** (PR-2): as above until that plan's text is final; a change
  there changes `ext/room.go`, the TypeScript, the vectors and
  `docs/protocol.md` together in PR-2, or in a fix PR before PR-5 if PR-2
  has merged.
- **Invitations reaching the open app** (PR-1, PR-4, PR-6): this plan adds
  `Host.RoomInvited` and the `room_invite` event, since `Host.Notify`
  reaches only notification sinks; proposed to that plan.
- **The ref's home separator and who supplies the join's ticket** (PR-4):
  a space before the home, and the server's own records for `Ticket`;
  proposed to that plan.
- **`point`'s kinds** (PR-2, PR-6): the list above; proposed to that plan.
- **Telling a screen track from a camera track** (PR-9, PR-10): the app
  names its transceivers' `mid`s in `state` (`screenMid`, `cameraMid`,
  each at most 16 bytes) rather than editing the SDP, which browsers do
  not mark; proposed to that plan, and PR-9 waits for its answer.
- **The `forwarded` topology's signaling** (PR-12): whom a `signal`
  names, and how a forwarded track is matched to its person, are not yet
  said; PR-12 waits for them.
- **The by-hand check's latency** (Tests per surface): that plan asks for
  a spec comparing a terminal's echo time with and without a call, p95
  within 50 ms; this plan proves the separation by mechanism instead
  (`AGENTS.md`, Tests that do not flake) and leaves the latency to the
  by-hand check; proposed to that plan.

## Events, orderings and invariants

**The call's states** (`RoomClient`, the app's own; the room service has
its own, which the client follows):

| State | Entered on | Next |
|---|---|---|
| `idle` | start, or after `ended` is shown and dismissed | `checking` |
| `checking` | Join pressed: the device check, a track held | `joining` (Join), `idle` (Cancel: the track stopped) |
| `joining` | `POST /api/rooms/join` answered, the socket open, `join` sent | `in` (`joined`), `ended` (a close code, a `409`) |
| `in` | `joined` | `reconnecting`, `ended` |
| `reconnecting` | the socket dropped or `1001` | `in` (`joined` after `resume` or a fresh `join`), `ended` (60 s, or a refusal) |
| `ended` | `ended`, a close code, 60 s, Leave, `pagehide` | `idle`; every track stopped and every peer connection closed on entry |

**What is done before what.**

- No device is opened before a press: `checking` is entered only from a
  click or a key, and `getUserMedia` is called only there
  (`huddle-audio.spec.ts` counts the calls from an init script before the
  first press).
- The app sends no frame before `join` (or `resume`), and treats nothing
  before `joined` as presence.
- On every end the tracks are stopped before the words are shown, so no
  one reads "You left" with a microphone still open.
- The server sends `room` after the provider's `RoomChanged` returns, and
  the app reads `GET /api/rooms` after the event; a page that misses the
  event (the stream down) reads again on the `snapshot`.
- `RecordHuddle`: the ticket resolved, then Dispatch's `link.huddle`
  committed with its receipt, then nil to the provider; a provider that
  calls again after a crash replays the receipt (same op id) and nothing
  is written twice (`TestRecordHuddleOnce`).
- `room` and `room_invite` events are queued and coalesced; a full queue
  drops the oldest; the provider's call never waits on a client.

**Invariants**, each a named test:

- I1. A plain build draws no huddle control and makes no request to
  `/api/rooms` (`huddle-offer.spec.ts`; `TestRoomsNotOfferedByTheFreeDefault`).
- I2. Joining grants nothing: the reply's token is accepted by no route of
  the server, and the principal's grants are the same after a join
  (`TestJoinGrantsNothing`).
- I3. A room is offered only for work the principal may read and that
  exists here; a provider's answer for any other ref is dropped
  (`TestRoomsFilteredToReadable`, `TestGuardRoomsKeepsAskedRefs`).
- I4. A media fault touches no session, run or decision: with the provider
  hanging, failing or panicking in `Room` and `Rooms`, a session's input
  echoes, a stub run's task proceeds and a question can be answered
  (`TestRoomFaultsTouchNoSession`); with the room service held, dropped
  and stopped, the page's terminal still echoes (`huddle-terminal.spec.ts`).
- I5. Every device track the app made is stopped at every end
  (`roomClient.test.ts`, `huddle-audio.spec.ts`, `huddle-states.spec.ts`).
- I6. A `huddle` link is written only through `RecordHuddle`, once per
  room session (`TestHuddleLinkRefusedOnLocalWrite`, `TestRecordHuddleOnce`).
- I7. Every frame, record, route body and reply is within its bound
  (`TestRoomVectors`, `roomProtocol.test.ts`, `TestRoomsReplyBounded`,
  `TestJoinBodyBounded`).

**Stores.** The server stores nothing of a room but the `huddle` link.
Dispatch's kind is text: no schema version, no migration; a replica
running an older build keeps the link as it keeps an unknown reserved
kind brought by a merge (DS1's rule for `decision`). The browser keeps
the two device ids. The fake keeps everything in memory.

**Protocol.** The room protocol in `ext/room.go`, `roomProtocol.ts` and
`docs/protocol.md`'s Rooms (PR-2); the routes and events in
`internal/api`, `protocol.ts` and `docs/protocol.md`'s HTTP API (PR-4).
`internal/proto` is not touched: the room protocol is not spoken by this
server, and its records must be importable by a provider's service
(Owner decisions, 1).

## Out of scope

- The room service, its relay, its admission, its feed of presence and
  any group forwarding: the provider's.
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
- The desktop forwards owed for #140.

## Tests per surface

**Go** (`make lint`, `go test -race -count=1 -timeout 30m ./...`,
`make lint-static`; `internal/opencore`'s rules hold, no new module, so no
notices change):

- PR-1, `ext`: `TestRoomJoinValidAt` (each field at its bound and one
  past; `ws://` to a loopback host only; `ExpiresAt` past and too far),
  `TestRoomRequestValid`, `TestRoomsAnswerValid`, `TestRoomInviteValid`,
  `TestHuddleRecordValid`. `ext/exttest`: `TestMediaCheckCatchesInvalidJoin`,
  `TestMediaCheckRoomLister` (a lister that answers late, invalid, or
  outside its refs is caught), `TestFakeMediaRooms`,
  `TestFakeHostRecordsRoomDoors`. `ext/local`: `TestLocalMediaListsNoRooms`.
  `internal/api`: `TestGuardMediaJoinValid` (an invalid join is ok
  false), `TestGuardRoomsLateFailingPanicking`,
  `TestGuardRoomsKeepsAskedRefs`, `TestHostRoomDoorsStubbed`.
- PR-2, `ext`: `TestRoomVectors` (every vector decodes or is refused with
  its error), `TestRoomFrameBounds`, `TestRoomFramesRefuseUnknownFields`,
  `TestRoomCloseCodesNamed`.
- PR-3, `internal/roomfake`: `TestTwoPeopleTalkThroughTheFake` (two
  `pion/webrtc` peers in the test process join one room with the fake's
  tickets, exchange an Opus track through its signaling, and each reads
  RTP from the other: two people's audio end to end with no browser),
  `TestFakeSpeaksTheVectors`, `TestFakeProviderPassesTheSuite`
  (`exttest.Media` and `exttest.Starter` on the provider),
  `TestFakeTicketsOneUse`, `TestFakeResumeWithinAMinute` (its clock seam),
  `TestFakeControlSeams`, `TestFakeRoomClosedRecordsOnce`,
  `TestFakeKeepsNoFrames` (its logs searched for the SDPs and tickets).
- PR-4, `internal/api`: `TestRoomsOfferedOnlyWhenAvailable`,
  `TestRoomsNotOfferedByTheFreeDefault`, `TestRoomsRefsBounded`,
  `TestRoomsRefWithHomeParsed`, `TestRoomsFilteredToReadable`,
  `TestRoomsReplyBounded`, `TestJoinNeedsReadAccess`, `TestJoinGuarded`
  (late, failing, panicking, invalid), `TestJoinBodyBounded`,
  `TestJoinRateLimited`, `TestJoinTicketFromServerRecords`,
  `TestJoinGrantsNothing`, `TestRoomChangedCoalesced` (driven through the
  coalescer's clock seam), `TestRoomEventsAdminOnly`,
  `TestRecordHuddleOnce`, `TestRecordHuddleNoTicketWritesNothing`,
  `TestRoomFaultsTouchNoSession`; `internal/dispatch`:
  `TestHuddleLinkRefusedOnLocalWrite`, `TestAddHuddleReplays`,
  `TestHuddleNoteBounded`, `TestHuddleLinkKeptOnMerge`.
- PR-8, `internal/api`: `TestRoomsDecisionNeedsItsRecord`,
  `TestJoinDecisionTicketFromRecord`.
- PR-11, `internal/api`: `TestRoomsDecideGrantOwnDecisionOnly`,
  `TestJoinDecideGrantRefusedElsewhere`, `TestDecisionStreamRoomEvent`.

**Vitest** (`npm --prefix web run typecheck`, `npm --prefix web test`):
`roomProtocol.test.ts` (PR-2: the vectors), `protocol.test.ts` (PR-4: the
routes' and events' types); `roomClient.test.ts` (PR-5: each state's
transitions, `renew` every `renewEvery`, `resume` after a drop, a refused
resume rejoining, connected peers kept on a rejoin, 60 s without a room
connection ending the call, every end stopping every track, all on fake
timers and fake sockets), `deviceCheck.test.ts` (PR-5: no device before
the press, Cancel stopping the track, the blocked and missing words, the
speaker list only with `setSinkId`), `huddle.test.ts` (PR-5: the state
table's words, `secureEnough`, `offered` from capabilities and the reply);
`huddlePoint.test.ts` (PR-6: `pointRoute` for each kind), the phone
sheet's layout function (PR-6); the screen and camera states (PR-9, PR-10).

**Playwright** (`make test-e2e`; the huddle specs drive a second server of
their own, `bin/conductor-roomfake`, started by `web/e2e/huddle.ts` with
its own root, HOME and data directory and stopped after the file, with
two browser contexts holding the workbench token; Chromium's fake devices
through `--use-fake-device-for-media-stream`; the microphone granted per
context with `permissions`, never the fake-UI flag, so the blocked state
is a context without the grant):

- `huddle-offer.spec.ts` (PR-5): on the plain e2e server, after the
  ticket page's header is drawn and the capabilities read has answered,
  no huddle control, and no request to `/api/rooms` made.
- `huddle-audio.spec.ts` (PR-5): no `getUserMedia` call before the press
  (counted by an init script); both join; each page's inbound audio's
  `bytesReceived` and `totalAudioEnergy` grow (polled from `getStats`, the
  mechanism, until they do); mute proven at the sender (the track's
  `enabled` false) and by the other page's muted mark from `person`; Leave
  ends every track the page made (`readyState` `ended`, read from the
  init script's list).
- `huddle-states.spec.ts` (PR-6): blocked, missing (a context with no
  device), full, removed, access ended, elsewhere (a second tab),
  reconnecting after the fake's `drop` and back in with the mute kept and
  the same `RTCPeerConnection` (counted by the init script), "Could not
  reconnect" after `refuse-resume` and `stop`, unreachable at the fake's
  `ended {why: unavailable}`, ending soon at the fake's `ending`; each
  driven by a control seam, never a clock.
- `huddle-invite.spec.ts` (PR-6): Invite lists the fake's people; the
  invitee's toast from `room_invite`; Join from the toast opens the work
  and its device check; Show them sends and the other's Go opens the
  ticket.
- `huddle-phone.spec.ts` (PR-6): at 390 by 844, the sheet collapsed and
  expanded, Mute and Leave reachable, the page scrolling above it.
- `huddle-terminal.spec.ts` (PR-6): a stub session's terminal on the run
  page with a call in progress; with the fake's `hold`, then `drop`, then
  `stop`, the terminal echoes a typed line each time (waited for by its
  text), and the run's task still answers its commands.
- `huddle-history.spec.ts` (PR-6): after both leave and the fake's grace,
  the ticket's history shows the huddle's line (waited for by the
  receipt's arrival in the page, not by time).
- `huddle-decision.spec.ts` (PR-8): the control on a decision's card on
  `/decisions` and in the ticket page's Decisions section, on the Review
  view keyed by the candidate; the dock never covering the answer
  buttons (their boxes checked against the dock's).
- `huddle-screen.spec.ts` (PR-9), `huddle-camera.spec.ts` (PR-10) with the
  fake devices; `huddle-reviewer.spec.ts` (PR-11) with a principal holding
  the decide grant; `huddle-group.spec.ts` (PR-12) with three contexts.

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
usage string, the entitlement, the installer's rule added and removed;
PR-10 the camera's), `picker.test.ts` (PR-9). `make desktop-e2e` unchanged.

**By hand, what tests cannot cover**, each made by the owner, who has the
machines and a microphone, on one machine with two browsers against the
e2e binary (PR-6 and PR-7 merged) and then, for the item, with a
provider (Acceptance): the audio to the ear for ten minutes; the macOS
microphone prompt and System Settings words in the desktop app; on
Windows with the server inside WSL, the desktop app and Edge on that
machine talking with nothing configured and no firewall prompt; Safari
and Firefox against Chromium; the phone layout on a real phone; the
terminal's feel during a call with a screen shared (PR-9); and, with a
provider, two machines on different networks, one forced to its relay.
An agent may run the Mac's and Windows' automated rows; the by-hand rows
need a person at the machine.

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

The first five rows deliver two people's audio end to end against the
fake: PR-3 with two pion peers in Go, PR-5 with two browsers. PR-8 is
the first row that needs another plan's rows; PR-11 the first that needs
Program item ID1's.

| PR | Title | Touches | Depends on | Parallel with | Size |
|---|---|---|---|---|---|
| PR-1 | ext: the room seam's records, presence and three Host doors, checked by the guard and the contract suite | `ext`, `ext/exttest`, `ext/local`, `internal/api` | none (C8 on `main`) | PR-7 | ~550 + 650 |
| PR-2 | ext, web, docs: the room protocol conductor-room/1, its records, TypeScript and vectors | `ext`, `web/app/utils`, `docs/protocol.md` | PR-1 (`ext`) | PR-7 | ~600 + 550 |
| PR-3 | roomfake: the fake room service, the fake provider and the e2e server binary | `internal/roomfake` (new), `Makefile`, `docs/repository-map.md` | PR-2 (the records) | PR-4, PR-7 | ~750 + 650 |
| PR-4 | api, dispatch: the room routes, the room events, the Host doors built, the huddle's line on the ticket | `internal/api`, `internal/dispatch`, `web/app/utils/protocol.ts` and its test, `docs/protocol.md` | PR-2 (`web/app/utils`, `docs/protocol.md`) | PR-3, PR-7 | ~650 + 750 |
| PR-5 | web: two people talk: the room client, the device check, the control on the ticket and run pages, the dock, mute, devices, presence, leave | `web/app`, `web/e2e` | PR-3, PR-4 | PR-7 | ~800 + 700 |
| PR-6 | web: the huddle's states, reconnect, invitations, Show them, the phone sheet, the line in the history, media faults | `web/app`, `web/e2e` | PR-5 | PR-7 | ~650 + 750 |
| PR-7 | desktop: the microphone in the desktop app: the permission handler after a press, macOS usage and entitlement, the Windows program rule | `desktop` | none | PR-1 to PR-6, PR-8 | ~300 + 350 |
| PR-8 | web, api: huddles on the decision page, the decision cards and the Review view | `web/app`, `web/e2e`, `internal/api` | PR-6; #637 and #640 (DR1), #647 and #652 (MB0), #631 (RV1) | PR-7 | ~450 + 500 |
| PR-9 | web, desktop, roomfake: screen sharing | `web/app`, `web/e2e`, `desktop`, `internal/roomfake`, `docs/protocol.md` | PR-8 (`web/app`), PR-7 (`desktop`); the hosted plan's answer on telling a screen track | none | ~600 + 550 |
| PR-10 | web, desktop, roomfake: cameras | `web/app`, `web/e2e`, `desktop`, `internal/roomfake` | PR-9 | none | ~450 + 450 |
| PR-11 | api, web: a reviewer's huddle on a decision through the decide grant; the features entry | `internal/api`, `web/app`, `web/e2e`, `docs/protocol.md`, `docs/features.md`, `README.md` | PR-10 (`web/app`), PR-8 (`internal/api`); Program item ID1's open-source rows (the decide grant, the decision stream) | none | ~450 + 500 |
| PR-12 | web, roomfake: small groups, up to six people | `web/app`, `web/e2e`, `internal/roomfake`, `docs/features.md` | PR-11; the hosted plan's forwarded topology, qualified | none | ~450 + 450 |

The row numbers name the PRs; the table's order is the merge order.
PR-9 and PR-10 come after PR-8 so that the decision's huddle, which the
acceptance needs, is not held behind them; should DR1's, MB0's or RV1's
rows be late, PR-9 and PR-10 may go first, PR-8 then merging `main` in,
and the order is recorded on their PRs.

**PR-1** (`ext`, `ext/exttest`, `ext/local`, `internal/api`): `RoomJoin`'s
two fields and `ValidAt`; `RoomRequest`'s two fields, bounds and `Valid`;
`RoomLister`, `RoomRef`, `RoomsAnswer`, `RoomState`, `RoomPerson`;
`RoomInvite`, `HuddleRecord`; the three `Host` methods; the constants;
`ext/ext.go`'s Records paragraph; `guardMedia`'s `ValidAt` and `Rooms`;
`extHost`'s stubs; `FakeMedia`'s lister, `FakeHost`'s records, the `Media`
check. `ext/local.Media` unchanged (its test that it lists no rooms).
`cmd/conductor/ext_bounds_test.go` gains the room bounds beside the
server's. Tests: PR-1's above.

**PR-2** (`ext`, `web/app/utils`, `docs`): `ext/room.go` (the messages,
`DecodeAppFrame`, `DecodeServiceFrame`, `PointKinds`, `EndReasons`, the
error codes, the close codes), `ext/testdata/room/app.json` and
`service.json`, `web/app/utils/roomProtocol.ts`, `docs/protocol.md`'s
Rooms section. Tests: `TestRoomVectors` and its siblings,
`roomProtocol.test.ts` on the same files.

**PR-3** (`internal/roomfake`, `Makefile`, `docs`): the `Service`, its
control seams, the `Provider`, `internal/roomfake/e2e/main_test.go`, the
Makefile's `build-roomfake` and `test-e2e`'s dependency on it,
`docs/repository-map.md`'s row. Tests: PR-3's above, among them
`TestTwoPeopleTalkThroughTheFake`.

**PR-4** (`internal/api`, `internal/dispatch`, `web/app/utils`, `docs`):
`rooms.go` (the two routes, the access rules, the ticket's resolution, the
rate limit), the two events and their queue, `extHost`'s three doors
built, `LinkHuddle` and `link.huddle` in Dispatch, the protocol rows and
types. Kind `decision` answers no room until PR-8 (no record to read).
Tests: PR-4's above.

**PR-5** (`web/app`, `web/e2e`): `roomClient.ts`, `huddle.ts`,
`useHuddle.ts`, `HuddleControl`, `HuddleDeviceCheck`, `HuddleDock`,
`HuddlePeople`, `HuddleIndicator`, `HuddleAnnouncer`; the control in
`DispatchTicketPage.vue`'s and the run page's headers; the layout's dock;
`useAttention.ts`'s two events; the bridge call to `mediaPress` where it
exists; `web/e2e/huddle.ts` (the second server); `huddle-offer.spec.ts`,
`huddle-audio.spec.ts`. Tests: the vitest files and specs named for PR-5.

**PR-6** (`web/app`, `web/e2e`): every state of the table not in PR-5,
the reconnect and rejoin, `stats` and the relayed mark, `HuddleSheet`,
`HuddleInvite`, `HuddlePoint` and `pointRoute`, the M key and its `?`
line, the `huddle` link's line in `DispatchHistory.vue` (an icon, the
note, the time; no URL); the specs named for PR-6.

**PR-7** (`desktop`): `permissionAllowed` with details and the press
record, `setPermissionCheckHandler`, `mediaPress` and `mediaAccess` in
`preload.ts` and `ipc`, `mac.extendInfo`'s microphone string, the
audio-input entitlement, `installer.nsh`'s program rule and its removal.
Tests: `window.test.ts`, `packaging.test.ts`.

**PR-8** (`web/app`, `web/e2e`, `internal/api`): `HuddleControl` on
DR1's `DecisionCard.vue` and in `DispatchDecisions.vue`, on MB0's
`InboxItem.vue`, and on RV1's Review view keyed by its candidate; the
dock's padding on `/decisions` so it never covers the answer buttons;
`/api/rooms` and the join reading DR1's record for kind `decision` (it
exists; its ticket). Tests: `TestRoomsDecisionNeedsItsRecord`,
`TestJoinDecisionTicketFromRecord`, `huddle-decision.spec.ts`.

**PR-9** (`web/app`, `web/e2e`, `desktop`, `internal/roomfake`, `docs`):
Share screen, `HuddleScreen` (the sharer's and the viewers' states, the
panel, fit or actual size, one sharer), `state`'s mids as the hosted plan
settles them (and `docs/protocol.md` with the vectors if they change), the
fake's `screen` kind, the desktop's display handler and picker and
`mediaAccess('screen')`. Tests: `huddle-screen.spec.ts`, `picker.test.ts`,
`window.test.ts`'s display case.

**PR-10** (`web/app`, `web/e2e`, `desktop`, `internal/roomfake`): Camera,
`HuddleCameras`, the fake's `camera` kind, the camera's usage string and
entitlement, `video` after a camera press. Tests: `huddle-camera.spec.ts`,
`window.test.ts`'s and `packaging.test.ts`'s camera cases.

**PR-11** (`internal/api`, `web/app`, `web/e2e`, `docs`, `README.md`): the
decide grant admitted to `/api/rooms` and the join for a decision that
admits it, and refused everywhere else; the decision stream's `room`
event; the app asking `/api/rooms` without the capabilities read for such
a principal; the access-ended words from a pass that ended; the README's
Extensions line; `docs/features.md`'s entry (what a person sees, the
observed acceptance's line left for the owner). Tests: PR-11's Go tests
and `huddle-reviewer.spec.ts`.

**PR-12** (`web/app`, `web/e2e`, `internal/roomfake`, `docs`): up to six
people, the `forwarded` topology's client side as the hosted plan defines
it, the group's dock and phone layout, the fake's `maxPeople` 6; the
features entry extended. Tests: `huddle-group.spec.ts`, the room client's
forwarded cases.

## Owner decisions

Dated 2026-11-06. Each is a default the owner can overturn with a dated
row in the Program's status log (the Program, section 3.1's convention).
The Program leaves this plan no open decision of its own: O7 was closed
by D21, which this plan follows (two people's audio first; the group
topology is the provider's to qualify).

1. **The room protocol's records live in `ext`**, its TypeScript in
   `web/app/utils/roomProtocol.ts`, its words in `docs/protocol.md`, with
   one set of vectors read by both. `internal/proto` holds what this
   server speaks; a provider's room service in another module cannot
   import it, and this server does not speak the room protocol.
2. **The fake room service and provider are in `internal/roomfake`, and
   the e2e server that registers them is a test binary** (`go test -c`),
   so no build of `cmd/conductor` can contain a provider; the huddle
   specs start it as a second server beside the plain one.
3. **Invitations reach the open app through `Host.RoomInvited` and a
   `room_invite` event**, beside `Host.Notify`, which reaches only the
   notification sinks.
4. **One call per tab, in the layout**: moving between pages keeps it;
   closing or reloading the tab leaves the huddle; a second tab that
   joins ends the first ("elsewhere", the room service's rule).
5. **The screens read `offered`**: the capabilities' `media` row only
   decides whether an admin's page asks at all.
6. **The phone layout below `sm` (640 px)** is a bottom sheet above the
   bottom bar; screen sharing is not offered there, a shared screen is
   shown.
7. **Mute on the M key**, only in a huddle and only with the focus in no
   text field, terminal or editor; listed in the `?` list.
8. **The work's ticket for a room is the server's reading**, never the
   client's: a room cannot be attached to another ticket by a request.
9. **A home ref is separated by a space** in `GET /api/rooms`.
10. **`point`'s kinds** are `ticket`, `decision`, `run`, `task`,
    `candidate`, `finding`, `file`.
11. **Remove** is offered to an admin principal on the server that holds
    the work; the room service decides whether it is taken.
12. **No audio settings** beyond the microphone, the speaker and Join
    muted; the browsers' own echo cancellation, noise suppression and
    gain control stay on.
13. **The Windows program rule comes with the installer**, at install and
    at the next update; no Settings button asks for it.
14. **The decision's huddle (PR-8) goes before screen sharing and
    cameras**, since the acceptance is a decision discussed by two
    people.

## Acceptance

The item's boundary (the Program, section 4.5, CL1) is people discussing
a decision around the same work and recording the outcome through the
normal workflow, with no media fault ever interrupting execution. It is
observed with a registered provider and its room service, which are not
in this repository, on a build at a tag holding PR-11, DR1's and MB0's
records and pages and Program item ID1's open-source rows:

1. A managed run opens a decision. The operator opens it on `/decisions`
   and presses **Huddle**; the device check asks for the microphone only
   then. A second person, admitted to that decision, opens it and sees
   "<operator> is in a huddle · Join", joins from a phone, and the two
   talk. One side is the desktop app on Windows with the server inside
   WSL, with nothing configured on Windows.
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
PR-1 to PR-7 give two people's audio end to end against the fake, with
every state, on a desktop and a phone layout, and the media-fault and
plain-build invariants (`huddle-terminal.spec.ts`, `huddle-offer.spec.ts`,
`TestRoomFaultsTouchNoSession`). Done for this plan is every row merged
with its checks green and the by-hand rows above made; Done for the item
is the five steps observed, recorded in `docs/features.md` and in the
Program's status log. The owner is needed for the by-hand rows, the
second person and the observation; until the group topology is
qualified, Done covers two people, and PR-12 follows.
