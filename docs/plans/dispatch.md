# Dispatch: projects, tickets and the views over them (Program item DS1)

Dispatch is Conductor's own work tracking: projects, tickets with stable
keys, their discussion and their history, kept in a SQLite file of their
own in the data directory and shown as list, board and roadmap views in
the workbench. This plan is Program item DS1 (the Program, section 4.3),
written against `main` at b6d017df (PR #381, 2026-10-28), after reading
the store, the crew engine, the ledger, the API, the palette and the pages
the item touches. It is reviewed before any of its code; the PRs at the
end are cut into issues after the review, one issue, one branch `issue-N`,
one PR each (AGENTS.md).

The names follow the railroad (the Program, section 1.2): Conductor is the
application, Yardmaster the agent harness that runs a crew's tasks,
Dispatch the ticketing and project tracking component, and its items are
tickets. "Workspace" is never used here for a worktree. Dispatch owns what
was asked and what became of it; Yardmaster's ledger stays the truth of
execution, and Dispatch links to it rather than copying it.

Later Program items this plan leaves room for, in plain words: DS2 links a
ticket to the runs, candidates, checks and findings that worked on it;
IN1a imports and links GitHub issues with the user's own token; DR1 and
HM1 add durable decision records and people as crew members; CS1
replicates a team's Dispatch between machines; BK1 lets Brakeman draft
tickets; C6 adds `conductor export`, a zip of the data directory.

## Articles it serves

1. No licence check: Dispatch has no plan, tier or provider check; every
   route is the admin's and works the same on every build.
2. The open-source build is fully useful on its own: a team can run its
   backlog in Dispatch with no account and no network.
3. Nothing is gated by a flag: there is no switch that turns a paid part
   on; there is no paid part.
4. Inspect and export stay free: every Dispatch read is a `GET` the
   workbench token answers; the store is two plain things in the data
   directory (one SQLite file, one directory of attachment files), so the
   export of item C6 carries it whole and a restore reads it back.
5. Dependencies point one way: `internal/dispatch` imports the standard
   library and `modernc.org/sqlite`; nothing private, nothing new.
6. No feature build tag: a plain `go build` carries Dispatch.
7. The free default of every interface is what main does today: Dispatch
   touches none of the eight interfaces; it offers `Store.OnChange`, the
   seam CS1's continuity store hooks into, and calls no provider.
8. Not touched: no hosted dependency exists in this item, so there is
   nothing to answer as the free default during an outage; and the
   allocation rule this item writes for CS1 (the field rules) keeps a
   create local, so no block, no registration and no service is ever a
   prerequisite of a ticket or a project, reachable or not.
9. `CGO_ENABLED=0` stays: the store is `modernc.org/sqlite`, SQLite
   translated to Go, as the ledger's is (checked on 2026-10-28: the pinned
   v1.61.0 reports SQLite 3.54.1 with FTS5 and JSON1 compiled in).
10. The repository contract holds: this plan before the code; race tests
    per package; vitest and Playwright for the UI; no new dependency, so
    `make notices` has nothing to regenerate; no protocol frame changes,
    so `internal/proto` and `protocol.ts` are untouched and `docs/
    protocol.md` gains the HTTP rows and one event row.
11. The railroad names keep their meaning: the component is Dispatch, its
    items are tickets, a run's tasks stay Yardmaster's. One word of the
    repo's collides: the engine calls its scheduling of attempts
    "dispatch" in text people read (the run log's "dispatch paused" and
    "dispatch resumed", the run page's "Dispatch paused" toast and "Pause
    the dispatch" button and palette row, the task words "the dispatch is
    paused", and the README's and `docs/protocol.md`'s lines for them),
    which the Program's baseline names as what this item starts from.
    PR-0 renames that verb to "scheduling" in every text a person reads
    and in those docs, before PR-8 puts Dispatch in the sidebar; Go
    identifiers, test names and the docs that describe the code keep the
    lower-case verb beside the capitalised component (Owner decisions,
    19).
12. Execution stays behind one seam: Dispatch starts no process, opens no
    session and runs no check; section "Boxcar constraints" says so item
    by item.

## Why and what happens

Today the repository tracks its work in GitHub issues, and a run's tasks
live only inside the managed run that made them: nothing in Conductor
holds a durable objective, its acceptance criteria, who is accountable for
it, the discussion around it and what became of it (the Program, section
1.4). Dispatch is that record. It is local first: one SQLite file the
server owns, no account, no network, written and read on the machine that
runs `conductor serve`, so an individual developer has it on day one and a
team shares it later through replication (Program item CS1; decision D3:
one local SQLite store, shared later by replication, never a hosted
Dispatch service).

After this item ships, a person sees:

- **Dispatch** in the sidebar's foot (`G D`; `Alt+D` from a terminal),
  listing the projects. A project has a short key (`CND`), a name, a
  description, a repository folder, a close policy and either a native
  tracker or a pointer at an external one (a GitHub repository's URL, say).
- A project's tickets as a **list** (fast, filtered and sorted on the
  server, pages of 200), a **board** (one column per status, cards moved by
  drag or by the keyboard) and a **roadmap** (Now, Next, Later), the filter
  in the URL, saved views as chips, and the keys Linear-style ticketing
  taught people: `j` and `k`, `Enter`, `c` for a new ticket, `s` `p` `a`
  `l` `m` `o` for status, priority, assignee, label, milestone and owner.
- A **ticket page** at `/dispatch/CND/12`: `CND-12` and its title, the
  description and the acceptance criteria as Markdown with a preview, the
  status, priority and Now/Next/Later horizon, the accountable owner (a
  person) apart from the assignees (people or agents), labels, milestone,
  parent and children, what it blocks and what blocks it, comments,
  attachments, links (a pull request, a release, a run), and its history
  as the store recorded it, field by field.
- The **close policy** on the page: "To close: the owner's acceptance ·
  a delivery link", each with its state and, unmet, why ("accepted for
  Alice; the owner is now Bob", "accepted before the criteria changed"),
  and a Close that is refused, in words, until the policy is met.
  Acceptance is its own fact, recorded by the operator for the owner and
  against the criteria as they stood, never implied by a status. A merge
  is a delivery link in this item (the merged pull request's or the
  release's URL); reading a link's merged state from the tracker comes
  with IN1a.
- The **run Tasks view** inside Dispatch: a ticket linked to a managed run
  shows that run's tasks on its page, with the same rows the run page
  has, and the run page says which ticket it belongs to. Launching a run
  from a ticket, with its snapshot in the attempt's task file, is Program
  item DS2; here the link is made by hand or by the CLI.
- Tickets in the **command palette**: a key or a title typed finds the
  ticket, `>` offers "New ticket in CND", and the Dispatch pages have
  their own group of rows.
- `conductor dispatch` for the shell and two MCP tools for an attempt
  whose run is linked to a ticket: read the ticket, comment on it.

Every change is a typed operation with an id the client mints and a
receipt the store keeps (the Program asks it of every write), the way a
managed run's commands already work, so a reply lost to the network is
asked for again without a second change, and the same code serves the
UI, the CLI and the MCP tools.

## The changes

### `internal/dispatch` (new): the store

One package, no HTTP, no sessions: the API, the CLI and the tests are its
callers. It is the second importer of `modernc.org/sqlite` after
`internal/crew/ledger` (`docs/dependencies.md` says so; the ledger is not
touched). Files: `dispatch.go` (open, pragmas, the schema check, close,
the WAL checkpoint, `OnChange`), `schema.go` (version 1 and the migration
table), `ids.go`, `model.go` (the row types and every bound), `ops.go`
(the typed operation, its receipt and the changes log), `rules.go` (the
field classes and the merge rules, pure), `projects.go`, `tickets.go`,
`comments.go`, `labels.go`, `milestones.go`, `people.go`, `relations.go`,
`links.go`, `views.go`, `query.go` (the list, its filters, its cursor,
full-text search), `attachments.go` (the files), `sweep.go` (orphans and
old receipts); a test beside each.

**Where.** `<dataDir>/dispatch/dispatch.sqlite`, mode 0600, and
`<dataDir>/dispatch/attachments/<sha256>` (0600, content addressed, the
first two hex characters as a sub-directory; `attachments/tmp/` holds the
uploads in flight). The data directory is
refused to Conductor's own file routes and to every session's file reads
(`config.FileDenyFunc`), so no viewer reaches the store through a Files
pane; an agent's own process runs as the server's OS user and can open
the file itself, as it can the ledger's, and nothing in this item keeps
it from it. The trust model is the API's (an attempt's tools read and
comment, nothing more; Owner decisions, 13) until a guest of item F
isolates the process.

**Open.** `dispatch.Open(dir)` makes `dir` (0700) when missing, refuses a
database file that is a symbolic link, opens with the ledger's pragmas
(`busy_timeout(5000)`, `foreign_keys(1)`, `synchronous(FULL)`,
`journal_mode(WAL)`, on darwin `fullfsync(1)` and
`checkpoint_fullfsync(1)`, `_txlock=immediate`, `_time_format=sqlite`),
one connection (`SetMaxOpenConns(1)`), runs `PRAGMA quick_check`, and
brings the schema to `Version` (1 here) or opens read-only: a database a
newer Conductor wrote, or one that fails its check, is read when it can
be and never written, and `ReadOnly()` says why. The version is read
inside the `BEGIN IMMEDIATE` transaction that would create or migrate
the schema, never before it: the ledger's `prepare` (`ledger.go`) reads
it first and begins after, which its owner lock makes safe, and Dispatch
has no such lock, so two openers on one directory (two servers, or two
stores in a test) are ordered by the write lock instead: the second
waits on the busy timeout for the first's commit, then reads the
version it wrote and finds nothing to do; one still refused busy past
the timeout (`busyTimeout`, 5 s, a field a test sets) opens read-only
with "another writer holds the database" as why, since a write lock
held that long is not an opener's (the schema is a few dozen
statements). `Open` fails only when
the directory cannot be made or the file is a link. There is no owner
lock (see Owner decisions, 2): SQLite's own locking orders writers across
processes, every revision is read and bumped inside the transaction
that uses it, and the schema is made inside the transaction that read
its version. `Close` checkpoints the WAL (`wal_checkpoint(TRUNCATE)`)
and closes; `Checkpoint(ctx)` does the first alone, for C6's export of a
running server. `OnChange(func([]Change))` is called once after every
commit with what it changed, one change per entity (a relation counts
for both its tickets, a label's delete for the label and every ticket it
left), each with the commit's last `seq`, the entity, its id, and the
ticket and project it belongs to, every listener a copy of its own; the
API turns a commit's changes into the SSE events below, together (Owner
decisions, 22), CS1's continuity store will hook it too.

