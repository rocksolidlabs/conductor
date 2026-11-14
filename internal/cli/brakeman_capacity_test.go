package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/capacity"
)

// conductor brakeman observe capacity (docs/plans/capacity.md, PR-6), in a
// turn's session, sends the turn's token and its session to GET
// /api/capacity with the agent and account asked, and prints the view the
// server kept as it answered it; the MCP tool's observe sends the same.
// The server is a stub answering an invented view as a turn reads it.
func TestBrakemanObserveCapacity(t *testing.T) {
	clearConductorEnv(t)
	at := time.Date(2026, 11, 11, 3, 0, 0, 0, time.UTC)
	reset := at.Add(72 * time.Hour)
	view := brakeman.CapacityOf(capacity.View{
		Agent: capacity.ViewAgent{ID: "claude", Name: "Claude Code", Adapter: "claude", Version: "2.1.318"}, Account: capacity.ViewAccount{ID: "a-abcdefghijk2", Label: "Work", Plan: "max"},
		Host: capacity.ViewHost{Kind: capacity.HostNative}, State: capacity.StateOK, Fresh: capacity.FreshNow, ReadAt: &at,
		Windows: []capacity.Window{{Kind: "weekly_all", Name: "Weekly, all models", Percent: 40, ResetsAt: &reset, Severity: "normal"}},
		ReadBy:  &capacity.ReadBy{Program: "Claude Code", Version: "2.1.318", Qualified: "2.1.318"},
	}, at)
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("Authorization")+" "+r.Header.Get("X-Conductor-Session"))
		mu.Unlock()
		if r.URL.Path != "/api/capacity" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(view)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CONDUCTOR_NOTIFY_URL", srv.URL+"/api/sessions/s1/attention")
	t.Setenv("CONDUCTOR_NOTIFY_TOKEN", "agent-tok")
	t.Setenv(envBrakeman, "br-1")
	code, out, _, err := brakemanCmd(t, "", "observe", "capacity", "--agent", "claude", "--account", "a-abcdefghijk2")
	var got, want map[string]any
	b, _ := json.Marshal(view)
	if code != 0 || err != nil || json.Unmarshal([]byte(out), &got) != nil || json.Unmarshal(b, &want) != nil {
		t.Fatalf("the command: %d %v %q", code, err, out)
	}
	if !reflect.DeepEqual(got, want) || got["ref"] != "capacity:claude/a-abcdefghijk2@2026-11-11T03:00:00Z" {
		t.Fatalf("printed %s, want %s", out, b)
	}
	replies := mcpRun(t, brakemanEnv(srv.URL, "s1", "agent-tok"),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"observe","arguments":{"what":"capacity","query":{"agent":"claude","account":"a-abcdefghijk2"}}}}`)
	if text, isErr := toolText(t, replies[0]); isErr || text+"\n" != out {
		t.Errorf("the tool's text: %q %v, want %q", text, isErr, out)
	}
	mu.Lock()
	defer mu.Unlock()
	sent := "GET /api/capacity?account=a-abcdefghijk2&agent=claude Bearer agent-tok s1"
	if len(asked) != 2 || asked[0] != sent || asked[1] != sent {
		t.Fatalf("what the server was asked: %q, want %q twice", asked, sent)
	}
}
