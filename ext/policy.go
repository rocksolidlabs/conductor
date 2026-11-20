package ext

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"
	"unicode/utf8"
)

// A decision router's policy on a durable decision's record (SW1,
// docs/plans/decision-policy.md). A registered DecisionRouter that also
// implements PolicyConfirmer may answer a record's Route with a Policy: who
// may answer it. The server writes the policy into the record, lets only
// the persons it names answer while the router confirms it, asks again
// when the answer is applied, and answers under its own policy (the
// operator) whenever the router does not confirm it. A router that does not
// implement PolicyConfirmer gets the server's own policy whatever it
// answers: a Policy from it is refused as invalid.
//
// The seam's types are whole as of here for every row of the plan (a
// router's policy, the record it is told and the confirmation it gives,
// the open records and the outcome outbox it reads back, a re-route, a
// routed record's outcome, a notice for one person): the later rows serve
// them and change none of their shapes.

// DecisionRecord is what a durable decision's record says of itself to a
// router (Decision.Record): never set for a session's question.
type DecisionRecord struct {
	// Type is clarification, approval or assignment; Origin agent, plan or
	// compare (a comparison's selection, which only the operator answers:
	// a router may route it for the record's history, and a Policy it
	// answers for one is written nothing for).
	Type   string `json:"type"`
	Origin string `json:"origin"`
	// Gate is a plan's gate (at most MaxRecordKey bytes), Ticket a ticket's
	// key, Project its project's key, Crew the run's saved crew and Task
	// the asking task (at most MaxRecordKey bytes).
	Gate    string `json:"gate,omitempty"`
	Ticket  string `json:"ticket,omitempty"`
	Project string `json:"project,omitempty"`
	Crew    string `json:"crew,omitempty"`
	Task    string `json:"task,omitempty"`
	// For is whom the request names: an approved plan revision's (origin
	// plan, an assignment a person approved) or the requesting agent's own
	// (origin agent, a recommendation).
	For *RecordFor `json:"for,omitempty"`
	// Expertise is at most MaxRecordExpertise labels.
	Expertise []string `json:"expertise,omitempty"`
	// Subject is what the decision is about, by kind and key.
	Subject *DecisionSubject `json:"subject,omitempty"`
	// Recommended are the routes written before the router was told (by
	// the operator or by Brakeman), at most MaxRecommended.
	Recommended []Destination `json:"recommended,omitempty"`
}

// RecordFor is whom a record's request names: a crew's person's label (at
// most MaxRecordForLabel characters), the person it resolved to and the
// responsibility it holds.
type RecordFor struct {
	Label          string `json:"label"`
	Person         string `json:"person,omitempty"`
	Responsibility string `json:"responsibility,omitempty"`
}

// DecisionSubject is what a record is about: its kind (commit, candidate,
// artifact or plan) and Key, the first 32 hex digits of the SHA-256 of the
// subject's canonical JSON, so that a router tells two subjects apart, and
// finds one subject asked about twice, without holding a path.
type DecisionSubject struct {
	Kind string `json:"kind"`
	Key  string `json:"key"`
}

// Bounds of a record and of a policy.
const (
	MaxRecordKey       = 40
	MaxRecordForLabel  = 60
	MaxRecordExpertise = 4
	MaxExpertiseLabel  = 40
	MaxRecommended     = 4
	SubjectKeyLen      = 32

	MaxPolicyGroups   = 4
	MaxGroupQuorum    = 4
	MaxGroupEligible  = 16
	MaxPolicyEligible = 32
	MaxPolicyRecent   = 16
	MaxPolicy         = 14 << 10
	MaxPolicyScope    = 64
	MaxPolicyRevision = 40
	MaxRoleName       = 40
	MaxGroupLabel     = 60 // characters
	MaxPolicyRule     = 32
	MaxEligibleName   = 64 // characters
	MaxEligibleRef    = 32
	MaxPolicyGap      = 200
	MaxRouteChange    = 112 // a RouteChange as encoded: the longest why, a MaxPolicyRevision revision and a UTC time to the nanosecond, as the server writes one
	MaxConfirmWhy     = 200
	MinEscalateAfter  = 15 * time.Minute
	MaxEscalateAfter  = 7 * 24 * time.Hour

	// ConfirmWait bounds a Confirm, as IdentityWait bounds an
	// Authenticate: a router answers from what it holds.
	ConfirmWait = time.Second
	// MaxBehind is how long a router answers Behind, Routes, or a Revision
	// other than the record's for one record, counted from the first such
	// answer after one that was none of them: it then answers in force at
	// the record's revision, or not in force. The server holds a router to
	// it: past it, such an answer is taken as not in force.
	MaxBehind = 120 * time.Second
)

