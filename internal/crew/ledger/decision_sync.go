package ledger

// The tables version 16 adds for sync's seams (docs/plans/sync-seams.md,
// row OS-3a, the step before OS-3). Nothing writes or reads either yet.

// decisionStatementsTable keeps the statement a held answer carries
// (version 16): one row for each response of a decision whose authority
// names a statement, keyed by the decision record's n and the response's
// k, as decision_responses is, the statement whole as text. The authority
// holds only its SHA-256, so the statement need not fit DR1's bound of an
// authority. A row is written in the transaction of the response it
// belongs to and is deleted with its decision (ON DELETE CASCADE), as a
// pruned run's decisions are: a decision handed on to another run keeps
// its number, and so its rows.
const decisionStatementsTable = `CREATE TABLE decision_statements (
		decision INTEGER NOT NULL REFERENCES decisions(n) ON DELETE CASCADE,
		k INTEGER NOT NULL,
		statement TEXT NOT NULL,
		PRIMARY KEY (decision, k)
	)`

// decisionOutputTable keeps a decision's copied-output verdicts (version
// 16): one row for each free-text part of a decision (its request's
// fields, each response's text and rationale, each receipt's and the
// application's words), keyed by the decision record's n and the part's
// name, with its verdict and where a part found to quote output is cut (-1
// for none), written in the transaction that writes the part, so a
// document built later, after the output it quoted is gone, still cuts
// it: a row outlives the output it was judged against, and is deleted
// with its decision (ON DELETE CASCADE), whose document is never built
// again; a decision handed on to another run keeps its number, and so its
// rows.
const decisionOutputTable = `CREATE TABLE decision_output (
		decision INTEGER NOT NULL REFERENCES decisions(n) ON DELETE CASCADE,
		part TEXT NOT NULL,
		verdict TEXT NOT NULL,
		cut_at INTEGER NOT NULL DEFAULT -1,
		PRIMARY KEY (decision, part)
	)`
