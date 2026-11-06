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
switchyard it publishes to (or, on a switchyard, the servers publishing
to it). That half is free and whole (articles 1, 2 and 4). When a sync
provider is registered (Program item CS1) and current, the same page
gains a second section, **Other machines**: the workspace's other
servers, as the sync service last heard from them, the runs each holds
and the decisions waiting there, as the sync provider hands them over.
When the service is unreachable, the subscription has lapsed or the
person is signed out, that section shrinks to one line saying so and
since when, and the page is exactly the first half again, mid-session,
with no restart (article 8).

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
  PR-2 (#647).
- HS1's PR-12 (`docs/plans/history.md`, "what a host reports of its
  sessions"): the hosted session's `where` and its git facts. Not needed;
  the page shows a hosted session's `where` when its host reports one and
  says "not reported by the host" otherwise.
- CS1's open-source rows (its public part will be
  `docs/plans/sync-seams.md`): the remote records other machines hold
  (`ext.Host.PutRemote`, `DeleteRemote`, `ClearRemote`, the data
  directory's `remote/`, `GET /api/remote`) and the sync status
  (`ext.SyncStatus`, `GET /api/sync`), all in its OS-4 row. PR-5 below
  waits for them.
- ID1 (identity and workspaces): a workspace's devices. The cloud and
  premium rows below wait for it through CS1.

So PR-1 to PR-3 build on `main` as it is and ship the first half; PR-4
waits for DR1 and MB0; PR-5 waits for CS1's OS-4; PR-6 (the sync
service) and PR-7 (the provider) wait for CS1's and ID1's hosted rows.

**This plan follows the final text of two plans under review, in the
other repositories.** CS1's plan (sync and continuity: what replicates
per workspace, run pointers and decision copies among it, the remote
records a device is handed, the sync status and its outage and lapse
behaviour) and ID1's plan (identity and workspaces: a workspace's
devices, servers and switchyards, their names, platforms and
revocation). Where either changes in review, this plan takes the change;
what HS2 needs that they lack is said below (Owner decisions, 11 and 12)
as findings sent to each.

**The items this one meets, and who owns what they share.**

