package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// A comparison over HTTP (docs/plans/compare-crew.md, Program item CP1,
// PR-3): the template that makes a comparison's crew, and a comparison's
// read. The template saves a crew and launches nothing: a comparison is
// launched from a ticket's approved plan revision alone, by a person who
// repeats the count its statement says (ticketLaunch). The read is the
// admin's (and, once it is listed among them, a Brakeman turn's shared
// read): an agent's session token, a lane's among them, and a share link
// are refused it.

const (
	// maxCompareSpecBody bounds the template's body: the spec with each
	// text at its own bound in characters (the name, goal, instructions,
	// twelve criteria and four approaches, the candidates' arguments) as a
	// client may write them, every character outside ASCII escaped as
	// \uXXXX (six bytes for one of a three-byte script, twelve for one
	// outside the Basic Multilingual Plane), within the task file's
	// never-cut bound the crew is held to.
	maxCompareSpecBody = 256 << 10
	// comparisonPattern is the comparison's read, as the mux and the
	// shared reads name it.
	comparisonPattern = "GET /api/runs/{run}/comparison"
	// notObservedWords is what a comparison's independence part says the
	// server does not observe: the measured part shows the commits two
	// lanes share and the patches they have in common, never what an
	// agent's hooks say it touched (the touched index, which the reply
	// does not read).
	// personLaunchesWords refuses a comparison's launch to every caller
	// but a person's route (ext.Host's LaunchApproved, Brakeman's granted
	// launch act among them).
	personLaunchesWords = "a comparison is launched by a person, who reads what it starts: open the ticket's revision"
	notObservedWords    = "A read of another candidate's folder or of an agent's own stored state is not observed, and a change rewritten by hand is not detected; what the agents report of their own file activity is not used as a measure."
)

// handleCompareTemplate makes a comparison's crew from a CompareSpec
// (crew.CompareTemplate) and saves it as POST /api/crews saves a crew:
// body at most maxCompareSpecBody, decoded with unknown fields refused
// (400 invalid_request, as the crew routes answer what they cannot
// decode); the crew held to its rules and its agents to the catalog (400
// invalid_crew in the rule's words); 201 {crew, revision}. Its working
// directory is the crew's, checked against the allowed roots where every
// crew's is, at its plan's proposal and at its launch (resolveCwd).
func (s *Server) handleCompareTemplate(w http.ResponseWriter, r *http.Request) {
	if s.crews == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "no data directory is configured")
		return
	}
	var spec crew.CompareSpec
	if err := decodeJSONLimit(w, r, &spec, maxCompareSpecBody); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	spec.Name = strings.TrimSpace(spec.Name)
	c, err := crew.CompareTemplate(spec)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_crew", err.Error())
		return
	}
	if err := c.CheckAgents(s.Catalog()); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_crew", err.Error())
		return
	}
	saved, err := s.crews.Create(c)
	if err != nil {
		s.crewStoreError(w, "", err)
		return
	}
	s.log.Info("comparison crew created", "crew", saved.ID)
	writeJSON(w, http.StatusCreated, crewReply(saved))
}

// comparisonReply is a comparison's read: the engine's measure of the
// chain (crew.Comparison), the independence the server adds, and the parts
// later rows read beside it (supporting, selection), absent while they
// are none.
type comparisonReply struct {
	crew.Comparison
	Independence comparisonIndependence `json:"independence"`
	Supporting   *comparisonSupporting  `json:"supporting,omitempty"`
	Selection    *comparisonSelection   `json:"selection,omitempty"`
	// State is where the comparison stands (PR-6b): running, selecting,
	// answered, selected, none or ended.
	State string `json:"state,omitempty"`
}

// comparisonIndependence says what the comparison does not observe.
type comparisonIndependence struct {
	NotObserved string `json:"notObserved"`
}

