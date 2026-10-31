package ext

import (
	"context"
	"time"
)

// DecisionRouter says whom a question a session asks goes to (Switchman's
// team routing). Its free default answers the empty route: the people
// watching answer, and the first answer wins.
type DecisionRouter interface {
	Provider
	// Route is told, once per question, a question a session shows, and
	// answers at once whom it went to and why; it may carry an answer the
	// router already holds. It never waits for a person: an answer that
	// comes later goes through Host.Submit. Safe for concurrent use.
	Route(ctx context.Context, d Decision) (Route, error)
	// Resolved is told that a question Route was given no longer waits:
	// answered (by whoever), gone from the screen, or ended with its
	// session. Called in order, from one goroutine.
	Resolved(ctx context.Context, r Resolution)
}

// SessionRef names a session, with its crew reference when it is a
// member's or an attempt's.
type SessionRef struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	AgentID string `json:"agentId,omitempty"`
	Run     string `json:"run,omitempty"`
	Task    string `json:"task,omitempty"`
	Attempt int    `json:"attempt,omitempty"`
}

// Decision is a question as the session shows it.
type Decision struct {
	ID      string           `json:"id"` // the question's id: what an answer names
	Session SessionRef       `json:"session"`
	Message string           `json:"message"`           // at most 500 bytes
	Kind    string           `json:"kind,omitempty"`    // permission | prompt | ""
	Source  string           `json:"source,omitempty"`  // api, bell, osc, pattern, trust, question, …
	Options []DecisionOption `json:"options,omitempty"` // at most 6
	Plan    string           `json:"plan,omitempty"`    // the plan a question asks approval for
	Since   time.Time        `json:"since"`
}

// DecisionOption is one of a question's choices.
type DecisionOption struct {
	Label string `json:"label"`
	Input string `json:"input"` // the keys that pick it: what an Answer's Input names
}

// Route is whom a question went to and why, and an answer the router holds
// now. The empty Route is the free default.
type Route struct {
	To     []Destination `json:"to,omitempty"`     // at most MaxDestinations
	Rule   string        `json:"rule,omitempty"`   // why, at most MaxRouteRule bytes
	Answer *Answer       `json:"answer,omitempty"` // an answer the router holds now
}

// Destination is one whom a question went to.
type Destination struct {
	Kind string `json:"kind"` // person | role | agent | crew
	Name string `json:"name"` // at most MaxDestinationName characters
}

// Answer is a choice or a line, and who gave it.
type Answer struct {
	Input string    `json:"input,omitempty"` // one of the decision's options' Input
	Text  string    `json:"text,omitempty"`  // a line of at most MaxAnswerText bytes
	By    Principal `json:"by"`
}

// Resolution tells a router that a question it was given no longer waits.
type Resolution struct {
	ID      string     `json:"id"`
	Session SessionRef `json:"session"`
	Ended   bool       `json:"ended,omitempty"`
	At      time.Time  `json:"at"`
}

const (
	MaxDestinations    = 8
	MaxDestinationName = 60
	MaxRouteRule       = 200
	MaxAnswerText      = 4096 // proto.MaxSubmit
	RouteWait          = 5 * time.Second
)

// destinationKinds are the kinds a Destination may name.
var destinationKinds = map[string]bool{"person": true, "role": true, "agent": true, "crew": true}

// Valid checks r as an answer to d: at most MaxDestinations destinations,
// each of a known kind with a name of 1 to MaxDestinationName characters; a
// rule of one line of at most MaxRouteRule bytes; and an answer, when there
// is one, that is exactly one of a choice d offers (an option's Input) and
// a line of at most MaxAnswerText bytes of text (tabs and line ends its only
// control characters), given by a valid principal.
func (r Route) Valid(d Decision) error {
	const rec = "route"
	if len(r.To) > MaxDestinations {
		return invalid(rec, "%d destinations, past %d", len(r.To), MaxDestinations)
	}
	for _, to := range r.To {
		if !destinationKinds[to.Kind] {
			return invalid(rec, "destination kind %q is not person, role, agent or crew", to.Kind)
		}
		if err := name(rec, "destination name", to.Name, MaxDestinationName); err != nil {
			return err
		}
	}
	if err := line(rec, "rule", r.Rule, MaxRouteRule, false); err != nil {
		return err
	}
	if r.Answer == nil {
		return nil
	}
	a := r.Answer
	switch {
	case (a.Input == "") == (a.Text == ""):
		return invalid(rec, "an answer is exactly one of a choice and a line")
	case a.Input != "" && !offers(d, a.Input):
		return invalid(rec, "the answer's choice %q is not one the question %q offers", a.Input, d.ID)
	case a.Text != "":
		if err := typed(rec, "the answer's text", a.Text); err != nil {
			return err
		}
	}
	if err := a.By.Valid(); err != nil {
		return invalid(rec, "the answer's by: %v", err)
	}
	return nil
}

// offers reports whether input is the keys of one of d's options.
func offers(d Decision, input string) bool {
	for _, o := range d.Options {
		if o.Input == input {
			return true
		}
	}
	return false
}
