package exttest

import (
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