**Ids and keys.** Every row but a person's has an opaque id of 16
lower-case hex characters from 8 random bytes (`crypto/rand`, the shape
of `session.NewID`, without importing `internal/session`); a person is
its name (the table says why). A project also has
a key, `^[A-Z][A-Z0-9]{1,5}$`, unique, chosen at creation and never
changed; a ticket has a number, allocated from `projects.next_number` in
the transaction that creates it, and its key is `<project key>-<number>`
(`CND-12`), unique, never reassigned, kept by a soft delete. Two columns
reserved for replication, `number_from` and `number_to` (0 in this item:
the whole range is this store's), let CS1 hand each replica a block of
numbers so concurrent creation never collides, and the allocation rule
under "Field rules for replication" says what a replica does when its
block is out and the service unreachable: it creates, from a range of
its own, and waits on nothing (Owner decisions, 7). CS1's open-source
rows (`docs/plans/sync-seams.md`, OS-1a and OS-1b) add the columns
beside them: the tag a block was handed to, the block asked ahead, the
overflow range and a retired store's provisional window, each usable
only by the instance and the active tag it was handed to.

**Schema, version 1** (`schema.go`; times are RFC 3339 text in UTC with
nanoseconds; JSON is text). `rev` is on every row with a scalar field
that can change (`projects`, `tickets`, `labels`, `milestones`,
`comments`, `views`), bumped by every operation that writes a `changes`
row for the row; the rows of a set (`ticket_assignees`, `ticket_labels`,
`relations`, `links`, `attachments`) are added and removed, never edited,
and carry none: their ticket's `rev` is bumped instead and is what an
`expectedRev` compares with. `deleted_at` is on every row with an id of
its own; "live" below means `deleted_at = ''`:

| Table | Columns (bounds in `model.go`) |
|---|---|
| `schema_version` | `version` |
| `projects` | `id` PK, `key` UNIQUE, `name` (1 to 60 characters, no control characters), `description` (Markdown, ≤ 16 KiB), `cwd` (≤ 4,096 bytes, a path kept as text, resolved only when something launches from it, which is DS2's), `tracker` (`native` or `external`), `tracker_url` (≤ 2 KiB, an `http` or `https` URL, with `external` only), `close_requires` (JSON array of `acceptance` and `delivery`; `verified` is refused until DS2 defines it), `next_number`, `number_from`, `number_to`, `archived_at`, `created_at`, `updated_at`, `rev` |
| `people` | `name` PK (1 to 60 characters), `created_at`: the directory the owner and person pickers offer; a name typed that is not there is added (decision D4: people are local labels until identity exists); the name is the person's identity, carried by `owner` and by `ticket_assignees.actor`, and nothing carries a person's id, so two replicas adding one name add one row |
| `tickets` | `id` PK, `project_id` FK, `number`, `key` UNIQUE, `title` (1 to 200 characters), `description` (Markdown, ≤ 64 KiB), `acceptance` (Markdown, ≤ 16 KiB), `status`, `priority` (0 to 4), `horizon` (`''`, `now`, `next`, `later`), `owner` (a person's name or `''`), `parent_id` (a ticket of the same project or `''`, live when set; inactive while it is a tombstone, see "Deleting a ticket"), `milestone_id` (a milestone of the same project or `''`, live when set; one a merge left a tombstone reads as none), `accepted_at`, `accepted_by`, `accepted_for` (the owner the acceptance was recorded for), `accepted_criteria` (the SHA-256 of `acceptance` then), `accepted_note` (≤ 1,000 characters), `source` (`native`; IN1a adds its own), `external_id`, `external_url`, `external_snapshot`, `authority` (the last four `''` here, reserved for IN1a so it adds no column), `created_at`, `created_by`, `updated_at`, `closed_at`, `deleted_at`, `rev`; UNIQUE(`project_id`, `number`) |
| `ticket_assignees` | `ticket_id`, `actor` (`person:<name>` or `agent:<catalog id>`), `added_at`; PK of the two; at most 8 per ticket |
| `labels` | `id` PK, `project_id`, `name` (1 to 40 characters), `color` (one of the twelve names the UI maps to the brand's status scales, never a hex value from the client), `created_at`, `deleted_at`, `rev`; a live label's name is unique in its project on a local write (`invalid`; a rule in the operation, not an index, since a merge may bring a second: see the field rules); at most 100 live per project |
| `ticket_labels` | `ticket_id`, `label_id` (a live label of the ticket's project, checked in the operation); PK of the two; at most 20 per ticket |
| `milestones` | `id` PK, `project_id`, `name` (1 to 60), `description` (≤ 4 KiB), `target` (`YYYY-MM-DD` or `''`), `state` (`open` or `closed`), `created_at`, `updated_at`, `deleted_at`, `rev`; at most 100 live per project |
| `relations` | `id` PK, `from_id`, `to_id`, `kind` (`blocks` or `relates`), `created_at`, `created_by`, `deleted_at`; one live row per (`from_id`, `to_id`, `kind`) on a local write (`invalid`; a rule, not an index: a merge may bring a second, read as one member, the later add's row standing for it (the field rules); one added again after a remove is a new row); both tickets live and of one project; at most 100 live per ticket; "depends on" is `blocks` read from the other end |
| `comments` | `id` PK, `ticket_id`, `author` (an actor as above, with `by`, the display name), `body` (Markdown, ≤ 16 KiB), `created_at`, `edited_at`, `deleted_at`, `rev`; at most 2,000 per ticket. A comment's number on the page, `n`, is its 1-based place in the ticket's comments ordered by (`created_at`, `author`, `id`), derived at read and never stored, so a replicated comment takes its place without renumbering a row; a comment is addressed by its `id` |
| `attachments` | `id` PK, `ticket_id`, `name` (1 to 255 bytes, no `/`, `\`, NUL or control character, not `.` or `..`), `size`, `sha256`, `content_type` (as given, ≤ 100 bytes; served only when on the allow list below), `created_at`, `created_by`, `deleted_at`; at most 50 per ticket, 16 MiB each |
| `uploads` | `id` PK, `sha256`, `created_at`: a hash held from before its file is published until the row that names it is committed (see Attachments), dropped in that commit or in the refusal's; one older than `sweepGrace` is a crashed upload's and the sweep drops it; the row is the upload's lease: the commit that names its hash deletes it and is refused `expired` when it finds none |
| `links` | `id` PK, `ticket_id`, `kind` (`delivery`, `run`, `external`; DS2 adds `plan`, `candidate`, `check`, `finding`, DR1 `decision`), `ref` (≤ 2 KiB: a run id, a PR number, else the `url` itself when none is given, so every link has an identity), `url` (≤ 2 KiB, `http` or `https`), `note` (≤ 1,000), `created_at`, `created_by`, `deleted_at`; one live row per (`ticket_id`, `kind`, `ref`) on a local write (a second is `invalid`; a rule, not an index: a merge may bring a second, read as one member, the later add's row standing for it (the field rules); one added again after a remove is a new row); a `run` link's `ref` names a run no other ticket's live `run` link names, on a local write (`run_linked`) and after a merge by `Reconcile`, so a run belongs to one ticket at most; at most 50 live per ticket |
| `views` | `id` PK, `project_id` (`''` for every project), `name` (1 to 60), `kind` (`list`, `board`, `roadmap`), `query` (JSON, ≤ 8 KiB, the filter, sort and grouping of `query.go`), `created_at`, `updated_at`, `deleted_at`, `rev`; at most 200 live |
| `changes` | `seq` INTEGER PRIMARY KEY AUTOINCREMENT (local to this database: the SSE event's and the history's paging cursor, never replicated), `entity` (`project`, `ticket`, `comment`, `label`, `milestone`, `view`, `person`; a set member, an assignee, label, relation, link or attachment, is a row of its ticket's `field`), `entity_id`, `ticket_id` and `project_id` (the ticket and project the row belongs to, `''` where there is none), `field`, `op` (`set`, `add`, `remove`, `create`, `delete`, `reconcile`, `migrate`), `value` (a string field's text as it is, a structured field's JSON; ≤ 64 KiB for every row: a text value meets it by its field's bound, a structured one by its shape, the largest a link's `add`, `{id, kind, ref, url, note, createdAt, createdBy}`, under 16 KiB with every field at its bound and every byte at its escaped worst (a `\` or `"` doubled; U+2028 and U+2029 six bytes each, which `encoding/json` escapes whatever `SetEscapeHTML` says), which `model_test.go` builds and measures as the ticket reply's test does; an attachment's add (its name and type at most doubled), a relation's, a member's actor or label id and a view's `query` (≤ 8 KiB as stored) are smaller), `prior` (the same shape: the value before a `set`; for a `remove`, the tags of the adds it removes), `at`, `actor`, `by`, `op_id`, `n` (the row's place in its operation, from 0); UNIQUE(`actor`, `op_id`, `n`), the row's identity across replicas: the operation log, the ticket's history and what CS1 replicates, in the one order (`at`, `actor`, `op_id`, `n`) |
| `ops` | `actor`, `id`, `intent`, `fingerprint`, `status` (`done` or `failed`), `receipt` (JSON, ≤ 4 KiB: it carries no entity), `created_at`; PK(`actor`, `id`); the newest 20,000 kept |
| `tickets_fts` | FTS5 entries, maintained by the store in the same transaction as the rows they index: one of each ticket's own, over its `key`, `title`, `description` and `acceptance`, rewritten by its create and by an edit of those fields, and one of each live comment, over its `body`, written by the comment's add, rewritten by its edit and removed by its delete, so a comment's write re-indexes that comment alone (Owner decisions, 20); an entry's rowid holds its ticket's rowid and its comment's (none for the ticket's own), so a search reads an entry's ticket off its rowid; a deleted ticket keeps its entries; the `unicode61` tokenizer with `remove_diacritics 2` |

Indexes: `tickets(project_id, status, updated_at)`, `tickets(project_id,
priority)`, `tickets(milestone_id)`, `tickets(parent_id)`,
`tickets(owner)`, `tickets(deleted_at)`, `ticket_assignees(actor)`,
`relations(to_id)`, `comments(ticket_id, created_at)`, `changes(entity,
entity_id, seq)`, `changes(ticket_id, seq)`, `links(kind, ref)`,
`attachments(sha256)`, `uploads(sha256)`. The unique indexes are those
of the keys never minted twice and nothing else: `projects.key`,
`tickets.key` and (`project_id`, `number`), `people.name`, `changes`
(`actor`, `op_id`, `n`) and `ops` (`actor`, `id`); every other
uniqueness in the table is a rule checked on a local write inside the
operation's transaction, so no index refuses a replicated add (see the
field rules), and a test reads the schema back and asserts the list.

Vocabularies: `status` is `backlog`, `ready`, `in_progress`, `in_review`,
`done`, `cancelled` (six, fixed in this item; Owner decisions, 4);
`priority` is `none` 0, `low` 1, `medium` 2, `high` 3, `urgent` 4;
`horizon` is empty, `now`, `next`, `later`. The Markdown fields (a
ticket's `description` and `acceptance`, a comment's `body`, a project's
and a milestone's `description`, a link's and an acceptance's `note`)
allow `\n` and `\t` and refuse every other control character; names and
titles refuse them all.

**Operations** (`ops.go`). Every write is `Apply(ctx, op Op, intent
string, args any, change func(*tx) error)`: `Op{Actor, ID,
ExpectedRev}`, the id `^[A-Za-z0-9._~-]{1,32}$` as the ledger's command
ids, the fingerprint the SHA-256 of the intent and the arguments as JSON.
One transaction: the receipt is looked up (the same id and content again
answers it with `repeat: true`; other content is `op_conflict`); the
entity's `rev` is compared with `ExpectedRev` when one is given
(`stale_revision`, nothing changed); the change is made; one `changes` row
per field it set, added or removed; `rev` is bumped; the receipt is
written; the commit; then `OnChange`. A refusal (the store's rules:
`not_found`, `invalid`, `stale_revision`, `close_policy`, `cycle`,
`bound`, `archived`, `run_linked`, `expired`) is a failed receipt written in a transaction with
nothing else, so the same id answers it again, as a refused task command
is kept; a failure of the database itself, or a read-only store, writes
nothing, and `op_conflict` is answered without a row. One transaction
suffices where the ledger's commands take two: Dispatch has no side
effect outside the database but an attachment's file, which is written
before its row (see below). The receipt is `{opId, intent, status, rev,
id?, key?, repeat?, error?, refusal?, missing?}`, at most 4 KiB: `id` and
`key` name what a create made, `missing` what a `close_policy` refusal
lacks, and the entity itself is read after the commit for the reply,
never kept in the receipt, so no receipt grows with a description. The
refusals: `not_found`, `invalid`, `stale_revision`, `op_conflict`,
`close_policy`, `cycle`, `bound`, `read_only`, `archived`, `run_linked`,
`expired` (an attachment's upload outlived its lease: see Attachments).
The intents: `project.create`, `project.edit`,
`project.archive`, `project.unarchive`; `ticket.create`, `ticket.edit`
(any of title, description, acceptance, status, priority, horizon, owner,
parent, milestone, and `add`/`remove` of assignees and labels, in one
operation), `ticket.delete`, `ticket.undelete`, `ticket.accept`,
`ticket.unaccept`; `comment.add`, `comment.edit`, `comment.delete`;
`label.create`, `label.edit`, `label.delete`; `milestone.create`,
`milestone.edit`, `milestone.delete`; `person.add`; `relation.add`,
`relation.remove`; `link.add`, `link.remove`; `attachment.add`,
`attachment.remove`; `view.create`, `view.edit`, `view.delete`.

**Rules** (`rules.go`): the field classes of the table in "Events,
orderings and invariants" as code, `Class(entity, field)`,
`Merge(local, remote Change) Winner` and `Reconcile(changes
[]EdgeChange) []EdgeChange` (a serial replay of one invariant's edge
changes in the one order, naming the adds it refuses, so a merged parent
or `blocks` graph keeps its invariants and a run stays linked to one
ticket, the same whatever batches the changes arrived in), pure and
tested here, used by CS1 later; the schema comment names the table.

**Queries** (`query.go`): `List(ctx, Query) (Page, error)` with `Query{
Project, Status[], Priority[], Labels[], Owner, Assignees[], Milestone,
Horizon[], Parent, Blocked bool, Q string, Sort (updated, created,
priority, status, title, key, milestone), Desc bool, Limit (1 to 200),
Cursor}`: one SQL statement from the filter, the FTS5 matches joined
when `Q` is set (≤ 200 characters: the text is cut into terms at
whitespace; a term with no letter or digit in it is dropped, since
`unicode61` makes no token of punctuation alone and `*` or `"` by itself
would match nothing; every term kept is passed as a quoted string with
its `"` doubled, the last one also as a prefix (`"term"*`), so `CND-12`,
`OR`, `NOT`, `a:b` and `(x)` are words to match, never syntax, and a
query whose terms are all dropped is an empty page, what FTS5 answers a
phrase of no tokens; each term is matched on its own against every
entry, a ticket's own and its comments', and the tickets the terms match
are intersected, so a ticket is found when every term is in one of its
entries, not necessarily the same one: Owner decisions, 20), the cursor
the last row's sort key and id, encoded opaque; the page carries
summaries without the description.
`Ticket(ctx, id)` reads one with its assignees, labels, relations both
ways, the first 50 children by number with their count, links,
attachments and the last 50 comments, in one read transaction (see the
store's invariants); `History(ctx, id, after, limit ≤ 500)` pages the
changes; `Children` (paged as `List` with `Parent` is), `Blockers`. A
benchmark,
`BenchmarkList`, over 10,000 tickets in three projects: the targets to
measure in PR-4 and name in its body are a filtered, sorted page under
20 ms and a full-text page under 50 ms on the Linux box; a target missed
is tuned in that PR (an index, the statement), not waived, save a search
of several very common words, whose cost the owner accepted (Owner
decisions, 21).

**Attachments** (`attachments.go`): `Add(ctx, op, ticket, name,
contentType, r io.Reader) (Receipt, Attachment, error)` reads at most
16 MiB + 1 (`bound` past it), into a temp file in `attachments/tmp/`
while hashing, fsyncs, and holds the hash before it publishes the file:
one transaction (`BEGIN IMMEDIATE`) inserts an `uploads` row naming the
hash and commits; then `publish(tmp, dst)` renames the temp file to its
hash (a file already there is the same content; the temp file goes; no
file's modification time is consulted by anything) and makes the new
name durable before anything can name it, since a file's fsync does not
carry its directory entry (fsync(2)): one `publish` per platform,
`attachments_unix.go` making the shard directory (`attachments/<xx>/`)
when it is missing, renaming into it, then fsyncing the shard directory
and then `attachments/`, the parent on every upload and not only on the
one that made the shard, as SQLite syncs a directory after making a
journal in it: a shard found already there may be one an upload made
and then failed or stopped at its parent's sync, in this server or in
another on the same directory, and nothing on disk tells a durable
shard from one the page cache alone holds, so every upload makes the
shard's own entry durable itself before its row can name a file inside
it (a sync of a directory nothing changed since its last costs little);
`attachments_windows.go` moving with `MoveFileEx` and
`MOVEFILE_WRITE_THROUGH` (`golang.org/x/sys/windows`, a package of a
module `internal/proc` already imports, so `make notices` is
unchanged), which returns once the move is on the disk, since a
directory handle cannot be flushed there; the file's own `Sync` is
`F_FULLFSYNC` on darwin by Go's doing. The syncs go through `syncDir
func(path string) error`, a field of the store a test replaces; a
`publish` that fails, at the rename or at a sync, is returned from `Add`
as an error before the operation runs, as a failure of the database
itself is (the route answers `500 store_failed`, as the crews routes
answer one), so no attachment row and no receipt names the file: the
lease is deleted in a transaction with nothing else when the database
answers, else left to expire, and the file, published or not, is the
sweep's; then the operation that
writes the attachment row runs, and its transaction deletes the
`uploads` row first and requires that delete to have found the row: the
row is the upload's lease, and a delete that finds none means the sweep
dropped it as older than `sweepGrace`, which an upload suspended between
its two transactions (a stopped process, a machine asleep) looks like,
since age cannot tell the two apart, and the file may have gone with it,
so the operation is refused `expired` (a failed receipt; nothing names
the file; the route answers `409 expired` and the page offers the upload
again), and a rename that finds no temp file (the sweep took it, older
than the grace) is the same refusal before any row is written; a
refusal's transaction deletes the lease too; `beforeCommit`, a seam of
the store a test sets, runs between `publish` and the operation, and
`beforePublish` between the lease's commit and `publish`. So from
before a hash-named file exists until the row that
names it is committed, an `uploads` row names its hash, and a file named
by neither is an orphan: a crash between the rename and the commit (its
`uploads` row then expires), or a tombstoned attachment's content. Only
the sweep deletes a file: `sweep(ctx, now)` (`sweep.go`), run by `Open`
before any route is served and called by a test, is two steps, each a
function a test can call alone. `expireLeases(ctx, now)` is one `BEGIN
IMMEDIATE` transaction that deletes the `uploads` rows older than
`sweepGrace` (an hour; a field a test sets) and commits; only once that
commit has returned, on disk (`synchronous(FULL)`), does
`collectOrphans(ctx, now)` run: it lists the directory, then in a second
`BEGIN IMMEDIATE` transaction it holds while it works reads the hashes
the live `attachments` rows and the `uploads` rows name, deletes every
listed hash-named file that neither names and every temp file older
than the grace, and ends. The leases go before any file does, and in a
transaction of their own, because a deletion the database undoes cannot
bring a file back: a sweep that expired the leases and unlinked the
files in one transaction and then failed or died before its commit
would leave the leases restored by the rollback and the files gone, and
an upload paused at `beforeCommit` through it would resume, find its
lease, and commit a row naming content that is not there. With the
expiry committed first that upload finds no lease and is refused
`expired` whatever becomes of the sweep after it; the collection's
transaction writes nothing, so nothing of it is undone, and the files
it unlinked were named by no row while it held the lock (an unlink a
power loss takes back leaves an orphan again, the next sweep's). Each
unlink goes through `unlink func(path string) error`, a field of the
store a test replaces; a file the sweep cannot delete is left to the
next sweep, the sweep going on with the rest and returning the first
error at its end, logged at open, the store serving regardless, since
an orphan left is harmless and a lease it failed to expire is still a
lease, its file left too. The write lock is what orders publication,
reference and collection across processes: an `Add`, in this server or
a second one on the same directory, publishes a file only after its
`uploads` row is committed, and no commit falls inside the collection's
transaction, so a collection whose transaction began before that commit
finishes its deletes before the file is published (a reused hash it
deleted is published afresh by the rename), and one that begins after
it sees the row and leaves the file, whatever the file's age and
whether the content is new or a reused hash whose last attachment was
removed an hour ago (a lease committed between the two steps is one the
second reads); and an `Add` whose lease a sweep dropped before its row
was written is refused rather than trusted, so no row names a file a
sweep may have deleted. `Remove` tombstones the row and
deletes no file, since an `Add` of the same content may be between its
rename and its commit. `Open(ctx, id)` gives the row and an `*os.File`
of the content, opened by its hash, never by a client's path.

### `internal/api`: the routes and the event

`server.go`: a `dispatch *dispatch.Store` field, opened in `New` beside
the ledger (`openDispatch(st, log)`: logged and left nil when the
directory cannot be made; read-only logged with why), closed in `Shutdown`
and `WaitBackground` after the ledger; `s.dispatch.OnChange` wired to the
event hub. Routes, in `dispatch.go` and `dispatch_attachments.go`, every
one `s.launching(...)` (the workbench token; `403 switchyard` on a
switchyard, which keeps no tickets): without a data directory `503
store_unavailable`; a store opened read-only answers every write `503
dispatch_unavailable` with why and every read as it can. Bodies decode as
the other routes do (`DisallowUnknownFields`), 64 KiB but where the table
says more; every write takes `{opId, expectedRev?, byName?}` beside its fields and
answers `200 {receipt, <entity>}`, `201` on a create; a refused operation
is `409 {error, receipt}` with the refusal as the code, as the task
commands answer; `400 invalid_request` for a body the store would not
even look at (a bad op id, a bad key); `413` for a body past its bound.
The ticket routes take 192 KiB, which holds every field of their body at
its bound with each character doubled by JSON escaping: the Markdown
fields allow only `\n` and `\t` of the control characters, so a
browser's `JSON.stringify` at most doubles them, and the CLI encodes
with HTML escaping off (`<` stays one byte); a field over its own bound
after decoding is the store's `bound`. Replies are encoded with HTML
escaping off too (`SetEscapeHTML(false)`, as `internal/store` and the
task file write; `writeJSON`'s default encoder makes six bytes of `<`,
`>` and `&`, which would put fifty comments of `<` at 4.9 MB), so every
text field encodes to at most twice its bytes, and the full ticket read
is bounded by its shape: the last 50 comments, the first 50 children
with their count, the sets at their bounds. The largest reply the server
can make, every field and set at its bound with each text doubled (fifty
comments at 32 KiB, the description at 128 KiB, fifty links, fifty
attachments, a hundred relations and fifty children with their titles),
is under 3 MiB, `maxTicketReply`, a constant `dispatch_test.go` proves
by building that ticket and measuring the reply, and that the CLI's and
the tools' limits are checked against (PR-12).

| Route | Body and reply |
|---|---|
| `GET /api/dispatch/projects?archived=1&limit=&cursor=` | `{projects, next?}`: pages of at most 200 summaries by key (`limit` 1 to 200, default 200; the cursor opaque, as the tickets'), each `{key, name, tracker, archivedAt, open, done, rev, updatedAt}` with its `open` and `done` counts and without `description`, `cwd`, `trackerUrl` and `closeRequires`, which the project read carries, so a page is under 128 KiB whatever the descriptions hold (a name of 60 characters encodes to at most 360 bytes) |
| `POST /api/dispatch/projects` | `{opId, key, name, description?, cwd?, tracker?, trackerUrl?, closeRequires?}` → `201 {receipt, project}`; `409 key_taken` |
| `GET /api/dispatch/projects/{key}` | `{project, labels, milestones, views}`, a view's `query` as the JSON it is, not a string; the largest reply (the project's fields at their bounds, 100 labels, 100 milestones with 4 KiB descriptions, 200 views with 8 KiB queries, every text at its escaped worst) is under `maxProjectReply`, 3 MiB, which `dispatch_test.go` proves as it proves `maxTicketReply` |
| `POST /api/dispatch/projects/{key}/edit`, `…/archive`, `…/unarchive` | `{opId, expectedRev?, …}` → `{receipt, project}` |
| `POST /api/dispatch/projects/{key}/labels`, `…/labels/{id}/edit`, `…/labels/{id}/delete` | `{opId, name, color}`; a deleted label leaves every ticket (its membership rows removed, one `changes` row per ticket), its row a tombstone, its name free |
| `POST /api/dispatch/projects/{key}/milestones`, `…/milestones/{id}/edit`, `…/milestones/{id}/delete` | `{opId, name, description?, target?, state?}`; a deleted milestone leaves every ticket (`milestone_id` cleared, one `changes` row per ticket), its row a tombstone |
| `GET /api/dispatch/people`, `POST /api/dispatch/people` | `{people}`; `{opId, name}` |
| `GET /api/dispatch/tickets?project=&status=&priority=&label=&owner=&assignee=&milestone=&horizon=&parent=&blocked=&q=&sort=&desc=&limit=&cursor=` | `{tickets, next?, total}` (repeatable parameters as `status=a&status=b`; `limit` 1 to 200, default 100; `400 invalid_request` for a bad value) |
| `POST /api/dispatch/tickets` | `{opId, project, title, description?, acceptance?, status?, priority?, horizon?, owner?, assignees?, labels?, milestone?, parent?}` (192 KiB) → `201 {receipt, ticket}` |
| `GET /api/dispatch/tickets/{key}` | `{ticket}` in full, from one read transaction: assignees, labels, `parent` (its key, title and status, and `deletedAt` when it is a tombstone), relations both ways (each with its other end's `deletedAt`), `children` (the first 50 live by number and `total`; the rest through the list's `parent=`), links, attachments, the last 50 comments (each with its derived `n`), the close policy's state (`close: {requires, met: {acceptance, delivery}, missing, why}`, `why` the words for an acceptance recorded but unmet) |
| `POST /api/dispatch/tickets/{key}/edit` | `{opId, expectedRev?, set?: {…}, add?: {assignees?, labels?}, remove?: {…}}` (192 KiB) → `{receipt, ticket}`; a `set.status` of `done` refused by the policy is `409 close_policy` with `missing` in the receipt; a `set.parent` that would cycle or pass depth 8 `409 cycle` |
| `POST …/{key}/accept`, `…/unaccept` | `{opId, note?}`: records `accepted_at`, `accepted_by` (the admin's display name, `by`), `accepted_for` (the ticket's owner now; `409 invalid` when it has none), `accepted_criteria` (the hash of its criteria now) and the note; `unaccept` clears them |
| `POST …/{key}/delete`, `…/undelete` | a tombstone and nothing else written: its children, its relations and its run links stay, inactive, and come back with the undelete (see "Deleting a ticket"); a deleted ticket is listed only with `?deleted=1`, its key kept, its page read and every other write on it `404 not_found` |
| `GET …/{key}/history?after=&limit=` | `{changes, next?}` |
| `GET …/{key}/comments?after=&limit=` | `{comments}` beyond the page the ticket read carries |
| `POST …/{key}/comments`, `…/comments/{id}/edit`, `…/comments/{id}/delete` | `{opId, body}`; the author is the admin's display name, recorded as `actor: admin`, `by: <name>` |
| `POST …/{key}/relations`, `…/relations/{id}/delete` | `{opId, kind, to}` (`to` a key of the same project); a `blocks` that would cycle `409 cycle` |
| `POST …/{key}/links`, `…/links/{id}/delete` | `{opId, kind, ref?, url?, note?}`: `delivery` needs `url`; `run` needs `ref`, a run the engine holds or has a record of (`404 not_found` otherwise) and linked to no other ticket (`409 run_linked`, the receipt naming that ticket); `external` needs `url`; a `delivery` or `external` link without `ref` takes its URL as one; a second live link with the same identity `409 invalid` |
| `GET /api/dispatch/links?run={id}` | `{ticket}` (`null` when none): the ticket a run is linked to, for the run page's chip: the live ticket whose live `run` link names the run when there is one (one at most, by `run_linked` and `Reconcile`); else, when only deleted tickets' live `run` links name it, the one whose link is latest by its add's log order (`at`, `actor`, `op_id`, `n`), answered with `deletedAt` set, the chip saying so; a live ticket always wins over a tombstone, so a run linked again after its ticket's delete answers the new ticket |
| `POST …/{key}/attachments` | `multipart/form-data`, one part `file` (its filename the name, its type the content type; 16 MiB plus 64 KiB of envelope, `413 bound` past it) → `201 {receipt, attachment}`; `409 expired` when the upload's lease was swept before its row was written (the server suspended past `sweepGrace` between the two), the page offering the upload again |
| `GET /api/dispatch/attachments/{id}` | the content, `Content-Disposition: attachment; filename*=…`, `X-Content-Type-Options: nosniff`, `Content-Type` the stored type when it is `image/png`, `image/jpeg`, `image/gif`, `image/webp`, `text/plain`, `application/pdf` or `application/json`, else `application/octet-stream` (an SVG is served as a download, never inline, since it can script); `Content-Length` |
| `POST …/attachments/{id}/delete` | `{opId}` |
| `GET /api/dispatch/views?project=`, `POST /api/dispatch/views`, `…/{id}/edit`, `…/{id}/delete` | `{views}`; `{opId, project?, name, kind, query}` |
| `GET /api/dispatch/ops/{opId}` | a receipt of the admin's without acting, as `GET /api/runs/{run}/commands/{command}` does; `404` when unknown |

The admin's operations are kept under the actor `admin`, as the task
commands are; `by` carries the display name the workbench sends
(`?by=` is never used: the name travels in the body as `byName` does for a
broadcast).

`events.go`: `dispatch(changes []dispatch.Change)` broadcasts a commit's
`event: dispatch`, one per change, in the store's order and as one entry
of every client's queue, so a commit that changed many entities (a
label's delete on hundreds of tickets) takes one place in the queue and
never drops a client that keeps up; past `maxDispatchEach = 64` changes
of one kind in one project, the commit tells them in one event where the
first of them was, that `entity` with `id` and `ticket` `""`, the
project's key and the commit's `seq`, so an entry holds at most 64
events of each kind in each project its commit changed, each at most
`maxDispatchEvent` bytes and 24 of framing: an operation of the store
changes one project's entities and the people its tickets name, seven
kinds at most, so its entry is at most 82,432 bytes, and a label deleted
on 10,000 tickets is two events (Owner decisions, 22); each event
with `{"seq", "entity", "id", "ticket", "project"}` (`ticket` the id of
the ticket the change belongs to, `project` its project's key, `""`
where there is none; `id` is `""` for a `person`, whose id is its name,
up to 60 characters of up to six encoded bytes each: the directory is
small and the pickers re-read it whole, so the event says only that it
changed) to every client, bounded by `maxDispatchEvent = 160` bytes,
derived from the longest payload: `seq` at most 19 digits, `entity` at
most 9 (`milestone`), `id` and `ticket` 16 hex characters, `project` 6,
the envelope 53, 119 bytes in all, every id being hex or a key and
never escaped; a test builds the longest payload of every entity, the
person's included, and asserts each fits, since the guard before
`broadcast` drops what is over as the `run` event's does and a dropped
event would be silence, by the `run`
event's path (`broadcast`): state a client cannot do without, so a
client that cannot keep up is dropped and starts over from a `snapshot`,
never skipped and kept as `activity` is; never a sink's: webhooks and
the run engine do not see it.

`docs/protocol.md`: the rows above in the HTTP API table, a "Dispatch
operations" paragraph beside "Commands with receipts", and the `dispatch`
row in Events. `docs/repository-map.md`: a row for `internal/dispatch`.
`docs/architecture.md`: a "Dispatch" section after "Crew runs" and a
paragraph under "Persistence".

### `internal/cli`, `internal/agents`: the CLI, the tools, the skill

`cli/dispatch.go`: `conductor dispatch projects`, `tickets [--project KEY]
[--status S]... [--q TEXT] [--json]`, `show KEY-n`, `create --project KEY
--title T [--description-file F] [--priority P] [--owner NAME] [--label
L]...`, `edit KEY-n [--status S] [--priority P] [--owner NAME] [--assignee
+x|-x]... [--label +l|-l]... [--milestone NAME] [--horizon H]`, `comment
KEY-n TEXT...`, `accept KEY-n [--note TEXT]`, `link KEY-n --kind
delivery|run|external [--url U] [--ref R]`, with `--server` and `--token`
as `conductor crews` takes them (the plain-`http` refusal included); each
write mints its op id and asks once more on a lost reply, as the web
does; its reads go through `apiClient.doLimit` with `maxDispatchReply`,
4 MiB, the smallest whole number of MiB above the largest reply any
read it makes can carry (`maxTicketReply` and `maxProjectReply`; a page
of 200 project or ticket summaries is far under), the way `conductor
crews` reads with `maxCrewsReply` (`maxReply`'s 1 MiB is a run's), and
`projects` and `tickets` follow `next` page by page, as `conductor
crews` walks its offsets, so a store of any size lists whole. `cli/task.go`: `conductor task ticket` prints the ticket this attempt's run
is linked to (one at most: a run belongs to one ticket, so the tools have
one target), `conductor task ticket comment TEXT...` comments on it as
`agent:<member>`. `cli/mcp.go`: `ticket_read` and `ticket_comment` over
the same two routes, `GET /api/sessions/{id}/ticket` and `POST
/api/sessions/{id}/ticket/comments` (the session's own token or the
admin's, through `taskSession`; `404 no_ticket` when no live ticket's
live `run` link names the session's run, a deleted ticket's link holding
none); the task commands and the tools read through
`crewClient`, whose 4 MiB (`crew.go`) holds `maxTicketReply`, and a test
of each reads the largest ticket whole. The usage in `root.go`, the completion table, and the
skill's new "Your ticket" section (`agents/skill.go`, with its test). No
agent creates or edits a ticket in this item (Owner decisions, 13).

### `web/app`: the pages, the views, the palette

- `composables/useDispatch.ts`: the client over `useApi` with the types of
  every reply (as `useSessions.ts` holds the crews' and runs'), and the
  ordering gate every page's state and the palette's Tickets cache go
  through, the `RunStore`'s rule (`utils/runs.ts`, `take`: a read's
  answer replaces a run only when no later read or removal of that run
  has been applied): a page numbers every request it sends, reads and
  operations alike, per thing shown (a ticket by key, a list by its
  filter, the project, the projects), keeps per thing the number of the
  last reply it applied, and applies a reply when its number is above
  that, else drops it; a request that fails applies nothing and moves
  nothing, so a success it overtook still lands (an edit committed, its
  event's re-read failed: the edit's reply shows the ticket at its new
  `rev`), and only a later applied reply supersedes an earlier one; a
  reply carrying an entity whose `rev` is below the one shown is
  dropped whatever its number, since the store's `rev` orders states
  as the client's numbers cannot (a list is taken by number alone); a
  filter change is a new thing, so the old filter's late page never
  shows under the new one; an operation's reply (`{receipt, ticket}`)
  goes through the same gate, its `rev` becoming the `expectedRev` of
  the next operation; a read sent after an operation and served before
  its commit shows the older state only until the commit's `dispatch`
  event, whose re-read is later than both; and a re-read that fails is
  asked again after the window, doubled each time up to 8 s, until one
  answers or a `snapshot` re-reads everything, so a page behind on a
  transient failure catches up with no further event. The gate is
  `utils/dispatchOrder.ts`, pure, tested with replies completing in
  reverse order and failing. `useDispatchOps.ts`: a minted op id, `expectedRev` from
  the entity shown, a lost reply asked for once with the same id, a
  refusal read from the receipt (`sendCommand` of `utils/tasks.ts`,
  generalised to take the op body, used by both), toasts in the task
  commands' words.
- `composables/useDispatchLive.ts`: the `dispatch` events and the
  `snapshot`, taken in `useAttention.handleBlock` beside `run`, handed to
  listeners. What a page shows derives from more than its own entity: a
  ticket's `blocked` from its blockers' statuses, its children and its
  relations' rows from other tickets' titles, statuses and parents, its
  project's labels, milestones and policy from the project, and the
  projects page's counts from every ticket; so the scope a page re-reads
  on is its project, not its entity. A ticket page re-reads its ticket at
  once when the event's `ticket` is it (a comment, link, attachment or
  relation names the ticket it belongs to) and, gathered for 250 ms
  (`RUN_EVENT_WINDOW_MS`'s rule), on any other event whose `project` is
  its project (a blocker closed, a sibling reparented, a label renamed,
  the policy edited); a list, board or roadmap re-reads itself, gathered
  the same way, when `project` is its project, and its saved views,
  gathered the same way, on a `view` event whose `project` is `""`, since
  a view of every project is offered on every project's pages beside the
  project's own and its save, edit and delete name no project; the
  projects page on any event at all, gathered the same way, since a
  ticket's status moves its project's counts; the run page's chip
  (`links?run=`) on any event
  whose `entity` is `ticket`, gathered the same way, since a link's add
  or remove and a ticket's delete or undelete are events of that ticket,
  the event names no run and no field, and a run linked to nothing has
  no project to narrow by (the read is one indexed row, `links(kind,
  ref)`); every subscribed page on a `snapshot`, the stream
  having reconnected after a drop, when what was missed is unknown;
  nothing polls. The event with `id` and `ticket` `""` that a commit
  sends for more than 64 changes of one kind in one project
  (`events.go`; Owner decisions, 22) needs no rule of its own: the
  project rules above re-read on it, a ticket page gathered rather than
  at once. Every re-read passes the ordering gate of `useDispatch.ts`
  above.
- `utils/dispatch.ts` (vitest): the key parser (`CND-12`), the
  vocabularies with their words, colours and icons, `ticketWords`, the
  close policy's words from `close`, the list's sort and the board's
  columns, the roadmap's columns, the keyboard model of the list (a roving
  focus by key, as the palette's rows). `utils/dispatchQuery.ts`: the
  filter to and from the URL (`?status=in_progress&label=api&q=…`) and to
  and from a saved view's `query`, one codec both ways.
- `pages/dispatch/index.vue`: the projects (key, name, open and done
  counts, the tracker) and New project (a dialog: key, name, folder with
  `DirInput`, tracker, close policy). `pages/dispatch/[project]/index.vue`:
  the views (`?view=list|board|roadmap`, kept per browser as the run
  page keeps its view), one toolbar (`DispatchFilters.vue`: the view
  tabs, a search box, Filter, whose popover holds status, priority,
  labels, owner, assignees, milestone, horizon and blocked, a chip per
  value in use, the count, Views and the sort; UX1 PR-12), the saved
  views behind the toolbar's Views button (`DispatchViewsBar.vue`: a menu
  of the views, save, rename, delete), `DispatchTicketList.vue` (rows:
  key, title, status, priority, owner, assignees as avatar chips, labels,
  milestone, updated, each in a column of its own from a 48 rem list;
  `x` selects, the page's palette group offers "Set status of 3
  selected…" and the like, one operation per ticket), `DispatchBoard.vue`
  (a column per status, cards with key, title, priority, labels and
  assignees, moved by HTML5 drag and drop or by `←` `→` on a focused
  card, a move to Done refused by the policy with the policy's words,
  the card snapping back), `DispatchRoadmap.vue` (Now, Next, Later and
  a folded Not planned, moved the same way; the milestones beside with
  `done/total`). `pages/dispatch/[project]/[number].vue`: the ticket page
  (`DispatchTicketPage.vue` with `TicketMarkdown.vue`, a textarea and
  the existing `MarkdownPreview.vue` with no folder and relative links
  disabled, `attachment:<id>` images resolved to object URLs the page
  fetched with the token, since an `img` cannot carry a header;
  `DispatchProperties.vue`, `DispatchComments.vue`,
  `DispatchAttachments.vue`, `DispatchRelations.vue`,
  `DispatchHistory.vue`, `DispatchClosePolicy.vue` with Accept and Close;
  `DispatchRuns.vue` listing the linked runs with `RunTasks.vue` embedded
  for the run the live store holds or its record). Pickers:
  `PersonPicker.vue` (the people, a new name added), `AssigneePicker.vue`
  (people and the catalog's agents, the agent chip as the sidebar's),
  `LabelPicker.vue`, `MilestonePicker.vue`, `TicketPicker.vue` (parent,
  relations: a search over the project's tickets by key and title).
- `layouts/default.vue`: Dispatch in `nav` (`i-lucide-ticket`, after
  Crews), `g-d` and `alt_d` (`KeyD` added to `ALT_PASSTHROUGH_CODES`, so
  `Alt+D` in a terminal reaches the page as `Alt+C` does; `GLOBAL_SHORTCUTS`
  gains the row). `useShortcuts.ts`: `DISPATCH_SHORTCUTS` (the list's and
  the page's keys above), registered by the Dispatch pages.
- `pages/runs/[run].vue` and `CrewRunHeader.vue`: a chip "CND-12" when
  `GET /api/dispatch/links?run=` names a ticket, opening its page, and
  "(deleted)" on it when the ticket is a tombstone; the lookup is read
  with the page and re-read through `useDispatchLive` on every
  `dispatch` event whose `entity` is `ticket`, gathered 250 ms, and on a
  `snapshot`, through the ordering gate as every read, so a run linked
  or unlinked from another browser gains or loses its chip with no
  reload, a run that had no ticket when the page opened included.
- The palette (`usePaletteRows.ts`, `utils/palette.ts`): a Tickets group
  in the search (the open tickets of every project, read after the first
  paint and cached a minute as the crews are, read again on a `dispatch`
  event while the palette is open; a key or a word of the title matches;
  `→` offers Open, Copy key, Set status…, Set priority…, Assign to me),
  `>` gains "New ticket in <project>" per project and "Go to Dispatch",
  `go dispatch` joins the `PAGES` of the `go` verb, and the Dispatch pages
  register their group (the views, New ticket, the selection's actions).
  No new prefix and no new verb (Owner decisions, 11).
- Phone widths: the list is rows that wrap, the board scrolls sideways,
  the ticket page is one column; nothing of the desktop layout decides for
  the phone.
- No new npm package: drag and drop is the browser's, Markdown is the
  editor's renderer, icons are the Lucide set already bundled.

### Docs and the rest

`README.md`: a "Dispatch" section in its voice (the pages, the keys, the
CLI). `docs/features.md`: the entry, in the last PR, recording what was
verified. `AGENTS.md`: Dispatch added to the railroad-names line. No
config key, no workflow change, no change under `desktop/`.

## Events, orderings and invariants

**The store.**

- One connection; every operation one `BEGIN IMMEDIATE … COMMIT`; every
  read of more than one statement (`Ticket` with its sets, comments,
  links, attachments, children and policy; the project read with its
  labels, milestones and views; `List` with its page and `total`; the
  projects with their counts) runs inside one read transaction
  (`BeginTx` with `ReadOnly: true`, which the driver begins as a plain
  `BEGIN` whatever `_txlock` says, so no write lock is taken; under WAL
  the transaction reads one snapshot from its first statement to its
  end), so a reader never sees an operation half done and a composite
  reply describes one state, its `rev` and its rows agreeing, whatever
  commits between its statements from this process or from a second
  server on the same file.
- `changes.seq` is monotonic within one database and local to it: the
  cursor the SSE event and the history's paging carry, never replicated.
  A change row's identity across replicas is (`actor`, `op_id`, `n`), and
  the one order of the log, here and under CS1, is (`at`, `actor`,
  `op_id`, `n`).
- Every entity's `rev` starts at 1 and is bumped by every operation that
  writes a `changes` row for it, inside that operation; `expectedRev`
  compares with it before anything changes.
- A ticket's `key` and `number` never change, not by a delete, a project
  rename (the project's key cannot change) or a move (a ticket is never
  moved between projects: it is cancelled and recreated, and a `relates`
  link may say so).
- Deletes are tombstones (`deleted_at`) for every row with an id of its
  own: a deleted ticket, comment, attachment, link, relation, label,
  milestone or view keeps its row and its history; a membership row
  (`ticket_assignees`, `ticket_labels`) is state alone, removed by a
  `remove` whose `changes` row is its history; a label or milestone
  deleted leaves every ticket, one `changes` row each, and its name is
  free for a new row; a deleted ticket's incoming references (its
  children's `parent_id`, the relations and run links that name it)
  stay as they are, inactive, and return with its undelete ("Deleting a
  ticket" below); a relation or link added again after a remove is a
  new row with a new id; lists leave tombstones out unless asked; a hard
  delete exists for nothing in this item.
- `tickets_fts` is written in the transaction that writes the row it
  indexes, and an operation rewrites the entries of the rows it writes
  and no other (a comment's add, edit or delete its comment's entry, an
  edit of a ticket's text the ticket's own); a read never finds a ticket
  the search does not, nor the reverse.
- An attachment's file is on disk, fsynced, named by its hash and its
  new name made durable (`publish`: on unix the shard directory synced
  and then `attachments/`, on every upload, so the shard's own entry is
  durable whoever made the shard and however that ended; the move
  written through on Windows) before the row that names it is
  committed, so a committed row never names a file a power loss can
  take back; and an `uploads` row committed
  before the rename names its hash until that commit; only the sweep
  deletes a file, at open, before any route is served, under a
  `BEGIN IMMEDIATE` transaction it holds while it unlinks, when neither
  a live `attachments` row nor an `uploads` row names its hash (an
  `uploads` row older than `sweepGrace`, an hour, is a crashed upload's
  and is dropped first, in a transaction of its own committed before
  any file goes, so a dropped lease stays dropped however the sweep
  ends and no rollback can revive a lease whose file was unlinked), so
  an upload another server on the same directory has renamed and not yet
  committed is named by a row every sweep sees, a reused file included;
  the row is the upload's lease, and the operation that names the hash
  deletes it and is refused `expired` when it is gone, so an upload
  resumed after a sweep dropped its lease names nothing; a remove
  deletes no file.
- The receipt is written in the operation's transaction, a refusal's in a
  transaction with nothing else; a database failure writes none; a repeat
  answers the receipt, a conflicting repeat `op_conflict`; the newest
  20,000 receipts are kept (`prune(ctx, now)` drops the rest, called at
  open and by a goroutine every `pruneEvery`, a field of the store, a day
  by default, and by a test directly). A resend whose receipt is pruned
  is answered by the `changes` log, which keeps the operation's identity
  for good: when a row (`actor`, `op_id`, 0) exists the operation ran,
  and the resend is answered `{repeat: true, status: done}` with no
  `rev`, `id` or `key` (the detail went with the receipt; the entity can
  be read) whatever its content, since the fingerprint went too; the
  lookup is made before anything is written, never left to the log's
  unique index. When no such row exists the receipt was a refusal's,
  which wrote no change, and the resend runs as a new operation. So a
  successful operation never runs twice, however old its resend, and the
  only resend that acts again is a refused one, which is refused again
  or allowed now on its own merits.
- `OnChange`'s listeners are called once per commit, after it, with all
  it changed, outside the transaction, on the caller's goroutine; the
  event hub's `dispatch` never waits.
- The store refuses a write while read-only; a read works whenever the
  database opens.

**Status.** `backlog`, `ready`, `in_progress`, `in_review`, `done`,
`cancelled`. Any status may follow any other except that `done` is
entered only when the project's `close_requires` is met: `acceptance` is
met when `accepted_at` is set, `accepted_for` is the ticket's owner now
and `accepted_criteria` is the hash of its criteria now; `delivery`
needs a live `delivery` link (a merge is such a link here; IN1a reads a
link's merged state). A change of owner or of the criteria after an
acceptance leaves it recorded, in the history, and unmet, and the page
says which ("accepted for Alice; the owner is now Bob", "accepted before
the criteria changed") until Accept is made again. A move to `done` or
`cancelled` sets `closed_at`; a move out of them clears it. Acceptance is
a fact of its own: `accept` sets it, `unaccept` clears it, no status
change touches it, and a ticket may be accepted while open or closed.
The policy's state is computed on every ticket read (`close`), never
stored, so a policy, an owner or criteria edited later read true at
once.

**Hierarchy, relations and references.** Every reference a ticket
carries, its parent, its milestone, each of its labels and each
relation's other end, names a live row of the ticket's own project,
checked inside the operation's transaction after the revision check: a
row missing or tombstoned is `not_found`, a live row of another project
`invalid`, and nothing is written on either; a foreign key alone would
not see a tombstone. The write lock orders a reference against a
delete: a `label.delete` or `milestone.delete` committed first leaves
the add or set `not_found`, and one committed after removes what the
add made, with its `changes` row, as it does for every ticket. A view's
`project_id` names a live project or is `''`; a comment, link,
attachment or relation names a live ticket. A parent is a ticket of the
same project; a parent chain is at most 8 deep and never a cycle, checked in the
operation by walking up the new parent's chain and down the moved
ticket's subtree (a recursive CTE under the transaction, bounded by the
project's tickets): the moved ticket is not among its new ancestors, and
the chain from the deepest ticket of its subtree to the root is at most
8, so a subtree moved under a long chain is refused as a new ticket
under it would be. `blocks` edges never
form a cycle, checked by a depth-first walk from `to` under the
transaction (a project's blocks graph is small; the walk is bounded by
the 100 relations per ticket and the tickets of the project). A ticket
whose blockers are not all `done` or `cancelled` is `blocked` in the list
filter and on its page; nothing stops its status from changing (a person
decides), the page only says so.

**Deleting a ticket.** `ticket.delete` tombstones its own row and
touches no other: one `changes` row (`deleted_at`), its own `rev`
bumped, nothing written on its children, on its relations' other ends
or for the runs its links name, so `ticket.undelete` restores it whole,
every reference to it included, by the reverse of that one row. A
reference whose target is a tombstone is inactive: it stays in its row,
is read as such (the child's page says "parent CND-12 (deleted)", the
key opening the deleted ticket's page, where Undelete is; a relation's
row and the run page's chip say the same) and counts for nothing while
the target is deleted: a deleted parent's children keep their
`parent_id` (the list's `parent=` finds them by the column) and its own
`children` are not read from it, a deleted blocker does not block
(`blocked` is from live blockers, as above), a deleted ticket's live
`run` links hold no run (`run_linked` counts the live links of live
tickets, so the run can be linked to another ticket; while no live
ticket holds the run, `links?run=` answers the deleted ticket with
`deletedAt` set, the latest-linked of them when several tombstones hold
a link to it, and the session route and the tools answer `no_ticket`;
once a live ticket holds it, every lookup answers that ticket and the
tombstones' links count for nothing, through the deleted ticket's
undelete, which removes its link, below), and a tombstoned label's
membership and a tombstoned
milestone read as absent and none. The write-time check above is a
check on the write alone: it keeps a person from making a reference to
what is deleted; the inactive state is for what a later delete or a
merge leaves behind, and it ends the moment the target is undeleted.
An undelete that finds one of its `run` links naming a run another live
ticket has linked meanwhile removes that link in the same operation (a
`remove` row, `prior` naming its tags), so a run belongs to one live
ticket at most; every other reference comes back as it was, and no
other ticket's `rev` moves on a delete or an undelete, since nothing of
theirs is written (their pages re-read on the project's event, as every
derived row does). The parent and `blocks` walks take tombstoned rows
as edges (a tombstone keeps its `parent_id` and its relations), so the
invariants hold over the whole table and an undelete never makes a
cycle or a chain past 8. Every write on a deleted ticket but `undelete`
is `not_found`, a comment, link, attachment or relation on it included.
A label or milestone has no undelete, so its delete leaves every ticket
(the cascades above); a ticket's has one, so its delete leaves every
reference in place.

**The SSE event.** `event: dispatch`, `data: {"seq":N,"entity":"ticket",
"id":"…","ticket":"…","project":"CND"}`, at most 160 bytes (`id` empty for a
person; the bound derived in `events.go` above), after the
commit, once per entity the commit changed but once for all of a kind in
a project past 64 of them (that `entity`, `id` and `ticket` `""`), a
commit's events together and in order, one entry of a client's queue
(Owner decisions, 22), by the `run` event's path: a client that cannot
keep up is dropped and reconnects to a new `snapshot`, never skipped and
kept (the one rule, stated the same in `events.go` above), and never for
one commit's size. What a page re-reads: a ticket page its ticket at
once when `ticket` is it (its own fields and sets, its comments, links,
attachments and relations all name it) and, gathered for 250 ms, on any
other event of its project, since its `blocked`, its children, its
relations' rows and its project's labels, milestones and policy derive
from other rows of the project (a blocker closed is an event naming the
blocker, not the blocked ticket); a list, board or roadmap itself
(gathered 250 ms) when `project` is its project, a project's policy edit
included, since it changes `close` on every ticket, and its saved views
(gathered the same way) on a `view` event whose `project` is `""`,
since a view of every project is offered on every project's pages
beside the project's own; the projects page on
any event, gathered the same way, since its counts derive from every
ticket; the run page's chip its `links?run=` lookup on any `ticket`
event, gathered the same way, since the event names no run and an
unlinked run has no project; every subscribed page on a `snapshot`,
which is how a dropped
client repairs what it missed, beside the sessions' and runs' re-read.
The one event a commit sends for more than 64 of a kind in a project
names no one entity, and these rules take it as any other event of its
project: a ticket page among those changed re-reads gathered rather
than at once, and the run page's chip on it as on any `ticket` event.
No `internal/proto` and no `protocol.ts` change: an event on `GET
/api/events` is an HTTP API change, as the `run` event was (its commit,
4413a183, touched `events.go`, its test and `docs/protocol.md` and
neither protocol package), documented in `docs/protocol.md`'s Events
section with its bound and tested in `api/events_test.go`; the event's
TypeScript type lives in `useDispatch.ts` beside the replies'.

**Migrations.** Version 1 is new: `prepare` reads the version and makes
the schema whole in one `BEGIN IMMEDIATE` transaction (Open, above).
Later versions follow the ledger's shape, `migrations[v]` statements and
`migrationsGo[v]` steps in one transaction with the version bump, the
version read inside that transaction too, so the steps run are chosen
from the version the lock holder sees and a second opener finds the
bump, never runs the steps again; and the conservative rule the owner
set for the ledger on
2026-10-25: a migration never drops or reinterprets what an older
version kept; a value it cannot derive is left at its default and said
in one `changes` row of `op: migrate` per entity it touched, so a later
reader can tell derived from recorded. A database of a later version
opens read-only.

**Field rules for replication** (O10: the Program's open decision on how
a replicated ticket's fields merge, which this item settles as a schema
rule CS1 applies). Every `changes` row is an operation on one field of
one entity; the row tables are the state those rows make. CS1 ships
`changes` rows between replicas, drops one whose identity (`actor`,
`op_id`, `n`) it holds, and applies the rest in the one order by each
field's class, below; the rules are `rules.go`'s `Class`, `Merge` and
`Reconcile`, tested there, and this table and the paragraph after it are
their text.

| Class | Fields | Rule when two replicas changed the same field |
|---|---|---|
| immutable | every `id`, `project_id`, `number`, `key`, `created_at`, `created_by`, a change row's (`actor`, `op_id`, `n`) | never written twice; a replica that receives a second `create` of an id it has, or a change row whose identity it holds, drops it |
| scalar | `title`, `description`, `acceptance`, `status`, `priority`, `horizon`, `owner`, `parent_id`, `milestone_id`, `accepted_at`, `accepted_by`, `accepted_for`, `accepted_criteria`, `accepted_note`, `closed_at`, `deleted_at`, `external_*`, `authority`; a project's `name`, `description`, `cwd`, `tracker`, `tracker_url`, `close_requires`, `archived_at`; a label's `name`, `color`, `deleted_at`; a milestone's and a view's fields, `deleted_at` included; a comment's `body`, `edited_at`, `deleted_at` | last writer wins per field: the later `at` wins; equal, the greater `actor`; equal, the greater `op_id`. A field is merged alone: concurrent edits of `title` and `priority` both survive |
| set | `ticket_assignees`, `ticket_labels`, `relations`, `links`, `attachments` | an observed-remove set: an `add` is tagged by its change row's identity (`actor`, `op_id`, `n`); a `remove` names in `prior` the tags of that member's adds it had seen and removes those alone; a member is present while an add of it exists that no remove names, so an add a remove did not see survives whatever the clocks say, and two adds of one member from two replicas are two tags, both needed gone. The set tables hold the members those tags make |
| log | `comments`, `changes` | append only, in the one order (`at`, `actor`, `op_id`, `n`); a comment's place on the page by (`created_at`, `author`, `id`), its number derived; never merged, never lost |
| allocation | `number`, a project's `key` | a store with no replication provider, this item whole and the free default for good, owns the whole range: `next_number` alone, no block and nothing registered anywhere (`number_from` and `number_to` 0 say so). Under CS1 a create is local and never waits on the service (articles 7 and 8): each replica mints from a block the sync service handed it ahead of need (the next asked for when half is used, so an outage of days exhausts none) and, when its block is out and the service unreachable, from a private overflow range of its own, high and never shared, handed to it once when it joined (columns CS1 adds beside the two reserved here), so an offline create succeeds at once with a key that is unique by construction and never renumbered, only long (`CND-1000012`, which the page says is from the replica's overflow and which a new block does not replace); a project made while the service is unreachable is made at once, its key registered when the service answers, and a key found taken by then keeps the project on its replica, unshared, its page saying so, until the person gives it a free key (`project.rekey`, an intent CS1 adds, allowed only while the project is neither shared nor being shared, its tickets' keys following since none has been seen elsewhere). Joining a team with a store that already holds tickets is CS1's share, an ordered cut-over (`docs/plans/sync-seams.md`, Events, Sharing a project): the project's next 1,000 numbers reserved, its history sent, the share completed only when no row is past what was sent, and block numbers taken once it is shared. A project's numbers before its share and every block are below 1,000,000, each replica's overflow range above it, and a retired store's provisional window above 2,000,000,000; a shared project's key on every replica is the workspace registry's, never projected from a row |
| gated | `status` set to `done` | the writer applied the close policy; a merge takes the scalar rule and never re-applies the policy, since the facts it rests on travel in the same log; the page shows the policy's state as read |

A bound (8 assignees, 20 labels, 100 relations, 50 links, 50 attachments
per ticket, 100 labels and milestones per project) is checked on a local
write alone: a merge never refuses a replicated add, so a set may stand
past its bound after one, which the page shows as it is and which
refuses further local adds until it is under the bound again. The
invariants that span rows, a parent chain acyclic and at most 8 deep, a
`blocks` graph acyclic and a run linked to one ticket at most, are
checked on a local write and re-established after a merge by one rule
both replicas apply to the same log, which answers from the log alone
and never from the state a batch found: `Reconcile` replays the source
changes of one invariant (a project's parent sets; a project's `blocks`
adds and removes; one run's `run` link adds and removes with the
tickets' `deleted_at` sets, since a tombstone holds no run) in the one
order (`at`, `actor`, `op_id`, `n`), as if they had been written one
after another on one replica, admitting an add only when the edges
admitted and not removed before it keep the invariant and refusing it
otherwise, as that writer would have been refused had it seen the
others (a `run` link added, or a ticket undeleted, while another live
ticket holds the run is the refused change, its link the edge); the row
tables hold the admitted edges. A refused add is undone there, the
parent cleared to `''` or the relation or link removed, with a `changes`
row of `op: reconcile` by the actor `merge`, and that row is derived,
never a source change: CS1 ships none (the actor `merge` marks it), and
the projection is computed again after every merge, so an add refused
after one batch and admitted once an earlier change arrives is restored,
with a second `reconcile` row that says so, and the reverse; the history
shows the repairs, the row tables the projection of the log as it
stands. A repair is therefore a function of the source log alone: the
four `blocks` edges `B→A`, `B→C`, `A→B`, `C→A` in log order leave
`B→A`, `B→C` and `C→A` and refuse `A→B` whether they arrive whole, as
{1, 3} then {2, 4} or as {2, 3, 4} then {1}, where a repair made against
each batch's state would have undone `A→B` in the first case and `C→A`
with `A→B` in the second. `rules.go`'s `Reconcile(changes []EdgeChange)
[]EdgeChange`, pure, names the adds refused, and two replicas holding
the same log reach the same graph whichever batches it arrived in.

What the schema keeps unique is kept so under replication by one of
these rules and never by an index a replicated add could trip (the
unique indexes are only those of keys never minted twice, listed under
the schema). Allocated: a ticket's `number` and a project's `key`, the
allocation class. Folded: a person's name is the person, so a second
`person.add` of a name held is dropped as a second `create` of an id is;
a relation's (`from_id`, `to_id`, `kind`) and a link's (`ticket_id`,
`kind`, `ref`) are a member of a set, so two live rows of one from two
replicas are one member with two tags, read as one: the row latest by
the log order (`at`, `actor`, `op_id`, `n`) among the member's live
rows stands for it, its `id` the one a read shows and a remove names,
and for a link its `url` and `note` the ones shown (the scalar rule
applied to the member's payload: two replicas that added one link with
two URLs show the later one, the same from either side, while a
`link.add` that repeats an identity with a new payload on one replica
stays `invalid` there), and a local remove through that `id` names
every live tag of the member in `prior`, so the member is gone on both;
a membership table (`ticket_assignees`, `ticket_labels`) holds one row
per member however many tags the log holds for it, and a membership
whose label is a tombstone reads as absent, as does every reference a
merge left naming a tombstone: a `milestone_id` set on one replica
while the milestone was deleted on another (the delete's clears travel
as scalar sets of the tickets it saw; the set of the ticket it did not
see wins or loses by the scalar rule and, winning, names a tombstone)
reads as none, and a `parent_id`, a relation or a run link naming a
ticket deleted elsewhere is inactive as "Deleting a ticket" says and
active again on its undelete; none of these needs a `Reconcile` rule,
since the rows are kept and read, never undone. Reconciled: a run
linked to two tickets, by `Reconcile` above.
Kept: two live labels of one name in one project, created on two
replicas, both stand (the local rule refused only what its writer saw);
the project page shows both and a person deletes or renames one, as a
milestone of a repeated name is already a person's to tidy. Each rule
gives the same rows whichever order the log arrived in, and the rules
tests apply both.

`at` is the writer's clock, UTC with nanoseconds; CS1 may replace it with
a hybrid logical clock without changing a rule, since every rule compares
`at`, `actor`, `op_id` and `n` and nothing else. The schema reserves
nothing more for it. A replica's identity is the origin its rows are
written under: CS1's rows (`docs/plans/sync-seams.md`, OS-1a) add
`changes.origin`, a tag a replica's provider hands it, set on every
local row from the replica's first join on, and the rules compare
canonical actors, `@<origin>/<actor>`, so every replica orders the same
rows the same way; a local actor never begins with `@`.

## Out of scope

- Launching a crew from a ticket, the launch package in an attempt's task
  file, plan revisions, candidates, checks and findings on the ticket
  page, and the reserved link kinds filled in (Program item DS2); human
  review and repair (RV1); importing or publishing GitHub issues and the
  per-field authority of an external tracker (IN1a: this item records
  `source`, `external_*` and `authority` as empty columns and shows an
  external project as such); decision records (DR1); people as crew
  members with identities (HM1, ID1); replication (CS1: this item writes
  the rules and the log it needs); Brakeman drafting tickets (BK1); the
  `verified` close requirement (DS2); a `merged` close requirement read
  from a tracker (IN1a: here a merge is a `delivery` link a person adds,
  and its merged state is not read).
- Custom statuses or workflows per project, manual ordering of cards (a
  rank), cycles or sprints, estimates and points, time tracking, ticket
  templates, recurring tickets, a trash with restore beyond `undelete`,
  bulk import, search inside attachments, inline SVG attachments.
- Tickets for share-link viewers: every Dispatch route is the admin's; a
  guest's join page shows none.
- Routing `dispatch` events to notifications, the Roundhouse or webhooks:
  the event feeds the pages alone.
- A shared SQLite helper package for the ledger and Dispatch, and a shared
  owner lock (Owner decisions, 2 and 3).
- Anything under `desktop/`, any config key, any protocol frame.

## Tests per surface

Per `docs/checks.md`: a Go change runs `make lint`, `go test -race
-count=1 ./...` (the whole module) and `make lint-static`; a UI change
runs `npm --prefix web run typecheck`, `npm --prefix web test` and `make
test-e2e` (which builds the web app and the binary first; the spec alone
while iterating, after `make web-build build-go`); a PR spanning both runs
both; a docs-only PR runs nothing and says so. No Python script changes,
so the scripts suite is not run by this item. `make preflight` before the
last push of every PR; the Mac and Windows rows (`python3
scripts/preflight.py --machines mac,windows`) on PR-7 and PR-12, the last
Go PRs of each half, and on PR-11, the last UI PR. A check that did not
run is reported as not run. PR-0 spans Go and the UI and runs both
suites.

**Go, race, `internal/dispatch`** (each fails without its part; the
timing-free ones `-count=1`, the sweep and the pruning `-count=30`):

- open and schema: a new database made whole; version 1 reads back; a
  database marked version 2 opens read-only and a write is `read_only`
  with the words; a damaged file (a byte over the header) is read-only
  with the check's words; a symbolic link in the file's place refuses
  `Open`; the pragmas in force (`journal_mode`, `foreign_keys`); `Close`
  leaves no `-wal` behind; `Checkpoint` on an open store; eight stores
  opened at once on one empty directory from eight goroutines all open
  writable, the schema made once (one `schema_version` row, every table
  once in `sqlite_master`) and each writes after, `-count=30`; a store
  opened while the test holds a `BEGIN IMMEDIATE` on the file past
  `busyTimeout` (50 ms for the test) opens read-only with the words,
  and one opened while the test holds it for less opens writable and
  made the schema once.
- ids and keys: a project key refused outside the pattern; `CND-1`,
  `CND-2` allocated in order under 50 goroutines creating at once (no
  duplicate, no gap); a key kept through a delete; `number_from`/`_to`
  read back as 0.
- operations: the same op id and content answers the receipt with
  `repeat`; other content `op_conflict`; `expectedRev` behind
  `stale_revision` kept as a failed receipt and nothing changed; a refused
  change (a bound passed) leaves a failed receipt and no change; a commit
  made to fail by a test seam leaves neither; `rev` bumped once per
  operation whatever it set; `changes` rows one per field with `prior`,
  `n` from 0 in the operation's order, a row with an identity already
  held refused; `prune(ctx, now)` called on 20,001 receipts keeps the
  newest 20,000, and the goroutine with `pruneEvery` a millisecond calls
  it, seen on the store's `pruned` counter (polled, never slept on); a
  successful operation whose `ops` row the test deletes, resent with the
  same content and with other content, answers `repeat` and `done` with
  no `rev`, changes nothing and errs nowhere; a refused one whose row is
  deleted, resent, runs anew; `OnChange` called once per commit, after
  it, with one change per entity, each with the sequence, the ticket and
  the project, and every listener a copy of its own.
- every bound in `model.go`, at the bound and one past it (a table test);
  a description at its bound made of quotes, backslashes and newlines,
  set and then replaced, recorded whole in `value` and `prior`; a link
  whose `ref` and `url` are 2 KiB each of backslashes (`https://h/`
  then backslashes, which `url.Parse` takes) and whose note is 1,000 of
  U+2028, added and removed, recorded whole in its two `changes` rows,
  the add's `value` measured under 16 KiB; a control
  character other than `\n` and `\t` in a Markdown field refused.
- rules: `Class` for every field named in the table; `Merge` for each
  class: the later scalar wins, the tie-breakers in order, an add whose
  tag a remove did not name survives (two adds of one member from two
  actors with equal `at`, one removed, leave the member present), an
  observed add is removed, a log is never merged, a second create and a
  change row of a held identity are dropped; `Reconcile` on two
  concurrent parent sets that make a 2-cycle, on three that make a
  3-cycle, on a reparenting that makes a chain of 9, on two `blocks`
  that make a cycle and on two `run` links of one run on two tickets
  refuses the later edge, the same from either replica's log in either
  order; on the four `blocks` edges of the field rules delivered whole,
  as {1, 3} then {2, 4} and as {2, 3, 4} then {1}, the same three stand
  and `A→B` is refused every way, and the test shows that a repair
  against each batch's state would differ; an edge refused after one
  batch and admitted once an earlier remove arrives is restored with a
  second `reconcile` row, the reverse undone the same way; an undelete
  arriving while another live ticket holds the run leaves the undeleted
  ticket's link refused; every `reconcile` row carries the actor
  `merge`; the uniqueness rules in both orders: two creates of one label
  name in one project leave both labels, two adds of one relation and of
  one link from two actors, the link's with two URLs and two notes,
  leave one member with two tags that reads as the later add's row (its
  id, url and note) in both delivery orders and that one local remove
  through that id names together, a second `person.add` of a held name
  is dropped, a membership of a tombstoned label reads absent, a
  `milestone_id` set on one replica while the milestone was deleted on
  another names the tombstone and reads as none in both orders, a
  relation added on one replica to a ticket deleted on another is live
  and inactive in both orders and active after the undelete; the schema
  read back holds exactly the unique indexes listed; the one order of
  the log on a seeded pair of logs.
- tickets: create with every field; edit setting several fields in one
  operation; `done` refused by `acceptance` then allowed once accepted;
  refused by `delivery` then allowed with a link; `done` refused again
  after the owner is changed and after the criteria are edited, allowed
  after a new acceptance, the page's reason in `close`; `closed_at` set
  and cleared; `unaccept`; a parent cycle and a depth of 9 refused, by a
  new parent and by moving a subtree of height 4 under a chain of 5
  (allowed under a chain of 4); a parent of another project refused
  `invalid` and a deleted one `not_found`; a label and a milestone of
  another project `invalid`, a tombstoned one `not_found`; the race
  ordered both ways by the test: a label deleted then added to a ticket
  `not_found`, added then deleted leaves no membership and one `changes`
  row for the ticket, the same for a milestone;
  delete and undelete with the key kept; a parent with live children, a
  blocker and a ticket with a `run` link deleted: the children keep
  `parent_id` and read it as deleted, the blocked ticket reads
  unblocked, the run's lookup names the deleted ticket as deleted, then
  the run links to another ticket and the lookup names that one, live,
  from then on: after a second deleted ticket's link names the run too,
  and after the first deleted ticket's undelete (its link removed); with
  the two deleted tickets' links alone (the live one removed) it names
  the later-linked tombstone; no other ticket's `rev` moved; a comment,
  relation, link, attachment or edit on the deleted ticket `not_found`;
  the undelete restores every reference and removes the run link a live
  ticket took meanwhile with one `changes` row; a cycle and a chain of 9
  through a tombstone refused; assignees past 8, labels past
  20 refused; a set past its bound by a row the test inserts refuses an
  add and allows a remove.
- relations: `blocks` cycle refused (A blocks B blocks C blocks A); a
  cross-project relation refused; `blocked` computed from live blockers
  only; a relation removed and added again is a new row with a new id,
  the old one a tombstone.
- comments: `n` derived at read, 1 to k with no gap under 50 concurrent
  adds, a row the test inserts with an earlier `created_at` taking its
  place and renumbering the later ones; edit and delete as tombstones;
  the comment's FTS entry written with it and no other entry (a word in
  a new comment found, gone after its delete, and the ticket's own entry
  and its other comments' left as they were).
- queries: each filter alone and three together against a seeded set;
  every sort both ways; the cursor pages 1,000 tickets without a repeat
  or a gap; `q` of a ticket key (`CND-12`), of `OR`, `AND`, `NOT`, `a:b`
  and `(x)`, each matched as the words in it and none an error; a quote
  alone, a star alone and `"*"` an empty page and no error, and one of
  them beside a word dropped with the word matched; the last term
  matching as a prefix; a ticket found by words spread across its own
  entry and two of its comments, and not found when one word is in none
  of them; a deleted ticket out unless asked; a composite
  read paused by a test seam (`betweenReads`, between the ticket's row
  and its comments) while a second `Store` on the same file commits a
  comment and a status answers the state before the commit whole, the
  `rev` and the rows agreeing, and the next read the new state, the
  same with the writer in this process; `BenchmarkList` with its two
  targets.
- attachments: the file written and hashed before the row, the `uploads`
  row before the file; a second upload of the same content shares the
  file; a remove deletes no file, and the sweep after it deletes the file
  no live row names and leaves a shared one; the sweep deletes an orphan
  made by hand whatever its age and leaves a named file and one an
  `uploads` row names; an upload paused at `beforeCommit` while a second
  store opened on the directory sweeps (grace an hour), then committed,
  names a file still there, and the same when the content is a reused
  hash: uploaded before, its only attachment removed, its file's
  modification time set back a day by the test, the second store's sweep
  leaves it because the `uploads` row names it, and the attachment opens
  whole after the commit; an upload paused at `beforeCommit`, and
  another at `beforePublish`, while the second store sweeps with its
  `sweepGrace` at zero (the lease older than it), then resumed, is
  refused `expired` with a failed receipt, writes no attachment row and
  leaves no file in the directory, and the same content uploaded again
  under a new op id succeeds; the same with the second store's `unlink`
  replaced by one that deletes the file and then returns an error, so
  its sweep fails after unlinking: the sweep returns the error, the
  resumed upload is still refused `expired` with a failed receipt,
  writes no attachment row, and no file of its hash is in the directory
  (the lease's expiry was committed before the unlink, so the sweep's
  failure restored nothing); an upload paused at `beforeCommit` while
  the second store runs `expireLeases` alone with its `sweepGrace` at
  zero, then resumed, is refused `expired`, its file left in the
  directory, named by no row, and gone after the next `collectOrphans`;
  an `uploads` row older than the grace is
  dropped by the sweep and its file, named by no live row, with it; a
  temp file older than the grace goes and a younger one stays; a refusal
  (the 51st attachment) leaves no `uploads` row; a name with `/` or `..`
  refused; 16 MiB + 1 refused with no file left; `syncDir` made to fail
  by the test: `Add` returns the error, writes no attachment row and no
  receipt, leaves no lease, and the file it published is swept as an
  orphan; the same content uploaded again under a new op id succeeds;
  `syncDir` made to fail on `attachments/` alone, the shard's sync
  passing, on the upload that makes the shard: `Add` returns the error
  as above and the shard directory is left behind; the same content
  uploaded again under a new op id, `syncDir` restored and recording,
  succeeds, its `syncDir` given the shard directory and then
  `attachments/` though the shard was there before it; `syncDir`
  recording its paths is given the shard directory and then
  `attachments/`, in that order, on every upload, among them one into a
  shard the test made by hand with `os.Mkdir` and never synced (what an
  upload stopped between making the shard and syncing its parent leaves
  behind, in this server or another); an upload held by the test inside
  its `syncDir` at the shard's sync, the shard it made not yet synced
  into its parent, while a second upload into the same shard runs whole:
  the second's `syncDir` is given the shard directory and then
  `attachments/` and its row commits, and the first, released, finishes
  and names its file; the Windows `publish` runs in the Windows
  preflight row.
- views: create, edit, delete (a tombstone); a `query` past 8 KiB
  refused; a view of every project.
- links and labels: a `delivery` link without `ref` takes its URL as
  one, two with different URLs allowed, the same URL twice `invalid`, one
  removed and added again a new row; a `run` linked to a second ticket
  `run_linked`; a label deleted leaves every ticket with one `changes`
  row each and its name free for a new label; the same for a milestone.

**Go, race, `internal/api`** (`dispatch_test.go`, `dispatch_attachments_
test.go`, `events_test.go`): every route of the table with its 2xx and
each 4xx named there, through the real store in a temp data directory;
the workbench token required on every route (a share token refused);
`403 switchyard` on a switchyard; `503` without a data directory and
`503 dispatch_unavailable` for a write on a read-only store (a database
of a later version planted); the `dispatch` event on the stream after a
create, at most 160 bytes, the longest payload of every entity (a
person's with its empty `id`, a milestone's with every id at its length
and `seq` at 19 digits) measured under it, a client whose queue is full dropped and closed by
it as a `run` event drops one; a commit's events one entry of a client's
queue, in order: a commit of 1,000 changes, none past 64 of a kind in a
project, reaches a client nobody drains as one entry of 1,000 events and
leaves it subscribed; at 64 changes of a kind in a project each is an
event of its own and past it one event tells them where the first was,
kinds and projects counted apart and a person's event as ever; an entry
measured at its worst (every kind in two projects at 64 each, every value
at its longest), one past it and at 10,000 of every kind, never past 64
events of a kind and project at their longest; and a label and then a
milestone deleted through their routes from more tickets than the queue
holds entries, while a live stream is open, each reach a client that
reads nothing as one entry, their own event and one for their tickets,
and the stream whole, the stream open for the next commit's event; a
ticket body with a description at its
bound of escaped characters accepted and read back whole, a body past
192 KiB `413`; the largest ticket the server can make (every field and
set at its bound, every text of `<`) read back whole and under
`maxTicketReply`, the reply holding no `\u003c`; the largest project read
(200 views with 8 KiB queries, 100 milestones, 100 labels, every text
at its escaped worst) under `maxProjectReply`, and 201 projects with
16 KiB descriptions of backslashes listed over two pages, each page
under 128 KiB; a receipt read back through `GET /api/dispatch/ops/
{opId}`; an upload past the bound `413`; the download headers (nosniff,
attachment, an `image/svg+xml` served as `application/octet-stream`); the
`links?run=` route against a live run of the engine (a fake launcher) and
a run's record, naming the one ticket, and `409 run_linked` from a second
ticket; after the ticket's delete the route names it with `deletedAt`
and `GET /api/sessions/{id}/ticket` is `404 no_ticket`, after the run is
linked to a second ticket both name the second, and still do after the
first's undelete. `internal/cli` (`dispatch_test.go`, `task_test.go`,
`mcp_test.go`): each command and tool against a test server, a lost
reply asked for once with the same op id, `no_ticket` for a session in
no linked run and in one whose only link is a deleted ticket's, the live
ticket once the run is linked to one, `show`, `conductor task ticket`
and `ticket_read` of the
largest ticket printed whole through their clients' limits, `projects`
over 201 projects with 16 KiB descriptions of backslashes listed whole
through two pages, each under the limit, and `tickets` over 201 the
same, the usage.
`internal/agents`: the skill's section present and its marker intact.

**vitest** (`web/app/utils/*.test.ts`, `composables/*.test.ts`):
`dispatch.test.ts` (the key parser, the vocabularies' words, `ticketWords`,
the close policy's words, the board and roadmap columns, the keyboard
model: `j` `k` wrap at the ends, `x` toggles, a row gone hands the focus
on), `dispatchQuery.test.ts` (the URL codec both ways, a saved view's
query both ways, an unknown parameter ignored), `useDispatchLive.test.ts`
(an event naming the page's ticket re-reads it at once; one naming
another ticket of its project re-reads it once after the 250 ms window,
three such events once; one of another project does not; a comment's
event names its ticket; a `project` event re-reads a list of that
project and a ticket page; a ticket event re-reads the projects page and
a run page's lookup once after the window, a label event the projects
page and not the lookup; a `view` event whose `project` is `""` the
saved views of a list of any project once after the window (PR-9); a
`snapshot` re-reads every subscriber, the
lookup included; a re-read that fails is asked
again after the window, doubled, and stops once one answers or a
`snapshot` re-reads), `dispatchOrder.test.ts` (two
reads of one ticket answered in reverse order leave the later's; an
operation's reply arriving after a later read's was applied is dropped;
a later request that fails leaves an earlier success applied, the
edit's reply landing after the failed re-read and the shown `rev`
moving; a reply with a `rev` below the shown one dropped whatever its
number; a filter
changed drops the old filter's late page; another thing's reply is not
dropped), `palette.test.ts` (a key typed ranks its ticket first
in Tickets; `go dispatch`), `useShortcuts.test.ts` (`KeyD`, the rows),
`tasks.test.ts` (`sendCommand` with an op body unchanged for the task
commands).

**Playwright** (`web/e2e`, the stub agents, one worker):

- `dispatch.spec.ts`: New project (key, name, policy); a ticket made from
  the page's New ticket button (the `c` key is PR-9's), its key `E2E-1`;
  the ticket page edits the title, the description (preview shown), the
  priority and the owner; a comment posted and shown; the history lists
  the fields set; the project page's counts; a second browser context on
  the same ticket sees a title edit without a reload (PR-8), the comment
  (PR-10) and its close card following a policy edit made on the project
  page by the first (PR-11); the second context's projects page sees the
  open count move when the first makes a ticket (PR-8); the second
  context on a ticket blocked by another sees Blocked go when the first
  cancels the blocker, and on a parent sees a child leave its children
  when the first reparents it (PR-10).
- `dispatch-views.spec.ts`: the filter in the URL (`?status=`, `?q=`),
  the list filtered; a saved view made, its chip applying the filter,
  renamed and deleted; a view of every project saved by a second browser
  context, its chip shown on the first's open list of a project with no
  reload and gone when the second deletes it; the board's columns with
  the ticket in its status;
  a drag to In review; the roadmap's columns; Next and Now by `→` on a
  focused card; the phone width (390 px) for the list and the page
  (`phone.spec.ts`'s viewport).
- `dispatch-keys.spec.ts`: `j` `k` `Enter` on the list, `Esc` back, `s`
  opening the status picker and a choice applying, `x` selecting two and
  the palette's page group setting their status, `G D` from the Yard,
  `Alt+D` from a focused terminal (the terminal did not get it).
- `dispatch-close.spec.ts`: Done refused on the page with the policy's
  words; a board drop to Done snapping back with the same words; Accept
  then Done allowed; the owner changed after Accept and Done refused with
  the card's reason, Accept again and allowed; a `delivery` link required
  by a second project; the policy card's states.
- `dispatch-links.spec.ts`: a managed run launched (`tasks.spec.ts`'s
  crew), linked to a ticket by the link form; the ticket page's Runs
  section shows the run's task rows (`[data-task-row]`); the run page's
  chip names the ticket and opens it; a second context on the run page,
  opened before any link, gains the chip when the first context links
  the run and loses it when the first removes the link, with no reload.
- `palette.spec.ts` and `palette-rows.spec.ts` gain the Tickets group (a
  key typed opens the ticket on Enter; `→` Copy key by its toast; "New
  ticket in E2E" opens the dialog) and the Dispatch page's rows.

Every spec names what it made (keys of its own project, `stopStrays`
first where it launches a run) and never counts a list it did not fill.

**By hand** (what tests cannot): the acceptance boundary below, a week of
real use; the desktop app on Windows (WSL) and the Mac opening Dispatch
after an upgrade with an empty store and after a restore of the data
directory; `conductor export` once C6 lands; Markdown pasted from a
GitHub issue rendering as expected; an attachment of 15 MiB over a slow
link.

## Boxcar constraints

1. The engine starts an attempt only through `crew.Launcher`: not touched;
   Dispatch starts nothing, and the link from a ticket to a run is a row
   that names a run the engine made.
2. `session.Local` drives a `session.Process`: not touched; no new code
   reaches a session, a pid or a tty.
3. An attempt's context is files in its worktree: not touched here; DS2
   will put a ticket's snapshot into `.conductor/task.json` through the
   same `os.Root` the task file uses, never into the server's memory
   alone.
4. The agent talks to the server over the network: honoured; `conductor
   task ticket` and the `ticket_read` and `ticket_comment` tools go over
   HTTP with `CONDUCTOR_NOTIFY_TOKEN`, the address-based path every other
   task tool takes.
5. Checks go through `internal/proc`: not touched; Dispatch runs no
   command.
6. Platform splits stay platform tags and the UI shows capabilities: not
   touched; Dispatch has no platform split and no capability, it is
   always there.
7. The ledger's evidence rows gain `where` in C10: not touched; Dispatch
   adds no evidence row and reads none; a run link is a reference.
8. hostagent is a launch site as much as api: not touched; Dispatch lives
   on the server, and a `conductor host` session reaches it only through
   the same routes a server session does.

## Open-core checklist

- [x] Article 1: no licence or plan check anywhere in `internal/dispatch`,
  the routes or the pages; the only condition is whether a data
  directory exists.
- [x] Article 2: the open-source build gets the whole of Dispatch: store,
  views, policy, history, CLI and tools, with no account.
- [x] Article 3: nothing is gated; there is no flag a fork could flip
  because there is nothing behind one.
- [x] Article 4: every read is a `GET` the admin makes; the store is plain
  files in the data directory, exported whole by C6 and restored by
  copying them back; no subscription exists to lapse.

## The PRs

Each PR is one issue and one branch cut from `main`, a draft PR at its
first commit, the checks for its surfaces before every push, its body
ticking the open-core checklist and naming the agent that made it; each
leaves `main` working and green on its own (a route without its page is
reachable by the CLI and the tests; a page without its later parts says
what is not there yet). Parallel means the two touch no common Go
package, no common directory under `web/`, and no common file under
`docs/` or at the root: two PRs editing different docs merge without a
conflict, two editing `docs/protocol.md` or `README.md` do not, so a PR
that edits one of those is not parallel with another that does. Sizes
are estimates of changed lines
with tests, measured against `internal/crew/ledger` (1,784 lines of code
and 1,300 of tests for 8 tables): the store's four PRs come to about
5,500 for 15 tables, the FTS index, the rules and the pruning.

| PR | Title | Touches | Depends on | Parallel with | Size |
|---|---|---|---|---|---|
| PR-0 | crew, web: the engine's verb is "scheduling" in text people read | `internal/crew`, `web/app`, `README.md`, `docs/protocol.md` | none | PR-1, PR-2, PR-3, PR-4 | ~150 |
| PR-1 | dispatch: the store, its schema, ids and keys, the typed operations with receipts, the changes log and the rules | `internal/dispatch`, `docs/dependencies.md`, `docs/repository-map.md` | none | PR-0 | ~1,500 |
| PR-2 | dispatch: the row types and bounds, projects and tickets | `internal/dispatch` | PR-1 | PR-0 | ~1,300 |
| PR-3 | dispatch: comments, labels, milestones, people, relations, links, acceptance and the close policy | `internal/dispatch` | PR-2 | PR-0 | ~1,500 |
| PR-4 | dispatch: the list query, full-text search, saved views, the pruning goroutine and the benchmark | `internal/dispatch` | PR-3 | PR-0 | ~1,200 |
| PR-5 | api: projects and tickets over HTTP, the `dispatch` event and the receipts route | `internal/api`, `docs/protocol.md`, `docs/architecture.md` | PR-4, PR-0 | none | ~1,400 |
| PR-6 | api: comments, labels, milestones, people, relations, links, acceptance and views over HTTP | `internal/api`, `internal/dispatch`, `docs/protocol.md`, `docs/architecture.md` | PR-5 | PR-8 | ~1,400 |
| PR-7 | dispatch, api: attachments as content-addressed files, uploaded, swept and served | `internal/dispatch`, `internal/api`, `docs/protocol.md` | PR-6 | PR-8, PR-9 | ~1,000 |
| PR-8 | web: the Dispatch client, the live events, the projects page, the list view and the ticket page | `web/app`, `web/e2e` | PR-5 | PR-6, PR-7 | ~1,500 |
| PR-9 | web: the board and roadmap views, filters, saved views, the keyboard and the palette rows | `web/app`, `web/e2e` | PR-8, PR-6 | PR-7, PR-12 | ~1,400 |
| PR-10 | web: comments, attachments, parent and relations, labels and milestones on the ticket page | `web/app`, `web/e2e` | PR-9, PR-7 | PR-12 | ~1,400 |
| PR-11 | web: the close policy and acceptance, delivery links, the run Tasks view inside a ticket, the run page's chip | `web/app`, `web/e2e` | PR-10 | PR-12 | ~1,000 |
| PR-12 | cli, mcp, skill: `conductor dispatch`, `conductor task ticket`, `ticket_read` and `ticket_comment` | `internal/api`, `internal/cli`, `internal/agents`, `docs/protocol.md`, `README.md` | PR-7 | PR-9, PR-10, PR-11 | ~1,100 |
| PR-13 | docs: the features entry, the README section, the architecture section, the names line, the by-hand record | `docs`, `README.md`, `AGENTS.md` | every PR above | none | ~300 |

**PR-0** (`internal/crew`, `web/app`, README, `docs/protocol.md`): the run
log's notes become "scheduling paused" and "scheduling resumed"
(`commands.go`, and `task.go`'s "scheduling paused: the ledger could not
record…"), the toast "Scheduling paused" and "Scheduling resumed", the
button and palette row "Pause scheduling" and "Resume scheduling"
(`CrewRunHeader.vue`, `pages/runs/[run].vue`), the task words "ready,
scheduling is paused" (`utils/tasks.ts`), and the README's and
`docs/protocol.md`'s lines that say "dispatch" for them; the `paused`
field, the `/pause` and `/unpause` routes, the Go identifiers and the
test names stay (Owner decisions, 19). Tests: the Go tests that wait on
the notes' words follow them (`managed_test.go`, `ledger_bounds_test.go`),
`tasks.test.ts` follows, `tasks.spec.ts` unchanged and green. Go and UI:
both suites.

**PR-1** (`internal/dispatch`): `Open` with the pragmas, the quick check,
version 1 of the schema (every table above, the FTS table included so
version 1 is whole), the migration table empty, read-only on a newer or
damaged database, `Close`, `Checkpoint`, `OnChange`; ids and keys;
`Apply` with receipts, the `changes` log with its identities, and
`prune(ctx, now)`; `rules.go` with `Class`, `Merge` and `Reconcile`; the
`docs/dependencies.md` row for `modernc.org/sqlite` naming two importers
and the `docs/repository-map.md` row. `Apply` is proven on `person.add`,
the simplest intent, which this PR carries; the other intents come with
their entities. Tests: open and schema, ids and keys, operations, rules.
Go only.

**PR-2** (`internal/dispatch`): `model.go` with every row type and bound;
projects (create, edit, archive, unarchive) and tickets (create, edit of
every scalar field, assignees and labels as sets, delete, undelete), the
number allocation and the parent checks up the chain and down the
subtree. Tests: bounds, the ticket tests that need no relations, comments
or policy. Go only.

**PR-3** (`internal/dispatch`): comments with the number derived at read;
labels and milestones with their bounds, a delete leaving every ticket;
people's remaining intents; relations with the cycle checks and
`blocked`; links with the run kind's reference checked by a `RunExists
func(id string) bool` the API gives the store and one ticket per run;
`accept` and `unaccept` with the owner and the criteria they record; the
close policy's `close` state on a read and the gate on `done`. Tests:
relations, comments, the policy and acceptance, labels and milestones,
links. Go only.

**PR-4** (`internal/dispatch`): `List` with every filter, sort and the
cursor; FTS5 maintained in the transaction (a ticket's own entry, an
entry per comment) and the query's terms quoted; `Ticket`, `History`,
`Children`, `Blockers`, the composite reads in one read transaction with the
`betweenReads` seam; views; the goroutine that calls `prune` every `pruneEvery`
(the sweep of orphan files is PR-7's); `BenchmarkList` with the two
targets measured on the Linux box and named in the PR body. Tests:
queries, views, the pruning goroutine, the benchmark. Go only.

**PR-5** (`internal/api`, docs): `openDispatch` in `New`, the field, the
close; the projects, people, tickets (create, read, edit, accept,
unaccept, delete, undelete, history, comments read), `links?run=` and
`ops/{opId}` routes; the `dispatch` event with its bound on the `run`
event's path; `docs/protocol.md` rows and the Events row; the
architecture section, which opens by telling the component from the
engine's verb. Tests: those routes' 2xx and 4xx, auth, switchyard, the
two 503s, the event and the drop, the receipt route, the body bound, the
replies with HTML escaping off and the largest ticket under
`maxTicketReply`. Go only.

**PR-6** (`internal/api`, `internal/dispatch`, docs): comments (add,
edit, delete), labels, milestones, people, relations, links
(`run_linked` among them), views routes; their `docs/protocol.md` rows;
and, its label and milestone deletes being the first routes whose
commit can change more entities than a client's event queue holds,
`OnChange` once per commit, a commit's events one entry of the queue
and more than 64 of a kind in a project told in one event (Owner
decisions, 22), the event's paragraphs here, in `docs/protocol.md` and
in `docs/architecture.md` saying so. Tests: each, and a label and a
milestone deleted through their routes from more tickets than the queue
holds while a live stream stays open. Go only.

**PR-7** (`internal/dispatch`, `internal/api`, docs): `attachments.go`
(the file first, the hash, the `uploads` row as the lease, `publish` per
platform with `syncDir`, the shard and then `attachments/` synced on
every upload, the attachment row with the lease check and `expired`,
`beforePublish` and `beforeCommit`), `sweep.go` (`sweep(ctx, now)` as
`expireLeases` and then `collectOrphans`, two write transactions with
`sweepGrace`, the leases' expiry committed before any unlink, `unlink`
the seam, at open), `Open(ctx, id)`; the upload route (multipart, the
bound), the download route (the headers, the allow list), the delete;
the protocol rows. Tests: the attachment tests of the store, the paused
upload against a second store with new content, with a reused hash,
swept past its grace, through a sweep that fails after unlinking and
through its expiry alone, the parent sync failing and the shard reused
by a later and by a concurrent upload, the two routes, the headers. Go
only; the Mac and Windows preflight rows run here.

**PR-8** (`web/app`, `web/e2e`): `useDispatch.ts` with the ordering gate
(`utils/dispatchOrder.ts`), `useDispatchOps.ts` (`sendCommand`
generalised), `useDispatchLive.ts` and the `dispatch` and
`snapshot` branches in `useAttention.handleBlock`; `utils/dispatch.ts`
and `dispatchQuery.ts`; the nav item, `G D`, `Alt+D`, the shortcuts rows;
`pages/dispatch/index.vue` with New project; `pages/dispatch/[project]/
index.vue` with the list view, its New ticket button and the filter bar
(the board and roadmap tabs present and saying "comes with the next
change" until PR-9); the ticket page with title, description and
acceptance (edit and preview), status, priority, horizon, owner,
assignees, history. Tests: vitest `dispatch.test.ts`,
`dispatchQuery.test.ts`, `dispatchOrder.test.ts`,
`useDispatchLive.test.ts`, `useShortcuts.test.ts`; Playwright
`dispatch.spec.ts` (its second context seeing a title edit and the
projects page's count; the comment and the policy parts come with PR-10
and PR-11). UI only.

**PR-9** (`web/app`, `web/e2e`): the board and the roadmap with drag and
keyboard moves; the saved views bar, read again on a `view` event of
every project (`project` `""`) as on its project's events; the list's
keys (`c` among them) and selection with the page's palette group; the
palette's Tickets group, the `>` rows, `go dispatch`; phone widths.
Tests: vitest additions to `dispatch.test.ts`, `palette.test.ts` and
`useDispatchLive.test.ts`; Playwright `dispatch-views.spec.ts`,
`dispatch-keys.spec.ts`, the `palette.spec.ts` and `palette-rows.spec.ts`
additions. UI only.

**PR-10** (`web/app`, `web/e2e`): comments (post, edit, delete, the
author's name, the derived number), attachments (upload with progress,
the list, images inline through object URLs, download, delete), parent
and children, relations with `TicketPicker.vue`, labels and milestones
management on the project page and their pickers on the ticket page.
Tests: Playwright `dispatch.spec.ts` additions (comments, one seen live
by the second context, an attachment uploaded and downloaded byte for
byte, a parent set, a `blocks` relation shown both ways, the blocked
ticket's page and the old parent's page following the other context's
change); vitest for the pickers' pure parts. UI only.

**PR-11** (`web/app`, `web/e2e`): `DispatchClosePolicy.vue` with Accept,
Unaccept and Close, the refusal's words and the unmet reasons; delivery
links; `DispatchRuns.vue` with `RunTasks.vue` embedded and the run link
form; the run page's chip; the project dialog's close policy editor.
Tests: Playwright `dispatch-close.spec.ts`, `dispatch-links.spec.ts`, the
`dispatch.spec.ts` policy-edit addition; `tasks.spec.ts` unchanged and
still green. UI only; the Mac and Windows preflight rows run here.

**PR-12** (`internal/api`, `internal/cli`, `internal/agents`, docs,
README): `GET /api/sessions/{id}/ticket` and `POST /api/sessions/{id}/
ticket/comments` through `taskSession`; `conductor dispatch` and
`conductor task ticket`; the two MCP tools; the usage, the completion
table, the skill's section; the protocol rows and the README's CLI lines.
Tests: `cli/dispatch_test.go`, `task_test.go`, `mcp_test.go` (the
largest ticket through each client's limit among them), the skill test,
`api/dispatch_test.go` additions. Go only; the Mac and Windows preflight
rows run here.

**PR-13** (docs): the `docs/features.md` entry recording what each PR
verified and the by-hand checks run; the README's Dispatch section; the
AGENTS.md names line; the acceptance observation below once it is made,
else the entry says it is pending. Docs only; no check applies.

## Owner decisions

Dated 2026-10-28, the twentieth and twenty-first 2026-10-29, the
twenty-second 2026-10-30. Each is a default the owner can overturn with
a dated row in the Program's status log, its convention for a decision
changed; the reasoning is why it was taken.

1. **O10, the conflict rules.** Dispatch keeps an operation log
   (`changes`) beside its tables and classifies every field as immutable,
   scalar (last writer wins per field), set (observed-remove, add wins),
   log (append only), allocation (number blocks) or gated (a close
   through the policy); the table in "Events, orderings and invariants"
   and `rules.go` are the decision, and CS1 replays the log by them.
   Per-field rules were chosen over per-row ones because two people
   editing a title and a priority at once is the common case and neither
   should lose; an observed-remove set because a label removed on one
   machine and added on another should end added, which is what a person
   expects of the later action.
2. **No owner lock for Dispatch.** The ledger needs one because two
   servers would each recover the other's runs; Dispatch has no process
   to recover, SQLite's locking orders writers across processes, the
   schema is made or migrated inside a `BEGIN IMMEDIATE` that reads the
   version first (Open), and every revision is read and bumped inside
   its transaction, so a second
   server on the same data directory (already refused unless each has a
   token of its own) changes nothing wrongly (an `uploads` row names
   each of its uploads until the row commits, the sweep sees it, and an
   upload whose row a sweep dropped first is refused, never trusted); it
   only misses the other's SSE events, which the next read repairs.
3. **A second importer of `modernc.org/sqlite`, no shared helper.** The
   open, pragma and migration code is about sixty lines; copying them
   into `internal/dispatch`, with one change (the version read moves
   inside the schema transaction, since no owner lock orders two openers
   here; Open, above), keeps the ledger untouched by this stream. A
   shared package can come when a third store needs it.
4. **Six statuses, five priorities, three horizons, fixed.** Linear's
   vocabulary without custom workflows: `backlog`, `ready`, `in_progress`,
   `in_review`, `done`, `cancelled`; per-project statuses are out of
   scope until a project needs one.
5. **People are a local directory of names** (decision D4): the owner is
   one name, assignees are names or catalog agents (`person:`,
   `agent:`); nothing authenticates them until Program item ID1, and an
   acceptance records the operator's display name as `by` beside the
   owner it was made for (`accepted_for`) and the criteria it was made
   against (`accepted_criteria`), so a ticket reassigned or rewritten
   after it is accepted again, on purpose.
6. **The close policy's default is `["acceptance"]`** for a new native
   project and `[]` for an external one (its tracker decides there);
   `delivery` is offered; `verified` is refused until DS2 defines it. A
   merge is represented by a `delivery` link in this item, the merged
   pull request's or the release's URL that a person adds; reading a
   link's merged state from the tracker comes with IN1a, and a `merged`
   requirement with it.
7. **Keys are `KEY-n`**, the project key `^[A-Z][A-Z0-9]{1,5}$`, numbers
   allocated by the store from the whole range while no replication
   provider is registered (this item, and the free default for good)
   and under CS1 from a block the sync service hands out ahead of need,
   with a private overflow range per replica for a create made while
   the service is unreachable, so a create never waits on the service
   (articles 7 and 8) and a key, once minted, is never renumbered and
   never moves between projects; a project's key is registered with
   the service when it can be reached, a project made before that
   staying on its replica until its key is free or re-keyed by the
   person. A long overflow key now and then was taken over a
   provisional key renumbered later, since a key a person has pasted
   into a commit or a chat must stay what it was.
8. **Attachments are content-addressed files beside the database**,
   16 MiB each, 50 per ticket, served as downloads with an allow list
   for inline types; the page fetches with the token and shows images
   through object URLs. Files rather than blobs keep the database small
   and the export plain.
9. **Routes under `/api/dispatch/`, pages under `/dispatch/`, the nav item
   "Dispatch" with `G D` and `Alt+D`**; a ticket page at
   `/dispatch/<KEY>/<n>`.
10. **The list's keys are Linear's letters** (`j` `k` `Enter` `x` `c` `s`
    `p` `a` `l` `m` `o`), plain keys that pause in text fields and
    terminals as every page key does.
11. **No new palette prefix or verb.** A key or a title finds a ticket in
    the search; `>` offers the new-ticket rows; the pages register their
    group. The twelve verbs stay twelve.
12. **External-tracker projects are a kind and a URL in this item.** A
    ticket of such a project is made by hand with an external reference;
    every field is native until IN1a records which system owns it; the
    `authority` and `external_*` columns are reserved now so IN1a adds
    no migration for them.
13. **Agents read and comment, nothing more.** An attempt whose run is
    linked to a ticket may read it and comment on it with its own token;
    creating or editing tickets is the admin's, and Brakeman's drafts
    come later through the same typed operations.
14. **Search is FTS5**, compiled into the pinned driver (checked
    2026-10-28); a typed query is words, every term quoted, never FTS
    syntax; a term of punctuation alone is dropped, a query of nothing
    else an empty page.
15. **Receipts: the newest 20,000 kept**, pruned by `prune(ctx, now)` at
    open and daily; the `changes` log answers a resend older than that
    (`repeat`, `done`), so no successful operation runs twice.
16. **Deletes are tombstones**; projects are archived, never deleted; keys
    are kept. A ticket's delete touches no other row, so its undelete
    restores it whole: references to a deleted ticket are inactive and
    shown as such, never detached, since a detachment could not be
    undone and a person deleting a parent by mistake expects its
    children back.
17. **Every Dispatch route is the admin's**; share-link viewers see no
    ticket.
18. **The `dispatch` event reaches the pages alone**, never the Events
    page's routing, notifications or webhooks, until someone asks.
19. **The engine's verb is "scheduling" where people read it.** The run
    log's "scheduling paused" and "scheduling resumed", the toast
    "Scheduling paused" and "Scheduling resumed", the button and palette
    row "Pause scheduling" and "Resume scheduling", the task words
    "ready, scheduling is paused", and the README's and
    `docs/protocol.md`'s lines for them (PR-0); the `paused` field, the
    `/pause` and `/unpause` routes, the Go identifiers (`dispatch`,
    `pauseDispatch`), the test names and the docs that describe the code
    (`task.go` dispatches a task) keep the lower-case verb, which is what
    the engine does, beside the capitalised component, which is the
    tracker; the architecture's Dispatch section says so first. The
    harness plan already called the action "pausing scheduling"
    (`docs/plans/agent-harness.md`), and once Dispatch is the tracker,
    "Dispatch paused" on a run page would read as the tracker going down.
20. **Each comment is its own search entry, 2026-10-29, the owner's.**
    The search index holds an entry of each ticket's own, its key,
    title, description and acceptance criteria, and one of each live
    comment, so a comment's add, edit or delete re-indexes that comment
    alone, and an edit of a ticket's text its own entry alone. A search
    takes its terms one by one, each matching the tickets of every entry
    it is in, and intersects them, so every word of a query must still
    appear somewhere in the ticket or its comments, not necessarily in
    one entry, as before. The plan's first design kept one entry per
    ticket with its comments in it, rewritten whole by every comment's
    write while the store's one connection was held and every Dispatch
    read and write waited behind it: PR-4's review found that cost
    growing with the ticket's comment text, with no bound short of the
    comment bound, and measured it at 2.78 s for one comment's edit on a
    ticket of 1,999 comments of 16 KiB
    ([PR #425, review finding 3](https://github.com/rocksolidlabs/conductor/pull/425#discussion_r4416676542)).
    Chosen over keeping that design, and over capping the comment text
    one entry holds, which would leave older comments' words unfound.
    The search pays instead: a term reads every entry it matches, where
    FTS5's AND inside one entry skipped through them, and PR-4's body
    measures the full-text pages against their target. It lands in
    PR-4, before that PR merges; version 1 of the schema is in no
    release yet, so it changes version 1.
21. **A search of several very common words may pass the full-text
    target, 2026-10-29, the owner's.** Under 20 a search builds each
    term's set of tickets from every entry the term is in, and FTS5
    alone reads a very common word's entries in 4.5 to 6.2 ms, so each
    such word adds about 9 ms to a page. PR-4's second review measured
    a page of 100 in BENA for the vocabulary's 3, 5 and 10 most common
    words at 69, 96 and 151 ms, against 27 to 31 ms under the first
    design, and no statement or index it measured brought them under
    50 ms
    ([PR #425, review finding 1](https://github.com/rocksolidlabs/conductor/pull/425#discussion_r4418998440)).
    The owner accepted that cost: the 50 ms target holds for a search of
    one common word, of rarer words and of a prefix of one or two
    letters, and `BenchmarkList` reports the searches of several common
    words without a target. Chosen over capping a search's terms, which
    bounds only the longest search, and over keeping a ticket's comments
    in its own entry up to a bound, a second way of indexing with its
    switch-over.
22. **A large commit's events drop no stream, 2026-10-30, the
    owner's.** One Dispatch commit that changes more entities than a
    client's event queue holds, 256 entries, must not close the live
    `GET /api/events` streams. `Store.OnChange` hands a commit's changes
    to its listeners together, once per commit; the event hub queues a
    commit's events as one entry, so a client is dropped only when it is
    256 entries behind; and a commit that changed more than 64 entities
    of one kind in one project is sent as one `dispatch` event for them,
    that `entity` with `id` and `ticket` empty, the project's key and
    the commit's `seq`, which the pages' re-read rules already take,
    since they key on the project. The plan's first design sent an event
    per entity, each an entry of its own, so a label's or a milestone's
    delete on more tickets than the queue holds dropped every open
    browser at once, one that kept up among them: PR-5's review found it
    ([PR #427, review finding 3](https://github.com/rocksolidlabs/conductor/pull/427#discussion_r4420424910)),
    and a label on 1,000 tickets, deleted through the store, closed a
    live stream after 261 events. Chosen over batching alone, which puts
    every event of a commit in its one entry, so a label on 10,000
    tickets would put about 1.2 MB in each client's queue at once, and
    over keeping the behaviour and saying so, since one large delete
    would drop every open browser. An entry now holds at most 64 events
    of each kind in each project its commit changed, at most 82,432
    bytes for an operation of the store. It lands in PR-6, whose label
    and milestone deletes are the first routes that can make such a
    commit.

One question is left open, the shape of the week of use (Acceptance),
which is the owner's; each answer above lets the plan stand, and the
owner overturns any of them with one row.

## Acceptance

The boundary, in this plan's words: for seven days a team's work goes
through Dispatch and through nothing else, and a run's tasks are reached
from the ticket that asked for them. It is observed, not ticked:

- Once PR-11 is merged, the owner and one other person use Dispatch for
  seven days on one server: two browsers on the owner's `conductor
  serve`, each with the workbench token (the only way in until Program
  item ID1) and the display name its browser keeps (`useIdentity`), so
  comments and acceptances carry two names. Not two machines: DS1 has no
  replication (CS1), and a second `conductor serve` would be a second
  store, two trackers. The week's record says what sharing one token
  cost. The work is a native project `CND` holding the stream that
  follows (DS2's PRs, or EN1's), each a ticket with its acceptance
  criteria, owner, priority and horizon; the managed runs that work on
  them linked to their tickets; comments, labels, a milestone for the
  release, a saved view per person; closings through the policy. Nothing
  is published back (IN1a).
- Which work the week is observed on is the owner's to decide before
  PR-11 lands, since either shape sets one of the owner's own rules
  aside for the week: either this repository's own stream runs in
  Dispatch alone, a GitHub issue made only as each PR's anchor when its
  branch is cut (a one-line body naming the ticket; nothing tracked or
  moved there, and the project board left for that week), which sets
  aside AGENTS.md's backlog-in-issues and the board; or the week is
  observed on work outside this repository's issue-per-PR flow, which
  sets aside the boundary's "alone" for the repository itself. Mirroring
  tickets into issues by hand is neither: it is two trackers, and the
  week could not show the boundary.
- After the seven days the owner writes what was missing, what was slow
  and what was wrong into `docs/features.md` (PR-13 carries it when the
  week fits before it; else a later docs PR of the stream), a bug the
  stream caused fixed in the stream, a product defect outside it filed.
- Done means: every PR merged, the entry written, the week recorded with
  the owner's words that Dispatch was the only tracker in use and that a
  run's tasks were looked at from the ticket; the Program's status row
  moves to Done then, never on the table alone.

What needs the owner: the review of this plan and its decisions; the
shape of the week (above); cutting the fourteen issues; the week of use
and its record; the merges.
