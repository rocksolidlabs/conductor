package crew

import (
	"encoding/json"
	"slices"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// Who may answer and read a decision (docs/plans/decide-grant.md,
// Admission and its end): one judgement, AdmissionOf, over the record a
// route read, with Admits over the record's entries inside it. A
// decision's entries are its policy's authority and, while its route
// target is the decide grant (the request's for, which only Tx.RouteFor
// writes and only while the decision is open), the grant's entry beside
// it; the policy column itself is never written.

// GrantDecideLabel is the route target of a decision given to the decide
// grant, as its request's for keeps it ({label: "grant:decide"}, no
// person): the one place the label is spelled.
const GrantDecideLabel = "grant:decide"

// The kinds of a policy entry: the operator (the admin grant) and a grant.
// A later kind adds its rule to PolicyEntry.admits and nowhere else; an
// unknown kind admits nobody.
const (
	EntryOperator = "operator"
	EntryGrant    = "grant"
)

// PolicyEntry is one entry of a decision's authority.
type PolicyEntry struct {
	Kind  string `json:"kind"`
	Grant string `json:"grant,omitempty"`
}

// Admittee is who asks: the grants its principal holds here, how it
// authenticated (ViaWorkbench, ViaIdentity, ViaRouter) and an identity's
// subject.
type Admittee struct {
	Grants  []string
	Via     string
	Subject string
}

// holds says who holds grant g.
func (who Admittee) holds(g ext.Grant) bool { return slices.Contains(who.Grants, string(g)) }

// Sent says response r is who's own: who is an identity, and r was kept
// under its actor ("user:<subject>") with an authority that says it came
// through an identity with who's subject. An authority that does not
// decode is nobody's, and a decision router's answer kept in a person's
// name is the router's, never that person's.
func (who Admittee) Sent(r ledger.ResponseRow) bool {
	var a Authority
	return who.Via == ViaIdentity && who.Subject != "" && r.Actor == ext.SubjectActor+who.Subject &&
		json.Unmarshal([]byte(r.Authority), &a) == nil && a.Via == ViaIdentity && a.Subject == who.Subject
}

// admits says whether e admits who: the operator the admin grant through
// the workbench token or an identity; a grant an identity holding it; any
// other kind nobody, a router's answer above all.
func (e PolicyEntry) admits(who Admittee) bool {
	switch e.Kind {
	case EntryOperator:
		return (who.Via == ViaWorkbench || who.Via == ViaIdentity) && who.holds(ext.GrantAdmin)
	case EntryGrant:
		return e.Grant != "" && who.Via == ViaIdentity && who.holds(ext.Grant(e.Grant))
	}
	return false
}

// operator is the operator entry's own rule, reading no policy: AdmissionOf
// and a response's transaction (respondTx) judge the operator by it.
func (who Admittee) operator() bool { return PolicyEntry{Kind: EntryOperator}.admits(who) }

// Admits says whether one of entries admits who.
func Admits(entries []PolicyEntry, who Admittee) bool {
	return slices.ContainsFunc(entries, func(e PolicyEntry) bool { return e.admits(who) })
}

// EntriesOf is rec's entries: its policy's authority, and the decide
// grant's entry beside it while its route target is the grant. A policy
// that cannot be read gives none, so it admits nobody.
func EntriesOf(rec ledger.DecisionRecord) []PolicyEntry {
	var p struct {
		Authority []PolicyEntry `json:"authority"`
	}
	if json.Unmarshal([]byte(rec.Policy), &p) != nil {
		return nil
	}
	if GivenToGrant(rec.Request) {
		p.Authority = append(p.Authority, PolicyEntry{Kind: EntryGrant, Grant: string(ext.GrantDecide)})
	}
	return p.Authority
}

// GivenToGrant says request, as the ledger keeps it, names the decide grant
// as its route target: its for is {label: "grant:decide"} with no person.
// It is decoded as checkRequest decodes it (encoding/json), so text it
// cannot decode names no target.
func GivenToGrant(request string) bool {
	var body struct {
		For *DecisionFor `json:"for"`
	}
	return json.Unmarshal([]byte(request), &body) == nil && body.For != nil && body.For.Label == GrantDecideLabel && body.For.Person == ""
}

// Admission is what AdmissionOf answers.
type Admission int

const (
	// AdmitNone: nothing of the decision, as one that does not exist.
	AdmitNone Admission = iota
	// AdmitWhole: the admin grant, DR1's routes as they are.
	AdmitWhole
	// AdmitOpen: the decide grant, admitted by an entry, the decision open.
	AdmitOpen
	// AdmitOutcome: the decide grant, admitted by an entry, the decision
	// answered or final, and a response of its own: its outcome alone.
	AdmitOutcome
)

// AdmissionOf is the one judgement of what who may read of rec and answer.
func AdmissionOf(rec ledger.DecisionRecord, who Admittee) Admission {
	switch {
	case who.operator():
		// The operator entry's own rule: the admin grant through the
		// workbench token or an identity, and no other way in.
		return AdmitWhole
	case !who.holds(ext.GrantDecide) || !Admits(EntriesOf(rec), who):
		return AdmitNone
	case rec.State == ledger.DecisionOpen:
		return AdmitOpen
	case slices.ContainsFunc(rec.Responses, who.Sent):
		return AdmitOutcome
	}
	return AdmitNone
}

// OutcomeOf is the outcome of who's own answers to rec (Sent), from the
// record whole, never naming a reason: applied when the application names
// its accepted response, whatever the state now; waiting while it is the
// accepted one and the decision answered; not_used once the decision is
// final without applying it; refused, with the newest one's code, when
// none of its responses was accepted; "" when it has none.
func OutcomeOf(rec ledger.DecisionRecord, who Admittee) (outcome, code string) {
	var app struct {
		Response int `json:"response"`
	}
	applied := rec.Applied != "" && json.Unmarshal([]byte(rec.Applied), &app) == nil
	own := false
	for _, r := range rec.Responses {
		if !who.Sent(r) {
			continue
		}
		own = true
		var rc ResponseReceipt
		_ = json.Unmarshal([]byte(r.Receipt), &rc)
		code = rc.Code
		if r.Status != ledger.ResponseAccepted {
			continue
		}
		switch {
		case applied && app.Response == r.K:
			return proto.OutcomeApplied, ""
		case rec.State == ledger.DecisionAnswered:
			return proto.OutcomeWaiting, ""
		}
		return proto.OutcomeNotUsed, ""
	}
	if !own {
		return "", ""
	}
	return proto.OutcomeRefused, code
}
