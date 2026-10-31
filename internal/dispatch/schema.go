package dispatch

import (
	"context"
	"database/sql"
)

// Version is the schema this binary writes. A database of a later version
// opens read-only; an earlier one is brought to it (migrations).
// Version 1 is DS1's (docs/plans/dispatch.md); version 2 adds a ticket's
// plan revisions and launches and three columns of a link
// (docs/plans/dispatch-evidence.md, Program item DS2).
const Version = 2

// migrations bring a database from the version keyed to the next, in the
// transaction that read the version, with the version's bump. A migration
// follows the ledger's shape and the conservative rule: it never drops or
// reinterprets what an older version kept; a value it cannot derive is
// left at its default and said in one changes row of op migrate per
// entity it touched, so a later reader can tell derived from recorded.
//
// Version 1 to 2 makes the three tables of plan revisions and launches
// and their indexes, and adds a link's fingerprint, candidate and
// verified, each empty on every version 1 row, which is true of all of them:
// a version 1 add kept no fingerprint, and no link had a candidate. It
// reads and rewrites no row, so nothing is derived and no migrate row is
// written.
var migrations = map[int][]string{
	1: append([]string{
		plansTable,
		launchesTable,
		launchFilesTable,
		`ALTER TABLE links ADD COLUMN fingerprint TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE links ADD COLUMN candidate TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE links ADD COLUMN verified TEXT NOT NULL DEFAULT ''`,
	}, version2Indexes...),
}

// migrationsGo run after a version's statements, in the same transaction:
// what SQL alone cannot work out.
var migrationsGo = map[int]func(context.Context, *sql.Tx) error{}

// plansTable is a ticket's plan revisions (plans.go): each immutable once
// made but for its state, its approval and its withdrawal, its body the
// JSON of PlanBody named by its digest, its fingerprint the proposing
// operation's (the request's content, kept with what it made so a resend
// whose receipt is pruned is still checked: Replay). based_on is a
// revision of the same ticket or empty; at most MaxPlans a ticket, a rule
// of the proposal's.
const plansTable = `CREATE TABLE plans (
		id TEXT PRIMARY KEY,
		ticket_id TEXT NOT NULL REFERENCES tickets(id),
		based_on TEXT NOT NULL DEFAULT '',
		crew_id TEXT NOT NULL,
		crew_name TEXT NOT NULL DEFAULT '',
		crew_updated_at TEXT NOT NULL DEFAULT '',
		base TEXT NOT NULL,
		body TEXT NOT NULL,
		digest TEXT NOT NULL,
		bytes INTEGER NOT NULL,
		state TEXT NOT NULL DEFAULT 'proposed',
		approved_at TEXT NOT NULL DEFAULT '',
		approved_by TEXT NOT NULL DEFAULT '',
		approved_note TEXT NOT NULL DEFAULT '',
		approved_rev INTEGER NOT NULL DEFAULT 0,
		approved_intent TEXT NOT NULL DEFAULT '',
		withdrawn_at TEXT NOT NULL DEFAULT '',
		withdrawn_by TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		created_by TEXT NOT NULL DEFAULT '',
		fingerprint TEXT NOT NULL DEFAULT '',
		rev INTEGER NOT NULL DEFAULT 0
	)`

// launchesTable is a ticket's launches of runs from its approved
// revisions (Program item DS2, launches.go): the run's id minted here,
// the package (JSON) named by its SHA-256, pending until the run's
// server records it started or failed. Its run id is unique by a rule of
// the launch's and by the ledger's insert, never by an index, which
// could refuse a replicated row.
const launchesTable = `CREATE TABLE launches (
		id TEXT PRIMARY KEY,
		ticket_id TEXT NOT NULL REFERENCES tickets(id),
		plan_id TEXT NOT NULL REFERENCES plans(id),
		run_id TEXT NOT NULL,
		label TEXT NOT NULL DEFAULT '',
		ticket_rev INTEGER NOT NULL,
		intent TEXT NOT NULL,
		criteria TEXT NOT NULL,
		package TEXT NOT NULL,
		package_sha256 TEXT NOT NULL,
		state TEXT NOT NULL DEFAULT 'pending',
		why TEXT NOT NULL DEFAULT '',
		agents TEXT NOT NULL DEFAULT '[]',
		created_at TEXT NOT NULL,
		created_by TEXT NOT NULL DEFAULT '',
		fingerprint TEXT NOT NULL DEFAULT '',
		started_at TEXT NOT NULL DEFAULT '',
		rev INTEGER NOT NULL DEFAULT 0
	)`

