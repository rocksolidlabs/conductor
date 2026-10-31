# Architecture

Conductor is one Go binary plus an embedded Nuxt single-page app. It runs in two
roles.

The names follow the railroad. **Conductor** is the application, where people
direct and supervise the work. **Yardmaster** is the agent harness: it manages
the agents' execution, crews, tools, lifecycle and coordination, the managed
runs of #334. **Brakeman**, to come (#356), is the sidekick agent that watches
the work, anticipates problems, summarizes progress and assists the people: an
agent powered by Yardmaster with privileged access to the operation's
telemetry, not a part of the harness. **Switchman**, to come, routes tasks
between agents, crews and people.

## Server-hosted sessions

```mermaid
sequenceDiagram
  participant B as Browser (Nuxt + xterm.js)
  participant S as conductor serve
  participant P as PTY (claude, codex, …)
  B->>S: POST /api/sessions {agentId}
  S->>P: fork with creack/pty
  B->>S: GET /ws/sessions/{id}?token=… + hello
  S-->>B: welcome, SCROLLBACK…, ready
  P-->>S: output
  S-->>B: OUTPUT frames (fan-out to every viewer)
  B->>S: INPUT / resize (control role only)
  S->>P: write / TIOCSWINSZ
```

`internal/session.Local` owns the PTY: a pump goroutine copies output into a
256 KiB ring buffer and broadcasts it to subscribers. Each subscriber has its own
bounded queue; exceeding 1 MiB of backlog evicts it as a slow consumer, and the
client reconnects to get a fresh replay. Each subscriber's own goroutine writes
its frames and then closes its transport; ending a subscriber only marks it,
so a transport that stops taking frames, or takes its time to close, holds up
neither the pump nor anything else under the session's lock. A revoke waits
for the link's viewers' transports to close, all at once, holding no lock.
What was queued for a subscriber is dropped as it ends, but for one the
session's end closes (its link ends with the session): it is written first,
the session's last output and its status, unless its link is revoked or
expires meanwhile or the session leaves the server, and a server cuts a
connection that has not taken it 5 s after the close. A hosted session's
viewers on the server are closed and drained the same way at its end; a
revoke is never drained.

`Local.Submit` is the one way Conductor types into a session (a crew member's
prompt, a handoff, a broadcast) and the way a reply box's `submit` message is
typed: the text, as a bracketed paste while the program has that mode on (the
pump follows `ESC[?2004h`/`ESC[?2004l`), then, 250 ms later, a carriage return
written on its own, one submission at a time per session, with no lock held
across the pause (which, in a session with a trust watcher, lasts until the
watcher has looked at what was drawn since the text), cancellable, and never
written twice. A submission whose link no longer opens the session (revoked,
expired) before its carriage return is written gets none: if its text is
typed, the session's turn stays held (`holdTyped`, attention `source:"submit"`)
until a person's INPUT, or the session's end, so no other line joins the
text. A person's INPUT stays
raw. The same pump treats output that only sets the window title as no output,
watches the screen's text for the agent's workspace-trust question
(`Options.TrustPattern`), and an INPUT that is only a terminal's automatic
reports answers nothing. A session that ends clears its attention as it ends.

## Developer-hosted sessions

```mermaid
sequenceDiagram
  participant H as conductor host (laptop)
  participant S as conductor serve
  participant B as Browser
  H->>S: GET /ws/host + register
  S-->>H: registered {sessionId, secret, iceServers}
  B->>S: GET /ws/sessions/{id}?token=…
  S-->>B: welcome {transport: webrtc, iceServers}
  S->>H: viewer_join
  B->>S: SIGNAL offer / ice
  S->>H: offer / ice
  H->>S: answer / ice
  S-->>B: SIGNAL answer / ice
  B-->>H: data channel: hello, INPUT…
  H-->>B: welcome, SCROLLBACK, OUTPUT…
  Note over B,S: no channel within relayTimeoutMs → SIGNAL relay
  S->>H: relay_start
  B->>S: INPUT (WebSocket) → RELAY[viewerId] → H
  H->>S: RELAY[viewerId] OUTPUT → B
```

