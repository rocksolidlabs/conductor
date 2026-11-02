package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// A delivery's candidate (Program item DS2, docs/plans/dispatch-evidence.md,
// PR-9, "A delivery's candidate"): a delivery link that names the
// candidate it delivers, <run>/<task>/<n>@<commit>, keeps that
// candidate's verification receipt, read here from the ledger when the
// link is added and handed to the store apart from the request
// (dispatch.Verified), so the close policy's verified is a function of
// Dispatch's own rows. The order is the one that makes a resend safe: the
// operation is replayed first, so a retry after a lost reply gets the
// first answer whatever the ledger holds now, or whether it can be read at
// all; only a new operation reads the ledger.

// addDelivery adds a delivery link naming its candidate (link.add with
// candidate), in this order: the operation replayed (dispatch.Store.Replay,
// its content the key and the link as sent, the candidate among it), a
// resend answered from its receipt, or once pruned through its add row
// compared by the link's fingerprint, before anything is read; then, for
// a new operation, the candidate parsed (a bad one is the store's to
// refuse, its receipt kept), the run asked to be the ticket's (a run of a
// chain the ticket holds alone: 409 run_linked naming the holder, or every
// holder of a contested chain; 409 invalid for a run no ticket holds; 404
// not_found for no live ticket; nothing kept), the attempt read from the
// ledger (503 ledger_unavailable when there is none to read, nothing
// kept; 409 invalid for a run the ledger has not, or an attempt that is no
// work attempt's whose result is the commit named), its receipts as the
// candidates route reads them, and the store's AddLink with the receipt.
func (s *Server) addDelivery(ctx context.Context, w http.ResponseWriter, ds *dispatch.Store, op dispatch.Op, key string, l dispatch.NewLink) {
	rc, found, err := ds.Replay(ctx, op, dispatch.IntentLinkAdd, dispatch.LinkAddArgs{Key: key, NewLink: l})
	if err != nil || found {
		s.writeOp(w, "ticket", rc, err, true, "ticket", ticketAfter(ctx, ds, key))
		return
	}
	c, err := dispatch.ParseCandidate(l.Candidate)
	if err != nil {
		// The store refuses it, its receipt kept, before it looks for a
		// receipt.
		rc, err := ds.AddLink(ctx, op, key, l, nil)
		s.writeOp(w, "ticket", rc, err, true, "ticket", ticketAfter(ctx, ds, key))
		return
	}
	var refusal *dispatch.Refusal
	switch err := ds.CandidateRefusal(ctx, key, c.Run); {
	case errors.As(err, &refusal):
		reply := map[string]any{"error": apiError{Code: refusal.Code, Message: refusal.Words}}
		if refusal.Key != "" {
			reply["key"] = refusal.Key
		}
		writeDispatch(w, http.StatusConflict, reply)
		return
	case err != nil:
		s.dispatchError(w, "ticket", err)
		return
	}
	v, code, words, err := s.candidateVerified(ctx, c)
	switch {
	case err != nil:
		s.receiptError(w, "a delivery's candidate", c.Run, err)
		return
	case code == "ledger_unavailable":
		writeError(w, http.StatusServiceUnavailable, code, words)
		return
	case code != "":
		writeError(w, http.StatusConflict, code, words)
		return
	}
	rc, err = ds.AddLink(ctx, op, key, l, v)
	s.writeOp(w, "ticket", rc, err, true, "ticket", ticketAfter(ctx, ds, key))
}

// candidateVerified reads the candidate's verification receipt from the
// ledger: its attempt, a work attempt whose result is the candidate's
// commit, and that attempt's receipts as the candidates route reads them
// (its check and verify rows whose subject is the commit, at most 16,
// each with where it ran), passed when the attempt succeeded and every
// receipt finished with exit 0, stable, and false for a candidate with no
// check at all. A refusal is its code and words (ledger_unavailable with
// no ledger to read; invalid for a run the ledger has not, or an attempt
// that is not the candidate); an error is the ledger's.
func (s *Server) candidateVerified(ctx context.Context, c dispatch.Candidate) (v *dispatch.Verified, code, words string, err error) {
	if s.ledger == nil {
		return nil, "ledger_unavailable", "the ledger could not be opened, so the candidate's receipts cannot be read; see the server's log", nil
	}
	row, found, err := s.ledger.Run(ctx, c.Run)
	if err != nil {
		return nil, "", "", err
	}
	if !found {
		return nil, dispatch.RefusalInvalid, fmt.Sprintf("the run %s has no candidates: the ledger has no managed run of that id", c.Run), nil
	}
	kind := ""
	var rc crew.Crew
	if json.Unmarshal([]byte(row.Crew), &rc) == nil {
		if i := slices.IndexFunc(rc.Tasks, func(t crew.Task) bool { return t.ID == c.Task }); i >= 0 {
			kind = rc.Tasks[i].Kind
		}
	}
	taskRows, err := s.ledger.Tasks(ctx, c.Run)
	if err != nil {
		return nil, "", "", err
	}
	var tr ledger.TaskRow
	if i := slices.IndexFunc(taskRows, func(r ledger.TaskRow) bool { return r.ID == c.Task }); i >= 0 {
		tr = taskRows[i]
		kind = cmp.Or(tr.Kind, kind)
	}
	attempts, err := s.ledger.Attempts(ctx, c.Run)
	if err != nil {
		return nil, "", "", err
	}
	i := slices.IndexFunc(attempts, func(a ledger.AttemptRow) bool { return a.TaskID == c.Task && a.N == c.Attempt })
	switch {
	case i < 0:
		return nil, dispatch.RefusalInvalid, fmt.Sprintf("the run %s has no attempt %d of a task %s", c.Run, c.Attempt, c.Task), nil
	case kind == crew.TaskReview:
		return nil, dispatch.RefusalInvalid, fmt.Sprintf("%s is a review: a delivery names a work attempt's result", c.Name()), nil
	}
	a := attempts[i]
	checks, err := s.ledger.Checks(ctx, c.Run, c.Task, c.Attempt, false)
	if err != nil {
		return nil, "", "", err
	}
	cand := candidateOf(kind, tr, a, checks, nil)
	if cand.Commit != c.Commit {
		result := "no result"
		if cand.Commit != "" {
			result = "the result " + cand.Commit
		}
		return nil, dispatch.RefusalInvalid, fmt.Sprintf("%s's commit is not %s: the attempt has %s", c.Name(), c.Commit, result), nil
	}
	v = &dispatch.Verified{At: time.Now().UTC(), Attempt: c.Attempt, Receipts: []dispatch.VerifiedReceipt{}}
	passed := a.State == ledger.AttemptSucceeded
	for _, r := range cand.Receipts {
		passed = passed && r.State == ledger.CheckFinished && r.Exit != nil && *r.Exit == 0 && r.Stable && !r.TimedOut && !r.Killed && r.Signal == ""
		v.Receipts = append(v.Receipts, dispatch.VerifiedReceipt{Phase: r.Phase, ID: r.ID, K: r.K, Where: r.Where, Exit: r.Exit, Stable: r.Stable, LogSHA256: r.LogSHA256})
	}
	v.Passed = passed && len(v.Receipts) > 0
	return v, "", "", nil
}
