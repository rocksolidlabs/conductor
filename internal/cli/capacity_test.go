package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

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
	refusals map[string]stubReply // by "<method> <path>": answered instead
	endless  bool                 // the listing always names a next further on
	big      bool                 // GET /api/accounts answers past maxReply
}

type stubReply struct {
	status int
	body   any
}

func (s *capacityStub) sent() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
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
		rev, n := s.rev, len(s.requests)
		refusal, refused := s.refusals[r.Method+" "+r.URL.Path]
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		answer := func(status int, v any) {
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(v)
		}
		read := time.Date(2026, 11, 10, 20, 0, 0, 0, time.UTC)
		account := map[string]any{"id": "a-aaaaaaaaaaaa", "adapter": "claude", "label": "Home office", "folder": "/srv/work", "createdAt": read}
		switch q := r.URL.Query(); {
		case refused:
			answer(refusal.status, refusal.body)
		case r.URL.Path == "/api/accounts" && r.Method == http.MethodGet && s.big:
			w.Write(bytes.Repeat([]byte(" "), maxReply+1))
		case r.URL.Path == "/api/accounts" && r.Method == http.MethodGet:
			answer(http.StatusOK, map[string]any{"owner": true, "rev": rev,
				"accounts": []any{map[string]any{"id": "a-aaaaaaaaaaaa", "adapter": "claude", "label": "Work", "folder": "/srv/work", "createdAt": read}},
				"adapters": []any{map[string]any{"adapter": "claude", "name": "Claude Code", "default": "default", "reading": map[string]any{"agent": "claude-a", "state": "ok", "readAt": read}}}})
		case r.URL.Path == "/api/accounts" && r.Method == http.MethodPost:
			answer(http.StatusCreated, map[string]any{"account": account, "rev": rev + 1})
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case strings.HasPrefix(r.URL.Path, "/api/accounts/defaults/"):
			answer(http.StatusOK, map[string]any{"defaults": map[string]string{"claude": "a-aaaaaaaaaaaa"}, "rev": rev + 1})
		case strings.HasSuffix(r.URL.Path, "/retire"):
			account["retired"] = true
			answer(http.StatusOK, map[string]any{"account": account, "rev": rev + 1})
		case strings.HasPrefix(r.URL.Path, "/api/accounts/"):
			answer(http.StatusOK, map[string]any{"account": account, "rev": rev + 1})
		case r.URL.Path == "/api/capacity" && q.Get("agent") != "":
			v, ok := s.views[q.Get("agent")]
			if !ok {
				answer(http.StatusNotFound, map[string]any{"error": map[string]any{"code": "not_found", "message": "no such agent"}})
				return
			}
			answer(http.StatusOK, v)
		case r.URL.Path == "/api/capacity" && s.endless:
			answer(http.StatusOK, map[string]any{"views": []any{s.views["ghost"]}, "next": fmt.Sprintf("zz/%06d", n)})
		case r.URL.Path == "/api/capacity":
			// Two a page, in the server's order, as capacityPage pages them.
			keys := []string{"claude-a/default", "codex-a/a-bbbbbbbbbbbb", "ghost/default"}
			page := map[string]any{"views": []capacity.View{}}
			var views []capacity.View
			for i, k := range keys {
				if after := q.Get("after"); after != "" && k <= after {
					continue
				}
				if len(views) == 2 {
					page["next"] = keys[i-1]
					break
				}
				views = append(views, s.views[strings.SplitN(k, "/", 2)[0]])
			}
			page["views"] = views
			answer(http.StatusOK, page)
		case r.URL.Path == "/api/capacity/read":
			var b struct{ Agent string }
			json.Unmarshal(body, &b)
			answer(http.StatusAccepted, s.views[b.Agent])
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
		"later": {Agent: capacity.ViewAgent{ID: "later", Adapter: "claude"}, Account: capacity.ViewAccount{ID: "default"},
			Host: capacity.ViewHost{Kind: "elsewhere"}, State: "a_later_state", Fresh: "a_later_freshness"},
	}
}

type cliRun func(args []string, out, errOut io.Writer) (int, error)

