package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/capacity"
)

// conductor accounts and conductor capacity (docs/plans/capacity.md,
// PR-3), against a stub of the admin's routes answering invented views:
// no agent's tool runs and no server reads anything.

type capacityStub struct {
	mu       sync.Mutex
	requests []string // "<method> <path?query> <body>"
	views    map[string]capacity.View
	rev      int64
}

func (s *capacityStub) serve(t *testing.T) *httptest.Server {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+dispatchToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		rev := s.rev
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/accounts" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(map[string]any{"owner": true, "rev": rev, "accounts": []any{map[string]any{"id": "a-aaaaaaaaaaaa", "adapter": "claude", "label": "Work", "folder": "/srv/work", "createdAt": time.Now()}},
				"adapters": []any{map[string]any{"adapter": "claude", "name": "Claude Code", "default": "default"}}})
		case strings.HasPrefix(r.URL.Path, "/api/accounts"):
			if r.Method == http.MethodDelete {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"account": map[string]any{"id": "a-aaaaaaaaaaaa", "adapter": "claude", "label": "Home"}, "rev": rev + 1})
		case r.URL.Path == "/api/capacity" && r.URL.Query().Get("agent") != "":
			json.NewEncoder(w).Encode(s.views[r.URL.Query().Get("agent")])
		case r.URL.Path == "/api/capacity":
			var views []capacity.View
			for _, id := range []string{"claude-a", "codex-a", "ghost"} {
				views = append(views, s.views[id])
			}
			json.NewEncoder(w).Encode(map[string]any{"views": views})
		case r.URL.Path == "/api/capacity/read":
			var b struct{ Agent string }
			json.Unmarshal(body, &b)
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(s.views[b.Agent])
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(hs.Close)
	return hs
}

func stubViews() map[string]capacity.View {
	reset := time.Date(2026, 11, 11, 3, 0, 0, 0, time.UTC)
	read := time.Date(2026, 11, 10, 20, 0, 0, 0, time.UTC)
	off := false
	balance := "1250"
	return map[string]capacity.View{
		"claude-a": {Agent: capacity.ViewAgent{ID: "claude-a", Name: "Claude A", Adapter: "claude"}, Account: capacity.ViewAccount{ID: "default", Default: true, Plan: "max"},
			Host: capacity.ViewHost{Kind: capacity.HostNative, Name: "box"}, State: capacity.StateOK,
			Windows: []capacity.Window{{Kind: "weekly_all", Name: "Weekly, all models", Percent: 40, ResetsAt: &reset}},
			Credits: []capacity.Credit{{Kind: "extra_usage", Enabled: &off}}, ReadAt: &read, ReadBy: &capacity.ReadBy{Program: "Claude Code", Version: "2.0.0", Qualified: "2.0.0"}, Fresh: capacity.FreshNow},
		"codex-a": {Agent: capacity.ViewAgent{ID: "codex-a", Name: "Codex A", Adapter: "codex"}, Account: capacity.ViewAccount{ID: "a-bbbbbbbbbbbb", Label: "Work"},
			Host: capacity.ViewHost{Kind: capacity.HostNative, Name: "box"}, State: capacity.StateOK, Credits: []capacity.Credit{{Kind: "credits", Balance: &balance}},
			Limits: capacity.Limits{OrdinaryAllowed: &off}, ReadAt: &read, Fresh: capacity.FreshStale},
		"ghost": {Agent: capacity.ViewAgent{ID: "ghost", Name: "Ghost", Adapter: "claude"}, Account: capacity.ViewAccount{ID: "default"},
			Host: capacity.ViewHost{Kind: capacity.HostNative, Name: "box"}, State: capacity.StateNotInstalled, Words: capacity.StateWords(capacity.StateNotInstalled), Fresh: capacity.FreshNone},
	}
}

func runCLI(t *testing.T, run func(args []string, out, errOut io.Writer) (int, error), url string, args ...string) (int, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code, err := run(append(args, "--server", url, "--token", dispatchToken), &out, &errb)
	if err != nil && code == 0 {
		t.Fatalf("%v: %v", args, err)
	}
	return code, out.String() + errb.String()
}