// The words of a policy.
const (
	OverrideOperator = "operator"
	OverrideNone     = "none"
	NotifyNow        = "now"
	NotifyBatch      = "batch"
	ViaAssigned      = "assigned"
	ViaRole          = "role"
	ViaDelegate      = "delegate"
	ViaBackup        = "backup"
)

// The kinds of a router's change of a record's policy (RerouteRequest.Why).
const (
	RerouteAssigned  = "assigned"
	RerouteDelegated = "delegated"
	RerouteEscalated = "escalated"
	Rerouted         = "rerouted"
)

// RoutePolicy is who may answer a record: groups, each a requirement with
// its quorum and the persons eligible to fill it, whether the server's
// operator may answer in its place (Override), and the requirement pinned
// for the record's life (Pinned).
type RoutePolicy struct {
	// Revision is the router's name for who may answer: two policies of
	// one revision are one policy. 1 to MaxPolicyRevision bytes of
	// printable ASCII.
	Revision string `json:"revision"`
	// Scope is the router's own name of what it routed the record for, 1
	// to MaxPolicyScope bytes of printable ASCII: kept and returned, never
	// read.
	Scope  string            `json:"scope"`
	Pinned PinnedRequirement `json:"pinned"`
	// Groups are Pinned's groups (the same roles and quorums, in order),
	// or, with Unsupported, one group of quorum 1 with nobody eligible and
	// its gap.
	Groups      []Requirement `json:"groups"`
	Distinct    bool          `json:"distinct,omitempty"`
	Override    string        `json:"override"`
	Unsupported bool          `json:"unsupported,omitempty"`
	// Reroutes and Recent are the server's (a router sends them empty):
	// the count of changes since the first route and the latest
	// MaxPolicyRecent of them past the history's.
	Reroutes int           `json:"reroutes,omitempty"`
	Recent   []RouteChange `json:"recent,omitempty"`
}

// Requirement is one group of a policy: a role (1 to MaxRoleName of a-z,
// 0-9 and '-'), its label for people, a quorum of 1 to MaxGroupQuorum, the
// rule that made it, its eligible persons, and its gap's words when it has
// nobody eligible.
type Requirement struct {
	Role     string     `json:"role"`
	Label    string     `json:"label"`
	Quorum   int        `json:"quorum"`
	Rule     string     `json:"rule"`
	Eligible []Eligible `json:"eligible,omitempty"`
	Gap      string     `json:"gap,omitempty"`
}

// Eligible is a person who may fill a group: a principal's subject, the
// name shown, how they are eligible (Via: assigned, role, delegate,
// backup), the subject they stand beside when a delegate, and the router's
// ref.
type Eligible struct {
	Subject string `json:"subject"`
	Name    string `json:"name"`
	Via     string `json:"via"`
	For     string `json:"for,omitempty"`
	Ref     string `json:"ref"`
}

// PinnedRequirement is what must be answered and when anyone is told,
// written with the first policy and kept for the record's life.
type PinnedRequirement struct {
	Rule          string        `json:"rule"`
	Groups        []PinnedGroup `json:"groups"`
	Distinct      bool          `json:"distinct,omitempty"`
	Override      string        `json:"override"`
	Notify        string        `json:"notify"`
	EscalateAfter time.Duration `json:"escalateAfter,omitempty"`
}

// PinnedGroup is a pinned group's role and quorum.
type PinnedGroup struct {
	Role   string `json:"role"`
	Quorum int    `json:"quorum"`
}