// runCLI runs a command against url and answers its exit and all it
// printed, its error's text last (what conductor prints on stderr).
func runCLI(t *testing.T, run cliRun, url string, args ...string) (int, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code, err := run(append(args, "--server", url, "--token", dispatchToken), &out, &errb)
	if err != nil && code == 0 {
		t.Fatalf("%v: %v", args, err)
	}
	text := out.String() + errb.String()
	if err != nil {
		text += "conductor: " + err.Error() + "\n"
	}
	return code, text
}

func accountsRun(t *testing.T) cliRun {
	return func(args []string, out, errOut io.Writer) (int, error) {
		return runAccounts(t.Context(), args, out, errOut)
	}
}

func capacityRun(t *testing.T) cliRun {
	return func(args []string, out, errOut io.Writer) (int, error) {
		return runCapacity(t.Context(), args, out, errOut)
	}
}

// The accounts commands: list prints every adapter's default and each
// account; each write sends the rev it read, to its route, with its body
// as the route takes it; a refusal is the server's own words with its
// field and what holds the account; the token goes in the Authorization
// header alone, never in a request's path, query or body, and is never
// printed; a usage error asks nothing.
func TestAccountsCommand(t *testing.T) {
	stub := &capacityStub{views: stubViews(), rev: 7}
	hs := stub.serve(t)
	run := accountsRun(t)
	code, out := runCLI(t, run, hs.URL, "list")
	for _, want := range []string{"Claude Code (claude): default default; its own login: ok, read 2026-11-10 20:00 UTC by claude-a", "a-aaaaaaaaaaaa  claude  Work  /srv/work  never read"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Fatalf("list lacks %q: %d %s", want, code, out)
		}
	}
	code, out = runCLI(t, run, hs.URL, "list", "--json")
	var doc listedAccounts
	if err := json.Unmarshal([]byte(out), &doc); code != 0 || err != nil || doc.Rev != 7 || len(doc.Accounts) != 1 || doc.Adapters[0].Reading == nil {
		t.Fatalf("list --json: %d %v %s", code, err, out)
	}
	for _, c := range []struct {
		args       []string
		want, said string
	}{
		{[]string{"add", "--agent", "claude", "--label", "Work", "--folder", "/srv/work"}, `POST /api/accounts {"adapter":"claude","expectedRev":7,"folder":"/srv/work","label":"Work"}`, "added a-aaaaaaaaaaaa (claude, Home office)"},
		{[]string{"rename", "a-aaaaaaaaaaaa", "Home", "office"}, `PATCH /api/accounts/a-aaaaaaaaaaaa {"expectedRev":7,"label":"Home office"}`, "renamed a-aaaaaaaaaaaa to Home office"},
		{[]string{"retire", "a-aaaaaaaaaaaa"}, `POST /api/accounts/a-aaaaaaaaaaaa/retire {"expectedRev":7}`, "retired a-aaaaaaaaaaaa"},
		{[]string{"remove", "a-aaaaaaaaaaaa"}, `DELETE /api/accounts/a-aaaaaaaaaaaa?expectedRev=7 `, "removed a-aaaaaaaaaaaa"},
		{[]string{"default", "claude", "a-aaaaaaaaaaaa"}, `PUT /api/accounts/defaults/claude {"account":"a-aaaaaaaaaaaa","expectedRev":7}`, "the default of claude is a-aaaaaaaaaaaa"},
	} {
		before := len(stub.sent())
		code, out := runCLI(t, run, hs.URL, c.args...)
		if code != 0 || !strings.Contains(out, c.said) {
			t.Fatalf("%v: %d %s", c.args, code, out)
		}
		sent := stub.sent()[before:]
		if len(sent) != 2 || !strings.HasPrefix(sent[0], "GET /api/accounts ") || strings.TrimSpace(sent[1]) != strings.TrimSpace(c.want) {
			t.Errorf("%v sent %q, want the accounts read, then %s", c.args, sent, c.want)
		}
	}

	// A refusal: the server's code and words, its field and what holds the
	// account, through the helper.
	stub.mu.Lock()
	stub.refusals = map[string]stubReply{
		"DELETE /api/accounts/a-aaaaaaaaaaaa": {http.StatusConflict, map[string]any{"error": map[string]any{"code": "account_in_use", "message": "the account is held", "names": []string{"s-1", "run r"}}}},
		"POST /api/accounts":                  {http.StatusBadRequest, map[string]any{"error": map[string]any{"code": "invalid_account", "message": "not a folder the server registers as it is spelt", "field": "folder"}}},
	}
	stub.mu.Unlock()
	if code, out := runCLI(t, run, hs.URL, "remove", "a-aaaaaaaaaaaa"); code != 1 || !strings.Contains(out, "account_in_use: the account is held; held by s-1, run r") || strings.Contains(out, "removed") {
		t.Errorf("a remove refused: %d %s", code, out)
	}
	if code, out := runCLI(t, run, hs.URL, "add", "--agent", "claude", "--label", "W", "--folder", "/x"); code != 1 || !strings.Contains(out, "invalid_account: not a folder the server registers as it is spelt (the field folder)") {
		t.Errorf("an add refused: %d %s", code, out)
	}

	// Usage errors ask nothing.
	before := len(stub.sent())
	for _, args := range [][]string{{"add", "--agent", "claude"}, {"rename", "a-aaaaaaaaaaaa"}, {"retire"}, {"default", "claude"}, {"list", "stray"}, {"forget"}} {
		if code, _ := runCLI(t, run, hs.URL, args...); code != 2 {
			t.Errorf("%v: exit %d, not a usage error", args, code)
		}
	}
	if n := len(stub.sent()); n != before {
		t.Errorf("usage errors made %d requests", n-before)
	}

	// A reply past the bound is refused before it is decoded.
	stub.mu.Lock()
	stub.big = true
	stub.mu.Unlock()
	if code, out := runCLI(t, run, hs.URL, "list"); code != 1 || !strings.Contains(out, "larger than 1 MiB") {
		t.Errorf("a reply past its bound: %d %s", code, out)
	}

	for _, req := range stub.sent() {
		if strings.Contains(req, dispatchToken) {
			t.Errorf("the token went in a request's path, query or body: %s", req)
		}
	}
}

