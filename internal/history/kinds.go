package history

import "slices"

// The classes of evidence (docs/plans/history.md, "The kinds and their
// class"), which never mix: what was said, what a person decided, what
// the server checked, and the plain records the three are about.
const (
	ClassSaid    = "said"    // Conversation: a person's or an agent's words as Conductor received them
	ClassDecided = "decided" // Decision: a person's act the server recorded with a receipt
	ClassChecked = "checked" // Checked: a receipt of something the server ran on an exact commit
	ClassRecord  = "record"  // the stores' own objects
)

// kinds is the one table of an entry's kinds and their classes: an
// entry's class comes from what made it, never from its words, how often
// they recur or who repeats them. It is closed: a kind it does not hold
// is refused (ErrKind), so a PR that indexes a new record changes this
// table, its bounds (bodyBound) and TestClassComesFromTheKindAlone
// together. A review finding stays said; its verified resolution is an
// entry of its own checked kind (review.resolved), never the finding's
// class changed.
var kinds = map[string]string{
	// The stores' own objects.
	"session": ClassRecord,
	"run":     ClassRecord,
	"task":    ClassRecord,
	"attempt": ClassRecord,
	"ticket":  ClassRecord,
	"plan":    ClassRecord,
	"launch":  ClassRecord,
	"step":    ClassRecord, // a project manifest's setup or service step that failed
	// A DR1 decision request (docs/plans/decisions.md): its question,
	// its subject and its scope, as the ledger keeps it.
	"decision": ClassRecord,

	// Words, as Conductor received them.
	"session.typed":      ClassSaid,
	"session.answer":     ClassSaid,
	"session.question":   ClassSaid,
	"session.report":     ClassSaid,
	"session.file":       ClassSaid,
	"session.chat":       ClassSaid,
	"attempt.result":     ClassSaid, // a submitted summary and issues
	"review.verdict":     ClassSaid, // an agent reviewer's, though the engine acts on it
	"review.finding":     ClassSaid, // an agent reviewer's, or a person's in an RV1 review batch
	"attempt.checkpoint": ClassSaid,
	"attempt.blocker":    ClassSaid,
	"message":            ClassSaid,
	"comment":            ClassSaid,
	"brief.item":         ClassSaid, // Brakeman's account, citing the records it names

	// A person's recorded acts.
	"command":           ClassDecided, // pause, unpause, cancel, retry, accept, accept anyway
	"plan.approved":     ClassDecided,
	"plan.withdrawn":    ClassDecided,
	"plan.superseded":   ClassDecided,
	"ticket.accepted":   ClassDecided,
	"ticket.unaccepted": ClassDecided,
	"ticket.closed":     ClassDecided,
	// A response a DR1 decision accepted, with its responder and
	// rationale: the ledger's receipt of it.
	"decision.response": ClassDecided,
	// A person's review batch of a candidate (RV1,
	// docs/plans/review-repair.md): Request changes, Comment or Approve,
	// bound to its candidate and base, by the reviewer.
	"review.batch": ClassDecided,

	// Receipts of what the server ran.
	"check":              ClassChecked, // a task's check
	"verify":             ClassChecked, // a project manifest's verify step
	"candidate.verified": ClassChecked, // a result commit whose every check passed
	"delivery.verified":  ClassChecked, // a delivery whose candidate's receipt is kept
	// A finding's resolution verified against a later candidate of another
	// commit, as RV1's mark of it says (a resolved mark is refused on the
	// finding's own commit): the finding itself stays said.
	"review.resolved": ClassChecked,
}

// classOf is the class of a kind, the only place one is decided; ok is
// false for a kind the table does not hold.
func classOf(kind string) (class string, ok bool) {
	class, ok = kinds[kind]
	return class, ok
}

// Kinds is every kind of the table, sorted.
func Kinds() []string {
	out := make([]string, 0, len(kinds))
	for k := range kinds {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// receiptKinds are the kinds Similar ranks: a check's or a verify step's
// receipt, each of one command (its subject).
var receiptKinds = []string{"check", "verify"}

// needsCommit are the checked kinds bound to the commit they checked: an
// entry of one with no commit is refused.
var needsCommit = []string{"check", "verify", "candidate.verified", "review.resolved"}