// RouteChange is one change of a record's policy, as the server keeps it.
type RouteChange struct {
	Why      string    `json:"why"`
	Revision string    `json:"revision"`
	At       time.Time `json:"at"`
}

// PolicyConfirmer is a DecisionRouter that stands behind the policies it
// answers. Confirm is answered from what the router holds, never a round
// trip, within ConfirmWait. Subject "" asks only whether the policy is in
// force; Since asks whether the subject stayed eligible since then (a
// router that cannot say answers not eligible). A router answers Behind,
// Routes, or a Revision other than the one asked about for one record for
// at most MaxBehind. Safe for concurrent use.
type PolicyConfirmer interface {
	Confirm(ctx context.Context, q ConfirmQuery) (Confirmation, error)
}

// ConfirmQuery asks a router about a record's policy at a revision.
type ConfirmQuery struct {
	Decision Decision  `json:"decision"`
	Revision string    `json:"revision"`
	Subject  string    `json:"subject,omitempty"`
	Since    time.Time `json:"since,omitzero"`
}

// Confirmation is a router's answer about a record's policy now.
type Confirmation struct {
	// InForce says the router stands behind its policy for the record now.
	InForce bool `json:"inForce"`
	// Behind: in force, but the router's own state is being refreshed: the
	// server answers "try again".
	Behind bool `json:"behind,omitempty"`
	// Routes: the record has no policy of the router's yet, and the
	// router will route it now.
	Routes bool `json:"routes,omitempty"`
	// Revision is the router's current revision when it differs from the
	// one asked about.
	Revision string `json:"revision,omitempty"`
	// Groups are the roles of every group the subject is eligible in now,
	// at most MaxPolicyGroups.
	Groups []string `json:"groups,omitempty"`
	// Why is one line of at most MaxConfirmWhy bytes, shown on the card.
	Why string `json:"why,omitempty"`
}

// DecisionOutcome is how a routed record ended: its final State (applied,
// superseded, cancelled, expired), By (a subject, "operator", or "" when
// no response decided it), Via (eligible, override, local, or "" with an
// empty By), the fresh decision that superseded it, and why.
type DecisionOutcome struct {
	State        string `json:"state"`
	By           string `json:"by,omitempty"`
	Via          string `json:"via,omitempty"`
	SupersededBy string `json:"supersededBy,omitempty"`
	Why          string `json:"why,omitempty"`
}

// The ways a routed record's answer came (DecisionOutcome.Via).
const (
	OutcomeEligible = "eligible"
	OutcomeOverride = "override"
	OutcomeLocal    = "local"
)

// OpenDecisionsPage is a page of the open records (Host.OpenDecisions).
type OpenDecisionsPage struct {
	Decisions []OpenDecision `json:"decisions"`
	Next      string         `json:"next,omitempty"`
}

// OpenDecision is an open record with its policy (nil under the server's
// own), when a router's policy was first written, the last route's why, its
// rev and its counted responses.
type OpenDecision struct {
	Decision Decision          `json:"decision"`
	Policy   *RoutePolicy      `json:"policy,omitempty"`
	RoutedAt time.Time         `json:"routedAt,omitzero"`
	Why      string            `json:"why,omitempty"`
	Rev      int               `json:"rev"`
	Counted  []CountedResponse `json:"counted,omitempty"`
}

// CountedResponse is a response that counted toward a group.
type CountedResponse struct {
	Subject   string    `json:"subject"`
	Groups    []string  `json:"groups"`
	At        time.Time `json:"at"`
	Confirmed bool      `json:"confirmed,omitempty"`
}

// OutcomesPage is a page of the outcome outbox (Host.Outcomes).
type OutcomesPage struct {
	Outcomes []OutboxEntry `json:"outcomes"`
	Next     uint64        `json:"next,omitempty"`
}

// OutboxEntry is one routed record's outcome, kept until the router
// acknowledges it.
type OutboxEntry struct {
	Seq      uint64          `json:"seq"`
	Decision Decision        `json:"decision"`
	Policy   *RoutePolicy    `json:"policy,omitempty"`
	RoutedAt time.Time       `json:"routedAt,omitzero"`
	Outcome  DecisionOutcome `json:"outcome"`
	ClosedAt time.Time       `json:"closedAt,omitzero"`
}

