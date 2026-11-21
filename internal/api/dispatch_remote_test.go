package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// remoteRevision makes, on a store of another machine (tag a1) joined to
// the workspace w1, a shared project ODD with a ticket and a revision
// proposed on it, and applies every row of it to the server's store
// (joined as b1, the other machine named "studio"); it answers the
// revision's id and its ticket's key.
func remoteRevision(t *testing.T, e *testEnv) (string, string) {
	t.Helper()
	ctx := context.Background()
	other, err := dispatch.Open(filepath.Join(t.TempDir(), "dispatch"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Close() })
	if err := other.Join(ctx, "a1", "w1", "dev-a", 1_000_001, 1_100_000); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.dispatch.Join(ctx, "b1", "w1", "dev-b", 2_000_001, 2_100_000); err != nil {
		t.Fatal(err)
	}
	op := func(id string) dispatch.Op { return dispatch.Op{Actor: "admin", ID: id, By: "Operator"} }
	if rc, err := other.CreateProject(ctx, op("p"), dispatch.NewProject{Key: "ODD", Name: "Odd", Cwd: "/work/elsewhere"}); err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("project %+v %v", rc, err)
	}
	if err := other.ShareForTest(ctx, "ODD", "w1"); err != nil {
		t.Fatal(err)
	}
	rc, err := other.CreateTicket(ctx, op("t"), dispatch.NewTicket{Project: "ODD", Title: "Made elsewhere"})
	if err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("ticket %+v %v", rc, err)
	}
	crewJSON, _ := json.Marshal(map[string]any{"id": "crew", "name": "Crew", "cwd": "/work/elsewhere", "members": []map[string]any{{"name": "dev", "agentId": "cat"}},
		"tasks": []map[string]any{{"id": "build", "member": "dev", "kind": "work", "instructions": "Build it", "checks": []map[string]any{{"id": "t", "argv": []string{"secret-check"}}}}}})
	body := dispatch.PlanBody{Base: strings.Repeat("ab", 20), Crew: crewJSON,
		Agents: []dispatch.PlanAgent{{ID: "cat", Command: []string{"/opt/elsewhere/cat-bin"}, SHA256: strings.Repeat("cd", 32)}}}
	prc, err := other.ProposePlan(ctx, op("plan"), rc.Key, dispatch.PlanRequest{Crew: "crew"}, body)
	if err != nil || prc.Status != dispatch.StatusDone {
		t.Fatalf("propose %+v %v", prc, err)
	}
	if arc, err := other.ApprovePlan(ctx, op("approve"), prc.ID, 0, "go"); err != nil || arc.Status != dispatch.StatusDone {
		t.Fatalf("approve %+v %v", arc, err)
	}
	rows, _, err := other.ChangesFrom(ctx, 0, dispatch.MaxChangesPage)
	if err != nil {
		t.Fatal(err)
	}
	views := map[string][]byte{}
	for _, c := range rows {
		if c.ViewDigest != "" {
			if views[c.ViewDigest], err = other.RevisionView(ctx, c.ViewDigest); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := e.srv.dispatch.ApplyRemote(ctx, rows, views, nil); err != nil {
		t.Fatal(err)
	}
	return prc.ID, rc.Key
}

// A revision made on another machine reads as its view: no body, its
// tasks and agents by name, the machine it was made on and approved on,
// and nothing of its crew's paths or commands; the ticket's list says the
// same; a launch of it here is refused 409 body_elsewhere.
func TestViewOnlyRevisionReadsItsView(t *testing.T) {
	e := newTestEnv(t, func(cfg *config.Config) { cfg.DataDir = filepath.Join(t.TempDir(), "data") })
	id, key := remoteRevision(t, e)
	out := e.expect(t, "GET", "/api/dispatch/plans/"+id, nil, http.StatusOK, "")
	plan := entityOf(t, out, "plan")
	if plan["bodyHere"] != false || plan["body"] != nil || plan["view"] == nil || plan["origin"] != "a1" || plan["approvedOrigin"] != "a1" {
		t.Fatalf("the revision held as its view: %v", plan)
	}
	raw, _ := json.Marshal(out)
	for _, w := range []string{"/work/elsewhere", "secret-check", "/opt/elsewhere"} {
		if strings.Contains(string(raw), w) {
			t.Fatalf("the read holds %q: %s", w, raw)
		}
	}
	list := listOf(t, e.expect(t, "GET", "/api/dispatch/tickets/"+key+"/plans", nil, http.StatusOK, ""), "plans")
	if len(list) != 1 || list[0]["bodyHere"] != false {
		t.Fatalf("the list: %v", list)
	}
	e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/launch", map[string]any{"opId": opID(), "plan": id}, http.StatusConflict, dispatch.RefusalBodyElsewhere)
}
