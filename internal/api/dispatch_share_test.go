package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// A rekey and Brakeman's scope (docs/plans/sync-seams.md, OS-1b): a
// project's new key is carried into the scope that names its old one,
// through the configuration's own save, before the rows a workspace's
// project of the old key held back are received under it.

// asTurnOf sends a request as Brakeman's turn sid with its token,
// answering the status and the reply.
func (b *bkEnv) asTurnOf(t *testing.T, sid, tok, method, path string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, b.http.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set(brakemanSessionHeader, sid)
	resp, err := b.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	out := map[string]any{}
	_ = json.Unmarshal(buf.Bytes(), &out)
	return resp.StatusCode, out
}

// heldWorkspaceProject joins the server's store to a workspace and hands
// it a workspace's project of the key, with one ticket, made on another
// machine: held while a project of this machine has the key. It answers
// the workspace's ticket's key.
func heldWorkspaceProject(t *testing.T, own *extHost, key string) string {
	t.Helper()
	ctx := context.Background()
	if err := own.DispatchJoin(ctx, "b1", "w1", "dev-b", 2_000_001, 2_100_000); err != nil {
		t.Fatal(err)
	}
	other, err := dispatch.Open(filepath.Join(t.TempDir(), "dispatch"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Close() })
	if err := other.Join(ctx, "a1", "w1", "dev-a", 1_000_001, 1_100_000); err != nil {
		t.Fatal(err)
	}
	op := func(id string) dispatch.Op { return dispatch.Op{Actor: "admin", ID: id} }
	if rc, err := other.CreateProject(ctx, op("p"), dispatch.NewProject{Key: key, Name: "The workspace's"}); err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("%+v %v", rc, err)
	}
	if err := other.ShareForTest(ctx, key, "w1"); err != nil {
		t.Fatal(err)
	}
	rc, err := other.CreateTicket(ctx, op("t"), dispatch.NewTicket{Project: key, Title: "Theirs"})
	if err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("%+v %v", rc, err)
	}
	rows, _, err := other.HandedChanges(ctx, 0, dispatch.MaxChangesPage)
	if err != nil {
		t.Fatal(err)
	}
	var batch []ext.DispatchChange
	for _, c := range rows {
		batch = append(batch, toDispatchChange(c))
	}
	if _, err := own.DispatchApply(ctx, batch, nil, map[string]string{rows[0].EntityID: key}); err != nil {
		t.Fatalf("the workspace's rows: %v", err)
	}
	return rc.Key
}

// The person scopes Brakeman to their project CND; a workspace's project
// CND is held while theirs has the key; they give theirs the key OWN.
// Brakeman's scope names OWN from then on, and a turn reads the person's
// ticket, now OWN-1, and is refused the workspace's ticket received under
// CND: the grant follows the person's project and never admits the one
// received under the key it freed.
func TestARekeyCarriesTheNewKeyIntoBrakemansScope(t *testing.T) {
	f := newFakeSync()
	e := syncEnv(t, syncDataDir(t), f, bkAgents()...)
	own := &extHost{s: e.srv, name: f.Name(), ctx: context.Background()}
	b := bkEnvOf(t, e)
	e.newDispatchProject(t, "CND", nil)
	mine := e.newDispatchTicket(t, "CND", "Mine", nil)["key"].(string)
	theirs := heldWorkspaceProject(t, own, "CND")
	if p := entityOf(t, e.expect(t, "GET", "/api/dispatch/projects/CND", nil, http.StatusOK, ""), "project"); p["heldBy"] == nil {
		t.Fatalf("the workspace's project is not held: %v", p)
	}
	b.enable(t, "bk", func(c map[string]any) {
		c["visibility"].(map[string]any)["dispatch"] = true
		c["scope"] = map[string]any{"projects": []any{"CND"}}
	})
	// What Brakeman's scope names as each of the rekey's commits is made,
	// the held rows' among them: OWN at every one.
	var mu sync.Mutex
	var during [][]string
	watching := true
	e.srv.dispatch.OnChange(func([]dispatch.Change) {
		c, err := b.desk.st.Config()
		mu.Lock()
		defer mu.Unlock()
		if watching && err == nil {
			during = append(during, slices.Clone(c.Scope.Projects))
		}
	})
	e.expect(t, "POST", "/api/dispatch/projects/CND/rekey", map[string]any{"opId": opID(), "key": "OWN"}, http.StatusOK, "")
	mu.Lock()
	watching = false
	seen := slices.Clone(during)
	mu.Unlock()
	if len(seen) < 2 || slices.ContainsFunc(seen, func(p []string) bool { return !slices.Equal(p, []string{"OWN"}) }) {
		t.Fatalf("Brakeman's scope as the rekey's commits were made (the rekey's, then the held rows'): %v", seen)
	}
	scope, _ := b.config(t)["scope"].(map[string]any)
	if got := scope["projects"]; !slices.Equal(anyStrings(got), []string{"OWN"}) {
		t.Fatalf("Brakeman's scope after the rekey: %v", got)
	}
	// The workspace's ticket is received under the freed key.
	e.expect(t, "GET", "/api/dispatch/tickets/"+theirs, nil, http.StatusOK, "")
	id := b.briefing(t)
	sid, tok, _ := b.turnSession(t, id)
	if status, out := b.asTurnOf(t, sid, tok, http.MethodGet, "/api/dispatch/tickets/OWN-1"); status != http.StatusOK {
		t.Fatalf("the person's ticket (was %s): %d %v", mine, status, out)
	}
	if status, out := b.asTurnOf(t, sid, tok, http.MethodGet, "/api/dispatch/tickets/"+theirs); status != http.StatusForbidden || codeOf(out) != "outside_scope" {
		t.Fatalf("the workspace's ticket %s: %d %v", theirs, status, out)
	}
}