The host reuses `session.Local` unchanged; only the sinks differ (a pion data
channel with backpressure, or a relay sink that wraps frames in RELAY envelopes).
Everything the host writes on its control connection goes through one writer
per connection (`hostagent/controlwriter.go`): the host's own messages ahead
of the relayed frames, the relayed viewers in turn, and a socket whose writes
never wait, so the WebSocket library's own time limits (a write's, a ping's
answer) never fire on a server that reads slowly; a relayed viewer whose frame
waits too long for its turn is let go alone, and the connection is given up
only when its socket takes nothing for two minutes.
The server tells the host on that connection when a link goes, in a lane of
the server's own that its writer takes ahead of the frames it relays
(`signal.HostConn`): the link's viewers are closed first, and what of
theirs was still queued is dropped as the writer comes to it, so nothing of
a viewer of a link reaches the host after the link's going. What a viewer
who joined through a link sends (a line, a key, a save, a read, an editor's
open, a chat post) is acted on only while its link stands
(`AttachOptions.Authorize`, which an attach needs alone) and the host
vouches for its server (`AttachOptions.Fresh`; `hostagent/liveness.go`): a
ping of the host's own, sent within 15 s, answered, nothing else counting
(see docs/protocol.md, the host control connection). The host pings as a
viewer joins through a link and every 5 s while such a viewer, or something
of theirs, is there; a first line waits for its join's ping to be answered.
A connection that stops carrying anything without either end closing it,
or whose server pauses its reading for longer than the bound, is then a
link gone for what they send, as a connection lost is, and one the host has
pinged and heard nothing on for two minutes it gives up. The server, for
its part, pings the host every 5 s while a link a viewer joined the session
through stands (`api/ws_host.go`, `hostKeepalive`), for older hosts. The
host's control connection's read loop waits on nothing but the read: a
save's parts and an offer's answer run off it, a save's in a bounded,
ordered queue of the viewer's, as its keys and editor requests do.
A relayed viewer the
server holds more than 512 KiB for, unwritten, is behind (`viewer_behind`):
the host holds its frames until it catches up, as it holds a WebRTC viewer's
while the data channel is full, so the shared connection carries what each
relayed viewer takes.
The server never sees terminal bytes on the WebRTC path and never runs the
command. On the relay path it carries them and can read them; and since it
carries the signalling and tells the host each viewer's role, a host trusts
its server with both.

## Reachability

What each piece does when a link crosses a network boundary:

- **The server** serves the join page, brokers the WebRTC signalling of a
  hosted session (offer, answer, candidates) and relays its terminal channel
  when no direct path forms. Everything a guest does begins with an HTTP
  request to it, so a link works only where the server is reachable.
- **STUN** (the `iceServers`) answers a browser or a `conductor host` with its
  own public address and port, so ICE can try a direct UDP path for the
  terminal channel. It carries no data and does not make an HTTP page
  reachable.
- **TURN** (none configured) would relay the terminal channel through a public
  relay; Conductor's own relay over the host's WebSocket does that job today.
- **A forwarded port, a public address or a proxy** makes the server
  reachable. That, and only that, makes a link open from outside.

Two packages make a machine behind a home router reachable with nothing
configured: `internal/reach` asks STUN for the public address and maps the
TLS port on the router through UPnP IGD, PCP or NAT-PMP (the plain port is
never mapped; the lease is renewed and deleted on shutdown), and
`internal/certs` obtains a certificate from Let's Encrypt through lego for
that address (an IP-address certificate, `tls-alpn-01` on the mapped 443) or
for configured domains, keeps it under `dataDir/tls` and serves it on a
second listener (`tls.listen`), HTTP/1.1 only. The manager serves nothing
before the first issuance. Share links take `https://<public address>/…`
only once the port is mapped and the certificate is ready; until then they
keep the address the request came through. A mapping is reported as
"mapped", never "reachable": the server's own check through the public
address is often refused by the router (no hairpinning), so a person on
another network is the only proof. `conductor serve` wires the three: the
mapper tells the certificate manager the address once the port is mapped,
and the API reads both for `GET /api/reach` and the link base.

