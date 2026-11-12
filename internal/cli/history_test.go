package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/journal"
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

// cannedHistory runs conductor history sub against a server that answers
// path with reply, as the server writes JSON (HTML characters escaped),
// and 404 for anything else.
func cannedHistory(t *testing.T, path string, reply any, sub string, args ...string) (int, string, error) {
	t.Helper()
	clearConductorEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(srv.Close)
	var out, errb bytes.Buffer
	code, err := runHistory(t.Context(), append([]string{sub, "--server", srv.URL, "--token", dispatchToken}, args...), &out, &errb)
	return code, out.String(), err
}

// A search that stopped at its scan or ranking bound says so, with no
// entries as much as with some: it is never taken for one that read every
// match. One that read every match says nothing of a bound.
func TestHistorySearchSaysWhichBoundItStoppedAt(t *testing.T) {
	entry := map[string]any{"key": "session:0000000000000001#e1", "kind": "session", "class": "said", "title": "the talk", "at": "2026-11-05T09:00:00Z"}
	for _, c := range []struct {
		cut     string
		entries []any
		want    string
	}{
		{"scanned", []any{}, "stopped at its scan bound"},
		{"scanned", []any{entry}, "stopped at its scan bound"},
		{"ranked", []any{entry}, "stopped at its ranking bound"},
		{"", []any{entry}, ""},
	} {
		reply := map[string]any{"entries": c.entries, "total": len(c.entries), "indexing": map[string]any{"state": "ready"}}
		if c.cut != "" {
			reply["cut"] = c.cut
		}
		code, out, err := cannedHistory(t, "/api/history", reply, "search", "agent:talker", "words")
		if code != 0 || err != nil {
			t.Fatalf("cut %q: %d %v", c.cut, code, err)
		}
		switch {
		case c.want != "" && !strings.Contains(out, c.want):
			t.Errorf("cut %q, %d entries: no %q in\n%s", c.cut, len(c.entries), c.want, out)
		case c.want == "" && strings.Contains(out, "bound"):
			t.Errorf("no cut: a bound named in\n%s", out)
		}
	}
}

// conductor history session prints every string of a record through
// oneLine: a control character a record holds reaches the terminal as a
// space or U+FFFD, never as itself, and makes no line of its own. The
// record is filled by reflection over its type and every type it holds,
// so a field added later is held to the same.
func TestARecordsViewPrintsEveryFieldThroughOneLine(t *testing.T) {
	const marked = "a\x1b]0;t\x07\x1b[2J\x00\x7f\u0085\u009b\r\t\v\fb\nc"
	at := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name   string
		live   bool
		adjust func(*journal.Record)
	}{
		{"ended", false, func(*journal.Record) {}},
		{"live", true, func(*journal.Record) {}},
		// The branches a field's own value picks: a resume that started
		// names its session; an answer's entry prints its outcome.
		{"a resume started, an answer", false, func(r *journal.Record) {
			r.Resumes[0].State = journal.ResumeStarted
			r.Entries[0].Kind = journal.KindAnswer
		}},
		{"arguments not kept", false, func(r *journal.Record) { r.Launch.ArgsKept = false }},
	} {
		view := func(text string) string {
			var r journal.Record
			fillRecord(reflect.ValueOf(&r).Elem(), text, at)
			c.adjust(&r)
			var b bytes.Buffer
			writeRecord(&b, r, c.live)
			return b.String()
		}
		plain, out := view("x"), view(marked)
		for _, ch := range out {
			if ch != '\n' && unicode.IsControl(ch) {
				t.Errorf("%s: the view holds %U:\n%q", c.name, ch, out)
				break
			}
		}
		if got, want := strings.Count(out, "\n"), strings.Count(plain, "\n"); got != want {
			t.Errorf("%s: the view has %d lines, %d with plain text:\n%q", c.name, got, want, out)
		}
		if !strings.Contains(out, "b c") {
			t.Errorf("%s: the fields' text is not in the view:\n%q", c.name, out)
		}
	}
}

// fillRecord sets every exported field v holds, through pointers, slices
// and maps: a string to text, a bool to true, a number to 1, a time to at.
func fillRecord(v reflect.Value, text string, at time.Time) {
	switch {
	case v.Type() == reflect.TypeFor[time.Time]():
		v.Set(reflect.ValueOf(at))
	case v.Kind() == reflect.String:
		v.SetString(text)
	case v.Kind() == reflect.Bool:
		v.SetBool(true)
	case v.CanInt():
		v.SetInt(1)
	case v.Kind() == reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fillRecord(v.Elem(), text, at)
	case v.Kind() == reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fillRecord(v.Index(0), text, at)
	case v.Kind() == reflect.Map:
		k, e := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		fillRecord(k, text, at)
		fillRecord(e, text, at)
		v.Set(reflect.MakeMap(v.Type()))
		v.SetMapIndex(k, e)
	case v.Kind() == reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fillRecord(v.Field(i), text, at)
			}
		}
	default:
		panic("fillRecord: a field of kind " + v.Kind().String())
	}
}

// conductor history sessions reads a whole page of rows each at its most
// as the server writes it, every path in '<', which HTML escaping writes
// in six bytes: a page past the other routes' bound.
func TestHistorySessionsReadsAWholePageAtItsBound(t *testing.T) {
	f := func(n int) string { return strings.Repeat("<", n) }
	var rows []journal.Summary
	for i := range journal.MaxPage {
		rows = append(rows, journal.Summary{ID: fmt.Sprintf("%016x", i+1), Name: f(200), Kind: "server",
			Agent: journal.Agent{ID: f(200), Name: f(200), Adapter: f(200), Version: f(200), Session: &journal.AgentSession{ID: f(200), Source: f(16), Resumable: true}},
			Host:  journal.Host{Name: f(200), User: f(200), Hosted: true}, Where: f(200), Cwd: f(journal.MaxCwd), Repo: f(journal.MaxCwd), Branch: f(journal.MaxBranch),
			Crew:      &journal.Crew{RunID: f(200), CrewID: f(200), Member: f(200), Task: f(200), Attempt: 1 << 30, Brakeman: f(200)},
			StartedAt: time.Now(), EndedAt: time.Now(), LostAt: time.Now(), Ending: f(200), Entries: 1 << 30, Files: 1 << 30, Dropped: 1 << 30, Newer: true})
	}
	reply := map[string]any{"records": rows, "next": "", "readOnly": true}
	var page bytes.Buffer
	_ = json.NewEncoder(&page).Encode(reply)
	if page.Len() <= maxHistoryCLIReply || page.Len() > maxHistorySessionsReply {
		t.Fatalf("the page is %d bytes: want past %d, within %d", page.Len(), maxHistoryCLIReply, maxHistorySessionsReply)
	}
	code, out, err := cannedHistory(t, "/api/history/sessions", reply, "sessions")
	if code != 0 || err != nil || strings.Count(out, "\n") != journal.MaxPage+1 {
		t.Fatalf("a page of %d bytes: %d %v, %d lines", page.Len(), code, err, strings.Count(out, "\n"))
	}
}
