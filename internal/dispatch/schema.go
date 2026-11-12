package dispatch

import (
	"context"
	"database/sql"
	"slices"
)

// Version is the schema this binary writes. A database of a later version
// opens read-only; an earlier one is brought to it (migrations).
// Version 1 is DS1's (docs/plans/dispatch.md); version 2 adds a ticket's
// plan revisions and launches and three columns of a link
// (docs/plans/dispatch-evidence.md, Program item DS2); version 3 adds a
// ticket's reviews, their findings and the findings' marks
// (docs/plans/review-repair.md, Program item RV1); version 4 adds a
// ticket's linked issue in the columns DS1 reserved, the reads of linked
// GitHub items, the store's instance and the publications' table, and a
// ticket's closed_on (docs/plans/tracker-linking.md, Program item IN1a);
// version 5 adds a person's captures of a preview, their annotations and
// their sends (docs/plans/browser-evidence.md, Program item PV1).
const Version = captureSchema

// captureSchema is the version that made the captures' tables: the one
// place a later version renumbers them (the migration's key, the reads of
// an older database, the version 4 test).
const captureSchema = 5

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
//
// Version 2 to 3 makes the three tables of reviews, their findings and
// the findings' marks and their five indexes, and reads or rewrites no
// row: a version 2 database holds no review, so nothing is derived and no
// migrate row is written.
//
// Version 3 to 4 makes four tables (the links' and the linked issues'
// reads, the store's instance and the publications) and their four
// indexes, adds a ticket's closed_on, empty on every version 3 row, and
// writes the instance's one row (migrationsGo). It reads and rewrites no
// other row: every version 3 ticket has source native and the other
// reserved columns empty, since nothing wrote them, so nothing is derived
// and no migrate row is written.
//
// Version 4 to 5 makes the seven tables of captures, their annotations'
// versions and sets, their sends, the sends' outcomes and cancels and the
// files they hold, and their five indexes, and reads or rewrites no row: a
// version 4 database holds no capture.
var migrations = map[int][]string{
	captureSchema - 1: append(slices.Clone(captureTables), captureIndexes...),
	1: append([]string{
		plansTable,
		launchesTable,
		launchFilesTable,
		`ALTER TABLE links ADD COLUMN fingerprint TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE links ADD COLUMN candidate TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE links ADD COLUMN verified TEXT NOT NULL DEFAULT ''`,
	}, version2Indexes...),
	2: append([]string{
		reviewsTable,
		reviewFindingsTable,
		findingMarksTable,
	}, version3Indexes...),
	3: append([]string{
		linkReadsTable,
		ticketReadsTable,
		storeInstanceTable,
		publicationsTable,
		`ALTER TABLE tickets ADD COLUMN closed_on TEXT NOT NULL DEFAULT ''`,
	}, version4Indexes...),
}

// migrationsGo run after a version's statements, in the same transaction:
// what SQL alone cannot work out.
var migrationsGo = map[int]func(context.Context, *sql.Tx) error{
	3: writeInstance,
}

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

// reviewsTable is a ticket's reviews (Program item RV1, reviews.go): a
// person's batch of notes on one candidate, <run>/<task>/<n>@<commit>,
// against a base, a full commit, both fixed at the submit with the
// verdict and the summary, its fingerprint the submitting operation's.
// The app_* fields are its application as the ledger's owner recorded
// it: none for a comment or an approval, pending (its try) then applied
// (the run, the command, the run's revision after it, the repair
// attempt's number) or refused (why) for a request, nothing else.
const reviewsTable = `CREATE TABLE reviews (
		id TEXT PRIMARY KEY,
		ticket_id TEXT NOT NULL REFERENCES tickets(id),
		candidate TEXT NOT NULL,
		base TEXT NOT NULL,
		verdict TEXT NOT NULL,
		summary TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		created_by TEXT NOT NULL DEFAULT '',
		actor TEXT NOT NULL,
		via TEXT NOT NULL DEFAULT '',
		fingerprint TEXT NOT NULL DEFAULT '',
		app_state TEXT NOT NULL DEFAULT 'none',
		app_try INTEGER NOT NULL DEFAULT 0,
		app_run TEXT NOT NULL DEFAULT '',
		app_command TEXT NOT NULL DEFAULT '',
		app_revision INTEGER NOT NULL DEFAULT 0,
		app_attempt INTEGER NOT NULL DEFAULT 0,
		app_why TEXT NOT NULL DEFAULT '',
		app_at TEXT NOT NULL DEFAULT '',
		rev INTEGER NOT NULL DEFAULT 0
	)`

