package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// conductor dispatch review, reviews, findings and finding, and conductor
// crew recheck (Program item RV1, docs/plans/review-repair.md, PR-5):
// against stand-ins of the routes, which decode every body as the route
// does (the store's own types beside the operation, no field it does not
// take) and answer what the test tells them, and against a real server,
// whose routes take every body the commands send.

// The stand-ins' candidate: attempt 1 of build in run A, its commit, the
// run's base, and B, the run that continues A.
var (
	rvRunA   = "team-0a1b2c3d"
	rvRunB   = "team-9f8e7d6c"
	rvCommit = strings.Repeat("a", 40)
	rvBase   = strings.Repeat("b", 40)
	rvCand   = rvRunA + "/build/1@" + rvCommit
	rvCand2  = rvRunA + "/build/2@" + strings.Repeat("c", 40)
	rvID     = "0123456789abcdef"
	rvF1     = "1111111111111111"
	rvF2     = "2222222222222222"
)

// standIn serves mux and records every request: its method, path, query
// and body. It loses the next replies lose names ("METHOD path") as
// dispatchAPI does: the handler acts on each whole, and the connection is
// then cut before the reply.
type standIn struct {
	mu     sync.Mutex
	seen   []seenRequest
	lose   string
	losing int
	mux    *http.ServeMux
	srv    *httptest.Server
}

func newStandIn(t *testing.T, mux *http.ServeMux) *standIn {
	t.Helper()
	clearConductorEnv(t)
	s := &standIn{mux: mux}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var decoded map[string]any
		_ = json.Unmarshal(body, &decoded)
		s.mu.Lock()
		lost := s.losing > 0 && s.lose == r.Method+" "+r.URL.Path
		if lost {
			s.losing--
		}
		seen := seenRequest{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, body: decoded}
		if lost {
			seen.reply = -1
		}
		s.seen = append(s.seen, seen)
		s.mu.Unlock()
		if lost {
			s.mux.ServeHTTP(httptest.NewRecorder(), r)
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				conn.Close()
			}
			return
		}
		s.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// loseNext arms the loss of the next n replies to "METHOD path".
func (s *standIn) loseNext(method, path string, n int) {
	s.mu.Lock()
	s.lose, s.losing = method+" "+path, n
	s.mu.Unlock()
}

// requests is what the stand-in saw.
func (s *standIn) requests() []seenRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.seen)
}

