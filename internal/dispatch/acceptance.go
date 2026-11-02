package dispatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Acceptance is a fact of its own, never implied by a status: the
// operator records it for the ticket's owner, against the acceptance
// criteria as they stand (accepted_at, accepted_by, accepted_for,
// accepted_criteria, accepted_note), and no status change touches it, so
// a ticket may be accepted while open or closed. The close policy is a
// project's: what a ticket needs before it is done, the owner's
// acceptance, a delivery link, a verified delivery (one naming a
// candidate whose recorded receipt passed: delivery.go), any of them or
// none. Its state on a ticket is
// computed on every read (CloseState), never stored, so a policy, an
// owner or criteria edited later read true at once; done is entered only
// when it is met (EditTicket's gate), and any other status may follow
// any other.

// CloseState is a ticket's close policy as read: what its project
// requires, which requirements the ticket meets, the required ones it
// does not (Missing, the order of Requires), Why, the words for an
// acceptance recorded but unmet ("accepted for Alice; the owner is now
// Bob", "accepted before the criteria changed"), "" when there is none
// or it is met, and VerifiedWhy, the words for verified unmet ("the
// delivered candidate build#2 failed the check test (exit 1)", "no
// delivery names a candidate"), "" when it is met.
type CloseState struct {
	Requires    []string `json:"requires"`
	Met         CloseMet `json:"met"`
	Missing     []string `json:"missing"`
	Why         string   `json:"why,omitempty"`
	VerifiedWhy string   `json:"verifiedWhy,omitempty"`
}

// CloseMet says which of the close policy's requirements a ticket meets,
// required or not: the owner's acceptance, a delivery link, and a
// delivery whose candidate's recorded receipt passed (verified, which
// implies a delivery).
type CloseMet struct {
	Acceptance bool `json:"acceptance"`
	Delivery   bool `json:"delivery"`
	Verified   bool `json:"verified"`
}

// AcceptTicket records the owner's acceptance of the live ticket with the
// key (ticket.accept): accepted_at now; accepted_by the operator's display
// name (the actor when it has none); accepted_for the ticket's owner now
// (invalid when it has none: an acceptance is the owner's); accepted_criteria
// the SHA-256 of its acceptance criteria now; accepted_note the note, at
// most MaxAcceptedNote characters of Markdown. One set row per field that
// changes, so an acceptance made again (after the owner or the criteria
// changed) records the new one beside the old in the history. The status
// is not touched. A deleted ticket is not_found; an expected revision is
// the ticket's; an archived project refuses it (archived).
func (s *Store) AcceptTicket(ctx context.Context, op Op, key, note string) (Receipt, error) {
	args := struct {
		Key  string `json:"key"`
		Note string `json:"note,omitempty"`
	}{key, note}
	return s.Apply(ctx, op, "ticket.accept", args, func(t *tx) error {
		tk, _, err := t.ticket(key, false)
		if err != nil {
			return err
		}
		if tk.Owner == "" {
			return refuse(RefusalInvalid, "%s has no owner to accept it", tk.Key)
		}
		if err := checkField("ticket.accepted_note", "the note", note); err != nil {
			return err
		}
		before, err := t.scalars("tickets", tk.ID, acceptanceColumns)
		if err != nil {
			return err
		}
		after := []fieldValue{{"accepted_at", stamp(t.now)}, {"accepted_by", t.op.author()}, {"accepted_for", tk.Owner},
			{"accepted_criteria", criteriaHash(tk.Acceptance)}, {"accepted_note", note}}
		return t.setScalars("tickets", ChangeRow{Entity: EntityTicket, EntityID: tk.ID, TicketID: tk.ID, ProjectID: tk.ProjectID}, before, after)
	})
}

// UnacceptTicket clears the acceptance of the live ticket with the key
// (ticket.unaccept): the five fields AcceptTicket records, one set row
// each that held something. A ticket not accepted is invalid; the status
// is not touched. A deleted ticket is not_found; an expected revision is
// the ticket's; an archived project refuses it (archived).
func (s *Store) UnacceptTicket(ctx context.Context, op Op, key string) (Receipt, error) {
	args := struct {
		Key string `json:"key"`
	}{key}
	return s.Apply(ctx, op, "ticket.unaccept", args, func(t *tx) error {
		tk, _, err := t.ticket(key, false)
		if err != nil {
			return err
		}
		if tk.AcceptedAt.IsZero() {
			return refuse(RefusalInvalid, "%s is not accepted", tk.Key)
		}
		before, err := t.scalars("tickets", tk.ID, acceptanceColumns)
		if err != nil {
			return err
		}
		after := fieldValues(before)
		for i := range after {
			after[i].value = ""
		}
		return t.setScalars("tickets", ChangeRow{Entity: EntityTicket, EntityID: tk.ID, TicketID: tk.ID, ProjectID: tk.ProjectID}, before, after)
	})
}