Every server publishes its sessions to a switchyard by default (`rendezvous`
in the config: the public `switchyard.rslabs.net`, another one, or off), so
a link works from anywhere whatever is in front of this machine:
`hostagent.Uplink` registers each local session with the public
Conductor over the host control connection, exactly as `conductor host`
does for the process it runs, one connection per session; the rendezvous
lists it as hosted, serves its viewers over WebRTC or its relay, and mints
the links; the local server chains its session hooks to the publication so
attention and activity travel both ways, and ends the publication when the
session leaves.

## Crew runs

A crew run is `internal/crew`'s `Engine`, and every member is an ordinary
server session: the engine starts it through the `Launcher` interface, which
`internal/api` implements with `createLocalSession`, the one path
`POST /api/sessions` takes, so the working-directory check, the catalog
lookup, the environment and the hook injection are the same as for any
session. The session carries its run in `Info.crew`, and its process gets
`CONDUCTOR_CREW`, `CONDUCTOR_RUN`, `CONDUCTOR_MEMBER` and `GOAL`. With
worktree isolation the engine first makes the member's worktree
(`gitcli.AddWorktree`: `git worktree add --no-checkout`, then the checkout
inside the new worktree, so that the configuration its branch and git
directory include is the one looked at; argv, never a shell, through
`gitcli.Run` like every git Conductor runs: command-line configuration turns
off the hooks, the file system monitor, the pager, external diff and text
conversion, the editor, credential helpers, transports and every clean,
smudge and process filter the configuration may name) and starts the member
in its own worktree under
`<cwd>/.conductor/worktrees`; it never removes one. A launch returns once the
sessions exist; a goroutine per member, on the run's own context, then waits
until the session is ready (its agent reports `needs_input` or `done`, or its
output goes quiet, and it is not between two screens of its start), held while
a trust question shows, and submits the role prompt with `Local.Submit`,
stamping the member prompted just before the carriage return. The engine
listens through two hooks that never wait: a sink on the activity fan-out
(`Engine.OnActivity`, beside the webhooks) sees every entry of every session,
starts the `after` members when a member first reports `done` and takes a
member's `handoff` events, and the session change hook (`Engine.OnChange`,
chained after the SSE publish) wakes the handoffs queued for a member when it
leaves `needs_input`: a prompt that is cleared records no entry, only a change.
Handoffs and broadcasts are submitted unless the session waits, a look at the
attention state made in the same step that finds the prompt a write would
answer, and a prompt raised during the pause keeps their carriage return back,
so neither ever answers a prompt. What changes in a run without a session
change (a member reserved, started, prompted or ended, an entry in its log, a
stop) is reported through `Engine.OnRunChange`, under the engine's lock and
without waiting, as a `run` event on `/api/events`, and a forgotten run as one
with `removed`; the browser's live store reads the run again, so no page
polls. A run's state (running, needs input, stopped, finished) is derived each
time it is read. The run log's note of a delivered handoff carries the two
members as fields (`byName`, `to`), which the crew graph on the run page
draws as dashed edges beside the solid "after" edges of the start rules; the
layout is the client's (`web/app/utils/crewGraph.ts`: column by depth in the
forest the rules form, row by crew order), and the editor's graph holds the
same rules as the server (one parent, no self edge, no cycle) before a save. `Engine.ResumeMember` starts an ended member again in its
worktree, resuming its agent's own session through the agent's session recipe
when it can. Runs live in memory, as sessions do;
run links are share-store links scoped to a run (`Link.RunID`), which the
server resolves to the run's member sessions through `Engine.MemberOf`.

