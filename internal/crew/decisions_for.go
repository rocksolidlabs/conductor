package crew

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// Whom a decision is for (docs/plans/decisions.md, HM1, PR-12): a plan
// revision's decision, or an agent's, names a responsibility of the run's
// crew (role:architecture) or a person of it (person:nate). The label is
// resolved with Holder over the run's crew as launched, in the transaction
// that opens the decision (or a route's that changes it), and the request
// keeps the person it named, so a later edit of the saved crew changes
// nothing about it. The label never changes who may answer: the operator
// answers every decision under local/1 (decision D4), for the person it
// names, and the receipt keeps who answered and for whom side by side.

// DecisionFor is whom a decision is for, as its request keeps it: the
// label as given, the person it named when it was resolved, and the
// responsibility (role:) or "" (person:).
type DecisionFor struct {
	Label          string `json:"label"`
	Person         string `json:"person"`
	Responsibility string `json:"responsibility,omitempty"`
}

// ReceiptFor is whom an accepted response answered for: the request's
// for at the rev the response was accepted at. Once a decision is
// answered nothing changes its for, so the application, the typed line
// and the task file read it from the receipt.
type ReceiptFor struct {
	Person         string `json:"person"`
	Responsibility string `json:"responsibility,omitempty"`
}

// words is whom f is for in words: "nate, the architecture owner", or
// "nate" for a person named by name.
func (f *DecisionFor) words() string {
	if f == nil {
		return ""
	}
	return forWords(f.Person, f.Responsibility)
}

// words is whom f answered for in words.
func (f *ReceiptFor) words() string {
	if f == nil {
		return ""
	}
	return forWords(f.Person, f.Responsibility)
}

func forWords(person, resp string) string {
	if w, ok := ResponsibilityWords[resp]; ok {
		return person + ", the " + w
	}
	return person
}

// launchedCrew is the run's crew as launched, as far as a label reads it:
// its id and name (for the words) and its people, fixed at the launch.
func (r *run) launchedCrew() Crew {
	return Crew{ID: r.crewID, Name: r.name, People: r.people}
}

// resolveFor resolves label in r's crew as launched (Holder): nil for no
// label; a DecisionError invalid, naming the crew's responsibilities and
// their holders, for one no person of it holds. r.people never changes
// while the run is held, so no lock is needed to read it.
func (r *run) resolveFor(label string) (*DecisionFor, error) {
	if label == "" {
		return nil, nil
	}
	c := r.launchedCrew()
	p, resp, err := Holder(c, label)
	if err != nil {
		return nil, refuse(CodeInvalid, "%s: %s", err.Error(), holdersWords(c))
	}
	return &DecisionFor{Label: label, Person: p.Name, Responsibility: resp}, nil
}

// holdersWords say who holds what in c, for a label's refusal.
func holdersWords(c Crew) string {
	if len(c.People) == 0 {
		return "the run's crew lists no people"
	}
	var held, names []string
	for _, resp := range Responsibilities {
		if p, ok := c.holderOf(resp); ok {
			held = append(held, fmt.Sprintf("%s (%s)", resp, p.Name))
		}
	}
	for _, p := range c.People {
		names = append(names, p.Name)
	}
	words := "its people are " + strings.Join(names, ", ")
	if len(held) > 0 {
		words += "; they hold " + strings.Join(held, ", ")
	} else {
		words += "; none holds a responsibility"
	}
	return words
}

// forRule is the rule a routed row says for f under local/1.
func forRule(f *DecisionFor) string {
	if f == nil || f.Person == "" {
		return "local/1: the operator answers"
	}
	if w, ok := ResponsibilityWords[f.Responsibility]; ok {
		return fmt.Sprintf("local/1: the operator answers, for the %s (%s)", w, f.Label)
	}
	return fmt.Sprintf("local/1: the operator answers, for %s (%s)", f.Person, f.Label)
}

// forTo is the destinations a routed row names for f: the operator, and
// beside it the person f names.
func forTo(f *DecisionFor) []any {
	to := []any{map[string]string{"kind": "operator"}}
	if f != nil && f.Person != "" {
		to = append(to, map[string]string{"kind": "person", "name": f.Person})
	}
	return to
}

// routedDetail is the routed row of a decision's opening for f: one row,
// the operator's (routedOperator) for none, else the operator and the
// person beside it, so the history's count is DR1's.
func routedDetail(f *DecisionFor) string {
	if f == nil || f.Person == "" {
		return routedOperator
	}
	return historyDetail(map[string]any{"to": forTo(f), "rule": forRule(f), "by": "policy"})
}

// requestFor is the for a request as the ledger keeps it names, nil for
// none.
func requestFor(request string) *DecisionFor {
	var body struct {
		For *DecisionFor `json:"for"`
	}
	if json.Unmarshal([]byte(request), &body) != nil || body.For == nil || body.For.Person == "" {
		return nil
	}
	return body.For
}

// receiptFor is the receipt's for of request's.
func receiptFor(request string) *ReceiptFor {
	f := requestFor(request)
	if f == nil {
		return nil
	}
	return &ReceiptFor{Person: f.Person, Responsibility: f.Responsibility}
}

