# Sync's seams: Dispatch shared between machines, UI state in the data directory, decisions answered elsewhere, and what other machines hold (Program item CS1, its open-source rows; issue #996)

This is the public plan of the open-source rows of Program item CS1
(sync), OS-1a to OS-4. Its own row, OS-0 (issue #996), is this file and
the amendments it makes to `docs/plans/dispatch.md`,
`docs/plans/dispatch-evidence.md` and `docs/plans/decisions.md` (quoted
whole at the end). It is reviewed and merged before any of those rows'
code is cut, as this repository's plan-before-code rule asks, and every
open-source row is built from it alone.

It was written against `main` at `f10eb297` (2026-11-18) after reading
`ext` (`continuity.go`, `host.go`, `identity.go`, `decisions.go`,
`registry.go`, `exttest/`), `internal/api` (`sync.go`, `exthost.go`,
`extensions.go`, `capabilities.go`, `dispatch_plans.go`,
`decisions.go`), `internal/dispatch` (`schema.go`, `rules.go`,
`ops.go`, `plans.go`, `launches.go`, `links.go`, `external.go`,
`publications.go`, `model.go`), `internal/export` (`leftout.go`,
`restore.go`), `internal/crew` (`decisions.go`) and
`internal/crew/ledger` (`ledger.go`, `pointer.go`, `decisions.go`),
`internal/session/screen.go`, `internal/agents/skill.go`, and in
`web/app` the palette, the editor's layout, the sidebar, the crew graph,
the run and project pages' view switches and Settings → The app
(`components/AboutCard.vue`).

**What these rows are.** Seams that any registered sync provider (C6's
`ext.ContinuityStore`, `docs/plans/ext.md`) uses, and nothing else: a
Dispatch store that reads its own log from a position and applies
another store's rows by DS1's field rules; UI state kept in the data
directory instead of one browser's storage; a decision answered away
from its server and handed in by a provider, recorded and applied by
DR1's own path after the identity provider rechecks it; and a read-only
view of what a provider says other machines hold. This repository knows
nothing of what a provider does with what it is handed, where it keeps
it, whom it serves or how it is reached. Every door below refuses a
caller that is not the registered sync store, and the only gate any row
knows is whether a sync provider is registered. With none (every build
of this repository) the server behaves as `main` does, with one
exception that improves the free build: UI state lives in the data
directory (OS-2).

**Prerequisites, on `main`.** C6 (the sync adapter, `internal/api/
sync.go`; export and restore, `internal/export`); C9 (`GET
/api/capabilities`, `ext/exttest`'s `Continuity` check and `FakeHost`);
C2 (the identity provider seam); DS1 and DS2 (`docs/plans/dispatch.md`,
`docs/plans/dispatch-evidence.md`: the `changes` log, its one order and
`rules.go`'s `Class`, `Merge` and `Reconcile`); IN1a
(`docs/plans/tracker-linking.md`: the counterpart group and
`ReconcileCounterpart`, the row kinds `observe` and `adopt`, the store's
instance `store_instance` with `dispatch.Reissue` at a restore, and the
refusal `external_linked`); RV1 and PV1 (reviews, findings, captures and
their sends: entities that do not replicate, below); DR1 with every row
(`docs/plans/decisions.md`: the records, `Engine.RespondDecision`,
`ConfirmAuthority`, `not_held`); MB0's decision page with DR1's records
on it (`docs/plans/decision-inbox.md`); the decide grant
(`docs/plans/decide-grant.md`, #776). Dispatch's store is at schema
version 7 and the ledger at 12; each migration below takes the next
version at its merge (8 and 13 today).

**Not on `main`, and what waits for it.** HS2's PR-5
(`docs/plans/hosts.md`) defines `ext.HostSummary`, the body of the
`hosts` remote kind (OS-4 bounds that kind's record until then). The
adapter's computed sources (HS2's PR-5 or TS1's PR-5,
`docs/plans/team-history.md`) are not on `main`; OS-3's decision
document uses them if they have merged and its own store name
otherwise (OS-3). No provider is registered in this repository, ever:
the doors are exercised through `ext/exttest.FakeHost` and the real
server's handlers in Go tests.

**Names.** A machine's copy of Dispatch is a **replica**. The short id a
replica writes its rows under is its **tag**, handed to it by its
provider; a row's **origin** is the tag it was written under. What a
provider keeps of a decision is its **copy**; an answer a provider keeps
for an owner who is away, and hands in later, is a **held answer**. What
a machine is shown of another machine's decisions, runs, pins, devices
and hosts is a **remote record** (`ext.Remote`, `remote/`), never a
replica. "Workspace" here is only the provider's name for the group of
machines a project is shared with: an opaque id this repository stores
and compares, never reads. Conductor is the application, Yardmaster the
harness that holds a decision's wait and applies its answer, Dispatch
the ticketing component whose items are tickets.

**The items this one meets, and who owns what they share.**

| Shared piece | Owner | This plan |
|---|---|---|
| The sync seam: `ext.ContinuityStore`, `ext.Doc`, the adapter's allowlist, export and restore | C6, `docs/plans/ext.md` | adds three data documents and a decision document to the adapter, `Host` doors used only by the sync store, a restore door to another provider's directory, and two optional interfaces (`ext.SyncStatus`, `ext.DispatchWatcher`) |
| Dispatch's field classes and merge rules (the Program's open decision O10) | DS1 and DS2, `rules.go` | applies them to rows from another replica, and adds the replica's identity and the rows' origins, number blocks and namespaces, a project's share and its rekey, plan views and launches only where approved; amends both plans |
| The counterpart group of a linked ticket | IN1a, `docs/plans/tracker-linking.md` | merges it on a receiver without another machine's snapshot; marks two tickets of one tracker item |
| The decision record, its respond and apply path, its rechecks | DR1, `docs/plans/decisions.md` | builds a bounded document of each record; hands in a held answer through DR1's path with the authority `via: sync`; amends DR1's plan |
| The decision page and its four rows | MB0, `docs/plans/decision-inbox.md` | lists another machine's decisions beside the local records, read only |
| The decide grant | `docs/plans/decide-grant.md` | a held answer to a decision opened to the grant is admitted under it |
| The host overview | HS2, `docs/plans/hosts.md` | gives the remote kinds `device` and `hosts` and the sync status's own device; HS2 defines `ext.HostSummary` |
| History and skills across machines | TS1, `docs/plans/team-history.md` | gives `outputGuard.Check`, the remote records and `ext.SyncReport`, which TS1 extends |
| The identity provider seam | C2, `docs/plans/ext.md` | adds an optional `ext.SubjectChecker` beside `ext.IdentityProvider` |

## Articles it serves

1. **No paid code.** Every row adds a seam of `ext` and the server's
   side of it. The one check is a registration's: `409 no_sync` where no
   sync provider is registered, and each door refused (`ErrUnsupported`)
   on a `Host` given to any other provider. No row checks a plan, a
   licence or an account, and no word names a hosted service.
2. **The open-source build is fully useful.** With nothing registered,
   Dispatch, decisions, the decision page and every safety control work
   as on `main`; UI state moving into the data directory (OS-2) makes
   the free build better: one layout, one set of palette recents and the
   pinned sessions for every browser of a server, and in its export.
3. **Nothing paid behind a flag.** Sharing a project, receiving another
   machine's rows, a held answer and a remote record exist only when a
   provider registered through `ext` calls the doors. No setting, flag
   or tag turns any of them on.
4. **Stop, inspect, revoke, export, approvals free.** A held answer is
   recorded only through DR1's respond path and applied only by DR1's
   application on the server whose ledger holds the run, after the
   identity provider says yes again at that moment; "not known" never
   applies and never supersedes. A replica never launches, continues or
   retries another machine's run, and a plan revision approved on
   another machine is launched here only on this machine's operator's
   approval. `conductor export` carries the new documents; the
   operator's cancel and stop are untouched.
5. **Dependencies point one way.** `ext` stays standard library only;
   nothing new is imported anywhere.
6. **No feature build tag.** None is added.
7. **The free default is `main`.** `local.Continuity` keeps nothing and
   calls no door; with no provider no row is applied from elsewhere, no
   answer is handed in and no remote record is written.
8. **A provider that fails is the free default.** Every door answers
   from the server's own stores and never waits on a provider; a
   provider that stops calling leaves the server working alone, its log
   and its held rows where they were.
9. **CGO off.** The new index is `modernc.org/sqlite`, the stores'
   driver.
10. **The contract.** This plan before the code; race tests per surface,
    vitest and Playwright for the app's screens; `docs/protocol.md`
    changes with every route; no `internal/proto` frame changes
    (Protocol, below).
11. **Names keep their meaning.** "Sync" for the feature, never
    "continuity", which on `main` means a managed run continued after a
    restart (`internal/crew/continue.go`); the interface keeps its Go
    name, `ext.ContinuityStore`.
12. **Execution behind one seam.** Nothing here starts a process. A held
    answer reaches a run only through DR1's application, whose dispatch
    is the engine's.

## Why and what happens

Every Conductor server is its own island: Dispatch's tickets live in one
data directory, a decision waits on the one machine that runs the work,
and layouts and palette recents live in one browser's storage. These
rows let a registered provider join islands without this repository
knowing how. On a build where one is registered:

- **A project shared.** The project page shows **Share this project**
  when `GET /api/capabilities` lists `sync`. Pressing it reserves the
  project's next 1,000 numbers and marks it pending; the provider sends
  its history and, once the project is shared, every change to it after
  its commit. A project never shared is never handed out.
- **A second machine.** Another server whose provider receives the
  project applies its rows (tickets, comments, labels, milestones, the
  ticket's sets, saved views, plan revisions as their views) and edits
  them as its own. Concurrent edits merge by DS1's field rules, the same
  on every machine whatever order the rows arrive in. A ticket made while
  the provider is unreachable is made at once with a number of the
  machine's own block or range. A plan revision made on studio shows
  "approved on studio" and is launched only on studio, on studio's own
  operator's approval.
- **Decisions seen elsewhere, answered once.** A decision a managed run
  opens is handed to the provider as a bounded document (OS-3). Another
  machine shows it, read only, on its ticket's page and on the decision
  page, marked "on studio". If the provider holds an answer for it from
  a person away from studio, it hands the answer to studio's server when
  it can; studio records it through DR1 once its identity provider
  admits the person's signed statement of that answer for that decision,
  and applies it once, rechecked again at application. Nothing another
  machine holds can record or apply it.
- **Run pointers.** A run's state shows on other machines' ticket pages
  for a shared ticket's run, named with the machine that holds it
  ("running on studio"), and nothing there can start, continue or retry
  it.
- **UI state follows the person.** The editor's and the sidebar's
  layouts, the palette's recents and the pinned sessions are kept in the
  data directory; a provider may carry them to the person's other
  machines, where a layout and the recents are taken as that machine's
  own and another machine's pins show in the sidebar, read only.
- **A replacement machine.** `conductor restore --provider` lets the
  restoring provider write another registered provider's directory (its
  sign-in), restores the UI state and pins, and the shared projects then
  arrive by replication as on any second machine. No ledger travels.
- **Settings → The app** gains a **Sync** row saying what the provider
  reports: current, "unreachable since 14:02; 37 changes waiting",
  lapsed, signed out, and any change kept back because the copied-output
  check found it quoting captured output.

What a person does not see change: a build with no provider (every build
of this repository), and every project not shared, behave exactly as on
`main`.

## The changes

### What this repository hands a provider, and what it never does

**Terminal output never leaves the server.** The bytes Conductor
captured from a session or a check (its screen, scrollback and ring
buffer, its activity log's words, a check's log, a history record's
words) are handed to no provider by any door or document below. Every
field handed out is an id, an enum, a time, a number, a name the person
chose (a session's name in a pin, a palette recent's key of the kinds
named under OS-2), or text a person or an agent wrote into Dispatch or a
decision through a route whose bounds refuse control characters; no
path on this machine is handed out. Such text can still quote captured
output (an agent's paste and a person's alike), so each free-text value
is judged when it is written by the copied-output check (OS-1c for
Dispatch's rows, OS-3 for decision records), its verdict stored beside
it, and every hand-out carries the verdict, so no provider reads
captured output to judge it. What counts as terminal output for that
check is the bytes Conductor captured, as the owner decided on
2026-11-20 (What counts as terminal output, below; Owner decisions, 27).
A provider may apply checks of its own to what it is
handed and keep back what they flag; what it keeps back it names in its
status (`SyncReport.Withheld`, OS-4), and this repository applies no
other check to it.

**Dispatch's rows** (OS-1a, OS-1b), handed out by
`Host.DispatchChanges` whole, each with its origin, its two verdicts and
whether it came from another replica, and the entities it needs
(`Refs`). Only rows whose (entity, op,
field) replicates are ever handed out or applied (`Replicates`,
`rules.go`; Owner decisions, 21):

| Entity | Ops | Fields that replicate | Kept on this machine |
|---|---|---|---|
| `project` | `create`, `set` | `id`, `created_at`, `key` (logged; projected from the registry key on a receiver, OS-1b), `name`, `description`, `tracker`, `tracker_url`, `close_requires`, `archived_at` | `cwd` (a path on this machine), `next_number`, `number_from`, `number_to` and every column OS-1a adds (each replica's own allocation) |
| `ticket` | `create`, `set`, `add`, `remove`, `delete`; `observe` and `adopt` for `title` and `description` only | the immutable fields, `number`, `status`, every scalar but `external_snapshot` (`closed_on` among them), and the sets `assignees`, `labels`, `relations`, `links` | `external_snapshot` (each machine reads the tracker itself, OS-1a); the set `attachments` |
| `comment` | `create`, `set`, `delete` | every field of its class table | |
| `label`, `milestone` | `create`, `set`, `delete` | every field of its class table | |
| `view` | `create`, `set`, `delete` | every field, for a view whose `project_id` is a shared project | a view whose project is `''` |
| `person` | `create` | `name`, `created_at` | |
| `plan` | `create`, `set` | the immutable fields, the create row naming the revision's **view** (`ViewDigest`) beside the whole body's digest; `state`, the approval and withdrawal scalars | the body itself |
| any other | none | | `launch` (execution's record), `review`, `finding`, `capture`, `capture_send`, `publication`; `uploads`; the `ops` receipts; `tickets_fts`; `seq`; the derived tables (`link_reads`, `ticket_reads`, `store_instance`, `capture_sends_open`); rows of the actor `merge` (each replica derives its own) |

A scalar `set` row's `prior` (the value it replaced, which no merge
reads) is judged apart from its value: a row may be handed on with its
prior left out (`PriorKept`), its identity and value unchanged, and a
receiver stores such a row with an empty prior. A remove's `prior` (the
tags it observed) is always needed: the set's merge reads it.

**Plan views** (OS-1a). A revision's body (DS2's `PlanBody`) holds the
saved crew whole (its `cwd`, `setup` and every check's argv) and each
agent's command, start and yolo arguments, which stay on the machine.
What is handed out is the revision's **view**, version 1: the body's
`base`, `references`, `comments`, `constraints`, `decisions`, `note` and
`attachments` (names), the crew's tasks (`id`, `title`, `needs`, the
agent's name, the prompt) and the agents by name; never `crew.cwd`,
`crew.setup`, a check's or a task's argv, an agent's command, start or
yolo arguments, or a manifest file's contents. The view is canonical
JSON with `{v: 1, digest: <the whole body's digest>}`, at most
`dispatch.MaxPlanBody`, kept and handed out under its own SHA-256 (the
view digest). The whole body's digest stays the revision's identity
everywhere; a store that holds only the view cannot launch it (`409
body_elsewhere`, OS-1a).

**Run pointers.** C6's pointer document as on `main` (`id`, `crew`,
`state`, `outcome`, `paused`, `revision`, `continuedFrom`,
`continuedBy`, `createdAt`, `closedAt`), with one field added (OS-4,
Owner decisions, 23): `ticket`, the ticket whose launch made the run's
chain (the ledger's `runs.ticket`, DS2's, which continuations carry),
present only while that ticket's project is shared here and absent for
every other run, a run of a ticket in a project not shared among them.
Never the run's record, the crew as launched,
its tasks, attempts, checks, logs, events, receipts, worktree paths,
members' messages or checkpoints, and no count of what waits on it.

**Decision documents** (OS-3), one per decision, as the table under
OS-3 says: the request's words and ids, the accepted response with what
was answered, the states, never the session, the authority, the
fingerprints, the history or any evidence a ref names.

**UI documents** (OS-2): `ui/layout.json` whole; `ui/palette.json`
with only the recents of the kinds `go:`, `ticket:`, `crew:`, `run:` and
`action:` (never a file's path, a session, a joined link, an artifact, a
member or an answer-all key); `ui/pins.json` with each pin's name under
the copied-output check (from OS-1c on), an output-like name delivered
empty. The server's own copies keep everything, for the free build.

**Never handed out, by anything here**: the workbench token, `links/`,
`tls/`, `hooks/`, `ext/` (every provider's own state), `history/`,
`brakeman/`, `remote/`, `output-index.sqlite`, `ui/writer`, the
ledger's other tables and every session's output. C6 hands
`catalog.json`, `crews/` and `runs/` as it does on `main`; this item
adds no use of them (Owner decisions, 3).

### `internal/dispatch` and `ext` (OS-1a): rows from elsewhere

**Schema version 8** (the next at its merge; migration keyed 7), row
OS-1a0, which ships alone before OS-1a; additions only:

- `CREATE TABLE replica_tags (tag TEXT PRIMARY KEY, workspace TEXT NOT
  NULL, device TEXT NOT NULL, instance TEXT NOT NULL, joined_at TEXT NOT
  NULL, active INTEGER NOT NULL)`: at most one row active, the rest
  history; `instance` the store's instance (`store_instance`, IN1a's)
  when it joined.
- `CREATE TABLE replica_names (tag TEXT PRIMARY KEY, name TEXT NOT
  NULL)`: other replicas' device names, which the provider writes
  (`DispatchNames`, OS-1b), for "made on studio".
- `changes` gains `origin TEXT NOT NULL DEFAULT ''`, `output TEXT NOT
  NULL DEFAULT ''` and `prior_output TEXT NOT NULL DEFAULT ''` (the
  copied-output verdicts of the row's value and of its scalar prior,
  judged apart and written with the row by OS-1c: `clean`, `output`,
  `pending` while the index cannot judge, or `''` for a row written
  before the check existed), `recovered_by TEXT NOT NULL DEFAULT ''`
  (the tag of the replica that sent another origin's row in a recovery,
  below; `''` for a row its origin sent) and `remote INTEGER NOT NULL
  DEFAULT 0` (1 for a row applied from another replica).
- `CREATE TABLE held_rows (seq INTEGER PRIMARY KEY, project_key TEXT NOT
  NULL, why TEXT NOT NULL, waits_for TEXT NOT NULL DEFAULT '', row TEXT
  NOT NULL, held_at TEXT NOT NULL)` (`waits_for` the entity a held row
  needs, as `kind:id`) and `CREATE TABLE held_bodies (digest TEXT
  PRIMARY KEY, body BLOB NOT NULL)` (the views that came with held
  rows).
- `CREATE TABLE plan_views (view_digest TEXT PRIMARY KEY, digest TEXT
  NOT NULL, view BLOB NOT NULL)`.
- `projects` gains `shared TEXT NOT NULL DEFAULT ''` (the workspace's id
  once shared), `share_state TEXT NOT NULL DEFAULT ''` (`pending`,
  `completing`, `shared`, `dormant`, `refused`), `share_upto INTEGER NOT
  NULL DEFAULT 0`, `share_reserved_to INTEGER NOT NULL DEFAULT 0`,
  `block_tag TEXT NOT NULL DEFAULT ''` (the tag its blocks were handed
  to), `next_from` and `next_to` (the block asked ahead),
  `overflow_from`, `overflow_to` and `overflow_next` (INTEGER, default
  0), `provisional_from`, `provisional_to` and `provisional_next`
  (INTEGER, default 0; the window below) and `registry_key TEXT NOT NULL
  DEFAULT ''` (the key the workspace's registry holds for a project
  received or shared, OS-1b).
- `plans` gains `view_digest TEXT NOT NULL DEFAULT ''` and `body_here
  INTEGER NOT NULL DEFAULT 1` (`plans.body` is `''` where it is 0).

The migration's Go step (`migrationsGo[7]`) makes the view of every
revision the store holds from its `plans.body`, in the migration's
transaction, writing `plan_views` and `view_digest`, so a revision made
before the upgrade has its view like any other
(`TestMigrationMakesTheViewOfEveryRevision`). Every other version 7 row
is right as it is (no project was shared), so nothing else is derived
and no `migrate` row is written. A version 8 database opens read-only
under a version 7 binary, as DS1's rule says.

**Schema version 9** (migration keyed 8, #1049), the store step after
OS-1b, which ships alone before the code that moves onto it; additions
only. Two values of OS-1b's had no column of their own: the overflow
range a join hands the replica, which OS-1b wrote on every project (lost
for a store with no project yet), and the words a share was given (a
refusal's, a dormant or unshared workspace's, a retire's), which it kept
in the process's memory (lost at a restart). No build on `main` wrote
either: OS-1b's code had not merged when version 9 did. Version 9 gives
each a column:

- `ALTER TABLE replica_tags ADD COLUMN overflow_from INTEGER NOT NULL
  DEFAULT 0` and `ALTER TABLE replica_tags ADD COLUMN overflow_to
  INTEGER NOT NULL DEFAULT 0`: the overflow range on the tag it was
  handed to.
- `ALTER TABLE projects ADD COLUMN share_words TEXT NOT NULL DEFAULT
  ''`: the words the project's share was given.

Once OS-1b moves onto them, `Join` writes the range on the tag's row and
copies it to every project, `Retire` zeroes both, and a project made or
received after the join copies the active tag's (`replicaRanges`).
`projects.overflow_from`, `overflow_to` and `overflow_next` stay written:
they are each project's copy with its next number in it, which a create
reads in the one row it numbers from (`allocateNumber`); nothing hands
them out, and a version 8 build opens a version 9 store read-only, so no
older build reads them. A refused share is `share_state` `refused` (a
value the state already had), numbered as a project not shared, its rows
kept here, and a rekey allowed (`TestARefusedShareOutlastsARestart`).

A fresh store makes them at the end of its `CREATE TABLE` statements;
the migration adds them in the transaction that read version 8. Every
version 8 row is right as it is at the defaults (no range on a tag, no
words on a share), so nothing is derived and no `migrate` row is
written; a migration that fails leaves version 8 as it was. A version 9
database opens read-only under a version 8 binary.

**The store's instance.** IN1a's `store_instance` is the instance these
rows judge by: the create and the migration write it, and a restore
writes a new one (`dispatch.Reissue`, called by `internal/export`'s
`Install`), so a store restored from an export is never the instance it
was exported from, and two stores restored from one export are two
instances. A raw copy of a data directory keeps it, and is then the
same instance and, to its provider, the same device: a number both
copies take is held as any duplicate is (below), and what a provider
does about two processes of one device is the provider's (Owner
decisions, 22).

**The replica's identity and the rows' origins.** A local row is written
with its raw actor and `origin` `''`. `Store.Join(ctx, tag, workspace,
device string, overflowFrom, overflowTo int64) error` writes an active
`replica_tags` row and, in the same transaction, sets `origin` to the
tag on every local row whose origin is `''`; from then on each local row
is written with the active tag as its origin. A row's **canonical
actor** is `@<origin>/<actor>` (`rules.go`'s `Canon(actor, origin)`): a
remote row is stored with its actor qualified and its origin, the tag in
both, at most `MaxRemoteActor` (72) bytes. A tag is 1 to 8 bytes of
lower-case letters and digits (`dispatch.ValidTag`, `ext.ValidTag`). A
local actor may not begin with `@` (`Op.check` refuses it, `ErrBadActor`,
from this version on; no actor on `main` does). `Merge`, `Less`, `Order`
and `Reconcile` compare canonical actors, so every replica orders the
same rows the same way; the qualification applies to every identity a
row carries, its own and the tags a remove's `prior` names. An origin,
once set, never changes: a store that joins again under a new tag (its
device enrolled again, or a store restored as another instance and
retired at its open) moves the old tag to history and qualifies only the
rows written since, so history keeps its identity on every replica and
two stores restored from one export write under two tags. The receipt
lookup of a resend (DS1: a local row `(actor, op_id, 0)`) reads rows of
this store's origins only (`TestResendLooksUpLocalRowsOnly`).

**Nothing allocated to a tag outlives it, and nothing is written under
an allocation that has been invalidated.** A tag's blocks, its overflow
range and a share in progress (its reserved numbers) were handed to one
tag of one store, and a copy of the data directory carries them all.
Every allocation is usable only by the instance and the active tag it
was handed to, and the store judges that itself, before it accepts any
write:

- `Store.Open`, in the open's first transaction (before `Open` returns,
  so before any route, any provider's `Start` or any create can run),
  compares the active tag's `instance` with `store_instance`; when they
  differ (a restore from an export) it retires every allocation in that
  transaction.
- `Store.Retire(ctx) error` does the same on demand: the provider calls
  it when the identity its tag was joined under is no longer its own
  (Events, A provider's life, below), and `Join` calls it in its own
  transaction.
- Retiring clears every project's current and next blocks (`number_from`,
  `number_to`, `next_from`, `next_to` to 0, `block_tag` to `''`) and the
  overflow range, and turns a `pending` or `completing` share back to
  unshared with the words "sharing was interrupted when this machine's
  data was copied: share it again".
- A create in a shared project takes a block or overflow number only
  when `block_tag` is the active tag and the active tag's `instance` is
  this store's. Otherwise (after a retire, before the `Join` that brings
  a fresh overflow range, offline after a restore) it takes a number from
  a **provisional window** of 1,000,000 numbers drawn at random above
  2,000,000,000 when the store retired (past every overflow range), so a
  create never waits, and two stores restored from one export draw two
  windows (a collision, which is unlikely, is held, never failed, as any
  duplicate number is).
- So no two stores ever allocate from one block, and no create of a
  restored store runs before its inherited blocks are retired
  (`TestRestoredStoreRetiresAtOpenBeforeAnyWrite`: a store restored from
  an export and opened with the provider's `Start` held at a gate in
  `FakeHost`, then a create in a shared project: its number from the
  provisional window, never the inherited block;
  `TestTwoStoresRestoredFromOneExportNeverShareANumber`: both restored,
  both joined, concurrent creates in one shared project on each, and
  again with both offline before they join).

**Reading the log.** `Store.ChangesFrom(ctx, after int64, limit int)
([]ChangeRow, int64, error)`: whole rows (value and prior) after the
position `after`, at most `MaxChangesPage` (1,000, new, equal to
`ext.MaxDispatchBatch`) and 4 MiB encoded (`ext.MaxDispatchBytes`), in
`seq` order, local and remote alike, and the position to read from next
(a limit past `MaxChangesPage` refused); it hands only the rows that
replicate (`Replicates`), its next position past the rows it skipped, and
`Store.HandedChanges` only those of a project shared, completing or
pending (the history a share hands out) and the people's, which is what
`DispatchChanges` hands a provider. Main's `MaxFeedPage` (5,000,
`feed.go`) stays as it is for `ChangesAfter`, Brakeman's changes route
and the History index. `ChangeRow` gains `Origin`, `RecoveredBy`,
`Output`, `PriorOutput`, `Remote` (`Refs` of a row is computed where the rows are handed out, `dispatch.Refs`, never kept on the row, which stays comparable)
and, for a plan's create, `ViewDigest` (from `plans.
view_digest`, so a revision made before version 8 is named as one made
after). `Store.RevisionView(ctx, viewDigest string) ([]byte, error)`: a
revision's view, made in the transaction that makes the revision (and by
the migration for those before it), kept in `plan_views`.

**Reading a revision held as its view.** `Store.Plan` (the revision's
read, `dispatch.PlanView` on `main`), `planBodyHanded` and the launch's
read of a body (`launches.go`) read `plans.body` only where `body_here`
is 1; for a revision held as its view they read `plan_views`. The
revision's read gains `bodyHere`, `view` (its tasks, agents by name,
comments and notes) with `body` absent, `origin` and `approvedOrigin`
(the device names of the tags that made and last approved it, from
`replica_names`; `''` for this machine). `internal/api`'s
`dispatch_plans.go` derives what it reads from a whole body (the crew's
paths, the agents' commands) only from a body here. The web app's plan
view (`utils/plans.ts`, `utils/dispatchPlans.ts`, and `protocol.ts`'s
revision types) draws a view revision's tasks and comments, "made on
studio" and "approved on studio", and no Launch
(`TestViewOnlyRevisionReadsItsView` in `internal/api`;
`utils/planView.test.ts` and the plan's drawing in vitest).

**Applying another replica's rows.** `Store.ApplyRemote(ctx, rows
[]ChangeRow, views map[string][]byte, keys map[string]string) (Applied,
error)`, one transaction:

1. Refuse the batch, applying nothing, when a row's (entity, op, field)
   does not replicate (`ErrNotReplicated`, naming it), when a row has no
   origin or a canonical actor past its bound (`ErrBadActor`), when the
   store has no active tag, or when it names a project held here whose
   `share_state` is `''`, `pending`, `completing` or `refused` (a local
   project, which only its own share makes shared: `ErrNotShared`). A
   provider never sends such a batch; a defective one changes nothing
   here.
2. **Receive** a project not held here whose `create` row is in the
   batch (or already in `held_rows`): it is made from that row with
   `shared` the active tag's workspace (`replica_tags`), `share_state`
   `shared` and its registry key from `keys`, so from then on its
   creates here take block, then overflow, numbers, its local rows are
   handed out as a shared project's are, and `project.rekey` on it is
   `409 shared` (`TestFirstBatchOfAnUnknownProjectApplied`,
   `TestReceivedProjectIsShared`). A project of `dormant` state is
   received the same way and turns `shared`. A row of a project neither
   held here nor created in the batch is an orphan (step 5).
3. Drop a row whose canonical identity the log holds (DS1's immutable
   class).
4. Record each project's registry key from `keys`
   (`projects.registry_key`, OS-1b).
5. Write to `held_rows` and `held_bodies`, untouched and in the same
   transaction: the rows of an incoming project whose registry key a
   local unshared project holds ("CND is this machine's own project:
   give it another key to receive the workspace's"); the rows of an
   entity this store does not hold and whose create is not in the batch
   (an orphan, "held: CND-12's first change has not arrived"); every row
   whose references are not all here (References, below); and the
   create of a ticket whose (project, number) a ticket here already
   holds ("CND-12 was numbered twice: kept on the machine that numbered
   it first"). So no batch of a correct provider fails for good, and its
   follower never stalls.
6. Insert the rest with their qualified actors, origins and
   `recovered_by`, `remote` 1.
7. Project each field by its class: a project's `key` and its tickets'
   keys from the registry key, never from a row (OS-1b); every other
   field by `Merge` (scalar last writer wins, sets observed-remove, logs
   appended); the counterpart group by `ReconcileCounterpart` over this
   replica's rows (below).
8. Run `Reconcile` over every invariant the batch touched, writing its
   `reconcile` rows under the actor `merge`; maintain `tickets_fts`;
   bump each entity's `rev`.
9. Apply the held rows the batch made applicable (an orphan's create or
   a missing reference arrived), in the one order, until nothing more
   applies.
10. Then `OnCommit` and `OnChange`, as for a local commit.

A plan's create arrives with its view (`views`, by view digest), stored
with `body_here` 0 and `plans.body` `''`. A local bound is not applied
to a remote add (DS1: a merge never refuses one). Two stores holding the
same rows reach the same row tables whatever batches they arrived in
(`TestReplicasConverge`, whose generated logs put children before their
parents in some splits). `RemoteApplied {Applied int; Held []string;
Words string}` (named so beside the reviews' `Applied`): how many rows
were applied, the held rows' words (at most 50, each at most 200 bytes)
and one line for the page.

**References.** `rules.go` gains `Refs(row ChangeRow) []EntityRef`,
every entity a row needs by the schema's foreign keys: a ticket's
project; a comment's, link's, assignee's and plan's ticket; a relation's
two tickets; a label's and a milestone's project; a ticket-label add's
ticket and label. `ApplyRemote` applies a row only when each of them is
held here or created earlier in the batch's one order; otherwise the row
is held with `waits_for` naming the first one missing ("held: CND-12's
comment waits for its ticket"), and each create applied replays the
held rows waiting for it, in the one order and in the same transaction.
A reference the schema does not enforce (`parent_id`, `milestone_id`) is
applied and read as unknown until its entity arrives, as a deleted one
is (DS1). A provider in another module cannot import
`internal/dispatch`, so `Host.DispatchChanges` hands each row's
references with it (`DispatchChange.Refs`, each `kind:id`, at most 3,
filled from `Refs`). A provider that withholds an entity's `create`
withholds every later row of that entity (the same `Entity` and
`EntityID`) and every row whose `Refs` names it, so a row waits here
only for an entity still on its way (`TestDispatchChangesCarryRefs`: a
comment, a ticket-label add and a relation handed out, each change's
`Refs` naming its ticket, its label and its far end as `Refs` does;
`TestWithheldCreateWithholdsItsDependants`, with `exttest.FakeHost`: a
ticket's create kept back, the rows a provider then sends by `Refs`
leave no held row on the receiver; `TestChildBeforeParentHeldThenApplied`:
a comment before its ticket, a ticket-label add before its label, a
relation before its far end, in every order;
`TestOrphanRowsHeldUntilTheirCreate`).

**Held rows are bounded and durable.** At most 10,000 held rows per store
and 64 MiB of held bodies; past either `ApplyRemote` refuses the batch
with `ErrHeldFull` and the words "too many changes from other machines
wait for what has not arrived: N held", which the provider reports
(`ext.SyncReport`, OS-4) and stops following until the held rows drain.
Held rows survive a restart (`TestHeldRowsSurviveARestart`), and each
start replays those that have become applicable.

**Tracker-linked tickets on a receiver** (IN1a's counterpart group).
IN1a merges a ticket's `external_id`, `external_url`,
`external_snapshot` and `authority` as one group, and its rules 3 and 5
read the linked item's snapshot rows, which never leave a machine
(`external_snapshot` does not replicate). So a replica merges the group
over the rows it holds: every replica's rows of the group but their
snapshots, and its own snapshot rows. Rules 1, 2 and 4 read no snapshot
and run as written, so every replica holding the same rows has the same
linked item and the same owners, and a field Dispatch owns is DS1's
scalar result as before. Rule 3 keeps this replica's own latest read of
the decided item as `external_snapshot`, or `''` when it has none;
`external_url` is that read's `url` or, with none, the decided item's
last `external_url` row in DS1's order (an import's, a link's or a
move's). A field the merged `authority` gives the tracker takes rule 5's
text when this replica's snapshot names it, and otherwise rule 5's own
fallback (the last `observe` row and an import's `create`, in DS1's
order). Whenever a batch brings a row of the group from another
replica, `ApplyRemote` drops this replica's `ticket_reads` row for the
ticket, so its next open reads the tracker with this machine's own
credential, writes its own snapshot and writes the tracker's text only
where it changed, as any refresh does. A machine with no credential for
the tracker shows the text and the link the rows carried, with "not
read on this machine": the address, the title and the description are
then each the last of its rows in DS1's order (an import's, an
observe's, a person's), until this machine reads the item itself. What converges is each replica's read of the
tracker, never a snapshot copied between machines
(`TestCounterpartMergedWithoutRemoteSnapshots`: A imports and
refreshes, B receives: the item, the owners, the URL, the title and the
description as A's, no snapshot on B and B's `ticket_reads` row gone;
B's read through a fake tracker writes B's snapshot and no row; an
unlink on A while B refreshes: B's snapshot dropped and the adopted text
kept on both; a take-over on B while A refreshes: the Dispatch text on
both).

**One tracker item, two tickets.** IN1a refuses an import or a link of an
item a live ticket here already has (`external_linked`), a rule of the
local write, so two tickets of one item arise only from imports or links
made on two replicas before either held the other's ticket. They are
kept side by side, never folded: a fold would have to merge two numbers,
two histories, their comments, relations and run links, for which DS1's
rules have no operation, and would rewrite immutable rows on every
replica. Each replica derives, never as a row, the other live tickets of
its shared projects linked to the same item (`Store.TicketsLinkedTo(ctx,
externalID string) ([]string, error)`, over IN1a's `tickets_external`
index); the ticket's read carries them as `alsoLinked` (their keys, at
most 8), and the ticket page says so (OS-4). Publications stay each
ticket's own, each made by a person on that ticket
(`TestSameItemImportedOnTwoReplicasKeptAndMarked`: both creates applied
on both replicas, neither held nor renumbered, each naming the other;
an import on a third replica after both arrived refused
`external_linked`; an unlink of one clears both marks).

**Local writes by the same rules.** From the first `Join` on, every local
write is projected as a remote row is: its row's `at` is the later of
the server's clock and one microsecond past the newest row the log holds
for that field (a scalar's entity and field; a set's or a log's
entity), and the field's value is what `Merge` makes of its rows, so a
person's edit made after a later-dated edit arrived from a machine whose
clock runs ahead wins on every machine, as it did on the screen.
`setScalars` (`ops.go`) and every writer of a scalar (`comments.go`,
`labels.go`, `milestones.go`, `views.go`, `acceptance.go`, `plans.go`,
`projects.go`, `external.go`) take their stamp from one function,
`stampFor`, so no writer sets a column the log would not make. Before
the first `Join` a store stamps as on `main`. The projection is then a
function of the log for local and remote rows alike
(`TestLocalEditAfterALaterDatedRemoteEditWins`;
`TestLocalAndRemoteWritesConverge`, a property test of two stores
interleaving local writes and each other's batches, their row tables
equal at the end).

**Launches only where approved, and only where the body is.**
`launchable` (`launches.go`) also refuses a revision whose body is not
here (`409 body_elsewhere`: "this revision's crew is on studio: launch
it there") and one whose `approved_by` was last written by a row of
another origin (`409 approved_elsewhere`: "approved on another machine:
approve it here to launch it here"). `ApprovePlan` accepts such a
revision, already `approved`, again from this machine's operator,
writing this origin's approval rows (today it refuses "approved
already"). So a revision is launched only on the machine that made it,
and only on that machine's own operator's approval
(`TestRemoteApprovalDoesNotLaunch`,
`TestLocalApprovalOfARemoteApprovalLaunches`,
`TestViewOnlyRevisionDoesNotLaunch`).

**`ext`** (`ext/dispatch.go`, new):

```go
// DispatchChange is one row of Dispatch's log as the sync store is handed
// it, or hands back from another replica.
type DispatchChange struct {
	Seq             int64     `json:"seq,omitempty"`  // this store's position; never compared across replicas
	Entity          string    `json:"entity"`
	EntityID        string    `json:"entityId"`
	TicketID        string    `json:"ticketId,omitempty"`
	ProjectID       string    `json:"projectId,omitempty"`
	Field           string    `json:"field,omitempty"`
	Op              string    `json:"op"`
	Value           string    `json:"value"`           // at most MaxDispatchValue bytes
	Prior           string    `json:"prior,omitempty"` // the same
	PriorKept       bool      `json:"priorKept,omitempty"`
	At              time.Time `json:"at"`
	Actor           string    `json:"actor"` // the raw actor, at most MaxDispatchActor bytes
	By              string    `json:"by,omitempty"`
	OpID            string    `json:"opId"`
	N               int       `json:"n"`
	Origin          string    `json:"origin"` // a tag
	RecoveredBy     string    `json:"recoveredBy,omitempty"`
	ViewDigest      string    `json:"viewDigest,omitempty"` // a plan's create
	Remote          bool      `json:"remote,omitempty"`     // applied here from another replica
	Refs            []string  `json:"refs,omitempty"`       // the entities the row needs, each "kind:id", at most MaxDispatchRefs
	OutputLike      bool      `json:"outputLike,omitempty"`
	PriorOutputLike bool      `json:"priorOutputLike,omitempty"`
}
func (c DispatchChange) Valid() error
type DispatchApplied struct {
	Applied int      `json:"applied"`
	Held    []string `json:"held,omitempty"` // at most 50, each at most 200 bytes
	Words   string   `json:"words,omitempty"`
}
const (
	MaxDispatchValue  = 64 << 10 // DS1's bound of a row's value
	MaxDispatchActor  = 60       // dispatch.MaxActor
	MaxTag            = 8
	MaxDispatchRefs   = 3       // the most any row needs is two (a relation's ends, a ticket-label add's ticket and label)
	MaxDispatchBatch  = 1000    // rows a batch holds, either way
	MaxDispatchBytes  = 4 << 20 // a batch's rows encoded
)
func ValidTag(tag string) bool
```

A row that replicates names its project in `ProjectID`, but a person's
(entity `person`, which names none), and a project's own rows name its
own id (`ProjectID` equal to `EntityID`), as the store writes them.

`Valid` checks every bound above, the op id's pattern (`dispatch.
OpIDPattern`), an origin that is a valid tag, `By` within
`dispatch.MaxByName` characters, `Refs` within its bound and each of
the shape `kind:id`, and the absence of control characters where DS1's
fields refuse them. `DispatchApply` ignores a row's `Refs` and computes
them again from the row (`rules.go`'s `Refs`): what a provider sends
never decides what a row needs here.

`docs/repository-map.md`'s `internal/dispatch` and `ext` rows say what
OS-1a adds.

### `internal/dispatch`, `internal/api`, `ext`, `ext/exttest` (OS-1b): sharing, numbers and the provider's doors

**Two values in columns of schema 9's.** The replica's overflow range a
`Join` hands is kept on its tag (`replica_tags.overflow_from` and
`overflow_to`) and copied to every project (`keepOverflow`), and a
project made or received later copies the tag's (`replicaRanges`), so a
store that joins before it has a project keeps its range
(`TestAJoinBeforeAnyProjectKeepsItsRange`); a share's words (a
refusal's, a dormant or unshared workspace's, a retire's "sharing was
interrupted") are `projects.share_words`, written with the state they
go with and forgotten when a share is asked again or a dormant project
is shared again, so a restart shows them (`TestARefusedShareOutlastsARestart`).

**Numbers.** `Store.SetBlock(ctx, project string, from, to int64, next
bool) error` writes the current block (`next` false) or the block asked
ahead (`next` true), with `block_tag` the active tag. A create in a
shared project takes the next number of its current block (only one
handed to the active tag, OS-1a); past half of it the store tells the
provider (`DispatchWatcher.BlockLow`, after the commit); with no block
left it takes the replica's overflow range's next number, so a create
never waits (DS1's allocation class). A project's own numbers before its
share and every block are below 1,000,000; overflow ranges are above it,
and provisional windows above 2,000,000,000 (Events, Numbers). A store
with no active tag behaves as on `main` (`number_from` 0, `next_number`
alone; `TestUnjoinedStoreIsMain`).

**Sharing** (the cut-over's store side; the whole order is under Events):

- Intent `project.share` (local only): refused for an archived project
  (`409 archived`) and for one whose highest number is 1,000,000 or more
  (`409 numbers_out_of_range`); otherwise it sets `share_state`
  `pending` and `share_reserved_to` to the project's next number plus
  999, in one transaction. While `pending` or `completing`, creates take
  numbers up to `share_reserved_to` as on `main`, then overflow numbers.
  A `share` of a `dormant` project reserves nothing (its numbers are
  blocks already) and makes it `pending` again.
- `Store.CompleteShare(ctx, project, workspace string, upto int64)
  error`: in one transaction, refuses with `ErrShareMoved{Newest}` when
  the log holds a row of the project past `upto`, otherwise sets
  `shared` (the workspace), `share_state` `completing` and
  `share_upto`.
- `Store.ConfirmShare(ctx, project string) error`: `completing` to
  `shared`, once the provider says the project is shared; from then on
  the project's creates take block numbers.
- `Store.RefuseShare(ctx, project, words string) error`: `share_state`
  back to `''` and the reservation dropped, the words kept for the
  project page, so the person can rekey.
- `Store.Dormant(ctx, workspace, words string) error`: every `shared` or
  `completing` project of the workspace `dormant` (still that
  workspace's, its blocks kept, nothing of it handed out), when the
  provider says the workspace's shared copy is gone; a remote batch of a
  dormant project's rows is applied as a shared one's and turns it
  `shared` (`TestDormantProjectTurnsSharedOnItsFirstRemoteRow`).
- `Store.Unshared(ctx, workspace, words string) error`: every project of
  the workspace marked "no longer shared with you" (`shared` kept for
  history, `share_state` `''`, nothing handed out or applied for it).

**Rekey.** Intent `project.rekey` (`{key}`, DS1's allocation row),
allowed only while `shared` and `share_state` are both `''` (`409
share_in_progress` while a share is pending or completing, since the
provider may already have registered the key; `409 shared` once
shared): the project's key and its tickets' keys follow, and the commit
applies, in the next transaction and in their order, the rows
`held_rows` holds for the new key's old holder (State machines, Rekeyed
history) (`TestRekeyOnlyBeforeShare`,
`TestRekeyRefusedWhileSharePending`,
`TestRekeyAfterRestartAppliesHeldRows`).

**Keys: the registry's, never a row's.** A project's create row and each
ticket's create row carry the key they were made under (`projects.go`,
`tickets.go`), and history is immutable, so a project rekeyed before its
share (CND to NEW, because the workspace already has a CND) still begins
its history with `create {key: CND}`. The key a shared project has on
every replica is therefore the workspace registry's, which the sharer's
provider registers at the share (the current key) and which never
changes while the project is shared. Each batch the provider applies
carries each project's registry key (`keys`); `ApplyRemote` writes it to
`projects.registry_key`, projects `projects.key` from it and each
ticket's `key` as `TicketKey(registry key, number)`; the rows that carry
a key (a project's and a ticket's create, a rekey's `set`) are logged as
they are and never projected for the key on a replica that received
them. So a rekeyed project shared into a replica that holds the
workspace's other CND arrives as NEW, its creates never meet the UNIQUE
key of the CND already there, and no later row is needed to fix it; an
incoming project whose registry key a local unshared project holds is
held, with its words (`TestRekeyedProjectSharedIntoAReplicaHoldingItsOldKey`,
`TestRegistryKeyProjectedNeverTheRowsKey`).

**Routes** (`internal/api`): `POST /api/dispatch/projects/{key}/share`
(`{opId, byName?}`) and `POST /api/dispatch/projects/{key}/rekey`
(`{opId, byName?, key}`), admin, each body at most 256 bytes
(`DisallowUnknownFields`), answering `{receipt, project}`; a repeat
answers the same receipt. `share` answers `409 no_sync` when no sync
provider is registered (a registration check: article 1), `409
archived`, `409 numbers_out_of_range`; `rekey` answers `409
share_in_progress`, `409 shared`, `409 key_taken` (a key another project
holds) and `400 invalid` for a key that is not one. Each is a row of
`docs/protocol.md`'s Dispatch table with its bound.

**The project's read** gains `share: {state, words?, reservedTo?,
shared?}` and, for a project held back by a key, `heldBy: {key, rows}`
(the held rows waiting for it), so the page can say why.

**`ext.Host` doors** (`ext/host.go`, gathered as the interface
`ext.SyncDoors` in `ext/sync.go`, which `Host` embeds, with every door of
OS-1b, OS-2 and OS-3; `ext.UnsupportedSync` answers each
`ErrUnsupported`, for a `Host` that is not the sync store's), each answering `ErrUnsupported` on
a `Host` given to any provider but the registered sync store
(`extHost`'s `name` against the sync slot's, as `HostToken` checks the
identity slot's; the host agent's `Host`, `internal/hostagent/
exthost.go`, answers `ErrUnsupported` to every one) and on a switchyard:

```go
DispatchJoin(ctx context.Context, tag, workspace, device string, overflowFrom, overflowTo int64) error
DispatchRetire(ctx context.Context) error // Store.Retire
DispatchChanges(ctx context.Context, after int64, n int) ([]DispatchChange, int64, error)
DispatchPlanView(ctx context.Context, viewDigest string) ([]byte, error)
DispatchApply(ctx context.Context, rows []DispatchChange, views map[string][]byte, keys map[string]string) (DispatchApplied, error)
DispatchBlock(ctx context.Context, project string, from, to int64, next bool) error
DispatchBlocks(ctx context.Context) ([]BlockState, error)
DispatchShared(ctx context.Context) ([]SharedProject, error)
DispatchCompleteShare(ctx context.Context, project, workspace string, upto int64) error // ErrShareMoved
DispatchConfirmShare(ctx context.Context, project string) error
DispatchRefuseShare(ctx context.Context, project, words string) error
DispatchUnshared(ctx context.Context, workspace, words string) error
DispatchDormant(ctx context.Context, workspace, words string) error
DispatchNames(ctx context.Context, names map[string]string) error // tag to device name
```

- `DispatchChanges` hands at most `n` (at most `MaxDispatchBatch`) rows
  whose triple replicates after `after`, within `MaxDispatchBytes`, and
  the position to ask from next (past the rows it skipped). It stops
  before the first row whose verdict is `pending` (OS-1c), so a
  provider's cursor never passes one. Before OS-1c is on `main` every
  row's verdicts are `''` and handed out as `false`; a provider built
  against a module without OS-1c is not to be deployed (The PRs).
- `DispatchApply` takes at most `MaxDispatchBatch` rows and
  `MaxDispatchBytes`, each row's `Valid`, the views each at most
  `dispatch.MaxPlanBody` (`ext.MaxPlanView`), the keys each a project
  key, by the project's id; it is `Store.ApplyRemote`. Its `Held` holds
  at most `ext.MaxHeldWords` (50) words of at most `ext.MaxHeldWord`
  (200) bytes; the words a share's refusal, `Unshared` and `Dormant`
  keep are at most `ext.MaxSyncWords` (200) bytes.
- `BlockState {Project string; From, To, Next, NextFrom, NextTo int64;
  Left int64}` (each shared project's current and next block and how
  much is left); `SharedProject {ID, Key, Name, State, Words string;
  ReservedTo, Highest int64}` (each `{id, key, name, state, reservedTo,
  highest}`); both at most 1,000 entries (`ext.MaxDispatchProjects`).
  `ErrShareMoved` is `ext.ErrShareMoved{Newest int64}`, an error type.
- `DispatchNames` takes at most 1,000 names (`ext.MaxDispatchNames`),
  each a valid tag to at most 64 bytes (`ext.MaxDeviceName`) of one line
  of words: a name is kept address-free, as Dispatch keeps words.
- An optional interface the sync store may implement,
  `ext.DispatchWatcher { BlockLow(project string) }`, called after the
  commit, from the store's own goroutine, never blocking a write (a
  call that has not returned drops the next until it does).
- `ext/exttest.FakeHost` records each door's calls and answers from a
  script; `exttest.Continuity` gains the doors' refusals on a provider's
  `Host` that is not the sync store.

`TestHostDispatchDoorsOnlyForTheSyncStore`,
`TestBlockLowCalledAfterCommit`, `TestShareNeedsASyncProvider` (in
`internal/api`).

### `internal/api`, `internal/export`, `ext`, `web/app` (OS-2): UI state, pins, a restore into another provider's directory, a replay on request

**Three documents** in the data directory, through `store.Sub("ui")`:
`ui/layout.json`, `ui/palette.json` and `ui/pins.json`, each a JSON
object with `v` (1), `at` (when its content last changed, by the
server's clock) and `by` (this data directory's random writer id, 32 hex
characters made once in `ui/writer`), at most 64 KiB each:

- `layout`: the editor's layout and side split (`utils/editorLayout.ts`,
  `components/EditorColumn.vue`), the sidebar's mode, width and folds
  (`composables/useSidebar.ts`, `useSidebarFolds.ts`, `utils/sidebar.ts`),
  the crew graph's direction, handoffs and members switches
  (`components/CrewGraph.vue`), and the run and project pages' chosen
  views (`pages/runs/[run].vue`, `pages/dispatch/[project]/index.vue`):
  enums and fractions only, each with its own schema.
- `palette`: the palette's recents (`utils/palette.ts`'s `Recent`,
  `{key: {count, last}}`), at most `RECENT_MAX` (200) entries, each key
  at most 300 bytes.
- `pins`: the pinned sessions, at most 20, each `{session, name, agent,
  at}` (the session id, its name and agent when pinned).

Routes, admin: `GET /api/ui/{name}` (`{}` when absent) and `PUT
/api/ui/{name}` (the document whole without `at` and `by`,
`DisallowUnknownFields` against its schema, `400 invalid_request` past
its bound or for a name not one of the three). The server sets `at` and
`by` only when the content changed, so a replay or a write that changes
nothing keeps them, and sets `at` to the later of its clock and one
millisecond past the held document's `at`, so an edit made after a
later-dated document arrived from a machine whose clock runs ahead is
still the newest (`TestUIEditAfterALaterDatedDocumentIsNewest`). The
reply is the document as kept. `ui/writer` is left out of the export
(`leftOut`, a new reason `instance`: "this machine's own: made again
where it is absent") and made again when absent, so two machines
restored from one export write under two ids. `internal/export` carries
the three documents as it carries the data directory's others. Each
route is a row of `docs/protocol.md` with its bound.

**The adapter's allowlist** (`syncDataDoc`) gains
`ui/(layout|palette|pins)\.json` (`Store: "data"`), and its replay lists
`ui/` beside `crews/` and `runs/` (`TestUIStateOnTheAllowlist`). What
leaves of them is narrower than what the server keeps, and the adapter
builds it at delivery, as it masks `catalog.json`:

- `ui/layout.json` whole: enums and fractions only.
- `ui/palette.json` with only the recents whose key is of the kinds
  `go:` (a page), `ticket:`, `crew:`, `run:` and `action:` (a built-in
  action's id). Every other kind stays on the machine: `open:` and
  `touched:` (file paths), `shared:` (joined links), `session:`,
  `artifact:`, `member:`, `answer-all:`, and any kind a later row adds
  until it is named here (`TestPaletteDeliveredWithoutPaths`: a recent of
  each kind written, the delivered document holding the five kinds
  alone).
- `ui/pins.json` with each pin's session id and agent, and its name
  under the copied-output check: the name's verdict is made when the pin
  is written and kept beside it in the server's document as
  `nameVerdict`, which the server sets and a `PUT` may not carry (from
  OS-1c on; until then a pin's name is delivered empty)
  and an output-like or `pending` name is delivered empty
  (`TestPinNameUnderTheOutputCheck`).

The server's own documents, its export and its own pages keep every
recent and every name, so the free build loses nothing.

**An `ext.Host` door, `SetUIState(ctx, name string, data []byte)
error`**, for the sync store alone: writes `layout` or `palette` (never
`pins`: another machine's pins are remote records, OS-4) through the
route's validation, keeping the given document's `at` and `by`, and
refused (`ErrRefused`, wrapped with "an older document") for an (`at`,
`by`) older than or equal to the one
held, so an old document replayed never overwrites a newer one
(`TestSetUIStateRefusesAnOlderDocument`).

**An `ext.Host` door, `SyncReplay(ctx) error`**, for the sync store
alone: asks the adapter for a new epoch beginning with a replay of the
whole state (C6's `replay`, every allowlisted document, every run
pointer, every decision once OS-3 is in, Dispatch's `log` doc, then
`replayed`), exactly as the adapter's own overflow asks one (its `due`),
with no restart and no source document changing. It coalesces and is
bounded: a request while one is wanted or running is that one (at most
one waiting and one running), and it returns once the request is
recorded, never waiting for the replay. A provider calls it whenever
what it holds elsewhere may be behind this machine's state
(`TestReplayDoorReplaysEveryDocWithoutAChange`,
`TestReplayRequestsCoalesce`).

**A restore door to another provider's directory.** `ext.RestoreRequest`
gains `ProviderDir func(name string) (string, error)`: the directory of
another registered provider (`<dataDir>/ext/<name>`), made 0700 and
empty on a fresh machine, where a restoring provider may write that
provider's files (its sign-in, for instance); a name that is not a
registered provider's, or the restoring provider's own, is refused.
C6's checks of the archive still refuse anything outside `ext/` and the
archive's own files (`TestRestoreWritesAnotherProvidersDir`).

**The web app.** `composables/useUiState.ts` reads the three documents at
the first paint and writes on change (debounced 1 s), keeping local
storage as the cache it reads first, so the page draws at once; a
server without the route (an older one, through a switchyard: `404`)
leaves local storage as it is and the app works as on `main`. The
palette (`usePaletteRows.ts`, `CommandPalette.vue`), the editor's layout,
the sidebar's mode, width and folds, the crew graph and the two pages'
view switches read and write through it. The palette's recents keep
their 200 entries.

**Pins.** A session row's menu (`SidebarSessionRow.vue`) gains **Pin to
top** and **Unpin**; pinned sessions are the sidebar's first group
(`utils/sidebar.ts` gains the section `pinned`). A pin of a session no
longer listed is shown greyed and removed after 7 days
(`utils/pins.ts`).

**As built** (the row's own readings, where the paragraphs above leave a
choice). The layout's schema is `editor {layout, split, sideSplit}`,
`sidebar {mode, width, folds}` (the width in rem, the folds by section,
`pinned` among them), `crewGraph {direction, handoffs, members}`,
`runView` and `projectView`, each optional and each the setting a
local-storage key held before, so the cache and the document hold one
thing. A change this browser made and the server has not kept yet (a reload
inside the second a change waits, a server not answering) is marked
unsent in local storage, and the next load writes it rather than taking
the server's older copy (`ui-state.spec.ts`, a fold just before a reload).
A pin is removed a week after it was pinned once its session is no
longer listed: a pin keeps only when it was pinned, so "after 7 days" is
counted from there. A replay request while one is wanted, or while a
replay is listing, is that one; one made while a replay's documents are
being handed over waits, so at most one waits and one runs and no request
is lost (`TestReplayRequestsCoalesce`). The sync store's `SetUIState`
refuses `pins` and a document whose `at` or `by` is missing or not of its
shape, as well as an older one. Every Playwright test starts from a fresh
server's three documents (`uiStateFresh` in `web/e2e/fixtures.ts`), since
the server keeps them for every browser and one test's fold would be the
next test's start.

### `internal/api`, `internal/dispatch`, `ext`, `docs` (OS-1c): the output index and the copied-output check for Dispatch's rows

The check holds the owner's reading of terminal output (What counts as
terminal output, below).

**`outputGuard.Check(ctx, text string) Verdict`** (`internal/api/
output_guard.go`): one function over one string, `clean`, `output` or
`pending`, with no knowledge of Dispatch or decisions. Each caller stores
the verdict beside its own text, so a later route, or a `Host` door a
later item adds (TS1's publishing), calls it unchanged
(`TestOutputGuardOverAnyText`).

*What it reads*, under the default reading: every free-text value a
Dispatch change row or a decision record carries, whoever wrote it (an
agent through MCP, a person through the workbench token or a decision
link, a held answer recorded through OS-3): titles, descriptions,
acceptance criteria, comments, names, notes, a plan body's text (its
view inherits its create row's verdict); and, for OS-3, a decision's
`question`, `context`, each option's `label` and `tradeoffs`,
`recommendation`, `uncertainty`, `authorizes`, `expertise`, a response's
`text` and `rationale`, receipt and application words, `why`.

*Against what*: everything the server holds captured at that moment,
not only the writer's attempt: every session's ring buffer as rendered
text (escape sequences removed), every session's screen as drawn
(`internal/session`'s `Screen.Rows`: a program that moves the cursor and
redraws only the cells that changed assembles on the screen a passage
whose bytes are never contiguous in the ring, and the screen is what a
person copies from), every check log the ledger keeps, and HS1's
history words, read through an index kept as the output arrives. A
screen is a source of its own, `screen:<session>`: its rows, each row
and the rows joined in order, are fingerprinted whenever the screen
settles (`Screen.Settled`) and at least once a second while output
arrives, each snapshot kept while the session's ring still holds the
output written before it and pruned with that output; the rows on the
screen at the write are read whole and compared then too. A snapshot due
and not yet taken makes the index behind, so the verdict is `pending`
(`TestPassageAssembledByCursorMovesCaught`: a 64-byte passage drawn one
cell at a time by cursor moves in shuffled order, then pasted into a
comment: output-like while it is on the screen and after the screen has
redrawn over it).

*The index* (`outputIndex`, its own SQLite file `output-index.sqlite` in
the data directory, never exported (`leftOut`, `derived`) and never on
the sync allowlist) holds no output, only fingerprints, and misses no
quote the rule names: the text of every source, whitespace folded, is
fingerprinted by winnowing (the hash of every 32-byte window, and of
each run of 33 consecutive windows the smallest, the rightmost on a
tie), which guarantees that any run of at least 64 bytes common to a
value and a source shares at least one fingerprint (32 + 33 - 1 = 64);
the hash of every line of at least 16 bytes is kept whole. Each
fingerprint names its source and offset, and a hit is confirmed against
the source's text before it counts. Memory holds only a cache of it; a
lookup that misses the cache reads the file, so a quote of a source
outside the cache is caught as one inside it. A source pruned (a run's
logs, a ring overwritten, history pruned) takes its fingerprints with
it, in the transaction that records the prune, with the prune's time
(`TestIndexPrunedWithItsSource`).

*It fails closed.* The index is bounded on disk (1 GiB, past the sum of
the sources' own caps in an ordinary data directory). While it is past
its bound, missing, being rebuilt (its file absent or unreadable at
start: rebuilt from the ledger, the rings and history in the
background) or behind the output that arrived, a verdict it cannot make
is `pending`, never `clean`. A row or a decision part whose verdict is
`pending` is judged when the index is whole again, against what it then
holds, and is judged `output` if a source it could have quoted was
pruned meanwhile (the prune's time after the write's), or if the server
process that wrote it has ended since: a session's ring and screen live
in that process's memory, and a crash takes them with no prune recorded.
So at every start, before the index answers anything, every `pending`
verdict that Dispatch's `changes` and the ledger's `decision_output`
hold becomes `output`, one transaction each
(`TestPendingVerdictOfAnEndedProcessWithheld`: a quote of a live ring
written while the index is behind, the process killed before it
indexes, the restart: the row withheld, never `clean`). Past its bound
for good, the sync status says "the output check is over its bound:
text written here stays on this machine until it is reindexed"
(`TestWinnowingCatchesEvery64ByteQuote`, a property test over random
sources and quotes of 64 to 200 bytes at random offsets with whitespace
varied; `TestQuoteFromOutsideTheMemoryCacheCaught`;
`TestSaturatedIndexFailsClosed`;
`TestPendingVerdictNotHandedOutDuringRebuild`).

*The rule*, under the default reading: a run of at least 64 bytes, or
two whole lines of at least 16 bytes each, found in a source verbatim
(whitespace folded) makes the value output-like. Below the threshold a
fragment is a phrase, not output: a test's name or "exit status 1"
appears in writing and in output alike.

*When*: at the write, in the writing transaction. Every Dispatch
operation's commit runs `outputGuard` over each free-text value it
writes and, apart, over a scalar's prior, whoever its actor, and stores
the two verdicts in `changes.output` and `changes.prior_output` in the
same transaction (`TestOutputVerdictWrittenWithTheRow`). So a row shared
a year later, when the attempt and its logs are gone, is judged by what
the server held when it was written (`TestVerdictKeptAfterTheSourceIsGone`:
a comment quoting a check's log, written while the log exists, the run
pruned, the project shared: withheld). A row written before the check
existed (version 7 and earlier) has no verdict: at hand-out it is
checked against what the server holds then, and when its writer was an
agent it is marked output-like whatever that finds, since what it could
have quoted may be gone (`TestHistoricalAgentRowWithheld`; the provider's
words: "CND-12's comment was written by an agent before this server
checked for copied output: kept here; an edit here sends it").

*What follows*: `Host.DispatchChanges` sets `OutputLike` and
`PriorOutputLike` from the stored verdicts, and stops before the first
`pending` row. A provider withholds an output-like row as it withholds
any row it does not send, and sends a clean value without an
output-like prior (`PriorKept`)
(`TestAgentRowQuotingOutputIsMarked`,
`TestPersonsPasteOfCapturedOutputWithheld`,
`TestQuoteAtTheThresholdCaught`: 64 bytes and two lines caught, 63
not; `TestOutputLikePriorLeftOutOfACleanCorrection`: output-like text
replaced by exactly one clean edit, the source pruned, then shared: the
edit's value `clean`, its prior `output`, the row handed out with
`PriorOutputLike` alone).

**As built (OS-1c).** The index is `internal/api/output_index.go` and
the check `output_guard.go`; the rule's reading is the constant the open
question names. A source's text is made plain (escape sequences removed,
a carriage return read as a line's end) before it is folded. The sources:
a local session's output, fed as it arrives (`session.Options.OnOutput`,
before any viewer has it) as `ring:<session>`, holding what the ring holds
and pruned with what the ring drops and with the session when it leaves
the list; its screen, `screen:<session>`, a snapshot of its rows queued
whenever output arrives (one queued at a time, read when the index takes
it, so the index is behind while one is due) and the screens read whole
at every check besides; every check's and prerequisite's log the ledger
keeps, told after its commit (`ledger.OnLog`), read again through the
ledger for a confirmation, and pruned with its run; every entry a
session's history record keeps (its activity's and its chat's words),
told as it is kept (`journal.Options.Kept`), `history:<session>/<seq>`,
read again through the record and pruned with the record. The index is
opened only with a sync store registered. A feed never waits on the
file; a live source has at most one feed queued, held to what it keeps
and a quarter, and the bytes queued across sources to 64 MiB, what is let
go recorded as a prune of the source. A check made outside a write waits
up to 2 s for the feeds queued before it, then answers `pending`; one
made inside a write never waits and never reads a kept source. A hit
is confirmed against its source's text; one that cannot be confirmed
counts. An index missing or unreadable at the start is made again from
the ledger's logs and the history's records before it answers. A hosted
session's output does not pass the server whole (the host link carries
its signalling, and its terminal only to a viewer the server relays), so
the check fails closed for it: free text its agent writes is judged
`output` whatever it says (`judgeRow`) and stays on the machine; today
such an agent is no member of the server's runs and has no ticket here
to write to. OS-4's sync status names such a row as kept because the
server does not see its agent's output. A browser below the width where
the desktop sidebar shows (lg) writes no layout (OS-2): its sidebar is a
drawer, and a phone never drives the desk's layout. A row's value that is JSON is judged
by each string it holds as well as whole. Every write's rows carry the
two verdicts (`changes.output`, `changes.prior_output`) once the server
gave the store the check (`dispatch.Store.SetOutputCheck`, at start with
a sync store registered, after `WithholdPending` made every verdict a
process before left `pending` `output`); a pending verdict is judged
again when its write is told and every 2 s, once the index is whole
(`settlePending`), `output` when a source was pruned after the row was
written or when another process wrote it. `DispatchChanges` judges a row from before the check
at hand-out (an agent's `output` whatever the check finds, every actor but
the operator's and the store's own taken as an agent's) and ends its page
before the first row still `pending`, its cursor the position before it.
A pin's name is judged when the pin is kept (`nameVerdict`, the server's
alone) and leaves only when clean. Not built here: the sync status's
words for an index past its bound and for a hosted agent's row (OS-4's
status); the state is in `outputIndex.state`.

### `internal/api`, `internal/crew`, `internal/crew/ledger`, `internal/agents`, `ext`, `ext/exttest` (OS-3): decisions handed out, answers handed in

Cut once DR1's PR-4 (#637) and the decide grant (#776) are on `main`
(both are) and after OS-1c, and once the owner has answered The open
question.

**The ledger tells the adapter which decisions changed.** On `main`
`ledger.OnCommit` takes a run's pointer per transition, not an event a
field could join, so the ledger gains `OnDecisions(fn func(ns
[]int64))`, called under `l.mu` right after each committed transaction
that changed a decision (DR1's writers know them: `OpenDecision`,
`PutDecision`, `MoveDecisions`, a response, an application, a prune's
hand-back), after `OnCommit`'s calls, for an observer that only queues
(`TestOnDecisionsNamesTheDecisionsChanged`). The adapter marks
`decision/dr-n` pending (`Store: "decision"`, `Path` `dr-n`, `Rev` the
decision's `rev`) and, at delivery, reads `Ledger.Decision` and builds
the document below (`syncDecision`). Where the adapter's computed
sources (HS2's or TS1's PR-5) are on `main` at OS-3's cut, the decision
document is a computed source of store `decision`; otherwise OS-3 adds
the store `decision` beside `ledger` and `dispatch` in `deliver`. A
pruned run's decisions are `Deleted`. A replay lists every decision the
ledger holds (`Ledger.DecisionRevisions`, paged).

**The decision document** (`syncDecision`, deterministic: the same
record at the same `rev` gives the same bytes and the same
`requestSHA256`), at most `ext.MaxDecisionDoc` (80 KiB, the sum of its
parts' bounds below):

| Holds | Never holds |
|---|---|
| `id` (`dr-n`), `run` (its `current_run`), `openedIn` (its `run_id`, which never changes), `origin`, `type`, `gate`, `task`, `attempt`, `member` (the requesting member's name), `ticket`, `plan`, `planDigest`; `request.question`, `request.context`, `request.options` (`label`, `tradeoffs`), `request.recommendation`, `request.uncertainty`, `request.authorizes`, `request.expertise`, `request.subject` (its kind and its commit, candidate, relative path and SHA-256, or plan digest), `request.supersedes`, `request.evidence` (the ref strings alone), and `requestSHA256`; `policy`; `state`, `rev`, `why`, `supersededBy`, `createdAt`, `answeredAt`, `closedAt`; the accepted response and the last 8 others, each its `k`, `actor`, `status`, `responseId`, `at`, its authority's `via` alone, its `choice` and its receipt's code and words (words cut at 500 bytes), with `responsesOmitted`; the **accepted** response also with what was answered, its receipt's `text` (at most 4,096 bytes) and `rationale` (at most 2 KiB), however it was made; `applied`'s `at`, `how`, `run`, `task`, `attempt`, `response` and words; DR1's four states as the record reports them (`states`) | `session_id`; `request_id` and every fingerprint; each response's authority but its `via` (its `boot`, statement, fence, subject's membership); the `text` and `rationale` of a response that was not accepted; the evidence baseline's `seen`; `decision_history` rows; `decision_holds`, `decision_refunds`; any evidence a ref names (a check's log, a session's words) |

In parts, each with its own bound so the whole always fits: the request
at most 40 KiB (past it `context` and then `options[].tradeoffs` are cut
at a character's end, `cut: "size"`), the responses at most 16 KiB (the
accepted one always, at most 7 KiB with its text, rationale and words,
then the newest others while they fit, each under 1 KiB), `applied` and
`states` at most 4 KiB together, the ids and enums at most 4 KiB, and
`policy`, the record's policy whole, a part of its own at the ledger's
`MaxDecisionPolicy` (16 KiB once the decision-policy plan,
`docs/plans/decision-policy.md`, raises it from main's 1 KiB): a
provider that shows the decision elsewhere needs to know who may answer
it. So the total is 40 + 16 + 4 + 4 + 16 = 80 KiB, and a test holds
`ext.MaxDecisionDoc` to that sum computed from the constants, so a
later raise of a part's bound fails it until the total moves too. A
decision with DR1's most responses (32, each with an 8 KiB receipt, the
accepted one with the longest text and rationale) and its policy at
`MaxDecisionPolicy` fits (`TestDecisionDocWithTheMostResponsesFits`, `TestDecisionDocCutsPastItsBound`,
`TestDecisionDocIsTheTable`: the fields against the table, no
`session_id`, authority, history or fingerprint).

**Whom it is opened to.** The document carries `openedTo`, written by
the server from its own record at every build: `decide` when the
decision's request was made for the decide grant (its `for` names the
grant, `{label: "grant:decide"}` with no person, as `crew.GivenToGrant`
reads it) and the decision is not a comparison's selection (origin
`compare`, the operator's alone, never opened to the grant whatever its
request says), and `operator` otherwise. These are exactly the records
`crew.EntriesOf` gives the grant's entry. Whatever holds the document
for an absent operator reads there who may answer it, without reading
the policy's entries itself. It is outside the request part, so the
request part and `requestSHA256` are as they would be without it
(`TestDecisionDocOpenedToFollowsTheRecord`: a record given to the grant
`decide`, one that is not `operator`, a comparison's selection whose
request names the grant `operator`, and the request hash the same
either way).

A decision's `origin` is the ledger's own word, `agent`, `plan` or
`compare` (`ext.DecisionOrigins`; `internal/crew/ledger`'s `OriginAgent`,
`OriginPlan` and `OriginCompare` are those constants), so every origin
the ledger writes is one the document carries
(`TestEveryOriginTheLedgerWritesIsOneTheDocumentCarries`).

The Go shape of the document is `ext.DecisionDoc` (`ext/sync.go`), with
`ext.DecisionRequestPart`, the part hashed, and `ext.DecisionDocStates`,
the four states as `{open, recorded, validated, applied}`, each but the
first `{at, words}` once reached; the parts' bounds are
`ext.MaxDecisionDocRequest`, `MaxDecisionDocResponses`,
`MaxDecisionDocApplied`, `MaxDecisionDocIDs` and `MaxDecisionDocPolicy`,
and `ext.MaxDecisionDoc` their sum.

The document's **request part** is what a person answers and what a yes
acts on: the target's identifiers, `{decision (dr-n), openedIn, origin,
type, gate, task, attempt, ticket, plan, planDigest}`, with the
`request` object as cut. Two decisions that ask the same question with
the same options differ in it by their ids, their subject and what they
authorize. A request kept back (`requestKept: "output"`, below) is
absent: the document then carries no `request` at all and the mark, and
its request part leaves `request` out, so the part is the target's
identifiers alone; a document carries exactly one of `request` and
`requestKept`, never both and never neither (`ext.DecisionDoc.
RequestPart` refuses either). `requestSHA256` is the SHA-256, in
lower-case hex, of the request part in canonical JSON (every object's
keys sorted, no insignificant space, strings as `encoding/json` writes
them with HTML escaping off, numbers as integers;
`ext.DecisionRequestPart.SHA256`), with the request when it is there and
without it when it is kept (`TestAKeptRequestIsLeftOutOfThePart`: a kept
request's part without `request`, the mark in the document, the hash
over exactly that; and a document with both, or neither, refused); the server
computes it again from its own record before it records a held answer,
so a statement made for one decision's request part is never recorded
against another's (`TestRequestPartNamesTheTarget`,
`TestTwoDecisionsWithTheSameQuestionToldApart`: two decisions of one run
asking "Proceed?" with the same options and other subjects and
authorizations: a statement for one refused for the other). The fields
that move with a continuation (`run`, the states, the responses) are
outside it, so a held answer survives the run's continuation. An answer
made at the machine reaches the document through its receipt, so
whatever holds the document holds what was answered, not only that
something was (`TestLocalAnswerReachesTheDocument`: a choice and a
free-text answer made with the workbench token, in the document with
text and rationale).

**Where a held answer's statement is kept.** DR1's authority is at most
`ledger.MaxAuthority` (1 KiB, checked by `authorityOf` as given and as
re-encoded), and the statement alone may be 2 KiB, so the statement is
not in the authority: the ledger's step keeps it in a table of its own,
`CREATE TABLE decision_statements (decision INTEGER NOT NULL REFERENCES
decisions(n) ON DELETE CASCADE, k INTEGER NOT NULL, statement TEXT NOT
NULL, PRIMARY KEY (decision, k))`, written
in DR1's transaction with the response row it belongs to (accepted, or a
kept refusal), and the authority holds only its SHA-256
(`statementSha256`). With the opaque ids of the shape above (no byte
JSON escapes), the `via: sync` authority at every field's bound (`grant`,
`via`, `subject` at `MaxSubject`, `membership`, `signIn` or `appDevice`,
`at`, `statementSha256`, `held`, `fence`, `boot`, with their keys) is
under 700 bytes, so `MaxAuthority` stays 1 KiB
(`TestSyncAuthorityAtEveryBoundRecorded`: a held answer with a 2 KiB
statement and every other field at its bound recorded; one whose
statement is past 2 KiB refused by `HeldAnswer.Valid`, never by
`authorityOf`).

**The copied-output verdicts, kept in the ledger** (schema version 16,
the next at its merge, row OS-3a, the same step as
`decision_statements`;
additions only): `CREATE TABLE decision_output
(decision INTEGER NOT NULL REFERENCES decisions(n) ON DELETE CASCADE,
part TEXT NOT NULL, verdict TEXT NOT NULL, cut_at INTEGER NOT NULL
DEFAULT -1, PRIMARY KEY (decision, part))`, a
row for each free-text part of a decision (the request's fields, each
response's `text` and `rationale`, each receipt's and the application's
words, `why`), written by `outputGuard` in the transaction that writes
the part, whoever wrote it, so a document built later, after the
attempt's ring and its check logs are gone, still cuts what quoted them.
A decision of an earlier schema has no rows: its parts are checked when
its document is first built, and an agent's request text is kept back
(`requestKept: "output"`) whatever that finds. In the document an
output-like `context`, `tradeoffs`, `text`, `rationale` or words are cut
at the run (`cut: "output"`, "[output kept on studio]"), and an
output-like `question`, `label`, `recommendation`, `uncertainty` or
`authorizes` keeps the whole request text back: the document carries
its ids and states with `requestKept: "output"`, and nothing can answer
it away from the machine. A `pending` part is kept back as if
output-like until it is judged
(`TestOutputInTheQuestionOrAnOptionKeepsTheRequestBack`,
`TestAgentContextQuotingOutputIsCut`,
`TestDecisionVerdictKeptAfterThePrune`,
`TestEarlierSchemaDecisionOfAnAgentKeptBack`).

**`SyncResend(ctx, store, path string) error`**, an `ext.Host` door for
the sync store alone: marks one document pending again as if its source
had committed, so a change of what the provider makes of a document with
no change of the document (a project shared, a run link added or
removed) is delivered again (`TestSyncResendMarksOneDoc`).

**The answer's authority, typed** (`ext/answers.go`, new):

```go
// AnswerCheck is what a held answer says of who answered, when and what,
// for the identity provider to judge.
type AnswerCheck struct {
	Subject       string    `json:"subject"`             // the person, MaxSubject
	Membership    string    `json:"membership"`          // the membership the answer was made under: opaque, at most 64 bytes of [A-Za-z0-9._~:-]
	SignIn        string    `json:"signIn,omitempty"`    // the sign-in it was made through, opaque, the same shape
	AppDevice     string    `json:"appDevice,omitempty"` // or the device, the same shape; exactly one of the two
	At            time.Time `json:"at"`
	Statement     string    `json:"statement"`           // the identity provider's signed statement of the answer, at most 2 KiB, opaque here
	Decision      string    `json:"decision"`            // dr-n
	ResponseID    string    `json:"responseId"`
	ExpectedRev   int       `json:"expectedRev"`
	ContentSHA256 string    `json:"contentSha256"` // set by the server, never by the provider
	RequestSHA256 string    `json:"requestSha256"` // the same
}
type Admission int
const (
	AdmitNo Admission = iota
	AdmitYes
	AdmitUnknown
)
// SubjectChecker is an optional interface beside IdentityProvider.
type SubjectChecker interface {
	AdmitsAnswer(ctx context.Context, a AnswerCheck, g Grant) (Admission, error)
}
type HeldAnswer struct {
	Decision    string      `json:"decision"`
	ExpectedRev int         `json:"expectedRev"`
	ResponseID  string      `json:"responseId"`
	Choice      *int        `json:"choice,omitempty"`
	Text        string      `json:"text,omitempty"`      // at most MaxAnswerText
	Rationale   string      `json:"rationale,omitempty"` // at most 2 KiB
	Check       AnswerCheck `json:"check"`
	HeldAt      time.Time   `json:"heldAt"`
	Held        string      `json:"held"`  // the provider's id of the held answer, the same shape
	Fence       string      `json:"fence"` // the provider's token for this hand-out, the same shape
}
type AnswerOutcome struct {
	Status  string `json:"status"` // recorded | refused | not_held | moved | unknown_authority
	// Code, for refused: DR1's codes, and answered_at_server (decision-policy.md)
	Repeat  bool   `json:"repeat,omitempty"`
	Code    string `json:"code,omitempty"`
	Words   string `json:"words,omitempty"`
	Receipt []byte `json:"receipt,omitempty"` // DR1's receipt as JSON; the whole at most 8 KiB
}
const MaxAnswerRationale = 2 << 10
func (h HeldAnswer) Valid() error
func (o AnswerOutcome) Valid() error
```

A held answer's `choice` is present only where the decision's request
has options, and is then below their count; `HeldAnswer.Valid` checks
its shape and the door checks it against the record.

The statuses are the constants `ext.AnswerRecorded`, `AnswerRefused`,
`AnswerNotHeld`, `AnswerMoved` and `AnswerUnknownAuthority`; the
statement is at most `ext.MaxStatement` (2 KiB), each opaque id
(`membership`, `signIn`, `appDevice`, `held`, `fence`) at most
`ext.MaxHeldID` (64) bytes, and an outcome with its receipt at most
`ext.MaxAnswerOutcome` (8 KiB). The types are in `ext/sync.go` beside the
Dispatch ones (the plan's `ext/dispatch.go` and `ext/answers.go` are one
file).

`AdmitsAnswer` answers yes only when the identity provider verifies the
statement against its keys and its claims against `a` (this server, the
decision, the response id, the rev, the content and the request shown),
and the subject holds the grant on this server now under the same
membership (a membership ended and begun again is another), with the
sign-in or device not revoked; no when any of that is false by what it
knows for certain; unknown when it cannot know now (its view not yet
loaded or past its lease, a sign-in's status not to be had). It is
called through the guard (`ext.IdentityWait`); an error or a late answer
is unknown. No identity provider, or one without the interface, is no.

**`RespondDecision(ctx, r HeldAnswer) (AnswerOutcome, error)`**, an
`ext.Host` door for the sync store alone. The server computes the
content's SHA-256 (of the canonical `{choice, text, rationale}`) and
the request's SHA-256 (from its own record's document) itself, sets them
in `r.Check`, and judges it by the record's policy:

- **A held answer and a router's policy do not combine in the first
  version** (one rule, mirrored from `docs/plans/decision-policy.md`,
  Responding, Held answers; Owner decisions, 26). On a server whose
  registered decision router confirms policies (it implements
  `ext.PolicyConfirmer`: the engine's `SetRouterPolicies(true)`, the
  decision-policy plan's I17), this door refuses every held answer to a
  record under a router's policy:
  nothing is written to the decision, the router is not asked, and the
  refusal is final (the provider does not hand it in again), `403
  answered_at_server` ("dr-7 is answered at its server, by
  the operator or the people its router names, at the server's own
  routes: an answer given elsewhere is not taken here"). The engine
  refuses the same way a `via: sync` response that reaches it for a
  record under a router's policy while such a router is registered (the
  door is not the only guard).
  As built (#1065): A held answer to a record under the server's own
  policy (`local/1`, or opened to the decide grant) on such a server is
  judged as the respond route judges an answer to it: a repeat answered
  from its row and a decision no longer open said by its state first;
  then the router asked as the respond route asks it (`confirmFor`), and
  a record in its opening window, or one the router will route now
  (`Routes`, which tells it again), is not taken now
  (`unknown_authority`, nothing written: the provider releases the
  answer and hands it in again later); then the identity provider's
  `AdmitsAnswer` (admin, or decide for a record given to the grant) and
  DR1's path with the router's confirmation, where a policy written
  between the ask and the commit is `stale_policy`, unkept, and so not
  taken now for the provider either, and DR1's rules after (a rev raised
  since is `stale_decision`, final).
- **Elsewhere** (no router registered, or one that confirms no
  policies), a held answer is judged by the record's own policy and its
  entries, the store's own check first, DR1's rules after: the door asks
  `AdmitsAnswer(r.Check, admin)` (or `decide`, for a decision opened to
  the decide grant); no is `refused` with `not_approver`; unknown is
  `unknown_authority` and nothing is written (the provider releases the
  answer and asks again later). `PolicyEntry.admits` takes `via: sync`
  as it takes `via: identity` for the operator entry with the admin
  grant and the grant entry with the decide grant, each with the grant
  this door checked; `Admittee.Sent` takes a `sync` response as its
  subject's own. A record left under a router's policy with no such
  router registered is not in force, so `local/1`, and judged so.
- A held answer recorded before such a router was registered (a
  restart with a new router) stands as recorded: it is applied by its
  `AdmitsAnswer` recheck alone, as an identity's answer under the
  server's own policy is; the refusal is made at the response only.
- **A comparison's selection is never answered by a held answer.** A
  selection (origin `compare`) is the operator's alone, answered only by
  a principal authenticated at this server (the workbench token, or an
  identity provider's principal with the admin grant): a held answer to
  one is refused `403 not_approver`, kept ("dr-12 is a comparison's
  selection: it is answered only at this server"), whatever grant its
  subject holds (Owner decisions, 25).
- Held by `TestHeldAnswerRefusedWhereTheRouterConfirmsPolicies` (the
  door: on a server whose router confirms policies, an admin's and a
  decide principal's held answers, to a record under the router's
  policy, each `403 answered_at_server`, final,
  with its words, no row of the decision written, the fake router asked
  nothing; the store's `AdmitsAnswer` answering unknown: still
  `answered_at_server`, the store not asked),
  `TestEngineRefusesAHeldAnswerUnderAPolicy` (the engine: once with no
  confirmation and once with an in-force confirmation naming its
  subject eligible: each `403 answered_at_server`),
  `TestHeldAnswerAdmittedByTheEntries` (table-driven, on a server with
  no router: an admin's `via: sync` answer accepted under `local/1`; a
  decide one accepted on a record given to the grant and refused on one
  that is not; `Sent` taking it as its subject's own) and
  `TestHeldAnswerToASelectionIsRefused`: this plan's and the
  decision-policy plan's, the same names in both, built in whichever of
  its PR-7 and OS-3 lands second, against the other's code.

Then, admitted: `Engine.RespondDecision` with the actor `user:<subject>` and the
authority `{grant, via: "sync", subject, membership, signIn | appDevice,
at, statementSha256, held, fence}` and the statement in
`decision_statements`: DR1's path whole (its idempotence on
`(decision, actor, responseId)`, its rechecks, `not_held` for a run not
held, `stale_decision`, `decision_answered`, `decision_closed`,
`stale_policy`, `decision_limit`, its bounds and the replacement's
place, counted as an identity's), its answer mapped to the outcome by
its status, never by `Repeat`. The held answer's id and fence are
written in DR1's own transaction, with the accepted response or with a
kept refusal's receipt, so a ledger holds an answer's fence exactly when
it recorded that answer (`TestFenceWrittenInDR1sTransaction`,
`TestRespondDecisionNeedsAdmits`, `TestUnknownAdmissionWritesNothing`,
`TestHeldAnswerRecordedOnceAndRepeat`, `TestRepeatOfARefusalIsRefused`,
`TestHeldAnswerNotHeldRunStaysHeld`, `TestForgedHeldAnswerRefused`: a
statement for another server, decision, response id, content or request
shown, or one the identity provider cannot verify;
`TestSubstitutedAnswerRefused`: a statement whose request hash is not
the hash of this server's own record's request part).

**`SyncDelivery(ctx, decision, responseID string) (AnswerOutcome,
string, bool, error)`**, an `ext.Host` door for the sync store alone:
what this ledger holds for a `via: sync` response, its outcome as
`RespondDecision` would answer a repeat, its fence, and whether there is
one. It reads and never writes, so asking it records nothing
(`TestSyncDeliveryReadsAndNeverWrites`): a provider that is not sure
whether an answer it handed out was recorded asks the ledger, which
alone settles it.

**`LastDecision(ctx) (int64, error)`**, an `ext.Host` door for the sync
store alone: the highest decision number this ledger holds (the `n` of
`dr-n`, 0 for none), read and never written, so a provider can tell a
decision opened before a moment it records from one opened after
(`TestLastDecisionReadsTheLedger`).

**At application**, a `via: sync` response is rechecked by
`AdmitsAnswer` again from the authority the ledger keeps and the
statement kept beside it (`decision_statements`: a signed statement,
not a token, so it is kept), in
`ConfirmAuthority`'s place: yes applies it, in a transaction that checks
`k` is still the decision's accepted response, and not the `boot` (a
restart asks again rather than superseding, since nothing that was kept
is gone: `TestSyncAnswerRecheckedAfterRestart`); no supersedes, as DR1
does for an identity's answer (`TestViaSyncSupersededWhenAdmitsSaysNo`,
`TestRevokedSignInAfterDeliveryNotApplied`); unknown leaves the
decision `answered` and asks again (1 s doubling to 60 s, while the run
is held), never applying and never superseding on what nobody said,
bounded only as a hang: an unknown that lasts is shown on the decision
page as "waiting to recheck user:nate's answer: the identity provider
cannot say now", and the operator can still cancel the decision or stop
the run, as on `main` (`TestViaSyncUnknownWaitsAndNeverSupersedes`).
Applied once: DR1's application transaction marks it applied, and a
second recheck finds it so (`TestViaSyncAppliedOnlyAfterAdmits`,
`TestViaSyncAppliedOnce`).

**The agent skill** (`internal/agents/skill.go`'s Decisions section)
gains one line: a decision's context summarizes what the agent saw and
never pastes a check's or a session's output; the evidence refs carry
that.

`ext.Doc.Store`'s comment gains `decision`; `exttest.FakeHost` records
the new doors and `exttest.Continuity` checks the decision document's
shape and the doors' refusals on a provider that is not the sync store.

**As built (OS-3).** Who may answer a decision given to the decide grant
from another machine is whoever holds `decide`: the door asks
`AdmitsAnswer` with that grant alone, and an admin without it answers at
the server, as the owner confirmed on 2026-11-20. The engine refuses
every held answer while a router that confirms policies is registered,
whatever the record's policy, before the policy is judged. A provider's
no is `not_approver` with nothing written; a request kept on the machine
is refused `not_approver` before the provider is asked; a held answer's
text and rationale must be as the ledger keeps them (no control
character, no space at either end), or it is `invalid`. A part that
quotes captured output is cut whole (`cut_at` 0), so a person elsewhere
sees the cut's words in place of the whole part. The ledger judges a
decision's parts inside its transaction in a form that never waits for
the index and never reads a kept source (`JudgeNow`); what it cannot
judge then is `pending`, and the settle loop, running whenever a sync
store is registered, judges it outside the ledger's lock. While any part
of a request is pending the whole request is kept back, so a request has
one form at a rev. The door keeps the request part's hash it admitted in
the answer's authority (`requestSha256`), and the recheck shows the
provider that hash and the answer's time as handed in. A replacement
copies its source's request verdicts, the worse of each with its own;
one whose source's question was never judged keeps none. A decision's
parts with no verdict are judged once, at its document's first build,
and the verdicts kept. With no sync store registered no decision is
taken for delivery, built or judged. What the store is told of a
hand-in, and `SyncDelivery`, carry a receipt's words only as the
document would show them. An assignment's choices are its type's two
answers. Not built here: the decision page's words for a recheck the
provider cannot make yet (OS-4's screens).

### `internal/api`, `internal/export`, `ext`, `ext/exttest`, `web/app`, `web/e2e`, `docs` (OS-4): what other machines hold, the Sync row, Share in the project page

Cut once OS-3, DR1's PR-7 (#640) and MB0's PR-4 are on `main`.

**`ext.Host` doors for the sync store alone**: `PutRemote(ctx, r Remote)
error`, `DeleteRemote(ctx, kind, device, id string) error` and
`ClearRemote(ctx) error`, in `ext.SyncDoors` with the others. The kinds
are the constants `ext.RemoteDecision`, `RemotePointer`, `RemotePins`,
`RemoteDevice` and `RemoteHosts`; a report's states are `ext.SyncCurrent`,
`SyncBehind`, `SyncUnreachable`, `SyncLapsed` and `SyncSignedOut`, its
withheld changes at most `ext.MaxSyncWithheld` (50) and its answer page
at most `ext.MaxAnswerURL` (2 KiB); `MaxDecisionDoc` is OS-3's constant,
declared once.

```go
type Remote struct {
	Kind       string    `json:"kind"`       // decision | pointer | pins | device | hosts
	Device     string    `json:"device"`     // the provider's id of the machine that reported it, at most 64 bytes of [A-Za-z0-9._-]
	DeviceName string    `json:"deviceName"` // at most 64 bytes, one line
	ID         string    `json:"id"`         // the record's id on that machine (dr-n, a run's id, "pins", a device's id, "summary"), at most 64 bytes of [A-Za-z0-9._-]
	Rev        int64     `json:"rev"`
	At         time.Time `json:"at"`
	Data       []byte    `json:"data"` // the kind's body, below
}
func (r Remote) Valid() error
const (
	MaxRemoteData   = 64 << 10 // a pointer or a pins document
	MaxDecisionDoc  = 80 << 10 // a decision document (OS-3): the sum of its parts' bounds
	MaxRemoteDevice = 1 << 10
	MaxRemoteHosts  = 16 << 10
	MaxRemoteFiles  = 10_000
)
```

`Data` is, by kind: `decision`, a decision document of OS-3's shape, at
most `MaxDecisionDoc`; `pointer`, a run pointer document (with `ticket`
when present, above); `pins`, a `ui/pins.json` document as delivered
(OS-2); each of the last two at most `MaxRemoteData`. For HS2
(`docs/plans/hosts.md`): `device`, a live server device of the
workspace as the provider knows it (`{id, name, kind, platform, version,
lastReport, revoked}`, at most `MaxRemoteDevice`; a device of the kind
`host` is never one, refused by `Valid`), and `hosts`, another of the
person's servers' host summary (at most `MaxRemoteHosts`; its body is
`ext.HostSummary`, which HS2's PR-5 defines and makes the kind's `Valid`
check; until then the kind is checked for its bound and for being a JSON
object). Each kind has its own `Valid` (`TestRemoteDeviceAndHostsKinds`,
in `ext` and `internal/api`).

Kept in the data directory under `remote/<device>/<kind>-<id>.json` (at
most `MaxRemoteFiles`; past it the oldest of the final ones, a
decision closed or a run ended, are dropped first), written through
`store.Sub("remote")`, left out of the export and refused in an archive
(`leftOut`, `derived`: "the provider writes it again"), never on the sync
allowlist (`TestRemoteLeftOutOfTheExport`). `ClearRemote` removes the
whole directory.

**Routes**, admin: `GET /api/remote?kind=&ticket=&after=` answers
`{records, next?}`, at most 200 records a page, each `{kind, device,
deviceName, id, rev, at, data}`; `ticket` filters a decision by the
ticket its document names and a pointer by its `ticket` or by the run
ids the ticket's run links name; at most 16 MiB; `400 invalid_request`
for a bad value; `{records: []}` when `remote/` holds none, as it always
does with no provider. `GET /api/sync` (admin) answers the provider's
`SyncReport`, `{}` with no provider or one without `ext.SyncStatus`
(`TestRemoteRoutes`, `TestSyncStatusRoute`). Both are rows of
`docs/protocol.md` with their bounds.

**`ext.SyncStatus`**, an optional interface beside
`ext.ContinuityStore`:

```go
type SyncStatus interface{ SyncStatus() SyncReport }
type SyncReport struct {
	State      string         `json:"state"` // current | behind | unreachable | lapsed | signed_out
	Since      time.Time      `json:"since,omitzero"`
	Waiting    int            `json:"waiting,omitempty"`
	Withheld   []SyncWithheld `json:"withheld,omitempty"` // at most 50
	AnswerURL  string         `json:"answerUrl,omitempty"` // https, at most 2 KiB: where a remote decision is answered
	Device     string         `json:"device,omitempty"`     // this server's own device, as Remote.Device names devices
	DeviceName string         `json:"deviceName,omitempty"`
	Workspace  string         `json:"workspace,omitempty"` // opaque ids of the provider's, at most 64 bytes each
	Person     string         `json:"person,omitempty"`
}
type SyncWithheld struct {
	Ref   string `json:"ref"`   // what was kept back (CND-12's comment, dr-7), at most 64 bytes
	Words string `json:"words"` // at most 200 characters, one line
}
func (r SyncReport) Valid() error
```

Answered from memory, through the guard (a new wait, `ext.StatusWait`,
100 ms as `TakesWait` is; past it, an error or a panic, the route
answers `{}`, the free default); `Device` and `DeviceName` let a page tell
this server's own records from other `device` records; empty while the
provider is not signed in (`TestSyncStatusNamesItsOwnDevice`: its
device, workspace and person).

**The web app**:

- The ticket page's Decisions and Runs sections
  (`pages/dispatch/[project]/[number].vue`) and the decision page
  (`pages/decisions.vue`) list a remote record beside the local
  records, marked "on <device name>", read only: its actions hidden, and
  for an open decision whose document does not keep its request back,
  **Open its answer page** (the report's `AnswerURL` with the
  decision's id, in a new tab) when the report names one. A remote
  pointer reads "running on studio", "finished on studio" and so on, by
  the one status language (`utils/status.ts`, `StatusMark`).
- The sidebar's pinned group lists another machine's pins after this
  machine's, marked "on <device name>", greyed, opening nothing.
- The ticket page says, for a ticket whose read carries `alsoLinked`,
  "CND-14 is linked to the same GitHub issue, imported on studio: keep
  one, and unlink or close the other" (`utils/ticketAlsoLinked.ts`).
- Settings → The app (`components/AboutCard.vue`) gains a **Sync** row
  under Extensions (`data-about-row="sync"`) when `sync` is in `GET
  /api/capabilities`, from `GET /api/sync` (`utils/syncStatus.ts`):
  "current", "behind: 37 changes waiting", "unreachable since 14:02; 37
  changes waiting", "lapsed since 14:02: this machine works alone and
  shares nothing new", "signed out: nothing is shared", and the withheld
  changes' words beneath it.
- The project page (`pages/dispatch/[project]/index.vue`) shows **Share
  this project** and, for a project refused for its key or holding rows
  back for one, **Give this project another key** (`project.rekey`),
  under the same condition, with the share's state and words.

The Share, rekey and Sync controls need a registered provider, which
this repository never has: vitest covers their drawing with `sync` in a
stubbed capabilities reply. `docs/features.md` gains the entry for
sync's seams.

**As built (OS-4).** A record's file is `remote/<device>/<kind>-<id>.json`
with the device and the id hex-encoded, since the data directory's stores
take lower-case names alone. `GET /api/remote`'s `ticket` is the ticket's
key (CND-12): the server reads its id and its run links. The app reads
other machines' records and the report (`GET /api/sync`) only with `sync`
in the capabilities, every page of them, again when the page shows and
when the events stream comes back; `GET /api/remote` answers none with no
sync store registered, whatever a store once left in `remote/`. Beside the
report, `GET /api/sync` answers `here`, what this server keeps on the
machine of its own judgement (the copied-output check's state when it
cannot judge, the decisions with a part cut as output, the texts a hosted
session's agent wrote, kept), which the Sync row says beneath the
store's own withheld changes. A ticket's `alsoLinked` says "imported on
another machine": its read carries the other tickets' keys alone. A
decision's view says, while a held answer's recheck cannot be made yet,
"waiting to recheck user:nate's answer: the identity provider cannot say
now" (`waiting`, OS-3's words for the screens). A remote record of this
server's own device (the report's `device`) is never drawn. A project's
new key is carried into Brakeman's saved scope before the rekey, so a
grant follows the person's project; a project made under the new key
between that save and the rekey makes the rekey refuse while the scope
names that project, until it is put back.

**As built (the stream's review).** What the apply does with a row naming
one of this store's own tags (a tag this store's instance joined under):
it is this store's own row handed back by another machine (which hands
on what it received) when the log holds it as written here, the same
actor, op id and number, and is dropped; any other such row refuses the
whole batch, the refusal naming the tag; nothing that comes in is ever
kept as this machine's own. A tag a restored store inherited from the
instance it was exported from is another machine's: its rows the log
holds as written here are dropped, and the rest are applied as another
machine's rows. A row from
elsewhere is bound to the project of the entity it touches as this store
holds it (a create's by its value, a comment's or a revision's through
the ticket the row names; a relation's far end and a label added held to
it as well): a row bound to another project than the one it names, or to
one held here and neither shared nor dormant, refuses its batch; every
reference a row carries, as `Refs` lists them (a parent and a milestone
among them), is bound to the row's project, but for a relation's far
end, which may be of another shared project. A relation or a link from
elsewhere is applied only under an id this machine holds for no other
ticket, and the projection of a ticket's relations and links writes, on
a conflict of ids, only that ticket's own rows. A row
from elsewhere dated zero or from the year 9999 on is refused, at the
seam (`DispatchChange.Valid`) and at the apply. An approval writes its five
fields and the revision's state together, and a launch here needs the
newest row of every one of the six to be this machine's own: any one from
elsewhere reads as approved elsewhere. A
run link names a run of the machine that wrote it (the run's edge carries
the add's origin), and the reads that ask whether a run here is linked
count this machine's links alone. A project's key row is logged and never
projected (`TestRegistryKeyProjectedNeverTheRowsKey`), and a create held
for a key keeps the registry's key it waits for and takes it at its
replay (`TestRekeyedProjectSharedIntoAReplicaHoldingItsOldKey`). A batch
offers again only the held rows it can have released (those waiting for
what it made or a view it brought; with no rows, a start's or a rekey's,
those waiting for a key no project here holds), and a hold reads the held
table once a call; the invariants are replayed only after a row of their
edges, and that replay reads every edge row, since an edge's standing
turns on every other edge in the one order. A share's refusal acts on a
pending share alone: repeated it keeps the share refused, in any other
state it is an error. With no sync store registered Dispatch takes no
check and the output index is not opened; with one, Dispatch's check and
the ledger's never wait nor read a kept source inside a write, a row
written pending wakes its settle, and a process settles by its check
only the rows it wrote pending (kept in memory with the time it wrote
them, against whose prunes it settles them, whatever their stamp), any
other pending row being output. A settle judges a row first and reads
the prunes since its writing after the judgement, a prune's row is
committed before its fingerprints leave memory, and the index's stamps
have a fixed width, so that they order as text as their times do; the
prunes since a writing are compared by the machine's wall clock. Every
server that runs sessions over a data folder holds a presence lock there
for its life (`presence/<id>.lock`), with a sync store or with none:
while another live server holds one, a server with a sync store judges
every text output, a decision's parts too; alone again, it judges by its
check. A server of a version before the presence locks holds none and
is not seen. A server that cannot take its lock (a folder it
cannot write, a filesystem with no locks) cannot tell whether it is
alone and judges every text output as well; a server tries again for a
lock another server's look held for a moment, one look at the folder is
made at a time, and a lock a process left behind is nobody's and is
removed once it is a minute old. A live source has one queued feed, held to what it keeps
and a quarter and to 64 MiB across sources, what is let go recorded as a
prune; a source cut below what its ring still holds is holed until its
ring has turned over past the cut, and while any source is holed nothing
is judged clean, the hole staying until the prune recorded as it closes
is committed; one check reads at most 32 kept sources to confirm a hit, past
which the text is kept back as output. A decision's part is settled
against the prunes since its own writing (a response's since the
response). An answer handed in for a run that is not running is told
in fixed words that name the decision and what to do, and no run (a
run's name is a person's text, and fixed words carry none): "dr-1: its
run is not running here: continue the run to apply this answer". An
answer's content hash (`ext.AnswerContentSHA256`) is taken over the
fields the answer has, a key left out when it has no choice, no text or
no rationale, as a held answer's statement signs it. The Dispatch doors
answer a state of the store that passes (as many rows held as it may
hold, no workspace joined) as `ext.ErrNotNow`, apart from a refusal of
what was handed in (`ext.ErrRefused`): nothing is written, and a
provider hands the rows again later. A UI document is bounded as its file holds it at both
writers, and a file past the bound is set aside and read as none. The
tests the paragraphs above name that stand under other names:
`TestDormantProjectTurnsSharedOnItsFirstRemoteRow` is
`TestRefuseDormantAndUnshared`'s dormant case,
`TestLocalAndRemoteWritesConverge` is `TestReplicasConverge` (local
writes on both stores between exchanges, in six orders), and
`TestOrphanRowsHeldUntilTheirCreate` is
`TestChildBeforeParentHeldThenApplied` with
`TestHeldRowsSurviveARestart`; `TestWithheldCreateWithholdsItsDependants`
is held at the store's level, the rows filtered as a provider filters
them.

### Protocol

No WebSocket frame changes, so `internal/proto` and
`web/app/utils/protocol.ts`'s frame types do not change. The new HTTP
routes of OS-1b (`share`, `rekey`), OS-2 (`/api/ui/{name}`) and OS-4
(`/api/remote`, `/api/sync`) are rows of `docs/protocol.md` with their
bounds and their tests, and their reply types join `protocol.ts` beside
the other route types the web app reads. The project's read
(`share`, `heldBy`), the ticket's read (`alsoLinked`) and the revision's
read (`bodyHere`, `view`, `origin`, `approvedOrigin`) are documented in
`docs/protocol.md`'s Dispatch section with the rows that change them.
Each row changes `docs/repository-map.md` for what it adds.

### New files

Every other path this plan names is on `main`. New: `ext/dispatch.go`
(OS-1a, with OS-1b's types), `ext/answers.go` (OS-3), `ext/remote.go`
(OS-4); `internal/api/output_guard.go` and `internal/api/output_index.go`
(OS-1c), `internal/api/ui_state.go` (OS-2), `internal/api/remote.go`
(OS-4); in the data directory `ui/layout.json`, `ui/palette.json`,
`ui/pins.json`, `ui/writer`, `remote/` and `output-index.sqlite`; in
`web/app`, `composables/useUiState.ts`, `utils/uiState.ts`,
`utils/pins.ts`, `utils/planView.ts`, `utils/remote.ts`,
`utils/syncStatus.ts` and `utils/ticketAlsoLinked.ts` with their
`.test.ts` files; in `web/e2e`, `ui-state.spec.ts`, `remote.spec.ts` and
`sync-free.spec.ts`.

## Events, orderings and invariants

### Dispatch between replicas

- **Joining.** A provider whose machine is enrolled with its identity
  provider calls `DispatchJoin` with the tag, the workspace, its device
  and the overflow range it was handed: at its first `Start`, and
  again, with no restart, whenever the identity its tag was joined
  under is replaced (the machine enrolled again). A store whose active
  tag was joined under an earlier identity is retired
  (`DispatchRetire`) before the new join; a store restored as another
  instance has retired at its open already. The old tag becomes history
  and only rows written since take the new one
  (`TestJoinQualifiesOnlyUnqualifiedRows`,
  `TestReenrolmentKeepsHistoricalOrigins`,
  `TestTwoStoresRestoredFromOneExportJoinAsTwo`). A machine that leaves
  the workspace keeps its store; its provider marks its shared projects
  "no longer shared with you" (`DispatchUnshared`).
- **Sharing a project**, an ordered cut-over (a store already holding
  tickets joining a team, DS1's open question):
  1. `project.share` marks the project `pending` and reserves its numbers
     up to `share_reserved_to` (its next number plus 999), in one local
     transaction; creates go on taking those numbers, then overflow
     ones.
  2. The provider registers the project's key in the workspace's
     registry. A key the workspace holds already is refused there, and
     the provider calls `DispatchRefuseShare` with the words "the
     workspace has a project CND: give this one another key"; the person
     rekeys it, which `project.rekey` allows because nothing of it has
     been shared.
  3. The provider sends every row of the project from its first, in
     `seq` order, with its plan views, up to `upto`, the end of its read
     (`DispatchChanges`); it sends no row of a `pending` project
     otherwise.
  4. `DispatchCompleteShare(upto)` commits only if no row of the project
     is past `upto` (otherwise `ErrShareMoved`: the rows past it are sent
     first and it is tried again), and sets the project `completing`, so
     every row of the project is either in the share's read or written
     after the completion's commit, where the provider's ordinary sending
     takes it: none is skipped by both. While `completing`, creates still
     take reserved, then overflow numbers.
  5. Once the provider says the project is shared,
     `DispatchConfirmShare` sets it `shared` here; from then on the
     sharer's creates take block numbers, as every replica's do.
  A crash or a lost reply at any boundary leaves the project `pending` or
  `completing`, the phase a provider resumes from at its next start, and
  each step it repeats changes nothing twice
  (`TestShareReservesItsNumbers`, `TestCompleteShareRefusesRowsPastUpto`,
  `TestCompleteThenConfirmShare`).
- **Numbers.** Three disjoint namespaces per shared project, and a
  fourth for a retired store until it joins: the project's own numbers
  from before its share, up to `share_reserved_to` (a share refused at or
  past 1,000,000); blocks, handed by the provider above the reservation
  and below 1,000,000; each replica's overflow range, above 1,000,000 and
  disjoint from every other replica's; past them, a retired store's
  provisional window above 2,000,000,000 until it joins again. A replica
  holds its current block and the next, both handed to its active tag
  and dropped with it; a create never waits
  (`TestBlocksAndOverflowNeverWait`, `TestBlocksStopBelowTheOverflowRange`,
  `TestBlockOfARetiredTagNotUsed`,
  `TestShareRefusesNumbersInTheReservedRange`,
  `TestRetireDropsBlocksOverflowAndShares`). Overflow and provisional
  numbers are never renumbered. An incoming create whose number a ticket
  here already holds (which these rules make impossible, and a restored
  store might still meet) is held, not failed
  (`TestDuplicateNumberHeldNotFailed`).
- **Sending.** A provider reads the log through `DispatchChanges` and
  sends the local rows of a shared project whatever tag they were
  written under (a re-enrolled machine's offline edits under its old
  tag, a project never shared before a restore and shared after it). It
  never sends a row with `Remote` set, but in a recovery (below). The
  store hands no row past a `pending` verdict, so a cursor never passes
  one.
- **Receiving.** A provider hands each batch it receives to
  `DispatchApply`, which applies or durably holds every row in one
  Dispatch transaction, and advances its own cursor only after it
  returns; a crash in between applies the batch again, whose rows are
  then dropped as held identities. A project the store did not hold
  arrives with its `create` and is received as shared, in the
  workspace of the store's active tag (OS-1a, `ApplyRemote`'s step 2),
  so the receiver's own creates in it take block numbers and its own
  rows are sent (`TestApplyRemoteDropsHeldIdentities`,
  `TestApplyRemoteProjectsByClass` for each class,
  `TestReconcileAfterRemoteRows`, `TestRemoteRowsIgnoreLocalBounds`,
  `TestIncomingKeyHeldByALocalProjectIsHeld`).
- **Recovery.** A provider that rebuilds a project the workspace lost
  sends every row its store holds of the project, its own and those it
  received from others, each with its own origin; a receiver stores such
  a row with `recovered_by` the sender's tag and the page shows
  "restored by studio" beside its author (`TestRecoveredByKept`). A row
  of the same identity sent later by its own origin with the same
  content is the same row; with other content it is the provider's to
  refuse and report.
- **The merge.** DS1's and DS2's field rules, as `rules.go` holds them,
  over canonical actors, for local writes and remote rows alike: every
  writer stamps its row through `stampFor` and projects the field by
  `Merge`, so the projection is a function of the log alone and two
  replicas holding the same rows hold the same tables, whichever wrote
  last on which clock (`TestCanonOrdersTheSameEverywhere`). A row waits,
  held, for every entity it references, so no order of arrival fails a
  batch or skips a row. A value one machine keeps back is the one way
  two replicas can hold different rows, until the field's next edit that
  is sent.
- **Launches.** A replica never launches what another machine approved
  or made: a revision held as a view is not launchable, and the machine
  holding the whole body launches it only on its own operator's
  approval. Launches do not replicate; another machine sees the run a
  launch made by its pointer, matched to the ticket by the pointer's
  `ticket` (OS-4).
- **Echo.** A row applied from elsewhere is in the local log with
  `remote` 1; `DispatchChanges` hands it with `Remote` set, and a
  provider sends only local rows.
- **Rekeyed history** (OS-1b): unshared under key k to k′ by
  `project.rekey` (its row logged, the tickets' keys following, the rows
  held for k′'s local holder applied in the next transaction; a crash
  between leaves them held, applied at the next start's replay of held
  rows); unshared to `pending` by `project.share`; `pending` back to
  unshared by `DispatchRefuseShare`; no rekey while `shared` or
  `share_state` is set; a receiver keys the project and its tickets by
  the registry key from the first batch on.

### A held answer on the owner's server (OS-3)

- **Only the server whose ledger holds the run records it.** A provider
  hands a held answer to `RespondDecision` on the one server whose
  ledger holds the decision; any other server's ledger answers
  `not_found` for it, since a decision is never replicated (Owner
  decisions, 6).
- **Recorded once.** DR1's idempotence on `(decision, actor,
  responseId)` makes a repeat, after a `moved` or a lost reply, answer
  DR1's repeat with the same receipt; `Repeat` is kept apart from the
  status, so a repeat of a kept refusal is `refused` with `Repeat`
  true, never `recorded`.
- **The fence is the ledger's evidence.** The held answer's id and fence
  are written in DR1's transaction with the response or the kept
  refusal, so `SyncDelivery` finds a fence exactly when the ledger
  recorded that answer; a provider that finds none settles nothing,
  since a ledger that never received the answer and one that crashed
  before its commit look the same from outside.
- **Statuses**, as `RespondDecision` answers them: `recorded` (DR1
  accepted it, the receipt kept); `refused` (`decision_answered`,
  `decision_closed`, `stale_decision`, `stale_policy`, `not_approver`,
  `decision_limit`, first time or a repeat, with DR1's words;
  `not_approver` for a held answer to a comparison's selection; and
  `answered_at_server` to a record under a router's policy on a server
  whose router confirms policies, final, nothing written);
  `unknown_authority` (the identity provider cannot say now, or the
  decision's policy is being written: nothing written); `not_held` (the run is not running: the server was stopped
  and the run interrupted, as on `main`; nothing written, the words
  "run release-7 is not running: continue it to apply this answer";
  the operator's Continue moves the decision to the continuation, DR1's
  O6, and a provider hands the answer again when the run's pointer next
  changes); `moved` (DR1's `decision_moved`: tried again).
- **Applied only as DR1 applies it, and once**, after `AdmitsAnswer`
  says yes at application; unknown waits, no supersedes (OS-3, At
  application).
- **"Not known" never decides.** A server whose identity provider cannot
  say records nothing and applies nothing; it neither refuses nor
  supersedes on it (Owner decisions, 7).

### UI state and remote records (OS-2, OS-4)

- A layout is ordered by its (`at`, `by`): `at` set when its content
  changed, past the `at` it replaced, and kept through C6's replays and
  through `SetUIState`, so a machine that comes back and replays an
  unchanged old layout never wins over a newer one from another machine.
  Recents merge by key (each key's greater `count` and later `last`, a
  pure function, commutative and idempotent), so two machines writing
  them reach one value without a loop: a `SetUIState` that changes
  nothing writes nothing and marks nothing pending. Pins are each
  machine's own and shown elsewhere as remote records.
- Remote records are written and removed only by the provider
  (`PutRemote`, `DeleteRemote`, `ClearRemote`); the server reads them
  and never acts on them: no route takes a remote record's id as the
  target of an action.

### A provider's life, as the doors see it

What a provider calls, in order, so the store's part holds whatever the
provider has not yet done:

| When | The provider calls | The store's part |
|---|---|---|
| its first `Start` with an enrolled machine | `DispatchRetire` if the active tag was joined under another identity, `ClearRemote`, `LastDecision`, `SyncReplay`, `DispatchJoin` | at `Open`, before any of it, a store opened as another instance has retired its allocations already |
| the machine enrolled again, mid-session | the same, with no restart | `Join` retires in its own transaction |
| the workspace's shared copy gone (the provider says so) | `DispatchDormant`, `SyncReplay`, `ClearRemote`, then a join again | dormant projects keep their blocks and send nothing |
| what the provider holds elsewhere put back from an older copy | `SyncReplay`, a recovery of every shared project (above), `ClearRemote` | rows of other origins kept with `recovered_by` |
| the machine removed or revoked | `ClearRemote`, `DispatchUnshared` | every project of the workspace "no longer shared with you" |

Each call is safe to repeat: a retire of a retired store changes
nothing, a join of the active tag answers the same, a replay request
coalesces.

### The invariants

- **Nothing is written under an allocation that has been invalidated.**
  Blocks, overflow ranges and share reservations are usable only by the
  instance and tag they were handed to, judged at the store's open and
  at every join (`TestRestoredStoreRetiresAtOpenBeforeAnyWrite`, OS-1a).
- **A shared project's key is the registry's on every replica**
  (`TestRekeyedProjectSharedIntoAReplicaHoldingItsOldKey`,
  `TestRegistryKeyProjectedNeverTheRowsKey`, OS-1b).
- **Every row keeps its origin**, and a recovered row says who sent it
  (`TestRecoveredByKept`, OS-1a).
- **Every document can be handed out again with no change to its
  source** (`TestReplayDoorReplaysEveryDocWithoutAChange`, OS-2).
- **No verdict is `clean` that the check could not make.** The value and
  the prior are judged apart; an index past its bound, missing,
  rebuilding or behind answers `pending`, never `clean` (the OS-1c
  tests).
- **A tracker's fields come from a tracker read, never from another
  machine's snapshot** (`TestCounterpartMergedWithoutRemoteSnapshots`,
  OS-1a).
- **Two tickets of one tracker item are both kept and each names the
  other**, never folded or renumbered
  (`TestSameItemImportedOnTwoReplicasKeptAndMarked`, OS-1a;
  `utils/ticketAlsoLinked.test.ts`, OS-4).
- **A held answer is recorded only by the ledger that holds its run, at
  most once, and applied only after a yes at that moment**
  (`TestFenceWrittenInDR1sTransaction`, `TestViaSyncAppliedOnce`,
  `TestViaSyncUnknownWaitsAndNeverSupersedes`, OS-3).
- **What the person confirmed names the target.** The server records a
  held answer only when the request hash it computes from its own record
  is the one the statement names (`TestTwoDecisionsWithTheSameQuestionToldApart`,
  `TestSubstitutedAnswerRefused`, OS-3).

### What each store holds

Dispatch's database: remote rows among its own, with their origins,
verdicts and `recovered_by`, held rows and bodies, plan views, the
replica's tags and other replicas' names (schema 8), the replica's
overflow range on its tag and a share's words on its project (schema 9),
its instance as on `main`. The data directory: `ui/` (three documents and `ui/writer`),
`remote/` (derived, never exported), `output-index.sqlite`
(fingerprints of captured output, never output, never exported or
handed out). The ledger: the decision records with their `via: sync`
authorities (with the statement's hash and the fence),
`decision_statements` and `decision_output` (schema 16). Nothing here keeps a provider's credential or address.

### Migrations

Each store step is a row of its own (The PRs), holding every table and
column the later rows need, and ships alone, right before the code that
uses it:

- **Dispatch 7 → 8 (OS-1a0)**: additions only (every table and column
  of OS-1a and OS-1b above), with a Go step that makes the view of every
  existing revision; a version 8 database opens read-only under a
  version 7 binary (`TestSchema8MigratesAndKeepsEveryRow`,
  `TestMigrationMakesTheViewOfEveryRevision`,
  `TestVersion8OpensReadOnlyInVersion7`).
- **Dispatch 8 → 9 (#1049)**: additions only, a replica tag's
  `overflow_from` and `overflow_to` and a project's `share_words`, which
  OS-1b's range and share words then move onto; a version 9 database
  opens read-only under a version 8 binary
  (`TestAFreshStoreHasVersion9sColumns`,
  `TestSchema9MigratesAndKeepsEveryRow`,
  `TestAFailedMigrationLeavesVersion8AsItWas`,
  `TestVersion9OpensReadOnlyInVersion8`).
- **The ledger 15 → 16 (OS-3a)**: additions only, `decision_output`
  and `decision_statements`, each keyed by the decision record's number
  and deleted with its decision (`ON DELETE CASCADE`), so a pruned run's
  decisions take their statements and verdicts with them, and a decision
  handed on to another run keeps them with it; a version 16 database opens read-only under a version 15
  binary, as the ledger's rule is (`TestSchema16MigratesAndKeepsEveryRow`,
  `TestVersion16OpensReadOnlyInVersion15`,
  `TestSchema16FreshAndAFailedMigration`). OS-3 then writes DR1's
  `authority` JSON with the `via: sync` fields, which DR1's decoder
  gains (an older binary reads a `via` it does not know as no grant, and
  applies nothing of it). The
output index: a file of its own, made and filled at the first start that
lacks it, its verdicts `pending` until it is whole. Each migration takes
the next version at its merge, whatever other row took one first.

## What counts as terminal output

**Decided by the owner on 2026-11-20: captured bytes only** (Owner
decisions, 27). The Program's stored-record rule (no terminal output,
tokens or secrets in any stored record that leaves a machine) is held
by this reading.

Terminal output is the bytes Conductor captured: a session's screen,
scrollback and ring, its activity log's words, a check's log, a history
record's words. None of them is handed out, and any text, an agent's or
a person's, that quotes at least 64 bytes or two whole lines of what the
server held captured when the text was written is withheld (a Dispatch
row) or cut (a decision's part) by a verdict stored with the text, so
time does not weaken it; text whose verdict cannot be known (an
agent's, written before the check existed) is withheld. An agent's own
text syncs unless it quotes captured output. Not caught, by definition:
output Conductor never captured (a paste from another terminal) and a
fragment below the threshold (a test's name, "exit status 1"), which
appear in writing and in output alike.

**As built.** The reading is one place: `agentTextIsOutput` in
`internal/api/output_guard.go`, false, read by `outputGuard.Judge`,
which every write of a Dispatch row and every hand-out of one written
before the check calls with whether an agent wrote the text
(`dispatch.AgentActor`: every actor but the operator's and the store's
own). OS-3's decision parts go through the same calls.

## Out of scope

- Moving execution: a run continued, retried or taken over on another
  machine, failover. Replicas are read only for runs; a plan approved
  elsewhere is launched here only after this machine's own approval.
- Attachments' files and rows, launch packages and launches across
  machines; crews, the catalog and run records (Owner decisions, 3
  and 5).
- Reviews and their findings (RV1), captures and their sends (PV1) and
  publications (IN1a) across machines: they stay on the machine that
  made them in v1 (Owner decisions, 21); a later item may add them with
  their own field rules. A limit that follows: nothing stops one outcome
  being published twice from two machines that share a ticket; a person
  on the second machine sees the first machine's publication only on the
  tracker item itself, not on the ticket's page.
- Replicating decision records: another machine is shown a copy, read
  only, as a remote record.
- History (HS1's records) across machines: TS1.
- Unsharing a project, or moving one to another workspace; a project's
  deletion and archive replicate as its fields do.
- A hybrid logical clock for `at` (Owner decisions, 1).
- What a provider does with what it is handed, how it authenticates, how
  it numbers, how it lets a person answer, and the pages a person uses
  away from the machine: the provider module's. The host overview page:
  HS2. Push and a phone app: MB1.
- A person answering through the public switchyard: MB0's decision links
  are the free path.

## Tests per surface

Go race tests for every new path, each refusal beside its success;
vitest for the app's units; Playwright on the shipped build (no
test-only binary registers a provider: the doors are reached through
`exttest.FakeHost` and the real handlers in Go tests).

- `internal/dispatch` (OS-1a): `TestSchema8MigratesAndKeepsEveryRow`,
  `TestMigrationMakesTheViewOfEveryRevision`,
  `TestVersion8OpensReadOnlyInVersion7`, `TestLocalActorMayNotBeginWithAt`,
  `TestCanonOrdersTheSameEverywhere`, `TestJoinQualifiesOnlyUnqualifiedRows`,
  `TestReenrolmentKeepsHistoricalOrigins`,
  `TestTwoStoresRestoredFromOneExportJoinAsTwo`,
  `TestChangesFromPagesWholeRows` (the page's bound in rows and bytes,
  the next position), `TestApplyRemoteDropsHeldIdentities`,
  `TestApplyRemoteProjectsByClass` (each class),
  `TestApplyRemoteRefusesWhatDoesNotReplicate` (a `launch`, a `review`,
  a `capture`, a `publication`, a project's `cwd`, a ticket's
  `external_snapshot` and an `attachments` add: the batch refused,
  nothing applied), `TestReplicasConverge` (two stores, the same rows in
  every batch split and order of a generated log, the row tables equal),
  `TestReconcileAfterRemoteRows`, `TestRemoteRowsIgnoreLocalBounds`,
  `TestIncomingKeyHeldByALocalProjectIsHeld`,
  `TestOrphanRowsHeldUntilTheirCreate`, `TestChildBeforeParentHeldThenApplied`,
  `TestDuplicateNumberHeldNotFailed`, `TestHeldRowsSurviveARestart`,
  `TestHeldRowsBounded`, `TestPlanViewLeavesNoArgvOrPath` (a real DS2
  body), `TestRemoteApprovalDoesNotLaunch`,
  `TestLocalApprovalOfARemoteApprovalLaunches`,
  `TestViewOnlyRevisionDoesNotLaunch`, `TestResendLooksUpLocalRowsOnly`,
  `TestLocalEditAfterALaterDatedRemoteEditWins`,
  `TestLocalAndRemoteWritesConverge`, `TestRetireDropsBlocksOverflowAndShares`,
  `TestTwoStoresRestoredFromOneExportNeverShareANumber`,
  `TestRestoredStoreRetiresAtOpenBeforeAnyWrite`, `TestRecoveredByKept`,
  `TestCounterpartMergedWithoutRemoteSnapshots`,
  `TestSameItemImportedOnTwoReplicasKeptAndMarked`,
  `TestFirstBatchOfAnUnknownProjectApplied`, `TestReceivedProjectIsShared`,
  `TestChangesFromPagesWholeRows` at `MaxChangesPage` (a limit of 1,001
  refused, a full page one valid `ext` batch). The three schema tests
  are row OS-1a0's.
- `internal/crew/ledger` (OS-3a): `TestSchema16MigratesAndKeepsEveryRow`,
  `TestVersion16OpensReadOnlyInVersion15`,
  `TestSchema16FreshAndAFailedMigration`.
- `internal/dispatch` (OS-1b): `TestRekeyOnlyBeforeShare`,
  `TestRekeyAfterRestartAppliesHeldRows`, `TestRekeyRefusedWhileSharePending`,
  `TestShareReservesItsNumbers`, `TestCompleteShareRefusesRowsPastUpto`,
  `TestCompleteThenConfirmShare`, `TestRefuseShareDropsTheReservation`,
  `TestDormantProjectTurnsSharedOnItsFirstRemoteRow`,
  `TestUnsharedProjectTakesNothing`, `TestBlocksAndOverflowNeverWait`,
  `TestBlocksStopBelowTheOverflowRange`, `TestBlockOfARetiredTagNotUsed`,
  `TestShareRefusesNumbersInTheReservedRange`, `TestUnjoinedStoreIsMain`,
  `TestRekeyedProjectSharedIntoAReplicaHoldingItsOldKey`,
  `TestRegistryKeyProjectedNeverTheRowsKey`.
- `internal/dispatch`, `internal/api` (OS-1c): `TestOutputGuardOverAnyText`,
  `TestOutputVerdictWrittenWithTheRow`,
  `TestOutputLikePriorLeftOutOfACleanCorrection`,
  `TestAgentRowQuotingOutputIsMarked`, `TestVerdictKeptAfterTheSourceIsGone`,
  `TestHistoricalAgentRowWithheld`, `TestPersonsPasteOfCapturedOutputWithheld`,
  `TestQuoteAtTheThresholdCaught`, `TestWinnowingCatchesEvery64ByteQuote`,
  `TestQuoteFromOutsideTheMemoryCacheCaught`, `TestSaturatedIndexFailsClosed`,
  `TestPendingVerdictNotHandedOutDuringRebuild`,
  `TestPendingVerdictOfAnEndedProcessWithheld`,
  `TestPassageAssembledByCursorMovesCaught`, `TestIndexPrunedWithItsSource`,
  `TestOutputIndexLeftOutOfTheExport`.
- `internal/api` (OS-1a to OS-4): `TestViewOnlyRevisionReadsItsView`,
  `TestShareNeedsASyncProvider`, `TestShareAndRekeyRoutes` (bounds,
  unknown fields, each refusal), `TestHostDispatchDoorsOnlyForTheSyncStore`
  (every door, on a provider's `Host` that is not the sync store, on the
  host agent's `Host` and on a switchyard: `ErrUnsupported`),
  `TestBlockLowCalledAfterCommit`, `TestUIStateRoutes` (bounds, unknown
  fields, an unknown name), `TestUIStateKeepsAtAndByOnReplay`,
  `TestUIEditAfterALaterDatedDocumentIsNewest`,
  `TestUIWriterLeftOutOfTheExport`, `TestSetUIStateRefusesAnOlderDocument`,
  `TestUIStateOnTheAllowlist`, `TestPinsInTheExport`,
  `TestRestoreWritesAnotherProvidersDir`, `TestReplayDoorReplaysEveryDocWithoutAChange`,
  `TestReplayRequestsCoalesce`, `TestDecisionDocIsTheTable`,
  `TestDecisionDocCutsPastItsBound`, `TestDecisionDocWithTheMostResponsesFits`,
  `TestAgentContextQuotingOutputIsCut`,
  `TestOutputInTheQuestionOrAnOptionKeepsTheRequestBack`,
  `TestDecisionVerdictKeptAfterThePrune`, `TestEarlierSchemaDecisionOfAnAgentKeptBack`,
  `TestLocalAnswerReachesTheDocument`, `TestSyncResendMarksOneDoc`,
  `TestRequestPartNamesTheTarget`, `TestTwoDecisionsWithTheSameQuestionToldApart`,
  `TestRespondDecisionNeedsAdmits`, `TestUnknownAdmissionWritesNothing`,
  `TestHeldAnswerRecordedOnceAndRepeat`, `TestRepeatOfARefusalIsRefused`,
  `TestHeldAnswerNotHeldRunStaysHeld`, `TestForgedHeldAnswerRefused`,
  `TestSubstitutedAnswerRefused`, `TestFenceWrittenInDR1sTransaction`,
  `TestSyncDeliveryReadsAndNeverWrites`, `TestLastDecisionReadsTheLedger`,
  `TestSyncAnswerRecheckedAfterRestart`,
  `TestHeldAnswerRefusedWhereTheRouterConfirmsPolicies`,
  `TestHeldAnswerAdmittedByTheEntries`, `TestHeldAnswerToASelectionIsRefused`,
  `TestSyncAuthorityAtEveryBoundRecorded`, `TestDispatchChangesCarryRefs`,
  `TestWithheldCreateWithholdsItsDependants`,
  `TestPaletteDeliveredWithoutPaths`, `TestPinNameUnderTheOutputCheck`,
  `TestPointerOfAnUnsharedProjectNamesNoTicket`,
  `TestPointerTicketFollowsTheShare`, `TestRemoteLeftOutOfTheExport`,
  `TestRemoteRoutes`, `TestSyncStatusRoute`, `TestRemoteDeviceAndHostsKinds`,
  `TestSyncStatusNamesItsOwnDevice`, `TestPointerNamesItsLaunchTicket`; and
  the export's tests for `ui/`, `remote/` and `output-index.sqlite`.
- `internal/crew`, `internal/crew/ledger` (OS-3):
  `TestOnDecisionsNamesTheDecisionsChanged`, `TestDecisionOutputSchema16`,
  `TestViaSyncAppliedOnlyAfterAdmits`, `TestViaSyncSupersededWhenAdmitsSaysNo`,
  `TestViaSyncUnknownWaitsAndNeverSupersedes`, `TestViaSyncAppliedOnce`,
  `TestRevokedSignInAfterDeliveryNotApplied`,
  `TestEngineRefusesAHeldAnswerUnderAPolicy`.
- `ext`, `ext/exttest` (OS-1a to OS-4): each new record's `Valid` at and
  past every bound (`DispatchChange`, `DispatchApplied`, `HeldAnswer`,
  `AnswerOutcome`, `Remote` by kind, `SyncReport`), `FakeHost`'s new
  doors, the `Continuity` check with decision documents and the doors'
  refusals.
- vitest (OS-1a, OS-2, OS-4): `utils/planView.test.ts`,
  `utils/uiState.test.ts`, additions to `utils/palette.test.ts`,
  `utils/editorLayout.test.ts` and `utils/sidebar.test.ts`,
  `utils/pins.test.ts`, `utils/remote.test.ts`, `utils/syncStatus.test.ts`,
  `utils/ticketAlsoLinked.test.ts`, and the Share, rekey and Sync
  controls' drawing with `sync` in a stubbed capabilities reply.
- Playwright (OS-2, OS-4): `ui-state.spec.ts` (a layout, recents and a
  pin set in one browser context are there in a second, through the
  server; a pin survives a reload; a layout written past its bound is
  refused and the page keeps working), `remote.spec.ts` (remote records
  seeded in the e2e server's data directory before the server starts,
  derived files it only reads: the ticket page's and the decision page's
  "on studio" rows, read only with the actions hidden, and the sidebar's
  remote pins; its header says the Share, rekey and Sync controls need a
  registered provider and are vitest's), `sync-free.spec.ts` (the free
  build: no Share control, no Sync row, no remote rows). Each new spec
  passes `contrast.spec.ts` and `utils/designRules.test.ts` as they are,
  and asserts layout by measures in both themes and at 390 x 844.

## Boxcar constraints

1. Launches through `crew.Launcher`: respected; nothing here launches,
   and a held answer reaches a run only through DR1's application.
2. `session.Local` drives a `session.Process`: not touched (the index
   reads the screen and the ring, never the process).
3. An attempt's context is files in its worktree: respected; an answer
   that reaches a next attempt does so through DR1's task file.
4. The agent talks to the server over the network: not touched.
5. Checks go through `internal/proc`: not touched; the index reads the
   check logs the ledger keeps.
6. Platform splits are platform tags and the UI shows a capability:
   respected; Share, the Sync row and the remote rows show when `sync`
   is in `GET /api/capabilities`, never by a plan.
7. Evidence rows' `where`: not touched; pointers carry no evidence.
8. `hostagent` is a launch site: not touched; its `Host` refuses every
   new door.

## Open-core checklist

- [x] Article 1: no paid code; every row is a seam any registered
  sync provider uses, and its refusals are registration checks (`409
  no_sync`, `ErrUnsupported`).
- [x] Article 2: the open-source build stays fully useful and gains UI
  state in the data directory and pins; nothing is removed or moved
  behind a provider.
- [x] Article 3: no flag, setting or tag; sharing, receiving, a held
  answer and a remote record exist only through a registered provider's
  calls.
- [x] Article 4: stop, inspect, revoke, export and required approvals are
  untouched; a held answer is recorded and applied only by DR1 on the
  server that holds the run, after the identity provider says yes, never
  on "not known".
- [x] Stored-record rule: no captured output, token or secret is handed
  to a provider; free text carries the copied-output verdict made when
  it was written; the one credential-shaped value kept, a held answer's
  statement, grants nothing and records only the answer it names.

## The PRs

The rows below are units of work, in their order; how rows are grouped
into pull requests follows `AGENTS.md` (Working an issue) at the time
they are built. Two things that rule sets apart hold whatever the
grouping: each store step is a row of its own that ships alone, right
before the code that uses it (OS-1a0 and OS-3a), and OS-3's held answer
handed in (`via: sync`) changes who may answer a decision, a boundary,
so it is a part of its own with an adversarial review of its own. Each
row leaves `main` working and green on its own, its checks those of
every surface it touches. Sizes are changed lines, code and tests. A
provider in another module builds against a tag of this module: a tag
that holds OS-1a, OS-1b, OS-2 and OS-1c gives it the Dispatch doors
with their verdicts, and one that holds OS-3 and OS-4 the rest; a
provider is not to use `DispatchChanges` from a tag without OS-1c,
whose verdicts it relies on.

| Row | Title | Contains | Touches | Tests | Depends on | Size |
|---|---|---|---|---|---|---|
| OS-0 (#996) | plans: sync's seams | this file; the amendments to `docs/plans/dispatch.md`, `docs/plans/dispatch-evidence.md`, `docs/plans/decisions.md`, `docs/plans/review-repair.md` and `docs/plans/tracker-linking.md`; the copied-output check's reading written as the open question it is | `docs/plans` | none (docs) | none | docs ~2,300 |
| OS-1a0 | dispatch: schema 8 alone | every table and column OS-1a and OS-1b use (`replica_tags`, `replica_names`, the `changes` columns, `held_rows`, `held_bodies`, `plan_views`, the `projects` and `plans` columns) and the views' Go step (`migrationsGo[7]`); nothing reads or writes them yet | `internal/dispatch` | `TestSchema8MigratesAndKeepsEveryRow`, `TestMigrationMakesTheViewOfEveryRevision`, `TestVersion8OpensReadOnlyInVersion7` | OS-0 | ~200 + 250 |
| OS-1a | dispatch, ext: rows from elsewhere (after the step) | `Join`, `Retire` (and at `Open`), `Canon`, origins, `ChangesFrom` with `MaxChangesPage`, `RevisionView`, `ApplyRemote` with the received project, `held_rows` and `Refs`, `Replicates`, `stampFor` in every scalar writer, the provisional window, view-only revisions in `Store.Plan` and `dispatch_plans.go`, `launchable`'s and `ApprovePlan`'s rules; the counterpart group merged on a receiver and `TicketsLinkedTo` with `alsoLinked`; `ext.DispatchChange` with `Refs`, `ext.DispatchApplied`; the plan view's drawing; `docs/repository-map.md` | `internal/dispatch`, `internal/api`, `ext`, `web/app`, `docs` | the OS-1a tests above | OS-1a0 | ~700 + 650 |
| Dispatch 8 → 9 (#1049) | dispatch: schema 9 alone | a replica tag's `overflow_from` and `overflow_to` and a project's `share_words`; nothing reads or writes them yet | `internal/dispatch` | `TestAFreshStoreHasVersion9sColumns`, `TestSchema9MigratesAndKeepsEveryRow`, `TestAFailedMigrationLeavesVersion8AsItWas`, `TestVersion9OpensReadOnlyInVersion8` | OS-1a | ~70 + 300 |
| OS-1b | dispatch, api, ext: sharing with its cut-over, numbers, rekey and registry keys, the provider's doors | blocks with their tag and instance, overflow, the namespaces, `project.share`, `CompleteShare`, `ConfirmShare`, `RefuseShare`, `Dormant`, `Unshared`, `project.rekey` with held rows applied, keys projected from the registry, the two routes, the `Host` doors and `DispatchWatcher`, `FakeHost`; the range and a share's words on version 9's columns, a refused share kept as `refused`; `docs/protocol.md` | `internal/dispatch`, `internal/api`, `internal/hostagent`, `ext`, `ext/exttest`, `docs` | the OS-1b tests above | OS-1a; Dispatch 8 → 9 | ~700 + 700 |
| OS-2 | api, web: UI state in the data directory, pins, restore into another provider's directory, a replay on request | `ui/` documents with `at` and `by`, routes, `ui/writer` left out, the allowlist with what leaves of each document, `Host.SetUIState`, `RestoreRequest.ProviderDir`, `Host.SyncReplay`; `useUiState` and its callers; pins; `ui-state.spec.ts` | `internal/api`, `internal/export`, `internal/hostagent`, `ext`, `web/app`, `web/e2e`, `docs` | the OS-2 tests above | OS-1b | ~800 + 750 |
| OS-1c | api, dispatch: the output index and the copied-output check for Dispatch's rows and pins' names | `outputGuard`, `outputIndex` (winnowed fingerprints and lines in `output-index.sqlite`, the memory cache, prunes with their sources, the bound, failing closed with `pending`, pending made `output` at start), the verdicts at every operation's commit and a pin's `nameVerdict`, `DispatchChanges` carrying both and stopping before a `pending` row; this file amended with the owner's reading (no store step: the columns are OS-1a0's, the index a file of its own) | `internal/api`, `internal/dispatch`, `internal/export`, `ext`, `docs` | the OS-1c tests above | OS-2; the owner's reading of terminal output (Owner decisions, 27) | ~650 + 700 |
| OS-3a | crew/ledger: schema 16 alone | `decision_output` and `decision_statements`; nothing reads or writes them yet | `internal/crew/ledger` | `TestSchema16MigratesAndKeepsEveryRow`, `TestVersion16OpensReadOnlyInVersion15`, `TestSchema16FreshAndAFailedMigration` | OS-1c | ~80 + 150 |
| OS-3 | api, crew, ext: decisions handed out, answers handed in (after the step; a boundary part with its own adversarial review) | `ledger.OnDecisions`, the decision document with its request part, its policy part and `requestSHA256`, the check at each write into `decision_output`, `Host.SyncResend`, `ext.AnswerCheck`, `ext.SubjectChecker`, `Host.RespondDecision` with `via: sync` (the statement in `decision_statements`), every held answer refused `answered_at_server` on a server whose router confirms policies, `Host.SyncDelivery`, `Host.LastDecision`, the recheck at application that waits on unknown, `AnswerOutcome.Repeat`; the agent skill's line | `internal/api`, `internal/crew`, `internal/crew/ledger`, `internal/agents`, `internal/hostagent`, `ext`, `ext/exttest`, `docs` | the OS-3 tests above | OS-3a; DR1's PR-4 (#637) and the decide grant (#776), both on `main`; the owner's reading of terminal output (Owner decisions, 27) | ~800 + 850 |
| OS-4 | api, web, ext: what other machines hold, read only; the Sync row; Share in the project page; the features entry | `Host.PutRemote`, `DeleteRemote`, `ClearRemote`, `remote/` with the kinds `decision`, `pointer`, `pins`, `device` and `hosts`, the pointer's `ticket` (`RunPointer` and its columns read `runs.ticket`), `GET /api/remote`, `ext.SyncStatus` with its own device, workspace and person, `GET /api/sync`; the ticket page's, the decision page's and the sidebar's remote rows, the Sync row, Share and rekey controls, the ticket page's `alsoLinked` words; `remote.spec.ts`, `sync-free.spec.ts`; `docs/features.md` | `internal/api`, `internal/crew/ledger`, `internal/export`, `internal/hostagent`, `ext`, `ext/exttest`, `web/app`, `web/e2e`, `docs` | the OS-4 tests above | OS-3; DR1's PR-7 (#640) and MB0's PR-4 on `main` | ~750 + 750 |

Each row's checks are the repository's (`docs/checks.md`): the module's
race tests, `make lint`, `make lint-static`, and for OS-1a, OS-2 and
OS-4 the web's typecheck, vitest and `make test-e2e`. Every row that
adds an `ext.Host` method adds it to the host agent's `Host`
(`internal/hostagent/exthost.go`), answering `ErrUnsupported`, or the
build fails. OS-4 is the last code row and adds the `docs/features.md`
entry.

## Owner decisions

Dated 2026-11-18, from the item's decisions that shape its open-source
rows, each at its default. Each is a default the owner may overturn with
a dated row in the Program's status log; the reasoning is why it was
taken. The last, 27, the owner took on 2026-11-20; none is open.

1. **Dispatch's conflict rules are DS1's and DS2's as written, with the
   replica's identity added** (O10). Scalars last writer wins per field
   by (`at`, actor, op id), sets observed-remove, logs append-only,
   allocation by blocks and ranges, gated statuses never checked again,
   kept duplicates left to a person; `Reconcile` repairs the parent and
   `blocks` graphs and a run's one ticket from the log alone. This item
   adds only what makes those rules the same on every machine: actors
   qualified by their row's origin, compared canonically. `at` stays the
   writer's clock rather than a hybrid logical clock now: the rules
   compare nothing but `at`, actor, op id and `n`, so a clock can replace
   it later without a rule changing (DS1 says so).
2. **Sharing is per project, chosen by a person, one way in v1.** A
   machine holds personal projects beside a team's; sharing all of
   Dispatch would send them all. Unsharing is out of scope: what left
   cannot be called back.
3. **What a provider is handed is what this item names: Dispatch,
   decisions, UI state, run pointers.** The catalog, crews and run
   records are handed by C6 as on `main` and this item adds no use of
   them: a crew's checks and setup are argv arrays that can carry a
   credential, and a run record holds attempts' and checks' details that
   come close to terminal output. A plan revision's crew leaves only as
   its view.
4. **A value kept back is kept back, never altered.** Altering a value on
   the way out would leave two replicas holding different text under one
   change's identity; keeping it back leaves the field's earlier version
   on the others until its next edit that is sent. A scalar's prior,
   which no merge reads, is judged apart and may be left out of a row
   (`PriorKept`), so the clean edit that replaces output-like text does
   not carry it.
5. **No attachments, launch packages or launches across machines in v1;
   an approval launches only where it was made.** A file cannot be
   checked for terminal output the way text can (a screenshot of a
   terminal is both), and a launch is execution's record on the server
   whose ledger has its run. Plan revisions replicate as their views, and
   a view is never launched; on the machine that holds a revision's
   body, an approval written by another machine does not launch it until
   this machine's operator approves it. This amends DS2's "a replica
   shows the launch as it was given".
6. **A held answer belongs to the one machine that runs the work.** Only
   the server whose ledger holds the decision records it, through DR1;
   `SyncDelivery` only reads; no route hands a decision, a held answer
   or a pointer to another machine to act on, and a restore carries no
   ledger.
7. **"Not known" waits, everywhere.** A server whose identity provider
   cannot say records no held answer and applies no `via: sync`
   response; neither refuses nor supersedes on it. Only a "no" refuses
   or supersedes.
8. **The server records only what the person confirmed.** The server
   computes the content's and the request's hashes itself from its own
   record and hands them to the identity provider beside the statement;
   it records nothing whose request hash is not its own record's, so a
   statement for one decision is never recorded against another, and the
   target's identifiers are in the request part because two whole, valid
   decisions can ask the same question.
9. **"Offline" is a server running off the network; a stopped server is
   continued by its operator.** A held answer handed to a server whose
   run is not running is `not_held`, nothing is recorded, and the
   operator's Continue moves the decision to the continuation, where the
   answer is then recorded once. Nothing here continues a run by itself.
10. **A restore brings the UI state and pins; Dispatch arrives by
    replication; no ledger travels.** C6's restore installs an archive
    into an empty directory, and the restoring provider may write
    another registered provider's directory (`ProviderDir`); the shared
    projects then arrive exactly as on a second machine, so one path is
    tested and no run can run twice.
11. **UI state is layouts, palette recents and pins; pins are shown, not
    taken.** A pin names a session of one server, which another server
    cannot open, so another machine shows it read only.
12. **Plan revisions travel as views.** The whole body stays on its
    machine; its digest still names the revision everywhere, and the
    view is what a person reads on another machine.
13. **A local write is the newest of its field.** A person who edits a
    field after seeing another machine's edit means theirs to win;
    stamping the row past every row of the field the store holds makes it
    win everywhere, without a hybrid clock.
14. **What a copy of a data directory inherits, it does not use.**
    Blocks, overflow ranges, a share in progress and the UI writer's id
    were handed to one replica; a store opened as another instance
    retires them before its first write and takes a provisional window
    until it joins.
15. **The registry's key is a shared project's key everywhere.** Rows
    keep the key they were written under (history is immutable); the key
    every replica shows comes from the workspace's registry, which never
    changes while the project is shared.
16. **Every row keeps its author, and a recovered row says who sent
    it.** A provider that rebuilds a project the workspace lost may send
    other members' rows; they are stored with their own origins and
    `recovered_by`, never as the sender's.
17. **Two tickets of one tracker item are kept, each naming the other.**
    A fold would rewrite immutable rows on every replica; a person keeps
    one and unlinks or closes the other.
18. **The output check fails closed.** Past its bound, missing,
    rebuilding or behind, its verdict is `pending`, never `clean`, and
    pending text is not handed out. The cost is that text written on a
    machine whose captured output passes the bound waits until it is
    reindexed.
19. **The remote kinds `device` and `hosts` and the status's own device
    are this item's.** HS2 builds its page on them and defines the
    `hosts` body.
20. **A decision whose path is uncertain is answered at the machine.**
    `LastDecision` lets a provider keep from answering away from the
    machine any decision opened before a moment it records (its first
    join, a rebuild); a held answer lost that way is an inconvenience,
    one applied twice is the defect the item exists to prevent.
21. **The store knows what replicates** (main has entities this item was
    first written without). `rules.go`'s `Replicates(entity, op,
    field)` is the table above: `DispatchChanges` hands out only those
    rows and `ApplyRemote` refuses a batch with any other, so reviews,
    findings, captures, their sends and publications, launches, a
    project's `cwd`, a ticket's snapshot and its attachments stay on
    their machine whatever a provider does. The alternative, leaving the
    filter to the provider alone, would let a defective provider apply a
    launch row or a path on another machine. This narrows two plans for
    CS1's first version, and amends them in so many words: reviews, their
    findings and marks (`docs/plans/review-repair.md`, Owner decisions 2,
    which put them in Dispatch so a workspace could share them) and
    publications (`docs/plans/tracker-linking.md`, whose field classes
    for a publication were written for replicas) do not replicate yet;
    their field classes stand for the later item that adds them. Nothing
    then stops one outcome being published twice from two machines that
    share a ticket: each machine's publications are its own, a person on
    the second machine sees no publication the first made on the
    ticket's page (only the comment itself, on the tracker item), and may
    publish the same outcome again. That is a limit of the first version
    (Out of scope).
22. **The store's instance is IN1a's `store_instance`.** A restore's
    `Reissue` already makes a restored store a new instance; a raw copy
    of a data directory is the same instance, and what a provider does
    about two processes of one machine is the provider's.
23. **A run a launch made is shown on other machines by its launch's
    ticket, and only for a shared project.** A launched run has no `run`
    link (DS2's Owner decisions, 10) and launches do not replicate, so
    the run pointer document names the ticket whose launch made the
    run's chain (`ticket`, from the ledger's `runs.ticket`), and the
    ticket page matches remote pointers by it as well as by run links.
    The field is present only while that ticket's project is shared
    here; for a run of a ticket in a project that is not shared it is
    absent, so a personal project's ticket ids are never handed out on a
    pointer. When a project's share state turns `shared`, or leaves it,
    the adapter marks pending the pointers of the runs of its tickets, so
    the field follows the share with no change to the run
    (`TestPointerNamesItsLaunchTicket`,
    `TestPointerOfAnUnsharedProjectNamesNoTicket`,
    `TestPointerTicketFollowsTheShare`).
24. **This public plan comes first.** The open-source rows are planned
    here and land as open-source PRs, this plan reviewed and merged
    before any of their code, so the repository's plan-before-code rule
    holds with nothing private in it.
25. **A comparison's selection is never answered by a held answer**
    (default; the same decision as the decision-policy plan's Owner
    decisions, 10). A selection is the operator's alone, answered only
    by a principal authenticated at this server itself (the workbench
    token, or an identity provider's principal with the admin grant); a
    held answer to one is refused `403 not_approver`, kept, with words
    that say so, whatever grant its subject holds
    (`TestHeldAnswerToASelectionIsRefused`). On a server whose router
    confirms no policies, every other record judges a held answer by its
    entries, `via: sync` taken as `via: identity`
    (`TestHeldAnswerAdmittedByTheEntries`); where it confirms policies,
    decision 26 holds. The other way: an admin
    subject's held answer counts as the operator's for a selection too;
    it changes the decision-policy plan's respond judgement and that
    test, and this plan's door, and no other row.
26. **A held answer and a router's policy do not combine in the first
    version** (the default; decided by the owner on 2026-11-18: it
    stands; the same decision as the decision-policy plan's Owner
    decisions, 11). On a server whose registered router confirms
    policies, this door refuses every held answer to a record under a
    router's policy, final, `403
    answered_at_server`, nothing written and the router not asked, and
    the engine refuses a `via: sync` response to a record under a
    router's policy (`TestHeldAnswerRefusedWhereTheRouterConfirmsPolicies`,
    `TestEngineRefusesAHeldAnswerUnderAPolicy`). What a person sees
    meanwhile: on such a server a decision is answered at the server's
    own routes by whoever its policy admits (the operator, or the people
    its router names on the decide page), and an answer given on another
    device is refused with words that say where to answer. The other
    way, a held answer judged under the router's policy, is a later
    design, which must first solve three things: a held answer is bound
    to the revision it was given for, and a policy written after it
    raises the revision; a held answer that waits for a silent router
    needs a bound that nothing else resets; and a response that carries
    no confirmation must be refused without closing the decide grant's
    own route. It changes the decision-policy plan's respond judgement
    and its tests, and this plan's door.
27. **What counts as terminal output: captured bytes only** (decided by
    the owner on 2026-11-20). What the terminal recorded stays on the
    machine: a session's output, a check's log, a history record. An
    agent's own text syncs unless it quotes that, and then the part is
    withheld (a Dispatch row) or cut (a decision's part) (What counts as
    terminal output, above).

## Acceptance

Observed row by row, each when its PR's checks pass in CI and locally:

- OS-1a: `TestReplicasConverge`, `TestLocalAndRemoteWritesConverge` and
  `TestChildBeforeParentHeldThenApplied` show two stores reaching the
  same tables from each other's rows in any order;
  `TestRemoteApprovalDoesNotLaunch` and `TestViewOnlyRevisionDoesNotLaunch`
  show no launch on another machine's approval.
- OS-1b: `TestShareReservesItsNumbers`, `TestCompleteShareRefusesRowsPastUpto`,
  `TestBlocksAndOverflowNeverWait` and
  `TestRekeyedProjectSharedIntoAReplicaHoldingItsOldKey` show the
  cut-over losing no row and no two tickets sharing a number;
  `TestHostDispatchDoorsOnlyForTheSyncStore` shows the doors refused to
  every other caller.
- OS-2: `ui-state.spec.ts` shows a layout, recents and a pin following
  the server, not the browser.
- OS-1c: `TestWinnowingCatchesEvery64ByteQuote` and
  `TestPendingVerdictOfAnEndedProcessWithheld` show no quote of captured
  output handed out and no verdict `clean` that could not be made.
- OS-3: `TestHeldAnswerRecordedOnceAndRepeat`, `TestViaSyncAppliedOnce`
  and `TestViaSyncUnknownWaitsAndNeverSupersedes` show a held answer
  recorded once and applied once, and "not known" deciding nothing.
- OS-4: `remote.spec.ts` and `sync-free.spec.ts` show another machine's
  records read only and the free build unchanged.

Done is every row merged with those checks green. Nothing needs the
owner but the merges; a live check with a real provider is the provider
module's.

## The amendments to the other plans

This plan's pull request changes five plans in these places, quoted as
they now read.

**`docs/plans/dispatch.md`**, "Ids and keys", the reserved columns:

> Two columns reserved for replication, `number_from` and `number_to` (0
> in this item: the whole range is this store's), let CS1 hand each
> replica a block of numbers so concurrent creation never collides, and
> the allocation rule under "Field rules for replication" says what a
> replica does when its block is out and the service unreachable: it
> creates, from a range of its own, and waits on nothing (Owner
> decisions, 7). CS1's open-source rows (`docs/plans/sync-seams.md`,
> OS-1a and OS-1b) add the columns beside them: the tag a block was
> handed to, the block asked ahead, the overflow range and a retired
> store's provisional window, each usable only by the instance and the
> active tag it was handed to.

The allocation row of "Field rules for replication", its last clause:

> ... until the person gives it a free key (`project.rekey`, an intent
> CS1 adds, allowed only while the project is neither shared nor being
> shared, its tickets' keys following since none has been seen
> elsewhere). Joining a team with a store that already holds tickets is
> CS1's share, an ordered cut-over (`docs/plans/sync-seams.md`, Events,
> Sharing a project): the project's next 1,000 numbers reserved, its
> history sent, the share completed only when no row is past what was
> sent, and block numbers taken once it is shared. A project's numbers
> before its share and every block are below 1,000,000, each replica's
> overflow range above it, and a retired store's provisional window
> above 2,000,000,000; a shared project's key on every replica is the
> workspace registry's, never projected from a row.

The paragraph on `at`, its last sentence:

> The schema reserves nothing more for it. A replica's identity is the
> origin its rows are written under: CS1's rows (`docs/plans/
> sync-seams.md`, OS-1a) add `changes.origin`, a tag a replica's provider
> hands it, set on every local row from the replica's first join on, and
> the rules compare canonical actors, `@<origin>/<actor>`, so every
> replica orders the same rows the same way; a local actor never begins
> with `@`.

**`docs/plans/dispatch-evidence.md`**, after "Field rules for
replication"'s table:

> A launch's start is recorded only by the server whose ledger has its
> run. Launches do not replicate (CS1, `docs/plans/sync-seams.md`): a
> replica shows the run a launch made by its pointer, matched to the
> ticket the pointer names. A revision replicates as its **view** (the
> body less the crew's paths, its setup, every argv and each agent's
> command), named by its own digest beside the whole body's, and a
> store that holds only the view does not launch it (`409
> body_elsewhere`); on the machine that holds the whole body, an
> approval last written by another machine does not launch it until this
> machine's operator approves it again (`409 approved_elsewhere`), so a
> revision is launched only where it was made, on that machine's own
> approval.

Out of scope, the replication bullet:

> - Replicating revisions as their views, and not launches (CS1,
>   `docs/plans/sync-seams.md`): the field rules are written here.

**`docs/plans/decisions.md`**, the CS1 bullet of the items it meets:

> - **CS1** (sync): decision records are not replicated. CS1's
>   open-source rows (`docs/plans/sync-seams.md`, OS-3) hand a
>   registered sync provider a bounded document of each decision, told
>   by the ledger's `OnDecisions` hook, and let it hand in a held answer
>   (`Host.RespondDecision`), recorded through this plan's respond path
>   with the authority `via: sync` and applied only after the identity
>   provider admits it again at application; another machine shows the
>   document read only, as a remote record.

The response's authority (The changes, the ledger):

> A response's `authority` is what the policy checks, kept apart from its
> `actor`, the audit's words: `grant` (`admin`, else `decide`, or none),
> `via` (`workbench`, `identity`, `router` or `sync`) and the identity's
> `subject`, all from the principal the request authenticated
> (`authorityOf` in `internal/api`, beside `actorOf`), never from a
> body; `boot` is the server process's id, minted at its start. A
> router's answer offered to the engine has `via: router` and no grant,
> so it can never pass for an admin's answer whose subject it shares. A
> `sync` response is a held answer a registered sync provider handed in
> (`docs/plans/sync-seams.md`, OS-3); its authority also keeps the
> membership, the sign-in or device it was made through, when it was
> made, the SHA-256 of the identity provider's signed statement of it
> (the statement itself kept beside the response, in the ledger's
> `decision_statements`, since it may pass `MaxAuthority`), the held
> answer's id and its fence, all from the door's checked record, never
> from a route's body.

The recheck (Applying), after "... so no such response waits for a
continuation.":

> A `sync` response is rechecked at application by the identity
> provider's `AdmitsAnswer` over the authority the ledger keeps and the
> statement kept beside it (a signed statement, not a token, so it is
> kept), not by
> `ConfirmAuthority`: yes applies it, in a transaction that checks `k`
> is still the decision's accepted response, and not its `boot`, so a
> restart asks again rather than superseding; no supersedes it as an
> identity's is; not known leaves the decision `answered` and asks again
> (1 s doubling to 60 s while the run is held), never applying and never
> superseding on what nobody said (`docs/plans/sync-seams.md`, OS-3).
> A held answer and a router's policy do not combine in the first
> version: on a server whose registered router confirms policies every
> held answer to a record under a router's policy is refused, final
> (nothing written; the provider does not hand it in again), `403
> answered_at_server`
> (`docs/plans/decision-policy.md`, Responding, Held answers).

Out of scope, the replication bullet:

> - Replicating decisions: none in v1. A registered sync provider is
>   handed a bounded document of each decision and may hand in a held
>   answer, recorded and rechecked here (CS1, `docs/plans/sync-seams.md`,
>   OS-3).

Owner decisions 3, its last clause, and 12, its last clause:

> ... never reused in a data directory; a decision is never replicated,
> and another machine shows a copy under the device that reported it
> (CS1).

> ... the `decision` link kind DS2 reserved stays reserved and refused
> locally; CS1 does not use it, since a copy names its ticket.

The history's cap paragraph, corrected to main's count
(`mandatoryHistory`):

> ... at most 32 of the operator's operations together (...); at most
> four of a Brakeman turn's routes, a group of their own
> (`MaxDecisionTurnOps`); at most 32 `responded`, ... That is at most 77
> rows (`mandatoryHistory`: 1 + 2 + 32 + 4 + 32 + 1 + 1 + 1 + 3; 93 with
> the decision-policy plan's 16 router changes); ...

**`docs/plans/review-repair.md`**, Owner decisions 2, after its last
sentence:

> CS1's first version does not replicate reviews, findings or marks
> (`docs/plans/sync-seams.md`, Owner decisions 21): they stay on the
> machine that made them until a later item adds them under the field
> rules above.

**`docs/plans/tracker-linking.md`**, the field rules' publication row,
its last cell, after "is never sent":

> CS1's first version does not replicate publications
> (`docs/plans/sync-seams.md`, Owner decisions 21): each machine's are
> its own until a later item adds them under these classes

and the sentence after the counterpart group's rules:

> "One ticket per linked issue" is a rule of the local write; two
> replicas that each import the same issue make two tickets, which CS1's
> plan shows, never folds: both are kept, each naming the other
> (`docs/plans/sync-seams.md`, One tracker item, two tickets).