A crew with tasks launches a **managed run**, Yardmaster's (#334): the same
engine with its truth in the ledger (`internal/crew/ledger`, one SQLite database per data
directory) instead of memory. `task.go` dispatches each task as an attempt
once the tasks it needs have results: a reservation among the server's
sessions, a worktree at the commit the task starts from (its first need's
result, or the run's base) on the branch `crew/<run>/<task>.<n>`, the task
written into it as `.conductor/task.md` and `task.json`, the worktree made
usable by the crew's project manifest (below), then the member's session
through the same `Launcher` and one typed line saying where the task is and
how to submit. The attempt's
session submits its result with its own token (`POST
/api/sessions/{id}/task/result`, the `task_result` tool or `conductor task
result`): the commit at its HEAD, refused while the tree is dirty or the
commit does not descend from every input. `verify.go` then runs the task's
checks against that commit in a detached checkout through `internal/proc`
(argv, a timeout, the process group ended with the run, the output's head and
tail), one at a time across the server, and the checkout compared with the
commit after each; a failing check fails the attempt and the next one starts
from that commit with what failed in its task file. A review's `needs_changes`
reopens the task it reviewed and withdraws, transitively, whatever rested on
the result. Every transition is one ledger transaction committed before the
side effect it precedes (the reservation before the worktree, the launch
before the session, the typing before the line, a check before its process),
so a crash leaves intents the next start concludes: `ledger.Recover` marks
what was open, the records are rewritten from the ledger, and
`continue.go` makes a continuation of an interrupted run that keeps the
results that stood, verifies a candidate again and dispatches the rest, the
attempts spent before counting against the tasks. A person's commands (pause,
retry, cancel, accept) are `commands.go`'s receipts, written in the change's
own transaction, so a repeated command answers the same receipt. `messages.go`
carries the members' durable messages, typed after the recipient's task line,
and `checkpoints.go` an attempt's blocker and checkpoints, where the next
attempt starts after a session is lost.

A managed crew may carry a **project manifest** (EN1, `manifest.go`): what
makes a fresh tree of the project usable, how it is checked and how it is
cleaned up. It is not an agent's recipes, which are the catalog's. It is
either the crew's own, counted by the server at each change (a receipt names
it `crew:<revision>`), or `conductor.manifest.json`, read at each tree's
commit (`file:<blob>`), so a task that changes it is checked by the manifest
it changed; a crew saved with `setup` runs it as the manifest's setup phase.
A tree is the attempt's worktree or its result's checkout. `steps.go` makes
each one usable before the member's session or the first check rests on it:
the manifest resolved and its host names checked (`env`: only names the
server's `envPassthrough` allows and its environment has), its ports drawn
(`CONDUCTOR_PORT_<NAME>`, leased by the engine until the tree ends), its
state directory made (`.conductor/state`, through an `os.Root`), its setup
steps, its services (`proc.Serve`, under the run's teardown context, never
the attempt's), its readiness probes (a command, or one `GET` on the
loopback with no redirect followed), then `tree.ready` with the preview. The
checkout runs the manifest's verify steps after the task's checks. The
prerequisites run once per run in the crew's directory before the first
attempt, and again at each unpause. Every step goes through the tree's
executor (`treeExec`: `internal/proc` natively) and is a receipt: a `checks`
row with its phase (a `prerequisites` row for a prerequisite) holding its
command, directory, manifest, exit, timing, words, log and SHA-256, read
through `GET /api/runs/{run}/tasks/{task}/attempts/{n}/steps` and `GET
/api/runs/{run}/prerequisites` with their logs, from the ledger, so a run
the engine has let go reads the same. A step that fails in a tree the agent
has not touched is the host's or the manifest's: the attempt is not
counted, the task says which step failed, and the dispatch pauses with the
words (`pausedWhy`); one that fails in the checkout of a result is the
result's, counted as a failing check is. However a tree ends (a result
taken, a check failed, a cancel, a withdrawal, a stop, the server's
shutdown), `endTree` waits for it to be quiet, stops its services, runs its
cleanup steps, releases its ports and records `tree.ended`; nothing removes
the tree itself. A stop ends the run's services beside its sessions before
`recordStop`, and the server's shutdown arms a cut-off first
(`Engine.Draining`) so the teardown fits its budget; a service a crash left
running is reported at the next start, never signalled. The run page shows
the receipts under each attempt (`RunAttemptSteps.vue`) and the
prerequisites above the tasks, the header's `paused` badge with its words
and `cleaning up` while trees end, and Open preview on the task's row and
in the palette while the attempt's `preview` is set: a new tab, since the
preview is on the server's loopback, not proxied, and an iframe of it would
be mixed content under TLS.

