package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/history"
	"github.com/rocksolidlabs/conductor/internal/journal"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The history's Dispatch and Brakeman sources, availability and search
// routes (docs/plans/history.md, PR-4). Every wait is on the indexer
// (settle, reconcileNow) or on a store's own rows, never on a clock.

// hsGet is GET path?query as the admin: its status and its reply.
func hsGet(t *testing.T, e *testEnv, path string, query url.Values, token string) (int, map[string]any) {
	t.Helper()
	p := path
	if len(query) > 0 {
		p += "?" + query.Encode()
	}
	resp, out := e.do("GET", p, token, nil)
	return resp.StatusCode, out
}

// hsSearch searches e's history for the line, failing t unless it
// answers 200, and answers the reply.
func hsSearch(t *testing.T, e *testEnv, line string) map[string]any {
	t.Helper()
	status, out := hsGet(t, e, "/api/history", url.Values{"q": {line}, "limit": {"100"}}, adminToken)
	if status != http.StatusOK {
		t.Fatalf("search %q: %d %v", line, status, out)
	}
	return out
}

// hsKeys is a reply's entries' keys, in their order.
func hsKeys(out map[string]any) []string {
	keys := []string{}
	es, _ := out["entries"].([]any)
	for _, x := range es {
		keys = append(keys, x.(map[string]any)["key"].(string))
	}
	return keys
}

// hsEntry is one entry of a reply by its key, nil when it holds none.
func hsEntry(out map[string]any, key string) map[string]any {
	es, _ := out["entries"].([]any)
	for _, x := range es {
		if m := x.(map[string]any); m["key"] == key {
			return m
		}
	}
	return nil
}

// hsEntryOf reads one entry through /api/history/entry.
func hsEntryOf(t *testing.T, e *testEnv, key string) map[string]any {
	t.Helper()
	status, out := hsGet(t, e, "/api/history/entry", url.Values{"key": {key}}, adminToken)
	if status != http.StatusOK {
		t.Fatalf("the entry %s: %d %v", key, status, out)
	}
	return out["entry"].(map[string]any)
}

// hsErr is an error reply's code and words.
func hsErr(out map[string]any) (code, message string) {
	e, _ := out["error"].(map[string]any)
	c, _ := e["code"].(string)
	m, _ := e["message"].(string)
	return c, m
}

func availOf(m map[string]any) map[string]any {
	a, _ := m["availability"].(map[string]any)
	return a
}

// A ticket's entries: its record, a comment said, the approvals,
// supersession and withdrawal decided with the operator, a deleted
// comment its place and number without its words, and a tombstoned
// ticket deleted; a verified delivery checked with its candidate.
func TestTicketSourcesAreClassified(t *testing.T) {
	p := newPlanTest(t, nil)
	saved := p.saveCrew(t, teamCrew("Team", "cat"))
	r1, r2 := p.proposed(t, saved.ID, nil), p.proposed(t, saved.ID, nil)
	p.expect(t, "POST", "/api/dispatch/plans/"+r1+"/approve", map[string]any{"opId": "approve-1", "byName": "Nate", "note": "Looks right."}, http.StatusOK, "")
	p.expect(t, "POST", "/api/dispatch/plans/"+r2+"/approve", map[string]any{"opId": "approve-2"}, http.StatusOK, "")
	p.expect(t, "POST", "/api/dispatch/plans/"+r2+"/withdraw", map[string]any{"opId": "withdraw-2", "byName": "Nate"}, http.StatusOK, "")
	said := entityOf(t, p.expect(t, "POST", "/api/dispatch/tickets/CND-1/comments", map[string]any{"opId": opID(), "body": "the turnstile sticks"}, http.StatusCreated, ""), "comment")
	gone := entityOf(t, p.expect(t, "POST", "/api/dispatch/tickets/CND-1/comments", map[string]any{"opId": opID(), "body": "quagmire words"}, http.StatusCreated, ""), "comment")
	p.expect(t, "POST", "/api/dispatch/tickets/CND-1/comments/"+gone["id"].(string)+"/delete", map[string]any{"opId": opID()}, http.StatusOK, "")
	histSettle(t, p.testEnv)

	c := hsEntry(hsSearch(t, p.testEnv, "turnstile"), hCommentRef("CND-1", said["id"].(string)))
	if c == nil || c["class"] != history.ClassSaid || c["kind"] != "comment" || c["url"] != "/dispatch/CND/1?comment=1" {
		t.Fatalf("the comment: %v", c)
	}
	if keys := hsKeys(hsSearch(t, p.testEnv, "quagmire")); len(keys) != 0 {
		t.Fatalf("a deleted comment's words are found: %v", keys)
	}
	dc := hsEntryOf(t, p.testEnv, hCommentRef("CND-1", gone["id"].(string)))
	if dc["outcome"] != "deleted" || availOf(dc)["state"] != availDeleted || strings.Contains(fmt.Sprint(dc), "quagmire") {
		t.Fatalf("the deleted comment: %v", dc)
	}
	decided := hsSearch(t, p.testEnv, "ticket:CND-1 class:decided")
	var kinds []string
	for _, k := range hsKeys(decided) {
		x := hsEntry(decided, k)
		if x["actor"] != adminActor || x["class"] != history.ClassDecided {
			t.Fatalf("a decision without the operator: %v", x)
		}
		kinds = append(kinds, x["kind"].(string)+" "+x["revision"].(map[string]any)["label"].(string))
	}
	sort.Strings(kinds)
	if want := []string{"plan.approved r1", "plan.approved r2", "plan.superseded r1", "plan.withdrawn r2"}; !slices.Equal(kinds, want) {
		t.Fatalf("the decisions: %v, want %v", kinds, want)
	}
	if a := hsEntry(decided, "ticket:CND-1#plan.approved/r1/approve-1"); a == nil || !strings.Contains(fmt.Sprint(hsEntryOf(t, p.testEnv, a["key"].(string))), "r1 of CND-1 approved by Nate") {
		t.Fatalf("the first approval: %v", a)
	}
	if hsEntry(hsSearch(t, p.testEnv, "Looks right"), "ticket:CND-1#plan.approved/r1/approve-1") == nil {
		t.Fatal("the approval's note is not found")
	}
	// The tombstone.
	p.expect(t, "POST", "/api/dispatch/tickets/CND-1/delete", map[string]any{"opId": opID()}, http.StatusOK, "")
	histSettle(t, p.testEnv)
	if tk := hsEntryOf(t, p.testEnv, "ticket:CND-1"); tk["outcome"] != "deleted" || availOf(tk)["state"] != availDeleted || tk["class"] != history.ClassRecord {
		t.Fatalf("the tombstone: %v", tk)
	}

	// A verified delivery, as Dispatch keeps one: checked, its commit the
	// candidate's, delivering the candidate's attempt.
	exit := 0
	v, _ := json.Marshal(dispatch.Verified{At: histNow, Passed: true, Attempt: 2, Receipts: []dispatch.VerifiedReceipt{{Phase: "check", ID: "unit", K: 1, Where: "native", Exit: &exit, Stable: true}}})
	cand := "hist-00000020/build/2@" + commitID(2)
	entries, links := ticketEntries(ticketRead{v: dispatch.TicketView{Ticket: dispatch.Ticket{Key: "CND-7", Title: "Deliver", CreatedAt: histNow},
		Links: []dispatch.Link{{ID: "0000000000000001", Kind: dispatch.LinkDelivery, Candidate: cand, Verified: v, CreatedAt: histNow, CreatedBy: adminActor}}}})
	ix := histSettle(t, p.testEnv)
	if err := ix.Replace(t.Context(), history.Source{Key: "ticket:CND-7", Mark: "Q1"}, entries, links); err != nil {
		t.Fatal(err)
	}
	d := histEntry(t, ix, "ticket:CND-7#delivery/0000000000000001")
	if d.Class != history.ClassChecked || d.Outcome != "passed" || d.Commit != commitID(2) || !slices.Equal(histOut(t, ix, "ticket:CND-7", relDelivers), []string{attemptKey("hist-00000020", "build", 2)}) {
		t.Fatalf("the delivery: %+v %v", d.Entry, histOut(t, ix, d.Ref, relDelivers))
	}
}

