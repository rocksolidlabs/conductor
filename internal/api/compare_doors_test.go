package api

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// compareEnv is a server with Dispatch's store running a comparison's run
// (two candidates, approach-a by cand-a and approach-b by cand-b, each
// reviewed, review-a by rev-a and review-b by rev-b) beside an ordinary
// managed run (build by lead, docs by writer): approach-a's, approach-b's
// and build's sessions, each with its token. Both runs are stopped when the
// test ends, before its folders go.
type compareEnv struct {
	*testEnv
	run, plain           string
	a, b, member         string
	aTok, bTok, plainTok string
}

func newCompareEnv(t *testing.T) *compareEnv {
	t.Helper()
	e := readyEnv(t, func(c *config.Config) { c.DataDir = filepath.Join(t.TempDir(), "data") })
	e.gitRepo(t)
	// Every refusal is the token's: the per-client limiter would answer
	// some with 429.
	e.srv.limiter = newRateLimiter(1e6, 1e6)
	c, err := crew.CompareTemplate(crew.CompareSpec{Name: "Compare", Cwd: e.root, Goal: "Make the lookup fast.", Instructions: "Make it fast.",
		Criteria:   []crew.Criterion{{ID: "fast", Text: "The lookup is fast."}},
		Checks:     []crew.Check{{ID: "ok", Argv: []string{"true"}}},
		Candidates: []crew.CandidateSpec{{AgentID: "ready-cat", Approach: "Cache every answer."}, {AgentID: "ready-cat", Approach: "Index the table."}},
		Reviews:    true, ReviewAgent: "ready-cat"})
	if err != nil {
		t.Fatal(err)
	}
	c.ID = "cmp"
	e.srv.runs.SetCompareOpenForTest(true)
	ce := &compareEnv{testEnv: e}
	for _, cr := range []crew.Crew{c, managedCrew(e.root)} {
		run, release, err := e.srv.runs.LaunchNamed(t.Context(), cr, "")
		release()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { e.do("POST", "/api/runs/"+run.ID+"/stop", adminToken, nil) })
		if cr.Compare != nil {
			ce.run = run.ID
		} else {
			ce.plain = run.ID
		}
	}
	session := func(run, task string) (string, string) {
		sid := runningAttempt(t, e, run, task, 1)["sessionId"].(string)
		return sid, e.agentToken(sid)
	}
	ce.a, ce.aTok = session(ce.run, "approach-a")
	ce.b, ce.bTok = session(ce.run, "approach-b")
	ce.member, ce.plainTok = session(ce.plain, "build")
	return ce
}

// ticket is a ticket both runs are linked to.
func (ce *compareEnv) ticket(t *testing.T) string {
	t.Helper()
	ce.newDispatchProject(t, "CMP", nil)
	key := ce.newDispatchTicket(t, "CMP", "Fast lookup", nil)["key"].(string)
	for _, run := range []string{ce.run, ce.plain} {
		ce.expect(t, "POST", "/api/dispatch/tickets/"+key+"/links", map[string]any{"opId": opID(), "kind": "run", "ref": run}, http.StatusCreated, "")
	}
	return key
}

