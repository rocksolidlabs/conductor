package api

import (
	"net/http"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/session"
)

// The join reply's session says whether its agent reports its turns
// (Info.Turns), which the guest page reads: absent while the agent has
// reported nothing of its own, true once it has.
func TestAJoinSaysWhetherTheAgentReportsTurns(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	resp, out := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create link: %d %v", resp.StatusCode, out)
	}
	token := out["token"].(string)
	joined := func() map[string]any {
		t.Helper()
		resp, out := e.do("GET", "/api/join/"+token, "", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("join %d %v", resp.StatusCode, out)
		}
		return out["session"].(map[string]any)
	}
	if s := joined(); s["id"] != id {
		t.Fatalf("joined %v", s)
	} else if _, has := s["turns"]; has {
		t.Fatalf("turns before any report: %v", s)
	}
	d, ok := e.srv.registry.Get(id)
	if !ok {
		t.Fatal("the session is not listed")
	}
	l := d.(*session.Local)
	// An operator's mark teaches nothing; the agent's own working does.
	if err := l.TrySetAttentionFull(session.AttentionWorking, "", session.SourceAdmin, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, has := joined()["turns"]; has {
		t.Fatal("turns after an operator's mark")
	}
	if err := l.TrySetAttentionFull(session.AttentionDone, "done", session.SourceAPI, session.KindDone, nil); err != nil {
		t.Fatal(err)
	}
	if s := joined(); s["turns"] != true {
		t.Fatalf("joined after the agent's done: %v", s)
	}
}
