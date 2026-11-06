package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// conductor task decide, decision and withdraw, and conductor decision
// (docs/plans/decisions.md, PR-6): against a real server with a managed
// run whose one attempt asks, and against a stand-in for a page at its
// bound.

// decisionAPI is a plannedAPI whose crew is launched, its task's first
// attempt running: the run, and the attempt's session and token, which
// the test's environment names (inSessionOf).
type decisionAPI struct {
	*plannedAPI
	run, sid, token string
}

// newDecisionAPI launches the planned crew and waits for its attempt.
func newDecisionAPI(t *testing.T) *decisionAPI {
	t.Helper()
	a := newPlannedAPI(t)
	run := a.admin(t, "POST", "/api/crews/"+a.crew+"/launch", nil, http.StatusCreated)["run"].(map[string]any)["id"].(string)
	sid, token := a.memberSession(t, run)
	d := &decisionAPI{plannedAPI: a, run: run, sid: sid, token: token}
	d.until(t, "build's attempt running", func() bool {
		tasks, _ := a.admin(t, "GET", "/api/runs/"+run, nil, http.StatusOK)["run"].(map[string]any)["tasks"].([]any)
		for _, x := range tasks {
			task, _ := x.(map[string]any)
			at, _ := task["attempt"].(map[string]any)
			if task["id"] == "build" && at != nil && at["state"] == "running" && at["sessionId"] == sid {
				return true
			}
		}
		return false
	})
	inSessionOf(t, a.hs.URL, sid, token)
	return d
}

