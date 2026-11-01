package brakeman

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestEveryRequestTransition walks every pair of states: the table's are
// taken, every other refused, a request's own state among them.
func TestEveryRequestTransition(t *testing.T) {
	allowed := map[[2]string]bool{}
	for from, tos := range map[string][]string{
		StateQueued:   {StateStarting, StateCancelled, StateOverBudget},
		StateStarting: {StateRunning, StateBriefed, StateEnded, StateFailed, StateTimedOut, StateCancelled, StateInterrupted},
		StateRunning:  {StateBriefed, StateTimedOut, StateEnded, StateFailed, StateCancelled, StateInterrupted},
	} {
		for _, to := range tos {
			allowed[[2]string{from, to}] = true
		}
	}
	// The ones the plan names among them.
	for _, p := range [][2]string{{StateStarting, StateBriefed}, {StateStarting, StateEnded}, {StateStarting, StateFailed}, {StateRunning, StateFailed}} {
		if !allowed[p] {
			t.Fatalf("%s to %s is not in the test's table", p[0], p[1])
		}
	}
	at := time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)
	for _, from := range States {
		for _, to := range States {
			r := Request{State: from}
			err := r.To(to, "why", at)
			if allowed[[2]string{from, to}] {
				if err != nil {
					t.Errorf("%s to %s: %v", from, to, err)
					continue
				}
				if r.State != to || r.Why != "why" {
					t.Errorf("%s to %s: %+v", from, to, r)
				}
				if Closed(to) != !r.ClosedAt.IsZero() {
					t.Errorf("%s to %s: closedAt %v", from, to, r.ClosedAt)
				}
				continue
			}
			if !errors.Is(err, ErrTransition) || r.State != from {
				t.Errorf("%s to %s: %v, state %s", from, to, err, r.State)
			}
		}
	}
	for _, s := range States {
		if Closed(s) == slices.Contains([]string{StateQueued, StateStarting, StateRunning}, s) {
			t.Errorf("%s closed %v", s, Closed(s))
		}
	}
	if Closed("nothing") {
		t.Error("an unknown state is closed")
	}
}