## Dispatch

Dispatch is a component, Conductor's own work tracking: projects, tickets
with stable keys (`CND-12`), their discussion and their history
(`docs/plans/dispatch.md`). It is not the engine's verb: the engine
dispatches a managed run's tasks as attempts (`task.go`, above), which
the pages and the run log call scheduling, and that stays Yardmaster's.
Dispatch records what was asked for and what became of it; it starts no
process, opens no session and runs no check, and links to a run the
engine made rather than copying it, so the ledger stays the truth of
execution.

`internal/dispatch` is the store, with no HTTP: one SQLite database of its
own (`modernc.org/sqlite`, as the ledger's), opened by the server beside
the ledger with the ledger's pragmas and one connection, and no owner
lock: SQLite's own locking orders writers across processes, and the
schema is made inside the transaction that read its version. Every write
is a typed operation (`Apply`), one `BEGIN IMMEDIATE` transaction that
looks up the receipt kept under the actor and the op id the client
minted, compares the revision the client expects, makes the change with
one `changes` row per field it set, added or removed, bumps the entity's
`rev` once, keeps the receipt and commits, so a reply lost to the network
is asked for again with the same id and never makes a second change; a
refusal of the store's rules is a failed receipt kept alone. The
`changes` log is a ticket's history, and what replication (CS1) will ship
between replicas, merged by the field rules of `rules.go`. A read of more
than one statement (a ticket with its sets, comments, links and close
state; a list and its total) is one read transaction, so a reply
describes one state whatever commits meanwhile. The store's one sight of
the engine is `RunExists`, which the server gives it: a run link names a
run the engine holds or has a record of. A ticket's attachments are files
beside the database, one per content, named by its SHA-256
(`attachments.go`): an upload writes and hashes its file, holds the hash
with a lease (an `uploads` row) committed before the file is published
under it, and writes the row naming it, the lease dropped in the same
transaction, only once the file's new name is durable (on unix its shard
and then `attachments/` synced, on Windows the move written through). The
sweep at open (`sweep.go`) is the one thing that deletes a file: it drops
the leases older than an hour in a transaction committed before any
unlink, then deletes the files no live row and no lease names, under a
write lock it holds while it does, so an upload paused across another
server's sweep either finds its file named or is refused `expired`.

`internal/api`'s `dispatch.go` puts the store on the HTTP API: the admin's
routes alone, a switchyard's 403 (it opens no store), each write's body
an operation with its receipt in the reply, and replies written with HTML
escaping off, so the largest is bounded by its shape. After every commit,
outside the transaction and on the writer's goroutine, the store's
`OnChange` hands the server what the commit changed in one call, a
change per entity, and the event hub sends the browsers a `dispatch`
event for each, but one for more than 64 of a kind in a project, naming
none of them (a page re-reads what it shows of the project), the
commit's together as one entry of a client's queue, by the `run` event's
path: a client that cannot keep up is dropped and starts over from a
snapshot, never for one commit's size, and no webhook or sink sees it.

