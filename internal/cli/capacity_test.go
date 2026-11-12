package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/api"
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
			Host: capacity.ViewHost{Kind: "elsewhere"}, State: "a_later_state", ReadAt: &read, Fresh: "a_later_freshness"},
	}
}

type cliRun func(args []string, out, errOut io.Writer) (int, error)

// runCmd runs a command with args and answers its exit, what it wrote on
// standard output and on standard error, and its error. Every test of
// the two commands runs them through here (runCLI too), so every command
// a test runs is checked to print the token nowhere: on neither stream
// and not in its error's text.
func runCmd(t *testing.T, run cliRun, args ...string) (int, string, string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	code, err := run(args, &out, &errb)
	noToken(t, dispatchToken, out.String(), errb.String(), err)
	return code, out.String(), errb.String(), err
}

// runCLI runs a command against url with the token, through runCmd, and
// answers its exit and all it printed, its error's text last (what
// conductor prints on stderr).
func runCLI(t *testing.T, run cliRun, url string, args ...string) (int, string) {
	t.Helper()
	code, out, errOut, err := runCmd(t, run, append(args, "--server", url, "--token", dispatchToken)...)
	if err != nil && code == 0 {
		t.Fatalf("%v: %v", args, err)
	}
	text := out + errOut
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
	for _, want := range []string{"runs where this conductor does not know (elsewhere)", "a_later_state: a state this conductor does not know",
		"read 2026-11-10 20:00 UTC; a freshness this conductor does not know (a_later_freshness)"} {
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

// hostile is text that holds characters of every kind that is not printed:
// an escape sequence, an operating system command, a bell, C1's CSI and
// NEL, DEL, NUL, a backspace, a carriage return and a newline, a
// bidirectional override, a zero-width space and a private-use character.
// It begins with mark and ends with kram, so a line that holds one and
// not the other was broken by its newline.
const hostile = "mark\x1b[31m\x1b]0;title\x07\u009b2J\u0085\x7f\x00\b\r\n\t" + string(rune(0x202e)) + string(rune(0x200b)) + string(rune(0xe000)) + "kram"

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

// clean fails t when text holds a character that is not printed, other
// than a newline, or a byte that is not UTF-8, or a line that holds a
// mark without its kram or a kram without its mark: hostile's newline
// broke the line it was printed on.
func clean(t *testing.T, what, text string) {
	t.Helper()
	if !utf8.ValidString(text) {
		t.Errorf("%s printed a byte that is not UTF-8:\n%q", what, text)
	}
	for i, r := range text {
		if r != '\n' && !printed(r) {
			t.Errorf("%s printed %U at byte %d:\n%q", what, r, i, text)
			return
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.Count(line, "mark") != strings.Count(line, "kram") {
			t.Errorf("%s broke a line at a string's newline:\n%q", what, text)
			return
		}
	}
}

// jsonTree is v as encoding/json decodes it into an any.
func jsonTree(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	var tree any
	if err != nil || json.Unmarshal(b, &tree) != nil {
		t.Fatalf("%T: %v", v, err)
	}
	return tree
}

// cloneTree is a deep copy of a jsonTree.
func cloneTree(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = cloneTree(e)
		}
		return m
	case []any:
		s := make([]any, len(x))
		for i, e := range x {
			s[i] = cloneTree(e)
		}
		return s
	}
	return v
}

// isString reports whether v is a string of the reply's types: a time is
// a string in JSON and none in Go, and is never empty.
func isString(v any) bool {
	s, ok := v.(string)
	_, err := time.Parse(time.RFC3339, s)
	return ok && err != nil
}

// emptied is a copy of v with every string empty.
func emptied(v any) any {
	switch x := v.(type) {
	case string:
		if isString(x) {
			return ""
		}
		return x
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = emptied(e)
		}
		return m
	case []any:
		s := make([]any, len(x))
		for i, e := range x {
			s[i] = emptied(e)
		}
		return s
	}
	return v
}

// members answers the path of every member of every object v holds,
// however deep: a key of an object, or an index of an array, a step.
func members(v any, at []any) [][]any {
	var out [][]any
	switch x := v.(type) {
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(x)) {
			p := append(slices.Clip(at), k)
			out = append(append(out, p), members(x[k], p)...)
		}
	case []any:
		for i, e := range x {
			out = append(out, members(e, append(slices.Clip(at), i))...)
		}
	}
	return out
}