// until polls ok until it holds, as long as the test may take.
func (d *decisionAPI) until(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for !ok() {
		if t.Context().Err() != nil {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// decision reads a decision through the admin's route.
func (d *decisionAPI) decision(t *testing.T, id string) map[string]any {
	t.Helper()
	return d.admin(t, "GET", "/api/decisions/"+id, nil, http.StatusOK)["decision"].(map[string]any)
}

// decisionCmd runs conductor decision against the server, its server and
// token given as flags after the command, the rest after them.
func (d *decisionAPI) decisionCmd(t *testing.T, sub string, args ...string) (int, string, string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	full := append([]string{sub, "--server", d.hs.URL, "--token", dispatchToken}, args...)
	code, err := runDecision(t.Context(), full, &out, &errb)
	return code, out.String(), errb.String(), err
}

// mustTask runs conductor task and fails the test unless it exits 0.
func mustTask(t *testing.T, args ...string) string {
	t.Helper()
	code, out, err := task(t, "", args...)
	if code != 0 || err != nil {
		t.Fatalf("task %q: exit %d %v", args, code, err)
	}
	return out
}

var opIDShape = regexp.MustCompile(`^[0-9a-f]{16}$`)

// task decide sends the request its flags or its file make, with a
// requestId it mints, once more with the same id when the reply is lost
// (one decision made); it answers at once with the id, open and what to
// do while it waits. task decision lists the task's decisions newest
// first and reads one whole, its options numbered as an answer names
// them; --json prints them as JSON. Usage errors send nothing; the
// server's refusal is its words and exit 1.
func TestTaskDecideAndRead(t *testing.T) {
	d := newDecisionAPI(t)
	posts := "/api/sessions/" + d.sid + "/decisions"
	before := d.count()
	out := mustTask(t, "decide", "--type", "approval", "--question", "Ship it?", "--context", "The tests pass.\nThe docs do not.",
		"--option", "Ship: now, the docs after", "--option", "Hold", "--recommend", "0", "--uncertainty", "the docs", "--authorizes", "the deploy",
		"--evidence", "task:"+d.run+"/build", "--subject", "commit")
	if !strings.HasPrefix(out, "dr-1 open. Pending. ") || !hasAll(out, "do not submit your result until it is answered", "'Decision dr-1 … answered'", "save one before you end your turn") {
		t.Fatalf("decide: %q", out)
	}
	sent := d.posts(before, posts)
	if len(sent) != 1 {
		t.Fatalf("decide's posts: %v", sent)
	}
	b := sent[0].body
	opts, _ := b["options"].([]any)
	if !opIDShape.MatchString(fmt.Sprint(b["requestId"])) || b["type"] != "approval" || b["question"] != "Ship it?" || len(opts) != 2 ||
		opts[0].(map[string]any)["label"] != "Ship" || opts[0].(map[string]any)["tradeoffs"] != "now, the docs after" || opts[1].(map[string]any)["tradeoffs"] != nil ||
		b["recommendation"] != 0.0 || b["authorizes"] != "the deploy" || b["subject"].(map[string]any)["kind"] != "commit" || fmt.Sprint(b["evidence"]) != "[task:"+d.run+"/build]" {
		t.Fatalf("decide's body: %v", b)
	}

	// A request from a file, its reply lost once: sent again with the
	// same id, one decision made.
	file := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(file, []byte(`{"type":"clarification","question":"Which schema?","options":[{"label":"v1"},{"label":"v2"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before = d.count()
	d.loseNext("POST", posts, 1)
	if out := mustTask(t, "decide", "--file", file); !strings.HasPrefix(out, "dr-2 open. ") {
		t.Fatalf("decide --file: %q", out)
	}
	if sent := d.posts(before, posts); len(sent) != 2 || sent[0].reply != -1 || sent[0].body["requestId"] != sent[1].body["requestId"] || !opIDShape.MatchString(fmt.Sprint(sent[0].body["requestId"])) {
		t.Fatalf("a lost reply's posts: %v", sent)
	}

	// The list, newest first; one whole; JSON.
	if out, want := mustTask(t, "decision"), "dr-2 (clarification, open, rev 1): Which schema?\ndr-1 (approval, open, rev 1): Ship it?\n"; out != want {
		t.Fatalf("decision:\n%q\nwant\n%q", out, want)
	}
	out = mustTask(t, "decision", "dr-1")
	if !hasAll(out, "dr-1: approval, open, rev 1\n", "asked by: task build's attempt 1, live, in run "+d.run+"\n", "question: Ship it?\n", "context:\n  The tests pass.\n  The docs do not.\n",
		"options (answer with the number):\n  0. Ship: now, the docs after (recommended)\n  1. Hold\n", "uncertainty: the docs\n", "authorizes: the deploy\n",
		"subject: commit "+d.head+"\n", "evidence:\n  task:"+d.run+"/build\n") {
		t.Fatalf("decision dr-1:\n%s", out)
	}
	var list []map[string]any
	if err := json.Unmarshal([]byte(mustTask(t, "decision", "--json")), &list); err != nil || len(list) != 2 || list[0]["id"] != "dr-2" {
		t.Fatalf("decision --json: %v %v", list, err)
	}
	var one map[string]any
	if err := json.Unmarshal([]byte(mustTask(t, "decision", "dr-2", "--json")), &one); err != nil || one["id"] != "dr-2" || one["type"] != "clarification" {
		t.Fatalf("decision dr-2 --json: %v %v", one, err)
	}

	// Usage errors: exit 2, nothing sent.
	before = d.count()
	for _, args := range [][]string{
		{"decide", "--question", "No type?"},
		{"decide", "--type", "approval"},
		{"decide", "--file", file, "--type", "approval"},
		{"decide", "--type", "approval", "--question", "Q", "--option", ": no label"},
		{"decide", "--type", "approval", "--question", "Q", "--option", "a", "--option", "b", "--recommend", "2"},
		{"decide", "--type", "approval", "--question", "Q", "--subject", "candidate:" + d.run + "/build/1"},
		{"decide", "--type", "approval", "--question", "Q", "--subject", "branch"},
		{"decide", "--type", "approval", "--question", "Q", "--supersedes", "7"},
		{"decision", "7"}, {"decision", "dr-1", "dr-2"}, {"withdraw"},
	} {
		if code, out, err := task(t, "", args...); code != 2 || err == nil || out != "" {
			t.Errorf("%q: %d %q %v", args, code, out, err)
		}
	}
	if n := d.count(); n != before {
		t.Fatalf("a usage error sent %d requests", n-before)
	}
	// The server's refusal, in its words.
	if code, _, err := task(t, "", "decide", "--type", "approval", "--question", "Q", "--option", "only one", "--authorizes", "x"); code != 1 || err == nil || !strings.Contains(err.Error(), "an approval has 2 to 6 options, not 1 (invalid)") {
		t.Fatalf("a refused request: %d %v", code, err)
	}
	if code, _, err := task(t, "", "decision", "dr-9"); code != 1 || err == nil || !strings.Contains(err.Error(), "(not_found)") {
		t.Fatalf("another's decision: %d %v", code, err)
	}
}

// task withdraw withdraws an open decision the attempt asked, which its
// task waits for no more; a second withdrawal is refused in words.
func TestTaskWithdraw(t *testing.T) {
	d := newDecisionAPI(t)
	mustTask(t, "decide", "--type", "clarification", "--question", "Which schema?")
	if out := mustTask(t, "withdraw", "dr-1"); out != "dr-1 is cancelled: your task waits for it no more\n" {
		t.Fatalf("withdraw: %q", out)
	}
	if st := d.decision(t, "dr-1")["state"]; st != "cancelled" {
		t.Fatalf("dr-1 is %v", st)
	}
	if code, _, err := task(t, "", "withdraw", "dr-1"); code != 1 || err == nil || !strings.Contains(err.Error(), "(decision_closed)") {
		t.Fatalf("a second withdrawal: %d %v", code, err)
	}
}

// conductor decision respond reads the decision's rev and sends it with a
// minted responseId, once more with it when the reply is lost (one
// response kept), and prints the receipt; Yardmaster applies the answer,
// which the attempt's list then shows. A second answer is refused in
// words; show prints the responses and the history; list filters; cancel
// reads the rev too. Usage errors send nothing.
func TestDecisionRespondSendsTheRev(t *testing.T) {
	d := newDecisionAPI(t)
	mustTask(t, "decide", "--type", "approval", "--question", "Ship it?", "--option", "Ship", "--option", "Hold", "--authorizes", "the deploy")
	mustTask(t, "decide", "--type", "clarification", "--question", "Which schema?")
	respond := "/api/decisions/dr-1/respond"
	before := d.count()
	d.loseNext("POST", respond, 1)
	code, out, _, err := d.decisionCmd(t, "respond", "dr-1", "--choice", "0", "--why", "the tests pass")
	if code != 0 || err != nil || !strings.HasPrefix(out, "Recorded: dr-1 answered by admin (receipt dr-1/1, rev 2)\ndr-1 is ") {
		t.Fatalf("respond: %d %v %q", code, err, out)
	}
	seen := d.requests(before)
	if len(seen) != 3 || seen[0].method != "GET" || seen[0].path != "/api/decisions/dr-1" {
		t.Fatalf("respond's requests: %v", seen)
	}
	first, again := seen[1].body, seen[2].body
	if seen[1].reply != -1 || first["expectedRev"] != 1.0 || first["choice"] != 0.0 || first["rationale"] != "the tests pass" || first["text"] != nil ||
		!opIDShape.MatchString(fmt.Sprint(first["responseId"])) || again["responseId"] != first["responseId"] || again["expectedRev"] != 1.0 {
		t.Fatalf("respond's bodies: %v %v", first, again)
	}
	if rs, _ := d.decision(t, "dr-1")["responses"].([]any); len(rs) != 1 {
		t.Fatalf("responses kept: %v", rs)
	}
	d.until(t, "dr-1 applied", func() bool { return d.decision(t, "dr-1")["state"] == "applied" })
	if out := mustTask(t, "decision"); !strings.Contains(out, "dr-1 (approval, applied, rev ") || !strings.Contains(out, "\n  answered: Ship\n") {
		t.Fatalf("the attempt's list once applied:\n%s", out)
	}
	// A second answer, read at the rev now, is refused as answered.
	if code, _, _, err := d.decisionCmd(t, "respond", "dr-1", "--choice", "1"); code != 1 || err == nil || !strings.HasPrefix(err.Error(), "decision_") {
		t.Fatalf("a second answer: %d %v", code, err)
	}
	code, out, _, err = d.decisionCmd(t, "show", "dr-1")
	if code != 0 || err != nil || !hasAll(out, "dr-1: approval, applied, rev ", "answer: Ship\n", "responses:\n  dr-1/1 accepted by admin at ", "applied: ", "history:\n  1 ") {
		t.Fatalf("show: %d %v\n%s", code, err, out)
	}
	if code, out, _, err := d.decisionCmd(t, "list", "--state", "open"); code != 0 || err != nil || !regexp.MustCompile(`^dr-2\topen\trev 1\tclarification\trun `+d.run+`, task build\tWhich schema\?\n$`).MatchString(out) {
		t.Fatalf("list --state open: %d %v %q", code, err, out)
	}
	decisionsPage = 1
	t.Cleanup(func() { decisionsPage = 10 })
	before = d.count()
	if code, out, _, err := d.decisionCmd(t, "list", "--run", d.run); code != 0 || err != nil || !regexp.MustCompile(`^dr-2\t.*\ndr-1\tapplied\t.*\n$`).MatchString(out) || d.count()-before != 2 {
		t.Fatalf("list --run, a page of one: %d %v %q, %d requests", code, err, out, d.count()-before)
	}
	// cancel reads the rev and sends it with a minted commandId.
	before = d.count()
	if code, out, _, err := d.decisionCmd(t, "cancel", "dr-2", "--why", "asked twice", "--release"); code != 0 || err != nil || !hasAll(out, "dr-2 cancelled (rev ", "dr-2 is cancelled") {
		t.Fatalf("cancel: %d %v %q", code, err, out)
	}
	if c := d.posts(before, "/api/decisions/dr-2/cancel"); len(c) != 1 || c[0].body["expectedRev"] != 1.0 || c[0].body["why"] != "asked twice" || c[0].body["release"] != true || !opIDShape.MatchString(fmt.Sprint(c[0].body["commandId"])) {
		t.Fatalf("cancel's body: %v", c)
	}
	before = d.count()
	for _, args := range [][]string{
		{"respond", "dr-1"}, {"respond", "dr-1", "--choice", "-2"}, {"respond", "1", "--choice", "0"}, {"cancel", "dr-1"}, {"show"},
		{"list", "--state", "pending"}, {"list", "--ticket", "cnd"}, {"list", "--run", "Not A Run"}, {"list", "dr-1"},
	} {
		if code, out, _, err := d.decisionCmd(t, args[0], args[1:]...); code != 2 || err == nil || out != "" {
			t.Errorf("%q: %d %q %v", args, code, out, err)
		}
	}
	if n := d.count(); n != before {
		t.Fatalf("a usage error sent %d requests", n-before)
	}
}

// The 100 fullest summaries of a page (each question, answer and why
// at the summary's cut, every other field at its longest), the list's
// bound under the session client's: conductor task decision prints them
// all, following the next page to its end, and conductor mcp's
// decision_list answers the first page whole.
func TestDecisionListFitsTheSessionClient(t *testing.T) {
	page, summary := opBound("decision_list", "reply"), opBound("decision_list", "summary")
	if page <= 0 || page >= maxSessionReply || summary <= 0 || 100*summary > page {
		t.Fatalf("decision_list's reply bound %d, its summary's %d, the session client's %d", page, summary, maxSessionReply)
	}
	cut := strings.Repeat("\x01", 1<<10/6) // six bytes each as encoded, at the summary's 1 KiB
	at := time.Date(2026, 11, 5, 23, 59, 59, 999999999, time.UTC)
	fullest := func(n int64) map[string]any {
		return map[string]any{"id": fmt.Sprintf("dr-%d", n), "requestId": strings.Repeat("r", 32), "type": "clarification", "origin": "agent", "task": strings.Repeat("t", 40),
			"gate": strings.Repeat("g", 40), "state": "superseded", "rev": 1 << 30, "question": cut, "answer": cut, "why": cut, "supersededBy": "dr-999999999999999999",
			"createdAt": at, "answeredAt": at, "closedAt": at, "cut": true}
	}
	var first []any
	for i := range int64(100) {
		s := fullest(999999999999999999 - i) // the longest id ParseDecisionID reads
		if b, _ := json.Marshal(s); len(b) > summary || len(b) < summary*3/4 {
			t.Fatalf("a summary of %d bytes, the bound %d", len(b), summary)
		}
		first = append(first, s)
	}
	firstPage, _ := json.Marshal(map[string]any{"decisions": first, "next": "dr-999999999999999900"})
	if len(firstPage) > page {
		t.Fatalf("a page of %d bytes, over %d", len(firstPage), page)
	}
	lastPage, _ := json.Marshal(map[string]any{"decisions": []any{fullest(9)}})
	var (
		mu     sync.Mutex
		afters []string
	)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/sessions/s1/decisions" || r.Header.Get("Authorization") != "Bearer agent-tok" {
			http.Error(w, "unexpected", http.StatusTeapot)
			return
		}
		after := r.URL.Query().Get("after")
		mu.Lock()
		afters = append(afters, after)
		mu.Unlock()
		if after == "" {
			_, _ = w.Write(firstPage)
			return
		}
		_, _ = w.Write(lastPage)
	}))
	defer hs.Close()
	inSession(t, hs)
	out := mustTask(t, "decision")
	if n := strings.Count(out, "\ndr-") + 1; !strings.HasPrefix(out, "dr-999999999999999999 (clarification, superseded, rev 1073741824, the plan's ") || n != 101 || !strings.HasSuffix(out, "  (cut: task decision dr-9 reads it whole)\n") {
		t.Fatalf("%d decisions printed:\n%.400s", n, out)
	}
	mu.Lock()
	asked := fmt.Sprint(afters)
	mu.Unlock()
	if asked != "[ dr-999999999999999900]" {
		t.Fatalf("the pages asked: %s", asked)
	}
	env := map[string]string{"CONDUCTOR_SESSION_ID": "s1", "CONDUCTOR_NOTIFY_URL": hs.URL + "/api/sessions/s1/attention", "CONDUCTOR_NOTIFY_TOKEN": "agent-tok", "CONDUCTOR_TASK": "build"}
	s := &mcpServer{getenv: func(k string) string { return env[k] }}
	text, err := s.call(t.Context(), "decision_list", json.RawMessage(`{}`))
	var got struct {
		Decisions []map[string]any `json:"decisions"`
		Next      string           `json:"next"`
	}
	if err != nil || json.Unmarshal([]byte(text), &got) != nil || len(got.Decisions) != 100 || got.Next != "dr-999999999999999900" || got.Decisions[99]["question"] != cut {
		t.Fatalf("decision_list: %d decisions, next %q, %v", len(got.Decisions), got.Next, err)
	}
}

// --subject and --option read as the request names them.
func TestDecideReadsSubjectsAndOptions(t *testing.T) {
	for in, want := range map[string]string{
		"commit":                           `{"kind":"commit"}`,
		"commit:0123abc":                   `{"kind":"commit","commit":"0123abc"}`,
		"artifact:docs/api.md":             `{"kind":"artifact","path":"docs/api.md"}`,
		"candidate:team-1/build/3@9f2c1ab": `{"kind":"candidate","commit":"9f2c1ab","run":"team-1","task":"build","n":3}`,
	} {
		s, err := parseSubject(in)
		b, _ := json.Marshal(s)
		if err != nil || string(b) != want {
			t.Errorf("%s: %s %v, want %s", in, b, err, want)
		}
	}
	for _, in := range []string{"", "artifact:", "candidate:team-1/build/3", "candidate:team-1/build/0@9f2c1ab", "candidate:team-1/build@9f2c1ab", "candidate:team-1/build/3@9f2c", "tree"} {
		if _, err := parseSubject(in); err == nil {
			t.Errorf("%q read as a subject", in)
		}
	}
	if o, err := parseOption("v2: faster: and riskier"); err != nil || o.Label != "v2" || o.Tradeoffs != "faster: and riskier" {
		t.Errorf("an option with its tradeoffs: %+v %v", o, err)
	}
	if o, err := parseOption("Keep: it"); err != nil || o.Label != "Keep" || o.Tradeoffs != "it" {
		t.Errorf("an option: %+v %v", o, err)
	}
	if o, err := parseOption("a:b"); err != nil || o.Label != "a:b" || o.Tradeoffs != "" {
		t.Errorf("a label with a colon: %+v %v", o, err)
	}
}
