package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The selection on the routes (docs/plans/compare-crew.md, PR-6b): the
// comparison's reply carries its newest selection record as DR1 keeps it;
// a delivery naming a comparison's candidate is taken for the applied
// selection's candidate alone (and a later one of its task); a Request
// changes review waits for the selection and is refused on a lane not
// selected; and the selected lane's candidate comments on its ticket once
// the selection is applied (door 8). Every refusal is made before
// anything is written. The answer itself is DR1's: the operator's alone,
// with a rationale, never cancelled (internal/crew, compare_select*.go).

// The selection part's bounds: each option's label and tradeoffs, the
// rationale, the answerer's name and the application's words, cut as
// encoded; at most maxSelection in all.
const (
	maxSelection          = 16 << 10
	maxSelectionLabel     = 300
	maxSelectionTradeoffs = 1 << 10
	maxSelectionRationale = 4 << 10
	maxSelectionBy        = 200
	maxSelectionWords     = 500
	maxSelectionOptions   = 5
)

// comparisonSelection is the selection part of a comparison: its record,
// the options, the answer and how it was applied.
type comparisonSelection struct {
	Decision  string            `json:"decision"`
	State     string            `json:"state"`
	Options   []selectionOption `json:"options"`
	Chosen    *int              `json:"chosen,omitempty"`
	Rationale string            `json:"rationale,omitempty"`
	By        string            `json:"by,omitempty"`
	For       *crew.DecisionFor `json:"for,omitempty"`
	At        *time.Time        `json:"at,omitempty"`
	Applied   *selectionApplied `json:"applied,omitempty"`
}

// selectionOption is an option as the record's request holds it.
type selectionOption struct {
	Label     string          `json:"label"`
	Tradeoffs string          `json:"tradeoffs,omitempty"`
	Subject   json.RawMessage `json:"subject,omitempty"`
}

// selectionApplied is how the answer was applied: the candidate selected,
// or None of these.
type selectionApplied struct {
	How      string                 `json:"how"`
	Selected *crew.SettledCandidate `json:"selected,omitempty"`
	None     bool                   `json:"none,omitempty"`
	Words    string                 `json:"words,omitempty"`
}

// comparisonSelection reads the selection part of the comparison c: its
// chain's newest selection record, nil while it has none (or its record
// cannot be read, which the log says).
func (s *Server) comparisonSelection(ctx context.Context, c crew.Comparison) *comparisonSelection {
	st, err := s.runs.ComparisonStateOf(ctx, c.Run)
	if err != nil || st.Decision == "" {
		if err != nil {
			s.log.Warn("a comparison's selection could not be read", "run", c.Run, "err", err)
		}
		return nil
	}
	d, err := s.runs.Decision(ctx, st.Decision)
	if err != nil {
		s.log.Warn("a comparison's selection record could not be read", "run", c.Run, "decision", st.Decision, "err", err)
		return nil
	}
	return selectionOf(d)
}

// selectionOf is the selection part of the record d, within its bound.
func selectionOf(d crew.Decision) *comparisonSelection {
	var req struct {
		Options []selectionOption `json:"options"`
		For     *crew.DecisionFor `json:"for"`
	}
	_ = json.Unmarshal(d.Request, &req)
	out := &comparisonSelection{Decision: d.ID, State: d.State, Options: []selectionOption{}, For: req.For, At: d.AnsweredAt}
	for _, o := range req.Options[:min(len(req.Options), maxSelectionOptions)] {
		out.Options = append(out.Options, selectionOption{Label: cutEncoded(o.Label, maxSelectionLabel), Tradeoffs: cutEncoded(o.Tradeoffs, maxSelectionTradeoffs), Subject: o.Subject})
	}
	if out.For != nil {
		f := *out.For
		f.Label, f.Person, f.Responsibility = cutEncoded(f.Label, maxSelectionBy), cutEncoded(f.Person, maxSelectionBy), cutEncoded(f.Responsibility, maxSelectionBy)
		out.For = &f
	}
	for _, r := range d.Responses {
		if r.Status == ledger.ResponseAccepted {
			out.Chosen, out.Rationale, out.By = r.Choice, cutEncoded(r.Rationale, maxSelectionRationale), cutEncoded(cmpOr(r.ByName, r.Actor), maxSelectionBy)
		}
	}
	if a := d.Applied; a != nil {
		out.Applied = &selectionApplied{How: a.How, Selected: a.Selected, None: a.None, Words: cutEncoded(a.Words, maxSelectionWords)}
	}
	return out
}

// comparisonState is the comparison's state word (running, selecting,
// answered, selected, none, ended), "" when it cannot be read.
func (s *Server) comparisonState(ctx context.Context, c crew.Comparison) string {
	st, err := s.runs.ComparisonStateOf(ctx, c.Run)
	if err != nil {
		return ""
	}
	return st.State
}

// notSelectedWords is a candidate of the task refused as not selected in
// the record: the engine's words for a command on a lane not selected.
func notSelectedWords(task, decision string) string {
	return fmt.Sprintf("%s was not selected in %s: run a new trial to try it again", task, decision)
}