// holder is the object in v that holds the member at path.
func holder(v any, path []any) map[string]any {
	for _, step := range path[:len(path)-1] {
		if i, ok := step.(int); ok {
			v = v.([]any)[i]
		} else {
			v = v.(map[string]any)[step.(string)]
		}
	}
	return v.(map[string]any)
}

type replyVariant struct {
	name string
	tree any
}

// variants are the ways a reply is given: every string filled, every
// string empty, every member absent, and then each member alone empty
// (a string) and absent, the rest filled.
func variants(filled any) []replyVariant {
	out := []replyVariant{{"filled", filled}, {"every string empty", emptied(filled)}, {"every member absent", map[string]any{}}}
	for _, p := range members(filled, nil) {
		key := p[len(p)-1].(string)
		if isString(holder(filled, p)[key]) {
			v := cloneTree(filled)
			holder(v, p)[key] = ""
			out = append(out, replyVariant{fmt.Sprintf("%v empty", p), v})
		}
		v := cloneTree(filled)
		delete(holder(v, p), key)
		out = append(out, replyVariant{fmt.Sprintf("%v absent", p), v})
	}
	return out
}

// Every string of every reply these commands read (the accounts, a
// write's answer, a view and a page of them, a refusal), filled through
// reflection so a field added later is filled too, with text that holds
// characters that are not printed, reaches the terminal as printed
// characters on the line it was printed on, in the text form, in --json
// (still the same JSON) and in a refusal's words. Each reply is given
// every way a string can take: filled, empty and absent, each member
// alone and all together, so every branch a string decides is run.
func TestCapacityCommandsPrintNoControlCharacter(t *testing.T) {
	accounts := filled[listedAccounts](t)
	accounts.Owner = false // so its why is printed
	view := filled[capacity.View](t)
	viewOK := filled[capacity.View](t) // the state whose figures are printed
	viewOK.State, viewOK.Host.Kind, viewOK.Agent.Adapter = capacity.StateOK, capacity.HostNative, "claude"
	page := filled[capacityPageReply](t) // a next filled too: its page names none
	for i, v := range []capacity.View{view, viewOK} {
		page.Views[i], _ = json.Marshal(v)
	}
	replies := map[string]any{
		"accounts": jsonTree(t, accounts), "write": jsonTree(t, filled[accountWrite](t)), "view": jsonTree(t, viewOK),
		"page": jsonTree(t, page), "refusal": jsonTree(t, filled[capacityRefusal](t)),
	}

	var refusing bool
	var mu sync.Mutex
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		status, reply := http.StatusOK, any(nil)
		switch {
		case refusing:
			status, reply = http.StatusConflict, replies["refusal"]
		case r.URL.Path == "/api/accounts" && r.Method == http.MethodGet:
			reply = replies["accounts"]
		case r.Method == http.MethodDelete:
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		case strings.HasPrefix(r.URL.Path, "/api/accounts"):
			reply = replies["write"]
		case r.URL.Path == "/api/capacity" && r.URL.Query().Get("agent") == "":
			reply = cloneTree(replies["page"])
			if m, ok := reply.(map[string]any); ok && r.URL.Query().Get("after") != "" {
				delete(m, "next") // the page after the first ends the walk
			}
		default:
			reply = replies["view"]
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(hs.Close)

	type command struct {
		run  cliRun
		args []string
	}
	list, listJSON := command{accountsRun(t), []string{"list"}}, command{accountsRun(t), []string{"list", "--json"}}
	writes := []command{
		{accountsRun(t), []string{"add", "--agent", "claude", "--label", "W", "--folder", "/x"}},
		{accountsRun(t), []string{"rename", "a-aaaaaaaaaaaa", "W"}},
		{accountsRun(t), []string{"retire", "a-aaaaaaaaaaaa"}},
		{accountsRun(t), []string{"default", hostile, "a-aaaaaaaaaaaa"}},
	}
	views := []command{{capacityRun(t), []string{"--agent", "a"}}, {capacityRun(t), []string{"--agent", "a", "--json"}}, {capacityRun(t), []string{"--agent", "a", "--read"}}}
	listing := []command{{capacityRun(t), nil}, {capacityRun(t), []string{"--json"}}}
	every := append(append(append([]command{list, listJSON, {accountsRun(t), []string{"remove", hostile}}}, writes...), views...), listing...)
	readers := map[string][]command{"accounts": {list, listJSON}, "write": writes, "view": views, "page": listing, "refusal": every}

	runs := 0
	for _, reply := range slices.Sorted(maps.Keys(readers)) {
		base := replies[reply]
		for _, v := range variants(base) {
			mu.Lock()
			replies[reply], refusing = v.tree, reply == "refusal"
			mu.Unlock()
			for _, c := range readers[reply] {
				what := fmt.Sprintf("%q", fmt.Sprintf("%v, the %s %s", c.args, reply, v.name))
				code, out := runCLI(t, c.run, hs.URL, c.args...)
				runs++
				if want := map[bool]int{false: 0, true: 1}[reply == "refusal"]; code != want {
					t.Errorf("%s: exit %d, want %d:\n%q", what, code, want, out)
				}
				if v.name == "filled" && !strings.Contains(out, "mark") {
					t.Errorf("%s: without the reply's text:\n%q", what, out)
				}
				clean(t, what, out)
			}
		}
		mu.Lock()
		replies[reply] = base
		mu.Unlock()
	}
	if runs < 1000 {
		t.Errorf("only %d runs: a reply's members were not walked", runs)
	}
	t.Logf("%d runs", runs)

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

// What the two commands write that is not a reply (the flag parser's
// words and usage, a usage error that names an argument, the client's
// set-up refusal and warning that name the server) holds printed
// characters alone, on either stream, as their error does.
func TestCapacityCommandsArgumentsPrintOnlyPrinted(t *testing.T) {
	clearConductorEnv(t)
	evil := "\x1b[31m\u009b2J\u202e"
	host := "http://ho\u009b\u202est.example:1" // plain http off the machine
	cancelled, cancel := context.WithCancel(t.Context())
	cancel() // a request is never sent: the client's set-up is all that runs
	for _, c := range []struct {
		accounts bool
		args     []string
		code     int
	}{
		{false, []string{"--bad" + evil}, 2},
		{true, []string{"list", "--bad" + evil}, 2},
		{true, []string{"forget" + evil}, 2},
		{false, []string{"stray" + evil}, 2},
		{false, []string{"--server", "http://h\x1bst" + evil}, 2},
		{false, []string{"--server", host}, 2},
		{true, []string{"list", "--server", host}, 2},
		{false, []string{"--server", host, "--insecure"}, 1},
		{true, []string{"remove", "a" + evil, "--server", host, "--insecure"}, 1},
	} {
		command := runCapacity
		if c.accounts {
			command = runAccounts
		}
		run := func(args []string, out, errOut io.Writer) (int, error) { return command(cancelled, args, out, errOut) }
		code, out, errOut, err := runCmd(t, run, append(c.args, "--token", dispatchToken)...)
		what := fmt.Sprintf("%q", c.args)
		if code != c.code || err == nil {
			t.Errorf("%s: exit %d %v, want %d with an error", what, code, err, c.code)
		}
		clean(t, what+" on stdout", out)
		clean(t, what+" on stderr", errOut)
		if err != nil {
			clean(t, what+" as its error", err.Error())
		}
	}
}

// The two commands write through printedOnly and nothing else: neither
// names a raw stream, each runs its command through printedOnly alone,
// handing it the two streams and naming them nowhere else, and the
// commands are run from nowhere but there.
func TestCapacityCommandsWriteThroughOneWriter(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string][]string{} // accountsCmd and capacityCmd: the functions that call them
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.SelectorExpr:
					if id, ok := x.X.(*ast.Ident); ok && id.Name == "os" && (x.Sel.Name == "Stdout" || x.Sel.Name == "Stderr") && name == "capacity.go" {
						t.Errorf("%s names os.%s", fn.Name.Name, x.Sel.Name)
					}
				case *ast.CallExpr:
					if id, ok := x.Fun.(*ast.Ident); ok && (id.Name == "accountsCmd" || id.Name == "capacityCmd") {
						calls[id.Name] = append(calls[id.Name], fn.Name.Name)
					}
				}
				return true
			})
			if name != "capacity.go" || (fn.Name.Name != "runAccounts" && fn.Name.Name != "runCapacity") {
				continue
			}
			ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
			if len(fn.Body.List) != 1 || !ok || len(ret.Results) != 1 {
				t.Errorf("%s is not one return of printedOnly", fn.Name.Name)
				continue
			}
			call, ok := ret.Results[0].(*ast.CallExpr)
			if id, isID := call.Fun.(*ast.Ident); !ok || !isID || id.Name != "printedOnly" || len(call.Args) != 3 {
				t.Errorf("%s does not return printedOnly's answer", fn.Name.Name)
				continue
			}
			uses := map[string]int{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok {
					uses[id.Name]++
				}
				return true
			})
			a, aok := call.Args[0].(*ast.Ident)
			b, bok := call.Args[1].(*ast.Ident)
			if !aok || !bok || a.Name != "stdout" || b.Name != "stderr" || uses["stdout"] != 1 || uses["stderr"] != 1 {
				t.Errorf("%s names its raw streams other than as printedOnly's: %v", fn.Name.Name, uses)
			}
		}
	}
	if !slices.Equal(calls["accountsCmd"], []string{"runAccounts"}) || !slices.Equal(calls["capacityCmd"], []string{"runCapacity"}) {
		t.Errorf("the commands are run from %v, not through printedOnly alone", calls)
	}
}