// conductor capacity prints each state in words (one it does not know as
// one it does not know), a window not reported as "not reported", the
// provider's figures and this server's estimate apart; --json is the view
// as the server sent it; the listing follows next page by page and gives
// up past maxCapacityPages; --read posts a read.
func TestCapacityCommand(t *testing.T) {
	stub := &capacityStub{views: stubViews()}
	hs := stub.serve(t)
	run := capacityRun(t)
	code, out := runCLI(t, run, hs.URL)
	for _, want := range []string{
		"Claude A (claude-a) on the agent's own login (default)", "runs on this server (box)", "plan: max (as the tool reports it)",
		"Weekly, all models: 40% used, resets 2026-11-11 03:00 UTC", "5-hour: not reported", "extra_usage (the provider's): enabled no",
		"this server's count (an estimate)", "read 2026-11-10 20:00 UTC by Claude Code 2.0.0 (checked on 2.0.0); fresh",
		"Codex A (codex-a) on Work (a-bbbbbbbbbbbb)", "Primary: not reported", "Secondary: not reported", "credits (the provider's): balance 1250", "ordinary use allowed no", "stale",
		"not_installed: the agent's program is not installed on this server",
	} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("the listing lacks %q (exit %d):\n%s", want, code, out)
		}
	}
	if strings.Count(out, "% used") != 1 {
		t.Errorf("a window not reported shown as a share:\n%s", out)
	}
	if got := stub.sent(); len(got) != 2 || got[0] != "GET /api/capacity " || got[1] != "GET /api/capacity?after=codex-a%2Fa-bbbbbbbbbbbb " {
		t.Errorf("the listing asked %q", got)
	}

	code, out = runCLI(t, run, hs.URL, "--agent", "later")
	for _, want := range []string{"runs where this conductor does not know (elsewhere)", "a_later_state: a state this conductor does not know"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("a view of states this conductor does not know lacks %q:\n%s", want, out)
		}
	}

	code, out = runCLI(t, run, hs.URL, "--agent", "claude-a", "--json")
	var v, want any
	sentView, _ := json.Marshal(stub.views["claude-a"])
	json.Unmarshal(sentView, &want)
	if err := json.Unmarshal([]byte(out), &v); code != 0 || err != nil || !reflect.DeepEqual(v, want) {
		t.Fatalf("--json is not the view: %d %v %s", code, err, out)
	}
	code, out = runCLI(t, run, hs.URL, "--json")
	var page struct{ Views []capacity.View }
	if err := json.Unmarshal([]byte(out), &page); code != 0 || err != nil || len(page.Views) != 3 || page.Views[2].Agent.ID != "ghost" {
		t.Fatalf("the listing's --json: %d %v %s", code, err, out)
	}

	if code, out = runCLI(t, run, hs.URL, "--agent", "codex-a", "--read", "--check"); code != 0 || !strings.Contains(out, "Codex A") {
		t.Fatalf("--read: %d %s", code, out)
	}
	sent := stub.sent()
	if last := sent[len(sent)-1]; last != `POST /api/capacity/read {"agent":"codex-a","check":true}` {
		t.Fatalf("the read sent: %s", last)
	}
	if code, _ = runCLI(t, run, hs.URL, "--agent", "codex-a", "--account", "a-bbbbbbbbbbbb", "--model", "m"); code != 0 {
		t.Fatalf("--account --model: %d", code)
	}
	sent = stub.sent()
	if last := sent[len(sent)-1]; last != "GET /api/capacity?account=a-bbbbbbbbbbbb&agent=codex-a&model=m " {
		t.Fatalf("the view asked: %s", last)
	}
	if code, out = runCLI(t, run, hs.URL, "--agent", "nobody"); code != 1 || !strings.Contains(out, "not_found: no such agent") {
		t.Fatalf("an agent the server does not have: %d %s", code, out)
	}

	before := len(stub.sent())
	for _, args := range [][]string{{"--read"}, {"--check"}, {"--agent", "x", "--check"}, {"--agent", "x", "--read", "--model", "m"}, {"--account", "a-bbbbbbbbbbbb"}, {"stray"}} {
		if code, _ := runCLI(t, run, hs.URL, args...); code != 2 {
			t.Errorf("%v: exit %d, not a usage error", args, code)
		}
	}
	if n := len(stub.sent()); n != before {
		t.Errorf("usage errors made %d requests", n-before)
	}

	// A listing that never ends is given up after maxCapacityPages pages,
	// and nothing of it is printed.
	stub.mu.Lock()
	stub.endless = true
	stub.mu.Unlock()
	before = len(stub.sent())
	if code, out = runCLI(t, run, hs.URL); code != 1 || strings.Contains(out, "Ghost") || !strings.Contains(out, "longer than 64 pages") {
		t.Fatalf("an endless listing: %d %s", code, out)
	}
	if n := len(stub.sent()) - before; n != maxCapacityPages {
		t.Errorf("an endless listing asked %d pages, want %d", n, maxCapacityPages)
	}
}