// A rekey the store refuses gives Brakeman's scope back what it named;
// one Brakeman's scope cannot be saved for is refused before anything
// changes, the key and the scope as they were.
func TestARekeyNotMadeLeavesBrakemansScope(t *testing.T) {
	f := newFakeSync()
	e := syncEnv(t, syncDataDir(t), f, bkAgents()...)
	own := &extHost{s: e.srv, name: f.Name(), ctx: context.Background()}
	b := bkEnvOf(t, e)
	e.newDispatchProject(t, "CND", nil)
	b.enable(t, "bk", func(c map[string]any) { c["scope"] = map[string]any{"projects": []any{"CND", "OPS"}} })
	if err := own.DispatchJoin(context.Background(), "b1", "w1", "dev-b", 2_000_001, 2_100_000); err != nil {
		t.Fatal(err)
	}
	e.expect(t, "POST", "/api/dispatch/projects/CND/share", map[string]any{"opId": opID()}, http.StatusOK, "")
	e.expect(t, "POST", "/api/dispatch/projects/CND/rekey", map[string]any{"opId": opID(), "key": "OWN"}, http.StatusConflict, dispatch.RefusalShareInProgress)
	scope, _ := b.config(t)["scope"].(map[string]any)
	if got := anyStrings(scope["projects"]); !slices.Equal(got, []string{"CND", "OPS"}) {
		t.Fatalf("the scope after a refused rekey: %v", got)
	}
	// The save refused (the desk shutting down): the rekey is refused,
	// nothing renamed.
	e.newDispatchProject(t, "TWO", nil)
	b.configure(t, func(c map[string]any) { c["scope"] = map[string]any{"projects": []any{"TWO"}} })
	b.desk.closing.Store(true)
	e.expect(t, "POST", "/api/dispatch/projects/TWO/rekey", map[string]any{"opId": opID(), "key": "NEW"}, http.StatusConflict, "scope_not_saved")
	b.desk.closing.Store(false)
	e.expect(t, "GET", "/api/dispatch/projects/TWO", nil, http.StatusOK, "")
	scope, _ = b.config(t)["scope"].(map[string]any)
	if got := anyStrings(scope["projects"]); !slices.Equal(got, []string{"TWO"}) {
		t.Fatalf("the scope after a refused save: %v", got)
	}
}