// RerouteRequest is a router's change of a record's policy: the record, the
// route with its new Policy, why (assigned, delegated, escalated or
// rerouted), the operation's id and the record's rev the router computed
// from.
type RerouteRequest struct {
	Decision    string `json:"decision"`
	Route       Route  `json:"route"`
	Why         string `json:"why"`
	OpID        string `json:"opId"`
	ExpectedRev int    `json:"expectedRev"`
}

// RerouteResult is what a re-route did: the record's rev now, Unchanged
// when its revision was the record's already, and the fresh decision that
// superseded it when one did.
type RerouteResult struct {
	Rev        int    `json:"rev"`
	Unchanged  bool   `json:"unchanged,omitempty"`
	Superseded string `json:"superseded,omitempty"`
}

// ErrStale says a re-route was computed from a rev the record moved past;
// it is wrapped with the current rev and revision.
var ErrStale = errors.New("ext: stale")

// RecipientSink is a NotificationSink that delivers a notice meant for one
// person (Notification.Recipient) to that person alone. A notice with a
// Recipient reaches only a sink whose DeliversToRecipients answers true.
type RecipientSink interface {
	DeliversToRecipients() bool
}

var (
	recordTypes   = []string{"clarification", "approval", "assignment"}
	recordOrigins = []string{"agent", "plan", "compare"}
	subjectKinds  = []string{"commit", "candidate", "artifact", "plan"}
	eligibleVias  = []string{ViaAssigned, ViaRole, ViaDelegate, ViaBackup}
	rerouteWhys   = []string{RerouteAssigned, RerouteDelegated, RerouteEscalated, Rerouted}
	outcomeStates = []string{"applied", "superseded", "cancelled", "expired"}
	outcomeVias   = []string{OutcomeEligible, OutcomeOverride, OutcomeLocal}
)

// printable checks a field of min to max bytes of printable ASCII, no space.
func printable(record, field, s string, min, max int) error {
	switch {
	case len(s) < min || len(s) > max:
		return invalid(record, "%s is %d bytes, not %d to %d", field, len(s), min, max)
	case !printableASCII(s):
		return invalid(record, "%s is not printable ASCII without spaces", field)
	}
	return nil
}

// Valid checks s: a known kind and a key of SubjectKeyLen lower-case hex.
func (s DecisionSubject) Valid() error {
	const rec = "decision subject"
	switch {
	case !slices.Contains(subjectKinds, s.Kind):
		return invalid(rec, "kind %q is not commit, candidate, artifact or plan", s.Kind)
	case !lowerHex(s.Key, SubjectKeyLen):
		return invalid(rec, "key is not %d lower-case hex digits", SubjectKeyLen)
	}
	return nil
}

// Valid checks r against its bounds.
func (r DecisionRecord) Valid() error {
	const rec = "decision record"
	switch {
	case !slices.Contains(recordTypes, r.Type):
		return invalid(rec, "type %q", r.Type)
	case !slices.Contains(recordOrigins, r.Origin):
		return invalid(rec, "origin %q", r.Origin)
	case r.Ticket != "" && !ticketKey(r.Ticket):
		return invalid(rec, "ticket %q is not a ticket's key", r.Ticket)
	case r.Project != "" && !projectKey(r.Project):
		return invalid(rec, "project %q is not a project's key", r.Project)
	case r.Crew != "" && !slug(r.Crew, MaxCrewID, false):
		return invalid(rec, "crew %q is not a crew id", r.Crew)
	case len(r.Expertise) > MaxRecordExpertise:
		return invalid(rec, "%d expertise labels, past %d", len(r.Expertise), MaxRecordExpertise)
	case len(r.Recommended) > MaxRecommended:
		return invalid(rec, "%d recommended routes, past %d", len(r.Recommended), MaxRecommended)
	}
	for _, f := range []struct{ field, s string }{{"gate", r.Gate}, {"task", r.Task}} {
		if err := line(rec, f.field, f.s, MaxRecordKey, false); err != nil {
			return err
		}
	}
	if r.For != nil {
		if err := name(rec, "for's label", r.For.Label, MaxRecordForLabel); err != nil {
			return err
		}
		for _, f := range []struct{ field, s string }{{"for's person", r.For.Person}, {"for's responsibility", r.For.Responsibility}} {
			if err := line(rec, f.field, f.s, MaxRecordForLabel, false); err != nil {
				return err
			}
		}
	}
	for _, e := range r.Expertise {
		if err := name(rec, "an expertise label", e, MaxExpertiseLabel); err != nil {
			return err
		}
	}
	if r.Subject != nil {
		if err := r.Subject.Valid(); err != nil {
			return invalid(rec, "%v", err)
		}
	}
	return Route{To: r.Recommended}.Valid(Decision{})
}