## Switchyard

A switchyard is the server in a mode (`config.Switchyard`): the signaling
hub, the host route, the share links, the join route, the events stream and
the workbench, with the launching routes answering `403 switchyard` and no
catalog or agents needed. Other Conductors publish their sessions to it
through the host protocol (`rendezvous`), `conductor host` does the same,
and viewers join by links minted there; the terminal goes over WebRTC
between the viewer and the publisher, through the switchyard's relay only
when ICE fails and the relay is on (`switchyard.relay`). The desktop app's
workbench, served from a loopback address, may join from its own page: the
join route answers the switchyard's allowed origins across origins and the
session WebSocket accepts them. What a switchyard carries is signaling and
the relayed terminals, so one small machine with a certificate serves many.
It is trusted with what is published to it: it mints and checks the links
and tells the publisher each viewer's role. A Conductor publishing there
(`hostagent.Options.Switchyard`, which the Uplink sets) tells it less: each
session's name, agent, folder's last element and machine name, status and
attention state, no chat or activity; takes no report from it; and ends the
publication, not the process, on its stop. In switchyard mode the workbench
token lists the published sessions and has no role on them
(`principal.yard`).

Its pages are its own (`internal/api/switchyard_page.go`): a server-rendered
landing page at `/` (what the server is, how a link looks, how a machine
publishes to it, a status card from the same facts as health, reach and the
certificate manager, and the operator's figures behind the workbench token
through `GET /api/switchyard/status`), a 404 page for every workbench path,
and the app only under `/join/` and `/paste` and for its assets. The pages
are Go templates with the brand tokens inlined, so a switchyard built
without the UI still answers. A `relayMeter` counts the bytes the relay
carried over the last hour for the operator's card.

## Desktop shell

`desktop/` is an Electron shell around the same binary: it starts
`conductor serve --listen 127.0.0.1:0 --print-listen --exit-on-stdin-close`
with the settings it keeps (`userData/settings.json`: data directory,
allowed roots, default directory, yolo, reach) as `CONDUCTOR_*` variables,
a workbench token minted for the run in the environment, and the login shell's
PATH so the agents are found; reads the handshake line from stdout; opens the
workbench in a window whose preload exposes the token through a
context-isolated bridge (`window.conductorDesktop`); polls the server's
health and restarts it with backoff; and stops it by closing its stdin when
the app quits. Navigation stays on the server's origin, other links open in
the system browser, and "Open in browser" carries the token in a URL
fragment the workbench takes and drops. On Windows the shell runs the Linux
binary inside a WSL 2 distribution. `desktop/README.md` has the layout.

On Windows the shell also runs a UDP forwarder in front of the server in
WSL (its default NAT mode left as it is): one port on Windows, each remote
peer carried to the distribution on a socket of its own, the server
advertising the Windows address (`ice.udpPort`, `ice.publicIp`). The shell
registers the `conductor:` scheme: an invite opens its own join page with
`?server=`, which signals to the switchyard named.

## Packages