// acceptanceColumns is the acceptance's fields, in the order an operation
// records them.
var acceptanceColumns = []string{"accepted_at", "accepted_by", "accepted_for", "accepted_criteria", "accepted_note"}

// criteriaHash is the SHA-256 of acceptance criteria, in hex: what an
// acceptance records it was made against.
func criteriaHash(acceptance string) string {
	sum := sha256.Sum256([]byte(acceptance))
	return hex.EncodeToString(sum[:])
}

// CloseState reads the close policy's state of the ticket with the key,
// deleted or not, computed now from its project's policy, its acceptance,
// its owner and criteria, and its live links, in one read transaction.
// ErrNotFound when there is no such ticket.
func (s *Store) CloseState(ctx context.Context, key string) (CloseState, error) {
	var out CloseState
	err := s.read(ctx, func(q queryer) error {
		tk, err := readTicket(ctx, q, key)
		if err != nil {
			return err
		}
		p, err := projectByID(ctx, q, tk.ProjectID)
		if err != nil {
			return err
		}
		out, err = closeOf(ctx, q, p, tk)
		return err
	})
	return out, err
}

// closeOf is the close policy's state of the ticket tk of the project p:
// tk's acceptance, owner and criteria as given (the gate passes them as
// an edit leaves them), and its live delivery links.
func closeOf(ctx context.Context, q queryer, p Project, tk Ticket) (CloseState, error) {
	deliveries, err := deliveriesOf(ctx, q, tk.ID)
	if err != nil {
		return CloseState{}, err
	}
	return closeState(p.CloseRequires, tk, deliveries), nil
}

// closeState is the close policy's state, pure: the acceptance is met when
// one is recorded, for the owner the ticket has now, against the criteria
// it has now; a delivery when a live delivery link is there; verified when
// one of them names a candidate whose recorded receipt passed
// (verifiedState).
func closeState(requires []string, tk Ticket, deliveries []delivery) CloseState {
	st := CloseState{Requires: append([]string{}, requires...), Missing: []string{}}
	var why []string
	if !tk.AcceptedAt.IsZero() {
		forOwner, forCriteria := tk.AcceptedFor == tk.Owner, tk.AcceptedCriteria == criteriaHash(tk.Acceptance)
		st.Met.Acceptance = forOwner && forCriteria
		switch {
		case !forOwner && tk.Owner == "":
			why = append(why, "accepted for "+tk.AcceptedFor+"; the ticket has no owner now")
		case !forOwner:
			why = append(why, "accepted for "+tk.AcceptedFor+"; the owner is now "+tk.Owner)
		}
		if !forCriteria {
			why = append(why, "accepted before the criteria changed")
		}
	}
	st.Met.Delivery = len(deliveries) > 0
	st.Met.Verified, st.VerifiedWhy = verifiedState(deliveries)
	for _, r := range requires {
		if (r == RequireAcceptance && !st.Met.Acceptance) || (r == RequireDelivery && !st.Met.Delivery) || (r == RequireVerified && !st.Met.Verified) {
			st.Missing = append(st.Missing, r)
		}
	}
	st.Why = strings.Join(why, "; ")
	return st
}

// policyRefusal is the close_policy refusal of a move of what to done
// while the policy is unmet: its words name what is missing, why an
// acceptance recorded is unmet and why verified is; Missing names the
// requirements.
func policyRefusal(what string, st CloseState) error {
	var lacks []string
	for _, r := range st.Missing {
		switch r {
		case RequireAcceptance:
			if st.Why != "" {
				lacks = append(lacks, "the owner's acceptance ("+st.Why+")")
			} else {
				lacks = append(lacks, "the owner's acceptance")
			}
		case RequireDelivery:
			lacks = append(lacks, "a delivery link")
		case RequireVerified:
			lacks = append(lacks, "a verified delivery ("+st.VerifiedWhy+")")
		}
	}
	return &Refusal{Code: RefusalClosePolicy, Words: what + " is done only once its project's close policy is met: it lacks " + strings.Join(lacks, " and "),
		Missing: append([]string{}, st.Missing...)}
}
