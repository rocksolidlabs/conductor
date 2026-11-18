package ledger

// runOriginTable is where a run was launched from (version 13), one row
// per run, removed with it: the launching provider's registered name,
// the launch's operation id (empty for none), the provider's reference
// and label, a Dispatch project, the subject and its link, an earlier run
// of the same provider, the start commit asked for, the instant the run
// is stopped and the limit it was given in seconds, the digest the launch
// was held to and the agents' selections (JSON). No row of an older table is rewritten to
// make it, and no run before this version has a row. The columns' bounds
// are their writer's; no CHECK holds them.
const runOriginTable = `CREATE TABLE run_origin (
		run_id TEXT NOT NULL PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
		provider TEXT NOT NULL,
		op_id TEXT NOT NULL DEFAULT '',
		ref TEXT NOT NULL,
		label TEXT NOT NULL DEFAULT '',
		project TEXT NOT NULL DEFAULT '',
		subject TEXT NOT NULL DEFAULT '',
		subject_url TEXT NOT NULL DEFAULT '',
		prior TEXT NOT NULL DEFAULT '',
		start_commit TEXT NOT NULL DEFAULT '',
		deadline TEXT NOT NULL DEFAULT '',
		limit_seconds INTEGER NOT NULL DEFAULT 0,
		resolved_digest TEXT NOT NULL DEFAULT '',
		agents TEXT NOT NULL DEFAULT ''
	)`

// runOriginOpIndex keeps one run per provider's operation id: rows with
// an empty operation id are not keyed, so any number of a provider's runs
// may have none. Its rule is part of what the table means, not a read's
// speed, so it is the version's statement, never an access path
// (accessPaths).
const runOriginOpIndex = `CREATE UNIQUE INDEX run_origin_op ON run_origin(provider, op_id) WHERE op_id != ''`
