package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A delivery's candidate (Program item DS2, docs/plans/dispatch-evidence.md,
// "A delivery's candidate"): a delivery link may name the candidate it
// delivers, an attempt's result commit, as <run>/<task>/<n>@<commit>, the
// ledger's own reference to the attempt with the commit. The link keeps
// that candidate's verification receipt as the API read it from the
// ledger when the link was added (Verified), the one fact of a run's
// evidence Dispatch records, so that the close policy's verified is a
// function of Dispatch's own rows, readable on a replica with no ledger.
// A finished receipt never changes, so the recorded one stays true. The
// receipt is an argument of AddLink apart from the request: computed
// evidence, never part of the operation's fingerprint.

// The bounds of a verification receipt's parts.
const (
	// MaxVerifiedReceipts bounds a verification receipt's receipts: a
	// task's eight checks and its manifest's eight verify steps.
	MaxVerifiedReceipts = 16
	// MaxVerifiedWhere bounds where a receipt's check ran, in bytes.
	MaxVerifiedWhere = 32
)

// The phases a verification receipt's receipt is of: a task's check, or a
// project manifest's verify step (the ledger's PhaseCheck and PhaseVerify).
const (
	PhaseCheck  = "check"
	PhaseVerify = "verify"
)

var (
	// whereIDPattern is where a check ran: a runtime's name, native until
	// Program item F.
	whereIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)
	// runSuffixPattern is a run id's suffix after the crew's id and its
	// dash: eight hex digits (internal/crew's ValidRunID).
	runSuffixPattern = regexp.MustCompile(`^[0-9a-f]{8}$`)
	// attemptPattern is an attempt's number: 1 or more, no leading zero.
	attemptPattern = regexp.MustCompile(`^[1-9][0-9]{0,5}$`)
)

// Candidate is a delivery's candidate, parsed: the run, the task, the
// attempt and its result commit.
type Candidate struct {
	Run     string
	Task    string
	Attempt int
	Commit  string
}

// String is the candidate as a link keeps it: <run>/<task>/<n>@<commit>.
func (c Candidate) String() string {
	return c.Run + "/" + c.Task + "/" + strconv.Itoa(c.Attempt) + "@" + c.Commit
}

// Name is the candidate as words name it: <task>#<n>.
func (c Candidate) Name() string { return c.Task + "#" + strconv.Itoa(c.Attempt) }

// ParseCandidate reads a delivery's candidate, <run>/<task>/<n>@<commit>:
// a run id of the engine's shape (a crew's id, a dash, eight hex digits),
// a task id, an attempt's number from 1, and a full commit id in lower-case
// hex (SHA-1 or SHA-256), at most MaxLinkCandidate bytes in all. An error
// is a Refusal, bound past the bound and invalid otherwise.
func ParseCandidate(s string) (Candidate, error) {
	if len(s) > MaxLinkCandidate {
		return Candidate{}, refuse(RefusalBound, "the candidate is %d bytes, more than %d", len(s), MaxLinkCandidate)
	}
	bad := refuse(RefusalInvalid, "the candidate %q is not <run>/<task>/<n>@<commit>, the commit in full", s)
	ref, commit, ok := strings.Cut(s, "@")
	if !ok || !objectIDPattern.MatchString(commit) {
		return Candidate{}, bad
	}
	parts := strings.Split(ref, "/")
	if len(parts) != 3 {
		return Candidate{}, bad
	}
	run, task, n := parts[0], parts[1], parts[2]
	i := strings.LastIndexByte(run, '-')
	if i <= 0 || !crewIDPattern.MatchString(run[:i]) || !runSuffixPattern.MatchString(run[i+1:]) ||
		!taskIDPattern.MatchString(task) || !attemptPattern.MatchString(n) {
		return Candidate{}, bad
	}
	attempt, _ := strconv.Atoi(n)
	return Candidate{Run: run, Task: task, Attempt: attempt, Commit: commit}, nil
}

// Verified is a delivery's verification receipt: when the API read it,
// whether the candidate passed (its attempt succeeded and every receipt
// finished with exit 0, stable; false for a candidate with no check at
// all), the attempt, and the receipts of its checks and verify steps run
// against the candidate's commit, each with where it ran, so a recorded
// receipt tells native checks from a guest's once Program item F lands.
// At most MaxLinkVerified bytes as encoded.
type Verified struct {
	At       time.Time         `json:"at"`
	Passed   bool              `json:"passed"`
	Attempt  int               `json:"attempt"`
	Receipts []VerifiedReceipt `json:"receipts"`
}