// run runs conductor <args...> against the stand-in, its server and token
// given after the command's words.
func (s *standIn) run(t *testing.T, stdin string, args ...string) (int, string, string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	full := append(slices.Clone(args), "--server", s.srv.URL, "--token", dispatchToken)
	code, err := Run(t.Context(), full, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String(), err
}

// strictBody decodes r's body into v as the routes do, no field v does not
// take; a body that is not one answers 400 and fails the test.
func strictBody(t *testing.T, w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Errorf("%s %s: the body is not the route's: %v", r.Method, r.URL.Path, err)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"invalid_request","message":"not the route's body"}}`))
		return false
	}
	return true
}

// answer writes v as JSON with the status.
func answer(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// The routes' bodies, as internal/api decodes them: the operation (DS1's
// opId, expectedRev and byName) beside the store's own types.
type (
	submitBody struct {
		OpID        string `json:"opId"`
		ExpectedRev int64  `json:"expectedRev,omitempty"`
		ByName      string `json:"byName,omitempty"`
		dispatch.NewReview
		ExpectedRevision int64 `json:"expectedRevision,omitempty"`
		ContinueRun      bool  `json:"continueRun,omitempty"`
	}
	retryBody struct {
		OpID             string `json:"opId"`
		ExpectedRev      int64  `json:"expectedRev,omitempty"`
		ByName           string `json:"byName,omitempty"`
		ExpectedRevision int64  `json:"expectedRevision,omitempty"`
		ContinueRun      bool   `json:"continueRun,omitempty"`
	}
	markBody struct {
		OpID        string `json:"opId"`
		ExpectedRev int64  `json:"expectedRev,omitempty"`
		ByName      string `json:"byName,omitempty"`
		dispatch.NewMark
	}
	recheckBody struct {
		crew.Command
		Attempt int    `json:"attempt"`
		Commit  string `json:"commit"`
	}
)

// opIDShape is an operation's id as the CLI mints it.
var rvOpID = regexp.MustCompile(`^[0-9a-f]{16}$`)

// reviewOf is the review a stand-in answers: rv as sent, with its id, its
// findings numbered and its application.
func reviewOf(rv dispatch.NewReview, app dispatch.ReviewApplication) dispatch.Review {
	v := dispatch.Review{ReviewSummary: dispatch.ReviewSummary{ID: rvID, TicketKey: "CND-1", Candidate: rv.Candidate, Base: rv.Base, Verdict: rv.Verdict, Summary: rv.Summary, Actor: "admin", Via: rv.Via, Application: app}}
	for i, f := range rv.Findings {
		v.Findings = append(v.Findings, dispatch.Finding{ID: fmt.Sprintf("%016d", i+1), ReviewID: rvID, K: i + 1, Kind: f.Kind, Intent: f.Intent, Severity: f.Severity, Path: f.Path, Side: f.Side, From: f.From, To: f.To, Lines: f.Lines, Message: f.Message, Resolution: f.Resolution, Candidate: rv.Candidate, State: dispatch.FindingOpen})
		v.Counts.Open++
	}
	return v
}

// The notes a file holds: a change on two lines, a question on the
// candidate as a whole.
var rvNotes = []dispatch.NewFinding{
	{Kind: dispatch.FindingKindLine, Intent: dispatch.FindingIntentChange, Severity: "major", Path: "internal/api/users.go", Side: dispatch.SideCandidate, From: 3, To: 4, Lines: []string{"x := 1", "y := <2>"}, Message: "name them", Resolution: "n and m"},
	{Kind: dispatch.FindingKindGeneral, Intent: dispatch.FindingIntentQuestion, Message: "why the retry?"},
}

// notesFile writes v as JSON to a file of the test's.
func notesFile(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "notes.json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// reviewStandIn is the review routes of CND-1's candidate: the diff, which
// names the run's base; the submit, which answers a request for changes
// applied (or continuation_needed unless running or continueRun says to)
// and anything else none; Request again, applied as try 2 (or
// continuation_needed, the same way). Every body is decoded as the route
// does.
func reviewStandIn(t *testing.T, running bool) *standIn {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/runs/{run}/tasks/{task}/attempts/{n}/diff", func(w http.ResponseWriter, r *http.Request) {
		answer(w, 200, map[string]any{"candidate": rvCommit, "base": rvBase, "files": []any{}, "truncated": false})
	})
	continuation := func(w http.ResponseWriter) {
		answer(w, 409, map[string]any{"error": map[string]any{"code": "continuation_needed", "message": "Yardmaster continues run A"}, "run": rvRunA,
			"words": "Yardmaster continues run " + rvRunA + " as a new run: build's repair attempt 2 starts at " + rvCommit[:12]})
	}
	mux.HandleFunc("POST /api/dispatch/tickets/{key}/reviews", func(w http.ResponseWriter, r *http.Request) {
		var body submitBody
		if !strictBody(t, w, r, &body) {
			return
		}
		app := dispatch.ReviewApplication{State: dispatch.ApplicationNone}
		if body.Verdict == dispatch.VerdictRequestChanges {
			if !running && !body.ContinueRun {
				continuation(w)
				return
			}
			run := rvRunA
			if body.ContinueRun {
				run = rvRunB
			}
			app = dispatch.ReviewApplication{State: dispatch.ApplicationApplied, Try: 1, Run: run, Command: "rv." + rvID + ".1", Revision: 7, Attempt: 2}
		}
		answer(w, 201, map[string]any{"receipt": dispatch.Receipt{OpID: body.OpID, Intent: "review.submit", Status: "done", ID: rvID}, "review": reviewOf(body.NewReview, app)})
	})
	mux.HandleFunc("POST /api/dispatch/reviews/{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		var body retryBody
		if !strictBody(t, w, r, &body) {
			return
		}
		if !running && !body.ContinueRun {
			continuation(w)
			return
		}
		rv := dispatch.NewReview{Candidate: rvCand, Base: rvBase, Verdict: dispatch.VerdictRequestChanges, Findings: rvNotes, Via: dispatch.ViaCLI}
		answer(w, 200, map[string]any{"receipt": dispatch.Receipt{OpID: body.OpID, Intent: "review.retry", Status: "done", ID: r.PathValue("id")},
			"review": reviewOf(rv, dispatch.ReviewApplication{State: dispatch.ApplicationApplied, Try: 2, Run: rvRunA, Command: "rv." + rvID + ".2", Revision: 9, Attempt: 3})})
	})
	return newStandIn(t, mux)
}

// A review from a file of notes: its base read from the candidate's diff
// when not given, its notes sent as the route takes them with via cli and
// a minted operation, a reply lost asked for once more with the same one;
// what became of it printed as two facts; from stdin and with a base
// given, no diff read. What is wrong in the flags is refused before
// anything is sent.
func TestDispatchReviewFromAFile(t *testing.T) {
	s := reviewStandIn(t, true)
	notes := notesFile(t, rvNotes)
	s.loseNext("POST", "/api/dispatch/tickets/CND-1/reviews", 1)
	code, out, errOut, err := s.run(t, "", "dispatch", "review", "CND-1", "--candidate", rvCand, "--verdict", "request_changes", "--summary", "two <things>", "--file", notes)
	if code != 0 || err != nil {
		t.Fatalf("review: %d %v\n%s", code, err, errOut)
	}
	want := "review " + rvID + " of CND-1: request_changes of " + rvCand + " against " + rvBase[:12] + ", 2 notes\n" +
		"recorded; applied: Yardmaster reopened build in run " + rvRunA + ", attempt 2 to start (command rv." + rvID + ".1, the run's revision 7)\n"
	if out != want {
		t.Fatalf("review:\n%q\nwant\n%q", out, want)
	}
	seen := s.requests()
	if len(seen) != 3 || seen[0].method != "GET" || seen[0].path != "/api/runs/"+rvRunA+"/tasks/build/attempts/1/diff" || seen[1].reply != -1 || seen[2].reply == -1 {
		t.Fatalf("the requests: %+v", seen)
	}
	if seen[1].body["opId"] != seen[2].body["opId"] || !rvOpID.MatchString(fmt.Sprint(seen[1].body["opId"])) {
		t.Fatalf("the resend's operation: %v and %v", seen[1].body["opId"], seen[2].body["opId"])
	}
	b, _ := json.Marshal(seen[2].body)
	var sent submitBody
	if err := json.Unmarshal(b, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Candidate != rvCand || sent.Base != rvBase || sent.Verdict != "request_changes" || sent.Summary != "two <things>" || sent.Via != "cli" || sent.ContinueRun ||
		!slices.EqualFunc(sent.Findings, rvNotes, func(a, b dispatch.NewFinding) bool { return fmt.Sprint(a) == fmt.Sprint(b) }) {
		t.Fatalf("the review sent: %+v", sent)
	}
	// From stdin, a base given: no diff read; a comment applies nothing.
	b, _ = json.Marshal(rvNotes[1:])
	code, out, _, err = s.run(t, string(b), "dispatch", "review", "CND-1", "--candidate", rvCand, "--base", rvBase, "--verdict", "comment", "--file", "-")
	if code != 0 || err != nil || out != "review "+rvID+" of CND-1: comment of "+rvCand+" against "+rvBase[:12]+", 1 note\nrecorded; a comment applies nothing\n" {
		t.Fatalf("a comment from stdin: %d %v %q", code, err, out)
	}
	if seen := s.requests()[3:]; len(seen) != 1 || seen[0].method != "POST" || seen[0].body["base"] != rvBase {
		t.Fatalf("the comment's requests: %+v", seen)
	}
	// An approval of its summary alone.
	if code, out, _, err := s.run(t, "", "dispatch", "review", "CND-1", "--candidate", rvCand, "--base", rvBase, "--verdict", "approve", "--summary", "good"); code != 0 || err != nil || !strings.HasSuffix(out, ", 0 notes\nrecorded; an approval applies nothing: it accepts, delivers and closes nothing\n") {
		t.Fatalf("an approval: %d %v %q", code, err, out)
	}
	before := len(s.requests())
	for _, args := range [][]string{
		{"review", "CND-1", "--verdict", "comment", "--summary", "x"},                                         // no candidate
		{"review", "CND-1", "--candidate", rvRunA + "/build/1@abc", "--verdict", "comment", "--summary", "x"}, // a commit cut short
		{"review", "CND-1", "--candidate", rvCand, "--verdict", "lgtm", "--summary", "x"},
		{"review", "CND-1", "--candidate", rvCand, "--base", "HEAD", "--verdict", "comment", "--summary", "x"},
		{"review", "CND-1", "--candidate", rvCand, "--verdict", "comment", "--continue", "--summary", "x"},
		{"review", "CND-1", "--candidate", rvCand, "--verdict", "comment"}, // no notes, no summary
		{"review", "--candidate", rvCand, "--verdict", "comment", "--summary", "x"},
		{"review", "cnd", "--candidate", rvCand, "--verdict", "comment", "--summary", "x"},
		{"review", "CND-1", "CND-2", "--candidate", rvCand, "--verdict", "comment", "--summary", "x"},
		{"review", "CND-1", "--candidate", rvCand, "--verdict", "comment", "--file", filepath.Join(t.TempDir(), "none.json")},
	} {
		if code, _, _, err := s.run(t, "", append([]string{"dispatch"}, args...)...); code != 2 || err == nil {
			t.Errorf("%q: exit %d %v, want 2", args, code, err)
		}
	}
	if n := len(s.requests()); n != before {
		t.Fatalf("%d requests after the usage errors", n-before)
	}
}

// A file of notes is read as the route reads a body: no field the notes
// do not take, one JSON value and nothing after it, an array; each is
// refused before anything is sent, with the file's words.
func TestDispatchReviewRefusesUnknownFields(t *testing.T) {
	s := reviewStandIn(t, true)
	for _, c := range []struct{ body, says string }{
		{`[{"kind":"general","intent":"note","message":"x","colour":"red"}]`, `unknown field "colour"`},
		{`[{"kind":"check","intent":"change","severity":"major","check":{"source":"ledger","receipt":"r","tail":"x"}}]`, `unknown field "tail"`},
		{`[{"kind":"general","intent":"note","message":"x"}] []`, "more after its one JSON value"},
		{`{"findings":[]}`, "cannot unmarshal object"},
		{``, "no JSON value"},
		{`[{"kind":"general","intent":"note","message":"x"`, "unexpected EOF"},
	} {
		p := filepath.Join(t.TempDir(), "notes.json")
		if err := os.WriteFile(p, []byte(c.body), 0o600); err != nil {
			t.Fatal(err)
		}
		code, _, _, err := s.run(t, "", "dispatch", "review", "CND-1", "--candidate", rvCand, "--base", rvBase, "--verdict", "comment", "--file", p)
		if code != 2 || err == nil || !strings.Contains(err.Error(), c.says) || !strings.HasPrefix(err.Error(), "dispatch review: "+p+": ") {
			t.Errorf("%s: exit %d %v, want it to say %q", c.body, code, err, c.says)
		}
	}
	if n := len(s.requests()); n != 0 {
		t.Fatalf("%d requests for notes that are not the route's", n)
	}
}

// reviews follows every page of the ticket's reviews, newest first, each
// with its findings counted and what became of it; review show prints one
// whole, its summary and its findings with their latest marks.
func TestDispatchReviewsAndShow(t *testing.T) {
	mux := http.NewServeMux()
	summary := func(id, verdict string, app dispatch.ReviewApplication, open int) dispatch.ReviewSummary {
		return dispatch.ReviewSummary{ID: id, TicketKey: "CND-1", Candidate: rvCand, Base: rvBase, Verdict: verdict, CreatedBy: "Nate", Actor: "admin", Via: "web", Application: app, Counts: dispatch.FindingCounts{Open: open, Resolved: 1}}
	}
	mux.HandleFunc("GET /api/dispatch/tickets/{key}/reviews", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.PathValue("key") == "CND-2":
			answer(w, 200, dispatch.ReviewsPage{Reviews: []dispatch.ReviewSummary{}})
		case r.URL.Query().Get("before") == "":
			answer(w, 200, dispatch.ReviewsPage{Reviews: []dispatch.ReviewSummary{
				summary("3333333333333333", "request_changes", dispatch.ReviewApplication{State: "refused", Try: 2, Why: "the task's result is now attempt 4: review that candidate"}, 2),
				summary("4444444444444444", "comment", dispatch.ReviewApplication{State: "none"}, 1),
			}, Next: "4444444444444444"})
		default:
			answer(w, 200, dispatch.ReviewsPage{Reviews: []dispatch.ReviewSummary{summary(rvID, "request_changes", dispatch.ReviewApplication{State: "pending", Try: 1}, 0)}})
		}
	})
	mux.HandleFunc("GET /api/dispatch/reviews/{id}", func(w http.ResponseWriter, r *http.Request) {
		v := reviewOf(dispatch.NewReview{Candidate: rvCand, Base: rvBase, Verdict: "request_changes", Summary: "Two things.\n\n- names\n- the retry", Findings: rvNotes, Via: "cli"},
			dispatch.ReviewApplication{State: "applied", Try: 1, Run: rvRunB, Command: "rv." + rvID + ".1", Revision: 7, Attempt: 2})
		v.Findings[0].State, v.Findings[0].Marks = "resolved", 2
		v.Findings[0].Latest = &dispatch.Mark{ID: "5555555555555555", Candidate: rvCand2, Verdict: "resolved", Located: json.RawMessage(`{"state":"moved","from":5,"to":6}`), Note: "renamed", CreatedBy: "Nate", Actor: "admin", Via: "cli"}
		answer(w, 200, map[string]any{"review": v})
	})
	s := newStandIn(t, mux)
	code, out, _, err := s.run(t, "", "dispatch", "reviews", "CND-1")
	if code != 0 || err != nil {
		t.Fatalf("reviews: %d %v", code, err)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 6 || !strings.HasPrefix(lines[0], "3333333333333333\trequest_changes\t"+rvCand+"\t2 open, 1 resolved, 0 withdrawn\t") || !strings.HasSuffix(lines[0], " by Nate via web") ||
		lines[1] != "  recorded; refused (try 2): the task's result is now attempt 4: review that candidate; conductor dispatch review retry 3333333333333333 asks again" ||
		!strings.HasPrefix(lines[2], "4444444444444444\tcomment\t") || lines[3] != "  recorded; a comment applies nothing" ||
		!strings.HasPrefix(lines[4], rvID+"\trequest_changes\t") || lines[5] != "  recorded; pending: Yardmaster's answer is not recorded yet; conductor dispatch review show "+rvID+" reads it again" {
		t.Fatalf("reviews:\n%s", out)
	}
	if seen := s.requests(); len(seen) != 2 || seen[0].query != "" || seen[1].query != "before=4444444444444444" {
		t.Fatalf("the pages asked: %+v", seen)
	}
	if code, out, _, err := s.run(t, "", "dispatch", "reviews", "CND-2"); code != 0 || err != nil || out != "CND-2 has no reviews\n" {
		t.Fatalf("no reviews: %d %v %q", code, err, out)
	}
	code, out, _, err = s.run(t, "", "dispatch", "review", "show", rvID)
	if code != 0 || err != nil {
		t.Fatalf("review show: %d %v", code, err)
	}
	for _, want := range []string{
		"# review " + rvID + " of CND-1\n\n", "- candidate: " + rvCand + "\n", "- base: " + rvBase + "\n", "- verdict: request_changes\n",
		"- application: recorded; applied: Yardmaster reopened build in run " + rvRunB + ", attempt 2 to start (command rv." + rvID + ".1, the run's revision 7)\n",
		"- findings: 2 open, 0 resolved, 0 withdrawn\n", "## Summary\n\nTwo things.\n\n- names\n- the retry\n",
		"## Findings\n\n- 0000000000000001\tresolved\tchange major\tinternal/api/users.go:3-4 (candidate side)\ton " + rvCand + "\n  name them\n  asked: n and m\n",
		"  latest of 2 marks: resolved on " + rvCand2 + ", 0001-01-01 00:00 UTC by Nate via cli; its lines there: moved: now 5-6; renamed\n",
		"- 0000000000000002\topen\tquestion\tthe candidate\ton " + rvCand + "\n  why the retry?\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("review show does not say %q:\n%s", want, out)
		}
	}
	before := len(s.requests())
	for _, args := range [][]string{{"reviews"}, {"reviews", "CND-1", "CND-2"}, {"review", "show"}, {"review", "show", "nope"}, {"review", "show", rvID, rvID}} {
		if code, _, _, err := s.run(t, "", append([]string{"dispatch"}, args...)...); code != 2 || err == nil {
			t.Errorf("%q: exit %d %v, want 2", args, code, err)
		}
	}
	if n := len(s.requests()); n != before {
		t.Fatalf("%d requests after the usage errors", n-before)
	}
}

// findings follows every page with the filter given, each finding with
// where its lines are, what the repair attempts said of it and its last
// mark; finding marks follows every page of a finding's marks; finding
// mark sends the mark as the route takes it, a reply lost asked for once
// more with the same operation, and prints where the server found the
// lines.
func TestDispatchFindingsAndMark(t *testing.T) {
	mux := http.NewServeMux()
	finding := func(id, state string) map[string]any {
		return map[string]any{"id": id, "reviewId": rvID, "k": 1, "kind": "line", "intent": "change", "severity": "minor", "path": "a.go", "side": "base", "from": 7, "to": 7,
			"lines": []string{"old"}, "message": "keep it", "candidate": rvCand, "state": state, "marks": 0}
	}
	mux.HandleFunc("GET /api/dispatch/tickets/{key}/findings", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("key") == "CND-2" {
			answer(w, 200, map[string]any{"findings": []any{}})
			return
		}
		if r.URL.Query().Get("after") == "" {
			f := finding(rvF1, "open")
			f["locatedOn"], f["located"] = rvCand2, map[string]any{"state": "renamed", "path": "b.go", "located": map[string]any{"state": "moved", "from": 9, "to": 9}}
			f["addressed"] = []any{map[string]any{"run": rvRunA, "attempt": 2, "note": "kept it"}, map[string]any{"run": rvRunA, "attempt": 3}}
			answer(w, 200, map[string]any{"findings": []any{f}, "next": rvF1})
			return
		}
		f := finding(rvF2, "open")
		f["locatedOn"], f["located"] = rvCand2, map[string]any{"state": "unknown", "why": "the file is binary"}
		f["latest"] = map[string]any{"id": "5555555555555555", "candidate": rvCand2, "verdict": "open", "note": "not yet", "createdBy": "", "actor": "admin", "via": "web", "createdAt": "2026-11-07T10:00:00Z"}
		answer(w, 200, map[string]any{"findings": []any{f}})
	})
	mux.HandleFunc("GET /api/dispatch/findings/{id}/marks", func(w http.ResponseWriter, r *http.Request) {
		mark := func(n int, verdict string) dispatch.Mark {
			return dispatch.Mark{ID: fmt.Sprintf("%016d", n), FindingID: r.PathValue("id"), Candidate: rvCand2, Verdict: verdict, Note: fmt.Sprintf("mark %d", n), Actor: "admin", Via: "cli"}
		}
		switch r.URL.Query().Get("after") {
		case "":
			answer(w, 200, dispatch.MarksPage{Marks: []dispatch.Mark{mark(1, "open"), mark(2, "resolved")}, Next: fmt.Sprintf("%016d", 2)})
		case fmt.Sprintf("%016d", 2):
			answer(w, 200, dispatch.MarksPage{Marks: []dispatch.Mark{mark(3, "open")}, Next: fmt.Sprintf("%016d", 3)})
		default:
			answer(w, 200, dispatch.MarksPage{Marks: []dispatch.Mark{mark(4, "withdrawn")}})
		}
	})
	mux.HandleFunc("POST /api/dispatch/findings/{id}/marks", func(w http.ResponseWriter, r *http.Request) {
		var body markBody
		if !strictBody(t, w, r, &body) {
			return
		}
		answer(w, 201, map[string]any{"receipt": dispatch.Receipt{OpID: body.OpID, Intent: "finding.mark", Status: "done", ID: "6666666666666666"},
			"mark": dispatch.Mark{ID: "6666666666666666", FindingID: r.PathValue("id"), Candidate: body.Candidate, Verdict: body.Verdict, Note: body.Note, Located: json.RawMessage(`{"state":"same"}`), Actor: "admin", Via: body.Via}})
	})
	s := newStandIn(t, mux)
	code, out, _, err := s.run(t, "", "dispatch", "findings", "CND-1", "--state", "open", "--candidate", rvCand2)
	if code != 0 || err != nil {
		t.Fatalf("findings: %d %v", code, err)
	}
	want := "- " + rvF1 + "\topen\tchange minor\ta.go:7 (base side)\ton " + rvCand + "\n  keep it\n" +
		"  on " + rvCand2 + ": renamed to b.go, moved: now 9-9\n" +
		"  the agent says (attempt 2 of run " + rvRunA + "): kept it\n  the agent says (attempt 3 of run " + rvRunA + "): addressed\n" +
		"- " + rvF2 + "\topen\tchange minor\ta.go:7 (base side)\ton " + rvCand + "\n  keep it\n" +
		"  on " + rvCand2 + ": not known: the file is binary\n" +
		"  last mark: open on " + rvCand2 + ", 2026-11-07 10:00 UTC by admin via web; not yet\n"
	if out != want {
		t.Fatalf("findings:\n%s\nwant\n%s", out, want)
	}
	first, next := url.Values{"state": {"open"}, "candidate": {rvCand2}}, url.Values{"state": {"open"}, "candidate": {rvCand2}, "after": {rvF1}}
	if seen := s.requests(); len(seen) != 2 || seen[0].query != first.Encode() || seen[1].query != next.Encode() {
		t.Fatalf("the pages asked: %+v", seen)
	}
	if code, out, _, err := s.run(t, "", "dispatch", "findings", "CND-2", "--state", "withdrawn"); code != 0 || err != nil || out != "CND-2 has no withdrawn findings\n" {
		t.Fatalf("no findings: %d %v %q", code, err, out)
	}
	// Every page of a finding's marks.
	before := len(s.requests())
	code, out, _, err = s.run(t, "", "dispatch", "finding", "marks", rvF1)
	if code != 0 || err != nil {
		t.Fatalf("marks: %d %v", code, err)
	}
	if lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n"); len(lines) != 4 || !strings.HasPrefix(lines[0], "0000000000000001\topen on "+rvCand2+", ") || !strings.HasSuffix(lines[3], "by admin via cli; mark 4") || !strings.Contains(lines[3], "\twithdrawn on ") {
		t.Fatalf("marks:\n%s", out)
	}
	if seen := s.requests()[before:]; len(seen) != 3 || seen[0].query != "" || seen[1].query != "after=0000000000000002" || seen[2].query != "after=0000000000000003" {
		t.Fatalf("the marks' pages asked: %+v", seen)
	}
	// A mark, its reply lost: sent once more with the same operation.
	before = len(s.requests())
	s.loseNext("POST", "/api/dispatch/findings/"+rvF1+"/marks", 1)
	code, out, _, err = s.run(t, "", "dispatch", "finding", "mark", rvF1, "--candidate", rvCand2, "--verdict", "resolved", "--note", "done <here>")
	if code != 0 || err != nil || out != "finding "+rvF1+" marked resolved on "+rvCand2+"; its lines there: same lines\n" {
		t.Fatalf("mark: %d %v %q", code, err, out)
	}
	seen := s.requests()[before:]
	if len(seen) != 2 || seen[0].reply != -1 || seen[0].body["opId"] != seen[1].body["opId"] || !rvOpID.MatchString(fmt.Sprint(seen[1].body["opId"])) ||
		seen[1].body["candidate"] != rvCand2 || seen[1].body["verdict"] != "resolved" || seen[1].body["note"] != "done <here>" || seen[1].body["via"] != "cli" {
		t.Fatalf("the mark's posts: %+v", seen)
	}
	before = len(s.requests())
	for _, args := range [][]string{
		{"findings", "CND-1", "--state", "closed"}, {"findings", "CND-1", "--candidate", "build#2"}, {"findings"},
		{"finding", "marks"}, {"finding", "marks", "x"}, {"finding", "frob", rvF1}, {"finding"},
		{"finding", "mark", rvF1, "--verdict", "resolved"}, {"finding", "mark", rvF1, "--candidate", rvCand2, "--verdict", "fixed"},
		{"finding", "mark", "--candidate", rvCand2, "--verdict", "open"},
	} {
		if code, _, _, err := s.run(t, "", append([]string{"dispatch"}, args...)...); code != 2 || err == nil {
			t.Errorf("%q: exit %d %v, want 2", args, code, err)
		}
	}
	if n := len(s.requests()); n != before {
		t.Fatalf("%d requests after the usage errors", n-before)
	}
}

// review retry is Request again: a minted operation, a reply lost asked for
// once more with the same one, and what became of the try printed.
func TestDispatchReviewRetry(t *testing.T) {
	s := reviewStandIn(t, true)
	s.loseNext("POST", "/api/dispatch/reviews/"+rvID+"/retry", 1)
	code, out, _, err := s.run(t, "", "dispatch", "review", "retry", rvID)
	want := "review " + rvID + " of CND-1 sent again: request_changes of " + rvCand + " against " + rvBase[:12] + ", 2 notes\n" +
		"recorded; applied (try 2): Yardmaster reopened build in run " + rvRunA + ", attempt 3 to start (command rv." + rvID + ".2, the run's revision 9)\n"
	if code != 0 || err != nil || out != want {
		t.Fatalf("retry: %d %v\n%q\nwant\n%q", code, err, out, want)
	}
	seen := s.requests()
	if len(seen) != 2 || seen[0].reply != -1 || seen[0].body["opId"] != seen[1].body["opId"] || !rvOpID.MatchString(fmt.Sprint(seen[1].body["opId"])) || len(seen[1].body) != 1 {
		t.Fatalf("the retry's posts: %+v", seen)
	}
	if code, _, _, err := s.run(t, "", "dispatch", "review", "retry"); code != 2 || err == nil {
		t.Fatalf("a retry of no review: %d %v", code, err)
	}
	if code, _, _, err := s.run(t, "", "dispatch", "review", "retry", "CND-1"); code != 2 || err == nil {
		t.Fatalf("a retry of a ticket: %d %v", code, err)
	}
	if n := len(s.requests()); n != 2 {
		t.Fatalf("%d requests after the usage errors", n-2)
	}
}

// A request for changes to a run the server does not run: without
// --continue the answer's words are printed, exit 1, and nothing is sent
// again, for a review and for Request again; with it, the request says to
// continue the run and is applied there.
func TestDispatchReviewContinue(t *testing.T) {
	s := reviewStandIn(t, false)
	notes := notesFile(t, rvNotes)
	words := "run " + rvRunA + " is not running: Yardmaster continues run " + rvRunA + " as a new run: build's repair attempt 2 starts at " + rvCommit[:12] + "\n"
	code, out, _, err := s.run(t, "", "dispatch", "review", "CND-1", "--candidate", rvCand, "--base", rvBase, "--verdict", "request_changes", "--file", notes)
	if code != 1 || err == nil || !strings.HasPrefix(err.Error(), "continuation_needed: ") || !strings.Contains(err.Error(), "--continue") ||
		out != words+"nothing was recorded: send it again with --continue to continue the run with the repair\n" {
		t.Fatalf("without --continue: %d %v %q", code, err, out)
	}
	if seen := s.requests(); len(seen) != 1 || seen[0].body["continueRun"] != nil {
		t.Fatalf("sent again without --continue: %+v", seen)
	}
	code, out, _, err = s.run(t, "", "dispatch", "review", "CND-1", "--candidate", rvCand, "--base", rvBase, "--verdict", "request_changes", "--file", notes, "--continue")
	if code != 0 || err != nil || !strings.Contains(out, "recorded; applied: Yardmaster reopened build in run "+rvRunB+", attempt 2 to start") {
		t.Fatalf("with --continue: %d %v %q", code, err, out)
	}
	if seen := s.requests(); len(seen) != 2 || seen[1].body["continueRun"] != true {
		t.Fatalf("the review with --continue: %+v", seen)
	}
	// Request again, the same rule.
	code, out, _, err = s.run(t, "", "dispatch", "review", "retry", rvID)
	if code != 1 || err == nil || !strings.HasPrefix(err.Error(), "continuation_needed: ") || out != words+"nothing was sent: send it again with --continue to continue the run with the repair\n" {
		t.Fatalf("retry without --continue: %d %v %q", code, err, out)
	}
	if seen := s.requests(); len(seen) != 3 || seen[2].body["continueRun"] != nil {
		t.Fatalf("Request again sent again without --continue: %+v", seen)
	}
	if code, out, _, err := s.run(t, "", "dispatch", "review", "retry", "--continue", rvID); code != 0 || err != nil || !strings.Contains(out, "recorded; applied (try 2)") {
		t.Fatalf("retry with --continue: %d %v %q", code, err, out)
	}
	if seen := s.requests(); len(seen) != 4 || seen[3].body["continueRun"] != true {
		t.Fatalf("Request again with --continue: %+v", seen)
	}
}

// crew recheck names the failed candidate in its own run: the route of
// that run and task, the attempt and the commit, a minted command id that
// a lost reply is sent again with; it prints the run and the attempt the
// server checked it in, a continuation's when the run was continued since;
// a candidate that moved on is the server's refusal, exit 1.
func TestCrewRecheck(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/runs/{run}/tasks/{task}/recheck", func(w http.ResponseWriter, r *http.Request) {
		var body recheckBody
		if !strictBody(t, w, r, &body) {
			return
		}
		switch {
		case body.Attempt == 1:
			answer(w, 409, map[string]any{"error": map[string]any{"code": "stale_candidate", "message": "build#1 is no longer build's standing failed candidate: attempt 2 is"}, "run": rvRunA,
				"receipt": crew.Receipt{CommandID: body.ID, Status: "failed", Refusal: "stale_candidate"}})
		case r.PathValue("run") == rvRunA && body.Attempt == 3:
			// A was continued as B: checked again there.
			answer(w, 200, map[string]any{"receipt": crew.Receipt{CommandID: body.ID, Status: "done", Task: &crew.ReceiptTask{ID: "build", State: "verifying", Attempts: 4}}, "run": rvRunB, "attempt": 4})
		default:
			answer(w, 200, map[string]any{"receipt": crew.Receipt{CommandID: body.ID, Status: "done", Task: &crew.ReceiptTask{ID: "build", State: "verifying", Attempts: 3}}, "run": r.PathValue("run"), "attempt": 3})
		}
	})
	s := newStandIn(t, mux)
	two := rvRunA + "/build/2@" + rvCommit
	s.loseNext("POST", "/api/runs/"+rvRunA+"/tasks/build/recheck", 1)
	code, out, _, err := s.run(t, "", "crew", "recheck", two)
	if code != 0 || err != nil || out != "build#2 checked again as attempt 3 of run "+rvRunA+"\n" {
		t.Fatalf("recheck: %d %v %q", code, err, out)
	}
	seen := s.requests()
	if len(seen) != 2 || seen[0].reply != -1 || seen[0].body["commandId"] != seen[1].body["commandId"] || !rvOpID.MatchString(fmt.Sprint(seen[1].body["commandId"])) ||
		seen[1].body["attempt"] != 2.0 || seen[1].body["commit"] != rvCommit || seen[1].path != "/api/runs/"+rvRunA+"/tasks/build/recheck" {
		t.Fatalf("the recheck's posts: %+v", seen)
	}
	// The candidate moved on: the server's refusal and its words.
	if code, _, _, err := s.run(t, "", "crew", "recheck", rvCand); code != 1 || err == nil || err.Error() != "stale_candidate: build#1 is no longer build's standing failed candidate: attempt 2 is" {
		t.Fatalf("a candidate that moved: %d %v", code, err)
	}
	// After a Continue: A's candidate named, checked again in B.
	code, out, _, err = s.run(t, "", "crew", "recheck", rvRunA+"/build/3@"+rvCommit)
	if code != 0 || err != nil || out != "build#3 checked again as attempt 4 of run "+rvRunB+", a continuation of "+rvRunA+"\n" {
		t.Fatalf("after a Continue: %d %v %q", code, err, out)
	}
	if seen := s.requests(); seen[len(seen)-1].path != "/api/runs/"+rvRunA+"/tasks/build/recheck" {
		t.Fatalf("the continued candidate's post: %+v", seen[len(seen)-1])
	}
	before := len(s.requests())
	for _, args := range [][]string{{"crew", "recheck"}, {"crew", "recheck", rvRunA + "/build/2"}, {"crew", "recheck", two, two}} {
		if code, _, _, err := s.run(t, "", args...); code != 2 || err == nil {
			t.Errorf("%q: exit %d %v, want 2", args, code, err)
		}
	}
	if n := len(s.requests()); n != before {
		t.Fatalf("%d requests after the usage errors", n-before)
	}
	// Its usage is crew's, with the server's flags.
	var errb bytes.Buffer
	if code, err := Run(t.Context(), []string{"crew", "recheck", "-h"}, strings.NewReader(""), io.Discard, &errb); code != 0 || err != nil || !strings.Contains(errb.String(), "conductor crew recheck RUN/TASK/N@COMMIT") || !strings.Contains(errb.String(), "-token") {
		t.Fatalf("crew recheck -h: %d %v\n%s", code, err, errb.String())
	}
}

// The commands reach the real routes, which take every body they send:
// each answer is the route's judgement of what it names (no such run,
// review or finding; a ticket that holds no run), never invalid_request.
func TestDispatchReviewCommandsReachTheRoutes(t *testing.T) {
	a := newDispatchAPI(t)
	a.admin(t, "POST", "/api/dispatch/projects", map[string]any{"opId": "p1", "key": "CND", "name": "Conductor"}, http.StatusCreated)
	a.admin(t, "POST", "/api/dispatch/tickets", map[string]any{"opId": "t1", "project": "CND", "title": "Review it"}, http.StatusCreated)
	if out := a.mustDispatch(t, "reviews", "CND-1"); out != "CND-1 has no reviews\n" {
		t.Fatalf("reviews: %q", out)
	}
	if out := a.mustDispatch(t, "findings", "CND-1", "--state", "all"); out != "CND-1 has no findings\n" {
		t.Fatalf("findings: %q", out)
	}
	notes := notesFile(t, rvNotes)
	for _, c := range []struct {
		args []string
		code string
	}{
		{[]string{"review", "CND-1", "--candidate", rvCand, "--base", rvBase, "--verdict", "comment", "--summary", "s", "--file", notes}, "not_ticket_run"},
		{[]string{"review", "show", rvID}, "not_found"},
		{[]string{"review", "retry", rvID, "--continue"}, "not_found"},
		{[]string{"finding", "mark", rvF1, "--candidate", rvCand2, "--verdict", "resolved", "--note", "n"}, "not_found"},
	} {
		sub, args := c.args[0], c.args[1:]
		code, _, _, err := a.dispatchCmd(t, "", sub, args...)
		if code != 1 || err == nil || !strings.HasPrefix(err.Error(), c.code+": ") {
			t.Errorf("%q: exit %d %v, want %s", c.args, code, err, c.code)
		}
	}
	var out, errb bytes.Buffer
	if code, err := Run(t.Context(), []string{"crew", "recheck", rvCand, "--server", a.hs.URL, "--token", dispatchToken}, strings.NewReader(""), &out, &errb); code != 1 || err == nil || !strings.HasPrefix(err.Error(), "not_found: ") {
		t.Fatalf("crew recheck of no run: %d %v", code, err)
	}
}

// The guide's CLI forms of the review operations and of Check again are
// commands: each one's -h exits 0, and its usage carries the form as far
// as its first flag and that flag's value.
func TestTheReviewOperationsCLIFormsAreCommands(t *testing.T) {
	clearConductorEnv(t)
	names := []string{"review_submit", "review_retry", "reviews", "review", "findings", "finding_marks", "finding_mark", "task_recheck"}
	for _, op := range api.Operations() {
		if !slices.Contains(names, op.Name) {
			continue
		}
		names = slices.DeleteFunc(names, func(n string) bool { return n == op.Name })
		if len(op.CLI) < 3 || op.CLI[0] != "conductor" {
			t.Errorf("%s: no CLI form: %q", op.Name, op.CLI)
			continue
		}
		var words []string
		form := op.CLI
		for i, w := range op.CLI {
			if strings.HasPrefix(w, "--") {
				form = op.CLI[:min(i+2, len(op.CLI))]
				break
			}
			if w != strings.ToLower(w) || i == 0 {
				continue
			}
			if len(words) == i-1 {
				words = append(words, w)
			}
		}
		var errb bytes.Buffer
		if code, err := Run(t.Context(), append(words, "-h"), strings.NewReader(""), io.Discard, &errb); code != 0 || err != nil {
			t.Errorf("%s: %q -h: exit %d %v", op.Name, words, code, err)
		}
		if want := strings.Join(form, " "); !strings.Contains(errb.String(), want) {
			t.Errorf("%s: the usage does not say %q:\n%s", op.Name, want, errb.String())
		}
	}
	if len(names) > 0 {
		t.Fatalf("not in the table: %q", names)
	}
}