| Package | Responsibility |
|---|---|
| `cmd/conductor` | entry point; `serve`, `host`, `notify`, `hooks`, `skill`, `up`, `crews`, `completion`, `version` |
| `internal/cli` | flag parsing, help and completion text (the completion table and scripts, the rc-file line); no business logic |
| `internal/config` | JSON config, `CONDUCTOR_*` overrides, validation |
| `internal/catalog` | launchable agents (argv arrays, never shell strings), their yolo, trust and session recipes |
| `internal/store` | atomic JSON documents in the data directory, and plain files such as the generated workbench token |
| `internal/agents` | hook adapters per agent: assets under `dataDir/hooks`, launch injection, on-demand install, payload mappers |
| `internal/crew` | saved crews, one file each in `dataDir/crews/`: members, role prompts, start conditions, validation; runs: member sessions through the server's launch path, git worktrees, readiness and the trust hold, prompts, start conditions, handoffs between members (an activity sink and a change hook of `internal/api`), run state, change reports, member resume |
| `internal/crew/ledger` | the managed runs' ledger (#334): `runs/managed.sqlite` in the data directory, opened beside the records with the owner lock `runs/owner.lock`; one connection, every transition one transaction committed before the side effect it precedes; the rows of runs, tasks, attempts, checks (with their logs), commands (receipts) and events; recovery of what the last owner left open; read-only, with managed launches refused, when another server holds the lock, the filesystem has no locks, the server may not write the database file, or the database is damaged or newer |
| `internal/dispatch` | Dispatch's store: projects, tickets, their discussion and history in `dataDir/dispatch/dispatch.sqlite`; every write a typed operation with a kept receipt and its rows in the `changes` log; no HTTP, no session, no process |
| `internal/proto` | frame codec and message structs (mirrored in `web/app/utils/protocol.ts`) |
| `internal/pty` | process start, resize, stop; environment allowlist |
| `internal/proc` | a bounded run of one command outside a PTY: argv, directory, timeout, the output's first and last bytes, the process group killed when the run ends; the managed-run checks' runner (#334) |
| `internal/session` | ring buffer, fan-out hub, `Local` session (submissions, attention, trust watcher, the agent's own session), registry, bounded file reads |
| `internal/share` | share tokens (random, hashed at rest) and revocation |
| `internal/signal` | hosted sessions: host connections, viewer brokering, relay |
| `internal/api` | HTTP routes, admin/share/host authentication, viewer and host WebSockets |
| `internal/hostagent` | `conductor host`: control connection, pion peers, relay, local terminal |
| `internal/notify` | `conductor notify`: reports from inside a session; hook payload mappers, the agent's own session id among what they read |
| `internal/web` | embedded SPA with index fallback |
| `web/` | Nuxt 4 + Nuxt UI 4 + xterm 6 workbench |

## Security model

- The workbench token protects launching, listing, stopping, link management and
  editing the agent catalog, which is as powerful as the server user: a saved
  agent's argv and env run as that user.
- Share links carry their own 256-bit token; only its SHA-256 is stored. A link
  grants `view` or `control` on exactly one session, or, for a run link, on
  every member session of its run, and can be revoked, which
  disconnects its viewers immediately; one that expires disconnects them as
  it expires. A viewer's link is checked again as the viewer attaches, under
  the session's lock, which a revoke takes after marking the link to close
  its viewers: a viewer attaching meanwhile is either refused or closed.
- Host tokens allow registering hosted sessions. A host never receives admin or
  share tokens.
- A plain connection to another machine is said out loud. `conductor host`
  refuses a plain `http://` (or `ws://`) server that is not this machine
  (`localhost` in lower case, `127.0.0.0/8`, `::1`: the hosts a proxy the
  environment names never carries; `config.PlainOffMachine`) before it
  writes or dials anything, unless `--insecure` says to go ahead, and then
  says on stderr and in its log that what passes travels in the clear and
  unverified; a redirect the dial follows is held to the same rule as the URL
  given. A server's own uplink follows it through its config: such a
  `rendezvous.server` is a config error unless `rendezvous.insecure`
  (`CONDUCTOR_RENDEZVOUS_INSECURE=1`), so `conductor serve` and `conductor
  switchyard` stop at start with the words, and the file is left as it is.
  The admin client (`conductor up`, `conductor crews`), which sends the
  workbench token with every request, refuses such a `--server` (or
  `CONDUCTOR_SERVER`) before any request unless `--insecure`. The desktop
  app's Settings page refuses such a switchyard as it is saved; settings
  saved with one before are kept, and the app starts its server without
  publishing until Settings names an `https` one.
- A server is named by its address alone. `rendezvous.server` (file or
  environment) and `conductor host --server` (`CONDUCTOR_SERVER`, read once
  the flags are) refuse a URL with user information, a query or a fragment
  (`config.ServerURLExtra`), publishing on or off: tokens have settings of
  their own. Logs, errors and replies show a server URL as its scheme, host
  and path (`config.ShownURL`), as the URL a hosted session's hooks report to
  is built; a failed control dial is said in the host package's own words
  (`dialFailure`), never the HTTP, TLS or WebSocket library's, which quote the
  URL dialed or what the server sent. The desktop app refuses such a
  switchyard as Settings saves it; one saved before is kept, never handed to
  the server, and Settings says why.
- Commands are argv arrays from the catalog; user-supplied extra arguments are
  appended element-wise only for agents that allow it. Server sessions get an
  allowlisted environment; `CONDUCTOR_*` from the server's own environment never
  reaches a child, and Conductor sets only the few a session needs itself.
- Yolo recipes are catalog data, applied as argv and through the filtered
  environment; they run agents without their permission prompts (Codex's
  without its sandbox) as the server's user. An agent session id is passed to
  an agent only as one argument, after matching its agent's pattern and
  beginning with a letter or a digit.
- Server session working directories must resolve under `allowedRoots` after
  symlink evaluation, and are opened through the root they lie under
  (`os.Root`): the process starts in the directory opened (on Linux by its
  descriptor; on macOS by its path, checked to name it just before the
  start), and the session keeps it open. File reads, listings, finds
  and saves are confined to that directory: a path is checked, then opened
  from it one element at a time without following links, so a link put on
  the way after the check leads nowhere. In
  server sessions they never reach the data directory, the config file or the
  catalog file, even when those lie inside it; a hosted session refuses the
  same files of the server on its machine (`~/.conductor` or the data
  directory the environment names, the files `--server-config` names, and
  the desktop app's directories).
- Frame sizes, viewer counts, session counts, scrollback and in-flight file
  requests are all bounded. Query strings (which may carry tokens) are never
  logged; a request is logged by the route pattern it matched (`/api/join/{token}`),
  or, on a catch-all route, by its path with a link's token replaced
  (`/join/{token}`). A workbench token the server generates is kept in
  `workbench-token` in the data directory (mode 0600, written atomically) while
  the server runs, and nowhere else but the `--print-listen` line; the
  terminal and the log name the file, never the token. A configured token
  removes the file. The server that keeps it holds a lock on
  `workbench-token.lock` beside it, so a second server started on the same
  data directory leaves it alone, and one of those that generates its own
  token stops unless it has `--print-listen`.

## Persistence

Sessions, links and crew runs remain in memory, while the data directory
(`dataDir`) holds UI-managed state as JSON files, saved crews included, and
the records of runs that ended (`runs/<id>.json`, the run as the API answers
it, 500 at most), which `GET /api/crews/{id}/runs` lists after the live runs. A
server restart ends server sessions and forgets links and runs (worktrees and
their branches stay on disk), and with them what Resume needs: an ended session
can be resumed while it is listed, a crew member while its run is kept. Hosted
sessions survive a brief server outage
through the host's reconnect-and-resume secret only while the server process is
alive.

A managed run (a crew with tasks, #334) has its truth in the ledger,
`runs/managed.sqlite`, beside the records: its record is a cache of it with a
revision, never counted among the 500, never pruned by the records nor marked
interrupted when they open, and never written back to an older revision.

Dispatch's store, `dispatch/dispatch.sqlite` beside the ledger, keeps the
projects, the tickets and their history across restarts: one SQLite file
and its write-ahead log, checkpointed into it as the server shuts down, and
the attachments' files beside it in `dispatch/attachments/`. A
ticket's delete is a tombstone that keeps its key, a project is archived
and never deleted, and the log keeps every change; only the receipts past
the newest 20,000 are dropped, the log still answering a resend of an
operation that ran. A database a newer Conductor wrote, or one that fails
its check, opens read-only: its reads are answered, its writes refused with
why, and it is never written.
