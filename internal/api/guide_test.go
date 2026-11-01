package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/version"
)

// guideOf reads a guide's reply with token: its status and its JSON.
func guideOf(t *testing.T, e *testEnv, token, path string) (int, []byte, map[string]any) {
	t.Helper()
	return e.callAs(t, token, "GET", path, nil)
}

// opNames is the names of a guide's operations section.
func opNames(t *testing.T, out map[string]any) []string {
	t.Helper()
	ops, ok := out["operations"].([]any)
	if !ok {
		t.Fatalf("no operations in %v", out)
	}
	var names []string
	for _, op := range ops {
		names = append(names, op.(map[string]any)["name"].(string))
	}
	return names
}

// The overview names this build, the caller and every section with the
// size of its reply; a section is answered alone, an unknown one is 404
// no_section and another parameter 400.
func TestTheGuideNamesThisBuild(t *testing.T) {
	e := newTestEnv(t, nil)
	status, _, out := guideOf(t, e, adminToken, "/api/guide")
	if status != http.StatusOK || out["guide"] != float64(1) || out["version"] != version.Version || out["commit"] != version.Commit || out["section"] != "overview" {
		t.Fatalf("overview: %d %v", status, out)
	}
	ov := out["overview"].(map[string]any)
	if c := ov["caller"].(map[string]any); c["kind"] != "admin" || fmt.Sprint(c["scopes"]) != "[admin session task ticket]" {
		t.Fatalf("caller %v", c)
	}
	if rules, _ := ov["rules"].([]any); len(rules) != 3 || !strings.Contains(fmt.Sprint(rules), "with ask") {
		t.Fatalf("rules %v", ov["rules"])
	}
	sections, _ := ov["sections"].([]any)
	if len(sections) != len(guideSections) {
		t.Fatalf("sections %v", sections)
	}
	for i, x := range sections {
		sec := x.(map[string]any)
		if sec["name"] != guideSections[i] {
			t.Fatalf("section %d %v", i, sec)
		}
		status, raw, out := guideOf(t, e, adminToken, "/api/guide?section="+guideSections[i])
		if status != http.StatusOK || out["section"] != guideSections[i] || out[guideSections[i]] == nil || out["version"] != version.Version {
			t.Fatalf("%s: %d %v", guideSections[i], status, out)
		}
		if sec["bytes"] != float64(len(raw)) {
			t.Fatalf("%s: the overview says %v bytes, the reply is %d", guideSections[i], sec["bytes"], len(raw))
		}
	}
	if status, _, out := guideOf(t, e, adminToken, "/api/guide?section=secrets"); status != http.StatusNotFound || errorCode(out) != "no_section" {
		t.Fatalf("unknown section: %d %v", status, out)
	}
	if status, _, out := guideOf(t, e, adminToken, "/api/guide?other=1"); status != http.StatusBadRequest || errorCode(out) != "invalid_request" {
		t.Fatalf("another parameter: %d %v", status, out)
	}
	if status, _, _ := guideOf(t, e, "", "/api/guide"); status != http.StatusUnauthorized {
		t.Fatalf("no token: %d", status)
	}
	// The limits are the constants that enforce them.
	_, _, out = guideOf(t, e, adminToken, "/api/guide?section=limits")
	limits := map[string]map[string]any{}
	for _, x := range out["limits"].([]any) {
		l := x.(map[string]any)
		limits[l["name"].(string)] = l
	}
	if limits["crew_tasks"]["max"] != float64(128) || limits["task_checks"]["max"] != float64(8) || limits["check_timeout"]["max"] != float64(3600) ||
		limits["check_timeout"]["min"] != float64(1) || limits["comment"]["max"] != float64(16<<10) || limits["inbox"]["max"] != float64(50) || limits["task_id"]["pattern"] != `^[a-z0-9][a-z0-9_-]{0,39}$` {
		t.Fatalf("limits %v", limits)
	}
}

