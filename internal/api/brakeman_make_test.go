package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// Drafts made work by a person (docs/plans/brakeman.md, Drafts made work;
// PR-4): the make route, its claim, its derived op ids, the edges of
// draft tickets' blocks, the crew's origin, and the recovery of a make a
// server left pending. The brief is kept through Brakeman's store, as a
// turn leaves one; the crashes are the make's seams, never a sleep.

// draftTicket is a draft_ticket/1 item of the project CND, blocking the
// items blocks names.
func draftTicket(title string, blocks ...int) brakeman.Item {
	return brakeman.Item{Kind: brakeman.ItemDraftTicket, DraftTicket: &brakeman.DraftTicket{Project: "CND", Title: title, Description: "from the discussion", Blocks: blocks}}
}

// draftCrew is a draft_crew/1 item: a crew of one cat in the server's
// root, without a worktree.
func (f *obsFixture) draftCrew(name string) brakeman.Item {
	body, _ := json.Marshal(map[string]any{"name": name, "goal": "fix the build", "cwd": f.root, "where": "server", "isolation": "none",
		"members": []any{map[string]any{"name": "lead", "agentId": "cat", "start": map[string]any{"when": "immediately"}}}})
	return brakeman.Item{Kind: brakeman.ItemDraftCrew, DraftCrew: &brakeman.DraftCrew{Crew: body}}
}

// drafts is a briefed request whose brief is items.
func (f *obsFixture) drafts(t *testing.T, items ...brakeman.Item) string {
	t.Helper()
	return f.keepEarlier(t, brakeman.DefaultConfig(f.root), items, "Drafts.", nil)
}

// make posts a make of the request's item i.
func (f *obsFixture) make(t *testing.T, req string, i int, opID string, edit any) (int, map[string]any) {
	t.Helper()
	body := map[string]any{"opId": opID, "by": "Ann"}
	if edit != nil {
		body["edit"] = edit
	}
	status, _, out := f.call(t, "POST", fmt.Sprintf("/api/brakeman/requests/%s/items/%d/make", req, i), body)
	return status, out
}