// jsonText keeps every value it is given: each string, written raw into
// the JSON as a server may (encodeJSON) or escaped as encoding/json
// writes it, indented or compact, parses back to the same string, the
// text holding printed characters and the indent's newlines alone; what
// is not printed is written as its JSON escape (a character past the
// basic plane as its surrogate pair), never replaced.
func TestJSONTextKeepsEveryValue(t *testing.T) {
	for _, s := range []string{
		"a\u200db", "\U0001F469\u200d\U0001F4BB", "\u200c", "\u202eabc\u202c", "\u05e9\u05dc\u05d5\u05dd", "a\u2028b\u2029c",
		"\x7f", "\u0080\u0085\u009b2J\u009f", "\ufffd", string(rune(0xd800)), "\U0001F600\U000e0001\U0010fffd", "\x1b[31m\x00\t\r\n",
		"\u00a0\u3000\u00ad\ufeff", "<&>", hostile,
	} {
		raw, _ := encodeJSON(map[string]string{"a": s})
		escaped, _ := json.Marshal(map[string]string{"a": s})
		for _, in := range [][]byte{raw, escaped} {
			for _, indent := range []bool{true, false} {
				text, err := jsonText(append(in, "\r\n"...), indent)
				var got map[string]string
				if err != nil || json.Unmarshal([]byte(text), &got) != nil || got["a"] != s {
					t.Errorf("jsonText(%q, %v) = %q %v: not the value %q", in, indent, text, err, s)
				}
				clean(t, fmt.Sprintf("jsonText of %q", s), text)
				if strings.Contains(text, "\n") != indent {
					t.Errorf("indent %v: %q", indent, text)
				}
			}
		}
	}
	if text, err := jsonText([]byte("{\"a\":\"x\x9by\"}"), false); err != nil || text != `{"a":"x\ufffdy"}` {
		t.Errorf("a byte that is not UTF-8 (which encoding/json reads as U+FFFD): %q %v", text, err)
	}
	if _, err := jsonText([]byte("not json \x1b[2J"), true); err == nil || strings.ContainsRune(err.Error(), 0x1b) {
		t.Errorf("a reply that is not JSON: %v", err)
	}
}

