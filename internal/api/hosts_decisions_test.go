package api

import (
	"net/http"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The decisions waiting (docs/plans/hosts.md, PR-4), over DR1's records as
// its own writers make them: an attempt's requests, the admin's answers
// and a continuation's move.

// heldRunOf waits until the reply lists run id with the waiting count and
// answers the reply and the row.
func (e *testEnv) heldRunOf(t *testing.T, id, waiting string) (HostsReply, HostsRun) {
	t.Helper()
	var r HostsReply
	var run HostsRun
	waitUntil(t, "run "+id+" waiting "+waiting, func() bool {
		var ok bool
		r = e.hostsOf(t)
		run, ok = hostsRunOf(r, id)
		return ok && waitingOf(run) == waiting
	})
	return r, run
}

// Two open decisions and an answered one: 2 on the run and on this
// server's decisions, and no single link.
func TestHostsCountsOpenDecisions(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	for _, id := range []string{"r1", "r2", "r3"} {
		f.ask(t, clarificationBody(id), http.StatusCreated, "")
	}
	f.respond(t, adminToken, "dr-3", "a1", 0, 1, http.StatusCreated, "")
	f.decisionWhen(t, "dr-3", "no longer open", func(d map[string]any) bool { return d["state"] != ledger.DecisionOpen })
	r, run := f.heldRunOf(t, f.run, "2")
	if run.WaitingRef != "" || r.This.Decisions == nil || *r.This.Decisions != (HostsDecisions{Open: 2}) {
		t.Fatalf("two open: %+v, this %+v", run, r.This.Decisions)
	}
}

// I8: a decision counts on the run that holds it now. Open on A, A
// continued as B: B waits on it and links to it, A is not listed; one
// answered on A before the move is counted nowhere.
func TestHostsDecisionMovesWithContinuation(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	f.ask(t, clarificationBody("r1"), http.StatusCreated, "")
	f.ask(t, clarificationBody("r2"), http.StatusCreated, "")
	f.respond(t, adminToken, "dr-2", "a1", 0, 1, http.StatusCreated, "")
	f.decisionWhen(t, "dr-2", "no longer open", func(d map[string]any) bool { return d["state"] != ledger.DecisionOpen })
	if _, run := f.heldRunOf(t, f.run, "1"); run.WaitingRef != "decision:dr-1" {
		t.Fatalf("on A: %+v", run)
	}
	if err := f.srv.runs.Stop(t.Context(), f.run); err != nil {
		t.Fatal(err)
	}
	next, err := f.srv.runs.ContinueRun(t.Context(), f.run, "")
	if err != nil {
		t.Fatal(err)
	}
	f.decisionWhen(t, "dr-1", "moved to "+next.ID, func(d map[string]any) bool { return d["run"] == next.ID })
	r, run := f.heldRunOf(t, next.ID, "1")
	if run.WaitingRef != "decision:dr-1" || r.This.Decisions == nil || *r.This.Decisions != (HostsDecisions{Open: 1, WaitingRef: "decision:dr-1"}) {
		t.Fatalf("on B: %+v, this %+v", run, r.This.Decisions)
	}
	if a, ok := hostsRunOf(r, f.run); ok {
		t.Fatalf("A is still listed: %+v", a)
	}
}

// I8: a mirror is counted as its decision: open with its mirror showing,
// 1 on the run and none on this server's sessions, its link the
// decision's; answered, its mirror still showing, 0 and no link; then a
// member's own question: 1, its link the question's.
func TestHostsMirroredDecisionCountedOnce(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	f.ask(t, clarificationBody("r1"), http.StatusCreated, "")
	f.mirrored(t, "dr-1")
	r, run := f.heldRunOf(t, f.run, "1")
	if run.WaitingRef != "decision:dr-1" || r.This.Sessions.Waiting != 0 || r.This.Sessions.WaitingRef != "" {
		t.Fatalf("an open decision mirrored: %+v, this %+v", run, r.This.Sessions)
	}
	f.respond(t, adminToken, "dr-1", "a1", 0, 1, http.StatusCreated, "")
	f.decisionWhen(t, "dr-1", "no longer open", func(d map[string]any) bool { return d["state"] != ledger.DecisionOpen })
	r, run = f.heldRunOf(t, f.run, "0")
	if run.WaitingRef != "" || r.This.Sessions.Waiting != 0 || *r.This.Decisions != (HostsDecisions{}) {
		t.Fatalf("answered: %+v, this %+v %+v", run, r.This.Sessions, r.This.Decisions)
	}
	f.decisionWhen(t, "dr-1", "applied", inState(ledger.DecisionApplied))
	f.heldRunOf(t, f.run, "0")
	l := f.local(f.sid)
	waitUntil(t, "the mirror gone", func() bool { return l.Info().Attention.Decision == "" })
	l.SetAttentionFull(session.AttentionNeedsInput, "Go on?", session.SourceAPI, session.KindPrompt, dbChoices)
	q := l.Info().Attention.ID
	if _, run := f.heldRunOf(t, f.run, "1"); run.WaitingRef != questionRef(f.sid, q) {
		t.Fatalf("a member's own question: %+v", run)
	}
}

// A lone decision a continuation inherited links to its record though no
// session mirrors it (the continuation's attempt waits for it, so none
// runs); two such decisions: 2 and no link.
func TestHostsLoneGateLinksWithoutSession(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	f.ask(t, clarificationBody("r1"), http.StatusCreated, "")
	f.ask(t, clarificationBody("r2"), http.StatusCreated, "")
	if err := f.srv.runs.Stop(t.Context(), f.run); err != nil {
		t.Fatal(err)
	}
	next, err := f.srv.runs.ContinueRun(t.Context(), f.run, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, did := range []string{"dr-1", "dr-2"} {
		f.decisionWhen(t, did, "moved", func(d map[string]any) bool { return d["run"] == next.ID })
	}
	r, run := f.heldRunOf(t, next.ID, "2")
	if run.WaitingRef != "" || *r.This.Decisions != (HostsDecisions{Open: 2}) {
		t.Fatalf("two on B: %+v, this %+v", run, r.This.Decisions)
	}
	f.respond(t, adminToken, "dr-2", "a1", 0, 1, http.StatusCreated, "")
	f.decisionWhen(t, "dr-2", "no longer open", func(d map[string]any) bool { return d["state"] != ledger.DecisionOpen })
	r, run = f.heldRunOf(t, next.ID, "1")
	for _, info := range f.srv.registry.List() {
		if info.Attention.Decision != "" && !info.Status.Ended() {
			t.Fatalf("a session mirrors %s: %+v", info.Attention.Decision, info)
		}
	}
	if run.WaitingRef != "decision:dr-1" || *r.This.Decisions != (HostsDecisions{Open: 1, WaitingRef: "decision:dr-1"}) {
		t.Fatalf("the lone gate: %+v, this %+v", run, r.This.Decisions)
	}
}

// An open decision of a stopped run is counted, and linked, on this
// server's decisions though no run row lists it: a stop ends no decision,
// which can still be answered, and Needs your decision lists it.
func TestHostsStoppedRunsDecisionCounted(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	f.ask(t, clarificationBody("r1"), http.StatusCreated, "")
	if err := f.srv.runs.Stop(t.Context(), f.run); err != nil {
		t.Fatal(err)
	}
	var r HostsReply
	waitUntil(t, "the stopped run gone from the list", func() bool {
		r = f.hostsOf(t)
		_, listed := hostsRunOf(r, f.run)
		return !listed
	})
	f.decisionWhen(t, "dr-1", "still open", inState(ledger.DecisionOpen))
	if r.This.Decisions == nil || *r.This.Decisions != (HostsDecisions{Open: 1, WaitingRef: "decision:dr-1"}) {
		t.Fatalf("this server's decisions: %+v", r.This.Decisions)
	}
	b, ok := f.srv.hostSummaryBytes(t.Context())
	if !ok {
		t.Fatal("no summary")
	}
	if sum, err := ext.ParseHostSummary(b); err != nil || sum.Runs.Held != 0 {
		t.Fatalf("the summary: %s %v", b, err)
	}
}
