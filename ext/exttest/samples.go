package exttest

import (
	"fmt"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
)

// SampleDecision is the decision RunAll hands a router: a permission
// question of a crew's attempt with two choices.
func SampleDecision() ext.Decision {
	return ext.Decision{
		ID:      "exttest-q1",
		Session: ext.SessionRef{ID: "exttest-session", Name: "exttest", AgentID: "claude", Run: "exttest-run", Task: "build", Attempt: 1},
		Message: "Allow the edit of main.go?",
		Kind:    "permission",
		Source:  "pattern",
		Options: []ext.DecisionOption{{Label: "Yes", Input: "1"}, {Label: "No", Input: "2"}},
		Since:   time.Now().UTC(),
	}
}

// SampleDecisionRequest is a durable decision's record as a router is
// told it (Program item DR1): its id as ID and Request, kind decision, the
// asking attempt's session, and its options picked by their places.
func SampleDecisionRequest() ext.Decision {
	return ext.Decision{
		ID:      "dr-7",
		Request: "dr-7",
		Session: ext.SessionRef{ID: "exttest-session", Run: "exttest-run", Task: "build", Attempt: 1},
		Message: "Ship the schema change?",
		Kind:    "decision",
		Options: []ext.DecisionOption{{Label: "Approve", Input: "1"}, {Label: "Reject", Input: "2"}},
		Since:   time.Now().UTC(),
	}
}

// SampleQuery is the attempt RunAll asks a context provider about, its
// worktree dir (a t.TempDir(), since a provider may read it).
func SampleQuery(dir string) ext.ContextQuery {
	return ext.ContextQuery{
		Run:          "exttest-run",
		Crew:         "exttest",
		Task:         "build",
		Attempt:      1,
		Kind:         "work",
		Goal:         "Build the thing",
		Instructions: "Make the build pass.",
		Worktree:     dir,
		Dir:          dir,
		Start:        "0123456789abcdef0123456789abcdef01234567",
		Inputs:       []ext.ContextInput{{Task: "plan", Commit: "89abcdef0123456789abcdef0123456789abcdef"}},
	}
}

// FullQuery is q naming as many decisions and reviews as a query holds
// (ext.MaxContextDecisions, ext.MaxContextReviews of
// ext.MaxContextReviewFindings findings), each field at its bound and of
// every state, as a provider may be asked: Context checks a provider with
// it as well as with the query it is given.
func FullQuery(q ext.ContextQuery) ext.ContextQuery {
	states := []struct {
		state string
		final bool
	}{{"open", false}, {"answered", false}, {"applied", true}, {"superseded", true}, {"cancelled", true}, {"expired", true}}
	q.Decisions = make([]ext.DecisionState, ext.MaxContextDecisions)
	for i := range q.Decisions {
		s := states[i%len(states)]
		d := ext.DecisionState{Request: fmt.Sprintf("dr-%d", 999_999_999_999_999_000+i), State: s.state, Final: s.final, Rev: i + 1}
		if s.final {
			d.ClosedAt = time.Date(2026, 11, 1, 12, 0, i, 0, time.UTC)
		}
		q.Decisions[i] = d
	}
	findingStates := []string{ext.FindingOpen, ext.FindingResolved, ext.FindingWithdrawn}
	q.Reviews = make([]ext.ContextReview, ext.MaxContextReviews)
	for i := range q.Reviews {
		fs := make([]ext.ContextReviewFinding, ext.MaxContextReviewFindings)
		for k := range fs {
			fs[k] = ext.ContextReviewFinding{ID: fmt.Sprintf("%016x", 0xf1<<48|i<<16|k), State: findingStates[k%len(findingStates)]}
			if k%2 == 0 {
				fs[k].Path = "src/" + strings.Repeat("p", ext.MaxContextRef-len("src/"))
				fs[k].Line = ext.MaxContextLine - k
			}
		}
		q.Reviews[i] = ext.ContextReview{ID: fmt.Sprintf("%016x", 0xae<<48|i), Findings: fs}
	}
	return q
}

// SampleRecord is what a durable decision's record says of itself to a
// router: a plan's approval gate of a ticket, for its architecture owner.
func SampleRecord() *ext.DecisionRecord {
	return &ext.DecisionRecord{
		Type:    "approval",
		Origin:  "plan",
		Gate:    "schema",
		Ticket:  "CND-12",
		Project: "CND",
		Crew:    "exttest",
		For:     &ext.RecordFor{Label: "architecture owner", Responsibility: "architecture"},
		Subject: &ext.DecisionSubject{Kind: "plan", Key: "0123456789abcdef0123456789abcdef"},
	}
}

// SamplePolicy is a policy a router answers for SampleRecord: one group,
// the architecture owner, quorum 1, one person eligible by the role, and
// the operator may override.
func SamplePolicy() ext.RoutePolicy {
	return ext.RoutePolicy{
		Revision: "exttest-r1",
		Scope:    "exttest-scope",
		Pinned:   ext.PinnedRequirement{Rule: "r-1", Groups: []ext.PinnedGroup{{Role: "architecture", Quorum: 1}}, Override: ext.OverrideOperator, Notify: ext.NotifyNow},
		Groups:   []ext.Requirement{{Role: "architecture", Label: "architecture owner", Quorum: 1, Rule: "r-1", Eligible: []ext.Eligible{{Subject: "ana", Name: "Ana", Via: ext.ViaRole, Ref: "ref-1"}}}},
		Override: ext.OverrideOperator,
	}
}