// keepBrief keeps a brief of items on e's Brakeman store, its turn begun
// under cfg and its reads noted, and answers its request's id.
func keepBrief(t *testing.T, e *testEnv, cfg brakeman.Config, reads []brakeman.Read, items ...brakeman.Item) string {
	t.Helper()
	st := e.srv.brakeman.st
	r, _, err := st.Create(brakeman.Request{OpID: fmt.Sprintf("hist-%d", time.Now().UnixNano()), Kind: brakeman.KindBriefing, Note: "what did the night bring, codeword pumpernickel"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Start(r.ID, cfg, brakeman.Stamp{AgentID: "cat"}); err != nil {
		t.Fatal(err)
	}
	if len(reads) > 0 {
		if _, err := st.NoteReads(r.ID, reads...); err != nil {
			t.Fatal(err)
		}
	}
	b := brakeman.Brief{Schema: brakeman.BriefSchema, Title: "Overnight", Summary: "One run failed.", Items: items}
	if _, err := st.KeepBrief(r.ID, brakeman.Kept{OpID: "brief-1", Brief: b}); err != nil {
		t.Fatal(err)
	}
	return r.ID
}

// A brief's facts, interpretations, proposals and outcomes are said, each
// citing the refs it names; the person's ask is never indexed; a brief
// whose turn saw the terminals keeps its items' words out.
func TestBriefItemsAreSaidAndCiteTheirRefs(t *testing.T) {
	e := newTestEnv(t, nil)
	cfg := brakeman.DefaultConfig(e.root)
	id := keepBrief(t, e, cfg, nil,
		fact("the turnstile test failed overnight", ref(t, "run:hist-00000021"), ref(t, "ticket:CND-3")),
		brakeman.Item{Kind: brakeman.ItemInterpretation, Interpretation: &brakeman.Interpretation{Text: "the turnstile is flaky", Facts: []int{0}}},
		panel(ref(t, "run:hist-00000021")))
	cfg.Visibility.Terminals = true
	screen := brakeman.Read{Ref: ref(t, "session:00000000000000c1"), Class: brakeman.ClassTerminals, At: histNow}
	seen := keepBrief(t, e, cfg, []brakeman.Read{screen}, fact("the screen said hunter2 on the turnstile", ref(t, "run:hist-00000022")))
	ix := histSettle(t, e)
	out := hsSearch(t, e, "turnstile")
	f := hsEntry(out, hBriefItemRef(id, 0))
	if f == nil || f["class"] != history.ClassSaid || f["kind"] != "brief.item" || f["url"] != "/brakeman?request="+id+"#item-0" {
		t.Fatalf("the fact: %v (%v)", f, hsKeys(out))
	}
	if got := histOut(t, ix, hBriefItemRef(id, 0), relCites); !slices.Equal(got, []string{"run:hist-00000021", "ticket:CND-3"}) {
		t.Fatalf("the fact cites %v", got)
	}
	if hsEntry(out, hBriefItemRef(id, 1)) == nil {
		t.Fatalf("the interpretation: %v", hsKeys(out))
	}
	histGone(t, ix, hBriefItemRef(id, 2)) // an evidence panel is no account
	if keys := hsKeys(hsSearch(t, e, "pumpernickel")); len(keys) != 0 {
		t.Fatalf("the person's ask is indexed: %v", keys)
	}
	if keys := hsKeys(hsSearch(t, e, "hunter2")); len(keys) != 0 {
		t.Fatalf("a brief that saw the terminals is searchable by its words: %v", keys)
	}
	if h := histEntry(t, ix, hBriefItemRef(seen, 0)); h.Body != "" || !slices.Equal(histOut(t, ix, hBriefItemRef(seen, 0), relCites), []string{"run:hist-00000022"}) {
		t.Fatalf("the terminals' brief: %+v", h.Entry)
	}
	if a := availOf(hsEntryOf(t, e, hBriefItemRef(id, 0))); a["state"] != availKept {
		t.Fatalf("the brief's availability: %v", a)
	}
}

// Brakeman's own runs and sessions are indexed, and left out of the
// search unless it names a crew or asks for brief items.
func TestBrakemansRunsAreLeftOutByDefault(t *testing.T) {
	e := newTestEnv(t, nil)
	saveRecord(t, e.srv, crew.Run{ID: "brakeman-00000001", CrewID: crew.ClassBrakeman, Name: "Brakeman", Label: "turnstile turn", Class: crew.ClassBrakeman,
		Request: "br-1", Isolation: crew.IsolationNone, State: crew.RunStopped, Cwd: e.root, StartedAt: histNow})
	saveRecord(t, e.srv, crew.Run{ID: "hist-00000023", Label: "turnstile by a person", Cwd: e.root, StartedAt: histNow})
	histSettle(t, e)
	if keys := hsKeys(hsSearch(t, e, "turnstile")); !slices.Equal(keys, []string{"run:hist-00000023"}) {
		t.Fatalf("the default search: %v", keys)
	}
	if keys := hsKeys(hsSearch(t, e, "turnstile crew:brakeman")); !slices.Equal(keys, []string{"run:brakeman-00000001"}) {
		t.Fatalf("crew:brakeman: %v", keys)
	}
	if keys := hsKeys(hsSearch(t, e, "turnstile kind:run,brief.item")); len(keys) != 2 {
		t.Fatalf("kind:brief.item: %v", keys)
	}
}

// For every ref Brakeman's refURL links to a page, the history opens the
// same page; for the four whose refURL is an API route or no page of that
// evidence, the history's own; no URL is an API route.
func TestHistoryURLsAgreeWithBrakemans(t *testing.T) {
	listed := func(string) bool { return true }
	for _, r := range []string{"run:hist-00000024", "task:hist-00000024/build", "attempt:hist-00000024/build/2", "candidate:hist-00000024/build/2",
		"message:hist-00000024/3", "session:0123456789abcdef", "question:0123456789abcdef/fedcba9876543210", "ticket:CND-12",
		"plan:CND-12/2", "brief:br-4/1"} {
		br := ref(t, r)
		if got, want := historyURL("", r, r, 0, "", listed), refURL(br); got != want || want == "" {
			t.Errorf("%s: %q, Brakeman's %q", r, got, want)
		}
	}
	for _, c := range []struct{ kind, key, ref, title, want string }{
		{"check", "check:hist-00000024/build/1/unit/2", "check:hist-00000024/build/1/unit/2", "", "/runs/hist-00000024?view=tasks&task=build&attempt=1&step=check%2Funit%2F2"},
		{"verify", "check:hist-00000024/build/1/lint/1", "check:hist-00000024/build/1/lint/1", "", "/runs/hist-00000024?view=tasks&task=build&attempt=1&step=verify%2Flint%2F1"},
		{"step", "check:hist-00000024/build/1/db/1", "check:hist-00000024/build/1/db/1", "setup step db on build #1", "/runs/hist-00000024?view=tasks&task=build&attempt=1&step=setup%2Fdb%2F1"},
		{"command", "run:hist-00000024#command/admin/c1", "command:hist-00000024/c1", "", "/runs/hist-00000024?command=c1&actor=admin"},
	} {
		if got := historyURL(c.kind, c.key, c.ref, 0, c.title, listed); got != c.want {
			t.Errorf("%s: %q, want %q", c.key, got, c.want)
		}
	}
	if got := historyURL("comment", "comment:CND-12/0123456789abcdef", "comment:CND-12/0123456789abcdef", 3, "", listed); got != "/dispatch/CND/12?comment=3" {
		t.Errorf("a comment: %q", got)
	}
	if got := historyURL("session.chat", "session:0123456789abcdef#e4", "session:0123456789abcdef", 4, "", func(string) bool { return false }); got != "/history/sessions/0123456789abcdef#e4" {
		t.Errorf("a record's entry: %q", got)
	}
	e := newTestEnv(t, nil)
	av := e.srv.newAvailability(t.Context(), nil)
	launch := e.srv.serveEntry(av, history.Entry{Key: "launch:0123456789abcdef", Kind: "launch", Ref: "launch:0123456789abcdef", Ticket: "CND-12", Run: "hist-00000024", At: histNow, Title: "launch"}, "", history.Repeats{}, history.Links{}, false)
	if launch.URL != "/runs/hist-00000024" || strings.HasPrefix(refURL(ref(t, "launch:0123456789abcdef")), "/api/") == false {
		t.Errorf("a launch: %q", launch.URL)
	}
}

// A session's availability: live while it runs, listed once it ended,
// its record alone after a restart, lost when the server stopped
// observing it, host away for a hosted one, forgotten once its record is
// gone.
func TestAvailabilityOfASession(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	histSettle(t, e)
	if a := availOf(hsEntryOf(t, e, hSessionRef(id))); a["state"] != availLive {
		t.Fatalf("running: %v", a)
	}
	e.stopAndWait(id)
	histSettle(t, e)
	if a := availOf(hsEntryOf(t, e, hSessionRef(id))); a["state"] != availListed {
		t.Fatalf("ended: %v", a)
	}
	e2 := restartServer(t, e)
	histSettle(t, e2)
	if a := availOf(hsEntryOf(t, e2, hSessionRef(id))); a["state"] != availRecord {
		t.Fatalf("after a restart: %v", a)
	}
	av := e2.srv.newAvailability(t.Context(), nil)
	if a := av.session(id, true, history.Entry{Outcome: journal.EndingLost}); a.State != availLost {
		t.Fatalf("lost: %v", a)
	}
	if a := av.session(id, true, history.Entry{Outcome: journal.EndingHostAway, Hosted: true}); a.State != availHostAway {
		t.Fatalf("host away: %v", a)
	}
	if resp, _ := e2.do("DELETE", "/api/history/sessions/"+id, adminToken, nil); resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("forget: %d", resp.StatusCode)
	}
	if a := av.session(id, true, history.Entry{}); a.State != availForgotten {
		t.Fatalf("forgotten: %v", a)
	}
}

// A receipt's availability: its log kept, dropped by the run's bound, or
// pruned with the run's rows.
func TestAvailabilityOfACheck(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	histCreate(t, ld, "hist-00000025", "", histCrew(e.root))
	failedAt(t, ld, "hist-00000025", 1, 1, "TestKept")
	dropped := histReceipt("build", 1, "unit", 2, commitID(1), 1, "dropped")
	dropped.LogDropped, dropped.Log = true, nil
	histTx(t, ld, "hist-00000025", func(tx *ledger.Tx) error { return tx.PutCheck(dropped) })
	histSettle(t, e)
	kept := hsEntryOf(t, e, hCheckRef("hist-00000025", "build", 1, "unit", 1))
	if a := availOf(kept); a["state"] != availLog || a["log"].(map[string]any)["sha256"] == "" {
		t.Fatalf("kept: %v", a)
	}
	if a := availOf(hsEntryOf(t, e, hCheckRef("hist-00000025", "build", 1, "unit", 2))); a["state"] != availLogDropped {
		t.Fatalf("dropped: %v", a)
	}
	av := e.srv.newAvailability(t.Context(), nil)
	if a := av.check(ref(t, hCheckRef("hist-00000099", "build", 1, "unit", 1)), false); a.State != availPruned {
		t.Fatalf("pruned: %v", a)
	}
}

// An attempt that ran in another Runtime (its where_ran a fake Runtime's)
// is said as its row has it and its worktree never looked at; a native
// one's is stat'd.
func TestAWhereOtherThanNativeIsNotLookedAt(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	histCreate(t, ld, "hist-00000026", "", histCrew(e.root))
	there := filepath.Join(e.root, "wt")
	if err := os.MkdirAll(there, 0o700); err != nil {
		t.Fatal(err)
	}
	histTx(t, ld, "hist-00000026", func(tx *ledger.Tx) error {
		for i, w := range []string{"boxcar", ledger.WhereNative, ledger.WhereNative} {
			a := histAttempt("build", i+1, ledger.AttemptFailed, "", commitID(0), "")
			a.Where, a.Worktree = w, there
			if i == 2 {
				a.Worktree = filepath.Join(e.root, "gone")
			}
			if err := tx.PutAttempt(a); err != nil {
				return err
			}
		}
		return nil
	})
	histSettle(t, e)
	for n, want := range map[int]string{1: availElsewhere, 2: availWorktree, 3: availWorktreeGone} {
		a := availOf(hsEntryOf(t, e, attemptKey("hist-00000026", "build", n)))
		if a["state"] != want {
			t.Fatalf("attempt %d: %v, want %s", n, a, want)
		}
		if n == 1 && a["words"] != "on boxcar: not looked at from here" {
			t.Fatalf("the other Runtime's words: %v", a)
		}
	}
}

// Resume from a record is offered by the resume routes' own rules
// (resumeCheck), each refusal in their words, and the record's: never a
// hosted session, a crew member, a managed attempt or a Brakeman turn; never
// a launch whose arguments were not kept or in a Runtime the server lacks;
// a lost record with the person's second yes.
func TestResumeOfferedByTheResumeRoutesRules(t *testing.T) {
	recipe := catalog.Agent{ID: "rec", Name: "Recipe", Command: []string{"/bin/sh", "-c", "exec cat"}, AllowArgs: true,
		Session: &catalog.SessionRecipe{StartArgs: []string{"--session-id", "{id}"}, IDFrom: "hook", ResumeArgs: []string{"--resume", "{id}"}, IDPattern: "^[0-9a-f-]+$"}}
	needs := recipe
	needs.ID, needs.Session = "rec-cwd", &catalog.SessionRecipe{StartArgs: []string{"--session-id", "{id}"}, IDFrom: "hook", ResumeArgs: []string{"--resume", "{id}"}, IDPattern: "^[0-9a-f-]+$", ResumeNeedsCwd: true}
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), []catalog.Agent{recipe, needs})
	conv := &journal.AgentSession{ID: "0a1b2c3d-0000-4000-8000-000000000001", Resumable: true}
	base := func() journal.Record {
		return journal.Record{ID: "00000000000000a1", Kind: "server", Agent: journal.Agent{ID: "rec", Name: "Recipe", Session: conv}, Cwd: e.root, Where: "native",
			Launch: &journal.Launch{ArgsKept: true}, StartedAt: histNow, EndedAt: histNow.Add(time.Minute), Ending: "exited"}
	}
	for _, c := range []struct {
		name    string
		edit    func(r *journal.Record)
		offered bool
		why     string
		confirm bool
	}{
		{"resumable", func(*journal.Record) {}, true, "", false},
		{"hosted", func(r *journal.Record) { r.Host = journal.Host{Name: "lab-box", Hosted: true} }, false, "resume it on lab-box: `conductor host` resumes it there", false},
		{"a crew member", func(r *journal.Record) { r.Crew = &journal.Crew{RunID: "hist-00000027", Member: "lead"} }, false, "resume it as the run's member, from its run", false},
		{"a managed attempt", func(r *journal.Record) { r.Crew = &journal.Crew{RunID: "hist-00000027", Task: "build", Attempt: 1} }, false, "a managed run continues from its run page", false},
		{"a Brakeman turn", func(r *journal.Record) { r.Crew = &journal.Crew{RunID: "brakeman-00000002", Brakeman: "br-2"} }, false, "a Brakeman turn is never resumed: ask Brakeman again", false},
		{"arguments not kept", func(r *journal.Record) { r.Launch.ArgsKept = false }, false, "its arguments were too long to keep", false},
		{"a Runtime gone", func(r *journal.Record) { r.Where = "boxcar" }, false, "its Runtime boxcar is not on this server", false},
		{"an agent gone", func(r *journal.Record) { r.Agent.ID = "nope" }, false, "the session's agent is no longer in the catalog", false},
		{"a folder gone, needed", func(r *journal.Record) { r.Agent.ID, r.Cwd = "rec-cwd", filepath.Join(e.root, "gone") }, false, "the session's working directory is gone", false},
		{"a folder gone, not needed", func(r *journal.Record) { r.Cwd = filepath.Join(e.root, "gone") }, true, "", false},
		{"no turn yet", func(r *journal.Record) { r.Agent.Session = &journal.AgentSession{ID: conv.ID} }, false, "nothing to resume yet: the agent had no turn", false},
		{"no recipe", func(r *journal.Record) { r.Agent.ID = "cat" }, false, "cat has no session recipe", false},
		{"lost", func(r *journal.Record) { r.EndedAt, r.LostAt, r.Ending = time.Time{}, histNow, journal.EndingLost }, true, "the server stopped while this session ran on native", true},
	} {
		r := base()
		c.edit(&r)
		got := e.srv.resumeOffered(t.Context(), nil, r)
		if got.Offered != c.offered || got.Confirm != c.confirm || !strings.HasPrefix(got.Why, c.why) {
			t.Errorf("%s: %+v, want offered %v, %q", c.name, got, c.offered, c.why)
		}
	}
	// A running session holding the conversation: the resume route's 409.
	id := e.createSession("rec")
	e.local(id).ReportAgentSession(conv.ID, session.PolicyLatest, true)
	until(t, "the conversation held", func() bool { return e.srv.liveAgentSession(conv.ID) })
	if got := e.srv.resumeOffered(t.Context(), nil, base()); got.Offered || got.Why != "a running session holds that conversation" {
		t.Fatalf("held: %+v", got)
	}
}