// A character the two commands print is one of a letter, a mark, a
// number, punctuation or a symbol, by its category, or the space U+0020:
// one character of every category the standard library tells, and
// characters not assigned, are printed or not as their category says;
// the commands' writer replaces each one that is not, and jsonText
// escapes it and keeps its value.
func TestPrintedIsByCategory(t *testing.T) {
	seen := 0
	check := func(what string, r rune, want bool) {
		t.Helper()
		if printed(r) != want {
			t.Errorf("%s %U: printed %v, want %v", what, r, printed(r), want)
		}
		if unicode.Is(unicode.Cs, r) {
			return // no string holds a surrogate
		}
		s := "a" + string(r) + "b"
		var out bytes.Buffer
		printedOnly(&out, io.Discard, func(w, _ io.Writer) (int, error) { _, err := fmt.Fprintln(w, s); return 0, err })
		if got, tab := strings.TrimSuffix(out.String(), "\n"), r == '\t' || r == '\r'; (got == s) != want || (!want && !tab && got != "a\ufffdb") || (tab && got != "a b") {
			t.Errorf("%s %U: the commands' writer gives %q", what, r, got)
		}
		b, _ := json.Marshal(map[string]string{"a": string(r)})
		text, err := jsonText(b, false)
		var back map[string]string
		if err != nil || json.Unmarshal([]byte(text), &back) != nil || back["a"] != string(r) || (!want && strings.ContainsRune(text, r)) {
			t.Errorf("%s %U: jsonText gives %q %v", what, r, text, err)
		}
		seen++
	}
	for _, name := range slices.Sorted(maps.Keys(unicode.Categories)) {
		if len(name) != 2 {
			continue // a major category: its subcategories are walked
		}
		table := unicode.Categories[name]
		r := rune(0)
		if len(table.R16) > 0 {
			r = rune(table.R16[0].Lo)
		} else {
			r = rune(table.R32[0].Lo)
		}
		check(name, r, strings.ContainsRune("LMNPS", rune(name[0])) || r == ' ')
	}
	for r, want := range map[rune]bool{
		0x0378: false, 0x086f: false, 0x10fffe: false, 0x10ffff: false, // not assigned
		0x09: false, 0x0d: false, 0x1d: false, 0x7f: false, 0x9b: false, 0x200b: false, 0x200c: false, 0x200d: false, 0x202e: false, 0xfeff: false, 0xad: false, 0xe0001: false,
		0xa0: false, 0x3000: false, 0x2028: false, 0x2029: false, // separators other than the space
		0x20: true, 0x2014: true, 0x1f600: true, 0x0301: true,
	} {
		check("by hand", r, want)
	}
	if seen < 30 {
		t.Errorf("only %d categories walked", seen)
	}
}

