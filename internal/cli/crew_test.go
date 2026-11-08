package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/agents"
)

// fakeSelf fakes the session's self-service routes and records what came in.
func fakeSelf(t *testing.T) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var seen []map[string]any
	mux := http.NewServeMux()
	record := func(r *http.Request, body any) {
		seen = append(seen, map[string]any{"method": r.Method, "path": r.URL.Path, "auth": r.Header.Get("Authorization"), "body": body})
	}
	run := map[string]any{"id": "team-1a2b3c4d", "state": "running", "name": "review team", "members": []any{
		map[string]any{"name": "lead", "status": "running", "agentId": "claude"},
		map[string]any{"name": "reviewer", "status": "starting", "agentId": "codex", "needsInput": true},
	}}
	mux.HandleFunc("POST /api/sessions/s1/crew", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		record(r, body)
		if body["self"] == "nobody" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_crew","message":"self must name one of the members"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"run": run, "member": body["self"], "url": "http://example.test/runs/team-1a2b3c4d"})
	})
	mux.HandleFunc("POST /api/sessions/s1/run/members", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		record(r, body)
		_ = json.NewEncoder(w).Encode(map[string]any{"run": run})
	})
	mux.HandleFunc("GET /api/sessions/s1/run", func(w http.ResponseWriter, r *http.Request) {
		record(r, nil)
		_ = json.NewEncoder(w).Encode(map[string]any{"run": run, "member": "lead"})
	})
	mux.HandleFunc("POST /api/sessions/s1/links/agent", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		record(r, body)
		_ = json.NewEncoder(w).Encode(map[string]any{"url": "http://example.test/join/tok", "link": map[string]any{"role": "view"}, "token": "tok"})
	})
	mux.HandleFunc("POST /api/sessions/s1/open", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		record(r, body)
		if body["url"] == "ftp://example.test/" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_url","message":"url must be an http:// or https:// URL with a host"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"outcome": "browser", "browser": map[string]any{"name": "edge", "profile": "Profile 2"}, "message": "opened in edge (Profile 2)"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &seen
}

func inSession(t *testing.T, srv *httptest.Server) {
	t.Helper()
	t.Setenv("CONDUCTOR_SESSION_ID", "s1")
	t.Setenv("CONDUCTOR_NOTIFY_URL", srv.URL+"/api/sessions/s1/attention")
	t.Setenv("CONDUCTOR_NOTIFY_TOKEN", "agent-tok")
}

