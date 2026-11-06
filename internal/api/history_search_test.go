package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
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
	// A plan revision's entry is keyed by Brakeman's grammar: its ref
	// parses, opens the page Brakeman's opens, and a brief citing it is
	// joined to it.
	plan1 := hsEntryOf(t, p.testEnv, "plan:CND-1/1")
	if pr, err := brakeman.ParseRef(plan1["key"].(string)); err != nil || plan1["url"] == "" || plan1["url"] != refURL(pr) {
		t.Fatalf("the plan revision's entry: %v (%v)", plan1, err)
	}
	brief := keepBrief(t, p.testEnv, brakeman.DefaultConfig(p.root), nil, fact("r1 was the plan", ref(t, "plan:CND-1/1")))
	histSettle(t, p.testEnv)
	if links := fmt.Sprint(hsEntryOf(t, p.testEnv, "plan:CND-1/1")["links"]); !strings.Contains(links, hBriefItemRef(brief, 0)) {
		t.Fatalf("the brief's citation is not joined to the revision: %s", links)
	}
	// A ticket's earlier field values and a plan revision's body are never
	// read: an old title and words only in a revision's note and
	// constraints find nothing; the title as it is finds the ticket.
	for _, title := range []string{"albatross", "pelican"} {
		p.expect(t, "POST", "/api/dispatch/tickets/CND-1/edit", map[string]any{"opId": opID(), "set": map[string]any{"title": title}}, http.StatusOK, "")
	}
	p.proposed(t, saved.ID, map[string]any{"note": "the quokka note", "constraints": "no quokka harmed"})
	histSettle(t, p.testEnv)
	for line, want := range map[string][]string{"albatross": {}, "quokka": {}, "pelican": {"ticket:CND-1"}} {
		if keys := hsKeys(hsSearch(t, p.testEnv, line)); !slices.Equal(keys, want) {
			t.Fatalf("%q finds %v, want %v", line, keys, want)
		}
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
// one's is looked at where its run puts it, through the run's folder.
func TestAWhereOtherThanNativeIsNotLookedAt(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	const run = "hist-00000026"
	histCreate(t, ld, run, "", histCrew(e.root))
	tree := func(n int) string {
		return filepath.Join(e.root, ".conductor", "worktrees", run, fmt.Sprintf("build.%d", n))
	}
	if err := os.MkdirAll(tree(2), 0o700); err != nil {
		t.Fatal(err)
	}
	histTx(t, ld, run, func(tx *ledger.Tx) error {
		for i, w := range []string{"boxcar", ledger.WhereNative, ledger.WhereNative} {
			a := histAttempt("build", i+1, ledger.AttemptFailed, "", commitID(0), "")
			a.Where, a.Worktree = w, tree(i+1)
			if err := tx.PutAttempt(a); err != nil {
				return err
			}
		}
		return nil
	})
	histSettle(t, e)
	for n, want := range map[int]string{1: availElsewhere, 2: availWorktree, 3: availWorktreeGone} {
		a := availOf(hsEntryOf(t, e, attemptKey(run, "build", n)))
		if a["state"] != want {
			t.Fatalf("attempt %d: %v, want %s", n, a, want)
		}
		if n == 1 && a["words"] != "on boxcar: not looked at from here" {
			t.Fatalf("the other Runtime's words: %v", a)
		}
		if n == 2 && a["branch"] != "conductor/build" {
			t.Fatalf("the kept worktree's branch, its row's: %v", a)
		}
	}
}

// An attempt's worktree is looked at only where its run puts it, through
// the run's folder's root: a row naming a folder outside it (an absolute
// path, one through "..") or a link put in its place out of the folder is
// answered as a worktree not kept, in the very words of one removed, with
// no branch: nothing of the outside path (whether it is there, what it
// holds) is learned from the answer.
func TestAWorktreeOutsideItsRunIsNeverLookedAt(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	const run = "hist-0000002c"
	histCreate(t, ld, run, "", histCrew(e.root))
	outside := t.TempDir() // there, a directory, beside no allowed root
	tree := func(n int) string {
		return filepath.Join(e.root, ".conductor", "worktrees", run, fmt.Sprintf("build.%d", n))
	}
	if err := os.MkdirAll(filepath.Dir(tree(1)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, tree(4)); err != nil {
		t.Fatal(err)
	}
	worktrees := map[int]string{
		2: outside,                                                                 // an absolute path outside the run's folder
		3: tree(3) + strings.Repeat(string(filepath.Separator)+"..", 64) + outside, // up out of it, to the same folder
		4: tree(4),                                                                 // its own place, a link out of the folder
		5: tree(5),                                                                 // its own place, not there
	}
	histTx(t, ld, run, func(tx *ledger.Tx) error {
		for n := 2; n <= 5; n++ {
			a := histAttempt("build", n, ledger.AttemptFailed, "", commitID(0), "")
			a.Worktree = worktrees[n]
			if err := tx.PutAttempt(a); err != nil {
				return err
			}
		}
		return nil
	})
	histSettle(t, e)
	removed := availOf(hsEntryOf(t, e, attemptKey(run, "build", 5)))
	if removed["state"] != availWorktreeGone || removed["branch"] != nil {
		t.Fatalf("a worktree not there: %v", removed)
	}
	for n := 2; n <= 4; n++ {
		if a := availOf(hsEntryOf(t, e, attemptKey(run, "build", n))); !reflect.DeepEqual(a, removed) {
			t.Errorf("attempt %d (%s): %v, a removed one's %v", n, worktrees[n], a, removed)
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
		offered answer
		why     string
		confirm bool
	}{
		{"resumable", func(*journal.Record) {}, answerYes, "", false},
		{"hosted", func(r *journal.Record) { r.Host = journal.Host{Name: "lab-box", Hosted: true} }, answerNo, "resume it on lab-box: `conductor host` resumes it there", false},
		{"a crew member", func(r *journal.Record) { r.Crew = &journal.Crew{RunID: "hist-00000027", Member: "lead"} }, answerNo, "resume it as the run's member, from its run", false},
		{"a managed attempt", func(r *journal.Record) { r.Crew = &journal.Crew{RunID: "hist-00000027", Task: "build", Attempt: 1} }, answerNo, "a managed run continues from its run page", false},
		{"a Brakeman turn", func(r *journal.Record) { r.Crew = &journal.Crew{RunID: "brakeman-00000002", Brakeman: "br-2"} }, answerNo, "a Brakeman turn is never resumed: ask Brakeman again", false},
		{"arguments not kept", func(r *journal.Record) { r.Launch.ArgsKept = false }, answerNo, "its arguments were too long to keep", false},
		{"a Runtime gone", func(r *journal.Record) { r.Where = "boxcar" }, answerNo, "its Runtime boxcar is not on this server", false},
		{"an agent gone", func(r *journal.Record) { r.Agent.ID = "nope" }, answerNo, "the session's agent is no longer in the catalog", false},
		{"a folder gone, needed", func(r *journal.Record) { r.Agent.ID, r.Cwd = "rec-cwd", filepath.Join(e.root, "gone") }, answerNo, "the session's working directory is gone", false},
		{"a folder gone, not needed", func(r *journal.Record) { r.Cwd = filepath.Join(e.root, "gone") }, answerYes, "", false},
		{"no turn yet", func(r *journal.Record) { r.Agent.Session = &journal.AgentSession{ID: conv.ID} }, answerNo, "nothing to resume yet: the agent had no turn", false},
		{"no recipe", func(r *journal.Record) { r.Agent.ID = "cat" }, answerNo, "cat has no session recipe", false},
		{"lost", func(r *journal.Record) { r.EndedAt, r.LostAt, r.Ending = time.Time{}, histNow, journal.EndingLost }, answerYes, "the server stopped while this session ran on native", true},
	} {
		r := base()
		c.edit(&r)
		got := e.srv.resumeOffered(r)
		if got.Offered != c.offered || got.Confirm != c.confirm || !strings.HasPrefix(got.Why, c.why) {
			t.Errorf("%s: %+v, want offered %s, %q", c.name, got, c.offered, c.why)
		}
	}
	// A member whose adoption the journal had not written when the server
	// stopped: its run's record names it, read through the run records.
	saveRecord(t, e.srv, crew.Run{ID: "hist-0000002d", Cwd: e.root, StartedAt: histNow,
		Members: []crew.MemberState{{Name: "lead", AgentID: "rec", SessionID: base().ID, Status: crew.MemberEnded}}})
	if got := e.srv.resumeOffered(base()); got.Offered != answerNo || got.URL != "/runs/hist-0000002d" || got.Why != "resume it as the run's member, from its run" {
		t.Fatalf("a member by its run's record: %+v", got)
	}
	if err := e.srv.records.Delete("hist-0000002d"); err != nil {
		t.Fatal(err)
	}
	// A run record that cannot be read could name it: whether it is a
	// member is unknown, and unknown is not no.
	unread := filepath.Join(e.srv.cfg.DataDir, "runs", "hist-0000002e.json")
	if err := os.WriteFile(unread, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := e.srv.resumeOffered(base()); got.Offered != answerUnknown || got.Why != "whether it was a crew member's session could not be read; try again" {
		t.Fatalf("beside a run record that cannot be read: %+v", got)
	}
	if err := os.Remove(unread); err != nil {
		t.Fatal(err)
	}
	if got := e.srv.resumeOffered(base()); got.Offered != answerYes {
		t.Fatalf("once it is gone: %+v", got)
	}
	// A running session holding the conversation: the resume route's 409.
	id := e.createSession("rec")
	e.local(id).ReportAgentSession(conv.ID, session.PolicyLatest, true)
	until(t, "the conversation held", func() bool { return e.srv.liveAgentSession(conv.ID) })
	if got := e.srv.resumeOffered(base()); got.Offered != answerNo || got.Why != "a running session holds that conversation" {
		t.Fatalf("held: %+v", got)
	}
}

// Every search route is the admin's: no token, a session's agent token, a
// share link's token (view or control), a host token and a Brakeman
// turn's agent token are each refused.
func TestHistorySearchRoutesAreAdminOnly(t *testing.T) {
	b := brakemanEnv(t, nil)
	e := b.testEnv
	id := e.createSession("cat")
	agent := e.agentToken(id)
	links := map[string]string{}
	for _, role := range []string{"view", "control"} {
		resp, lo := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": role})
		tok, _ := lo["token"].(string)
		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK || tok == "" {
			t.Fatalf("a %s link: %d %v", role, resp.StatusCode, lo)
		}
		links[role] = tok
	}
	b.enable(t, "bk", nil)
	_, turn, _ := b.turnSession(t, b.briefing(t))
	for _, p := range []string{"/api/history?q=x", "/api/history/entry?key=run:x", "/api/history/similar?key=run:x", "/api/history/command?run=hist-00000028&actor=admin&id=c1"} {
		for name, tok := range map[string]string{"no token": "", "an agent token": agent, "a view link": links["view"], "a control link": links["control"],
			"a host token": "test-host-token", "a Brakeman turn's token": turn, "a wrong token": "wrong"} {
			if resp, _ := e.do("GET", p, tok, nil); resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s with %s: %d", p, name, resp.StatusCode)
			}
		}
		if resp, _ := e.do("GET", p, adminToken, nil); resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
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
		entries = append(entries, history.Entry{Key: fmt.Sprintf("run:hist-00000034#message/%d", i+1), Kind: "message", Ref: "run:hist-00000034",
			Anchor: "run:hist-00000034", At: histNow.Add(time.Duration(i) * time.Second), Title: "zeppelin", Summary: strings.Repeat("s", history.MaxSummary)})
	}
	if err := ix.Replace(t.Context(), history.Source{Key: "run:hist-00000034", Mark: "x"}, entries, nil); err != nil {
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
	// Page after page, each reading on from the last row served: every
	// row once, in the search's order.
	all := slices.Clone(first)
	for next, _ := out["next"].(string); next != ""; {
		status, page := hsGet(t, e, "/api/history", url.Values{"q": {"zeppelin"}, "limit": {"100"}, "after": {next}}, adminToken)
		if status != http.StatusOK || len(hsKeys(page)) == 0 {
			t.Fatalf("the next page: %d %v after %v", status, page, all)
		}
		all = append(all, hsKeys(page)...)
		next, _ = page["next"].(string)
	}
	historyReplyLimit = old
	whole := hsKeys(hsSearch(t, e, "zeppelin"))
	historyReplyLimit = 16 << 10
	if len(whole) != 40 || !slices.Equal(all, whole) {
		t.Fatalf("the pages served %d rows %v, the search's order %v", len(all), all, whole)
	}
	// An entry's links past the bound are cut, and the reply says so.
	var links []history.Link
	for i := range history.MaxLinks {
		links = append(links, history.Link{From: "run:hist-00000034", Rel: "cites", To: fmt.Sprintf("run:%s-%08x", strings.Repeat("r", 60), i)})
	}
	if err := ix.Replace(t.Context(), history.Source{Key: "run:hist-00000034", Mark: "y"}, entries, links); err != nil {
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
	e.srv.historyHold = func(context.Context, *http.Request) {
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

// A record forgotten is never served once Forget has answered, whatever
// the index still holds: with the indexer held at a gate (its pass that
// removes the record not yet run), a search answers none of the record's
// rows, counts none in its total and names none in a cursor; an entry or
// similar read of it answers entry_gone; a link to it is left out. Once
// the indexer's pass has run, the index holds none of it either.
func TestAForgottenRecordIsNeverServed(t *testing.T) {
	e := newTestEnv(t, nil)
	gone, kept := e.createSession("cat"), e.createSession("cat")
	e.stopAndWait(gone)
	e.stopAndWait(kept)
	const gate = "hist-0000002f"
	saveRecord(t, e.srv, crew.Run{ID: gate, Label: "the gate", Cwd: e.root, StartedAt: histNow})
	histSettle(t, e)
	both := "session:" + gone + "," + kept
	if keys := hsKeys(hsSearch(t, e, both)); !slices.ContainsFunc(keys, func(k string) bool { return strings.HasPrefix(k, hSessionRef(gone)) }) {
		t.Fatalf("the record is not indexed before: %v", keys)
	}
	keptTotal := hsSearch(t, e, "session:"+kept)["total"]

	// The indexer held at a pass of another source, the record forgotten.
	held, release := make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	let := func() { once.Do(func() { close(release) }) }
	t.Cleanup(let)
	freshIndexer(t, e, func(h *historyIndexer) {
		h.afterMark = func(key string) {
			if key == hRunRef(gate) {
				select {
				case held <- struct{}{}:
				default:
				}
				<-release
			}
		}
	})
	saveRecord(t, e.srv, crew.Run{ID: gate, Label: "the gate, again", Cwd: e.root, StartedAt: histNow})
	<-held
	if resp, out := e.do("DELETE", "/api/history/sessions/"+gone, adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("forget: %d %v", resp.StatusCode, out)
	}
	ix := e.srv.history.index()
	histEntry(t, ix, hSessionRef(gone)) // the index still holds it: the gate held its removal

	out := hsSearch(t, e, both)
	for _, k := range hsKeys(out) {
		if strings.HasPrefix(k, hSessionRef(gone)) {
			t.Fatalf("a forgotten record's row is served: %v", hsKeys(out))
		}
	}
	if out["total"] != keptTotal || out["stale"] != true {
		t.Fatalf("the total: %v, the kept record's alone %v; stale %v", out["total"], keptTotal, out["stale"])
	}
	// Page by page, one row at a time: no row of it, no cursor naming it.
	seen := 0
	for after := ""; ; {
		q := url.Values{"q": {both}, "limit": {"1"}}
		if after != "" {
			q.Set("after", after)
		}
		status, page := hsGet(t, e, "/api/history", q, adminToken)
		if status != http.StatusOK {
			t.Fatalf("a page: %d %v", status, page)
		}
		for _, k := range hsKeys(page) {
			if strings.HasPrefix(k, hSessionRef(gone)) {
				t.Fatalf("a page serves %s", k)
			}
			seen++
		}
		next, _ := page["next"].(string)
		if next == "" {
			break
		}
		if raw, err := base64.RawURLEncoding.DecodeString(next); err != nil || bytes.Contains(raw, []byte(gone)) {
			t.Fatalf("a cursor names the forgotten record: %s (%v)", raw, err)
		}
		after = next
	}
	if float64(seen) != keptTotal {
		t.Fatalf("the pages served %d rows, the kept record holds %v", seen, keptTotal)
	}
	for _, path := range []string{"/api/history/entry", "/api/history/similar"} {
		status, out := hsGet(t, e, path, url.Values{"key": {hSessionRef(gone)}}, adminToken)
		if code, _ := hsErr(out); status != http.StatusNotFound || code != "entry_gone" {
			t.Fatalf("%s of the forgotten record: %d %v", path, status, out)
		}
	}
	av := e.srv.newAvailability(t.Context(), ix)
	served := e.srv.serveEntry(av, history.Entry{Key: "run:" + gate, Kind: "run", Ref: "run:" + gate, Anchor: "run:" + gate, At: histNow}, "", history.Repeats{},
		history.Links{Out: []history.LinkEnd{{Rel: relSessionOf, Ref: hSessionRef(gone)}, {Rel: relSessionOf, Ref: hSessionRef(kept)}},
			In: []history.LinkEnd{{Rel: relResumedFrom, Ref: "session:" + kept, Entry: &history.Linked{Key: hSessionRef(gone), Kind: "session", Title: "the forgotten one"}}}}, false)
	if len(served.Links) != 1 || served.Links[0].Ref != hSessionRef(kept) || served.Links[0].Dir != "out" {
		t.Fatalf("the links served: %+v", served.Links)
	}

	let()
	histSettle(t, e)
	histGone(t, e.srv.history.index(), hSessionRef(gone))
	if out := hsSearch(t, e, both); out["stale"] != nil || out["total"] != keptTotal {
		t.Fatalf("after the indexer's pass: %v", out)
	}
}

// stalledWriter is a client that never reads: a write blocks until the
// test lets it go, and the write deadline set on it is kept.
type stalledWriter struct {
	header   http.Header
	status   atomic.Int64
	deadline atomic.Pointer[time.Time]
	writing  chan struct{}
	release  chan struct{}
	once     sync.Once
}

func newStalledWriter() *stalledWriter {
	return &stalledWriter{header: http.Header{}, writing: make(chan struct{}), release: make(chan struct{})}
}

func (w *stalledWriter) Header() http.Header  { return w.header }
func (w *stalledWriter) WriteHeader(code int) { w.status.Store(int64(code)) }
func (w *stalledWriter) Write([]byte) (int, error) {
	w.once.Do(func() { close(w.writing) })
	<-w.release
	return 0, os.ErrDeadlineExceeded
}
func (w *stalledWriter) SetWriteDeadline(at time.Time) error {
	if !at.IsZero() {
		w.deadline.Store(&at)
	}
	return nil
}

// A read's slot is let go on every way out of its handler: before its
// reply is written, so clients that never read hold none (and the write
// has its own deadline); when the client leaves, which is no failure of
// the server's (nothing logged, no 500); and at the read's deadline,
// answered in its own words.
func TestAReadsSlotIsLetGoOnEveryWayOut(t *testing.T) {
	logs := &logBuffer{}
	e := newTestEnvLogging(t, nil, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	histSettle(t, e)

	// Clients that never read, more of them from one address than its
	// slots: each is served, none holds a slot.
	var stalled []*stalledWriter
	var wg sync.WaitGroup
	for range maxHistoryReadsPerClient + 1 {
		sw := newStalledWriter()
		stalled = append(stalled, sw)
		r := httptest.NewRequest("GET", "/api/history?q=x", nil)
		r.Header.Set("Authorization", "Bearer "+adminToken)
		r.RemoteAddr = "192.0.2.7:4000"
		before := time.Now()
		wg.Go(func() { e.srv.Handler().ServeHTTP(sw, r) })
		<-sw.writing
		if status := sw.status.Load(); status != http.StatusOK {
			t.Fatalf("a read beside stalled ones: %d", status)
		}
		if d := sw.deadline.Load(); d == nil || d.Before(before) || d.After(time.Now().Add(historyWriteTimeout)) {
			t.Fatalf("the write's deadline: %v", d)
		}
		if n := e.srv.historyReads.inFlight(); n != 0 {
			t.Fatalf("%d slots held by replies being written", n)
		}
	}
	for _, sw := range stalled {
		close(sw.release)
	}
	wg.Wait()

	// A client that leaves while its read is held.
	held := make(chan struct{}, 1)
	e.srv.historyHold = func(ctx context.Context, _ *http.Request) {
		held <- struct{}{}
		<-ctx.Done()
	}
	ctx, cancel := context.WithCancel(t.Context())
	r, _ := http.NewRequestWithContext(ctx, "GET", e.http.URL+"/api/history?q=x", nil)
	r.Header.Set("Authorization", "Bearer "+adminToken)
	left := make(chan struct{})
	go func() {
		defer close(left)
		if resp, err := e.client.Do(r); err == nil {
			resp.Body.Close()
		}
	}()
	<-held
	cancel()
	<-left
	until(t, "the slot of a read whose client left let go", func() bool { return e.srv.historyReads.inFlight() == 0 })
	if out := logs.String(); strings.Contains(out, "a history read failed") || strings.Contains(out, "status=500") {
		t.Fatalf("a client that left is logged as a failure:\n%s", out)
	}

	// The read's deadline: answered as one, the slot let go.
	e.srv.historyDeadline = 50 * time.Millisecond
	status, out := hsGet(t, e, "/api/history", url.Values{"q": {"x"}}, adminToken)
	if code, msg := hsErr(out); status != http.StatusServiceUnavailable || code != "history_timeout" || !strings.Contains(msg, "took longer than 50ms") {
		t.Fatalf("a read past its deadline: %d %v", status, out)
	}
	if n := e.srv.historyReads.inFlight(); n != 0 {
		t.Fatalf("%d slots held after the deadline", n)
	}
}

// An act made again keeps the note in effect at it: Dispatch writes a
// note only when it changes, so an approval or an acceptance made again
// with the same note writes no note row, and a note cleared is a row of
// its own.
func TestARepeatedActKeepsItsNote(t *testing.T) {
	plan := func(seq int64, field, value, op string) dispatch.ChangeRow {
		return dispatch.ChangeRow{Seq: seq, Entity: dispatch.EntityPlan, EntityID: "0000000000000001", Field: field, Value: value, OpID: op, Actor: adminActor}
	}
	tk := func(seq int64, field, value, op string) dispatch.ChangeRow {
		return dispatch.ChangeRow{Seq: seq, Entity: dispatch.EntityTicket, EntityID: "0000000000000009", Field: field, Value: value, OpID: op, Actor: adminActor}
	}
	at := histNow.Format(time.RFC3339Nano)
	rows := []dispatch.ChangeRow{
		plan(2, "approved_note", "Ship it.", "a1"), plan(1, "approved_at", at, "a1"), // a note row before or after its act
		plan(3, "approved_at", at, "a2"),                                     // again, the same note: no note row
		plan(4, "approved_at", at, "a3"), plan(5, "approved_note", "", "a3"), // cleared
		plan(6, "approved_at", at, "a4"),
		tk(7, "accepted_at", at, "c1"), tk(8, "accepted_note", "Good.", "c1"),
		tk(9, "accepted_at", "", "c2"), tk(10, "accepted_at", at, "c3"),
	}
	entries, _ := ticketEntries(ticketRead{v: dispatch.TicketView{Ticket: dispatch.Ticket{Key: "CND-9", Title: "Notes", CreatedAt: histNow}},
		plans: []dispatch.PlanSummary{{ID: "0000000000000001", N: 1, CreatedAt: histNow}}, changes: rows})
	got := map[string]string{}
	for _, e := range entries {
		got[e.Key] = e.Body
	}
	for key, want := range map[string]string{
		"ticket:CND-9#plan.approved/r1/a1": "Ship it.", "ticket:CND-9#plan.approved/r1/a2": "Ship it.",
		"ticket:CND-9#plan.approved/r1/a3": "", "ticket:CND-9#plan.approved/r1/a4": "",
		"ticket:CND-9#ticket.accepted/c1": "Good.", "ticket:CND-9#ticket.accepted/c3": "Good.",
	} {
		if body, ok := got[key]; !ok || body != want {
			t.Errorf("%s: %q (%v), want %q", key, body, ok, want)
		}
	}
}

// A launch linked from its run or to its plan revision opens its run's
// page, its ticket's before it has a run: never no page at all.
func TestALinkedLaunchOpensItsPage(t *testing.T) {
	e := newTestEnv(t, nil)
	ix := histSettle(t, e)
	const ran, waiting = "0000000000000a01", "0000000000000a02"
	entries, links := ticketEntries(ticketRead{v: dispatch.TicketView{Ticket: dispatch.Ticket{Key: "CND-8", Title: "Launch", CreatedAt: histNow}},
		plans:    []dispatch.PlanSummary{{ID: "0000000000000001", N: 1, CreatedAt: histNow}},
		launches: []dispatch.LaunchSummary{{ID: ran, PlanN: 1, RunID: "hist-00000031", CreatedAt: histNow}, {ID: waiting, PlanN: 1, CreatedAt: histNow}}})
	if err := ix.Replace(t.Context(), history.Source{Key: "ticket:CND-8", Mark: "Q1"}, entries, links); err != nil {
		t.Fatal(err)
	}
	run := []history.Entry{{Key: "run:hist-00000031", Kind: "run", Ref: "run:hist-00000031", Anchor: "run:hist-00000031", At: histNow, Title: "the launched run"}}
	if err := ix.Replace(t.Context(), history.Source{Key: "run:hist-00000031", Mark: "x"}, run,
		[]history.Link{{From: "run:hist-00000031", Rel: relLaunched, To: hLaunchRef(ran)}}); err != nil {
		t.Fatal(err)
	}
	urls := func(key string) map[string]string {
		out := map[string]string{}
		for _, l := range hsEntryOf(t, e, key)["links"].([]any) {
			m := l.(map[string]any)
			out[m["ref"].(string)], _ = m["url"].(string)
		}
		return out
	}
	if u := urls("run:hist-00000031")[hLaunchRef(ran)]; u != "/runs/hist-00000031" {
		t.Fatalf("the run's launch: %q", u)
	}
	plan := urls("plan:CND-8/1")
	if plan[hLaunchRef(ran)] != "/runs/hist-00000031" || plan[hLaunchRef(waiting)] != "/dispatch/CND/8" {
		t.Fatalf("the revision's launches: %v", plan)
	}
}

// A read of a store that failed is said unknown, never as evidence gone:
// a receipt's and an attempt's rows not read (the read's context done),
// the ledger not open.
func TestAFailedAvailabilityReadIsUnknown(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	av := e.srv.newAvailability(ctx, nil)
	defer av.close()
	if a := av.check(ref(t, hCheckRef("hist-00000032", "build", 1, "unit", 1)), false); a.State != availUnknown {
		t.Fatalf("a receipt whose rows were not read: %v", a)
	}
	if a := av.attempt(ref(t, attemptKey("hist-00000032", "build", 1)), history.Entry{Where: ledger.WhereNative}, false); a.State != availUnknown {
		t.Fatalf("an attempt whose rows were not read: %v", a)
	}
	ld := e.srv.ledger
	e.srv.ledger = nil
	t.Cleanup(func() { e.srv.ledger = ld })
	av = e.srv.newAvailability(t.Context(), nil)
	if a := av.check(ref(t, hCheckRef("hist-00000032", "build", 1, "unit", 1)), false); a.State != availUnknown {
		t.Fatalf("a receipt with no ledger open: %v", a)
	}
	e.srv.ledger = ld

	// The run's folder: a worktree's place that cannot be looked at (a
	// step of it unreadable) is unknown, one removed is gone; a run whose
	// folder is not known (its record unreadable, no row of it) unknown.
	const run = "hist-00000035"
	proj := filepath.Join(e.root, "unreadable")
	histCreate(t, ld, run, "", histCrew(proj))
	tree := func(n int) string {
		return filepath.Join(proj, ".conductor", "worktrees", run, fmt.Sprintf("build.%d", n))
	}
	if err := os.MkdirAll(tree(1), 0o700); err != nil {
		t.Fatal(err)
	}
	histTx(t, ld, run, func(tx *ledger.Tx) error {
		for n := 1; n <= 2; n++ {
			a := histAttempt("build", n, ledger.AttemptFailed, "", commitID(0), "")
			a.Worktree = tree(n)
			if err := tx.PutAttempt(a); err != nil {
				return err
			}
		}
		return nil
	})
	at := func(n int) availabilityOf {
		av := e.srv.newAvailability(t.Context(), nil)
		defer av.close()
		return av.attempt(ref(t, attemptKey(run, "build", n)), history.Entry{Where: ledger.WhereNative}, false)
	}
	if a := at(2); a.State != availWorktreeGone {
		t.Fatalf("a worktree removed: %v", a)
	}
	if os.Geteuid() != 0 {
		if err := os.Chmod(filepath.Join(proj, ".conductor"), 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(filepath.Join(proj, ".conductor"), 0o700) })
		for n := 1; n <= 2; n++ {
			if a := at(n); a.State != availUnknown {
				t.Fatalf("attempt %d, its place unreadable: %v", n, a)
			}
		}
		if err := os.Chmod(filepath.Join(proj, ".conductor"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	const lost = "hist-00000036"
	if err := os.WriteFile(filepath.Join(e.srv.cfg.DataDir, "runs", lost+".json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	av = e.srv.newAvailability(t.Context(), nil)
	defer av.close()
	for _, r := range []string{lost, "hist-00000037"} {
		row := histAttempt("build", 1, ledger.AttemptFailed, "", commitID(0), "")
		row.RunID, row.Worktree = r, filepath.Join(e.root, ".conductor", "worktrees", r, "build.1")
		if got := av.worktreeKept(row); got != answerUnknown {
			t.Fatalf("a run whose folder is not known (%s): %s", r, got)
		}
	}
}

// A session record the journal holds but cannot decode is served (no
// Forget removed it), and whether it can be resumed is unknown, never no;
// a record the journal does not find is no.
func TestAnUnreadableRecordsResumeIsUnknown(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	e.stopAndWait(id)
	histSettle(t, e)
	e2 := restartServer(t, e)
	histSettle(t, e2)
	file := filepath.Join(e2.srv.cfg.DataDir, journal.Dir, journal.SessionsDir, id+".json")
	if err := os.WriteFile(file, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, _ := availOf(hsEntryOf(t, e2, hSessionRef(id)))["resume"].(map[string]any)
	if r["offered"] != string(answerUnknown) || r["why"] != "its record could not be read now" {
		t.Fatalf("an unreadable record's resume: %v", r)
	}
	if got := e2.srv.resumeOfRecord("00000000000000ff"); got.Offered != answerNo || got.Why != "its record is no longer kept" {
		t.Fatalf("a record not found: %+v", got)
	}
}

// keepBriefSplit keeps a brief on e's Brakeman store as another server
// would, its two writes apart: the brief's file first, then, at commit,
// the request that says it keeps one. Between them it calls between,
// with the request's file as it was before, and answers the request's id.
func keepBriefSplit(t *testing.T, e *testEnv, between func(id string)) string {
	t.Helper()
	st := e.srv.brakeman.st
	r, _, err := st.Create(brakeman.Request{OpID: fmt.Sprintf("split-%d", time.Now().UnixNano()), Kind: brakeman.KindBriefing})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Start(r.ID, brakeman.DefaultConfig(e.root), brakeman.Stamp{AgentID: "cat"}); err != nil {
		t.Fatal(err)
	}
	reqFile := filepath.Join(e.srv.cfg.DataDir, brakeman.Dir, brakeman.RequestsDir, r.ID+".json")
	before, err := os.ReadFile(reqFile)
	if err != nil {
		t.Fatal(err)
	}
	b := brakeman.Brief{Schema: brakeman.BriefSchema, Title: "Split", Summary: "Kept in two writes.", Items: []brakeman.Item{fact("the gannet dived", ref(t, "run:hist-00000033"))}}
	if _, err := st.KeepBrief(r.ID, brakeman.Kept{OpID: "brief-1", Brief: b}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(reqFile)
	if err != nil {
		t.Fatal(err)
	}
	replace := func(body []byte) {
		t.Helper()
		tmp := reqFile + ".swap"
		if err := os.WriteFile(tmp, body, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, reqFile); err != nil {
			t.Fatal(err)
		}
	}
	replace(before) // the brief's file written, its request's commit not yet
	between(r.ID)
	replace(after) // the commit
	return r.ID
}

// unhookedIndexer replaces e's indexer with one whose hooks are on no
// fan: like another server's, it learns of a write only by a reconcile.
func unhookedIndexer(t *testing.T, e *testEnv, set func(h *historyIndexer)) {
	t.Helper()
	histSettle(t, e)
	e.srv.stopHistory()
	old := e.srv.history
	h := newHistoryIndexer(e.srv.store, e.srv.sessionRecords, old.ledger, e.srv.dispatch, old.host, e.srv.log)
	h.brakeman = old.brakeman
	h.after = func(d time.Duration) <-chan time.Time {
		if d == h.reconcileTick {
			return nil
		}
		return time.After(d)
	}
	if set != nil {
		set(h)
	}
	e.srv.history = h
	h.start()
	histSettle(t, e)
}

// A brief another server keeps is found by a later reconcile, whichever
// of its two writes a reconcile came between: the brief's file is there
// and its request does not yet say it keeps one, so that pass removes it,
// and the request's commit after it says to read it again.
func TestAReconcileBetweenABriefsWritesFindsItLater(t *testing.T) {
	e := newTestEnv(t, nil)
	unhookedIndexer(t, e, nil)
	id := keepBriefSplit(t, e, func(id string) {
		histReconcile(t, e)
		histGone(t, e.srv.history.index(), hBriefItemRef(id, 0))
	})
	histReconcile(t, e)
	histEntry(t, e.srv.history.index(), hBriefItemRef(id, 0))
}

// A brief kept while a catch-up lists Brakeman's requests, after the
// listing, is found by the next reconcile: the look at the files was
// taken before the listing.
func TestABriefKeptDuringACatchUpIsFoundLater(t *testing.T) {
	e := newTestEnv(t, nil)
	var once sync.Once
	id := ""
	unhookedIndexer(t, e, func(h *historyIndexer) {
		h.briefsListed = func() {
			once.Do(func() {
				id = keepBrief(t, e, brakeman.DefaultConfig(e.root), nil, fact("the gannet dived", ref(t, "run:hist-00000033")))
			})
		}
	})
	if id == "" {
		t.Fatal("the catch-up listed no requests")
	}
	histGone(t, e.srv.history.index(), hBriefItemRef(id, 0))
	histReconcile(t, e)
	histEntry(t, e.srv.history.index(), hBriefItemRef(id, 0))
}

// hsPages pages through the search for line one row at a time, and
// answers every page as served, its indexing reduced to how far back the
// history holds and stale left out, with whether any page said stale.
func hsPages(t *testing.T, e *testEnv, line string) (pages []map[string]any, stale bool) {
	t.Helper()
	for after := ""; ; {
		q := url.Values{"q": {line}, "limit": {"1"}}
		if after != "" {
			q.Set("after", after)
		}
		status, page := hsGet(t, e, "/api/history", q, adminToken)
		if status != http.StatusOK {
			t.Fatalf("%q, a page: %d %v", line, status, page)
		}
		stale = stale || page["stale"] == true
		delete(page, "stale")
		ixg, _ := page["indexing"].(map[string]any)
		page["indexing"] = ixg["oldest"]
		pages = append(pages, page)
		next, _ := page["next"].(string)
		if next == "" || len(pages) > 50 {
			return pages, stale
		}
		after = next
	}
}

// A forgotten record counts in nothing a search answers, on any page: not
// a row, the total, a cursor, a row's repeats or links, nor how far back
// the history holds; with records forgotten older and newer than the kept
// ones, the indexer held at a gate so the index still holds them, every
// page of a search is the page served once the indexer's pass has
// removed them. An entry and its similar ones the same.
func TestAForgottenRecordCountsInNothing(t *testing.T) {
	e := newTestEnv(t, nil)
	old := e.createSession("cat")
	kept := []string{e.createSession("cat"), e.createSession("cat")}
	young := e.createSession("cat")
	for _, id := range append([]string{old, young}, kept...) {
		e.stopAndWait(id)
	}
	const gate = "hist-00000033"
	saveRecord(t, e.srv, crew.Run{ID: gate, Label: "the gate", Cwd: e.root, StartedAt: histNow})
	histSettle(t, e)
	lines := []string{"kind:session", "session:" + strings.Join([]string{old, kept[0], kept[1], young}, ",")}

	held, release := make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	let := func() { once.Do(func() { close(release) }) }
	t.Cleanup(let)
	freshIndexer(t, e, func(h *historyIndexer) {
		h.afterMark = func(key string) {
			if key == hRunRef(gate) {
				select {
				case held <- struct{}{}:
				default:
				}
				<-release
			}
		}
	})
	saveRecord(t, e.srv, crew.Run{ID: gate, Label: "the gate, again", Cwd: e.root, StartedAt: histNow})
	<-held
	for _, id := range []string{old, young} {
		if resp, out := e.do("DELETE", "/api/history/sessions/"+id, adminToken, nil); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("forget %s: %d %v", id, resp.StatusCode, out)
		}
		histEntry(t, e.srv.history.index(), hSessionRef(id)) // still indexed: the gate holds its removal
	}
	before := map[string][]map[string]any{}
	for _, line := range lines {
		pages, stale := hsPages(t, e, line)
		if !stale {
			t.Fatalf("%q: no page says the index still held a forgotten record", line)
		}
		before[line] = pages
	}
	entryBefore := hsEntryOf(t, e, hSessionRef(kept[0]))

	let()
	histSettle(t, e)
	for _, id := range []string{old, young} {
		histGone(t, e.srv.history.index(), hSessionRef(id))
	}
	for _, line := range lines {
		after, stale := hsPages(t, e, line)
		if stale {
			t.Fatalf("%q: stale once the index holds none", line)
		}
		if !reflect.DeepEqual(before[line], after) {
			t.Errorf("%q with the forgotten records still indexed:\n%v\nonce they are removed:\n%v", line, before[line], after)
		}
		if len(after) != 2 || after[0]["total"] != float64(2) || after[1]["total"] != float64(2) {
			t.Errorf("%q: %d pages, %v: one kept record a page, each counting two", line, len(after), after)
		}
	}
	if entryAfter := hsEntryOf(t, e, hSessionRef(kept[0])); !reflect.DeepEqual(entryBefore, entryAfter) {
		t.Errorf("an entry beside forgotten records:\n%v\nonce they are removed:\n%v", entryBefore, entryAfter)
	}
}

// historyRepo makes a repository at dir with one commit holding name, and
// answers the commit's id.
func historyRepo(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "commit", "-q", "-m", name}} {
		if _, err := gitcli.Run(t.Context(), dir, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	id, err := gitcli.Run(t.Context(), dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(id)
}

// Whether a result commit is in its run's repository is asked of git in
// the run's folder as this reply opened it, never at its path again: the
// folder renamed and a link to another repository put at its path after
// it was opened, the answer is the opened folder's (no), nothing of the
// other's (which holds the commit); the folder removed after it was
// opened, the answer is unknown, never no.
func TestACommitIsLookedForInTheFolderOpened(t *testing.T) {
	e := newTestEnv(t, nil)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	if runtime.GOOS != "linux" {
		t.Skip("git starts in an open directory on Linux only; elsewhere the answer is unknown")
	}
	folder, other := filepath.Join(e.root, "proj"), filepath.Join(e.root, "other")
	historyRepo(t, folder, "proj.txt")
	onlyOther := historyRepo(t, other, "other.txt")
	ld := e.srv.ledger
	const run = "hist-00000034"
	histCreate(t, ld, run, "", histCrew(folder))
	histTx(t, ld, run, func(tx *ledger.Tx) error {
		return tx.PutAttempt(histAttempt("build", 1, ledger.AttemptFailed, "", commitID(0), onlyOther))
	})
	histSettle(t, e)
	present := func() any {
		c, _ := availOf(hsEntryOf(t, e, attemptKey(run, "build", 1)))["commit"].(map[string]any)
		return c["present"]
	}
	if got := present(); got != string(answerNo) {
		t.Fatalf("a commit its folder does not hold: %v", got)
	}
	t.Cleanup(func() { historyBeforeGit = nil })
	historyBeforeGit = func(string) {
		historyBeforeGit = nil
		if err := os.Rename(folder, folder+".moved"); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(other, folder); err != nil {
			t.Error(err)
		}
	}
	if got := present(); got != string(answerNo) {
		t.Fatalf("the folder swapped for a link to a repository holding the commit, after it was opened: %v, the opened folder's is no", got)
	}
	if err := os.Remove(folder); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(folder+".moved", folder); err != nil {
		t.Fatal(err)
	}
	historyBeforeGit = func(string) {
		historyBeforeGit = nil
		if err := os.RemoveAll(folder); err != nil {
			t.Error(err)
		}
	}
	if got := present(); got != string(answerUnknown) {
		t.Fatalf("the folder removed after it was opened: %v, want unknown", got)
	}
}

// slowReader is a listener whose connections keep the least the system
// lets a socket buffer for sending.
type slowReader struct{ net.Listener }

func (l slowReader) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetWriteBuffer(1)
	}
	return c, err
}

// A client that never reads its replies holds the connection's goroutine
// no longer than the write's deadline: replies small enough for the
// server's own buffers, asked for one after another on one connection by
// a client that reads none, fill the socket, and the reply that no longer
// fits fails at its deadline and the connection is closed, rather than
// net/http's flush after the handler waiting on it for ever.
func TestAClientThatNeverReadsHoldsNoConnection(t *testing.T) {
	e := newTestEnv(t, nil)
	histSettle(t, e)
	historyWriteLimit = 200 * time.Millisecond
	t.Cleanup(func() { historyWriteLimit = historyWriteTimeout })
	closed := make(chan struct{})
	var once sync.Once
	ts := httptest.NewUnstartedServer(e.srv.Handler())
	ts.Listener = slowReader{ts.Listener}
	ts.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateClosed {
			once.Do(func() { close(closed) })
		}
	}
	ts.Start()
	t.Cleanup(ts.Close)
	d := net.Dialer{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) { serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 1) }); err != nil {
			return err
		}
		return serr
	}}
	conn, err := d.DialContext(t.Context(), "tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	req := "GET /api/history?q=x HTTP/1.1\r\nHost: " + ts.Listener.Addr().String() + "\r\nAuthorization: Bearer " + adminToken + "\r\n\r\n"
	go func() {
		for range 4000 {
			if _, err := io.WriteString(conn, req); err != nil {
				return
			}
		}
	}()
	select {
	case <-closed:
	case <-time.After(2 * testWait):
		t.Fatal("the connection of a client that reads nothing is held past every write's deadline")
	}
	if n := e.srv.historyReads.inFlight(); n != 0 {
		t.Fatalf("%d slots held", n)
	}
}