// role reports whether s is a role's name: 1 to MaxRoleName of a-z, 0-9
// and '-', not beginning with '-'.
func role(s string) bool { return slug(s, MaxRoleName, false) }

// Valid checks p as a policy for the record r: every bound of a policy,
// the groups following the pinned requirement (or the unsupported form),
// Distinct and Override equal to the pinned ones, and at most MaxPolicy
// bytes as encoded. Reroutes and Recent are checked against their bounds
// here; a router sends them empty (the server refuses one that does not).
func (p RoutePolicy) Valid(r DecisionRecord) error {
	const rec = "route policy"
	if err := r.Valid(); err != nil {
		return invalid(rec, "%v", err)
	}
	if err := printable(rec, "revision", p.Revision, 1, MaxPolicyRevision); err != nil {
		return err
	}
	if err := printable(rec, "scope", p.Scope, 1, MaxPolicyScope); err != nil {
		return err
	}
	if err := p.Pinned.valid(rec); err != nil {
		return err
	}
	switch {
	case p.Distinct != p.Pinned.Distinct:
		return invalid(rec, "distinct is not the pinned requirement's")
	case p.Override != p.Pinned.Override:
		return invalid(rec, "override is not the pinned requirement's")
	case p.Reroutes < 0:
		return invalid(rec, "reroutes is below 0")
	case len(p.Recent) > MaxPolicyRecent:
		return invalid(rec, "%d recent changes, past %d", len(p.Recent), MaxPolicyRecent)
	}
	if p.Unsupported {
		if len(p.Groups) != 1 || p.Groups[0].Quorum != 1 || len(p.Groups[0].Eligible) != 0 {
			return invalid(rec, "the unsupported form is one group of quorum 1 with nobody eligible")
		}
	} else {
		if len(p.Groups) != len(p.Pinned.Groups) {
			return invalid(rec, "%d groups for a requirement of %d", len(p.Groups), len(p.Pinned.Groups))
		}
		for i, g := range p.Groups {
			if g.Role != p.Pinned.Groups[i].Role || g.Quorum != p.Pinned.Groups[i].Quorum {
				return invalid(rec, "group %d is not the requirement's role and quorum", i+1)
			}
		}
	}
	eligible := 0
	for i, g := range p.Groups {
		if err := g.valid(rec, i+1); err != nil {
			return err
		}
		eligible += len(g.Eligible)
	}
	if eligible > MaxPolicyEligible {
		return invalid(rec, "%d eligible, past %d", eligible, MaxPolicyEligible)
	}
	for _, c := range p.Recent {
		if err := c.valid(rec); err != nil {
			return err
		}
	}
	b, err := json.Marshal(p)
	if err != nil {
		return invalid(rec, "%v", err)
	}
	if len(b) > MaxPolicy {
		return invalid(rec, "%d bytes as encoded, past %d", len(b), MaxPolicy)
	}
	return nil
}