// Every JSON the package prints goes through jsonText. What this check
// reads is the source's calls, by their shape: no function but jsonText
// calls json.Indent, json.MarshalIndent or json.Compact, none but
// encodeJSON makes a json.NewEncoder, no call writes x.Write(append(...)),
// and no fmt.Fprint* is handed a string(...) argument, the shapes the
// package's printers once had. It follows no value through a variable: a
// JSON reply held as a string and written with io.WriteString is not one
// of its shapes, and brakeman observe's, the one the package had, is held
// by its own test (TestObservePrintsALogsBytesAsTheyCame).
func TestEveryJSONGoesThroughJSONText(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, _ := sel.X.(*ast.Ident)
				at := fset.Position(call.Pos()).String()
				switch {
				case pkg != nil && pkg.Name == "json" && slices.Contains([]string{"Indent", "MarshalIndent", "Compact"}, sel.Sel.Name) && fn.Name.Name != "jsonText":
					t.Errorf("%s: %s calls json.%s: print JSON through jsonText", at, fn.Name.Name, sel.Sel.Name)
				case pkg != nil && pkg.Name == "json" && sel.Sel.Name == "NewEncoder" && fn.Name.Name != "encodeJSON":
					t.Errorf("%s: %s makes a JSON encoder", at, fn.Name.Name)
				case sel.Sel.Name == "Write" && len(call.Args) == 1:
					if inner, ok := call.Args[0].(*ast.CallExpr); ok {
						if id, ok := inner.Fun.(*ast.Ident); ok && id.Name == "append" {
							t.Errorf("%s: %s writes a byte slice onto a stream", at, fn.Name.Name)
						}
					}
				case pkg != nil && pkg.Name == "fmt" && strings.HasPrefix(sel.Sel.Name, "Fprint"):
					for _, a := range call.Args[1:] {
						if conv, ok := a.(*ast.CallExpr); ok {
							if id, ok := conv.Fun.(*ast.Ident); ok && id.Name == "string" {
								t.Errorf("%s: %s prints a byte slice as it is", at, fn.Name.Name)
							}
						}
					}
				}
				return true
			})
		}
	}
}