// made is the request's made entry of item i.
func (f *obsFixture) made(t *testing.T, req string, i int) brakeman.Made {
	t.Helper()
	r, err := f.desk.st.Get(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range r.Made {
		if m.Item == i {
			return m
		}
	}
	return brakeman.Made{}
}

// tickets is how many live tickets CND holds.
func (f *obsFixture) tickets(t *testing.T) int {
	t.Helper()
	page, err := f.srv.dispatch.List(t.Context(), dispatch.Query{Project: "CND", Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	return len(page.Tickets)
}

// atSeam sets the make's seam to fn for the test.
func atSeam(t *testing.T, fn func(stage, request string, item int) error) {
	t.Helper()
	makeSeam.Store(&fn)
	t.Cleanup(func() { makeSeam.Store(nil) })
}

// crashAt makes the make stop at stage once, as a server that died there.
func crashAt(t *testing.T, stage string) {
	t.Helper()
	var once sync.Once
	atSeam(t, func(s, _ string, _ int) error {
		var err error
		if s == stage {
			once.Do(func() { err = errors.New("the server stopped at " + stage) })
		}
		return err
	})
}

// recoverNow runs the desk's recovery, as a start after a crash does.
func (f *obsFixture) recoverNow(t *testing.T) {
	t.Helper()
	makeSeam.Store(nil)
	f.desk.recoverMakes(t.Context())
	if p, err := f.desk.st.Pending(); err != nil || len(p) != 0 {
		t.Fatalf("pending after the recovery: %+v %v", p, err)
	}
}

// pruneUnder makes 500 newer closed requests and prunes: the request
// before them is kept only if it is pinned.
func (f *obsFixture) pruneUnder(t *testing.T) {
	t.Helper()
	st := f.desk.st
	for i := range brakeman.Keep {
		r, _, err := st.Create(brakeman.Request{OpID: fmt.Sprintf("filler-%d-%d", i, bkOps.Add(1)), Kind: brakeman.KindBriefing})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.Update(r.ID, func(q *brakeman.Request) error { return q.To(brakeman.StateCancelled, "filler", time.Now()) }); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Prune(); err != nil {
		t.Fatal(err)
	}
}

// A draft ticket is made once: the same opId again answers it, another
// is already made, and a crash between Dispatch's commit and made's write
// is completed by the same opId without a second ticket.
func TestADraftTicketIsMadeOnce(t *testing.T) {
	f := newObsFixture(t)
	req := f.drafts(t, draftTicket("Fix the flaky test"))
	before := f.tickets(t)
	crashAt(t, "committed")
	if status, out := f.make(t, req, 0, "make-1", nil); status != http.StatusInternalServerError {
		t.Fatalf("the crash: %d %v", status, out)
	}
	if m := f.made(t, req, 0); m.State != brakeman.MadePending {
		t.Fatalf("the claim after the crash: %+v", m)
	}
	status, out := f.make(t, req, 0, "make-1", nil)
	if status != http.StatusOK || entityOf(t, out, "made")["state"] != brakeman.MadeDone {
		t.Fatalf("the same opId again: %d %v", status, out)
	}
	key := entityOf(t, out, "ticket")["key"]
	if again, out := f.make(t, req, 0, "make-1", nil); again != http.StatusOK || entityOf(t, out, "ticket")["key"] != key {
		t.Fatalf("once more: %d %v", again, out)
	}
	if other, out := f.make(t, req, 0, "make-2", nil); other != http.StatusConflict || errorCode(out) != "already_made" {
		t.Fatalf("another opId: %d %v", other, out)
	}
	if n := f.tickets(t); n != before+1 {
		t.Fatalf("%d tickets made", n-before)
	}
	tk, err := f.srv.dispatch.Ticket(t.Context(), key.(string))
	if err != nil || tk.CreatedBy != "Ann" || tk.Title != "Fix the flaky test" {
		t.Fatalf("the ticket: %+v %v", tk.Ticket, err)
	}
	// Any other item is no draft.
	req2 := f.drafts(t, fact("a fact", ref(t, "run:"+f.run)))
	if status, out := f.make(t, req2, 0, "make-3", nil); status != http.StatusBadRequest || errorCode(out) != "not_a_draft" {
		t.Fatalf("a fact: %d %v", status, out)
	}
}

// A draft crew is saved, opened by its id, never launched.
func TestADraftCrewIsSavedNotLaunched(t *testing.T) {
	f := newObsFixture(t)
	req := f.drafts(t, f.draftCrew("Fixers"))
	runs := len(f.srv.runs.List())
	status, out := f.make(t, req, 0, "crew-1", nil)
	if status != http.StatusCreated {
		t.Fatalf("the make: %d %v", status, out)
	}
	id := entityOf(t, out, "crew")["id"].(string)
	c, err := f.srv.crews.Get(id)
	if err != nil || c.Name != "Fixers" || c.Origin != "brakeman:"+req+"/0/"+makeOp(req, 0, "crew-1") {
		t.Fatalf("the crew saved: %+v %v", c, err)
	}
	if n := len(f.srv.runs.List()); n != runs {
		t.Fatalf("%d runs launched", n-runs)
	}
}

// A draft crew saved and the server stopped before made's write is found
// by its origin: one crew, made.
func TestADraftCrewIsFoundByItsOrigin(t *testing.T) {
	f := newObsFixture(t)
	req := f.drafts(t, f.draftCrew("Fixers"))
	crashAt(t, "committed")
	if status, _ := f.make(t, req, 0, "crew-1", nil); status != http.StatusInternalServerError {
		t.Fatalf("the crash: %d", status)
	}
	f.recoverNow(t)
	m := f.made(t, req, 0)
	if m.State != brakeman.MadeDone || m.Crew == "" {
		t.Fatalf("recovered: %+v", m)
	}
	if _, total, _ := f.srv.crews.List(0, 100); total != 1 {
		t.Fatalf("%d crews", total)
	}
}

// Brakeman's own token makes nothing: the make route is the admin's.
func TestBrakemansTokenMakesNothing(t *testing.T) {
	f := newObsFixture(t)
	req := f.drafts(t, draftTicket("A draft"))
	f.turn(t, nil)
	before := f.tickets(t)
	for _, h := range []map[string]string{{brakemanSessionHeader: f.sid}, nil} {
		r, _ := http.NewRequest("POST", f.http.URL+"/api/brakeman/requests/"+req+"/items/0/make", strings.NewReader(`{"opId":"x"}`))
		r.Header.Set("Authorization", "Bearer "+f.tok)
		for k, v := range h {
			r.Header.Set(k, v)
		}
		resp, err := f.client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("the turn's token: %d", resp.StatusCode)
		}
	}
	if f.tickets(t) != before || f.made(t, req, 0).State != "" {
		t.Fatal("the turn's token made something")
	}
}

// One make of an item at a time: while the first is under way another
// opId is make_pending, the first opId with other content op_conflict,
// the first with the same content rejoins; one ticket, and one crew.
func TestConcurrentMakesOfOneItemMakeOne(t *testing.T) {
	f := newObsFixture(t)
	for _, c := range []struct {
		name string
		item brakeman.Item
		edit any
	}{
		{"ticket", draftTicket("Once"), map[string]any{"project": "CND", "title": "Other content"}},
		{"crew", f.draftCrew("Once"), map[string]any{"crew": f.draftCrew("Other").DraftCrew.Crew}},
	} {
		req := f.drafts(t, c.item)
		held, release := make(chan struct{}), make(chan struct{})
		var once, freed sync.Once
		free := func() { freed.Do(func() { close(release) }) }
		t.Cleanup(free)
		atSeam(t, func(stage, r string, _ int) error {
			if stage == "claimed" && r == req {
				first := false
				once.Do(func() { first = true })
				if first {
					close(held)
					<-release
				}
			}
			return nil
		})
		done := make(chan int, 1)
		go func() {
			status, _ := f.make(t, req, 0, "first", nil)
			done <- status
		}()
		<-held
		if status, out := f.make(t, req, 0, "second", nil); status != http.StatusConflict || errorCode(out) != "make_pending" {
			t.Fatalf("%s: another opId: %d %v", c.name, status, out)
		}
		input, _ := f.desk.st.MakeInput(req, 0)
		if status, out := f.make(t, req, 0, "first", c.edit); status != http.StatusConflict || errorCode(out) != "op_conflict" {
			t.Fatalf("%s: other content: %d %v", c.name, status, out)
		}
		if after, _ := f.desk.st.MakeInput(req, 0); string(after) != string(input) {
			t.Fatalf("%s: the input was rewritten", c.name)
		}
		if status, out := f.make(t, req, 0, "first", nil); status != http.StatusOK || entityOf(t, out, "made")["state"] != brakeman.MadeDone {
			t.Fatalf("%s: the same content rejoins: %d %v", c.name, status, out)
		}
		free()
		if status := <-done; status != http.StatusCreated && status != http.StatusOK {
			t.Fatalf("%s: the first: %d", c.name, status)
		}
		makeSeam.Store(nil)
	}
	if n := f.tickets(t); n != 2 { // the fixture's CND-1 and the one made
		t.Fatalf("%d tickets", n)
	}
	if _, total, _ := f.srv.crews.List(0, 100); total != 1 {
		t.Fatalf("%d crews", total)
	}
}

// A request with a make pending outlives a prune past 500 newer ones, and
// its make is recorded once the backend commits: nothing is left pending.
func TestPruneDuringAPendingMakeKeepsItsRecords(t *testing.T) {
	f := newObsFixture(t)
	req := f.drafts(t, draftTicket("Kept"))
	held, release := make(chan struct{}), make(chan struct{})
	var freed sync.Once
	free := func() { freed.Do(func() { close(release) }) }
	t.Cleanup(free)
	atSeam(t, func(stage, _ string, _ int) error {
		if stage == "claimed" {
			close(held)
			<-release
		}
		return nil
	})
	done := make(chan int, 1)
	go func() {
		status, _ := f.make(t, req, 0, "kept", nil)
		done <- status
	}()
	<-held
	f.pruneUnder(t)
	if _, err := f.desk.st.Get(req); err != nil {
		t.Fatalf("the pinned request was pruned: %v", err)
	}
	free()
	if status := <-done; status != http.StatusCreated {
		t.Fatalf("the make: %d", status)
	}
	if m := f.made(t, req, 0); m.State != brakeman.MadeDone || m.Key == "" {
		t.Fatalf("made: %+v", m)
	}
	f.recoverNow(t)
}

// A request pruned before its make claims it calls no backend: 404, no
// ticket.
func TestAMakeOfAPrunedRequestCallsNoBackend(t *testing.T) {
	f := newObsFixture(t)
	req := f.drafts(t, draftTicket("Gone"))
	before := f.tickets(t)
	atSeam(t, func(stage, _ string, _ int) error {
		if stage == "input" {
			f.pruneUnder(t)
		}
		return nil
	})
	if status, out := f.make(t, req, 0, "gone", nil); status != http.StatusNotFound {
		t.Fatalf("a pruned request: %d %v", status, out)
	}
	if f.tickets(t) != before {
		t.Fatal("a ticket was made")
	}
	if _, err := os.Stat(filepath.Join(f.dataDir(t), brakeman.Dir, brakeman.RequestsDir, req+"-make-0.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("its input is left: %v", err)
	}
}

// A duplicate of a crew made from a draft carries no origin: deleting it
// leaves no tombstone, and the recovery finds the original by its origin.
func TestADuplicateOfAMadeCrewCarriesNoOrigin(t *testing.T) {
	f := newObsFixture(t)
	req := f.drafts(t, f.draftCrew("Fixers"))
	crashAt(t, "committed")
	if status, _ := f.make(t, req, 0, "crew-1", nil); status != http.StatusInternalServerError {
		t.Fatalf("the crash: %d", status)
	}
	page, _, _ := f.srv.crews.List(0, 10)
	if len(page) != 1 {
		t.Fatalf("crews: %v", page)
	}
	orig := page[0].ID
	dup := entityOf(t, f.expect(t, "POST", "/api/crews/"+orig+"/duplicate", nil, http.StatusCreated, ""), "crew")
	if dup["origin"] != nil {
		t.Fatalf("the duplicate's origin: %v", dup["origin"])
	}
	f.expect(t, "DELETE", "/api/crews/"+dup["id"].(string), nil, http.StatusNoContent, "")
	if tombs, _ := os.ReadDir(filepath.Join(f.dataDir(t), "crew-origins")); len(tombs) != 0 {
		t.Fatalf("a tombstone for the duplicate: %v", tombs)
	}
	f.recoverNow(t)
	if m := f.made(t, req, 0); m.State != brakeman.MadeDone || m.Crew != orig {
		t.Fatalf("recovered: %+v, want %s", m, orig)
	}
}

// dataDir is the server's data directory.
func (f *obsFixture) dataDir(t *testing.T) string {
	t.Helper()
	return f.srv.store.Dir()
}

// A crew's create takes no origin from its body: POST /api/crews refuses
// the field, and the store clears one a caller sets.
func TestCreateClearsAnOriginInItsBody(t *testing.T) {
	f := newObsFixture(t)
	body := map[string]any{"name": "Planted", "goal": "g", "cwd": f.root, "where": "server", "isolation": "none", "origin": "brakeman:br-1/0/bkx",
		"members": []any{map[string]any{"name": "lead", "agentId": "cat", "start": map[string]any{"when": "immediately"}}}}
	f.expect(t, "POST", "/api/crews", body, http.StatusBadRequest, "invalid_request")
	planted := crew.Crew{Name: "Planted", Goal: "g", Cwd: f.root, Where: crew.WhereServer, Isolation: crew.IsolationNone, Origin: "brakeman:br-1/0/bkx",
		Members: []crew.Member{{Name: "lead", AgentID: "cat", Start: crew.Start{When: crew.StartImmediately}}}}
	saved, err := f.srv.crews.Create(planted)
	if err != nil || saved.Origin != "" {
		t.Fatalf("a create naming an origin: %+v %v", saved, err)
	}
}

// A draft crew a second server deleted after it was saved and before the
// make was recorded is marked deleted, never saved again.
func TestADraftCrewDeletedBeforeMadeIsNotRecreated(t *testing.T) {
	f := newObsFixture(t)
	req := f.drafts(t, f.draftCrew("Fixers"))
	crashAt(t, "committed")
	if status, _ := f.make(t, req, 0, "crew-1", nil); status != http.StatusInternalServerError {
		t.Fatalf("the crash: %d", status)
	}
	st, err := store.Open(f.dataDir(t))
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := crew.NewStore(st)
	if err != nil {
		t.Fatal(err)
	}
	page, _, _ := other.List(0, 10)
	if len(page) != 1 {
		t.Fatalf("crews: %v", page)
	}
	if ok, err := other.Delete(page[0].ID); err != nil || !ok {
		t.Fatalf("the other server's delete: %v %v", ok, err)
	}
	f.recoverNow(t)
	if m := f.made(t, req, 0); m.State != brakeman.MadeDeleted {
		t.Fatalf("recovered: %+v", m)
	}
	if _, total, _ := f.srv.crews.List(0, 10); total != 0 {
		t.Fatalf("%d crews saved again", total)
	}
}

// A ticket whose make stopped before Dispatch committed is made by the
// recovery from its recorded input, with the person's edits and name,
// once.
func TestAPendingTicketIsCompletedFromItsInput(t *testing.T) {
	f := newObsFixture(t)
	req := f.drafts(t, draftTicket("As drafted"))
	before := f.tickets(t)
	crashAt(t, "claimed")
	if status, _ := f.make(t, req, 0, "edit-1", map[string]any{"project": "CND", "title": "As edited", "priority": 2}); status != http.StatusInternalServerError {
		t.Fatalf("the crash: %d", status)
	}
	if f.tickets(t) != before {
		t.Fatal("a ticket before the recovery")
	}
	f.recoverNow(t)
	m := f.made(t, req, 0)
	if m.State != brakeman.MadeDone {
		t.Fatalf("recovered: %+v", m)
	}
	tk, err := f.srv.dispatch.Ticket(t.Context(), m.Key)
	if err != nil || tk.Title != "As edited" || tk.Priority != 2 || tk.CreatedBy != "Ann" {
		t.Fatalf("the ticket: %+v %v", tk.Ticket, err)
	}
	f.desk.recoverMakes(t.Context())
	if n := f.tickets(t); n != before+1 {
		t.Fatalf("%d tickets", n-before)
	}
}

// A pending ticket whose receipt Dispatch pruned is found by the change
// log's ticket for its op: made, and no second ticket.
func TestAPendingTicketWithAPrunedReceiptFindsItsKey(t *testing.T) {
	f := newObsFixture(t)
	req := f.drafts(t, draftTicket("Pruned receipt"))
	before := f.tickets(t)
	crashAt(t, "committed")
	if status, _ := f.make(t, req, 0, "pr-1", nil); status != http.StatusInternalServerError {
		t.Fatalf("the crash: %d", status)
	}
	db, err := sql.Open("sqlite", filepath.Join(f.srv.dispatch.Dir(), dispatch.File))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec("PRAGMA busy_timeout = 10000")
	res, err := db.Exec("DELETE FROM ops WHERE actor = ? AND id = ?", dispatch.ActorAdmin, makeOp(req, 0, "pr-1"))
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("%d receipts pruned", n)
	}
	f.recoverNow(t)
	if m := f.made(t, req, 0); m.State != brakeman.MadeDone || m.Key == "" {
		t.Fatalf("recovered: %+v", m)
	}
	if n := f.tickets(t); n != before+1 {
		t.Fatalf("%d tickets", n-before)
	}
}

// A make Dispatch refused is marked refused with its words, and the item
// can be made again with a new opId.
func TestARefusedPendingTicketCanBeMadeAgain(t *testing.T) {
	f := newObsFixture(t)
	draft := draftTicket("To a project not made yet")
	draft.DraftTicket.Project = "NEW"
	req := f.drafts(t, draft)
	status, out := f.make(t, req, 0, "r-1", nil)
	if status != http.StatusConflict || f.made(t, req, 0).State != brakeman.MadeRefused {
		t.Fatalf("refused: %d %v", status, out)
	}
	doneOf(t)(f.srv.dispatch.CreateProject(t.Context(), adminOp("Ann"), dispatch.NewProject{Key: "NEW", Name: "New"}))
	if status, out := f.make(t, req, 0, "r-2", nil); status != http.StatusCreated || f.made(t, req, 0).State != brakeman.MadeDone {
		t.Fatalf("made again: %d %v", status, out)
	}
}

// Draft tickets keep their blocks whichever is made first, and a crash
// between a create and its edge leaves the edge for the recovery.
func TestDraftTicketsKeepTheirBlocksInEitherOrder(t *testing.T) {
	f := newObsFixture(t)
	blocks := func(t *testing.T, req string) {
		t.Helper()
		a, b := f.made(t, req, 0), f.made(t, req, 1)
		rels, err := f.srv.dispatch.Relations(t.Context(), a.Key)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, r := range rels {
			if r.Kind == dispatch.RelationBlocks && r.Other.Key == b.Key && r.DeletedAt.IsZero() {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("%s blocks %s %d times: %+v", a.Key, b.Key, n, rels)
		}
	}
	for _, order := range [][]int{{0, 1}, {1, 0}} {
		req := f.drafts(t, draftTicket("A", 1), draftTicket("B"))
		for _, i := range order {
			if status, out := f.make(t, req, i, fmt.Sprintf("o-%d", i), nil); status != http.StatusCreated {
				t.Fatalf("make %d: %d %v", i, status, out)
			}
		}
		blocks(t, req)
		// Made again, nothing more.
		if status, _ := f.make(t, req, 0, "o-0", nil); status != http.StatusOK {
			t.Fatalf("again: %d", status)
		}
		blocks(t, req)
	}
	req := f.drafts(t, draftTicket("A", 1), draftTicket("B"))
	if status, _ := f.make(t, req, 0, "c-0", nil); status != http.StatusCreated {
		t.Fatal("the first")
	}
	crashAt(t, "edge")
	if status, _ := f.make(t, req, 1, "c-1", nil); status != http.StatusCreated {
		// The ticket is made; its edge is left for the recovery.
		t.Fatalf("the second: %d", status)
	}
	if p, _ := f.desk.st.Pending(); len(p) != 1 || p[0].Edge == nil {
		t.Fatalf("the edge pending: %+v", p)
	}
	f.recoverNow(t)
	blocks(t, req)
}