// The commands against a real server, whose routes take what they take
// and nothing else: each write is taken, a refusal comes back in the
// server's words with its field, and a view of an agent with no reader
// is said in words. No agent's tool runs: the catalog's agents run
// /bin/cat, whose usage nothing reads.
func TestCapacityCommandsAgainstTheServer(t *testing.T) {
	a := newDispatchAPI(t)
	folder := filepath.Join(realTempDir(t), "login")
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	accounts, capacityCmd := accountsRun(t), capacityRun(t)
	must := func(run cliRun, want string, args ...string) string {
		t.Helper()
		code, out := runCLI(t, run, a.hs.URL, args...)
		if code != 0 || !strings.Contains(out, want) {
			t.Fatalf("%v: exit %d, want %q:\n%s", args, code, want, out)
		}
		return out
	}
	must(accounts, "Claude Code (claude): default default; its own login: never read", "list")
	out := must(accounts, "added a-", "add", "--agent", "claude", "--label", "Work", "--folder", folder)
	id := regexp.MustCompile(`a-[a-z2-7]{12}`).FindString(out)
	must(accounts, id+"  claude  Work  "+folder+"  never read", "list")
	must(accounts, "renamed "+id+" to Home office", "rename", id, "Home", "office")
	must(accounts, "the default of claude is "+id, "default", "claude", id)
	must(accounts, "Claude Code (claude): default "+id, "list")
	must(accounts, "the default of claude is default", "default", "claude", "default")
	must(accounts, "retired "+id, "retire", id)
	must(accounts, id+"  claude  Home office (retired)", "list")
	must(accounts, "removed "+id, "remove", id)
	if out := must(accounts, "Codex CLI (codex)", "list"); strings.Contains(out, id) {
		t.Errorf("a removed account listed:\n%s", out)
	}
	if code, out := runCLI(t, accounts, a.hs.URL, "add", "--agent", "claude", "--label", "W", "--folder", "relative"); code != 1 || !strings.Contains(out, "invalid_account: ") || !strings.Contains(out, "(the field folder)") {
		t.Errorf("a relative folder: exit %d:\n%s", code, out)
	}
	if code, out := runCLI(t, accounts, a.hs.URL, "retire", id); code != 1 || strings.Contains(out, "retired") {
		t.Errorf("retiring a removed account: exit %d:\n%s", code, out)
	}

	must(capacityCmd, "cat (cat) on the agent's own login (default)", "--agent", "cat")
	must(capacityCmd, "unsupported: "+capacity.StateWords(capacity.StateUnsupported), "--agent", "cat", "--read")
	out = must(capacityCmd, `"state": "unsupported"`, "--agent", "cat", "--json")
	var v capacity.View
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.Agent.ID != "cat" {
		t.Errorf("--json is not the view: %v %s", err, out)
	}
	must(capacityCmd, "", "--json")
	if code, out := runCLI(t, capacityCmd, a.hs.URL, "--agent", "nobody"); code != 1 || !strings.Contains(out, "not_found: no such agent") {
		t.Errorf("an agent the catalog does not have: exit %d:\n%s", code, out)
	}
}