func (p PinnedRequirement) valid(rec string) error {
	if err := printable(rec, "the pinned rule", p.Rule, 1, MaxPolicyRule); err != nil {
		return err
	}
	switch {
	case len(p.Groups) == 0 || len(p.Groups) > MaxPolicyGroups:
		return invalid(rec, "the requirement has %d groups, not 1 to %d", len(p.Groups), MaxPolicyGroups)
	case p.Override != OverrideOperator && p.Override != OverrideNone:
		return invalid(rec, "override %q is not operator or none", p.Override)
	case p.Notify != NotifyNow && p.Notify != NotifyBatch:
		return invalid(rec, "notify %q is not now or batch", p.Notify)
	case p.EscalateAfter != 0 && (p.EscalateAfter < MinEscalateAfter || p.EscalateAfter > MaxEscalateAfter):
		return invalid(rec, "escalateAfter is not 0 or from %s to %s", MinEscalateAfter, MaxEscalateAfter)
	}
	seen := map[string]bool{}
	for _, g := range p.Groups {
		switch {
		case !role(g.Role):
			return invalid(rec, "the pinned role %q is not a role's name", g.Role)
		case seen[g.Role]:
			return invalid(rec, "the pinned role %q is named twice", g.Role)
		case g.Quorum < 1 || g.Quorum > MaxGroupQuorum:
			return invalid(rec, "the pinned quorum %d is not 1 to %d", g.Quorum, MaxGroupQuorum)
		}
		seen[g.Role] = true
	}
	return nil
}

func (g Requirement) valid(rec string, i int) error {
	switch {
	case !role(g.Role):
		return invalid(rec, "group %d's role %q is not a role's name", i, g.Role)
	case g.Quorum < 1 || g.Quorum > MaxGroupQuorum:
		return invalid(rec, "group %d's quorum %d is not 1 to %d", i, g.Quorum, MaxGroupQuorum)
	case len(g.Eligible) > MaxGroupEligible:
		return invalid(rec, "group %d has %d eligible, past %d", i, len(g.Eligible), MaxGroupEligible)
	case (g.Gap == "") != (len(g.Eligible) > 0):
		return invalid(rec, "group %d has a gap's words exactly when nobody is eligible", i)
	}
	if err := name(rec, "a group's label", g.Label, MaxGroupLabel); err != nil {
		return err
	}
	if err := printable(rec, "a group's rule", g.Rule, 1, MaxPolicyRule); err != nil {
		return err
	}
	if err := line(rec, "a group's gap", g.Gap, MaxPolicyGap, false); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, e := range g.Eligible {
		if err := e.valid(rec); err != nil {
			return err
		}
		if seen[e.Subject] {
			return invalid(rec, "group %d names %q twice", i, e.Subject)
		}
		seen[e.Subject] = true
	}
	return nil
}

func (e Eligible) valid(rec string) error {
	if err := printable(rec, "an eligible's subject", e.Subject, 1, MaxSubject); err != nil {
		return err
	}
	if err := name(rec, "an eligible's name", e.Name, MaxEligibleName); err != nil {
		return err
	}
	if !slices.Contains(eligibleVias, e.Via) {
		return invalid(rec, "via %q is not assigned, role, delegate or backup", e.Via)
	}
	if (e.Via == ViaDelegate) != (e.For != "") {
		return invalid(rec, "an eligible names whom it stands beside exactly when it is a delegate")
	}
	if e.For != "" {
		if err := printable(rec, "a delegate's for", e.For, 1, MaxSubject); err != nil {
			return err
		}
	}
	return printable(rec, "an eligible's ref", e.Ref, 1, MaxEligibleRef)
}

func (c RouteChange) valid(rec string) error {
	if !slices.Contains(rerouteWhys, c.Why) {
		return invalid(rec, "a change's why %q", c.Why)
	}
	if err := printable(rec, "a change's revision", c.Revision, 1, MaxPolicyRevision); err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil || len(b) > MaxRouteChange {
		return invalid(rec, "a change is past %d bytes as encoded", MaxRouteChange)
	}
	return nil
}

