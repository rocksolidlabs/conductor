package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/agents"
)

// open_url and conductor open post the link to the session's open route
// with the session's token and say, in one line, what the server did.
func TestOpenURLToolAndCommandPostTheLink(t *testing.T) {
	srv, seen := fakeSelf(t)
	env := func(k string) string {
		switch k {
		case "CONDUCTOR_SESSION_ID":
			return "s1"
		case "CONDUCTOR_NOTIFY_URL":
			return srv.URL + "/api/sessions/s1/attention"
		case "CONDUCTOR_NOTIFY_TOKEN":
			return "agent-tok"
		}
		return ""
	}
	replies := mcpRun(t, env,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"open_url","arguments":{"url":"https://claude.ai/artifact/abc"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"open_url","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"open_url","arguments":{"url":"ftp://example.test/"}}}`,
	)
	if text, isErr := toolText(t, replies[0]); isErr || text != "opened in edge (Profile 2)" {
		t.Fatalf("open_url %q %v", text, isErr)
	}
	if text, isErr := toolText(t, replies[1]); !isErr || !strings.Contains(text, "needs a url") {
		t.Fatalf("open_url with no url %q %v", text, isErr)
	}
	if text, isErr := toolText(t, replies[2]); !isErr || !strings.Contains(text, "invalid_url") {
		t.Fatalf("open_url with an ftp url %q %v", text, isErr)
	}
	if len(*seen) != 2 {
		t.Fatalf("the requests %v", *seen)
	}
	first := (*seen)[0]
	if first["path"] != "/api/sessions/s1/open" || first["auth"] != "Bearer agent-tok" || first["body"].(map[string]any)["url"] != "https://claude.ai/artifact/abc" {
		t.Fatalf("open_url's request %v", first)
	}

	var out, errb bytes.Buffer
	code, err := runOpen(context.Background(), []string{"--", "https://github.com/rocksolidlabs/conductor/pull/1"}, &out, &errb, env)
	if code != 0 || err != nil || out.String() != "opened in edge (Profile 2)\n" {
		t.Fatalf("conductor open: %d %v %q %q", code, err, out.String(), errb.String())
	}
	if got := (*seen)[2]["body"].(map[string]any)["url"]; got != "https://github.com/rocksolidlabs/conductor/pull/1" {
		t.Fatalf("conductor open's request %v", (*seen)[2])
	}
	// The URL is the one argument, never two.
	if code, err := runOpen(context.Background(), []string{"https://a.test/", "https://b.test/"}, &out, &errb, env); code != 2 || err == nil {
		t.Fatalf("two links: %d %v", code, err)
	}
	if code, err := runOpen(context.Background(), nil, &out, &errb, env); code != 2 || err == nil {
		t.Fatalf("no link: %d %v", code, err)
	}
	if len(*seen) != 3 {
		t.Fatalf("a refused command posted: %v", *seen)
	}
}

// Outside a session both answer as the other tools do.
func TestOpenURLOutsideASession(t *testing.T) {
	none := func(string) string { return "" }
	replies := mcpRun(t, none, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"open_url","arguments":{"url":"https://example.test/"}}}`)
	if text, isErr := toolText(t, replies[0]); !isErr || text != errNotInSession.Error() {
		t.Fatalf("open_url outside a session %q %v", text, isErr)
	}
	var out, errb bytes.Buffer
	if code, err := runOpen(context.Background(), []string{"https://example.test/"}, &out, &errb, none); code != 1 || err != errNotInSession {
		t.Fatalf("conductor open outside a session: %d %v", code, err)
	}
}

// Under Codex the MCP server sees only the variables Codex passes on
// (env_vars, #354): open_url works with those alone.
func TestOpenURLWorksWithCodexsMCPEnvironment(t *testing.T) {
	srv, seen := fakeSelf(t)
	var listed []string
	for _, a := range agents.MCPFor("codex", t.TempDir()) {
		if v, ok := strings.CutPrefix(a, "mcp_servers.conductor.env_vars="); ok {
			if err := json.Unmarshal([]byte(v), &listed); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(listed) == 0 {
		t.Fatal("Codex's launch names no env_vars")
	}
	session := map[string]string{
		"CONDUCTOR_SESSION_ID":   "s1",
		"CONDUCTOR_NOTIFY_URL":   srv.URL + "/api/sessions/s1/attention",
		"CONDUCTOR_NOTIFY_TOKEN": "agent-tok",
	}
	env := func(k string) string {
		if !slices.Contains(listed, k) {
			return ""
		}
		return session[k]
	}
	replies := mcpRun(t, env, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"open_url","arguments":{"url":"https://example.test/"}}}`)
	if text, isErr := toolText(t, replies[0]); isErr || text != "opened in edge (Profile 2)" || len(*seen) != 1 {
		t.Fatalf("open_url under Codex's environment: %q %v", text, isErr)
	}
}
