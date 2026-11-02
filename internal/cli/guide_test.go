package cli

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/version"
)

// hasAll reports whether s holds every word.
func hasAll(s string, words ...string) bool {
	for _, w := range words {
		if !strings.Contains(s, w) {
			return false
		}
	}
	return true
}

// guideCmd runs conductor guide with args.
func guideCmd(t *testing.T, args ...string) (int, string, string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	code, err := Run(t.Context(), append([]string{"guide"}, args...), strings.NewReader(""), &out, &errb)
	return code, out.String(), errb.String(), err
}

// conductor guide in a session reads the session's guide with its token.
func TestGuideInASession(t *testing.T) {
	a := newDispatchAPI(t)
	_, sid, token := a.launchMember(t)
	inSessionOf(t, a.hs.URL, sid, token)
	code, out, errOut, err := guideCmd(t)
	if code != 0 || err != nil || !hasAll(out, "You are session "+sid, "Read one: conductor guide --section NAME.") {
		t.Fatalf("overview: %d %v %q %q", code, err, out, errOut)
	}
	code, out, _, err = guideCmd(t, "--section", "host")
	if code != 0 || err != nil || !hasAll(out, `"os"`, `"ledger"`) || strings.Contains(out, `"sessions"`) {
		t.Fatalf("host: %d %v %q", code, err, out)
	}
	if code, _, _, err := guideCmd(t, "--section", "nope"); code != 1 || err == nil || !strings.Contains(err.Error(), "no_section") {
		t.Fatalf("no section: %d %v", code, err)
	}
	if code, _, _, err := guideCmd(t, "extra"); code != 2 || err == nil {
		t.Fatalf("an argument: %d %v", code, err)
	}
}

// conductor guide outside a session, or with --server and --token, reads
// the admin's guide with the workbench token.
func TestGuideWithTheWorkbenchToken(t *testing.T) {
	a := newDispatchAPI(t)
	code, out, errOut, err := guideCmd(t, "--server", a.hs.URL, "--token", dispatchToken)
	if code != 0 || err != nil || !hasAll(out, "You are the admin (the workbench token); your scopes: admin, session, task, ticket, observe.") {
		t.Fatalf("overview: %d %v %q %q", code, err, out, errOut)
	}
	code, out, _, err = guideCmd(t, "--server", a.hs.URL, "--token", dispatchToken, "--section", "operations")
	if code != 0 || err != nil || !hasAll(out, "run_pause [admin] POST /api/runs/{run}/pause", "checks the run's revision", "task_result [task]") {
		t.Fatalf("operations: %d %v %q", code, err, out)
	}
	// An admin operation's input is its route's body: a Dispatch one's
	// operation id among its properties, and required.
	if _, comment, _ := strings.Cut(out, "ticket_comment_admin [admin]"); !hasAll(comment, `"opId":{"type":"string"}`, `"expectedRev":{"type":"integer"}`, `"required":["body","opId"]`) {
		t.Fatalf("ticket_comment_admin's input: %q", comment)
	}
	if code, _, _, err := guideCmd(t, "--server", a.hs.URL, "--token", "wrong"); code != 1 || err == nil {
		t.Fatalf("a wrong token: %d %v", code, err)
	}
	// No token, outside a session: a usage error before any request.
	t.Setenv("CONDUCTOR_WORKBENCH_TOKEN", "")
	t.Setenv("CONDUCTOR_ADMIN_TOKEN", "")
	if code, _, _, err := guideCmd(t, "--server", a.hs.URL); code != 2 || err == nil {
		t.Fatalf("no token: %d %v", code, err)
	}
}

// The operations text carries what the running server says of each
// operation's input and bounds, whatever this binary's tools would say:
// a property only that server has is printed, compact, and its bounds in
// order.
func TestGuideTextCarriesTheServersInputs(t *testing.T) {
	raw := `{"guide":1,"version":"` + version.Version + `","commit":"c","section":"operations","operations":[` +
		`{"name":"report","summary":"Report.","scope":"session","method":"POST","path":"/api/sessions/{id}/events","mcp":"report",` +
		`"input":{"type": "object", "properties": {"onlyOnThatServer": {"type": "string"}}},"bounds":{"message":500,"body":65536}},` +
		`{"name":"run_stop","summary":"Stop.","scope":"admin","method":"POST","path":"/api/runs/{run}/stop","input":null}]}`
	text, err := guideText([]byte(raw), "how")
	if err != nil {
		t.Fatal(err)
	}
	want := "report [session] POST /api/sessions/{id}/events\n    Report.\n    tool report\n" +
		`    input {"type":"object","properties":{"onlyOnThatServer":{"type":"string"}}}` + "\n    bounds body 65536, message 500\n" +
		"run_stop [admin] POST /api/runs/{run}/stop\n    Stop."
	if text != want {
		t.Fatalf("got\n%s\nwant\n%s", text, want)
	}
}

// --json prints the server's reply as it is: one JSON document naming its
// section, the server's version and the section's content.
func TestGuideJSON(t *testing.T) {
	a := newDispatchAPI(t)
	for _, section := range []string{"", "operations", "limits", "host", "receipts", "providers"} {
		args := []string{"--server", a.hs.URL, "--token", dispatchToken, "--json"}
		if section != "" {
			args = append(args, "--section", section)
		}
		code, out, _, err := guideCmd(t, args...)
		if code != 0 || err != nil {
			t.Fatalf("%q: %d %v", section, code, err)
		}
		var reply map[string]json.RawMessage
		if err := json.Unmarshal([]byte(out), &reply); err != nil {
			t.Fatalf("%q: %v %q", section, err, out)
		}
		name := section
		if name == "" {
			name = "overview"
		}
		keys := []string{}
		for k := range reply {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		want := []string{"commit", "guide", name, "section", "version"}
		slices.Sort(want)
		if !slices.Equal(keys, want) || string(reply["section"]) != `"`+name+`"` || string(reply["version"]) != `"`+version.Version+`"` {
			t.Fatalf("%q: %v", section, keys)
		}
	}
}