// anyStrings is a JSON list of strings.
func anyStrings(v any) []string {
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, x := range l {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

// briefedTurn runs one briefing turn of Brakeman's: the newest earlier
// brief its grant is given (lastBriefing, "" for none), then the turn's
// own brief, kept, its fact resting on the ticket. It answers the earlier
// brief's id and its own.
func (b *bkEnv) briefedTurn(t *testing.T, ticket string) (earlier, own string) {
	t.Helper()
	id := b.briefing(t)
	sid, tok, _ := b.turnSession(t, id)
	status, out := b.readRequest(t, sid, tok)
	if status != http.StatusOK {
		t.Fatalf("the turn's request: %d %v", status, out)
	}
	if last, _ := entityOf(t, out, "request")["lastBriefing"].(map[string]any); last != nil {
		earlier, _ = last["id"].(string)
	}
	if status, out := b.submit(t, sid, tok, "brief-"+id, factBrief("ticket:"+ticket)); status != http.StatusCreated {
		t.Fatalf("the turn's brief: %d %v", status, out)
	}
	b.waitState(t, id, brakeman.StateBriefed)
	return earlier, id
}

// A brief made while Brakeman was scoped to the person's project CND is
// derived from that project. Once CND is given the key OWN and the
// workspace's project is received under CND, the brief is still given
// to a turn scoped to OWN, the person's project, and never to one scoped
// to CND, which only the workspace's project now is.
func TestAnEarlierBriefFollowsTheRekeyedProject(t *testing.T) {
	f := newFakeSync()
	e := syncEnv(t, syncDataDir(t), f, bkAgents()...)
	own := &extHost{s: e.srv, name: f.Name(), ctx: context.Background()}
	b := bkEnvOf(t, e)
	e.newDispatchProject(t, "CND", nil)
	e.newDispatchTicket(t, "CND", "Mine", nil)
	theirs := heldWorkspaceProject(t, own, "CND")
	b.enable(t, "bk", func(c map[string]any) {
		c["visibility"].(map[string]any)["dispatch"] = true
		c["scope"] = map[string]any{"projects": []any{"CND"}}
	})
	_, first := b.briefedTurn(t, "CND-1")
	e.expect(t, "POST", "/api/dispatch/projects/CND/rekey", map[string]any{"opId": opID(), "key": "OWN"}, http.StatusOK, "")
	if r, err := b.desk.st.Get(first); err != nil || !slices.Equal(r.Config.Scope.Projects, []string{"OWN"}) {
		t.Fatalf("the earlier request's scope after the rekey: %v %v", r.Config.Scope.Projects, err)
	}
	if earlier, _ := b.briefedTurn(t, "OWN-1"); earlier != first {
		t.Fatalf("a turn scoped to the person's project, now OWN, is given %q, not the brief %s made of it", earlier, first)
	}
	b.configure(t, func(c map[string]any) { c["scope"] = map[string]any{"projects": []any{"CND"}} })
	if earlier, _ := b.briefedTurn(t, theirs); earlier != "" {
		t.Fatalf("a turn scoped to CND, the workspace's project now, is given the brief %s", earlier)
	}
}

// A rekey not made puts Brakeman's scopes back even when the
// configuration was saved again in between: the new key back to the old
// in what is saved now, the person's other edit kept, and in the scope
// stored with the earlier request the carry renamed.
func TestARekeyNotMadePutsTheOldKeyBackInWhatWasSavedSince(t *testing.T) {
	f := newFakeSync()
	e := syncEnv(t, syncDataDir(t), f, bkAgents()...)
	own := &extHost{s: e.srv, name: f.Name(), ctx: context.Background()}
	b := bkEnvOf(t, e)
	e.newDispatchProject(t, "CND", nil)
	e.newDispatchTicket(t, "CND", "Mine", nil)
	b.enable(t, "bk", func(c map[string]any) {
		c["visibility"].(map[string]any)["dispatch"] = true
		c["scope"] = map[string]any{"projects": []any{"CND"}}
	})
	_, first := b.briefedTurn(t, "CND-1")
	if err := own.DispatchJoin(context.Background(), "b1", "w1", "dev-b", 2_000_001, 2_100_000); err != nil {
		t.Fatal(err)
	}
	e.expect(t, "POST", "/api/dispatch/projects/CND/share", map[string]any{"opId": opID()}, http.StatusOK, "")
	// The person saves the configuration between the carry and the
	// rekey's refusal: another project added to the carried scope.
	save := func() {
		b.configure(t, func(c map[string]any) {
			c["scope"] = map[string]any{"projects": append(anyStringsOf(c["scope"].(map[string]any)["projects"]), "OPS")}
		})
	}
	b.desk.afterScopeCarry.Store(&save)
	t.Cleanup(func() { b.desk.afterScopeCarry.Store(nil) })
	e.expect(t, "POST", "/api/dispatch/projects/CND/rekey", map[string]any{"opId": opID(), "key": "OWN"}, http.StatusConflict, dispatch.RefusalShareInProgress)
	scope, _ := b.config(t)["scope"].(map[string]any)
	if got := anyStrings(scope["projects"]); !slices.Equal(got, []string{"CND", "OPS"}) {
		t.Fatalf("the scope saved since, after the rekey not made: %v", got)
	}
	if r, err := b.desk.st.Get(first); err != nil || !slices.Equal(r.Config.Scope.Projects, []string{"CND"}) {
		t.Fatalf("the earlier request's scope after the rekey not made: %v %v", r.Config.Scope.Projects, err)
	}
}

// anyStringsOf is a JSON list of strings as a list of any, to append to.
func anyStringsOf(v any) []any {
	out := []any{}
	for _, s := range anyStrings(v) {
		out = append(out, s)
	}
	return out
}