// reviewFindingsTable is a review's findings, each an entity of its own
// with its place k (1 to MaxReviewFindings) in the batch, immutable once
// made: lines and check_ref are JSON (the lines as the person saw them,
// a check finding's receipt as the server read it). ticket_id is the
// review's, kept so a ticket's findings are read by an index.
const reviewFindingsTable = `CREATE TABLE review_findings (
		id TEXT PRIMARY KEY,
		review_id TEXT NOT NULL REFERENCES reviews(id),
		ticket_id TEXT NOT NULL REFERENCES tickets(id),
		k INTEGER NOT NULL,
		kind TEXT NOT NULL,
		intent TEXT NOT NULL,
		severity TEXT NOT NULL DEFAULT '',
		path TEXT NOT NULL DEFAULT '',
		side TEXT NOT NULL DEFAULT '',
		from_line INTEGER NOT NULL DEFAULT 0,
		to_line INTEGER NOT NULL DEFAULT 0,
		anchor_commit TEXT NOT NULL DEFAULT '',
		lines TEXT NOT NULL DEFAULT '[]',
		message TEXT NOT NULL DEFAULT '',
		resolution TEXT NOT NULL DEFAULT '',
		check_ref TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL
	)`

// findingMarksTable is a finding's marks, a log: each a person's verdict
// on the finding against a candidate, with what the page showed of its
// lines there (located, the server's JSON), never edited. op_id is the
// marking operation's, which with created_at and actor places the mark in
// the log's one order, the latest deciding the finding's state.
const findingMarksTable = `CREATE TABLE finding_marks (
		id TEXT PRIMARY KEY,
		finding_id TEXT NOT NULL REFERENCES review_findings(id),
		ticket_id TEXT NOT NULL REFERENCES tickets(id),
		candidate TEXT NOT NULL,
		verdict TEXT NOT NULL,
		located TEXT NOT NULL DEFAULT '',
		note TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		created_by TEXT NOT NULL DEFAULT '',
		actor TEXT NOT NULL,
		via TEXT NOT NULL DEFAULT '',
		op_id TEXT NOT NULL,
		fingerprint TEXT NOT NULL DEFAULT ''
	)`

// linkReadsTable is the last read of the GitHub item a live link names
// (Program item IN1a, reads.go): a cache, derived and local, never a
// changes row, never replicated; a link removed drops its row in the same
// transaction. checks is JSON (at most MaxLinkChecks at the item's head
// commit), checks_incomplete the words of why the set is not whole.
const linkReadsTable = `CREATE TABLE link_reads (
		link_id TEXT PRIMARY KEY REFERENCES links(id),
		ticket_id TEXT NOT NULL REFERENCES tickets(id),
		system TEXT NOT NULL DEFAULT '',
		host TEXT NOT NULL DEFAULT '',
		kind TEXT NOT NULL DEFAULT '',
		node_id TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL DEFAULT '',
		merged INTEGER NOT NULL DEFAULT 0,
		merged_at TEXT NOT NULL DEFAULT '',
		merge_sha TEXT NOT NULL DEFAULT '',
		head_sha TEXT NOT NULL DEFAULT '',
		checks TEXT NOT NULL DEFAULT '[]',
		checks_incomplete TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL DEFAULT '',
		read_at TEXT NOT NULL DEFAULT '',
		issue_etag TEXT NOT NULL DEFAULT '',
		pull_etag TEXT NOT NULL DEFAULT '',
		error TEXT NOT NULL DEFAULT '',
		error_at TEXT NOT NULL DEFAULT ''
	)`