// hostile is text that holds control characters of every kind a terminal
// reads: an escape sequence, an operating system command, a bell, C1's
// CSI and NEL, DEL, NUL, a backspace, a carriage return and a newline.
const hostile = "mark\x1b[31m\x1b]0;title\x07\u009b2J\u0085\x7f\x00\b\r\n\tend"

// fill sets every string v holds, however deep, to s (a map gets one entry
// keyed s), every bool to true and every number to 1, and makes every
// pointer and a slice of two; it answers how many strings it set. Times
// are a fixed one; a json.RawMessage is left empty for the caller.
func fill(v reflect.Value, s string) int {
	switch v.Kind() {
	case reflect.String:
		v.SetString(s)
		return 1
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64:
		v.SetInt(1)
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		return fill(v.Elem(), s)
	case reflect.Struct:
		if v.Type() == reflect.TypeFor[time.Time]() {
			v.Set(reflect.ValueOf(time.Date(2026, 11, 10, 20, 0, 0, 0, time.UTC)))
			return 0
		}
		n := 0
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				n += fill(v.Field(i), s)
			}
		}
		return n
	case reflect.Slice:
		if v.Type() == reflect.TypeFor[json.RawMessage]() {
			return 0
		}
		v.Set(reflect.MakeSlice(v.Type(), 2, 2))
		n := 0
		for i := range 2 {
			n += fill(v.Index(i), s)
		}
		return n
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		val := reflect.New(v.Type().Elem()).Elem()
		n := fill(val, s)
		m.SetMapIndex(reflect.ValueOf(s), val)
		v.Set(m)
		return n + 1
	default:
		panic("fill: a kind the replies' types do not hold: " + v.Kind().String())
	}
	return 0
}

// filled is a T with every string hostile.
func filled[T any](t *testing.T) T {
	var v T
	if fill(reflect.ValueOf(&v).Elem(), hostile) == 0 {
		t.Fatalf("%T holds no string", v)
	}
	return v
}

// clean fails t when text holds a control character other than a newline,
// or a byte that is not UTF-8.
func clean(t *testing.T, what, text string) {
	t.Helper()
	if !utf8.ValidString(text) {
		t.Errorf("%s printed a byte that is not UTF-8:\n%q", what, text)
	}
	for i, r := range text {
		if r != '\n' && unicode.IsControl(r) {
			t.Errorf("%s printed %U at byte %d:\n%q", what, r, i, text)
			return
		}
	}
}