// VerifiedReceipt is one receipt of a verification receipt: a check's or
// a verify step's run (phase, id, k: the ledger's identity of the row
// within its attempt), where it ran, its exit when it finished, whether
// it was stable, and its log's SHA-256.
type VerifiedReceipt struct {
	Phase     string `json:"phase"`
	ID        string `json:"id"`
	K         int    `json:"k"`
	Where     string `json:"where"`
	Exit      *int   `json:"exit,omitempty"`
	Stable    bool   `json:"stable"`
	LogSHA256 string `json:"logSha256,omitempty"`
}

// passes says the receipt is a pass: it finished with exit 0, stable.
func (r VerifiedReceipt) passes() bool { return r.Exit != nil && *r.Exit == 0 && r.Stable }

// what is the receipt's check as words name it: "the check test", "the
// verify step lint".
func (r VerifiedReceipt) what() string {
	if r.Phase == PhaseVerify {
		return "the verify step " + r.ID
	}
	return "the check " + r.ID
}

// encodeVerified checks a verification receipt for the candidate c and
// encodes it as its link keeps it: its attempt c's, at most
// MaxVerifiedReceipts receipts, each of a phase, a check id, a k from 0,
// where it ran and a log hash of their shapes, passed only when it has a
// receipt and every one passes. An error is a Refusal: invalid, or bound
// past MaxLinkVerified as encoded.
func encodeVerified(c Candidate, v Verified) (string, error) {
	switch {
	case v.At.IsZero():
		return "", refuse(RefusalInvalid, "the candidate's verification receipt has no time")
	case v.Attempt != c.Attempt:
		return "", refuse(RefusalInvalid, "the verification receipt is attempt %d's, not the candidate's attempt %d", v.Attempt, c.Attempt)
	case len(v.Receipts) > MaxVerifiedReceipts:
		return "", refuse(RefusalBound, "at most %d receipts in a verification receipt; this has %d", MaxVerifiedReceipts, len(v.Receipts))
	case v.Passed && len(v.Receipts) == 0:
		return "", refuse(RefusalInvalid, "a candidate with no check is not verified")
	}
	for i, r := range v.Receipts {
		switch {
		case r.Phase != PhaseCheck && r.Phase != PhaseVerify:
			return "", refuse(RefusalInvalid, "receipt %d: the phase %q is neither check nor verify", i, r.Phase)
		case !taskIDPattern.MatchString(r.ID):
			return "", refuse(RefusalInvalid, "receipt %d: %q is no check's id", i, r.ID)
		case r.K < 0:
			return "", refuse(RefusalInvalid, "receipt %d: k is %d", i, r.K)
		case !whereIDPattern.MatchString(r.Where):
			return "", refuse(RefusalInvalid, "receipt %d: where it ran, %q, is not a runtime's name of at most %d bytes", i, r.Where, MaxVerifiedWhere)
		case r.LogSHA256 != "" && !sha256Pattern.MatchString(r.LogSHA256):
			return "", refuse(RefusalInvalid, "receipt %d: the log's SHA-256 is not one", i)
		case v.Passed && !r.passes():
			return "", refuse(RefusalInvalid, "receipt %d: %s did not pass, so the candidate did not", i, r.what())
		}
	}
	if v.Receipts == nil {
		v.Receipts = []VerifiedReceipt{}
	}
	value, err := encodeValue(v)
	if err != nil {
		return "", err
	}
	if len(value) > MaxLinkVerified {
		return "", refuse(RefusalBound, "the verification receipt is %d bytes, more than %d", len(value), MaxLinkVerified)
	}
	return value, nil
}

// checkCandidate checks the candidate a link names and the receipt it
// comes with, and encodes the receipt: only a delivery link names a
// candidate, a candidate comes with its receipt and a receipt with its
// candidate (invalid otherwise), each of its shape (ParseCandidate,
// encodeVerified). With neither, it answers nothing.
func checkCandidate(l NewLink, verified *Verified) (Candidate, string, error) {
	switch {
	case l.Candidate == "" && verified == nil:
		return Candidate{}, "", nil
	case l.Candidate == "":
		return Candidate{}, "", refuse(RefusalInvalid, "a verification receipt comes with the candidate it verifies")
	case l.Kind != LinkDelivery:
		return Candidate{}, "", refuse(RefusalInvalid, "only a delivery link names a candidate")
	}
	c, err := ParseCandidate(l.Candidate)
	if err != nil {
		return Candidate{}, "", err
	}
	if verified == nil {
		return Candidate{}, "", refuse(RefusalInvalid, "a delivery's candidate comes with its verification receipt, which the server reads from the ledger")
	}
	value, err := encodeVerified(c, *verified)
	if err != nil {
		return Candidate{}, "", err
	}
	return c, value, nil
}