// Each operation of the table is a route the mux has: its method and
// pattern answer something other than the mux's own 404 or 405.
func TestEveryOperationIsARegisteredRoute(t *testing.T) {
	e := newTestEnv(t, nil)
	e.srv.limiter = newRateLimiter(1e6, 1e6)
	wild := regexp.MustCompile(`\{[a-z]+\}`)
	for _, op := range Operations() {
		path := wild.ReplaceAllString(op.Path, "x")
		var body any
		if op.Method == http.MethodPost {
			body = []byte("{}")
		}
		status, data, out := e.callAs(t, adminToken, op.Method, path, body)
		if status == http.StatusMethodNotAllowed || (status == http.StatusNotFound && apiMessage(out) == "no such route") {
			t.Errorf("%s %s %s: %d %.200s", op.Name, op.Method, op.Path, status, data)
		}
	}
}

// muxPatterns is the patterns the package's code registers under prefix,
// read from its source as the mux is given them.
func muxPatterns(t *testing.T, prefix string) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`"((?:GET|POST|PUT|PATCH|DELETE) ` + regexp.QuoteMeta(prefix) + `[^"]*)"`)
	var out []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			out = append(out, m[1])
		}
	}
	if len(out) == 0 {
		t.Fatal("no patterns found")
	}
	return out
}

// Every route under /api/sessions/{id}/ that a session's own token gets
// past its authentication is in the table, with a session's scope; and
// every session operation of the table is a route registered there.
func TestEverySessionRouteIsInTheTable(t *testing.T) {
	e := newTestEnv(t, nil)
	e.srv.limiter = newRateLimiter(1e6, 1e6)
	sid := e.createSession("cat")
	tok := e.agentToken(sid)
	table := map[string]Operation{}
	for _, op := range Operations() {
		table[op.Method+" "+op.Path] = op
	}
	registered := map[string]bool{}
	wild := regexp.MustCompile(`\{[a-zA-Z]+\}`)
	for _, pattern := range muxPatterns(t, "/api/sessions/{id}") {
		registered[pattern] = true
		method, path, _ := strings.Cut(pattern, " ")
		var body any
		if method != http.MethodGet {
			body = []byte("{}")
		}
		status, data, _ := e.callAs(t, tok, method, wild.ReplaceAllString(strings.Replace(path, "{id}", sid, 1), "x"), body)
		if status == http.StatusUnauthorized {
			continue
		}
		op, ok := table[pattern]
		if !ok || op.Scope == "admin" {
			t.Errorf("%s: the session's token reaches it (%d %.200s), the table has %+v", pattern, status, data, op.Name)
		}
	}
	for key, op := range table {
		if op.Scope != "admin" && !registered[key] {
			t.Errorf("%s: %s is no route the server registers", op.Name, key)
		}
	}
}

// The largest guide the table can make: every operation's summary and
// description at their bounds, more agents than the host section lists
// with their names at theirs, a session caller of every kind, keeps
// each section, the overview and the whole within their bounds.
func TestEachSectionIsBounded(t *testing.T) {
	e := newTestEnv(t, nil)
	var list []catalog.Agent
	for i := range maxGuideAgents + 10 {
		list = append(list, catalog.Agent{ID: fmt.Sprintf("%032d", i), Name: strings.Repeat("n", 60), Adapter: strings.Repeat("a", 32), Command: []string{"/bin/cat"}})
	}
	cat, err := catalog.Load(catalog.File{DisableDefaults: true, Agents: list})
	if err != nil {
		t.Fatal(err)
	}
	e.setCatalog(cat)
	ops := Operations()
	for i := range ops {
		ops[i].Summary = strings.Repeat("s", maxOpSummary)
		ops[i].Description = strings.Repeat("d", maxOpDescription)
	}
	long := strings.Repeat("x", 64)
	callers := []guideCaller{adminCaller(), {Kind: "session", Session: long, Kinds: []string{"run_member", "attempt", "ticket_member"}, Run: long, Member: strings.Repeat("m", 40), Scopes: []string{"session", "task", "ticket"}, local: true}}
	for _, c := range callers {
		replies, err := e.srv.guideReplies(t.Context(), c, ops)
		if err != nil {
			t.Fatal(err)
		}
		whole := 0
		for name, b := range replies {
			whole += len(b)
			limit := maxGuideSection
			if name == "overview" {
				limit = maxGuideOverview
			}
			if len(b) > limit {
				t.Errorf("%s's %s: %d bytes, over %d", c.Kind, name, len(b), limit)
			}
			t.Logf("%s's %s: %d bytes", c.Kind, name, len(b))
		}
		if whole > maxGuideWhole {
			t.Errorf("%s's guide: %d bytes, over %d", c.Kind, whole, maxGuideWhole)
		}
		var host struct {
			Host struct {
				Agents     []any `json:"agents"`
				MoreAgents int   `json:"moreAgents"`
			} `json:"host"`
		}
		if err := json.Unmarshal(replies["host"], &host); err != nil || len(host.Host.Agents) != maxGuideAgents || host.Host.MoreAgents != 10 {
			t.Fatalf("%s's host: %d agents, %d more, %v", c.Kind, len(host.Host.Agents), host.Host.MoreAgents, err)
		}
	}
}