| Shared piece | Owner | This plan |
|---|---|---|
| `proto.HostSession`'s provenance fields and `session_info` | HS1, `docs/plans/history.md` (PR-12) | adds nothing to `HostSession`; adds `machine` and `platform` to `proto.HostInfo` and keeps the `version` the server drops today (PR-1). Both touch `internal/proto`, `internal/hostagent` and `internal/signal`: whichever merges second rebases on the other, never beside it |
| The decision record, its routes and states | DR1, `docs/plans/decisions.md` | counts a run's open decisions through DR1's reads and links to them; adds no field (PR-4) |
| Needs your decision (`/decisions`) and its items | MB0, `docs/plans/decision-inbox.md` | links each waiting count to MB0's page; answers nothing on its own page |
| The sync seam (`ext.ContinuityStore`, `ext.Doc`, the adapter's allowlist) | C6, `docs/plans/ext.md` | adds one document to the allowlist, the host summary (PR-5) |
| The remote records, their doors and files, the sync status | CS1 (its OS-4) | reads them; asks CS1 for two record kinds, `device` and `hosts` (Owner decisions, 11); adds them itself in PR-5 if CS1's OS-4 has not |
| A workspace's devices: names, kinds, platforms, revocation | ID1 | reads a device's name, platform and last report through the sync service (Owner decisions, 12) |
| `GET /api/switchyard/status` and the switchyard's landing page | the switchyard (`internal/api/switchyard_page.go`) | its host grouping uses the machine key of PR-1 when a host reports one; the figures and the page are otherwise unchanged |
| Who holds a run's decisions while its owner is away | CS1 (the managed copy) and MB1 | shows the holder; never offers to answer or apply elsewhere |

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
   session, untouched. Export leaves out the machine id and the remote
   records, which belong to this machine and to the provider (PR-1, and
   CS1's OS-4). A decision waiting on another machine is shown, never
   answered or approved from here.
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
   never waits on the network. The premium PR (PR-7) carries the outage
   and lapse tests.
9. **`CGO_ENABLED=0`.** No cgo, no new store: the machine id is one file,
   the remote records are CS1's files, the recently seen hosts are held
   in memory.
10. **The repository contract.** This plan before code; one issue,
    branch and PR per row; race tests per package; the protocol change
    (two fields of `HostInfo`, the session's host fields, two routes) in
    `internal/proto`, `web/app/utils/protocol.ts` and `docs/protocol.md`
    together, each with its bound and test; no new dependency, so no
    `make notices`.
11. **Names.** "Host" keeps its meaning (a machine running `conductor
    host`); "Hosts" names the page that lists every machine, servers
    among them, as the sidebar's machine tag already does; "holds" is the
    word for a run's owner. Nothing here is called graph or continuity.
12. **Execution behind one seam.** Not touched: nothing starts a
    process. The page shows the `where` a session or an attempt reports
    (the Runtime's, `native` until Boxcar), never assuming it.

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
  id of PR-1 groups a machine's `conductor host` processes, so a Mac
  hosting three sessions is one card): name, platform, version and the OS
  user as the host reported them, its availability (**Connected**,
  **Away since 14:02** while a session waits in its 60 s grace, **Last
  seen 13:40** for a machine that left within the last 24 hours of this
  server's life), its sessions and questions waiting, each a link to the
  session. A host's card says **Holds no runs**: a host runs sessions,
  and only a server's ledger holds a managed run.
- **The switchyard** this server publishes to: its address as the
  configuration names it, whether this server's publications are
  connected, how many sessions are published. On a switchyard the section
  is **Servers publishing here**, one card per publisher, as the
  landing page counts them.
- **Runs and who holds them**: every open managed run, its crew, its
  state, its tasks waiting for a person, and **Held by this server**;
  or, when another process on this data directory holds the ledger,
  **Held by another server on this data directory** with the pid and
  start time that process wrote, said as what it wrote, and the run
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
time the sync service last heard from it, never "online"), and below it:

- for the person's own other servers, the summary that server last sent
  (its hosts, their availability as it saw them, its counts of sessions
  and questions);
- for every server, the runs it holds that sync shares with this one (a
  run pointer, with the device that sent it as its holder: **Held by
  studio**) and the decisions waiting there, each read only, with the
  sync service's own link to answer where CS1's plan gives one.

When the status is unreachable, behind past its bound, lapsed or signed
out, Other machines is one line: "Other machines are not shown: the sync
service has not answered since 14:02" (or "the subscription has lapsed",
or "you are signed out"), and the rest of the page is unchanged.

## The changes

### `internal/machine`, `internal/proto`, `internal/hostagent`, `internal/signal`, `internal/session` (PR-1): what a host says of its machine

- `proto.HostInfo` gains two optional fields:
  - `machine`: the host machine's id, 32 lowercase hex digits, random
    (`crypto/rand`), never a hardware or OS identifier. `conductor host`
    reads it from `<dataDir>/machine-id`, the data directory it resolves
    as a server does (`CONDUCTOR_DATA_DIR` or `~/.conductor`), and makes
    the file once (mode 0600, written through `internal/store`'s atomic
    write) when it is not there; a file it cannot read or that does not
    match the pattern means none reported, and a warning once. A server
    on the same machine reads the same file, so its own row and its
    hosts on that machine share one key (`hostMachine` in
    `internal/api`). `internal/export`'s `leftOut` gains `machine-id`
    ("this machine's own: a restore elsewhere makes its own"), so an
    archive restored on another machine never gives two machines one id.
    The uplink (a server publishing to a switchyard) sends it too: the
    switchyard groups a publisher's sessions by it, as PR-2 does.
  - `platform`: `<GOOS>/<GOARCH>`, with `+wsl` when the host runs in
    WSL, at most 32 bytes, in the pattern
    `^[a-z0-9]{1,15}/[a-z0-9]{1,15}(\+wsl)?$`. WSL is read once from
    `/proc/sys/kernel/osrelease` (a release containing `microsoft`,
    case-folded); no build tag (the file is not there off Linux).
  - Both are read by a new package, `internal/machine` (standard library
    and `internal/store` only): `machine.ID(dataDir) (string, error)`
    and `machine.Platform() string`, the second through a seam a test
    gives its own release string. `internal/hostagent` and
    `internal/api` both use it (`internal/api` does not import
    `internal/hostagent`, and neither should for this);
    `docs/repository-map.md` gains its row.
- `signal.Hub.Register` checks both at their bounds (a register past one
  refused with `ErrBadRegister`, as `Name` and `User` are), keeps
  `HostInfo.Version` (dropped today; at most 64 bytes, the pattern of
  `version.Version`), and keeps the machine id hashed: the session's
  `machineKey` is the first 16 hex digits of SHA-256 of `machine`, so the
  id itself is never served. A host that reports no machine (an older
  host) gets the key of its `owner` hash (`ownerOf(instance)`, one
  process) and its card says "one process; this host does not report its
  machine".
- `session.Info` gains `hostPlatform`, `hostVersion`, `hostKey` (the
  machine key) and `hostAwaySince` (the private `disconnectedAt` of a
  hosted session, set while its status is `host_disconnected`), each
  omitted when empty. They are in `GET /api/sessions` and the `session`
  event as every `Info` field is; `web/app/utils/protocol.ts`'s session
  type and `docs/protocol.md`'s session table gain them.
- `api.PublishedSession` gains `Connected() bool`, true while the
  publication's connection to the switchyard is up (`hostagent.Published`
  answers from its agent's connection state under its lock), so PR-2
  can say whether this server's publications are connected without a
  round trip.
- `docs/protocol.md`'s "Host control connection" lists the two fields
  with their bounds; `protocol.ts`'s `HostRegister` type gains them.

### `internal/api`, `internal/crew` (PR-2): `GET /api/hosts`

A new file `internal/api/hosts.go` builds the model the page draws from,
from what the server already holds, under each holder's own lock, read
once per request; nothing is persisted.

- **Route.** `GET /api/hosts`, admin (`requireAdmin`): the workbench token
  or an identity with the admin grant, as `GET /api/runs` takes. Not on
  Brakeman's shared reads and never through a share link. On a switchyard
  it answers too (its publishers are its hosts).
- **Reply.**

  ```json
  {
    "at": "2026-11-05T14:05:00Z",
    "this": {"key": "…", "kind": "server", "name": "linuxbox", "platform": "linux/amd64",
             "version": "v0.7.0", "availability": "here",
             "sessions": {"running": 3, "waiting": 1}, "where": ["native"]},
    "hosts": [{"key": "…", "kind": "host", "name": "studio", "platform": "darwin/arm64",
               "version": "v0.7.0", "user": "ana", "availability": "connected",
               "since": "…", "sessions": {"running": 2, "waiting": 1},
               "sessionIds": ["…"], "machineReported": true}],
    "switchyard": {"url": "https://…", "publishing": 2, "connected": 2},
    "publishers": [],
    "runs": [{"id": "…", "name": "…", "crew": "…", "state": "running", "paused": false,
              "waiting": 1, "heldBy": "this"}],
    "ledger": {"holder": "this", "note": ""},
    "more": {"hosts": 0, "runs": 0}
  }
  ```

  - `kind` is `server` (this server), `host` (a machine hosting sessions
    here) or `publisher` (a server publishing to this switchyard).
  - `availability` is `here` (this server), `connected` (at least one of
    the machine's hosted sessions has a live connection), `away` (none
    has, at least one is in its grace; `since` the earliest
    `hostAwaySince`), or `seen` (no session left; `since` when the last
    one went, from the recently seen table below).
  - `sessions.running` counts the machine's sessions that are starting,
    running or away; `waiting` those whose attention is `needs_input`
    (the same rule as the Yard's count, `needingInput`).
  - `runs` lists the engine's open managed runs (`engine.List()`, a run
    whose state is not final), newest first; `waiting` is the run's
    `needsInput`, and from PR-4 its open decisions too. `heldBy` is
    `this` when this server's ledger holds `runs/owner.lock`, `other`
    when another process holds it, `none` when the filesystem has no
    locks (the ledger read-only, as `TakeOwnerLock` says).
  - `ledger.note` is, for `other`, the holder's line as that process
    wrote it into the lock file (`pid 4242 since 2026-11-05T13:00:00Z`),
    which the page shows as "as that server wrote it": nothing trusts it
    (`internal/store/lock_unix.go`), and the page says so. `crew.Engine`
    gains `LedgerHolder() (holder string, note string)`, answering from
    the ledger's state (`Held`, and the reason its last try was refused,
    which `awaitLock` already has) without trying the lock again.
  - `switchyard` is present when this server publishes
    (`config.RendezvousServer()` non-empty): `url` is `Publisher.Server()`
    (scheme, host and path, never user information, a query or a
    fragment, as it is shown today), `publishing` the sessions
    published, `connected` those whose `Connected()` is true.
  - `publishers` is present on a switchyard only: one row per publishing
    machine, kind `publisher`, as `hosts`.
- **Bounds.** At most 256 host or publisher rows and 500 runs; past
  either, the oldest rows are left out and counted in `more`. A row's
  `sessionIds` at most 64. The reply at most 256 KiB
  (`hostsReplyMax`, checked by `TestHostsReplyBounded` with every field
  at its longest).
- **Recently seen.** `hostsSeen`, a table in memory keyed by machine key:
  name, platform, version and the time the machine's last session left
  (`Hub.OnChange` with a removal, the hub's `Expire` after
  `DisconnectGrace`). At most 64 entries, the oldest dropped first, an
  entry dropped after 24 hours or when the machine connects again;
  empty after a restart, said on the page ("since this server started at
  09:12").
- **Names.** This server's `name` is `os.Hostname()` (at most 120 bytes,
  the hub's bound for a host name), its `platform` `machine.Platform()`,
  its `key` the machine key of its own `machine-id` (made on first need,
  as a host makes it). A host machine's name is the newest of its
  sessions' `HostName`, its user the newest `HostUser`; two sessions of
  one machine that disagree show the newest and the card says
  "reported as 2 names".
- **The switchyard's landing page.** `switchyardCounts` groups by the
  machine key when a session has one, by `HostName` as today otherwise;
  its JSON and its page are unchanged.
- `docs/protocol.md` gains the route's row with its fields and bound;
  `web/app/utils/protocol.ts` the `HostsReply` type.

### `web/app` (PR-3): the Hosts page

- `pages/hosts.vue` (route `/hosts`): three sections, **This server**,
  **Hosts** (on a switchyard, **Servers publishing here**) and **Runs and
  who holds them**, and the footer line of Why and what happens. It reads
  `GET /api/hosts` on open and again, debounced to once a second, on the
  `session`, `removed` and `run` events the layout's attention store
  already streams (`useAttention.ts`, `/api/events`), and every 30 s for
  the `seen` times; no new event and no polling of its own beyond that.
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
  count and **Held by this server** or **Held by another server on this
  data directory** with the note. No other control: no Stop, Continue,
  Retry, Move or Launch on this page; they live on the run page.
- `utils/hosts.ts`: the pure model (the platform's words, the
  availability's words and order, the sort: this server, then connected,
  away, seen, each by name), the debounce, and `remoteSection` (PR-5).
- The nav: `layouts/default.vue`'s `nav` gains **Hosts**
  (`i-lucide-server`, `to: '/hosts'`) after **Events**, its rail label,
  its chord; `BottomBar.vue` lists it under More; `utils/palette.ts`'s
  `PAGES` gains `hosts` and `usePaletteRows.ts` its row.
- `docs/features.md`: the entry **Hosts: the machines this server knows,
  and who holds each run**, with the by-hand observation of Acceptance,
  part A.

### `internal/api`, `web/app` (PR-4): the decisions waiting

- `hosts.go` counts each run's open DR1 decisions (`Ledger` reads DR1's
  PR-4 serves, `state=open`, by `run_id`) into the run's `waiting`, and
  this server's `decisions` (open decision records) beside its
  `sessions.waiting`. A hosted session's question stays a question
  (DR1: a managed run's attempts are server sessions, so no decision
  record is ever a host's).
- The page links each waiting count to MB0's page: a session's to
  `/decisions?item=question:<session>/<question>` when one question
  waits, a run's to `/decisions?item=decision:<id>` for one decision,
  `/decisions` otherwise. It answers nothing itself.
- Tests below; `docs/protocol.md`'s row gains `decisions`.

### `internal/api`, `ext`, `ext/exttest`, `web/app` (PR-5): other machines

- **The host summary, a sync document.** The sync adapter's allowlist
  gains one document: store `hosts`, path `summary`, the first half of
  `GET /api/hosts` reduced to what may leave the machine: this server's
  platform and version, its counts (sessions running and waiting, runs
  held and waiting), and each host and publisher row's name, platform,
  version, availability, `since` and counts, at most 32 rows and 16 KiB
  (`ext.MaxHostSummary`). Never a session's id, command, folder,
  branch, a run's name or anything a terminal printed; never the OS user
  or the machine id. The adapter marks it pending when the model's
  reduced form changes (a hash of its JSON compared with the last
  delivered), at most once per 30 s, and lists it in every replay. With
  no provider `local.Continuity` drops it like every document. Its type
  is `ext.HostSummary` (standard library only, with a `Valid` method, as
  every record a provider is handed has).
- **Two record kinds of the remote records.** CS1's `ext.Remote.Kind`
  (`decision`, `pointer`, `pins`) gains `device` (a workspace device's
  record: `{id, name, kind, platform, version, lastReport, revoked}`, at
  most 1 KiB) and `hosts` (another server's host summary, as above). If
  CS1's OS-4 has taken them (Owner decisions, 11), this PR only reads
  them; otherwise it adds them to `ext.Remote`, its `Valid`,
  `exttest.FakeHost` and the `Continuity` check, beside OS-4's kinds.
- **The model.** `GET /api/hosts` gains `other`, present when `sync` is
  in the capabilities:

  ```json
  "other": {"state": "current", "since": "…",
            "machines": [{"device": "…", "name": "studio", "platform": "darwin/arm64",
                          "version": "…", "lastReport": "…", "own": true,
                          "summary": {"…": "…"},
                          "runs": [{"id": "…", "crew": "…", "state": "running", "heldBy": "studio"}],
                          "decisions": {"open": 1}}],
            "more": 0}
  ```

  - `state` is the sync status's (`current`, `behind`, `unreachable`,
    `lapsed`, `signed_out`) and `since` its time; for anything but
    `current` (and `behind` for less than 10 minutes, which a backlog of
    uploads is), `machines` is empty: the page shows the one line, and
    the remote files stay where the provider wrote them.
  - `machines` is one row per `device` record that is not this server
    (the sync status's own device id, Owner decisions, 11) and not
    revoked, joined with that device's `hosts` summary (present only for
    the person's own servers, `own: true`, Owner decisions, 6), its
    `pointer` records whose run is not final (each `heldBy` the device's
    name: the device that sent a pointer is the server whose ledger
    holds it, in CS1's records) and its `decision` records whose state
    is open. At most 64 machines and 200 runs per machine, counted in
    `more`.
  - A run appears once: a remote pointer never has a local run's id from
    this ledger, and a pointer whose device is this server is dropped.
- **The page.** `pages/hosts.vue` gains **Other machines** after Hosts:
  a `HostCard` per machine, kind `remote`, availability **Last reported
  14:05** (`neutral`; never **Connected** or **Here**), its summary's
  hosts as rows inside it ("as studio last saw them"), its runs as
  `HostRunRow`s with **Held by studio** and no link but the sync
  service's own for a decision where CS1 gives one (OS-4's link), and the
  one line for a state that is not current. `utils/hosts.ts`'s
  `remoteSection` is the pure part, tested on its own.
- `docs/protocol.md`: the route's `other`, the document's row in the
  sync section; `protocol.ts` the types. `docs/features.md`'s Hosts
  entry gains Other machines and its observation, Acceptance, part B.

### Cloud (PR-6): the sync service

In the cloud repository, in words (its files are private): the sync
service's wire contract gains the two record kinds of PR-5 and the host
summary document, with their bounds; the service keeps a server's last
report time by a rule (every authenticated call of the device, at most
one write a minute), writes a `device` record to every device of the
workspace for each live server device (from identity's device record:
name, kind, platform, version, last report) and deletes it when the
device is revoked or retired, and relays a server's `hosts` summary to
that person's other devices only (scope personal). Its tests: the
contract's vectors for both kinds, the visibility rules (another
member's devices see the `device` record, never the `hosts` summary),
revocation deleting the record everywhere, the last-report rule, the
bounds.

### Premium (PR-7): the provider

In the premium repository, in words: the sync provider's remote worker
hands the two kinds to `Host.PutRemote` and `DeleteRemote` as it does
OS-4's, uploads the `hosts` document with scope personal, and answers
the sync status with the server's own device id and name (Owner
decisions, 11). Its tests are the outage and the lapse (article 8): with
the service unreachable mid-session the page's `other.state` is
`unreachable` and the first half is unchanged, and it returns to
`current` with no restart; with the subscription lapsed the same with
`lapsed`; uploads queued in the outbox and never waited on.

## Events, orderings and invariants

No state machine of the engine, the ledger or a session changes. What
changes is one register message's fields, one sync document, and a
model read from existing state.

**A host machine's availability**, per machine key, read when the page
asks:

| From | Event | To |
|---|---|---|
| (none) | a session of the machine registers (`Hub.Register`) | `connected` |
| `connected` | its last connected session's link drops (`HostDisconnected`) while another is in its grace | `away`, `since` the earliest `hostAwaySince` |
| `away` | a session reattaches (`attach`) | `connected` |
| `away` or `connected` | its last session is removed (ended, or `Expire` after `DisconnectGrace`, 60 s) | `seen`, `since` the removal, held in `hostsSeen` for 24 h |
| `seen` | a session of the machine registers | `connected`; the `hostsSeen` entry dropped |
| `seen` | 24 h pass, the table passes 64, or the server restarts | gone from the page |

Invariants:

- **I1. A run has one holder, and the page names it from the ledger,
  never from a guess.** Locally, the holder is the process that holds
  `runs/owner.lock`; the page reads it from the ledger's own state and
  never takes or tries the lock. Remotely, it is the device that sent the
  pointer. A host never holds a run (`Launchable` refuses `where: host`
  on `main`), and its card says so.
- **I2. Nothing on the page acts on work.** Its only actions are links
  to existing pages of this server (`/runs/<id>`, `/sessions/<id>`,
  `/decisions`) and, for another machine's decision, the sync service's
  own link. No button starts, stops, continues, retries, moves or
  launches anything, and no route of this plan writes (`GET /api/hosts`
  is its only route). A test of each half proves it (Tests).
- **I3. Never "online" from a report.** Another machine's availability
  is the time the sync service last heard from it, said as a time;
  `connected` and `here` are only ever said of what this server sees
  itself.
- **I4. The second half is the sync status's, read at the moment of the
  reply.** `other` is built from the status read once per reply, then the
  remote files; a state that is not current empties `machines` in that
  same reply, so the page never shows another machine beside a state
  that says the service is gone. The status is answered from memory
  (CS1), so the reply never waits on the network.
- **I5. What leaves the machine is the summary's allowlist.** The host
  summary is built from the model's reduced form only (ids of sessions,
  commands, folders, branches, run names, OS users and the machine id
  never in it), checked at its bound by `ext.HostSummary.Valid` before
  it is queued.
- **I6. The machine id identifies a machine to its own servers only.**
  It is read and written on the machine, hashed by the hub before
  anything is kept, never served, never in an export, never in the host
  summary.

**Orderings.** The page reads `GET /api/hosts` after the events it
already gets (`session`, `removed`, `run`), so it can lag an event by its
debounce (one second) and never shows a state older than its last read.
The sync document is marked pending after the model's change (the
adapter's rule for every document: queued under no lock of the ledger or
the hub, delivered by its own goroutine), so a host summary on another
machine lags by at most the 30 s coalescing plus the delivery.