// candidateRefusal is why the ticket tk may not name a candidate of the
// run, by who holds the run's chain (chainHolders), nil when tk holds it
// alone: a run of a chain another live ticket holds is that ticket's
// (run_linked naming it), a contested chain is no ticket's (run_linked
// naming every holder, the receipt's key the first), so no close policy is
// met through it, and a run of a chain no live ticket holds is none of
// tk's runs (invalid).
func candidateRefusal(tk Ticket, run string, launched, linked []ChainHolder) error {
	holders := heldBy(launched, linked)
	switch {
	case len(holders) == 1 && holders[0].TicketID == tk.ID:
		return nil
	case len(holders) == 1:
		h := holders[0]
		return &Refusal{Code: RefusalRunLinked, Key: h.Key,
			Words: fmt.Sprintf("the run %s is among %s's runs: a delivery names a candidate of its own ticket's runs", run, h.Key)}
	case len(holders) > 1:
		keys := make([]string, len(holders))
		for i, h := range holders {
			keys[i] = h.Key
		}
		return &Refusal{Code: RefusalRunLinked, Key: keys[0],
			Words: fmt.Sprintf("the chain of run %s is contested: %s each hold a run of it, so it is no ticket's; remove one of the links first", run, strings.Join(keys, " and "))}
	}
	return refuse(RefusalInvalid, "the run %s is none of %s's runs: neither launched from it nor linked to it", run, tk.Key)
}

// CandidateRefusal says whether the live ticket with the key may name a
// candidate of the run, as link.add will check it in its transaction
// (candidateRefusal): nil when the ticket holds the run's chain alone, a
// *Refusal otherwise. The API asks it before it reads the ledger, so a
// candidate of another ticket's run, or of a contested chain, is refused
// with nothing read and nothing kept. ErrNotFound when there is no such
// live ticket; ErrChainUnread when the run's chain could not be read.
func (s *Store) CandidateRefusal(ctx context.Context, key, run string) error {
	s.mu.Lock()
	sight := s.sight
	s.mu.Unlock()
	var refusal error
	err := s.read(ctx, func(q queryer) error {
		tk, err := ticketByKey(ctx, q, key)
		switch {
		case isNoRows(err):
			return ErrNotFound
		case err != nil:
			return err
		case !tk.DeletedAt.IsZero():
			return ErrNotFound
		}
		chain, err := sight.chainOf(run)
		if err != nil {
			return err
		}
		launched, linked, err := chainHolders(ctx, q, sight, chain)
		if err != nil {
			return err
		}
		refusal = candidateRefusal(tk, run, launched, linked)
		return nil
	})
	if err != nil {
		return err
	}
	return refusal
}

// delivery is a live delivery link as the close policy reads it: its
// candidate and its verification receipt, both empty for a delivery that
// names no candidate.
type delivery struct {
	candidate string
	verified  string
}

// deliveriesOf reads the ticket's live delivery links as the ticket shows
// them (linksOf: a member two replicas added read as its later add), in
// the order they were made, so a receipt the ticket does not show never
// meets the close policy. A version 1 database has no candidates.
func deliveriesOf(ctx context.Context, q queryer, ticketID string) ([]delivery, error) {
	links, err := linksOf(ctx, q, ticketID)
	if err != nil {
		return nil, err
	}
	var out []delivery
	for _, l := range links {
		if l.Kind == LinkDelivery {
			out = append(out, delivery{candidate: l.Candidate, verified: string(l.Verified)})
		}
	}
	return out, nil
}

// verifiedState is the close policy's verified, pure: met when a live
// delivery's recorded receipt says passed; else the words for the latest
// delivery that names a candidate (how its candidate fell short), or
// "no delivery names a candidate".
func verifiedState(deliveries []delivery) (met bool, why string) {
	why = "no delivery names a candidate"
	for _, d := range deliveries {
		if d.candidate == "" {
			continue
		}
		name := d.candidate
		if c, err := ParseCandidate(d.candidate); err == nil {
			name = c.Name()
		}
		var v Verified
		if json.Unmarshal([]byte(d.verified), &v) != nil {
			why = "the delivered candidate " + name + "'s verification receipt cannot be read"
			continue
		}
		if v.Passed {
			return true, ""
		}
		why = "the delivered candidate " + name + " " + shortfall(v)
	}
	return false, why
}

// shortfall is how a receipt that did not pass fell short, in words: the
// first receipt that did not pass, else no check at all, else the attempt.
func shortfall(v Verified) string {
	if len(v.Receipts) == 0 {
		return "ran no check"
	}
	for _, r := range v.Receipts {
		switch {
		case r.Exit == nil:
			return "did not finish " + r.what()
		case *r.Exit != 0:
			return fmt.Sprintf("failed %s (exit %d)", r.what(), *r.Exit)
		case !r.Stable:
			return "passed " + r.what() + " but not stably"
		}
	}
	return "passed its checks, but its attempt did not succeed"
}
