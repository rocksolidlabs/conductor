package ledger

// attemptContextTable is the record of the context each attempt was given
// (version 15, docs/plans/context-seams.md, CT-3a): one row for each block
// a context provider gave an attempt, at its start (phase task) or by a
// lookup while it worked (phase lookup), never its text, which the data
// directory's context file keeps. seq numbers an attempt's blocks from 1
// across both phases; source is the provider's name; title, ref and pack
// are the block's title, what it is about and the provider's mark of the
// record it came from ("" for none); sha256 is the body's digest in
// lower-case hex and bytes its length; at is when it was recorded (RFC
// 3339, UTC). The key is the attempt and seq, so an attempt's rows are
// read in order by the key's prefix (its run and task for a task's
// attempts, the run for a run's candidates, the attempt for the next seq)
// and one block by the whole key, with no index of its own. A row goes
// with its attempt when its run is pruned, as a preview does (ON DELETE
// CASCADE). The bounds of a row's fields are its writer's, as ext's
// bounds give them; only the phase is held by a CHECK, since a row's
// meaning rests on it. No attempt before this version was given a
// recorded block, and nothing writes or reads the table yet.
const attemptContextTable = `CREATE TABLE attempt_context (
		run_id TEXT NOT NULL,
		task_id TEXT NOT NULL,
		n INTEGER NOT NULL,
		seq INTEGER NOT NULL,
		phase TEXT NOT NULL CHECK (phase IN ('task', 'lookup')),
		source TEXT NOT NULL,
		title TEXT NOT NULL,
		ref TEXT NOT NULL DEFAULT '',
		pack TEXT NOT NULL DEFAULT '',
		sha256 TEXT NOT NULL,
		bytes INTEGER NOT NULL,
		at TEXT NOT NULL,
		PRIMARY KEY (run_id, task_id, n, seq),
		FOREIGN KEY (run_id, task_id, n) REFERENCES attempts(run_id, task_id, n) ON DELETE CASCADE
	)`