// approach-b's session reads its run as its lane: the run's id, name,
// goal and state, its own lane's members and tasks, the block with its own
// candidate; nothing of approach-a's lane and no log, whichever token
// asks. An ordinary run's member reads its whole run as before.
func TestRunStatusShowsOneLane(t *testing.T) {
	ce := newCompareEnv(t)
	aWorktree := runningAttempt(t, ce.testEnv, ce.run, "approach-a", 1)["worktree"].(string)
	for _, tok := range []string{ce.bTok, adminToken} {
		_, raw, out := ce.callAs(t, tok, "GET", "/api/sessions/"+ce.b+"/run", nil)
		run, _ := out["run"].(map[string]any)
		if out["member"] != "cand-b" || run["id"] != ce.run || run["name"] != "Compare" || run["goal"] != "Make the lookup fast." || run["state"] != crew.RunRunning {
			t.Fatalf("the lane's run: %s", raw)
		}
		var keys []string
		for k := range run {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		if !slices.Equal(keys, []string{"compare", "goal", "id", "members", "name", "state", "tasks"}) {
			t.Fatalf("the lane's run has %v", keys)
		}
		if got := fieldsOf(run["members"], "name"); !slices.Equal(got, []string{"cand-b", "rev-b"}) {
			t.Fatalf("members %v", got)
		}
		if got := fieldsOf(run["tasks"], "id"); !slices.Equal(got, []string{"approach-b", "review-b"}) {
			t.Fatalf("tasks %v", got)
		}
		cmp, _ := run["compare"].(map[string]any)
		if cands, _ := cmp["candidates"].([]any); len(cands) != 1 || cands[0].(map[string]any)["task"] != "approach-b" || cands[0].(map[string]any)["approach"] != "Index the table." || len(cmp["criteria"].([]any)) != 1 {
			t.Fatalf("the block: %v", cmp)
		}
		for _, other := range []string{"approach-a", "review-a", "cand-a", "rev-a", "Cache every answer.", ce.a, aWorktree} {
			if strings.Contains(string(raw), other) {
				t.Fatalf("the lane's run names %q: %s", other, raw)
			}
		}
	}
	_, raw, out := ce.callAs(t, ce.plainTok, "GET", "/api/sessions/"+ce.member+"/run", nil)
	run, _ := out["run"].(map[string]any)
	if _, log := run["log"]; !log || !slices.Equal(fieldsOf(run["tasks"], "id"), []string{"build", "docs"}) || out["member"] != "lead" {
		t.Fatalf("an ordinary run's member: %s", raw)
	}
}

// fieldsOf is the field of each object of a JSON list.
func fieldsOf(list any, field string) []string {
	var out []string
	for _, v := range list.([]any) {
		s, _ := v.(map[string]any)[field].(string)
		out = append(out, s)
	}
	return out
}

// approach-b's ticket read leaves out the comments approach-a's lane's
// members made and the links naming approach-a's lane's branches or
// candidates, with their reads; a person's comments, its own lane's and
// another run's member's stay. An ordinary run's member reads them all.
func TestTicketReadHidesOtherLanes(t *testing.T) {
	ce := newCompareEnv(t)
	key := ce.ticket(t)
	ce.expect(t, "POST", "/api/dispatch/tickets/"+key+"/comments", map[string]any{"opId": opID(), "body": "a person's"}, http.StatusCreated, "")
	for _, actor := range []string{"agent:cand-a", "agent:rev-a", "agent:cand-b", "agent:lead"} {
		if _, err := ce.srv.dispatch.AddComment(context.Background(), dispatch.Op{Actor: actor, ID: opID()}, key, "by "+actor); err != nil {
			t.Fatal(err)
		}
	}
	branch := func(task string) string { return "crew/" + ce.run + "/" + task + ".1" }
	links := map[string]map[string]any{
		"a-url":     {"kind": "delivery", "url": "https://example.test/compare/main..." + branch("approach-a")},
		"a-escaped": {"kind": "delivery", "url": "https://example.test/tree/" + strings.ReplaceAll(branch("review-a"), "/", "%2F")},
		"a-note":    {"kind": "delivery", "url": "https://example.test/pull/1", "note": "from " + branch("approach-a")},
		"b-url":     {"kind": "delivery", "url": "https://example.test/compare/main..." + branch("approach-b")},
	}
	for name, l := range links {
		l["opId"] = opID()
		l["ref"] = name
		ce.expect(t, "POST", "/api/dispatch/tickets/"+key+"/links", l, http.StatusCreated, "")
	}
	read := func(tok, sid string) (authors, refs []string) {
		tk := entityOf(t, ce.expectAs(t, tok, "GET", "/api/sessions/"+sid+"/ticket", nil, http.StatusOK, ""), "ticket")
		authors, refs = fieldsOf(tk["comments"], "author"), fieldsOf(tk["links"], "ref")
		slices.Sort(authors)
		slices.Sort(refs)
		return authors, refs
	}
	kept := []string{"b-url", ce.plain, ce.run}
	slices.Sort(kept)
	for _, tok := range []string{ce.bTok, adminToken} {
		authors, refs := read(tok, ce.b)
		if !slices.Equal(authors, []string{adminActor, "agent:cand-b", "agent:lead"}) || !slices.Equal(refs, kept) {
			t.Fatalf("approach-b's read: %v %v", authors, refs)
		}
	}
	authors, refs := read(ce.plainTok, ce.member)
	if len(authors) != 5 || len(refs) != 6 {
		t.Fatalf("an ordinary run's member's read: %v %v", authors, refs)
	}

	// A delivery's candidate of another lane, and the link's read, go too.
	lane, ok := ce.srv.runs.LaneOf(ce.b)
	if !ok {
		t.Fatal("approach-b's session is in no lane")
	}
	commit := strings.Repeat("a", 40)
	v := laneTicket(dispatch.TicketView{
		Links:     []dispatch.Link{{ID: "l-a", Candidate: ce.run + "/approach-a/1@" + commit}, {ID: "l-b", Candidate: ce.run + "/approach-b/1@" + commit}},
		LinkReads: map[string]dispatch.LinkReadSummary{"l-a": {State: "open"}, "l-b": {State: "open"}},
	}, lane)
	if len(v.Links) != 1 || v.Links[0].ID != "l-b" || len(v.LinkReads) != 1 || v.LinkReads["l-b"].State != "open" {
		t.Fatalf("the candidates' links: %+v %+v", v.Links, v.LinkReads)
	}
}

// Every lane's session is refused a ticket comment, 409 lane, whichever
// token asks, and nothing is written; an ordinary run's member comments.
func TestTicketCommentIsRefusedToALane(t *testing.T) {
	ce := newCompareEnv(t)
	key := ce.ticket(t)
	for _, s := range []struct{ sid, tok, actor string }{{ce.a, ce.aTok, "agent:cand-a"}, {ce.b, ce.bTok, "agent:cand-b"}} {
		for _, tok := range []string{s.tok, adminToken} {
			op := opID()
			out := ce.expectAs(t, tok, "POST", "/api/sessions/"+s.sid+"/ticket/comments", map[string]any{"opId": op, "body": "mine is better"}, http.StatusConflict, "lane")
			if msg := apiMessage(out); msg != "a candidate of a comparison does not comment on its ticket before the selection: say it in your result's summary" {
				t.Fatalf("the refusal: %q", msg)
			}
			if _, found, err := ce.srv.dispatch.Receipt(context.Background(), s.actor, op); err != nil || found {
				t.Fatalf("a receipt for the refused comment: %v %v", found, err)
			}
		}
	}
	if cs := entityOf(t, ce.expect(t, "GET", "/api/dispatch/tickets/"+key, nil, http.StatusOK, ""), "ticket")["comments"].([]any); len(cs) != 0 {
		t.Fatalf("the ticket's comments: %v", cs)
	}
	ce.expectAs(t, ce.plainTok, "POST", "/api/sessions/"+ce.member+"/ticket/comments", map[string]any{"opId": opID(), "body": "built"}, http.StatusCreated, "")
}

// agentScopes are the scopes of the operations an agent's token calls.
var agentScopes = []string{"session", "task", "ticket", "observe", "act"}

// Every route an agent's token may call has its rule in compareDoors, and
// every entry is such a route: the guide's operations of the agent's
// scopes, the granted acts' routes and Brakeman's shared reads. DR1's
// decision tools and the self-service that forms or grows a crew are
// refused; a candidate's token is answered so by them, its own tools
// serve it, and an ordinary run's member is served by each.
func TestEveryAgentDoorIsListed(t *testing.T) {
	routes := map[string]bool{}
	for _, op := range operationsTable() {
		if slices.Contains(agentScopes, op.Scope) && op.Path != "" {
			routes[op.Method+" "+op.Path] = true
		}
	}
	for p := range actRoutes {
		routes[p] = true
	}
	for _, sr := range brakemanReads {
		routes[sr.pattern] = true
	}
	for p := range routes {
		if _, ok := compareDoors[p]; !ok {
			t.Errorf("%s has no entry in compareDoors", p)
		}
	}
	for p := range compareDoors {
		if !routes[p] {
			t.Errorf("compareDoors' %s is no route an agent's token calls", p)
		}
	}
	for _, p := range []string{"POST /api/sessions/{id}/decisions", "GET /api/sessions/{id}/decisions", "GET /api/sessions/{id}/decisions/{did}",
		"POST /api/sessions/{id}/decisions/{did}/withdraw", "POST /api/sessions/{id}/crew", "POST /api/sessions/{id}/run/members"} {
		if rule, ok := compareDoors[p]; !ok || rule != doorRefused {
			t.Errorf("%s is %v, not refused", p, rule)
		}
	}

	ce := newCompareEnv(t)
	for _, c := range []struct {
		method, path string
		body         any
		// plain is what an ordinary run's member is answered.
		status int
		code   string
	}{
		{"POST", "/decisions", map[string]any{"requestId": "r1", "type": "clarification", "question": "Which?"}, http.StatusCreated, ""},
		{"GET", "/decisions", nil, http.StatusOK, ""},
		{"GET", "/decisions/dr-404", nil, http.StatusNotFound, "not_found"},
		{"POST", "/decisions/dr-404/withdraw", map[string]any{}, http.StatusNotFound, "not_found"},
		{"POST", "/crew", map[string]any{"name": "x", "self": "me", "members": []any{map[string]any{"name": "me", "agentId": "ready-cat"}}}, http.StatusConflict, "in_a_run"},
		{"POST", "/run/members", map[string]any{"name": "x", "agentId": "ready-cat", "start": map[string]any{"when": "manual"}}, http.StatusConflict, "managed_run"},
	} {
		ce.expectAs(t, ce.bTok, c.method, "/api/sessions/"+ce.b+c.path, c.body, http.StatusUnauthorized, "unauthorized")
		ce.expectAs(t, ce.plainTok, c.method, "/api/sessions/"+ce.member+c.path, c.body, c.status, c.code)
	}
	for _, c := range []struct {
		method, path string
		body         any
		status       int
	}{
		{"POST", "/events", map[string]any{"type": "progress", "message": "halfway"}, http.StatusAccepted},
		{"POST", "/attention", map[string]any{"state": "working"}, http.StatusOK},
		{"GET", "/guide", nil, http.StatusOK},
		{"POST", "/links/agent", map[string]any{}, http.StatusCreated},
		{"GET", "/task", nil, http.StatusOK},
	} {
		for _, s := range []struct{ sid, tok string }{{ce.b, ce.bTok}, {ce.member, ce.plainTok}} {
			ce.expectAs(t, s.tok, c.method, "/api/sessions/"+s.sid+c.path, c.body, c.status, "")
		}
	}
	// An admin route a Brakeman turn's token reads refuses a candidate's
	// token as any agent's.
	ce.expectAs(t, ce.bTok, "GET", "/api/runs/"+ce.run, nil, http.StatusUnauthorized, "")
}

// The session read and the files route take no agent token: a candidate's
// and an ordinary run's member's are refused 401, as every agent's.
func TestTheSessionReadsTakeNoAgentToken(t *testing.T) {
	ce := newCompareEnv(t)
	for _, s := range []struct{ sid, tok string }{{ce.a, ce.aTok}, {ce.b, ce.bTok}, {ce.member, ce.plainTok}} {
		for _, path := range []string{"/api/sessions/" + s.sid, "/api/sessions/" + s.sid + "/files?path=README"} {
			ce.expectAs(t, s.tok, "GET", path, nil, http.StatusUnauthorized, "unauthorized")
		}
	}
}

// No file of the package takes a session's agent token but agentTokenOK,
// where a comparison's lanes are held to compareDoors.
func TestEveryAgentTokenPathIsGuarded(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	inGuard := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, _ := decl.(*ast.FuncDecl)
			ast.Inspect(decl, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "AgentTokenOK" {
					if fn == nil || fn.Name.Name != "agentTokenOK" || fn.Recv == nil {
						t.Errorf("%s: an agent token is taken outside agentTokenOK", fset.Position(call.Pos()))
					} else {
						inGuard++
					}
				}
				return true
			})
		}
	}
	if inGuard == 0 {
		t.Fatal("agentTokenOK takes no agent token")
	}
}