// ticketReadsTable is when a ticket's linked issue was last read, for the
// identity it was read for, and the read's validators (external.go):
// derived and local as link_reads is; unlinking drops it.
const ticketReadsTable = `CREATE TABLE ticket_reads (
		ticket_id TEXT PRIMARY KEY REFERENCES tickets(id),
		external_id TEXT NOT NULL,
		updated_at TEXT NOT NULL DEFAULT '',
		read_at TEXT NOT NULL DEFAULT '',
		read_by TEXT NOT NULL DEFAULT '',
		issue_etag TEXT NOT NULL DEFAULT '',
		pull_etag TEXT NOT NULL DEFAULT '',
		error TEXT NOT NULL DEFAULT '',
		error_at TEXT NOT NULL DEFAULT ''
	)`

// storeInstanceTable is this database's instance, one row of 32 hex
// digits, written by the create or the migration and written again with a
// new value by a restore (Reissue), so a restored copy is never the
// instance it was exported from: derived and local, never a changes row,
// never replicated.
const storeInstanceTable = `CREATE TABLE store_instance (
		id TEXT PRIMARY KEY
	)`

// publicationsTable is a ticket's publications, each one comment
// Conductor posts to GitHub for a person (docs/plans/tracker-linking.md,
// "publications"): made here by version 4, written by the operations of
// a later row of the plan, which this version's store has none of.
const publicationsTable = `CREATE TABLE publications (
		id TEXT PRIMARY KEY,
		ticket_id TEXT NOT NULL REFERENCES tickets(id),
		target TEXT NOT NULL,
		target_kind TEXT NOT NULL,
		target_url TEXT NOT NULL DEFAULT '',
		target_ref TEXT NOT NULL DEFAULT '',
		target_node_id TEXT NOT NULL,
		target_space_id TEXT NOT NULL,
		instance TEXT NOT NULL,
		marker TEXT NOT NULL,
		body TEXT NOT NULL,
		post_sha256 TEXT NOT NULL,
		parts TEXT NOT NULL DEFAULT '[]',
		close_issue TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL DEFAULT 'draft',
		tries INTEGER NOT NULL DEFAULT 0,
		attempt INTEGER NOT NULL DEFAULT 0,
		find_since TEXT NOT NULL DEFAULT '',
		sent_as_id TEXT NOT NULL DEFAULT '',
		sent_as_login TEXT NOT NULL DEFAULT '',
		remote_id TEXT NOT NULL DEFAULT '',
		remote_url TEXT NOT NULL DEFAULT '',
		close_state TEXT NOT NULL DEFAULT '',
		close_tries INTEGER NOT NULL DEFAULT 0,
		close_error TEXT NOT NULL DEFAULT '',
		error TEXT NOT NULL DEFAULT '',
		sending_at TEXT NOT NULL DEFAULT '',
		sent_at TEXT NOT NULL DEFAULT '',
		sent_by TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		created_by TEXT NOT NULL DEFAULT '',
		fingerprint TEXT NOT NULL DEFAULT '',
		rev INTEGER NOT NULL DEFAULT 0
	)`