// The accounts commands: list prints every adapter's default and each
// account; each write sends the rev it read.
func TestAccountsCommand(t *testing.T) {
	stub := &capacityStub{views: stubViews(), rev: 7}
	hs := stub.serve(t)
	run := func(args []string, out, errOut io.Writer) (int, error) {
		return runAccounts(t.Context(), args, out, errOut)
	}
	if code, out := runCLI(t, run, hs.URL, "list"); code != 0 || !strings.Contains(out, "Claude Code (claude): default default") || !strings.Contains(out, "a-aaaaaaaaaaaa  claude  Work") || !strings.Contains(out, "never read") {
		t.Fatalf("list: %d %s", code, out)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"add", "--agent", "claude", "--label", "Work", "--folder", "/srv/work"}, `POST /api/accounts {"adapter":"claude","expectedRev":7,"folder":"/srv/work","label":"Work"}`},
		{[]string{"rename", "a-aaaaaaaaaaaa", "Home", "office"}, `PATCH /api/accounts/a-aaaaaaaaaaaa {"expectedRev":7,"label":"Home office"}`},
		{[]string{"retire", "a-aaaaaaaaaaaa"}, `POST /api/accounts/a-aaaaaaaaaaaa/retire {"expectedRev":7}`},
		{[]string{"remove", "a-aaaaaaaaaaaa"}, `DELETE /api/accounts/a-aaaaaaaaaaaa?expectedRev=7 `},
		{[]string{"default", "claude", "a-aaaaaaaaaaaa"}, `PUT /api/accounts/defaults/claude {"account":"a-aaaaaaaaaaaa","expectedRev":7}`},
	} {
		if code, out := runCLI(t, run, hs.URL, c.args...); code != 0 {
			t.Fatalf("%v: %d %s", c.args, code, out)
		}
		stub.mu.Lock()
		last := strings.TrimSpace(stub.requests[len(stub.requests)-1])
		stub.mu.Unlock()
		if last != strings.TrimSpace(c.want) {
			t.Errorf("%v sent %s, want %s", c.args, last, c.want)
		}
	}
	if code, _ := runCLI(t, run, hs.URL, "add", "--agent", "claude"); code != 2 {
		t.Fatalf("add without a label and a folder: %d", code)
	}
}

// conductor capacity prints each state in words, a window not reported as
// "not reported", the provider's figures and this server's estimate apart;
// --json is the view; --read posts a read.
func TestCapacityCommand(t *testing.T) {
	stub := &capacityStub{views: stubViews()}
	hs := stub.serve(t)
	run := func(args []string, out, errOut io.Writer) (int, error) {
		return runCapacity(t.Context(), args, out, errOut)
	}
	code, out := runCLI(t, run, hs.URL)
	for _, want := range []string{
		"Claude A (claude-a) on the agent's own login (default)", "runs on this server (box)", "plan: max (as the tool reports it)",
		"Weekly, all models: 40% used, resets 2026-11-11 03:00 UTC", "5-hour: not reported", "extra_usage (the provider's): enabled no",
		"this server's count (an estimate)", "read 2026-11-10 20:00 UTC by Claude Code 2.0.0 (checked on 2.0.0); fresh",
		"Codex A (codex-a) on Work (a-bbbbbbbbbbbb)", "Primary window: not reported", "credits (the provider's): balance 1250", "ordinary use allowed no", "stale",
		"not_installed: the agent's program is not installed on this server",
	} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("the listing lacks %q (exit %d):\n%s", want, code, out)
		}
	}
	if strings.Count(out, "% used") != 1 {
		t.Errorf("a window not reported shown as a share:\n%s", out)
	}
	code, out = runCLI(t, run, hs.URL, "--agent", "claude-a", "--json")
	var v capacity.View
	if err := json.Unmarshal([]byte(out), &v); code != 0 || err != nil || v.Agent.ID != "claude-a" || v.State != capacity.StateOK {
		t.Fatalf("--json: %d %v %s", code, err, out)
	}
	if code, out = runCLI(t, run, hs.URL, "--agent", "codex-a", "--read", "--check"); code != 0 || !strings.Contains(out, "Codex A") {
		t.Fatalf("--read: %d %s", code, out)
	}
	stub.mu.Lock()
	last := stub.requests[len(stub.requests)-1]
	stub.mu.Unlock()
	if !strings.HasPrefix(last, `POST /api/capacity/read {"account":"","agent":"codex-a","check":true}`) {
		t.Fatalf("the read sent: %s", last)
	}
	for _, args := range [][]string{{"--read"}, {"--check"}, {"--agent", "x", "--read", "--model", "m"}, {"stray"}} {
		if code, _ := runCLI(t, run, hs.URL, args...); code != 2 {
			t.Errorf("%v: exit %d, not a usage error", args, code)
		}
	}
}