// Every search route is the admin's: no token, a session's agent token
// and a host token are refused.
func TestHistorySearchRoutesAreAdminOnly(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	agent := e.agentToken(id)
	for _, p := range []string{"/api/history?q=x", "/api/history/entry?key=run:x", "/api/history/similar?key=run:x", "/api/history/command?run=hist-00000028&actor=admin&id=c1"} {
		for _, tok := range []string{"", agent, "test-host-token", "wrong"} {
			if resp, _ := e.do("GET", p, tok, nil); resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s with %q: %d", p, tok, resp.StatusCode)
			}
		}
		if resp, _ := e.do("GET", p, adminToken, nil); resp.StatusCode == http.StatusUnauthorized {
			t.Errorf("%s as the admin: %d", p, resp.StatusCode)
		}
	}
}

// A search's reply is at most 2 MiB: the rows past the bound are cut, the
// reply says so, and its cursor reads on from the last row served.
func TestTheSearchReplyIsBounded(t *testing.T) {
	if maxHistoryReply != 2<<20 {
		t.Fatalf("the bound is %d", maxHistoryReply)
	}
	e := newTestEnv(t, nil)
	ix := histSettle(t, e)
	var entries []history.Entry
	for i := range 40 {
		entries = append(entries, history.Entry{Key: fmt.Sprintf("session:00000000000000b1#e%d", i+1), Kind: "session.chat", Ref: "session:00000000000000b1",
			Anchor: "session:00000000000000b1", At: histNow.Add(time.Duration(i) * time.Second), Title: "zeppelin", Summary: strings.Repeat("s", history.MaxSummary)})
	}
	if err := ix.Replace(t.Context(), history.Source{Key: "session:00000000000000b1", Mark: "x"}, entries, nil); err != nil {
		t.Fatal(err)
	}
	old := historyReplyLimit
	historyReplyLimit = 16 << 10
	t.Cleanup(func() { historyReplyLimit = old })
	resp, err := e.client.Do(func() *http.Request {
		r, _ := http.NewRequest("GET", e.http.URL+"/api/history?q=zeppelin&limit=100", nil)
		r.Header.Set("Authorization", "Bearer "+adminToken)
		return r
	}())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	first := hsKeys(out)
	if out["truncated"] != true || len(first) == 0 || len(first) >= 40 || len(raw) > historyReplyLimit || out["total"] != 40.0 {
		t.Fatalf("a reply past its bound: %d bytes, %d entries, %v", len(raw), len(first), out["truncated"])
	}
	status, next := hsGet(t, e, "/api/history", url.Values{"q": {"zeppelin"}, "limit": {"100"}, "after": {out["next"].(string)}}, adminToken)
	if status != http.StatusOK || len(hsKeys(next)) == 0 || slices.Contains(first, hsKeys(next)[0]) {
		t.Fatalf("the next page: %d %v after %v", status, hsKeys(next), first)
	}
	// An entry's links past the bound are cut, and the reply says so.
	var links []history.Link
	for i := range history.MaxLinks {
		links = append(links, history.Link{From: "session:00000000000000b1", Rel: "cites", To: fmt.Sprintf("run:%s-%08x", strings.Repeat("r", 60), i)})
	}
	if err := ix.Replace(t.Context(), history.Source{Key: "session:00000000000000b1", Mark: "y"}, entries, links); err != nil {
		t.Fatal(err)
	}
	status, one := hsGet(t, e, "/api/history/entry", url.Values{"key": {entries[0].Key}}, adminToken)
	b, _ := json.Marshal(one)
	if got := len(one["entry"].(map[string]any)["links"].([]any)); status != http.StatusOK || one["linksCut"] != true || got == 0 || got >= history.MaxLinks || len(b) > historyReplyLimit {
		t.Fatalf("an entry past its bound: %d, %d links, %d bytes, cut %v", status, got, len(b), one["linksCut"])
	}
}