// captureTables is version 5's tables (captures.go, Program item PV1). A
// capture's page, origin and image are written once with it; annotations
// points at its live annotation set ({rev, set}). An annotation's version
// is content alone, keyed by its SHA-256, and a set is a revision of its
// own with its members' hashes in their order: both written once, never
// changed, removed only with the live capture they belong to. A send's
// input is written once, its outcomes and its cancels appended, its state
// read from the outcomes (sendState), never kept. capture_files is every
// content file a live capture (holder capture) or a send (holder its id)
// holds, which the sweep keeps while the row is there.
var captureTables = []string{
	`CREATE TABLE captures (
		id TEXT PRIMARY KEY,
		rev INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		created_by TEXT NOT NULL DEFAULT '',
		actor TEXT NOT NULL,
		via TEXT NOT NULL,
		fingerprint TEXT NOT NULL DEFAULT '',
		url TEXT NOT NULL,
		title TEXT NOT NULL DEFAULT '',
		viewport TEXT NOT NULL,
		selected_at TEXT NOT NULL,
		saved_at TEXT NOT NULL,
		origin TEXT NOT NULL,
		run_id TEXT NOT NULL DEFAULT '',
		task_id TEXT NOT NULL DEFAULT '',
		attempt_n INTEGER NOT NULL DEFAULT 0,
		tree TEXT NOT NULL DEFAULT '',
		commit_id TEXT NOT NULL DEFAULT '',
		changed INTEGER NOT NULL DEFAULT 0,
		changed_paths TEXT NOT NULL DEFAULT '[]',
		changed_cut INTEGER NOT NULL DEFAULT 0,
		content TEXT NOT NULL DEFAULT '',
		mutable INTEGER NOT NULL DEFAULT 0,
		candidate TEXT NOT NULL DEFAULT '',
		where_ran TEXT NOT NULL DEFAULT '',
		scope TEXT NOT NULL DEFAULT '{}',
		drift TEXT NOT NULL DEFAULT '',
		serving TEXT NOT NULL DEFAULT '{}',
		image_sha256 TEXT NOT NULL DEFAULT '',
		image_width INTEGER NOT NULL DEFAULT 0,
		image_height INTEGER NOT NULL DEFAULT 0,
		image_bytes INTEGER NOT NULL DEFAULT 0,
		image_how TEXT NOT NULL,
		image_why TEXT NOT NULL DEFAULT '',
		image_scale REAL NOT NULL DEFAULT 0,
		epoch INTEGER NOT NULL DEFAULT 0,
		retake_of TEXT NOT NULL DEFAULT '',
		retake_via TEXT NOT NULL DEFAULT '',
		deleted_at TEXT NOT NULL DEFAULT '',
		annotations TEXT NOT NULL DEFAULT '{}'
	)`,
	`CREATE TABLE capture_annotations (
		capture_id TEXT NOT NULL REFERENCES captures(id),
		sha256 TEXT NOT NULL,
		bytes INTEGER NOT NULL,
		kind TEXT NOT NULL,
		note TEXT NOT NULL DEFAULT '',
		rect TEXT NOT NULL,
		element TEXT NOT NULL DEFAULT '',
		observed TEXT NOT NULL DEFAULT '',
		sources TEXT NOT NULL DEFAULT '[]',
		PRIMARY KEY (capture_id, sha256)
	)`,
	`CREATE TABLE capture_annotation_sets (
		capture_id TEXT NOT NULL REFERENCES captures(id),
		id TEXT NOT NULL,
		rev INTEGER NOT NULL,
		digest TEXT NOT NULL,
		members TEXT NOT NULL,
		at TEXT NOT NULL,
		actor TEXT NOT NULL,
		op_id TEXT NOT NULL,
		PRIMARY KEY (capture_id, id)
	)`,
	`CREATE TABLE capture_sends (
		id TEXT PRIMARY KEY,
		capture_id TEXT NOT NULL REFERENCES captures(id),
		rev INTEGER NOT NULL,
		kind TEXT NOT NULL,
		asked TEXT NOT NULL DEFAULT '{}',
		digest TEXT NOT NULL,
		request TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		created_by TEXT NOT NULL DEFAULT '',
		actor TEXT NOT NULL,
		op_id TEXT NOT NULL,
		fingerprint TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE capture_send_outcomes (
		send_id TEXT NOT NULL REFERENCES capture_sends(id),
		id TEXT NOT NULL,
		kind TEXT NOT NULL,
		target TEXT NOT NULL DEFAULT '',
		files TEXT NOT NULL DEFAULT '[]',
		why TEXT NOT NULL DEFAULT '',
		at TEXT NOT NULL,
		actor TEXT NOT NULL,
		op_id TEXT NOT NULL,
		PRIMARY KEY (send_id, id)
	)`,
	`CREATE TABLE capture_send_cancels (
		send_id TEXT NOT NULL REFERENCES capture_sends(id),
		op_id TEXT NOT NULL,
		at TEXT NOT NULL,
		actor TEXT NOT NULL,
		PRIMARY KEY (send_id, op_id)
	)`,
	`CREATE TABLE capture_files (
		capture_id TEXT NOT NULL REFERENCES captures(id),
		sha256 TEXT NOT NULL,
		holder TEXT NOT NULL,
		PRIMARY KEY (capture_id, sha256, holder)
	)`,
}

