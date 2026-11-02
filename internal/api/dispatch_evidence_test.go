package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// A ticket's runs, a run's candidates with their receipts, and the
// attempt's ticket as launched (docs/plans/dispatch-evidence.md, PR-8).
// The tests find their runs by id and their entries by the launch, link
// or run they name, never by a list's length alone; they wait on the
// ledger's rows and the routes' answers, never on the clock. Each runs
// git under a HOME of its own (planRepo), so none is parallel.

// ticketRunPages follows the ticket's runs page by page through next, and
// answers every entry in order, failing t for a page over ten entries or
// over MaxTicketRunsReply as encoded, and for a next on a page that is
// not full.
func ticketRunPages(t *testing.T, e *testEnv, key string) (entries []map[string]any, pages int) {
	t.Helper()
	path := "/api/dispatch/tickets/" + key + "/runs"
	for {
		status, raw, out := e.call(t, "GET", path, nil)
		if status != http.StatusOK {
			t.Fatalf("%s: %d %s", path, status, raw)
		}
		if len(raw) > MaxTicketRunsReply {
			t.Fatalf("%s: %d bytes, past MaxTicketRunsReply", path, len(raw))
		}
		page := listOf(t, out, "entries")
		pages++
		entries = append(entries, page...)
		next, _ := out["next"].(string)
		if next == "" {
			return entries, pages
		}
		if len(page) != ticketRunsPage {
			t.Fatalf("%s: a page of %d entries with a next", path, len(page))
		}
		path = "/api/dispatch/tickets/" + key + "/runs?before=" + next
	}
}

// entryWith is the one entry of the kind whose part (launch, link or run)
// has the id; it fails t for none or more than one.
func entryWith(t *testing.T, entries []map[string]any, kind, part, id string) map[string]any {
	t.Helper()
	var found map[string]any
	for _, e := range entries {
		p, _ := e[part].(map[string]any)
		if e["kind"] == kind && p != nil && p["id"] == id {
			if found != nil {
				t.Fatalf("two %s entries of %s %s", kind, part, id)
			}
			found = e
		}
	}
	if found == nil {
		t.Fatalf("no %s entry of %s %s among %v", kind, part, id, entries)
	}
	return found
}

// legacyRun launches a crew without tasks of one cat in the project's
// folder from the Crews page, a run DS1 links by hand, and answers its id
// once its member's session runs.
func legacyRun(t *testing.T, e *testEnv, cwd string) (run, session string) {
	t.Helper()
	c, err := e.srv.crews.Create(crew.Crew{Name: "Legacy", Goal: "look", Cwd: cwd, Where: crew.WhereServer, Isolation: crew.IsolationNone,
		Members: []crew.Member{{Name: "lead", AgentID: "cat", Start: crew.Start{When: crew.StartImmediately}}}})
	if err != nil {
		t.Fatal(err)
	}
	out := e.expect(t, "POST", "/api/crews/"+c.ID+"/launch", nil, http.StatusCreated, "")
	run = out["run"].(map[string]any)["id"].(string)
	waitUntil(t, "the legacy run's member running", func() bool {
		r, ok := e.srv.runs.Get(run)
		if ok && len(r.Members) == 1 && r.Members[0].Status == crew.MemberRunning {
			session = r.Members[0].SessionID
			return true
		}
		return false
	})
	return run, session
}

// linkRun links the run to the ticket by hand (DS1's run link), failing t
// unless the route answers the status and code, and answers the link's id.
func linkRun(t *testing.T, e *testEnv, key, run string, status int, code string) string {
	t.Helper()
	out := e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/links", map[string]any{"opId": opID(), "kind": dispatch.LinkRun, "ref": run}, status, code)
	if status != http.StatusCreated {
		return ""
	}
	return receiptOf(t, out)["id"].(string)
}