// A bad line, cursor, limit, key, scope, actor or parameter is refused
// with words.
func TestABadQueryIsRefusedWithWords(t *testing.T) {
	e := newTestEnv(t, nil)
	histSettle(t, e)
	for _, c := range []struct {
		path        string
		q           url.Values
		status      int
		code, words string
	}{
		{"/api/history", url.Values{"q": {strings.Repeat("a", history.MaxQuery+1)}}, 400, "bad_query", "more than 512"},
		{"/api/history", url.Values{"q": {"since:yesterday"}}, 400, "bad_query", "since"},
		{"/api/history", url.Values{"q": {"x"}, "limit": {"101"}}, 400, "bad_query", "limit is 1 to 100"},
		{"/api/history", url.Values{"q": {"x"}, "after": {"not a cursor"}}, 400, "bad_cursor", "cursor"},
		{"/api/history", url.Values{"q": {"x"}, "after": {strings.Repeat("a", maxHistoryAfter+1)}}, 400, "bad_cursor", "more than 2048"},
		{"/api/history", url.Values{"q": {"x", "y"}}, 400, "bad_query", "more than once"},
		{"/api/history", url.Values{"query": {"x"}}, 400, "invalid_request", "no parameter"},
		{"/api/history", url.Values{"q": {strings.Repeat("a", maxHistoryQueryString)}}, 400, "invalid_request", "query string"},
		{"/api/history/entry", url.Values{}, 400, "bad_key", "key"},
		{"/api/history/entry", url.Values{"key": {strings.Repeat("k", history.MaxRef+1)}}, 400, "bad_key", "512"},
		{"/api/history/similar", url.Values{"key": {"run:x"}, "scope": {"world"}}, 400, "bad_query", "scope"},
		{"/api/history/similar", url.Values{"key": {"run:x"}, "limit": {"51"}}, 400, "bad_query", "limit"},
		{"/api/history/command", url.Values{"run": {"hist-00000029"}, "actor": {strings.Repeat("a", maxHistoryActor+1)}, "id": {"c1"}}, 400, "invalid_request", "actor"},
		{"/api/history/command", url.Values{"run": {"hist-00000029"}, "actor": {"admin"}, "id": {"c 1"}}, 400, "invalid_request", "command id"},
		{"/api/history/command", url.Values{"run": {"nope"}, "actor": {"admin"}, "id": {"c1"}}, 400, "invalid_request", "run"},
	} {
		status, out := hsGet(t, e, c.path, c.q, adminToken)
		if code, msg := hsErr(out); status != c.status || code != c.code || !strings.Contains(msg, c.words) {
			t.Errorf("%s %v: %d %v, want %d %s %q", c.path, c.q, status, out, c.status, c.code, c.words)
		}
	}
}