// The listing's walk ends by itself: a next that names a page already
// read, at once or after three pages, is refused whole, as is a page past
// the route's bound on one page by the white space after its value; 64
// pages each just inside it are read whole.
func TestCapacityListingEndsByItself(t *testing.T) {
	view, _ := json.Marshal(stubViews()["ghost"])
	listing := func(t *testing.T, page func(n int, after string, w http.ResponseWriter, r *http.Request)) (int, string, int) {
		var mu sync.Mutex
		n := 0
		hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			n++
			at := n
			mu.Unlock()
			page(at, r.URL.Query().Get("after"), w, r)
		}))
		t.Cleanup(hs.Close)
		code, out := runCLI(t, capacityRun(t), hs.URL)
		mu.Lock()
		defer mu.Unlock()
		return code, out, n
	}
	answer := func(w http.ResponseWriter, next string, pad int) {
		json.NewEncoder(w).Encode(map[string]any{"views": []json.RawMessage{view}, "next": next, "pad": strings.Repeat("x", pad)})
	}
	for _, c := range []struct {
		what  string
		nexts []string
		pages int
	}{
		{"a next that repeats at once", []string{"x", "x"}, 2},
		{"a next that cycles after three pages", []string{"a", "b", "c", "a"}, 4},
	} {
		code, out, n := listing(t, func(at int, _ string, w http.ResponseWriter, _ *http.Request) { answer(w, c.nexts[at-1], 0) })
		if code != 1 || n != c.pages || strings.Contains(out, "Ghost") || !strings.Contains(out, "the server's listing does not move on") {
			t.Errorf("%s: exit %d after %d pages:\n%s", c.what, code, n, out)
		}
	}

	// A page is read through the route's bound on one page, counted on its
	// bytes as they arrive, the white space around its value included: so
	// the 64 pages are 8 MiB at most.
	page := func(at, last int, pad string) []byte {
		next := fmt.Sprintf("k%d", at)
		if at == last {
			next = ""
		}
		b, _ := json.Marshal(map[string]any{"views": []json.RawMessage{view}, "next": next})
		return append(b, pad[:max(0, len(pad)-len(b))]...)
	}
	inside, past := strings.Repeat(" ", maxListingPage), strings.Repeat(" ", maxListingPage+1)
	code, out, n := listing(t, func(at int, _ string, w http.ResponseWriter, _ *http.Request) { w.Write(page(at, 3, past)) })
	if code != 1 || n != 1 || strings.Contains(out, "Ghost") || !strings.Contains(out, "larger than 0.125 MiB") {
		t.Errorf("a page padded past its bound outside its value: exit %d after %d pages:\n%.300s", code, n, out)
	}
	code, out, n = listing(t, func(at int, _ string, w http.ResponseWriter, _ *http.Request) {
		w.Write(page(at, maxCapacityPages, inside))
	})
	if code != 0 || n != maxCapacityPages || strings.Count(out, "Ghost (ghost)") != maxCapacityPages {
		t.Errorf("%d pages each just inside the page bound: exit %d after %d pages:\n%.300s", maxCapacityPages, code, n, out)
	}
}

// listingClock is a clock the test owns for the listing's deadline: each
// deadline set (listingDeadline) is the clock's time then plus
// listingTimeout, and passes when the test moves the clock to it.
type listingClock struct {
	mu        sync.Mutex
	now       time.Duration
	deadlines []listingClockDeadline
}

type listingClockDeadline struct {
	at     time.Duration
	ctx    context.Context
	cancel context.CancelCauseFunc
}

func (c *listingClock) deadline(ctx context.Context) (context.Context, context.CancelFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	dctx, cancel := context.WithCancelCause(ctx)
	c.deadlines = append(c.deadlines, listingClockDeadline{c.now + listingTimeout, dctx, cancel})
	return dctx, func() { cancel(context.Canceled) }
}

// advance moves the clock on by d, passing every deadline it reaches, and
// answers whether the last one set, the one the request in flight is
// under, passed now.
func (c *listingClock) advance(d time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now += d
	passed := false
	for i, dl := range c.deadlines {
		if dl.at <= c.now && dl.ctx.Err() == nil {
			dl.cancel(context.DeadlineExceeded)
			passed = i == len(c.deadlines)-1
		}
	}
	return passed
}