// Every string of every reply these commands read (the accounts, a
// write's answer, a view and a page of them, a refusal), filled through
// reflection so a field added later is filled too, with text that holds
// control characters, reaches the terminal without one, in the text form,
// in --json (still the same JSON) and in a refusal's words.
func TestCapacityCommandsPrintNoControlCharacter(t *testing.T) {
	accounts := filled[listedAccounts](t)
	accounts.Owner = false // so its why is printed
	write := filled[accountWrite](t)
	view := filled[capacity.View](t)
	viewOK := filled[capacity.View](t) // the state whose figures are printed
	viewOK.State, viewOK.Host.Kind, viewOK.Agent.Adapter = capacity.StateOK, capacity.HostNative, "claude"
	refusal := filled[capacityRefusal](t)
	page := filled[capacityPageReply](t) // its next the same each time, so the walk ends
	for i, v := range []capacity.View{view, viewOK} {
		page.Views[i], _ = json.Marshal(v)
	}

	var refusing bool
	var mu sync.Mutex
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		refuse := refusing
		mu.Unlock()
		status, reply := http.StatusOK, any(nil)
		switch {
		case refuse:
			status, reply = http.StatusConflict, refusal
		case r.URL.Path == "/api/accounts" && r.Method == http.MethodGet:
			reply = accounts
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
			return
		case strings.HasPrefix(r.URL.Path, "/api/accounts"):
			reply = write
		case r.URL.Path == "/api/capacity" && r.URL.Query().Get("agent") == "":
			reply = page
		default:
			reply = viewOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(hs.Close)

	commands := []struct {
		run  cliRun
		args []string
	}{
		{accountsRun(t), []string{"list"}},
		{accountsRun(t), []string{"list", "--json"}},
		{accountsRun(t), []string{"add", "--agent", "claude", "--label", "W", "--folder", "/x"}},
		{accountsRun(t), []string{"rename", "a-aaaaaaaaaaaa", "W"}},
		{accountsRun(t), []string{"retire", "a-aaaaaaaaaaaa"}},
		{accountsRun(t), []string{"remove", hostile}},
		{accountsRun(t), []string{"default", hostile, "a-aaaaaaaaaaaa"}},
		{capacityRun(t), nil},
		{capacityRun(t), []string{"--json"}},
		{capacityRun(t), []string{"--agent", "a"}},
		{capacityRun(t), []string{"--agent", "a", "--json"}},
		{capacityRun(t), []string{"--agent", "a", "--read"}},
	}
	for _, refused := range []bool{false, true} {
		mu.Lock()
		refusing = refused
		mu.Unlock()
		for _, c := range commands {
			what := fmt.Sprintf("%v (refused %v)", c.args, refused)
			code, out := runCLI(t, c.run, hs.URL, c.args...)
			if want := map[bool]int{false: 0, true: 1}[refused]; code != want || !strings.Contains(out, "mark") {
				t.Errorf("%s: exit %d, want %d, with the reply's text:\n%q", what, code, want, out)
			}
			clean(t, what, out)
		}
	}

	// --json is the same JSON: its strings decode to what the server sent.
	mu.Lock()
	refusing = false
	mu.Unlock()
	_, out := runCLI(t, accountsRun(t), hs.URL, "list", "--json")
	var got listedAccounts
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Why != hostile || got.Accounts[1].Folder != hostile {
		t.Errorf("--json is not the reply: %v %q", err, out)
	}
}

// jsonText escapes what JSON may carry raw in a string and a terminal
// reads (DEL, C1, a byte that is not UTF-8) and keeps the JSON's meaning.
func TestJSONTextEscapesControls(t *testing.T) {
	raw := []byte("{\"a\":\"x\x7f\xc2\x9b2J\xc2\x85\x9b\u2028y\"}\r\n")
	text, err := jsonText(raw)
	if err != nil {
		t.Fatal(err)
	}
	clean(t, "jsonText", text)
	var got struct{ A string }
	if err := json.Unmarshal([]byte(text), &got); err != nil || got.A != "x\x7f\u009b2J\u0085\ufffd\u2028y" {
		t.Errorf("jsonText changed the JSON: %v %q from %q", err, got.A, text)
	}
	if _, err := jsonText([]byte("not json \x1b[2J")); err == nil || strings.ContainsRune(err.Error(), 0x1b) {
		t.Errorf("a reply that is not JSON: %v", err)
	}
}