// The request log's lines for a search name the route, never its query:
// no line holds the words searched, nor the key read.
func TestTheQueryIsNeverLogged(t *testing.T) {
	logs := &logBuffer{}
	e := newTestEnvLogging(t, nil, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	histSettle(t, e)
	hsGet(t, e, "/api/history", url.Values{"q": {"xylophonesecret"}}, adminToken)
	hsGet(t, e, "/api/history", url.Values{"q": {"since:xylophonesecret"}}, adminToken)
	hsGet(t, e, "/api/history/entry", url.Values{"key": {"session:xylophonekey"}}, adminToken)
	hsGet(t, e, "/api/history/similar", url.Values{"key": {"check:xylophonekey"}}, adminToken)
	hsGet(t, e, "/api/history/command", url.Values{"run": {"hist-0000002a"}, "actor": {"xylophoneactor"}, "id": {"c1"}}, adminToken)
	out := logs.String()
	if !strings.Contains(out, "path=/api/history ") {
		t.Fatalf("no request line for the search:\n%s", out)
	}
	for _, s := range []string{"xylophone", " q=", " key="} {
		if strings.Contains(out, s) {
			t.Fatalf("the log holds %q:\n%s", s, out)
		}
	}
}

// The history's search and Dispatch's read the same words in a line: for
// each of a table of lines, the tickets one finds are the tickets the
// other finds.
func TestHistoryAndDispatchReadWordsAlike(t *testing.T) {
	e := dispatchEnv(t)
	e.newDispatchProject(t, "WRD", nil)
	for _, title := range []string{"Fix the login loop", "Login page a:b", "Café menu", "OR NOT both", "Loop-de-loop", "The CND-12 report"} {
		e.newDispatchTicket(t, "WRD", title, map[string]any{"description": "about " + strings.ToLower(title)})
	}
	histSettle(t, e)
	for _, line := range []string{"login", "log", "LOGIN loop", "loop login", "OR", "NOT", "a:b", "cafe", "café", "the the", "-", "loop-de", "CND-12", "WRD", "menu caf", "zzz"} {
		status, raw, out := e.call(t, "GET", "/api/dispatch/tickets?q="+url.QueryEscape(line), nil)
		if status != http.StatusOK {
			t.Fatalf("Dispatch %q: %d %s", line, status, raw)
		}
		var dk []string
		for _, x := range listOf(t, out, "tickets") {
			dk = append(dk, "ticket:"+x["key"].(string))
		}
		hk := []string{}
		for _, k := range hsKeys(hsSearch(t, e, line)) {
			if strings.HasPrefix(k, "ticket:") {
				hk = append(hk, k)
			}
		}
		sort.Strings(dk)
		sort.Strings(hk)
		if dk == nil {
			dk = []string{}
		}
		if !slices.Equal(dk, hk) {
			t.Errorf("%q: Dispatch finds %v, the history %v", line, dk, hk)
		}
	}
}

// A key the index no longer holds answers entry_gone, never another
// entry, on the entry and the similar routes.
func TestAGoneKeyAnswersEntryGone(t *testing.T) {
	e := newTestEnv(t, nil)
	saveRecord(t, e.srv, crew.Run{ID: "hist-0000002b", Label: "here", Cwd: e.root, StartedAt: histNow})
	histSettle(t, e)
	hsEntryOf(t, e, "run:hist-0000002b")
	if err := os.Remove(filepath.Join(e.srv.cfg.DataDir, "runs", "hist-0000002b.json")); err != nil {
		t.Fatal(err)
	}
	e.srv.history.mark("run:hist-0000002b")
	histSettle(t, e)
	for _, path := range []string{"/api/history/entry", "/api/history/similar"} {
		status, out := hsGet(t, e, path, url.Values{"key": {"run:hist-0000002b"}}, adminToken)
		if code, msg := hsErr(out); status != http.StatusNotFound || code != "entry_gone" || msg != "this entry is no longer in the history" {
			t.Fatalf("%s: %d %v", path, status, out)
		}
	}
}

// A ticket the other server edits reaches no hook of this one's; one
// reconcile (Dispatch's feed) makes the edit searchable, with no restart.
func TestAnEditByTheOtherServerBecomesSearchable(t *testing.T) {
	e := newTestEnv(t, nil)
	noTick(t, e)
	e.newDispatchProject(t, "OTH", nil)
	e.newDispatchTicket(t, "OTH", "Before", nil)
	histSettle(t, e)
	other := otherServer(t, e)
	other.expect(t, "POST", "/api/dispatch/tickets/OTH-1/edit", map[string]any{"opId": opID(), "set": map[string]any{"title": "the wombat edit"}}, http.StatusOK, "")
	histSettle(t, e)
	if keys := hsKeys(hsSearch(t, e, "wombat")); len(keys) != 0 {
		t.Fatalf("found before a reconcile: %v", keys)
	}
	histReconcile(t, e)
	if keys := hsKeys(hsSearch(t, e, "wombat")); !slices.Equal(keys, []string{"ticket:OTH-1"}) {
		t.Fatalf("after a reconcile: %v", keys)
	}
}

// Two servers on one data directory: the second's searches, its index's
// connection open throughout, answer after the first's reindex with no
// restart.
func TestAReindexWithASecondServerSearching(t *testing.T) {
	e := newTestEnv(t, nil)
	e.newDispatchProject(t, "TWO", nil)
	e.newDispatchTicket(t, "TWO", "the marmalade ticket", nil)
	histSettle(t, e)
	other := otherServer(t, e)
	waitReader := func() {
		t.Helper()
		until(t, "the second server's search finds the ticket", func() bool {
			status, out := hsGet(t, other, "/api/history", url.Values{"q": {"marmalade"}}, adminToken)
			return status == http.StatusOK && slices.Equal(hsKeys(out), []string{"ticket:TWO-1"})
		})
	}
	waitReader()
	if out := hsSearch(t, other, "marmalade"); out["readOnly"] != true {
		t.Fatalf("the second server's reply: %v", out)
	}
	ix := other.srv.history.index()
	if resp, _ := e.do("POST", "/api/history/reindex", adminToken, nil); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("reindex: %d", resp.StatusCode)
	}
	histSettle(t, e)
	waitReader()
	if other.srv.history.index() != ix {
		t.Fatal("the second server opened its index again")
	}
}