// selectFirstWords is what a delivery or a repair before the selection is told:
// the record that waits for its answer (open, or answered and not yet
// applied), or that the lanes still run: a record superseded by a lane's
// change is named by nothing, since it takes no answer.
func selectFirstWords(st crew.ComparisonState) string {
	if st.Decision == "" || st.State != crew.CompareSelecting && st.State != crew.CompareAnswered {
		return "select a candidate first: the trial has not settled"
	}
	return "select a candidate first: " + st.Decision + " is open"
}

// compareDeliveryRefusal is the refusal of a delivery naming the
// candidate c when its run is of a comparison's chain (its crew as
// launched, which every run of the chain carries, has a compare block):
// 409 not_selected, nothing kept, for every candidate but the applied
// selection's and a later one of its task ("select a candidate first:
// dr-9 is open" before the selection is applied, "no candidate was
// selected: launch a new comparison" once it ended with none, the
// engine's words for a lane not selected). "" for a candidate of any
// other run, and for one the ledger cannot tell (no ledger, no such run),
// which candidateVerified refuses; an error is the ledger's.
func (s *Server) compareDeliveryRefusal(ctx context.Context, c dispatch.Candidate) (code, words string, err error) {
	if s.ledger == nil {
		return "", "", nil
	}
	row, found, err := s.ledger.Run(ctx, c.Run)
	if err != nil || !found || !rowIsComparison(row) {
		return "", "", err
	}
	st, err := s.runs.ComparisonStateOf(ctx, c.Run)
	if err != nil {
		return "", "", err
	}
	switch st.State {
	case crew.CompareSelected:
		sel := st.Selected
		if sel.Task != c.Task {
			return "not_selected", notSelectedWords(c.Task, st.Decision), nil
		}
		later, err := s.atOrAfter(ctx, *sel, c)
		if err != nil || later {
			return "", "", err
		}
		return "not_selected", fmt.Sprintf("%s/%d is before %s's selected candidate %s/%d: deliver the selected one or a later one", c.Run, c.Attempt, st.Decision, sel.Run, sel.Attempt), nil
	case crew.CompareNone, crew.CompareEnded:
		return "not_selected", "no candidate was selected: run a new trial", nil
	}
	return "not_selected", selectFirstWords(st), nil
}

// atOrAfter reports whether the candidate c of the selected task is the
// selected candidate or a later one: a later attempt in the selected
// candidate's run, or any in a run that continues it.
func (s *Server) atOrAfter(ctx context.Context, sel crew.AppliedSelection, c dispatch.Candidate) (bool, error) {
	if c.Run == sel.Run {
		return c.Attempt >= sel.Attempt, nil
	}
	for id, n := sel.Run, 0; id != "" && n < maxSupportedChains; n++ {
		row, ok, err := s.ledger.Run(ctx, id)
		if err != nil || !ok {
			return false, err
		}
		if row.ContinuedBy == c.Run {
			return true, nil
		}
		id = row.ContinuedBy
	}
	return false, nil
}

// compareReviewRefusal is the refusal of a Request changes review of the
// candidate c when its run is of a comparison's chain, made after the
// candidate is checked and before Dispatch keeps anything: 409
// not_selected before the selection is applied ("a repair before the
// selection would make the candidates unlike"), on a lane not selected
// and after None of these; nil for the selected lane's candidate and for
// a candidate of any other run. A Brakeman turn's request_repair act
// reaches the same route, and so this step.
func (s *Server) compareReviewRefusal(ctx context.Context, c dispatch.Candidate) (*apiError, error) {
	if s.ledger == nil {
		return nil, nil
	}
	row, found, err := s.ledger.Run(ctx, c.Run)
	if err != nil || !found || !rowIsComparison(row) {
		return nil, err
	}
	st, err := s.runs.ComparisonStateOf(ctx, c.Run)
	if err != nil {
		return nil, err
	}
	refuse := func(words string) (*apiError, error) {
		return &apiError{Code: "not_selected", Message: words, status: http.StatusConflict}, nil
	}
	switch st.State {
	case crew.CompareSelected:
		if st.Selected.Task == c.Task {
			return nil, nil
		}
		return refuse(notSelectedWords(c.Task, st.Decision))
	case crew.CompareNone, crew.CompareEnded:
		return refuse("no candidate was selected: run a new trial")
	}
	return refuse("select a candidate first: a repair before the selection would make the candidates unlike")
}

// laneSelected reports whether the session d is the selected candidate's
// own (door 8): a session of a comparison's lane the engine holds, whose
// lane is the applied selection's, and whose member is the candidate
// task's, not its reviewer's. Anything that cannot be told is false.
func (s *Server) laneSelected(ctx context.Context, d session.Driver) bool {
	l, st := s.laneOf(d)
	if st != inLane || s.runs == nil {
		return false
	}
	sel, ok, err := s.runs.SelectedLane(ctx, l.Run)
	if err != nil || !ok || sel.None || sel.Task != l.Candidate {
		return false
	}
	run, ok := s.runs.Get(l.Run)
	if !ok {
		return false
	}
	for _, t := range run.Tasks {
		if t.ID == l.Candidate {
			return t.Member == l.Member
		}
	}
	return false
}
