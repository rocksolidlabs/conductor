package dispatch

// schemaVersion1 is version 1 of the schema, whole, as DS1 shipped it
// (internal/dispatch/schema.go at main eaed26ab, its comments left out):
// the fixture a migration test brings to the version this binary
// writes, frozen here so that a change to a version 1 table in the
// schema of today cannot pass for version 1.
var schemaVersion1 = []string{
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
		deleted_at TEXT NOT NULL DEFAULT ''
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
	`CREATE VIRTUAL TABLE tickets_fts USING fts5(key, title, description, acceptance, body, tokenize = 'unicode61 remove_diacritics 2', prefix = '1 2', content = '', contentless_delete = 1)`,
	`CREATE INDEX tickets_project_status_updated ON tickets (project_id, status, updated_at)`,
	`CREATE INDEX tickets_project_priority ON tickets (project_id, priority)`,
	`CREATE INDEX tickets_milestone ON tickets (milestone_id)`,
	`CREATE INDEX tickets_parent ON tickets (parent_id)`,
	`CREATE INDEX tickets_owner ON tickets (owner)`,
	`CREATE INDEX tickets_deleted ON tickets (deleted_at)`,
	`CREATE INDEX tickets_list ON tickets (project_id, deleted_at, status, priority, horizon, owner, milestone_id, parent_id, updated_at, created_at, number, id)`,
	`CREATE INDEX ticket_assignees_actor ON ticket_assignees (actor)`,
	`CREATE INDEX ticket_labels_label ON ticket_labels (label_id)`,
	`CREATE INDEX relations_to ON relations (to_id)`,
	`CREATE INDEX comments_ticket_created ON comments (ticket_id, created_at)`,
	`CREATE INDEX changes_entity ON changes (entity, entity_id, seq)`,
	`CREATE INDEX changes_ticket ON changes (ticket_id, seq)`,
	`CREATE INDEX links_kind_ref ON links (kind, ref)`,
	`CREATE INDEX attachments_sha256 ON attachments (sha256)`,
	`CREATE INDEX uploads_sha256 ON uploads (sha256)`,
}