**What each store holds.** `<dataDir>/machine-id`: 33 bytes (the id and a
newline), 0600, made once. Memory: `hostsSeen`, at most 64 entries. The
remote files are CS1's (`remote/<device>/device-<id>.json` and
`hosts-summary.json` in its layout). The ledger, Dispatch, the sessions'
stores: unchanged.

**Migration.** None: no schema changes. An older host sends neither new
field and keeps working (its card says what it does not report); an
older server ignores them (`json.Unmarshal` of the register ignores
unknown fields, as HS1's PR-12 relies on too).

**Protocol.** Every place it lives:

| Change | Places | Bound | Test |
|---|---|---|---|
| `HostInfo.machine`, `HostInfo.platform` | `internal/proto/hostmsg.go`, `protocol.ts` (`HostRegister`), `docs/protocol.md` (Host control connection) | 32 hex; 32 bytes and the pattern; the register within `MaxHostMessage` | `TestRegisterHostMachineAndPlatformBounds` |
| `HostInfo.version` kept | `internal/signal/hub.go`, `docs/protocol.md` | 64 bytes, `version.Version`'s pattern | `TestRegisterKeepsHostVersion` |
| `session.Info` `hostPlatform`, `hostVersion`, `hostKey`, `hostAwaySince` | `internal/session/info.go`, `protocol.ts` (`Session`), `docs/protocol.md` (sessions) | as above; `hostKey` 16 hex | `TestHostedInfoCarriesHostFields` |
| `GET /api/hosts` | `internal/api/hosts.go`, `protocol.ts` (`HostsReply`), `docs/protocol.md` (routes) | 256 KiB, the row bounds | `TestHostsReplyBounded` |
| the host summary document | `internal/api/sync.go`, `ext` (`HostSummary`), `docs/protocol.md` (sync) | 16 KiB, 32 rows | `TestHostSummaryValid`, `TestSyncDeliversHostSummary` |
| remote kinds `device`, `hosts` | `ext` (`Remote`), `docs/protocol.md`; the sync service's contract (PR-6) | 1 KiB; 16 KiB | `TestRemoteDeviceAndHostsKinds` |

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
  switchyard, a publisher to it nor a workspace device: a host connected
  to another server is seen only through that server's summary, by the
  same person.
- Answering a decision of another machine from this page; that is the
  sync service's and MB1's.

## Tests per surface

The checks per surface are `docs/checks.md`'s. Go: `make lint`, `go test
-race -count=1 -timeout 30m ./...` (the whole module), `make lint-static`.
UI: `npm --prefix web run typecheck`, `npm --prefix web test`, `make
test-e2e` (the new specs alone first). No Python and no desktop change.

**`internal/proto`** (PR-1): `TestRegisterHostMachineAndPlatformBounds`
(each field at its bound and one past, the pattern's refusals),
`TestHostInfoOmitsEmpty` (an older host's register byte for byte).

**`internal/machine`** (PR-1): `TestMachineIDMadeOnce` (two reads, one
file, 0600, the same id), `TestMachineIDUnreadableReportsNone` (a file of
the wrong form: an error, no id), `TestPlatformReadsWSL` (an
`osrelease` with `microsoft-standard-WSL2` read through the seam gives
`+wsl`; without, none).

**`internal/hostagent`** (PR-1): `TestUnreadableMachineIDSendsNone` (none
sent, one warning), `TestRegistrationCarriesMachine`
(both sent to a server and to a switchyard), `TestPublishedConnected`
(a publication's `Connected` false when its connection drops, true
after the reconnect).

**`internal/signal`** (PR-1): `TestRegisterKeepsHostVersion`,
`TestMachineKeyHashesTheID` (the id never in `Info`, the key 16 hex),
`TestOlderHostKeyedByProcess` (no machine: the owner hash),
`TestHostAwaySinceSetAndCleared` (disconnect sets it, reattach clears it).

**`internal/session`** (PR-1): `TestHostedInfoCarriesHostFields` (JSON
names and omission).

**`internal/export`** (PR-1): `TestExportLeavesMachineIDOut` and the
restore's refusal of one in an archive.

**`internal/api`** (PR-2): `TestHostsThisServer` (name, platform,
version, here, counts), `TestHostsGroupsAMachine` (three `conductor host`
registrations with one machine id are one row; two machines with one
host name are two), `TestHostsAvailability` (connected, away with its
`since`, seen after `Expire`, gone after 24 h through the table's clock
seam, never a sleep), `TestHostsSeenBounded` (65 machines, the oldest
dropped), `TestHostsRunsHeldByThis` and `TestHostsRunsHeldByOther` (a
second ledger on the same data directory holding the lock: `other` with
the note, read without a try of the lock), `TestHostsLockless` (the
filesystem's refusal gives `none`), `TestHostsSwitchyardPublishing`
(a fake publisher: `url`, `publishing`, `connected`),
`TestHostsOnASwitchyard` (publishers, no runs),
`TestHostsReplyBounded`, `TestHostsAdminOnly` (a host token, a share link
and Brakeman's principal refused), `TestSwitchyardCountsByMachine`
(the landing page's figures grouped by key, its JSON unchanged),
`TestHostsWritesNothing` (the route over a server whose stores are
observed: no commit).

**`internal/crew`** (PR-2): `TestLedgerHolderAnswersFromState` (the
holder and note as the last try left them; no new try).

**`web/app` vitest** (PR-3): `utils/hosts.test.ts` (the platform's words
for each form and an unknown one, the availability's words and order,
the sort, the debounce through fake timers), `components/HostCard.test.ts`
(each availability's badge and colour; **Holds no runs** for a host; a
host that reports no machine), `components/HostRunRow.test.ts` (the two
holders' words; the only control is the run's link: no button rendered,
which is I2's test for the first half).

**Playwright** (PR-3): `web/e2e/hosts.spec.ts`: the e2e server and a
real `conductor host` with the stub agent (`fixtures.ts`'s
`hostedSession`, its teardown by name, `stopStrays` first): the page,
reached by `go hosts` after the layout's sidebar root is drawn, shows
This server with Here, the host's card with its platform and
**Connected**, its session as a link; the session's question (the stub's
ask) makes the card's waiting count 1, waited on by the count itself;
the host process stopped by its process group makes the card **Away
since**, then **Last seen** after the grace (the spec's own wait bounded
by `DisconnectGrace` plus the maintenance tick, with `test.setTimeout`
above it); a managed run of the example crew is listed **Held by this
server** and links to its run page; the page has no button whose name is
start, stop, move, launch, continue or retry, checked again after a
redraw the spec causes (a second host connecting). Counting only what the
spec made, by its host name.

**`internal/api`** (PR-4): `TestHostsCountsOpenDecisions` (two open, one
answered: 2; by run), `TestHostsHostedQuestionStaysAQuestion`. vitest:
the links' targets in `utils/hosts.test.ts`. Playwright: `hosts.spec.ts`
gains a gate's decision on the example crew's run: the run's waiting
count 1, its link opening `/decisions` on that item.

**`ext`** (PR-5): `TestHostSummaryValid` (each field at its bound and
past; a session id, a path or a user refused by shape),
`TestRemoteDeviceAndHostsKinds` (if PR-5 adds them). **`ext/exttest`**:
the `Continuity` check sends the summary document; `FakeHost` takes the
kinds.

**`internal/api`** (PR-5): `TestSyncDeliversHostSummary` (a host's
connection changes the summary: one delivery after the coalescing, the
fake clock's seam; an unchanged model none; listed in a replay),
`TestHostSummaryLeavesOutTheLocal` (a session id, cwd, branch, run name,
user and machine id never in the bytes delivered),
`TestHostsOtherMachines` (seeded remote files: a device, its summary,
two pointers, one final, a decision open; one machine row, one run held
by its name, one decision), `TestHostsOtherOnlyWhenCurrent` (each status
but current, and behind past 10 minutes, empties `machines` in the same
reply; current again shows them, no restart), `TestHostsOtherDropsThis`
(a device and pointers of this server's own device id dropped),
`TestHostsOtherBounded`, `TestHostsNoOtherWithoutSync` (no provider: no
`other` field). vitest: `remoteSection` in `utils/hosts.test.ts` (the
state's line for each state; never "Connected" for a remote row; I2 for
the second half: a remote run row renders no link but the decision's).
Playwright: `web/e2e/hosts-remote.spec.ts`, seeding `remote/` in the e2e
server's data directory before it starts as CS1's `remote.spec.ts` does,
and `hosts-free.spec.ts`, the plain server's page with no Other machines
section. The section's appearance needs a registered sync provider,
which this repository has none of, so its state lines stay vitest-only;
the spec's header says so.

**Cloud** (PR-6) and **premium** (PR-7): their repositories' checks,
with the tests named in their sections above; the premium PR's outage
and lapse tests are article 8's for this item.

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
7. **The ledger's evidence rows carry `where`.** The page shows a
   session's `where` as reported (`native` today; "not reported by the
   host" for a host that reports none, HS1's rule) and assumes nothing of
   a run's attempts.
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
| PR-1 | open source | machine, proto, hostagent, signal, session, export: a host's machine and platform, the version kept, the session's host fields, a publication's connection | `internal/machine` (new), `internal/proto`, `internal/hostagent`, `internal/signal`, `internal/session`, `internal/export`, `internal/api` (`publish.go`'s interface), `web/app/utils/protocol.ts`, `docs` | none (`main` as it is) | none | ~550 + 650 |
| PR-2 | open source | api, crew: `GET /api/hosts`, the runs' holder, the recently seen, the switchyard's grouping | `internal/api`, `internal/crew`, `web/app/utils/protocol.ts`, `docs` | PR-1 | none | ~600 + 750 |
| PR-3 | open source | web: the Hosts page, its cards and run rows, the nav and the palette; `hosts.spec.ts`; the features entry | `web/app`, `web/e2e`, `docs/features.md` | PR-2 | none | ~650 + 600 |
| PR-4 | open source | api, web: the decisions waiting, counted and linked to Needs your decision | `internal/api`, `web/app`, `web/e2e`, `docs/protocol.md` | PR-3; DR1's PR-4 (#637) and MB0's PR-2 (#647) on `main` | none | ~300 + 450 |
| PR-5 | open source | api, ext, web: the host summary sent through sync, other machines read from the remote records, the page's Other machines; the features entry's second half | `internal/api`, `ext`, `ext/exttest`, `web/app`, `web/e2e`, `docs` | PR-3 (PR-4 if merged first, rebased); CS1's OS-4 on `main` | none | ~700 + 750 |
| PR-6 | cloud | the sync service: device records and host summaries in the remote feed, the last-report rule, the contract | the sync service, its store and its contract | CS1's service rows that serve the remote feed; ID1's device records with name, platform and version; an open-source tag holding PR-5's `ext` types | PR-7 (other repository) | ~450 + 650 |
| PR-7 | premium | the sync provider: the two kinds handed over, the summary uploaded, its own device in the status; the outage and lapse tests | the sync provider's client and registration | PR-6 deployed to staging; CS1's provider rows; the same open-source tag | PR-6 | ~300 + 550 |

**PR-1**: the two `HostInfo` fields and their bounds; `machine-id` made
and read through `internal/machine` (`ID`, `Platform`); the hub's checks, the version kept, the
machine key; `session.Info`'s four fields; `PublishedSession.Connected`
and `Published`'s answer; `leftOut`'s row; the three protocol places. No
page yet; `GET /api/sessions` carries the new fields, which the sidebar
ignores. Tests: `internal/proto`, `internal/hostagent`, `internal/signal`,
`internal/session`, `internal/export` above. Ordering with HS1's PR-12
and MB0's PR-6, which touch the same three packages: whichever merges
second rebases; never beside them.

**PR-2**: `hosts.go`, the reply and its bounds, `hostsSeen`,
`LedgerHolder`, the switchyard's grouping; the route's row. Tests:
`internal/api` and `internal/crew` above.

**PR-3**: the page, `HostCard.vue`, `HostRunRow.vue`, `utils/hosts.ts`,
the nav, the bottom bar and the palette; `hosts.spec.ts`; the features
entry with Acceptance part A's record (written from what was seen, by
hand, before the PR is marked ready). After this PR the first half is
shipped.

**PR-4**: the decision counts and links, once DR1's routes and MB0's
page are on `main`. Tests above.

**PR-5**: `ext.HostSummary` and (if CS1's OS-4 has not) the two remote
kinds; the adapter's document; the `other` model; the page's section;
the two specs; the features entry completed. Tests above. This is the
last open-source PR, and it ends the item's open-source work.

**PR-6 and PR-7**: in the cloud and premium repositories, in their own
words and checks; their PR bodies tick article 3 (each feature names the
hosted dependency that gates it: here, the sync service). They follow
CS1's and ID1's PR order and are cut when those rows are planned in
their repositories' issues.

## Owner decisions

Dated 2026-11-05. Each is a default the owner may overturn with a row in
the Program's status log, its convention for a decision the owner did
not choose (the Program, section 3.1).

1. **The open decisions named with this item are not its own.** O4
   (human policy v1) blocks SW1, O5 (graph v1 tooling) blocks CG1, O8
   (packaging and pricing) blocks the public price, and O10 (Dispatch's
   merge rules) blocks CS1; HS2 resolves none and needs none resolved
   (O10 only through CS1, its prerequisite). No question is left open by
   this plan.
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
11. **Findings for CS1's plan** (sync and continuity), sent with this
    plan: (a) the remote records gain a `device` kind, one per live
    server device of the workspace with its name, kind, platform,
    version and last report, written by the service to every device of
    the workspace and deleted on revocation or retirement; (b) the
    service's last report of a device needs a rule (every authenticated
    call of the device, at most one write a minute), since the plan
    names the field and nothing updates it; (c) a `hosts` document kind,
    personal scope, carrying the host summary of PR-5 (16 KiB); (d) the
    sync status names this server's own device id and name, so the page
    can tell itself from its workspace's other servers; (e) the list of
    a workspace's devices the plan mentions for HS2 is a person's route,
    unreachable from a server: HS2 takes the `device` records instead
    and does not need that route; (f) a pointer carries no count of what
    waits on the run, so a remote run's waiting count comes only from
    the decision copies sync shares. If CS1's plan takes (a), (c) and
    (d) into its OS-4, PR-5 here only reads them.
12. **Findings for ID1's plan** (identity and workspaces): (a) a
    device's `platform` is free text; HS2 asks that a server enrol with
    this plan's form (`linux/amd64+wsl`, at most 20 characters, which it
    fits) and that the field reach the sync service with the device's
    name; (b) the device's version is recorded by its sync call but has
    no column; (c) the device records a service reads carry no name,
    platform or last sync, which the `device` record of 11 (a) needs;
    (d) whether a signed-in `conductor host` is a device, and of which
    kind, is implied only by a test: HS2 shows a host through the server
    it connects to and asks that a host never be listed as a server that
    holds runs.

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
   on the same data directory shows the run **Held by another server on
   this data directory**, with the line that server wrote.
4. The Mac's host stopped: its card Away since, then Last seen after the
   grace.
5. Nowhere on the page a control that starts, moves, continues or
   retries work; the footer's line.

**Part B, the second half, after PR-5 to PR-7 and CS1**: two servers of
one workspace, the desktop app's server on the Windows machine (in WSL)
and a server on the Mac, each signed in with the sync provider
registered. On each, Other machines shows the other server with its
platform and Last reported, its runs that a shared ticket links **Held
by** its name, and a decision waiting there; the person's own other
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