// The workbench token and an identity provider's administrator each made
// a command of one id in one run: the index holds the admin's receipt,
// and /api/history/command opens it for the provider's administrator with
// its actor; the run's command route still answers each caller its own.
func TestTheCommandReaderOpensTheIndexedActorsReceipt(t *testing.T) {
	id := &tokenIdentity{known: map[string]ext.Principal{providerToken: cloudAdmin("u1")}}
	e := providerEnv(t, nil, id)
	e.gitRepo(t)
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), managedCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	runningAttempt(t, e, run.ID, "build", 1)
	base := "/api/runs/" + run.ID
	for _, c := range []struct{ tok, path string }{{providerToken, "/pause"}, {adminToken, "/unpause"}} {
		if resp, out := e.do("POST", base+c.path, c.tok, map[string]any{"commandId": "c1"}); resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %v", c.path, resp.StatusCode, out)
		}
	}
	ix := histSettle(t, e)
	h := histEntry(t, ix, "run:"+run.ID+"#command/admin/c1")
	histGone(t, ix, "run:"+run.ID+"#command/user:u1/c1")
	served := e.srv.serveEntry(e.srv.newAvailability(t.Context(), ix), h.Entry, "", h.Repeats, h.Links, false)
	if served.URL != "/runs/"+run.ID+"?command=c1&actor=admin" {
		t.Fatalf("the command's URL: %s", served.URL)
	}
	resp, out := e.do("GET", "/api/history/command?run="+run.ID+"&actor=admin&id=c1", providerToken, nil)
	if c, _ := out["command"].(map[string]any); resp.StatusCode != http.StatusOK || c["actor"] != adminActor || c["intent"] != "unpause" {
		t.Fatalf("the indexed receipt for the provider's administrator: %d %v", resp.StatusCode, out)
	}
	for tok, intent := range map[string]string{providerToken: "pause", adminToken: "unpause"} {
		resp, out := e.do("GET", base+"/commands/c1", tok, nil)
		if c, _ := out["command"].(map[string]any); resp.StatusCode != http.StatusOK || c["intent"] != intent {
			t.Fatalf("the run's command route for %s: %d %v", tok, resp.StatusCode, out)
		}
	}
	if resp, _ := e.do("GET", "/api/history/command?run="+run.ID+"&actor=admin&id=c9", adminToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("no such receipt: %d", resp.StatusCode)
	}
}

