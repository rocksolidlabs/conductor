# Hosts: every machine this server knows, and who holds each run (Program item HS2)

The host overview is one page, **Hosts** (`/hosts`), that answers four
questions about the machines a person's work runs on: which machines are
there and can they be reached now, what is running on each, what waits
for a person there, and which server holds each managed run. It is
Program item HS2 (the Program, section 4.4), written against `main` at
`cf704db8` (2026-11-05) after reading how a host is known today
(`internal/hostagent`, `internal/signal`'s hosted sessions and the
switchyard, the host tokens of `internal/api/identity.go` and `conductor
host` in `internal/cli/host.go`, `GET /api/capabilities`), the ledger's
owner lock (`internal/crew/ledger`, `internal/store/lock_unix.go`), the
sync adapter (`internal/api/sync.go`, `ext/continuity.go`), the run page
(`web/app/pages/runs/[run].vue`), the sidebar's machine tags
(`web/app/utils/sidebar.ts`), and the plans this item meets
(`docs/plans/ext.md`, `docs/plans/history.md`, `docs/plans/decisions.md`,
`docs/plans/decision-inbox.md`). It is reviewed before any of its code;
the PRs at the end are cut into issues after the review, one issue, one
branch `issue-N`, one PR each (AGENTS.md).

The names follow the railroad (the Program, section 1.2): Conductor is
the application that shows the page; Yardmaster is the agent harness that
holds a managed run, and the only thing that schedules its tasks; a host
is a machine running `conductor host` against a server; the Switchyard is
the server in its hosted mode; a workspace is the paid tenant, whose
machines the page lists only when a sync provider is registered. An
attempt's checkout is a worktree, never a workspace (decision D5).

**What the page is not.** It shows where work is and who holds it. It
never offers, and never implies, starting, moving, retrying, continuing
or failing over work from one machine to another: a run stays with the
server whose ledger holds it, a hosted session with the host that runs
its process, and the page's only actions are links to a page that
already exists (the run page, a session, Needs your decision). Remote
managed-crew ownership (a run held by a host, `Crew.Where` `host`, which
`Launchable` refuses on `main`) is a later Yardmaster extension, and
automatic cross-owner migration or failover is out of scope near-term
(the Program, section 7).

**Two halves, one page.** A plain build of this repository, with nothing
registered and no network beyond its own, shows the machines this server
already knows: itself, the hosts hosting sessions through it, and the
switchyard it publishes to. That half is free and whole (articles 1, 2
and 4). A switchyard serves no workbench, so it has no Hosts page; its
landing page stays its view of the servers publishing to it. When a sync
provider is registered (Program item CS1) and current, the same page
gains a second section, **Other machines**: the workspace's other
servers, as the sync service last heard from them, the runs each
reported and the decisions waiting there, as the sync provider hands
them over. When the service, after being current in this server's life,
becomes unreachable or the subscription lapses, that section shrinks to
one line saying so and since when, and the rest of the page is the
first half, mid-session, with no restart (article 8). A provider that is
registered but still starting, or a person never signed in or signed
out, gives no section and no line: the reply and the page are a plain
build's (articles 7 and 8).

**Prerequisites: what is on `main` and what is not.** The Program gives
HS2 one prerequisite, CS1 (sync and continuity), which is Not started:
none of its rows is on `main`, and its plan is under review. What the
first half needs is on `main`: hosted sessions and their hub
(`internal/signal`), the host connection and its reconnect
(`internal/hostagent`), the switchyard and its publishers
(`config.Switchyard`, `config.Rendezvous`, `internal/api/publish.go`),
the ledger and its owner lock, the engine's runs (`GET /api/runs`), the
sessions' attention (questions waiting), the extension registry and
`GET /api/capabilities` (C1 to C9, `docs/plans/ext.md`), and C6's sync
adapter, which already hands a run's pointer to a sync store. What is
not on `main`:

- DR1's decision records and their routes (`docs/plans/decisions.md`,
  in implementation: its PR-1 is open as #664 and its PR-4, the routes,
  is issue #637) and MB0's Needs your decision page
  (`docs/plans/decision-inbox.md`, issues #646 to #653). The page counts
  the questions waiting on `main` from the start; DR1's open decisions
  join the count in PR-4 below, which waits for DR1's PR-4 and MB0's
  PR-4 (DR1's records drawn on Needs your decision, after MB0's PR-3 makes
  them its items): before those, a link to a decision's item would open a
  page that cannot show it.
- HS1's PR-12 (`docs/plans/history.md`, "what a host reports of its
  sessions"): the hosted session's `where` and its git facts. Not needed,
  and nothing here shows a `where`: no `session.Info` on `main` carries
  one (`internal/session/info.go`), and the API keeps no record of the
  Runtime a launch chose. The page gains a session's `where` with the row
  that reports it (HS1's PR-12 for a hosted session, C10's for a local
  one), not before.
- CS1's open-source rows (its public part will be
  `docs/plans/sync-seams.md`): the remote records other machines hold
  (`ext.Host.PutRemote`, `DeleteRemote`, `ClearRemote`, the data
  directory's `remote/<device>/<kind>-<id>.json`, left out of the
  export, `GET /api/remote`), among them the two kinds this page reads,
  `device` and `hosts`, and the sync status (`ext.SyncStatus`, `GET
  /api/sync`), which names this server's own device (`Device`,
  `DeviceName`), all in its OS-4 row. PR-5 below waits for them.
- CS1's hosted rows and ID1 (identity and workspaces): the sync service
  that keeps each server device's last report and publishes its `device`
  records, and relays a person's `hosts` document to that person's other
  devices; the provider that hands both kinds to `ext.Host`, uploads the
  summary and names its own device in the status; identity's devices
  with their names, platforms and versions. PR-6 below waits for the
  provider's row.

So PR-1 to PR-3 build on `main` as it is and ship the first half; PR-4
waits for DR1 and MB0; PR-5 waits for CS1's OS-4; PR-6, the premium
repository's checks of this page with a registered provider, waits for
CS1's provider row.