func runCrewCmd(t *testing.T, stdin string, args ...string) (int, string, string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	code, err := runCrew(context.Background(), args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String(), err
}

func TestCrewCreateReadsJSONAndPrintsTheRunURL(t *testing.T) {
	srv, seen := fakeSelf(t)
	inSession(t, srv)
	file := filepath.Join(t.TempDir(), "crew.json")
	_ = os.WriteFile(file, []byte(`{"name":"review team","goal":"review","members":[{"name":"lead","agentId":"claude","prompt":"lead","start":{"when":"manual"}},{"name":"reviewer","agentId":"codex","prompt":"review $GOAL","start":{"when":"immediately"}}]}`), 0o600)
	code, out, _, err := runCrewCmd(t, "", "create", file, "--self", "lead", "--open")
	if err != nil || code != 0 {
		t.Fatalf("%d %v", code, err)
	}
	if !strings.HasPrefix(out, "run team-1a2b3c4d\nhttp://example.test/runs/team-1a2b3c4d\nmember lead\n") || !strings.Contains(out, "reviewer") || !strings.Contains(out, "needs input") {
		t.Fatalf("out %q", out)
	}
	req := (*seen)[0]
	body := req["body"].(map[string]any)
	if req["auth"] != "Bearer agent-tok" || req["path"] != "/api/sessions/s1/crew" || body["self"] != "lead" || body["open"] != true || body["name"] != "review team" {
		t.Fatalf("request %v", req)
	}
	// From stdin, with self in the file; a refusal is the server's words.
	code, _, _, err = runCrewCmd(t, `{"name":"x","goal":"g","members":[],"self":"nobody"}`, "create", "-")
	if code != 1 || err == nil || !strings.Contains(err.Error(), "self must name one of the members (invalid_crew)") {
		t.Fatalf("refused: %d %v", code, err)
	}
	if code, _, _, err := runCrewCmd(t, `{"name":"x"}`, "create", "-"); code != 2 || err == nil || !strings.Contains(err.Error(), "--self") {
		t.Fatalf("no self: %d %v", code, err)
	}
	if code, _, _, err := runCrewCmd(t, `{"nope":1`, "create", "-", "--self", "me"); code != 2 || err == nil {
		t.Fatalf("bad json: %d %v", code, err)
	}
}

func TestCrewAddStatusAndLink(t *testing.T) {
	srv, seen := fakeSelf(t)
	inSession(t, srv)
	code, out, _, err := runCrewCmd(t, `{"name":"docs","agentId":"claude","prompt":"write","start":{"when":"manual"}}`, "add", "-")
	if err != nil || code != 0 || !strings.HasPrefix(out, "added docs\n") {
		t.Fatalf("add: %d %v %q", code, err, out)
	}
	code, out, _, err = runCrewCmd(t, "", "status")
	if err != nil || code != 0 || !strings.HasPrefix(out, "run team-1a2b3c4d (running) review team\nyou are lead\n") || !strings.Contains(out, "lead") {
		t.Fatalf("status: %d %v %q", code, err, out)
	}
	code, out, _, err = runCrewCmd(t, "", "link", "--ttl", "3h", "--label", "for the PR")
	if err != nil || code != 0 || out != "http://example.test/join/tok\n" {
		t.Fatalf("link: %d %v %q", code, err, out)
	}
	link := (*seen)[len(*seen)-1]["body"].(map[string]any)
	if link["ttlSeconds"] != float64(10800) || link["label"] != "for the PR" {
		t.Fatalf("link body %v", link)
	}
	if code, _, _, err := runCrewCmd(t, "", "nope"); code != 2 || err == nil {
		t.Fatalf("unknown: %d %v", code, err)
	}
}

func TestCrewCommandsAreSilentOutsideASession(t *testing.T) {
	for _, k := range []string{"CONDUCTOR_SESSION_ID", "CONDUCTOR_NOTIFY_URL", "CONDUCTOR_NOTIFY_TOKEN"} {
		t.Setenv(k, "")
	}
	for _, args := range [][]string{{"status"}, {"link"}, {"create", "-", "--self", "me"}, {"add", "-"}} {
		code, out, errOut, err := runCrewCmd(t, `{}`, args...)
		if code != 0 || err != nil || out != "" || errOut != "" {
			t.Fatalf("%v: %d %v %q %q", args, code, err, out, errOut)
		}
	}
	code, _, _, err := runCrewCmd(t, "", "status", "--quiet=false")
	if code != 1 || err == nil {
		t.Fatalf("loud: %d %v", code, err)
	}
	var errb bytes.Buffer
	if code, _ := runCrew(context.Background(), nil, strings.NewReader(""), io.Discard, &errb); code != 2 || !strings.Contains(errb.String(), "Usage") {
		t.Fatalf("no args: %d %q", code, errb.String())
	}
}

// The skill teaches every crew subcommand there is, with the binary the
// session names, and the completion table agrees on the subcommands; crew
// recheck, the operator's with the admin's token, which an agent's session
// cannot call, it never teaches.
func TestSkillNamesEveryCrewCommand(t *testing.T) {
	if strings.Contains(agents.Skill, "crew recheck") {
		t.Error("the skill teaches crew recheck, the operator's")
	}
	for _, tc := range []struct {
		name  string
		usage string
	}{{"crew", crewUsage}, {"task", taskUsage}} {
		var words []string
		for _, c := range completionSpec() {
			if c.name == tc.name {
				words = c.words
			}
		}
		if len(words) == 0 {
			t.Fatalf("no %s words in the completion table", tc.name)
		}
		for _, w := range words {
			if tc.name == "crew" && w == "recheck" {
				if !strings.Contains(tc.usage, "conductor crew recheck") {
					t.Error("the usage does not list crew recheck")
				}
				continue
			}
			if !strings.Contains(agents.Skill, `"${CONDUCTOR_BIN:-conductor}" `+tc.name+" "+w) {
				t.Errorf("the skill does not teach %s %s", tc.name, w)
			}
			if !strings.Contains(tc.usage, "conductor "+tc.name+" "+w) {
				t.Errorf("the usage does not list %s %s", tc.name, w)
			}
			if !strings.Contains(usage, "conductor "+tc.name+" ") {
				t.Errorf("conductor's usage does not list %s", tc.name)
			}
		}
	}
}
