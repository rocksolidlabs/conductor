package exttest

import (
	"encoding/json"
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

// SampleHostSummary is a host summary of every field (ext.HostSummary):
// a server with two hosts reaching it, one connected and one away, none
// left out (More 0). A test
// hands it to a sync store as the hosts/summary document, or into a
// server as another machine's RemoteHosts record (SampleRemoteHosts).
func SampleHostSummary() ext.HostSummary {
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	return ext.HostSummary{
		Platform: "linux/amd64", Version: "v0.7.0",
		Sessions: ext.HostSessions{Running: 3, Waiting: 1},
		Runs:     ext.HostRuns{Held: 2, Waiting: 1},
		Hosts: []ext.HostSummaryRow{
			{Name: "studio", Platform: "darwin/arm64", Version: "v0.7.0", Availability: ext.HostConnected, Since: at, Sessions: ext.HostSessions{Running: 2, Waiting: 1}},
			{Name: "desk", Platform: "linux/amd64+wsl", Version: "v0.7.0", Availability: ext.HostAway, Since: at.Add(time.Hour), Sessions: ext.HostSessions{Running: 1}},
		},
	}
}

// SampleRemoteHosts is the RemoteHosts record a sync store hands in for
// device: its host summary, body as given (nil: SampleHostSummary's
// encoding). A test that needs a summary the server refuses gives its own
// body.
func SampleRemoteHosts(device, deviceName string, body []byte) ext.Remote {
	if body == nil {
		body = sampleHostSummaryJSON()
	}
	return ext.Remote{Kind: ext.RemoteHosts, Device: device, DeviceName: deviceName, ID: "summary", Rev: 1,
		At: time.Date(2026, 11, 20, 9, 5, 0, 0, time.UTC), Data: body}
}

// sampleHostSummaryJSON is SampleHostSummary encoded.
func sampleHostSummaryJSON() []byte {
	b, err := json.Marshal(SampleHostSummary())
	if err != nil {
		panic(err)
	}
	return b
}
