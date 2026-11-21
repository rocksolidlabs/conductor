package api

import (
	"context"
	"net/http"

	"github.com/rocksolidlabs/conductor/ext"
)

// GET /api/sync (docs/plans/sync-seams.md, OS-4): the sync store's report
// (ext.SyncStatus), asked through the guard within ext.StatusWait; {}
// with no sync store registered, and the report's fields left out when the
// store has none to give (no SyncStatus, late, failing, or a report its
// Valid refuses) or says it has nothing to report (the zero report, which
// is no failure). Beside it, what this server keeps on the machine of its
// own judgement (here): the copied-output check's state when it cannot
// judge, how many decisions have parts cut as output, and how many texts
// a hosted session's agent wrote were kept because the server does not see
// that agent's output.

// syncHere is what this server keeps on the machine of its own judgement.
type syncHere struct {
	Check  string `json:"check,omitempty"`  // the copied-output check's state in words, when it cannot judge
	Cut    int    `json:"cut,omitempty"`    // decisions with a part cut as output
	Hosted int64  `json:"hosted,omitempty"` // texts a hosted session's agent wrote, kept
}

// syncStatusReply is GET /api/sync's answer.
type syncStatusReply struct {
	*ext.SyncReport
	Here *syncHere `json:"here,omitempty"`
}

// maxSyncStatusReply bounds GET /api/sync's answer: a report at its
// bounds as JSON encodes it (50 changes kept back with their words, each
// character of which may take six bytes escaped, an answer page of 2 KiB)
// and the server's own words.
const maxSyncStatusReply = 128 << 10

// kindSyncStatus is the guard's kind of a sync store's SyncStatus.
const kindSyncStatus = ext.KindSync + "-status"

// syncReport asks the sync store for its report; false when there is none
// to give, the zero report among them (valid: nothing to report, nothing
// told).
func (s *Server) syncReport(ctx context.Context) (ext.SyncReport, bool) {
	g, ok := s.exts.sync.(guardSync)
	if !ok || g.p == nil || !live(g.ready) {
		return ext.SyncReport{}, false
	}
	st, ok := g.p.(ext.SyncStatus)
	if !ok {
		return ext.SyncReport{}, false
	}
	rep, _, ok := extCallTold(ctx, s, kindSyncStatus, g.name, ext.StatusWait, ext.SyncReport{}, func(r ext.SyncReport) error {
		return r.Valid()
	}, func(context.Context) (ext.SyncReport, error) {
		return st.SyncStatus(), nil
	})
	return rep, ok && !rep.IsZero()
}

// syncHereNow is what this server keeps of its own judgement now; nil for
// nothing.
func (s *Server) syncHereNow(ctx context.Context) *syncHere {
	h := &syncHere{Hosted: s.hostedKept.Load()}
	if s.outputs != nil {
		h.Check = s.outputs.state()
	}
	if s.ledger != nil {
		if n, err := s.ledger.OutputCut(ctx); err == nil {
			h.Cut = n
		}
	}
	if *h == (syncHere{}) {
		return nil
	}
	return h
}

func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "this route takes no query")
		return
	}
	if s.syncName() == "" {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	var reply syncStatusReply
	if rep, ok := s.syncReport(r.Context()); ok {
		reply.SyncReport = &rep
	}
	reply.Here = s.syncHereNow(r.Context())
	writeJSON(w, http.StatusOK, reply)
}