// One client holds at most two of the history's reads at once, and the
// server four: past them a read is refused, never queued.
func TestHistoryReadsAreBoundedPerClient(t *testing.T) {
	e := newTestEnv(t, nil)
	histSettle(t, e)
	held, release := make(chan struct{}, 8), make(chan struct{})
	var once sync.Once
	let := func() { once.Do(func() { close(release) }) }
	t.Cleanup(let) // before the server's own end, which waits for its handlers
	e.srv.historyHold = func(*http.Request) {
		held <- struct{}{}
		<-release
	}
	var wg sync.WaitGroup
	for range maxHistoryReadsPerClient {
		wg.Go(func() { hsGet(t, e, "/api/history", url.Values{"q": {"x"}}, adminToken) })
	}
	for range maxHistoryReadsPerClient {
		<-held
	}
	if status, out := hsGet(t, e, "/api/history", url.Values{"q": {"x"}}, adminToken); status != http.StatusTooManyRequests || func() bool { c, _ := hsErr(out); return c != "history_busy" }() {
		t.Fatalf("a third read from the client: %d %v", status, out)
	}
	let()
	wg.Wait()
	if !e.srv.historyReads.enter("a") || !e.srv.historyReads.enter("b") || !e.srv.historyReads.enter("c") || !e.srv.historyReads.enter("d") || e.srv.historyReads.enter("e") {
		t.Fatal("the server's bound of four")
	}
}