// TestAClosedRequestOnlyGainsMade: a closed request takes made entries,
// edges, a stray run and an empty run or session id, and nothing else:
// never a state, a why, a new act, or another run.
func TestAClosedRequestOnlyGainsMade(t *testing.T) {
	f := newFixture(t)
	r := f.start("op-1")
	f.close(r.ID, StateCancelled)
	ok := func(name string, fn func(*Request)) {
		t.Helper()
		if _, err := f.s.Update(r.ID, func(r *Request) error { fn(r); return nil }); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	refused := func(name string, fn func(*Request)) {
		t.Helper()
		before, _ := f.s.Get(r.ID)
		if _, err := f.s.Update(r.ID, func(r *Request) error { fn(r); return nil }); err == nil {
			t.Errorf("%s was taken by a closed request", name)
		}
		after, _ := f.s.Get(r.ID)
		if a, b := mustEncode(t, after), mustEncode(t, before); a != b {
			t.Errorf("%s: a refused change was written", name)
		}
	}
	ok("a made entry", func(r *Request) {
		r.Made = append(r.Made, Made{Item: 0, OpID: "m-1", Op: "bk1", State: MadePending, At: f.clock.now()})
	})
	ok("an edge", func(r *Request) { r.Edges = append(r.Edges, Edge{From: 0, To: 1, Op: "bk2", State: EdgePending}) })
	ok("an outcome", func(r *Request) { r.Made[0].State, r.Made[0].Key = MadeDone, "CND-1" })
	ok("a run id while empty", func(r *Request) { r.RunID = runA })
	ok("a session id while empty", func(r *Request) { r.SessionID = sessA })
	ok("a stray run", func(r *Request) { r.StrayRun = "team-0000000b" })
	refused("another run", func(r *Request) { r.RunID = "team-0000000c" })
	refused("another session", func(r *Request) { r.SessionID = "1111111111111111" })
	refused("another stray run", func(r *Request) { r.StrayRun = "team-0000000d" })
	refused("a state", func(r *Request) { r.State = StateBriefed })
	refused("a why", func(r *Request) { r.Why = "changed" })
	refused("a brief", func(r *Request) { r.HasBrief = true })
	refused("a new act", func(r *Request) {
		r.Acts = append(r.Acts, Act{OpID: "a-1", Operation: "run_retry", Target: ref(t, "run:"+runA), Op: "bk3", Args: strings.Repeat("0", 64), State: "pending"})
	})
	refused("the note", func(r *Request) { r.Note = "something else" })
	if _, err := f.s.Update(r.ID, func(r *Request) error { return r.To(StateRunning, "", f.clock.now()) }); !errors.Is(err, ErrTransition) {
		t.Errorf("a closed request moved on: %v", err)
	}
}

func mustEncode(t *testing.T, v any) string {
	t.Helper()
	b, err := encode(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestEachKindTakesItsSubjects: each kind takes the subjects, the note
// and the time it reads, and no other.
func TestEachKindTakesItsSubjects(t *testing.T) {
	run, task, attempt := ref(t, "run:"+runA), ref(t, "task:"+runA+"/build"), ref(t, "attempt:"+runA+"/build/1")
	sess, quest := ref(t, "session:"+sessA), ref(t, "question:"+sessA+"/"+questA)
	ticket, comment := ref(t, "ticket:CND-1"), ref(t, "comment:CND-1/"+commentA)
	check := ref(t, "check:"+runA+"/build/1/vet/1")
	since := time.Date(2026, 10, 31, 18, 0, 0, 0, zone)
	type ask struct {
		kind     string
		subjects []Ref
		note     string
		since    time.Time
		ok       bool
	}
	cases := []ask{
		{KindBriefing, nil, "", time.Time{}, true},
		{KindBriefing, nil, "anything new on the docs?", since, true},
		{KindBriefing, []Ref{run}, "", time.Time{}, false},
		{KindExplain, []Ref{run}, "", time.Time{}, true},
		{KindExplain, []Ref{task}, "", time.Time{}, true},
		{KindExplain, []Ref{attempt}, "", time.Time{}, true},
		{KindExplain, []Ref{sess}, "", time.Time{}, true},
		{KindExplain, []Ref{quest}, "", time.Time{}, true},
		{KindExplain, []Ref{ticket}, "", time.Time{}, true},
		{KindExplain, []Ref{check}, "", time.Time{}, false},
		{KindExplain, nil, "", time.Time{}, false},
		{KindExplain, []Ref{run, task}, "", time.Time{}, false},
		{KindExplain, []Ref{run}, "", since, false},
		{KindDecision, []Ref{quest}, "", time.Time{}, true},
		{KindDecision, []Ref{task}, "", time.Time{}, true},
		{KindDecision, []Ref{ticket}, "", time.Time{}, true},
		{KindDecision, []Ref{run}, "", time.Time{}, false},
		{KindDraftTickets, []Ref{ticket}, "", time.Time{}, true},
		{KindDraftTickets, []Ref{comment}, "", time.Time{}, true},
		{KindDraftTickets, nil, "we said: split the parser", time.Time{}, true},
		{KindDraftTickets, nil, "", time.Time{}, false},
		{KindDraftTickets, []Ref{run}, "", time.Time{}, false},
		{KindProposeCrew, []Ref{ticket}, "", time.Time{}, true},
		{KindProposeCrew, []Ref{comment}, "", time.Time{}, false},
		{KindProposeCrew, nil, "", time.Time{}, false},
		{KindHandoff, []Ref{task}, "", time.Time{}, true},
		{KindHandoff, []Ref{attempt}, "", time.Time{}, true},
		{KindHandoff, []Ref{sess}, "", time.Time{}, true},
		{KindHandoff, []Ref{ticket}, "", time.Time{}, false},
		{KindLocate, nil, "where is the retry policy?", time.Time{}, true},
		{KindLocate, nil, "", time.Time{}, false},
		{KindLocate, []Ref{run}, "where?", time.Time{}, false},
		{KindOverlaps, nil, "", time.Time{}, true},
		{KindOverlaps, []Ref{run}, "", time.Time{}, false},
		{KindAsk, nil, "why is it slow?", time.Time{}, true},
		{KindAsk, []Ref{run, task, sess, check, ticket, comment, quest, attempt, ref(t, "op:admin/o-1"), ref(t, "brief:br-1/0")}, "and these?", time.Time{}, true},
		{KindAsk, []Ref{run, run}, "twice", time.Time{}, false},
		{KindAsk, nil, "", time.Time{}, false},
		{"wake", nil, "", time.Time{}, false},
		{"nothing", nil, "", time.Time{}, false},
	}
	for i, c := range cases {
		r := Request{OpID: "op-1", Kind: c.kind, Subjects: c.subjects, Note: c.note, Since: c.since}
		err := r.checkAsked()
		if c.ok != (err == nil) {
			t.Errorf("case %d (%s, %d subjects, note %q, since %v): %v", i, c.kind, len(c.subjects), c.note, !c.since.IsZero(), err)
		}
	}
	// Eleven subjects is past the bound for any kind.
	many := make([]Ref, 11)
	for i := range many {
		many[i] = ref(t, "event:"+runA+"/"+string(rune('1'+i%9))+strings.Repeat("0", i/9))
	}
	r := Request{OpID: "op-1", Kind: KindAsk, Subjects: many, Note: "all"}
	wantInvalid(t, r.checkAsked(), "subjects")
	r = Request{OpID: "op-1", Kind: KindAsk, Note: pad("n", MaxNote)}
	if err := r.checkAsked(); err != nil {
		t.Errorf("a note at its bound: %v", err)
	}
	r = Request{OpID: "op-1", Kind: KindAsk, Note: pad("n", MaxNote+1)}
	wantInvalid(t, r.checkAsked(), "note")
	r = Request{OpID: "bad op", Kind: KindAsk, Note: "x"}
	wantInvalid(t, r.checkAsked(), "opId")
	r = Request{OpID: "op-1", Kind: KindAsk, Note: "x", By: pad("b", MaxBy+1)}
	wantInvalid(t, r.checkAsked(), "by")
}
