package cli

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/session"
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
	return launchDecisionAPI(t, newPlannedAPI(t))
}

// newReviewedDecisionAPI is newDecisionAPI with a crew whose build is
// reviewed: its review task, on the same member, starts at build's
// result once that is taken.
func newReviewedDecisionAPI(t *testing.T) *decisionAPI {
	t.Helper()
	a := newPlannedAPI(t)
	a.crew = a.admin(t, "POST", "/api/crews", map[string]any{
		"name": "Reviewed", "goal": "fix it", "cwd": a.repo, "where": "server", "isolation": "worktree",
		"members": []any{map[string]any{"name": "lead", "agentId": "ready-cat", "start": map[string]any{"when": "task"}}},
		"tasks": []any{
			map[string]any{"id": "build", "member": "lead", "kind": "work", "instructions": "Build it.",
				"checks": []any{map[string]any{"id": "test", "argv": []string{"true"}}}},
			map[string]any{"id": "review", "member": "lead", "kind": "review", "instructions": "Review it.", "needs": []string{"build"}},
		},
	}, http.StatusCreated)["crew"].(map[string]any)["id"].(string)
	return launchDecisionAPI(t, a)
}

// launchDecisionAPI launches a's crew and waits for build's attempt.
func launchDecisionAPI(t *testing.T, a *plannedAPI) *decisionAPI {
	t.Helper()
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

// attempt polls the run until task's attempt runs, and gives its
// session an agent token, as its process gets one: the attempt as the
// run reports it, and the token.
func (d *decisionAPI) attempt(t *testing.T, task string) (map[string]any, string) {
	t.Helper()
	var at map[string]any
	d.until(t, task+"'s attempt running", func() bool {
		tasks, _ := d.admin(t, "GET", "/api/runs/"+d.run, nil, http.StatusOK)["run"].(map[string]any)["tasks"].([]any)
		for _, x := range tasks {
			tk, _ := x.(map[string]any)
			if a, _ := tk["attempt"].(map[string]any); tk["id"] == task && a != nil && a["state"] == "running" && a["sessionId"] != nil && a["sessionId"] != "" {
				at = a
				return true
			}
		}
		return false
	})
	sid := at["sessionId"].(string)
	s, ok := d.srv.Registry().Get(sid)
	local, isLocal := s.(*session.Local)
	if !ok || !isLocal {
		t.Fatalf("session %s is not a server session", sid)
	}
	token := "agent-token-" + sid
	local.SetAgentToken(token)
	return at, token
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
	// None yet: an empty JSON array, never null.
	if out := mustTask(t, "decision", "--json"); out != "[]\n" {
		t.Fatalf("decision --json with none: %q", out)
	}
	before := d.count()
	out := mustTask(t, "decide", "--type", "approval", "--question", "Ship it?", "--context", "The tests pass.\nThe docs do not.",
		"--option", "Ship: now, the docs after", "--option", "Hold", "--recommend", "0", "--uncertainty", "the docs", "--authorizes", "the deploy",
		"--evidence", "task:"+d.run+"/build", "--subject", "commit")
	if !strings.HasPrefix(out, "dr-1 open. Pending. ") || !hasAll(out, "do not submit your result until its answer is applied", "'Decision dr-1 … answered by …'", "save one before you end your turn") {
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
	if out, want := mustTask(t, "decision"), "dr-2 (clarification, open, rev 1): Which schema?\n  now: open: no answer yet, wait for it\n"+
		"dr-1 (approval, open, rev 1): Ship it?\n  now: open: no answer yet, wait for it\n"; out != want {
		t.Fatalf("decision:\n%q\nwant\n%q", out, want)
	}
	out = mustTask(t, "decision", "dr-1")
	if !hasAll(out, "dr-1: approval, open, rev 1\n", "asked by: task build's attempt 1, live, in run "+d.run+"\n", "question: Ship it?\n", "context:\n  The tests pass.\n  The docs do not.\n",
		"options (answer with the number):\n  0. Ship: now, the docs after (recommended)\n  1. Hold\n", "uncertainty: the docs\n", "authorizes: the deploy\n",
		"subject: commit "+d.head+"\n", "evidence:\n  task:"+d.run+"/build\n", "now: open: no answer yet, wait for it\n") {
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

	// Usage errors: exit 2, nothing sent; a file with more after its one
	// value among them, as the server refuses a body.
	trailing := filepath.Join(t.TempDir(), "two.json")
	if err := os.WriteFile(trailing, []byte(`{"type":"clarification","question":"Which schema?"} {"type":"approval"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before = d.count()
	for _, args := range [][]string{
		{"decide", "--file", trailing},
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

// A withdrawal refused not_held, its run continued (#637, review round
// 3), prints the route's words with its code, which tell the agent to
// stop, prints nothing on stdout and exits 1; the skill and the usage
// name the code and say the same.
func TestAWithdrawalRefusedNotHeldSaysStop(t *testing.T) {
	const words = "dr-7's run was continued: its attempt is no longer the one that asked it. dr-7 stays as it is in the run that continues this one, which waits on it; stop here, as this attempt's work is over"
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/sessions/s1/decisions/dr-7/withdraw" {
			http.Error(w, "unexpected", http.StatusTeapot)
			return
		}
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprintf(w, `{"error":{"code":"not_held","message":%q}}`, words)
	}))
	defer hs.Close()
	inSession(t, hs)
	code, out, err := task(t, "", "withdraw", "dr-7")
	if code != 1 || out != "" || err == nil || err.Error() != "task: "+words+" (not_held)" {
		t.Fatalf("withdraw refused not_held: %d %q %v", code, out, err)
	}
	skill := strings.Join(strings.Fields(agents.Skill), " ")
	if !strings.Contains(skill, "A withdrawal refused not_held means your run was continued") || !strings.Contains(skill, "Stop there and end your turn") {
		t.Error("the skill does not say what a withdrawal refused not_held means")
	}
	if !strings.Contains(strings.Join(strings.Fields(taskUsage), " "), "A withdrawal refused not_held means your run was continued") {
		t.Error("the usage does not say what a withdrawal refused not_held means")
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
	code, out, _, err := d.decisionCmd(t, "respond", "dr-1", "--choice", "0", "--why", "Only deploy after the backup")
	if code != 0 || err != nil || !strings.HasPrefix(out, "Recorded: dr-1 answered by admin (receipt dr-1/1, rev 2)\ndr-1 is ") {
		t.Fatalf("respond: %d %v %q", code, err, out)
	}
	seen := d.requests(before)
	if len(seen) != 3 || seen[0].method != "GET" || seen[0].path != "/api/decisions/dr-1" {
		t.Fatalf("respond's requests: %v", seen)
	}
	first, again := seen[1].body, seen[2].body
	if seen[1].reply != -1 || first["expectedRev"] != 1.0 || first["choice"] != 0.0 || first["rationale"] != "Only deploy after the backup" || first["text"] != nil ||
		!opIDShape.MatchString(fmt.Sprint(first["responseId"])) || again["responseId"] != first["responseId"] || again["expectedRev"] != 1.0 {
		t.Fatalf("respond's bodies: %v %v", first, again)
	}
	if rs, _ := d.decision(t, "dr-1")["responses"].([]any); len(rs) != 1 {
		t.Fatalf("responses kept: %v", rs)
	}
	d.until(t, "dr-1 applied", func() bool { return d.decision(t, "dr-1")["state"] == "applied" })
	// A conditional approval: the list's answer is the option alone, so
	// it never says to go on with it, but to read the decision whole,
	// where the condition is (#689, review round 2).
	if out := mustTask(t, "decision"); !strings.Contains(out, "dr-1 (approval, applied, rev ") ||
		!strings.Contains(out, "\n  now: applied: read it whole before you go on, with the rationale its answer was given: task decision dr-1\n  answered: Ship\n") ||
		strings.Contains(out, "go on with") {
		t.Fatalf("the attempt's list once applied:\n%s", out)
	}
	if out := mustTask(t, "decision", "dr-1"); !hasAll(out, "answer: Ship\n", "\n    why: Only deploy after the backup\n", "now: applied: go on with its answer\n") {
		t.Fatalf("the attempt's read once applied:\n%s", out)
	}
	// A second answer, read at the rev now, is refused as answered.
	if code, _, _, err := d.decisionCmd(t, "respond", "dr-1", "--choice", "1"); code != 1 || err == nil || !strings.HasPrefix(err.Error(), "decision_") {
		t.Fatalf("a second answer: %d %v", code, err)
	}
	// A comment, kept in every state: show prints its words in the history.
	rev := int(d.decision(t, "dr-1")["rev"].(float64))
	d.admin(t, "POST", "/api/decisions/dr-1/comment", map[string]any{"opId": "o1", "kind": "comment", "text": "Looks right.", "expectedRev": rev}, http.StatusOK)
	code, out, _, err = d.decisionCmd(t, "show", "dr-1")
	if code != 0 || err != nil || !hasAll(out, "dr-1: approval, applied, rev ", "answer: Ship\n", "responses:\n  dr-1/1 accepted by admin at ", "\n    why: Only deploy after the backup\n", "applied: ", "history:\n  1 ",
		" commented by admin: Looks right.\n") {
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

// conductor decision show prints each evidence ref with what changed of
// its record since the question was asked, read from the admin's refs
// (which respond and cancel read too): the run stopped since.
func TestDecisionShowsWhatChangedOfItsEvidence(t *testing.T) {
	d := newDecisionAPI(t)
	mustTask(t, "decide", "--type", "clarification", "--question", "Which schema?", "--evidence", "run:"+d.run, "--evidence", "task:"+d.run+"/build")
	code, out, _, err := d.decisionCmd(t, "show", "dr-1")
	if code != 0 || err != nil || !hasAll(out, "evidence:\n  run:"+d.run+"\n  task:"+d.run+"/build\n") {
		t.Fatalf("show before: %d %v\n%s", code, err, out)
	}
	d.admin(t, "POST", "/api/runs/"+d.run+"/stop", nil, http.StatusOK)
	code, out, _, err = d.decisionCmd(t, "show", "dr-1")
	if code != 0 || err != nil || !regexp.MustCompile(`\n  run:`+regexp.QuoteMeta(d.run)+` \(changed: was running[^\n]*, now [a-z]+[^\n]*\)\n`).MatchString(out) {
		t.Fatalf("show after the run stopped: %d %v\n%s", code, err, out)
	}
}

// Each ref's resolution goes with the ref it names, never with its place:
// a resolution the server left out shifts none of the others.
func TestDecisionTextMatchesRefsByName(t *testing.T) {
	req, _ := json.Marshal(map[string]any{"question": "Q", "evidence": []string{"run:r1", "task:r1/build"}})
	out := decisionText(crew.Decision{ID: "dr-1", Type: "clarification", State: "open", Rev: 1, Request: req},
		[]decisionRef{{Ref: "task:r1/build", Seen: refAt{State: "running"}, Now: refAt{State: "failed"}, Change: "changed"}}, false)
	if !strings.Contains(out, "evidence:\n  run:r1\n  task:r1/build (changed: was running, now failed)\n") {
		t.Fatalf("%s", out)
	}
}

// The agent's commands read the states apart: only applied goes on, and
// a request answered at once (its requestId sent before) that is only
// recorded says to wait, never to go on.
func TestTheAgentReadsTheStatesApart(t *testing.T) {
	seen := map[string]string{}
	for _, st := range decisionStates {
		w := agentStateWords(st)
		if !strings.HasPrefix(w, st+": ") || strings.Contains(w, "go on") != (st == "applied") {
			t.Errorf("%s: %q", st, w)
		}
		seen[w] = st
	}
	if len(seen) != len(decisionStates) {
		t.Errorf("states told alike: %v", seen)
	}
	var state atomic.Value
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/sessions/s1/decisions" {
			http.Error(w, "unexpected", http.StatusTeapot)
			return
		}
		_, _ = fmt.Fprintf(w, `{"decision":{"id":"dr-7","state":%q,"answer":"Ship"}}`, state.Load())
	}))
	defer hs.Close()
	inSession(t, hs)
	// The route's words (decisionRequestText), as decision_request gives
	// them: answered waits, applied goes on, and applied adds that the
	// answer's rationale is read whole first.
	state.Store("answered")
	out := mustTask(t, "decide", "--type", "clarification", "--question", "Which schema?")
	if !strings.HasPrefix(out, "dr-7 is answered: Ship. The answer is recorded, not yet applied") || strings.Contains(out, "Go on with") ||
		!strings.Contains(out, "do not submit your result until it is applied") || strings.Contains(out, "read it whole") {
		t.Fatalf("an answered request: %q", out)
	}
	state.Store("applied")
	out = mustTask(t, "decide", "--type", "clarification", "--question", "Which schema?")
	if !strings.HasPrefix(out, "dr-7 is applied: Ship. Go on with your task with this answer") ||
		!strings.HasSuffix(out, "\nBefore you act, read it whole, with the rationale its answer was given: conductor task decision dr-7\n") {
		t.Fatalf("an applied request: %q", out)
	}
}

// shellWords splits an example line of the skill as a shell would: words
// apart at spaces, "..." kept whole, and $NAME (and ${NAME:-default})
// read from the environment.
func shellWords(t *testing.T, line string) []string {
	t.Helper()
	expand := func(s string) string {
		return os.Expand(s, func(name string) string {
			if n, def, ok := strings.Cut(name, ":-"); ok {
				return cmp.Or(os.Getenv(n), def)
			}
			return os.Getenv(name)
		})
	}
	var words []string
	var cur strings.Builder
	in, started := false, false
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case c == '"':
			in, started = !in, true
		case c == ' ' && !in:
			if started {
				words = append(words, expand(cur.String()))
				cur.Reset()
				started = false
			}
		default:
			cur.WriteByte(c)
			started = true
		}
	}
	if in {
		t.Fatalf("an unclosed quote: %s", line)
	}
	if started {
		words = append(words, expand(cur.String()))
	}
	return words
}

// Each command the skill's Decisions section shows is run as it stands,
// against a real server, in the order it shows them: a work task's task
// decide makes dr-7 (six asked and withdrawn before it) and replies as
// the skill quotes it, task decision dr-7 reads it, task withdraw dr-7
// withdraws it. A change to a command that its example does not follow
// fails here (#689, review round 2). The review's example runs in the
// review task once build's result is taken: a review saves no checkpoint
// (task checkpoint refuses it, as the skill says), and its decide opens
// its decision without one, about the candidate at its HEAD (#689,
// review round 3).
func TestTheSkillsDecisionExamplesRun(t *testing.T) {
	d := newReviewedDecisionAPI(t)
	t.Setenv("CONDUCTOR_RUN", d.run)
	t.Setenv("CONDUCTOR_TASK", "build")
	t.Setenv("CONDUCTOR_BIN", "")
	section := agents.Skill[strings.Index(agents.Skill, "\n## Decisions\n"):]
	section = section[:strings.Index(section[1:], "\n## ")+1]
	// Each example is a work task's or the review's, by the bullet it is
	// under; the review's is read as a shell would once the review runs,
	// its environment the review's.
	var work [][]string
	var review []string
	reviews := false
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "- ") {
			reviews = strings.HasPrefix(line, "- A review ")
		}
		if line = strings.TrimSpace(line); strings.HasPrefix(line, `"${CONDUCTOR_BIN:-conductor}" `) {
			if reviews {
				review = append(review, line)
			} else {
				work = append(work, shellWords(t, line))
			}
		}
	}
	if len(work) != 3 || len(review) != 1 || !strings.HasPrefix(review[0], `"${CONDUCTOR_BIN:-conductor}" task decide `) {
		t.Fatalf("the section's examples: a work task's %q, the review's %q", work, review)
	}
	// Six asked and withdrawn before it (an attempt holds at most four
	// open), so that the example's is dr-7.
	for i := 1; i <= 6; i++ {
		mustTask(t, "decide", "--type", "clarification", "--question", fmt.Sprintf("Question %d?", i))
		mustTask(t, "withdraw", fmt.Sprintf("dr-%d", i))
	}
	skill := strings.Join(strings.Fields(agents.Skill), " ")
	for _, words := range work {
		if words[0] != "conductor" || words[1] != "task" {
			t.Fatalf("an example that is no task command: %q", words)
		}
		out := mustTask(t, words[2:]...)
		switch words[2] {
		case "decide":
			// The skill quotes the reply's start: "dr-7 open. Pending. ...".
			quoted := regexp.MustCompile(`"(dr-7 open\. Pending\.) \.\.\."`).FindStringSubmatch(skill)
			if quoted == nil || !strings.HasPrefix(out, quoted[1]+" ") {
				t.Errorf("decide's reply %q, the skill quotes %q", out, quoted)
			}
			if b := d.decision(t, "dr-7"); b["type"] != "approval" || b["state"] != "open" {
				t.Errorf("dr-7 as decide made it: %v", b)
			}
		case "decision":
			if !strings.HasPrefix(out, "dr-7: approval, open, rev 1\n") || !strings.Contains(out, "evidence:\n  task:"+d.run+"/build\n") {
				t.Errorf("decision dr-7:\n%s", out)
			}
		case "withdraw":
			if out != withdrawnWords("dr-7", "cancelled")+"\n" {
				t.Errorf("withdraw dr-7: %q", out)
			}
		default:
			t.Errorf("an example of no decision command: %q", words)
		}
	}

	// build's result, a commit of its own, taken; the review runs at it.
	built, _ := d.attempt(t, "build")
	wt := built["worktree"].(string)
	if err := os.WriteFile(filepath.Join(wt, "built.txt"), []byte("built\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt, "add", "built.txt")
	gitIn(t, wt, "commit", "-q", "-m", "built")
	candidate := gitIn(t, wt, "rev-parse", "HEAD")
	mustTask(t, "result", "--summary", "built")
	rev, token := d.attempt(t, "review")
	inSessionOf(t, d.hs.URL, rev["sessionId"].(string), token)
	t.Setenv("CONDUCTOR_TASK", "review")
	if code, _, err := task(t, "", "checkpoint", "--done", "read it", "--remaining", "the verdict"); code != 1 || err == nil || !strings.Contains(err.Error(), "a review saves no checkpoint") {
		t.Fatalf("a review's checkpoint: %d %v", code, err)
	}
	out := mustTask(t, shellWords(t, review[0])[2:]...)
	if !strings.HasPrefix(out, "dr-8 open. Pending. ") {
		t.Errorf("the review's decide: %q", out)
	}
	b := d.decision(t, "dr-8")
	asker, _ := b["asker"].(map[string]any)
	var req struct {
		Subject  crew.DecisionSubject `json:"subject"`
		Evidence []string             `json:"evidence"`
	}
	raw, _ := json.Marshal(b["request"])
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	if b["type"] != "clarification" || b["state"] != "open" || asker == nil || asker["task"] != "review" ||
		req.Subject.Kind != "commit" || req.Subject.Commit != candidate || fmt.Sprint(req.Evidence) != "[task:"+d.run+"/review]" {
		t.Errorf("dr-8 as the review's decide made it: %v, its request %+v (the candidate %s)", b, req, candidate)
	}
}

// decision cancel's help tells an agent's decision from a plan's gate:
// cancelling an agent's never cancels its task, which goes on told to
// take it as a no, and --release changes nothing there; a gate's tasks
// are cancelled with it unless --release releases them (#689, review
// round 2; crew.CancelDecision).
func TestCancelsHelpTellsAgentsFromGates(t *testing.T) {
	var out, errb bytes.Buffer
	if code, err := runDecision(t.Context(), []string{"cancel", "-h"}, &out, &errb); code != 0 || err != nil {
		t.Fatalf("cancel -h: %d %v", code, err)
	}
	help := strings.Join(strings.Fields(errb.String()), " ")
	if !hasAll(help, "An agent's (asked by a task's attempt): its task waits for it no more and goes on, told the decision was cancelled, not approved, and to take it as a no; --release changes nothing.",
		"A plan's gate (the plan's decision, list shows \"the plan's\"): the tasks it holds are cancelled with it, or, with --release, released to run without its answer",
		"a plan's gate: release the tasks it holds to run without its answer, instead of cancelling them; an agent's decision always releases its task, told to take it as a no") {
		t.Fatalf("cancel's help:\n%s", errb.String())
	}
	if strings.Contains(help, "cancel an open or answered decision: the tasks it holds are cancelled with it") {
		t.Fatalf("cancel's help still says every decision's tasks are cancelled:\n%s", errb.String())
	}
}

// task decide --for names whom a decision is for (docs/plans/decisions.md,
// HM1, PR-12): sent as the request's for, the server's refusal of a label
// the run's crew does not resolve said in its words; a label of neither
// shape is a usage error, nothing sent.
func TestTaskDecideFor(t *testing.T) {
	d := newDecisionAPI(t)
	posts := "/api/sessions/" + d.sid + "/decisions"
	before := d.count()
	code, _, err := task(t, "", "decide", "--type", "clarification", "--question", "Which schema?", "--for", "role:architecture")
	if code != 1 || err == nil || !strings.Contains(err.Error(), "the run's crew lists no people (invalid)") {
		t.Fatalf("a label the crew lacks: %d %v", code, err)
	}
	if sent := d.posts(before, posts); len(sent) != 1 || sent[0].body["for"] != "role:architecture" {
		t.Fatalf("decide --for's body: %v", sent)
	}
	before = d.count()
	if code, out, err := task(t, "", "decide", "--type", "clarification", "--question", "Q", "--for", "architecture"); code != 2 || err == nil || out != "" {
		t.Fatalf("--for of no shape: %d %q %v", code, out, err)
	}
	if n := d.count(); n != before {
		t.Fatalf("a usage error sent %d requests", n-before)
	}
}