// A typed answer to a question is never returned by a search: the words
// are found in no entry and in no reply.
func TestASearchNeverReturnsATypedAnswer(t *testing.T) {
	const secret = "typedanswersecret7"
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	l := e.local(id)
	h := testHost(e.srv, t.Context(), "switchman")
	l.SetAttentionFull(session.AttentionNeedsInput, "What is the password?", session.SourceAPI, session.KindPrompt, nil)
	q := l.Info().Attention.ID
	if res, err := h.Submit(t.Context(), ext.SubmitRequest{Session: id, Question: q, Text: secret, By: ada}); err != nil || res.Status != "entered" {
		t.Fatalf("the routed answer: %+v %v", res, err)
	}
	until(t, "the answer in the record", func() bool {
		r, _, err := e.srv.sessionRecords.Get(id)
		return err == nil && slices.ContainsFunc(r.Entries, func(e journal.Entry) bool { return e.Kind == journal.KindAnswer && e.Ref == q })
	})
	e.srv.sessionRecords.Flush()
	histSettle(t, e)
	for _, line := range []string{secret, "password"} {
		status, raw, _ := e.call(t, "GET", "/api/history?q="+url.QueryEscape(line), nil)
		if status != http.StatusOK || bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("%q: %d %s", line, status, raw)
		}
	}
}

// seedTicket makes a project and its first ticket in the Dispatch store
// of the data directory dir, before any server opens it, and answers the
// ticket's key.
func seedTicket(t *testing.T, dir, project, title string) string {
	t.Helper()
	ds, err := dispatch.Open(filepath.Join(dir, "dispatch"))
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()
	ctx := context.Background()
	if _, err := ds.CreateProject(ctx, adminOp(""), dispatch.NewProject{Key: project, Name: project}); err != nil {
		t.Fatal(err)
	}
	if _, err := ds.CreateTicket(ctx, adminOp(""), dispatch.NewTicket{Project: project, Title: title}); err != nil {
		t.Fatal(err)
	}
	return project + "-1"
}

// histTicketAtStart fails t unless the start's catch-up indexed the
// ticket and kept Dispatch's feed position.
func histTicketAtStart(t *testing.T, ix *history.Index, key, title string) {
	t.Helper()
	if h := histEntry(t, ix, hTicketRef(key)); h.Title != key+" "+title {
		t.Fatalf("the ticket: %+v", h.Entry)
	}
	if m := histMark(t, ix, feedSource); m == "" || m == "0" {
		t.Fatalf("the feed's position: %q", m)
	}
}