// restartAfterStop stops the run, shuts the server down and starts
// another over its store, as `conductor serve` comes up again.
func (l *launchTest) restartAfterStop(t *testing.T, run string) *testEnv {
	t.Helper()
	l.expect(t, "POST", "/api/runs/"+run+"/stop", nil, http.StatusOK, "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	l.srv.Shutdown(ctx)
	again := l.restart()
	t.Cleanup(func() { stopEveryRun(again.srv) })
	return again
}

// A ticket's runs: a launch with its run's summary (its member and its
// session, its tasks counted, the blocker its attempt reported and the
// question its session asks, what changed since), then, after a restart,
// the run from its record and its continuation as an entry of its own
// under the launch, and a run linked by hand as a link's entry; the
// newest first, each once, every page under its bound.
func TestTicketRunsLaunchesAndLinks(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	launch := entityOf(t, l.launch(t, l.testEnv, nil, http.StatusCreated, ""), "launch")
	launchID, runID := launch["id"].(string), launch["runId"].(string)
	sid := runningAttempt(t, l.testEnv, runID, "build", 1)["sessionId"].(string)
	if resp, out := l.do("POST", "/api/sessions/"+sid+"/task/blocked", l.agentToken(sid), map[string]any{"reason": "the API key is missing", "message": "Where is the key?"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("blocked: %d %v", resp.StatusCode, out)
	}
	var entries []map[string]any
	waitUntil(t, "the run's question counted", func() bool {
		entries, _ = ticketRunPages(t, l.testEnv, "CND-1")
		run, _ := entryWith(t, entries, entryLaunch, "launch", launchID)["run"].(map[string]any)
		return run != nil && run["questions"] == 1.0
	})
	if len(entries) != 1 {
		t.Fatalf("the entries: %v", entries)
	}
	e := entries[0]
	run := e["run"].(map[string]any)
	members := run["members"].([]any)
	blockers := run["blockers"].([]any)
	tasks := run["tasks"].(map[string]any)
	if run["id"] != runID || run["record"] != false || run["ticket"].(map[string]any)["launch"] != launchID || len(members) != 1 ||
		members[0].(map[string]any)["sessionId"] != sid || members[0].(map[string]any)["name"] != "lead" || len(tasks) != 1 {
		t.Fatalf("the launch's run: %v", run)
	}
	if len(blockers) != 1 || blockers[0].(map[string]any)["task"] != "build" || blockers[0].(map[string]any)["reason"] != "the API key is missing" {
		t.Fatalf("the blockers: %v", blockers)
	}
	if changed := e["changed"].(map[string]any); len(changed["fields"].([]any)) != 0 || changed["comments"] != 0.0 {
		t.Fatalf("changed since: %v", changed)
	}

	// After a restart: the run from its record, its continuation under the
	// launch, and a run linked by hand.
	again := l.restartAfterStop(t, runID)
	next := again.expect(t, "POST", "/api/runs/"+runID+"/resume", nil, http.StatusCreated, "")["run"].(map[string]any)["id"].(string)
	legacy, _ := legacyRun(t, again, l.repo)
	linkID := linkRun(t, again, "CND-1", legacy, http.StatusCreated, "")
	entries, pages := ticketRunPages(t, again, "CND-1")
	if len(entries) != 3 || pages != 1 {
		t.Fatalf("after the restart: %d entries on %d pages: %v", len(entries), pages, entries)
	}
	byLink := entryWith(t, entries, entryLink, "link", linkID)
	cont := entryWith(t, entries, entryRun, "run", next)
	byLaunch := entryWith(t, entries, entryLaunch, "launch", launchID)
	if !reflect.DeepEqual(entries, []map[string]any{byLink, cont, byLaunch}) {
		t.Fatalf("not newest first: %v", entries)
	}
	if r := byLaunch["run"].(map[string]any); r["id"] != runID || r["record"] != true || r["resumedBy"] != next {
		t.Fatalf("the launched run from its record: %v", r)
	}
	if cont["continues"] != runID || cont["launch"].(map[string]any)["id"] != launchID || cont["link"] != nil {
		t.Fatalf("the continuation: %v", cont)
	}
	if r := cont["run"].(map[string]any); r["resumedFrom"] != runID || r["ticket"].(map[string]any)["launch"] != launchID {
		t.Fatalf("the continuation's run: %v", r)
	}
	if r := byLink["run"].(map[string]any); r["id"] != legacy || byLink["link"].(map[string]any)["ref"] != legacy || byLink["contested"] != nil {
		t.Fatalf("the linked run: %v", byLink)
	}
	again.expect(t, "GET", "/api/dispatch/tickets/CND-1/runs?before=nonsense", nil, http.StatusBadRequest, "invalid_request")
	again.expect(t, "GET", "/api/dispatch/tickets/CND-9/runs", nil, http.StatusNotFound, "not_found")
}

// putLedgerRun writes a run's row into the ledger as its first
// transaction does, continuing from (marked continued by it) when from is
// not "".
func putLedgerRun(t *testing.T, ld *ledger.Ledger, row ledger.RunRow) {
	t.Helper()
	if _, err := ld.Transition(context.Background(), row.ID, func(tx *ledger.Tx) error {
		row.CreatedAt = tx.Now()
		if err := tx.CreateRun(row); err != nil {
			return err
		}
		if row.ContinuedFrom != "" {
			return tx.Continued(row.ContinuedFrom)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// saveRecord saves a run's record, a legacy run's as a server before this
// one left it.
func saveRecord(t *testing.T, srv *Server, r crew.Run) {
	t.Helper()
	if r.Name == "" {
		r.CrewID, r.Name, r.Isolation, r.State = "legacy", "Legacy", crew.IsolationNone, crew.RunStopped
	}
	if err := srv.records.Save(r); err != nil {
		t.Fatal(err)
	}
}

// Every launch, run and continuation of a ticket is on some page: 200
// launches (the bound), one of them continued 21 times, and hand links,
// one to B, a continuation of A, which lists A and B once each under that
// link; following next reaches each once, the newest continuation among
// them. A chain two tickets' links hold (as a version 1 store let them) is
// contested: each ticket lists its own linked run alone, marked with the
// other's key and the words, the chain's other runs on neither, and a new
// link into it is refused.
func TestTicketRunsPageThroughEverything(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	ctx := t.Context()
	ds := l.srv.dispatch
	var launches []string
	for range dispatch.MaxLaunches {
		launches = append(launches, doneOf(t)(ds.Launch(ctx, adminOp(""), "CND-1", l.plan, "")).ID)
	}
	first := l.launchOf(t, launches[0])
	crewJSON, _ := json.Marshal(l.crew)
	row := ledger.RunRow{ID: first.RunID, CrewID: l.crew.ID, Crew: string(crewJSON), Base: l.head, State: ledger.RunFinished, Ticket: l.ticketID, Package: string(first.Package)}
	putLedgerRun(t, l.srv.ledger, row)
	doneOf(t)(ds.StartedLaunch(ctx, dispatch.Op{}, first.ID, nil))
	conts := []string{first.RunID}
	for i := 1; i <= 21; i++ {
		row.ID, row.ContinuedFrom = fmt.Sprintf("%s-%08x", l.crew.ID, i), conts[len(conts)-1]
		putLedgerRun(t, l.srv.ledger, row)
		conts = append(conts, row.ID)
	}
	// Hand links: B, a continuation of A, and five runs alone.
	now := time.Now().UTC()
	saveRecord(t, l.srv, crew.Run{ID: "legacy-000000a1", StartedAt: now.Add(-2 * time.Hour), ResumedBy: "legacy-000000b1"})
	saveRecord(t, l.srv, crew.Run{ID: "legacy-000000b1", StartedAt: now.Add(-time.Hour), ResumedFrom: "legacy-000000a1"})
	linkB := linkRun(t, l.testEnv, "CND-1", "legacy-000000b1", http.StatusCreated, "")
	linkRun(t, l.testEnv, "CND-1", "legacy-000000a1", http.StatusConflict, dispatch.RefusalRunLinked)
	var alone []string
	for i := range 5 {
		id := fmt.Sprintf("legacy-0000010%d", i)
		saveRecord(t, l.srv, crew.Run{ID: id, StartedAt: now})
		alone = append(alone, linkRun(t, l.testEnv, "CND-1", id, http.StatusCreated, ""))
	}

	entries, pages := ticketRunPages(t, l.testEnv, "CND-1")
	if want := len(launches) + 21 + 6 + 1; len(entries) != want || pages != (want+ticketRunsPage-1)/ticketRunsPage {
		t.Fatalf("%d entries on %d pages, want %d", len(entries), pages, want)
	}
	for _, id := range launches {
		e := entryWith(t, entries, entryLaunch, "launch", id)
		if r, _ := e["run"].(map[string]any); (id == first.ID) != (r != nil) || r != nil && (r["id"] != first.RunID || r["record"] != true) {
			t.Fatalf("launch %s: %v", id, e)
		}
	}
	for i, id := range conts[1:] {
		e := entryWith(t, entries, entryRun, "run", id)
		if e["continues"] != conts[i] || e["launch"].(map[string]any)["id"] != first.ID {
			t.Fatalf("continuation %d: %v", i+1, e)
		}
	}
	if e := entryWith(t, entries, entryLink, "link", linkB); e["run"].(map[string]any)["id"] != "legacy-000000b1" {
		t.Fatalf("the link to B: %v", e)
	}
	if e := entryWith(t, entries, entryRun, "run", "legacy-000000a1"); e["link"].(map[string]any)["id"] != linkB || e["continues"] != nil || e["launch"] != nil {
		t.Fatalf("A under B's link: %v", e)
	}
	for _, id := range alone {
		entryWith(t, entries, entryLink, "link", id)
	}
	// The newest entries come first: the hand links, made last.
	if entries[0]["kind"] != entryLink {
		t.Fatalf("the newest entry: %v", entries[0])
	}

	// A contested chain: X on CND-2 and Y on CND-3 linked while unrelated,
	// then found to be one chain X, Y, Z.
	l.newDispatchTicket(t, "CND", "Two", nil)
	l.newDispatchTicket(t, "CND", "Three", nil)
	saveRecord(t, l.srv, crew.Run{ID: "legacy-000000c1", StartedAt: now.Add(-3 * time.Hour)})
	saveRecord(t, l.srv, crew.Run{ID: "legacy-000000c2", StartedAt: now.Add(-2 * time.Hour)})
	onX := linkRun(t, l.testEnv, "CND-2", "legacy-000000c1", http.StatusCreated, "")
	onY := linkRun(t, l.testEnv, "CND-3", "legacy-000000c2", http.StatusCreated, "")
	saveRecord(t, l.srv, crew.Run{ID: "legacy-000000c1", StartedAt: now.Add(-3 * time.Hour), ResumedBy: "legacy-000000c2"})
	saveRecord(t, l.srv, crew.Run{ID: "legacy-000000c2", StartedAt: now.Add(-2 * time.Hour), ResumedFrom: "legacy-000000c1", ResumedBy: "legacy-000000c3"})
	saveRecord(t, l.srv, crew.Run{ID: "legacy-000000c3", StartedAt: now.Add(-time.Hour), ResumedFrom: "legacy-000000c2"})
	for _, tc := range []struct{ key, link, run, other, words string }{
		{"CND-2", onX, "legacy-000000c1", "CND-3", "run legacy-000000c2, which CND-3 links, continues run legacy-000000c1: remove one of the links"},
		{"CND-3", onY, "legacy-000000c2", "CND-2", "run legacy-000000c2 continues run legacy-000000c1, which CND-2 links: remove one of the links"},
	} {
		entries, _ := ticketRunPages(t, l.testEnv, tc.key)
		if len(entries) != 1 {
			t.Fatalf("%s: %v", tc.key, entries)
		}
		e := entryWith(t, entries, entryLink, "link", tc.link)
		c, _ := e["contested"].(map[string]any)
		if e["run"].(map[string]any)["id"] != tc.run || c == nil || !reflect.DeepEqual(c["keys"], []any{tc.other}) || c["words"] != tc.words {
			t.Fatalf("%s: %v", tc.key, e)
		}
	}
	linkRun(t, l.testEnv, "CND-1", "legacy-000000c3", http.StatusConflict, dispatch.RefusalRunLinked)
}

// A chain one ticket launched and another links by hand (X linked to
// CND-2 while unrelated, then written as a continuation of A, which CND-1
// launched) is contested on both: CND-1's launch lists its run alone,
// marked with CND-2's key and words naming CND-2's link, X an entry of
// neither ticket's chain; CND-2's link lists X alone, marked with CND-1's
// key and words naming the launch; a ticket with no runs answers an empty
// list. CND-2's link removed, the chain is CND-1's whole. While the
// ledger cannot be read the page still answers, a run only the ledger had
// an entry without its run.
func TestTicketRunsContestedByALaunch(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	ctx := t.Context()
	ds := l.srv.dispatch
	l.newDispatchTicket(t, "CND", "Two", nil)
	l.newDispatchTicket(t, "CND", "Three", nil)
	if status, raw, _ := l.call(t, "GET", "/api/dispatch/tickets/CND-3/runs", nil); status != http.StatusOK || !strings.Contains(string(raw), `"entries":[]`) {
		t.Fatalf("a ticket with no runs: %d %s", status, raw)
	}
	la := l.launchOf(t, doneOf(t)(ds.Launch(ctx, adminOp(""), "CND-1", l.plan, "")).ID)
	crewJSON, _ := json.Marshal(l.crew)
	row := ledger.RunRow{ID: la.RunID, CrewID: l.crew.ID, Crew: string(crewJSON), Base: l.head, State: ledger.RunFinished, Ticket: l.ticketID, Package: string(la.Package)}
	putLedgerRun(t, l.srv.ledger, row)
	doneOf(t)(ds.StartedLaunch(ctx, dispatch.Op{}, la.ID, nil))
	x := "legacy-000000d1"
	saveRecord(t, l.srv, crew.Run{ID: x, StartedAt: time.Now().UTC()})
	onX := linkRun(t, l.testEnv, "CND-2", x, http.StatusCreated, "")
	row.ID, row.ContinuedFrom, row.Ticket, row.Package = x, la.RunID, "", ""
	putLedgerRun(t, l.srv.ledger, row)
	for _, tc := range []struct{ key, kind, part, id, run, other, words string }{
		{"CND-1", entryLaunch, "launch", la.ID, la.RunID, "CND-2", "run " + x + ", which CND-2 links, continues run " + la.RunID + ": remove CND-2's link"},
		{"CND-2", entryLink, "link", onX, x, "CND-1", "run " + x + " continues run " + la.RunID + ", launched from CND-1: remove CND-2's link"},
	} {
		entries, _ := ticketRunPages(t, l.testEnv, tc.key)
		if len(entries) != 1 {
			t.Fatalf("%s: %v", tc.key, entries)
		}
		e := entryWith(t, entries, tc.kind, tc.part, tc.id)
		c, _ := e["contested"].(map[string]any)
		if e["run"].(map[string]any)["id"] != tc.run || c == nil || !reflect.DeepEqual(c["keys"], []any{tc.other}) || c["words"] != tc.words {
			t.Fatalf("%s: %v", tc.key, e)
		}
	}
	l.expect(t, "POST", "/api/dispatch/tickets/CND-2/links/"+onX+"/delete", map[string]any{"opId": opID()}, http.StatusOK, "")
	entries, _ := ticketRunPages(t, l.testEnv, "CND-1")
	if len(entries) != 2 || entryWith(t, entries, entryLaunch, "launch", la.ID)["contested"] != nil ||
		entryWith(t, entries, entryRun, "run", x)["continues"] != la.RunID {
		t.Fatalf("the chain CND-1's whole: %v", entries)
	}

	// The ledger closed: the page answers, the launch's run unread.
	if err := l.srv.ledger.Close(); err != nil {
		t.Fatal(err)
	}
	entries, _ = ticketRunPages(t, l.testEnv, "CND-1")
	if e := entryWith(t, entries, entryLaunch, "launch", la.ID); e["run"] != nil {
		t.Fatalf("the launch's run with the ledger closed: %v", e)
	}
}

// A chain whose walk fails is never judged: while the walk of B's chain
// fails (a ledger read's error, through chainWalkSeam), a link to B is
// 503 ledger_unavailable with nothing kept, no link and no receipt, and
// the ticket's runs page is 503 too; once the walk reads again, the same
// operation id is refused run_linked, B being of the chain A's link holds.
func TestALinkIntoAChainThatCannotBeWalkedIsNotKept(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	l.newDispatchTicket(t, "CND", "Two", nil)
	now := time.Now().UTC()
	saveRecord(t, l.srv, crew.Run{ID: "legacy-000000e1", StartedAt: now.Add(-time.Hour), ResumedBy: "legacy-000000e2"})
	saveRecord(t, l.srv, crew.Run{ID: "legacy-000000e2", StartedAt: now, ResumedFrom: "legacy-000000e1"})
	linkRun(t, l.testEnv, "CND-1", "legacy-000000e1", http.StatusCreated, "")
	fail := func(run string) error {
		if run == "legacy-000000e2" {
			return errors.New("database is locked")
		}
		return nil
	}
	chainWalkSeam.Store(&fail)
	t.Cleanup(func() { chainWalkSeam.Store(nil) })
	op := opID()
	body := map[string]any{"opId": op, "kind": dispatch.LinkRun, "ref": "legacy-000000e2"}
	l.expect(t, "POST", "/api/dispatch/tickets/CND-2/links", body, http.StatusServiceUnavailable, "ledger_unavailable")
	l.expect(t, "GET", "/api/dispatch/ops/"+op, nil, http.StatusNotFound, "not_found")
	if links, err := l.srv.dispatch.Links(t.Context(), "CND-2"); err != nil || len(links) != 0 {
		t.Fatalf("CND-2's links: %v %v", links, err)
	}
	l.expect(t, "GET", "/api/dispatch/tickets/CND-1/runs", nil, http.StatusServiceUnavailable, "ledger_unavailable")
	chainWalkSeam.Store(nil)
	out := l.expect(t, "POST", "/api/dispatch/tickets/CND-2/links", body, http.StatusConflict, dispatch.RefusalRunLinked)
	if receiptOf(t, out)["key"] != "CND-1" {
		t.Fatalf("the link once the chain reads: %v", out)
	}
}

// A chain whose older run is gone is held still by a link to it: A linked
// to CND-1 and continued by B, A's record unreadable, a link of B to
// CND-2 is 503 ledger_unavailable with nothing kept (A's marks may name
// more of the chain); A's record pruned, B still naming it, the link is
// refused run_linked naming CND-1, whose link holds the chain by A, and
// B's holders name CND-1.
func TestALinkToAPrunedRunHoldsItsChain(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	l.newDispatchTicket(t, "CND", "Two", nil)
	now := time.Now().UTC()
	a, b := "legacy-000000f1", "legacy-000000f2"
	saveRecord(t, l.srv, crew.Run{ID: a, StartedAt: now.Add(-time.Hour), ResumedBy: b})
	saveRecord(t, l.srv, crew.Run{ID: b, StartedAt: now, ResumedFrom: a})
	linkRun(t, l.testEnv, "CND-1", a, http.StatusCreated, "")

	record := filepath.Join(l.srv.cfg.DataDir, "runs", a+".json")
	if err := os.WriteFile(record, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	op := opID()
	body := map[string]any{"opId": op, "kind": dispatch.LinkRun, "ref": b}
	l.expect(t, "POST", "/api/dispatch/tickets/CND-2/links", body, http.StatusServiceUnavailable, "ledger_unavailable")
	l.expect(t, "GET", "/api/dispatch/ops/"+op, nil, http.StatusNotFound, "not_found")
	if links, err := l.srv.dispatch.Links(t.Context(), "CND-2"); err != nil || len(links) != 0 {
		t.Fatalf("CND-2's links with A unreadable: %v %v", links, err)
	}

	if err := l.srv.records.Delete(a); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := l.srv.records.Get(a); ok || err != nil {
		t.Fatalf("A's record pruned: %v %v", ok, err)
	}
	out := l.expect(t, "POST", "/api/dispatch/tickets/CND-2/links", body, http.StatusConflict, dispatch.RefusalRunLinked)
	if receiptOf(t, out)["key"] != "CND-1" {
		t.Fatalf("the link of B with A pruned: %v", out)
	}
	holders, err := l.srv.dispatch.ChainHolders(t.Context(), b)
	if err != nil || len(holders) != 1 || holders[0].Key != "CND-1" || holders[0].Run != a {
		t.Fatalf("B's holders with A pruned: %v %v", holders, err)
	}
	if links, err := l.srv.dispatch.Links(t.Context(), "CND-2"); err != nil || len(links) != 0 {
		t.Fatalf("CND-2's links: %v %v", links, err)
	}
}

// The largest page of a ticket's runs: ten entries, each with a launch
// whose texts are at their escaped worst (its why, its label, its agents),
// a link at its bounds, the run's summary with twelve members and twenty
// blockers whose reasons are cut to 200 characters, what changed since,
// and a contest naming twenty keys, encoded as the route encodes it, is
// under MaxTicketRunsReply.
func TestTheTicketRunsPageAtItsWorstIsUnderItsBound(t *testing.T) {
	worst := func(n int) string { return strings.Repeat(string(rune(0x2028)), n) }
	hex64 := strings.Repeat("f", 64)
	agents := make([]dispatch.LaunchAgent, dispatch.MaxPlanAgents)
	for i := range agents {
		agents[i] = dispatch.LaunchAgent{Member: strings.Repeat("m", 40), Agent: strings.Repeat("a", 64), Version: strings.Repeat(`"`, dispatch.MaxLaunchAgentVersion)}
	}
	members := make([]crew.MemberState, 40)
	for i := range members {
		members[i] = crew.MemberState{Name: strings.Repeat("m", 40), SessionID: strings.Repeat("s", 32), Status: crew.MemberRunning}
	}
	tasks := make([]crew.TaskState, 128)
	for i := range tasks {
		tasks[i] = crew.TaskState{ID: fmt.Sprintf("%040d", i), State: ledger.TaskRunning,
			Blocker: &crew.Blocker{Reason: worst(500), Message: worst(4000), By: strings.Repeat("m", 40), At: time.Now()}}
	}
	stopped := time.Now()
	run := summarizeRun(crew.Run{ID: strings.Repeat("c", 40) + "-0000aaaa", Label: worst(dispatch.MaxLaunchLabel), State: crew.RunRunning, Outcome: ledger.OutcomeCancelled,
		StartedAt: time.Now(), StoppedAt: &stopped, ResumedFrom: strings.Repeat("c", 40) + "-0000aaab", ResumedBy: strings.Repeat("c", 40) + "-0000aaac",
		Ticket:  &crew.RunTicket{ID: strings.Repeat("1", 16), Key: "ABCDEF-999999", Rev: 1 << 40, Plan: strings.Repeat("2", 16), PlanN: dispatch.MaxPlans, Launch: strings.Repeat("0", 16), Digest: hex64},
		Members: members, Tasks: tasks, NeedsInput: 40}, false)
	if len(run.Members) != maxSummaryMembers || len(run.Blockers) != maxSummaryBlockers || len([]rune(run.Blockers[0].Reason)) != maxSummaryReason+1 {
		t.Fatalf("the summary is not bounded: %d members, %d blockers", len(run.Members), len(run.Blockers))
	}
	launch := dispatch.LaunchSummary{ID: strings.Repeat("0", 16), TicketID: strings.Repeat("1", 16), TicketKey: "ABCDEF-999999", PlanID: strings.Repeat("2", 16), PlanN: dispatch.MaxPlans,
		RunID: run.ID, Label: worst(dispatch.MaxLaunchLabel), TicketRev: 1 << 40, Intent: hex64, Criteria: hex64, PackageSHA256: hex64, State: dispatch.LaunchFailed,
		Why: worst(dispatch.MaxLaunchWhy), Agents: agents, CreatedAt: time.Now(), CreatedBy: worst(dispatch.MaxByName), Fingerprint: hex64, StartedAt: time.Now(), Rev: 1 << 40}
	link := dispatch.Link{ID: strings.Repeat("3", 16), Kind: dispatch.LinkDelivery, Ref: strings.Repeat("r", dispatch.MaxLinkURL), URL: strings.Repeat("u", dispatch.MaxLinkURL),
		Note: worst(dispatch.MaxLinkNote), Candidate: strings.Repeat("c", 200), Verified: json.RawMessage(`"` + strings.Repeat("v", 8<<10-2) + `"`),
		CreatedAt: time.Now(), CreatedBy: worst(dispatch.MaxByName)}
	keys := make([]string, maxContestedKeys)
	for i := range keys {
		keys[i] = "ABCDEF-999999"
	}
	page := make([]ticketRunEntry, ticketRunsPage)
	for i := range page {
		page[i] = ticketRunEntry{Kind: entryLaunch, Launch: &launch, Link: &link, Run: &run,
			Changed:   &dispatch.LaunchChanges{Fields: []string{"title", "description", "acceptance"}, Comments: dispatch.MaxComments, Rev: 1 << 40},
			Contested: &contested{Keys: keys, Words: fmt.Sprintf("run %s, which %s links, continues run %s: remove one of the links", run.ID, keys[0], run.ID)},
			Continues: run.ID}
	}
	rec := httptest.NewRecorder()
	writeDispatch(rec, http.StatusOK, map[string]any{"entries": page, "next": runsCursor{at: time.Now(), kind: entryLaunch, id: strings.Repeat("c", 40) + "-0000aaaa"}.String()})
	if n := rec.Body.Len(); n > MaxTicketRunsReply {
		t.Fatalf("the largest page of a ticket's runs is %d bytes, its bound %d", n, MaxTicketRunsReply)
	}
}

// candidatesOf is GET /api/runs/{run}/candidates's reply, its query q.
func candidatesOf(t *testing.T, e *testEnv, run, q string) map[string]any {
	t.Helper()
	path := "/api/runs/" + run + "/candidates"
	if q != "" {
		path += "?" + q
	}
	status, raw, out := e.call(t, "GET", path, nil)
	if status != http.StatusOK || len(raw) > MaxCandidatesReply {
		t.Fatalf("%s: %d, %d bytes: %.400s", path, status, len(raw), raw)
	}
	return out
}

// taskOn is the task's entry of a candidates reply.
func taskOn(t *testing.T, out map[string]any, task string) map[string]any {
	t.Helper()
	for _, tc := range listOf(t, out, "candidates") {
		if tc["task"] == task {
			return tc
		}
	}
	t.Fatalf("no task %s in %v", task, out)
	return nil
}

// candidateList is a task entry's candidates, newest first.
func candidateList(tc map[string]any) []map[string]any {
	var out []map[string]any
	for _, c := range tc["candidates"].([]any) {
		out = append(out, c.(map[string]any))
	}
	return out
}

// A run's candidates are bound to their commits: a check failing at
// attempt 1 and passing at attempt 2, each attempt's receipt its own
// commit's (the ledger's row of it has that subject), a review's verdict
// and its findings counted; after a restart, the run the engine let go
// answers the same from the ledger. Then a run as the ledger alone has
// it: six candidates a task and more, Accept anyway and a withdrawal
// marked, a row of another commit or phase never a receipt, a review's
// counts, more than eight tasks paged by next.
func TestCandidatesAreBoundToTheirCommits(t *testing.T) {
	l := newLaunchTest(t, nil, func(c *crew.Crew) {
		c.Members = append(c.Members, crew.Member{Name: "reviewer", AgentID: "ready-cat", Start: crew.Start{When: crew.StartTask}})
		c.Tasks[0].Checks = []crew.Check{{ID: "test", Argv: []string{"test", "-f", "fixed"}}}
		c.Tasks = append(c.Tasks, crew.Task{ID: "review", Member: "reviewer", Kind: crew.TaskReview, Instructions: "Review it.", Needs: []string{"build"}})
	})
	runID := entityOf(t, l.launch(t, l.testEnv, nil, http.StatusCreated, ""), "launch")["runId"].(string)
	a1 := runningAttempt(t, l.testEnv, runID, "build", 1)
	c1 := commitFile(t, a1["worktree"].(string), "a.txt", "one\n")
	l.submit(t, a1["sessionId"].(string))
	ledgerAttemptIn(t, l.srv, runID, "build", 1, ledger.AttemptFailed)
	a2 := runningAttempt(t, l.testEnv, runID, "build", 2)
	c2 := commitFile(t, a2["worktree"].(string), "fixed", "yes\n")
	l.submit(t, a2["sessionId"].(string))
	ledgerAttemptIn(t, l.srv, runID, "build", 2, ledger.AttemptSucceeded)
	rv := runningAttempt(t, l.testEnv, runID, "review", 1)["sessionId"].(string)
	findings := []map[string]any{{"severity": "minor", "message": "a name"}, {"severity": "nit", "message": "a comma"}, {"severity": "minor", "message": "a test"}}
	if resp, out := l.do("POST", "/api/sessions/"+rv+"/task/result", l.agentToken(rv), map[string]any{"verdict": "approved", "findings": findings}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("the review: %d %v", resp.StatusCode, out)
	}
	ledgerAttemptIn(t, l.srv, runID, "review", 1, ledger.AttemptSucceeded)

	out := candidatesOf(t, l.testEnv, runID, "")
	if out["run"] != runID || out["managed"] != true || out["base"] != l.head || out["next"] != nil {
		t.Fatalf("the reply: %v", out)
	}
	build := candidateList(taskOn(t, out, "build"))
	if len(build) != 2 || build[0]["attempt"] != 2.0 || build[0]["commit"] != c2 || build[0]["start"] != c1 || build[0]["standing"] != true ||
		build[1]["attempt"] != 1.0 || build[1]["commit"] != c1 || build[1]["standing"] != false || build[1]["state"] != ledger.AttemptFailed {
		t.Fatalf("build's candidates: %v", build)
	}
	for _, c := range build {
		n := int(c["attempt"].(float64))
		rows, err := l.srv.ledger.Checks(t.Context(), runID, "build", n, false)
		if err != nil {
			t.Fatal(err)
		}
		receipts := c["receipts"].([]any)
		if len(receipts) != 1 {
			t.Fatalf("attempt %d's receipts: %v", n, receipts)
		}
		rc := receipts[0].(map[string]any)
		wantExit := map[int]float64{1: 1, 2: 0}[n]
		if rc["id"] != "test" || rc["phase"] != ledger.PhaseCheck || rc["exit"] != wantExit || rc["where"] != whereNative || rc["logSha256"] == "" || rc["pid"] != nil {
			t.Fatalf("attempt %d's receipt: %v", n, rc)
		}
		i := slices.IndexFunc(rows, func(r ledger.CheckRow) bool { return r.ID == rc["id"] && float64(r.K) == rc["k"] })
		if i < 0 || rows[i].Subject != c["commit"] || rows[i].LogSHA256 != rc["logSha256"] {
			t.Fatalf("attempt %d's receipt is not its commit's row: %v %+v", n, rc, rows)
		}
	}
	review := candidateList(taskOn(t, out, "review"))
	if len(review) != 1 || review[0]["verdict"] != crew.VerdictApproved || review[0]["start"] != c2 || review[0]["commit"] != nil ||
		!reflect.DeepEqual(review[0]["findings"], map[string]any{"minor": 2.0, "nit": 1.0}) || len(review[0]["receipts"].([]any)) != 0 {
		t.Fatalf("the review's candidate: %v", review)
	}

	// The run let go: the same answer, from the ledger.
	again := l.restartAfterStop(t, runID)
	if _, held := again.srv.runs.Get(runID); held {
		t.Fatal("the engine holds the run after the restart")
	}
	if got := candidatesOf(t, again, runID, ""); !reflect.DeepEqual(got, out) {
		t.Fatalf("from the ledger:\n%v\nwhile held:\n%v", got, out)
	}

	// A run as the ledger alone has it.
	synth := "synth-0000abcd"
	sc := crew.Crew{ID: "synth", Name: "Synth", Members: []crew.Member{{Name: "lead", AgentID: "ready-cat"}}}
	for i := 1; i <= 10; i++ {
		sc.Tasks = append(sc.Tasks, crew.Task{ID: fmt.Sprintf("t%02d", i), Member: "lead", Kind: crew.TaskWork})
	}
	sc.Tasks = append(sc.Tasks, crew.Task{ID: "rv", Member: "lead", Kind: crew.TaskReview, Needs: []string{"t01"}})
	scJSON, _ := json.Marshal(sc)
	commit := func(n int) string { return fmt.Sprintf("%040x", n) }
	result := func(r crew.Result) string { b, _ := json.Marshal(r); return string(b) }
	if _, err := again.srv.ledger.Transition(t.Context(), synth, func(tx *ledger.Tx) error {
		if err := tx.CreateRun(ledger.RunRow{ID: synth, CrewID: "synth", Crew: string(scJSON), Base: commit(0), State: ledger.RunFinished, CreatedAt: tx.Now()}); err != nil {
			return err
		}
		for _, task := range sc.Tasks {
			if err := tx.PutTask(ledger.TaskRow{ID: task.ID, Member: "lead", Kind: task.Kind, State: ledger.TaskSucceeded}); err != nil {
				return err
			}
		}
		if err := tx.PutTask(ledger.TaskRow{ID: "t01", Member: "lead", Kind: crew.TaskWork, State: ledger.TaskSucceeded, Attempts: 8, ResultAttempt: 7, ResultCommit: commit(7), Withdrawn: 3}); err != nil {
			return err
		}
		for n := 1; n <= 8; n++ {
			a := ledger.AttemptRow{TaskID: "t01", N: n, Member: "lead", StartCommit: commit(n - 1), ResultCommit: commit(n), State: ledger.AttemptFailed, Stage: "check", Result: result(crew.Result{Commit: commit(n)})}
			if n == 8 {
				// Never submitted: no candidate.
				a.ResultCommit, a.Result, a.State, a.Stage = "", "", ledger.AttemptCancelled, ""
			}
			if err := tx.PutAttempt(a); err != nil {
				return err
			}
		}
		for _, c := range []ledger.CheckRow{
			{TaskID: "t01", N: 7, ID: "test", K: 1, Subject: commit(7), State: ledger.CheckFinished, Exit: 1, LogSHA256: strings.Repeat("a", 64)},
			{TaskID: "t01", N: 7, ID: "lint", K: 2, Phase: ledger.PhaseVerify, Subject: commit(7), State: ledger.CheckFinished, Exit: 0, Stable: true},
			{TaskID: "t01", N: 7, ID: "other", K: 3, Subject: commit(6), State: ledger.CheckFinished},
			{TaskID: "t01", N: 7, ID: "deps", K: 4, Phase: ledger.PhaseSetup, Subject: commit(7), State: ledger.CheckFinished},
		} {
			c.StartedAt = tx.Now()
			if err := tx.PutCheck(c); err != nil {
				return err
			}
		}
		if err := tx.PutAttempt(ledger.AttemptRow{TaskID: "rv", N: 1, Member: "lead", StartCommit: commit(7), State: ledger.AttemptSucceeded,
			Result: result(crew.Result{Verdict: crew.VerdictNeedsChanges, Findings: []crew.Finding{{Severity: "blocker", Message: "x"}, {Severity: "major", Message: "y"}, {Severity: "major", Message: "z"}, {Severity: "nit", Message: "w"}}})}); err != nil {
			return err
		}
		if _, _, err := tx.Command("admin", "acc-1", "accept", "fp"); err != nil {
			return err
		}
		return tx.Settle("admin", "acc-1", ledger.CommandDone, crew.Receipt{CommandID: "acc-1", Intent: "accept", Status: ledger.CommandDone,
			Task: &crew.ReceiptTask{ID: "t01", State: ledger.TaskSucceeded, Result: &crew.ReceiptResult{Attempt: 7, Commit: commit(7)}}})
	}); err != nil {
		t.Fatal(err)
	}
	page1 := candidatesOf(t, again, synth, "")
	if tcs := listOf(t, page1, "candidates"); len(tcs) != candidateTasksPage || page1["next"] != "t08" || tcs[0]["task"] != "t01" || tcs[7]["task"] != "t08" {
		t.Fatalf("page 1: %v", page1)
	}
	t01 := taskOn(t, page1, "t01")
	cs := candidateList(t01)
	if len(cs) != candidatesPerTask || t01["more"] != 1.0 || cs[0]["attempt"] != 7.0 || cs[5]["attempt"] != 2.0 {
		t.Fatalf("t01: %v", t01)
	}
	for _, c := range cs {
		n := c["attempt"].(float64)
		if c["withdrawn"] != (n <= 3) || c["standing"] != (n == 7) || (c["acceptedAnyway"] != nil) != (n == 7) || c["commit"] != commit(int(n)) {
			t.Fatalf("t01's attempt %v: %v", n, c)
		}
	}
	if acc := cs[0]["acceptedAnyway"].(map[string]any); acc["actor"] != "admin" || acc["command"] != "acc-1" {
		t.Fatalf("the Accept anyway: %v", acc)
	}
	var ids []string
	for _, r := range cs[0]["receipts"].([]any) {
		ids = append(ids, r.(map[string]any)["id"].(string))
	}
	if !slices.Equal(ids, []string{"lint", "test"}) {
		t.Fatalf("attempt 7's receipts: %v", cs[0]["receipts"])
	}
	page2 := candidatesOf(t, again, synth, "after=t08")
	if tcs := listOf(t, page2, "candidates"); len(tcs) != 3 || page2["next"] != nil || tcs[2]["task"] != "rv" {
		t.Fatalf("page 2: %v", page2)
	}
	rvc := candidateList(taskOn(t, page2, "rv"))
	if len(rvc) != 1 || rvc[0]["verdict"] != crew.VerdictNeedsChanges || rvc[0]["start"] != commit(7) ||
		!reflect.DeepEqual(rvc[0]["findings"], map[string]any{"blocker": 1.0, "major": 2.0, "nit": 1.0}) {
		t.Fatalf("rv: %v", rvc)
	}
	for _, q := range []string{"after=nope", "after=-x", "after=t01&after=t02", "bogus=1"} {
		again.expect(t, "GET", "/api/runs/"+synth+"/candidates?"+q, nil, http.StatusBadRequest, "invalid_request")
	}
}

// The largest page of a run's candidates: eight tasks with six candidates
// each, sixteen receipts each whose errors are cut to 120 characters, every
// text at its bound and escaped worst, encoded as the route encodes it, is
// under MaxCandidatesReply.
func TestTheCandidatesPageAtItsWorstIsUnderItsBound(t *testing.T) {
	worst := func(n int) string { return strings.Repeat(string(rune(0x2028)), n) }
	at := time.Now()
	row := ledger.CheckRow{Phase: ledger.PhaseVerify, ID: strings.Repeat("i", 40), K: 1 << 20, State: ledger.CheckInterrupted, Exit: -1 << 20, Signal: worst(16),
		TimedOut: true, Killed: true, Stable: true, Error: worst(8000), StartedAt: at, FinishedAt: at, LogSHA256: strings.Repeat("f", 64), LogBytes: ledger.MaxLog, LogDropped: true}
	receipt := candidateReceiptOf(row)
	if len([]rune(receipt.Error)) != maxReceiptError+1 {
		t.Fatalf("the error is not cut: %d characters", len([]rune(receipt.Error)))
	}
	receipts := make([]candidateReceipt, maxCandidateReceipts)
	for i := range receipts {
		receipts[i] = receipt
	}
	c := runCandidate{Task: strings.Repeat("t", 40), Kind: crew.TaskReview, Attempt: 1 << 20, Commit: strings.Repeat("c", 64), Start: strings.Repeat("s", 64),
		State: ledger.AttemptInterrupted, Stage: worst(16), Standing: true, Withdrawn: true,
		AcceptedAnyway: &candidateAccepted{Actor: worst(dispatch.MaxActor), At: at, Command: strings.Repeat("~", 32)}, Verdict: crew.VerdictNeedsChanges,
		Findings: map[string]int{"blocker": 50, "major": 50, "minor": 50, "nit": 50}, Receipts: receipts}
	page := make([]taskCandidates, candidateTasksPage)
	for i := range page {
		page[i] = taskCandidates{Task: strings.Repeat("t", 40), Kind: crew.TaskReview, More: 1 << 20}
		for range candidatesPerTask {
			page[i].Candidates = append(page[i].Candidates, c)
		}
	}
	rec := httptest.NewRecorder()
	writeDispatch(rec, http.StatusOK, map[string]any{"run": strings.Repeat("c", 40) + "-0000aaaa", "managed": true, "base": strings.Repeat("b", 64), "candidates": page, "next": strings.Repeat("t", 40)})
	if n := rec.Body.Len(); n > MaxCandidatesReply {
		t.Fatalf("the largest page of candidates is %d bytes, its bound %d", n, MaxCandidatesReply)
	}
}

// A legacy run, a real crew launch without tasks the ledger has not:
// {managed: false, candidates: []}, from the engine and, after a restart,
// from its record; an id found nowhere is 404, one not shaped as one 400.
func TestCandidatesOfALegacyRun(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	legacy, _ := legacyRun(t, l.testEnv, l.repo)
	want := map[string]any{"run": legacy, "managed": false, "base": "", "candidates": []any{}}
	if got := candidatesOf(t, l.testEnv, legacy, ""); !reflect.DeepEqual(got, want) {
		t.Fatalf("the legacy run: %v", got)
	}
	again := l.restartAfterStop(t, legacy)
	waitUntil(t, "the legacy run's record", func() bool { _, ok, _ := again.srv.records.Get(legacy); return ok })
	if got := candidatesOf(t, again, legacy, ""); !reflect.DeepEqual(got, want) {
		t.Fatalf("the legacy run from its record: %v", got)
	}
	again.expect(t, "GET", "/api/runs/nobody-00000000/candidates", nil, http.StatusNotFound, "not_found")
	again.expect(t, "GET", "/api/runs/nobody/candidates", nil, http.StatusBadRequest, "invalid_request")
	if resp, _ := again.do("GET", "/api/runs/"+legacy+"/candidates", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without a token: %d", resp.StatusCode)
	}
	// The ledger closed: the legacy run, which needs none, answers the
	// same; a run found nowhere may be the ledger's, so it is 503.
	if err := again.srv.ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if got := candidatesOf(t, again, legacy, ""); !reflect.DeepEqual(got, want) {
		t.Fatalf("the legacy run with the ledger closed: %v", got)
	}
	again.expect(t, "GET", "/api/runs/nobody-00000000/candidates", nil, http.StatusServiceUnavailable, "ledger_unavailable")
}

// A page's rows are of one revision: a transition committed between its
// reads of the tasks and of the attempts (through candidatesSeam: attempt
// 1 succeeded and made the task's result) is read again, so the attempt
// is answered succeeded and standing, never succeeded and not standing.
func TestCandidatesAreReadAtOneRevision(t *testing.T) {
	e := readyEnv(t, nil)
	run := "torn-0000abcd"
	c, _ := json.Marshal(crew.Crew{ID: "torn", Name: "Torn", Tasks: []crew.Task{{ID: "build", Member: "lead", Kind: crew.TaskWork}}})
	sub := strings.Repeat("a", 40)
	attempt := ledger.AttemptRow{TaskID: "build", N: 1, ResultCommit: sub, State: ledger.AttemptRunning, Stage: "check", Result: `{"commit":"` + sub + `"}`}
	if _, err := e.srv.ledger.Transition(t.Context(), run, func(tx *ledger.Tx) error {
		if err := tx.CreateRun(ledger.RunRow{ID: run, CrewID: "torn", Crew: string(c), State: ledger.RunRunning, CreatedAt: tx.Now()}); err != nil {
			return err
		}
		if err := tx.PutTask(ledger.TaskRow{ID: "build", Member: "lead", Kind: crew.TaskWork, State: ledger.TaskVerifying, Attempts: 1}); err != nil {
			return err
		}
		return tx.PutAttempt(attempt)
	}); err != nil {
		t.Fatal(err)
	}
	var calls int
	seam := func(id string) {
		if id != run {
			return
		}
		calls++
		if calls > 1 {
			return
		}
		if _, err := e.srv.ledger.Transition(context.Background(), run, func(tx *ledger.Tx) error {
			if err := tx.PutTask(ledger.TaskRow{ID: "build", Member: "lead", Kind: crew.TaskWork, State: ledger.TaskSucceeded, Attempts: 1, ResultAttempt: 1, ResultCommit: sub}); err != nil {
				return err
			}
			a := attempt
			a.State, a.Stage = ledger.AttemptSucceeded, ""
			return tx.PutAttempt(a)
		}); err != nil {
			t.Error(err)
		}
	}
	candidatesSeam.Store(&seam)
	t.Cleanup(func() { candidatesSeam.Store(nil) })
	cs := candidateList(taskOn(t, candidatesOf(t, e, run, ""), "build"))
	candidatesSeam.Store(nil)
	if len(cs) != 1 || cs[0]["state"] != ledger.AttemptSucceeded || cs[0]["standing"] != true || calls != 2 {
		t.Fatalf("the candidate across a transition (%d reads): %v", calls, cs)
	}
}

// A run that moves through every read of a page is never answered with
// rows of two revisions: a transition committed between the tasks' and
// the attempts' reads on each of the candidateReadTries passes (the last
// among them) is 503 run_changing with Retry-After, and one that stops
// before the last pass answers that pass's rows, of one revision.
func TestCandidatesMovingThroughEveryReadAreRefused(t *testing.T) {
	e := readyEnv(t, nil)
	run := "churn-0000abcd"
	c, _ := json.Marshal(crew.Crew{ID: "churn", Name: "Churn", Tasks: []crew.Task{{ID: "build", Member: "lead", Kind: crew.TaskWork}}})
	sub := strings.Repeat("a", 40)
	attempt := ledger.AttemptRow{TaskID: "build", N: 1, ResultCommit: sub, State: ledger.AttemptRunning, Stage: "check", Result: `{"commit":"` + sub + `"}`}
	verifying := ledger.TaskRow{ID: "build", Member: "lead", Kind: crew.TaskWork, State: ledger.TaskVerifying, Attempts: 1}
	if _, err := e.srv.ledger.Transition(t.Context(), run, func(tx *ledger.Tx) error {
		if err := tx.CreateRun(ledger.RunRow{ID: run, CrewID: "churn", Crew: string(c), State: ledger.RunRunning, CreatedAt: tx.Now()}); err != nil {
			return err
		}
		if err := tx.PutTask(verifying); err != nil {
			return err
		}
		return tx.PutAttempt(attempt)
	}); err != nil {
		t.Fatal(err)
	}
	// Each pass the seam moves, the attempt succeeds and becomes the
	// task's result, or both go back: a pass that read the tasks before
	// and the attempts after would pair succeeded with not standing.
	var calls, moves int
	seam := func(id string) {
		if id != run {
			return
		}
		calls++
		if calls > moves {
			return
		}
		succeeded := calls%2 == 1
		if _, err := e.srv.ledger.Transition(context.Background(), run, func(tx *ledger.Tx) error {
			tr, a := verifying, attempt
			if succeeded {
				tr.State, tr.ResultAttempt, tr.ResultCommit = ledger.TaskSucceeded, 1, sub
				a.State, a.Stage = ledger.AttemptSucceeded, ""
			}
			if err := tx.PutTask(tr); err != nil {
				return err
			}
			return tx.PutAttempt(a)
		}); err != nil {
			t.Error(err)
		}
	}
	candidatesSeam.Store(&seam)
	t.Cleanup(func() { candidatesSeam.Store(nil) })

	moves = candidateReadTries
	resp, out := e.do("GET", "/api/runs/"+run+"/candidates", adminToken, nil)
	if resp.StatusCode != http.StatusServiceUnavailable || errorCode(out) != "run_changing" || resp.Header.Get("Retry-After") == "" || calls != candidateReadTries {
		t.Fatalf("a run moving through all %d reads: %d %v, Retry-After %q, %d reads", candidateReadTries, resp.StatusCode, out, resp.Header.Get("Retry-After"), calls)
	}

	// Moving through every pass but the last: the last pass's rows, of
	// the revision the seventh move made (the attempt succeeded and its
	// task's result).
	calls, moves = 0, candidateReadTries-1
	cs := candidateList(taskOn(t, candidatesOf(t, e, run, ""), "build"))
	if calls != candidateReadTries || len(cs) != 1 || cs[0]["state"] != ledger.AttemptSucceeded || cs[0]["standing"] != true {
		t.Fatalf("a run settling on the last read (%d reads): %v", calls, cs)
	}
}

// Every receipt carries where it ran: native, the only runtime until one
// other exists, for a check's and a verify step's alike.
func TestCandidatesCarryWhere(t *testing.T) {
	e := readyEnv(t, nil)
	if e.srv.ledger == nil {
		t.Fatal("no ledger")
	}
	run := "where-0000abcd"
	c, _ := json.Marshal(crew.Crew{ID: "where", Name: "Where", Tasks: []crew.Task{{ID: "build", Member: "lead", Kind: crew.TaskWork}}})
	sub := strings.Repeat("a", 40)
	if _, err := e.srv.ledger.Transition(t.Context(), run, func(tx *ledger.Tx) error {
		if err := tx.CreateRun(ledger.RunRow{ID: run, CrewID: "where", Crew: string(c), State: ledger.RunFinished, CreatedAt: tx.Now()}); err != nil {
			return err
		}
		if err := tx.PutTask(ledger.TaskRow{ID: "build", Member: "lead", Kind: crew.TaskWork, State: ledger.TaskSucceeded, ResultAttempt: 1, ResultCommit: sub}); err != nil {
			return err
		}
		if err := tx.PutAttempt(ledger.AttemptRow{TaskID: "build", N: 1, ResultCommit: sub, State: ledger.AttemptSucceeded, Result: `{"commit":"` + sub + `"}`}); err != nil {
			return err
		}
		for k, phase := range []string{ledger.PhaseCheck, ledger.PhaseVerify} {
			if err := tx.PutCheck(ledger.CheckRow{TaskID: "build", N: 1, ID: phase + "s", K: k + 1, Phase: phase, Subject: sub, State: ledger.CheckFinished, StartedAt: tx.Now()}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cs := candidateList(taskOn(t, candidatesOf(t, e, run, ""), "build"))
	if len(cs) != 1 || len(cs[0]["receipts"].([]any)) != 2 {
		t.Fatalf("the candidates: %v", cs)
	}
	for _, r := range cs[0]["receipts"].([]any) {
		if rc := r.(map[string]any); rc["where"] != "native" {
			t.Fatalf("a receipt's where: %v", rc)
		}
	}
}

// The attempt's ticket follows its package: the session reads the ticket
// its run was launched from with the launch (its id, the ticket's revision
// then, the plan revision, the digest, what changed since), comments on
// it, and so does a continuation's session after a restart; the ticket
// deleted is 404 no_ticket saying the run follows its package, for the
// read and the comment; a run linked by hand is still found by its link.
func TestSessionTicketFollowsThePackage(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	launch := entityOf(t, l.launch(t, l.testEnv, nil, http.StatusCreated, ""), "launch")
	launchID, runID := launch["id"].(string), launch["runId"].(string)
	sid := runningAttempt(t, l.testEnv, runID, "build", 1)["sessionId"].(string)
	read := func(e *testEnv, sid string, status int, code string) map[string]any {
		t.Helper()
		resp, out := e.do("GET", "/api/sessions/"+sid+"/ticket", e.agentToken(sid), nil)
		if resp.StatusCode != status || code != "" && errorCode(out) != code {
			t.Fatalf("the session's ticket: %d %v, want %d %s", resp.StatusCode, out, status, code)
		}
		return out
	}
	out := read(l.testEnv, sid, http.StatusOK, "")
	lp := out["launch"].(map[string]any)
	if entityOf(t, out, "ticket")["key"] != "CND-1" || lp["id"] != launchID || lp["rev"] != launch["ticketRev"] || lp["digest"] != launch["packageSha256"] ||
		!reflect.DeepEqual(lp["plan"], map[string]any{"id": l.plan, "n": 1.0}) || !reflect.DeepEqual(lp["changed"], map[string]any{"fields": []any{}, "comments": 0.0, "rev": launch["ticketRev"]}) {
		t.Fatalf("the launch part: %v", lp)
	}
	l.expect(t, "POST", "/api/dispatch/tickets/CND-1/edit", map[string]any{"opId": opID(), "set": map[string]any{"description": "It loops forever."}}, http.StatusOK, "")
	if resp, out := l.do("POST", "/api/sessions/"+sid+"/ticket/comments", l.agentToken(sid), map[string]any{"opId": opID(), "body": "Seen."}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("the session's comment: %d %v", resp.StatusCode, out)
	}
	changed := read(l.testEnv, sid, http.StatusOK, "")["launch"].(map[string]any)["changed"].(map[string]any)
	if !reflect.DeepEqual(changed["fields"], []any{"description"}) || changed["comments"] != 1.0 {
		t.Fatalf("changed since: %v", changed)
	}

	// A continuation's session, after a restart.
	again := l.restartAfterStop(t, runID)
	next := again.expect(t, "POST", "/api/runs/"+runID+"/resume", nil, http.StatusCreated, "")["run"].(map[string]any)["id"].(string)
	if r := runOf(t, again, next); r["paused"] == true {
		command(t, again, next, "unpause")
	}
	csid := runningAttempt(t, again, next, "build", 0)["sessionId"].(string)
	if lp := read(again, csid, http.StatusOK, "")["launch"].(map[string]any); lp["id"] != launchID {
		t.Fatalf("the continuation's launch part: %v", lp)
	}
	if resp, out := again.do("POST", "/api/sessions/"+csid+"/ticket/comments", again.agentToken(csid), map[string]any{"opId": opID(), "body": "Still here."}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("the continuation's comment: %d %v", resp.StatusCode, out)
	}

	// A run linked by hand is found by its link, with no launch part.
	again.newDispatchTicket(t, "CND", "Linked", nil)
	legacy, lsid := legacyRun(t, again, l.repo)
	linkRun(t, again, "CND-2", legacy, http.StatusCreated, "")
	if out := again.expect(t, "GET", "/api/sessions/"+lsid+"/ticket", nil, http.StatusOK, ""); entityOf(t, out, "ticket")["key"] != "CND-2" || out["launch"] != nil {
		t.Fatalf("the linked run's ticket: %v", out)
	}

	// The ticket deleted: the run follows its package.
	again.expect(t, "POST", "/api/dispatch/tickets/CND-1/delete", map[string]any{"opId": opID()}, http.StatusOK, "")
	if msg := apiMessage(read(again, csid, http.StatusNotFound, "no_ticket")); !strings.Contains(msg, "follows its package") {
		t.Fatalf("the deleted ticket's words: %s", msg)
	}
	resp, cout := again.do("POST", "/api/sessions/"+csid+"/ticket/comments", again.agentToken(csid), map[string]any{"opId": opID(), "body": "Anyone?"})
	if code, msg := apiErr(cout); resp.StatusCode != http.StatusNotFound || code != "no_ticket" || !strings.Contains(msg, "follows its package") {
		t.Fatalf("a comment on the deleted ticket: %d %v", resp.StatusCode, cout)
	}
}