// The listing's walk has one deadline, set at its start, for all its
// pages, and the test owns its clock: a stated number of pages answer,
// then the deadline passes while a page is held, and the walk is refused.
// Each page moving the clock on by less than the deadline, as a deadline
// set again for each page would let through, is refused as well, at the
// page where the walk's own time passes it.
func TestCapacityListingHasOneDeadline(t *testing.T) {
	view, _ := json.Marshal(stubViews()["ghost"])
	oldRequest, oldDeadline := requestTimeout, listingDeadline
	t.Cleanup(func() { requestTimeout, listingDeadline = oldRequest, oldDeadline })
	requestTimeout = time.Hour // a page's own bound is never the one that ends it
	for _, c := range []struct {
		what        string
		step        func(at int) time.Duration // how far the clock moves at page at
		pages, held int
	}{
		{"three pages answer, then the deadline passes while the fourth is held", func(at int) time.Duration {
			return map[bool]time.Duration{true: listingTimeout}[at == 4]
		}, 6, 4},
		{"each page takes four tenths of the deadline", func(int) time.Duration { return listingTimeout * 4 / 10 }, 6, 3},
	} {
		clock := &listingClock{}
		listingDeadline = clock.deadline
		var mu sync.Mutex
		n := 0
		hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			n++
			at := n
			mu.Unlock()
			if clock.advance(c.step(at)) {
				<-r.Context().Done() // held: the walk's deadline ends it
				return
			}
			next := fmt.Sprintf("k%d", at)
			if at == c.pages {
				next = ""
			}
			json.NewEncoder(w).Encode(map[string]any{"views": []json.RawMessage{view}, "next": next})
		}))
		code, out := runCLI(t, capacityRun(t), hs.URL)
		hs.Close()
		mu.Lock()
		got := n
		mu.Unlock()
		if code != 1 || got != c.held || strings.Contains(out, "Ghost") || !strings.Contains(out, "the listing takes longer than "+listingTimeout.String()) {
			t.Errorf("%s: exit %d after %d pages, want the refusal at page %d:\n%s", c.what, code, got, c.held, out)
		}
	}
}

// The guide's CLI forms of the accounts and capacity operations are
// commands as written: each, its placeholders given values, runs through
// the commands' own parser against a stub of the routes and exits 0, and
// each placeholder is one its command's usage names. (The guide's other
// rows have no test of this kind: this one is for these eight rows.)
func TestTheCapacityOperationsCLIFormsAreCommands(t *testing.T) {
	clearConductorEnv(t)
	hs := (&capacityStub{views: stubViews()}).serve(t)
	values := map[string]string{"ADAPTER": "claude", "WORDS": "Work", "DIR": "/srv/work", "ID": "a-aaaaaaaaaaaa"}
	root := func(args []string, out, errOut io.Writer) (int, error) {
		return Run(t.Context(), args, strings.NewReader(""), out, errOut)
	}
	rows := 0
	for _, op := range api.Operations() {
		if !strings.HasPrefix(op.Name, "accounts_") && !strings.HasPrefix(op.Name, "capacity_") {
			continue
		}
		rows++
		if len(op.CLI) < 2 || op.CLI[0] != "conductor" {
			t.Errorf("%s: no CLI form: %q", op.Name, op.CLI)
			continue
		}
		usage := map[string]string{"accounts": accountsUsage, "capacity": capacityUsage}[op.CLI[1]]
		args := slices.Clone(op.CLI[1:])
		for i, w := range args {
			if w != strings.ToUpper(w) || strings.HasPrefix(w, "-") {
				continue
			}
			v, ok := values[w]
			if i > 0 && args[i-1] == "--agent" {
				v = "claude-a" // an agent of the stub's
			}
			if !ok || !strings.Contains(usage, " "+w) {
				t.Errorf("%s: the placeholder %s is not one the usage names", op.Name, w)
			}
			args[i] = v
		}
		if code, out := runCLI(t, root, hs.URL, args...); code != 0 {
			t.Errorf("%s: %q is not a command as written: exit %d\n%s", op.Name, op.CLI, code, out)
		}
	}
	if rows != 8 {
		t.Errorf("%d accounts and capacity rows, want 8", rows)
	}
}