// Valid checks c against its bounds: Behind and Groups only in force, at
// most MaxPolicyGroups roles of a role's form, none twice, a revision of
// printable ASCII or none, and a why of one line of at most MaxConfirmWhy
// bytes.
func (c Confirmation) Valid() error {
	const rec = "confirmation"
	switch {
	case c.Behind && !c.InForce:
		return invalid(rec, "behind is an answer in force")
	case len(c.Groups) > 0 && !c.InForce:
		return invalid(rec, "groups are an answer in force")
	case len(c.Groups) > MaxPolicyGroups:
		return invalid(rec, "%d groups, past %d", len(c.Groups), MaxPolicyGroups)
	}
	for i, g := range c.Groups {
		if !role(g) || slices.Contains(c.Groups[:i], g) {
			return invalid(rec, "group %q is not a role's name, or named twice", g)
		}
	}
	if c.Revision != "" {
		if err := printable(rec, "revision", c.Revision, 1, MaxPolicyRevision); err != nil {
			return err
		}
	}
	return line(rec, "why", c.Why, MaxConfirmWhy, false)
}

// Valid checks r: a decision's id, a route whose Policy is there, a why of
// the four kinds, an operation's id and a rev from 1. Whether the policy
// fits the record is the server's to answer.
func (r RerouteRequest) Valid() error {
	const rec = "reroute request"
	switch {
	case !decisionID(r.Decision):
		return invalid(rec, "decision %q is not a decision's id", r.Decision)
	case r.Route.Policy == nil:
		return invalid(rec, "a re-route carries a policy")
	case r.Route.Answer != nil:
		return invalid(rec, "a re-route carries no answer")
	case !slices.Contains(rerouteWhys, r.Why):
		return invalid(rec, "why %q is not assigned, delegated, escalated or rerouted", r.Why)
	case !opID(r.OpID):
		return invalid(rec, "opId %q is not an operation's id", r.OpID)
	case r.ExpectedRev < 1:
		return invalid(rec, "expectedRev is below 1")
	}
	return Route{To: r.Route.To, Rule: r.Route.Rule}.Valid(Decision{})
}

// decisionID reports whether s is dr-<n>, n from 1 with no leading zero.
func decisionID(s string) bool {
	if len(s) > MaxDecisionRef || len(s) < 4 || s[:3] != "dr-" {
		return false
	}
	return digits(s[3:], MaxDecisionRef-3)
}

// Valid checks o: a final state, By and Via empty together, Via one of
// eligible, override and local, a superseding decision only for
// superseded, By and SupersededBy of their shapes, and a why of one line
// of at most MaxConfirmWhy bytes.
func (o DecisionOutcome) Valid() error {
	const rec = "decision outcome"
	switch {
	case !slices.Contains(outcomeStates, o.State):
		return invalid(rec, "state %q is not a final state", o.State)
	case (o.By == "") != (o.Via == ""):
		return invalid(rec, "by and via are empty together")
	case o.Via != "" && !slices.Contains(outcomeVias, o.Via):
		return invalid(rec, "via %q", o.Via)
	case o.SupersededBy != "" && (o.State != "superseded" || !decisionID(o.SupersededBy)):
		return invalid(rec, "supersededBy names a decision of a superseded record")
	}
	if o.By != "" {
		if err := printable(rec, "by", o.By, 1, MaxSubject); err != nil {
			return err
		}
	}
	if !utf8.ValidString(o.Why) {
		return invalid(rec, "why is not UTF-8")
	}
	return line(rec, "why", o.Why, MaxDecisionWhyLine, false)
}

// MaxDecisionWhyLine bounds an outcome's why, in bytes.
const MaxDecisionWhyLine = 200

// Clone is r with its slices and pointers copied.
func (r *DecisionRecord) Clone() *DecisionRecord {
	if r == nil {
		return nil
	}
	c := *r
	if r.For != nil {
		f := *r.For
		c.For = &f
	}
	if r.Subject != nil {
		s := *r.Subject
		c.Subject = &s
	}
	c.Expertise = slices.Clone(r.Expertise)
	c.Recommended = slices.Clone(r.Recommended)
	return &c
}

// Clone is p with its slices copied.
func (p *RoutePolicy) Clone() *RoutePolicy {
	if p == nil {
		return nil
	}
	c := *p
	c.Pinned.Groups = slices.Clone(p.Pinned.Groups)
	c.Groups = slices.Clone(p.Groups)
	for i := range c.Groups {
		c.Groups[i].Eligible = slices.Clone(p.Groups[i].Eligible)
	}
	c.Recent = slices.Clone(p.Recent)
	return &c
}