// withFor is request (as the ledger keeps it) with its for set to f, the
// other fields as they were, in their order: what a route that changes
// whom the decision is for writes. The request's fingerprint, of the
// request as sent, is not changed.
func withFor(request string, f *DecisionFor) (string, error) {
	var body requestBody
	if err := json.Unmarshal([]byte(request), &body); err != nil {
		return "", err
	}
	body.For = f
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	if len(raw) > ledger.MaxDecisionRequest {
		return "", refuse(CodeInvalid, "the request would be %d bytes as kept, more than %d", len(raw), ledger.MaxDecisionRequest)
	}
	return string(raw), nil
}

// The destinations of a route (Destination.Kind): the operator, DR1's
// one, and a person's label (HM1): a person by name, or a responsibility's
// holder.
const (
	DestOperator = "operator"
	DestPerson   = "person"
	DestRole     = "role"
)

// routeLabel is the label a route's destination names, "" for the
// operator's; false for a destination that is not one.
func routeLabel(to []Destination) (string, bool) {
	if len(to) == 1 {
		switch to[0].Kind {
		case DestPerson:
			return labelPerson + to[0].Name, to[0].Name != ""
		case DestRole:
			return labelRole + to[0].Name, to[0].Name != ""
		}
	}
	for _, d := range to {
		if d.Kind != DestOperator || d.Name != "" {
			return "", false
		}
	}
	return "", true
}

// forRoute is the change of whom decision id is for that a route to
// label makes, judged against the run's crew as launched and the record
// as the route's transaction reads it (held, the engine's copy d): taken
// only while the decision is open (decision_answered for one answered,
// decision_closed for one applied or otherwise final, a response's words),
// the label resolved in r's crew as launched. It returns the route's detail and the record's edit.
func (e *Engine) forRoute(r *run, d *decision, rec ledger.DecisionRecord, id, label, note, actor string) (opDetail, func(decisionTx, ledger.DecisionRow) error, error) {
	state := cmpState(d, rec)
	switch {
	case state == ledger.DecisionAnswered:
		return opDetail{}, nil, refuse(CodeDecisionAnswered, "%s: whom it is for changes only while it is open", answeredWords(id, rec))
	case state != ledger.DecisionOpen:
		return opDetail{}, nil, refuse(CodeDecisionClosed, "%s is %s: whom it is for changes only while it is open", id, state)
	}
	f, err := r.resolveFor(label)
	if err != nil {
		return opDetail{}, nil, err
	}
	was := requestFor(rec.Request)
	dt := opDetail{Note: note, To: forTo(f), Rule: forRule(f), By: actor, For: f, ForWas: was,
		Words: fmt.Sprintf("%s is now for %s: routed by %s", id, f.words(), actor)}
	edit := func(dtx decisionTx, row ledger.DecisionRow) error {
		// Its row judges, as the transaction reads it: answered or closed
		// since the look is refused in a response's words, and nothing of
		// the route is kept.
		switch {
		case row.State == ledger.DecisionAnswered:
			return refuse(CodeDecisionAnswered, "%s was answered: whom it is for changes only while it is open", id)
		case row.State != ledger.DecisionOpen:
			return refuse(CodeDecisionClosed, "%s is %s: whom it is for changes only while it is open", id, row.State)
		}
		req, err := withFor(row.Request, f)
		if err != nil {
			return err
		}
		return dtx.RouteFor(row.N, req)
	}
	return dt, edit, nil
}

// forSince is the newest change of whom a decision is for after the rev
// read, in words ("dr-7 is now for ana, the security reviewer: routed by
// admin"), "" for none: a stale response's words lead with it, so no
// answer is recorded for a person its responder did not see.
func forSince(rec ledger.DecisionRecord, read int) string {
	for _, h := range slices.Backward(rec.History) {
		if h.Kind != ledger.HistoryRouted || h.Detail == "" {
			continue
		}
		var dt struct {
			Rev   int          `json:"rev"`
			For   *DecisionFor `json:"for"`
			Words string       `json:"words"`
		}
		if json.Unmarshal([]byte(h.Detail), &dt) != nil || dt.Rev <= read || dt.For == nil {
			continue
		}
		return dt.Words
	}
	return ""
}

// gateFor is whom plan decision pd's gate is for, resolved in r's crew as
// launched: nil for no label. A label it does not resolve, which a
// planned launch refuses (checkPackageFor) and so only a crew as launched
// that lost its people can give (an older build's launch row, made whole
// at the ledger's recovery), is kept as given with no person, and the
// gate is the operator's alone, as DR1's are.
func (r *run) gateFor(pd PackageDecision) *DecisionFor {
	if pd.For == "" {
		return nil
	}
	f, err := r.resolveFor(pd.For)
	if err != nil {
		return &DecisionFor{Label: pd.For}
	}
	return f
}

// checkPackageFor holds a planned launch's gates to the crew it launches:
// every label a decision of the package names resolves in it (Holder),
// as the proposal's check did against the revision's crew, which is this
// one byte for byte.
func checkPackageFor(c Crew, p Package) error {
	for _, pd := range p.Decisions {
		if pd.For == "" {
			continue
		}
		if _, _, err := Holder(c, pd.For); err != nil {
			return invalidf("the package's decision %s is for %s: %s", quote(pd.ID), quote(pd.For), err.Error())
		}
	}
	return nil
}