**This plan follows two plans in the other repositories**, CS1's (sync
and continuity) and ID1's (identity and workspaces), each still under
review. Both have answered what HS2 needs of them (Owner decisions, 11):
the sync plan in its third review round, identity in its amendment
after its review rounds. What they settled is stated in this plan as
settled, under their names for the records and doors it uses; the work
they took on (the sync service's device records, last reports and relay
of a host summary, and the provider's handing of them to `ext.Host`) is
theirs, and this plan does not repeat it. Where either changes again
before it is approved, this plan takes the change.

**The items this one meets, and who owns what they share.**

| Shared piece | Owner | This plan |
|---|---|---|
| `proto.HostSession`'s provenance fields and `session_info` | HS1, `docs/plans/history.md` (PR-12) | adds nothing to `HostSession`; adds `machine` and `platform` to `proto.HostInfo` and keeps the `version` the server drops today (PR-1). Both touch `internal/proto`, `internal/hostagent` and `internal/signal`: whichever merges second rebases on the other, never beside it |
| The decision record, its routes and states | DR1, `docs/plans/decisions.md` | counts a run's open decisions through DR1's reads and links to them; adds no field (PR-4) |
| Needs your decision (`/decisions`) and its items | MB0, `docs/plans/decision-inbox.md` | links each waiting count to MB0's page; answers nothing on its own page |
| The sync seam (`ext.ContinuityStore`, `ext.Doc`, the adapter's allowlist) | C6, `docs/plans/ext.md` | adds one document to the allowlist, the host summary (PR-5) |
| The remote records, their doors and files, the sync status | CS1 (its OS-4, which takes the kinds `device` and `hosts` and the status's own device) | reads them; defines `ext.HostSummary`, the `hosts` record's body, and makes that kind's `Valid` check it (PR-5) |
| A workspace's devices: names, kinds, platforms, versions, revocation | ID1, and CS1's service for each device's last report | reads them only from the `device` records a registered sync provider hands over (Owner decisions, 11) |
| `GET /api/switchyard/status` and the switchyard's landing page | the switchyard (`internal/api/switchyard_page.go`) | unchanged: a switchyard is never sent a machine key (PR-1), so its grouping by `HostName` stays as it is |
| Who holds a run's decisions while its owner is away | CS1 (the managed copy) and MB1 | shows which device reported each remote run and when; never offers to answer or apply elsewhere, and links to no answer |

## Articles it serves

The articles are the Program's (section 2.1).

1. **No premium code and no licence check.** The page's second section
   appears when `sync` is in `GET /api/capabilities` and the sync status
   says current, which is asking whether a provider is registered and
   what it reports; nothing here reads a plan, a tier or an entitlement.
2. **The open-source build is fully useful on its own.** The first half
   (this server, its hosts, its switchyard, every run it holds and every
   question waiting) is the whole page for a plain build, with nothing
   registered and no network.
3. **Paid features are gated by hosted dependencies.** Seeing other
   machines needs the sync service, which is what carries their records:
   no flag, setting or build tag shows the second section, and a fork
   that set one would have nothing to show in it.
4. **Stop, inspect, revoke, export, required approvals.** The page is an
   inspect surface and is free in full; it adds no control, so none can
   degrade. Stop, pause and the rest stay on the run page and the
   session, untouched. The machine id never leaves the machine (I6): no
   export carries it and no restore plants it (PR-1); export leaves out the
   remote records, which belong to the provider (CS1's OS-4). A decision
   waiting on another machine is shown, never answered or approved from
   here, and the page links to no answer.
5. **Dependencies one way.** No new module; `ext` gains record kinds and
   one record, standard library only; the cloud reads the open-source
   `ext` types at a tag.
6. **No feature build tag.** None added. `internal/hostagent` reads
   whether it runs under WSL from a file, on every platform; no new tag.
7. **The free default is `main`.** With no provider, the page is the
   first half and the sync adapter's allowlist gains a document that
   `local.Continuity` drops like every other. A registered provider adds
   the second section; it removes nothing.
8. **An unreachable or lapsed provider answers as the free default.**
   The page reads the sync status from memory through the guard (CS1's
   `ext.SyncStatus`); anything but current shows the first half alone,
   and the section returns at the next current status with no reload of
   the server. The host summary is queued like every sync document and
   never waits on the network. The premium PR (PR-6) carries the outage
   and lapse tests.
9. **`CGO_ENABLED=0`.** No cgo, no new store: the machine id is one file,
   the remote records are CS1's files, the recently seen hosts are held
   in memory.
10. **The repository contract.** This plan before code; one issue,
    branch and PR per row; race tests per package; the protocol change
    (two fields of `HostInfo`, the host upgrade's `Conductor-Server-Key`
    header, the session's host fields, one route, one sync document) in
    `internal/proto`, `web/app/utils/protocol.ts` (with the app's session
    type, `SessionInfo` in `composables/useSessions.ts`, for the
    session's fields) and `docs/protocol.md` together, each with its
    bound and test; no new dependency, so no `make notices`.
11. **Names.** "Host" keeps its meaning (a machine running `conductor
    host`); "Hosts" names the page that lists every machine, servers
    among them, as the sidebar's machine tag already does; "holds" is the
    word for a run's owner. Nothing here is called graph or continuity.
12. **Execution behind one seam.** Not touched: nothing starts a
    process. The page shows no `where` until a session reports one
    (Boxcar constraints, 7), and assumes none.

## Why and what happens

Conductor must make it easy to answer what is running, waiting, failed or
ready for review, and who is responsible (the Program, section 1.1). A
person who runs agents on a Mac, a Windows machine through WSL and a
Linux box today sees each hosted session as a row with a machine tag in
the sidebar (`hostName` first in its meta), and a run's page with no word
about which server holds it. Nothing shows the machines themselves:
whether the Mac's host is still connected, when the WSL host was last
seen, how many questions wait on the Linux box, or that a run is held by
this server and not another one on the same data directory.

After PR-3 (the first half), opening **Hosts** from the sidebar, the
rail, the phone's bottom bar or the palette (`go hosts`) shows:

- **This server**, first: its name (the machine's host name), its
  platform (`darwin/arm64`, `windows/amd64`, `linux/amd64`, with `WSL`
  when the server runs inside WSL), its version, **Here** as its
  availability, its own sessions (running, and the questions waiting
  among them), and the managed runs it holds.
- **Hosts connected to this server**, one card per machine (the machine
  key of PR-1 groups a machine's `conductor host` processes, so a Mac
  hosting three sessions is one card): name, platform, version and the OS
  user as the host reported them, its availability (**Connected** while
  any of its sessions holds a live link, **Away since 14:02** once none
  does and one waits in its 60 s grace, **Last
  seen 13:40** for a machine that left within the last 24 hours of this
  server's life), its sessions and questions waiting, each a link to the
  session. A host's card says **Holds no runs**: a host runs sessions,
  and only a server's ledger holds a managed run.
- **The switchyard** this server publishes to: its address as the
  configuration names it, whether this server's publications are
  connected, how many sessions are published.
- **Runs and who holds them**: every open managed run on this data
  directory (one a restart interrupted, not yet continued, among them,
  as its row says), its crew, its state and its tasks waiting for a person,
  under its holder as it is when the page asks: **Held by this server**;
  **Held by this server, read-only** with the ledger's reason (a damaged
  database, one a newer Conductor wrote), where the run page refuses
  every control; **Held by another server on this data directory** with
  the pid and start time that process wrote in the lock file, said as
  what it wrote, the runs then read from the database that server writes
  and their waiting counts said to be counted there; or **Held by no
  server** with why (this one stopped waiting for the lock after its
  10 s, or the filesystem has no locks), until a server takes it; or
  **Not known who holds these runs** when the lock file cannot be read,
  with why. Each says only what is true when the page asks: a former
  holder's line is never shown once it has let go. The run
  page's controls are where they are today (a read-only server refuses
  them as `ledger_unavailable`).

Each count of questions links to Needs your decision once MB0's page is
on `main` (PR-4), and DR1's open decisions join the count. The page's
footer says, in one line: "Runs stay with the server that holds them;
nothing on this page moves or starts work."

After PR-5 (the second half), with a sync provider registered and
current, the page gains **Other machines**: each other server of the
workspace that the sync service lets this one see, with its name and
platform from the workspace's device record, **Last reported 14:05** (the
time the sync service last heard from it, as the device records last
carried it, which can be some minutes behind; never "online"), and below
it:

- for the person's own other servers, the summary that server last sent
  (its hosts, their availability as it saw them, its counts of sessions
  and questions);
- for every server, the runs sync shares with this one, each as
  **Reported by studio at 14:05** (the device that sent the run's pointer,
  and when): nothing across machines records a run's holder, so the page
  never calls a remote device one; with the decisions waiting on it
  there, counted from the decision copies sync shares, each of which
  names the run that holds it now. Each
  is read only, with no link and no control: answering another machine's
  decision is the sync service's and MB1's, not this page's.

When the status, after being current in this server's life, is
unreachable, behind past its bound or lapsed, Other machines is one
line: "Other machines are not shown: the sync service has not answered
since 14:02" (or "the subscription has lapsed"), and the rest of the
page is unchanged. Signed out, never signed in, or a provider still
starting: no section and no line.

## The changes

### `internal/machine`, `internal/proto`, `internal/hostagent`, `internal/signal`, `internal/session` (PR-1): what a host says of its machine

- `proto.HostInfo` gains two optional fields:
  - `machine`: a key for the host's machine that only this server can
    use, 32 lowercase hex digits: HMAC-SHA-256 of the machine's id keyed
    by the server's key (below), its first 16 bytes. No server is ever
    sent the machine's id itself.
    - **The machine's id** is random (`crypto/rand`, 32 hex digits), never
      a hardware or OS identifier, and kept per user, outside the default
      data directories: `<os.UserConfigDir()>/conductor/machine-id`
      (`~/.config/conductor` on Linux and inside WSL, `~/Library/Application
      Support/conductor` on macOS). So a `conductor host` started in a WSL
      shell and the desktop app's server in the same distribution (whose
      data directory, `~/.local/share/conductor/data`, is set only in the
      server's environment) read one file. Each WSL distribution has a
      home of its own, so each is a machine of its own, and the card says
      `WSL`.
    - **Made once, agreed by every maker.** The file (0600, its directory
      0700) is published by `os.Link` from a fully written temporary file
      beside it, which never replaces a file that is there: of two
      processes that find none, one link wins, and the other removes its
      temporary file and reads the winner's id, so both report one id. A
      file that cannot be read or does not match the pattern, or a
      filesystem that refuses the link, means none reported, and a
      warning once. Whatever data directory a server is given (`dataDir`
      and `CONDUCTOR_DATA_DIR` take any, `ResolveDataDir`), this
      directory or one holding it among them, no export carries the file
      and no restore plants it (below), so an archive restored on another
      machine never gives two machines one id.
    - **The server's key** is 32 random bytes made at each start, in
      memory only. The server sends it, hex, in the `Conductor-Server-Key`
      response header of an upgrade of `GET /ws/host` it accepts (a valid
      host token), set before `websocket.Accept`; the host reads it from
      the dial's response and registers with the key it computes. An
      upgrade without the header (an older server, a proxy that drops
      it) means none reported. So one machine has a different key at
      every server and a new one at each restart of a server: two
      servers' session lists cannot be joined by it. The server computes
      its own row's key from its own machine id, read once at start in
      the composition root (`cli.runServe`, never in a route), through
      the one function the hub keys a host by:
      `machine.Group(machine.Key(id, serverKey))`, the key its hub keeps
      for a host of its own machine. So its row and a host on the same
      machine hold one key, made by one function and compared as equal
      strings (I7).
    - **Never to a switchyard.** A registration to a switchyard
      (`Options.Switchyard`, and the uplink of a server publishing there)
      leaves `machine` out, as it leaves out the OS user and the branch
      today (`internal/hostagent/agent.go`, `registration`).
    - **What the key does not prove.** A host holding a host token can
      send any key, and so put its sessions on another machine's card.
      The key groups what the page shows and decides nothing: each
      session on a card is its own host's, controlled as today.
    - **Where the id is kept from** (I6 lists every way it could leave
      and the test that holds each). *A session's files*: the deny lists
      of `internal/config` gain `machine.Dir()` beside `~/.conductor` and
      the desktop app's directories, both a server's (`machineFiles`,
      under `FileDeny`) and `conductor host`'s (`LocalFileDeny`), so no
      reader of a session's files (a view link, which may read files by
      default, the Files tab) can read the file of a session rooted at
      the home, though Conductor reads as the file's owner and 0600 does
      not stop it. *The export*: `internal/export`'s `leftOut` gains a
      name row, `machine-id` at any depth, with its own reason
      (`WhyMachine`, "this machine's id: another machine must never hold
      it"), so an export leaves it out and a restore refuses an archive
      holding one, whatever directory was chosen. *A log or an error*:
      `internal/machine` names a bad file by its path and why, never its
      bytes, and nothing logs the id or a key a host sent.
  - `platform`: `<GOOS>/<GOARCH>`, with `+wsl` when the host runs in
    WSL, at most 32 bytes, in the pattern
    `^[a-z0-9]{1,15}/[a-z0-9]{1,15}(\+wsl)?$`. WSL is read once from
    `/proc/sys/kernel/osrelease` (a release containing `microsoft`,
    case-folded); no build tag (the file is not there off Linux).
  - Both are read by a new package, `internal/machine` (standard library
    only): `machine.Dir() (string, error)` (`os.UserConfigDir()`'s
    `conductor`), `machine.ID(dir) (string, error)`, `machine.Key(id
    string, serverKey []byte) string` (what a host sends),
    `machine.Group(sent string) string` (what a server keeps and serves
    for a key sent: the first 16 bytes of SHA-256 of `"conductor host
    machine\x00"` and the key, 32 lowercase hex digits; the only place
    that hash is made, called by the hub for a host and by the
    composition root for the server's own row) and `machine.Platform()
    string`, the last through a seam a test gives its own release
    string, and `ID` taking its directory so a test gives its own.
    `internal/hostagent` and
    `internal/api` both use it (`internal/api` does not import
    `internal/hostagent`, and neither should for this);
    `docs/repository-map.md` gains its row.
- `signal.Hub.Register` checks both at their bounds (a register past one
  refused with `ErrBadRegister`, as `Name` and `User` are), keeps
  `HostInfo.Version` (dropped today; at most 64 bytes, the pattern of
  `version.Version`), and keys the session. `hostKey` is always 32
  lowercase hex digits: `machine.Group(machine)` when the host reports
  one (`hostKeyFrom` `machine`), the function the server's own row's key
  is made by; otherwise `ownerOf(instance)`, the 32-hex hash the hub
  already keeps of the host's instance (`process`: one process); and for
  the oldest hosts, which send no instance either (`Hub.Register` leaves
  `owner` empty), a hash of the session's own id (`session`), so two
  such hosts never share a key. The key the host sent is never served.
  The three are hashed under three different prefixes, so a key from a
  process or a session never equals a machine's, nor this server's own.
  The card of a key not from a machine says "one process; this host does
  not report its machine" (or "one session").
- `session.Info` gains `hostPlatform`, `hostVersion`, `hostKey`,
  `hostKeyFrom` (`machine`, `process` or `session`), `hostLink` and
  `hostAwaySince`, each omitted when empty. `hostLink` is the hosted
  session's host connection as it is, apart from its status: `up` while
  the session holds its host's control connection (`conn` set), `down`
  once `HostDisconnected` has let it go; `hostAwaySince` is the private
  `disconnectedAt`, set exactly while `hostLink` is `down`. Both are read
  in `Info` under the session's lock, from the connection itself, never
  from the status or the time alone: an ended session (`exited`,
  `stopped`) keeps its status when its host leaves (`HostStatus`,
  `HostDisconnected`, `internal/signal/hosted.go`), so the status cannot
  tell its host there from its host gone, and a reattached session keeps
  an old `disconnectedAt` (`attach` sets the status back to running
  only). They are in `GET /api/sessions` and the `session` event as
  every `Info` field is. The web app's session type, `SessionInfo` in
  `web/app/composables/useSessions.ts` (what the REST list and the event
  stream are read into, and `useAttention.ts` and the sidebar import;
  `protocol.ts` has no session type), gains them, and so does
  `docs/protocol.md`'s session table.
- `api.PublishedSession` gains `Connected() bool`, true while the
  publication's connection to the switchyard is up (`hostagent.Published`
  answers from its agent's connection state under its lock), so PR-2
  can say whether this server's publications are connected without a
  round trip.
- `docs/protocol.md`'s "Host control connection" lists the two fields
  and the upgrade's `Conductor-Server-Key` header (64 hex digits) with
  their bounds. `protocol.ts` has no register type: it declares the
  register's optional parts one by one (`HostIdentity`,
  `HostAnswerAsInfo`), so it gains a new declaration beside them,
  `HostMachine` (`machine`, `platform` and the `version` kept), with its
  bound constants (`HOST_MAX_MACHINE`, `HOST_MAX_PLATFORM`,
  `HOST_MAX_VERSION`).

### `internal/api`, `internal/crew/ledger` (PR-2): `GET /api/hosts`

A new file `internal/api/hosts.go` builds the model the page draws from,
from what the server already holds, under each holder's own lock, read
once per request; nothing is persisted.

- **Route.** `GET /api/hosts`, admin (`requireAdmin`): the workbench token
  or an identity with the admin grant, as `GET /api/runs` takes. Not on
  Brakeman's shared reads and never through a share link. On a
  switchyard it answers 404 `not_found`: a switchyard serves no
  workbench page (`switchyardFront` serves the app only for joining and
  pasting), and its landing page stays its view of its publishers.
- **Reply.**

  ```json
  {
    "at": "2026-11-05T14:05:00Z",
    "this": {"key": "…", "kind": "server", "name": "linuxbox", "platform": "linux/amd64",
             "version": "v0.7.0", "availability": "here",
             "sessions": {"running": 3, "waiting": 1}},
    "hosts": [{"key": "…", "kind": "host", "name": "studio", "platform": "darwin/arm64",
               "version": "v0.7.0", "user": "ana", "availability": "connected",
               "since": "…", "sessions": {"running": 2, "waiting": 1},
               "sessionIds": ["…"], "keyFrom": "machine"}],
    "switchyard": {"url": "https://…", "publishing": 2, "connected": 2},
    "runs": [{"id": "…", "name": "…", "crew": "…", "state": "running", "paused": false,
              "waiting": 1, "heldBy": "this"}],
    "ledger": {"holder": "this", "note": ""},
    "more": {"hosts": 0, "runs": 0}
  }
  ```

  - `kind` is `server` (this server) or `host` (a machine hosting
    sessions here); `keyFrom` is the key's `hostKeyFrom` (PR-1).
  - `availability` is `here` (this server); `connected` (at least one of
    the machine's listed hosted sessions has `hostLink` `up`, whatever its
    status: an ended session whose host is still connected keeps its
    machine connected); `away` (none has, and at least one is listed with
    `hostLink` `down`, which `Expire` removes after its grace whatever its
    status; `since` the latest `hostAwaySince` among them, the moment the
    machine's last link dropped); or `seen` (no session left; `since` when
    the last one went, from the recently seen table below). It is made
    from `hostLink` alone, never from a session's status.
  - A host row is built from the registry's list (`session.Info` with a
    `hostKey`, the list `GET /api/sessions` serves), never from the
    hub's own map, which keeps a removed session until its next
    `Expire` (I10).
  - `sessions.running` counts the machine's sessions that are starting,
    running or away; `waiting` those that wait for a person by one
    predicate, `waitsForPerson(info)`, the only one the page, its links
    and the host summary (PR-5) count by: the session not ended, its
    attention `needs_input` and its `Answered` empty. A question that
    keys typed in its terminal answered keeps `needs_input` until the
    screen shows it gone, and waits for no one: the engine's `refresh`
    (`internal/crew/run.go`) and the Yard (`liveAttention` before
    `needingInput`, `web/app/utils/attention.ts`) leave it out the same
    way (#258). From PR-4 the predicate also asks `Attention.Decision`
    empty: a decision's mirror is counted as its decision (I8).
  - `runs` lists the open managed runs on this data directory, newest
    first, and `ledger` says who holds them, decided each time a reply is
    built, from the ledger the server opened (the one its engine was
    given). The list is the ledger's, for every holder (I1): a new
    bounded reader, `Ledger.OpenRuns(ctx, n)`, gives the runs whose rows
    are open (state `running`, or `interrupted` with no `continued_by`:
    a run a recovery interrupted, whose continuation is not made;
    newest first, at most n, and the count of the rest), read first;
    the engine's snapshot then stands in for each row it holds, and adds
    a run it holds open that was launched after the read; it never hides
    an open row it does not hold. The engine holds only what it
    launched or continued in this process: `Ledger.Recover` marks a run
    its owner left `interrupted` without closing it and launches
    nothing (`internal/crew/ledger/transition.go`), and `Engine.List`
    lists only its own order (`internal/crew/run.go`), so after a
    restart, or a lock taken late, the open rows are on the page only
    through the ledger.
    - The ledger is this server's and writable (`ReadOnly` false):
      `holder` `this`. A run the engine holds is its snapshot (`state`,
      `paused` and `waiting` as `engine.List()` gives them, `waiting`
      its members' sessions that wait by `waitsForPerson`, and from
      PR-4 its open decisions), and one it lists as final (finished or
      stopped since the read) is left out; a run only the ledger has
      (interrupted, or recovered
      after a restart or a late lock) is its row as committed, with
      `waiting` 0 before PR-4, since none of its attempts runs in any
      process and a question goes with its attempt, and its open
      decisions from PR-4.
    - This server holds the lock but its ledger is read-only (a new
      reader, `Ledger.Lock()`, below, says owned and read-only: a damaged
      database, a newer schema, a file it may not write): `holder`
      `this_read_only`.
    - Otherwise `ledger.Probe(runsDir)` is asked at that moment (it tries
      the lock and lets it go at once, never making the file; it takes
      the place of `ledger.Held`, `internal/crew/ledger/snapshot.go`,
      whose callers read its `State`), never the state the ledger's last
      try left, which goes stale once `awaitLock` stops after `LockWait`:
      `other`, `none` or `unknown`, as the table below says.

    **The lock's states, and what the reply and the page say in each.**
    `note` is made by `hosts.go` from structured facts read for that
    reply, never from a string the ledger or the lock code kept:
    `TakeOwnerLock`'s reason (`internal/store/lock_unix.go`) quotes the
    holder of the moment it was made, and `awaitLock` keeps that reason
    unchanged when it gives up (`internal/crew/ledger/ledger.go`), so
    after that holder left it would still claim it holds.

    | State | Told, for this reply, by | `holder` | `note` | The page |
    |---|---|---|---|---|
    | Held here | `Lock()`: owned, writable | `this` | empty | **Held by this server** |
    | Held here, read-only | `Lock()`: owned, read-only | `this_read_only` | `Lock().Why`, the reason the ledger gives for its own database, set only while it owns the lock | **Held by this server, read-only** and the reason |
    | Held by another server | not owned; `Probe` finds the lock held and reads the holder's line in that same try | `other` | `pid 4242 since 2026-11-05T13:00:00Z` when the line is in that form (`^pid [0-9]{1,10} since ` and an RFC 3339 time), otherwise "its line is not readable"; at most 200 bytes | **Held by another server on this data directory**, "pid 4242, since 13:00, as that server wrote" |
    | Held by no server | not owned; `Probe` finds the lock free, or the filesystem without locks | `none` | from `Lock()`'s structured reason alone: "this server stopped waiting for the lock at 13:02:10" (`GaveUpAt`) or "the data directory's filesystem has no locks" (`NoLocks`) | **Held by no server** and the why |
    | Not known | not owned; `Probe` fails (the lock file cannot be opened or read) | `unknown` | "the lock file could not be read" and the error's class (permission, missing, other), never its text | **Not known who holds these runs** and the why |

    `Ledger.Lock()` returns `{Owned, ReadOnly bool; Why string; GaveUpAt
    time.Time; NoLocks bool}`, read under the ledger's lock: `Why` only
    while `Owned`; `awaitLock`, where it gives up today, records
    `GaveUpAt` or `NoLocks` beside the `why` it keeps for its existing
    readers, and a lock taken later clears both. `ledger.Probe` returns
    `{State (held, free, no_locks), Line string}` and an error, `Line`
    read only in a try that found the lock held.

    For every holder but `this`, the engine holds no managed run (a
    read-only server refuses managed launches with 503
    `ledger_unavailable`, and a run enters the engine only by a launch,
    `internal/crew/run.go`), so `runs` are the same `Ledger.OpenRuns`
    rows, read from the database the ledger opened read-only, each with
    its state and paused flag as committed and `waiting` `null`: a run's
    attention is in the memory of the process that holds it, and the
    page says "counted on the server that holds it". A ledger with
    nothing to read gives no runs, and the page says so. `heldBy` on
    each run is `holder`.
  - The page shows a `note` from the lock file "as that server wrote
    it": nothing trusts it (`internal/store/lock_unix.go`). The probe is
    the only lock try the page causes, let go at once; a server waiting
    for the lock (`awaitLock`, a try every 250 ms) that loses one try to
    it takes the lock at its next.
  - `switchyard` is present when this server publishes
    (`config.RendezvousServer()` non-empty): `url` is `Publisher.Server()`
    (scheme, host and path, never user information, a query or a
    fragment, as it is shown today), `publishing` the sessions
    published, `connected` those whose `Connected()` is true.
- **Bounds.** The whole reply at most 256 KiB (`hostsReplyMax`),
  measured as encoded. Each string has its bound (a name 120 bytes, a
  platform 32, a version 64, a user 64, a note 200, a run's name and
  crew 120 each, a `waitingRef` 512 from PR-4), a row's `sessionIds` at
  most 64, and the rows are added
  in order (this server; hosts by availability, then newest; the
  switchyard; runs newest first) while their encoded size, counted row
  by row as `json.Marshal` writes it (escapes included), stays within its
  half's budget: 192 KiB for this first half, 56 KiB for `other` (PR-5),
  the rest for the envelope. A row past its budget, or past 256 host
  rows or 500 runs, is left out and counted in `more` (`hosts`, `runs`,
  and PR-5's `machines` and `remoteRuns`), so the counts say exactly what
  was left out.
- **Recently seen.** `hostsSeen`, a table in memory keyed by machine key:
  name, platform, version and the time the machine's last session left.
  The hook is the registry's `OnRemove` (`internal/api/server.go`),
  which every removal of a session reaches: `Hub.Expire` after
  `DisconnectGrace` (through `registry.Remove`), `DELETE
  /api/sessions/{id}` (`registry.Remove`) and the retention sweep of
  ended sessions (`removeIf`); `Hub.OnChange` reports no removal.
  `hostsSeen` has its own lock: the removal hook, under it, records the
  machine only when the registry then lists no session of that key, and
  a register's `OnChange`, under it, drops the key's entry, so of a
  register and a last removal the second decides (`Hub.Register` adds
  to the registry before its `OnChange`). The registry decides, never
  the hub: `Registry.Remove` drops its own entry before it calls the
  hook, while the hub keeps its entry of an ended session until its next
  `Expire`, which then finds the registry without it and calls no hook
  (`internal/session/registry.go`, `internal/signal/hub.go`), so a check
  of the hub's map would record nothing for a machine whose last session
  was deleted or swept. The reply also drops an entry whose key has a
  listed session. At most 64 entries, the
  oldest dropped first, an entry dropped after 24 hours or when the
  machine connects again; empty after a restart, said on the page
  ("since this server started at 09:12").
- **Names.** This server's `name` is `os.Hostname()` (at most 120 bytes,
  the hub's bound for a host name), its `platform` `machine.Platform()`,
  its `key` `machine.Group(machine.Key(its id, its server key))` (PR-1),
  computed at start: the key its hub gives a host of its own machine, so
  the comparison below is of two strings made by one function (I7). A
  host machine's name is the newest of its
  sessions' `HostName`, its user the newest `HostUser`; two sessions of
  one machine that disagree show the newest and the card says
  "reported as 2 names". A host whose key is this server's own runs on
  this server's machine: it is no card of its own, and `this` gains its
  sessions as `hosted` (the same fields as a host row's `sessionIds` and
  counts), drawn on This server's card as "Hosted from this machine"
  (`TestHostsHostOnThisMachine`).
- `docs/protocol.md` gains the route's row with its fields and bound;
  `web/app/utils/protocol.ts` the `HostsReply` type.

### `web/app` (PR-3): the Hosts page

- `pages/hosts.vue` (route `/hosts`): three sections, **This server**,
  **Hosts** and **Runs and who holds them**, and the footer line of Why
  and what happens. It reads `GET /api/hosts` on open and again,
  debounced to once a second, on the `session`, `removed` and `run`
  events the layout's attention store already streams (`useAttention.ts`,
  `/api/events`), and every 30 s for the `seen` times; no new event and
  no polling of its own beyond that. **One read at a time**: a refresh
  asked while a read is under way is queued (at most one) and starts when
  that read ends, so replies arrive in the order their reads began; and
  each read carries the page's generation, bumped when the workbench
  token changes, a reply of an older generation dropped (as the run
  store's tickets drop an answer older than one applied,
  `useAttention.ts`). So the page never shows a reply older than one it
  has shown, and a slow `current` reply cannot bring Other machines back
  after a newer `lapsed` one.
- `components/HostCard.vue` (a `UCard`): the name, the platform as words
  (`macOS · arm64`, `Windows · x64`, `Linux · x64 · WSL`), the version,
  the user, the availability as a `UBadge` (**Here**, **Connected**,
  **Away since 14:02**, **Last seen 13:40**), the counts, each session as
  a link to `/sessions/<id>`, and the run line (**Holds no runs** for a
  host). The brand palette of `web/app/app.config.ts`: `success` for
  here and connected, `warning` for away, `neutral` for seen; the
  junction mark is never a status light (`docs/design/brand.md`).
- `components/HostRunRow.vue`: a run's name linking to `/runs/<id>`, its
  crew, its state badge (`SessionStatusBadge`'s words), its waiting
  count (or "counted on the server that holds it" for `null`) and its
  holder's words (**Held by this server**, **Held by this server,
  read-only**, **Held by another server on this data directory**, **Held
  by no server**, **Not known who holds these runs**) with the note, drawn
  from the reply it was given and nothing it kept from an earlier one. No
  other control: no Stop, Continue,
  Retry, Move or Launch on this page; they live on the run page.
- `utils/hosts.ts`: the pure model (the platform's words,
  `platformWords`, the availability's words and order, the holder's
  words, the sort: this server, then connected, away, seen, each by
  name), the one-read-at-a-time reader with its queue and generation,
  the debounce, and `remoteSection` (PR-5). It imports no Nuxt module,
  so the Playwright spec can import `platformWords`.
- The nav: `layouts/default.vue`'s `nav` gains **Hosts**
  (`i-lucide-server`, `to: '/hosts'`) after **Events**, its rail label,
  its chord; `BottomBar.vue` lists it under More; `utils/palette.ts`'s
  `PAGES` gains `hosts` and `usePaletteRows.ts` its row.
- `docs/features.md`: the entry **Hosts: the machines this server knows,
  and who holds each run**, with the by-hand observation of Acceptance,
  part A.

### `internal/api`, `web/app` (PR-4): the decisions waiting

- `hosts.go` counts what waits for a person on each run as distinct
  items (I8): the run's open DR1 decisions and its members' questions
  that no decision mirrors.
  - **A decision counts on the run that holds it now.** The read is
    DR1's `Ledger.Decisions` with the filter state `open` and current
    run the listed run's id: `current_run`, never `run_id`, which only
    names the run that opened it. A continuation moves its open and
    answered decisions to itself (`Tx.MoveDecisions`), so a gate a
    continuation inherited counts on the continuation, which waits for
    it, and on no predecessor (`docs/plans/decisions.md`, the record and
    its readers). One read per listed run of this server, each within
    one page, since a run holds at most 64 open decisions; a run held
    by another process keeps `waiting` `null`, as in PR-2.
  - **A mirror is told by its session, never by a field the record
    lacks.** DR1 mirrors an agent's decision into its session as a
    question and serves the association in the session's attention,
    `Attention.Decision` (`dr-<n>`, in `session.Info`); the mirror's
    question id lives only on the engine's in-memory copy of the
    decision, and no durable column keeps it. So PR-2's predicate
    `waitsForPerson` asks `Attention.Decision` empty: a session whose
    question names a decision is never counted as a question, whatever
    that decision's state, and the decision is counted from its record
    when it is open. A decision open with its mirror showing counts
    once; answered but not yet applied, its mirror still showing, it
    counts 0 (the record is not open, the mirror is no question);
    applied, 0. The engine's own `NeedsInput` counts a mirror as a
    member waiting, so a run's count here is made from its members'
    sessions by the predicate, not taken from it; the way MB0 drops the
    question item a record replaces
    (`TestADecisionRecordReplacesItsQuestion`).
  - This server's `decisions` (the open decision records of the runs it
    holds, by `current_run`) sit beside `sessions.waiting`, which leaves
    out the mirrors by the same predicate. A hosted session's question
    stays a question (DR1: a managed run's attempts are server sessions,
    so no decision record is ever a host's).
- **The reply carries each link's target**, `waitingRef`, made in the
  same pass as the count it belongs to, so the page derives no target
  and needs no other read. Each count (this server's `sessions`, each
  host row's `sessions`, each run) gains `waitingRef`: the ref of MB0's
  item (`docs/plans/decision-inbox.md`, Item identity) when exactly one
  thing waits there, empty otherwise. A lone open decision gives
  `decision:<id>`, from the DR1 record the count read by `current_run`,
  so a gate, an inherited one among them, has its target with no
  session to recover it from; a lone question that no decision mirrors
  gives `question:<session>/<question>`, from that session's attention;
  a mirror is counted and linked as its decision, the `Attention.Decision`
  its session names, never as the question MB0 leaves out, and a mirror
  of a decision no longer open is counted and linked nowhere (I8). At
  most 512 bytes (`MaxRef`, MB0's bound for a ref), counted in its row's
  encoded size (PR-2's Bounds).
- The page links a count to `/decisions?item=<waitingRef>` (encoded)
  when the ref is set, to `/decisions` when more than one waits, and
  nowhere at 0. It answers nothing itself.
- Tests below; `docs/protocol.md`'s row and `protocol.ts`'s `HostsReply`
  gain `decisions` and `waitingRef`.

### `internal/api`, `ext`, `ext/exttest`, `web/app` (PR-5): other machines

- **The host summary, a sync document.** One document, store `hosts`,
  path `summary`: the first half of `GET /api/hosts` reduced to what may
  leave the machine: this server's platform and version, its counts
  (sessions running and waiting, runs held and waiting), and each host
  row's name, platform, version, availability, `since` and counts, at
  most 32 rows and 16 KiB (`ext.MaxHostSummary`), every count made by
  PR-2's predicate and run list (I1, I8). Never a session's id,
  command, folder, branch, a run's name or anything a terminal printed;
  never the OS user, the machine id or a machine key. Its type is
  `ext.HostSummary` (standard library only, with a `Valid` method, as
  every record a provider is handed has). With no provider
  `local.Continuity` drops it like every document.
- **How the adapter carries a document no commit makes.** The adapter
  today delivers only what a store committed, read at delivery from
  where it was committed (`store.OpenDoc`, `ledger.Pointer`, Dispatch).
  The summary has no commit and no store, so the adapter gains a fourth
  source kind, a computed document: a store name `syncHosts` (`hosts`)
  beside `data`, `ledger` and `dispatch`, registered with a build
  function (`a.computed(store, path, build func() ([]byte, error))`).
  - **Marked** (I9): by every change of what the summary is built from,
    at the one place all of them pass, the server's event hub
    (`internal/api/events.go`): its `session` sends (`publishIf` and
    `publishLatest`, which a server session's change reaches through
    `Server.localChange` and a hosted session's through the hub's
    `OnChange` alike), its `removed` send (the registry's `OnRemove`,
    every removal) and its `run` send (the engine's run events), and
    from PR-4 its `decision` send, each call `a.computedChanged("hosts")`
    on entry, whether or not a browser is told. These are the events the
    page itself refreshes on (PR-3), so the summary follows what the page
    would show: a standalone session's start, its question and its
    answer mark it though no host connects and no run changes (the
    engine's `OnChange` returns at once for a session outside a crew,
    `internal/crew/handoff.go`, and the hub hears no server session). A
    change no event tells (a `hostsSeen` entry passing its 24 hours,
    another process taking or letting go the ledger's lock) is caught by
    a check every 5 minutes on the adapter's clock seam, which builds and
    marks only when the hash differs. `computedChanged` only arms a
    coalescing timer of 30 s (the adapter's clock seam), takes the
    adapter's own lock alone and never builds or waits. When it fires,
    the adapter's goroutine builds the summary (outside the adapter's
    lock; the build takes the hub's and the engine's own locks as `GET
    /api/hosts` does, one at a time), hashes its bytes and marks the
    entry pending only when the hash differs from the last delivered.
  - **Delivered**: the drain builds it again at delivery, so the latest
    model goes out, as a data document is read as it then is; the hash
    of what was acknowledged becomes the last delivered.
  - **Revisions** (I9): the document's `Rev` is the source's own
    sequence in the epoch, never left at its zero value: 1 for the first
    bytes the drain hands the store in an epoch, and one more each time
    it hands bytes whose hash differs from the bytes it last handed in
    that epoch, acknowledged or not. A delivery retried with the same
    bytes keeps its `Rev`; one retried after the model changed carries
    the new bytes under the next `Rev`. So within an epoch revisions
    rise with the contents, and a store that ignores a doc whose `Rev`
    is not newer than the last it applied of that document, as `ext.Doc`
    allows (`ext/continuity.go`) and the adapter's own model does
    (`syncModel`, `internal/api/sync_test.go`), never keeps an older
    summary after a newer one reached it, nor drops a newer one as
    stale. A new epoch starts the sequence again at 1 with its replay,
    as `Rev` orders the docs of one epoch only. `ext.Doc.Rev`'s comment
    gains `hosts`: the summary's sequence in this epoch.
  - **Replayed**: every replay lists `hosts/summary` after the run
    pointers and before Dispatch's `log` doc, and a new epoch forgets the
    last delivered hash and starts the revisions again, so each epoch
    delivers it once, as `Rev` 1.
  - **The allowlist's rule**, restated in the adapter's comment and in
    `docs/protocol.md`'s sync section: what reaches the sync store is
    `syncDataDoc`'s data documents, a run's pointer, Dispatch's
    references, and one computed document, the host summary, built from
    the reduced model only; nothing else.
  - **Not on a switchyard.** A switchyard's hosts are other people's
    servers, so on a switchyard the computed source is never registered
    and no summary is sent, whatever provider is registered.
- **The two record kinds, CS1's.** CS1's OS-4 gives `ext.Remote.Kind`,
  beside `decision`, `pointer` and `pins`, the kinds `device` (a
  workspace's live server device: `{id, name, kind, platform, version,
  lastReport, revoked}`, at most 1 KiB, never a device of identity's kind
  `host`) and `hosts` (another of the person's servers' host summary, at
  most 16 KiB), each with its own `Valid`, kept under
  `remote/<device>/<kind>-<id>.json` and left out of the export, with
  `exttest.FakeHost` taking them. This PR only reads them. It defines the
  `hosts` body, `ext.HostSummary`, and makes the `hosts` kind's `Valid`
  call `HostSummary.Valid`, unless OS-4 has already taken the type from
  this plan's text.
- **The model.** `GET /api/hosts` gains `other`, present only when
  `sync` is in the capabilities with the state `ready` (never while
  `starting`, `internal/api/capabilities.go`) and the sync status has
  been `current` at least once in this server's life and is not
  `signed_out`. A provider never signed in, signed out, or still
  starting gives no `other` field at all, so the reply and the page are
  a plain build's:

  ```json
  "other": {"state": "current", "since": "…",
            "machines": [{"device": "…", "name": "studio", "platform": "darwin/arm64",
                          "version": "…", "lastReport": "…", "own": true,
                          "summary": {"…": "…"},
                          "runs": [{"id": "…", "crew": "…", "state": "running",
                                    "waiting": 1,
                                    "reportedBy": "studio", "reportedAt": "…"}],
                          "decisions": {"open": 1}}],
            "more": {"machines": 0, "remoteRuns": 0}}
  ```

  - `state` is the sync status's (`current`, `behind`, `unreachable`,
    `lapsed`) and `since` its time; for anything but `current` (and
    `behind` for less than 10 minutes, which a backlog of uploads is),
    `machines` is empty: the page shows the one line, and the remote
    files stay where the provider wrote them.
  - `machines` is one row per `device` record that is not this server
    (the sync status's `Device`, Owner decisions, 11) and not revoked,
    joined with that device's `hosts` summary (present only for the
    person's own servers, `own: true`, Owner decisions, 6), its `pointer`
    records whose run is not final and its `decision` records whose state
    is open. `lastReport` is the device record's, the sync service's own
    time of the device's last call as the records last carried it. A
    remote run's `waiting` is the count of that device's open `decision`
    records whose `run` (the copy's run that holds the decision now, DR1's
    `current_run`, beside `openedIn`, the run it was opened in) is that
    run, so an inherited gate counts on the continuation and on no
    predecessor (I8); the machine's `decisions.open` counts them all. A
    remote run says what is recorded:
    `reportedBy`, the name of the device that sent the pointer, and
    `reportedAt`, when; never a holder, since nothing across machines
    records one. A summary's host rows are at most 8 per machine in the
    reply (the rest counted in the row). At most 64 machines and 200 runs
    per machine, and all of `other` within its 56 KiB of the reply's
    budget (PR-2's Bounds), the rows left out counted in `more`.
  - A pointer whose device is this server is dropped, and so is one
    whose run id is a run of this ledger (this server's own row says
    more of it). One run id sent by two other devices (a data directory
    copied, or an archive restored on another machine: an export carries
    the ledger) shows under each device, each as reported; the page
    chooses neither.
- **The page.** `pages/hosts.vue` gains **Other machines** after Hosts:
  a `HostCard` per machine, kind `remote`, availability **Last reported
  14:05** (`neutral`; never **Connected** or **Here**), its summary's
  hosts as rows inside it ("as studio last saw them"), its runs as
  `HostRunRow`s with **Reported by studio at 14:05** and no link, its
  open decisions as a count with no link and no control, and the one
  line for a state that is not current. `utils/hosts.ts`'s
  `remoteSection` is the pure part, tested on its own (nothing for no
  `other` field).
- `docs/protocol.md`: the route's `other`, the document's row in the
  sync section; `protocol.ts` the types. `docs/features.md`'s Hosts
  entry gains Other machines and its observation, Acceptance, part B.

### The sync service and the provider: CS1's rows

No cloud PR is left to this item. The sync plan's service rows build
what the second half reads, with their own tests: every authenticated
call of a device moves its last report, written at most once a minute;
each live server device of the workspace (never identity's kind `host`)
is a `device` record carrying identity's name, platform and version and
that last report, which every member's servers fetch when the
workspace's devices change or a last report has moved by some minutes,
and which is gone at their next fetch once the device is revoked or
retired; a server's `hosts` document is personal and reaches only its
person's other devices. Its provider row (in the premium repository)
hands both kinds to `Host.PutRemote` and `DeleteRemote`, uploads the
summary with personal scope, and answers the sync status with its own
device.

### Premium (PR-6): the page with a registered provider

In the premium repository, in words, after the sync plan's provider row:
the Hosts page's own checks with a provider registered. Its tests are
the outage and the lapse (article 8): with
the service unreachable mid-session the page's `other.state` is
`unreachable` and the first half is unchanged, and it returns to
`current` with no restart; with the subscription lapsed the same with
`lapsed`; uploads queued in the outbox and never waited on. The browser
check of Other machines appearing lives here too, since only a
registered provider makes the section appear: a Playwright run of the
premium build against its fake sync service shows the section with a
machine **Reported by** its name, and its one line after the service is
stopped, with no restart. The open-source repository has no provider,
so its browser checks prove only that the section is absent (PR-5).

## Events, orderings and invariants

No state machine of the engine, the ledger or a session changes. What
changes is one register message's fields, one header of the host
connection's upgrade, one sync document, and a model read from existing
state.

**A host machine's availability**, per machine key, read when the page
asks:

| From | Event | To |
|---|---|---|
| (none) | a session of the machine registers (`Hub.Register`) | `connected` |
| `connected` | a session's link drops (`HostDisconnected`, its `hostLink` `down`, whatever its status) while another's is `up` | `connected` |
| `connected` | the last `up` link of its sessions drops, an ended session's among them | `away`, `since` that drop's `hostAwaySince` |
| `away` | a session reattaches (`attach`) or registers | `connected` |
| `away` or `connected` | its last session is removed from the registry (a `DELETE`, the retention sweep of ended sessions, or `Expire` after `DisconnectGrace`, 60 s) | `seen`, `since` the removal, held in `hostsSeen` for 24 h |
| `seen` | a session of the machine registers | `connected`; the `hostsSeen` entry dropped |
| `seen` | 24 h pass, the table passes 64, or the server restarts | gone from the page |

Invariants:

- **I1. A local run has one holder, and the page names it as it is
  when the reply is built, never from a guess or a stale state.** The
  holder is the process that holds `runs/owner.lock`: this server when
  its ledger owns it (writable, or read-only with its reason), another
  when `ledger.Probe` finds it held at that moment (its runs read from the
  database it writes, its line read in that same try), none when the
  probe finds it free or no locks, not known when the probe fails. The
  note is made for that reply from structured facts of that state alone
  (the lock's states table, PR-2), so no state shows another's words: a
  former holder's line is gone from the reply that finds the lock free.
  The probe is the page's only try of the lock and is let go at once. Every open run of the data
  directory is listed under that holder, whether or not an engine holds
  it in memory: the list is the ledger's open rows
  (`Ledger.OpenRuns`), read for every holder, and the engine's snapshot
  stands in for the rows it holds and never hides one it does not (a run
  a recovery interrupted, the runs of a lock taken late). A remote run is
  never given a holder: the page says which device reported it and when.
  A host never holds a run (`Launchable` refuses `where: host` on
  `main`), and its card says so. At stake in: Why and what happens (Runs
  and who holds them), PR-2's `runs` and `ledger`, PR-5's summary counts
  and its dropping of a pointer of this ledger's run, Acceptance A.3.
  Built and tested in PR-2: `TestOpenRunsBounded`, `TestLockView`,
  `TestProbeStates`, `TestHostsRunsHeldByThis`,
  `TestHostsRunsRecoveredHeldByThis`, `TestHostsRunsTakenLate`,
  `TestHostsRunsHeldByOther`, `TestHostsLedgerStates`,
  `TestHostsHeldHereReadOnly`, `TestHostsLockless`; in PR-3,
  `HostRunRow.test.ts`'s transitions.
- **I2. Nothing on the page acts on work.** Its only actions are links
  to existing pages of this server (`/runs/<id>`, `/sessions/<id>`,
  `/decisions`); a remote run or decision has no link and no control.
  No button starts, stops, continues, retries, moves or launches
  anything, and no route of this plan writes (`GET /api/hosts` is its
  only route, and a lock probe that is let go is no write). A test of
  each half proves it (Tests).
- **I3. Never "online" from a report.** Another machine's availability
  is the time the sync service last heard from it, said as a time;
  `connected` and `here` are only ever said of what this server sees
  itself.
- **I4. The second half is the sync status's, read at the moment of the
  reply.** `other` is built from the capability's state and the status
  read once per reply, then the remote files; a state that is not
  current empties `machines` in that same reply, so the page never shows
  another machine beside a state that says the service is gone, and a
  provider starting, never signed in or signed out leaves no `other` at
  all. The status is answered from memory (CS1), so the reply never
  waits on the network.
- **I5. What leaves the machine is the summary's allowlist.** The host
  summary is built from the model's reduced form only (ids of sessions,
  commands, folders, branches, run names, OS users, the machine id and
  machine keys never in it), checked at its bound by
  `ext.HostSummary.Valid` before it is handed to the sync store. No
  summary is sent from a switchyard.
- **I6. The machine id never leaves the machine, and no viewer reads
  it.** Every way it could, and what holds each:
  1. *A connection.* A server is sent only `machine.Key(id, serverKey)`,
     keyed by that server's key of this start, so two servers, or one
     server across a restart, see unrelated keys; a switchyard is sent
     no key at all (`TestRegistrationCarriesMachine`,
     `TestMachineKeyPerServer`, `TestNoMachineToASwitchyard`).
  2. *A route's reply or an event.* The hub hashes the key it is sent
     again (`machine.Group`) before anything is kept, and serves only
     that; no route or event carries the id or a key a host sent, and a
     webhook's or a notification sink's payload carries a session's id,
     name and agent alone (`internal/api/webhooks.go`)
     (`TestMachineKeyHashesTheKey`, `TestHostsReplyHoldsNoMachineID` over
     `GET /api/hosts`, `GET /api/sessions` and the `session` event).
  3. *A session's files.* Every session's file routes refuse the
     machine's directory (`machineFiles` and `LocalFileDeny` list
     `machine.Dir()`), so a view link on a session rooted at the home,
     or the Files tab, cannot read the file (`TestFileDenyRefusesMachineDir`,
     `TestSharedSessionCannotReadMachineID`).
  4. *The export and a restore.* `leftOut`'s `machine-id` row leaves it
     out of every archive and refuses an archive holding one, whatever
     data directory was chosen (`TestExportLeavesOutMachineID`, with a
     data directory equal to the machine's directory and one holding
     it).
  5. *A log or an error.* `internal/machine` names a bad file by its
     path and why, never its bytes, and nothing logs the id or a key a
     host sent (`TestMachineIDErrorsHoldNoContent`,
     `TestUnreadableMachineIDSendsNone`, and the captured logs of
     `TestRegistrationCarriesMachine` and `TestHostsReplyHoldsNoMachineID`).
  6. *A replicated record.* The host summary carries neither the id nor
     a key (I5, `TestHostSummaryLeavesOutTheLocal`); the sync adapter's
     allowlist (`syncDataDoc`) names no file at a data directory's root
     but `catalog.json`, so a data directory holding the file never
     sends it (`TestSyncNeverCarriesMachineID`); and this plan adds the
     id to no other record, HS1's `session_info` among them.
  7. *A process of the person's.* An agent in a session runs as the user
     and can read the user's files, this one among them, as it can any
     other. Nothing here stops that, and nothing needs to: the id is no
     credential (it opens nothing, and a key made from it groups
     sessions on one server's page and decides nothing, PR-1), so it
     tells such a process only that two hosts share a machine, which its
     own machine already tells it.
  At stake in: PR-1's `internal/machine`, deny lists, `leftOut` row and
  hub, PR-2's reply, PR-5's summary. Built and tested in PR-1, but for
  `TestHostsReplyHoldsNoMachineID` (PR-2) and
  `TestHostSummaryLeavesOutTheLocal` (PR-5).
- **I7. One machine is one card on this server's page.** The key a host
  of this server's machine is kept under and the key of this server's
  own row are made by one function, `machine.Group`, from one input,
  `machine.Key(id, serverKey)` of the one `machine-id` file and this
  start's server key, and compared as equal strings; nothing else makes
  a machine's key, and a key from a process or a session is hashed under
  another prefix and never equals one. At stake in: PR-1's server key
  and `internal/machine`'s functions, the hub's `hostKey`, PR-2's
  Names (`this.hosted`), Acceptance A.5. Built and tested in PR-1
  (`TestGroupIsTheHubsKey`, `TestMachineKeyPerServer`) and PR-2
  (`TestHostsHostOnThisMachine`, through a real registration).
- **I8. What waits for a person is counted once, where it waits now.**
  One predicate, `waitsForPerson` (not ended, `needs_input`, `Answered`
  empty, and from PR-4 `Decision` empty), decides every question the
  page and the host summary count and link; a decision counts on its
  `current_run` while its record is open, and its mirror, told by the
  session's `Attention.Decision`, is never a question. So a question
  keys answered in its terminal, a mirror, and an inherited gate's
  predecessor each count 0, and no count links to a question that no
  longer waits. At stake in: PR-2's `sessions.waiting` and runs'
  `waiting`, PR-3's links to sessions, PR-4's counts and links, PR-5's
  summary counts. Built and tested in PR-2
  (`TestHostsTerminalAnsweredNotWaiting`) and PR-4
  (`TestHostsCountsOpenDecisions`, `TestHostsDecisionMovesWithContinuation`,
  `TestHostsMirroredDecisionCountedOnce`,
  `TestHostsLoneGateLinksWithoutSession`: a count's link target,
  `waitingRef`, is made in the same pass as the count, so a link never
  names what the count left out).
- **I9. The summary another machine holds is the model as it last
  was.** Every change the page would show marks the summary (the event
  hub's `session`, `removed`, `run` and, from PR-4, `decision` sends,
  and the 5-minute check for what no event tells), and its revisions
  rise with its contents within an epoch and start again with each
  replay, so a store that orders by `Rev` holds, within the coalescing,
  the 5-minute check and the delivery, the latest build and never an
  older one. This is what appears only with a workspace's sync: with no
  provider `local.Continuity` drops the document, and the page's own
  first half never reads it. At stake in: PR-5's Marked, Delivered,
  Revisions and Replayed, `ext.Doc.Rev`'s comment, Orderings. Built and
  tested in PR-5: `TestSyncDeliversHostSummary`,
  `TestSyncHostSummaryRevisions`,
  `TestSyncHostSummaryFollowsStandaloneSessions`,
  `TestSyncHostSummaryPeriodicCheck`, `TestSyncReplayListsHostSummary`.
- **I10. A machine whose last session leaves this server is Last seen,
  whichever way the session left.** The registry decides, both for the
  cards and for `hostsSeen`: a host row is built from the registry's
  list, and the removal hook, which every removal reaches (`Expire`, a
  `DELETE`, the retention sweep), records the machine when the
  registry then lists no session of its key; the hub's map, which
  outlives a removal until its next `Expire`, decides nothing. At stake
  in: PR-2's host rows and Recently seen, the availability table's
  `seen` rows, PR-5's summary rows. Built and tested in PR-2:
  `TestHostsSeenAfterExpire`, `TestHostsSeenAfterDelete`,
  `TestHostsSeenAfterRetention`, `TestHostsSeenRegisterAndRemoveRace`.

The item's five promises and the invariants that keep them: what a
plain build shows, the first half whole with nothing registered (I1,
I7, I8, I10; `TestHostsNoOtherWithoutSync`, `hosts-free.spec.ts`); what
appears only with a workspace's sync (I4, I5, I9); what the page shows
when the service is unreachable or the subscription has lapsed, the
first half and one line (I4; `TestHostsOtherOnlyWhenCurrent`, PR-6's
outage and lapse tests); who holds each run (I1, and I3 for a remote
run's report); and that the page never offers or implies moving work
(I2, the footer, Out of scope).

**Orderings.** The page reads `GET /api/hosts` after the events it
already gets (`session`, `removed`, `run`), so it can lag an event by its
debounce (one second). Its reads are one at a time, a refresh asked
meanwhile queued behind the read under way, and a reply of an older
token's generation dropped (PR-3), so replies are applied in the order
their reads began and the page never shows a state older than one it
has shown. The sync document is marked pending after the model's change
(the coalescing timer armed from the event hub's sends under no lock of
the ledger or the hub, the build and the delivery made by the adapter's
own goroutine), so a host summary on another machine lags by at most the
30 s coalescing plus the delivery, or the 5-minute check for a change no
event tells, and its `Rev` rises with each new content in an epoch (I9).
Of a register and a machine's last removal, the second decides
`hostsSeen`, by the registry's list (PR-2, I10).

**What each store holds.** `<os.UserConfigDir()>/conductor/machine-id`:
33 bytes (the id and a newline), 0600, made once, outside the default
data directories, refused to every session's file routes and left out
of every export (I6). Memory: the server's key of this start (32 bytes) and
`hostsSeen`, at most 64 entries. The
remote files are CS1's (`remote/<device>/<kind>-<id>.json`:
`device-<id>.json` and `hosts-summary.json`). The ledger, Dispatch, the sessions'
stores: unchanged.

**Migration.** None: no schema changes. An older host sends neither new
field and keeps working (its card says what it does not report); an
older server ignores them (`json.Unmarshal` of the register ignores
unknown fields, as HS1's PR-12 relies on too).

**Protocol.** Every place it lives:

| Change | Places | Bound | Test |
|---|---|---|---|
| `HostInfo.machine`, `HostInfo.platform` | `internal/proto/hostmsg.go`, `protocol.ts` (`HostMachine`, a new declaration beside `HostIdentity`), `docs/protocol.md` (Host control connection) | 32 hex; 32 bytes and the pattern; the register within `MaxHostMessage` | `TestRegisterHostMachineAndPlatformBounds` |
| the upgrade's `Conductor-Server-Key` header | `internal/api/ws_host.go`, `internal/hostagent`, `docs/protocol.md` (Host control connection) | 64 hex; a header of another form means none | `TestHostUpgradeCarriesServerKey`, `TestBadServerKeySendsNoMachine` |
| `HostInfo.version` kept | `internal/signal/hub.go`, `docs/protocol.md` | 64 bytes, `version.Version`'s pattern | `TestRegisterKeepsHostVersion` |
| `session.Info` `hostPlatform`, `hostVersion`, `hostKey`, `hostKeyFrom`, `hostLink`, `hostAwaySince` | `internal/session/info.go`, `web/app/composables/useSessions.ts` (`SessionInfo`, the app's session type), `docs/protocol.md` (sessions) | as above; `hostKey` 32 hex; `hostKeyFrom` one of three words; `hostLink` `up` or `down` | `TestHostedInfoCarriesHostFields`, `TestHostLinkApartFromStatus` |
| `GET /api/hosts` | `internal/api/hosts.go`, `protocol.ts` (`HostsReply`), `docs/protocol.md` (routes) | 256 KiB encoded, the halves' budgets, the field and row bounds; `waitingRef` 512 (PR-4) | `TestHostsReplyBounded`, `TestHostsLoneGateLinksWithoutSession` |
| the host summary document | `internal/api/sync.go`, `ext` (`HostSummary`, `Doc.Rev`'s comment), `docs/protocol.md` (sync) | 16 KiB, 32 rows; `Rev` from 1 in each epoch, rising with the contents | `TestHostSummaryValid`, `TestSyncDeliversHostSummary`, `TestSyncHostSummaryRevisions` |
| the `hosts` kind's body, `ext.HostSummary` (the kinds `device` and `hosts` are CS1's OS-4) | `ext` (`Remote`'s `hosts` `Valid`), `docs/protocol.md` (sync) | 16 KiB | `TestHostsKindValidIsTheSummarys` |

## Out of scope

- Starting, moving, continuing, retrying or failing over work on another
  machine; a run held by a host (remote managed-crew ownership is a later
  Yardmaster extension); a "run on" picker; anything that implies the
  page schedules (the Program, section 7).
- Naming, issuing or revoking host tokens, and per-token host identity:
  `config.HostTokens` stays a list, and an identity's host credentials are
  ID1's.
- Notifications when a machine goes away; a history of availability; a
  graph of load. The page is a view of now.
- Brakeman reading the page (`GET /api/hosts` is not on its shared
  reads); the MCP server and the CLI: no `conductor hosts` command.
- Another member's servers' hosts and personal counts (Owner decisions,
  6); runs and decisions sync does not share (CS1's scope rules).
- Showing a machine that is neither this server, a host of it, its
  switchyard nor a workspace device: a host connected
  to another server is seen only through that server's summary, by the
  same person.
- Answering a decision of another machine from this page, or a link to
  where it is answered; that is the sync service's and MB1's.
- A Hosts page on a switchyard, and grouping a switchyard's publishers
  by machine: a switchyard is sent no machine key, and its landing page
  stays as it is.
- A session's or an attempt's `where`: shown once a row reports it
  (HS1's PR-12, C10), not here.

## Tests per surface

The checks per surface are `docs/checks.md`'s. Go: `make lint`, `go test
-race -count=1 -timeout 30m ./...` (the whole module), `make lint-static`.
UI: `npm --prefix web run typecheck`, `npm --prefix web test`, `make
test-e2e` (the new specs alone first). No Python and no desktop change.
Per PR: PR-1 and PR-2 change the app's types beside Go, so each runs
the Go checks and `npm --prefix web run typecheck` and `npm --prefix web
test`: PR-1's `SessionInfo` fields are typechecked through their real
consumers (`useAttention.ts`, the sidebar), and `utils/sidebar.test.ts`
gains a hosted session carrying them, typed `SessionInfo`; PR-2's
`HostsReply` is typechecked, and its first consumer and tests are
PR-3's `utils/hosts.ts`. (No `utils/protocol.test.ts` exists, and none
is made.) PR-3 runs the UI checks; PR-4 and PR-5 run both.

**`internal/proto`** (PR-1): `TestRegisterHostMachineAndPlatformBounds`
(each field at its bound and one past, the pattern's refusals),
`TestHostInfoOmitsEmpty` (an older host's register byte for byte).

**`internal/machine`** (PR-1): `TestMachineIDMadeOnce` (two reads, one
file, 0600, the same id), `TestMachineIDMadeOnceByTwo` (two makers
started together against an empty directory, held at a gate between
writing their temporary files and linking: one link wins, both return
the winner's id, one file, no temporary file left),
`TestMachineIDUnreadableReportsNone` (a file of the wrong form: an
error, no id), `TestMachineIDErrorsHoldNoContent` (I6: a file holding
something near an id, too long, or not hex: the error names the path
and the reason, never the bytes), `TestMachineKeyPerServer` (one id and two server keys:
two unrelated keys; one key: the same), `TestPlatformReadsWSL` (an
`osrelease` with `microsoft-standard-WSL2` read through the seam gives
`+wsl`; without, none).

**`internal/hostagent`** (PR-1): `TestUnreadableMachineIDSendsNone` (none
sent, one warning, which holds the file's path and not its bytes),
`TestRegistrationCarriesMachine` (to a server: the key computed from the
upgrade's header, never the id; one machine registering with two
servers sends two different keys; the captured log holds neither the id
nor the key),
`TestNoMachineToASwitchyard` (a registration with `Options.Switchyard`,
and the uplink's, carries no `machine`), `TestBadServerKeySendsNoMachine`
(no header, or one not 64 hex: none sent), `TestPublishedConnected` (a
publication's `Connected` false when its connection drops, true after
the reconnect).

**`internal/signal`** (PR-1): `TestRegisterKeepsHostVersion`,
`TestMachineKeyHashesTheKey` (what the host sent never in `Info`, the
key 32 hex), `TestGroupIsTheHubsKey` (I7: a register's `hostKey` is
`machine.Group` of the key it sent, byte for byte, and a process's or a
session's key never equals the `machine.Group` of any key in the test),
`TestOlderHostKeyedByProcess` (no machine: the owner hash,
`hostKeyFrom` `process`), `TestOldestHostsKeyedBySession` (several
registrations with neither machine nor instance: each its own key),
`TestHostLinkApartFromStatus` (a running session: `hostLink` `up`; its
link dropped: `down` with `hostAwaySince`, the status
`host_disconnected`; reattached: `up`, the field gone, though
`disconnectedAt` stays; an exited session whose host is still
connected: `up` and its status `exited`; that host gone: `down` with
`hostAwaySince`, its status still `exited`).

**`internal/config`** (PR-1): `TestFileDenyRefusesMachineDir` (I6: a
server's `FileDeny` and `LocalFileDeny` both list `machine.Dir()` under
a test home, and `session.ResolvePath` refuses a path under it, the
`machine-id` file among them).

**`internal/export`** (PR-1): `TestExportLeavesOutMachineID` (I6: a data
directory that is the machine's directory, and one that holds it: the
archive has no `machine-id`, `Left` names it with `WhyMachine`; an
archive holding one is refused by `Install`).

**`internal/session`** (PR-1): `TestHostedInfoCarriesHostFields` (JSON
names and omission).

**`internal/api`** (PR-1): `TestHostUpgradeCarriesServerKey` (an
accepted upgrade of `/ws/host` carries the header, the same within one
start; a refused one carries none), `TestSharedSessionCannotReadMachineID`
(I6: a server session rooted at a test home whose config directory holds
a `machine-id`: a view link's file read of it refused, a file beside it
under the home served), `TestSyncNeverCarriesMachineID` (I6: a data
directory that is the machine's directory, a fake sync store: no
delivery and no replay holds `machine-id`).

**`internal/crew/ledger`** (PR-2): `TestLockView` (an owner writable;
an owner whose database a newer schema wrote: owned and read-only with
the reason; a ledger whose wait for the lock ended, through `LockWait`'s
seam: not owned, `GaveUpAt` set and no `Why`; a lock taken later: both
cleared; a filesystem without locks, through the seam: `NoLocks`),
`TestProbeStates` (held, with the holder's line read in that try; free,
with no line; no locks through the seam; a lock file that cannot be
opened: an error), `TestOpenRunsBounded` (open rows only:
a running row and an interrupted one not continued listed; a finished,
a stopped and an interrupted one continued left out, its continuation
listed once; newest first, at most n and the count of the rest, on a
writable and on a read-only ledger).

**`internal/api`** (PR-2): `TestHostsThisServer` (name, platform,
version, here, counts), `TestHostsGroupsAMachine` (three `conductor host`
registrations with one machine key are one row; two machines with one
host name are two), `TestHostsAvailability` (connected, away with its
`since` the last link's drop, seen after `Expire`, gone after 24 h
through the table's clock seam, never a sleep),
`TestHostsEndedSessionKeepsItsLink` (a machine whose only session has
exited with its host still connected: `connected`; that host's link
dropped: `away` with its `since`, the session's status still `exited`;
after `Expire`: `seen`; the replies before and after the drop differ), `TestHostsSeenAfterExpire` (the registry's
`OnRemove` records the machine after `Expire`; a machine with a second
session left records nothing), `TestHostsSeenAfterDelete` and
`TestHostsSeenAfterRetention` (I10: a machine's last hosted session
ended and removed by `DELETE /api/sessions/{id}`, and another's by the
retention sweep through its clock, while the hub still keeps each
entry: the machine is Last seen at once, no card lists the session, and
the hub's later `Expire` changes nothing), `TestHostsSeenRegisterAndRemoveRace` (a
register and the last removal of one key in both orders, held at a gate
in the hook: the second decides), `TestHostsSeenBounded` (65 machines,
the oldest dropped), `TestHostsTerminalAnsweredNotWaiting` (I8: a
server session's and a run member's question shown, then answered by
keys typed in its terminal while the screen still shows it, through the
session's own terminal-answer seam: `waiting` 0 for the session, its
machine, this server and the run, and no link to it; the screen
cleared: still 0; a new question: 1), `TestHostsRunsHeldByThis`,
`TestHostsRunsRecoveredHeldByThis` (I1: a server ended with a run
running; a new server over the same data directory recovers it to
`interrupted`, which its engine does not hold: the run listed `this`,
`interrupted`, `waiting` 0; continued, the predecessor gone from the
list and the continuation listed once, from the engine),
`TestHostsRunsHeldByOther` (two servers over one data directory, as
`serve` makes them, the later ends first: the first launches a run of
the fake launcher; the second's reply lists that run from the database,
`holder` `other` with the note, `waiting` null; the first ended after
the second's wait for the lock has given up, which the test sees in the
second ledger's `Lock().GaveUpAt`, never by a sleep: the next reply says
`none`, never `other`, and its `note`, and every other field of the
reply, holds neither the first server's pid nor its start time nor the
words "another server holds"), `TestHostsLedgerStates` (I1: one data
directory walked through every state of the lock's table, each reply
built twice with a session's event between, the redraw the test causes:
`other` with the holder's line; `none` with this server's reason and
nothing of the former line; `unknown` through `Probe`'s seam returning
an error, with the class's words and not the error's text; a lock file
whose line is not in its form: `other` with "its line is not
readable"), `TestHostsRunsTakenLate` (the first ended within
the wait: the second takes the lock and says `this`, and the first's
run, which its recovery interrupted and its engine never launched, is
listed under `this` from the ledger),
`TestHostsHeldHereReadOnly` (a database a newer schema wrote:
`this_read_only` with the reason), `TestHostsLockless` (the
filesystem's refusal gives `none`), `TestHostsSwitchyardPublishing` (a
fake publisher: `url`, `publishing`, `connected`),
`TestHostsNotOnASwitchyard` (404 `not_found`), `TestHostsReplyBounded`
(every list past its bound and every field at its longest and full of
characters JSON escapes: the reply within 256 KiB, each half within its
budget, the `more` counts exactly what was left out),
`TestHostsHostOnThisMachine` (I7, through a real registration: the
test server given a machine id, and a `hostagent` given the same
machine directory dialing its `/ws/host`, reading the upgrade's header
and registering with the key it computes; its session on `this` as
`hosted`, `this.key` equal to the session's `hostKey`, no card of its
own; a second test server, with another server key: that host's card
there is its own, not that server's row),
`TestHostsReplyHoldsNoMachineID` (I6: a test server given a known
machine id, a host registering with a key: the bytes of `GET
/api/hosts`, of `GET /api/sessions` and of the `session` event, and the
server's captured log, hold neither the id nor the key the host sent),
`TestHostsAdminOnly` (a host token, a share link and Brakeman's
principal refused), `TestHostsWritesNothing` (the route over a server
whose stores are observed: no commit, and no file made under the data
directory or the machine's directory).

**`web/app` vitest** (PR-3): `utils/hosts.test.ts` (`platformWords` for
each form and an unknown one, the availability's words and order, the
holders' words, the sort, the debounce through fake timers, and the
reader: a refresh asked during a read starts after it, never beside
it; a reply of an older generation is dropped after a token change; an
older reply released after a newer one, through deferred fetches the
test resolves in that order, is never applied), `components/HostCard.test.ts`
(each availability's badge and colour; **Holds no runs** for a host; a
host whose key is not a machine's), `components/HostRunRow.test.ts`
(the five holders' words; a null waiting count's words; the transitions
`this`, `other` with its line, `none`, `unknown`, `this_read_only`,
`this`, each a re-render with the next reply's row, the redraw the test
causes, checked, then checked again after a second re-render that only
changes its waiting count: each shows only its own state's words and
note, and from `none` on the former holder's pid and time are nowhere in
the row; the only control is the run's link: no button rendered, which
is I2's test for the first half).

**Playwright** (PR-3): `web/e2e/hosts.spec.ts`: the e2e server and a
real `conductor host` with the stub agent (`fixtures.ts`'s
`hostedSession`, its teardown by name, `stopStrays` first): the page,
reached by `go hosts` after the layout's sidebar root is drawn, shows
This server with Here, the host's card with its platform as the words
`platformWords` (imported from `utils/hosts.ts`) gives for the
`platform` the spec reads from `GET /api/hosts` itself, so the same
check holds on a WSL machine and in CI, and **Connected**, its session
as a link; the session's question (the stub's ask) makes the card's
waiting count 1, waited on by the count itself; the host process
stopped by its process group makes the card **Away since**, then
**Last seen** after the grace (the spec's own wait bounded by
`DisconnectGrace` plus the maintenance tick, with `test.setTimeout`
above it); a managed run of the example crew is listed **Held by this
server** and links to its run page; the page has no button whose name is
start, stop, move, launch, continue or retry; the run's holder words and
the controls are checked again after a redraw the spec causes (a second
host connecting). Counting only what the
spec made, by its host name.

**`internal/api`** (PR-4), each over DR1's records as DR1's own writers
make them (`Tx.OpenDecision`, `Tx.Answer`, `Tx.MoveDecisions`,
`TryAskDecision`) and as `Ledger.Decisions` and `session.Info` return
them, never a fixture with a field DR1 does not store:
`TestHostsCountsOpenDecisions` (two open, one answered: 2; by run),
`TestHostsDecisionMovesWithContinuation` (I8: a gate open on run A, A
interrupted and continued as B: B's `waiting` 1 and its link the
decision's, A not listed and nothing counted for it; a decision
answered on A before the continuation moved: counted nowhere),
`TestHostsMirroredDecisionCountedOnce` (I8, the mirror told by the
session's `Attention.Decision`: an open decision whose mirror shows: 1
on the run and 0 on this server's `sessions.waiting`, its link the
decision's; answered but not applied, its mirror still showing: 0 and
no link; applied: 0; a member's own question beside it: 1 more, its
link the question's), `TestHostsLoneGateLinksWithoutSession` (a gate
opened on run A, A interrupted and continued as B before B's attempt
starts, so no session of either is listed: B's `waiting` 1 and its
`waitingRef` `decision:<id>`, from the record; a second decision opened
on B: 2 and no `waitingRef`; a lone question on a host's session: its
`question:` ref on the session's machine row, built in the same pass as
its count), `TestHostsHostedQuestionStaysAQuestion`. vitest: the links'
targets in `utils/hosts.test.ts`, from `waitingRef` alone (a set ref: its
item; none and more than one: `/decisions`; 0: no link). Playwright: `hosts.spec.ts` gains a gate's
decision on the example crew's run: the run's waiting count 1, its link
opening `/decisions` on that item, drawn by MB0's PR-4.

**`ext`** (PR-5): `TestHostSummaryValid` (each field at its bound and
past; a session id, a path or a user refused by shape),
`TestHostsKindValidIsTheSummarys` (a `hosts` record whose body fails
`HostSummary.Valid` is refused by the kind's `Valid`; the kinds and
their bounds are OS-4's, tested there). **`ext/exttest`**: the
`Continuity` check sends the summary document.

**`internal/api`** (PR-5): `TestSyncDeliversHostSummary` (a host's
connection changes the summary: one delivery after the coalescing, the
fake clock's seam; an unchanged model none; the bytes delivered the
model as it is at delivery), `TestSyncReplayListsHostSummary` (a replay
lists `hosts/summary` after the pointers and before the `log` doc; a new
epoch delivers it again though unchanged, as `Rev` 1),
`TestSyncHostSummaryRevisions` (I9: three changes of the model, a
delivery that fails and is retried once with the same bytes and once
after the model changed, then a replay; each doc's `Rev` above the last
of its epoch, a retry of unchanged bytes the same `Rev`, the new epoch
from 1; applied through `syncModel`, which drops a `Rev` not newer, the
store holds the model as it last was after each step),
`TestSyncHostSummaryFollowsStandaloneSessions` (I9: a server session in
no run and no host connected: its start, its question, its answer and
its removal each deliver a summary whose counts are the model's after
the coalescing, through the fake clock, with no host or run activity),
`TestSyncHostSummaryPeriodicCheck` (I9: a `hostsSeen` entry passing its
24 hours through the table's clock: delivered at the next 5-minute
check; an unchanged model: no delivery), `TestSyncNoHostSummaryOnASwitchyard`
(a switchyard with a fake sync provider: no `hosts` document, in a
delivery or a replay), `TestHostSummaryLeavesOutTheLocal` (a session id,
cwd, branch, run name, user, machine id and machine key never in the
bytes delivered), `TestHostsOtherMachines` (a fake registered sync
provider of `ext/exttest` and seeded remote files: a device, its
summary, two pointers, one final, a decision open on the open run and
one opened on a run it was continued from: one machine row, one run
reported by its name and time, `waiting` 2 on that run by the copies'
`run`, `decisions.open` 2),
`TestHostsOtherSameRunTwoDevices` (one run id from two devices: under
each, each as reported, no holder), `TestHostsOtherOnlyWhenCurrent`
(each status but current, and behind past 10 minutes, empties
`machines` in the same reply; current again shows them, no restart),
`TestHostsNoOtherUntilCurrent` (a registered provider still `starting`,
never signed in, or signed out: the same reply as
`TestHostsNoOtherWithoutSync`'s), `TestHostsOtherDropsThis` (a device
and pointers of this server's own device id dropped, and a pointer of a
run of this ledger), `TestHostsOtherBounded` (within its 56 KiB, the
counts exact), `TestHostsNoOtherWithoutSync` (no provider: no `other`
field). vitest: `remoteSection` in `utils/hosts.test.ts` (nothing for no
`other` field; the state's line for each state; never "Connected" for a
remote row; I2 for the second half: a remote run row and a remote
decision render no link and no button). Playwright:
`hosts-free.spec.ts`, the plain server's page with no Other machines
section. The section's appearance needs a registered sync provider,
which this repository has none of: seeded remote files alone show
nothing here, so no open-source spec claims to show it; the Go tests
above drive it through `ext/exttest`'s fake provider, and the browser
check of the section is the premium PR's (PR-6).

**Premium** (PR-6): its repository's checks, with the tests named in its
section above; its outage and lapse tests are article 8's for this item.
The sync service's and the provider's own tests are the sync plan's.

**By hand** (Acceptance): real machines of three platforms, a real
WSL server, a real disconnect, and the sync service's states, which no
test here can reach.

## Boxcar constraints

The eight of the Program, section 2.5:

1. **The engine starts an attempt only through `crew.Launcher`.** Not
   touched: nothing here starts a run or a process; the page reads the
   engine's list.
2. **`session.Local` drives a `session.Process`.** Not touched: the page
   reads `session.Info`; nothing reaches a pid or a tty (the owner lock's
   pid is a line of text, shown as written, never used).
3. **An attempt's context is files relative to its worktree.** Not
   touched: nothing an attempt needs changes.
4. **The agent talks to the server over the network.** Not touched; the
   host's register stays the address-based WebSocket it is.
5. **Checks go through `internal/proc`.** Not touched: no check runs.
6. **Platform splits stay platform tags.** The WSL read is a file read on
   every platform, no tag; the page shows a platform as reported, and a
   capability (`sync`) rather than a plan.
7. **The ledger's evidence rows carry `where`.** The page shows no
   `where` (no session record on `main` carries one, and the API keeps no
   record of the Runtime a launch chose) and assumes nothing of a run's
   attempts or a session's process; it gains a `where` only with the row
   that reports one (HS1's PR-12, C10).
8. **`hostagent` is a launch site as much as `api`.** The host reports
   its machine and platform itself, through its own registration; the
   server never describes a host's machine from its own.

## Open-core checklist

- [x] **1. No paid code, licence check or plan check.** The second
  section shows on `sync` in the capabilities and the provider's status;
  no plan or entitlement is read.
- [x] **2. Fully useful on its own.** The first half is the whole page of
  a plain build: every machine this server knows and every run it holds.
- [x] **3. Nothing paid behind a flag.** Other machines need the sync
  service's records; no setting shows them.
- [x] **4. Stop, inspect, revoke, export, required approvals free and
  whole.** The page is an inspect view and adds no control; export
  leaves out only what is this machine's or the provider's; no decision
  is answered from the page.

## The PRs

Each PR is one issue and one branch `issue-N` from `main` (or a branch
in its own repository), cut after the PRs it depends on have merged; it
leaves its repository's `main` working and green on its own, ticks the
checklist above, names its checks, and updates the
`docs/repository-map.md` and `docs/protocol.md` rows it changes. Sizes
are changed lines including tests, each a batch (the owner, 2026-11-05:
300 to 800 changed lines, at most about 1,500 with tests). "May run
beside" names only PRs that share no package or directory with it.

| PR | Repo | Title | Touches | Depends on | May run beside | Size |
|---|---|---|---|---|---|---|
| PR-1 | open source | machine, proto, hostagent, signal, session, config, export, api: a host's machine key and platform, the server's key, the version kept, the session's host fields and link, the machine id kept from files and exports, a publication's connection | `internal/machine` (new), `internal/proto`, `internal/hostagent`, `internal/signal`, `internal/session`, `internal/config` (the deny lists), `internal/export` (`leftOut`), `internal/api` (`ws_host.go`'s header, `publish.go`'s interface, the I6 tests), `internal/cli` (the server's machine id at start), `web/app/utils/protocol.ts`, `web/app/composables/useSessions.ts`, `web/app/utils/sidebar.test.ts`, `docs` | none (`main` as it is) | none | ~650 + 850 |
| PR-2 | open source | api, ledger: `GET /api/hosts`, the runs' holder probed and their rows read, the recently seen | `internal/api`, `internal/crew/ledger`, `web/app/utils/protocol.ts`, `docs` | PR-1 | none | ~750 + 950 |
| PR-3 | open source | web: the Hosts page, its cards and run rows, the nav and the palette; `hosts.spec.ts`; the features entry | `web/app`, `web/e2e`, `docs/features.md` | PR-2 | none | ~650 + 600 |
| PR-4 | open source | api, web: the decisions waiting, counted once each and linked to Needs your decision | `internal/api`, `web/app`, `web/e2e`, `docs/protocol.md` | PR-3; DR1's PR-4 (#637) and MB0's PR-4 (DR1's records on the page, after its PR-3) on `main` | none | ~350 + 500 |
| PR-5 | open source | api, ext, web: the host summary sent through sync, other machines read from the remote records, the page's Other machines; the features entry's second half | `internal/api`, `ext`, `ext/exttest`, `web/app`, `web/e2e`, `docs` | PR-3 (PR-4 if merged first, rebased); CS1's OS-4 on `main` | none | ~700 + 750 |
| PR-6 | premium | the Hosts page with a registered provider: the outage and lapse tests, the browser check of Other machines | the provider's tests and its Playwright run against its fake sync service | PR-5 in an open-source tag; CS1's provider row (the two kinds handed over, the summary uploaded, its own device in the status) and its service rows in staging | none (other repository) | ~50 + 450 |

**PR-1**: the two `HostInfo` fields and their bounds; `machine-id` made
and read through `internal/machine` (`Dir`, `ID`, `Key`, `Group`,
`Platform`); the machine's directory in both deny lists and the
`machine-id` row of `leftOut` (I6); the server's key and the upgrade's
header; the hub's checks, the version kept, the session's key and where
it came from; `session.Info`'s six fields, `hostLink` read from the
connection; `PublishedSession.Connected` and `Published`'s answer; the
three protocol places (`SessionInfo` in `composables/useSessions.ts`,
`HostMachine` in `protocol.ts`). No page yet; `GET /api/sessions` carries the new
fields, which the sidebar ignores. Tests: `internal/proto`,
`internal/machine`, `internal/hostagent`, `internal/signal`,
`internal/session`, `internal/config`, `internal/export`,
`internal/api` above, and the web checks for the app's types; I6's
tests but PR-2's and PR-5's; I7's half here is `machine.Group`, the one maker of a
machine's key, proved by `TestGroupIsTheHubsKey`. Ordering with HS1's PR-12
and MB0's PR-6, which touch the same three packages: whichever merges
second rebases; never beside them.

**PR-2**: `hosts.go`, the reply and its budgets, the host rows and
`hostsSeen` decided by the registry's list on its `OnRemove`, the
predicate `waitsForPerson`, the holder probed per reply (`ledger.Probe`,
`Ledger.Lock()`) and its note made from that state alone,
`Ledger.OpenRuns` read for every holder with the engine's snapshot
standing in for the runs it holds; the route's row. Tests:
`internal/api` and `internal/crew/ledger` above, and the web checks for
`protocol.ts`; the invariants this PR builds and the tests that prove
them: I1 (`TestOpenRunsBounded`, `TestHostsRunsRecoveredHeldByThis`,
`TestHostsRunsTakenLate`, `TestHostsLedgerStates`), I6's reply
(`TestHostsReplyHoldsNoMachineID`), I7 (`TestHostsHostOnThisMachine`), I8's
first half (`TestHostsTerminalAnsweredNotWaiting`), I10
(`TestHostsSeenAfterDelete`, `TestHostsSeenAfterRetention`).

**PR-3**: the page, `HostCard.vue`, `HostRunRow.vue`, `utils/hosts.ts`,
the nav, the bottom bar and the palette; `hosts.spec.ts`; the features
entry with Acceptance part A's record (written from what was seen, by
hand, before the PR is marked ready). After this PR the first half is
shipped.

**PR-4**: the decision counts by `current_run`, a mirror told by its
session's `Attention.Decision` and counted once as its decision, each
count's `waitingRef` made with it, and the links, once DR1's routes and
MB0's PR-4 are on `main`. Tests above; I8's
second half: `TestHostsDecisionMovesWithContinuation`,
`TestHostsMirroredDecisionCountedOnce`,
`TestHostsLoneGateLinksWithoutSession`.

**PR-5**: `ext.HostSummary` and the `hosts` kind's `Valid` calling it
(the kinds are CS1's OS-4); the adapter's computed source and its document, marked from the
event hub's sends and the 5-minute check, its revisions; the `other`
model; the page's section; `hosts-free.spec.ts`; the features entry
completed. Tests above; I9: `TestSyncHostSummaryRevisions`,
`TestSyncHostSummaryFollowsStandaloneSessions`,
`TestSyncHostSummaryPeriodicCheck`. This is the
last open-source PR, and it ends the item's open-source work.

**PR-6**: in the premium repository, in its own words and checks; its
PR body ticks article 3 (the feature names the hosted dependency that
gates it: here, the sync service). It follows CS1's provider row and is
cut when that row is planned in its repository's issues.

## Owner decisions

Dated 2026-11-05. Each is a default the owner may overturn with a row in
the Program's status log, its convention for a decision the owner did
not choose (the Program, section 3.1).

1. **The open decisions named with this item are not its own.** O4,
   O5, O8 and O10 (the Program's open decisions) belong to other items;
   HS2 resolves none and needs none resolved (O10 only through CS1, its
   prerequisite). No question is left open by this plan.
2. **A host is a machine, keyed by a random id the machine keeps.** Not
   the host name (two Macs are often both "MacBook-Pro", and the
   switchyard's grouping by name today merges them) and not a hardware
   id (a fingerprint a person did not agree to). An older host is keyed
   by its process, and says so.
3. **Availability is said as what this server sees, or as a time.**
   Here, Connected, Away since, Last seen (in memory, 24 hours, 64
   machines) locally; Last reported for another machine, never
   "online": a report minutes old is not a connection.
4. **Recently seen hosts are not persisted.** A new store file for a
   view of now is not worth its migration; the page says the table
   began when the server started.
5. **When the sync status is not current, the page is the first half.**
   The owner asked the page to degrade to the local view when the
   service is unreachable or the subscription has lapsed; showing last
   known machines greyed beside it was the alternative, declined
   because an availability that cannot be refreshed misleads more than
   it helps. A backlog of uploads (`behind`) under 10 minutes still
   counts as current.
6. **Across machines, a person sees the full summary of their own
   servers only.** Another member's server is shown as its workspace
   device record (name, platform, last report) and the runs and
   decisions sync already shares from it (CS1's scope: a run linked from
   a shared ticket); its hosts and its counts stay with its person. This
   keeps a member's working hours and machines out of a workspace view
   they did not choose to share.
7. **The route is admin only**, not a Brakeman read, never through a
   link; a reviewer or decide-only identity (ID1) does not see it, as
   they do not see `GET /api/events`.
8. **The page is "Hosts", at `/hosts`, after Events in the nav.** The
   word is the sidebar's machine tag's and the repository's; the page
   lists servers among hosts and says which is which.
9. **A platform is `<GOOS>/<GOARCH>`, with `+wsl` inside WSL**, shown as
   words; WSL is told from the kernel's release string, which needs no
   setting and no networking.
10. **The page's only actions are links**, and its footer says runs stay
    with the server that holds them. A "where to run" choice belongs to a
    launch and is a later Yardmaster extension (the Program, sections 4.4
    and 7).
11. **What the second half needs from a registered sync provider and
    the workspace**, as HS2's own interface requirements, each settled
    by the plan that owns it:
    - a remote record of kind `device` for each live server device of
      the workspace (id, name, kind, platform in this plan's form,
      version, last report, revoked), at most 1 KiB, gone when the device
      is revoked or retired: CS1's OS-4 and its service;
    - a last report the service keeps by its own rule, every
      authenticated call of the device, written at most once a minute
      (CS1);
    - a personal-scope `hosts` document carrying PR-5's host summary, at
      most 16 KiB, relayed to the same person's other devices only, with
      its own row in the sync plan's allowlist (CS1);
    - a sync status naming this server's own device (`Device`,
      `DeviceName`), so the page tells itself from the workspace's other
      servers (CS1's OS-4);
    - a host (`conductor host`) never listed as a server device that
      holds runs: identity's own device kind `host`, every list of a
      workspace's servers taking kind `server` alone, with the
      platform's and the version's forms and the device's name, platform
      and version on its feed (ID1's amendment);
    - a remote run's waiting count only from the decision copies sync
      shares, each naming the run that holds it now (`run`, DR1's
      `current_run`) beside the run it was opened in (`openedIn`) (CS1).
    PR-5 waits for CS1's OS-4 in this repository, and PR-6 for CS1's
    provider row.

## Acceptance

The boundary (the Program, section 4.4, HS2): one page shows every
authorized host and who holds each run, without implying cross-host
scheduling.

**Part A, the first half, after PR-3 (and PR-4 when it merges)**, by
hand, with no networking configured by anyone on Windows or WSL:

1. A server on the LAN Linux box (`conductor serve`, a host token in its
   configuration). From the Mac, `conductor host --server <the LAN box>`
   with the stub agent or Claude Code; from the Windows machine, inside
   WSL, `conductor host` against the same server: an outbound
   connection from WSL, which needs nothing set up.
2. On the LAN box's Hosts page: This server (`Linux · x64`, Here); the
   Mac's card (`macOS · arm64`, Connected) and the WSL card (`Linux ·
   x64 · WSL`, Connected), each one card however many sessions it
   hosts; a question asked in the Mac's session counted on its card,
   and its link opening the session.
3. A managed run of the example crew, launched on the LAN box: **Held by
   this server**, its link opening its run page. A second server started
   on the same data directory (another port) shows the run **Held by
   another server on this data directory**, with the line that server
   wrote, its waiting count said to be counted there. The first server
   stopped after the second's 10 s wait: the second's page says **Held
   by no server** with why, never the first's line again.
4. The Mac's host stopped: its card Away since, then Last seen after the
   grace.
5. On the Windows machine, the desktop app's server (inside WSL) and a
   `conductor host` started in a shell of the same WSL distribution
   against it: the app's Hosts page shows that host on the server's own
   machine key (one machine, `Linux · x64 · WSL`), with no networking
   set up.
6. Nowhere on the page a control that starts, moves, continues or
   retries work; the footer's line.

**Part B, the second half, after PR-5, PR-6 and CS1's rows**: two servers of
one workspace, the desktop app's server on the Windows machine (in WSL)
and a server on the Mac, each signed in with the sync provider
registered. On each, Other machines shows the other server with its
platform and Last reported, its runs that a shared ticket links
**Reported by** its name and time, and a decision waiting there, with
no link or control; the person's own other
server shows its hosts. Then, mid-session: the sync service made
unreachable (the staging service stopped by the owner) and the
subscription lapsed (in staging): each time the section becomes its one
line and the first half is unchanged, and it returns with no restart.

**What Done means.** HS2 is Done when part A and part B have been
observed and recorded in `docs/features.md`'s Hosts entry, not when the
rows are merged. Part A can be observed and recorded on its own, after
PR-3; the item stays In review until part B. **What needs the owner:**
the review of this plan and of each PR; the workspace, sign-ins and
staging services for part B, and the lapse, which only the owner can set
in staging.