// launchFilesTable is the attachments a launch's package names, by their
// hash, which the sweep keeps while a launch names them.
const launchFilesTable = `CREATE TABLE launch_files (
		launch_id TEXT NOT NULL REFERENCES launches(id),
		sha256 TEXT NOT NULL,
		PRIMARY KEY (launch_id, sha256)
	)`

// version2Indexes is the indexes version 2 adds: a ticket's revisions
// and launches in their order, a revision's launches, the pending
// launches, a run's launch, and the launches that name a file. None is
// unique.
var version2Indexes = []string{
	`CREATE INDEX plans_ticket_created ON plans (ticket_id, created_at)`,
	`CREATE INDEX launches_ticket_created ON launches (ticket_id, created_at)`,
	`CREATE INDEX launches_plan ON launches (plan_id)`,
	`CREATE INDEX launches_state ON launches (state)`,
	`CREATE INDEX launches_run ON launches (run_id)`,
	`CREATE INDEX launch_files_sha256 ON launch_files (sha256)`,
}

// schema is version 2, whole: every table of docs/plans/dispatch.md's
// schema table and docs/plans/dispatch-evidence.md's (plans, launches,
// launch_files, and a link's three columns, last of its table as the
// migration adds them), the FTS index included, its indexes and two of the
// list's own (tickets_list, ticket_labels_label), and the unique
// indexes of the keys never minted twice and nothing else (projects.key,
// tickets.key and (project_id, number), people.name, changes (actor,
// op_id, n), ops (actor, id), beside every row's own id); every other
// uniqueness the plan's table names (a live label's name in its project,
// one live relation per (from_id, to_id, kind), one live link per
// (ticket_id, kind, ref)) is a rule checked on a local write inside the
// operation's transaction, so that no index refuses a replicated add: the
// plan's "Field rules for replication" table, which rules.go is, says
// what a merge makes of a second one.
//
// Conventions: times are RFC 3339 text in UTC with nine fractional digits
// always (stamp), so at orders as text; JSON is text; rev is on every row
// with a scalar field that can change (projects, tickets, labels,
// milestones, comments, views), written 0 by a create and bumped by every
// operation that writes a changes row for the row, the create's included,
// so every entity's rev starts at 1; the rows of a set (ticket_assignees,
// ticket_labels, relations, links, attachments) are added and removed,
// never edited, and carry none: their ticket's rev is bumped instead;
// deleted_at is on every row with an id of its own, and a row is live
// while it is empty; an empty reference is none. tickets_fts holds an
// entry of each ticket's own and one of each live comment, written by the
// store in the transaction that writes the rows they index (indexTicket,
// indexComment), an entry's rowid made of its ticket's rowid and its
// comment's (entryRowid; tickets' and comments' ids are text, so the
// tables have implicit rowids, which only VACUUM could renumber and the
// store never runs). changes.seq is local to this database: the event's
// and the history's paging cursor, never replicated; a change row's
// identity across replicas is (actor, op_id, n), and the one order of the
// log is (at, actor, op_id, n).
var schema = append([]string{
	`CREATE TABLE schema_version (version INTEGER NOT NULL)`,
	`CREATE TABLE projects (
		id TEXT PRIMARY KEY,
		key TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		cwd TEXT NOT NULL DEFAULT '',
		tracker TEXT NOT NULL DEFAULT 'native',
		tracker_url TEXT NOT NULL DEFAULT '',
		close_requires TEXT NOT NULL DEFAULT '[]',
		next_number INTEGER NOT NULL DEFAULT 1,
		number_from INTEGER NOT NULL DEFAULT 0,
		number_to INTEGER NOT NULL DEFAULT 0,
		archived_at TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		rev INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE people (
		name TEXT PRIMARY KEY,
		created_at TEXT NOT NULL
	)`,
	`CREATE TABLE tickets (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL REFERENCES projects(id),
		number INTEGER NOT NULL,
		key TEXT NOT NULL UNIQUE,
		title TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		acceptance TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'backlog',
		priority INTEGER NOT NULL DEFAULT 0,
		horizon TEXT NOT NULL DEFAULT '',
		owner TEXT NOT NULL DEFAULT '',
		parent_id TEXT NOT NULL DEFAULT '',
		milestone_id TEXT NOT NULL DEFAULT '',
		accepted_at TEXT NOT NULL DEFAULT '',
		accepted_by TEXT NOT NULL DEFAULT '',
		accepted_for TEXT NOT NULL DEFAULT '',
		accepted_criteria TEXT NOT NULL DEFAULT '',
		accepted_note TEXT NOT NULL DEFAULT '',
		source TEXT NOT NULL DEFAULT 'native',
		external_id TEXT NOT NULL DEFAULT '',
		external_url TEXT NOT NULL DEFAULT '',
		external_snapshot TEXT NOT NULL DEFAULT '',
		authority TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		created_by TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL,
		closed_at TEXT NOT NULL DEFAULT '',
		deleted_at TEXT NOT NULL DEFAULT '',
		rev INTEGER NOT NULL DEFAULT 0,
		UNIQUE (project_id, number)
	)`,
	`CREATE TABLE ticket_assignees (
		ticket_id TEXT NOT NULL REFERENCES tickets(id),
		actor TEXT NOT NULL,
		added_at TEXT NOT NULL,
		PRIMARY KEY (ticket_id, actor)
	)`,
	`CREATE TABLE labels (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL REFERENCES projects(id),
		name TEXT NOT NULL,
		color TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		deleted_at TEXT NOT NULL DEFAULT '',
		rev INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE ticket_labels (
		ticket_id TEXT NOT NULL REFERENCES tickets(id),
		label_id TEXT NOT NULL REFERENCES labels(id),
		PRIMARY KEY (ticket_id, label_id)
	)`,
	`CREATE TABLE milestones (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL REFERENCES projects(id),
		name TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		target TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL DEFAULT 'open',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		deleted_at TEXT NOT NULL DEFAULT '',
		rev INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE relations (
		id TEXT PRIMARY KEY,
		from_id TEXT NOT NULL REFERENCES tickets(id),
		to_id TEXT NOT NULL REFERENCES tickets(id),
		kind TEXT NOT NULL,
		created_at TEXT NOT NULL,
		created_by TEXT NOT NULL DEFAULT '',
		deleted_at TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE comments (
		id TEXT PRIMARY KEY,
		ticket_id TEXT NOT NULL REFERENCES tickets(id),
		author TEXT NOT NULL,
		by TEXT NOT NULL DEFAULT '',
		body TEXT NOT NULL,
		created_at TEXT NOT NULL,
		edited_at TEXT NOT NULL DEFAULT '',
		deleted_at TEXT NOT NULL DEFAULT '',
		rev INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE attachments (
		id TEXT PRIMARY KEY,
		ticket_id TEXT NOT NULL REFERENCES tickets(id),
		name TEXT NOT NULL,
		size INTEGER NOT NULL,
		sha256 TEXT NOT NULL,
		content_type TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		created_by TEXT NOT NULL DEFAULT '',
		deleted_at TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE uploads (
		id TEXT PRIMARY KEY,
		sha256 TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`,
	`CREATE TABLE links (
		id TEXT PRIMARY KEY,
		ticket_id TEXT NOT NULL REFERENCES tickets(id),
		kind TEXT NOT NULL,
		ref TEXT NOT NULL DEFAULT '',
		url TEXT NOT NULL DEFAULT '',
		note TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		created_by TEXT NOT NULL DEFAULT '',
		deleted_at TEXT NOT NULL DEFAULT '',
		fingerprint TEXT NOT NULL DEFAULT '',
		candidate TEXT NOT NULL DEFAULT '',
		verified TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE views (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL,
		kind TEXT NOT NULL,
		query TEXT NOT NULL DEFAULT '{}',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		deleted_at TEXT NOT NULL DEFAULT '',
		rev INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE changes (
		seq INTEGER PRIMARY KEY AUTOINCREMENT,
		entity TEXT NOT NULL,
		entity_id TEXT NOT NULL,
		ticket_id TEXT NOT NULL DEFAULT '',
		project_id TEXT NOT NULL DEFAULT '',
		field TEXT NOT NULL DEFAULT '',
		op TEXT NOT NULL,
		value TEXT NOT NULL DEFAULT '',
		prior TEXT NOT NULL DEFAULT '',
		at TEXT NOT NULL,
		actor TEXT NOT NULL,
		by TEXT NOT NULL DEFAULT '',
		op_id TEXT NOT NULL,
		n INTEGER NOT NULL,
		UNIQUE (actor, op_id, n)
	)`,
	`CREATE TABLE ops (
		actor TEXT NOT NULL,
		id TEXT NOT NULL,
		intent TEXT NOT NULL,
		fingerprint TEXT NOT NULL,
		status TEXT NOT NULL,
		receipt TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		PRIMARY KEY (actor, id)
	)`,
	// The search index (query.go): a ticket's own entry, its key, title,
	// description and acceptance criteria, and an entry of each of its
	// live comments, the comment's body, so that a comment's add, edit or
	// delete rewrites that comment's entry alone (docs/plans/dispatch.md,
	// Owner decisions, 20). Two options of FTS5's beside the plan's
	// tokenizer. prefix keeps an index of every term's first one and first
	// two characters: a search's last term is a prefix, and FTS5 otherwise
	// merges the list of every term a short prefix begins, which for the
	// first letter typed in a search box is hundreds of terms
	// (BenchmarkList). content = '' with contentless_delete keeps no copy
	// of the text the tables hold already, so the rewrite of an entry
	// neither reads its old text back to unindex it nor writes a copy of
	// the new (BenchmarkIndexedWrite). The store reads only rowids from it.
	`CREATE VIRTUAL TABLE tickets_fts USING fts5(key, title, description, acceptance, body, tokenize = 'unicode61 remove_diacritics 2', prefix = '1 2', content = '', contentless_delete = 1)`,
	`CREATE INDEX tickets_project_status_updated ON tickets (project_id, status, updated_at)`,
	`CREATE INDEX tickets_project_priority ON tickets (project_id, priority)`,
	`CREATE INDEX tickets_milestone ON tickets (milestone_id)`,
	`CREATE INDEX tickets_parent ON tickets (parent_id)`,
	`CREATE INDEX tickets_owner ON tickets (owner)`,
	`CREATE INDEX tickets_deleted ON tickets (deleted_at)`,
	// The list's own (query.go), beside the plan's: a project's live
	// tickets with every column a list filters or sorts by but the title,
	// and the id, so a page's keys and its total are read from the index
	// alone and never from the rows, whose descriptions make them wide
	// (BenchmarkList).
	`CREATE INDEX tickets_list ON tickets (project_id, deleted_at, status, priority, horizon, owner, milestone_id, parent_id, updated_at, created_at, number, id)`,
	`CREATE INDEX ticket_assignees_actor ON ticket_assignees (actor)`,
	// A label's tickets: a list's label filter, and a label's delete.
	`CREATE INDEX ticket_labels_label ON ticket_labels (label_id)`,
	`CREATE INDEX relations_to ON relations (to_id)`,
	`CREATE INDEX comments_ticket_created ON comments (ticket_id, created_at)`,
	`CREATE INDEX changes_entity ON changes (entity, entity_id, seq)`,
	`CREATE INDEX changes_ticket ON changes (ticket_id, seq)`,
	`CREATE INDEX links_kind_ref ON links (kind, ref)`,
	`CREATE INDEX attachments_sha256 ON attachments (sha256)`,
	`CREATE INDEX uploads_sha256 ON uploads (sha256)`,
	plansTable,
	launchesTable,
	launchFilesTable,
}, version2Indexes...)

// tables is every table of the schema but the FTS index's own and
// SQLite's, by name: what a test lists sqlite_master against.
var tables = []string{
	"attachments", "changes", "comments", "labels", "launch_files", "launches", "links", "milestones", "ops", "people", "plans", "projects",
	"relations", "schema_version", "ticket_assignees", "ticket_labels", "tickets", "tickets_fts", "uploads", "views",
}
