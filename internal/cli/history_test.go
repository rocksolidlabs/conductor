package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// conductor history (docs/plans/history.md, PR-5), against a real server.

// historyTalker is an agent with a session recipe: it names its session
// at launch and resumes it, and waits on its input.
var historyTalker = catalog.Agent{ID: "talker", Name: "Talker", Command: []string{"/bin/sh", "-c", "exec /bin/cat", "sh"}, AllowArgs: true,
	Session: &catalog.SessionRecipe{StartArgs: []string{"--session-id", "{id}"}, IDFrom: "hook", ResumeArgs: []string{"--resume", "{id}"},
		IDPattern: `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`}}

// historyCmd runs conductor history sub against a's server.
func (a *dispatchAPI) historyCmd(t *testing.T, sub string, args ...string) (int, string, string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	full := append([]string{sub, "--server", a.hs.URL, "--token", dispatchToken}, args...)
	code, err := runHistory(t.Context(), full, &out, &errb)
	return code, out.String(), errb.String(), err
}

// mustHistory runs historyCmd and fails the test unless it exits 0.
func (a *dispatchAPI) mustHistory(t *testing.T, sub string, args ...string) string {
	t.Helper()
	code, out, errOut, err := a.historyCmd(t, sub, args...)
	if code != 0 || err != nil {
		t.Fatalf("history %s %q: exit %d %v\n%s", sub, args, code, err, errOut)
	}
	return out
}