// Three routes no entry names, one through taskSession, one through
// attentionPrincipal and one through agentTokenOK itself, refuse a
// candidate's token 401 and serve an ordinary run's member.
func TestAnUnlistedDoorIsClosedToALane(t *testing.T) {
	ce := newCompareEnv(t)
	s := ce.srv
	mux := http.NewServeMux()
	served := func(w http.ResponseWriter) { writeJSON(w, http.StatusOK, map[string]any{"served": true}) }
	mux.HandleFunc("GET /api/sessions/{id}/unlisted-task", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.taskSession(w, r, false); ok {
			served(w)
		}
	})
	mux.HandleFunc("GET /api/sessions/{id}/unlisted-attention", func(w http.ResponseWriter, r *http.Request) {
		if d, ok := s.reportTarget(w, r); ok {
			if _, ok := s.attentionPrincipal(w, r, d); ok {
				served(w)
			}
		}
	})
	mux.HandleFunc("GET /api/sessions/{id}/unlisted-token", func(w http.ResponseWriter, r *http.Request) {
		if d, ok := s.registry.Get(r.PathValue("id")); ok && s.agentTokenOK(r, d) {
			served(w)
			return
		}
		writeError(w, http.StatusUnauthorized, "unauthorized", "agent token required")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	call := func(tok, sid, route string) int {
		req, _ := http.NewRequest("GET", srv.URL+"/api/sessions/"+sid+"/"+route, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for _, route := range []string{"unlisted-task", "unlisted-attention", "unlisted-token"} {
		if got := call(ce.aTok, ce.a, route); got != http.StatusUnauthorized {
			t.Errorf("%s: a candidate's token answered %d", route, got)
		}
		if got := call(ce.plainTok, ce.member, route); got != http.StatusOK {
			t.Errorf("%s: an ordinary run's member's token answered %d", route, got)
		}
	}
}