// A session's guide lists what its scopes reach: a plain session the
// session's operations and no task, ticket or admin one; an attempt of a
// managed run the task tools too; a member of a run a ticket holds the
// ticket's two. The admin reads a session's guide as the session's.
func TestASessionsGuideListsItsScopesOnly(t *testing.T) {
	sessionOps := []string{"report", "set_state", "ask", "form_crew", "add_member", "run_status", "link", "guide"}
	taskOps := []string{"task_context", "task_result", "message_send", "inbox_read", "inbox_ack", "task_blocked", "task_checkpoint"}
	ticketOps := []string{"ticket_read", "ticket_comment"}
	check := func(t *testing.T, e *testEnv, sid, tok string, kinds []string, want []string) {
		t.Helper()
		for _, token := range []string{tok, adminToken} {
			status, _, out := guideOf(t, e, token, "/api/sessions/"+sid+"/guide?section=operations")
			if got := opNames(t, out); status != http.StatusOK || !slices.Equal(got, want) {
				t.Fatalf("operations %d %v, want %v", status, got, want)
			}
			_, _, out = guideOf(t, e, token, "/api/sessions/"+sid+"/guide")
			c := out["overview"].(map[string]any)["caller"].(map[string]any)
			if c["kind"] != "session" || c["session"] != sid || fmt.Sprint(c["kinds"]) != fmt.Sprint(kinds) {
				t.Fatalf("caller %v, want kinds %v", c, kinds)
			}
		}
	}
	t.Run("plain", func(t *testing.T) {
		e := newTestEnv(t, nil)
		sid := e.createSession("cat")
		check(t, e, sid, e.agentToken(sid), []string{"plain"}, sessionOps)
	})
	t.Run("attempt", func(t *testing.T) {
		e := readyEnv(t, nil)
		e.gitRepo(t)
		run, release, err := e.srv.runs.LaunchNamed(t.Context(), managedCrew(e.root), "")
		release()
		if err != nil {
			t.Fatal(err)
		}
		sid := runningAttempt(t, e, run.ID, "build", 1)["sessionId"].(string)
		check(t, e, sid, e.agentToken(sid), []string{"run_member", "attempt"}, slices.Concat(sessionOps[:7], taskOps, []string{"guide"}))
	})
	t.Run("ticket", func(t *testing.T) {
		e := dispatchEnv(t)
		run, sid, tok := e.memberSession(t, "Ticketed")
		check(t, e, sid, tok, []string{"run_member"}, sessionOps)
		e.newDispatchProject(t, "GDE", nil)
		key := e.newDispatchTicket(t, "GDE", "Guided", nil)["key"].(string)
		e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/links", map[string]any{"opId": opID(), "kind": "run", "ref": run}, http.StatusCreated, "")
		check(t, e, sid, tok, []string{"run_member", "ticket_member"}, slices.Concat(sessionOps[:7], ticketOps, []string{"guide"}))
	})
	t.Run("self-service off", func(t *testing.T) {
		e := newTestEnv(t, func(c *config.Config) { off := false; c.Agents.SelfService = &off })
		sid := e.createSession("cat")
		check(t, e, sid, e.agentToken(sid), []string{"plain"}, []string{"report", "set_state", "ask", "guide"})
	})
}