// waitFor polls the admin route path until ok holds of its reply, as long
// as the test may take.
func (a *dispatchAPI) waitFor(t *testing.T, what, path string, ok func(map[string]any) bool) map[string]any {
	t.Helper()
	for {
		out := a.admin(t, "GET", path, nil, http.StatusOK)
		if ok(out) {
			return out
		}
		if t.Context().Err() != nil {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// endedSession launches agent with body's fields, gives its agent a turn
// when it names a conversation (resumable then), stops it and waits for
// its record's end: its id.
func (a *dispatchAPI) endedSession(t *testing.T, body map[string]any) string {
	t.Helper()
	id := a.admin(t, "POST", "/api/sessions", body, http.StatusCreated)["id"].(string)
	d, _ := a.srv.Registry().Get(id)
	l := d.(*session.Local)
	if as := l.Info().AgentSession; as != nil {
		l.ReportAgentSession(as.ID, session.PolicyLatest, true)
		a.waitFor(t, "the record's conversation resumable", "/api/history/sessions/"+id, func(out map[string]any) bool {
			s, _ := out["record"].(map[string]any)["agent"].(map[string]any)["session"].(map[string]any)
			return s != nil && s["resumable"] == true
		})
	}
	_ = l.Stop(context.Background())
	<-l.Ended()
	a.waitFor(t, "the record's end", "/api/history/sessions/"+id, func(out map[string]any) bool {
		return out["record"].(map[string]any)["endedAt"] != nil
	})
	return id
}

// conductor history search prints a table of the entries: each one's
// class, kind and title and its six fields as the server serves them.
func TestHistorySearchPrintsTheSixFields(t *testing.T) {
	a := newDispatchAPI(t)
	a.endedSession(t, map[string]any{"agentId": "cat", "name": "searchable talk"})
	// The indexer takes the record up on its own: the search is read once
	// it holds it, nothing left to catch up.
	reply := a.waitFor(t, "the session found", "/api/history?"+url.Values{"q": {"searchable"}}.Encode(), func(out map[string]any) bool {
		entries, _ := out["entries"].([]any)
		indexing, _ := out["indexing"].(map[string]any)
		return len(entries) > 0 && indexing["state"] == "ready"
	})
	e := reply["entries"].([]any)[0].(map[string]any)
	out := a.mustHistory(t, "search", "searchable")
	t.Logf("conductor history search searchable:\n%s", out)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 || strings.Join(strings.Fields(lines[0]), " ") != "CLASS KIND TITLE SOURCE HOST PROJECT REVISION WHEN AVAILABILITY" {
		t.Fatalf("the table:\n%s", out)
	}
	row := lines[1]
	at, _ := time.Parse(time.RFC3339Nano, e["at"].(string))
	project := e["project"].(map[string]any)
	for _, want := range []string{
		classWord(e["class"].(string), str(e["outcome"])), e["kind"].(string), e["title"].(string),
		e["source"].(map[string]any)["label"].(string), e["host"].(map[string]any)["name"].(string),
		cellOr(str(project["key"]) + str(project["repo"])), cellOr(str(e["revision"].(map[string]any)["label"])), when(at),
		e["availability"].(map[string]any)["words"].(string),
	} {
		if !strings.Contains(row, want) {
			t.Errorf("the row has no %q:\n%s", want, row)
		}
	}
	if j := a.mustHistory(t, "search", "searchable", "--json"); !strings.Contains(j, `"entries"`) {
		t.Fatalf("--json: %s", j)
	}
	// A usage error is refused before any request.
	before := a.count()
	if code, _, _, err := a.historyCmd(t, "search", "--limit", "0", "x"); code != 2 || err == nil || a.count() != before {
		t.Fatalf("a limit of 0: %d %v", code, err)
	}
	if code, _, _, err := a.historyCmd(t, "session", "not-an-id"); code != 2 || err == nil || a.count() != before {
		t.Fatalf("an id that is none: %d %v", code, err)
	}
}

// str is v as a string, "" for none.
func str(v any) string {
	s, _ := v.(string)
	return s
}

// conductor history resume sends its command id once more when the reply
// is lost: the server answers the second from the record's receipt, and
// one session is started.
func TestHistoryResumeSendsOneCommandIDTwice(t *testing.T) {
	a := newDispatchAPIWith(t, historyTalker)
	id := a.endedSession(t, map[string]any{"agentId": "talker"})
	before := a.count()
	path := "/api/history/sessions/" + id + "/resume"
	a.loseNext("POST", path, 1)
	out := a.mustHistory(t, "resume", id)
	var posts []seenRequest
	for _, r := range a.requests(before) {
		if r.method == "POST" && r.path == path {
			posts = append(posts, r)
		}
	}
	if len(posts) != 2 || posts[0].reply != -1 || posts[1].reply <= 0 || posts[0].body["commandId"] == nil || posts[0].body["commandId"] != posts[1].body["commandId"] {
		t.Fatalf("the posts %+v, want one command twice, the first's reply lost", posts)
	}
	started := 0
	var next string
	for _, s := range a.srv.Registry().List() {
		if s.ResumedFrom == id {
			started++
			next = s.ID
		}
	}
	if started != 1 || !strings.Contains(out, "resumed "+id+" as "+next) {
		t.Fatalf("%d sessions resumed from %s; printed %q", started, id, out)
	}
	t.Cleanup(func() {
		if d, ok := a.srv.Registry().Get(next); ok {
			_ = d.Stop(context.Background())
		}
	})
	// The record says so, as conductor history session prints it.
	rec := a.mustHistory(t, "session", id)
	t.Logf("conductor history session:\n%s", rec)
	if !strings.Contains(rec, "resume "+posts[0].body["commandId"].(string)) || !strings.Contains(rec, "started as "+next) {
		t.Fatalf("the record:\n%s", rec)
	}
}

// conductor history forget --all forgets every record that is neither
// live nor being resumed, and says how many it kept; the list is then
// empty of them.
func TestHistoryForgetAll(t *testing.T) {
	a := newDispatchAPI(t)
	first := a.endedSession(t, map[string]any{"agentId": "cat", "name": "first"})
	a.endedSession(t, map[string]any{"agentId": "cat", "name": "second"})
	live := a.admin(t, "POST", "/api/sessions", map[string]any{"agentId": "cat", "name": "live"}, http.StatusCreated)["id"].(string)
	t.Cleanup(func() {
		if d, ok := a.srv.Registry().Get(live); ok {
			_ = d.Stop(context.Background())
		}
	})
	if out := a.mustHistory(t, "sessions"); !strings.Contains(out, first) || strings.Count(out, "\n") != 4 {
		t.Fatalf("the records:\n%s", out)
	}
	if out := a.mustHistory(t, "forget", "--all"); out != "forgot 2 records; kept 1 (live, or being resumed)\n" {
		t.Fatalf("forget --all: %q", out)
	}
	out := a.mustHistory(t, "sessions")
	if strings.Contains(out, first) || !strings.Contains(out, live) || strings.Count(out, "\n") != 2 {
		t.Fatalf("the records after forget --all:\n%s", out)
	}
	if code, _, _, err := a.historyCmd(t, "forget", first); code != 1 || err == nil || !strings.Contains(err.Error(), "not_found") {
		t.Fatalf("forget a forgotten record: %d %v", code, err)
	}
	if code, _, _, err := a.historyCmd(t, "forget", "--all", first); code != 2 || err == nil {
		t.Fatalf("an id and --all: %d %v", code, err)
	}
}