// captureIndexes is the indexes version 5 adds: an attempt's captures, the
// captures in their order, a capture's sends in theirs, the outcomes by
// their target (a ticket's captures), and the files by their hash (the
// sweep). None is unique beyond the keys.
var captureIndexes = []string{
	`CREATE INDEX captures_attempt ON captures (run_id, task_id, attempt_n)`,
	`CREATE INDEX captures_created ON captures (created_at)`,
	`CREATE INDEX capture_sends_capture_created ON capture_sends (capture_id, created_at)`,
	`CREATE INDEX capture_send_outcomes_target ON capture_send_outcomes (kind, target)`,
	`CREATE INDEX capture_files_sha256 ON capture_files (sha256)`,
}

// version4Indexes is the indexes version 4 adds: the tickets by their
// linked item's identity, a ticket's links' reads, a ticket's
// publications in their order, and the publications by their state. None
// is unique: one ticket per linked item is a rule of the local write.
var version4Indexes = []string{
	`CREATE INDEX tickets_external ON tickets (external_id)`,
	`CREATE INDEX link_reads_ticket ON link_reads (ticket_id)`,
	`CREATE INDEX publications_ticket_created ON publications (ticket_id, created_at)`,
	`CREATE INDEX publications_state ON publications (state)`,
}

// version3Indexes is the indexes version 3 adds: a ticket's reviews in
// their order, the reviews by their application's state (the reconcile's
// pending ones), a review's findings in their order, a ticket's findings,
// and a finding's marks in their order. None is unique.
var version3Indexes = []string{
	`CREATE INDEX reviews_ticket_created ON reviews (ticket_id, created_at)`,
	`CREATE INDEX reviews_app_state ON reviews (app_state)`,
	`CREATE INDEX review_findings_review ON review_findings (review_id, k)`,
	`CREATE INDEX review_findings_ticket ON review_findings (ticket_id)`,
	`CREATE INDEX finding_marks_finding_created ON finding_marks (finding_id, created_at)`,
}

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

// schema is version 5, whole: every table of docs/plans/dispatch.md's
// schema table, docs/plans/dispatch-evidence.md's (plans, launches,
// launch_files, and a link's three columns, last of its table as the
// migration adds them), docs/plans/review-repair.md's (reviews,
// review_findings, finding_marks) and docs/plans/tracker-linking.md's
// (link_reads, ticket_reads, store_instance, publications, and a ticket's
// closed_on, last of its columns as the migration adds it) and
// docs/plans/browser-evidence.md's (captureTables), the FTS index included, its indexes and two of the
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
		closed_on TEXT NOT NULL DEFAULT '',
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
	reviewsTable,
	reviewFindingsTable,
	findingMarksTable,
	linkReadsTable,
	ticketReadsTable,
	storeInstanceTable,
	publicationsTable,
}, append(append(append(append(slices.Clone(version2Indexes), version3Indexes...), version4Indexes...), captureTables...), captureIndexes...)...)

// tables is every table of the schema but the FTS index's own and
// SQLite's, by name: what a test lists sqlite_master against.
var tables = []string{
	"attachments", "capture_annotation_sets", "capture_annotations", "capture_files", "capture_send_cancels", "capture_send_outcomes", "capture_sends",
	"captures", "changes", "comments", "finding_marks", "labels", "launch_files", "launches", "link_reads", "links", "milestones", "ops",
	"people", "plans", "projects", "publications", "relations", "review_findings", "reviews", "schema_version", "store_instance",
	"ticket_assignees", "ticket_labels", "ticket_reads", "tickets", "tickets_fts", "uploads", "views",
}