// A session's guide takes its own agent token or the admin's: another
// session's token, a share token of it, a host token or none are
// refused, and an agent token reaches no admin guide.
func TestTheGuideRefusesAnotherSessionsToken(t *testing.T) {
	e := newTestEnv(t, nil)
	e.srv.limiter = newRateLimiter(1e6, 1e6)
	a, b := e.createSession("cat"), e.createSession("cat")
	aTok, bTok := e.agentToken(a), e.agentToken(b)
	_, share, err := e.srv.links.Create(a, session.RoleControl, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{bTok, share, "test-host-token", "", "wrong"} {
		if status, _, out := guideOf(t, e, tok, "/api/sessions/"+a+"/guide"); status != http.StatusUnauthorized {
			t.Fatalf("%q: %d %v", tok, status, out)
		}
	}
	if status, _, _ := guideOf(t, e, aTok, "/api/sessions/"+a+"/guide"); status != http.StatusOK {
		t.Fatalf("its own token: %d", status)
	}
	if status, _, _ := guideOf(t, e, aTok, "/api/guide"); status != http.StatusUnauthorized {
		t.Fatalf("an agent token on the admin's guide: %d", status)
	}
	if status, _, out := guideOf(t, e, adminToken, "/api/sessions/nobody/guide"); status != http.StatusNotFound || errorCode(out) != "not_found" {
		t.Fatalf("no session: %d %v", status, out)
	}
}

// A switchyard answers its own guide: no Dispatch, no agents, nothing
// that launches (its ledger, which it opens beside its records, holds no
// managed run); a session published to it reads its own with its token,
// the operations a hosted session has.
func TestASwitchyardAnswersItsOwnGuide(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.Switchyard.Enabled = true })
	status, _, out := guideOf(t, e, adminToken, "/api/guide?section=host")
	host, _ := out["host"].(map[string]any)
	if status != http.StatusOK || host["switchyard"] != true || host["dispatch"].(map[string]any)["state"] != "absent" || len(host["agents"].([]any)) != 0 || host["sessions"] != float64(0) {
		t.Fatalf("host: %d %v", status, out)
	}
	_, _, out = guideOf(t, e, adminToken, "/api/guide?section=operations")
	if got := opNames(t, out); !slices.Equal(got, []string{"report", "set_state", "ask", "guide", "server_guide"}) {
		t.Fatalf("the admin's operations %v", got)
	}
	h := dialFakeHost(t, e, "hosted-agent-token")
	status, _, out = guideOf(t, e, "hosted-agent-token", "/api/sessions/"+h.sessionID+"/guide?section=operations")
	if got := opNames(t, out); status != http.StatusOK || !slices.Equal(got, []string{"report", "set_state", "ask", "guide"}) {
		t.Fatalf("the hosted session's operations %d %v", status, got)
	}
	// The switchyard's workbench token has no role on a published session.
	if status, _, _ := guideOf(t, e, adminToken, "/api/sessions/"+h.sessionID+"/guide"); status != http.StatusUnauthorized {
		t.Fatalf("the switchyard's admin on a hosted session: %d", status)
	}
}

// Until the server names its providers (Program item C9), the guide says
// it does not know them, for the admin and for a session.
func TestProvidersAreUnknownBeforeC9(t *testing.T) {
	e := newTestEnv(t, nil)
	sid := e.createSession("cat")
	for path, tok := range map[string]string{"/api/guide": adminToken, "/api/sessions/" + sid + "/guide": e.agentToken(sid)} {
		status, raw, _ := guideOf(t, e, tok, path+"?section=providers")
		if status != http.StatusOK || !strings.Contains(string(raw), `"providers":{"known":false,"providers":[]}`) {
			t.Fatalf("%s: %d %s", path, status, raw)
		}
	}
}