// handleRunComparison answers the comparison of the chain the run belongs
// to (any run of it): 200 the comparison (comparisonReply), at most
// crew.MaxComparison bytes; 404 not_found for a run the ledger has not, and
// for a run of no comparison ("this run is not a comparison"); 503
// run_changing with Retry-After: 1 when the chain moved through every read
// of it (crew.ErrRunChanging); 409 chain_too_long for a chain longer than
// a comparison's (which no continuation makes); 503 ledger_unavailable;
// 400 invalid_request
// for an id that is not one and for any parameter. Nothing of the touched
// index is read.
func (s *Server) handleRunComparison(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("run")
	if !crew.ValidRunID(runID) {
		writeError(w, http.StatusBadRequest, "invalid_request", "a run id")
		return
	}
	if !noQuery(w, r) {
		return
	}
	if s.ledger == nil {
		writeError(w, http.StatusServiceUnavailable, "ledger_unavailable", "the ledger could not be opened; see the server's log")
		return
	}
	ctx := r.Context()
	row, found, err := s.ledger.Run(ctx, runID)
	switch {
	case err != nil:
		s.receiptError(w, "comparison", runID, err)
		return
	case !found:
		writeError(w, http.StatusNotFound, "not_found", "no such run")
		return
	case !rowIsComparison(row):
		writeError(w, http.StatusNotFound, "not_found", "this run is not a comparison")
		return
	}
	c, err := s.runs.Comparison(ctx, runID)
	switch {
	case errors.Is(err, crew.ErrRunChanging):
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "run_changing", err.Error())
		return
	case errors.Is(err, crew.ErrRunNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such run")
		return
	case errors.Is(err, crew.ErrChainTooLong):
		writeError(w, http.StatusConflict, "chain_too_long", err.Error())
		return
	case errors.Is(err, ledger.ErrUnavailable), errors.Is(err, ledger.ErrClosed), errors.Is(err, crew.ErrNoLedger):
		writeError(w, http.StatusServiceUnavailable, "ledger_unavailable", err.Error())
		return
	case err != nil:
		s.log.Warn("a comparison could not be read", "run", runID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "the comparison could not be read")
		return
	}
	reply := comparisonReply{Comparison: c, Independence: comparisonIndependence{NotObserved: notObservedWords},
		Supporting: s.comparisonSupporting(ctx, c), Selection: s.comparisonSelection(ctx, c), State: s.comparisonState(ctx, c)}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(reply); err != nil || b.Len() > crew.MaxComparison {
		s.log.Warn("a comparison's reply is past its bound", "run", runID, "bytes", b.Len(), "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "the comparison could not be read")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b.Bytes())
}

// rowIsComparison reports whether the ledger's row is a run of a
// comparison: its crew as launched, which every continuation carries
// unchanged, has a compare block.
func rowIsComparison(row ledger.RunRow) bool {
	var c struct {
		Compare json.RawMessage `json:"compare"`
	}
	return json.Unmarshal([]byte(row.Crew), &c) == nil && len(c.Compare) > 0 && string(c.Compare) != "null"
}

// comparisonRevisionRefused answers w, and reports true, for a launch of
// the plan revision plan by any caller but a person's launch route when
// the revision's crew is a comparison (409 refused, personLaunchesWords),
// or when the revision cannot be read to tell (its read's error): such a
// launch writes nothing. A revision the store has not is left to the
// launch, which refuses it not_found.
func (s *Server) comparisonRevisionRefused(ctx context.Context, w http.ResponseWriter, ds *dispatch.Store, plan string) bool {
	if !dispatch.ValidID(plan) {
		return false
	}
	v, err := ds.Plan(ctx, plan)
	switch {
	case errors.Is(err, dispatch.ErrNotFound):
		return false
	case err != nil:
		s.dispatchError(w, "plan revision", err)
		return true
	}
	f, err := s.planFacts.get(v.Digest, func() (json.RawMessage, error) { return v.Body, nil })
	if err != nil {
		s.dispatchError(w, "plan revision", err)
		return true
	}
	if f.Compare == nil {
		return false
	}
	writeError(w, http.StatusConflict, "refused", personLaunchesWords)
	return true
}
