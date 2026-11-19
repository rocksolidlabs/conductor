package ledger

// decisionOutboxTable is the outcome outbox (version 14): one row for each
// decision record that ever carried a decision router's policy, written in
// the transaction that gives the record its final state (applied,
// superseded, cancelled, expired), kept until the router acknowledges it.
// seq orders the rows and is never reused (AUTOINCREMENT); decision is the
// record's n, with no foreign key, so a row outlives a run pruned since;
// outcome is the outcome as the server encodes it (JSON), self-contained,
// so nothing in it is read from another table when it is served; at is
// when it was written (RFC 3339, UTC). A record under the server's own
// policy alone writes no row, so a server with no router keeps none, and
// no row of an older table is rewritten to make it. The outcome's bound is
// its writer's; no CHECK holds it. Nothing writes or reads the table yet.
const decisionOutboxTable = `CREATE TABLE decision_outbox (
		seq INTEGER PRIMARY KEY AUTOINCREMENT,
		decision INTEGER NOT NULL,
		outcome TEXT NOT NULL,
		at TEXT NOT NULL
	)`

// decisionOutboxIndex keeps one outbox row for a decision record. Its rule
// is part of what the table means, not a read's speed, so it is the
// version's statement, never an access path (accessPaths).
const decisionOutboxIndex = `CREATE UNIQUE INDEX decision_outbox_decision ON decision_outbox(decision)`
